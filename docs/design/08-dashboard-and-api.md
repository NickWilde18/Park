# 8. Dashboard and API

The dashboard is a client of the same API that executors and bridges use; it has no private back door.

Every view comes in a light and a dark theme. A switch in the header picks *System*, which follows the operating system, *Light*, or *Dark*; the browser keeps the choice, and a group's description follows it ([Grouping](03-grouping.md#descriptions)).

## Views

### Sign in

A username and password, or a sign-in provider. With an invite, the same page joins Park, with a local account or through a provider that carries the invite; an invite's link opens it ready to join. When a sign-in through a provider fails, the page says why, from the code Park sends back ([Identity](07-identity.md#flow-sign-in)).

### My queue

Every unit whose ball I hold, as group → chain → pull request, with my actions on each pull request. Each row shows the action, the reason, how long I have held the ball, and badges: the chain position (`3/9`), the state of any task on it, "model" with the confidence when a judgement sharpened the reason, and "stale" when its facts could not be refreshed.

Units are ordered by priority, then by how long I have held them, oldest first. Below the queue, collapsed, are the units I have a part in whose ball someone else holds — with who holds it and why — and the reminders offered to me.

From a row, a member can open the pull request on the forge, mark it nudged, or open its task.

### Board

Every open unit in one table, as it stands for me: a row for each unit, which folds open to its pull requests. The unit's row says where its ball is from my side — mine, someone else's, or none of mine — since when, and who holds it, and my task on it, with its state and note. Each pull request's row says my action there and its reason — or where I have nothing to do there, quietly, what its holders hold on it — the path of rules that decided it, its checks, author, chain position, and last activity, the threads waiting on me, the open threads, and my review — read as the rules read them for the current verdict ([D62](10-design-log.md#d62-the-board-is-read-in-one-request-as-the-rules-read-it)).

My units come first, by priority and then by how long I have held them, then the units I wait on, then the rest. Rows sort by any column, and filter by where the ball is and by repository; which columns show is my choice, kept in the browser.

From the board, I can ask the forge now: for every repository, or the one the board is filtered to, and for one unit's pull requests ([D64](10-design-log.md#d64-a-member-can-ask-for-a-poll-now)). A group's row links to its description.

### Team

Each member's held units and oldest hold, and every group with where its ball is and who holds it. Filters by repository and by member.

### Groups

Groups with their pull requests, nested by chain, each pull request marked with the source that placed it; each group's description, linked; the suggestions inbox; bridges' open proposals, each with its groups, to accept as proposed or edited, or to dismiss ([Grouping](03-grouping.md#flow-bridge-proposals)); and the manual actions: move, take out, create, merge, split, rename. The grouping pipeline is drawn as its boxes — sources, then resolution, then groups — each configurable, none rewirable.

### Unit

A group, or a pull request in no group, showing only the pull requests the viewer can read ([Identity](07-identity.md#visibility)). Where its ball is and who holds it, and every person's part with its path — `author → not a draft → no conflicts → checks not failing → Changes requested, all answered` — the judgements used, and the rule set version. For each pull request: its facts — checks, reviews, and threads — when Park last checked them on the forge, and the timeline of its fact events. Its tasks, with their history. A member can refresh its open pull requests from the forge. A group's description shows beside it, in a sidebar, with the controls to upload, replace, or remove it for those who may ([Grouping](03-grouping.md#descriptions)).

### Tasks

Tasks by state. The inbox of tasks that need a person, with every attempt's failures, item by item, and the retry and dismiss actions.

### Rules

Each role's list drawn as its first-match tree, with passed-over labels on the edges; the editor, structured or expression; drafts, previews with the verdicts they would change, pinned examples, and every published version with its differences from the one before. Drafts and the editor are for members with the rules permission, who also start a draft from a rule-set file, and discard one.

### Settings

Mine: automation (which actions become tasks, in which repositories), linked identities, which I connect through a sign-in provider and can unlink unless one is my last way in ([I10](07-identity.md#rules)), tokens. For admins: forges; installations, with the address each forge delivers webhooks to, and credentials typed in and never shown back; repositories, to watch, stop watching, or refresh now; bridges, with a token for one the admin owns, shown once; members, with their permissions and the accounts linked to them by hand; invites, whose code and link are shown once; sign-in providers, with the callback address each forge needs; the model provider and each question's threshold; and grouping settings.

## Live updates

`GET /events` streams Server-Sent Events: small notices — `verdict` (a unit's ball or parts changed), `task`, `group`, `pr`, `proposal` — naming what changed. The client fetches what it needs again, and fetches what it shows each time its stream opens. Notices are filtered by visibility, numbered, and can be resumed with `Last-Event-ID` for an hour; a client that falls further behind reloads. A stream ends when the credential it was opened with stops working — a token revoked, a session ended, a member disabled — and when Park shuts down; the dashboard opens it again, or signs in. A comment line every 30 seconds keeps proxies from closing the stream.

## Conventions

- **Base path** `/api/v1`, JSON bodies; a request body of any other type is refused, but for a group description's zip, `application/zip`, which browsers do not send across sites without asking first. The OpenAPI document, served at `/api/v1/openapi.json`, is the single source for server and client code.
- **Authentication**: browsers use the session cookie and the CSRF header, `X-CSRF-Token`, whose value `GET /me` gives; machines send `Authorization: Bearer <token>`. Tokens are never accepted in URLs.
- **Errors**: `{"error": {"code": "…", "message": "…"}}`, with stable codes. Internal details go to logs, never into responses.
- **Pagination**: cursors — `?cursor=…&limit=…`, and a `next_cursor` in the response.
- **Identifiers**: Park's own IDs in paths. Where noted, machines may name pull requests by repository name and number, and Park resolves them.
- **Links**: responses may include links to the forge, built when the response is rendered and never stored.
- **Idempotency**: reports, assertions, and proposals are idempotent by design; claims accept an `Idempotency-Key` header.
- **Leases**: every task call after the claim carries the lease ID it was given; a call from a lease that is no longer current is refused ([Tasks](06-tasks.md#the-api)).
- **Compatibility**: within `v1`, changes only add.

## Endpoints

| Area | Endpoints |
|---|---|
| Session | `GET /setup` · `POST /setup` · `GET /auth/providers` · `POST /auth/login` · `POST /auth/register` · `GET /auth/{provider}/start` · `GET /auth/{provider}/callback` · `POST /auth/logout` · `GET /me` |
| Queue and verdicts | `GET /queue` · `GET /board` · `GET /verdicts` · `GET /verdicts/{id}` |
| Pull requests | `GET /prs` · `POST /prs/refresh` · `GET /prs/{id}` · `GET /prs/{id}/timeline` · `POST /prs/{id}/refresh` · `POST /prs/{id}/nudged` |
| Groups | `GET /groups` · `GET /groups/{id}` · `POST /groups` · `PATCH /groups/{id}` · `POST /groups/{id}/members` · `DELETE /groups/{id}/members/{pr}` · `POST /groups/{id}/merge` · `POST /groups/{id}/split` · `PUT /groups/{id}/doc` · `DELETE /groups/{id}/doc` |
| Suggestions | `GET /suggestions` · `POST /suggestions/{id}/accept` · `POST /suggestions/{id}/dismiss` |
| Assertions | `POST /assertions` · `DELETE /assertions?anchor=…` |
| Proposals | `POST /proposals` · `DELETE /proposals?anchor=…` · `GET /proposals` · `POST /proposals/{id}/accept` · `POST /proposals/{id}/dismiss` |
| Rules | `GET /fields` · `GET /questions` · `GET /actions` · `GET /rulesets` · `GET /rulesets/{version}` · `GET /rulesets/drafts` · `POST /rulesets/drafts` · `GET /rulesets/drafts/{id}` · `PUT /rulesets/drafts/{id}` · `DELETE /rulesets/drafts/{id}` · `POST /rulesets/drafts/{id}/preview` · `POST /rulesets/drafts/{id}/publish` · `POST /examples` · `DELETE /examples/{id}` |
| Tasks | `GET /tasks` · `GET /tasks/{id}` · `POST /tasks/claim` · `POST /tasks/{id}/heartbeat` · `POST /tasks/{id}/report` · `POST /tasks/{id}/release` · `POST /tasks/{id}/retry` · `POST /tasks/{id}/dismiss` |
| My settings | `GET /me/automation` · `PUT /me/automation` · `GET /me/identities` · `DELETE /me/identities/{id}` · `GET /me/tokens` · `POST /me/tokens` · `DELETE /me/tokens/{id}` |
| Administration | `/admin/forges` · `/admin/installations` · `/admin/repos` · `/admin/members` · `/admin/invites` · `/admin/providers` · `/admin/bridges` · `/admin/grouping` · `/admin/model` |
| Events | `GET /events` |

Outside `/api/v1`: `POST /hooks/{installation_id}` for forge deliveries; `GET /docs/{token}/…` for groups' descriptions, by their links; and `GET /healthz` and `GET /readyz` for health checks. `{provider}` is the ID an admin gave a configured sign-in provider, not a vendor's name.

## Rules

- **A1. One contract.** The OpenAPI document is the source; server stubs and the client are generated from it, and CI fails when they drift.
- **A2. Same origin.** The frontend calls relative paths (`/api/...`) and contains no hostnames — nothing about where or how Park is deployed.
- **A3. No stored links.** Forge links are rendered per response from stable IDs.
- **A4. Filtered by visibility** ([I7](07-identity.md#rules)), pull request by pull request: every list, every read, every event, and every write. A group's description, read by its link, is the one exception ([Grouping](03-grouping.md#descriptions)).
- **A5. Every write is authorized** by permission and recorded with its actor.
- **A6. Errors carry codes, not internals.**
- **A7. Changes within a version only add.**
