package linkorders

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Statuses an order may hold. The check on coverage_link_orders.status
// lists the same values.
const (
	StatusRequested   = "requested"
	StatusQuoted      = "quoted"
	StatusNegotiating = "negotiating"
	StatusApproved    = "approved"
	StatusPublished   = "published"
	StatusPaid        = "paid"
	StatusVerified    = "verified"
	StatusLost        = "lost"
)

// Currencies the quoted_currency / currency columns hold. USD and EUR are
// never summed. An invoice in another currency (InvoiceCurrencies) keeps
// its own amount in QuotedInvoice / Invoice, and these columns hold its
// US-dollar equivalent in USD.
const (
	CurrencyUSD = "USD"
	CurrencyEUR = "EUR"
)

// DonorSource is how a catalog row was last written.
const (
	SourceManual        = "manual"
	SourceCSV           = "csv"
	SourceCompetitorGap = "competitor_gap"
)

// DefaultGuaranteeDays is used when a new order does not set a window and
// the catalog row has none either.
const DefaultGuaranteeDays = 30

// Errors the API maps to HTTP status codes. Wrapping is fine; use
// errors.Is to match.
var (
	ErrInvalid         = errors.New("invalid request")
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
	ErrForbidden       = errors.New("forbidden")
	ErrOverBudget      = errors.New("approve would exceed the brand budget for this currency")
	ErrNoApprover      = errors.New("set an approver on link-order settings first")
	ErrNotApprover     = errors.New("only the brand's designated approver can do this")
	ErrNotLive         = errors.New("paid is only allowed after a passing live-link check (the link is on the page and the target matches)")
	ErrNotPublished    = errors.New("paid is only allowed after publication")
	ErrAmountFrozen    = errors.New("the donor amount is frozen after approval and cannot change")
	ErrOpenReplacement = errors.New("this order already has an open replacement")
	ErrReplaceNotLost  = errors.New("only a lost order can be replaced")
	ErrReplaceCycle    = errors.New("a replacement cannot form a cycle")
)

// Price is a published rate on a donor, in minor units.
type Price struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

// Donor is one catalog row: a publisher plus the donor-only fields and
// the current published prices.
type Donor struct {
	ID            uuid.UUID  `json:"id"`
	PublisherID   uuid.UUID  `json:"publisher_id"`
	Domain        string     `json:"domain"`
	PBN           bool       `json:"pbn"`
	Country       string     `json:"country"`
	Languages     []string   `json:"languages"`
	Niches        []string   `json:"niches"`
	CasinoAllowed bool       `json:"casino_allowed"`
	RelOffered    []string   `json:"rel_offered"`
	SlotTypes     []string   `json:"slot_types"`
	Duration      string     `json:"duration"`
	DurationDays  *int       `json:"duration_days"`
	TATDays       *int       `json:"tat_days"`
	GuaranteeDays *int       `json:"guarantee_days"`
	VendorID      string     `json:"vendor_id"`
	Source        string     `json:"source"`
	DomainRating  *int       `json:"domain_rating"`
	Traffic       *int64     `json:"traffic"`
	SpamScore     *float64   `json:"spam_score"`
	MetricsAt     *time.Time `json:"metrics_at"`
	MetricsSource string     `json:"metrics_source"`
	Notes         string     `json:"notes"`
	// Intermediary is the broker or contact the site is bought through.
	Intermediary string `json:"intermediary"`
	// Theme is the site's own theme, separate from Niches.
	Theme string `json:"theme"`
	// SiteSince is the date (YYYY-MM-DD) the site has existed since, and
	// SiteAgeYears the whole years from then to now.
	SiteSince    *string      `json:"site_since"`
	SiteAgeYears *int         `json:"site_age_years"`
	Metrics      DonorMetrics `json:"metrics"`
	// DirectContact is whether the site is worth writing to directly,
	// computed on read (see DirectContact).
	DirectContact DirectContact `json:"direct_contact"`
	ArchivedAt    *time.Time    `json:"archived_at"`
	Prices        []Price       `json:"prices"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// Order is one link order, including its current status and frozen donor
// price once approved.
type Order struct {
	ID                  uuid.UUID  `json:"id"`
	PublisherID         uuid.UUID  `json:"publisher_id"`
	DonorProfileID      uuid.UUID  `json:"donor_profile_id"`
	Domain              string     `json:"domain"`
	Status              string     `json:"status"`
	TargetURL           string     `json:"target_url"`
	TargetID            *uuid.UUID `json:"target_id"`
	TargetKind          string     `json:"target_kind"`
	TargetChoice        string     `json:"target_choice"`
	AnchorPrimary       string     `json:"anchor_primary"`
	AnchorPrimarySource string     `json:"anchor_primary_source"`
	AnchorSecondary     string     `json:"anchor_secondary"`
	AnchorSecondaryType string     `json:"anchor_secondary_type"`
	SlotType            string     `json:"slot_type"`
	RelRequested        string     `json:"rel_requested"`
	QuotedAmountMinor   *int64     `json:"quoted_amount_minor"`
	QuotedCurrency      *string    `json:"quoted_currency"`
	DonorAmountMinor    *int64     `json:"donor_amount_minor"`
	Currency            *string    `json:"currency"`
	// QuotedInvoice is the quote as it is on the invoice, with its
	// US-dollar equivalent; Invoice is the same, frozen at Approve and
	// refreshed at Mark Paid. Accounting only: nothing sends money.
	QuotedInvoice        *Invoice   `json:"quoted_invoice"`
	Invoice              *Invoice   `json:"invoice"`
	GuaranteeDays        *int       `json:"guarantee_days"`
	GuaranteeUntil       *time.Time `json:"guarantee_until"`
	PublishedURL         string     `json:"published_url"`
	ProofURL             string     `json:"proof_url"`
	PublishedAt          *time.Time `json:"published_at"`
	PlacementID          *uuid.UUID `json:"placement_id"`
	AgreementID          *uuid.UUID `json:"agreement_id"`
	InvoiceID            *uuid.UUID `json:"invoice_id"`
	PaymentID            *uuid.UUID `json:"payment_id"`
	CreditPaymentID      *uuid.UUID `json:"credit_payment_id"`
	ReplacesOrderID      *uuid.UUID `json:"replaces_order_id"`
	OwnerUserID          *uuid.UUID `json:"owner_user_id"`
	LastCheckAt          *time.Time `json:"last_check_at"`
	LastCheckLive        *bool      `json:"last_check_live"`
	LastCheckTargetMatch *bool      `json:"last_check_target_match"`
	LastCheckRel         string     `json:"last_check_rel"`
	LastCheckAnchor      string     `json:"last_check_anchor"`
	ApprovedBy           *uuid.UUID `json:"approved_by"`
	ApprovedAt           *time.Time `json:"approved_at"`
	PaidBy               *uuid.UUID `json:"paid_by"`
	PaidAt               *time.Time `json:"paid_at"`
	LostAt               *time.Time `json:"lost_at"`
	LostReason           string     `json:"lost_reason"`
	Notes                string     `json:"notes"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// Event is one row of an order's history.
type Event struct {
	ID         uuid.UUID      `json:"id"`
	OrderID    uuid.UUID      `json:"order_id"`
	Action     string         `json:"action"`
	FromStatus *string        `json:"from_status"`
	ToStatus   *string        `json:"to_status"`
	ActorID    *uuid.UUID     `json:"actor_id"`
	Payload    map[string]any `json:"payload"`
	CreatedAt  time.Time      `json:"created_at"`
}

// CurrencyTotal is a money total in one currency, never mixed with another.
type CurrencyTotal struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

// Summary is committed, spent, and lost donor amounts by currency, plus
// counts by status. USD and EUR are always separate.
type Summary struct {
	Counts    map[string]int  `json:"counts"`
	Committed []CurrencyTotal `json:"committed"`
	Spent     []CurrencyTotal `json:"spent"`
	Lost      []CurrencyTotal `json:"lost"`
}

// BudgetView is the current period's limit and how much of it is already
// committed or spent, per currency. Missing a budget is not a cap.
type BudgetView struct {
	Budgets []BudgetCurrency `json:"budgets"`
}

// BudgetCurrency is one currency's cap for the current period.
type BudgetCurrency struct {
	BudgetID    uuid.UUID `json:"budget_id"`
	Title       string    `json:"title"`
	Currency    string    `json:"currency"`
	AmountMinor int64     `json:"amount_minor"`
	StartsOn    string    `json:"starts_on"`
	EndsOn      string    `json:"ends_on"`
	Committed   int64     `json:"committed"`
	Spent       int64     `json:"spent"`
	Remaining   int64     `json:"remaining"`
}

// ImportResult is the outcome of a catalog CSV import. Bad rows are
// listed; they do not abort the rest of the file.
type ImportResult struct {
	Created int           `json:"created"`
	Updated int           `json:"updated"`
	Errors  []ImportError `json:"errors"`
	// IgnoredColumns are header columns the import does not read, so a
	// person can see what in their file did not map to a field.
	IgnoredColumns []string `json:"ignored_columns"`
}

// ImportError is one CSV row that was not applied.
type ImportError struct {
	Row    int    `json:"row"`
	Domain string `json:"domain"`
	Error  string `json:"error"`
}
