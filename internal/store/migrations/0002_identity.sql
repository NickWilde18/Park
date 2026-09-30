-- Forges, and who members are: docs/design/07-identity.md.

-- A forge instance. Its base address is configuration, kept only here: links and API addresses are built from it
-- when they are needed, and nothing else stores a URL.
CREATE TABLE forge (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind         text        NOT NULL CHECK (kind IN ('github')),
    name         text        NOT NULL,
    base_address text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- A person's account on a forge, keyed by the forge and its stable account ID. Login and name are display data.
CREATE TABLE account (
    forge_id   bigint      NOT NULL REFERENCES forge (id),
    account_id bigint      NOT NULL,
    login      text        NOT NULL,
    name       text        NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (forge_id, account_id)
);

CREATE TABLE member (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    display_name text        NOT NULL,
    disabled     boolean     NOT NULL DEFAULT false,
    admin        boolean     NOT NULL DEFAULT false,
    rules        boolean     NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- A configured way to sign in. The ID is the {provider} of /auth/{provider}, chosen by an admin. The client secret
-- is sealed with the server's secret key.
CREATE TABLE sign_in_provider (
    id            text        PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{0,31}$'),
    kind          text        NOT NULL CHECK (kind IN ('forge')),
    name          text        NOT NULL,
    forge_id      bigint      REFERENCES forge (id),
    client_id     text        NOT NULL,
    client_secret bytea       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'forge') = (forge_id IS NOT NULL))
);

-- A forge account of a member: verified by signing in through the forge, or linked by an admin, who is recorded.
CREATE TABLE member_identity (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    member_id  bigint      NOT NULL REFERENCES member (id),
    forge_id   bigint      NOT NULL REFERENCES forge (id),
    subject    bigint      NOT NULL,
    linked_by  bigint      REFERENCES member (id),
    linked_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (forge_id, subject)
);

CREATE TABLE local_credential (
    member_id     bigint      PRIMARY KEY REFERENCES member (id),
    username      text        NOT NULL UNIQUE CHECK (username = lower(username)),
    password_hash text        NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE browser_session (
    id_hash      bytea       PRIMARY KEY,
    member_id    bigint      NOT NULL REFERENCES member (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);

CREATE INDEX browser_session_member ON browser_session (member_id);

-- The ID is the short one in the token's prefix, park_<id>_…; only a hash of the secret after it is kept.
CREATE TABLE api_token (
    id           text        PRIMARY KEY,
    member_id    bigint      NOT NULL REFERENCES member (id),
    name         text        NOT NULL,
    secret_hash  bytea       NOT NULL,
    scopes       text[]      NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz
);

CREATE INDEX api_token_member ON api_token (member_id);

CREATE TABLE invite (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code_hash  bytea       NOT NULL UNIQUE,
    created_by bigint      NOT NULL REFERENCES member (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_by    bigint      REFERENCES member (id),
    used_at    timestamptz
);

-- The one-time code that creates the first administrator. There is at most one.
CREATE TABLE setup_code (
    only_one   boolean     PRIMARY KEY DEFAULT true CHECK (only_one),
    code_hash  bytea       NOT NULL,
    expires_at timestamptz NOT NULL
);
