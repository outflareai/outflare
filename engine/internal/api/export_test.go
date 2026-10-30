package api

import (
	"context"
	"net/http"
	"time"
)

// SetCRMImportSizes makes CRM import pages, batches and steps small, so a
// test covers paging and steps with a few records. It returns the undo.
func SetCRMImportSizes(page, batch int, step time.Duration) func() {
	p, b, s := crmImportPage, crmImportBatch, crmImportStep
	crmImportPage, crmImportBatch, crmImportStep = page, batch, step
	return func() { crmImportPage, crmImportBatch, crmImportStep = p, b, s }
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
