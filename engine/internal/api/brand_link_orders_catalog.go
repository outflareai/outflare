package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
)

func (s *Server) listLinkOrderCatalog(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	cur, ok := linkOrderCursor(w, r, s.pool, "coverage_donor_profiles", ws, brand)
	if !ok {
		return
	}
	f := linkorders.DonorListFilter{
		Q:            strings.TrimSpace(r.URL.Query().Get("q")),
		Niche:        strings.TrimSpace(r.URL.Query().Get("niche")),
		Intermediary: strings.TrimSpace(r.URL.Query().Get("intermediary")),
	}
	switch r.URL.Query().Get("archived") {
	case "1", "true":
		f.OnlyArchived = true
	case "all":
		f.IncludeArchived = true
	}
	if v := r.URL.Query().Get("pbn"); v != "" {
		b := v == "1" || v == "true"
		if v == "0" || v == "false" || b {
			if v != "1" && v != "true" && v != "0" && v != "false" {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "pbn must be true or false")
				return
			}
			f.PBN = &b
		}
	}
	// direct_contact=true lists only the sites worth writing to directly
	// (not a PBN, DR or traffic stored, a stored address at the site).
	switch v := r.URL.Query().Get("direct_contact"); v {
	case "":
	case "1", "true", "0", "false":
		b := v == "1" || v == "true"
		f.DirectContact = &b
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "direct_contact must be true or false")
		return
	}
	list, err := linkorders.ListDonors(r.Context(), s.pool, ws, brand, f, limit+1, cur)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) > limit {
		id := list[limit-1].ID.String()
		next = &id
		list = list[:limit]
	}
	if list == nil {
		list = []linkorders.Donor{}
	}
	writeLinkOrderList(w, list, next)
}

func (s *Server) getLinkOrderCatalogItem(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	d, err := linkorders.GetDonor(r.Context(), s.pool, ws, brand, id)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) createLinkOrderCatalog(w http.ResponseWriter, r *http.Request) {
	s.upsertLinkOrderCatalog(w, r, nil, http.StatusCreated)
}

func (s *Server) updateLinkOrderCatalogItem(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	s.upsertLinkOrderCatalog(w, r, &id, http.StatusOK)
}

func (s *Server) upsertLinkOrderCatalog(w http.ResponseWriter, r *http.Request, existing *uuid.UUID, status int) {
	a := accessFrom(r.Context())
	ws, brand, actor, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	in, ok := decodeDonorWrite(w, r, existing == nil)
	if !ok {
		return
	}
	if existing != nil {
		cur, err := linkorders.GetDonor(r.Context(), s.pool, ws, brand, *existing)
		if err != nil {
			s.writeLinkOrderError(w, r, err)
			return
		}
		if in.Domain == "" {
			in.Domain = cur.Domain
		} else if in.Domain != cur.Domain {
			// Domain is the publisher's identity; changing it would be a
			// different catalog row. Callers update the other fields.
			writeError(w, r, http.StatusBadRequest, "invalid_request", "domain cannot change; create a new catalog row")
			return
		}
	} else if in.Domain == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domain is required")
		return
	}
	if in.Source == "" {
		in.Source = linkorders.SourceManual
	}
	var out *linkorders.Donor
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		d, created, err := linkorders.UpsertDonor(r.Context(), tx, ws, brand, in)
		if err != nil {
			return err
		}
		if existing != nil && created {
			return linkorders.ErrNotFound
		}
		action := "link_orders.catalog_updated"
		if created {
			action = "link_orders.catalog_created"
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, action, "brand", a.Brand, map[string]any{"brand_id": a.Brand}, map[string]any{"brand_id": a.Brand}); err != nil {
			return err
		}
		out = d
		return nil
	})
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	_ = actor
	writeJSON(w, status, out)
}

func (s *Server) importLinkOrderCatalog(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	body, err := readCatalogCSV(w, r)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request",
				fmt.Sprintf("the file is larger than %d MB; split it into smaller files (the Catalog page sends a large file in batches on its own)", maxCatalogImportBytes>>20))
			return
		}
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	file, err := linkorders.ParseCatalogCSV(strings.NewReader(body))
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	out := file.NewResult()
	// A large file is written in chunks, one transaction each, so a full
	// intermediary database never holds one long transaction. The audit
	// event is written with the last chunk, with the totals.
	n := len(file.Rows)
	for from := 0; from == 0 || from < n; from += linkorders.ImportChunkRows {
		to := min(from+linkorders.ImportChunkRows, n)
		last := to >= n
		err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
			if err := file.ImportRows(r.Context(), tx, ws, brand, from, to, out); err != nil {
				return err
			}
			if !last {
				return nil
			}
			props := map[string]any{"brand_id": a.Brand, "created": out.Created, "updated": out.Updated, "errors": len(out.Errors), "rows": n}
			return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "link_orders.catalog_imported", "brand", a.Brand, props, props)
		})
		if err != nil {
			s.writeLinkOrderError(w, r, err)
			return
		}
		if last {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// maxCatalogImportBytes bounds one catalog import request. A full
// intermediary database (11,000+ sites with every metric column) fits;
// the Catalog page still sends a large file in batches of about 1 MB so
// no single request is slow. It matches the Idempotency-Key body limit.
const maxCatalogImportBytes = 32 << 20

func readCatalogCSV(w http.ResponseWriter, r *http.Request) (string, error) {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCatalogImportBytes))
	if err != nil {
		return "", err
	}
	switch ct {
	case "text/csv", "application/csv", "":
		if ct == "" {
			// Fall through and try JSON if it looks like an object.
			trim := strings.TrimSpace(string(raw))
			if strings.HasPrefix(trim, "{") {
				return csvFromJSON(raw)
			}
		}
		return string(raw), nil
	case "application/json":
		return csvFromJSON(raw)
	default:
		// Some clients send text/plain; treat it as CSV.
		if ct == "text/plain" {
			return string(raw), nil
		}
		return "", linkorders.ErrInvalid
	}
}

func csvFromJSON(raw []byte) (string, error) {
	var in struct {
		CSV string `json:"csv"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.CSV == "" {
		return "", errInvalidCSV
	}
	return in.CSV, nil
}

var errInvalidCSV = errInvalid("send the file as text/csv, or JSON {\"csv\": \"...\"}")

func decodeDonorWrite(w http.ResponseWriter, r *http.Request, create bool) (linkorders.DonorWrite, bool) {
	var in linkorders.DonorWrite
	var raw map[string]json.RawMessage
	if !decode(w, r, &raw) {
		return in, false
	}
	str := func(key string) (*string, bool) {
		b, ok := raw[key]
		if !ok {
			return nil, true
		}
		if string(b) == "null" {
			s := ""
			return &s, true
		}
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", key+" must be a string")
			return nil, false
		}
		return &s, true
	}
	boolVal := func(key string) (*bool, bool) {
		b, ok := raw[key]
		if !ok {
			return nil, true
		}
		if string(b) == "null" {
			v := false
			return &v, true
		}
		var v bool
		if err := json.Unmarshal(b, &v); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", key+" must be a boolean")
			return nil, false
		}
		return &v, true
	}
	strs := func(key string) (*[]string, bool) {
		b, ok := raw[key]
		if !ok {
			return nil, true
		}
		if string(b) == "null" {
			s := []string{}
			return &s, true
		}
		var s []string
		if err := json.Unmarshal(b, &s); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", key+" must be an array of strings")
			return nil, false
		}
		return &s, true
	}
	intVal := func(key string) (**int, bool) {
		b, ok := raw[key]
		if !ok {
			return nil, true
		}
		if string(b) == "null" {
			var n *int
			return &n, true
		}
		var n int
		if err := json.Unmarshal(b, &n); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", key+" must be an integer")
			return nil, false
		}
		return ptrTo(&n), true
	}
	int64Val := func(key string) (**int64, bool) {
		b, ok := raw[key]
		if !ok {
			return nil, true
		}
		if string(b) == "null" {
			var n *int64
			return &n, true
		}
		var n int64
		if err := json.Unmarshal(b, &n); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", key+" must be an integer")
			return nil, false
		}
		return ptrTo(&n), true
	}
	if v, ok := str("domain"); !ok {
		return in, false
	} else if v != nil {
		in.Domain = *v
	}
	if v, ok := str("name"); !ok {
		return in, false
	} else if v != nil {
		in.Name = *v
	}
	if v, ok := boolVal("pbn"); !ok {
		return in, false
	} else if v != nil {
		in.PBN = v
	}
	if v, ok := str("country"); !ok {
		return in, false
	} else if v != nil {
		in.Country = ptrTo(v)
	}
	if v, ok := strs("languages"); !ok {
		return in, false
	} else if v != nil {
		in.Languages = v
	}
	if v, ok := strs("niches"); !ok {
		return in, false
	} else if v != nil {
		in.Niches = v
	}
	if v, ok := boolVal("casino_allowed"); !ok {
		return in, false
	} else if v != nil {
		in.CasinoAllowed = ptrTo(v)
	}
	if v, ok := strs("rel_offered"); !ok {
		return in, false
	} else if v != nil {
		in.RelOffered = v
	}
	if v, ok := strs("slot_types"); !ok {
		return in, false
	} else if v != nil {
		in.SlotTypes = v
	}
	if v, ok := str("duration"); !ok {
		return in, false
	} else if v != nil {
		in.Duration = ptrTo(v)
	}
	if v, ok := intVal("duration_days"); !ok {
		return in, false
	} else if v != nil {
		in.DurationDays = v
	}
	if v, ok := intVal("tat_days"); !ok {
		return in, false
	} else if v != nil {
		in.TATDays = v
	}
	if v, ok := intVal("guarantee_days"); !ok {
		return in, false
	} else if v != nil {
		in.GuaranteeDays = v
	}
	if v, ok := str("vendor_id"); !ok {
		return in, false
	} else if v != nil {
		in.VendorID = ptrTo(v)
	}
	if v, ok := str("notes"); !ok {
		return in, false
	} else if v != nil {
		in.Notes = ptrTo(v)
	}
	if v, ok := intVal("domain_rating"); !ok {
		return in, false
	} else if v != nil {
		in.DomainRating = v
	}
	if v, ok := int64Val("traffic"); !ok {
		return in, false
	} else if v != nil {
		in.Traffic = v
	}
	if v, ok := str("intermediary"); !ok {
		return in, false
	} else if v != nil {
		in.Intermediary = ptrTo(v)
	}
	if v, ok := str("theme"); !ok {
		return in, false
	} else if v != nil {
		in.Theme = ptrTo(v)
	}
	if v, ok := str("site_age"); !ok {
		return in, false
	} else if v != nil {
		if strings.TrimSpace(*v) == "" {
			var none *time.Time
			in.SiteSince = &none
		} else {
			t, set, err := linkorders.ParseSiteAge(*v, time.Now())
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "site_age: "+err.Error())
				return in, false
			}
			if set {
				in.SiteSince = ptrTo(&t)
			} else {
				var none *time.Time
				in.SiteSince = &none
			}
		}
	}
	if b, ok := raw["metrics"]; ok && string(b) != "null" {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "metrics must be an object")
			return in, false
		}
		patch, err := linkorders.ValidateMetrics(m)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(err.Error(), linkorders.ErrInvalid.Error()+": "))
			return in, false
		}
		in.Metrics = patch
	}
	if v, ok := boolVal("archived"); !ok {
		return in, false
	} else if v != nil {
		in.Archived = v
	}
	if b, ok := raw["prices"]; ok && string(b) != "null" {
		var prices []linkorders.Price
		if err := json.Unmarshal(b, &prices); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "prices must be an array of {currency, amount_minor}")
			return in, false
		}
		in.Prices = &prices
	}
	_ = create
	return in, true
}

func ptrTo[T any](v *T) **T { return &v }
