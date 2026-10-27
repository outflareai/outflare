// Package health says how each sending mailbox is doing, from every signal
// the engine has: sign-in and sync errors (and what they mean, such as a
// suspended account), send failures, bounce rate, warm-up inbox placement,
// the domain's DNS and blocklists, and whether a new mailbox is warming up.
// It is computed on read, so it is never stale, and a Monitor pauses a
// mailbox whose bounce rate spikes, saying why.
package health

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Levels, worst last.
const (
	OK       = "ok"
	Warning  = "warning"
	Critical = "critical"
	// Paused mailboxes are reported but not graded: nobody is sending from them.
	Paused = "paused"
)

// Signal is one thing that is right or wrong with a mailbox, with what to do.
type Signal struct {
	Key    string   `json:"key"`
	Level  string   `json:"level"`
	Title  string   `json:"title"`
	Detail string   `json:"detail"`
	Fix    string   `json:"fix,omitempty"`
	Value  *float64 `json:"value,omitempty"`
}

// Mailbox is a mailbox's health.
type Mailbox struct {
	MailboxID string `json:"mailbox_id"`
	Email     string `json:"email"`
	Domain    string `json:"domain"`
	Provider  string `json:"provider"`
	Status    string `json:"status"`
	// ok, warning, critical, or paused.
	Level   string   `json:"level"`
	Score   int      `json:"score"` // 100 is healthy
	Signals []Signal `json:"signals"`
	Stats   Stats    `json:"stats"`
}

// Stats are the numbers behind the signals.
type Stats struct {
	Sent24h      int      `json:"sent_24h"`
	Bounced24h   int      `json:"bounced_24h"`
	Sent7d       int      `json:"sent_7d"`
	Bounced7d    int      `json:"bounced_7d"`
	BounceRate7d *float64 `json:"bounce_rate_7d"` // percent; null under 20 sends
	Failed24h    int      `json:"failed_24h"`
	Replies7d    int      `json:"replies_7d"`
	// Warm-up emails from this mailbox that landed, last 7 days.
	WarmupLanded int      `json:"warmup_landed_7d"`
	WarmupSpam   int      `json:"warmup_spam_7d"`
	WarmupLost   int      `json:"warmup_missing_7d"`
	Placement    *float64 `json:"inbox_placement_7d"` // percent in the inbox; null under 10 landed
}

// Summary counts mailboxes by level.
type Summary struct {
	Total    int `json:"total"`
	OK       int `json:"ok"`
	Warning  int `json:"warning"`
	Critical int `json:"critical"`
	Paused   int `json:"paused"`
}

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type row struct {
	Mailbox
	statusReason, syncError            *string
	lastSynced, created, warmupStarted *time.Time
	warmup                             bool
	lastFailure                        *string
	spf, dkim, dmarc, domainStatus     *string
	blocklists                         []byte
}

// Check returns the health of the workspace's mailboxes, or just ids when given.
func Check(ctx context.Context, q Querier, ws string, ids []string) ([]Mailbox, error) {
	rows, err := q.Query(ctx, `
		SELECT m.id, m.email, m.domain, m.provider, m.status, m.status_reason, m.sync_error, m.last_synced_at, m.created_at,
		       m.warmup_enabled OR EXISTS (SELECT 1 FROM mailbox_platforms mp WHERE mp.mailbox_id = m.id AND mp.warmup_enabled), m.warmup_started_at,
		       s.sent24, s.sent7, s.failed24, s.last_failure,
		       b.b24, b.b7, r.replies7,
		       w.landed, w.spam, w.lost,
		       d.spf, d.dkim, d.dmarc, d.status, d.blocklists
		FROM mailboxes m
		LEFT JOIN LATERAL (
			SELECT count(*) FILTER (WHERE status = 'sent' AND created_at > now() - interval '24 hours')::int AS sent24,
			       count(*) FILTER (WHERE status = 'sent')::int AS sent7,
			       count(*) FILTER (WHERE status = 'failed' AND created_at > now() - interval '24 hours')::int AS failed24,
			       (array_agg(error ORDER BY created_at DESC) FILTER (WHERE status = 'failed' AND created_at > now() - interval '24 hours'))[1] AS last_failure
			FROM messages WHERE mailbox_id = m.id AND created_at > now() - interval '7 days') s ON true
		LEFT JOIN LATERAL (
			SELECT count(*) FILTER (WHERE updated_at > now() - interval '24 hours')::int AS b24, count(*)::int AS b7
			FROM enrollments WHERE mailbox_id = m.id AND status = 'bounced' AND updated_at > now() - interval '7 days') b ON true
		LEFT JOIN LATERAL (
			SELECT count(*)::int AS replies7 FROM inbound_messages
			WHERE mailbox_id = m.id AND kind = 'reply' AND received_at > now() - interval '7 days') r ON true
		LEFT JOIN LATERAL (
			SELECT count(*) FILTER (WHERE landed = 'inbox')::int AS landed, count(*) FILTER (WHERE landed = 'spam')::int AS spam,
			       count(*) FILTER (WHERE landed = 'missing')::int AS lost
			FROM warmup_messages WHERE from_mailbox_id = m.id AND created_at > now() - interval '7 days') w ON true
		LEFT JOIN sending_domains d ON d.workspace_id = m.workspace_id AND d.domain = m.domain
		WHERE m.workspace_id = $1 AND (cardinality($2::uuid[]) = 0 OR m.id = ANY($2))
		ORDER BY m.email`, ws, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mailbox{}
	for rows.Next() {
		var r row
		var sent24, sent7, failed24, b24, b7, replies, landed, spam, lost *int
		if err := rows.Scan(&r.MailboxID, &r.Email, &r.Domain, &r.Provider, &r.Status, &r.statusReason, &r.syncError, &r.lastSynced, &r.created,
			&r.warmup, &r.warmupStarted, &sent24, &sent7, &failed24, &r.lastFailure, &b24, &b7, &replies, &landed, &spam, &lost,
			&r.spf, &r.dkim, &r.dmarc, &r.domainStatus, &r.blocklists); err != nil {
			return nil, err
		}
		n := func(p *int) int {
			if p == nil {
				return 0
			}
			return *p
		}
		r.Stats = Stats{Sent24h: n(sent24), Sent7d: n(sent7), Failed24h: n(failed24), Bounced24h: n(b24), Bounced7d: n(b7), Replies7d: n(replies),
			WarmupLanded: n(landed), WarmupSpam: n(spam), WarmupLost: n(lost)}
		out = append(out, grade(r, time.Now()))
	}
	return out, rows.Err()
}

// Summarise counts mailboxes by level.
func Summarise(list []Mailbox) Summary {
	s := Summary{Total: len(list)}
	for _, m := range list {
		switch m.Level {
		case OK:
			s.OK++
		case Warning:
			s.Warning++
		case Critical:
			s.Critical++
		case Paused:
			s.Paused++
		}
	}
	return s
}

func pct(a, b int) float64 { return float64(int(float64(a)*1000/float64(b)+0.5)) / 10 }

func grade(r row, now time.Time) Mailbox {
	m := r.Mailbox
	m.Signals = []Signal{}
	add := func(s Signal) { m.Signals = append(m.Signals, s) }
	st := &m.Stats

	// Signing in.
	if r.Status == "error" {
		reason := deref(r.statusReason)
		kind, fix := Explain(reason, r.Provider)
		add(Signal{Key: "sign_in", Level: Critical, Title: kind, Detail: reason, Fix: fix})
	}
	// Reading replies.
	if r.syncError != nil && *r.syncError != "" && r.Status != "error" {
		kind, fix := Explain(*r.syncError, r.Provider)
		add(Signal{Key: "inbox_sync", Level: Warning, Title: "Replies can't be read: " + strings.ToLower(kind), Detail: *r.syncError, Fix: fix})
	} else if r.Status == "active" && r.lastSynced != nil && now.Sub(*r.lastSynced) > time.Hour {
		add(Signal{Key: "inbox_sync", Level: Warning, Title: "Replies last checked " + since(now, *r.lastSynced), Detail: "The inbox is checked every two minutes; it hasn't been for over an hour.", Fix: "Check the engine is running and the mailbox's IMAP access."})
	}
	// Sending.
	if st.Failed24h >= 3 {
		kind, fix := Explain(deref(r.lastFailure), r.Provider)
		lvl := Warning
		if st.Failed24h >= 10 {
			lvl = Critical
		}
		add(Signal{Key: "send_failures", Level: lvl, Title: fmt.Sprintf("%d sends failed in 24 hours: %s", st.Failed24h, strings.ToLower(kind)), Detail: deref(r.lastFailure), Fix: fix})
	}
	// Bounces.
	if st.Sent7d >= 20 {
		rate := pct(st.Bounced7d, st.Sent7d)
		st.BounceRate7d = &rate
		switch {
		case rate >= 5:
			add(Signal{Key: "bounce_rate", Level: Critical, Value: &rate, Title: fmt.Sprintf("%.1f%% of emails bounced this week", rate),
				Detail: fmt.Sprintf("%d of %d. Providers start sending to spam above 2–3%%.", st.Bounced7d, st.Sent7d), Fix: "Verify the leads before sending (Connectors → Email verification) and pause this mailbox until the list is clean."})
		case rate >= 3:
			add(Signal{Key: "bounce_rate", Level: Warning, Value: &rate, Title: fmt.Sprintf("%.1f%% of emails bounced this week", rate),
				Detail: fmt.Sprintf("%d of %d.", st.Bounced7d, st.Sent7d), Fix: "Verify the leads before sending and check the list's source."})
		}
	}
	// Warm-up placement.
	total := st.WarmupLanded + st.WarmupSpam + st.WarmupLost
	if total >= 10 {
		p := pct(st.WarmupLanded, total)
		st.Placement = &p
		detail := fmt.Sprintf("Of %d warm-up emails: %d inbox, %d spam, %d never arrived.", total, st.WarmupLanded, st.WarmupSpam, st.WarmupLost)
		switch {
		case p < 60:
			add(Signal{Key: "placement", Level: Critical, Value: &p, Title: fmt.Sprintf("Only %.0f%% reach the inbox", p), Detail: detail, Fix: "Pause cold sending from this mailbox, keep warm-up on, and check the domain's DNS and blocklists."})
		case p < 80:
			add(Signal{Key: "placement", Level: Warning, Value: &p, Title: fmt.Sprintf("%.0f%% reach the inbox", p), Detail: detail, Fix: "Lower the daily limit and keep warm-up on until placement is above 80%."})
		}
	}
	// New and cold.
	if !r.warmup && r.created != nil && now.Sub(*r.created) < 14*24*time.Hour && r.Status == "active" {
		add(Signal{Key: "warmup", Level: Warning, Title: "New mailbox not warming up", Detail: "Added " + since(now, *r.created) + ". New mailboxes that send cold email straight away are flagged as spam.", Fix: "Warm it up for two weeks in a connected sequencer (EmailBison, Instantly or Smartlead) before sending sequences from it."})
	}
	// The domain.
	switch {
	case r.spf == nil:
	case *r.spf == "missing" || *r.spf == "invalid":
		add(Signal{Key: "domain_spf", Level: Critical, Title: "Domain has no valid SPF record", Detail: r.Domain + " doesn't say which servers may send its mail.", Fix: "Open the domain under Domains for the record to add."})
	}
	if r.dkim != nil && *r.dkim == "missing" {
		add(Signal{Key: "domain_dkim", Level: Critical, Title: "Domain isn't signing mail (no DKIM)", Detail: r.Domain + " has no DKIM key, so receivers can't verify its mail.", Fix: "Turn on DKIM in Google Workspace or Microsoft 365 and publish the key; see Domains."})
	}
	if r.dmarc != nil && *r.dmarc == "missing" {
		add(Signal{Key: "domain_dmarc", Level: Warning, Title: "Domain has no DMARC record", Detail: "Gmail and Yahoo require DMARC from bulk senders.", Fix: "Add a DMARC record; see Domains."})
	}
	if listed := blocklisted(r.blocklists); len(listed) > 0 {
		add(Signal{Key: "blocklist", Level: Critical, Title: "Domain is on " + strings.Join(listed, ", "), Detail: "Mail from listed domains is often rejected or sent to spam.", Fix: "Pause the domain, find the cause (usually bounces or complaints) and request delisting."})
	}
	if r.domainStatus != nil && *r.domainStatus == "paused" {
		add(Signal{Key: "domain_paused", Level: Warning, Title: "Its domain is paused", Detail: "No sequence email goes from any mailbox on " + r.Domain + ".", Fix: "Resume the domain under Domains when it's fixed."})
	}

	m.Score, m.Level = 100, OK
	for _, s := range m.Signals {
		switch s.Level {
		case Critical:
			m.Score -= 40
			m.Level = Critical
		case Warning:
			m.Score -= 15
			if m.Level == OK {
				m.Level = Warning
			}
		}
	}
	m.Score = max(m.Score, 0)
	if r.Status == "paused" {
		m.Level = Paused
		if r.statusReason != nil && *r.statusReason != "" {
			m.Signals = append([]Signal{{Key: "paused", Level: Paused, Title: "Paused", Detail: *r.statusReason}}, m.Signals...)
		}
	}
	sort.SliceStable(m.Signals, func(i, j int) bool { return rank(m.Signals[i].Level) > rank(m.Signals[j].Level) })
	return m
}

func rank(l string) int {
	switch l {
	case Paused:
		return 3
	case Critical:
		return 2
	case Warning:
		return 1
	}
	return 0
}

var problems = []struct {
	re         *regexp.Regexp
	title, fix string
}{
	{regexp.MustCompile(`(?i)smtp ?auth.*(disabled|not enabled)|SmtpClientAuthentication is disabled|IMAP (access )?(is )?disabled|enable IMAP`),
		"SMTP or IMAP access is turned off", "Turn on authenticated SMTP and IMAP for this account in the provider's admin console."},
	{regexp.MustCompile(`(?i)invalid_grant|token (has been )?(expired|revoked)|consent|unauthorized_client|AADSTS`),
		"Sign-in permission expired or revoked", "Reconnect the Google or Microsoft sign-in under Mailboxes."},
	{regexp.MustCompile(`(?i)suspend|disabled|account (is )?(locked|inactive)|user ?not ?found|mailbox unavailable|5\.7\.0 .*(disabled|suspended)`),
		"Account suspended or disabled", "Sign in to the provider's admin console to see why. Google suspends accounts that bounce or send too fast; Microsoft locks ones flagged as compromised. Remove it from sequences until it's restored."},
	{regexp.MustCompile(`(?i)(daily|sending) (user )?(sending )?(quota|limit)|5\.4\.5|too many (messages|recipients)|rate limit|throttl`),
		"Provider's sending limit reached", "Lower this mailbox's daily limit. Google Workspace allows about 2,000 a day but new accounts far fewer."},
	{regexp.MustCompile(`(?i)(invalid|bad|wrong|incorrect).*(credential|password|login)|authentication (failed|unsuccessful)|535|username and password not accepted|AUTHENTICATIONFAILED|LOGIN failed`),
		"Password or app password rejected", "Create a new app password (or sign in again for OAuth) and update the mailbox."},
	{regexp.MustCompile(`(?i)blocked|spam|5\.7\.1|policy|reputation|blacklist|blocklist`),
		"Mail is being blocked", "Check the domain's blocklists and DNS under Domains, and pause this mailbox until it's clear."},
	{regexp.MustCompile(`(?i)timeout|timed out|connection refused|no such host|i/o timeout|EOF|reset by peer`),
		"Can't reach the mail server", "Usually temporary. If it lasts, check the SMTP and IMAP host and port."},
}

// Explain turns a provider's error into what it means and what to do.
func Explain(errText, provider string) (title, fix string) {
	for _, p := range problems {
		if p.re.MatchString(errText) {
			return p.title, p.fix
		}
	}
	if errText == "" {
		return "Can't sign in", "Check the mailbox's credentials and test it again."
	}
	return "Mail server error", "Test the mailbox again; if it keeps failing, check its settings at the provider."
}

func blocklisted(b []byte) []string {
	var lists []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	_ = jsonUnmarshal(b, &lists)
	var out []string
	for _, l := range lists {
		if l.Status == "listed" {
			out = append(out, l.Name)
		}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func since(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
