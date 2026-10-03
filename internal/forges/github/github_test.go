package github

import (
	"net/url"
	"strings"
	"testing"
)

// github.com's API lives at api.github.com; an Enterprise Server's under /api/v3 of its own address.
func TestAPIBase(t *testing.T) {
	for base, want := range map[string]string{
		"https://github.com":       "https://api.github.com",
		"https://github.com/":      "https://api.github.com",
		"https://git.example.com":  "https://git.example.com/api/v3",
		"https://git.example.com/": "https://git.example.com/api/v3",
		"http://127.0.0.1:9999":    "http://127.0.0.1:9999/api/v3",
	} {
		if got := APIBase(base); got != want {
			t.Errorf("%s: got %s, want %s", base, got, want)
		}
	}
}

// The authorization asks for PKCE with S256 and for no scopes, and sends the browser back to Park.
func TestAuthorizeURL(t *testing.T) {
	to := signIn{}.AuthorizeURL("https://github.com/", "client", "https://park.test/api/v1/auth/hub/callback", "state", "challenge")
	u, err := url.Parse(to)
	if err != nil || u.Host != "github.com" || u.Path != "/login/oauth/authorize" {
		t.Fatalf("got %s", to)
	}
	q := u.Query()
	if q.Get("code_challenge") != "challenge" || q.Get("code_challenge_method") != "S256" || q.Get("state") != "state" ||
		q.Get("client_id") != "client" || q.Get("redirect_uri") != "https://park.test/api/v1/auth/hub/callback" || q.Has("scope") {
		t.Fatalf("query %v", q)
	}
}

// A pull request's address is read back as pullURL writes it, on github.com and on a server of its own, wherever it is
// in a text and whatever follows its number; an issue's address, or another forge's, is not a pull request's.
func TestPullPattern(t *testing.T) {
	for _, base := range []string{"https://github.com", "https://git.example.test/"} {
		p := pullPattern(base)
		link := pullURL(base, "acme", "web.app", 12)
		m := p.FindStringSubmatch("see " + link + "/files#diff-1, please")
		if len(m) != 4 || m[1] != "acme" || m[2] != "web.app" || m[3] != "12" {
			t.Fatalf("%s: got %q from %s", base, m, link)
		}
		for _, other := range []string{strings.TrimSuffix(base, "/") + "/acme/web/issues/12", "https://elsewhere.test/acme/web/pull/12"} {
			if p.MatchString(other) {
				t.Fatalf("%s: matched %s", base, other)
			}
		}
	}
}
