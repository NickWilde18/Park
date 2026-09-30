package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/NickWilde18/Park/internal/facts"
)

// Delivery verifies a webhook delivery's X-Hub-Signature-256 with the installation's secret, and works out what
// it concerns.
func (c *collector) Delivery(header http.Header, body, secret []byte) (facts.Delivery, error) {
	sig, ok := strings.CutPrefix(header.Get("X-Hub-Signature-256"), "sha256=")
	want, err := hex.DecodeString(sig)
	if !ok || err != nil || len(secret) == 0 {
		return facts.Delivery{}, facts.ErrBadSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), want) {
		return facts.Delivery{}, facts.ErrBadSignature
	}
	return concerns(header.Get("X-GitHub-Delivery"), header.Get("X-GitHub-Event"), body)
}

// payload is the part of a delivery that says what it concerns. Nothing else of it is read: what changed is
// always fetched again.
type payload struct {
	PullRequest *struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Issue *struct {
		PullRequest *json.RawMessage `json:"pull_request"`
		Number      int              `json:"number"`
	} `json:"issue"`
	CheckRun *struct {
		HeadSHA string `json:"head_sha"`
	} `json:"check_run"`
	CheckSuite *struct {
		HeadSHA string `json:"head_sha"`
	} `json:"check_suite"`
	Team *struct {
		ID int64 `json:"id"`
	} `json:"team"`
	Repository *struct {
		ID int64 `json:"id"`
	} `json:"repository"`
	SHA string `json:"sha"`
	Ref string `json:"ref"`
}

// concerns works out what a delivery of an event is about.
func concerns(id, event string, body []byte) (facts.Delivery, error) {
	d := facts.Delivery{ID: id, Event: event}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return d, fmt.Errorf("decode a %s delivery: %w", event, err)
	}
	if p.Repository != nil {
		d.ForgeRepo = p.Repository.ID
	}
	switch event {
	case "pull_request", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread":
		if p.PullRequest != nil {
			d.PRs = []int{p.PullRequest.Number}
		}
	case "issue_comment":
		if p.Issue != nil && p.Issue.PullRequest != nil {
			d.PRs = []int{p.Issue.Number}
		}
	case "check_run":
		if p.CheckRun != nil {
			d.SHAs = []string{p.CheckRun.HeadSHA}
		}
	case "check_suite":
		if p.CheckSuite != nil {
			d.SHAs = []string{p.CheckSuite.HeadSHA}
		}
	case "status":
		d.SHAs = []string{p.SHA}
	case "push":
		if branch, ok := strings.CutPrefix(p.Ref, "refs/heads/"); ok {
			d.Pushed = branch
		}
	case "repository", "branch_protection_rule", "branch_protection_configuration", "repository_ruleset":
		d.RepoChanged = true
	case "team", "membership", "team_add":
		if p.Team != nil {
			d.Teams = []int64{p.Team.ID}
		}
	}
	return d, nil
}
