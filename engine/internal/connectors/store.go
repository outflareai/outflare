package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/secrets"
)

// Integration statuses.
const (
	StatusConnected = "connected"
	StatusError     = "error" // the last check failed; see LastError
)

// Integration is a workspace's connected account. Credentials never leave
// the Store.
type Integration struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"-"`
	Connector   string          `json:"connector"`
	AuthMethod  string          `json:"auth_method"` // api_key or oauth2: how it was connected
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Account     Account         `json:"account"`
	Config      json.RawMessage `json:"config"`
	// The brand that owns the connection, or nil for a workspace
	// connection. A brand's own connection replaces the workspace's
	// connections to the same tool for that brand.
	BrandID *string `json:"brand_id"`
	// The brands a workspace connection is shared with; empty is every
	// brand.
	BrandIDs      []string   `json:"brand_ids"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	LastError     *string    `json:"last_error"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// outflareSecret matches Outflare's own secrets (oft_, ofa_, ofs_, ofi_, ofc_).
var outflareSecret = regexp.MustCompile(`^of[a-z]_[A-Za-z0-9_\-]{8,}`)

// ErrNotFound means no such integration in the workspace.
var ErrNotFound = errors.New("integration not found")

// Store keeps integrations and opens them for use.
type Store struct {
	Pool     *pgxpool.Pool
	Box      *secrets.Box
	Registry *Registry
	Client   *Client
	// OAuth apps by connector id, for consent screens and token refresh.
	OAuthApps map[string]OAuthApp
	// Hooks run inside the transaction that connects or disconnects an
	// integration, e.g. to queue a first sync or remove a webhook. An error
	// rolls the change back. Register them before serving.
	OnConnect    []func(ctx context.Context, tx pgx.Tx, i Integration) error
	OnDisconnect []func(ctx context.Context, tx pgx.Tx, i Integration) error
}

// Connected runs the OnConnect hooks.
func (s *Store) Connected(ctx context.Context, tx pgx.Tx, i Integration) error {
	for _, h := range s.OnConnect {
		if err := h(ctx, tx, i); err != nil {
			return err
		}
	}
	return nil
}

// Disconnecting runs the OnDisconnect hooks, before the row is deleted.
func (s *Store) Disconnecting(ctx context.Context, tx pgx.Tx, i Integration) error {
	for _, h := range s.OnDisconnect {
		if err := h(ctx, tx, i); err != nil {
			return err
		}
	}
	return nil
}

// Querier is a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const integrationCols = `id, workspace_id, connector, auth_method, name, status, account, config, brand_id, brand_ids::text[], last_checked_at, last_error, created_at, updated_at`

func scanIntegration(row pgx.Row) (Integration, error) {
	var i Integration
	var account []byte
	err := row.Scan(&i.ID, &i.WorkspaceID, &i.Connector, &i.AuthMethod, &i.Name, &i.Status, &account, &i.Config,
		&i.BrandID, &i.BrandIDs, &i.LastCheckedAt, &i.LastError, &i.CreatedAt, &i.UpdatedAt)
	if err == nil {
		_ = json.Unmarshal(account, &i.Account)
	}
	return i, err
}

// List returns the workspace's integrations, optionally for one connector.
func (s *Store) List(ctx context.Context, q Querier, ws, connector string) ([]Integration, error) {
	if connector != "" && s.Registry != nil {
		connector = s.Registry.Resolve(connector)
	}
	rows, err := q.Query(ctx, `SELECT `+integrationCols+` FROM integrations
		WHERE workspace_id = $1 AND ($2 = '' OR connector = $2) ORDER BY created_at`, ws, connector)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Integration{}
	for rows.Next() {
		i, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Serves reports whether the connection is used by the brand: its own,
// or a workspace connection shared with it when the brand has none of its
// own to the same tool among all (the workspace's connections). An empty
// brand (every brand) is served by every connection. It matches the
// integration_serves SQL function.
func (i Integration) Serves(brand string, all []Integration) bool {
	switch {
	case brand == "":
		return true
	case i.BrandID != nil:
		return *i.BrandID == brand
	case len(i.BrandIDs) > 0 && !slices.Contains(i.BrandIDs, brand):
		return false
	}
	for _, o := range all {
		if o.Connector == i.Connector && o.BrandID != nil && *o.BrandID == brand {
			return false
		}
	}
	return true
}

// ForBrand keeps the connections the brand uses (Serves); an empty brand
// keeps them all.
func ForBrand(list []Integration, brand string) []Integration {
	if brand == "" {
		return list
	}
	out := []Integration{}
	for _, i := range list {
		if i.Serves(brand, list) {
			out = append(out, i)
		}
	}
	return out
}

// Get returns one integration.
func (s *Store) Get(ctx context.Context, q Querier, ws, id string) (Integration, error) {
	i, err := scanIntegration(q.QueryRow(ctx, `SELECT `+integrationCols+` FROM integrations WHERE workspace_id = $1 AND id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return i, ErrNotFound
	}
	return i, err
}

// Test checks creds against the connector without saving anything.
func (s *Store) Test(ctx context.Context, connector string, creds Creds) (Account, error) {
	c, ok := s.Registry.Get(connector)
	if !ok {
		return Account{}, Errf(ErrInvalid, "unknown or unavailable connector %q", connector)
	}
	for _, v := range creds {
		// An Outflare secret pasted into another service's box would be
		// sent to that service. Refuse it before any call.
		if outflareSecret.MatchString(strings.TrimSpace(v)) {
			return Account{}, Errf(ErrInvalid, "That is an Outflare token, not a %s credential. Never paste Outflare tokens into another service; paste the key %s gave you.", c.Definition().Name, c.Definition().Name)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	creds, err := s.ready(ctx, c.Definition(), creds)
	if err != nil {
		return Account{}, err
	}
	return c.Test(ctx, s.Client, creds)
}

// ready adds what a pasted key needs before calls, without storing it: a
// Google service account key becomes a short-lived access token.
func (s *Store) ready(ctx context.Context, d Definition, creds Creds) (Creds, error) {
	if creds["service_account_json"] == "" || !IsGoogle(d) {
		return creds, nil
	}
	return ServiceAccountCreds(ctx, s.Client, d, creds)
}

// Insert saves a tested integration, connected with method (api_key or
// oauth2). Call it inside the transaction that writes the audit event.
func (s *Store) Insert(ctx context.Context, tx pgx.Tx, ws, connector, method, name string, creds Creds, account Account, actorID string) (Integration, error) {
	var id string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&id); err != nil {
		return Integration{}, err
	}
	sealed, err := s.seal(id, creds)
	if err != nil {
		return Integration{}, err
	}
	acct, _ := json.Marshal(account)
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	return scanIntegration(tx.QueryRow(ctx, `
		INSERT INTO integrations (id, workspace_id, connector, auth_method, name, status, credentials, account, last_checked_at, created_by)
		VALUES ($1, $2, $3, $4, $5, 'connected', $6, $7, now(), $8)
		RETURNING `+integrationCols, id, ws, connector, method, name, sealed, acct, actor))
}

// UpdateCreds replaces an integration's credentials, e.g. a refreshed
// OAuth token, keeping how it connects.
func (s *Store) UpdateCreds(ctx context.Context, q Querier, ws, id string, creds Creds) error {
	return s.SetCreds(ctx, q, ws, id, "", creds)
}

// SetCreds replaces an integration's credentials and, when method is set,
// how it connects: pasting an API key on an OAuth connection switches it
// to the key.
func (s *Store) SetCreds(ctx context.Context, q Querier, ws, id, method string, creds Creds) error {
	sealed, err := s.seal(id, creds)
	if err != nil {
		return err
	}
	var x string
	err = q.QueryRow(ctx, `UPDATE integrations SET credentials = $3, auth_method = COALESCE(NULLIF($4, ''), auth_method), updated_at = now()
		WHERE workspace_id = $1 AND id = $2 RETURNING id`,
		ws, id, sealed, method).Scan(&x)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Checked records the outcome of a check or a call: connected with the
// account, or error with the reason.
func (s *Store) Checked(ctx context.Context, q Querier, ws, id string, account *Account, callErr error) error {
	if callErr != nil {
		msg := callErr.Error()
		// Only credential problems mark the integration broken; a timeout
		// or an empty balance does not mean the key is wrong.
		status := StatusConnected
		if KindOf(callErr) == ErrAuth {
			status = StatusError
		}
		_, err := execQ(ctx, q, `UPDATE integrations SET status = $3, last_error = $4, last_checked_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, ws, id, status, msg)
		return err
	}
	acct, _ := json.Marshal(account)
	_, err := execQ(ctx, q, `UPDATE integrations SET status = 'connected', last_error = NULL, account = $3, last_checked_at = now(), updated_at = now()
		WHERE workspace_id = $1 AND id = $2`, ws, id, acct)
	return err
}

func execQ(ctx context.Context, q Querier, sql string, args ...any) (int64, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	rows.Close()
	return rows.CommandTag().RowsAffected(), rows.Err()
}

// Opened is an integration ready to call.
type Opened struct {
	Integration
	Connector Connector
	Creds     Creds
	Client    *Client
}

// Open loads an integration with its credentials and implementation.
func (s *Store) Open(ctx context.Context, q Querier, ws, id string) (*Opened, error) {
	var sealed []byte
	i, err := scanIntegrationWith(q.QueryRow(ctx, `SELECT `+integrationCols+`, credentials FROM integrations WHERE workspace_id = $1 AND id = $2`, ws, id), &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c, ok := s.Registry.Get(i.Connector)
	if !ok {
		return nil, Errf(ErrInvalid, "the %s connector is not available in this version", i.Connector)
	}
	plain, err := s.Box.Open(sealed, []byte(i.ID))
	if err != nil {
		return nil, err
	}
	var creds Creds
	if err := json.Unmarshal(plain, &creds); err != nil {
		return nil, err
	}
	d := c.Definition()
	if i.AuthMethod == AuthOAuth2 && d.OAuth != nil {
		fresh, changed, err := Refresh(ctx, s.Client, d, s.OAuthApps[d.ID], creds)
		if err != nil {
			_ = s.Checked(ctx, q, ws, id, nil, err)
			return nil, err
		}
		if changed {
			if err := s.UpdateCreds(ctx, q, ws, id, fresh); err != nil {
				return nil, err
			}
			creds = fresh
		}
	}
	if rf, ok := c.(CredsRefresher); ok {
		if creds, err = s.refreshOwn(ctx, ws, id, rf, creds); err != nil {
			_ = s.Checked(ctx, q, ws, id, nil, err)
			return nil, err
		}
	}
	if creds, err = s.ready(ctx, d, creds); err != nil {
		_ = s.Checked(ctx, q, ws, id, nil, err)
		return nil, err
	}
	return &Opened{Integration: i, Connector: c, Creds: creds, Client: s.Client}, nil
}

// refreshOwn renews credentials through the connector itself. With a pool
// it holds a lock on the connection and reads the credentials again under
// it, so two jobs never spend the same single-use refresh token.
func (s *Store) refreshOwn(ctx context.Context, ws, id string, rf CredsRefresher, creds Creds) (Creds, error) {
	if !rf.RefreshDue(creds) {
		return creds, nil
	}
	if s.Pool == nil {
		return rf.RefreshCreds(ctx, s.Client, creds)
	}
	out := creds
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('integration:' || $1, 0))`, id); err != nil {
			return err
		}
		var sealed []byte
		if err := tx.QueryRow(ctx, `SELECT credentials FROM integrations WHERE workspace_id = $1 AND id = $2`, ws, id).Scan(&sealed); err != nil {
			return err
		}
		plain, err := s.Box.Open(sealed, []byte(id))
		if err != nil {
			return err
		}
		var now Creds
		if err := json.Unmarshal(plain, &now); err != nil {
			return err
		}
		if !rf.RefreshDue(now) {
			// Another job renewed them while this one waited.
			out = now
			return nil
		}
		fresh, err := rf.RefreshCreds(ctx, s.Client, now)
		if err != nil {
			return err
		}
		out = fresh
		return s.UpdateCreds(ctx, tx, ws, id, fresh)
	})
	return out, err
}

func scanIntegrationWith(row pgx.Row, sealed *[]byte) (Integration, error) {
	var i Integration
	var account []byte
	err := row.Scan(&i.ID, &i.WorkspaceID, &i.Connector, &i.AuthMethod, &i.Name, &i.Status, &account, &i.Config,
		&i.BrandID, &i.BrandIDs, &i.LastCheckedAt, &i.LastError, &i.CreatedAt, &i.UpdatedAt, sealed)
	if err == nil {
		_ = json.Unmarshal(account, &i.Account)
	}
	return i, err
}

func (s *Store) seal(id string, creds Creds) ([]byte, error) {
	plain, err := json.Marshal(creds)
	if err != nil {
		return nil, err
	}
	return s.Box.Seal(plain, []byte(id)), nil
}

// ErrNoCapable means no connected integration in the workspace has the
// capability asked for.
var ErrNoCapable = errors.New("no connected integration can do this")

// OpenCapable opens the workspace's oldest connected integration with a
// capability that serves brand ("" for every brand), or returns
// ErrNoCapable.
func (s *Store) OpenCapable(ctx context.Context, q Querier, ws, brand, capability string) (*Opened, error) {
	list, err := s.List(ctx, q, ws, "")
	if err != nil {
		return nil, err
	}
	for _, i := range ForBrand(list, brand) {
		if i.Status != StatusConnected {
			continue
		}
		if d, ok := s.Registry.Definition(i.Connector); !ok || !d.Has(capability) {
			continue
		}
		return s.Open(ctx, q, ws, i.ID)
	}
	return nil, ErrNoCapable
}
