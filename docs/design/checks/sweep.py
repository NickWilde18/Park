"""Every state of one pull request with an author and two reviewers, placed with placement.py.

Each reviewer is not involved, requested through a team, requested directly, or has reviewed — with every decision,
thread state, pending request, repeated review, and written body. These are combined with every forge requirement,
draft, check state, dismissal permission, new commits, elapsed time, and nudge. In every state:

- the ball is placed without the fallback;
- a reviewer who approved holds it only when asked again;
- a reviewer who requested changes holds it only when the author cannot dismiss the request and asked again;
- a reviewer holds it only with an act part;
- no model answer changes anyone's kind or action;
- "all answered" never holds while a written review is unanswered.

It also writes, for every thousand states, a digest of the rule each person reaches, to
internal/verdicts/testdata/sweep.json: the Go port of the sweep must reach the same rules in every state. With --write,
the file is written; otherwise the run fails if it differs from what the tables give.
"""
import hashlib
import itertools
import json
import os
import sys
import time

from placement import AUTHOR, REVIEWER, evaluate, people, place

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))
DIGESTS = os.path.join(ROOT, "internal", "verdicts", "testdata", "sweep.json")
CHUNK = 1000


def digest(records):
    """A chunk's digest: each state's people, by name, with the rule each reaches."""
    return hashlib.sha256(";".join(records).encode()).hexdigest()[:16]


def reviewer_states():
    for role in ("none", "team", "direct"):
        yield dict(role=role, reviewed=False, decision="none", thread="none", requested=role == "direct",
                   again=False, body=False)
    for decision, thread, requested in itertools.product(("none", "changes_requested", "approved"),
                                                          ("none", "reviewer_last", "author_last"), (False, True)):
        for again in ((False, True) if decision == "changes_requested" else (False,)):
            for body in ((False, True) if decision != "approved" else (False,)):
                yield dict(role="reviewed", reviewed=True, decision=decision, thread=thread, requested=requested,
                           again=again, body=body)


def build(ben, carol, required, draft, checks, can_dismiss, new_commits, hours, nudge):
    reviews, direct, teams, threads = {}, set(), {}, []
    for name, s in (("ben", ben), ("carol", carol)):
        if s["role"] == "team":
            teams[f"team-{name}"] = {name}
        if s["requested"]:
            direct.add(name)
        if s["reviewed"]:
            reviews[name] = dict(decision=s["decision"], again=s["again"], body=s["body"], new_commits=new_commits,
                                 hours=hours, nudged=nudge)
        if s["thread"] != "none":
            last = name if s["thread"] == "reviewer_last" else "ana"
            threads.append(dict(starter=name, participants=[name, "ana"], last=last))
    return {"pr": dict(author="ana", required=required, draft=draft, checks=checks, can_dismiss=can_dismiss,
                       hours_checks_pending=hours if checks == "pending" else None, reviews=reviews, direct=direct,
                       teams=teams, threads=threads, hours_requested=hours, author_nudged=nudge)}


def with_judgements(pr, answer):
    pr = dict(pr)
    pr["reviews"] = {p: dict(r, j_commits=answer, j_blocking=answer) for p, r in pr["reviews"].items()}
    return pr


def states():
    """Every state of the sweep: its parameters — ben, carol, required, draft, checks, can_dismiss, new_commits,
    hours, nudge — and its unit."""
    r = list(reviewer_states())
    for params in itertools.product(r, r, (0, 1, 2), (False, True), ("passing", "pending", "failing"), (True, False),
                                    (False, True), (1, 30), (None, 5)):
        yield params, build(*params)


def main():
    start = time.time()
    count = bad = 0
    seen = {}
    records, digests = [], []
    for (ben, carol, required, draft, checks, can_dismiss, new_commits, hours, nudge), unit in states():
        count += 1
        side, holders, parts = place(unit)
        records.append(",".join(f"{p['person']}:{p['n']}" for p in sorted(parts, key=lambda p: p["person"])))
        if len(records) == CHUNK:
            digests.append(digest(records))
            records = []
        problems = []
        if side == "fallback":
            problems.append("the ball needs the fallback")
        for h in holders:
            if h["role"] != "reviewer":
                continue
            s = ben if h["person"] == "ben" else carol
            if s["decision"] == "approved" and not s["requested"]:
                problems.append("a reviewer who approved holds the ball without being asked again")
            if s["decision"] == "changes_requested" and (can_dismiss or not s["requested"]):
                problems.append("a reviewer who requested changes holds the ball")
            if h["kind"] != "act":
                problems.append("a reviewer holds the ball with a part that is not act")
        base = {p["person"]: (p["kind"], p["action"]) for p in parts}
        for answer in ("yes", "no"):
            for person, (role, fields) in people(with_judgements(unit["pr"], answer)).items():
                rule = evaluate(AUTHOR if role == "author" else REVIEWER, fields)
                if (rule["kind"], rule["action"]) != base[person]:
                    problems.append(f"a model answer '{answer}' changes the {role}'s part")
        author = next(p for p in parts if p["role"] == "author")
        if (author["name"] in ("Changes requested, all answered", "All answered, cannot dismiss")
                and people(unit["pr"])["ana"][1]["pr.unanswered_reviews"]):
            problems.append("'all answered' while a written review is unanswered")
        for p in problems:
            seen.setdefault(p, (ben, carol, required, draft, checks, can_dismiss, new_commits, hours, nudge, parts))
            bad += 1
    if records:
        digests.append(digest(records))
    about = ("Written by docs/design/checks/sweep.py; do not edit. For every chunk of states, in the sweep's order, a "
             "digest of each person's name and the rule they reach, people by name, joined as name:rule.")
    lines = ",\n".join(f'    "{d}"' for d in digests)
    text = (f'{{\n  "about": {json.dumps(about)},\n  "states": {count},\n  "chunk": {CHUNK},\n'
            f'  "digests": [\n{lines}\n  ]\n}}\n')
    if "--write" in sys.argv:
        with open(DIGESTS, "w", encoding="utf-8") as f:
            f.write(text)
    elif not os.path.exists(DIGESTS) or open(DIGESTS, encoding="utf-8").read() != text:
        seen.setdefault(f"{os.path.relpath(DIGESTS, ROOT)} is not what the tables give; run with --write", None)
        bad += 1
    print(f"states: {count}; failing: {bad}; kinds of failure: {len(seen)}; {time.time() - start:.0f}s")
    for p, example in seen.items():
        if example is None:
            print(f"  {p}")
            continue
        ben, carol, *rest, parts = example
        print(f"  {p}: ben={ben} carol={carol} rest={rest}\n    "
              + "; ".join(f"{x['person']} #{x['n']} {x['name']} → {x['kind']}" for x in parts))
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
