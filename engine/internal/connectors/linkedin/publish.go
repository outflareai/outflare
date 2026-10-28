package linkedin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Publishing as a page the person administers or as their own profile,
// through their sign-in. LinkedIn's API costs nothing.

var _ c.SocialPublisher = LinkedIn{}

// CanPublish needs a sign-in; it asks for w_member_social and
// w_organization_social.
func (LinkedIn) CanPublish(creds c.Creds) bool { return c.Token(creds) != "" }

// MaxLength is LinkedIn's limit for a post's commentary.
func (LinkedIn) MaxLength() int { return 3000 }

// Publish creates a post with POST /rest/posts. The new post's URN comes
// back in the x-restli-id header.
func (LinkedIn) Publish(ctx context.Context, cl *c.Client, creds c.Creds, in c.SocialPostInput) (c.SocialResult, error) {
	if c.Token(creds) == "" {
		return c.SocialResult{}, c.Errf(c.ErrAuth, "sign in to LinkedIn first")
	}
	author := in.Account.ExternalID
	if !strings.HasPrefix(author, "urn:li:organization:") && !strings.HasPrefix(author, "urn:li:person:") {
		return c.SocialResult{}, c.Errf(c.ErrInvalid, "the account is not a LinkedIn page or profile")
	}
	if in.Text == "" {
		return c.SocialResult{}, c.Errf(c.ErrInvalid, "the post has no text")
	}
	body, _ := json.Marshal(map[string]any{
		"author":                    author,
		"commentary":                escapeCommentary(in.Text),
		"visibility":                "PUBLIC",
		"distribution":              map[string]any{"feedDistribution": "MAIN_FEED", "targetEntities": []any{}, "thirdPartyDistributionChannels": []any{}},
		"lifecycleState":            "PUBLISHED",
		"isReshareDisabledByAuthor": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.Base(ID, apiBase)+"/rest/posts", bytes.NewReader(body))
	if err != nil {
		return c.SocialResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token(creds))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("LinkedIn-Version", version)
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	resp, err := cl.Do(req)
	if err != nil {
		return c.SocialResult{}, c.Errf(c.ErrUnavailable, "could not reach LinkedIn: %v", err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.SocialResult{}, c.HTTPError("LinkedIn", resp.StatusCode, string(msg))
	}
	id := resp.Header.Get("x-restli-id")
	if id == "" {
		return c.SocialResult{}, c.Errf(c.ErrUnavailable, "LinkedIn did not return the new post's id")
	}
	return c.SocialResult{ExternalID: id, URL: "https://www.linkedin.com/feed/update/" + id + "/"}, nil
}

// escapeCommentary escapes the characters LinkedIn's "little text" format
// reads as markup, so the post shows exactly what was written.
func escapeCommentary(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\|{}@[]()<>#*_~`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
