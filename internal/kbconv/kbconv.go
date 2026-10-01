// Package kbconv переводит файлы клиента (PDF, DOCX, XLSX, CSV, TXT, MD) в черновик
// базы знаний в формате, который читает бот: markdown с секциями по заголовкам.
// Черновик вычитывается руками, поэтому утилита не гадает молча: всё, что взять
// не удалось, и всё подозрительное попадает в отчёт.
package kbconv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/matthewprokofiev/go-llm-consultant/internal/knowledge"
)

// PageChars — «страница» для обещания «база до 20 страниц»: стандартная страница
// 1 800 знаков с пробелами, считается по готовой базе, а не по вёрстке исходников.
const (
	PageChars = 1800
	MaxPages  = 20
)

// Section — секция черновика: заголовок нужного уровня и строки под ним.
type Section struct {
	Level int
	Title string
	Lines []string
}

func (s Section) markdown() string {
	var b strings.Builder
	b.WriteString(strings.Repeat("#", max(s.Level, 1)) + " " + s.Title)
	for _, l := range s.Lines {
		b.WriteString("\n" + l)
	}
	return b.String()
}

// FileResult — что получилось из одного файла.
type FileResult struct {
	Name     string
	Sections []Section
	Problems []string // что взять не удалось: сканы, картинки, вложенные объекты, сломанные таблицы
}

type Result struct {
	Files []FileResult
}

// Convert переводит файлы по расширению. Неподдерживаемый или битый файл не
// роняет весь прогон, а попадает в отчёт.
func Convert(paths []string) Result {
	var res Result
	for _, p := range paths {
		fr := FileResult{Name: filepath.Base(p)}
		sections, problems, err := convertFile(p)
		if err != nil {
			problems = append(problems, "файл не прочитан: "+err.Error())
		}
		fr.Sections, fr.Problems = sections, problems
		res.Files = append(res.Files, fr)
	}
	return res
}

func convertFile(path string) ([]Section, []string, error) {
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".docx":
		return convertDOCX(data, title)
	case ".xlsx":
		return convertXLSX(data)
	case ".csv":
		return convertCSV(data, title)
	case ".pdf":
		return convertPDF(data, title)
	case ".md", ".markdown":
		s, err := convertMarkdown(data, title)
		return s, nil, err
	case ".txt":
		s, err := convertText(data, title)
		return s, nil, err
	default:
		return nil, nil, fmt.Errorf("формат %s не поддерживается: PDF, DOCX, XLSX, CSV, TXT, MD", filepath.Ext(path))
	}
}

// Markdown — черновик базы: секции всех файлов подряд.
func (r Result) Markdown() string {
	var parts []string
	for _, f := range r.Files {
		for _, s := range f.Sections {
			parts = append(parts, s.markdown())
		}
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// Report — отчёт после перевода: что взято из каждого файла, что не удалось,
// предупреждения и объём против обещания «до 20 страниц».
func (r Result) Report() string {
	var b strings.Builder
	for _, f := range r.Files {
		chars := 0
		for _, s := range f.Sections {
			chars += runes(s.markdown())
		}
		fmt.Fprintf(&b, "%s\n  взято: секций %d, знаков %d\n", f.Name, len(f.Sections), chars)
		for _, p := range f.Problems {
			fmt.Fprintf(&b, "  не удалось: %s\n", p)
		}
	}

	if warnings := r.Warnings(); len(warnings) > 0 {
		b.WriteString("\nПредупреждения:\n")
		for _, w := range warnings {
			fmt.Fprintf(&b, "  - %s\n", w)
		}
	}

	total := runes(r.Markdown())
	pages := (total + PageChars - 1) / PageChars
	fmt.Fprintf(&b, "\nОбъём: %d знаков ≈ %d стр. (1 стр. = %d знаков), в базовом заказе до %d стр.\n", total, pages, PageChars, MaxPages)
	if pages > MaxPages {
		fmt.Fprintf(&b, "  - больше %d страниц: это уже опция «ещё 30 страниц базы»\n", MaxPages)
	}
	return b.String()
}

// Warnings — то, что стоит поправить руками до загрузки: секции больше лимита
// промпта, дубли, пустые секции и вопросы, которые тянут почти весь лимит.
func (r Result) Warnings() []string {
	var warnings []string
	seenTitle := map[string]string{}
	seenText := map[string]string{}

	for _, f := range r.Files {
		for i, s := range f.Sections {
			where := fmt.Sprintf("«%s» (%s)", s.Title, f.Name)
			size := runes(s.markdown())
			if size > knowledge.MaxPromptChars {
				warnings = append(warnings, fmt.Sprintf("секция %s — %d знаков, больше лимита промпта %d: модель увидит её обрезанной, разбейте на части", where, size, knowledge.MaxPromptChars))
			}
			body := strings.TrimSpace(strings.Join(s.Lines, "\n"))
			if body == "" {
				// Заголовок-раздел с подразделами — не пустой, у него текст ниже.
				if i+1 == len(f.Sections) || f.Sections[i+1].Level <= s.Level {
					warnings = append(warnings, fmt.Sprintf("пустая секция %s", where))
				}
				continue
			}
			titleKey := strings.ToLower(strings.TrimSpace(s.Title))
			if prev, ok := seenTitle[titleKey]; ok {
				warnings = append(warnings, fmt.Sprintf("дубль заголовка: %s и %s — модели будет трудно их различить", prev, where))
			}
			seenTitle[titleKey] = where
			if prev, ok := seenText[body]; ok {
				warnings = append(warnings, fmt.Sprintf("дубль текста: %s повторяет %s", where, prev))
			}
			seenText[body] = where
		}
	}

	// Типичный вопрос по теме секции не должен тянуть весь лимит: иначе нужная
	// строка прайса может не влезть рядом с соседями.
	kb := knowledge.FromText(r.Markdown())
	for _, f := range r.Files {
		for _, s := range f.Sections {
			if got := runes(kb.Select(s.Title)); got >= knowledge.MaxPromptChars*9/10 {
				warnings = append(warnings, fmt.Sprintf("вопрос про «%s» подтягивает %d знаков из %d — почти весь лимит: сделайте заголовки точнее или разбейте крупные секции", s.Title, got, knowledge.MaxPromptChars))
			}
		}
	}
	return warnings
}

func runes(s string) int { return len([]rune(s)) }
