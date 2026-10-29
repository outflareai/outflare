package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/linkbuild"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// Link pitches: a prospect pitched to one contact through one of the
// workspace's Outreach sequences, with an opener the workspace's AI wrote
// (package linkbuild). A pitch waits for a person's approval unless the
// sequence's link pitches skip it; approving enrolls the contact, so it
// needs the Outreach scope. Results count the prospects by method.

func (s *Server) routesLinkPitches(mux *http.ServeMux) {
	read, manage := auth.ScopeSequencesRead, auth.ScopeSequencesManage
	mux.HandleFunc("POST /v1/workspaces/{ws}/link-prospects/{prospect}/pitches", s.inBrand(manage, s.startLinkPitch))
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-pitches", s.inBrand(read, s.listLinkPitches))
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-pitches/{pitch}", s.inBrand(read, s.getLinkPitch))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/link-pitches/{pitch}", s.inBrand(manage, s.editLinkPitch))
	mux.HandleFunc("POST /v1/workspaces/{ws}/link-pitches/{pitch}/approve", s.inBrand(manage, s.approveLinkPitch))
	mux.HandleFunc("POST /v1/workspaces/{ws}/link-pitches/{pitch}/reject", s.inBrand(manage, s.rejectLinkPitch))
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-building/sequences", s.inBrand(read, s.listLinkSequences))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/link-building/sequences/{sequence}", s.inBrand(manage, s.updateLinkSequence))
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-prospects/results", s.inBrand(auth.ScopeSEORead, s.linkResults))
}

type linkPitchView struct {
	ID           string     `json:"id"`
	ProspectID   string     `json:"prospect_id"`
	SequenceID   string     `json:"sequence_id"`
	SequenceName string     `json:"sequence_name"`
	BrandID      *string    `json:"brand_id"`
	RecordID     string     `json:"record_id"`
	ContactName  string     `json:"contact_name"`
	ContactEmail *string    `json:"contact_email"`
	LeadID       *string    `json:"lead_id"`
	PageURL      string     `json:"page_url"`
	Domain       string     `json:"domain"`
	TargetURL    string     `json:"target_url"`
	Stage        string     `json:"stage"`
	Status       string     `json:"status"`
	Opener       string     `json:"opener"`
	Reason       string     `json:"reason"`
	ReplyKind    *string    `json:"reply_kind"`
	RepliedAt    *time.Time `json:"replied_at"`
	ApprovedAt   *time.Time `json:"approved_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const linkPitchCols = `k.id::text, k.prospect_id::text, k.sequence_id::text, q.name, x.brand_id::text, k.record_id::text, r.name,
	(SELECT i.value FROM record_identities i WHERE i.record_id = k.record_id AND i.namespace = 'email' AND i.valid_to IS NULL
		ORDER BY i.verified DESC, i.created_at LIMIT 1),
	k.lead_id::text, x.page_url, x.target_url, x.stage, k.status, k.opener, k.reason, k.reply_kind, k.replied_at, k.approved_at, k.created_at, k.updated_at
	FROM link_pitches k JOIN link_prospects x ON x.id = k.prospect_id JOIN sequences q ON q.id = k.sequence_id JOIN records r ON r.id = k.record_id`

func scanLinkPitch(row pgx.Row) (linkPitchView, error) {
	var v linkPitchView
	err := row.Scan(&v.ID, &v.ProspectID, &v.SequenceID, &v.SequenceName, &v.BrandID, &v.RecordID, &v.ContactName, &v.ContactEmail,
		&v.LeadID, &v.PageURL, &v.TargetURL, &v.Stage, &v.Status, &v.Opener, &v.Reason, &v.ReplyKind, &v.RepliedAt, &v.ApprovedAt,
		&v.CreatedAt, &v.UpdatedAt)
	_, v.Domain = seosync.ProspectPage(v.PageURL)
	return v, err
}

func (s *Server) loadLinkPitch(ctx context.Context, ws, id string, sel brandSel) (*linkPitchView, error) {
	args := []any{ws, id}
	v, err := scanLinkPitch(s.pool.QueryRow(ctx, `SELECT `+linkPitchCols+` WHERE k.workspace_id = $1 AND k.id = $2`+sel.sql("x.brand_id", &args), args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

func (s *Server) linkPitchFor(w http.ResponseWriter, r *http.Request, a *access) (*linkPitchView, bool) {
	id := r.PathValue("pitch")
	if validUUID(id) {
		p, err := s.loadLinkPitch(r.Context(), a.WorkspaceID, id, brandSel{in: a.Brands})
		if err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		if p != nil {
			return p, true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "link pitch not found")
	return nil, false
}

// pitchError answers why a pitch couldn't start or be approved.
func (s *Server) pitchError(w http.ResponseWriter, r *http.Request, err error) {
	conflict := func(msg string) { writeError(w, r, http.StatusConflict, "conflict", msg) }
	switch {
	case errors.Is(err, linkbuild.ErrNotPitchable):
		conflict("the prospect isn't to pitch: only a prospect at stage prospect is pitched")
	case errors.Is(err, linkbuild.ErrPitchOpen):
		conflict("the prospect already has a pitch being written, waiting for approval or sent; reject it first")
	case errors.Is(err, linkbuild.ErrSequenceLink):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sequence_id runs as a campaign in a sequencer; pitch through one of Outflare's own sequences")
	case errors.Is(err, linkbuild.ErrSequenceGone):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sequence_id is archived")
	case errors.Is(err, linkbuild.ErrNoContact):
		conflict("nobody to write to: check the prospect's page to find contacts (POST .../link-prospects/{id}/check), or pass one of its contacts' record_id")
	case errors.Is(err, linkbuild.ErrPitchState):
		conflict("only a draft pitch is approved, edited or rejected")
	case errors.Is(err, linkbuild.ErrNoOpener):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "opener is empty: write one, or wait for the AI's")
	case errors.Is(err, seosync.ErrSiteInOutreach):
		writeError(w, r, http.StatusConflict, "site_in_outreach", "another prospect on this website is being pitched, or was declined within the cool-down")
	case errors.Is(err, seosync.ErrSiteOptedOut), errors.Is(err, linkbuild.ErrContactBlocked):
		conflict("the website or the contact is on the suppression list, so it is never pitched")
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
	default:
		s.allocError(w, r, err)
	}
}

func (s *Server) startLinkPitch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		SequenceID string `json:"sequence_id"`
		RecordID   string `json:"record_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validUUID(in.SequenceID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sequence_id is a sequence's id")
		return
	}
	if in.RecordID != "" && !validUUID(in.RecordID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "record_id is one of the prospect's contacts")
		return
	}
	p, ok := s.linkProspectFor(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	var seqBrand *string
	err := s.pool.QueryRow(ctx, `SELECT brand_id::text FROM sequences WHERE id = $1 AND workspace_id = $2`, in.SequenceID, a.WorkspaceID).Scan(&seqBrand)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(nil, seqBrand)) {
		writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !a.writes(nil, p.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, err = linkbuild.StartPitch(ctx, tx, a.WorkspaceID, linkbuild.NewPitch{ProspectID: p.ID, SequenceID: in.SequenceID, RecordID: in.RecordID,
			CreatedBy: actorRef(a)}, s.prospectRecorder(a))
		return err
	})
	if err != nil {
		s.pitchError(w, r, err)
		return
	}
	v, err := s.loadLinkPitch(ctx, a.WorkspaceID, id, brandSel{})
	if err != nil || v == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

var linkPitchStatuses = []string{"writing", "draft", "enrolled", "rejected", "failed"}

func (s *Server) listLinkPitches(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	args := []any{a.WorkspaceID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := `k.workspace_id = $1`
	if v := q.Get("status"); v != "" {
		if !slices.Contains(linkPitchStatuses, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(linkPitchStatuses, ", "))
			return
		}
		where += ` AND k.status = ` + arg(v)
	}
	for _, f := range []struct{ param, col string }{{"sequence_id", "k.sequence_id"}, {"prospect_id", "k.prospect_id"}} {
		if v := q.Get(f.param); v != "" {
			if !validUUID(v) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", f.param+" is an id")
				return
			}
			where += ` AND ` + f.col + ` = ` + arg(v) + `::uuid`
		}
	}
	where += sel.sql("x.brand_id", &args)
	limit, err := limitParam(r, 100, 500)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+linkPitchCols+` WHERE `+where+` ORDER BY k.created_at DESC, k.id LIMIT `+arg(limit+1)+` OFFSET `+arg(offset), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkPitchView, error) { return scanLinkPitch(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if len(list) > limit {
		list = list[:limit]
		n := offset + limit
		next = &n
	}
	if list == nil {
		list = []linkPitchView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_offset": next})
}

func (s *Server) getLinkPitch(w http.ResponseWriter, r *http.Request) {
	p, ok := s.linkPitchFor(w, r, accessFrom(r.Context()))
	if ok {
		writeJSON(w, http.StatusOK, p)
	}
}

// writablePitch finds a pitch the caller may change.
func (s *Server) writablePitch(w http.ResponseWriter, r *http.Request, a *access) (*linkPitchView, bool) {
	p, ok := s.linkPitchFor(w, r, a)
	if !ok {
		return nil, false
	}
	if !a.writes(nil, p.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return nil, false
	}
	return p, true
}

func (s *Server) editLinkPitch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Opener string `json:"opener"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Opener = strings.TrimSpace(in.Opener)
	if in.Opener == "" || len(in.Opener) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "opener is 1 to 2000 characters")
		return
	}
	p, ok := s.writablePitch(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE link_pitches SET opener = $3, reason = '', updated_at = now() WHERE id = $1 AND workspace_id = $2 AND status = 'draft'`,
			p.ID, a.WorkspaceID, in.Opener)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return linkbuild.ErrPitchState
		}
		data := map[string]any{"prospect_id": p.ProspectID, "sequence_id": p.SequenceID}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "link_pitch.edited", "link_pitch", p.ID, data, data)
	})
	if err != nil {
		s.pitchError(w, r, err)
		return
	}
	s.getLinkPitch(w, r)
}

func (s *Server) approveLinkPitch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Opener *string `json:"opener"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	if in.Opener != nil && len(*in.Opener) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "opener is at most 2000 characters")
		return
	}
	p, ok := s.writablePitch(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return linkbuild.Approve(ctx, tx, a.WorkspaceID, p.ID, in.Opener, actorRef(a), s.prospectRecorder(a))
	})
	if err != nil {
		s.pitchError(w, r, err)
		return
	}
	s.getLinkPitch(w, r)
}

func (s *Server) rejectLinkPitch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	p, ok := s.writablePitch(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return linkbuild.Reject(ctx, tx, a.WorkspaceID, p.ID, strings.TrimSpace(in.Reason), s.prospectRecorder(a))
	})
	if err != nil {
		s.pitchError(w, r, err)
		return
	}
	s.getLinkPitch(w, r)
}

type linkSequenceView struct {
	SequenceID     string  `json:"sequence_id"`
	Name           string  `json:"name"`
	Status         string  `json:"status"`
	BrandID        *string `json:"brand_id"`
	ApprovePitches bool    `json:"approve_pitches"`
	// UsesOpener: one of its steps has {{custom.opener}}.
	UsesOpener bool `json:"uses_opener"`
	Pitches    int  `json:"pitches"`
}

// listLinkSequences lists the sequences pitches can go through (Outflare's
// own, not archived), with whether their pitches wait for approval.
func (s *Server) listLinkSequences(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), `
		SELECT q.id::text, q.name, q.status, q.brand_id::text, COALESCE(l.approve_pitches, true),
			EXISTS (SELECT 1 FROM sequence_steps st WHERE st.sequence_id = q.id AND (st.body ILIKE '%custom.opener%' OR st.body ILIKE '%{{opener%')),
			(SELECT count(*) FROM link_pitches k WHERE k.sequence_id = q.id)::int
		FROM sequences q LEFT JOIN link_sequence_settings l ON l.sequence_id = q.id
		WHERE q.workspace_id = $1 AND q.status <> 'archived' AND NOT EXISTS (SELECT 1 FROM campaign_links c WHERE c.sequence_id = q.id)`+
		sel.sql("q.brand_id", &args)+` ORDER BY q.name LIMIT 500`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkSequenceView, error) {
		var v linkSequenceView
		return v, row.Scan(&v.SequenceID, &v.Name, &v.Status, &v.BrandID, &v.ApprovePitches, &v.UsesOpener, &v.Pitches)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []linkSequenceView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// updateLinkSequence turns approval of a sequence's link pitches on or off.
func (s *Server) updateLinkSequence(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		ApprovePitches *bool `json:"approve_pitches"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ApprovePitches == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "approve_pitches is true or false")
		return
	}
	id := r.PathValue("sequence")
	ctx := r.Context()
	var brand *string
	err := s.pool.QueryRow(ctx, `SELECT brand_id::text FROM sequences WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID).Scan(&brand)
	if !validUUID(id) || errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(nil, brand)) {
		writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !a.writes(nil, brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO link_sequence_settings (sequence_id, workspace_id, approve_pitches, updated_by) VALUES ($1, $2, $3, $4)
			ON CONFLICT (sequence_id) DO UPDATE SET approve_pitches = EXCLUDED.approve_pitches, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			id, a.WorkspaceID, *in.ApprovePitches, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		data := map[string]any{"approve_pitches": *in.ApprovePitches}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "link_building.sequence_updated", "sequence", id, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sequence_id": id, "approve_pitches": *in.ApprovePitches})
}

type linkResultRow struct {
	Method   string   `json:"method"`
	Found    int      `json:"found"`
	Pitched  int      `json:"pitched"`
	Replied  int      `json:"replied"`
	Agreed   int      `json:"agreed"`
	Live     int      `json:"live"`
	Declined int      `json:"declined"`
	Removed  int      `json:"removed"`
	WinRate  *float64 `json:"win_rate"`
}

// linkResults counts prospects found in the last days by method: how many
// were pitched and how far each got. win_rate is live links per pitch.
func (s *Server) linkResults(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	site, ok := siteParam(w, r)
	if !ok {
		return
	}
	days := 90
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 730 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 730")
			return
		}
		days = n
	}
	args := []any{a.WorkspaceID, site, days}
	rows, err := s.pool.Query(r.Context(), `
		SELECT method, count(*)::int,
			count(*) FILTER (WHERE pitched_at IS NOT NULL)::int,
			count(*) FILTER (WHERE stage IN ('replied', 'agreed', 'live', 'removed') OR (stage = 'declined' AND pitched_at IS NOT NULL))::int,
			count(*) FILTER (WHERE agreed_at IS NOT NULL)::int,
			count(*) FILTER (WHERE live_at IS NOT NULL)::int,
			count(*) FILTER (WHERE stage = 'declined')::int,
			count(*) FILTER (WHERE stage = 'removed')::int
		FROM link_prospects p
		WHERE p.workspace_id = $1 AND ($2 = '' OR p.site_id = NULLIF($2, '')::uuid) AND p.created_at > now() - make_interval(days => $3)
		  AND p.stage <> 'excluded'`+sel.sql("p.brand_id", &args)+`
		GROUP BY method ORDER BY count(*) DESC, method`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkResultRow, error) {
		var v linkResultRow
		return v, row.Scan(&v.Method, &v.Found, &v.Pitched, &v.Replied, &v.Agreed, &v.Live, &v.Declined, &v.Removed)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	total := linkResultRow{Method: "all"}
	rate := func(v *linkResultRow) {
		if v.Pitched > 0 {
			x := float64(v.Live) / float64(v.Pitched)
			v.WinRate = &x
		}
	}
	for i := range list {
		rate(&list[i])
		t := list[i]
		total.Found, total.Pitched, total.Replied, total.Agreed = total.Found+t.Found, total.Pitched+t.Pitched, total.Replied+t.Replied, total.Agreed+t.Agreed
		total.Live, total.Declined, total.Removed = total.Live+t.Live, total.Declined+t.Declined, total.Removed+t.Removed
	}
	rate(&total)
	if list == nil {
		list = []linkResultRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "totals": total, "data": list})
}

// decodeOptional is decode for a body that may be left out.
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	return decode(w, r, v)
}
