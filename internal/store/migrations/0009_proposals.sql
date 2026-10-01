-- Bridge proposals: docs/design/03-grouping.md#flow-bridge-proposals.

-- A bridge's proposed groups under an anchor, which change nothing until a member accepts them (G16). Only one
-- proposal per anchor is live: a repost of other groups retracts it and opens another, and history is kept. The
-- fingerprint is of the groups alone, so that a repost of the same groups matches whatever the thread's title.
CREATE TABLE proposal (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source       text        NOT NULL,
    anchor       text        NOT NULL,
    title        text,
    evidence     text[]      NOT NULL DEFAULT '{}',
    fingerprint  text        NOT NULL,
    state        text        NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'accepted', 'dismissed')),
    token_id     text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    decided_at   timestamptz,
    decided_by   bigint      REFERENCES member (id),
    retracted_at timestamptz
);

CREATE UNIQUE INDEX proposal_live_anchor ON proposal (source, anchor) WHERE retracted_at IS NULL;
CREATE INDEX proposal_open ON proposal (created_at) WHERE state = 'open' AND retracted_at IS NULL;

-- One proposed group: its place in the proposal, an optional name, and its pull requests as repository and number,
-- so that a bridge can name one Park has not collected yet.
CREATE TABLE proposal_group (
    proposal_id bigint  NOT NULL REFERENCES proposal (id),
    position    integer NOT NULL,
    name        text,
    members     jsonb   NOT NULL,
    PRIMARY KEY (proposal_id, position)
);

-- A proposal changes what the groups page shows when it opens, and when it is decided, retracted, or retitled.
ALTER TABLE event_notice DROP CONSTRAINT event_notice_kind_check;
ALTER TABLE event_notice ADD CONSTRAINT event_notice_kind_check CHECK (kind IN ('verdict', 'task', 'group', 'pr', 'proposal'));

CREATE CONSTRAINT TRIGGER proposal_created_notice AFTER INSERT ON proposal DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION notice('proposal', 'id');
CREATE CONSTRAINT TRIGGER proposal_notice AFTER UPDATE ON proposal DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
    WHEN ((OLD.state, OLD.retracted_at, OLD.title) IS DISTINCT FROM (NEW.state, NEW.retracted_at, NEW.title))
    EXECUTE FUNCTION notice('proposal', 'id');
