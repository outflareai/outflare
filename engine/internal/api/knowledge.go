package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Knowledge: what a business knows, written by its people for staff and
// for every AI in Outflare. Articles and quick answers (snippets) live in
// folders (collections), each with who may see it (visibility), a brand,
// and every version kept. Deleting moves an item to the bin for
// knowledgeBinDays; housekeeping deletes it for good after that. Reading
// takes workspace.read and changing context.manage, like company context
// and memories, which the web app shows beside it.

const (
	maxKnowledgeItems       = 10000
	maxKnowledgeCollections = 500
	maxKnowledgeDepth       = 5
	maxKnowledgeBody        = 200000
	knowledgeBinDays        = 30
)

var (
	knowledgeKinds        = []string{"article", "snippet"}
	knowledgeVisibilities = []string{"public", "customers_ai", "team"}
	knowledgeStatuses     = []string{"draft", "published"}
)

func (s *Server) routesKnowledge(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeContextManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/knowledge/collections", s.inBrand(read, s.listKnowledgeCollections))
	mux.HandleFunc("POST /v1/workspaces/{ws}/knowledge/collections", s.inBrand(manage, s.createKnowledgeCollection))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/knowledge/collections/{id}", s.inBrand(manage, s.updateKnowledgeCollection))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/knowledge/collections/{id}", s.inBrand(manage, s.deleteKnowledgeCollection))
	mux.HandleFunc("GET /v1/workspaces/{ws}/knowledge/items", s.inBrand(read, s.listKnowledgeItems))
	mux.HandleFunc("POST /v1/workspaces/{ws}/knowledge/items", s.inBrand(manage, s.createKnowledgeItem))
	mux.HandleFunc("GET /v1/workspaces/{ws}/knowledge/items/{id}", s.inBrand(read, s.getKnowledgeItem))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/knowledge/items/{id}", s.inBrand(manage, s.updateKnowledgeItem))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/knowledge/items/{id}", s.inBrand(manage, s.deleteKnowledgeItem))
	mux.HandleFunc("POST /v1/workspaces/{ws}/knowledge/items/{id}/restore", s.inBrand(manage, s.restoreKnowledgeItem))
	mux.HandleFunc("GET /v1/workspaces/{ws}/knowledge/items/{id}/versions", s.inBrand(read, s.listKnowledgeVersions))
	mux.HandleFunc("POST /v1/workspaces/{ws}/knowledge/items/{id}/versions/{version}/restore", s.inBrand(manage, s.restoreKnowledgeVersion))
}

// Collections

type knowledgeCollectionView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	ParentID    *string   `json:"parent_id"`
	BrandID     *string   `json:"brand_id"`
	Position    int       `json:"position"`
	ItemCount   int       `json:"item_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const knowledgeCollectionSelect = `
	SELECT c.id::text, c.name, c.description, c.parent_id::text, c.brand_id::text, c.position,
	       (SELECT count(*)::int FROM knowledge_items i WHERE i.collection_id = c.id AND i.deleted_at IS NULL),
	       c.created_at, c.updated_at
	FROM knowledge_collections c
	WHERE c.workspace_id = $1`

func scanKnowledgeCollection(row pgx.Row) (knowledgeCollectionView, error) {
	var v knowledgeCollectionView
	err := row.Scan(&v.ID, &v.Name, &v.Description, &v.ParentID, &v.BrandID, &v.Position, &v.ItemCount, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (s *Server) listKnowledgeCollections(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), knowledgeCollectionSelect+brand.sql("c.brand_id", &args)+`
		ORDER BY c.position, lower(c.name), c.id`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (knowledgeCollectionView, error) { return scanKnowledgeCollection(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []knowledgeCollectionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// knowledgeCollectionFor loads a collection the caller may see, or
// errNotFound.
func knowledgeCollectionFor(ctx context.Context, q pgx.Tx, a *access, id string) (knowledgeCollectionView, error) {
	if !validUUID(id) {
		return knowledgeCollectionView{}, errNotFound
	}
	v, err := scanKnowledgeCollection(q.QueryRow(ctx, knowledgeCollectionSelect+` AND c.id = $2`, a.WorkspaceID, id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(v.BrandID)) {
		return knowledgeCollectionView{}, errNotFound
	}
	return v, err
}

type knowledgeCollectionInput struct {
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	ParentID    optional[string] `json:"parent_id"`
	// A brand's id or handle; null for every brand.
	BrandID  optional[string] `json:"brand_id"`
	Position *int             `json:"position"`
}

func (in *knowledgeCollectionInput) validate() error {
	if in.Name != nil {
		*in.Name = strings.Join(strings.Fields(*in.Name), " ")
		if *in.Name == "" || utf8.RuneCountInString(*in.Name) > 100 {
			return errInvalid("name must be 1 to 100 characters")
		}
	}
	if in.Description != nil {
		*in.Description = strings.TrimSpace(*in.Description)
		if utf8.RuneCountInString(*in.Description) > 1000 {
			return errInvalid("description must be at most 1000 characters")
		}
	}
	if v := in.ParentID.Value; v != nil && !validUUID(*v) {
		return errInvalid("parent_id is a folder's id, or null for the top level")
	}
	return nil
}

// checkKnowledgeParent checks a folder may sit in parent: the parent is
// the caller's to see, of the same brand or shared, not the folder itself
// or inside it, and not too deep.
func checkKnowledgeParent(ctx context.Context, tx pgx.Tx, a *access, self string, parent *string, brand *string) error {
	if parent == nil {
		return nil
	}
	p, err := knowledgeCollectionFor(ctx, tx, a, *parent)
	if errors.Is(err, errNotFound) {
		return errInvalid("parent folder not found")
	}
	if err != nil {
		return err
	}
	if p.BrandID != nil && (brand == nil || *brand != *p.BrandID) {
		return errInvalid("a folder inside a brand's folder must be of the same brand")
	}
	// Walk up from the parent: the folder must not be above it, and the
	// tree stays shallow enough to read.
	var depth int
	var loop bool
	if err := tx.QueryRow(ctx, `
		WITH RECURSIVE up AS (
			SELECT id, parent_id, 1 AS depth FROM knowledge_collections WHERE id = $1 AND workspace_id = $3
			UNION ALL
			SELECT c.id, c.parent_id, up.depth + 1 FROM knowledge_collections c JOIN up ON c.id = up.parent_id WHERE up.depth < 50
		)
		SELECT COALESCE(max(depth), 0), bool_or(id::text = $2) FROM up`, *parent, self, a.WorkspaceID).Scan(&depth, &loop); err != nil {
		return err
	}
	if loop {
		return errInvalid("a folder can't go inside itself")
	}
	// The folders below self move with it.
	var below int
	if self != "" {
		if err := tx.QueryRow(ctx, `
			WITH RECURSIVE down AS (
				SELECT id, 0 AS depth FROM knowledge_collections WHERE id = $1
				UNION ALL
				SELECT c.id, down.depth + 1 FROM knowledge_collections c JOIN down ON c.parent_id = down.id WHERE down.depth < 50
			)
			SELECT max(depth) FROM down`, self).Scan(&below); err != nil {
			return err
		}
	}
	if depth+1+below > maxKnowledgeDepth {
		return errInvalid("folders go at most 5 deep")
	}
	return nil
}

func (s *Server) createKnowledgeCollection(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in knowledgeCollectionInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if err := in.validate(); s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	brand, err := s.brandRef(ctx, a.WorkspaceID, in.BrandID.Value)
	if s.allocError(w, r, err) {
		return
	}
	var v knowledgeCollectionView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if brand == nil && in.ParentID.Value != nil {
			// A folder made inside a brand's folder takes its brand.
			if p, err := knowledgeCollectionFor(ctx, tx, a, *in.ParentID.Value); err == nil {
				brand = p.BrandID
			}
		}
		if brand, err = brandWrite(ctx, tx, a, brand); err != nil {
			return err
		}
		if err := checkKnowledgeParent(ctx, tx, a, "", in.ParentID.Value, brand); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM knowledge_collections WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= maxKnowledgeCollections {
			return errInvalid("a workspace can have at most 500 knowledge folders")
		}
		pos := 0
		if in.Position != nil {
			pos = *in.Position
		} else if err := tx.QueryRow(ctx, `SELECT COALESCE(max(position) + 1, 0) FROM knowledge_collections WHERE workspace_id = $1 AND parent_id IS NOT DISTINCT FROM $2::uuid`,
			a.WorkspaceID, in.ParentID.Value).Scan(&pos); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO knowledge_collections (workspace_id, brand_id, parent_id, name, description, position, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
			a.WorkspaceID, brand, in.ParentID.Value, *in.Name, deref(in.Description), pos, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "knowledge_collection.created", "knowledge_collection", id,
			map[string]any{"name": *in.Name, "parent_id": in.ParentID.Value, "brand_id": brand}); err != nil {
			return err
		}
		v, err = knowledgeCollectionFor(ctx, tx, a, id)
		return err
	})
	if s.knowledgeError(w, r, err, "folder") {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateKnowledgeCollection(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in knowledgeCollectionInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.validate(); s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	var v knowledgeCollectionView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := knowledgeCollectionFor(ctx, tx, a, r.PathValue("id"))
		if err != nil {
			return err
		}
		if !a.writes(cur.BrandID) {
			return errSharedRecord
		}
		brand := cur.BrandID
		if in.BrandID.Set {
			if brand, err = s.brandRef(ctx, a.WorkspaceID, in.BrandID.Value); err != nil {
				return err
			}
			if a.Brands != nil && (brand == nil || !a.writes(brand)) {
				return errInvalid("you can only move folders between your own brands")
			}
		}
		parent := cur.ParentID
		if in.ParentID.Set {
			parent = in.ParentID.Value
		}
		if in.ParentID.Set || in.BrandID.Set {
			if err := checkKnowledgeParent(ctx, tx, a, cur.ID, parent, brand); err != nil {
				return err
			}
		}
		if in.BrandID.Set && !sameBrand(brand, cur.BrandID) && brand != nil {
			// What it holds must fit the new brand.
			var other bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM knowledge_items WHERE collection_id = $1 AND brand_id IS DISTINCT FROM $2::uuid)
				    OR EXISTS (SELECT 1 FROM knowledge_collections WHERE parent_id = $1 AND brand_id IS DISTINCT FROM $2::uuid)`,
				cur.ID, *brand).Scan(&other); err != nil {
				return err
			}
			if other {
				return errInvalid("the folder holds knowledge of another brand or shared by every brand; move it out first")
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE knowledge_collections SET name = COALESCE($3, name), description = COALESCE($4, description),
				parent_id = $5, brand_id = $6, position = COALESCE($7, position), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, cur.ID, in.Name, in.Description, parent, brand, in.Position); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "knowledge_collection.updated", "knowledge_collection", cur.ID,
			map[string]any{"name": coalesce(in.Name, cur.Name), "parent_id": parent, "brand_id": brand}); err != nil {
			return err
		}
		v, err = knowledgeCollectionFor(ctx, tx, a, cur.ID)
		return err
	})
	if s.knowledgeError(w, r, err, "folder") {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// deleteKnowledgeCollection removes a folder. What it holds, items and
// folders, moves up to its parent, so nothing is lost.
func (s *Server) deleteKnowledgeCollection(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := knowledgeCollectionFor(ctx, tx, a, r.PathValue("id"))
		if err != nil {
			return err
		}
		if !a.writes(cur.BrandID) {
			return errSharedRecord
		}
		items, err := tx.Exec(ctx, `UPDATE knowledge_items SET collection_id = $3 WHERE workspace_id = $1 AND collection_id = $2`, a.WorkspaceID, cur.ID, cur.ParentID)
		if err != nil {
			return err
		}
		folders, err := tx.Exec(ctx, `UPDATE knowledge_collections SET parent_id = $3, updated_at = now() WHERE workspace_id = $1 AND parent_id = $2`, a.WorkspaceID, cur.ID, cur.ParentID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM knowledge_collections WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "knowledge_collection.deleted", "knowledge_collection", cur.ID,
			map[string]any{"name": cur.Name, "parent_id": cur.ParentID, "brand_id": cur.BrandID, "items_moved": items.RowsAffected(), "folders_moved": folders.RowsAffected()},
			map[string]any{"items_moved": items.RowsAffected(), "folders_moved": folders.RowsAffected()})
	})
	if s.knowledgeError(w, r, err, "folder") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Items

type knowledgeItemView struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Title        string     `json:"title"`
	Body         *string    `json:"body,omitempty"`
	Excerpt      string     `json:"excerpt"`
	Visibility   string     `json:"visibility"`
	Status       string     `json:"status"`
	BrandID      *string    `json:"brand_id"`
	CollectionID *string    `json:"collection_id"`
	Version      int        `json:"version"`
	OwnerID      *string    `json:"owner_id"`
	OwnerName    *string    `json:"owner_name"`
	ReviewBy     *string    `json:"review_by"`
	CreatedBy    *string    `json:"created_by"`
	UpdatedBy    *string    `json:"updated_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	DeletedAt    *time.Time `json:"deleted_at"`
}

// knowledgeSearchVector is the expression knowledge_items_search indexes;
// a query must use it exactly for the index to apply.
const knowledgeSearchVector = `(setweight(to_tsvector('simple', i.title), 'A') || setweight(to_tsvector('simple', left(i.body, 100000)), 'C'))`

const knowledgeItemColumns = `
	i.id::text, i.kind, i.title, i.body, i.visibility, i.status, i.brand_id::text, i.collection_id::text, i.version,
	ou.id::text, ou.name, to_char(i.review_by, 'YYYY-MM-DD'), ca.display_name, ua.display_name, i.created_at, i.updated_at, i.deleted_at`

const knowledgeItemJoins = `
	FROM knowledge_items i
	LEFT JOIN users ou ON ou.actor_id = i.owner_id
	LEFT JOIN actors ca ON ca.id = i.created_by
	LEFT JOIN actors ua ON ua.id = i.updated_by`

func scanKnowledgeItem(row pgx.Row, full bool, extra ...any) (knowledgeItemView, error) {
	var v knowledgeItemView
	var body string
	dest := append([]any{&v.ID, &v.Kind, &v.Title, &body, &v.Visibility, &v.Status, &v.BrandID, &v.CollectionID, &v.Version,
		&v.OwnerID, &v.OwnerName, &v.ReviewBy, &v.CreatedBy, &v.UpdatedBy, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return v, err
	}
	v.Excerpt = knowledgeExcerpt(body)
	if full {
		v.Body = &body
	}
	return v, nil
}

// knowledgeExcerpt is the start of a body as plain text, for lists.
func knowledgeExcerpt(body string) string {
	var lines []string
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		// Table rules and code fences say nothing.
		if strings.Trim(l, "|-: ") == "" || strings.HasPrefix(l, "```") {
			continue
		}
		lines = append(lines, l)
	}
	r := strings.NewReplacer("#", "", "*", "", "_", "", "`", "", ">", "", "|", " ", "[ ]", "", "[x]", "", "[", "", "]", "")
	text := strings.Join(strings.Fields(r.Replace(strings.Join(lines, " "))), " ")
	if utf8.RuneCountInString(text) > 200 {
		text = string([]rune(text)[:199]) + "…"
	}
	return text
}

// knowledgeItemFor loads an item the caller may see. In the bin it is
// found only with bin true.
func knowledgeItemFor(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, a *access, id string, bin bool) (knowledgeItemView, error) {
	if !validUUID(id) {
		return knowledgeItemView{}, errNotFound
	}
	v, err := scanKnowledgeItem(q.QueryRow(ctx, `SELECT `+knowledgeItemColumns+knowledgeItemJoins+`
		WHERE i.workspace_id = $1 AND i.id = $2 AND (i.deleted_at IS NULL OR $3)`, a.WorkspaceID, id, bin), true)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(v.BrandID)) {
		return knowledgeItemView{}, errNotFound
	}
	return v, err
}

func (s *Server) listKnowledgeItems(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	qs := r.URL.Query()
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	limit, offset := 50, 0
	for name, dst := range map[string]*int{"limit": &limit, "offset": &offset} {
		if v := qs.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || (name == "limit" && (n < 1 || n > 200)) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", name+map[string]string{"limit": " must be a whole number from 1 to 200", "offset": " must be a whole number of 0 or more"}[name])
				return
			}
			*dst = n
		}
	}
	kind, visibility, status, collection := qs.Get("kind"), qs.Get("visibility"), qs.Get("status"), qs.Get("collection_id")
	switch {
	case kind != "" && !slices.Contains(knowledgeKinds, kind):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is article or snippet")
		return
	case visibility != "" && !slices.Contains(knowledgeVisibilities, visibility):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "visibility is public, customers_ai or team")
		return
	case status != "" && !slices.Contains(knowledgeStatuses, status):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is draft or published")
		return
	case collection != "" && collection != "none" && !validUUID(collection):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "collection_id is a folder's id, or none for knowledge in no folder")
		return
	}
	bin := false
	if v := qs.Get("deleted"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "deleted is true (the bin) or false")
			return
		}
		bin = b
	}
	query := searchQuery(qs.Get("q"))
	args := []any{a.WorkspaceID, bin, kind, visibility, status, collection, query}
	where := ` WHERE i.workspace_id = $1 AND (i.deleted_at IS NOT NULL) = $2
		AND ($3 = '' OR i.kind = $3) AND ($4 = '' OR i.visibility = $4) AND ($5 = '' OR i.status = $5)
		AND ($6 = '' OR ($6 = 'none' AND i.collection_id IS NULL) OR i.collection_id::text = $6)
		AND ($7 = '' OR ` + knowledgeSearchVector + ` @@ to_tsquery('simple', $7))` + brand.sql("i.brand_id", &args)
	order := ` ORDER BY i.updated_at DESC, i.id`
	switch {
	case bin:
		order = ` ORDER BY i.deleted_at DESC, i.id`
	case query != "":
		order = ` ORDER BY ts_rank(` + knowledgeSearchVector + `, to_tsquery('simple', $7)) DESC, i.updated_at DESC, i.id`
	}
	args = append(args, limit, offset)
	rows, err := s.pool.Query(r.Context(), `SELECT `+knowledgeItemColumns+`, count(*) OVER ()::int`+knowledgeItemJoins+where+order+
		` LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	total := 0
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (knowledgeItemView, error) {
		return scanKnowledgeItem(row, false, &total)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []knowledgeItemView{}
	}
	var next *int
	if offset+len(list) < total {
		n := offset + len(list)
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_offset": next})
}

func (s *Server) getKnowledgeItem(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := knowledgeItemFor(r.Context(), s.pool, a, r.PathValue("id"), true)
	if s.knowledgeError(w, r, err, "knowledge") {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type knowledgeItemInput struct {
	Kind         *string          `json:"kind"`
	Title        *string          `json:"title"`
	Body         *string          `json:"body"`
	Visibility   *string          `json:"visibility"`
	Status       *string          `json:"status"`
	CollectionID optional[string] `json:"collection_id"`
	// A brand's id or handle; null for every brand.
	BrandID optional[string] `json:"brand_id"`
	// A member's user id; null for nobody.
	OwnerID  optional[string] `json:"owner_id"`
	ReviewBy optional[string] `json:"review_by"`
}

func (in *knowledgeItemInput) validate() error {
	if in.Kind != nil && !slices.Contains(knowledgeKinds, *in.Kind) {
		return errInvalid("kind is article or snippet")
	}
	if in.Title != nil {
		*in.Title = strings.Join(strings.Fields(*in.Title), " ")
		if *in.Title == "" || utf8.RuneCountInString(*in.Title) > 300 {
			return errInvalid("title must be 1 to 300 characters")
		}
	}
	if in.Body != nil {
		*in.Body = strings.TrimSpace(strings.ReplaceAll(*in.Body, "\r\n", "\n"))
		if utf8.RuneCountInString(*in.Body) > maxKnowledgeBody {
			return errInvalid("body must be at most 200,000 characters; split it into several articles")
		}
	}
	if in.Visibility != nil && !slices.Contains(knowledgeVisibilities, *in.Visibility) {
		return errInvalid("visibility is public, customers_ai or team")
	}
	if in.Status != nil && !slices.Contains(knowledgeStatuses, *in.Status) {
		return errInvalid("status is draft or published")
	}
	if v := in.CollectionID.Value; v != nil && !validUUID(*v) {
		return errInvalid("collection_id is a folder's id, or null for no folder")
	}
	if v := in.OwnerID.Value; v != nil && !validUUID(*v) {
		return errInvalid("owner_id is a member's user id, or null")
	}
	if v := in.ReviewBy.Value; v != nil {
		if _, err := time.Parse(time.DateOnly, *v); err != nil {
			return errInvalid("review_by is a date (YYYY-MM-DD), or null")
		}
	}
	return nil
}

// knowledgeOwner is the actor of a member of the workspace, by user id.
func knowledgeOwner(ctx context.Context, tx pgx.Tx, ws string, user *string) (*string, error) {
	if user == nil {
		return nil, nil
	}
	var actor string
	err := tx.QueryRow(ctx, `
		SELECT u.actor_id::text FROM users u
		WHERE u.id = $2 AND u.disabled_at IS NULL AND
			EXISTS (SELECT 1 FROM memberships m WHERE m.workspace_id = $1 AND m.user_id = u.id)`,
		ws, *user).Scan(&actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errInvalid("owner_id must be a member of the workspace")
	}
	return &actor, err
}

// knowledgeCollectionBrand checks an item may go in a folder, and returns
// the brand it takes: a brand's folder holds only that brand's knowledge.
func knowledgeCollectionBrand(ctx context.Context, tx pgx.Tx, a *access, collection, brand *string, brandSet bool) (*string, error) {
	if collection == nil {
		return brand, nil
	}
	c, err := knowledgeCollectionFor(ctx, tx, a, *collection)
	if errors.Is(err, errNotFound) {
		return nil, errInvalid("folder not found")
	}
	if err != nil {
		return nil, err
	}
	if c.BrandID == nil {
		return brand, nil
	}
	if !brandSet {
		return c.BrandID, nil
	}
	if brand == nil || *brand != *c.BrandID {
		return nil, errInvalid("that folder holds one brand's knowledge; give the item that brand or pick another folder")
	}
	return brand, nil
}

func (s *Server) createKnowledgeItem(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in knowledgeItemInput
	if !decode(w, r, &in) {
		return
	}
	if in.Title == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "title is required: the article's title, or the question a quick answer answers")
		return
	}
	if err := in.validate(); s.allocError(w, r, err) {
		return
	}
	kind := coalesce(in.Kind, "article")
	if kind == "snippet" && strings.TrimSpace(deref(in.Body)) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a quick answer needs its answer in body")
		return
	}
	ctx := r.Context()
	brand, err := s.brandRef(ctx, a.WorkspaceID, in.BrandID.Value)
	if s.allocError(w, r, err) {
		return
	}
	var v knowledgeItemView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if brand, err = knowledgeCollectionBrand(ctx, tx, a, in.CollectionID.Value, brand, in.BrandID.Set); err != nil {
			return err
		}
		if brand, err = brandWrite(ctx, tx, a, brand); err != nil {
			return err
		}
		owner := nullIfEmpty(a.ActorID)
		if in.OwnerID.Set {
			if owner, err = knowledgeOwner(ctx, tx, a.WorkspaceID, in.OwnerID.Value); err != nil {
				return err
			}
		} else if a.UserID == "" {
			owner = nil
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM knowledge_items WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= maxKnowledgeItems {
			return errInvalid("a workspace can keep at most 10,000 knowledge items; empty the bin or delete ones that are out of date")
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO knowledge_items (workspace_id, brand_id, collection_id, kind, title, body, visibility, status, owner_id, review_by, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::date, $11, $11) RETURNING id::text`,
			a.WorkspaceID, brand, in.CollectionID.Value, kind, *in.Title, deref(in.Body), coalesce(in.Visibility, "team"),
			coalesce(in.Status, "published"), owner, in.ReviewBy.Value, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO knowledge_versions (workspace_id, item_id, version, title, body, created_by)
			SELECT workspace_id, id, version, title, body, created_by FROM knowledge_items WHERE id = $1`, id); err != nil {
			return err
		}
		v, err = knowledgeItemFor(ctx, tx, a, id, false)
		if err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "knowledge.created", "knowledge", id,
			knowledgeAuditData(v), knowledgeProps(v))
	})
	if s.knowledgeError(w, r, err, "knowledge") {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// knowledgeAuditData is an item as the audit log and webhooks show it:
// never its body, which can be long.
func knowledgeAuditData(v knowledgeItemView) map[string]any {
	return map[string]any{"kind": v.Kind, "title": v.Title, "visibility": v.Visibility, "status": v.Status,
		"version": v.Version, "collection_id": v.CollectionID, "brand_id": v.BrandID}
}

// knowledgeProps are an item's product event properties: enums and counts.
func knowledgeProps(v knowledgeItemView) map[string]any {
	n := 0
	if v.Body != nil {
		n = utf8.RuneCountInString(*v.Body)
	}
	return map[string]any{"kind": v.Kind, "visibility": v.Visibility, "status": v.Status, "version": v.Version, "body_chars": n}
}

func (s *Server) updateKnowledgeItem(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in knowledgeItemInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.validate(); s.allocError(w, r, err) {
		return
	}
	if in.Kind != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind can't change; make a new item instead")
		return
	}
	ctx := r.Context()
	var v knowledgeItemView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := knowledgeItemFor(ctx, tx, a, r.PathValue("id"), false)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM knowledge_items WHERE id = $1 FOR UPDATE`, cur.ID); err != nil {
			return err
		}
		if !a.writes(cur.BrandID) {
			return errSharedRecord
		}
		brand := cur.BrandID
		if in.BrandID.Set {
			if brand, err = s.brandRef(ctx, a.WorkspaceID, in.BrandID.Value); err != nil {
				return err
			}
			if a.Brands != nil && (brand == nil || !a.writes(brand)) {
				return errInvalid("you can only move knowledge between your own brands")
			}
		}
		collection := cur.CollectionID
		if in.CollectionID.Set {
			collection = in.CollectionID.Value
		}
		if in.CollectionID.Set || in.BrandID.Set {
			if brand, err = knowledgeCollectionBrand(ctx, tx, a, collection, brand, in.BrandID.Set || cur.BrandID != nil); err != nil {
				return err
			}
		}
		owner, ownerSet := (*string)(nil), in.OwnerID.Set
		if ownerSet {
			if owner, err = knowledgeOwner(ctx, tx, a.WorkspaceID, in.OwnerID.Value); err != nil {
				return err
			}
		}
		if cur.Kind == "snippet" && in.Body != nil && *in.Body == "" {
			return errInvalid("a quick answer needs its answer in body")
		}
		textChanged := (in.Title != nil && *in.Title != cur.Title) || (in.Body != nil && *in.Body != *cur.Body)
		if _, err := tx.Exec(ctx, `
			UPDATE knowledge_items SET title = COALESCE($3, title), body = COALESCE($4, body),
				visibility = COALESCE($5, visibility), status = COALESCE($6, status), collection_id = $7, brand_id = $8,
				owner_id = CASE WHEN $9 THEN $10::uuid ELSE owner_id END,
				review_by = CASE WHEN $11 THEN $12::date ELSE review_by END,
				version = version + CASE WHEN $13 THEN 1 ELSE 0 END, updated_by = $14, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, cur.ID, in.Title, in.Body, in.Visibility, in.Status, collection, brand,
			ownerSet, owner, in.ReviewBy.Set, in.ReviewBy.Value, textChanged, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		if textChanged {
			if _, err := tx.Exec(ctx, `
				INSERT INTO knowledge_versions (workspace_id, item_id, version, title, body, created_by)
				SELECT workspace_id, id, version, title, body, updated_by FROM knowledge_items WHERE id = $1`, cur.ID); err != nil {
				return err
			}
		}
		if v, err = knowledgeItemFor(ctx, tx, a, cur.ID, false); err != nil {
			return err
		}
		data := knowledgeAuditData(v)
		data["text_changed"] = textChanged
		props := knowledgeProps(v)
		props["text_changed"] = textChanged
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "knowledge.updated", "knowledge", cur.ID, data, props)
	})
	if s.knowledgeError(w, r, err, "knowledge") {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// deleteKnowledgeItem moves an item to the bin, or with ?permanent=true
// deletes one already in the bin for good.
func (s *Server) deleteKnowledgeItem(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	permanent := r.URL.Query().Get("permanent") == "true"
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := knowledgeItemFor(ctx, tx, a, r.PathValue("id"), permanent)
		if err != nil {
			return err
		}
		if !a.writes(cur.BrandID) {
			return errSharedRecord
		}
		if permanent {
			if cur.DeletedAt == nil {
				return errInvalid("only knowledge in the bin can be deleted for good; delete it once to move it to the bin")
			}
			if _, err := tx.Exec(ctx, `DELETE FROM knowledge_items WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID); err != nil {
				return err
			}
			return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "knowledge.purged", "knowledge", cur.ID, knowledgeAuditData(cur), knowledgeProps(cur))
		}
		if _, err := tx.Exec(ctx, `UPDATE knowledge_items SET deleted_at = now(), deleted_by = $3 WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, cur.ID, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "knowledge.deleted", "knowledge", cur.ID, knowledgeAuditData(cur), knowledgeProps(cur))
	})
	if s.knowledgeError(w, r, err, "knowledge") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// restoreKnowledgeItem takes an item out of the bin. A folder deleted
// since leaves it in no folder.
func (s *Server) restoreKnowledgeItem(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var v knowledgeItemView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := knowledgeItemFor(ctx, tx, a, r.PathValue("id"), true)
		if err != nil {
			return err
		}
		if !a.writes(cur.BrandID) {
			return errSharedRecord
		}
		if cur.DeletedAt == nil {
			return errInvalid("this knowledge isn't in the bin")
		}
		if _, err := tx.Exec(ctx, `UPDATE knowledge_items SET deleted_at = NULL, deleted_by = NULL, updated_by = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, cur.ID, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		if v, err = knowledgeItemFor(ctx, tx, a, cur.ID, false); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "knowledge.restored", "knowledge", cur.ID, knowledgeAuditData(v), knowledgeProps(v))
	})
	if s.knowledgeError(w, r, err, "knowledge") {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type knowledgeVersionView struct {
	Version   int       `json:"version"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedBy *string   `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) listKnowledgeVersions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	cur, err := knowledgeItemFor(ctx, s.pool, a, r.PathValue("id"), true)
	if s.knowledgeError(w, r, err, "knowledge") {
		return
	}
	rows, err := s.pool.Query(ctx, `
		SELECT v.version, v.title, v.body, ac.display_name, v.created_at
		FROM knowledge_versions v LEFT JOIN actors ac ON ac.id = v.created_by
		WHERE v.workspace_id = $1 AND v.item_id = $2
		ORDER BY v.version DESC LIMIT 100`, a.WorkspaceID, cur.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (knowledgeVersionView, error) {
		var v knowledgeVersionView
		err := row.Scan(&v.Version, &v.Title, &v.Body, &v.CreatedBy, &v.CreatedAt)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []knowledgeVersionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// restoreKnowledgeVersion makes an earlier version's text the current one,
// as a new version, so the restore can be undone too.
func (s *Server) restoreKnowledgeVersion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	n, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || n < 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "version not found")
		return
	}
	var v knowledgeItemView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := knowledgeItemFor(ctx, tx, a, r.PathValue("id"), false)
		if err != nil {
			return err
		}
		if !a.writes(cur.BrandID) {
			return errSharedRecord
		}
		var title, body string
		err = tx.QueryRow(ctx, `SELECT title, body FROM knowledge_versions WHERE item_id = $1 AND version = $2`, cur.ID, n).Scan(&title, &body)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalid("that version isn't kept for this item")
		}
		if err != nil {
			return err
		}
		if title == cur.Title && body == *cur.Body {
			v = cur
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE knowledge_items SET title = $3, body = $4, version = version + 1, updated_by = $5, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, title, body, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO knowledge_versions (workspace_id, item_id, version, title, body, created_by)
			SELECT workspace_id, id, version, title, body, updated_by FROM knowledge_items WHERE id = $1`, cur.ID); err != nil {
			return err
		}
		if v, err = knowledgeItemFor(ctx, tx, a, cur.ID, false); err != nil {
			return err
		}
		data := knowledgeAuditData(v)
		data["text_changed"], data["restored_version"] = true, n
		props := knowledgeProps(v)
		props["text_changed"], props["restored_version"] = true, n
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "knowledge.updated", "knowledge", cur.ID, data, props)
	})
	if s.knowledgeError(w, r, err, "knowledge") {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// knowledgeError answers a knowledge request's error; true when it did.
func (s *Server) knowledgeError(w http.ResponseWriter, r *http.Request, err error, what string) bool {
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", what+" not found")
		return true
	}
	return s.allocError(w, r, err)
}

// PruneKnowledgeBin deletes knowledge that has been in the bin for more
// than knowledgeBinDays, a batch at a time.
func (s *Server) PruneKnowledgeBin(ctx context.Context) (int64, error) {
	var total int64
	for {
		tag, err := s.pool.Exec(ctx, `DELETE FROM knowledge_items WHERE id IN (
			SELECT id FROM knowledge_items WHERE deleted_at < now() - make_interval(days => $1) LIMIT 1000)`, knowledgeBinDays)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < 1000 {
			return total, nil
		}
	}
}

func sameBrand(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
