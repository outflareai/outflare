package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// logs prints the workspace's request logs, oldest first, like tail: the
// latest calls, then with --follow every new one as it happens. In a
// terminal each call is one line; piped, each is a JSON object.
func (c *runner) logs(ctx context.Context, args []string) int {
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
	follow := fl.bool("follow")
	lines := 50
	if v := fl.take("lines"); v != "" {
		if _, err := fmt.Sscan(v, &lines); err != nil || lines < 0 || lines > 500 {
			return c.fail(2, errors.New("--lines is a number from 0 to 500"))
		}
	}
	every := 2 * time.Second
	if v := fl.take("interval"); v != "" {
		if every, err = time.ParseDuration(v); err != nil || every < 100*time.Millisecond {
			return c.fail(2, errors.New("--interval is a duration of at least 100ms, such as 2s"))
		}
	}
	filter := url.Values{}
	for _, k := range []string{"level", "status", "surface", "method", "operation", "error-code", "mcp-tool", "request-id", "mission-id", "brand", "q", "since"} {
		if v := fl.take(k); v != "" {
			filter.Set(strings.ReplaceAll(k, "-", "_"), v)
		}
	}
	for name := range fl.values {
		return c.fail(2, fmt.Errorf("unknown option --%s; see outflare help logs", name))
	}
	if s.URL == "" {
		return c.fail(2, errors.New(`no server: run "outflare login", or set OUTFLARE_URL and OUTFLARE_TOKEN`))
	}
	if s.Workspace == "" {
		return c.fail(2, errors.New("--workspace is required: this token is not tied to one workspace"))
	}
	feed := s.URL + "/v1/workspaces/" + url.PathEscape(s.Workspace) + "/logs"

	type page struct {
		Data       []json.RawMessage `json:"data"`
		NextAfter  *string           `json:"next_after"`
		NextCursor *string           `json:"next_cursor"`
	}
	get := func(q url.Values) (*page, int) {
		for k, v := range filter {
			q[k] = v
		}
		status, body, hdr, err := c.send(ctx, s, "GET", feed+"?"+q.Encode(), "", nil, "")
		if ctx.Err() != nil {
			return nil, 0
		}
		if err != nil {
			return nil, c.fail(1, err)
		}
		if status != http.StatusOK {
			return nil, c.output(status, body, hdr, true)
		}
		var p page
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, c.fail(1, errors.New("the server's logs answered something unexpected"))
		}
		return &p, -1
	}

	// Where following starts: before the latest calls, which are printed
	// first, oldest first.
	start, code := get(url.Values{"after": {"latest"}})
	if start == nil {
		return code
	}
	after := *start.NextAfter
	if lines > 0 {
		recent, code := get(url.Values{"limit": {fmt.Sprint(lines)}})
		if recent == nil {
			return code
		}
		for i := len(recent.Data) - 1; i >= 0; i-- {
			c.logLine(recent.Data[i])
		}
	}
	if !follow {
		return 0
	}
	fmt.Fprintln(c.env.Stderr, "Following new calls. Ctrl-C to stop.")
	for {
		p, code := get(url.Values{"after": {after}, "limit": {"500"}})
		if p == nil {
			return max(code, 0)
		}
		for _, l := range p.Data {
			c.logLine(l)
		}
		if p.NextAfter != nil {
			after = *p.NextAfter
		}
		if p.NextCursor != nil {
			continue
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(every):
		}
	}
}

// logLine prints one call: a line in a terminal, its JSON when piped.
func (c *runner) logLine(raw json.RawMessage) {
	if !c.env.TTY {
		c.write(raw, true)
		return
	}
	var l struct {
		CreatedAt  time.Time `json:"created_at"`
		Method     string    `json:"method"`
		Path       string    `json:"path"`
		Status     int       `json:"status"`
		DurationMS int       `json:"duration_ms"`
		ErrorCode  *string   `json:"error_code"`
		Surface    string    `json:"surface"`
		MCPTool    *string   `json:"mcp_tool"`
		ActorName  *string   `json:"actor_name"`
		RequestID  string    `json:"request_id"`
	}
	if json.Unmarshal(raw, &l) != nil {
		return
	}
	who := l.Surface
	if l.MCPTool != nil {
		who += ":" + *l.MCPTool
	}
	if l.ActorName != nil {
		who += " " + *l.ActorName
	}
	extra := ""
	if l.ErrorCode != nil {
		extra = " " + *l.ErrorCode
	}
	fmt.Fprintf(c.env.Stdout, "%s  %d  %-6s %s  %dms  %s%s  %s\n", l.CreatedAt.Local().Format("15:04:05"), l.Status, l.Method, l.Path,
		l.DurationMS, who, extra, l.RequestID)
}

const logsHelp = `Show the workspace's request logs, and follow new calls

  outflare logs [--follow] [--level warning,error] [--surface mcp] [--since 1h]
  Needs: scope audit.read

Prints the latest calls made in the workspace, from the web app, the API,
MCP, the CLI and missions, oldest first: one line each in a terminal, JSON
when piped. With --follow it keeps printing new calls as they happen.

Flags:
  --follow           keep printing new calls (Ctrl-C to stop)
  --lines 50         how many recent calls to print first (0 to 500)
  --level LIST       info, warning (4xx) or error (5xx)
  --status LIST      codes or classes, such as 404,5xx
  --surface LIST     web, api, mcp, cli, sdk or mission
  --method LIST      such as POST,DELETE
  --operation LIST   operation ids, such as createLead
  --error-code CODE  such as rate_limited
  --mcp-tool NAME    calls an MCP tool made
  --request-id ID    one call
  --mission-id ID    calls made in a mission
  --brand HANDLE     calls about a brand
  --q TEXT           text in the path, operation, error code or request id
  --since 1h         only calls since then (15m, 24h, 7d or a timestamp)
  --interval 2s      how often --follow checks for new calls
`
