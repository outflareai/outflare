package seo

import (
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestGeonodeParseHook(t *testing.T) {
	evs, err := Geonode{}.ParseHook(nil, []byte(`{"event":"batch_completed","webhook_id":"w1","delivery_id":"d1","timestamp":"2026-10-27T12:00:00Z",
		"data":{"job_id":"job-1","status":"completed","total_urls":40,"results":[{"url":"https://example.com","html":"<p>text</p>"}]}}`))
	if err != nil || len(evs) != 1 {
		t.Fatalf("parse: %v %v", evs, err)
	}
	ev := evs[0]
	if ev.ID != "batch_completed:job-1" || ev.Type != "batch_completed" || ev.Summary["total_urls"] != 40.0 || ev.Summary["status"] != "completed" {
		t.Fatalf("event: %+v", ev)
	}
	if _, ok := ev.Summary["results"]; ok {
		t.Fatal("the summary keeps page content")
	}
	// Without a job id, the delivery names the event.
	evs, err = Geonode{}.ParseHook(nil, []byte(`{"event":"extract_completed","delivery_id":"d9","data":{}}`))
	if err != nil || evs[0].ID != "d9" {
		t.Fatalf("no job: %v %v", evs, err)
	}
	for _, body := range []string{`not json`, `{"delivery_id":"d1"}`, `{"event":"batch_completed","data":{}}`} {
		if _, err := (Geonode{}).ParseHook(nil, []byte(body)); c.KindOf(err) != c.ErrInvalid {
			t.Errorf("%s: %v", body, err)
		}
	}
}
