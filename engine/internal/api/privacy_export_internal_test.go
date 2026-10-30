package api

import "testing"

// A privacy export never carries secrets: columns named like a token,
// secret, password, hash, sealed value or credential are left out, and so
// are binary columns and search vectors, whatever their names.
func TestPrivacyExportHidesSecrets(t *testing.T) {
	for _, c := range []struct {
		name, typ, udt string
		hidden         bool
	}{
		{"token_hash", "bytea", "bytea", true},
		{"token_id", "uuid", "uuid", true},
		{"webhook_secret", "text", "text", true},
		{"password", "text", "text", true},
		{"push_token_sealed", "bytea", "bytea", true},
		{"credentials", "jsonb", "jsonb", true},
		{"api_key", "text", "text", true},
		{"payout_method", "bytea", "bytea", true},
		{"search", "tsvector", "tsvector", true},
		{"embedding", "USER-DEFINED", "vector", true},
		{"subject", "text", "text", false},
		{"attributes", "jsonb", "jsonb", false},
		{"status", "USER-DEFINED", "some_enum", false},
		{"record_id", "uuid", "uuid", false},
	} {
		if got := exportHidden(c.name, c.typ, c.udt); got != c.hidden {
			t.Errorf("%s (%s): hidden %v, want %v", c.name, c.typ, got, c.hidden)
		}
	}
}

// The export reads every table that points at a record, once each, and
// never the exports themselves.
func TestPrivacyExportSources(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range privacyExportSources() {
		if seen[s.table] {
			t.Errorf("%s is read twice", s.table)
		}
		seen[s.table] = true
		if s.table == "privacy_exports" {
			t.Error("an export includes the exports")
		}
	}
	for _, want := range []string{"customer_events", "consents", "emails", "messages", "inbound_messages", "meetings", "deals",
		"payment_subscriptions", "record_identities", "record_relationships", "attribute_changes", "event_inbox"} {
		if !seen[want] {
			t.Errorf("%s is not exported", want)
		}
	}
}
