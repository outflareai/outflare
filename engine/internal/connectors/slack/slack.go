// Package slack connects the workspace's own Slack, so alerts can post to
// a channel it picks. It signs in through Slack's consent screen (needs a
// Slack app configured on the server) or with a bot token from the
// workspace's own Slack app.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const api = "https://slack.com/api"

// Scopes to post to channels, including public ones the app hasn't
// joined, and to list them.
var scopes = []string{"chat:write", "chat:write.public", "channels:read", "groups:read"}

// All returns Slack.
func All() []c.Connector { return []c.Connector{Slack{}} }

// Slack posts alerts to a channel.
type Slack struct{}

func (Slack) Definition() c.Definition {
	return c.Definition{
		ID: "slack", Name: "Slack", Category: c.CategoryNotification,
		Summary:     "Post alerts, such as hot replies and hot website visitors, to a Slack channel.",
		Description: "Posts each alert you choose to a channel in your own Slack workspace, with a link to it in Outflare. Outflare only posts; it never reads your messages.",
		Website:     "https://slack.com", DocsURL: "https://api.slack.com/methods/chat.postMessage",
		KeyURL: "https://api.slack.com/apps",
		Auth:   c.AuthOAuth2, AuthMethods: c.Both,
		OAuth: &c.OAuth{
			AuthURL: "https://slack.com/oauth/v2/authorize", TokenURL: api + "/oauth.v2.access",
			Scopes: scopes,
		},
		Fields: []c.Field{{Name: "api_key", Label: "Bot token", Secret: true, Placeholder: "xoxb-…",
			Help: "In Slack: create an app at api.slack.com/apps, add the bot scopes " + strings.Join(scopes, ", ") + ", install it to your workspace and copy the Bot User OAuth Token."}},
		Capabilities: []string{c.CapNotify},
		Pricing:      "Works on every Slack plan, including free",
	}
}

// answer is the part of every Slack Web API answer that says whether it
// worked: Slack answers 200 with ok false when it didn't.
type answer struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func call(ctx context.Context, cl *c.Client, creds c.Creds, method, path string, body, out any) error {
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req, _ = http.NewRequestWithContext(ctx, method, cl.Base("slack", api)+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	} else {
		req, _ = http.NewRequestWithContext(ctx, method, cl.Base("slack", api)+path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token(creds))
	var raw json.RawMessage
	if _, err := c.DoJSON(ctx, cl, "Slack", req, &raw); err != nil {
		return err
	}
	var a answer
	if err := json.Unmarshal(raw, &a); err != nil {
		return c.Errf(c.ErrUnavailable, "Slack returned something unexpected")
	}
	if !a.OK {
		switch a.Error {
		case "invalid_auth", "not_authed", "account_inactive", "token_revoked", "token_expired", "missing_scope":
			return c.Errf(c.ErrAuth, "Slack refused the token (%s). Connect Slack again.", a.Error)
		case "ratelimited":
			return c.Errf(c.ErrQuota, "Slack is rate limiting this app; it is tried again shortly")
		case "channel_not_found", "not_in_channel", "is_archived":
			return c.Errf(c.ErrInvalid, "Slack can't post to that channel (%s). Pick another, or invite the app to it.", a.Error)
		}
		return c.Errf(c.ErrUnavailable, "Slack said %s", a.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (Slack) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var out struct {
		Team   string `json:"team"`
		TeamID string `json:"team_id"`
	}
	if err := call(ctx, cl, creds, http.MethodPost, "/auth.test", nil, &out); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: out.TeamID, Name: out.Team}, nil
}

// Channels lists the channels the app can see, up to 1,000, by name.
func (Slack) Channels(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.Channel, error) {
	var out []c.Channel
	cursor := ""
	for page := 0; page < 5; page++ {
		q := url.Values{"types": {"public_channel,private_channel"}, "exclude_archived": {"true"}, "limit": {"200"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var res struct {
			Channels []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				IsPrivate bool   `json:"is_private"`
			} `json:"channels"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := call(ctx, cl, creds, http.MethodGet, "/conversations.list?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		for _, ch := range res.Channels {
			out = append(out, c.Channel{ID: ch.ID, Name: ch.Name, Private: ch.IsPrivate})
		}
		if cursor = res.Meta.NextCursor; cursor == "" {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Post sends a notice as a short message with a link button. Links don't
// unfurl, so Slack never fetches the page.
func (Slack) Post(ctx context.Context, cl *c.Client, creds c.Creds, channel string, n c.Notice) error {
	text := n.Title
	if n.Text != "" {
		text += "\n" + n.Text
	}
	blocks := []map[string]any{{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "*" + escape(n.Title) + "*\n" + escape(n.Text)}}}
	if n.URL != "" {
		button := n.Button
		if button == "" {
			button = "Open in Outflare"
		}
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []map[string]any{{
			"type": "button", "text": map[string]any{"type": "plain_text", "text": button}, "url": n.URL,
		}}})
	}
	return call(ctx, cl, creds, http.MethodPost, "/chat.postMessage", map[string]any{
		"channel": channel, "text": text, "blocks": blocks, "unfurl_links": false, "unfurl_media": false,
	}, nil)
}

// escape keeps names from being read as Slack formatting or mentions.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
