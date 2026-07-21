# go-llm-consultant

<!-- CI badge: replace OWNER/REPO with your repository after the first push -->
<!-- ![CI](https://github.com/OWNER/go-llm-consultant/actions/workflows/ci.yml/badge.svg) -->

🇷🇺 [Русская версия](README.md)

A Telegram consultant bot in Go: it answers customer questions from a company
knowledge base (FAQ / price list in markdown) using Russian LLMs — **GigaChat**
(Sber) or **YandexGPT**. OpenAI/Anthropic can't be paid for directly from Russia,
so the providers are domestic; each sits behind a common interface and is switched
with a single environment variable.

## What it does

- A user asks a question in Telegram → the bot picks the relevant knowledge-base
  sections, builds a system prompt and queries the LLM → returns the answer.
- The knowledge base is loaded from `knowledge/faq.md` at startup. **RAG-lite**, no
  vectors: the file is split into sections by markdown headings, and sections
  relevant to the question are selected by keyword match with light stemming.
- Prompt size is capped (12,000 characters) — a guard against `400 Bad Request`
  from overflowing the model's context.
- Every question/answer pair is logged to Postgres (`dialogs`).
- `/reload` re-reads the knowledge-base file on the fly — admin only.

## Demo

![Demo](docs/demo.gif)

## Stack

- **Go 1.25**
- **go-telegram/bot** — message handling, "typing…" indicator
- **custom net/http client** (crypto/tls, crypto/x509) — both LLMs; no SDK needed
- **jackc/pgx/v5** + **pgxpool** — dialog log in Postgres
- **pressly/goose/v3** — migrations from `embed.FS`, applied at startup
- **log/slog** — logging (text+Debug in `local`, JSON+Info in prod)
- **Docker Compose** — bot + Postgres with a healthcheck

## Getting API keys

### GigaChat (primary provider — easiest for an individual in Russia)

1. Sign in to [developers.sber.ru](https://developers.sber.ru/) via Sber ID.
2. Create a **GigaChat API** project and get an **Authorization Key** — it is
   `base64(ClientID:ClientSecret)`.
3. Freemium: 1,000,000 tokens for 12 months (scope `GIGACHAT_API_PERS`).
4. Put the key into `GIGACHAT_AUTH_KEY`, adjust `GIGACHAT_SCOPE` if needed.
5. Download the Ministry of Digital Development root certificate: `make cert`
   (see [TLS](#tls-and-the-ministry-certificate)).

Docs: <https://developers.sber.ru/docs/ru/gigachat>

### YandexGPT (second implementation behind the same interface)

1. In [Yandex Cloud](https://yandex.cloud/ru/docs/foundation-models) create a folder
   (you need the `folder_id`) and a service account with the `ai.languageModels.user`
   role.
2. Issue an **API key** for that service account and attach a billing profile.
3. Put in `YANDEX_API_KEY` and `YANDEX_FOLDER_ID`, switch `LLM_PROVIDER=yandexgpt`.

Docs: <https://yandex.cloud/ru/docs/foundation-models>

> **Hand-rolled, no SDK.** The client is plain `net/http`. Third-party SDKs exist
> ([tigusigalpa/gigachat-go](https://github.com/tigusigalpa/gigachat-go),
> [sheeiavellie/go-yandexgpt](https://github.com/sheeiavellie/go-yandexgpt)) and can
> be plugged in as an alternative, but the default is the thin in-house client.

## How to run

### With Docker Compose (recommended)

```bash
cp .env.example .env      # fill in BOT_TOKEN and keys for your chosen LLM provider
make cert                 # downloads the Ministry cert into certs/ (needed for GigaChat)
make up                   # starts Postgres (waits for healthy) and the bot, runs migrations
make down                 # stop
```

- `BOT_TOKEN` — bot token from [@BotFather](https://t.me/BotFather).
- `ADMIN_TG_ID` — your Telegram id (find it via [@userinfobot](https://t.me/userinfobot))
  to access `/reload`.
- `LLM_PROVIDER` — `gigachat` or `yandexgpt`; only the selected provider's keys are required.

### Locally

```bash
export DATABASE_URL="postgres://consultant:consultant@localhost:5432/consultant?sslmode=disable"
export BOT_TOKEN="..." LLM_PROVIDER="gigachat" GIGACHAT_AUTH_KEY="..."
make cert
make run
```

> `.env` is read only by Docker Compose. With `make run` you set the variables
> yourself — there is no dotenv loader in the app.

### Tests and lint

```bash
make test    # go test -race ./...  (no real APIs are hit — httptest only)
make lint    # pinned golangci-lint version
```

## Project layout

```
cmd/bot/                 entry point, process-wide ctx and graceful shutdown
internal/config/         ENV config, provider-specific validation, slog
internal/llm/            LLMClient interface + gigachat.go + yandexgpt.go
internal/knowledge/      faq.md loading, heading chunking, keyword selection
internal/telegram/       Consultant (core) + Bot (transport), /reload
internal/storage/        pgx pool, goose migrations, dialog log
knowledge/faq.md         sample knowledge base
certs/                   Ministry root certificate (fetched by make cert)
migrations/              goose migrations, embedded via embed.FS
docs/DECISIONS.md        engineering decision log, step by step
```

## Technical decisions

### Custom net/http client instead of an SDK

Each LLM needs exactly two HTTP calls (GigaChat: OAuth + chat; Yandex: a single
completion). Pulling in an external SDK isn't worth it, and hand-written code is
easier to audit in the sensitive spots — the TLS pool and the token cache. Both
implementations hide behind the `LLMClient` interface with a single method
`Ask(ctx, systemPrompt, userMessage) (Answer, error)`, where `Answer` carries the
text and the actual token spend (`usage.total_tokens`) for the log; switching
providers is one line in the `llm.New` factory.

### TLS and the Ministry certificate

GigaChat's TLS chain is signed by the Russian Ministry of Digital Development root
CA, which isn't in the system trust store. The client reads the PEM from disk and
appends it to a copy of the system pool (`x509.SystemCertPool` + `AppendCertsFromPEM`),
`MinVersion: TLS 1.2`. The certificate is not stored in git — it is fetched by
`make cert` from the official URL `https://gu-st.ru/.../russian_trusted_root_ca.cer`.
A last-resort local-demo fallback is `GIGACHAT_INSECURE_SKIP_VERIFY=true` (with a log
warning; **never in production**).

### OAuth token cache

The GigaChat token lives for 30 minutes. The client caches it for that period minus
a 60s safety window and refreshes lazily under a mutex (multiple users write to the
bot concurrently). On a `401` response the token is force-refreshed and the request
is retried **exactly once** — no infinite loop.

### RAG-lite without vectors

For a single company's FAQ/price list, embeddings and a vector DB are overkill. The
file is split into sections by headings, and relevant ones are selected by matching
the question's keywords against each section (with prefix stemming so "оплаты" matches
"оплата"). No match → fall back to the first sections so the model still gets general
context.

### Prompt size limit

The total size of selected sections is capped at `MaxPromptChars = 12000` (a rough
token estimate for Russian is characters / 3). Sections are packed in relevance order
until the limit is reached; the rest are dropped. This guards against `400 Bad Request`
on context overflow.

### Timeouts

LLM generation normally takes 10–20 seconds. Every request is wrapped in
`context.WithTimeout(45s)`; the `http.Client`'s own `Timeout` is deliberately `0` so a
short global deadline can't preempt the context and cut generation short.

## Limitations and notes

- Real GigaChat/YandexGPT keys were not used during development — all client checks
  run through `httptest.Server`. Verify response formats against the official docs
  (marked `TODO` in the code).
- A full `app` service under `docker compose up` requires a valid `BOT_TOKEN`: the bot
  runs `getMe` at startup. The DB half (migrations, logging) is verified independently.

## License

[MIT](LICENSE)
