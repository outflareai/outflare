package analytics

import (
	"context"
	"fmt"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const organicPaymentLimit = 100000

// OrganicConversions reads first-touch Organic Search sessions, signups
// and payment events from one PostHog project. Payment events are returned
// raw so the sync can count a payment once across payment_succeeded and
// service_purchased. This only queries PostHog; it never writes to it.
func (p PostHog) OrganicConversions(ctx context.Context, cl *c.Client, creds c.Creds, q c.OrganicConversionQuery) (c.OrganicConversionResult, error) {
	var out c.OrganicConversionResult
	if err := checkSite("PostHog", q.Site, digits); err != nil {
		return out, err
	}
	whenEvents := fmt.Sprintf("timestamp >= toDateTime('%s 00:00:00') AND timestamp < toDateTime('%s 00:00:00')", q.From, dayAfter(q.To)) + testFilters(q.TestAccounts)
	whenSessions := fmt.Sprintf("$start_timestamp >= toDateTime('%s 00:00:00') AND $start_timestamp < toDateTime('%s 00:00:00')", q.From, dayAfter(q.To))
	if q.TestAccounts {
		whenSessions += " AND {filters}"
	}
	run := func(sql string) ([][]any, error) { return p.hogql(ctx, cl, creds, q.Site, sql, q.TestAccounts) }

	sessSQL := fmt.Sprintf(`SELECT toDate($start_timestamp) AS day, coalesce(nullIf(toString($entry_hostname), ''), ''), coalesce(nullIf(toString($entry_pathname), ''), '/'), count()
FROM sessions
WHERE $channel_type = 'Organic Search' AND %s
GROUP BY day, 2, 3
ORDER BY day, 4 DESC
LIMIT 50000`, whenSessions)
	res, err := run(sessSQL)
	if err != nil {
		// Self-hosted PostHog, or a project without the sessions table,
		// still has pageviews: count a session as organic from its
		// starting referrer, the same way PageConversions does.
		fallback := fmt.Sprintf(`SELECT toDate(start) AS day, host, path, count()
FROM (
	SELECT properties.$session_id AS sid, min(timestamp) AS start,
		argMinIf(properties.$host, timestamp, event = '$pageview') AS host,
		argMinIf(properties.$pathname, timestamp, event = '$pageview') AS path,
		match(coalesce(argMinIf(properties.$referring_domain, timestamp, event = '$pageview'), ''), '%s') AS search
	FROM events
	WHERE event = '$pageview' AND %s AND properties.$session_id IS NOT NULL
	GROUP BY sid
	HAVING countIf(event = '$pageview') > 0 AND search
)
GROUP BY day, host, path
ORDER BY day, 4 DESC
LIMIT 50000`, searchReferrer, whenEvents)
		res, err = run(fallback)
		if err != nil {
			return out, err
		}
		out.SessionsSource = "events_referrer"
	} else {
		out.SessionsSource = "sessions"
	}
	n := func(r []any, i int) int {
		if i < len(r) {
			if v := num(r[i]); v != nil {
				return int(*v)
			}
		}
		return 0
	}
	f64 := func(r []any, i int) float64 {
		if i < len(r) {
			if v := num(r[i]); v != nil {
				return *v
			}
		}
		return 0
	}
	for _, r := range res {
		if len(r) < 4 {
			continue
		}
		out.Sessions = append(out.Sessions, c.OrganicSessionRow{
			Day: expand(str(r[0])), Host: strings.ToLower(str(r[1])), Path: str(r[2]),
			Sessions: n(r, 3), Source: out.SessionsSource,
		})
	}

	signupSQL := fmt.Sprintf(`SELECT toDate(timestamp) AS day,
	coalesce(nullIf(toString(person.properties.$initial_host), ''), ''),
	coalesce(nullIf(toString(person.properties.$initial_pathname), ''), nullIf(toString(person.properties.$initial_current_url), ''), '/'),
	count(), count(DISTINCT person_id)
FROM events
WHERE event = 'user_signed_up' AND %s
	AND person.properties.$virt_initial_channel_type = 'Organic Search'
GROUP BY day, 2, 3
ORDER BY day, 4 DESC
LIMIT 50000`, whenEvents)
	res, err = run(signupSQL)
	if err != nil {
		return out, err
	}
	for _, r := range res {
		if len(r) < 5 {
			continue
		}
		out.Signups = append(out.Signups, c.OrganicSignupRow{
			Day: expand(str(r[0])), Host: strings.ToLower(str(r[1])), Path: str(r[2]),
			Signups: n(r, 3), SignupPeople: n(r, 4),
		})
	}

	paySQL := fmt.Sprintf(`SELECT toDate(timestamp) AS day,
	coalesce(nullIf(toString(person.properties.$initial_host), ''), ''),
	coalesce(nullIf(toString(person.properties.$initial_pathname), ''), nullIf(toString(person.properties.$initial_current_url), ''), '/'),
	toString(person_id), event,
	if(event = 'payment_succeeded', toFloat(properties.amount), toFloat(properties.price)),
	lower(coalesce(nullIf(toString(properties.currency), ''), 'usd')),
	coalesce(toBool(properties.is_trial), false),
	coalesce(toBool(properties.is_wallet_top_up), false),
	toString(properties.transaction_id)
FROM events
WHERE event IN ('payment_succeeded', 'service_purchased') AND %s
	AND person.properties.$virt_initial_channel_type = 'Organic Search'
ORDER BY timestamp
LIMIT %d`, whenEvents, organicPaymentLimit)
	res, err = run(paySQL)
	if err != nil {
		return out, err
	}
	for _, r := range res {
		if len(r) < 10 {
			continue
		}
		out.Payments = append(out.Payments, c.OrganicPaymentEvent{
			Day: expand(str(r[0])), Host: strings.ToLower(str(r[1])), Path: str(r[2]),
			PersonID: str(r[3]), Event: str(r[4]), Amount: f64(r, 5), Currency: strings.ToLower(str(r[6])),
			Trial: boolish(r[7]), Wallet: boolish(r[8]), TxID: str(r[9]),
		})
	}

	firstSQL := "SELECT min(toDate(timestamp)) FROM events WHERE event = 'payment_succeeded'"
	res, err = run(firstSQL)
	if err == nil && len(res) > 0 && len(res[0]) > 0 {
		if d := expand(str(res[0][0])); d != "" && d != "1970-01-01" {
			out.PaymentSucceededFrom = d
		}
	}
	return out, nil
}

func boolish(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	case string:
		return x == "true" || x == "1" || x == "t"
	}
	if n := num(v); n != nil {
		return *n != 0
	}
	return false
}
