package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
)

// copyLinkOrderCatalog writes one page of another brand's donor catalog
// into this brand (linkorders.CopyCatalog). The page sends pages one after
// another until next_cursor is null, the way a large import is sent in
// batches; donors upsert by domain, so running it again is safe.
func (s *Server) copyLinkOrderCatalog(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var in struct {
		FromBrand string `json:"from_brand"`
		Cursor    string `json:"cursor"`
		Limit     int    `json:"limit"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send JSON with from_brand")
		return
	}
	from, err := uuid.Parse(strings.TrimSpace(in.FromBrand))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from_brand must be a brand id")
		return
	}
	if from == brand {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from_brand must be another brand")
		return
	}
	// A caller limited to some brands copies only from those.
	if a.Brands != nil && !slices.Contains(a.Brands, from.String()) {
		writeError(w, r, http.StatusNotFound, "not_found", "brand not found")
		return
	}
	if in.Limit < 0 || in.Limit > linkorders.MaxCopyPage {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is 1 to 500")
		return
	}
	var after *linkorders.Cursor
	if in.Cursor != "" {
		c, ok := decodeCopyCursor(in.Cursor)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "cursor is not one this endpoint returned")
			return
		}
		after = &c
	}
	var res linkorders.CopyResult
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if res, err = linkorders.CopyCatalog(r.Context(), tx, ws, from, brand, after, in.Limit); err != nil {
			return err
		}
		props := map[string]any{"brand_id": a.Brand, "from_brand_id": from.String(), "created": res.Created, "updated": res.Updated, "done": res.Next == nil}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "link_orders.catalog_copied", "brand", a.Brand, props, props)
	})
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	var next *string
	if res.Next != nil {
		c := encodeCopyCursor(*res.Next)
		next = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": res.Created, "updated": res.Updated, "next_cursor": next})
}

func encodeCopyCursor(c linkorders.Cursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.At.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()))
}

func decodeCopyCursor(s string) (linkorders.Cursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return linkorders.Cursor{}, false
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return linkorders.Cursor{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return linkorders.Cursor{}, false
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return linkorders.Cursor{}, false
	}
	return linkorders.Cursor{At: t, ID: u}, true
}
