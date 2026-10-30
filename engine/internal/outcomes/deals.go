package outcomes

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Deal states.
const (
	DealOpen = "open"
	DealWon  = "won"
	DealLost = "lost"
)

// Deal is a copy of a CRM deal (or one logged by hand), for reporting.
type Deal struct {
	ID                string     `json:"id"`
	SequenceID        *string    `json:"sequence_id"`
	MissionID         *string    `json:"mission_id"`
	LeadID            *string    `json:"lead_id"`
	MeetingID         *string    `json:"meeting_id"`
	Source            string     `json:"source"`
	IntegrationID     *string    `json:"integration_id"`
	ExternalID        *string    `json:"external_id"`
	Name              string     `json:"name"`
	Stage             string     `json:"stage"`
	State             string     `json:"state"`
	AmountCents       *int64     `json:"amount_cents"`
	Currency          string     `json:"currency"`
	OpenedAt          time.Time  `json:"opened_at"`
	ClosedAt          *time.Time `json:"closed_at"`
	Attribution       string     `json:"attribution"`
	AttributionPinned bool       `json:"attribution_pinned"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	LeadEmail         *string    `json:"lead_email"`
	SequenceName      *string    `json:"sequence_name"`
}

const dealCols = `d.id, d.sequence_id, d.mission_id, d.lead_id, d.meeting_id, d.source, d.integration_id, d.external_id,
	d.name, d.stage, d.state, d.amount_cents, d.currency, d.opened_at, d.closed_at, d.attribution, d.attribution_pinned,
	d.created_at, d.updated_at, l.email, s.name`

const dealFrom = ` FROM deals d
	LEFT JOIN leads l ON l.id = d.lead_id
	LEFT JOIN sequences s ON s.id = d.sequence_id`

func scanDeal(row pgx.Row) (Deal, error) {
	var d Deal
	err := row.Scan(&d.ID, &d.SequenceID, &d.MissionID, &d.LeadID, &d.MeetingID, &d.Source, &d.IntegrationID,
		&d.ExternalID, &d.Name, &d.Stage, &d.State, &d.AmountCents, &d.Currency, &d.OpenedAt, &d.ClosedAt, &d.Attribution,
		&d.AttributionPinned, &d.CreatedAt, &d.UpdatedAt, &d.LeadEmail, &d.SequenceName)
	return d, err
}

// DealFilter narrows ListDeals.
type DealFilter struct {
	From, To   *time.Time // on opened_at
	SequenceID string
	MissionID  string
	State      string
	Attributed bool
	// Brand is a brand's id, "none" for rows no brand owns, or "" for
	// all; a row's brand is its sequence's.
	Brand  string
	Limit  int
	Before *time.Time
}

// ListDeals returns deals, newest first.
func ListDeals(ctx context.Context, q Querier, ws string, f DealFilter) ([]Deal, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	rows, err := q.Query(ctx, `SELECT `+dealCols+dealFrom+`
		WHERE d.workspace_id = $1
		  AND ($2::timestamptz IS NULL OR d.opened_at >= $2)
		  AND ($3::timestamptz IS NULL OR d.opened_at < $3)
		  AND ($4 = '' OR d.sequence_id::text = $4)
		  AND ($5 = '' OR d.mission_id::text = $5)
		  AND ($6 = '' OR d.state = $6)
		  AND (NOT $7 OR d.attribution <> 'none')
		  AND ($8::timestamptz IS NULL OR d.opened_at < $8)
		  AND ($10 = '' OR (SELECT q.brand_id FROM sequences q WHERE q.id = d.sequence_id)::text IS NOT DISTINCT FROM NULLIF($10, 'none'))
		ORDER BY d.opened_at DESC, d.id LIMIT $9`,
		ws, f.From, f.To, f.SequenceID, f.MissionID, f.State, f.Attributed, f.Before, f.Limit, f.Brand)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Deal{}
	for rows.Next() {
		d, err := scanDeal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDeal returns one deal.
func GetDeal(ctx context.Context, q Querier, ws, id string) (Deal, error) {
	d, err := scanDeal(q.QueryRow(ctx, `SELECT `+dealCols+dealFrom+` WHERE d.workspace_id = $1 AND d.id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// NewDeal is a deal logged by hand, for workspaces without a CRM.
type NewDeal struct {
	Name         string     `json:"name"`
	LeadID       *string    `json:"lead_id"`
	ContactEmail string     `json:"contact_email"`
	SequenceID   *string    `json:"sequence_id"`
	MissionID    *string    `json:"mission_id"`
	MeetingID    *string    `json:"meeting_id"`
	Stage        string     `json:"stage"`
	State        string     `json:"state"`
	AmountCents  *int64     `json:"amount_cents"`
	Currency     string     `json:"currency"`
	OpenedAt     *time.Time `json:"opened_at"`
	ClosedAt     *time.Time `json:"closed_at"`
	ExternalID   *string    `json:"external_id"`
}

func checkDeal(state string, amount *int64, currency *string) error {
	if state != DealOpen && state != DealWon && state != DealLost {
		return invalid("state must be open, won or lost")
	}
	if amount != nil && *amount < 0 {
		return invalid("amount_cents cannot be negative")
	}
	if *currency == "" {
		*currency = "USD"
	}
	*currency = strings.ToUpper(*currency)
	if !currencyRe.MatchString(*currency) {
		return invalid("currency must be a three-letter code such as USD")
	}
	return nil
}

// CreateDeal logs a deal.
func CreateDeal(ctx context.Context, tx pgx.Tx, ws, source, actorID string, n NewDeal) (Deal, error) {
	n.Name = strings.TrimSpace(n.Name)
	if n.Name == "" || len(n.Name) > 300 {
		return Deal{}, invalid("name is required (at most 300 characters)")
	}
	if n.State == "" {
		n.State = DealOpen
	}
	if err := checkDeal(n.State, n.AmountCents, &n.Currency); err != nil {
		return Deal{}, err
	}
	opened := time.Now()
	if n.OpenedAt != nil {
		opened = *n.OpenedAt
	}
	if n.State != DealOpen && n.ClosedAt == nil {
		now := time.Now()
		n.ClosedAt = &now
	}
	m := Match{Attribution: AttrNone}
	var err error
	switch {
	case n.MeetingID != nil:
		mt, err := GetMeeting(ctx, tx, ws, *n.MeetingID)
		if errors.Is(err, ErrNotFound) {
			return Deal{}, invalid("meeting_id is not a meeting in this workspace")
		}
		if err != nil {
			return Deal{}, err
		}
		m = Match{Attribution: mt.Attribution, LeadID: mt.LeadID, SequenceID: mt.SequenceID}
	case n.LeadID != nil:
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM leads WHERE workspace_id = $1 AND id = $2)`, ws, *n.LeadID).Scan(&ok); err != nil {
			return Deal{}, err
		}
		if !ok {
			return Deal{}, invalid("lead_id is not a lead in this workspace")
		}
		m, err = AttributeLead(ctx, tx, ws, *n.LeadID, opened)
	case n.ContactEmail != "":
		m, err = Attribute(ctx, tx, ws, []string{strings.ToLower(strings.TrimSpace(n.ContactEmail))}, opened)
	}
	if err != nil {
		return Deal{}, err
	}
	pinned := false
	if n.SequenceID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sequences WHERE workspace_id = $1 AND id = $2)`, ws, *n.SequenceID).Scan(&ok); err != nil {
			return Deal{}, err
		}
		if !ok {
			return Deal{}, invalid("sequence_id is not a sequence in this workspace")
		}
		m.SequenceID, m.Attribution, pinned = n.SequenceID, AttrManual, true
	}
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO deals (workspace_id, sequence_id, mission_id, lead_id, meeting_id, source, external_id, name, stage,
			state, amount_cents, currency, opened_at, closed_at, attribution, attribution_pinned, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id`,
		ws, m.SequenceID, n.MissionID, m.LeadID, n.MeetingID, source, n.ExternalID, n.Name, strings.TrimSpace(n.Stage),
		n.State, n.AmountCents, n.Currency, opened, n.ClosedAt, m.Attribution, pinned, actor).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return Deal{}, invalid("a deal with this external_id already exists")
		}
		return Deal{}, err
	}
	return GetDeal(ctx, tx, ws, id)
}

// DealPatch changes a deal logged by hand, or re-attributes a synced one.
type DealPatch struct {
	Name         *string    `json:"name"`
	Stage        *string    `json:"stage"`
	State        *string    `json:"state"`
	AmountCents  *int64     `json:"amount_cents"`
	Currency     *string    `json:"currency"`
	ClosedAt     *time.Time `json:"closed_at"`
	SequenceID   *string    `json:"sequence_id"`
	MissionID    *string    `json:"mission_id"`
	Unattributed bool       `json:"unattributed"`
}

// UpdateDeal applies a patch. Fields owned by a CRM (name, stage, state,
// amount) are refused on synced deals: change them in the CRM.
func UpdateDeal(ctx context.Context, tx pgx.Tx, ws, id string, p DealPatch) (Deal, error) {
	cur, err := GetDeal(ctx, tx, ws, id)
	if err != nil {
		return cur, err
	}
	crmOwned := p.Name != nil || p.Stage != nil || p.State != nil || p.AmountCents != nil || p.Currency != nil || p.ClosedAt != nil
	if crmOwned && cur.Source != "manual" && cur.Source != SourceAPI {
		return cur, invalid("this deal is synced from %s; change its name, stage or amount there", cur.Source)
	}
	if p.Name != nil {
		cur.Name = strings.TrimSpace(*p.Name)
	}
	if p.Stage != nil {
		cur.Stage = strings.TrimSpace(*p.Stage)
	}
	if p.State != nil {
		if cur.State == DealOpen && *p.State != DealOpen && p.ClosedAt == nil {
			now := time.Now()
			cur.ClosedAt = &now
		}
		if *p.State == DealOpen {
			cur.ClosedAt = nil
		}
		cur.State = *p.State
	}
	if p.AmountCents != nil {
		cur.AmountCents = p.AmountCents
	}
	if p.Currency != nil {
		cur.Currency = *p.Currency
	}
	if p.ClosedAt != nil {
		cur.ClosedAt = p.ClosedAt
	}
	if err := checkDeal(cur.State, cur.AmountCents, &cur.Currency); err != nil {
		return cur, err
	}
	if p.MissionID != nil {
		cur.MissionID = p.MissionID
		if *p.MissionID == "" {
			cur.MissionID = nil
		}
	}
	if p.Unattributed {
		cur.SequenceID, cur.Attribution, cur.AttributionPinned = nil, AttrNone, true
	}
	if p.SequenceID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sequences WHERE workspace_id = $1 AND id = $2)`, ws, *p.SequenceID).Scan(&ok); err != nil {
			return cur, err
		}
		if !ok {
			return cur, invalid("sequence_id is not a sequence in this workspace")
		}
		cur.SequenceID, cur.Attribution, cur.AttributionPinned = p.SequenceID, AttrManual, true
	}
	_, err = tx.Exec(ctx, `UPDATE deals SET name = $3, stage = $4, state = $5, amount_cents = $6, currency = $7, closed_at = $8,
		mission_id = $9, sequence_id = $10, attribution = $11, attribution_pinned = $12, updated_at = now()
		WHERE workspace_id = $1 AND id = $2`,
		ws, id, cur.Name, cur.Stage, cur.State, cur.AmountCents, cur.Currency, cur.ClosedAt,
		cur.MissionID, cur.SequenceID, cur.Attribution, cur.AttributionPinned)
	if err != nil {
		return cur, err
	}
	return GetDeal(ctx, tx, ws, id)
}

// DeleteDeal removes a deal.
func DeleteDeal(ctx context.Context, tx pgx.Tx, ws, id string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM deals WHERE workspace_id = $1 AND id = $2`, ws, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SyncedDeal is a deal as the CRM reports it.
type SyncedDeal struct {
	ExternalID  string
	Name        string
	Stage       string
	State       string
	AmountCents *int64
	Currency    string
	OpenedAt    time.Time
	ClosedAt    *time.Time
	// Emails of the deal's contacts, matched to leads.
	ContactEmails []string
}

// UpsertSyncedDeal records a CRM deal. Deals with no contact tied to
// outreach are skipped unless already stored.
func UpsertSyncedDeal(ctx context.Context, tx pgx.Tx, ws, source, integrationID string, sd SyncedDeal) (bool, error) {
	if sd.State == "" {
		sd.State = DealOpen
	}
	if err := checkDeal(sd.State, sd.AmountCents, &sd.Currency); err != nil {
		return false, err
	}
	emails := make([]string, 0, len(sd.ContactEmails))
	for _, e := range sd.ContactEmails {
		emails = append(emails, strings.ToLower(strings.TrimSpace(e)))
	}
	m, err := Attribute(ctx, tx, ws, emails, sd.OpenedAt)
	if err != nil {
		return false, err
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT id FROM deals WHERE workspace_id = $1 AND source = $2 AND external_id = $3`,
		ws, source, sd.ExternalID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if m.Attribution == AttrNone && existing == "" {
		return false, nil
	}
	// The meeting that most likely led to the deal: the lead's latest
	// meeting before it opened.
	var meeting *string
	if m.LeadID != nil {
		_ = tx.QueryRow(ctx, `SELECT id FROM meetings WHERE workspace_id = $1 AND lead_id = $2 AND booked_at <= $3 AND status <> 'canceled'
			ORDER BY booked_at DESC LIMIT 1`, ws, *m.LeadID, sd.OpenedAt.Add(time.Hour)).Scan(&meeting)
	}
	if existing == "" {
		_, err = tx.Exec(ctx, `
			INSERT INTO deals (workspace_id, sequence_id, lead_id, meeting_id, source, integration_id, external_id,
				name, stage, state, amount_cents, currency, opened_at, closed_at, attribution)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			ws, m.SequenceID, m.LeadID, meeting, source, ptr(integrationID), sd.ExternalID,
			truncate(sd.Name, 300), truncate(sd.Stage, 200), sd.State, sd.AmountCents, sd.Currency, sd.OpenedAt, sd.ClosedAt, m.Attribution)
		return err == nil, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE deals SET name = $3, stage = $4, state = $5, amount_cents = $6, currency = $7, opened_at = $8, closed_at = $9,
			lead_id = CASE WHEN attribution_pinned THEN lead_id ELSE COALESCE($10, lead_id) END,
			sequence_id = CASE WHEN attribution_pinned THEN sequence_id ELSE COALESCE($11, sequence_id) END,
			meeting_id = COALESCE(meeting_id, $12),
			attribution = CASE WHEN attribution_pinned OR $13 = 'none' THEN attribution ELSE $13 END,
			updated_at = now()
		WHERE workspace_id = $1 AND id = $2`,
		ws, existing, truncate(sd.Name, 300), truncate(sd.Stage, 200), sd.State, sd.AmountCents, sd.Currency, sd.OpenedAt, sd.ClosedAt,
		m.LeadID, m.SequenceID, meeting, m.Attribution)
	return err == nil, err
}
