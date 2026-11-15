package journeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/outflareai/outflare/engine/internal/emails"
)

// The outflare_action step: one API call in the journey's brand, made as
// one of the workspace's agents. Nothing here decides what is allowed:
// the call goes through the API as that agent, so its scopes, its grants
// (the action layer), every limit and the audit log apply as they do to
// the agent itself.

// AgentCaller makes an API call as one of the workspace's agents (the
// API server). path is the part after /v1/brands/{brand}/, and key makes
// a retry of the same call safe.
type AgentCaller interface {
	ActAsAgent(ctx context.Context, ws, brand, agentID, method, path string, body json.RawMessage, reason, key string) (status int, answer []byte, err error)
}

// ErrNoAgent means the step's agent can't make the call: it is gone, not
// this workspace's or brand's, or changes it makes don't go through
// approvals.
var ErrNoAgent = errors.New("the agent is not an API token of this brand with approvals on")

// actionPathOK reports whether a brand path may be called by a step: a
// relative path with no query, and never the routes that decide what an
// agent may do.
func actionPathOK(p string) string {
	switch {
	case p == "" || len(p) > 500:
		return "path is 1 to 500 characters"
	case strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.ContainsAny(p, "?#\\ \r\n"):
		return "path is the part after /v1/brands/{brand}/, such as records/{{person.id}}/notes, with no query"
	case strings.HasPrefix(p, "actions") || strings.HasPrefix(p, "action-grants"):
		return "a step can't approve changes or set what an agent may do"
	}
	return ""
}

// ActionPreset is one ready-made call for an outflare_action step: the
// flow builder lists them so nobody has to look a path up. Picking one
// fills in the step's method, path and body, which stay editable.
type ActionPreset struct {
	ID          string `json:"id"`
	Group       string `json:"group"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// The API operation it calls, as api/openapi.yaml names it.
	OperationID string         `json:"operation_id"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	Body        map[string]any `json:"body,omitempty"`
	// What the call is about, when it needs one: the builder offers the
	// brand's own to pick from and puts its id in place of Token.
	Pick *PresetPick `json:"pick,omitempty"`
}

// PresetPick is the thing a preset acts on. Token is the word in capitals
// in its path, such as CAMPAIGN_ID; From is what the builder lists:
// journey, rule, smart_list or ad_campaign.
type PresetPick struct {
	Token string `json:"token"`
	Label string `json:"label"`
	From  string `json:"from"`
}

// ActionPresets are the calls the builder offers. A test checks each
// against api/openapi.yaml.
var ActionPresets = []ActionPreset{
	{ID: "add_note", Group: "Records", Label: "Add a note to their record", OperationID: "createCRMActivity",
		Description: "Writes a note on the person or company, in their timeline.",
		Method:      "POST", Path: "crm/activities",
		Body: map[string]any{"record_id": "{{person.id}}", "kind": "note", "title": "Note from a journey", "body": "What happened, in your words."}},
	{ID: "route_record", Group: "Records", Label: "Give them an owner by the routing rules", OperationID: "routeRecord",
		Description: "Runs the brand's routing rules on the person or company. An owner already set stays.",
		Method:      "POST", Path: "records/{{person.id}}/routing",
		Body: map[string]any{"replace": false}},
	{ID: "refresh_list", Group: "Records", Label: "Refresh a smart list", OperationID: "refreshBrandSmartList",
		Description: "Works out who is on a smart list again, now.",
		Method:      "POST", Path: "smart-lists/LIST_ID/refresh",
		Pick: &PresetPick{Token: "LIST_ID", Label: "Which list", From: "smart_list"}},
	{ID: "pause_ad_campaign", Group: "Ads", Label: "Pause an ad campaign", OperationID: "pauseBrandAdCampaign",
		Description: "Pauses one campaign on its ad platform.",
		Method:      "POST", Path: "ads/campaigns/CAMPAIGN_ID/pause",
		Pick: &PresetPick{Token: "CAMPAIGN_ID", Label: "Which campaign", From: "ad_campaign"}},
	{ID: "resume_ad_campaign", Group: "Ads", Label: "Resume an ad campaign", OperationID: "resumeBrandAdCampaign",
		Description: "Turns a paused campaign back on.",
		Method:      "POST", Path: "ads/campaigns/CAMPAIGN_ID/resume",
		Pick: &PresetPick{Token: "CAMPAIGN_ID", Label: "Which campaign", From: "ad_campaign"}},
	{ID: "pause_all_ads", Group: "Ads", Label: "Pause every ad campaign", OperationID: "pauseAllAds",
		Description: "The kill switch: pauses every active campaign of the brand.",
		Method:      "POST", Path: "ads/pause-all"},
	{ID: "pause_journey", Group: "Automation", Label: "Pause another journey", OperationID: "pauseBrandJourney",
		Description: "Stops another journey taking people in and moving them on, until someone resumes it.",
		Method:      "POST", Path: "journeys/JOURNEY_ID/pause",
		Pick: &PresetPick{Token: "JOURNEY_ID", Label: "Which journey", From: "journey"}},
	{ID: "pause_rule", Group: "Automation", Label: "Pause a rule", OperationID: "pauseBrandRule",
		Description: "Stops a rule firing until someone resumes it.",
		Method:      "POST", Path: "rules/RULE_ID/pause",
		Pick: &PresetPick{Token: "RULE_ID", Label: "Which rule", From: "rule"}},
	{ID: "refresh_competition", Group: "Research", Label: "Check competitors again", OperationID: "refreshCompetition",
		Description: "Starts a fresh look at the brand's competitors.",
		Method:      "POST", Path: "competition/refresh"},
}

// unfilledRE finds a preset's word in capitals left in a path.
var unfilledRE = regexp.MustCompile(`(^|/)([A-Z]+_ID)(/|$)`)

func checkOutflareAction(st Step) string {
	if !uuidRE.MatchString(st.AgentID) {
		return "agent_id is the agent to run as: an API token with approvals on"
	}
	switch st.Method {
	case "", "POST", "PUT", "PATCH", "DELETE":
	default:
		return "method is POST, PUT, PATCH or DELETE"
	}
	if msg := actionPathOK(st.Path); msg != "" {
		return msg
	}
	if m := unfilledRE.FindStringSubmatch(st.Path); m != nil {
		return "put the id in place of " + m[2] + " in the path"
	}
	if err := emails.CheckFill(st.Path); err != nil {
		return "path: " + err.Error()
	}
	if st.Body != nil {
		if _, ok := st.Body.(map[string]any); !ok {
			return "body is a JSON object"
		}
		raw, _ := json.Marshal(st.Body)
		if len(raw) > maxBody {
			return fmt.Sprintf("body is at most %d KB", maxBody>>10)
		}
		if msg := checkBody(st.Body); msg != "" {
			return "body: " + msg
		}
	}
	if st.SaveAs != "" && !saveRE.MatchString(st.SaveAs) {
		return "save_as is lowercase letters, digits and _, starting with a letter"
	}
	return ""
}

func outflareActionCall(ctx context.Context, c *call) callResult {
	st := c.st
	if c.s.Agents == nil {
		return failed("not_configured", "Outflare actions can't run from here")
	}
	if c.j.brandID == nil {
		return failed("invalid_request", "the journey has no brand, so there is nowhere to act")
	}
	method := st.Method
	if method == "" {
		method = http.MethodPost
	}
	path, err := emails.Fill(st.Path, c.data, url.PathEscape)
	if err != nil {
		return failed("invalid_request", "path: "+err.Error())
	}
	// Checked again once filled in: a value can't steer the call elsewhere.
	if msg := actionPathOK(path); msg != "" {
		return failed("invalid_request", msg)
	}
	var body json.RawMessage
	if st.Body != nil {
		filled, err := fillBody(st.Body, c.data)
		if err != nil {
			return failed("invalid_request", "body: "+err.Error())
		}
		body, _ = json.Marshal(filled)
		if len(body) > maxBody {
			return failed("invalid_request", fmt.Sprintf("the body is over %d KB once filled in", maxBody>>10))
		}
	}
	label := st.Name
	if label == "" {
		label = st.ID
	}
	reason := "Journey step: " + label
	status, answer, err := c.s.Agents.ActAsAgent(ctx, c.ws, *c.j.brandID, st.AgentID, method, path, body, reason, c.id)
	switch {
	case errors.Is(err, ErrNoAgent):
		return failed("agent_unavailable", "the step's agent was removed or no longer asks for approval: pick another on the step")
	case err != nil:
		return retry("internal", "the call could not be made")
	}
	var parsed struct {
		ApprovalRequired bool `json:"approval_required"`
		Error            struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(answer, &parsed)
	what := method + " " + path
	switch {
	case status == http.StatusAccepted && parsed.ApprovalRequired:
		return callResult{outcome: "done", detail: what + " is waiting for a person to approve it in Agents", saved: saved(status, "application/json", answer)}
	case status >= 200 && status < 300:
		return callResult{outcome: "done", detail: fmt.Sprintf("%s answered %d", what, status), saved: saved(status, "application/json", answer)}
	case status == http.StatusForbidden && parsed.Error.Code == "action_refused":
		return failed("action_refused", "the agent's grants refuse "+what)
	case status == http.StatusTooManyRequests || status >= 500:
		return retry("action_failed", fmt.Sprintf("%s answered %d", what, status))
	}
	code := parsed.Error.Code
	if code == "" {
		code = "action_failed"
	}
	return failed(code, fmt.Sprintf("%s answered %d (%s)", what, status, code))
}
