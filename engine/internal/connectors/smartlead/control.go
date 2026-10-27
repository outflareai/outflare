package smartlead

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Mailbox allocation: Smartlead adds email accounts to a campaign with
// POST /campaigns/{id}/email-accounts and removes them with DELETE on the
// same path, by email account id, and sets an account's daily cap with
// POST /email-accounts/{id}.

var _ c.Controller = Smartlead{}

func send(ctx context.Context, cl *c.Client, cr c.Creds, method, path string, body any) error {
	q := url.Values{"api_key": {cr["api_key"]}}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, cl.Base(ID, "https://server.smartlead.ai")+"/api/v1"+path+"?"+q.Encode(), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	_, err := c.DoJSON(ctx, cl, "Smartlead", req, nil)
	return err
}

func (Smartlead) Observe(ctx context.Context, cl *c.Client, cr c.Creds) (c.Snapshot, error) {
	var snap c.Snapshot
	var list []campaign
	if err := get(ctx, cl, cr, "/campaigns", nil, &list); err != nil {
		return snap, err
	}
	attached := map[string][]string{}
	for _, k := range list {
		id := strconv.Itoa(k.ID)
		st := controlStatus(k.Status)
		snap.Campaigns = append(snap.Campaigns, c.CampaignState{ExternalID: id, Name: k.Name, Status: st})
		if st != "active" && st != "paused" {
			continue
		}
		var seqs []struct {
			Delay struct {
				Days int `json:"delay_in_days"`
			} `json:"seq_delay_details"`
		}
		if err := get(ctx, cl, cr, "/campaigns/"+id+"/sequences", nil, &seqs); err != nil {
			return snap, err
		}
		days := 0
		for i, s := range seqs {
			if i > 0 { // the first step's delay is before the first email
				days += s.Delay.Days
			}
		}
		snap.Campaigns[len(snap.Campaigns)-1].SequenceDays = &days
		var accts []struct {
			ID flexInt `json:"id"`
		}
		if err := get(ctx, cl, cr, "/campaigns/"+id+"/email-accounts", nil, &accts); err != nil {
			return snap, err
		}
		for _, a := range accts {
			k := strconv.Itoa(int(a.ID))
			attached[k] = append(attached[k], id)
		}
	}
	for offset := 0; ; offset += 100 {
		var page []struct {
			ID            flexInt `json:"id"`
			FromEmail     string  `json:"from_email"`
			FromName      string  `json:"from_name"`
			MessagePerDay *int    `json:"message_per_day"`
			SMTPOK        bool    `json:"is_smtp_success"`
			IMAPOK        bool    `json:"is_imap_success"`
			Warmup        *struct {
				Status     string `json:"status"`
				Reputation string `json:"warmup_reputation"`
			} `json:"warmup_details"`
		}
		if err := get(ctx, cl, cr, "/email-accounts/", url.Values{"offset": {strconv.Itoa(offset)}, "limit": {"100"}}, &page); err != nil {
			return snap, err
		}
		for _, a := range page {
			id := strconv.Itoa(int(a.ID))
			m := c.MailboxState{ExternalID: id, Email: a.FromEmail, Name: a.FromName, DailyLimit: a.MessagePerDay,
				Status: c.MailboxOK, CampaignIDs: attached[id]}
			if !a.SMTPOK || !a.IMAPOK {
				m.Status, m.StatusReason = c.MailboxError, "Smartlead cannot sign in to it over SMTP or IMAP"
			}
			if a.Warmup != nil {
				m.WarmupEnabled = strings.EqualFold(a.Warmup.Status, "ACTIVE")
				if n, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(a.Warmup.Reputation), "%"), 64); err == nil {
					score := int(n + 0.5)
					m.WarmupScore = &score
				}
			}
			snap.Mailboxes = append(snap.Mailboxes, m)
		}
		if len(page) < 100 {
			return snap, nil
		}
		if err := ctx.Err(); err != nil {
			return snap, err
		}
	}
}

func controlStatus(s string) string {
	switch strings.ToUpper(s) {
	case "ACTIVE", "START", "STARTED":
		return "active"
	case "PAUSED", "STOPPED":
		return "paused"
	case "COMPLETED", "ARCHIVED":
		return "completed"
	}
	return "draft"
}

func accountIDs(ms []c.MailboxState) ([]int, error) {
	out := make([]int, 0, len(ms))
	for _, m := range ms {
		n, err := strconv.Atoi(m.ExternalID)
		if err != nil {
			return nil, c.Errf(c.ErrInvalid, "Smartlead email account id %q is not a number", m.ExternalID)
		}
		out = append(out, n)
	}
	return out, nil
}

func (Smartlead) Attach(ctx context.Context, cl *c.Client, cr c.Creds, campaignID string, ms []c.MailboxState) error {
	ids, err := accountIDs(ms)
	if err != nil {
		return err
	}
	return send(ctx, cl, cr, http.MethodPost, "/campaigns/"+url.PathEscape(campaignID)+"/email-accounts", map[string]any{"email_account_ids": ids})
}

func (Smartlead) Detach(ctx context.Context, cl *c.Client, cr c.Creds, campaignID string, ms []c.MailboxState) error {
	ids, err := accountIDs(ms)
	if err != nil {
		return err
	}
	// The reference spells this field email_accounts_ids on removal.
	return send(ctx, cl, cr, http.MethodDelete, "/campaigns/"+url.PathEscape(campaignID)+"/email-accounts", map[string]any{"email_accounts_ids": ids})
}

func (Smartlead) SetDailyLimit(ctx context.Context, cl *c.Client, cr c.Creds, m c.MailboxState, limit int) error {
	if limit < 1 {
		return c.Errf(c.ErrInvalid, "a daily limit must be at least 1")
	}
	return send(ctx, cl, cr, http.MethodPost, "/email-accounts/"+url.PathEscape(m.ExternalID), map[string]any{"max_email_per_day": limit})
}

// SetWarmup turns Smartlead's own warm-up on or off, one email account at
// a time.
func (Smartlead) SetWarmup(ctx context.Context, cl *c.Client, cr c.Creds, ms []c.MailboxState, on bool) error {
	for _, m := range ms {
		if err := send(ctx, cl, cr, http.MethodPost, "/email-accounts/"+url.PathEscape(m.ExternalID)+"/warmup", map[string]any{"warmup_enabled": on}); err != nil {
			return err
		}
	}
	return nil
}

var _ c.WarmupController = Smartlead{}
