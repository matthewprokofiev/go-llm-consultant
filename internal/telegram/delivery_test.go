package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type fakeSender struct {
	mu       sync.Mutex
	failures int // сколько первых попыток падает
	calls    int
}

func (f *fakeSender) SendMessage(_ context.Context, _ *bot.SendMessageParams) (*models.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failures {
		return nil, errors.New("connection reset")
	}
	return &models.Message{ID: 100 + f.calls}, nil
}

func testDelivery(api sender) (*delivery, *[]time.Duration) {
	d := newDelivery(api, -100, func(err error) string { return err.Error() }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var waits []time.Duration
	d.wait = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}
	return d, &waits
}

func TestDeliveryRetriesWithBackoff(t *testing.T) {
	api := &fakeSender{failures: 3}
	d, waits := testDelivery(api)

	if !d.deliver(context.Background(), 42, "заявка") {
		t.Fatal("после трёх сбоев четвёртая попытка должна пройти")
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	if len(*waits) != len(want) {
		t.Fatalf("паузы = %v, ожидались %v", *waits, want)
	}
	for i := range want {
		if (*waits)[i] != want[i] {
			t.Errorf("паузы = %v, ожидались %v", *waits, want)
		}
	}
	if chatID, ok := d.target(104); !ok || chatID != 42 {
		t.Errorf("ответ владельца на доставленное сообщение должен вести в чат 42, получено %d, %v", chatID, ok)
	}
}

func TestDeliveryGivesUpAfterWindow(t *testing.T) {
	api := &fakeSender{failures: 1000}
	d, waits := testDelivery(api)

	if d.deliver(context.Background(), 42, "заявка") {
		t.Fatal("при постоянных сбоях доставка должна сдаться")
	}
	var total time.Duration
	for _, w := range *waits {
		if w > deliveryMaxDelay {
			t.Errorf("пауза %v больше потолка %v", w, deliveryMaxDelay)
		}
		total += w
	}
	if total > deliveryWindow || total < deliveryWindow-deliveryMaxDelay {
		t.Errorf("суммарное ожидание %v, ожидалось около %v", total, deliveryWindow)
	}
}

func TestDeliveryStopsOnShutdown(t *testing.T) {
	api := &fakeSender{failures: 1000}
	d, _ := testDelivery(api)
	d.wait = sleep

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := make(chan bool, 1)
	d.start(ctx, 42, "заявка", func(delivered bool) { results <- delivered })
	d.waitAll()

	if delivered := <-results; delivered {
		t.Error("при остановке недоставленная заявка должна закончиться отказом")
	}
	if api.calls != 1 {
		t.Errorf("попыток = %d, при остановке повторять не нужно", api.calls)
	}
}

func TestReplyLinksExpire(t *testing.T) {
	d, _ := testDelivery(&fakeSender{})
	now := time.Now()
	d.now = func() time.Time { return now }

	d.link(1, 42)
	if _, ok := d.target(2); ok {
		t.Error("чужое сообщение не должно вести к клиенту")
	}
	now = now.Add(replyLinkTTL + time.Minute)
	if _, ok := d.target(1); ok {
		t.Error("связь старше 48 часов должна быть забыта")
	}
	d.link(3, 43)
	if len(d.links) != 1 {
		t.Errorf("устаревшие связи должны вычищаться, осталось %d", len(d.links))
	}
}
