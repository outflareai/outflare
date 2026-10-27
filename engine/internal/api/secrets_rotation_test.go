package api_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/outflareai/outflare/engine/internal/secrets"
)

// Rotating OUTFLARE_SECRET_KEY re-encrypts what is stored, so the old key
// can be dropped without losing anything.
func TestSecretKeyRotation(t *testing.T) {
	e, owner, ws, _ := webhookEnv(t)
	rc := newReceiver(t)
	hook := owner.must(201, "POST", "/v1/workspaces/"+ws+"/webhooks", map[string]any{"url": rc.URL + "/hook", "events": []string{"lead.*"}})
	id, secret := hook["id"].(string), hook["secret"].(string)

	oldKey := base64.StdEncoding.EncodeToString(make([]byte, 32)) // the test server's key
	k := make([]byte, 32)
	rand.Read(k) //nolint:errcheck
	newKey := base64.StdEncoding.EncodeToString(k)
	rotating, err := secrets.NewBox(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// A mailbox sending through another platform has no login: its empty
	// credentials are nothing to reseal, not unreadable.
	if _, err := e.pool.Exec(ctx, `INSERT INTO mailboxes (workspace_id, email, display_name, provider, smtp_host, smtp_port, smtp_username, imap_host, imap_port,
		imap_username, credentials, status, daily_limit, source, has_login)
		VALUES ($1, 'elsewhere@example.com', '', 'other', '', 0, '', '', 0, '', '\x', 'active', 10, 'emailbison', false)`, ws); err != nil {
		t.Fatal(err)
	}
	res, err := rotating.ResealStored(ctx, e.pool)
	if err != nil || res.Resealed < 1 || res.Unreadable != 0 {
		t.Fatalf("reseal: %+v %v", res, err)
	}
	if again, _ := rotating.ResealStored(ctx, e.pool); again.Resealed != 0 || again.Current != res.Resealed+res.Current {
		t.Fatalf("second pass: %+v", again)
	}
	onlyNew, _ := secrets.NewBox(newKey)
	var sealed []byte
	if err := e.pool.QueryRow(ctx, `SELECT secret FROM webhook_endpoints WHERE id = $1`, id).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if pt, err := onlyNew.Open(sealed, []byte(id)); err != nil || string(pt) != secret {
		t.Fatalf("after rotation: %v", err)
	}
	// A key nobody listed leaves the values unreadable, and says so.
	if res, _ := onlyNew.ResealStored(ctx, e.pool); res.Unreadable != 0 {
		t.Fatalf("new key alone: %+v", res)
	}
	stranger, _ := secrets.NewBox(oldKey)
	if res, _ := stranger.ResealStored(ctx, e.pool); res.Unreadable == 0 {
		t.Fatalf("wrong key: %+v", res)
	}
}
