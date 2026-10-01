# Telegram Knowledge-Base Consultant Bot

[Русский](README.md)

Answers your customers in Telegram about your prices, services and terms — around the clock, in its own words.

- **Only from your materials.** Quotes prices exactly as they appear in the price list, never calculates discounts, never makes things up.
- **Honestly says "I don't know".** If the answer isn't in your materials, the bot says so and offers to pass the question to a manager. Off-topic chatter gets politely steered back to business.
- **Leads and questions land in your Telegram.** Name, phone and what the customer wants go to the owner's DM or a team group. You can answer the customer right from there: reply to the lead and the bot forwards your answer.
- **Learns from your answers.** An answer to a question the base didn't cover can be added to the base with one tap — only with your approval.
- **Keeps no conversations.** No message texts, names or phone numbers — not in a database, not in files, not in logs.
- **Russian LLMs.** GigaChat or YandexGPT; optionally any service with an OpenAI-compatible API.
- **Checked before hand-off.** A 20-question exam with trick questions and a readable report for the owner.

![Bot demo](docs/demo.gif)

[![CI](https://github.com/matthewprokofiev/go-llm-consultant/actions/workflows/ci.yml/badge.svg)](https://github.com/matthewprokofiev/go-llm-consultant/actions/workflows/ci.yml)
![Go 1.25](https://img.shields.io/badge/Go-1.25-00ADD8)
![License MIT](https://img.shields.io/badge/license-MIT-green)

## How it works

1. A customer types a question. The chat always shows "Leave a request" and "Contacts" buttons.
2. The bot picks the knowledge base sections relevant to the question (and to the previous one, so "and how much is that?" makes sense) and builds a prompt with rules: answer only from the base, quote prices verbatim, never follow instructions embedded in the customer's message.
3. The model answers and tags its answer: from the base, partial, not in the base, or off-topic. The tag is stripped and the bot decides what to offer:
   - no answer or only part of it — a "Pass the question to a manager" button; if the customer declines, the bot gives the business contacts;
   - a clear intent to buy or book — a gentle offer to leave a request, at most once per conversation;
   - off-topic — just the text, nothing goes to the owner.
4. The request form collects a phone number (one tap on "share contact", or typed) and a name. A question asked mid-form gets answered and the form resumes. A consent line is shown if the owner configured one.
5. The message to the owner is retried for up to 5 minutes, since Telegram is throttled in Russia. If delivery ultimately fails, the customer is told so honestly and gets the contacts to call directly.
6. When the owner answers a question the base didn't cover, the bot offers to add that answer to the knowledge base. The owner sees exactly what will be saved and can replace a personal reply with a general wording. After "Add to base" the bot answers everyone who asks something similar.

The knowledge base is a plain markdown file. `/reload` re-reads it without a restart. For a new client there is a tool that builds the base from their PDF, Word and spreadsheet files, plus the exam — see the [client setup guide](docs/NEW_CLIENT.md) (in Russian).

## Running it

You need Docker, a bot token from [@BotFather](https://t.me/BotFather) and an LLM key.

```bash
git clone https://github.com/matthewprokofiev/go-llm-consultant
cd go-llm-consultant
cp .env.example .env   # fill in BOT_TOKEN, OWNER_CHAT_ID, BUSINESS_*, the LLM key
make cert              # Ministry of Digital Development root CA (needed for GigaChat)
make up                # bot without a database
```

Stop with `make down`. Each bot is a separate container with its own `.env` and knowledge base, so bots for different clients can share one server:

```bash
ENV_FILE=clients/kadr/.env KNOWLEDGE_DIR=clients/kadr/knowledge docker compose -p kadr up -d --build
```

### Statistics (optional)

`make up-stats` starts Postgres alongside, and the admin gets a `/stats` command: questions over 7 and 30 days, how many had no answer or were off-topic, leads delivered and lost, tokens spent. Only such counters are stored, with timestamps rounded to the hour — no texts, names or user IDs.

## LLM providers

**GigaChat** — [developers.sber.ru](https://developers.sber.ru/portal/products/gigachat-api). You need an Authorization Key, `base64(ClientID:ClientSecret)` from the dashboard. The free personal tier (scope `GIGACHAT_API_PERS`) is fine for testing; for a bot serving a business the model is connected to the client's own account (sole trader or company, scope `GIGACHAT_API_B2B` or `GIGACHAT_API_CORP`) and the client pays Sber directly. The Ministry root certificate is mandatory — `make cert` installs it.

**YandexGPT** — [Yandex Cloud](https://yandex.cloud/en/services/foundation-models). You need a `folder_id`, a service account with the `ai.languageModels.user` role, and an API key. Free access without a billing profile has been discontinued.

**OpenAI-compatible services** — ChatGPT, DeepSeek and others. Connected through settings only: `LLM_PROVIDER=openai`, base URL, model and key. ⚠️ With these services customer questions are sent to servers outside Russia, and the client sets up payment for the service themselves.

All safeguards behave the same for every provider: honesty rules in the prompt, the prompt size cap, a 45-second timeout, one retry on network or server errors. If the provider is down or the key is wrong, the customer gets "I can't answer right now" plus the business contacts, and the log keeps the technical reason.

## Configuration

Everything lives in `.env`; a new client never needs a code change. Multi-line text is written on one line with `\n`.

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `BOT_TOKEN` | yes | — | Token from @BotFather |
| `OWNER_CHAT_ID` | yes | — | Where leads go: the owner's ID or a team group ID (negative) |
| `BUSINESS_NAME` | yes | — | Business name shown to customers |
| `BUSINESS_CONTACTS` | yes | — | Phone, address, hours: the "Contacts" button and fallback answer |
| `LLM_PROVIDER` | no | `gigachat` | `gigachat`, `yandexgpt` or `openai` |
| `GREETING_TEXT` | no | "Hello! I'm the online assistant…" | Greeting on `/start` |
| `BUTTON_LEAD_TEXT` | no | `📝 Оставить заявку` | Menu button |
| `BUTTON_CONTACTS_TEXT` | no | `📍 Контакты` | Menu button |
| `LEAD_SENT_TEXT` | no | "Thank you! Your request has been passed on…" | Lead confirmation |
| `QUESTION_SENT_TEXT` | no | "Thank you! Your question has been passed on…" | Question hand-off confirmation |
| `DELIVERY_FAILED_TEXT` | no | "Sorry, we couldn't pass your message on…" | Shown when delivery to the owner fails; contacts follow |
| `OWNER_REPLY_PREFIX` | no | "A manager replied:" | Label on the owner's forwarded answer |
| `CONSENT_TEXT`, `PRIVACY_URL` | no | — | Consent text and privacy policy link before the phone request; empty means no line |
| `LEAD_OFFER_ENABLED` | no | `true` | Offer a request when the customer shows intent to buy or book |
| `LIMIT_LEADS_PER_HOUR` | no | `3` | Requests per hour per person, 0 means unlimited |
| `LIMIT_QUESTIONS_PER_HOUR` | no | `20` | Questions per hour per person, 0 means unlimited |
| `TIMEZONE` | no | `Europe/Moscow` | Time zone for lead timestamps |
| `ADMIN_TG_ID` | no | — | Who may run `/reload` and `/stats` |
| `DATABASE_URL` | no | — | Enables statistics; set automatically by `docker-compose.stats.yml` |
| `KNOWLEDGE_PATH` | no | `knowledge/faq.md` | Path to the knowledge base |
| `APP_ENV` | no | `local` | `local` → text logs at Debug, otherwise JSON at Info |

Default texts are in Russian; the English in the table is a translation.

**LLM** — only the selected provider's keys are required.

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `GIGACHAT_AUTH_KEY` | yes | — | `base64(ClientID:ClientSecret)` |
| `GIGACHAT_SCOPE` | no | `GIGACHAT_API_PERS` | Access scope |
| `GIGACHAT_MODEL` | no | `GigaChat-2` | `GigaChat-2`, `GigaChat-2-Pro`, `GigaChat-2-Max` |
| `GIGACHAT_CERT_PATH` | no | `certs/russian_trusted_root_ca.cer` | Ministry root certificate |
| `GIGACHAT_INSECURE_SKIP_VERIFY` | no | `false` | Disables TLS verification. Debugging last resort, never in production |
| `YANDEX_API_KEY`, `YANDEX_FOLDER_ID` | yes | — | Service account key and folder |
| `YANDEX_MODEL` | no | `yandexgpt-lite` | Model |
| `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `OPENAI_MODEL` | yes | — | API base URL, key and model |
| `LLM_PRICE_INPUT_PER_1K`, `LLM_PRICE_OUTPUT_PER_1K`, `LLM_MIN_MONTHLY` | no | — | Prices in RUB for the exam's cost summary; the bot doesn't need them |

Without the critical settings the bot refuses to start and lists everything missing in one message.

## Engineering decisions

The full log is in [docs/DECISIONS.md](docs/DECISIONS.md).

**The bot stores nothing.** Recent messages (3 pairs, 30 minutes), the form step and rate limits live in memory only and vanish on restart. The only thing the bot writes to disk is answers the owner explicitly approved for the knowledge base. Logs contain technical events only: answer time, tokens, status, whether the owner's message was delivered. Telegram errors are scrubbed before logging: the library puts the token-bearing URL and the full response body, with user messages, into them.

**Honesty in two places.** The prompt forbids inventing and calculating, and a status tag lets the bot decide what to do next without a second model call. No tag — the answer is shown as is and nothing is offered.

**A thin HTTP client instead of an SDK.** GigaChat speaks the same `/chat/completions` format as OpenAI-compatible services, so the request code is shared; GigaChat only adds OAuth and the certificate. YandexGPT has its own client. One interface:

```go
type LLMClient interface {
    Ask(ctx context.Context, messages []Message) (Answer, error)
}
```

**The Ministry root certificate.** GigaChat's chain is signed by a root CA missing from system trust stores. The client appends it to a **copy** of the system pool, `MinVersion: TLS 1.2`. The certificate isn't committed — `make cert` fetches it.

**OAuth token caching.** The GigaChat token is cached for its lifetime minus a minute; refreshes are collapsed with `singleflight`. On `401` — one forced refresh and exactly one retry.

**Prompt cap and timeouts.** Sections are packed by relevance up to 12,000 characters. Each question gets its own 45-second `context.WithTimeout` covering the retry too, while `http.Client.Timeout` is deliberately zero so a long generation isn't cut mid-stream.

## Development

```bash
make test   # tests under -race
make lint   # pinned golangci-lint version
make cert   # Ministry root certificate into certs/
go run ./cmd/kb -out draft.md files...                       # client files → knowledge base draft
go run ./cmd/exam -env .env -questions exam.csv -out exam    # the exam
```

Tests never hit external APIs: LLM clients run against `httptest.Server`, Telegram logic is tested through pure functions.

## Layout

```
cmd/bot/              the bot: process-wide ctx and graceful shutdown
cmd/kb/               client files → knowledge base tool
cmd/exam/             the exam: client report and cost summary
internal/config/      ENV settings with defaults and validation
internal/llm/         GigaChat, YandexGPT, OpenAI-compatible API
internal/consultant/  answering core: prompt, statuses, intent, retry
internal/knowledge/   knowledge base: heading sections and selection
internal/telegram/    menu, requests, question hand-off, delivery to the owner
internal/storage/     Postgres for anonymous statistics (optional)
internal/kbconv/      PDF, DOCX, XLSX, CSV, TXT, MD parsing
internal/exam/        verdicts, runs, HTML report
knowledge/faq.md      demo photo studio knowledge base
examples/photostudio/ demo sources as "client files" and the exam questions
docs/                 decision log and client setup guide
```

Stack: Go 1.25, [go-telegram/bot](https://github.com/go-telegram/bot), a hand-rolled client on `net/http` + `crypto/tls`, [go-pdfium](https://github.com/klippa-app/go-pdfium) (WebAssembly) in the knowledge base tool, [pgx/v5](https://github.com/jackc/pgx) and [goose](https://github.com/pressly/goose) for statistics, `log/slog`, Docker Compose.

## Limitations

- No semantic search: sections are selected by word overlap with light stemming. The model links synonyms ("Saturday" and "weekend") only if the right section made it into the selection. Fine for a price list and an FAQ of a few dozen pages; not for hundreds.
- Only the text layer is taken from PDFs: scans and text in images aren't recognized; the tool flags them in its report.
- The bot keeps no history: the owner gets no question statistics beyond anonymous counters in statistics mode. Conversation memory is the last 3 exchanges and 30 minutes, until restart.
- A lead can be lost if the process crashes: delivery retries live in memory. On a graceful shutdown the customer gets an honest message and the contacts.
- The owner's reply reaches the customer only if the bot hasn't restarted and less than 48 hours have passed; only text is forwarded, one way.
- Question and request limits reset on restart.
- The base grows only with answers the owner approved with a button; they're appended to an "Ответы администратора" (manager answers) section and aren't covered by the exam until it's re-run. Duplicate questions aren't merged.
- YandexGPT and OpenAI-compatible services are verified against mocks; only GigaChat has been tested against the live model.
- The model can be wrong even with an accurate base. That's why there is an exam before hand-off, and doubtful questions go to a human.

## License

MIT — see [LICENSE](LICENSE).
