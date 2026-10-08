-- What stops collection through an installation: docs/design/02-facts.md#collection-problems. refused while the
-- forge refuses its credential, rate_limited while the forge's rate limit holds, until problem_until; none while
-- collection works. problem_since is when the problem began.
ALTER TABLE installation
    ADD COLUMN problem       text CHECK (problem IN ('refused', 'rate_limited')),
    ADD COLUMN problem_since timestamptz,
    ADD COLUMN problem_until timestamptz,
    ADD CONSTRAINT installation_problem_since CHECK ((problem IS NULL) = (problem_since IS NULL));

-- The board and the installations an admin manages say what stops collection, so they follow an installation as its
-- name or its problem changes (docs/design/08-dashboard-and-api.md#live-updates).
ALTER TABLE event_notice DROP CONSTRAINT event_notice_kind_check;
ALTER TABLE event_notice ADD CONSTRAINT event_notice_kind_check
    CHECK (kind IN ('verdict', 'task', 'group', 'pr', 'proposal', 'bridge', 'installation'));

CREATE CONSTRAINT TRIGGER installation_notice AFTER UPDATE ON installation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
    WHEN ((OLD.name, OLD.problem, OLD.problem_until) IS DISTINCT FROM (NEW.name, NEW.problem, NEW.problem_until))
    EXECUTE FUNCTION notice('installation', 'id');
