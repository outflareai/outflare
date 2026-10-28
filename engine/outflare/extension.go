package outflare

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/pipeline"
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
	aiTools         []api.AITool
	trackingCerts   api.TrackingCerts
	store           *connectors.Store
	server          *api.Server
	emailFinder     *emailfind.Service
	stages          *pipeline.Runner
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
