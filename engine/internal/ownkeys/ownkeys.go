// Package ownkeys enforces the bring-your-own-keys rule (AGENTS.md): every
// third-party API a workspace uses runs on that workspace's own account.
// Outflare never lends its own keys for connectors, AI or anything else, so
// a workspace feature takes credentials only from the workspace's own
// connections, never from the server's environment.
//
// Three things hold the line:
//
//   - Scan reads the source (Go and TypeScript) and reports every place that
//     reads an environment variable named like a third-party key or after a
//     connector's vendor, reads one by a computed name, or imports an SDK
//     that picks up ambient credentials, unless the allowlist names that
//     file. The engine's test runs it over this repository; Cloud runs
//     cmd/keycheck over its own.
//   - Scrub runs at start-up and removes vendor keys such as
//     ANTHROPIC_API_KEY from the process environment, so no SDK, library or
//     child process can pick one up by default.
//   - The LLM client tests prove a call carries only the workspace's key.
//
// The only exceptions are Outflare's own operations, never a workspace
// feature: account email, OAuth app ids, the operator's own analytics and
// Cloud's staff tools. Each one is a line in the allowlist with its reason.
package ownkeys

import (
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/outflareai/outflare/engine/internal/connectors/catalog"
	"github.com/outflareai/outflare/engine/internal/llm"
)

// extraVendors are third-party APIs that aren't connectors (yet), so their
// keys can't sneak in under a name the catalog doesn't know.
var extraVendors = []string{
	"ANTHROPIC", "GEONODE", "OPENROUTER", "GROQ", "COHERE", "DEEPSEEK", "XAI", "PERPLEXITY",
	"REPLICATE", "HUGGINGFACE", "AWS", "AZURE", "GCP", "SERPAPI", "CLEARBIT", "ZOOMINFO", "LUSHA",
}

// generic words that a connector id can start with but that say nothing
// about a vendor.
var generic = []string{"SIMPLE", "BUILTIN", "CUSTOM", "WWW", "APP", "API", "CAL"}

// codeHosts are where some connectors' websites live, which says nothing
// about the vendor.
var codeHosts = []string{"github", "gitlab", "bitbucket"}

// Vendors returns every third-party vendor name, as environment variable
// segments: each connector's id, the first word of its id, its website's
// name, each AI provider's vendor, and extraVendors. New connectors are
// covered as soon as they join the catalog.
func Vendors() []string {
	set := map[string]bool{}
	add := func(s string) {
		s = strings.Trim(strings.ToUpper(strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(s)), "_")
		if s != "" && !slices.Contains(generic, s) {
			set[s] = true
		}
	}
	for _, d := range catalog.New().All() {
		add(d.ID)
		if first, _, ok := strings.Cut(d.ID, "_"); ok && len(first) >= 4 {
			add(first)
		}
		if u, err := url.Parse(d.Website); err == nil && u.Hostname() != "" {
			labels := strings.Split(strings.TrimPrefix(u.Hostname(), "www."), ".")
			if len(labels) >= 2 && len(labels[len(labels)-2]) >= 4 && !slices.Contains(codeHosts, labels[len(labels)-2]) {
				add(labels[len(labels)-2])
			}
		}
	}
	for _, p := range llm.Providers {
		add(p.ID)
		add(p.Vendor)
	}
	for _, v := range extraVendors {
		add(v)
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// credentialWords are environment variable segments that mean a secret.
var credentialWords = []string{"KEY", "KEYS", "APIKEY", "TOKEN", "TOKENS", "SECRET", "SECRETS", "PASSWORD", "PASSWD", "PASS",
	"PAT", "CREDENTIAL", "CREDENTIALS", "AUTH", "BEARER", "DSN"}

// Vendor returns the vendor an environment variable is named after, if any.
func Vendor(name string, vendors []string) string {
	segs := strings.Split(strings.ToUpper(name), "_")
	for _, v := range vendors {
		vs := strings.Split(v, "_")
		for i := 0; i+len(vs) <= len(segs); i++ {
			if slices.Equal(segs[i:i+len(vs)], vs) {
				return v
			}
		}
	}
	return ""
}

// Credential reports whether an environment variable is named like a secret.
func Credential(name string) bool {
	for _, s := range strings.Split(strings.ToUpper(name), "_") {
		if slices.Contains(credentialWords, s) || strings.HasSuffix(s, "APIKEY") || strings.HasSuffix(s, "TOKEN") || strings.HasSuffix(s, "SECRET") {
			return true
		}
	}
	return false
}

// ThirdParty reports why reading the variable needs an allowlist entry:
// it is named after a vendor, or it is a secret that isn't Outflare's own
// (OUTFLARE_ variables are Outflare's own settings and tokens, unless
// they name a vendor). Empty means it is fine to read anywhere.
func ThirdParty(name string, vendors []string) string {
	if v := Vendor(name, vendors); v != "" {
		return "named after " + v
	}
	if Credential(name) && !strings.HasPrefix(strings.ToUpper(name), "OUTFLARE_") {
		return "named like a third-party key"
	}
	return ""
}

// keySuffixes are how SDKs and habit name a vendor's key.
var keySuffixes = []string{"KEY", "API_KEY", "APIKEY", "TOKEN", "API_TOKEN", "ACCESS_TOKEN", "AUTH_TOKEN",
	"SECRET", "API_SECRET", "SECRET_KEY", "PASSWORD", "USERNAME", "USER"}

// ambient are variables SDKs read for credentials without being asked.
var ambient = []string{"GOOGLE_APPLICATION_CREDENTIALS", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"AZURE_CLIENT_SECRET", "AZURE_OPENAI_API_KEY"}

// PlatformKey reports whether name is a vendor key an operator might set
// for the whole server, such as ANTHROPIC_API_KEY or OUTFLARE_GEONODE_TOKEN,
// which Scrub removes. allowed names (Outflare's own operations) never are.
func PlatformKey(name string, vendors []string, allowed []Rule) bool {
	name = strings.ToUpper(name)
	for _, r := range allowed {
		if r.Matches(name) {
			return false
		}
	}
	if slices.Contains(ambient, name) {
		return true
	}
	name = strings.TrimPrefix(name, "OUTFLARE_")
	for _, v := range vendors {
		if rest, ok := strings.CutPrefix(name, v+"_"); ok && slices.Contains(keySuffixes, rest) {
			return true
		}
	}
	return false
}

// Scrub removes platform vendor keys from the process environment and
// returns their names, sorted. Values are never read beyond the check.
func Scrub() []string {
	vendors, allowed := Vendors(), Allowed()
	var removed []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if PlatformKey(name, vendors, allowed) {
			os.Unsetenv(name)
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	return removed
}
