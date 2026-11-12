package seosync

import (
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

func TestChargeableCost(t *testing.T) {
	cost := connectors.SEOCost{Units: 150, Estimated: true}
	if got := chargeableCost(nil, cost); got != cost {
		t.Fatalf("success booked %+v, want %+v", got, cost)
	}
	err := connectors.Errf(connectors.ErrInvalid, "Ahrefs refused this report (HTTP 403): the Ahrefs plan or API key may not include it")
	if got := chargeableCost(err, cost); got.Units != 0 || got.USD != 0 {
		t.Fatalf("failed call still booked %+v", got)
	}
}

func TestPermanentError(t *testing.T) {
	if permanentError(nil) {
		t.Fatal("nil is not permanent")
	}
	if !permanentError(connectors.Errf(connectors.ErrInvalid, "Ahrefs refused this report (HTTP 403): the Ahrefs plan or API key may not include it")) {
		t.Fatal("403 refusal should wait for next week")
	}
	if !permanentError(connectors.Errf(connectors.ErrAuth, "Ahrefs refused the API key (HTTP 401). Check the key, or create a new one.")) {
		t.Fatal("bad key should wait for next week")
	}
	if !permanentError(&BudgetError{Name: "Ahrefs", Budget: Budget{Unit: "API unit", Limit: 100000, Spent: 99873}, Cost: 150}) {
		t.Fatal("reached budget should wait")
	}
	if permanentError(connectors.Errf(connectors.ErrUnavailable, "Ahrefs is not responding properly (HTTP 503). Try again in a few minutes.")) {
		t.Fatal("transient errors should still retry with backoff")
	}
}

func TestSuggestSlotIsWeekly(t *testing.T) {
	now := time.Date(2026, 11, 10, 16, 53, 0, 0, time.UTC)
	start := now.UTC().Truncate(interval)
	if suggestSlot(start) != suggestSlot(start.Add(interval-time.Second)) {
		t.Fatalf("got %s and %s in the same week", suggestSlot(start), suggestSlot(start.Add(interval-time.Second)))
	}
	if suggestSlot(start) != suggestSlot(start.Add(24*time.Hour)) {
		t.Fatal("a day later should stay in the same weekly slot")
	}
	if suggestSlot(start) == suggestSlot(start.Add(interval)) {
		t.Fatalf("week slot %s did not change after %s", suggestSlot(start), interval)
	}
}

func TestCompetitorSuggestionsOncePerWeeklyCycle(t *testing.T) {
	if !snapshotSuggestsCompetitors(false) {
		t.Fatal("the weekly snapshot should fetch competitor suggestions")
	}
	if snapshotSuggestsCompetitors(true) {
		t.Fatal("a refresh must not fetch new suggestions")
	}
	if snapshotSuggestsCompetitors(false) && queueSuggestAlongsideWeeklySnapshot() {
		t.Fatal("QueueDue would enqueue queueSuggest in the same cycle as the weekly snapshot, fetching suggestions twice")
	}
}
