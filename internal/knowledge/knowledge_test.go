package knowledge

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func testBase(sections []Section) *Base {
	return &Base{
		maxChars: MaxPromptChars,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		sections: sections,
	}
}

func TestParseSections(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantCount  int
		wantTitles []string
	}{
		{
			name:       "без заголовков — одна безымянная секция",
			raw:        "Просто текст про компанию.\nВторая строка.",
			wantCount:  1,
			wantTitles: []string{""},
		},
		{
			name:       "один заголовок",
			raw:        "# Доставка\nВозим по всей стране.",
			wantCount:  1,
			wantTitles: []string{"Доставка"},
		},
		{
			name:       "преамбула до первого заголовка сохраняется",
			raw:        "Вступление.\n# Доставка\nтекст",
			wantCount:  2,
			wantTitles: []string{"", "Доставка"},
		},
		{
			name:       "вложенные заголовки — каждый своя секция",
			raw:        "# Услуги\nобщий текст\n## Доставка\nо доставке\n## Оплата\nоб оплате",
			wantCount:  3,
			wantTitles: []string{"Услуги", "Доставка", "Оплата"},
		},
		{
			name:       "ведущие пустые строки не создают безымянную секцию",
			raw:        "\n\n# Доставка\nтекст",
			wantCount:  1,
			wantTitles: []string{"Доставка"},
		},
		{
			name:       "# внутри code-fence не режет секцию",
			raw:        "# Установка\nПример:\n```bash\n# это комментарий, не заголовок\napt install foo\n```\nГотово.",
			wantCount:  1,
			wantTitles: []string{"Установка"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSections(tt.raw)
			if len(got) != tt.wantCount {
				t.Fatalf("секций = %d, ожидалось %d: %+v", len(got), tt.wantCount, got)
			}
			for i, want := range tt.wantTitles {
				if got[i].Title != want {
					t.Errorf("секция %d: title = %q, ожидался %q", i, got[i].Title, want)
				}
			}
		})
	}
}

func TestKeywords(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"Как оформить доставку?", []string{"оформить", "доставку"}},
		{"что это для меня", nil},                   // всё — стоп-слова или короткие
		{"ДОСТАВКА доставка", []string{"доставка"}}, // регистр + дедуп
		{"", nil},
	}
	for _, tt := range tests {
		got := keywords(tt.query)
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("keywords(%q) = %v, ожидалось %v", tt.query, got, tt.want)
		}
	}
}

func TestSelectRelevance(t *testing.T) {
	sections := []Section{
		{Title: "Доставка", Text: "# Доставка\nДоставляем курьером и почтой по всей России."},
		{Title: "Оплата", Text: "# Оплата\nПринимаем карты и наличные."},
		{Title: "Гарантия", Text: "# Гарантия\nГарантия на технику 12 месяцев."},
	}
	b := testBase(sections)

	// Совпадение есть: должен вернуть секцию про оплату и не тащить остальные.
	got := b.Select("Какие способы оплаты картой?")
	if !strings.Contains(got, "Принимаем карты") {
		t.Errorf("релевантная секция про оплату не выбрана: %q", got)
	}
	if strings.Contains(got, "Гарантия на технику") {
		t.Errorf("нерелевантная секция про гарантию просочилась: %q", got)
	}
}

func TestSelectFallback(t *testing.T) {
	sections := []Section{
		{Title: "Доставка", Text: "# Доставка\nВозим по России."},
		{Title: "Оплата", Text: "# Оплата\nКарты и наличные."},
	}
	b := testBase(sections)

	// Ни одного совпадения по ключевым словам → фолбэк: непустой результат из секций.
	got := b.Select("расскажите про уникорнов и радугу")
	if strings.TrimSpace(got) == "" {
		t.Fatal("фолбэк вернул пусто — модель осталась бы без контекста")
	}
	if !strings.Contains(got, "Возим по России") {
		t.Errorf("фолбэк не вернул первые секции: %q", got)
	}
}

func TestSelectRespectsLimit(t *testing.T) {
	big := strings.Repeat("оплата ", 4000) // ~28000 символов, заведомо больше лимита
	sections := []Section{
		{Title: "A", Text: "# A\n" + big},
		{Title: "B", Text: "# B\n" + big},
		{Title: "C", Text: "# C\n" + big},
	}
	b := testBase(sections)

	got := b.Select("оплата")
	if n := len([]rune(got)); n > MaxPromptChars {
		t.Errorf("результат %d рун превышает лимит %d", n, MaxPromptChars)
	}
	if strings.TrimSpace(got) == "" {
		t.Error("при секциях больше лимита результат не должен быть пустым")
	}
}

func TestSelectEmptyBase(t *testing.T) {
	b := testBase(nil)
	if got := b.Select("что угодно"); got != "" {
		t.Errorf("пустая база должна возвращать пусто, вернула %q", got)
	}
}
