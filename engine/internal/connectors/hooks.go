package connectors

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
)

// CapHooks marks a connector that can tell Outflare when something happens,
// through a webhook to the connection's own address (implements Hooked).
const CapHooks = "hooks.receive"

// HookSetup says how a connector's webhook reaches Outflare.
type HookSetup struct {
	// Auto: Outflare registers the address through the connector's API
	// (implements HookRegistrar). Otherwise the person pastes it.
	Auto bool `json:"auto"`
	// Signed: the connector signs each delivery with a secret; a paste
	// setup asks for that secret.
	Signed bool `json:"signed"`
	// Events are the event types the connector sends, in its own words.
	Events []string `json:"events"`
	// Steps tell a person where to paste the address, for a paste setup or
	// when the automatic one fails.
	Steps string `json:"steps"`
}

// HookEvent is one event from a webhook delivery.
type HookEvent struct {
	// ID is the connector's own id for the event or delivery, so a repeat
	// is recognised. Never empty.
	ID   string
	Type string
	// Summary holds what the log shows: ids, counts and statuses, never
	// page text, names or email addresses.
	Summary map[string]any
}

// Hooked is a connector that sends webhooks Outflare can read.
type Hooked interface {
	Connector
	HookSetup() HookSetup
	// VerifyHook checks a delivery's signature against the connection's
	// signing secrets (several when the connector issues one per event
	// type). It returns an ErrAuth error when none matches.
	VerifyHook(secrets []string, h http.Header, body []byte) error
	// ParseHook reads a verified delivery.
	ParseHook(h http.Header, body []byte) ([]HookEvent, error)
}

// RemoteHook is a webhook subscription at the connector.
type RemoteHook struct {
	ID     string `json:"id"`
	Secret string `json:"-"`
}

// HookRegistrar can register and remove its webhook through its API.
type HookRegistrar interface {
	Hooked
	// RegisterHooks subscribes url to every event type and returns the
	// subscriptions with their signing secrets.
	RegisterHooks(ctx context.Context, c *Client, creds Creds, url string) ([]RemoteHook, error)
	// DeleteHooks removes subscriptions; one already gone is not an error.
	DeleteHooks(ctx context.Context, c *Client, creds Creds, ids []string) error
}

// VerifyHMAC checks an HMAC-SHA256 signature of body against each secret.
// It takes the signature as hex or base64, with or without a "sha256="
// prefix, and also accepts one made over "<timestamp>.<body>" when the
// delivery carries a timestamp, the common variants vendors use.
func VerifyHMAC(secrets []string, signature, timestamp string, body []byte) error {
	sig := strings.TrimSpace(signature)
	if i := strings.Index(sig, "v1="); i >= 0 && strings.Contains(sig, "t=") {
		// t=<timestamp>,v1=<signature>
		for _, part := range strings.Split(sig, ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			switch k {
			case "t":
				timestamp = v
			case "v1":
				sig = v
			}
		}
	}
	sig = strings.TrimPrefix(sig, "sha256=")
	var given [][]byte
	if b, err := hex.DecodeString(sig); err == nil {
		given = append(given, b)
	}
	if b, err := base64.StdEncoding.DecodeString(sig); err == nil {
		given = append(given, b)
	}
	if len(given) == 0 {
		return Errf(ErrAuth, "the delivery has no signature")
	}
	signed := [][]byte{body}
	if timestamp != "" {
		signed = append(signed, []byte(timestamp+"."+string(body)))
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, msg := range signed {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write(msg)
			want := mac.Sum(nil)
			for _, g := range given {
				if hmac.Equal(g, want) {
					return nil
				}
			}
		}
	}
	return Errf(ErrAuth, "the signature does not match")
}
