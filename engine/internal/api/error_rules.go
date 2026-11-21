package api

import (
	"context"
	"errors"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jserrors"
)

// Ignore rules (docs/error-tracking.md): errors a brand never wants in its
// open list, matched by a piece of text in one part of the error, case not
// mattering. Rules apply to errors already stored as well as new ones, so
// adding one tidies the list at once and deleting one brings the errors
// back. Errors a rule or a built-in filter matches are kept, listed under
// Filtered, and never alert.

func (s *Server) routesErrorRules(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/error-rules", s.onBrand(read, s.listErrorRules))
	mux.HandleFunc("POST /v1/brands/{brand}/error-rules", s.onBrand(manage, s.createErrorRule))
	mux.HandleFunc("PATCH /v1/brands/{brand}/error-rules/{id}", s.onBrand(manage, s.updateErrorRule))
	mux.HandleFunc("DELETE /v1/brands/{brand}/error-rules/{id}", s.onBrand(manage, s.deleteErrorRule))
}

// errorRuleFields are the parts of an error a rule can look in.
var errorRuleFields = []string{"message", "type", "stack", "page", "host"}

// errorHost is an error's site: the host of its page's address.
const errorHost = `coalesce(lower(substring(context->'page'->>'url' FROM '^[a-zA-Z][a-zA-Z0-9+.-]*://([^/:?#]+)')), '')`

// errorRuleText is the text a rule r looks in, for an event.
const errorRuleText = `CASE r.field
	WHEN 'message' THEN coalesce(properties->>'message', '') || ' ' || coalesce(properties->>'message_full', '')
	WHEN 'type' THEN coalesce(properties->>'type', '')
	WHEN 'stack' THEN coalesce(properties->>'stack', '') || ' ' || coalesce(properties->>'source', '')
	WHEN 'page' THEN coalesce(context->'page'->>'url', '')
	ELSE ` + errorHost + ` END`

// errorFilter is why an event is filtered: a built-in reason, "rule", or
// "" when it isn't. $2 is the brand.
const errorFilter = `coalesce(properties->>'noise', CASE WHEN EXISTS (SELECT 1 FROM error_rules r WHERE r.brand_id = $2
	AND strpos(lower(` + errorRuleText + `), lower(r.pattern)) > 0) THEN 'rule' END, '')`

type errorRule struct {
	ID        string    `json:"id"`
	Field     string    `json:"field"`
	Pattern   string    `json:"pattern"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy *string   `json:"created_by"`
	// Errors it matched in the last 7 days.
	Matched int `json:"matched"`
}

// builtinFilter is one of the filters every brand has.
type builtinFilter struct {
	Reason      string `json:"reason"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

var builtinFilters = []builtinFilter{
	{jserrors.NoiseExtension, "Browser extensions", "Errors thrown in a browser extension's code (chrome-extension://, moz-extension://, Safari's hidden extension addresses), not your site's."},
	{jserrors.NoiseLinkScanner, "Email link scanners", "\"Object Not Found Matching Id\": Outlook's link scanner opening your page from an email."},
	{jserrors.NoiseInjected, "Code added by wallets and browsers", "Errors about code a crypto wallet or the browser puts on the page, such as window.ethereum or __firefox__."},
	{jserrors.NoiseLocalFile, "Saved copies of a page", "The page was saved and opened from a file on someone's computer."},
	{jserrors.NoiseIPHost, "Copies on a bare IP address", "A copy of your page served from an IP address instead of your domain."},
}

func (s *Server) listErrorRules(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT r.id::text, r.field, r.pattern, r.note, r.created_at, act.display_name,
			(SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND brand_id = $2 AND name = $3 AND occurred_at >= now() - interval '7 days'
				AND strpos(lower(`+errorRuleText+`), lower(r.pattern)) > 0)
		FROM error_rules r LEFT JOIN actors act ON act.id = r.created_by
		WHERE r.workspace_id = $1 AND r.brand_id = $2 ORDER BY r.created_at`, a.WorkspaceID, a.Brand, errorEvent)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (errorRule, error) {
		var v errorRule
		return v, row.Scan(&v.ID, &v.Field, &v.Pattern, &v.Note, &v.CreatedAt, &v.CreatedBy, &v.Matched)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []errorRule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "builtin": builtinFilters})
}

type errorRuleInput struct {
	Field   *string `json:"field"`
	Pattern *string `json:"pattern"`
	Note    *string `json:"note"`
}

// check validates what was sent; all says every field is needed.
func (in *errorRuleInput) check(all bool) error {
	if all && (in.Field == nil || in.Pattern == nil) {
		return errorReply{http.StatusBadRequest, "invalid_request", "field and pattern are required"}
	}
	if in.Field != nil && !slices.Contains(errorRuleFields, *in.Field) {
		return errorReply{http.StatusBadRequest, "invalid_request", "field is message, type, stack, page or host"}
	}
	if in.Pattern != nil {
		p := strings.TrimSpace(*in.Pattern)
		if p == "" || len(p) > 500 {
			return errorReply{http.StatusBadRequest, "invalid_request", "pattern is the text to find, 1 to 500 characters"}
		}
		in.Pattern = &p
	}
	if in.Note != nil {
		n := strings.TrimSpace(*in.Note)
		if len(n) > 500 {
			return errorReply{http.StatusBadRequest, "invalid_request", "note is at most 500 characters"}
		}
		in.Note = &n
	}
	return nil
}

func (s *Server) createErrorRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in errorRuleInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.check(true); err != nil {
		s.replyErr(w, r, err)
		return
	}
	note := ""
	if in.Note != nil {
		note = *in.Note
	}
	ctx := r.Context()
	var v errorRule
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM error_rules WHERE brand_id = $1`, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= 200 {
			return errorReply{http.StatusBadRequest, "invalid_request", "a brand has at most 200 ignore rules"}
		}
		err := tx.QueryRow(ctx, `INSERT INTO error_rules (workspace_id, brand_id, field, pattern, note, created_by) VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id::text, field, pattern, note, created_at`, a.WorkspaceID, a.Brand, *in.Field, *in.Pattern, note, nullableString(a.ActorID)).
			Scan(&v.ID, &v.Field, &v.Pattern, &v.Note, &v.CreatedAt)
		if isUnique(err) {
			return errorReply{http.StatusConflict, "conflict", "this brand already has that rule"}
		}
		if err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "error_rule.created", "error_rule", v.ID,
			map[string]any{"brand_id": a.Brand, "field": v.Field, "pattern": v.Pattern}, map[string]any{"field": v.Field})
	})
	if err != nil {
		s.replyErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateErrorRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such rule")
		return
	}
	var in errorRuleInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.check(false); err != nil {
		s.replyErr(w, r, err)
		return
	}
	ctx := r.Context()
	var v errorRule
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE error_rules SET field = coalesce($4, field), pattern = coalesce($5, pattern), note = coalesce($6, note)
			WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 RETURNING id::text, field, pattern, note, created_at`,
			id, a.WorkspaceID, a.Brand, in.Field, in.Pattern, in.Note).Scan(&v.ID, &v.Field, &v.Pattern, &v.Note, &v.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return errorReply{http.StatusNotFound, "not_found", "no such rule"}
		}
		if isUnique(err) {
			return errorReply{http.StatusConflict, "conflict", "this brand already has that rule"}
		}
		if err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "error_rule.updated", "error_rule", v.ID,
			map[string]any{"brand_id": a.Brand, "field": v.Field, "pattern": v.Pattern}, map[string]any{"field": v.Field})
	})
	if err != nil {
		s.replyErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteErrorRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such rule")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var field string
		err := tx.QueryRow(ctx, `DELETE FROM error_rules WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 RETURNING field`, id, a.WorkspaceID, a.Brand).Scan(&field)
		if errors.Is(err, pgx.ErrNoRows) {
			return errorReply{http.StatusNotFound, "not_found", "no such rule"}
		}
		if err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "error_rule.deleted", "error_rule", id,
			map[string]any{"brand_id": a.Brand}, map[string]any{"field": field})
	})
	if err != nil {
		s.replyErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// errorRuleHit says whether one of the brand's rules matches an error
// about to be stored.
func errorRuleHit(ctx context.Context, tx pgx.Tx, brand string, props, ctxObj map[string]any) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT field, pattern FROM error_rules WHERE brand_id = $1`, brand)
	if err != nil {
		return false, err
	}
	page, _ := ctxObj["page"].(map[string]any)
	url := propText(page["url"])
	hit := false
	var field, pattern string
	_, err = pgx.ForEachRow(rows, []any{&field, &pattern}, func() error {
		var text string
		switch field {
		case "message":
			text = propText(props["message"]) + " " + propText(props["message_full"])
		case "type":
			text = propText(props["type"])
		case "stack":
			text = propText(props["stack"]) + " " + propText(props["source"])
		case "page":
			text = url
		case "host":
			text = errorHostOf(url)
		}
		if strings.Contains(strings.ToLower(text), strings.ToLower(pattern)) {
			hit = true
		}
		return nil
	})
	return hit, err
}

// errorReply is an answer other than 200 a handler's transaction gives.
type errorReply struct {
	status        int
	code, message string
}

func (e errorReply) Error() string { return e.message }

// replyErr writes err: its errorReply, or a 500.
func (s *Server) replyErr(w http.ResponseWriter, r *http.Request, err error) {
	var e errorReply
	if errors.As(err, &e) {
		writeError(w, r, e.status, e.code, e.message)
		return
	}
	s.internal(w, r, err)
}

// errorHostOf is an address's host, lowercased, as errorHost reads it.
func errorHostOf(addr string) string {
	if u, err := neturl.Parse(addr); err == nil {
		return strings.ToLower(u.Hostname())
	}
	return ""
}
