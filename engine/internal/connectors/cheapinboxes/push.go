package cheapinboxes

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Cheap Inboxes pushes mailboxes to the sequencers it supports. It has no
// Outflare provider, but it takes a base URL for a self-hosted EmailBison,
// and Outflare answers the EmailBison API at /emailbison/{ws}. So the push
// connection is an EmailBison integration pointed at Outflare.

// pushBatch is the most mailboxes Cheap Inboxes links or syncs per call.
const pushBatch = 399

// ConnectPush adds Outflare at Cheap Inboxes as a self-hosted EmailBison.
func (Connector) ConnectPush(ctx context.Context, cl *c.Client, cr c.Creds, t c.PushTarget) (string, error) {
	in := map[string]any{
		"provider":     "emailbison",
		"api_key":      t.APIKey,
		"base_url":     t.BaseURL,
		"display_name": t.Name,
		"sequencer_login": map[string]any{
			"email":    t.LoginEmail,
			"password": t.LoginPassword,
		},
	}
	var out struct {
		Integration struct {
			ID string `json:"id"`
		} `json:"integration"`
	}
	// Once only: a retry after a lost answer would add a second connection.
	if err := send(ctx, cl, cr, http.MethodPost, "/v1/integrations", in, &out, nil, 0); err != nil {
		return "", err
	}
	if out.Integration.ID == "" {
		return "", c.Errf(c.ErrUnavailable, "Cheap Inboxes did not return the new connection's id")
	}
	return out.Integration.ID, nil
}

// PushMailboxes links the mailboxes to the connection, then asks Cheap
// Inboxes to sync them, in batches of 399.
func (Connector) PushMailboxes(ctx context.Context, cl *c.Client, cr c.Creds, remoteID string, ids []string) (c.PushResult, error) {
	var total c.PushResult
	path := "/v1/integrations/" + url.PathEscape(remoteID)
	for start := 0; start < len(ids); start += pushBatch {
		batch := ids[start:min(start+pushBatch, len(ids))]
		body := map[string]any{"mailbox_ids": batch}
		if err := call(ctx, cl, cr, http.MethodPost, path+"/link-mailboxes", body, nil); err != nil {
			return total, err
		}
		var res c.PushResult
		if err := call(ctx, cl, cr, http.MethodPost, path+"/sync", body, &res); err != nil {
			return total, err
		}
		total.Synced += res.Synced
		total.Failed += res.Failed
		total.Skipped += res.Skipped
	}
	return total, nil
}

// DisconnectPush removes the connection. Cheap Inboxes keeps the mailboxes,
// and what it already pushed stays in Outflare.
func (Connector) DisconnectPush(ctx context.Context, cl *c.Client, cr c.Creds, remoteID string) error {
	err := call(ctx, cl, cr, http.MethodDelete, "/v1/integrations/"+url.PathEscape(remoteID), nil, nil)
	if err != nil && strings.HasPrefix(err.Error(), noSuchItem) {
		return nil // already removed there
	}
	return err
}

var _ c.PushVendor = Connector{}
