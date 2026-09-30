package verdicts

import (
	"github.com/NickWilde18/Park/internal/rules"
)

// Part is one person's part on one pull request, with its path through the rules.
type Part struct {
	rules.Part
	Login   string     `json:"login"`
	Name    string     `json:"name"`
	Role    rules.Role `json:"role"`
	PR      int64      `json:"pr"`
	Forge   int64      `json:"forge"`
	Account int64      `json:"account"`
}

// Placement is where a unit's ball is.
type Placement struct {
	Side rules.Role
	// Holders are the parts that placed the ball; on the fallback, every author's.
	Holders  []Part
	Fallback bool
}

// steps are the placement order of docs/design/04-verdicts.md#placing-the-ball: a side, and the kind of part that
// places the ball there (V3).
var steps = []struct {
	side rules.Role
	kind string
}{
	{rules.Author, rules.KindAct},
	{rules.Reviewer, rules.KindAct},
	{rules.Author, rules.KindHold},
	{rules.Author, rules.KindFinish},
}

// Place places a unit's ball from every part in it: author work first, then reviewer work, then an author who only
// waits, then the author's last step. With none of those, the ball is on the fallback: the author side, held by the
// authors, and flagged.
func Place(parts []Part) Placement {
	for _, st := range steps {
		var holders []Part
		for _, p := range parts {
			if p.Role == st.side && p.Kind == st.kind {
				holders = append(holders, p)
			}
		}
		if len(holders) > 0 {
			return Placement{Side: st.side, Holders: holders}
		}
	}
	var authors []Part
	for _, p := range parts {
		if p.Role == rules.Author {
			authors = append(authors, p)
		}
	}
	return Placement{Side: rules.Author, Holders: authors, Fallback: true}
}
