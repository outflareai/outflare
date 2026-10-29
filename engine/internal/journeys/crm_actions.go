package journeys

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
)

// More action steps: a transactional email, consent, the record's owner,
// and the person's deal.

// DealMove is a move_deal step's deal: the record type of deals, its
// status field that holds the stage, the stage to move to, and the final
// stages (won, lost) a deal is never moved out of. The same shape as a
// reply rule's deal move.
type DealMove struct {
	Type   string   `json:"type"`
	Field  string   `json:"field"`
	Stage  string   `json:"stage"`
	Closed []string `json:"closed,omitempty"`
}

// DealMover moves deals for move_deal steps (the API server, which knows
// the catalog). It moves the person's newest open deal forward to the
// stage, never back and never over a stage a person set, or opens one
// there; ref is the run, for the audit data.
type DealMover interface {
	MoveDeal(ctx context.Context, tx pgx.Tx, ws, recordID string, d DealMove, ref string) (outcome, detail string, err error)
}

func checkTransactional(st Step) string {
	if strings.TrimSpace(st.Template) == "" {
		return "a transactional_email step needs a template"
	}
	return ""
}

// sendTransactional sends a transactional template to the person. Unlike a
// lifecycle email it ignores marketing consent, topics and the frequency
// cap, so it is for email the person needs (a receipt, a reset, an account
// notice); the suppression list and the policy check still apply when it
// sends.
func sendTransactional(ctx context.Context, x *act) (string, string, error) {
	if x.s.Emails == nil {
		return "failed", "email can't be sent here", nil
	}
	var templateID, purpose string
	err := x.tx.QueryRow(ctx, `SELECT id::text, purpose FROM email_templates WHERE workspace_id = $1 AND (id::text = $2 OR handle = $2)`,
		x.ws, x.st.Template).Scan(&templateID, &purpose)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "failed", "the template " + x.st.Template + " no longer exists", nil
	case err != nil:
		return "", "", err
	case purpose != "transactional":
		return "failed", "the template " + x.st.Template + " is a lifecycle template; use an email step", nil
	}
	var name, address string
	var attrs []byte
	err = x.tx.QueryRow(ctx, `SELECT r.name, COALESCE(i.value, ''), r.attributes FROM records r
		LEFT JOIN LATERAL (SELECT value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'email' AND i.verified
			AND i.valid_to IS NULL ORDER BY i.valid_from, i.id LIMIT 1) i ON true
		WHERE r.workspace_id = $1 AND r.id = $2`, x.ws, x.r.recordID).Scan(&name, &address, &attrs)
	if err != nil {
		return "", "", err
	}
	if address == "" {
		return "skipped", "no verified email address", nil
	}
	var attributes map[string]any
	_ = json.Unmarshal(attrs, &attributes)
	d := emails.Draft{NewEmail: emails.NewEmail{WorkspaceID: x.ws, BrandID: x.j.brandID, To: []string{address}, Purpose: "transactional",
		Trigger: "journey", TriggerRef: x.j.id, Surface: "job"},
		TemplateID: templateID, Data: map[string]any{"person": emails.PersonData(x.r.recordID, name, address, attributes), "event": x.r.context}}
	e, err := x.s.Emails.Compose(ctx, x.tx, d)
	var ce *emails.ComposeError
	if errors.As(err, &ce) {
		if ce.Code == "template_variables_missing" || ce.Code == "invalid_request" {
			return "skipped", ce.Message, nil
		}
		return "failed", ce.Message, nil
	}
	if err != nil {
		return "", "", err
	}
	if _, err := x.s.Emails.Queue(ctx, x.tx, emails.System, e); err != nil {
		return "", "", err
	}
	return "done", "sent", nil
}

func checkConsent(st Step) string {
	if st.Consent != customers.Granted && st.Consent != customers.Withdrawn {
		return "consent is granted or withdrawn"
	}
	if st.Topic != "" && !topicRE.MatchString(st.Topic) {
		return "topic is an email topic's key"
	}
	return ""
}

// setConsent records the person's email consent to a topic, or to all
// marketing email, with the run as its evidence.
func setConsent(ctx context.Context, x *act) (string, string, error) {
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
	if status == x.st.Consent {
		return "skipped", "already " + x.st.Consent, nil
	}
	return "done", x.st.Consent, customers.RecordConsent(ctx, x.tx, x.ws, customers.ConsentChange{RecordID: x.r.recordID, Channel: "email", Topic: x.st.Topic,
		Status: x.st.Consent, Source: "journey", Evidence: map[string]any{"journey_id": x.j.id, "run_id": x.r.id}})
}

func checkOwner(st Step) string {
	switch {
	case st.OwnerID != "" && len(st.Members) > 0:
		return "give owner_id (one member) or members (spread evenly), not both"
	case st.OwnerID != "" && !uuidRE.MatchString(st.OwnerID):
		return "owner_id is a workspace member's user id"
	case st.OwnerID == "" && len(st.Members) < 2:
		return "give owner_id, or at least two members to spread people between"
	case len(st.Members) > 50:
		return "at most 50 members"
	}
	for _, m := range st.Members {
		if !uuidRE.MatchString(m) {
			return "members are workspace members' user ids"
		}
	}
	return ""
}

// assignOwner sets the record's owner: one member, or of several the one
// who owns the fewest active records now, so new people are spread evenly.
// A record that has an owner keeps it unless replace_owner is set.
func assignOwner(ctx context.Context, x *act) (string, string, error) {
	var current *string
	if err := x.tx.QueryRow(ctx, `SELECT attributes->>'owner' FROM records WHERE workspace_id = $1 AND id = $2`, x.ws, x.r.recordID).Scan(&current); err != nil {
		return "", "", err
	}
	if current != nil && *current != "" && !x.st.ReplaceOwner {
		return "skipped", "already has an owner", nil
	}
	owner := x.st.OwnerID
	if owner == "" {
		err := x.tx.QueryRow(ctx, `SELECT m.user_id::text FROM memberships m
			WHERE m.workspace_id = $1 AND m.user_id::text = ANY($2::text[])
			ORDER BY (SELECT count(*) FROM records r WHERE r.workspace_id = $1 AND r.attributes->>'owner' = m.user_id::text
				AND r.archived_at IS NULL AND r.merged_into IS NULL), m.user_id
			LIMIT 1`, x.ws, x.st.Members).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return "failed", "none of the members is in the workspace any more", nil
		}
		if err != nil {
			return "", "", err
		}
	}
	if current != nil && *current == owner {
		return "skipped", "already their owner", nil
	}
	return x.s.writeField(ctx, x, "owner", "set", owner, true)
}

func checkDealMove(st Step) string {
	d := st.Deal
	switch {
	case d == nil:
		return "a move_deal step needs deal: the type, its stage field and the stage"
	case !topicRE.MatchString(d.Type) || !topicRE.MatchString(d.Field):
		return "deal.type and deal.field are a record type's and its status field's keys, such as deal and deal_stage"
	case d.Stage == "":
		return "deal.stage is the stage to move to"
	case len(d.Closed) > 20:
		return "at most 20 closed stages"
	}
	return ""
}

// moveDeal moves the person's open deal forward to the stage, or opens
// one there.
func moveDeal(ctx context.Context, x *act) (string, string, error) {
	if x.s.Deals == nil {
		return "failed", "deals can't be moved here", nil
	}
	return x.s.Deals.MoveDeal(ctx, x.tx, x.ws, x.r.recordID, *x.st.Deal, x.r.id)
}
