package adsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Changes to an ad account itself rather than one campaign (Google's
// Required Minimum Functionality, docs/paid-media.md): a website
// conversion action created, an account-level callout added or removed, a
// portfolio bidding strategy created or its target moved. Like a campaign
// change, each is a row (ad_account_changes) the API makes with its audit
// event, carried out here by the change job (JobChange, with the row's id
// as account_change_id) and read back from the platform afterwards.

// Account change kinds.
const (
	AccountConversionCreate = "conversion_action_create"
	AccountCalloutAdd       = "callout_add"
	AccountCalloutRemove    = "callout_remove"
	AccountPortfolioCreate  = "bidding_strategy_create"
	AccountPortfolioUpdate  = "bidding_strategy_update"
)

// AccountChangeKinds lists them, for the API's checks.
var AccountChangeKinds = []string{AccountConversionCreate, AccountCalloutAdd, AccountCalloutRemove, AccountPortfolioCreate, AccountPortfolioUpdate}

// AccountRequest is what an account change asks for, as kept in the row's
// request: the website conversion to create, the callout's text (and, to
// remove one, its id), or the strategy to create or change (and its id).
type AccountRequest struct {
	Conversion        *connectors.AdWebConversion `json:"conversion,omitempty"`
	Text              string                      `json:"text,omitempty"`
	CalloutID         string                      `json:"callout_id,omitempty"`
	BiddingStrategyID string                      `json:"bidding_strategy_id,omitempty"`
	Portfolio         *connectors.AdPortfolioSpec `json:"portfolio,omitempty"`
}

// AccountResult is what the platform made: the new conversion action's,
// callout's or strategy's id.
type AccountResult struct {
	ConversionActionID string `json:"conversion_action_id,omitempty"`
	CalloutID          string `json:"callout_id,omitempty"`
	BiddingStrategyID  string `json:"bidding_strategy_id,omitempty"`
}

// EnqueueAccountChange queues the change job for an ad_account_changes
// row, once.
func EnqueueAccountChange(ctx context.Context, q jobs.Querier, ws, changeID string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobChange, WorkspaceID: &ws, Payload: changePayload{AccountChange: changeID},
		IdempotencyKey: "ads.account_change:" + changeID, MaxAttempts: 6})
	return err
}

// ApplyAccountChange carries one queued account change out. last says this
// is the job's last try.
func (s *Syncer) ApplyAccountChange(ctx context.Context, ws, changeID string, last bool) error {
	var (
		kind, status, connector, integrationID string
		brand                                  *string
		request                                []byte
		acct                                   account
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT c.kind, c.status, c.request, c.brand_id, a.connector, a.integration_id, a.id, a.external_id, a.name, a.currency, a.time_zone, a.meta
		FROM ad_account_changes c JOIN ad_accounts a ON a.id = c.ad_account_id
		WHERE c.workspace_id = $1 AND c.id = $2`, ws, changeID).
		Scan(&kind, &status, &request, &brand, &connector, &integrationID, &acct.id, &acct.externalID, &acct.name, &acct.currency, &acct.timeZone, &acct.meta)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "queued" {
		return nil
	}
	var req AccountRequest
	_ = json.Unmarshal(request, &req)
	ev := track.Event{Name: "ad_change.applied", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"connector": connector, "kind": kind, "change_id": changeID, "level": "account"}}
	if brand != nil {
		ev.Props["brand_id"] = *brand
	}
	fail := func(outcome, code string, err error) error {
		metrics.AdChanged(connector, kind, outcome)
		ev.Name = "ad_change.failed"
		if outcome == "failed" {
			ev.Outcome = track.Failure
		}
		track.Fail(ctx, s.Log, ev, code, err)
		if outcome == "error" {
			return err
		}
		_, dbErr := s.Pool.Exec(ctx, `UPDATE ad_account_changes SET status = 'failed', error = $3, applied_at = now() WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`,
			ws, changeID, trim(err.Error(), 500))
		return dbErr
	}
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		if errors.Is(err, connectors.ErrNotFound) {
			return fail("failed", "connector_gone", errors.New("the ad platform connection is gone"))
		}
		return fail("error", "internal", err)
	}
	ad := acct.ad()
	name := o.Connector.Definition().Name
	invalid := func(what string) error {
		return fail("failed", "connector_invalid", fmt.Errorf("%s cannot %s here", name, what))
	}
	var res AccountResult
	// check reads the change back: whether the platform shows it, and if
	// not what it shows.
	var check func() (bool, string, error)
	switch kind {
	case AccountConversionCreate:
		m, ok := o.Connector.(connectors.AdConversionManager)
		if !ok || req.Conversion == nil {
			return invalid("create conversion actions")
		}
		res.ConversionActionID, err = m.CreateWebConversion(ctx, o.Client, o.Creds, ad, *req.Conversion)
		check = func() (bool, string, error) {
			list, err := m.AdConversionActions(ctx, o.Client, o.Creds, ad)
			if err != nil {
				return false, "", err
			}
			for _, x := range list {
				if x.ID == res.ConversionActionID {
					if len(x.Snippets) == 0 {
						return true, "made; Google shows its website code once it has generated it", nil
					}
					return true, "", nil
				}
			}
			return false, "the platform doesn't list the new conversion action yet", nil
		}
	case AccountCalloutAdd, AccountCalloutRemove:
		m, ok := o.Connector.(connectors.AdCalloutManager)
		if !ok {
			return invalid("change callouts")
		}
		want := req.CalloutID
		if kind == AccountCalloutAdd {
			res.CalloutID, err = m.AddCallout(ctx, o.Client, o.Creds, ad, req.Text)
			want = res.CalloutID
		} else {
			err = m.RemoveCallout(ctx, o.Client, o.Creds, ad, req.CalloutID)
		}
		check = func() (bool, string, error) {
			list, err := m.AdCallouts(ctx, o.Client, o.Creds, ad)
			if err != nil {
				return false, "", err
			}
			there := slices.ContainsFunc(list, func(x connectors.AdCallout) bool { return x.ID == want })
			switch {
			case kind == AccountCalloutAdd && !there:
				return false, "the platform doesn't list the callout on the account", nil
			case kind == AccountCalloutRemove && there:
				return false, "the platform still lists the callout on the account", nil
			}
			return true, "", nil
		}
	case AccountPortfolioCreate, AccountPortfolioUpdate:
		m, ok := o.Connector.(connectors.AdPortfolioManager)
		if !ok || req.Portfolio == nil {
			return invalid("change bidding strategies")
		}
		res.BiddingStrategyID = req.BiddingStrategyID
		if kind == AccountPortfolioCreate {
			res.BiddingStrategyID, err = m.CreatePortfolio(ctx, o.Client, o.Creds, ad, *req.Portfolio)
		} else {
			err = m.UpdatePortfolio(ctx, o.Client, o.Creds, ad, req.BiddingStrategyID, *req.Portfolio)
		}
		check = func() (bool, string, error) {
			list, err := m.AdPortfolios(ctx, o.Client, o.Creds, ad)
			if err != nil {
				return false, "", err
			}
			for _, x := range list {
				if x.ID != res.BiddingStrategyID {
					continue
				}
				p := req.Portfolio
				switch {
				case p.TargetCPAMicros != nil && (x.TargetCPAMicros == nil || *x.TargetCPAMicros != *p.TargetCPAMicros):
					return false, fmt.Sprintf("the platform shows a target cost per conversion of %s micros", budgetWord(x.TargetCPAMicros)), nil
				case p.TargetROAS != nil && (x.TargetROAS == nil || *x.TargetROAS != *p.TargetROAS):
					return false, "the platform shows another target return on spend", nil
				}
				return true, "", nil
			}
			return false, "the platform doesn't list the strategy", nil
		}
	default:
		return fail("failed", "connector_invalid", fmt.Errorf("unknown account change %q", kind))
	}
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrUnavailable && !last {
			return err
		}
		return fail("failed", "connector_"+connectors.KindOf(err), err)
	}
	now := s.now()
	verified, note, readErr := check()
	if readErr != nil {
		note = "could not read it back: " + trim(readErr.Error(), 300)
	}
	var verifiedAt *time.Time
	if verified {
		verifiedAt = &now
	}
	resJSON, _ := json.Marshal(res)
	if _, err := s.Pool.Exec(ctx, `UPDATE ad_account_changes SET status = 'applied', applied_at = $3, verified_at = $4, verify_note = $5, result = $6
		WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`, ws, changeID, now, verifiedAt, note, resJSON); err != nil {
		return fail("error", "internal", err)
	}
	outcome := "applied"
	if !verified {
		outcome = "unverified"
		ev.Props["unverified"] = true
	}
	metrics.AdChanged(connector, kind, outcome)
	track.Emit(ctx, ev)
	return nil
}
