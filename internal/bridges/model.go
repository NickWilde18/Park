package bridges

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Group is a unit of work a model found in a thread: a name, and its pull requests as owner/name#number.
type Group struct {
	Name string   `json:"name"`
	PRs  []string `json:"pull_requests"`
}

// model asks one model, at one address, with one key, how a thread's pull requests divide into units of work. It
// speaks the chat completions protocol, which model providers and gateways commonly serve. A thread's text goes to the
// model and nowhere else: none of it is logged or kept.
type model struct {
	http *http.Client
	url  string
	name string
	key  string
}

// newModel returns a model client of the model name, whose chat completions are under url, such as
// https://models.example.test/v1, with key; it gives up on an answer after timeout.
func newModel(url, name, key string, timeout time.Duration) *model {
	return &model{http: &http.Client{Timeout: timeout}, url: strings.TrimRight(url, "/") + "/chat/completions", name: name, key: key}
}

// instructions tell the model what it is asked and how to answer.
const instructions = `You read one message from a team's code review chat. It asks for a review of pull requests, ` +
	`written as owner/name#number. It may be about one unit of work — a feature or a fix whose pull requests are ` +
	`reviewed together — or about several, often as a numbered list, or it may report unrelated changes. ` +
	`Divide the pull requests into units of work. Answer with JSON only, in the form ` +
	`{"groups": [{"name": "...", "pull_requests": ["owner/name#number", ...]}]}: one group for each unit of two or ` +
	`more pull requests, named in a few words in the message's own language. A pull request is in one group at most; ` +
	`leave out a pull request that is a unit on its own. When no unit has two or more pull requests, answer ` +
	`{"groups": []}.`

// Split asks how a thread's pull requests, as owner/name#number, divide into units of work. It returns the groups of
// two or more of them, each pull request in one group at most and only those given, in the model's order; a pull
// request the model left out stands alone. No groups means the model found no unit of two or more.
func (c *model) Split(ctx context.Context, text string, prs []string) ([]Group, error) {
	body, err := json.Marshal(map[string]any{"model": c.name, "messages": []map[string]string{
		{"role": "system", "content": instructions},
		{"role": "user", "content": "Message:\n" + text + "\n\nPull requests:\n" + strings.Join(prs, "\n")},
	}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if utf8.RuneCountInString(msg) > 200 {
			msg = string([]rune(msg)[:200]) + "…"
		}
		return nil, fmt.Errorf("the model answered %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return nil, errors.New("the model's answer has no message")
	}
	return groups(out.Choices[0].Message.Content, prs)
}

// groups reads the JSON in a model's message — which may come wrapped in a code block, or after other words — and
// keeps what a round can use of it: pull requests it was given, each in its first group only, and groups of two or
// more.
func groups(content string, prs []string) ([]Group, error) {
	start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if start < 0 || end < start {
		return nil, errors.New("the model's answer is not JSON")
	}
	var answer struct {
		Groups []Group `json:"groups"`
	}
	if err := json.Unmarshal([]byte(content[start:end+1]), &answer); err != nil {
		return nil, fmt.Errorf("the model's answer is not JSON: %w", err)
	}
	given := map[string]bool{}
	for _, pr := range prs {
		given[pr] = true
	}
	placed := map[string]bool{}
	out := []Group{}
	for _, g := range answer.Groups {
		kept := Group{Name: strings.TrimSpace(g.Name)}
		if utf8.RuneCountInString(kept.Name) > 200 {
			kept.Name = string([]rune(kept.Name)[:200])
		}
		for _, pr := range g.PRs {
			pr = strings.TrimSpace(pr)
			if given[pr] && !placed[pr] {
				placed[pr] = true
				kept.PRs = append(kept.PRs, pr)
			}
		}
		if len(kept.PRs) >= 2 {
			out = append(out, kept)
		}
	}
	return out, nil
}
