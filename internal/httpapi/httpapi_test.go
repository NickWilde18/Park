package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// built is a frontend as Vite builds it: index.html and hashed assets.
var built = fstest.MapFS{
	"index.html":       {Data: []byte("<!doctype html><title>Park</title>")},
	"assets/app-1a.js": {Data: []byte("console.log('park')")},
}

func ready(context.Context) error { return nil }

// newHandler builds the handler with a readiness check and a frontend, and returns it with its log.
func newHandler(t *testing.T, check func(context.Context) error, ui fs.FS) (http.Handler, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	h, err := New(Options{Ready: check, UI: ui, Logger: slog.New(slog.NewJSONHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return h, logs
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), method, target, nil))
	return w
}

// errorCode reads the code of an error response.
func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body Error
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("not an error body: %q", w.Body)
	}
	return body.Error.Code
}

// /healthz answers as long as the process is up.
func TestHealthz(t *testing.T) {
	h, _ := newHandler(t, ready, built)
	w := do(h, "GET", "/healthz")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
}

// /readyz reports ready when the check passes. When it fails, it answers 503 with a code; why it failed goes to the
// log, never into the response.
func TestReadyz(t *testing.T) {
	h, _ := newHandler(t, ready, built)
	if w := do(h, "GET", "/readyz"); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"status":"ready"}` {
		t.Fatalf("ready: got %d %q", w.Code, w.Body)
	}
	h, logs := newHandler(t, func(context.Context) error { return errors.New("connect to 10.0.0.7: refused") }, built)
	w := do(h, "GET", "/readyz")
	if w.Code != http.StatusServiceUnavailable || errorCode(t, w) != "not_ready" {
		t.Fatalf("not ready: got %d %q", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "10.0.0.7") {
		t.Errorf("the response leaks the cause: %q", w.Body)
	}
	if !strings.Contains(logs.String(), "10.0.0.7") {
		t.Errorf("the log lacks the cause: %q", logs)
	}
}

// The sign-in providers are an empty list, not null, until providers can be configured.
func TestListAuthProviders(t *testing.T) {
	h, _ := newHandler(t, ready, built)
	w := do(h, "GET", "/api/v1/auth/providers")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"providers":[]}` {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
}

// /api/v1/openapi.json serves the contract, as JSON, with the paths of api/openapi.yaml.
func TestContract(t *testing.T) {
	h, _ := newHandler(t, ready, built)
	w := do(h, "GET", "/api/v1/openapi.json")
	var doc struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &doc) != nil {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
	if doc.OpenAPI != "3.0.3" || doc.Paths["/auth/providers"] == nil {
		t.Fatalf("got %+v", doc)
	}
}

// An unknown path under /api/ is a JSON error, never the frontend's page.
func TestUnknownEndpoint(t *testing.T) {
	h, _ := newHandler(t, ready, built)
	w := do(h, "GET", "/api/v1/no-such-thing")
	if w.Code != http.StatusNotFound || errorCode(t, w) != "not_found" {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
}

// The frontend: "/" and the frontend's own routes serve index.html, never cached; built files are served, and
// hashed assets are cached for good; a missing file is a 404; only reads are allowed.
func TestFrontend(t *testing.T) {
	h, _ := newHandler(t, ready, built)
	for _, path := range []string{"/", "/index.html", "/groups/12"} {
		w := do(h, "GET", path)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<title>Park</title>") {
			t.Errorf("%s: got %d %q", path, w.Code, w.Body)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control %q", path, cc)
		}
	}
	w := do(h, "GET", "/assets/app-1a.js")
	if w.Code != http.StatusOK || w.Body.String() != "console.log('park')" {
		t.Errorf("asset: got %d %q", w.Code, w.Body)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset: Cache-Control %q", cc)
	}
	if w := do(h, "GET", "/assets/app-0f.js"); w.Code != http.StatusNotFound {
		t.Errorf("missing asset: got %d", w.Code)
	}
	if w := do(h, "POST", "/"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: got %d", w.Code)
	}
}

// A binary built without the frontend says so on "/".
func TestNoFrontend(t *testing.T) {
	h, _ := newHandler(t, ready, fstest.MapFS{".gitkeep": {}})
	w := do(h, "GET", "/")
	if w.Code != http.StatusNotFound || w.Body.String() != noFrontend {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
}

// A panic becomes a logged error and a 500 with a code. When the handler already answered, the answer stands.
// http.ErrAbortHandler passes through.
func TestRecoverPanics(t *testing.T) {
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	w := httptest.NewRecorder()
	recoverPanics(log, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })).
		ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), "GET", "/x", nil))
	if w.Code != http.StatusInternalServerError || errorCode(t, w) != "internal" || strings.Contains(w.Body.String(), "boom") {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
	if !strings.Contains(logs.String(), "handler panicked") || !strings.Contains(logs.String(), "boom") {
		t.Fatalf("log: %q", logs)
	}

	w = httptest.NewRecorder()
	recoverPanics(log, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("late")
	})).ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), "GET", "/x", nil))
	if w.Code != http.StatusAccepted || w.Body.Len() != 0 {
		t.Fatalf("after answering: got %d %q", w.Code, w.Body)
	}

	defer func() {
		if err, _ := recover().(error); !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", err)
		}
	}()
	recoverPanics(log, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), "GET", "/x", nil))
}

// Requests are logged with their status; successful health checks are not.
func TestLogRequests(t *testing.T) {
	h, logs := newHandler(t, ready, built)
	do(h, "GET", "/healthz")
	do(h, "GET", "/readyz")
	if logs.Len() != 0 {
		t.Fatalf("health checks were logged: %q", logs)
	}
	do(h, "GET", "/api/v1/auth/providers")
	var entry struct {
		Msg    string `json:"msg"`
		Path   string `json:"path"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil || entry.Msg != "request" ||
		entry.Path != "/api/v1/auth/providers" || entry.Status != http.StatusOK {
		t.Fatalf("log %q: %+v, %v", logs, entry, err)
	}
}
