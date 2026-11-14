package outflare

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/datanetwork"
	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/inboundemail"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/pipeline"
	"github.com/outflareai/outflare/engine/internal/quota"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/telemetry"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/prometheus/client_golang/prometheus"
)

// An Extension adds to the engine without changing it: routes, middleware,
// background jobs and capabilities. The hosted Cloud is built from the
// open-source engine plus its extensions (billing, sign-up, usage limits),
// so a change merged here reaches Cloud with a dependency bump, not a port.
type Extension interface {
	// Name identifies the extension in logs and errors.
	Name() string
	// Setup runs once, after migrations and before the server starts. It
	// registers what the extension adds through the Host. An extension that
	// owns tables creates them here, in its own schema.
	Setup(ctx context.Context, h *Host) error
}

// Host is what an Extension can reach in the running engine.
type Host struct {
	Pool      *pgxpool.Pool
	Log       *slog.Logger
	PublicURL string

	signedInDomain string

	queue        *jobs.Queue
	box          *secrets.Box
	mailer       sysmail.Sender
	routes       []route
	middleware   []func(http.Handler) http.Handler
	capabilities []string
	background   []func(ctx context.Context)
	telemetry    *telemetry.Reporter

	workspaceRoutes []api.WorkspaceRoute
	menuLinks       []api.MenuLinks
	menuBadges      []api.MenuBadges
	aiTools         []api.AITool
	trackingCerts   api.TrackingCerts
	quota           quota.Checker
	store           *connectors.Store
	server          *api.Server
	emailFinder     *emailfind.Service
	dataNetwork     *datanetwork.Service
	stages          *pipeline.Runner
	inboundEmail    *inboundemail.Service
	supportAuto     func(ctx context.Context, workspaceID string) bool
	adAutoApprove   func(ctx context.Context, workspaceID, brandID, kind string) bool
}

type route struct {
	pattern string
	handler http.Handler
}

// Handle adds an HTTP route, with a net/http pattern such as
// "POST /v1/cloud/billing/webhook". Extensions keep to their own path prefix
// so they never shadow an open-source route; a clash panics at start-up.
func (h *Host) Handle(pattern string, handler http.Handler) {
	h.routes = append(h.routes, route{pattern, handler})
}

// Use wraps every request. Middleware added first runs first. Request ids,
// logging and panic recovery are already applied outside it.
func (h *Host) Use(mw func(http.Handler) http.Handler) {
	h.middleware = append(h.middleware, mw)
}

// HandleJob processes background jobs of a kind, from the same Postgres
// queue the engine uses. Returning an error retries the job with backoff.
func (h *Host) HandleJob(kind string, fn func(ctx context.Context, payload json.RawMessage) error) {
	h.queue.Handle(kind, func(ctx context.Context, j jobs.Job) error { return fn(ctx, j.Payload) })
}

// EnqueueJob adds a job for HandleJob to process. A non-empty idempotency
// key makes a repeated enqueue a no-op.
func (h *Host) EnqueueJob(ctx context.Context, kind string, payload any, idempotencyKey string) error {
	_, err := jobs.Enqueue(ctx, h.Pool, jobs.NewJob{Kind: kind, Payload: payload, IdempotencyKey: idempotencyKey})
	return err
}

// EnqueueJobTx atomically queues an extension job with the state change that
// requires it. Rollback removes both. Payloads must contain references, not
// credentials; handlers resolve authorized resources from persisted scope.
func (h *Host) EnqueueJobTx(ctx context.Context, tx pgx.Tx, kind string, payload any, idempotencyKey string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: kind, Payload: payload, IdempotencyKey: idempotencyKey})
	return err
}

// EnableInboundEmail turns on inbound email: each workspace gets
// addresses at domain (such as in.outflare.ai) for purposes like support
// and AI receipts. The extension receives the mail at its own edge and
// delivers each message to POST /v1/inbound/email: the raw message as the
// body, the recipient in X-Outflare-To, and X-Outflare-Timestamp and
// X-Outflare-Signature from SignInboundEmail with secret. A 404 means no
// such address: refuse the message. It also lists the inbound_email
// capability, so the web app offers it.
func (h *Host) EnableInboundEmail(domain string, secret []byte) {
	h.inboundEmail.Enable(domain, secret)
	h.AddCapability("inbound_email")
}

// EnableInboundEmailAt turns inbound email on at domain for an extension
// that receives the mail itself, in its own process, and hands each
// message to DeliverInboundEmail: no edge, no signed POST. Use it when
// the domain's mail already reaches the extension (Outflare Cloud's hosted
// mailboxes). It lists the inbound_email capability like
// EnableInboundEmail.
func (h *Host) EnableInboundEmailAt(domain string) {
	h.inboundEmail.Enable(domain, nil)
	h.AddCapability("inbound_email")
}

// ErrNoInboundAddress means DeliverInboundEmail has no live address like
// the recipient: refuse the message.
var ErrNoInboundAddress = inboundemail.ErrUnknownAddress

// DeliverInboundEmail stores a raw message sent to one of the engine's
// inbound addresses and queues it, exactly as a signed POST to
// /v1/inbound/email would. It returns ErrNoInboundAddress when to isn't a
// live address; a message delivered twice (same Message-ID) is taken once.
func (h *Host) DeliverInboundEmail(ctx context.Context, to string, raw []byte) error {
	if !h.inboundEmail.Enabled() {
		return ErrNoInboundAddress
	}
	_, purpose, err := h.inboundEmail.Deliver(ctx, to, raw)
	switch {
	case errors.Is(err, inboundemail.ErrUnknownAddress):
		metrics.InboundReceived("", "unknown_address", 0)
		return ErrNoInboundAddress
	case errors.Is(err, inboundemail.ErrDuplicate):
		metrics.InboundReceived(purpose, "duplicate", 0)
		return nil
	case errors.Is(err, inboundemail.ErrTooLarge):
		metrics.InboundReceived("", "too_large", 0)
		return err
	case err != nil:
		metrics.InboundReceived(purpose, "error", 0)
		return err
	}
	metrics.InboundReceived(purpose, "accepted", len(raw))
	return nil
}

// SetInboundAddressTaken tells the engine which addresses at the inbound
// domain the extension already uses for something else (its own
// mailboxes), so the engine never picks one of them. fn gets the full
// address and is called while an address is made.
func (h *Host) SetInboundAddressTaken(fn func(ctx context.Context, address string) (bool, error)) {
	h.inboundEmail.SetTaken(fn)
}

// InboundAddressTaken reports whether address is one of the engine's
// inbound addresses (live or retired), so an extension sharing the domain
// doesn't hand it out.
func (h *Host) InboundAddressTaken(ctx context.Context, address string) (bool, error) {
	return h.inboundEmail.TakenHere(ctx, h.Pool, address)
}

// SignInboundEmail is the X-Outflare-Signature for a delivery made at
// unix second ts.
func SignInboundEmail(secret []byte, ts int64, raw []byte) string {
	return inboundemail.Sign(secret, ts, raw)
}

// SetSupportAutoReplies decides which workspaces the support assistant may
// answer customers in without a person (Auto mode). Without it the
// assistant only drafts replies for the team to send. fn runs before every
// automatic send and when settings are read or saved, so keep it quick.
// The engine still runs every check on each reply and signs it as the
// assistant; fn only says whether Auto is allowed at all.
func (h *Host) SetSupportAutoReplies(fn func(ctx context.Context, workspaceID string) bool) {
	h.supportAuto = fn
	h.AddCapability("support_auto_replies")
}

// SetAdAutoApprove decides when a paid ads change may be made without a
// person: fn gets the workspace, the finding's brand and its kind, and
// says yes or no. The engine asks it before the rules apply a saving on
// their own, before the protective overspend pause (kind
// overspend_pause), and when an agent approves a suggestion. It asks only
// for kinds that save money (a lower budget, a pause, a negative keyword,
// a keyword or ad paused, a tighter target, an exclusion, and
// overspend_pause) on a brand's own campaigns;
// anything that can spend more always waits for a person, and a campaign
// on an account every brand shares is never changed on its own. A change
// fn allows is recorded with an undo and measured and judged like one a
// person approved; an agent's grants still apply to its approvals; a
// brand in shadow mode only records what would have happened. Without
// it, every change waits for a person and the overspend is only an
// alert. fn runs on every rules run, so keep it quick.
func (h *Host) SetAdAutoApprove(fn func(ctx context.Context, workspaceID, brandID, kind string) bool) {
	h.adAutoApprove = fn
	h.AddCapability("ad_suggestions_auto")
}

// AdSavingKinds are the paid ads savings an extension may offer to let
// apply on their own: a lower budget, a pause and a negative keyword. The
// engine counts more kinds as savings (adsuggest.SavingKinds) and asks
// SetAdAutoApprove's fn about each of them; an fn that allows only what
// this lists keeps every other saving waiting for a person. The list
// grows only together with the extensions that offer it, so their
// settings never show a kind they haven't handled.
func AdSavingKinds() []string {
	return []string{adsuggest.BudgetDown, adsuggest.Pause, adsuggest.NegativeAdd}
}

// Metrics is the registry the engine's private metrics port serves
// (OUTFLARE_METRICS_ADDR). An extension registers its own collectors here so
// they reach the same Prometheus and dashboards; name them outflare_<name>_*.
func (h *Host) Metrics() prometheus.Registerer { return metrics.Registry }

// Go runs fn in the background until the engine shuts down.
func (h *Host) Go(fn func(ctx context.Context)) {
	h.background = append(h.background, fn)
}

// AddCapability lists a capability in /v1/meta, which the web app reads to
// show the screens a build supports.
func (h *Host) AddCapability(name string) {
	h.capabilities = append(h.capabilities, name)
}

// AddMenuLinks adds sidebar items for some signed-in people only, such as a
// staff console for staff. fn runs on every /v1/me, so keep it to one quick
// query; an error is logged and the links are left out. The page a link
// opens must still check who is calling: a link is not permission.
func (h *Host) AddMenuLinks(fn func(ctx context.Context, userID string) ([]MenuLink, error)) {
	h.menuLinks = append(h.menuLinks, fn)
}

// AddMenuBadges shows short texts, up to 16 characters, next to the
// extension's own sidebar links for the workspace someone has open, such as
// an account balance next to a billing link: fn returns them by link id.
// It runs each time the web app opens a workspace, so keep it to one quick
// query; an error is logged and no badges are shown.
func (h *Host) AddMenuBadges(fn func(ctx context.Context, workspaceID string) (map[string]string, error)) {
	h.menuBadges = append(h.menuBadges, fn)
}

// AITool names an extension's own AI calls: ID is what it passes as the
// task to AI().Chat, Name and Description are what Settings → AI usage
// shows, and ModelTask is the model task whose model the calls use (such
// as classify_domain), so a workspace sees and limits them on their own
// without choosing another model.
type AITool = api.AITool

// AddAITool lists t on every workspace's AI usage page, so its calls are
// shown, and can be limited, apart from the engine's tools. A bad or
// clashing tool panics at start-up.
func (h *Host) AddAITool(t AITool) {
	if err := api.CheckAITool(t); err != nil {
		panic(err)
	}
	h.aiTools = append(h.aiTools, t)
}

// SetTrackingCerts gets HTTPS certificates for workspaces' tracking
// domains through certs, for a server whose own proxy can't (Cloud asks
// Cloudflare). Once set, a tracking domain's links use it only after its
// certificate is active.
func (h *Host) SetTrackingCerts(certs TrackingCerts) {
	h.trackingCerts = certs
}

// Quota decides whether a workspace may have more of something before the
// engine adds it: a brand, a mailbox, a tracking domain, customer records,
// or another email sent. Allow returns nil to allow, a *QuotaError to
// refuse (the API answers 402 quota_exceeded with its Message; a refused
// email waits an hour and tries again; a CRM import ends with the
// reason), or any other error, which is logged and allowed. It is asked
// often, so answer from memory or one quick query.
type Quota = quota.Checker

// QuotaError is a refusal: Resource is the resource asked about, Message
// what stopped and what to do about it, shown to the person.
type QuotaError = quota.Error

// The resources a Quota is asked about. n is how many would be added: for
// records and mailboxes the most an import could add, since some may
// already be there.
const (
	QuotaBrands          = quota.Brands
	QuotaMailboxes       = quota.Mailboxes
	QuotaTrackingDomains = quota.TrackingDomains
	QuotaRecords         = quota.Records
	QuotaEmailsSent      = quota.EmailsSent
	QuotaTrackedEvents   = quota.TrackedEvents
	// QuotaTrackerClicks is asked once per batch of redirect clicks
	// written, a moment after the redirects. A refusal drops those clicks;
	// the people were already sent on.
	QuotaTrackerClicks = quota.TrackerClicks
)

// SetQuota lets q refuse work a workspace isn't allowed more of. Without
// one, as on a self-hosted server, everything is allowed.
func (h *Host) SetQuota(q Quota) {
	h.quota = q
}

// DisableTelemetry stops the daily anonymous usage report and update check
// that self-hosted servers send (docs/telemetry.md). The hosted Cloud calls
// it: its usage is measured on its own side, not reported to itself.
func (h *Host) DisableTelemetry() {
	if h.telemetry != nil {
		h.telemetry.BuildOff = true
	}
}

func (h *Host) registerRoutes(mux *http.ServeMux) {
	for _, r := range h.routes {
		mux.Handle(r.pattern, r.handler)
	}
}

// Accounts creates and recognises the engine's users and sessions, for
// extensions that sign people up (invite-only Cloud sign-up) or gate staff
// pages on who is signed in.
// ClientIP is the visitor's address, honouring CLIENT_IP_HEADER when the
// engine runs behind a proxy such as Fly.io's.
func (h *Host) ClientIP(r *http.Request) string { return api.ClientIP(r) }

func (h *Host) Accounts() *api.Accounts {
	return &api.Accounts{Pool: h.Pool, PublicURL: h.PublicURL, Box: h.box, Mailer: h.mailer, Log: h.Log, SignedInCookieDomain: h.signedInDomain}
}

// Re-exported so extensions outside this module can name them.
type (
	Accounts   = api.Accounts
	NewAccount = api.NewAccount
	MenuLink   = api.MenuLink
	// TrackingCerts and CertState are what SetTrackingCerts takes.
	TrackingCerts = api.TrackingCerts
	CertState     = api.CertState
	InputError    = api.InputError
	// UserDeletion is what Accounts.DeleteUser did, or would do.
	UserDeletion      = api.UserDeletion
	DeletionWorkspace = api.DeletionWorkspace
	// SupportView is a read-only view Accounts.StartSupportView began.
	SupportView = api.SupportView
)

var (
	ErrEmailTaken = api.ErrEmailTaken
	// Accounts.FinishSignup: the link was opened in another browser, so the
	// person must choose a password; or the link is unknown, used or expired.
	ErrChoosePassword = api.ErrChoosePassword
	ErrBadSignupLink  = api.ErrBadSignupLink
	// Accounts.StartSupportView: the person turned support access off.
	ErrSupportAccessOff = api.ErrSupportAccessOff
)

// SignupConfirmPath is the page a sign-up confirmation link opens, with
// the token in its #fragment (Accounts.StartSignup, FinishSignup).
const SignupConfirmPath = api.SignupConfirmPath

// Event is a product event: an action someone took or an error they hit.
// AGENTS.md has the rules (names, properties, no personal data).
type Event = track.Event

// Event outcomes.
const (
	OutcomeSuccess = track.Success
	OutcomeFailure = track.Failure
	OutcomeError   = track.Error
)

// Track records a product event, the same way the engine's own actions
// are: to the operator's PostHog when OUTFLARE_POSTHOG_KEY is set, and the
// debug log. Inside a request it is sent once the request succeeds (a
// failure at once). Requests to an extension's routes are already recorded
// as api.called; call Track for what they did.
func Track(ctx context.Context, e Event) { track.Emit(ctx, e) }

// TrackError logs err and records e as an error with code: for failures
// outside a request, such as a background job of the extension's own.
func TrackError(ctx context.Context, log *slog.Logger, e Event, code string, err error) {
	track.Fail(ctx, log, e, code, err)
}

// Classification says what kind of email domain an address is: free,
// private, alias, throwaway, school, government or unknown (see
// docs/classification.md).
type Classification = classify.Result

// Disposable is the category of throwaway email domains.
const Disposable = classify.Disposable

// ClassifyEmail classifies an address from the engine's built-in data,
// without DNS, so it is quick enough for a sign-up form. A malformed
// address is an error.
func ClassifyEmail(ctx context.Context, email string) (Classification, error) {
	return (&classify.Classifier{}).Classify(ctx, classify.Input{Email: email})
}
