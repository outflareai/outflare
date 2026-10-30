package fxrates

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The bank's format, with made-up rates on days no other test uses.
const sample = `<?xml version="1.0" encoding="UTF-8"?>
<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref">
	<gesmes:subject>Reference rates</gesmes:subject>
	<Cube>
		<Cube time="2001-02-02">
			<Cube currency="USD" rate="1.2000"/>
			<Cube currency="JPY" rate="150.00"/>
			<Cube currency="SGD" rate="1.6000"/>
		</Cube>
		<Cube time="2001-02-01">
			<Cube currency="USD" rate="1.1000"/>
			<Cube currency="SGD" rate="1.5000"/>
		</Cube>
	</Cube>
</gesmes:Envelope>`

func TestParse(t *testing.T) {
	days, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	d := days["2001-02-02"]
	if !near(d["EUR"], 1.2) || !near(d["SGD"], 0.75) || !near(d["JPY"], 0.008) || d["USD"] != 0 {
		t.Errorf("2001-02-02: %v", d)
	}
	if len(days) != 2 {
		t.Errorf("%d days", len(days))
	}
	if _, err := Parse(strings.NewReader("<html>maintenance</html>")); err == nil {
		t.Error("a page with no rates was read")
	}
}

func TestFetchAndConvert(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sample)) }))
	defer srv.Close()
	s := &Service{Pool: pool, URL: srv.URL}
	newest, err := s.Fetch(ctx)
	if err != nil || newest.Format("2006-01-02") != "2001-02-02" {
		t.Fatalf("fetch: %v %v", newest, err)
	}
	if _, err := s.Fetch(ctx); err != nil {
		t.Fatalf("fetching again: %v", err)
	}
	for _, c := range []struct {
		cur   string
		minor int64
		day   string
		want  int64 // -1 for no rate
	}{
		{"SGD", 30000, "2001-02-02", 22500}, // S$300 at 0.75
		{"SGD", 30000, "2001-02-01", 22000}, // the day before, its own rate
		{"SGD", 30000, "2001-02-04", 22500}, // a Sunday: Friday's rate
		{"JPY", 3000, "2001-02-02", 2400},   // ¥3000 has no decimals
		{"EUR", 1815, "2001-02-02", 2178},   // €18.15
		{"USD", 1999, "2001-02-02", 1999},   // as charged
		{"SGD", 30000, "2001-03-01", -1},    // too long after the last rate
		{"SGD", 30000, "2001-01-15", -1},    // before any rate
		{"XAU", 100, "2001-02-02", -1},      // a currency without one
	} {
		got, err := ToUSD(ctx, pool, c.cur, c.minor, c.day)
		if err != nil {
			t.Fatal(err)
		}
		if (c.want < 0) != (got == nil) || (got != nil && *got != c.want) {
			t.Errorf("%s %d on %s = %v, want %d", c.cur, c.minor, c.day, got, c.want)
		}
	}

	// A run records what it did, calls after, and a failed fetch is an
	// error event and skips after.
	events := track.Capture(t)
	converted := 0
	s.runOnce(ctx, func(context.Context) error { converted++; return nil })
	srv.Config.Handler = http.NotFoundHandler()
	s.runOnce(ctx, func(context.Context) error { converted++; return nil })
	got := events()
	if converted != 1 || len(got) != 2 || got[0].Name != "exchange_rates.fetched" || got[1].Name != "exchange_rates.fetch_failed" || got[1].ErrorCode != "unavailable" {
		t.Errorf("converted %d, events %+v", converted, got)
	}
}
