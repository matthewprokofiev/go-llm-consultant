package kbconv

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/matthewprokofiev/go-llm-consultant/internal/knowledge"
)

// zipOf собирает DOCX/XLSX в памяти: фикстуры видны прямо в тесте.
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const docxStyles = `<w:styles xmlns:w="w">
<w:style w:type="paragraph" w:styleId="1"><w:name w:val="heading 1"/></w:style>
<w:style w:type="paragraph" w:styleId="2"><w:name w:val="heading 2"/></w:style>
</w:styles>`

const docxBody = `<w:document xmlns:w="w"><w:body>
<w:p><w:r><w:t>Фотостудия «Свет» — съёмки и аренда зала.</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="1"/></w:pPr><w:r><w:t>Что взять с собой</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr/></w:pPr><w:r><w:t>Сменную </w:t></w:r><w:r><w:t>одежду</w:t></w:r></w:p>
<w:p><w:r><w:drawing/></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="2"/></w:pPr><w:r><w:t>Аренда</w:t></w:r></w:p>
<w:tbl>
<w:tr><w:tc><w:p><w:r><w:t>Зал</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Цена</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>Белый зал</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>1 500 ₽</w:t></w:r><w:r><w:tab/><w:t>/ час</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:tcPr><w:gridSpan w:val="2"/></w:tcPr><w:p><w:r><w:t>Выходные</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>Белый зал</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>2 000 ₽ / час</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
</w:body></w:document>`

func TestConvertDOCX(t *testing.T) {
	p := writeFile(t, "услуги.docx", zipOf(t, map[string]string{"word/document.xml": docxBody, "word/styles.xml": docxStyles}))
	res := Convert([]string{p})
	md := res.Markdown()

	for _, want := range []string{
		"# услуги\nФотостудия «Свет» — съёмки и аренда зала.",
		"# Что взять с собой\n- Сменную одежду",
		"## Аренда\n- Зал: Белый зал; Цена: 1 500 ₽ / час",
		"### Выходные\n- Зал: Белый зал; Цена: 2 000 ₽ / час",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("в черновике нет %q:\n%s", want, md)
		}
	}
	problems := strings.Join(res.Files[0].Problems, "\n")
	if !strings.Contains(problems, "картинки: 1") || !strings.Contains(problems, "объединённые ячейки") {
		t.Errorf("отчёт не упомянул картинку и объединённые ячейки: %s", problems)
	}
}

func xlsxFiles(sheet string) map[string]string {
	return map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Прайс" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml": `<sst><si><t>Услуга</t></si><si><t>Цена</t></si><si><t>Длительность</t></si>` +
			`<si><t>Съёмки</t></si><si><r><t>Портретная </t></r><r><t>съёмка</t></r></si><si><t>1 час</t></si></sst>`,
		"xl/styles.xml": `<styleSheet><numFmts><numFmt numFmtId="164" formatCode="#,##0 &quot;₽&quot;"/></numFmts>` +
			`<cellXfs><xf numFmtId="0"/><xf numFmtId="164"/><xf numFmtId="14"/></cellXfs></styleSheet>`,
		"xl/worksheets/sheet1.xml": sheet,
		"xl/media/image1.png":      "png",
	}
}

func TestConvertXLSX(t *testing.T) {
	sheet := `<worksheet><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c></row>
<row r="2"><c r="A2" t="s"><v>3</v></c></row>
<row r="3"><c r="A3" t="s"><v>4</v></c><c r="B3" s="1"><v>3500</v></c><c r="C3" t="s"><v>5</v></c></row>
<row r="4"><c r="A4" t="inlineStr"><is><t>Фотокнига</t></is></c><c r="B4" s="1"><f>B3*2</f></c><c r="C4" s="2"><v>46296</v></c></row>
</sheetData><mergeCells><mergeCell ref="A2:C2"/></mergeCells></worksheet>`
	p := writeFile(t, "прайс.xlsx", zipOf(t, xlsxFiles(sheet)))
	res := Convert([]string{p})
	md := res.Markdown()

	for _, want := range []string{
		"# Прайс",
		"## Съёмки\n- Услуга: Портретная съёмка; Цена: 3 500 ₽; Длительность: 1 час",
		"- Услуга: Фотокнига; Длительность: 01.10.2026",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("в черновике нет %q:\n%s", want, md)
		}
	}
	problems := strings.Join(res.Files[0].Problems, "\n")
	for _, want := range []string{"B4: формула без сохранённого значения", "объединённые ячейки A2:C2", "картинки: 1"} {
		if !strings.Contains(problems, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, problems)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		raw    string
		numFmt int
		code   string
		want   string
	}{
		{"2500", 164, `#,##0 "₽"`, "2 500 ₽"},
		{"1500.5", 165, `#,##0.00\ [$₽-419]`, "1 500,5 ₽"},
		{"12000", 166, `#,##0\ "р."`, "12 000 ₽"},
		{"12000", 3, "", "12 000"},
		{"2015", 0, "", "2015"},
		{"0.3", 9, "", "30%"},
		{"46296", 14, "", "01.10.2026"},
		{"7", 167, `[Red]0`, "7"},
	}
	for _, tt := range tests {
		if got := formatNumber(tt.raw, tt.numFmt, tt.code); got != tt.want {
			t.Errorf("formatNumber(%q, %d, %q) = %q, ожидалось %q", tt.raw, tt.numFmt, tt.code, got, tt.want)
		}
	}
}

func TestConvertCSVWindows1251(t *testing.T) {
	csv := "Услуга;Цена\r\nАренда зала;1 500 ₽ / час\r\nПлатье напрокат;500 ₽\r\n"
	// ₽ нет в Windows-1251 — в таких файлах пишут «руб.».
	csv = strings.ReplaceAll(csv, "₽", "руб.")
	encoded, err := charmap.Windows1251.NewEncoder().String(csv)
	if err != nil {
		t.Fatal(err)
	}
	p := writeFile(t, "аренда.csv", []byte(encoded))
	md := Convert([]string{p}).Markdown()
	if !strings.Contains(md, "# аренда\n- Услуга: Аренда зала; Цена: 1 500 руб. / час\n- Услуга: Платье напрокат; Цена: 500 руб.") {
		t.Errorf("кириллица из Windows-1251 искажена или таблица не разобрана:\n%s", md)
	}
}

func TestTableWithoutHeaderAndWideRow(t *testing.T) {
	sections, problems := appendTable([]Section{{Level: 1, Title: "Контакты"}}, [][]string{
		{"Адрес", "ул. Примерная, 1"},
		{"Часы", "10:00–22:00"},
	}, "таблица")
	if got := strings.Join(sections[0].Lines, "\n"); got != "- Адрес — ул. Примерная, 1\n- Часы — 10:00–22:00" {
		t.Errorf("таблица «ключ — значение»: %q", got)
	}
	if len(problems) != 0 {
		t.Errorf("лишние проблемы: %v", problems)
	}

	_, problems = appendTable([]Section{{Level: 1, Title: "Прайс"}}, [][]string{
		{"Услуга", "Цена"},
		{"Портрет", "3 500 ₽", "лишняя ячейка"},
	}, "таблица")
	if len(problems) != 1 || !strings.Contains(problems[0], "строка 2") {
		t.Errorf("строка шире заголовка должна попасть в отчёт: %v", problems)
	}
}

func TestConvertMarkdownAndText(t *testing.T) {
	md := writeFile(t, "faq.md", []byte("Вступление\n\n## Оплата\nКартой и наличными.\n"))
	txt := writeFile(t, "правила.txt", []byte("Бронь по предоплате.\n\n\n\nОтмена за 48 часов.\n"))
	got := Convert([]string{md, txt}).Markdown()
	want := "# faq\nВступление\n\n## Оплата\nКартой и наличными.\n\n# правила\nБронь по предоплате.\n\nОтмена за 48 часов.\n"
	if got != want {
		t.Errorf("черновик:\n%q\nожидался:\n%q", got, want)
	}
}

func TestConvertPDF(t *testing.T) {
	res := Convert([]string{"testdata/price.pdf"})
	md := res.Markdown()
	for _, want := range []string{
		"# Прайс фотостудии",
		"## Съёмки\nПортретная съёмка — 3 500 руб. / час\nСемейная съёмка — 6 000 руб. / час",
		"## Условия брони\nПредоплата 30%, перенос бесплатно за 48 часов до съёмки.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("в черновике из PDF нет %q:\n%s", want, md)
		}
	}
	problems := strings.Join(res.Files[0].Problems, "\n")
	if !strings.Contains(problems, "страницы 2 без текстового слоя") {
		t.Errorf("страница-скан не отмечена в отчёте: %s", problems)
	}
}

func TestUnsupportedFileDoesNotStopRun(t *testing.T) {
	bad := writeFile(t, "фото.jpg", []byte("jpg"))
	good := writeFile(t, "faq.md", []byte("# Оплата\nКартой.\n"))
	res := Convert([]string{bad, good})
	if !strings.Contains(strings.Join(res.Files[0].Problems, ""), "не поддерживается") {
		t.Errorf("неподдерживаемый формат должен попасть в отчёт: %v", res.Files[0].Problems)
	}
	if !strings.Contains(res.Markdown(), "# Оплата") {
		t.Error("остальные файлы должны перевестись")
	}
}

func TestWarnings(t *testing.T) {
	huge := strings.Repeat("Длинное описание услуги. ", knowledge.MaxPromptChars/20)
	res := Result{Files: []FileResult{{Name: "a.docx", Sections: []Section{
		{Level: 1, Title: "Цены", Lines: []string{"- Портрет: 3 500 ₽"}},
		{Level: 1, Title: "цены", Lines: []string{"- Семейная: 6 000 ₽"}},
		{Level: 1, Title: "Раздел"},
		{Level: 2, Title: "Подраздел", Lines: []string{"Текст"}},
		{Level: 1, Title: "Пустая"},
		{Level: 1, Title: "Описание", Lines: []string{huge}},
		{Level: 1, Title: "Копия", Lines: []string{"- Портрет: 3 500 ₽"}},
	}}}}
	got := strings.Join(res.Warnings(), "\n")
	for _, want := range []string{"больше лимита промпта", "дубль заголовка", "пустая секция «Пустая»", "дубль текста", "вопрос про «Описание» подтягивает"} {
		if !strings.Contains(got, want) {
			t.Errorf("нет предупреждения %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "«Раздел»") {
		t.Errorf("раздел с подразделами не пустой:\n%s", got)
	}

	report := res.Report()
	if !strings.Contains(report, "стр. (1 стр. = 1800 знаков)") {
		t.Errorf("в отчёте нет объёма в страницах:\n%s", report)
	}
}
