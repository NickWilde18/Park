package verdicts

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/store/storetest"
)

type env struct {
	s       *Service
	groups  *grouping.Service
	rules   *rules.Service
	pool    *pgxpool.Pool
	ctx     context.Context
	members map[string]int64
	repo    int64
	forge   int64
	clock   time.Time
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := storetest.MigratedPool(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &env{pool: pool, ctx: context.Background(), members: map[string]int64{}, clock: now}
	e.rules = rules.New(pool, log)
	e.groups = grouping.New(pool, log)
	e.s = New(pool, log, e.rules, e.groups)
	e.s.now = func() time.Time { return e.clock }
	e.groups.Follow(e.s.GroupingFollower())
	e.rules.Follow(e.s.RulesFollower())
	_, err := e.rules.Bootstrap(e.ctx)
	must(t, err)
	var inst int64
	must(t, pool.QueryRow(e.ctx, "INSERT INTO forge (kind, name, base_address) VALUES ('github', 'GitHub', 'https://github.com') RETURNING id").Scan(&e.forge))
	must(t, pool.QueryRow(e.ctx, "INSERT INTO installation (forge_id, name, credential_kind, credential) VALUES ($1, 'acme', 'token', '') RETURNING id", e.forge).Scan(&inst))
	must(t, pool.QueryRow(e.ctx, `INSERT INTO repo (forge_id, forge_repo_id, installation_id, owner, name, default_branch)
		VALUES ($1, 101, $2, 'acme', 'api', 'main') RETURNING id`, e.forge, inst).Scan(&e.repo))
	for name, id := range accounts {
		_, err := pool.Exec(e.ctx, "INSERT INTO account (forge_id, account_id, login) VALUES ($1, $2, $3)", e.forge, id, name)
		must(t, err)
	}
	for _, name := range []string{"ana", "ben"} {
		var m int64
		must(t, pool.QueryRow(e.ctx, "INSERT INTO member (display_name, rules) VALUES ($1, true) RETURNING id", name).Scan(&m))
		_, err := pool.Exec(e.ctx, "INSERT INTO member_identity (member_id, forge_id, subject) VALUES ($1, $2, $3)", m, e.forge, accounts[name])
		must(t, err)
		e.members[name] = m
	}
	return e
}

// store stores a pull request's facts as number, open unless they say otherwise, one sequence number on.
func (e *env) store(t *testing.T, number int, pr facts.PR) int64 {
	t.Helper()
	pr.Number = number
	state, err := json.Marshal(pr)
	must(t, err)
	var id int64
	must(t, e.pool.QueryRow(e.ctx, `INSERT INTO pr_fact (repo_id, number, state, seq, open, head_sha, head_branch, head_repo_id,
			base_branch, forge_updated_at, fetched_at, refreshed_at)
		VALUES ($1, $2, $3, 1, $4, $5, $6, 101, $7, now(), now(), now())
		ON CONFLICT (repo_id, number) DO UPDATE SET state = EXCLUDED.state, seq = pr_fact.seq + 1, open = EXCLUDED.open
		RETURNING id`, e.repo, number, state, pr.Open(), pr.HeadSHA, "feat/"+strconv.Itoa(number), pr.BaseBranch).Scan(&id))
	return id
}

func (e *env) pr(t *testing.T, number int, m model) int64 {
	t.Helper()
	pr, _ := m.facts()
	return e.store(t, number, pr)
}

func unitOfPR(id int64) string { return "pr:" + strconv.FormatInt(id, 10) }

func (e *env) current(t *testing.T, unit string) Verdict {
	t.Helper()
	tx, err := e.pool.Begin(e.ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(e.ctx) }()
	v, err := currentVerdict(e.ctx, tx, unit)
	must(t, err)
	if v == nil {
		t.Fatalf("%s has no current verdict", unit)
	}
	return *v
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(e.ctx, sql, args...).Scan(&n))
	return n
}

func holderNames(v Verdict) []string {
	var out []string
	for _, h := range v.Holders {
		out = append(out, h.Login)
	}
	return out
}

// A pull request waiting for Ben's first review has its ball with Ben, and the verdict explains itself: every part's
// path, the inputs, the fact sequence numbers, and the rule set version. Evaluating it again stores nothing new. Once
// Ben requests changes, the ball goes to Ana, and the earlier verdict is superseded.
func TestEvaluate(t *testing.T) {
	e := newEnv(t)
	id := e.pr(t, 12, model{author: "ana", direct: []string{"ben"}})
	unit := unitOfPR(id)
	must(t, e.s.Evaluate(e.ctx, unit))
	v := e.current(t, unit)
	if v.Side != rules.Reviewer || !slices.Equal(holderNames(v), []string{"ben"}) || v.Holders[0].Parts[0].Action != "review" ||
		v.RuleVersion != 1 || v.Seqs[id] != 1 || v.Fallback || len(v.Parts) != 2 || len(v.Inputs) != 2 || len(v.Parts[0].Path) == 0 {
		t.Fatalf("verdict %+v", v)
	}
	if v.Parts[0].Login != "ana" || v.Parts[0].Role != rules.Author || v.Inputs[1].Fields["me.requested"] != true {
		t.Fatalf("parts %+v, inputs %+v", v.Parts, v.Inputs)
	}
	must(t, e.s.Evaluate(e.ctx, unit))
	if n := e.count(t, "SELECT count(*) FROM verdict"); n != 1 {
		t.Fatalf("%d verdicts after an evaluation that changed nothing", n)
	}
	e.pr(t, 12, model{author: "ana", reviews: map[string]modelReview{"ben": {decision: "changes_requested", body: true}}})
	must(t, e.s.Evaluate(e.ctx, unit))
	after := e.current(t, unit)
	if after.Side != rules.Author || !slices.Equal(holderNames(after), []string{"ana"}) || after.Holders[0].Parts[0].Action != "address_comments" ||
		after.Seqs[id] != 2 || after.ID == v.ID {
		t.Fatalf("after Ben's review: %+v", after)
	}
	if n := e.count(t, "SELECT count(*) FROM verdict WHERE current"); n != 1 {
		t.Fatalf("%d current verdicts", n)
	}
}

// A person holds the ball since the first verdict they held it in: a new verdict where they still hold it keeps
// that time.
func TestHolderSince(t *testing.T) {
	e := newEnv(t)
	id := e.pr(t, 12, model{author: "ana", direct: []string{"ben"}})
	unit := unitOfPR(id)
	must(t, e.s.Evaluate(e.ctx, unit))
	first := e.clock
	e.clock = e.clock.Add(3 * time.Hour)
	e.pr(t, 12, model{author: "ana", direct: []string{"ben", "carol"}})
	must(t, e.s.Evaluate(e.ctx, unit))
	v := e.current(t, unit)
	if !slices.Equal(holderNames(v), []string{"ben", "carol"}) || !v.Holders[0].Since.Equal(first) || !v.Holders[1].Since.Equal(e.clock) {
		t.Fatalf("holders %+v", v.Holders)
	}
}

// A verdict is never replaced by one computed from older facts (the fact-sequence guard).
func TestOlderFactsLose(t *testing.T) {
	e := newEnv(t)
	id := e.pr(t, 12, model{author: "ana", direct: []string{"ben"}})
	e.pr(t, 12, model{author: "ana", direct: []string{"ben"}})
	unit := unitOfPR(id)
	must(t, e.s.Evaluate(e.ctx, unit))
	older, _ := model{author: "ana", reviews: map[string]modelReview{"ben": {decision: "changes_requested"}}}.facts()
	state, err := json.Marshal(older)
	must(t, err)
	_, err = e.pool.Exec(e.ctx, "UPDATE pr_fact SET state = $2, seq = 1 WHERE id = $1", id, state)
	must(t, err)
	must(t, e.s.Evaluate(e.ctx, unit))
	if v := e.current(t, unit); v.Side != rules.Reviewer || v.Seqs[id] != 2 {
		t.Fatalf("older facts replaced the verdict: %+v", v)
	}
}

// Once every pull request of a unit is merged, its verdict is closed out, and evaluating it again stores nothing new.
// A unit never evaluated while open gets no verdict.
func TestCloseOut(t *testing.T) {
	e := newEnv(t)
	id := e.pr(t, 12, model{author: "ana", direct: []string{"ben"}})
	unit := unitOfPR(id)
	must(t, e.s.Evaluate(e.ctx, unit))
	merged, _ := model{author: "ana"}.facts()
	merged.State = facts.StateMerged
	e.store(t, 12, merged)
	must(t, e.s.Evaluate(e.ctx, unit))
	must(t, e.s.Evaluate(e.ctx, unit))
	v := e.current(t, unit)
	if v.Closed != "merged" || v.Side != "" || len(v.Holders) != 0 || e.count(t, "SELECT count(*) FROM verdict") != 2 {
		t.Fatalf("closed out: %+v", v)
	}
	closed, _ := model{author: "ana", closed: true}.facts()
	other := e.store(t, 13, closed)
	must(t, e.s.Evaluate(e.ctx, unitOfPR(other)))
	if n := e.count(t, "SELECT count(*) FROM verdict WHERE unit = $1", unitOfPR(other)); n != 0 {
		t.Fatalf("a unit never open got %d verdicts", n)
	}
}

func (e *env) queued(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(e.ctx, "SELECT key FROM work_item WHERE kind = $1 ORDER BY key", KindEvaluate)
	must(t, err)
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	must(t, err)
	_, err = e.pool.Exec(e.ctx, "DELETE FROM work_item")
	must(t, err)
	return keys
}

// A group is one unit: both of its pull requests' people have parts in one verdict, and the ball is placed across
// them. Taking a pull request out of the group queues the unit it left and the one it is now; publishing a rule set
// queues every open unit; a fact change queues its pull request's unit.
func TestGroupsAndFollowers(t *testing.T) {
	e := newEnv(t)
	a := e.pr(t, 12, model{author: "ana", direct: []string{"ben"}})
	b := e.pr(t, 13, model{author: "ana", draft: true})
	g, err := e.groups.Create(e.ctx, e.members["ana"], "Billing", []int64{a, b})
	must(t, err)
	group := "group:" + strconv.FormatInt(g, 10)
	e.queued(t)
	must(t, e.s.Evaluate(e.ctx, group))
	v := e.current(t, group)
	if v.Side != rules.Reviewer || !slices.Equal(holderNames(v), []string{"ben"}) || len(v.Parts) != 3 || v.Inputs[0].Fields["group.size"] != float64(2) {
		t.Fatalf("group verdict %+v", v)
	}
	must(t, e.groups.TakeOut(e.ctx, e.members["ana"], a, g))
	if got := e.queued(t); !slices.Equal(got, []string{group, unitOfPR(a)}) {
		t.Fatalf("taking one out queued %v", got)
	}
	draft, err := e.rules.CreateDraft(e.ctx, e.members["ana"], nil, nil)
	must(t, err)
	_, err = e.rules.Publish(e.ctx, e.members["ana"], draft.ID)
	must(t, err)
	if got := e.queued(t); !slices.Equal(got, []string{group, unitOfPR(a)}) {
		t.Fatalf("publishing queued %v", got)
	}
	must(t, pgx.BeginFunc(e.ctx, e.pool, func(tx pgx.Tx) error { return e.s.FactsFollower()(e.ctx, tx, b, []string{"state"}) }))
	if got := e.queued(t); !slices.Equal(got, []string{group}) {
		t.Fatalf("a fact change queued %v", got)
	}
}

// A member's queue holds the open units whose ball one of their accounts holds; their waiting list, the open units
// they have a part in, where someone else holds it (V10).
func TestQueue(t *testing.T) {
	e := newEnv(t)
	id := e.pr(t, 12, model{author: "ana", direct: []string{"ben"}})
	must(t, e.s.Evaluate(e.ctx, unitOfPR(id)))
	holding, waiting, err := e.s.Queue(e.ctx, e.members["ben"])
	must(t, err)
	if len(holding) != 1 || len(waiting) != 0 || holding[0].Unit != unitOfPR(id) {
		t.Fatalf("Ben: holding %+v, waiting %+v", holding, waiting)
	}
	holding, waiting, err = e.s.Queue(e.ctx, e.members["ana"])
	must(t, err)
	if len(holding) != 0 || len(waiting) != 1 {
		t.Fatalf("Ana: holding %+v, waiting %+v", holding, waiting)
	}
}

// Marking a pull request nudged records the member's action and queues its unit; the next evaluation reads it, for
// the member's accounts, as me.hours_since_nudge.
func TestNudge(t *testing.T) {
	e := newEnv(t)
	id := e.pr(t, 12, model{author: "ana", direct: []string{"ben"}, requested: 30})
	must(t, e.s.MarkNudged(e.ctx, e.members["ana"], id))
	if got := e.queued(t); !slices.Equal(got, []string{unitOfPR(id)}) {
		t.Fatalf("queued %v", got)
	}
	e.clock = e.clock.Add(5 * time.Hour)
	_, err := e.pool.Exec(e.ctx, "UPDATE member_action SET at = $1", now)
	must(t, err)
	must(t, e.s.Evaluate(e.ctx, unitOfPR(id)))
	v := e.current(t, unitOfPR(id))
	if v.Inputs[0].Fields["me.hours_since_nudge"] != float64(5) || v.Inputs[1].Fields["me.hours_since_nudge"] != nil {
		t.Fatalf("inputs %+v", v.Inputs)
	}
	if err := e.s.MarkNudged(e.ctx, e.members["ana"], 999); !isCode(err, "not_found") {
		t.Fatalf("an unknown pull request: %v", err)
	}
}

func isCode(err error, code string) bool {
	f, ok := fail.As(err)
	return ok && f.Code == code
}

// A verdict never changes but for being superseded (V8): the database refuses anything else.
func TestVerdictsNeverChange(t *testing.T) {
	e := newEnv(t)
	id := e.pr(t, 12, model{author: "ana", direct: []string{"ben"}})
	must(t, e.s.Evaluate(e.ctx, unitOfPR(id)))
	for _, sql := range []string{"UPDATE verdict SET side = 'author'", "DELETE FROM verdict"} {
		if _, err := e.pool.Exec(e.ctx, sql); err == nil || !strings.Contains(err.Error(), "never changes") {
			t.Errorf("%s: %v", sql, err)
		}
	}
	_, err := e.pool.Exec(e.ctx, "UPDATE verdict SET current = false")
	must(t, err)
	if _, err := e.pool.Exec(e.ctx, "UPDATE verdict SET current = true"); err == nil {
		t.Error("a superseded verdict became current again")
	}
}

// Two evaluations of one unit at once are serialized by its lock: the second finds the first's verdict, and stores
// nothing more. Both are held on the unit's lock until both wait, then let go together.
func TestConcurrentEvaluations(t *testing.T) {
	e := newEnv(t)
	id := e.pr(t, 12, model{author: "ana", direct: []string{"ben"}})
	unit := unitOfPR(id)
	hold, err := e.pool.Begin(e.ctx)
	must(t, err)
	defer func() { _ = hold.Rollback(e.ctx) }()
	_, err = hold.Exec(e.ctx, "SELECT pg_advisory_xact_lock(hashtext('park.unit:' || $1))", unit)
	must(t, err)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = e.s.Evaluate(e.ctx, unit)
		}()
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		n := e.count(t, "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'")
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d evaluations waiting, not 2", n)
		}
	}
	must(t, hold.Rollback(e.ctx))
	wg.Wait()
	for _, err := range errs {
		must(t, err)
	}
	if n := e.count(t, "SELECT count(*) FROM verdict"); n != 1 {
		t.Fatalf("%d verdicts", n)
	}
}
