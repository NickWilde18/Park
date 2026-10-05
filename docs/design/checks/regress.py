"""Units whose ball must land in a known place.

Each case builds a unit of one or more pull requests, places its ball with placement.py, and checks the side, the
holders, and — where it matters — the holders' action.
"""
import sys

from placement import AUTHOR, REVIEWER, STEPS, describe, place


def thread(starter, last, *others):
    return dict(starter=starter, participants=[starter, *others], last=last)


CASES = [
    ("A  the forge requires no approval; Ben is asked and has not reviewed",
     {"api#12": dict(author="ana", required=0, direct={"ben"})},
     "reviewer", {"ben"}, None),
    ("B  group: api#12 waits for Ben's first review; web#7 is a draft until api#12 merges",
     {"api#12": dict(author="ana", direct={"ben"}), "web#7": dict(author="ana", draft=True)},
     "reviewer", {"ben"}, None),
    ("C  group, two authors: Ben's first review of api#12; Carol's web#7 has checks running",
     {"api#12": dict(author="ana", direct={"ben"}), "web#7": dict(author="carol", checks="pending", direct={"ben"})},
     "reviewer", {"ben"}, None),
    ("D  Ben requests changes in the review body only, no inline threads",
     {"api#12": dict(author="ana", reviews={"ben": dict(decision="changes_requested", body=True)})},
     "author", {"ana"}, "address_comments"),
    ("E  Ben comments in the review body only; his request is used up; nobody else is asked",
     {"api#12": dict(author="ana", reviews={"ben": dict(decision="none", body=True)})},
     "author", {"ana"}, "address_comments"),
    ("E2 same, after Ana answered Ben's review: she hands it back",
     {"api#12": dict(author="ana", reviews={"ben": dict(decision="none", body=True, answered=True)})},
     "author", {"ana"}, "request_review"),
    ("F  Ben approves and leaves a new nit (his comment is the latest): Ana answers before merging",
     {"api#12": dict(author="ana", reviews={"ben": dict(decision="approved")}, threads=[thread("ben", "ben")])},
     "author", {"ana"}, "address_comments"),
    ("G  code owner Dave's approval is required; Carol (not an owner) approved; Dave is asked",
     {"api#12": dict(author="ana", reviews={"carol": dict(decision="approved")}, direct={"dave"},
                     code_owners={"dave"})},
     "reviewer", {"dave"}, None),
    ("H1 Ana replied last in Ben's thread and re-requested Ben's team {ben, eve}, not Ben",
     {"api#12": dict(author="ana", reviews={"ben": dict(decision="none")}, threads=[thread("ben", "ana", "ana")],
                     teams={"backend": {"ben", "eve"}})},
     "author", {"ana"}, "request_review"),
    ("H2 same, the team is {ben} only",
     {"api#12": dict(author="ana", reviews={"ben": dict(decision="none")}, threads=[thread("ben", "ana", "ana")],
                     teams={"backend": {"ben"}})},
     "author", {"ana"}, "request_review"),
    ("H3 only a team is asked, and its known members are only the author",
     {"api#12": dict(author="ana", teams={"owners": {"ana"}})},
     "author", {"ana"}, "request_review"),
    ("J1 Ben requested changes; Ana, who cannot dismiss, answered everything and has not asked again",
     {"api#12": dict(author="ana", can_dismiss=False, reviews={"ben": dict(decision="changes_requested")},
                     threads=[thread("ben", "ana", "ana")])},
     "author", {"ana"}, "request_review"),
    ("J2 same, after Ana asked Ben again",
     {"api#12": dict(author="ana", can_dismiss=False, reviews={"ben": dict(decision="changes_requested")},
                     threads=[thread("ben", "ana", "ana")], direct={"ben"})},
     "reviewer", {"ben"}, None),
    ("K  required checks have been running for five hours",
     {"api#12": dict(author="ana", checks="pending", hours_checks_pending=5, direct={"ben"})},
     "author", {"ana"}, "fix_checks"),
    ("L  approved, but the forge still blocks merging for another reason",
     {"api#12": dict(author="ana", reviews={"ben": dict(decision="approved")}, forge_blocks=["other"])},
     "author", {"ana"}, "unblock"),
    ("M  two approvals required: Ben approved, Carol is asked",
     {"api#12": dict(author="ana", required=2, reviews={"ben": dict(decision="approved")}, direct={"carol"})},
     "reviewer", {"carol"}, None),
    ("N  Ben reviewed again and resolved his threads, but his request for changes stands",
     {"api#12": dict(author="ana", reviews={"ben": dict(decision="changes_requested", again=True)})},
     "author", {"ana"}, "request_review"),
    ("O  group: api#12 merged with an open thread; web#7 waits for Ben's first review",
     {"api#12": dict(author="ana", closed=True, threads=[thread("ben", "ben")]),
      "web#7": dict(author="ana", direct={"ben"})},
     "reviewer", {"ben"}, None),
    ("P  single draft: the author holds it",
     {"api#12": dict(author="ana", draft=True, direct={"ben"})},
     "author", {"ana"}, None),
    ("Q  single pull request, checks running, Ben asked again: he waits for green checks",
     {"api#12": dict(author="ana", checks="pending", reviews={"ben": dict(decision="none")}, direct={"ben"})},
     "author", {"ana"}, None),
    ("R1 Ana dismissed Ben's request for changes, his threads resolved, and asked him again; Carol and Dave approved, "
     "and two are required: Ana merges",
     {"api#12": dict(author="ana", required=2, direct={"ben"},
                     reviews={"ben": dict(decision="none"), "carol": dict(decision="approved"),
                              "dave": dict(decision="approved")})},
     "author", {"ana"}, "merge"),
    ("R2 same, but Ben's thread is still open, Ana replied last: Ben looks again",
     {"api#12": dict(author="ana", required=2, direct={"ben"}, threads=[thread("ben", "ana", "ana")],
                     reviews={"ben": dict(decision="none"), "carol": dict(decision="approved"),
                              "dave": dict(decision="approved")})},
     "reviewer", {"ben"}, "review"),
    ("R3 Ben approved, and Ana asked him again; Carol approved too, and two are required: Ben looks again",
     {"api#12": dict(author="ana", required=2, direct={"ben"},
                     reviews={"ben": dict(decision="approved"), "carol": dict(decision="approved")})},
     "reviewer", {"ben"}, "review"),
    ("R4 as R1, but code owner Eve is asked and has not approved: Ben, asked again, holds it, and so does Eve",
     {"api#12": dict(author="ana", required=2, direct={"ben", "eve"}, code_owners={"eve"},
                     reviews={"ben": dict(decision="none"), "carol": dict(decision="approved"),
                              "dave": dict(decision="approved")})},
     "reviewer", {"ben", "eve"}, "review"),
    ("R5 as R1, but a thread Carol started waits for Ben's reply, Ana replied last: Ben looks again",
     {"api#12": dict(author="ana", required=2, direct={"ben"}, threads=[thread("carol", "ana", "ben")],
                     reviews={"ben": dict(decision="none"), "carol": dict(decision="approved"),
                              "dave": dict(decision="approved")})},
     "reviewer", {"ben"}, "review"),
]

def main():
    print(f"rules: {len(AUTHOR)} author, {len(REVIEWER)} reviewer; placement: "
          + " → ".join(f"{side} {'/'.join(sorted(kinds))}" for side, kinds in STEPS))
    failed = 0
    for title, unit, side, holders, action in CASES:
        got_side, got, parts = place(unit)
        got_people = {p["person"] for p in got}
        ok = got_side == side and got_people == holders
        if ok and action:
            ok = all(action in p["action"] for p in got)
        failed += not ok
        print(f"{'ok  ' if ok else 'FAIL'} {title}\n      want {side}: {', '.join(sorted(holders))}"
              + (f" ({action})" if action else "")
              + f" | got {got_side}: {', '.join(sorted(got_people)) or 'nobody'}"
              + (f" ({', '.join(p['action'] or p['kind'] for p in got)})" if got else "")
              + (f"\n      parts: {describe(parts)}" if not ok else ""))
    print(f"{len(CASES) - failed} of {len(CASES)} cases pass")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
