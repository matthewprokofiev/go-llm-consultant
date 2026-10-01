package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/matthewprokofiev/go-llm-consultant/internal/config"
	"github.com/matthewprokofiev/go-llm-consultant/internal/consultant"
	"github.com/matthewprokofiev/go-llm-consultant/internal/knowledge"
	"github.com/matthewprokofiev/go-llm-consultant/internal/llm"
	"github.com/matthewprokofiev/go-llm-consultant/internal/storage"
	"github.com/matthewprokofiev/go-llm-consultant/internal/telegram"
)

func main() {
	if err := run(); err != nil {
		// Логгер к этому моменту мог быть ещё не создан (ошибка конфига), поэтому в stderr.
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := config.NewLogger(cfg.AppEnv)

	// Один ctx на весь процесс уходит в миграции, БД, LLM-запросы и long-polling.
	// По SIGINT/SIGTERM отменяется — и всё дерево операций сворачивается разом.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Без DATABASE_URL бот ничего не пишет; с ним — только обезличенную статистику.
	var stats *storage.Storage
	if cfg.DatabaseURL != "" {
		if err := storage.Migrate(ctx, cfg.DatabaseURL); err != nil {
			return fmt.Errorf("миграции: %w", err)
		}
		stats, err = storage.New(ctx, cfg.DatabaseURL, log)
		if err != nil {
			return err
		}
		defer stats.Close()
	}

	kb, err := knowledge.New(cfg.KnowledgePath, log)
	if err != nil {
		return err
	}

	llmClient, err := llm.New(cfg.LLM, log)
	if err != nil {
		return err
	}

	c := consultant.New(llmClient, kb, cfg.BusinessName, cfg.LLM.Provider, log)

	b, err := telegram.New(cfg, c, kb, stats, log)
	if err != nil {
		return err
	}

	return b.Run(ctx)
}
