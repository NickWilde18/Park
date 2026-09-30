package facts

import (
	"reflect"
	"testing"
	"time"
)

func samplePR() PR {
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	return PR{Number: 12, Title: "Billing export", State: StateOpen, Author: 501, HeadSHA: "a1", BaseBranch: "main",
		CreatedAt: at, UpdatedAt: at, HeadPushedAt: at, Labels: []string{}, MergeBlockers: []string{}, ReviewRequests: []Request{},
		Reviews: []Review{{ID: 1, Author: 502, State: ReviewApproved, At: at, Counts: true}}, Threads: []Thread{},
		Conversation: []Comment{}, Checks: []Check{{Name: "build", State: CheckPending}}, MissingChecks: []string{},
		References: []Ref{}, ClosesIssues: []Ref{}, Mergeable: MergeableClean}
}

// The same state twice differs in nothing (F3); a change names only the fields that moved, with both values.
func TestDiff(t *testing.T) {
	a := samplePR()
	b := samplePR()
	if changes, err := Diff(&a, &b); err != nil || len(changes) != 0 {
		t.Fatalf("the same state: %v, %v", changes, err)
	}
	b.Checks = []Check{{Name: "build", State: CheckPassing}}
	b.HeadSHA = "b2"
	changes, err := Diff(&a, &b)
	if err != nil || !reflect.DeepEqual(Changed(changes), []string{"checks", "head_sha"}) {
		t.Fatalf("changes %v, %v", Changed(changes), err)
	}
	if string(changes["head_sha"].Old) != `"a1"` || string(changes["head_sha"].New) != `"b2"` {
		t.Fatalf("head_sha change %+v", changes["head_sha"])
	}
	all, _ := Diff(nil, &a)
	if len(all) < 20 || string(all["title"].Old) != "null" {
		t.Fatalf("a new pull request: %d fields", len(all))
	}
}

// Replaying the events in order rebuilds the state (F1).
func TestReplay(t *testing.T) {
	a := samplePR()
	b := samplePR()
	b.Title, b.HeadSHA = "Billing export v2", "b2"
	c := b
	c.State, c.MergedAt = StateMerged, &b.UpdatedAt
	var events []map[string]Change
	prev := (*PR)(nil)
	for _, p := range []PR{a, b, c} {
		p := p
		changes, err := Diff(prev, &p)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, changes)
		prev = &p
	}
	got, err := Replay(events)
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("replayed %+v\nwant %+v\n%v", got, c, err)
	}
}

// References: a bare #N is in the same repository; name#N and owner/name#N only when exactly one watched repository
// matches; fragments of addresses, the pull request itself, and duplicates are not references.
func TestParseRefs(t *testing.T) {
	api := WatchedRepo{ID: 1, Owner: "acme", Name: "api"}
	watched := []WatchedRepo{api, {ID: 2, Owner: "acme", Name: "web"}, {ID: 3, Owner: "acme", Name: "docs"}, {ID: 4, Owner: "other", Name: "docs"}}
	text := "Needs web#7 and #9 (see acme/web#8, docs#5, other/docs#6, ghost#1, #12, #9).\n" +
		"Log line https://ci.example.test/job#123 and &#38; and user@host#4."
	got := ParseRefs(text, api, 12, watched)
	want := []Ref{{Repo: 1, Number: 9}, {Repo: 2, Number: 7}, {Repo: 2, Number: 8}, {Repo: 4, Number: 6}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
