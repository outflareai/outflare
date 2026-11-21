package api

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// What a Sales channel's posts show first. Every source carries different
// fields (a form's questions, a ticket's fields), so each channel has an
// ordered list of the labels its team wants to see first
// (sales_feed_channels.preview_fields); with none chosen, Outflare picks.

const (
	feedLayoutMax   = 6   // labels a channel can show first
	feedLabelMax    = 120 // characters of a label
	feedQueueFields = 4   // of them, in a queue row
	feedKeyValueMax = 200 // characters of a value in a queue row
	feedLabelSample = 50  // newest posts read for a channel's labels
	feedLabelsMax   = 40  // labels listed for a channel
)

// feedOnPerson matches the label of a field the queue already shows beside
// the person (their name, address, phone, company and website) or beside
// the conversation (who owns it).
var feedOnPerson = regexp.MustCompile(`(?i)\bname\b|e-?mail|phone|company|website|\burl\b|linkedin|owner|assigned`)

// feedLabelKey is a label as two posts' labels are compared: case, outer
// spaces and a closing colon or question mark don't matter.
func feedLabelKey(label string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(label), ":?")))
}

// feedKeyFields are the fields a post leads with, at most max: the layout's
// labels in its order, one with an empty value when the post has none, or,
// without a layout, the first three answered fields that are not about who
// the person is.
func feedKeyFields(c feedContent, layout []string, max int) []feedFieldView {
	out := []feedFieldView{}
	if len(layout) == 0 {
		for _, f := range c.Fields {
			if len(out) == min(max, 3) {
				break
			}
			if v := strings.TrimSpace(f.Value); v != "" && !feedOnPerson.MatchString(f.Label) {
				out = append(out, feedFieldView{Label: f.Label, Value: feedCut(v, feedKeyValueMax)})
			}
		}
		return out
	}
	for _, label := range layout {
		if len(out) == max {
			break
		}
		v := feedFieldView{Label: label}
		for _, f := range c.Fields {
			if feedLabelKey(f.Label) == feedLabelKey(label) {
				v.Value = feedCut(strings.TrimSpace(f.Value), feedKeyValueMax)
				break
			}
		}
		out = append(out, v)
	}
	return out
}

// feedQueueBody is the text under a queue row's fields: what the person
// wrote, when it is not already one of the fields shown. A post with no
// fields at all (free text) shows the AI's summary, else the post.
func feedQueueBody(c feedContent, shown []feedFieldView) string {
	body := ""
	if c.Reading != nil {
		body = strings.TrimSpace(c.Reading.Message)
	}
	if body == "" {
		body = feedSaidText(c)
	}
	if body == "" && len(c.Fields) == 0 {
		if c.Reading != nil && c.Reading.Summary != "" {
			return c.Reading.Summary
		}
		return c.Text
	}
	flat := strings.Join(strings.Fields(body), " ")
	// A reading that only says the form's fields over again ("Expected
	// monthly traffic volume: 1TB+. ...") adds nothing under them.
	if len(shown) > 0 {
		low := strings.ToLower(flat)
		for _, f := range c.Fields {
			if key := feedLabelKey(f.Label); key != "" && strings.Contains(low, key+":") {
				return ""
			}
		}
	}
	for _, f := range shown {
		if v := strings.Join(strings.Fields(f.Value), " "); v != "" && (strings.Contains(v, flat) || strings.Contains(flat, v) && utf8.RuneCountInString(v)*2 >= utf8.RuneCountInString(flat)) {
			return ""
		}
	}
	return body
}

type feedLabelCount struct {
	Label string `json:"label"`
	Posts int    `json:"posts"`
}

// listSalesFeedFields lists the field labels a channel's newest posts carry,
// the most common first, for choosing what the channel shows first.
func (s *Server) listSalesFeedFields(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.feedDeskOnly(w, r); !ok {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var layout []string
	err := errNotFound
	if validUUID(id) {
		err = s.pool.QueryRow(r.Context(), `SELECT preview_fields FROM sales_feed_channels WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND removed_at IS NULL`, a.WorkspaceID, a.Brand, id).Scan(&layout)
		if errors.Is(err, pgx.ErrNoRows) {
			err = errNotFound
		}
	}
	if s.allocError(w, r, err) {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id::text,content_sealed FROM sales_feed_items
		WHERE workspace_id=$1 AND brand_id=$2 AND feed_channel_id=$3 AND status NOT IN ('spam','ignored') AND NOT deleted_in_slack
		ORDER BY occurred_at DESC,id DESC LIMIT $4`, a.WorkspaceID, a.Brand, id, feedLabelSample)
	if s.allocError(w, r, err) {
		return
	}
	defer rows.Close()
	labels := []feedLabelCount{}
	at := map[string]int{}
	for rows.Next() {
		var item string
		var sealed []byte
		if err := rows.Scan(&item, &sealed); s.allocError(w, r, err) {
			return
		}
		var c feedContent
		if s.salesOpen(sealed, item, &c) != nil {
			continue // a post that can't be opened has no labels to offer
		}
		seen := map[string]bool{}
		for _, f := range c.Fields {
			key := feedLabelKey(f.Label)
			if key == "" || seen[key] || utf8.RuneCountInString(f.Label) > feedLabelMax {
				continue
			}
			seen[key] = true
			i, ok := at[key]
			if !ok {
				i = len(labels)
				at[key] = i
				labels = append(labels, feedLabelCount{Label: strings.TrimSpace(f.Label)})
			}
			labels[i].Posts++
		}
	}
	if s.allocError(w, r, rows.Err()) {
		return
	}
	slices.SortStableFunc(labels, func(x, y feedLabelCount) int { return y.Posts - x.Posts })
	if len(labels) > feedLabelsMax {
		labels = labels[:feedLabelsMax]
	}
	if layout == nil {
		layout = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": labels, "preview_fields": layout})
}

// putSalesFeedLayout saves what a channel's posts show first. Anyone on the
// sales desk may: it changes how posts are laid out, not what is read.
func (s *Server) putSalesFeedLayout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.feedDeskOnly(w, r); !ok {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var in struct {
		PreviewFields []string `json:"preview_fields"`
	}
	if !decode(w, r, &in) {
		return
	}
	layout := []string{}
	seen := map[string]bool{}
	for _, label := range in.PreviewFields {
		label = strings.TrimSpace(label)
		key := feedLabelKey(label)
		if key == "" || utf8.RuneCountInString(label) > feedLabelMax || seen[key] {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "preview_fields are different field labels of at most 120 characters")
			return
		}
		seen[key] = true
		layout = append(layout, label)
	}
	if len(layout) > feedLayoutMax {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a channel shows at most 6 fields first")
		return
	}
	err := errNotFound
	if validUUID(id) {
		err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(r.Context(), `UPDATE sales_feed_channels SET preview_fields=$4,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND removed_at IS NULL`, a.WorkspaceID, a.Brand, id, layout)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errNotFound
			}
			return record(r.Context(), tx, &a.principal, a.WorkspaceID, "sales_feed.layout_updated", "sales_feed_channel", id, map[string]any{"brand_id": a.Brand, "fields": len(layout)})
		})
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "preview_fields": layout})
}
