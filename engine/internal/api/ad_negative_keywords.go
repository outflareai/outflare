package api

// Excluding a search from a campaign: a person, or an agent within its
// grants, adds a negative keyword so the campaign stops paying for a
// search that brings nothing. It is the change a negative_add finding
// makes (adsync.ChangeNegativeAdd), asked for directly: an ad_changes row
// a job carries out on the workspace's own connection. A negative can
// only spend less.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

func (s *Server) routesAdNegativeKeywords(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/brands/{brand}/ads/campaigns/{id}/negative-keywords", s.onBrand(auth.ScopeAdsManage, s.addAdNegativeKeyword))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/campaigns/{id}/keywords", s.onBrand(auth.ScopeAdsManage, s.addAdKeyword))
}

// addAdKeyword adds a search as a keyword of one of the campaign's ad
// groups: the change a keyword_add finding makes, asked for directly. A
// new keyword can spend more, so only a person on their own session adds
// one; an agent or a token gets a finding to approve instead.
func (s *Server) addAdKeyword(w http.ResponseWriter, r *http.Request) {
	if a := accessFrom(r.Context()); a.ActorType != "human" || a.TokenID != "" || a.ViewID != "" {
		writeError(w, r, http.StatusForbidden, "person_only", "a new keyword can spend more, so only a person, signed in, adds one: ask them to do it in Outflare under Paid ads")
		return
	}
	s.queueAdKeyword(w, r, adsync.ChangeKeywordAdd)
}

// negativeKeywordMax is the longest keyword Google takes.
const negativeKeywordMax = 80

func (s *Server) addAdNegativeKeyword(w http.ResponseWriter, r *http.Request) {
	s.queueAdKeyword(w, r, adsync.ChangeNegativeAdd)
}

// queueAdKeyword queues a keyword (kind keyword_add, in one ad group) or a
// negative keyword (negative_add, in the campaign or one ad group).
func (s *Server) queueAdKeyword(w http.ResponseWriter, r *http.Request, kind string) {
	negative := kind == adsync.ChangeNegativeAdd
	if !s.needIntegrations(w, r) {
		return
	}
	var in struct {
		Keyword   string `json:"keyword"`
		MatchType string `json:"match_type"`
		AdGroupID string `json:"ad_group_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	keyword := strings.Join(strings.Fields(in.Keyword), " ")
	match := strings.ToLower(strings.TrimSpace(in.MatchType))
	if match == "" {
		match = "exact"
	}
	if keyword == "" || len(keyword) > negativeKeywordMax || len(strings.Fields(keyword)) > 10 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "keyword is the search, as people typed it: up to 80 characters and 10 words")
		return
	}
	if match != "exact" && match != "phrase" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "match_type is exact (only this search) or phrase (every search that contains it)")
		return
	}
	if !negative && in.AdGroupID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "ad_group_id is the ad group the keyword goes in")
		return
	}
	if in.AdGroupID != "" && !isUUID(in.AdGroupID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "ad_group_id is the id of one of the campaign's ad groups")
		return
	}
	a := accessFrom(r.Context())
	c, ok := s.loadAdCampaign(w, r, nil, a, r.PathValue("id"))
	if !ok {
		return
	}
	if c.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign was removed on the platform")
		return
	}
	after := adsync.ChangeState{Keyword: keyword, MatchType: match}
	if in.AdGroupID != "" {
		err := s.pool.QueryRow(r.Context(), `SELECT external_id FROM ad_entities
			WHERE workspace_id = $1 AND id = $2 AND level = 'ad_group' AND parent_id = $3 AND status <> 'removed'`, a.WorkspaceID, in.AdGroupID, c.id).Scan(&after.AdGroupID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "ad_group_id is not one of this campaign's ad groups")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	// One change at a time for the same keyword, so a retry queues one.
	var queued bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM ad_changes WHERE ad_entity_id = $1 AND kind = $2 AND status = 'queued'
		AND lower(after->>'keyword') = lower($3) AND after->>'match_type' = $4 AND COALESCE(after->>'ad_group_id', '') = $5)`,
		c.id, kind, keyword, match, after.AdGroupID).Scan(&queued); err != nil {
		s.internal(w, r, err)
		return
	}
	if queued {
		writeError(w, r, http.StatusConflict, "not_active", "this keyword is already queued; GET .../ads/changes shows it")
		return
	}
	if !s.keywordManagerFor(w, r, a.WorkspaceID, c.integrationID) {
		return
	}
	var out adChangeOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.changeAdCampaign(r.Context(), tx, a, c, kind, after, nil)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "change": out})
}
