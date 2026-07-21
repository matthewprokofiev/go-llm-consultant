// Package telegram связывает Telegram, базу знаний и LLM: принимает вопрос,
// собирает системный промпт с релевантными секциями FAQ, спрашивает модель,
// отвечает пользователю и пишет диалог в журнал.
package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/matveiprokofev/go-llm-consultant/internal/llm"
)

// askTimeout — дедлайн одного запроса к LLM. Генерация штатно идёт 10–20с, берём
// с запасом 45с. Задаётся через context.WithTimeout, а не через http.Client.Timeout.
const askTimeout = 45 * time.Second

// Разделители базы знаний в системном промпте. Вынесены в константы, чтобы вырезать
// их же из контента базы (см. sanitizeKnowledge) и не дать строке из FAQ подделать
// границу блока.
const (
	knowledgeStartMarker = "=== БАЗА ЗНАНИЙ ==="
	knowledgeEndMarker   = "=== КОНЕЦ БАЗЫ ЗНАНИЙ ==="
)

// LLM, Knowledge — зависимости консультанта за интерфейсами: так его логику
// (сборка промпта, таймаут) можно проверить с заглушками, без сети.
type LLM interface {
	Ask(ctx context.Context, systemPrompt, userMessage string) (llm.Answer, error)
}

type Knowledge interface {
	Select(query string) string
	Reload() error
}

type Consultant struct {
	llm      LLM
	kb       Knowledge
	provider string
	log      *slog.Logger
}

func NewConsultant(llm LLM, kb Knowledge, provider string, log *slog.Logger) *Consultant {
	return &Consultant{llm: llm, kb: kb, provider: provider, log: log}
}

// Answer отбирает релевантные секции, спрашивает модель и возвращает ответ вместе
// с расходом токенов для журнала: фактический usage от провайдера, а при его
// отсутствии — грубая оценка по всему промпту.
func (c *Consultant) Answer(ctx context.Context, question string) (answer string, tokens int, err error) {
	knowledge := c.kb.Select(question)
	prompt := buildSystemPrompt(knowledge)

	askCtx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()

	res, err := c.llm.Ask(askCtx, prompt, question)
	if err != nil {
		return "", 0, fmt.Errorf("запрос к LLM (%s): %w", c.provider, err)
	}
	answer = strings.TrimSpace(res.Text)

	tokens = res.TokensUsed
	if tokens <= 0 {
		// Фолбэк учитывает и системный промпт (база знаний до ~12k символов),
		// иначе расход занижался бы кратно.
		tokens = estimateTokens(prompt + question + answer)
	}
	return answer, tokens, nil
}

// buildSystemPrompt заворачивает выбранную базу знаний в инструкцию: отвечать только
// по ней и честно признавать незнание, а не выдумывать цены и условия.
func buildSystemPrompt(knowledge string) string {
	var b strings.Builder
	b.WriteString("Ты — вежливый онлайн-консультант компании. ")
	b.WriteString("Отвечай на вопросы клиентов ТОЛЬКО на основе базы знаний ниже. ")
	b.WriteString("Если ответа в ней нет — честно скажи, что не знаешь, и предложи обратиться в поддержку. ")
	b.WriteString("Не выдумывай факты, цены, сроки и условия. Отвечай кратко и по делу, на русском языке.\n\n")
	b.WriteString(knowledgeStartMarker + "\n")
	if strings.TrimSpace(knowledge) == "" {
		b.WriteString("(база знаний пуста)")
	} else {
		b.WriteString(sanitizeKnowledge(knowledge))
	}
	b.WriteString("\n" + knowledgeEndMarker)
	return b.String()
}

// sanitizeKnowledge вырезает из контента базы строки, совпадающие с разделителями
// блока: строка-разделитель внутри FAQ иначе подделала бы границу и позволила бы
// «дописать» инструкции модели за пределами блока знаний.
func sanitizeKnowledge(knowledge string) string {
	lines := strings.Split(knowledge, "\n")
	kept := lines[:0]
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case knowledgeStartMarker, knowledgeEndMarker:
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// estimateTokens — грубая оценка (символы/3 для русского). Используется фолбэком,
// когда провайдер не вернул usage.total_tokens.
func estimateTokens(s string) int {
	return len([]rune(s)) / 3
}
