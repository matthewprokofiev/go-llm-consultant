package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matthewprokofiev/go-llm-consultant/internal/config"
)

func TestOpenAIAsk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("путь = %q, ожидался /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("тело запроса: %v", err)
		}
		// История диалога должна уйти целиком и в том же порядке ролей.
		if req.Model != "deepseek-chat" || len(req.Messages) != 4 || req.Messages[2].Role != RoleAssistant {
			t.Errorf("запрос = %+v", req)
		}
		if req.Temperature != temperature {
			t.Errorf("temperature = %v, ожидалась общая %v", req.Temperature, temperature)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":" ответ "}}],"usage":{"prompt_tokens":30,"completion_tokens":5}}`)
	}))
	defer srv.Close()

	o := NewOpenAI(config.OpenAIConfig{BaseURL: srv.URL + "/v1", APIKey: "test-key", Model: "deepseek-chat"})
	got, err := o.Ask(context.Background(), []Message{
		{Role: RoleSystem, Content: "s"},
		{Role: RoleUser, Content: "первый вопрос"},
		{Role: RoleAssistant, Content: "первый ответ"},
		{Role: RoleUser, Content: "а сколько стоит?"},
	})
	if err != nil {
		t.Fatalf("Ask вернул ошибку: %v", err)
	}
	if got.Text != "ответ" || got.InputTokens != 30 || got.OutputTokens != 5 {
		t.Errorf("ответ = %+v", got)
	}
}

func TestOpenAIBadKeyIsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid API key"}}`)
	}))
	defer srv.Close()

	o := NewOpenAI(config.OpenAIConfig{BaseURL: srv.URL, APIKey: "bad", Model: "m"})
	_, err := o.Ask(context.Background(), msgs("s", "u"))
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusUnauthorized {
		t.Fatalf("ошибка = %v, ожидался StatusError 401", err)
	}
}
