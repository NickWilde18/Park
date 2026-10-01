package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// app authenticates as a GitHub App installation: a JWT signed with the app's private key buys an installation
// token, which lasts an hour and is renewed five minutes before it expires.
type app struct {
	expires        time.Time
	key            *rsa.PrivateKey
	http           *http.Client
	token          string
	rest           string
	appID          int64
	installationID int64
	mu             sync.Mutex
}

func newApp(rest string, appID, installationID int64, privateKey string, hc *http.Client) (*app, error) {
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return nil, errors.New("the app's private key is not PEM")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("the app's private key: %w", err)
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("the app's private key: %w", err)
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("the app's private key is not an RSA key")
		}
		key = rk
	default:
		return nil, fmt.Errorf("the app's private key is a %s", block.Type)
	}
	return &app{rest: rest, appID: appID, installationID: installationID, key: key, http: hc}, nil
}

// jwt is the app's own token, valid for nine minutes; issued a minute early for clock drift.
func (a *app) jwt(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(a.appID, 10),
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// authorization is the header for calls as the installation.
func (a *app) authorization(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if a.token != "" && now.Before(a.expires.Add(-5*time.Minute)) {
		return "token " + a.token, nil
	}
	jwt, err := a.jwt(now)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/app/installations/%d/access_tokens", a.rest, a.installationID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("get an installation token: %w", statusError(resp.StatusCode, req.URL.Path))
	}
	var out struct {
		ExpiresAt time.Time `json:"expires_at"`
		Token     string    `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	a.token, a.expires = out.Token, out.ExpiresAt
	return "token " + a.token, nil
}

// appAuthorization is the header for calls as the app itself, such as reading its delivery log.
func (a *app) appAuthorization(context.Context) (string, error) {
	jwt, err := a.jwt(time.Now())
	if err != nil {
		return "", err
	}
	return "Bearer " + jwt, nil
}
