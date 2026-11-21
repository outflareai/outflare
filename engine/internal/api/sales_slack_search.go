package api

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	slackSearchAsk  = 50  // hits asked of Slack, so dropped private ones leave enough
	slackSearchKeep = 20  // hits shown
	slackSearchText = 500 // characters of a hit's text
)

var (
	slackEmailTerm  = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
	slackDomainTerm = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
)

type slackSearchHit struct {
	ChannelName string    `json:"channel_name"`
	Author      string    `json:"author"`
	At          time.Time `json:"at"`
	Text        string    `json:"text"`
	Link        string    `json:"link"`
}

type slackSearchOut struct {
	Available bool             `json:"available"`
	Query     string           `json:"query"`
	Data      []slackSearchHit `json:"data"`
}

// slackSearchQuery is what a conversation is searched for: the person's
// email, the company's domain (never a free-mail one) and the company's name
// in quotes, joined with OR. Empty terms are left out.
func slackSearchQuery(email, company, website string) string {
	var terms []string
	if slackEmailTerm.MatchString(email) { // a term that fails its check is left out
		terms = append(terms, email)
	}
	domain := ""
	if website != "" {
		w := website
		if !strings.Contains(w, "://") {
			w = "https://" + w
		}
		if u, err := url.Parse(w); err == nil {
			domain = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		}
	}
	if domain == "" {
		if _, d, ok := strings.Cut(email, "@"); ok && slackEmailTerm.MatchString(email) {
			domain = strings.ToLower(d)
		}
	}
	if slackDomainTerm.MatchString(domain) && !outcomes.IsFreeMail(domain) {
		terms = append(terms, domain)
	}
	name := strings.NewReplacer(`"`, "", "\n", " ", "\r", " ", "\t", " ", `\`, " ").Replace(company)
	name = strings.Join(strings.Fields(name), " ")
	if rs := []rune(name); len(rs) > 100 {
		name = strings.TrimSpace(string(rs[:100]))
	}
	if name != "" {
		terms = append(terms, `"`+name+`"`)
	}
	return strings.Join(terms, " OR ")
}

// searchSalesLeadSlack searches the workspace's Slack live for the
// conversation's person and company. Nothing is stored.
func (s *Server) searchSalesLeadSlack(w http.ResponseWriter, r *http.Request) {
	m, ok := s.salesPerson(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	l, err := s.salesLoad(r.Context(), s.pool, a, r.PathValue("id"), false)
	if err == nil && !salesVisible(l, a, m) {
		err = errNotFound
	}
	if s.allocError(w, r, err) {
		return
	}
	out := slackSearchOut{Query: slackSearchQuery(l.Email, l.Company, l.Website), Data: []slackSearchHit{}}

	// The connection is the one the brand's channels are set up on. A
	// removed channel, or one whose connection was deleted, is not one of
	// them: its private channel's hits are dropped like any other's.
	rows, err := s.pool.Query(r.Context(), `SELECT integration_id::text,channel_id FROM sales_feed_channels
		WHERE workspace_id=$1 AND brand_id=$2 AND removed_at IS NULL AND integration_id IS NOT NULL ORDER BY created_at,channel_name,id`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var integs, leadChannels []string
	for rows.Next() {
		var integ, ch string
		if err := rows.Scan(&integ, &ch); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		integs = append(integs, integ)
		leadChannels = append(leadChannels, ch)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	var o *connectors.Opened
	if len(integs) > 0 {
		serving, err := s.feedServing(r.Context(), a)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for _, id := range integs {
			if !slices.ContainsFunc(serving, func(i connectors.Integration) bool { return i.ID == id }) {
				continue
			}
			op, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, id)
			if err != nil {
				// Gone or unreadable: the answer stays "not available", and the event says why.
				track.Fail(r.Context(), s.log, track.Event{Name: "sales_feed.slack_search_failed", WorkspaceID: a.WorkspaceID, Outcome: track.Failure}, slackSearchCode(err), err)
				continue
			}
			o = op
			break
		}
	}
	if o != nil {
		out.Available = connectors.CanSearchMessages(o.Connector, o.Creds)
	}
	if !out.Available || out.Query == "" {
		s.slackSearched(r, a, out)
		writeJSON(w, http.StatusOK, out)
		return
	}
	hits, err := o.Connector.(connectors.MessageSearcher).Search(r.Context(), s.integrations.Client, o.Creds, out.Query, slackSearchAsk)
	if err != nil {
		code := slackSearchCode(err)
		// Slack's own code says why ("not_allowed_token_type") without
		// its words; a service that is down has none.
		ev := track.Event{Name: "sales_feed.slack_search_failed", WorkspaceID: a.WorkspaceID, Outcome: track.Failure}
		if pc := connectors.ProviderCode(err); pc != "" {
			ev.Props = map[string]any{"provider_code": pc}
		}
		if isConnectorErr(err) {
			// Slack's own words can quote what was searched for or a response body.
			kind, pc := connectors.KindOf(err), connectors.ProviderCode(err)
			switch {
			case kind == connectors.ErrAuth:
				err = connectors.Errf(kind, "Slack refused the search token. On the Slack connection, add a search token that is a person's token (it starts with xoxp-) with the search:read scope.")
			case pc != "":
				err = connectors.Errf(kind, "Slack search failed (%s: %s)", kind, pc)
			default:
				err = connectors.Errf(kind, "Slack search failed (%s)", kind)
			}
		}
		track.Fail(r.Context(), s.log, ev, code, err)
		s.connectorFailed(w, r, err)
		return
	}
	for _, h := range hits {
		if len(out.Data) == slackSearchKeep {
			break
		}
		if !h.Public && (h.Channel.ID == "" || !slices.Contains(leadChannels, h.Channel.ID)) {
			continue
		}
		text := h.Text
		if rs := []rune(text); len(rs) > slackSearchText {
			text = string(rs[:slackSearchText])
		}
		out.Data = append(out.Data, slackSearchHit{ChannelName: h.Channel.Name, Author: h.Author, At: h.At, Text: text, Link: h.Link})
	}
	s.slackSearched(r, a, out)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) slackSearched(r *http.Request, a *access, out slackSearchOut) {
	track.Emit(r.Context(), track.Event{Name: "sales_feed.slack_searched", WorkspaceID: a.WorkspaceID,
		Props: map[string]any{"hits": len(out.Data), "available": out.Available}})
}

// slackSearchCode is the catalog code of a failed Slack call.
func slackSearchCode(err error) string {
	switch connectors.KindOf(err) {
	case connectors.ErrAuth:
		return "connector_auth"
	case connectors.ErrQuota:
		return "connector_quota"
	case connectors.ErrInvalid:
		return "connector_invalid"
	}
	return "connector_unavailable"
}
