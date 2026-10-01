package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"+7 (900) 123-45-67", "+79001234567", true},
		{"89001234567", "+79001234567", true},
		{"9001234567", "+79001234567", true},
		{"8 800 555-35-35", "+78005553535", true},
		{"79001234567", "+79001234567", true}, // контакт из Telegram приходит без «+»
		{"+375 29 123-45-67", "+375291234567", true},
		{"12345", "", false},
		{"+7 000 000-00-00", "", false},
		{"11111111111", "", false},
		{"+7 123 456-78-90", "", false}, // кода 1xx после +7 не бывает
		{"мой номер 89001234567", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := normalizePhone(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("normalizePhone(%q) = %q, %v; ожидалось %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLooksLikePhoneAttempt(t *testing.T) {
	for in, want := range map[string]bool{
		"8900123":               true,
		"мой номер 89001234567": true,
		"Какой у вас адрес?":    false,
		"Можно на 25 мая?":      false,
	} {
		if got := looksLikePhoneAttempt(in); got != want {
			t.Errorf("looksLikePhoneAttempt(%q) = %v, ожидалось %v", in, got, want)
		}
	}
}

func TestLooksLikeQuestion(t *testing.T) {
	for in, want := range map[string]bool{
		"Анна": false,
		"Анна Сергеевна Петрова":       false,
		"а парковка у вас есть?":       true,
		"сначала скажите где вы точно": true,
	} {
		if got := looksLikeQuestion(in); got != want {
			t.Errorf("looksLikeQuestion(%q) = %v, ожидалось %v", in, got, want)
		}
	}
}

func TestFormViaButton(t *testing.T) {
	f := &leadForm{kind: formLead, step: stepInterest}

	if act := f.apply(formInput{text: "Семейная съёмка в субботу"}); act != actPrompt || f.step != stepPhone {
		t.Fatalf("после интереса: act=%v step=%v", act, f.step)
	}
	if act := f.apply(formInput{text: "8 (900) 123-45-67"}); act != actPrompt || f.step != stepName || f.phone != "+79001234567" {
		t.Fatalf("после телефона: act=%v step=%v phone=%q", act, f.step, f.phone)
	}
	if act := f.apply(formInput{text: "Анна"}); act != actSubmit || f.name != "Анна" {
		t.Fatalf("после имени: act=%v name=%q", act, f.name)
	}
	if f.topic != "Семейная съёмка в субботу" {
		t.Errorf("topic = %q", f.topic)
	}
}

func TestFormSharedContactSkipsName(t *testing.T) {
	f := &leadForm{kind: formLead, step: stepPhone, topic: "сколько стоит съёмка и можно ли на субботу?"}
	act := f.apply(formInput{contact: &models.Contact{PhoneNumber: "79001234567", FirstName: "Анна", LastName: "П."}})
	if act != actSubmit || f.name != "Анна П." || f.phone != "+79001234567" {
		t.Errorf("контакт: act=%v name=%q phone=%q", act, f.name, f.phone)
	}
}

func TestFormQuestionInTheMiddle(t *testing.T) {
	f := &leadForm{kind: formLead, step: stepPhone, topic: "портрет"}
	if act := f.apply(formInput{text: "А где вы находитесь?"}); act != actQuestion || f.step != stepPhone {
		t.Errorf("вопрос на шаге телефона: act=%v step=%v", act, f.step)
	}
	if act := f.apply(formInput{text: "8900123"}); act != actBadPhone {
		t.Errorf("короткий номер: act=%v, ожидалась просьба исправить", act)
	}

	f.step = stepName
	if act := f.apply(formInput{text: "а парковка есть?"}); act != actQuestion || f.name != "" {
		t.Errorf("вопрос на шаге имени: act=%v name=%q", act, f.name)
	}
}

func TestFormIgnoresEmptyAndStrayContact(t *testing.T) {
	f := &leadForm{kind: formLead, step: stepName, phone: "+79001234567"}
	if act := f.apply(formInput{}); act != actPrompt {
		t.Errorf("пустой ввод (стикер): act=%v", act)
	}
	if act := f.apply(formInput{contact: &models.Contact{PhoneNumber: "79001234567"}}); act != actPrompt || f.name != "" {
		t.Errorf("контакт не на шаге телефона: act=%v name=%q", act, f.name)
	}
}

func TestOwnerMessage(t *testing.T) {
	at := time.Date(2026, 9, 30, 14, 5, 0, 0, time.UTC)

	lead := ownerMessage(&leadForm{kind: formLead, topic: "Семейная съёмка", name: "Анна", phone: "+79001234567"}, at)
	for _, want := range []string{ownerHeaderLead, "Интересует: Семейная съёмка", "Имя: Анна", "Телефон: +7 900 123-45-67", "30.09.2026 14:05", ownerReplyHint} {
		if !strings.Contains(lead, want) {
			t.Errorf("в заявке нет %q:\n%s", want, lead)
		}
	}
	if !isOwnerMessage(lead) {
		t.Error("isOwnerMessage не узнаёт заявку")
	}

	q := ownerMessage(&leadForm{kind: formQuestion, topic: "Есть студенческая скидка?", context: "Сколько стоит портрет?", phone: "+375291234567"}, at)
	for _, want := range []string{ownerHeaderQuestion, "Вопрос: Есть студенческая скидка?", "Предыдущий вопрос: Сколько стоит портрет?", "Имя: не указано", "Телефон: +375291234567"} {
		if !strings.Contains(q, want) {
			t.Errorf("в вопросе нет %q:\n%s", want, q)
		}
	}
}

func TestKBOfferRoundTrip(t *testing.T) {
	q, a, ok := parseKBOffer(kbOffer("Есть парковка?", "Да.\nВо дворе, бесплатно."))
	if !ok || q != "Есть парковка?" || a != "Да.\nВо дворе, бесплатно." {
		t.Errorf("parseKBOffer = %q, %q, %v", q, a, ok)
	}
	if _, _, ok := parseKBOffer("✅ Ответ отправлен клиенту."); ok {
		t.Error("обычное подтверждение — не предложение")
	}
}

func TestQuestionFromOwnerMessage(t *testing.T) {
	at := time.Now()
	q := ownerMessage(&leadForm{kind: formQuestion, topic: "Есть\nпарковка?", phone: "+79001234567"}, at)
	if got := questionFromOwnerMessage(q); got != "Есть парковка?" {
		t.Errorf("вопрос из сообщения владельцу = %q", got)
	}
	lead := ownerMessage(&leadForm{kind: formLead, topic: "Портрет", phone: "+79001234567"}, at)
	if got := questionFromOwnerMessage(lead); got != "" {
		t.Errorf("из заявки вопрос для базы не берём, получено %q", got)
	}
}
