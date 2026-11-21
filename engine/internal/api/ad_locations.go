package api

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// A campaign's places set together, and a campaign renamed.
//
// The places call takes the whole of a side as it should be: `included`
// (the places it shows in; empty, or target_all_locations, for every
// place) and `excluded` (the places it is kept out of). A side left out
// stays exactly as it is, so excluding nothing new never drops an
// exclusion. The preview route answers what would be added and taken out
// without changing anything, with the mark of the places as they are; the
// change carries that mark and is refused, there and again in its job,
// when the campaign's places have changed since.

const adLocationsMax = 500

type adLocationsIn struct {
	TargetAllLocations  *bool     `json:"target_all_locations"`
	Included            *[]string `json:"included"`
	Excluded            *[]string `json:"excluded"`
	ExpectedFingerprint string    `json:"expected_fingerprint"`
}

// planAdLocations reads the request and the campaign's places as they are
// now, and works out the change. It answers the error itself.
func (s *Server) planAdLocations(w http.ResponseWriter, r *http.Request) (adCampaignRow, connectors.AdTargeting, connectors.AdLocationChange, bool) {
	var (
		none connectors.AdTargeting
		plan connectors.AdLocationChange
	)
	a := accessFrom(r.Context())
	var in adLocationsIn
	if !decode(w, r, &in) {
		return adCampaignRow{}, none, plan, false
	}
	bad := func(msg string) (adCampaignRow, connectors.AdTargeting, connectors.AdLocationChange, bool) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return adCampaignRow{}, none, plan, false
	}
	if in.TargetAllLocations != nil {
		switch {
		case *in.TargetAllLocations && in.Included != nil && len(*in.Included) > 0:
			return bad("target_all_locations shows the campaign everywhere, so it takes no included places")
		case *in.TargetAllLocations:
			in.Included = &[]string{}
		case in.Included == nil || len(*in.Included) == 0:
			return bad("target_all_locations false needs the places to show in, as included")
		}
	}
	if in.Included == nil && in.Excluded == nil {
		return bad("give included (the places to show in; empty, or target_all_locations true, for everywhere), excluded (the places to keep out of), or both; a side left out stays as it is")
	}
	for _, side := range []*[]string{in.Included, in.Excluded} {
		if side != nil && len(*side) > adLocationsMax {
			return bad("at most 500 places a side")
		}
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, r.PathValue("id"))
	if !ok {
		return c, none, plan, false
	}
	if c.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign was removed on the platform")
		return c, none, plan, false
	}
	o, t, ok := s.adTargeterFor(w, r, a.WorkspaceID, c.integrationID)
	if !ok {
		return c, none, plan, false
	}
	ls, ok := o.Connector.(connectors.AdLocationSetter)
	if !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" places can't be set together here yet; add or remove a country with POST .../countries")
		return c, none, plan, false
	}
	var acct connectors.AdAccount
	err := s.pool.QueryRow(r.Context(), `SELECT external_id, name, currency, time_zone, meta FROM ad_accounts WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, c.accountID).
		Scan(&acct.ID, &acct.Name, &acct.Currency, &acct.TimeZone, &acct.Meta)
	if err != nil {
		s.internal(w, r, err)
		return c, none, plan, false
	}
	resolve := func(refs *[]string) ([]connectors.AdLocation, error) {
		if refs == nil {
			return nil, nil
		}
		got, err := ls.ResolveLocations(r.Context(), o.Client, o.Creds, acct, *refs)
		if err == nil && got == nil {
			got = []connectors.AdLocation{}
		}
		return got, err
	}
	included, err := resolve(in.Included)
	if err != nil {
		s.connectorFailed(w, r, err)
		return c, none, plan, false
	}
	excluded, err := resolve(in.Excluded)
	if err != nil {
		s.connectorFailed(w, r, err)
		return c, none, plan, false
	}
	cur, err := t.CampaignTargeting(r.Context(), o.Client, o.Creds, acct, c.externalID)
	if err != nil {
		s.connectorFailed(w, r, err)
		return c, none, plan, false
	}
	if fp := strings.TrimSpace(in.ExpectedFingerprint); fp != "" && fp != cur.LocationFingerprint() {
		writeError(w, r, http.StatusConflict, "conflict", "the campaign's places are not the ones this was planned on (location_fingerprint is now "+cur.LocationFingerprint()+"): read the targeting again and plan it on what is there")
		return c, none, plan, false
	}
	plan, err = connectors.PlanLocations(cur, included, excluded)
	if err != nil {
		var ce *connectors.Error
		if errors.As(err, &ce) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", ce.Message)
		} else {
			s.internal(w, r, err)
		}
		return c, none, plan, false
	}
	return c, cur, plan, true
}

// adLocationsView is a planned change as the API shows it.
func adLocationsView(cur connectors.AdTargeting, plan connectors.AdLocationChange) map[string]any {
	list := func(l []connectors.AdLocation) []connectors.AdLocation {
		if l == nil {
			return []connectors.AdLocation{}
		}
		return l
	}
	after := map[string]any{"target_all_locations": plan.TargetAllLocations, "included_locations": list(cur.IncludedLocations), "excluded_locations": list(cur.ExcludedLocations)}
	if plan.Included != nil {
		after["included_locations"] = plan.Included
	}
	if plan.Excluded != nil {
		after["excluded_locations"] = plan.Excluded
	}
	return map[string]any{
		"location_fingerprint": plan.Fingerprint,
		"changes":              !plan.Empty(),
		"reaches_more":         plan.ReachesMore(),
		"before":               map[string]any{"target_all_locations": cur.TargetAllLocations, "included_locations": list(cur.IncludedLocations), "excluded_locations": list(cur.ExcludedLocations)},
		"after":                after,
		"add_included":         list(plan.AddIncluded), "remove_included": list(plan.RemoveIncluded),
		"add_excluded": list(plan.AddExcluded), "remove_excluded": list(plan.RemoveExcluded),
		"observed_at": time.Now().UTC(),
	}
}

// previewAdLocations answers what a places change would add and take out.
// It only reads.
func (s *Server) previewAdLocations(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	_, cur, plan, ok := s.planAdLocations(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, adLocationsView(cur, plan))
}

func (s *Server) setAdLocations(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	c, cur, plan, ok := s.planAdLocations(w, r)
	if !ok {
		return
	}
	if plan.Empty() {
		writeError(w, r, http.StatusConflict, "conflict", "the campaign's places are already what was asked for, so there is nothing to change")
		return
	}
	if s.adChangeQueued(w, r, c.id, adsync.ChangeLocations) {
		return
	}
	var out adChangeOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.changeAdCampaign(r.Context(), tx, a, c, adsync.ChangeLocations, adsync.ChangeState{Locations: &plan}, nil)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "change": out, "plan": adLocationsView(cur, plan)})
}

func (s *Server) renameAdCampaign(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 255 || strings.ContainsAny(name, "\x00\n\r") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is the campaign's new name, on one line and at most 255 characters")
		return
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, r.PathValue("id"))
	if !ok {
		return
	}
	if c.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign was removed on the platform")
		return
	}
	if name == c.name {
		writeError(w, r, http.StatusConflict, "conflict", "the campaign already has that name")
		return
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, c.integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign's ad platform is no longer connected")
		return
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	if _, ok := o.Connector.(connectors.AdCampaignRenamer); !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" campaigns can't be renamed here yet")
		return
	}
	if s.adChangeQueued(w, r, c.id, adsync.ChangeRename) {
		return
	}
	var out adChangeOut
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.changeAdCampaign(r.Context(), tx, a, c, adsync.ChangeRename, adsync.ChangeState{Name: name}, nil)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "change": out})
}
