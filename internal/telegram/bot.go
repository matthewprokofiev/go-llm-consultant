package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/matveiprokofev/go-llm-consultant/internal/storage"
)

// Store — журнал диалогов. За интерфейсом ради подмены в тестах и слабой связности.
type Store interface {
	SaveDialog(ctx context.Context, d storage.Dialog) error
}

type Bot struct {
	api        *bot.Bot
	consultant *Consultant
	store      Store
	kb         Knowledge
	provider   string
	adminID    int64
	log        *slog.Logger
}

// New поднимает бота и регистрирует обработчики. getMe на старте не пропускаем:
// битый токен лучше поймать сразу, а не при первом сообщении.
func New(token string, consultant *Consultant, store Store, kb Knowledge, provider string, adminID int64, log *slog.Logger) (*Bot, error) {
	b := &Bot{
		consultant: consultant,
		store:      store,
		kb:         kb,
		provider:   provider,
		adminID:    adminID,
		log:        log,
	}

	api, err := bot.New(token, bot.WithDefaultHandler(b.handleMessage))
	if err != nil {
		return nil, fmt.Errorf("инициализация Telegram-бота: %w", err)
	}
	b.api = api

	api.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeCommand, b.handleStart)
	api.RegisterHandler(bot.HandlerTypeMessageText, "/reload", bot.MatchTypeCommand, b.handleReload)

	return b, nil
}

// Run запускает long-polling. Блокирует до отмены ctx. Штатная остановка по сигналу
// (ctx отменён) — это не ошибка: иначе процесс завершался бы с кодом 1 на каждый
// Ctrl+C или `docker compose down`.
func (b *Bot) Run(ctx context.Context) error {
	b.log.Info("бот запущен", "provider", b.provider)
	b.api.Start(ctx)

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	b.log.Info("бот остановлен")
	return nil
}

func (b *Bot) handleStart(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	b.reply(ctx, update.Message.Chat.ID,
		"Здравствуйте! Я консультант компании. Задайте вопрос — отвечу на основе нашей базы знаний.")
}

// handleMessage — основной путь: свободный текст → ответ модели по базе знаний.
func (b *Bot) handleMessage(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	question := strings.TrimSpace(update.Message.Text)
	chatID := update.Message.Chat.ID

	if question == "" {
		return
	}
	// Неизвестные команды не уходят в LLM: иначе бот тратил бы токены на «/foo».
	if strings.HasPrefix(question, "/") {
		b.reply(ctx, chatID, "Не знаю такой команды. Просто задайте вопрос текстом.")
		return
	}

	// «печатает…»: ответ идёт 10–20 секунд, без индикатора кажется, что бот завис.
	if _, err := b.api.SendChatAction(ctx, &bot.SendChatActionParams{
		ChatID: chatID,
		Action: models.ChatActionTyping,
	}); err != nil {
		b.log.Debug("не удалось отправить chat action", "error", err)
	}

	answer, tokens, err := b.consultant.Answer(ctx, question)
	if err != nil {
		b.log.Error("ошибка получения ответа", "error", err)
		b.reply(ctx, chatID, userFacingError(err))
		return
	}
	if answer == "" {
		answer = "Извините, не могу сформулировать ответ. Попробуйте переформулировать вопрос."
	}

	b.reply(ctx, chatID, answer)
	b.saveDialog(ctx, update, question, answer, tokens)
}

func (b *Bot) handleReload(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	chatID := update.Message.Chat.ID

	if b.adminID == 0 || update.Message.From == nil || update.Message.From.ID != b.adminID {
		b.reply(ctx, chatID, "Команда доступна только администратору.")
		return
	}

	if err := b.kb.Reload(); err != nil {
		b.log.Error("не удалось перечитать базу знаний", "error", err)
		b.reply(ctx, chatID, "Не удалось перечитать базу знаний. Подробности в логах.")
		return
	}
	b.reply(ctx, chatID, "База знаний перечитана.")
}

// saveDialog пишет диалог в журнал best-effort: сбой логирования не должен
// отменять уже отправленный пользователю ответ.
func (b *Bot) saveDialog(ctx context.Context, update *models.Update, question, answer string, tokens int) {
	var userID int64
	if update.Message.From != nil {
		userID = update.Message.From.ID
	}

	err := b.store.SaveDialog(ctx, storage.Dialog{
		UserTgID:   userID,
		Question:   question,
		Answer:     answer,
		Provider:   b.provider,
		TokensUsed: tokens,
	})
	if err != nil {
		b.log.Error("не удалось сохранить диалог", "error", err)
	}
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if _, err := b.api.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	}); err != nil {
		b.log.Error("не удалось отправить сообщение", "chat_id", chatID, "error", err)
	}
}

// userFacingError переводит техническую ошибку в понятную реплику, не раскрывая
// внутренностей. Таймаут отделён от прочих сбоев — пользователю есть смысл повторить.
func userFacingError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "Извините, ответ занял слишком много времени. Попробуйте задать вопрос ещё раз."
	}
	return "Извините, сервис временно недоступен. Пожалуйста, попробуйте немного позже."
}
