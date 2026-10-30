// Package datanetwork connects a workspace to a data network an extension
// provides (plans/data-network.md). Workspaces that join share whether the
// addresses they emailed delivered or bounced, and the address shape each
// fits; in return the email finder can check and find addresses at domains
// they have never emailed.
//
// The rule behind it: confirm, never reveal. A lookup only asks about
// addresses the asker brought (or built from a name it brought), and an
// answer never says who else knows them. Names, lists, replies and message
// text never leave the workspace, and neither does anyone who opted out.
//
// The engine holds membership, decides what is shared, and asks the network
// during finding and verifying. The network itself (storage, fingerprints,
// confidence, allowances and limits) is the extension's. Without one
// registered, nothing is shared or asked, and the email finder works as it
// always has.
package datanetwork

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Outcomes a workspace shares for an address it emailed.
const (
	Delivered = "delivered"
	Bounced   = "bounced"
)

// Contribution is everything that leaves a workspace about one address it
// emailed. The network turns Email into a keyed fingerprint as soon as it
// receives it and keeps one outcome per address and workspace, the latest.
type Contribution struct {
	Email   string    // lower case
	Domain  string    // lower case
	Shapes  []string  // the address shapes it fits for the person's name; may be empty
	Outcome string    // Delivered or Bounced
	SentAt  time.Time // the latest send to it
}

// Query asks about one domain. Emails are only addresses the asking
// workspace brought, or built from a name it brought: the network answers
// about them and nothing else.
type Query struct {
	Domain string
	Emails []string
	// Member says the asking workspace has joined: unlimited lookups. Others
	// get the network's small monthly allowance.
	Member bool
}

// Signal is what the network knows about one address.
type Signal struct {
	Outcome    string // Delivered or Bounced
	Confidence int    // 0-100
}

// Answer is the network's reply. It never says which workspaces, or how
// many, are behind it.
type Answer struct {
	// Shape is the domain's best-supported address shape, "" when unknown.
	Shape           string
	ShapeConfidence int // 0-100
	// CatchAll: the domain accepts any address, so delivered proves little.
	CatchAll bool
	// Emails has a signal for queried addresses the network knows.
	Emails map[string]Signal
}

// Allowance is how many lookups a workspace has this month.
type Allowance struct {
	Unlimited bool
	Limit     int
	Used      int
	ResetsAt  time.Time
}

// Errors a Network returns when it refuses a lookup. The finder then
// carries on without the network.
var (
	ErrAllowance = errors.New("data network: this month's lookups are used up")
	ErrLimit     = errors.New("data network: today's lookups for this domain are used up")
)

// Network is what an extension provides. Every method takes the workspace
// id the engine already checked; never one from input.
type Network interface {
	// Lookup answers a query and counts it against the workspace's
	// allowance and the domain's daily limit, or returns ErrAllowance or
	// ErrLimit.
	Lookup(ctx context.Context, workspaceID string, q Query) (Answer, error)
	// Contribute stores a batch from a joined workspace. It may be called
	// again with the same items after a failure.
	Contribute(ctx context.Context, workspaceID string, batch []Contribution) error
	// Withdraw removes everything the workspace contributed. The engine
	// calls it after the workspace leaves and promises it is done within
	// WithdrawWithin.
	Withdraw(ctx context.Context, workspaceID string) error
	// Allowance reports the workspace's lookups this month.
	Allowance(ctx context.Context, workspaceID string, member bool) (Allowance, error)
	// Enabled says whether the network is turned on for the workspace. It
	// is off for everyone the operator hasn't turned it on for: to them
	// there is no network, nothing is shared or looked up, and joining is
	// refused.
	Enabled(ctx context.Context, workspaceID string) bool
}

// WithdrawWithin is how long after leaving a workspace's contributions are
// gone. The owner is told this when they join and when they leave.
const WithdrawWithin = 30 * 24 * time.Hour

// SettleAfter is how long after a send it is shared: long enough for a
// bounce to come back, so an address is shared as delivered only once it
// has had the chance to bounce.
const SettleAfter = 7 * 24 * time.Hour

// Service is the engine's side of the network.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger

	mu  sync.RWMutex
	net Network
}

// Set registers the network. An extension calls it during setup.
func (s *Service) Set(n Network) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.net = n
}

// Network returns the registered network, or nil.
func (s *Service) Network() Network {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.net
}

// For returns the network when it is turned on for the workspace, or nil.
func (s *Service) For(ctx context.Context, ws string) Network {
	n := s.Network()
	if n == nil || !n.Enabled(ctx, ws) {
		return nil
	}
	return n
}

// Available reports whether the network is turned on for the workspace.
func (s *Service) Available(ctx context.Context, ws string) bool { return s.For(ctx, ws) != nil }
