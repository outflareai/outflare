// Package outflare sends product events to Outflare from a Go server:
// identify, track, page, screen, group and alias calls are queued in
// memory and sent in batches to POST /v1/brands/{brand}/events by a
// background goroutine, with retries. It uses only the standard library.
//
//	t, err := outflare.New(outflare.Config{Token: os.Getenv("OUTFLARE_TOKEN"), Brand: "northpeak"})
//	defer t.Close()
//	t.Identify(outflare.Identify{UserID: "u_123", Traits: map[string]any{"plan": "pro"}})
//	t.Track(outflare.Track{UserID: "u_123", Event: "Order Completed", Properties: map[string]any{"total": 42}})
package outflare

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version is this library's version.
const Version = "0.110.0"

const maxBatch = 500

// Config sets up a Tracker.
type Config struct {
	// Token is an API token with customers.manage (oft_...). Keep it on
	// your servers.
	Token string
	// Brand is the brand's id or slug.
	Brand string
	// BaseURL is your Outflare server. Default https://app.outflare.ai.
	BaseURL string
	// FlushAt sends once this many events wait. Default 20, at most 500.
	FlushAt int
	// FlushInterval sends at least this often. Default 10 seconds.
	FlushInterval time.Duration
	// MaxQueue is the most events kept waiting; the oldest are dropped
	// past it. Default 10000.
	MaxQueue int
	// MaxRetries is how often a batch the server could not take is tried
	// again. Default 3.
	MaxRetries int
	// OnError hears about batches that were given up on, with how many
	// events were lost.
	OnError func(err error, lost int)
	// HTTPClient replaces the HTTP client. Default one with a 10 second
	// timeout.
	HTTPClient *http.Client
	// backoff replaces the wait between tries, for tests.
	backoff func(attempt int, retryAfter time.Duration) time.Duration
}

// Message is what every call can carry besides its own fields.
type Message struct {
	UserID      string
	AnonymousID string
	// Timestamp is when it happened; now when zero.
	Timestamp time.Time
	// MessageID is your own id for the message, so a retry is stored
	// once. One is made when empty.
	MessageID string
	Context   map[string]any
}

// Track is something a person did.
type Track struct {
	UserID, AnonymousID string
	Event               string
	Properties          map[string]any
	Timestamp           time.Time
	MessageID           string
	Context             map[string]any
}

// Identify says who a person is.
type Identify struct {
	UserID, AnonymousID string
	Traits              map[string]any
	Timestamp           time.Time
	MessageID           string
	Context             map[string]any
}

// Group is the company a person belongs to.
type Group struct {
	UserID, AnonymousID string
	GroupID             string
	Traits              map[string]any
	Timestamp           time.Time
	MessageID           string
	Context             map[string]any
}

// Page is a page someone saw; Screen uses it for a screen in an app.
type Page struct {
	UserID, AnonymousID string
	Name                string
	Properties          map[string]any
	Timestamp           time.Time
	MessageID           string
	Context             map[string]any
}

// Alias joins an id a person had before (PreviousID) to UserID.
type Alias struct {
	UserID, PreviousID string
	Timestamp          time.Time
	MessageID          string
	Context            map[string]any
}

// Tracker is a queue of product events for one brand. Make one and share
// it: it is safe for use from many goroutines.
type Tracker struct {
	cfg    Config
	url    string
	mu     sync.Mutex
	queue  []map[string]any
	closed bool
	sendMu sync.Mutex
	wake   chan struct{}
	done   chan struct{}
	ended  chan struct{}
}

// New makes a Tracker and starts sending in the background.
func New(cfg Config) (*Tracker, error) {
	if cfg.Token == "" {
		return nil, errors.New("outflare: Token is required")
	}
	if cfg.Brand == "" {
		return nil, errors.New("outflare: Brand is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://app.outflare.ai"
	}
	if cfg.FlushAt <= 0 {
		cfg.FlushAt = 20
	}
	if cfg.FlushAt > maxBatch {
		cfg.FlushAt = maxBatch
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 10 * time.Second
	}
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = 10000
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	} else if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.backoff == nil {
		cfg.backoff = func(attempt int, retryAfter time.Duration) time.Duration {
			if retryAfter > 0 {
				return min(retryAfter, 30*time.Second)
			}
			return min(500*time.Millisecond<<attempt, 30*time.Second)
		}
	}
	t := &Tracker{cfg: cfg, url: strings.TrimRight(cfg.BaseURL, "/") + "/v1/brands/" + url.PathEscape(cfg.Brand) + "/events",
		wake: make(chan struct{}, 1), done: make(chan struct{}), ended: make(chan struct{})}
	go t.run()
	return t, nil
}

// Track queues something a person did and returns its message id.
func (t *Tracker) Track(m Track) (string, error) {
	if m.Event == "" {
		return "", errors.New("outflare: Track needs an Event")
	}
	props := m.Properties
	if props == nil {
		props = map[string]any{}
	}
	return t.add("track", Message{m.UserID, m.AnonymousID, m.Timestamp, m.MessageID, m.Context}, map[string]any{"event": m.Event, "properties": props})
}

// Identify queues who a person is.
func (t *Tracker) Identify(m Identify) (string, error) {
	if m.UserID == "" {
		return "", errors.New("outflare: Identify needs a UserID")
	}
	return t.add("identify", Message{m.UserID, m.AnonymousID, m.Timestamp, m.MessageID, m.Context}, map[string]any{"traits": orEmpty(m.Traits)})
}

// Group queues the company a person belongs to.
func (t *Tracker) Group(m Group) (string, error) {
	if m.GroupID == "" {
		return "", errors.New("outflare: Group needs a GroupID")
	}
	return t.add("group", Message{m.UserID, m.AnonymousID, m.Timestamp, m.MessageID, m.Context}, map[string]any{"groupId": m.GroupID, "traits": orEmpty(m.Traits)})
}

// Page queues a page someone saw.
func (t *Tracker) Page(m Page) (string, error) {
	return t.add("page", Message{m.UserID, m.AnonymousID, m.Timestamp, m.MessageID, m.Context}, map[string]any{"name": m.Name, "properties": orEmpty(m.Properties)})
}

// Screen queues a screen someone saw in an app.
func (t *Tracker) Screen(m Page) (string, error) {
	return t.add("screen", Message{m.UserID, m.AnonymousID, m.Timestamp, m.MessageID, m.Context}, map[string]any{"name": m.Name, "properties": orEmpty(m.Properties)})
}

// Alias queues an id a person had before.
func (t *Tracker) Alias(m Alias) (string, error) {
	if m.UserID == "" || m.PreviousID == "" {
		return "", errors.New("outflare: Alias needs a UserID and a PreviousID")
	}
	return t.add("alias", Message{UserID: m.UserID, Timestamp: m.Timestamp, MessageID: m.MessageID, Context: m.Context}, map[string]any{"previousId": m.PreviousID})
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func (t *Tracker) add(kind string, m Message, rest map[string]any) (string, error) {
	if m.UserID == "" && m.AnonymousID == "" {
		return "", fmt.Errorf("outflare: %s needs a UserID or an AnonymousID", kind)
	}
	if m.MessageID == "" {
		m.MessageID = newID()
	}
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now()
	}
	ctx := map[string]any{}
	for k, v := range m.Context {
		ctx[k] = v
	}
	ctx["library"] = map[string]any{"name": "outflare-go", "version": Version}
	rest["type"] = kind
	rest["messageId"] = m.MessageID
	rest["timestamp"] = m.Timestamp.UTC().Format(time.RFC3339Nano)
	rest["context"] = ctx
	if m.UserID != "" {
		rest["userId"] = m.UserID
	}
	if m.AnonymousID != "" {
		rest["anonymousId"] = m.AnonymousID
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return "", errors.New("outflare: the tracker is closed")
	}
	t.queue = append(t.queue, rest)
	lost := 0
	if len(t.queue) > t.cfg.MaxQueue {
		lost = len(t.queue) - t.cfg.MaxQueue
		t.queue = append([]map[string]any(nil), t.queue[lost:]...)
	}
	full := len(t.queue) >= t.cfg.FlushAt
	t.mu.Unlock()
	if lost > 0 && t.cfg.OnError != nil {
		t.cfg.OnError(errors.New("outflare: the queue is full, so the oldest events were dropped"), lost)
	}
	if full {
		select {
		case t.wake <- struct{}{}:
		default:
		}
	}
	return m.MessageID, nil
}

func (t *Tracker) run() {
	defer close(t.ended)
	tick := time.NewTicker(t.cfg.FlushInterval)
	defer tick.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
		case <-t.wake:
		}
		t.Flush(context.Background())
	}
}

// Flush sends everything waiting now and returns once it was sent or
// given up on, or ctx ended.
func (t *Tracker) Flush(ctx context.Context) {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	for {
		t.mu.Lock()
		n := min(len(t.queue), maxBatch)
		batch := t.queue[:n:n]
		t.queue = t.queue[n:]
		t.mu.Unlock()
		if n == 0 {
			return
		}
		if err := t.send(ctx, batch); err != nil && t.cfg.OnError != nil {
			t.cfg.OnError(err, n)
		}
	}
}

// Close sends what waits and takes no more. Call it before your program
// exits.
func (t *Tracker) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
	close(t.done)
	<-t.ended
	t.Flush(context.Background())
}

func (t *Tracker) send(ctx context.Context, batch []map[string]any) error {
	body, err := json.Marshal(map[string]any{"source": "go", "batch": batch})
	if err != nil {
		return fmt.Errorf("outflare: an event could not be written as JSON: %w", err)
	}
	for attempt := 0; ; attempt++ {
		status, retryAfter, err := t.post(ctx, body)
		if err == nil && status >= 200 && status < 300 {
			return nil
		}
		// A refusal (a bad token, no such brand) won't change on a retry.
		if status >= 400 && status < 500 && status != http.StatusTooManyRequests && status != http.StatusRequestTimeout {
			return fmt.Errorf("outflare: the server refused the batch (%d)", status)
		}
		if attempt >= t.cfg.MaxRetries {
			if err != nil {
				return fmt.Errorf("outflare: the server could not be reached: %w", err)
			}
			return fmt.Errorf("outflare: the server could not take the batch (%d)", status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(t.cfg.backoff(attempt, retryAfter)):
		}
	}
}

func (t *Tracker) post(ctx context.Context, body []byte) (int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.cfg.Token)
	req.Header.Set("User-Agent", "outflare-go/"+Version)
	res, err := t.cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	secs, _ := strconv.Atoi(res.Header.Get("Retry-After"))
	return res.StatusCode, time.Duration(secs) * time.Second, nil
}

// newID makes a random version 4 UUID.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
