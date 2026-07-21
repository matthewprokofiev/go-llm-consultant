package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/matveiprokofev/go-llm-consultant/internal/config"
	"github.com/matveiprokofev/go-llm-consultant/internal/knowledge"
	"github.com/matveiprokofev/go-llm-consultant/internal/llm"
	"github.com/matveiprokofev/go-llm-consultant/internal/storage"
	"github.com/matveiprokofev/go-llm-consultant/internal/telegram"
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

	if err := storage.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return fmt.Errorf("миграции: %w", err)
	}

	store, err := storage.New(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer store.Close()

	kb, err := knowledge.New(cfg.KnowledgePath, log)
	if err != nil {
		return err
	}

	llmClient, err := llm.New(cfg, log)
	if err != nil {
		return err
	}

	consultant := telegram.NewConsultant(llmClient, kb, cfg.Provider, log)

	b, err := telegram.New(cfg.BotToken, consultant, store, kb, cfg.Provider, cfg.AdminTgID, log)
	if err != nil {
		return err
	}

	return b.Run(ctx)
}
