# Park — Architecture Design

> Status: all sections decided (§3–§9). §10 lists details to settle during implementation.

## 1. What Park is

In one sentence: **the team-level source of truth for "whose turn is it, and to do what" — plus a dashboard, a verified task queue for executors, and a contract.**

| Park does | Park does not |
|---|---|
| Collect facts from forges (webhooks first, polling as a backstop) | Start AI sessions, manage worktrees, or manage agent lifecycles |
| Group pull requests into units of work | Ship integrations for chat tools or issue trackers |
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

A reported result is never taken at face value. The executor says what it did; the forge facts say what is true. Park checks the second against the task's post-condition (§7).

## 3. Fact layer (decided)

### The problem

Most of the confusion in Park's predecessor came from four layers reading each other: verdicts read raw collection results, the state machine read verdicts, the display read the state machine. When any layer lagged, the board rendered rows that contradicted themselves (a session marked Done while the state column still said "in progress").

Park's goal: **a verdict is a pure function `(facts, rules) → conclusion`.** Facts are the only input — model judgements included, recorded as facts (§5) — and every verdict can be traced back and replayed.

Prior art reaches the same principle independently: Agent Orchestrator stores durable facts and computes display status at read time rather than storing it.

### Design

- `fact_event`: an immutable event stream. Webhook deliveries and polling diffs **both land here**; nothing downstream can tell them apart.
- `pr_fact`: current facts, updated from events, for fast reads by the dashboard, the rule engine, and task verification.
- `verdict`: a decision result that **embeds a snapshot of the facts it was computed from (JSONB)**.

The third point is what matters: answering "why was this judged that way three days ago?" needs no replay — the inputs, the rule that matched, and the conclusion are all in one row. The cost is a few KB per verdict; at a few hundred pull requests and a few hundred verdicts a day, that is well under 1 GB a year.

Two alternatives were set aside. Keeping only current facts is simpler but cannot answer "why", the question people ask most. Pure event sourcing answers it but makes every verdict walk history, and leaves out-of-order delivery, duplicates, and snapshot points to be handled by hand.

### Unifying webhooks and polling

Both paths produce the same thing — fact-change events — and differ only in how a change is discovered:

- Webhook: receive a delivery → verify the signature → fetch the full current state of the affected object → diff against `pr_fact` → write an event only if something changed.
- Polling: fetch on a schedule → the same diff → the same write.

Both fetch the full state and diff, rather than trusting the payload, because forges **guarantee neither delivery nor ordering**. After a diff, duplicate deliveries are naturally idempotent and out-of-order ones cost at most one empty diff. Events record their `source` (webhook / poll) for observability only; rules never look at it.

### Stable identifiers only

Facts store stable forge identifiers — repository ID, pull request number, account ID — never URLs and never usernames. URLs are built at render time. Usernames can be renamed: a stored login silently stops matching, while a numeric account ID does not.

## 4. Grouping (decided: assertions from several sources, resolved by authority)

Pull requests rarely stand alone. A feature often spans repositories — a backend and a frontend pull request — and a pull request may sit in a stacked chain. Park needs both relations: **groups** (these pull requests are one piece of work) and **chains** (this one depends on that one).

### Chains are facts; groups are judgments

A chain is structural: a pull request whose base branch is another pull request's head branch depends on it. Park derives chains from forge facts and nobody can override them — the dependency is in the git graph whether or not anyone agrees.

A group is a judgment: "these belong together" can come from several places with different reliability. Being stacked does not by itself make pull requests one feature — a chain of unrelated refactors is still a chain — so chains and groups are separate relations. The dashboard nests chains inside groups when both apply.

### The aggregator resolves; it does not collect

```
 forge facts  ─▶ structural ─┐
 dashboard    ─▶ manual     ─┤
 bridges      ─▶ external   ─┼─▶ assertions ─▶ resolve ─▶ groups (stable IDs)
 unplaced PRs ─▶ model      ─┘                             + chains (git graph)
                                                                 │
                                          verdicts · task units · dashboard
```

The grouping stage takes **assertions** and produces **groups**. It gathers nothing itself: forge facts arrive through the collectors (§3), and outside signals arrive from bridges through the open API.

This is the picture "several sources, then a judgement step, then the next stage". The dashboard draws it and lets each box be configured — which sources are on and in what order, whether the model runs, its confidence threshold — but the boxes cannot be rewired.

### Four sources, two shapes of assertion

Sources, from highest authority to lowest:

1. **Manual** — a member joins, splits, merges, or creates groups on the dashboard.
2. **External** — programs outside Park post assertions through the open API: a bridge that reads review threads from a chat tool, or epics from an issue tracker. Park ships no such integration; it only accepts assertions.
3. **Structural** — built in, from forge facts: the same head branch name across repositories, pull requests that reference each other, pull requests that close the same issue. Weaker signals, such as a one-way reference between similarly named branches, are shown as suggestions and never asserted.
4. **Model** — optional, like smart judgement: placements for pull requests that nothing else explains. Off unless a provider is configured.

External and structural sources do not know Park's group IDs; they can only say which pull requests go together. Members and the model work against existing groups. So assertions come in two shapes:

| Shape | Says | Sent by | Example |
|---|---|---|---|
| Co-membership | These pull requests belong together, under an anchor | External, structural | A chat thread links `api#12` and `web#7` (anchor: the thread's ID); both repositories have a branch `feat/schedule` (anchor: the branch name) |
| Placement | Pull request X is, or is not, in group G | Manual, model | A member drags `web#11` into "Scheduled tasks"; the model places `api#15` in an existing group |

### Resolution: one group per pull request, the higher authority wins

- Each open pull request belongs to at most one group.
- The claim with the highest authority wins. Between claims of the same authority, the newest wins: when a pull request is re-submitted for review, teams open a new thread, and the new thread is where it belongs.
- Negative placements ("not in G") come only from members, and outrank every other source.
- A structural link that a higher source overrides is neither dropped nor applied silently. It becomes a suggestion on the dashboard: "`mobile#4` shares branch `feat/schedule` with `web#7`, which is in 'Scheduled tasks' — add it?"
- Resolution is a deterministic function of the previous memberships, the assertions, and the facts, and every membership records the assertion that placed it. Groups are therefore derived like everything else, and any verdict or task can show which grouping it saw.

Claims are not unioned, because union is transitive: a few weak links — A to C, C to D, D to E — glue two unrelated features into one group. Picking one winner per pull request cannot do that.

### Keeping groups stable

- **Anchors.** An anchor — a thread ID, a branch name, an issue ID — always maps to the same group. While an anchor stands, so does its group. Outside the model source, this is what keeps groups from reshuffling.
- **Identity is independent of membership.** A group has its own ID; pull requests join and leave it over time. The predecessor derived group IDs from the member list, so every regrouping looked like one group vanishing and another appearing: cooldowns reset, pull requests that were still open were announced as merged, and the same alert went out again and again.
- **Names come from the best source**: an external title, then a manual rename, then a branch name, then a model-proposed name.
- **Merging** folds one group into another. The absorbed group's ID stays as an alias, so everything that referenced it still resolves.
- **Splitting** moves pull requests into a new group and records a negative placement against the old one, so no lower-authority source can put them back.
- **Groups are archived, never deleted**, once every member is merged or closed; past verdicts and tasks still reference them.

### How the model source places pull requests without reshuffling

Model clustering is where the predecessor's reshuffling came from: it re-clustered every unexplained pull request together whenever that set changed, so one new pull request re-rolled everyone's grouping, and the same question asked twice could get a different answer. The model source works differently:

- **Incremental, not global.** When a pull request appears that no higher source places, the model is asked one question: which existing group does it belong to, or none? Pull requests already placed are not re-decided.
- **Recorded, never asked twice.** The answer is an assertion keyed by the inputs it saw, like any judgement (§5). The model is asked again only if those inputs change materially — the pull request is retitled, or its references change.
- **Joins by ID.** The model proposes joining a group by its stable ID or creating a new one; it never renumbers.
- **A confidence threshold.** Above it, the assertion applies automatically as a model-source assertion. Below it, the dashboard shows a suggestion that a member accepts with one click — which makes it a manual assertion.

### A worked example

1. `api#12` and `web#7` are both on branch `feat/schedule`. The structural source asserts co-membership under the anchor `branch:feat/schedule`, and group G is created.
2. A bridge reads a chat thread titled "Scheduled tasks" that links both. The external source, with higher authority, asserts co-membership under the thread's anchor. Both pull requests are already in G, so no new group appears: G gains the thread anchor and takes the thread's title as its name.
3. Later `mobile#4` opens on `feat/schedule`, and the thread does not mention it. Its only claim is the branch anchor, which maps to G, so it joins G, marked "same branch".
4. An unrelated `api#15` appears and no source claims it. The model answers "Billing" with confidence 0.55, below the threshold, so the dashboard shows a suggestion; nothing changes unless a member accepts it.

The dashboard shows: **Scheduled tasks** → `api#12` (external · thread), `web#7` (external · thread), `mobile#4` (structural · same branch).

### Downstream

- Verdicts can conclude at the group level: "nothing in this group is waiting on me".
- The task queue uses groups and chains as units (§7). A group is busy while any of its pull requests is in the scope of an active task, so a chain that spans two groups occupies both.
- The dashboard nests group → chain → pull request, and marks each member with the source that placed it.

### The assertion API for bridges

- `POST /assertions` submits a co-membership assertion. It is idempotent by source and anchor: posting the same thread again updates that assertion rather than creating a second group.
- `DELETE /assertions?anchor=…` retracts one, for example when the thread is deleted.
- Bridges authenticate with an API token scoped to `assertions:write` (§8). The source is taken from the token, not from the request, so one bridge cannot speak for another.
- Anchors are the external system's stable IDs, never its URLs.

### Alternatives considered

Structural signals plus manual grouping alone were set aside: they miss groupings that exist only in people's heads or conversations, which in the predecessor were the most accurate source of all. Model-first clustering corrected by people was set aside because clustering output shifts from run to run, and every reshuffle ripples into verdicts, tasks, and notifications.

## 5. Decision layer (decided)

### Model calls: synchronous, recorded, never asked twice

When the rule engine reaches a branch marked "judgement needed", it looks for a judgement fact for **exactly these inputs** — a fingerprint of the facts the judgement depends on.

- **Found**: use it. No model call.
- **Not found, provider configured**: call the model synchronously, with a timeout. Record the answer — with its confidence and the fingerprint it answered — as a judgement fact, then continue down the branch it selects.
- **Not found and no provider, or the call timed out or failed**: take the rule's **fallback branch**, a deterministic conservative conclusion. A late answer is still recorded when it arrives, and the next pass uses it.

Recording every answer as a fact keyed by its inputs is what keeps verdicts reproducible: replaying a verdict reads the recorded judgement and never calls the model again, so the same facts always give the same conclusion. It also bounds cost — a judgement is paid for once per distinct input, not once per recompute. Rule logic is tested with recorded judgement facts, without network mocks.

This gives:

- **No lag in the normal case.** A verdict that needs judgement is computed with the answer in hand; the dashboard never shows one conclusion and then switches to another.
- **Smart judgement is optional by construction.** With no provider configured, every judgement branch takes its fallback — no feature flag needed.
- **Visible provenance.** Every verdict references the judgement facts it used, so the dashboard shows which conclusions came from a model, and with what confidence.

The cost: a verdict pass can wait on the model, bounded by the timeout, and concurrent calls are capped so a burst of new inputs cannot stall everything. During a model outage, verdicts that need judgement fall back and switch once answers arrive.

### Roles are a function, not a column

`(member, pull request) → role`. The same person can be the author of one pull request and a reviewer of another, holding both roles at once. So `member` has no role column; roles are derived from facts, per pull request. Verdicts are therefore computed **per member**: the same pull request yields different conclusions for different members.

### From verdicts to tasks

A verdict that says "this member should do X" becomes a task when X is something an executor can do and the member has opted in to automation for it. Verdicts are recomputed freely; tasks are created only on a change of verdict, so recomputation never floods the queue.

## 6. Rule model (decided: structured conditions plus an expression escape hatch)

Rules live in the database and are visualized and editable in the dashboard.

Conditions come in two forms, evaluated by **one engine**:

- **Structured** (the default): `(field, operator, value)` triples combined with AND/OR, edited with drop-downs. Every branch in the predecessor had this shape — "unresolved threads > 0", "CI status = failure", "my latest review = dismissed".
- **Expression** (the escape hatch), for conditions the structured form cannot state.

Structured conditions compile to the same expression form, so there is one evaluator, one test harness, and one audit trail; the structured editor is a view over a subset of the language.

The expression language is **CEL**. It is a published specification designed for user-written policy conditions, with type checking — a malformed rule is rejected when it is saved, not when it first runs — and runtime cost limits, so a slow rule is cut off instead of stalling a verdict pass. It brings in protobuf-related dependencies; as a library used in one place, that cost is acceptable.

An escape hatch, once open, tends to be used for everything. Two mitigations: the structured editor must cover the common fields well enough that there is little reason to leave it, and the dashboard marks expression rules visibly so they stay exceptions.

Rules form an **ordered list; the first match wins** — the predecessor's proven semantics, which draw naturally as a tree and naturally explain why later branches were never reached. Each rule carries: the role it applies to, priority, conditions, and a conclusion (who holds the ball / suggested action / whether it can become a task / whether it needs judgement).

Rules are **versioned**. Every edit stores a new version, and each verdict references the version it used. Otherwise, after a bad edit, past verdicts become unexplainable and nobody can tell who changed what.

## 7. Executor contract (decided: a task queue with verified outcomes)

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

Executors authenticate with API tokens (§8); every lease is recorded against the token that holds it.

### Rules the queue follows

- **One task per unit of work, not per pull request.** A unit is a stacked chain or a feature group (§4), and a group has at most one active task at a time — two agents editing the same code collide.
- **Tasks are idempotent by construction.** Before dispatching, Park checks the post-condition; a task whose post-condition already holds is closed without running. A task re-dispatched after a lost lease is therefore safe.
- **New facts can supersede a task.** If new review comments arrive while a task is queued, the queued task widens to cover them; if they arrive while it is leased, a follow-up task is queued behind it.
- **Retries are bounded**, and the reason for each failed verification is recorded. Unbounded retries burn model quota on an agent that keeps reaching the same conclusion.
- **Park does not care how a task is done.** An AI session, another orchestration tool, or a person at a keyboard can all claim tasks; the contract is the same.

### Costs

Park now tracks leases, expires them, and handles lost executors, and each task type needs a post-condition that can be checked from forge facts. This is the price of never losing track of a task, and it is paid once, in Park, rather than in every executor.

## 8. Identity and authentication (decided)

Three ways to sign in, all resolving to the same identity, plus one machine identity:

| Method | Suits | Forge account link |
|---|---|---|
| Local account | Self-hosting, zero dependencies | Needs verification (admin approval, or the user authorizing once with the forge) |
| Forge OAuth | The most natural fit | Linked and verified by signing in |
| OIDC | Enterprise SSO | From a claim, or linked on first sign-in |
| API token | Executors, bridges, and other machines | Belongs to a member; revocable and scoped |

```
member            a person
member_identity   (member_id, provider, subject)   provider ∈ local | oidc | <forge kind>
api_token         (member_id, name, hash, scopes, last_used_at)
```

A forge identity's `subject` is the forge's **stable account ID**, and it is what roles are derived from. Linking must be verified: if anyone could type in any username, anyone could see anyone's queue.

One member, many identities — so that "start with a local account, connect SSO later" never means recreating a person. Cheap on day one, expensive to retrofit.

Security baseline (not negotiable): the first start walks through creating an administrator, and **no default account ever exists**; passwords use argon2id; failed sign-ins are rate-limited; tokens are shown in plaintext exactly once, at creation.

## 9. Tech stack (decided)

| Layer | Choice |
|---|---|
| Backend | Go, standard library `net/http` (the Go 1.22+ ServeMux is enough; no router framework) |
| Database | Postgres (facts, verdicts, groups, rules, and the task queue) |
| Rule expressions | CEL |
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
  internal/        fact layer / grouping / decisions / rule engine / task queue / collectors / identity
  web/             frontend (Vite); the build output is embedded
  docs/
```

### Neutral vocabulary

Domain terms are forge-neutral — `forge`, `repo`, `pull_request`, `review`, `account` — in tables, columns, API paths, and configuration keys. The forge kind is a value (`github`), never part of a name. GitHub is the only implementation at first; adding another forge means adding a collector, not changing the schema. There is no adapter layer until a second forge actually exists.

The frontend never learns the deployment topology: it calls same-origin relative paths (`/api/...`) and contains no hostnames.

## 10. Still open

- **Lease durations and retry limits** per task type (§7).
- **What to port from the predecessor** (ballpark): its collector (~1,600 lines), model-judgement client (~1,000), and grouping/clustering (~1,000) — about 3,600 lines. Its decision code is not ported (rules move into the database), nor its session spawning (Park does not execute), nor its spreadsheet integration. Ported code must be rewritten as it moves: the predecessor's comments and tests reference real repositories, pull request numbers, and colleagues' usernames, none of which belong in a public repository.
