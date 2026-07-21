package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // регистрирует драйвер "pgx" для database/sql, нужен goose
	"github.com/pressly/goose/v3"

	"github.com/matveiprokofev/go-llm-consultant/migrations"
)

type Storage struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func New(ctx context.Context, databaseURL string, log *slog.Logger) (*Storage, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("разбор DATABASE_URL: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("создание пула соединений: %w", err)
	}

	if err := pingWithRetry(ctx, pool, log); err != nil {
		pool.Close()
		return nil, err
	}

	return &Storage{pool: pool, log: log}, nil
}

func (s *Storage) Close() { s.pool.Close() }

// pingWithRetry: Postgres может быть ещё не готов даже после healthcheck compose
// (или при локальном запуске без compose), поэтому первые соединения пробуем
// несколько раз, а не падаем сразу.
func pingWithRetry(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	const attempts = 10

	var lastErr error
	for i := 1; i <= attempts; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		lastErr = pool.Ping(pingCtx)
		cancel()

		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("подключение к БД прервано: %w", ctx.Err())
		}

		log.Debug("БД пока недоступна, повтор", "attempt", i, "error", lastErr)

		select {
		case <-ctx.Done():
			return fmt.Errorf("подключение к БД прервано: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("БД недоступна после %d попыток: %w", attempts, lastErr)
}

// Migrate прогоняет goose-миграции из embed.FS: бинарник самодостаточен, в образ
// не нужно копировать каталог migrations.
func Migrate(ctx context.Context, databaseURL string) (err error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("открытие соединения для миграций: %w", err)
	}
	defer func() {
		// Ошибку закрытия не проглатываем, но и не затираем ею ошибку самих миграций.
		if cerr := db.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("закрытие соединения миграций: %w", cerr)
		}
	}()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("установка диалекта goose: %w", err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("применение миграций: %w", err)
	}
	return nil
}
