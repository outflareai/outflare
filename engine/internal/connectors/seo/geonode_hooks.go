package seo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Geonode's Scraper API calls a webhook when an extract, batch or crawl
// job finishes. Each subscription covers one event type and has its own
// signing secret; a delivery carries an HMAC-SHA256 of its body in
// X-Webhook-Signature.
var geonodeHookEvents = []string{"extract_completed", "batch_completed", "crawl_completed"}

func (Geonode) HookSetup() c.HookSetup {
	return c.HookSetup{
		Auto: true, Signed: true, Events: geonodeHookEvents,
		Steps: "Outflare adds this address in your Geonode account for you. To add it yourself: Geonode dashboard, Scraper API, Webhooks, " +
			"add a webhook for each of extract_completed, batch_completed and crawl_completed with this address, then paste its signing secret here.",
	}
}

// RegisterHooks adds one subscription per event type. If one fails, the
// ones already made are removed so a retry starts clean.
func (g Geonode) RegisterHooks(ctx context.Context, cl *c.Client, creds c.Creds, hookURL string) ([]c.RemoteHook, error) {
	var out []c.RemoteHook
	for _, ev := range geonodeHookEvents {
		status, raw, err := g.post(ctx, cl, creds, "/v1/webhooks", map[string]any{
			"url": hookURL, "event_type": ev, "description": "Outflare",
		})
		if err == nil && (status < 200 || status > 299) {
			err = geonodeError(status, raw)
		}
		var resp struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
		}
		if err == nil {
			if jerr := json.Unmarshal(raw, &resp); jerr != nil || resp.ID == "" || resp.Secret == "" {
				err = c.Errf(c.ErrUnavailable, "Geonode answered the webhook request without an id and secret")
			}
		}
		if err != nil {
			ids := make([]string, len(out))
			for i, h := range out {
				ids[i] = h.ID
			}
			_ = g.DeleteHooks(ctx, cl, creds, ids)
			return nil, err
		}
		out = append(out, c.RemoteHook{ID: resp.ID, Secret: resp.Secret})
	}
	return out, nil
}

func (g Geonode) DeleteHooks(ctx context.Context, cl *c.Client, creds c.Creds, ids []string) error {
	var firstErr error
	for _, id := range ids {
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, cl.Base("geonode", geonodeAPI)+"/v1/webhooks/"+url.PathEscape(id), nil)
		if err != nil {
			return c.Errf(c.ErrInvalid, "bad Geonode address: %v", err)
		}
		for h, v := range g.header(creds) {
			req.Header[h] = v
		}
		resp, err := cl.Do(req)
		if err != nil {
			if firstErr == nil {
				firstErr = c.Errf(c.ErrUnavailable, "could not reach Geonode: %v", err)
			}
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode <= 299) {
			continue
		}
		if firstErr == nil {
			firstErr = geonodeError(resp.StatusCode, raw)
		}
	}
	return firstErr
}

func (Geonode) VerifyHook(secrets []string, h http.Header, body []byte) error {
	return c.VerifyHMAC(secrets, h.Get("X-Webhook-Signature"), h.Get("X-Webhook-Timestamp"), body)
}

// ParseHook reads {event, webhook_id, delivery_id, timestamp, data}. A job
// finishes once, so its id names the event and Geonode's retries of the
// same news are recognised as repeats.
func (Geonode) ParseHook(_ http.Header, body []byte) ([]c.HookEvent, error) {
	var d struct {
		Event      string         `json:"event"`
		DeliveryID string         `json:"delivery_id"`
		Data       map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, c.Errf(c.ErrInvalid, "the Geonode delivery is not JSON: %v", err)
	}
	if d.Event == "" {
		return nil, c.Errf(c.ErrInvalid, "the Geonode delivery has no event")
	}
	summary := map[string]any{}
	for _, k := range []string{"job_id", "status", "total_urls", "completed_urls", "failed_urls", "cancelled_urls", "pages_crawled", "total_pages"} {
		switch v := d.Data[k].(type) {
		case string:
			if len(v) <= 64 {
				summary[k] = v
			}
		case float64:
			summary[k] = v
		}
	}
	id := d.DeliveryID
	if job, ok := summary["job_id"].(string); ok && job != "" {
		id = d.Event + ":" + job
	}
	if id == "" {
		return nil, c.Errf(c.ErrInvalid, "the Geonode delivery has no delivery_id or job_id")
	}
	return []c.HookEvent{{ID: id, Type: d.Event, Summary: summary}}, nil
}

func geonodeError(status int, raw []byte) error {
	type body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	var e struct {
		body
		Error *body `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != nil {
		e.body = *e.Error
	}
	if e.Code != "" {
		return c.Errf(geonodeKind(e.Code), "Geonode: %s", strings.TrimSpace(fmt.Sprintf("%s %s", e.Code, e.Message)))
	}
	return c.HTTPError("Geonode", status, string(raw))
}
