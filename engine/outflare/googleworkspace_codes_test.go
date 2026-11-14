package outflare

import (
	"fmt"
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Extensions tell a missing sign-in, a missing permission to sign and a
// refused delegation apart by code, while kind-only callers still see auth.
func TestWorkspaceSignerCodes(t *testing.T) {
	for _, code := range []string{CodeWorkspaceSignerNeeded, CodeWorkspaceSignerDenied, CodeWorkspaceDelegationDenied} {
		err := fmt.Errorf("probe: %w", connectors.Codedf(code, connectors.ErrAuth, "message"))
		if got := ErrorCode(err); got != code {
			t.Errorf("ErrorCode = %q, want %q", got, code)
		}
		if ConnectorErrorKind(err) != connectors.ErrAuth || connectors.KindOf(err) != connectors.ErrAuth {
			t.Errorf("%s lost its auth kind", code)
		}
	}
	if got := ErrorCode(connectors.Errf(connectors.ErrAuth, "plain")); got != "connector_auth" {
		t.Errorf("plain connector errors changed code: %q", got)
	}
}
