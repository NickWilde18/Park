package httpapi

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/forges"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/tasks"
)

// GetBoard is every open unit as it stands for the caller: from the current verdicts, the groups and chains, the
// pull requests' facts, and the caller's tasks. What the rules read for the caller — the threads waiting on them,
// their review, whether one is asked of them — comes from the verdict's inputs, so that the board shows what the
// rules saw when they placed the ball.
func (s *server) GetBoard(ctx context.Context, _ GetBoardRequestObject) (GetBoardResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	identities, err := s.id.Identities(ctx, c.Member.ID)
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{}
	for _, i := range identities {
		mine[accountKey(i.ForgeID, i.AccountID)] = true
	}
	vs, err := s.verdicts.Current(ctx)
	if err != nil {
		return nil, err
	}
	gs, err := s.grouping.Groups(ctx)
	if err != nil {
		return nil, err
	}
	chains, err := s.grouping.ChainsOf(ctx)
	if err != nil {
		return nil, err
	}
	prs, err := s.facts.PRs(ctx, 0, true)
	if err != nil {
		return nil, err
	}
	mineTasks, err := s.tasks.Tasks(ctx, c.Member.ID, false)
	if err != nil {
		return nil, err
	}

	byID := map[int64]facts.PRRecord{}
	authors := map[int64][]int64{}
	for _, p := range prs {
		byID[p.ID] = p
		authors[p.Repo.ForgeID] = append(authors[p.Repo.ForgeID], p.State.Author)
	}
	accounts := map[int64]map[int64]forges.Account{}
	for f, ids := range authors {
		if accounts[f], err = s.facts.Accounts(ctx, f, ids); err != nil {
			return nil, err
		}
	}
	groups := map[int64]grouping.GroupView{}
	groupIDs := make([]int64, 0, len(gs))
	for _, g := range gs {
		groups[g.ID] = g
		groupIDs = append(groupIDs, g.ID)
	}
	described, err := s.docsOf(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	taskOf := map[string]tasks.Task{}
	for _, t := range mineTasks {
		if _, seen := taskOf[t.Unit]; !seen && underway[t.State] {
			taskOf[t.Unit] = t
		}
	}

	links := s.linker()
	var rows []boardRow
	for _, v := range vs {
		u := BoardUnit{Unit: unitOut(v.Unit), Ball: BoardUnitBallNone, Holders: []Account{}, PullRequests: []BoardPullRequest{}}
		var members []int64
		kind, raw, _ := strings.Cut(v.Unit, ":")
		id, _ := strconv.ParseInt(raw, 10, 64)
		if kind == "group" {
			g, ok := groups[id]
			if !ok {
				continue
			}
			u.Name = g.Name
			source := BoardUnitNameSource(g.NameSource)
			u.NameSource = &source
			if d, ok := described[g.ID]; ok {
				link := d.Link
				u.DocLink = &link
			}
			for _, m := range g.Members {
				if m.Open {
					members = append(members, m.PR)
				}
			}
		} else {
			p, ok := byID[id]
			if !ok {
				continue
			}
			u.Name = p.State.Title
			members = []int64{id}
		}
		if v.Side != "" {
			side := BoardUnitSide(v.Side)
			u.Side = &side
		}

		row := boardRow{priority: 1}
		var since, mineSince time.Time
		held := map[string]bool{}
		for _, h := range v.Holders {
			k := accountKey(h.Forge, h.Account)
			if !held[k] {
				held[k] = true
				u.Holders = append(u.Holders, accountOut(h.Account, h.Login, h.Name))
			}
			if since.IsZero() || h.Since.Before(since) {
				since = h.Since
			}
			if !mine[k] {
				continue
			}
			u.Ball = BoardUnitBallYours
			if mineSince.IsZero() || h.Since.Before(mineSince) {
				mineSince = h.Since
			}
			for _, part := range h.Parts {
				if part.Priority == "normal" {
					row.priority = 0
				}
			}
		}
		if u.Ball != BoardUnitBallYours && len(v.Holders) > 0 {
			for _, p := range v.Parts {
				if mine[accountKey(p.Forge, p.Account)] {
					u.Ball = BoardUnitBallOthers
					break
				}
			}
		}
		switch {
		case u.Ball == BoardUnitBallYours:
			u.HeldSince = &mineSince
		case !since.IsZero():
			u.HeldSince = &since
		}
		if t, ok := taskOf[v.Unit]; ok {
			out, err := s.taskOut(ctx, t, false)
			if err != nil {
				return nil, err
			}
			u.Task = &out
		}

		for _, pr := range members {
			p, ok := byID[pr]
			if !ok {
				continue
			}
			chain := chains[pr]
			bp := BoardPullRequest{Summary: summary(p, accounts[p.Repo.ForgeID][p.State.Author], links.pull(ctx, p.Repo, p.State.Number)),
				UpdatedAt: p.State.UpdatedAt, Chain: chainOut(chain), ThreadsUnresolved: unresolved(p.State.Threads)}
			for _, part := range v.Parts {
				if part.PR == pr && mine[accountKey(part.Forge, part.Account)] {
					out := partOut(part)
					bp.Part = &out
					break
				}
			}
			for _, in := range v.Inputs {
				if in.PR != pr || !mine[accountKey(in.Forge, in.Account)] {
					continue
				}
				if n, ok := whole(in.Fields["me.threads_on_me"]); ok {
					bp.ThreadsOnYou = &n
				}
				if d, ok := in.Fields["me.decision"].(string); ok {
					review := BoardPullRequestYourReview(d)
					bp.YourReview = &review
				}
				if r, ok := in.Fields["me.requested"].(bool); ok {
					bp.Requested = &r
				}
				break
			}
			u.PullRequests = append(u.PullRequests, bp)
		}
		if len(u.PullRequests) == 0 {
			continue
		}
		row.unit = u
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].before(rows[j]) })
	out := GetBoard200JSONResponse{Units: []BoardUnit{}}
	for _, r := range rows {
		out.Units = append(out.Units, r.unit)
	}
	return out, nil
}

// underway are the states of a task that is still going, or waiting on its member.
var underway = map[string]bool{"queued": true, "leased": true, "verifying": true, "needs_person": true}

// boardRow is a unit of the board, with what orders it: the caller's units first, those with a part of normal
// priority before the rest, longest held first; then the units the caller waits on, longest held first; then the
// others, by name.
type boardRow struct {
	unit     BoardUnit
	priority int
}

var ballOrder = map[BoardUnitBall]int{BoardUnitBallYours: 0, BoardUnitBallOthers: 1, BoardUnitBallNone: 2}

func (r boardRow) before(o boardRow) bool {
	if a, b := ballOrder[r.unit.Ball], ballOrder[o.unit.Ball]; a != b {
		return a < b
	}
	if r.unit.Ball == BoardUnitBallNone {
		return strings.ToLower(r.unit.Name) < strings.ToLower(o.unit.Name)
	}
	if r.priority != o.priority {
		return r.priority < o.priority
	}
	switch {
	case r.unit.HeldSince == nil:
		return false
	case o.unit.HeldSince == nil:
		return true
	}
	return r.unit.HeldSince.Before(*o.unit.HeldSince)
}

// accountKey names a forge account as verdicts do: forge and account ID.
func accountKey(forge, account int64) string {
	return strconv.FormatInt(forge, 10) + "/" + strconv.FormatInt(account, 10)
}

// unresolved counts a pull request's review threads that are not resolved.
func unresolved(threads []facts.Thread) int {
	n := 0
	for _, t := range threads {
		if !t.Resolved {
			n++
		}
	}
	return n
}

// whole reads a rule field's number, which a verdict's stored inputs hold as a JSON number.
func whole(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}
