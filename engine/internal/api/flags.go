package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/flags"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Feature flags: switches and splits a workspace's website and servers
// read to decide what a visitor sees (engine/internal/flags decides). The
// website script asks POST /v1/site/flags with its site key; a server asks
// POST .../flags/evaluate with a token. Experiments build on them.

const (
	maxFlags = 200
	// How long a process keeps a workspace's flags in memory. A change
	// made through another process reaches visitors within this.
	flagCacheTTL = 10 * time.Second
	maxFlagBody  = 16 << 10
)

var flagStatuses = []string{"on", "off", "archived"}

func (s *Server) routesFlags(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/flags", s.inWorkspace(read, s.listFlags))
	mux.HandleFunc("POST /v1/workspaces/{ws}/flags", s.inWorkspace(manage, s.createFlag))
	mux.HandleFunc("POST /v1/workspaces/{ws}/flags/evaluate", s.inWorkspace(read, s.evaluateFlags))
	mux.HandleFunc("GET /v1/workspaces/{ws}/flags/{flag}", s.inWorkspace(read, s.getFlag))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/flags/{flag}", s.inWorkspace(manage, s.updateFlag))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/flags/{flag}", s.inWorkspace(manage, s.archiveFlag))
	// Public: the site key in the body is the authority.
	mux.HandleFunc("POST /v1/site/flags", s.siteFlags)
	mux.HandleFunc("OPTIONS /v1/site/flags", s.siteEventsPreflight)
	// The visual editor: the signed edit token is the authority.
	mux.HandleFunc("POST /v1/workspaces/{ws}/flags/{flag}/edit-link", s.inWorkspace(manage, s.createFlagEditLink))
	mux.HandleFunc("GET /v1/site/flags/edits", s.siteFlagEdits)
	mux.HandleFunc("POST /v1/site/flags/edits", s.siteFlagEdits)
	mux.HandleFunc("OPTIONS /v1/site/flags/edits", s.siteEventsPreflight)
	mux.HandleFunc("GET /v1/site/editor.js", s.editorJS)
}

type flagView struct {
	ID          string          `json:"id"`
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	BrandID     *string         `json:"brand_id"`
	Status      string          `json:"status"`
	Variants    []flags.Variant `json:"variants"`
	Rollout     int             `json:"rollout"`
	Rules       []flags.Rule    `json:"rules"`
	OnSite      bool            `json:"on_site"`
	// ExclusionGroup is the group whose visitors the flag shares with its
	// other flags ("" for none); GroupShare is the percent of them it
	// owns, and GroupStart where its slice begins.
	ExclusionGroup string     `json:"exclusion_group"`
	GroupShare     int        `json:"group_share"`
	GroupStart     int        `json:"group_start"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ArchivedAt     *time.Time `json:"archived_at"`
	salt           string
}

type flagInput struct {
	Key         *string          `json:"key"`
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	BrandID     optional[string] `json:"brand_id"`
	Status      *string          `json:"status"`
	Variants    *[]flags.Variant `json:"variants"`
	Rollout     *int             `json:"rollout"`
	Rules       *[]flags.Rule    `json:"rules"`
	OnSite      *bool            `json:"on_site"`
	// ExclusionGroup joins a group ("" leaves it); GroupShare is the
	// percent of the group's visitors to take (default 50).
	ExclusionGroup *string `json:"exclusion_group"`
	GroupShare     *int    `json:"group_share"`
}

const flagCols = `id::text, key, name, description, brand_id::text, status, variants, rollout, rules, on_site, exclusion_group, group_from, group_to, created_at, updated_at, archived_at, salt`

func scanFlag(row pgx.Row) (flagView, error) {
	var v flagView
	var variants, rules []byte
	var from, to int
	err := row.Scan(&v.ID, &v.Key, &v.Name, &v.Description, &v.BrandID, &v.Status, &variants, &v.Rollout, &rules, &v.OnSite,
		&v.ExclusionGroup, &from, &to, &v.CreatedAt, &v.UpdatedAt, &v.ArchivedAt, &v.salt)
	if err != nil {
		return v, err
	}
	if v.ExclusionGroup != "" {
		v.GroupStart, v.GroupShare = from/100, (to-from)/100
	}
	if err := json.Unmarshal(variants, &v.Variants); err != nil {
		return v, err
	}
	if err := json.Unmarshal(rules, &v.Rules); err != nil {
		return v, err
	}
	for i := range v.Rules {
		if v.Rules[i].Values == nil {
			v.Rules[i].Values = []string{}
		}
	}
	return v, nil
}

func (s *Server) loadFlags(ctx context.Context, q pgx.Tx, ws string, where string, args ...any) ([]flagView, error) {
	sql := `SELECT ` + flagCols + ` FROM flags WHERE workspace_id = $1` + where + ` ORDER BY created_at, id`
	args = append([]any{ws}, args...)
	var rows pgx.Rows
	var err error
	if q != nil {
		rows, err = q.Query(ctx, sql, args...)
	} else {
		rows, err = s.pool.Query(ctx, sql, args...)
	}
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (flagView, error) { return scanFlag(row) })
	if list == nil {
		list = []flagView{}
	}
	return list, err
}

func (s *Server) listFlags(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "all" && !slices.Contains(flagStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is on, off, archived or all; without it, every flag but the archived ones")
		return
	}
	where := ""
	args := []any{a.WorkspaceID}
	switch status {
	case "":
		where = ` AND status <> 'archived'`
	case "all":
	default:
		args = append(args, status)
		where = ` AND status = $2`
	}
	// A brand's flags include the shared ones, since those reach its site
	// too.
	if sel.id != "" && sel.id != "none" {
		args = append(args, sel.id)
		where += fmt.Sprintf(` AND (brand_id IS NULL OR brand_id = $%d)`, len(args))
		sel.id = ""
	}
	where += sel.sql("brand_id", &args)
	list, err := s.loadFlags(r.Context(), nil, a.WorkspaceID, where, args[1:]...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// oneFlag loads the flag in the path, by id or key, or answers 404.
func (s *Server) oneFlag(w http.ResponseWriter, r *http.Request) (flagView, bool) {
	a := accessFrom(r.Context())
	ref := r.PathValue("flag")
	var list []flagView
	var err error
	if validUUID(ref) {
		list, err = s.loadFlags(r.Context(), nil, a.WorkspaceID, ` AND id = $2`, ref)
	} else if flags.ValidKey(ref) {
		list, err = s.loadFlags(r.Context(), nil, a.WorkspaceID, ` AND key = $2 AND status <> 'archived'`, ref)
	}
	if err != nil {
		s.internal(w, r, err)
		return flagView{}, false
	}
	if len(list) == 1 && a.sees(list[0].BrandID) {
		return list[0], true
	}
	writeError(w, r, http.StatusNotFound, "not_found", "flag not found: use its id or key")
	return flagView{}, false
}

func (s *Server) getFlag(w http.ResponseWriter, r *http.Request) {
	if f, ok := s.oneFlag(w, r); ok {
		writeJSON(w, http.StatusOK, f)
	}
}

// apply checks in and sets it on f, for a new flag or a change.
func (s *Server) applyFlagInput(ctx context.Context, a *access, f *flagView, in flagInput, isNew bool) error {
	if in.Key != nil {
		if !isNew && *in.Key != f.Key {
			return errInvalid("a flag's key can't change, since code asks for it; archive this flag and add another")
		}
		f.Key = strings.TrimSpace(*in.Key)
	}
	if !flags.ValidKey(f.Key) {
		return errInvalid("key is required: lowercase letters, digits, - and _, up to 64, such as new-pricing")
	}
	if in.Name != nil {
		f.Name = strings.TrimSpace(*in.Name)
	}
	if f.Name == "" {
		f.Name = f.Key
	}
	if len(f.Name) > 100 {
		return errInvalid("name is at most 100 characters")
	}
	if in.Description != nil {
		f.Description = strings.TrimSpace(*in.Description)
	}
	if len(f.Description) > flags.MaxDescription {
		return errInvalid("description is at most 1,000 characters")
	}
	if in.BrandID.Set {
		f.BrandID = in.BrandID.Value
		if err := s.siteKeyBrand(ctx, a.WorkspaceID, f.BrandID); err != nil {
			return err
		}
	}
	if !a.writes(f.BrandID) {
		return errDenied("you are limited to some brands: give the flag one of your brands")
	}
	if in.Status != nil {
		if *in.Status != "on" && *in.Status != "off" {
			return errInvalid("status is on or off; DELETE archives a flag")
		}
		f.Status = *in.Status
	}
	if in.Variants != nil {
		f.Variants = *in.Variants
	}
	for i := range f.Variants {
		if string(f.Variants[i].Payload) == "null" {
			f.Variants[i].Payload = nil
		}
	}
	if msg := flags.CheckVariants(f.Variants); msg != "" {
		return errInvalid(msg)
	}
	if in.Rollout != nil {
		f.Rollout = *in.Rollout
	}
	if f.Rollout < 0 || f.Rollout > 100 {
		return errInvalid("rollout is a percentage, 0 to 100")
	}
	if in.Rules != nil {
		f.Rules = *in.Rules
	}
	if msg := flags.CheckRules(f.Rules); msg != "" {
		return errInvalid(msg)
	}
	if in.OnSite != nil {
		f.OnSite = *in.OnSite
	}
	if in.ExclusionGroup != nil {
		g := strings.TrimSpace(*in.ExclusionGroup)
		if g != "" && !flags.ValidKey(g) {
			return errInvalid("exclusion_group is a name of lowercase letters, digits, - and _, such as homepage; \"\" leaves the group")
		}
		if g != f.ExclusionGroup {
			f.ExclusionGroup, f.GroupShare, f.GroupStart = g, 0, 0
		}
	}
	if in.GroupShare != nil {
		if *in.GroupShare < 1 || *in.GroupShare > 100 {
			return errInvalid("group_share is the percent of the group's visitors the flag takes, 1 to 100")
		}
		if f.ExclusionGroup == "" {
			return errInvalid("group_share needs an exclusion_group")
		}
		if *in.GroupShare != f.GroupShare {
			f.GroupShare, f.GroupStart = *in.GroupShare, -1
		}
	}
	if f.ExclusionGroup != "" && f.GroupShare == 0 {
		f.GroupShare, f.GroupStart = 50, -1
	}
	return nil
}

// placeInGroup gives f a slice of its exclusion group when it needs one
// (GroupStart -1, or a group just joined), inside tx with the group's
// flags locked, so two flags can't take the same slice.
func placeInGroup(ctx context.Context, tx pgx.Tx, ws string, f *flagView) error {
	if f.ExclusionGroup == "" || f.GroupStart >= 0 && f.GroupShare > 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT group_from, group_to FROM flags
		WHERE workspace_id = $1 AND exclusion_group = $2 AND status <> 'archived' AND id::text <> $3 FOR UPDATE`, ws, f.ExclusionGroup, f.ID)
	if err != nil {
		return err
	}
	taken, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]int, error) {
		var t [2]int
		return t, r.Scan(&t[0], &t[1])
	})
	if err != nil {
		return err
	}
	from, _, ok := flags.Allocate(taken, f.GroupShare)
	if !ok {
		return errABConflict(fmt.Sprintf("exclusion group %s has no free %d%% left: give this flag a smaller group_share, or take a flag out of the group", f.ExclusionGroup, f.GroupShare))
	}
	f.GroupStart = from / 100
	return nil
}

var errFlagKeyTaken = errors.New("flag key taken")

func (s *Server) flagWriteError(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, errFlagKeyTaken) || isUnique(err) {
		writeError(w, r, http.StatusConflict, "conflict", "another flag has this key: keys name one live flag")
		return true
	}
	var c errABConflict
	if errors.As(err, &c) {
		writeError(w, r, http.StatusConflict, "conflict", string(c))
		return true
	}
	return s.allocError(w, r, err)
}

func (s *Server) createFlag(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in flagInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	f := flagView{Status: "off", Rollout: 100, Rules: []flags.Rule{}, OnSite: true,
		Variants: []flags.Variant{{Key: "on", Weight: 100}}}
	if err := s.applyFlagInput(ctx, a, &f, in, true); s.flagWriteError(w, r, err) {
		return
	}
	variants, _ := json.Marshal(f.Variants)
	rules, _ := json.Marshal(f.Rules)
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM flags WHERE workspace_id = $1 AND status <> 'archived'`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= maxFlags {
			return errInvalid("a workspace can have at most 200 flags; archive the ones you no longer use")
		}
		if err := placeInGroup(ctx, tx, a.WorkspaceID, &f); err != nil {
			return err
		}
		from, to := groupRange(f)
		if err := tx.QueryRow(ctx, `INSERT INTO flags (workspace_id, brand_id, key, name, description, status, variants, rollout, rules, on_site, exclusion_group, group_from, group_to, salt, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING id::text`,
			a.WorkspaceID, f.BrandID, f.Key, f.Name, f.Description, f.Status, variants, f.Rollout, rules, f.OnSite, f.ExclusionGroup, from, to, newID(), nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "flag.created", "flag", id, flagAuditData(f))
	})
	if s.flagWriteError(w, r, err) {
		return
	}
	s.flagCache.forget(a.WorkspaceID)
	list, err := s.loadFlags(ctx, nil, a.WorkspaceID, ` AND id = $2`, id)
	if err != nil || len(list) != 1 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, list[0])
}

// flagAuditData is what the audit log and product events keep of a flag:
// its shape, never its key, name or payloads.
func flagAuditData(f flagView) map[string]any {
	return map[string]any{"status": f.Status, "brand_id": f.BrandID, "variants": len(f.Variants), "rollout": f.Rollout, "rules": len(f.Rules), "on_site": f.OnSite,
		"in_exclusion_group": f.ExclusionGroup != "", "group_share": f.GroupShare}
}

// groupRange is f's slice of its exclusion group in buckets, or 0, 0.
func groupRange(f flagView) (int, int) {
	if f.ExclusionGroup == "" {
		return 0, 0
	}
	return f.GroupStart * 100, (f.GroupStart + f.GroupShare) * 100
}

func (s *Server) updateFlag(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneFlag(w, r)
	if !ok {
		return
	}
	if cur.Status == "archived" {
		writeError(w, r, http.StatusConflict, "conflict", "this flag is archived; add a new one")
		return
	}
	var in flagInput
	if !decode(w, r, &in) {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", brandLimitedMessage)
		return
	}
	ctx := r.Context()
	f := cur
	if err := s.applyFlagInput(ctx, a, &f, in, false); s.flagWriteError(w, r, err) {
		return
	}
	variants, _ := json.Marshal(f.Variants)
	rules, _ := json.Marshal(f.Rules)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Who a running test's visitors are can't change under it.
		if f.ExclusionGroup != cur.ExclusionGroup || f.GroupShare != cur.GroupShare {
			var running bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ab_tests WHERE workspace_id = $1 AND flag_id = $2 AND status = 'running')`,
				a.WorkspaceID, cur.ID).Scan(&running); err != nil {
				return err
			}
			if running {
				return errABConflict("this flag's A/B test is running: its exclusion group and share are fixed until it ends, so the visitors it measures don't change")
			}
		}
		if err := placeInGroup(ctx, tx, a.WorkspaceID, &f); err != nil {
			return err
		}
		from, to := groupRange(f)
		if _, err := tx.Exec(ctx, `UPDATE flags SET name = $3, description = $4, brand_id = $5, status = $6, variants = $7, rollout = $8, rules = $9, on_site = $10,
				exclusion_group = $11, group_from = $12, group_to = $13, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, cur.ID, a.WorkspaceID, f.Name, f.Description, f.BrandID, f.Status, variants, f.Rollout, rules, f.OnSite,
			f.ExclusionGroup, from, to); err != nil {
			return err
		}
		data := flagAuditData(f)
		data["was"] = cur.Status
		return record(ctx, tx, &a.principal, a.WorkspaceID, "flag.updated", "flag", cur.ID, data)
	})
	if s.flagWriteError(w, r, err) {
		return
	}
	s.flagCache.forget(a.WorkspaceID)
	list, err := s.loadFlags(ctx, nil, a.WorkspaceID, ` AND id = $2`, cur.ID)
	if err != nil || len(list) != 1 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

// archiveFlag turns a flag off for good and frees its key. It stays in the
// list with ?status=archived, for the experiments that used it.
func (s *Server) archiveFlag(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneFlag(w, r)
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", brandLimitedMessage)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE flags SET status = 'archived', archived_at = now(), updated_at = now() WHERE id = $1 AND workspace_id = $2 AND status <> 'archived'`,
			cur.ID, a.WorkspaceID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "flag.archived", "flag", cur.ID, map[string]any{"was": cur.Status})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.flagCache.forget(a.WorkspaceID)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Deciding ----

// liveFlag is a flag as evaluation reads it, from memory.
type liveFlag struct {
	flags.Flag
	brandID *string
	onSite  bool
}

// flagCache keeps each workspace's live flags in memory for a few
// seconds, so a page view costs no query.
type flagCache struct {
	mu   sync.Mutex
	byWS map[string]flagCacheEntry
}

type flagCacheEntry struct {
	at   time.Time
	list []liveFlag
}

func (c *flagCache) forget(ws string) {
	c.mu.Lock()
	delete(c.byWS, ws)
	c.mu.Unlock()
}

// liveFlags are the workspace's flags that aren't archived: those that are
// on, and those that are off, for previews.
func (s *Server) liveFlags(ctx context.Context, ws string) ([]liveFlag, error) {
	c := &s.flagCache
	c.mu.Lock()
	e, ok := c.byWS[ws]
	c.mu.Unlock()
	if ok && time.Since(e.at) < flagCacheTTL {
		metrics.FlagCache(true)
		return e.list, nil
	}
	metrics.FlagCache(false)
	list, err := s.loadFlags(ctx, nil, ws, ` AND status <> 'archived'`)
	if err != nil {
		return nil, err
	}
	out := make([]liveFlag, 0, len(list))
	for _, f := range list {
		out = append(out, liveFlag{Flag: flags.Flag{Key: f.Key, Status: f.Status, Variants: f.Variants, Rollout: f.Rollout, Rules: f.Rules, Salt: f.salt,
			Group: f.ExclusionGroup, GroupFrom: f.GroupStart * 100, GroupTo: (f.GroupStart + f.GroupShare) * 100},
			brandID: f.BrandID, onSite: f.OnSite})
	}
	c.mu.Lock()
	if c.byWS == nil || len(c.byWS) > 10_000 {
		c.byWS = map[string]flagCacheEntry{}
	}
	c.byWS[ws] = flagCacheEntry{at: time.Now(), list: out}
	c.mu.Unlock()
	return out, nil
}

// flagAnswer is what both surfaces return: each flag that is on, with the
// visitor's variant or false.
type flagAnswer struct {
	Flags    map[string]any             `json:"flags"`
	Payloads map[string]json.RawMessage `json:"payloads"`
	Reasons  map[string]string          `json:"reasons,omitempty"`
}

// decideFlags decides each flag that is on and passes keep. preview forces
// a version of a flag, on or off, so a person can see it before visitors
// do; it only gives what is already the flag's.
func decideFlags(list []liveFlag, v flags.Visitor, keep func(liveFlag) bool, preview map[string]string, reasons bool) flagAnswer {
	out := flagAnswer{Flags: map[string]any{}, Payloads: map[string]json.RawMessage{}}
	if reasons {
		out.Reasons = map[string]string{}
	}
	for _, f := range list {
		if !keep(f) {
			continue
		}
		if want, ok := preview[f.Key]; ok {
			if i := slices.IndexFunc(f.Variants, func(x flags.Variant) bool { return x.Key == want }); i >= 0 {
				out.Flags[f.Key] = want
				if p := f.Variants[i].Payload; len(p) > 0 {
					out.Payloads[f.Key] = p
				}
				continue
			}
		}
		if f.Status != "on" {
			continue
		}
		res := flags.Evaluate(f.Flag, v)
		if res.Variant == "" {
			out.Flags[f.Key] = false
		} else {
			out.Flags[f.Key] = res.Variant
			if len(res.Payload) > 0 {
				out.Payloads[f.Key] = res.Payload
			}
		}
		if reasons {
			out.Reasons[f.Key] = res.Reason
		}
	}
	return out
}

// siteFlags answers the website script: the visitor's variant of every
// flag that is on, served to the site and for the key's brand. Like
// events, it is public, and the key in the body is the authority.
func (s *Server) siteFlags(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	siteCORS(w)
	w.Header().Set("Cache-Control", "no-store")
	outcome := "refused"
	defer func() { metrics.FlagsDecided("site", outcome, time.Since(start)) }()
	ctx := r.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFlagBody))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the body must be under 16 KB")
		return
	}
	var in struct {
		Key         string `json:"key"`
		AnonymousID string `json:"anonymousId"`
		URL         string `json:"url"`
		Referrer    string `json:"referrer"`
		Language    string `json:"language"`
		// Versions a person chose to see (?of_flag=key:version).
		Preview map[string]string `json:"preview"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `send {"key": "ofk_...", "anonymousId": "...", "url": "..."}`)
		return
	}
	conn := clientIP(r)
	if s.siteIPLimit.over(conn) {
		s.siteIPLimit.allow(w, r, conn)
		return
	}
	var k siteKeyRow
	if strings.HasPrefix(in.Key, auth.PrefixSiteKey) && len(in.Key) <= 100 {
		err = s.pool.QueryRow(ctx, `SELECT id::text, workspace_id::text, brand_id::text, origins FROM site_keys
			WHERE revoked_at IS NULL AND (key = $1 OR (previous_key = $1 AND previous_expires_at > now()))`, in.Key).
			Scan(&k.id, &k.workspaceID, &k.brandID, &k.origins)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			outcome = "error"
			s.internal(w, r, err)
			return
		}
	}
	if k.id == "" {
		if !s.siteIPLimit.allow(w, r, conn) {
			return
		}
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "the site key is not valid: copy the snippet again from the workspace's website tracking settings")
		return
	}
	if !originAllowed(k.origins, r.Header.Get("Origin")) {
		writeError(w, r, http.StatusForbidden, "forbidden", "this site key does not take requests from this origin; add it to the key's origins")
		return
	}
	if !s.siteFlagLimit.allow(w, r, k.id) {
		return
	}
	anon := strings.TrimSpace(in.AnonymousID)
	if len(anon) < 8 || len(anon) > 64 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "anonymousId is required: the visitor's id from the script")
		return
	}
	list, err := s.liveFlags(ctx, k.workspaceID)
	if err != nil {
		outcome = "error"
		s.internal(w, r, err)
		return
	}
	device, _, _ := parseUA(r.Header.Get("User-Agent"))
	v := flags.Visitor{Subject: anon, URL: clip(in.URL, 2000), Referrer: clip(in.Referrer, 2000), Language: clip(in.Language, 35), Device: device}
	ans := decideFlags(list, v, func(f liveFlag) bool {
		return f.onSite && (k.brandID == nil || f.brandID == nil || *f.brandID == *k.brandID)
	}, in.Preview, false)
	outcome = "ok"
	writeJSON(w, http.StatusOK, ans)
}

// evaluateFlags decides flags for a server: the variant a visitor or user
// gets of every flag that is on (or only those asked for), with why.
func (s *Server) evaluateFlags(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	outcome := "refused"
	defer func() { metrics.FlagsDecided("api", outcome, time.Since(start)) }()
	a := accessFrom(r.Context())
	var in struct {
		AnonymousID string         `json:"anonymous_id"`
		UserID      string         `json:"user_id"`
		URL         string         `json:"url"`
		Referrer    string         `json:"referrer"`
		UserAgent   string         `json:"user_agent"`
		Language    string         `json:"language"`
		Properties  map[string]any `json:"properties"`
		Keys        []string       `json:"keys"`
	}
	if !decode(w, r, &in) {
		return
	}
	subject := strings.TrimSpace(in.AnonymousID)
	if subject == "" {
		subject = strings.TrimSpace(in.UserID)
	}
	if subject == "" || len(subject) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send anonymous_id (outflare.anonymousId() from the page, so the variant matches the site's) or user_id")
		return
	}
	if len(in.Keys) > maxFlags || len(in.Properties) > 50 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send at most 200 keys and 50 properties")
		return
	}
	props := map[string]string{}
	for k, v := range in.Properties {
		switch x := v.(type) {
		case string:
			props[k] = clip(x, 500)
		case nil:
		default:
			b, _ := json.Marshal(x)
			props[k] = clip(strings.Trim(string(b), `"`), 500)
		}
	}
	list, err := s.liveFlags(r.Context(), a.WorkspaceID)
	if err != nil {
		outcome = "error"
		s.internal(w, r, err)
		return
	}
	device, _, _ := parseUA(in.UserAgent)
	v := flags.Visitor{Subject: subject, SignedIn: strings.TrimSpace(in.UserID) != "", URL: clip(in.URL, 2000), Referrer: clip(in.Referrer, 2000),
		Language: clip(in.Language, 35), Device: device, Properties: props}
	ans := decideFlags(list, v, func(f liveFlag) bool {
		return a.sees(f.brandID) && (len(in.Keys) == 0 || slices.Contains(in.Keys, f.Key))
	}, nil, true)
	outcome = "ok"
	writeJSON(w, http.StatusOK, ans)
}
