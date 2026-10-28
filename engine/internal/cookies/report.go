package cookies

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Report is what one browser saw on a page of the site: the names of its
// cookies and storage keys and the addresses of its scripts and embeds,
// never a value. Granted is the visitor's choice by category when the
// consent banner is on, so what was there before they allowed it can be
// told; nil when the site has no banner.
type Report struct {
	Cookies []string
	Local   []string
	Session []string
	// Scripts and iframes, as ScriptName gives them.
	Scripts []string
	Iframes []string
	// The page's host and path.
	Host    string
	Path    string
	Granted map[string]bool
}

// Reporter keeps what browsers report. Many browsers report the same
// names, so each one is written at most once an hour per site; new names
// are written at once and change the policy.
type Reporter struct {
	Pool *pgxpool.Pool
	mu   sync.Mutex
	seen map[string]time.Time
}

// Every name is written again after this long, so last_seen_at moves.
const reportEvery = time.Hour

// maxRemembered bounds the memory a Reporter uses.
const maxRemembered = 200_000

// Take records a browser's report for a site key, and reports how many
// names were new to the site and whether its policy changed.
func (r *Reporter) Take(ctx context.Context, ws, keyID string, rep Report, now time.Time) (added int, pub Published, err error) {
	own := registrable(rep.Host)
	path := cleanPath(rep.Path)
	var seen []Seen
	add := func(kind string, names []string) {
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n == "" || len(n) > 300 {
				continue
			}
			if kind == KindScript || kind == KindIframe {
				host, _, _ := strings.Cut(n, "/")
				if registrable(host) == own && Lookup(kind, n).Vendor == "" {
					continue
				}
			}
			s := Seen{Kind: kind, Name: n, Source: "browser", Page: path}
			if kind == KindScript || kind == KindIframe {
				s.Host, _, _ = strings.Cut(n, "/")
			}
			if cat := Lookup(kind, n).Category; rep.Granted != nil && cat != Necessary && cat != Unknown && !rep.Granted[cat] {
				s.BeforeConsent = true
			}
			seen = append(seen, s)
		}
	}
	add(KindCookie, rep.Cookies)
	add(KindLocalStorage, rep.Local)
	add(KindSessionStorage, rep.Session)
	add(KindScript, rep.Scripts)
	add(KindIframe, rep.Iframes)
	fresh := r.fresh(keyID, seen, now)
	if len(fresh) == 0 {
		return 0, pub, nil
	}
	err = pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		var err error
		if added, err = Observe(ctx, tx, ws, keyID, fresh); err != nil || added == 0 {
			return err
		}
		pub, err = Publish(ctx, tx, ws, keyID, Who{ActorType: "system"}, now)
		return err
	})
	if err != nil {
		r.forget(keyID, fresh)
	}
	return added, pub, err
}

// fresh keeps what wasn't written for this site in the last hour, and
// remembers it as written.
func (r *Reporter) fresh(keyID string, seen []Seen, now time.Time) []Seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil || len(r.seen) > maxRemembered {
		r.seen = map[string]time.Time{}
	}
	var out []Seen
	for _, s := range seen {
		k := memo(keyID, s)
		if at, ok := r.seen[k]; ok && now.Sub(at) < reportEvery {
			continue
		}
		r.seen[k] = now
		out = append(out, s)
	}
	return out
}

func (r *Reporter) forget(keyID string, seen []Seen) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range seen {
		delete(r.seen, memo(keyID, s))
	}
}

func memo(keyID string, s Seen) string {
	before := "0"
	if s.BeforeConsent {
		before = "1"
	}
	return keyID + "\x00" + s.Kind + "\x00" + s.Name + "\x00" + before
}
