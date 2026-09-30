package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// recoverPanics turns a panic in a handler into a logged error and a 500 response, if nothing was sent yet.
// http.ErrAbortHandler is passed on: it is how a handler aborts a response on purpose.
func recoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			log.ErrorContext(r.Context(), "handler panicked", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
			if !rec.wrote {
				writeError(rec, http.StatusInternalServerError, "internal", "Something went wrong.")
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

// logRequests logs every request with its status and duration, except successful health checks.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") && rec.status == http.StatusOK {
			return
		}
		log.InfoContext(r.Context(), "request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

// recorder notes the status a handler sends. Unwrap lets http.ResponseController reach the writer beneath, for
// flushing a stream.
type recorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *recorder) WriteHeader(status int) {
	if !r.wrote {
		r.status, r.wrote = status, true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
