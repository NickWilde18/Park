# 9. Architecture

## Stack

| Layer | Choice |
|---|---|
| Backend | Go, standard library `net/http` (the Go 1.22+ ServeMux is enough; no router framework) |
| Database | Postgres: facts, verdicts, groups, rules, the task queue, and the work queue |
| Rule expressions | CEL |
| Frontend | React + Vite + TypeScript, built output embedded with `go:embed` |
| Contract | OpenAPI as the single source; both sides generated (`oapi-codegen` / `openapi-typescript`) |
| Live updates | Server-Sent Events |
| Authentication | Identity-provider abstraction: local, forge, OIDC; plus API tokens |
| Forge access | Credential-provider abstraction: app installation first, personal token for a quick start |
| Model judgement | Provider abstraction, bring your own key; off unless configured |
| Repository | Monorepo, without monorepo tooling |
| License | MIT |
| Language | English throughout: code, comments, commits, docs |

## Runtime

One binary, `park`:

| Command | Does |
|---|---|
| `park serve` | Runs the HTTP server and every worker; applies pending migrations first, under a lock |
| `park migrate` | Applies pending migrations and exits |
| `park version` | Prints the version |

Inside `park serve`:

| Part | Does |
|---|---|
| HTTP | The API, the event stream, webhook intake, health checks, and the embedded frontend |
| Refresher | Refreshes pull requests and repositories ([Facts](02-facts.md)) |
| Poller | Schedules polling and full sweeps |
| Resolver | Chains, grouping resolution, and model placement ([Grouping](03-grouping.md)) |
| Evaluator | Verdicts and judgements ([Verdicts](04-verdicts.md)) |
| Task keeper | Creating tasks, expiring leases, verification, and retries ([Tasks](06-tasks.md)) |
| Model pool | Calls to the model provider, under the concurrency limit |

## How stages hand off

Work moves through a queue table in Postgres. Each entry names a kind and a key — refresh pull request N of repository R, resolve the component of pull request P, evaluate unit U, verify task T — and the earliest time it may run. Entries with the same kind and key coalesce while they wait. An entry that is already running is not joined: it is marked dirty, and runs again when it finishes, so work that arrives mid-run is never lost. Workers take entries with `FOR UPDATE SKIP LOCKED`, and `LISTEN/NOTIFY` wakes them without polling. Evaluation is keyed by unit, not by pull request, so two changes in one group evaluate it once, and never side by side.

A stage writes its output and the entries for the stages after it in the same transaction, so a change is never recorded without its follow-up work, or the other way round.

All coordination goes through Postgres, so several `park serve` processes can run side by side. One is enough for most teams.

## Layout

```
Park/
  cmd/park/            entry point: serve, migrate, version
  api/openapi.yaml     the contract
  internal/
    facts/             refresh, webhook intake, polling, fact events, and the collector interface it calls
    forges/github/     the GitHub collector: fetching, and normalizing into neutral facts
    grouping/          chains, assertions, resolution, suggestions
    verdicts/          roles, parts, placing the ball, queues
    judgement/         questions, fingerprints, the model provider interface
    rules/             rule sets, the field catalog, compilation to CEL
    tasks/             creation, leases, verification
    identity/          members, identities, sign-in providers, tokens, sessions
    httpapi/           handlers generated from the contract, and middleware
    store/             Postgres access and migrations
    work/              the work queue and notifications
  web/                 React + Vite + TypeScript; the build output is embedded
  docs/
```

## Neutral vocabulary

Domain terms are forge-neutral — `forge`, `repo`, `pull_request`, `review`, `account` — in tables, columns, API paths, and configuration keys. A forge's kind is a value (`github`), never part of a name; the only place a vendor's name appears in code is its own collector's package. GitHub is the only forge at first, and adding another means adding a collector, not changing the schema. The fact layer calls its collector through a thin interface, shaped by what the fact layer needs — fetch a pull request's state, list open pull requests, read a delivery — so it never imports a vendor's package. There is no broader adapter layer, designed around forges Park does not support, until a second forge actually exists.

## Configuration

A file plus environment variables; the environment wins. Keys are neutral: `http.addr`, `database.url`, `secret_key`, `identity.oidc`, `model.provider`. Forges and installations, sign-in and model providers, bridges, and grouping settings are edited on the dashboard and stored in the database. Their addresses — a forge's base address, an OIDC issuer, a bridge's link template — are configuration, kept in one place and used to build links and check tokens; no fact, identity, or assertion stores a URL. Secrets — forge credentials, webhook secrets, provider keys — are stored encrypted with the server's secret key, or read from the environment or from files.

## Deployment

- One binary or container, and Postgres.
- Only the webhook endpoint has to be reachable from the forge. Without it, polling alone keeps Park correct, only slower.
- `/healthz` reports that the process is up; `/readyz`, that the database is reachable and migrations are current.
- A setup for one person — a local account and a personal token — is the predecessor's use case, and needs nothing more.

## Operations

- **Observability**: structured logs with trace IDs; OpenTelemetry traces and metrics for webhook deliveries (accepted, rejected, duplicate), refresh latency and failures, the lag from fact to verdict, model calls (latency, timeouts, errors), and task states and verification failures.
- **Growth**: fact events and judgements are small and kept for good. A verdict's inputs take a few KB; at a few hundred pull requests and a few hundred verdicts a day, verdicts stay well under 1 GB a year. Event-stream notices are kept for an hour, delivery IDs for a week, and finished work-queue entries are deleted.
- **Backups** are Postgres backups; nothing else holds state.
- **Upgrades** apply forward-only migrations, and never rewrite facts.
- **Tests**: rules — the default rule set included — run against recorded inputs and recorded judgements, with no network. The forge collector runs against recorded forge responses. CI checks that generated code matches the contract.
