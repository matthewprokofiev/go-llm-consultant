package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	EnvLocal = "local"

	ProviderGigaChat  = "gigachat"
	ProviderYandexGPT = "yandexgpt"
	ProviderOpenAI    = "openai"

	defaultProvider      = ProviderGigaChat
	defaultGigaChatScope = "GIGACHAT_API_PERS"
	defaultGigaChatModel = "GigaChat-2"
	defaultGigaChatCert  = "certs/russian_trusted_root_ca.cer"
	defaultYandexModel   = "yandexgpt-lite"
	defaultKnowledgePath = "knowledge/faq.md"
	defaultTimezone      = "Europe/Moscow"

	defaultLeadsPerHour     = 3
	defaultQuestionsPerHour = 20
)

// GigaChatConfig — всё, что нужно клиенту GigaChat. InsecureSkipVerify — крайний
// фолбэк для локального демо, когда сертификат Минцифры недоступен.
type GigaChatConfig struct {
	AuthKey            string
	Scope              string
	Model              string
	CertPath           string
	InsecureSkipVerify bool
}

// YandexConfig — параметры клиента YandexGPT.
type YandexConfig struct {
	APIKey   string
	FolderID string
	Model    string
}

// OpenAIConfig — любой сервис с API как у OpenAI (ChatGPT, DeepSeek и т.п.):
// подключается адресом, моделью и ключом, без правки кода.
type OpenAIConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Prices — тарифы провайдера для сводки экзамена, ₽ за 1 000 токенов. В коде не
// зашиты: меняются чаще, чем код.
type Prices struct {
	InputPer1K  float64
	OutputPer1K float64
	MinMonthly  float64
}

type LLMConfig struct {
	Provider string
	GigaChat GigaChatConfig
	Yandex   YandexConfig
	OpenAI   OpenAIConfig
	Prices   Prices
}

// Model — имя модели выбранного провайдера, для логов и сводки экзамена.
func (c LLMConfig) Model() string {
	switch c.Provider {
	case ProviderGigaChat:
		return c.GigaChat.Model
	case ProviderYandexGPT:
		return c.Yandex.Model
	default:
		return c.OpenAI.Model
	}
}

// Core — то, что нужно ядру ответа: нейросеть, база знаний и название бизнеса для
// промпта. Экзамен гоняет ту же логику, что и бот, но без Telegram, поэтому
// грузит только эту часть.
type Core struct {
	LLM           LLMConfig
	BusinessName  string
	KnowledgePath string
	AppEnv        string
}

// Texts — реплики бота, которые отличаются у разных клиентов.
type Texts struct {
	Greeting         string
	ButtonLead       string
	ButtonContacts   string
	LeadSent         string
	QuestionSent     string
	DeliveryFailed   string
	OwnerReplyPrefix string
	Consent          string
	PrivacyURL       string
}

type Config struct {
	Core

	BotToken         string
	OwnerChatID      int64
	AdminTgID        int64
	DatabaseURL      string
	BusinessContacts string
	Location         *time.Location
	LeadOfferEnabled bool
	LeadsPerHour     int
	QuestionsPerHour int
	Texts            Texts
}

// Load собирает конфиг бота из ENV и падает при отсутствии критичных переменных.
// Все проблемы копятся в срез и возвращаются одной ошибкой — иначе запуск
// превращается в игру «почини переменную — узнай про следующую».
func Load() (Config, error) {
	var cfg Config
	core, problems := loadCore()
	cfg.Core = core

	// Секреты триммятся: перенос строки при копипасте в .env иначе уехал бы
	// в заголовок Authorization и давал бы невнятные 401.
	cfg.BotToken = strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if cfg.BotToken == "" {
		problems = append(problems, "BOT_TOKEN не задан: получите токен у @BotFather")
	}

	if raw := strings.TrimSpace(os.Getenv("OWNER_CHAT_ID")); raw == "" {
		problems = append(problems, "OWNER_CHAT_ID не задан: id владельца или рабочей группы, куда приходят заявки")
	} else if id, err := strconv.ParseInt(raw, 10, 64); err != nil {
		problems = append(problems, fmt.Sprintf("OWNER_CHAT_ID=%q не является числовым chat id", raw))
	} else {
		cfg.OwnerChatID = id
	}

	// ADMIN_TG_ID опционален: пусто — /reload и /stats просто никому не доступны.
	if raw := strings.TrimSpace(os.Getenv("ADMIN_TG_ID")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			problems = append(problems, fmt.Sprintf("ADMIN_TG_ID=%q не является числовым tg id", raw))
		} else {
			cfg.AdminTgID = id
		}
	}

	// DATABASE_URL опционален: без него бот ничего не пишет, с ним — обезличенную статистику.
	cfg.DatabaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))

	cfg.BusinessContacts = text("BUSINESS_CONTACTS", "")
	if cfg.BusinessContacts == "" {
		problems = append(problems, `BUSINESS_CONTACTS не задан: телефон, адрес и часы работы — бот даёт их, когда не может помочь сам (перенос строки — \n)`)
	}

	tz := envOrDefault("TIMEZONE", defaultTimezone)
	loc, err := time.LoadLocation(tz)
	if err != nil {
		problems = append(problems, fmt.Sprintf("TIMEZONE=%q: неизвестный часовой пояс, например Europe/Moscow", tz))
	}
	cfg.Location = loc

	cfg.LeadOfferEnabled = parseBool("LEAD_OFFER_ENABLED", true, &problems)
	cfg.LeadsPerHour = parseNonNegative("LIMIT_LEADS_PER_HOUR", defaultLeadsPerHour, &problems)
	cfg.QuestionsPerHour = parseNonNegative("LIMIT_QUESTIONS_PER_HOUR", defaultQuestionsPerHour, &problems)

	cfg.Texts = Texts{
		Greeting: text("GREETING_TEXT", fmt.Sprintf(
			"Здравствуйте! Я онлайн-помощник «%s». Отвечу на вопросы об услугах, ценах и условиях. Оставить заявку можно кнопкой внизу.",
			cfg.BusinessName)),
		ButtonLead:       text("BUTTON_LEAD_TEXT", "📝 Оставить заявку"),
		ButtonContacts:   text("BUTTON_CONTACTS_TEXT", "📍 Контакты"),
		LeadSent:         text("LEAD_SENT_TEXT", "Спасибо! Заявка передана, с вами скоро свяжутся."),
		QuestionSent:     text("QUESTION_SENT_TEXT", "Спасибо! Вопрос передан администратору, с вами скоро свяжутся."),
		DeliveryFailed:   text("DELIVERY_FAILED_TEXT", "Извините, передать сообщение не получилось: связь сейчас нестабильна. Пожалуйста, свяжитесь с нами напрямую:"),
		OwnerReplyPrefix: text("OWNER_REPLY_PREFIX", "Вам ответил администратор:"),
		Consent:          text("CONSENT_TEXT", ""),
		PrivacyURL:       strings.TrimSpace(os.Getenv("PRIVACY_URL")),
	}

	if len(problems) > 0 {
		return Config{}, joinProblems(problems)
	}
	return cfg, nil
}

// LoadCore — конфиг для инструментов без Telegram (экзамен): не требует токена
// бота и чата владельца.
func LoadCore() (Core, error) {
	core, problems := loadCore()
	if len(problems) > 0 {
		return Core{}, joinProblems(problems)
	}
	return core, nil
}

func loadCore() (Core, []string) {
	var core Core
	var problems []string

	core.BusinessName = text("BUSINESS_NAME", "")
	if core.BusinessName == "" {
		problems = append(problems, "BUSINESS_NAME не задан: название бизнеса, как его называть клиентам")
	}
	core.KnowledgePath = envOrDefault("KNOWLEDGE_PATH", defaultKnowledgePath)
	core.AppEnv = envOrDefault("APP_ENV", EnvLocal)

	llmCfg, llmProblems := loadLLM()
	core.LLM = llmCfg
	return core, append(problems, llmProblems...)
}

// Провайдер-специфичные переменные обязательны только для выбранного LLM_PROVIDER:
// требовать ключи Yandex при работе через GigaChat бессмысленно.
func loadLLM() (LLMConfig, []string) {
	var cfg LLMConfig
	var problems []string

	cfg.Provider = strings.ToLower(envOrDefault("LLM_PROVIDER", defaultProvider))
	switch cfg.Provider {
	case ProviderGigaChat:
		problems = append(problems, loadGigaChat(&cfg.GigaChat)...)
	case ProviderYandexGPT:
		problems = append(problems, loadYandex(&cfg.Yandex)...)
	case ProviderOpenAI:
		problems = append(problems, loadOpenAI(&cfg.OpenAI)...)
	default:
		problems = append(problems, fmt.Sprintf("LLM_PROVIDER=%q не поддерживается: ожидается gigachat, yandexgpt или openai", cfg.Provider))
	}

	cfg.Prices = Prices{
		InputPer1K:  parsePrice("LLM_PRICE_INPUT_PER_1K", &problems),
		OutputPer1K: parsePrice("LLM_PRICE_OUTPUT_PER_1K", &problems),
		MinMonthly:  parsePrice("LLM_MIN_MONTHLY", &problems),
	}
	return cfg, problems
}

func loadGigaChat(gc *GigaChatConfig) []string {
	var problems []string

	gc.AuthKey = strings.TrimSpace(os.Getenv("GIGACHAT_AUTH_KEY"))
	if gc.AuthKey == "" {
		problems = append(problems, "GIGACHAT_AUTH_KEY не задан: base64(ClientID:ClientSecret) из кабинета developers.sber.ru")
	}

	gc.Scope = envOrDefault("GIGACHAT_SCOPE", defaultGigaChatScope)
	gc.Model = envOrDefault("GIGACHAT_MODEL", defaultGigaChatModel)
	gc.CertPath = envOrDefault("GIGACHAT_CERT_PATH", defaultGigaChatCert)
	gc.InsecureSkipVerify = parseBool("GIGACHAT_INSECURE_SKIP_VERIFY", false, &problems)

	return problems
}

func loadYandex(yc *YandexConfig) []string {
	var problems []string

	yc.APIKey = strings.TrimSpace(os.Getenv("YANDEX_API_KEY"))
	if yc.APIKey == "" {
		problems = append(problems, "YANDEX_API_KEY не задан: API-ключ сервис-аккаунта с ролью ai.languageModels.user")
	}

	yc.FolderID = strings.TrimSpace(os.Getenv("YANDEX_FOLDER_ID"))
	if yc.FolderID == "" {
		problems = append(problems, "YANDEX_FOLDER_ID не задан: идентификатор каталога Yandex Cloud")
	}

	yc.Model = envOrDefault("YANDEX_MODEL", defaultYandexModel)

	return problems
}

func loadOpenAI(oc *OpenAIConfig) []string {
	var problems []string

	oc.BaseURL = strings.TrimRight(strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), "/")
	if u, err := url.Parse(oc.BaseURL); oc.BaseURL == "" || err != nil || u.Scheme == "" || u.Host == "" {
		problems = append(problems, "OPENAI_BASE_URL не задан или некорректен: адрес API, например https://api.deepseek.com/v1")
	}

	oc.APIKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if oc.APIKey == "" {
		problems = append(problems, "OPENAI_API_KEY не задан: ключ API выбранного сервиса")
	}

	oc.Model = strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if oc.Model == "" {
		problems = append(problems, "OPENAI_MODEL не задан: имя модели, например deepseek-chat")
	}

	return problems
}

func NewLogger(appEnv string) *slog.Logger {
	if appEnv == EnvLocal {
		return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func joinProblems(problems []string) error {
	return fmt.Errorf("некорректная конфигурация:\n  - %s", strings.Join(problems, "\n  - "))
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// text читает текстовую настройку: в .env многострочный текст записывается одной
// строкой с \n, здесь он превращается в настоящие переносы.
func text(key, fallback string) string {
	return strings.ReplaceAll(envOrDefault(key, fallback), `\n`, "\n")
}

func parseBool(key string, fallback bool, problems *[]string) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s=%q: ожидается true или false", key, raw))
		return fallback
	}
	return v
}

// parseNonNegative читает лимит: 0 отключает ограничение.
func parseNonNegative(key string, fallback int, problems *[]string) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		*problems = append(*problems, fmt.Sprintf("%s=%q: ожидается целое число ≥ 0 (0 — без ограничения)", key, raw))
		return fallback
	}
	return v
}

// parsePrice читает тариф в рублях; десятичная запятая допускается, как пишут в прайсах.
func parsePrice(key string, problems *[]string) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", "."), 64)
	if err != nil || v < 0 {
		*problems = append(*problems, fmt.Sprintf("%s=%q: ожидается цена в рублях, например 0.065", key, raw))
		return 0
	}
	return v
}
