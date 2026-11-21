package ads

import (
	"context"
	"encoding/json"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Reddit's customer lists are custom audiences of the type CUSTOMER_LIST,
// filled with hashed emails.

var _ c.AudienceCreator = RedditAds{}

// redditUsersMax is how many people one users call carries.
const redditUsersMax = 2500

// Audiences lists the account's customer list audiences.
func (r RedditAds) Audiences(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdAudience, error) {
	out := []c.AdAudience{}
	err := r.list(ctx, cl, creds, "GET", r.base(cl)+"/ad_accounts/"+esc(acct.ID)+"/custom_audiences", nil, func(raw json.RawMessage) error {
		var list []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &list)
		for _, a := range list {
			if a.ID != "" && (a.Type == "" || a.Type == "CUSTOMER_LIST") {
				out = append(out, c.AdAudience{ID: a.ID, Name: a.Name})
			}
		}
		return nil
	})
	return out, err
}

// CreateAudience makes a customer list audience, or finds the one with
// that name.
func (r RedditAds) CreateAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, name, description string) (string, error) {
	have, err := r.Audiences(ctx, cl, creds, acct)
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
	body := map[string]any{"data": map[string]any{"name": name, "type": "CUSTOMER_LIST"}}
	if err := r.call(cl, creds).post(ctx, r.base(cl)+"/ad_accounts/"+esc(acct.ID)+"/custom_audiences", body, &out); err != nil {
		return "", r.editError(err)
	}
	if out.Data.ID == "" {
		return "", c.Errf(c.ErrUnavailable, "Reddit made no custom audience")
	}
	return out.Data.ID, nil
}

func (RedditAds) editError(err error) error {
	if c.KindOf(err) == c.ErrAuth {
		return c.Errf(c.ErrAuth, "Reddit refused the change. Connect Reddit Ads again so it grants edit access, with an account that can manage this ad account.")
	}
	return err
}

// SyncAudience adds one person to a customer list, or takes them out.
func (r RedditAds) SyncAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, audienceID, change string, p c.AudienceMember) error {
	_, err := r.IngestAudience(ctx, cl, creds, acct, audienceID, change, []c.AudienceMember{p})
	return err
}

// IngestAudience adds many people to a customer list by their hashed
// emails, or takes them out. Reddit matches them on its side and gives no
// request id; someone with no email is left out.
func (r RedditAds) IngestAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, audienceID, change string, members []c.AudienceMember) (string, error) {
	action := "ADD"
	if change == c.AudienceRemove {
		action = "REMOVE"
	}
	var rows [][]string
	for _, p := range members {
		for _, e := range p.Emails {
			rows = append(rows, []string{e})
		}
	}
	for start := 0; start < len(rows); start += redditUsersMax {
		body := map[string]any{"data": map[string]any{"action_type": action, "column_order": []string{"EMAIL_SHA256"},
			"user_data": rows[start:min(start+redditUsersMax, len(rows))]}}
		if err := r.call(cl, creds).patch(ctx, r.base(cl)+"/custom_audiences/"+esc(audienceID)+"/users", body, nil); err != nil {
			return "", r.editError(err)
		}
	}
	return "", nil
}
