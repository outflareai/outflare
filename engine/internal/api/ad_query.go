package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// A live read of any of an ad account's data, in the platform's own terms
// (Google Ads: a resource, its fields, conditions and orderings), and the
// list of what can be asked for. It reads only, so analytics.read is
// enough; nothing is stored.

func (s *Server) queryAdAccount(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := connectors.AdQuery{Resource: strings.TrimSpace(q.Get("resource")), Conditions: q["where"], Orderings: q["order_by"]}
	for _, f := range q["select"] {
		for _, one := range strings.Split(f, ",") {
			if one = strings.TrimSpace(one); one != "" {
				in.Fields = append(in.Fields, one)
			}
		}
	}
	for i := range in.Conditions {
		in.Conditions[i] = strings.TrimSpace(in.Conditions[i])
	}
	for i := range in.Orderings {
		in.Orderings[i] = strings.TrimSpace(in.Orderings[i])
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > connectors.AdQueryMaxRows {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is a number from 1 to "+strconv.Itoa(connectors.AdQueryMaxRows))
			return
		}
		in.Limit = n
	}
	if in.Resource == "" || len(in.Fields) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "resource and select are needed, such as resource=campaign and select=campaign.id,campaign.name")
		return
	}
	acct, o, m, ok := adAccountOpen[connectors.AdQuerier](s, w, r, "live reads")
	if !ok {
		return
	}
	res, err := m.AdQuery(r.Context(), o.Client, o.Creds, acct.acct, in)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": res.Query, "rows": res.Rows, "row_count": res.RowCount, "limit": res.Limit, "complete": res.Complete,
		"observed_at": time.Now().UTC(), "currency": acct.acct.Currency, "time_zone": acct.acct.TimeZone})
}

func (s *Server) listAdQueryFields(w http.ResponseWriter, r *http.Request) {
	resource := strings.TrimSpace(r.URL.Query().Get("resource"))
	acct, o, m, ok := adAccountOpen[connectors.AdQuerier](s, w, r, "live reads")
	if !ok {
		return
	}
	list, err := m.AdQueryFields(r.Context(), o.Client, o.Creds, acct.acct, resource)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	if list == nil {
		list = []connectors.AdQueryField{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}
