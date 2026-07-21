// Package llm — тонкие клиенты к российским LLM (GigaChat, YandexGPT) за одним
// интерфейсом. Клиенты написаны на стандартном net/http: зависимость ради двух
// HTTP-вызовов не окупается, а свой код проще аудировать (TLS, кэш токена).
package llm

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/matveiprokofev/go-llm-consultant/internal/config"
)

// Answer — ответ модели вместе с фактическим расходом токенов из поля usage.
// TokensUsed == 0 означает, что провайдер usage не вернул: вызывающий сам решает,
// оценивать ли расход эвристикой.
type Answer struct {
	Text       string
	TokensUsed int
}

// LLMClient — то, что видит бот: задать вопрос с системным промптом (базой знаний)
// и сообщением пользователя, получить ответ. Возврат — структура, а не голая строка:
// провайдеры отдают точный usage.total_tokens, и терять его ради узкого интерфейса
// значило бы врать журналу диалогов (система токенов GigaChat freemium ограничена).
type LLMClient interface {
	Ask(ctx context.Context, systemPrompt, userMessage string) (Answer, error)
}

// New выбирает реализацию по конфигу. Провайдер уже провалидирован в config.Load,
// поэтому default здесь — защита от рассинхрона, а не пользовательский путь.
func New(cfg config.Config, log *slog.Logger) (LLMClient, error) {
	switch cfg.Provider {
	case config.ProviderGigaChat:
		return NewGigaChat(cfg.GigaChat, log)
	case config.ProviderYandexGPT:
		return NewYandexGPT(cfg.Yandex, log)
	default:
		return nil, fmt.Errorf("неизвестный LLM-провайдер %q", cfg.Provider)
	}
}
