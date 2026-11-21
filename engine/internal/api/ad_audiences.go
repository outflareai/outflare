package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Audiences: a brand names one of its lists as an audience once and
// chooses the ad accounts it goes to, so the same people reach several ad
// networks (adconv/audiences.go keeps each network's list in step). A
// network that only takes uploads gets a file of hashed emails and phones
// instead.

func (s *Server) routesAdAudiences(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAdsManage
	mux.HandleFunc("GET /v1/brands/{brand}/ads/audiences", s.onBrand(read, s.listBrandAdAudiences))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/audiences", s.onBrand(manage, s.createBrandAdAudience))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/audiences/{id}", s.onBrand(read, s.getBrandAdAudience))
	mux.HandleFunc("PATCH /v1/brands/{brand}/ads/audiences/{id}", s.onBrand(manage, s.updateBrandAdAudience))
	mux.HandleFunc("DELETE /v1/brands/{brand}/ads/audiences/{id}", s.onBrand(manage, s.deleteBrandAdAudience))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/audiences/{id}/sync", s.onBrand(manage, s.syncBrandAdAudience))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/audiences/{id}/file", s.onBrand(manage, s.exportBrandAdAudience))
}

type adAudienceList struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Members int    `json:"members"`
}

type adAudienceDestination struct {
	ID            string `json:"id"`
	AdAccountID   string `json:"ad_account_id"`
	AccountName   string `json:"account_name"`
	Connector     string `json:"connector"`
	ConnectorName string `json:"connector_name"`
	// ExternalID is the list's id on the network; "" until the first
	// sync makes it.
	ExternalID string `json:"external_id"`
	// Status is waiting (not synced yet), synced or error.
	Status  string                  `json:"status"`
	Members int                     `json:"members"`
	Skipped int                     `json:"skipped"`
	Size    connectors.AudienceSize `json:"size"`
	Error   string                  `json:"error"`
	// CanExclude says the network takes the list as an exclusion on a
	// campaign from here; ExcludedCampaigns is how many campaigns have
	// it, or have it queued.
	CanExclude        bool       `json:"can_exclude"`
	ExcludedCampaigns int        `json:"excluded_campaigns"`
	SyncedAt          *time.Time `json:"synced_at"`
	CreatedAt         time.Time  `json:"created_at"`
}

type adAudienceOut struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Exclude makes the audience one whose people every campaign in its
	// ad accounts stops showing to.
	Exclude bool `json:"exclude"`
	// List is null once the list was deleted: the audience no longer
	// syncs.
	List         *adAudienceList         `json:"list"`
	Destinations []adAudienceDestination `json:"destinations"`
	CreatedAt    time.Time               `json:"created_at"`
	UpdatedAt    time.Time               `json:"updated_at"`
}

// adAudienceAccount is one of the brand's ad accounts an audience could
// go to.
type adAudienceAccount struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Connector     string `json:"connector"`
	ConnectorName string `json:"connector_name"`
	// CanSync is false for a network Outflare can't fill a list on: it
	// takes the audience's file instead.
	CanSync bool `json:"can_sync"`
	// CanExclude is true for a network that takes a list as an exclusion
	// on a campaign from here; on the others it is set in the network.
	CanExclude bool `json:"can_exclude"`
}

type rowsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (s *Server) audienceConnector(id string) (name string, canSync bool) {
	if s.integrations == nil || s.integrations.Registry == nil {
		return id, false
	}
	conn, ok := s.integrations.Registry.Get(id)
	if !ok {
		return id, false
	}
	_, creates := conn.(connectors.AudienceCreator)
	return conn.Definition().Name, creates && conn.Definition().Has(connectors.CapAdsAudiences)
}

// audienceCanExclude reports whether a network takes a customer list as
// an exclusion on a campaign from here.
func (s *Server) audienceCanExclude(id string) bool {
	if s.integrations == nil || s.integrations.Registry == nil {
		return false
	}
	conn, ok := s.integrations.Registry.Get(id)
	return ok && adconv.CanExclude(conn)
}

// adAudiences reads the brand's audiences (one when id is set) with their
// destinations.
func (s *Server) adAudiences(ctx context.Context, q rowsQuerier, ws, brand, id string) ([]adAudienceOut, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.name, a.enabled, a.exclude, a.created_at, a.updated_at, l.id, coalesce(l.name, ''), coalesce(l.members, 0)
		FROM ad_audiences a LEFT JOIN smart_lists l ON l.id = a.list_id AND l.workspace_id = a.workspace_id
		WHERE a.workspace_id = $1 AND a.brand_id = $2 AND ($3 = '' OR a.id::text = $3)
		ORDER BY lower(a.name), a.id`, ws, brand, id)
	if err != nil {
		return nil, err
	}
	out := []adAudienceOut{}
	at := map[string]int{}
	for rows.Next() {
		var a adAudienceOut
		var listID *string
		var l adAudienceList
		if err := rows.Scan(&a.ID, &a.Name, &a.Enabled, &a.Exclude, &a.CreatedAt, &a.UpdatedAt, &listID, &l.Name, &l.Members); err != nil {
			rows.Close()
			return nil, err
		}
		if listID != nil {
			l.ID = *listID
			a.List = &l
		}
		a.Destinations = []adAudienceDestination{}
		at[a.ID] = len(out)
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT d.audience_id, d.id, d.ad_account_id, c.name, c.connector, d.external_id, d.members, d.skipped, d.size, d.error, d.synced_at, d.created_at,
			(SELECT count(*)::int FROM (SELECT DISTINCT ON (ch.ad_entity_id) ch.kind, ch.status FROM ad_changes ch
				JOIN ad_entities e ON e.id = ch.ad_entity_id AND e.ad_account_id = d.ad_account_id
				WHERE ch.workspace_id = d.workspace_id AND d.external_id <> '' AND ch.kind IN ('exclusion_add', 'exclusion_remove')
				  AND ch.after->'exclusion'->>'kind' = 'audience' AND ch.after->'exclusion'->>'value' = d.external_id
				ORDER BY ch.ad_entity_id, ch.created_at DESC, ch.id DESC) x WHERE x.kind = 'exclusion_add' AND x.status <> 'failed')
		FROM ad_audience_destinations d
		JOIN ad_audiences a ON a.id = d.audience_id
		JOIN ad_accounts c ON c.id = d.ad_account_id
		WHERE d.workspace_id = $1 AND a.brand_id = $2 AND ($3 = '' OR a.id::text = $3)
		ORDER BY c.connector, lower(c.name), d.id`, ws, brand, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d adAudienceDestination
		var audience string
		var size []byte
		if err := rows.Scan(&audience, &d.ID, &d.AdAccountID, &d.AccountName, &d.Connector, &d.ExternalID, &d.Members, &d.Skipped, &size, &d.Error, &d.SyncedAt, &d.CreatedAt, &d.ExcludedCampaigns); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(size, &d.Size)
		d.ConnectorName, _ = s.audienceConnector(d.Connector)
		d.CanExclude = s.audienceCanExclude(d.Connector)
		switch {
		case d.Error != "":
			d.Status = "error"
		case d.SyncedAt == nil:
			d.Status = "waiting"
		default:
			d.Status = "synced"
		}
		if i, ok := at[audience]; ok {
			out[i].Destinations = append(out[i].Destinations, d)
		}
	}
	return out, rows.Err()
}

// adAudienceAccounts lists the brand's active ad accounts.
func (s *Server) adAudienceAccounts(ctx context.Context, q rowsQuerier, ws, brand string) ([]adAudienceAccount, error) {
	rows, err := q.Query(ctx, `SELECT id, name, connector FROM ad_accounts
		WHERE workspace_id = $1 AND status = 'active' AND (brand_id IS NULL OR brand_id::text = $2)
		ORDER BY connector, lower(name), id`, ws, brand)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []adAudienceAccount{}
	for rows.Next() {
		var a adAudienceAccount
		if err := rows.Scan(&a.ID, &a.Name, &a.Connector); err != nil {
			return nil, err
		}
		a.ConnectorName, a.CanSync = s.audienceConnector(a.Connector)
		a.CanExclude = s.audienceCanExclude(a.Connector)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Server) listBrandAdAudiences(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := s.adAudiences(r.Context(), s.pool, a.WorkspaceID, a.Brand, "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	accounts, err := s.adAudienceAccounts(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "accounts": accounts})
}

// loadAdAudience answers 404 for an id that isn't one of the brand's
// audiences.
func (s *Server) loadAdAudience(w http.ResponseWriter, r *http.Request, a *access) (adAudienceOut, bool) {
	id := r.PathValue("id")
	if isUUID(id) {
		list, err := s.adAudiences(r.Context(), s.pool, a.WorkspaceID, a.Brand, id)
		if err != nil {
			s.internal(w, r, err)
			return adAudienceOut{}, false
		}
		if len(list) == 1 {
			return list[0], true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "audience not found")
	return adAudienceOut{}, false
}

func (s *Server) getBrandAdAudience(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadAdAudience(w, r, accessFrom(r.Context())); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// adAudienceInput checks what a create or an update names: the list is a
// list of people of this brand (or every brand's), and each account is
// one of the brand's active ad accounts on a network Outflare can fill a
// list on. A nil field is one the caller left out.
func (s *Server) adAudienceInput(w http.ResponseWriter, r *http.Request, a *access, name, listID *string, accounts *[]string) bool {
	ctx := r.Context()
	if name != nil {
		*name = strings.TrimSpace(*name)
		if n := len([]rune(*name)); n < 1 || n > 120 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "name is 1 to 120 characters")
			return false
		}
	}
	if listID != nil {
		var recordType string
		err := pgx.ErrNoRows
		if isUUID(*listID) {
			err = s.pool.QueryRow(ctx, `SELECT record_type FROM smart_lists WHERE workspace_id = $1 AND id = $2 AND (brand_id IS NULL OR brand_id::text = $3)`,
				a.WorkspaceID, *listID, a.Brand).Scan(&recordType)
		}
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, r, http.StatusBadRequest, "invalid_request", "list_id is one of the brand's lists")
			return false
		case err != nil:
			s.internal(w, r, err)
			return false
		case recordType != "person":
			writeError(w, r, http.StatusBadRequest, "invalid_request", "list_id is a list of people: ad networks match people, not companies")
			return false
		}
	}
	if accounts != nil {
		if len(*accounts) > 20 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "ad_account_ids holds at most 20 ad accounts")
			return false
		}
		have, err := s.adAudienceAccounts(ctx, s.pool, a.WorkspaceID, a.Brand)
		if err != nil {
			s.internal(w, r, err)
			return false
		}
		byID := map[string]adAudienceAccount{}
		for _, c := range have {
			byID[c.ID] = c
		}
		seen := map[string]bool{}
		uniq := []string{}
		for _, id := range *accounts {
			c, ok := byID[id]
			switch {
			case !ok:
				writeError(w, r, http.StatusBadRequest, "invalid_request", "ad_account_ids are the brand's active ad accounts")
				return false
			case !c.CanSync:
				writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", c.ConnectorName+" takes audiences as an uploaded file: leave it out, and download the audience's file instead")
				return false
			}
			if !seen[id] {
				seen[id] = true
				uniq = append(uniq, id)
			}
		}
		*accounts = uniq
	}
	return true
}

// setAdAudienceDestinations makes the audience's destinations exactly the
// accounts named, and queues a sync of each. A destination taken away
// keeps its list on the network, which stops changing.
func setAdAudienceDestinations(ctx context.Context, tx pgx.Tx, ws, brand, id string, accounts []string, key string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM ad_audience_destinations WHERE workspace_id = $1 AND audience_id = $2 AND NOT (ad_account_id = ANY($3::uuid[]))`, ws, id, accounts); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ad_audience_destinations (workspace_id, brand_id, audience_id, ad_account_id)
		SELECT $1, $2, $3, unnest($4::uuid[]) ON CONFLICT (audience_id, ad_account_id) DO NOTHING`, ws, brand, id, accounts); err != nil {
		return err
	}
	return queueAdAudience(ctx, tx, ws, id, key)
}

// queueAdAudience queues a sync of each of an audience's destinations,
// when the audience is on.
func queueAdAudience(ctx context.Context, tx pgx.Tx, ws, id, key string) error {
	rows, err := tx.Query(ctx, `SELECT d.id FROM ad_audience_destinations d JOIN ad_audiences a ON a.id = d.audience_id
		WHERE d.workspace_id = $1 AND d.audience_id = $2 AND a.enabled AND a.list_id IS NOT NULL`, ws, id)
	if err != nil {
		return err
	}
	dests, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, d := range dests {
		if _, err := adconv.EnqueueAudience(ctx, tx, ws, d, key); err != nil {
			return err
		}
	}
	return nil
}

func adAudienceKey(reason string) string {
	return reason + ":" + time.Now().UTC().Format("2006-01-02T15:04:05")
}

func (s *Server) createBrandAdAudience(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Name         string   `json:"name"`
		ListID       string   `json:"list_id"`
		AdAccountIDs []string `json:"ad_account_ids"`
		Exclude      bool     `json:"exclude"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.AdAccountIDs == nil {
		in.AdAccountIDs = []string{}
	}
	if !s.adAudienceInput(w, r, a, &in.Name, &in.ListID, &in.AdAccountIDs) {
		return
	}
	ctx := r.Context()
	var out adAudienceOut
	taken := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO ad_audiences (workspace_id, brand_id, name, list_id, created_by, exclude) VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (brand_id, lower(name)) DO NOTHING RETURNING id`, a.WorkspaceID, a.Brand, in.Name, in.ListID, actorRef(a), in.Exclude).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			taken = true
			return nil
		}
		if err != nil {
			return err
		}
		if err := setAdAudienceDestinations(ctx, tx, a.WorkspaceID, a.Brand, id, in.AdAccountIDs, adAudienceKey("created")); err != nil {
			return err
		}
		if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ad_audience.created", "ad_audience", id,
			map[string]any{"name": in.Name, "list_id": in.ListID, "ad_account_ids": in.AdAccountIDs, "brand_id": a.Brand, "exclude": in.Exclude},
			map[string]any{"destinations": len(in.AdAccountIDs), "exclude": in.Exclude}); err != nil {
			return err
		}
		list, err := s.adAudiences(ctx, tx, a.WorkspaceID, a.Brand, id)
		if err == nil && len(list) == 1 {
			out = list[0]
		}
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if taken {
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has an audience with this name")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateBrandAdAudience(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Name         *string   `json:"name"`
		ListID       *string   `json:"list_id"`
		Enabled      *bool     `json:"enabled"`
		Exclude      *bool     `json:"exclude"`
		AdAccountIDs *[]string `json:"ad_account_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, ok := s.loadAdAudience(w, r, a)
	if !ok {
		return
	}
	if !s.adAudienceInput(w, r, a, in.Name, in.ListID, in.AdAccountIDs) {
		return
	}
	ctx := r.Context()
	var out adAudienceOut
	taken := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ad_audiences SET name = COALESCE($3, name), list_id = COALESCE($4::uuid, list_id), enabled = COALESCE($5, enabled),
				exclude = COALESCE($6, exclude), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID, in.Name, in.ListID, in.Enabled, in.Exclude)
		if isUniqueViolation(err) {
			taken = true
			return err
		}
		if err != nil {
			return err
		}
		// An audience that stops excluding comes off the campaigns the
		// sync put it on. One that starts is put on by its next sync,
		// queued below.
		if in.Exclude != nil && !*in.Exclude && v.Exclude {
			for _, d := range v.Destinations {
				queued, err := adconv.QueueAudienceExclusions(ctx, tx, a.WorkspaceID, d.ID, false, actorRef(a))
				if err != nil {
					return err
				}
				for _, q := range queued {
					if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ad_campaign.exclusion_removed", "ad_campaign", q.CampaignID,
						map[string]any{"change_id": q.ChangeID, "campaign_id": q.CampaignID, "external_id": q.ExternalID, "connector": q.Connector, "brand_id": a.Brand,
							"after": map[string]any{"exclusion": q.Exclusion}, "audience_id": v.ID},
						map[string]any{"connector": q.Connector, "kind": q.Kind, "exclusion_kind": q.Exclusion.Kind}); err != nil {
						return err
					}
				}
			}
		}
		key := adAudienceKey("updated")
		if in.AdAccountIDs != nil {
			err = setAdAudienceDestinations(ctx, tx, a.WorkspaceID, a.Brand, v.ID, *in.AdAccountIDs, key)
		} else {
			err = queueAdAudience(ctx, tx, a.WorkspaceID, v.ID, key)
		}
		if err != nil {
			return err
		}
		data := map[string]any{"brand_id": a.Brand}
		props := map[string]any{}
		if in.Name != nil {
			data["name"] = *in.Name
		}
		if in.ListID != nil {
			data["list_id"] = *in.ListID
			props["list_changed"] = v.List == nil || v.List.ID != *in.ListID
		}
		if in.Enabled != nil {
			data["enabled"], props["enabled"] = *in.Enabled, *in.Enabled
		}
		if in.Exclude != nil {
			data["exclude"], props["exclude"] = *in.Exclude, *in.Exclude
		}
		if in.AdAccountIDs != nil {
			data["ad_account_ids"], props["destinations"] = *in.AdAccountIDs, len(*in.AdAccountIDs)
		}
		if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ad_audience.updated", "ad_audience", v.ID, data, props); err != nil {
			return err
		}
		list, err := s.adAudiences(ctx, tx, a.WorkspaceID, a.Brand, v.ID)
		if err == nil && len(list) == 1 {
			out = list[0]
		}
		return err
	})
	if taken {
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has an audience with this name")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteBrandAdAudience stops an audience. The lists it made on the
// networks stay, with whoever is in them, and stop changing.
func (s *Server) deleteBrandAdAudience(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.loadAdAudience(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM ad_audiences WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ad_audience.deleted", "ad_audience", v.ID,
			map[string]any{"name": v.Name, "brand_id": a.Brand}, map[string]any{"destinations": len(v.Destinations)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncBrandAdAudience(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.loadAdAudience(w, r, a)
	if !ok {
		return
	}
	switch {
	case !v.Enabled:
		writeError(w, r, http.StatusConflict, "conflict", "the audience is off: turn it on to sync it")
		return
	case v.List == nil:
		writeError(w, r, http.StatusConflict, "conflict", "the audience's list was deleted: choose another list")
		return
	case len(v.Destinations) == 0:
		writeError(w, r, http.StatusConflict, "conflict", "the audience goes to no ad account yet: add one")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := queueAdAudience(ctx, tx, a.WorkspaceID, v.ID, adAudienceKey("asked")); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ad_audience.sync_requested", "ad_audience", v.ID,
			map[string]any{"brand_id": a.Brand}, map[string]any{"destinations": len(v.Destinations)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// exportBrandAdAudience writes the audience as a CSV of hashed emails and
// phone numbers, for a network that takes audiences as an uploaded file.
func (s *Server) exportBrandAdAudience(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.loadAdAudience(w, r, a)
	if !ok {
		return
	}
	if v.List == nil {
		writeError(w, r, http.StatusConflict, "conflict", "the audience's list was deleted: choose another list")
		return
	}
	ctx := r.Context()
	var kind string
	if err := s.pool.QueryRow(ctx, `SELECT kind FROM smart_lists WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.List.ID).Scan(&kind); err != nil {
		s.internal(w, r, err)
		return
	}
	people, skipped, err := adconv.AudienceFile(ctx, s.pool, a.WorkspaceID, a.Brand, v.List.ID, kind)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ad_audience.exported", "ad_audience", v.ID,
			map[string]any{"brand_id": a.Brand, "people": len(people), "skipped": skipped}, map[string]any{"people": len(people), "skipped": skipped})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audience.csv"`)
	w.Header().Set("X-Outflare-Skipped", strconv.Itoa(skipped))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"email_sha256", "phone_number_sha256"})
	for _, p := range people {
		for i := 0; i < max(len(p.Emails), len(p.Phones)); i++ {
			row := []string{"", ""}
			if i < len(p.Emails) {
				row[0] = p.Emails[i]
			}
			if i < len(p.Phones) {
				row[1] = p.Phones[i]
			}
			_ = cw.Write(row)
		}
	}
	cw.Flush()
}
