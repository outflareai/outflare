package api

import (
	"context"
	"net/http"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"time"
)

// SetCRMImportSizes makes CRM import pages, batches and steps small, so a
// test covers paging and steps with a few records. It returns the undo.
func SetCRMImportSizes(page, batch int, step time.Duration) func() {
	p, b, s := crmImportPage, crmImportBatch, crmImportStep
	crmImportPage, crmImportBatch, crmImportStep = page, batch, step
	return func() { crmImportPage, crmImportBatch, crmImportStep = p, b, s }
}

// SetUndoInline sets how many rows a batch undo does in the request
// before it runs in a job, and returns a func that puts it back.
func SetUndoInline(n int) func() {
	old := undoInline
	undoInline = n
	return func() { undoInline = old }
}

// SyncPayments runs one payment sync now, as its job would.
func (s *Server) SyncPayments(ctx context.Context, ws, id string) error {
	return s.syncPayments(ctx, ws, id)
}

// SchedulePayments queues the payment syncs that are due, as the loop would.
func (s *Server) SchedulePayments(ctx context.Context) error { return s.schedulePayments(ctx) }

// SetPartnerPostbackClient swaps the client postbacks to partners'
// trackers use (the real one only reaches public addresses). It returns
// the undo.
func SetPartnerPostbackClient(c *http.Client) func() {
	old := partnerPostbackClient
	partnerPostbackClient = c
	return func() { partnerPostbackClient = old }
}

// SetSourcePostbackClient swaps the client postbacks to traffic sources
// use. It returns the undo.
func SetSourcePostbackClient(c *http.Client) func() {
	old := sourcePostbackClient
	sourcePostbackClient = c
	return func() { sourcePostbackClient = old }
}

// ApproveDuePartnerRewards runs one pass of the loop that approves rewards
// whose hold ended.
func (s *Server) ApproveDuePartnerRewards(ctx context.Context) error {
	return s.approveDuePartnerRewards(ctx)
}

// SetReviewJobTimes makes review page jobs poll every poll and a round
// wait up to wait for one, so a test covers a slow job in milliseconds. It
// returns the undo.
func SetReviewJobTimes(poll, wait time.Duration) func() {
	p, w := reviewJobPoll, reviewJobWait
	reviewJobPoll, reviewJobWait = poll, wait
	return func() { reviewJobPoll, reviewJobWait = p, w }
}

// DeprecateOperation marks an operation as on its way out, so a test can
// see install health warn about calls to it. It returns the undo.
func DeprecateOperation(op, instead string) func() {
	deprecatedOperations[op] = deprecatedOperation{instead: instead}
	return func() { delete(deprecatedOperations, op) }
}

// SetPrivacyExportLimit lowers the size a privacy export may reach before
// it fails with too_large. It returns the undo.
func SetPrivacyExportLimit(n int64) func() {
	old := exportLimit.Swap(n)
	return func() { exportLimit.Store(old) }
}

// SetLinkJobWait shortens how the link finder waits for a page job; the
// returned func restores it.
func SetLinkJobWait(poll, wait time.Duration) func() {
	p, w := linkJobPoll, linkJobWait
	linkJobPoll, linkJobWait = poll, wait
	return func() { linkJobPoll, linkJobWait = p, w }
}

// RefreshPresenceTraction runs the daily traction refresh for one brand,
// as the discover job's last step does.
func (s *Server) RefreshPresenceTraction(ctx context.Context, ws, brand string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	n, err := coverageRefreshTraction(ctx, tx, ws, brand)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// CompetitorNamesFor writes names as a brand's competitor list knows them.
func (s *Server) CompetitorNamesFor(ctx context.Context, ws string, brand *string, names []string) ([]string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	return competitorNamesFor(ctx, tx, ws, brand, names)
}

// ResolvePresenceRedirects runs the step of daily discovery that replaces
// Google redirect links among a brand's finds with their pages.
func (s *Server) ResolvePresenceRedirects(ctx context.Context, ws, brand string) (int, int, error) {
	return s.coverageResolveRedirects(ctx, &access{WorkspaceID: ws, Brand: brand})
}

// RunCoverageVerify runs one read of a brand's unchecked finds.
func (s *Server) RunCoverageVerify(ctx context.Context, ws, brand string) error {
	return s.coverageVerifyJob(ctx, jobs.Job{ID: 1, WorkspaceID: &ws, Payload: []byte(`{"brand_id":"` + brand + `"}`)})
}

// RunPartnershipSync runs a partnership's stage actions.
func (s *Server) RunPartnershipSync(ctx context.Context, ws, record string) error {
	return s.partnershipSyncJob(ctx, jobs.Job{ID: 1, WorkspaceID: &ws, Payload: []byte(`{"record_id":"` + record + `"}`)})
}

// QueuePartnershipSyncs runs the daily sweep's partnership step.
func (s *Server) QueuePartnershipSyncs(ctx context.Context) error {
	return s.queuePartnershipSyncs(ctx)
}

// RunAffiliateSync brings a partner's affiliate partnerships up to date.
func (s *Server) RunAffiliateSync(ctx context.Context, ws, partner string) error {
	return s.partnershipAffiliateJob(ctx, jobs.Job{ID: 1, WorkspaceID: &ws, Payload: []byte(`{"partner_id":"` + partner + `"}`)})
}

// RunMailPrivacyCheck runs one email privacy check for a brand.
// CheckEventNames runs the event name check for a brand now, and says
// whether more names wait.
func (s *Server) CheckEventNames(ctx context.Context, ws, brand string) (bool, error) {
	return s.checkEventNames(ctx, ws, brand)
}

func (s *Server) RunMailPrivacyCheck(ctx context.Context, ws, brand string) error {
	return s.crmMailPrivacyJob(ctx, jobs.Job{ID: 1, WorkspaceID: &ws, Payload: []byte(`{"brand_id":"` + brand + `"}`)})
}

// RunWorkspaceCalendar reads one Workspace-read member's calendar once.
func (s *Server) RunWorkspaceCalendar(ctx context.Context, ws, connection string) error {
	return s.crmCalendarWorkspaceJob(ctx, jobs.Job{ID: 1, WorkspaceID: &ws, Payload: []byte(`{"connection_id":"` + connection + `"}`)})
}
