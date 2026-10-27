package inbox

import (
	"slices"
	"strings"
	"testing"
)

func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

func TestParseReply(t *testing.T) {
	p, err := Parse(crlf("From: \"Li Wei\" <Li@One-io.example>\nTo: ana@acme.test\nSubject: Re: Hello\nMessage-ID: <a@b>\nIn-Reply-To: <x@acme.test>\nReferences: <w@acme.test> <x@acme.test>\n" +
		"Content-Type: multipart/alternative; boundary=zz\n\n--zz\nContent-Type: text/html; charset=utf-8\n\n<p>Yes&nbsp;please</p><div>Li</div>\n--zz--\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.From != "li@one-io.example" || p.FromName != "Li Wei" || p.InReplyTo != "x@acme.test" || !slices.Equal(p.References, []string{"w@acme.test", "x@acme.test"}) || p.MessageID != "a@b" {
		t.Fatalf("headers: %+v", p)
	}
	if p.Body != "Yes please\nLi" || p.AutoReply || p.Bounce != nil {
		t.Fatalf("body: %q %+v", p.Body, p)
	}
}

func TestParseAutoReplyAndBounce(t *testing.T) {
	p, _ := Parse(crlf("From: x@y-io.example\nSubject: Automatic reply: Hello\n\nAway\n"))
	if !p.AutoReply {
		t.Error("subject auto reply")
	}
	p, _ = Parse(crlf("From: x@y-io.example\nSubject: Re: Hello\nAuto-Submitted: no\n\nHi\n"))
	if p.AutoReply {
		t.Error("Auto-Submitted: no is a person")
	}
	p, _ = Parse(crlf("From: postmaster@mx.y-io.example\nSubject: Delivery Status Notification (Failure)\n\n" +
		"550 5.1.1 The email account that you tried to reach does not exist.\nMessage-ID: <orig@acme.test>\n"))
	if p.Bounce == nil || !slices.Equal(p.Bounce.MessageIDs, []string{"orig@acme.test"}) {
		t.Fatalf("plain bounce: %+v", p.Bounce)
	}
	p, _ = Parse(crlf("From: postmaster@mx.y-io.example\nSubject: Delivery delayed\nContent-Type: multipart/report; report-type=delivery-status; boundary=b\n\n" +
		"--b\nContent-Type: message/delivery-status\n\nReporting-MTA: dns; mx\n\nFinal-Recipient: rfc822; a@y-io.example\nAction: delayed\nStatus: 4.4.1\n--b--\n"))
	if p.Bounce == nil || len(p.Bounce.Failed) != 0 {
		t.Fatalf("a delay is not a failure: %+v", p.Bounce)
	}
}

func TestOptOut(t *testing.T) {
	for body, want := range map[string]bool{
		"Please remove me from your list":                true,
		"UNSUBSCRIBE":                                    true,
		"Stop emailing me.":                              true,
		"Not interested right now, maybe next quarter.":  false,
		"Sure, send me times.\n\n> Reply to unsubscribe": false,
		"Sounds good\n\nOn Tue, Ana wrote:\nunsubscribe": false,
		"Can you remove me from the CC and add my boss?": false,
	} {
		if got := OptOut(body); got != want {
			t.Errorf("%q: %v", body, got)
		}
	}
}
