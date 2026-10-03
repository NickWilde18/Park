-- The rounds of the bridges Park runs: docs/design/03-grouping.md#flow-a-round.

-- A Park-run bridge's model, set as its credentials are: the address of a service that speaks the chat completions
-- protocol, the model's name, its key, sealed, and how long an answer may take. Without an address, the bridge asks
-- no model, and every thread is an assertion. Its state is what it remembers between rounds: what it posted, with
-- each thread's time, and the model's answers.
ALTER TABLE bridge
    ADD COLUMN model_url             text    NOT NULL DEFAULT '',
    ADD COLUMN model_name            text    NOT NULL DEFAULT '',
    ADD COLUMN model_key             bytea,
    ADD COLUMN model_timeout_seconds integer NOT NULL DEFAULT 60 CHECK (model_timeout_seconds BETWEEN 1 AND 600),
    ADD COLUMN state                 jsonb   NOT NULL DEFAULT '{}';

-- One round of a Park-run bridge: when it ran, how many messages it read, how many assertions and proposals it
-- posted or retracted, and how it ended: ok, or failed, with the code of what went wrong; the details went to the log.
CREATE TABLE bridge_round (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    bridge_id  bigint      NOT NULL REFERENCES bridge (id),
    started_at timestamptz NOT NULL,
    ended_at   timestamptz NOT NULL,
    read       integer     NOT NULL,
    changes    integer     NOT NULL,
    outcome    text        NOT NULL CHECK (outcome IN ('ok', 'failed')),
    reason     text        NOT NULL DEFAULT ''
);

CREATE INDEX bridge_round_latest ON bridge_round (bridge_id, ended_at DESC);
