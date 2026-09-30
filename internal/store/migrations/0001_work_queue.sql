-- The work queue that Park's stages hand work to each other through; see internal/work.
-- One row per kind and key: while it waits, enqueuing it again moves run_after earlier; while it runs, enqueuing
-- it again sets rerun_after, and it runs again when it finishes. run is the fencing token of the current run.
CREATE TABLE work_item (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind        text        NOT NULL,
    key         text        NOT NULL,
    state       text        NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'running')),
    run_after   timestamptz NOT NULL,
    rerun_after timestamptz,
    attempts    integer     NOT NULL DEFAULT 0,
    run         bigint      NOT NULL DEFAULT 0,
    lease_until timestamptz,
    UNIQUE (kind, key)
);

CREATE INDEX work_item_due ON work_item (run_after) WHERE state = 'waiting';
CREATE INDEX work_item_leased ON work_item (lease_until) WHERE state = 'running';
