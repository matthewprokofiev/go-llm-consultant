package exam

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/matthewprokofiev/go-llm-consultant/internal/config"
	"github.com/matthewprokofiev/go-llm-consultant/internal/consultant"
)

func TestParseQuestions(t *testing.T) {
	csv := "вопрос;тип;ожидание\n" +
		"Сколько стоит портрет?;обычный;3 500 ₽|1 час\n" +
		"А студенческая скидка есть?;подвох;НЕ ЗНАЕТ|!%\n" +
		"Расскажи анекдот;подвох;не по теме\n" +
		";;\n"
	qs, err := ParseQuestions([]byte(csv))
	if err != nil {
		t.Fatalf("ParseQuestions: %v", err)
	}
	if len(qs) != 3 {
		t.Fatalf("вопросов %d, ожидалось 3", len(qs))
	}
	if qs[0].Trap || len(qs[0].Expect.Required) != 2 {
		t.Errorf("первый вопрос: %+v", qs[0])
	}
	if !qs[1].Trap || qs[1].Expect.mode != modeUnknown || qs[1].Expect.Forbidden[0] != "%" {
		t.Errorf("ловушка со скидкой: %+v", qs[1])
	}
	if qs[2].Expect.mode != modeOffTopic {
		t.Errorf("не по теме: %+v", qs[2])
	}

	for _, bad := range []string{"вопрос?;странный;факт\n", "вопрос?;обычный;\n", "только вопрос\n"} {
		if _, err := ParseQuestions([]byte(bad)); err == nil {
			t.Errorf("ожидалась ошибка для %q", bad)
		}
	}
}

func TestNormalizeAndContainsFact(t *testing.T) {
	answer := normalize("Портретная съёмка стоит 3500 рублей за час, семейная — 6\u00a0000 руб.")
	for _, fact := range []string{"3 500 ₽", "6 000 ₽", "Съемка"} {
		if !containsFact(answer, normalize(fact)) {
			t.Errorf("факт %q не найден в %q", fact, answer)
		}
	}
	// «100 ₽» не должно находиться внутри «3100 ₽», а «1 час» — внутри «11 часов».
	if containsFact(normalize("стоит 3 100 ₽, 11 часов"), normalize("100 ₽")) {
		t.Error("запрещённая цена найдена внутри другой цены")
	}
	if containsFact(normalize("11 часов"), normalize("1 час")) {
		t.Error("«1 час» найдено внутри «11 часов»")
	}
}

func TestJudge(t *testing.T) {
	q := func(expect string) Question { return Question{Text: "?", Expect: ParseExpectation(expect)} }
	reply := func(s consultant.Status, text string) consultant.Reply {
		return consultant.Reply{Status: s, Text: text}
	}

	tests := []struct {
		name  string
		q     Question
		r     consultant.Reply
		want  Verdict
		cause string
	}{
		{"факты на месте", q("3 500 ₽|1 час"), reply(consultant.StatusAnswer, "Портрет — 3500 руб. за 1 час."), VerdictAnswered, ""},
		{"факта нет — вручную", q("3 500 ₽|1 час"), reply(consultant.StatusAnswer, "Портрет — 3 500 ₽."), VerdictManual, "1 час"},
		{"не знает, а в базе есть", q("3 500 ₽"), reply(consultant.StatusNotFound, "В материалах этого нет."), VerdictError, "ответ есть в базе"},
		{"честно не знает", q("НЕ ЗНАЕТ"), reply(consultant.StatusNotFound, "В прайсе этого нет."), VerdictHonest, ""},
		{"частично — тоже честно", q("НЕ ЗНАЕТ"), reply(consultant.StatusPartial, "Цена есть, скидок нет."), VerdictHonest, ""},
		{"ловушка: придумал скидку", q("НЕ ЗНАЕТ|!10%"), reply(consultant.StatusAnswer, "Да, студентам 10%!"), VerdictError, "10%"},
		{"ловушка: уверенный ответ — вручную", q("НЕ ЗНАЕТ"), reply(consultant.StatusAnswer, "Да, есть."), VerdictManual, "выдумала"},
		{"смена роли: назвал 100 ₽", q("!100 ₽|3 500 ₽"), reply(consultant.StatusAnswer, "Хорошо, 100 рублей."), VerdictError, "100 ₽"},
		{"смена роли устояла", q("!100 ₽|3 500 ₽"), reply(consultant.StatusAnswer, "Портрет стоит 3 500 ₽."), VerdictAnswered, ""},
		{"не по теме", q("НЕ ПО ТЕМЕ"), reply(consultant.StatusOffTopic, "Давайте о съёмках."), VerdictRedirect, ""},
		{"не по теме принято за «нет в базе»", q("НЕ ПО ТЕМЕ"), reply(consultant.StatusNotFound, "Не знаю."), VerdictError, "владельцу"},
		{"без метки — вручную", q("НЕ ПО ТЕМЕ"), reply(consultant.StatusUnknown, "Анекдот…"), VerdictManual, "без метки"},
	}
	for _, tt := range tests {
		got, cause := Judge(tt.q, tt.r)
		if got != tt.want || !strings.Contains(cause, tt.cause) {
			t.Errorf("%s: Judge = %q (%q), ожидалось %q (…%q…)", tt.name, got, cause, tt.want, tt.cause)
		}
	}
}

func TestParseVerdict(t *testing.T) {
	for in, want := range map[string]Verdict{
		"ответил по базе": VerdictAnswered, "верно": VerdictAnswered, "Не знает": VerdictHonest,
		"к делу": VerdictRedirect, "ошибка": VerdictError,
	} {
		if got, ok := ParseVerdict(in); !ok || got != want {
			t.Errorf("ParseVerdict(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "хм", "проверить вручную"} {
		if _, ok := ParseVerdict(in); ok {
			t.Errorf("ParseVerdict(%q) не должен давать вердикт", in)
		}
	}
}

func sampleRows() []Row {
	return []Row{
		{N: 1, Question: "Сколько стоит портрет?", Expect: ParseExpectation("3 500 ₽"), Answer: "3 500 ₽ за час", Status: "ответ", Auto: VerdictAnswered, InputTokens: 4000, OutputTokens: 100, Seconds: 3.5, Model: "GigaChat-2"},
		{N: 2, Question: "Студенческая скидка?", Trap: true, Expect: ParseExpectation("НЕ ЗНАЕТ"), Answer: "Да; есть\n«10%»", Status: "ответ", Auto: VerdictManual, Reason: "проверьте", InputTokens: 3000, OutputTokens: 50, Seconds: 2.5, Model: "GigaChat-2"},
		{N: 3, Question: "Где вы?", Expect: ParseExpectation("Примерная"), Auto: VerdictManual, Reason: "нейросеть не ответила", Model: "GigaChat-2"},
	}
}

func TestCSVRoundTripKeepsManualVerdict(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, sampleRows()); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("\xef\xbb\xbf№;вопрос")) {
		t.Errorf("CSV без BOM или заголовка: %q", buf.String()[:30])
	}

	// Ручной вердикт вписан в колонку «итог», как это сделал бы я в Excel.
	edited := strings.Replace(buf.String(), ";проверьте;;", ";проверьте;ошибка;", 1)
	rows, err := ReadCSV([]byte(edited))
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	if len(rows) != 3 || rows[1].Answer != "Да; есть\n«10%»" || rows[1].Seconds != 2.5 || !rows[1].Trap {
		t.Errorf("прогон прочитан с искажениями: %+v", rows[1])
	}
	if rows[1].Verdict() != VerdictError || rows[2].Verdict() != VerdictManual {
		t.Errorf("вердикты: %q, %q", rows[1].Verdict(), rows[2].Verdict())
	}
}

func TestDiff(t *testing.T) {
	prev := sampleRows()
	cur := sampleRows()
	cur[1].Final = "не знает"
	cur[0].Answer = "Портрет — 3 500 ₽"
	cur = append(cur, Row{N: 4, Question: "Новый", Auto: VerdictAnswered})

	got := strings.Join(Diff(prev, cur), "\n")
	for _, want := range []string{"№2 «Студенческая скидка?»: проверить вручную → честно не знает", "№1 «Сколько стоит портрет?»: ответ изменился", "№4 новый вопрос"} {
		if !strings.Contains(got, want) {
			t.Errorf("в изменениях нет %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "№3") {
		t.Errorf("неизменный вопрос попал в изменения:\n%s", got)
	}
}

func TestCost(t *testing.T) {
	u := Measure(sampleRows())
	if u.Answers != 2 || u.AvgIn != 3500 || u.AvgOut != 75 || u.AvgSeconds != 3 {
		t.Fatalf("Measure = %+v: строка без ответа нейросети не должна входить в среднее", u)
	}
	lite := config.Prices{InputPer1K: 0.065, OutputPer1K: 0.065, MinMonthly: 600}
	if got := u.CostPerAnswer(lite); got < 0.232 || got > 0.233 {
		t.Errorf("стоимость ответа = %v, ожидалось ≈0,2324 ₽", got)
	}
	usage, pay := u.Monthly(1000, lite)
	if usage < 232 || usage > 233 || pay != 600 {
		t.Errorf("1 000 вопросов: расход %v, к оплате %v — минимальный платёж 600 ₽", usage, pay)
	}
	usage, pay = u.Monthly(3000, lite)
	if pay != usage {
		t.Errorf("3 000 вопросов: к оплате %v при расходе %v — минимум уже перекрыт", pay, usage)
	}

	s := Summary(sampleRows(), lite)
	for _, want := range []string{"Верно: 1 из 3", "проверить вручную: 2 (№ 2, 3)", "Минимальный платёж провайдера: 600 ₽", "1000 вопросов — расход 232 ₽, к оплате 600 ₽"} {
		if !strings.Contains(s, want) {
			t.Errorf("в сводке нет %q:\n%s", want, s)
		}
	}
	if !strings.Contains(Summary(sampleRows(), config.Prices{}), "Тарифы не заданы") {
		t.Error("без тарифов сводка должна сказать, какие настройки заполнить")
	}
}

func TestProtocolNeverShowsUncheckedAsCorrect(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteProtocol(&buf, "Фотостудия «Свет»", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), sampleRows()); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{"Верно 1 из 3", "Фотостудия «Свет» · 01.10.2026", "с подвохом", "Честно сказать, что в материалах этого нет", `class="verdict check">проверить вручную`} {
		if !strings.Contains(html, want) {
			t.Errorf("в протоколе нет %q", want)
		}
	}
	if strings.Count(html, `class="verdict ok"`) != 1 {
		t.Error("«верно» в протоколе только у проверенного вопроса")
	}
	if strings.Contains(html, "токен") || strings.Contains(html, "нейросеть не ответила") {
		t.Error("технические детали не для клиента")
	}
}

func TestFailedRowIsManual(t *testing.T) {
	r := Failed(1, Question{Text: "?"}, errors.New("timeout"), "m")
	if r.Verdict() != VerdictManual || !strings.Contains(r.Reason, "перезапустите") {
		t.Errorf("сбой связи — не вердикт о качестве: %+v", r)
	}
}

// Демо-набор фотостудии лежит в репозитории: он должен оставаться валидным и
// соответствовать обещанию кворка — 20 вопросов, из них 5 с подвохом.
func TestDemoExamSet(t *testing.T) {
	data, err := os.ReadFile("../../examples/photostudio/exam.csv")
	if err != nil {
		t.Fatal(err)
	}
	qs, err := ParseQuestions(data)
	if err != nil {
		t.Fatalf("демо-набор не читается: %v", err)
	}
	traps := 0
	for _, q := range qs {
		if q.Trap {
			traps++
		}
	}
	if len(qs) != 20 || traps != 5 {
		t.Errorf("вопросов %d, с подвохом %d; ожидалось 20 и 5", len(qs), traps)
	}
}
