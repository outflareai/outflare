package conversions

// Preset is a set of definitions a kind of business usually starts from.
// A workspace adds them as its own and changes anything; nothing reads a
// preset after that.
type Preset struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Definitions []Definition `json:"definitions"`
}

// Presets, in display order. Network events use each network's standard
// event names (Meta, TikTok, Reddit); other networks are added per
// workspace.
var Presets = []Preset{
	{ID: "saas", Name: "SaaS", Description: "Sign-ups, trials and payments, with Stripe's payment.succeeded carrying the amount.",
		Definitions: []Definition{
			{Key: "signup", Name: "Sign-up", Event: "signed_up", Attribution: true,
				NetworkEvents: map[string]string{"meta": "CompleteRegistration", "tiktok": "CompleteRegistration", "reddit": "SignUp"}},
			{Key: "trial", Name: "Trial started", Event: "trial_started", Attribution: true,
				NetworkEvents: map[string]string{"meta": "StartTrial"}},
			{Key: "payment", Name: "Payment", Event: "payment.succeeded", Attribution: true, ValueMode: ValueProperty, ValueProperty: "amount_minor", ValueUnit: "minor",
				NetworkEvents: map[string]string{"meta": "Purchase", "tiktok": "CompletePayment", "reddit": "Purchase"}},
		}},
	{ID: "booked_calls", Name: "Booked calls", Description: "Sales calls from your calendar, booking tool or CRM, booked and held, and the payments they lead to.",
		Definitions: []Definition{
			{Key: "booked_call", Name: "Booked call", Event: "meeting.booked", Attribution: true,
				NetworkEvents: map[string]string{"meta": "Schedule", "reddit": "Lead", "tiktok": "SubmitForm"}},
			{Key: "held_call", Name: "Held call", Event: "meeting.held", Attribution: true},
			{Key: "deal_won", Name: "Deal won", Event: "payment.succeeded", Attribution: true, ValueMode: ValueProperty, ValueProperty: "amount_minor", ValueUnit: "minor",
				NetworkEvents: map[string]string{"meta": "Purchase", "tiktok": "CompletePayment", "reddit": "Purchase"}},
		}},
	{ID: "ecommerce", Name: "E-commerce", Description: "Orders with their revenue, as a store's order_completed event sends them.",
		Definitions: []Definition{
			{Key: "purchase", Name: "Purchase", Event: "order_completed", Attribution: true, ValueMode: ValueProperty, ValueProperty: "revenue", ValueUnit: "major",
				NetworkEvents: map[string]string{"meta": "Purchase", "tiktok": "CompletePayment", "reddit": "Purchase"}},
			{Key: "signup", Name: "Account created", Event: "signed_up", Attribution: false,
				NetworkEvents: map[string]string{"meta": "CompleteRegistration", "tiktok": "CompleteRegistration", "reddit": "SignUp"}},
		}},
	{ID: "lead_gen", Name: "Lead generation", Description: "Form leads, and the ones sales qualifies.",
		Definitions: []Definition{
			{Key: "lead", Name: "Lead", Event: "form_submitted", Attribution: true,
				NetworkEvents: map[string]string{"meta": "Lead", "tiktok": "SubmitForm", "reddit": "Lead"}},
			{Key: "qualified_lead", Name: "Qualified lead", Event: "lead_qualified", Attribution: true},
		}},
}

// PresetOf returns the preset with id.
func PresetOf(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Presets are checked, and their defaults filled in, when the engine
// starts: a broken preset never reaches a workspace.
func init() {
	for i := range Presets {
		for j := range Presets[i].Definitions {
			if err := Presets[i].Definitions[j].Validate(); err != nil {
				panic("conversions: preset " + Presets[i].ID + "/" + Presets[i].Definitions[j].Key + ": " + err.Error())
			}
		}
	}
}
