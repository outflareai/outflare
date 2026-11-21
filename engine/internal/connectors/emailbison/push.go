package emailbison

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// People are added to a campaign the way the API reference does it:
//
//  1. Each address is looked up in the campaign
//     (GET /api/campaigns/{id}/leads?search=); one already there is left
//     as it is (PushAlreadyIn), so a retry never adds anyone twice.
//  2. Then in the whole account (GET /api/leads?search=). A contact the
//     account already holds is left out (PushRefused), because someone may
//     be emailing them already, unless the push allows people in other
//     campaigns (AllowElsewhere) or the contact is this push's own unused
//     one: it carries the push's Note, is in no campaign, and has been sent
//     nothing and replied to nothing (GET /api/leads/{id}). That is an
//     earlier try that failed before the contact went into the campaign.
//  3. New contacts (and this push's own unused ones) are created or
//     patched in one call for the batch,
//     POST /api/leads/create-or-update/multiple, with email, first_name,
//     last_name, company and notes, and custom_variables as name and value
//     pairs that must already exist in EmailBison. An address the answer
//     leaves out was not taken; it is that person's error.
//  4. Everyone is attached in one call,
//     POST /api/campaigns/{id}/leads/attach-leads. Leads attached to an
//     active campaign can take up to five minutes to start.

var (
	_ c.LeadPusher        = EmailBison{}
	_ c.LeadsPusher       = EmailBison{}
	_ c.CampaignReadiness = EmailBison{}
)

// upsertChunk is the most contacts one create-or-update call takes.
const upsertChunk = 500

type foundLead struct {
	ID    id     `json:"id"`
	Email string `json:"email"`
}

func (a *api) findLead(ctx context.Context, path, email string) (string, error) {
	var p page[foundLead]
	if err := a.do(ctx, http.MethodGet, path+"?search="+url.QueryEscape(email), nil, &p); err != nil {
		return "", err
	}
	for _, l := range p.Data {
		if strings.EqualFold(l.Email, email) {
			return string(l.ID), nil
		}
	}
	return "", nil
}

// ownUnused says whether the account's contact leadID is this push's own
// and was never used: it carries note, is in no campaign, and has been
// sent nothing and has not replied. Anything EmailBison doesn't say counts
// as used.
func (a *api) ownUnused(ctx context.Context, leadID, email, note string) (bool, error) {
	if strings.TrimSpace(note) == "" {
		return false, nil
	}
	var got struct {
		Data struct {
			ID        id                `json:"id"`
			Email     string            `json:"email"`
			Notes     *string           `json:"notes"`
			Campaigns []json.RawMessage `json:"lead_campaign_data"`
			Stats     *struct {
				Sent    json.RawMessage `json:"emails_sent"`
				Replies json.RawMessage `json:"replies"`
			} `json:"overall_stats"`
		} `json:"data"`
	}
	if err := a.do(ctx, http.MethodGet, "/api/leads/"+url.PathEscape(leadID), nil, &got); err != nil {
		return false, err
	}
	d := got.Data
	zero := func(raw json.RawMessage) bool {
		var f float64
		return len(raw) > 0 && json.Unmarshal(raw, &f) == nil && f == 0
	}
	return string(d.ID) == leadID && strings.EqualFold(strings.TrimSpace(d.Email), email) &&
		d.Notes != nil && strings.TrimSpace(*d.Notes) == strings.TrimSpace(note) &&
		d.Campaigns != nil && len(d.Campaigns) == 0 && d.Stats != nil && zero(d.Stats.Sent) && zero(d.Stats.Replies), nil
}

func (e EmailBison) PushLead(ctx context.Context, cl *c.Client, creds c.Creds, campaignID string, l c.PushedLead) (c.LeadPushed, error) {
	out := e.PushLeads(ctx, cl, creds, campaignID, []c.PushedLead{l})
	return out[0].LeadPushed, out[0].Err
}

func (EmailBison) PushLeads(ctx context.Context, cl *c.Client, creds c.Creds, campaignID string, leads []c.PushedLead) []c.PushOutcome {
	out := make([]c.PushOutcome, len(leads))
	fail := func(idx []int, err error) {
		for _, i := range idx {
			out[i].Err = err
		}
	}
	all := make([]int, len(leads))
	for i := range leads {
		all[i] = i
	}
	a, err := open(cl, creds)
	if err != nil {
		fail(all, err)
		return out
	}
	campaign := "/api/campaigns/" + url.PathEscape(campaignID)
	var upsert, attach []int // indexes
	ids := make([]string, len(leads))
	for i, l := range leads {
		email := strings.ToLower(strings.TrimSpace(l.Email))
		if leadID, err := a.findLead(ctx, campaign+"/leads", email); err != nil {
			out[i].Err = err
			continue
		} else if leadID != "" {
			out[i].LeadPushed = c.LeadPushed{Outcome: c.PushAlreadyIn, ExternalID: leadID}
			continue
		}
		leadID, err := a.findLead(ctx, "/api/leads", email)
		if err != nil {
			out[i].Err = err
			continue
		}
		switch {
		case leadID == "":
			upsert = append(upsert, i)
		case l.AllowElsewhere:
			ids[i] = leadID
			attach = append(attach, i)
		default:
			ours, err := a.ownUnused(ctx, leadID, email, l.Note)
			switch {
			case err != nil:
				out[i].Err = err
			case ours:
				upsert = append(upsert, i)
			default:
				out[i].LeadPushed = c.LeadPushed{Outcome: c.PushRefused, ExternalID: leadID,
					Reason: "already a contact in this EmailBison account"}
			}
		}
	}
	for from := 0; from < len(upsert); from += upsertChunk {
		chunk := upsert[from:min(from+upsertChunk, len(upsert))]
		body := make([]map[string]any, len(chunk))
		for n, i := range chunk {
			l := leads[i]
			lead := map[string]any{"email": strings.ToLower(strings.TrimSpace(l.Email)), "first_name": l.FirstName, "last_name": l.LastName,
				"company": l.Company, "notes": l.Note}
			if len(l.Variables) > 0 {
				names := make([]string, 0, len(l.Variables))
				for n := range l.Variables {
					names = append(names, n)
				}
				sort.Strings(names)
				vars := make([]map[string]string, len(names))
				for k, n := range names {
					vars[k] = map[string]string{"name": n, "value": l.Variables[n]}
				}
				lead["custom_variables"] = vars
			}
			body[n] = lead
		}
		var made struct {
			Data []foundLead `json:"data"`
		}
		if err := a.do(ctx, http.MethodPost, "/api/leads/create-or-update/multiple",
			map[string]any{"existing_lead_behavior": "patch", "leads": body}, &made); err != nil {
			fail(chunk, err)
			continue
		}
		byEmail := map[string]string{}
		for _, m := range made.Data {
			if m.ID != "" {
				byEmail[strings.ToLower(strings.TrimSpace(m.Email))] = string(m.ID)
			}
		}
		for _, i := range chunk {
			if ids[i] = byEmail[strings.ToLower(strings.TrimSpace(leads[i].Email))]; ids[i] == "" {
				out[i].Err = c.Errf(c.ErrUnavailable, "EmailBison didn't take this address or didn't say its id")
				continue
			}
			attach = append(attach, i)
		}
	}
	if len(attach) == 0 {
		return out
	}
	// Ids are numbers, as for sender emails.
	nums, strs := make([]int, 0, len(attach)), make([]string, 0, len(attach))
	for _, i := range attach {
		strs = append(strs, ids[i])
		if n, err := strconv.Atoi(ids[i]); err == nil {
			nums = append(nums, n)
		}
	}
	var lids any = strs
	if len(nums) == len(strs) {
		lids = nums
	}
	if err := a.do(ctx, http.MethodPost, campaign+"/leads/attach-leads", map[string]any{"lead_ids": lids}, nil); err != nil {
		fail(attach, err)
		return out
	}
	for _, i := range attach {
		out[i].LeadPushed = c.LeadPushed{Outcome: c.PushAdded, ExternalID: ids[i]}
	}
	return out
}

var _ c.ContactHistory = EmailBison{}

// ContactSent says whether the account has a lead with this address and
// how many emails it has sent them (connectors.ContactHistory), from
// GET /api/leads?search=. The search also matches other leads, so only the
// row whose email is the address counts. It only reads.
func (EmailBison) ContactSent(ctx context.Context, cl *c.Client, creds c.Creds, email string) (c.SentBefore, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return c.SentBefore{}, c.Errf(c.ErrInvalid, "EmailBison needs an email address to look up")
	}
	a, err := open(cl, creds)
	if err != nil {
		return c.SentBefore{}, err
	}
	// The counts are numbers; anything else counts as none.
	var p page[struct {
		Email string          `json:"email"`
		Sent  json.RawMessage `json:"emails_sent"`
		Stats *struct {
			Sent json.RawMessage `json:"emails_sent"`
		} `json:"overall_stats"`
	}]
	if err := a.do(ctx, http.MethodGet, "/api/leads?search="+url.QueryEscape(email), nil, &p); err != nil {
		return c.SentBefore{}, err
	}
	count := func(raw json.RawMessage) (int, bool) {
		var f float64
		if len(raw) == 0 || json.Unmarshal(raw, &f) != nil || f < 0 {
			return 0, false
		}
		return int(f), true
	}
	for _, l := range p.Data {
		if !strings.EqualFold(strings.TrimSpace(l.Email), email) {
			continue
		}
		out := c.SentBefore{Found: true}
		if l.Stats != nil {
			if n, ok := count(l.Stats.Sent); ok {
				out.EmailsSent = n
				return out, nil
			}
		}
		out.EmailsSent, _ = count(l.Sent)
		return out, nil
	}
	return c.SentBefore{}, nil
}
