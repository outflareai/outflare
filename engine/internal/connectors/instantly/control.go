package instantly

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Mailbox allocation: Instantly identifies sending accounts by email, and a
// campaign's accounts are its email_list, replaced whole by PATCH
// /campaigns/{id}. Attach and Detach read the list fresh and change only
// the mailboxes they are given.

var (
	_ c.Controller       = Instantly{}
	_ c.WarmupController = Instantly{}
)

type controlAccount struct {
	Email        string   `json:"email"`
	FirstName    string   `json:"first_name"`
	LastName     string   `json:"last_name"`
	Status       int      `json:"status"`
	WarmupStatus int      `json:"warmup_status"`
	DailyLimit   *int     `json:"daily_limit"`
	WarmupScore  *float64 `json:"stat_warmup_score"`
}

func (Instantly) Observe(ctx context.Context, cl *c.Client, cr c.Creds) (c.Snapshot, error) {
	var snap c.Snapshot
	attached := map[string][]string{}
	err := page(ctx, func(after string) ([]campaign, string, error) {
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("starting_after", after)
		}
		var r struct {
			Items []campaign `json:"items"`
			Next  string     `json:"next_starting_after"`
		}
		err := call(ctx, cl, cr, http.MethodGet, "/campaigns", q, nil, &r)
		return r.Items, r.Next, err
	}, func(k campaign) error {
		st := controlStatus(k.Status)
		out := c.CampaignState{ExternalID: k.ID, Name: k.Name, Status: st}
		if k.DailyLimit > 0 {
			n := k.DailyLimit
			out.DailyLimit = &n
		}
		if len(k.Sequences) > 0 {
			days := 0
			for i, s := range k.Sequences[0].Steps {
				if i < len(k.Sequences[0].Steps)-1 { // each step holds the wait before the next
					days += s.Delay
				}
			}
			out.SequenceDays = &days
		}
		snap.Campaigns = append(snap.Campaigns, out)
		if st == "active" || st == "paused" {
			for _, e := range k.EmailList {
				e = strings.ToLower(e)
				attached[e] = append(attached[e], k.ID)
			}
		}
		return nil
	})
	if err != nil {
		return snap, err
	}
	err = page(ctx, func(after string) ([]controlAccount, string, error) {
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("starting_after", after)
		}
		var r struct {
			Items []controlAccount `json:"items"`
			Next  string           `json:"next_starting_after"`
		}
		err := call(ctx, cl, cr, http.MethodGet, "/accounts", q, nil, &r)
		return r.Items, r.Next, err
	}, func(a controlAccount) error {
		m := c.MailboxState{ExternalID: a.Email, Email: a.Email, Name: strings.TrimSpace(a.FirstName + " " + a.LastName),
			DailyLimit: a.DailyLimit, WarmupEnabled: a.WarmupStatus == 1, CampaignIDs: attached[strings.ToLower(a.Email)]}
		switch a.Status {
		case 1:
			m.Status = c.MailboxOK
		case 2:
			m.Status = c.MailboxPaused
		default:
			m.Status, m.StatusReason = c.MailboxError, map[int]string{-1: "connection error", -2: "soft bounce error", -3: "sending error"}[a.Status]
		}
		if a.WarmupScore != nil {
			n := int(*a.WarmupScore + 0.5)
			m.WarmupScore = &n
		}
		snap.Mailboxes = append(snap.Mailboxes, m)
		return nil
	})
	return snap, err
}

func controlStatus(s int) string {
	switch s {
	case 1, 4:
		return "active"
	case 2, -1, -2, -99:
		return "paused" // paused by a person, or by Instantly for account health
	case 3:
		return "completed"
	}
	return "draft"
}

func (i Instantly) Attach(ctx context.Context, cl *c.Client, cr c.Creds, campaignID string, ms []c.MailboxState) error {
	return i.editList(ctx, cl, cr, campaignID, func(list []string) []string {
		for _, m := range ms {
			if !slices.ContainsFunc(list, func(e string) bool { return strings.EqualFold(e, m.Email) }) {
				list = append(list, m.Email)
			}
		}
		return list
	})
}

func (i Instantly) Detach(ctx context.Context, cl *c.Client, cr c.Creds, campaignID string, ms []c.MailboxState) error {
	return i.editList(ctx, cl, cr, campaignID, func(list []string) []string {
		return slices.DeleteFunc(list, func(e string) bool {
			return slices.ContainsFunc(ms, func(m c.MailboxState) bool { return strings.EqualFold(e, m.Email) })
		})
	})
}

func (Instantly) editList(ctx context.Context, cl *c.Client, cr c.Creds, campaignID string, edit func([]string) []string) error {
	var k campaign
	path := "/campaigns/" + url.PathEscape(campaignID)
	if err := call(ctx, cl, cr, http.MethodGet, path, nil, nil, &k); err != nil {
		return err
	}
	before := slices.Clone(k.EmailList)
	after := edit(k.EmailList)
	if slices.Equal(before, after) {
		return nil
	}
	if after == nil {
		after = []string{}
	}
	return call(ctx, cl, cr, http.MethodPatch, path, nil, map[string]any{"email_list": after}, nil)
}

func (Instantly) SetDailyLimit(ctx context.Context, cl *c.Client, cr c.Creds, m c.MailboxState, limit int) error {
	if limit < 1 {
		return c.Errf(c.ErrInvalid, "a daily limit must be at least 1")
	}
	return call(ctx, cl, cr, http.MethodPatch, "/accounts/"+url.PathEscape(m.Email), nil, map[string]any{"daily_limit": limit}, nil)
}

// SetWarmup turns Instantly's own warm-up on or off for accounts, by email.
func (Instantly) SetWarmup(ctx context.Context, cl *c.Client, cr c.Creds, ms []c.MailboxState, on bool) error {
	emails := make([]string, 0, len(ms))
	for _, m := range ms {
		emails = append(emails, m.Email)
	}
	action := "disable"
	if on {
		action = "enable"
	}
	return call(ctx, cl, cr, http.MethodPost, "/accounts/warmup/"+action, nil, map[string]any{"emails": emails}, nil)
}
