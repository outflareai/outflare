package api

import "errors"

// icpKeys are the filters an ideal customer profile may hold, in the order
// they are shown.
var icpKeys = []string{"titles", "seniorities", "industries", "employee_ranges", "locations", "keywords"}

// maxShortField is the longest a one-line text field may be.
const maxShortField = 4000

// normalICP returns every ICP key, empty lists included, so clients never
// have to check for missing keys.
func normalICP(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(icpKeys))
	for _, k := range icpKeys {
		out[k] = in[k]
		if out[k] == nil {
			out[k] = []string{}
		}
	}
	return out
}

// errStaleBrief: the context changed after the caller read it.
var errStaleBrief = errors.New("stale brief")
