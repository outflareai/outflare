package connectors

import (
	"context"
	"errors"
	"time"
)

// May an address be contacted: connectors that answer from a source
// outside Outflare, on the workspace's own account. A product's account
// lookup says whether the person or their company is already a customer;
// a shared outreach blocklist says whether the address is blocked. A
// feature asks every connected guard before it starts outreach to someone.
//
// A guard fails closed. Only a clean "clear" lets a person through: an
// answer the guard could not get, or one that is malformed, late, out of
// order or about another address, is "unknown", and unknown is never read
// as clear. What a guard's service answers is data from outside: only the
// fields named in each connector are read.

// CapContactGuard marks a connector that implements ContactGuard.
const CapContactGuard = "guard.contact"

// What a guard says about one contact (ContactVerdict.Status).
const (
	GuardClear   = "clear"   // the guard knows of no reason not to contact them
	GuardBlocked = "blocked" // do not contact them; Reason says why
	GuardUnknown = "unknown" // the guard could not say: hold, ask again later
)

// Why a guard blocked a contact (ContactVerdict.Reason).
const (
	GuardCustomer    = "customer"    // the person or their company is already a customer
	GuardBlocklisted = "blocklisted" // the address is on a blocklist
)

// GuardedContact is one person a feature is about to contact, or, with no
// Email, one company it is about to look into.
type GuardedContact struct {
	// Email is the person's address. Empty when only the company is asked
	// about: before anyone there is known, a feature asks whether the
	// company itself may be contacted, so no money is spent on one that
	// may not.
	Email string
	// Domain is the domain of the company they work at, when known. A
	// guard that knows companies checks it beside the address, or alone
	// when there is no address.
	Domain string
}

// ContactVerdict is a guard's answer about one contact.
type ContactVerdict struct {
	// Email is the contact's address as it was asked about.
	Email  string `json:"email"`
	Status string `json:"status"`
	// Reason is set when Status is blocked.
	Reason string `json:"reason,omitempty"`
	// Detail is a short code for an unknown answer (invalid_email,
	// http_503, timeout, incomplete_check): never text from the service,
	// an address or a key.
	Detail string `json:"detail,omitempty"`
}

// ContactGuard answers for many contacts at once. It only reads.
//
// CheckContacts returns one verdict for each contact, in the same order,
// whatever happens: when a call to the service fails, the contacts that
// call was about are unknown and the first such failure is returned as the
// error (a *GuardError) beside the verdicts.
type ContactGuard interface {
	Connector
	CheckContacts(ctx context.Context, c *Client, creds Creds, contacts []GuardedContact) ([]ContactVerdict, error)
}

// A guard whose whole list can be read: the workspace keeps a copy of it
// in its opt-out list (engine/internal/guardsync), so a company is checked
// against the copy without a call, and someone already in a campaign is
// stopped when they join the list. The live check before anyone is added
// to a campaign stays: the copy is only as new as its last sync.
const (
	// GuardListFeed is a list read as a feed that only grows, a page at a
	// time from a cursor: each sync reads what came after the last one.
	GuardListFeed = "feed"
	// GuardListSnapshot is a list read whole each time (an export): what
	// left it since the last sync leaves the copy too.
	GuardListSnapshot = "snapshot"
)

// GuardListEntry is one entry of a guard's list: an email address, or a
// whole domain when Domain is set. Value is lower case.
type GuardListEntry struct {
	Value  string
	Domain bool
}

// GuardListInfo says how a guard's list is read.
type GuardListInfo struct {
	// Mode is GuardListFeed or GuardListSnapshot.
	Mode string
	// Reason is what being on the list means: GuardCustomer or
	// GuardBlocklisted.
	Reason string
	// Every is how often it is read by default.
	Every time.Duration
}

// GuardLister is a ContactGuard whose list can be read. It only reads.
type GuardLister interface {
	ContactGuard
	GuardList() GuardListInfo
	// ReadGuardList reads the list and hands it to emit in batches, each
	// with the cursor to carry on from once the batch is kept. A feed
	// starts after cursor ("" is the beginning) and reads at most pages
	// pages; more says whether it stopped before the end. A snapshot reads
	// everything from the beginning whatever cursor and pages say, and
	// more is false. An error from emit stops the read and is returned.
	ReadGuardList(ctx context.Context, c *Client, creds Creds, cursor string, pages int,
		emit func(entries []GuardListEntry, cursor string) error) (more bool, err error)
}

// GuardError is a guard's call that got no usable answer.
type GuardError struct {
	// Err says what happened in words a person can act on, with its kind.
	Err *Error
	// Code is a short code for events and logs (http_429, timeout, schema,
	// incomplete_check, redirect): never text from the service.
	Code string
	// RetryAfter is how long the service asked to be left alone, when it
	// said (a 429 with Retry-After). Zero when it didn't.
	RetryAfter time.Duration
}

func (e *GuardError) Error() string { return e.Err.Message }
func (e *GuardError) Unwrap() error { return e.Err }

// GuardFailure reads a guard's failure: its code and how long to wait
// before asking again (zero when the service did not say). An error that
// is not a GuardError has the code "internal".
func GuardFailure(err error) (code string, retryAfter time.Duration) {
	var g *GuardError
	if errors.As(err, &g) {
		return g.Code, g.RetryAfter
	}
	return "internal", 0
}
