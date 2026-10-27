package ownkeys

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// The rule itself: nothing in this repository reads a third-party key
// from the environment except Outflare's own operations in allowed.txt.
func TestRepositoryUsesOnlyWorkspaceKeys(t *testing.T) {
	found, err := Scan("../../..", Allowed(), Vendors())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Errorf("%s", f)
	}
	if len(found) > 0 {
		t.Log("Every workspace brings its own keys for every third-party API, with no exceptions (AGENTS.md, \"Bring your own keys\"). " +
			"Take the key from the workspace's own connection. Only Outflare's own operations go in allowed.txt, with a maintainer's approval.")
	}
}

// Every allowlist line still allows something, so the list can't grow
// stale exceptions.
func TestAllowlistHasNoStaleLines(t *testing.T) {
	all, err := collect("../../..", Vendors())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range Allowed() {
		if !slices.ContainsFunc(all, func(f Finding) bool { return allowed(f, []Rule{r}) }) {
			t.Errorf("allowed.txt: %s %s in %v allows nothing any more; remove it", r.Kind, r.Name, r.Paths)
		}
	}
}

func TestScanFindsPlatformKeys(t *testing.T) {
	b, _ := os.ReadFile("testdata/repo/allowed.txt")
	rules, err := ParseRules(b)
	if err != nil {
		t.Fatal(err)
	}
	found, err := Scan("testdata/repo", rules, Vendors())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range found {
		got = append(got, f.Kind+" "+f.Name)
	}
	want := []string{
		"import github.com/openai/openai-go",
		"env ANTHROPIC_API_KEY",
		"env OUTFLARE_GEONODE_PASSWORD",
		"env APOLLO_TOKEN",
		"env SHARED_LLM_KEY",
		"dynamic *API_KEY",
		"env GOOGLE_OAUTH_CLIENT_ID", // allowed only in another file
		"import @anthropic-ai/sdk",
		"env OPENAI_API_KEY",
		"dynamic *",
	}
	if !slices.Equal(got, want) {
		t.Errorf("found\n %s\nwant\n %s", strings.Join(got, "\n "), strings.Join(want, "\n "))
	}
}

func TestVendorsCoverConnectorsAndAI(t *testing.T) {
	v := Vendors()
	for _, want := range []string{"ANTHROPIC", "OPENAI", "GEMINI", "MISTRAL", "GEONODE", "APOLLO", "DATAFORSEO", "AHREFS", "HUBSPOT", "ZEROBOUNCE", "POSTHOG", "GOOGLE", "CHEAPINBOXES"} {
		if !slices.Contains(v, want) {
			t.Errorf("Vendors() is missing %s", want)
		}
	}
	for _, generic := range []string{"SIMPLE", "BUILTIN", "API"} {
		if slices.Contains(v, generic) {
			t.Errorf("Vendors() has the generic word %s", generic)
		}
	}
}

func TestParseRulesNeedsAReason(t *testing.T) {
	for _, line := range []string{"env FOO_KEY engine/x.go", "dynamic engine/x.go why", "secret FOO engine/x.go why"} {
		if _, err := ParseRules([]byte(line)); err == nil {
			t.Errorf("%q parsed", line)
		}
	}
}

// At start-up the engine removes vendor keys from its environment, and
// leaves Outflare's own settings and operations alone.
func TestScrubRemovesPlatformKeys(t *testing.T) {
	remove := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"OUTFLARE_GEONODE_PASSWORD", "GEONODE_USERNAME", "APOLLO_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS",
		// GitHub is a connector, so a GitHub token is a vendor key too.
		"GITHUB_TOKEN"}
	// Outflare's own operations.
	keep := []string{"DATABASE_URL", "OUTFLARE_SECRET_KEY", "OUTFLARE_SECRET_KEY_PREVIOUS", "OUTFLARE_POSTHOG_KEY", "OUTFLARE_POSTHOG_HOST",
		"EMAIL_FROM", "CLOUDFLARE_EMAIL_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID", "POSTMARK_SERVER_TOKEN", "RESEND_API_KEY",
		"GOOGLE_OAUTH_CLIENT_ID", "GOOGLE_OAUTH_CLIENT_SECRET", "MICROSOFT_OAUTH_CLIENT_ID", "MICROSOFT_OAUTH_CLIENT_SECRET",
		"CONNECTOR_HUBSPOT_CLIENT_ID", "CONNECTOR_HUBSPOT_CLIENT_SECRET", "CONNECTOR_AHREFS_SCOPES",
		// An extension's own staff tools, named after the vendor they read.
		"EXT_BILLING_ANTHROPIC_ADMIN_KEY", "EXT_BILLING_OPENAI_ADMIN_KEY", "EXT_BILLING_POSTHOG_KEY", "EXT_BILLING_CLOUDFLARE_TOKEN",
		"EXT_BILLING_GITHUB_TOKEN", "EXT_GITHUB_TOKEN", "EXT_TURNSTILE_SITE_KEY", "EXT_TURNSTILE_SECRET_KEY",
		"EXT_ISSUES_GITHUB_TOKEN", "POSTHOG_PERSONAL_API_KEY", "ANTHROPIC_BASE_URL"}
	for _, k := range append(remove, keep...) {
		t.Setenv(k, "test-not-a-real-key")
	}
	var got []string
	for _, k := range Scrub() {
		if slices.Contains(remove, k) || slices.Contains(keep, k) {
			got = append(got, k)
		}
	}
	slices.Sort(remove)
	if !slices.Equal(got, remove) {
		t.Errorf("removed %v, want %v", got, remove)
	}
	for _, k := range remove {
		if _, ok := os.LookupEnv(k); ok {
			t.Errorf("%s is still set", k)
		}
	}
	for _, k := range keep {
		if _, ok := os.LookupEnv(k); !ok {
			t.Errorf("%s was removed", k)
		}
	}
}
