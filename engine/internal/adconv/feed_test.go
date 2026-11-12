package adconv

import (
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// The pure parts of the feed: which rows a feed role takes, how a refund
// goes back, and when the reconciliation ratio counts as drifted. The
// database-backed paths (matching, sending, the payers list, the goal) are
// exercised through the API in engine/internal/api/ad_feed_test.go.

func TestRoleFilterSQL(t *testing.T) {
	plain := roleFilterSQL(false)
	if !strings.Contains(plain, "r.feed_role NOT IN ('first_payment', 'repeat_payment')") || !strings.Contains(plain, "e.record_id IS NOT NULL") ||
		!strings.Contains(plain, "p.name = 'payment.succeeded'") || strings.Contains(plain, "ad_revenue") {
		t.Fatalf("plain filter: %s", plain)
	}
	ledger := roleFilterSQL(true)
	if !strings.Contains(ledger, "SELECT v.bucket = 'new' FROM ad_revenue v WHERE v.event_id = e.id") || !strings.Contains(ledger, "COALESCE(") {
		t.Fatalf("ledger filter: %s", ledger)
	}
}

func TestViaFeed(t *testing.T) {
	v := int64(3000)
	cases := []struct {
		a       adjustment
		hasFeed bool
		want    bool
	}{
		{adjustment{kind: "restatement", transport: connectors.TransportDataManager, value: &v}, true, true},
		{adjustment{kind: "restatement", transport: connectors.TransportDataManager, value: &v}, false, false},
		{adjustment{kind: "restatement", transport: connectors.TransportAdsAPI, value: &v}, true, false},
		{adjustment{kind: "retraction", transport: connectors.TransportDataManager}, true, false},
		{adjustment{kind: "restatement", transport: connectors.TransportDataManager}, true, false},
	}
	for i, c := range cases {
		if got := viaFeed(c.a, c.hasFeed); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}

func TestRatios(t *testing.T) {
	days := make([]HealthDay, 36)
	for i := range days {
		days[i].PlatformConversions, days[i].SendsAccepted = 10, 9
	}
	week, base, drifted := ratios(days)
	if week == nil || base == nil || *week != 0.9 || *base != 0.9 || drifted {
		t.Fatalf("steady: %v %v %v", week, base, drifted)
	}
	// The last week accepted half as many: drifted.
	for i := len(days) - 7; i < len(days); i++ {
		days[i].SendsAccepted = 4
	}
	if _, _, drifted := ratios(days); !drifted {
		t.Fatal("a halved ratio should drift")
	}
	// Nothing to compare without platform conversions.
	for i := range days {
		days[i].PlatformConversions = 0
	}
	if week, base, drifted := ratios(days); week != nil || base != nil || drifted {
		t.Fatalf("no conversions: %v %v %v", week, base, drifted)
	}
	if week, _, _ := ratios(days[:10]); week != nil {
		t.Fatal("too few days should say nothing")
	}
}

func TestFeedReady(t *testing.T) {
	var f Feed
	if f.Ready() {
		t.Fatal("an empty feed is not ready")
	}
	f.Actions = map[string]FeedAction{RoleSignup: {ID: "1"}}
	if !f.Ready() {
		t.Fatal("a feed with actions is ready")
	}
	if len(Rules) != 3 || Rules[1].CustomerType != connectors.CustomerNew || Rules[2].CustomerType != connectors.CustomerReturning || Rules[0].Event != "signed_up" {
		t.Fatalf("rules: %+v", Rules)
	}
	_ = time.Now()
}
