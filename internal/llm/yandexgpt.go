package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/matveiprokofev/go-llm-consultant/internal/config"
)

const yandexCompletionURL = "https://llm.api.cloud.yandex.net/foundationModels/v1/completion"

// YandexGPT — клиент к YandexGPT. Проще GigaChat: аутентификация через Api-Key,
// TLS по публичным CA (кастомный пул не нужен).
type YandexGPT struct {
	httpClient *http.Client
	apiKey     string
	modelURI   string
	url        string
	log        *slog.Logger
}

func NewYandexGPT(cfg config.YandexConfig, log *slog.Logger) (*YandexGPT, error) {
	return &YandexGPT{
		// Timeout 0 — дедлайн держит context вызывающего (см. коммент в GigaChat).
		httpClient: &http.Client{},
		apiKey:     cfg.APIKey,
		modelURI:   fmt.Sprintf("gpt://%s/%s/latest", cfg.FolderID, cfg.Model),
		url:        yandexCompletionURL,
		log:        log,
	}, nil
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
			TotalTokens string `json:"totalTokens"`
		} `json:"usage"`
	} `json:"result"`
}

func (y *YandexGPT) Ask(ctx context.Context, systemPrompt, userMessage string) (Answer, error) {
	body, err := json.Marshal(yandexRequest{
		ModelURI: y.modelURI,
		CompletionOptions: yandexOptions{
			Stream:      false,
			Temperature: 0.3,
			MaxTokens:   2000,
		},
		Messages: []yandexReqMessage{
			{Role: "system", Text: systemPrompt},
			{Role: "user", Text: userMessage},
		},
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
		return Answer{}, fmt.Errorf("YandexGPT вернул статус %d: %s", resp.StatusCode, snippet(data))
	}

	var parsed yandexResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Answer{}, fmt.Errorf("разбор ответа YandexGPT: %w", err)
	}
	if len(parsed.Result.Alternatives) == 0 {
		return Answer{}, fmt.Errorf("YandexGPT вернул пустой список alternatives")
	}
	y.log.Debug("ответ YandexGPT получен", "total_tokens", parsed.Result.Usage.TotalTokens)

	// usage.totalTokens приходит строкой ("123"); при неразборе токены просто 0.
	tokens, _ := strconv.Atoi(parsed.Result.Usage.TotalTokens)
	return Answer{
		Text:       strings.TrimSpace(parsed.Result.Alternatives[0].Message.Text),
		TokensUsed: tokens,
	}, nil
}
