package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leadfeed"
	"github.com/outflareai/outflare/engine/internal/mentions"
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

// SetLanderCheckClient swaps the client that asks landers and offers for
// their page
// (the real one only reaches public addresses). It returns the undo.
func SetLanderCheckClient(c *http.Client) func() {
	old := landerCheckClient
	landerCheckClient = c
	return func() { landerCheckClient = old }
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

// RunCRMMailSend runs one queued send's job once.
func (s *Server) RunCRMMailSend(ctx context.Context, ws, send string) error {
	return s.crmMailSendJob(ctx, jobs.Job{ID: 1, WorkspaceID: &ws, Payload: []byte(`{"send_id":"` + send + `"}`)})
}

// RunConversationRead reads one conversation for signals once.
func (s *Server) RunConversationRead(ctx context.Context, ws, brand, channel, ref string) error {
	return s.conversationJob(ctx, jobs.Job{ID: 1, WorkspaceID: &ws, Payload: []byte(`{"brand_id":"` + brand + `","channel":"` + channel + `","ref":"` + ref + `"}`)})
}

// SetLoopInEmailAfter changes how long a loop-in waits unopened before
// its email. It returns the undo.
func SetLoopInEmailAfter(d time.Duration) func() {
	was := loopInEmailAfter
	loopInEmailAfter = d
	return func() { loopInEmailAfter = was }
}

// RunLoopInEmail runs the email job of one loop-in once.
func (s *Server) RunLoopInEmail(ctx context.Context, ws, kind, id, user string) error {
	return s.loopInEmailJob(ctx, jobs.Job{ID: 1, WorkspaceID: &ws, Payload: []byte(`{"kind":"` + kind + `","id":"` + id + `","user_id":"` + user + `"}`)})
}

// SetCatalogSheetFetch replaces the Google Sheet download for the Link
// orders catalog, so a test needs no network. It returns the undo.
func SetCatalogSheetFetch(f func(ctx context.Context, sheetID, gid string) ([]byte, error)) func() {
	old := fetchCatalogSheet
	fetchCatalogSheet = func(ctx context.Context, ref linkorders.SheetRef) ([]byte, error) { return f(ctx, ref.ID, ref.GID) }
	return func() { fetchCatalogSheet = old }
}

// RunCostUpdate runs one cost update's job now, as a worker would.
func (s *Server) RunCostUpdate(ctx context.Context, ws, id string) error {
	payload, _ := json.Marshal(map[string]string{"update_id": id})
	return s.costUpdateJob(ctx, jobs.Job{WorkspaceID: &ws, Payload: payload, Attempt: 1, MaxAttempts: 5})
}

// BufferBadClick queues a redirect click the database can't store (its
// destination isn't an id), as a later bug in the click path might, so a
// test can see that it doesn't hold up the clicks around it.
func (s *Server) BufferBadClick(ws, link, key string) {
	s.clicks.add(bufferedClick{ws: ws, linkID: link, key: key, at: time.Now(), destinationID: "not-an-id"})
}

// FeedRead reads a Slack post with the workspace's AI, as the feed does.
func (s *Server) FeedRead(ctx context.Context, ws, itemID, text string, x leadfeed.Extracted) (FeedReadingForTest, bool, error) {
	r, ok, err := s.feedRead(ctx, ws, itemID, text, x)
	return FeedReadingForTest(r), ok, err
}

// FeedReadingForTest is feedReading, so tests outside the package can name it.
type FeedReadingForTest = feedReading

// FeedReadPrompt is the fixed system prompt of feedRead.
const FeedReadPrompt = feedReadPrompt

// PollSalesFeed runs one poll of a Slack sales channel now, as its job would.
func (s *Server) PollSalesFeed(ctx context.Context, ws, brand, channel string) error {
	payload, _ := json.Marshal(map[string]string{"id": channel, "brand_id": brand})
	return s.salesFeedPollJob(ctx, jobs.Job{Kind: jobSalesFeedPoll, Payload: payload, WorkspaceID: &ws, Attempt: 1, MaxAttempts: 5})
}

// EnqueueDueSalesFeeds queues the channels due a read, as the loop would.
func (s *Server) EnqueueDueSalesFeeds(ctx context.Context) (int, error) {
	return s.enqueueDueSalesFeeds(ctx)
}

// SalesFeedContent opens a stored post.
func (s *Server) SalesFeedContent(ctx context.Context, ws, item string) (FeedContentForTest, error) {
	var c feedContent
	var sealed []byte
	if err := s.pool.QueryRow(ctx, `SELECT content_sealed FROM sales_feed_items WHERE workspace_id=$1 AND id=$2`, ws, item).Scan(&sealed); err != nil {
		return c, err
	}
	err := s.salesOpen(sealed, item, &c)
	return c, err
}

// FeedContentForTest is feedContent, so tests outside the package can name it.
type FeedContentForTest = feedContent

// SeedSalesFeedItem stores a pending post with text in a feed channel, as
// the poll would, without queueing it.
func (s *Server) SeedSalesFeedItem(ctx context.Context, ws, brand, channel, ts, text string) (string, error) {
	var kind string
	if err := s.pool.QueryRow(ctx, `SELECT kind FROM sales_feed_channels WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, ws, brand, channel).Scan(&kind); err != nil {
		return "", err
	}
	id := newID()
	c := feedContent{Text: text, Author: "B1", Bot: true, Link: "https://acme.slack.example/archives/C1/p" + ts}
	_, err := s.pool.Exec(ctx, `INSERT INTO sales_feed_items(id,workspace_id,brand_id,feed_channel_id,slack_ts,kind,content_sealed,content_hash,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,now())`, id, ws, brand, channel, ts, kind, s.salesSeal(id, c), leadfeed.Hash(text))
	return id, err
}

// EditSalesFeedItem changes a stored post's text, as the sweep does after an
// edit in Slack, without queueing it.
func (s *Server) EditSalesFeedItem(ctx context.Context, ws, item, text string) error {
	var c feedContent
	var sealed []byte
	if err := s.pool.QueryRow(ctx, `SELECT content_sealed FROM sales_feed_items WHERE workspace_id=$1 AND id=$2`, ws, item).Scan(&sealed); err != nil {
		return err
	}
	if err := s.salesOpen(sealed, item, &c); err != nil {
		return err
	}
	c.Text = text
	_, err := s.pool.Exec(ctx, `UPDATE sales_feed_items SET content_sealed=$3,content_hash=$4,edited_at=now() WHERE workspace_id=$1 AND id=$2`, ws, item, s.salesSeal(item, c), leadfeed.Hash(text))
	return err
}

// FeedReplyForTest is feedReply, so tests outside the package can make one.
type FeedReplyForTest = feedReply

// SetSalesFeedReplies replaces a stored post's thread replies, as the sweep
// would after replies in Slack.
func (s *Server) SetSalesFeedReplies(ctx context.Context, ws, item string, replies []FeedReplyForTest) error {
	var c feedContent
	var sealed []byte
	if err := s.pool.QueryRow(ctx, `SELECT content_sealed FROM sales_feed_items WHERE workspace_id=$1 AND id=$2`, ws, item).Scan(&sealed); err != nil {
		return err
	}
	if err := s.salesOpen(sealed, item, &c); err != nil {
		return err
	}
	c.Replies = replies
	_, err := s.pool.Exec(ctx, `UPDATE sales_feed_items SET content_sealed=$3 WHERE workspace_id=$1 AND id=$2`, ws, item, s.salesSeal(item, c))
	return err
}

// SetSalesFeedReading sets what the AI read in a stored post: the
// person's own message and the one-line summary.
func (s *Server) SetSalesFeedReading(ctx context.Context, ws, item, message, summary string) error {
	var c feedContent
	var sealed []byte
	if err := s.pool.QueryRow(ctx, `SELECT content_sealed FROM sales_feed_items WHERE workspace_id=$1 AND id=$2`, ws, item).Scan(&sealed); err != nil {
		return err
	}
	if err := s.salesOpen(sealed, item, &c); err != nil {
		return err
	}
	if c.Reading == nil {
		c.Reading = &feedReading{}
	}
	c.Reading.Message, c.Reading.Summary = message, summary
	_, err := s.pool.Exec(ctx, `UPDATE sales_feed_items SET content_sealed=$3 WHERE workspace_id=$1 AND id=$2`, ws, item, s.salesSeal(item, c))
	return err
}

// RunSalesAlert brings one conversation's Slack message up to date now, as
// its job would.
func (s *Server) RunSalesAlert(ctx context.Context, ws, brand, lead string) error {
	payload, _ := json.Marshal(map[string]string{"id": lead, "brand_id": brand})
	return s.salesAlertJob(ctx, jobs.Job{Kind: jobSalesAlert, Payload: payload, WorkspaceID: &ws, Attempt: 1, MaxAttempts: 5})
}

// RunSalesFeedItem reads one stored post now, as its job's attempt would.
func (s *Server) RunSalesFeedItem(ctx context.Context, ws, brand, item string, attempt int) error {
	payload, _ := json.Marshal(map[string]string{"id": item, "brand_id": brand})
	return s.salesFeedItemJob(ctx, jobs.Job{Kind: jobSalesFeedItem, Payload: payload, WorkspaceID: &ws, Attempt: attempt, MaxAttempts: 5})
}

// MentionSubjectsForTest is what a scan of the brand listens for.
func (s *Server) MentionSubjectsForTest(ctx context.Context, ws, brand string) ([]mentions.Subject, error) {
	return s.mentionSubjects(ctx, ws, brand)
}

// SetMentionTranscriptHTTP lets a test read transcripts from a local fake
// server. It returns the undo.
func SetMentionTranscriptHTTP(c *http.Client) func() {
	was := mentionTranscriptHTTP
	mentionTranscriptHTTP = c
	return func() { mentionTranscriptHTTP = was }
}
