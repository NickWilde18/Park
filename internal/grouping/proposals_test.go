package grouping

import (
	"slices"
	"strings"
	"testing"

	"github.com/NickWilde18/Park/internal/facts"
)

// A proposal is refused, with a code the bridge can act on, for an anchor that is no ID, no groups, a group of fewer
// than two pull requests — counting a pull request named twice once — a pull request in two groups, a repository
// Park does not watch, a link as evidence, and a name too long.
func TestProposalChecks(t *testing.T) {
	e := newEnv(t)
	bridge, err := e.s.CreateBridge(e.ctx, e.actor, "Tracker", "")
	must(t, err)
	pair := []PRRef{{Repo: "api", Number: 10}, {Repo: "web", Number: 11}}
	for _, c := range []struct {
		name string
		want string
		p    Proposal
	}{
		{name: "no anchor", want: "invalid_anchor", p: Proposal{Anchor: " ", Groups: []ProposedGroup{{PRs: pair}}}},
		{name: "no groups", want: "no_groups", p: Proposal{Anchor: "t"}},
		{name: "one pull request twice", want: "too_few_pull_requests",
			p: Proposal{Anchor: "t", Groups: []ProposedGroup{{PRs: []PRRef{{Repo: "api", Number: 10}, {Repo: "acme/api", Number: 10}}}}}},
		{name: "in two groups", want: "duplicate_pull_request", p: Proposal{Anchor: "t", Groups: []ProposedGroup{{PRs: pair},
			{PRs: []PRRef{{Repo: "web", Number: 11}, {Repo: "mobile", Number: 12}}}}}},
		{name: "unwatched repository", want: "unknown_repo",
			p: Proposal{Anchor: "t", Groups: []ProposedGroup{{PRs: []PRRef{{Repo: "api", Number: 10}, {Repo: "acme/secret", Number: 1}}}}}},
		{name: "a link as evidence", want: "invalid_evidence",
			p: Proposal{Anchor: "t", Evidence: []string{"https://tracker.example.test/t/1"}, Groups: []ProposedGroup{{PRs: pair}}}},
		{name: "a long name", want: "invalid_name",
			p: Proposal{Anchor: "t", Groups: []ProposedGroup{{Name: strings.Repeat("x", 201), PRs: pair}}}},
	} {
		if _, err := e.s.Propose(e.ctx, bridge.ID, "tok", c.p); code(err) != c.want {
			t.Errorf("%s: got %v, want %s", c.name, err, c.want)
		}
	}
	var n int
	must(t, e.pool.QueryRow(e.ctx, "SELECT count(*) FROM proposal").Scan(&n))
	if n != 0 {
		t.Fatalf("%d proposals stored from refused posts", n)
	}
}

// A proposal from a thread that announces two features, one of them over pull requests a bridge's assertion already
// grouped. Proposing changes no group, and shows only the groups with two or more open pull requests; a pull request
// Park has not collected yet is refreshed. Reposting the same groups, in another order and under a new title, keeps
// the proposal; dismissed, it stays dismissed when they are posted again, and other groups open a new proposal.
// Accepted as proposed, it groups the pull requests by members' placements, which take them from the assertion's
// group, named as proposed or after the oldest pull request; it is then closed, and retracting it keeps the groups.
func TestProposalLifecycle(t *testing.T) {
	e := newEnv(t)
	e.pr(t, "api", 10, "exports-api", "main", nil)
	e.pr(t, "web", 11, "exports-web", "main", nil)
	e.pr(t, "mobile", 12, "exports-mobile", "main", nil)
	e.pr(t, "api", 20, "retry-api", "main", nil)
	e.pr(t, "web", 21, "retry-web", "main", nil)
	e.pr(t, "api", 30, "old-api", "main", func(p *facts.PR) { p.State = facts.StateMerged })
	e.pr(t, "web", 31, "old-web", "main", func(p *facts.PR) { p.State = facts.StateMerged })
	bridge, err := e.s.CreateBridge(e.ctx, e.actor, "Tracker", "https://tracker.example.test/t/{id}")
	must(t, err)
	if _, err := e.s.Assert(e.ctx, bridge.ID, "tok", Assertion{Anchor: "thread-0", Title: "Thread",
		PRs: []PRRef{{Repo: "api", Number: 10}, {Repo: "web", Number: 11}}}); err != nil {
		t.Fatal(err)
	}
	thread, _, _ := e.group(t, "api#10")
	if thread == 0 {
		t.Fatal("the assertion made no group")
	}

	exports := ProposedGroup{Name: "Exports", PRs: []PRRef{{Repo: "api", Number: 10}, {Repo: "web", Number: 11}, {Repo: "mobile", Number: 12}}}
	retry := ProposedGroup{PRs: []PRRef{{Repo: "api", Number: 20}, {Repo: "web", Number: 21}, {Repo: "api", Number: 40}}}
	old := ProposedGroup{Name: "Old", PRs: []PRRef{{Repo: "api", Number: 30}, {Repo: "web", Number: 31}}}
	first, err := e.s.Propose(e.ctx, bridge.ID, "tok", Proposal{Anchor: "thread-1", Title: "Two features",
		Groups: []ProposedGroup{exports, retry, old}})
	if err != nil || first.State != "open" {
		t.Fatalf("propose: %+v, %v", first, err)
	}
	if g, _, _ := e.group(t, "api#10"); g != thread {
		t.Fatalf("proposing moved api#10 to %d", g)
	}
	var queued int
	must(t, e.pool.QueryRow(e.ctx, "SELECT count(*) FROM work_item WHERE kind = $1 AND key = $2", facts.KindRefreshPR,
		itoa(int(e.repos["api"]))+"/40").Scan(&queued))
	if queued != 1 {
		t.Fatalf("api#40 is not queued for a refresh: %d", queued)
	}
	views, err := e.s.Proposals(e.ctx)
	must(t, err)
	want := [][]int64{{e.prs["api#10"], e.prs["web#11"], e.prs["mobile#12"]}, {e.prs["api#20"], e.prs["web#21"]}}
	if len(views) != 1 || views[0].ID != first.ID || views[0].Title != "Two features" || views[0].Bridge != "Tracker" ||
		views[0].Anchor != "thread-1" || len(views[0].Groups) != 2 || views[0].Groups[0].Name != "Exports" || views[0].Groups[1].Name != "" ||
		!slices.Equal(views[0].Groups[0].PRs, want[0]) || !slices.Equal(views[0].Groups[1].PRs, want[1]) {
		t.Fatalf("listed %+v", views)
	}

	again, err := e.s.Propose(e.ctx, bridge.ID, "tok", Proposal{Anchor: "thread-1", Title: "Two features, again",
		Groups: []ProposedGroup{old, retry, {Name: "Exports", PRs: []PRRef{{Repo: "mobile", Number: 12}, {Repo: "api", Number: 10}, {Repo: "web", Number: 11}}}}})
	if err != nil || again.ID != first.ID || again.State != "open" {
		t.Fatalf("the same groups again: %+v, %v", again, err)
	}
	if views, _ := e.s.Proposals(e.ctx); len(views) != 1 || views[0].Title != "Two features, again" {
		t.Fatalf("after the repost: %+v", views)
	}
	must(t, e.s.DismissProposal(e.ctx, e.actor, first.ID))
	if views, _ := e.s.Proposals(e.ctx); len(views) != 0 {
		t.Fatalf("a dismissed proposal is listed: %+v", views)
	}
	if again, err := e.s.Propose(e.ctx, bridge.ID, "tok", Proposal{Anchor: "thread-1", Groups: []ProposedGroup{exports, retry, old}}); err != nil ||
		again.ID != first.ID || again.State != "dismissed" {
		t.Fatalf("a dismissed proposal posted again: %+v, %v", again, err)
	}
	if err := e.s.DismissProposal(e.ctx, e.actor, first.ID); code(err) != "not_found" {
		t.Fatalf("dismissing twice: %v", err)
	}

	exports.PRs = exports.PRs[:2]
	second, err := e.s.Propose(e.ctx, bridge.ID, "tok", Proposal{Anchor: "thread-1", Groups: []ProposedGroup{exports, retry}})
	if err != nil || second.ID == first.ID || second.State != "open" {
		t.Fatalf("other groups: %+v, %v", second, err)
	}
	var live int
	must(t, e.pool.QueryRow(e.ctx, "SELECT count(*) FROM proposal WHERE anchor = 'thread-1' AND retracted_at IS NULL").Scan(&live))
	if live != 1 {
		t.Fatalf("%d live proposals under one anchor", live)
	}

	made, err := e.s.AcceptProposal(e.ctx, e.actor, second.ID, nil)
	if err != nil || len(made) != 2 {
		t.Fatalf("accept: %v, %v", made, err)
	}
	if g, name, source := e.group(t, "api#10"); g != made[0] || name != "Exports" || source != "manual" {
		t.Fatalf("api#10 in %d %q %q", g, name, source)
	}
	if g, _, _ := e.group(t, "web#11"); g != made[0] {
		t.Fatalf("web#11 in %d", g)
	}
	if g, _, _ := e.group(t, "mobile#12"); g != 0 {
		t.Fatalf("mobile#12, no longer proposed, is in %d", g)
	}
	if g, name, _ := e.group(t, "api#20"); g != made[1] || name != "api change retry-api" {
		t.Fatalf("api#20 in %d %q", g, name)
	}
	var placed string
	must(t, e.pool.QueryRow(e.ctx, `SELECT a.source FROM membership m JOIN assertion a ON a.id = m.assertion_id WHERE m.pr_id = $1`,
		e.prs["web#21"]).Scan(&placed))
	if placed != "manual" {
		t.Fatalf("web#21 placed by %q", placed)
	}
	if _, err := e.s.AcceptProposal(e.ctx, e.actor, second.ID, nil); code(err) != "not_found" {
		t.Fatalf("accepting twice: %v", err)
	}
	if views, _ := e.s.Proposals(e.ctx); len(views) != 0 {
		t.Fatalf("an accepted proposal is listed: %+v", views)
	}
	must(t, e.s.RetractProposal(e.ctx, bridge.ID, "thread-1"))
	if g, _, _ := e.group(t, "api#10"); g != made[0] {
		t.Fatalf("retracting the proposal moved api#10 to %d", g)
	}
	if err := e.s.RetractProposal(e.ctx, bridge.ID, "thread-1"); code(err) != "not_found" {
		t.Fatalf("retracting twice: %v", err)
	}
}

// A member edits a proposal before accepting it: the groups are made as edited, under the names given. A name is
// required, a pull request is in one group at most, and a group left with fewer than two open pull requests is
// skipped; when none is left, the proposal is refused and stays open.
func TestAcceptEditedProposal(t *testing.T) {
	e := newEnv(t)
	e.pr(t, "api", 10, "a", "main", nil)
	e.pr(t, "web", 11, "b", "main", nil)
	e.pr(t, "mobile", 12, "c", "main", nil)
	e.pr(t, "api", 30, "d", "main", func(p *facts.PR) { p.State = facts.StateMerged })
	bridge, err := e.s.CreateBridge(e.ctx, e.actor, "Tracker", "")
	must(t, err)
	p, err := e.s.Propose(e.ctx, bridge.ID, "tok", Proposal{Anchor: "t", Groups: []ProposedGroup{{Name: "All",
		PRs: []PRRef{{Repo: "api", Number: 10}, {Repo: "web", Number: 11}, {Repo: "mobile", Number: 12}}}}})
	must(t, err)
	if _, err := e.s.AcceptProposal(e.ctx, e.actor, p.ID, []EditedGroup{{Name: " ", PRs: []int64{e.prs["api#10"], e.prs["web#11"]}}}); code(err) != "invalid_name" {
		t.Fatalf("no name: %v", err)
	}
	if _, err := e.s.AcceptProposal(e.ctx, e.actor, p.ID, []EditedGroup{{Name: "A", PRs: []int64{e.prs["api#10"], e.prs["web#11"]}},
		{Name: "B", PRs: []int64{e.prs["web#11"], e.prs["mobile#12"]}}}); code(err) != "duplicate_pull_request" {
		t.Fatalf("a pull request in two groups: %v", err)
	}
	if _, err := e.s.AcceptProposal(e.ctx, e.actor, p.ID, []EditedGroup{{Name: "A", PRs: []int64{e.prs["api#10"], e.prs["api#30"]}}}); code(err) != "nothing_to_group" {
		t.Fatalf("nothing left to group: %v", err)
	}
	if g, _, _ := e.group(t, "api#10"); g != 0 {
		t.Fatalf("a refused acceptance grouped api#10 in %d", g)
	}
	made, err := e.s.AcceptProposal(e.ctx, e.actor, p.ID, []EditedGroup{{Name: "Web and mobile", PRs: []int64{e.prs["web#11"], e.prs["mobile#12"]}},
		{Name: "Alone", PRs: []int64{e.prs["api#10"], e.prs["api#30"]}}})
	if err != nil || len(made) != 1 {
		t.Fatalf("accept edited: %v, %v", made, err)
	}
	if g, name, _ := e.group(t, "mobile#12"); g != made[0] || name != "Web and mobile" {
		t.Fatalf("mobile#12 in %d %q", g, name)
	}
	if g, _, _ := e.group(t, "api#10"); g != 0 {
		t.Fatalf("api#10, in a skipped group, is in %d", g)
	}
}
