package ads

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// New ads (step 6 of the paid ads plan) on Meta: a link ad with an image
// in one of the campaign's ad sets, made paused as a creative and an ad,
// and turned on later. New ad sets and campaigns aren't made here: they
// need an optimisation goal, billing and targeting of their own.

var _ c.AdCreator = MetaAds{}

func (MetaAds) DraftKinds() []string { return []string{c.DraftAd} }

// metaCallsToAction are the buttons a link ad here can show.
var metaCallsToAction = map[string]bool{"LEARN_MORE": true, "SIGN_UP": true, "SHOP_NOW": true, "SUBSCRIBE": true, "CONTACT_US": true,
	"DOWNLOAD": true, "GET_QUOTE": true, "APPLY_NOW": true, "GET_OFFER": true, "ORDER_NOW": true, "BOOK_NOW": true}

func metaCTA(s string) string {
	if s == "" {
		return "LEARN_MORE"
	}
	return strings.ToUpper(strings.TrimSpace(s))
}

func (MetaAds) CheckDraft(d c.AdDraft) error {
	if d.Kind != c.DraftAd {
		return c.Errf(c.ErrInvalid, "a Meta draft is a new ad in one of the campaign's ad sets; make new ad sets and campaigns in Meta Ads Manager")
	}
	if !metaID(d.AdGroupID) {
		return c.Errf(c.ErrInvalid, "a new Meta ad goes in one of the campaign's ad sets")
	}
	if err := draftURL(d.FinalURL); err != nil {
		return err
	}
	if err := draftURL(d.ImageURL); err != nil {
		return c.Errf(c.ErrInvalid, "image_url is the full address of the image the ad shows, starting with https://")
	}
	if len(d.Headlines) != 1 || len(d.Descriptions) > 1 {
		return c.Errf(c.ErrInvalid, "a Meta ad has one headline and at most one description")
	}
	h := strings.TrimSpace(d.Headlines[0])
	if h == "" || utf8.RuneCountInString(h) > 255 {
		return c.Errf(c.ErrInvalid, "the headline has text and at most 255 characters")
	}
	if len(d.Descriptions) == 1 && utf8.RuneCountInString(d.Descriptions[0]) > 255 {
		return c.Errf(c.ErrInvalid, "the description is at most 255 characters")
	}
	if t := strings.TrimSpace(d.PrimaryText); t == "" || utf8.RuneCountInString(t) > 2000 {
		return c.Errf(c.ErrInvalid, "a Meta ad has its main text (primary_text), at most 2,000 characters")
	}
	if !metaCallsToAction[metaCTA(d.CallToAction)] {
		return c.Errf(c.ErrInvalid, "call_to_action is learn_more, sign_up, shop_now, subscribe, contact_us, download, get_quote, apply_now, get_offer, order_now or book_now")
	}
	if d.PageID != "" && !metaID(d.PageID) {
		return c.Errf(c.ErrInvalid, "Meta page ids are numbers")
	}
	if len(d.Keywords) > 0 || d.DailyBudgetMicros != nil || d.Bidding != nil || len(d.Countries) > 0 || d.Path1 != "" || d.Path2 != "" {
		return c.Errf(c.ErrInvalid, "a new Meta ad takes its budget, bidding and targeting from its ad set")
	}
	return nil
}

// page is the Facebook page a new ad runs as: the draft's, else the first
// the account can promote.
func (m MetaAds) page(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, want string) (string, error) {
	if want != "" {
		return want, nil
	}
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := m.call(cl, creds).get(ctx, m.base(cl)+"/act_"+esc(acct.ID)+"/promote_pages?fields=id&limit=1", &resp); err != nil {
		return "", err
	}
	if len(resp.Data) == 0 || !metaID(resp.Data[0].ID) {
		return "", c.Errf(c.ErrInvalid, "the Meta ad account has no Facebook page to run ads as; connect one in Meta Business Settings")
	}
	return resp.Data[0].ID, nil
}

func (m MetaAds) CreateAds(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, d c.AdDraft) (c.AdMade, error) {
	if err := m.CheckDraft(d); err != nil {
		return c.AdMade{}, err
	}
	page, err := m.page(ctx, cl, creds, acct, d.PageID)
	if err != nil {
		return c.AdMade{}, err
	}
	link := map[string]any{"link": strings.TrimSpace(d.FinalURL), "message": strings.TrimSpace(d.PrimaryText), "name": strings.TrimSpace(d.Headlines[0]),
		"picture": strings.TrimSpace(d.ImageURL), "call_to_action": map[string]any{"type": metaCTA(d.CallToAction), "value": map[string]any{"link": strings.TrimSpace(d.FinalURL)}}}
	if len(d.Descriptions) == 1 && strings.TrimSpace(d.Descriptions[0]) != "" {
		link["description"] = strings.TrimSpace(d.Descriptions[0])
	}
	spec, _ := json.Marshal(map[string]any{"page_id": page, "link_data": link})
	name := strings.TrimSpace(d.Name)
	if name == "" {
		name = strings.TrimSpace(d.Headlines[0])
	}
	var creative, ad struct {
		ID string `json:"id"`
	}
	if err := m.call(cl, creds).post(ctx, m.base(cl)+"/act_"+esc(acct.ID)+"/adcreatives", map[string]any{"name": name, "object_story_spec": string(spec)}, &creative); err != nil {
		return c.AdMade{}, err
	}
	if !metaID(creative.ID) {
		return c.AdMade{}, c.Errf(c.ErrUnavailable, "Meta made the creative but did not say its id")
	}
	ref, _ := json.Marshal(map[string]any{"creative_id": creative.ID})
	if err := m.call(cl, creds).post(ctx, m.base(cl)+"/act_"+esc(acct.ID)+"/ads", map[string]any{"name": name, "adset_id": d.AdGroupID,
		"creative": string(ref), "status": "PAUSED"}, &ad); err != nil {
		return c.AdMade{}, err
	}
	if !metaID(ad.ID) {
		return c.AdMade{}, c.Errf(c.ErrUnavailable, "Meta made the ad but did not say its id")
	}
	return c.AdMade{CampaignID: d.CampaignID, AdGroupID: d.AdGroupID, AdID: ad.ID, Made: []string{c.DraftAd}}, nil
}

func (m MetaAds) GoLive(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, made c.AdMade) error {
	if !metaID(made.AdID) {
		return c.Errf(c.ErrInvalid, "Meta ad ids are numbers")
	}
	return m.call(cl, creds).post(ctx, m.base(cl)+"/"+esc(made.AdID), map[string]any{"status": "ACTIVE"}, nil)
}
