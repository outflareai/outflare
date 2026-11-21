package emailbison

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// CampaignReady reads whether a campaign can take new people, before any
// are added: the campaign (GET /api/campaigns/{id}: its status and its own
// daily limits), its sequence (GET /api/campaigns/v1.1/{id}/sequence-steps),
// its schedule (GET /api/campaigns/{id}/schedule) and its sender emails
// (GET /api/campaigns/{id}/sender-emails, every page). It only reads.
//
// A campaign is not ready when it is not active, its sequence has no
// steps, a step has no body (or no subject, unless it replies in the same
// thread), a step uses a variable people added from Outflare don't have,
// its schedule has no sending day or no valid sending window or time
// zone, or it has no connected sender with a daily limit. Its daily
// capacity is its connected senders' daily limits, within its own limits
// for new leads and for emails a day.
func (EmailBison) CampaignReady(ctx context.Context, cl *c.Client, creds c.Creds, campaignID string) (c.Readiness, error) {
	a, err := open(cl, creds)
	if err != nil {
		return c.Readiness{}, err
	}
	base := "/api/campaigns/" + url.PathEscape(campaignID)
	var camp struct {
		Data struct {
			Status      string `json:"status"`
			MaxNewLeads *int   `json:"max_new_leads_per_day"`
			MaxEmails   *int   `json:"max_emails_per_day"`
		} `json:"data"`
	}
	if err := a.do(ctx, http.MethodGet, base, nil, &camp); err != nil {
		return c.Readiness{}, err
	}
	var seq struct {
		Data struct {
			Steps []map[string]any `json:"sequence_steps"`
		} `json:"data"`
	}
	if err := a.do(ctx, http.MethodGet, "/api/campaigns/v1.1/"+url.PathEscape(campaignID)+"/sequence-steps", nil, &seq); err != nil {
		return c.Readiness{}, err
	}
	var sched struct {
		Data map[string]any `json:"data"`
	}
	if err := a.do(ctx, http.MethodGet, base+"/schedule", nil, &sched); err != nil {
		return c.Readiness{}, err
	}
	senders, err := all[struct {
		ID         id              `json:"id"`
		Status     string          `json:"status"`
		DailyLimit json.RawMessage `json:"daily_limit"`
	}](ctx, a, base+"/sender-emails")
	if err != nil {
		return c.Readiness{}, err
	}
	var r c.Readiness
	capacity := 0
	for _, s := range senders {
		var n int
		if !strings.EqualFold(strings.TrimSpace(s.Status), "connected") || json.Unmarshal(s.DailyLimit, &n) != nil || n <= 0 {
			continue
		}
		r.Senders++
		capacity += n
	}
	if status := strings.TrimSpace(camp.Data.Status); !strings.EqualFold(status, "active") {
		if status == "" {
			status = "not active"
		}
		r.Reasons = append(r.Reasons, fmt.Sprintf("The campaign is %s; new people wait until it is active.", strings.ToLower(status)))
	}
	if camp.Data.MaxNewLeads != nil {
		capacity = min(capacity, max(0, *camp.Data.MaxNewLeads))
	}
	if camp.Data.MaxEmails != nil {
		capacity = min(capacity, max(0, *camp.Data.MaxEmails))
	}
	if r.Senders == 0 || capacity == 0 {
		r.Reasons = append(r.Reasons, "The campaign has no connected sender with a daily limit; new people wait until it has one.")
	}
	r.Reasons = append(r.Reasons, sequenceIssues(seq.Data.Steps)...)
	r.Reasons = append(r.Reasons, scheduleIssues(sched.Data)...)
	if r.Ready() {
		r.DailyCapacity = capacity
	}
	return r, nil
}

// leadTags are the merge tags EmailBison fills from a contact Outflare
// creates: the fields a push sends. Any other {TAG} in the sequence would
// go out as the bare word.
var leadTags = map[string]bool{"FIRST_NAME": true, "LAST_NAME": true, "EMAIL": true, "COMPANY": true}

var (
	mergeTag = regexp.MustCompile(`\{([A-Z][A-Z0-9_]*)\}`)
	htmlTag  = regexp.MustCompile(`<[^>]+>`)
	// badTag is a merge tag EmailBison can't fill: double braces, or not
	// in capitals.
	badTag = regexp.MustCompile(`\{\{[^{}]*\}\}|\{[a-z][A-Za-z0-9_]*\}`)
)

func sequenceIssues(steps []map[string]any) []string {
	if len(steps) == 0 {
		return []string{"The campaign's sequence has no email steps."}
	}
	text := func(v any) string { s, _ := v.(string); return s }
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, st := range steps {
		subject, body := text(st["email_subject"]), text(st["email_body"])
		reply, _ := st["thread_reply"].(bool)
		if strings.TrimSpace(htmlTag.ReplaceAllString(body, "")) == "" || (!reply && strings.TrimSpace(subject) == "") {
			add("The campaign's sequence has an empty subject or body.")
		}
		for _, t := range []string{subject, body} {
			if badTag.MatchString(t) {
				add("The campaign's sequence has merge tags EmailBison can't fill.")
			}
			for _, m := range mergeTag.FindAllStringSubmatch(t, -1) {
				if !leadTags[m[1]] {
					add(fmt.Sprintf("The campaign's sequence uses {%s}, which people added from Outflare don't have.", m[1]))
				}
			}
		}
	}
	return out
}

var clock = regexp.MustCompile(`^(?:[01]\d|2[0-3]):[0-5]\d(?::[0-5]\d)?$`)

func scheduleIssues(s map[string]any) []string {
	days := []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
	sending := false
	for _, d := range days {
		on, ok := s[d].(bool)
		if !ok {
			return []string{"The campaign needs a complete schedule with at least one sending day."}
		}
		sending = sending || on
	}
	if !sending {
		return []string{"The campaign needs a complete schedule with at least one sending day."}
	}
	start, _ := s["start_time"].(string)
	end, _ := s["end_time"].(string)
	if !clock.MatchString(start) || !clock.MatchString(end) || start[:5] >= end[:5] {
		return []string{"The campaign needs a valid daytime sending window."}
	}
	tz, _ := s["timezone"].(string)
	if _, err := time.LoadLocation(tz); err != nil || strings.TrimSpace(tz) == "" {
		return []string{"The campaign's time zone is not valid."}
	}
	return nil
}
