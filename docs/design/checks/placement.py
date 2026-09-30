"""Places the ball of a unit the way the design chapters say.

The default rule set is read from the tables in 05-rules.md, and the placement order from the numbered list in
04-verdicts.md, so both are checked as written. The fields a rule reads are computed by people(), a model of the
field catalog in 05-rules.md: a change to a field's meaning in the catalog changes people() in the same pull request.

A unit is a dict from a pull request's name to its description. A description holds:

    author          the author's name
    reviews         {reviewer: {decision, again, body, answered, new_commits, hours, nudged, j_commits, j_blocking}}
                    decision is approved, changes_requested, or none (the review only commented); again: another
                    review since the decisive one; body: the review has a written body; answered: the author
                    commented or pushed after it; j_commits and j_blocking: recorded judgements, None when unknown
    direct          reviewers with a pending direct review request
    teams           {team: members} with a pending review request
    threads         unresolved threads: {starter, participants, last}
    required        approvals the forge requires (default 1)
    code_owners     reviewers whose approval the forge requires as code owners
    checks          passing, pending, failing, or none
    forge_blocks    anything else the forge says blocks merging
    can_dismiss     the author may dismiss reviews (default true)
    draft, conflicting, closed, position, length, open_below, group_size, hours_checks_pending,
    hours_requested, author_nudged
"""
import os
import re

DESIGN = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RULES = open(os.path.join(DESIGN, "05-rules.md"), encoding="utf-8").read()
VERDICTS = open(os.path.join(DESIGN, "04-verdicts.md"), encoding="utf-8").read()
UNKNOWN = object()
OPS = {"=": lambda a, b: a == b, "≠": lambda a, b: a != b, "<": lambda a, b: a < b,
       "≤": lambda a, b: a <= b, ">": lambda a, b: a > b, "≥": lambda a, b: a >= b}


def compile_term(t):
    """One term of a When cell, as a function of a person's fields: True, False, or UNKNOWN."""
    t = t.strip()
    if t == "always":
        return lambda f: True
    if m := re.fullmatch(r"not `([\w.]+)`", t):
        k = m[1]
        return lambda f: not f[k]
    if m := re.fullmatch(r"`([\w.]+)`", t):
        k = m[1]
        return lambda f: bool(f[k])
    if m := re.fullmatch(r"`([\w.]+)` is not empty", t):
        k = m[1]
        return lambda f: len(f[k]) > 0
    if m := re.fullmatch(r"`([\w.]+)` is empty", t):
        k = m[1]
        return lambda f: len(f[k]) == 0
    if m := re.fullmatch(r"`([\w.]+)` contains `(\w+)`", t):
        k, v = m[1], m[2]
        return lambda f: v in f[k]
    if m := re.fullmatch(r"`([\w.]+)` is not `(\w+)`", t):
        k, v = m[1], m[2]
        return lambda f: f[k] != v
    if m := re.fullmatch(r"`([\w.]+)` is `(\w+)`", t):
        k, v = m[1], m[2]
        judged = k.startswith("judgement.")
        return lambda f: UNKNOWN if judged and f.get(k) is None else f[k] == v
    if m := re.fullmatch(r"`([\w.]+)` (=|≠|<|≤|>|≥) (\d+)", t):
        k, op, n = m[1], OPS[m[2]], int(m[3])
        return lambda f: False if f[k] is None else op(f[k], n)
    if m := re.fullmatch(r"`([\w.]+)` (=|≠|<|≤|>|≥) `([\w.]+)`", t):
        k, op, k2 = m[1], OPS[m[2]], m[3]
        return lambda f: op(f[k], f[k2])
    raise SystemExit(f"cannot parse condition term: {t!r}")


def rows(section, until):
    """The rule table between two headings of 05-rules.md, as dicts of its cells."""
    body = RULES.split(section, 1)[1].split(until, 1)[0]
    header = next(line for line in body.splitlines() if line.startswith("| # |"))
    cols = [c.strip() for c in header.strip().strip("|").split("|")]
    return [dict(zip(cols, [x.strip() for x in line.strip().strip("|").split("|")]))
            for line in body.splitlines() if re.match(r"^\| \d+ \|", line)]


def compile_rules(table):
    out = []
    for c in table:
        terms = [compile_term(t) for t in re.split(r",\s*and\s+|,\s*|\s+and\s+", c["When"])]
        out.append(dict(n=int(c["#"]), name=c["Rule"], kind=c["Kind"].split(",")[0].strip(),
                        action=c["Action"], terms=terms))
    return out


AUTHOR = compile_rules(rows("### Author\n", "### Reviewer\n"))
REVIEWER = compile_rules(rows("### Reviewer\n", "### How to read them\n"))


def placement_steps():
    """The placement order from 04-verdicts.md: [(side, kinds)], tried in order."""
    body = VERDICTS.split("## Placing the ball", 1)[1].split("\n## ", 1)[0]
    steps = []
    for line in body.splitlines():
        if m := re.match(r"^\d\. (?:Otherwise, )?\*\*[Tt]he (author|reviewer) side\*\*, if any (author|reviewer) has an? (.+?) part", line):
            steps.append((m[1], set(re.findall(r"`(\w+)`", m[3]))))
    if not steps:
        raise SystemExit("could not read the placement order from 04-verdicts.md")
    return steps


STEPS = placement_steps()


def evaluate(rules, fields):
    """The first rule whose terms are all true. A term on an unknown judgement is not true."""
    for r in rules:
        if all(t(fields) is True for t in r["terms"]):
            return r
    raise SystemExit("no rule matched: a list has no catch-all")


def people(pr):
    """{person: (role, fields)} for the author and every reviewer of one open pull request."""
    author = pr["author"]
    reviews = pr.get("reviews", {})
    direct = set(pr.get("direct", ()))
    threads = pr.get("threads", [])
    # A team request counts only when the team has someone other than the author.
    teams = {t: ms - {author} for t, ms in pr.get("teams", {}).items()}
    teams = {t: ms for t, ms in teams.items() if ms}
    team_members = {m for ms in teams.values() for m in ms}
    reviewers = (direct | team_members | set(reviews)) - {author}

    def decision(p):
        return reviews.get(p, {}).get("decision", "none")

    approved = {p for p in reviewers if decision(p) == "approved"}
    blocked = sorted(p for p in reviewers if decision(p) == "changes_requested")
    forge_required = pr.get("required", 1)
    owners = set(pr.get("code_owners", ()))
    owners_pending = sorted(owners - approved)
    checks = pr.get("checks", "passing")
    # What the forge itself says blocks merging; the team's minimum approvals is not the forge's.
    blockers = []
    if len(approved) < forge_required:
        blockers.append("reviews")
    if owners_pending:
        blockers.append("code_owners")
    if checks in ("pending", "failing"):
        blockers.append("checks")
    if pr.get("conflicting"):
        blockers.append("conflicts")
    blockers += pr.get("forge_blocks", [])
    unanswered = sum(1 for r in reviews.values()
                     if r.get("body") and not r.get("answered") and r.get("decision", "none") != "approved")

    def waiting_in(t):
        """A thread's participants it waits on: everyone but the author and those who approved."""
        return set(t["participants"]) - {author} - approved

    on_reviewers = [t for t in threads if t["last"] == author and waiting_in(t)]
    awaited = sorted({p for t in on_reviewers for p in waiting_in(t)})
    base = {
        "pr.author": author,
        "pr.draft": pr.get("draft", False),
        "pr.conflicting": pr.get("conflicting", False),
        "pr.checks": checks,
        "pr.failing_checks": ["ci"] if checks == "failing" else [],
        "pr.hours_checks_pending": pr.get("hours_checks_pending", 0.5) if checks == "pending" else None,
        "pr.approvals": len(approved),
        "pr.required_approvals": max(forge_required, 1),
        "pr.blocked_by": blocked,
        "pr.blockers_rerequested": all(p in direct for p in blocked),
        "pr.requested": sorted(direct) + sorted(teams),
        "pr.hours_requested": pr.get("hours_requested", 2) if (direct or teams) else None,
        "pr.reviewed": bool(reviews),
        "pr.unanswered_reviews": unanswered,
        "pr.threads_on_reviewers": len(on_reviewers),
        "pr.awaited_reviewers": awaited,
        "pr.awaited_rerequested": all(p in direct for p in awaited),
        "pr.mergeable_now": not blockers and not pr.get("draft", False),
        "pr.merge_blocked_by": blockers,
        "pr.code_owners_pending": owners_pending,
        "pr.author_can_dismiss": pr.get("can_dismiss", True),
        "chain.position": pr.get("position", 1),
        "chain.length": pr.get("length", 1),
        "chain.open_below": pr.get("open_below", 0),
        "group.size": pr.get("group_size", 0),
    }
    out = {author: ("author", dict(base, **{
        "me.threads_on_me": sum(1 for t in threads if t["last"] != author),
        "me.hours_since_nudge": pr.get("author_nudged"),
    }))}
    for p in reviewers:
        r = reviews.get(p, {})
        out[p] = ("reviewer", dict(base, **{
            "me.reviewed": p in reviews,
            "me.requested": p in direct,
            "me.decision": decision(p),
            "me.code_owner": p in owners,
            "me.reviewed_since_decision": r.get("again", False),
            "me.new_commits": r.get("new_commits", False),
            "me.threads_open": sum(1 for t in threads if t["starter"] == p),
            "me.threads_on_me": sum(1 for t in threads if p in t["participants"] and t["last"] == author),
            "me.hours_since_review": r.get("hours", 2) if p in reviews else None,
            "me.hours_since_nudge": r.get("nudged"),
            "judgement.commits_address_my_comments": r.get("j_commits"),
            "judgement.comments_blocking": r.get("j_blocking"),
        }))
    return out


def place(unit):
    """Returns (side, holders, parts). Only open pull requests take part; side is "fallback" when no step places it."""
    parts = []
    for name, pr in unit.items():
        if pr.get("closed"):
            continue
        for person, (role, fields) in people(pr).items():
            rule = evaluate(AUTHOR if role == "author" else REVIEWER, fields)
            parts.append(dict(pr=name, person=person, role=role, **rule))
    for side, kinds in STEPS:
        holders = [p for p in parts if p["role"] == side and p["kind"] in kinds]
        if holders:
            return side, holders, parts
    return "fallback", [], parts


def describe(parts):
    return "; ".join(f"{p['pr']} {p['person']} #{p['n']} {p['name']} → {p['kind']}" for p in parts)
