package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

const (
	EnvLocal = "local"

	ProviderGigaChat  = "gigachat"
	ProviderYandexGPT = "yandexgpt"

	defaultGigaChatScope = "GIGACHAT_API_PERS"
	defaultGigaChatModel = "GigaChat-2"
	defaultGigaChatCert  = "certs/russian_trusted_root_ca.cer"
	defaultYandexModel   = "yandexgpt-lite"
	defaultKnowledgePath = "knowledge/faq.md"
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

type Config struct {
	BotToken      string
	AdminTgID     int64
	DatabaseURL   string
	Provider      string
	KnowledgePath string
	AppEnv        string

	GigaChat GigaChatConfig
	Yandex   YandexConfig
}

// Load собирает конфиг из ENV и падает при отсутствии критичных переменных.
// Все проблемы копятся в срез и возвращаются одной ошибкой — иначе запуск
// превращается в игру «почини переменную — узнай про следующую».
//
// Провайдер-специфичные переменные обязательны только для выбранного LLM_PROVIDER:
// требовать ключи Yandex при работе через GigaChat бессмысленно.
func Load() (Config, error) {
	var cfg Config
	var problems []string

	// Секреты и DSN триммятся: перенос строки/пробел при копипасте в .env иначе
	// уехал бы в заголовок Authorization или в строку подключения и давал бы
	// невнятные 401 / ошибки коннекта.
	cfg.BotToken = strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if cfg.BotToken == "" {
		problems = append(problems, "BOT_TOKEN не задан: получите токен у @BotFather")
	}

	cfg.DatabaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if cfg.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL не задан: например postgres://user:pass@host:5432/db?sslmode=disable")
	}

	// ADMIN_TG_ID опционален: пусто — команда /reload просто никому не доступна.
	if raw := strings.TrimSpace(os.Getenv("ADMIN_TG_ID")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			problems = append(problems, fmt.Sprintf("ADMIN_TG_ID=%q не является числовым tg id", raw))
		} else {
			cfg.AdminTgID = id
		}
	}

	cfg.Provider = strings.ToLower(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
	switch cfg.Provider {
	case ProviderGigaChat:
		problems = append(problems, loadGigaChat(&cfg.GigaChat)...)
	case ProviderYandexGPT:
		problems = append(problems, loadYandex(&cfg.Yandex)...)
	case "":
		problems = append(problems, "LLM_PROVIDER не задан: ожидается gigachat или yandexgpt")
	default:
		problems = append(problems, fmt.Sprintf("LLM_PROVIDER=%q не поддерживается: ожидается gigachat или yandexgpt", cfg.Provider))
	}

	cfg.KnowledgePath = envOrDefault("KNOWLEDGE_PATH", defaultKnowledgePath)
	cfg.AppEnv = envOrDefault("APP_ENV", EnvLocal)

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("некорректная конфигурация:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return cfg, nil
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

	if raw := strings.TrimSpace(os.Getenv("GIGACHAT_INSECURE_SKIP_VERIFY")); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("GIGACHAT_INSECURE_SKIP_VERIFY=%q: ожидается true или false", raw))
		} else {
			gc.InsecureSkipVerify = v
		}
	}

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

func NewLogger(appEnv string) *slog.Logger {
	if appEnv == EnvLocal {
		return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
