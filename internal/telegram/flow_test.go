package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/matthewprokofiev/go-llm-consultant/internal/config"
	"github.com/matthewprokofiev/go-llm-consultant/internal/consultant"
	"github.com/matthewprokofiev/go-llm-consultant/internal/knowledge"
	"github.com/matthewprokofiev/go-llm-consultant/internal/llm"
)

// Сквозные проверки идут через настоящую библиотеку Telegram: апдейты подаются в
// ProcessUpdate, а запросы бота принимает поддельный сервер. Так проверяется
// маршрутизация команд, кнопок и колбэков, которую юнит-тесты функций не видят.

const (
	testToken   = "123:SECRET"
	userChat    = int64(42)
	ownerChat   = int64(-100500)
	botUserID   = int64(123)
	testContact = "📞 +7 900 000-00-00"
)

type sent struct {
	id     int
	method string
	chatID string
	text   string
	markup string
}

type fakeTelegram struct {
	mu     sync.Mutex
	calls  []sent
	nextID int
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseMultipartForm(1 << 20)
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	f.mu.Lock()
	f.nextID++
	id := f.nextID
	f.calls = append(f.calls, sent{id: id, method: method, chatID: r.FormValue("chat_id"), text: r.FormValue("text"), markup: r.FormValue("reply_markup")})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if method == "sendMessage" {
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":0,"chat":{"id":%s,"type":"private"}}}`, id, r.FormValue("chat_id"))
		return
	}
	_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
}

// messages — отправленные сообщения в чат, по порядку.
func (f *fakeTelegram) messages(chatID int64) []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sent
	for _, c := range f.calls {
		if c.method == "sendMessage" && c.chatID == fmt.Sprint(chatID) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTelegram) last(t *testing.T, chatID int64) sent {
	t.Helper()
	msgs := f.messages(chatID)
	if len(msgs) == 0 {
		t.Fatalf("в чат %d ничего не отправлено", chatID)
	}
	return msgs[len(msgs)-1]
}

// scriptedLLM отвечает по первому совпавшему ключу в вопросе.
type scriptedLLM map[string]string

func (s scriptedLLM) Ask(_ context.Context, messages []llm.Message) (llm.Answer, error) {
	q := messages[len(messages)-1].Content
	for key, answer := range s {
		if strings.Contains(q, key) {
			return llm.Answer{Text: answer, InputTokens: 100, OutputTokens: 10}, nil
		}
	}
	return llm.Answer{Text: "СТАТУС: ОТВЕТ\nОтвет по базе."}, nil
}

// newTestBot поднимает бота на поддельном Telegram с настоящей базой знаний во
// временном файле; третий результат — путь к этому файлу.
func newTestBot(t *testing.T, answers scriptedLLM) (*Bot, *fakeTelegram, string) {
	t.Helper()
	tg := &fakeTelegram{}
	srv := httptest.NewServer(tg)
	t.Cleanup(srv.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	kbPath := filepath.Join(t.TempDir(), "faq.md")
	if err := os.WriteFile(kbPath, []byte("# Прайс\nПортрет — 3 500 ₽\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kb, err := knowledge.New(kbPath, log)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		BotToken:         testToken,
		OwnerChatID:      ownerChat,
		BusinessContacts: testContact,
		Location:         time.UTC,
		LeadOfferEnabled: true,
		Texts: config.Texts{
			Greeting: "Здравствуйте!", ButtonLead: "📝 Оставить заявку", ButtonContacts: "📍 Контакты",
			LeadSent: "Заявка передана.", QuestionSent: "Вопрос передан.", DeliveryFailed: "Не получилось.",
			OwnerReplyPrefix: "Вам ответил администратор:",
		},
	}
	c := consultant.New(answers, kb, "Фотостудия", "test", log)
	b, err := New(cfg, c, kb, nil, log, bot.WithServerURL(srv.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	if err != nil {
		t.Fatal(err)
	}
	return b, tg, kbPath
}

func private(text string) *models.Update {
	msg := &models.Message{ID: 1, Chat: models.Chat{ID: userChat, Type: models.ChatTypePrivate}, From: &models.User{ID: userChat}, Text: text}
	if strings.HasPrefix(text, "/") {
		msg.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: len(strings.Fields(text)[0])}}
	}
	return &models.Update{Message: msg}
}

func press(data string) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{ID: "cb", Data: data, Message: models.MaybeInaccessibleMessage{
		Type:    models.MaybeInaccessibleMessageTypeMessage,
		Message: &models.Message{ID: 7, Chat: models.Chat{ID: userChat, Type: models.ChatTypePrivate}},
	}}}
}

func TestStartShowsGreetingAndMenu(t *testing.T) {
	b, tg, _ := newTestBot(t, nil)
	b.api.ProcessUpdate(context.Background(), private("/start"))

	got := tg.last(t, userChat)
	if got.text != "Здравствуйте!" || !strings.Contains(got.markup, "Оставить заявку") || !strings.Contains(got.markup, "Контакты") {
		t.Errorf("на /start ожидалось приветствие с меню, получено %+v", got)
	}
}

func TestUnansweredQuestionReachesOwnerAndOwnerReplyComesBack(t *testing.T) {
	b, tg, _ := newTestBot(t, scriptedLLM{"скидка": "СТАТУС: НЕТ_В_БАЗЕ\nВ прайсе этого нет."})
	ctx := context.Background()

	b.api.ProcessUpdate(ctx, private("А студенческая скидка есть?"))
	answer := tg.last(t, userChat)
	if answer.text != "В прайсе этого нет." || !strings.Contains(answer.markup, cbHandoff) {
		t.Fatalf("ожидался честный ответ с кнопкой передачи, получено %+v", answer)
	}

	b.api.ProcessUpdate(ctx, press(cbHandoff))
	if got := tg.last(t, userChat); !strings.Contains(got.text, "номер телефона") || !strings.Contains(got.markup, `"request_contact":true`) {
		t.Fatalf("ожидался запрос телефона с кнопкой контакта, получено %+v", got)
	}

	contact := private("")
	contact.Message.Contact = &models.Contact{PhoneNumber: "79001234567", FirstName: "Анна"}
	b.api.ProcessUpdate(ctx, contact)
	b.delivery.waitAll()

	owner := tg.last(t, ownerChat)
	for _, want := range []string{ownerHeaderQuestion, "А студенческая скидка есть?", "Анна", "+7 900 123-45-67"} {
		if !strings.Contains(owner.text, want) {
			t.Errorf("владельцу пришло без %q:\n%s", want, owner.text)
		}
	}
	if got := tg.last(t, userChat); got.text != "Вопрос передан." {
		t.Errorf("человеку ожидалось подтверждение, получено %q", got.text)
	}

	// Владелец отвечает reply на сообщение с вопросом — ответ уходит человеку.
	b.api.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		ID: 99, Chat: models.Chat{ID: ownerChat, Type: models.ChatTypeSupergroup}, Text: "Скидок нет, но есть акция по будням.",
		ReplyToMessage: &models.Message{ID: owner.id, From: &models.User{ID: botUserID, IsBot: true}, Text: owner.text},
	}})
	if got := tg.last(t, userChat); got.text != "Вам ответил администратор:\n\nСкидок нет, но есть акция по будням." {
		t.Errorf("ответ владельца не дошёл до человека: %q", got.text)
	}
	if got := tg.last(t, ownerChat); !strings.Contains(got.text, "Ответ отправлен") || !strings.Contains(got.markup, cbKBAdd) {
		t.Errorf("владелец должен увидеть подтверждение и предложение добавить в базу, получено %+v", got)
	}
}

// askOwner проводит вопрос без ответа до владельца и возвращает сообщение ему.
func askOwner(t *testing.T, b *Bot, tg *fakeTelegram, question string) sent {
	t.Helper()
	ctx := context.Background()
	b.api.ProcessUpdate(ctx, private(question))
	b.api.ProcessUpdate(ctx, press(cbHandoff))
	contact := private("")
	contact.Message.Contact = &models.Contact{PhoneNumber: "79001234567", FirstName: "Анна"}
	b.api.ProcessUpdate(ctx, contact)
	b.delivery.waitAll()
	return tg.last(t, ownerChat)
}

func ownerReply(to sent, text string) *models.Update {
	return &models.Update{Message: &models.Message{
		ID: 500 + to.id, Chat: models.Chat{ID: ownerChat, Type: models.ChatTypeSupergroup}, Text: text,
		ReplyToMessage: &models.Message{ID: to.id, From: &models.User{ID: botUserID, IsBot: true}, Text: to.text},
	}}
}

func ownerPress(data string, on sent) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{ID: "cb", Data: data, Message: models.MaybeInaccessibleMessage{
		Type:    models.MaybeInaccessibleMessageTypeMessage,
		Message: &models.Message{ID: on.id, Chat: models.Chat{ID: ownerChat, Type: models.ChatTypeSupergroup}, Text: on.text},
	}}}
}

func TestOwnerAnswerAddedToKnowledge(t *testing.T) {
	b, tg, kbPath := newTestBot(t, scriptedLLM{"парковк": "СТАТУС: НЕТ_В_БАЗЕ\nПро парковку в материалах нет."})
	ctx := context.Background()

	owner := askOwner(t, b, tg, "Есть парковка?")
	b.api.ProcessUpdate(ctx, ownerReply(owner, "Да, бесплатная, во дворе."))
	offer := tg.last(t, ownerChat)
	if !strings.Contains(offer.text, "Вопрос: Есть парковка?\nОтвет: Да, бесплатная, во дворе.") {
		t.Fatalf("в предложении нет вопроса и ответа:\n%s", offer.text)
	}

	b.api.ProcessUpdate(ctx, ownerPress(cbKBAdd, offer))
	raw, _ := os.ReadFile(kbPath)
	if !strings.Contains(string(raw), "# "+knowledge.OwnerAnswersTitle+"\n\n## Есть парковка?\nДа, бесплатная, во дворе.") {
		t.Errorf("ответ не записан в базу:\n%s", raw)
	}
	if got := tg.last(t, ownerChat); !strings.Contains(got.text, "Добавлено в базу") {
		t.Errorf("владелец должен увидеть подтверждение записи, получено %q", got.text)
	}
	if strings.Contains(string(raw), "Анна") || strings.Contains(string(raw), "900") {
		t.Errorf("в базу попали данные клиента:\n%s", raw)
	}
}

func TestOwnerRewordsAnswerForKnowledge(t *testing.T) {
	b, tg, kbPath := newTestBot(t, scriptedLLM{"скидк": "СТАТУС: НЕТ_В_БАЗЕ\nСкидок в прайсе нет."})
	ctx := context.Background()

	owner := askOwner(t, b, tg, "А студенческая скидка есть?")
	b.api.ProcessUpdate(ctx, ownerReply(owner, "Анна, для вас сделаем 10%!"))
	offer := tg.last(t, ownerChat)

	// Личный ответ владелец заменяет общей формулировкой — reply на предложение.
	b.api.ProcessUpdate(ctx, ownerReply(offer, "Студенческой скидки нет."))
	raw, _ := os.ReadFile(kbPath)
	if !strings.Contains(string(raw), "## А студенческая скидка есть?\nСтуденческой скидки нет.") || strings.Contains(string(raw), "10%") {
		t.Errorf("в базу должна попасть новая формулировка, а не личный ответ:\n%s", raw)
	}
}

func TestOwnerSkipsKnowledge(t *testing.T) {
	b, tg, kbPath := newTestBot(t, scriptedLLM{"суббот": "СТАТУС: НЕТ_В_БАЗЕ\nНе знаю."})
	ctx := context.Background()
	before, _ := os.ReadFile(kbPath)

	owner := askOwner(t, b, tg, "Свободна ли эта суббота?")
	b.api.ProcessUpdate(ctx, ownerReply(owner, "Да, в 14:00 свободно."))
	b.api.ProcessUpdate(ctx, ownerPress(cbKBSkip, tg.last(t, ownerChat)))

	after, _ := os.ReadFile(kbPath)
	if string(before) != string(after) {
		t.Errorf("после «Не добавлять» база изменилась:\n%s", after)
	}
}

func TestLeadReplyHasNoKnowledgeOffer(t *testing.T) {
	b, tg, _ := newTestBot(t, nil)
	ctx := context.Background()

	b.api.ProcessUpdate(ctx, private("📝 Оставить заявку"))
	b.api.ProcessUpdate(ctx, private("Портрет"))
	b.api.ProcessUpdate(ctx, private("89001234567"))
	b.api.ProcessUpdate(ctx, private("Анна"))
	b.delivery.waitAll()

	b.api.ProcessUpdate(ctx, ownerReply(tg.last(t, ownerChat), "Перезвоню через час."))
	if got := tg.last(t, ownerChat); got.text != "✅ Ответ отправлен клиенту." || got.markup != "" {
		t.Errorf("ответ на заявку личный — в базу не предлагаем, получено %+v", got)
	}
}

func TestLeadViaButtonWithQuestionInTheMiddle(t *testing.T) {
	b, tg, _ := newTestBot(t, scriptedLLM{"адрес": "СТАТУС: ОТВЕТ\nул. Примерная, 12."})
	ctx := context.Background()

	b.api.ProcessUpdate(ctx, private("📝 Оставить заявку"))
	b.api.ProcessUpdate(ctx, private("Семейная съёмка в субботу"))
	b.api.ProcessUpdate(ctx, private("А какой у вас адрес?"))

	msgs := tg.messages(userChat)
	if len(msgs) < 2 || msgs[len(msgs)-2].text != "ул. Примерная, 12." || !strings.Contains(msgs[len(msgs)-1].text, "номер телефона") {
		t.Fatalf("на вопрос посреди заявки бот должен ответить и вернуться к телефону: %+v", msgs)
	}

	b.api.ProcessUpdate(ctx, private("8 900 123-45-67"))
	b.api.ProcessUpdate(ctx, private("Анна"))
	b.delivery.waitAll()

	owner := tg.last(t, ownerChat)
	if !strings.Contains(owner.text, ownerHeaderLead) || !strings.Contains(owner.text, "Семейная съёмка в субботу") || !strings.Contains(owner.text, "Имя: Анна") {
		t.Errorf("заявка владельцу:\n%s", owner.text)
	}
	if got := tg.last(t, userChat); got.text != "Заявка передана." {
		t.Errorf("человеку ожидалось подтверждение, получено %q", got.text)
	}
}

func TestOffTopicAndDeclineGiveNoOwnerMessages(t *testing.T) {
	b, tg, _ := newTestBot(t, scriptedLLM{
		"анекдот":  "СТАТУС: НЕ_ПО_ТЕМЕ\nДавайте лучше о съёмках.",
		"парковка": "СТАТУС: НЕТ_В_БАЗЕ\nПро парковку в материалах нет.",
	})
	ctx := context.Background()

	b.api.ProcessUpdate(ctx, private("Расскажи анекдот"))
	if got := tg.last(t, userChat); got.markup != "" {
		t.Errorf("на вопрос не по теме — без кнопок передачи, получено %+v", got)
	}

	b.api.ProcessUpdate(ctx, private("Есть парковка?"))
	b.api.ProcessUpdate(ctx, press(cbHandoffNo))
	if got := tg.last(t, userChat); !strings.Contains(got.text, testContact) {
		t.Errorf("при отказе дать контакт бот должен дать контакты бизнеса, получено %q", got.text)
	}
	if n := len(tg.messages(ownerChat)); n != 0 {
		t.Errorf("владельцу ушло %d сообщений, ожидалось 0", n)
	}
}

func TestIntentOfferedOncePerConversation(t *testing.T) {
	b, tg, _ := newTestBot(t, scriptedLLM{"суббот": "СТАТУС: ОТВЕТ\nНАМЕРЕНИЕ: ДА\nСемейная — 5 000 ₽."})
	ctx := context.Background()

	b.api.ProcessUpdate(ctx, private("Сколько стоит съёмка и можно ли на субботу?"))
	if got := tg.last(t, userChat); !strings.Contains(got.markup, `"callback_data":"lead"`) {
		t.Fatalf("при намерении ожидалось предложение заявки, получено %+v", got)
	}
	b.api.ProcessUpdate(ctx, private("А на субботу вечером?"))
	if got := tg.last(t, userChat); got.markup != "" {
		t.Errorf("повторно в том же разговоре заявку не предлагаем, получено %+v", got)
	}
}

func TestReplyMarkupIsValidJSON(t *testing.T) {
	// Разметка уходит в Telegram строкой JSON — битая разметка молча ломает кнопки.
	b, tg, _ := newTestBot(t, nil)
	b.api.ProcessUpdate(context.Background(), private("/start"))
	var v map[string]any
	if err := json.Unmarshal([]byte(tg.last(t, userChat).markup), &v); err != nil {
		t.Errorf("reply_markup не JSON: %v", err)
	}
}
