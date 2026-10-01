"""A model of chain derivation and membership resolution, as 03-grouping.md words them, and the outcomes they need —
among them that the order assertions arrive in does not change the groups (D50).

The long-lived branch names come from the Defaults table of 03-grouping.md. The flows themselves are modelled by
hand: a change to "Flow: derive chains" or "Flow: resolve memberships" changes this model in the same pull request.
"""
import fnmatch
import itertools
import os
import re
import sys

DESIGN = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GROUPING = open(os.path.join(DESIGN, "03-grouping.md"), encoding="utf-8").read()
row = next(line for line in GROUPING.splitlines() if line.startswith("| Long-lived branches"))
LONG_LIVED = re.findall(r"`([^`]+)`", row)
if not LONG_LIVED:
    raise SystemExit("could not read the long-lived branch names from 03-grouping.md")


# ---------- chains ----------
def long_lived(branch):
    return any(fnmatch.fnmatchcase(branch, pattern) for pattern in LONG_LIVED)


def edges(prs):
    """X depends on Y when X's base branch is Y's head branch, in X's repository, and Y's head is neither a fork's
    branch nor long-lived. All the pull requests given are open."""
    return {(x["id"], y["id"]) for x in prs for y in prs
            if x is not y and x["repo"] == y["repo"] and x["base"] == y["head"]
            and y["head_repo"] == y["repo"] and not long_lived(y["head"])}


def pr(i, head, base, head_repo="acme/api"):
    return dict(id=i, repo="acme/api", head=head, base=base, head_repo=head_repo)


CHAIN_PRS = [
    pr("rel#50", "develop", "main"), pr("feat#51", "feat/a", "develop"), pr("feat#52", "feat/b", "develop"),
    pr("fork#53", "main", "main", head_repo="bob/api"), pr("fix#54", "fix/c", "main"),
    pr("s#60", "feat/x", "main"), pr("s#61", "feat/x2", "feat/x"), pr("s#62", "feat/x3", "feat/x"),
    pr("b#70", "fix/y", "main"), pr("b#71", "fix/y", "release/1"), pr("c#72", "fix/y2", "fix/y"),
]
WANT_EDGES = {("s#61", "s#60"), ("s#62", "s#60"), ("c#72", "b#70"), ("c#72", "b#71")}


# ---------- resolution ----------
STRENGTH = {"stack": 0, "issue": 1, "branch": 2, "refs": 3}  # structural tie-break: the lower wins


def resolve(assertions):
    """The whole component at once: each pull request's winning claim first, then anchors in canonical order, each
    mapped from the pull requests whose winning claim it is — so the order in which events arrived cannot matter."""
    canonical = lambda a: (STRENGTH[a["kind"]], a["seen"], a["key"])
    prs = sorted({p for a in assertions for p in a["prs"]})
    winner = {p: min((a for a in assertions if p in a["prs"]), key=canonical) for p in prs}
    member, groups = {}, []
    for a in sorted(assertions, key=canonical):
        winners = [p for p in prs if winner[p] is a]
        if not winners:
            continue
        counts = {}
        for p in winners:
            if p in member:
                counts[member[p]] = counts.get(member[p], 0) + 1
        if counts:
            top = max(counts.values())
            g = min(x for x, c in counts.items() if c == top)
        else:
            g = len(groups)
            groups.append(a["key"])
        for p in winners:
            member[p] = g
    out = {}
    for p, g in member.items():
        out.setdefault(g, set()).add(p)
    return frozenset(frozenset(s) for s in out.values())


def claim(kind, key, prs, seen):
    return dict(kind=kind, key=key, prs=set(prs), seen=seen)


RESOLUTION_CASES = [
    ("two assertions share web#2: a branch {api#1, web#2} and an issue {web#2, mobile#3}",
     [claim("branch", "branch:feat/x", ["api#1", "web#2"], 1), claim("issue", "issue:99", ["web#2", "mobile#3"], 2)],
     {frozenset({"api#1"}), frozenset({"web#2", "mobile#3"})}),
    ("claims of different kinds in a row, a–c by branch, c–d by issue, d–e by branch: c and e share nothing",
     [claim("branch", "branch:one", ["a", "c"], 1), claim("issue", "issue:7", ["c", "d"], 2),
      claim("branch", "branch:two", ["d", "e"], 3)],
     {frozenset({"a"}), frozenset({"c", "d"}), frozenset({"e"})}),
    ("a stack {api#1, api#2, api#3} made after mutual references {api#1, web#9}: the stack keeps its root",
     [claim("refs", "refs:api#1", ["api#1", "web#9"], 1), claim("stack", "stack:1/40", ["api#1", "api#2", "api#3"], 2)],
     {frozenset({"api#1", "api#2", "api#3"}), frozenset({"web#9"})}),
]

# ---------- arrival order ----------
def arrive(events):
    """Resolution after each assertion arrives, remembering where anchors point and where pull requests are. An anchor
    not mapped yet takes the group holding most of its winners, among groups that no other winning claim keeps
    (D50), or a new one."""
    live, anchors, current, groups = {}, {}, {}, []
    for kind, key, prs in events:
        seen = live[key]["seen"] if key in live else len(live) + 1
        live[key] = claim(kind, key, prs, seen)
        canonical = lambda a: (STRENGTH[a["kind"]], a["seen"], a["key"])
        members = sorted({p for a in live.values() for p in a["prs"]})
        winner = {p: min((a for a in live.values() if p in a["prs"]), key=canonical) for p in members}
        kept = {anchors[a["key"]] for a in live.values() if a["key"] in anchors and any(winner[p] is a for p in members)}
        placed = {}
        for a in sorted(live.values(), key=canonical):
            winners = [p for p in members if winner[p] is a]
            if not winners:
                continue
            g = anchors.get(a["key"])
            if g is None:
                counts = {}
                for p in winners:
                    if p in current and current[p] not in kept:
                        counts[current[p]] = counts.get(current[p], 0) + 1
                g = min(counts, key=lambda x: (-counts[x], x)) if counts else len(groups)
                if g == len(groups):
                    groups.append(a["key"])
                anchors[a["key"]] = g
                kept.add(g)
            for p in winners:
                placed[p] = g
        current = placed
    out = {}
    for p, g in current.items():
        out.setdefault(g, set()).add(p)
    return frozenset(frozenset(x) for x in out.values())


ARRIVAL_CASES = [
    ("a branch {api#1, web#2} and an issue {web#2, mobile#3}, in either order",
     [("branch", "branch:feat/x", ["api#1", "web#2"]), ("issue", "issue:99", ["web#2", "mobile#3"])],
     {frozenset({"api#1"}), frozenset({"web#2", "mobile#3"})}),
    ("a–c by branch, c–d by issue, d–e by branch, in every order",
     [("branch", "branch:one", ["a", "c"]), ("issue", "issue:7", ["c", "d"]), ("branch", "branch:two", ["d", "e"])],
     {frozenset({"a"}), frozenset({"c", "d"}), frozenset({"e"})}),
    ("mutual references {api#1, web#9} and a stack {api#1, api#2, api#3}, in either order",
     [("refs", "refs:api#1", ["api#1", "web#9"]), ("stack", "stack:1/40", ["api#1", "api#2", "api#3"])],
     {frozenset({"api#1", "api#2", "api#3"}), frozenset({"web#9"})}),
]

print(f"long-lived branches: {', '.join(LONG_LIVED)}")
failed = 0
got = edges(CHAIN_PRS)
ok = got == WANT_EDGES
failed += not ok
print(f"{'ok  ' if ok else 'FAIL'} chain edges: git-flow release, fork main, stacked tree, backport")
if not ok:
    print(f"      false edges: {sorted(got - WANT_EDGES)}; missing: {sorted(WANT_EDGES - got)}")

for title, assertions, want in RESOLUTION_CASES:
    got = resolve(assertions)
    ok = got == frozenset(want)
    failed += not ok
    print(f"{'ok  ' if ok else 'FAIL'} {title}\n      "
          + " | ".join(", ".join(sorted(g)) for g in sorted(got, key=sorted)))
for title, events, want in ARRIVAL_CASES:
    outcomes = {arrive(list(order)) for order in itertools.permutations(events)}
    ok = outcomes == {frozenset(want)}
    failed += not ok
    print(f"{'ok  ' if ok else 'FAIL'} arrival: {title}: {len(outcomes)} outcome(s)")
print("all pass" if not failed else f"{failed} failing")
sys.exit(1 if failed else 0)
