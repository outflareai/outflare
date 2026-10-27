package sysmail

import (
	"strings"
	"testing"
)

func TestEmailMessage(t *testing.T) {
	m := Email{
		To:      "ann@example.com",
		Subject: "Reset your Outflare password",
		Preview: "Choose a new password.",
		Heading: "Reset your password",
		Body:    []string{"Someone asked to reset the password for <ann@example.com>."},
		Button:  "Choose a new password",
		URL:     "https://app.outflare.ai/reset-password#ofr_abc",
		Note:    "The link works once, for an hour.",
	}.Message()
	if m.To != "ann@example.com" || m.Subject != "Reset your Outflare password" {
		t.Fatalf("header: %+v", m)
	}
	for _, want := range []string{"Reset your password", "Choose a new password:\nhttps://app.outflare.ai/reset-password#ofr_abc", "for an hour", "https://outflare.ai"} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("text has no %q:\n%s", want, m.Text)
		}
	}
	for _, want := range []string{`href="https://app.outflare.ai/reset-password#ofr_abc"`, "Reset your password", "&lt;ann@example.com&gt;", "Choose a new password.", `color-scheme`, `src="https://outflare.ai/brand/png/outflare-lockup-black-640.png"`} {
		if !strings.Contains(m.HTML, want) {
			t.Errorf("html has no %q", want)
		}
	}
	if strings.Contains(m.HTML, "<ann@") {
		t.Error("html doesn't escape the body")
	}
}
