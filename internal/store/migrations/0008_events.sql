-- Live updates: docs/design/08-dashboard-and-api.md#live-updates.

-- A small notice that something changed — a unit's verdict, a task, a group, a pull request's facts — naming what, so
-- that a client fetches it again. Notices are numbered in the order their changes commit, so that a stream that has
-- seen one has seen every one before it, and resumes after the last it saw. They are kept for an hour.
CREATE TABLE event_notice (
    id      bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind    text        NOT NULL CHECK (kind IN ('verdict', 'task', 'group', 'pr')),
    subject text        NOT NULL,
    at      timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX event_notice_at ON event_notice (at);

-- notice(kind, column) writes a notice of kind, naming the changed row by one of its columns. The triggers that call
-- it are deferred, so it runs as the change's transaction commits; its lock is held until the commit is visible, so
-- the next transaction to write notices numbers them after this one's, and a transaction that rolls back writes none.
-- It assumes no other deferred trigger takes locks: the lock is then the last a transaction waits for, and cannot be
-- part of a deadlock.
CREATE FUNCTION notice() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    changed jsonb;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('park.event_notice'));
    IF TG_OP = 'DELETE' THEN
        changed := to_jsonb(OLD);
    ELSE
        changed := to_jsonb(NEW);
    END IF;
    INSERT INTO event_notice (kind, subject) VALUES (TG_ARGV[0], changed ->> TG_ARGV[1]);
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER verdict_notice AFTER INSERT ON verdict DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION notice('verdict', 'unit');

CREATE CONSTRAINT TRIGGER task_created_notice AFTER INSERT ON task DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION notice('task', 'id');
CREATE CONSTRAINT TRIGGER task_notice AFTER UPDATE ON task DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
    WHEN ((OLD.state, OLD.note, OLD.waiting, OLD.cancel_marked, OLD.attempt) IS DISTINCT FROM
          (NEW.state, NEW.note, NEW.waiting, NEW.cancel_marked, NEW.attempt))
    EXECUTE FUNCTION notice('task', 'id');

CREATE CONSTRAINT TRIGGER membership_notice AFTER INSERT OR UPDATE OR DELETE ON membership DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION notice('group', 'group_id');
CREATE CONSTRAINT TRIGGER group_notice AFTER UPDATE ON pr_group DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION notice('group', 'id');

CREATE CONSTRAINT TRIGGER pr_created_notice AFTER INSERT ON pr_fact DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION notice('pr', 'id');
CREATE CONSTRAINT TRIGGER pr_notice AFTER UPDATE ON pr_fact DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
    WHEN (OLD.seq IS DISTINCT FROM NEW.seq OR OLD.stale IS DISTINCT FROM NEW.stale)
    EXECUTE FUNCTION notice('pr', 'id');
