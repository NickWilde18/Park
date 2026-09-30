// Package githubtest is a fake GitHub for tests: the OAuth endpoints that sign-in uses. Its token endpoint checks
// the client secret, and the PKCE verifier against the challenge the member authorized with.
package githubtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/NickWilde18/Park/internal/forges"
)

// Fake is a fake GitHub. Its URL is the forge's base address.
type Fake struct {
	srv    *httptest.Server
	codes  map[string]grant
	tokens map[string]forges.Account
	URL    string
	secret string
	mu     sync.Mutex
	n      int
}

type grant struct {
	challenge string
	account   forges.Account
}

// New starts a fake GitHub that accepts clientSecret, and stops it when the test ends.
func New(t testing.TB, clientSecret string) *Fake {
	f := &Fake{codes: map[string]grant{}, tokens: map[string]forges.Account{}, secret: clientSecret}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", f.token)
	mux.HandleFunc("GET /api/v3/user", f.user)
	f.srv = httptest.NewServer(mux)
	f.URL = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

// Authorize is the member approving a sign-in at GitHub: it returns the code the browser comes back with.
func (f *Fake) Authorize(challenge string, a forges.Account) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	code := "code-" + strconv.Itoa(f.n)
	f.codes[code] = grant{challenge: challenge, account: a}
	return code
}

func (f *Fake) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.codes[r.Form.Get("code")]
	delete(f.codes, r.Form.Get("code"))
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	w.Header().Set("Content-Type", "application/json")
	if !ok || r.Form.Get("client_secret") != f.secret || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
		return
	}
	f.n++
	token := "user-token-" + strconv.Itoa(f.n)
	f.tokens[token] = g.account
	_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token})
}

func (f *Fake) user(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	a, ok := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": a.ID, "login": a.Login, "name": a.Name})
}
