# Telegram LLM Consultant Bot

[Русский](README.md)

Answers customer questions from your price list and FAQ — around the clock, in its own words rather than through a fixed button menu. The knowledge base is a plain markdown file: edit the text, send `/reload`, and the bot answers from the new version.

![Bot demo](docs/demo.gif)

[![CI](https://github.com/matthewprokofiev/go-llm-consultant/actions/workflows/ci.yml/badge.svg)](https://github.com/matthewprokofiev/go-llm-consultant/actions/workflows/ci.yml)
![Go 1.25](https://img.shields.io/badge/Go-1.25-00ADD8)
![License MIT](https://img.shields.io/badge/license-MIT-green)

It runs on Russian LLMs — **GigaChat** (Sber) or **YandexGPT**. OpenAI and Anthropic can't be paid for directly from Russia, so the providers are domestic; both sit behind a shared interface and are swapped with a single environment variable.

## What it does

1. A customer asks a question in Telegram as plain text.
2. The bot picks the knowledge base sections relevant to that question and assembles a system prompt from them.
3. The request goes to the LLM and the answer comes back to the chat, with a "typing" indicator while generation runs.
4. Each question/answer pair and its token usage are written to PostgreSQL — so you can see what people actually ask and what it costs.

The knowledge base is `knowledge/faq.md`, loaded at startup. `/reload` re-reads the file on the fly without a restart; it is restricted to the admin listed in `ADMIN_TG_ID`.

Retrieval is **RAG-lite, no vectors**: the text is split on markdown headings and sections are selected by word overlap with light stemming. For a price list and an FAQ a few pages long that's enough, and a vector store plus embeddings would be infrastructure and cost with no payoff at this size.

## Running it

You need Docker, a bot token from [@BotFather](https://t.me/BotFather), and a key for one of the LLM providers.

```bash
git clone https://github.com/matthewprokofiev/go-llm-consultant
cd go-llm-consultant
cp .env.example .env   # fill in BOT_TOKEN and the provider keys
make cert              # download the Ministry of Digital Development root CA (GigaChat needs it)
make up
```

Stop with `make down`.

### Provider keys

**GigaChat** — [developers.sber.ru](https://developers.sber.ru/portal/products/gigachat-api). You need an Authorization Key, which is `base64(ClientID:ClientSecret)` from the dashboard. Individuals get a freemium tier: 1,000,000 tokens over 12 months, scope `GIGACHAT_API_PERS`, no billing profile required. The Ministry root certificate is mandatory — `make cert` installs it.

**YandexGPT** — [Yandex Cloud](https://yandex.cloud/en/services/foundation-models). You need a `folder_id`, a service account with the `ai.languageModels.user` role, and an API key. Switch with `LLM_PROVIDER=yandexgpt`. Note that free access without a billing profile has been discontinued.

## Configuration

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `BOT_TOKEN` | yes | — | Token from @BotFather |
| `LLM_PROVIDER` | yes | `gigachat` | `gigachat` or `yandexgpt` |
| `DATABASE_URL` | yes | — | PostgreSQL DSN (set automatically in compose) |
| `ADMIN_TG_ID` | no | — | The only Telegram ID allowed to run `/reload`; empty means nobody can |
| `KNOWLEDGE_PATH` | no | `knowledge/faq.md` | Path to the knowledge base |
| `APP_ENV` | no | `local` | `local` → text logs at Debug, otherwise JSON at Info |

**GigaChat only**

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `GIGACHAT_AUTH_KEY` | yes | — | `base64(ClientID:ClientSecret)` |
| `GIGACHAT_SCOPE` | no | `GIGACHAT_API_PERS` | Access scope |
| `GIGACHAT_MODEL` | no | `GigaChat-2` | Model |
| `GIGACHAT_CERT_PATH` | no | `certs/russian_trusted_root_ca.cer` | Ministry root certificate |
| `GIGACHAT_INSECURE_SKIP_VERIFY` | no | `false` | Disables TLS chain verification. Debugging last resort, never in production |

**YandexGPT only**

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `YANDEX_API_KEY` | yes | — | Service account API key |
| `YANDEX_FOLDER_ID` | yes | — | Folder identifier |
| `YANDEX_MODEL` | no | `yandexgpt-lite` | Model |

Validation is provider-specific: only the keys for the provider named in `LLM_PROVIDER` are required.

## Engineering decisions

The full log is in [docs/DECISIONS.md](docs/DECISIONS.md).

**A thin HTTP client instead of an SDK.** Each provider needs at most two requests: GigaChat does OAuth plus chat, Yandex does a single completion. The available Go SDKs for both are immature and thinly maintained, and the dependency would have to ship to production. The interface is one method:

```go
type LLMClient interface {
    Ask(ctx context.Context, systemPrompt, userMessage string) (Answer, error)
}
```

The provider is chosen by one line in the `llm.New` factory. Adding a third means writing a ~150-line file and touching nothing else.

**The Ministry root certificate.** GigaChat's chain is signed by a root CA that isn't in the system trust store, so without it the client gets `x509: certificate signed by unknown authority`. The client reads the PEM from disk and appends it to a **copy** of the system pool (`x509.SystemCertPool` + `AppendCertsFromPEM`) rather than replacing it, with `MinVersion: TLS 1.2`. The certificate itself is not committed — `make cert` fetches it.

**OAuth token caching.** A GigaChat token lives 30 minutes. The client caches it for that minus a 60-second safety window and refreshes lazily under a mutex. On `401` it forces one refresh and retries exactly once, rather than looping.

**Prompt size cap.** Selected sections are packed in relevance order up to `MaxPromptChars = 12000`. Without a cap, a knowledge base of a few dozen pages reliably produces `400 Bad Request`; sending the most relevant part is strictly better than sending everything and failing.

**Timeouts.** Each request gets its own `context.WithTimeout` of 45 seconds, while `http.Client.Timeout` is deliberately zero: a global client timeout would cut a long generation mid-stream and hand the user a broken connection instead of an answer.

## Development

```bash
make test   # tests under -race
make lint   # pinned golangci-lint version
make cert   # Ministry root certificate into certs/
make run    # local run (export the env vars in your shell)
```

Tests never hit external APIs — both clients are exercised through `httptest.Server`.

## Layout

```
cmd/bot/            entry point, process-wide ctx and graceful shutdown
internal/config/    ENV config, provider-specific validation, slog
internal/llm/       LLMClient interface + gigachat.go + yandexgpt.go
internal/knowledge/ faq.md loading, heading chunking, section selection
internal/telegram/  Consultant (logic) + Bot (transport), /reload
internal/storage/   pgx pool, goose migrations, dialog log
knowledge/faq.md    sample knowledge base
certs/              Ministry root certificate (fetched by make cert)
docs/DECISIONS.md   engineering decision log
```

Stack: Go 1.25, [go-telegram/bot](https://github.com/go-telegram/bot), a hand-rolled client on `net/http` + `crypto/tls` + `crypto/x509`, [pgx/v5](https://github.com/jackc/pgx), [goose](https://github.com/pressly/goose), `log/slog`, Docker Compose.

## Limitations

- Both API response formats were verified against mocks, not live keys; a run with real credentials is required before production.
- The bot answers one question at a time — conversation history is not passed into the prompt, so a follow-up like "and how much is that?" won't be linked to the previous turn.
- Keyword selection loses to vector search on large, heterogeneous corpora. Fine for a price list and an FAQ; not fine for hundreds of pages.
- There is no per-user rate limiting: a public launch would burn through the token quota in a day.
- The model can be wrong even with an accurate knowledge base. For anything sensitive — pricing, contract terms — the answer should route to a human rather than replace one.

## License

MIT — see [LICENSE](LICENSE).
