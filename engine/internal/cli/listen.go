package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/webhooks"
)

// listen follows the workspace's events feed from now on and prints each
// event, or forwards it to a local URL signed the way real webhook
// deliveries are, so a receiver can be built and tested on a laptop with
// no public URL.
func (c *runner) listen(ctx context.Context, args []string) int {
	fl, err := parseFlags(args)
	if err != nil {
		return c.fail(2, err)
	}
	s := c.settings()
	if v := fl.take("url"); v != "" {
		s.URL = strings.TrimRight(v, "/")
	}
	if v := fl.take("token"); v != "" {
		s.Token = v
	}
	if v := fl.take("workspace"); v != "" {
		s.Workspace = v
	}
	forward, types, secret := fl.take("forward-to"), fl.take("types"), fl.take("secret")
	every := time.Second
	if v := fl.take("interval"); v != "" {
		if every, err = time.ParseDuration(v); err != nil || every < 100*time.Millisecond {
			return c.fail(2, errors.New("--interval is a duration of at least 100ms, such as 2s"))
		}
	}
	for name := range fl.values {
		return c.fail(2, fmt.Errorf("unknown option --%s; see outflare help listen", name))
	}
	if s.URL == "" {
		return c.fail(2, errors.New(`no server: run "outflare login", or set OUTFLARE_URL and OUTFLARE_TOKEN`))
	}
	if s.Workspace == "" {
		return c.fail(2, errors.New("--workspace is required: this token is not tied to one workspace"))
	}
	if forward != "" {
		if u, err := url.Parse(forward); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return c.fail(2, errors.New("--forward-to is an http or https URL, such as http://localhost:3000/webhooks"))
		}
		if secret == "" {
			secret = webhooks.NewSecret()
		}
	}

	feed := s.URL + "/v1/workspaces/" + url.PathEscape(s.Workspace) + "/events"
	after := "latest"
	if forward != "" {
		fmt.Fprintf(c.env.Stderr, "Forwarding events to %s, signed with %s\nPass that secret to verifyWebhook in your receiver. Ctrl-C to stop.\n", forward, secret)
	} else {
		fmt.Fprintln(c.env.Stderr, "Listening for events. Ctrl-C to stop.")
	}
	for {
		q := url.Values{"after": {after}, "limit": {"100"}}
		if types != "" {
			q.Set("types", types)
		}
		status, body, hdr, err := c.send(ctx, s, "GET", feed+"?"+q.Encode(), "", nil, "")
		if ctx.Err() != nil {
			return 0
		}
		if err != nil {
			return c.fail(1, err)
		}
		if status != http.StatusOK {
			return c.output(status, body, hdr, true)
		}
		var page struct {
			Data      []json.RawMessage `json:"data"`
			NextAfter string            `json:"next_after"`
			HasMore   bool              `json:"has_more"`
		}
		if err := json.Unmarshal(body, &page); err != nil || page.NextAfter == "" {
			return c.fail(1, errors.New("the server's events feed answered something unexpected"))
		}
		for _, ev := range page.Data {
			if forward == "" {
				c.write(ev, !c.env.TTY)
				continue
			}
			c.forward(ctx, forward, secret, ev)
		}
		after = page.NextAfter
		if page.HasMore {
			continue
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(every):
		}
	}
}

// forward POSTs one event with the Standard Webhooks headers and reports
// how the receiver answered. A failure is shown and not retried: the point
// is to see what the receiver does.
func (c *runner) forward(ctx context.Context, target, secret string, ev json.RawMessage) {
	var head struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	json.Unmarshal(ev, &head) //nolint:errcheck
	now := time.Now()
	sig, err := webhooks.Sign(secret, head.ID, now, ev)
	if err != nil {
		fmt.Fprintln(c.env.Stderr, "outflare: --secret is not a whsec_ secret:", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(ev))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "outflare-cli/"+c.env.Version)
	req.Header.Set("webhook-id", head.ID)
	req.Header.Set("webhook-timestamp", fmt.Sprint(now.Unix()))
	req.Header.Set("webhook-signature", sig)
	start := time.Now()
	resp, err := c.env.HTTP.Do(req)
	if err != nil {
		fmt.Fprintf(c.env.Stderr, "%s  %-24s  failed: %v\n", now.Format("15:04:05"), head.Type, err)
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) //nolint:errcheck
	resp.Body.Close()
	fmt.Fprintf(c.env.Stderr, "%s  %-24s  %d %s  %s  %dms\n", now.Format("15:04:05"), head.Type,
		resp.StatusCode, http.StatusText(resp.StatusCode), head.ID, time.Since(start).Milliseconds())
}
