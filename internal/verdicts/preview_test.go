package verdicts

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/rules"
)

// A draft's preview evaluates every open unit with the version in force and with the draft, from the same facts at the
// same moment, two hours after the units were last evaluated, and lists only what the draft changes:
//
//   - The Billing group, Ana's draft and Ben's conflicting pull request, has its ball with Ben, to resolve the
//     conflicts. The draft words that reason differently, so the group changes, and Ben, who holds the ball now,
//     holds it since he did. Eve's pull request in it closed, and has no part: open, nobody asked to review it would
//     have her hold the ball too.
//   - Ana's pull request awaits Ben's first review. The draft makes first reviews wait, so nobody has anything to do
//     there, and the ball falls back to Ana from the preview's moment: the unit both changes and is on the fallback.
//   - Carol's pull request has checks running for half an hour when it was evaluated, and two and a half hours by the
//     preview's moment: the version in force now has her fix them, and so does the draft, so it is not listed, though
//     its stored verdict still says she waits.
//   - A closed pull request in no group is not evaluated.
//
// The draft's first reviewer rule reads a judgement nobody answered: Ben's path shows it unknown, and no model is
// asked. Groups are listed before pull requests, and nothing is stored or queued. Once the draft is published,
// previewing it again changes nothing, and still lists Ana's pull request on the fallback.
func TestPreview(t *testing.T) {
	e := newEnv(t)
	own := e.pr(t, 12, model{author: "ana", direct: []string{"ben"}})
	drafted := e.pr(t, 13, model{author: "ana", draft: true})
	conflicting := e.pr(t, 14, model{author: "ben", conflicting: true})
	running := e.pr(t, 15, model{author: "carol", checks: "pending"})
	e.pr(t, 16, model{author: "dave", closed: true})
	closing := e.pr(t, 17, model{author: "eve"})
	g, err := e.groups.Create(e.ctx, e.members["ana"], "Billing", []int64{drafted, conflicting, closing})
	must(t, err)
	e.pr(t, 17, model{author: "eve", closed: true})
	group := "group:" + strconv.FormatInt(g, 10)
	for _, u := range []string{group, unitOfPR(own), unitOfPR(running)} {
		must(t, e.s.Evaluate(e.ctx, u))
	}
	if v := e.current(t, unitOfPR(running)); v.Holders[0].Parts[0].Kind != rules.KindHold {
		t.Fatalf("Carol's stored verdict %+v", v)
	}
	e.queued(t)
	stored := e.count(t, "SELECT count(*) FROM verdict")
	e.clock = now.Add(2 * time.Hour)

	rs := rules.Default()
	for i, r := range rs.Author {
		if r.Name == "Conflicts" {
			rs.Author[i].Then.Reason = "Resolve the conflicts first"
		}
	}
	for i, r := range rs.Reviewer {
		if r.Name == "First review" {
			rs.Reviewer[i].Then = rules.Conclusion{Kind: rules.KindIdle, Reason: "First reviews wait"}
		}
	}
	rs.Reviewer = append([]rules.Rule{{Name: "Blocking comments", When: rules.Condition{Expr: "judgement.comments_blocking == 'yes'"},
		Then: rules.Conclusion{Kind: rules.KindAct, Action: "review", Reason: "Your comments block"}}}, rs.Reviewer...)
	draft, problems := rules.Compile(rs)
	if len(problems) > 0 {
		t.Fatalf("the draft's problems: %+v", problems)
	}

	p, err := e.s.Preview(e.ctx, draft)
	must(t, err)
	if p.Version != 1 || p.Units != 3 || !p.EvaluatedAt.Equal(e.clock) || len(p.Changes) != 2 || len(p.Fallback) != 1 {
		t.Fatalf("preview: version %d, %d units at %v, %d changes, %d on the fallback", p.Version, p.Units, p.EvaluatedAt,
			len(p.Changes), len(p.Fallback))
	}
	before, after := p.Changes[0].Before, p.Changes[0].After
	if before.Unit != group || after.Unit != group || !slices.Equal(holderNames(before), []string{"ben"}) ||
		!slices.Equal(holderNames(after), []string{"ben"}) || before.Holders[0].Parts[0].Reason == "Resolve the conflicts first" ||
		after.Holders[0].Parts[0].Reason != "Resolve the conflicts first" || !after.Holders[0].Since.Equal(now) {
		t.Fatalf("the group, before %+v, after %+v", before.Holders, after.Holders)
	}
	before, after = p.Changes[1].Before, p.Changes[1].After
	if before.Unit != unitOfPR(own) || before.Side != rules.Reviewer || before.Fallback || !slices.Equal(holderNames(before), []string{"ben"}) ||
		!before.Holders[0].Since.Equal(now) || after.Side != rules.Author || !after.Fallback ||
		!slices.Equal(holderNames(after), []string{"ana"}) || !after.Holders[0].Since.Equal(e.clock) || after.RuleVersion != 0 {
		t.Fatalf("Ana's pull request, before %+v, after %+v", before, after)
	}
	if p.Fallback[0].Unit != unitOfPR(own) {
		t.Fatalf("on the fallback: %s", p.Fallback[0].Unit)
	}
	var path []rules.Step
	for _, part := range after.Parts {
		if part.Login == "ben" {
			path = part.Path
		}
	}
	if len(path) == 0 || path[0].Outcome != rules.Unknown || !slices.Equal(path[0].Judgements, []string{"comments_blocking"}) {
		t.Fatalf("Ben's path %+v", path)
	}
	if n := e.count(t, "SELECT count(*) FROM verdict"); n != stored {
		t.Fatalf("the preview stored verdicts: %d, then %d", stored, n)
	}
	if q := e.queued(t); len(q) != 0 {
		t.Fatalf("the preview queued %v", q)
	}

	d, err := e.rules.CreateDraft(e.ctx, e.members["ana"], nil, &rs)
	must(t, err)
	_, err = e.rules.Publish(e.ctx, e.members["ana"], d.ID)
	must(t, err)
	p, err = e.s.Preview(e.ctx, draft)
	must(t, err)
	if p.Version != 2 || len(p.Changes) != 0 || len(p.Fallback) != 1 || p.Fallback[0].Unit != unitOfPR(own) {
		t.Fatalf("the published draft's preview: version %d, %d changes, on the fallback %+v", p.Version, len(p.Changes), p.Fallback)
	}
}
