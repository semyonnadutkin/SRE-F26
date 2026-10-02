-- +goose Up

CREATE TABLE users (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT users_username_policy
        CHECK (username ~ '^[a-z0-9_]{3,32}$'),

    CONSTRAINT users_password_hash
        CHECK (password_hash ~ '^\$2[aby]\$12\$[./A-Za-z0-9]{53}$')
);

CREATE TABLE sessions (
    token_hash BYTEA PRIMARY KEY,

    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,

    CONSTRAINT sessions_token_hash_length
        CHECK (octet_length(token_hash) = 32),

    CONSTRAINT sessions_expiration
        CHECK (expires_at > created_at)
);

CREATE INDEX sessions_expires_idx
    ON sessions (expires_at);

CREATE TABLE request_limits (
    key TEXT PRIMARY KEY,
    count INTEGER NOT NULL CHECK (count > 0),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX request_limits_expires_idx
    ON request_limits (expires_at);

CREATE TABLE files (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,

    name TEXT NOT NULL,
    storage_key TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    state TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT files_name_length
        CHECK (char_length(btrim(name)) BETWEEN 1 AND 255),

    CONSTRAINT files_storage_key_not_empty
        CHECK (btrim(storage_key) <> ''),

    CONSTRAINT files_content_type_not_empty
        CHECK (btrim(content_type) <> ''),

    CONSTRAINT files_size_nonnegative
        CHECK (size_bytes >= 0),

    CONSTRAINT files_state
        CHECK (state IN ('uploading', 'ready', 'deleting'))
);

CREATE INDEX files_pending_idx
    ON files (updated_at)
    WHERE state <> 'ready';

CREATE INDEX files_ready_owner_idx
    ON files (user_id, id DESC)
    WHERE state = 'ready';


-- +goose Down

DROP TABLE files;
DROP TABLE request_limits;
DROP TABLE sessions;
DROP TABLE users;