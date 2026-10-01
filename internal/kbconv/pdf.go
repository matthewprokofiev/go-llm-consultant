package kbconv

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// PDF читается движком pdfium (тот же, что в Chrome), собранным в WebAssembly:
// ничего не нужно ставить в систему. Берётся только текстовый слой — сканы без
// текста честно помечаются в отчёте, распознавание в утилиту не входит.

var (
	pdfOnce sync.Once
	pdfPool pdfium.Pool
	pdfErr  error
)

func pdfInstance() (pdfium.Pdfium, error) {
	pdfOnce.Do(func() {
		pdfPool, pdfErr = webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	})
	if pdfErr != nil {
		return nil, fmt.Errorf("запуск pdfium: %w", pdfErr)
	}
	return pdfPool.GetInstance(30 * time.Second)
}

type pdfLine struct {
	text string
	size float64
}

func convertPDF(data []byte, title string) ([]Section, []string, error) {
	inst, err := pdfInstance()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = inst.Close() }()

	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, nil, fmt.Errorf("PDF не открыт (повреждён или под паролем): %w", err)
	}
	defer func() { _, _ = inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document}) }()

	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return nil, nil, err
	}

	var lines []pdfLine
	var scans, withImages []string
	for i := 0; i < count.PageCount; i++ {
		pageLines, images, err := readPDFPage(inst, doc, i)
		if err != nil {
			return nil, nil, fmt.Errorf("страница %d: %w", i+1, err)
		}
		switch {
		case len(pageLines) == 0:
			scans = append(scans, strconv.Itoa(i+1))
		case images > 0:
			withImages = append(withImages, strconv.Itoa(i+1))
		}
		lines = append(lines, pageLines...)
	}

	var problems []string
	if len(scans) > 0 {
		problems = append(problems, fmt.Sprintf("страницы %s без текстового слоя (скан или картинка) — не взяты, распознавание не входит", strings.Join(scans, ", ")))
	}
	if len(withImages) > 0 {
		problems = append(problems, fmt.Sprintf("на страницах %s есть картинки — их содержимое не взято", strings.Join(withImages, ", ")))
	}
	return pdfSections(lines, title), problems, nil
}

func readPDFPage(inst pdfium.Pdfium, doc *responses.OpenDocument, index int) ([]pdfLine, int, error) {
	page, err := inst.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: doc.Document, Index: index})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _, _ = inst.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: page.Page}) }()
	ref := requests.Page{ByReference: &page.Page}

	text, err := inst.GetPageTextStructured(&requests.GetPageTextStructured{
		Page:                   ref,
		Mode:                   requests.GetPageTextStructuredModeRects,
		CollectFontInformation: true,
	})
	if err != nil {
		return nil, 0, err
	}

	images := 0
	if objs, err := inst.FPDFPage_CountObjects(&requests.FPDFPage_CountObjects{Page: ref}); err == nil {
		for j := 0; j < objs.Count; j++ {
			obj, err := inst.FPDFPage_GetObject(&requests.FPDFPage_GetObject{Page: ref, Index: j})
			if err != nil {
				continue
			}
			if t, err := inst.FPDFPageObj_GetType(&requests.FPDFPageObj_GetType{PageObject: obj.PageObject}); err == nil && t.Type == enums.FPDF_PAGEOBJ_IMAGE {
				images++
			}
		}
	}
	return groupLines(text.Rects), images, nil
}

// groupLines собирает строки из текстовых фрагментов: фрагменты на одной высоте —
// одна строка, слева направо. Большой разрыв между фрагментами — это столбцы
// таблицы или прайса, между ними ставится тире, чтобы название и цена не склеились.
func groupLines(rects []*responses.GetPageTextStructuredRect) []pdfLine {
	type frag struct {
		text             string
		left, right, mid float64
		height, size     float64
	}
	var frags []frag
	for _, r := range rects {
		text := strings.Join(strings.Fields(r.Text), " ")
		if text == "" {
			continue
		}
		p := r.PointPosition
		size := 0.0
		if r.FontInformation != nil {
			size = max(r.FontInformation.RenderedSize, r.FontInformation.Size)
		}
		// В PDF ось Y смотрит вверх: у верхних строк Top больше.
		frags = append(frags, frag{text: text, left: p.Left, right: p.Right, mid: (p.Top + p.Bottom) / 2, height: math.Abs(p.Top - p.Bottom), size: size})
	}
	sort.SliceStable(frags, func(i, j int) bool {
		if math.Abs(frags[i].mid-frags[j].mid) > max(frags[i].height, frags[j].height)/2 {
			return frags[i].mid > frags[j].mid
		}
		return frags[i].left < frags[j].left
	})

	var lines []pdfLine
	var cur []frag
	flush := func() {
		if len(cur) == 0 {
			return
		}
		var b strings.Builder
		size := 0.0
		for i, f := range cur {
			if i > 0 {
				gap := f.left - cur[i-1].right
				if gap > 2*max(f.size, f.height) {
					b.WriteString(" — ")
				} else {
					b.WriteString(" ")
				}
			}
			b.WriteString(f.text)
			size = max(size, f.size)
		}
		lines = append(lines, pdfLine{text: b.String(), size: size})
		cur = nil
	}
	for _, f := range frags {
		if len(cur) > 0 && math.Abs(f.mid-cur[0].mid) > max(f.height, cur[0].height)/2 {
			flush()
		}
		cur = append(cur, f)
	}
	flush()
	return lines
}

// pdfSections: строка шрифтом заметно крупнее основного текста — заголовок.
// Основной размер — самый частый по числу знаков.
func pdfSections(lines []pdfLine, title string) []Section {
	chars := map[float64]int{}
	for _, l := range lines {
		chars[math.Round(l.size)] += runes(l.text)
	}
	body, best := 0.0, -1
	for size, n := range chars {
		if n > best || n == best && size < body {
			body, best = size, n
		}
	}

	var sections []Section
	prevSize := 0.0 // размер предыдущей строки-заголовка, 0 — предыдущая строка не заголовок
	for _, l := range lines {
		isHeading := body > 0 && l.size >= body*1.2 && runes(l.text) <= 120
		switch {
		case isHeading && math.Abs(l.size-prevSize) < 0.5:
			// Длинный заголовок, перенесённый на вторую строку тем же шрифтом.
			last := &sections[len(sections)-1]
			last.Title += " " + l.text
		case isHeading:
			level := 2
			if l.size >= body*1.6 {
				level = 1
			}
			sections = append(sections, Section{Level: level, Title: l.text})
		default:
			if len(sections) == 0 {
				sections = append(sections, Section{Level: 1, Title: title})
			}
			last := &sections[len(sections)-1]
			last.Lines = append(last.Lines, l.text)
		}
		prevSize = 0
		if isHeading {
			prevSize = l.size
		}
	}
	return sections
}
