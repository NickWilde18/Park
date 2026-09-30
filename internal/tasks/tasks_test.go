package tasks

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/identity"
)

func failing(p facts.PR) facts.PR {
	p.Checks = []facts.Check{{Name: "build", State: facts.CheckFailing, Required: true}}
	return p
}

func waitingOn(p facts.PR, threads ...string) facts.PR {
	p.Threads = nil
	for _, id := range threads {
		p.Threads = append(p.Threads, thread(id, facts.Comment{Author: benAccount, At: t0.Add(-time.Hour)}))
	}
	return p
}

func asked(p facts.PR, accounts ...int64) facts.PR {
	at := t0.Add(-2 * time.Hour)
	for _, a := range accounts {
		p.ReviewRequests = append(p.ReviewRequests, facts.Request{Account: a, RequestedAt: &at})
	}
	return p
}

// A holder's action becomes an item of their one queued task on the unit (T3): Ana's failing check becomes fix_checks,
// with the required checks as its targets. Evaluating again adds nothing. When her part changes to answering comments,
// fix_checks is dropped and address_comments joins the same task. An action outside a member's automation becomes no
// task, and neither does the part of someone who is no member.
func TestCreate(t *testing.T) {
	e := newEnv(t)
	p := failing(pull(anaAccount))
	id := e.store(t, 12, p)
	e.evaluate(t, prUnit(id))
	task := e.taskOf(t, "ana", prUnit(id), Queued)
	if items := openItems(task); len(items) != 1 || !slices.Equal(items["fix_checks"].Targets.Checks, []string{"build"}) {
		t.Fatalf("task %+v", task)
	}
	e.evaluate(t, prUnit(id))
	if n := e.count(t, "SELECT count(*) FROM task_item"); n != 1 {
		t.Fatalf("%d items after an evaluation that changed nothing", n)
	}
	e.store(t, 12, waitingOn(pull(anaAccount), "T1"))
	e.evaluate(t, prUnit(id))
	after := e.taskOf(t, "ana", prUnit(id), Queued)
	if items := openItems(after); after.ID != task.ID || len(items) != 1 || !slices.Equal(items["address_comments"].Targets.Threads, []string{"T1"}) {
		t.Fatalf("after her part changed: %+v", after)
	}

	ben := e.store(t, 13, waitingOn(pull(benAccount), "T9"))
	e.evaluate(t, prUnit(ben))
	carol := e.store(t, 14, failing(pull(carolAccount)))
	e.evaluate(t, prUnit(carol))
	if n := e.count(t, "SELECT count(*) FROM task WHERE unit IN ($1, $2)", prUnit(ben), prUnit(carol)); n != 0 {
		t.Fatalf("%d tasks outside automation or for no member", n)
	}
	reviewed := e.store(t, 15, asked(pull(anaAccount), benAccount, carolAccount))
	e.evaluate(t, prUnit(reviewed))
	if items := openItems(e.taskOf(t, "ben", prUnit(reviewed), Queued)); len(items) != 1 || items["review"].ID == 0 {
		t.Fatalf("Ben's review: %+v", items)
	}
}

// A claim leases a member's task to their executor, taking only the items whose actions it names; the rest wait in a
// queued task of their own (T10), which no claim takes while the group is locked (T4). Another member's executor gets
// nothing of it. An item already satisfied is dropped at the claim, and a task left with nothing to do is done without
// running (T5).
func TestClaim(t *testing.T) {
	e := newEnv(t)
	a := e.store(t, 12, waitingOn(pull(anaAccount), "T1"))
	conflicting := pull(anaAccount)
	conflicting.Mergeable = facts.MergeableConflicting
	b := e.store(t, 13, conflicting)
	g, err := e.groups.Create(e.ctx, e.members["ana"], "Billing", []int64{a, b})
	must(t, err)
	unit := "group:" + strconv.FormatInt(g, 10)
	e.evaluate(t, unit)
	if items := openItems(e.taskOf(t, "ana", unit, Queued)); len(items) != 2 {
		t.Fatalf("items %+v", items)
	}
	if got := e.claim(t, "ben", "tok-ben", "address_comments", "resolve_conflicts"); got != nil {
		t.Fatalf("Ben's executor claimed Ana's task: %+v", got)
	}
	leased := e.claim(t, "ana", "tok-ana", "address_comments")
	if leased == nil || leased.State != Leased || leased.LeaseID == "" || leased.Attempt != 1 || leased.LeaseToken != "tok-ana" ||
		len(leased.open()) != 1 || leased.open()[0].Action != "address_comments" || leased.open()[0].Start == nil {
		t.Fatalf("leased %+v", leased)
	}
	rest := e.taskOf(t, "ana", unit, Queued)
	if items := openItems(rest); len(items) != 1 || items["resolve_conflicts"].ID == 0 {
		t.Fatalf("the rest %+v", rest)
	}
	if got := e.claim(t, "ana", "tok-ana-2", "resolve_conflicts"); got != nil {
		t.Fatalf("claimed on a locked group: %+v", got)
	}
	lone := pull(anaAccount)
	lone.Mergeable = facts.MergeableConflicting
	other := e.store(t, 14, lone)
	e.evaluate(t, prUnit(other))
	task := e.taskOf(t, "ana", prUnit(other), Queued)
	lone.Mergeable = facts.MergeableClean
	e.store(t, 14, lone)
	if got := e.claim(t, "ana", "tok-ana-2", "resolve_conflicts"); got != nil {
		t.Fatalf("claimed work already done: %+v", got)
	}
	if done, err := e.tasks.Task(e.ctx, task.ID); err != nil || done.State != Done {
		t.Fatalf("the task with nothing to do: %+v %v", done, err)
	}
	if still, err := e.tasks.Task(e.ctx, rest.ID); err != nil || still.State != Queued {
		t.Fatalf("the rest, while the group is locked: %+v %v", still, err)
	}
}

// Two claims on one group at once, by Ana's and Ben's executors, each for their own task: one leases its task, and the
// other finds the group locked and gets nothing (T4). Both claims are held from writing tasks until both wait, then
// let go together.
func TestConcurrentClaims(t *testing.T) {
	e := newEnv(t)
	a := e.store(t, 12, failing(pull(anaAccount)))
	b := e.store(t, 13, failing(pull(benAccount)))
	g, err := e.groups.Create(e.ctx, e.members["ana"], "Billing", []int64{a, b})
	must(t, err)
	unit := "group:" + strconv.FormatInt(g, 10)
	e.evaluate(t, unit)
	e.taskOf(t, "ana", unit, Queued)
	e.taskOf(t, "ben", unit, Queued)
	hold, err := e.pool.Begin(e.ctx)
	must(t, err)
	defer func() { _ = hold.Rollback(e.ctx) }()
	_, err = hold.Exec(e.ctx, "LOCK TABLE task IN SHARE MODE")
	must(t, err)
	got := make([]*Task, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, who := range []string{"ana", "ben"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = e.tasks.Claim(e.ctx, e.members[who], "tok-"+who, ClaimRequest{Executor: who, Actions: []string{"fix_checks"}})
		}()
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		n := e.count(t, "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'")
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d claims waiting, not 2", n)
		}
	}
	must(t, hold.Rollback(e.ctx))
	wg.Wait()
	must(t, errs[0])
	must(t, errs[1])
	if (got[0] == nil) == (got[1] == nil) {
		t.Fatalf("claims %+v and %+v", got[0], got[1])
	}
	if n := e.count(t, "SELECT count(*) FROM task WHERE state = 'leased'"); n != 1 {
		t.Fatalf("%d leased tasks on one group", n)
	}
}

// claimed is Ana's fix_checks task on a pull request with a failing check, claimed by her executor.
func claimed(t *testing.T, e *env) (int64, *Task) {
	t.Helper()
	id := e.store(t, 12, failing(pull(anaAccount)))
	e.evaluate(t, prUnit(id))
	task := e.claim(t, "ana", "tok-ana", "fix_checks")
	if task == nil {
		t.Fatal("nothing to claim")
	}
	return id, task
}

// A report or a release under a lease that has ended is refused, saying the task's state; a heartbeat under it says
// the lease is no longer current, and the executor must stop (T13).
func TestStaleLeaseRefused(t *testing.T) {
	e := newEnv(t)
	_, task := claimed(t, e)
	e.clock = e.clock.Add(16 * time.Minute)
	must(t, e.tasks.leaseEnds(e.ctx, task.ID))
	_, err := e.tasks.Report(e.ctx, e.members["ana"], task.ID, Report{Lease: task.LeaseID, Outcome: "done"})
	if codeOf(err) != "lease_not_current" || !strings.Contains(err.Error(), "verifying") {
		t.Fatalf("a report under the ended lease: %v", err)
	}
	if _, err := e.tasks.Release(e.ctx, e.members["ana"], task.ID, task.LeaseID); codeOf(err) != "lease_not_current" {
		t.Fatalf("a release under the ended lease: %v", err)
	}
	beat, err := e.tasks.Beat(e.ctx, e.members["ana"], task.ID, task.LeaseID, "", false)
	if err != nil || beat.Current || beat.State != Verifying {
		t.Fatalf("heartbeat %+v %v", beat, err)
	}
	if _, err := e.tasks.Beat(e.ctx, e.members["ben"], task.ID, task.LeaseID, "", false); codeOf(err) != "not_found" {
		t.Fatalf("another member's heartbeat: %v", err)
	}
}

// A lease that ends without a report is verified as if the executor had reported (T8): Ana's executor pushed a fix,
// and died. Once the lease expires, the new head with its check passing makes the task done.
func TestExpiredLeaseVerified(t *testing.T) {
	e := newEnv(t)
	_, task := claimed(t, e)
	fixed := pull(anaAccount)
	fixed.HeadSHA = "h2"
	e.clock = e.clock.Add(16 * time.Minute)
	must(t, e.tasks.leaseEnds(e.ctx, task.ID))
	e.store(t, 12, fixed)
	must(t, e.tasks.verify(e.ctx, task.ID))
	if got, _ := e.tasks.Task(e.ctx, task.ID); got.State != Done {
		t.Fatalf("after the lease expired: %+v", got)
	}
}

// A review task whose review lands is done, not cancelled (T7): the ball leaving Ben only marks his leased task to
// cancel after its attempt, which is then verified, and the review satisfies it.
func TestReviewDoneNotCancelled(t *testing.T) {
	e := newEnv(t)
	id := e.store(t, 12, asked(pull(anaAccount), benAccount))
	e.evaluate(t, prUnit(id))
	task := e.claim(t, "ben", "tok-ben", "review")
	if task == nil || task.open()[0].Action != "review" {
		t.Fatalf("claimed %+v", task)
	}
	e.clock = e.clock.Add(time.Minute)
	reviewed := asked(pull(anaAccount), benAccount)
	reviewed.ReviewRequests = nil
	reviewed.Reviews = []facts.Review{{ID: 1, Author: benAccount, State: facts.ReviewApproved, At: e.clock, Commit: "h1", Counts: true}}
	e.store(t, 12, reviewed)
	e.evaluate(t, prUnit(id))
	beat, err := e.tasks.Beat(e.ctx, e.members["ben"], task.ID, task.LeaseID, "reviewed", false)
	if err != nil || !beat.Current || !beat.Cancel {
		t.Fatalf("the ball left Ben: %+v %v", beat, err)
	}
	if _, err := e.tasks.Report(e.ctx, e.members["ben"], task.ID, Report{Lease: task.LeaseID, Outcome: "done"}); err != nil {
		t.Fatal(err)
	}
	e.store(t, 12, reviewed)
	must(t, e.tasks.verify(e.ctx, task.ID))
	if got, _ := e.tasks.Task(e.ctx, task.ID); got.State != Done {
		t.Fatalf("after the review: %+v", got)
	}
}

// A report does not make a task done; the facts do (T1). An attempt the facts do not satisfy fails, with its reasons
// item by item, counts against the budget, and queues the task again after a backoff, with the reasons for the next
// attempt (T9). Once the attempts are used up, it needs a person, whose retry resets them.
func TestFailedAttempts(t *testing.T) {
	e := newEnv(t)
	id := e.store(t, 12, waitingOn(pull(anaAccount), "T1"))
	e.evaluate(t, prUnit(id))
	backoff := 10 * time.Minute
	var task *Task
	for attempt := 1; attempt <= 3; attempt++ {
		if task = e.claim(t, "ana", "tok-ana", "address_comments"); task == nil || task.Attempt != attempt {
			t.Fatalf("attempt %d: %+v", attempt, task)
		}
		if attempt > 1 && !slices.Equal(task.open()[0].Failures, []string{"1 of 1 threads have no answer: T1"}) {
			t.Fatalf("attempt %d without the reasons: %+v", attempt, task.open()[0])
		}
		if _, err := e.tasks.Report(e.ctx, e.members["ana"], task.ID, Report{Lease: task.LeaseID, Outcome: "done"}); err != nil {
			t.Fatal(err)
		}
		e.store(t, 12, waitingOn(pull(anaAccount), "T1"))
		must(t, e.tasks.verify(e.ctx, task.ID))
		got, _ := e.tasks.Task(e.ctx, task.ID)
		if attempt < 3 {
			if got.State != Queued || !got.NextAttemptAt.Equal(e.clock.Add(backoff)) {
				t.Fatalf("after attempt %d: %s, next at %v", attempt, got.State, got.NextAttemptAt)
			}
			if again := e.claim(t, "ana", "tok-ana", "address_comments"); again != nil {
				t.Fatalf("claimed during the backoff: %+v", again)
			}
			e.clock = e.clock.Add(backoff)
			backoff *= 2
			continue
		}
		if got.State != NeedsPerson || e.count(t, "SELECT used FROM attempt_budget") != 3 {
			t.Fatalf("after the last attempt: %+v", got)
		}
	}
	retried, err := e.tasks.Retry(e.ctx, e.members["ana"], false, task.ID)
	if err != nil || retried.State != Queued || e.count(t, "SELECT count(*) FROM attempt_budget") != 0 {
		t.Fatalf("retried %+v %v", retried, err)
	}
	if _, err := e.tasks.Retry(e.ctx, e.members["ben"], false, task.ID); codeOf(err) != "not_found" {
		t.Fatalf("Ben retried Ana's task: %v", err)
	}
}

// Verification waits for the facts to settle, up to a deadline that pauses while they are stale, so a forge outage
// uses up no attempt. Past the deadline, what never settled fails the attempt.
func TestDeadline(t *testing.T) {
	e := newEnv(t)
	_, task := claimed(t, e)
	pushed := pull(anaAccount)
	pushed.HeadSHA = "h2"
	pushed.Checks = []facts.Check{{Name: "build", State: facts.CheckPending, Required: true}}
	if _, err := e.tasks.Report(e.ctx, e.members["ana"], task.ID, Report{Lease: task.LeaseID, Outcome: "done"}); err != nil {
		t.Fatal(err)
	}
	e.store(t, 12, pushed)
	must(t, e.tasks.verify(e.ctx, task.ID))
	got, _ := e.tasks.Task(e.ctx, task.ID)
	if got.State != Verifying || !got.Deadline.Equal(t0.Add(time.Hour)) {
		t.Fatalf("while the check runs: %s until %v", got.State, got.Deadline)
	}
	_, err := e.pool.Exec(e.ctx, "UPDATE pr_fact SET stale = true")
	must(t, err)
	e.clock = e.clock.Add(90 * time.Minute)
	must(t, e.tasks.verify(e.ctx, task.ID))
	got, _ = e.tasks.Task(e.ctx, task.ID)
	if got.State != Verifying || !got.Deadline.Equal(t0.Add(150*time.Minute)) {
		t.Fatalf("while stale: %s until %v", got.State, got.Deadline)
	}
	e.store(t, 12, pushed)
	e.clock = e.clock.Add(61 * time.Minute)
	must(t, e.tasks.verify(e.ctx, task.ID))
	got, _ = e.tasks.Task(e.ctx, task.ID)
	if got.State != Queued || !strings.Contains(got.Items[0].Failures[0], "not settled by the deadline") {
		t.Fatalf("past the deadline: %s %+v", got.State, got.Items)
	}
}

// A heartbeat extends the lease and records its note. A revoked token's lease ends at once, and its attempt is
// verified like one whose lease expired (T7, T8).
func TestHeartbeatAndRevocation(t *testing.T) {
	e := newEnv(t)
	_, task := claimed(t, e)
	e.clock = e.clock.Add(10 * time.Minute)
	beat, err := e.tasks.Beat(e.ctx, e.members["ana"], task.ID, task.LeaseID, "pushing the fix", true)
	if err != nil || !beat.Current || beat.Cancel || !beat.ExpiresAt.Equal(e.clock.Add(15*time.Minute)) {
		t.Fatalf("heartbeat %+v %v", beat, err)
	}
	if got, _ := e.tasks.Task(e.ctx, task.ID); got.Note != "pushing the fix" || !got.Waiting {
		t.Fatalf("note %q waiting %v", got.Note, got.Waiting)
	}
	must(t, pgx.BeginFunc(e.ctx, e.pool, func(tx pgx.Tx) error {
		return e.tasks.IdentityFollower()(e.ctx, tx, identity.Revoked{Tokens: []string{"tok-ana"}, Member: e.members["ana"]})
	}))
	if got, _ := e.tasks.Task(e.ctx, task.ID); got.State != Verifying {
		t.Fatalf("after the token was revoked: %s", got.State)
	}
}

// A leased task's scope is fixed: work that arrives meanwhile goes to a follow-up task (T6). When the attempt fails and
// the task is queued again, it takes the follow-up's items, and the follow-up is cancelled, leaving one queued task
// (T3).
func TestFollowUp(t *testing.T) {
	e := newEnv(t)
	id := e.store(t, 12, waitingOn(pull(anaAccount), "T1"))
	e.evaluate(t, prUnit(id))
	task := e.claim(t, "ana", "tok-ana", "address_comments")
	e.store(t, 12, waitingOn(pull(anaAccount), "T1", "T2"))
	e.evaluate(t, prUnit(id))
	follow := e.taskOf(t, "ana", prUnit(id), Queued)
	if items := openItems(follow); !slices.Equal(items["address_comments"].Targets.Threads, []string{"T2"}) {
		t.Fatalf("the follow-up %+v", follow)
	}
	if got, _ := e.tasks.Task(e.ctx, task.ID); !slices.Equal(got.open()[0].Targets.Threads, []string{"T1"}) {
		t.Fatalf("the leased task's scope changed: %+v", got.open()[0])
	}
	if _, err := e.tasks.Report(e.ctx, e.members["ana"], task.ID, Report{Lease: task.LeaseID, Outcome: "failed"}); err != nil {
		t.Fatal(err)
	}
	must(t, e.tasks.verify(e.ctx, task.ID))
	back := e.taskOf(t, "ana", prUnit(id), Queued)
	if back.ID != task.ID || !slices.Equal(openItems(back)["address_comments"].Targets.Threads, []string{"T1", "T2"}) {
		t.Fatalf("back in the queue: %+v", back)
	}
	if got, _ := e.tasks.Task(e.ctx, follow.ID); got.State != Cancelled {
		t.Fatalf("the follow-up: %s", got.State)
	}
}

// A claim retried with the same key returns the same lease rather than a second task, and a report retried under its
// lease returns the task as it is.
func TestIdempotency(t *testing.T) {
	e := newEnv(t)
	id := e.store(t, 12, failing(pull(anaAccount)))
	e.evaluate(t, prUnit(id))
	req := ClaimRequest{Executor: "laptop", Actions: []string{"fix_checks"}, Key: "claim-1"}
	first, err := e.tasks.Claim(e.ctx, e.members["ana"], "tok-ana", req)
	must(t, err)
	again, err := e.tasks.Claim(e.ctx, e.members["ana"], "tok-ana", req)
	must(t, err)
	if first == nil || again == nil || again.ID != first.ID || again.LeaseID != first.LeaseID {
		t.Fatalf("first %+v, again %+v", first, again)
	}
	report := Report{Lease: first.LeaseID, Outcome: "done"}
	if _, err := e.tasks.Report(e.ctx, e.members["ana"], first.ID, report); err != nil {
		t.Fatal(err)
	}
	if got, err := e.tasks.Report(e.ctx, e.members["ana"], first.ID, report); err != nil || got.State != Verifying {
		t.Fatalf("the report again: %+v %v", got, err)
	}
}

// A person dismisses a task with a reason. A task that needs a person leaves on its own: done when the facts satisfy
// every item, whoever did it; cancelled when its pull requests merge.
func TestPeople(t *testing.T) {
	e := newEnv(t)
	id := e.store(t, 12, failing(pull(anaAccount)))
	e.evaluate(t, prUnit(id))
	queued := e.taskOf(t, "ana", prUnit(id), Queued)
	if _, err := e.tasks.Dismiss(e.ctx, e.members["ana"], false, queued.ID, " "); codeOf(err) != "invalid_reason" {
		t.Fatalf("a dismissal without a reason: %v", err)
	}
	if got, err := e.tasks.Dismiss(e.ctx, e.members["ana"], false, queued.ID, "fixing it by hand"); err != nil || got.State != Dismissed {
		t.Fatalf("dismissed %+v %v", got, err)
	}
	worse := failing(pull(anaAccount))
	worse.Checks = append(worse.Checks, facts.Check{Name: "e2e", State: facts.CheckFailing, Required: true})
	e.store(t, 12, worse)
	e.evaluate(t, prUnit(id))
	waiting := e.taskOf(t, "ana", prUnit(id), Queued)
	_, err := e.pool.Exec(e.ctx, "UPDATE task SET state = 'needs_person' WHERE id = $1", waiting.ID)
	must(t, err)
	fixed := pull(anaAccount)
	fixed.HeadSHA = "h2"
	fixed.Checks = append(fixed.Checks, facts.Check{Name: "e2e", State: facts.CheckPassing, Required: true})
	e.store(t, 12, fixed)
	must(t, e.tasks.verify(e.ctx, waiting.ID))
	if got, _ := e.tasks.Task(e.ctx, waiting.ID); got.State != Done {
		t.Fatalf("fixed by hand: %s", got.State)
	}
}

// Task events are never changed (T2).
func TestEventsNeverChange(t *testing.T) {
	e := newEnv(t)
	id := e.store(t, 12, failing(pull(anaAccount)))
	e.evaluate(t, prUnit(id))
	for _, sql := range []string{"UPDATE task_event SET kind = 'x'", "DELETE FROM task_event"} {
		if _, err := e.pool.Exec(e.ctx, sql); err == nil || !strings.Contains(err.Error(), "never changed") {
			t.Errorf("%s: %v", sql, err)
		}
	}
}

func (e *env) followFacts(t *testing.T, pr int64) {
	t.Helper()
	must(t, pgx.BeginFunc(e.ctx, e.pool, func(tx pgx.Tx) error { return e.tasks.FactsFollower()(e.ctx, tx, pr, []string{"state"}) }))
}

// Facts never end a lease (T7): a pull request merging under a leased task only marks it to cancel after its attempt,
// which is then verified; a queued task on it loses its items, and is cancelled when none are left.
func TestMergedUnderATask(t *testing.T) {
	e := newEnv(t)
	id, task := claimed(t, e)
	queuedID := e.store(t, 13, failing(pull(anaAccount)))
	e.evaluate(t, prUnit(queuedID))
	queued := e.taskOf(t, "ana", prUnit(queuedID), Queued)
	for _, pr := range []int64{id, queuedID} {
		merged := failing(pull(anaAccount))
		merged.State = facts.StateMerged
		e.store(t, map[int64]int{id: 12, queuedID: 13}[pr], merged)
		e.followFacts(t, pr)
	}
	got, _ := e.tasks.Task(e.ctx, task.ID)
	if got.State != Leased || !got.CancelMarked {
		t.Fatalf("the leased task: %s, marked %v", got.State, got.CancelMarked)
	}
	if got, _ := e.tasks.Task(e.ctx, queued.ID); got.State != Cancelled {
		t.Fatalf("the queued task: %s", got.State)
	}
	if _, err := e.tasks.Report(e.ctx, e.members["ana"], task.ID, Report{Lease: task.LeaseID, Outcome: "failed"}); err != nil {
		t.Fatal(err)
	}
	must(t, e.tasks.verify(e.ctx, task.ID))
	if got, _ := e.tasks.Task(e.ctx, task.ID); got.State != Cancelled {
		t.Fatalf("after its attempt: %s", got.State)
	}
}

// A queued task its member's executors leave unclaimed for too long, while its lock is free, needs a person; while
// another task holds its lock, it keeps waiting.
func TestUnclaimed(t *testing.T) {
	e := newEnv(t)
	a := e.store(t, 12, failing(pull(anaAccount)))
	b := e.store(t, 13, failing(pull(benAccount)))
	g, err := e.groups.Create(e.ctx, e.members["ana"], "Billing", []int64{a, b})
	must(t, err)
	unit := "group:" + strconv.FormatInt(g, 10)
	e.evaluate(t, unit)
	ben := e.taskOf(t, "ben", unit, Queued)
	e.claim(t, "ana", "tok-ana", "fix_checks")
	e.clock = e.clock.Add(25 * time.Hour)
	must(t, e.tasks.unclaimed(e.ctx, ben.ID))
	if got, _ := e.tasks.Task(e.ctx, ben.ID); got.State != Queued {
		t.Fatalf("while the group is locked: %s", got.State)
	}
	_, err = e.pool.Exec(e.ctx, "UPDATE task SET state = 'done', locks = '{}' WHERE state = 'leased'")
	must(t, err)
	must(t, e.tasks.unclaimed(e.ctx, ben.ID))
	if got, _ := e.tasks.Task(e.ctx, ben.ID); got.State != NeedsPerson {
		t.Fatalf("unclaimed with the lock free: %s", got.State)
	}
}

// A disabled member's queued tasks are cancelled, and their leases end at once, verified, with the task marked to
// cancel after the attempt.
func TestDisabledMember(t *testing.T) {
	e := newEnv(t)
	_, task := claimed(t, e)
	other := e.store(t, 13, failing(pull(anaAccount)))
	e.evaluate(t, prUnit(other))
	queued := e.taskOf(t, "ana", prUnit(other), Queued)
	must(t, pgx.BeginFunc(e.ctx, e.pool, func(tx pgx.Tx) error {
		return e.tasks.IdentityFollower()(e.ctx, tx, identity.Revoked{Tokens: []string{"tok-ana"}, Member: e.members["ana"], Disabled: true})
	}))
	if got, _ := e.tasks.Task(e.ctx, task.ID); got.State != Verifying || !got.CancelMarked {
		t.Fatalf("the leased task: %s, marked %v", got.State, got.CancelMarked)
	}
	if got, _ := e.tasks.Task(e.ctx, queued.ID); got.State != Cancelled {
		t.Fatalf("the queued task: %s", got.State)
	}
}

// Verification judges only facts fetched after the attempt ended (D56): Ana's executor answered the thread, but the
// facts Park holds predate its report, so verification waits for them rather than failing the attempt; once they are
// fetched, the task is done.
func TestVerifyWaitsForFreshFacts(t *testing.T) {
	e := newEnv(t)
	id := e.store(t, 12, waitingOn(pull(anaAccount), "T1"))
	e.evaluate(t, prUnit(id))
	task := e.claim(t, "ana", "tok-ana", "address_comments")
	e.clock = e.clock.Add(time.Minute)
	if _, err := e.tasks.Report(e.ctx, e.members["ana"], task.ID, Report{Lease: task.LeaseID, Outcome: "done"}); err != nil {
		t.Fatal(err)
	}
	must(t, e.tasks.verify(e.ctx, task.ID))
	if got, _ := e.tasks.Task(e.ctx, task.ID); got.State != Verifying {
		t.Fatalf("judged facts older than the report: %s", got.State)
	}
	answered := waitingOn(pull(anaAccount), "T1")
	answered.Threads[0].Comments = append(answered.Threads[0].Comments, facts.Comment{Author: anaAccount, At: e.clock.Add(-time.Second)})
	e.store(t, 12, answered)
	must(t, e.tasks.verify(e.ctx, task.ID))
	if got, _ := e.tasks.Task(e.ctx, task.ID); got.State != Done {
		t.Fatalf("with the answer fetched: %s", got.State)
	}
}
