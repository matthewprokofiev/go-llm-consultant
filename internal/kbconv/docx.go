package kbconv

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DOCX — это zip с XML. Разбираем document.xml потоком: абзацы, заголовки по
// стилям, списки, таблицы. Картинки и вложенные объекты взять нельзя — считаем
// их для отчёта.
func convertDOCX(data []byte, title string) ([]Section, []string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("не похоже на DOCX: %w", err)
	}
	doc, err := readZipFile(zr, "word/document.xml")
	if err != nil {
		return nil, nil, err
	}
	styles := map[string]int{}
	if raw, err := readZipFile(zr, "word/styles.xml"); err == nil {
		styles = headingStyles(raw)
	}

	p := docxParser{title: title, styles: styles}
	if err := p.parse(doc); err != nil {
		return nil, nil, fmt.Errorf("разбор document.xml: %w", err)
	}

	problems := p.problems
	if p.images > 0 {
		problems = append(problems, fmt.Sprintf("картинки: %d — их содержимое не взято, текст с картинок перепечатайте вручную", p.images))
	}
	if p.objects > 0 {
		problems = append(problems, fmt.Sprintf("вложенные объекты: %d — не взяты", p.objects))
	}
	if p.merged {
		problems = append(problems, "в таблицах есть объединённые ячейки — проверьте, что позиции не съехали")
	}
	if p.nested {
		problems = append(problems, "есть таблица внутри таблицы — её текст склеен в одну ячейку")
	}
	return p.sections, problems, nil
}

func readZipFile(zr *zip.Reader, name string) ([]byte, error) {
	f, err := zr.Open(name)
	if err != nil {
		return nil, fmt.Errorf("в архиве нет %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, 64<<20))
}

// headingStyles — id стилей-заголовков и их уровень. Встроенные имена в styles.xml
// хранятся по-английски («heading 1») даже в русском Word.
func headingStyles(raw []byte) map[string]int {
	var parsed struct {
		Styles []struct {
			ID   string `xml:"styleId,attr"`
			Name struct {
				Val string `xml:"val,attr"`
			} `xml:"name"`
			Outline *struct {
				Val int `xml:"val,attr"`
			} `xml:"pPr>outlineLvl"`
		} `xml:"style"`
	}
	levels := map[string]int{}
	if xml.Unmarshal(raw, &parsed) != nil {
		return levels
	}
	for _, s := range parsed.Styles {
		name := strings.ToLower(s.Name.Val)
		switch {
		case name == "title":
			levels[s.ID] = 1
		case strings.HasPrefix(name, "heading "):
			if n, err := strconv.Atoi(strings.TrimPrefix(name, "heading ")); err == nil {
				levels[s.ID] = n
			}
		case s.Outline != nil && s.Outline.Val < 9:
			levels[s.ID] = s.Outline.Val + 1
		}
	}
	return levels
}

type docxParser struct {
	title    string
	styles   map[string]int
	sections []Section
	problems []string

	images, objects int
	merged, nested  bool

	inText bool
	para   strings.Builder
	level  int
	list   bool

	tblDepth int
	tables   int
	rows     [][]string
	row      []string
	cell     strings.Builder
}

func (p *docxParser) parse(doc []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			p.start(t)
		case xml.EndElement:
			p.end(t.Name.Local)
		case xml.CharData:
			if p.inText {
				p.para.Write(t)
			}
		}
	}
}

func (p *docxParser) start(t xml.StartElement) {
	switch t.Name.Local {
	case "p":
		p.para.Reset()
		p.level, p.list = 0, false
	case "pStyle":
		p.level = p.styles[attr(t, "val")]
	case "outlineLvl":
		if n, err := strconv.Atoi(attr(t, "val")); err == nil && n < 9 {
			p.level = n + 1
		}
	case "numPr":
		p.list = true
	case "t":
		p.inText = true
	case "tab", "br", "cr":
		p.para.WriteString(" ")
	case "tbl":
		p.tblDepth++
		if p.tblDepth == 1 {
			p.rows = nil
		} else {
			p.nested = true
		}
	case "tr":
		if p.tblDepth == 1 {
			p.row = nil
		}
	case "tc":
		if p.tblDepth == 1 {
			p.cell.Reset()
		}
	case "gridSpan", "vMerge":
		p.merged = true
	case "drawing", "pict":
		p.images++
	case "object":
		p.objects++
	}
}

func (p *docxParser) end(name string) {
	switch name {
	case "t":
		p.inText = false
	case "p":
		text := strings.Join(strings.Fields(p.para.String()), " ")
		switch {
		case text == "":
		case p.tblDepth > 0:
			if p.cell.Len() > 0 {
				p.cell.WriteString(" ")
			}
			p.cell.WriteString(text)
		case p.level > 0:
			p.sections = append(p.sections, Section{Level: min(p.level, 6), Title: text})
		default:
			if p.list {
				text = "- " + text
			}
			p.current().Lines = append(p.current().Lines, text)
		}
	case "tc":
		if p.tblDepth == 1 {
			p.row = append(p.row, p.cell.String())
		}
	case "tr":
		if p.tblDepth == 1 {
			p.rows = append(p.rows, p.row)
		}
	case "tbl":
		if p.tblDepth == 1 {
			p.tables++
			p.current()
			var probs []string
			p.sections, probs = appendTable(p.sections, p.rows, fmt.Sprintf("таблица %d", p.tables))
			p.problems = append(p.problems, probs...)
		}
		p.tblDepth--
	}
}

// current — секция, куда идёт текст. Текст до первого заголовка попадает в секцию
// с именем файла, чтобы у него был осмысленный заголовок для подбора.
func (p *docxParser) current() *Section {
	if len(p.sections) == 0 {
		p.sections = append(p.sections, Section{Level: 1, Title: p.title})
	}
	return &p.sections[len(p.sections)-1]
}

func attr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
