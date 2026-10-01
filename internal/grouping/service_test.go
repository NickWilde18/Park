package grouping

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/store/storetest"
)

type env struct {
	s     *Service
	pool  *pgxpool.Pool
	ctx   context.Context
	repos map[string]int64
	prs   map[string]int64
	actor int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := storetest.MigratedPool(t)
	e := &env{s: New(pool, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))), pool: pool, ctx: context.Background(),
		repos: map[string]int64{}, prs: map[string]int64{}}
	var forge, inst int64
	must(t, pool.QueryRow(e.ctx, "INSERT INTO forge (kind, name, base_address) VALUES ('github', 'GitHub', 'https://github.com') RETURNING id").Scan(&forge))
	must(t, pool.QueryRow(e.ctx, "INSERT INTO installation (forge_id, name, credential_kind, credential) VALUES ($1, 'acme', 'token', '') RETURNING id", forge).Scan(&inst))
	for i, name := range []string{"api", "web", "mobile"} {
		var id int64
		must(t, pool.QueryRow(e.ctx, `INSERT INTO repo (forge_id, forge_repo_id, installation_id, owner, name, default_branch, protection)
			VALUES ($1, $2, $3, 'acme', $4, 'main', '{}') RETURNING id`, forge, 101+i, inst, name).Scan(&id))
		e.repos[name] = id
	}
	must(t, pool.QueryRow(e.ctx, "INSERT INTO member (display_name, admin) VALUES ('Ana', true) RETURNING id").Scan(&e.actor))
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var forgeRepo = map[string]int64{"api": 101, "web": 102, "mobile": 103}

// pr stores a pull request's facts, open unless mutate says otherwise, as "repo#number".
func (e *env) pr(t *testing.T, repo string, number int, head, base string, mutate func(*facts.PR)) int64 {
	t.Helper()
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC).Add(time.Duration(len(e.prs)) * time.Minute)
	p := facts.PR{Number: number, Title: repo + " change " + head, State: facts.StateOpen, Author: 501, HeadBranch: head,
		BaseBranch: base, HeadRepo: forgeRepo[repo], CreatedAt: at, UpdatedAt: at, References: []facts.Ref{}, ClosesIssues: []facts.Ref{}}
	if mutate != nil {
		mutate(&p)
	}
	state, _ := json.Marshal(p)
	var id int64
	must(t, e.pool.QueryRow(e.ctx, `INSERT INTO pr_fact (repo_id, number, state, open, head_sha, head_branch, head_repo_id, base_branch,
			forge_updated_at, fetched_at, refreshed_at)
		VALUES ($1, $2, $3, $4, '', $5, $6, $7, now(), now(), now())
		ON CONFLICT (repo_id, number) DO UPDATE SET state = EXCLUDED.state, open = EXCLUDED.open, head_branch = EXCLUDED.head_branch,
			base_branch = EXCLUDED.base_branch
		RETURNING id`, e.repos[repo], number, state, p.Open(), p.HeadBranch, p.HeadRepo, p.BaseBranch).Scan(&id))
	e.prs[repo+"#"+itoa(number)] = id
	return id
}

func itoa(n int) string { return strconv.Itoa(n) }

func (e *env) resolve(t *testing.T) {
	t.Helper()
	must(t, e.s.Resolve(e.ctx))
}

// group returns the group a pull request is in, with its name and name source; 0 when it stands alone.
func (e *env) group(t *testing.T, key string) (int64, string, string) {
	t.Helper()
	var g int64
	var name, source string
	err := e.pool.QueryRow(e.ctx, `SELECT g.id, g.name, g.name_source FROM membership m JOIN pr_group g ON g.id = m.group_id
		WHERE m.pr_id = $1`, e.prs[key]).Scan(&g, &name, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ""
	}
	must(t, err)
	return g, name, source
}

func code(err error) string {
	if f, ok := fail.As(err); ok {
		return f.Code
	}
	return ""
}

// docs/design/03-grouping.md's worked example, through the database: a branch pair makes a group named after the
// branch; a bridge's thread over both takes it over and names it; a third pull request with the branch joins; a
// member takes it out, and it stands alone with no suggestion to come back. Every membership records its assertion
// (G12).
func TestWorkedExampleStored(t *testing.T) {
	e := newEnv(t)
	e.pr(t, "api", 12, "feat/schedule", "main", nil)
	e.pr(t, "web", 7, "feat/schedule", "main", nil)
	e.resolve(t)
	g, name, source := e.group(t, "api#12")
	if g2, _, _ := e.group(t, "web#7"); g == 0 || g2 != g || name != "feat/schedule" || source != "branch" {
		t.Fatalf("group %d %q %q, web#7 in %d", g, name, source, g2)
	}
	bridge, err := e.s.CreateBridge(e.ctx, e.actor, "Tracker", "https://tracker.example.test/t/{id}")
	must(t, err)
	res, err := e.s.Assert(e.ctx, bridge.ID, "tok1", Assertion{Anchor: "thread-1", Title: "Scheduled tasks",
		PRs: []PRRef{{Repo: "acme/api", Number: 12}, {Repo: "web", Number: 7}}, Evidence: []string{"msg-9"}})
	if err != nil || !res.Applied {
		t.Fatalf("assert %+v, %v", res, err)
	}
	if again, name, source := e.group(t, "api#12"); again != g || name != "Scheduled tasks" || source != "external" {
		t.Fatalf("after the thread: %d %q %q", again, name, source)
	}
	e.pr(t, "mobile", 4, "feat/schedule", "main", nil)
	e.resolve(t)
	if joined, _, _ := e.group(t, "mobile#4"); joined != g {
		t.Fatalf("mobile#4 in %d, want %d", joined, g)
	}
	var assertion int64
	must(t, e.pool.QueryRow(e.ctx, "SELECT assertion_id FROM membership WHERE pr_id = $1", e.prs["mobile#4"]).Scan(&assertion))
	var kind string
	must(t, e.pool.QueryRow(e.ctx, "SELECT kind FROM assertion WHERE id = $1", assertion).Scan(&kind))
	if kind != KindBranch {
		t.Fatalf("mobile#4 placed by a %q assertion", kind)
	}
	must(t, e.s.TakeOut(e.ctx, e.actor, e.prs["mobile#4"], g))
	if out, _, _ := e.group(t, "mobile#4"); out != 0 {
		t.Fatalf("mobile#4 is still in %d", out)
	}
	if sugs, _ := e.s.Suggestions(e.ctx); len(sugs) != 0 {
		t.Fatalf("a suggestion reopened after an exclusion: %+v", sugs)
	}
	view, err := e.s.Group(e.ctx, g)
	if err != nil || len(view.Members) != 2 || view.Members[0].Source != "external" || view.Members[0].Reason != "Tracker" {
		t.Fatalf("view %+v, %v", view, err)
	}
}

// A forge's stack is one group (D63), through the database. Before the forge records it, three stacked pull
// requests are a chain and no more: the root is grouped with another repository's pull request it references both
// ways, and the other two stand alone. Once the forge records the three in one stack, they are one group, named
// after the oldest of them with the stack as the name's source, each placed for being in the same stack. The root
// leaves the reference group, whose other pull request stays where it was: nothing is unioned. A pull request taken
// out of the stack stands alone again.
func TestStackGroupStored(t *testing.T) {
	e := newEnv(t)
	made := func(minute int, stacked bool, refs ...facts.Ref) func(*facts.PR) {
		return func(p *facts.PR) {
			p.CreatedAt = time.Date(2026, 9, 1, 9, minute, 0, 0, time.UTC)
			p.References = append([]facts.Ref{}, refs...)
			if stacked {
				stack := 372
				p.Stack = &stack
			}
		}
	}
	toWeb, toAPI := facts.Ref{Repo: e.repos["web"], Number: 491}, facts.Ref{Repo: e.repos["api"], Number: 366}
	e.pr(t, "web", 491, "config/backend", "main", made(3, false, toAPI))
	place := func(stacked ...bool) {
		t.Helper()
		e.pr(t, "api", 366, "config/1", "main", made(0, stacked[0], toWeb))
		e.pr(t, "api", 367, "config/2", "config/1", made(1, stacked[1]))
		e.pr(t, "api", 368, "config/3", "config/2", made(2, stacked[2]))
		e.resolve(t)
	}
	place(false, false, false)
	refsGroup, _, _ := e.group(t, "api#366")
	if g, _, _ := e.group(t, "web#491"); refsGroup == 0 || g != refsGroup {
		t.Fatalf("the root and the pull request it references: groups %d and %d", refsGroup, g)
	}
	for _, k := range []string{"api#367", "api#368"} {
		if g, _, _ := e.group(t, k); g != 0 {
			t.Fatalf("%s is in group %d before the forge records a stack", k, g)
		}
	}

	place(true, true, true)
	stackGroup, name, source := e.group(t, "api#366")
	if stackGroup == 0 || stackGroup == refsGroup || name != "api change config/1" || source != "stack" {
		t.Fatalf("the stack's group: %d %q from %q; the reference group was %d", stackGroup, name, source, refsGroup)
	}
	for _, k := range []string{"api#367", "api#368"} {
		if g, _, _ := e.group(t, k); g != stackGroup {
			t.Fatalf("%s is in group %d, not the stack's %d", k, g, stackGroup)
		}
	}
	if g, _, _ := e.group(t, "web#491"); g != refsGroup {
		t.Fatalf("the reference group's other pull request moved to group %d", g)
	}
	view, err := e.s.Group(e.ctx, stackGroup)
	if err != nil || len(view.Members) != 3 {
		t.Fatalf("view %+v, %v", view, err)
	}
	for _, m := range view.Members {
		if m.Source != "structural" || m.Reason != "same stack" {
			t.Fatalf("member %+v", m)
		}
	}

	place(true, true, false)
	if g, _, _ := e.group(t, "api#368"); g != 0 {
		t.Fatalf("a pull request out of the stack is still in group %d", g)
	}
	if g, _, _ := e.group(t, "api#367"); g != stackGroup {
		t.Fatalf("the stack's group lost a pull request still in it: %d", g)
	}
}

// Bridges: a repository that is not watched is refused like an unknown one; a large assertion is recorded but not
// applied; reposting under an anchor keeps one live assertion, first seen when the anchor was; retracting it lets
// the next claim decide.
func TestBridgeAssertions(t *testing.T) {
	e := newEnv(t)
	for i, name := range []string{"api", "web", "mobile"} {
		e.pr(t, name, 10+i, "topic-"+name, "main", nil)
	}
	e.pr(t, "api", 20, "other", "main", nil)
	bridge, _ := e.s.CreateBridge(e.ctx, e.actor, "Tracker", "")
	_, err := e.s.Assert(e.ctx, bridge.ID, "tok", Assertion{Anchor: "t", PRs: []PRRef{{Repo: "acme/api", Number: 10}, {Repo: "acme/secret", Number: 1}}})
	if code(err) != "unknown_repo" {
		t.Fatalf("an unwatched repository: %v", err)
	}
	must(t, e.pool.QueryRow(e.ctx, "SELECT 1").Scan(new(int)))
	if _, err := e.pool.Exec(e.ctx, "UPDATE repo SET watched = false WHERE name = 'mobile'"); err != nil {
		t.Fatal(err)
	}
	_, err = e.s.Assert(e.ctx, bridge.ID, "tok", Assertion{Anchor: "t", PRs: []PRRef{{Repo: "acme/api", Number: 10}, {Repo: "mobile", Number: 12}}})
	if code(err) != "unknown_repo" {
		t.Fatalf("a repository no longer watched: %v", err)
	}
	if _, err := e.pool.Exec(e.ctx, "UPDATE repo SET watched = true WHERE name = 'mobile'"); err != nil {
		t.Fatal(err)
	}
	_, err = e.s.Assert(e.ctx, bridge.ID, "tok", Assertion{Anchor: "t", PRs: []PRRef{{Repo: "api", Number: 10}, {Repo: "web", Number: 11}},
		Evidence: []string{"https://tracker.example.test/t/1"}})
	if code(err) != "invalid_evidence" {
		t.Fatalf("a link as evidence: %v", err)
	}
	big, err := e.s.Assert(e.ctx, bridge.ID, "tok", Assertion{Anchor: "report", PRs: []PRRef{{Repo: "api", Number: 10}, {Repo: "web", Number: 11},
		{Repo: "mobile", Number: 12}, {Repo: "api", Number: 20}}})
	if err != nil || big.Applied {
		t.Fatalf("a large assertion: %+v, %v", big, err)
	}
	if g, _, _ := e.group(t, "api#10"); g != 0 {
		t.Fatal("a large assertion was applied")
	}
	first, err := e.s.Assert(e.ctx, bridge.ID, "tok", Assertion{Anchor: "t", PRs: []PRRef{{Repo: "api", Number: 10}, {Repo: "web", Number: 11}}})
	must(t, err)
	second, err := e.s.Assert(e.ctx, bridge.ID, "tok", Assertion{Anchor: "t", PRs: []PRRef{{Repo: "api", Number: 10}, {Repo: "mobile", Number: 12}}})
	must(t, err)
	var live int
	var seen1, seen2 time.Time
	must(t, e.pool.QueryRow(e.ctx, "SELECT count(*) FROM assertion WHERE anchor = 't' AND retracted_at IS NULL").Scan(&live))
	must(t, e.pool.QueryRow(e.ctx, "SELECT seen_at FROM assertion WHERE id = $1", first.ID).Scan(&seen1))
	must(t, e.pool.QueryRow(e.ctx, "SELECT seen_at FROM assertion WHERE id = $1", second.ID).Scan(&seen2))
	if live != 1 || !seen1.Equal(seen2) {
		t.Fatalf("%d live, seen %v and %v", live, seen1, seen2)
	}
	g, _, _ := e.group(t, "api#10")
	if m, _, _ := e.group(t, "mobile#12"); g == 0 || m != g {
		t.Fatalf("api#10 in %d, mobile#12 in %d", g, m)
	}
	if w, _, _ := e.group(t, "web#11"); w != 0 {
		t.Fatalf("web#11 kept a retracted claim: %d", w)
	}
	must(t, e.s.Retract(e.ctx, bridge.ID, "t"))
	if g, _, _ := e.group(t, "api#10"); g != 0 {
		t.Fatalf("api#10 kept a retracted claim: %d", g)
	}
	if err := e.s.Retract(e.ctx, bridge.ID, "t"); code(err) != "not_found" {
		t.Fatalf("retracting twice: %v", err)
	}
}

// A member's actions: create, rename, move, merge — an alias that resolves to the group it joined — and split.
func TestManualActions(t *testing.T) {
	e := newEnv(t)
	a := e.pr(t, "api", 15, "x", "main", nil)
	b := e.pr(t, "web", 16, "y", "main", nil)
	c := e.pr(t, "mobile", 17, "z", "main", nil)
	g1, err := e.s.Create(e.ctx, e.actor, "Billing", []int64{a, b})
	must(t, err)
	if g, name, source := e.group(t, "api#15"); g != g1 || name != "Billing" || source != "manual" {
		t.Fatalf("create: %d %q %q", g, name, source)
	}
	g2, err := e.s.Create(e.ctx, e.actor, "Invoices", []int64{c})
	must(t, err)
	must(t, e.s.Rename(e.ctx, g2, "Invoice export"))
	must(t, e.s.Merge(e.ctx, e.actor, g1, g2))
	if g, _, _ := e.group(t, "mobile#17"); g != g1 {
		t.Fatalf("after the merge, mobile#17 is in %d", g)
	}
	if view, err := e.s.Group(e.ctx, g2); err != nil || view.ID != g1 || len(view.Members) != 3 {
		t.Fatalf("the absorbed group: %+v, %v", view, err)
	}
	g3, err := e.s.Split(e.ctx, e.actor, g1, "Billing API", []int64{a})
	must(t, err)
	if g, _, _ := e.group(t, "api#15"); g != g3 {
		t.Fatalf("after the split, api#15 is in %d", g)
	}
	must(t, e.s.Move(e.ctx, e.actor, a, g1))
	if g, _, _ := e.group(t, "api#15"); g != g1 {
		t.Fatalf("moving back lifts the exclusion: api#15 is in %d", g)
	}
	if err := e.s.Move(e.ctx, e.actor, a, 99999); code(err) != "not_found" {
		t.Fatalf("an unknown group: %v", err)
	}
	if err := e.s.Merge(e.ctx, e.actor, g1, g1); code(err) != "same_group" {
		t.Fatalf("merging a group into itself: %v", err)
	}
}

// Merging re-points the absorbed group's anchors (G7): a pull request its branch claims later joins the group it was
// merged into, and the absorbed group is not made again.
func TestMergeRepointsAnchors(t *testing.T) {
	e := newEnv(t)
	e.pr(t, "api", 12, "feat/schedule", "main", nil)
	e.pr(t, "web", 7, "feat/schedule", "main", nil)
	e.resolve(t)
	absorbed, _, _ := e.group(t, "api#12")
	calendar := e.pr(t, "mobile", 3, "feat/calendar", "main", nil)
	into, err := e.s.Create(e.ctx, e.actor, "Scheduling", []int64{calendar})
	must(t, err)
	must(t, e.s.Merge(e.ctx, e.actor, into, absorbed))
	e.pr(t, "mobile", 4, "feat/schedule", "main", nil)
	e.resolve(t)
	for _, key := range []string{"api#12", "web#7", "mobile#3", "mobile#4"} {
		if g, _, _ := e.group(t, key); g != into {
			t.Fatalf("%s is in %d, not in %d", key, g, into)
		}
	}
	var active int
	must(t, e.pool.QueryRow(e.ctx, "SELECT count(*) FROM pr_group WHERE state = 'active'").Scan(&active))
	if active != 1 {
		t.Fatalf("%d active groups after the merge", active)
	}
}

// A structural claim that loses opens a suggestion; a dismissed one never reopens; an accepted one becomes a manual
// placement (G10).
func TestSuggestions(t *testing.T) {
	e := newEnv(t)
	e.pr(t, "api", 1, "feat/a", "main", nil)
	e.pr(t, "web", 2, "feat/a", "main", nil)
	e.pr(t, "mobile", 3, "feat/b", "main", func(p *facts.PR) { p.ClosesIssues = []facts.Ref{{Repo: 101, Number: 50}} })
	e.resolve(t)
	branch, _, _ := e.group(t, "api#1")
	// web#2 also closes the issue mobile#3 closes: the stronger issue claim wins, and the branch claim becomes a
	// suggestion back to the branch's group.
	e.pr(t, "web", 2, "feat/a", "main", func(p *facts.PR) { p.ClosesIssues = []facts.Ref{{Repo: 101, Number: 50}} })
	e.resolve(t)
	issue, _, _ := e.group(t, "web#2")
	if issue == 0 || issue == branch {
		t.Fatalf("web#2 in %d, the branch group is %d", issue, branch)
	}
	sugs, err := e.s.Suggestions(e.ctx)
	if err != nil || len(sugs) != 1 || sugs[0].PR != e.prs["web#2"] || sugs[0].Group != branch {
		t.Fatalf("suggestions %+v, %v", sugs, err)
	}
	must(t, e.s.Dismiss(e.ctx, e.actor, sugs[0].ID))
	e.resolve(t)
	if sugs, _ := e.s.Suggestions(e.ctx); len(sugs) != 0 {
		t.Fatalf("a dismissed suggestion reopened: %+v", sugs)
	}
	var id int64
	must(t, e.pool.QueryRow(e.ctx, "INSERT INTO suggestion (pr_id, group_id, reason, source, key) VALUES ($1, $2, 'test', 'structural', 'k') RETURNING id",
		e.prs["web#2"], branch).Scan(&id))
	must(t, e.s.Accept(e.ctx, e.actor, id))
	if g, _, _ := e.group(t, "web#2"); g != branch {
		t.Fatalf("an accepted suggestion: web#2 in %d, want %d", g, branch)
	}
}

// A group left with no open pull request is archived, and its anchor released; a closed pull request keeps the
// group it had (G2); a later claim under the released anchor starts a new group.
func TestArchive(t *testing.T) {
	e := newEnv(t)
	e.pr(t, "api", 1, "feat/a", "main", nil)
	e.pr(t, "web", 2, "feat/a", "main", nil)
	e.resolve(t)
	g, _, _ := e.group(t, "api#1")
	e.pr(t, "api", 1, "feat/a", "main", func(p *facts.PR) { p.State = facts.StateMerged })
	e.pr(t, "web", 2, "feat/a", "main", func(p *facts.PR) { p.State = facts.StateMerged })
	e.resolve(t)
	var state string
	must(t, e.pool.QueryRow(e.ctx, "SELECT state FROM pr_group WHERE id = $1", g).Scan(&state))
	if kept, _, _ := e.group(t, "api#1"); state != "archived" || kept != g {
		t.Fatalf("state %q, api#1 in %d", state, kept)
	}
	var released bool
	must(t, e.pool.QueryRow(e.ctx, "SELECT group_id IS NULL FROM anchor WHERE key = 'branch:501/feat/a'").Scan(&released))
	if !released {
		t.Fatal("the anchor was not released")
	}
	e.pr(t, "api", 3, "feat/a", "main", nil)
	e.pr(t, "mobile", 4, "feat/a", "main", nil)
	e.resolve(t)
	if fresh, _, _ := e.group(t, "api#3"); fresh == 0 || fresh == g {
		t.Fatalf("a claim under a released anchor went to %d", fresh)
	}
}

// Stacked pull requests are never paired by a structural signal (G9), even when both close the issue a third one
// closes; the chain's root is the one grouped.
func TestNeverWithinAChain(t *testing.T) {
	e := newEnv(t)
	closes := func(p *facts.PR) { p.ClosesIssues = []facts.Ref{{Repo: 101, Number: 7}} }
	e.pr(t, "api", 1, "feat/a", "main", closes)
	e.pr(t, "api", 2, "feat/a2", "feat/a", closes)
	e.pr(t, "web", 3, "feat/w", "main", closes)
	e.resolve(t)
	root, _, _ := e.group(t, "api#1")
	if web, _, _ := e.group(t, "web#3"); root == 0 || web != root {
		t.Fatalf("api#1 in %d, web#3 in %d", root, web)
	}
	if stacked, _, _ := e.group(t, "api#2"); stacked != 0 {
		t.Fatalf("the stacked pull request was grouped with its parent: %d", stacked)
	}
	chains, err := e.s.ChainsOf(e.ctx)
	if err != nil || chains[e.prs["api#2"]].Position != 2 || chains[e.prs["api#1"]].Length != 2 {
		t.Fatalf("chains %+v, %v", chains, err)
	}
}

// Followers learn which pull requests changed group; the facts follower queues a resolution only for facts grouping
// reads.
func TestFollowersAndTriggers(t *testing.T) {
	e := newEnv(t)
	var changed [][]int64
	e.s.Follow(func(_ context.Context, _ pgx.Tx, prs []int64) error {
		changed = append(changed, slices.Clone(prs))
		return nil
	})
	a := e.pr(t, "api", 1, "feat/a", "main", nil)
	b := e.pr(t, "web", 2, "feat/a", "main", nil)
	e.resolve(t)
	e.resolve(t)
	if len(changed) != 1 || !slices.Equal(changed[0], []int64{a, b}) {
		t.Fatalf("changed %v", changed)
	}
	follow := e.s.FactsFollower()
	for _, c := range []struct {
		fields []string
		queued bool
	}{{[]string{"checks"}, false}, {[]string{"checks", "head_branch"}, true}, {[]string{"stack"}, true}} {
		if _, err := e.pool.Exec(e.ctx, "DELETE FROM work_item"); err != nil {
			t.Fatal(err)
		}
		must(t, pgx.BeginFunc(e.ctx, e.pool, func(tx pgx.Tx) error { return follow(e.ctx, tx, a, c.fields) }))
		var n int
		must(t, e.pool.QueryRow(e.ctx, "SELECT count(*) FROM work_item WHERE kind = $1", KindResolve).Scan(&n))
		if (n == 1) != c.queued {
			t.Errorf("fields %v: queued %d", c.fields, n)
		}
	}
}

// Two resolutions at once, as two servers would run them, map an anchor once (G7): the second waits for the first,
// then finds the anchor mapped, and the branch pair is one group.
func TestConcurrentResolutions(t *testing.T) {
	e := newEnv(t)
	e.pr(t, "api", 12, "feat/schedule", "main", nil)
	e.pr(t, "web", 7, "feat/schedule", "main", nil)
	first, err := e.pool.Begin(e.ctx)
	must(t, err)
	defer func() { _ = first.Rollback(e.ctx) }()
	must(t, e.s.resolveTx(e.ctx, first))
	second := make(chan error, 1)
	go func() { second <- e.s.Resolve(e.ctx) }()
	// The first commits only once the second waits on a lock the first holds.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var waiting int
		must(t, e.pool.QueryRow(e.ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting))
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second resolution never waited for the first")
		}
	}
	must(t, first.Commit(e.ctx))
	must(t, <-second)
	var groups, anchors int
	must(t, e.pool.QueryRow(e.ctx, "SELECT count(*) FROM pr_group").Scan(&groups))
	must(t, e.pool.QueryRow(e.ctx, "SELECT count(*) FROM anchor").Scan(&anchors))
	g, _, _ := e.group(t, "api#12")
	if g2, _, _ := e.group(t, "web#7"); groups != 1 || anchors != 1 || g == 0 || g2 != g {
		t.Fatalf("%d groups, %d anchors; api#12 in %d, web#7 in %d", groups, anchors, g, g2)
	}
}
