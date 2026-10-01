# Working on Park

Park is built from its design: [the design](docs/design/README.md) says what to build, and this file says how to work on it.

## Before you start

1. Read the [design index](docs/design/README.md), then every chapter your change touches.
2. Features and fixes start from an issue. One issue, one branch, one pull request, which says `Closes #N`.
3. Check the working tree before changing anything, and leave changes that are not yours alone.

## The design is the specification

- The chapters in `docs/design/` say what Park does, and the code does what they say. Where a chapter is silent, decide, and write the decision into the chapter in the same pull request.
- A change that departs from a chapter changes the chapter in the same pull request, and adds an entry under *Proposed* in the [design log](docs/design/10-design-log.md#proposed) with its reason. Chapters cite each other's numbered rules: never renumber a rule, and never reuse a removed number.
- After changing a chapter, run the [design checks](docs/design/checks/README.md).

## Constraints

- **Neutral names.** No vendor's name in a table, a column, an API path, a configuration key, or the frontend. A forge's kind is a value, `github`; a vendor's name appears in code only in its own collector's package ([Architecture](docs/design/09-architecture.md#neutral-vocabulary)).
- **No stored URLs.** Store stable IDs, and build links when a response is rendered ([A3](docs/design/08-dashboard-and-api.md#rules)).
- **The frontend knows nothing about deployment.** Relative paths only: no hostnames, host lists, regions, or instances ([A2](docs/design/08-dashboard-and-api.md#rules)).
- **Public from the first commit.** No organization's name, colleague's username, internal project, or internal pull request number — in code, tests, fixtures, comments, commits, issues, or pull requests. Code ported from the predecessor is rewritten as it moves. `scripts/check_text.py` checks the repository against a private list of such names ([Private names](docs/design/checks/README.md#private-names)).
- **English throughout**: code, comments, commits, documents, issues, and pull requests.
- **Nothing sensitive in logs**: no tokens, credentials, comment or review text, or personal data. Log IDs and the trace ID.

## Writing code

- Standard library first. A new dependency needs its reason in the pull request.
- Packages follow the [layout](docs/design/09-architecture.md#layout), and a stage reads the stages before it, never the ones after.
- Code uses the design's terms — unit, ball, part, verdict, item — as the [glossary](docs/design/README.md#glossary) defines them.
- Generated code is never edited by hand: change its source and regenerate it.
- The API changes in `api/openapi.yaml` first. Within `v1`, changes only add.
- Migrations are forward-only, and a merged migration is never edited. Before numbering a new one, check the open pull requests for the same number.
- Comments say what code is for, what it assumes, and how it fails. How it came to be, and what was set aside, belong in the pull request or the design log.
- Every exported identifier has a doc comment.

## Commands

| Command | Does |
|---|---|
| `make db` | Starts a Postgres in Docker for development and tests |
| `make gen` | Regenerates the server code in `internal/httpapi` and the client types in `web/src/api` from `api/openapi.yaml` |
| `make test` | Runs the Go tests with the race detector, against `make db`'s Postgres |
| `make lint` | Runs golangci-lint, oxlint, and the TypeScript type check |
| `make build` | Builds the frontend and `bin/park`, with the frontend embedded |
| `make e2e` | Seeds the empty database `PARK_DATABASE_URL` names with `cmd/seed`, and runs the dashboard's Playwright tests against `bin/park serve` |

A test that needs a database gets an empty one of its own from `internal/store/storetest`. Without `PARK_TEST_DATABASE_URL`, such tests are skipped — except in CI, where they fail; `make test` sets it for `make db`'s Postgres. The dashboard's end-to-end tests in `web/e2e` share one seeded database and run in order; arguments to `scripts/e2e.sh` go to Playwright, so `sh scripts/e2e.sh settings` runs one file's tests.

## Tests

- **A bug fix starts with a failing test.** Write a test for the wrong behaviour, run it against the unfixed code, and keep the failing output. A test that passes before the fix does not pin the bug: rewrite it. Then fix, run the test again, and keep the passing output. Both outputs go in the pull request.
- Tests use no network: forge responses and model answers are recorded.
- **Before calling a change done**, list everything it touches — callers, callees, configuration, the schema, the API contract, generated code, the frontend, documents — and check each one: run it, read it, or find every reference to it. Say which were not checked, and why.

## Git and pull requests

- Never push to `main`. Branch from it: `feat/…`, `fix/…`, `docs/…`, `test/…`, `refactor/…`, `chore/…`, `ci/…`.
- Stage files by name, never with `git add -A` or `git add .`.
- Commit messages are `type(scope): summary`, with the summary in lower case and the scope optional: `feat(facts): refresh a pull request on webhook delivery`. The body says why. No `Co-Authored-By` trailers.
- Push normally. Force-push only your own branch after a rebase, with `--force-with-lease`.
- Open pull requests as drafts. The description says what changed and why, how it was verified — the commands and their output — and what is left. Mark it ready for review when CI passes.
