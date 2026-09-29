# 6. Tasks

This is Park's most important external interface, and the hardest to change once published.

A **task** is work that a verdict calls for, leased to an executor, and complete only when forge facts confirm it. Executors are programs or people; Park never does the work itself.

## Responsibilities

- Turn changed verdicts into tasks, for members who turned automation on.
- Lease tasks to executors, so that no two work on the same code at once.
- Verify every outcome against forge facts, retry within limits, and hand what cannot be fixed to a person.

## Data

| Record | Holds |
|---|---|
| `task` | Member, unit, state, priority (the highest among the parts behind its items), attempt number, lease (the token holding it, the executor's label, expiry), earliest time for the next attempt, state times |
| `task_item` | Task, pull request, action, targets (thread IDs, check names, or reviewer accounts), the facts at the start of the attempt, state (open, satisfied, or dropped) |
| `task_event` | Every state change, report, heartbeat note, and verification result, with its actor: an executor's token, a member, or Park. Never updated or deleted |
| `automation` | Per member: the actions that may become tasks, optionally limited to some repositories |

## Units and locks

A task works on one **unit** — a group, or a pull request in no group — the same unit whose ball its member holds ([Verdicts](04-verdicts.md#placing-the-ball)). The pull requests of a chain that is in no group are reviewed separately, so each is a unit of its own; a task on one of them still locks the whole chain.

A group is one piece of work, and two agents editing it at once collide; so do two agents in one chain, where changing one pull request moves the ones stacked on it. Hence:

- **At most one queued task per member per unit.** New work for that member and unit joins it.
- **At most one active task — leased or verifying — per group and per chain**, whichever member it is for. A task **locks** every group and chain its items touch, so a chain that spans two groups occupies both.

## Items and post-conditions

A task holds **items**: one action on one pull request, with its targets. Each action declares a **post-condition** over forge facts, and a task is done when every item's post-condition holds.

| Action | Targets | Post-condition | Settled when |
|---|---|---|---|
| `resolve_conflicts` | — | The forge reports the pull request mergeable | Mergeability is known |
| `fix_checks` | The failing required checks | The head commit differs from the one at the start, and every required check on it passes | Every required check on the new head has finished |
| `address_comments` | The threads waiting on the member at the start | Each target thread is resolved, or its latest comment is the member's and newer than the start | At once |
| `request_review` | The reviewers the verdict names; none, meaning anyone | Each target no longer requests changes — the author dismissed the review, or the reviewer lifted it — and has a pending review request; with no targets, someone has a pending request | At once |
| `review` | — | The member submitted a review after the start | At once |

"The member's" means from one of the member's linked forge accounts: the executor acts on the member's behalf, as the member.

A post-condition that has not settled is not judged. Verification waits for the facts to settle, up to a deadline.

## Lifecycle

```
 verdict ─▶ queued ──claim──▶ leased ──report──▶ verifying ──satisfied──▶ done
             │ ▲ ▲             │  │                ▲   │                   ▲
             │ │ └───release───┘  └─lease expires──┘   │                   │
             │ │                                       │ not satisfied     │
             │ └─── retry, if attempts remain ◀────────┤                   │
             │                                         └──▶ needs person   │
             └─────────────── every item already satisfied ────────────────┘
```

| State | Means | Leaves by |
|---|---|---|
| `queued` | Waiting for an executor, a free lock, or the time of its next attempt | A claim (→ leased); facts satisfying every item (→ done); nothing left to do (→ cancelled); a person (→ dismissed) |
| `leased` | An executor holds it | A report or an expired lease (→ verifying); a release (→ queued); nothing left to do (→ cancelled); a person (→ dismissed) |
| `verifying` | Waiting for the facts to confirm it | Every item satisfied (→ done); an item not satisfied once settled, or the deadline passing (→ queued for another attempt, or needs person) |
| `done` | The facts confirmed every item | Final |
| `needs_person` | Its attempts are used up | A person retries it (→ queued, attempts reset) or dismisses it |
| `cancelled` | Nothing is left to do: its pull requests merged or closed, or its member no longer holds the ball for any item | Final |
| `dismissed` | A person closed it, with a reason | Final |

A task is in exactly one state at a time, and each state has exactly one meaning. Agents will still make mistakes; what changes is that every mistake is detected and shown — "reported done by executor E, but 3 of 27 threads have no answer" — instead of a silent stall.

## Flow: create

Runs when a unit's verdict changes, for each member who holds its ball, and for each of their actions in it.

1. **Qualify.** Continue only if the action can be a task, the rule allows it, and the member's automation covers the action for this repository.
2. **Build the item**: the action on its pull request, with its targets — threads and checks from the facts, reviewers from the part.
3. **Keep only what is new.** If its post-condition holds now, there is nothing to do. If an unfinished task of the member already holds the same action on this pull request, only the targets it does not hold go on — so an executor's own progress, which changes the part's reason, never queues a copy of the work it is doing.
4. **Place it.** If the member has a queued task on the unit, add the item there, merging it with any item for the same pull request and action. Otherwise create a queued task — even if an active task holds the unit: the new one waits for the lock as a follow-up.

When a member stops holding a unit's ball, or no longer has an action, the matching items are dropped from their queued task, and a queued task left with no items is cancelled. Items of leased and verifying tasks stay until the attempt ends. When a pull request moves to another group, its items in queued tasks move to the new unit.

## Flow: claim

1. The executor calls `POST /tasks/claim`, naming the actions it can do and, optionally, the repositories it covers and how long it is willing to wait.
2. Park considers the queued tasks of the member who owns the token, whose items the executor can do, and whose next-attempt time has passed — normal priority before low, oldest first.
3. For each candidate, in one transaction, skipping any that a concurrent claim holds:
   1. **Check the lock**: every group and chain the task's items touch must be free of active tasks. If not, try the next candidate.
   2. **Check the post-conditions** against current facts, and drop satisfied items. A task left with none is done without running; try the next candidate.
   3. **Record the start**: for each item, the facts its post-condition compares against — head commit, thread states, check states, review requests.
   4. **Lease it**: state `leased`, a lease expiry, the token, and the executor's label.
4. Return the task — or nothing, after waiting up to the requested time.

The executor receives the task's ID, attempt number, lease expiry, and heartbeat interval, and for each item: the pull request (repository, number, head and base branches, head commit), the action, its targets, why earlier attempts failed verification, and a link to the pull request rendered for this response.

## Flow: work

- The executor sends heartbeats before the lease expires. Each one extends the lease, and may carry a progress note and a flag saying the executor is waiting on its member; the dashboard shows both.
- The heartbeat's answer says whether the task is still leased to this executor. If it is not — the lease expired, the task was cancelled, or a person dismissed it — the executor must stop.
- An executor that has to pause, for example on a usage limit, either keeps sending heartbeats or releases the task.
- Work that arrives meanwhile goes to a follow-up task. The scope of a leased task does not change.

## Flow: report and verify

1. The executor calls `POST /tasks/{id}/report` with an outcome, `done` or `failed`, and for each item what it did: commits pushed, threads answered, a note. Reports are idempotent per attempt.
2. The task moves to `verifying`, whatever the outcome says: a report is a claim, not a fact.
3. Park refreshes every pull request the task touches at once, without waiting for webhooks, and evaluates each item's post-condition as facts arrive.
4. When every item is satisfied, the task is `done`.
5. When an item is not satisfied once its facts have settled, or the deadline passes, the attempt fails. Park records why, item by item — "3 of 27 threads have no answer: T4, T9, T12" — then queues the task again, after a backoff, as the next attempt; or moves it to `needs_person` when its attempts are used up. A task that returns to the queue while a follow-up waits there on the same unit takes over the follow-up's items, and the follow-up is closed, so there is still one queued task per member per unit.
6. Items whose pull request merged or closed meanwhile are dropped.

An expired lease is verified in the same way, as if the executor had reported: it may have finished the work and died before reporting.

## Flow: a person takes over

The dashboard lists tasks that need a person, with every attempt's verification failures. A person can retry the task, which queues it again with its attempts reset, or dismiss it with a reason. A person can also dismiss a queued or leased task at any time.

## The API

| Endpoint | Does |
|---|---|
| `POST /tasks/claim` | Claim the next task this executor may take; returns it with a lease, or nothing |
| `POST /tasks/{id}/heartbeat` | Extend the lease, optionally with a progress note |
| `POST /tasks/{id}/report` | Report the outcome of this attempt; moves the task to verifying |
| `POST /tasks/{id}/release` | Give the task back without a result; the attempt is not counted |

Executors authenticate with API tokens scoped to `tasks:work` ([Identity](07-identity.md)), and every lease is recorded against the token that holds it. A claim retried with the same `Idempotency-Key` returns the same lease instead of a second task.

A claim:

```
POST /api/v1/tasks/claim
{
  "executor": "laptop",
  "actions": ["address_comments", "fix_checks", "resolve_conflicts"],
  "wait_seconds": 30
}

200 OK
{
  "id": "tsk_8f2c",
  "attempt": 2,
  "lease_expires_at": "2026-09-28T10:15:00Z",
  "heartbeat_seconds": 300,
  "items": [
    {
      "id": "itm_41",
      "pull_request": {
        "repo": "acme/api", "number": 12,
        "head": "feat/schedule", "base": "main", "head_commit": "9b1e0c4"
      },
      "action": "address_comments",
      "targets": { "threads": ["T3"] },
      "previous_failures": ["T3: no answer"],
      "link": "https://forge.example.com/acme/api/pull/12"
    }
  ]
}
```

A report:

```
POST /api/v1/tasks/tsk_8f2c/report
{
  "attempt": 2,
  "outcome": "done",
  "items": [
    { "id": "itm_41", "commits": ["c07d2aa"], "threads": ["T3"], "note": "Renamed the option and answered the thread." }
  ]
}
```

## Rules

- **T1. Facts decide.** A task is done only when forge facts satisfy every item's post-condition. A report moves a task to verifying, never to done.
- **T2. One state at a time**, and every change is recorded with its actor and reason.
- **T3. One queued task per member per unit.** New work joins it.
- **T4. One active task per group and per chain**, across members. A task locks every group and chain its items touch.
- **T5. Checked before dispatch.** Items already satisfied are dropped when a task is claimed, and a task left with none is done without running — so dispatching again after a lost lease is safe.
- **T6. A leased task's scope is fixed.** Work that arrives meanwhile goes to a follow-up.
- **T7. Only a report, a release, or an expiry ends a lease** — never facts that already look done. The executor may still be finishing: it has answered every thread but not yet pushed the change it promised, and ending the lease early would let another task start on the same code.
- **T8. An expired lease is verified like a report.**
- **T9. Attempts are bounded.** Every failed verification records its reasons, item by item, and the next attempt receives them.
- **T10. Executors see and claim only their own member's tasks**, and only items whose actions they name.
- **T11. Tasks never feed verdicts** ([V7](04-verdicts.md#rules)).
- **T12. Park does not care how a task is done**, and no action merges or closes a pull request.

## Defaults

These are starting points; see [Open questions](11-open-questions.md).

| Setting | Default |
|---|---|
| Lease | 15 minutes, extended by each heartbeat |
| Heartbeat | Every 5 minutes |
| Attempts | 3 |
| Backoff before another attempt | 10 minutes, doubling |
| Verification deadline | 60 minutes for `fix_checks`; 10 minutes for the rest |
| Longest claim wait | 30 seconds |

## Costs

Park tracks leases, expires them, and deals with lost executors, and every action needs a post-condition that can be checked from forge facts. This is the price of never losing track of a task, and it is paid once, in Park, rather than in every executor.
