package llm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/matveiprokofev/go-llm-consultant/internal/config"
)

const (
	gigaOAuthURL = "https://ngw.devices.sberbank.ru:9443/api/v2/oauth"
	gigaChatURL  = "https://gigachat.devices.sberbank.ru/api/v1/chat/completions"

	// Токен живёт 30 минут. Кэшируем на этот срок минус запас, чтобы не уйти в запрос
	// с токеном, протухающим в полёте.
	gigaTokenTTL          = 30 * time.Minute
	gigaTokenSafetyWindow = 60 * time.Second
)

// GigaChat — клиент к GigaChat API с OAuth-токеном и кастомным TLS.
type GigaChat struct {
	httpClient *http.Client
	authKey    string
	scope      string
	model      string
	oauthURL   string
	chatURL    string
	log        *slog.Logger

	// now вынесен в поле, чтобы тест кэша токена мог управлять «временем» без sleep.
	now func() time.Time

	// sf схлопывает параллельные переполучения токена в один сетевой запрос:
	// N одновременных промахов кэша (или N ответов 401) не устраивают N походов в OAuth.
	sf singleflight.Group

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewGigaChat строит клиент. Главное здесь — http.Client с CertPool, куда добавлен
// корневой сертификат НУЦ Минцифры: цепочка GigaChat подписана им, и без него
// TLS-рукопожатие к API падает. InsecureSkipVerify — крайний фолбэк для локального
// демо с явным предупреждением в лог.
func NewGigaChat(cfg config.GigaChatConfig, log *slog.Logger) (*GigaChat, error) {
	tlsCfg, err := gigaTLSConfig(cfg, log)
	if err != nil {
		return nil, err
	}

	return &GigaChat{
		// Timeout у http.Client намеренно 0: дедлайн задаёт вызывающий через
		// context.WithTimeout (45с). Короткий глобальный Timeout перебил бы контекст
		// и рвал бы генерацию, которая штатно идёт 10–20 секунд.
		httpClient: &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
		authKey:  cfg.AuthKey,
		scope:    cfg.Scope,
		model:    cfg.Model,
		oauthURL: gigaOAuthURL,
		chatURL:  gigaChatURL,
		log:      log,
		now:      time.Now,
	}, nil
}

func gigaTLSConfig(cfg config.GigaChatConfig, log *slog.Logger) (*tls.Config, error) {
	if cfg.InsecureSkipVerify {
		log.Warn("GIGACHAT_INSECURE_SKIP_VERIFY=true: проверка TLS-сертификата GigaChat отключена — только для локального демо, никогда в проде")
		return &tls.Config{InsecureSkipVerify: true}, nil //nolint:gosec // осознанный фолбэк, задаётся явным ENV
	}

	pem, err := os.ReadFile(cfg.CertPath)
	if err != nil {
		return nil, fmt.Errorf("чтение сертификата Минцифры %s (скачайте `make cert`): %w", cfg.CertPath, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("сертификат %s не удалось добавить в пул: ожидается PEM", cfg.CertPath)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

// Ask задаёт вопрос модели. При 401 (протухший токен) один раз переполучает токен
// и повторяет запрос — это штатная ситуация на границе 30-минутного окна.
func (g *GigaChat) Ask(ctx context.Context, systemPrompt, userMessage string) (Answer, error) {
	token, err := g.accessToken(ctx, false)
	if err != nil {
		return Answer{}, err
	}

	ans, status, err := g.chat(ctx, token, systemPrompt, userMessage)
	if err == nil {
		return ans, nil
	}
	if status != http.StatusUnauthorized {
		return Answer{}, err
	}

	// Ровно один повтор с принудительно обновлённым токеном.
	g.log.Debug("GigaChat вернул 401, обновляю токен и повторяю")
	token, err = g.accessToken(ctx, true)
	if err != nil {
		return Answer{}, err
	}
	ans, _, err = g.chat(ctx, token, systemPrompt, userMessage)
	return ans, err
}

type gigaChatRequest struct {
	Model    string            `json:"model"`
	Messages []gigaChatMessage `json:"messages"`
}

type gigaChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type gigaChatResponse struct {
	Choices []struct {
		Message gigaChatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// chat делает один запрос к /chat/completions. Второй возврат — HTTP-статус, он нужен
// вызывающему, чтобы отличить 401 (повторяемый) от прочих ошибок.
func (g *GigaChat) chat(ctx context.Context, token, systemPrompt, userMessage string) (Answer, int, error) {
	body, err := json.Marshal(gigaChatRequest{
		Model: g.model,
		Messages: []gigaChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
	})
	if err != nil {
		return Answer{}, 0, fmt.Errorf("сборка запроса GigaChat: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.chatURL, bytes.NewReader(body))
	if err != nil {
		return Answer{}, 0, fmt.Errorf("создание запроса GigaChat: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return Answer{}, 0, fmt.Errorf("запрос к GigaChat: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Answer{}, resp.StatusCode, fmt.Errorf("чтение ответа GigaChat: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Answer{}, resp.StatusCode, fmt.Errorf("GigaChat вернул статус %d: %s", resp.StatusCode, snippet(data))
	}

	var parsed gigaChatResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Answer{}, resp.StatusCode, fmt.Errorf("разбор ответа GigaChat: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return Answer{}, resp.StatusCode, fmt.Errorf("GigaChat вернул пустой список choices")
	}
	g.log.Debug("ответ GigaChat получен", "total_tokens", parsed.Usage.TotalTokens)
	return Answer{
		Text:       strings.TrimSpace(parsed.Choices[0].Message.Content),
		TokensUsed: parsed.Usage.TotalTokens,
	}, resp.StatusCode, nil
}

// accessToken отдаёт валидный токен из кэша или получает новый. force=true
// принудительно переполучает (после 401).
//
// Быстрый путь читает кэш под коротким локом и не держит мьютекс на время сетевого
// запроса: иначе один медленный OAuth заблокировал бы все параллельные Ask, даже те,
// у кого на руках валидный токен. Само переполучение идёт через singleflight —
// параллельные промахи схлопываются в один поход в OAuth.
func (g *GigaChat) accessToken(ctx context.Context, force bool) (string, error) {
	if !force {
		g.mu.Lock()
		if g.token != "" && g.now().Before(g.tokenExp.Add(-gigaTokenSafetyWindow)) {
			token := g.token
			g.mu.Unlock()
			return token, nil
		}
		g.mu.Unlock()
	}

	v, err, _ := g.sf.Do("token", func() (any, error) {
		token, serverExp, err := g.fetchToken(ctx)
		if err != nil {
			return "", err
		}

		// Срок жизни — минимум из «наивных» 30 минут и серверного expires_at (если
		// он есть и раньше). Так кэш не переживёт реальный токен при коротком сроке
		// или расхождении часов; повтор на 401 остаётся страховкой.
		exp := g.now().Add(gigaTokenTTL)
		if !serverExp.IsZero() && serverExp.Before(exp) {
			exp = serverExp
		}

		g.mu.Lock()
		g.token = token
		g.tokenExp = exp
		g.mu.Unlock()

		g.log.Debug("получен новый OAuth-токен GigaChat", "expires_at", exp.Format(time.RFC3339))
		return token, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

type gigaOAuthResponse struct {
	AccessToken string `json:"access_token"`
	// expires_at — момент истечения токена, Unix-время в миллисекундах.
	// TODO: сверить единицы с офиц. документацией developers.sber.ru/docs/ru/gigachat.
	ExpiresAt int64 `json:"expires_at"`
}

// fetchToken возвращает токен и момент его истечения по данным сервера (нулевое
// время, если expires_at не пришёл).
func (g *GigaChat) fetchToken(ctx context.Context) (string, time.Time, error) {
	form := url.Values{}
	form.Set("scope", g.scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.oauthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("создание запроса OAuth GigaChat: %w", err)
	}
	rquid, err := uuidV4()
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("RqUID", rquid)
	req.Header.Set("Authorization", "Basic "+g.authKey)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("запрос OAuth GigaChat: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("чтение ответа OAuth GigaChat: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("OAuth GigaChat вернул статус %d: %s", resp.StatusCode, snippet(data))
	}

	var parsed gigaOAuthResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("разбор ответа OAuth GigaChat: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("OAuth GigaChat вернул пустой access_token")
	}

	var serverExp time.Time
	if parsed.ExpiresAt > 0 {
		serverExp = time.UnixMilli(parsed.ExpiresAt)
	}
	return parsed.AccessToken, serverExp, nil
}

// uuidV4 генерирует UUID для заголовка RqUID без внешней зависимости.
func uuidV4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("генерация RqUID: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // версия 4
	b[8] = (b[8] & 0x3f) | 0x80 // вариант 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// snippet обрезает тело ошибки для лога/сообщения: полный дамп чужого ответа не нужен.
func snippet(data []byte) string {
	const max = 300
	s := strings.TrimSpace(string(data))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
