package grouping

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
)

var t0 = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)

var longLived = []string{"main", "master", "develop", "dev", "trunk", "release/*"}

// node is an open pull request in repository 1 (forge ID 101), unless moved.
func node(id int64, head, base string) Node {
	return Node{ID: id, Repo: 1, RepoForge: 101, HeadRepo: 101, HeadBranch: head, BaseBranch: base, Number: int(id),
		CreatedAt: t0.Add(time.Duration(id) * time.Minute)}
}

// The cases of docs/design/checks/grouping.py: a release pull request from develop is no parent; a fork's main is
// no parent; two pull requests on one parent make a tree; a backport's descendant depends on both copies (G15).
func TestChainEdges(t *testing.T) {
	fork := node(53, "main", "main")
	fork.HeadRepo = 999
	nodes := []Node{
		node(50, "develop", "main"), node(51, "feat/a", "develop"), node(52, "feat/b", "develop"), fork, node(54, "fix/c", "main"),
		node(60, "feat/x", "main"), node(61, "feat/x2", "feat/x"), node(62, "feat/x3", "feat/x"),
		node(70, "fix/y", "main"), node(71, "fix/y", "release/1"), node(72, "fix/y2", "fix/y"),
	}
	chains := Chains(nodes, map[int64]RepoInfo{1: {DefaultBranch: "main"}}, longLived)
	edges := map[string]bool{}
	for id, c := range chains {
		for _, p := range c.Parents {
			edges[fmt.Sprintf("%d->%d", id, p)] = true
		}
	}
	want := map[string]bool{"61->60": true, "62->60": true, "72->70": true, "72->71": true}
	if !reflect.DeepEqual(edges, want) {
		t.Fatalf("edges %v, want %v", edges, want)
	}
	if c := chains[61]; c.ID != 60 || c.Position != 2 || c.Length != 3 || c.Below != 1 {
		t.Fatalf("61: %+v", c)
	}
	if c := chains[72]; c.Position != 2 || c.Below != 2 || c.Length != 3 {
		t.Fatalf("72: %+v", c)
	}
	if c := chains[51]; c.ID != 0 || c.Position != 1 || c.Length != 1 {
		t.Fatalf("51 hangs from a long-lived branch: %+v", c)
	}
}

// A protected branch is long-lived; positions count the longest way down; a cycle makes no chain.
func TestChainPositionsAndCycles(t *testing.T) {
	nodes := []Node{node(1, "a", "main"), node(2, "b", "a"), node(3, "c", "b"), node(4, "d", "c"), node(5, "stable", "main"), node(6, "e", "stable")}
	chains := Chains(nodes, map[int64]RepoInfo{1: {DefaultBranch: "main", Protected: map[string]bool{"stable": true}}}, longLived)
	if chains[4].Position != 4 || chains[4].Below != 3 || chains[1].Length != 4 || chains[6].ID != 0 {
		t.Fatalf("chains %+v", chains)
	}
	cycle := []Node{node(1, "a", "b"), node(2, "b", "a")}
	for id, c := range Chains(cycle, map[int64]RepoInfo{1: {DefaultBranch: "main"}}, longLived) {
		if c.ID != 0 {
			t.Fatalf("%d is in a chain despite the cycle: %+v", id, c)
		}
	}
}

// simulator runs resolution the way the service does, event after event: it remembers where anchors point, where
// pull requests are, and which groups are active, creating and archiving groups as resolution says.
type simulator struct {
	assertions map[string]simAssertion // by anchor; the latest version of each
	excluded   map[int64]map[int64]bool
	current    map[int64]int64
	anchors    map[string]int64
	groups     map[int64]time.Time
	prs        []int64
	next       int64
	clock      time.Time
}

type simAssertion struct {
	seen      time.Time
	kind      string
	prs       []int64
	id        int64
	authority int
}

func newSimulator(prs ...int64) *simulator {
	return &simulator{assertions: map[string]simAssertion{}, excluded: map[int64]map[int64]bool{}, current: map[int64]int64{},
		anchors: map[string]int64{}, groups: map[int64]time.Time{}, prs: prs, clock: t0}
}

// assert records an assertion under an anchor — replacing the previous one there — and resolves.
func (s *simulator) assert(anchor string, authority int, kind string, prs ...int64) {
	s.clock = s.clock.Add(time.Minute)
	seen := s.clock
	if old, ok := s.assertions[anchor]; ok {
		seen = old.seen
	}
	s.next++
	s.assertions[anchor] = simAssertion{seen: seen, kind: kind, prs: prs, id: s.next, authority: authority}
	s.resolve()
}

func (s *simulator) resolve() {
	b := &board{PRs: s.prs, Claims: map[int64][]claim{}, Excluded: s.excluded, Current: s.current, Anchors: s.anchors, Groups: s.groups}
	for anchor, a := range s.assertions {
		for _, pr := range a.prs {
			b.Claims[pr] = append(b.Claims[pr], claim{Seen: a.seen, Anchor: anchor, Kind: a.kind, Assertion: a.id, Authority: a.authority})
		}
	}
	res := resolve(b)
	made := map[int64]int64{}
	for i := range res.NewGroups {
		s.clock = s.clock.Add(time.Second)
		s.next++
		made[-int64(i+1)] = s.next
		s.groups[s.next] = s.clock
	}
	real := func(g int64) int64 {
		if g < 0 {
			return made[g]
		}
		return g
	}
	for a, g := range res.Mapped {
		s.anchors[a] = real(g)
	}
	s.current = map[int64]int64{}
	for pr, p := range res.Member {
		s.current[pr] = real(p.Group)
	}
	held := map[int64]bool{}
	for _, g := range s.current {
		held[g] = true
	}
	for g := range s.groups {
		if !held[g] {
			delete(s.groups, g)
			for a, ag := range s.anchors {
				if ag == g {
					delete(s.anchors, a)
				}
			}
		}
	}
}

// partition is the grouping as sets of pull requests, sorted, ignoring group IDs.
func (s *simulator) partition() string {
	by := map[int64][]int64{}
	for pr, g := range s.current {
		by[g] = append(by[g], pr)
	}
	var sets []string
	for _, prs := range by {
		sort.Slice(prs, func(i, j int) bool { return prs[i] < prs[j] })
		sets = append(sets, fmt.Sprint(prs))
	}
	sort.Strings(sets)
	return strings.Join(sets, " ")
}

type event struct {
	anchor    string
	authority int
	kind      string
	prs       []int64
}

// permutations calls f with every order of events.
func permutations(events []event, f func([]event)) {
	var rec func(int)
	rec = func(k int) {
		if k == len(events) {
			f(append([]event(nil), events...))
			return
		}
		for i := k; i < len(events); i++ {
			events[k], events[i] = events[i], events[k]
			rec(k + 1)
			events[k], events[i] = events[i], events[k]
		}
	}
	rec(0)
}

// Resolution gives one outcome under every order in which the assertions arrive (G5, G14), for the cases of
// docs/design/checks/grouping.py and more: claims never chain into a union.
func TestArrivalOrder(t *testing.T) {
	for _, c := range []struct {
		name   string
		prs    []int64
		events []event
		want   string
	}{
		{"a branch {1, 2} and an issue {2, 3} share 2", []int64{1, 2, 3},
			[]event{{"branch:x", AuthorityStructural, KindBranch, []int64{1, 2}}, {"issue:9", AuthorityStructural, KindIssue, []int64{2, 3}}},
			"[1] [2 3]"},
		{"a–c by branch, c–d by issue, d–e by branch: c and e share nothing", []int64{1, 3, 4, 5},
			[]event{{"branch:one", AuthorityStructural, KindBranch, []int64{1, 3}}, {"issue:7", AuthorityStructural, KindIssue, []int64{3, 4}},
				{"branch:two", AuthorityStructural, KindBranch, []int64{4, 5}}},
			"[1] [3 4] [5]"},
		{"an external thread over a branch pair keeps the pair; a second thread takes one of them", []int64{1, 2, 3},
			[]event{{"branch:x", AuthorityStructural, KindBranch, []int64{1, 2}}, {"bridge:1/t1", AuthorityExternal, "", []int64{1, 2}},
				{"bridge:1/t2", AuthorityExternal, "", []int64{2, 3}}},
			""},
	} {
		outcomes := map[string][]string{}
		permutations(c.events, func(order []event) {
			s := newSimulator(c.prs...)
			var names []string
			for _, e := range order {
				s.assert(e.anchor, e.authority, e.kind, e.prs...)
				names = append(names, e.anchor)
			}
			p := s.partition()
			outcomes[p] = append(outcomes[p], strings.Join(names, ","))
		})
		if c.want == "" {
			// Between two external claims the newer wins, so the order may matter; only a union is wrong.
			for p := range outcomes {
				if strings.Contains(p, "[1 2 3]") {
					t.Errorf("%s: a union %s after %v", c.name, p, outcomes[p])
				}
			}
			continue
		}
		if len(outcomes) != 1 || outcomes[c.want] == nil {
			t.Errorf("%s: outcomes %v, want only %s", c.name, outcomes, c.want)
		}
	}
}

// Within one resolution, the order of the pull requests changes nothing.
func TestBatchOrder(t *testing.T) {
	claims := map[int64][]claim{
		1: {{Anchor: "branch:x", Kind: KindBranch, Authority: AuthorityStructural, Assertion: 1, Seen: t0}},
		2: {{Anchor: "branch:x", Kind: KindBranch, Authority: AuthorityStructural, Assertion: 1, Seen: t0},
			{Anchor: "issue:9", Kind: KindIssue, Authority: AuthorityStructural, Assertion: 2, Seen: t0.Add(time.Minute)}},
		3: {{Anchor: "issue:9", Kind: KindIssue, Authority: AuthorityStructural, Assertion: 2, Seen: t0.Add(time.Minute)}},
	}
	var first map[int64]placement
	for _, order := range [][]int64{{1, 2, 3}, {3, 2, 1}, {2, 3, 1}} {
		res := resolve(&board{PRs: order, Claims: claims, Current: map[int64]int64{}, Anchors: map[string]int64{}, Groups: map[int64]time.Time{}})
		if first == nil {
			first = res.Member
		} else if !reflect.DeepEqual(first, res.Member) {
			t.Fatalf("order %v: %v, want %v", order, res.Member, first)
		}
	}
}

// docs/design/03-grouping.md's worked example: a branch pair makes a group; a thread over both takes it over rather
// than making another; a third pull request with the branch name joins it; a member's exclusion stands against the
// branch claim.
func TestWorkedExample(t *testing.T) {
	s := newSimulator(12, 7, 4)
	s.assert("branch:ana/feat/schedule", AuthorityStructural, KindBranch, 12, 7)
	g := s.current[12]
	s.assert("bridge:1/thread", AuthorityExternal, "", 12, 7)
	if s.current[12] != g || s.current[7] != g || len(s.groups) != 1 {
		t.Fatalf("the thread made another group: %v, groups %v", s.current, s.groups)
	}
	s.assert("branch:ana/feat/schedule", AuthorityStructural, KindBranch, 12, 7, 4)
	if s.current[4] != g {
		t.Fatalf("mobile#4 did not join: %v", s.current)
	}
	s.excluded[4] = map[int64]bool{g: true}
	s.resolve()
	if _, grouped := s.current[4]; grouped {
		t.Fatalf("an exclusion was overridden: %v", s.current)
	}
}

// Authority (G3): manual beats external beats structural; manual and external ties go to the newest, structural ones
// to the stronger kind, then to the claim that keeps the pull request where it is (G4).
func TestWinningClaims(t *testing.T) {
	b := &board{Anchors: map[string]int64{"branch:x": 10, "issue:9": 20, "bridge:1/a": 10, "bridge:1/b": 20},
		Groups: map[int64]time.Time{10: t0, 20: t0.Add(time.Hour), 30: t0}, Current: map[int64]int64{}}
	manual := claim{Group: 30, Authority: AuthorityManual, Assertion: 1, Seen: t0}
	external := claim{Anchor: "bridge:1/a", Authority: AuthorityExternal, Assertion: 2, Seen: t0}
	newerExternal := claim{Anchor: "bridge:1/b", Authority: AuthorityExternal, Assertion: 3, Seen: t0.Add(time.Minute)}
	branch := claim{Anchor: "branch:x", Kind: KindBranch, Authority: AuthorityStructural, Assertion: 4, Seen: t0}
	issue := claim{Anchor: "issue:9", Kind: KindIssue, Authority: AuthorityStructural, Assertion: 5, Seen: t0.Add(time.Hour)}
	for _, c := range []struct {
		name    string
		x, y    claim
		current int64
		want    bool
	}{
		{"manual over external", manual, external, 0, true},
		{"external over structural", external, issue, 0, true},
		{"the newer external", newerExternal, external, 0, true},
		{"the stronger structural kind", issue, branch, 0, true},
		{"the stronger kind even against staying", issue, branch, 10, true},
	} {
		if got := b.better(c.x, c.y, c.current); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	sameKind := claim{Anchor: "branch:y", Kind: KindBranch, Authority: AuthorityStructural, Assertion: 6, Seen: t0.Add(-time.Hour)}
	b.Anchors["branch:y"] = 20
	if !b.better(branch, sameKind, 10) {
		t.Error("a tie did not keep the pull request where it is")
	}
	if !b.better(sameKind, branch, 0) {
		t.Error("a tie with nowhere to stay did not go to the older claim")
	}
}

// Structural signals: the same issue, the same branch name across repositories by one author, mutual references;
// never within a chain (G9), never for never-grouped names; weak signals only suggest (G10).
func TestSignals(t *testing.T) {
	web := func(n Node) Node { n.Repo, n.RepoForge, n.HeadRepo = 2, 102, 102; return n }
	a := node(1, "feat/schedule", "main")
	a.Author, a.ClosesIssues = 501, []facts.Ref{{Repo: 101, Number: 40}}
	b := web(node(2, "feat/schedule", "main"))
	b.Author, b.ClosesIssues = 501, []facts.Ref{{Repo: 101, Number: 40}}
	c := web(node(3, "feat/schedule", "main"))
	c.Author = 502 // another author: no branch signal
	d := node(4, "main", "release/1")
	d.Author = 501
	e := web(node(5, "main", "develop"))
	e.Author = 501 // the default branch name is never grouped
	stacked := node(6, "feat/schedule-2", "feat/schedule")
	stacked.Author, stacked.ClosesIssues = 501, []facts.Ref{{Repo: 101, Number: 40}}
	x := node(7, "fix/invoice-totals", "main")
	x.References = []facts.Ref{{Repo: 2, Number: 8}}
	y := web(node(8, "fix/invoice-rounding-totals", "main"))
	y.References = []facts.Ref{{Repo: 1, Number: 7}}
	z := web(node(9, "chore/invoice-totals-report", "main"))
	x.References = append(x.References, facts.Ref{Repo: 2, Number: 9})
	nodes := []Node{a, b, c, d, e, stacked, x, y, z}
	// Words in more than a fifth of the open pull requests' branch names are noise; unrelated pull requests make
	// "invoice" and "totals" rare enough to count.
	for i := int64(0); i < 10; i++ {
		filler := node(100+i, fmt.Sprintf("topic%d/change%d", i, i), "main")
		filler.Repo, filler.RepoForge, filler.HeadRepo = 3, 103, 103
		nodes = append(nodes, filler)
	}
	chains := Chains(nodes, map[int64]RepoInfo{1: {DefaultBranch: "main"}, 2: {DefaultBranch: "main"}, 3: {DefaultBranch: "main"}}, longLived)
	signals, weak := Signals(nodes, chains, []string{"main", "master", "develop", "dev", "trunk", "release/*", "hotfix/*"})
	got := map[string][]int64{}
	for _, s := range signals {
		got[s.Anchor] = s.PRs
	}
	want := map[string][]int64{
		"issue:101/40":             {1, 2},
		"branch:501/feat/schedule": {1, 2},
		"refs:7":                   {7, 8},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("signals %v, want %v", got, want)
	}
	if len(weak) != 1 || weak[0].PR != 7 || weak[0].Other != 9 {
		t.Fatalf("weak %+v", weak)
	}
}

// A structural claim that loses points to where it would have put the pull request, for a suggestion (G10).
func TestLostClaims(t *testing.T) {
	b := &board{PRs: []int64{1}, Current: map[int64]int64{1: 10}, Anchors: map[string]int64{"branch:x": 20, "bridge:1/t": 10},
		Groups: map[int64]time.Time{10: t0, 20: t0}, Claims: map[int64][]claim{1: {
			{Anchor: "bridge:1/t", Authority: AuthorityExternal, Assertion: 1, Seen: t0},
			{Anchor: "branch:x", Kind: KindBranch, Authority: AuthorityStructural, Assertion: 2, Seen: t0},
		}}}
	res := resolve(b)
	if res.Member[1].Group != 10 || len(res.Lost[1]) != 1 || res.Lost[1][0].Group != 20 {
		t.Fatalf("result %+v", res)
	}
}
