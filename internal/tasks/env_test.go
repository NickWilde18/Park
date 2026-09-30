package tasks

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/store/storetest"
	"github.com/NickWilde18/Park/internal/verdicts"
)

// Accounts on the test forge, and the members linked to them: Carol has no member.
const (
	anaAccount   int64 = 1
	benAccount   int64 = 2
	carolAccount int64 = 3
)

type env struct {
	tasks    *Service
	verdicts *verdicts.Service
	groups   *grouping.Service
	pool     *pgxpool.Pool
	ctx      context.Context
	members  map[string]int64
	clock    time.Time
	forge    int64
	repo     int64
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// newEnv sets up a forge with one repository, accounts for Ana, Ben, and Carol, and members Ana and Ben. Ana's
// automation covers every action; Ben's, only review and fix_checks.
func newEnv(t *testing.T) *env {
	t.Helper()
	pool := storetest.MigratedPool(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &env{pool: pool, ctx: context.Background(), members: map[string]int64{}, clock: t0}
	ruleSets := rules.New(pool, log)
	_, err := ruleSets.Bootstrap(e.ctx)
	must(t, err)
	e.groups = grouping.New(pool, log)
	e.verdicts = verdicts.New(pool, log, ruleSets, e.groups)
	e.tasks = New(pool, log, e.groups)
	e.tasks.now = func() time.Time { return e.clock }
	e.verdicts.Follow(e.tasks.VerdictFollower())
	var inst int64
	must(t, pool.QueryRow(e.ctx, "INSERT INTO forge (kind, name, base_address) VALUES ('github', 'GitHub', 'https://github.com') RETURNING id").Scan(&e.forge))
	must(t, pool.QueryRow(e.ctx, "INSERT INTO installation (forge_id, name, credential_kind, credential) VALUES ($1, 'acme', 'token', '') RETURNING id", e.forge).Scan(&inst))
	must(t, pool.QueryRow(e.ctx, `INSERT INTO repo (forge_id, forge_repo_id, installation_id, owner, name, default_branch)
		VALUES ($1, 101, $2, 'acme', 'api', 'main') RETURNING id`, e.forge, inst).Scan(&e.repo))
	for name, id := range map[string]int64{"ana": anaAccount, "ben": benAccount, "carol": carolAccount} {
		_, err := pool.Exec(e.ctx, "INSERT INTO account (forge_id, account_id, login) VALUES ($1, $2, $3)", e.forge, id, name)
		must(t, err)
	}
	for name, id := range map[string]int64{"ana": anaAccount, "ben": benAccount} {
		var m int64
		must(t, pool.QueryRow(e.ctx, "INSERT INTO member (display_name) VALUES ($1) RETURNING id", name).Scan(&m))
		_, err := pool.Exec(e.ctx, "INSERT INTO member_identity (member_id, forge_id, subject) VALUES ($1, $2, $3)", m, e.forge, id)
		must(t, err)
		e.members[name] = m
	}
	_, err = e.tasks.SetAutomation(e.ctx, e.members["ana"], Automation{Actions: []string{"resolve_conflicts", "fix_checks", "address_comments",
		"request_review", "review"}})
	must(t, err)
	_, err = e.tasks.SetAutomation(e.ctx, e.members["ben"], Automation{Actions: []string{"review", "fix_checks"}})
	must(t, err)
	return e
}

// pull is a pull request's facts, at their simplest: open, by an author, with a passing required check.
func pull(author int64) facts.PR {
	return facts.PR{Title: "A change", State: facts.StateOpen, Author: author, HeadSHA: "h1", HeadPushedAt: t0.Add(-1000 * time.Hour),
		BaseBranch: "main", Mergeable: facts.MergeableClean, RequiredApproval: 1, AuthorCanDismiss: true, MergeBlockers: []string{},
		Checks: []facts.Check{{Name: "build", State: facts.CheckPassing, Required: true}}}
}

// store stores a pull request's facts, fetched now, one sequence number on.
func (e *env) store(t *testing.T, number int, pr facts.PR) int64 {
	t.Helper()
	pr.Number = number
	state, err := json.Marshal(pr)
	must(t, err)
	var id int64
	must(t, e.pool.QueryRow(e.ctx, `INSERT INTO pr_fact (repo_id, number, state, seq, open, head_sha, head_branch, head_repo_id,
			base_branch, forge_updated_at, fetched_at, refreshed_at)
		VALUES ($1, $2, $3, 1, $4, $5, $6, 101, 'main', $7, $7, $7)
		ON CONFLICT (repo_id, number) DO UPDATE SET state = EXCLUDED.state, seq = pr_fact.seq + 1, open = EXCLUDED.open,
			head_sha = EXCLUDED.head_sha, fetched_at = EXCLUDED.fetched_at, refreshed_at = EXCLUDED.refreshed_at, stale = false
		RETURNING id`, e.repo, number, state, pr.Open(), pr.HeadSHA, "feat/"+strconv.Itoa(number), e.clock).Scan(&id))
	return id
}

func prUnit(id int64) string { return "pr:" + strconv.FormatInt(id, 10) }

func (e *env) evaluate(t *testing.T, unit string) {
	t.Helper()
	must(t, e.verdicts.Evaluate(e.ctx, unit))
}

// taskOf is a member's one task on a unit in a state; it fails the test unless there is exactly one.
func (e *env) taskOf(t *testing.T, member, unit, state string) Task {
	t.Helper()
	rows, err := e.pool.Query(e.ctx, "SELECT id FROM task WHERE member_id = $1 AND unit = $2 AND state = $3", e.members[member], unit, state)
	must(t, err)
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	must(t, err)
	if len(ids) != 1 {
		t.Fatalf("%s has %d %s tasks on %s", member, len(ids), state, unit)
	}
	task, err := e.tasks.Task(e.ctx, ids[0])
	must(t, err)
	return task
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(e.ctx, sql, args...).Scan(&n))
	return n
}

func (e *env) claim(t *testing.T, member, token string, actions ...string) *Task {
	t.Helper()
	task, err := e.tasks.Claim(e.ctx, e.members[member], token, ClaimRequest{Executor: member + "'s laptop", Actions: actions})
	must(t, err)
	return task
}

func openItems(task Task) map[string]Item {
	out := map[string]Item{}
	for _, it := range task.open() {
		out[it.Action] = it
	}
	return out
}

func thread(id string, comments ...facts.Comment) facts.Thread {
	return facts.Thread{ID: id, Comments: comments}
}

func codeOf(err error) string {
	if f, ok := fail.As(err); ok {
		return f.Code
	}
	return ""
}
