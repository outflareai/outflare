// Package fraud holds Outflare's first-party fraud signals
// (plans/ad-tracker.md, B7a; the design is in ideas/fraud-signals.md): the
// reasons a tracker click or conversion is suspect, and the checks that
// find them. It flags; it never blocks or changes anything on its own. A
// suspect click still goes where a person's would, but caps, rotation and
// AI weights leave it out, so they don't optimise toward bots.
//
// Every check uses what Outflare already keeps: the click's network (its
// ASN, from DB-IP), the keyed hash of its address (never the address), and
// its times. Paid lookups (IPQualityScore, MaxMind) and the partner checks
// come later, as other sources of the same reasons.
package fraud

import (
	"bufio"
	"embed"
	"fmt"
	"hash/fnv"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The reasons a click or conversion is suspect.
const (
	// DatacenterNetwork is a click from a cloud or hosting provider's
	// network, where people rarely browse from and bots run.
	DatacenterNetwork = "datacenter_network"
	// RepeatClick is a click from an address and browser (user agent) that
	// clicked the same link more than RepeatClickLimit times within
	// RepeatClickWindow. The browser is part of it because many people can
	// share an address (a mobile carrier's, an office's).
	RepeatClick = "repeat_click"
	// FastConversion is a conversion less than FastConversionWithin after
	// its click: faster than a person fills in an offer.
	FastConversion = "fast_conversion"
	// FilterList is a click whose address or browser (user agent) is on
	// one of the brand's own filter lists (B7b).
	FilterList = "filter_list"
	// Honeypot is a click whose lander page then followed a link no
	// person can see or reach: the lander script's hidden link (B7b).
	Honeypot = "honeypot"
	// IPReputation is a click whose address the brand's own reputation
	// provider (IPQualityScore) scored at or over the brand's threshold
	// (B7c).
	IPReputation = "ip_reputation"
)

// Reasons lists every reason, for documentation and validation.
var Reasons = []string{DatacenterNetwork, RepeatClick, FastConversion, FilterList, Honeypot, IPReputation}

// ClickFilters are a brand's filter lists: addresses and ranges, and
// pieces of a user agent, matched without regard to case.
type ClickFilters struct {
	Prefixes []netip.Prefix
	Agents   []string
}

// Empty says there is nothing to match.
func (f ClickFilters) Empty() bool { return len(f.Prefixes) == 0 && len(f.Agents) == 0 }

// Match says whether a click from addr with userAgent is on the lists. An
// invalid addr matches no address.
func (f ClickFilters) Match(addr netip.Addr, userAgent string) bool {
	if addr.IsValid() {
		addr = addr.Unmap()
		for _, p := range f.Prefixes {
			if p.Contains(addr) {
				return true
			}
		}
	}
	if len(f.Agents) > 0 {
		ua := strings.ToLower(userAgent)
		for _, a := range f.Agents {
			if strings.Contains(ua, a) {
				return true
			}
		}
	}
	return false
}

// ParseFilterPrefix reads an address (1.2.3.4) or a range (1.2.3.0/24) as
// a prefix.
func ParseFilterPrefix(v string) (netip.Prefix, bool) {
	v = strings.TrimSpace(v)
	if p, err := netip.ParsePrefix(v); err == nil {
		return p.Masked(), true
	}
	if a, err := netip.ParseAddr(v); err == nil {
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), true
	}
	return netip.Prefix{}, false
}

const (
	RepeatClickLimit     = 10
	RepeatClickWindow    = time.Hour
	FastConversionWithin = 10 * time.Second
	// repeatMax bounds the repeat counter's memory per server.
	repeatMax = 200_000
)

// Hosting networks.

//go:embed data/hosting-asns.txt
var hostingFile embed.FS

// HostingNetwork is a cloud or hosting provider's network.
type HostingNetwork struct {
	ASN    uint32
	Name   string
	Source string
}

var (
	hostingOnce sync.Once
	hostingMu   sync.RWMutex
	hosting     map[uint32]HostingNetwork
	hostingErr  error
)

// relays are networks real people browse through (Apple's iCloud Private
// Relay, Cloudflare WARP): never on the hosting list.
var relays = map[uint32]string{13335: "Cloudflare", 20940: "Akamai", 36183: "Akamai", 16625: "Akamai", 54113: "Fastly"}

// LoadHosting parses the hosting list: one network per line, as
// "ASN<TAB>name<TAB>source URL", with # comments and blank lines.
func LoadHosting(text string) (map[uint32]HostingNetwork, error) {
	out := map[uint32]HostingNetwork{}
	sc := bufio.NewScanner(strings.NewReader(text))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return nil, fmt.Errorf("line %d: want ASN, name and source, tab separated", n)
		}
		asn, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil || asn == 0 {
			return nil, fmt.Errorf("line %d: %q isn't an ASN", n, parts[0])
		}
		if name, ok := relays[uint32(asn)]; ok {
			return nil, fmt.Errorf("line %d: AS%d is %s, whose network carries people through privacy relays", n, asn, name)
		}
		if _, dup := out[uint32(asn)]; dup {
			return nil, fmt.Errorf("line %d: AS%d is listed twice", n, asn)
		}
		if strings.TrimSpace(parts[1]) == "" || !strings.HasPrefix(parts[2], "https://") {
			return nil, fmt.Errorf("line %d: want a name and an https source", n)
		}
		out[uint32(asn)] = HostingNetwork{ASN: uint32(asn), Name: strings.TrimSpace(parts[1]), Source: parts[2]}
	}
	return out, sc.Err()
}

func loadHosting() {
	b, err := hostingFile.ReadFile("data/hosting-asns.txt")
	if err == nil {
		hosting, err = LoadHosting(string(b))
	}
	hostingErr = err
}

// Hosting reports whether asn is a hosting provider's network on the list.
func Hosting(asn uint32) (HostingNetwork, bool) {
	hostingOnce.Do(loadHosting)
	hostingMu.RLock()
	defer hostingMu.RUnlock()
	h, ok := hosting[asn]
	return h, ok && asn != 0
}

// SetHostingForTests adds networks to the hosting list, such as
// geoiptest's AS64502, and returns the undo. Only tests call it.
func SetHostingForTests(nets ...HostingNetwork) (restore func()) {
	hostingOnce.Do(loadHosting)
	hostingMu.Lock()
	defer hostingMu.Unlock()
	old := hosting
	next := make(map[uint32]HostingNetwork, len(old)+len(nets))
	for k, v := range old {
		next[k] = v
	}
	for _, n := range nets {
		next[n.ASN] = n
	}
	hosting = next
	return func() {
		hostingMu.Lock()
		defer hostingMu.Unlock()
		hosting = old
	}
}

// HostingErr is the error reading the embedded list, if any.
func HostingErr() error {
	hostingOnce.Do(loadHosting)
	return hostingErr
}

// Repeat clicks.

// RepeatCounter counts clicks per link, address hash and browser within
// RepeatClickWindow, on one server: a repeat clicker whose clicks reach
// different servers can be counted on each apart.
type RepeatCounter struct {
	mu sync.Mutex
	m  map[uint64]*repeatCount
}

type repeatCount struct {
	start time.Time
	n     int
}

// Count records a click and returns how many clicks this link had from
// this address and browser within the window, this one included; 0
// without an address.
func (r *RepeatCounter) Count(link, ipHash, userAgent string, now time.Time) int {
	if ipHash == "" {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(link + "|" + ipHash + "|" + userAgent))
	key := h.Sum64()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[uint64]*repeatCount{}
	}
	if len(r.m) >= repeatMax {
		for k, c := range r.m {
			if now.Sub(c.start) > RepeatClickWindow {
				delete(r.m, k)
			}
		}
		if len(r.m) >= repeatMax {
			r.m = map[uint64]*repeatCount{}
		}
	}
	c := r.m[key]
	if c == nil || now.Sub(c.start) > RepeatClickWindow {
		c = &repeatCount{start: now}
		r.m[key] = c
	}
	c.n++
	return c.n
}

// ClickReasons are a click's reasons: its network and how often its
// address clicked the link lately.
func ClickReasons(asn uint32, repeats int) []string {
	var out []string
	if _, ok := Hosting(asn); ok {
		out = append(out, DatacenterNetwork)
	}
	if repeats > RepeatClickLimit {
		out = append(out, RepeatClick)
	}
	return out
}

// ConversionReasons are a conversion's reasons: its click's, and its own
// timing.
func ConversionReasons(clickReasons []string, clicked, converted time.Time) []string {
	out := append([]string{}, clickReasons...)
	if d := converted.Sub(clicked); d >= 0 && d < FastConversionWithin {
		out = append(out, FastConversion)
	}
	return out
}
