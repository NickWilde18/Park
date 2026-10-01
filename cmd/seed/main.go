// Command seed fills an empty database with a small team's pull requests, for the dashboard's end-to-end tests and
// for trying Park without a forge.
//
// It makes two members, Ana, an admin, and Ben, both with the password in [Password]; their accounts and Carol's on a
// forge named Hub; five pull requests in acme/api, with the fact events a refresh would have written, two of them, one
// stacked on the other, in a group named Billing; the default rule set; Ana's automation; and every unit's verdict, with Ana's task. It reads the
// same configuration as park serve, and refuses a database that has members already. Hub's address answers nothing,
// so the facts it seeds are never refreshed away.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
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

	stored := map[int]int64{}
	for number, history := range pulls(time.Now()) {
		id, err := storePR(ctx, pool, repo, number, history)
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
	ruleSets := rules.New(pool, log)
	if _, err := ruleSets.Bootstrap(ctx); err != nil {
		return err
	}
	stage := tasks.New(pool, log, groups)
	if _, err := stage.SetAutomation(ctx, ana.ID, tasks.Automation{Actions: []string{"fix_checks", "address_comments"}}); err != nil {
		return err
	}
	balls := verdicts.New(pool, log, ruleSets, groups)
	balls.Follow(stage.VerdictFollower())
	for _, unit := range []string{"group:" + strconv.FormatInt(billing, 10), "pr:" + strconv.FormatInt(stored[13], 10),
		"pr:" + strconv.FormatInt(stored[15], 10), "pr:" + strconv.FormatInt(stored[16], 10)} {
		if err := balls.Evaluate(ctx, unit); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "Seeded Park: sign in as ana, an admin, or ben, with the password %q.\n", Password)
	return err
}

// seen is a pull request as Park saw it at a time.
type seen struct {
	at time.Time
	pr facts.PR
}

// pulls are the seeded pull requests, each as it was seen, time after time: Ana's two in Billing, one stacked on the
// other — the first, whose check began failing after Ben was asked to review it, and the second, which Ben requested
// changes on; a review of Carol's that Ben is asked for; Ana's draft; and Ana's pull request waiting for Ben's first
// review.
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
		12: {{now.Add(-6 * time.Hour), passing}, {lately, failing}},
		13: {{lately, layout}}, 14: {{lately, retry}}, 15: {{lately, formats}}, 16: {{now.Add(-20 * time.Minute), emails}},
	}
}

// storePR stores a pull request as a refresh would have, seeing it time after time: its last state, and a fact event
// for each sighting with the fields it changed, numbered from 1.
func storePR(ctx context.Context, pool *pgxpool.Pool, repo int64, number int, history []seen) (int64, error) {
	var id int64
	var before *facts.PR
	for i, s := range history {
		pr := s.pr
		pr.Number = number
		changes, err := facts.Diff(before, &pr)
		if err != nil {
			return 0, err
		}
		state, err := json.Marshal(pr)
		if err != nil {
			return 0, err
		}
		if i == 0 {
			err = pool.QueryRow(ctx, `INSERT INTO pr_fact (repo_id, number, state, seq, open, head_sha, head_branch, head_repo_id, base_branch,
					forge_updated_at, fetched_at, refreshed_at)
				VALUES ($1, $2, $3, 1, $4, $5, $6, 1001, $7, $8, now(), now()) RETURNING id`,
				repo, number, state, pr.Open(), pr.HeadSHA, pr.HeadBranch, pr.BaseBranch, pr.UpdatedAt).Scan(&id)
		} else {
			_, err = pool.Exec(ctx, `UPDATE pr_fact SET state = $2, seq = seq + 1, open = $3, head_sha = $4, head_branch = $5,
					base_branch = $6, forge_updated_at = $7 WHERE id = $1`,
				id, state, pr.Open(), pr.HeadSHA, pr.HeadBranch, pr.BaseBranch, pr.UpdatedAt)
		}
		if err != nil {
			return 0, err
		}
		data, err := json.Marshal(changes)
		if err != nil {
			return 0, err
		}
		if _, err := pool.Exec(ctx, "INSERT INTO fact_event (pr_id, seq, changes, source, observed_at) VALUES ($1, $2, $3, 'forge', $4)",
			id, i+1, data, s.at); err != nil {
			return 0, err
		}
		before = &pr
	}
	return id, nil
}
