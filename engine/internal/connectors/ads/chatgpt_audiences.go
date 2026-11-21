package ads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ChatGPT Ads custom audiences: a list of people by hashed email or phone
// number, which a campaign targets or excludes in Ads Manager. Outflare
// makes the list and keeps who is in it up to date; choosing the campaigns
// that use it stays in Ads Manager, because the API documents no way to
// change one part of a campaign's targeting and leave the rest as it is.
// OpenAI doesn't offer custom audiences on campaigns that show in the EEA
// or Switzerland.

var _ c.AudienceCreator = ChatGPTAds{}

// chatgptIdentifiersMax is how many identifiers one add or remove carries.
const chatgptIdentifiersMax = 10000

type chatgptAudience struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Audiences lists the account's custom audiences.
func (g ChatGPTAds) Audiences(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdAudience, error) {
	if err := g.checkKey(ctx, cl, creds, acct); err != nil {
		return nil, err
	}
	out := []c.AdAudience{}
	after := ""
	for i := 0; i < maxPages; i++ {
		u := g.base(cl) + "/custom_audiences?limit=100"
		if after != "" {
			u += "&after=" + esc(after)
		}
		var raw json.RawMessage
		if err := g.call(cl, creds).get(ctx, u, &raw); err != nil {
			return nil, err
		}
		// A list object, or a bare array.
		var page struct {
			Data    []chatgptAudience `json:"data"`
			LastID  string            `json:"last_id"`
			HasMore bool              `json:"has_more"`
		}
		if json.Unmarshal(raw, &page) != nil || page.Data == nil {
			_ = json.Unmarshal(raw, &page.Data)
		}
		for _, a := range page.Data {
			if a.ID != "" {
				out = append(out, c.AdAudience{ID: a.ID, Name: a.Name})
			}
		}
		if !page.HasMore || page.LastID == "" || page.LastID == after {
			break
		}
		after = page.LastID
	}
	return out, nil
}

// CreateAudience makes a custom audience, or finds the one with that
// name.
func (g ChatGPTAds) CreateAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, name, description string) (string, error) {
	have, err := g.Audiences(ctx, cl, creds, acct)
	if err != nil {
		return "", err
	}
	for _, a := range have {
		if a.Name == name {
			return a.ID, nil
		}
	}
	var out chatgptAudience
	err = g.call(cl, creds).post(ctx, g.base(cl)+"/custom_audiences", map[string]any{"name": name, "description": description}, &out)
	if err != nil {
		return "", g.audienceError(err)
	}
	if out.ID == "" {
		return "", c.Errf(c.ErrUnavailable, "OpenAI made no custom audience")
	}
	return out.ID, nil
}

func (ChatGPTAds) audienceError(err error) error {
	if c.KindOf(err) == c.ErrAuth {
		return c.Errf(c.ErrAuth, "OpenAI refused the change: check that the Advertiser API key on the connection is still active in Ads Manager, Settings, and that the ad account may use custom audiences (%v)", err)
	}
	return err
}

// SyncAudience adds one person to a custom audience, or takes them out.
func (g ChatGPTAds) SyncAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, audienceID, change string, p c.AudienceMember) error {
	_, err := g.IngestAudience(ctx, cl, creds, acct, audienceID, change, []c.AudienceMember{p})
	return err
}

// IngestAudience adds many people to a custom audience by their hashed
// emails and phone numbers, or takes them out. Each request carries an
// Idempotency-Key made from what it sends, so a retry of the same people
// is taken once. It returns the id of the last operation OpenAI started:
// membership changes there are carried out after the call answers.
func (g ChatGPTAds) IngestAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, audienceID, change string, members []c.AudienceMember) (string, error) {
	if !chatgptIDRe.MatchString(audienceID) {
		return "", c.Errf(c.ErrInvalid, "that is not a ChatGPT Ads custom audience id")
	}
	if err := g.checkKey(ctx, cl, creds, acct); err != nil {
		return "", err
	}
	verb := "add"
	if change == c.AudienceRemove {
		verb = "remove"
	}
	type identifier struct {
		Type  string `json:"identifier_type"`
		Value string `json:"identifier"`
	}
	var ids []identifier
	for _, p := range members {
		for _, e := range p.Emails {
			ids = append(ids, identifier{"email_sha256", e})
		}
		for _, ph := range p.Phones {
			ids = append(ids, identifier{"phone_number_sha256", ph})
		}
	}
	operation := ""
	for start := 0; start < len(ids); start += chatgptIdentifiersMax {
		batch := ids[start:min(start+chatgptIdentifiersMax, len(ids))]
		h := sha256.New()
		h.Write([]byte(audienceID + "\n" + verb))
		for _, id := range batch {
			h.Write([]byte("\n" + id.Type + ":" + id.Value))
		}
		var out struct {
			OperationID string `json:"operation_id"`
		}
		err := g.call(cl, creds).set("Idempotency-Key", "outflare-"+hex.EncodeToString(h.Sum(nil))[:48]).
			post(ctx, g.base(cl)+"/custom_audiences/"+esc(audienceID)+"/"+verb, map[string]any{"identifiers": batch}, &out)
		if err != nil {
			return operation, g.audienceError(err)
		}
		if s := strings.TrimSpace(out.OperationID); s != "" {
			operation = s
		}
	}
	return operation, nil
}
