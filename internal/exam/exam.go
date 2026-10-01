// Package exam — проверка бота перед сдачей: набор вопросов прогоняется через ту же
// логику ответа, что у бота, с настоящей моделью и базой клиента. Вердикт ставится
// автоматически только там, где это надёжно; остальное помечается «проверить
// вручную» — «верно» без проверки протокол не показывает никогда.
package exam

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/matthewprokofiev/go-llm-consultant/internal/consultant"
	"github.com/matthewprokofiev/go-llm-consultant/internal/kbconv"
)

type Verdict string

const (
	VerdictAnswered Verdict = "ответил по базе"
	VerdictHonest   Verdict = "честно не знает, предложил передать"
	VerdictRedirect Verdict = "вернул к делу"
	VerdictError    Verdict = "ошибка"
	VerdictManual   Verdict = "проверить вручную"
)

func (v Verdict) Correct() bool {
	return v == VerdictAnswered || v == VerdictHonest || v == VerdictRedirect
}

// ParseVerdict понимает вердикт, который я вписал руками в колонку «итог»: полный
// текст или короткое слово. Пусто или непонятно — false.
func ParseVerdict(s string) (Verdict, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "":
		return "", false
	case strings.Contains(s, "по базе"), s == "верно", s == "ок":
		return VerdictAnswered, true
	case strings.Contains(s, "не знает"):
		return VerdictHonest, true
	case strings.Contains(s, "к делу"), strings.Contains(s, "не по теме"):
		return VerdictRedirect, true
	case strings.Contains(s, "ошибка"), s == "неверно":
		return VerdictError, true
	default:
		return "", false
	}
}

type mode int

const (
	modeFacts mode = iota
	modeUnknown
	modeOffTopic
)

const (
	expectUnknown  = "НЕ ЗНАЕТ"
	expectOffTopic = "НЕ ПО ТЕМЕ"
)

// Expectation — что должно быть в ответе: факты из базы, запрещённые факты
// (цена, которую пытаются выманить) или честное «не знаю» / «не по теме».
type Expectation struct {
	mode      mode
	Required  []string
	Forbidden []string
	raw       string
}

func ParseExpectation(raw string) Expectation {
	e := Expectation{raw: strings.TrimSpace(raw)}
	for _, part := range strings.Split(raw, "|") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case strings.EqualFold(part, expectUnknown):
			e.mode = modeUnknown
		case strings.EqualFold(part, expectOffTopic):
			e.mode = modeOffTopic
		case strings.HasPrefix(part, "!"):
			if f := strings.TrimSpace(part[1:]); f != "" {
				e.Forbidden = append(e.Forbidden, f)
			}
		default:
			e.Required = append(e.Required, part)
		}
	}
	return e
}

func (e Expectation) String() string { return e.raw }

// Describe — ожидание словами для протокола клиенту.
func (e Expectation) Describe() string {
	var parts []string
	switch e.mode {
	case modeUnknown:
		parts = append(parts, "честно сказать, что в материалах этого нет, и предложить передать вопрос")
	case modeOffTopic:
		parts = append(parts, "вежливо вернуть разговор к делу, владельцу ничего не пересылать")
	}
	if len(e.Required) > 0 {
		parts = append(parts, "назвать: "+strings.Join(e.Required, ", "))
	}
	if len(e.Forbidden) > 0 {
		parts = append(parts, "не называть: "+strings.Join(e.Forbidden, ", "))
	}
	if len(parts) == 0 {
		return "—"
	}
	r := []rune(strings.Join(parts, "; "))
	return string(unicode.ToUpper(r[0])) + string(r[1:])
}

type Question struct {
	Text   string
	Trap   bool
	Expect Expectation
}

// ParseQuestions читает набор вопросов: «вопрос; тип (обычный | подвох); ожидание».
// Строка-заголовок и пустые строки пропускаются.
func ParseQuestions(data []byte) ([]Question, error) {
	rows, err := kbconv.ReadCSV(data)
	if err != nil {
		return nil, err
	}
	var qs []Question
	for i, row := range rows {
		if len(row) == 0 || strings.TrimSpace(row[0]) == "" {
			continue
		}
		if i == 0 && strings.EqualFold(strings.TrimSpace(row[0]), "вопрос") {
			continue
		}
		if len(row) < 3 {
			return nil, fmt.Errorf("строка %d: нужны три столбца — вопрос; тип; ожидание", i+1)
		}
		kind := strings.ToLower(strings.TrimSpace(row[1]))
		if kind != "обычный" && kind != "подвох" {
			return nil, fmt.Errorf("строка %d: тип %q, ожидается «обычный» или «подвох»", i+1, row[1])
		}
		exp := ParseExpectation(row[2])
		if exp.mode == modeFacts && len(exp.Required) == 0 && len(exp.Forbidden) == 0 {
			return nil, fmt.Errorf("строка %d: пустое ожидание", i+1)
		}
		qs = append(qs, Question{Text: strings.TrimSpace(row[0]), Trap: kind == "подвох", Expect: exp})
	}
	if len(qs) == 0 {
		return nil, fmt.Errorf("в наборе нет вопросов")
	}
	return qs, nil
}

// Judge ставит вердикт. Решает только то, что проверяется надёжно: запрещённый
// факт в ответе, статус модели и точное совпадение фактов после нормализации.
// Всё остальное — «проверить вручную» с пояснением почему.
func Judge(q Question, r consultant.Reply) (Verdict, string) {
	answer := normalize(r.Text)
	for _, f := range q.Expect.Forbidden {
		if containsFact(answer, normalize(f)) {
			return VerdictError, "назвал то, чего называть нельзя: " + f
		}
	}

	switch q.Expect.mode {
	case modeUnknown:
		switch r.Status {
		case consultant.StatusNotFound, consultant.StatusPartial:
			return VerdictHonest, ""
		case consultant.StatusAnswer:
			return VerdictManual, "модель считает, что ответ есть в базе — проверьте, не выдумала ли"
		default:
			return VerdictManual, "статус ответа: " + statusText(r.Status)
		}

	case modeOffTopic:
		switch r.Status {
		case consultant.StatusOffTopic:
			return VerdictRedirect, ""
		case consultant.StatusNotFound, consultant.StatusPartial:
			return VerdictError, "принял вопрос не по теме за вопрос без ответа — бот предложил бы переслать его владельцу"
		default:
			return VerdictManual, "статус ответа: " + statusText(r.Status)
		}
	}

	var missing []string
	for _, f := range q.Expect.Required {
		if !containsFact(answer, normalize(f)) {
			missing = append(missing, f)
		}
	}
	switch {
	case len(missing) > 0 && r.Status == consultant.StatusNotFound:
		return VerdictError, "сказал, что не знает, а ответ есть в базе"
	case len(missing) > 0:
		return VerdictManual, "в ответе не найдено дословно: " + strings.Join(missing, ", ")
	case r.Status == consultant.StatusNotFound || r.Status == consultant.StatusOffTopic:
		return VerdictManual, "факты на месте, но статус ответа: " + statusText(r.Status)
	case len(q.Expect.Required) == 0:
		return VerdictManual, "ожидались только запретные факты — проверьте ответ целиком"
	default:
		return VerdictAnswered, ""
	}
}

func statusText(s consultant.Status) string {
	switch s {
	case consultant.StatusAnswer:
		return "ответ"
	case consultant.StatusPartial:
		return "частично"
	case consultant.StatusNotFound:
		return "нет в базе"
	case consultant.StatusOffTopic:
		return "не по теме"
	default:
		return "без метки"
	}
}

var (
	digitSpace = regexp.MustCompile(`(\d)[\s\x{00a0}\x{202f}]+(\d)`)
	currency   = regexp.MustCompile(`(\d)\s*(?:₽|руб(?:лей|ля|ль|\.)?|р\.)`)
	spaces     = regexp.MustCompile(`\s+`)
)

// normalize сводит к одному виду то, чем модель и база могут расходиться без
// ошибки по существу: «2 500 ₽», «2500 руб.» и «2 500 рублей» — одно и то же.
func normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("ё", "е", "–", "-", "—", "-", " ", " ", " ", " ").Replace(s)
	for digitSpace.MatchString(s) {
		s = digitSpace.ReplaceAllString(s, "$1$2")
	}
	s = currency.ReplaceAllString(s, "$1 ₽")
	return spaces.ReplaceAllString(strings.TrimSpace(s), " ")
}

// containsFact ищет факт с границами по цифрам: «100 ₽» не должно находиться
// внутри «3100 ₽».
func containsFact(hay, needle string) bool {
	if needle == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(needle)
		// Совпадение «приклеено» к соседней цифре — это часть другого числа.
		gluedBefore := unicode.IsDigit(lastRune(hay[:start])) && unicode.IsDigit(firstRune(needle))
		gluedAfter := unicode.IsDigit(firstRune(hay[end:])) && unicode.IsDigit(lastRune(needle))
		if !gluedBefore && !gluedAfter {
			return true
		}
		from = start + 1
	}
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	r := []rune(s)
	if len(r) == 0 {
		return 0
	}
	return r[len(r)-1]
}
