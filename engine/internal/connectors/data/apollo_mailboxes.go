package data

import (
	"context"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Apollo picks the sending mailbox for each contact when it is added to a
// sequence, so mailbox allocation cannot move mailboxes between Apollo
// sequences. It only watches which mailboxes are connected in Apollo, so
// they can be flagged as also sending from there.
// https://docs.apollo.io/reference/get-a-list-of-email-accounts

type apolloEmailAccount struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Active *bool  `json:"active"`
	// Apollo reports a sending problem here, e.g. a lost connection.
	LastSyncedError string `json:"last_synced_error"`
}

// Observe lists the email accounts connected in Apollo. It reports no
// campaigns: Apollo does not say which mailbox sends for which sequence.
func (a Apollo) Observe(ctx context.Context, cl *c.Client, cr c.Creds) (c.Snapshot, error) {
	var r struct {
		EmailAccounts []apolloEmailAccount `json:"email_accounts"`
	}
	if _, err := call(ctx, cl, "Apollo", a.req(getReq(a.base(cl)+"/api/v1/email_accounts", nil), cr), &r); err != nil {
		return c.Snapshot{}, err
	}
	var snap c.Snapshot
	for _, e := range r.EmailAccounts {
		m := c.MailboxState{ExternalID: e.ID, Email: strings.ToLower(e.Email), Status: c.MailboxOK}
		switch {
		case e.Active != nil && !*e.Active:
			m.Status, m.StatusReason = c.MailboxPaused, "Apollo shows it as inactive"
		case e.LastSyncedError != "":
			m.Status, m.StatusReason = c.MailboxError, "Apollo: "+e.LastSyncedError
		}
		snap.Mailboxes = append(snap.Mailboxes, m)
	}
	return snap, nil
}

var _ c.Observer = Apollo{}
