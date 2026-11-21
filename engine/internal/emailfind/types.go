package emailfind

import (
	"context"
	"math"
	"time"
)

// Code is the outcome of finding or verifying one address. It maps to what a
// caller's pipeline does next: skip (we have an answer), wait (a slow check is
// running), retry (a transient failure), or stop (the run cannot continue). It
// is the shared contract with the lead-finder pipeline.
type Code string

const (
	CodeFound         Code = "found"         // a confident guess, or a connected finder's answer; continue
	CodeVerified      Code = "verified"      // seen deliverable first-party; continue
	CodeRisky         Code = "risky"         // catch-all or low confidence; caller's policy
	CodeNotFound      Code = "not_found"     // no address could be built; skip
	CodeUndeliverable Code = "undeliverable" // known bad; skip
	CodePending       Code = "pending"       // a slow check is running; re-call later
	CodeRetry         Code = "retry"         // transient failure; retry after RetryAfter
	CodeStop          Code = "stop"          // cannot continue; see StopCode
)

// Evidence names where a result's confidence came from.
const (
	EvidenceHistory = "history" // a first-party outcome for this exact address
	EvidencePattern = "pattern" // the domain's learned shape
	EvidenceDefault = "default" // shape priors only, no domain evidence yet
	EvidenceNetwork = "network" // the data network: other workspaces' outcomes or the domain's shape
)

// NetworkLookup asks the data network about addresses at one domain
// (internal/datanetwork). ok is false when there is no network, the domain
// is a personal mail provider, or the lookup was refused or failed; the
// finder then carries on as if there were none.
type NetworkLookup interface {
	LookupNetwork(ctx context.Context, ws, domain string, emails []string) (a NetworkAnswer, ok bool)
}

// NetworkAnswer is what the network knows. Emails only holds addresses
// that were asked about.
type NetworkAnswer struct {
	Shape           string
	ShapeConfidence int
	CatchAll        bool
	Emails          map[string]NetworkSignal
}

// NetworkSignal is the network's view of one address.
type NetworkSignal struct {
	Deliverable bool // false: it bounced
	Confidence  int
}

// Cost is what one find or verify spent. Native, first-party work is free, so
// Cents and Units are zero and Provider is "outflare"; the workspace's own
// connected finder and paid verifier report their spend and which
// connection answered: the lookups the finder charged plus one credit an
// address the verifier checked (Units), as the spend ledger books them.
// When both charged, Provider and Connection name the finder and Units is
// the total. An answer from the cache costs nothing.
//
// Micros is what the providers said those units cost, in millionths of a
// dollar, for a caller that keeps the exact cost (Treg reports each call's
// cost; most providers report none, and their units are priced with the
// workspace's spend rates). Only Go callers see it.
type Cost struct {
	Cents      int    `json:"cents"`
	Units      int    `json:"units"`
	Provider   string `json:"provider"`
	Connection string `json:"connection,omitempty"`
	Micros     int64  `json:"-"`
}

// micros rounds a provider's reported cost to whole millionths.
func micros(v float64) int64 {
	if v <= 0 {
		return 0
	}
	return int64(math.Round(v))
}

// FindItem is one person to find an address for. Ref is the caller's own
// reference, echoed back so results match without relying on order.
type FindItem struct {
	Ref       string `json:"ref"`
	RecordID  string `json:"record_id,omitempty"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Domain    string `json:"domain"`
	Company   string `json:"company,omitempty"`
	// LinkedInURL is the person's own LinkedIn profile, when the caller
	// has it. Resolve gives it to the connected finders that can look a
	// person up by profile and asks those first (finder.go). Anything that
	// is not a person's profile URL is ignored.
	LinkedInURL string `json:"linkedin_url,omitempty"`
}

// FindRequest is a batch to resolve. A repeat for the same people in the
// same workspace does not spend again: a finder's answer is remembered per
// person and a verifier's per address, whatever the IdempotencyKey. Keep
// Items at or below MaxBatch so a job finishes inside the queue's lease.
type FindRequest struct {
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
	Items          []FindItem `json:"items"`
}

// MaxBatch is the most items one find or verify call takes.
const MaxBatch = 50

// ItemResult is the answer for one FindItem.
type ItemResult struct {
	Ref        string        `json:"ref"`
	RecordID   string        `json:"record_id,omitempty"`
	Email      string        `json:"email,omitempty"`
	Confidence int           `json:"confidence"` // 0-100
	Code       Code          `json:"code"`
	Evidence   string        `json:"evidence,omitempty"`
	Cost       Cost          `json:"cost"`
	StopCode   string        `json:"stop_code,omitempty"`   // errorcodes.* when Code is stop
	RetryAfter time.Duration `json:"retry_after,omitempty"` // set when pending or retry
}

// FindResult is a batch answer. RetryAfter is set at the batch level when the
// whole batch is rate limited.
type FindResult struct {
	Items      []ItemResult  `json:"items"`
	RetryAfter time.Duration `json:"retry_after,omitempty"`
}
