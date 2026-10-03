package bridges

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"testing"
	"time"
)

// fakePoster keeps what a round posts, by anchor, and fails for the anchors in failing. A proposal is open unless
// states says otherwise.
type fakePoster struct {
	asserted  map[string][]string
	titles    map[string]string
	proposed  map[string][]Group
	retracted []string
	withdrawn []string
	states    map[string]string
	failing   map[string]bool
	posts     int
}

func newPoster() *fakePoster {
	return &fakePoster{asserted: map[string][]string{}, titles: map[string]string{}, proposed: map[string][]Group{},
		states: map[string]string{}, failing: map[string]bool{}}
}

var errPost = errors.New("the grouping is unavailable")

func (f *fakePoster) assert(_ context.Context, anchor, title string, prs []string) (bool, error) {
	if f.failing[anchor] {
		return false, errPost
	}
	f.posts++
	f.asserted[anchor], f.titles[anchor] = prs, title
	return true, nil
}

func (f *fakePoster) retract(_ context.Context, anchor string) error {
	if f.failing[anchor] {
		return errPost
	}
	f.retracted = append(f.retracted, anchor)
	return nil
}

func (f *fakePoster) propose(_ context.Context, anchor, title string, groups []Group) (string, error) {
	if f.failing[anchor] {
		return "", errPost
	}
	f.posts++
	f.proposed[anchor], f.titles[anchor] = groups, title
	if s, ok := f.states[anchor]; ok {
		return s, nil
	}
	return "open", nil
}

func (f *fakePoster) retractProposal(_ context.Context, anchor string) error {
	f.withdrawn = append(f.withdrawn, anchor)
	return nil
}

// fakeModel answers each thread's text with groups, or with err, and keeps what it was asked.
type fakeModel struct {
	answers map[string][]Group
	err     error
	asked   []string
}

func (m *fakeModel) Split(_ context.Context, text string, prs []string) ([]Group, error) {
	m.asked = append(m.asked, text)
	if m.err != nil {
		return nil, m.err
	}
	return m.answers[text], nil
}

var roundNow = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

// thread is a thread posted hours ago, whose text is its title, linking prs.
func thread(anchor string, hours int, title string, prs ...string) found {
	return found{Anchor: anchor, Created: roundNow.Add(-time.Duration(hours) * time.Hour), Title: title, Text: title, PRs: prs}
}

// options are a round's options: acme/api and acme/web are watched, threads count for 45 days, and a model, when
// given, is asked about threads of four pull requests or more.
func options(m splitter) roundOptions {
	o := roundOptions{Now: roundNow, Lookback: 45 * 24 * time.Hour, Covered: map[string]bool{"acme/api": true, "acme/web": true},
		ProposeFrom: 4}
	if m != nil {
		o.Model = m
	}
	return o
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)) }

// A thread of two pull requests of watched repositories is asserted under its anchor, with its title. A pull request
// of a repository Park does not watch is left out, and a thread left with fewer than two is no assertion.
func TestAssertsThreads(t *testing.T) {
	var mem memory
	p := newPoster()
	n := mem.round(context.Background(), quiet(), []found{
		thread("m1", 1, "Billing export", "acme/api#12", "acme/web#7"),
		thread("m2", 2, "Invoices", "acme/api#13", "other/tool#4"),
	}, 2, p, options(nil))
	if n != 1 || !slices.Equal(p.asserted["m1"], []string{"acme/api#12", "acme/web#7"}) || p.titles["m1"] != "Billing export" {
		t.Fatalf("posted %d: %v %v", n, p.asserted, p.titles)
	}
	if _, ok := p.asserted["m2"]; ok {
		t.Fatal("a thread with one watched pull request was asserted")
	}
}

// A pull request belongs to the newest thread that links it: an older thread loses it, and with one left is no
// assertion.
func TestTheNewestThreadWins(t *testing.T) {
	var mem memory
	p := newPoster()
	mem.round(context.Background(), quiet(), []found{
		thread("old", 5, "Billing, first try", "acme/api#12", "acme/api#13"),
		thread("new", 1, "Billing", "acme/api#12", "acme/web#7"),
	}, 2, p, options(nil))
	if _, ok := p.asserted["old"]; ok || !slices.Equal(p.asserted["new"], []string{"acme/api#12", "acme/web#7"}) {
		t.Fatalf("asserted %v", p.asserted)
	}
}

// An assertion is posted again only when its thread changed: the same thread is not posted twice, and one retitled
// is posted again.
func TestPostsOnlyWhatChanged(t *testing.T) {
	var mem memory
	p := newPoster()
	threads := []found{thread("m1", 1, "Billing export", "acme/api#12", "acme/web#7")}
	mem.round(context.Background(), quiet(), threads, 1, p, options(nil))
	if n := mem.round(context.Background(), quiet(), threads, 1, p, options(nil)); n != 0 || p.posts != 1 {
		t.Fatalf("the same thread posted again: %d changes, %d posts", n, p.posts)
	}
	threads[0].Title = "Billing export, front and back"
	if n := mem.round(context.Background(), quiet(), threads, 1, p, options(nil)); n != 1 || p.titles["m1"] != "Billing export, front and back" {
		t.Fatalf("a retitled thread: %d changes, title %q", n, p.titles["m1"])
	}
}

// A thread gone from the window is retracted; one that aged out of it is forgotten, and left in Park as it is.
func TestRetractsWhatIsGone(t *testing.T) {
	mem := memory{Posted: map[string]posted{
		"gone": {Created: roundNow.Add(-24 * time.Hour), Title: "Deleted", PRs: []string{"acme/api#1", "acme/api#2"}},
		"aged": {Created: roundNow.Add(-50 * 24 * time.Hour), Title: "Old", PRs: []string{"acme/api#3", "acme/api#4"}},
	}}
	p := newPoster()
	n := mem.round(context.Background(), quiet(), nil, 30, p, options(nil))
	if n != 1 || !slices.Equal(p.retracted, []string{"gone"}) || len(mem.Posted) != 0 {
		t.Fatalf("%d changes, retracted %v, remembers %v", n, p.retracted, mem.Posted)
	}
}

// A round that read no messages retracts nothing: the account may not be able to read the chat.
func TestRetractsNothingWhenNothingWasRead(t *testing.T) {
	mem := memory{Posted: map[string]posted{"m1": {Created: roundNow.Add(-time.Hour), PRs: []string{"acme/api#1", "acme/api#2"}}}}
	p := newPoster()
	if n := mem.round(context.Background(), quiet(), nil, 0, p, options(nil)); n != 0 || len(p.retracted) != 0 || len(mem.Posted) != 1 {
		t.Fatalf("%d changes, retracted %v, remembers %v", n, p.retracted, mem.Posted)
	}
}

// What fails to post is not remembered, and is posted next round.
func TestFailedPostsAreTriedAgain(t *testing.T) {
	var mem memory
	p := newPoster()
	p.failing["m1"] = true
	threads := []found{thread("m1", 1, "Billing", "acme/api#12", "acme/web#7")}
	if n := mem.round(context.Background(), quiet(), threads, 1, p, options(nil)); n != 0 || len(mem.Posted) != 0 {
		t.Fatalf("a failed post: %d changes, remembers %v", n, mem.Posted)
	}
	p.failing["m1"] = false
	if n := mem.round(context.Background(), quiet(), threads, 1, p, options(nil)); n != 1 || len(p.asserted["m1"]) != 2 {
		t.Fatalf("the next round: %d changes, asserted %v", n, p.asserted)
	}
}

// What a bridge remembers of a thread from before Park ran it carries the time Park first saw it; once a round reads
// the thread unchanged, it carries the thread's own time, and nothing is posted.
func TestSeededThreadsTakeTheirOwnTime(t *testing.T) {
	seen := roundNow.Add(-time.Hour)
	mem := memory{Seeded: true, Posted: map[string]posted{"m1": {Created: seen, Title: "Billing", PRs: []string{"acme/api#12", "acme/web#7"}}}}
	p := newPoster()
	th := thread("m1", 3, "Billing", "acme/api#12", "acme/web#7")
	if n := mem.round(context.Background(), quiet(), []found{th}, 1, p, options(nil)); n != 0 || p.posts != 0 {
		t.Fatalf("%d changes, %d posts", n, p.posts)
	}
	if got := mem.Posted["m1"].Created; !got.Equal(th.Created) {
		t.Fatalf("the thread's time is %v, want %v", got, th.Created)
	}
}

// big is a thread of four pull requests, which the model is asked about.
func big(anchor, text string) found {
	return found{Anchor: anchor, Created: roundNow.Add(-time.Hour), Title: text, Text: text,
		PRs: []string{"acme/api#1", "acme/api#2", "acme/web#3", "acme/web#4"}}
}

var twoFeatures = []Group{{Name: "Exports", PRs: []string{"acme/api#1", "acme/web#3"}}, {Name: "Retries", PRs: []string{"acme/api#2", "acme/web#4"}}}

// A thread of four pull requests or more is asked about, with its text, and what the model answers is proposed under
// its anchor, its groups named as the model named them; a thread of fewer is asserted, and the model is not asked
// about it.
func TestProposesLargeThreads(t *testing.T) {
	var mem memory
	p := newPoster()
	m := &fakeModel{answers: map[string][]Group{"Two features": twoFeatures}}
	mem.round(context.Background(), quiet(), []found{big("b1", "Two features"), thread("s1", 2, "Small", "acme/api#9", "acme/web#9")}, 2, p,
		options(m))
	if !slices.Equal(m.asked, []string{"Two features"}) || !reflect.DeepEqual(p.proposed["b1"], twoFeatures) {
		t.Fatalf("asked %v, proposed %v", m.asked, p.proposed)
	}
	if _, ok := p.asserted["b1"]; ok || len(p.asserted["s1"]) != 2 {
		t.Fatalf("asserted %v", p.asserted)
	}
}

// The model's answer is kept: an unchanged thread is not asked about again, nor proposed again. A thread whose text
// is edited, its title kept, is asked about again, and proposed again only when the answer changed.
func TestKeepsTheModelsAnswer(t *testing.T) {
	var mem memory
	p := newPoster()
	m := &fakeModel{answers: map[string][]Group{"Two features": twoFeatures, "Two features, edited": twoFeatures,
		"Two features, split anew": twoFeatures[:1]}}
	edited := func(text string) found {
		b := big("b1", "Two features")
		b.Text = text
		return b
	}
	mem.round(context.Background(), quiet(), []found{edited("Two features")}, 1, p, options(m))
	mem.round(context.Background(), quiet(), []found{edited("Two features")}, 1, p, options(m))
	if len(m.asked) != 1 || p.posts != 1 {
		t.Fatalf("asked %v, %d posts", m.asked, p.posts)
	}
	mem.round(context.Background(), quiet(), []found{edited("Two features, edited")}, 1, p, options(m))
	if len(m.asked) != 2 || p.posts != 1 {
		t.Fatalf("an edit with the same answer: asked %v, %d posts", m.asked, p.posts)
	}
	mem.round(context.Background(), quiet(), []found{edited("Two features, split anew")}, 1, p, options(m))
	if len(m.asked) != 3 || p.posts != 2 || !reflect.DeepEqual(p.proposed["b1"], twoFeatures[:1]) {
		t.Fatalf("an edit with another answer: asked %v, %d posts, proposed %v", m.asked, p.posts, p.proposed["b1"])
	}
}

// A thread in which the model finds no unit of two or more is neither proposed nor asserted, and what was proposed
// for it before is retracted; so is an assertion made for it before the bridge had a model.
func TestNoUnitIsNothing(t *testing.T) {
	mem := memory{
		Posted:   map[string]posted{"b1": {Created: roundNow.Add(-time.Hour), PRs: big("b1", "").PRs}},
		Proposed: map[string]proposed{"b2": {Created: roundNow.Add(-time.Hour), Groups: twoFeatures}},
	}
	p := newPoster()
	m := &fakeModel{answers: map[string][]Group{}}
	mem.round(context.Background(), quiet(), []found{big("b1", "A progress report"), big("b2", "Another report")}, 2, p, options(m))
	if len(p.asserted) != 0 || len(p.proposed) != 0 || !slices.Equal(p.retracted, []string{"b1"}) || !slices.Equal(p.withdrawn, []string{"b2"}) {
		t.Fatalf("asserted %v, proposed %v, retracted %v, withdrew %v", p.asserted, p.proposed, p.retracted, p.withdrawn)
	}
}

// A thread the model cannot be asked about is left as it is: its assertion stays, and it is asked about again next
// round, when an answer proposes it, and retracts the assertion.
func TestHoldsWhatTheModelCannotAnswer(t *testing.T) {
	mem := memory{Posted: map[string]posted{"b1": {Created: roundNow.Add(-time.Hour), PRs: big("b1", "").PRs}}}
	p := newPoster()
	m := &fakeModel{err: errors.New("the model is down")}
	mem.round(context.Background(), quiet(), []found{big("b1", "Two features")}, 1, p, options(m))
	if len(p.retracted) != 0 || len(mem.Posted) != 1 {
		t.Fatalf("held: retracted %v, remembers %v", p.retracted, mem.Posted)
	}
	m.err, m.answers = nil, map[string][]Group{"Two features": twoFeatures}
	mem.round(context.Background(), quiet(), []found{big("b1", "Two features")}, 1, p, options(m))
	if !reflect.DeepEqual(p.proposed["b1"], twoFeatures) || !slices.Equal(p.retracted, []string{"b1"}) {
		t.Fatalf("answered: proposed %v, retracted %v", p.proposed, p.retracted)
	}
}

// A proposal whose thread is gone from the window is retracted; one whose thread aged out of it is forgotten, with the
// model's answer, and left as it is. A proposal a member decided is kept as posted.
func TestRetractsProposalsThatAreGone(t *testing.T) {
	mem := memory{
		Proposed: map[string]proposed{
			"gone": {Created: roundNow.Add(-24 * time.Hour), Groups: twoFeatures},
			"aged": {Created: roundNow.Add(-50 * 24 * time.Hour), Groups: twoFeatures},
		},
		Answers: map[string]answer{"aged": {Created: roundNow.Add(-50 * 24 * time.Hour), Groups: twoFeatures}},
	}
	p := newPoster()
	p.states["decided"] = "accepted"
	m := &fakeModel{answers: map[string][]Group{"Decided": twoFeatures}}
	mem.round(context.Background(), quiet(), []found{big("decided", "Decided")}, 3, p, options(m))
	if !slices.Equal(p.withdrawn, []string{"gone"}) || len(mem.Answers) != 1 {
		t.Fatalf("withdrew %v, answers %v", p.withdrawn, mem.Answers)
	}
	if _, ok := mem.Proposed["decided"]; !ok || len(mem.Proposed) != 1 {
		t.Fatalf("remembers %v", mem.Proposed)
	}
}
