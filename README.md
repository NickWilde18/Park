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

Park applies its migrations and listens on port 8080.

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
| `database.url` | `PARK_DATABASE_URL` | Required. A `postgres://` URL |
| `secret_key` | `PARK_SECRET_KEY` | Required, at least 32 bytes. Encrypts the secrets Park stores; they cannot be read without it |

## Develop

| Command | Does |
|---|---|
| `make db` | Starts a Postgres in Docker for development and tests |
| `make gen` | Regenerates the server code and the client types from `api/openapi.yaml` |
| `make test` | Runs the Go tests, with the race detector |
| `make lint` | Runs golangci-lint, oxlint, and the TypeScript type check |
| `make build` | Builds the frontend and `bin/park` |

[AGENTS.md](AGENTS.md) is how to work on Park.

## License

[MIT](LICENSE)
