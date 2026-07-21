.PHONY: build run test lint vet tidy migrate-up migrate-down up down docker-build cert

GOOSE_DRIVER ?= postgres
DATABASE_URL ?= postgres://consultant:consultant@localhost:5432/consultant?sslmode=disable

# При явном -o Go не дописывает .exe сам, а Windows не запустит файл без расширения.
BINARY ?= bin/bot
ifeq ($(OS),Windows_NT)
	BINARY := bin/bot.exe
endif

# Версия запинена и запускается через go run: не требует глобальной установки
# и гарантирует, что локально и в CI линтит одна и та же версия.
GOLANGCI_LINT_VERSION ?= v2.12.2
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# Корневой сертификат НУЦ Минцифры: GigaChat отдаёт TLS-цепочку, подписанную им,
# и без этого сертификата в CertPool рукопожатие к API падает.
CERT_URL ?= https://gu-st.ru/content/Other/doc/russian_trusted_root_ca.cer
CERT_PATH ?= certs/russian_trusted_root_ca.cer

build:
	go build -o $(BINARY) ./cmd/bot

run:
	go run ./cmd/bot

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	$(GOLANGCI_LINT) run ./...

tidy:
	go mod tidy

# Скачивает корневой сертификат Минцифры в certs/. Запускать один раз перед сборкой
# образа: Dockerfile ждёт файл на месте (в git он не хранится).
cert:
	curl -fsSL $(CERT_URL) -o $(CERT_PATH)

# Миграции применяются и автоматически на старте приложения; эти цели — для ручной работы.
migrate-up:
	go run github.com/pressly/goose/v3/cmd/goose@latest -dir migrations $(GOOSE_DRIVER) "$(DATABASE_URL)" up

migrate-down:
	go run github.com/pressly/goose/v3/cmd/goose@latest -dir migrations $(GOOSE_DRIVER) "$(DATABASE_URL)" down

up:
	docker compose up -d --build

down:
	docker compose down

docker-build:
	docker compose build
