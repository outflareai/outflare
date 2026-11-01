# Country data

| File | Source | Licence | Refresh |
| --- | --- | --- | --- |
| `dbip-country.bin.gz` | [DB-IP](https://db-ip.com) IP to Country Lite, through the [ip-location-db](https://github.com/sapics/ip-location-db) mirror (`dbip-country/`) | CC BY 4.0 (`DBIP-LICENSE.txt`; keep it next to the data) | Monthly: download `dbip-country-ipv4.csv` and `dbip-country-ipv6.csv`, then `go run ./engine/cmd/geoipgen <YYYY-MM> <ipv4.csv> <ipv6.csv>` |

The file is DB-IP's ranges packed small (see `Pack` in `../geoip.go`), with
the release month inside it. The licence asks for a credit: every page that
shows or uses a country from it links to db-ip.com (`geoip.Credit`).
