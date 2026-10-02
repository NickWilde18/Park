package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
	"github.com/NickWilde18/Park/internal/work"
)

// siteZip is a description's zip: a page, a screenshot, and a sixteen-byte video.
func siteZip(t *testing.T, heading string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"index.html":   "<h1>" + heading + `</h1><img src="shots/a.png"><video src="video/v.mp4"></video>`,
		"shots/a.png":  "\x89PNG fake",
		"video/v.mp4":  "0123456789abcdef",
		"__MACOSX/._x": "junk",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// A group's description, through the API (G17). Ana wrote the group's pull requests, and Carol wrote none: Carol may
// not upload one, and Ana may, and then everyone reads it on the group, though only Ana may change it. Its link
// serves the site to anyone, without a session, sandboxed and without a referrer, its video by ranges. A token may not
// upload; neither may a body that is not a zip, nor a zip that is not a site. Uploading again moves the description to
// a new link, and the old one stops working; removing it ends the link too. An admin may upload, though they wrote
// none of the pull requests. No link's token reaches the log.
func TestGroupDocsOverHTTP(t *testing.T) {
	pool := storetest.MigratedPool(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	box, _ := secret.New(secretKey)
	ids, err := identity.New(pool, box, log, identity.Options{SecretKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	collected := facts.New(pool, box, log)
	groups := grouping.New(pool, log)
	collected.Follow(groups.FactsFollower())
	runner := work.NewRunner(pool, log, work.Options{})
	collected.Register(runner)
	groups.Register(runner)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h, err := New(Options{Ready: ready, UI: built, Logger: log, Identity: ids, Facts: collected, Grouping: groups, Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	a := &harness{h: h, ids: ids, pool: pool, logs: logs}
	admin := a.admin(t)
	var f Forge
	admin.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "testhub", "name": "Hub", "base_address": "https://hub.example.test"}, &f)
	var inst Installation
	admin.call(t, "POST", "/api/v1/admin/installations", map[string]any{"forge_id": f.Id, "name": "acme", "kind": "token", "token": "t"}, &inst)
	for _, name := range []string{"api", "web"} {
		wantStatus(t, admin.call(t, "POST", "/api/v1/admin/repos", map[string]any{"installation_id": inst.Id, "owner": "acme", "name": name}, nil),
			http.StatusCreated, "")
	}
	var prs PullRequests
	for deadline := time.Now().Add(10 * time.Second); len(prs.PullRequests) < 2; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the pull requests were never collected; log:\n%s", logs.String())
		}
		admin.call(t, "GET", "/api/v1/prs", nil, &prs)
	}
	member := func(name string, account int64) *browser {
		t.Helper()
		var inv NewInvite
		wantStatus(t, admin.call(t, "POST", "/api/v1/admin/invites", map[string]any{}, &inv), http.StatusCreated, "")
		b := a.browser()
		wantStatus(t, b.call(t, "POST", "/api/v1/auth/register", map[string]string{"invite": inv.Code, "username": strings.ToLower(name),
			"password": "a long enough password", "display_name": name}, nil), http.StatusCreated, "")
		b.refreshCSRF(t)
		var me Me
		b.call(t, "GET", "/api/v1/me", nil, &me)
		wantStatus(t, admin.call(t, "POST", "/api/v1/admin/members/"+strconv.FormatInt(me.Member.Id, 10)+"/identities",
			map[string]any{"forge_id": f.Id, "account_id": account}, nil), http.StatusCreated, "")
		return b
	}
	ana, carol := member("Ana", 501), member("Carol", 503)
	var g Group
	wantStatus(t, admin.call(t, "POST", "/api/v1/groups", map[string]any{"name": "Billing",
		"pull_requests": []int64{prs.PullRequests[0].Id, prs.PullRequests[1].Id}}, &g), http.StatusCreated, "")
	doc := "/api/v1/groups/" + strconv.FormatInt(g.Id, 10) + "/doc"
	upload := func(b *browser, body string) *httptest.ResponseRecorder {
		return b.raw("PUT", doc, "application/zip", body)
	}
	read := func(b *browser) Group {
		t.Helper()
		var out Group
		wantStatus(t, b.call(t, "GET", "/api/v1/groups/"+strconv.FormatInt(g.Id, 10), nil, &out), http.StatusOK, "")
		return out
	}

	wantStatus(t, upload(carol, siteZip(t, "Billing")), http.StatusForbidden, "not_an_author")
	w := upload(ana, siteZip(t, "Billing"))
	wantStatus(t, w, http.StatusOK, "")
	first := read(ana)
	if first.Doc == nil || !strings.HasPrefix(first.Doc.Link, "/docs/") || first.Doc.Files != 3 || first.Doc.UploadedBy != "Ana" ||
		first.DocEditable == nil || !*first.DocEditable {
		t.Fatalf("Ana's view: %+v %+v", first.Doc, first.DocEditable)
	}
	if theirs := read(carol); theirs.Doc == nil || theirs.Doc.Link != first.Doc.Link || theirs.DocEditable == nil || *theirs.DocEditable {
		t.Fatalf("Carol's view: %+v %+v", theirs.Doc, theirs.DocEditable)
	}
	var listed Groups
	carol.call(t, "GET", "/api/v1/groups", nil, &listed)
	if len(listed.Groups) != 1 || listed.Groups[0].Doc == nil || listed.Groups[0].Doc.Link != first.Doc.Link {
		t.Fatalf("listed: %+v", listed)
	}

	get := func(target string, header ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), "GET", target, nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	page := get(first.Doc.Link)
	csp := page.Header().Get("Content-Security-Policy")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "<h1>Billing</h1>") ||
		page.Header().Get("Content-Type") != "text/html; charset=utf-8" || !strings.HasPrefix(csp, "sandbox ") ||
		strings.Contains(csp, "allow-same-origin") || page.Header().Get("Referrer-Policy") != "no-referrer" ||
		page.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("the page: %d %v %q", page.Code, page.Header(), page.Body)
	}
	// The page, which has no head, starts with the script that lets it follow the dashboard's theme, once.
	if body := page.Body.String(); !strings.HasPrefix(body, "<script>") || strings.Count(body, `"park-theme"`) != 1 ||
		!strings.HasSuffix(body, "<h1>Billing</h1>"+`<img src="shots/a.png"><video src="video/v.mp4"></video>`) {
		t.Fatalf("the page's theme script: %q", body)
	}
	if shot := get(first.Doc.Link + "shots/a.png"); shot.Code != http.StatusOK || shot.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("the screenshot: %d %v", shot.Code, shot.Header())
	}
	if part := get(first.Doc.Link+"video/v.mp4", "Range", "bytes=0-3"); part.Code != http.StatusPartialContent ||
		part.Body.String() != "0123" || part.Header().Get("Content-Range") != "bytes 0-3/16" {
		t.Fatalf("a range of the video: %d %v %q", part.Code, part.Header(), part.Body)
	}
	if missing := get(first.Doc.Link + "shots/b.png"); missing.Code != http.StatusNotFound {
		t.Fatalf("a file the site lacks: %d", missing.Code)
	}
	if unknown := get("/docs/no-such-token/"); unknown.Code != http.StatusNotFound {
		t.Fatalf("a link Park never gave: %d", unknown.Code)
	}

	var token NewToken
	ana.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "reader", "scopes": []string{"read"}}, &token)
	reader := a.browser()
	reader.bearer = token.Token
	wantStatus(t, upload(reader, siteZip(t, "x")), http.StatusForbidden, "insufficient_scope")
	wantStatus(t, ana.raw("PUT", doc, "text/plain", siteZip(t, "x")), http.StatusUnsupportedMediaType, "unsupported_media_type")
	wantStatus(t, ana.raw("POST", "/api/v1/groups", "application/zip", siteZip(t, "x")), http.StatusUnsupportedMediaType,
		"unsupported_media_type")
	wantStatus(t, upload(ana, "not a zip"), http.StatusBadRequest, "not_a_zip")

	wantStatus(t, upload(ana, siteZip(t, "Billing, take two")), http.StatusOK, "")
	second := read(ana)
	if second.Doc == nil || second.Doc.Link == first.Doc.Link {
		t.Fatalf("the replaced description: %+v", second.Doc)
	}
	if old := get(first.Doc.Link); old.Code != http.StatusNotFound {
		t.Fatalf("the replaced description's old link: %d", old.Code)
	}
	if now := get(second.Doc.Link); !strings.Contains(now.Body.String(), "take two") {
		t.Fatalf("the new link: %q", now.Body)
	}

	wantStatus(t, carol.call(t, "DELETE", doc, nil, nil), http.StatusForbidden, "not_an_author")
	wantStatus(t, ana.call(t, "DELETE", doc, nil, nil), http.StatusNoContent, "")
	if gone := get(second.Doc.Link); gone.Code != http.StatusNotFound {
		t.Fatalf("a removed description's link: %d", gone.Code)
	}
	if after := read(ana); after.Doc != nil {
		t.Fatalf("the group still has a description: %+v", after.Doc)
	}
	wantStatus(t, ana.call(t, "DELETE", doc, nil, nil), http.StatusNotFound, "not_found")
	wantStatus(t, upload(admin, siteZip(t, "By the admin")), http.StatusOK, "")

	for _, link := range []string{first.Doc.Link, second.Doc.Link} {
		if strings.Contains(logs.String(), strings.TrimSuffix(strings.TrimPrefix(link, "/docs/"), "/")) {
			t.Fatalf("the log holds a description's token, of %s", link)
		}
	}
}
