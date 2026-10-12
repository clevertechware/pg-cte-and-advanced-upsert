CREATE TABLE jobs (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type        text        NOT NULL,
    status      text        NOT NULL,
    config      jsonb       NOT NULL,
    result      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    lock_token  text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    finished_at timestamptz,
    duration_ms bigint
);

CREATE UNIQUE INDEX jobs_price_refresh_pending_catalog_uq
    ON jobs ((config->>'catalogId'))
    WHERE status = 'PENDING' AND type = 'PRICE_REFRESH';
