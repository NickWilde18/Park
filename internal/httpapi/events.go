package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/NickWilde18/Park/internal/events"
)

// streamEvents streams notices as Server-Sent Events: docs/design/08-dashboard-and-api.md#live-updates. A stream
// resumes after its Last-Event-ID; one that has fallen further behind than notices are kept is sent a reload event,
// and a fresh one begins after the newest notice committed, so that it is sent any still committing. Notices are
// numbered in the order their changes commit, so a stream that has sent one has sent every one before it. A comment
// every 30 seconds keeps proxies from closing it.
func (s *server) streamEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := requireCaller(ctx); err != nil {
		s.respondError(w, r, err)
		return
	}
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	oldest, newest, last, err := s.events.Bounds(ctx)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	switch {
	case after <= 0:
		after = newest
	case events.Missed(after, oldest, last):
		_, _ = fmt.Fprint(w, "event: reload\ndata: {}\n\n")
		_ = rc.Flush()
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}
	poll := time.NewTicker(s.eventPoll)
	defer poll.Stop()
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()
	for {
		notices, err := s.events.After(ctx, after, 100)
		if err != nil {
			if ctx.Err() == nil {
				s.log.WarnContext(ctx, "read notices", "error", err)
			}
			return
		}
		for _, n := range notices {
			data, _ := json.Marshal(map[string]string{"subject": n.Subject})
			_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", n.ID, n.Kind, data)
			after = n.ID
		}
		if len(notices) > 0 {
			if err := rc.Flush(); err != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}
