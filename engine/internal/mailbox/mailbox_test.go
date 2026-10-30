package mailbox_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/mailbox/mailtest"
)

func TestParseCSVResellerExport(t *testing.T) {
	in := "\ufeffEmail Address,First Name,Last Name,App Password,Daily Limit,Tags\n" +
		"ana@acme-io.example,Ana,Lee,pw-1,40,\"warm, us\"\n" +
		"\n" +
		"not-an-email,Bob,,pw,,\n" +
		"cy@acme-io.example,Cy,,,,\n" +
		"dee@acme-io.example,Dee,,pw,900,\n"
	rows, bad, err := mailbox.ParseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if r.Email != "ana@acme-io.example" || r.DisplayName != "Ana Lee" || r.SMTPPassword != "pw-1" || r.IMAPPassword != "pw-1" ||
		r.SMTPUsername != "ana@acme-io.example" || r.IMAPUsername != "ana@acme-io.example" || r.DailyLimit != 40 || r.Pool != "warm" || r.Line != 2 {
		t.Fatalf("row = %+v", r)
	}
	if len(bad) != 3 || bad[0].Line != 4 || bad[1].Line != 5 || bad[2].Line != 6 {
		t.Fatalf("bad = %+v", bad)
	}
	if !strings.Contains(bad[1].Error, "missing password") || !strings.Contains(bad[2].Error, "daily_limit") {
		t.Fatalf("bad = %+v", bad)
	}
}

func TestParseCSVSequencerExport(t *testing.T) {
	in := "email,from_name,smtp_host,smtp_port,smtp_username,smtp_password,imap_host,imap_port,imap_username,imap_password,provider\n" +
		"x@y-com.example,X,mail.y-com.example,587,xuser,s1,mail.y-com.example,993,ximap,i1,custom\n"
	rows, bad, err := mailbox.ParseCSV(strings.NewReader(in))
	if err != nil || len(bad) != 0 || len(rows) != 1 {
		t.Fatalf("rows=%v bad=%v err=%v", rows, bad, err)
	}
	r := rows[0]
	if r.Provider != "other" || r.SMTPHost != "mail.y-com.example" || r.SMTPPort != 587 || r.SMTPUsername != "xuser" ||
		r.IMAPUsername != "ximap" || r.IMAPPassword != "i1" || r.IMAPPort != 993 || r.DisplayName != "X" {
		t.Fatalf("row = %+v", r)
	}
}

func TestParseCSVErrors(t *testing.T) {
	if _, _, err := mailbox.ParseCSV(strings.NewReader("")); err == nil {
		t.Fatal("empty file accepted")
	}
	if _, _, err := mailbox.ParseCSV(strings.NewReader("name,password\nA,b\n")); err == nil {
		t.Fatal("file without email column accepted")
	}
	if _, _, err := mailbox.ParseCSV(strings.NewReader("email,password,provider\na@b-co.example,p,yahoo\n")); err != nil {
		t.Fatal(err)
	}
}

type fakeDNS struct {
	mx   map[string][]*net.MX
	txt  map[string][]string
	host map[string][]string
}

func (f fakeDNS) LookupHost(_ context.Context, name string) ([]string, error) {
	if v, ok := f.host[name]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if v, ok := f.mx[name]; ok {
		return v, nil
	}
	return nil, errors.New("no such host")
}

func (f fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := f.txt[name]; ok {
		return v, nil
	}
	return nil, errors.New("no such host")
}

func TestComplete(t *testing.T) {
	dns := fakeDNS{mx: map[string][]*net.MX{
		"g-io.example": {{Host: "aspmx.l.google.com.", Pref: 1}},
		"m-io.example": {{Host: "m-io.mail.protection.outlook.com.", Pref: 0}},
		"o-io.example": {{Host: "mx.o-io.example.", Pref: 10}},
	}}
	ctx := context.Background()

	g := mailbox.Row{Email: "a@g-io.example"}
	if err := mailbox.Complete(ctx, dns, &g); err != nil || g.Provider != "google" || g.SMTPHost != "smtp.gmail.com" || g.SMTPPort != 465 || g.IMAPPort != 993 {
		t.Fatalf("google: %+v %v", g, err)
	}
	m := mailbox.Row{Email: "a@m-io.example"}
	if err := mailbox.Complete(ctx, dns, &m); err != nil || m.Provider != "microsoft" || m.SMTPHost != "smtp.office365.com" || m.SMTPPort != 587 {
		t.Fatalf("microsoft: %+v %v", m, err)
	}
	o := mailbox.Row{Email: "a@o-io.example"}
	if err := mailbox.Complete(ctx, dns, &o); err == nil {
		t.Fatalf("other provider without hosts accepted: %+v", o)
	}
	given := mailbox.Row{Email: "a@o-io.example", SMTPHost: "s", SMTPPort: 25, IMAPHost: "i", IMAPPort: 143}
	if err := mailbox.Complete(ctx, dns, &given); err != nil || given.Provider != "other" {
		t.Fatalf("explicit hosts: %+v %v", given, err)
	}
	// An explicit provider skips detection.
	forced := mailbox.Row{Email: "a@o-io.example", Provider: "google"}
	if err := mailbox.Complete(ctx, dns, &forced); err != nil || forced.SMTPHost != "smtp.gmail.com" {
		t.Fatalf("forced: %+v %v", forced, err)
	}
}

func TestCheckDomain(t *testing.T) {
	dns := fakeDNS{
		mx: map[string][]*net.MX{"good-io.example": {{Host: "aspmx.l.google.com."}}},
		txt: map[string][]string{
			"good-io.example":                   {"google-site-verification=x", "v=spf1 include:_spf.google.com ~all"},
			"google._domainkey.good-io.example": {"v=DKIM1; k=rsa; p=MIIBIjAN"},
			"_dmarc.good-io.example":            {"v=DMARC1; p=quarantine; rua=mailto:d@good-io.example"},

			"weak.io":               {"v=spf1 -all", "v=spf1 include:x ~all"},
			"k1._domainkey.weak.io": {"v=DKIM1; p="},
			"s1._domainkey.weak.io": {"v=DKIM1; k=rsa; ", "p=MIGf"},
			"_dmarc.weak.io":        {"v=DMARC1; p=none"},
		},
	}
	ctx := context.Background()
	g := mailbox.CheckDomain(ctx, dns, "good-io.example")
	if g.MXProvider != "google" || g.SPF != "pass" || g.DKIM != "pass" || g.DKIMSelector != "google" || g.DMARC != "pass" {
		t.Fatalf("good: %+v", g)
	}
	w := mailbox.CheckDomain(ctx, dns, "weak.io")
	if w.MXProvider != "none" || w.SPF != "invalid" || w.DKIM != "pass" || w.DKIMSelector != "s1" || w.DMARC != "none_policy" {
		t.Fatalf("weak: %+v", w)
	}
	n := mailbox.CheckDomain(ctx, dns, "none.io")
	if n.SPF != "missing" || n.DKIM != "missing" || n.DMARC != "missing" {
		t.Fatalf("none: %+v", n)
	}
}

func TestConnectionChecksAndSend(t *testing.T) {
	srv := mailtest.Start(t, map[string]string{"ana@acme-io.example": "right"})
	d := &mailbox.Dialer{TLSConfig: srv.ClientTLS, Timeout: 5 * time.Second}
	ctx := context.Background()
	smtpEP := mailbox.EndpointFor(srv.Host, srv.SMTPPort)
	imapEP := mailbox.EndpointFor(srv.Host, srv.IMAPPort)
	good := mailbox.Credentials{Username: "ana@acme-io.example", Password: "right"}
	bad := mailbox.Credentials{Username: "ana@acme-io.example", Password: "wrong"}

	if err := d.CheckSMTP(ctx, smtpEP, good); err != nil {
		t.Fatalf("smtp: %v", err)
	}
	if err := d.CheckIMAP(ctx, imapEP, good); err != nil {
		t.Fatalf("imap: %v", err)
	}

	var ce *mailbox.ConnError
	if err := d.CheckSMTP(ctx, smtpEP, bad); !errors.As(err, &ce) || ce.Stage != mailbox.StageAuth {
		t.Fatalf("smtp bad password: %v", err)
	}
	if err := d.CheckIMAP(ctx, imapEP, bad); !errors.As(err, &ce) || ce.Stage != mailbox.StageAuth {
		t.Fatalf("imap bad password: %v", err)
	}
	if !strings.Contains(ce.Error(), "app password") {
		t.Fatalf("auth error lacks hint: %v", ce)
	}

	// An untrusted certificate fails at TLS, before any credentials are sent.
	strict := &mailbox.Dialer{Timeout: 5 * time.Second}
	if err := strict.CheckSMTP(ctx, smtpEP, good); !errors.As(err, &ce) || ce.Stage != mailbox.StageTLS {
		t.Fatalf("untrusted cert: %v", err)
	}

	// Nothing listening.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := mailbox.EndpointFor("127.0.0.1", l.Addr().(*net.TCPAddr).Port)
	l.Close()
	if err := d.CheckSMTP(ctx, closed, good); !errors.As(err, &ce) || ce.Stage != mailbox.StageConnect {
		t.Fatalf("closed port: %v", err)
	}

	msg := "From: ana@acme-io.example\r\nTo: bo@x-io.example\r\nSubject: hi\r\n\r\nhello\r\n"
	if _, err := d.Send(ctx, smtpEP, good, "ana@acme-io.example", []string{"bo@x-io.example"}, strings.NewReader(msg)); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := srv.Messages()
	if len(got) != 1 || got[0].From != "ana@acme-io.example" || got[0].To[0] != "bo@x-io.example" || !strings.Contains(string(got[0].Data), "hello") {
		t.Fatalf("messages = %+v", got)
	}
}

func TestServerIPsAndBlocklists(t *testing.T) {
	dns := fakeDNS{host: map[string][]string{
		"smtp.reef.io":                  {"198.51.100.7", "2001:db8::7", "198.51.100.6"},
		"7.100.51.198.zen.spamhaus.org": {"127.0.0.4"},
		"7.100.51.198.bl.spamcop.net":   {"127.0.0.2"},
		"6.100.51.198.zen.spamhaus.org": {"127.255.255.254"},
	}}
	ctx := context.Background()
	ips, err := mailbox.ResolveServerIPs(ctx, dns, "smtp.reef.io")
	if err != nil || fmt.Sprint(ips) != "[198.51.100.6 198.51.100.7 2001:db8::7]" {
		t.Fatalf("ips: %v %v", ips, err)
	}
	if ips, _ := mailbox.ResolveServerIPs(ctx, dns, "203.0.113.4"); fmt.Sprint(ips) != "[203.0.113.4]" {
		t.Fatalf("literal: %v", ips)
	}
	got := map[string]string{}
	for _, l := range mailbox.CheckIPBlocklists(ctx, dns, ips) {
		got[l.IP+" "+l.Name] = l.Status
	}
	want := map[string]string{
		"198.51.100.7 Spamhaus ZEN": "listed", "198.51.100.7 SpamCop": "listed", "198.51.100.7 Barracuda": "clear",
		"198.51.100.6 Spamhaus ZEN": "unknown", "198.51.100.6 SpamCop": "clear",
		"2001:db8::7 Spamhaus ZEN": "unknown",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %s, want %s", k, got[k], v)
		}
	}

	limit := 200
	issues := mailbox.ServerIssues(mailbox.ServerFacts{Host: "smtp.reef.io", Checked: true, IPs: ips,
		Listings: mailbox.CheckIPBlocklists(ctx, dns, ips), Mailboxes: 12, Sent7d: 100, Bounces7d: 3, DailyLimit: &limit})
	var codes []string
	for _, i := range issues {
		codes = append(codes, i.Code)
	}
	if fmt.Sprint(codes) != "[ip_blocklisted ip_blocklisted bounce_rate]" {
		t.Fatalf("issues: %v", codes)
	}
	issues = mailbox.ServerIssues(mailbox.ServerFacts{Host: "smtp.reef.io", Checked: true, Mailboxes: 12, Capacity: 360})
	if len(issues) != 2 || issues[0].Code != "no_ips" || issues[1].Code != "no_server_limit" || !strings.Contains(issues[1].Detail, "360") {
		t.Fatalf("no ips: %+v", issues)
	}
}
