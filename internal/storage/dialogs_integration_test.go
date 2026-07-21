package storage

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestSaveDialogIntegration гоняет реальный путь записи в Postgres. Запускается
// только если задан TEST_DATABASE_URL — в CI без БД тест пропускается, а локально
// на compose-Postgres проверяет, что миграция и INSERT согласованы.
func TestSaveDialogIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL не задан — пропускаем интеграционный тест")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("миграции: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(ctx, dsn, log)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	defer s.Close()

	d := Dialog{
		UserTgID:   424242,
		Question:   "Сколько идёт доставка?",
		Answer:     "От 2 до 7 рабочих дней.",
		Provider:   "gigachat",
		TokensUsed: 15,
	}
	if err := s.SaveDialog(ctx, d); err != nil {
		t.Fatalf("SaveDialog: %v", err)
	}

	var (
		question string
		provider string
		tokens   int
	)
	err = s.pool.QueryRow(ctx,
		`SELECT question, provider, tokens_used FROM dialogs WHERE user_tg_id = $1 ORDER BY created_at DESC LIMIT 1`,
		d.UserTgID).Scan(&question, &provider, &tokens)
	if err != nil {
		t.Fatalf("чтение записанного диалога: %v", err)
	}
	if question != d.Question || provider != d.Provider || tokens != d.TokensUsed {
		t.Errorf("прочитано (%q, %q, %d), ожидалось (%q, %q, %d)",
			question, provider, tokens, d.Question, d.Provider, d.TokensUsed)
	}
}
