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
	"sync"
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
	"github.com/outflareai/outflare/engine/internal/track"
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
	// CampaignNotReady: the sequencer says its campaign can't take people
	// now (paused, no sequence steps, no sending days, no connected
	// sender), or couldn't say; they wait at enroll until it can.
	CampaignNotReady = "campaign_not_ready"
	// CampaignDailyCapacity: the campaign took as many people today as its
	// senders can start; the rest wait at enroll for the next day (UTC).
	CampaignDailyCapacity = "campaign_daily_capacity"
	// PushFailed: the person could not be added after the feature's
	// EnrollAttempts and is set aside.
	PushFailed = "push_failed"
)

// ErrRecheckEmail is what a feature's Campaign returns for a person whose
// address must be checked again before they are added (the check is too
// old): the item goes back to find_email, and comes through the steps to
// enroll again.
var ErrRecheckEmail = errors.New("peoplesteps: check the address again")

// Later is the error a feature's Campaign returns for a person it can't
// decide about yet: a check of its own (may this person be contacted)
// that got no answer. The item stays at enroll and is asked about again
// after After (half a minute when zero), without counting an attempt, so
// an outage holds people for as long as it lasts and never adds them.
type Later struct {
	After time.Duration
	// Code is the feature's code for why, for the run page and events.
	Code string
	// Reason is a short note for the run page. Never in events.
	Reason string
}

func (l *Later) Error() string { return "peoplesteps: ask again later (" + l.Code + ")" }

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
	// releases it there, for people a person approves first. An error that
	// is a *Later asks again after a while without adding them and without
	// counting an attempt. When set, add_person goes on to enroll.
	// Optional.
	Campaign func(ctx context.Context, it pipeline.Item) (sequenceID string, hold bool, err error)
	// EnrollAttempts is how many times enroll tries to add a person before
	// setting them aside (Stopped with PushFailed). Zero is the pipeline's
	// default. Optional.
	EnrollAttempts int
	// Enrolled records, in tx, that an item's person was added to
	// sequenceID as leadID (or already was). Needed with Campaign.
	Enrolled func(ctx context.Context, tx pgx.Tx, it pipeline.Item, sequenceID, leadID string) error
	// Next is the step after add_person (or enroll); "" finishes the item
	// there.
	Next string
	// AfterFind is the feature's own stage an item goes to once find_email
	// has its address, before contact_check: a check of the person the
	// feature makes itself, which then sends the item on to ContactCheck
	// or skips it. "" (or no AfterFind) goes straight to contact_check.
	// Optional.
	AfterFind func(ctx context.Context, it pipeline.Item) (stage string, err error)
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
	s := steps{pool: pool, finder: finder, store: store, c: c, ready: &readyCache{seen: map[string]readySeen{}}}
	stages := []pipeline.Stage{
		// One finder call takes a whole batch, up to its own limit.
		{Pipeline: c.Pipeline, Name: FindEmail, Work: s.findEmail, Batch: 25},
		{Pipeline: c.Pipeline, Name: ContactCheck, Work: s.contactCheck},
		{Pipeline: c.Pipeline, Name: AddPerson, Work: s.addPerson},
	}
	if c.Campaign != nil {
		// MaxAttempts matches EnrollAttempts, where the last one sets the
		// person aside (retry).
		stages = append(stages, pipeline.Stage{Pipeline: c.Pipeline, Name: Enroll, Work: s.enroll, MaxAttempts: c.EnrollAttempts})
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
	// ready remembers campaigns found ready, for readyReuse.
	ready *readyCache
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
				Domain: customers.CompanyDomain(ct.Domain), LinkedInURL: strings.TrimSpace(ct.LinkedInURL)})
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
		if s.c.AfterFind != nil {
			next, err := s.c.AfterFind(ctx, it)
			if err != nil {
				return retry(it, err)
			}
			if next != "" {
				return advance(it, next)
			}
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
			Reason: "The workspace's email finder or verifier refused or ran out. Check it on the Connectors page, then resume."}
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

// enroll adds each person to the feature's campaign. People going into a
// connected sequencer's campaign are pushed together, one batch per
// campaign (pushBatch).
func (s steps) enroll(ctx context.Context, items []pipeline.Item) ([]pipeline.Result, error) {
	var out []pipeline.Result
	var order []string
	batches := map[string][]pending{}
	for _, it := range items {
		seq, hold, err := s.c.Campaign(ctx, it)
		var later *Later
		if errors.As(err, &later) {
			out = append(out, pipeline.Result{ItemID: it.ID, Outcome: pipeline.Wait, Code: later.Code, Reason: later.Reason, RetryAfter: later.After})
			continue
		}
		if errors.Is(err, ErrRecheckEmail) {
			out = append(out, advance(it, FindEmail))
			continue
		}
		if err != nil {
			out = append(out, s.retry(ctx, it, err))
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
		res, push, err := s.enrollOne(ctx, it, seq)
		switch {
		case err != nil:
			res = s.retry(ctx, it, err)
		case push != nil:
			key := push.link.IntegrationID + "\x00" + push.link.CampaignID
			if batches[key] == nil {
				order = append(order, key)
			}
			batches[key] = append(batches[key], *push)
			continue
		}
		out = append(out, res)
	}
	for _, key := range order {
		out = append(out, s.pushBatch(ctx, batches[key])...)
	}
	return out, nil
}

// retry tries an item again at enroll. With Config.EnrollAttempts, the
// last attempt sets the person aside instead (PushFailed), with the error
// as its reason, so someone who can't be added stops coming back.
func (s steps) retry(ctx context.Context, it pipeline.Item, err error) pipeline.Result {
	if n := s.c.EnrollAttempts; n <= 0 || it.Attempt < n {
		return retry(it, err)
	}
	if serr := s.c.Stopped(ctx, it, PushFailed, []string{err.Error()}); serr != nil {
		return retry(it, serr)
	}
	return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Skip, Code: PushFailed, Reason: err.Error()}
}

// enrollOne adds an item's person to an Outflare sequence, or, for one
// linked to a connected sequencer's campaign, says who to push there.
func (s steps) enrollOne(ctx context.Context, it pipeline.Item, seq string) (pipeline.Result, *pending, error) {
	ct, err := s.c.Contact(ctx, it)
	if err != nil {
		return pipeline.Result{}, nil, err
	}
	if ct.RecordID == "" {
		return pipeline.Result{}, nil, fmt.Errorf("peoplesteps: an item of %s reached enroll without a record", it.RunID)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return pipeline.Result{}, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	ws := it.WorkspaceID
	var status string
	var linked bool
	err = tx.QueryRow(ctx, `SELECT status, EXISTS (SELECT 1 FROM campaign_links l WHERE l.sequence_id = s.id) FROM sequences s
		WHERE s.workspace_id = $1 AND s.id = $2`, ws, seq).Scan(&status, &linked)
	if errors.Is(err, pgx.ErrNoRows) || status == "archived" {
		return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Pause, Code: CampaignUnavailable,
			Reason: "The campaign was removed or archived. Pick another campaign, then resume."}, nil, nil
	}
	if err != nil {
		return pipeline.Result{}, nil, err
	}
	if linked {
		_ = tx.Rollback(ctx)
		return s.preparePush(ctx, it, seq, ct)
	}
	leadID, err := s.lead(ctx, tx, ws, ct)
	if err != nil {
		return pipeline.Result{}, nil, err
	}
	var here, suppressed, elsewhere bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM enrollments WHERE sequence_id = $1 AND lead_id = $2),
			EXISTS (`+leads.SuppressedSQL("l.workspace_id", "l.email")+`),
			EXISTS (SELECT 1 FROM enrollments e JOIN sequences q ON q.id = e.sequence_id WHERE e.lead_id = l.id AND e.status = 'active'
				AND e.sequence_id <> $1 AND q.status IN ('active', 'paused'))
		FROM leads l WHERE l.id = $2`, seq, leadID).Scan(&here, &suppressed, &elsewhere); err != nil {
		return pipeline.Result{}, nil, err
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
			return pipeline.Result{}, nil, err
		}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, Action: "sequence.leads_enrolled", TargetType: "sequence", TargetID: seq,
			ActorType: "system", Data: map[string]any{"enrolled": 1, "suppressed": 0, "active_elsewhere": 0, "via": s.c.Via}}); err != nil {
			return pipeline.Result{}, nil, err
		}
	}
	if stop != "" {
		if err := tx.Commit(ctx); err != nil {
			return pipeline.Result{}, nil, err
		}
		if err := s.c.Stopped(ctx, it, stop, nil); err != nil {
			return pipeline.Result{}, nil, err
		}
		return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Skip, Code: stop}, nil, nil
	}
	if err := s.c.Enrolled(ctx, tx, it, seq, leadID); err != nil {
		return pipeline.Result{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return pipeline.Result{}, nil, err
	}
	return s.done(it), nil, nil
}

// pause stops the run at enroll with a reason a person can act on.
func pause(it pipeline.Item, reason string) pipeline.Result {
	return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Pause, Code: CampaignUnavailable, Reason: reason}
}

// pending is a person checked and ready to be pushed into a sequencer's
// campaign.
type pending struct {
	it   pipeline.Item
	seq  string
	ct   Contact
	link pushlead.Link
	lead connectors.PushedLead
}

// Note is what a push writes on the contact it creates in a sequencer:
// the feature and the item, so the contact is known as this push's own.
func Note(via, ref string) string { return "Outflare " + via + " " + ref }

// preparePush checks a person against what enrolling and journeys check
// before a push into the platform campaign a sequence is linked to
// (Smartlead, Instantly, EmailBison): the opt-out list and the sequence's
// exclusion. The sequencer paces and sends from there.
func (s steps) preparePush(ctx context.Context, it pipeline.Item, seq string, ct Contact) (pipeline.Result, *pending, error) {
	ws := it.WorkspaceID
	var reg *connectors.Registry
	if s.store != nil {
		reg = s.store.Registry
	}
	link, _, err := pushlead.LinkOf(ctx, s.pool, reg, ws, seq)
	if err != nil {
		return pipeline.Result{}, nil, err
	}
	if link == nil {
		return pause(it, "The campaign runs in a sequencer Outflare can't add people to. Pick another campaign, then resume."), nil, nil
	}
	email := strings.ToLower(strings.TrimSpace(ct.Email))
	var suppressed bool
	var exclude customers.Exclusion
	var company string
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (`+leads.SuppressedSQL("$1::uuid", "$2::text")+`), q.exclude,
			COALESCE((SELECT name FROM records WHERE id = NULLIF($4, '')::uuid AND workspace_id = $1), '')
		FROM sequences q WHERE q.workspace_id = $1 AND q.id = $3`, ws, email, seq, ct.CompanyID).Scan(&suppressed, &exclude, &company); err != nil {
		return pipeline.Result{}, nil, err
	}
	stop := ""
	if suppressed {
		stop = OptedOut
	} else if why, err := customers.Excluded(ctx, s.pool, ws, ct.RecordID, exclude); err != nil {
		return pipeline.Result{}, nil, err
	} else if why != "" {
		stop = OptedOut
	}
	if stop != "" {
		if err := s.c.Stopped(ctx, it, stop, nil); err != nil {
			return pipeline.Result{}, nil, err
		}
		return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Skip, Code: stop}, nil, nil
	}
	return pipeline.Result{}, &pending{it: it, seq: seq, ct: ct, link: *link, lead: connectors.PushedLead{Email: email,
		FirstName: strings.TrimSpace(ct.FirstName), LastName: strings.TrimSpace(ct.LastName), Company: company,
		Website: customers.CompanyDomain(ct.Domain), Note: Note(s.c.Via, it.Ref)}}, nil
}

// pushBatch pushes the people of one platform campaign together. First it
// reads whether the campaign is ready, when its sequencer can say
// (connectors.CampaignReadiness, read at most every readyReuse while it
// stays ready): a campaign that isn't holds its people at enroll, without
// using up an attempt, and so does one that took its daily capacity today.
// Then everyone left goes in, in as few calls as the sequencer allows.
func (s steps) pushBatch(ctx context.Context, batch []pending) []pipeline.Result {
	out := make([]pipeline.Result, 0, len(batch))
	ws, link := batch[0].it.WorkspaceID, batch[0].link
	waitAll := func(rows []pending, code, reason string, after time.Duration) {
		for _, p := range rows {
			out = append(out, pipeline.Result{ItemID: p.it.ID, Outcome: pipeline.Wait, Code: code, Reason: reason, RetryAfter: after})
		}
	}
	ready, err := s.readiness(ctx, ws, link)
	switch {
	case errors.Is(err, pushlead.ErrGone):
		for _, p := range batch {
			out = append(out, pause(p.it, "The sequencer connection for this campaign is gone. Connect it again on the Connectors page, then resume."))
		}
		return out
	case err != nil && (connectors.KindOf(err) == connectors.ErrAuth || connectors.KindOf(err) == connectors.ErrInvalid):
		for _, p := range batch {
			out = append(out, pause(p.it, "The sequencer refused the connection or the campaign. Check it on the Connectors page, then resume."))
		}
		return out
	case err != nil:
		track.Fail(ctx, nil, track.Event{Name: "sequencer.readiness_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"connector": link.Connector, "integration_id": link.IntegrationID, "people": len(batch)}}, "connector_unavailable", err)
		waitAll(batch, CampaignNotReady, "The sequencer could not say whether the campaign is ready; asked again shortly.", readyRecheck)
		return out
	case ready != nil && !ready.Ready():
		track.Emit(ctx, track.Event{Name: "sequencer.campaign_not_ready", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Outcome: track.Failure, ErrorCode: CampaignNotReady,
			Props: map[string]any{"connector": link.Connector, "integration_id": link.IntegrationID, "people": len(batch), "reasons": len(ready.Reasons)}})
		waitAll(batch, CampaignNotReady, strings.Join(ready.Reasons, " "), readyRecheck)
		return out
	}
	if ready != nil {
		// The campaign's daily capacity, counted from the people pushed
		// into it since midnight UTC.
		var pushed int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'sequence.lead_pushed'
			AND created_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
			AND data->>'integration_id' = $2 AND data->>'campaign_id' = $3`, ws, link.IntegrationID, link.CampaignID).Scan(&pushed); err != nil {
			for _, p := range batch {
				out = append(out, s.retry(ctx, p.it, err))
			}
			return out
		}
		room := max(0, ready.DailyCapacity-pushed)
		if room < len(batch) {
			now := time.Now().UTC()
			tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
			waitAll(batch[room:], CampaignDailyCapacity, "The campaign took as many new people today as its senders can start; the rest go in tomorrow.", tomorrow.Sub(now)+time.Minute)
			batch = batch[:room]
			if len(batch) == 0 {
				return out
			}
		}
	}
	leadsIn := make([]connectors.PushedLead, len(batch))
	for i, p := range batch {
		leadsIn[i] = p.lead
	}
	results := pushlead.PushMany(ctx, s.store, ws, link, leadsIn)
	for i, p := range batch {
		out = append(out, s.pushed(ctx, p, results[i]))
	}
	return out
}

// pushed records what a push did for one person.
func (s steps) pushed(ctx context.Context, p pending, r connectors.PushOutcome) pipeline.Result {
	it, ws, link, res, err := p.it, p.it.WorkspaceID, p.link, r.LeadPushed, r.Err
	switch {
	case errors.Is(err, pushlead.ErrGone):
		return pause(it, "The sequencer connection for this campaign is gone. Connect it again on the Connectors page, then resume.")
	case errors.Is(err, pushlead.ErrCannotPush):
		return pause(it, "That sequencer connection can't add people to campaigns. Pick another campaign, then resume.")
	case err != nil && (connectors.KindOf(err) == connectors.ErrAuth || connectors.KindOf(err) == connectors.ErrInvalid):
		return pause(it, "The sequencer refused the connection or the campaign. Check it on the Connectors page, then resume.")
	case err != nil:
		return s.retry(ctx, it, err)
	}
	if res.Outcome == connectors.PushRefused {
		var reasons []string
		if r := strings.TrimSpace(res.Reason); r != "" {
			reasons = []string{r}
		}
		if err := s.c.Stopped(ctx, it, SequencerRefused, reasons); err != nil {
			return s.retry(ctx, it, err)
		}
		return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Skip, Code: SequencerRefused}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if res.Outcome == connectors.PushAdded {
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "sequence.lead_pushed",
				TargetType: "sequence", TargetID: p.seq, Data: map[string]any{"record_id": p.ct.RecordID, "integration_id": link.IntegrationID,
					"connector": link.Connector, "campaign_id": link.CampaignID, "external_id": res.ExternalID, "via": s.c.Via},
				Props: map[string]any{"connector": link.Connector}}); err != nil {
				return err
			}
		}
		// No Outflare lead: the sequencer holds the person now.
		return s.c.Enrolled(ctx, tx, it, p.seq, "")
	})
	if err != nil {
		return s.retry(ctx, it, err)
	}
	return s.done(it)
}

// readyReuse is how long a campaign found ready stands for later batches;
// readyRecheck how long people wait before a campaign that isn't ready (or
// couldn't say) is read again.
const (
	readyReuse   = 2 * time.Minute
	readyRecheck = 5 * time.Minute
)

type readyCache struct {
	mu   sync.Mutex
	seen map[string]readySeen
}

type readySeen struct {
	at    time.Time
	ready connectors.Readiness
}

// readiness is whether link's campaign is ready, nil when its sequencer
// can't say. Only an answer that it is ready is reused, for readyReuse.
func (s steps) readiness(ctx context.Context, ws string, link pushlead.Link) (*connectors.Readiness, error) {
	key := ws + "\x00" + link.IntegrationID + "\x00" + link.CampaignID
	if s.ready != nil {
		s.ready.mu.Lock()
		got, ok := s.ready.seen[key]
		s.ready.mu.Unlock()
		if ok && time.Since(got.at) < readyReuse {
			return &got.ready, nil
		}
	}
	r, err := pushlead.Ready(ctx, s.store, ws, link)
	if err != nil || r == nil {
		return r, err
	}
	if s.ready != nil {
		s.ready.mu.Lock()
		if r.Ready() {
			s.ready.seen[key] = readySeen{at: time.Now(), ready: *r}
		} else {
			delete(s.ready.seen, key)
		}
		s.ready.mu.Unlock()
	}
	return r, nil
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
