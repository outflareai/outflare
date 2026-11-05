package conversions

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier runs queries, in or out of a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ErrNotFound is a key the workspace has no definition for.
var ErrNotFound = errors.New("no conversion definition with that key")

// ErrExists is a key the workspace already uses.
var ErrExists = errors.New("a conversion definition with that key exists")

const cols = `id::text, coalesce(brand_id::text, ''), key, name, event_name, filters, value_mode, fixed_value_minor, value_property, value_unit, currency,
	network_events, attribution, archived_at, created_at, updated_at`

func scan(row pgx.Row) (Definition, error) {
	var d Definition
	var filters, networks []byte
	err := row.Scan(&d.ID, &d.BrandID, &d.Key, &d.Name, &d.Event, &filters, &d.ValueMode, &d.FixedValueMinor, &d.ValueProperty, &d.ValueUnit,
		&d.Currency, &networks, &d.Attribution, &d.ArchivedAt, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(filters, &d.Filters); err != nil {
		return d, err
	}
	return d, json.Unmarshal(networks, &d.NetworkEvents)
}

// List returns a brand's definitions by key (brand "": every brand's),
// archived ones too when archived is true.
func List(ctx context.Context, q Querier, ws, brand string, archived bool) ([]Definition, error) {
	rows, err := q.Query(ctx, `SELECT `+cols+` FROM conversion_definitions d WHERE workspace_id = $1 AND ($2 OR archived_at IS NULL)
		AND ($3 = '' OR brand_id = $3::uuid) ORDER BY key`, ws, archived, brand)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Definition{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// inBrand keeps the definitions with key $2 of the brand in $3: a brand
// id, or "" for whichever brand uses the key, the workspace's first brand
// before the others.
const inBrand = `brand_id IS NOT DISTINCT FROM CASE WHEN $3 = '' THEN (SELECT x.brand_id FROM conversion_definitions x
	WHERE x.workspace_id = $1 AND x.key = $2 ORDER BY x.brand_id IS NOT DISTINCT FROM outflare_first_brand($1) DESC, x.created_at LIMIT 1) ELSE $3::uuid END`

// Get returns one of a brand's definitions by key (brand "": see inBrand).
func Get(ctx context.Context, q Querier, ws, brand, key string) (Definition, error) {
	d, err := scan(q.QueryRow(ctx, `SELECT `+cols+` FROM conversion_definitions WHERE workspace_id = $1 AND key = $2 AND `+inBrand, ws, key, brand))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// GetByID returns one definition by id.
func GetByID(ctx context.Context, q Querier, ws, id string) (Definition, error) {
	d, err := scan(q.QueryRow(ctx, `SELECT `+cols+` FROM conversion_definitions WHERE workspace_id = $1 AND id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// Create stores a new definition, which must be valid, in d.BrandID's
// brand ("": the workspace's first).
func Create(ctx context.Context, q Querier, ws string, d Definition, actor *string) (Definition, error) {
	filters, _ := json.Marshal(d.Filters)
	networks, _ := json.Marshal(d.NetworkEvents)
	out, err := scan(q.QueryRow(ctx, `INSERT INTO conversion_definitions (workspace_id, key, name, event_name, filters, value_mode, fixed_value_minor,
			value_property, value_unit, currency, network_events, attribution, created_by, brand_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NULLIF($14, '')::uuid) RETURNING `+cols,
		ws, d.Key, d.Name, d.Event, filters, d.ValueMode, d.FixedValueMinor, d.ValueProperty, d.ValueUnit, d.Currency, networks, d.Attribution, actor, d.BrandID))
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return out, ErrExists
	}
	return out, err
}

// CreateIfMissing stores a new definition unless its brand already uses
// its key, and reports whether it did. Unlike Create it never fails a
// transaction on a key that is taken.
func CreateIfMissing(ctx context.Context, q Querier, ws string, d Definition, actor *string) (Definition, bool, error) {
	filters, _ := json.Marshal(d.Filters)
	networks, _ := json.Marshal(d.NetworkEvents)
	out, err := scan(q.QueryRow(ctx, `INSERT INTO conversion_definitions (workspace_id, key, name, event_name, filters, value_mode, fixed_value_minor,
			value_property, value_unit, currency, network_events, attribution, created_by, brand_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NULLIF($14, '')::uuid) ON CONFLICT DO NOTHING RETURNING `+cols,
		ws, d.Key, d.Name, d.Event, filters, d.ValueMode, d.FixedValueMinor, d.ValueProperty, d.ValueUnit, d.Currency, networks, d.Attribution, actor, d.BrandID))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	return out, err == nil, err
}

// Update replaces the fields of definition d.ID (not its key or brand),
// which must be valid.
func Update(ctx context.Context, q Querier, ws string, d Definition) (Definition, error) {
	filters, _ := json.Marshal(d.Filters)
	networks, _ := json.Marshal(d.NetworkEvents)
	out, err := scan(q.QueryRow(ctx, `UPDATE conversion_definitions SET name = $3, event_name = $4, filters = $5, value_mode = $6, fixed_value_minor = $7,
			value_property = $8, value_unit = $9, currency = $10, network_events = $11, attribution = $12, updated_at = now()
		WHERE workspace_id = $1 AND id = $2 RETURNING `+cols,
		ws, d.ID, d.Name, d.Event, filters, d.ValueMode, d.FixedValueMinor, d.ValueProperty, d.ValueUnit, d.Currency, networks, d.Attribution))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

// Archive stops definition id counting; what it already credited stays.
func Archive(ctx context.Context, q Querier, ws, id string) (Definition, error) {
	out, err := scan(q.QueryRow(ctx, `UPDATE conversion_definitions SET archived_at = now(), updated_at = now()
		WHERE workspace_id = $1 AND id = $2 AND archived_at IS NULL RETURNING `+cols, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

// ForAttribution returns the definitions attribution credits a brand's
// conversions with, and the event names they cover.
func ForAttribution(ctx context.Context, q Querier, ws, brand string) ([]Definition, []string, error) {
	all, err := List(ctx, q, ws, brand, false)
	if err != nil {
		return nil, nil, err
	}
	var defs []Definition
	var names []string
	for _, d := range all {
		if !d.Attribution {
			continue
		}
		defs = append(defs, d)
		if !slices.Contains(names, d.Event) {
			names = append(names, d.Event)
		}
	}
	return defs, names, nil
}
