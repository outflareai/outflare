package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
)

// Customer records: the people, companies and other things a workspace
// tracks, with the identities that point at them (emails, phones, the
// workspace's own user ids, Stripe customers, domains) and the links
// between them. Every value is checked against the catalog, so each fact
// has one definition and one place, whichever feature wrote it.
//
// Only a verified identity claims its value. An unconfirmed sign-up that
// typed someone else's email gets its own record and never takes over that
// person's; a verified identity another record already holds is refused
// with identity_taken.

func (s *Server) routesRecords(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	base := "/v1/workspaces/{ws}/records"
	mux.HandleFunc("GET "+base, s.inBrand(read, s.listRecords))
	mux.HandleFunc("POST "+base, s.inBrand(manage, s.createRecord))
	mux.HandleFunc("GET "+base+"/lookup", s.inBrand(read, s.lookupRecords))
	mux.HandleFunc("GET "+base+"/{record}", s.inBrand(read, s.getRecord))
	mux.HandleFunc("PATCH "+base+"/{record}", s.inBrand(manage, s.updateRecord))
	mux.HandleFunc("POST "+base+"/{record}/review", s.inBrand(manage, s.reviewRecord))
	mux.HandleFunc("GET "+base+"/{record}/history", s.inBrand(read, s.recordHistory))
	mux.HandleFunc("GET "+base+"/{record}/identities", s.inBrand(read, s.listIdentities))
	mux.HandleFunc("POST "+base+"/{record}/identities", s.inBrand(manage, s.addIdentity))
	mux.HandleFunc("POST "+base+"/{record}/identities/{identity}/close", s.inBrand(manage, s.closeIdentity))
	mux.HandleFunc("GET "+base+"/{record}/relationships", s.inBrand(read, s.listRelationships))
	mux.HandleFunc("POST "+base+"/{record}/relationships", s.inBrand(manage, s.addRelationship))
	mux.HandleFunc("POST "+base+"/{record}/relationships/{relationship}/end", s.inBrand(manage, s.endRelationship))
}

var (
	recordOrigins   = []string{"first_party", "prospect", "crm_sync", "import", "manual"}
	devicePlatforms = []string{"ios", "android", "web"}
	relationKind    = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)
	e164            = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
	isoDate         = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	currencyCode    = regexp.MustCompile(`^[A-Z]{3}$`)
)

type identityView struct {
	ID           string         `json:"id"`
	Namespace    string         `json:"namespace"`
	Value        string         `json:"value"`
	Verified     bool           `json:"verified"`
	VerifiedAt   *time.Time     `json:"verified_at"`
	Properties   map[string]any `json:"properties"`
	Source       string         `json:"source"`
	ValidFrom    time.Time      `json:"valid_from"`
	ValidTo      *time.Time     `json:"valid_to"`
	ClosedReason *string        `json:"closed_reason"`
}

type skippedField struct {
	Field string `json:"field"`
	// The source that set the current value and outranks this write's.
	KeptSource string `json:"kept_source"`
}

type recordView struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Name             string            `json:"name"`
	BrandID          *string           `json:"brand_id"`
	Origin           string            `json:"origin"`
	Attributes       map[string]any    `json:"attributes"`
	AttributeSources map[string]string `json:"attribute_sources"`
	Identities       []identityView    `json:"identities"`
	// People only: what may go out to them, and why.
	Contactability *contactability `json:"contactability,omitempty"`
	// Held for someone to approve: hidden from the list and never emailed
	// until then.
	NeedsReview bool      `json:"needs_review"`
	Version     int       `json:"version"`
	MergedInto  *string   `json:"merged_into"`
	Archived    bool      `json:"archived"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Set on a write: fields left as they were because a higher-precedence
	// source set them.
	Skipped []skippedField `json:"skipped,omitempty"`
}

// recordConflict is a 409 with its own code.
type recordConflict struct{ code, msg string }

func (e recordConflict) Error() string { return e.msg }

// recordError writes a records error, or what allocError would.
func (s *Server) recordError(w http.ResponseWriter, r *http.Request, err error) bool {
	var c recordConflict
	if errors.As(err, &c) {
		writeError(w, r, http.StatusConflict, c.code, c.msg)
		return true
	}
	return s.allocError(w, r, err)
}

// ---- Loading ----

const recordColumns = `r.id::text, r.record_type, r.name, r.brand_id::text, r.origin, r.attributes, r.attribute_sources, r.version,
	r.merged_into::text, r.archived_at IS NOT NULL, r.created_at, r.updated_at, r.needs_review`

func scanRecord(row pgx.Row) (recordView, error) {
	var v recordView
	err := row.Scan(&v.ID, &v.Type, &v.Name, &v.BrandID, &v.Origin, &v.Attributes, &v.AttributeSources, &v.Version,
		&v.MergedInto, &v.Archived, &v.CreatedAt, &v.UpdatedAt, &v.NeedsReview)
	return v, err
}

const identityColumns = `i.id::text, i.namespace, i.value, i.verified, i.verified_at, i.properties, i.source, i.valid_from, i.valid_to, i.closed_reason`

func scanIdentity(row pgx.CollectableRow) (identityView, error) {
	var v identityView
	err := row.Scan(&v.ID, &v.Namespace, &v.Value, &v.Verified, &v.VerifiedAt, &v.Properties, &v.Source, &v.ValidFrom, &v.ValidTo, &v.ClosedReason)
	return v, err
}

// withIdentities fills in each record's active identities.
func withIdentities(ctx context.Context, q pgx.Tx, list []recordView) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	at := map[string]int{}
	for i, v := range list {
		ids[i], at[v.ID] = v.ID, i
		list[i].Identities = []identityView{}
	}
	rows, err := q.Query(ctx, `SELECT i.record_id::text, `+identityColumns+` FROM record_identities i
		WHERE i.record_id = ANY($1::uuid[]) AND i.valid_to IS NULL ORDER BY i.namespace, i.valid_from, i.id`, ids)
	if err != nil {
		return err
	}
	type owned struct {
		record string
		identityView
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (owned, error) {
		var o owned
		err := row.Scan(&o.record, &o.ID, &o.Namespace, &o.Value, &o.Verified, &o.VerifiedAt, &o.Properties, &o.Source, &o.ValidFrom, &o.ValidTo, &o.ClosedReason)
		return o, err
	})
	if err != nil {
		return err
	}
	for _, o := range found {
		i := at[o.record]
		list[i].Identities = append(list[i].Identities, o.identityView)
	}
	return withContactability(ctx, q, list)
}

// loadRecord reads one record the caller can see, with its identities.
func loadRecord(ctx context.Context, q pgx.Tx, a *access, id string) (recordView, error) {
	if !validUUID(id) {
		return recordView{}, errNotFound
	}
	v, err := scanRecord(q.QueryRow(ctx, `SELECT `+recordColumns+` FROM records r WHERE r.id = $1 AND r.workspace_id = $2`, id, a.WorkspaceID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(nil, v.BrandID)) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	list := []recordView{v}
	err = withIdentities(ctx, q, list)
	return list[0], err
}

// lockRecord locks a record the caller may change.
func lockRecord(ctx context.Context, tx pgx.Tx, a *access, id string) (recordView, error) {
	if !validUUID(id) {
		return recordView{}, errNotFound
	}
	v, err := scanRecord(tx.QueryRow(ctx, `SELECT `+recordColumns+` FROM records r WHERE r.id = $1 AND r.workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(nil, v.BrandID)) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	if !a.writes(nil, v.BrandID) {
		return v, errSharedRecord
	}
	if v.MergedInto != nil {
		return v, recordConflict{"conflict", "this record was merged into " + *v.MergedInto + "; change that one instead"}
	}
	return v, nil
}

func (s *Server) readTx(ctx context.Context, f func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, f)
}

// ---- Values ----

// normalizeValue checks one attribute value against its field and returns
// it in stored form. ctx and tx check references.
func normalizeValue(ctx context.Context, tx pgx.Tx, c *catalog, ws string, f *fieldView, raw json.RawMessage) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, errInvalid(f.Key + " is not valid JSON")
	}
	if f.Multiple {
		list, ok := v.([]any)
		if !ok {
			return nil, errInvalid(f.Key + " takes a list of values")
		}
		if len(list) > 100 {
			return nil, errInvalid(f.Key + " takes at most 100 values")
		}
		out := make([]any, 0, len(list))
		seen := map[string]bool{}
		for _, item := range list {
			n, err := normalizeOne(c, f, item)
			if err != nil {
				return nil, err
			}
			k := fmt.Sprint(n)
			if !seen[k] {
				seen[k] = true
				out = append(out, n)
			}
		}
		return out, checkReferences(ctx, tx, c, ws, f, out)
	}
	n, err := normalizeOne(c, f, v)
	if err != nil {
		return nil, err
	}
	return n, checkReferences(ctx, tx, c, ws, f, []any{n})
}

func normalizeOne(c *catalog, f *fieldView, v any) (any, error) {
	bad := func(want string) error { return errInvalid(f.Key + " must be " + want) }
	str := func() (string, bool) {
		s, ok := v.(string)
		return strings.TrimSpace(s), ok
	}
	switch f.ValueType {
	case "text":
		s, ok := str()
		if !ok || len(s) > 10000 {
			return nil, bad("text of at most 10,000 characters")
		}
		return s, nil
	case "number":
		n, ok := v.(float64)
		if !ok {
			return nil, bad("a number")
		}
		return n, nil
	case "rating":
		n, ok := v.(float64)
		if !ok || n != math.Trunc(n) || n < 0 || n > 5 {
			return nil, bad("a whole number from 0 to 5")
		}
		return n, nil
	case "checkbox":
		b, ok := v.(bool)
		if !ok {
			return nil, bad("true or false")
		}
		return b, nil
	case "currency":
		m, ok := v.(map[string]any)
		amount, aok := m["amount_minor"].(float64)
		code, cok := m["currency"].(string)
		if !ok || len(m) != 2 || !aok || amount != math.Trunc(amount) || math.Abs(amount) > 1e15 || !cok || !currencyCode.MatchString(code) {
			return nil, bad(`{"amount_minor": 1999, "currency": "USD"}: a whole amount in the currency's smallest unit and an ISO 4217 code`)
		}
		return map[string]any{"amount_minor": amount, "currency": code}, nil
	case "date":
		s, ok := str()
		if _, err := time.Parse(time.DateOnly, s); !ok || !isoDate.MatchString(s) || err != nil {
			return nil, bad("a date such as 2026-10-26")
		}
		return s, nil
	case "timestamp":
		s, ok := str()
		t, err := time.Parse(time.RFC3339Nano, s)
		if !ok || err != nil {
			return nil, bad("an RFC 3339 time such as 2026-10-26T09:30:00Z")
		}
		return t.UTC().Format(time.RFC3339Nano), nil
	case "select", "status":
		s, ok := str()
		set := c.optionSet(deref(f.OptionSet))
		if !ok || set == nil {
			return nil, bad("an option key")
		}
		for _, o := range set.Options {
			if o.Key == s && !o.Archived {
				return s, nil
			}
		}
		return nil, errInvalid(fmt.Sprintf("%s must be one of the %s option keys (GET .../catalog/option-sets lists them)", f.Key, set.Key))
	case "email":
		s, ok := str()
		e, err := normalizeEmail(s)
		if !ok || err != nil {
			return nil, bad("an email address")
		}
		return e, nil
	case "phone":
		s, ok := str()
		p, err := normalizePhone(s)
		if !ok || err != nil {
			return nil, bad("a phone number in international format, such as +61412345678")
		}
		return p, nil
	case "url":
		s, ok := str()
		u, err := url.Parse(s)
		if !ok || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(s) > 2000 {
			return nil, bad("an http or https URL")
		}
		return s, nil
	case "location":
		m, ok := v.(map[string]any)
		if !ok {
			return nil, bad(`a location such as {"city": "Sydney", "country": "AU"}`)
		}
		out := map[string]any{}
		for k, val := range m {
			s, sok := val.(string)
			if !slices.Contains([]string{"line1", "line2", "city", "region", "postal_code", "country"}, k) || !sok || len(s) > 200 {
				return nil, bad("a location with only line1, line2, city, region, postal_code and country, each text")
			}
			if s = strings.TrimSpace(s); s == "" {
				continue
			}
			if k == "country" {
				s = strings.ToUpper(s)
				if !slices.ContainsFunc(countries, func(p [2]string) bool { return p[0] == s }) {
					return nil, bad("a location whose country is a two-letter ISO 3166 code, such as AU")
				}
			}
			out[k] = s
		}
		return out, nil
	case "record_reference", "actor_reference":
		s, ok := str()
		if !ok || !validUUID(s) {
			return nil, bad("an id")
		}
		return strings.ToLower(s), nil
	}
	return nil, bad("a supported value")
}

// checkReferences checks that record and member references point at
// something in the workspace.
func checkReferences(ctx context.Context, tx pgx.Tx, c *catalog, ws string, f *fieldView, values []any) error {
	if f.ValueType != "record_reference" && f.ValueType != "actor_reference" {
		return nil
	}
	ids := make([]string, 0, len(values))
	for _, v := range values {
		ids = append(ids, v.(string))
	}
	if len(ids) == 0 {
		return nil
	}
	var n int
	var err error
	if f.ValueType == "record_reference" {
		err = tx.QueryRow(ctx, `SELECT count(*) FROM records WHERE workspace_id = $1 AND id = ANY($2::uuid[]) AND record_type = $3 AND merged_into IS NULL`,
			ws, ids, deref(f.TargetType)).Scan(&n)
	} else {
		err = tx.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE workspace_id = $1 AND user_id = ANY($2::uuid[])`, ws, ids).Scan(&n)
	}
	if err != nil {
		return err
	}
	if n != len(ids) {
		if f.ValueType == "record_reference" {
			target := "record"
			if t := c.recordType(deref(f.TargetType)); t != nil {
				target = strings.ToLower(t.Name)
			}
			return errInvalid(fmt.Sprintf("%s must be the id of a %s in this workspace", f.Key, target))
		}
		return errInvalid(f.Key + " must be the user id of a member of this workspace")
	}
	return nil
}

// sourceRank orders a source by a field's precedence: lower wins. Sources
// the list leaves out rank last.
func sourceRank(f *fieldView, source string) int {
	if i := slices.Index(f.Precedence, source); i >= 0 {
		return i
	}
	return len(f.Precedence)
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// applyAttributes writes attribute changes to rec, field by field: checked
// against the catalog, kept when a higher-precedence source set them, and
// recorded in attribute_changes. It reports whether anything changed.
func applyAttributes(ctx context.Context, tx pgx.Tx, c *catalog, t *recordTypeView, a *access, rec *recordView, in map[string]json.RawMessage, source string) (bool, error) {
	return writeAttributes(ctx, tx, c, t, a, rec, in, source, false)
}

// applyComputed is applyAttributes for Outflare's own computed fields, such
// as revenue from payment connections. Only the engine calls it.
func applyComputed(ctx context.Context, tx pgx.Tx, c *catalog, t *recordTypeView, a *access, rec *recordView, in map[string]json.RawMessage, source string) (bool, error) {
	return writeAttributes(ctx, tx, c, t, a, rec, in, source, true)
}

func writeAttributes(ctx context.Context, tx pgx.Tx, c *catalog, t *recordTypeView, a *access, rec *recordView, in map[string]json.RawMessage, source string, computed bool) (bool, error) {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	changed := false
	for _, key := range keys {
		f := t.field(key)
		if f == nil {
			return false, errInvalid(fmt.Sprintf("%s has no field %s: add it with POST /v1/workspaces/{ws}/catalog/types/%s/fields, or use an existing one (GET .../catalog/types/%s)", t.Name, key, t.Key, t.Key))
		}
		if f.Archived {
			return false, errInvalid(fmt.Sprintf("field %s is archived; restore it in the catalog to write it", key))
		}
		switch {
		case f.WritePolicy == "computed" && !computed:
			return false, errInvalid(fmt.Sprintf("field %s is computed by Outflare and cannot be written", key))
		case f.WritePolicy == "source_owned" && source == "manual":
			return false, errInvalid(fmt.Sprintf("field %s is owned by its source system; send the write with that source", key))
		}
		var next any
		if string(in[key]) != "null" {
			v, err := normalizeValue(ctx, tx, c, a.WorkspaceID, f, in[key])
			if err != nil {
				return false, err
			}
			next = v
		}
		prev, had := rec.Attributes[key]
		if cur := rec.AttributeSources[key]; had && cur != "" && sourceRank(f, cur) < sourceRank(f, source) {
			rec.Skipped = append(rec.Skipped, skippedField{Field: key, KeptSource: cur})
			continue
		}
		if (next == nil && !had) || (had && sameJSON(prev, next)) {
			continue
		}
		if next == nil {
			delete(rec.Attributes, key)
			delete(rec.AttributeSources, key)
		} else {
			rec.Attributes[key] = next
			rec.AttributeSources[key] = source
		}
		if err := attributeChanged(ctx, tx, a, rec.ID, key, prev, next, had, source); err != nil {
			return false, err
		}
		if f.ValueType == "record_reference" {
			if err := syncReferences(ctx, tx, a, rec.ID, key, next); err != nil {
				return false, err
			}
		}
		changed = true
	}
	return changed, nil
}

func attributeChanged(ctx context.Context, tx pgx.Tx, a *access, id, key string, prev, next any, had bool, source string) error {
	asJSON := func(v any, ok bool) *string {
		if !ok {
			return nil
		}
		b, _ := json.Marshal(v)
		s := string(b)
		return &s
	}
	old, now := asJSON(prev, had), asJSON(next, next != nil)
	if _, err := tx.Exec(ctx, `INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source, actor_id, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, a.WorkspaceID, id, key, old, now, source, nullIfEmpty(a.ActorID), RequestID(ctx)); err != nil {
		return err
	}
	return customers.FieldChanged(ctx, tx, a.WorkspaceID, customers.FieldChange{RecordID: id, Field: key, Old: prev, HadOld: had, Source: source})
}

// syncReferences keeps a record reference field's links in
// record_relationships, so references and links are one graph.
func syncReferences(ctx context.Context, tx pgx.Tx, a *access, id, key string, value any) error {
	ids := []string{}
	switch v := value.(type) {
	case string:
		ids = append(ids, v)
	case []any:
		for _, x := range v {
			ids = append(ids, x.(string))
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE record_relationships SET valid_to = now()
		WHERE from_id = $1 AND field_key = $2 AND valid_to IS NULL AND NOT (to_id = ANY($3::uuid[]))`, id, key, ids); err != nil {
		return err
	}
	for _, to := range ids {
		if _, err := tx.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, field_key, created_by)
			VALUES ($1, $2, $3, $4, $4, $5) ON CONFLICT DO NOTHING`, a.WorkspaceID, id, to, key, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
	}
	return nil
}

// ---- Identities ----

func normalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || addr.Name != "" || !strings.Contains(s[strings.LastIndex(s, "@")+1:], ".") {
		return "", errInvalid("not an email address")
	}
	return s, nil
}

func normalizePhone(s string) (string, error) {
	s = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(strings.TrimSpace(s))
	if strings.HasPrefix(s, "00") {
		s = "+" + s[2:]
	}
	if !e164.MatchString(s) {
		return "", errInvalid("not a phone number")
	}
	return s, nil
}

func normalizeDomain(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if host, _, ok := strings.Cut(s, ":"); ok {
		s = host
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "www."), ".")
	if len(s) > 253 || !domainShape.MatchString(s) {
		return "", errInvalid("not a domain")
	}
	return s, nil
}

type identityInput struct {
	Namespace  string         `json:"namespace"`
	Value      string         `json:"value"`
	Verified   *bool          `json:"verified"`
	Properties map[string]any `json:"properties"`
	Source     string         `json:"source"`
	// The id of an identity of the same namespace this one replaces, such
	// as the old email when a person changes theirs. It is closed as
	// changed and kept in the history.
	Replaces *string `json:"replaces"`
}

// normalize checks the identity and puts its value in stored form.
func (in *identityInput) normalize() error {
	in.Namespace = strings.ToLower(strings.TrimSpace(in.Namespace))
	if !eventName.MatchString(in.Namespace) || len(in.Namespace) > 60 {
		return errInvalid("namespace must be lowercase words joined by dots or underscores, such as email, user_id or stripe.customer")
	}
	in.Value = strings.TrimSpace(in.Value)
	if in.Value == "" || len(in.Value) > 500 {
		return errInvalid("value must be 1 to 500 characters")
	}
	var err error
	switch in.Namespace {
	case "email":
		if in.Value, err = normalizeEmail(in.Value); err != nil {
			return errInvalid("an email identity's value must be an email address")
		}
	case "phone":
		if in.Value, err = normalizePhone(in.Value); err != nil {
			return errInvalid("a phone identity's value must be in international format, such as +61412345678")
		}
	case "domain":
		if in.Value, err = normalizeDomain(in.Value); err != nil {
			return errInvalid("a domain identity's value must be a domain such as example.com")
		}
	case "device_token":
		if p, _ := in.Properties["platform"].(string); !slices.Contains(devicePlatforms, p) {
			return errInvalid(`a device_token identity needs properties.platform: ios, android or web`)
		}
	}
	if in.Properties == nil {
		in.Properties = map[string]any{}
	}
	if b, _ := json.Marshal(in.Properties); len(b) > 4000 {
		return errInvalid("properties must be under 4 KB")
	}
	if in.Source = strings.TrimSpace(in.Source); in.Source == "" {
		in.Source = "api"
	}
	if len(in.Source) > 60 {
		return errInvalid("source must be at most 60 characters")
	}
	if in.Verified == nil {
		// An email or phone counts only once someone proved they own it. The
		// workspace's own ids, payment customers and domains come from its
		// servers, so they are trusted as sent.
		v := in.Namespace != "email" && in.Namespace != "phone"
		in.Verified = &v
	}
	return nil
}

// putIdentity adds an identity to a record, or verifies or updates the one
// it already has. It reports the audit action it recorded, or "".
func putIdentity(ctx context.Context, tx pgx.Tx, a *access, rec *recordView, in identityInput) (identityView, string, error) {
	var replaced *identityView
	if in.Replaces != nil {
		old, err := activeIdentity(ctx, tx, rec.ID, *in.Replaces)
		if err != nil {
			return identityView{}, "", err
		}
		if old.Namespace != in.Namespace {
			return identityView{}, "", errInvalid("replaces must be an identity of the same namespace, " + in.Namespace)
		}
		if old.Value == in.Value {
			return identityView{}, "", errInvalid("replaces is this same identity")
		}
		replaced = &old
	}
	if *in.Verified {
		if err := claimFree(ctx, tx, a, rec.ID, in.Namespace, in.Value); err != nil {
			return identityView{}, "", err
		}
	}
	if replaced != nil {
		if _, err := tx.Exec(ctx, `UPDATE record_identities SET valid_to = now(), closed_reason = 'changed' WHERE id = $1`, replaced.ID); err != nil {
			return identityView{}, "", err
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+identityColumns+` FROM record_identities i
		WHERE i.record_id = $1 AND i.namespace = $2 AND i.value = $3 AND i.valid_to IS NULL FOR UPDATE`, rec.ID, in.Namespace, in.Value)
	if err != nil {
		return identityView{}, "", err
	}
	existing, err := pgx.CollectRows(rows, scanIdentity)
	if err != nil {
		return identityView{}, "", err
	}
	action := "identity.added"
	if replaced != nil {
		action = "identity.changed"
	}
	if len(existing) == 1 {
		cur := existing[0]
		upgrade := *in.Verified && !cur.Verified
		props := !sameJSON(cur.Properties, in.Properties) && len(in.Properties) > 0
		if !upgrade && !props {
			return cur, "", nil
		}
		if _, err := tx.Exec(ctx, `UPDATE record_identities SET verified = verified OR $2, verified_at = CASE WHEN $2 AND NOT verified THEN now() ELSE verified_at END,
			properties = CASE WHEN $3::jsonb = '{}' THEN properties ELSE $3::jsonb END WHERE id = $1`, cur.ID, *in.Verified, in.Properties); err != nil {
			return identityView{}, "", claimError(err, in.Namespace)
		}
		action = "identity.updated"
		if upgrade {
			action = "identity.verified"
		}
	} else if _, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, verified_at, properties, source, created_by)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $5 THEN now() END, $6, $7, $8)`,
		a.WorkspaceID, rec.ID, in.Namespace, in.Value, *in.Verified, in.Properties, in.Source, nullIfEmpty(a.ActorID)); err != nil {
		return identityView{}, "", claimError(err, in.Namespace)
	}
	rows, err = tx.Query(ctx, `SELECT `+identityColumns+` FROM record_identities i
		WHERE i.record_id = $1 AND i.namespace = $2 AND i.value = $3 AND i.valid_to IS NULL`, rec.ID, in.Namespace, in.Value)
	if err != nil {
		return identityView{}, "", err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanIdentity)
	if err != nil {
		return v, "", err
	}
	data := map[string]any{"record_id": rec.ID, "namespace": in.Namespace, "verified": v.Verified}
	if n, err := flagDuplicates(ctx, tx, a.WorkspaceID, rec.ID, in.Namespace, in.Value); err != nil {
		return v, "", err
	} else if n > 0 {
		data["possible_duplicates"] = n
	}
	if replaced != nil {
		data["replaced_id"] = replaced.ID
	}
	return v, action, record(ctx, tx, &a.principal, a.WorkspaceID, action, "identity", v.ID, data)
}

// claimFree refuses a verified identity another record already holds.
func claimFree(ctx context.Context, tx pgx.Tx, a *access, id, namespace, value string) error {
	var owner string
	var brand *string
	err := tx.QueryRow(ctx, `SELECT r.id::text, r.brand_id::text FROM record_identities i JOIN records r ON r.id = i.record_id
		WHERE i.workspace_id = $1 AND i.namespace = $2 AND i.value = $3 AND i.valid_to IS NULL AND i.verified`, a.WorkspaceID, namespace, value).Scan(&owner, &brand)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner == id) {
		return nil
	}
	if err != nil {
		return err
	}
	msg := "another record already has this verified " + namespace
	if a.sees(nil, brand) {
		msg += ": " + owner
	}
	return recordConflict{"identity_taken", msg}
}

// claimError turns a lost race for a verified value into identity_taken.
func claimError(err error, namespace string) error {
	if isUnique(err) {
		return recordConflict{"identity_taken", "another record already has this verified " + namespace}
	}
	return err
}

func activeIdentity(ctx context.Context, tx pgx.Tx, record, id string) (identityView, error) {
	if !validUUID(id) {
		return identityView{}, errNotFound
	}
	rows, err := tx.Query(ctx, `SELECT `+identityColumns+` FROM record_identities i WHERE i.id = $1 AND i.record_id = $2 FOR UPDATE`, id, record)
	if err != nil {
		return identityView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanIdentity)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err == nil && v.ValidTo != nil {
		return v, recordConflict{"conflict", "this identity is already closed"}
	}
	return v, err
}

// touch bumps a record's version after a change to it.
func touch(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE records SET version = version + 1, updated_at = now() WHERE id = $1`, id)
	return err
}

// ---- Handlers ----

const versionRequired = "agents send the record's version with every change, so one agent never overwrites another's: " +
	"read the record (get_record), then send its version"

// agentWrite reports whether a change comes from an agent: the MCP server,
// a connected AI app or an approved mission. Agents must send the version
// they read, so two of them working on one record never overwrite each
// other; people and plain API clients may leave it out.
func agentWrite(r *http.Request) bool {
	if a := accessFrom(r.Context()); a != nil && a.TokenGrantID != "" {
		return true
	}
	info := reqInfoFrom(r.Context())
	if info == nil {
		return false
	}
	switch surface(r, info) {
	case "mcp", "mission":
		return true
	}
	return false
}

type recordInput struct {
	Type    *string         `json:"type"`
	Name    *string         `json:"name"`
	BrandID json.RawMessage `json:"brand_id"`
	Origin  *string         `json:"origin"`
	// The tool or feature the record came from, such as apollo or csv.
	AcquiredVia *string                    `json:"acquired_via"`
	Source      *string                    `json:"source"`
	Attributes  map[string]json.RawMessage `json:"attributes"`
	Identities  []identityInput            `json:"identities"`
	Version     *int                       `json:"version"`
	Archived    *bool                      `json:"archived"`
}

// common checks what create and update share.
func (in *recordInput) common() (string, error) {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if len(*in.Name) > 500 {
			return "", errInvalid("name must be at most 500 characters")
		}
	}
	if in.Origin != nil && !slices.Contains(recordOrigins, *in.Origin) {
		return "", errInvalid("origin is first_party, prospect, crm_sync, import or manual")
	}
	if in.AcquiredVia != nil && *in.AcquiredVia != "" && !suppressionSource.MatchString(*in.AcquiredVia) {
		return "", errInvalid("acquired_via must be 1 to 64 lowercase letters, digits, dots, dashes or underscores, such as apollo")
	}
	source := "manual"
	if in.Source != nil {
		source = *in.Source
	}
	if !slices.Contains(sourceKinds, source) {
		return "", errInvalid("source is manual, first_party, crm_sync, enrichment or import")
	}
	return source, nil
}

// recordBrand resolves a brand_id field: a brand's id or handle, or null for
// a record every brand shares.
func (s *Server) recordBrand(ctx context.Context, a *access, raw json.RawMessage) (*string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var ref string
	if err := json.Unmarshal(raw, &ref); err != nil {
		return nil, errInvalid("brand_id is a brand's id or handle, or null")
	}
	id, err := s.brandIDFor(ctx, a.WorkspaceID, ref)
	if errors.Is(err, errNotFound) || (err == nil && !a.sees(nil, &id)) {
		return nil, errInvalid("brand_id is not a brand in this workspace")
	}
	return &id, err
}

func (s *Server) createRecord(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req recordInput
	if !decode(w, r, &req) {
		return
	}
	if req.Version != nil || req.Archived != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "version and archived are only for updates")
		return
	}
	source, err := req.common()
	if s.allocError(w, r, err) {
		return
	}
	if req.Type == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "type is required: person, account or a type from GET .../catalog/types")
		return
	}
	if len(req.Identities) > 50 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "at most 50 identities")
		return
	}
	for i := range req.Identities {
		if req.Identities[i].Replaces != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "replaces is only for adding an identity to an existing record")
			return
		}
		if err := req.Identities[i].normalize(); s.allocError(w, r, err) {
			return
		}
	}
	brand, err := s.recordBrand(ctx, a, req.BrandID)
	if err == nil {
		brand, err = brandWrite(ctx, s.pool, a, nil, brand)
	}
	if s.allocError(w, r, err) {
		return
	}
	origin := "manual"
	if req.Origin != nil {
		origin = *req.Origin
	}
	var view recordView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, false)
		if err != nil {
			return err
		}
		t := c.recordType(*req.Type)
		if t == nil || t.Archived {
			return errInvalid("type must be person, account or an active type from GET .../catalog/types")
		}
		name := ""
		if req.Name != nil {
			name = *req.Name
		}
		var id string
		via := ""
		if req.AcquiredVia != nil {
			via = *req.AcquiredVia
		}
		if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, name, origin, acquired_via, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
			a.WorkspaceID, t.Key, brand, name, origin, via, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		rec := recordView{ID: id, Attributes: map[string]any{}, AttributeSources: map[string]string{}}
		if _, err := applyAttributes(ctx, tx, c, t, a, &rec, req.Attributes, source); err != nil {
			return err
		}
		if name == "" && t.Key == "person" {
			first, _ := rec.Attributes["first_name"].(string)
			last, _ := rec.Attributes["last_name"].(string)
			name = strings.TrimSpace(first + " " + last)
		}
		if _, err := tx.Exec(ctx, `UPDATE records SET name = $2, attributes = $3, attribute_sources = $4 WHERE id = $1`,
			id, name, rec.Attributes, rec.AttributeSources); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "record.created", "record", id,
			map[string]any{"type": t.Key, "origin": origin, "identities": len(req.Identities)}); err != nil {
			return err
		}
		for _, in := range req.Identities {
			if _, _, err := putIdentity(ctx, tx, a, &rec, in); err != nil {
				return err
			}
		}
		view, err = loadRecord(ctx, tx, a, id)
		view.Skipped = rec.Skipped
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) getRecord(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var v recordView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		v, err = loadRecord(r.Context(), tx, a, r.PathValue("record"))
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateRecord(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req recordInput
	if !decode(w, r, &req) {
		return
	}
	if req.Type != nil || req.Identities != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a record's type never changes; add identities with POST .../records/{record}/identities")
		return
	}
	if req.Version == nil && agentWrite(r) {
		writeError(w, r, http.StatusBadRequest, "version_required", versionRequired)
		return
	}
	source, err := req.common()
	if s.allocError(w, r, err) {
		return
	}
	var brand *string
	if len(req.BrandID) > 0 {
		if brand, err = s.recordBrand(ctx, a, req.BrandID); s.allocError(w, r, err) {
			return
		}
		if !a.writes(nil, brand) {
			writeError(w, r, http.StatusForbidden, "forbidden", "you are limited to some brands: brand_id must be one of them")
			return
		}
	}
	var view recordView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := lockRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		if req.Version != nil && *req.Version != rec.Version {
			return recordConflict{"record_changed", fmt.Sprintf("the record changed since version %d (it is at %d): read it again, check your change still makes sense, and send it with version %d", *req.Version, rec.Version, rec.Version)}
		}
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, false)
		if err != nil {
			return err
		}
		t := c.recordType(rec.Type)
		if t == nil {
			return errNotFound
		}
		if rec.Attributes == nil {
			rec.Attributes = map[string]any{}
		}
		if rec.AttributeSources == nil {
			rec.AttributeSources = map[string]string{}
		}
		changed, err := applyAttributes(ctx, tx, c, t, a, &rec, req.Attributes, source)
		if err != nil {
			return err
		}
		data := map[string]any{"type": rec.Type}
		if req.Name != nil && *req.Name != rec.Name {
			if err := attributeChanged(ctx, tx, a, rec.ID, "name", rec.Name, *req.Name, true, source); err != nil {
				return err
			}
			rec.Name, changed = *req.Name, true
		}
		if req.Origin != nil && *req.Origin != rec.Origin {
			rec.Origin, changed = *req.Origin, true
		}
		if req.AcquiredVia != nil {
			tag, err := tx.Exec(ctx, `UPDATE records SET acquired_via = $2 WHERE id = $1 AND acquired_via <> $2`, rec.ID, *req.AcquiredVia)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				changed = true
				data["acquired_via"] = *req.AcquiredVia
			}
		}
		if len(req.BrandID) > 0 && deref(brand) != deref(rec.BrandID) {
			rec.BrandID, changed = brand, true
		}
		if changed {
			if _, err := tx.Exec(ctx, `UPDATE records SET name = $2, attributes = $3, attribute_sources = $4, origin = $5, brand_id = $6,
				version = version + 1, updated_at = now() WHERE id = $1`, rec.ID, rec.Name, rec.Attributes, rec.AttributeSources, rec.Origin, rec.BrandID); err != nil {
				return err
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "record.updated", "record", rec.ID, data); err != nil {
				return err
			}
		}
		if req.Archived != nil && *req.Archived != rec.Archived {
			action, at := "record.archived", "now()"
			if !*req.Archived {
				action, at = "record.restored", "NULL"
			}
			if _, err := tx.Exec(ctx, `UPDATE records SET archived_at = `+at+`, version = version + 1, updated_at = now() WHERE id = $1`, rec.ID); err != nil {
				return err
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "record", rec.ID, data); err != nil {
				return err
			}
		}
		view, err = loadRecord(ctx, tx, a, rec.ID)
		view.Skipped = rec.Skipped
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) listRecords(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	where := `r.workspace_id = $1 AND r.merged_into IS NULL`
	if t := q.Get("type"); t != "" {
		args = append(args, t)
		where += fmt.Sprintf(` AND r.record_type = $%d`, len(args))
	}
	if o := q.Get("origin"); o != "" {
		if !slices.Contains(recordOrigins, o) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "origin is first_party, prospect, crm_sync, import or manual")
			return
		}
		args = append(args, o)
		where += fmt.Sprintf(` AND r.origin = $%d`, len(args))
	}
	switch q.Get("archived") {
	case "", "false":
		where += ` AND r.archived_at IS NULL`
	case "true":
		where += ` AND r.archived_at IS NOT NULL`
	case "all":
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "archived is false, true or all")
		return
	}
	// People held for review stay out of the list unless asked for.
	switch q.Get("review") {
	case "", "approved":
		where += ` AND NOT r.needs_review`
	case "needs_review":
		where += ` AND r.needs_review`
	case "all":
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "review is approved, needs_review or all")
		return
	}
	if v := q.Get("acquired_via"); v != "" {
		args = append(args, strings.Split(v, ","))
		where += fmt.Sprintf(` AND r.acquired_via = ANY($%d)`, len(args))
	}
	if text := strings.ToLower(strings.TrimSpace(q.Get("q"))); text != "" {
		args = append(args, escapeLike(text))
		n := len(args)
		where += fmt.Sprintf(` AND (lower(r.name) LIKE '%%' || $%d || '%%' OR EXISTS (SELECT 1 FROM record_identities i
			WHERE i.record_id = r.id AND i.valid_to IS NULL AND lower(i.value) LIKE '%%' || $%d || '%%'))`, n, n)
	}
	where += sel.sql("r.brand_id", &args)
	if b := q.Get("before"); b != "" {
		if !validUUID(b) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "before must be a record id from next_before")
			return
		}
		args = append(args, b)
		where += fmt.Sprintf(` AND (r.created_at, r.id) < (SELECT created_at, id FROM records WHERE id = $%d AND workspace_id = $1)`, len(args))
	}
	args = append(args, limit)
	var list []recordView
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+recordColumns+` FROM records r WHERE `+where+
			fmt.Sprintf(` ORDER BY r.created_at DESC, r.id DESC LIMIT $%d`, len(args)), args...)
		if err != nil {
			return err
		}
		if list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (recordView, error) { return scanRecord(row) }); err != nil {
			return err
		}
		return withIdentities(ctx, tx, list)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// lookupRecords finds the records an identity points at: the one record
// holding it verified (owner), and any holding it unverified.
func (s *Server) lookupRecords(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	in := identityInput{Namespace: r.URL.Query().Get("namespace"), Value: r.URL.Query().Get("value"), Properties: map[string]any{"platform": "web"}}
	if err := in.normalize(); s.allocError(w, r, err) {
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, in.Namespace, in.Value}
	where := sel.sql("r.brand_id", &args)
	var list []recordView
	var owner *string
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+recordColumns+`, bool_or(i.verified) FROM records r JOIN record_identities i ON i.record_id = r.id
			WHERE i.workspace_id = $1 AND i.namespace = $2 AND i.value = $3 AND i.valid_to IS NULL`+where+`
			GROUP BY r.id ORDER BY bool_or(i.verified) DESC, r.created_at LIMIT 50`, args...)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (recordView, error) {
			var v recordView
			var verified bool
			err := row.Scan(&v.ID, &v.Type, &v.Name, &v.BrandID, &v.Origin, &v.Attributes, &v.AttributeSources, &v.Version,
				&v.MergedInto, &v.Archived, &v.CreatedAt, &v.UpdatedAt, &v.NeedsReview, &verified)
			if verified {
				owner = &v.ID
			}
			return v, err
		})
		if err != nil {
			return err
		}
		return withIdentities(ctx, tx, list)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "owner": owner})
}

type attributeChangeView struct {
	ID        int64     `json:"id"`
	Field     string    `json:"field"`
	OldValue  any       `json:"old_value"`
	NewValue  any       `json:"new_value"`
	Source    string    `json:"source"`
	ActorID   *string   `json:"actor_id"`
	RequestID string    `json:"request_id"`
	ChangedAt time.Time `json:"changed_at"`
}

func (s *Server) recordHistory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	var before int64 = math.MaxInt64
	if v := r.URL.Query().Get("before"); v != "" {
		if before, err = strconv.ParseInt(v, 10, 64); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "before must be a change id from next_before")
			return
		}
	}
	field := r.URL.Query().Get("field")
	var list []attributeChangeView
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		rec, err := loadRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, field_key, old_value, new_value, source, actor_id::text, request_id, changed_at FROM attribute_changes
			WHERE record_id = $1 AND id < $2 AND ($3 = '' OR field_key = $3) ORDER BY id DESC LIMIT $4`, rec.ID, before, field, limit)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[attributeChangeView])
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	var next *int64
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) listIdentities(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	closed := r.URL.Query().Get("closed") == "true"
	var list []identityView
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rec, err := loadRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+identityColumns+` FROM record_identities i WHERE i.record_id = $1 AND ($2 OR i.valid_to IS NULL)
			ORDER BY i.valid_to IS NOT NULL, i.namespace, i.valid_from DESC`, rec.ID, closed)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanIdentity)
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) addIdentity(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req identityInput
	if !decode(w, r, &req) {
		return
	}
	if err := req.normalize(); s.allocError(w, r, err) {
		return
	}
	var view identityView
	var action string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := lockRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		if view, action, err = putIdentity(ctx, tx, a, &rec, req); err != nil || action == "" {
			return err
		}
		return touch(ctx, tx, rec.ID)
	})
	if s.recordError(w, r, err) {
		return
	}
	status := http.StatusOK
	if action == "identity.added" || action == "identity.changed" {
		status = http.StatusCreated
	}
	writeJSON(w, status, view)
}

func (s *Server) closeIdentity(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var view identityView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := lockRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		old, err := activeIdentity(ctx, tx, rec.ID, r.PathValue("identity"))
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE record_identities i SET valid_to = now(), closed_reason = 'removed' WHERE i.id = $1 RETURNING `+identityColumns, old.ID)
		if err != nil {
			return err
		}
		if view, err = pgx.CollectExactlyOneRow(rows, scanIdentity); err != nil {
			return err
		}
		if err := touch(ctx, tx, rec.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "identity.closed", "identity", old.ID,
			map[string]any{"record_id": rec.ID, "namespace": old.Namespace})
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type recordSummary struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

type relationshipView struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	FieldKey   *string        `json:"field_key"`
	Direction  string         `json:"direction"`
	Record     recordSummary  `json:"record"`
	Properties map[string]any `json:"properties"`
	ValidFrom  time.Time      `json:"valid_from"`
	ValidTo    *time.Time     `json:"valid_to"`
}

func loadRelationships(ctx context.Context, tx pgx.Tx, a *access, id string, ended bool, only *string) ([]relationshipView, error) {
	rows, err := tx.Query(ctx, `
		SELECT l.id::text, l.kind, l.field_key, CASE WHEN l.from_id = $1 THEN 'outgoing' ELSE 'incoming' END,
		       o.id::text, o.record_type, o.name, o.brand_id::text, l.properties, l.valid_from, l.valid_to
		FROM record_relationships l JOIN records o ON o.id = CASE WHEN l.from_id = $1 THEN l.to_id ELSE l.from_id END
		WHERE (l.from_id = $1 OR l.to_id = $1) AND ($2 OR l.valid_to IS NULL) AND ($3::uuid IS NULL OR l.id = $3)
		ORDER BY l.valid_to IS NOT NULL, l.valid_from DESC, l.id`, id, ended, only)
	if err != nil {
		return nil, err
	}
	type seen struct {
		relationshipView
		brand *string
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (seen, error) {
		var v seen
		err := row.Scan(&v.ID, &v.Kind, &v.FieldKey, &v.Direction, &v.Record.ID, &v.Record.Type, &v.Record.Name, &v.brand, &v.Properties, &v.ValidFrom, &v.ValidTo)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	out := []relationshipView{}
	for _, v := range list {
		if a.sees(nil, v.brand) {
			out = append(out, v.relationshipView)
		}
	}
	return out, nil
}

func (s *Server) listRelationships(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var list []relationshipView
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rec, err := loadRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		list, err = loadRelationships(ctx, tx, a, rec.ID, r.URL.Query().Get("ended") == "true", nil)
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type relationshipInput struct {
	To         string         `json:"to"`
	Kind       string         `json:"kind"`
	Properties map[string]any `json:"properties"`
}

func (s *Server) addRelationship(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req relationshipInput
	if !decode(w, r, &req) {
		return
	}
	req.Kind = strings.TrimSpace(req.Kind)
	if !relationKind.MatchString(req.Kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind must be 2 to 63 lowercase letters, numbers and underscores, such as member_of or partner_of")
		return
	}
	if req.Properties == nil {
		req.Properties = map[string]any{}
	}
	if b, _ := json.Marshal(req.Properties); len(b) > 4000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "properties must be under 4 KB")
		return
	}
	var view relationshipView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := lockRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		to, err := loadRecord(ctx, tx, a, req.To)
		if errors.Is(err, errNotFound) {
			return errInvalid("to must be the id of a record in this workspace")
		}
		if err != nil {
			return err
		}
		if to.ID == rec.ID {
			return errInvalid("a record cannot link to itself")
		}
		if to.MergedInto != nil {
			return errInvalid("to was merged into " + *to.MergedInto + "; link that one instead")
		}
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, false)
		if err != nil {
			return err
		}
		if t := c.recordType(rec.Type); t != nil {
			if f := t.field(req.Kind); f != nil && f.ValueType == "record_reference" {
				return errInvalid(fmt.Sprintf("%s is a field: set it with PATCH .../records/{record} and attributes.%s", req.Kind, req.Kind))
			}
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, properties, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, a.WorkspaceID, rec.ID, to.ID, req.Kind, req.Properties, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			if isUnique(err) {
				return recordConflict{"conflict", "these records are already linked as " + req.Kind}
			}
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "relationship.added", "relationship", id,
			map[string]any{"from_id": rec.ID, "to_id": to.ID, "kind": req.Kind}); err != nil {
			return err
		}
		list, err := loadRelationships(ctx, tx, a, rec.ID, false, &id)
		if err == nil && len(list) == 1 {
			view = list[0]
		}
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) endRelationship(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var view relationshipView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := lockRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		id := r.PathValue("relationship")
		if !validUUID(id) {
			return errNotFound
		}
		var field *string
		var ended bool
		err = tx.QueryRow(ctx, `SELECT field_key, valid_to IS NOT NULL FROM record_relationships WHERE id = $1 AND (from_id = $2 OR to_id = $2) FOR UPDATE`,
			id, rec.ID).Scan(&field, &ended)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if field != nil {
			return errInvalid(fmt.Sprintf("this link is the %s field's value: change it with PATCH .../records/{record} and attributes.%s", *field, *field))
		}
		if ended {
			return recordConflict{"conflict", "this link has already ended"}
		}
		if _, err := tx.Exec(ctx, `UPDATE record_relationships SET valid_to = now() WHERE id = $1`, id); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "relationship.ended", "relationship", id, map[string]any{"record_id": rec.ID}); err != nil {
			return err
		}
		list, err := loadRelationships(ctx, tx, a, rec.ID, true, &id)
		if err == nil && len(list) == 1 {
			view = list[0]
		}
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}
