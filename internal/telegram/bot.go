// Package telegram — транспорт: принимает сообщения, ведёт заявку, показывает
// кнопки и передаёт владельцу заявки и вопросы без ответа. Содержательный ответ
// даёт consultant. Ничего из переписки не сохраняется: состояние живёт в памяти.
package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/matthewprokofiev/go-llm-consultant/internal/config"
	"github.com/matthewprokofiev/go-llm-consultant/internal/consultant"
	"github.com/matthewprokofiev/go-llm-consultant/internal/storage"
)

const (
	buttonPhone  = "📱 Отправить номер"
	buttonCancel = "Отмена"
	buttonSkip   = "Пропустить"

	cbLead      = "lead"
	cbLeadNo    = "lead:no"
	cbHandoff   = "handoff"
	cbHandoffNo = "handoff:no"
	cbKBAdd     = "kb:add"
	cbKBSkip    = "kb:skip"

	replyTimeout = 20 * time.Second
)

// Knowledge — база знаний глазами бота: перечитать по /reload и дописать ответ,
// который одобрил владелец.
type Knowledge interface {
	Reload() error
	Append(question, answer string) error
}

type Bot struct {
	api        *bot.Bot
	cfg        config.Config
	consultant *consultant.Consultant
	kb         Knowledge
	stats      *storage.Storage // nil — статистика выключена
	sessions   *sessions
	delivery   *delivery
	redact     func(error) string
	log        *slog.Logger
}

// New поднимает бота и регистрирует обработчики. getMe на старте не пропускаем:
// битый токен лучше поймать сразу, а не при первом сообщении. opts — опции
// библиотеки поверх штатных; ими тесты подставляют поддельный сервер Telegram.
func New(cfg config.Config, c *consultant.Consultant, kb Knowledge, stats *storage.Storage, log *slog.Logger, opts ...bot.Option) (*Bot, error) {
	b := &Bot{
		cfg:        cfg,
		consultant: c,
		kb:         kb,
		stats:      stats,
		sessions:   newSessions(),
		redact:     newRedactor(cfg.BotToken),
		log:        log,
	}

	api, err := bot.New(cfg.BotToken, append([]bot.Option{
		bot.WithDefaultHandler(b.handleUpdate),
		bot.WithCallbackQueryDataHandler("", bot.MatchTypePrefix, b.handleCallback),
		// Штатный обработчик библиотеки печатает тело ответа Telegram целиком — а в
		// нём сообщения, имена и телефоны. Пишем только очищенную причину.
		bot.WithErrorsHandler(func(err error) { log.Error("ошибка Telegram", "error", b.redact(err)) }),
	}, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("инициализация Telegram-бота: %s", b.redact(err))
	}
	b.api = api
	b.delivery = newDelivery(api, cfg.OwnerChatID, b.redact, log)

	// MatchTypeCommand сравнивает команду без слэша: «/start» здесь не совпал бы никогда.
	api.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, b.handleStart)
	api.RegisterHandler(bot.HandlerTypeMessageText, "reload", bot.MatchTypeCommand, b.handleReload)
	api.RegisterHandler(bot.HandlerTypeMessageText, "stats", bot.MatchTypeCommand, b.handleStats)

	return b, nil
}

// Run запускает long-polling и блокирует до отмены ctx. После остановки ждёт
// незавершённые отправки владельцу: люди узнают, что заявка не ушла. Штатная
// остановка — не ошибка, иначе процесс выходил бы с кодом 1 на каждый Ctrl+C.
func (b *Bot) Run(ctx context.Context) error {
	b.log.Info("бот запущен", "provider", b.cfg.LLM.Provider, "model", b.cfg.LLM.Model(), "stats", b.stats != nil)
	go b.sessions.cleanupLoop(ctx)
	b.api.Start(ctx)
	b.delivery.waitAll()
	b.log.Info("бот остановлен")
	return nil
}

func (b *Bot) handleStart(ctx context.Context, _ *bot.Bot, update *models.Update) {
	msg := update.Message
	if msg == nil || msg.Chat.Type != models.ChatTypePrivate {
		return
	}
	s := b.sessions.acquire(msg.Chat.ID)
	defer s.mu.Unlock()
	s.resetConversation()
	b.send(ctx, msg.Chat.ID, b.cfg.Texts.Greeting, b.mainKeyboard())
}

func (b *Bot) handleUpdate(ctx context.Context, _ *bot.Bot, update *models.Update) {
	msg := update.Message
	if msg == nil {
		return
	}
	if msg.Chat.ID == b.cfg.OwnerChatID && msg.ReplyToMessage != nil &&
		msg.ReplyToMessage.From != nil && msg.ReplyToMessage.From.ID == b.api.ID() {
		b.relayOwnerReply(ctx, msg)
		return
	}
	// В группах бот только принимает ответы владельца, остальное там не его дело.
	if msg.Chat.Type != models.ChatTypePrivate {
		return
	}

	s := b.sessions.acquire(msg.Chat.ID)
	defer s.mu.Unlock()

	text := strings.TrimSpace(msg.Text)
	chatID := msg.Chat.ID
	switch {
	case s.form != nil:
		b.continueForm(ctx, s, chatID, formInput{text: text, contact: msg.Contact})
	case text == b.cfg.Texts.ButtonLead:
		b.startForm(ctx, s, chatID, &leadForm{kind: formLead, step: stepInterest})
	case text == b.cfg.Texts.ButtonContacts:
		b.send(ctx, chatID, b.cfg.BusinessContacts, nil)
	case text == "":
		// Стикеры, фото, голосовые: отвечаем только на текст.
	case strings.HasPrefix(text, "/"):
		// Неизвестные команды не уходят в LLM, чтобы не жечь токены на «/foo».
		b.send(ctx, chatID, "Не знаю такой команды. Просто задайте вопрос текстом.", nil)
	default:
		b.answer(ctx, s, chatID, text, true)
	}
}

type offer int

const (
	offerNone offer = iota
	offerHandoff
	offerLead
)

// chooseOffer: вопрос без ответа всегда можно передать владельцу, а заявку бот
// предлагает сам только при явном намерении и не чаще раза за разговор.
func chooseOffer(r consultant.Reply, leadOfferEnabled, offerShown bool) offer {
	switch {
	case r.Status == consultant.StatusNotFound || r.Status == consultant.StatusPartial:
		return offerHandoff
	case r.Status == consultant.StatusAnswer && r.Intent && leadOfferEnabled && !offerShown:
		return offerLead
	default:
		return offerNone
	}
}

// answer отвечает на вопрос по базе. withOffers=false — вопрос задан посреди заявки:
// ответить и вернуться к заявке, ничего нового не предлагая.
func (b *Bot) answer(ctx context.Context, s *session, chatID int64, question string, withOffers bool) {
	if !withinLimit(&s.questions, b.cfg.QuestionsPerHour, b.sessions.now()) {
		b.record(ctx, storage.EventLimited, 0, 0)
		b.send(ctx, chatID, "Вы задали много вопросов подряд, давайте сделаем паузу. Если срочно, свяжитесь с нами:\n"+b.cfg.BusinessContacts, nil)
		return
	}
	s.questions = append(s.questions, b.sessions.now())

	// «печатает…»: ответ идёт 10–20 секунд, без индикатора кажется, что бот завис.
	if _, err := b.api.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping}); err != nil {
		b.log.Debug("не удалось отправить chat action", "error", b.redact(err))
	}

	reply, err := b.consultant.Answer(ctx, s.history, question)
	if err != nil {
		b.log.Error("нейросеть не ответила", "error", err)
		b.record(ctx, storage.EventLLMError, 0, 0)
		b.send(ctx, chatID, "Извините, сейчас не могу ответить. Пожалуйста, свяжитесь с нами напрямую:\n"+b.cfg.BusinessContacts, nil)
		return
	}
	text := reply.Text
	if text == "" {
		text = "Извините, не могу сформулировать ответ. Попробуйте переформулировать вопрос."
	}
	s.remember(question, text)
	b.record(ctx, statusEvent(reply.Status), reply.InputTokens, reply.OutputTokens)

	var markup models.ReplyMarkup
	if withOffers {
		switch chooseOffer(reply, b.cfg.LeadOfferEnabled, s.offerShown) {
		case offerHandoff:
			s.pendingQuestion, s.pendingContext = question, s.previousQuestion()
			markup = inlineButtons("Передать вопрос администратору", cbHandoff, "Не нужно", cbHandoffNo)
		case offerLead:
			// Отказ или игнор — в этот разговор больше не предлагаем.
			s.offerShown = true
			s.pendingInterest = question
			markup = inlineButtons(b.cfg.Texts.ButtonLead, cbLead, "Не сейчас", cbLeadNo)
		}
	}
	b.send(ctx, chatID, text, markup)
}

func statusEvent(s consultant.Status) string {
	if s == consultant.StatusUnknown {
		return "unmarked"
	}
	return string(s)
}

func (b *Bot) handleCallback(ctx context.Context, _ *bot.Bot, update *models.Update) {
	cq := update.CallbackQuery
	if _, err := b.api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID}); err != nil {
		b.log.Debug("не удалось ответить на callback", "error", b.redact(err))
	}
	msg := cq.Message.Message
	if msg == nil {
		return
	}
	if msg.Chat.ID == b.cfg.OwnerChatID && (cq.Data == cbKBAdd || cq.Data == cbKBSkip) {
		b.handleKBChoice(ctx, cq.Data, msg)
		return
	}
	if msg.Chat.Type != models.ChatTypePrivate {
		return
	}
	chatID := msg.Chat.ID
	// Кнопки убираем сразу, чтобы их не нажали второй раз.
	b.removeButtons(ctx, chatID, msg.ID)

	s := b.sessions.acquire(chatID)
	defer s.mu.Unlock()

	switch cq.Data {
	case cbLead:
		f := &leadForm{kind: formLead, step: stepPhone, topic: s.pendingInterest}
		if f.topic == "" {
			f.step = stepInterest
		}
		b.startForm(ctx, s, chatID, f)
	case cbLeadNo:
		b.send(ctx, chatID, "Хорошо! Если появятся вопросы — пишите.", nil)
	case cbHandoff:
		if s.pendingQuestion == "" {
			b.send(ctx, chatID, "Напишите, пожалуйста, вопрос ещё раз.", nil)
			return
		}
		b.startForm(ctx, s, chatID, &leadForm{kind: formQuestion, step: stepPhone, topic: s.pendingQuestion, context: s.pendingContext})
	case cbHandoffNo:
		b.send(ctx, chatID, "Хорошо. Если удобнее связаться напрямую:\n"+b.cfg.BusinessContacts, nil)
	}
}

func (b *Bot) startForm(ctx context.Context, s *session, chatID int64, f *leadForm) {
	if !withinLimit(&s.leads, b.cfg.LeadsPerHour, b.sessions.now()) {
		b.record(ctx, storage.EventLimited, 0, 0)
		b.send(ctx, chatID, "Вы уже оставили несколько заявок — с вами обязательно свяжутся. Если срочно:\n"+b.cfg.BusinessContacts, b.mainKeyboard())
		return
	}
	s.pendingQuestion, s.pendingContext, s.pendingInterest = "", "", ""
	s.form = f
	b.promptStep(ctx, chatID, f)
}

func (b *Bot) continueForm(ctx context.Context, s *session, chatID int64, in formInput) {
	f := s.form
	switch {
	case in.text == b.cfg.Texts.ButtonLead:
		b.promptStep(ctx, chatID, f)
		return
	case in.text == b.cfg.Texts.ButtonContacts:
		b.send(ctx, chatID, b.cfg.BusinessContacts, nil)
		b.promptStep(ctx, chatID, f)
		return
	case in.text == buttonCancel:
		s.form = nil
		b.send(ctx, chatID, "Хорошо, отменил. Если появятся вопросы — пишите.", b.mainKeyboard())
		return
	case in.text == buttonSkip && f.kind == formQuestion && f.step == stepName:
		b.submit(ctx, s, chatID)
		return
	}

	switch f.apply(in) {
	case actPrompt:
		b.promptStep(ctx, chatID, f)
	case actBadPhone:
		b.send(ctx, chatID, "Кажется, в номере ошибка. Напишите его полностью, например +7 900 123-45-67, или нажмите «"+buttonPhone+"».", phoneKeyboard())
	case actQuestion:
		b.answer(ctx, s, chatID, in.text, false)
		b.promptStep(ctx, chatID, f)
	case actSubmit:
		b.submit(ctx, s, chatID)
	}
}

func (b *Bot) promptStep(ctx context.Context, chatID int64, f *leadForm) {
	switch f.step {
	case stepInterest:
		b.send(ctx, chatID, "Что вас интересует? Напишите в одном-двух предложениях.", keyboard(buttonCancel))
	case stepPhone:
		text := "Оставьте номер телефона, чтобы с вами связались: нажмите «" + buttonPhone + "» или напишите его."
		if consent := b.consentText(); consent != "" {
			text += "\n\n" + consent
		}
		b.send(ctx, chatID, text, phoneKeyboard())
	case stepName:
		if f.kind == formQuestion {
			b.send(ctx, chatID, "Как к вам обращаться? Можно пропустить.", keyboard(buttonSkip, buttonCancel))
			return
		}
		b.send(ctx, chatID, "Как к вам обращаться?", keyboard(buttonCancel))
	}
}

// consentText — строка согласия перед запросом телефона. Нет ни текста, ни ссылки
// в настройках — нет и строки.
func (b *Bot) consentText() string {
	parts := make([]string, 0, 2)
	if b.cfg.Texts.Consent != "" {
		parts = append(parts, b.cfg.Texts.Consent)
	}
	if b.cfg.Texts.PrivacyURL != "" {
		parts = append(parts, "Политика обработки данных: "+b.cfg.Texts.PrivacyURL)
	}
	return strings.Join(parts, "\n")
}

// submit передаёт заявку владельцу. Человек сразу видит «передаю», а подтверждение
// или честный отказ с контактами приходит, когда доставка закончится.
func (b *Bot) submit(ctx context.Context, s *session, chatID int64) {
	f := s.form
	s.form = nil
	now := b.sessions.now()
	s.leads = append(s.leads, now)

	b.send(ctx, chatID, "Передаю…", b.mainKeyboard())
	b.delivery.start(ctx, chatID, ownerMessage(f, now.In(b.cfg.Location)), func(delivered bool) {
		b.afterDelivery(chatID, f.kind, delivered)
	})
}

// afterDelivery работает со своим контекстом: при остановке бота процессный уже
// отменён, а человеку всё равно нужно сказать, что заявка не ушла.
func (b *Bot) afterDelivery(chatID int64, kind formKind, delivered bool) {
	ctx, cancel := context.WithTimeout(context.Background(), replyTimeout)
	defer cancel()

	sentEvent, failedEvent, sentText := storage.EventLeadSent, storage.EventLeadFailed, b.cfg.Texts.LeadSent
	if kind == formQuestion {
		sentEvent, failedEvent, sentText = storage.EventQuestionSent, storage.EventQuestionFailed, b.cfg.Texts.QuestionSent
	}
	if delivered {
		b.record(ctx, sentEvent, 0, 0)
		b.send(ctx, chatID, sentText, nil)
		return
	}
	b.log.Error("передать владельцу не удалось, человеку даны контакты")
	b.record(ctx, failedEvent, 0, 0)
	b.send(ctx, chatID, b.cfg.Texts.DeliveryFailed+"\n"+b.cfg.BusinessContacts, nil)
}

// relayOwnerReply пересылает человеку ответ владельца на заявку или вопрос. Ответ
// на вопрос без ответа бот предлагает добавить в базу знаний; reply на само
// предложение — это другая, общая формулировка для базы.
func (b *Bot) relayOwnerReply(ctx context.Context, msg *models.Message) {
	orig := msg.ReplyToMessage
	text := strings.TrimSpace(msg.Text)
	if question, _, ok := parseKBOffer(orig.Text); ok {
		if text == "" {
			b.replyTo(ctx, msg, "Для базы знаний нужен текст ответа.", nil)
			return
		}
		b.addToKnowledge(ctx, msg.Chat.ID, orig.ID, question, text)
		return
	}

	chatID, ok := b.delivery.target(orig.ID)
	if !ok {
		if isOwnerMessage(orig.Text) {
			b.replyTo(ctx, msg, "Не могу переслать ответ: бот перезапускался или прошло больше 48 часов. Позвоните клиенту по номеру из заявки.", nil)
		}
		return
	}
	if text == "" {
		b.replyTo(ctx, msg, "Пересылаю клиенту только текст.", nil)
		return
	}
	if _, err := b.api.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: b.cfg.Texts.OwnerReplyPrefix + "\n\n" + text}); err != nil {
		b.log.Warn("ответ владельца не доставлен", "error", b.redact(err))
		b.replyTo(ctx, msg, "Не удалось доставить ответ клиенту. Позвоните по номеру из заявки.", nil)
		return
	}
	b.log.Info("ответ владельца передан клиенту")

	if question := questionFromOwnerMessage(orig.Text); question != "" {
		b.replyTo(ctx, msg, kbOffer(question, text), inlineButtons("Добавить в базу", cbKBAdd, "Не добавлять", cbKBSkip))
		return
	}
	b.replyTo(ctx, msg, "✅ Ответ отправлен клиенту.", nil)
}

// handleKBChoice — владелец нажал кнопку под предложением добавить ответ в базу.
// Вопрос и ответ берутся из текста самого предложения.
func (b *Bot) handleKBChoice(ctx context.Context, data string, offer *models.Message) {
	if data == cbKBSkip {
		b.removeButtons(ctx, offer.Chat.ID, offer.ID)
		b.send(ctx, offer.Chat.ID, "Хорошо, в базу знаний не добавлено.", nil)
		return
	}
	question, answer, ok := parseKBOffer(offer.Text)
	if !ok {
		b.send(ctx, offer.Chat.ID, "Не удалось разобрать предложение. Добавьте ответ в базу вручную.", nil)
		return
	}
	b.addToKnowledge(ctx, offer.Chat.ID, offer.ID, question, answer)
}

// addToKnowledge дописывает одобренный владельцем ответ в базу; бот начинает
// отвечать по нему сразу, без /reload.
func (b *Bot) addToKnowledge(ctx context.Context, chatID int64, offerID int, question, answer string) {
	if err := b.kb.Append(question, answer); err != nil {
		b.log.Error("не удалось пополнить базу знаний", "error", err)
		b.send(ctx, chatID, "Не удалось записать в базу знаний. Подробности в логах бота.", nil)
		return
	}
	b.removeButtons(ctx, chatID, offerID)
	b.send(ctx, chatID, "✅ Добавлено в базу знаний. Бот уже отвечает так на похожие вопросы.", nil)
}

func (b *Bot) handleReload(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if !b.fromAdmin(ctx, update) {
		return
	}
	chatID := update.Message.Chat.ID
	if err := b.kb.Reload(); err != nil {
		b.log.Error("не удалось перечитать базу знаний", "error", err)
		b.send(ctx, chatID, "Не удалось перечитать базу знаний. Подробности в логах.", nil)
		return
	}
	b.send(ctx, chatID, "База знаний перечитана.", nil)
}

func (b *Bot) handleStats(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if !b.fromAdmin(ctx, update) {
		return
	}
	chatID := update.Message.Chat.ID
	if b.stats == nil {
		b.send(ctx, chatID, "Статистика выключена: бот запущен без базы данных (DATABASE_URL).", nil)
		return
	}
	week, err := b.stats.Summary(ctx, 7)
	if err == nil {
		var month storage.Summary
		if month, err = b.stats.Summary(ctx, 30); err == nil {
			b.send(ctx, chatID, formatStats(week, month), nil)
			return
		}
	}
	b.log.Error("не удалось собрать статистику", "error", err)
	b.send(ctx, chatID, "Не удалось собрать статистику. Подробности в логах.", nil)
}

func (b *Bot) fromAdmin(ctx context.Context, update *models.Update) bool {
	msg := update.Message
	if msg == nil {
		return false
	}
	if b.cfg.AdminTgID == 0 || msg.From == nil || msg.From.ID != b.cfg.AdminTgID {
		b.send(ctx, msg.Chat.ID, "Команда доступна только администратору.", nil)
		return false
	}
	return true
}

// formatStats — сводка для /stats: слева 7 дней, справа 30.
func formatStats(week, month storage.Summary) string {
	questionKinds := []string{"answer", "partial", "not_found", "off_topic", "unmarked", storage.EventLLMError}
	total := func(s storage.Summary) int {
		n := 0
		for _, k := range questionKinds {
			n += s.Counts[k]
		}
		return n
	}
	row := func(label string, w, m int) string { return fmt.Sprintf("%s: %d / %d\n", label, w, m) }

	var b strings.Builder
	b.WriteString("📊 Статистика: 7 дней / 30 дней\n\n")
	b.WriteString(row("Вопросов", total(week), total(month)))
	for _, r := range []struct{ label, kind string }{
		{"  ответ по базе", "answer"},
		{"  частично", "partial"},
		{"  нет в базе", "not_found"},
		{"  не по теме", "off_topic"},
		{"  без метки", "unmarked"},
		{"  нейросеть не ответила", storage.EventLLMError},
	} {
		b.WriteString(row(r.label, week.Counts[r.kind], month.Counts[r.kind]))
	}
	b.WriteString(row("Заявки доставлены", week.Counts[storage.EventLeadSent], month.Counts[storage.EventLeadSent]))
	b.WriteString(row("Заявки не доставлены", week.Counts[storage.EventLeadFailed], month.Counts[storage.EventLeadFailed]))
	b.WriteString(row("Вопросы владельцу доставлены", week.Counts[storage.EventQuestionSent], month.Counts[storage.EventQuestionSent]))
	b.WriteString(row("Вопросы владельцу не доставлены", week.Counts[storage.EventQuestionFailed], month.Counts[storage.EventQuestionFailed]))
	b.WriteString(row("Отказано по лимиту", week.Counts[storage.EventLimited], month.Counts[storage.EventLimited]))
	b.WriteString(row("Токены на вход", week.InputTokens, month.InputTokens))
	b.WriteString(row("Токены на выход", week.OutputTokens, month.OutputTokens))
	return b.String()
}

// record пишет событие статистики, если она включена. Сбой не мешает ответу.
func (b *Bot) record(ctx context.Context, kind string, inputTokens, outputTokens int) {
	if b.stats == nil {
		return
	}
	if err := b.stats.Record(ctx, kind, inputTokens, outputTokens); err != nil {
		b.log.Error("не удалось записать статистику", "error", err)
	}
}

func (b *Bot) send(ctx context.Context, chatID int64, text string, markup models.ReplyMarkup) {
	if _, err := b.api.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyMarkup: markup}); err != nil {
		b.log.Error("не удалось отправить сообщение", "error", b.redact(err))
	}
}

func (b *Bot) replyTo(ctx context.Context, msg *models.Message, text string, markup models.ReplyMarkup) {
	if _, err := b.api.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          msg.Chat.ID,
		Text:            text,
		ReplyParameters: &models.ReplyParameters{MessageID: msg.ID},
		ReplyMarkup:     markup,
	}); err != nil {
		b.log.Error("не удалось ответить владельцу", "error", b.redact(err))
	}
}

func (b *Bot) removeButtons(ctx context.Context, chatID int64, messageID int) {
	if _, err := b.api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{ChatID: chatID, MessageID: messageID}); err != nil {
		b.log.Debug("не удалось убрать кнопки", "error", b.redact(err))
	}
}

func (b *Bot) mainKeyboard() models.ReplyMarkup {
	return &models.ReplyKeyboardMarkup{
		Keyboard:       [][]models.KeyboardButton{{{Text: b.cfg.Texts.ButtonLead}, {Text: b.cfg.Texts.ButtonContacts}}},
		IsPersistent:   true,
		ResizeKeyboard: true,
	}
}

func phoneKeyboard() models.ReplyMarkup {
	return &models.ReplyKeyboardMarkup{
		Keyboard:       [][]models.KeyboardButton{{{Text: buttonPhone, RequestContact: true}}, {{Text: buttonCancel}}},
		ResizeKeyboard: true,
	}
}

func keyboard(buttons ...string) models.ReplyMarkup {
	row := make([]models.KeyboardButton, len(buttons))
	for i, t := range buttons {
		row[i] = models.KeyboardButton{Text: t}
	}
	return &models.ReplyKeyboardMarkup{Keyboard: [][]models.KeyboardButton{row}, ResizeKeyboard: true}
}

func inlineButtons(yes, yesData, no, noData string) models.ReplyMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: yes, CallbackData: yesData},
		{Text: no, CallbackData: noData},
	}}}
}

var responseBody = regexp.MustCompile(`(?s)(error decode (?:response|request) body(?: for method \S+?)?), .*`)

// newRedactor очищает ошибки Telegram перед логом: в URL запроса стоит токен бота,
// а при сбое разбора библиотека вкладывает в ошибку всё тело ответа — с текстами
// сообщений, именами и телефонами.
func newRedactor(token string) func(error) string {
	return func(err error) string {
		msg := err.Error()
		if token != "" {
			msg = strings.ReplaceAll(msg, token, "***")
		}
		return responseBody.ReplaceAllString(msg, "$1 (тело скрыто)")
	}
}
