package pipeline

import (
	"context"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Rate is how fast calls to one outside account may go.
type Rate struct {
	PerMinute float64
	// Burst is how many calls may go at once after a quiet spell.
	// Default PerMinute / 6 (ten seconds' worth), at least 1.
	Burst float64
}

// Call outcomes for Report.
const (
	Succeeded   = "success"
	RateLimited = "rate_limited"
	Failed      = "failure"
)

// Breaker settings.
const (
	breakerFailures = 5                // consecutive failures that open it
	breakerMaxOpen  = 15 * time.Minute // longest it stays open
	slowFor         = 5 * time.Minute  // how long a 429 slows an account
)

// Limits are rate limits and circuit breakers per outside account (a
// connection, an AI key), shared by every worker through the database.
// Before a call, a stage asks Take; when it says wait, the stage returns
// Wait with that delay instead of calling. After the call it Reports how
// it went: a rate limit answer halves the rate for a while, and repeated
// failures open the breaker, which lets one probe call through after a
// pause, doubling the pause each time it fails again.
type Limits struct{ Q Querier }

// Key names an account for Limits: a kind and an id, such as
// Key("integration", id) or Key("ai", workspaceID).
func Key(kind, id string) string { return kind + ":" + id }

func kindOf(key string) string {
	k, _, _ := strings.Cut(key, ":")
	return k
}

// Take asks for one call on key. A zero wait means go ahead; otherwise
// wait that long and ask again.
func (l Limits) Take(ctx context.Context, workspaceID, key string, rate Rate) (time.Duration, error) {
	burst := rate.Burst
	if burst <= 0 {
		burst = max(rate.PerMinute/6, 1)
	}
	if rate.PerMinute <= 0 {
		rate.PerMinute = 60
	}
	if _, err := l.Q.Exec(ctx, `INSERT INTO pipeline_limits (key, workspace_id, tokens) VALUES ($1, $2, $3) ON CONFLICT (key) DO NOTHING`,
		key, workspaceID, burst); err != nil {
		return 0, err
	}
	// One statement reads, refills and takes, under the row's lock.
	var tokens, factor float64
	var was string
	var reopenIn float64
	err := l.Q.QueryRow(ctx, `
		WITH cur AS (
			SELECT key, breaker, coalesce(extract(epoch FROM reopen_at - now()), 0)::float8 AS reopen_in,
				CASE WHEN slow_until > now() THEN slow_factor ELSE 1 END AS f,
				least($3::float8, tokens + extract(epoch FROM now() - refilled_at)::float8 / 60 * $2::float8
					* CASE WHEN slow_until > now() THEN slow_factor ELSE 1 END) AS t
			FROM pipeline_limits WHERE key = $1 FOR UPDATE
		)
		UPDATE pipeline_limits p SET
			tokens = CASE
				WHEN cur.breaker <> 'closed' AND cur.reopen_in > 0 THEN cur.t
				WHEN cur.t >= 1 THEN cur.t - 1 ELSE cur.t END,
			refilled_at = now(),
			-- Once the pause is over, one call goes through as the probe;
			-- if it never reports, another may try a minute later.
			breaker = CASE WHEN cur.breaker <> 'closed' AND cur.reopen_in <= 0 AND cur.t >= 1 THEN 'half_open' ELSE cur.breaker END,
			reopen_at = CASE WHEN cur.breaker <> 'closed' AND cur.reopen_in <= 0 AND cur.t >= 1 THEN now() + interval '1 minute' ELSE p.reopen_at END,
			updated_at = now()
		FROM cur WHERE p.key = cur.key
		RETURNING cur.t, cur.f, cur.breaker, cur.reopen_in`, key, rate.PerMinute, burst).Scan(&tokens, &factor, &was, &reopenIn)
	if err != nil {
		return 0, err
	}
	account := kindOf(key)
	switch {
	case was == "open" && reopenIn > 0:
		metrics.PipelineLimitWait(account, "breaker")
		return time.Duration(reopenIn * float64(time.Second)), nil
	case was == "half_open" && reopenIn > 0:
		// Another worker's probe call is deciding.
		metrics.PipelineLimitWait(account, "probe")
		return time.Duration(min(reopenIn, 10) * float64(time.Second)), nil
	case tokens < 1:
		metrics.PipelineLimitWait(account, "rate")
		perSecond := rate.PerMinute * factor / 60
		return time.Duration((1 - tokens) / perSecond * float64(time.Second)), nil
	}
	return 0, nil
}

// Report records how a call on key went (Succeeded, RateLimited, Failed).
// Only failures that say the account is unwell count toward the breaker:
// a provider error or timeout, not a bad input.
func (l Limits) Report(ctx context.Context, key, outcome string) error {
	metrics.PipelineLimitReport(kindOf(key), outcome)
	var err error
	switch outcome {
	case Succeeded:
		_, err = l.Q.Exec(ctx, `UPDATE pipeline_limits SET failures = 0, breaker = 'closed', open_seconds = 60, reopen_at = NULL, updated_at = now()
			WHERE key = $1 AND (failures > 0 OR breaker <> 'closed')`, key)
	case RateLimited:
		_, err = l.Q.Exec(ctx, `UPDATE pipeline_limits SET tokens = 0, refilled_at = now(),
				slow_factor = greatest(0.125, CASE WHEN slow_until > now() THEN slow_factor / 2 ELSE 0.5 END),
				slow_until = now() + $2::interval, updated_at = now()
			WHERE key = $1`, key, interval(slowFor))
	default:
		_, err = l.Q.Exec(ctx, `UPDATE pipeline_limits SET
				failures = failures + 1,
				breaker = CASE WHEN breaker = 'half_open' OR failures + 1 >= $2 THEN 'open' ELSE breaker END,
				reopen_at = CASE WHEN breaker = 'half_open' OR (breaker = 'closed' AND failures + 1 >= $2)
					THEN now() + make_interval(secs => open_seconds) ELSE reopen_at END,
				open_seconds = CASE WHEN breaker = 'half_open' THEN least(open_seconds * 2, $3) ELSE open_seconds END,
				updated_at = now()
			WHERE key = $1`, key, breakerFailures, int(breakerMaxOpen.Seconds()))
	}
	return err
}
