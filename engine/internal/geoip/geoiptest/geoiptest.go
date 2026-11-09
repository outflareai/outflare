// Package geoiptest writes small City and ASN databases in DB-IP's format
// for tests, with documentation addresses only (RFC 5737 and 3849).
package geoiptest

import (
	"bytes"
	"compress/gzip"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"

	"github.com/outflareai/outflare/engine/internal/geoip"
)

// The addresses the files know.
const (
	// Springfield, Illinois, US, on Northpeak Fiber (AS64500).
	SpringfieldIP = "203.0.113.7"
	// Lyon, Auvergne-Rhone-Alpes, FR, on Acme Mobile (AS64501).
	LyonIP = "2001:db8::1"
	// A host's data centre (AS64502), with no country or city.
	HostIP = "198.51.100.20"
)

// City writes a City Lite database, built at built.
func City(t testing.TB, built time.Time) []byte {
	t.Helper()
	w := newWriter(t, "DBIP-City-Lite", built)
	place := func(country, city, region string) mmdbtype.Map {
		return mmdbtype.Map{
			"country":      mmdbtype.Map{"iso_code": mmdbtype.String(country)},
			"city":         mmdbtype.Map{"names": mmdbtype.Map{"en": mmdbtype.String(city)}},
			"subdivisions": mmdbtype.Slice{mmdbtype.Map{"names": mmdbtype.Map{"en": mmdbtype.String(region)}}},
		}
	}
	insert(t, w, "203.0.113.0/24", place("US", "Springfield", "Illinois"))
	insert(t, w, "2001:db8::/32", place("FR", "Lyon", "Auvergne-Rhone-Alpes"))
	return write(t, w)
}

// ASN writes an ASN Lite database, built at built.
func ASN(t testing.TB, built time.Time) []byte {
	t.Helper()
	w := newWriter(t, "DBIP-ASN-Lite (compat=GeoLite2-ASN)", built)
	asn := func(n uint32, org string) mmdbtype.Map {
		return mmdbtype.Map{"autonomous_system_number": mmdbtype.Uint32(n), "autonomous_system_organization": mmdbtype.String(org)}
	}
	insert(t, w, "203.0.113.0/24", asn(64500, "Northpeak Fiber"))
	insert(t, w, "2001:db8::/32", asn(64501, "Acme Mobile"))
	insert(t, w, "198.51.100.0/24", asn(64502, "Acme Cloud Hosting"))
	return write(t, w)
}

// Gzip packs a file the way DB-IP publishes it.
func Gzip(t testing.TB, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// Use writes both files to a new directory and puts them in use until the
// test ends.
func Use(t testing.TB) {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	if err := os.WriteFile(filepath.Join(dir, geoip.CityFile), City(t, now), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, geoip.ASNFile), ASN(t, now), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := geoip.Load(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(geoip.Unload)
}

func newWriter(t testing.TB, kind string, built time.Time) *mmdbwriter.Tree {
	w, err := mmdbwriter.New(mmdbwriter.Options{DatabaseType: kind, IncludeReservedNetworks: true, BuildEpoch: built.Unix(), RecordSize: 24})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func insert(t testing.TB, w *mmdbwriter.Tree, cidr string, v mmdbtype.DataType) {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Insert(n, v); err != nil {
		t.Fatal(err)
	}
}

func write(t testing.TB, w *mmdbwriter.Tree) []byte {
	t.Helper()
	var b bytes.Buffer
	if _, err := w.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
