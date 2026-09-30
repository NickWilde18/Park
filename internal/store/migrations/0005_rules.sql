-- Rule sets: docs/design/05-rules.md#editing-and-publishing.

-- A published rule set. Versions are numbered 1, 2, 3, … and never change (R7): every verdict names the version that
-- produced it. The default rule set is published by no one, when Park first starts.
CREATE TABLE rule_version (
    version      integer     PRIMARY KEY CHECK (version > 0),
    rules        jsonb       NOT NULL,
    based_on     integer     REFERENCES rule_version (version),
    published_by bigint      REFERENCES member (id),
    published_at timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION rule_version_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'a published rule set version never changes';
END
$$;

CREATE TRIGGER rule_version_immutable BEFORE UPDATE OR DELETE ON rule_version
    FOR EACH ROW EXECUTE FUNCTION rule_version_immutable();

-- A draft of the next version, started from the version published then; none, before the first. A published draft is
-- kept, and names the version it became.
CREATE TABLE rule_draft (
    id                bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    base_version      integer     REFERENCES rule_version (version),
    rules             jsonb       NOT NULL,
    created_by        bigint      NOT NULL REFERENCES member (id),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    published_version integer     REFERENCES rule_version (version)
);
