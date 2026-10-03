package bridges

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"time"
)

// posted is an assertion a bridge posted: its thread's time, its title, and its pull requests, as owner/name#number,
// in order.
type posted struct {
	Created time.Time `json:"created"`
	Title   string    `json:"title,omitempty"`
	PRs     []string  `json:"prs"`
}

// proposed is a proposal a bridge posted: its thread's time, its title, and its groups.
type proposed struct {
	Created time.Time `json:"created"`
	Title   string    `json:"title,omitempty"`
	Groups  []Group   `json:"groups"`
}

// answer is what the model answered about a thread: the thread's time; a digest of what the model was asked, the
// thread's text and pull requests, so that it is asked again only when they change; and the groups it found.
type answer struct {
	Created time.Time `json:"created"`
	Asked   string    `json:"asked"`
	Groups  []Group   `json:"groups"`
}

// memory is what a bridge remembers between rounds, by its threads' anchors. Seeded is set once it has taken in what
// the bridge had posted before Park ran it.
type memory struct {
	Posted   map[string]posted   `json:"posted"`
	Proposed map[string]proposed `json:"proposed,omitempty"`
	Answers  map[string]answer   `json:"answers,omitempty"`
	Seeded   bool                `json:"seeded,omitempty"`
}

func (m *memory) init() {
	if m.Posted == nil {
		m.Posted = map[string]posted{}
	}
	if m.Proposed == nil {
		m.Proposed = map[string]proposed{}
	}
	if m.Answers == nil {
		m.Answers = map[string]answer{}
	}
}

// poster posts what a round decides, under its bridge's anchors.
type poster interface {
	assert(ctx context.Context, anchor, title string, prs []string) (applied bool, err error)
	retract(ctx context.Context, anchor string) error
	propose(ctx context.Context, anchor, title string, groups []Group) (state string, err error)
	retractProposal(ctx context.Context, anchor string) error
}

// splitter says how a thread's pull requests divide into units of work.
type splitter interface {
	Split(ctx context.Context, text string, prs []string) ([]Group, error)
}

// roundOptions are what a round reads besides its threads: the time; how far back threads count; the watched
// repositories, as owner/name in lower case; and the model, if the bridge has one, asked about a thread that links
// ProposeFrom pull requests or more.
type roundOptions struct {
	Now         time.Time
	Covered     map[string]bool
	Model       splitter
	Lookback    time.Duration
	ProposeFrom int
}

// round brings the assertions and proposals the bridge posts in step with its chat's threads, read as how many
// messages, and returns how many it posted or retracted:
//
//   - A pull request belongs to the newest thread that links it; only watched repositories' count; a thread left with
//     fewer than two is nothing.
//   - Without a model, or below the size asked about, a thread is an assertion. Otherwise the model is asked about it,
//     once for each text and set of pull requests, and the groups it answers are proposed; an answer of none is
//     nothing, and a thread the model cannot be asked about is left as it is.
//   - An assertion or proposal is posted when it is new or its thread changed, since each post replaces the last
//     under its anchor and adds to its history.
//   - One whose thread is gone from the window is retracted; one whose thread aged out of it is forgotten, and left as
//     it is. A round that read no messages retracts nothing: a platform answers an account that cannot read a chat
//     with no messages, and that must not retract everything.
//
// What fails to post is logged and tried again next round; what the bridge remembers changes only for what posted.
func (m *memory) round(ctx context.Context, log *slog.Logger, threads []found, read int, p poster, o roundOptions) int {
	m.init()
	since := o.Now.Add(-o.Lookback)
	slices.SortStableFunc(threads, func(x, y found) int { return y.Created.Compare(x.Created) })
	claimed := map[string]bool{}
	asserted := map[string]posted{}
	proposals := map[string]proposed{}
	answers := map[string]answer{}
	held := map[string]bool{}
	var anchors []string
	for _, t := range threads {
		var prs []string
		for _, k := range t.PRs {
			repo, _, _ := strings.Cut(k, "#")
			if !o.Covered[strings.ToLower(repo)] || claimed[k] {
				continue
			}
			claimed[k] = true
			prs = append(prs, k)
		}
		if len(prs) < 2 {
			continue
		}
		anchors = append(anchors, t.Anchor)
		if o.Model == nil || len(prs) < o.ProposeFrom {
			asserted[t.Anchor] = posted{Created: t.Created, Title: t.Title, PRs: prs}
			continue
		}
		asked := digest(t.Text, prs)
		a, known := m.Answers[t.Anchor]
		if !known || a.Asked != asked {
			groups, err := o.Model.Split(ctx, t.Text, prs)
			if err != nil {
				log.WarnContext(ctx, "ask the model about a thread: it is left as it is until the model answers", "anchor", t.Anchor,
					"error", err)
				held[t.Anchor] = true
				if known {
					answers[t.Anchor] = a
				}
				continue
			}
			a = answer{Asked: asked, Groups: groups}
		}
		a.Created = t.Created
		answers[t.Anchor] = a
		if len(a.Groups) > 0 {
			proposals[t.Anchor] = proposed{Created: t.Created, Title: t.Title, Groups: a.Groups}
		}
	}

	changes := 0
	for _, anchor := range anchors {
		if w, ok := asserted[anchor]; ok {
			if was, ok := m.Posted[anchor]; ok && was.Title == w.Title && slices.Equal(was.PRs, w.PRs) {
				m.Posted[anchor] = w
			} else if applied, err := p.assert(ctx, anchor, w.Title, w.PRs); err != nil {
				log.WarnContext(ctx, "post an assertion", "anchor", anchor, "error", err)
			} else {
				if !applied {
					log.InfoContext(ctx, "Park recorded an assertion without applying it", "anchor", anchor, "prs", len(w.PRs))
				}
				m.Posted[anchor] = w
				changes++
			}
		}
		if w, ok := proposals[anchor]; ok {
			if was, ok := m.Proposed[anchor]; ok && was.Title == w.Title && reflect.DeepEqual(was.Groups, w.Groups) {
				m.Proposed[anchor] = w
			} else if state, err := p.propose(ctx, anchor, w.Title, w.Groups); err != nil {
				log.WarnContext(ctx, "post a proposal", "anchor", anchor, "error", err)
			} else {
				if state != "open" {
					log.InfoContext(ctx, "the bridge's thread has these groups proposed already, and a member decided them", "anchor", anchor,
						"state", state)
				}
				m.Proposed[anchor] = w
				changes++
			}
		}
	}

	retract := read > 0
	if !retract && (len(gone(m.Posted, asserted, held)) > 0 || len(gone(m.Proposed, proposals, held)) > 0) {
		log.WarnContext(ctx, "read no messages from the chat: retracting nothing, since the account may not be able to read it")
	}
	if retract {
		for _, anchor := range gone(m.Posted, asserted, held) {
			if m.Posted[anchor].Created.Before(since) {
				delete(m.Posted, anchor)
				continue
			}
			if err := p.retract(ctx, anchor); err != nil {
				log.WarnContext(ctx, "retract an assertion", "anchor", anchor, "error", err)
				continue
			}
			delete(m.Posted, anchor)
			changes++
		}
		for _, anchor := range gone(m.Proposed, proposals, held) {
			if m.Proposed[anchor].Created.Before(since) {
				delete(m.Proposed, anchor)
				continue
			}
			if err := p.retractProposal(ctx, anchor); err != nil {
				log.WarnContext(ctx, "retract a proposal", "anchor", anchor, "error", err)
				continue
			}
			delete(m.Proposed, anchor)
			changes++
		}
		m.Answers = answers
	} else {
		for anchor, a := range answers {
			m.Answers[anchor] = a
		}
	}
	return changes
}

// gone lists, in order, the anchors kept that a round no longer wants, except those it holds.
func gone[T any](kept map[string]T, wanted map[string]T, held map[string]bool) []string {
	var out []string
	for anchor := range kept {
		if _, ok := wanted[anchor]; !ok && !held[anchor] {
			out = append(out, anchor)
		}
	}
	slices.Sort(out)
	return out
}

// digest identifies what the model is asked about a thread: its text and its pull requests.
func digest(text string, prs []string) string {
	sum := sha256.Sum256([]byte(text + "\x00" + strings.Join(prs, "\n")))
	return hex.EncodeToString(sum[:])
}
