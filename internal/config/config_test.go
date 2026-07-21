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
		"DATABASE_URL":      "postgres://u:p@localhost:5432/db",
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
				if c.Provider != ProviderGigaChat {
					t.Errorf("Provider = %q, ожидался gigachat", c.Provider)
				}
				if c.GigaChat.Scope != defaultGigaChatScope {
					t.Errorf("Scope = %q, ожидался дефолт %q", c.GigaChat.Scope, defaultGigaChatScope)
				}
				if c.GigaChat.Model != defaultGigaChatModel {
					t.Errorf("Model = %q, ожидался дефолт", c.GigaChat.Model)
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
			env: map[string]string{
				"BOT_TOKEN":        "token",
				"DATABASE_URL":     "postgres://x",
				"LLM_PROVIDER":     "yandexgpt",
				"YANDEX_API_KEY":   "apikey",
				"YANDEX_FOLDER_ID": "folder",
			},
			check: func(t *testing.T, c Config) {
				if c.Yandex.Model != defaultYandexModel {
					t.Errorf("Model = %q, ожидался дефолт", c.Yandex.Model)
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
			name:       "нет DATABASE_URL",
			env:        without(baseValid(), "DATABASE_URL"),
			wantErr:    true,
			errSubstrs: []string{"DATABASE_URL"},
		},
		{
			name:       "неизвестный провайдер",
			env:        merge(baseValid(), map[string]string{"LLM_PROVIDER": "openai"}),
			wantErr:    true,
			errSubstrs: []string{"LLM_PROVIDER", "openai"},
		},
		{
			name:       "gigachat без ключа",
			env:        without(baseValid(), "GIGACHAT_AUTH_KEY"),
			wantErr:    true,
			errSubstrs: []string{"GIGACHAT_AUTH_KEY"},
		},
		{
			name: "yandex без folder id",
			env: map[string]string{
				"BOT_TOKEN":      "token",
				"DATABASE_URL":   "postgres://x",
				"LLM_PROVIDER":   "yandexgpt",
				"YANDEX_API_KEY": "apikey",
			},
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
		"BOT_TOKEN", "ADMIN_TG_ID", "DATABASE_URL", "LLM_PROVIDER",
		"GIGACHAT_AUTH_KEY", "GIGACHAT_SCOPE", "GIGACHAT_MODEL", "GIGACHAT_CERT_PATH",
		"GIGACHAT_INSECURE_SKIP_VERIFY", "YANDEX_API_KEY", "YANDEX_FOLDER_ID",
		"YANDEX_MODEL", "KNOWLEDGE_PATH", "APP_ENV",
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
