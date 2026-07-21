// Package knowledge — RAG-lite: грузит markdown-базу знаний, режет её на секции
// по заголовкам и отбирает релевантные вопросу по ключевым словам, без векторов.
// Этого достаточно для FAQ/прайса одной компании, а нулевая инфраструктура (ни
// эмбеддингов, ни векторной БД) — сознательный размен ради простоты демо.
package knowledge

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// MaxPromptChars — верхняя граница суммарного размера выбранных секций. Защита от
// 400 Bad Request из-за переполнения контекста модели. Грубая оценка токенов для
// русского — символы/3, то есть ~4000 токенов на базу знаний.
const MaxPromptChars = 12000

const minKeywordLen = 3

// stemLen — длина префикса, по которому сравниваются слова. Дешёвая замена
// морфологии: «оплата», «оплаты», «оплатой» → «опла», «картой» и «карты» → «карт».
// Без этого точное совпадение подстроки промахивалось бы на любом падеже.
const stemLen = 4

// stopwords — частые слова без смысловой нагрузки: без их отсева «как» и «для»
// в вопросе матчили бы почти каждую секцию и обнуляли ранжирование.
var stopwords = map[string]bool{
	"как": true, "что": true, "это": true, "для": true, "или": true, "the": true,
	"вы": true, "мне": true, "мой": true, "моя": true, "при": true, "под": true,
	"меня": true, "тебя": true, "вас": true, "нас": true, "его": true, "их": true,
	"and": true, "you": true, "are": true, "can": true, "how": true, "what": true,
}

// Section — фрагмент базы знаний от одного заголовка до следующего.
type Section struct {
	Title string
	Text  string // полный текст секции, включая строку заголовка
}

func (s Section) runeLen() int { return len([]rune(s.Text)) }

// Base — потокобезопасное хранилище секций с возможностью перечитать файл (/reload).
type Base struct {
	path     string
	maxChars int
	log      *slog.Logger

	mu       sync.RWMutex
	sections []Section
}

func New(path string, log *slog.Logger) (*Base, error) {
	b := &Base{path: path, maxChars: MaxPromptChars, log: log}
	if err := b.Reload(); err != nil {
		return nil, err
	}
	return b, nil
}

// Reload перечитывает файл базы знаний. Вызывается на старте и по команде /reload.
func (b *Base) Reload() error {
	raw, err := os.ReadFile(b.path)
	if err != nil {
		return fmt.Errorf("чтение базы знаний %s: %w", b.path, err)
	}
	sections := parseSections(string(raw))

	b.mu.Lock()
	b.sections = sections
	b.mu.Unlock()

	b.log.Info("база знаний загружена", "path", b.path, "sections", len(sections))
	return nil
}

// Select возвращает текст релевантных вопросу секций, уложенный в лимит по размеру.
// Если ни одна секция не совпала по ключевым словам — фолбэк на первые секции файла,
// чтобы модель всё равно получила общий контекст о компании.
func (b *Base) Select(query string) string {
	b.mu.RLock()
	sections := b.sections
	b.mu.RUnlock()

	if len(sections) == 0 {
		return ""
	}

	kws := keywords(query)
	ranked := rankByRelevance(sections, kws)

	if len(ranked) == 0 {
		// Фолбэк: совпадений нет — берём секции в исходном порядке под лимит.
		return assemble(fitToLimit(indexRange(len(sections)), sections, b.maxChars), sections, b.maxChars)
	}
	return assemble(fitToLimit(ranked, sections, b.maxChars), sections, b.maxChars)
}

// parseSections режет markdown на секции по строкам-заголовкам (начинаются с #).
// Текст до первого заголовка (или файл без заголовков вовсе) становится одной
// безымянной секцией — его нельзя терять. Строки внутри fenced-блоков (```)
// заголовками не считаются: `# комментарий` в примере кода не должен резать секцию.
func parseSections(raw string) []Section {
	lines := strings.Split(raw, "\n")

	var sections []Section
	var cur *Section
	flush := func() {
		if cur != nil && strings.TrimSpace(cur.Text) != "" {
			cur.Text = strings.TrimRight(cur.Text, "\n")
			sections = append(sections, *cur)
		}
		cur = nil
	}

	inFence := false
	for _, line := range lines {
		if isFence(line) {
			inFence = !inFence
		}

		if !inFence && isHeading(line) {
			flush()
			cur = &Section{Title: headingTitle(line), Text: line + "\n"}
			continue
		}
		if cur == nil {
			cur = &Section{Title: "", Text: ""}
		}
		cur.Text += line + "\n"
	}
	flush()
	return sections
}

func isHeading(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "#")
}

// isFence распознаёт открывающую/закрывающую строку fenced-блока markdown (``` или ~~~).
func isFence(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

func headingTitle(line string) string {
	return strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
}

// keywords разбирает вопрос на значимые слова: нижний регистр, без пунктуации,
// короче minKeywordLen и стоп-слова отброшены.
func keywords(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	seen := map[string]bool{}
	var kws []string
	for _, f := range fields {
		if len([]rune(f)) < minKeywordLen || stopwords[f] || seen[f] {
			continue
		}
		seen[f] = true
		kws = append(kws, f)
	}
	return kws
}

// wordStemCounts разбивает текст секции на слова и считает их стемы: по этой карте
// потом за O(1) проверяется каждое ключевое слово вопроса.
func wordStemCounts(text string) map[string]int {
	m := map[string]int{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		m[stem(w)]++
	}
	return m
}

// stem обрезает слово до префикса stemLen: грубое отбрасывание русских окончаний.
func stem(w string) string {
	r := []rune(w)
	if len(r) > stemLen {
		return string(r[:stemLen])
	}
	return w
}

type scored struct {
	idx   int
	score int
}

// rankByRelevance возвращает индексы секций со счётом > 0, от самых релевантных
// к менее. Счёт — сколько слов секции совпали по стему с ключевыми словами вопроса.
func rankByRelevance(sections []Section, kws []string) []int {
	if len(kws) == 0 {
		return nil
	}

	stems := make([]string, len(kws))
	for i, kw := range kws {
		stems[i] = stem(kw)
	}

	var hits []scored
	for i, s := range sections {
		counts := wordStemCounts(s.Text)
		total := 0
		for _, st := range stems {
			total += counts[st]
		}
		if total > 0 {
			hits = append(hits, scored{idx: i, score: total})
		}
	}

	// Сортируем по убыванию счёта, при равенстве — по исходному порядку (стабильно).
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })

	idxs := make([]int, len(hits))
	for i, h := range hits {
		idxs[i] = h.idx
	}
	return idxs
}

// fitToLimit проходит секции в переданном порядке приоритета и набирает их, пока
// суммарный размер укладывается в лимит. Возвращает выбранные индексы. Если самая
// приоритетная секция одна уже больше лимита — она всё равно берётся (её обрежет
// assemble), иначе промпт остался бы пустым.
func fitToLimit(order []int, sections []Section, maxChars int) []int {
	var chosen []int
	total := 0
	for _, idx := range order {
		size := sections[idx].runeLen()
		if len(chosen) > 0 && total+size > maxChars {
			continue
		}
		chosen = append(chosen, idx)
		total += size
		if total >= maxChars {
			break
		}
	}
	return chosen
}

// assemble собирает выбранные секции в исходном порядке документа (так читабельнее)
// и подрезает результат под лимит по рунам на всякий случай.
func assemble(chosen []int, sections []Section, maxChars int) string {
	sort.Ints(chosen)

	parts := make([]string, 0, len(chosen))
	for _, idx := range chosen {
		parts = append(parts, sections[idx].Text)
	}
	out := strings.Join(parts, "\n\n")

	runes := []rune(out)
	if len(runes) > maxChars {
		return strings.TrimSpace(string(runes[:maxChars]))
	}
	return out
}

func indexRange(n int) []int {
	idxs := make([]int, n)
	for i := range idxs {
		idxs[i] = i
	}
	return idxs
}
