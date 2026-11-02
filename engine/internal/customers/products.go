package customers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Where a product name in an event comes from (product_aliases.source).
const (
	ProductFromEvent   = "event"   // properties.product_id or properties.product of a product event
	ProductFromBilling = "billing" // a billing connection's own product id
)

// JobLifecycleEvent is the job that hands an event about a product to the
// lifecycle engine (engine/internal/lifecycle).
const JobLifecycleEvent = "lifecycle.event"

// ProductRef is the product an event names, as sent: the product's id or
// key, or a name the brand's product or billing system uses for it.
func ProductRef(props map[string]any) string {
	for _, k := range []string{"product_id", "productId", "product"} {
		if v, ok := props[k].(string); ok {
			if v = strings.TrimSpace(v); v != "" && len(v) <= 200 {
				return v
			}
		}
	}
	return ""
}

// ResolveProduct finds the product ref names in a brand (see ProductRef):
// a product's id or key, then a mapped alias from source. A name with no
// match is held for review (product_aliases, status held) when the brand
// uses products and takes names from source, and drives nothing until
// someone maps it. It returns nil when no product matched.
func ResolveProduct(ctx context.Context, q Querier, ws, brand, ref, source string, at time.Time) (*string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || brand == "" {
		return nil, nil
	}
	value := strings.ToLower(ref)
	var id string
	err := q.QueryRow(ctx, `SELECT id::text FROM products WHERE workspace_id = $1 AND brand_id = $2
		AND (id::text = $3 OR key = $3) LIMIT 1`, ws, brand, value).Scan(&id)
	if err == nil {
		return &id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var product *string
	var status string
	err = q.QueryRow(ctx, `UPDATE product_aliases SET seen_count = seen_count + 1,
			first_seen_at = LEAST(COALESCE(first_seen_at, $4), $4), last_seen_at = GREATEST(COALESCE(last_seen_at, $4), $4), updated_at = now()
		WHERE brand_id = $1 AND source = $2 AND value = $3 RETURNING product_id::text, status`, brand, source, value, at).Scan(&product, &status)
	if err == nil {
		if status == "mapped" {
			return product, nil
		}
		return nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// Hold the name for review, if the brand uses products and takes names
	// from this source.
	_, err = q.Exec(ctx, `INSERT INTO product_aliases (workspace_id, brand_id, source, value, status, seen_count, first_seen_at, last_seen_at)
		SELECT $1, $2, $3, $4, 'held', 1, $5, $5
		WHERE (EXISTS (SELECT 1 FROM products p WHERE p.brand_id = $2) OR EXISTS (SELECT 1 FROM lifecycle_settings l WHERE l.brand_id = $2 AND l.enabled_at IS NOT NULL))
		  AND COALESCE((SELECT $3 = ANY (l.sources) FROM lifecycle_settings l WHERE l.brand_id = $2), true)
		ON CONFLICT (brand_id, source, value) DO NOTHING`, ws, brand, source, value, at)
	return nil, err
}

// ProductEventAdded hands an event about a product to the lifecycle engine
// when its brand has lifecycle stages on. usage says whether it counts as
// using the product: an event the product itself sent, not a billing sync
// or a derived event.
func ProductEventAdded(ctx context.Context, q Querier, ws, brand, eventID string, usage bool) error {
	var on bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lifecycle_settings WHERE brand_id = $1 AND workspace_id = $2 AND enabled_at IS NOT NULL)`,
		brand, ws).Scan(&on); err != nil || !on {
		return err
	}
	payload := map[string]any{"event_id": eventID, "usage": usage}
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobLifecycleEvent, WorkspaceID: &ws, Payload: payload, IdempotencyKey: "lifecycle-event:" + eventID})
	return err
}
