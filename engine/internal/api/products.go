package api

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/lifecycle"
)

// Products a brand sells, and the names its events and billing system use
// for them (product_aliases). Events say which product they are about, so
// lifecycle stages can be kept per product (docs/product-lifecycle.md).

func (s *Server) routesProducts(mux *http.ServeMux) {
	read, manage, catalog := auth.ScopeCustomersRead, auth.ScopeCustomersManage, auth.ScopeCatalogManage
	base := "/v1/brands/{brand}/products"
	mux.HandleFunc("GET "+base, s.onBrand(read, s.listProducts))
	mux.HandleFunc("POST "+base, s.onBrand(catalog, s.createProduct))
	mux.HandleFunc("GET "+base+"/{product}", s.onBrand(read, s.getProduct))
	mux.HandleFunc("PATCH "+base+"/{product}", s.onBrand(catalog, s.updateProduct))
	mux.HandleFunc("POST "+base+"/{product}/archive", s.onBrand(catalog, s.archiveProduct))
	mux.HandleFunc("POST "+base+"/{product}/restore", s.onBrand(catalog, s.restoreProduct))
	names := "/v1/brands/{brand}/product-names"
	mux.HandleFunc("GET "+names, s.onBrand(read, s.listProductNames))
	mux.HandleFunc("POST "+names, s.onBrand(manage, s.addProductName))
	mux.HandleFunc("PATCH "+names+"/{name}", s.onBrand(manage, s.updateProductName))
}

var productKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

type productView struct {
	ID         string     `json:"id"`
	BrandID    string     `json:"brand_id"`
	Key        string     `json:"key"`
	Name       string     `json:"name"`
	ChurnMode  string     `json:"churn_mode"`
	AtRiskDays int        `json:"at_risk_days"`
	ChurnDays  int        `json:"churn_days"`
	Aliases    []string   `json:"aliases"`
	ArchivedAt *time.Time `json:"archived_at"`
	Version    int        `json:"version"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

const productColumns = `p.id::text, p.brand_id::text, p.key, p.name, p.churn_mode, p.at_risk_days, p.churn_days,
	COALESCE((SELECT array_agg(a.source || ':' || a.value ORDER BY a.source, a.value) FROM product_aliases a WHERE a.product_id = p.id), '{}'),
	p.archived_at, p.version, p.created_at, p.updated_at`

func scanProduct(row pgx.CollectableRow) (productView, error) {
	var v productView
	err := row.Scan(&v.ID, &v.BrandID, &v.Key, &v.Name, &v.ChurnMode, &v.AtRiskDays, &v.ChurnDays, &v.Aliases, &v.ArchivedAt, &v.Version, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (s *Server) listProducts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	archived := r.URL.Query().Get("archived") == "true"
	var list []productView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT `+productColumns+` FROM products p WHERE p.workspace_id = $1 AND p.brand_id = $2
			AND (p.archived_at IS NOT NULL) = $3 ORDER BY lower(p.name), p.id`, a.WorkspaceID, a.Brand, archived)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanProduct)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list)})
}

func findProduct(r *http.Request, tx pgx.Tx, a *access, id string) (productView, error) {
	if !validUUID(id) {
		return productView{}, errNotFound
	}
	rows, err := tx.Query(r.Context(), `SELECT `+productColumns+` FROM products p WHERE p.id = $1 AND p.workspace_id = $2 AND p.brand_id = $3`,
		id, a.WorkspaceID, a.Brand)
	if err != nil {
		return productView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanProduct)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) getProduct(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var v productView
	err := s.readTx(r.Context(), func(tx pgx.Tx) (err error) {
		v, err = findProduct(r, tx, a, r.PathValue("product"))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type productFields struct {
	Key        *string `json:"key"`
	Name       *string `json:"name"`
	ChurnMode  *string `json:"churn_mode"`
	AtRiskDays *int    `json:"at_risk_days"`
	ChurnDays  *int    `json:"churn_days"`
	// Names the brand's events (event:residential) or billing system
	// (billing:prod_123) use for it; a bare name is an event name.
	Aliases *[]string `json:"aliases"`
	Version *int      `json:"version"`
}

// check validates fields and returns the aliases as (source, value) pairs.
func (f productFields) check() ([][2]string, error) {
	if f.Key != nil && !productKeyRE.MatchString(*f.Key) {
		return nil, errInvalid("key is lowercase letters, digits, dots, dashes and underscores, such as residential")
	}
	if f.Name != nil && (strings.TrimSpace(*f.Name) == "" || len(*f.Name) > 200) {
		return nil, errInvalid("name is 1 to 200 characters")
	}
	if f.ChurnMode != nil && *f.ChurnMode != "cancellation" && *f.ChurnMode != "inactivity" {
		return nil, errInvalid("churn_mode is cancellation or inactivity")
	}
	if f.AtRiskDays != nil && (*f.AtRiskDays < 1 || *f.AtRiskDays > 365) {
		return nil, errInvalid("at_risk_days is 1 to 365")
	}
	if f.ChurnDays != nil && (*f.ChurnDays < 1 || *f.ChurnDays > 730) {
		return nil, errInvalid("churn_days is 1 to 730")
	}
	if f.Aliases == nil {
		return nil, nil
	}
	if len(*f.Aliases) > 100 {
		return nil, errInvalid("a product has at most 100 aliases")
	}
	var out [][2]string
	for _, raw := range *f.Aliases {
		source, value := "event", raw
		if i := strings.Index(raw, ":"); i > 0 && (raw[:i] == "event" || raw[:i] == "billing") {
			source, value = raw[:i], raw[i+1:]
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || len(value) > 200 {
			return nil, errInvalid("an alias is 1 to 200 characters, optionally after event: or billing:")
		}
		p := [2]string{source, value}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out, nil
}

// setAliases makes aliases the product's own: names held or ignored become
// mapped to it, and its other aliases go. A name another product of the
// brand holds is a conflict.
func setAliases(r *http.Request, tx pgx.Tx, a *access, product string, aliases [][2]string) error {
	ctx := r.Context()
	if _, err := tx.Exec(ctx, `DELETE FROM product_aliases WHERE product_id = $1`, product); err != nil {
		return err
	}
	for _, al := range aliases {
		var other *string
		err := tx.QueryRow(ctx, `INSERT INTO product_aliases (workspace_id, brand_id, product_id, source, value, status)
			VALUES ($1, $2, $3, $4, $5, 'mapped')
			ON CONFLICT (brand_id, source, value) DO UPDATE SET product_id = EXCLUDED.product_id, status = 'mapped', updated_at = now()
				WHERE product_aliases.product_id IS NULL
			RETURNING NULL::text`, a.WorkspaceID, a.Brand, product, al[0], al[1]).Scan(&other)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflictMsg("the alias " + al[0] + ":" + al[1] + " belongs to another product")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// errConflictMsg is a 409 with a message.
type errConflictMsg string

func (e errConflictMsg) Error() string { return string(e) }

// productError writes err, with a conflict for a duplicate key or alias.
func (s *Server) productError(w http.ResponseWriter, r *http.Request, err error) bool {
	var c errConflictMsg
	switch {
	case err == nil:
		return false
	case errors.As(err, &c):
		writeError(w, r, http.StatusConflict, "conflict", string(c))
	case uniqueViolation(err):
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has a product with this key")
	default:
		return s.allocError(w, r, err)
	}
	return true
}

func productData(v productView) map[string]any {
	return map[string]any{"churn_mode": v.ChurnMode, "at_risk_days": v.AtRiskDays, "churn_days": v.ChurnDays, "aliases": len(v.Aliases)}
}

func (s *Server) createProduct(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f productFields
	if !decode(w, r, &f) {
		return
	}
	if f.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if f.Key == nil {
		k := productSlug(*f.Name)
		f.Key = &k
	}
	aliases, err := f.check()
	if s.productError(w, r, err) {
		return
	}
	ctx := r.Context()
	var v productView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO products (workspace_id, brand_id, key, name, churn_mode, at_risk_days, churn_days, created_by)
			VALUES ($1, $2, $3, $4, COALESCE($5, 'cancellation'), COALESCE($6, 14), COALESCE($7, 30), $8) RETURNING id::text`,
			a.WorkspaceID, a.Brand, *f.Key, strings.TrimSpace(*f.Name), f.ChurnMode, f.AtRiskDays, f.ChurnDays, createdBy).Scan(&id); err != nil {
			return err
		}
		if err := setAliases(r, tx, a, id, aliases); err != nil {
			return err
		}
		var err error
		if v, err = findProduct(r, tx, a, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "product.created", "product", id, productData(v))
	})
	if s.productError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// productSlug makes a key from a name: Residential Proxies → residential-proxies.
func productSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			b.WriteRune(c)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	k := strings.TrimRight(b.String(), "-")
	if len(k) > 63 {
		k = strings.TrimRight(k[:63], "-")
	}
	if k == "" {
		k = "product"
	}
	return k
}

func (s *Server) updateProduct(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f productFields
	if !decode(w, r, &f) {
		return
	}
	if f.Key != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a product's key never changes, so rules and events keep working; rename it with name")
		return
	}
	aliases, err := f.check()
	if s.productError(w, r, err) {
		return
	}
	ctx := r.Context()
	var v productView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findProduct(r, tx, a, r.PathValue("product"))
		if err != nil {
			return err
		}
		if f.Version != nil && *f.Version != cur.Version {
			return errConflictMsg("the product changed since you read it (version " + strconv.Itoa(cur.Version) + "); read it again")
		}
		if _, err := tx.Exec(ctx, `UPDATE products SET name = COALESCE($2, name), churn_mode = COALESCE($3, churn_mode),
				at_risk_days = COALESCE($4, at_risk_days), churn_days = COALESCE($5, churn_days), version = version + 1, updated_at = now()
			WHERE id = $1`, cur.ID, trimmed(f.Name), f.ChurnMode, f.AtRiskDays, f.ChurnDays); err != nil {
			return err
		}
		if f.Aliases != nil {
			if err := setAliases(r, tx, a, cur.ID, aliases); err != nil {
				return err
			}
		}
		// Changed days or churn mode move inactivity clocks.
		if f.ChurnMode != nil || f.AtRiskDays != nil || f.ChurnDays != nil {
			if err := lifecycle.Recheck(ctx, tx, a.Brand, cur.ID); err != nil {
				return err
			}
		}
		if v, err = findProduct(r, tx, a, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "product.updated", "product", cur.ID, productData(v))
	})
	if s.productError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) archiveProduct(w http.ResponseWriter, r *http.Request) {
	s.setProductArchived(w, r, true)
}
func (s *Server) restoreProduct(w http.ResponseWriter, r *http.Request) {
	s.setProductArchived(w, r, false)
}

// setProductArchived archives or restores a product. An archived product
// keeps its id, key and history; its events no longer move anyone, and it
// leaves companies' overall status.
func (s *Server) setProductArchived(w http.ResponseWriter, r *http.Request, archive bool) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var v productView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findProduct(r, tx, a, r.PathValue("product"))
		if err != nil {
			return err
		}
		if (cur.ArchivedAt != nil) == archive {
			v = cur
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE products SET archived_at = CASE WHEN $2 THEN now() END, version = version + 1, updated_at = now() WHERE id = $1`,
			cur.ID, archive); err != nil {
			return err
		}
		if v, err = findProduct(r, tx, a, cur.ID); err != nil {
			return err
		}
		action := "product.restored"
		if archive {
			action = "product.archived"
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "product", cur.ID, productData(v))
	})
	if s.productError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// Product names: what the brand's events and billing system call its
// products. A held name was seen and waits for someone to map it to a
// product or ignore it.

type productNameView struct {
	ID          string     `json:"id"`
	Source      string     `json:"source"`
	Value       string     `json:"value"`
	Status      string     `json:"status"`
	ProductID   *string    `json:"product_id"`
	SeenCount   int64      `json:"seen_count"`
	FirstSeenAt *time.Time `json:"first_seen_at"`
	LastSeenAt  *time.Time `json:"last_seen_at"`
}

const productNameColumns = `id::text, source, value, status, product_id::text, seen_count, first_seen_at, last_seen_at`

func scanProductName(row pgx.CollectableRow) (productNameView, error) {
	var v productNameView
	err := row.Scan(&v.ID, &v.Source, &v.Value, &v.Status, &v.ProductID, &v.SeenCount, &v.FirstSeenAt, &v.LastSeenAt)
	return v, err
}

func (s *Server) listProductNames(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && status != "held" && status != "mapped" && status != "ignored" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is held, mapped or ignored")
		return
	}
	var list []productNameView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT `+productNameColumns+` FROM product_aliases WHERE workspace_id = $1 AND brand_id = $2
			AND ($3 = '' OR status = $3) ORDER BY last_seen_at DESC NULLS LAST, value LIMIT 500`, a.WorkspaceID, a.Brand, status)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanProductName)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list)})
}

type productNameFields struct {
	Source    *string `json:"source"`
	Value     *string `json:"value"`
	Status    *string `json:"status"`
	ProductID *string `json:"product_id"`
}

// checkProductName validates a status and product: a mapped name names a
// product of the brand, and only a mapped one does.
func checkProductName(r *http.Request, tx pgx.Tx, a *access, status string, product *string) error {
	switch status {
	case "mapped":
		if product == nil {
			return errInvalid("a mapped name needs product_id")
		}
		if _, err := findProduct(r, tx, a, *product); errors.Is(err, errNotFound) {
			return errInvalid("product_id is not a product of this brand")
		} else if err != nil {
			return err
		}
	case "held", "ignored":
		if product != nil {
			return errInvalid("only a mapped name has product_id")
		}
	default:
		return errInvalid("status is held, mapped or ignored")
	}
	return nil
}

// addProductName maps or ignores a name the brand's events or billing
// system use, before it is ever seen.
func (s *Server) addProductName(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f productNameFields
	if !decode(w, r, &f) {
		return
	}
	if f.Value == nil || strings.TrimSpace(*f.Value) == "" || len(*f.Value) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "value is 1 to 200 characters")
		return
	}
	source := "event"
	if f.Source != nil {
		source = *f.Source
	}
	if source != "event" && source != "billing" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is event or billing")
		return
	}
	status := "mapped"
	if f.Status != nil {
		status = *f.Status
	}
	ctx := r.Context()
	var v productNameView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := checkProductName(r, tx, a, status, f.ProductID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `INSERT INTO product_aliases (workspace_id, brand_id, product_id, source, value, status)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+productNameColumns,
			a.WorkspaceID, a.Brand, f.ProductID, source, strings.ToLower(strings.TrimSpace(*f.Value)), status)
		if err != nil {
			return err
		}
		if v, err = pgx.CollectExactlyOneRow(rows, scanProductName); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "product_name.added", "brand", a.Brand,
			map[string]any{"name_id": v.ID, "source": source, "status": status, "product_id": v.ProductID})
	})
	if uniqueViolation(err) {
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has this name; change it instead")
		return
	}
	if s.productError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// updateProductName maps a held name to a product, ignores it, or puts it
// back on hold. Events already stored keep the product they had; new ones
// use the change.
func (s *Server) updateProductName(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f productNameFields
	if !decode(w, r, &f) {
		return
	}
	if f.Status == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is required: held, mapped or ignored")
		return
	}
	if f.Source != nil || f.Value != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a name's source and value never change; add another name instead")
		return
	}
	id := r.PathValue("name")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
		return
	}
	ctx := r.Context()
	var v productNameView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := checkProductName(r, tx, a, *f.Status, f.ProductID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE product_aliases SET status = $4, product_id = $5, updated_at = now()
			WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 RETURNING `+productNameColumns, id, a.WorkspaceID, a.Brand, *f.Status, f.ProductID)
		if err != nil {
			return err
		}
		v, err = pgx.CollectExactlyOneRow(rows, scanProductName)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "product_name.updated", "brand", a.Brand,
			map[string]any{"name_id": v.ID, "source": v.Source, "status": v.Status, "product_id": v.ProductID})
	})
	if s.productError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}
