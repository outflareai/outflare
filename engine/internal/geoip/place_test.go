package geoip_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/geoip"
	"github.com/outflareai/outflare/engine/internal/geoip/geoiptest"
)

func TestLookupWithFiles(t *testing.T) {
	geoiptest.Use(t)
	got := geoip.Lookup(netip.MustParseAddr(geoiptest.SpringfieldIP))
	want := geoip.Place{Country: "US", Region: "Illinois", City: "Springfield", ASN: 64500, Org: "Northpeak Fiber"}
	if got != want {
		t.Fatalf("Springfield: got %+v, want %+v", got, want)
	}
	got = geoip.Lookup(netip.MustParseAddr(geoiptest.LyonIP))
	if got.Country != "FR" || got.City != "Lyon" || got.Region != "Auvergne-Rhone-Alpes" || got.ASN != 64501 || got.Org != "Acme Mobile" {
		t.Fatalf("Lyon over IPv6: got %+v", got)
	}
	// A data centre has a network and no city.
	got = geoip.Lookup(netip.MustParseAddr(geoiptest.HostIP))
	if got.City != "" || got.ASN != 64502 {
		t.Fatalf("host: got %+v", got)
	}
	// An IPv4 address written as IPv6 is the same address.
	if got := geoip.Lookup(netip.MustParseAddr("::ffff:" + geoiptest.SpringfieldIP)); got.City != "Springfield" {
		t.Fatalf("mapped address: got %+v", got)
	}
	if got := geoip.Lookup(netip.Addr{}); got != (geoip.Place{}) {
		t.Fatalf("invalid address: got %+v", got)
	}
}

func TestLookupWithoutFiles(t *testing.T) {
	geoip.Unload()
	// The embedded country file still answers; the rest stays empty.
	got := geoip.Lookup(netip.MustParseAddr("8.8.8.8"))
	if got.Country != "US" || got.City != "" || got.ASN != 0 {
		t.Fatalf("got %+v", got)
	}
	if city, asn := geoip.Loaded(); !city.IsZero() || !asn.IsZero() {
		t.Fatalf("Loaded with no files: %v %v", city, asn)
	}
}

func TestLoadRefusesTheWrongFile(t *testing.T) {
	geoiptest.Use(t)
	dir := t.TempDir()
	// An ASN database where the city file should be.
	if err := os.WriteFile(filepath.Join(dir, geoip.CityFile), geoiptest.ASN(t, time.Now()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := geoip.Load(dir); err == nil || !strings.Contains(err.Error(), "not City") {
		t.Fatalf("got %v", err)
	}
	// The files already in use stay.
	if got := geoip.Lookup(netip.MustParseAddr(geoiptest.SpringfieldIP)); got.City != "Springfield" {
		t.Fatalf("after a refused load: %+v", got)
	}
}

// dbip serves the files the way DB-IP does: one name per month, 404 for a
// month not published.
type dbip struct {
	mu       sync.Mutex
	files    map[string][]byte
	requests []string
}

func (d *dbip) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := strings.TrimPrefix(r.URL.Path, "/free/")
	d.requests = append(d.requests, name)
	b, ok := d.files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Write(b)
}

func TestUpdaterDownloads(t *testing.T) {
	geoip.Unload()
	t.Cleanup(geoip.Unload)
	october := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	november := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	d := &dbip{files: map[string][]byte{
		// November isn't out yet for the city file.
		"dbip-city-lite-2026-10.mmdb.gz": geoiptest.Gzip(t, geoiptest.City(t, october)),
		"dbip-asn-lite-2026-11.mmdb.gz":  geoiptest.Gzip(t, geoiptest.ASN(t, november)),
	}}
	srv := httptest.NewServer(d)
	defer srv.Close()
	dir := t.TempDir()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	u := &geoip.Updater{Dir: dir, Download: true, URL: srv.URL + "/free/", Now: func() time.Time { return now }}
	u.Refresh(context.Background())

	// With no city file at all, last month's is used until this month's
	// is out.
	got := geoip.Lookup(netip.MustParseAddr(geoiptest.SpringfieldIP))
	if got.City != "Springfield" || got.ASN != 64500 {
		t.Fatalf("after the first refresh: %+v", got)
	}
	city, asn := geoip.Loaded()
	if !city.Equal(october) || !asn.Equal(november) {
		t.Fatalf("built: city %v, asn %v", city, asn)
	}
	for _, f := range []string{geoip.CityFile, geoip.ASNFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s not kept: %v", f, err)
		}
	}

	// Later, the city file is out of date and November's appears; the ASN
	// file is current and isn't asked for again.
	d.mu.Lock()
	d.files["dbip-city-lite-2026-11.mmdb.gz"] = geoiptest.Gzip(t, geoiptest.City(t, november))
	d.requests = nil
	d.mu.Unlock()
	now = now.Add(6 * time.Hour)
	u.Refresh(context.Background())
	if city, _ := geoip.Loaded(); !city.Equal(november) {
		t.Fatalf("city not refreshed: %v", city)
	}
	if strings.Join(d.requests, ",") != "dbip-city-lite-2026-11.mmdb.gz" {
		t.Fatalf("requests: %v", d.requests)
	}

	// A new server finds the files on disk.
	geoip.Unload()
	if err := geoip.Load(dir); err != nil {
		t.Fatal(err)
	}
	if got := geoip.Lookup(netip.MustParseAddr(geoiptest.LyonIP)); got.City != "Lyon" {
		t.Fatalf("from disk: %+v", got)
	}
}

func TestUpdaterKeepsTheFileInUseWhenADownloadIsBad(t *testing.T) {
	geoip.Unload()
	t.Cleanup(geoip.Unload)
	now := time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC)
	november := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, geoip.CityFile), geoiptest.City(t, november), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := geoip.Load(dir); err != nil {
		t.Fatal(err)
	}
	d := &dbip{files: map[string][]byte{
		// December's city file is an ASN database: refused.
		"dbip-city-lite-2026-12.mmdb.gz": geoiptest.Gzip(t, geoiptest.ASN(t, now)),
		// December's ASN file is not gzip.
		"dbip-asn-lite-2026-12.mmdb.gz": []byte("not a database"),
	}}
	srv := httptest.NewServer(d)
	defer srv.Close()
	u := &geoip.Updater{Dir: dir, Download: true, URL: srv.URL + "/free/", Now: func() time.Time { return now }}
	u.Refresh(context.Background())
	if city, asn := geoip.Loaded(); !city.Equal(november) || !asn.IsZero() {
		t.Fatalf("built: city %v, asn %v", city, asn)
	}
	if got := geoip.Lookup(netip.MustParseAddr(geoiptest.SpringfieldIP)); got.City != "Springfield" {
		t.Fatalf("lookup after a bad download: %+v", got)
	}
	// Only the kept files are left in the directory.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("directory: %v", entries)
	}
}

func TestUpdaterWithDownloadsOff(t *testing.T) {
	geoip.Unload()
	t.Cleanup(geoip.Unload)
	d := &dbip{files: map[string][]byte{}}
	srv := httptest.NewServer(d)
	defer srv.Close()
	u := &geoip.Updater{Dir: t.TempDir(), URL: srv.URL + "/free/"}
	u.Refresh(context.Background())
	if len(d.requests) != 0 {
		t.Fatalf("downloaded with downloads off: %v", d.requests)
	}
}
