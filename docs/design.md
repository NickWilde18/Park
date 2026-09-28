# Park — Architecture Design (candidates and trade-offs)

> Status: draft. Each section lists candidates, their trade-offs, and a recommendation. No implementation code until a candidate is chosen.

## 1. What Park is

In one sentence: **the team-level source of truth for "whose turn is it, and to do what" — plus a dashboard and a contract.**

| Park does | Park does not |
|---|---|
| Collect facts from forges (webhooks first, polling as a backstop) | Start AI sessions, manage worktrees, or manage agent lifecycles |
| Decide, by rules, who holds the ball and what should happen next | Carry out any work on a pull request |
| Member identity and roles | Make decisions for people — verdicts are advice; people and executors decide |
| Web dashboard and open API | |

Not executing is a deliberate scope constraint. Executors already exist — local tools such as the author's ballpark, or Agent Orchestrator — each owning the worktrees and sessions on its own machine. Rebuilding that layer would be both redundant and behind. Park provides what they all lack: multiple members, roles, a server, and a contract.

## 2. Division of labor with executors

```
   forge ──webhook/poll──▶ Park ──verdicts──▶ dashboard (people)
                            │
                            ├──▶ open API ──pull work──▶ executor (ballpark / a bridge / your own)
                            │                               │
                            └◀──── optional claim/report ───┘
                            ▲                               │
                            └──── forge facts, observed ◀───┘ pushes code
```

**When an executor finishes and pushes, Park sees it on the next collection pass.** Reporting only makes that faster; it is never required. This is why Park needs no agent registration, heartbeats, or offline detection — that whole class of complexity only exists when the server is responsible for execution outcomes, and Park is not.

Executors live on the user's machine, and some are loopback-only by design: Agent Orchestrator's daemon binds to `127.0.0.1` and treats that as load-bearing. Park therefore can never push work to an executor; every interaction starts from the executor's side. An executor can also be a thin bridge that pulls from Park and drives an existing tool through its local API.

## 3. Fact layer

### The problem

Most of the confusion in Park's predecessor came from four layers reading each other: verdicts read raw collection results, the state machine read verdicts, the display read the state machine. When any layer lagged, the board rendered rows that contradicted themselves (a session marked Done while the state column still said "in progress").

Park's goal: **a verdict is a pure function `(facts, rules) → conclusion`.** Facts are the only input; every verdict can be traced back and replayed.

Prior art reaches the same principle independently: Agent Orchestrator stores durable facts and computes display status at read time rather than storing it.

### Candidates

**A. Current facts only** (one `pr_fact` table, overwritten on every collection)
- Simple and fast to query.
- Cannot answer "why was this judged that way three days ago?" — the question people ask most.

**B. Pure event sourcing** (only immutable events; current state is a fold)
- Fully traceable and replayable.
- Every verdict walks history; out-of-order delivery, duplicates, and snapshot points are all yours to handle.

**C. Event stream + materialized current state + verdicts that embed their inputs** (recommended)
- `fact_event`: an immutable event stream. Webhook deliveries and polling diffs **both land here**; nothing downstream can tell them apart.
- `pr_fact`: current facts, updated from events, for fast reads by the dashboard and the rule engine.
- `verdict`: a decision result that **embeds a snapshot of the facts it was computed from (JSONB)**.

The third point is what matters: answering "why?" needs no replay — the inputs, the rule that matched, and the conclusion are all in one row. The cost is a few KB per verdict; at a few hundred pull requests and a few hundred verdicts a day, that is well under 1 GB a year.

### Unifying webhooks and polling

Both paths produce the same thing — fact-change events — and differ only in how a change is discovered:

- Webhook: receive a delivery → verify the signature → fetch the full current state of the affected object → diff against `pr_fact` → write an event only if something changed.
- Polling: fetch on a schedule → the same diff → the same write.

Both fetch the full state and diff, rather than trusting the payload, because forges **guarantee neither delivery nor ordering**. After a diff, duplicate deliveries are naturally idempotent and out-of-order ones cost at most one empty diff. Events record their `source` (webhook / poll) for observability only; rules never look at it.

### Stable identifiers only

Facts store stable forge identifiers — repository ID, pull request number, account ID — never URLs and never usernames. URLs are built at render time. Usernames can be renamed: a stored login silently stops matching, while a numeric account ID does not.

## 4. Decision layer

### Keeping model calls out of the pure function

Verdicts must be pure, but "smart judgement" is a network call. The answer is **two passes**:

1. The rule engine runs. When it reaches a branch marked "judgement needed" and the corresponding judgement fact does not exist yet, it takes the **fallback branch** — a deterministic, conservative conclusion — and emits a judgement request.
2. Only if a model provider is configured is the request sent. The answer is **written back to the fact layer** as an ordinary fact. The next verdict run reads it and takes the real branch.

This gives three properties:

- Verdicts stay pure: replayable, testable, no network mocks.
- **Smart judgement is optional by construction.** With no provider configured, the judgement fact never exists and rules always take the fallback — no feature flag needed.
- The model's answer and its confidence are facts, so the dashboard can show whether each conclusion came from a rule or from a model.

### Roles are a function, not a column

`(member, pull request) → role`. The same person can be the author of one pull request and a reviewer of another, holding both roles at once. So `member` has no role column; roles are derived from facts, per pull request. Verdicts are therefore computed **per member**: the same pull request yields different conclusions for different members.

## 5. Rule model

Rules live in the database and are visualized and editable in the dashboard (decided). What remains is how conditions are expressed.

**A. Free-form expressions** (CEL or expr-lang)
- Maximum expressiveness.
- "Editable" becomes "editable by people who can write expressions", and injection and runaway evaluation must be guarded against.

**B. Structured conditions** (recommended)
- A condition is a `(field, operator, value)` triple, combined with AND/OR.
- Editable in the dashboard with drop-downs; no code.
- Expressive enough: every branch in the predecessor had this shape — "unresolved threads > 0", "CI status = failure", "my latest review = dismissed".
- Cost: a condition that truly cannot be expressed means adding a field or an operator.

**C. Both** (structured by default, with an expression escape hatch)
- Flexible, but the dashboard needs two editors, and an escape hatch, once open, gets used for everything.

Recommendation: B. Start structured and open an escape hatch only when a real wall is hit — the reverse cannot be taken back.

Rules form an **ordered list; the first match wins** — the predecessor's proven semantics, which draw naturally as a tree and naturally explain why later branches were never reached. Each rule carries: the role it applies to, priority, conditions, and a conclusion (who holds the ball / suggested action / whether it can be automated / whether it needs judgement).

Rules are **versioned**. Every edit stores a new version, and each verdict references the version it used. Otherwise, after a bad edit, past verdicts become unexplainable and nobody can tell who changed what.

## 6. Executor contract

This is Park's most important external interface, and the hardest to change once published.

**A. Task queue** (Park dispatches tasks; executors claim them with leases and a state machine)
- Strongest semantics; prevents duplicate execution.
- But Park must then track execution state, expire leases, and handle lost executors — it becomes an orchestrator, which contradicts the scope.

**B. Read-only** (executors pull verdicts and decide for themselves; no claims, no reports)
- Park stays stateless; simplest.
- Two executors will pick up the same work.

**C. Soft claims + optional reports** (recommended)
- `GET /queue?member=…` returns what should be done — verdicts, not tasks.
- `POST /claims` optionally claims an item with a TTL, only so other executors can see "someone is on it".
- `POST /reports` optionally reports progress or results, only so the dashboard updates sooner.
- **Park does not depend on either.** An expired claim is harmless; a missing report is harmless; forge facts move things forward on their own.

The point of C is that "optional" is real: disable claims and reports entirely and Park behaves **exactly the same**, just with a slower dashboard. That is the fundamental difference from a task queue. It is also the only shape that works with loopback-only executors (section 2): every call originates on the executor's side.

## 7. Identity and authentication

Three ways to sign in, all resolving to the same identity, plus one machine identity:

| Method | Suits | Forge account link |
|---|---|---|
| Local account | Self-hosting, zero dependencies | Needs verification (admin approval, or the user authorizing once with the forge) |
| Forge OAuth | The most natural fit | Linked and verified by signing in |
| OIDC | Enterprise SSO | From a claim, or linked on first sign-in |
| API token | Executors and other machines | Belongs to a member; revocable and scoped |

```
member            a person
member_identity   (member_id, provider, subject)   provider ∈ local | oidc | <forge kind>
api_token         (member_id, name, hash, scopes, last_used_at)
```

A forge identity's `subject` is the forge's **stable account ID**, and it is what roles are derived from. Linking must be verified: if anyone could type in any username, anyone could see anyone's queue.

One member, many identities — so that "start with a local account, connect SSO later" never means recreating a person. Cheap on day one, expensive to retrofit.

Security baseline (not negotiable): the first start walks through creating an administrator, and **no default account ever exists**; passwords use argon2id; failed sign-ins are rate-limited; tokens are shown in plaintext exactly once, at creation.

## 8. Tech stack (decided)

| Layer | Choice |
|---|---|
| Backend | Go, standard library `net/http` (the Go 1.22+ ServeMux is enough; no router framework) |
| Database | Postgres |
| Frontend | React + Vite + TypeScript, built output embedded with `go:embed` |
| Contract | OpenAPI as the single source; generate both sides (`oapi-codegen` / `openapi-typescript`) |
| Live updates | Server-Sent Events |
| Authentication | Identity-provider abstraction: local / forge / OIDC, plus API tokens |
| Forge access | Credential-provider abstraction: app installation first, personal token for quick start |
| Smart judgement | Provider abstraction, bring your own key; off unless configured |
| Repository | Monorepo, without monorepo tooling |
| License | MIT |
| Language | English throughout: code, comments, commits, docs |

Layout:

```
Park/
  cmd/park/        server entry point (the binary is `park`)
  internal/        fact layer / decisions / rule engine / collectors / identity
  web/             frontend (Vite); the build output is embedded
  docs/
```

### Neutral vocabulary

Domain terms are forge-neutral — `forge`, `repo`, `pull_request`, `review`, `account` — in tables, columns, API paths, and configuration keys. The forge kind is a value (`github`), never part of a name. GitHub is the only implementation at first; adding another forge means adding a collector, not changing the schema. There is no adapter layer until a second forge actually exists.

The frontend never learns the deployment topology: it calls same-origin relative paths (`/api/...`) and contains no hostnames.

## 9. Still open

- **What to port from the predecessor** (ballpark): its collector (~1,600 lines), model-judgement client (~1,000), and grouping/clustering (~1,000) — about 3,600 lines. Its decision code is not ported (rules move into the database), nor its session spawning (Park does not execute), nor its spreadsheet integration. Ported code must be rewritten as it moves: the predecessor's comments and tests reference real repositories, pull request numbers, and colleagues' usernames, none of which belong in a public repository.
