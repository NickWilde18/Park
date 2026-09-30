// Package httpapi serves Park over HTTP: the API under /api/v1, whose handlers are generated from the contract in
// api/openapi.yaml; the health checks; and the embedded frontend.
//
// Errors are JSON, {"error": {"code": "…", "message": "…"}}, with stable codes. What went wrong inside is logged
// and never sent.
package httpapi

//go:generate go tool oapi-codegen -config oapi-codegen.yaml ../../api/openapi.yaml

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.yaml.in/yaml/v3"

	"github.com/NickWilde18/Park/api"
	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/tasks"
	"github.com/NickWilde18/Park/internal/verdicts"
)

// Options are what the handler needs from the rest of Park.
type Options struct {
	// Ready reports whether Park can serve requests: the database answers and its schema is current.
	Ready func(context.Context) error
	// UI is the built frontend. Without an index.html in it, "/" says that this build has no frontend.
	UI       fs.FS
	Logger   *slog.Logger
	Identity *identity.Service
	Facts    *facts.Service
	Grouping *grouping.Service
	Rules    *rules.Service
	Verdicts *verdicts.Service
	Tasks    *tasks.Service
	Pool     *pgxpool.Pool
}

// timeNow is the clock errors that carry a wait read.
var timeNow = time.Now

// New returns Park's HTTP handler.
func New(opts Options) (http.Handler, error) {
	spec, err := contractJSON()
	if err != nil {
		return nil, err
	}
	log := opts.Logger
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", readyz(opts.Ready, log))
	mux.HandleFunc("GET /api/v1/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec)
	})
	badRequest := func(w http.ResponseWriter, r *http.Request, err error) {
		log.InfoContext(r.Context(), "bad request", "path", r.URL.Path, "error", err)
		writeError(w, http.StatusBadRequest, "bad_request", "The request is not valid.")
	}
	srv := &server{id: opts.Identity, facts: opts.Facts, grouping: opts.Grouping, rules: opts.Rules, verdicts: opts.Verdicts, tasks: opts.Tasks, pool: opts.Pool,
		log: log}
	strict := NewStrictHandlerWithOptions(srv, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  badRequest,
		ResponseErrorHandlerFunc: srv.respondError,
	})
	HandlerWithOptions(strict, StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: badRequest})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "There is no such endpoint.")
	})
	if opts.Facts != nil {
		mux.Handle("POST /hooks/{installation}", opts.Facts.Webhook())
	}
	mux.Handle("/", frontend(opts.UI))
	return recoverPanics(log, logRequests(log, guardJSON(srv.authenticate(mux)))), nil
}

// server implements the API.
type server struct {
	id       *identity.Service
	facts    *facts.Service
	grouping *grouping.Service
	rules    *rules.Service
	verdicts *verdicts.Service
	tasks    *tasks.Service
	pool     *pgxpool.Pool
	log      *slog.Logger
}

// statuses maps each kind of error the caller can act on to its HTTP status.
var statuses = map[fail.Kind]int{
	fail.Invalid:         http.StatusBadRequest,
	fail.Unauthenticated: http.StatusUnauthorized,
	fail.Forbidden:       http.StatusForbidden,
	fail.NotFound:        http.StatusNotFound,
	fail.Conflict:        http.StatusConflict,
	fail.RateLimited:     http.StatusTooManyRequests,
	fail.Unavailable:     http.StatusServiceUnavailable,
}

// respondError answers with an error the caller can act on, or logs any other error and answers 500.
func (s *server) respondError(w http.ResponseWriter, r *http.Request, err error) {
	if f, ok := fail.As(err); ok {
		if f.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(f.RetryAfter.Seconds()))))
		}
		writeError(w, statuses[f.Kind], f.Code, f.Message)
		return
	}
	s.log.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
}

func readyz(ready func(context.Context) error, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			log.WarnContext(ctx, "not ready", "error", err)
			writeError(w, http.StatusServiceUnavailable, "not_ready", "Park is not ready to serve requests.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

// contractJSON is the contract, converted from YAML to JSON.
func contractJSON() ([]byte, error) {
	var doc any
	if err := yaml.Unmarshal(api.OpenAPI, &doc); err != nil {
		return nil, fmt.Errorf("parse api/openapi.yaml: %w", err)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("convert api/openapi.yaml to JSON: %w", err)
	}
	return out, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body Error
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}
