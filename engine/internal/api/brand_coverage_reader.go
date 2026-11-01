package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
)

// The connected reader is explicit, never a fallback around an owned-site
// check. ReadPage still checks robots and limits redirects. Credentials and
// costs come only from this workspace's brand-capable connection.
type coverageConnectedReader struct {
	server               *Server
	workspace, placement string
	opened               *connectors.Opened
	reader               connectors.PageFetcher
	native               crawl.Fetcher
}

func (f *coverageConnectedReader) Fetch(ctx context.Context, raw string) (*crawl.Response, error) {
	if err := crawl.CheckURL(raw, false); err != nil {
		return nil, err
	}
	if strings.HasSuffix(raw, "/robots.txt") {
		return f.native.Fetch(ctx, raw)
	}
	if err := usagelimits.Check(ctx, f.server.pool, f.workspace, f.opened.ID, "", 0); err != nil {
		return nil, err
	}
	page, err := f.reader.FetchPage(ctx, f.opened.Client, f.opened.Creds, raw, true)
	if page.Cost.Units > 0 || page.Cost.USD > 0 {
		id, ref := f.opened.ID, "coverage_read:"+f.placement+":"+time.Now().UTC().Format(time.RFC3339Nano)
		entry := outcomes.Entry{WorkspaceID: f.workspace, Category: outcomes.CategoryData, Provider: f.opened.Connector.Definition().ID, IntegrationID: &id, OccurredAt: time.Now(), Source: outcomes.SourceAuto, Ref: &ref, Note: "Coverage page read"}
		if page.Cost.Units > 0 {
			units := page.Cost.Units
			entry.Units = &units
			entry.UnitName = "token"
		} else {
			cents := int64(math.Ceil(page.Cost.USD * 100))
			entry.AmountCents = &cents
			entry.Currency = "USD"
		}
		if _, _, bookErr := outcomes.Record(ctx, f.server.pool, entry); bookErr != nil {
			return nil, bookErr
		}
	}
	if err != nil {
		return nil, err
	}
	if len(page.HTML) > 5<<20 {
		return nil, errors.New("page reader returned more than 5 MB")
	}
	if page.URL != "" && page.URL != raw {
		// Do not treat a redirect destination as proof about the requested URL.
		if err := crawl.CheckURL(page.URL, false); err != nil {
			return nil, err
		}
		return &crawl.Response{URL: raw, Status: 302, Header: http.Header{"Location": []string{page.URL}}}, nil
	}
	return &crawl.Response{URL: raw, Status: page.Status, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: []byte(page.HTML)}, nil
}
func (s *Server) coverageReader(ctx context.Context, ws, brand, placement string, native crawl.Fetcher) (crawl.Fetcher, error) {
	if s.integrations == nil {
		return nil, connectors.ErrNoCapable
	}
	opened, err := s.integrations.OpenCapable(ctx, s.pool, ws, brand, connectors.CapCrawlFetch)
	if err != nil {
		return nil, err
	}
	reader, ok := opened.Connector.(connectors.PageFetcher)
	if !ok {
		return nil, connectors.ErrNoCapable
	}
	return &coverageConnectedReader{server: s, workspace: ws, placement: placement, opened: opened, reader: reader, native: native}, nil
}
