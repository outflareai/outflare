// Package fxrates keeps the European Central Bank's daily reference rates,
// so amounts charged in other currencies can be shown in US dollars. The
// rates are public and read without a key; they are the same for every
// workspace.
package fxrates

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Source is the last 90 days of reference rates (euros against each
// currency), published on working days around 16:00 CET.
const Source = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-hist-90d.xml"

// MaxAge is how far back a rate is used for a day with none (weekends and
// holidays have none).
const MaxAge = 10 * 24 * time.Hour

// Querier runs a query: a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Service fetches the rates into exchange_rates.
type Service struct {
	Pool   *pgxpool.Pool
	Log    *slog.Logger
	URL    string       // Source when empty
	Client *http.Client // a client with a timeout when nil
}

type envelope struct {
	Days []struct {
		Time  string `xml:"time,attr"`
		Rates []struct {
			Currency string `xml:"currency,attr"`
			Rate     string `xml:"rate,attr"`
		} `xml:"Cube"`
	} `xml:"Cube>Cube"`
}

// Parse reads the bank's XML into US dollars per unit of each currency,
// by day. Euros are included; US dollars are not (always 1).
func Parse(r io.Reader) (map[string]map[string]float64, error) {
	var e envelope
	if err := xml.NewDecoder(io.LimitReader(r, 4<<20)).Decode(&e); err != nil {
		return nil, err
	}
	out := map[string]map[string]float64{}
	for _, d := range e.Days {
		if _, err := time.Parse(time.DateOnly, d.Time); err != nil {
			continue
		}
		perEUR := map[string]float64{}
		for _, r := range d.Rates {
			if v, err := strconv.ParseFloat(r.Rate, 64); err == nil && v > 0 && len(r.Currency) == 3 {
				perEUR[r.Currency] = v
			}
		}
		usd := perEUR["USD"]
		if usd == 0 {
			continue
		}
		day := map[string]float64{"EUR": usd}
		for c, v := range perEUR {
			if c != "USD" {
				day[c] = usd / v
			}
		}
		out[d.Time] = day
	}
	if len(out) == 0 {
		return nil, errors.New("no rates in the response")
	}
	return out, nil
}

// Fetch downloads and stores the rates, and returns the newest day.
func (s *Service) Fetch(ctx context.Context) (time.Time, error) {
	url, client := s.URL, s.Client
	if url == "" {
		url = Source
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return time.Time{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("exchange rates: status %d", res.StatusCode)
	}
	days, err := Parse(res.Body)
	if err != nil {
		return time.Time{}, err
	}
	var newest time.Time
	b := &pgx.Batch{}
	for day, rates := range days {
		if t, _ := time.Parse(time.DateOnly, day); t.After(newest) {
			newest = t
		}
		for c, v := range rates {
			b.Queue(`INSERT INTO exchange_rates (currency, day, usd_per_unit) VALUES ($1, $2, $3)
				ON CONFLICT (currency, day) DO UPDATE SET usd_per_unit = EXCLUDED.usd_per_unit`, c, day, v)
		}
	}
	if err := s.Pool.SendBatch(ctx, b).Close(); err != nil {
		return time.Time{}, err
	}
	return newest, nil
}

// Run fetches the rates now and then every interval while on reports true
// (only servers that receive receipts need them), then calls after, which
// converts what was waiting for a rate.
func (s *Service) Run(ctx context.Context, every time.Duration, on func() bool, after func(context.Context) error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if on() {
			s.runOnce(ctx, after)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) runOnce(ctx context.Context, after func(context.Context) error) {
	newest, err := s.Fetch(ctx)
	if err != nil {
		if ctx.Err() == nil {
			metrics.RatesFetched("error", time.Time{})
			track.Fail(ctx, s.Log, track.Event{Name: "exchange_rates.fetch_failed", ActorType: "system", Surface: "system"}, "unavailable", err)
		}
		return
	}
	metrics.RatesFetched("success", newest)
	track.Emit(ctx, track.Event{Name: "exchange_rates.fetched", ActorType: "system", Surface: "system"})
	if after != nil {
		if err := after(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "exchange_rates.convert_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
	}
}

// MinorDigits is how many decimal places a currency's amounts have.
func MinorDigits(currency string) int {
	switch currency {
	case "JPY", "KRW", "ISK", "HUF":
		return 0
	}
	return 2
}

// ToUSD converts an amount in a currency's minor unit to US cents with the
// rate for day (YYYY-MM-DD, today when empty), or the latest one before it
// within MaxAge. It returns nil when there is no such rate.
func ToUSD(ctx context.Context, q Querier, currency string, minor int64, day string) (*int64, error) {
	if currency == "USD" {
		return &minor, nil
	}
	var cents *int64
	err := q.QueryRow(ctx, `
		SELECT round($2::numeric / $3 * usd_per_unit * 100)::bigint FROM exchange_rates
		WHERE currency = $1 AND day <= coalesce($4::date, current_date) AND day > coalesce($4::date, current_date) - $5::int
		ORDER BY day DESC LIMIT 1`,
		currency, minor, pow10(MinorDigits(currency)), nilIfEmpty(day), int(MaxAge.Hours()/24)).Scan(&cents)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return cents, err
}

func pow10(n int) int64 {
	p := int64(1)
	for range n {
		p *= 10
	}
	return p
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
