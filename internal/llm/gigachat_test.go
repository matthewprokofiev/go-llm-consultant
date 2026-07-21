package llm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestGigaChat собирает клиент в обход NewGigaChat: тесту не нужен реальный
// сертификат и TLS, нужен обычный http.Client, смотрящий на httptest-серверы.
func newTestGigaChat(oauthURL, chatURL string) *GigaChat {
	return &GigaChat{
		httpClient: &http.Client{},
		authKey:    "test-auth-key",
		scope:      "GIGACHAT_API_PERS",
		model:      "GigaChat-2",
		oauthURL:   oauthURL,
		chatURL:    chatURL,
		log:        testLogger(),
		now:        time.Now,
	}
}

func oauthServer(t *testing.T, counter *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(counter, 1)
		if r.Header.Get("Authorization") == "" || r.Header.Get("RqUID") == "" {
			t.Errorf("OAuth-запрос без Authorization/RqUID: %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"tok-`+time.Now().Format("150405.000000")+`","expires_at":9999999999999}`)
	}))
}

func TestGigaChatAsk(t *testing.T) {
	var oauthHits int32
	oauth := oauthServer(t, &oauthHits)
	defer oauth.Close()

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("chat без Bearer-токена: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"  Привет из FAQ  "}}],"usage":{"total_tokens":42}}`)
	}))
	defer chat.Close()

	g := newTestGigaChat(oauth.URL, chat.URL)
	got, err := g.Ask(context.Background(), "system", "вопрос")
	if err != nil {
		t.Fatalf("Ask вернул ошибку: %v", err)
	}
	if got.Text != "Привет из FAQ" {
		t.Errorf("ответ = %q, ожидался обрезанный %q", got.Text, "Привет из FAQ")
	}
	if got.TokensUsed != 42 {
		t.Errorf("TokensUsed = %d, ожидалось 42 из usage.total_tokens", got.TokensUsed)
	}
}

// TestGigaChatTokenCache: пока токен не истёк — OAuth не дёргается повторно;
// после истечения — переполучаем. Время управляется через поле now.
func TestGigaChatTokenCache(t *testing.T) {
	var oauthHits int32
	oauth := oauthServer(t, &oauthHits)
	defer oauth.Close()

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":1}}`)
	}))
	defer chat.Close()

	g := newTestGigaChat(oauth.URL, chat.URL)
	base := time.Now()
	current := base
	g.now = func() time.Time { return current }

	// Первый вызов: токена нет → один поход в OAuth.
	if _, err := g.Ask(context.Background(), "s", "u"); err != nil {
		t.Fatalf("первый Ask: %v", err)
	}
	if got := atomic.LoadInt32(&oauthHits); got != 1 {
		t.Fatalf("после первого Ask oauthHits = %d, ожидался 1", got)
	}

	// Второй вызов в пределах срока жизни токена → OAuth не дёргается.
	current = base.Add(5 * time.Minute)
	if _, err := g.Ask(context.Background(), "s", "u"); err != nil {
		t.Fatalf("второй Ask: %v", err)
	}
	if got := atomic.LoadInt32(&oauthHits); got != 1 {
		t.Fatalf("токен не истёк, а oauthHits = %d, ожидался 1", got)
	}

	// Перешагиваем срок жизни (30 мин) → токен переполучается.
	current = base.Add(gigaTokenTTL + time.Second)
	if _, err := g.Ask(context.Background(), "s", "u"); err != nil {
		t.Fatalf("третий Ask: %v", err)
	}
	if got := atomic.LoadInt32(&oauthHits); got != 2 {
		t.Fatalf("токен истёк, а oauthHits = %d, ожидался 2", got)
	}
}

// TestGigaChatConcurrentTokenFetch: при пустом кэше N параллельных Ask должны
// сходить в OAuth ровно один раз (singleflight), а не устроить N походов. Заодно
// проверяет отсутствие гонки и то, что лок не держится на время сетевого запроса.
func TestGigaChatConcurrentTokenFetch(t *testing.T) {
	var oauthHits int32
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&oauthHits, 1)
		time.Sleep(50 * time.Millisecond) // расширяем окно, чтобы вызовы точно пересеклись
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"tok","expires_at":9999999999999}`)
	}))
	defer oauth.Close()

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":1}}`)
	}))
	defer chat.Close()

	g := newTestGigaChat(oauth.URL, chat.URL)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := g.Ask(context.Background(), "s", "u"); err != nil {
				t.Errorf("параллельный Ask вернул ошибку: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&oauthHits); got != 1 {
		t.Errorf("oauthHits = %d, ожидался 1 (singleflight схлопывает параллельные промахи)", got)
	}
}

// TestGigaChat401Retry: на 401 клиент один раз переполучает токен и повторяет запрос.
func TestGigaChat401Retry(t *testing.T) {
	var oauthHits int32
	oauth := oauthServer(t, &oauthHits)
	defer oauth.Close()

	var chatHits int32
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&chatHits, 1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"token expired"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"после повтора"}}],"usage":{"total_tokens":7}}`)
	}))
	defer chat.Close()

	g := newTestGigaChat(oauth.URL, chat.URL)
	got, err := g.Ask(context.Background(), "s", "u")
	if err != nil {
		t.Fatalf("Ask после 401-повтора вернул ошибку: %v", err)
	}
	if got.Text != "после повтора" {
		t.Errorf("ответ = %q, ожидался %q", got.Text, "после повтора")
	}
	if chatHits != 2 {
		t.Errorf("chatHits = %d, ожидалось ровно 2 (запрос + один повтор)", chatHits)
	}
	if oauthHits != 2 {
		t.Errorf("oauthHits = %d, ожидалось 2 (начальный токен + принудительное обновление)", oauthHits)
	}
}

// TestGigaChat401NoInfiniteRetry: если 401 повторяется и после обновления токена,
// клиент не зацикливается — отдаёт ошибку после ровно одного повтора.
func TestGigaChat401NoInfiniteRetry(t *testing.T) {
	var oauthHits int32
	oauth := oauthServer(t, &oauthHits)
	defer oauth.Close()

	var chatHits int32
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&chatHits, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer chat.Close()

	g := newTestGigaChat(oauth.URL, chat.URL)
	_, err := g.Ask(context.Background(), "s", "u")
	if err == nil {
		t.Fatal("ожидалась ошибка при постоянном 401")
	}
	if chatHits != 2 {
		t.Errorf("chatHits = %d, ожидалось 2 (без бесконечных повторов)", chatHits)
	}
}

// TestGigaChatTimeout: генерация дольше дедлайна контекста → Ask возвращает ошибку
// контекста, а не висит. http.Client без своего Timeout не мешает контексту.
func TestGigaChatTimeout(t *testing.T) {
	var oauthHits int32
	oauth := oauthServer(t, &oauthHits)
	defer oauth.Close()

	release := make(chan struct{})
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer chat.Close()
	defer close(release)

	g := newTestGigaChat(oauth.URL, chat.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := g.Ask(ctx, "s", "u")
	if err == nil {
		t.Fatal("ожидалась ошибка по таймауту")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ошибка = %v, ожидался context.DeadlineExceeded", err)
	}
}
