# 8. Dashboard and API

The dashboard is a client of the same API that executors and bridges use; it has no private back door.

## Views

### My queue

Everything whose ball is with me, as group → chain → pull request. Each row shows the action, the reason, how long the ball has been with me, and badges: the chain position (`3/9`), the state of any task on it, "model" with the confidence when a judgement was used, and "stale" when its facts could not be refreshed.

Rows are ordered by priority, then by how long they have waited, oldest first. The rows of one group stay together, placed by the group's oldest row. Below the queue, collapsed, are what waits on others and what waits on nobody.

From a row, a member can open the pull request on the forge, mark it nudged, or open its task.

### Team

Each member's queue size and oldest wait, and every group with each member's verdicts on its pull requests. Filters by repository and by member.

### Groups

Groups with their pull requests, nested by chain, each pull request marked with the source that placed it; the suggestions inbox; and the manual actions: move, take out, create, merge, split, rename. The grouping pipeline is drawn as its boxes — sources, then resolution, then groups — each configurable, none rewirable.

### Pull request

Its facts: checks, reviews, and threads. Each member's verdict with its path — `author → not a draft → no conflicts → checks not failing → Changes requested, all answered` — the judgements it used, and the rule set version. The timeline of fact events. Its tasks, with their history.

### Tasks

Tasks by state. The inbox of tasks that need a person, with every attempt's failures, item by item, and the retry and dismiss actions.

### Rules

Each role's list drawn as its first-match tree, with passed-over labels on the edges; the editor, structured or expression; drafts, previews with the verdicts they would change, pinned examples, and every published version with its differences from the one before.

### Settings

Mine: automation (which actions become tasks, in which repositories), linked identities, tokens. For admins: installations and repositories, members and invites, sign-in providers, the model provider and each question's threshold, and grouping settings.

## Live updates

`GET /events` streams Server-Sent Events: small notices — `verdict`, `task`, `group`, `pr` — naming what changed. The client fetches what it needs again. Notices are filtered by visibility, numbered, and can be resumed with `Last-Event-ID` for an hour; a client that falls further behind reloads. A comment line every 30 seconds keeps proxies from closing the stream.

## Conventions

- **Base path** `/api/v1`, JSON bodies. The OpenAPI document, served at `/api/v1/openapi.json`, is the single source for server and client code.
- **Authentication**: browsers use the session cookie and the CSRF header; machines send `Authorization: Bearer <token>`. Tokens are never accepted in URLs.
- **Errors**: `{"error": {"code": "…", "message": "…"}}`, with stable codes. Internal details go to logs, never into responses.
- **Pagination**: cursors — `?cursor=…&limit=…`, and a `next_cursor` in the response.
- **Identifiers**: Park's own IDs in paths. Where noted, machines may name pull requests by repository name and number, and Park resolves them.
- **Links**: responses may include links to the forge, built when the response is rendered and never stored.
- **Idempotency**: reports and assertions are idempotent by design; claims accept an `Idempotency-Key` header.
- **Compatibility**: within `v1`, changes only add.

## Endpoints

| Area | Endpoints |
|---|---|
| Session | `GET /auth/providers` · `POST /auth/login` · `GET /auth/{provider}/start` · `GET /auth/{provider}/callback` · `POST /auth/logout` · `GET /me` |
| Queue and verdicts | `GET /queue` · `GET /verdicts` · `GET /verdicts/{id}` |
| Pull requests | `GET /prs` · `GET /prs/{id}` · `GET /prs/{id}/timeline` · `POST /prs/{id}/refresh` · `POST /prs/{id}/nudged` |
| Groups | `GET /groups` · `GET /groups/{id}` · `POST /groups` · `PATCH /groups/{id}` · `POST /groups/{id}/members` · `DELETE /groups/{id}/members/{pr}` · `POST /groups/{id}/merge` · `POST /groups/{id}/split` |
| Suggestions | `GET /suggestions` · `POST /suggestions/{id}/accept` · `POST /suggestions/{id}/dismiss` |
| Assertions | `POST /assertions` · `DELETE /assertions?anchor=…` |
| Rules | `GET /fields` · `GET /questions` · `GET /rulesets` · `GET /rulesets/{version}` · `POST /rulesets/drafts` · `PUT /rulesets/drafts/{id}` · `POST /rulesets/drafts/{id}/preview` · `POST /rulesets/drafts/{id}/publish` · `POST /examples` · `DELETE /examples/{id}` |
| Tasks | `GET /tasks` · `GET /tasks/{id}` · `POST /tasks/claim` · `POST /tasks/{id}/heartbeat` · `POST /tasks/{id}/report` · `POST /tasks/{id}/release` · `POST /tasks/{id}/retry` · `POST /tasks/{id}/dismiss` |
| My settings | `GET /me/automation` · `PUT /me/automation` · `GET /me/identities` · `DELETE /me/identities/{id}` · `GET /me/tokens` · `POST /me/tokens` · `DELETE /me/tokens/{id}` |
| Administration | `/admin/installations` · `/admin/repos` · `/admin/members` · `/admin/invites` · `/admin/providers` · `/admin/grouping` · `/admin/model` |
| Events | `GET /events` |

Outside `/api/v1`: `POST /hooks/{installation_id}` for forge deliveries, and `GET /healthz` and `GET /readyz` for health checks. `{provider}` is the ID an admin gave a configured sign-in provider, not a vendor's name.

## Rules

- **A1. One contract.** The OpenAPI document is the source; server stubs and the client are generated from it, and CI fails when they drift.
- **A2. Same origin.** The frontend calls relative paths (`/api/...`) and contains no hostnames — nothing about where or how Park is deployed.
- **A3. No stored links.** Forge links are rendered per response from stable IDs.
- **A4. Filtered by visibility** ([I7](07-identity.md#rules)): every list, every read, and every event.
- **A5. Every write is authorized** by permission and recorded with its actor.
- **A6. Errors carry codes, not internals.**
- **A7. Changes within a version only add.**
