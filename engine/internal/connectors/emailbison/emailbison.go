// Package emailbison connects EmailBison, a cold email sequencer, for
// mailbox allocation: Outflare reads an account's sender emails and
// campaigns, attaches sender emails to campaigns and removes them, and
// sets their daily limits. It can also run warm-up in EmailBison instead
// of in Outflare, with PATCH /api/warmup/sender-emails/enable and disable.
//
// EmailBison runs one instance per customer (a dedicated or white-label
// URL), so the instance URL is part of the connection. Requests follow the
// public API reference at docs.emailbison.com: bearer token, Laravel-style
// pages under "data", sender emails attached with
// POST /api/campaigns/{id}/attach-sender-emails and removed with
// DELETE /api/campaigns/{id}/remove-sender-emails. Replies received in
// EmailBison come into the unified inbox and are answered there through
// EmailBison (replies.go). Not yet exercised
// against a live account.
package emailbison

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ID is the connector id.
const ID = "emailbison"

// EmailBison implements connectors.Controller.
type EmailBison struct{}

func (EmailBison) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: "EmailBison", Category: c.CategorySequencer,
		Summary:     "Let Outflare decide which sender emails each EmailBison campaign uses.",
		Description: "Outflare reads your sender emails and campaigns every few minutes, adds and removes sender emails on campaigns as mailbox allocation decides, and accepts changes you make in EmailBison as manual ones. Replies your campaigns get come into the Outflare inbox, labelled, and you can answer them there.",
		Website:     "https://emailbison.com", DocsURL: "https://docs.emailbison.com",
		KeyURL: "Settings > API in your EmailBison instance",
		Auth:   c.AuthAPIKey,
		Fields: []c.Field{
			{Name: "base_url", Label: "Instance URL", Placeholder: "https://send.youragency-com.example", Help: "The address you open EmailBison at."},
			{Name: "api_key", Label: "API key", Secret: true, Help: "Create one under Settings > API."},
		},
		Capabilities: []string{c.CapObserveMailboxes, c.CapControlMailboxes, c.CapControlWarmup, c.CapReadReplies, c.CapBlocklist, c.CapPushLeads},
	}
}

type api struct {
	cl    *c.Client
	base  string
	token string
}

func open(cl *c.Client, creds c.Creds) (*api, error) {
	base := strings.TrimRight(strings.TrimSpace(creds["base_url"]), "/")
	if u, err := url.Parse(base); err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
		return nil, c.Errf(c.ErrInvalid, "the instance URL should look like https://send.youragency-com.example")
	}
	return &api{cl: cl, base: cl.Base(ID, base), token: creds["api_key"]}, nil
}

func (a *api) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return c.Errf(c.ErrInvalid, "bad EmailBison request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.cl.Do(req)
	if err != nil {
		return c.Errf(c.ErrUnavailable, "could not reach EmailBison: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return c.Errf(c.ErrUnavailable, "reading the EmailBison response: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.HTTPError("EmailBison", resp.StatusCode, string(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return c.Errf(c.ErrUnavailable, "EmailBison returned something that is not JSON")
		}
	}
	return nil
}

// page is one Laravel-style page.
type page[T any] struct {
	Data []T `json:"data"`
	Meta struct {
		CurrentPage int `json:"current_page"`
		LastPage    int `json:"last_page"`
	} `json:"meta"`
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
}

// all reads every page of path.
func all[T any](ctx context.Context, a *api, path string) ([]T, error) {
	var out []T
	for n := 1; n <= 1000; n++ {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		var p page[T]
		if err := a.do(ctx, http.MethodGet, path+sep+"page="+strconv.Itoa(n), nil, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Data...)
		if len(p.Data) == 0 || p.Links.Next == nil && (p.Meta.LastPage == 0 || n >= p.Meta.LastPage) {
			break
		}
	}
	return out, nil
}

// id reads ids sent as numbers or strings.
type id string

func (i *id) UnmarshalJSON(b []byte) error {
	*i = id(strings.Trim(string(b), `"`))
	return nil
}

type senderEmail struct {
	ID            id       `json:"id"`
	Email         string   `json:"email"`
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	DailyLimit    *int     `json:"daily_limit"`
	WarmupEnabled *bool    `json:"warmup_enabled"`
	WarmupScore   *float64 `json:"warmup_score"`
}

type campaign struct {
	ID     id     `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// The campaign's cap on emails a day, when set.
	MaxEmailsPerDay *int `json:"max_emails_per_day"`
}

func (EmailBison) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	a, err := open(cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	var me struct {
		Data struct {
			ID    id     `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"data"`
	}
	if err := a.do(ctx, http.MethodGet, "/api/users", nil, &me); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: string(me.Data.ID), Name: me.Data.Name, Email: me.Data.Email}, nil
}

func (EmailBison) Observe(ctx context.Context, cl *c.Client, creds c.Creds) (c.Snapshot, error) {
	a, err := open(cl, creds)
	if err != nil {
		return c.Snapshot{}, err
	}
	senders, err := all[senderEmail](ctx, a, "/api/sender-emails")
	if err != nil {
		return c.Snapshot{}, err
	}
	camps, err := all[campaign](ctx, a, "/api/campaigns")
	if err != nil {
		return c.Snapshot{}, err
	}
	attached := map[string][]string{} // sender email id -> campaign ids
	var snap c.Snapshot
	for _, k := range camps {
		status := campaignStatus(k.Status)
		snap.Campaigns = append(snap.Campaigns, c.CampaignState{ExternalID: string(k.ID), Name: k.Name, Status: status, DailyLimit: k.MaxEmailsPerDay})
		if status == "completed" || status == "draft" {
			continue
		}
		on, err := all[senderEmail](ctx, a, "/api/campaigns/"+url.PathEscape(string(k.ID))+"/sender-emails")
		if err != nil {
			return c.Snapshot{}, err
		}
		for _, s := range on {
			attached[string(s.ID)] = append(attached[string(s.ID)], string(k.ID))
		}
	}
	for _, s := range senders {
		m := c.MailboxState{ExternalID: string(s.ID), Email: s.Email, Name: s.Name, DailyLimit: s.DailyLimit,
			CampaignIDs: attached[string(s.ID)]}
		m.Status, m.StatusReason = mailboxStatus(s.Status)
		if s.WarmupEnabled != nil {
			m.WarmupEnabled = *s.WarmupEnabled
		}
		if s.WarmupScore != nil {
			score := int(*s.WarmupScore + 0.5)
			m.WarmupScore = &score
		}
		snap.Mailboxes = append(snap.Mailboxes, m)
	}
	return snap, nil
}

func campaignStatus(s string) string {
	switch strings.ToLower(s) {
	case "active", "launching", "queued", "running":
		return "active"
	case "paused", "stopped", "pending":
		return "paused"
	case "completed", "archived", "deleted", "failed":
		return "completed"
	}
	return "draft"
}

func mailboxStatus(s string) (string, string) {
	switch strings.ToLower(s) {
	case "connected", "active", "":
		return c.MailboxOK, ""
	case "paused":
		return c.MailboxPaused, ""
	case "disconnected", "not connected":
		return c.MailboxDisconnected, "EmailBison says it is not connected"
	}
	return c.MailboxError, "EmailBison status: " + s
}

func senderIDs(ms []c.MailboxState) ([]int, error) {
	out := make([]int, 0, len(ms))
	for _, m := range ms {
		n, err := strconv.Atoi(m.ExternalID)
		if err != nil {
			return nil, c.Errf(c.ErrInvalid, "EmailBison sender email id %q is not a number", m.ExternalID)
		}
		out = append(out, n)
	}
	return out, nil
}

func (EmailBison) Attach(ctx context.Context, cl *c.Client, creds c.Creds, campaignID string, ms []c.MailboxState) error {
	a, err := open(cl, creds)
	if err != nil {
		return err
	}
	ids, err := senderIDs(ms)
	if err != nil {
		return err
	}
	return a.do(ctx, http.MethodPost, "/api/campaigns/"+url.PathEscape(campaignID)+"/attach-sender-emails",
		map[string]any{"sender_email_ids": ids}, nil)
}

func (EmailBison) Detach(ctx context.Context, cl *c.Client, creds c.Creds, campaignID string, ms []c.MailboxState) error {
	a, err := open(cl, creds)
	if err != nil {
		return err
	}
	ids, err := senderIDs(ms)
	if err != nil {
		return err
	}
	return a.do(ctx, http.MethodDelete, "/api/campaigns/"+url.PathEscape(campaignID)+"/remove-sender-emails",
		map[string]any{"sender_email_ids": ids}, nil)
}

func (EmailBison) SetDailyLimit(ctx context.Context, cl *c.Client, creds c.Creds, m c.MailboxState, limit int) error {
	a, err := open(cl, creds)
	if err != nil {
		return err
	}
	if limit < 1 {
		return c.Errf(c.ErrInvalid, "a daily limit must be at least 1")
	}
	return a.do(ctx, http.MethodPatch, "/api/sender-emails/"+url.PathEscape(m.ExternalID), map[string]any{"daily_limit": limit}, nil)
}

// SetWarmup turns EmailBison's own warm-up on or off for sender emails.
func (EmailBison) SetWarmup(ctx context.Context, cl *c.Client, creds c.Creds, ms []c.MailboxState, on bool) error {
	a, err := open(cl, creds)
	if err != nil {
		return err
	}
	ids, err := senderIDs(ms)
	if err != nil {
		return err
	}
	action := "disable"
	if on {
		action = "enable"
	}
	return a.do(ctx, http.MethodPatch, "/api/warmup/sender-emails/"+action, map[string]any{"sender_email_ids": ids}, nil)
}

var (
	_ c.Controller       = EmailBison{}
	_ c.WarmupController = EmailBison{}
)

// String helps logs.
func (EmailBison) String() string { return fmt.Sprintf("connector %s", ID) }
