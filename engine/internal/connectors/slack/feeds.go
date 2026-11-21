package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Bot scopes to list and read the channels a workspace picks. Search needs
// a person's token with the user scope search:read. Posting an alert for
// each new conversation is optional and needs alertScopes too.
var feedScopes = []string{"channels:read", "groups:read", "channels:history", "groups:history"}

// AlertScopes are the bot scopes for posting Sales alerts and their emojis.
var AlertScopes = []string{"chat:write", "reactions:write"}

// Feeds reads the Slack channels that other tools post into (a website form,
// a usage alert), so each post can become a person and a conversation. It is
// separate from the Slack connector that posts alerts and from the knowledge
// connector, and connects with keys only.
type Feeds struct{}

func (Feeds) Definition() c.Definition {
	return c.Definition{
		ID: "slack_feeds", Name: "Slack (sales channels)", Category: c.CategoryKnowledge,
		Summary: "Turn posts in the Slack channels you choose, such as a website form feed, into people and Sales conversations.",
		Description: "Reads only the channels you pick, and only to find new posts and their thread replies. It never reads other channels. " +
			"It posts only if you turn on Sales alerts: one message per new conversation in the one channel you choose, kept current with emojis; that needs the bot scopes " + strings.Join(AlertScopes, " and ") + ". " +
			"An optional search token lets Outflare search Slack when you open a conversation.",
		Website: "https://slack.com", DocsURL: "https://api.slack.com/methods/conversations.history",
		KeyURL: "https://api.slack.com/apps",
		Auth:   c.AuthAPIKey, AuthMethods: []string{c.AuthAPIKey},
		Fields: []c.Field{
			{Name: "api_key", Label: "Bot token", Secret: true, Placeholder: "xoxb-…",
				Help: "In Slack: create an app at api.slack.com/apps, add the bot scopes " + strings.Join(feedScopes, ", ") + ", install it to your workspace and copy the Bot User OAuth Token."},
			{Name: "search_token", Label: "Search token", Secret: true, Optional: true, Placeholder: "xoxp-…",
				Help: "Optional. Add the user scope search:read to the same app and copy the User OAuth Token. Search shows what that person can see in Slack."},
		},
		Capabilities: []string{c.CapReadFeeds, c.CapSearchMessages, c.CapPostFeedAlerts},
		Pricing:      "Works on every Slack plan, including free. On free plans Slack only keeps 90 days of messages.",
	}
}

func (Feeds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	return Slack{}.Test(ctx, cl, creds)
}

func (Feeds) Channels(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.Channel, error) {
	return Slack{}.Channels(ctx, cl, creds)
}

func (Feeds) BaseURL(ctx context.Context, cl *c.Client, creds c.Creds) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := call(ctx, cl, creds, http.MethodPost, "/auth.test", nil, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// PostAlert posts text as mrkdwn. Links don't unfurl, so Slack never fetches
// the pages it names.
func (Feeds) PostAlert(ctx context.Context, cl *c.Client, creds c.Creds, channel, text string) (string, error) {
	var out struct {
		TS string `json:"ts"`
	}
	err := call(ctx, cl, creds, http.MethodPost, "/chat.postMessage", map[string]any{
		"channel": channel, "text": text, "mrkdwn": true, "unfurl_links": false, "unfurl_media": false,
	}, &out)
	if err == nil && out.TS == "" {
		err = c.Errf(c.ErrUnavailable, "Slack returned no message id")
	}
	return out.TS, err
}

func (Feeds) UpdateAlert(ctx context.Context, cl *c.Client, creds c.Creds, channel, id, text string) error {
	return call(ctx, cl, creds, http.MethodPost, "/chat.update", map[string]any{"channel": channel, "ts": id, "text": text}, nil)
}

// React adds or removes one emoji. Slack answers already_reacted or
// no_reaction when there is nothing to do, which is fine.
func (Feeds) React(ctx context.Context, cl *c.Client, creds c.Creds, channel, id, emoji string, on bool) error {
	path, done := "/reactions.add", "already_reacted"
	if !on {
		path, done = "/reactions.remove", "no_reaction"
	}
	err := call(ctx, cl, creds, http.MethodPost, path, map[string]any{"channel": channel, "timestamp": id, "name": emoji}, nil)
	if err != nil && strings.HasSuffix(err.Error(), "Slack said "+done) {
		return nil
	}
	return err
}

// Escape keeps names from being read as Slack formatting or mentions.
func Escape(s string) string { return escape(s) }

// Link is the address of a message in the Slack workspace at base.
func Link(base, channel, ts string) string {
	return base + "archives/" + channel + "/p" + strings.Replace(ts, ".", "", 1)
}

type feedMessage struct {
	TS         string `json:"ts"`
	User       string `json:"user"`
	BotID      string `json:"bot_id"`
	BotProfile *struct {
		Name string `json:"name"`
	} `json:"bot_profile"`
	Subtype     string `json:"subtype"`
	Text        string `json:"text"`
	ReplyCount  int    `json:"reply_count"`
	LatestReply string `json:"latest_reply"`
	Edited      *struct {
		TS string `json:"ts"`
	} `json:"edited"`
	Attachments []struct {
		Pretext string `json:"pretext"`
		Title   string `json:"title"`
		Text    string `json:"text"`
		Fields  []struct {
			Title string `json:"title"`
			Value string `json:"value"`
		} `json:"fields"`
	} `json:"attachments"`
	Blocks []json.RawMessage `json:"blocks"`
}

func (m feedMessage) feed() c.FeedMessage {
	author := m.User
	if author == "" {
		author = m.BotID
	}
	f := c.FeedMessage{
		ID: m.TS, Author: author, Bot: m.BotID != "" || m.Subtype == "bot_message",
		Text: m.flatten(), At: fromTS(m.TS), ReplyCount: m.ReplyCount, LatestReply: m.LatestReply,
	}
	if m.Edited != nil && m.Edited.TS != "" {
		t := fromTS(m.Edited.TS)
		f.EditedAt = &t
	}
	// The app's own name, as its workspace installed it. A bot's "username"
	// is left out: a tool can set it per message, to a person's name.
	if f.Bot && m.BotProfile != nil {
		f.Source = sourceName(m.BotProfile.Name)
	}
	return f
}

// sourceName is an app's name on one line, at most 60 characters.
func sourceName(s string) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " ")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return s
}

var boldLine = regexp.MustCompile(`^\*([^*]{1,80})\*$`)

// labelLines joins a form's question and answer. Form tools post each answer
// as a bold line with the answer under it; "*Work email*" then
// "ana@acme.example" becomes "Work email: ana@acme.example", the shape every
// other "Label: value" line has. A bold line followed by another bold line,
// or by nothing, stays as it is.
func labelLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		m := boldLine.FindStringSubmatch(l)
		if m == nil || i+1 >= len(lines) {
			out = append(out, lines[i])
			continue
		}
		next := strings.TrimSpace(lines[i+1])
		if next == "" || boldLine.MatchString(next) {
			out = append(out, lines[i])
			continue
		}
		out = append(out, strings.TrimRight(strings.TrimSpace(m[1]), ":")+": "+next)
		i++
	}
	return strings.Join(out, "\n")
}

var (
	slackLink = regexp.MustCompile(`<((?:https?:|mailto:)[^<>|]+)(?:\|[^<>]*)?>`)
	slackBold = regexp.MustCompile(`\*([^*\n]+)\*`)
)

// plain turns Slack's markup into text: links become their address, bold
// marks go, and the three escaped characters come back.
func plain(s string, mrkdwn bool) string {
	s = slackLink.ReplaceAllStringFunc(s, func(l string) string {
		return strings.TrimPrefix(slackLink.FindStringSubmatch(l)[1], "mailto:")
	})
	if mrkdwn {
		s = slackBold.ReplaceAllString(s, "$1")
	}
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(s)
}

// flatten is the message's text, then its attachments, then its blocks, one
// line each, with blank and repeated lines dropped (Slack often repeats a
// message's blocks in its text).
func (m feedMessage) flatten() string {
	var lines []string
	add := func(s string, mrkdwn bool) {
		for _, l := range strings.Split(plain(labelLines(s), mrkdwn), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
	}
	add(m.Text, false)
	for _, a := range m.Attachments {
		add(a.Pretext, true)
		add(a.Title, false)
		for _, f := range a.Fields {
			if f.Title != "" && f.Value != "" {
				add(f.Title+": "+f.Value, false)
			} else {
				add(f.Title+f.Value, false)
			}
		}
		add(a.Text, true)
	}
	for _, b := range m.Blocks {
		var v any
		if json.Unmarshal(b, &v) == nil {
			blockLines(v, add)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, l := range lines {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// blockLines walks a Block Kit block: text objects and fields give a line
// each; a rich text section's inline elements (text, links, emoji) join into
// one line.
func blockLines(v any, add func(string, bool)) {
	o, ok := v.(map[string]any)
	if !ok {
		return
	}
	str := func(k string) string { s, _ := o[k].(string); return s }
	switch str("type") {
	case "mrkdwn", "plain_text":
		add(str("text"), str("type") == "mrkdwn")
		return
	case "rich_text_section", "rich_text_preformatted", "rich_text_quote":
		var sb strings.Builder
		els, _ := o["elements"].([]any)
		for _, e := range els {
			if em, ok := e.(map[string]any); ok {
				switch em["type"] {
				case "text":
					s, _ := em["text"].(string)
					sb.WriteString(s)
				case "link":
					u, _ := em["url"].(string)
					sb.WriteString(strings.TrimPrefix(u, "mailto:"))
				}
			}
		}
		add(sb.String(), false)
		return
	}
	for _, k := range []string{"text"} {
		if t, ok := o[k].(map[string]any); ok {
			blockLines(t, add)
		}
	}
	for _, k := range []string{"fields", "elements"} {
		if arr, ok := o[k].([]any); ok {
			for _, e := range arr {
				blockLines(e, add)
			}
		}
	}
}

// History returns the channel's top-level messages after oldest, oldest
// first, with channel joins and the like dropped; bots' posts are kept. A
// thread reply also sent to the channel (thread_broadcast) is dropped too:
// it is a reply, which Thread returns under its parent.
func (Feeds) History(ctx context.Context, cl *c.Client, creds c.Creds, channel, oldest, latest string) ([]c.FeedMessage, error) {
	if oldest == "" {
		oldest = "0"
	}
	var out []c.FeedMessage
	cursor := ""
	for {
		q := url.Values{"channel": {channel}, "limit": {"200"}, "oldest": {oldest}, "inclusive": {"false"}}
		if latest != "" {
			q.Set("latest", latest)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var res struct {
			Messages []feedMessage `json:"messages"`
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := call(ctx, cl, creds, http.MethodGet, "/conversations.history?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		for _, m := range res.Messages {
			if skipSubtype(m.Subtype) || m.Subtype == "thread_broadcast" {
				continue
			}
			out = append(out, m.feed())
		}
		if cursor = res.Meta.NextCursor; cursor == "" {
			break
		}
	}
	sortFeed(out)
	return out, nil
}

// Latest returns the newest limit top-level messages, newest first, with the
// same messages dropped as History. It asks for limit messages once and, when
// dropping leaves fewer, for one more page.
func (Feeds) Latest(ctx context.Context, cl *c.Client, creds c.Creds, channel string, limit int) ([]c.FeedMessage, error) {
	limit = min(max(limit, 1), 200)
	var out []c.FeedMessage
	cursor := ""
	for page := 0; page < 2 && len(out) < limit; page++ {
		q := url.Values{"channel": {channel}, "limit": {strconv.Itoa(limit)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var res struct {
			Messages []feedMessage `json:"messages"`
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := call(ctx, cl, creds, http.MethodGet, "/conversations.history?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		for _, m := range res.Messages {
			if !skipSubtype(m.Subtype) && m.Subtype != "thread_broadcast" {
				out = append(out, m.feed())
			}
		}
		if cursor = res.Meta.NextCursor; cursor == "" {
			break
		}
	}
	sortFeed(out)
	slices.Reverse(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Thread returns every reply under ts, oldest first, without the parent.
func (Feeds) Thread(ctx context.Context, cl *c.Client, creds c.Creds, channel, ts string) ([]c.FeedMessage, error) {
	var out []c.FeedMessage
	cursor := ""
	for {
		q := url.Values{"channel": {channel}, "ts": {ts}, "limit": {"200"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var res struct {
			Messages []feedMessage `json:"messages"`
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := call(ctx, cl, creds, http.MethodGet, "/conversations.replies?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		for _, m := range res.Messages {
			if m.TS == ts || skipSubtype(m.Subtype) {
				continue
			}
			out = append(out, m.feed())
		}
		if cursor = res.Meta.NextCursor; cursor == "" {
			break
		}
	}
	sortFeed(out)
	return out, nil
}

func skipSubtype(s string) bool {
	switch s {
	case "channel_join", "channel_leave", "channel_topic", "channel_purpose", "channel_name", "channel_archive", "channel_unarchive", "group_join", "group_leave":
		return true
	}
	return false
}

func sortFeed(m []c.FeedMessage) {
	sort.SliceStable(m, func(i, j int) bool { return m[i].At.Before(m[j].At) })
}

// CanSearch says whether the connection has a search token.
func (Feeds) CanSearch(creds c.Creds) bool { return creds["search_token"] != "" }

// Search runs Slack's message search with the connection's search token.
func (Feeds) Search(ctx context.Context, cl *c.Client, creds c.Creds, query string, limit int) ([]c.SearchHit, error) {
	tok := creds["search_token"]
	if tok == "" {
		return nil, c.Errf(c.ErrInvalid, "Add a search token to the Slack connection to search Slack.")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100 // Slack's most per page
	}
	q := url.Values{"query": {query}, "count": {strconv.Itoa(limit)}, "sort": {"timestamp"}, "sort_dir": {"desc"}}
	var res struct {
		Messages struct {
			Matches []struct {
				Channel *struct {
					ID                 string `json:"id"`
					Name               string `json:"name"`
					IsChannel          bool   `json:"is_channel"`
					IsPrivate          bool   `json:"is_private"`
					IsIM               bool   `json:"is_im"`
					IsMPIM             bool   `json:"is_mpim"`
					IsGroup            bool   `json:"is_group"`
					IsExtShared        bool   `json:"is_ext_shared"`
					IsOrgShared        bool   `json:"is_org_shared"`
					IsShared           bool   `json:"is_shared"`
					IsPendingExtShared bool   `json:"is_pending_ext_shared"`
				} `json:"channel"`
				User      string `json:"user"`
				Username  string `json:"username"`
				Text      string `json:"text"`
				TS        string `json:"ts"`
				Permalink string `json:"permalink"`
			} `json:"matches"`
		} `json:"messages"`
	}
	// The search token replaces the bot token for this call only.
	if err := call(ctx, cl, c.Creds{"api_key": tok}, http.MethodGet, "/search.messages?"+q.Encode(), nil, &res); err != nil {
		return nil, err
	}
	var out []c.SearchHit
	for _, m := range res.Messages.Matches {
		author := m.User
		if author == "" {
			author = m.Username
		}
		ch := m.Channel
		if ch == nil {
			// No channel object: nothing says where it was posted, so it is neither public nor private.
			out = append(out, c.SearchHit{Channel: c.Channel{Private: true}, Author: author, Text: plain(m.Text, false), At: fromTS(m.TS), Link: m.Permalink})
			continue
		}
		private := ch.IsPrivate || ch.IsIM || ch.IsMPIM || ch.IsGroup
		shared := ch.IsExtShared || ch.IsOrgShared || ch.IsShared || ch.IsPendingExtShared
		out = append(out, c.SearchHit{
			Channel: c.Channel{ID: ch.ID, Name: ch.Name, Private: private},
			Public:  ch.ID != "" && ch.IsChannel && !private && !shared,
			Author:  author, Text: plain(m.Text, false), At: fromTS(m.TS), Link: m.Permalink,
		})
	}
	return out, nil
}
