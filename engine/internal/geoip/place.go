package geoip

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Place is what the IP databases say about an address. The country comes
// from the city file when it has one for the address, else the embedded
// country file; the region, city and network come from
// DB-IP's free City Lite and ASN Lite files (CC BY 4.0), which the server
// keeps on disk and refreshes monthly (Updater). Without those files the
// other fields are empty. The address itself is never stored.
type Place struct {
	Country string
	// Region is the state, province or other first-level division, by its
	// English name ("California", "England").
	Region string
	City   string
	// ASN is the network's autonomous system number, 0 when unknown; Org is
	// who runs it: an internet provider, a mobile carrier or a host.
	ASN uint32
	Org string
}

// The files in the directory Updater keeps them in.
const (
	CityFile = "dbip-city-lite.mmdb"
	ASNFile  = "dbip-asn-lite.mmdb"
)

// files are the open databases. Swapped whole when a new month arrives.
type files struct {
	city, asn *maxminddb.Reader
}

var current atomic.Pointer[files]

type cityRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Subdivisions []struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
}

type asnRecord struct {
	Number uint32 `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

// Lookup finds where addr is. An invalid, private or unknown address gives
// an empty Place.
func Lookup(addr netip.Addr) Place {
	if !addr.IsValid() {
		return Place{}
	}
	addr = addr.Unmap()
	p := Place{Country: Country(addr)}
	f := current.Load()
	if f == nil {
		return p
	}
	if f.city != nil {
		var r cityRecord
		if err := f.city.Lookup(addr).Decode(&r); err == nil {
			// The city file is the same vendor's and newer: its country
			// agrees with its city.
			if c := r.Country.ISOCode; len(c) == 2 {
				p.Country = c
			}
			p.City = clean(r.City.Names["en"])
			if len(r.Subdivisions) > 0 {
				p.Region = clean(r.Subdivisions[0].Names["en"])
			}
		}
	}
	if f.asn != nil {
		var r asnRecord
		if err := f.asn.Lookup(addr).Decode(&r); err == nil {
			p.ASN, p.Org = r.Number, clean(r.Org)
		}
	}
	return p
}

// clean trims a name from the files and keeps it to 200 bytes, whole
// characters only.
func clean(s string) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	for len(s) > 200 {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// Loaded reports which files are in use and the day each was built.
func Loaded() (city, asn time.Time) {
	f := current.Load()
	if f == nil {
		return
	}
	if f.city != nil {
		city = built(f.city)
	}
	if f.asn != nil {
		asn = built(f.asn)
	}
	return
}

func built(r *maxminddb.Reader) time.Time {
	return time.Unix(int64(r.Metadata.BuildEpoch), 0).UTC()
}

// Unload stops using the files, so lookups give the country only.
func Unload() { swap(nil) }

// Load opens the files in dir that exist and uses them from now on. A file
// that is missing leaves its fields empty; one that can't be read is an
// error, and the files already in use stay.
func Load(dir string) error {
	city, err := open(filepath.Join(dir, CityFile), "City")
	if err != nil {
		return err
	}
	asn, err := open(filepath.Join(dir, ASNFile), "ASN")
	if err != nil {
		if city != nil {
			city.Close()
		}
		return err
	}
	swap(&files{city: city, asn: asn})
	return nil
}

// open reads one file and checks it is the kind of database expected. A
// missing file is (nil, nil).
func open(path, kind string) (*maxminddb.Reader, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("geoip: %s: %w", filepath.Base(path), err)
	}
	if !strings.Contains(r.Metadata.DatabaseType, kind) {
		r.Close()
		return nil, fmt.Errorf("geoip: %s is a %q database, not %s", filepath.Base(path), r.Metadata.DatabaseType, kind)
	}
	return r, nil
}

// swap puts new files in use. The old ones are closed a minute later, so a
// lookup already reading them finishes first.
func swap(f *files) {
	old := current.Swap(f)
	if old == nil {
		return
	}
	var keep files
	if f != nil {
		keep = *f
	}
	time.AfterFunc(time.Minute, func() {
		if old.city != nil && old.city != keep.city {
			old.city.Close()
		}
		if old.asn != nil && old.asn != keep.asn {
			old.asn.Close()
		}
	})
}

// replace puts one new file in use beside the other one already in use.
func replace(kind string, r *maxminddb.Reader) {
	next := &files{}
	if f := current.Load(); f != nil {
		*next = *f
	}
	if kind == "City" {
		next.city = r
	} else {
		next.asn = r
	}
	swap(next)
}
