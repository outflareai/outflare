package reviewrequests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/policy"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

const JobDispatch = "reviews.dispatch"

type Service struct {
	Pool *pgxpool.Pool
	Mail *emails.Service
}

func (s *Service) Register(q *jobs.Queue) { q.Handle(JobDispatch, s.dispatch) }
func Enqueue(ctx context.Context, tx pgx.Tx, ws, brand, campaign string, at time.Time) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobDispatch, WorkspaceID: &ws, Payload: map[string]string{"brand": brand, "campaign": campaign}, RunAt: at, MaxAttempts: 8})
	return err
}

// AudienceIDs resolves lists and rule audiences at launch. The request rows are
// the immutable audience snapshot; customer eligibility is checked again later.
func AudienceIDs(ctx context.Context, tx pgx.Tx, ws, brand string, a Audience) ([]string, error) {
	where := `r.workspace_id=$1 AND r.brand_id=$2 AND r.deleted_at IS NULL AND r.merged_into IS NULL AND r.archived_at IS NULL AND r.record_type='person'`
	args := []any{ws, brand}
	switch a.Kind {
	case "records":
		args = append(args, a.RecordIDs)
		where += ` AND r.id=ANY($3::uuid[])`
	case "list":
		var kind, match string
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT kind,rules,match FROM smart_lists WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, ws, brand, a.ListID).Scan(&kind, &raw, &match); err != nil {
			return nil, fmt.Errorf("customer list not found: %w", err)
		}
		if kind == smartlists.KindSmart {
			var rules []smartlists.Rule
			if err := json.Unmarshal(raw, &rules); err != nil {
				return nil, err
			}
			return AudienceIDs(ctx, tx, ws, brand, Audience{Kind: "rules", Rules: rules, Match: match})
		}
		args = append(args, a.ListID)
		where += ` AND EXISTS(SELECT 1 FROM static_list_members m WHERE m.workspace_id=$1 AND m.list_id=$3 AND m.record_id=r.id)`
	case "rules":
		b := brand
		rules, more := smartlists.Where(a.Rules, a.Match, &b, []any{ws})
		args = more
		// Where owns the argument numbering. Also require the exact brand.
		args = append(args, brand)
		where = rules + fmt.Sprintf(" AND r.brand_id=$%d AND r.deleted_at IS NULL", len(args))
	default:
		return nil, fmt.Errorf("choose a valid audience")
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text FROM records r WHERE `+where+` ORDER BY r.id LIMIT 10001`, args...)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if len(ids) > 10000 {
		return nil, fmt.Errorf("audience exceeds 10,000 customers; narrow the audience")
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, err
}

type Person struct {
	ID, Name, Email string
	Attributes      map[string]any
}

func PersonFor(ctx context.Context, q jobs.Querier, ws, brand, id string) (Person, error) {
	p := Person{ID: id}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT r.name,r.attributes,COALESCE((SELECT value FROM record_identities i WHERE i.workspace_id=$1 AND i.record_id=r.id AND i.namespace='email' AND i.valid_to IS NULL ORDER BY i.verified DESC,i.valid_from,i.id LIMIT 1),'') FROM records r WHERE r.workspace_id=$1 AND r.brand_id=$2 AND r.id=$3 AND r.deleted_at IS NULL AND r.archived_at IS NULL AND r.merged_into IS NULL AND r.record_type='person'`, ws, brand, id).Scan(&p.Name, &raw, &p.Email)
	if err == nil {
		err = json.Unmarshal(raw, &p.Attributes)
	}
	return p, err
}

// Eligible returns the remaining destinations. Per-platform limits remove only
// destinations already requested; a multi-link email is still one invitation.
type Querier interface {
	jobs.Querier
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func Eligible(ctx context.Context, q Querier, ws, brand, id, request string, settings Settings, dests []Destination) ([]Destination, string, error) {
	p, err := PersonFor(ctx, q, ws, brand, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "customer_unavailable", nil
	}
	if err != nil {
		return nil, "", err
	}
	if p.Email == "" {
		return nil, "missing_email", nil
	}
	why, err := emails.SkipReason(ctx, q, ws, id, nil)
	if err != nil || why != "" {
		return nil, why, err
	}
	ds, err := policy.CheckPurpose(ctx, q, ws, "lifecycle", []string{p.Email})
	if err != nil {
		return nil, "", err
	}
	for _, d := range ds {
		if !d.Allowed {
			return nil, "email_policy", nil
		}
	}
	if settings.Frequency == "unlimited" {
		return dests, "", nil
	}
	remaining := []Destination{}
	for _, d := range dests {
		var asked bool
		err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM review_requests r JOIN emails e ON e.id=r.email_id AND e.workspace_id=r.workspace_id WHERE r.workspace_id=$1 AND r.brand_id=$2 AND r.record_id=$3 AND r.id::text<>$4 AND r.status='queued' AND e.status NOT IN ('failed','cancelled','suppressed') AND ($5='once' OR COALESCE(e.sent_at,e.send_at,e.created_at)>now()-$6::int*interval '1 day') AND ($7='brand' OR $8=ANY(r.platforms)))`, ws, brand, id, request, settings.Frequency, settings.IntervalDays, settings.FrequencyScope, d.Platform).Scan(&asked)
		if err != nil {
			return nil, "", err
		}
		if !asked {
			remaining = append(remaining, d)
		}
	}
	if len(remaining) == 0 {
		return nil, "review_frequency_limit", nil
	}
	return remaining, "", nil
}
func Content(c Config, dests []Destination) (string, string) {
	text := c.Body + "\n\n"
	body := "<div style=\"white-space:pre-wrap\">" + html.EscapeString(c.Body) + "</div><ul>"
	for _, d := range dests {
		name := platform(d.Platform).Name
		text += "Leave a review on " + name + ": " + d.URL + "\n"
		body += "<li><a href=\"" + html.EscapeString(d.URL) + "\">Leave a review on " + html.EscapeString(name) + "</a></li>"
	}
	return body + "</ul>", text
}
func (s *Service) Compose(ctx context.Context, q jobs.Querier, ws, brand string, c Config, dests []Destination, p Person, test bool) (emails.NewEmail, error) {
	if s.Mail == nil {
		return emails.NewEmail{}, &emails.ComposeError{Status: 409, Code: "no_email_provider", Message: "Connect an email provider in Lifecycle before sending review requests."}
	}
	settings, err := LoadSettings(ctx, q, ws, brand)
	if err != nil {
		return emails.NewEmail{}, err
	}
	if c.From == "" {
		c.From = settings.From
	}
	if c.ReplyTo == "" {
		c.ReplyTo = settings.ReplyTo
	}
	// Reuse the brand's most recent lifecycle sender as a default.
	if c.From == "" {
		err = q.QueryRow(ctx, `SELECT from_address FROM email_templates WHERE workspace_id=$1 AND brand_id=$2 AND purpose='lifecycle' AND from_address<>'' AND status='active' ORDER BY updated_at DESC LIMIT 1`, ws, brand).Scan(&c.From)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return emails.NewEmail{}, err
		}
	}
	h, t := Content(c, dests)
	clicks := true
	var brandName string
	if err = q.QueryRow(ctx, `SELECT name FROM brands WHERE workspace_id=$1 AND id=$2`, ws, brand).Scan(&brandName); err != nil {
		return emails.NewEmail{}, err
	}
	e := emails.NewEmail{WorkspaceID: ws, BrandID: &brand, From: c.From, To: []string{p.Email}, Purpose: "lifecycle", RecordID: p.ID, Test: test, Trigger: "system", Surface: "job"}
	if c.ReplyTo != "" {
		e.ReplyTo = []string{c.ReplyTo}
	}
	return s.Mail.Compose(ctx, q, emails.Draft{NewEmail: e, Subject: &c.Subject, HTML: &h, Text: &t, TrackClicks: &clicks, Data: map[string]any{"person": emails.PersonData(p.ID, p.Name, p.Email, p.Attributes), "brand": map[string]any{"name": brandName}}})
}

// Add records an individual request once. Workflow callers supply run/step as
// source; launch callers supply customer id. Everything commits together.
func Add(ctx context.Context, tx pgx.Tx, ws, brand, campaign, record, source string, c Config, dests []Destination) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	links, _ := json.Marshal(dests)
	ids := []string{}
	for _, d := range dests {
		ids = append(ids, d.Platform)
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO review_requests(workspace_id,brand_id,campaign_id,record_id,source_key,platforms,destinations,config) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(campaign_id,source_key) DO UPDATE SET source_key=EXCLUDED.source_key RETURNING id::text`, ws, brand, campaign, record, source, ids, links, raw).Scan(&id)
	return id, err
}
func (s *Service) QueueOne(ctx context.Context, tx pgx.Tx, ws, brand, id string) (string, string, error) {
	var rec, campaign, state string
	var raw, links []byte
	err := tx.QueryRow(ctx, `SELECT record_id::text,campaign_id::text,status,config,destinations FROM review_requests WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 FOR UPDATE`, ws, brand, id).Scan(&rec, &campaign, &state, &raw, &links)
	if err != nil {
		return "", "", err
	}
	if state != "pending" {
		return "skipped", "already_processed", nil
	}
	// Serialize all campaigns for this brand/customer, regardless of destination.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "review:"+ws+":"+brand+":"+rec); err != nil {
		return "", "", err
	}
	var c Config
	var dests []Destination
	if err = json.Unmarshal(raw, &c); err != nil {
		return "", "", err
	}
	if err = json.Unmarshal(links, &dests); err != nil {
		return "", "", err
	}
	settings, err := LoadSettings(ctx, tx, ws, brand)
	if err != nil {
		return "", "", err
	}
	dests, why, err := Eligible(ctx, tx, ws, brand, rec, id, settings, dests)
	if err != nil {
		return "", "", err
	}
	finish := func(state, why string) (string, string, error) {
		_, err := tx.Exec(ctx, `UPDATE review_requests SET status=$4,reason=$5 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, ws, brand, id, state, why)
		if err == nil {
			err = audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "review_request." + state, TargetType: "review_request", TargetID: id, Data: map[string]any{"campaign_id": campaign, "record_id": rec}})
		}
		if err == nil {
			_, err = customers.Record(ctx, tx, ws, customers.Event{RecordID: rec, Name: "review_request." + state, Source: "reviews", Properties: map[string]any{"review_request_id": id, "campaign_id": campaign, "reason": why}, MessageID: "review:" + id + ":" + state})
		}
		return state, why, err
	}
	if why != "" {
		return finish("skipped", why)
	}
	p, err := PersonFor(ctx, tx, ws, brand, rec)
	if err != nil {
		return "", "", err
	}
	e, err := s.Compose(ctx, tx, ws, brand, c, dests, p, false)
	var ce *emails.ComposeError
	if errors.As(err, &ce) {
		return finish("failed", ce.Code)
	}
	if err != nil {
		return "", "", err
	}
	e.TriggerRef = campaign
	e.Metadata = map[string]any{"review_request_id": id, "review_campaign_id": campaign}
	guarded, err := s.Mail.Guard(ctx, tx, rec, &e, emails.GuardOptions{})
	if err != nil {
		return "", "", err
	}
	if guarded.Capped != "" {
		return finish("skipped", "frequency_cap")
	}
	emailID, err := s.Mail.Queue(ctx, tx, emails.System, e)
	if err != nil {
		return "", "", err
	}
	ids := []string{}
	for _, d := range dests {
		ids = append(ids, d.Platform)
	}
	links, _ = json.Marshal(dests)
	_, err = tx.Exec(ctx, `UPDATE review_requests SET status='queued',email_id=$4,platforms=$5,destinations=$6 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, ws, brand, id, emailID, ids, links)
	if err != nil {
		return "", "", err
	}
	err = audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "review_request.queued", TargetType: "review_request", TargetID: id, Data: map[string]any{"campaign_id": campaign, "record_id": rec, "email_id": emailID}})
	if err == nil {
		_, err = customers.Record(ctx, tx, ws, customers.Event{RecordID: rec, Name: "review_request.queued", Source: "reviews", Properties: map[string]any{"review_request_id": id, "campaign_id": campaign, "email_id": emailID}, MessageID: "review:" + id + ":" + emailID})
	}
	return "done", emailID, err
}
func (s *Service) dispatch(ctx context.Context, j jobs.Job) error {
	var p struct {
		Brand    string `json:"brand"`
		Campaign string `json:"campaign"`
	}
	if j.WorkspaceID == nil || json.Unmarshal(j.Payload, &p) != nil {
		return fmt.Errorf("%w: invalid review dispatch", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	// Each batch is atomic, bounded, and retryable. Jobs supply queue, duration,
	// failure and backlog metrics through the existing job instrumentation.
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM review_request_campaigns WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 FOR UPDATE`, ws, p.Brand, p.Campaign).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if status != "active" {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT id::text FROM review_requests WHERE workspace_id=$1 AND brand_id=$2 AND campaign_id=$3 AND status='pending' ORDER BY id LIMIT 50`, ws, p.Brand, p.Campaign)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, _, err = s.QueueOne(ctx, tx, ws, p.Brand, id); err != nil {
				return err
			}
		}
		if len(ids) == 50 {
			return Enqueue(ctx, tx, ws, p.Brand, p.Campaign, time.Time{})
		}
		return nil
	})
}

// FromWorkflow uses the same campaign configuration and frequency checks as a
// manual launch. A campaign and journey must belong to exactly the same brand.
func (s *Service) FromWorkflow(ctx context.Context, tx pgx.Tx, ws, brand, campaign, record, source string) (string, string, error) {
	var raw []byte
	var status string
	err := tx.QueryRow(ctx, `SELECT config,status FROM review_request_campaigns WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 FOR SHARE`, ws, brand, campaign).Scan(&raw, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "failed", "review campaign not found", nil
	}
	if err != nil {
		return "", "", err
	}
	if status != "active" {
		return "skipped", "review campaign is " + status, nil
	}
	var c Config
	if err = json.Unmarshal(raw, &c); err != nil {
		return "", "", err
	}
	if c.Mode != "workflow" {
		return "failed", "choose a workflow review campaign", nil
	}
	settings, err := LoadSettings(ctx, tx, ws, brand)
	if err != nil {
		return "", "", err
	}
	dests, err := c.Destinations(settings)
	if err != nil {
		return "failed", err.Error(), nil
	}
	// Never write a request against a customer belonging to another brand.
	if _, err = PersonFor(ctx, tx, ws, brand, record); errors.Is(err, pgx.ErrNoRows) {
		return "skipped", "customer unavailable", nil
	} else if err != nil {
		return "", "", err
	}
	id, err := Add(ctx, tx, ws, brand, campaign, record, source, c, dests)
	if err != nil {
		return "", "", err
	}
	return s.QueueOne(ctx, tx, ws, brand, id)
}

// Preview renders exactly the same safe email body as a real send.
func PreviewText(c Config, dests []Destination) string {
	_, t := Content(c, dests)
	return strings.TrimSpace(t)
}
