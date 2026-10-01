package telegram

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/go-telegram/bot/models"
)

// Одна форма на два случая: заявка и вопрос, на который в базе нет ответа. По сути
// вопрос без ответа — это заявка, где вместо «что интересует» сам вопрос.
type formKind int

const (
	formLead formKind = iota
	formQuestion
)

type formStep int

const (
	stepInterest formStep = iota
	stepPhone
	stepName
)

const (
	maxTopicRunes = 300
	maxNameRunes  = 60
)

type leadForm struct {
	kind    formKind
	step    formStep
	topic   string // что интересует или вопрос без ответа
	context string // предыдущий вопрос — короткий контекст для владельца
	phone   string
	name    string
}

type formInput struct {
	text    string
	contact *models.Contact
}

type formAction int

const (
	actPrompt   formAction = iota // спросить (или переспросить) текущий шаг
	actBadPhone                   // номер явно неправдоподобный — вежливо попросить исправить
	actQuestion                   // посреди заявки задан вопрос — ответить и вернуться к шагу
	actSubmit                     // всё собрано — передавать владельцу
)

// apply применяет ввод к текущему шагу. Отмена и «Пропустить» разбираются раньше,
// в боте: они зависят от текста кнопок.
func (f *leadForm) apply(in formInput) formAction {
	text := strings.TrimSpace(in.text)
	if (in.contact != nil && f.step != stepPhone) || (in.contact == nil && text == "") {
		return actPrompt
	}

	switch f.step {
	case stepInterest:
		f.topic = truncate(text, maxTopicRunes)
		f.step = stepPhone
		return actPrompt

	case stepPhone:
		if in.contact != nil {
			phone, ok := normalizePhone(in.contact.PhoneNumber)
			if !ok {
				return actBadPhone
			}
			f.phone = phone
			// Контакт из Telegram приходит с именем — второй раз его не спрашиваем.
			f.name = truncate(strings.TrimSpace(in.contact.FirstName+" "+in.contact.LastName), maxNameRunes)
			if f.name != "" {
				return actSubmit
			}
			f.step = stepName
			return actPrompt
		}
		if phone, ok := normalizePhone(text); ok {
			f.phone = phone
			f.step = stepName
			return actPrompt
		}
		if looksLikePhoneAttempt(text) {
			return actBadPhone
		}
		return actQuestion

	default: // stepName
		if looksLikeQuestion(text) {
			return actQuestion
		}
		f.name = truncate(text, maxNameRunes)
		return actSubmit
	}
}

// normalizePhone приводит номер к виду +79001234567. Российские 8XXXXXXXXXX и
// 9XXXXXXXXX дополняются до +7. Явно неправдоподобные номера отклоняются: буквы,
// слишком мало или много цифр, одна цифра подряд, +7 с несуществующим кодом.
func normalizePhone(s string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case strings.ContainsRune(" +-(). ", r):
		default:
			return "", false
		}
	}
	d := b.String()
	switch {
	case len(d) == 11 && d[0] == '8':
		d = "7" + d[1:]
	case len(d) == 10 && d[0] == '9':
		d = "7" + d
	}
	if len(d) < 10 || len(d) > 15 || strings.Count(d, d[:1]) == len(d) {
		return "", false
	}
	// В России и Казахстане после +7 идут коды 3xx, 4xx, 7xx, 8xx и 9xx.
	if len(d) == 11 && d[0] == '7' && !strings.ContainsRune("34789", rune(d[1])) {
		return "", false
	}
	return "+" + d, true
}

// looksLikePhoneAttempt — человек пытался ввести номер, но ошибся: цифр много и
// они составляют больше половины текста.
func looksLikePhoneAttempt(s string) bool {
	digits, total := 0, 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if unicode.IsDigit(r) {
			digits++
		}
	}
	return digits >= 5 && digits*2 > total
}

// looksLikeQuestion — на шаге имени пришло не имя, а вопрос: есть «?» или слов больше,
// чем бывает в имени.
func looksLikeQuestion(s string) bool {
	return strings.Contains(s, "?") || len(strings.Fields(s)) > 3
}

// formatPhone делает российский номер удобным для глаз владельца; Telegram всё
// равно распознаёт его как телефон и даёт позвонить в одно касание.
func formatPhone(p string) string {
	if len(p) == 12 && strings.HasPrefix(p, "+7") {
		return fmt.Sprintf("+7 %s %s-%s-%s", p[2:5], p[5:8], p[8:10], p[10:12])
	}
	return p
}

const (
	ownerHeaderLead     = "📝 Новая заявка"
	ownerHeaderQuestion = "❓ Вопрос без ответа"
	ownerReplyHint      = "Ответьте на это сообщение (reply) — бот перешлёт ответ клиенту."
)

// ownerMessage — сообщение владельцу, которое читается с первого взгляда.
func ownerMessage(f *leadForm, at time.Time) string {
	name := f.name
	if name == "" {
		name = "не указано"
	}

	var b strings.Builder
	if f.kind == formQuestion {
		b.WriteString(ownerHeaderQuestion + "\n")
		// Одной строкой: из этой строки вопрос потом берётся для базы знаний.
		fmt.Fprintf(&b, "%s%s\n", questionPrefix, strings.Join(strings.Fields(f.topic), " "))
		if f.context != "" {
			fmt.Fprintf(&b, "Предыдущий вопрос: %s\n", truncate(f.context, maxTopicRunes))
		}
	} else {
		b.WriteString(ownerHeaderLead + "\n")
		fmt.Fprintf(&b, "Интересует: %s\n", f.topic)
	}
	fmt.Fprintf(&b, "Имя: %s\n", name)
	fmt.Fprintf(&b, "Телефон: %s\n", formatPhone(f.phone))
	fmt.Fprintf(&b, "Время: %s\n\n", at.Format("02.01.2006 15:04"))
	b.WriteString(ownerReplyHint)
	return b.String()
}

func isOwnerMessage(text string) bool {
	return strings.HasPrefix(text, ownerHeaderLead) || strings.HasPrefix(text, ownerHeaderQuestion)
}

const (
	questionPrefix = "Вопрос: "
	answerPrefix   = "Ответ: "
	kbOfferHeader  = "📚 Добавить в базу знаний?"
)

// questionFromOwnerMessage достаёт вопрос из «❓ Вопрос без ответа». Для заявок —
// пусто: ответ на заявку личный, в базу его не предлагаем.
func questionFromOwnerMessage(text string) string {
	if !strings.HasPrefix(text, ownerHeaderQuestion) {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		if q, ok := strings.CutPrefix(line, questionPrefix); ok {
			return strings.TrimSpace(q)
		}
	}
	return ""
}

// kbOffer — предложение владельцу добавить ответ в базу. Вопрос и ответ живут в
// самом сообщении: кнопка берёт их оттуда и работает даже после перезапуска бота.
func kbOffer(question, answer string) string {
	return "✅ Ответ отправлен клиенту.\n\n" + kbOfferHeader +
		" Бот будет так отвечать всем, кто спросит похожее. Проверьте, что в ответе нет имени клиента и личных договорённостей." +
		" Чтобы записать другую формулировку, ответьте на это сообщение новым текстом.\n\n" +
		questionPrefix + question + "\n" + answerPrefix + answer
}

// parseKBOffer разбирает предложение обратно. ok=false — это не предложение.
func parseKBOffer(text string) (question, answer string, ok bool) {
	if !strings.Contains(text, kbOfferHeader) {
		return "", "", false
	}
	_, rest, found := strings.Cut(text, "\n"+questionPrefix)
	if !found {
		return "", "", false
	}
	question, answer, found = strings.Cut(rest, "\n"+answerPrefix)
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(question), strings.TrimSpace(answer), true
}

func truncate(s string, maxRunes int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= maxRunes {
		return string(r)
	}
	return strings.TrimSpace(string(r[:maxRunes])) + "…"
}
