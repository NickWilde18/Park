// Package docs keeps groups' descriptions: docs/design/03-grouping.md#descriptions.
//
// A description is a static site — an index.html and the files it loads — that a member uploads as a zip. Read checks
// the zip and takes its files out; the service stores them under a random token and serves them at /docs/{token}/…
// to anyone who has the link. Every answer is sandboxed into an origin of its own (D66), so that a page's scripts can
// neither read the viewer's session nor call Park as them.
//
// Who may change a group's description is decided by the caller, which knows the group's pull requests and the
// member's accounts; this package only stores and serves.
package docs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/fail"
)

// The limits on a description's zip (03-grouping.md#defaults).
const (
	MaxZip      = 20 << 20
	MaxUnpacked = 100 << 20
	MaxFiles    = 500
)

// Doc is a group's description, as the dashboard names it.
type Doc struct {
	UploadedAt time.Time
	// Token is what the description's link carries; a new upload gets a new one.
	Token string
	// UploaderName is the display name of the member who uploaded it.
	UploaderName string
	Size         int64
	UploadedBy   int64
	Group        int64
	Files        int
}

// Link is the description's address on Park, built when it is shown and never stored: its index.html is at the top.
func (d Doc) Link() string { return "/docs/" + d.Token + "/" }

// Service stores and serves descriptions.
type Service struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	now  func() time.Time
}

// New returns the descriptions' service.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, log: log, now: time.Now}
}

var errNoDoc = fail.New(fail.NotFound, "not_found", "This group has no description.")

// Put stores a group's description, as the member uploaded it, replacing the one it had: the old files go, and the old
// link stops working, since the new one carries a new token.
func (s *Service) Put(ctx context.Context, group, member int64, files []File) (Doc, error) {
	token, err := newToken()
	if err != nil {
		return Doc{}, err
	}
	d := Doc{Group: group, Token: token, Files: len(files), UploadedBy: member, UploadedAt: s.now()}
	for _, f := range files {
		d.Size += int64(len(f.Content))
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM group_doc WHERE group_id = $1", group); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO group_doc (group_id, token, files, size, uploaded_by, uploaded_at)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, group, token, d.Files, d.Size, member, d.UploadedAt).Scan(&id); err != nil {
			return err
		}
		rows := make([][]any, 0, len(files))
		for _, f := range files {
			rows = append(rows, []any{id, f.Path, f.ContentType, f.Content})
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"group_doc_file"}, []string{"doc_id", "path", "content_type", "content"},
			pgx.CopyFromRows(rows))
		return err
	})
	return d, err
}

// Delete removes a group's description and its files.
func (s *Service) Delete(ctx context.Context, group int64) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM group_doc WHERE group_id = $1", group)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNoDoc
	}
	return nil
}

// Of returns the descriptions of groups, by group; a group with none is not in it.
func (s *Service) Of(ctx context.Context, groups []int64) (map[int64]Doc, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.group_id, d.token, d.files, d.size, d.uploaded_by, m.display_name, d.uploaded_at
		FROM group_doc d JOIN member m ON m.id = d.uploaded_by WHERE d.group_id = ANY($1)`, groups)
	if err != nil {
		return nil, err
	}
	out := map[int64]Doc{}
	for rows.Next() {
		var d Doc
		if err := rows.Scan(&d.Group, &d.Token, &d.Files, &d.Size, &d.UploadedBy, &d.UploaderName, &d.UploadedAt); err != nil {
			return nil, err
		}
		out[d.Group] = d
	}
	return out, rows.Err()
}

// newToken is a link's token: 128 random bits, so that a link cannot be guessed.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", errors.Join(errors.New("read random bytes for a description's token"), err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
