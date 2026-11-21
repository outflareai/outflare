// Package telegrambot connects the workspace's own Telegram bot, so alerts
// can post to a chat, group or channel it picks. The workspace makes the
// bot with @BotFather and pastes its token; Outflare only posts with it
// and never reads the chats' messages.
//
// Telegram takes the token in the URL's path (/bot<token>/<method>), so
// this package makes its own calls and never lets a URL into an error: a
// transport error would carry it into last_error, the API and the logs.
package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ID is the connector id.
const ID = "telegram_bot"

const api = "https://api.telegram.org"

// All returns the Telegram bot connector.
func All() []c.Connector { return []c.Connector{Bot{}} }

// Bot posts alerts to a Telegram chat.
type Bot struct{}

func (Bot) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: "Telegram bot", Category: c.CategoryNotification,
		Summary:     "Post alerts, such as hot replies and hot website visitors, to a Telegram chat, group or channel.",
		Description: "Posts each alert you choose to a chat of your own Telegram bot, with a link to it in Outflare. Outflare only posts; it never reads the chat's messages.",
		Website:     "https://telegram.org", DocsURL: "https://core.telegram.org/bots/api#sendmessage",
		KeyURL: "https://t.me/BotFather",
		Auth:   c.AuthAPIKey,
		Fields: []c.Field{{Name: "api_key", Label: "Bot token", Secret: true, Placeholder: "123456789:AA…",
			Help: "In Telegram: message @BotFather, send /newbot and copy the token it gives you. Then add the bot to the group or channel the alerts go to (as an admin in a channel), or send the bot a message yourself."}},
		Capabilities: []string{c.CapNotifyTelegram},
		Pricing:      "Free",
	}
}

// tokenPattern is a bot token: the bot's id, a colon and its secret. It
// goes into a URL path, so nothing else is let through.
var tokenPattern = regexp.MustCompile(`^[0-9]{3,20}:[A-Za-z0-9_-]{20,100}$`)

// errWebhook is Telegram refusing getUpdates because the bot has a
// webhook: its updates go there, so the chats can't be listed.
var errWebhook = errors.New("the bot has a webhook")

// call runs one Bot API method and decodes its result into out (nil skips
// it). No error it returns holds the URL.
func call(ctx context.Context, cl *c.Client, creds c.Creds, method string, body, out any) error {
	token := strings.TrimSpace(c.Token(creds))
	if !tokenPattern.MatchString(token) {
		return c.Errf(c.ErrAuth, "That isn't a Telegram bot token. Copy the token @BotFather gave you: digits, a colon and letters.")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return c.Errf(c.ErrInvalid, "Telegram can't take that request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.Base(ID, api)+"/bot"+token+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return c.Errf(c.ErrUnavailable, "could not reach Telegram")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		// The cause without the URL, which holds the token.
		var ue *url.Error
		if errors.As(err, &ue) && ue.Err != nil && !strings.Contains(ue.Err.Error(), token) {
			return c.Errf(c.ErrUnavailable, "could not reach Telegram: %v", ue.Err)
		}
		return c.Errf(c.ErrUnavailable, "could not reach Telegram")
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return c.Errf(c.ErrUnavailable, "reading Telegram's answer failed")
	}
	var a struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(answer, &a); err != nil {
		if resp.StatusCode >= 500 {
			return c.Errf(c.ErrUnavailable, "Telegram is not responding properly (HTTP %d). Try again in a few minutes.", resp.StatusCode)
		}
		return c.Errf(c.ErrUnavailable, "Telegram returned something unexpected")
	}
	if !a.OK {
		code := a.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		why := strings.ReplaceAll(a.Description, token, "(token)")
		if len(why) > 200 {
			why = why[:200]
		}
		switch {
		case code == 401 || code == 404:
			return c.Errf(c.ErrAuth, "Telegram refused the bot token. Copy it again from @BotFather, or make a new one there.")
		case code == 429:
			return c.Errf(c.ErrQuota, "Telegram is rate limiting this bot; it is tried again shortly")
		case code == 409 && method == "getUpdates":
			return errWebhook
		case code == 400 || code == 403:
			return c.Errf(c.ErrInvalid, "Telegram can't post to that chat (%s). Add the bot to the group or channel, or send it a message first.", why)
		case code >= 500:
			return c.Errf(c.ErrUnavailable, "Telegram is not responding properly (HTTP %d). Try again in a few minutes.", code)
		}
		return c.Errf(c.ErrUnavailable, "Telegram said: %s", why)
	}
	if out != nil {
		if err := json.Unmarshal(a.Result, out); err != nil {
			return c.Errf(c.ErrUnavailable, "Telegram returned something unexpected")
		}
	}
	return nil
}

func (Bot) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Name     string `json:"first_name"`
	}
	if err := call(ctx, cl, creds, "getMe", map[string]any{}, &me); err != nil {
		return c.Account{}, err
	}
	name := me.Name
	if me.Username != "" {
		name = "@" + me.Username
	}
	return c.Account{ID: strconv.FormatInt(me.ID, 10), Name: name}, nil
}

type chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

func (ch chat) channel() c.Channel {
	name := ch.Title
	if name == "" {
		name = strings.TrimSpace(ch.FirstName + " " + ch.LastName)
	}
	if name == "" && ch.Username != "" {
		name = "@" + ch.Username
	}
	if name == "" {
		name = strconv.FormatInt(ch.ID, 10)
	}
	return c.Channel{ID: strconv.FormatInt(ch.ID, 10), Name: name, Private: ch.Type == "private"}
}

// Channels lists the chats the bot heard from in the last day: the groups
// and channels it was added to, and the people who wrote to it. Telegram
// has no list of a bot's chats, so this reads its waiting updates, which
// it keeps for 24 hours; reading them takes none away. A bot whose updates
// go to a webhook lists none: its chat is then picked by its id.
func (Bot) Channels(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.Channel, error) {
	var updates []map[string]json.RawMessage
	err := call(ctx, cl, creds, "getUpdates", map[string]any{"limit": 100, "timeout": 0}, &updates)
	if errors.Is(err, errWebhook) {
		return []c.Channel{}, nil
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []c.Channel{}
	for _, u := range updates {
		for key, raw := range u {
			if key == "update_id" {
				continue
			}
			var holder struct {
				Chat *chat `json:"chat"`
			}
			if json.Unmarshal(raw, &holder) != nil || holder.Chat == nil || holder.Chat.ID == 0 {
				continue
			}
			ch := holder.Chat.channel()
			if !seen[ch.ID] {
				seen[ch.ID] = true
				out = append(out, ch)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// chatID is a chat as Telegram takes it: its number, or a public
// channel's @name.
func chatID(id string) (any, error) {
	id = strings.TrimSpace(id)
	if n, err := strconv.ParseInt(id, 10, 64); err == nil && n != 0 {
		return n, nil
	}
	if regexp.MustCompile(`^@[A-Za-z][A-Za-z0-9_]{3,31}$`).MatchString(id) {
		return id, nil
	}
	return nil, c.Errf(c.ErrInvalid, "A Telegram chat is its number (such as -1001234567890) or a public channel's @name.")
}

// Channel finds one chat by its id, for a chat Channels doesn't list.
func (Bot) Channel(ctx context.Context, cl *c.Client, creds c.Creds, id string) (c.Channel, error) {
	target, err := chatID(id)
	if err != nil {
		return c.Channel{}, err
	}
	var ch chat
	if err := call(ctx, cl, creds, "getChat", map[string]any{"chat_id": target}, &ch); err != nil {
		return c.Channel{}, err
	}
	return ch.channel(), nil
}

// Post sends a notice as a short message with a link. The link has no
// preview, so Telegram never fetches the page.
func (Bot) Post(ctx context.Context, cl *c.Client, creds c.Creds, channel string, n c.Notice) error {
	target, err := chatID(channel)
	if err != nil {
		return err
	}
	text := "<b>" + html.EscapeString(n.Title) + "</b>"
	if n.Text != "" {
		text += "\n" + html.EscapeString(n.Text)
	}
	if n.URL != "" {
		button := n.Button
		if button == "" {
			button = "Open in Outflare"
		}
		text += "\n\n<a href=\"" + html.EscapeString(n.URL) + "\">" + html.EscapeString(button) + "</a>"
	}
	return call(ctx, cl, creds, "sendMessage", map[string]any{
		"chat_id": target, "text": text, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true},
	}, nil)
}
