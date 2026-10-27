package outcomes

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SpendFilter narrows ListSpend.
type SpendFilter struct {
	From, To   *time.Time
	ProjectID  string
	SequenceID string
	MissionID  string
	Category   string
	Provider   string
	Limit      int
	Before     *time.Time
}

// ListSpend returns ledger entries, newest first.
func ListSpend(ctx context.Context, q Querier, ws string, f SpendFilter) ([]Entry, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	rows, err := q.Query(ctx, `SELECT `+entryCols+` FROM spend_entries
		WHERE workspace_id = $1
		  AND ($2::timestamptz IS NULL OR occurred_at >= $2)
		  AND ($3::timestamptz IS NULL OR occurred_at < $3)
		  AND ($4 = '' OR project_id::text = $4)
		  AND ($5 = '' OR sequence_id::text = $5)
		  AND ($6 = '' OR mission_id::text = $6)
		  AND ($7 = '' OR category = $7)
		  AND ($8 = '' OR lower(provider) = lower($8))
		  AND ($9::timestamptz IS NULL OR occurred_at < $9)
		ORDER BY occurred_at DESC, id LIMIT $10`,
		ws, f.From, f.To, f.ProjectID, f.SequenceID, f.MissionID, f.Category, f.Provider, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows, ws)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetSpend returns one entry.
func GetSpend(ctx context.Context, q Querier, ws, id string) (Entry, error) {
	e, err := scanEntry(q.QueryRow(ctx, `SELECT `+entryCols+` FROM spend_entries WHERE workspace_id = $1 AND id = $2`, ws, id), ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// DeleteSpend removes an entry, e.g. one typed in by mistake.
func DeleteSpend(ctx context.Context, tx pgx.Tx, ws, id string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM spend_entries WHERE workspace_id = $1 AND id = $2`, ws, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Recurring is a cost that repeats every month or year.
type Recurring struct {
	ID          string    `json:"id"`
	ProjectID   *string   `json:"project_id"`
	SequenceID  *string   `json:"sequence_id"`
	Category    string    `json:"category"`
	Provider    string    `json:"provider"`
	Name        string    `json:"name"`
	AmountCents int64     `json:"amount_cents"`
	Currency    string    `json:"currency"`
	Every       string    `json:"every"`
	StartsOn    Date      `json:"starts_on"`
	EndsOn      *Date     `json:"ends_on"`
	Note        string    `json:"note"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Per day, for display: what the cost adds to a report for each day.
	DailyCents float64 `json:"daily_cents"`
}

// Date is a calendar date, YYYY-MM-DD in JSON.
type Date struct{ time.Time }

func (d Date) MarshalJSON() ([]byte, error) { return []byte(`"` + d.Format(time.DateOnly) + `"`), nil }

func (d *Date) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return invalid("dates look like 2026-10-31")
	}
	d.Time = t
	return nil
}

// Daily is what the cost adds per active day.
func (r Recurring) Daily() float64 {
	if r.Every == "year" {
		return float64(r.AmountCents) / 365
	}
	return float64(r.AmountCents) * 12 / 365
}

const recurringCols = `id, project_id, sequence_id, category, provider, name, amount_cents, currency, every, starts_on, ends_on, note, created_at, updated_at`

func scanRecurring(row pgx.Row) (Recurring, error) {
	var r Recurring
	var ends *time.Time
	err := row.Scan(&r.ID, &r.ProjectID, &r.SequenceID, &r.Category, &r.Provider, &r.Name, &r.AmountCents, &r.Currency, &r.Every,
		&r.StartsOn.Time, &ends, &r.Note, &r.CreatedAt, &r.UpdatedAt)
	if ends != nil {
		r.EndsOn = &Date{*ends}
	}
	r.DailyCents = r.Daily()
	return r, err
}

// ListRecurring returns every recurring cost, active first.
func ListRecurring(ctx context.Context, q Querier, ws string) ([]Recurring, error) {
	rows, err := q.Query(ctx, `SELECT `+recurringCols+` FROM spend_recurring WHERE workspace_id = $1
		ORDER BY (ends_on IS NOT NULL AND ends_on < current_date), lower(name)`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recurring{}
	for rows.Next() {
		r, err := scanRecurring(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (r *Recurring) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 200 {
		return invalid("name is required (at most 200 characters)")
	}
	if !validCategory(r.Category) {
		return invalid("category must be one of %s", strings.Join(Categories, ", "))
	}
	if r.AmountCents < 0 {
		return invalid("amount_cents cannot be negative")
	}
	if r.Every == "" {
		r.Every = "month"
	}
	if r.Every != "month" && r.Every != "year" {
		return invalid("every must be month or year")
	}
	if r.Currency == "" {
		r.Currency = "USD"
	}
	r.Currency = strings.ToUpper(r.Currency)
	if !currencyRe.MatchString(r.Currency) {
		return invalid("currency must be a three-letter code such as USD")
	}
	if r.StartsOn.IsZero() {
		r.StartsOn = Date{time.Now().UTC().Truncate(24 * time.Hour)}
	}
	if r.EndsOn != nil && r.EndsOn.Before(r.StartsOn.Time) {
		return invalid("ends_on is before starts_on")
	}
	r.Provider, r.Note = strings.TrimSpace(r.Provider), strings.TrimSpace(r.Note)
	return nil
}

func checkRefs(ctx context.Context, q Querier, ws string, project, sequence *string) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT ($2::uuid IS NULL OR EXISTS (SELECT 1 FROM projects WHERE workspace_id = $1 AND id = $2))
		AND ($3::uuid IS NULL OR EXISTS (SELECT 1 FROM sequences WHERE workspace_id = $1 AND id = $3))`, ws, project, sequence).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return invalid("the project or sequence is not in this workspace")
	}
	return nil
}

// CreateRecurring adds a recurring cost.
func CreateRecurring(ctx context.Context, tx pgx.Tx, ws, actorID string, r Recurring) (Recurring, error) {
	if err := r.normalize(); err != nil {
		return r, err
	}
	if err := checkRefs(ctx, tx, ws, r.ProjectID, r.SequenceID); err != nil {
		return r, err
	}
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	var ends *time.Time
	if r.EndsOn != nil {
		ends = &r.EndsOn.Time
	}
	return scanRecurring(tx.QueryRow(ctx, `
		INSERT INTO spend_recurring (workspace_id, project_id, sequence_id, category, provider, name, amount_cents, currency, every,
			starts_on, ends_on, note, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING `+recurringCols,
		ws, r.ProjectID, r.SequenceID, r.Category, r.Provider, r.Name, r.AmountCents, r.Currency, r.Every, r.StartsOn.Time, ends, r.Note, actor))
}

// UpdateRecurring replaces a recurring cost's fields. To stop a cost,
// set ends_on rather than deleting it, so past reports keep it.
func UpdateRecurring(ctx context.Context, tx pgx.Tx, ws, id string, r Recurring) (Recurring, error) {
	if err := r.normalize(); err != nil {
		return r, err
	}
	if err := checkRefs(ctx, tx, ws, r.ProjectID, r.SequenceID); err != nil {
		return r, err
	}
	var ends *time.Time
	if r.EndsOn != nil {
		ends = &r.EndsOn.Time
	}
	out, err := scanRecurring(tx.QueryRow(ctx, `
		UPDATE spend_recurring SET project_id = $3, sequence_id = $4, category = $5, provider = $6, name = $7, amount_cents = $8,
			currency = $9, every = $10, starts_on = $11, ends_on = $12, note = $13, updated_at = now()
		WHERE workspace_id = $1 AND id = $2 RETURNING `+recurringCols,
		ws, id, r.ProjectID, r.SequenceID, r.Category, r.Provider, r.Name, r.AmountCents, r.Currency, r.Every, r.StartsOn.Time, ends, r.Note))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

// GetRecurring returns one recurring cost.
func GetRecurring(ctx context.Context, q Querier, ws, id string) (Recurring, error) {
	r, err := scanRecurring(q.QueryRow(ctx, `SELECT `+recurringCols+` FROM spend_recurring WHERE workspace_id = $1 AND id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// DeleteRecurring removes a recurring cost from every report, past ones
// included.
func DeleteRecurring(ctx context.Context, tx pgx.Tx, ws, id string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM spend_recurring WHERE workspace_id = $1 AND id = $2`, ws, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Rate prices a unit, for entries that only know units.
type Rate struct {
	Provider        string    `json:"provider"`
	UnitName        string    `json:"unit_name"`
	UnitPriceMicros int64     `json:"unit_price_micros"`
	Currency        string    `json:"currency"`
	UpdatedAt       time.Time `json:"updated_at"`
	// Units recorded with this provider and unit and no amount, all time.
	UnpricedUnits float64 `json:"unpriced_units"`
}

// ListRates returns the workspace's rates, plus a row with a zero price
// for every provider and unit that appears in the ledger without one, so
// people can see what still needs a price.
func ListRates(ctx context.Context, q Querier, ws string) ([]Rate, error) {
	rows, err := q.Query(ctx, `
		WITH used AS (
			SELECT lower(provider) AS provider, unit_name, sum(units)::float8 AS units FROM spend_entries
			WHERE workspace_id = $1 AND amount_cents IS NULL AND units IS NOT NULL GROUP BY 1, 2)
		SELECT COALESCE(r.provider, u.provider), COALESCE(r.unit_name, u.unit_name), COALESCE(r.unit_price_micros, 0),
			COALESCE(r.currency, 'USD'), COALESCE(r.updated_at, 'epoch'::timestamptz), COALESCE(u.units, 0)
		FROM (SELECT * FROM spend_rates WHERE workspace_id = $1) r
		FULL JOIN used u ON u.provider = lower(r.provider) AND u.unit_name = r.unit_name
		ORDER BY 1, 2`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rate{}
	for rows.Next() {
		var r Rate
		if err := rows.Scan(&r.Provider, &r.UnitName, &r.UnitPriceMicros, &r.Currency, &r.UpdatedAt, &r.UnpricedUnits); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRate sets what one unit costs. Providers are matched without case.
func SetRate(ctx context.Context, tx pgx.Tx, ws string, r Rate) (Rate, error) {
	r.Provider, r.UnitName = strings.ToLower(strings.TrimSpace(r.Provider)), strings.TrimSpace(r.UnitName)
	if r.Provider == "" || r.UnitName == "" {
		return r, invalid("provider and unit_name are required")
	}
	if r.UnitPriceMicros < 0 {
		return r, invalid("unit_price_micros cannot be negative")
	}
	if r.Currency == "" {
		r.Currency = "USD"
	}
	r.Currency = strings.ToUpper(r.Currency)
	if !currencyRe.MatchString(r.Currency) {
		return r, invalid("currency must be a three-letter code such as USD")
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO spend_rates (workspace_id, provider, unit_name, unit_price_micros, currency) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (workspace_id, provider, unit_name) DO UPDATE SET unit_price_micros = $4, currency = $5, updated_at = now()
		RETURNING updated_at`, ws, r.Provider, r.UnitName, r.UnitPriceMicros, r.Currency).Scan(&r.UpdatedAt)
	return r, err
}

// DeleteRate removes a rate.
func DeleteRate(ctx context.Context, tx pgx.Tx, ws, provider, unit string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM spend_rates WHERE workspace_id = $1 AND provider = lower($2) AND unit_name = $3`, ws, provider, unit)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
