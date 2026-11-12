package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/conversions"
)

// The conversion feed (plans/paid-ads/plan.md, "Conversion feed"): one
// call makes, on an ad account, Outflare's three conversion actions
// (sign-up, first payment, repeat payment), the rules that feed them with
// the right customer type, and the Customer Match list of paying
// customers; another sets the account's new customer acquisition goal.
// Sends and the list's refresh run in jobs (engine/internal/adconv).

// ncaNewOnlyMinMembers is how many people the payers list needs before
// campaigns may serve new customers only: Google says a list under 1,000
// weakens the goal.
const ncaNewOnlyMinMembers = 1000

func (s *Server) routesAdFeed(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/ads/accounts/{id}/feed", s.onBrand(auth.ScopeAnalyticsRead, s.getAdFeed))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/accounts/{id}/feed", s.onBrand(auth.ScopeAdsManage, s.createAdFeed))
	mux.HandleFunc("PUT /v1/brands/{brand}/ads/accounts/{id}/feed", s.onBrand(auth.ScopeAdsManage, s.updateAdFeed))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/feed-health", s.onBrand(auth.ScopeAnalyticsRead, s.getAdFeedHealth))
}

type adFeedAction struct {
	Role     string `json:"role"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

type adFeedNCA struct {
	Mode                  string     `json:"mode"`
	AdditionalValueMicros int64      `json:"additional_value_micros"`
	SetAt                 *time.Time `json:"set_at"`
}

type adFeedView struct {
	AccountID       string             `json:"ad_account_id"`
	Connector       string             `json:"connector"`
	Ready           bool               `json:"ready"`
	Transport       string             `json:"transport"`
	TransportReason string             `json:"transport_reason"`
	Actions         []adFeedAction     `json:"actions"`
	Rules           []adConversionRule `json:"rules"`
	PayersList      *adconv.PayersList `json:"payers_list"`
	NCA             adFeedNCA          `json:"nca"`
	NewOnlyMembers  int                `json:"nca_new_only_min_members"`
	SetupNeeded     []connectors.Field `json:"setup_needed"`
	CreatedAt       *time.Time         `json:"created_at"`
}

// adFeedAccount is an active ad account of the route's brand, answered 404
// when it isn't one.
type adFeedAccount struct {
	id, integrationID, connector, brand string
	acct                                connectors.AdAccount
}

func (s *Server) adFeedAccount(w http.ResponseWriter, r *http.Request, ac *access, id string) (adFeedAccount, bool) {
	var a adFeedAccount
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad account not found")
		return a, false
	}
	var meta map[string]string
	err := s.pool.QueryRow(r.Context(), `SELECT id, integration_id, connector, coalesce(brand_id::text, ''), external_id, name, currency, time_zone, meta
		FROM ad_accounts WHERE workspace_id = $1 AND id = $2 AND brand_id = $3 AND status = 'active'`, ac.WorkspaceID, id, ac.Brand).Scan(
		&a.id, &a.integrationID, &a.connector, &a.brand, &a.acct.ID, &a.acct.Name, &a.acct.Currency, &a.acct.TimeZone, &meta)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad account not found")
		return a, false
	}
	if err != nil {
		s.internal(w, r, err)
		return a, false
	}
	a.acct.Meta = meta
	return a, true
}

// adFeedView builds the feed as the API shows it. o may be nil when the
// connection was not opened (a read).
func (s *Server) adFeedView(ctx context.Context, q pgx.Tx, ws string, acct adFeedAccount, feed adconv.Feed, o *connectors.Opened) (adFeedView, error) {
	v := adFeedView{AccountID: acct.id, Connector: acct.connector, Ready: feed.Ready(), Transport: feed.Transport, TransportReason: feed.TransportReason,
		Actions: []adFeedAction{}, Rules: []adConversionRule{}, PayersList: feed.PayersList, NCA: adFeedNCA{Mode: connectors.NCAOff},
		NewOnlyMembers: ncaNewOnlyMinMembers, SetupNeeded: []connectors.Field{}, CreatedAt: feed.CreatedAt}
	if v.Transport == "" {
		v.Transport = connectors.TransportDataManager
	}
	for role, a := range feed.Actions {
		v.Actions = append(v.Actions, adFeedAction{Role: role, ID: a.ID, Name: a.Name, Category: a.Category})
	}
	sort.Slice(v.Actions, func(i, j int) bool { return v.Actions[i].Role < v.Actions[j].Role })
	if feed.NCA != nil {
		at := feed.NCA.SetAt
		v.NCA = adFeedNCA{Mode: feed.NCA.Mode, AdditionalValueMicros: feed.NCA.AdditionalValueMicros, SetAt: &at}
	}
	rows, err := q.Query(ctx, adRuleSelect+` WHERE r.workspace_id = $1 AND r.ad_account_id = $2 AND r.feed_role <> 'custom' ORDER BY r.feed_role`, ws, acct.id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		x, err := scanAdRule(rows)
		if err != nil {
			return v, err
		}
		v.Rules = append(v.Rules, x)
	}
	if err := rows.Err(); err != nil {
		return v, err
	}
	if o != nil {
		v.SetupNeeded = setupNeededFor(o)
	}
	return v, nil
}

// adFeedOpen opens the account's connection as a feed manager, answering
// the error itself.
func (s *Server) adFeedOpen(w http.ResponseWriter, r *http.Request, ws string, acct adFeedAccount) (*connectors.Opened, connectors.ConversionFeedManager, bool) {
	o, err := s.integrations.Open(r.Context(), s.pool, ws, acct.integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the ad account's platform is no longer connected")
		return nil, nil, false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return nil, nil, false
	}
	m, ok := o.Connector.(connectors.ConversionFeedManager)
	if !ok || !o.Connector.Definition().Has(connectors.CapAdsFeed) {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" cannot take Outflare's conversion feed yet")
		return nil, nil, false
	}
	return o, m, true
}

func (s *Server) getAdFeed(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	acct, ok := s.adFeedAccount(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	feed, err := adconv.LoadFeed(r.Context(), s.pool, a.WorkspaceID, acct.id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// The connection is opened only to say whether it needs a reconnect;
	// a connection that cannot be opened says so under setup_needed.
	var opened *connectors.Opened
	if s.integrations != nil {
		if o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, acct.integrationID); err == nil {
			opened = o
		}
	}
	var out adFeedView
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		out, err = s.adFeedView(r.Context(), tx, a.WorkspaceID, acct, feed, opened)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if opened == nil {
		out.SetupNeeded = append(out.SetupNeeded, connectors.ReconnectField("The ad account's connection could not be opened: connect it again under Connectors."))
	}
	writeJSON(w, http.StatusOK, out)
}

// createAdFeed sets the feed up on an account.
func (s *Server) createAdFeed(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	ws := a.WorkspaceID
	acct, ok := s.adFeedAccount(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	feed, err := adconv.LoadFeed(r.Context(), s.pool, ws, acct.id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if feed.Ready() {
		writeError(w, r, http.StatusConflict, "conflict", "the conversion feed is already set up on this ad account; GET it, or change it with PUT")
		return
	}
	o, m, ok := s.adFeedOpen(w, r, ws, acct)
	if !ok {
		return
	}
	// A brand whose payments are subscriptions sells subscriptions:
	// Google's SUBSCRIBE_PAID; one-off payments are purchases.
	var subscriptions bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM payment_subscriptions WHERE workspace_id = $1 AND ($2 = '' OR brand_id IS NULL OR brand_id::text = $2))`,
		ws, acct.brand).Scan(&subscriptions); err != nil {
		s.internal(w, r, err)
		return
	}
	first := connectors.ConversionPurchase
	if subscriptions {
		first = connectors.ConversionSubscribePaid
	}
	specs := map[string]connectors.ConversionActionSpec{
		adconv.RoleSignup:        {Name: adconv.ActionNames[adconv.RoleSignup], Category: connectors.ConversionSignup, ClickWindowDays: 30},
		adconv.RoleFirstPayment:  {Name: adconv.ActionNames[adconv.RoleFirstPayment], Category: first, Primary: true, OnePerClick: true, ClickWindowDays: 90},
		adconv.RoleRepeatPayment: {Name: adconv.ActionNames[adconv.RoleRepeatPayment], Category: connectors.ConversionPurchase, ClickWindowDays: 90},
	}
	actions := map[string]adconv.FeedAction{}
	for _, role := range []string{adconv.RoleSignup, adconv.RoleFirstPayment, adconv.RoleRepeatPayment} {
		spec := specs[role]
		id, err := m.CreateConversionAction(r.Context(), o.Client, o.Creds, acct.acct, spec)
		if err != nil {
			s.connectorFailed(w, r, err)
			return
		}
		actions[role] = adconv.FeedAction{ID: id, Name: spec.Name, Category: spec.Category}
	}
	listID, err := m.CreateCustomerList(r.Context(), o.Client, o.Creds, acct.acct, adconv.PayersListName)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	// The sign-up rule sends the brand's sign-up conversion definition
	// when it has one on the signed_up event, else the bare event.
	var signupDef *conversions.Definition
	if d, err := conversions.Get(r.Context(), s.pool, ws, acct.brand, "signup"); err == nil && d.ArchivedAt == nil && d.Event == "signed_up" {
		signupDef = &d
	} else if err != nil && !errors.Is(err, conversions.ErrNotFound) {
		s.internal(w, r, err)
		return
	}
	var createdBy *string
	if a.ActorID != "" {
		createdBy = &a.ActorID
	}
	now := time.Now().UTC()
	feed.Actions = actions
	feed.PayersList = &adconv.PayersList{ID: listID, Name: adconv.PayersListName}
	feed.NCA = &adconv.NCASetting{Mode: connectors.NCAOff, SetAt: now}
	feed.CreatedAt = &now
	feed.Transport, feed.TransportReason, feed.TransportSetAt = "", "", nil
	var out adFeedView
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		for _, fr := range adconv.Rules {
			var conversionID *string
			eventName := fr.Event
			if fr.Role == adconv.RoleSignup && signupDef != nil {
				conversionID = &signupDef.ID
			}
			act := actions[fr.Role]
			if _, err := tx.Exec(r.Context(), `INSERT INTO ad_conversion_rules (workspace_id, ad_account_id, event_name, target_id, target_name, event_type,
					value_property, value_unit, currency, enabled, created_by, conversion_id, feed_role, customer_type)
				VALUES ($1, $2, $3, $4, $5, '', $6, $7, '', true, $8, $9, $10, $11)
				ON CONFLICT (ad_account_id, event_name, target_id, event_type) DO UPDATE SET feed_role = EXCLUDED.feed_role, customer_type = EXCLUDED.customer_type,
					conversion_id = EXCLUDED.conversion_id, value_property = EXCLUDED.value_property, value_unit = EXCLUDED.value_unit, enabled = true, updated_at = now()`,
				ws, acct.id, eventName, act.ID, trimTo(act.Name, 300), fr.ValueProperty, fr.ValueUnit, createdBy, conversionID, fr.Role, fr.CustomerType); err != nil {
				return err
			}
		}
		if err := adconv.SaveFeed(r.Context(), tx, ws, acct.id, feed); err != nil {
			return err
		}
		if _, err := adconv.EnqueuePayers(r.Context(), tx, ws, acct.id, "setup:"+now.Format("2006-01-02T15:04")); err != nil {
			return err
		}
		if out, err = s.adFeedView(r.Context(), tx, ws, acct, feed, o); err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, ws, "ad_feed.created", "ad_account", acct.id,
			map[string]any{"connector": acct.connector, "brand_id": acct.brand, "first_payment_category": first, "payers_list_id": listID, "signup_definition": signupDef != nil},
			map[string]any{"connector": acct.connector, "first_payment_category": first, "signup_definition": signupDef != nil})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// updateAdFeed sets the new customer acquisition goal, or puts the
// account back on the data feed after a fallback.
func (s *Server) updateAdFeed(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	ws := a.WorkspaceID
	var in struct {
		NCA                   *string `json:"nca"`
		AdditionalValueMicros *int64  `json:"additional_value_micros"`
		Transport             *string `json:"transport"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.NCA != nil && *in.NCA != connectors.NCAOff && *in.NCA != connectors.NCABidHigher && *in.NCA != connectors.NCANewOnly {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "nca is off, bid_higher or new_only")
		return
	}
	if in.AdditionalValueMicros != nil && (*in.AdditionalValueMicros < 0 || *in.AdditionalValueMicros > 1_000_000_000_000) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "additional_value_micros is 0 or more, in micros of the account's currency")
		return
	}
	if in.Transport != nil && *in.Transport != connectors.TransportDataManager {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "transport can only be set back to data_manager; Outflare falls back to ads_api on its own")
		return
	}
	if in.NCA == nil && in.AdditionalValueMicros == nil && in.Transport == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "nothing to change: give nca, additional_value_micros or transport")
		return
	}
	acct, ok := s.adFeedAccount(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	feed, err := adconv.LoadFeed(r.Context(), s.pool, ws, acct.id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !feed.Ready() {
		writeError(w, r, http.StatusConflict, "not_active", "set the conversion feed up first (POST .../feed)")
		return
	}
	mode := connectors.NCAOff
	var value int64
	if feed.NCA != nil {
		mode, value = feed.NCA.Mode, feed.NCA.AdditionalValueMicros
	}
	if in.NCA != nil {
		mode = *in.NCA
	}
	if in.AdditionalValueMicros != nil {
		value = *in.AdditionalValueMicros
	}
	if mode == connectors.NCANewOnly && (feed.PayersList == nil || feed.PayersList.Members < ncaNewOnlyMinMembers) {
		members := 0
		if feed.PayersList != nil {
			members = feed.PayersList.Members
		}
		writeError(w, r, http.StatusUnprocessableEntity, "payers_list_too_small",
			"serving new customers only needs at least 1,000 people on the paying customers list, which has "+itoa(members)+"; use bid_higher until it grows")
		return
	}
	o, m, ok := s.adFeedOpen(w, r, ws, acct)
	if !ok {
		return
	}
	changedNCA := in.NCA != nil || in.AdditionalValueMicros != nil
	if changedNCA {
		var campaigns []string
		rows, err := s.pool.Query(r.Context(), `SELECT external_id FROM ad_entities WHERE workspace_id = $1 AND ad_account_id = $2 AND level = 'campaign' AND status IN ('active', 'paused') ORDER BY external_id`, ws, acct.id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				s.internal(w, r, err)
				return
			}
			campaigns = append(campaigns, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			s.internal(w, r, err)
			return
		}
		if err := m.SetNewCustomerGoal(r.Context(), o.Client, o.Creds, acct.acct, connectors.NewCustomerGoal{Mode: mode, AdditionalValueMicros: value, CampaignIDs: campaigns}); err != nil {
			s.connectorFailed(w, r, err)
			return
		}
		feed.NCA = &adconv.NCASetting{Mode: mode, AdditionalValueMicros: value, SetAt: time.Now().UTC()}
	}
	if in.Transport != nil {
		feed.Transport, feed.TransportReason, feed.TransportSetAt = "", "", nil
	}
	var out adFeedView
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := adconv.SaveFeed(r.Context(), tx, ws, acct.id, feed); err != nil {
			return err
		}
		if out, err = s.adFeedView(r.Context(), tx, ws, acct, feed, o); err != nil {
			return err
		}
		props := map[string]any{"connector": acct.connector, "nca": mode, "additional_value_micros": value, "transport_reset": in.Transport != nil, "nca_changed": changedNCA}
		return recordWithProps(r.Context(), tx, &a.principal, ws, "ad_feed.updated", "ad_account", acct.id, props, props)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// getAdFeedHealth is the reconciliation: what the platform counts for the
// feed's actions against what Outflare sent, per day and campaign.
func (s *Server) getAdFeedHealth(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := strings.TrimSpace(r.URL.Query().Get("account_id"))
	if !isUUID(id) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is an ad account's id")
		return
	}
	acct, ok := s.adFeedAccount(w, r, a, id)
	if !ok {
		return
	}
	h, err := adconv.FeedHealth(r.Context(), s.pool, a.WorkspaceID, acct.id, time.Now())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}
