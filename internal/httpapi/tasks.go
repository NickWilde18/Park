package httpapi

import (
	"context"
	"strconv"
	"time"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/tasks"
)

// requireExecutor returns the calling member and their token: a lease is held by an API token (T13).
func requireExecutor(ctx context.Context) (int64, string, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return 0, "", err
	}
	if c.TokenID == "" {
		return 0, "", fail.New(fail.Forbidden, "token_required", "Executors work tasks with an API token scoped to tasks:work.")
	}
	return c.Member.ID, c.TokenID, nil
}

// targetsOut writes an item's targets, with its reviewers by login.
func (s *server) targetsOut(ctx context.Context, pr int64, t tasks.Targets) (ItemTargets, error) {
	out := ItemTargets{Threads: listOf(t.Threads), Checks: listOf(t.Checks)}
	if len(t.Reviews) > 0 {
		reviews := append([]int64(nil), t.Reviews...)
		out.Reviews = &reviews
	}
	if len(t.Reviewers) == 0 {
		return out, nil
	}
	rec, err := s.facts.PR(ctx, pr)
	if err != nil {
		return out, err
	}
	known, err := s.facts.Accounts(ctx, rec.Repo.ForgeID, t.Reviewers)
	if err != nil {
		return out, err
	}
	logins := make([]string, 0, len(t.Reviewers))
	for _, id := range t.Reviewers {
		if a, ok := known[id]; ok {
			logins = append(logins, a.Login)
		} else {
			logins = append(logins, strconv.FormatInt(id, 10))
		}
	}
	out.Reviewers = &logins
	return out, nil
}

func (s *server) taskOut(ctx context.Context, t tasks.Task, events bool) (Task, error) {
	out := Task{Id: t.ID, MemberId: t.Member, Unit: unitOut(t.Unit), State: TaskState(t.State), Priority: TaskPriority(t.Priority),
		Attempt: t.Attempt, Executor: optional(t.Executor), LeaseExpiresAt: t.LeaseExpiresAt, CancelMarked: t.CancelMarked,
		NextAttemptAt: t.NextAttemptAt, Deadline: t.Deadline, Note: t.Note, Waiting: t.Waiting, Reason: t.Reason,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, Items: []TaskItem{}}
	for _, it := range t.Items {
		targets, err := s.targetsOut(ctx, it.PR, it.Targets)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, TaskItem{Id: it.ID, PullRequest: it.PR, Action: it.Action, Targets: targets,
			State: TaskItemState(it.State), Failures: orNone(it.Failures)})
	}
	if events {
		list := []TaskEvent{}
		for _, e := range t.Events {
			list = append(list, TaskEvent{Id: e.ID, Kind: e.Kind, Actor: e.Actor, State: e.State, Detail: e.Detail, At: e.At})
		}
		out.Events = &list
	}
	return out, nil
}

func orNone(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// leasedOut writes a claimed task for its executor: each item's pull request, with a link built for this response.
func (s *server) leasedOut(ctx context.Context, t tasks.Task) (LeasedTask, error) {
	out := LeasedTask{Id: t.ID, LeaseId: t.LeaseID, Attempt: t.Attempt, HeartbeatSeconds: int(s.tasks.Settings().Heartbeat / time.Second),
		Items: []LeasedItem{}}
	if t.LeaseExpiresAt != nil {
		out.LeaseExpiresAt = *t.LeaseExpiresAt
	}
	for _, it := range t.Items {
		if it.State != "open" {
			continue
		}
		rec, err := s.facts.PR(ctx, it.PR)
		if err != nil {
			return out, err
		}
		targets, err := s.targetsOut(ctx, it.PR, it.Targets)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, LeasedItem{Id: it.ID, Action: it.Action, Targets: targets, PreviousFailures: orNone(it.Failures),
			PullRequest: LeasedPullRequest{Id: rec.ID, Repo: rec.Repo.Owner + "/" + rec.Repo.Name, Number: rec.State.Number,
				Head: rec.State.HeadBranch, Base: rec.State.BaseBranch, HeadCommit: rec.State.HeadSHA},
			Link: s.facts.PullURL(ctx, rec.Repo, rec.State.Number)})
	}
	return out, nil
}

func (s *server) ListTasks(ctx context.Context, req ListTasksRequestObject) (ListTasksResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	everyone := req.Params.All != nil && *req.Params.All && c.Member.Admin
	ts, err := s.tasks.Tasks(ctx, c.Member.ID, everyone)
	if err != nil {
		return nil, err
	}
	out := ListTasks200JSONResponse{Tasks: []Task{}}
	for _, t := range ts {
		v, err := s.taskOut(ctx, t, false)
		if err != nil {
			return nil, err
		}
		out.Tasks = append(out.Tasks, v)
	}
	return out, nil
}

func (s *server) GetTask(ctx context.Context, req GetTaskRequestObject) (GetTaskResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.tasks.Task(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if t.Member != c.Member.ID && !c.Member.Admin {
		return nil, fail.New(fail.NotFound, "not_found", "There is no such task.")
	}
	out, err := s.taskOut(ctx, t, true)
	return GetTask200JSONResponse(out), err
}

func (s *server) ClaimTask(ctx context.Context, req ClaimTaskRequestObject) (ClaimTaskResponseObject, error) {
	member, token, err := requireExecutor(ctx)
	if err != nil {
		return nil, err
	}
	claim := tasks.ClaimRequest{Executor: req.Body.Executor, Actions: req.Body.Actions}
	if req.Body.Repos != nil {
		claim.Repos = *req.Body.Repos
	}
	if req.Body.WaitSeconds != nil {
		claim.Wait = time.Duration(*req.Body.WaitSeconds) * time.Second
	}
	if req.Params.IdempotencyKey != nil {
		claim.Key = *req.Params.IdempotencyKey
	}
	t, err := s.tasks.Claim(ctx, member, token, claim)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return ClaimTask204Response{}, nil
	}
	out, err := s.leasedOut(ctx, *t)
	return ClaimTask200JSONResponse(out), err
}

func (s *server) HeartbeatTask(ctx context.Context, req HeartbeatTaskRequestObject) (HeartbeatTaskResponseObject, error) {
	member, _, err := requireExecutor(ctx)
	if err != nil {
		return nil, err
	}
	note, waiting := "", false
	if req.Body.Note != nil {
		note = *req.Body.Note
	}
	if req.Body.WaitingOnMember != nil {
		waiting = *req.Body.WaitingOnMember
	}
	hb, err := s.tasks.Beat(ctx, member, req.Id, req.Body.LeaseId, note, waiting)
	if err != nil {
		return nil, err
	}
	return HeartbeatTask200JSONResponse{Current: hb.Current, State: hb.State, Cancel: hb.Cancel, LeaseExpiresAt: hb.ExpiresAt}, nil
}

func (s *server) ReportTask(ctx context.Context, req ReportTaskRequestObject) (ReportTaskResponseObject, error) {
	member, _, err := requireExecutor(ctx)
	if err != nil {
		return nil, err
	}
	r := tasks.Report{Lease: req.Body.LeaseId, Outcome: string(req.Body.Outcome)}
	if req.Body.Items != nil {
		for _, it := range *req.Body.Items {
			ir := tasks.ItemReport{ID: it.Id}
			if it.Note != nil {
				ir.Note = *it.Note
			}
			if it.Commits != nil {
				ir.Commits = *it.Commits
			}
			if it.Threads != nil {
				ir.Threads = *it.Threads
			}
			if it.Reviews != nil {
				ir.Reviews = *it.Reviews
			}
			r.Items = append(r.Items, ir)
		}
	}
	t, err := s.tasks.Report(ctx, member, req.Id, r)
	if err != nil {
		return nil, err
	}
	out, err := s.taskOut(ctx, t, false)
	return ReportTask200JSONResponse(out), err
}

func (s *server) ReleaseTask(ctx context.Context, req ReleaseTaskRequestObject) (ReleaseTaskResponseObject, error) {
	member, _, err := requireExecutor(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.tasks.Release(ctx, member, req.Id, req.Body.LeaseId)
	if err != nil {
		return nil, err
	}
	out, err := s.taskOut(ctx, t, false)
	return ReleaseTask200JSONResponse(out), err
}

func (s *server) RetryTask(ctx context.Context, req RetryTaskRequestObject) (RetryTaskResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.tasks.Retry(ctx, c.Member.ID, c.Member.Admin, req.Id)
	if err != nil {
		return nil, err
	}
	out, err := s.taskOut(ctx, t, false)
	return RetryTask200JSONResponse(out), err
}

func (s *server) DismissTask(ctx context.Context, req DismissTaskRequestObject) (DismissTaskResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.tasks.Dismiss(ctx, c.Member.ID, c.Member.Admin, req.Id, req.Body.Reason)
	if err != nil {
		return nil, err
	}
	out, err := s.taskOut(ctx, t, false)
	return DismissTask200JSONResponse(out), err
}

func automationOut(a tasks.Automation) Automation {
	out := Automation{Actions: orNone(a.Actions)}
	if a.Repos != nil {
		repos := append([]int64(nil), a.Repos...)
		out.Repos = &repos
	}
	return out
}

func (s *server) GetAutomation(ctx context.Context, _ GetAutomationRequestObject) (GetAutomationResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.tasks.Automation(ctx, c.Member.ID)
	if err != nil {
		return nil, err
	}
	return GetAutomation200JSONResponse(automationOut(a)), nil
}

func (s *server) SetAutomation(ctx context.Context, req SetAutomationRequestObject) (SetAutomationResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	a := tasks.Automation{Actions: req.Body.Actions}
	if req.Body.Repos != nil {
		a.Repos = *req.Body.Repos
	}
	a, err = s.tasks.SetAutomation(ctx, c.Member.ID, a)
	if err != nil {
		return nil, err
	}
	return SetAutomation200JSONResponse(automationOut(a)), nil
}
