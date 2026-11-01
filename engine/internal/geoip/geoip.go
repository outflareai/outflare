// Package geoip finds the country of an IP address from DB-IP's free IP to
// Country Lite database (CC BY 4.0), shipped with the engine in data/. It
// needs no key and makes no calls: the lookup is in memory, and the address
// itself is never stored. Pages that show or use a result credit DB-IP with
// a link (Credit). `go run ./engine/cmd/geoipgen` rebuilds the file.
package geoip

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"sort"
	"sync"
)

// Credit is the attribution DB-IP's licence asks for, as a link.
const Credit = "IP geolocation by DB-IP"

// CreditURL is where Credit links to.
const CreditURL = "https://db-ip.com"

//go:embed data/dbip-country.bin.gz
var packed []byte

// Magic starts the unpacked file: the format's version.
const Magic = "OFGEO1"

// table is the unpacked database. Each family is a sorted list of range
// starts; a range runs to the next start, and its country is the index into
// countries (0 means no country).
type table struct {
	countries []string
	v4        []uint32
	v4c       []uint8
	v6        []uint64 // the first 64 bits: DB-IP's ranges don't split a /64
	v6c       []uint8
}

var (
	once    sync.Once
	loaded  *table
	errLoad error
)

// Version is the month of the DB-IP release the engine ships, such as
// "2026-10".
func Version() string {
	t, err := load()
	if err != nil {
		return ""
	}
	return t.countries[0]
}

// Country is the two-letter country code of addr, or "" when the database
// has none for it (private, reserved or unassigned addresses).
func Country(addr netip.Addr) string {
	t, err := load()
	if err != nil {
		return ""
	}
	return lookup(t, addr)
}

func lookup(t *table, addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}
	addr = addr.Unmap()
	if addr.Is4() {
		b := addr.As4()
		v := binary.BigEndian.Uint32(b[:])
		i := sort.Search(len(t.v4), func(i int) bool { return t.v4[i] > v }) - 1
		if i < 0 {
			return ""
		}
		return t.name(t.v4c[i])
	}
	b := addr.As16()
	v := binary.BigEndian.Uint64(b[:8])
	i := sort.Search(len(t.v6), func(i int) bool { return t.v6[i] > v }) - 1
	if i < 0 {
		return ""
	}
	return t.name(t.v6c[i])
}

func (t *table) name(c uint8) string {
	if c == 0 || int(c) >= len(t.countries) {
		return ""
	}
	return t.countries[c]
}

func load() (*table, error) {
	once.Do(func() { loaded, errLoad = unpack(packed) })
	return loaded, errLoad
}

// unpack reads a file Pack wrote.
func unpack(gz []byte) (*table, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	r := bytes.NewReader(raw)
	magic := make([]byte, len(Magic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != Magic {
		return nil, errors.New("geoip: not a country file")
	}
	t := &table{}
	var n uint16
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, err
	}
	// countries[0] holds the release month, since index 0 is "no country".
	for range n {
		var l uint8
		if err := binary.Read(r, binary.BigEndian, &l); err != nil {
			return nil, err
		}
		s := make([]byte, l)
		if _, err := io.ReadFull(r, s); err != nil {
			return nil, err
		}
		t.countries = append(t.countries, string(s))
	}
	var n4, n6 uint32
	if err := binary.Read(r, binary.BigEndian, &n4); err != nil {
		return nil, err
	}
	t.v4, t.v4c = make([]uint32, n4), make([]uint8, n4)
	if err := binary.Read(r, binary.BigEndian, t.v4); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, t.v4c); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.BigEndian, &n6); err != nil {
		return nil, err
	}
	t.v6, t.v6c = make([]uint64, n6), make([]uint8, n6)
	if err := binary.Read(r, binary.BigEndian, t.v6); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, t.v6c); err != nil {
		return nil, err
	}
	if len(t.countries) == 0 {
		return nil, errors.New("geoip: no countries")
	}
	return t, nil
}

// Range is one row of DB-IP's CSV: the first and last address and the
// country.
type Range struct {
	From, To netip.Addr
	Country  string
}

// Pack writes ranges (both families, sorted, not overlapping) as the
// embedded file, with version as the release month.
func Pack(version string, ranges []Range) ([]byte, error) {
	idx := map[string]uint8{}
	countries := []string{version}
	code := func(c string) (uint8, error) {
		if i, ok := idx[c]; ok {
			return i, nil
		}
		if len(countries) == 255 {
			return 0, errors.New("geoip: too many countries")
		}
		idx[c] = uint8(len(countries))
		countries = append(countries, c)
		return idx[c], nil
	}
	var v4 []uint32
	var v4c []uint8
	var v6 []uint64
	var v6c []uint8
	for _, rg := range ranges {
		c, err := code(rg.Country)
		if err != nil {
			return nil, err
		}
		if rg.From.Is4() {
			from, to := u32(rg.From), u32(rg.To)
			v4, v4c = addRange(v4, v4c, from, c, to == ^uint32(0), to+1)
		} else {
			// Kept to /64s: a /64 two ranges share goes to the first.
			from, to := u64(rg.From), u64(rg.To)
			if n := len(v6); n > 0 && from < v6[n-1] {
				if to < v6[n-1] {
					continue
				}
				from = v6[n-1]
			}
			v6, v6c = addRange(v6, v6c, from, c, to == ^uint64(0), to+1)
		}
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	w := func(v any) { _ = binary.Write(zw, binary.BigEndian, v) }
	_, _ = zw.Write([]byte(Magic))
	w(uint16(len(countries)))
	for _, c := range countries {
		w(uint8(len(c)))
		_, _ = zw.Write([]byte(c))
	}
	w(uint32(len(v4)))
	w(v4)
	_, _ = zw.Write(v4c)
	w(uint32(len(v6)))
	w(v6)
	_, _ = zw.Write(v6c)
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// addRange appends [from, next) as country c, joining it to the range
// before when that has the same country and ends where it starts, and
// marking the gap before it, if any, as no country.
func addRange[T uint32 | uint64](starts []T, cs []uint8, from T, c uint8, last bool, next T) ([]T, []uint8) {
	n := len(starts)
	switch {
	case n > 0 && starts[n-1] == from: // the end marker of the range before
		if n > 1 && cs[n-2] == c {
			starts, cs = starts[:n-1], cs[:n-1]
		} else {
			cs[n-1] = c
		}
	case n == 0 && from > 0:
		starts, cs = append(starts, 0, from), append(cs, 0, c)
	default:
		starts, cs = append(starts, from), append(cs, c)
	}
	if !last {
		starts, cs = append(starts, next), append(cs, 0)
	}
	return starts, cs
}

func u32(a netip.Addr) uint32 {
	b := a.As4()
	return binary.BigEndian.Uint32(b[:])
}

func u64(a netip.Addr) uint64 {
	b := a.As16()
	return binary.BigEndian.Uint64(b[:8])
}
