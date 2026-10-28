// Package auth holds the primitives for identity: password hashing, opaque
// tokens, roles and scopes.
//
// Authorization answers "can this actor ever do this?" with scopes. Roles are
// named bundles of scopes. API tokens carry an explicit scope list that can
// never exceed what their creator's role allows.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

// Scopes. Later slices add their own (mailboxes, sequences, outreach, inbox).
const (
	ScopeWorkspaceRead = "workspace.read"
	// Managing the workspace is renaming it and changing its URL name.
	ScopeWorkspaceManage = "workspace.manage"
	// Managing context is editing what Outflare remembers about the
	// business: the company context and memories agents save as they work.
	// Reading it needs only workspace.read.
	ScopeContextManage = "context.manage"
	ScopeMembersRead   = "members.read"
	// Managing members is inviting people and changing their roles and
	// brands, never past what the caller holds.
	ScopeMembersManage = "members.manage"
	// Removing members is taking someone out of the workspace or reducing
	// what they can do: a lower role or fewer brands. Only owners have it
	// by default.
	ScopeMembersRemove = "members.remove"
	// Managing roles is creating the workspace's own roles and changing
	// what any role but Owner allows.
	ScopeRolesManage     = "roles.manage"
	ScopeTokensManage    = "tokens.manage"
	ScopeAuditRead       = "audit.read"
	ScopeMailboxesRead   = "mailboxes.read"
	ScopeMailboxesManage = "mailboxes.manage"
	ScopeLeadsRead       = "leads.read"
	ScopeLeadsManage     = "leads.manage"
	// Removing a suppression can lead to emailing someone who opted out, so
	// it is separate from managing leads and reserved for admins by default.
	ScopeSuppressionsManage = "suppressions.manage"
	ScopeSequencesRead      = "sequences.read"
	// Managing sequences includes enrolling leads and activating sends.
	ScopeSequencesManage = "sequences.manage"
	ScopeInboxRead       = "inbox.read"
	// Managing the inbox is marking messages read and replying to them.
	ScopeInboxManage = "inbox.manage"
	// Connected services: email verification, other sequencers, mailbox
	// suppliers, CRMs. Managing them means handling their API keys.
	ScopeIntegrationsRead   = "integrations.read"
	ScopeIntegrationsManage = "integrations.manage"
	// Outcomes: meetings, deals, the spend ledger and cost reports.
	// Managing them is logging meetings, deals and spend by hand.
	ScopeOutcomesRead   = "outcomes.read"
	ScopeOutcomesManage = "outcomes.manage"
	ScopeProjectsRead   = "projects.read"
	// Managing projects is creating them, editing the brief and moving
	// domains, mailboxes, lists and sequences between them.
	ScopeProjectsManage = "projects.manage"
	// Infrastructure plans: reading them, drafting and changing them, and
	// approving one, which may buy domains and mailboxes with the
	// customer's money. Approving is its own scope so an agent can draft
	// plans without being able to spend.
	ScopeInfraRead     = "infrastructure.read"
	ScopeInfraManage   = "infrastructure.manage"
	ScopeInfraApprove  = "infrastructure.approve"
	ScopeAnalyticsRead = "analytics.read" // reports from connected analytics tools
	// Managing analytics is making campaign links and the campaigns they
	// belong to.
	ScopeAnalyticsManage = "analytics.manage"
	ScopeMissionsRead    = "missions.read"
	// Running a mission is what an agent does: create missions, plan, act
	// inside them and report. It never includes approving its own writes.
	ScopeMissionsRun = "missions.run"
	// Approving is starting a mission, approving or rejecting the writes
	// it holds, changing its limits, and resuming it.
	ScopeMissionsApprove = "missions.approve"
	// SEO: sites, ownership checks and crawls. Managing is adding sites and
	// starting crawls, which visit the site as OutflareBot.
	ScopeSEORead      = "seo.read"
	ScopeSEOManage    = "seo.manage"
	ScopeWebhooksRead = "webhooks.read"
	// Managing webhooks is registering URLs that receive workspace events
	// (lead emails and replies included), so it is for admins by default.
	ScopeWebhooksManage = "webhooks.manage"
	// Lifecycle and transactional email through the workspace's own email
	// provider: reading emails and their delivery, sending them (what an
	// app's backend token needs), and managing providers, sending domains
	// and templates.
	ScopeEmailsRead   = "emails.read"
	ScopeEmailsSend   = "emails.send"
	ScopeEmailsManage = "emails.manage"
	// Customer records: people, companies and the record types a workspace
	// adds, with their identities and links.
	ScopeCustomersRead   = "customers.read"
	ScopeCustomersManage = "customers.manage"
	// Changing the shared field catalog (record types, fields, option lists,
	// event definitions) changes every feature that uses it, so it is for
	// admins by default.
	ScopeCatalogManage = "catalog.manage"
	// Social channels such as Reddit: reading what watches found, drafting
	// replies and other actions (social.manage), and approving an action
	// so it runs on the brand's own account (social.post), which only a
	// person can do.
	ScopeSocialRead   = "social.read"
	ScopeSocialManage = "social.manage"
	ScopeSocialPost   = "social.post"
	// Partner programs (referrals, affiliates, influencers): reading them,
	// running them (partners, rewards, payouts), and reporting conversions
	// from the workspace's own backend (partners.conversions), the one
	// scope a server-to-server token needs.
	ScopePartnersRead        = "partners.read"
	ScopePartnersManage      = "partners.manage"
	ScopePartnersConversions = "partners.conversions"
)

// AllScopes lists every scope that exists, in display order.
var AllScopes = []string{
	ScopeWorkspaceRead,
	ScopeWorkspaceManage,
	ScopeContextManage,
	ScopeMembersRead,
	ScopeMembersManage,
	ScopeMembersRemove,
	ScopeRolesManage,
	ScopeTokensManage,
	ScopeAuditRead,
	ScopeMailboxesRead,
	ScopeMailboxesManage,
	ScopeLeadsRead,
	ScopeLeadsManage,
	ScopeSuppressionsManage,
	ScopeSequencesRead,
	ScopeSequencesManage,
	ScopeInboxRead,
	ScopeInboxManage,
	ScopeIntegrationsRead,
	ScopeIntegrationsManage,
	ScopeOutcomesRead,
	ScopeOutcomesManage,
	ScopeProjectsRead,
	ScopeProjectsManage,
	ScopeInfraRead,
	ScopeInfraManage,
	ScopeInfraApprove,
	ScopeAnalyticsRead,
	ScopeAnalyticsManage,
	ScopeMissionsRead,
	ScopeMissionsRun,
	ScopeMissionsApprove,
	ScopeSEORead,
	ScopeSEOManage,
	ScopeWebhooksRead,
	ScopeWebhooksManage,
	ScopeEmailsRead,
	ScopeEmailsSend,
	ScopeEmailsManage,
	ScopeCustomersRead,
	ScopeCustomersManage,
	ScopeCatalogManage,
	ScopeSocialRead,
	ScopeSocialManage,
	ScopeSocialPost,
	ScopePartnersRead,
	ScopePartnersManage,
	ScopePartnersConversions,
}

const (
	RoleOwner    = "owner"
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// PersonOnly are scopes that only a signed-in person uses: taking
// someone's access away and changing roles. No API token or connected app
// ever holds them.
var PersonOnly = []string{ScopeMembersRemove, ScopeRolesManage}

var roleScopes = map[string][]string{
	RoleOwner: AllScopes,
	// Admins do everything except take people's access away.
	RoleAdmin:    slices.DeleteFunc(slices.Clone(AllScopes), func(s string) bool { return s == ScopeMembersRemove }),
	RoleOperator: {ScopeWorkspaceRead, ScopeContextManage, ScopeMembersRead, ScopeMailboxesRead, ScopeMailboxesManage, ScopeLeadsRead, ScopeLeadsManage, ScopeSequencesRead, ScopeSequencesManage, ScopeInboxRead, ScopeInboxManage, ScopeIntegrationsRead, ScopeIntegrationsManage, ScopeOutcomesRead, ScopeOutcomesManage, ScopeProjectsRead, ScopeProjectsManage, ScopeInfraRead, ScopeInfraManage, ScopeAnalyticsRead, ScopeAnalyticsManage, ScopeMissionsRead, ScopeMissionsRun, ScopeMissionsApprove, ScopeSEORead, ScopeSEOManage, ScopeWebhooksRead, ScopeEmailsRead, ScopeEmailsSend, ScopeEmailsManage, ScopeCustomersRead, ScopeCustomersManage, ScopeSocialRead, ScopeSocialManage, ScopeSocialPost, ScopePartnersRead, ScopePartnersManage, ScopePartnersConversions},
	RoleViewer:   {ScopeWorkspaceRead, ScopeMembersRead, ScopeMailboxesRead, ScopeLeadsRead, ScopeSequencesRead, ScopeInboxRead, ScopeIntegrationsRead, ScopeOutcomesRead, ScopeProjectsRead, ScopeInfraRead, ScopeAnalyticsRead, ScopeMissionsRead, ScopeSEORead, ScopeWebhooksRead, ScopeEmailsRead, ScopeCustomersRead, ScopeSocialRead, ScopePartnersRead},
}

// workspaceWide are scopes about the whole workspace: settings, people,
// roles, tokens and the audit log. A role holding any of them can't be
// kept to some brands.
var workspaceWide = []string{ScopeWorkspaceManage, ScopeMembersManage, ScopeMembersRemove, ScopeRolesManage, ScopeTokensManage, ScopeAuditRead}

// BrandLimitable reports whether someone holding scopes may be kept to
// some brands.
func BrandLimitable(scopes []string) bool {
	return !slices.ContainsFunc(scopes, func(s string) bool { return slices.Contains(workspaceWide, s) })
}

// Within reports whether every scope in inner is also in outer.
func Within(inner, outer []string) bool {
	return !slices.ContainsFunc(inner, func(s string) bool { return !slices.Contains(outer, s) })
}

// CleanScopes keeps the known scopes in display order, without repeats.
func CleanScopes(scopes []string) []string {
	out := []string{}
	for _, s := range AllScopes {
		if slices.Contains(scopes, s) {
			out = append(out, s)
		}
	}
	return out
}

// ValidRole reports whether role exists.
func ValidRole(role string) bool { _, ok := roleScopes[role]; return ok }

// RoleScopes returns the scopes granted by a role.
func RoleScopes(role string) []string { return roleScopes[role] }

// Grants reports whether scopes allow want. Any scope in an area includes
// reading it: leads.manage can list leads, missions.run can read missions.
func Grants(scopes []string, want string) bool {
	if slices.Contains(scopes, want) {
		return true
	}
	area, ok := strings.CutSuffix(want, ".read")
	if !ok {
		return false
	}
	return slices.ContainsFunc(scopes, func(s string) bool { return strings.HasPrefix(s, area+".") })
}

// ValidScope reports whether s is a known scope.
func ValidScope(s string) bool { return slices.Contains(AllScopes, s) }

// RoleRank orders roles so that managers cannot act on someone above them.
func RoleRank(role string) int {
	switch role {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleOperator:
		return 1
	}
	return 0
}

// Token kinds, used as visible prefixes so a leaked secret is recognisable.
const (
	PrefixSession  = "ofs_"
	PrefixAPIToken = "oft_"
	// Agency tokens work in every client workspace of one agency.
	PrefixAgencyToken = "ofa_"
	PrefixInvite      = "ofi_"
	// Site keys are public: a website's tracking snippet writes browser
	// events with one, and it opens nothing else.
	PrefixSiteKey = "ofk_"
	// A site's own proxy sends its proxy secret with each forwarded
	// request, so the visitor's address it passes on can be trusted.
	PrefixSiteProxy = "ofp_"
	// A partner program's incoming postback URL carries one, so a partner's
	// tracker or the workspace's backend can report conversions by URL.
	PrefixPostback = "ofb_"
	// Partner portals: the one-time sign-in link a partner is emailed, and
	// the session it starts on that portal.
	PrefixPartnerLink    = "ofpl_"
	PrefixPartnerSession = "ofps_"
	// One-time links in account emails.
	PrefixVerifyEmail   = "ofv_"
	PrefixResetPassword = "ofr_"
	// A support view lets a staff member see one account read-only, beside
	// their own session (outflare_view cookie).
	PrefixSupportView = "ofsv_"
)

// NewToken returns a random secret with the given prefix and its hash for
// storage. Only the hash is stored; the secret is shown once.
func NewToken(prefix string) (secret string, hash []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	secret = prefix + base64.RawURLEncoding.EncodeToString(b)
	return secret, HashToken(secret)
}

// HashToken hashes a token for lookup. Tokens are 256-bit random, so a plain
// SHA-256 is sufficient (no need for a slow hash).
func HashToken(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// Argon2id parameters (RFC 9106 second recommended option, memory-constrained).
// Only FastHashesForTests changes them.
var (
	argonTime   uint32 = 3
	argonMemory uint32 = 64 * 1024
)

const (
	argonThreads = 2
	argonKeyLen  = 32
)

// MinPasswordLength is enforced on every password set.
const MinPasswordLength = 10

var ErrWeakPassword = fmt.Errorf("password must be at least %d characters", MinPasswordLength)

// HashPassword returns an encoded argon2id hash.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength {
		return "", ErrWeakPassword
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// CheckPassword compares a password with an encoded hash in constant time.
func CheckPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err := errors.Join(err1, err2); err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is checked when a login names an unknown email, so response time
// does not reveal which emails have accounts.
var dummyHash, _ = HashPassword("not-a-real-password-0000")

// BurnPasswordCheck spends the same time as a real password check.
func BurnPasswordCheck(password string) { CheckPassword(dummyHash, password) }

// FastHashesForTests makes new password hashes (and the dummy check) cheap.
// Under the race detector the real cost is most of a test run that signs
// people in. Call it from TestMain before any test runs; it panics outside a
// test binary, so production hashes always use the parameters above.
func FastHashesForTests() {
	if !testing.Testing() {
		panic("auth.FastHashesForTests called outside a test")
	}
	argonTime, argonMemory = 1, 64
	dummyHash, _ = HashPassword("not-a-real-password-0000")
}
