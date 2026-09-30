-- Facts from forges: docs/design/02-facts.md.

-- The kinds a forge can be are the ones the code registers, and forges.Create checks against them; a list in the
-- schema as well would have to change with every kind.
ALTER TABLE forge DROP CONSTRAINT forge_kind_check;

-- A connection to a forge: its credential — a token, or an app installation — and its webhook secret, both sealed
-- with the server's secret key.
CREATE TABLE installation (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    forge_id        bigint      NOT NULL REFERENCES forge (id),
    name            text        NOT NULL,
    credential_kind text        NOT NULL CHECK (credential_kind IN ('token', 'app')),
    credential      bytea       NOT NULL,
    webhook_secret  bytea,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- A repository on a forge. Owner and name address it on the forge and change with renames; the forge's own ID
-- does not. Protection holds what the forge requires of pull requests into each base branch in use.
CREATE TABLE repo (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    forge_id        bigint      NOT NULL REFERENCES forge (id),
    forge_repo_id   bigint      NOT NULL,
    installation_id bigint      NOT NULL REFERENCES installation (id),
    owner           text        NOT NULL,
    name            text        NOT NULL,
    default_branch  text        NOT NULL DEFAULT '',
    watched         boolean     NOT NULL DEFAULT true,
    protection      jsonb       NOT NULL DEFAULT '{}',
    refreshed_at    timestamptz,
    UNIQUE (forge_id, forge_repo_id)
);

-- A forge team's members, to resolve team review requests. Members is null when the forge would not tell.
CREATE TABLE team (
    forge_id     bigint      NOT NULL REFERENCES forge (id),
    team_id      bigint      NOT NULL,
    org          text        NOT NULL,
    slug         text        NOT NULL,
    name         text        NOT NULL DEFAULT '',
    members      bigint[],
    refreshed_at timestamptz,
    PRIMARY KEY (forge_id, team_id)
);

-- What an account may do in a repository, as the forge reports it: admin, maintain, write, triage, read, or none.
CREATE TABLE repo_access (
    repo_id      bigint      NOT NULL REFERENCES repo (id),
    account_id   bigint      NOT NULL,
    level        text        NOT NULL,
    refreshed_at timestamptz NOT NULL,
    PRIMARY KEY (repo_id, account_id)
);

-- The current state of one pull request: written by the refresh flow only. The state is the normalized record;
-- the columns beside it are copies of fields that deliveries are matched by. A state fetched earlier than the
-- stored one never replaces it.
CREATE TABLE pr_fact (
    id               bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id          bigint      NOT NULL REFERENCES repo (id),
    number           integer     NOT NULL,
    state            jsonb       NOT NULL,
    seq              bigint      NOT NULL DEFAULT 0,
    open             boolean     NOT NULL,
    head_sha         text        NOT NULL,
    head_branch      text        NOT NULL,
    head_repo_id     bigint,
    base_branch      text        NOT NULL,
    forge_updated_at timestamptz NOT NULL,
    fetched_at       timestamptz NOT NULL,
    refreshed_at     timestamptz NOT NULL,
    stale            boolean     NOT NULL DEFAULT false,
    UNIQUE (repo_id, number)
);

CREATE INDEX pr_fact_head_sha ON pr_fact (repo_id, head_sha) WHERE open;
CREATE INDEX pr_fact_head_branch ON pr_fact (head_repo_id, head_branch) WHERE open;
CREATE INDEX pr_fact_base_branch ON pr_fact (repo_id, base_branch) WHERE open;

-- One change to one pull request, in order. Never updated or deleted: pr_fact can be rebuilt by replaying them.
CREATE TABLE fact_event (
    pr_id       bigint      NOT NULL REFERENCES pr_fact (id),
    seq         bigint      NOT NULL,
    changes     jsonb       NOT NULL,
    source      text        NOT NULL,
    observed_at timestamptz NOT NULL,
    PRIMARY KEY (pr_id, seq)
);

-- Webhook deliveries already processed, kept for a week.
CREATE TABLE delivery (
    installation_id bigint      NOT NULL REFERENCES installation (id),
    delivery_id     text        NOT NULL,
    received_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (installation_id, delivery_id)
);
