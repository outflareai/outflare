package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/accounts"
)

// A saved session lets a task start signed in, the way a person's browser
// does, instead of signing in (and meeting MFA) on every run. It is the
// account's cookies after a run that signed in and confirmed the
// account's identity, sealed in the vault as the account's `session`
// secret. Only cookies a boundary host would send are kept, a session
// older than SessionMaxAge is dropped, and a run that finds the wrong
// identity, a locked account or a refused credential clears it. Cookies
// never reach the model, the task's log or its evidence.

// SecretSession is the vault kind a saved session lives under.
const SecretSession = "session"

// SessionMaxAge is how long a saved session is used before the task
// signs in again from scratch.
const SessionMaxAge = 30 * 24 * time.Hour

// sessionMaxBytes caps what one session may hold in the vault.
const sessionMaxBytes = 256 << 10

// Cookie is one cookie of a saved session.
type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path,omitempty"`
	// Expires is seconds since the epoch; 0 for a cookie that lasts the
	// browser session.
	Expires  float64 `json:"expires,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	HTTPOnly bool    `json:"http_only,omitempty"`
	SameSite string  `json:"same_site,omitempty"`
}

// Session is what the vault keeps for an account.
type Session struct {
	SavedAt time.Time `json:"saved_at"`
	Cookies []Cookie  `json:"cookies"`
}

// SessionKeeper is a Driver whose cookies can be read and set. The
// Chrome driver is one; a driver that isn't simply never keeps sessions.
type SessionKeeper interface {
	Cookies(ctx context.Context) ([]Cookie, error)
	SetCookies(ctx context.Context, cookies []Cookie) error
}

// Keep returns the cookies worth keeping for a task on hosts at now: not
// expired, and sent to at least one of the hosts (a cookie for
// .example.com is kept for a task on login.example.com; one for another
// site is dropped).
func Keep(cookies []Cookie, hosts []string, now time.Time) []Cookie {
	var out []Cookie
	for _, c := range cookies {
		if c.Name == "" || c.Domain == "" {
			continue
		}
		if c.Expires > 0 && c.Expires <= float64(now.Unix()) {
			continue
		}
		d := strings.TrimPrefix(NormalizeHost(c.Domain), ".")
		if d == "" || !(HostIn(d, hosts) || anyUnder(hosts, d)) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// anyUnder reports whether one of hosts is domain or under it.
func anyUnder(hosts []string, domain string) bool {
	for _, h := range hosts {
		if HostIn(h, []string{domain}) {
			return true
		}
	}
	return false
}

// ParseSession reads a saved session; one too old to use, or that can't
// be read, is ErrSessionStale.
func ParseSession(raw string, now time.Time) (Session, error) {
	var s Session
	if err := json.Unmarshal([]byte(raw), &s); err != nil || s.SavedAt.IsZero() {
		return Session{}, ErrSessionStale
	}
	if now.Sub(s.SavedAt) > SessionMaxAge {
		return Session{}, ErrSessionStale
	}
	return s, nil
}

// ErrSessionStale means a saved session is too old or unreadable; the
// task signs in from scratch.
var ErrSessionStale = errors.New("the saved session is too old to use")

// Encode is the session as the vault keeps it, or an error when it is
// over the size cap.
func (s Session) Encode() (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	if len(b) > sessionMaxBytes {
		return "", errors.New("the session is too large to keep")
	}
	return string(b), nil
}

// Session decisions after a run.
const (
	SessionSave  = "save"
	SessionClear = "clear"
	SessionLeave = ""
)

// SessionAfter says what a run's ending does to the account's saved
// session: a finished run that confirmed the account's identity saves
// it, a run that found the account unusable at any point (the wrong
// identity, locked, a refused credential, an expired session) clears it,
// and anything else leaves it as it was.
func SessionAfter(out Outcome) string {
	for _, o := range out.SignIns {
		switch o {
		case accounts.WrongIdentity, accounts.Locked, accounts.CredentialFailed, accounts.SessionExpired:
			return SessionClear
		}
	}
	if n := len(out.SignIns); n > 0 && out.SignIns[n-1] == accounts.Authenticated && out.Status == StatusDone {
		return SessionSave
	}
	return SessionLeave
}
