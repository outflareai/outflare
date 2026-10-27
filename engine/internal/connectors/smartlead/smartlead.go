// Package smartlead connects Smartlead so a workspace can move its
// campaigns, leads and email accounts over. It only reads.
//
// Written from Smartlead's API reference; not yet run against a live
// account.
package smartlead

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const ID = "smartlead"

type Smartlead struct{}

func (Smartlead) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: "Smartlead", Category: c.CategorySequencer,
		Summary:     "Switching from Smartlead? Import campaigns, leads and email accounts in one go.",
		Description: "Reads your Smartlead campaigns (with every step and variant), their leads and where each lead got to, and your email accounts. Importing only reads. Once connected, mailbox allocation also adds and removes email accounts on Smartlead campaigns, and accepts changes you make in Smartlead as manual ones.",
		Website:     "https://www.smartlead.ai", DocsURL: "https://api.smartlead.ai/reference/welcome",
		KeyURL: "https://app.smartlead.ai/app/settings/profile", Auth: c.AuthAPIKey,
		Fields:       []c.Field{{Name: "api_key", Label: "API key", Secret: true, Help: "Settings → Profile → API key."}},
		Capabilities: []string{c.CapImportCampaigns, c.CapImportLeads, c.CapImportMailboxes, c.CapObserveMailboxes, c.CapControlMailboxes, c.CapControlWarmup, c.CapBlocklist},
		Status:       c.Available,
	}
}

func get(ctx context.Context, cl *c.Client, cr c.Creds, path string, q url.Values, v any) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("api_key", cr["api_key"])
	req, _ := http.NewRequest(http.MethodGet, cl.Base(ID, "https://server.smartlead.ai")+"/api/v1"+path+"?"+q.Encode(), nil)
	_, err := c.DoJSON(ctx, cl, "Smartlead", req, v)
	return err
}

type campaign struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	MaxLeadsPerDay int    `json:"max_leads_per_day"`
	Timezone       string `json:"timezone"`
}

func (Smartlead) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var list []campaign
	if err := get(ctx, cl, cr, "/campaigns", nil, &list); err != nil {
		return c.Account{}, err
	}
	return c.Account{Details: map[string]string{"campaigns": strconv.Itoa(len(list))}}, nil
}

func (Smartlead) Campaigns(ctx context.Context, cl *c.Client, cr c.Creds, yield func(c.ExternalCampaign) error) error {
	var list []campaign
	if err := get(ctx, cl, cr, "/campaigns", nil, &list); err != nil {
		return err
	}
	for _, k := range list {
		id := strconv.Itoa(k.ID)
		out := c.ExternalCampaign{ID: id, Name: k.Name, Status: strings.ToLower(k.Status), DailyLimit: k.MaxLeadsPerDay, Timezone: k.Timezone, Steps: []c.ExternalStep{}, MailboxEmails: []string{}}
		var seqs []struct {
			SeqNumber int `json:"seq_number"`
			Delay     struct {
				Days int `json:"delay_in_days"`
			} `json:"seq_delay_details"`
			Subject  string `json:"subject"`
			Body     string `json:"email_body"`
			Variants []struct {
				Subject string `json:"subject"`
				Body    string `json:"email_body"`
			} `json:"sequence_variants"`
		}
		if err := get(ctx, cl, cr, "/campaigns/"+id+"/sequences", nil, &seqs); err != nil {
			return err
		}
		for _, s := range seqs {
			st := c.ExternalStep{DelayDays: s.Delay.Days, Subject: s.Subject, Body: s.Body}
			if len(s.Variants) > 0 {
				if st.Body == "" {
					st.Subject, st.Body = s.Variants[0].Subject, s.Variants[0].Body
				}
				for _, v := range s.Variants[1:] {
					st.Variants = append(st.Variants, v.Body)
				}
			}
			out.Steps = append(out.Steps, st)
		}
		var accts []struct {
			FromEmail string `json:"from_email"`
		}
		if err := get(ctx, cl, cr, "/campaigns/"+id+"/email-accounts", nil, &accts); err != nil {
			return err
		}
		for _, a := range accts {
			out.MailboxEmails = append(out.MailboxEmails, a.FromEmail)
		}
		if err := yield(out); err != nil {
			return err
		}
	}
	return nil
}

func (Smartlead) CampaignLeads(ctx context.Context, cl *c.Client, cr c.Creds, campaignID string, yield func(c.ExternalLead) error) error {
	for offset := 0; ; offset += 100 {
		var r struct {
			Total flexInt `json:"total_leads"`
			Data  []struct {
				Status string `json:"status"`
				Lead   struct {
					Email        string         `json:"email"`
					FirstName    string         `json:"first_name"`
					LastName     string         `json:"last_name"`
					CompanyName  string         `json:"company_name"`
					CustomFields map[string]any `json:"custom_fields"`
				} `json:"lead"`
			} `json:"data"`
		}
		if err := get(ctx, cl, cr, "/campaigns/"+campaignID+"/leads", url.Values{"offset": {strconv.Itoa(offset)}, "limit": {"100"}}, &r); err != nil {
			return err
		}
		for _, d := range r.Data {
			out := c.ExternalLead{Email: d.Lead.Email, FirstName: d.Lead.FirstName, LastName: d.Lead.LastName, Company: d.Lead.CompanyName,
				Status: map[string]string{"STARTED": "active", "INPROGRESS": "active", "COMPLETED": "completed", "PAUSED": "paused", "STOPPED": "paused", "BLOCKED": "unsubscribed"}[strings.ToUpper(d.Status)]}
			if out.Status == "" {
				out.Status = "active"
			}
			for k, v := range d.Lead.CustomFields {
				if out.Fields == nil {
					out.Fields = map[string]string{}
				}
				out.Fields[k] = fmt.Sprint(v)
			}
			if err := yield(out); err != nil {
				return err
			}
		}
		if len(r.Data) < 100 || (r.Total > 0 && offset+100 >= int(r.Total)) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (Smartlead) SendingAccounts(ctx context.Context, cl *c.Client, cr c.Creds, yield func(c.ExternalMailbox) error) error {
	for offset := 0; ; offset += 100 {
		var list []struct {
			FromEmail     string `json:"from_email"`
			FromName      string `json:"from_name"`
			Type          string `json:"type"`
			MessagePerDay int    `json:"message_per_day"`
			SMTPOK        bool   `json:"is_smtp_success"`
			IMAPOK        bool   `json:"is_imap_success"`
			Warmup        *struct {
				Status string `json:"status"`
			} `json:"warmup_details"`
		}
		if err := get(ctx, cl, cr, "/email-accounts/", url.Values{"offset": {strconv.Itoa(offset)}, "limit": {"100"}}, &list); err != nil {
			return err
		}
		for _, a := range list {
			status := "active"
			if !a.SMTPOK || !a.IMAPOK {
				status = "connection_error"
			}
			if err := yield(c.ExternalMailbox{
				Email: a.FromEmail, Name: a.FromName, DailyLimit: a.MessagePerDay, Status: status,
				WarmupEnabled: a.Warmup != nil && strings.EqualFold(a.Warmup.Status, "ACTIVE"),
				Provider:      map[string]string{"GMAIL": "google", "OUTLOOK": "microsoft", "SMTP": "smtp"}[strings.ToUpper(a.Type)],
			}); err != nil {
				return err
			}
		}
		if len(list) < 100 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// flexInt reads numbers Smartlead sometimes sends as strings.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	n, err := strconv.Atoi(strings.Trim(string(b), `"`))
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexInt(n)
	return nil
}
