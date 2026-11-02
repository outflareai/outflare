package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/mailbox/mailtest"
	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/outflareai/outflare/engine/internal/track"
)

// useMailFake points the Gmail and Outlook connectors at a fake.
func (e *env) useMailFake(t *testing.T) *mailFake {
	fake := fakeMailSignIn(t)
	e.integrations.Client.BaseURLs["google_mail.token"] = fake.URL + "/token"
	e.integrations.Client.BaseURLs["google.token"] = fake.URL + "/sa"
	e.integrations.Client.BaseURLs["outlook_mail.login"] = fake.URL + "/ms"
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"google_mail": {ClientID: "gid", ClientSecret: "gsecret"}}
	return fake
}

// legacyAdmin stores an admin connection the way POST /oauth-connections
// did: its secret sealed in oauth_connections, bound to its id.
func legacyAdmin(t *testing.T, e *env, ws, provider, name string, secret map[string]any) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := e.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(secret)
	if _, err := e.pool.Exec(ctx, `INSERT INTO oauth_connections (id, workspace_id, provider, name, details, secret) VALUES ($1, $2, $3, $4, '{}', $5)`,
		id, ws, provider, name, e.box.Seal(b, []byte(id))); err != nil {
		t.Fatal(err)
	}
	return id
}

// legacyMailbox stores an OAuth mailbox the way the old consent screen
// (refresh, a refresh token in its sealed credentials) or an old admin
// connection (conn) did.
func legacyMailbox(t *testing.T, e *env, ws, email, provider, refresh string, conn *string) string {
	t.Helper()
	ctx := context.Background()
	smtp, imap, port := "smtp.gmail.com", "imap.gmail.com", 465
	if provider == "microsoft" {
		smtp, imap, port = "smtp.office365.com", "outlook.office365.com", 587
	}
	var id string
	if err := e.pool.QueryRow(ctx, `INSERT INTO mailboxes (workspace_id, email, display_name, provider, smtp_host, smtp_port, smtp_username,
			imap_host, imap_port, imap_username, credentials, status, auth_method, oauth_connection_id)
		VALUES ($1, $2, 'Old Sign-in', $3, $4, $5, $2, $6, 993, $2, '\x00', 'active', 'oauth', $7) RETURNING id::text`,
		ws, email, provider, smtp, port, imap, conn).Scan(&id); err != nil {
		t.Fatal(err)
	}
	creds := map[string]string{}
	if refresh != "" {
		creds["refresh_token"] = refresh
	}
	b, _ := json.Marshal(creds)
	if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET credentials = $2 WHERE id = $1`, id, e.box.Seal(b, []byte(id))); err != nil {
		t.Fatal(err)
	}
	return id
}

// Mailbox sign-ins from before they were connections move into Gmail and
// Outlook connections at start-up: admin connections become api_key
// connections, a mailbox's own refresh token becomes an oauth2 connection
// (or joins the one its address already has), and the mailboxes keep
// signing in. Running it again changes nothing.
func TestMoveLegacyMailSignIns(t *testing.T) {
	e := freshEnv(t)
	e.useMailFake(t)
	mail := mailtest.Start(t, map[string]string{"li@gw.test": "unused", "bo@gw.test": "unused", "ana@acme.test": "unused", "cy@tenant.test": "unused"})
	e.mail.Dialer.TLSConfig, e.mail.Dialer.Route = mail.ClientTLS, mail.Route
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	sa := legacyAdmin(t, e, ws, "google", "Acme Google", map[string]any{"service_account": json.RawMessage(newServiceAccount(t))})
	app := legacyAdmin(t, e, ws, "microsoft", "Tenant", map[string]any{"tenant_id": "tenant-1", "client_id": "app-1", "client_secret": "app-secret"})
	ana := legacyMailbox(t, e, ws, "ana@acme.test", "google", "", &sa)
	cy := legacyMailbox(t, e, ws, "cy@tenant.test", "microsoft", "", &app)
	li := legacyMailbox(t, e, ws, "Li@GW.test", "google", "rt-li", nil)
	bo := legacyMailbox(t, e, ws, "bo@gw.test", "google", "rt-bo-old", nil)
	broken := legacyMailbox(t, e, ws, "gone@gw.test", "google", "rt-gone", nil)
	if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET credentials = '\x00' WHERE id = $1`, broken); err != nil {
		t.Fatal(err)
	}
	// bo already signed in again through the new flow.
	var boConn string
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_mail", connectors.AuthOAuth2, "Gmail (bo@gw.test)",
			connectors.Creds{"refresh_token": "rt-bo"}, connectors.Account{ID: "bo@gw.test", Email: "bo@gw.test"}, "")
		boConn = i.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	events := track.Capture(t)
	res, err := oauth.MoveLegacy(ctx, e.integrations, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if res != (oauth.MoveResult{Admin: 2, SignIns: 1, Linked: 1, Failed: 1}) {
		t.Fatalf("moved: %+v", res)
	}
	ev := events()
	if c := named(ev, "integration.connected"); len(c) != 3 {
		t.Fatalf("integration.connected: %+v", c)
	}
	if u := named(ev, "mailbox.updated"); len(u) != 2 {
		t.Fatalf("mailbox.updated: %+v", u)
	}
	if f := named(ev, "mailbox.sign_in_move_failed"); len(f) != 1 || f[0].WorkspaceID != ws || f[0].Props["kind"] != "sign_in" {
		t.Fatalf("failed: %+v", f)
	}
	if m := named(ev, "mailbox.sign_ins_moved"); len(m) != 1 || m[0].Props["admin"] != 2 || m[0].Outcome != track.Error {
		t.Fatalf("summary: %+v", m)
	}

	// The connections, listed like any other, and nothing left behind.
	var left int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM oauth_connections WHERE workspace_id = $1`, ws).Scan(&left); err != nil || left != 0 {
		t.Fatalf("oauth_connections left: %d %v", left, err)
	}
	conns := map[string]map[string]any{}
	for _, c := range owner.must(200, "GET", base+"/integrations?category=mailbox_provider", nil)["data"].([]any) {
		c := c.(map[string]any)
		conns[c["account"].(map[string]any)["id"].(string)] = c
	}
	if c := conns["sender@proj.iam.gserviceaccount.com"]; c == nil || c["connector"] != "google_mail" || c["auth_method"] != "api_key" || c["name"] != "Acme Google" {
		t.Fatalf("service account: %v", conns)
	}
	if c := conns["tenant-1"]; c == nil || c["connector"] != "outlook_mail" || c["auth_method"] != "api_key" {
		t.Fatalf("entra app: %v", conns)
	}
	if c := conns["li@gw.test"]; c == nil || c["connector"] != "google_mail" || c["auth_method"] != "oauth2" || c["name"] != "Gmail (li@gw.test)" {
		t.Fatalf("li: %v", conns)
	}
	if len(conns) != 4 {
		t.Fatalf("connections: %v", conns)
	}
	linked := func(id string) string {
		var conn *string
		var old *string
		var sealed []byte
		if err := e.pool.QueryRow(ctx, `SELECT integration_id::text, oauth_connection_id::text, credentials FROM mailboxes WHERE id = $1`, id).Scan(&conn, &old, &sealed); err != nil {
			t.Fatal(err)
		}
		if old != nil {
			t.Fatalf("mailbox %s still has oauth_connection_id", id)
		}
		if plain, err := e.box.Open(sealed, []byte(id)); err == nil && strings.Contains(string(plain), "refresh_token") {
			t.Fatalf("mailbox %s kept its refresh token", id)
		}
		if conn == nil {
			return ""
		}
		return *conn
	}
	if linked(ana) != conns["sender@proj.iam.gserviceaccount.com"]["id"] || linked(cy) != conns["tenant-1"]["id"] ||
		linked(li) != conns["li@gw.test"]["id"] || linked(bo) != boConn {
		t.Fatal("mailboxes not linked to their connections")
	}
	if linked(broken) != "" {
		t.Fatal("unreadable mailbox was moved")
	}

	// They still sign in: the token comes from the connection.
	for id, want := range map[string]string{ana: "sa-ana@acme.test", cy: "app-token", li: "access-rt-li", bo: "access-rt-bo"} {
		acct, err := e.mail.Load(ctx, e.pool, ws, id)
		if err != nil || acct.SMTPCred.Token != want || acct.IMAPCred.Token != want {
			t.Fatalf("%s: %v %v", id, acct, err)
		}
	}
	// The moved connection keeps the refreshed token.
	o, err := e.integrations.Open(ctx, e.pool, ws, conns["li@gw.test"]["id"].(string))
	if err != nil || o.Creds["access_token"] != "access-rt-li" || o.Creds["refresh_token"] != "rt-li" {
		t.Fatalf("li's connection: %v %v", o, err)
	}

	// Running again moves nothing; the unreadable mailbox is tried again.
	again, err := oauth.MoveLegacy(ctx, e.integrations, slog.Default())
	if err != nil || again != (oauth.MoveResult{Failed: 1}) {
		t.Fatalf("second run: %+v %v", again, err)
	}
	if n := len(owner.must(200, "GET", base+"/integrations?category=mailbox_provider", nil)["data"].([]any)); n != 4 {
		t.Fatalf("connections after a second run: %d", n)
	}
}
