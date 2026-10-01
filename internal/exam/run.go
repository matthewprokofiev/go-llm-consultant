package exam

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/matthewprokofiev/go-llm-consultant/internal/config"
	"github.com/matthewprokofiev/go-llm-consultant/internal/consultant"
	"github.com/matthewprokofiev/go-llm-consultant/internal/kbconv"
)

// Row — один вопрос прогона. Прогон хранится в CSV: его удобно открыть в Excel и
// вписать ручной вердикт в колонку «итог».
type Row struct {
	N            int
	Question     string
	Trap         bool
	Expect       Expectation
	Answer       string
	Status       string
	Auto         Verdict
	Reason       string
	Final        string // ручной вердикт; пусто — действует авто-вердикт
	InputTokens  int
	OutputTokens int
	Seconds      float64
	Model        string
}

// Verdict — итоговый вердикт: ручной, если он вписан, иначе автоматический.
func (r Row) Verdict() Verdict {
	if v, ok := ParseVerdict(r.Final); ok {
		return v
	}
	return r.Auto
}

var csvHeader = []string{"№", "вопрос", "тип", "ожидание", "ответ бота", "статус", "авто-вердикт", "пояснение", "итог", "вход, токенов", "выход, токенов", "время, с", "модель"}

// WriteCSV пишет прогон в виде, который Excel открывает без мастера импорта:
// UTF-8 с BOM, разделитель «;», десятичная запятая.
func WriteCSV(w io.Writer, rows []Row) error {
	if _, err := io.WriteString(w, "\xef\xbb\xbf"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	cw.UseCRLF = true
	if err := cw.Write(csvHeader); err != nil {
		return err
	}
	for _, r := range rows {
		kind := "обычный"
		if r.Trap {
			kind = "подвох"
		}
		if err := cw.Write([]string{
			strconv.Itoa(r.N), r.Question, kind, r.Expect.String(), r.Answer, r.Status, string(r.Auto), r.Reason, r.Final,
			strconv.Itoa(r.InputTokens), strconv.Itoa(r.OutputTokens),
			strings.Replace(strconv.FormatFloat(r.Seconds, 'f', 1, 64), ".", ",", 1), r.Model,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func ReadCSV(data []byte) ([]Row, error) {
	records, err := kbconv.ReadCSV(data)
	if err != nil {
		return nil, err
	}
	var rows []Row
	for i, rec := range records {
		if i == 0 || len(rec) == 0 || strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		if len(rec) < len(csvHeader) {
			return nil, fmt.Errorf("строка %d: ожидалось %d столбцов, найдено %d — это файл прогона экзамена?", i+1, len(csvHeader), len(rec))
		}
		n, _ := strconv.Atoi(rec[0])
		in, _ := strconv.Atoi(rec[9])
		out, _ := strconv.Atoi(rec[10])
		secs, _ := strconv.ParseFloat(strings.Replace(rec[11], ",", ".", 1), 64)
		rows = append(rows, Row{
			N: n, Question: rec[1], Trap: rec[2] == "подвох", Expect: ParseExpectation(rec[3]),
			Answer: rec[4], Status: rec[5], Auto: Verdict(rec[6]), Reason: rec[7], Final: rec[8],
			InputTokens: in, OutputTokens: out, Seconds: secs, Model: rec[12],
		})
	}
	return rows, nil
}

// Diff — что изменилось с прошлого прогона: нужно после правки базы, чтобы видеть,
// что исправилось и что сломалось.
func Diff(prev, cur []Row) []string {
	before := map[string]Row{}
	for _, r := range prev {
		before[r.Question] = r
	}
	var changes []string
	for _, r := range cur {
		p, ok := before[r.Question]
		switch {
		case !ok:
			changes = append(changes, fmt.Sprintf("№%d новый вопрос: %s", r.N, r.Verdict()))
		case p.Verdict() != r.Verdict():
			changes = append(changes, fmt.Sprintf("№%d «%s»: %s → %s", r.N, short(r.Question), p.Verdict(), r.Verdict()))
		case p.Answer != r.Answer:
			changes = append(changes, fmt.Sprintf("№%d «%s»: ответ изменился, вердикт тот же (%s)", r.N, short(r.Question), r.Verdict()))
		}
	}
	return changes
}

func short(s string) string {
	r := []rune(s)
	if len(r) <= 50 {
		return s
	}
	return string(r[:50]) + "…"
}

// Usage — средний расход на ответ по прогону. Вопросы, на которые нейросеть не
// ответила, в среднее не входят.
type Usage struct {
	Answers    int
	AvgIn      float64
	AvgOut     float64
	AvgSeconds float64
}

func Measure(rows []Row) Usage {
	var u Usage
	for _, r := range rows {
		if r.InputTokens+r.OutputTokens == 0 {
			continue
		}
		u.Answers++
		u.AvgIn += float64(r.InputTokens)
		u.AvgOut += float64(r.OutputTokens)
		u.AvgSeconds += r.Seconds
	}
	if u.Answers > 0 {
		n := float64(u.Answers)
		u.AvgIn, u.AvgOut, u.AvgSeconds = u.AvgIn/n, u.AvgOut/n, u.AvgSeconds/n
	}
	return u
}

func (u Usage) CostPerAnswer(p config.Prices) float64 {
	return u.AvgIn/1000*p.InputPer1K + u.AvgOut/1000*p.OutputPer1K
}

// Monthly — расход за месяц при потоке questions и сумма к оплате с учётом
// минимального платежа провайдера.
func (u Usage) Monthly(questions int, p config.Prices) (usage, pay float64) {
	usage = u.CostPerAnswer(p) * float64(questions)
	return usage, max(usage, p.MinMonthly)
}

// Summary — сводка для меня: качество, расход и прикидка стоимости в месяц.
// На ней держится обещание «до заказа прикину расход на нейросеть».
func Summary(rows []Row, p config.Prices) string {
	var b bytes.Buffer
	correct, wrong := 0, 0
	var manual []string
	model := ""
	for _, r := range rows {
		switch v := r.Verdict(); {
		case v.Correct():
			correct++
		case v == VerdictError:
			wrong++
		default:
			manual = append(manual, strconv.Itoa(r.N))
		}
		model = r.Model
	}
	fmt.Fprintf(&b, "Модель: %s\n", model)
	fmt.Fprintf(&b, "Верно: %d из %d, ошибок: %d", correct, len(rows), wrong)
	if len(manual) > 0 {
		fmt.Fprintf(&b, ", проверить вручную: %d (№ %s)", len(manual), strings.Join(manual, ", "))
	}
	b.WriteString("\n\n")

	u := Measure(rows)
	if u.Answers == 0 {
		b.WriteString("Нейросеть не ответила ни на один вопрос — расход не посчитан.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Среднее на ответ: вход %.0f токенов, выход %.0f, время %.1f с\n", u.AvgIn, u.AvgOut, u.AvgSeconds)
	if p.InputPer1K == 0 && p.OutputPer1K == 0 {
		b.WriteString("Тарифы не заданы (LLM_PRICE_INPUT_PER_1K, LLM_PRICE_OUTPUT_PER_1K, LLM_MIN_MONTHLY) — стоимость не посчитана.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Стоимость ответа: %.2f ₽ (вход %g ₽, выход %g ₽ за 1 000 токенов)\n", u.CostPerAnswer(p), p.InputPer1K, p.OutputPer1K)
	if p.MinMonthly > 0 {
		fmt.Fprintf(&b, "Минимальный платёж провайдера: %.0f ₽ в месяц\n", p.MinMonthly)
	}
	b.WriteString("В месяц:\n")
	for _, n := range []int{300, 1000, 3000} {
		usage, pay := u.Monthly(n, p)
		fmt.Fprintf(&b, "  %5d вопросов — расход %.0f ₽", n, usage)
		if pay > usage {
			fmt.Fprintf(&b, ", к оплате %.0f ₽ (минимальный платёж)", pay)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Evaluate — строка прогона по ответу бота.
func Evaluate(n int, q Question, r consultant.Reply, model string) Row {
	v, reason := Judge(q, r)
	return Row{
		N: n, Question: q.Text, Trap: q.Trap, Expect: q.Expect,
		Answer: r.Text, Status: statusText(r.Status), Auto: v, Reason: reason,
		InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, Seconds: r.Duration.Seconds(), Model: model,
	}
}

// Failed — строка прогона, когда нейросеть не ответила: это сбой связи, а не
// качество бота, поэтому вердикт не «ошибка», а ручная проверка.
func Failed(n int, q Question, err error, model string) Row {
	return Row{
		N: n, Question: q.Text, Trap: q.Trap, Expect: q.Expect, Model: model,
		Auto: VerdictManual, Reason: "нейросеть не ответила, перезапустите экзамен: " + err.Error(),
	}
}
