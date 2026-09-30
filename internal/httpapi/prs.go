package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/forges"
)

// factsError turns a forge's rate limit into an error the caller can act on.
func factsError(err error) error {
	var rl *facts.RateLimited
	if errors.As(err, &rl) {
		f := fail.New(fail.RateLimited, "forge_rate_limited", "The forge's rate limit holds; try again later.")
		f.RetryAfter = max(rl.Until.Sub(timeNow()), 0)
		return f
	}
	return err
}

// recast converts between two types with the same JSON shape: the fact layer's records and the contract's.
func recast[T any](from any) (T, error) {
	var to T
	data, err := json.Marshal(from)
	if err != nil {
		return to, err
	}
	err = json.Unmarshal(data, &to)
	return to, err
}

// requiredChecks sums up the required checks: failing if any fails; else pending if any is running or has not
// reported; else passing if there are any; else none.
func requiredChecks(p *facts.PR) PullRequestSummaryChecks {
	state := PullRequestSummaryChecksNone
	if len(p.MissingChecks) > 0 {
		state = PullRequestSummaryChecksPending
	}
	for _, c := range p.Checks {
		if !c.Required {
			continue
		}
		switch {
		case c.State == facts.CheckFailing:
			return PullRequestSummaryChecksFailing
		case c.State == facts.CheckPending:
			state = PullRequestSummaryChecksPending
		case state == PullRequestSummaryChecksNone:
			state = PullRequestSummaryChecksPassing
		}
	}
	return state
}

// linker builds forge links for one response, reading each forge once.
type linker struct {
	s      *server
	forges map[int64]forges.Forge
}

func (s *server) linker() *linker { return &linker{s: s, forges: map[int64]forges.Forge{}} }

func (l *linker) pull(ctx context.Context, r facts.RepoRecord, number int) string {
	f, ok := l.forges[r.ForgeID]
	if !ok {
		var err error
		if f, err = forges.Get(ctx, l.s.pool, r.ForgeID); err != nil {
			return ""
		}
		l.forges[r.ForgeID] = f
	}
	k, ok := forges.KindOf(f.Kind)
	if !ok || k.PullURL == nil {
		return ""
	}
	return k.PullURL(f.BaseAddress, r.Owner, r.Name, number)
}

func summary(p facts.PRRecord, author forges.Account, url string) PullRequestSummary {
	return PullRequestSummary{
		Id:          p.ID,
		Repo:        RepoRef{Id: p.Repo.ID, Owner: p.Repo.Owner, Name: p.Repo.Name},
		Number:      p.State.Number,
		Title:       p.State.Title,
		State:       PullRequestSummaryState(p.State.State),
		Author:      Account{Id: p.State.Author, Login: author.Login, Name: author.Name},
		HeadBranch:  p.State.HeadBranch,
		BaseBranch:  p.State.BaseBranch,
		Checks:      requiredChecks(&p.State),
		Stale:       p.Stale,
		RefreshedAt: p.RefreshedAt,
		Url:         url,
	}
}

func (s *server) ListPullRequests(ctx context.Context, req ListPullRequestsRequestObject) (ListPullRequestsResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	var repo int64
	if req.Params.Repo != nil {
		repo = *req.Params.Repo
	}
	openOnly := req.Params.State == nil || *req.Params.State == ListPullRequestsParamsStateOpen
	prs, err := s.facts.PRs(ctx, repo, openOnly)
	if err != nil {
		return nil, err
	}
	authors := map[int64][]int64{}
	for _, p := range prs {
		authors[p.Repo.ForgeID] = append(authors[p.Repo.ForgeID], p.State.Author)
	}
	accounts := map[int64]map[int64]forges.Account{}
	for f, ids := range authors {
		if accounts[f], err = s.facts.Accounts(ctx, f, ids); err != nil {
			return nil, err
		}
	}
	links := s.linker()
	out := ListPullRequests200JSONResponse{PullRequests: []PullRequestSummary{}}
	for _, p := range prs {
		out.PullRequests = append(out.PullRequests, summary(p, accounts[p.Repo.ForgeID][p.State.Author], links.pull(ctx, p.Repo, p.State.Number)))
	}
	return out, nil
}

// named lists every account a pull request's facts name.
func named(p *facts.PR) []int64 {
	ids := []int64{p.Author}
	for _, r := range p.ReviewRequests {
		if r.Account != 0 {
			ids = append(ids, r.Account)
		}
	}
	for _, r := range p.Reviews {
		ids = append(ids, r.Author)
	}
	for _, t := range p.Threads {
		for _, c := range t.Comments {
			ids = append(ids, c.Author)
		}
	}
	for _, c := range p.Conversation {
		ids = append(ids, c.Author)
	}
	return ids
}

func (s *server) GetPullRequest(ctx context.Context, req GetPullRequestRequestObject) (GetPullRequestResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	p, err := s.facts.PR(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	accounts, err := s.facts.Accounts(ctx, p.Repo.ForgeID, named(&p.State))
	if err != nil {
		return nil, err
	}
	factsOut, err := recast[PullRequestFacts](p.State)
	if err != nil {
		return nil, err
	}
	out := GetPullRequest200JSONResponse{
		Summary:  summary(p, accounts[p.State.Author], s.linker().pull(ctx, p.Repo, p.State.Number)),
		Facts:    factsOut,
		Accounts: []Account{},
	}
	for _, a := range accounts {
		out.Accounts = append(out.Accounts, Account{Id: a.ID, Login: a.Login, Name: a.Name})
	}
	return out, nil
}

func (s *server) GetPullRequestTimeline(ctx context.Context, req GetPullRequestTimelineRequestObject) (GetPullRequestTimelineResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	events, err := s.facts.Timeline(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	out := GetPullRequestTimeline200JSONResponse{Events: []FactEvent{}}
	for _, e := range events {
		changes, err := recast[map[string]FieldChange](e.Changes)
		if err != nil {
			return nil, err
		}
		out.Events = append(out.Events, FactEvent{Seq: e.Seq, ObservedAt: e.ObservedAt, Source: e.Source, Changes: changes})
	}
	return out, nil
}

func (s *server) RefreshPullRequest(ctx context.Context, req RefreshPullRequestRequestObject) (RefreshPullRequestResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	if err := s.facts.RequestRefresh(ctx, req.Id); err != nil {
		return nil, err
	}
	return RefreshPullRequest202Response{}, nil
}

func installationOut(i facts.Installation) Installation {
	return Installation{Id: i.ID, ForgeId: i.ForgeID, Name: i.Name, Kind: InstallationKind(i.Kind), HasWebhookSecret: i.HasWebhookSecret,
		WebhookPath: "/hooks/" + strconv.FormatInt(i.ID, 10), CreatedAt: i.CreatedAt}
}

func appCredential(a *AppCredential) *facts.AppCredential {
	if a == nil {
		return nil
	}
	return &facts.AppCredential{AppID: a.AppId, InstallationID: a.InstallationId, PrivateKey: a.PrivateKey}
}

func (s *server) ListInstallations(ctx context.Context, _ ListInstallationsRequestObject) (ListInstallationsResponseObject, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	is, err := s.facts.Installations(ctx)
	if err != nil {
		return nil, err
	}
	out := ListInstallations200JSONResponse{Installations: []Installation{}}
	for _, i := range is {
		out.Installations = append(out.Installations, installationOut(i))
	}
	return out, nil
}

func (s *server) CreateInstallation(ctx context.Context, req CreateInstallationRequestObject) (CreateInstallationResponseObject, error) {
	if _, err := requireAdminSession(ctx); err != nil {
		return nil, err
	}
	b := req.Body
	in := facts.InstallationInput{ForgeID: b.ForgeId, Name: b.Name, Kind: string(b.Kind), App: appCredential(b.App)}
	if b.Token != nil {
		in.Token = *b.Token
	}
	if b.WebhookSecret != nil {
		in.WebhookSecret = *b.WebhookSecret
	}
	i, err := s.facts.CreateInstallation(ctx, in)
	if err != nil {
		return nil, err
	}
	return CreateInstallation201JSONResponse(installationOut(i)), nil
}

func (s *server) UpdateInstallation(ctx context.Context, req UpdateInstallationRequestObject) (UpdateInstallationResponseObject, error) {
	if _, err := requireAdminSession(ctx); err != nil {
		return nil, err
	}
	b := req.Body
	i, err := s.facts.UpdateInstallation(ctx, req.Id, facts.InstallationPatch{Name: b.Name, Token: b.Token, App: appCredential(b.App),
		WebhookSecret: b.WebhookSecret})
	if err != nil {
		return nil, err
	}
	return UpdateInstallation200JSONResponse(installationOut(i)), nil
}

func repoOut(r facts.RepoRecord) Repo {
	return Repo{Id: r.ID, ForgeId: r.ForgeID, InstallationId: r.InstallationID, Owner: r.Owner, Name: r.Name,
		DefaultBranch: r.DefaultBranch, Watched: r.Watched, RefreshedAt: r.RefreshedAt}
}

func (s *server) ListRepos(ctx context.Context, _ ListReposRequestObject) (ListReposResponseObject, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	rs, err := s.facts.Repos(ctx)
	if err != nil {
		return nil, err
	}
	out := ListRepos200JSONResponse{Repos: []Repo{}}
	for _, r := range rs {
		out.Repos = append(out.Repos, repoOut(r))
	}
	return out, nil
}

func (s *server) WatchRepo(ctx context.Context, req WatchRepoRequestObject) (WatchRepoResponseObject, error) {
	if _, err := requireAdminSession(ctx); err != nil {
		return nil, err
	}
	r, err := s.facts.WatchRepo(ctx, req.Body.InstallationId, req.Body.Owner, req.Body.Name)
	if err != nil {
		return nil, factsError(err)
	}
	return WatchRepo201JSONResponse(repoOut(r)), nil
}

func (s *server) UpdateRepo(ctx context.Context, req UpdateRepoRequestObject) (UpdateRepoResponseObject, error) {
	if _, err := requireAdminSession(ctx); err != nil {
		return nil, err
	}
	r, err := s.facts.SetWatched(ctx, req.Id, req.Body.Watched)
	if err != nil {
		return nil, err
	}
	return UpdateRepo200JSONResponse(repoOut(r)), nil
}
