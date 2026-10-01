package telegram

import (
	"errors"
	"strings"
	"testing"

	"github.com/matthewprokofiev/go-llm-consultant/internal/consultant"
	"github.com/matthewprokofiev/go-llm-consultant/internal/storage"
)

func TestChooseOffer(t *testing.T) {
	tests := []struct {
		name       string
		reply      consultant.Reply
		enabled    bool
		offerShown bool
		want       offer
	}{
		{"нет в базе → передать", consultant.Reply{Status: consultant.StatusNotFound}, true, false, offerHandoff},
		{"частично → передать остальное", consultant.Reply{Status: consultant.StatusPartial}, true, true, offerHandoff},
		{"намерение → заявка", consultant.Reply{Status: consultant.StatusAnswer, Intent: true}, true, false, offerLead},
		{"заявку уже предлагали", consultant.Reply{Status: consultant.StatusAnswer, Intent: true}, true, true, offerNone},
		{"предложение выключено", consultant.Reply{Status: consultant.StatusAnswer, Intent: true}, false, false, offerNone},
		{"обычный ответ", consultant.Reply{Status: consultant.StatusAnswer}, true, false, offerNone},
		{"не по теме — владельцу ничего", consultant.Reply{Status: consultant.StatusOffTopic, Intent: true}, true, false, offerNone},
		{"без метки — ничего", consultant.Reply{Status: consultant.StatusUnknown}, true, false, offerNone},
	}
	for _, tt := range tests {
		if got := chooseOffer(tt.reply, tt.enabled, tt.offerShown); got != tt.want {
			t.Errorf("%s: chooseOffer = %v, ожидалось %v", tt.name, got, tt.want)
		}
	}
}

func TestRedactHidesTokenAndBody(t *testing.T) {
	redact := newRedactor("123:SECRET")

	netErr := errors.New(`error do request for method getUpdates, Post "https://api.telegram.org/bot123:SECRET/getUpdates": dial tcp: i/o timeout`)
	if got := redact(netErr); strings.Contains(got, "SECRET") || !strings.Contains(got, "i/o timeout") {
		t.Errorf("токен не вырезан или потеряна причина: %q", got)
	}

	bodyErr := errors.New(`error decode response body for method getUpdates, {"ok":true,"result":[{"message":{"text":"Анна +79001234567"}}]}, unexpected EOF`)
	got := redact(bodyErr)
	if strings.Contains(got, "Анна") || strings.Contains(got, "+7900") || !strings.Contains(got, "getUpdates") {
		t.Errorf("тело ответа с личными данными попало в лог: %q", got)
	}
}

func TestFormatStats(t *testing.T) {
	week := storage.Summary{Counts: map[string]int{"answer": 5, "not_found": 2, storage.EventLLMError: 1, storage.EventLeadSent: 3}, InputTokens: 9000}
	month := storage.Summary{Counts: map[string]int{"answer": 20, "not_found": 4, storage.EventLLMError: 1, storage.EventLeadSent: 7}, InputTokens: 30000}

	got := formatStats(week, month)
	for _, want := range []string{"Вопросов: 8 / 25", "нет в базе: 2 / 4", "Заявки доставлены: 3 / 7", "Токены на вход: 9000 / 30000"} {
		if !strings.Contains(got, want) {
			t.Errorf("в сводке нет %q:\n%s", want, got)
		}
	}
}
