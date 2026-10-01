package kbconv

import (
	"fmt"
	"strings"
	"unicode"
)

// appendTable дописывает таблицу в последнюю секцию так, чтобы модель читала каждую
// позицию прайса одной строкой: «- Услуга: Портрет; Цена: 3 500 ₽; Время: 1 час».
//   - строка с одной заполненной ячейкой — категория, становится подсекцией;
//   - первая строка без цифр из нескольких ячеек — заголовок столбцов;
//   - остальные строки — позиции.
//
// Если заголовка нет (таблица «ключ — значение»), ячейки соединяются тире.
func appendTable(sections []Section, rows [][]string, where string) ([]Section, []string) {
	baseLevel := sections[len(sections)-1].Level
	var header []string
	var problems []string
	for i, row := range rows {
		cells := make([]string, len(row))
		filled := 0
		for j, c := range row {
			cells[j] = strings.Join(strings.Fields(c), " ")
			if cells[j] != "" {
				filled++
			}
		}
		switch {
		case filled == 0:
		case filled == 1:
			sections = append(sections, Section{Level: min(baseLevel+1, 6), Title: firstFilled(cells)})
		case header == nil && !hasDigits(cells):
			header = cells
		default:
			if header != nil && lastFilled(cells) >= len(header) {
				problems = append(problems, fmt.Sprintf("%s, строка %d: ячеек больше, чем столбцов в заголовке — проверьте, не склеились ли столбцы", where, i+1))
			}
			last := &sections[len(sections)-1]
			last.Lines = append(last.Lines, itemLine(header, cells))
		}
	}
	return sections, problems
}

func itemLine(header, cells []string) string {
	var parts []string
	for j, c := range cells {
		if c == "" {
			continue
		}
		if j < len(header) && header[j] != "" {
			parts = append(parts, header[j]+": "+c)
		} else {
			parts = append(parts, c)
		}
	}
	sep := "; "
	if header == nil {
		sep = " — "
	}
	return "- " + strings.Join(parts, sep)
}

func firstFilled(cells []string) string {
	for _, c := range cells {
		if c != "" {
			return c
		}
	}
	return ""
}

func lastFilled(cells []string) int {
	for j := len(cells) - 1; j >= 0; j-- {
		if cells[j] != "" {
			return j
		}
	}
	return -1
}

func hasDigits(cells []string) bool {
	for _, c := range cells {
		if strings.IndexFunc(c, unicode.IsDigit) >= 0 {
			return true
		}
	}
	return false
}
