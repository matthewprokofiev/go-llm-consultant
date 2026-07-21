-- +goose Up
CREATE TABLE dialogs (
    id          BIGSERIAL PRIMARY KEY,
    user_tg_id  BIGINT      NOT NULL,
    question    TEXT        NOT NULL,
    answer      TEXT        NOT NULL,
    -- Каким провайдером сгенерирован ответ: gigachat | yandexgpt. Полезно при
    -- сравнении реализаций на одних и тех же вопросах.
    provider    TEXT        NOT NULL,
    -- Оценка израсходованных токенов. Может быть 0/NULL, если провайдер не вернул usage.
    tokens_used INT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Основной аналитический запрос — «диалоги пользователя за период»:
-- индекс в этом порядке отдаёт их без сортировки всей таблицы.
CREATE INDEX idx_dialogs_user_created
    ON dialogs (user_tg_id, created_at DESC);

-- +goose Down
DROP TABLE dialogs;
