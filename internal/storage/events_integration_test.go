package storage

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestEventsIntegration гоняет реальный путь записи и сводки в Postgres. Запускается
// только если задан TEST_DATABASE_URL — в CI без БД тест пропускается, а локально
// на compose-Postgres проверяет, что миграции и запросы согласованы.
func TestEventsIntegration(t *testing.T) {
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

	before, err := s.Summary(ctx, 7)
	if err != nil {
		t.Fatalf("Summary до записи: %v", err)
	}

	if err := s.Record(ctx, "not_found", 1200, 40); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Record(ctx, EventLeadSent, 0, 0); err != nil {
		t.Fatalf("Record: %v", err)
	}

	after, err := s.Summary(ctx, 7)
	if err != nil {
		t.Fatalf("Summary после записи: %v", err)
	}
	if after.Counts["not_found"] != before.Counts["not_found"]+1 || after.Counts[EventLeadSent] != before.Counts[EventLeadSent]+1 {
		t.Errorf("счётчики: до %v, после %v", before.Counts, after.Counts)
	}
	if after.InputTokens-before.InputTokens != 1200 || after.OutputTokens-before.OutputTokens != 40 {
		t.Errorf("токены: до %d/%d, после %d/%d", before.InputTokens, before.OutputTokens, after.InputTokens, after.OutputTokens)
	}

	var tables int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name = 'dialogs'`).Scan(&tables); err != nil {
		t.Fatalf("проверка схемы: %v", err)
	}
	if tables != 0 {
		t.Error("таблица dialogs с перепиской должна быть удалена миграцией")
	}
}
