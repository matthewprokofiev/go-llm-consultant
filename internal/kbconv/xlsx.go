package kbconv

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type xlsxWorkbook struct {
	Sheets []struct {
		Name string `xml:"name,attr"`
		RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	} `xml:"sheets>sheet"`
}

type xlsxRels struct {
	Rels []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

// xlsxRich — строка ячейки: простой текст или несколько кусков с разным форматированием.
type xlsxRich struct {
	T string `xml:"t"`
	R []struct {
		T string `xml:"t"`
	} `xml:"r"`
}

func (r xlsxRich) text() string {
	var b strings.Builder
	b.WriteString(r.T)
	for _, part := range r.R {
		b.WriteString(part.T)
	}
	return b.String()
}

type xlsxStyles struct {
	NumFmts []struct {
		ID   int    `xml:"numFmtId,attr"`
		Code string `xml:"formatCode,attr"`
	} `xml:"numFmts>numFmt"`
	Xfs []struct {
		NumFmtID int `xml:"numFmtId,attr"`
	} `xml:"cellXfs>xf"`
}

type xlsxSheet struct {
	Rows []struct {
		Cells []struct {
			Ref   string   `xml:"r,attr"`
			Type  string   `xml:"t,attr"`
			Style int      `xml:"s,attr"`
			F     *string  `xml:"f"`
			V     *string  `xml:"v"`
			IS    xlsxRich `xml:"is"`
		} `xml:"c"`
	} `xml:"sheetData>row"`
	Merges []struct {
		Ref string `xml:"ref,attr"`
	} `xml:"mergeCells>mergeCell"`
}

// convertXLSX: каждый лист — секция, строки листа — позиции прайса. Числа выводятся
// так, как их видел клиент в Excel: с ₽ и разрядами, если так задан формат ячейки.
func convertXLSX(data []byte) ([]Section, []string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("не похоже на XLSX: %w", err)
	}

	var wb xlsxWorkbook
	var rels xlsxRels
	if err := unmarshalZip(zr, "xl/workbook.xml", &wb); err != nil {
		return nil, nil, err
	}
	if err := unmarshalZip(zr, "xl/_rels/workbook.xml.rels", &rels); err != nil {
		return nil, nil, err
	}
	var sst struct {
		Items []xlsxRich `xml:"si"`
	}
	var styles xlsxStyles
	_ = unmarshalZip(zr, "xl/sharedStrings.xml", &sst) // в книге без текста файла нет
	_ = unmarshalZip(zr, "xl/styles.xml", &styles)

	formats := map[int]string{}
	for _, f := range styles.NumFmts {
		formats[f.ID] = f.Code
	}
	targets := map[string]string{}
	for _, r := range rels.Rels {
		targets[r.ID] = r.Target
	}

	var sections []Section
	var problems []string
	for _, sh := range wb.Sheets {
		target := targets[sh.RID]
		if strings.HasPrefix(target, "/") {
			target = strings.TrimPrefix(target, "/")
		} else {
			target = path.Join("xl", target)
		}
		var sheet xlsxSheet
		if err := unmarshalZip(zr, target, &sheet); err != nil {
			problems = append(problems, fmt.Sprintf("лист «%s» не прочитан: %v", sh.Name, err))
			continue
		}

		var rows [][]string
		for _, r := range sheet.Rows {
			var row []string
			for _, c := range r.Cells {
				col := columnIndex(c.Ref)
				for len(row) <= col {
					row = append(row, "")
				}
				switch {
				case c.Type == "s" && c.V != nil:
					if i, err := strconv.Atoi(*c.V); err == nil && i < len(sst.Items) {
						row[col] = sst.Items[i].text()
					}
				case c.Type == "inlineStr":
					row[col] = c.IS.text()
				case c.V == nil:
					if c.F != nil {
						problems = append(problems, fmt.Sprintf("лист «%s», %s: формула без сохранённого значения — откройте и пересохраните файл в Excel", sh.Name, c.Ref))
					}
				case c.Type == "b":
					row[col] = map[string]string{"1": "да", "0": "нет"}[*c.V]
				case c.Type == "str" || c.Type == "e":
					row[col] = *c.V
				default:
					numFmt := 0
					if c.Style < len(styles.Xfs) {
						numFmt = styles.Xfs[c.Style].NumFmtID
					}
					row[col] = formatNumber(*c.V, numFmt, formats[numFmt])
				}
			}
			rows = append(rows, row)
		}

		before := len(sections)
		sections = append(sections, Section{Level: 1, Title: sh.Name})
		var probs []string
		sections, probs = appendTable(sections, rows, fmt.Sprintf("лист «%s»", sh.Name))
		problems = append(problems, probs...)
		if len(sections) == before+1 && len(sections[before].Lines) == 0 {
			sections = sections[:before]
			problems = append(problems, fmt.Sprintf("лист «%s» пустой — пропущен", sh.Name))
		}

		if len(sheet.Merges) > 0 {
			refs := make([]string, 0, len(sheet.Merges))
			for _, m := range sheet.Merges {
				refs = append(refs, m.Ref)
			}
			problems = append(problems, fmt.Sprintf("лист «%s»: объединённые ячейки %s — проверьте, что позиции не съехали", sh.Name, strings.Join(refs, ", ")))
		}
	}

	images, charts := 0, 0
	for _, f := range zr.File {
		switch {
		case strings.HasPrefix(f.Name, "xl/media/"):
			images++
		case strings.HasPrefix(f.Name, "xl/charts/chart"):
			charts++
		}
	}
	if images > 0 {
		problems = append(problems, fmt.Sprintf("картинки: %d — их содержимое не взято", images))
	}
	if charts > 0 {
		problems = append(problems, fmt.Sprintf("диаграммы: %d — не взяты", charts))
	}
	return sections, problems, nil
}

func unmarshalZip(zr *zip.Reader, name string, v any) error {
	raw, err := readZipFile(zr, name)
	if err != nil {
		return err
	}
	if err := xml.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("разбор %s: %w", name, err)
	}
	return nil
}

// columnIndex: «C12» → 2.
func columnIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return max(n-1, 0)
}

// formatNumber показывает число так, как его видел клиент: валюта с ₽, разряды,
// проценты и даты по формату ячейки. Десятичный разделитель — запятая.
func formatNumber(raw string, numFmt int, code string) string {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	lower := strings.ToLower(code)
	switch {
	case isDateFormat(numFmt, lower):
		return excelDate(v)
	case strings.Contains(lower, "%") || numFmt == 9 || numFmt == 10:
		return decimal(v*100, false) + "%"
	case strings.Contains(lower, "₽") || strings.Contains(lower, "руб") || strings.Contains(lower, "р.") || strings.Contains(lower, "[$rub"):
		return decimal(v, true) + " ₽"
	case strings.Contains(code, "#,##0") || strings.Contains(code, "# ##0") || numFmt == 3 || numFmt == 4:
		return decimal(v, true)
	default:
		return decimal(v, false)
	}
}

// formatLiterals — цвета, локали и текст в кавычках внутри кода формата: буква d
// в «[Red]» не делает формат датой.
var formatLiterals = regexp.MustCompile(`\[[^\]]*\]|"[^"]*"`)

func isDateFormat(numFmt int, lower string) bool {
	if (numFmt >= 14 && numFmt <= 22) || (numFmt >= 45 && numFmt <= 47) {
		return true
	}
	code := formatLiterals.ReplaceAllString(lower, "")
	return !strings.ContainsAny(code, "0#") && strings.ContainsAny(code, "dmy")
}

func excelDate(serial float64) string {
	t := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).Add(time.Duration(serial * 24 * float64(time.Hour)))
	if serial == math.Trunc(serial) {
		return t.Format("02.01.2006")
	}
	return t.Format("02.01.2006 15:04")
}

// decimal печатает число без лишних нулей; group — с пробелом между разрядами.
func decimal(v float64, group bool) string {
	s := strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
	intPart, frac, hasFrac := strings.Cut(s, ".")
	sign := ""
	if strings.HasPrefix(intPart, "-") {
		sign, intPart = "-", intPart[1:]
	}
	if group {
		var b strings.Builder
		for i, r := range intPart {
			if i > 0 && (len(intPart)-i)%3 == 0 {
				b.WriteString(" ")
			}
			b.WriteRune(r)
		}
		intPart = b.String()
	}
	if hasFrac {
		return sign + intPart + "," + frac
	}
	return sign + intPart
}
