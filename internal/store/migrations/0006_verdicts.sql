-- Verdicts: docs/design/04-verdicts.md.

-- One conclusion for one unit — "group:<id>", or "pr:<id>" for a pull request in no group — current, or superseded by
-- a newer one. A verdict never changes but for being superseded (V8). Holder and party keys, "<forge>/<account>",
-- find a person's queue and waiting list (V10); seqs are the fact sequence numbers it was computed from, by pull
-- request.
CREATE TABLE verdict (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    unit         text        NOT NULL,
    current      boolean     NOT NULL DEFAULT true,
    side         text        CHECK (side IN ('author', 'reviewer')),
    closed       text        CHECK (closed IN ('merged', 'closed')),
    fallback     boolean     NOT NULL DEFAULT false,
    holders      jsonb       NOT NULL,
    parts        jsonb       NOT NULL,
    reminders    jsonb       NOT NULL,
    inputs       jsonb       NOT NULL,
    seqs         jsonb       NOT NULL,
    holder_keys  text[]      NOT NULL DEFAULT '{}',
    party_keys   text[]      NOT NULL DEFAULT '{}',
    rule_version integer     NOT NULL REFERENCES rule_version (version),
    evaluated_at timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK ((side IS NULL) = (closed IS NOT NULL))
);

CREATE UNIQUE INDEX verdict_current ON verdict (unit) WHERE current;
CREATE INDEX verdict_holders ON verdict USING gin (holder_keys) WHERE current;
CREATE INDEX verdict_parties ON verdict USING gin (party_keys) WHERE current;

CREATE FUNCTION verdict_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.current OR (to_jsonb(NEW) - 'current') IS DISTINCT FROM (to_jsonb(OLD) - 'current') THEN
        RAISE EXCEPTION 'a verdict never changes but for being superseded';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER verdict_immutable BEFORE UPDATE OR DELETE ON verdict
    FOR EACH ROW EXECUTE FUNCTION verdict_immutable();

-- Actions of members that rules can read: so far, marking a pull request as nudged.
CREATE TABLE member_action (
    id        bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    member_id bigint      NOT NULL REFERENCES member (id),
    pr_id     bigint      NOT NULL REFERENCES pr_fact (id),
    kind      text        NOT NULL CHECK (kind IN ('nudged')),
    at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX member_action_pr ON member_action (pr_id, kind, at);
