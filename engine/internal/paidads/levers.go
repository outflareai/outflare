package paidads

import "strings"

// Autonomy is how a finding of a kind is acted on.
const (
	// AutonomyAuto: applied without asking (a saving or the protective
	// overspend pause, where the extension's hook allows it: AutoApprove),
	// reported with an undo.
	AutonomyAuto = "auto"
	// AutonomyApprove: waits for a person.
	AutonomyApprove = "approve"
	// AutonomyAlert: nothing to apply; a person acknowledges it.
	AutonomyAlert = "alert"
	// AutonomyShadow: recorded, never applied or offered: what the rule
	// would have done, while the brand's shadow mode is on.
	AutonomyShadow = "shadow"
)

// Lever is one abstract change the core knows. Adapters translate it into
// network calls; the rules tag their findings with its kind.
type Lever struct {
	Kind string
	// Autonomy is the class the lever belongs to before the brand's
	// settings are applied: auto for a change that can only spend less or
	// protects, approve for one that can spend more, alert for none.
	Autonomy string
	// SpendsMore: the change can raise spend. "depends" is a target set
	// where none was.
	SpendsMore string // "yes", "no", "depends"
	// Reversible: how it is undone: "yes" (the inverse change), "partly",
	// "pause" (a draft goes live, so pausing it is the undo), "end" (an
	// experiment ends), "n/a".
	Reversible string
	// Requires is the Capabilities flag a network needs for it: manageable,
	// HasKeywords, SupportsTargets, SupportsCampaignCreate,
	// SupportsExperiments, exclusions (per flag), all.
	Requires string
	// Protective: raised by the alerts (an overspend pause), not by the
	// rules; it pauses only where AutoApprove allows it, and is an alert
	// otherwise.
	Protective bool
}

// Levers is the catalogue (plan.md "Lever catalogue"). keyword_split is
// retired (single-keyword ad groups are obsolete); consolidate replaces it.
var Levers = []Lever{
	{Kind: "budget_down", Autonomy: AutonomyAuto, SpendsMore: "no", Reversible: "yes", Requires: "manageable"},
	{Kind: "budget_up", Autonomy: AutonomyApprove, SpendsMore: "yes", Reversible: "yes", Requires: "manageable"},
	{Kind: "pause", Autonomy: AutonomyAuto, SpendsMore: "no", Reversible: "yes", Requires: "manageable"},
	{Kind: "resume", Autonomy: AutonomyApprove, SpendsMore: "yes", Reversible: "yes", Requires: "manageable"},
	{Kind: "negative_add", Autonomy: AutonomyAuto, SpendsMore: "no", Reversible: "yes", Requires: "HasKeywords"},
	{Kind: "keyword_add", Autonomy: AutonomyApprove, SpendsMore: "yes", Reversible: "yes", Requires: "HasKeywords"},
	{Kind: "keyword_pause", Autonomy: AutonomyAuto, SpendsMore: "no", Reversible: "yes", Requires: "HasKeywords"},
	{Kind: "ad_pause", Autonomy: AutonomyAuto, SpendsMore: "no", Reversible: "yes", Requires: "manageable"},
	{Kind: "target_tighten", Autonomy: AutonomyAuto, SpendsMore: "no", Reversible: "yes", Requires: "SupportsTargets"},
	{Kind: "target_loosen", Autonomy: AutonomyApprove, SpendsMore: "yes", Reversible: "yes", Requires: "SupportsTargets"},
	{Kind: "bidding_target", Autonomy: AutonomyApprove, SpendsMore: "depends", Reversible: "yes", Requires: "SupportsTargets"},
	{Kind: "exclusion_add", Autonomy: AutonomyAuto, SpendsMore: "no", Reversible: "yes", Requires: "exclusions"},
	{Kind: "consolidate", Autonomy: AutonomyApprove, SpendsMore: "no", Reversible: "partly", Requires: "HasKeywords"},
	{Kind: "draft_ad", Autonomy: AutonomyApprove, SpendsMore: "yes", Reversible: "pause", Requires: "SupportsCampaignCreate"},
	{Kind: "draft_ad_group", Autonomy: AutonomyApprove, SpendsMore: "yes", Reversible: "pause", Requires: "SupportsCampaignCreate"},
	{Kind: "draft_campaign", Autonomy: AutonomyApprove, SpendsMore: "yes", Reversible: "pause", Requires: "SupportsCampaignCreate"},
	{Kind: "experiment_start", Autonomy: AutonomyApprove, SpendsMore: "yes", Reversible: "end", Requires: "SupportsExperiments"},
	{Kind: "overspend_pause", Autonomy: AutonomyAuto, SpendsMore: "no", Reversible: "yes", Requires: "manageable", Protective: true},
	// alert.* kinds (alert.auto_apply_on, alert.target_stale, alert.tracking_broken, ...).
	{Kind: "alert.*", Autonomy: AutonomyAlert, SpendsMore: "no", Reversible: "n/a", Requires: "all"},
}

// LeverOf returns the lever for a kind; any alert.* kind is the alert
// lever.
func LeverOf(kind string) (Lever, bool) {
	for _, l := range Levers {
		if l.Kind == kind {
			return l, true
		}
	}
	if strings.HasPrefix(kind, "alert.") && len(kind) > len("alert.") {
		return Levers[len(Levers)-1], true
	}
	return Lever{}, false
}

// Kinds lists every lever kind (alerts as "alert.*").
func Kinds() []string {
	out := make([]string, len(Levers))
	for i, l := range Levers {
		out[i] = l.Kind
	}
	return out
}

// Saves reports whether a kind can only spend less: the only class
// AutoApprove ever asks the hook about. Protective changes are savings
// too.
func Saves(kind string) bool {
	l, ok := LeverOf(kind)
	return ok && l.Autonomy == AutonomyAuto && l.SpendsMore == "no"
}

// IsAlert reports whether a kind is an alert.
func IsAlert(kind string) bool {
	l, ok := LeverOf(kind)
	return ok && l.Autonomy == AutonomyAlert
}

// Autonomy is the one policy: how a finding of kind is acted on for a
// brand with these economics on a network with these capabilities.
// allowed is AutoApprove's answer for the brand and kind.
//
//   - An alert is always an alert.
//   - While the brand's shadow mode is on, every other lever is shadow:
//     recorded, never applied or offered.
//   - A saving applies on its own (auto) only when allowed, which only an
//     extension's hook can say (Host.SetAdAutoApprove); otherwise it waits
//     for a person (approve), as every change does in the open engine.
//   - Anything that can spend more waits for a person.
//   - A lever the network cannot apply is approve too, so it is never
//     made automatically; rules check Allows first and don't generate it.
func Autonomy(econ Economics, caps Capabilities, kind string, allowed bool) string {
	l, ok := LeverOf(kind)
	if !ok {
		return AutonomyApprove
	}
	if l.Autonomy == AutonomyAlert {
		return AutonomyAlert
	}
	if econ.ShadowMode {
		return AutonomyShadow
	}
	if !caps.Allows(kind) {
		return AutonomyApprove
	}
	if l.Autonomy == AutonomyAuto && allowed {
		return AutonomyAuto
	}
	return AutonomyApprove
}
