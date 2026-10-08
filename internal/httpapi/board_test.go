package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
	"github.com/NickWilde18/Park/internal/tasks"
	"github.com/NickWilde18/Park/internal/verdicts"
	"github.com/NickWilde18/Park/internal/work"
)

// The board for one member, as their linked account changes, over the hub's pull request: its check fails, a review
// is asked of ben, and carol has requested changes in a thread still open. Linked to ana, the author, the ball is
// theirs, with their action, its reason and path, the thread waiting on them, and the task their automation queued.
// Linked to carol instead, the ball is someone else's — ana's — and carol's review is changes requested. Linked to an
// account with no part, the ball is none of theirs, and nothing of a part is shown, but the pull request still says
// what ana holds on it. A read token sees the board too.
func TestBoardOverHTTP(t *testing.T) {
	pool := storetest.MigratedPool(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	box, _ := secret.New(secretKey)
	ids, err := identity.New(pool, box, log, identity.Options{SecretKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	ruleSets := rules.New(pool, log)
	if _, err := ruleSets.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	collected := facts.New(pool, box, log)
	groups := grouping.New(pool, log)
	balls := verdicts.New(pool, log, ruleSets, groups)
	stage := tasks.New(pool, log, groups)
	collected.Follow(groups.FactsFollower())
	collected.Follow(balls.FactsFollower())
	collected.Follow(stage.FactsFollower())
	groups.Follow(balls.GroupingFollower())
	balls.Follow(stage.VerdictFollower())
	ids.Follow(stage.IdentityFollower())
	runner := work.NewRunner(pool, log, work.Options{})
	collected.Register(runner)
	groups.Register(runner)
	balls.Register(runner)
	stage.Register(runner)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h, err := New(Options{Ready: ready, UI: built, Logger: log, Identity: ids, Facts: collected, Grouping: groups, Rules: ruleSets,
		Verdicts: balls, Tasks: stage, Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	a := &harness{h: h, ids: ids, pool: pool, logs: logs}
	admin := a.admin(t)
	var me Me
	admin.call(t, "GET", "/api/v1/me", nil, &me)
	var f Forge
	admin.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "testhub", "name": "Hub", "base_address": "https://hub.example.test"}, &f)
	link := func(account int64) {
		t.Helper()
		var mine Identities
		admin.call(t, "GET", "/api/v1/me/identities", nil, &mine)
		for _, i := range mine.Identities {
			wantStatus(t, admin.call(t, "DELETE", "/api/v1/me/identities/"+strconv.FormatInt(i.Id, 10), nil, nil), http.StatusNoContent, "")
		}
		wantStatus(t, admin.call(t, "POST", "/api/v1/admin/members/"+strconv.FormatInt(me.Member.Id, 10)+"/identities",
			map[string]any{"forge_id": f.Id, "account_id": account}, nil), http.StatusCreated, "")
	}
	link(501)
	wantStatus(t, admin.call(t, "PUT", "/api/v1/me/automation", map[string]any{"actions": []string{"fix_checks"}}, nil), http.StatusOK, "")
	var inst Installation
	admin.call(t, "POST", "/api/v1/admin/installations", map[string]any{"forge_id": f.Id, "name": "acme", "kind": "token", "token": "t"}, &inst)
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/repos", map[string]any{"installation_id": inst.Id, "owner": "acme", "name": "api"}, nil),
		http.StatusCreated, "")

	// Each read decodes into a fresh board: decoding into the last one would keep the optional fields it had.
	read := func(b *browser) Board {
		t.Helper()
		var board Board
		wantStatus(t, b.call(t, "GET", "/api/v1/board", nil, &board), http.StatusOK, "")
		return board
	}
	var board Board
	for deadline := time.Now().Add(10 * time.Second); len(board.Units) == 0 || board.Units[0].Task == nil; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the board never showed the unit and its task: %+v; log:\n%s", board, logs.String())
		}
		board = read(admin)
	}
	u := board.Units[0]
	if len(board.Units) != 1 || u.Unit.Kind != UnitKindPullRequest || u.Name != "Billing export" || u.NameSource != nil ||
		u.Ball != BoardUnitBallYours || u.Side == nil || *u.Side != BoardUnitSideAuthor || u.HeldSince == nil ||
		len(u.Holders) != 1 || u.Holders[0].Login != "ana" || u.Task.State != TaskStateQueued || len(u.PullRequests) != 1 {
		t.Fatalf("the author's board: %+v", board)
	}
	pr := u.PullRequests[0]
	if pr.Summary.Number != 12 || pr.Summary.Checks != PullRequestSummaryChecksFailing || pr.Summary.Author.Login != "ana" ||
		!pr.UpdatedAt.Equal(hubAt) || pr.Chain.Position != 1 || pr.Chain.Length != 1 || pr.ThreadsUnresolved != 1 ||
		pr.Part == nil || pr.Part.Action == nil || *pr.Part.Action != "fix_checks" || pr.Part.Reason != "1 required checks failing" ||
		len(pr.Part.Path) == 0 || pr.ThreadsOnYou == nil || *pr.ThreadsOnYou != 1 || pr.Requested == nil || *pr.Requested {
		t.Fatalf("the author's pull request: %+v; part %+v", pr, pr.Part)
	}
	held := *u.HeldSince

	link(503)
	board = read(admin)
	u = board.Units[0]
	pr = u.PullRequests[0]
	if u.Ball != BoardUnitBallOthers || u.HeldSince == nil || !u.HeldSince.Equal(held) || len(u.Holders) != 1 || u.Holders[0].Login != "ana" ||
		pr.Part == nil || pr.Part.Role != VerdictPartRoleReviewer || pr.YourReview == nil || *pr.YourReview != BoardPullRequestYourReviewChangesRequested ||
		pr.ThreadsOnYou == nil || *pr.ThreadsOnYou != 0 {
		t.Fatalf("a reviewer's board: %+v; part %+v", u, pr.Part)
	}

	link(999)
	board = read(admin)
	u = board.Units[0]
	pr = u.PullRequests[0]
	if u.Ball != BoardUnitBallNone || pr.Part != nil || pr.ThreadsOnYou != nil || pr.YourReview != nil || pr.Requested != nil ||
		pr.ThreadsUnresolved != 1 {
		t.Fatalf("a board with no part: %+v; pull request %+v", u, pr)
	}
	// With no part of their own, the caller still reads what the holder holds on the pull request.
	if len(pr.Holders) != 1 || pr.Holders[0].Account.Login != "ana" || len(pr.Holders[0].Parts) != 1 ||
		pr.Holders[0].Parts[0].Action == nil || *pr.Holders[0].Parts[0].Action != "fix_checks" ||
		pr.Holders[0].Parts[0].Reason != "1 required checks failing" || pr.Holders[0].Parts[0].PullRequest != pr.Summary.Id {
		t.Fatalf("the holders of a pull request with no part of the caller's: %+v", pr.Holders)
	}

	var token NewToken
	admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "reader", "scopes": []string{"read"}}, &token)
	reader := a.browser()
	reader.bearer = token.Token
	if board = read(reader); len(board.Units) != 1 || len(board.Problems) != 0 {
		t.Fatalf("the reader's board: %+v", board)
	}

	// What stops collection is on the board, for every reader: the forge refusing the installation's credential,
	// since when, and the repositories whose facts it stops.
	since := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(context.Background(), "UPDATE installation SET problem = 'refused', problem_since = $2 WHERE id = $1",
		inst.Id, since); err != nil {
		t.Fatal(err)
	}
	board = read(reader)
	if len(board.Problems) != 1 {
		t.Fatalf("the board's problems: %+v", board.Problems)
	}
	if p := board.Problems[0]; p.Problem != CollectionProblemKindRefused || !p.Since.Equal(since) || p.Until != nil || p.Forge != "Hub" ||
		len(p.Repos) != 1 || p.Repos[0].Owner != "acme" || p.Repos[0].Name != "api" {
		t.Fatalf("the board's problem: %+v", p)
	}
}
