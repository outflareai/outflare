package api

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/recmetrics"
)

// Products from events (docs/product-lifecycle.md, "Products from events").
// A server event that names a product and its category with
// properties.product_category describes the product itself, so the brand
// needs no setup: a product it doesn't know yet is added with that
// category, and a product whose category changes follows the events. An
// event without a category keeps the old way: the name is held for review.
// Turning off "event" in the brand's product sources (PATCH .../lifecycle)
// turns this off too, and a name someone ignored is never added.

// eventProductCategory is the category an event gives its product, or ""
// when it gives none or one that isn't a valid category.
func eventProductCategory(props map[string]any) string {
	for _, k := range []string{"product_category", "productCategory"} {
		if v, ok := props[k].(string); ok {
			v = strings.ToLower(strings.TrimSpace(v))
			if recmetrics.CategoryRE.MatchString(v) {
				return v
			}
		}
	}
	return ""
}

// productFromEvent resolves the product ref names in brand, adding it or
// updating its category from the event when category is set.
func productFromEvent(ctx context.Context, tx pgx.Tx, a *access, brand, ref, category string, at time.Time) (*string, error) {
	product, err := customers.ResolveProduct(ctx, tx, a.WorkspaceID, brand, ref, customers.ProductFromEvent, at)
	if err != nil || category == "" {
		return product, err
	}
	var on bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT 'event' = ANY (sources) FROM lifecycle_settings WHERE brand_id = $1), true)`,
		brand).Scan(&on); err != nil || !on {
		return product, err
	}
	if product != nil {
		return product, syncProductCategory(ctx, tx, a, brand, *product, category)
	}
	key := strings.ToLower(strings.TrimSpace(ref))
	if !productKeyRE.MatchString(key) {
		return nil, nil // not a usable key: stays held for review
	}
	var ignored bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM product_aliases WHERE brand_id = $1 AND source = $2 AND value = $3 AND status = 'ignored')`,
		brand, customers.ProductFromEvent, key).Scan(&ignored); err != nil || ignored {
		return nil, err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO products (workspace_id, brand_id, key, name, churn_mode, at_risk_days, churn_days, category)
		VALUES ($1, $2, $3, $4, 'inactivity', 14, 30, $5) ON CONFLICT (brand_id, key) DO NOTHING RETURNING id::text`,
		a.WorkspaceID, brand, key, productNameFromKey(key), category).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// Added meanwhile by another request.
		return customers.ResolveProduct(ctx, tx, a.WorkspaceID, brand, ref, customers.ProductFromEvent, at)
	}
	if err != nil {
		return nil, err
	}
	// The name is a product now, not something to review.
	if _, err := tx.Exec(ctx, `DELETE FROM product_aliases WHERE brand_id = $1 AND source = $2 AND value = $3 AND status = 'held'`,
		brand, customers.ProductFromEvent, key); err != nil {
		return nil, err
	}
	data := map[string]any{"brand_id": brand, "has_category": true, "churn_mode": "inactivity", "from_event": true}
	if err := record(ctx, tx, &a.principal, a.WorkspaceID, "product.created", "product", id, data); err != nil {
		return nil, err
	}
	return &id, nil
}

// syncProductCategory moves a product to the category its events give.
func syncProductCategory(ctx context.Context, tx pgx.Tx, a *access, brand, product, category string) error {
	tag, err := tx.Exec(ctx, `UPDATE products SET category = $3, updated_at = now()
		WHERE id = $1 AND brand_id = $2 AND category IS DISTINCT FROM $3`, product, brand, category)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return record(ctx, tx, &a.principal, a.WorkspaceID, "product.updated", "product", product,
		map[string]any{"brand_id": brand, "has_category": true, "from_event": true})
}

// productNameFromKey makes a readable name from a key such as
// residential_proxies: "Residential proxies". People can rename it.
func productNameFromKey(key string) string {
	name := strings.Join(strings.FieldsFunc(key, func(r rune) bool { return r == '_' || r == '-' || r == '.' }), " ")
	if name == "" {
		return key
	}
	r := []rune(name)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
