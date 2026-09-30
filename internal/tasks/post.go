package tasks

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
)

// Targets are what an item works on: threads and written reviews to answer, required checks to pass, or reviewers
// to ask. Reviewers are account IDs.
type Targets struct {
	Threads   []string `json:"threads,omitempty"`
	Reviews   []int64  `json:"reviews,omitempty"`
	Checks    []string `json:"checks,omitempty"`
	Reviewers []int64  `json:"reviewers,omitempty"`
}

func (t Targets) empty() bool {
	return len(t.Threads) == 0 && len(t.Reviews) == 0 && len(t.Checks) == 0 && len(t.Reviewers) == 0
}

// minus is the targets t has that held does not.
func (t Targets) minus(held Targets) Targets {
	return Targets{Threads: without(t.Threads, held.Threads), Reviews: without(t.Reviews, held.Reviews),
		Checks: without(t.Checks, held.Checks), Reviewers: without(t.Reviewers, held.Reviewers)}
}

// plus is the targets of both.
func (t Targets) plus(more Targets) Targets {
	return Targets{Threads: union(t.Threads, more.Threads), Reviews: union(t.Reviews, more.Reviews),
		Checks: union(t.Checks, more.Checks), Reviewers: union(t.Reviewers, more.Reviewers)}
}

func without[T comparable](xs, ys []T) []T {
	var out []T
	for _, x := range xs {
		if !slices.Contains(ys, x) {
			out = append(out, x)
		}
	}
	return out
}

func union[T comparable](xs, ys []T) []T {
	out := append([]T(nil), xs...)
	for _, y := range ys {
		if !slices.Contains(out, y) {
			out = append(out, y)
		}
	}
	return out
}

// Start is what an item's post-condition compares against: the time, and the head commit, at the start of an
// attempt — or, before the first, when the item was made.
type Start struct {
	At   time.Time `json:"at"`
	Head string    `json:"head"`
}

// outcome is what the facts say of an item: whether they have settled, whether every target is satisfied, which
// remain, and what is missing.
type outcome struct {
	missing   string
	remaining Targets
	settled   bool
	satisfied bool
}

// targetsOf builds an item's targets from the facts when it is made: docs/design/06-tasks.md#items-and-post-conditions.
// For fix_checks, the checks the forge requires, or without protection the checks that ran on the head; for
// address_comments, the threads and written reviews waiting on the author; for request_review, the reviewers the part
// names.
func targetsOf(action string, pr facts.PR, reviewers []int64) Targets {
	switch action {
	case "fix_checks":
		var required, ran []string
		for _, c := range pr.Checks {
			ran = union(ran, []string{c.Name})
			if c.Required {
				required = union(required, []string{c.Name})
			}
		}
		required = union(required, pr.MissingChecks)
		if len(required) == 0 {
			required = ran
		}
		sort.Strings(required)
		return Targets{Checks: required}
	case "address_comments":
		var t Targets
		for _, th := range pr.Threads {
			if !th.Resolved && len(th.Comments) > 0 && th.Comments[len(th.Comments)-1].Author != pr.Author {
				t.Threads = append(t.Threads, th.ID)
			}
		}
		for _, r := range pr.Reviews {
			if r.Author != pr.Author && r.HasBody && (r.State == facts.ReviewChangesRequested || r.State == facts.ReviewCommented) &&
				!authorAnswered(pr, r.At) {
				t.Reviews = append(t.Reviews, r.ID)
			}
		}
		return t
	case "request_review":
		return Targets{Reviewers: append([]int64(nil), reviewers...)}
	}
	return Targets{}
}

// authorAnswered reports whether the author commented on the pull request, in its conversation or a thread, or pushed,
// after t.
func authorAnswered(pr facts.PR, t time.Time) bool {
	if pr.HeadPushedAt.After(t) {
		return true
	}
	for _, c := range pr.Conversation {
		if c.Author == pr.Author && c.At.After(t) {
			return true
		}
	}
	for _, th := range pr.Threads {
		for _, c := range th.Comments {
			if c.Author == pr.Author && c.At.After(t) {
				return true
			}
		}
	}
	return false
}

// check judges an item's post-condition against the facts, looking only for what happened after the start (T14). The
// member's accounts are the ones whose events count as the member's.
func check(action string, pr facts.PR, t Targets, s Start, member map[int64]bool, login func(int64) string) outcome {
	switch action {
	case "resolve_conflicts":
		o := outcome{settled: pr.Mergeable != facts.MergeableUnknown, satisfied: pr.Mergeable == facts.MergeableClean}
		if !o.satisfied {
			o.missing = "the forge still reports conflicts"
		}
		return o
	case "fix_checks":
		return checkChecks(pr, t, s)
	case "address_comments":
		return checkComments(pr, t, s, member)
	case "request_review":
		return checkRequests(pr, t, s, login)
	case "review":
		for _, r := range pr.Reviews {
			if member[r.Author] && r.At.After(s.At) {
				return outcome{settled: true, satisfied: true}
			}
		}
		return outcome{settled: true, missing: "no review was submitted"}
	}
	return outcome{settled: true, missing: "no post-condition for " + action}
}

func checkChecks(pr facts.PR, t Targets, s Start) outcome {
	if pr.HeadSHA == s.Head {
		return outcome{settled: true, remaining: t, missing: "no commit was pushed"}
	}
	o := outcome{settled: true}
	for _, name := range t.Checks {
		state := ""
		for _, c := range pr.Checks {
			if c.Name == name {
				state = c.State
			}
		}
		switch state {
		case facts.CheckPassing:
		case facts.CheckFailing:
			o.remaining.Checks = append(o.remaining.Checks, name)
		default:
			o.settled = false
			o.remaining.Checks = append(o.remaining.Checks, name)
		}
	}
	o.satisfied = len(o.remaining.Checks) == 0
	if !o.satisfied {
		o.missing = fmt.Sprintf("%d of %d checks have not passed: %s", len(o.remaining.Checks), len(t.Checks),
			strings.Join(o.remaining.Checks, ", "))
	}
	return o
}

func checkComments(pr facts.PR, t Targets, s Start, member map[int64]bool) outcome {
	o := outcome{settled: true}
	for _, id := range t.Threads {
		i := slices.IndexFunc(pr.Threads, func(th facts.Thread) bool { return th.ID == id })
		if i < 0 || pr.Threads[i].Resolved {
			continue
		}
		answered := false
		for _, c := range pr.Threads[i].Comments {
			answered = answered || (member[c.Author] && c.At.After(s.At))
		}
		if !answered {
			o.remaining.Threads = append(o.remaining.Threads, id)
		}
	}
	for _, id := range t.Reviews {
		i := slices.IndexFunc(pr.Reviews, func(r facts.Review) bool { return r.ID == id })
		if i < 0 {
			continue
		}
		after := s.At
		if pr.Reviews[i].At.After(after) {
			after = pr.Reviews[i].At
		}
		answered := pr.HeadPushedAt.After(after)
		for _, c := range pr.Conversation {
			answered = answered || (member[c.Author] && c.At.After(after))
		}
		if !answered {
			o.remaining.Reviews = append(o.remaining.Reviews, id)
		}
	}
	o.satisfied = o.remaining.empty()
	var parts []string
	if n := len(o.remaining.Threads); n > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d threads have no answer: %s", n, len(t.Threads), strings.Join(o.remaining.Threads, ", ")))
	}
	if n := len(o.remaining.Reviews); n > 0 {
		ids := make([]string, n)
		for i, id := range o.remaining.Reviews {
			ids[i] = strconv.FormatInt(id, 10)
		}
		parts = append(parts, fmt.Sprintf("%d of %d written reviews have no answer: %s", n, len(t.Reviews), strings.Join(ids, ", ")))
	}
	o.missing = strings.Join(parts, "; ")
	return o
}

func checkRequests(pr facts.PR, t Targets, s Start, login func(int64) string) outcome {
	askedSince := func(r facts.Request) bool { return r.RequestedAt != nil && r.RequestedAt.After(s.At) }
	if len(t.Reviewers) == 0 {
		for _, r := range pr.ReviewRequests {
			if askedSince(r) {
				return outcome{settled: true, satisfied: true}
			}
		}
		return outcome{settled: true, missing: "nobody was asked to review"}
	}
	reviews := append([]facts.Review(nil), pr.Reviews...)
	sort.SliceStable(reviews, func(i, j int) bool { return reviews[i].At.Before(reviews[j].At) })
	o := outcome{settled: true}
	var missing []string
	for _, id := range t.Reviewers {
		asked, blocking := false, false
		for _, r := range pr.ReviewRequests {
			asked = asked || (r.Account == id && askedSince(r))
		}
		for _, r := range reviews {
			if r.Author != id {
				continue
			}
			asked = asked || r.At.After(s.At)
			switch r.State {
			case facts.ReviewChangesRequested:
				blocking = true
			case facts.ReviewApproved, facts.ReviewDismissed:
				blocking = false
			}
		}
		switch {
		case !asked:
			missing = append(missing, login(id)+" was not asked again")
		case pr.AuthorCanDismiss && blocking:
			missing = append(missing, login(id)+" still requests changes")
		default:
			continue
		}
		o.remaining.Reviewers = append(o.remaining.Reviewers, id)
	}
	o.satisfied = len(o.remaining.Reviewers) == 0
	o.missing = strings.Join(missing, "; ")
	return o
}
