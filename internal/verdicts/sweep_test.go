package verdicts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/rules"
)

// reviewerState is one reviewer's state in the sweep: docs/design/checks/sweep.py.
type reviewerState struct {
	role, decision, thread string
	reviewed, requested    bool
	again, body            bool
}

func reviewerStates() []reviewerState {
	var out []reviewerState
	for _, role := range []string{"none", "team", "direct"} {
		out = append(out, reviewerState{role: role, decision: "none", thread: "none", requested: role == "direct"})
	}
	for _, decision := range []string{"none", "changes_requested", "approved"} {
		for _, thread := range []string{"none", "reviewer_last", "author_last"} {
			for _, requested := range []bool{false, true} {
				againValues := []bool{false}
				if decision == "changes_requested" {
					againValues = []bool{false, true}
				}
				bodyValues := []bool{false, true}
				if decision == "approved" {
					bodyValues = []bool{false}
				}
				for _, again := range againValues {
					for _, body := range bodyValues {
						out = append(out, reviewerState{role: "reviewed", reviewed: true, decision: decision, thread: thread,
							requested: requested, again: again, body: body})
					}
				}
			}
		}
	}
	return out
}

// sweepState is one state of the sweep, and the model it builds.
type sweepState struct {
	nudge                         *int
	checks                        string
	ben, carol                    reviewerState
	required, hours               int
	draft, canDismiss, newCommits bool
}

// sweepStates are every state, in the sweep's order: the last parameter varies fastest.
func sweepStates() []sweepState {
	r := reviewerStates()
	var out []sweepState
	for _, ben := range r {
		for _, carol := range r {
			for _, required := range []int{0, 1, 2} {
				for _, draft := range []bool{false, true} {
					for _, checks := range []string{"passing", "pending", "failing"} {
						for _, canDismiss := range []bool{true, false} {
							for _, newCommits := range []bool{false, true} {
								for _, hours := range []int{1, 30} {
									for _, nudge := range []*int{nil, ptr(5)} {
										out = append(out, sweepState{ben: ben, carol: carol, required: required, draft: draft, checks: checks,
											canDismiss: canDismiss, newCommits: newCommits, hours: hours, nudge: nudge})
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}

func (s sweepState) model() model {
	m := model{author: "ana", required: ptr(s.required), draft: s.draft, checks: s.checks, noDismiss: !s.canDismiss,
		requested: s.hours, nudged: s.nudge, reviews: map[string]modelReview{}, teams: map[string][]string{}}
	if s.checks == "pending" {
		m.checksDue = ptr(time.Duration(s.hours) * time.Hour)
	}
	for _, p := range []struct {
		name  string
		state reviewerState
	}{{"ben", s.ben}, {"carol", s.carol}} {
		st := p.state
		if st.role == "team" {
			m.teams["team-"+p.name] = []string{p.name}
		}
		if st.requested {
			m.direct = append(m.direct, p.name)
		}
		if st.reviewed {
			m.reviews[p.name] = modelReview{decision: st.decision, again: st.again, body: st.body, newCommits: s.newCommits,
				hours: s.hours, nudged: s.nudge}
		}
		if st.thread != "none" {
			last := "ana"
			if st.thread == "reviewer_last" {
				last = p.name
			}
			m.threads = append(m.threads, thread(p.name, last, "ana"))
		}
	}
	return m
}

var defaultRules = rules.Default()

// sweepOne evaluates one state: the record of the rule each person reaches, and what goes wrong in it, as sweep.py
// checks.
func sweepOne(s sweepState) (string, []string) {
	m := s.model()
	pr, c := m.facts()
	people := People(pr, c)
	var parts []Part
	for _, p := range people {
		parts = append(parts, Part{Part: compiled.Evaluate(p.Role, p.Input), Role: p.Role, PR: 1, Account: p.Account})
	}
	placed := Place(parts)
	var problems []string
	if placed.Fallback {
		problems = append(problems, "the ball needs the fallback")
	}
	states := map[string]reviewerState{"ben": s.ben, "carol": s.carol}
	for _, h := range placed.Holders {
		if h.Role != rules.Reviewer {
			continue
		}
		st := states[names[h.Account]]
		if st.decision == "approved" && !st.requested {
			problems = append(problems, "a reviewer who approved holds the ball without being asked again")
		}
		if st.decision == "changes_requested" && (s.canDismiss || !st.requested) {
			problems = append(problems, "a reviewer who requested changes holds the ball")
		}
		if h.Kind != rules.KindAct {
			problems = append(problems, "a reviewer holds the ball with a part that is not act")
		}
	}
	for _, answer := range []string{"yes", "no"} {
		for i, p := range people {
			if p.Role != rules.Reviewer || !p.Input.Fields["me.reviewed"].(bool) {
				continue
			}
			in := p.Input
			in.Judgements = map[string]string{"commits_address_my_comments": answer, "comments_blocking": answer}
			judged := compiled.Evaluate(p.Role, in)
			if judged.Kind != parts[i].Kind || judged.Action != parts[i].Action {
				problems = append(problems, "a model answer '"+answer+"' changes the reviewer's part")
			}
		}
	}
	author := parts[0]
	if name := defaultRules.Author[max(author.Rule, 1)-1].Name; (name == "Changes requested, all answered" ||
		name == "All answered, cannot dismiss") && people[0].Input.Fields["pr.unanswered_reviews"].(int64) > 0 {
		problems = append(problems, "'all answered' while a written review is unanswered")
	}
	records := make([]string, len(parts))
	for i, p := range parts {
		records[i] = names[p.Account] + ":" + itoa(p.Rule)
	}
	sort.Strings(records)
	return strings.Join(records, ","), problems
}

// Every state of docs/design/checks/sweep.py, through the real field computation and the default rule set: every
// person reaches the rule the design's model reaches — each chunk's digest is sweep.py's — and no state has what the
// sweep checks againValuest. Under the race detector, one chunk in sweepEvery is run.
func TestSweep(t *testing.T) {
	data, err := os.ReadFile("testdata/sweep.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Digests []string `json:"digests"`
		States  int      `json:"states"`
		Chunk   int      `json:"chunk"`
	}
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	states := sweepStates()
	if len(states) != want.States || len(want.Digests) != (want.States+want.Chunk-1)/want.Chunk {
		t.Fatalf("%d states, %d digests; sweep.py has %d and %d", len(states), len(want.Digests), want.States, len(want.Digests))
	}
	type result struct {
		digest   string
		records  []string
		problems []string
	}
	results := make([]result, len(want.Digests))
	var chunks []int
	for i := 0; i < len(want.Digests); i += sweepEvery {
		chunks = append(chunks, i)
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				var r result
				for _, s := range states[i*want.Chunk : min((i+1)*want.Chunk, len(states))] {
					record, problems := sweepOne(s)
					r.records = append(r.records, record)
					for _, p := range problems {
						r.problems = append(r.problems, fmt.Sprintf("%s: %+v", p, s))
					}
				}
				sum := sha256.Sum256([]byte(strings.Join(r.records, ";")))
				r.digest = hex.EncodeToString(sum[:])[:16]
				results[i] = r
			}
		}()
	}
	for _, i := range chunks {
		work <- i
	}
	close(work)
	wg.Wait()
	checked, problems := 0, 0
	for _, i := range chunks {
		r := results[i]
		checked += len(r.records)
		for _, p := range r.problems {
			if problems < 10 {
				t.Error(p)
			}
			problems++
		}
		if r.digest != want.Digests[i] {
			t.Fatalf("chunk %d reaches other rules than sweep.py's model; its first states: %v", i, r.records[:3])
		}
	}
	if problems > 0 {
		t.Fatalf("%d problems", problems)
	}
	t.Logf("%d states in %d chunks match sweep.py", checked, len(chunks))
}
