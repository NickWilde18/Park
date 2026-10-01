package docs

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Policy is the Content-Security-Policy of every answer under /docs. A sandbox without allow-same-origin puts the
// page in an origin of its own wherever it is opened, so that its scripts run without the viewer's session and
// cannot call Park as them; only Park's own pages may frame it.
const Policy = "sandbox allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads; " +
	"frame-ancestors 'self'"

// Sandbox is the sandbox of the frames that show a description, the same as Policy's.
const Sandbox = "allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads"

// Serve answers GET /docs/{token}/{path...} with a file of the description the token names, or its index.html for
// the top and for a folder. No session is read, and none is needed: the token is the description's key (G17). The
// answer sends no referrer, so that the token stays out of what the page asks other sites for. Ranges and
// conditional requests are answered, so that videos seek; a token's files never change.
func (s *Service) Serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", Policy)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Robots-Tag", "noindex")
	p := r.PathValue("path")
	if p == "" || strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	var contentType string
	var content []byte
	var at time.Time
	err := s.pool.QueryRow(r.Context(), `SELECT f.content_type, f.content, d.uploaded_at FROM group_doc d
		JOIN group_doc_file f ON f.doc_id = d.id WHERE d.token = $1 AND f.path = $2`, r.PathValue("token"), p).
		Scan(&contentType, &content, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "There is no such file.", http.StatusNotFound)
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "read a description's file", "error", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, p, at, bytes.NewReader(content))
}
