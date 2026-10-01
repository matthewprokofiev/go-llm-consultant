package telegram

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const (
	// Telegram в России замедлен: сообщение владельцу может уйти не с первого раза.
	// Повторяем с растущей паузой 2с, 4с, 8с… до минуты, всего до 5 минут.
	deliveryFirstDelay = 2 * time.Second
	deliveryMaxDelay   = time.Minute
	deliveryWindow     = 5 * time.Minute
	attemptTimeout     = 20 * time.Second

	// replyLinkTTL — сколько бот помнит, чья это заявка, чтобы переслать ответ владельца.
	replyLinkTTL = 48 * time.Hour
)

type sender interface {
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
}

type replyLink struct {
	chatID  int64
	expires time.Time
}

// delivery отправляет заявки и вопросы владельцу и помнит, какое сообщение владельцу
// к какому чату относится. Всё — в памяти: после перезапуска связи теряются, и бот
// честно говорит об этом владельцу.
type delivery struct {
	api         sender
	ownerChatID int64
	redact      func(error) string
	log         *slog.Logger
	wait        func(ctx context.Context, d time.Duration) error
	now         func() time.Time

	wg    sync.WaitGroup
	mu    sync.Mutex
	links map[int]replyLink
}

func newDelivery(api sender, ownerChatID int64, redact func(error) string, log *slog.Logger) *delivery {
	return &delivery{
		api:         api,
		ownerChatID: ownerChatID,
		redact:      redact,
		log:         log,
		wait:        sleep,
		now:         time.Now,
		links:       map[int]replyLink{},
	}
}

// start отправляет сообщение владельцу в фоне. done вызывается ровно один раз с
// итогом — в том числе при штатной остановке бота, чтобы человек не остался без ответа.
func (d *delivery) start(ctx context.Context, userChatID int64, text string, done func(delivered bool)) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		done(d.deliver(ctx, userChatID, text))
	}()
}

func (d *delivery) deliver(ctx context.Context, userChatID int64, text string) bool {
	delay := deliveryFirstDelay
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		msg, err := d.api.SendMessage(attemptCtx, &bot.SendMessageParams{ChatID: d.ownerChatID, Text: text})
		cancel()
		if err == nil {
			d.link(msg.ID, userChatID)
			d.log.Info("сообщение владельцу доставлено", "attempt", attempt)
			return true
		}
		d.log.Warn("сообщение владельцу не доставлено", "attempt", attempt, "error", d.redact(err))

		// Окно считается по паузам: время самих попыток ограничено attemptTimeout.
		if waited+delay > deliveryWindow || d.wait(ctx, delay) != nil {
			return false
		}
		waited += delay
		delay = min(delay*2, deliveryMaxDelay)
	}
}

// waitAll дожидается окончания всех отправок: при остановке бот сначала сообщает
// людям о недоставленных заявках и только потом выходит.
func (d *delivery) waitAll() { d.wg.Wait() }

func (d *delivery) link(messageID int, chatID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	for id, l := range d.links {
		if now.After(l.expires) {
			delete(d.links, id)
		}
	}
	d.links[messageID] = replyLink{chatID: chatID, expires: now.Add(replyLinkTTL)}
}

// target — чей чат стоит за сообщением владельцу.
func (d *delivery) target(messageID int) (int64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	l, ok := d.links[messageID]
	if !ok || d.now().After(l.expires) {
		return 0, false
	}
	return l.chatID, true
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
