// Package seo connects SEO data providers. Each maps its own endpoints
// onto the shared SEO reports in connectors (overview, keywords, backlinks,
// competitors, audit) and says what every call cost, because these
// providers charge per call. Nothing here writes to the provider.
//
// Request shapes follow each provider's public API reference. Base URLs can
// be pointed at a fake in tests through connectors.Client.BaseURLs.
package seo

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// All returns every available SEO connector.
func All() []c.Connector {
	return []c.Connector{DataForSEO{}, Ahrefs{}}
}

func def(id, name, site, docs, keyURL, summary, description, pricing string, fields []c.Field) c.Definition {
	return c.Definition{
		ID: id, Name: name, Category: c.CategorySEO, Website: site, DocsURL: docs, KeyURL: keyURL,
		Summary: summary, Description: description, Auth: c.AuthAPIKey, Fields: fields,
		Capabilities: []string{c.CapSEO}, Pricing: pricing,
	}
}

func get(ctx context.Context, cl *c.Client, name, u string, header http.Header, out any) error {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return c.Errf(c.ErrInvalid, "bad %s address: %v", name, err)
	}
	for h, v := range header {
		req.Header[h] = v
	}
	_, err = c.DoJSON(ctx, cl, name, req, out)
	return err
}

func post(ctx context.Context, cl *c.Client, name, u string, header http.Header, body, out any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return c.Errf(c.ErrInvalid, "bad %s address: %v", name, err)
	}
	for h, v := range header {
		req.Header[h] = v
	}
	req.Header.Set("Content-Type", "application/json")
	_, err = c.DoJSON(ctx, cl, name, req, out)
	return err
}

// n makes a metric from a number the provider may leave out.
func n(v *float64) *float64 {
	if v == nil {
		return nil
	}
	return c.Num(*v)
}

func sum(vs ...*float64) *float64 {
	var t float64
	any := false
	for _, v := range vs {
		if v != nil {
			t += *v
			any = true
		}
	}
	if !any {
		return nil
	}
	return c.Num(t)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func itoa(i int) string { return strconv.Itoa(i) }

// ---- Loose JSON ----

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func items(result map[string]any) []map[string]any {
	list, _ := result["items"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func firstItem(result map[string]any) map[string]any {
	if l := items(result); len(l) > 0 {
		return l[0]
	}
	return nil
}

func num(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return c.Num(x)
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return c.Num(f)
		}
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return c.Num(f)
		}
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

func sorted(s []string) []string {
	sort.Strings(s)
	return s
}

func sortRows(rows []c.SEORow, less func(a, b c.SEORow) bool) {
	sort.SliceStable(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
}
