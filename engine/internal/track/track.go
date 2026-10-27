// Package track records product events: every action a person or agent
// takes in Outflare and every error they run into, so the operator can see
// which features people use, where they get stuck and what fails.
//
// Events go to the operator's own PostHog project (OUTFLARE_POSTHOG_KEY),
// the same one the web app reports to, to any Sink the engine adds (such as
// a workspace of the operator's own, OUTFLARE_EVENTS_WORKSPACE), and to the
// server log at debug level. With neither set nothing is kept: a
// self-hosted install sends nothing unless its owner turns it on. This is separate from the anonymous
// usage report (package telemetry), which goes to the Outflare project.
//
// Properties carry ids, enums, counts and durations only. Never put names,
// email addresses, domains, URLs, message content, search terms or error
// text in them: error text can quote customer data, so failures carry an
// error code and the full error goes to the log. docs/product-analytics.md
// lists the events and properties.
package track

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Outcomes.
const (
	Success = "success" // it worked (the default)
	Failure = "failure" // refused: bad input, no permission, a limit, not found (a 4xx)
	Error   = "error"   // broken: a bug, a crash, a provider or database failure (a 5xx)
)

// Event is one thing that happened.
type Event struct {
	// Name is object.verb in the past tense, lowercase snake_case, matching
	// audit actions: "mailbox.connected", "member.removed", "api.called",
	// "job.failed". Never rename a shipped event; add a new one.
	Name string
	// UserID is the person behind it (users.id, the id the web app
	// identifies with), also for their API tokens. Empty for system work.
	UserID      string
	ActorID     string
	ActorType   string // human, api_token, agent, system
	WorkspaceID string
	// Surface is where it came from: web, api, mcp, cli, sdk, job, system.
	Surface   string
	Outcome   string // Success when empty
	ErrorCode string // a registered error code when Outcome isn't Success
	RequestID string
	Props     map[string]any
	At        time.Time // when it happened; zero is now
}

// Client sends events to PostHog in batches. The zero value is unusable;
// use Start.
type Client struct {
	key      string
	sinks    []Sink
	endpoint string
	version  string
	log      *slog.Logger
	http     *http.Client
	ch       chan Event
	dropped  atomic.Int64
	done     chan struct{}
}

var (
	current   atomic.Pointer[Client]
	logger    atomic.Pointer[slog.Logger]
	capturing atomic.Pointer[func(Event)]
)

// Capture collects every event sent until the test ends, for tests that
// check an action is recorded. It returns what has been sent so far.
func Capture(t interface{ Cleanup(func()) }) func() []Event {
	var (
		mu  sync.Mutex
		got []Event
	)
	f := func(e Event) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	}
	capturing.Store(&f)
	t.Cleanup(func() { capturing.Store(nil) })
	return func() []Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]Event(nil), got...)
	}
}

// Sink receives every event in batches, besides PostHog: the engine's own
// workspace sink stores them as product events in a workspace. Send runs on
// the client's goroutine, one batch at a time, and must not block for long.
type Sink interface {
	Send(ctx context.Context, events []Event)
}

type muteKey struct{}

// Mute returns a context whose events are dropped. A sink that records
// events with the same code that emits them (an audit action, a request)
// mutes its own work, so storing events never makes more of them.
func Mute(ctx context.Context) context.Context { return context.WithValue(ctx, muteKey{}, true) }

// SetLogger sets where events are logged when no Client runs (and before
// Start).
func SetLogger(log *slog.Logger) { logger.Store(log) }

// Start sends events to PostHog at host with the project key, and to each
// sink, until ctx ends, then flushes what is queued. With no key and no
// sinks it only logs. It returns a function that waits for the final flush.
func Start(ctx context.Context, key, host, version string, log *slog.Logger, sinks ...Sink) (wait func()) {
	if log == nil {
		log = slog.Default()
	}
	SetLogger(log)
	if key == "" && len(sinks) == 0 {
		return func() {}
	}
	c := &Client{
		key:      key,
		sinks:    sinks,
		endpoint: strings.TrimRight(host, "/") + "/batch/",
		version:  version,
		log:      log,
		http:     &http.Client{Timeout: 10 * time.Second},
		ch:       make(chan Event, 10000),
		done:     make(chan struct{}),
	}
	current.Store(c)
	go c.run(ctx)
	return func() { <-c.done }
}

// Emit records e. Inside Hold a successful event waits until the hold is
// released; a failure is about the attempt, so it is sent at once. It never
// blocks and never fails the caller.
func Emit(ctx context.Context, e Event) {
	if ctx.Value(muteKey{}) != nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if h, ok := ctx.Value(holdKey{}).(*hold); ok && (e.Outcome == "" || e.Outcome == Success) {
		h.mu.Lock()
		h.events = append(h.events, e)
		h.mu.Unlock()
		return
	}
	send(e)
}

// Fail logs err and records e as a failure with code: the one call for an
// error outside a request (a background loop, a provider sync). Errors in
// requests and jobs are recorded for you.
func Fail(ctx context.Context, log *slog.Logger, e Event, code string, err error) {
	if e.Outcome == "" || e.Outcome == Success {
		e.Outcome = Error
	}
	e.ErrorCode = code
	if log == nil {
		log = logger.Load()
	}
	if log != nil {
		log.ErrorContext(ctx, e.Name, "err", err, "error_code", code, "workspace_id", e.WorkspaceID)
	}
	Emit(ctx, e)
}

type holdKey struct{}

type hold struct {
	mu     sync.Mutex
	events []Event
}

// Hold keeps the events emitted with the returned context until release:
// release(true, base) sends them, release(false, base) drops them. A
// request or job holds its events so the actions it recorded in a
// transaction are sent only when it succeeds. base fills in what the code
// that emitted an event didn't know: the surface and request id, and the
// person when the event's actor is the request's. Inside another hold it
// changes nothing; the outer one decides.
func Hold(ctx context.Context) (context.Context, func(send bool, base Event)) {
	if _, ok := ctx.Value(holdKey{}).(*hold); ok {
		return ctx, func(bool, Event) {}
	}
	h := &hold{}
	return context.WithValue(ctx, holdKey{}, h), func(ok bool, base Event) {
		h.mu.Lock()
		events := h.events
		h.events = nil
		h.mu.Unlock()
		if !ok {
			return
		}
		for _, e := range events {
			send(fill(e, base))
		}
	}
}

func fill(e, base Event) Event {
	if e.Surface == "" {
		e.Surface = base.Surface
	}
	if e.RequestID == "" {
		e.RequestID = base.RequestID
	}
	if e.UserID == "" && e.ActorID != "" && e.ActorID == base.ActorID {
		e.UserID = base.UserID
	}
	return e
}

func send(e Event) {
	if e.Outcome == "" {
		e.Outcome = Success
	}
	if log := logger.Load(); log != nil && log.Enabled(context.Background(), slog.LevelDebug) {
		log.Debug("event", "name", e.Name, "outcome", e.Outcome, "error_code", e.ErrorCode,
			"workspace_id", e.WorkspaceID, "user_id", e.UserID, "surface", e.Surface, "request_id", e.RequestID)
	}
	if f := capturing.Load(); f != nil {
		(*f)(e)
	}
	c := current.Load()
	if c == nil {
		return
	}
	select {
	case c.ch <- e:
	default:
		c.dropped.Add(1)
	}
}

func (c *Client) run(ctx context.Context) {
	defer close(c.done)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	var buf []Event
	flush := func() {
		if len(buf) > 0 {
			if c.key != "" {
				c.post(buf)
			}
			for _, s := range c.sinks {
				s.Send(context.WithoutCancel(ctx), buf)
			}
			buf = buf[:0]
		}
		if n := c.dropped.Swap(0); n > 0 {
			c.log.Warn("product events dropped: queue full", "count", n)
		}
	}
	for {
		select {
		case e := <-c.ch:
			buf = append(buf, e)
			if len(buf) >= 100 {
				flush()
			}
		case <-tick.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case e := <-c.ch:
					buf = append(buf, e)
				default:
					flush()
					return
				}
			}
		}
	}
}

// capture is one event in PostHog's batch format.
type capture struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Timestamp  time.Time      `json:"timestamp"`
	Properties map[string]any `json:"properties"`
}

func (c *Client) post(events []Event) {
	batch := make([]capture, 0, len(events))
	for _, e := range events {
		batch = append(batch, toCapture(e, c.version))
	}
	body, err := json.Marshal(map[string]any{"api_key": c.key, "batch": batch})
	if err != nil {
		c.log.Error("product events: encode", "err", err)
		return
	}
	resp, err := c.http.Post(c.endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		c.log.Warn("product events: send", "err", err, "count", len(events))
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		c.log.Warn("product events: send", "status", resp.StatusCode, "count", len(events))
	}
}

func toCapture(e Event, version string) capture {
	p := make(map[string]any, len(e.Props)+10)
	for k, v := range e.Props {
		p[k] = v
	}
	p["outcome"] = e.Outcome
	set := func(k, v string) {
		if v != "" {
			p[k] = v
		}
	}
	set("error_code", e.ErrorCode)
	set("surface", e.Surface)
	set("actor_type", e.ActorType)
	set("actor_id", e.ActorID)
	set("workspace_id", e.WorkspaceID)
	set("request_id", e.RequestID)
	p["$lib"] = "outflare-engine"
	p["$lib_version"] = version
	if e.WorkspaceID != "" {
		p["$groups"] = map[string]string{"workspace": e.WorkspaceID}
	}
	id := e.UserID
	if id == "" {
		// Work with no person behind it (a system job, a token whose maker
		// left) doesn't make a person in PostHog.
		p["$process_person_profile"] = false
		switch {
		case e.ActorID != "":
			id = "actor:" + e.ActorID
		case e.WorkspaceID != "":
			id = "workspace:" + e.WorkspaceID
		default:
			id = "system"
		}
	}
	return capture{Event: e.Name, DistinctID: id, Timestamp: e.At, Properties: p}
}
