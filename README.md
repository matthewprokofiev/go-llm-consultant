# go-llm-consultant

<!-- CI-бейдж: замените OWNER/REPO на свой репозиторий после первого пуша -->
<!-- ![CI](https://github.com/OWNER/go-llm-consultant/actions/workflows/ci.yml/badge.svg) -->

🇬🇧 [English version](README.en.md)

Telegram-бот-консультант на Go: отвечает на вопросы клиентов на основе базы знаний
компании (FAQ/прайс в markdown), обращаясь к российским LLM — **GigaChat** (Sber)
или **YandexGPT**. Внешние OpenAI/Anthropic из РФ напрямую не оплатить, поэтому
провайдеры отечественные, каждый за общим интерфейсом и переключается одной ENV.

## Что делает

- Пользователь пишет вопрос в Telegram → бот подбирает релевантные секции базы
  знаний, формирует системный промпт и спрашивает LLM → возвращает ответ.
- База знаний грузится из `knowledge/faq.md` при старте; **RAG-lite** без векторов:
  файл режется на секции по markdown-заголовкам, релевантные вопросу отбираются по
  ключевым словам с учётом простого стемминга.
- Размер промпта ограничен (12 000 символов) — защита от `400 Bad Request` из-за
  переполнения контекста модели.
- Каждая пара вопрос/ответ пишется в Postgres (`dialogs`).
- `/reload` перечитывает файл базы знаний на лету — доступно только администратору.

## Демка

![Демо](docs/demo.gif)

## Стек

- **Go 1.25**
- **go-telegram/bot** — приём сообщений, индикатор «печатает…»
- **собственный клиент на net/http** (crypto/tls, crypto/x509) — обе LLM; SDK не нужны
- **jackc/pgx/v5** + **pgxpool** — журнал диалогов в Postgres
- **pressly/goose/v3** — миграции из `embed.FS`, применяются на старте
- **log/slog** — логи (текст+Debug в `local`, JSON+Info в проде)
- **Docker Compose** — бот + Postgres с healthcheck

## Как получить ключи

### GigaChat (основной провайдер — проще для физлица из РФ)

1. Зайдите на [developers.sber.ru](https://developers.sber.ru/) через Сбер ID.
2. Создайте проект **GigaChat API**, получите **Authorization Key** — это
   `base64(ClientID:ClientSecret)`.
3. Freemium: 1 000 000 токенов на 12 месяцев (scope `GIGACHAT_API_PERS`).
4. Впишите ключ в `GIGACHAT_AUTH_KEY`, при необходимости смените `GIGACHAT_SCOPE`.
5. Скачайте корневой сертификат НУЦ Минцифры: `make cert` (см. [TLS](#tls-и-сертификат-минцифры)).

Документация: <https://developers.sber.ru/docs/ru/gigachat>

### YandexGPT (вторая реализация за тем же интерфейсом)

1. В [Yandex Cloud](https://yandex.cloud/ru/docs/foundation-models) создайте каталог
   (нужен `folder_id`) и сервис-аккаунт с ролью `ai.languageModels.user`.
2. Выпустите **API-ключ** сервис-аккаунта, привяжите платёжный профиль.
3. Впишите `YANDEX_API_KEY` и `YANDEX_FOLDER_ID`, переключите `LLM_PROVIDER=yandexgpt`.

Документация: <https://yandex.cloud/ru/docs/foundation-models>

> **Своими руками, без SDK.** Клиент написан на стандартном `net/http`. Существуют
> сторонние SDK ([tigusigalpa/gigachat-go](https://github.com/tigusigalpa/gigachat-go),
> [sheeiavellie/go-yandexgpt](https://github.com/sheeiavellie/go-yandexgpt)) — их можно
> подключить как альтернативу, но по умолчанию используется собственный тонкий клиент.

## Как запустить

### Через Docker Compose (рекомендуется)

```bash
cp .env.example .env      # впишите BOT_TOKEN, ключи выбранного LLM-провайдера
make cert                 # скачает сертификат Минцифры в certs/ (нужен для GigaChat)
make up                   # поднимет Postgres (ждёт healthy) и бота, применит миграции
make down                 # остановить
```

- `BOT_TOKEN` — токен бота от [@BotFather](https://t.me/BotFather).
- `ADMIN_TG_ID` — ваш Telegram id (узнать у [@userinfobot](https://t.me/userinfobot))
  для доступа к `/reload`.
- `LLM_PROVIDER` — `gigachat` или `yandexgpt`; обязательны ключи только выбранного.

### Локально

```bash
export DATABASE_URL="postgres://consultant:consultant@localhost:5432/consultant?sslmode=disable"
export BOT_TOKEN="..." LLM_PROVIDER="gigachat" GIGACHAT_AUTH_KEY="..."
make cert
make run
```

> `.env` читает только Docker Compose. При `make run` переменные задаются вручную —
> в приложении нет dotenv-загрузчика.

### Тесты и линт

```bash
make test    # go test -race ./...  (реальные API не дёргаются — только httptest)
make lint    # golangci-lint запинённой версии
```

## Структура проекта

```
cmd/bot/                 точка входа, сквозной ctx и graceful shutdown
internal/config/         конфиг из ENV, провайдер-специфичная валидация, slog
internal/llm/            интерфейс LLMClient + gigachat.go + yandexgpt.go
internal/knowledge/      загрузка faq.md, чанкинг по заголовкам, keyword-отбор
internal/telegram/       Consultant (ядро) + Bot (транспорт), /reload
internal/storage/        pgx-пул, миграции goose, журнал диалогов
knowledge/faq.md         пример базы знаний
certs/                   корневой сертификат Минцифры (качается make cert)
migrations/              goose-миграции, встроены через embed.FS
docs/DECISIONS.md        журнал технических решений по шагам
```

## Технические решения

### Свой клиент на net/http вместо SDK

Каждой LLM нужно ровно два HTTP-вызова (у GigaChat — OAuth + chat, у Yandex — один
completion). Тащить внешний SDK ради этого невыгодно, а свой код проще аудировать
в чувствительных местах — TLS-пул и кэш токена. Обе реализации скрыты за интерфейсом
`LLMClient` с единственным методом `Ask(ctx, systemPrompt, userMessage) (Answer, error)`,
где `Answer` несёт текст и фактический расход токенов (`usage.total_tokens`) для журнала;
выбор провайдера — одна строка в фабрике `llm.New`.

### TLS и сертификат Минцифры

TLS-цепочка GigaChat подписана корневым сертификатом НУЦ Минцифры, которого нет в
системном хранилище. Клиент читает PEM с диска и добавляет его в копию системного
пула (`x509.SystemCertPool` + `AppendCertsFromPEM`), `MinVersion: TLS 1.2`.
Сертификат не хранится в git — качается `make cert` с официального адреса
`https://gu-st.ru/.../russian_trusted_root_ca.cer`. Крайний фолбэк для локального
демо — `GIGACHAT_INSECURE_SKIP_VERIFY=true` (с предупреждением в лог; **никогда в проде**).

### Кэш OAuth-токена

Токен GigaChat живёт 30 минут. Клиент кэширует его на этот срок минус окно
безопасности 60с и обновляет лениво под мьютексом (к боту пишут параллельно). При
ответе `401` токен принудительно переполучается и запрос повторяется **ровно один
раз** — без зацикливания.

### RAG-lite без векторов

Для FAQ/прайса одной компании эмбеддинги и векторная БД избыточны. Файл режется на
секции по заголовкам, релевантные отбираются по совпадению ключевых слов вопроса и
секции (с префиксным стеммингом, чтобы «оплаты» матчило «оплата»). Нет совпадений —
фолбэк на первые секции, чтобы модель получила общий контекст.

### Лимит размера промпта

Суммарный размер выбранных секций ограничен `MaxPromptChars = 12000` (грубая оценка
токенов для русского — символы/3). Секции набираются в порядке релевантности, пока
укладываются в лимит; остальные отбрасываются. Это защита от `400 Bad Request` при
переполнении контекста модели.

### Таймауты

Генерация у LLM штатно идёт 10–20 секунд. Каждый запрос оборачивается
`context.WithTimeout(45s)`; у `http.Client` собственный `Timeout` намеренно `0`, чтобы
короткий глобальный дедлайн не перебивал контекст и не рвал генерацию.

## Ограничения и заметки

- Реальные ключи GigaChat/YandexGPT в разработке не использовались — все проверки
  клиентов идут через `httptest.Server`. Форматы ответов сверяйте с официальной
  документацией (в коде помечены `TODO`).
- `docker compose up` для полного `app`-сервиса требует валидного `BOT_TOKEN`: бот
  делает `getMe` на старте. Половина с БД (миграции, журнал) проверяется независимо.

## Лицензия

[MIT](LICENSE)
