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
