package searchinsights

import (
	"testing"
	"time"
)

func TestNextCollection(t *testing.T) {
	for _, tc := range []struct {
		name, freq, zone, now, want string
		day, hour                   int
	}{
		{"daily before", "daily", "UTC", "2026-10-30T07:00:00Z", "2026-10-30T08:00:00Z", 1, 8},
		{"at hour moves forward", "daily", "UTC", "2026-10-30T08:00:00Z", "2026-10-31T08:00:00Z", 1, 8},
		{"weekly", "weekly", "UTC", "2026-10-30T09:00:00Z", "2026-11-02T08:00:00Z", 1, 8},
		{"DST end", "daily", "America/New_York", "2026-10-31T13:00:00Z", "2026-11-01T13:00:00Z", 1, 8},
		{"DST start", "daily", "America/New_York", "2026-03-07T14:00:00Z", "2026-03-08T12:00:00Z", 1, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, tc.now)
			got, e := NextCollection(tc.freq, tc.day, tc.hour, tc.zone, now)
			if e != nil || got == nil || got.Format(time.RFC3339) != tc.want {
				t.Fatalf("got %v, %v, want %s", got, e, tc.want)
			}
		})
	}
	if n, e := NextCollection("off", 1, 8, "UTC", time.Now()); e != nil || n != nil {
		t.Fatal(n, e)
	}
	for _, args := range []struct {
		f, z string
		d, h int
	}{{"hourly", "UTC", 1, 8}, {"daily", "bad/zone", 1, 8}, {"daily", "UTC", 7, 8}, {"daily", "UTC", 1, 24}} {
		if _, e := NextCollection(args.f, args.d, args.h, args.z, time.Now()); e == nil {
			t.Fatal("accepted", args)
		}
	}
}
