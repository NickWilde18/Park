// Package github is the GitHub forge kind: github.com, or a GitHub Enterprise Server at its own address.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/forges"
)

// Kind is the value that names this forge kind.
const Kind = "github"

func init() {
	forges.Register(Kind, forges.Kind{SignIn: signIn{client: &http.Client{Timeout: 15 * time.Second}}, PullURL: pullURL,
		PullPattern: pullPattern})
	facts.Register(Kind, newCollector)
}

// pullURL is a pull request's page on GitHub.
func pullURL(base, owner, name string, number int) string {
	return strings.TrimSuffix(base, "/") + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/pull/" + strconv.Itoa(number)
}

// pullPattern matches a pull request's page on GitHub, at base, as pullURL writes it: owner, name, and number.
func pullPattern(base string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(strings.TrimSuffix(base, "/")) + `/([\w.-]+)/([\w.-]+)/pull/(\d+)`)
}

// APIBase is the REST API's address for a forge's base address: api.github.com for github.com, and /api/v3 on an
// Enterprise Server.
func APIBase(base string) string {
	u, err := url.Parse(base)
	if err == nil && (u.Host == "github.com" || u.Host == "www.github.com") {
		return "https://api.github.com"
	}
	return strings.TrimSuffix(base, "/") + "/api/v3"
}

// signIn is GitHub's OAuth sign-in. It asks for no scopes: reading the account needs none.
type signIn struct {
	client *http.Client
}

func (signIn) AuthorizeURL(base, clientID, redirectURI, state, challenge string) string {
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"allow_signup":          {"false"},
	}
	return strings.TrimSuffix(base, "/") + "/login/oauth/authorize?" + q.Encode()
}

func (s signIn) Exchange(ctx context.Context, base, clientID, clientSecret, code, redirectURI, verifier string) (string, error) {
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/login/oauth/access_token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := s.do(req, &out); err != nil {
		return "", fmt.Errorf("exchange the authorization code: %w", err)
	}
	if out.Error != "" || out.AccessToken == "" {
		// GitHub answers 200 with an error code, such as bad_verification_code; the code carries no secret.
		return "", fmt.Errorf("exchange the authorization code: the forge refused it (%s)", out.Error)
	}
	return out.AccessToken, nil
}

func (s signIn) Account(ctx context.Context, base, accessToken string) (forges.Account, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, APIBase(base)+"/user", nil)
	if err != nil {
		return forges.Account{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	var out struct {
		Login string `json:"login"`
		Name  string `json:"name"`
		ID    int64  `json:"id"`
	}
	if err := s.do(req, &out); err != nil {
		return forges.Account{}, fmt.Errorf("read the account: %w", err)
	}
	if out.ID == 0 {
		return forges.Account{}, errors.New("read the account: the forge returned no account ID")
	}
	return forges.Account{ID: out.ID, Login: out.Login, Name: out.Name}, nil
}

// do sends a request and decodes a JSON answer. An error names the status, never the request's credentials.
func (s signIn) do(req *http.Request, out any) error {
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s answered %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}
