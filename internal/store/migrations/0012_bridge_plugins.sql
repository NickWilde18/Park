-- Bridges Park runs through plugins: docs/design/03-grouping.md#bridges-park-runs and docs/design/12-bridge-plugins.md.

-- A plugin's switch. A plugin is an executable Park finds in its plugin directory when it starts, so this row only
-- says what an admin chose; a plugin without one is off. A row may outlive its plugin, when the executable is gone.
CREATE TABLE bridge_plugin (
    kind       text        PRIMARY KEY,
    enabled    boolean     NOT NULL,
    changed_by bigint      NOT NULL REFERENCES member (id),
    changed_at timestamptz NOT NULL DEFAULT now()
);

-- A bridge with a plugin is one Park runs; one without is external, and posts with its tokens. Every bridge has its own
-- switch. A Park-run bridge's credentials are its plugin's fields, sealed together as one JSON object; its chat is the
-- platform's ID for the chat it reads, and the chat's name is only shown. It runs every so many minutes, counts threads
-- from so many days back, and asks the model about a thread that links propose_from pull requests or more.
ALTER TABLE bridge
    ADD COLUMN plugin        text,
    ADD COLUMN enabled       boolean NOT NULL DEFAULT true,
    ADD COLUMN credentials   bytea,
    ADD COLUMN chat          text    NOT NULL DEFAULT '',
    ADD COLUMN chat_name     text    NOT NULL DEFAULT '',
    ADD COLUMN every_minutes integer NOT NULL DEFAULT 10 CHECK (every_minutes BETWEEN 1 AND 1440),
    ADD COLUMN lookback_days integer NOT NULL DEFAULT 45 CHECK (lookback_days BETWEEN 1 AND 365),
    ADD COLUMN propose_from  integer NOT NULL DEFAULT 4 CHECK (propose_from = 0 OR propose_from BETWEEN 2 AND 100);

-- The platform account a Park-run bridge reads as: the platform's ID and name for it, and its tokens, sealed, which
-- only the plugin reads. Lapsed is when the plugin last said the account has to be connected again.
CREATE TABLE bridge_account (
    bridge_id    bigint      PRIMARY KEY REFERENCES bridge (id),
    subject      text        NOT NULL,
    name         text        NOT NULL,
    tokens       bytea       NOT NULL,
    connected_by bigint      NOT NULL REFERENCES member (id),
    connected_at timestamptz NOT NULL DEFAULT now(),
    lapsed_at    timestamptz
);
