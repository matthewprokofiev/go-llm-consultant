package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestYandex(url string) *YandexGPT {
	return &YandexGPT{
		httpClient: &http.Client{},
		apiKey:     "test-api-key",
		modelURI:   "gpt://folder/yandexgpt-lite/latest",
		url:        url,
		log:        testLogger(),
	}
}

func TestYandexAsk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Api-Key test-api-key" {
			t.Errorf("Authorization = %q, ожидался Api-Key test-api-key", got)
		}
		body, _ := io.ReadAll(r.Body)
		// Проверяем, что используем поле text, а не content, и modelUri собран.
		if !strings.Contains(string(body), `"text"`) {
			t.Errorf("тело без поля text: %s", body)
		}
		if !strings.Contains(string(body), `gpt://folder/yandexgpt-lite/latest`) {
			t.Errorf("тело без modelUri: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":{"alternatives":[{"message":{"role":"assistant","text":"  ответ Яндекса  "}}],"usage":{"totalTokens":"15"}}}`)
	}))
	defer srv.Close()

	y := newTestYandex(srv.URL)
	got, err := y.Ask(context.Background(), "system", "вопрос")
	if err != nil {
		t.Fatalf("Ask вернул ошибку: %v", err)
	}
	if got.Text != "ответ Яндекса" {
		t.Errorf("ответ = %q, ожидался обрезанный %q", got.Text, "ответ Яндекса")
	}
	if got.TokensUsed != 15 {
		t.Errorf("TokensUsed = %d, ожидалось 15 из строкового usage.totalTokens", got.TokensUsed)
	}
}

func TestYandexErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"bad request"}`)
	}))
	defer srv.Close()

	y := newTestYandex(srv.URL)
	_, err := y.Ask(context.Background(), "s", "u")
	if err == nil {
		t.Fatal("ожидалась ошибка при статусе 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("ошибка %q не упоминает статус 400", err.Error())
	}
}

func TestYandexTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release)

	y := newTestYandex(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := y.Ask(ctx, "s", "u")
	if err == nil {
		t.Fatal("ожидалась ошибка по таймауту")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ошибка = %v, ожидался context.DeadlineExceeded", err)
	}
}
