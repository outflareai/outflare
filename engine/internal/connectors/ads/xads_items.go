package ads

import (
	"context"
	"net/http"
	"net/url"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// X's ad groups are line items: each sits in a campaign, holds its own
// targeting and bid, and can be paused on its own. Customer lists are X's
// custom audiences, filled with hashed emails and phone numbers.

var (
	_ c.AdItemsReader   = XAds{}
	_ c.AdGroupManager  = XAds{}
	_ c.AudienceCreator = XAds{}
)

type xLineItem struct {
	ID           string `json:"id"`
	CampaignID   string `json:"campaign_id"`
	Name         string `json:"name"`
	EntityStatus string `json:"entity_status"`
	Deleted      bool   `json:"deleted"`
}

func (x XAds) lineItems(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]xLineItem, error) {
	var out []xLineItem
	cursor := ""
	for i := 0; i < maxPages; i++ {
		q := url.Values{"count": {"1000"}, "with_deleted": {"true"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var resp struct {
			Data       []xLineItem `json:"data"`
			NextCursor string      `json:"next_cursor"`
		}
		if err := x.get(ctx, cl, creds, "/accounts/"+esc(acct.ID)+"/line_items", q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Data...)
		if cursor = resp.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// AdItems lists the account's line items as its ad groups. X's ads
// (promoted posts) aren't read, so the ad level is empty.
func (x XAds) AdItems(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, level string) ([]c.AdItem, error) {
	switch level {
	case c.AdLevelAd:
		return []c.AdItem{}, nil
	case c.AdLevelGroup:
	default:
		return nil, c.Errf(c.ErrInvalid, "level is %s or %s", c.AdLevelGroup, c.AdLevelAd)
	}
	items, err := x.lineItems(ctx, cl, creds, acct)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdItem, 0, len(items))
	for _, li := range items {
		st := li.EntityStatus
		if li.Deleted {
			st = "DELETED"
		}
		name := li.Name
		if name == "" {
			name = "Line item " + li.ID
		}
		out = append(out, c.AdItem{ID: li.ID, ParentID: li.CampaignID, Name: name, Status: status(st), ProviderStatus: st})
	}
	return out, nil
}

// AdItemDays reads each line item's numbers per day.
func (x XAds) AdItemDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, level, from, to string) ([]c.AdItemDay, error) {
	switch level {
	case c.AdLevelAd:
		return []c.AdItemDay{}, nil
	case c.AdLevelGroup:
	default:
		return nil, c.Errf(c.ErrInvalid, "level is %s or %s", c.AdLevelGroup, c.AdLevelAd)
	}
	items, err := x.lineItems(ctx, cl, creds, acct)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	by := map[string]xLineItem{}
	for _, li := range items {
		ids = append(ids, li.ID)
		by[li.ID] = li
	}
	days, err := x.stats(ctx, cl, creds, acct, "LINE_ITEM", ids, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdItemDay, 0, len(days))
	for _, d := range days {
		li := by[d.CampaignID]
		out = append(out, c.AdItemDay{ID: d.CampaignID, ParentID: li.CampaignID, Name: li.Name, Date: d.Date,
			SpendMicros: d.SpendMicros, Impressions: d.Impressions, Clicks: d.Clicks,
			Conversions: d.Conversions, ConversionValueMicros: d.ConversionValueMicros})
	}
	return out, nil
}

// SetAdGroupStatus pauses or resumes one line item.
func (x XAds) SetAdGroupStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID string, active bool) error {
	if !xID(adGroupID) {
		return c.Errf(c.ErrInvalid, "an X line item id is required")
	}
	st := "PAUSED"
	if active {
		st = "ACTIVE"
	}
	err := x.send(ctx, cl, creds, http.MethodPut, "/accounts/"+esc(acct.ID)+"/line_items/"+esc(adGroupID), url.Values{"entity_status": {st}}, nil, nil)
	if c.KindOf(err) == c.ErrAuth {
		return c.Errf(c.ErrAuth, "X refused the change. Make a new access token after setting your developer app's permissions to Read and write, and paste it on the connection.")
	}
	return err
}

// ---- audiences ----

// xAudienceUsersMax is how many people one users call carries.
const xAudienceUsersMax = 2500

// xAudienceDays is how long X keeps someone in a list after they were
// last sent: the longest X allows, so a daily sync keeps members in.
const xAudienceDays = 540

// Audiences lists the account's custom audiences that are customer
// lists, the kind people can be added to.
func (x XAds) Audiences(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdAudience, error) {
	out := []c.AdAudience{}
	cursor := ""
	for i := 0; i < maxPages; i++ {
		q := url.Values{"count": {"1000"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var resp struct {
			Data []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Type    string `json:"audience_type"`
				Deleted bool   `json:"deleted"`
			} `json:"data"`
			NextCursor string `json:"next_cursor"`
		}
		if err := x.get(ctx, cl, creds, "/accounts/"+esc(acct.ID)+"/custom_audiences", q, &resp); err != nil {
			return nil, err
		}
		for _, a := range resp.Data {
			if a.ID != "" && !a.Deleted && (a.Type == "" || a.Type == "CRM" || a.Type == "LIST") {
				out = append(out, c.AdAudience{ID: a.ID, Name: a.Name})
			}
		}
		if cursor = resp.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// CreateAudience makes a custom audience, or finds the one with that name.
func (x XAds) CreateAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, name, description string) (string, error) {
	have, err := x.Audiences(ctx, cl, creds, acct)
	if err != nil {
		return "", err
	}
	for _, a := range have {
		if a.Name == name {
			return a.ID, nil
		}
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	q := url.Values{"name": {name}}
	if description != "" {
		q.Set("description", description)
	}
	if err := x.send(ctx, cl, creds, http.MethodPost, "/accounts/"+esc(acct.ID)+"/custom_audiences", q, nil, &out); err != nil {
		return "", err
	}
	if out.Data.ID == "" {
		return "", c.Errf(c.ErrUnavailable, "X made no custom audience")
	}
	return out.Data.ID, nil
}

// SyncAudience adds one person to a custom audience, or takes them out.
func (x XAds) SyncAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, audienceID, change string, p c.AudienceMember) error {
	_, err := x.IngestAudience(ctx, cl, creds, acct, audienceID, change, []c.AudienceMember{p})
	return err
}

// IngestAudience adds many people to a custom audience, or takes them
// out, by their hashed emails and phone numbers. X matches them on its
// side and gives no request id.
func (x XAds) IngestAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, audienceID, change string, members []c.AudienceMember) (string, error) {
	if !xID(audienceID) {
		return "", c.Errf(c.ErrInvalid, "an X custom audience id is required")
	}
	op := "Update"
	if change == c.AudienceRemove {
		op = "Delete"
	}
	var users []map[string]any
	for _, p := range members {
		u := map[string]any{}
		if len(p.Emails) > 0 {
			u["email"] = p.Emails
		}
		if len(p.Phones) > 0 {
			u["phone_number"] = p.Phones
		}
		if len(u) > 0 {
			users = append(users, u)
		}
	}
	now := time.Now().UTC()
	for start := 0; start < len(users); start += xAudienceUsersMax {
		params := map[string]any{"users": users[start:min(start+xAudienceUsersMax, len(users))]}
		if op == "Update" {
			params["effective_at"] = now.Format(time.RFC3339)
			params["expires_at"] = now.AddDate(0, 0, xAudienceDays).Format(time.RFC3339)
		}
		body := []any{map[string]any{"operation_type": op, "params": params}}
		if err := x.send(ctx, cl, creds, http.MethodPost, "/accounts/"+esc(acct.ID)+"/custom_audiences/"+esc(audienceID)+"/users", nil, body, nil); err != nil {
			return "", err
		}
	}
	return "", nil
}
