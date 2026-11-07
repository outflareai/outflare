package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// The starter sequence for story pitches: a draft Outflare sequence for a
// brand that passes every rule startStoryPitches checks (stops on any
// reply, one email and one follow-up, the opener in the first step), set
// up like any new sequence for a person to give mailboxes and activate.

func (s *Server) routesStoryPitchSequence(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/brands/{brand}/stories/pitch-sequence", s.onBrand(auth.ScopeSequencesManage, s.createStoryPitchSequence))
	mux.HandleFunc("POST /v1/workspaces/{ws}/stories/pitch-sequence", s.inBrand(auth.ScopeSequencesManage, s.createStoryPitchSequence))
}

// storyPitchSteps are the starter sequence's steps: the headline as the
// subject and the AI's opener, then one short follow-up in the same
// thread. People edit them like any sequence.
var storyPitchSteps = []stepInput{
	{Subject: "{{custom.story_title}}", Body: "Hi {{first_name|there}},\n\n{{custom.opener}}\n\n" +
		"Happy to send the data, the full release or set up an interview if it helps.\n\n{{sender_first_name}}"},
	{DelayDays: 3, Body: "Hi {{first_name|there}}, just checking this reached you. Happy to send anything that helps with the story.\n\n{{sender_first_name}}"},
}

// createStoryPitchSequence makes the starter sequence for a brand's story
// pitches.
func (s *Server) createStoryPitchSequence(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		BrandID string `json:"brand_id"`
		Name    string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	if in.BrandID == "" {
		in.BrandID = a.Brand
	}
	brand, err := s.brandIDFor(ctx, a.WorkspaceID, in.BrandID)
	if errors.Is(err, errNotFound) || (err == nil && !a.sees(&brand)) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id: brand not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !a.writes(&brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		var brandName string
		if err := s.pool.QueryRow(ctx, `SELECT name FROM brands WHERE id = $1 AND workspace_id = $2`, brand, a.WorkspaceID).Scan(&brandName); err != nil {
			s.internal(w, r, err)
			return
		}
		name = clip("Story pitches: "+brandName, 200)
	}
	if len(name) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is at most 200 characters")
		return
	}
	stop := true
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if id, err = s.insertSequence(ctx, tx, a, &sequenceSettings{Name: &name, StopOnReply: &stop, BrandID: &brand}, storyPitchSteps); err != nil {
			return err
		}
		// The rules a pitch's sequence must pass, checked here so the
		// starter can never drift from them.
		if err := pitchSequence(ctx, tx, a.WorkspaceID, id, brand); err != nil {
			return err
		}
		if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "sequence.created", "sequence", id,
			map[string]any{"name": name, "starter": "story_pitches"}, map[string]any{"starter": "story_pitches"}); err != nil {
			return err
		}
		data := map[string]any{"brand_id": brand, "sequence_id": id}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "story.pitch_sequence_created", "sequence", id, data, data)
	})
	var e errPitch
	if errors.As(err, &e) {
		s.internal(w, r, err)
		return
	}
	if s.seqError(w, r, err) {
		return
	}
	if v, ok := s.findSequence(w, r, a.WorkspaceID, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}
