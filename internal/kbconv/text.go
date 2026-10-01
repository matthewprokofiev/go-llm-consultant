package kbconv

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/matthewprokofiev/go-llm-consultant/internal/knowledge"
)

// decodeText: UTF-8 (с BOM или без), иначе Windows-1251 — в ней русский Excel
// и старые редакторы сохраняют CSV и TXT.
func decodeText(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if utf8.Valid(data) {
		return string(data), nil
	}
	out, err := charmap.Windows1251.NewDecoder().Bytes(data)
	if err != nil {
		return "", fmt.Errorf("кодировка не распознана (ожидается UTF-8 или Windows-1251): %w", err)
	}
	return string(out), nil
}

func convertCSV(data []byte, title string) ([]Section, []string, error) {
	rows, err := ReadCSV(data)
	if err != nil {
		return nil, nil, err
	}
	sections, problems := appendTable([]Section{{Level: 1, Title: title}}, rows, "таблица")
	return sections, problems, nil
}

// ReadCSV читает таблицу так, как её сохраняют Excel и Numbers: UTF-8 или
// Windows-1251, разделитель «;», «,» или табуляция. Нужна и экзамену.
func ReadCSV(data []byte) ([][]string, error) {
	text, err := decodeText(data)
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = csvDelimiter(text)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("таблица не разобрана: %w", err)
	}
	return rows, nil
}

// csvDelimiter угадывает разделитель по первой строке: русский Excel пишет «;».
func csvDelimiter(text string) rune {
	first, _, _ := strings.Cut(text, "\n")
	best, bestCount := ',', -1
	for _, d := range []rune{';', ',', '\t'} {
		if n := strings.Count(first, string(d)); n > bestCount {
			best, bestCount = d, n
		}
	}
	return best
}

// convertText: простой текст — одна секция с именем файла; абзацы сохраняются.
func convertText(data []byte, title string) ([]Section, error) {
	text, err := decodeText(data)
	if err != nil {
		return nil, err
	}
	return []Section{{Level: 1, Title: title, Lines: textLines(text)}}, nil
}

// convertMarkdown сохраняет разметку клиента: секции режутся тем же разбором, что
// у бота. Текст до первого заголовка получает заголовок по имени файла.
func convertMarkdown(data []byte, title string) ([]Section, error) {
	text, err := decodeText(data)
	if err != nil {
		return nil, err
	}
	var sections []Section
	for _, ks := range knowledge.ParseSections(text) {
		if ks.Title == "" {
			sections = append(sections, Section{Level: 1, Title: title, Lines: textLines(ks.Text)})
			continue
		}
		heading, body, _ := strings.Cut(ks.Text, "\n")
		heading = strings.TrimSpace(heading)
		level := len(heading) - len(strings.TrimLeft(heading, "#"))
		sections = append(sections, Section{Level: min(max(level, 1), 6), Title: ks.Title, Lines: textLines(body)})
	}
	return sections, nil
}

// textLines обрезает хвостовые пробелы и схлопывает подряд идущие пустые строки.
func textLines(text string) []string {
	var lines []string
	blank := true
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l = strings.TrimRight(l, " \t\r")
		if l == "" {
			if !blank {
				lines = append(lines, "")
			}
			blank = true
			continue
		}
		lines = append(lines, l)
		blank = false
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
