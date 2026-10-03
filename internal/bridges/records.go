package bridges

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/NickWilde18/Park/internal/grouping"
)

// querier is what reads and writes go through: the pool, or a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Bridge is a bridge as admins see it. Plugin is the kind of the plugin Park runs it through, and empty for an
// external bridge; the settings after it are a Park-run bridge's. Credentials are the keys of the credentials that
// are set, never their values; Chat is empty until an admin chooses one, and Account nil until one is connected.
type Bridge struct {
	CreatedAt    time.Time
	Account      *ConnectedAccount
	LastRound    *Round
	Chat         Chat
	Model        ModelSettings
	Name         string
	LinkTemplate string
	Plugin       string
	Credentials  []string
	ID           int64
	OwnerID      int64
	EveryMinutes int
	LookbackDays int
	ProposeFrom  int
	Enabled      bool
	// Late is set for a Park-run bridge that is on, and whose last round ended more than two periods ago.
	Late bool
}

// ModelSettings are a Park-run bridge's model: the address of a service that speaks the chat completions protocol, the
// model's name, how long an answer may take, and whether its key is set, which is never shown. Without an address, the
// bridge asks no model.
type ModelSettings struct {
	URL            string
	Name           string
	TimeoutSeconds int
	KeySet         bool
}

// Round is one round of a Park-run bridge: when it ran, how many messages it read, how many assertions and proposals it
// posted or retracted, and how it ended, ok or failed, with the code of what went wrong.
type Round struct {
	StartedAt time.Time
	EndedAt   time.Time
	Outcome   string
	Reason    string
	Read      int
	Changes   int
}

// ConnectedAccount is the account a Park-run bridge reads as: who it is on the platform; which member connected it,
// and when; and when the plugin last said that it has to be connected again, if it did since.
type ConnectedAccount struct {
	ConnectedAt time.Time
	LapsedAt    *time.Time
	Account
	ConnectedBy int64
}

// credentialsContext and tokensContext are what a bridge's credentials and its account's tokens are sealed for, so
// that a sealed value copied to another bridge's row does not open there.
func credentialsContext(id int64) string { return "bridge_credentials:" + strconv.FormatInt(id, 10) }
func tokensContext(id int64) string      { return "bridge_tokens:" + strconv.FormatInt(id, 10) }

// modelKeyContext is what a bridge's model key is sealed for.
func modelKeyContext(id int64) string { return "bridge_model_key:" + strconv.FormatInt(id, 10) }

const bridgeColumns = `b.id, b.name, b.owner_id, b.link_template, b.created_at, coalesce(b.plugin, ''), b.enabled, b.credentials,
	b.chat, b.chat_name, b.every_minutes, b.lookback_days, b.propose_from,
	b.model_url, b.model_name, b.model_key IS NOT NULL, b.model_timeout_seconds,
	a.subject, a.name, a.connected_by, a.connected_at, a.lapsed_at,
	r.started_at, r.ended_at, r.read, r.changes, r.outcome, r.reason`

const bridgeFrom = ` FROM bridge b LEFT JOIN bridge_account a ON a.bridge_id = b.id
	LEFT JOIN LATERAL (SELECT started_at, ended_at, read, changes, outcome, reason FROM bridge_round
		WHERE bridge_id = b.id ORDER BY ended_at DESC, id DESC LIMIT 1) r ON true`

// scanBridge reads a row of bridgeColumns, opening the credentials only to learn which keys are set.
func (s *Service) scanBridge(row pgx.Row) (Bridge, error) {
	var b Bridge
	var sealed []byte
	var subject, name *string
	var by *int64
	var at *time.Time
	var lapsed *time.Time
	var started, ended *time.Time
	var roundRead, roundChanges *int
	var outcome, reason *string
	if err := row.Scan(&b.ID, &b.Name, &b.OwnerID, &b.LinkTemplate, &b.CreatedAt, &b.Plugin, &b.Enabled, &sealed, &b.Chat.ID,
		&b.Chat.Name, &b.EveryMinutes, &b.LookbackDays, &b.ProposeFrom, &b.Model.URL, &b.Model.Name, &b.Model.KeySet,
		&b.Model.TimeoutSeconds, &subject, &name, &by, &at, &lapsed, &started, &ended, &roundRead, &roundChanges, &outcome,
		&reason); err != nil {
		return Bridge{}, err
	}
	if started != nil {
		b.LastRound = &Round{StartedAt: *started, EndedAt: *ended, Read: *roundRead, Changes: *roundChanges, Outcome: *outcome,
			Reason: *reason}
		period := time.Duration(b.EveryMinutes) * time.Minute
		b.Late = b.Plugin != "" && b.Enabled && b.LastRound.EndedAt.Before(s.now().Add(-2*period))
	}
	creds, err := s.openCredentials(b.ID, sealed)
	if err != nil {
		return Bridge{}, err
	}
	b.Credentials = []string{}
	for key := range creds {
		b.Credentials = append(b.Credentials, key)
	}
	slices.Sort(b.Credentials)
	if subject != nil {
		b.Account = &ConnectedAccount{Account: Account{ID: *subject, Name: *name}, ConnectedBy: *by, ConnectedAt: *at, LapsedAt: lapsed}
	}
	return b, nil
}

// openCredentials opens a bridge's sealed credentials: none when nothing is sealed.
func (s *Service) openCredentials(id int64, sealed []byte) (map[string]string, error) {
	creds := map[string]string{}
	if len(sealed) == 0 {
		return creds, nil
	}
	data, err := s.box.Open(sealed, credentialsContext(id))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// List lists every bridge, external and Park-run, in the order they were registered.
func (s *Service) List(ctx context.Context) ([]Bridge, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+bridgeColumns+bridgeFrom+" ORDER BY b.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bridge{}
	for rows.Next() {
		b, err := s.scanBridge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Get returns one bridge.
func (s *Service) Get(ctx context.Context, id int64) (Bridge, error) {
	return s.get(ctx, s.pool, id, false)
}

// get reads one bridge through q, locking its row for the rest of q's transaction when lock is set.
func (s *Service) get(ctx context.Context, q querier, id int64, lock bool) (Bridge, error) {
	sql := "SELECT " + bridgeColumns + bridgeFrom + " WHERE b.id = $1"
	if lock {
		sql += " FOR UPDATE OF b"
	}
	b, err := s.scanBridge(q.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Bridge{}, errNotFound
	}
	return b, err
}

// Create registers a bridge that Park runs through a plugin, owned by a member. The plugin must be one Park has, and
// switched on. The bridge starts switched on, with its plugin's settings at their defaults, and with no credentials,
// account, or chat, so that it has no rounds until an admin gives it them.
func (s *Service) Create(ctx context.Context, owner int64, name, linkTemplate, plugin string) (Bridge, error) {
	name, err := grouping.CheckBridge(name, linkTemplate)
	if err != nil {
		return Bridge{}, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.usable(ctx, tx, plugin); err != nil {
			return err
		}
		return tx.QueryRow(ctx, "INSERT INTO bridge (name, owner_id, link_template, plugin) VALUES ($1, $2, $3, $4) RETURNING id",
			name, owner, linkTemplate, plugin).Scan(&id)
	})
	if err != nil {
		return Bridge{}, err
	}
	return s.Get(ctx, id)
}
