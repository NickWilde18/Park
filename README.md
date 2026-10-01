# Park

Park is a team's source of truth for "whose turn is it, and to do what" across pull requests. It collects facts from forges, groups pull requests into units of work, places each unit's ball with whoever must act next, and hands work to executors as tasks that count as done only when the forge confirms them.

[The design](docs/design/README.md) describes all of it. The code is the skeleton the first release builds on; the [`mvp` issues](https://github.com/NickWilde18/Park/issues?q=label%3Amvp) track the rest.

## Run it

Park needs Go 1.27, Node 24 with pnpm, and Postgres. `make db` starts a Postgres in Docker:

```sh
make db
make build
export PARK_DATABASE_URL='postgres://park:park@localhost:5432/park?sslmode=disable'
export PARK_SECRET_KEY="$(openssl rand -base64 32)"
bin/park serve
```

Park applies its migrations and listens on port 8080, where the dashboard is. With no members yet, it prints a setup code to its log; within an hour, create the first administrator with it on the dashboard, or through the API:

```sh
curl -c cookies -H 'Content-Type: application/json' http://localhost:8080/api/v1/setup \
  -d '{"code": "XXXX-XXXX-XXXX", "username": "admin", "password": "a long passphrase", "display_name": "Admin"}'
```

To collect pull requests, an admin adds a forge, an installation — a token, or a GitHub App's credentials — and the repositories to watch, through `/api/v1/admin/forges`, `/api/v1/admin/installations`, and `/api/v1/admin/repos`. The forge sends deliveries to `/hooks/{installation}`; without them, polling keeps Park correct, only slower.

Executors work a member's tasks with an API token scoped to `tasks:work`: `POST /api/v1/tasks/claim`, then heartbeats and a report under the lease; the member chooses which actions become tasks, and where, in the dashboard's settings or at `PUT /api/v1/me/automation`.

Each member's queue — the units whose ball they hold — and waiting list are the dashboard's first page, and `GET /api/v1/queue`, once an admin links their forge account, `POST /api/v1/admin/members/{id}/identities`, or they sign in through the forge.

To try the dashboard without a forge, fill an empty database with a small team's pull requests, then start Park on it and sign in as `ana` or `ben`, with the password the seed prints:

```sh
go run ./cmd/seed
bin/park serve
```

Park publishes its [default rule set](internal/rules/default.json) when it first starts. A member with the rules permission changes the rules by importing a rule-set file as a draft, `POST /api/v1/rulesets/drafts` with `{"rules": …}`, which lists any problems, and publishing it, `POST /api/v1/rulesets/drafts/{id}/publish`.

| Command | Does |
|---|---|
| `park serve [-config file]` | Runs the HTTP server, applying pending migrations first |
| `park migrate [-config file]` | Applies pending migrations and exits |
| `park version` | Prints the version |

## Configuration

A YAML file, named by `-config` or `PARK_CONFIG`, and environment variables, which win over it:

| Key | Variable | |
|---|---|---|
| `http.addr` | `PARK_HTTP_ADDR` | The address to listen on; `:8080` by default |
| `http.public_url` | `PARK_HTTP_PUBLIC_URL` | Park's own address, such as `https://park.example.com`; needed for signing in through a forge |
| `database.url` | `PARK_DATABASE_URL` | Required. A `postgres://` URL |
| `secret_key` | `PARK_SECRET_KEY` | Required, at least 32 bytes. Encrypts the secrets Park stores; they cannot be read without it |
| `setup_code` | `PARK_SETUP_CODE` | The one-time code that creates the first administrator; without it, Park prints one to its log |

## Develop

| Command | Does |
|---|---|
| `make db` | Starts a Postgres in Docker for development and tests |
| `make gen` | Regenerates the server code and the client types from `api/openapi.yaml` |
| `make test` | Runs the Go tests, with the race detector |
| `make lint` | Runs golangci-lint, oxlint, and the TypeScript type check |
| `make build` | Builds the frontend and `bin/park` |
| `make e2e` | Runs the dashboard's end-to-end tests against a seeded database; `PARK_DATABASE_URL` names an empty one |

[AGENTS.md](AGENTS.md) is how to work on Park.

## License

[MIT](LICENSE)
