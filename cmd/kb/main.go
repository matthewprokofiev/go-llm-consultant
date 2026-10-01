// kb — утилита для настройки нового клиента: файлы клиента (PDF, DOCX, XLSX, CSV,
// TXT, MD) → черновик базы знаний и отчёт о том, что взято и что нет.
//
//	go run ./cmd/kb -out clients/kadr/knowledge/faq.md прайс.xlsx услуги.docx
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/matthewprokofiev/go-llm-consultant/internal/kbconv"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "", "куда записать черновик базы, например knowledge/faq.md")
	force := flag.Bool("force", false, "перезаписать существующий файл базы")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Использование: go run ./cmd/kb -out knowledge/faq.md [-force] файл...")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *out == "" || flag.NArg() == 0 {
		flag.Usage()
		return errors.New("нужны -out и хотя бы один файл клиента")
	}

	// Готовую и вычитанную базу случайно затереть нельзя.
	if _, err := os.Stat(*out); err == nil && !*force {
		return fmt.Errorf("%s уже существует: запишите черновик в другой файл или добавьте -force", *out)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	res := kbconv.Convert(flag.Args())
	fmt.Print(res.Report())

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(res.Markdown()), 0o644); err != nil {
		return err
	}
	fmt.Printf("\nЧерновик записан в %s. Вычитайте его перед загрузкой боту.\n", *out)
	return nil
}
