package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

func (c *runner) usage(w io.Writer) {
	fmt.Fprintf(w, `outflare %s: the Outflare engine and its command line client.

Server:
  outflare serve                 run migrations, then the API and workers
  outflare migrate               run migrations and exit
  outflare version               print the version

Client:
  outflare login [--url URL]     save a server and API token (OUTFLARE_URL / OUTFLARE_TOKEN also work)
  outflare whoami                the token you are using, its workspace and scopes
  outflare logout                forget the saved token
  outflare help COMMAND          what a command takes, and the scope it needs
  outflare api METHOD PATH       call any path, e.g. outflare api GET /v1/meta
  outflare listen                new events as they happen; --forward-to URL sends them to a local receiver
  outflare logs [--follow]       the latest API calls in the workspace; --follow prints new ones as they happen

Every API operation is a command. Flags are its parameters and body fields
(--first-name Ana), or pass the body with --data JSON, --data @file.json or
--data - (stdin). The workspace comes from your login.
  --all               follow pages and print every item
  --curl              print the request as curl instead of sending it
  --mission ID        act inside an agent mission
  --idempotency-key K retry-safe key (writes get a fresh one automatically)
  --raw               print the answer as the server sent it

`, c.env.Version)
	byTag := map[string][]*operation{}
	width := 0
	for _, op := range c.doc.ops {
		// Operations only a signed-in person (a browser) can use are left
		// out: the client signs in with a token.
		if op.Access["signed_in"] == true {
			continue
		}
		byTag[op.Tag] = append(byTag[op.Tag], op)
		width = max(width, len(op.Command))
	}
	tags := make([]string, 0, len(byTag))
	for t := range byTag {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	for _, t := range tags {
		fmt.Fprintf(w, "%s:\n", strings.ToUpper(t[:1])+t[1:])
		for _, op := range byTag[t] {
			fmt.Fprintf(w, "  %-*s  %s\n", width, op.Command, op.Summary)
		}
		fmt.Fprintln(w)
	}
}

const listenHelp = `Send new workspace events to your terminal or a local receiver

  outflare listen [--forward-to URL] [--types lead.*,mailbox.auto_paused]
  Needs: scope webhooks.read

Reads the events feed from now on, so you can build and test a webhook
receiver on your own machine without a public URL. Without --forward-to,
each event is printed as JSON. With it, each event is POSTed to the URL
with the same webhook-id, webhook-timestamp and webhook-signature headers
a real delivery has, signed with a secret printed when it starts; pass
that secret to verifyWebhook in your receiver. Failed forwards are shown,
not retried.

Flags:
  --forward-to URL   a local receiver, such as http://localhost:3000/webhooks
  --types LIST       comma-separated event types or wildcards (default: all)
  --secret whsec_…   sign with this secret instead of a new one
  --interval 1s      how often to check for new events
`

func (c *runner) help(cmd string) int {
	if cmd == "listen" {
		fmt.Fprint(c.env.Stdout, listenHelp)
		return 0
	}
	if cmd == "logs" {
		fmt.Fprint(c.env.Stdout, logsHelp)
		return 0
	}
	op := c.doc.byCommand[cmd]
	if op == nil {
		fmt.Fprintf(c.env.Stderr, "outflare: unknown command %q.%s\n", cmd, c.suggest(cmd))
		return 2
	}
	w := c.env.Stdout
	fmt.Fprintf(w, "%s\n\n  %s %s\n", op.Summary, op.Method, op.Path)
	if access := describeAccess(op.Access); access != "" {
		fmt.Fprintf(w, "  Needs: %s\n", access)
	}
	if op.Description != "" {
		fmt.Fprintf(w, "\n%s\n", indent(strings.TrimSpace(op.Description)))
	}
	var lines [][2]string
	for _, p := range op.Params {
		if p.In == "path" && (p.Name == "ws" || p.Name == "ag") {
			continue
		}
		lines = append(lines, [2]string{flagLabel(kebab(p.Name), p.Schema, p.Required), firstLine(p.Desc)})
	}
	for _, f := range op.BodyFields {
		lines = append(lines, [2]string{flagLabel(kebab(f.Name), f.Schema, f.Required), firstLine(f.Schema.Description)})
	}
	if len(lines) > 0 {
		fmt.Fprintln(w, "\nFlags:")
		width := 0
		for _, l := range lines {
			width = max(width, len(l[0]))
		}
		for _, l := range lines {
			fmt.Fprintf(w, "  %-*s  %s\n", width, l[0], l[1])
		}
	}
	if op.BodyType != "" && op.BodyType != "application/json" {
		fmt.Fprintf(w, "\nBody: %s, with --data @file or --data - (stdin).\n", op.BodyType)
	}
	fmt.Fprintf(w, "\nExample:\n  outflare %s%s\n", op.Command, example(op))
	return 0
}

func flagLabel(name string, s schema, required bool) string {
	l := "--" + name
	switch {
	case len(s.Enum) > 0 && s.Type != "array":
		l += " " + strings.Join(s.Enum, "|")
	case s.Type == "array":
		l += " a,b,..."
	case s.Type == "boolean":
		l += " [true|false]"
	case s.Type != "" && s.Type != "string":
		l += " " + s.Type
	}
	if required {
		l += " (required)"
	}
	return l
}

func example(op *operation) string {
	var b strings.Builder
	for _, p := range op.Params {
		if p.Required && p.In == "path" && p.Name != "ws" && p.Name != "ag" {
			fmt.Fprintf(&b, " --%s <%s>", kebab(p.Name), p.Name)
		}
	}
	for _, f := range op.BodyFields {
		if f.Required {
			fmt.Fprintf(&b, " --%s <%s>", kebab(f.Name), f.Name)
		}
	}
	return b.String()
}

func describeAccess(a map[string]any) string {
	var parts []string
	if v, ok := a["scope"].(string); ok {
		parts = append(parts, "the "+v+" scope")
	}
	if a["signed_in"] == true {
		parts = append(parts, "a signed-in person (not an API token)")
	}
	if a["authenticated"] == true {
		parts = append(parts, "any token")
	}
	return strings.Join(parts, ", ")
}

// suggest names the closest commands, for a typo.
func (c *runner) suggest(cmd string) string {
	type cand struct {
		name string
		d    int
	}
	var cs []cand
	for name := range c.doc.byCommand {
		d := distance(cmd, name)
		if strings.Contains(name, cmd) {
			d = min(d, 2)
		}
		if d <= max(2, len(cmd)/3) {
			cs = append(cs, cand{name, d})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].d < cs[j].d || (cs[i].d == cs[j].d && cs[i].name < cs[j].name) })
	if len(cs) == 0 {
		return ""
	}
	var names []string
	for i := 0; i < len(cs) && i < 3; i++ {
		names = append(names, cs[i].name)
	}
	return " Did you mean " + strings.Join(names, ", ") + "?"
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}
