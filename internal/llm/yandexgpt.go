package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/matthewprokofiev/go-llm-consultant/internal/config"
)

const yandexCompletionURL = "https://llm.api.cloud.yandex.net/foundationModels/v1/completion"

const yandexProviderName = "YandexGPT"

// YandexGPT — клиент к YandexGPT. Проще GigaChat: аутентификация через Api-Key,
// TLS по публичным CA (кастомный пул не нужен).
type YandexGPT struct {
	httpClient *http.Client
	apiKey     string
	modelURI   string
	url        string
}

func NewYandexGPT(cfg config.YandexConfig) *YandexGPT {
	return &YandexGPT{
		// Timeout 0 — дедлайн держит context вызывающего (см. коммент в GigaChat).
		httpClient: &http.Client{},
		apiKey:     cfg.APIKey,
		modelURI:   fmt.Sprintf("gpt://%s/%s/latest", cfg.FolderID, cfg.Model),
		url:        yandexCompletionURL,
	}
}

type yandexRequest struct {
	ModelURI          string             `json:"modelUri"`
	CompletionOptions yandexOptions      `json:"completionOptions"`
	Messages          []yandexReqMessage `json:"messages"`
}

type yandexOptions struct {
	Stream      bool    `json:"stream"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"maxTokens"`
}

// Внимание: у Yandex поле называется "text", а не "content" как у OpenAI/GigaChat.
type yandexReqMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type yandexResponse struct {
	Result struct {
		Alternatives []struct {
			Message yandexReqMessage `json:"message"`
		} `json:"alternatives"`
		Usage struct {
			// Yandex отдаёт счётчики токенов строками ("123"), а не числами.
			InputTextTokens  string `json:"inputTextTokens"`
			CompletionTokens string `json:"completionTokens"`
		} `json:"usage"`
	} `json:"result"`
}

func (y *YandexGPT) Ask(ctx context.Context, messages []Message) (Answer, error) {
	msgs := make([]yandexReqMessage, len(messages))
	for i, m := range messages {
		msgs[i] = yandexReqMessage{Role: m.Role, Text: m.Content}
	}
	body, err := json.Marshal(yandexRequest{
		ModelURI: y.modelURI,
		CompletionOptions: yandexOptions{
			Stream:      false,
			Temperature: temperature,
			MaxTokens:   2000,
		},
		Messages: msgs,
	})
	if err != nil {
		return Answer{}, fmt.Errorf("сборка запроса YandexGPT: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, y.url, bytes.NewReader(body))
	if err != nil {
		return Answer{}, fmt.Errorf("создание запроса YandexGPT: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Api-Key "+y.apiKey)

	resp, err := y.httpClient.Do(req)
	if err != nil {
		return Answer{}, fmt.Errorf("запрос к YandexGPT: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Answer{}, fmt.Errorf("чтение ответа YandexGPT: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Answer{}, &StatusError{Provider: yandexProviderName, Code: resp.StatusCode, Body: snippet(data)}
	}

	var parsed yandexResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Answer{}, fmt.Errorf("разбор ответа YandexGPT: %w", err)
	}
	if len(parsed.Result.Alternatives) == 0 {
		return Answer{}, fmt.Errorf("YandexGPT вернул пустой список alternatives")
	}

	// Счётчики приходят строками ("123"); при неразборе токены просто 0.
	in, _ := strconv.Atoi(parsed.Result.Usage.InputTextTokens)
	out, _ := strconv.Atoi(parsed.Result.Usage.CompletionTokens)
	return Answer{
		Text:         strings.TrimSpace(parsed.Result.Alternatives[0].Message.Text),
		InputTokens:  in,
		OutputTokens: out,
	}, nil
}
