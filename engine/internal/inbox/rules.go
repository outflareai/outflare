package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leads"
)

// Reply rules say what Outflare does once a reply has a label it is sure
// of: snooze a "not now" until the date it gives, move a sequence past an
// out-of-office, add the person a referral names. A workspace has one set,
// and a project or a sequence can override it label by label. Every
// action can be undone, and a reply's text only ever picks a label, never
// an action, so a crafted reply cannot make Outflare do anything a person
// did not set up.

// JobRules applies the reply rules to one labelled reply.
const JobRules = "inbox.rules"

// Rule is what to do for one label.
type Rule struct {
	Action string `json:"action"`
	// Days is how long a "not now" waits when the reply gives no date.
	Days int `json:"days,omitempty"`
	// SequenceID is where an added referral is enrolled.
	SequenceID string `json:"sequence_id,omitempty"`
}

// RuleSet holds a rule per label.
type RuleSet map[string]Rule

// RuleActions are the labels rules exist for and the actions each can
// take, the default first.
var RuleActions = map[string][]string{
	// snooze: the reply leaves the open inbox and comes back on the date.
	// restart: the sequence it answered picks up again on the date.
	"not_now": {"snooze", "restart", "nothing"},
	// reschedule: the lead's next email waits until the day after they
	// are back.
	"out_of_office": {"reschedule", "nothing"},
	// suggest: the inbox offers to add the person; add: they become a
	// lead on the referrer's lists; add_and_enroll: and join a sequence.
	"referral":     {"suggest", "add", "add_and_enroll"},
	"wrong_person": {"suggest", "add", "add_and_enroll"},
}

// RuleLabels lists the labels with rules, in inbox order.
func RuleLabels() []string {
	var out []string
	for _, c := range Categories {
		if _, ok := RuleActions[c]; ok {
			out = append(out, c)
		}
	}
	return out
}

const (
	defaultNotNowDays = 90
	maxRuleDays       = 730
)

// DefaultRules apply where a workspace has not chosen.
func DefaultRules() RuleSet {
	rs := RuleSet{}
	for label, actions := range RuleActions {
		r := Rule{Action: actions[0]}
		if label == "not_now" {
			r.Days = defaultNotNowDays
		}
		rs[label] = r
	}
	return rs
}

// ErrInvalidRule is a rule set that cannot be saved; its message says why.
type ErrInvalidRule string

func (e ErrInvalidRule) Error() string { return string(e) }

// CheckRules validates a rule set for a workspace and fills defaults in
// each rule.
func CheckRules(ctx context.Context, q jobs.Querier, ws string, rs RuleSet) error {
	labels := make([]string, 0, len(rs))
	for l := range rs {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, label := range labels {
		r := rs[label]
		actions, ok := RuleActions[label]
		if !ok {
			return ErrInvalidRule(fmt.Sprintf("no rules for %q; rules exist for %s", label, strings.Join(RuleLabels(), ", ")))
		}
		if !slices.Contains(actions, r.Action) {
			return ErrInvalidRule(fmt.Sprintf("%s.action must be one of %s", label, strings.Join(actions, ", ")))
		}
		if label == "not_now" {
			if r.Days == 0 {
				r.Days = defaultNotNowDays
			}
			if r.Days < 1 || r.Days > maxRuleDays {
				return ErrInvalidRule(fmt.Sprintf("not_now.days must be 1 to %d", maxRuleDays))
			}
		} else if r.Days != 0 {
			return ErrInvalidRule(label + ".days is only for not_now")
		}
		if r.Action == "add_and_enroll" {
			var ok bool
			if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sequences WHERE id::text = $1 AND workspace_id = $2 AND status <> 'archived')`,
				r.SequenceID, ws).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return ErrInvalidRule(label + ".sequence_id must be a sequence in this workspace")
			}
		} else if r.SequenceID != "" {
			return ErrInvalidRule(label + ".sequence_id is only for add_and_enroll")
		}
		rs[label] = r
	}
	return nil
}

// RuleQuerier reads rule sets: a pool or a transaction.
type RuleQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// EffectiveRules are the rules for a reply to a sequence (none for a reply
// outside one): the defaults, then the workspace's set, the sequence's
// project's and the sequence's own, each winning label by label.
func EffectiveRules(ctx context.Context, q RuleQuerier, ws string, sequenceID *string) (RuleSet, error) {
	rows, err := q.Query(ctx, `
		SELECT r.rules FROM reply_rules r
		LEFT JOIN sequences s ON s.id = $2::uuid AND s.workspace_id = r.workspace_id
		WHERE r.workspace_id = $1 AND (
			(r.project_id IS NULL AND r.sequence_id IS NULL) OR
			(r.project_id IS NOT NULL AND r.project_id = s.project_id) OR
			(r.sequence_id IS NOT NULL AND r.sequence_id = s.id))
		ORDER BY (r.sequence_id IS NOT NULL), (r.project_id IS NOT NULL)`, ws, sequenceID)
	if err != nil {
		return nil, err
	}
	sets, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, err
	}
	out := DefaultRules()
	for _, raw := range sets {
		var rs RuleSet
		if err := json.Unmarshal(raw, &rs); err != nil {
			return nil, err
		}
		for label, r := range rs {
			if _, ok := RuleActions[label]; ok {
				out[label] = r
			}
		}
	}
	return out, nil
}

// QueueRules applies the reply rules to a reply once its label is sure, in
// the transaction that set it. The key makes each label act once per
// reply, so relabelling back and forth doesn't repeat an action.
func QueueRules(ctx context.Context, q jobs.Querier, ws, inboundID, category string) error {
	if _, ok := RuleActions[category]; !ok {
		return nil
	}
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobRules, WorkspaceID: &ws,
		Payload:        map[string]string{"inbound_id": inboundID, "category": category},
		IdempotencyKey: "rules:" + inboundID + ":" + category, MaxAttempts: 3})
	return err
}

// Rules runs JobRules.
type Rules struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Now is the clock, for tests.
	Now func() time.Time
}

// Register handles rule jobs.
func (r *Rules) Register(q *jobs.Queue) { q.Handle(JobRules, r.job) }

func (r *Rules) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Rules) job(ctx context.Context, j jobs.Job) error {
	var p struct {
		InboundID string `json:"inbound_id"`
		Category  string `json:"category"`
	}
	if err := json.Unmarshal(j.Payload, &p); err != nil || j.WorkspaceID == nil {
		return fmt.Errorf("%w: bad rules job", jobs.ErrPermanent)
	}
	return r.Apply(ctx, *j.WorkspaceID, p.InboundID, p.Category)
}

// Apply runs the rule for a reply's label, if the reply still has that
// label and no review is pending.
func (r *Rules) Apply(ctx context.Context, ws, inboundID, category string) error {
	return pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		var cat, enrollmentID, sequenceID, leadID *string
		var review bool
		var details []byte
		var received time.Time
		err := tx.QueryRow(ctx, `
			SELECT category, needs_review, enrollment_id::text, sequence_id::text, lead_id::text, COALESCE(details, '{}'), received_at
			FROM inbound_messages WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, inboundID, ws).
			Scan(&cat, &review, &enrollmentID, &sequenceID, &leadID, &details, &received)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if cat == nil || *cat != category || review {
			return nil // relabelled, or waiting for a person, since
		}
		rs, err := EffectiveRules(ctx, tx, ws, sequenceID)
		if err != nil {
			return err
		}
		rule := rs[category]
		var d map[string]string
		_ = json.Unmarshal(details, &d)
		data := map[string]any{"inbound_id": inboundID, "category": category, "action": rule.Action,
			"lead_id": leadID, "sequence_id": sequenceID}
		done := false
		switch category {
		case "not_now":
			until, dated := returnDate(d, r.now())
			if !dated {
				until = received.Add(time.Duration(rule.Days) * 24 * time.Hour)
			}
			data["until"] = until
			switch rule.Action {
			case "snooze":
				tag, err := tx.Exec(ctx, `UPDATE inbound_messages SET snoozed_until = $2 WHERE id = $1`, inboundID, until)
				if err != nil {
					return err
				}
				done = tag.RowsAffected() > 0
			case "restart":
				if enrollmentID == nil {
					break
				}
				// Only a sequence the reply stopped; one a person stopped
				// stays stopped.
				tag, err := tx.Exec(ctx, `
					UPDATE enrollments SET status = 'active', status_reason = 'restarts after a not now reply', next_send_at = $3, updated_at = now()
					WHERE id = $1 AND workspace_id = $2 AND status = 'replied'`, *enrollmentID, ws, until)
				if err != nil {
					return err
				}
				done = tag.RowsAffected() > 0
				data["enrollment_id"] = *enrollmentID
			}
		case "out_of_office":
			back, dated := returnDate(d, r.now())
			if rule.Action != "reschedule" || !dated || leadID == nil {
				break
			}
			next := back.Add(24 * time.Hour)
			tag, err := tx.Exec(ctx, `
				UPDATE enrollments SET next_send_at = $3, updated_at = now()
				WHERE workspace_id = $1 AND lead_id = $2 AND status = 'active' AND next_send_at IS NOT NULL AND next_send_at < $3`,
				ws, *leadID, next)
			if err != nil {
				return err
			}
			done = tag.RowsAffected() > 0
			data["until"], data["enrollments_moved"] = next, tag.RowsAffected()
		case "referral", "wrong_person":
			if rule.Action == "suggest" {
				break
			}
			seq := ""
			if rule.Action == "add_and_enroll" {
				seq = rule.SequenceID
			}
			ref, err := AddReferral(ctx, tx, ws, inboundID, seq)
			if errors.Is(err, ErrNoReferral) {
				break
			}
			if err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "lead.referral_added",
				TargetType: "lead", TargetID: ref.LeadID, Data: ref.Data(),
				Props: map[string]any{"created": ref.Created, "enrolled": ref.Enrolled, "via": "reply_rule"}}); err != nil {
				return err
			}
			done = true
			data["referral_lead_id"], data["enrolled"] = ref.LeadID, ref.Enrolled
		}
		if !done {
			return nil
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "inbox.rule_applied",
			TargetType: "inbound_message", TargetID: inboundID, Data: data,
			Props: map[string]any{"category": category, "action": rule.Action}})
	})
}

// returnDate reads the date a reply gives (return_date, YYYY-MM-DD) if it
// is in the future.
func returnDate(d map[string]string, now time.Time) (time.Time, bool) {
	t, err := time.Parse(time.DateOnly, strings.TrimSpace(d["return_date"]))
	if err != nil || !t.After(now) || t.After(now.AddDate(2, 0, 0)) {
		return time.Time{}, false
	}
	return t, true
}

// ErrNoReferral is a reply that names nobody to add.
var ErrNoReferral = errors.New("the reply doesn't name an email address to add")

// Referral is the person a reply named, added as a lead.
type Referral struct {
	LeadID   string `json:"lead_id"`
	Email    string `json:"email"`
	Created  bool   `json:"created"`
	Enrolled bool   `json:"enrolled"`
	data     map[string]any
}

// AddReferral adds the person a reply names (referral_email) as a lead,
// on the referrer's lists and marked as referred by them, and, with a
// sequence, enrols them unless they are on the opt-out list or already in
// it. The caller records lead.referral_added with Referral.Data.
func AddReferral(ctx context.Context, tx pgx.Tx, ws, inboundID, sequenceID string) (*Referral, error) {
	var details []byte
	var from string
	var referrer *string
	err := tx.QueryRow(ctx, `SELECT COALESCE(details, '{}'), from_email, lead_id::text FROM inbound_messages WHERE id = $1 AND workspace_id = $2`,
		inboundID, ws).Scan(&details, &from, &referrer)
	if err != nil {
		return nil, err
	}
	var d map[string]string
	_ = json.Unmarshal(details, &d)
	addr, err := mail.ParseAddress(strings.TrimSpace(d["referral_email"]))
	if err != nil || !strings.Contains(addr.Address, "@") {
		return nil, ErrNoReferral
	}
	email := strings.ToLower(addr.Address)
	if email == strings.ToLower(from) {
		return nil, ErrNoReferral
	}
	first, last, _ := strings.Cut(strings.TrimSpace(d["referral_name"]), " ")
	l := leads.Lead{Email: email, FirstName: first, LastName: strings.TrimSpace(last), Custom: map[string]string{"referred_by": strings.ToLower(from)}}
	if referrer != nil {
		var company, website string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(company, ''), COALESCE(website, '') FROM leads WHERE id = $1`, *referrer).Scan(&company, &website); err != nil {
			return nil, err
		}
		l.Company, l.Website = company, website
	}
	res, err := leads.Import(ctx, tx, ws, "referral", nil, []leads.Lead{l})
	if err != nil {
		return nil, err
	}
	ref := &Referral{Email: email, Created: res.Created > 0}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM leads WHERE workspace_id = $1 AND lower(email) = $2`, ws, email).Scan(&ref.LeadID); err != nil {
		return nil, err
	}
	if referrer != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO lead_list_members (list_id, lead_id) SELECT list_id, $2 FROM lead_list_members WHERE lead_id = $1
			ON CONFLICT DO NOTHING`, *referrer, ref.LeadID); err != nil {
			return nil, err
		}
	}
	if sequenceID != "" {
		tag, err := tx.Exec(ctx, `
			INSERT INTO enrollments (workspace_id, sequence_id, lead_id, next_send_at)
			SELECT $1, s.id, l.id, now() FROM sequences s, leads l
			WHERE s.id = $2 AND s.workspace_id = $1 AND s.status <> 'archived' AND l.id = $3
			  AND NOT EXISTS (`+leads.SuppressedSQL("l.workspace_id", "l.email")+`)
			ON CONFLICT DO NOTHING`, ws, sequenceID, ref.LeadID)
		if err != nil {
			return nil, err
		}
		ref.Enrolled = tag.RowsAffected() > 0
	}
	ref.data = map[string]any{"inbound_id": inboundID, "referred_by_lead_id": referrer, "created": ref.Created,
		"sequence_id": nilIfEmpty(sequenceID), "enrolled": ref.Enrolled}
	return ref, nil
}

// Data is the audit data for lead.referral_added.
func (r *Referral) Data() map[string]any { return r.data }
