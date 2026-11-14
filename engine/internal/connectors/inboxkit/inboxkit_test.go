package inboxkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Fixtures follow the published account/workspace, mailbox list/detail and
// credentials envelopes. No live provider key or mailbox is used.
func fixture(t *testing.T, handler http.HandlerFunc) (*c.Client, c.Creds) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer oft_test_not_a_real_token" || r.Header.Get("X-Workspace-Id") != "workspace-test" {
			t.Error("provider key and workspace were not scoped to this connection")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{ID: srv.URL}}, c.Creds{"api_key": "oft_test_not_a_real_token", "workspace_id": "workspace-test"}
}

func TestAccountChecksWorkspaceMembership(t *testing.T) {
	cl, cr := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/api/account":
			io.WriteString(w, `{"uid":"team-test","team":"Acme","admin_email":"admin@acme.example"}`)
		case "/v1/api/workspaces/list":
			io.WriteString(w, `{"error":false,"workspaces":[{"uid":"workspace-test","name":"Sales"}]}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	})
	acct, err := (Connector{}).Test(context.Background(), cl, cr)
	if err != nil || acct.ID != "team-test" || acct.Details["workspace_name"] != "Sales" {
		t.Fatalf("account = %+v, %v", acct, err)
	}
	// A different workspace must not be accepted by an account-only test.
	cl2, cr2 := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/api/account" {
			io.WriteString(w, `{"uid":"team-test","team":"Acme"}`)
		} else {
			io.WriteString(w, `{"error":false,"workspaces":[{"uid":"another-workspace","name":"Other"}]}`)
		}
	})
	if _, err := (Connector{}).Test(context.Background(), cl2, cr2); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("wrong workspace accepted: %v", err)
	}
}

func TestInventoryPaginationAndLifecycle(t *testing.T) {
	var pages []int
	cl, cr := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/api/mailboxes/list" {
			t.Fatalf("unexpected inventory request %s %s", r.Method, r.URL.Path)
		}
		var body struct{ Page, Limit int }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		pages = append(pages, body.Page)
		if body.Limit != 100 {
			t.Errorf("limit not bounded: %d", body.Limit)
		}
		switch body.Page {
		case 1:
			// Provider caps effective pages at one, despite requesting 100.
			io.WriteString(w, `{"error":false,"mailboxes":[{"uid":"mb-1","username":"Ana","domain_name":"Acme.example","platform":"GOOGLE","status":"active","createdAt":"2026-10-01T10:00:00Z"}],"total":2,"pages":2,"current_page":1,"limit":1}`)
		case 2:
			io.WriteString(w, `{"error":false,"mailboxes":[{"uid":"mb-2","username":"lee","domain_name":"acme.example","platform":"MICROSOFT","status":"scheduled_for_cancellation"}],"total":2,"pages":2,"current_page":2,"limit":1}`)
		default:
			t.Fatalf("read beyond final page: %d", body.Page)
		}
	})
	ci := Connector{}
	a, cursor, err := ci.ListMailboxes(context.Background(), cl, cr, c.VendorPage{Limit: 1000})
	if err != nil || cursor != "2" || len(a) != 1 || a[0].Email != "ana@acme.example" || a[0].Provider != "google" || a[0].CreatedAt == nil || a[0].ActivatedAt != nil {
		t.Fatalf("first page: %+v %q %v", a, cursor, err)
	}
	b, end, err := ci.ListMailboxes(context.Background(), cl, cr, c.VendorPage{Cursor: cursor})
	if err != nil || end != "" || len(b) != 1 || b[0].Status != c.VendorCancelling || b[0].Provider != "microsoft" {
		t.Fatalf("last page: %+v %q %v", b, end, err)
	}
	if _, _, err := ci.ListMailboxes(context.Background(), cl, cr, c.VendorPage{Cursor: "not-a-page"}); c.KindOf(err) != c.ErrInvalid || len(pages) != 2 {
		t.Fatalf("invalid cursor sent: %v, %v", pages, err)
	}
}

func TestCredentialsWaitForGoogleAppPassword(t *testing.T) {
	appPassword := ""
	status := "active"
	credentialReads := 0
	cl, cr := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("uid") != "mb-1" {
			t.Error("wrong mailbox requested")
		}
		switch r.URL.Path {
		case "/v1/api/mailboxes/details":
			json.NewEncoder(w).Encode(map[string]any{"error": false, "mailbox": map[string]any{
				"uid": "mb-1", "username": "ana", "domain": "acme.example", "platform": "GOOGLE", "status": status,
			}})
		case "/v1/api/mailboxes/show-credentials":
			credentialReads++
			json.NewEncoder(w).Encode(map[string]any{"error": false, "password": "test-login-password", "app_password": appPassword, "secret": "must-not-be-imported"})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	})
	ci := Connector{}
	if _, err := ci.MailboxCredentials(context.Background(), cl, cr, "mb-1"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("Google login password accepted without app password: %v", err)
	}
	appPassword = "test-app-password"
	k, err := ci.MailboxCredentials(context.Background(), cl, cr, "mb-1")
	if err != nil || k.Email != "ana@acme.example" || k.Username != k.Email || k.AppPassword != appPassword {
		t.Fatalf("credentials: %+v %v", k, err)
	}
	status = "configuring_auth"
	if _, err := ci.MailboxCredentials(context.Background(), cl, cr, "mb-1"); c.KindOf(err) != c.ErrInvalid || credentialReads != 2 {
		t.Fatalf("unfinished mailbox credentials read: %d %v", credentialReads, err)
	}
}

func TestProviderFailureIsRedactedAndNotRetried(t *testing.T) {
	for _, status := range []int{200, 401, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			cl, cr := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				io.WriteString(w, `{"error":true,"message":"secret-value-must-not-leak"}`)
			})
			_, _, err := (Connector{}).ListMailboxes(context.Background(), cl, cr, c.VendorPage{})
			if err == nil || strings.Contains(err.Error(), "secret-value") || calls != 1 {
				t.Fatalf("failure: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestIncompleteInventoryCannotLookLikeDeletion(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"mailboxes":null}`, `{"mailboxes":[],"pages":2,"current_page":1}`,
		`{"mailboxes":[{"uid":"mb-1","username":"ana"}]}`,
		`{"mailboxes":[],"pages":2,"current_page":2}`,
		`{"mailboxes":[],"total":-1}`, `{"mailboxes":[],"pages":-1}`,
	} {
		cl, cr := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		if _, _, err := (Connector{}).ListMailboxes(context.Background(), cl, cr, c.VendorPage{}); err == nil {
			t.Errorf("accepted incomplete inventory: %s", body)
		}
	}
}
