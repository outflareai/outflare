package browser

import (
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/accounts"
)

func TestKeepOnlyTheTasksCookies(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	in := []Cookie{
		{Name: "sid", Value: "a", Domain: "community.example.com"},
		{Name: "sso", Value: "b", Domain: ".example.com"},             // sent to community.example.com
		{Name: "sub", Value: "c", Domain: "eu.community.example.com"}, // under a task host
		{Name: "ads", Value: "d", Domain: ".tracker.example.net"},
		{Name: "old", Value: "e", Domain: "community.example.com", Expires: float64(now.Unix() - 1)},
		{Name: "", Value: "f", Domain: "community.example.com"},
		{Name: "evil", Value: "g", Domain: "community.example.com.evil.example"},
	}
	var names []string
	for _, c := range Keep(in, []string{"community.example.com"}, now) {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != "sid,sso,sub" {
		t.Fatalf("kept %s", got)
	}
}

func TestParseSession(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	raw, err := Session{SavedAt: now.Add(-time.Hour), Cookies: []Cookie{{Name: "sid", Value: "a", Domain: "example.com"}}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if s, err := ParseSession(raw, now); err != nil || len(s.Cookies) != 1 {
		t.Fatalf("fresh: %v %v", s, err)
	}
	if _, err := ParseSession(raw, now.Add(SessionMaxAge)); err != ErrSessionStale {
		t.Fatalf("old: %v", err)
	}
	if _, err := ParseSession("not json", now); err != ErrSessionStale {
		t.Fatalf("garbage: %v", err)
	}
	if _, err := (Session{SavedAt: now, Cookies: []Cookie{{Name: "big", Value: strings.Repeat("x", sessionMaxBytes)}}}).Encode(); err == nil {
		t.Fatal("an oversized session must be refused")
	}
}

func TestSessionAfter(t *testing.T) {
	for _, c := range []struct {
		status  string
		signIns []string
		want    string
	}{
		{StatusDone, []string{accounts.Authenticated}, SessionSave},
		{StatusDone, []string{accounts.MFARequired, accounts.Authenticated}, SessionSave},
		{StatusFailed, []string{accounts.Authenticated}, SessionLeave},
		{StatusWaitingPerson, []string{accounts.MFARequired}, SessionLeave},
		{StatusDone, nil, SessionLeave},
		{StatusDone, []string{accounts.WrongIdentity, accounts.Authenticated}, SessionClear},
		{StatusFailed, []string{accounts.Locked}, SessionClear},
		{StatusFailed, []string{accounts.SessionExpired}, SessionClear},
	} {
		if got := SessionAfter(Outcome{Status: c.status, SignIns: c.signIns}); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.status, c.signIns, got, c.want)
		}
	}
}
