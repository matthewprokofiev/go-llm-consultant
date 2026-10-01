-- +goose Up
-- Бот больше не хранит переписку: вопросы, ответы и id пользователей уходят
-- вместе с таблицей dialogs. Вместо неё — обезличенные события для статистики.
DROP TABLE IF EXISTS dialogs;

CREATE TABLE events (
    id            BIGSERIAL PRIMARY KEY,
    -- Время округлено до часа: для статистики точнее не нужно, а точная метка
    -- помогала бы сопоставить событие с конкретным сообщением в Telegram.
    created_at    TIMESTAMPTZ NOT NULL DEFAULT date_trunc('hour', now()),
    -- answer | partial | not_found | off_topic | unmarked | llm_error | limited
    -- | lead_sent | lead_failed | question_sent | question_failed
    kind          TEXT        NOT NULL,
    input_tokens  INT         NOT NULL DEFAULT 0,
    output_tokens INT         NOT NULL DEFAULT 0
);

-- /stats считает события за последние 7 и 30 дней.
CREATE INDEX idx_events_created ON events (created_at);

-- +goose Down
DROP TABLE events;

CREATE TABLE dialogs (
    id          BIGSERIAL PRIMARY KEY,
    user_tg_id  BIGINT      NOT NULL,
    question    TEXT        NOT NULL,
    answer      TEXT        NOT NULL,
    provider    TEXT        NOT NULL,
    tokens_used INT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_dialogs_user_created
    ON dialogs (user_tg_id, created_at DESC);
