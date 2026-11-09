// Package tgapp is the workspace's own Telegram app: the api_id and
// api_hash it registered at my.telegram.org. Company Telegram accounts in
// the team directory sign in to Outflare through it, as a linked device,
// so Telegram counts every session against the workspace's app and never
// against one of Outflare's.
package tgapp

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ID is the connector id.
const ID = "telegram"

// App is the Telegram app a workspace connected.
type App struct {
	ID   int
	Hash string
	// Proxy is an optional socks5:// or http:// address every connection to
	// Telegram goes through, so each account is seen from one steady place.
	Proxy string
}

// Connector is the Telegram app connector.
type Connector struct{}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (Connector) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: "Telegram", Category: c.CategoryMessaging,
		Website: "https://telegram.org", DocsURL: "https://core.telegram.org/api/obtaining_api_id", KeyURL: "https://my.telegram.org/apps",
		Summary: "Reads the chats of the company's own Telegram accounts into customer records.",
		Description: "Your company's Telegram app, registered by you at my.telegram.org. " +
			"Each company Telegram account in Settings, Team signs in through it once, by scanning a QR code like Telegram Desktop, and Outflare then only reads its chats. " +
			"Nothing is sent from the accounts. Telegram's own terms apply to the app and the accounts.",
		Auth: c.AuthAPIKey, Capabilities: []string{}, Pricing: "Free from Telegram",
		BrandKind: c.KindShared,
		Fields: []c.Field{
			{Name: "api_id", Label: "App api_id", Placeholder: "1234567", Help: "my.telegram.org > API development tools > App api_id."},
			{Name: "api_hash", Label: "App api_hash", Secret: true, Help: "The 32-character App api_hash on the same page."},
			{Name: "proxy", Label: "Proxy", Secret: true, Optional: true, Placeholder: "socks5://user:password@proxy.example:1080",
				Help: "Optional: a steady proxy in the accounts' country, from your own proxy provider, so Telegram always sees them from the same place."},
		},
	}
}

// Parse reads and checks the fields of a connection.
func Parse(creds c.Creds) (App, error) {
	id, err := strconv.Atoi(strings.TrimSpace(creds["api_id"]))
	if err != nil || id <= 0 {
		return App{}, c.Errf(c.ErrInvalid, "api_id is the number my.telegram.org shows as App api_id")
	}
	hash := strings.ToLower(strings.TrimSpace(creds["api_hash"]))
	if !hashPattern.MatchString(hash) {
		return App{}, c.Errf(c.ErrInvalid, "api_hash is the 32 letters and digits my.telegram.org shows as App api_hash")
	}
	app := App{ID: id, Hash: hash, Proxy: strings.TrimSpace(creds["proxy"])}
	if app.Proxy != "" {
		u, err := url.Parse(app.Proxy)
		if err != nil || u.Host == "" || (u.Scheme != "socks5" && u.Scheme != "socks5h" && u.Scheme != "http" && u.Scheme != "https") {
			return App{}, c.Errf(c.ErrInvalid, "the proxy is an address like socks5://user:password@host:port")
		}
	}
	return app, nil
}

// Test checks the fields. Telegram only says whether an app id and hash
// are good when an account signs in with them, which is the first QR scan
// in Settings, Team; nothing is called here.
func (Connector) Test(_ context.Context, _ *c.Client, creds c.Creds) (c.Account, error) {
	app, err := Parse(creds)
	if err != nil {
		return c.Account{}, err
	}
	details := map[string]string{"api_id": strconv.Itoa(app.ID)}
	if app.Proxy != "" {
		details["proxy"] = "on"
	}
	return c.Account{Name: "Telegram app " + strconv.Itoa(app.ID), Details: details}, nil
}
