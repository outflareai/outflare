package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/adtrack"
	"github.com/outflareai/outflare/engine/internal/attribution"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Tracking health is the workspace's own view of whether its paid traffic
// is tracked: which networks' clicks land with their ids, which synced
// campaigns spend without a tracked visit (no tracking template), and
// whether conversions sent back are taken. Each problem comes with what to
// do about it. The operator's alerts watch the same signals across every
// workspace (OutflarePaidArrivalsWithoutIds); this is per workspace.

type trackingHealthNetwork struct {
	Network string `json:"network"`
	Label   string `json:"label"`
	// Paid visits from the network, and how each was tracked: its click id,
	// only the ad ids a tracking template adds, or neither.
	Visits    int `json:"visits"`
	ClickID   int `json:"click_id"`
	AdIDsOnly int `json:"ad_ids_only"`
	Untracked int `json:"untracked"`
}

type trackingHealthCampaign struct {
	AdEntityID string  `json:"ad_entity_id"`
	Connector  string  `json:"connector"`
	Name       string  `json:"name"`
	ExternalID string  `json:"external_id"`
	Spend      []money `json:"spend"`
}

type trackingHealthSends struct {
	Connector string `json:"connector"`
	Sent      int    `json:"sent"`
	Skipped   int    `json:"skipped"`
	Failed    int    `json:"failed"`
	// TopFailure is the most common reason a send failed, '' with none.
	TopFailure string `json:"top_failure"`
}

type trackingHealthIssue struct {
	// Code is untracked_clicks, campaigns_without_visits or sends_failing.
	Code     string `json:"code"`
	Severity string `json:"severity"`
	// Subject is the network or connector it is about.
	Subject string `json:"subject"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

const (
	// healthMinVisits is how many paid visits a network needs before its
	// share of untracked ones is called a problem.
	healthMinVisits = 20
	// healthUntrackedShare is the share of untracked paid visits that is.
	healthUntrackedShare = 0.2
	// healthFailedShare is the share of failed sends that is.
	healthFailedShare = 0.1
)

func (s *Server) adTrackingHealth(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 90 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 90")
			return
		}
		days = n
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	since := time.Now().UTC().AddDate(0, 0, -days)

	// Paid visits, by source and medium and the first click id they had.
	// Cookies kept beside click ids (fbp, ttp) don't say how a visit arrived.
	params := adtrack.Params()
	args := []any{a.WorkspaceID, since, params}
	rows, err := s.pool.Query(ctx, `SELECT lower(utm_medium), lower(utm_source),
			COALESCE((SELECT k FROM jsonb_object_keys(click_ids) k WHERE k = ANY($3::text[]) ORDER BY k LIMIT 1), ''),
			ad <> '{}'::jsonb, count(*)::int
		FROM site_sessions
		WHERE workspace_id = $1 AND started_at >= $2 AND utm_medium <> ''`+brand.sql(`brand_id`, &args)+`
		GROUP BY 1, 2, 3, 4`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	nets := map[string]*trackingHealthNetwork{}
	for rows.Next() {
		var medium, source, param string
		var hasAd bool
		var n int
		if err := rows.Scan(&medium, &source, &param, &hasAd, &n); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		if !attribution.PaidMedium(medium) {
			continue
		}
		ids := map[string]any{}
		if param != "" {
			ids[param] = "x"
		}
		ad := 0
		if hasAd {
			ad = 1
		}
		network, outcome := adtrack.Arrival(ids, ad, utm.Source(source))
		x := nets[network]
		if x == nil {
			label := "Other paid sources"
			if n, ok := adtrack.NetworkOf(network); ok {
				label = n.Label
			}
			x = &trackingHealthNetwork{Network: network, Label: label}
			nets[network] = x
		}
		x.Visits += n
		switch outcome {
		case adtrack.ArrivalClickID:
			x.ClickID += n
		case adtrack.ArrivalAdIDs:
			x.AdIDsOnly += n
		default:
			x.Untracked += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}

	// Synced campaigns that spent in the range and that no visit names.
	cargs := []any{a.WorkspaceID, since.Format(time.DateOnly), since}
	crow, err := s.pool.Query(ctx, `SELECT e.id::text, a.connector, e.name, e.external_id, a.currency, sum(m.spend_micros)::float8
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		JOIN ad_metrics_daily m ON m.ad_entity_id = e.id AND m.day >= $2::date
		WHERE e.workspace_id = $1 AND e.level = 'campaign'`+brand.sql(`COALESCE(e.brand_id, a.brand_id)`, &cargs)+`
		  AND NOT EXISTS (SELECT 1 FROM site_sessions s WHERE s.workspace_id = $1 AND s.started_at >= $3 AND s.ad->>'campaign' = e.external_id)
		GROUP BY e.id, a.id
		HAVING sum(m.spend_micros) > 0
		ORDER BY sum(m.spend_micros) DESC LIMIT 50`, cargs...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	campaigns := []trackingHealthCampaign{}
	for crow.Next() {
		var c trackingHealthCampaign
		var currency string
		var spend float64
		if err := crow.Scan(&c.AdEntityID, &c.Connector, &c.Name, &c.ExternalID, &currency, &spend); err != nil {
			crow.Close()
			s.internal(w, r, err)
			return
		}
		// Micros to cents.
		c.Spend = moneyList(map[string]float64{currency: spend / 10_000})
		campaigns = append(campaigns, c)
	}
	crow.Close()
	if err := crow.Err(); err != nil {
		s.internal(w, r, err)
		return
	}

	// Conversions sent back, per platform, with the commonest failure.
	sargs := []any{a.WorkspaceID, since}
	srow, err := s.pool.Query(ctx, `SELECT a.connector, q.status, q.reason, count(*)::int
		FROM ad_conversion_sends q JOIN ad_conversion_rules r ON r.id = q.rule_id JOIN ad_accounts a ON a.id = r.ad_account_id
		WHERE q.workspace_id = $1 AND q.created_at >= $2 AND q.status IN ('sent', 'skipped', 'failed')`+brand.sql(`a.brand_id`, &sargs)+`
		GROUP BY 1, 2, 3`, sargs...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sends := map[string]*trackingHealthSends{}
	topCount := map[string]int{}
	for srow.Next() {
		var connector, status, reason string
		var n int
		if err := srow.Scan(&connector, &status, &reason, &n); err != nil {
			srow.Close()
			s.internal(w, r, err)
			return
		}
		x := sends[connector]
		if x == nil {
			x = &trackingHealthSends{Connector: connector}
			sends[connector] = x
		}
		switch status {
		case "sent":
			x.Sent += n
		case "skipped":
			x.Skipped += n
		case "failed":
			x.Failed += n
			if n > topCount[connector] {
				topCount[connector], x.TopFailure = n, reason
			}
		}
	}
	srow.Close()
	if err := srow.Err(); err != nil {
		s.internal(w, r, err)
		return
	}

	out := struct {
		Days      int                      `json:"days"`
		Networks  []*trackingHealthNetwork `json:"networks"`
		Campaigns []trackingHealthCampaign `json:"campaigns_without_visits"`
		Sends     []*trackingHealthSends   `json:"sends"`
		Issues    []trackingHealthIssue    `json:"issues"`
	}{Days: days, Networks: []*trackingHealthNetwork{}, Campaigns: campaigns, Sends: []*trackingHealthSends{}, Issues: []trackingHealthIssue{}}
	for _, x := range nets {
		out.Networks = append(out.Networks, x)
	}
	sort.Slice(out.Networks, func(i, j int) bool {
		if out.Networks[i].Visits != out.Networks[j].Visits {
			return out.Networks[i].Visits > out.Networks[j].Visits
		}
		return out.Networks[i].Network < out.Networks[j].Network
	})
	for _, x := range sends {
		out.Sends = append(out.Sends, x)
	}
	sort.Slice(out.Sends, func(i, j int) bool { return out.Sends[i].Connector < out.Sends[j].Connector })
	out.Issues = healthIssues(out.Networks, campaigns, out.Sends)
	writeJSON(w, http.StatusOK, out)
}

// healthIssues says what is wrong, worst first, and what to do about it.
func healthIssues(nets []*trackingHealthNetwork, campaigns []trackingHealthCampaign, sends []*trackingHealthSends) []trackingHealthIssue {
	out := []trackingHealthIssue{}
	for _, n := range nets {
		if n.Network == "other" || n.Visits < healthMinVisits || float64(n.Untracked) < healthUntrackedShare*float64(n.Visits) {
			continue
		}
		pct := strconv.Itoa(int(float64(n.Untracked)*100/float64(n.Visits)+0.5)) + "%"
		out = append(out, trackingHealthIssue{Code: "untracked_clicks", Severity: "warning", Subject: n.Network,
			Message: pct + " of paid visits from " + n.Label + " arrived without a click id or ad ids, so they can't be tied to an ad.",
			Fix:     "Paste the " + n.Label + " tracking template (Tracking templates, below) into its ads, and keep auto-tagging on where the network has it."})
	}
	byConnector := map[string]int{}
	for _, c := range campaigns {
		byConnector[c.Connector]++
	}
	connectors := make([]string, 0, len(byConnector))
	for c := range byConnector {
		connectors = append(connectors, c)
	}
	sort.Strings(connectors)
	for _, c := range connectors {
		label := c
		for _, n := range adtrack.Networks {
			if n.Connector == c {
				label = n.Label
			}
		}
		count := byConnector[c]
		msg := strconv.Itoa(count) + " " + label + " campaigns spent with no tracked visit, so their sales can't be credited to them."
		fix := "Add the " + label + " tracking template to those campaigns, and check that their landing pages run the website snippet."
		if count == 1 {
			msg = "One " + label + " campaign spent with no tracked visit, so its sales can't be credited to it."
			fix = "Add the " + label + " tracking template to that campaign, and check that its landing page runs the website snippet."
		}
		out = append(out, trackingHealthIssue{Code: "campaigns_without_visits", Severity: "warning", Subject: c, Message: msg, Fix: fix})
	}
	for _, x := range sends {
		total := x.Sent + x.Failed
		if total == 0 || float64(x.Failed) < healthFailedShare*float64(total) {
			continue
		}
		label := x.Connector
		for _, n := range adtrack.Networks {
			if n.Connector == x.Connector {
				label = n.Label
			}
		}
		msg := strconv.Itoa(x.Failed) + " of " + strconv.Itoa(total) + " conversions sent to " + label + " were refused."
		if x.TopFailure != "" {
			msg += " Most often: " + strings.TrimSuffix(x.TopFailure, ".") + "."
		}
		out = append(out, trackingHealthIssue{Code: "sends_failing", Severity: "critical", Subject: x.Connector, Message: msg,
			Fix: "Open the send log under Send conversions back and fix the rule or the connection it names; " + label + " learns nothing from refused conversions."})
	}
	rank := map[string]int{"critical": 0, "warning": 1}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Severity] < rank[out[j].Severity] })
	return out
}
