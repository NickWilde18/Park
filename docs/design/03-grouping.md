# 3. Grouping

Pull requests rarely stand alone. A feature often spans repositories — a backend and a frontend pull request — and a pull request may sit in a stacked chain. This stage maintains both relations: **chains** (this one depends on that one) and **groups** (these are one piece of work).

## Chains are facts; groups are judgments

A chain is structural: a pull request whose base branch is another open pull request's head branch, in the same repository, depends on it. Chains are derived from forge facts and cannot be edited — the dependency is in the git graph whether or not anyone agrees.

A group is a judgment: "these belong together" can come from several places with different reliability. Being stacked does not by itself make pull requests one feature — a chain of separate refactors is still a chain, and each of its pull requests is reviewed and merged on its own — so chains and groups are separate relations. The dashboard nests chains inside groups where both apply.

## The aggregator resolves; it does not collect

```
 forge facts  ─▶ structural ─┐
 dashboard    ─▶ manual     ─┤
 bridges      ─▶ external   ─┼─▶ assertions ─▶ resolve ─▶ groups (stable IDs)
 unplaced PRs ─▶ model      ─┘                             + chains (git graph)
                                                                 │
                                          verdicts · task units · dashboard
```

The grouping stage takes **assertions** and produces **groups**. It gathers nothing itself: forge facts arrive through collection ([Facts](02-facts.md)), and outside signals arrive from bridges through the assertion API.

The dashboard draws this picture and lets each box be configured — which sources are on, whether the model runs, its confidence threshold — but the boxes cannot be rewired.

## Sources and assertions

Sources, from highest authority to lowest:

1. **Manual** — a member moves, splits, merges, creates, or renames groups on the dashboard.
2. **External** — programs outside Park post assertions through the API: a bridge that reads review threads from a chat tool, or epics from an issue tracker. Park ships no such integration; it only accepts assertions.
3. **Structural** — built in, derived from forge facts (below).
4. **Model** — optional: placements for pull requests that nothing else explains. Off unless a model provider is configured.

External and structural sources do not know Park's group IDs; they can only say which pull requests go together. Members and the model work against existing groups. So assertions come in two shapes:

| Shape | Says | Sent by | Example |
|---|---|---|---|
| Co-membership | These pull requests belong together, under an anchor | External, structural | A chat thread links `api#12` and `web#7` (anchor: the thread's ID); both repositories have a branch `feat/schedule` (anchor: the branch name) |
| Placement | Pull request X is, or is not, in group G | Manual, model | A member drags `web#11` into "Scheduled tasks"; the model places `api#15` in an existing group |

## Structural signals

| Signal | Anchor | Effect |
|---|---|---|
| Two or more pull requests close the same issue | `issue:<issue ID>` | Asserted |
| The same head branch name in two or more repositories | `branch:<name>` | Asserted |
| Pull requests that reference each other, in both directions; connected pairs form one assertion | `refs:<oldest pull request in the set>` | Asserted |
| A one-way reference across repositories, between pull requests with similar branch names | — | Suggestion |

- **Never within a chain.** No structural signal pairs two pull requests of the same chain. Stacked pull requests reference each other all the time ("based on #41"), yet each is reviewed and merged on its own; only members and external sources can group them.
- **Similar branch names** share at least two words, making up more than half of the shorter name's words. Words that appear in more than a fifth of all open pull requests' branch names are ignored: which words are noise depends on the team, so it is measured rather than listed.
- **One-way references within one repository are not a signal.** A repository's pull requests for one epic reference each other and share words naturally.
- **Some branch names never form a group**: default branches, names a forge generates for web edits, and dependency-update prefixes. The list is configurable.

## Data

| Record | Holds |
|---|---|
| `group` | Group ID, name, where the name came from, state (active or archived), creation time |
| `group_alias` | An absorbed group's ID and the group it now resolves to |
| `anchor` | Source and anchor key, and the group the anchor maps to |
| `assertion` | Shape; source; either an anchor with a set of pull requests (co-membership) or a pull request with a group and a negative flag (placement); an optional title; evidence; for the model, the fingerprint, confidence, and reason; creation and retraction times |
| `membership` | Pull request, group, the assertion that placed it, since when |
| `suggestion` | Pull request, proposed group, reason, source (structural or model), state (open, accepted, dismissed) |

Chains are derived on read from `pr_fact` and are not stored.

## Flow: derive chains

When a pull request's branches change, or a pull request opens or closes:

1. Build the dependency edges within the repository: X depends on Y when X's base branch is Y's head branch and Y is open.
2. A chain is a connected set of such edges. Its root is the pull request that depends on no open pull request. A pull request's position is its depth from the root plus one; the chain's length is the number of pull requests in it.
3. Two pull requests based on the same parent make the chain a tree. Positions are still depths, and the whole tree is one chain.
4. Cycles — a transient state while someone changes a base branch — form no chain until they resolve.

## Flow: resolve memberships

Runs for one open pull request when its facts change, when an assertion that names it is added or retracted, or when a member acts on it. The pull requests of one assertion are resolved in one transaction, so an anchor is mapped only once.

1. **Refresh structural assertions** for the pull request: recompute its signals, retract assertions whose signal is gone, and add new ones.
2. **Collect live claims**: placements that name the pull request, and co-membership assertions that include it.
3. **Find each claim's group.** A placement names its group, following aliases. A co-membership claim goes through its anchor. An anchor not mapped yet would map to the active group that holds the most of the assertion's open pull requests — the oldest such group on a tie — or to a new group if no group holds any.
4. **Drop excluded claims**: those whose group a member has excluded for this pull request.
5. **Keep the highest authority** among the remaining claims.
6. **Break ties** within it. Manual and external claims: the newest wins — when a pull request is resubmitted for review, teams open a new thread, and the new thread is where it belongs. Structural claims: closing the same issue beats the same branch name, which beats mutual references. Still tied: a claim that keeps the pull request in its current group wins; otherwise the oldest assertion does.
7. **Ask the model** if no claim remains and the model source is on (next flow).
8. **Write the change.** If the winning claim's anchor was unmapped, map it now, creating the group if needed. If the pull request's group changed, record the new membership with the assertion that placed it, and queue work for verdicts, tasks, and the dashboard.
9. **Open suggestions** for structural claims that lost and point to a different group than the one the pull request ended up in — "`mobile#4` has the same branch name as `web#7`, `feat/schedule`, and `web#7` is in 'Scheduled tasks'; add it?" — unless the same suggestion is open or was dismissed. Weak signals open suggestions the same way.
10. **Archive** groups left with no open pull requests, and release their anchors: a later claim under the same anchor starts a new group.

## Flow: place with the model

For an open pull request left with no claim at all:

1. **Pick candidates**: the active groups that share the most words with the pull request's branch name and title, at most ten.
2. **Fingerprint** the question: the pull request's title, head branch, repository, and references, and the candidates' IDs. The description is not sent: it is long and costly, and pulls the model toward details that do not matter for grouping.
3. **Use a recorded answer** for that fingerprint if there is one. Otherwise ask, synchronously and with a timeout: which candidate does this pull request belong to — or does it start a new group, under what name — or neither?
4. **Validate** the answer. One that names a group that was not a candidate is discarded as an error.
5. **Record** the answer as a model assertion, with its confidence and reason.
6. **Apply** it. At or above the threshold, the answer is a model-source placement, creating the proposed group if it named a new one. Below the threshold, it becomes a suggestion that takes effect only if a member accepts it. "Neither" leaves the pull request alone.
7. **On a timeout**, the call finishes in the background; its answer is recorded when it arrives, and resolution runs again. **On an error**, the question is retried with backoff.

A pull request the model placed keeps its placement until its own inputs change: the model never re-decides a placement because the set of groups moved. A pull request it left alone, or placed below the threshold, is asked again when its fingerprint changes — its own inputs, or its candidate list.

## Flow: manual actions

| Action | Records |
|---|---|
| Move P into G | A placement of P in G, replacing P's earlier manual placement and lifting any exclusion of P from G |
| Take P out of G | An exclusion: a negative placement of P against G, replacing P's manual placement in G. P falls back to its next-best claim, or stands alone |
| Create a group from P and Q | A new group, and placements of P and Q in it |
| Merge G2 into G1 | An alias from G2 to G1. G2's anchors, memberships, and placements move to G1, and exclusions from G2 now exclude G1; where that contradicts a placement in G1, the newer of the two stands |
| Split P and Q out of G | A new group, placements of P and Q in it, and exclusions of P and Q from G |
| Rename G | The new name, with its source recorded as manual |
| Accept a suggestion | A manual placement of the suggested pull request in the suggested group |
| Dismiss a suggestion | The dismissal; the same suggestion is not opened again |

## Flow: bridge assertions

1. A bridge sends `POST /assertions` with an anchor, a list of pull requests given as repository name and number, and optionally a title and evidence.
2. Park takes the source from the bridge's API token, not from the request, so one bridge cannot speak for another.
3. It resolves each repository name to a watched repository, and rejects references to repositories it does not watch.
4. If a live assertion exists under the same source and anchor, it is retracted, and the new one is appended: history is kept, and only one assertion per anchor is live.
5. An assertion that names more pull requests than the configured maximum is recorded but not applied: a message that lists many pull requests is usually a progress report, not a unit of review. The dashboard lists such assertions on the grouping settings page.
6. Resolution runs for every pull request in the old and new sets.

`DELETE /assertions?anchor=…` retracts the live assertion under that anchor, for example when the thread is deleted. Anchors are the external system's stable IDs, never its URLs.

## Group names

A group takes its name from the highest source that offers one: a manual rename, then an external assertion's title, then the branch name of a branch anchor, then the name the model proposed. The name changes when a higher source offers one, or when the source it came from changes it — a thread is retitled.

## A worked example

1. `api#12` and `web#7`, in the `api` and `web` repositories, both come from a branch named `feat/schedule`. The structural source asserts co-membership under the anchor `branch:feat/schedule`. No group holds either pull request, so group G is created, named "feat/schedule".
2. A bridge reads a chat thread titled "Scheduled tasks" that links both, and posts an assertion under the thread's anchor. External beats structural for both pull requests. The thread's anchor is new, and G holds all of its pull requests, so the anchor maps to G: no new group appears. G takes the thread's title as its name.
3. Later `mobile#4` opens in a third repository, `mobile`, from a branch also named `feat/schedule`; the thread does not mention it. Its only claim is the branch anchor, which maps to G, so it joins G, marked "same branch name".
4. An unrelated `api#15` appears, and no source claims it. The model proposes a new group, "Billing", with confidence 0.55 — below the threshold — so the dashboard shows a suggestion, and nothing changes unless a member accepts it.

The dashboard now shows: **Scheduled tasks** → `api#12` (external · thread), `web#7` (external · thread), `mobile#4` (structural · same branch name).

5. The mobile team says `mobile#4` is a separate change, and a member takes it out of G. The exclusion drops its branch claim, so it stands alone, and no suggestion reopens: the exclusion was explicit.

## Used by

- **Verdicts** read each pull request's chain position and group size (the `chain.*` and `group.*` fields in [Rules](05-rules.md)).
- **Tasks** use groups and chains as units of work and as locks ([Tasks](06-tasks.md)).
- **The dashboard** nests group → chain → pull request, and marks each pull request with the source that placed it.

## Rules

- **G1.** Chains are derived from branches and cannot be edited.
- **G2.** Each open pull request belongs to at most one group; one with no winning claim stands alone. A merged or closed pull request keeps the group it had when it closed.
- **G3.** Authority order: manual, external, structural, model. The highest claim present wins.
- **G4.** Ties are broken as in step 6 of the resolution flow; the last resort keeps a pull request where it is.
- **G5.** Claims are never unioned. Union is transitive: a few weak links — A to C, C to D, D to E — would glue unrelated features into one group.
- **G6.** Only members exclude, and an exclusion holds against every source.
- **G7.** An anchor maps to at most one group. The mapping is fixed when the anchor is first used, and changes only when groups merge (it is re-pointed) or its group is archived (it is released).
- **G8.** Group identity is independent of membership. Merges leave aliases; groups are archived, never deleted.
- **G9.** Structural signals never pair two pull requests of the same chain.
- **G10.** A structural claim that loses becomes a suggestion; weak signals are only ever suggestions.
- **G11.** The model places only pull requests with no other claim, one at a time, recorded by fingerprint. It never moves a pull request placed by another source, and keeps its own placements until the pull request's inputs change.
- **G12.** Every membership records the assertion that placed it.
- **G13.** Assertions are append-only. Reposting under the same source and anchor retracts the previous one.

## Defaults

| Setting | Default |
|---|---|
| Model placement threshold | 0.7 |
| Model call timeout | 20 seconds |
| Candidates per model question | 10 |
| Largest external assertion applied | 3 pull requests |
| Branch words ignored for similarity | Those in more than a fifth of open pull requests' branch names |
| Branch names never grouped | `main`, `master`, `develop`, `dev`, `trunk`, `patch-*`, `dependabot/*`, `renovate/*` |
