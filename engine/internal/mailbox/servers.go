package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// JobServerCheck resolves a sending server's IPs and checks them against
// IP blocklists.
const JobServerCheck = "server.check"

// ipBlocklists are the IP blocklists mailbox providers consult most. Like
// the domain lists, Spamhaus refuses queries from public resolvers and
// says so with a special answer, reported as unknown rather than clear.
var ipBlocklists = []struct {
	name, zone string
	classify   func(ip string) (status, detail string)
}{
	{"Spamhaus ZEN", "zen.spamhaus.org", func(ip string) (string, string) {
		switch {
		case ip == "127.0.0.2" || ip == "127.0.0.3" || ip == "127.0.0.9":
			return "listed", "on the Spamhaus Block List (" + ip + ")"
		case ip == "127.0.0.4" || ip == "127.0.0.5" || ip == "127.0.0.6" || ip == "127.0.0.7":
			return "listed", "on the Exploits Block List, usually a compromised machine (" + ip + ")"
		case ip == "127.0.0.10" || ip == "127.0.0.11":
			return "listed", "on the Policy Block List: the network owner says this address should not send mail directly (" + ip + ")"
		case strings.HasPrefix(ip, "127.255.255."):
			return "unknown", "Spamhaus refused the query; it does not answer public DNS resolvers"
		}
		return "unknown", "unexpected answer " + ip
	}},
	{"SpamCop", "bl.spamcop.net", func(ip string) (string, string) {
		if ip == "127.0.0.2" {
			return "listed", "listed (" + ip + ")"
		}
		return "unknown", "unexpected answer " + ip
	}},
	{"Barracuda", "b.barracudacentral.org", func(ip string) (string, string) {
		if ip == "127.0.0.2" {
			return "listed", "listed (" + ip + ")"
		}
		return "unknown", "unexpected answer " + ip
	}},
}

// IPListing is one IP's standing on one IP blocklist.
type IPListing struct {
	IP string `json:"ip"`
	Blocklist
}

// ResolveServerIPs returns a host's IPv4 and IPv6 addresses, sorted.
func ResolveServerIPs(ctx context.Context, res Resolver, host string) ([]string, error) {
	// A server added by its address needs no lookup.
	if ip, err := netip.ParseAddr(host); err == nil {
		return []string{ip.String()}, nil
	}
	hr, ok := res.(HostResolver)
	if !ok {
		return nil, errors.New("resolver cannot look up hosts")
	}
	addrs, err := hr.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if ip, err := netip.ParseAddr(a); err == nil {
			out = append(out, ip.String())
		}
	}
	sort.Strings(out)
	return out, nil
}

// CheckIPBlocklists checks each IPv4 address against the IP blocklists.
// IPv6 addresses are reported as unknown: the lists use another query
// format for them that is not supported yet.
func CheckIPBlocklists(ctx context.Context, res Resolver, ips []string) []IPListing {
	out := []IPListing{}
	hr, ok := res.(HostResolver)
	for _, raw := range ips {
		ip, err := netip.ParseAddr(raw)
		if err != nil {
			continue
		}
		for _, bl := range ipBlocklists {
			l := IPListing{IP: raw, Blocklist: Blocklist{Name: bl.name, Zone: bl.zone, Status: "unknown"}}
			switch {
			case !ip.Is4():
				l.Detail = "IPv6 addresses are not checked yet"
			case !ok:
			default:
				b := ip.As4()
				q := fmt.Sprintf("%d.%d.%d.%d.%s", b[3], b[2], b[1], b[0], bl.zone)
				answers, err := hr.LookupHost(ctx, q)
				var dnsErr *net.DNSError
				switch {
				case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
					l.Status = "clear"
				case err != nil:
					l.Detail = "lookup failed"
				case len(answers) > 0:
					l.Status, l.Detail = bl.classify(answers[0])
				}
			}
			out = append(out, l)
		}
	}
	return out
}

// ServerFacts is what ServerIssues needs to know about a sending server.
type ServerFacts struct {
	Host       string
	Checked    bool
	Paused     bool
	PausedBy   string
	IPs        []string
	Listings   []IPListing
	Mailboxes  int
	Failing    int
	Sent7d     int
	Bounces7d  int
	DailyLimit *int
	Capacity   int // sum of its active mailboxes' daily limits
}

// ServerIssues lists what is wrong with a sending server, most severe
// first, and how to fix each.
func ServerIssues(f ServerFacts) []Issue {
	var out []Issue
	add := func(i Issue) { out = append(out, i) }

	if f.Paused {
		d := "No sequence or test emails go out from this server's mailboxes. Replies you write in the inbox still send."
		if f.PausedBy == "blocklist" {
			d = "Paused automatically because one of its IPs is on a blocklist. Sending resumes on its own once a check finds every IP clear, or resume it yourself."
		}
		add(Issue{Code: "paused", Severity: "info", Title: "Sending is paused", Detail: d})
	}
	if f.Mailboxes == 0 {
		add(Issue{Code: "no_mailboxes", Severity: "info", Title: "No mailboxes use this server",
			Detail: "Mailboxes join a server when they are connected with " + f.Host + " as their SMTP host."})
	}
	if f.Failing > 0 {
		add(Issue{Code: "mailboxes_failing", Severity: "warning", Title: Plural(f.Failing, "mailbox can't", "mailboxes can't") + " connect",
			Detail: "They are out of rotation until their sign-in works. If every mailbox on the server fails, the server itself may be down: ask your provider."})
	}
	if !f.Checked {
		return sortIssues(out)
	}
	if len(f.IPs) == 0 {
		add(Issue{Code: "no_ips", Severity: "warning", Title: "No IP addresses found",
			Detail: f.Host + " has no A or AAAA record, so its IPs can't be checked. If mail leaves from other addresses than the sign-in host, set the sending IPs by hand."})
	}
	for _, l := range f.Listings {
		if l.Status == "listed" {
			add(Issue{Code: "ip_blocklisted", Severity: "critical", Title: l.IP + " is listed on " + l.Name,
				Detail: "Mail from a listed IP is junked or rejected by many providers. Stop sending from it, find the cause (bought lists, high bounces, a compromised account), then request removal at " + ipDelistURL(l.Zone) + "."})
		}
	}
	if f.Sent7d >= bounceMinSent {
		rate := float64(f.Bounces7d) / float64(f.Sent7d)
		pct := fmt.Sprintf("%.1f%%", rate*100)
		switch {
		case rate > bounceCritical:
			add(Issue{Code: "bounce_rate", Severity: "critical", Title: "Bounce rate is " + pct + " over 7 days",
				Detail: "Above 5% providers start blocking the server's IPs. Pause it and verify your lead lists before sending again."})
		case rate > bounceWarn:
			add(Issue{Code: "bounce_rate", Severity: "warning", Title: "Bounce rate is " + pct + " over 7 days",
				Detail: "Keep it under 2%: verify emails before importing leads, and remove role and catch-all addresses."})
		}
	}
	if f.DailyLimit == nil && f.Mailboxes >= 10 {
		add(Issue{Code: "no_server_limit", Severity: "info", Title: "No daily limit for the whole server",
			Detail: fmt.Sprintf("Its active mailboxes can send up to %d a day together. A server limit keeps the shared IPs within what they can carry while they warm up.", f.Capacity)})
	}
	return sortIssues(out)
}

func ipDelistURL(zone string) string {
	switch {
	case strings.Contains(zone, "spamhaus"):
		return "https://check.spamhaus.org"
	case strings.Contains(zone, "spamcop"):
		return "https://www.spamcop.net/bl.shtml"
	}
	return "https://www.barracudacentral.org/rbl/removal-request"
}

type serverPayload struct {
	ServerID string `json:"server_id"`
}

// EnsureServer returns the workspace's server for an SMTP host, adding it
// when new. added reports whether it was just added.
func EnsureServer(ctx context.Context, q jobs.Querier, workspaceID, host string) (id string, added bool, err error) {
	host = strings.ToLower(strings.TrimSpace(host))
	err = q.QueryRow(ctx, `
		INSERT INTO sending_servers (workspace_id, host) VALUES ($1, $2)
		ON CONFLICT (workspace_id, host) DO NOTHING
		RETURNING id`, workspaceID, host).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	err = q.QueryRow(ctx, `SELECT id FROM sending_servers WHERE workspace_id = $1 AND host = $2`, workspaceID, host).Scan(&id)
	return id, false, err
}

// QueueServerCheck schedules an IP and blocklist check for one server.
func (s *Service) QueueServerCheck(ctx context.Context, q jobs.Querier, workspaceID, serverID string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobServerCheck, WorkspaceID: &workspaceID, Payload: serverPayload{serverID}, MaxAttempts: 3})
	return err
}

func (s *Service) serverJob(ctx context.Context, job jobs.Job) error {
	var p serverPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || p.ServerID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	var host, source string
	var ips []string
	err := s.Pool.QueryRow(ctx, `SELECT host, ips_source, ips FROM sending_servers WHERE id = $1 AND workspace_id = $2`,
		p.ServerID, ws).Scan(&host, &source, &ips)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // removed since
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if source == "dns" {
		found, err := ResolveServerIPs(ctx, s.Resolver, host)
		var dnsErr *net.DNSError
		switch {
		case err == nil:
			ips = found
		case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
			ips = []string{}
		default:
			return err // a resolver hiccup: retry rather than record "no IPs"
		}
	}
	listings := CheckIPBlocklists(ctx, s.Resolver, ips)
	lists, err := json.Marshal(listings)
	if err != nil {
		return err
	}
	var listed []string
	for _, l := range listings {
		if l.Status == "listed" {
			listed = append(listed, l.IP+" on "+l.Name)
		}
	}

	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var status string
		var pausedBy *string
		var pauseOnListing bool
		err := tx.QueryRow(ctx, `SELECT status, paused_by, pause_on_listing FROM sending_servers WHERE id = $1 AND workspace_id = $2 FOR UPDATE`,
			p.ServerID, ws).Scan(&status, &pausedBy, &pauseOnListing)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE sending_servers SET ips = $3, blocklists = $4, checked_at = now(), updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, p.ServerID, ws, ips, lists); err != nil {
			return err
		}
		event := func(action, reason string) error {
			return audit.Record(ctx, tx, audit.Event{
				WorkspaceID: ws, ActorType: "system", Action: action, TargetType: "sending_server", TargetID: p.ServerID,
				Data: map[string]any{"host": host, "reason": reason},
			})
		}
		switch {
		case len(listed) > 0 && status == "active" && pauseOnListing:
			reason := "blocklisted: " + strings.Join(listed, ", ")
			if _, err := tx.Exec(ctx, `
				UPDATE sending_servers SET status = 'paused', paused_by = 'blocklist', status_reason = $2 WHERE id = $1`,
				p.ServerID, reason); err != nil {
				return err
			}
			if err := event("sending_server.auto_paused", reason); err != nil {
				return err
			}
		case len(listed) == 0 && status == "paused" && pausedBy != nil && *pausedBy == "blocklist":
			if _, err := tx.Exec(ctx, `
				UPDATE sending_servers SET status = 'active', paused_by = NULL, status_reason = NULL WHERE id = $1`, p.ServerID); err != nil {
				return err
			}
			if err := event("sending_server.auto_resumed", "every IP is clear"); err != nil {
				return err
			}
		}
		// One scheduled re-check per server per day.
		next := time.Now().Add(recheckEvery)
		_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{
			Kind: JobServerCheck, WorkspaceID: job.WorkspaceID, Payload: p, MaxAttempts: 3, RunAt: next,
			IdempotencyKey: ws + ":server:" + p.ServerID + ":" + next.UTC().Format("2006-01-02"),
		})
		return err
	})
}
