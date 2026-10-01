// Command seed fills an empty database with a small team's pull requests, for the dashboard's end-to-end tests and
// for trying Park without a forge.
//
// It makes two members, Ana, an admin, and Ben, both with the password in [Password]; their accounts and Carol's on a
// forge named Hub; and six pull requests in acme/api, with the fact events a refresh would have written. Two of them,
// one stacked on the other, are in a group named Billing, and one has merged; a bridge, Review threads, has proposed
// two more groups of the others, one named and one not, which wait for a member. It publishes the default rule set,
// turns on Ana's automation and Ben's, and evaluates every unit, which gives Ana her task and Ben his two. Ben's
// executor then tries one of his three times, and verification fails each time, so it needs a person: claims,
// reports, and verification are Park's own, with the facts marked as fetched after each report, as a refresh would
// have, and the backoff cut short.
//
// It reads the same configuration as park serve, and refuses a database that has members already. Hub's address
// answers nothing, so the facts it seeds are never refreshed away.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/config"
	"github.com/NickWilde18/Park/internal/facts"
	_ "github.com/NickWilde18/Park/internal/forges/github" // the forge kinds Park supports
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store"
	"github.com/NickWilde18/Park/internal/tasks"
	"github.com/NickWilde18/Park/internal/verdicts"
	"github.com/NickWilde18/Park/internal/work"
)

// Password is the seeded members' password.
const Password = "a long enough password"

// hub is the seeded forge's address, where nothing listens.
const hub = "http://127.0.0.1:9"

func main() {
	if err := seed(context.Background(), os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func seed(ctx context.Context, getenv func(string) string, out io.Writer) error {
	cfg, err := config.Load(getenv("PARK_CONFIG"), getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	migrations, err := store.Migrations()
	if err != nil {
		return err
	}
	pool, err := store.Open(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := store.Migrate(ctx, pool, migrations); err != nil {
		return err
	}
	box, err := secret.New(cfg.SecretKey)
	if err != nil {
		return err
	}
	ids, err := identity.New(pool, box, log, identity.Options{SecretKey: cfg.SecretKey})
	if err != nil {
		return err
	}
	if needed, err := ids.SetupNeeded(ctx); err != nil || !needed {
		return errors.Join(err, errors.New("the database has members already: seed an empty one"))
	}
	code, _, err := ids.IssueSetupCode(ctx, "")
	if err != nil {
		return err
	}
	ana, err := ids.Setup(ctx, code, "ana", Password, "Ana", "seed")
	if err != nil {
		return err
	}
	admin := &identity.Caller{Member: ana}
	invite, _, err := ids.CreateInvite(ctx, admin, time.Hour)
	if err != nil {
		return err
	}
	ben, err := ids.Register(ctx, invite, "ben", Password, "Ben", "seed")
	if err != nil {
		return err
	}

	var forge int64
	if err := pool.QueryRow(ctx, "INSERT INTO forge (kind, name, base_address) VALUES ('github', 'Hub', $1) RETURNING id", hub).Scan(&forge); err != nil {
		return err
	}
	collected := facts.New(pool, box, log)
	inst, err := collected.CreateInstallation(ctx, facts.InstallationInput{ForgeID: forge, Name: "acme", Kind: "token", Token: "unused"})
	if err != nil {
		return err
	}
	var repo int64
	if err := pool.QueryRow(ctx, `INSERT INTO repo (forge_id, forge_repo_id, installation_id, owner, name, default_branch)
		VALUES ($1, 1001, $2, 'acme', 'api', 'main') RETURNING id`, forge, inst.ID).Scan(&repo); err != nil {
		return err
	}
	for id, names := range map[int64][2]string{1: {"ana", "Ana"}, 2: {"ben", "Ben"}, 3: {"carol", "Carol"}} {
		if _, err := pool.Exec(ctx, "INSERT INTO account (forge_id, account_id, login, name) VALUES ($1, $2, $3, $4)", forge, id, names[0],
			names[1]); err != nil {
			return err
		}
	}
	for member, account := range map[int64]int64{ana.ID: 1, ben.ID: 2} {
		if _, err := ids.LinkByAdmin(ctx, admin, member, forge, account); err != nil {
			return err
		}
	}

	// In order of number, so that each pull request's ID is the same on every seeded database.
	history := pulls(time.Now())
	numbers := make([]int, 0, len(history))
	for number := range history {
		numbers = append(numbers, number)
	}
	slices.Sort(numbers)
	// Pull request 11 is seen open first, and evaluated, so that its verdict is closed out when it is seen merged.
	stored := map[int]int64{}
	for _, number := range numbers {
		sightings := history[number]
		if number == 11 {
			sightings = sightings[:1]
		}
		id, err := storePR(ctx, pool, repo, number, sightings)
		if err != nil {
			return err
		}
		stored[number] = id
	}
	groups := grouping.New(pool, log)
	billing, err := groups.Create(ctx, ana.ID, "Billing", []int64{stored[12], stored[14]})
	if err != nil {
		return err
	}
	if err := propose(ctx, groups, ana.ID); err != nil {
		return err
	}
	ruleSets := rules.New(pool, log)
	if _, err := ruleSets.Bootstrap(ctx); err != nil {
		return err
	}
	stage := tasks.New(pool, log, groups)
	for member, actions := range map[int64][]string{ana.ID: {"fix_checks", "address_comments"}, ben.ID: {"review"}} {
		if _, err := stage.SetAutomation(ctx, member, tasks.Automation{Actions: actions}); err != nil {
			return err
		}
	}
	balls := verdicts.New(pool, log, ruleSets, groups)
	balls.Follow(stage.VerdictFollower())
	units := []string{"group:" + strconv.FormatInt(billing, 10)}
	for _, number := range []int{11, 13, 15, 16} {
		units = append(units, "pr:"+strconv.FormatInt(stored[number], 10))
	}
	for _, unit := range units {
		if err := balls.Evaluate(ctx, unit); err != nil {
			return err
		}
	}
	if err := see(ctx, pool, stored[11], &history[11][0].pr, history[11][1], 2); err != nil {
		return err
	}
	if err := balls.Evaluate(ctx, units[1]); err != nil {
		return err
	}
	if err := failAttempts(ctx, ids, stage, pool, log, ben); err != nil {
		return fmt.Errorf("the attempts at Ben's task: %w", err)
	}
	_, err = fmt.Fprintf(out, "Seeded Park: sign in as ana, an admin, or ben, with the password %q.\n", Password)
	return err
}

// propose registers Ana's bridge, Review threads, whose threads link at a host where nothing answers, and posts its
// two proposals as its token would: a thread's named group of the export and statement pull requests, and another
// thread's unnamed group of the invoice and statement ones.
func propose(ctx context.Context, groups *grouping.Service, owner int64) error {
	threads, err := groups.CreateBridge(ctx, owner, "Review threads", "https://chat.example.test/thread/{id}")
	if err != nil {
		return err
	}
	for _, p := range []grouping.Proposal{
		{Anchor: "thread-exports", Title: "Exports and statement emails", Groups: []grouping.ProposedGroup{{Name: "Exports and statements",
			PRs: []grouping.PRRef{{Repo: "acme/api", Number: 15}, {Repo: "acme/api", Number: 16}}}}},
		{Anchor: "thread-invoices", Title: "Invoice follow-ups", Groups: []grouping.ProposedGroup{{
			PRs: []grouping.PRRef{{Repo: "acme/api", Number: 13}, {Repo: "acme/api", Number: 16}}}}},
	} {
		if _, err := groups.Propose(ctx, threads.ID, "", p); err != nil {
			return err
		}
	}
	return nil
}

// failAttempts has Ben dismiss his newer task, and his executor try the older one until it needs a person: each
// attempt is claimed and reported through Park's task contract; the facts are then marked as fetched, as a refresh
// after the attempt would have, and the verification brought forward, as if its poll had come round; and Park's own
// verification judges the attempt, and finds no review from Ben.
func failAttempts(ctx context.Context, ids *identity.Service, stage *tasks.Service, pool *pgxpool.Pool, log *slog.Logger,
	ben identity.Member) error {
	mine, err := stage.Tasks(ctx, ben.ID, false)
	if err != nil {
		return err
	}
	if len(mine) != 2 {
		return fmt.Errorf("%d tasks for Ben, not 2", len(mine))
	}
	if _, err := stage.Dismiss(ctx, ben.ID, false, max(mine[0].ID, mine[1].ID), "Reviewing it myself"); err != nil {
		return err
	}
	_, token, err := ids.CreateToken(ctx, &identity.Caller{Member: ben}, "laptop", []string{identity.ScopeTasksWork}, nil, nil)
	if err != nil {
		return err
	}
	for range stage.Settings().Attempts {
		task, err := stage.Claim(ctx, ben.ID, token.ID, tasks.ClaimRequest{Executor: "laptop", Actions: []string{"review"}})
		if err != nil {
			return err
		}
		if task == nil {
			return errors.New("nothing to claim")
		}
		if _, err := stage.Report(ctx, ben.ID, task.ID, tasks.Report{Lease: task.LeaseID, Outcome: "done"}); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, "UPDATE pr_fact SET fetched_at = now()"); err != nil {
			return err
		}
		if err := work.Enqueue(ctx, pool, tasks.KindVerify, strconv.FormatInt(task.ID, 10), time.Time{}); err != nil {
			return err
		}
		state, err := verify(ctx, stage, pool, log, task.ID)
		if err != nil {
			return err
		}
		if state == tasks.NeedsPerson {
			return nil
		}
		if _, err := pool.Exec(ctx, "UPDATE task SET next_attempt_at = now() WHERE id = $1", task.ID); err != nil {
			return err
		}
	}
	return errors.New("the task never needed a person")
}

// verify runs Park's verification of a reported task, and returns the state it leaves the task in.
func verify(ctx context.Context, stage *tasks.Service, pool *pgxpool.Pool, log *slog.Logger, id int64) (string, error) {
	runner := work.NewRunner(pool, log, work.Options{})
	stage.Register(runner)
	running, stop := context.WithCancel(ctx)
	ran := make(chan error, 1)
	go func() { ran <- runner.Run(running) }()
	defer func() {
		stop()
		<-ran
	}()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		t, err := stage.Task(ctx, id)
		if err != nil {
			return "", err
		}
		if t.State != tasks.Verifying {
			return t.State, nil
		}
	}
	return "", errors.New("verification took over 30 seconds")
}

// seen is a pull request as Park saw it at a time.
type seen struct {
	at time.Time
	pr facts.PR
}

// pulls are the seeded pull requests, each as it was seen, time after time: Ana's, which Ben approved and which
// merged yesterday; Ana's two in Billing, one stacked on the other — the first, whose check began failing after Ben was
// asked to review it, and the second, which Ben requested changes on; a review of Carol's that Ben is asked for; Ana's
// draft; and Ana's pull request waiting for Ben's first review.
func pulls(now time.Time) map[int][]seen {
	ago := func(d time.Duration) *time.Time {
		t := now.Add(-d)
		return &t
	}
	pr := func(author int64, title, branch, head string) facts.PR {
		return facts.PR{Title: title, State: facts.StateOpen, Author: author, HeadBranch: branch, HeadSHA: head, HeadRepo: 1001,
			HeadPushedAt: now.Add(-6 * time.Hour), BaseBranch: "main", CreatedAt: now.Add(-8 * time.Hour), UpdatedAt: now.Add(-time.Hour),
			Mergeable: facts.MergeableClean, RequiredApproval: 1, AuthorCanDismiss: true, MergeBlockers: []string{"reviews"},
			Labels: []string{}, Checks: []facts.Check{{Name: "build", State: facts.CheckPassing, Required: true}}, MissingChecks: []string{},
			ReviewRequests: []facts.Request{}, Reviews: []facts.Review{}, Threads: []facts.Thread{}, Conversation: []facts.Comment{},
			References: []facts.Ref{}, ClosesIssues: []facts.Ref{}}
	}
	lately := now.Add(-50 * time.Minute)
	ledger := pr(1, "Ledger cleanup", "feat/ledger-cleanup", "f6a7b8c")
	ledger.Reviews = []facts.Review{{ID: 5, Author: 2, State: facts.ReviewApproved, Commit: "f6a7b8c", At: now.Add(-26 * time.Hour),
		Counts: true}}
	ledger.MergeBlockers = []string{}
	merged := ledger
	merged.State = facts.StateMerged
	merged.MergedAt = ago(20 * time.Hour)
	merged.ClosedAt = ago(20 * time.Hour)
	merged.UpdatedAt = now.Add(-20 * time.Hour)
	passing := pr(1, "Billing export", "feat/billing-export", "a1b2c3d")
	passing.UpdatedAt = now.Add(-6 * time.Hour)
	failing := pr(1, "Billing export", "feat/billing-export", "a1b2c3d")
	failing.Checks = []facts.Check{{Name: "build", State: facts.CheckFailing, Required: true}}
	failing.MergeBlockers = []string{"reviews", "checks"}
	failing.ReviewRequests = []facts.Request{{Account: 2, RequestedAt: ago(3 * time.Hour)}}
	layout := pr(3, "Invoice layout", "feat/invoice-layout", "b2c3d4e")
	layout.ReviewRequests = []facts.Request{{Account: 2, RequestedAt: ago(time.Hour)}}
	retry := pr(1, "Retry failed payments", "feat/billing-retry", "c3d4e5f")
	retry.BaseBranch = "feat/billing-export"
	retry.Reviews = []facts.Review{{ID: 7, Author: 2, State: facts.ReviewChangesRequested, Commit: "c3d4e5f", At: now.Add(-2 * time.Hour),
		HasBody: true, Counts: true}}
	retry.Threads = []facts.Thread{{ID: "T1", Comments: []facts.Comment{{ID: 70, Author: 2, At: now.Add(-2 * time.Hour)}}}}
	formats := pr(1, "Export formats", "feat/export-formats", "d4e5f6a")
	formats.State = facts.StateDraft
	emails := pr(1, "Statement emails", "feat/statement-emails", "e5f6a7b")
	emails.ReviewRequests = []facts.Request{{Account: 2, RequestedAt: ago(30 * time.Minute)}}
	return map[int][]seen{
		11: {{now.Add(-26 * time.Hour), ledger}, {now.Add(-20 * time.Hour), merged}},
		12: {{now.Add(-6 * time.Hour), passing}, {lately, failing}},
		13: {{lately, layout}}, 14: {{lately, retry}}, 15: {{lately, formats}}, 16: {{now.Add(-20 * time.Minute), emails}},
	}
}

// storePR stores a pull request as a refresh would have, seeing it time after time: its last state, and a fact event
// for each sighting with the fields it changed, numbered from 1.
func storePR(ctx context.Context, pool *pgxpool.Pool, repo int64, number int, history []seen) (int64, error) {
	first := history[0].pr
	first.Number = number
	state, err := json.Marshal(first)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO pr_fact (repo_id, number, state, seq, open, head_sha, head_branch, head_repo_id, base_branch,
			forge_updated_at, fetched_at, refreshed_at)
		VALUES ($1, $2, $3, 0, $4, $5, $6, 1001, $7, $8, now(), now()) RETURNING id`,
		repo, number, state, first.Open(), first.HeadSHA, first.HeadBranch, first.BaseBranch, first.UpdatedAt).Scan(&id); err != nil {
		return 0, err
	}
	var before *facts.PR
	for i, sighting := range history {
		sighting.pr.Number = number
		if err := see(ctx, pool, id, before, sighting, int64(i+1)); err != nil {
			return 0, err
		}
		before = &history[i].pr
		before.Number = number
	}
	return id, nil
}

// see records a sighting of a stored pull request as its fact event seq, with the fields that changed since before,
// nil for none, and makes it the pull request's state.
func see(ctx context.Context, pool *pgxpool.Pool, id int64, before *facts.PR, sighting seen, seq int64) error {
	pr := sighting.pr
	if before != nil {
		pr.Number = before.Number
	}
	changes, err := facts.Diff(before, &pr)
	if err != nil {
		return err
	}
	state, err := json.Marshal(pr)
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `UPDATE pr_fact SET state = $2, seq = $3, open = $4, head_sha = $5, head_branch = $6, base_branch = $7,
			forge_updated_at = $8 WHERE id = $1`,
		id, state, seq, pr.Open(), pr.HeadSHA, pr.HeadBranch, pr.BaseBranch, pr.UpdatedAt); err != nil {
		return err
	}
	data, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, "INSERT INTO fact_event (pr_id, seq, changes, source, observed_at) VALUES ($1, $2, $3, 'forge', $4)",
		id, seq, data, sighting.at)
	return err
}
