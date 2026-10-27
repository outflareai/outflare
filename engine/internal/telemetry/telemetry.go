// Package telemetry sends one anonymous report a day from a self-hosted
// server and reads back whether a newer version or a security fix exists,
// the way WordPress, GitLab and Grafana pair usage stats with update checks.
//
// The report carries a random instance id, the version, the platform, rough
// usage counts in ranges and the ids of connectors in use. It never carries
// names, email addresses, domains, message content, credentials or the
// server's URL. docs/telemetry.md lists every field; Preview returns the
// exact report this server would send.
//
// It is off when OUTFLARE_TELEMETRY is off/false/0, when DO_NOT_TRACK is set,
// when the server owner turns it off in Settings, or when the build has no
// endpoint (URL empty).
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultURL is where reports go unless TELEMETRY_URL says otherwise. It is
// set at build time (-ldflags "-X .../telemetry.DefaultURL=https://...") by
// official releases; a build without it sends nothing.
var DefaultURL = ""

// Schema is the report format version. Bump it when a field changes meaning.
const Schema = 1

// Interval is how often a server reports.
const Interval = 24 * time.Hour

// Why telemetry is off, when it is.
const (
	OffEnv        = "env"         // OUTFLARE_TELEMETRY or DO_NOT_TRACK
	OffSetting    = "setting"     // the server owner turned it off
	OffBuild      = "build"       // a build on the engine turned it off (the hosted Cloud)
	OffNoEndpoint = "no_endpoint" // this build has nowhere to send it
)

// Reporter collects and sends the daily report.
type Reporter struct {
	Pool    *pgxpool.Pool
	Log     *slog.Logger
	Version string
	URL     string // empty sends nothing
	EnvOff  bool   // turned off by the environment
	// BuildOff is set by builds on the engine, such as the hosted Cloud,
	// whose usage is measured another way.
	BuildOff bool
	Client   *http.Client
}

// FromEnv reads OUTFLARE_TELEMETRY, DO_NOT_TRACK and TELEMETRY_URL.
func FromEnv(pool *pgxpool.Pool, log *slog.Logger, version string) *Reporter {
	r := &Reporter{Pool: pool, Log: log, Version: version, URL: DefaultURL}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OUTFLARE_TELEMETRY"))) {
	case "off", "false", "0", "no", "disabled":
		r.EnvOff = true
	}
	// https://consoledonottrack.com
	if v := strings.TrimSpace(os.Getenv("DO_NOT_TRACK")); v != "" && v != "0" && !strings.EqualFold(v, "false") {
		r.EnvOff = true
	}
	if v, ok := os.LookupEnv("TELEMETRY_URL"); ok {
		r.URL = strings.TrimSpace(v)
	}
	return r
}

// Report is everything a server sends. Counts are ranges, never exact.
type Report struct {
	Schema     int      `json:"schema"`
	InstanceID string   `json:"instance_id"`
	Version    string   `json:"version"`
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	GoVersion  string   `json:"go_version"`
	Container  bool     `json:"container"`
	Postgres   int      `json:"postgres_major"`
	AgeDays    string   `json:"install_age_days"`
	Counts     Counts   `json:"counts"`
	Connectors []string `json:"connectors"`
}

// Counts are rough sizes, as ranges such as "6-10".
type Counts struct {
	Users           string `json:"users"`
	Workspaces      string `json:"workspaces"`
	Mailboxes       string `json:"mailboxes"`
	ActiveMailboxes string `json:"active_mailboxes"`
	Leads           string `json:"leads"`
	ActiveSequences string `json:"active_sequences"`
	Sent24h         string `json:"sent_24h"`
	Replies24h      string `json:"replies_24h"`
	Projects        string `json:"projects"`
	Missions        string `json:"missions"`
	APITokens       string `json:"api_tokens"`
	ActiveUsers7d   string `json:"active_users_7d"`
}

// Notice is something the update check wants the server's admins to know,
// such as a security fix in a newer version.
type Notice struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // info, warning or critical
	Title    string `json:"title"`
	URL      string `json:"url,omitempty"`
}

// Status is what Settings shows about telemetry and updates.
type Status struct {
	Enabled         bool       `json:"enabled"`
	DisabledBy      *string    `json:"disabled_by"`
	Endpoint        *string    `json:"endpoint"`
	InstanceID      string     `json:"instance_id"`
	Version         string     `json:"version"`
	LastSentAt      *time.Time `json:"last_sent_at"`
	LastError       *string    `json:"last_error"`
	LatestVersion   *string    `json:"latest_version"`
	UpdateAvailable bool       `json:"update_available"`
	Notices         []Notice   `json:"notices"`
	ChangedAt       *time.Time `json:"changed_at"`
}

// Off says why telemetry is off, or "" when it is on.
func (r *Reporter) Off(ctx context.Context) (string, error) {
	switch {
	case r.BuildOff:
		return OffBuild, nil
	case r.EnvOff:
		return OffEnv, nil
	}
	var enabled bool
	if err := r.Pool.QueryRow(ctx, `SELECT enabled FROM telemetry_state`).Scan(&enabled); err != nil {
		return "", err
	}
	switch {
	case !enabled:
		return OffSetting, nil
	case r.URL == "":
		return OffNoEndpoint, nil
	}
	return "", nil
}

// Status reads the current state.
func (r *Reporter) Status(ctx context.Context) (Status, error) {
	s := Status{Version: r.Version, Notices: []Notice{}}
	var notices []byte
	err := r.Pool.QueryRow(ctx, `
		SELECT instance_id::text, last_sent_at, last_error, latest_version, notices, changed_at
		FROM telemetry_state`).Scan(&s.InstanceID, &s.LastSentAt, &s.LastError, &s.LatestVersion, &notices, &s.ChangedAt)
	if err != nil {
		return s, err
	}
	json.Unmarshal(notices, &s.Notices) //nolint:errcheck // stored by us; a bad row shows no notices
	off, err := r.Off(ctx)
	if err != nil {
		return s, err
	}
	s.Enabled = off == ""
	if off != "" {
		s.DisabledBy = &off
	}
	if r.URL != "" {
		u := r.URL
		s.Endpoint = &u
	}
	if s.LatestVersion != nil {
		s.UpdateAvailable = Newer(*s.LatestVersion, r.Version)
	}
	return s, nil
}

// SetEnabled records the server owner's choice.
func (r *Reporter) SetEnabled(ctx context.Context, enabled bool, userID string) error {
	_, err := r.Pool.Exec(ctx, `UPDATE telemetry_state SET enabled = $1, changed_by = $2, changed_at = now()`, enabled, userID)
	return err
}

// Preview builds the report without sending it, so anyone can see exactly
// what would leave the server.
func (r *Reporter) Preview(ctx context.Context) (Report, error) {
	rep := Report{
		Schema:     Schema,
		Version:    r.Version,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		GoVersion:  runtime.Version(),
		Container:  inContainer(),
		Connectors: []string{},
	}
	var pgNum int
	var created time.Time
	var n [12]int64
	err := r.Pool.QueryRow(ctx, `
		SELECT
			(SELECT instance_id::text FROM telemetry_state),
			(SELECT created_at FROM telemetry_state),
			current_setting('server_version_num')::int,
			(SELECT count(*) FROM users WHERE disabled_at IS NULL),
			(SELECT count(*) FROM workspaces),
			(SELECT count(*) FROM mailboxes),
			(SELECT count(*) FROM mailboxes WHERE status = 'active'),
			(SELECT count(*) FROM leads),
			(SELECT count(*) FROM sequences WHERE status = 'active'),
			(SELECT count(*) FROM messages WHERE status = 'sent' AND created_at > now() - interval '24 hours'),
			(SELECT count(*) FROM inbound_messages WHERE kind = 'reply' AND received_at > now() - interval '24 hours'),
			(SELECT count(*) FROM projects),
			(SELECT count(*) FROM missions),
			(SELECT count(*) FROM api_tokens WHERE revoked_at IS NULL),
			(SELECT count(DISTINCT user_id) FROM sessions WHERE last_seen_at > now() - interval '7 days')`,
	).Scan(&rep.InstanceID, &created, &pgNum, &n[0], &n[1], &n[2], &n[3], &n[4], &n[5], &n[6], &n[7], &n[8], &n[9], &n[10], &n[11])
	if err != nil {
		return rep, err
	}
	rep.Postgres = pgNum / 10000
	rep.AgeDays = Bucket(int64(time.Since(created).Hours() / 24))
	c := &rep.Counts
	for i, dst := range []*string{&c.Users, &c.Workspaces, &c.Mailboxes, &c.ActiveMailboxes, &c.Leads, &c.ActiveSequences,
		&c.Sent24h, &c.Replies24h, &c.Projects, &c.Missions, &c.APITokens, &c.ActiveUsers7d} {
		*dst = Bucket(n[i])
	}
	// Connector ids only (e.g. "hubspot"), like a plugin directory's active
	// install counts. Never the accounts they sign in to.
	rows, err := r.Pool.Query(ctx, `SELECT DISTINCT connector FROM integrations ORDER BY connector`)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return rep, err
		}
		rep.Connectors = append(rep.Connectors, id)
	}
	return rep, rows.Err()
}

// SendNow sends a report if telemetry is on, and stores the update check's
// answer. It returns nil without sending when telemetry is off.
func (r *Reporter) SendNow(ctx context.Context) error {
	off, err := r.Off(ctx)
	if err != nil || off != "" {
		return err
	}
	rep, err := r.Preview(ctx)
	if err != nil {
		return err
	}
	if _, err := r.Pool.Exec(ctx, `UPDATE telemetry_state SET last_attempt_at = now()`); err != nil {
		return err
	}
	answer, sendErr := r.post(ctx, rep)
	if sendErr != nil {
		r.Pool.Exec(ctx, `UPDATE telemetry_state SET last_error = $1`, sendErr.Error()) //nolint:errcheck
		return sendErr
	}
	notices, _ := json.Marshal(answer.Notices)
	_, err = r.Pool.Exec(ctx, `
		UPDATE telemetry_state SET last_sent_at = now(), last_error = NULL, latest_version = NULLIF($1, ''), notices = $2`,
		answer.LatestVersion, notices)
	return err
}

type answer struct {
	LatestVersion string   `json:"latest_version"`
	Notices       []Notice `json:"notices"`
}

func (r *Reporter) post(ctx context.Context, rep Report) (answer, error) {
	var a answer
	body, err := json.Marshal(rep)
	if err != nil {
		return a, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return a, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "outflare/"+r.Version)
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return a, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return a, fmt.Errorf("telemetry endpoint answered %d", resp.StatusCode)
	}
	// The answer is data from outside: keep it small and only what we use.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&a); err != nil && !errors.Is(err, io.EOF) {
		return a, fmt.Errorf("telemetry answer: %w", err)
	}
	a.LatestVersion = clip(a.LatestVersion, 40)
	kept := []Notice{}
	for _, n := range a.Notices {
		if len(kept) == 10 || n.Title == "" {
			continue
		}
		switch n.Severity {
		case "info", "warning", "critical":
		default:
			n.Severity = "info"
		}
		if u, err := url.Parse(n.URL); err != nil || u.Scheme != "https" {
			n.URL = ""
		}
		n.ID, n.Title, n.URL = clip(n.ID, 64), clip(n.Title, 200), clip(n.URL, 500)
		kept = append(kept, n)
	}
	a.Notices = kept
	return a, nil
}

// Run reports once a day until ctx ends. The first report goes a few
// minutes after start, so a crash loop does not flood the endpoint, and
// every report is jittered so servers do not all report at the same minute.
func (r *Reporter) Run(ctx context.Context) {
	wait := time.Duration(2+rand.IntN(8)) * time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = time.Hour
		var last *time.Time
		if err := r.Pool.QueryRow(ctx, `SELECT GREATEST(last_sent_at, last_attempt_at) FROM telemetry_state`).Scan(&last); err != nil {
			continue
		}
		if last != nil && time.Since(*last) < Interval-time.Duration(rand.IntN(60))*time.Minute {
			continue
		}
		if err := r.SendNow(ctx); err != nil && ctx.Err() == nil {
			r.Log.Info("telemetry not sent", "err", err)
		}
	}
}

var buckets = []struct {
	max   int64
	label string
}{
	{0, "0"}, {1, "1"}, {5, "2-5"}, {10, "6-10"}, {25, "11-25"}, {50, "26-50"}, {100, "51-100"},
	{250, "101-250"}, {500, "251-500"}, {1000, "501-1000"}, {5000, "1001-5000"}, {10000, "5001-10000"},
	{50000, "10001-50000"}, {100000, "50001-100000"},
}

// Bucket turns a count into the range reported for it.
func Bucket(n int64) string {
	for _, b := range buckets {
		if n <= b.max {
			return b.label
		}
	}
	return "100000+"
}

// Newer reports whether version a is newer than b, comparing dotted numbers
// (a leading v and any -suffix are ignored). A dev build is never behind.
func Newer(a, b string) bool {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka || !okb {
		return false
	}
	for i := range 3 {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var p [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return p, false
	}
	for i, s := range parts {
		if _, err := fmt.Sscanf(s, "%d", &p[i]); err != nil {
			return p, false
		}
	}
	return p, true
}

func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
