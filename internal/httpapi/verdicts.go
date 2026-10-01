package httpapi

import (
	"context"
	"strconv"
	"strings"

	"github.com/NickWilde18/Park/internal/verdicts"
)

func accountOut(id int64, login, name string) Account {
	return Account{Id: id, Login: login, Name: name}
}

func unitOut(unit string) Unit {
	kind, raw, _ := strings.Cut(unit, ":")
	id, _ := strconv.ParseInt(raw, 10, 64)
	if kind == "pr" {
		return Unit{Kind: UnitKindPullRequest, Id: id}
	}
	return Unit{Kind: UnitKindGroup, Id: id}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func listOf(xs []string) *[]string {
	if len(xs) == 0 {
		return nil
	}
	out := append([]string(nil), xs...)
	return &out
}

// verdictOut writes a verdict for the API; its inputs only when asked, for a single verdict.
func verdictOut(v verdicts.Verdict, inputs bool) Verdict {
	out := Verdict{Id: v.ID, Unit: unitOut(v.Unit), Current: v.Current, Fallback: v.Fallback, RuleVersion: v.RuleVersion,
		EvaluatedAt: v.EvaluatedAt, CreatedAt: v.CreatedAt, Holders: []Holder{}, Parts: []VerdictPart{}, Reminders: []Reminder{}}
	if v.Side != "" {
		side := VerdictSide(v.Side)
		out.Side = &side
	}
	if v.Closed != "" {
		closed := VerdictClosed(v.Closed)
		out.Closed = &closed
	}
	for _, h := range v.Holders {
		holder := Holder{Account: accountOut(h.Account, h.Login, h.Name), Since: h.Since, Parts: []HeldPart{}}
		for _, p := range h.Parts {
			holder.Parts = append(holder.Parts, HeldPart{PullRequest: p.PR, Kind: HeldPartKind(p.Kind), Action: optional(p.Action),
				Targets: listOf(p.Targets), Priority: HeldPartPriority(p.Priority), Reason: p.Reason})
		}
		out.Holders = append(out.Holders, holder)
	}
	for _, p := range v.Parts {
		part := VerdictPart{PullRequest: p.PR, Account: accountOut(p.Account, p.Login, p.Name), Role: VerdictPartRole(p.Role),
			Kind: VerdictPartKind(p.Kind), Action: optional(p.Action), Targets: listOf(p.Targets), Priority: VerdictPartPriority(p.Priority),
			Reason: p.Reason, ReasonError: optional(p.ReasonError), Rule: p.Rule, Path: []PathStep{}}
		if p.Remind {
			part.Remind = &p.Remind
		}
		if p.Task {
			part.Task = &p.Task
		}
		for _, s := range p.Path {
			part.Path = append(part.Path, PathStep{Rule: s.Rule, Name: s.Name, PassedOver: optional(s.PassedOver),
				Outcome: PathStepOutcome(s.Outcome), Judgements: listOf(s.Judgements), Error: optional(s.Error)})
		}
		out.Parts = append(out.Parts, part)
	}
	for _, r := range v.Reminders {
		out.Reminders = append(out.Reminders, Reminder{Account: accountOut(r.Account, r.Login, r.Name), PullRequest: r.PR, Reason: r.Reason})
	}
	if inputs {
		list := []VerdictInput{}
		for _, in := range v.Inputs {
			list = append(list, VerdictInput{PullRequest: in.PR, Account: accountOut(in.Account, in.Login, in.Name),
				Role: VerdictInputRole(in.Role), Fields: in.Fields})
		}
		out.Inputs = &list
	}
	return out
}

func verdictsOut(vs []verdicts.Verdict) []Verdict {
	out := []Verdict{}
	for _, v := range vs {
		out = append(out, verdictOut(v, false))
	}
	return out
}

func (s *server) GetQueue(ctx context.Context, _ GetQueueRequestObject) (GetQueueResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	holding, waiting, err := s.verdicts.Queue(ctx, c.Member.ID)
	if err != nil {
		return nil, err
	}
	return GetQueue200JSONResponse{Holding: verdictsOut(holding), Waiting: verdictsOut(waiting)}, nil
}

func (s *server) ListVerdicts(ctx context.Context, req ListVerdictsRequestObject) (ListVerdictsResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	if req.Params.Unit != nil {
		v, err := s.verdicts.CurrentOf(ctx, *req.Params.Unit)
		if err != nil {
			return nil, err
		}
		var vs []verdicts.Verdict
		if v != nil {
			vs = append(vs, *v)
		}
		return ListVerdicts200JSONResponse{Verdicts: verdictsOut(vs)}, nil
	}
	vs, err := s.verdicts.Current(ctx)
	if err != nil {
		return nil, err
	}
	return ListVerdicts200JSONResponse{Verdicts: verdictsOut(vs)}, nil
}

func (s *server) GetVerdict(ctx context.Context, req GetVerdictRequestObject) (GetVerdictResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	v, err := s.verdicts.Verdict(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return GetVerdict200JSONResponse(verdictOut(v, true)), nil
}

func (s *server) MarkNudged(ctx context.Context, req MarkNudgedRequestObject) (MarkNudgedResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.verdicts.MarkNudged(ctx, c.Member.ID, req.Id); err != nil {
		return nil, err
	}
	return MarkNudged204Response{}, nil
}
