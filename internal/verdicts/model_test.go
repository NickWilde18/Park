package verdicts

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/rules"
)

// model is a pull request as the design checks describe one, in docs/design/checks/placement.py; facts turns it into
// the facts the forge would report, so that tests run the real field computation.
type model struct {
	reviews     map[string]modelReview
	teams       map[string][]string
	required    *int
	checksDue   *time.Duration
	nudged      *int
	author      string
	checks      string
	direct      []string
	codeOwners  []string
	threads     []modelThread
	forgeBlocks []string
	position    int
	length      int
	openBelow   int
	groupSize   int
	requested   int
	noDismiss   bool
	draft       bool
	conflicting bool
	closed      bool
}

type modelReview struct {
	nudged     *int
	decision   string
	hours      int
	again      bool
	body       bool
	answered   bool
	newCommits bool
}

type modelThread struct {
	starter, last string
	others        []string
}

func thread(starter, last string, others ...string) modelThread {
	return modelThread{starter: starter, last: last, others: others}
}

func ptr[T any](v T) *T { return &v }

var accounts = map[string]int64{"ana": 1, "ben": 2, "carol": 3, "dave": 4, "eve": 5}

// now is the evaluation time of every model.
var now = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// facts returns the facts and the context of a model. Thread comments come before every review, so that a reply in a
// thread never answers a written review, which the model says apart; an answer comes right after its review. A model
// the facts cannot express is a mistake in a test, and panics.
func (m model) facts() (facts.PR, Context) {
	id := func(name string) int64 {
		if n, ok := accounts[name]; ok {
			return n
		}
		panic("an account the model does not know: " + name)
	}
	c := Context{Now: now, Logins: map[int64]string{}, Teams: map[int64]Team{}, Nudged: map[int64]time.Time{}, Repo: "api",
		Chain: grouping.Chain{Position: m.position, Length: m.length, Below: m.openBelow}, GroupSize: m.groupSize,
		MinimumApprovals: 1}
	for name, n := range accounts {
		c.Logins[n] = name
	}
	pr := facts.PR{Number: 12, Title: "A change", State: facts.StateOpen, Author: id(m.author), HeadSHA: "head",
		HeadPushedAt: now.Add(-2000 * time.Hour), BaseBranch: "main", Mergeable: facts.MergeableClean, RequiredApproval: 1,
		AuthorCanDismiss: !m.noDismiss, CodeOwnersNeeded: len(m.codeOwners) > 0}
	if m.required != nil {
		pr.RequiredApproval = *m.required
	}
	switch {
	case m.closed:
		pr.State = facts.StateClosed
	case m.draft:
		pr.State = facts.StateDraft
	}
	if m.conflicting {
		pr.Mergeable = facts.MergeableConflicting
	}
	requestedAt := now.Add(-time.Duration(max(m.requested, 0)) * time.Hour)
	if m.requested == 0 {
		requestedAt = now.Add(-2 * time.Hour)
	}
	owners := map[string]bool{}
	for _, name := range m.codeOwners {
		owners[name] = true
	}
	for _, name := range m.direct {
		pr.ReviewRequests = append(pr.ReviewRequests, facts.Request{Account: id(name), RequestedAt: &requestedAt, CodeOwner: owners[name]})
	}
	teamNames := make([]string, 0, len(m.teams))
	for name := range m.teams {
		teamNames = append(teamNames, name)
	}
	sort.Strings(teamNames)
	for i, name := range teamNames {
		team := Team{Slug: "acme/" + name, Members: []int64{}}
		for _, member := range m.teams[name] {
			team.Members = append(team.Members, id(member))
		}
		c.Teams[int64(101+i)] = team
		pr.ReviewRequests = append(pr.ReviewRequests, facts.Request{Team: int64(101 + i), RequestedAt: &requestedAt})
	}
	for i, th := range m.threads {
		speakers := append([]string{th.starter}, th.others...)
		if speakers[len(speakers)-1] != th.last {
			speakers = append(speakers, th.last)
		}
		thread := facts.Thread{ID: fmt.Sprintf("thread-%d", i)}
		for k, name := range speakers {
			thread.Comments = append(thread.Comments, facts.Comment{ID: int64(1000*i + k), Author: id(name),
				At: now.Add(-1000*time.Hour + time.Duration(10*i+k)*time.Minute)})
		}
		pr.Threads = append(pr.Threads, thread)
	}
	names := make([]string, 0, len(m.reviews))
	for name := range m.reviews {
		names = append(names, name)
	}
	sort.Strings(names)
	approved := 0
	var answers, unanswered []time.Time
	for _, name := range names {
		r := m.reviews[name]
		hours := r.hours
		if hours == 0 {
			hours = 2
		}
		at := now.Add(-time.Duration(hours) * time.Hour)
		commit := "head"
		if r.newCommits {
			commit = "old"
		}
		state := facts.ReviewCommented
		switch r.decision {
		case "approved":
			state = facts.ReviewApproved
			approved++
		case "changes_requested":
			state = facts.ReviewChangesRequested
		}
		first := at
		if r.again {
			first = at.Add(-time.Minute)
		}
		pr.Reviews = append(pr.Reviews, facts.Review{ID: int64(len(pr.Reviews) + 1), Author: id(name), State: state, At: first,
			Commit: commit, HasBody: r.body, Counts: true})
		if r.again {
			pr.Reviews = append(pr.Reviews, facts.Review{ID: int64(len(pr.Reviews) + 1), Author: id(name), State: facts.ReviewCommented,
				At: at, Commit: commit, Counts: true})
		}
		if r.body && !r.answered {
			unanswered = append(unanswered, first)
		}
		if r.answered {
			answers = append(answers, first.Add(time.Second))
			pr.Conversation = append(pr.Conversation, facts.Comment{ID: int64(9000 + len(answers)), Author: pr.Author, At: first.Add(time.Second)})
		}
		if r.nudged != nil {
			c.Nudged[id(name)] = now.Add(-time.Duration(*r.nudged) * time.Hour)
		}
	}
	// An answer answers every review before it: the model cannot leave an earlier written review unanswered.
	for _, u := range unanswered {
		for _, a := range answers {
			if a.After(u) {
				panic(fmt.Sprintf("an answer at %v would answer the unanswered review at %v", a, u))
			}
		}
	}
	if m.nudged != nil {
		c.Nudged[pr.Author] = now.Add(-time.Duration(*m.nudged) * time.Hour)
	}
	switch checks := m.checks; checks {
	case "", "passing":
		pr.Checks = []facts.Check{{Name: "ci", State: facts.CheckPassing, Required: true}}
	case "pending":
		due := 30 * time.Minute
		if m.checksDue != nil {
			due = *m.checksDue
		}
		since := now.Add(-due)
		pr.Checks = []facts.Check{{Name: "ci", State: facts.CheckPending, Required: true, Since: &since}}
	case "failing":
		pr.Checks = []facts.Check{{Name: "ci", State: facts.CheckFailing, Required: true}}
	case "none":
	default:
		panic("checks " + checks)
	}
	// What the forge itself would say blocks merging; the team's minimum is not the forge's.
	pr.MergeBlockers = []string{}
	if approved < pr.RequiredApproval {
		pr.MergeBlockers = append(pr.MergeBlockers, "reviews")
	}
	for _, name := range m.codeOwners {
		if m.reviews[name].decision != "approved" {
			pr.MergeBlockers = append(pr.MergeBlockers, "code_owners")
			break
		}
	}
	if m.checks == "pending" || m.checks == "failing" {
		pr.MergeBlockers = append(pr.MergeBlockers, "checks")
	}
	if m.conflicting {
		pr.MergeBlockers = append(pr.MergeBlockers, "conflicts")
	}
	pr.MergeBlockers = append(pr.MergeBlockers, m.forgeBlocks...)
	pr.CanMergeNow = len(pr.MergeBlockers) == 0 && !m.draft
	return pr, c
}

var compiled = func() *rules.Compiled {
	c, problems := rules.Compile(rules.Default())
	if len(problems) > 0 {
		panic(fmt.Sprintf("the default rule set: %+v", problems))
	}
	return c
}()

// unit evaluates every open pull request of a model unit with the default rule set, and places its ball.
func unit(t testing.TB, prs map[string]model) ([]Part, Placement) {
	t.Helper()
	names := make([]string, 0, len(prs))
	for name := range prs {
		names = append(names, name)
	}
	sort.Strings(names)
	var parts []Part
	for i, name := range names {
		m := prs[name]
		if m.closed {
			continue
		}
		pr, c := m.facts()
		for _, p := range People(pr, c) {
			parts = append(parts, Part{Part: compiled.Evaluate(p.Role, p.Input), Role: p.Role, PR: int64(i + 1), Account: p.Account})
		}
	}
	return parts, Place(parts)
}
