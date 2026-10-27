// Package cli is the outflare command line client: every API operation as
// a command, read from the OpenAPI document built into the binary.
//
//	outflare login                    save the server URL and an API token
//	outflare list-leads --q ana       call an operation (flags are its parameters and body fields)
//	outflare create-lead --email ana@acme-com.example --first-name Ana
//	outflare help create-webhook      what an operation takes and needs
//	outflare api GET /v1/whoami       call any path
//	outflare listen --forward-to URL  send new events to a local receiver
package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// Env is what a run reads from and writes to, so tests can drive it.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	ConfigDir      string // where config.json lives; empty uses the user config dir
	HTTP           *http.Client
	Version        string
	// Spec is the OpenAPI document the commands come from.
	Spec []byte
	// TTY reports whether stdout is a terminal (pretty output).
	TTY bool
	// RetryBase is the first wait between retries, doubling each time;
	// zero means 500ms. Tests make it tiny.
	RetryBase time.Duration
}

// Run runs one command line (without the program name) and returns the
// exit code.
func Run(ctx context.Context, env Env, args []string) int {
	doc, err := parseDocument(env.Spec)
	if err != nil {
		fmt.Fprintln(env.Stderr, "outflare: the built-in API document is broken:", err)
		return 2
	}
	c := &runner{env: env, doc: doc}
	if env.HTTP == nil {
		c.env.HTTP = &http.Client{Timeout: 2 * time.Minute}
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		if len(args) > 1 {
			return c.help(args[1])
		}
		c.usage(env.Stdout)
		return 0
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "login":
		return c.login(ctx, rest)
	case "logout":
		return c.logout()
	case "listen":
		if slices.Contains(rest, "--help") || slices.Contains(rest, "-h") {
			return c.help(cmd)
		}
		return c.listen(ctx, rest)
	case "logs":
		if slices.Contains(rest, "--help") || slices.Contains(rest, "-h") {
			return c.help(cmd)
		}
		return c.logs(ctx, rest)
	case "whoami":
		return c.call(ctx, &operation{Method: "GET", Path: "/v1/whoami"}, rest)
	case "api":
		if len(rest) < 2 {
			fmt.Fprintln(env.Stderr, "usage: outflare api METHOD PATH [--data JSON|@file|-] [--query k=v]")
			return 2
		}
		op := &operation{Method: strings.ToUpper(rest[0]), Path: rest[1], BodyType: "application/json", Idempotent: true}
		return c.call(ctx, op, rest[2:])
	}
	op := doc.byCommand[cmd]
	if op == nil {
		fmt.Fprintf(env.Stderr, "outflare: unknown command %q.%s Run \"outflare help\" for the list.\n", cmd, c.suggest(cmd))
		return 2
	}
	if slices.Contains(rest, "--help") || slices.Contains(rest, "-h") {
		return c.help(cmd)
	}
	return c.call(ctx, op, rest)
}

type runner struct {
	env Env
	doc *document
}

// ---- Settings ----

type settings struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	Workspace string `json:"workspace,omitempty"`
	Agency    string `json:"agency,omitempty"`
}

func (c *runner) configPath() (string, error) {
	dir := c.env.ConfigDir
	if dir == "" {
		d, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(d, "outflare")
	}
	return filepath.Join(dir, "config.json"), nil
}

// settings reads the saved login, overridden by OUTFLARE_URL,
// OUTFLARE_TOKEN, OUTFLARE_WORKSPACE and OUTFLARE_AGENCY (the same
// variables the MCP server reads).
func (c *runner) settings() settings {
	var s settings
	if p, err := c.configPath(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			json.Unmarshal(b, &s) //nolint:errcheck
		}
	}
	for name, dst := range map[string]*string{"URL": &s.URL, "TOKEN": &s.Token, "WORKSPACE": &s.Workspace, "AGENCY": &s.Agency} {
		if v := strings.TrimSpace(c.env.Getenv("OUTFLARE_" + name)); v != "" {
			*dst = v
		}
	}
	s.URL = strings.TrimRight(s.URL, "/")
	return s
}

func (c *runner) login(ctx context.Context, args []string) int {
	fl, err := parseFlags(args)
	if err != nil {
		return c.fail(2, err)
	}
	s := c.settings()
	if v := fl.take("url"); v != "" {
		s.URL = strings.TrimRight(v, "/")
	}
	if v := fl.take("workspace"); v != "" {
		s.Workspace = v
	}
	if fl.has("token-stdin") {
		fl.take("token-stdin")
		b, _ := io.ReadAll(c.env.Stdin)
		s.Token = strings.TrimSpace(string(b))
	}
	if len(fl.values) > 0 {
		return c.fail(2, fmt.Errorf("login takes --url, --workspace and --token-stdin"))
	}
	in := bufio.NewReader(c.env.Stdin)
	if s.URL == "" {
		fmt.Fprint(c.env.Stderr, "Server URL (e.g. https://outflare.example.com): ")
		line, _ := in.ReadString('\n')
		s.URL = strings.TrimRight(strings.TrimSpace(line), "/")
	}
	if s.Token == "" {
		fmt.Fprint(c.env.Stderr, "API token (oft_..., from API tokens in the web app): ")
		if f, ok := c.env.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(c.env.Stderr)
			if err != nil {
				return c.fail(1, err)
			}
			s.Token = strings.TrimSpace(string(b))
		} else {
			line, _ := in.ReadString('\n')
			s.Token = strings.TrimSpace(line)
		}
	}
	if s.URL == "" || s.Token == "" {
		return c.fail(2, errors.New("a server URL and an API token are needed"))
	}
	if _, err := url.ParseRequestURI(s.URL); err != nil {
		return c.fail(2, fmt.Errorf("%q is not a URL", s.URL))
	}
	// The token says which workspace it belongs to.
	var who struct {
		Token *struct {
			Name        string  `json:"name"`
			Kind        string  `json:"kind"`
			WorkspaceID *string `json:"workspace_id"`
			Workspace   *struct {
				Slug string `json:"slug"`
				Name string `json:"name"`
			} `json:"workspace"`
			AgencyID *string  `json:"agency_id"`
			Scopes   []string `json:"scopes"`
		} `json:"token"`
	}
	status, body, _, err := c.send(ctx, s, "GET", s.URL+"/v1/whoami", "", nil, "")
	if err != nil {
		return c.fail(1, fmt.Errorf("could not reach %s: %w", s.URL, err))
	}
	if status != 200 {
		return c.fail(1, fmt.Errorf("the server refused the token (%d): %s", status, errorText(body)))
	}
	if json.Unmarshal(body, &who) != nil || who.Token == nil {
		return c.fail(1, errors.New("log in with an API token, not a session"))
	}
	switch {
	case who.Token.WorkspaceID != nil:
		s.Workspace = *who.Token.WorkspaceID
	case who.Token.AgencyID != nil:
		s.Agency = *who.Token.AgencyID
	}
	p, err := c.configPath()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(p), 0o700)
	}
	if err == nil {
		b, _ := json.MarshalIndent(s, "", "  ")
		// WriteFile keeps an existing file's mode, so tighten it too: the
		// file holds the token.
		if err = os.WriteFile(p, b, 0o600); err == nil {
			err = os.Chmod(p, 0o600)
		}
	}
	if err != nil {
		return c.fail(1, fmt.Errorf("save login: %w", err))
	}
	where := "workspace " + s.Workspace
	if w := who.Token.Workspace; w != nil && w.Name != "" {
		where = fmt.Sprintf("workspace %q (%s)", w.Name, w.Slug)
	}
	if who.Token.Kind == "agency" {
		where = "agency " + s.Agency + " (pass --workspace or set OUTFLARE_WORKSPACE for a client)"
	}
	scopes := strings.Join(who.Token.Scopes, ", ")
	if scopes == "" {
		scopes = "none"
	}
	fmt.Fprintf(c.env.Stderr, "Logged in to %s as token %q in %s.\nScopes: %s.\nSaved to %s. Try: outflare whoami, or outflare help for every command.\n", s.URL, who.Token.Name, where, scopes, p)
	return 0
}

func (c *runner) logout() int {
	p, err := c.configPath()
	if err == nil {
		err = os.Remove(p)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c.fail(1, err)
	}
	fmt.Fprintln(c.env.Stderr, "Logged out: the saved token is deleted. Revoke it in the web app if it may have leaked.")
	return 0
}

// ---- Calling ----

// Global flags every operation takes.
var globalFlags = []string{"url", "token", "workspace", "mission", "idempotency-key", "data", "query", "all", "curl", "raw"}

func (c *runner) call(ctx context.Context, op *operation, args []string) int {
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
	mission, key, data := fl.take("mission"), fl.take("idempotency-key"), fl.take("data")
	all, curl, raw := fl.bool("all"), fl.bool("curl"), fl.bool("raw")
	extraQuery := fl.takeAll("query")
	if s.URL == "" {
		return c.fail(2, errors.New(`no server: run "outflare login", or set OUTFLARE_URL and OUTFLARE_TOKEN`))
	}

	// Path and query parameters.
	path := op.Path
	q := url.Values{}
	for _, p := range op.Params {
		name := kebab(p.Name)
		vals := fl.takeAll(name)
		if p.In == "path" {
			v := ""
			if len(vals) > 0 {
				v = vals[len(vals)-1]
			}
			if v == "" && p.Name == "ws" {
				if s.Workspace == "" && !curl {
					// Only OUTFLARE_URL and OUTFLARE_TOKEN set: the token
					// knows its workspace.
					s.Workspace = c.tokenWorkspace(ctx, s)
				}
				v = s.Workspace
			}
			if v == "" && p.Name == "ag" {
				v = s.Agency
			}
			if v == "" {
				return c.fail(2, fmt.Errorf("--%s is required%s", name, describeParam(p)))
			}
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(v))
			continue
		}
		for _, v := range vals {
			if p.Schema.Type == "array" {
				for _, part := range strings.Split(v, ",") {
					q.Add(p.Name, strings.TrimSpace(part))
				}
			} else {
				q.Add(p.Name, v)
			}
		}
		if p.Required && len(vals) == 0 {
			return c.fail(2, fmt.Errorf("--%s is required%s", name, describeParam(p)))
		}
	}
	for _, kv := range extraQuery {
		k, v, _ := strings.Cut(kv, "=")
		q.Add(k, v)
	}
	if strings.Contains(path, "{") {
		return c.fail(2, fmt.Errorf("fill in the path: %s", path))
	}

	// The body: --data, then any body fields given as flags.
	var body []byte
	contentType := op.BodyType
	if data != "" {
		if body, err = c.readData(data); err != nil {
			return c.fail(2, err)
		}
	}
	if len(fl.values) > 0 {
		if op.BodyType != "application/json" {
			return c.fail(2, c.unknownFlags(op, fl))
		}
		obj := map[string]any{}
		if len(body) > 0 && json.Unmarshal(body, &obj) != nil {
			return c.fail(2, errors.New("--data must be a JSON object to combine with field flags"))
		}
		for _, f := range op.BodyFields {
			vals := fl.takeAll(kebab(f.Name))
			if len(vals) == 0 {
				continue
			}
			v, err := convert(f.Schema, vals)
			if err != nil {
				return c.fail(2, fmt.Errorf("--%s: %w", kebab(f.Name), err))
			}
			obj[f.Name] = v
		}
		if len(fl.values) > 0 {
			return c.fail(2, c.unknownFlags(op, fl))
		}
		body, _ = json.Marshal(obj)
	}
	if op.BodyNeeded && len(body) == 0 {
		if op.BodyType == "application/json" {
			var missing []string
			for _, f := range op.BodyFields {
				if f.Required {
					missing = append(missing, "--"+kebab(f.Name))
				}
			}
			if len(missing) > 0 {
				return c.fail(2, fmt.Errorf("%s needs %s (or --data); see outflare help %s", op.Command, strings.Join(missing, ", "), op.Command))
			}
			body = []byte("{}")
		} else {
			return c.fail(2, fmt.Errorf("%s needs a %s body: --data @file or --data - for stdin", op.Command, op.BodyType))
		}
	}
	if len(body) == 0 {
		contentType = ""
	}
	if key == "" && op.Idempotent && op.Method != "GET" {
		key = newKey()
	}

	target := s.URL + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	if curl {
		c.printCurl(op.Method, target, contentType, body, mission, key)
		return 0
	}
	if s.Token == "" {
		return c.fail(2, errors.New(`no token: run "outflare login", or set OUTFLARE_TOKEN`))
	}

	if all && op.Method == "GET" {
		return c.paginate(ctx, s, target, mission, raw)
	}
	status, resp, hdr, err := c.send(ctx, s, op.Method, target, contentType, body, mission, key)
	if err != nil {
		return c.fail(1, err)
	}
	return c.output(status, resp, hdr, raw)
}

func describeParam(p param) string {
	if len(p.Schema.Enum) > 0 {
		return " (" + strings.Join(p.Schema.Enum, ", ") + ")"
	}
	if p.Desc != "" {
		return ": " + firstLine(p.Desc)
	}
	return ""
}

func (c *runner) readData(data string) ([]byte, error) {
	switch {
	case data == "-":
		return io.ReadAll(c.env.Stdin)
	case strings.HasPrefix(data, "@"):
		return os.ReadFile(data[1:])
	default:
		return []byte(data), nil
	}
}

// convert turns flag values into the JSON value the schema wants.
func convert(s schema, vals []string) (any, error) {
	one := vals[len(vals)-1]
	switch s.Type {
	case "integer":
		return strconv.ParseInt(one, 10, 64)
	case "number":
		return strconv.ParseFloat(one, 64)
	case "boolean":
		return strconv.ParseBool(one)
	case "array":
		if len(vals) == 1 && strings.HasPrefix(strings.TrimSpace(one), "[") {
			var v []any
			return v, json.Unmarshal([]byte(one), &v)
		}
		var out []any
		for _, v := range vals {
			for _, part := range strings.Split(v, ",") {
				item, err := convert(schema{Type: s.Items}, []string{strings.TrimSpace(part)})
				if err != nil {
					return nil, err
				}
				out = append(out, item)
			}
		}
		return out, nil
	case "object", "":
		if t := strings.TrimSpace(one); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			var v any
			if err := json.Unmarshal([]byte(t), &v); err != nil {
				return nil, fmt.Errorf("not valid JSON: %w", err)
			}
			return v, nil
		}
		if s.Type == "object" {
			return nil, errors.New("give a JSON object")
		}
	}
	if one == "null" {
		return nil, nil
	}
	return one, nil
}

func (c *runner) unknownFlags(op *operation, fl *flags) error {
	var names []string
	for n := range fl.values {
		names = append(names, "--"+n)
	}
	sort.Strings(names)
	return fmt.Errorf("%s does not take %s; see outflare help %s", op.Command, strings.Join(names, ", "), op.Command)
}

// send makes the request, retrying network errors, 429 and 502–504. Writes
// are only retried when they carry an Idempotency-Key, so a retry never
// does anything twice.
func (c *runner) send(ctx context.Context, s settings, method, target, contentType string, body []byte, mission string, key ...string) (int, []byte, http.Header, error) {
	idem := ""
	if len(key) > 0 {
		idem = key[0]
	}
	retryable := method == "GET" || idem != ""
	var lastErr error
	var after time.Duration // the server's Retry-After, when it sent one
	base := c.env.RetryBase
	if base == 0 {
		base = 500 * time.Millisecond
	}
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			wait := base << (attempt - 1)
			if after > wait {
				wait = min(after, time.Minute)
			}
			select {
			case <-ctx.Done():
				return 0, nil, nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
		if err != nil {
			return 0, nil, nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Authorization", "Bearer "+s.Token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "outflare-cli/"+c.env.Version)
		if mission != "" {
			req.Header.Set("Outflare-Mission", mission)
		}
		if idem != "" {
			req.Header.Set("Idempotency-Key", idem)
		}
		resp, err := c.env.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if retryable {
				continue
			}
			return 0, nil, nil, err
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			if retryable {
				continue
			}
			return 0, nil, nil, err
		}
		switch resp.StatusCode {
		case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			if retryable && attempt < 3 {
				lastErr = fmt.Errorf("server answered %d", resp.StatusCode)
				after = 0
				if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n > 0 {
					after = time.Duration(n) * time.Second
				}
				continue
			}
		}
		return resp.StatusCode, b, resp.Header, nil
	}
	return 0, nil, nil, lastErr
}

func (c *runner) output(status int, body []byte, hdr http.Header, raw bool) int {
	// An error goes to stderr only, so stdout piped into jq or a file
	// never holds an error body; --raw prints it to stdout as well.
	if len(body) > 0 && (raw || status >= 200 && status < 300) {
		c.write(body, raw)
	}
	switch {
	case status >= 200 && status < 300:
		if status == http.StatusAccepted && bytes.Contains(body, []byte(`"mission_action"`)) {
			fmt.Fprintln(c.env.Stderr, "Held by the mission: it did not run yet (see mission_action).")
		}
		return 0
	default:
		rid := hdr.Get("X-Request-Id")
		msg := errorText(body)
		if rid != "" {
			msg += " (request " + rid + ")"
		}
		fmt.Fprintf(c.env.Stderr, "outflare: %d %s: %s\n", status, http.StatusText(status), msg)
		return 1
	}
}

func (c *runner) write(body []byte, raw bool) {
	var v any
	if !raw && c.env.TTY && json.Unmarshal(body, &v) == nil {
		b, _ := json.MarshalIndent(v, "", "  ")
		c.env.Stdout.Write(append(b, '\n')) //nolint:errcheck
		return
	}
	c.env.Stdout.Write(body) //nolint:errcheck
	if !bytes.HasSuffix(body, []byte("\n")) {
		fmt.Fprintln(c.env.Stdout)
	}
}

// paginate follows next_cursor (or next_before, next_after or next_offset) and prints one
// {"data": [...]} with every page.
func (c *runner) paginate(ctx context.Context, s settings, target, mission string, raw bool) int {
	var all []json.RawMessage
	for pages := 0; pages < 1000; pages++ {
		status, body, hdr, err := c.send(ctx, s, "GET", target, "", nil, mission)
		if err != nil {
			return c.fail(1, err)
		}
		if status != 200 {
			return c.output(status, body, hdr, raw)
		}
		var page map[string]json.RawMessage
		if json.Unmarshal(body, &page) != nil {
			return c.output(status, body, hdr, raw)
		}
		var data []json.RawMessage
		json.Unmarshal(page["data"], &data) //nolint:errcheck
		all = append(all, data...)
		// next_cursor is the one rule every list follows; the older
		// next_ fields are a fallback for servers that predate it.
		next := ""
		for _, k := range []string{"cursor", "before", "after", "offset"} {
			if v, ok := page["next_"+k]; ok && string(v) != "null" {
				u, _ := url.Parse(target)
				q := u.Query()
				q.Del("cursor")
				q.Set(k, strings.Trim(string(v), `"`))
				u.RawQuery = q.Encode()
				next = u.String()
				break
			}
		}
		if string(page["has_more"]) == "false" {
			break
		}
		if next == "" || len(data) == 0 {
			break
		}
		target = next
	}
	if all == nil {
		all = []json.RawMessage{}
	}
	b, _ := json.Marshal(map[string]any{"data": all})
	c.write(b, raw)
	return 0
}

func (c *runner) printCurl(method, target, contentType string, body []byte, mission, key string) {
	var b strings.Builder
	fmt.Fprintf(&b, "curl -X %s %s \\\n  -H \"Authorization: Bearer $OUTFLARE_TOKEN\"", method, shellQuote(target))
	if contentType != "" {
		fmt.Fprintf(&b, " \\\n  -H %s", shellQuote("Content-Type: "+contentType))
	}
	if mission != "" {
		fmt.Fprintf(&b, " \\\n  -H %s", shellQuote("Outflare-Mission: "+mission))
	}
	if key != "" {
		fmt.Fprintf(&b, " \\\n  -H %s", shellQuote("Idempotency-Key: "+key))
	}
	if len(body) > 0 {
		fmt.Fprintf(&b, " \\\n  -d %s", shellQuote(string(body)))
	}
	fmt.Fprintln(c.env.Stdout, b.String())
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func errorText(body []byte) string {
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Code + ": " + e.Error.Message
	}
	return strings.TrimSpace(string(body))
}

// tokenWorkspace asks whoami which workspace the token belongs to; empty
// for an agency token or when the server cannot say.
func (c *runner) tokenWorkspace(ctx context.Context, s settings) string {
	if s.Token == "" {
		return ""
	}
	status, body, _, err := c.send(ctx, s, "GET", s.URL+"/v1/whoami", "", nil, "")
	if err != nil || status != http.StatusOK {
		return ""
	}
	var who struct {
		Token *struct {
			WorkspaceID *string `json:"workspace_id"`
		} `json:"token"`
	}
	if json.Unmarshal(body, &who) != nil || who.Token == nil || who.Token.WorkspaceID == nil {
		return ""
	}
	return *who.Token.WorkspaceID
}

func (c *runner) fail(code int, err error) int {
	fmt.Fprintln(c.env.Stderr, "outflare:", err)
	return code
}

func newKey() string {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ---- Flags ----

// flags are --name value, --name=value, or a bare --name (true).
type flags struct{ values map[string][]string }

func parseFlags(args []string) (*flags, error) {
	f := &flags{values: map[string][]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") || a == "--" {
			return nil, fmt.Errorf("unexpected %q: options are --name value", a)
		}
		name, val, hasVal := strings.Cut(a[2:], "=")
		name = kebab(name)
		if !hasVal {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				val = args[i+1]
				i++
			} else {
				val = "true"
			}
		}
		f.values[name] = append(f.values[name], val)
	}
	return f, nil
}

func (f *flags) has(name string) bool { _, ok := f.values[name]; return ok }

func (f *flags) takeAll(name string) []string {
	v := f.values[name]
	delete(f.values, name)
	return v
}

func (f *flags) take(name string) string {
	v := f.takeAll(name)
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

func (f *flags) bool(name string) bool {
	v := f.take(name)
	b, _ := strconv.ParseBool(v)
	return b
}
