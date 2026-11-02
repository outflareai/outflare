package slack

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Read-only scopes for the knowledge connector: list channels, join public
// ones, and read the history of the channels the workspace picks. It never
// posts.
var readScopes = []string{"channels:read", "groups:read", "channels:history", "groups:history", "channels:join"}

// Knowledge reads the Slack channels a workspace chooses, so the support
// assistant can find answers the team already gave there. It is separate
// from the Slack connector that posts alerts, which never reads.
type Knowledge struct{}

func (Knowledge) Definition() c.Definition {
	return c.Definition{
		ID: "slack_knowledge", Name: "Slack (knowledge)", Category: c.CategoryKnowledge,
		Summary: "Let support answers learn from the Slack channels you choose, such as #support or #product.",
		Description: "Reads only the channels you pick under Support settings, when the support assistant can't answer a customer from your knowledge. " +
			"What it finds is suggested in Knowledge for a person to approve; nothing is used until then. It never posts and never reads other channels.",
		Website: "https://slack.com", DocsURL: "https://api.slack.com/methods/conversations.history",
		KeyURL: "https://api.slack.com/apps",
		Auth:   c.AuthOAuth2, AuthMethods: c.Both,
		OAuth: &c.OAuth{
			AuthURL: "https://slack.com/oauth/v2/authorize", TokenURL: api + "/oauth.v2.access",
			Scopes: readScopes,
		},
		Fields: []c.Field{{Name: "api_key", Label: "Bot token", Secret: true, Placeholder: "xoxb-…",
			Help: "In Slack: create an app at api.slack.com/apps, add the bot scopes " + strings.Join(readScopes, ", ") + ", install it to your workspace and copy the Bot User OAuth Token."}},
		Capabilities: []string{c.CapReadMessages},
		Pricing:      "Works on every Slack plan, including free. On free plans Slack only keeps 90 days of messages.",
	}
}

func (Knowledge) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	return Slack{}.Test(ctx, cl, creds)
}

func (Knowledge) Channels(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.Channel, error) {
	return Slack{}.Channels(ctx, cl, creds)
}

func (Knowledge) Join(ctx context.Context, cl *c.Client, creds c.Creds, channel string) error {
	return call(ctx, cl, creds, http.MethodPost, "/conversations.join", map[string]any{"channel": channel}, nil)
}

type slackMessage struct {
	TS         string `json:"ts"`
	ThreadTS   string `json:"thread_ts"`
	User       string `json:"user"`
	BotID      string `json:"bot_id"`
	Subtype    string `json:"subtype"`
	Text       string `json:"text"`
	ReplyCount int    `json:"reply_count"`
}

func (m slackMessage) message(channel string) c.Message {
	return c.Message{ID: m.TS, Channel: channel, Author: m.User, Bot: m.BotID != "" || m.Subtype == "bot_message", Text: m.Text, At: fromTS(m.TS)}
}

// fromTS reads Slack's "seconds.micros" timestamps.
func fromTS(ts string) time.Time {
	sec, frac, _ := strings.Cut(ts, ".")
	s, _ := strconv.ParseInt(sec, 10, 64)
	us, _ := strconv.ParseInt((frac + "000000")[:6], 10, 64)
	return time.Unix(s, us*1000).UTC()
}

// Threads read per call at most: each is one more request to Slack.
const maxThreads = 25

func (Knowledge) Recent(ctx context.Context, cl *c.Client, creds c.Creds, channel string, since time.Time, limit int) ([]c.Message, error) {
	var out []c.Message
	cursor := ""
	threads := 0
	for page := 0; page < 5 && len(out) < limit; page++ {
		q := url.Values{"channel": {channel}, "limit": {strconv.Itoa(min(200, limit))}, "oldest": {strconv.FormatInt(since.Unix(), 10)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var res struct {
			Messages []slackMessage `json:"messages"`
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := call(ctx, cl, creds, http.MethodGet, "/conversations.history?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		for _, m := range res.Messages {
			if m.Subtype != "" && m.Subtype != "bot_message" && m.Subtype != "thread_broadcast" {
				continue // joins, topic changes and the like
			}
			msg := m.message(channel)
			if m.ReplyCount > 0 && threads < maxThreads {
				threads++
				var rep struct {
					Messages []slackMessage `json:"messages"`
				}
				rq := url.Values{"channel": {channel}, "ts": {m.TS}, "limit": {"50"}}
				if err := call(ctx, cl, creds, http.MethodGet, "/conversations.replies?"+rq.Encode(), nil, &rep); err != nil {
					return nil, err
				}
				for _, r := range rep.Messages {
					if r.TS != m.TS {
						msg.Replies = append(msg.Replies, r.message(channel))
					}
				}
			}
			out = append(out, msg)
			if len(out) >= limit {
				break
			}
		}
		if cursor = res.Meta.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// Permalink is a link to one message in Slack.
func (Knowledge) Permalink(ctx context.Context, cl *c.Client, creds c.Creds, channel, ts string) (string, error) {
	var out struct {
		Permalink string `json:"permalink"`
	}
	q := url.Values{"channel": {channel}, "message_ts": {ts}}
	err := call(ctx, cl, creds, http.MethodGet, "/chat.getPermalink?"+q.Encode(), nil, &out)
	return out.Permalink, err
}
