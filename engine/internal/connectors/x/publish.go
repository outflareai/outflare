package x

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Publishing as the brand's X account, through the workspace's own app.
// X bills the app's owner for each post created.

var _ c.SocialPublisher = X{}

// CanPublish needs a sign-in; the scopes asked for at sign-in include
// tweet.write.
func (X) CanPublish(creds c.Creds) bool {
	return creds["access_token"] != "" || creds["refresh_token"] != ""
}

// MaxLength is X's limit for accounts without a paid plan.
func (X) MaxLength() int { return 280 }

// Publish creates a post with POST /2/tweets.
func (X) Publish(ctx context.Context, cl *c.Client, creds c.Creds, in c.SocialPostInput) (c.SocialResult, error) {
	if creds["access_token"] == "" {
		return c.SocialResult{}, c.Errf(c.ErrAuth, "sign in to the brand's X account first")
	}
	if in.Text == "" {
		return c.SocialResult{}, c.Errf(c.ErrInvalid, "the post has no text")
	}
	body, _ := json.Marshal(map[string]string{"text": in.Text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.Base(ID, apiBase)+"/2/tweets", bytes.NewReader(body))
	if err != nil {
		return c.SocialResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+creds["access_token"])
	req.Header.Set("Content-Type", "application/json")
	var r struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	status, err := c.DoJSON(ctx, cl, "X", req, &r)
	if status == http.StatusPaymentRequired {
		return c.SocialResult{}, c.Errf(c.ErrQuota, "X says the developer account has no credits left; add credits in the X developer console")
	}
	if err != nil {
		return c.SocialResult{}, err
	}
	if r.Data.ID == "" {
		return c.SocialResult{}, c.Errf(c.ErrUnavailable, "X did not return the new post's id")
	}
	handle := in.Account.Handle
	if handle == "" {
		handle = "i/web"
	}
	return c.SocialResult{ExternalID: r.Data.ID, URL: "https://x.com/" + url.PathEscape(handle) + "/status/" + r.Data.ID}, nil
}
