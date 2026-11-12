package auth

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The web app's permissions screen (web/src/lib/permissions.ts) is where a
// person gives an AI app or an API token its scopes. A scope missing there can
// never be given, so the app's tools for it fail with forbidden. Every scope
// must be in an area or an extra, or listed as one only a person may have.
func TestEveryScopeIsOnThePermissionsScreen(t *testing.T) {
	src, err := os.ReadFile("../../../web/src/lib/permissions.ts")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([a-z_]+(?:\.[a-z_]+)+)"`).FindAllStringSubmatch(string(src), -1) {
		listed[m[1]] = true
	}
	var missing []string
	for _, s := range AllScopes {
		if !listed[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("add these scopes to an area or extras in web/src/lib/permissions.ts (or personOnly, if no app or token may have them): %s", strings.Join(missing, ", "))
	}
}
