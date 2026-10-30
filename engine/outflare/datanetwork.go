package outflare

import "github.com/outflareai/outflare/engine/internal/datanetwork"

// The data network (plans/data-network.md): workspaces that join share
// whether the addresses they email deliver or bounce, and their address
// shapes; in return the email finder checks and finds addresses at
// domains they have never emailed. The engine holds membership, decides
// what is shared and asks during finding; the network itself is an
// extension's, registered with SetDataNetwork.
type (
	DataNetwork         = datanetwork.Network
	NetworkContribution = datanetwork.Contribution
	NetworkQuery        = datanetwork.Query
	NetworkAnswer       = datanetwork.Answer
	NetworkSignal       = datanetwork.Signal
	NetworkAllowance    = datanetwork.Allowance
)

// Outcomes in a NetworkContribution or NetworkSignal.
const (
	NetworkDelivered = datanetwork.Delivered
	NetworkBounced   = datanetwork.Bounced
)

// Errors a DataNetwork's Lookup returns to refuse a lookup.
var (
	ErrNetworkAllowance = datanetwork.ErrAllowance
	ErrNetworkLimit     = datanetwork.ErrLimit
)

// NetworkWithdrawWithin is how long after a workspace leaves its
// contributions must be gone: Withdraw is called well before.
const NetworkWithdrawWithin = datanetwork.WithdrawWithin

// SetDataNetwork registers the data network. Call it in Setup. Without
// one, nothing is shared or looked up, and joining answers 409. It is
// internal for now: Enabled turns it on per workspace, and every other
// workspace sees no network at all.
func (h *Host) SetDataNetwork(n DataNetwork) { h.dataNetwork.Set(n) }
