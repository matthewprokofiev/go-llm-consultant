# Пин патч-версии обязателен: goose/v3 требует go >= 1.25.7 (см. go.mod),
# а плавающий тег golang:1.25-alpine на Docker Hub может отставать на патч и
# ронять сборку с «requires go >= 1.25.7». Держим тег в паре с полом go.mod.
FROM golang:1.25.7-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 appuser

WORKDIR /app

COPY --from=builder /out/bot /usr/local/bin/bot
# База знаний и корневой сертификат Минцифры читаются на старте с диска, поэтому
# едут в образ. certs/ должен быть заполнен `make cert` до сборки образа.
COPY knowledge/ /app/knowledge/
COPY certs/ /app/certs/

USER appuser

ENTRYPOINT ["/usr/local/bin/bot"]
