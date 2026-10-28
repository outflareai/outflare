// Package companysteps holds the pipeline steps any feature that finds
// companies can share (docs/pipeline.md, "Shared steps for companies").
// An item is one company and its ref is the company's domain. The steps
// run in this order:
//
//	known          the workspace already has the company: link the item to
//	               its record and stop
//	contact_rules  the domain is on the opt-out list as an opt-out or a
//	               bounce: stop; a competitor, customer or partner entry
//	               goes on with a warning
//	add_to_crm     add the company as a prospect record (records.acquired_via
//	               names the feature) and link the item to it
//
// The feature keeps its own data: it says which company an item is
// (Config.Company) and records what happened to it (Config.Linked and
// Config.Stopped).
package companysteps

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/pipeline"
	"github.com/outflareai/outflare/engine/internal/policy"
)

// Step names, in order. The first is where a feature sends an item.
const (
	Known        = "known"
	ContactRules = "contact_rules"
	AddToCRM     = "add_to_crm"
)

// Codes the steps skip an item with.
const (
	AlreadyKnown = "already_known"
	OptedOut     = "opted_out"
	Bounced      = "bounced"
	NotACompany  = "not_a_company"
)

// Config is a feature's use of the steps.
type Config struct {
	// Pipeline is the feature's pipeline, which the steps join.
	Pipeline string
	// Via is the tool new records came from (records.acquired_via), such
	// as lead_finder.
	Via string
	// Company is the company an item stands for and the brand to add it
	// for (nil for every brand), from the feature's own data.
	Company func(ctx context.Context, it pipeline.Item) (customers.Company, *string, error)
	// Linked records, in tx, that an item's company is recordID: a record
	// the workspace already had (existed) or one just added. Warnings are
	// opt-out list entries that let it through (suppressed_domain:customer).
	Linked func(ctx context.Context, tx pgx.Tx, it pipeline.Item, recordID string, existed bool, warnings []string) error
	// Stopped records that the contact rules stopped an item, with the
	// skip code (opted_out, bounced) and the opt-out list entries behind it.
	Stopped func(ctx context.Context, it pipeline.Item, code string, reasons []string) error
	// Next is the step after add_to_crm; "" finishes the item there.
	Next string
}

// Stages are the steps for c, to register on the pipeline runner.
func Stages(pool *pgxpool.Pool, c Config) []pipeline.Stage {
	if c.Company == nil || c.Linked == nil || c.Stopped == nil || c.Via == "" {
		panic(fmt.Sprintf("companysteps: %q needs Via, Company, Linked and Stopped", c.Pipeline))
	}
	s := steps{pool: pool, c: c}
	return []pipeline.Stage{
		{Pipeline: c.Pipeline, Name: Known, Work: s.known},
		{Pipeline: c.Pipeline, Name: ContactRules, Work: s.contactRules},
		{Pipeline: c.Pipeline, Name: AddToCRM, Work: s.addToCRM},
	}
}

type steps struct {
	pool *pgxpool.Pool
	c    Config
}

func retry(it pipeline.Item, err error) pipeline.Result {
	return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Retry, Code: "internal", Reason: err.Error()}
}

func skip(it pipeline.Item, code string) pipeline.Result {
	return pipeline.Result{ItemID: it.ID, Outcome: pipeline.Skip, Code: code}
}

// byWorkspace groups items, since every query is for one workspace.
func byWorkspace(items []pipeline.Item) map[string][]pipeline.Item {
	out := map[string][]pipeline.Item{}
	for _, it := range items {
		out[it.WorkspaceID] = append(out[it.WorkspaceID], it)
	}
	return out
}

// known links items whose domain is already a company record of the
// workspace, and sends the rest on.
func (s steps) known(ctx context.Context, items []pipeline.Item) ([]pipeline.Result, error) {
	var out []pipeline.Result
	for ws, group := range byWorkspace(items) {
		domains := make([]string, len(group))
		for i, it := range group {
			domains[i] = customers.CompanyDomain(it.Ref)
		}
		rows, err := s.pool.Query(ctx, `
			SELECT DISTINCT ON (i.value) i.value, i.record_id::text
			FROM record_identities i JOIN records r ON r.id = i.record_id AND r.record_type = 'account' AND r.merged_into IS NULL
			WHERE i.workspace_id = $1 AND i.namespace = 'domain' AND i.value = ANY($2) AND i.valid_to IS NULL
			ORDER BY i.value, i.verified DESC, i.created_at`, ws, domains)
		if err != nil {
			return nil, err
		}
		have := map[string]string{}
		for rows.Next() {
			var d, id string
			if err := rows.Scan(&d, &id); err != nil {
				rows.Close()
				return nil, err
			}
			have[d] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for i, it := range group {
			switch id, ok := have[domains[i]]; {
			case domains[i] == "":
				out = append(out, skip(it, NotACompany))
			case !ok:
				out = append(out, pipeline.Result{ItemID: it.ID, Outcome: pipeline.Advance, Next: ContactRules})
			default:
				if err := s.link(ctx, it, id, true, nil); err != nil {
					out = append(out, retry(it, err))
					continue
				}
				out = append(out, skip(it, AlreadyKnown))
			}
		}
	}
	return out, nil
}

func (s steps) link(ctx context.Context, it pipeline.Item, recordID string, existed bool, warnings []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.c.Linked(ctx, tx, it, recordID, existed, warnings); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// hard says whether an opt-out list entry stops a company: opt-outs and
// bounces. Competitor, customer and partner entries only warn.
func hard(reason string) (string, bool) {
	_, why, _ := strings.Cut(reason, ":")
	switch why {
	case "bounced":
		return Bounced, true
	case "competitor", "customer", "partner":
		return "", false
	}
	return OptedOut, true
}

// check splits a domain's opt-out list entries into what stops it (with
// the skip code) and what only warns.
func check(d policy.DomainDecision) (code string, stops, warnings []string) {
	for _, r := range d.Reasons {
		if c, ok := hard(r); ok {
			if code == "" || c == Bounced {
				code = c
			}
			stops = append(stops, r)
		} else {
			warnings = append(warnings, r)
		}
	}
	return code, stops, warnings
}

// contactRules stops companies the workspace must not contact.
func (s steps) contactRules(ctx context.Context, items []pipeline.Item) ([]pipeline.Result, error) {
	var out []pipeline.Result
	for ws, group := range byWorkspace(items) {
		domains := make([]string, len(group))
		for i, it := range group {
			domains[i] = customers.CompanyDomain(it.Ref)
		}
		decisions, err := policy.CheckDomains(ctx, s.pool, ws, policy.PurposeCold, domains)
		if err != nil {
			return nil, err
		}
		for i, it := range group {
			code, stops, _ := check(decisions[i])
			if code == "" {
				out = append(out, pipeline.Result{ItemID: it.ID, Outcome: pipeline.Advance, Next: AddToCRM})
				continue
			}
			if err := s.c.Stopped(ctx, it, code, stops); err != nil {
				out = append(out, retry(it, err))
				continue
			}
			out = append(out, skip(it, code))
		}
	}
	return out, nil
}

// addToCRM adds each company as a prospect record, or finds the one
// added since the known step, with its contact rule warnings.
func (s steps) addToCRM(ctx context.Context, items []pipeline.Item) ([]pipeline.Result, error) {
	var out []pipeline.Result
	for _, it := range items {
		if err := s.add(ctx, it); err != nil {
			out = append(out, retry(it, err))
			continue
		}
		if s.c.Next == "" {
			out = append(out, pipeline.Result{ItemID: it.ID, Outcome: pipeline.Done})
		} else {
			out = append(out, pipeline.Result{ItemID: it.ID, Outcome: pipeline.Advance, Next: s.c.Next})
		}
	}
	return out, nil
}

func (s steps) add(ctx context.Context, it pipeline.Item) error {
	company, brand, err := s.c.Company(ctx, it)
	if err != nil {
		return err
	}
	company.Domain = customers.CompanyDomain(it.Ref)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	decisions, err := policy.CheckDomains(ctx, tx, it.WorkspaceID, policy.PurposeCold, []string{company.Domain})
	if err != nil {
		return err
	}
	_, _, warnings := check(decisions[0])
	ids, err := customers.EnsureCompanies(ctx, tx, it.WorkspaceID, brand, s.c.Via, []customers.Company{company})
	if err != nil {
		return err
	}
	id, ok := ids[company.Domain]
	if !ok {
		return fmt.Errorf("companysteps: no record for %s", company.Domain)
	}
	if err := s.c.Linked(ctx, tx, it, id, false, warnings); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
