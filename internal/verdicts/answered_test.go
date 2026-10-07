package verdicts

import (
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/rules"
)

// A written review is answered when its writer approves after it (#67). Ana's pull request has passing checks and
// needs one approval. Since her last push, Ben wrote a review that only comments, with a body — a full review that
// found nothing, say — and approved an hour later; Ana has not commented since. Ben's approval answers his own review,
// so nothing waits for Ana's answer, and she is not asked to address comments. A review written after its writer's
// approval, and a review that another reviewer approved after, still wait for her.
func TestApprovalAnswersTheReviewersOwnReview(t *testing.T) {
	for _, c := range []struct {
		name       string
		comment    string        // who wrote the commenting review, with a body
		commentAgo time.Duration // how long ago
		want       int64         // reviews waiting for Ana's answer
	}{
		{"Ben comments, then approves", "ben", 3 * time.Hour, 0},
		{"Ben approves, then comments", "ben", time.Hour, 1},
		{"Carol comments, Ben approves after", "carol", 3 * time.Hour, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := model{author: "ana", reviews: map[string]modelReview{"ben": {decision: "approved"}}}
			pr, ctx := m.facts()
			pr.Reviews = append(pr.Reviews, facts.Review{ID: int64(len(pr.Reviews) + 1), Author: accounts[c.comment],
				State: facts.ReviewCommented, At: now.Add(-c.commentAgo), Commit: "head", HasBody: true, Counts: true})
			for _, p := range People(pr, ctx) {
				if p.Role != rules.Author {
					continue
				}
				if got := p.Input.Fields["pr.unanswered_reviews"]; got != c.want {
					t.Fatalf("%v reviews wait for Ana's answer, want %d", got, c.want)
				}
				part := compiled.Evaluate(p.Role, p.Input)
				if asked := part.Action == "address_comments"; asked != (c.want > 0) {
					t.Fatalf("Ana's part: %s %s, %q", part.Kind, part.Action, part.Reason)
				}
				return
			}
			t.Fatal("Ana has no part")
		})
	}
}

// Asking a written review's writer to review again answers it: it hands the review back (#92). Ana's pull request has
// passing checks. Since her last push, Ben wrote a review that only comments, with a body, three hours ago, and Ana
// has not commented since. Asking Ben again an hour ago answers his review: nothing waits for Ana's answer, she is not
// asked to address comments, and Ben, asked again, has his review to do. Asking Carol instead answers nothing of Ben's,
// and a request made before Ben's review — one the forge drops when he reviews — does not answer it either.
func TestAskingAgainAnswersTheReview(t *testing.T) {
	for _, c := range []struct {
		name     string
		asked    string        // whose review Ana asked for
		askedAgo time.Duration // how long ago
		want     int64         // reviews waiting for Ana's answer
	}{
		{"Ana asks Ben again after his review", "ben", time.Hour, 0},
		{"Ana asks Carol after Ben's review", "carol", time.Hour, 1},
		{"Ana asked Ben before his review", "ben", 4 * time.Hour, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			pr, ctx := model{author: "ana"}.facts()
			pr.Reviews = append(pr.Reviews, facts.Review{ID: 1, Author: accounts["ben"], State: facts.ReviewCommented,
				At: now.Add(-3 * time.Hour), Commit: "head", HasBody: true, Counts: true})
			asked := now.Add(-c.askedAgo)
			pr.ReviewRequests = append(pr.ReviewRequests, facts.Request{Account: accounts[c.asked], RequestedAt: &asked})
			parts := map[string]rules.Part{}
			for _, p := range People(pr, ctx) {
				if got := p.Input.Fields["pr.unanswered_reviews"]; p.Role == rules.Author && got != c.want {
					t.Errorf("%v reviews wait for Ana's answer, want %d", got, c.want)
				}
				parts[names[p.Account]] = compiled.Evaluate(p.Role, p.Input)
			}
			if a := parts["ana"]; (a.Action == "address_comments") != (c.want > 0) {
				t.Errorf("Ana's part: %s %s, %q", a.Kind, a.Action, a.Reason)
			}
			if b := parts["ben"]; c.want == 0 && (b.Kind != rules.KindAct || b.Action != "review") {
				t.Errorf("Ben's part: %s %s, %q", b.Kind, b.Action, b.Reason)
			}
		})
	}
}
