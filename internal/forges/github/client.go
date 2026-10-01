package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
)

// client calls GitHub's REST and GraphQL APIs with one credential.
type client struct {
	http    *http.Client
	auth    func(ctx context.Context) (string, error)
	rest    string
	graphql string
}

// graphQLBase is the GraphQL endpoint for a forge's base address.
func graphQLBase(base string) string {
	u, err := url.Parse(base)
	if err == nil && (u.Host == "github.com" || u.Host == "www.github.com") {
		return "https://api.github.com/graphql"
	}
	return strings.TrimSuffix(base, "/") + "/api/graphql"
}

type graphQLError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// query sends a GraphQL query and decodes its data into out.
func (c *client) query(ctx context.Context, q string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphql, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	ans, err := c.send(req)
	if err != nil {
		return err
	}
	if ans.status != http.StatusOK {
		return statusError(ans.status, "graphql")
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors"`
	}
	if err := json.Unmarshal(ans.body, &envelope); err != nil {
		return fmt.Errorf("decode the GraphQL answer: %w", err)
	}
	for _, e := range envelope.Errors {
		switch e.Type {
		case "RATE_LIMITED":
			return &facts.RateLimited{Until: resetTime(ans.header)}
		case "NOT_FOUND":
			return facts.ErrNotFound
		}
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("GraphQL: %s", strings.Join(msgs, "; "))
	}
	return json.Unmarshal(envelope.Data, out)
}

// errForbidden is a REST refusal that is not a rate limit: the credential may not read this.
var errForbidden = errors.New("GitHub does not let this credential read it")

// get sends a REST GET and decodes the answer into out. A 404 is facts.ErrNotFound, and a 403 errForbidden.
func (c *client) get(ctx context.Context, path string, out any) error {
	_, err := c.getPage(ctx, path, out)
	return err
}

// getPage is get, and also returns the path of the next page from the Link header, or "" on the last page.
func (c *client) getPage(ctx context.Context, path string, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.rest+path, nil)
	if err != nil {
		return "", err
	}
	ans, err := c.send(req)
	if err != nil {
		return "", err
	}
	switch ans.status {
	case http.StatusOK:
		return nextPage(ans.header.Get("Link"), c.rest), json.Unmarshal(ans.body, out)
	case http.StatusNotFound:
		return "", facts.ErrNotFound
	case http.StatusForbidden:
		return "", errForbidden
	}
	return "", statusError(ans.status, path)
}

// nextPage finds rel="next" in a Link header, as a path under the REST base.
func nextPage(link, base string) string {
	for _, part := range strings.Split(link, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		target = strings.Trim(strings.TrimSpace(target), "<>")
		if rest, ok := strings.CutPrefix(target, base); ok {
			return rest
		}
	}
	return ""
}

// answer is a response, read and closed.
type answer struct {
	header http.Header
	body   []byte
	status int
}

// send authorizes and sends a request, and reads the answer. A rate limit becomes facts.RateLimited.
func (c *client) send(req *http.Request) (answer, error) {
	auth, err := c.auth(req.Context())
	if err != nil {
		return answer{}, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.http.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return answer{}, err
	}
	if limited(resp) {
		return answer{}, &facts.RateLimited{Until: resetTime(resp.Header)}
	}
	return answer{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

// limited reports a primary or secondary rate limit.
func limited(resp *http.Response) bool {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	return resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0"
}

// resetTime is when a rate limit resets: Retry-After, or X-RateLimit-Reset, or a minute from now.
func resetTime(h http.Header) time.Time {
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
		return time.Now().Add(time.Duration(s) * time.Second)
	}
	if s, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		return time.Unix(s, 0)
	}
	return time.Now().Add(time.Minute)
}

// statusError is GitHub's answer of an unexpected status to what: a 401 is facts.ErrRefused.
func statusError(status int, what string) error {
	if status == http.StatusUnauthorized {
		return facts.ErrRefused
	}
	return fmt.Errorf("GitHub answered %d to %s", status, what)
}
