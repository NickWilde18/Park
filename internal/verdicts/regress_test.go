package verdicts

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/rules"
)

var names = func() map[int64]string {
	m := map[int64]string{}
	for name, id := range accounts {
		m[id] = name
	}
	return m
}()

// The units of docs/design/checks/regress.py, each with the side, the holders, and — where it matters — the holders'
// action its ball must have, placed through the real field computation and the default rule set.
func TestRegress(t *testing.T) {
	for _, c := range []struct {
		title   string
		unit    map[string]model
		side    rules.Role
		holders []string
		action  string
	}{
		{"A  the forge requires no approval; Ben is asked and has not reviewed",
			map[string]model{"api#12": {author: "ana", required: ptr(0), direct: []string{"ben"}}},
			rules.Reviewer, []string{"ben"}, ""},
		{"B  group: api#12 waits for Ben's first review; web#7 is a draft until api#12 merges",
			map[string]model{"api#12": {author: "ana", direct: []string{"ben"}}, "web#7": {author: "ana", draft: true}},
			rules.Reviewer, []string{"ben"}, ""},
		{"C  group, two authors: Ben's first review of api#12; Carol's web#7 has checks running",
			map[string]model{"api#12": {author: "ana", direct: []string{"ben"}},
				"web#7": {author: "carol", checks: "pending", direct: []string{"ben"}}},
			rules.Reviewer, []string{"ben"}, ""},
		{"D  Ben requests changes in the review body only, no inline threads",
			map[string]model{"api#12": {author: "ana", reviews: map[string]modelReview{"ben": {decision: "changes_requested", body: true}}}},
			rules.Author, []string{"ana"}, "address_comments"},
		{"E  Ben comments in the review body only; his request is used up; nobody else is asked",
			map[string]model{"api#12": {author: "ana", reviews: map[string]modelReview{"ben": {decision: "none", body: true}}}},
			rules.Author, []string{"ana"}, "address_comments"},
		{"E2 same, after Ana answered Ben's review: she hands it back",
			map[string]model{"api#12": {author: "ana", reviews: map[string]modelReview{"ben": {decision: "none", body: true, answered: true}}}},
			rules.Author, []string{"ana"}, "request_review"},
		{"F  Ben approves and leaves a new nit (his comment is the latest): Ana answers before merging",
			map[string]model{"api#12": {author: "ana", reviews: map[string]modelReview{"ben": {decision: "approved"}},
				threads: []modelThread{thread("ben", "ben")}}},
			rules.Author, []string{"ana"}, "address_comments"},
		{"G  code owner Dave's approval is required; Carol (not an owner) approved; Dave is asked",
			map[string]model{"api#12": {author: "ana", reviews: map[string]modelReview{"carol": {decision: "approved"}},
				direct: []string{"dave"}, codeOwners: []string{"dave"}}},
			rules.Reviewer, []string{"dave"}, ""},
		{"H1 Ana replied last in Ben's thread and re-requested Ben's team {ben, eve}, not Ben",
			map[string]model{"api#12": {author: "ana", reviews: map[string]modelReview{"ben": {decision: "none"}},
				threads: []modelThread{thread("ben", "ana", "ana")}, teams: map[string][]string{"backend": {"ben", "eve"}}}},
			rules.Author, []string{"ana"}, "request_review"},
		{"H2 same, the team is {ben} only",
			map[string]model{"api#12": {author: "ana", reviews: map[string]modelReview{"ben": {decision: "none"}},
				threads: []modelThread{thread("ben", "ana", "ana")}, teams: map[string][]string{"backend": {"ben"}}}},
			rules.Author, []string{"ana"}, "request_review"},
		{"H3 only a team is asked, and its known members are only the author",
			map[string]model{"api#12": {author: "ana", teams: map[string][]string{"owners": {"ana"}}}},
			rules.Author, []string{"ana"}, "request_review"},
		{"J1 Ben requested changes; Ana, who cannot dismiss, answered everything and has not asked again",
			map[string]model{"api#12": {author: "ana", noDismiss: true, reviews: map[string]modelReview{"ben": {decision: "changes_requested"}},
				threads: []modelThread{thread("ben", "ana", "ana")}}},
			rules.Author, []string{"ana"}, "request_review"},
		{"J2 same, after Ana asked Ben again",
			map[string]model{"api#12": {author: "ana", noDismiss: true, reviews: map[string]modelReview{"ben": {decision: "changes_requested"}},
				threads: []modelThread{thread("ben", "ana", "ana")}, direct: []string{"ben"}}},
			rules.Reviewer, []string{"ben"}, ""},
		{"K  required checks have been running for five hours",
			map[string]model{"api#12": {author: "ana", checks: "pending", checksDue: ptr(5 * time.Hour), direct: []string{"ben"}}},
			rules.Author, []string{"ana"}, "fix_checks"},
		{"L  approved, but the forge still blocks merging for another reason",
			map[string]model{"api#12": {author: "ana", reviews: map[string]modelReview{"ben": {decision: "approved"}},
				forgeBlocks: []string{"other"}}},
			rules.Author, []string{"ana"}, "unblock"},
		{"M  two approvals required: Ben approved, Carol is asked",
			map[string]model{"api#12": {author: "ana", required: ptr(2), reviews: map[string]modelReview{"ben": {decision: "approved"}},
				direct: []string{"carol"}}},
			rules.Reviewer, []string{"carol"}, ""},
		{"N  Ben reviewed again and resolved his threads, but his request for changes stands",
			map[string]model{"api#12": {author: "ana", reviews: map[string]modelReview{"ben": {decision: "changes_requested", again: true}}}},
			rules.Author, []string{"ana"}, "request_review"},
		{"O  group: api#12 merged with an open thread; web#7 waits for Ben's first review",
			map[string]model{"api#12": {author: "ana", closed: true, threads: []modelThread{thread("ben", "ben")}},
				"web#7": {author: "ana", direct: []string{"ben"}}},
			rules.Reviewer, []string{"ben"}, ""},
		{"P  single draft: the author holds it",
			map[string]model{"api#12": {author: "ana", draft: true, direct: []string{"ben"}}},
			rules.Author, []string{"ana"}, ""},
		{"Q  single pull request, checks running, Ben asked again: he waits for green checks",
			map[string]model{"api#12": {author: "ana", checks: "pending", reviews: map[string]modelReview{"ben": {decision: "none"}},
				direct: []string{"ben"}}},
			rules.Author, []string{"ana"}, ""},
		{"R1 Ana dismissed Ben's request for changes, his threads resolved, and asked him again; Carol and Dave approved, " +
			"and two are required: Ana merges",
			map[string]model{"api#12": {author: "ana", required: ptr(2), direct: []string{"ben"},
				reviews: map[string]modelReview{"ben": {decision: "none"}, "carol": {decision: "approved"}, "dave": {decision: "approved"}}}},
			rules.Author, []string{"ana"}, "merge"},
		{"R2 same, but Ben's thread is still open, Ana replied last: Ben looks again",
			map[string]model{"api#12": {author: "ana", required: ptr(2), direct: []string{"ben"}, threads: []modelThread{thread("ben", "ana", "ana")},
				reviews: map[string]modelReview{"ben": {decision: "none"}, "carol": {decision: "approved"}, "dave": {decision: "approved"}}}},
			rules.Reviewer, []string{"ben"}, "review"},
		{"R3 Ben approved, and Ana asked him again; Carol approved too, and two are required: Ben looks again",
			map[string]model{"api#12": {author: "ana", required: ptr(2), direct: []string{"ben"},
				reviews: map[string]modelReview{"ben": {decision: "approved"}, "carol": {decision: "approved"}}}},
			rules.Reviewer, []string{"ben"}, "review"},
		{"R4 as R1, but code owner Eve is asked and has not approved: Ben, asked again, holds it, and so does Eve",
			map[string]model{"api#12": {author: "ana", required: ptr(2), direct: []string{"ben", "eve"}, codeOwners: []string{"eve"},
				reviews: map[string]modelReview{"ben": {decision: "none"}, "carol": {decision: "approved"}, "dave": {decision: "approved"}}}},
			rules.Reviewer, []string{"ben", "eve"}, "review"},
		{"R5 as R1, but a thread Carol started waits for Ben's reply, Ana replied last: Ben looks again",
			map[string]model{"api#12": {author: "ana", required: ptr(2), direct: []string{"ben"}, threads: []modelThread{thread("carol", "ana", "ben")},
				reviews: map[string]modelReview{"ben": {decision: "none"}, "carol": {decision: "approved"}, "dave": {decision: "approved"}}}},
			rules.Reviewer, []string{"ben"}, "review"},
	} {
		parts, got := unit(t, c.unit)
		var holders []string
		for _, h := range got.Holders {
			if !slices.Contains(holders, names[h.Account]) {
				holders = append(holders, names[h.Account])
			}
		}
		sort.Strings(holders)
		ok := got.Side == c.side && !got.Fallback && slices.Equal(holders, c.holders)
		for _, h := range got.Holders {
			ok = ok && (c.action == "" || h.Action == c.action)
		}
		if !ok {
			t.Errorf("%s\n  want %s: %v %s | got %s (fallback %v): %v\n  parts: %s", c.title, c.side, c.holders, c.action, got.Side,
				got.Fallback, holders, describe(parts))
		}
	}
}

// A reviewer asked again is optional once the pull request has every approval it needs and nothing waits on them
// (reviewer 13). Ana dismissed Ben's request for changes, and Carol and Dave approved, two being required. Asked again,
// with nothing of his open, Ben has nothing to do. A thread he started that is still open keeps him to look again,
// though it waits on Ana, who answers it first; so does a thread of Carol's that waits for his reply, and so does an
// approval still missing, with only Carol's in. Not asked again, he waits for the author, as before.
func TestAskedAgainIsOptional(t *testing.T) {
	both := map[string]modelReview{"ben": {decision: "none"}, "carol": {decision: "approved"}, "dave": {decision: "approved"}}
	carol := map[string]modelReview{"ben": {decision: "none"}, "carol": {decision: "approved"}}
	for _, c := range []struct {
		reviews map[string]modelReview
		name    string
		kind    string
		direct  []string
		threads []modelThread
		rule    int
	}{
		{both, "asked again, nothing of his open", rules.KindIdle, []string{"ben"}, nil, 13},
		{both, "asked again, his own thread open, waiting on Ana", rules.KindAct, []string{"ben"}, []modelThread{thread("ben", "ben")}, 14},
		{both, "asked again, Carol's thread waiting for his reply", rules.KindAct, []string{"ben"}, []modelThread{thread("carol", "ana", "ben")}, 14},
		{carol, "asked again, an approval still missing", rules.KindAct, []string{"ben"}, nil, 14},
		{both, "not asked again", rules.KindIdle, nil, nil, 20},
	} {
		parts, _ := unit(t, map[string]model{"api#12": {author: "ana", required: ptr(2), direct: c.direct, threads: c.threads, reviews: c.reviews}})
		i := slices.IndexFunc(parts, func(p Part) bool { return names[p.Account] == "ben" })
		if i < 0 || parts[i].Rule != c.rule || parts[i].Kind != c.kind {
			t.Errorf("%s: Ben's part %s, want rule %d, %s", c.name, describe(parts), c.rule, c.kind)
		}
	}
}

func describe(parts []Part) string {
	var out []string
	for _, p := range parts {
		out = append(out, names[p.Account]+" #"+itoa(p.Rule)+" "+p.Kind+" "+p.Action)
	}
	return strings.Join(out, "; ")
}

// The placement order is the chapter's numbered list: a side, and the kind of part that places the ball there.
func TestPlacementOrderMatchesTheChapter(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "design", "04-verdicts.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := strings.Cut(string(data), "## Placing the ball")
	body, _, _ = strings.Cut(body, "\n## ")
	step := regexp.MustCompile("(?m)^\\d\\. (?:Otherwise, )?\\*\\*[Tt]he (author|reviewer) side\\*\\*, if any (?:author|reviewer) has an? `(\\w+)` part")
	var want []string
	for _, m := range step.FindAllStringSubmatch(body, -1) {
		want = append(want, m[1]+" "+m[2])
	}
	var got []string
	for _, s := range steps {
		got = append(got, string(s.side)+" "+s.kind)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("placement order %v; the chapter says %v", got, want)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
