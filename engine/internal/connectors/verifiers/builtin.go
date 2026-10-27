package verifiers

import (
	"context"
	"errors"
	"net"
	"net/mail"
	"strings"

	"github.com/outflareai/outflare/engine/internal/classify"
	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Resolver looks up mail servers. *net.Resolver satisfies it.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Builtin is the free check every workspace has: address syntax, whether the
// domain accepts mail at all, disposable domains and role addresses. It
// never talks to the mailbox itself, so it cannot tell a real mailbox from a
// typo at a real company: those come back unknown.
type Builtin struct {
	Resolver Resolver // nil uses net.DefaultResolver
}

// BuiltinID is the built-in verifier's connector id.
const BuiltinID = "builtin"

func (Builtin) Definition() c.Definition {
	return c.Definition{
		ID: BuiltinID, Name: "Built-in checks", Category: c.CategoryVerification,
		Summary:     "Free checks on every address: syntax, mail servers, disposable domains and role addresses.",
		Description: "Runs without an account. It catches typos in the domain, domains with no mail servers, throwaway addresses and shared inboxes like info@. It does not confirm the mailbox exists; connect a verification service for that.",
		Website:     "https://github.com/outflareai/outflare", Auth: c.AuthNone,
		Fields: []c.Field{}, Capabilities: verifyCaps(), Pricing: "Free",
	}
}

func (b Builtin) Test(context.Context, *c.Client, c.Creds) (c.Account, error) {
	return c.Account{Name: "Built in"}, nil
}

func (b Builtin) resolver() Resolver {
	if b.Resolver != nil {
		return b.Resolver
	}
	return net.DefaultResolver
}

func (b Builtin) Verify(ctx context.Context, _ *c.Client, _ c.Creds, email string) (c.Verification, error) {
	v := c.Verification{Email: email, ProviderStatus: "builtin"}
	a, err := mail.ParseAddress(email)
	at := strings.LastIndexByte(email, '@')
	if err != nil || a.Address != email || at < 1 || at == len(email)-1 {
		v.Status, v.Reason = c.Undeliverable, "invalid_syntax"
		return v, nil
	}
	domain := strings.ToLower(email[at+1:])
	if fix, ok := typoDomains[domain]; ok {
		v.Suggestion = email[:at+1] + fix
		v.Status, v.Reason = c.Undeliverable, "domain_typo"
		return v, nil
	}
	// What kind of domain it is comes from the classifier, so lead checks
	// and sign-up checks share one set of lists. DNS stays here: it
	// decides deliverability, not category.
	cl, err := (&classify.Classifier{}).Classify(ctx, classify.Input{Email: email})
	if err != nil {
		v.Status, v.Reason = c.Undeliverable, "invalid_syntax"
		return v, nil
	}
	v.Free = cl.Global.Category == classify.FreePersonal
	v.Role = cl.Global.Attributes.IsRoleAddress != nil && *cl.Global.Attributes.IsRoleAddress == classify.TriTrue
	if cl.Global.Category == classify.Disposable {
		v.Status, v.Reason, v.Disposable = c.Undeliverable, "disposable_domain", true
		return v, nil
	}
	mxs, err := b.resolver().LookupMX(ctx, domain)
	var dnsErr *net.DNSError
	switch {
	case err == nil && len(mxs) == 1 && mxs[0].Host == ".":
		// Null MX (RFC 7505): the domain says it takes no mail.
		v.Status, v.Reason = c.Undeliverable, "domain_accepts_no_mail"
		return v, nil
	case err == nil && len(mxs) > 0:
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound, err == nil:
		// No MX: mail falls back to the domain's own address, if it has one.
		if hosts, herr := b.resolver().LookupHost(ctx, domain); herr != nil || len(hosts) == 0 {
			v.Status, v.Reason = c.Undeliverable, "no_mail_servers"
			return v, nil
		}
	default:
		// A DNS timeout is not proof of anything.
		v.Status, v.Reason = c.Unknown, "dns_unavailable"
		return v, nil
	}
	if v.Role {
		v.Status, v.Reason = c.Risky, "role_address"
		return v, nil
	}
	v.Status, v.Reason = c.Unknown, "mailbox_not_checked"
	return v, nil
}

// Common misspellings of big providers.
var typoDomains = map[string]string{
	"gmial.com": "gmail.com", "gmai.com": "gmail.com", "gmal.com": "gmail.com", "gamil.com": "gmail.com",
	"gmail.co": "gmail.com", "gmail.con": "gmail.com", "gnail.com": "gmail.com",
	"hotmial.com": "hotmail.com", "hotmal.com": "hotmail.com", "hotmail.con": "hotmail.com",
	"outlok.com": "outlook.com", "outllook.com": "outlook.com", "outlook.con": "outlook.com",
	"yaho.com": "yahoo.com", "yahooo.com": "yahoo.com", "yahoo.con": "yahoo.com",
	"iclod.com": "icloud.com", "icloud.con": "icloud.com",
}
