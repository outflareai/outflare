package classify

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Classifier answers what kind of email infrastructure an address or domain
// is. The zero value uses the built-in data and no DNS.
type Classifier struct {
	Intel *Intel // nil uses Builtin()
	// Resolver checks mail servers; nil leaves the mail attributes unknown.
	Resolver Resolver
	Timeout  time.Duration // per DNS lookup; 0 means 3 seconds
	cache    dnsCache
}

// Input is an email address or a domain, never both. A domain alone keeps
// the person's address out of the request.
type Input struct {
	Email  string
	Domain string
}

// ErrInput is returned when Input has both or neither.
var ErrInput = errors.New("email or domain is required, but not both")

// Result is the answer. Global is the same for every workspace; Effective
// is what this workspace's rules make of it.
type Result struct {
	Scope               string     `json:"scope"` // email or domain
	NormalizedDomain    string     `json:"normalized_domain"`
	RegistrableDomain   string     `json:"registrable_domain"`
	TaxonomyVersion     string     `json:"taxonomy_version"`
	IntelligenceVersion string     `json:"intelligence_version"`
	Global              Assessment `json:"global"`
	// Research is what this workspace's own research made of the domain,
	// when it has researched it. It is absent for every other workspace.
	Research  *Research `json:"research,omitempty"`
	Effective Effective `json:"effective"`
	Analysis  Analysis  `json:"analysis"`
}

// Assessment is the global classification of the domain.
type Assessment struct {
	Category    string  `json:"category"`
	Subcategory *string `json:"subcategory"`
	// Confidence is a calibrated probability the category is right. It is
	// null until calibration exists; ConfidenceStatus says what the
	// category rests on.
	Confidence       *float64   `json:"confidence"`
	ConfidenceStatus string     `json:"confidence_status"`
	Provider         *Ref       `json:"provider"`
	Attributes       Attributes `json:"attributes"`
	Risk             Risk       `json:"risk"`
	Evidence         []Evidence `json:"evidence"`
	// LastCheckedAt is when DNS was last asked, or null.
	LastCheckedAt *time.Time `json:"last_checked_at"`
}

// Ref names a provider.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Attributes are independent of the category. Address-only ones are
// left out for a domain lookup.
type Attributes struct {
	IsDisposable              Tri     `json:"is_disposable"`
	IsPublicInbox             Tri     `json:"is_public_inbox"`
	IsForwarder               Tri     `json:"is_forwarder"`
	IsAliasService            Tri     `json:"is_alias_service"`
	IsPrivacyProvider         Tri     `json:"is_privacy_provider"`
	IsFreeProvider            Tri     `json:"is_free_provider"`
	IsRoleAddress             *Tri    `json:"is_role_address,omitempty"`
	IsCatchAllDomain          Tri     `json:"is_catch_all_domain"`
	IsNewDomain               Tri     `json:"is_new_domain"`
	HasMX                     Tri     `json:"has_mx"`
	HasMailReceivingPath      Tri     `json:"has_mail_receiving_path"`
	HasValidSyntax            Tri     `json:"has_valid_syntax"`
	IsInternationalizedDomain Tri     `json:"is_internationalized_domain"`
	MXProviderID              *string `json:"mx_provider_id"`
}

// Risk is how much a domain should concern a sign-up flow, separate from
// what it is. Scores are rule-based until calibrated: 0 to 100, higher is
// more concern, never a probability that a person is fraudulent.
type Risk struct {
	Status  string   `json:"status"` // assessed or not_assessed
	Score   *int     `json:"score"`
	Level   string   `json:"level"` // low, medium, high or unknown
	Signals []string `json:"signals"`
}

// Evidence is one fact behind the answer.
type Evidence struct {
	Source string `json:"source"`
	Signal string `json:"signal"`
	// Detail is a fixed, non-personal note, such as the list entry matched.
	Detail string `json:"detail,omitempty"`
}

// Effective is the workspace's view: the global category after its
// overrides, and the action its rules recommend. Until workspaces have
// overrides and rules, it is the global category and Outflare's default
// rules.
type Effective struct {
	Category        string  `json:"category"`
	Subcategory     *string `json:"subcategory"`
	OverrideApplied bool    `json:"override_applied"`
	OverrideID      *string `json:"override_id"`
	Action          string  `json:"action"` // allow, verify, review or block
	MatchedPolicyID string  `json:"matched_policy_id"`
}

// Analysis statuses: where the answer is in its life.
const (
	AnalysisComplete   = "complete"   // nothing more to find out
	AnalysisNotQueued  = "not_queued" // research could help, but none was asked for
	AnalysisQueued     = "queued"     // research is running; ask again shortly
	AnalysisResearched = "researched" // the workspace's own research answered
)

// Analysis says whether the answer is final or waiting on more research.
type Analysis struct {
	Status      string `json:"status"`
	Provisional bool   `json:"provisional"`
}

// Classify answers for one address or domain.
func (c *Classifier) Classify(ctx context.Context, in Input) (Result, error) {
	var (
		d    Domain
		role *Tri
	)
	switch {
	case (in.Email == "") == (in.Domain == ""):
		return Result{}, ErrInput
	case in.Email != "":
		a, err := ParseEmail(in.Email)
		if err != nil {
			return Result{}, err
		}
		d = a.Domain
		r := Of(RoleLocal(a.Local))
		role = &r
	default:
		var err error
		if d, err = ParseDomain(in.Domain); err != nil {
			return Result{}, err
		}
	}
	intel := c.Intel
	if intel == nil {
		intel = Builtin()
	}
	res := Result{Scope: "domain", NormalizedDomain: d.Name, RegistrableDomain: d.Registrable,
		TaxonomyVersion: TaxonomyVersion, IntelligenceVersion: intel.Version}
	if role != nil {
		res.Scope = "email"
	}
	g := &res.Global
	g.Evidence = []Evidence{}
	at := &g.Attributes
	at.IsRoleAddress = role
	at.HasValidSyntax = TriTrue
	at.IsInternationalizedDomain = Of(d.IDN())

	m := intel.lookup(d)
	inst, instRule := institution(d)
	signals := []string{}
	score := -1
	raise := func(n int, signal string) {
		score = max(score, n)
		if signal != "" {
			signals = append(signals, signal)
		}
	}

	switch {
	case m.provider != nil:
		p := m.provider
		g.Category, g.ConfidenceStatus = p.Category, Verified
		if p.Subcategory != "" {
			g.Subcategory = &p.Subcategory
		}
		g.Provider = &Ref{ID: p.ID, Name: p.Name}
		g.Evidence = append(g.Evidence, Evidence{Source: SourceOutflare, Signal: "reviewed_provider", Detail: m.providerDomain})
		relay := p.Category == RelayAlias
		at.IsDisposable, at.IsPublicInbox = TriFalse, TriFalse
		at.IsForwarder, at.IsAliasService = Of(relay), Of(relay)
		at.IsPrivacyProvider = Of(relay || p.Category == PrivateMailbox)
		at.IsFreeProvider = Of(p.Category == FreePersonal)
		// Reviewed providers all take mail; DNS is not asked.
		at.HasMX, at.HasMailReceivingPath = TriTrue, TriTrue
		for _, l := range m.listed {
			// A reviewed provider outranks an open list; keep the
			// disagreement visible.
			g.Evidence = append(g.Evidence, Evidence{Source: l.Source, Signal: "listed_disposable_outranked", Detail: m.listedDomain})
		}
		raise(10, "")
	case m.listed != nil:
		g.Category = Disposable
		sources := map[string]bool{}
		sub, group := UnknownDisposableSubtype, ""
		for _, l := range m.listed {
			sources[l.Source] = true
			g.Evidence = append(g.Evidence, Evidence{Source: l.Source, Signal: "listed_disposable", Detail: m.listedDomain})
			if l.Group != "" {
				group = l.Group
			}
		}
		if group != "" {
			g.Provider = &Ref{ID: "fakefilter:" + group, Name: group}
			if intel.groupSize[group] >= 3 {
				sub = RotatingDomainProvider
			}
		}
		g.Subcategory = &sub
		g.ConfidenceStatus = Inferred
		if len(sources) > 1 {
			g.ConfidenceStatus = Corroborated
		}
		if inst != "" {
			g.Evidence = append(g.Evidence, Evidence{Source: SourceSuffix, Signal: "institution_suffix", Detail: instRule})
			g.ConfidenceStatus = Conflicted
		}
		at.IsDisposable = TriTrue
		if g.ConfidenceStatus == Corroborated {
			raise(90, "listed_disposable_provider")
		} else {
			raise(75, "listed_disposable_provider")
		}
		if sub == RotatingDomainProvider {
			signals = append(signals, "rotating_domain_provider")
		}
	case inst != "":
		g.Category, g.ConfidenceStatus = inst, Inferred
		g.Evidence = append(g.Evidence, Evidence{Source: SourceSuffix, Signal: "institution_suffix", Detail: instRule})
		at.IsDisposable, at.IsFreeProvider = TriFalse, TriFalse
	default:
		g.Category, g.ConfidenceStatus = Unknown, InsufficientEvidence
	}

	noMail := false
	if c.Resolver != nil && m.provider == nil {
		p := c.mail(ctx, d.Name)
		if p.checked {
			t := p.at
			g.LastCheckedAt = &t
			at.HasMX, at.HasMailReceivingPath = Of(p.hasMX), Of(p.receives)
			if p.mxProvider != "" {
				at.MXProviderID = &p.mxProvider
				g.Evidence = append(g.Evidence, Evidence{Source: SourceDNS, Signal: "mx_provider", Detail: p.mxProvider})
			}
			switch {
			case p.nullMX:
				noMail = true
				g.Evidence = append(g.Evidence, Evidence{Source: SourceDNS, Signal: "null_mx"})
				raise(100, "domain_accepts_no_mail")
			case !p.receives:
				noMail = true
				g.Evidence = append(g.Evidence, Evidence{Source: SourceDNS, Signal: "no_mail_servers"})
				raise(100, "no_mail_servers")
			}
		}
	}
	if role != nil && *role == TriTrue {
		raise(40, "role_address")
	}

	g.Risk = Risk{Status: "not_assessed", Level: "unknown", Signals: signals}
	if score >= 0 {
		g.Risk.Status, g.Risk.Score = "assessed", &score
		switch {
		case score >= 67:
			g.Risk.Level = "high"
		case score >= 34:
			g.Risk.Level = "medium"
		default:
			g.Risk.Level = "low"
		}
	}

	e := &res.Effective
	e.Category, e.Subcategory = g.Category, g.Subcategory
	e.Action, e.MatchedPolicyID = decide(g.Category, noMail)
	res.Analysis = Analysis{Status: AnalysisComplete}
	if g.Category == Unknown {
		// An unknown domain is the long tail. Research can read its
		// homepage, if the workspace turned research on; until something
		// does, the answer stays provisional.
		res.Analysis = Analysis{Status: AnalysisNotQueued, Provisional: true}
	}
	return res, nil
}

// decide is Outflare's default policy: what to do about a category, before
// a workspace has rules of its own.
func decide(category string, noMail bool) (action, policy string) {
	switch {
	case noMail:
		return Block, "default_no_mail"
	case category == Disposable:
		return Block, "default_disposable"
	case category == Unknown:
		return Verify, "default_unknown"
	}
	return Allow, "default_allow"
}

// institution reads a domain's public suffix for the conventions schools
// and governments register under (edu, ac.uk, gov.au, go.jp). A suffix is
// a hint, not proof: the answer is inferred.
func institution(d Domain) (category, rule string) {
	if !d.ICANN {
		return "", ""
	}
	labels := strings.Split(d.Suffix, ".")
	first := labels[0]
	if len(labels) == 1 {
		switch first {
		case "edu":
			return Education, d.Suffix
		case "gov", "mil":
			return Government, d.Suffix
		}
		return "", ""
	}
	switch first {
	case "edu", "ac":
		return Education, d.Suffix
	case "gov", "govt", "gob", "gouv", "go", "gv", "mil", "gc":
		return Government, d.Suffix
	}
	return "", ""
}
