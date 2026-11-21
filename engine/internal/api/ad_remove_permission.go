package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/actionctx"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Removing a campaign, an ad or a keyword can't be undone on the ad
// platform, so it is a permission of its own, off for every agent and
// token until a person turns it on:
//
//   - a person needs ads.remove in their role (Owner, Admin and Member
//     have it; a role the workspace changed or made has it only if ticked);
//   - an API token needs ads.remove among its scopes;
//   - an agent token on the action layer needs a grant that names the
//     removal itself (ads/campaigns/*/remove, ads/items/*/remove or
//     ads/keywords/*/remove; brands/*/ in front for a brand route). A broad
//     grant such as "**" or "ads/**" never covers a removal. The grant's
//     mode is the permission's: refuse is Off, hold is Ask (a person
//     approves each one), run is On. Its conditions, daily_limit and brand
//     keep it to some removals.
//
// Every refusal names the permission (error code permission_required).

// adRemovePermission names the permission each removal route needs, by
// the collection in its path.
var adRemovePermission = map[string]string{
	"campaigns": "ads.remove_campaign",
	"items":     "ads.remove_ad",
	"keywords":  "ads.remove_keyword",
}

// removalConditions keep a removal grant to some removals. All that are
// set must hold.
type removalConditions struct {
	// Only in a campaign that is paused (or the campaign itself paused).
	PausedCampaignOnly bool `json:"paused_campaign_only,omitempty"`
	// Only an ad the platform disapproved; a campaign only when every one
	// of its ads is disapproved. Keywords have no verdict, so a grant with
	// it never removes one.
	DisapprovedOnly bool `json:"disapproved_only,omitempty"`
	// Only when it spent at most this much over the last 30 days, in
	// micros of its account's currency (0: never spent).
	MaxSpendMicros *int64 `json:"max_spend_micros,omitempty"`
}

func (c removalConditions) empty() bool {
	return !c.PausedCampaignOnly && !c.DisapprovedOnly && c.MaxSpendMicros == nil
}

// parseRemovalConditions checks a grant's conditions as sent.
func parseRemovalConditions(raw map[string]any) (*removalConditions, string) {
	if raw == nil {
		return nil, ""
	}
	b, _ := json.Marshal(raw)
	var c removalConditions
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, "conditions takes paused_campaign_only, disapproved_only and max_spend_micros"
	}
	if c.MaxSpendMicros != nil && *c.MaxSpendMicros < 0 {
		return nil, "conditions.max_spend_micros is 0 or more"
	}
	if c.empty() {
		return nil, ""
	}
	return &c, ""
}

// removalGrantPath reports whether a grant's path names a removal, and
// which collection: ads/campaigns/*/remove, or the same under brands/*/.
func removalGrantPath(p string) (string, bool) {
	p = brandRelative(strings.TrimPrefix(p, "brands/*/"))
	segs := strings.Split(p, "/")
	if len(segs) != 4 || segs[0] != "ads" || segs[3] != "remove" || segs[2] == "**" {
		return "", false
	}
	if _, ok := adRemovePermission[segs[1]]; !ok {
		return "", false
	}
	return segs[1], true
}

// writePermissionRequired answers 403 permission_required, naming the
// permission so a person knows exactly what to turn on, and where.
func writePermissionRequired(w http.ResponseWriter, r *http.Request, permission, message string) {
	writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
		"code":       "permission_required",
		"message":    message,
		"request_id": RequestID(r.Context()),
		"permission": permission,
	}})
}

// removalGate decides an agent's removal on the action layer, before the
// usual grants: only a grant naming this removal decides it, and with
// none it is refused. It returns the grant to use, or false when it has
// answered.
func (s *Server) removalGate(w http.ResponseWriter, r *http.Request, a *access, grants []grantRule, kind, id string) (grantRule, bool) {
	perm := adRemovePermission[kind]
	var named []grantRule
	for _, g := range grants {
		if k, ok := removalGrantPath(g.path); ok && k == kind && (g.method == nil || *g.method == http.MethodPost) {
			named = append(named, g)
		}
	}
	path := actionPath(r)
	g, found := pickGrant(named, r.Method, brandRelative(path), nil)
	if !found {
		g, found = pickGrant(named, r.Method, path, nil)
	}
	if !found {
		writePermissionRequired(w, r, perm, "removing a "+removalWord(kind)+" can't be undone, so this agent needs the permission "+perm+
			": a person turns it on for this agent (Ask holds each removal for them, On runs it) on its page under Agents, or removes it themselves under Paid ads")
		return g, false
	}
	if g.mode == "refuse" {
		writePermissionRequired(w, r, perm, "this agent's permission "+perm+" is Off: a person can set it to Ask or On on its page under Agents")
		return g, false
	}
	if g.conditions != nil {
		why, err := s.removalOutside(r.Context(), a.WorkspaceID, kind, id, *g.conditions)
		if err != nil {
			s.internal(w, r, err)
			return g, false
		}
		if why != "" {
			writePermissionRequired(w, r, perm, "this agent's permission "+perm+" covers only some removals, and this one is outside it: "+why)
			return g, false
		}
	}
	return g, true
}

func removalWord(kind string) string {
	switch kind {
	case "campaigns":
		return "campaign"
	case "keywords":
		return "keyword"
	}
	return "ad"
}

// removalOutside says why a removal falls outside a grant's conditions,
// or "" when it is inside them.
func (s *Server) removalOutside(ctx context.Context, ws, kind, id string, c removalConditions) (string, error) {
	f, err := s.removalFacts(ctx, ws, kind, id)
	if errors.Is(err, pgx.ErrNoRows) {
		// The handler answers 404 for it after the gate.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case c.PausedCampaignOnly && f.campaignStatus != connectors.AdPaused:
		return "only removals in a paused campaign, and its campaign is " + f.campaignStatus, nil
	case c.DisapprovedOnly && kind == "keywords":
		return "only disapproved ads, and a keyword has no such verdict", nil
	case c.DisapprovedOnly && !f.disapproved:
		if kind == "campaigns" {
			return "only campaigns whose ads the platform all disapproved", nil
		}
		return "only ads the platform disapproved", nil
	case c.MaxSpendMicros != nil && f.spend30 > *c.MaxSpendMicros:
		return "only what spent at most " + micros(*c.MaxSpendMicros) + " over the last 30 days, and this spent " + micros(f.spend30), nil
	}
	return "", nil
}

// removalFacts is what a removal's conditions and its record need to
// know about the thing removed.
type removalFact struct {
	name, status, providerStatus, campaignID, campaignName, campaignStatus, groupName, currency string
	disapproved                                                                                 bool
	spend30                                                                                     int64
	details                                                                                     map[string]any
}

func (s *Server) removalFacts(ctx context.Context, ws, kind, id string) (removalFact, error) {
	var f removalFact
	if !validUUID(id) {
		return f, pgx.ErrNoRows
	}
	const spend = `(SELECT COALESCE(sum(m.spend_micros), 0) FROM ad_metrics_daily m WHERE m.ad_entity_id = %s AND m.day > current_date - 30)`
	switch kind {
	case "campaigns":
		return f, s.pool.QueryRow(ctx, `SELECT c.name, c.status, COALESCE(c.provider_status, ''), c.id::text, c.name, c.status, '', a.currency,
			EXISTS (SELECT 1 FROM ad_entities g JOIN ad_entities d ON d.parent_id = g.id AND d.level = 'ad' WHERE g.parent_id = c.id AND d.status <> 'removed')
			AND NOT EXISTS (SELECT 1 FROM ad_entities g JOIN ad_entities d ON d.parent_id = g.id AND d.level = 'ad' WHERE g.parent_id = c.id AND d.status <> 'removed'
				AND COALESCE(d.details->>'`+connectors.AdDetailPolicy+`', '') <> 'disapproved'),
			`+strings.Replace(spend, "%s", "c.id", 1)+`, COALESCE(c.details, '{}')
			FROM ad_entities c JOIN ad_accounts a ON a.id = c.ad_account_id WHERE c.workspace_id = $1 AND c.id = $2 AND c.level = 'campaign'`, ws, id).
			Scan(&f.name, &f.status, &f.providerStatus, &f.campaignID, &f.campaignName, &f.campaignStatus, &f.groupName, &f.currency, &f.disapproved, &f.spend30, &f.details)
	case "items":
		return f, s.pool.QueryRow(ctx, `SELECT e.name, e.status, COALESCE(e.provider_status, ''), c.id::text, c.name, c.status, g.name, a.currency,
			COALESCE(e.details->>'`+connectors.AdDetailPolicy+`', '') = 'disapproved',
			`+strings.Replace(spend, "%s", "e.id", 1)+`, COALESCE(e.details, '{}')
			FROM ad_entities e JOIN ad_entities g ON g.id = e.parent_id JOIN ad_entities c ON c.id = g.parent_id AND c.level = 'campaign'
			JOIN ad_accounts a ON a.id = e.ad_account_id
			WHERE e.workspace_id = $1 AND e.id = $2 AND e.level = 'ad'`, ws, id).
			Scan(&f.name, &f.status, &f.providerStatus, &f.campaignID, &f.campaignName, &f.campaignStatus, &f.groupName, &f.currency, &f.disapproved, &f.spend30, &f.details)
	default:
		f.details = map[string]any{}
		var match string
		err := s.pool.QueryRow(ctx, `SELECT k.text, k.status, k.match_type, c.id::text, c.name, c.status, g.name, a.currency,
			(SELECT COALESCE(sum(m.spend_micros), 0) FROM ad_keyword_metrics_daily m WHERE m.keyword_id = k.id AND m.day > current_date - 30)
			FROM ad_keywords k JOIN ad_entities g ON g.id = k.ad_group_id JOIN ad_entities c ON c.id = g.parent_id AND c.level = 'campaign'
			JOIN ad_accounts a ON a.id = k.ad_account_id
			WHERE k.workspace_id = $1 AND k.id = $2`, ws, id).
			Scan(&f.name, &f.status, &match, &f.campaignID, &f.campaignName, &f.campaignStatus, &f.groupName, &f.currency, &f.spend30)
		f.details["match_type"] = match
		return f, err
	}
}

// removalSnapshot is what is kept in the audit log with a removal, so a
// person can see exactly what went and make it again by hand: its name,
// text and links, where it was, its status and the last 30 days' spend.
func (s *Server) removalSnapshot(ctx context.Context, ws, kind, id string) map[string]any {
	f, err := s.removalFacts(ctx, ws, kind, id)
	if err != nil {
		return nil
	}
	snap := map[string]any{"name": f.name, "status": f.status, "provider_status": f.providerStatus, "campaign_id": f.campaignID,
		"campaign_name": f.campaignName, "campaign_status": f.campaignStatus, "spend_30d_micros": f.spend30, "currency": f.currency}
	if f.groupName != "" {
		snap["ad_group_name"] = f.groupName
	}
	if kind != "campaigns" && len(f.details) > 0 {
		snap["details"] = f.details
	}
	return snap
}

// canRemove answers unless the caller may remove a kind of thing, and
// the body says confirm: true. A person needs ads.remove in their role;
// a token needs it among its scopes, unless the action layer let it
// through by a grant naming the removal (or a person approved it).
func canRemove(w http.ResponseWriter, r *http.Request, kind string) bool {
	a := accessFrom(r.Context())
	what := removalWord(kind)
	switch {
	case a.ViewID != "":
		writeError(w, r, http.StatusForbidden, "forbidden", "a support view is read-only")
		return false
	case a.TokenID != "" && (a.TokenActionGate || a.TokenRequiresMission):
		if actionctx.ID(r.Context()) == "" {
			writePermissionRequired(w, r, adRemovePermission[kind], "removing a "+what+" can't be undone: this agent needs the permission "+adRemovePermission[kind]+", which a person turns on for it under Agents")
			return false
		}
	case !auth.Grants(a.Scopes, auth.ScopeAdsRemove):
		if a.TokenID != "" {
			writePermissionRequired(w, r, auth.ScopeAdsRemove, "removing a "+what+" can't be undone, so this API token needs the scope ads.remove: make a token with it under Settings, API tokens")
		} else {
			writePermissionRequired(w, r, auth.ScopeAdsRemove, "removing a "+what+" can't be undone, so it needs the permission \"Remove ad campaigns, ads and keywords\" in your role: an admin turns it on under Settings, Roles")
		}
		return false
	}
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &in) {
		return false
	}
	if !in.Confirm {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "confirm must be true: removing a "+what+" can't be undone in the ad platform")
		return false
	}
	return true
}
