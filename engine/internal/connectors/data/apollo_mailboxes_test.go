package data

import (
	"context"
	"net/http"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestApolloObserveListsEmailAccounts(t *testing.T) {
	f, cl := newFake(t)
	f.routes["/api/v1/email_accounts"] = func(w http.ResponseWriter, _ map[string]any, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"email_accounts":[{"id":"e1","email":"Ana@Acme.test","active":true},
			{"id":"e2","email":"bo@acme.test","active":false},{"id":"e3","email":"cy@acme.test","last_synced_error":"token revoked"}]}`))
	}
	snap, err := Apollo{}.Observe(context.Background(), cl, c.Creds{"api_key": "k"})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Mailboxes) != 3 || len(snap.Campaigns) != 0 {
		t.Fatalf("snapshot %+v", snap)
	}
	if m := snap.Mailboxes[0]; m.Email != "ana@acme.test" || m.Status != c.MailboxOK {
		t.Fatalf("ana %+v", m)
	}
	if snap.Mailboxes[1].Status != c.MailboxPaused || snap.Mailboxes[2].Status != c.MailboxError {
		t.Fatalf("statuses %+v", snap.Mailboxes)
	}
}
