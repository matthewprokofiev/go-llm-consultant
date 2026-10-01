// exam — экзамен бота перед сдачей: вопросы клиента прогоняются через настоящую
// логику ответа с настоящей моделью и базой, без Telegram.
//
//	go run ./cmd/exam -env clients/kadr/.env -questions clients/kadr/exam.csv -out clients/kadr/exam
//	go run ./cmd/exam -env clients/kadr/.env -report clients/kadr/exam/run-2026-10-01-140500.csv
//
// Результат: run-*.csv (ответы и вердикты, колонка «итог» для ручных вердиктов),
// protocol-*.html для клиента и сводка расхода в терминале.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/matthewprokofiev/go-llm-consultant/internal/config"
	"github.com/matthewprokofiev/go-llm-consultant/internal/consultant"
	"github.com/matthewprokofiev/go-llm-consultant/internal/exam"
	"github.com/matthewprokofiev/go-llm-consultant/internal/knowledge"
	"github.com/matthewprokofiev/go-llm-consultant/internal/llm"
)

const stampLayout = "2006-01-02-150405"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func run() error {
	envFile := flag.String("env", "", "файл настроек клиента (.env); переменные окружения важнее файла")
	questions := flag.String("questions", "", "набор вопросов: CSV «вопрос;тип;ожидание»")
	out := flag.String("out", "exam", "папка для прогонов и протоколов")
	report := flag.String("report", "", "пересобрать протокол из прогона с ручными вердиктами (run-*.csv)")
	flag.Parse()

	if *envFile != "" {
		if err := loadEnvFile(*envFile); err != nil {
			return err
		}
	}
	core, err := config.LoadCore()
	if err != nil {
		return err
	}

	if *report != "" {
		return rebuildProtocol(*report, core)
	}
	if *questions == "" {
		flag.Usage()
		return errors.New("нужен -questions или -report")
	}
	return runExam(*questions, *out, core)
}

func runExam(questionsPath, outDir string, core config.Core) error {
	data, err := os.ReadFile(questionsPath)
	if err != nil {
		return err
	}
	qs, err := exam.ParseQuestions(data)
	if err != nil {
		return fmt.Errorf("%s: %w", questionsPath, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	kb, err := knowledge.New(core.KnowledgePath, log)
	if err != nil {
		return err
	}
	client, err := llm.New(core.LLM, log)
	if err != nil {
		return err
	}
	c := consultant.New(client, kb, core.BusinessName, core.LLM.Provider, log)
	model := core.LLM.Model()

	rows := make([]exam.Row, 0, len(qs))
	for i, q := range qs {
		fmt.Fprintf(os.Stderr, "\rвопрос %d из %d…", i+1, len(qs))
		// Каждый вопрос — с чистого листа, без истории: так прогоны сравнимы.
		reply, err := c.Answer(ctx, nil, q.Text)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			rows = append(rows, exam.Failed(i+1, q, err, model))
			continue
		}
		rows = append(rows, exam.Evaluate(i+1, q, reply, model))
	}
	fmt.Fprintln(os.Stderr)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	prev := latestRun(outDir)
	now := time.Now()
	stamp := now.Format(stampLayout)
	runPath := filepath.Join(outDir, "run-"+stamp+".csv")
	protocolPath := filepath.Join(outDir, "protocol-"+stamp+".html")

	if err := writeFile(runPath, func(f *os.File) error { return exam.WriteCSV(f, rows) }); err != nil {
		return err
	}
	if err := writeFile(protocolPath, func(f *os.File) error { return exam.WriteProtocol(f, core.BusinessName, now, rows) }); err != nil {
		return err
	}

	fmt.Print(exam.Summary(rows, core.LLM.Prices))
	if prev != "" {
		if prevData, err := os.ReadFile(prev); err == nil {
			if prevRows, err := exam.ReadCSV(prevData); err == nil {
				printDiff(filepath.Base(prev), exam.Diff(prevRows, rows))
			}
		}
	}
	fmt.Printf("\nПрогон: %s\nПротокол для клиента: %s\n", runPath, protocolPath)
	fmt.Println("Ручной вердикт впишите в колонку «итог» прогона и пересоберите протокол: -report", runPath)
	return nil
}

func rebuildProtocol(runPath string, core config.Core) error {
	data, err := os.ReadFile(runPath)
	if err != nil {
		return err
	}
	rows, err := exam.ReadCSV(data)
	if err != nil {
		return fmt.Errorf("%s: %w", runPath, err)
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(runPath), "run-"), ".csv")
	date, err := time.ParseInLocation(stampLayout, stamp, time.Local)
	if err != nil {
		date = time.Now()
	}
	protocolPath := filepath.Join(filepath.Dir(runPath), "protocol-"+stamp+".html")
	if err := writeFile(protocolPath, func(f *os.File) error { return exam.WriteProtocol(f, core.BusinessName, date, rows) }); err != nil {
		return err
	}
	fmt.Print(exam.Summary(rows, core.LLM.Prices))
	fmt.Printf("\nПротокол для клиента: %s\n", protocolPath)
	return nil
}

func printDiff(prev string, changes []string) {
	fmt.Printf("\nПо сравнению с %s:\n", prev)
	if len(changes) == 0 {
		fmt.Println("  изменений нет")
		return
	}
	for _, c := range changes {
		fmt.Println("  " + c)
	}
}

// latestRun — самый свежий прошлый прогон: имена со штампом времени сортируются по дате.
func latestRun(dir string) string {
	runs, _ := filepath.Glob(filepath.Join(dir, "run-*.csv"))
	if len(runs) == 0 {
		return ""
	}
	sort.Strings(runs)
	return runs[len(runs)-1]
}

func writeFile(path string, write func(*os.File) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// loadEnvFile читает .env так же, как docker compose: KEY=VALUE, комментарии с #,
// значение можно взять в кавычки. Через shell-овый source такой файл не прочитать:
// значения с пробелами и «ёлочками» там ломаются. Уже заданные переменные не
// перезаписываются — так удобно прогнать экзамен на другой модели:
// GIGACHAT_MODEL=GigaChat-2-Pro go run ./cmd/exam -env ...
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if _, set := os.LookupEnv(key); !set {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
