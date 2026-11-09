package geoip

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Source is where DB-IP publishes its free Lite databases, a new release on
// the first of each month. They need no account or key.
const Source = "https://download.db-ip.com/free/"

// maxFile is the most an unpacked file may be: the City Lite file is about
// 130 MB.
const maxFile = 512 << 20

// Updater keeps DB-IP's City Lite and ASN Lite files in Dir and in use.
type Updater struct {
	Dir string
	// Download fetches a new month's files; off, the files already in Dir
	// are used as they are (an install without internet access copies them
	// in by hand).
	Download bool
	URL      string       // Source when empty
	Client   *http.Client // a client with a timeout when nil
	Log      *slog.Logger
	Now      func() time.Time
}

// kinds are the two files: their database type, the file in Dir and the
// name DB-IP publishes them under.
var kinds = []struct{ kind, file, prefix string }{
	{"City", CityFile, "dbip-city-lite"},
	{"ASN", ASNFile, "dbip-asn-lite"},
}

// Run loads the files in Dir, then keeps them current, checking every
// interval. It returns when ctx ends.
func (u *Updater) Run(ctx context.Context, every time.Duration) {
	if err := os.MkdirAll(u.Dir, 0o755); err != nil {
		track.Fail(ctx, u.Log, track.Event{Name: "geoip.load_failed", ActorType: "system", Surface: "system"}, "internal", err)
		return
	}
	if err := Load(u.Dir); err != nil {
		track.Fail(ctx, u.Log, track.Event{Name: "geoip.load_failed", ActorType: "system", Surface: "system"}, "internal", err)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		u.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Refresh downloads a file that wasn't built this month, when downloading
// is on, and reports the age of each file in use.
func (u *Updater) Refresh(ctx context.Context) {
	now := u.now()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	city, asn := Loaded()
	for _, k := range kinds {
		have := city
		if k.kind == "ASN" {
			have = asn
		}
		if u.Download && have.Before(month) {
			got, err := u.fetch(ctx, k.kind, k.file, k.prefix, month)
			// At the start of a month the new release may not be out yet.
			// With no file at all, last month's will do until it is.
			if err == nil && !got && have.IsZero() {
				got, err = u.fetch(ctx, k.kind, k.file, k.prefix, month.AddDate(0, -1, 0))
			}
			switch {
			case err != nil:
				if ctx.Err() != nil {
					return
				}
				metrics.GeoDownloaded(k.kind, "error")
				track.Fail(ctx, u.Log, track.Event{Name: "geoip.download_failed", ActorType: "system", Surface: "system",
					Props: map[string]any{"file": k.kind}}, "unavailable", err)
			case got:
				metrics.GeoDownloaded(k.kind, "success")
				track.Emit(ctx, track.Event{Name: "geoip.downloaded", ActorType: "system", Surface: "system",
					Props: map[string]any{"file": k.kind}})
			default:
				metrics.GeoDownloaded(k.kind, "not_published")
			}
		}
	}
	city, asn = Loaded()
	metrics.GeoFileBuilt("City", city, now)
	metrics.GeoFileBuilt("ASN", asn, now)
}

// fetch downloads one month's file, checks it, and puts it in use. It
// reports false, with no error, when that month isn't published yet.
func (u *Updater) fetch(ctx context.Context, kind, file, prefix string, month time.Time) (bool, error) {
	base, client := u.URL, u.Client
	if base == "" {
		base = Source
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	url := fmt.Sprintf("%s%s-%s.mmdb.gz", base, prefix, month.Format("2006-01"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	res, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("geoip: %s: status %d", prefix, res.StatusCode)
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		return false, fmt.Errorf("geoip: %s: %w", prefix, err)
	}
	tmp, err := os.CreateTemp(u.Dir, file+".tmp-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(zr, maxFile+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, fmt.Errorf("geoip: %s: %w", prefix, err)
	}
	if n > maxFile {
		return false, errors.New("geoip: " + prefix + " is larger than expected")
	}
	// Check the new file before it replaces the one in use.
	r, err := open(tmp.Name(), kind)
	if err != nil {
		return false, err
	}
	r.Close()
	final := filepath.Join(u.Dir, file)
	if err := os.Rename(tmp.Name(), final); err != nil {
		return false, err
	}
	r, err = open(final, kind)
	if err != nil || r == nil {
		return false, fmt.Errorf("geoip: %s: reopening: %w", prefix, err)
	}
	replace(kind, r)
	return true, nil
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now().UTC()
	}
	return time.Now().UTC()
}
