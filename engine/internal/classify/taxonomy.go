// Package classify says what kind of email infrastructure an address or
// domain is: a free consumer mailbox, a private mailbox, an alias service,
// a throwaway inbox, a school, a government, or not known yet. It answers
// from intelligence Outflare maintains (data/), plus DNS when asked.
//
// The data is global: the same for every workspace. Workspace input
// (overrides, feedback, results from a workspace's paid verifier) never
// changes it.
//
// Confidence is null until it is calibrated against a reviewed set of
// domains; ConfidenceStatus says what the answer rests on instead.
package classify

import "encoding/json"

// TaxonomyVersion versions the category and attribute codes below. Codes
// are added, never renamed; a deprecated code maps to its replacement.
const TaxonomyVersion = "1.0"

// Categories: what an email domain is.
const (
	Business               = "business"
	FreePersonal           = "free_personal"
	PrivateMailbox         = "private_mailbox"
	Disposable             = "disposable"
	RelayAlias             = "relay_alias"
	Education              = "education"
	Government             = "government"
	NonprofitInstitutional = "nonprofit_institutional"
	PersonalCustom         = "personal_custom"
	ManagedOther           = "managed_other"
	Unknown                = "unknown"
)

// Categories lists every category code.
var Categories = []string{Business, FreePersonal, PrivateMailbox, Disposable, RelayAlias, Education, Government,
	NonprofitInstitutional, PersonalCustom, ManagedOther, Unknown}

// Subcategories of disposable and relay_alias.
const (
	ShortLivedMailbox        = "short_lived_mailbox"
	ThrowawayPersistent      = "throwaway_persistent"
	SharedPublicInbox        = "shared_public_inbox"
	TemporaryForwarder       = "temporary_forwarder"
	RotatingDomainProvider   = "rotating_domain_provider"
	AnonymousAlias           = "anonymous_alias"
	PermanentAlias           = "permanent_alias"
	UnknownDisposableSubtype = "unknown_disposable_subtype"
)

// Subcategories lists every subcategory code.
var Subcategories = []string{ShortLivedMailbox, ThrowawayPersistent, SharedPublicInbox, TemporaryForwarder,
	RotatingDomainProvider, AnonymousAlias, PermanentAlias, UnknownDisposableSubtype}

// Confidence statuses: what an answer rests on.
const (
	Verified             = "verified"              // a provider Outflare reviewed
	Corroborated         = "corroborated"          // two or more independent sources agree
	Inferred             = "inferred"              // one source or one indicator, such as a .edu suffix
	InsufficientEvidence = "insufficient_evidence" // nothing to go on yet
	Conflicted           = "conflicted"            // sources disagree and none outranks the others
)

// Recommended actions.
const (
	Allow  = "allow"
	Verify = "verify"
	Review = "review"
	Block  = "block"
)

// Tri is an attribute that is true, false or not known. Absence of evidence
// is Unknown, never false.
type Tri int8

const (
	TriUnknown Tri = iota
	TriFalse
	TriTrue
)

// String writes a Tri the way the API does: true, false or unknown.
func (t Tri) String() string {
	switch t {
	case TriTrue:
		return "true"
	case TriFalse:
		return "false"
	}
	return "unknown"
}

// Of turns a bool into a known Tri.
func Of(b bool) Tri {
	if b {
		return TriTrue
	}
	return TriFalse
}

// MarshalJSON writes true, false or "unknown".
func (t Tri) MarshalJSON() ([]byte, error) {
	switch t {
	case TriTrue:
		return []byte("true"), nil
	case TriFalse:
		return []byte("false"), nil
	}
	return []byte(`"unknown"`), nil
}

// UnmarshalJSON reads true, false or "unknown".
func (t *Tri) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch v {
	case true:
		*t = TriTrue
	case false:
		*t = TriFalse
	default:
		*t = TriUnknown
	}
	return nil
}
