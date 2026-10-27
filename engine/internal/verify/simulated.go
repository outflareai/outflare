package verify

import (
	"strings"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// SimulatedProvider answers verification in test workspaces.
const SimulatedProvider = "simulated"

// Simulated is a verification result made up from the address, so a test
// workspace can try every outcome without spending credits. An address is
// deliverable unless its local part names another result:
// undeliverable@, risky@ or unknown@ (or contains it, as in
// ana+undeliverable@).
func Simulated(email string) connectors.Verification {
	email = strings.ToLower(strings.TrimSpace(email))
	local, _, _ := strings.Cut(email, "@")
	v := connectors.Verification{Email: email, Status: connectors.Deliverable, ProviderStatus: "simulated"}
	switch {
	case strings.Contains(local, connectors.Undeliverable):
		v.Status, v.Reason = connectors.Undeliverable, "simulated: the address does not exist"
	case strings.Contains(local, connectors.Risky):
		v.Status, v.Reason, v.CatchAll = connectors.Risky, "simulated: the domain accepts every address", true
	case strings.Contains(local, connectors.Unknown):
		v.Status, v.Reason = connectors.Unknown, "simulated: the server did not answer"
	default:
		v.Reason = "simulated"
	}
	return v
}
