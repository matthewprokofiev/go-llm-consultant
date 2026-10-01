package exam

import (
	"html/template"
	"io"
	"time"
)

// Протокол для клиента — один HTML-файл со стилями внутри: открывается двойным
// кликом в любом браузере и на телефоне, в PDF сохраняется через «Печать».
var protocolTmpl = template.Must(template.New("protocol").Parse(`<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Проверка бота — {{.Business}}</title>
<style>
  :root { --text:#1d1d1f; --muted:#6e6e73; --line:#e5e5ea; --ok:#1a7f37; --ok-bg:#e8f5ec; --bad:#c62828; --bad-bg:#fdecec; --check:#9a6700; --check-bg:#fff4d6; }
  * { box-sizing: border-box; }
  body { margin: 0; padding: 24px 16px 48px; font: 16px/1.5 -apple-system, "Segoe UI", Roboto, Arial, sans-serif; color: var(--text); background: #fff; }
  main { max-width: 820px; margin: 0 auto; }
  h1 { font-size: 24px; margin: 0 0 4px; }
  .meta { color: var(--muted); margin: 0 0 20px; }
  .score { font-size: 20px; font-weight: 600; padding: 14px 16px; border-radius: 10px; background: #f5f5f7; margin-bottom: 24px; }
  .q { border: 1px solid var(--line); border-radius: 10px; padding: 14px 16px; margin-bottom: 14px; break-inside: avoid; }
  .head { display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; margin-bottom: 8px; }
  .num { color: var(--muted); }
  .question { font-weight: 600; flex: 1 1 60%; }
  .trap { font-size: 13px; color: var(--muted); border: 1px solid var(--line); border-radius: 6px; padding: 0 6px; }
  .label { font-size: 13px; color: var(--muted); margin-top: 8px; }
  .answer { white-space: pre-wrap; }
  .verdict { display: inline-block; margin-top: 10px; padding: 2px 10px; border-radius: 6px; font-weight: 600; font-size: 14px; }
  .ok { color: var(--ok); background: var(--ok-bg); }
  .bad { color: var(--bad); background: var(--bad-bg); }
  .check { color: var(--check); background: var(--check-bg); }
  footer { color: var(--muted); font-size: 14px; margin-top: 24px; }
  @media print { body { padding: 0; } .q { border-color: #ccc; } }
</style>
</head>
<body>
<main>
  <h1>Проверка бота перед сдачей</h1>
  <p class="meta">{{.Business}} · {{.Date}}</p>
  <div class="score">Верно {{.Correct}} из {{.Total}}{{if .Traps}} · из них с подвохом: {{.Traps}}{{end}}</div>
  {{range .Rows}}
  <section class="q">
    <div class="head">
      <span class="num">№{{.N}}</span>
      <span class="question">{{.Question}}</span>
      {{if .Trap}}<span class="trap">с подвохом</span>{{end}}
    </div>
    <div class="label">Ответ бота</div>
    <div class="answer">{{if .Answer}}{{.Answer}}{{else}}—{{end}}</div>
    <div class="label">Что ожидалось</div>
    <div>{{.Expected}}</div>
    <span class="verdict {{.Class}}">{{.Verdict}}</span>
  </section>
  {{end}}
  <footer>Бот отвечает только по материалам бизнеса. «Честно не знает» — правильное поведение: бот не выдумывает, а предлагает передать вопрос администратору.</footer>
</main>
</body>
</html>
`))

type protocolRow struct {
	N        int
	Question string
	Trap     bool
	Answer   string
	Expected string
	Verdict  Verdict
	Class    string
}

// WriteProtocol пишет протокол для клиента: вопрос, ответ бота, что ожидалось,
// вердикт и итог «верно X из N». Технических деталей (токены, статусы) в нём нет.
func WriteProtocol(w io.Writer, business string, date time.Time, rows []Row) error {
	data := struct {
		Business       string
		Date           string
		Correct, Total int
		Traps          int
		Rows           []protocolRow
	}{Business: business, Date: date.Format("02.01.2006"), Total: len(rows)}

	for _, r := range rows {
		v := r.Verdict()
		class := "check"
		switch {
		case v.Correct():
			class = "ok"
			data.Correct++
		case v == VerdictError:
			class = "bad"
		}
		if r.Trap {
			data.Traps++
		}
		data.Rows = append(data.Rows, protocolRow{
			N: r.N, Question: r.Question, Trap: r.Trap, Answer: r.Answer,
			Expected: r.Expect.Describe(), Verdict: v, Class: class,
		})
	}
	return protocolTmpl.Execute(w, data)
}
