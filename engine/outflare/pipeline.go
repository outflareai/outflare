package outflare

import (
	"context"

	"github.com/outflareai/outflare/engine/internal/companysteps"
	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/peoplesteps"
	"github.com/outflareai/outflare/engine/internal/pipeline"
)

// Pipelines: work that moves through stages one item at a time, such as
// a lead finder run. See docs/pipeline.md.
type (
	Stage          = pipeline.Stage
	PipelineItem   = pipeline.Item
	PipelineResult = pipeline.Result
	PipelineNew    = pipeline.NewItem
	PipelineRun    = pipeline.RunView
	PipelineStage  = pipeline.StageCount
	Rate           = pipeline.Rate
	Outcome        = pipeline.Outcome
)

// What a stage decides for an item (PipelineResult.Outcome).
const (
	Advance = pipeline.Advance
	Done    = pipeline.Done
	Skip    = pipeline.Skip
	Hold    = pipeline.Hold
	Retry   = pipeline.Retry
	Wait    = pipeline.Wait
	Pause   = pipeline.Pause
	Fail    = pipeline.Fail
)

// Call outcomes for Limits.Report.
const (
	CallSucceeded   = pipeline.Succeeded
	CallRateLimited = pipeline.RateLimited
	CallFailed      = pipeline.Failed
)

// ErrRunNotRunning means the run is cancelled or finished.
var ErrRunNotRunning = pipeline.ErrNotRunning

// Pipeline registers stages and works with runs.
type Pipeline struct{ h *Host }

// Pipeline is the pipeline API.
func (h *Host) Pipeline() *Pipeline { return &Pipeline{h} }

// Register adds a stage. Call it in Setup; it panics on a bad stage.
// Stages run in worker processes.
func (p *Pipeline) Register(s Stage) { p.h.stages.Register(s) }

// CompanySteps is a feature's use of the engine's shared steps for
// companies (docs/pipeline.md, "Shared steps for companies"): known,
// contact_rules and add_to_crm, for items whose ref is a company's domain.
type CompanySteps = companysteps.Config

// The shared company steps, in order: send an item to StepKnown.
const (
	StepKnown        = companysteps.Known
	StepContactRules = companysteps.ContactRules
	StepAddToCRM     = companysteps.AddToCRM
)

// RegisterCompanySteps adds the shared company steps to c.Pipeline. Call
// it in Setup; it panics on a Config missing Via or a callback.
func (p *Pipeline) RegisterCompanySteps(c CompanySteps) {
	for _, s := range companysteps.Stages(p.h.Pool, c) {
		p.Register(s)
	}
}

// PeopleSteps is a feature's use of the engine's shared steps for people
// (docs/pipeline.md, "Shared steps for people"): find_email,
// contact_check and add_person, and enroll when the feature names a
// campaign, for items that are each one person.
type (
	PeopleSteps = peoplesteps.Config
	Contact     = peoplesteps.Contact
	EmailCode   = emailfind.Code
	EmailCost   = emailfind.Cost
	// EmailFinder is what PeopleSteps.Finder replaces the engine's email
	// finder with, in tests.
	EmailFinder        = peoplesteps.Finder
	EmailFindRequest   = emailfind.FindRequest
	EmailVerifyRequest = emailfind.VerifyRequest
	EmailFindResult    = emailfind.FindResult
	EmailItemResult    = emailfind.ItemResult
)

// What the email finder says about one address (EmailItemResult.Code).
const (
	EmailFound         = emailfind.CodeFound
	EmailVerified      = emailfind.CodeVerified
	EmailRisky         = emailfind.CodeRisky
	EmailNotFound      = emailfind.CodeNotFound
	EmailUndeliverable = emailfind.CodeUndeliverable
	EmailPending       = emailfind.CodePending
	EmailRetry         = emailfind.CodeRetry
	EmailStop          = emailfind.CodeStop
)

// The shared people steps, in order: send an item to StepFindEmail.
const (
	StepFindEmail    = peoplesteps.FindEmail
	StepContactCheck = peoplesteps.ContactCheck
	StepAddPerson    = peoplesteps.AddPerson
	StepEnroll       = peoplesteps.Enroll
)

// Codes the enroll step skips or pauses with.
const (
	EnrollInAnotherCampaign   = peoplesteps.InAnotherCampaign
	EnrollCampaignUnavailable = peoplesteps.CampaignUnavailable
	EnrollCampaignReady       = peoplesteps.CampaignReady
)

// RegisterPeopleSteps adds the shared people steps to c.Pipeline. Call it
// in Setup; it panics on a Config missing Via or a callback, or when the
// engine has no email finder.
func (p *Pipeline) RegisterPeopleSteps(c PeopleSteps) {
	var finder peoplesteps.Finder
	if p.h.emailFinder != nil {
		finder = p.h.emailFinder
	}
	for _, s := range peoplesteps.Stages(p.h.Pool, finder, c) {
		p.Register(s)
	}
}

// OnSettled runs fn when a run of pipeline has nothing waiting or being
// worked on. It may run more than once for a run; make it idempotent.
func (p *Pipeline) OnSettled(name string, fn func(ctx context.Context, workspaceID, runID string) error) {
	p.h.stages.OnSettled(name, fn)
}

// Limits are rate limits and breakers per outside account, shared by
// every worker.
func (p *Pipeline) Limits() pipeline.Limits { return pipeline.Limits{Q: p.h.Pool} }

// LimitKey names an account for Limits, such as LimitKey("integration",
// connection.ID).
func LimitKey(kind, id string) string { return pipeline.Key(kind, id) }

// Runs works with one workspace's runs. Every call checks the run is the
// workspace's.
type Runs struct{ ws string }

// RunsFor is the caller's workspace's runs, for request handlers.
func (p *Pipeline) RunsFor(c *Caller) Runs { return Runs{c.WorkspaceID} }

// Runs is a workspace's runs, for background jobs. workspaceID must come
// from a row the job already holds for that workspace, never from input.
func (p *Pipeline) Runs(workspaceID string) Runs { return Runs{workspaceID} }

// Start creates a run with the feature's own run id, in q's transaction.
func (r Runs) Start(ctx context.Context, q pipeline.Querier, name, runID string) error {
	return pipeline.Start(ctx, q, r.ws, name, runID)
}

// Add puts items in a run; a ref already there is left alone.
func (r Runs) Add(ctx context.Context, q pipeline.Querier, runID string, items []PipelineNew) (int, error) {
	return pipeline.Add(ctx, q, r.ws, runID, items)
}

// Pause stops a run with a code and a reason for the run page.
func (r Runs) Pause(ctx context.Context, q pipeline.Querier, runID, code, reason string) error {
	return pipeline.PauseRun(ctx, q, r.ws, runID, code, reason)
}

// Resume lets a paused run carry on.
func (r Runs) Resume(ctx context.Context, q pipeline.Querier, runID string) error {
	return pipeline.ResumeRun(ctx, q, r.ws, runID)
}

// Cancel ends a run, skipping its unfinished items.
func (r Runs) Cancel(ctx context.Context, q pipeline.Querier, runID string) error {
	return pipeline.CancelRun(ctx, q, r.ws, runID)
}

// Finish marks a run finished once the feature is done with it.
func (r Runs) Finish(ctx context.Context, q pipeline.Querier, runID string) error {
	return pipeline.FinishRun(ctx, q, r.ws, runID)
}

// Release sends held items on to stage, or skips them with code when stage
// is "".
func (r Runs) Release(ctx context.Context, q pipeline.Querier, runID string, refs []string, stage, code string) (int, error) {
	return pipeline.Release(ctx, q, r.ws, runID, refs, stage, code)
}

// View reads a run and its counts per stage (pgx.ErrNoRows if it isn't
// the workspace's).
func (r Runs) View(ctx context.Context, q pipeline.Querier, runID string) (PipelineRun, error) {
	return pipeline.View(ctx, q, r.ws, runID)
}
