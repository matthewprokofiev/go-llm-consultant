package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/matthewprokofiev/go-llm-consultant/internal/config"
)

const openAIProviderName = "OpenAI-совместимый API"

// OpenAI — клиент к любому сервису с API как у OpenAI (ChatGPT, DeepSeek,
// OpenRouter и т.п.). Новый сервис подключается адресом, моделью и ключом.
type OpenAI struct {
	httpClient *http.Client
	url        string
	apiKey     string
	model      string
}

func NewOpenAI(cfg config.OpenAIConfig) *OpenAI {
	return &OpenAI{
		// Timeout 0 — дедлайн держит context вызывающего (см. коммент в GigaChat).
		httpClient: &http.Client{},
		url:        cfg.BaseURL + "/chat/completions",
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
	}
}

func (o *OpenAI) Ask(ctx context.Context, messages []Message) (Answer, error) {
	return chatCompletion(ctx, o.httpClient, o.url, "Bearer "+o.apiKey, o.model, messages, openAIProviderName)
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// chatCompletion — один запрос к /chat/completions в формате OpenAI. Этим же
// форматом говорит GigaChat, поэтому код общий: у GigaChat сверху только OAuth
// и сертификат Минцифры.
func chatCompletion(ctx context.Context, client *http.Client, url, authorization, model string, messages []Message, provider string) (Answer, error) {
	msgs := make([]chatMessage, len(messages))
	for i, m := range messages {
		msgs[i] = chatMessage(m)
	}
	body, err := json.Marshal(chatRequest{Model: model, Messages: msgs, Temperature: temperature})
	if err != nil {
		return Answer{}, fmt.Errorf("сборка запроса %s: %w", provider, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Answer{}, fmt.Errorf("создание запроса %s: %w", provider, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authorization)

	resp, err := client.Do(req)
	if err != nil {
		return Answer{}, fmt.Errorf("запрос к %s: %w", provider, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Answer{}, fmt.Errorf("чтение ответа %s: %w", provider, err)
	}
	if resp.StatusCode != http.StatusOK {
		return Answer{}, &StatusError{Provider: provider, Code: resp.StatusCode, Body: snippet(data)}
	}

	var parsed chatResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Answer{}, fmt.Errorf("разбор ответа %s: %w", provider, err)
	}
	if len(parsed.Choices) == 0 {
		return Answer{}, fmt.Errorf("%s вернул пустой список choices", provider)
	}
	return Answer{
		Text:         strings.TrimSpace(parsed.Choices[0].Message.Content),
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}, nil
}

// snippet обрезает тело ошибки для лога/сообщения: полный дамп чужого ответа не нужен.
func snippet(data []byte) string {
	const limit = 300
	s := strings.TrimSpace(string(data))
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
