-- Tasks: docs/design/06-tasks.md.

-- Per member: the actions that may become tasks, and the repositories they are limited to; every one when null.
CREATE TABLE automation (
    member_id  bigint      PRIMARY KEY REFERENCES member (id),
    actions    text[]      NOT NULL DEFAULT '{}',
    repos      bigint[],
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Work a verdict calls for, for one member on one unit ("group:<id>" or "pr:<id>"). A leased or verifying task is
-- active, and locks the pull requests in locks: every pull request of every group and chain its items touch (T4).
-- The lease is its ID, the token holding it, the executor's label, and its expiry; claim_key is the Idempotency-Key
-- of the claim that took it.
CREATE TABLE task (
    id               bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    member_id        bigint      NOT NULL REFERENCES member (id),
    unit             text        NOT NULL,
    state            text        NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'leased', 'verifying', 'done', 'needs_person', 'cancelled', 'dismissed')),
    priority         text        NOT NULL DEFAULT 'normal' CHECK (priority IN ('normal', 'low')),
    attempt          integer     NOT NULL DEFAULT 0,
    lease_id         text,
    lease_token      text,
    executor         text,
    lease_expires_at timestamptz,
    claim_key        text,
    locks            bigint[]    NOT NULL DEFAULT '{}',
    cancel_marked    boolean     NOT NULL DEFAULT false,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    deadline         timestamptz,
    verified_at      timestamptz,
    note             text        NOT NULL DEFAULT '',
    waiting          boolean     NOT NULL DEFAULT false,
    reason           text        NOT NULL DEFAULT '',
    queued_at        timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX task_queued ON task (member_id, unit) WHERE state = 'queued';
CREATE UNIQUE INDEX task_lease ON task (lease_id) WHERE lease_id IS NOT NULL;
CREATE UNIQUE INDEX task_claim ON task (lease_token, claim_key) WHERE claim_key IS NOT NULL;
CREATE INDEX task_active ON task USING gin (locks) WHERE state IN ('leased', 'verifying');

-- One action on one pull request of a task, with its targets, and the facts at the start of the attempt that its
-- post-condition compares against.
CREATE TABLE task_item (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    task_id  bigint NOT NULL REFERENCES task (id),
    pr_id    bigint NOT NULL REFERENCES pr_fact (id),
    action   text   NOT NULL,
    targets  jsonb  NOT NULL,
    priority text   NOT NULL DEFAULT 'normal' CHECK (priority IN ('normal', 'low')),
    start    jsonb,
    state    text   NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'satisfied', 'dropped')),
    failures text[] NOT NULL DEFAULT '{}',
    report   jsonb,
    UNIQUE (task_id, pr_id, action)
);

-- The attempts used per member, unit, and action. A new task for the same work continues the count; only a person
-- resets it (T9).
CREATE TABLE attempt_budget (
    member_id bigint  NOT NULL REFERENCES member (id),
    unit      text    NOT NULL,
    action    text    NOT NULL,
    used      integer NOT NULL DEFAULT 0,
    PRIMARY KEY (member_id, unit, action)
);

-- Every state change, report, heartbeat note, and verification result, with its actor: "token:<id>", "member:<id>",
-- or "park". Never updated or deleted.
CREATE TABLE task_event (
    id      bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    task_id bigint      NOT NULL REFERENCES task (id),
    kind    text        NOT NULL,
    actor   text        NOT NULL,
    state   text        NOT NULL,
    detail  jsonb       NOT NULL DEFAULT '{}',
    at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX task_event_task ON task_event (task_id, id);

CREATE FUNCTION task_event_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'task events are never changed';
END
$$;

CREATE TRIGGER task_event_append_only BEFORE UPDATE OR DELETE ON task_event
    FOR EACH ROW EXECUTE FUNCTION task_event_append_only();
