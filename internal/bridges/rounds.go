package bridges

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/forges"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/work"
)

// KindRound is the work of one round of a Park-run bridge, keyed by the bridge's ID: one runs at a time, and each
// queues the next when it ends.
const KindRound = "bridges.round"

// keptRounds is how long a bridge's rounds are kept: the dashboard shows the last.
const keptRounds = 7 * 24 * time.Hour

var (
	errDisabled = fail.New(fail.Conflict, "bridge_disabled", "This bridge is switched off; an admin switches it on.")
	errLapsed   = fail.New(fail.Conflict, "signin_lapsed", "The bridge's account can no longer be used; connect it again.")
	errNoChat   = fail.New(fail.Conflict, "no_chat", "Choose the chat the bridge reads first.")
)

// Register gives the runner the bridges' rounds.
func (s *Service) Register(r *work.Runner) {
	r.Handle(KindRound, s.runRound)
}

// Start queues a round of every Park-run bridge that is on: when its last round ended and a period passed, or now. A
// round that is queued already stays as it is, unless this is earlier.
func (s *Service) Start(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT b.id, b.every_minutes, (SELECT max(ended_at) FROM bridge_round r WHERE r.bridge_id = b.id)
			FROM bridge b WHERE b.plugin IS NOT NULL AND b.enabled`)
		if err != nil {
			return err
		}
		type due struct {
			at time.Time
			id int64
		}
		var all []due
		for rows.Next() {
			var id int64
			var every int
			var last *time.Time
			if err := rows.Scan(&id, &every, &last); err != nil {
				rows.Close()
				return err
			}
			var at time.Time
			if last != nil && last.Add(time.Duration(every)*time.Minute).After(s.now()) {
				at = last.Add(time.Duration(every) * time.Minute)
			}
			all = append(all, due{id: id, at: at})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, d := range all {
			if err := work.Enqueue(ctx, tx, KindRound, strconv.FormatInt(d.id, 10), d.at); err != nil {
				return err
			}
		}
		return nil
	})
}

// Sync asks for a round of a Park-run bridge now, as a member: a round queued for later runs now, and one asked for
// while a round runs runs again when that one ends. The bridge must be ready to run: on, its plugin on, its
// credentials complete, its account connected and not lapsed, and its chat chosen.
func (s *Service) Sync(ctx context.Context, id int64) error {
	b, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.ready(ctx, b); err != nil {
		return err
	}
	return s.schedule(ctx, s.pool, id, time.Time{})
}

// ready reports why a bridge cannot run a round, or nil when it can.
func (s *Service) ready(ctx context.Context, b Bridge) error {
	switch {
	case b.Plugin == "":
		return errExternal
	case !b.Enabled:
		return errDisabled
	}
	p, err := s.usable(ctx, s.pool, b.Plugin)
	if err != nil {
		return err
	}
	if _, err := s.credentials(ctx, s.pool, b.ID, p); err != nil {
		return err
	}
	switch {
	case b.Account == nil:
		return errNoAccount
	case b.Account.LapsedAt != nil:
		return errLapsed
	case b.Chat.ID == "":
		return errNoChat
	}
	return nil
}

// schedule queues a bridge's round at a time, through q; the zero time is now, as the work queue tells it.
func (s *Service) schedule(ctx context.Context, q work.DB, id int64, at time.Time) error {
	return work.Enqueue(ctx, q, KindRound, strconv.FormatInt(id, 10), at)
}

// scheduleNow queues a round of a Park-run bridge now, after a change that may make it ready, or run it otherwise;
// one that is not ready does nothing when it runs.
func (s *Service) scheduleNow(ctx context.Context, b Bridge) error {
	if b.Plugin == "" || !b.Enabled {
		return nil
	}
	return s.schedule(ctx, s.pool, b.ID, time.Time{})
}

// runRound runs one round of a bridge, records it, and queues the next. A bridge that is not ready has no round: what
// makes it ready queues one. A failed round is recorded with its reason and the next queued as for any round, except
// for a lapsed sign-in, which waits for the account to be connected again. A round cut short as Park stops is neither
// recorded nor queued again: the work queue runs it again.
func (s *Service) runRound(ctx context.Context, item work.Item) error {
	id, err := strconv.ParseInt(item.Key, 10, 64)
	if err != nil {
		return nil
	}
	b, err := s.Get(ctx, id)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.ready(ctx, b) != nil {
		return nil
	}
	started := s.now()
	lookback := time.Duration(b.LookbackDays) * 24 * time.Hour
	var threads []Thread
	count := 0
	err = s.withAccount(ctx, id, "threads", func(p Plugin, creds map[string]string, tokens json.RawMessage) (json.RawMessage, error) {
		ts, n, renewed, err := p.threads(ctx, creds, tokens, b.Chat.ID, started.Add(-lookback))
		threads, count = ts, n
		return renewed, err
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		reason := "internal"
		if f, ok := fail.As(err); ok {
			reason = f.Code
		} else {
			s.log.ErrorContext(ctx, "a bridge's round failed", "bridge", id, "error", err)
		}
		return s.finish(ctx, b, nil, Round{StartedAt: started, Outcome: "failed", Reason: reason}, reason != errLapsed.Code)
	}
	links, covered, err := s.watched(ctx)
	if err != nil {
		return err
	}
	mem, err := s.memoryOf(ctx, b.ID)
	if err != nil {
		return err
	}
	o := roundOptions{Now: started, Covered: covered, Lookback: lookback, ProposeFrom: b.ProposeFrom}
	if b.ProposeFrom > 0 {
		m, err := s.modelOf(ctx, b)
		if err != nil {
			return err
		}
		if m != nil {
			o.Model = m
		}
	}
	changes := mem.round(ctx, s.log.With("bridge", id), read(threads, links), count, groupsPoster{groups: s.groups, bridge: id}, o)
	return s.finish(ctx, b, &mem, Round{StartedAt: started, Outcome: "ok", Read: count, Changes: changes}, true)
}

// finish records a round, with what the bridge remembers after it if it got so far, forgets rounds older than a week,
// and queues the next round a period after this one, when next is set.
func (s *Service) finish(ctx context.Context, b Bridge, mem *memory, r Round, next bool) error {
	r.EndedAt = s.now()
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if mem != nil {
			data, err := json.Marshal(mem)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE bridge SET state = $2 WHERE id = $1", b.ID, data); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO bridge_round (bridge_id, started_at, ended_at, read, changes, outcome, reason)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, b.ID, r.StartedAt, r.EndedAt, r.Read, r.Changes, r.Outcome, r.Reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM bridge_round WHERE bridge_id = $1 AND ended_at < $2", b.ID, r.EndedAt.Add(-keptRounds)); err != nil {
			return err
		}
		if !next {
			return nil
		}
		return s.schedule(ctx, tx, b.ID, r.EndedAt.Add(time.Duration(b.EveryMinutes)*time.Minute))
	})
}

// watched returns how pull requests' addresses read on the forges Park watches repositories of, and those
// repositories, as owner/name in lower case.
func (s *Service) watched(ctx context.Context) ([]pullLinks, map[string]bool, error) {
	rows, err := s.pool.Query(ctx, "SELECT DISTINCT f.kind, f.base_address FROM forge f JOIN repo r ON r.forge_id = f.id WHERE r.watched")
	if err != nil {
		return nil, nil, err
	}
	var links []pullLinks
	for rows.Next() {
		var kind, base string
		if err := rows.Scan(&kind, &base); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if k, ok := forges.KindOf(kind); ok && k.PullPattern != nil {
			links = append(links, newPullLinks(k.PullPattern(base)))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = s.pool.Query(ctx, "SELECT lower(owner || '/' || name) FROM repo WHERE watched")
	if err != nil {
		return nil, nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, err
	}
	covered := map[string]bool{}
	for _, n := range names {
		covered[n] = true
	}
	return links, covered, nil
}

// modelOf returns the model a bridge asks, or nil when it has none.
func (s *Service) modelOf(ctx context.Context, b Bridge) (*model, error) {
	if b.Model.URL == "" {
		return nil, nil
	}
	var sealed []byte
	if err := s.pool.QueryRow(ctx, "SELECT model_key FROM bridge WHERE id = $1", b.ID).Scan(&sealed); err != nil {
		return nil, err
	}
	key := ""
	if len(sealed) > 0 {
		data, err := s.box.Open(sealed, modelKeyContext(b.ID))
		if err != nil {
			return nil, err
		}
		key = string(data)
	}
	return newModel(b.Model.URL, b.Model.Name, key, time.Duration(b.Model.TimeoutSeconds)*time.Second), nil
}

// memoryOf reads what a bridge remembers. The first time Park runs a bridge, it takes in the bridge's live
// assertions and proposals, so that a bridge moved in from outside posts again only what changed. A thread's time is
// then when Park first saw it under its anchor, until a round reads the thread itself.
func (s *Service) memoryOf(ctx context.Context, id int64) (memory, error) {
	var mem memory
	var raw []byte
	if err := s.pool.QueryRow(ctx, "SELECT state FROM bridge WHERE id = $1", id).Scan(&raw); err != nil {
		return mem, err
	}
	if err := json.Unmarshal(raw, &mem); err != nil {
		return mem, err
	}
	mem.init()
	if mem.Seeded {
		return mem, nil
	}
	mem.Seeded = true
	names := map[int64]string{}
	rows, err := s.pool.Query(ctx, "SELECT id, owner || '/' || name FROM repo")
	if err != nil {
		return mem, err
	}
	for rows.Next() {
		var repo int64
		var name string
		if err := rows.Scan(&repo, &name); err != nil {
			rows.Close()
			return mem, err
		}
		names[repo] = name
	}
	rows.Close()
	keys := func(raw []byte) ([]string, error) {
		var members []struct {
			Repo   int64 `json:"repo"`
			Number int   `json:"number"`
		}
		if err := json.Unmarshal(raw, &members); err != nil {
			return nil, err
		}
		out := make([]string, 0, len(members))
		for _, m := range members {
			out = append(out, names[m.Repo]+"#"+strconv.Itoa(m.Number))
		}
		return out, nil
	}
	source := "bridge:" + strconv.FormatInt(id, 10)
	rows, err = s.pool.Query(ctx, `SELECT anchor, coalesce(title, ''), members, seen_at FROM assertion
		WHERE source = $1 AND retracted_at IS NULL AND anchor IS NOT NULL AND members IS NOT NULL`, source)
	if err != nil {
		return mem, err
	}
	for rows.Next() {
		var anchor, title string
		var members []byte
		var seen time.Time
		if err := rows.Scan(&anchor, &title, &members, &seen); err != nil {
			rows.Close()
			return mem, err
		}
		prs, err := keys(members)
		if err != nil {
			rows.Close()
			return mem, err
		}
		mem.Posted[anchor] = posted{Created: seen, Title: title, PRs: prs}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return mem, err
	}
	rows, err = s.pool.Query(ctx, `SELECT p.anchor, coalesce(p.title, ''), p.created_at, coalesce(g.name, ''), g.members
		FROM proposal p JOIN proposal_group g ON g.proposal_id = p.id
		WHERE p.source = $1 AND p.retracted_at IS NULL ORDER BY p.id, g.position`, source)
	if err != nil {
		return mem, err
	}
	defer rows.Close()
	for rows.Next() {
		var anchor, title, name string
		var created time.Time
		var members []byte
		if err := rows.Scan(&anchor, &title, &created, &name, &members); err != nil {
			return mem, err
		}
		prs, err := keys(members)
		if err != nil {
			return mem, err
		}
		p := mem.Proposed[anchor]
		p.Created, p.Title = created, title
		p.Groups = append(p.Groups, Group{Name: name, PRs: prs})
		mem.Proposed[anchor] = p
	}
	return mem, rows.Err()
}

// groupsPoster posts a round's decisions into Park's grouping, as the bridge does through the API, with its anchors
// as their own evidence. One that is gone already is retracted.
type groupsPoster struct {
	groups *grouping.Service
	bridge int64
}

func (g groupsPoster) assert(ctx context.Context, anchor, title string, prs []string) (bool, error) {
	res, err := g.groups.Assert(ctx, g.bridge, "", grouping.Assertion{Anchor: anchor, Title: title, Evidence: []string{anchor}, PRs: refs(prs)})
	return res.Applied, err
}

func (g groupsPoster) retract(ctx context.Context, anchor string) error {
	return goneAlready(g.groups.Retract(ctx, g.bridge, anchor))
}

func (g groupsPoster) propose(ctx context.Context, anchor, title string, groups []Group) (string, error) {
	p := grouping.Proposal{Anchor: anchor, Title: title, Evidence: []string{anchor}}
	for _, gr := range groups {
		p.Groups = append(p.Groups, grouping.ProposedGroup{Name: gr.Name, PRs: refs(gr.PRs)})
	}
	res, err := g.groups.Propose(ctx, g.bridge, "", p)
	return res.State, err
}

func (g groupsPoster) retractProposal(ctx context.Context, anchor string) error {
	return goneAlready(g.groups.RetractProposal(ctx, g.bridge, anchor))
}

// goneAlready treats a retraction of what is not live as done.
func goneAlready(err error) error {
	if f, ok := fail.As(err); ok && f.Kind == fail.NotFound {
		return nil
	}
	return err
}

// refs turns pull requests named owner/name#number into the grouping's references.
func refs(keys []string) []grouping.PRRef {
	out := make([]grouping.PRRef, 0, len(keys))
	for _, k := range keys {
		repo, number, _ := strings.Cut(k, "#")
		n, _ := strconv.Atoi(number)
		out = append(out, grouping.PRRef{Repo: repo, Number: n})
	}
	return out
}
