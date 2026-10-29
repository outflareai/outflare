package api

import (
	"net/http"

	"github.com/outflareai/outflare/engine/internal/auth"
)

func (s *Server) routesStats(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/stats", s.inBrand(auth.ScopeWorkspaceRead, s.stats))
}

type statsView struct {
	Mailboxes struct {
		Total         int `json:"total"`
		Active        int `json:"active"`
		Error         int `json:"error"`
		Paused        int `json:"paused"`
		Verifying     int `json:"verifying"`
		DailyCapacity int `json:"daily_capacity"`
		// Warming counts mailboxes in the warm-up stage, as Mailboxes and
		// Infrastructure do.
		Warming int `json:"warming"`
	} `json:"mailboxes"`
	Domains struct {
		Total         int `json:"total"`
		Paused        int `json:"paused"`
		NeedAttention int `json:"need_attention"`
	} `json:"domains"`
	// WarmupInboxRate is the share of the last 7 days' placed warm-up
	// mail that reached the inbox, null when there is none.
	WarmupInboxRate *float64 `json:"warmup_inbox_rate"`
	Sent24h         int      `json:"sent_24h"`
	Sent7d          int      `json:"sent_7d"`
	Replies7d       int      `json:"replies_7d"`
	// ContactedLeads7d and RepliedLeads7d give the reply rate the same
	// way everywhere: leads who replied over leads emailed.
	ContactedLeads7d  int `json:"contacted_leads_7d"`
	RepliedLeads7d    int `json:"replied_leads_7d"`
	Bounces7d         int `json:"bounces_7d"`
	UnreadReplies     int `json:"unread_replies"`
	ActiveSequences   int `json:"active_sequences"`
	ActiveEnrollments int `json:"active_enrollments"`
	Leads             int `json:"leads"`
	// LinkPitchesWaiting are draft link pitches: waiting for a person to
	// approve them, or to write the opener when there is no AI.
	LinkPitchesWaiting int `json:"link_pitches_waiting"`
}

// stats is the workspace overview: one round trip for the home screen and
// for agents checking how outreach is going.
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	var v statsView
	m := &v.Mailboxes
	// With a brand, everything but leads (which every brand shares) counts
	// that brand's mailboxes and sequences; mail goes by its mailbox.
	args := []any{a.WorkspaceID}
	mb := func(alias string) string { return brand.sql(alias+".brand_id", &args) }
	viaMailbox := func(alias string) string {
		if brand.all() {
			return ""
		}
		return ` AND ` + alias + `.mailbox_id IN (SELECT b.id FROM mailboxes b WHERE b.workspace_id = $1` + mb("b") + `)`
	}
	viaSequence := func(alias string) string {
		if brand.all() {
			return ""
		}
		return ` AND ` + alias + `.sequence_id IN (SELECT q.id FROM sequences q WHERE q.workspace_id = $1` + mb("q") + `)`
	}
	err := s.pool.QueryRow(r.Context(), `
		SELECT
			(SELECT count(*) FROM mailboxes x WHERE workspace_id = $1`+mb("x")+`),
			(SELECT count(*) FROM mailboxes x WHERE workspace_id = $1 AND status = 'active'`+mb("x")+`),
			(SELECT count(*) FROM mailboxes x WHERE workspace_id = $1 AND status = 'error'`+mb("x")+`),
			(SELECT count(*) FROM mailboxes x WHERE workspace_id = $1 AND status = 'paused'`+mb("x")+`),
			(SELECT count(*) FROM mailboxes x WHERE workspace_id = $1 AND status = 'verifying'`+mb("x")+`),
			(SELECT COALESCE(sum(daily_limit), 0) FROM mailboxes x WHERE workspace_id = $1 AND status = 'active'`+mb("x")+`),
			(SELECT count(*) FROM messages g WHERE workspace_id = $1 AND status = 'sent' AND created_at > now() - interval '24 hours'`+viaMailbox("g")+`),
			(SELECT count(*) FROM messages g WHERE workspace_id = $1 AND status = 'sent' AND created_at > now() - interval '7 days'`+viaMailbox("g")+`),
			(SELECT count(*) FROM inbound_messages i WHERE workspace_id = $1 AND kind = 'reply' AND received_at > now() - interval '7 days'`+viaMailbox("i")+`),
			(SELECT count(*) FROM inbound_messages i WHERE workspace_id = $1 AND kind = 'bounce' AND received_at > now() - interval '7 days'`+viaMailbox("i")+`),
			(SELECT count(*) FROM inbound_messages i WHERE workspace_id = $1 AND kind = 'reply' AND NOT read AND NOT done`+viaMailbox("i")+`),
			(SELECT count(*) FROM sequences x WHERE workspace_id = $1 AND status = 'active'`+mb("x")+`),
			(SELECT count(*) FROM enrollments e WHERE workspace_id = $1 AND status = 'active'`+viaSequence("e")+`),
			(SELECT count(*) FROM leads WHERE workspace_id = $1),
			(SELECT count(*) FROM mailboxes x WHERE workspace_id = $1 AND lifecycle_stage = 'warming' AND status IN ('active', 'verifying')`+mb("x")+`),
			(SELECT count(DISTINCT lead_id) FROM messages g WHERE workspace_id = $1 AND status = 'sent' AND lead_id IS NOT NULL AND created_at > now() - interval '7 days'`+viaMailbox("g")+`),
			(SELECT count(DISTINCT lead_id) FROM inbound_messages i WHERE workspace_id = $1 AND kind = 'reply' AND lead_id IS NOT NULL AND received_at > now() - interval '7 days'`+viaMailbox("i")+`),
			(SELECT count(*) FILTER (WHERE landed = 'inbox')::float / NULLIF(count(*), 0) FROM warmup_messages wm
				WHERE workspace_id = $1 AND landed IS NOT NULL AND created_at > now() - interval '7 days'`+viaWarmup(brand, mb)+`),
			(SELECT count(*) FROM link_pitches k WHERE workspace_id = $1 AND status = 'draft'`+viaSequence("k")+`)`, args...,
	).Scan(&m.Total, &m.Active, &m.Error, &m.Paused, &m.Verifying, &m.DailyCapacity,
		&v.Sent24h, &v.Sent7d, &v.Replies7d, &v.Bounces7d, &v.UnreadReplies, &v.ActiveSequences, &v.ActiveEnrollments, &v.Leads,
		&m.Warming, &v.ContactedLeads7d, &v.RepliedLeads7d, &v.WarmupInboxRate, &v.LinkPitchesWaiting)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	domains, err := s.queryDomains(r.Context(), a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Capacity is what can go out today: paused domains send nothing and
	// a domain cap can hold its mailboxes below their own limits.
	m.DailyCapacity = 0
	for _, d := range domains {
		if !brand.matches(d.BrandID) {
			continue
		}
		m.DailyCapacity += d.DailyCapacity
		v.Domains.Total++
		if d.Status == "paused" {
			v.Domains.Paused++
		}
		if d.Health == "warning" || d.Health == "critical" {
			v.Domains.NeedAttention++
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// viaWarmup keeps warm-up mail to the brand's mailboxes, by sender.
func viaWarmup(brand brandSel, mb func(string) string) string {
	if brand.all() {
		return ""
	}
	return ` AND wm.from_mailbox_id IN (SELECT b.id FROM mailboxes b WHERE b.workspace_id = $1` + mb("b") + `)`
}
