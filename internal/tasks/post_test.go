package tasks

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
)

var (
	t0     = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	before = t0.Add(-time.Hour)
	after  = t0.Add(time.Hour)
	start  = Start{At: t0, Head: "old"}
	anaIs  = map[int64]bool{1: true}
)

func login(id int64) string { return map[int64]string{1: "ana", 2: "ben", 3: "carol"}[id] }

func comment(author int64, at time.Time) facts.Comment { return facts.Comment{Author: author, At: at} }

// fix_checks: satisfied once a new head has passed every check of the set; not settled while one has not reported on
// it; not satisfied without a new commit, whatever the checks say.
func TestFixChecksPostCondition(t *testing.T) {
	set := Targets{Checks: []string{"build", "e2e"}}
	for _, c := range []struct {
		name               string
		head               string
		checks             []facts.Check
		settled, satisfied bool
		missing            string
	}{
		{"no new commit", "old", []facts.Check{{Name: "build", State: "passing"}, {Name: "e2e", State: "passing"}}, true, false, "no commit was pushed"},
		{"one still running", "new", []facts.Check{{Name: "build", State: "passing"}, {Name: "e2e", State: "pending"}}, false, false, "1 of 2 checks"},
		{"one not reported", "new", []facts.Check{{Name: "build", State: "passing"}}, false, false, "e2e"},
		{"one failed", "new", []facts.Check{{Name: "build", State: "failing"}, {Name: "e2e", State: "passing"}}, true, false, "1 of 2 checks have not passed: build"},
		{"every one passed", "new", []facts.Check{{Name: "build", State: "passing"}, {Name: "e2e", State: "passing"}}, true, true, ""},
	} {
		o := check("fix_checks", facts.PR{HeadSHA: c.head, Checks: c.checks}, set, start, anaIs, login)
		if o.settled != c.settled || o.satisfied != c.satisfied || !strings.Contains(o.missing, c.missing) {
			t.Errorf("%s: %+v", c.name, o)
		}
	}
}

// address_comments: a thread counts as answered once the member commented in it after the start, or it is resolved;
// a written review, once the member commented on the pull request or pushed after it. What happened before the start
// does not count (T14), and neither does someone else's comment.
func TestAddressCommentsPostCondition(t *testing.T) {
	pr := facts.PR{Author: 1, Threads: []facts.Thread{
		{ID: "T1", Comments: []facts.Comment{comment(2, before), comment(1, after)}},
		{ID: "T2", Comments: []facts.Comment{comment(2, before), comment(1, before)}},
		{ID: "T3", Comments: []facts.Comment{comment(2, before)}, Resolved: true},
		{ID: "T4", Comments: []facts.Comment{comment(2, before), comment(3, after)}},
	}, Reviews: []facts.Review{{ID: 7, Author: 2, At: before}, {ID: 8, Author: 3, At: before}}, Conversation: []facts.Comment{comment(1, after)}}
	o := check("address_comments", pr, Targets{Threads: []string{"T1", "T2", "T3", "T4"}, Reviews: []int64{7, 8}}, start, anaIs, login)
	if o.satisfied || !o.settled || !slices.Equal(o.remaining.Threads, []string{"T2", "T4"}) || len(o.remaining.Reviews) != 0 ||
		o.missing != "2 of 4 threads have no answer: T2, T4" {
		t.Fatalf("%+v", o)
	}
	pr.Conversation = nil
	o = check("address_comments", pr, Targets{Reviews: []int64{7}}, start, anaIs, login)
	if o.satisfied || !slices.Equal(o.remaining.Reviews, []int64{7}) {
		t.Fatalf("a review with no answer: %+v", o)
	}
	pr.HeadPushedAt = after
	if o := check("address_comments", pr, Targets{Reviews: []int64{7}}, start, anaIs, login); !o.satisfied {
		t.Fatalf("a push answers a written review: %+v", o)
	}
}

// request_review: each target reviewer asked, or reviewing, after the start; where the author may dismiss, none of
// them still requesting changes. With no targets, anyone asked after the start will do.
func TestRequestReviewPostCondition(t *testing.T) {
	asked := after
	old := before
	pr := facts.PR{AuthorCanDismiss: true, ReviewRequests: []facts.Request{{Account: 2, RequestedAt: &asked}, {Account: 3, RequestedAt: &old}},
		Reviews: []facts.Review{{Author: 2, State: facts.ReviewChangesRequested, At: before}}}
	o := check("request_review", pr, Targets{Reviewers: []int64{2, 3}}, start, anaIs, login)
	if o.satisfied || !slices.Equal(o.remaining.Reviewers, []int64{2, 3}) || o.missing != "ben still requests changes; carol was not asked again" {
		t.Fatalf("%+v", o)
	}
	pr.Reviews = append(pr.Reviews, facts.Review{Author: 2, State: facts.ReviewDismissed, At: before.Add(time.Minute)})
	if o := check("request_review", pr, Targets{Reviewers: []int64{2}}, start, anaIs, login); !o.satisfied {
		t.Fatalf("dismissed, and asked again: %+v", o)
	}
	pr.AuthorCanDismiss = false
	pr.Reviews = pr.Reviews[:1]
	if o := check("request_review", pr, Targets{Reviewers: []int64{2}}, start, anaIs, login); !o.satisfied {
		t.Fatalf("an author who cannot dismiss hands back by asking again: %+v", o)
	}
	if o := check("request_review", pr, Targets{}, start, anaIs, login); !o.satisfied {
		t.Fatalf("with no targets, anyone asked will do: %+v", o)
	}
	pr.ReviewRequests = pr.ReviewRequests[1:]
	if o := check("request_review", pr, Targets{}, start, anaIs, login); o.satisfied {
		t.Fatalf("nobody was asked after the start: %+v", o)
	}
}

// review: satisfied once one of the member's accounts reviewed after the start; nobody else's review counts.
func TestReviewPostCondition(t *testing.T) {
	pr := facts.PR{Reviews: []facts.Review{{Author: 2, At: after}, {Author: 1, At: before}}}
	if o := check("review", pr, Targets{}, start, anaIs, login); o.satisfied || o.missing != "no review was submitted" {
		t.Fatalf("%+v", o)
	}
	pr.Reviews = append(pr.Reviews, facts.Review{Author: 1, At: after})
	if o := check("review", pr, Targets{}, start, anaIs, login); !o.satisfied {
		t.Fatalf("%+v", o)
	}
}

// resolve_conflicts: settled once mergeability is known; satisfied when the forge reports no conflicts.
func TestResolveConflictsPostCondition(t *testing.T) {
	for mergeable, want := range map[string][2]bool{facts.MergeableUnknown: {false, false}, facts.MergeableConflicting: {true, false},
		facts.MergeableClean: {true, true}} {
		if o := check("resolve_conflicts", facts.PR{Mergeable: mergeable}, Targets{}, start, anaIs, login); o.settled != want[0] || o.satisfied != want[1] {
			t.Errorf("%s: %+v", mergeable, o)
		}
	}
}

// Targets come from the facts when an item is made: the required checks, or without protection every check that
// ran; the threads and written reviews waiting on the author; the reviewers the part names.
func TestTargetsOf(t *testing.T) {
	pr := facts.PR{Author: 1, Checks: []facts.Check{{Name: "lint"}, {Name: "build", Required: true}}, MissingChecks: []string{"e2e"},
		Threads: []facts.Thread{{ID: "T1", Comments: []facts.Comment{comment(2, before)}}, {ID: "T2", Comments: []facts.Comment{comment(2, before), comment(1, after)}}},
		Reviews: []facts.Review{{ID: 7, Author: 2, HasBody: true, State: facts.ReviewCommented, At: before}, {ID: 8, Author: 2, State: facts.ReviewCommented, At: before}}}
	if got := targetsOf("fix_checks", pr, nil); !slices.Equal(got.Checks, []string{"build", "e2e"}) {
		t.Errorf("required checks %v", got.Checks)
	}
	if got := targetsOf("fix_checks", facts.PR{Checks: []facts.Check{{Name: "lint"}, {Name: "build"}}}, nil); !slices.Equal(got.Checks, []string{"build", "lint"}) {
		t.Errorf("without protection %v", got.Checks)
	}
	if got := targetsOf("address_comments", pr, nil); !slices.Equal(got.Threads, []string{"T1"}) || len(got.Reviews) != 0 {
		t.Errorf("waiting on the author %+v (the author's reply after the review answers it)", got)
	}
	pr.Threads = nil
	if got := targetsOf("address_comments", pr, nil); !slices.Equal(got.Reviews, []int64{7}) {
		t.Errorf("written reviews %+v", got)
	}
	if got := targetsOf("request_review", pr, []int64{2, 3}); !slices.Equal(got.Reviewers, []int64{2, 3}) {
		t.Errorf("reviewers %+v", got)
	}
}

// A written review is a target of address_comments only while it waits on the author, as the rules read it: not
// once its writer was asked to review again after it (#92), nor once its writer approved after it (#67). Of three
// written reviews from before, account 2's still waits; the author asked account 3 again after; account 4 approved
// after.
func TestTargetsOfAnsweredReviews(t *testing.T) {
	asked := after
	pr := facts.PR{Author: 1, Reviews: []facts.Review{
		{ID: 7, Author: 2, HasBody: true, State: facts.ReviewCommented, At: before},
		{ID: 8, Author: 3, HasBody: true, State: facts.ReviewChangesRequested, At: before},
		{ID: 9, Author: 4, HasBody: true, State: facts.ReviewCommented, At: before},
		{ID: 10, Author: 4, State: facts.ReviewApproved, At: after},
	}, ReviewRequests: []facts.Request{{Account: 3, RequestedAt: &asked}}}
	if got := targetsOf("address_comments", pr, nil); !slices.Equal(got.Reviews, []int64{7}) {
		t.Errorf("written reviews waiting %v, want only account 2's, 7", got.Reviews)
	}
}
