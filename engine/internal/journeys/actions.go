package journeys

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

// Actions are the journey steps that do something other than email, wait
// or branch: change the person's record, move them between lists,
// journeys and sequences, suppress them, or tell someone. Each is one
// entry here, so the API, MCP and the flow builder pick up a new one from
// StepKinds without code of their own.
//
// Every action runs in its run's transaction, so it happens once even when
// the job is retried. It reports done, skipped (with why) or failed; a
// failed step does what its on_error says.

// FieldWriter changes a record's field with the API's checks, history and
// source precedence (the API server provides it).
type FieldWriter interface {
	WriteField(ctx context.Context, tx pgx.Tx, ws, recordID, field, change string, value any, overwriteManual bool, ref string) (outcome, detail string, err error)
}

// StepKind is a kind of step as the step-kinds catalog shows it.
type StepKind struct {
	Kind        string        `json:"kind"`
	Group       string        `json:"group"`
	Label       string        `json:"label"`
	Description string        `json:"description"`
	Requires    []Requirement `json:"requires"`
	// The settings the step takes, as JSON Schema.
	Settings map[string]any `json:"settings_schema"`
	// The step fields that name the step after it, for drawing the flow:
	// next; then and else; paths[].next for a list of paths.
	Outputs []string `json:"outputs"`
	// The field naming the step to go to when it fails, if it can fail.
	ErrorOutput string `json:"error_output,omitempty"`
}

// Requirement is something a step needs the workspace to have connected.
type Requirement struct {
	Connection string `json:"connection"`
	Label      string `json:"label"`
}

// actionDef is an action step: what it shows, checks and does.
type actionDef struct {
	StepKind
	check func(st Step) string
	run   func(ctx context.Context, x *act) (outcome, detail string, err error)
	// call, instead of run, is a step that calls out (a webhook, Slack):
	// it runs in a job of its own after the run's transaction, tried
	// again on errors that can pass, attempts times at most.
	call     func(ctx context.Context, c *call) callResult
	attempts int
}

// act is one action step being run for one person.
type act struct {
	s   *Service
	tx  pgx.Tx
	ws  string
	j   *journey
	r   run
	st  *Step
	ref string // the run, for logs and audit data
}

func (x *act) record(action, targetType, targetID string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	data["via"], data["journey_id"], data["run_id"] = "journey", x.j.id, x.r.id
	return audit.Record(context.Background(), x.tx, audit.Event{WorkspaceID: x.ws, ActorType: "system", Action: action,
		TargetType: targetType, TargetID: targetID, Data: data})
}

var (
	uuidRE  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	topicRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

func schema(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// ref is the id of something in the workspace. x-outflare-picker tells the
// flow builder what to offer: static_list, journey or sequence.
func ref(picker, desc string) map[string]any {
	return map[string]any{"type": "string", "format": "uuid", "description": desc, "x-outflare-picker": picker}
}
func enum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

var next = []string{"next"}

// conditionSchema is one branch condition, as JSON Schema.
var conditionSchema = schema(map[string]any{
	"attribute": str("A field on the person, such as plan."), "property": str("A property of the event that started the run."),
	"event": str("Whether they did this event since the run started."), "email": enum("Whether an earlier email step's email was", "delivered", "opened", "clicked"),
	"var":  str("Something an earlier step saved, such as billing.status for a webhook's answer saved as billing."),
	"step": str("The email step an email condition is about."), "op": enum("The test", "eq", "neq", "gt", "lt", "contains", "exists", "not_exists"),
	"value": map[string]any{"description": "What eq, neq, gt, lt and contains compare with."},
}, "op")

// builtinKinds are the steps journeys always had, in the same shape.
var builtinKinds = []StepKind{
	{Kind: KindEmail, Group: "messaging", Label: "Send an email", Description: "Send a published lifecycle template. Topic, consent and contact rules apply; a person who can't get it is skipped and goes on.",
		Requires: []Requirement{{Connection: "email_provider", Label: "An email provider (Lifecycle, Email providers)"}},
		Settings: schema(map[string]any{"template": str("A lifecycle template's handle or id.")}, "template"), Outputs: next},
	{Kind: KindWait, Group: "flow", Label: "Wait", Description: "Wait a while before the next step.", Requires: []Requirement{},
		Settings: schema(map[string]any{"wait_seconds": map[string]any{"type": "integer", "minimum": 60, "maximum": 7776000}}, "wait_seconds"), Outputs: next},
	{Kind: KindBranch, Group: "flow", Label: "Branch", Description: "Go one way when the conditions hold and another when they don't.", Requires: []Requirement{},
		Settings: schema(map[string]any{"conditions": map[string]any{"type": "array", "maxItems": 20, "items": conditionSchema},
			"match": enum("all (the default) or any", "all", "any")}, "conditions"), Outputs: []string{"then", "else"}},
	{Kind: KindExit, Group: "flow", Label: "Exit", Description: "End the run here.", Requires: []Requirement{}, Settings: schema(map[string]any{}), Outputs: []string{}},
}

// Actions are the action steps, in the order the step picker lists them.
var Actions = []actionDef{
	{StepKind: StepKind{Kind: "set_field", Group: "data", Label: "Update a field",
		Description: "Set, clear or add to a field on the person. It never overwrites a value a person set by hand unless overwrite_manual is on.",
		Settings: schema(map[string]any{"field": str("The field's key, such as lifecycle_stage."), "change": enum("set (the default), clear or increment", "set", "clear", "increment"),
			"value": map[string]any{"description": "The new value, or {{event.x}}, {{person.x}} or {{vars.x}} to copy one."}, "overwrite_manual": map[string]any{"type": "boolean"}}, "field")},
		check: func(st Step) string {
			switch {
			case !fieldRE.MatchString(st.Field) || strings.Contains(st.Field, "."):
				return "field is a field's key, such as lifecycle_stage"
			case st.Change != "" && st.Change != "set" && st.Change != "clear" && st.Change != "increment":
				return "change is set, clear or increment"
			case st.Change != "clear" && st.Value == nil:
				return "value is required"
			}
			return ""
		},
		run: func(ctx context.Context, x *act) (string, string, error) {
			return x.s.writeField(ctx, x, x.st.Field, x.st.Change, x.value(), x.st.OverwriteManual)
		}},
	{StepKind: StepKind{Kind: "add_tag", Group: "data", Label: "Add a tag", Description: "Put a tag such as churn-risk on the person.",
		Settings: schema(map[string]any{"tag": str("The tag: lowercase, up to 50 characters.")}, "tag")},
		check: checkTag,
		run: func(ctx context.Context, x *act) (string, string, error) {
			return x.s.writeField(ctx, x, "tags", "add", x.st.Tag, true)
		}},
	{StepKind: StepKind{Kind: "remove_tag", Group: "data", Label: "Remove a tag", Description: "Take a tag off the person.",
		Settings: schema(map[string]any{"tag": str("The tag.")}, "tag")},
		check: checkTag,
		run: func(ctx context.Context, x *act) (string, string, error) {
			return x.s.writeField(ctx, x, "tags", "remove", x.st.Tag, true)
		}},
	{StepKind: StepKind{Kind: "add_to_list", Group: "data", Label: "Add to a list", Description: "Put the person in a static list.",
		Settings: schema(map[string]any{"list_id": ref("static_list", "A static list's id.")}, "list_id")},
		check: needID("list_id", func(st Step) string { return st.ListID }),
		run:   func(ctx context.Context, x *act) (string, string, error) { return x.list(ctx, true) }},
	{StepKind: StepKind{Kind: "remove_from_list", Group: "data", Label: "Remove from a list", Description: "Take the person out of a static list.",
		Settings: schema(map[string]any{"list_id": ref("static_list", "A static list's id.")}, "list_id")},
		check: needID("list_id", func(st Step) string { return st.ListID }),
		run:   func(ctx context.Context, x *act) (string, string, error) { return x.list(ctx, false) }},
	{StepKind: StepKind{Kind: "enter_journey", Group: "cross_campaign", Label: "Start another journey",
		Description: "Put the person into another active journey. Its entry rule still applies.",
		Settings:    schema(map[string]any{"journey_id": ref("journey", "The journey to start.")}, "journey_id")},
		check: needID("journey_id", func(st Step) string { return st.JourneyID }),
		run:   enterJourney},
	{StepKind: StepKind{Kind: "exit_journey", Group: "cross_campaign", Label: "Take out of other journeys",
		Description: "End the person's run in another journey (scope journey), or in every other journey (scope others).",
		Settings:    schema(map[string]any{"scope": enum("journey (the default) or others", "journey", "others"), "journey_id": ref("journey", "For scope journey.")})},
		check: func(st Step) string {
			switch st.Scope {
			case "", "journey":
				if !uuidRE.MatchString(st.JourneyID) {
					return "journey_id is required for scope journey"
				}
			case "others":
			default:
				return "scope is journey or others"
			}
			return ""
		},
		run: exitJourney},
	{StepKind: StepKind{Kind: "enroll_in_sequence", Group: "cross_campaign", Label: "Add to a sequence",
		Description: "Add the person to a cold-email sequence, through their lead (made from their verified email when they have none). Suppressed people, and by default people already in another sequence, are skipped.",
		Settings: schema(map[string]any{"sequence_id": ref("sequence", "The sequence."),
			"allow_elsewhere": map[string]any{"type": "boolean", "description": "Enroll them even when they are active in another sequence."}}, "sequence_id")},
		check: needID("sequence_id", func(st Step) string { return st.SequenceID }),
		run:   enrollInSequence},
	{StepKind: StepKind{Kind: "stop_sequences", Group: "cross_campaign", Label: "Stop sequences",
		Description: "Stop the person's cold email: in one sequence, in every sequence, or for everyone at their company.",
		Settings: schema(map[string]any{"scope": enum("sequence, all (the default) or company", "sequence", "all", "company"),
			"sequence_id": ref("sequence", "For scope sequence.")})},
		check: func(st Step) string {
			switch st.Scope {
			case "sequence":
				if !uuidRE.MatchString(st.SequenceID) {
					return "sequence_id is required for scope sequence"
				}
			case "", "all", "company":
			default:
				return "scope is sequence, all or company"
			}
			return ""
		},
		run: stopSequences},
	{StepKind: StepKind{Kind: "suppress", Group: "cross_campaign", Label: "Suppress",
		Description: "Add the person's email addresses (target email) or their work domain (target domain) to the suppression list, so nothing is sent to them again. A journey can't undo it.",
		Settings:    schema(map[string]any{"target": enum("email (the default) or domain", "email", "domain")})},
		check: func(st Step) string {
			if st.Target != "" && st.Target != "email" && st.Target != "domain" {
				return "target is email or domain"
			}
			return ""
		},
		run: suppress},
	{StepKind: StepKind{Kind: "unsubscribe_topic", Group: "cross_campaign", Label: "Unsubscribe from a topic",
		Description: "Withdraw the person's consent to an email topic, or to all marketing email when topic is empty.",
		Settings:    schema(map[string]any{"topic": str("An email topic's key; empty for all marketing email.")})},
		check: func(st Step) string {
			if st.Topic != "" && !topicRE.MatchString(st.Topic) {
				return "topic is an email topic's key"
			}
			return ""
		},
		run: unsubscribeTopic},
	{StepKind: StepKind{Kind: "transactional_email", Group: "messaging", Label: "Send a transactional email",
		Description: "Send a published transactional template, such as an account notice. Marketing consent, topics and the frequency cap don't apply; the suppression list does. For marketing email, use Send an email.",
		Requires:    []Requirement{{Connection: "email_provider", Label: "An email provider (Lifecycle, Email providers)"}},
		Settings:    schema(map[string]any{"template": str("A transactional template's handle or id.")}, "template")},
		check: checkTransactional,
		run:   sendTransactional},
	{StepKind: StepKind{Kind: "set_consent", Group: "data", Label: "Record consent",
		Description: "Record that the person agreed to (granted) or declined (withdrawn) an email topic, or all marketing email when topic is empty, with this run as the evidence. Use it when they opted in somewhere Outflare doesn't see, such as a checkbox your app reports in an event.",
		Settings: schema(map[string]any{"consent": enum("granted or withdrawn", "granted", "withdrawn"),
			"topic": str("An email topic's key; empty for all marketing email.")}, "consent")},
		check: checkConsent,
		run:   setConsent},
	{StepKind: StepKind{Kind: "assign_owner", Group: "data", Label: "Assign an owner",
		Description: "Make a workspace member the person's owner: one member, or of several the one who owns the fewest people now, so new people are spread evenly. Someone who already has an owner keeps them unless replace_owner is on.",
		Settings: schema(map[string]any{"owner_id": map[string]any{"type": "string", "format": "uuid", "description": "One member's user id.", "x-outflare-picker": "member"},
			"members":       map[string]any{"type": "array", "minItems": 2, "maxItems": 50, "items": map[string]any{"type": "string", "format": "uuid", "x-outflare-picker": "member"}, "description": "Members to spread people between evenly."},
			"replace_owner": map[string]any{"type": "boolean", "description": "Change an owner already set (off: keep it)."}})},
		check: checkOwner,
		run:   assignOwner},
	{StepKind: StepKind{Kind: "move_deal", Group: "data", Label: "Open or move a deal",
		Description: "Move the person's newest open deal forward to a stage, or open a deal there when they have none, as reply rules do. A deal only moves forward, never over a stage a person set, and never out of a closed stage.",
		Settings: schema(map[string]any{"deal": schema(map[string]any{
			"type":   str("The record type of deals, such as deal."),
			"field":  str("Its status field that holds the stage, such as deal_stage."),
			"stage":  str("The stage to move to: one of that field's options."),
			"closed": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Final stages (won, lost): a deal in one is never moved, and a new one is opened instead."},
		}, "type", "field", "stage")}, "deal")},
		check: checkDealMove,
		run:   moveDeal},
	{StepKind: StepKind{Kind: "alert", Group: "notify", Label: "Alert",
		Description: "Raise an alert about the person: to their owner (to owner, the default) or to the owners and admins. It is emailed or sent to Slack as the workspace's alert settings say.",
		Settings: schema(map[string]any{"title": str("What the alert says, up to 120 characters."), "text": str("A line of detail, up to 500 characters."),
			"to": enum("owner (the default) or admins", "owner", "admins")}, "title")},
		check: func(st Step) string {
			switch {
			case strings.TrimSpace(st.Title) == "" || len(st.Title) > 120:
				return "title is 1 to 120 characters"
			case len(st.Text) > 500:
				return "text is at most 500 characters"
			case st.To != "" && st.To != "owner" && st.To != "admins":
				return "to is owner or admins"
			}
			return ""
		},
		run: raiseAlert},
	{StepKind: StepKind{Kind: "slack", Group: "notify", Label: "Post to Slack",
		Description: "Post a message to a channel in the workspace's own Slack. {{person.x}}, {{event.x}} and {{vars.x}} fill in. Tried again for about a minute when Slack is busy.",
		Requires:    []Requirement{{Connection: "slack", Label: "Slack (Settings, Connections)"}},
		Settings: schema(map[string]any{"channel": map[string]any{"type": "string", "description": "The channel's id, from the connection's channel list.", "x-outflare-picker": "slack_channel"},
			"text": str("The message, up to 3,000 characters."), "title": str("A bold line above it, up to 120 characters; optional."),
			"connection_id": map[string]any{"type": "string", "format": "uuid", "description": "Which Slack connection; the workspace's first when empty.", "x-outflare-picker": "connection"}}, "channel", "text")},
		check:    checkSlack,
		call:     slackCall,
		attempts: 5},
	{StepKind: StepKind{Kind: "webhook", Group: "integrations", Label: "Call a webhook",
		Description: "Call any URL: your own system, Zapier, n8n or another tool's API. {{person.x}}, {{event.x}} and {{vars.x}} fill in the URL, headers and body. Every call is signed with the workspace's signing secret; auth adds the receiver's own key. Tried again for about 40 minutes on 5xx, 429 or no answer; the answer can be saved for later steps.",
		Settings: schema(map[string]any{
			"method":  enum("POST (the default), GET, PUT, PATCH or DELETE", "POST", "GET", "PUT", "PATCH", "DELETE"),
			"url":     str("An https URL, up to 2,000 characters."),
			"headers": map[string]any{"type": "object", "maxProperties": 20, "additionalProperties": map[string]any{"type": "string"}, "description": "Headers anyone who can see the journey can read; put keys in auth."},
			"body":    map[string]any{"description": "The JSON body. A string that is only {{x}} copies the value as it is (a number stays a number)."},
			"auth": schema(map[string]any{"type": enum("none (the default), header, basic or query", "none", "header", "basic", "query"),
				"name":     str("The header (header) or URL parameter (query) the key goes in."),
				"username": str("For basic: the user name; the key is the password.")}, "type"),
			"save_as": str("Keep the answer in vars.<save_as> as {status, body}: lowercase letters, digits and _."),
		}, "url")},
		check:    checkWebhook,
		call:     webhookCall,
		attempts: 10},
	{StepKind: StepKind{Kind: "push_to_sequencer", Group: "integrations", Label: "Add to a sequencer campaign",
		Description: "Add the person to the Smartlead, Instantly or EmailBison campaign a sequence is linked to, through the workspace's own connection; the sequencer sends from there. Someone suppressed, without a verified email, already in the campaign or left out by the sequencer is skipped and goes on. Tried again for about 40 minutes when the sequencer is busy.",
		Requires:    []Requirement{{Connection: "sequencer", Label: "Smartlead, Instantly or EmailBison, with a campaign linked to the sequence (Settings, Connections)"}},
		Settings: schema(map[string]any{
			"sequence_id":     ref("sequence", "A sequence linked to a campaign in a connected sequencer."),
			"allow_elsewhere": map[string]any{"type": "boolean", "description": "Add them even when they are in another of the sequencer's campaigns (Smartlead and Instantly); off by default."},
			"variables": map[string]any{"type": "object", "maxProperties": 20, "additionalProperties": map[string]any{"type": "string"},
				"description": "The campaign's own variables, by their name there, such as {\"plan\": \"{{person.plan}}\"}. EmailBison needs them made there first."},
		}, "sequence_id")},
		check:    checkPush,
		call:     pushCall,
		attempts: 10},
	{StepKind: StepKind{Kind: "push_to_crm", Group: "integrations", Label: "Create or update in your CRM",
		Description: "Create the person in your HubSpot, or update the contact with their email: their name, company and website, and any properties you fill from {{person.x}}, {{event.x}} and {{vars.x}}. Through the workspace's own connection; deals aren't touched. Someone without a verified email is skipped. Tried again for about 40 minutes when the CRM is busy; the contact's id can be saved for later steps.",
		Requires:    []Requirement{{Connection: "crm", Label: "HubSpot (Settings, Connections)"}},
		Settings: schema(map[string]any{
			"properties": map[string]any{"type": "object", "maxProperties": 30, "additionalProperties": map[string]any{"type": "string"},
				"description": "The CRM's own properties by their internal names, such as {\"lifecyclestage\": \"lead\", \"plan\": \"{{person.plan}}\"}. A property the CRM doesn't have fails the step."},
			"connection_id": map[string]any{"type": "string", "format": "uuid", "description": "Which CRM connection; the workspace's first that can create contacts when empty.", "x-outflare-picker": "connection"},
			"save_as":       str("Keep {id, created} in vars.<save_as>: lowercase letters, digits and _."),
		})},
		check:    checkCRMPush,
		call:     crmPushCall,
		attempts: 10},
	{StepKind: StepKind{Kind: "sync_audience", Group: "integrations", Label: "Add to an ad audience",
		Description: "Add the person to a customer list audience on one of your ad accounts (a Meta custom audience, a Google Ads Customer Match list), or take them out, through the workspace's own connection. The platform gets only their hashed emails and phone numbers. Adding follows your ad consent setting (Paid ads, Settings), for ad data and ad personalisation; someone without consent, or without an email or phone number, is skipped and goes on. Tried again for about 40 minutes when the platform is busy.",
		Requires:    []Requirement{{Connection: "ad_audiences", Label: "Meta Ads or Google Ads (Paid ads)"}},
		Settings: schema(map[string]any{
			"ad_account_id": map[string]any{"type": "string", "format": "uuid", "description": "One of the workspace's ad accounts, from Paid ads.", "x-outflare-picker": "ad_account"},
			"audience_id":   map[string]any{"type": "string", "description": "A customer list audience on that ad account, from GET .../ads/audiences.", "x-outflare-picker": "ad_audience"},
			"change":        enum("add (the default) or remove", "add", "remove"),
		}, "ad_account_id", "audience_id")},
		check:    checkAudience,
		call:     audienceCall,
		attempts: 10},
	{StepKind: StepKind{Kind: "ai", Group: "ai", Label: "Ask AI",
		Description: "Ask your workspace's own AI model about the person: pick one of a few choices (to sort them) or write a short text. {{person.x}}, {{event.x}} and {{vars.x}} fill in the prompt. The answer is kept in vars.<save_as>.answer for a branch, an email or a webhook. Uses your own key and AI spend limits.",
		Requires:    []Requirement{{Connection: "ai_model", Label: "An AI model (Settings, AI model)"}},
		Settings: schema(map[string]any{
			"prompt": map[string]any{"type": "string", "maxLength": maxPrompt,
				"description": "What to ask, such as: Is {{person.company}} a good fit for our product? {{person.job_title}} at a {{person.company_size}} company."},
			"choices":   map[string]any{"type": "array", "minItems": 2, "maxItems": 20, "items": map[string]any{"type": "string", "maxLength": 100}, "description": "The only answers allowed, to sort people (such as hot, warm, cold). Leave out for a written answer."},
			"max_words": map[string]any{"type": "integer", "minimum": 1, "maximum": 300, "description": "For a written answer: at most this many words (100 when left out)."},
			"save_as":   str("Keep the answer in vars.<save_as> as {answer}: lowercase letters, digits and _."),
		}, "prompt", "save_as")},
		check:    checkAI,
		call:     aiCall,
		attempts: 5},
}

func init() {
	for i := range Actions {
		a := &Actions[i]
		if a.Requires == nil {
			a.Requires = []Requirement{}
		}
		a.Outputs, a.ErrorOutput = next, "on_error"
	}
	alerts.Register(alerts.Kind{ID: AlertKind, Title: "Journey alert",
		Description: "A journey's alert step: something about a person that its journey says someone should know.",
		Scope:       "emails.read", Describe: describeAlert})
}

// AlertKind is the kind of alert a journey's alert step raises.
const AlertKind = "journey.alert"

func action(kind string) *actionDef {
	for i := range Actions {
		if Actions[i].Kind == kind {
			return &Actions[i]
		}
	}
	return nil
}

// StepKinds is every kind of step, for the step picker.
func StepKinds() []StepKind {
	out := slices.Clone(builtinKinds)
	for _, a := range Actions {
		out = append(out, a.StepKind)
	}
	return out
}

// KindNames is every step kind's name.
func KindNames() []string {
	out := []string{}
	for _, k := range StepKinds() {
		out = append(out, k.Kind)
	}
	return out
}

func checkTag(st Step) string {
	t := strings.TrimSpace(st.Tag)
	if t == "" || len([]rune(t)) > 50 {
		return "tag is 1 to 50 characters"
	}
	return ""
}

func needID(name string, get func(Step) string) func(Step) string {
	return func(st Step) string {
		if !uuidRE.MatchString(get(st)) {
			return name + " must be an id"
		}
		return ""
	}
}

// value is the step's value, with {{event.x}}, {{person.x}} or {{vars.x}}
// copied from the run's event, the person's fields or what an earlier step
// saved.
func (x *act) value() any {
	s, ok := x.st.Value.(string)
	if !ok || !strings.HasPrefix(s, "{{") || !strings.HasSuffix(s, "}}") {
		return x.st.Value
	}
	path := strings.TrimSpace(s[2 : len(s)-2])
	switch {
	case strings.HasPrefix(path, "event."):
		v, _ := lookup(x.r.context, strings.TrimPrefix(path, "event."))
		return v
	case strings.HasPrefix(path, "vars."):
		v, _ := lookup(x.r.vars, strings.TrimPrefix(path, "vars."))
		return v
	case strings.HasPrefix(path, "person."):
		attrs, err := attributes(context.Background(), x.tx, x.ws, x.r.recordID)
		if err != nil {
			return nil
		}
		v, _ := lookup(attrs, strings.TrimPrefix(path, "person."))
		return v
	}
	return x.st.Value
}

func (s *Service) writeField(ctx context.Context, x *act, field, change string, value any, overwrite bool) (string, string, error) {
	if s.Fields == nil {
		return "failed", "fields can't be written here", nil
	}
	if change != "clear" && change != "remove" && value == nil {
		return "skipped", "no value to set", nil
	}
	return s.Fields.WriteField(ctx, x.tx, x.ws, x.r.recordID, field, change, value, overwrite, x.r.id)
}

func (x *act) list(ctx context.Context, add bool) (string, string, error) {
	var c smartlists.Change
	var err error
	if add {
		c, err = smartlists.AddStatic(ctx, x.tx, x.ws, x.st.ListID, []string{x.r.recordID}, "journey", x.r.id)
	} else {
		c, err = smartlists.RemoveStatic(ctx, x.tx, x.ws, x.st.ListID, []string{x.r.recordID})
	}
	switch {
	case errors.Is(err, smartlists.ErrGone):
		return "failed", "the list no longer exists", nil
	case errors.Is(err, smartlists.ErrNotStatic):
		return "failed", "the list is a smart list, which fills itself from its rules", nil
	case err != nil:
		return "", "", err
	case c.Changed == 0 && add:
		return "skipped", "already in the list, or not a person this list can hold", nil
	case c.Changed == 0:
		return "skipped", "not in the list", nil
	}
	action := "smart_list.members_added"
	if !add {
		action = "smart_list.members_removed"
	}
	return "done", "", x.record(action, "smart_list", x.st.ListID, map[string]any{"count": 1, "members": c.Members})
}

func enterJourney(ctx context.Context, x *act) (string, string, error) {
	if x.st.JourneyID == x.j.id {
		return "failed", "a journey can't start itself", nil
	}
	runID, why, err := Enroll(ctx, x.tx, x.ws, x.st.JourneyID, x.r.recordID, nil, x.r.context, true)
	if err != nil || why != "" {
		return "skipped", why, err
	}
	return "done", "", record(ctx, x.tx, x.ws, "journey_run.entered", x.st.JourneyID, map[string]any{"via": "journey", "from_journey_id": x.j.id, "run_id": runID})
}

func exitJourney(ctx context.Context, x *act) (string, string, error) {
	rows, err := x.tx.Query(ctx, `UPDATE journey_runs SET status = 'exited', exit_reason = 'journey', finished_at = now(), next_at = NULL, updated_at = now()
		WHERE workspace_id = $1 AND record_id = $2 AND status = 'active' AND journey_id <> $3 AND ($4 = 'others' OR journey_id::text = $5)
		RETURNING journey_id::text`, x.ws, x.r.recordID, x.j.id, x.st.Scope, x.st.JourneyID)
	if err != nil {
		return "", "", err
	}
	ended, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", "", err
	}
	if len(ended) == 0 {
		return "skipped", "not in another journey", nil
	}
	for _, jid := range ended {
		if err := record(ctx, x.tx, x.ws, "journey_run.exited", jid, map[string]any{"reason": "journey", "from_journey_id": x.j.id}); err != nil {
			return "", "", err
		}
	}
	return "done", fmt.Sprintf("left %d", len(ended)), nil
}

// personLead is the person's lead, made from their verified email when
// they have none.
func personLead(ctx context.Context, tx pgx.Tx, ws, recordID string) (string, string, error) {
	var leadID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM leads WHERE workspace_id = $1 AND record_id = $2 ORDER BY created_at LIMIT 1`, ws, recordID).Scan(&leadID)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return leadID, "", err
	}
	var email, first, last string
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT value FROM record_identities WHERE record_id = r.id AND namespace = 'email' AND verified AND valid_to IS NULL
			ORDER BY valid_from, id LIMIT 1), ''), COALESCE(r.attributes->>'first_name', ''), COALESCE(r.attributes->>'last_name', '')
		FROM records r WHERE r.workspace_id = $1 AND r.id = $2`, ws, recordID).Scan(&email, &first, &last)
	if err != nil {
		return "", "", err
	}
	if email == "" {
		return "", "no lead and no verified email to make one from", nil
	}
	if _, err := leads.Import(ctx, tx, ws, "journey", nil, []leads.Lead{{Email: email, FirstName: first, LastName: last}}); err != nil {
		return "", "", err
	}
	err = tx.QueryRow(ctx, `SELECT id::text FROM leads WHERE workspace_id = $1 AND lower(email) = lower($2)`, ws, email).Scan(&leadID)
	return leadID, "", err
}

func enrollInSequence(ctx context.Context, x *act) (string, string, error) {
	var status string
	var linked bool
	err := x.tx.QueryRow(ctx, `SELECT status, EXISTS (SELECT 1 FROM campaign_links l WHERE l.sequence_id = s.id) FROM sequences s
		WHERE s.workspace_id = $1 AND s.id = $2`, x.ws, x.st.SequenceID).Scan(&status, &linked)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "failed", "the sequence no longer exists", nil
	case err != nil:
		return "", "", err
	case status == "archived":
		return "failed", "the sequence is archived", nil
	case linked:
		return "failed", "the sequence runs in a connected sequencer; use a push_to_sequencer step", nil
	}
	leadID, why, err := personLead(ctx, x.tx, x.ws, x.r.recordID)
	if err != nil || why != "" {
		return "skipped", why, err
	}
	var here, suppressed, excluded, elsewhere bool
	if err := x.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM enrollments WHERE sequence_id = $1 AND lead_id = $2),
			EXISTS (`+leads.SuppressedSQL("l.workspace_id", "l.email")+`),
			`+customers.ExcludedByColumnSQL("$3::uuid", "(SELECT q.exclude FROM sequences q WHERE q.id = $1)")+`,
			EXISTS (SELECT 1 FROM enrollments e JOIN sequences q ON q.id = e.sequence_id WHERE e.lead_id = l.id AND e.status = 'active'
				AND e.sequence_id <> $1 AND q.status IN ('active', 'paused'))
		FROM leads l WHERE l.id = $2`, x.st.SequenceID, leadID, x.r.recordID).Scan(&here, &suppressed, &excluded, &elsewhere); err != nil {
		return "", "", err
	}
	switch {
	case here:
		return "skipped", "already in the sequence", nil
	case suppressed:
		return "skipped", "suppressed", nil
	case excluded:
		metrics.Excluded("sequence")
		return "skipped", "excluded by the sequence's exclusion", nil
	case elsewhere && !x.st.AllowElsewhere:
		return "skipped", "active in another sequence", nil
	}
	if _, err := x.tx.Exec(ctx, `INSERT INTO enrollments (workspace_id, sequence_id, lead_id, next_send_at) VALUES ($1, $2, $3, now()) ON CONFLICT DO NOTHING`,
		x.ws, x.st.SequenceID, leadID); err != nil {
		return "", "", err
	}
	return "done", "", x.record("sequence.leads_enrolled", "sequence", x.st.SequenceID, map[string]any{"enrolled": 1, "suppressed": 0, "active_elsewhere": 0})
}

func stopSequences(ctx context.Context, x *act) (string, string, error) {
	// Whose cold email stops: the person's leads, or for company everyone
	// linked to one of their companies.
	people := `SELECT $2::uuid`
	if x.st.Scope == "company" {
		people = `SELECT $2::uuid UNION SELECT p.from_id FROM record_relationships p
			JOIN record_relationships me ON me.to_id = p.to_id AND me.from_id = $2 AND me.kind = 'companies' AND me.valid_to IS NULL
			WHERE p.kind = 'companies' AND p.valid_to IS NULL AND p.workspace_id = $1`
	}
	rows, err := x.tx.Query(ctx, `UPDATE enrollments e SET status = 'stopped', status_reason = 'stopped by a journey', next_send_at = NULL, updated_at = now()
		FROM leads l WHERE l.id = e.lead_id AND e.workspace_id = $1 AND e.status = 'active' AND l.record_id IN (`+people+`)
		  AND ($3 <> 'sequence' OR e.sequence_id::text = $4)
		RETURNING e.id::text`, x.ws, x.r.recordID, x.st.Scope, x.st.SequenceID)
	if err != nil {
		return "", "", err
	}
	stopped, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", "", err
	}
	if len(stopped) == 0 {
		return "skipped", "no active sequence", nil
	}
	for _, eid := range stopped {
		if err := x.record("enrollment.stopped", "enrollment", eid, map[string]any{"scope": x.st.Scope}); err != nil {
			return "", "", err
		}
	}
	return "done", fmt.Sprintf("stopped %d", len(stopped)), nil
}

func suppress(ctx context.Context, x *act) (string, string, error) {
	rows, err := x.tx.Query(ctx, `SELECT value FROM record_identities WHERE workspace_id = $1 AND record_id = $2 AND namespace = 'email' AND valid_to IS NULL`,
		x.ws, x.r.recordID)
	if err != nil {
		return "", "", err
	}
	addrs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", "", err
	}
	var entries []leads.Suppression
	intel := classify.Builtin()
	for _, a := range addrs {
		a = strings.ToLower(a)
		if x.st.Target != "domain" {
			entries = append(entries, leads.Suppression{Kind: "email", Value: a})
			continue
		}
		// A free mailbox's domain is everyone's: only work domains.
		if at := strings.LastIndexByte(a, '@'); at >= 0 {
			if d := a[at+1:]; !intel.MailboxProvider(d) && !slices.ContainsFunc(entries, func(e leads.Suppression) bool { return e.Value == d }) {
				entries = append(entries, leads.Suppression{Kind: "domain", Value: d})
			}
		}
	}
	if len(entries) == 0 {
		return "skipped", "no email address or work domain", nil
	}
	n, err := leads.Suppress(ctx, x.tx, x.ws, leads.Added{Reason: "do_not_contact", Source: "journey", Evidence: map[string]any{"journey_id": x.j.id, "run_id": x.r.id}}, entries)
	if err != nil {
		return "", "", err
	}
	if n == 0 {
		return "skipped", "already suppressed", nil
	}
	return "done", "", x.record("suppressions.added", "workspace", x.ws, map[string]any{"count": n, "kind": entries[0].Kind, "source": "journey"})
}

func unsubscribeTopic(ctx context.Context, x *act) (string, string, error) {
	if x.st.Topic != "" {
		var ok bool
		if err := x.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM email_topics WHERE workspace_id = $1 AND key = $2)`, x.ws, x.st.Topic).Scan(&ok); err != nil {
			return "", "", err
		}
		if !ok {
			return "failed", "no email topic " + x.st.Topic, nil
		}
	}
	status, _, err := customers.Consent(ctx, x.tx, x.ws, x.r.recordID, "email", x.st.Topic, "marketing")
	if err != nil {
		return "", "", err
	}
	if status == customers.Withdrawn {
		return "skipped", "already unsubscribed", nil
	}
	return "done", "", customers.RecordConsent(ctx, x.tx, x.ws, customers.ConsentChange{RecordID: x.r.recordID, Channel: "email", Topic: x.st.Topic,
		Status: customers.Withdrawn, Source: "journey", Evidence: map[string]any{"journey_id": x.j.id, "run_id": x.r.id}})
}

func raiseAlert(ctx context.Context, x *act) (string, string, error) {
	var owner *string
	if x.st.To != "admins" {
		_ = x.tx.QueryRow(ctx, `SELECT attributes->>'owner' FROM records WHERE workspace_id = $1 AND id = $2`, x.ws, x.r.recordID).Scan(&owner)
	}
	rec := x.r.recordID
	id, err := alerts.Raise(ctx, x.tx, alerts.Alert{WorkspaceID: x.ws, BrandID: x.j.brandID, Kind: AlertKind, DedupeKey: x.r.id + ":" + x.st.ID,
		TargetType: "journey", TargetID: x.j.id, RecordID: &rec, OwnerID: owner, Data: map[string]any{"journey_id": x.j.id, "step": x.st.ID, "run_id": x.r.id}})
	if err != nil {
		return "", "", err
	}
	if id == "" {
		return "skipped", "already raised for this run", nil
	}
	return "done", "", nil
}

// describeAlert reads a journey alert's title and text from its step, so
// the alert keeps no copy of them.
func describeAlert(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	jid, _ := a.Data["journey_id"].(string)
	step, _ := a.Data["step"].(string)
	v := alerts.View{Title: "Journey alert", Path: "journeys/" + jid}
	j, err := loadJourney(ctx, q, a.WorkspaceID, jid, false)
	if err != nil || j == nil {
		return v, err
	}
	if st := j.step(step); st != nil {
		v.Title, v.Text = st.Title, st.Text
	}
	if a.RecordID != nil {
		var name string
		_ = q.QueryRow(ctx, `SELECT name FROM records WHERE id = $1`, *a.RecordID).Scan(&name)
		if name != "" {
			v.Text = strings.TrimSpace(name + ". " + v.Text)
		}
	}
	return v, nil
}
