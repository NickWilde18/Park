# 3. Grouping

Pull requests rarely stand alone. A feature often spans repositories — a backend and a frontend pull request — and a pull request may sit in a stacked chain. This stage maintains both relations: **chains** (this one depends on that one) and **groups** (these are one piece of work).

## Chains are facts; groups are judgments

A chain is structural: a pull request whose base branch is another open pull request's head branch, in the same repository, depends on it. Chains are derived from forge facts and cannot be edited — the dependency is in the git graph whether or not anyone agrees.

A group is a judgment: "these belong together" can come from several places with different reliability. Being stacked does not by itself make pull requests one feature — a chain of separate refactors is still a chain, and each of its pull requests is reviewed and merged on its own — so chains and groups are separate relations. The dashboard nests chains inside groups where both apply.

A stack that the forge records is a different fact. Its author put the pull requests in it, in order, as one series, so a forge's stack is a structural signal of its own: the one signal that groups pull requests of a chain ([D63](10-design-log.md#d63-a-stack-the-forge-records-is-a-group)).

## The aggregator resolves; it does not collect

```
 forge facts  ─▶ structural ─┐
 dashboard    ─▶ manual     ─┼─▶ assertions ─▶ resolve ─▶ groups (stable IDs)
 bridges      ─▶ external   ─┘         ▲                   + chains (git graph)
 bridges      ─▶ proposals ────────────┤ a member accepts        │
 unplaced PRs ─▶ model ─▶ suggestions ─┘        verdicts · task units · dashboard
```

The grouping stage takes **assertions** and produces **groups**. It gathers nothing itself: forge facts arrive through collection ([Facts](02-facts.md)), and outside signals arrive from bridges through the assertion and proposal APIs. The model does not assert: it suggests, and a suggestion becomes a member's placement only when a member accepts it. So does a bridge's proposal.

The dashboard draws this picture and lets each box be configured — which sources are on, whether the model suggests, its confidence threshold — but the boxes cannot be rewired.

## Sources and assertions

Sources, from highest authority to lowest:

1. **Manual** — a member moves, splits, merges, creates, or renames groups on the dashboard, or accepts a suggestion.
2. **External** — programs outside Park post assertions through the API: a bridge that reads review threads from a chat tool, or epics from an issue tracker. Park ships no such integration; it only accepts assertions. A bridge that cannot tell how a thread's pull requests divide into units — one thread that announces several features, or one large feature — proposes groups instead, and a member accepts them ([bridge proposals](#flow-bridge-proposals)).
3. **Structural** — built in, derived from forge facts (below).

The model, optional and off unless a model provider is configured, is not a source: it only suggests a group for a pull request that nothing else explains, and nothing changes until a member accepts.

External and structural sources do not know Park's group IDs; they can only say which pull requests go together. Members work against existing groups. So assertions come in two shapes:

| Shape | Says | Sent by | Example |
|---|---|---|---|
| Co-membership | These pull requests belong together, under an anchor | External, structural | A chat thread links `api#12` and `web#7` (anchor: the thread's ID); both repositories have a branch `feat/schedule` (anchor: the branch name) |
| Placement | Pull request X is, or is not, in group G | Manual | A member drags `web#11` into "Scheduled tasks", or accepts the model's suggestion to put `api#15` in an existing group |

## Structural signals

| Signal | Anchor | Effect |
|---|---|---|
| The forge records two or more open pull requests in one stack | `stack:<repository ID>/<number>` | Asserted |
| Two or more pull requests close the same issue | `issue:<repository ID>/<number>` | Asserted |
| The same head branch name in two or more repositories, by the same author | `branch:<author ID>/<name>` | Asserted |
| Pull requests that reference each other, in both directions; connected pairs form one assertion | `refs:<oldest pull request in the set>` | Asserted |
| A one-way reference across repositories, between pull requests with similar branch names | — | Suggestion |

- **Never within a chain**, but for a stack. No other structural signal pairs two pull requests of the same chain: of a chain's pull requests in one signal, it keeps the one nearest the root. Stacked pull requests reference each other all the time ("based on #41"), yet each is reviewed and merged on its own; only the forge's stack, members, and external sources group them.
- **Similar branch names** share at least two words, making up more than half of the shorter name's words. Words that appear in more than a fifth of all open pull requests' branch names are ignored: which words are noise depends on the team, so it is measured rather than listed.
- **One-way references within one repository are not a signal.** A repository's pull requests for one epic reference each other and share words naturally.
- **Some branch names never form a group**: default branches, release and hotfix branches, names a forge generates for web edits, and dependency-update prefixes. The list is configurable. Different authors who share a branch name are grouped by members or bridges, not by the name: generic names like `fix-typo` recur by chance.

## Data

| Record | Holds |
|---|---|
| `group` | Group ID, name, where the name came from, state (active or archived), creation time |
| `group_alias` | An absorbed group's ID and the group it now resolves to |
| `anchor` | Source and anchor key — unique together — and the group the anchor maps to |
| `assertion` | Shape; source; either an anchor with a set of pull requests, each as repository and number, so that a bridge can name one Park has not collected yet (co-membership), or a pull request with a group and a negative flag (placement); an optional title; evidence, as IDs in the source's own system and never links; when the claim under its anchor was first seen; creation and retraction times |
| `bridge` | A registered bridge: ID, name, the member who owns it, and the link template used to render its anchors and evidence. Its tokens are its credentials; the bridge, not a token, is the source of its assertions |
| `membership` | Pull request, group, the assertion that placed it, since when |
| `suggestion` | Pull request, proposed group — or, from the model, a proposed new group's name — reason, source (structural or model); for the model, the fingerprint and confidence; state (open, accepted, dismissed) |
| `proposal` | A bridge's proposed groups under an anchor: source, anchor, an optional title, evidence as IDs, a fingerprint of the groups, state (open, accepted, dismissed), who decided and when; creation and retraction times. One proposal per anchor is live |
| `proposal_group` | One proposed group: its position in the proposal, an optional name, and its pull requests as repository and number |
| `group_doc` | A group's description: the group, the random token its link carries, its entry file, how many files and bytes it holds, and who uploaded it and when |
| `group_doc_file` | One file of a description: its path, its content type, and its bytes |

Chains are derived on read from `pr_fact` and are not stored.

## Flow: derive chains

When a pull request's branches change, or a pull request opens or closes:

1. Build the dependency edges within the repository: X depends on Y when X's base branch is Y's head branch, Y's head branch is in this repository, Y is open, and Y's head branch is not long-lived. A fork's branch never has anything stacked on it, and neither does a long-lived branch — the default branch, a protected branch, or a name in the long-lived list — so a release pull request from `develop` to `main` is not the parent of every pull request based on `develop`.
2. When several open pull requests share that head branch — the same change backported to more than one base — X depends on all of them.
3. A chain is a connected set of such edges. Its root is a pull request that depends on no open pull request. A pull request's position is one more than the largest position among those it depends on — 1 for a root — and the chain's length is the number of pull requests in it.
4. Two pull requests based on the same parent make the chain a tree, and the whole tree is one chain.
5. Cycles — a transient state while someone changes a base branch — form no chain until they resolve.

## Flow: resolve memberships

Runs when a pull request's facts change, when an assertion that names it is added or retracted, or when a member acts on it; and once when Park starts, so that a release's changes to what resolution derives apply without waiting for any of those. It resolves the whole **component** — every open pull request connected to the changed ones through live assertions — as one batch, in one transaction, in a canonical order: by authority, then by when the claim was first seen, then by key. The outcome therefore does not depend on the order in which events arrived. In the first release one resolution covers every open pull request, which contains every component ([D50](10-design-log.md#d50-an-anchor-takes-only-a-group-no-other-claim-keeps)).

1. **Refresh structural assertions** for the component: recompute its signals, retract assertions whose signal is gone, and add new ones.
2. **Collect live claims** for each pull request: placements that name it, and co-membership assertions that include it.
3. **Decide each pull request's winning claim**, before any anchor is mapped:
   1. Drop claims a member has excluded: a placement in, or a mapped anchor pointing to, a group the member took this pull request out of.
   2. Keep the highest authority among the rest.
   3. Break ties within it. Manual and external claims: the newest wins — when a pull request is resubmitted for review, teams open a new thread, and the new thread is where it belongs. Structural claims: the same stack beats closing the same issue, which beats the same branch name, which beats mutual references. Still tied: a claim that keeps the pull request in its current group wins; otherwise the oldest assertion does.
4. **Map the anchors** of winning claims that are not mapped yet, in canonical order. Each maps to the active group that holds the most of the pull requests whose winning claim it is, among groups none of them is excluded from and that no other winning claim keeps — a mapped anchor that still has winners, a placement, or an anchor mapped before it — the oldest such group on a tie; or else to a new group. Only those pull requests count, and a group another claim keeps is not up for taking: an anchor never follows a pull request that chose a different claim, so claims cannot chain into a union ([D50](10-design-log.md#d50-an-anchor-takes-only-a-group-no-other-claim-keeps)).
5. **Ask the model for a suggestion** for pull requests with no claim at all, if it is on (next flow). Nothing changes until a member accepts one.
6. **Write the changes.** Record each pull request whose group changed, with the assertion that placed it, and queue work for verdicts, tasks, and the dashboard.
7. **Open suggestions** for structural claims that lost and point to a different group than the one the pull request ended up in — "`mobile#4` has the same branch name as `web#7`, `feat/schedule`, and `web#7` is in 'Scheduled tasks'; add it?" — unless the same suggestion is open or was dismissed. Weak signals open suggestions the same way.
8. **Archive** groups left with no open pull requests, and release their anchors: a later claim under the same anchor starts a new group. Then drop the open suggestions that name an archived group, since none can be accepted into it — those opened in this resolution, for a group it emptied, and those left from before. A group merged into another is archived too; its claims' anchors point to the group it joined, so a claim that still loses suggests that group. Dismissed suggestions stay recorded.

## Flow: suggest with the model

For an open pull request left with no claim at all:

1. **Pick candidates**: the active groups that share the most words with the pull request's branch name and title, at most ten.
2. **Fingerprint** the question: the pull request's title, head branch, repository, and references. The description is not sent: it is long and costly, and pulls the model toward details that do not matter for grouping.
3. **Use a recorded answer** for that fingerprint if there is one. Otherwise ask in the background, with a timeout: which candidate does this pull request belong to — or does it start a new group, under what name — or neither?
4. **Validate** the answer. One that names a group that was not a candidate is discarded as an error.
5. **Record** the answer, with its confidence and reason.
6. **Suggest** it. At or above the threshold, an answer that names a group, or proposes a new one, opens a suggestion; below it, the answer is recorded but not shown. "Neither" opens nothing. A suggestion changes nothing until a member accepts it, which records a manual placement — creating the proposed group if it named a new one.
7. **On a timeout or an error**, nothing is suggested, and the question is retried after a backoff.

A pull request is asked again only when its own inputs change. A change in the set of groups never re-rolls an answer; a member can still group the pull request by hand.

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
| Accept a proposal | For each proposed group, as proposed or as the member edited it, that still has two or more open pull requests: a new group under its name, and placements of its open pull requests in it |
| Dismiss a proposal | The dismissal; a repost of the same groups does not open it again |

## Flow: bridge assertions

1. A bridge sends `POST /assertions` with an anchor, a list of pull requests given as repository name and number, and optionally a title and evidence. Anchors and evidence are IDs in the bridge's own system, never links.
2. Park takes the source from the registered bridge that the token belongs to, not from the request, so one bridge cannot speak for another, and rotating a token does not change the source.
3. It resolves each repository name to a watched repository that the bridge's owner can see. A name that is unknown, not watched, or not visible to the owner gets the same refusal, so a bridge cannot learn which private repositories Park watches.
4. If a live assertion exists under the same source and anchor, it is retracted, and the new one is appended: history is kept, and only one assertion per anchor is live.
5. An assertion that names more pull requests than the configured maximum is recorded but not applied: a message that lists many pull requests is usually a progress report, not a unit of review. The dashboard lists such assertions on the grouping settings page.
6. Resolution runs for every pull request in the old and new sets.

`DELETE /assertions?anchor=…` retracts the live assertion under that anchor, for example when the thread is deleted. Links to a bridge's threads or issues are rendered from its link template when a page is shown, never stored.

## Flow: bridge proposals

A thread that links many pull requests may be one large unit of work, or several announced together, and its size does not say which. A bridge that works it out — by asking a model, say, which it does on its own, so that the thread's text stays in the bridge — proposes the groups it found, and a member decides ([D61](10-design-log.md#d61-a-bridge-proposes-the-groups-it-is-unsure-of)).

1. A bridge sends `POST /proposals` with an anchor, optionally a title and evidence, and one or more groups, each with an optional name and two or more pull requests given as repository name and number. A pull request is in one proposed group at most.
2. The source, the repository names, and pull requests Park has not collected yet are handled as for assertions.
3. If the live proposal under the same source and anchor proposes the same groups, nothing changes, whatever its state: a dismissed proposal stays dismissed, and an accepted one is not accepted again. Otherwise the live proposal is retracted, and the new one opens.
4. A proposal changes no group. The groups page lists the open proposals that have a group with two or more open pull requests.
5. A member accepts a proposal, as proposed or after editing its groups' names and pull requests, or dismisses it ([manual actions](#flow-manual-actions)). Accepting records manual placements, which win over the bridge's own assertions and everything below them.

`DELETE /proposals?anchor=…` retracts the live proposal under that anchor; groups a member made by accepting it stay.

## Descriptions

A group can carry a description: a static site — an `index.html` and the files it loads — that says what the group's work is for, shown beside the group ([D66](10-design-log.md#d66-a-groups-description-is-served-by-its-link-sandboxed)).

1. A member who wrote one of the group's pull requests, or an admin, uploads it as a zip. It holds at most 500 files, unpacking to at most 100 MB, from a zip of at most 20 MB. Every path in it is relative, stays inside it, and is no link. Its `index.html` is at its top, or in the one folder at its top, which then counts as its top. Files a system adds, such as `__MACOSX` and `.DS_Store`, are left out.
2. Park stores the files and a new random token. The description is served at `/docs/{token}/…` to anyone who has its link, without signing in. Every answer is sandboxed into an origin of its own, so that a page's scripts can neither read the viewer's session nor call Park as them. It sends no referrer, so that the link stays out of the requests the page makes elsewhere, and serves ranges, so that videos seek.
3. Uploading again replaces the description under a new token, and the old link stops working. Removing it removes its files.
4. A group that absorbs another takes the other's description when it has none of its own.
5. A description follows the dashboard's theme. Park names the theme it shows in the description's link, `?theme=light` or `?theme=dark`, and tells the frame again, by a message, when the theme changes or the frame loads a page. Every HTML page is served with a short script first in its head, which copies the theme to the page's root as `data-theme` and `color-scheme`; the stored files are not changed. A page's own styles follow through `[data-theme="dark"]`, and its default colors follow by themselves. Styles written for `prefers-color-scheme` follow the operating system instead, since most browsers do not carry a frame's color scheme into the page it frames ([D68](10-design-log.md#d68-a-description-is-told-the-dashboards-theme)).

A description's link is not filtered by visibility ([A4](08-dashboard-and-api.md#rules)): whoever has it reads it. A description is not a fact about pull requests, but what its uploader chose to share.

## Group names

A group takes its name from the highest source that offers one: a manual rename, then an external assertion's title, then the title of the oldest pull request in a stack anchor, then the title of the issue behind an issue anchor — for now, the title of the oldest pull request that closes it ([D51](10-design-log.md#d51-an-issue-anchor-is-named-after-its-oldest-pull-request)) — then the branch name of a branch anchor, then the title of the oldest pull request behind a reference anchor, then — for a group created by accepting the model's suggestion — the name the model proposed. A group made by accepting a bridge's proposal is named as a member names one: by the proposed name, or, when the proposal named none, by its oldest pull request's title. The name changes when a higher source offers one, or when the source it came from changes it — a thread is retitled.

## A worked example

1. Ana opens `api#12` and `web#7`, in the `api` and `web` repositories, both from a branch named `feat/schedule`. The structural source asserts co-membership under the anchor `branch:feat/schedule`. No group holds either pull request, so group G is created, named "feat/schedule".
2. A bridge reads a chat thread titled "Scheduled tasks" that links both, and posts an assertion under the thread's anchor. External beats structural for both pull requests. The thread's anchor is new, and G holds all of its pull requests, so the anchor maps to G: no new group appears. G takes the thread's title as its name.
3. Later Ana opens `mobile#4` in a third repository, `mobile`, from a branch also named `feat/schedule`; the thread does not mention it. Its only claim is the branch anchor, which maps to G, so it joins G, marked "same branch name".
4. An unrelated `api#15` appears, and no source claims it. The model suggests a new group, "Billing", with confidence 0.8, and the dashboard shows the suggestion; nothing changes unless a member accepts it.

The dashboard now shows: **Scheduled tasks** → `api#12` (external · thread), `web#7` (external · thread), `mobile#4` (structural · same branch name).

5. The mobile team says `mobile#4` is a separate change, and a member takes it out of G. The exclusion drops its branch claim, so it stands alone, and no suggestion reopens: the exclusion was explicit.

## Used by

- **Verdicts** place one ball per group ([Verdicts](04-verdicts.md#placing-the-ball)), and read each pull request's chain position and group size (the `chain.*` and `group.*` fields in [Rules](05-rules.md)).
- **Tasks** use groups and chains as units of work and as locks ([Tasks](06-tasks.md)).
- **The dashboard** nests group → chain → pull request, and marks each pull request with the source that placed it.

## Rules

- **G1.** Chains are derived from branches and cannot be edited.
- **G2.** Each open pull request belongs to at most one group; one with no winning claim stands alone. A merged or closed pull request keeps the group it had when it closed.
- **G3.** Authority order: manual, external, structural. The highest claim present wins. The model is not a source: it only suggests.
- **G4.** Ties are broken as in step 3 of the resolution flow; the last resort keeps a pull request where it is.
- **G5.** Claims are never unioned. Union is transitive: a few weak links — A to C, C to D, D to E — would glue unrelated features into one group.
- **G6.** Only members exclude, and an exclusion holds against every source.
- **G7.** An anchor maps to at most one group, and a source and key name one anchor; two servers resolving at once cannot create it twice. The mapping is fixed when the anchor is first used, and changes only when groups merge (it is re-pointed) or its group is archived (it is released).
- **G8.** Group identity is independent of membership. Merges leave aliases; groups are archived, never deleted.
- **G9.** Structural signals never pair two pull requests of the same chain, except the forge's own stack.
- **G10.** A structural claim that loses becomes a suggestion; weak signals are only ever suggestions.
- **G11.** The model only suggests, for pull requests with no claim, one at a time, recorded by fingerprint, and asks again only when a pull request's own inputs change. A suggestion changes nothing until a member accepts it.
- **G12.** Every membership records the assertion that placed it.
- **G13.** Assertions are append-only. Reposting under the same source and anchor retracts the previous one.
- **G14.** Resolution does not depend on the order in which events arrive: each component is resolved as one batch, winning claims first and anchors after, in a canonical order.
- **G15.** Chain edges stay within one repository, never start from a fork's branch, and never hang from a long-lived branch.
- **G16.** A bridge's proposal changes nothing until a member accepts it, and accepting records manual placements. Reposting the same groups under an anchor neither reopens a proposal nor repeats a decision.
- **G17.** A group's description changes only by a member who wrote one of its pull requests, or by an admin, and is served by its link alone, sandboxed.

## Defaults

| Setting | Default |
|---|---|
| Model suggestion threshold | 0.7 |
| Model call timeout | 20 seconds |
| Candidates per model question | 10 |
| Largest external assertion applied | 3 pull requests |
| A description's zip | At most 20 MB, unpacking to at most 100 MB and 500 files |
| Branch words ignored for similarity | Those in more than a fifth of open pull requests' branch names |
| Branch names never grouped | `main`, `master`, `develop`, `dev`, `trunk`, `release/*`, `hotfix/*`, `patch-*`, `dependabot/*`, `renovate/*` |
| Long-lived branches, never a parent in a chain | The default branch, protected branches, and `main`, `master`, `develop`, `dev`, `trunk`, `release/*` |
