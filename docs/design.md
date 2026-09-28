# Park — Architecture Design

> Status: draft. **Decided:** rule model (§5), executor contract (§6), tech stack (§8). **Recommended, pending confirmation:** fact layer (§3), decision layer (§4), identity (§7). No implementation code until the remaining sections are settled.

## 1. What Park is

In one sentence: **the team-level source of truth for "whose turn is it, and to do what" — plus a dashboard, a verified task queue for executors, and a contract.**

| Park does | Park does not |
|---|---|
| Collect facts from forges (webhooks first, polling as a backstop) | Start AI sessions, manage worktrees, or manage agent lifecycles |
| Decide, by rules, who holds the ball and what should happen next | Carry out any work on a pull request itself |
| Turn decisions into tasks, lease them to executors, and verify every outcome against forge facts | Care how an executor does the work — an AI session, another tool, or a person |
| Member identity and roles | |
| Web dashboard and open API | |

Park owns the **task** lifecycle; executors own the **agent** lifecycle. Executors already exist — local tools such as the author's ballpark, or a bridge driving Agent Orchestrator — each owning the worktrees and sessions on its own machine. Park provides what they all lack: multiple members, roles, a server, a verified task queue, and a contract.

### Where Park differs from what forges already offer

GitHub's own pull request dashboard (generally available since July 2026, open to every user) already covers the personal inbox: review requests, split into direct and team requests; pull requests that need fixing; pull requests ready to merge. Park does not compete on that list. It does what the inbox does not:

- **Whose turn it really is**, reasoned from thread state: the author answered every comment but never re-requested review; a blocking review was dismissed; comments were left unaddressed.
- **Work that spans repositories**: a backend and a frontend pull request are one feature.
- **Stacked chains**: reviewing the fifth pull request is wasted while the second is unmerged.
- **The team view**: what each member should do, not only one person's inbox.
- **Rules that can be seen and edited**, and a reason for every verdict.
- **A task contract any executor can implement**, with every reported outcome checked against the forge.

## 2. Division of labor with executors

```
   forge ──webhook/poll──▶ Park ──verdicts──▶ dashboard (people)
     ▲                      │
     │                      ├── tasks ◀── claim / heartbeat / report ──  executor
     │                      │                                            (ballpark, a bridge, a person)
     │                      └── verify each reported task against forge facts
     └──────────────── the executor's work lands on the forge ◀───────────┘
```

Every interaction starts from the executor's side: it claims a task, renews the lease while working, and reports a result. This is a requirement, not a style — some executors are loopback-only by design (Agent Orchestrator's daemon binds to `127.0.0.1` and treats that as load-bearing), so Park can never push to them.

A reported result is never taken at face value. The executor says what it did; the forge facts say what is true. Park checks the second against the task's post-condition (§6).

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
- `pr_fact`: current facts, updated from events, for fast reads by the dashboard, the rule engine, and task verification.
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

### From verdicts to tasks

A verdict that says "this member should do X" becomes a task when X is something an executor can do and the member has opted in to automation for it. Verdicts are recomputed freely; tasks are created only on a change of verdict, so recomputation never floods the queue.

## 5. Rule model (decided: structured conditions plus an expression escape hatch)

Rules live in the database and are visualized and editable in the dashboard.

Conditions come in two forms, evaluated by **one engine**:

- **Structured** (the default): `(field, operator, value)` triples combined with AND/OR, edited with drop-downs. Every branch in the predecessor had this shape — "unresolved threads > 0", "CI status = failure", "my latest review = dismissed".
- **Expression** (the escape hatch): a sandboxed expression language, for conditions the structured form cannot state.

Structured conditions compile to the same expression form, so there is one evaluator, one test harness, and one audit trail; the structured editor is a view over a subset of the language. The language must be free of side effects and bounded in evaluation cost, because rules are edited through the dashboard and run on every verdict.

An escape hatch, once open, tends to be used for everything. Two mitigations: the structured editor must cover the common fields well enough that there is little reason to leave it, and the dashboard marks expression rules visibly so they stay exceptions.

Rules form an **ordered list; the first match wins** — the predecessor's proven semantics, which draw naturally as a tree and naturally explain why later branches were never reached. Each rule carries: the role it applies to, priority, conditions, and a conclusion (who holds the ball / suggested action / whether it can become a task / whether it needs judgement).

Rules are **versioned**. Every edit stores a new version, and each verdict references the version it used. Otherwise, after a bad edit, past verdicts become unexplainable and nobody can tell who changed what.

## 6. Executor contract (decided: a task queue with verified outcomes)

This is Park's most important external interface, and the hardest to change once published.

### Why a task queue

Soft claims with optional reports were considered first. They keep Park stateless, but they leave Park unable to tell apart three situations that look identical from the forge: the executor is still working, the executor finished but skipped part of the job, or the executor died. In practice an agent occasionally reports completion while leaving review comments unanswered; with soft claims, the pull request then sits in "needs work" indefinitely and nothing says why. The goal is that this is never missed.

### Why a task queue alone is not enough

A plain task queue records the executor's word. If the executor reports success but did not do the job, the task shows as complete while the pull request stays stuck — the same failure, now with a green checkmark. Closing the gap needs one more piece: **every task type declares a post-condition over forge facts, and a task is complete only when the facts satisfy it.**

| Task type | Post-condition |
|---|---|
| Address review comments | Every thread in the task's scope is resolved, or its latest comment is from the task's member (the ball has been passed back) |
| Fix CI | Required checks pass on a head newer than the one the task started from |
| Resolve conflicts | The pull request is mergeable |
| Review | A review from the assigned member exists after the task started |

### Lifecycle

```
queued ──claim──▶ leased ──report──▶ verifying ──facts satisfy──▶ done
  ▲                 │                    │
  │           lease expires         facts do not satisfy
  │           (no heartbeat)             │
  └──── retry (bounded) ◀────────────────┘
                                         └── retries exhausted ──▶ needs human
```

At every moment a task is in exactly one state, and each state has exactly one meaning. Agents will still make mistakes; what changes is that every mistake is detected and visible — "reported done by executor E, but 3 of 27 threads are unanswered" — instead of a silent stall.

### Endpoints

- `POST /tasks/claim` — atomically claim the next task this executor may take (Postgres `FOR UPDATE SKIP LOCKED`); returns the task with a lease.
- `POST /tasks/{id}/heartbeat` — renew the lease while working.
- `POST /tasks/{id}/report` — report a result, with a structured account of what was done; moves the task to `verifying`.
- `POST /tasks/{id}/release` — give a task back without a result.

Executors authenticate with API tokens (§7); every lease is recorded against the token that holds it.

### Rules the queue follows

- **One task per unit of work, not per pull request.** A unit is a stacked chain or a feature group, and a group has at most one active task at a time — two agents editing the same code collide.
- **Tasks are idempotent by construction.** Before dispatching, Park checks the post-condition; a task whose post-condition already holds is closed without running. A task re-dispatched after a lost lease is therefore safe.
- **New facts can supersede a task.** If new review comments arrive while a task is queued, the queued task widens to cover them; if they arrive while it is leased, a follow-up task is queued behind it.
- **Retries are bounded**, and the reason for each failed verification is recorded. Unbounded retries burn model quota on an agent that keeps reaching the same conclusion.
- **Park does not care how a task is done.** An AI session, another orchestration tool, or a person at a keyboard can all claim tasks; the contract is the same.

### Costs

Park now tracks leases, expires them, and handles lost executors, and each task type needs a post-condition that can be checked from forge facts. This is the price of never losing track of a task, and it is paid once, in Park, rather than in every executor.

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
| Database | Postgres (facts, verdicts, rules, and the task queue) |
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
  internal/        fact layer / decisions / rule engine / task queue / collectors / identity
  web/             frontend (Vite); the build output is embedded
  docs/
```

### Neutral vocabulary

Domain terms are forge-neutral — `forge`, `repo`, `pull_request`, `review`, `account` — in tables, columns, API paths, and configuration keys. The forge kind is a value (`github`), never part of a name. GitHub is the only implementation at first; adding another forge means adding a collector, not changing the schema. There is no adapter layer until a second forge actually exists.

The frontend never learns the deployment topology: it calls same-origin relative paths (`/api/...`) and contains no hostnames.

## 9. Still open

- **Expression language for §5.** CEL (a specification, evaluation cost limits, widely used for policy) or expr-lang (pure Go, lighter). Both are free of side effects.
- **Lease durations and retry limits** per task type (§6).
- **What to port from the predecessor** (ballpark): its collector (~1,600 lines), model-judgement client (~1,000), and grouping/clustering (~1,000) — about 3,600 lines. Its decision code is not ported (rules move into the database), nor its session spawning (Park does not execute), nor its spreadsheet integration. Ported code must be rewritten as it moves: the predecessor's comments and tests reference real repositories, pull request numbers, and colleagues' usernames, none of which belong in a public repository.
