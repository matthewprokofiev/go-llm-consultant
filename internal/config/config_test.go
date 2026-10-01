package config

import (
	"strings"
	"testing"
)

// setEnv выставляет ENV на время теста; t.Setenv сам чистит после.
func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

// baseValid — минимально валидное окружение для GigaChat.
func baseValid() map[string]string {
	return map[string]string{
		"BOT_TOKEN":         "token",
		"OWNER_CHAT_ID":     "-1001234567890",
		"BUSINESS_NAME":     "Фотостудия «Кадр»",
		"BUSINESS_CONTACTS": `+7 900 000-00-00\nул. Примерная, 1`,
		"LLM_PROVIDER":      "gigachat",
		"GIGACHAT_AUTH_KEY": "authkey",
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		wantErr    bool
		errSubstrs []string
		check      func(t *testing.T, c Config)
	}{
		{
			name: "gigachat минимально валидный, дефолты подставлены",
			env:  baseValid(),
			check: func(t *testing.T, c Config) {
				if c.LLM.Provider != ProviderGigaChat {
					t.Errorf("Provider = %q, ожидался gigachat", c.LLM.Provider)
				}
				if c.LLM.GigaChat.Scope != defaultGigaChatScope {
					t.Errorf("Scope = %q, ожидался дефолт %q", c.LLM.GigaChat.Scope, defaultGigaChatScope)
				}
				if c.LLM.GigaChat.Model != defaultGigaChatModel {
					t.Errorf("Model = %q, ожидался дефолт", c.LLM.GigaChat.Model)
				}
				if c.KnowledgePath != defaultKnowledgePath {
					t.Errorf("KnowledgePath = %q, ожидался дефолт", c.KnowledgePath)
				}
				if c.AppEnv != EnvLocal {
					t.Errorf("AppEnv = %q, ожидался local", c.AppEnv)
				}
			},
		},
		{
			name: "yandex минимально валидный",
			env: merge(without(baseValid(), "GIGACHAT_AUTH_KEY"), map[string]string{
				"LLM_PROVIDER":     "yandexgpt",
				"YANDEX_API_KEY":   "apikey",
				"YANDEX_FOLDER_ID": "folder",
			}),
			check: func(t *testing.T, c Config) {
				if c.LLM.Yandex.Model != defaultYandexModel {
					t.Errorf("Model = %q, ожидался дефолт", c.LLM.Yandex.Model)
				}
			},
		},
		{
			name:       "нет BOT_TOKEN",
			env:        without(baseValid(), "BOT_TOKEN"),
			wantErr:    true,
			errSubstrs: []string{"BOT_TOKEN"},
		},
		{
			name:       "нет чата владельца, названия и контактов",
			env:        without(without(without(baseValid(), "OWNER_CHAT_ID"), "BUSINESS_NAME"), "BUSINESS_CONTACTS"),
			wantErr:    true,
			errSubstrs: []string{"OWNER_CHAT_ID", "BUSINESS_NAME", "BUSINESS_CONTACTS"},
		},
		{
			name:       "битый OWNER_CHAT_ID",
			env:        merge(baseValid(), map[string]string{"OWNER_CHAT_ID": "@group"}),
			wantErr:    true,
			errSubstrs: []string{"OWNER_CHAT_ID"},
		},
		{
			name: "без LLM_PROVIDER и DATABASE_URL: gigachat, статистика выключена",
			env:  without(baseValid(), "LLM_PROVIDER"),
			check: func(t *testing.T, c Config) {
				if c.LLM.Provider != ProviderGigaChat {
					t.Errorf("Provider = %q, ожидался дефолт gigachat", c.LLM.Provider)
				}
				if c.DatabaseURL != "" {
					t.Errorf("DatabaseURL = %q, ожидался пустой", c.DatabaseURL)
				}
			},
		},
		{
			name:       "неизвестный провайдер",
			env:        merge(baseValid(), map[string]string{"LLM_PROVIDER": "claude"}),
			wantErr:    true,
			errSubstrs: []string{"LLM_PROVIDER", "claude"},
		},
		{
			name: "openai-совместимый: адрес, ключ и модель из настроек",
			env: merge(without(baseValid(), "GIGACHAT_AUTH_KEY"), map[string]string{
				"LLM_PROVIDER":    "openai",
				"OPENAI_BASE_URL": "https://api.deepseek.com/v1/",
				"OPENAI_API_KEY":  "key",
				"OPENAI_MODEL":    "deepseek-chat",
			}),
			check: func(t *testing.T, c Config) {
				if c.LLM.OpenAI.BaseURL != "https://api.deepseek.com/v1" {
					t.Errorf("BaseURL = %q, ожидался без хвостового слэша", c.LLM.OpenAI.BaseURL)
				}
				if c.LLM.Model() != "deepseek-chat" {
					t.Errorf("Model() = %q", c.LLM.Model())
				}
			},
		},
		{
			name: "openai без адреса, ключа и модели",
			env: merge(without(baseValid(), "GIGACHAT_AUTH_KEY"), map[string]string{
				"LLM_PROVIDER":    "openai",
				"OPENAI_BASE_URL": "api.deepseek.com",
			}),
			wantErr:    true,
			errSubstrs: []string{"OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL"},
		},
		{
			name: "тексты: \\n становится переносом, дефолты подставлены",
			env:  baseValid(),
			check: func(t *testing.T, c Config) {
				if c.BusinessContacts != "+7 900 000-00-00\nул. Примерная, 1" {
					t.Errorf("BusinessContacts = %q, ожидался настоящий перенос строки", c.BusinessContacts)
				}
				if !strings.Contains(c.Texts.Greeting, "Фотостудия «Кадр»") {
					t.Errorf("приветствие по умолчанию без названия бизнеса: %q", c.Texts.Greeting)
				}
				if c.Texts.ButtonLead == "" || c.Texts.LeadSent == "" || c.Texts.Consent != "" {
					t.Errorf("неожиданные дефолты текстов: %+v", c.Texts)
				}
				if !c.LeadOfferEnabled || c.LeadsPerHour != defaultLeadsPerHour || c.QuestionsPerHour != defaultQuestionsPerHour {
					t.Errorf("неожиданные дефолты: offer=%v leads=%d questions=%d", c.LeadOfferEnabled, c.LeadsPerHour, c.QuestionsPerHour)
				}
				if c.Location == nil || c.Location.String() != defaultTimezone {
					t.Errorf("Location = %v, ожидался %s", c.Location, defaultTimezone)
				}
			},
		},
		{
			name: "тарифы с десятичной запятой",
			env:  merge(baseValid(), map[string]string{"LLM_PRICE_INPUT_PER_1K": "0,065", "LLM_MIN_MONTHLY": "600"}),
			check: func(t *testing.T, c Config) {
				if c.LLM.Prices.InputPer1K != 0.065 || c.LLM.Prices.MinMonthly != 600 {
					t.Errorf("Prices = %+v", c.LLM.Prices)
				}
			},
		},
		{
			name:       "битые лимит, часовой пояс и цена",
			env:        merge(baseValid(), map[string]string{"LIMIT_LEADS_PER_HOUR": "-1", "TIMEZONE": "Mars/Base", "LLM_PRICE_OUTPUT_PER_1K": "дёшево"}),
			wantErr:    true,
			errSubstrs: []string{"LIMIT_LEADS_PER_HOUR", "TIMEZONE", "LLM_PRICE_OUTPUT_PER_1K"},
		},
		{
			name:       "gigachat без ключа",
			env:        without(baseValid(), "GIGACHAT_AUTH_KEY"),
			wantErr:    true,
			errSubstrs: []string{"GIGACHAT_AUTH_KEY"},
		},
		{
			name: "yandex без folder id",
			env: merge(without(baseValid(), "GIGACHAT_AUTH_KEY"), map[string]string{
				"LLM_PROVIDER":   "yandexgpt",
				"YANDEX_API_KEY": "apikey",
			}),
			wantErr:    true,
			errSubstrs: []string{"YANDEX_FOLDER_ID"},
		},
		{
			name: "провайдер gigachat не требует ключей yandex",
			env:  baseValid(), // ключей Yandex нет — и это не ошибка
		},
		{
			name:       "битый ADMIN_TG_ID",
			env:        merge(baseValid(), map[string]string{"ADMIN_TG_ID": "not-a-number"}),
			wantErr:    true,
			errSubstrs: []string{"ADMIN_TG_ID"},
		},
		{
			name:       "битый INSECURE флаг",
			env:        merge(baseValid(), map[string]string{"GIGACHAT_INSECURE_SKIP_VERIFY": "maybe"}),
			wantErr:    true,
			errSubstrs: []string{"GIGACHAT_INSECURE_SKIP_VERIFY"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAll(t)
			setEnv(t, tt.env)

			c, err := Load()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ожидалась ошибка, получен nil")
				}
				for _, s := range tt.errSubstrs {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("ошибка %q не содержит %q", err.Error(), s)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}

func TestLoadCoreSkipsTelegram(t *testing.T) {
	clearAll(t)
	setEnv(t, without(without(without(baseValid(), "BOT_TOKEN"), "OWNER_CHAT_ID"), "BUSINESS_CONTACTS"))
	if _, err := LoadCore(); err != nil {
		t.Fatalf("экзамену не нужны токен бота, чат владельца и контакты: %v", err)
	}
}

func TestAdminOptional(t *testing.T) {
	clearAll(t)
	setEnv(t, baseValid())
	c, err := Load()
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if c.AdminTgID != 0 {
		t.Errorf("AdminTgID = %d, без ENV ожидался 0", c.AdminTgID)
	}
}

// clearAll обнуляет все влияющие на конфиг переменные, чтобы окружение раннера
// не протекало в тест.
func clearAll(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BOT_TOKEN", "OWNER_CHAT_ID", "ADMIN_TG_ID", "DATABASE_URL", "LLM_PROVIDER",
		"GIGACHAT_AUTH_KEY", "GIGACHAT_SCOPE", "GIGACHAT_MODEL", "GIGACHAT_CERT_PATH",
		"GIGACHAT_INSECURE_SKIP_VERIFY", "YANDEX_API_KEY", "YANDEX_FOLDER_ID",
		"YANDEX_MODEL", "OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL",
		"LLM_PRICE_INPUT_PER_1K", "LLM_PRICE_OUTPUT_PER_1K", "LLM_MIN_MONTHLY",
		"BUSINESS_NAME", "BUSINESS_CONTACTS", "TIMEZONE", "LEAD_OFFER_ENABLED",
		"LIMIT_LEADS_PER_HOUR", "LIMIT_QUESTIONS_PER_HOUR", "GREETING_TEXT",
		"BUTTON_LEAD_TEXT", "BUTTON_CONTACTS_TEXT", "LEAD_SENT_TEXT", "QUESTION_SENT_TEXT",
		"DELIVERY_FAILED_TEXT", "OWNER_REPLY_PREFIX", "CONSENT_TEXT", "PRIVACY_URL",
		"KNOWLEDGE_PATH", "APP_ENV",
	} {
		t.Setenv(k, "")
	}
}

func without(m map[string]string, key string) map[string]string {
	out := merge(nil, m)
	delete(out, key)
	return out
}

func merge(base, over map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}
