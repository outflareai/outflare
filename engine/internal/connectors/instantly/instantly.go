// Package instantly connects Instantly (API v2) so a workspace can move its
// campaigns, leads and sending accounts over. It only reads.
//
// Written from Instantly's API v2 reference; not yet run against a live
// account.
package instantly

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const ID = "instantly"

type Instantly struct{}

func (Instantly) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: "Instantly", Category: c.CategorySequencer,
		Summary:     "Switching from Instantly? Import campaigns, leads and sending accounts in one go.",
		Description: "Reads your Instantly campaigns (with every step and variant), their leads and where each lead got to, and your sending accounts. Importing only reads. Once connected, mailbox allocation also adds and removes sending accounts on Instantly campaigns, and accepts changes you make in Instantly as manual ones.",
		Website:     "https://instantly.ai", DocsURL: "https://developer.instantly.ai/",
		KeyURL: "https://app.instantly.ai/app/settings/integrations", Auth: c.AuthAPIKey,
		Fields:       []c.Field{{Name: "api_key", Label: "API key (v2)", Secret: true, Help: "Settings → Integrations → API keys. Give it all read scopes."}},
		Capabilities: []string{c.CapImportCampaigns, c.CapImportLeads, c.CapImportMailboxes, c.CapObserveMailboxes, c.CapControlMailboxes, c.CapControlWarmup, c.CapBlocklist},
		Status:       c.Available,
		Pricing:      "Included in Instantly's Hypergrowth plan and above",
	}
}

func base(cl *c.Client) string { return cl.Base(ID, "https://api.instantly.ai") + "/api/v2" }

func call(ctx context.Context, cl *c.Client, cr c.Creds, method, path string, q url.Values, body any, v any) error {
	u := base(cl) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	var req *http.Request
	if rd != nil {
		req, _ = http.NewRequest(method, u, rd)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, u, nil)
	}
	req.Header.Set("Authorization", "Bearer "+cr["api_key"])
	_, err := c.DoJSON(ctx, cl, "Instantly", req, v)
	return err
}

func (Instantly) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var ws struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := call(ctx, cl, cr, http.MethodGet, "/workspaces/current", nil, nil, &ws); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: ws.ID, Name: ws.Name}, nil
}

// page walks a cursor-paged list endpoint.
func page[T any](ctx context.Context, fetch func(after string) ([]T, string, error), yield func(T) error) error {
	after := ""
	for range 10000 {
		items, next, err := fetch(after)
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := yield(it); err != nil {
				return err
			}
		}
		if next == "" || len(items) == 0 || next == after {
			return nil
		}
		after = next
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return c.Errf(c.ErrUnavailable, "Instantly kept returning more pages; stopped")
}

type campaign struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Status     int      `json:"status"`
	DailyLimit int      `json:"daily_limit"`
	EmailList  []string `json:"email_list"`
	Schedule   struct {
		Schedules []struct {
			Timezone string `json:"timezone"`
		} `json:"schedules"`
	} `json:"campaign_schedule"`
	Sequences []struct {
		Steps []struct {
			Type     string `json:"type"`
			Delay    int    `json:"delay"`
			Variants []struct {
				Subject string `json:"subject"`
				Body    string `json:"body"`
			} `json:"variants"`
		} `json:"steps"`
	} `json:"sequences"`
}

var campaignStatus = map[int]string{0: "draft", 1: "active", 2: "paused", 3: "completed", 4: "running_subsequences", -99: "suspended", -1: "unhealthy", -2: "bounce_protect"}

func (Instantly) Campaigns(ctx context.Context, cl *c.Client, cr c.Creds, yield func(c.ExternalCampaign) error) error {
	return page(ctx, func(after string) ([]campaign, string, error) {
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
		out := c.ExternalCampaign{ID: k.ID, Name: k.Name, Status: campaignStatus[k.Status], DailyLimit: k.DailyLimit, MailboxEmails: k.EmailList, Steps: []c.ExternalStep{}}
		if out.Status == "" {
			out.Status = fmt.Sprint(k.Status)
		}
		if len(k.Schedule.Schedules) > 0 {
			out.Timezone = k.Schedule.Schedules[0].Timezone
		}
		if len(k.Sequences) > 0 {
			for _, st := range k.Sequences[0].Steps {
				if st.Type != "" && st.Type != "email" || len(st.Variants) == 0 {
					continue
				}
				s := c.ExternalStep{DelayDays: st.Delay, Subject: st.Variants[0].Subject, Body: st.Variants[0].Body}
				for _, v := range st.Variants[1:] {
					s.Variants = append(s.Variants, v.Body)
				}
				out.Steps = append(out.Steps, s)
			}
			// Instantly stores on each step the wait before the next one;
			// ExternalStep wants the wait since the previous one.
			for i := len(out.Steps) - 1; i >= 0; i-- {
				if i == 0 {
					out.Steps[i].DelayDays = 0
				} else {
					out.Steps[i].DelayDays = out.Steps[i-1].DelayDays
				}
			}
		}
		return yield(out)
	})
}

type lead struct {
	Email       string         `json:"email"`
	FirstName   string         `json:"first_name"`
	LastName    string         `json:"last_name"`
	CompanyName string         `json:"company_name"`
	JobTitle    string         `json:"job_title"`
	Status      int            `json:"status"`
	Payload     map[string]any `json:"payload"`
	// The step last sent ("0_2_0" is sequence 0, step 2, variant 0) and from which account.
	LastStep        string `json:"last_step_id"`
	LastContactFrom string `json:"last_contacted_from"`
	ReplyCount      int    `json:"email_reply_count"`
}

var leadStatus = map[int]string{1: "active", 2: "paused", 3: "completed", -1: "bounced", -2: "unsubscribed", -3: "skipped"}

func (Instantly) CampaignLeads(ctx context.Context, cl *c.Client, cr c.Creds, campaignID string, yield func(c.ExternalLead) error) error {
	return page(ctx, func(after string) ([]lead, string, error) {
		body := map[string]any{"campaign": campaignID, "limit": 100}
		if after != "" {
			body["starting_after"] = after
		}
		var r struct {
			Items []lead `json:"items"`
			Next  string `json:"next_starting_after"`
		}
		err := call(ctx, cl, cr, http.MethodPost, "/leads/list", nil, body, &r)
		return r.Items, r.Next, err
	}, func(l lead) error {
		out := c.ExternalLead{Email: l.Email, FirstName: l.FirstName, LastName: l.LastName, Company: l.CompanyName, Title: l.JobTitle,
			Status: leadStatus[l.Status], Mailbox: l.LastContactFrom}
		if l.ReplyCount > 0 {
			out.Status = "replied"
		}
		if out.Status == "" {
			out.Status = "active"
		}
		if parts := strings.Split(l.LastStep, "_"); len(parts) >= 2 {
			var n int
			if _, err := fmt.Sscan(parts[1], &n); err == nil {
				out.StepDone = n + 1
			}
		}
		for k, v := range l.Payload {
			if out.Fields == nil {
				out.Fields = map[string]string{}
			}
			out.Fields[k] = fmt.Sprint(v)
		}
		return yield(out)
	})
}

type account struct {
	Email        string `json:"email"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Status       int    `json:"status"`
	WarmupStatus int    `json:"warmup_status"`
	DailyLimit   int    `json:"daily_limit"`
	ProviderCode int    `json:"provider_code"`
}

func (Instantly) SendingAccounts(ctx context.Context, cl *c.Client, cr c.Creds, yield func(c.ExternalMailbox) error) error {
	return page(ctx, func(after string) ([]account, string, error) {
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("starting_after", after)
		}
		var r struct {
			Items []account `json:"items"`
			Next  string    `json:"next_starting_after"`
		}
		err := call(ctx, cl, cr, http.MethodGet, "/accounts", q, nil, &r)
		return r.Items, r.Next, err
	}, func(a account) error {
		status := map[int]string{1: "active", 2: "paused", -1: "connection_error", -2: "soft_bounce_error", -3: "sending_error"}[a.Status]
		return yield(c.ExternalMailbox{
			Email: a.Email, Name: strings.TrimSpace(a.FirstName + " " + a.LastName), DailyLimit: a.DailyLimit,
			WarmupEnabled: a.WarmupStatus == 1, Status: status,
			Provider: map[int]string{1: "smtp", 2: "google", 3: "microsoft", 4: "smtp"}[a.ProviderCode],
		})
	})
}
