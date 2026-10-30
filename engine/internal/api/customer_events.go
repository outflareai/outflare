package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
)

// Product events: what people do in the workspace's own product, sent the
// way Segment sends them (track, identify, page, screen, group and alias),
// so a product already wired to Segment, RudderStack or a similar library
// can point at Outflare as is. Each event is matched to a person record:
//
//   - userId, the product's own id, is the strongest key. A userId no
//     record holds makes a new person.
//   - Without one, a verified email in identify's traits finds its owner.
//   - Otherwise anonymousId finds the person it was last linked to, or the
//     event waits unmatched until an identify links that anonymousId.
//
// Two different userIds are never merged, and an unverified email never
// matches anyone. Traits that match a field in the catalog are written with
// source first_party; the rest stay on the event and are listed in
// ignored_traits, so the caller can add the fields it wants.

func (s *Server) routesCustomerEvents(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("POST /v1/workspaces/{ws}/customer-events", s.inBrand(manage, s.ingestCustomerEvents))
	mux.HandleFunc("GET /v1/workspaces/{ws}/customer-events", s.inBrand(read, s.listCustomerEvents))
	mux.HandleFunc("GET /v1/workspaces/{ws}/records/{record}/timeline", s.inBrand(read, s.recordTimeline))
	mux.HandleFunc("GET /v1/workspaces/{ws}/records/{record}/consents", s.inBrand(read, s.listConsents))
	mux.HandleFunc("POST /v1/workspaces/{ws}/records/{record}/consents", s.inBrand(manage, s.addConsent))
}

const (
	maxEventBatch      = 500
	maxEventBody       = 5 << 20
	maxEventProperties = 32 << 10
	maxEventContext    = 8 << 10
)

var (
	eventKinds = []string{"track", "identify", "page", "screen", "group", "alias"}
	// The names calls other than track are stored under.
	kindEventNames = map[string]string{
		"identify": "profile.identified",
		"page":     "page.viewed",
		"screen":   "screen.viewed",
		"group":    "group.identified",
		"alias":    "profile.aliased",
	}
	notNameChars = regexp.MustCompile(`[^a-z0-9.]+`)
	sourceLabel  = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,59}$`)
)

// eventNameFor turns a track call's event into a catalog name: Order
// Completed becomes order_completed and Signed Up! signed_up; dotted names
// such as product.activated stay as they are.
func eventNameFor(raw string) (string, bool) {
	var b strings.Builder
	prev := rune(0)
	for _, r := range strings.TrimSpace(raw) {
		// orderCompleted reads as order_completed.
		if unicode.IsUpper(r) && unicode.IsLower(prev) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
		prev = r
	}
	s := notNameChars.ReplaceAllString(b.String(), "_")
	s = regexp.MustCompile(`_*\._*`).ReplaceAllString(s, ".")
	s = regexp.MustCompile(`\.{2,}`).ReplaceAllString(s, ".")
	s = strings.Trim(s, "_.")
	if s != "" && (s[0] < 'a' || s[0] > 'z') {
		s = "event_" + s
	}
	if len(s) > 100 {
		s = strings.TrimRight(s[:100], "_.")
	}
	return s, eventName.MatchString(s)
}

// traitField maps a Segment trait to a field key: firstName is first_name,
// and title is job_title.
func traitField(key string) string {
	var b strings.Builder
	prev := rune(0)
	for _, r := range key {
		if unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
		prev = r
	}
	k := strings.Trim(notNameChars.ReplaceAllString(strings.ReplaceAll(b.String(), ".", "_"), "_"), "_")
	switch k {
	case "title":
		return "job_title"
	case "linkedin", "linkedin_url":
		return "linkedin_url"
	}
	return k
}

// Traits that say who someone is rather than describe them.
var identityTraits = []string{"email", "email_verified", "phone", "phone_verified", "id", "user_id", "anonymous_id", "name", "address", "company", "created_at", "website", "domain"}

type eventMessage map[string]json.RawMessage

// get returns the first of keys that is set, camelCase or snake_case.
func (m eventMessage) get(keys ...string) json.RawMessage {
	for _, k := range keys {
		if v, ok := m[k]; ok && string(v) != "null" {
			return v
		}
	}
	return nil
}

// id reads an id that may be a string or a number.
func (m eventMessage) id(keys ...string) (string, error) {
	raw := m.get(keys...)
	if raw == nil {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		var n json.Number
		if json.Unmarshal(raw, &n) != nil {
			return "", errInvalid(keys[0] + " must be a string")
		}
		s = n.String()
	}
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return "", errInvalid(keys[0] + " must be at most 200 characters")
	}
	return s, nil
}

func (m eventMessage) text(keys ...string) string {
	var s string
	_ = json.Unmarshal(m.get(keys...), &s)
	return strings.TrimSpace(s)
}

func (m eventMessage) object(limit int, keys ...string) (map[string]any, error) {
	raw := m.get(keys...)
	out := map[string]any{}
	if raw == nil {
		return out, nil
	}
	if len(raw) > limit {
		return nil, errInvalid(fmt.Sprintf("%s must be under %d KB", keys[0], limit>>10))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errInvalid(keys[0] + " must be an object")
	}
	return out, nil
}

func (m eventMessage) time(keys ...string) (time.Time, bool, error) {
	s := m.text(keys...)
	if s == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return t, false, errInvalid(keys[0] + " must be an RFC 3339 time, such as 2026-10-26T09:30:00Z")
	}
	return t, true, nil
}

type eventResult struct {
	Index         int      `json:"index"`
	Status        string   `json:"status"`
	EventID       *string  `json:"event_id"`
	RecordID      *string  `json:"record_id"`
	Name          string   `json:"name,omitempty"`
	IgnoredTraits []string `json:"ignored_traits,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
	// Records merged into this person because they proved they own an email
	// those records held.
	Merged []string `json:"merged,omitempty"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ingestTotals is what the batch did, for its audit event.
type ingestTotals struct {
	created, identities, duplicates, merged int
	names                                   map[string]*seenName
}

type seenName struct {
	kind        string
	count       int
	first, last time.Time
}

// eventEnvelope is a call's events as sent: the batch's own context and
// source apply to every event in it.
type eventEnvelope struct {
	top    eventMessage
	items  []json.RawMessage
	shared map[string]any
	source string
}

// readEventEnvelope reads the body both event routes take: one event, or
// {"batch": [...]} with up to 500, under 5 MB, with an optional source
// label. It checks only the envelope; each event's content is checked on
// its own.
func (s *Server) readEventEnvelope(w http.ResponseWriter, r *http.Request) (eventEnvelope, bool) {
	var env eventEnvelope
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, r, http.StatusUnsupportedMediaType, "invalid_request", "send a JSON body with Content-Type: application/json")
		return env, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventBody))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the body must be under 5 MB: send at most 500 events at a time")
		return env, false
	}
	if err := json.Unmarshal(body, &env.top); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send one event, or {\"batch\": [...]} with up to 500")
		return env, false
	}
	if raw := env.top.get("batch"); raw != nil {
		if err := json.Unmarshal(raw, &env.items); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "batch must be a list of events")
			return env, false
		}
	} else {
		env.items = []json.RawMessage{body}
	}
	if len(env.items) == 0 || len(env.items) > maxEventBatch {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send 1 to 500 events at a time")
		return env, false
	}
	// A batch's own context and source apply to every event in it.
	if env.shared, err = env.top.object(maxEventContext, "context"); s.allocError(w, r, err) {
		return env, false
	}
	env.source = env.top.text("source")
	if env.source == "" {
		env.source = "api"
	}
	if env.source == siteSource {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source browser is kept for the website snippet's events; name your own source, such as segment or api")
		return env, false
	}
	if !sourceLabel.MatchString(env.source) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is a short lowercase label such as segment, rudderstack or api")
		return env, false
	}
	return env, true
}

// eventRejection says whether err rejects only the event it came from, and
// why: code is the API's error code, detail a more precise one where the
// event inbox keeps it (missing_id, invalid_timestamp), else code.
func eventRejection(err error) (code, detail, msg string, ok bool) {
	var bad errInvalid
	var conflict recordConflict
	var ev eventError
	switch {
	case errors.Is(err, errSharedRecord):
		return "forbidden", "other_brand", "this person belongs to a brand you are not limited to", true
	case errors.As(err, &conflict):
		return conflict.code, conflict.code, conflict.msg, true
	case errors.As(err, &ev):
		return "invalid_request", ev.code, ev.msg, true
	case errors.As(err, &bad):
		return "invalid_request", "invalid_request", bad.Error(), true
	}
	return "", "", "", false
}

// eventError is an errInvalid with a precise code for the event inbox. The
// workspace route still answers invalid_request for it.
type eventError struct{ code, msg string }

func (e eventError) Error() string { return e.msg }
func (e eventError) Unwrap() error { return errInvalid(e.msg) }

// badEvent wraps a reason an event is refused, keeping err's message.
func badEvent(code string, err error) error {
	if err == nil {
		return nil
	}
	var bad errInvalid
	if errors.As(err, &bad) {
		return eventError{code, bad.Error()}
	}
	return err
}

func (s *Server) ingestCustomerEvents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	env, ok := s.readEventEnvelope(w, r)
	if !ok {
		return
	}
	msgs := make([]eventMessage, len(env.items))
	for i, raw := range env.items {
		if err := json.Unmarshal(raw, &msgs[i]); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "batch must be a list of events")
			return
		}
	}
	shared, source := env.shared, env.source
	brand, err := s.recordBrand(ctx, a, env.top.get("brand_id", "brandId"))
	if err == nil {
		brand, err = brandWrite(ctx, s.pool, a, brand)
	}
	if s.allocError(w, r, err) {
		return
	}
	results := make([]eventResult, len(msgs))
	totals := ingestTotals{names: map[string]*seenName{}}
	now := time.Now().UTC()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, false)
		if err != nil {
			return err
		}
		for i, m := range msgs {
			res := eventResult{Index: i}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			before := totals
			err = s.ingestOne(ctx, sp, a, c, brand, source, shared, m, now, &res, &totals)
			switch code, _, msg, rejected := eventRejection(err); {
			case err == nil && res.Status == "duplicate":
				err = sp.Rollback(ctx)
			case err == nil:
				res.Status = "accepted"
				err = sp.Commit(ctx)
			case rejected:
				res = eventResult{Index: i, Status: "rejected", Error: &struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}{code, msg}}
				totals.created, totals.identities, totals.duplicates, totals.merged = before.created, before.identities, before.duplicates, before.merged
				err = sp.Rollback(ctx)
			}
			if err != nil {
				return err
			}
			results[i] = res
		}
		if err := saveEventNames(ctx, tx, a.WorkspaceID, totals.names); err != nil {
			return err
		}
		counts := map[string]any{"accepted": 0, "duplicate": 0, "rejected": 0, "events": len(msgs), "records_created": totals.created, "identities_added": totals.identities, "possible_duplicates": totals.duplicates, "records_merged": totals.merged, "names": len(totals.names)}
		for _, res := range results {
			counts[res.Status] = counts[res.Status].(int) + 1
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "customer_events.ingested", "workspace", a.WorkspaceID, counts)
	})
	if s.recordError(w, r, err) {
		return
	}
	out := map[string]any{"accepted": 0, "duplicate": 0, "rejected": 0, "results": results}
	for _, res := range results {
		out[res.Status] = out[res.Status].(int) + 1
	}
	writeJSON(w, http.StatusOK, out)
}

// saveEventNames keeps one catalog row per name, however many events had
// it. A name the workspace never defined arrives quarantined, so it drives
// nothing until someone approves it.
func saveEventNames(ctx context.Context, tx pgx.Tx, ws string, names map[string]*seenName) error {
	for name, n := range names {
		status := "quarantined"
		if n.kind != "track" {
			status = "approved"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO event_definitions (workspace_id, name, status, first_seen_at, last_seen_at, seen_count)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (workspace_id, name) DO UPDATE SET seen_count = event_definitions.seen_count + EXCLUDED.seen_count,
				last_seen_at = GREATEST(event_definitions.last_seen_at, EXCLUDED.last_seen_at),
				first_seen_at = LEAST(COALESCE(event_definitions.first_seen_at, EXCLUDED.first_seen_at), EXCLUDED.first_seen_at)`,
			ws, name, status, n.first, n.last, n.count); err != nil {
			return err
		}
	}
	return nil
}

// ingestOne stores one message and applies what it says about the person.
// An errInvalid rejects only this message.
func (s *Server) ingestOne(ctx context.Context, tx pgx.Tx, a *access, c *catalog, brand *string, source string, shared map[string]any,
	m eventMessage, now time.Time, res *eventResult, totals *ingestTotals) error {
	kind := strings.ToLower(m.text("type"))
	if !slices.Contains(eventKinds, kind) {
		return eventError{"invalid_type", "type must be track, identify, page, screen, group or alias"}
	}
	userID, err := m.id("userId", "user_id")
	if err != nil {
		return badEvent("invalid_id", err)
	}
	anonID, err := m.id("anonymousId", "anonymous_id")
	if err != nil {
		return badEvent("invalid_id", err)
	}
	if userID == "" && anonID == "" {
		return eventError{"missing_id", "every event needs userId or anonymousId"}
	}
	messageID, err := m.id("messageId", "message_id")
	if err != nil {
		return badEvent("invalid_id", err)
	}
	if messageID != "" {
		var seen bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customer_events WHERE workspace_id = $1 AND message_id = $2)`, a.WorkspaceID, messageID).Scan(&seen); err != nil {
			return err
		}
		if seen {
			res.Status = "duplicate"
			return nil
		}
	}

	// The name.
	name, rawName := kindEventNames[kind], ""
	switch kind {
	case "track":
		rawName = m.text("event")
		if rawName == "" {
			return eventError{"missing_name", "a track call needs event, such as Order Completed"}
		}
		var ok bool
		if name, ok = eventNameFor(rawName); !ok {
			return eventError{"invalid_name", "event must have letters in it, such as Order Completed or product.activated"}
		}
	case "page", "screen":
		rawName = m.text("name")
	}
	if len(rawName) > 200 {
		return eventError{"invalid_name", "the event or page name must be at most 200 characters"}
	}
	res.Name = name

	// When it happened.
	at, ok, err := m.time("timestamp", "originalTimestamp", "original_timestamp")
	if err != nil {
		return badEvent("invalid_timestamp", err)
	}
	if !ok {
		at = now
	}
	if at.After(now.Add(24 * time.Hour)) {
		return eventError{"invalid_timestamp", "timestamp is more than a day in the future"}
	}
	if at.Before(now.AddDate(0, -25, 0)) {
		return eventError{"invalid_timestamp", "timestamp is more than 25 months ago, past how long events are kept"}
	}

	props, err := m.object(maxEventProperties, "properties")
	if err != nil {
		return badEvent("invalid_properties", err)
	}
	traits, err := m.object(maxEventProperties, "traits")
	if err != nil {
		return badEvent("invalid_traits", err)
	}
	ctxObj, err := m.object(maxEventContext, "context")
	if err != nil {
		return badEvent("invalid_context", err)
	}
	for k, v := range shared {
		if _, ok := ctxObj[k]; !ok {
			ctxObj[k] = v
		}
	}
	if rawName != "" && (kind == "page" || kind == "screen") {
		props["name"] = rawName
	}

	email, emailVerified := "", false
	if kind == "identify" {
		if e, _ := traits["email"].(string); strings.TrimSpace(e) != "" {
			if email, err = normalizeEmail(e); err != nil {
				res.Warnings = append(res.Warnings, "traits.email is not an email address, so it was not added")
				email = ""
			}
			emailVerified = traits["email_verified"] == true || traits["emailVerified"] == true
		}
	}

	// Who it is.
	rec, err := s.resolvePerson(ctx, tx, a, c, brand, kind, userID, anonID, email, emailVerified, traits, res, totals)
	if err != nil {
		return err
	}
	var account *string
	if kind == "group" {
		groupID, err := m.id("groupId", "group_id")
		if err != nil {
			return badEvent("invalid_id", err)
		}
		if groupID == "" {
			return eventError{"missing_group_id", "a group call needs groupId, your product's own id for the account"}
		}
		id, err := s.resolveAccount(ctx, tx, a, c, brand, groupID, traits, rec, res, totals)
		if err != nil {
			return err
		}
		account = &id
	}
	if kind == "alias" {
		prev, err := m.id("previousId", "previous_id")
		if err != nil {
			return badEvent("invalid_id", err)
		}
		if prev == "" {
			return eventError{"missing_previous_id", "an alias call needs previousId"}
		}
		if rec != nil {
			if err := s.aliasPerson(ctx, tx, a, c, *rec, prev, res, totals); err != nil {
				return err
			}
		}
	}
	if kind == "identify" && rec != nil {
		if err := applyTraits(ctx, tx, c, a, *rec, "person", traits, res); err != nil {
			return err
		}
	}

	var id string
	err = tx.QueryRow(ctx, `INSERT INTO customer_events (workspace_id, record_id, account_id, brand_id, kind, name, raw_name, user_id, anonymous_id,
			properties, traits, context, source, message_id, occurred_at, request_id)
		VALUES ($1, $2, $3, COALESCE($4, (SELECT r.brand_id FROM records r WHERE r.id = $2), outflare_first_brand($1)), $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING id::text`,
		a.WorkspaceID, rec, account, brand, kind, name, rawName, nullIfEmpty(userID), nullIfEmpty(anonID),
		props, traits, ctxObj, source, nullIfEmpty(messageID), at, RequestID(ctx)).Scan(&id)
	if isUnique(err) {
		res.Status = "duplicate"
		return nil
	}
	if err != nil {
		return err
	}
	res.EventID, res.RecordID = &id, rec
	// Anyone can send a browser event, so one never starts a journey.
	if rec != nil && source != siteSource {
		if err := customers.EventAdded(ctx, tx, a.WorkspaceID, id, name); err != nil {
			return err
		}
	}
	n := totals.names[name]
	if n == nil {
		n = &seenName{kind: kind, first: at, last: at}
		totals.names[name] = n
	}
	n.count++
	if at.Before(n.first) {
		n.first = at
	}
	if at.After(n.last) {
		n.last = at
	}
	return nil
}

// ownerOf is the record holding an identity verified, if the caller can
// see it.
func ownerOf(ctx context.Context, tx pgx.Tx, a *access, namespace, value string) (*string, *string, error) {
	var id string
	var brand *string
	err := tx.QueryRow(ctx, `SELECT r.id::text, r.brand_id::text FROM record_identities i JOIN records r ON r.id = i.record_id
		WHERE i.workspace_id = $1 AND i.namespace = $2 AND i.value = $3 AND i.valid_to IS NULL AND i.verified AND r.merged_into IS NULL`,
		a.WorkspaceID, namespace, value).Scan(&id, &brand)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !a.writes(brand) {
		return nil, nil, errSharedRecord
	}
	return &id, brand, nil
}

// attach gives a record an identity the ingest trusts. It reports whether
// it added one, and leaves an identity another record holds verified where
// it is.
func attach(ctx context.Context, tx pgx.Tx, a *access, rec, namespace, value string, verified bool, totals *ingestTotals) (bool, error) {
	if verified {
		var other string
		err := tx.QueryRow(ctx, `SELECT record_id::text FROM record_identities WHERE workspace_id = $1 AND namespace = $2 AND value = $3
			AND valid_to IS NULL AND verified`, a.WorkspaceID, namespace, value).Scan(&other)
		if err == nil && other != rec {
			return false, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, verified_at, source, created_by)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $5 THEN now() END, 'events', $6)
		ON CONFLICT (record_id, namespace, value) WHERE valid_to IS NULL
		DO UPDATE SET verified = true, verified_at = COALESCE(record_identities.verified_at, now())
		WHERE $5 AND NOT record_identities.verified`,
		a.WorkspaceID, rec, namespace, value, verified, nullIfEmpty(a.ActorID))
	if err != nil {
		return false, claimError(err, namespace)
	}
	if tag.RowsAffected() > 0 {
		totals.identities++
		n, err := flagDuplicates(ctx, tx, a.WorkspaceID, rec, namespace, value)
		if err != nil {
			return false, err
		}
		totals.duplicates += n
		if _, err := tx.Exec(ctx, `UPDATE records SET version = version + 1, updated_at = now() WHERE id = $1`, rec); err != nil {
			return false, err
		}
	}
	return tag.RowsAffected() > 0, nil
}

func hasUserID(ctx context.Context, tx pgx.Tx, rec string) (bool, error) {
	var yes bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM record_identities WHERE record_id = $1 AND namespace = 'user_id' AND valid_to IS NULL)`, rec).Scan(&yes)
	return yes, err
}

// resolvePerson finds or makes the person a message is about, or returns
// nil for an anonymous visitor nobody has identified yet.
func (s *Server) resolvePerson(ctx context.Context, tx pgx.Tx, a *access, c *catalog, brand *string, kind, userID, anonID, email string, emailVerified bool,
	traits map[string]any, res *eventResult, totals *ingestTotals) (*string, error) {
	var rec *string
	var err error
	if userID != "" {
		if rec, _, err = ownerOf(ctx, tx, a, "user_id", userID); err != nil {
			return nil, err
		}
	}
	if rec == nil && email != "" && emailVerified {
		if rec, _, err = ownerOf(ctx, tx, a, "email", email); err != nil {
			return nil, err
		}
		// An email someone proved belongs to the record that holds it, unless
		// that record is another user id's: two users are never merged.
		if rec != nil && userID != "" {
			if taken, err := hasUserID(ctx, tx, *rec); err != nil {
				return nil, err
			} else if taken {
				rec = nil
			}
		}
	}
	if rec == nil && anonID != "" {
		if rec, _, err = ownerOf(ctx, tx, a, "anonymous_id", anonID); err != nil {
			return nil, err
		}
		// A visitor who was identified as one user and now signs in as
		// another is two people on one device.
		if rec != nil && userID != "" {
			if taken, err := hasUserID(ctx, tx, *rec); err != nil {
				return nil, err
			} else if taken {
				rec = nil
			}
		}
		// A browser known only from an email click is not proof: whoever
		// signs in on it as someone else takes it.
		if rec != nil {
			if released, err := releaseClickBrowser(ctx, tx, a, c, *rec, anonID, userID, email); err != nil {
				return nil, err
			} else if released {
				rec = nil
			}
		}
	}
	// A new person: anyone with a userId, or an anonymous visitor who
	// identified themselves with traits, such as a newsletter sign-up.
	if rec == nil && (userID != "" || (kind == "identify" && len(traits) > 0)) {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, origin, created_by) VALUES ($1, 'person', COALESCE($2, outflare_first_brand($1)), 'first_party', $3) RETURNING id::text`,
			a.WorkspaceID, brand, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return nil, err
		}
		totals.created++
		rec = &id
	}
	if rec == nil {
		return nil, nil
	}
	if userID != "" {
		if _, err := attach(ctx, tx, a, *rec, "user_id", userID, true, totals); err != nil {
			return nil, err
		}
	}
	if anonID != "" {
		if other, _, err := ownerOf(ctx, tx, a, "anonymous_id", anonID); err != nil {
			return nil, err
		} else if other != nil && *other != *rec {
			if _, err := releaseClickBrowser(ctx, tx, a, c, *other, anonID, userID, email); err != nil {
				return nil, err
			}
		}
		added, err := attach(ctx, tx, a, *rec, "anonymous_id", anonID, true, totals)
		if err != nil {
			return nil, err
		}
		if added {
			if err := backfill(ctx, tx, a, c, *rec, anonID); err != nil {
				return nil, err
			}
		}
	}
	if email != "" {
		if added, err := attach(ctx, tx, a, *rec, "email", email, emailVerified, totals); err != nil {
			return nil, err
		} else if !added && emailVerified {
			owner, _, err := ownerOf(ctx, tx, a, "email", email)
			if err != nil {
				return nil, err
			}
			if owner != nil && *owner != *rec {
				if err := s.claimProvenEmail(ctx, tx, a, c, *rec, *owner, email, res, totals); err != nil {
					return nil, err
				}
			}
		}
	}
	if p, _ := traits["phone"].(string); p != "" && kind == "identify" {
		if phone, err := normalizePhone(p); err != nil {
			res.Warnings = append(res.Warnings, "traits.phone is not in international format, so it was not added")
		} else if _, err := attach(ctx, tx, a, *rec, "phone", phone, traits["phone_verified"] == true || traits["phoneVerified"] == true, totals); err != nil {
			return nil, err
		}
	}
	return rec, nil
}

// claimProvenEmail settles an email the person just proved they own that
// another record holds verified. A record with no user id of its own (an
// import, a CRM sync, a form) is the same person and is merged in; one with
// its own user id is another account in the product, so the two are only
// listed as a possible duplicate.
func (s *Server) claimProvenEmail(ctx context.Context, tx pgx.Tx, a *access, c *catalog, rec, owner, email string, res *eventResult, totals *ingestTotals) error {
	// Brands keep their data apart: a person of another brand is someone
	// else there, never merged or listed with this one.
	var same bool
	if err := tx.QueryRow(ctx, `SELECT x.brand_id IS NOT DISTINCT FROM y.brand_id FROM records x, records y WHERE x.id = $1 AND y.id = $2`,
		rec, owner).Scan(&same); err != nil {
		return err
	}
	if !same {
		res.Warnings = append(res.Warnings, "the verified email belongs to a person in another brand; brands keep their data apart, so they were not merged")
		return nil
	}
	ids, err := userIDs(ctx, tx, owner)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		res.Warnings = append(res.Warnings, "the verified email belongs to a person with another userId; they were not merged, and are listed as a possible duplicate")
		n, err := flagPair(ctx, tx, a.WorkspaceID, rec, owner, "email", true)
		totals.duplicates += n
		return err
	}
	if _, err := mergeRecords(ctx, tx, a, c, rec, owner); err != nil {
		return err
	}
	res.Merged = append(res.Merged, owner)
	totals.merged++
	_, err = attach(ctx, tx, a, rec, "email", email, true, totals)
	return err
}

// releaseClickBrowser takes a browser back from the person it joined
// through an email click when a sign-in on it says it is someone else: a
// userId without the person's email, or an email they don't have. A
// forwarded email then never puts two people in one record. The browser's events and visits leave the
// person, so the new owner's backfill picks them up. It reports whether it
// let go.
func releaseClickBrowser(ctx context.Context, tx pgx.Tx, a *access, c *catalog, rec, anonID, userID, email string) (bool, error) {
	if userID == "" && email == "" {
		return false, nil
	}
	var fromClick, hasEmail bool
	if err := tx.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM record_identities WHERE record_id = $1 AND namespace = 'anonymous_id' AND value = $2 AND valid_to IS NULL AND source = 'email_click'),
			EXISTS (SELECT 1 FROM record_identities WHERE record_id = $1 AND namespace = 'email' AND value = $3 AND valid_to IS NULL)`,
		rec, anonID, email).Scan(&fromClick, &hasEmail); err != nil {
		return false, err
	}
	if !fromClick || (email != "" && hasEmail) {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE record_identities SET valid_to = now() WHERE record_id = $1 AND namespace = 'anonymous_id' AND value = $2 AND valid_to IS NULL`,
		rec, anonID); err != nil {
		return false, err
	}
	for _, t := range []string{"customer_events", "site_sessions", "consents"} {
		if _, err := tx.Exec(ctx, `UPDATE `+t+` SET record_id = NULL WHERE workspace_id = $1 AND anonymous_id = $2 AND record_id = $3`,
			a.WorkspaceID, anonID, rec); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE site_visitors SET record_id = NULL, identified_by = '' WHERE workspace_id = $1 AND anonymous_id = $2 AND record_id = $3`,
		a.WorkspaceID, anonID, rec); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET version = version + 1, updated_at = now() WHERE id = $1`, rec); err != nil {
		return false, err
	}
	return true, recomputeTouches(ctx, tx, a, c, rec)
}

// backfill gives a person the events, website visits and consent choices
// their anonymousId had before anyone knew who they were, and works out
// their first and last touch from those visits.
func backfill(ctx context.Context, tx pgx.Tx, a *access, c *catalog, rec, anonID string) error {
	if _, err := tx.Exec(ctx, `UPDATE customer_events SET record_id = $3 WHERE workspace_id = $1 AND anonymous_id = $2 AND record_id IS NULL`,
		a.WorkspaceID, anonID, rec); err != nil {
		return err
	}
	// Choices made on the site's consent banner.
	if _, err := tx.Exec(ctx, `UPDATE consents SET record_id = $3 WHERE workspace_id = $1 AND anonymous_id = $2 AND record_id IS NULL`,
		a.WorkspaceID, anonID, rec); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE site_sessions SET record_id = $3 WHERE workspace_id = $1 AND anonymous_id = $2 AND record_id IS NULL`,
		a.WorkspaceID, anonID, rec)
	if err != nil {
		return err
	}
	// Their website visits too.
	if _, err := tx.Exec(ctx, `UPDATE site_visitors SET record_id = $3, identified_by = CASE WHEN identified_by IN ('', 'company_ip') THEN 'product' ELSE identified_by END
		WHERE workspace_id = $1 AND anonymous_id = $2 AND record_id IS NULL`, a.WorkspaceID, anonID, rec); err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	return recomputeTouches(ctx, tx, a, c, rec)
}

// aliasPerson links a previous id to the person. An earlier userId stays
// with whoever holds it: two users are never merged.
func (s *Server) aliasPerson(ctx context.Context, tx pgx.Tx, a *access, c *catalog, rec, prev string, res *eventResult, totals *ingestTotals) error {
	owner, _, err := ownerOf(ctx, tx, a, "user_id", prev)
	if err != nil {
		return err
	}
	if owner != nil {
		if *owner != rec {
			res.Warnings = append(res.Warnings, "previousId is another person's userId; they were not merged")
		}
		return nil
	}
	if other, _, err := ownerOf(ctx, tx, a, "anonymous_id", prev); err != nil {
		return err
	} else if other != nil && *other != rec {
		res.Warnings = append(res.Warnings, "previousId already belongs to another person; it was left there")
		return nil
	}
	added, err := attach(ctx, tx, a, rec, "anonymous_id", prev, true, totals)
	if err == nil && added {
		err = backfill(ctx, tx, a, c, rec, prev)
	}
	return err
}

// resolveAccount finds or makes the company a group call names, applies
// its traits and adds it to the person's companies.
func (s *Server) resolveAccount(ctx context.Context, tx pgx.Tx, a *access, c *catalog, brand *string, groupID string, traits map[string]any,
	person *string, res *eventResult, totals *ingestTotals) (string, error) {
	acct, _, err := ownerOf(ctx, tx, a, "group_id", groupID)
	if err != nil {
		return "", err
	}
	if acct == nil {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, origin, created_by) VALUES ($1, 'account', COALESCE($2, outflare_first_brand($1)), 'first_party', $3) RETURNING id::text`,
			a.WorkspaceID, brand, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return "", err
		}
		totals.created++
		acct = &id
		if _, err := attach(ctx, tx, a, id, "group_id", groupID, true, totals); err != nil {
			return "", err
		}
	}
	for _, k := range []string{"website", "domain"} {
		if v, _ := traits[k].(string); v != "" {
			if d, err := normalizeDomain(v); err == nil {
				if _, err := attach(ctx, tx, a, *acct, "domain", d, true, totals); err != nil {
					return "", err
				}
			}
		}
	}
	if err := applyTraits(ctx, tx, c, a, *acct, "account", traits, res); err != nil {
		return "", err
	}
	if person == nil {
		return *acct, nil
	}
	rec, err := lockRecord(ctx, tx, a, *person)
	if err != nil {
		return "", err
	}
	list := []any{}
	if cur, ok := rec.Attributes["companies"].([]any); ok {
		list = cur
	}
	if !slices.Contains(list, any(*acct)) {
		t := c.recordType("person")
		raw, _ := json.Marshal(append(list, *acct))
		if rec.Attributes == nil {
			rec.Attributes = map[string]any{}
		}
		if rec.AttributeSources == nil {
			rec.AttributeSources = map[string]string{}
		}
		changed, err := applyAttributes(ctx, tx, c, t, a, &rec, map[string]json.RawMessage{"companies": raw}, "first_party")
		if err != nil {
			return "", err
		}
		if changed {
			if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
				rec.ID, rec.Attributes, rec.AttributeSources); err != nil {
				return "", err
			}
		}
	}
	return *acct, nil
}

// applyTraits writes the traits that match the record type's fields, with
// source first_party, and lists the rest in ignored_traits. A trait with a
// value its field refuses is ignored too, so one bad trait never loses the
// event.
func applyTraits(ctx context.Context, tx pgx.Tx, c *catalog, a *access, id, typ string, traits map[string]any, res *eventResult) error {
	if len(traits) == 0 {
		return nil
	}
	rec, err := lockRecord(ctx, tx, a, id)
	if err != nil {
		return err
	}
	t := c.recordType(typ)
	if rec.Attributes == nil {
		rec.Attributes = map[string]any{}
	}
	if rec.AttributeSources == nil {
		rec.AttributeSources = map[string]string{}
	}
	flat := map[string]any{}
	for k, v := range traits {
		flat[k] = v
	}
	// Segment's address object holds city and country.
	if addr, ok := traits["address"].(map[string]any); ok {
		for _, k := range []string{"city", "country"} {
			if v, ok := addr[k]; ok {
				if _, set := flat[k]; !set {
					flat[k] = v
				}
			}
		}
	}
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	changed := false
	var ignored []string
	for _, k := range keys {
		key := traitField(k)
		if slices.Contains(identityTraits, key) {
			continue
		}
		f := t.field(key)
		if f == nil || f.Archived || f.WritePolicy == "computed" || f.ValueType == "record_reference" {
			ignored = append(ignored, k)
			continue
		}
		raw, _ := json.Marshal(flat[k])
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		ok, err := applyAttributes(ctx, sp, c, t, a, &rec, map[string]json.RawMessage{key: raw}, "first_party")
		var bad errInvalid
		if errors.As(err, &bad) {
			ignored = append(ignored, k)
			if err := sp.Rollback(ctx); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		changed = changed || ok
	}
	// The name: name, or first and last name for a person.
	name, _ := traits["name"].(string)
	if name = strings.TrimSpace(name); name == "" && typ == "person" {
		first, _ := rec.Attributes["first_name"].(string)
		last, _ := rec.Attributes["last_name"].(string)
		name = strings.TrimSpace(first + " " + last)
	}
	if len(name) > 500 {
		name = name[:500]
	}
	if name != "" && name != rec.Name {
		if err := attributeChanged(ctx, tx, a, rec.ID, "name", rec.Name, name, true, "first_party"); err != nil {
			return err
		}
		rec.Name, changed = name, true
	}
	if changed {
		if _, err := tx.Exec(ctx, `UPDATE records SET name = $2, attributes = $3, attribute_sources = $4, version = version + 1, updated_at = now() WHERE id = $1`,
			rec.ID, rec.Name, rec.Attributes, rec.AttributeSources); err != nil {
			return err
		}
	}
	res.IgnoredTraits = append(res.IgnoredTraits, ignored...)
	return nil
}

// ---- Reading ----

type customerEventView struct {
	ID          string         `json:"id"`
	RecordID    *string        `json:"record_id"`
	AccountID   *string        `json:"account_id"`
	BrandID     *string        `json:"brand_id"`
	Kind        string         `json:"kind"`
	Name        string         `json:"name"`
	RawName     string         `json:"raw_name"`
	UserID      *string        `json:"user_id"`
	AnonymousID *string        `json:"anonymous_id"`
	Properties  map[string]any `json:"properties"`
	Traits      map[string]any `json:"traits"`
	Context     map[string]any `json:"context"`
	Source      string         `json:"source"`
	MessageID   *string        `json:"message_id"`
	OccurredAt  time.Time      `json:"occurred_at"`
	ReceivedAt  time.Time      `json:"received_at"`
}

const customerEventColumns = `e.id::text, e.record_id::text, e.account_id::text, e.brand_id::text, e.kind, e.name, e.raw_name, e.user_id, e.anonymous_id,
	e.properties, e.traits, e.context, e.source, e.message_id, e.occurred_at, e.received_at`

// timeCursor pages newest first by time, then id: "<RFC 3339>|<id>".
func timeCursor(v string) (time.Time, string, error) {
	if v == "" {
		return time.Now().Add(100 * 365 * 24 * time.Hour), "", nil
	}
	ts, id, _ := strings.Cut(v, "|")
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return t, "", errInvalid("before must be next_before from the previous page")
	}
	return t, id, nil
}

func (s *Server) listCustomerEvents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	beforeAt, beforeID, err := timeCursor(q.Get("before"))
	if s.allocError(w, r, err) {
		return
	}
	where := []string{"e.workspace_id = $1", "(e.occurred_at, e.id::text) < ($2, $3)"}
	args := []any{a.WorkspaceID, beforeAt, beforeID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if v := q.Get("name"); v != "" {
		add("e.name = ?", v)
	}
	if v := q.Get("kind"); v != "" {
		if !slices.Contains(eventKinds, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is track, identify, page, screen, group or alias")
			return
		}
		add("e.kind = ?", v)
	}
	if v := q.Get("user_id"); v != "" {
		add("e.user_id = ?", v)
	}
	if v := q.Get("anonymous_id"); v != "" {
		add("e.anonymous_id = ?", v)
	}
	switch v := q.Get("matched"); v {
	case "", "all":
	case "true":
		where = append(where, "e.record_id IS NOT NULL")
	case "false":
		where = append(where, "e.record_id IS NULL")
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "matched is true, false or all")
		return
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "since must be an RFC 3339 time")
			return
		}
		add("e.occurred_at >= ?", t)
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	brandCond := sel.sql("e.brand_id", &args)
	args = append(args, limit)
	var list []customerEventView
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+customerEventColumns+` FROM customer_events e WHERE `+strings.Join(where, " AND ")+brandCond+`
			ORDER BY e.occurred_at DESC, e.id::text DESC LIMIT $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[customerEventView])
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	var next *string
	if len(list) == limit {
		last := list[len(list)-1]
		v := last.OccurredAt.Format(time.RFC3339Nano) + "|" + last.ID
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// timelineItem is one thing that happened to a record.
type timelineItem struct {
	Kind string    `json:"kind"`
	ID   string    `json:"id"`
	At   time.Time `json:"at"`
	// What happened, by kind: the event's name, the field that changed, the
	// identity's namespace, the consent's channel, the email's subject, the
	// visit's landing page, the opt-out's reason or the journey's name.
	Title  string         `json:"title"`
	Detail map[string]any `json:"detail"`
	Source string         `json:"source"`
	// Whose it is: the record itself, or one of a company's people.
	RecordID string `json:"record_id"`
}

var timelineKinds = []string{"event", "attribute_change", "identity", "consent", "message", "meeting", "deal", "visit", "opt_out", "journey"}

// timelinePeople caps how many of a company's people its timeline reads.
const timelinePeople = 5000

// timelineBranches is one query per kind, each returning kind, id, at,
// title, detail, source and record_id for the records in $2. $1 is the
// workspace and $7 the brand condition's arguments start; {brand:col}
// marks where a brand condition goes.
var timelineBranches = map[string]string{
	"event": `SELECT 'event', e.id::text, e.occurred_at, e.name,
			jsonb_build_object('event_kind', e.kind, 'raw_name', e.raw_name, 'properties', e.properties, 'brand_id', e.brand_id), e.source,
			CASE WHEN e.record_id = ANY($2) THEN e.record_id ELSE e.account_id END
		FROM customer_events e WHERE e.workspace_id = $1 AND (e.record_id = ANY($2) OR e.account_id = ANY($2)) {brand:e.brand_id}`,
	"attribute_change": `SELECT 'attribute_change', c.id::text, c.changed_at, c.field_key,
			jsonb_build_object('old_value', c.old_value, 'new_value', c.new_value, 'actor_id', c.actor_id), c.source, c.record_id
		FROM attribute_changes c WHERE c.record_id = ANY($2)`,
	"identity": `SELECT 'identity', i.id::text || ':added', i.valid_from, i.namespace,
			jsonb_build_object('change', 'added', 'value', i.value, 'verified', i.verified), i.source, i.record_id
		FROM record_identities i WHERE i.record_id = ANY($2)
		UNION ALL
		SELECT 'identity', i.id::text || ':closed', i.valid_to, i.namespace,
			jsonb_build_object('change', i.closed_reason, 'value', i.value, 'verified', i.verified), i.source, i.record_id
		FROM record_identities i WHERE i.record_id = ANY($2) AND i.valid_to IS NOT NULL`,
	"consent": `SELECT 'consent', k.id::text, k.occurred_at, k.channel,
			jsonb_build_object('status', k.status, 'topic', k.topic, 'purpose', k.purpose, 'evidence', k.evidence), k.source, k.record_id
		FROM consents k WHERE k.record_id = ANY($2)`,
	"message": `SELECT 'message', m.id::text, COALESCE(m.sent_at, m.created_at), COALESCE(m.subject, ''),
			jsonb_build_object('direction', 'out', 'status', m.status, 'sequence_id', m.sequence_id, 'step', m.step_position,
				'mailbox_id', m.mailbox_id, 'lead_id', m.lead_id), 'outreach', m.record_id
		FROM messages m LEFT JOIN sequences q ON q.id = m.sequence_id
		WHERE m.workspace_id = $1 AND m.record_id = ANY($2) {brand:q.brand_id}
		UNION ALL
		SELECT 'message', n.id::text, n.received_at, COALESCE(n.subject, ''),
			jsonb_build_object('direction', 'in', 'kind', n.kind, 'category', n.category, 'sequence_id', n.sequence_id,
				'mailbox_id', n.mailbox_id, 'lead_id', n.lead_id), 'inbox', n.record_id
		FROM inbound_messages n LEFT JOIN mailboxes x ON x.id = n.mailbox_id
		WHERE n.workspace_id = $1 AND n.record_id = ANY($2) {brand:x.brand_id}`,
	"meeting": `SELECT 'meeting', g.id::text, COALESCE(g.booked_at, g.created_at), COALESCE(g.title, ''),
			jsonb_build_object('status', g.status, 'starts_at', g.starts_at, 'sequence_id', g.sequence_id, 'qualified', g.qualified), g.source, g.record_id
		FROM meetings g LEFT JOIN sequences q ON q.id = g.sequence_id
		WHERE g.workspace_id = $1 AND g.record_id = ANY($2) {brand:q.brand_id}`,
	"deal": `SELECT 'deal', d.id::text, COALESCE(d.opened_at, d.created_at), COALESCE(d.name, ''),
			jsonb_build_object('stage', d.stage, 'state', d.state, 'amount_cents', d.amount_cents, 'currency', d.currency,
				'sequence_id', d.sequence_id, 'meeting_id', d.meeting_id), d.source,
			COALESCE(CASE WHEN d.record_id = ANY($2) THEN d.record_id END, l.to_id, d.id)
		FROM deals d LEFT JOIN sequences q ON q.id = d.sequence_id
		LEFT JOIN LATERAL (SELECT rr.to_id FROM record_relationships rr
			WHERE rr.from_id = d.id AND rr.to_id = ANY($2) AND rr.valid_to IS NULL LIMIT 1) l ON true
		WHERE d.workspace_id = $1 AND (d.record_id = ANY($2) OR d.id = ANY($2) OR l.to_id IS NOT NULL) {brand:q.brand_id}`,
	"visit": `SELECT 'visit', v.id::text, v.started_at, v.landing_path,
			jsonb_build_object('ended_at', v.last_seen_at, 'pageviews', v.pageviews, 'events', v.events, 'referrer_host', v.referrer_host,
				'utm_source', v.utm_source, 'utm_medium', v.utm_medium, 'utm_campaign', v.utm_campaign, 'country', v.country,
				'device', v.device, 'brand_id', v.brand_id), 'website', v.record_id
		FROM site_sessions v WHERE v.workspace_id = $1 AND v.record_id = ANY($2) {brand:v.brand_id}`,
	"opt_out": `SELECT 'opt_out', o.id::text, o.created_at, o.reason,
			jsonb_build_object('kind', o.kind, 'value', o.value, 'method', o.method, 'added_by_type', o.added_by_type,
				'integration_id', o.integration_id, 'confidence', o.confidence), o.source, o.record_id
		FROM suppressions o WHERE o.workspace_id = $1 AND o.record_id = ANY($2)`,
	"journey": `SELECT 'journey', j.id::text || ':entered', j.entered_at, y.name,
			jsonb_build_object('change', 'entered', 'journey_id', j.journey_id, 'status', j.status), 'journey', j.record_id
		FROM journey_runs j JOIN journeys y ON y.id = j.journey_id
		WHERE j.workspace_id = $1 AND j.record_id = ANY($2) {brand:y.brand_id}
		UNION ALL
		SELECT 'journey', j.id::text || ':finished', j.finished_at, y.name,
			jsonb_build_object('change', j.status, 'journey_id', j.journey_id, 'status', j.status, 'exit_reason', j.exit_reason), 'journey', j.record_id
		FROM journey_runs j JOIN journeys y ON y.id = j.journey_id
		WHERE j.workspace_id = $1 AND j.record_id = ANY($2) AND j.finished_at IS NOT NULL {brand:y.brand_id}`,
}

// timelineBrand is the brand condition on col: items for the chosen brand
// (?brand=) and items no brand owns, within the brands the caller sees.
func timelineBrand(sel brandSel, col string, args *[]any) string {
	out := ""
	switch sel.id {
	case "":
	case "none":
		out = ` AND ` + col + ` IS NULL`
	default:
		*args = append(*args, sel.id)
		out = fmt.Sprintf(` AND (%s IS NULL OR %s = $%d)`, col, col, len(*args))
	}
	if sel.in != nil {
		*args = append(*args, sel.in)
		out += fmt.Sprintf(` AND (%s IS NULL OR %s = ANY($%d::uuid[]))`, col, col, len(*args))
	}
	return out
}

var timelineBrandMark = regexp.MustCompile(`\{brand:([a-z_.]+)\}`)

func (s *Server) recordTimeline(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	beforeAt, beforeID, err := timeCursor(q.Get("before"))
	if s.allocError(w, r, err) {
		return
	}
	kinds := timelineKinds
	if v := q.Get("kinds"); v != "" {
		kinds = strings.Split(v, ",")
		for _, k := range kinds {
			if !slices.Contains(timelineKinds, k) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "kinds is a comma-separated list of "+strings.Join(timelineKinds, ", "))
				return
			}
		}
	}
	people := true
	switch q.Get("people") {
	case "", "true":
	case "false":
		people = false
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "people is true or false")
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	var list []timelineItem
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		rec, err := loadRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		subjects := []string{rec.ID}
		// A company's timeline is its own and its people's: the people
		// linked to it, by a reference field or a relationship.
		if rec.Type == "account" && people {
			rows, err := tx.Query(ctx, `SELECT DISTINCT p.id::text FROM record_relationships l
				JOIN records p ON p.id = CASE WHEN l.from_id = $1 THEN l.to_id ELSE l.from_id END
				WHERE (l.from_id = $1 OR l.to_id = $1) AND l.valid_to IS NULL AND l.workspace_id = $2
					AND p.workspace_id = $2 AND p.record_type = 'person' LIMIT $3`, rec.ID, a.WorkspaceID, timelinePeople)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			subjects = append(subjects, ids...)
		}
		args := []any{a.WorkspaceID, subjects, beforeAt, beforeID, limit}
		var parts []string
		for _, k := range kinds {
			if slices.Contains(kinds[:slices.Index(kinds, k)], k) {
				continue
			}
			branch := timelineBrandMark.ReplaceAllStringFunc(timelineBranches[k], func(m string) string {
				return timelineBrand(sel, timelineBrandMark.FindStringSubmatch(m)[1], &args)
			})
			// Each kind pages on its own, so a long history reads only
			// what one page needs.
			parts = append(parts, `(SELECT * FROM (`+branch+`) b (kind, id, at, title, detail, source, record_id)
				WHERE (at, id) < ($3, $4) ORDER BY at DESC, id DESC LIMIT $5)`)
		}
		rows, err := tx.Query(ctx, `SELECT kind, id, at, title, detail, source, record_id::text FROM (`+strings.Join(parts, " UNION ALL ")+`) t
			ORDER BY at DESC, id DESC LIMIT $5`, args...)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[timelineItem])
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	if a.ViewID != "" {
		// A support view sees that an email was sent or received, not
		// its subject.
		for i := range list {
			if list[i].Kind == "message" {
				list[i].Title = ""
			}
		}
	}
	var next *string
	if len(list) == limit {
		last := list[len(list)-1]
		v := last.At.Format(time.RFC3339Nano) + "|" + last.ID
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// ---- Consent ----

var (
	consentChannels = []string{"email", "sms", "push", "whatsapp", "in_app", "ads", "web"}
	consentKey      = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)
)

type consentView struct {
	ID       string `json:"id"`
	RecordID string `json:"record_id"`
	// The browser a consent banner choice was made in.
	AnonymousID *string        `json:"anonymous_id"`
	Channel     string         `json:"channel"`
	Topic       string         `json:"topic"`
	Purpose     string         `json:"purpose"`
	Status      string         `json:"status"`
	Source      string         `json:"source"`
	Evidence    map[string]any `json:"evidence"`
	OccurredAt  time.Time      `json:"occurred_at"`
	CreatedBy   *string        `json:"created_by"`
	CreatedAt   time.Time      `json:"created_at"`
}

const consentColumns = `k.id::text, k.record_id::text, k.anonymous_id, k.channel, k.topic, k.purpose, k.status, k.source, k.evidence, k.occurred_at, k.created_by::text, k.created_at`

func (s *Server) listConsents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	history := r.URL.Query().Get("history") == "true"
	var list []consentView
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rec, err := loadRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		sql := `SELECT DISTINCT ON (k.channel, k.topic, k.purpose) ` + consentColumns + ` FROM consents k WHERE k.record_id = $1
			ORDER BY k.channel, k.topic, k.purpose, k.occurred_at DESC, k.created_at DESC`
		if history {
			sql = `SELECT ` + consentColumns + ` FROM consents k WHERE k.record_id = $1 ORDER BY k.occurred_at DESC, k.created_at DESC LIMIT 500`
		}
		rows, err := tx.Query(ctx, sql, rec.ID)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[consentView])
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type consentInput struct {
	Channel    string         `json:"channel"`
	Topic      string         `json:"topic"`
	Purpose    string         `json:"purpose"`
	Status     string         `json:"status"`
	Source     string         `json:"source"`
	Evidence   map[string]any `json:"evidence"`
	OccurredAt *time.Time     `json:"occurred_at"`
}

func (s *Server) addConsent(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in consentInput
	if !decode(w, r, &in) {
		return
	}
	switch {
	case !slices.Contains(consentChannels, in.Channel):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "channel is email, sms, push, whatsapp, in_app, ads or web")
		return
	case in.Topic != "" && !consentKey.MatchString(in.Topic):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "topic is a topic's key, such as product_updates, or empty for the whole channel")
		return
	case in.Status != "granted" && in.Status != "withdrawn":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is granted or withdrawn")
		return
	}
	if in.Purpose == "" {
		in.Purpose = "marketing"
	}
	if !consentKey.MatchString(in.Purpose) || len(in.Purpose) > 40 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "purpose is a short lowercase key such as marketing")
		return
	}
	if in.Source == "" {
		in.Source = "api"
	}
	if !sourceLabel.MatchString(in.Source) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is a short lowercase label such as signup_form, preference_center or import")
		return
	}
	if in.Evidence == nil {
		in.Evidence = map[string]any{}
	}
	if b, _ := json.Marshal(in.Evidence); len(b) > 4000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "evidence must be under 4 KB")
		return
	}
	at := time.Now()
	if in.OccurredAt != nil {
		if in.OccurredAt.After(at.Add(5 * time.Minute)) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "occurred_at cannot be in the future")
			return
		}
		at = *in.OccurredAt
	}
	var view consentView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := lockRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		if rec.Type != "person" {
			return errInvalid("consent is recorded on people")
		}
		rows, err := tx.Query(ctx, `INSERT INTO consents (workspace_id, record_id, channel, topic, purpose, status, source, evidence, occurred_at, created_by, request_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING `+strings.ReplaceAll(consentColumns, "k.", ""),
			a.WorkspaceID, rec.ID, in.Channel, in.Topic, in.Purpose, in.Status, in.Source, in.Evidence, at, nullIfEmpty(a.ActorID), RequestID(ctx))
		if err != nil {
			return err
		}
		if view, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[consentView]); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "consent."+in.Status, "record", rec.ID, map[string]any{
			"channel": in.Channel, "purpose": in.Purpose, "topic": in.Topic != "", "source": in.Source,
		}); err != nil {
			return err
		}
		// The person opting in again clears an earlier unsubscribe.
		_, err = customers.OptedIn(ctx, tx, a.WorkspaceID, rec.ID, in.Channel, in.Topic, in.Purpose, in.Status, in.Source, RequestID(ctx))
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

// brandList is the brands a caller is limited to, or nil for every brand.
func brandList(brands []string) []string {
	if len(brands) == 0 {
		return nil
	}
	return brands
}
