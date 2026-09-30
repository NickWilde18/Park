// Command park runs Park.
//
//	park serve [-config file]     run the HTTP server, applying pending migrations first
//	park migrate [-config file]   apply pending migrations and exit
//	park version                  print the version
//
// The configuration file can also be named by PARK_CONFIG; see internal/config for the keys and their
// environment variables.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/config"
	"github.com/NickWilde18/Park/internal/facts"
	_ "github.com/NickWilde18/Park/internal/forges/github" // the forge kinds Park supports
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/httpapi"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store"
	"github.com/NickWilde18/Park/internal/verdicts"
	"github.com/NickWilde18/Park/internal/work"
	"github.com/NickWilde18/Park/web"
)

// version is set when a release is built: -ldflags "-X main.version=v1.2.3".
var version = "dev"

const usage = `usage:
  park serve [-config file]     run the HTTP server, applying pending migrations first
  park migrate [-config file]   apply pending migrations and exit
  park version                  print the version`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr, nil); err != nil {
		fmt.Fprintln(os.Stderr, "park:", err)
		os.Exit(1)
	}
}

// run carries out one command until ctx ends. listening, if not nil, receives the server's address once it listens.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, listening chan<- string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "version":
		_, err := fmt.Fprintln(stdout, fullVersion())
		return err
	case "serve", "migrate":
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}

	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", getenv("PARK_CONFIG"), "the configuration `file`")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(*path, getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	migrations, err := store.Migrations()
	if err != nil {
		return err
	}
	pool, err := store.Open(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := store.Migrate(ctx, pool, migrations)
	if err != nil {
		return err
	}
	log.InfoContext(ctx, "migrations applied", "applied", applied, "known", len(migrations))
	if args[0] == "migrate" {
		return nil
	}
	return serve(ctx, cfg, log, pool, migrations, listening)
}

func serve(ctx context.Context, cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool, migrations []store.Migration, listening chan<- string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	box, err := secret.New(cfg.SecretKey)
	if err != nil {
		return err
	}
	ruleSets := rules.New(pool, log)
	if _, err := ruleSets.Bootstrap(ctx); err != nil {
		return fmt.Errorf("publish the default rule set: %w", err)
	}
	collected := facts.New(pool, box, log)
	groups := grouping.New(pool, log)
	balls := verdicts.New(pool, log, ruleSets, groups)
	collected.Follow(groups.FactsFollower())
	collected.Follow(balls.FactsFollower())
	groups.Follow(balls.GroupingFollower())
	ruleSets.Follow(balls.RulesFollower())
	// Each stage gives the runner its work before it starts.
	runner := work.NewRunner(pool, log, work.Options{})
	collected.Register(runner)
	groups.Register(runner)
	balls.Register(runner)
	if err := collected.Start(ctx); err != nil {
		return fmt.Errorf("schedule collection: %w", err)
	}
	if err := balls.Start(ctx); err != nil {
		return fmt.Errorf("schedule verdicts: %w", err)
	}
	worked := make(chan error, 1)
	go func() { worked <- runner.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-worked; err != nil {
			log.ErrorContext(ctx, "work runner", "error", err)
		}
	}()

	ids, err := identity.New(pool, box, log, identity.Options{PublicURL: cfg.HTTP.PublicURL, SecretKey: cfg.SecretKey})
	if err != nil {
		return err
	}
	code, needed, err := ids.IssueSetupCode(ctx, cfg.SetupCode)
	if err != nil {
		return fmt.Errorf("prepare the setup code: %w", err)
	}
	switch {
	case needed && code != "":
		log.InfoContext(ctx, "Park has no members yet: set it up within an hour, with this code", "setup_code", code)
	case needed:
		log.InfoContext(ctx, "Park has no members yet: set it up within an hour, with the setup code from the configuration")
	}

	handler, err := httpapi.New(httpapi.Options{
		Ready: func(ctx context.Context) error {
			pending, err := store.Pending(ctx, pool, migrations)
			if err != nil {
				return err
			}
			if len(pending) > 0 {
				return fmt.Errorf("%d migrations are pending", len(pending))
			}
			return nil
		},
		UI:       web.Dist(),
		Logger:   log,
		Identity: ids,
		Facts:    collected,
		Grouping: groups,
		Rules:    ruleSets,
		Verdicts: balls,
		Pool:     pool,
	})
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTP.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTP.Addr, err)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	log.InfoContext(ctx, "listening", "addr", ln.Addr().String(), "version", fullVersion())
	if listening != nil {
		listening <- ln.Addr().String()
	}

	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	log.InfoContext(ctx, "shutting down")
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return fmt.Errorf("shut down: %w", err)
	}
	return nil
}

// fullVersion is the version, followed for a development build by the commit it was built from.
func fullVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				modified = ", modified"
			}
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision == "" {
		return version
	}
	return fmt.Sprintf("%s (%s%s)", version, revision, modified)
}
