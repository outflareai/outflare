// Command geoipgen rebuilds the engine's country database
// (engine/internal/geoip/data/dbip-country.bin.gz) from DB-IP's free IP to
// Country Lite CSVs. Download the month's IPv4 and IPv6 files (the
// ip-location-db mirror has them split: dbip-country/dbip-country-ipv4.csv
// and dbip-country-ipv6.csv), then run it from the repository root:
//
//	go run ./engine/cmd/geoipgen 2026-10 dbip-country-ipv4.csv dbip-country-ipv6.csv
package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/netip"
	"os"
	"regexp"

	"github.com/outflareai/outflare/engine/internal/geoip"
)

const out = "engine/internal/geoip/data/dbip-country.bin.gz"

var month = regexp.MustCompile(`^\d{4}-\d{2}$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "geoipgen:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 3 || !month.MatchString(args[0]) {
		return fmt.Errorf("usage: geoipgen <YYYY-MM> <ipv4.csv> <ipv6.csv>")
	}
	var ranges []geoip.Range
	for _, path := range args[1:] {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		r := csv.NewReader(f)
		r.FieldsPerRecord = 3
		for {
			row, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				f.Close()
				return fmt.Errorf("%s: %w", path, err)
			}
			from, err1 := netip.ParseAddr(row[0])
			to, err2 := netip.ParseAddr(row[1])
			if err1 != nil || err2 != nil || from.Is4() != to.Is4() || len(row[2]) != 2 {
				f.Close()
				return fmt.Errorf("%s: bad row %q", path, row)
			}
			ranges = append(ranges, geoip.Range{From: from, To: to, Country: row[2]})
		}
		f.Close()
	}
	b, err := geoip.Pack(args[0], ranges)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: %d ranges, %d bytes\n", out, len(ranges), len(b))
	return nil
}
