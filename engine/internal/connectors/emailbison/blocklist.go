package emailbison

import (
	"bytes"
	"context"
	"encoding/csv"
	"mime/multipart"
	"net/http"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// The blocklist: GET /api/blacklisted-emails and /api/blacklisted-domains
// list it, page by page; POST .../bulk adds many at once, from a CSV file.

var _ c.BlocklistReader = EmailBison{}

type blocked struct {
	Email  string `json:"email"`
	Domain string `json:"domain"`
	Value  string `json:"value"`
}

func (EmailBison) ListBlocked(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.Blocked, error) {
	a, err := open(cl, creds)
	if err != nil {
		return nil, err
	}
	var out []c.Blocked
	for _, kind := range []string{"emails", "domains"} {
		list, err := all[blocked](ctx, a, "/api/blacklisted-"+kind)
		if err != nil {
			return nil, err
		}
		for _, b := range list {
			v := strings.ToLower(strings.TrimSpace(b.Email + b.Domain))
			if v == "" {
				v = strings.ToLower(strings.TrimSpace(b.Value))
			}
			if v != "" {
				out = append(out, c.Blocked{Value: v, Domain: kind == "domains"})
			}
		}
	}
	return out, nil
}

func (EmailBison) Block(ctx context.Context, cl *c.Client, creds c.Creds, entries []c.Blocked) error {
	a, err := open(cl, creds)
	if err != nil {
		return err
	}
	var emails, domains []string
	for _, e := range entries {
		if e.Domain {
			domains = append(domains, e.Value)
		} else {
			emails = append(emails, e.Value)
		}
	}
	if len(emails) > 0 {
		if err := a.uploadList(ctx, "/api/blacklisted-emails/bulk", "email", emails); err != nil {
			return err
		}
	}
	if len(domains) > 0 {
		return a.uploadList(ctx, "/api/blacklisted-domains/bulk", "domain", domains)
	}
	return nil
}

// uploadList sends a list to one of EmailBison's bulk blocklist endpoints.
// They take a CSV file as multipart form data (field csv), one value a
// row under a header naming the column; a JSON body gets an HTTP 500.
func (a *api) uploadList(ctx context.Context, path, column string, values []string) error {
	var file bytes.Buffer
	w := csv.NewWriter(&file)
	w.Write([]string{column})
	for _, v := range values {
		w.Write([]string{v})
	}
	w.Flush()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("csv", column+"s.csv")
	if err != nil {
		return c.Errf(c.ErrInvalid, "bad EmailBison request: %v", err)
	}
	part.Write(file.Bytes())
	if err := mw.Close(); err != nil {
		return c.Errf(c.ErrInvalid, "bad EmailBison request: %v", err)
	}
	return a.send(ctx, http.MethodPost, path, mw.FormDataContentType(), &body, nil)
}
