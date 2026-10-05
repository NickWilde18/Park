package verdicts

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/NickWilde18/Park/internal/rules"
)

// Verdict is one conclusion for one unit: docs/design/04-verdicts.md#what-a-verdict-holds.
type Verdict struct {
	CreatedAt   time.Time
	EvaluatedAt time.Time
	// Seqs are the fact sequence numbers it was computed from, by pull request.
	Seqs map[int64]int64
	// Unit is "group:<id>", or "pr:<id>" for a pull request in no group.
	Unit string
	// Side is where the ball is; empty when every pull request is merged or closed.
	Side rules.Role
	// Closed is "merged" or "closed" once every pull request of the unit is.
	Closed    string
	Holders   []Holder
	Parts     []Part
	Reminders []Reminder
	Inputs    []Input
	ID        int64
	// RuleVersion is the published rule set that produced it.
	RuleVersion int
	Current     bool
	Fallback    bool
}

// Holder is a person holding the ball, with their actions, pull request by pull request.
type Holder struct {
	Since   time.Time  `json:"since"`
	Login   string     `json:"login"`
	Name    string     `json:"name"`
	Parts   []HeldPart `json:"parts"`
	Forge   int64      `json:"forge"`
	Account int64      `json:"account"`
}

// HeldPart is what a holder has to do on one pull request.
type HeldPart struct {
	Kind     string   `json:"kind"`
	Action   string   `json:"action,omitempty"`
	Priority string   `json:"priority"`
	Reason   string   `json:"reason"`
	Targets  []string `json:"targets,omitempty"`
	PR       int64    `json:"pr"`
}

// Reminder is a reminder offered to a person waiting on the holders: nudge them.
type Reminder struct {
	Login   string `json:"login"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
	PR      int64  `json:"pr"`
	Forge   int64  `json:"forge"`
	Account int64  `json:"account"`
}

// Input is what one person's list read.
type Input struct {
	Fields  map[string]any `json:"fields"`
	Login   string         `json:"login"`
	Name    string         `json:"name"`
	Role    rules.Role     `json:"role"`
	PR      int64          `json:"pr"`
	Forge   int64          `json:"forge"`
	Account int64          `json:"account"`
}

func key(forge, account int64) string {
	return strconv.FormatInt(forge, 10) + "/" + strconv.FormatInt(account, 10)
}

// holders groups the parts that placed the ball by person. A person who held it in the previous verdict holds it
// since then.
func holders(placed []Part, previous *Verdict, now time.Time) []Holder {
	since := map[string]time.Time{}
	if previous != nil {
		for _, h := range previous.Holders {
			since[key(h.Forge, h.Account)] = h.Since
		}
	}
	var out []Holder
	index := map[string]int{}
	for _, p := range placed {
		k := key(p.Forge, p.Account)
		i, ok := index[k]
		if !ok {
			at, held := since[k]
			if !held {
				at = now
			}
			index[k], i = len(out), len(out)
			out = append(out, Holder{Since: at, Login: p.Login, Name: p.Name, Forge: p.Forge, Account: p.Account})
		}
		out[i].Parts = append(out[i].Parts, HeldPart{Kind: p.Kind, Action: p.Action, Priority: p.Priority, Reason: p.Reason,
			Targets: p.Targets, PR: p.PR})
	}
	return out
}

// visible is what a person sees of a verdict: where the ball is, who holds it and what for, the reminders, every
// part, as a unit's page shows it, and what the board reads of each person's inputs — their review, whether one is
// asked of them directly, and the threads waiting on them. A new verdict is stored only when it changes (V8), so that
// a change that moves no ball, such as a reviewer approving while the author holds it, is stored all the same; one
// that changes only inputs nobody is shown, such as the hours since a request, is not.
func (v *Verdict) visible() string {
	type holder struct {
		Parts   []HeldPart `json:"parts"`
		Forge   int64      `json:"forge"`
		Account int64      `json:"account"`
	}
	hs := make([]holder, len(v.Holders))
	for i, h := range v.Holders {
		hs[i] = holder{Parts: h.Parts, Forge: h.Forge, Account: h.Account}
	}
	type read struct {
		Decision  any        `json:"decision"`
		Requested any        `json:"requested"`
		Threads   any        `json:"threads"`
		Role      rules.Role `json:"role"`
		PR        int64      `json:"pr"`
		Forge     int64      `json:"forge"`
		Account   int64      `json:"account"`
	}
	reads := make([]read, len(v.Inputs))
	for i, in := range v.Inputs {
		reads[i] = read{Decision: in.Fields["me.decision"], Requested: in.Fields["me.requested"], Threads: in.Fields["me.threads_on_me"],
			Role: in.Role, PR: in.PR, Forge: in.Forge, Account: in.Account}
	}
	data, _ := json.Marshal(struct {
		Holders   []holder   `json:"holders"`
		Reminders []Reminder `json:"reminders"`
		Parts     []Part     `json:"parts"`
		Reads     []read     `json:"reads"`
		Side      rules.Role `json:"side"`
		Closed    string     `json:"closed"`
		Fallback  bool       `json:"fallback"`
	}{hs, orEmpty(v.Reminders), orEmpty(v.Parts), reads, v.Side, v.Closed, v.Fallback})
	return string(data)
}
