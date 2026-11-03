// Package cms connects the brand's own website's CMS, so content written
// in Outflare (by a person, or drafted by an agent and approved) is
// published there as posts and pages, through the workspace's own
// connection (AGENTS.md "Bring your own keys"): WordPress with an
// application password, Ghost with an Admin API key. Each connector
// implements connectors.CMSPublisher; the engine's publish job calls it
// once per queued publication and reads the post back.
package cms

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// All lists the CMS connectors.
func All() []c.Connector {
	return []c.Connector{WordPress{}, Ghost{}}
}

func def(id, name, site, docs, keyURL, summary, description string) c.Definition {
	return c.Definition{
		ID: id, Name: name, Category: c.CategoryCMS, Website: site, DocsURL: docs, KeyURL: keyURL,
		Summary: summary, Description: description, Auth: c.AuthAPIKey, Fields: []c.Field{},
		Capabilities: []string{c.CapCMSPublish}, Pricing: "Free API; your own site and plan",
	}
}

// siteURL is the connection's site address, without a trailing slash.
func siteURL(creds c.Creds) string {
	u := strings.TrimSpace(creds["site_url"])
	if u != "" && !strings.Contains(u, "://") {
		u = "https://" + u
	}
	return strings.TrimRight(u, "/")
}

// ---- HTTP ----

type call struct {
	cl     *c.Client
	name   string
	header http.Header
}

func newCall(cl *c.Client, name string) *call {
	return &call{cl: cl, name: name, header: http.Header{}}
}

func (k *call) set(key, value string) *call {
	if value != "" {
		k.header.Set(key, value)
	}
	return k
}

func (k *call) do(ctx context.Context, method, u string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return 0, c.Errf(c.ErrInvalid, "bad %s address: %v", k.name, err)
	}
	for h, v := range k.header {
		req.Header[h] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.DoJSON(ctx, k.cl, k.name, req, out)
}

func (k *call) get(ctx context.Context, u string, out any) error {
	_, err := k.do(ctx, http.MethodGet, u, nil, out)
	return err
}

func (k *call) post(ctx context.Context, u string, body, out any) error {
	_, err := k.do(ctx, http.MethodPost, u, body, out)
	return err
}

func (k *call) put(ctx context.Context, u string, body, out any) error {
	_, err := k.do(ctx, http.MethodPut, u, body, out)
	return err
}

// status is a CMS status as draft or published.
func status(live bool) string {
	if live {
		return "published"
	}
	return "draft"
}
