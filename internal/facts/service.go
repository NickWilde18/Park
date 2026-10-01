package facts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/forges"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/work"
)

// The kinds of work the fact layer queues, each keyed as noted.
const (
	KindRefreshPR   = "facts.refresh_pr"   // repo/number: fetch the full state
	KindCheckPR     = "facts.check_pr"     // repo/number: short polling of checks and mergeability
	KindPollRepo    = "facts.poll_repo"    // repo: list open pull requests, refresh the changed ones
	KindSweepRepo   = "facts.sweep_repo"   // repo: refresh every open pull request
	KindRefreshRepo = "facts.refresh_repo" // repo: default branch and protection
	KindRefreshTeam = "facts.refresh_team" // forge/team: team members
	KindRedeliver   = "facts.redeliver"    // installation: deliveries missed while Park was down
	KindCleanup     = "facts.cleanup"      // deliveries: forget old delivery IDs
)

// Timing is how often the fact layer looks, from docs/design/02-facts.md#defaults.
type Timing struct {
	Coalesce     time.Duration // webhook refreshes of one pull request within this become one
	BaseMoved    time.Duration // before refreshing pull requests whose base branch moved
	Poll         time.Duration
	ShortPoll    time.Duration
	Sweep        time.Duration
	RepoRefresh  time.Duration
	TeamRefresh  time.Duration
	AccessLife   time.Duration // how long an account's access level is trusted
	DeliveryLife time.Duration // how long delivery IDs are kept
}

// DefaultTiming is the documented timing.
var DefaultTiming = Timing{
	Coalesce: 5 * time.Second, BaseMoved: time.Minute, Poll: 10 * time.Minute, ShortPoll: 30 * time.Second,
	Sweep: 6 * time.Hour, RepoRefresh: time.Hour, TeamRefresh: time.Hour, AccessLife: time.Hour,
	DeliveryLife: 7 * 24 * time.Hour,
}

// Follower is told, inside the transaction that stored it, which fields of a pull request changed, so that it can
// queue the work of a later stage.
type Follower func(ctx context.Context, tx pgx.Tx, prID int64, changed []string) error

// Service is the fact layer.
type Service struct {
	pool         *pgxpool.Pool
	box          *secret.Box
	log          *slog.Logger
	now          func() time.Time
	newCollector func(forges.Forge, Credential) (Collector, error)
	collectors   map[int64]Collector
	followers    []Follower
	timing       Timing
	mu           sync.Mutex
}

// New returns the fact layer.
func New(pool *pgxpool.Pool, box *secret.Box, log *slog.Logger) *Service {
	return &Service{pool: pool, box: box, log: log, now: time.Now, newCollector: collectorFor,
		collectors: map[int64]Collector{}, timing: DefaultTiming}
}

// Follow adds a follower. It must be called before the runner starts.
func (s *Service) Follow(f Follower) { s.followers = append(s.followers, f) }

// Register gives the runner the fact layer's work.
func (s *Service) Register(r *work.Runner) {
	r.Handle(KindRefreshPR, s.refreshPR)
	r.Handle(KindCheckPR, s.checkPR)
	r.Handle(KindPollRepo, s.pollRepo)
	r.Handle(KindSweepRepo, s.sweepRepo)
	r.Handle(KindRefreshRepo, s.refreshRepo)
	r.Handle(KindRefreshTeam, s.refreshTeam)
	r.Handle(KindRedeliver, s.redeliver)
	r.Handle(KindCleanup, s.cleanup)
}

// Start queues the recurring work: polling, sweeps, and repository refreshes for every watched repository, reading
// the delivery log of every app installation, and forgetting old deliveries. Queuing what is queued already
// changes nothing, so every start may do it.
func (s *Service) Start(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		now := s.now()
		rows, err := tx.Query(ctx, "SELECT id FROM repo WHERE watched")
		if err != nil {
			return err
		}
		repos, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range repos {
			k := strconv.FormatInt(id, 10)
			for _, e := range []struct {
				kind string
				at   time.Time
			}{{KindPollRepo, now}, {KindRefreshRepo, now}, {KindSweepRepo, now.Add(s.timing.Sweep)}} {
				if err := work.Enqueue(ctx, tx, e.kind, k, e.at); err != nil {
					return err
				}
			}
		}
		rows, err = tx.Query(ctx, "SELECT id FROM installation WHERE credential_kind = 'app'")
		if err != nil {
			return err
		}
		apps, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range apps {
			if err := work.Enqueue(ctx, tx, KindRedeliver, strconv.FormatInt(id, 10), now); err != nil {
				return err
			}
		}
		return work.Enqueue(ctx, tx, KindCleanup, "deliveries", now)
	})
}

// Installation is a connection to a forge, without its secrets.
type Installation struct {
	CreatedAt        time.Time
	Name             string
	Kind             string
	ID               int64
	ForgeID          int64
	HasWebhookSecret bool
}

// InstallationInput is a new installation, or the parts of one to change.
type InstallationInput struct {
	App           *AppCredential
	Name          string
	Kind          string
	Token         string
	WebhookSecret string
	ForgeID       int64
}

func credentialContext(id int64) string {
	return "installation:" + strconv.FormatInt(id, 10) + ":credential"
}

func webhookContext(id int64) string {
	return "installation:" + strconv.FormatInt(id, 10) + ":webhook_secret"
}

var errNoInstallation = fail.New(fail.NotFound, "not_found", "There is no such installation.")

// Installations lists the installations.
func (s *Service) Installations(ctx context.Context) ([]Installation, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, forge_id, name, credential_kind, webhook_secret IS NOT NULL, created_at
		FROM installation ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanInstallation)
}

func scanInstallation(row pgx.CollectableRow) (Installation, error) {
	var i Installation
	err := row.Scan(&i.ID, &i.ForgeID, &i.Name, &i.Kind, &i.HasWebhookSecret, &i.CreatedAt)
	return i, err
}

// sealCredential checks and seals an installation's credential on forge f. An app's private key is read as f's kind
// reads it, so a key it cannot use is refused here rather than on first use.
func (s *Service) sealCredential(id int64, f forges.Forge, in InstallationInput) ([]byte, error) {
	var plain []byte
	switch in.Kind {
	case "token":
		if strings.TrimSpace(in.Token) == "" {
			return nil, fail.New(fail.Invalid, "invalid_credential", "A token installation needs its token.")
		}
		plain = []byte(strings.TrimSpace(in.Token))
	case "app":
		if in.App == nil || in.App.AppID <= 0 || in.App.InstallationID <= 0 || !strings.Contains(in.App.PrivateKey, "PRIVATE KEY") {
			return nil, fail.New(fail.Invalid, "invalid_credential", "An app installation needs its app ID, installation ID, and private key.")
		}
		if _, err := collectorFor(f, Credential{Kind: in.Kind, App: in.App}); err != nil {
			return nil, fail.New(fail.Invalid, "invalid_credential",
				"The app's private key cannot be read; use the PEM file the forge generated for the app.")
		}
		data, err := json.Marshal(in.App)
		if err != nil {
			return nil, err
		}
		plain = data
	default:
		return nil, fail.New(fail.Invalid, "invalid_kind", "An installation's credential is a token or an app.")
	}
	return s.box.Seal(plain, credentialContext(id)), nil
}

// CreateInstallation adds an installation, with its credential and, optionally, its webhook secret.
func (s *Service) CreateInstallation(ctx context.Context, in InstallationInput) (Installation, error) {
	if strings.TrimSpace(in.Name) == "" {
		return Installation{}, fail.New(fail.Invalid, "invalid_name", "An installation needs a name.")
	}
	f, err := forges.Get(ctx, s.pool, in.ForgeID)
	if errors.Is(err, forges.ErrNotFound) {
		return Installation{}, fail.New(fail.Invalid, "invalid_forge", "There is no such forge.")
	}
	if err != nil {
		return Installation{}, err
	}
	if _, ok := collectors[f.Kind]; !ok {
		return Installation{}, fail.New(fail.Invalid, "invalid_forge", "Park cannot collect from that kind of forge.")
	}
	var out Installation
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO installation (forge_id, name, credential_kind, credential) VALUES ($1, $2, $3, '')
			RETURNING id`, in.ForgeID, strings.TrimSpace(in.Name), in.Kind).Scan(&id); err != nil {
			return err
		}
		sealed, err := s.sealCredential(id, f, in)
		if err != nil {
			return err
		}
		var hook []byte
		if in.WebhookSecret != "" {
			hook = s.box.Seal([]byte(in.WebhookSecret), webhookContext(id))
		}
		rows, err := tx.Query(ctx, `UPDATE installation SET credential = $2, webhook_secret = $3 WHERE id = $1
			RETURNING id, forge_id, name, credential_kind, webhook_secret IS NOT NULL, created_at`, id, sealed, hook)
		if err != nil {
			return err
		}
		out, err = pgx.CollectExactlyOneRow(rows, scanInstallation)
		return err
	})
	return out, err
}

// InstallationPatch is a change to an installation; empty fields stay as they are. A new credential replaces the
// old one of the same kind.
type InstallationPatch struct {
	Name          *string
	Token         *string
	App           *AppCredential
	WebhookSecret *string
}

// UpdateInstallation changes an installation.
func (s *Service) UpdateInstallation(ctx context.Context, id int64, p InstallationPatch) (Installation, error) {
	var out Installation
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var forgeID int64
		var kind string
		if err := tx.QueryRow(ctx, "SELECT forge_id, credential_kind FROM installation WHERE id = $1 FOR UPDATE", id).
			Scan(&forgeID, &kind); errors.Is(err, pgx.ErrNoRows) {
			return errNoInstallation
		} else if err != nil {
			return err
		}
		if p.Name != nil {
			if strings.TrimSpace(*p.Name) == "" {
				return fail.New(fail.Invalid, "invalid_name", "An installation needs a name.")
			}
			if _, err := tx.Exec(ctx, "UPDATE installation SET name = $2 WHERE id = $1", id, strings.TrimSpace(*p.Name)); err != nil {
				return err
			}
		}
		if p.Token != nil || p.App != nil {
			in := InstallationInput{Kind: kind, App: p.App}
			if p.Token != nil {
				in.Token = *p.Token
			}
			f, err := forges.Get(ctx, s.pool, forgeID)
			if err != nil {
				return err
			}
			sealed, err := s.sealCredential(id, f, in)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE installation SET credential = $2 WHERE id = $1", id, sealed); err != nil {
				return err
			}
		}
		if p.WebhookSecret != nil {
			var hook []byte
			if *p.WebhookSecret != "" {
				hook = s.box.Seal([]byte(*p.WebhookSecret), webhookContext(id))
			}
			if _, err := tx.Exec(ctx, "UPDATE installation SET webhook_secret = $2 WHERE id = $1", id, hook); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT id, forge_id, name, credential_kind, webhook_secret IS NOT NULL, created_at
			FROM installation WHERE id = $1`, id)
		if err != nil {
			return err
		}
		out, err = pgx.CollectExactlyOneRow(rows, scanInstallation)
		return err
	})
	if err == nil {
		s.mu.Lock()
		delete(s.collectors, id)
		s.mu.Unlock()
	}
	return out, err
}

// collector returns the collector of an installation, made once and reused, with its forge.
func (s *Service) collector(ctx context.Context, installationID int64) (Collector, forges.Forge, error) {
	var forgeID int64
	var kind string
	var sealed []byte
	err := s.pool.QueryRow(ctx, "SELECT forge_id, credential_kind, credential FROM installation WHERE id = $1", installationID).
		Scan(&forgeID, &kind, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, forges.Forge{}, errNoInstallation
	}
	if err != nil {
		return nil, forges.Forge{}, err
	}
	f, err := forges.Get(ctx, s.pool, forgeID)
	if err != nil {
		return nil, forges.Forge{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.collectors[installationID]; ok {
		return c, f, nil
	}
	plain, err := s.box.Open(sealed, credentialContext(installationID))
	if err != nil {
		return nil, f, fmt.Errorf("open the credential of installation %d: %w", installationID, err)
	}
	cred := Credential{Kind: kind}
	if kind == "app" {
		cred.App = &AppCredential{}
		if err := json.Unmarshal(plain, cred.App); err != nil {
			return nil, f, err
		}
	} else {
		cred.Token = string(plain)
	}
	c, err := s.newCollector(f, cred)
	if err != nil {
		return nil, f, err
	}
	s.collectors[installationID] = c
	return c, f, nil
}

// RepoRecord is a repository Park knows.
type RepoRecord struct {
	RefreshedAt    *time.Time
	Owner          string
	Name           string
	DefaultBranch  string
	ID             int64
	ForgeID        int64
	ForgeRepo      int64
	InstallationID int64
	Watched        bool
}

// repoState is a repository with what refreshes need.
type repoState struct {
	protection map[string]Protection
	RepoRecord
}

func (r *repoState) facts() Repo { return Repo{Owner: r.Owner, Name: r.Name, ForgeRepo: r.ForgeRepo} }

const repoColumns = "id, forge_id, forge_repo_id, installation_id, owner, name, default_branch, watched, refreshed_at"

func scanRepo(row pgx.CollectableRow) (RepoRecord, error) {
	var r RepoRecord
	err := row.Scan(&r.ID, &r.ForgeID, &r.ForgeRepo, &r.InstallationID, &r.Owner, &r.Name, &r.DefaultBranch, &r.Watched, &r.RefreshedAt)
	return r, err
}

var errNoRepo = fail.New(fail.NotFound, "not_found", "There is no such repository.")

func (s *Service) loadRepo(ctx context.Context, id int64) (repoState, error) {
	var r repoState
	var protection []byte
	err := s.pool.QueryRow(ctx, "SELECT "+repoColumns+", protection FROM repo WHERE id = $1", id).
		Scan(&r.ID, &r.ForgeID, &r.ForgeRepo, &r.InstallationID, &r.Owner, &r.Name, &r.DefaultBranch, &r.Watched, &r.RefreshedAt, &protection)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, errNoRepo
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(protection, &r.protection)
	return r, err
}

// Repos lists the repositories Park knows.
func (s *Service) Repos(ctx context.Context) ([]RepoRecord, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+repoColumns+" FROM repo ORDER BY owner, name")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanRepo)
}

// WatchRepo starts watching a repository through an installation, found on the forge by owner and name. Polling
// and a repository refresh start at once.
func (s *Service) WatchRepo(ctx context.Context, installationID int64, owner, name string) (RepoRecord, error) {
	col, f, err := s.collector(ctx, installationID)
	if err != nil {
		return RepoRecord{}, err
	}
	rf, err := col.Repository(ctx, strings.TrimSpace(owner), strings.TrimSpace(name))
	if errors.Is(err, ErrNotFound) {
		return RepoRecord{}, fail.New(fail.Invalid, "unknown_repo", "The forge has no such repository, or this installation cannot see it.")
	}
	if err != nil {
		return RepoRecord{}, err
	}
	var out RepoRecord
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `INSERT INTO repo (forge_id, forge_repo_id, installation_id, owner, name, default_branch)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (forge_id, forge_repo_id) DO UPDATE SET installation_id = EXCLUDED.installation_id,
				owner = EXCLUDED.owner, name = EXCLUDED.name, default_branch = EXCLUDED.default_branch, watched = true
			RETURNING `+repoColumns, f.ID, rf.ForgeRepo, installationID, rf.Owner, rf.Name, rf.DefaultBranch)
		if err != nil {
			return err
		}
		if out, err = pgx.CollectExactlyOneRow(rows, scanRepo); err != nil {
			return err
		}
		return s.startRepo(ctx, tx, out.ID)
	})
	return out, err
}

// startRepo queues a watched repository's polling and refreshes.
func (s *Service) startRepo(ctx context.Context, tx pgx.Tx, id int64) error {
	k := strconv.FormatInt(id, 10)
	now := s.now()
	for _, e := range []struct {
		kind string
		at   time.Time
	}{{KindRefreshRepo, now}, {KindPollRepo, now}, {KindSweepRepo, now.Add(s.timing.Sweep)}} {
		if err := work.Enqueue(ctx, tx, e.kind, k, e.at); err != nil {
			return err
		}
	}
	return nil
}

// SetWatched starts or stops watching a repository. Its facts stay either way.
func (s *Service) SetWatched(ctx context.Context, id int64, watched bool) (RepoRecord, error) {
	var out RepoRecord
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "UPDATE repo SET watched = $2 WHERE id = $1 RETURNING "+repoColumns, id, watched)
		if err != nil {
			return err
		}
		out, err = pgx.CollectExactlyOneRow(rows, scanRepo)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNoRepo
		}
		if err != nil || !watched {
			return err
		}
		return s.startRepo(ctx, tx, id)
	})
	return out, err
}

// watched lists the watched repositories of a forge, for resolving references.
func (s *Service) watched(ctx context.Context, forgeID int64) ([]WatchedRepo, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, owner, name FROM repo WHERE forge_id = $1 AND watched", forgeID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (WatchedRepo, error) {
		var w WatchedRepo
		err := row.Scan(&w.ID, &w.Owner, &w.Name)
		return w, err
	})
}

// PRRecord is a stored pull request.
type PRRecord struct {
	RefreshedAt time.Time
	State       PR
	Repo        RepoRecord
	ID          int64
	Seq         int64
	Stale       bool
}

// PRs lists stored pull requests, of one repository or all, and only open ones if asked.
func (s *Service) PRs(ctx context.Context, repoID int64, openOnly bool) ([]PRRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.id, p.state, p.seq, p.stale, p.refreshed_at, r.id, r.forge_id, r.forge_repo_id,
			r.installation_id, r.owner, r.name, r.default_branch, r.watched, r.refreshed_at
		FROM pr_fact p JOIN repo r ON r.id = p.repo_id
		WHERE ($1 = 0 OR p.repo_id = $1) AND (NOT $2 OR p.open)
		ORDER BY r.owner, r.name, p.number`, repoID, openOnly)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanPR)
}

func scanPR(row pgx.CollectableRow) (PRRecord, error) {
	var p PRRecord
	var state []byte
	r := &p.Repo
	err := row.Scan(&p.ID, &state, &p.Seq, &p.Stale, &p.RefreshedAt, &r.ID, &r.ForgeID, &r.ForgeRepo, &r.InstallationID,
		&r.Owner, &r.Name, &r.DefaultBranch, &r.Watched, &r.RefreshedAt)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(state, &p.State)
	return p, err
}

var errNoPR = fail.New(fail.NotFound, "not_found", "There is no such pull request.")

// PR returns one stored pull request.
func (s *Service) PR(ctx context.Context, id int64) (PRRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.id, p.state, p.seq, p.stale, p.refreshed_at, r.id, r.forge_id, r.forge_repo_id,
			r.installation_id, r.owner, r.name, r.default_branch, r.watched, r.refreshed_at
		FROM pr_fact p JOIN repo r ON r.id = p.repo_id WHERE p.id = $1`, id)
	if err != nil {
		return PRRecord{}, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, scanPR)
	if errors.Is(err, pgx.ErrNoRows) {
		return PRRecord{}, errNoPR
	}
	return p, err
}

// Event is one fact event.
type Event struct {
	ObservedAt time.Time
	Changes    map[string]Change
	Source     string
	Seq        int64
}

// Timeline lists a pull request's fact events, in sequence order.
func (s *Service) Timeline(ctx context.Context, prID int64) ([]Event, error) {
	if _, err := s.PR(ctx, prID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT seq, changes, source, observed_at FROM fact_event WHERE pr_id = $1 ORDER BY seq", prID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
		var e Event
		var changes []byte
		if err := row.Scan(&e.Seq, &changes, &e.Source, &e.ObservedAt); err != nil {
			return e, err
		}
		err := json.Unmarshal(changes, &e.Changes)
		return e, err
	})
}

// RequestRefresh queues a refresh of a stored pull request, at once.
func (s *Service) RequestRefresh(ctx context.Context, prID int64) error {
	p, err := s.PR(ctx, prID)
	if err != nil {
		return err
	}
	return work.Enqueue(ctx, s.pool, KindRefreshPR, prKey(p.Repo.ID, p.State.Number), time.Time{})
}

// PullURL is the address of a pull request on its forge, built when it is shown and never stored.
func (s *Service) PullURL(ctx context.Context, r RepoRecord, number int) string {
	f, err := forges.Get(ctx, s.pool, r.ForgeID)
	if err != nil {
		return ""
	}
	k, ok := forges.KindOf(f.Kind)
	if !ok || k.PullURL == nil {
		return ""
	}
	return k.PullURL(f.BaseAddress, r.Owner, r.Name, number)
}

// Accounts returns the display data of accounts on a forge; accounts Park has not seen are missing.
func (s *Service) Accounts(ctx context.Context, forgeID int64, ids []int64) (map[int64]forges.Account, error) {
	rows, err := s.pool.Query(ctx, "SELECT account_id, login, name FROM account WHERE forge_id = $1 AND account_id = ANY($2)", forgeID, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64]forges.Account{}
	for rows.Next() {
		var a forges.Account
		if err := rows.Scan(&a.ID, &a.Login, &a.Name); err != nil {
			return nil, err
		}
		out[a.ID] = a
	}
	return out, rows.Err()
}
