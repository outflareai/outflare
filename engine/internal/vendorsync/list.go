package vendorsync

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Mailbox is a supplier mailbox as the API shows it.
type Mailbox struct {
	ExternalID   string   `json:"external_id"`
	Email        string   `json:"email"`
	Name         string   `json:"name"`
	Provider     string   `json:"provider"`
	Status       string   `json:"status"`
	VendorStatus string   `json:"vendor_status"`
	DailyLimit   *int     `json:"daily_limit"`
	Tags         []string `json:"tags"`
	ConnectedTo  []string `json:"connected_to"`
	ImportState  string   `json:"import_state"`
	ImportError  *string  `json:"import_error"`
	MailboxID    *string  `json:"mailbox_id"`
	// The Outflare mailbox's own status, once imported.
	MailboxStatus *string    `json:"mailbox_status"`
	ImportedAt    *time.Time `json:"imported_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Filter narrows a list.
type Filter struct {
	ImportState string // one state, or "" for all
	Status      string // one supplier status, or ""
	Query       string // part of the email
	Limit       int
	Offset      int
}

// Counts summarise an account's mailboxes.
type Counts struct {
	Total       int            `json:"total"`
	ByState     map[string]int `json:"by_import_state"`
	ByStatus    map[string]int `json:"by_status"`
	ByProvider  map[string]int `json:"by_provider"`
	ConnectedTo map[string]int `json:"connected_to"`
}

// List returns matching mailboxes, the number matching, and counts over the
// whole account.
func (s *Service) List(ctx context.Context, q connectors.Querier, ws, id string, f Filter) ([]Mailbox, int, Counts, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	query := "%" + strings.ToLower(strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.Query)) + "%"
	rows, err := q.Query(ctx, `
		SELECT v.external_id, v.email, trim(v.first_name || ' ' || v.last_name), v.provider, v.status, v.vendor_status,
		       v.daily_limit, v.tags, v.connected_to, v.import_state, v.import_error, v.mailbox_id, m.status,
		       v.imported_at, v.updated_at, count(*) OVER ()
		FROM vendor_mailboxes v LEFT JOIN mailboxes m ON m.id = v.mailbox_id
		WHERE v.workspace_id = $1 AND v.integration_id = $2
		  AND ($3 = '' OR v.import_state = $3) AND ($4 = '' OR v.status = $4) AND v.email LIKE $5
		ORDER BY v.email LIMIT $6 OFFSET $7`, ws, id, f.ImportState, f.Status, query, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, Counts{}, err
	}
	total := 0
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Mailbox, error) {
		var m Mailbox
		err := r.Scan(&m.ExternalID, &m.Email, &m.Name, &m.Provider, &m.Status, &m.VendorStatus, &m.DailyLimit, &m.Tags,
			&m.ConnectedTo, &m.ImportState, &m.ImportError, &m.MailboxID, &m.MailboxStatus, &m.ImportedAt, &m.UpdatedAt, &total)
		return m, err
	})
	if err != nil {
		return nil, 0, Counts{}, err
	}
	c := Counts{ByState: map[string]int{}, ByStatus: map[string]int{}, ByProvider: map[string]int{}, ConnectedTo: map[string]int{}}
	for _, s := range []string{StateAvailable, StateQueued, StateWaiting, StateAppPassword, StateImported, StateFailed} {
		c.ByState[s] = 0
	}
	crow, err := q.Query(ctx, `
		SELECT import_state, status, provider, connected_to FROM vendor_mailboxes WHERE workspace_id = $1 AND integration_id = $2`, ws, id)
	if err != nil {
		return nil, 0, Counts{}, err
	}
	defer crow.Close()
	for crow.Next() {
		var state, status, provider string
		var to []string
		if err := crow.Scan(&state, &status, &provider, &to); err != nil {
			return nil, 0, Counts{}, err
		}
		c.Total++
		c.ByState[state]++
		c.ByStatus[status]++
		c.ByProvider[provider]++
		for _, p := range to {
			c.ConnectedTo[p]++
		}
	}
	return out, total, c, crow.Err()
}
