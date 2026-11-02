// Package peoplesteps holds the pipeline steps any feature that finds
// people at companies can share (docs/pipeline.md, "Shared steps for
// people"). An item is one person; the feature keeps who they are. The
// steps run in this order:
//
//	find_email     finds the person's work address with the email finder
//	               (the learned pattern and history first, then the
//	               workspace's own verifier when confidence is low), or
//	               verifies the address the feature already has
//	contact_check  the address or its domain is on the opt-out list as an
//	               opt-out or a bounce: stop; a competitor, customer or
//	               partner entry goes on with a warning
//	add_person     add the person as a prospect record (records.acquired_via
//	               names the feature), linked to their company
//	enroll         only when the feature names a campaign (Config.Campaign):
//	               add the person to that sequence, skipping anyone opted
//	               out or active in another sequence
//
// The feature says who an item is (Config.Contact) and records what
// happened to it (Config.Found, Config.Linked and Config.Stopped).
package peoplesteps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/pipeline"
	"github.com/outflareai/outflare/engine/internal/policy"
	"github.com/outflareai/outflare/engine/internal/pushlead"
)

// Step names, in order. The first is where a feature sends an item.
const (
	FindEmail    = "find_email"
	ContactCheck = "contact_check"
	AddPerson    = "add_person"
	Enroll       = "enroll"
)

// Codes the steps skip an item with, and the warning for a guessed address
// nothing could confirm.
const (
	NotFound      = "not_found"
	Undeliverable = "undeliverable"
	OptedOut      = "opted_out"
	Bounced       = "bounced"
	RiskyEmail    = "risky_email"
	// InAnotherCampaign: the person is active in another sequence.
	InAnotherCampaign = "in_another_campaign"
	// CampaignUnavailable: the sequence is gone or archived, or runs in a
	// connected sequencer Outflare can't add people to, or that sequencer's
	// connection is gone or refused, so the run pauses until someone fixes
	// it or the feature picks another.
	CampaignUnavailable = "campaign_unavailable"
	// SequencerRefused: the connected sequencer left the person out (in
	// another of its campaigns, unsubscribed or blocked there).
	SequencerRefused = "sequencer_refused"
	// CampaignReady: held at enroll until the feature releases the item.
	CampaignReady = "campaign_ready"
)

// Contact is the person an item stands for.
type Contact struct {
	FirstName, LastName, Title, LinkedInURL string
	// Domain is the company's domain, where the address is found.
	Domain string
	// Email is the address once known: one the feature already had (a
	// provider's, a published one), or the one find_email found.
	Email string
	// Risky is set when find_email could not confirm the address.
	Risky bool
	// CompanyID is the company record the person works at, if any.
	CompanyID string
	// Brand is the brand to add the person for (nil for every brand).
	Brand *string
	// RecordID is the person's record once add_person has run.
	RecordID string
}

// Config is a feature's use of the steps.
type Config struct {
	// Pipeline is the feature's pipeline, which the steps join.
	Pipeline string
	// Via is the tool new records came from (records.acquired_via).
	Via string
	// Contact is the person an item stands for, from the feature's data.
	Contact func(ctx context.Context, it pipeline.Item) (Contact, error)
	// Found records the address find_email settled on for an item, with
	// its code (found, verified or risky) and confidence (0-100).
	Found func(ctx context.Context, it pipeline.Item, email string, code emailfind.Code, confidence int) error
	// Spent records what finding or verifying an item's address cost on
	// the workspace's own paid verifier (Units credits on Connection),
	// before Found or Stopped. Called only when it cost something; a
	// cached check costs nothing. Optional.
	Spent func(ctx context.Context, it pipeline.Item, cost emailfind.Cost) error
	// Linked records, in tx, that an item's person is recordID. Warnings
	// are opt-out list entries that let them through, and risky_email.
	Linked func(ctx context.Context, tx pgx.Tx, it pipeline.Item, recordID string, warnings []string) error
	// Stopped records that an item was stopped, with the skip code
	// (not_found, undeliverable, opted_out, bounced) and what was behind it.
	Stopped func(ctx context.Context, it pipeline.Item, code string, reasons []string) error
	// Campaign is the sequence to add an item's person to, or "" for none.
	// Hold keeps the item at enroll (held, CampaignReady) until the feature
	// releases it there, for people a person approves first. When set,
	// add_person goes on to enroll. Optional.
	Campaign func(ctx context.Context, it pipeline.Item) (sequenceID string, hold bool, err error)
	// Enrolled records, in tx, that an item's person was added to
	// sequenceID as leadID (or already was). Needed with Campaign.
	Enrolled func(ctx context.Context, tx pgx.Tx, it pipeline.Item, sequenceID, leadID string) error
	// Next is the step after add_person (or enroll); "" finishes the item
	// there.
	Next string
	// Finder replaces the engine's email finder, for tests.
	Finder Finder
}

// Finder is the email finder the steps use.
type Finder interface {
	Resolve(ctx context.Context, ws string, req emailfind.FindRequest) (emailfind.FindResult, error)
	VerifyEmails(ctx context.Context, ws string, req emailfind.VerifyRequest) (emailfind.FindResult, error)
}

// Stages are the steps for c, to register on the pipeline runner.
func Stages(pool *pgxpool.Pool, finder Finder, store *connectors.Store, c Config) []pipeline.Stage {
	if c.Finder != nil {
		finder = c.Finder
	}
	if c.Contact == nil || c.Found == nil || c.Linked == nil || c.Stopped == nil || c.Via == "" || finder == nil {
		panic(fmt.Sprintf("peoplesteps: %q needs Via, an email finder, Contact, Found, Linked and Stopped", c.Pipeline))
	}
	if (c.Campaign == nil) != (c.Enrolled == nil) {
		panic(fmt.Sprintf("peoplesteps: %q needs both Campaign and Enrolled, or neither", c.Pipeline))
	}
	s := steps{pool: pool, finder: finder, store: store, c: c}
	stages := []pipeline.Stage{
		// One finder call takes a whole batch, up to its own limit.
		{Pipeline: c.Pipeline, Name: FindEmail, Work: s.findEmail, Batch: 25},
		{Pipeline: c.Pipeline, Name: ContactCheck, Work: s.contactCheck},
		{Pipeline: c.Pipeline, Name: AddPerson, Work: s.addPerson},
	}
	if c.Campaign != nil {
		stages = append(stages, pipeline.Stage{Pipeline: c.Pipeline, Name: Enroll, Work: s.enroll})
	}
	return stages
}

// done finishes an item, or sends it on to Config.Next.
func (s steps) done(it pipeline.Item) pipeline.Result {
	if s.c.Next == "" {
		return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Done}
	}
	return advance(it, s.c.Next)
}

type steps struct {
	pool   *pgxpool.Pool
	finder Finder
	// store reaches connected sequencers, for campaigns that send there.
	store *connectors.Store
	c     Config
}

func retry(it pipeline.Item, err error) pipeline.Result {
	return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Retry, Code: "internal", Reason: err.Error()}
}

func advance(it pipeline.Item, next string) pipeline.Result {
	return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Advance, Next: next}
}

func byWorkspace(items []pipeline.Item) map[string][]pipeline.Item {
	out := map[string][]pipeline.Item{}
	for _, it := range items {
		out[it.WorkspaceID] = append(out[it.WorkspaceID], it)
	}
	return out
}

// findEmail finds or verifies each person's address, in one finder call
// per workspace and kind.
func (s steps) findEmail(ctx context.Context, items []pipeline.Item) ([]pipeline.Result, error) {
	var out []pipeline.Result
	for ws, group := range byWorkspace(items) {
		byRef := map[string]pipeline.Item{}
		var find emailfind.FindRequest
		var check emailfind.VerifyRequest
		for _, it := range group {
			ct, err := s.c.Contact(ctx, it)
			if err != nil {
				out = append(out, retry(it, err))
				continue
			}
			byRef[it.ID] = it
			if ct.Email != "" {
				check.Items = append(check.Items, emailfind.VerifyItem{Ref: it.ID, Email: ct.Email})
				continue
			}
			find.Items = append(find.Items, emailfind.FindItem{Ref: it.ID, FirstName: ct.FirstName, LastName: ct.LastName,
				Domain: customers.CompanyDomain(ct.Domain)})
		}
		var results []emailfind.ItemResult
		for _, part := range [2]bool{true, false} {
			var res emailfind.FindResult
			var err error
			switch {
			case part && len(find.Items) > 0:
				res, err = s.finder.Resolve(ctx, ws, find)
			case !part && len(check.Items) > 0:
				res, err = s.finder.VerifyEmails(ctx, ws, check)
			default:
				continue
			}
			if err != nil {
				return nil, err
			}
			results = append(results, res.Items...)
		}
		seen := map[string]bool{}
		for _, r := range results {
			it, ok := byRef[r.Ref]
			if !ok || seen[r.Ref] {
				continue
			}
			seen[r.Ref] = true
			out = append(out, s.found(ctx, it, r))
		}
		// An item the finder left out is retried by the runner (no_result).
	}
	return out, nil
}

// found turns one finder answer into what happens to the item.
func (s steps) found(ctx context.Context, it pipeline.Item, r emailfind.ItemResult) pipeline.Result {
	if s.c.Spent != nil && (r.Cost.Units > 0 || r.Cost.Cents > 0) {
		if err := s.c.Spent(ctx, it, r.Cost); err != nil {
			return retry(it, err)
		}
	}
	switch r.Code {
	case emailfind.CodeFound, emailfind.CodeVerified, emailfind.CodeRisky:
		if err := s.c.Found(ctx, it, r.Email, r.Code, r.Confidence); err != nil {
			return retry(it, err)
		}
		return advance(it, ContactCheck)
	case emailfind.CodeNotFound, emailfind.CodeUndeliverable:
		code := NotFound
		if r.Code == emailfind.CodeUndeliverable {
			code = Undeliverable
		}
		if err := s.c.Stopped(ctx, it, code, nil); err != nil {
			return retry(it, err)
		}
		return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Skip, Code: code}
	case emailfind.CodePending:
		return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Wait, Code: "verify_pending", RetryAfter: max(r.RetryAfter, 30*time.Second)}
	case emailfind.CodeStop:
		return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Pause, Code: r.StopCode,
			Reason: "The workspace's email verifier refused or ran out. Check it on the Connectors page, then resume."}
	}
	return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Retry, Code: "verify_retry", RetryAfter: r.RetryAfter}
}

// hard says whether a contact rule stops a person: opt-outs (including a
// withdrawn consent), bounces and a bad address. Competitor, customer and
// partner entries, and a record held for review, only warn.
func hard(reason string) (string, bool) {
	kind, why, _ := strings.Cut(reason, ":")
	switch {
	case kind == policy.InvalidEmail:
		return Undeliverable, true
	case why == "bounced":
		return Bounced, true
	case kind == policy.NeedsReview, why == "competitor", why == "customer", why == "partner":
		return "", false
	}
	return OptedOut, true
}

func check(d policy.Decision) (code string, stops, warnings []string) {
	for _, r := range d.Reasons {
		if c, ok := hard(r); ok {
			if code == "" || c != OptedOut {
				code = c
			}
			stops = append(stops, r)
		} else {
			warnings = append(warnings, r)
		}
	}
	return code, stops, warnings
}

// contactCheck stops people the workspace must not email cold.
func (s steps) contactCheck(ctx context.Context, items []pipeline.Item) ([]pipeline.Result, error) {
	var out []pipeline.Result
	for ws, group := range byWorkspace(items) {
		var ok []pipeline.Item
		var emails []string
		for _, it := range group {
			ct, err := s.c.Contact(ctx, it)
			if err != nil {
				out = append(out, retry(it, err))
				continue
			}
			ok, emails = append(ok, it), append(emails, ct.Email)
		}
		if len(ok) == 0 {
			continue
		}
		decisions, err := policy.CheckContacts(ctx, s.pool, ws, policy.PurposeCold, emails)
		if err != nil {
			return nil, err
		}
		for i, it := range ok {
			code, stops, _ := check(decisions[i])
			if code == "" {
				out = append(out, advance(it, AddPerson))
				continue
			}
			if err := s.c.Stopped(ctx, it, code, stops); err != nil {
				out = append(out, retry(it, err))
				continue
			}
			out = append(out, pipeline.Result{ItemID: it.ID, Outcome: pipeline.Skip, Code: code})
		}
	}
	return out, nil
}

// addPerson adds each person as a prospect record linked to their company.
func (s steps) addPerson(ctx context.Context, items []pipeline.Item) ([]pipeline.Result, error) {
	var out []pipeline.Result
	for _, it := range items {
		if err := s.add(ctx, it); err != nil {
			out = append(out, retry(it, err))
			continue
		}
		if s.c.Campaign != nil {
			out = append(out, advance(it, Enroll))
		} else {
			out = append(out, s.done(it))
		}
	}
	return out, nil
}

func (s steps) add(ctx context.Context, it pipeline.Item) error {
	ct, err := s.c.Contact(ctx, it)
	if err != nil {
		return err
	}
	email := strings.ToLower(strings.TrimSpace(ct.Email))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	decisions, err := policy.CheckContacts(ctx, tx, it.WorkspaceID, policy.PurposeCold, []string{email})
	if err != nil {
		return err
	}
	_, _, warnings := check(decisions[0])
	if ct.Risky {
		warnings = append(warnings, RiskyEmail)
	}
	attrs := map[string]any{"first_name": strings.TrimSpace(ct.FirstName), "last_name": strings.TrimSpace(ct.LastName),
		"job_title": strings.TrimSpace(ct.Title), "linkedin_url": strings.TrimSpace(ct.LinkedInURL)}
	if ct.CompanyID != "" {
		attrs["companies"] = []any{ct.CompanyID}
	}
	name := strings.TrimSpace(ct.FirstName + " " + ct.LastName)
	ids, err := customers.EnsurePeople(ctx, tx, it.WorkspaceID, "prospect", s.c.Via, []customers.Person{{Email: email, Name: name, Attributes: attrs}})
	if err != nil {
		return err
	}
	id, ok := ids[email]
	if !ok {
		return fmt.Errorf("peoplesteps: no record for an item of %s", it.RunID)
	}
	if ct.CompanyID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, field_key)
			SELECT $1, $2, $3, 'companies', 'companies' WHERE EXISTS (SELECT 1 FROM records WHERE id = $3 AND workspace_id = $1)
			ON CONFLICT DO NOTHING`, it.WorkspaceID, id, ct.CompanyID); err != nil {
			return err
		}
	}
	if err := s.c.Linked(ctx, tx, it, id, warnings); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// enroll adds each person to the feature's campaign.
func (s steps) enroll(ctx context.Context, items []pipeline.Item) ([]pipeline.Result, error) {
	var out []pipeline.Result
	for _, it := range items {
		seq, hold, err := s.c.Campaign(ctx, it)
		if err != nil {
			out = append(out, retry(it, err))
			continue
		}
		if hold && seq != "" {
			out = append(out, pipeline.Result{ItemID: it.ID, Outcome: pipeline.Hold, Code: CampaignReady})
			continue
		}
		if seq == "" {
			out = append(out, s.done(it))
			continue
		}
		res, err := s.enrollOne(ctx, it, seq)
		if err != nil {
			res = retry(it, err)
		}
		out = append(out, res)
	}
	return out, nil
}

func (s steps) enrollOne(ctx context.Context, it pipeline.Item, seq string) (pipeline.Result, error) {
	ct, err := s.c.Contact(ctx, it)
	if err != nil {
		return pipeline.Result{}, err
	}
	if ct.RecordID == "" {
		return pipeline.Result{}, fmt.Errorf("peoplesteps: an item of %s reached enroll without a record", it.RunID)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return pipeline.Result{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	ws := it.WorkspaceID
	var status string
	var linked bool
	err = tx.QueryRow(ctx, `SELECT status, EXISTS (SELECT 1 FROM campaign_links l WHERE l.sequence_id = s.id) FROM sequences s
		WHERE s.workspace_id = $1 AND s.id = $2`, ws, seq).Scan(&status, &linked)
	if errors.Is(err, pgx.ErrNoRows) || status == "archived" {
		return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Pause, Code: CampaignUnavailable,
			Reason: "The campaign was removed or archived. Pick another campaign, then resume."}, nil
	}
	if err != nil {
		return pipeline.Result{}, err
	}
	if linked {
		_ = tx.Rollback(ctx)
		return s.pushOne(ctx, it, seq, ct)
	}
	leadID, err := s.lead(ctx, tx, ws, ct)
	if err != nil {
		return pipeline.Result{}, err
	}
	var here, suppressed, elsewhere bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM enrollments WHERE sequence_id = $1 AND lead_id = $2),
			EXISTS (`+leads.SuppressedSQL("l.workspace_id", "l.email")+`),
			EXISTS (SELECT 1 FROM enrollments e JOIN sequences q ON q.id = e.sequence_id WHERE e.lead_id = l.id AND e.status = 'active'
				AND e.sequence_id <> $1 AND q.status IN ('active', 'paused'))
		FROM leads l WHERE l.id = $2`, seq, leadID).Scan(&here, &suppressed, &elsewhere); err != nil {
		return pipeline.Result{}, err
	}
	stop := ""
	switch {
	case here:
	case suppressed:
		stop = OptedOut
	case elsewhere:
		stop = InAnotherCampaign
	default:
		if _, err := tx.Exec(ctx, `INSERT INTO enrollments (workspace_id, sequence_id, lead_id, next_send_at) VALUES ($1, $2, $3, now())
			ON CONFLICT DO NOTHING`, ws, seq, leadID); err != nil {
			return pipeline.Result{}, err
		}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, Action: "sequence.leads_enrolled", TargetType: "sequence", TargetID: seq,
			ActorType: "system", Data: map[string]any{"enrolled": 1, "suppressed": 0, "active_elsewhere": 0, "via": s.c.Via}}); err != nil {
			return pipeline.Result{}, err
		}
	}
	if stop != "" {
		if err := tx.Commit(ctx); err != nil {
			return pipeline.Result{}, err
		}
		if err := s.c.Stopped(ctx, it, stop, nil); err != nil {
			return pipeline.Result{}, err
		}
		return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Skip, Code: stop}, nil
	}
	if err := s.c.Enrolled(ctx, tx, it, seq, leadID); err != nil {
		return pipeline.Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return pipeline.Result{}, err
	}
	return s.done(it), nil
}

// pause stops the run at enroll with a reason a person can act on.
func pause(it pipeline.Item, reason string) pipeline.Result {
	return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Pause, Code: CampaignUnavailable, Reason: reason}
}

// pushOne adds the person to the platform campaign a sequence is linked to
// (Smartlead, Instantly, EmailBison), on the workspace's own connection,
// after the same checks enrolling and journeys make: the opt-out list and
// the sequence's exclusion. The sequencer paces and sends from there.
func (s steps) pushOne(ctx context.Context, it pipeline.Item, seq string, ct Contact) (pipeline.Result, error) {
	ws := it.WorkspaceID
	var reg *connectors.Registry
	if s.store != nil {
		reg = s.store.Registry
	}
	link, _, err := pushlead.LinkOf(ctx, s.pool, reg, ws, seq)
	if err != nil {
		return pipeline.Result{}, err
	}
	if link == nil {
		return pause(it, "The campaign runs in a sequencer Outflare can't add people to. Pick another campaign, then resume."), nil
	}
	email := strings.ToLower(strings.TrimSpace(ct.Email))
	var suppressed bool
	var exclude customers.Exclusion
	var company string
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (`+leads.SuppressedSQL("$1::uuid", "$2::text")+`), q.exclude,
			COALESCE((SELECT name FROM records WHERE id = NULLIF($4, '')::uuid AND workspace_id = $1), '')
		FROM sequences q WHERE q.workspace_id = $1 AND q.id = $3`, ws, email, seq, ct.CompanyID).Scan(&suppressed, &exclude, &company); err != nil {
		return pipeline.Result{}, err
	}
	stop := ""
	if suppressed {
		stop = OptedOut
	} else if why, err := customers.Excluded(ctx, s.pool, ws, ct.RecordID, exclude); err != nil {
		return pipeline.Result{}, err
	} else if why != "" {
		stop = OptedOut
	}
	var res connectors.LeadPushed
	if stop == "" {
		res, err = pushlead.Push(ctx, s.store, ws, *link, connectors.PushedLead{Email: email, FirstName: strings.TrimSpace(ct.FirstName),
			LastName: strings.TrimSpace(ct.LastName), Company: company, Website: customers.CompanyDomain(ct.Domain)})
		switch {
		case errors.Is(err, pushlead.ErrGone):
			return pause(it, "The sequencer connection for this campaign is gone. Connect it again on the Connectors page, then resume."), nil
		case errors.Is(err, pushlead.ErrCannotPush):
			return pause(it, "That sequencer connection can't add people to campaigns. Pick another campaign, then resume."), nil
		case err != nil && (connectors.KindOf(err) == connectors.ErrAuth || connectors.KindOf(err) == connectors.ErrInvalid):
			return pause(it, "The sequencer refused the connection or the campaign. Check it on the Connectors page, then resume."), nil
		case err != nil:
			return pipeline.Result{}, err
		}
		if res.Outcome == connectors.PushRefused {
			stop = SequencerRefused
		}
	}
	if stop != "" {
		var reasons []string
		if r := strings.TrimSpace(res.Reason); r != "" {
			reasons = []string{r}
		}
		if err := s.c.Stopped(ctx, it, stop, reasons); err != nil {
			return pipeline.Result{}, err
		}
		return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Skip, Code: stop}, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return pipeline.Result{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if res.Outcome == connectors.PushAdded {
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "sequence.lead_pushed",
			TargetType: "sequence", TargetID: seq, Data: map[string]any{"record_id": ct.RecordID, "integration_id": link.IntegrationID,
				"connector": link.Connector, "campaign_id": link.CampaignID, "external_id": res.ExternalID, "via": s.c.Via},
			Props: map[string]any{"connector": link.Connector}}); err != nil {
			return pipeline.Result{}, err
		}
	}
	// No Outflare lead: the sequencer holds the person now.
	if err := s.c.Enrolled(ctx, tx, it, seq, ""); err != nil {
		return pipeline.Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return pipeline.Result{}, err
	}
	return s.done(it), nil
}

// lead is the person's lead, made from the item's address when they have
// none.
func (s steps) lead(ctx context.Context, tx pgx.Tx, ws string, ct Contact) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM leads WHERE workspace_id = $1 AND record_id = $2 ORDER BY created_at LIMIT 1`, ws, ct.RecordID).Scan(&id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	email := strings.ToLower(strings.TrimSpace(ct.Email))
	if _, err := leads.Import(ctx, tx, ws, s.c.Via, nil, []leads.Lead{{Email: email, FirstName: strings.TrimSpace(ct.FirstName),
		LastName: strings.TrimSpace(ct.LastName), Title: strings.TrimSpace(ct.Title), LinkedInURL: strings.TrimSpace(ct.LinkedInURL),
		Website: customers.CompanyDomain(ct.Domain)}}); err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx, `SELECT id::text FROM leads WHERE workspace_id = $1 AND lower(email) = $2`, ws, email).Scan(&id)
	return id, err
}
