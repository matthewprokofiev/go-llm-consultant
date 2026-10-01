// Package llm — тонкие клиенты к LLM за одним интерфейсом: GigaChat и YandexGPT
// как основные, плюс любой сервис с API как у OpenAI. Клиенты написаны на
// стандартном net/http: зависимость ради пары HTTP-вызовов не окупается, а свой
// код проще аудировать (TLS, кэш токена).
package llm

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/matthewprokofiev/go-llm-consultant/internal/config"
)

const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// temperature одна на всех провайдеров: бот должен пересказывать базу, а не
// фантазировать, и вести себя одинаково, какой бы сервис ни стоял за ним.
const temperature = 0.2

type Message struct {
	Role    string
	Content string
}

// Answer — ответ модели и фактический расход токенов из поля usage, отдельно на
// вход и выход: у части провайдеров они стоят по-разному, а сводке экзамена нужна
// точная стоимость. Нули означают, что провайдер usage не вернул.
type Answer struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

// LLMClient — то, что видит бот: отправить диалог (системный промпт, история,
// вопрос) и получить ответ.
type LLMClient interface {
	Ask(ctx context.Context, messages []Message) (Answer, error)
}

// StatusError — провайдер ответил не 200. Код вынесен в поле, чтобы решение о
// повторе принималось по нему, а не по разбору текста ошибки.
type StatusError struct {
	Provider string
	Code     int
	Body     string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s вернул статус %d: %s", e.Provider, e.Code, e.Body)
}

// New выбирает реализацию по конфигу. Провайдер уже провалидирован в config,
// поэтому default здесь — защита от рассинхрона, а не пользовательский путь.
func New(cfg config.LLMConfig, log *slog.Logger) (LLMClient, error) {
	switch cfg.Provider {
	case config.ProviderGigaChat:
		return NewGigaChat(cfg.GigaChat, log)
	case config.ProviderYandexGPT:
		return NewYandexGPT(cfg.Yandex), nil
	case config.ProviderOpenAI:
		return NewOpenAI(cfg.OpenAI), nil
	default:
		return nil, fmt.Errorf("неизвестный LLM-провайдер %q", cfg.Provider)
	}
}
