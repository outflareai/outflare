package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Working on a creative after it is made, without losing anything: a fix
// to a talking video waits for the person's keep or go back, every
// version is kept, a creative can be liked or marked "not this", a good
// one gets variations, what a person taught is used for every later
// creative, and one history per creative holds all of it.

const (
	maxLessons    = 200
	maxVariations = 5
	historyShown  = 500
)

func (s *Server) routesCreativeWorkflows(mux *http.ServeMux) {
	read, manage := auth.ScopeContentRead, auth.ScopeContentManage
	videos := "/v1/brands/{brand}/videos/{video}"
	mux.HandleFunc("POST "+videos+"/fix/keep", s.onBrand(manage, s.keepVideoFix))
	mux.HandleFunc("POST "+videos+"/fix/go-back", s.onBrand(manage, s.discardVideoFix))
	mux.HandleFunc("POST "+videos+"/versions/{number}/restore", s.onBrand(manage, s.restoreVideoVersion))
	mux.HandleFunc("POST "+videos+"/rating", s.onBrand(manage, s.rateBrandVideo))
	mux.HandleFunc("POST "+videos+"/variations", s.onBrand(manage, s.varyBrandVideo))
	mux.HandleFunc("GET "+videos+"/history", s.onBrand(read, s.videoHistory))
	creatives := "/v1/brands/{brand}/creatives/{creative}"
	mux.HandleFunc("POST "+creatives+"/rating", s.onBrand(manage, s.rateCreative))
	mux.HandleFunc("GET "+creatives+"/history", s.onBrand(read, s.creativeHistory))
	lessons := "/v1/brands/{brand}/creative-lessons"
	mux.HandleFunc("GET "+lessons, s.onBrand(read, s.listCreativeLessons))
	mux.HandleFunc("POST "+lessons, s.onBrand(manage, s.addCreativeLesson))
	mux.HandleFunc("DELETE "+lessons+"/{lesson}", s.onBrand(manage, s.removeCreativeLesson))
}

// ---- Versions of a talking video ----

// videoVersion is one state a talking video has been in.
type videoVersion struct {
	Number int `json:"number"`
	// current: what the video is now. waiting: a fix nobody has answered.
	// before: an earlier version. gone_back: a fix that was turned down.
	State        string                  `json:"state"`
	BaseAssetID  *string                 `json:"base_asset_id"`
	FinalAssetID *string                 `json:"final_asset_id"`
	Words        []connectors.SpokenWord `json:"words"`
	Seconds      float64                 `json:"seconds"`
	Notes        []string                `json:"notes"`
	// The edit that made it; null for the first version.
	Edit *videoEdit `json:"edit"`
	// Why the person went back, when they did.
	Reason    string     `json:"reason"`
	Note      string     `json:"note"`
	DecidedBy *string    `json:"decided_by"`
	DecidedAt *time.Time `json:"decided_at"`
	CreatedAt time.Time  `json:"created_at"`
}

func videoVersions(ctx context.Context, q videoQuerier, ws, id string) ([]videoVersion, error) {
	rows, err := q.Query(ctx, `SELECT number, state, base_asset_id::text, final_asset_id::text, words, notes, edit, reason, note, decided_by::text, decided_at, created_at
		FROM brand_video_versions WHERE video_id = $1 AND workspace_id = $2 ORDER BY number DESC`, id, ws)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (videoVersion, error) {
		var v videoVersion
		var words, notes, edit []byte
		err := row.Scan(&v.Number, &v.State, &v.BaseAssetID, &v.FinalAssetID, &words, &notes, &edit, &v.Reason, &v.Note, &v.DecidedBy, &v.DecidedAt, &v.CreatedAt)
		v.Words, v.Notes = []connectors.SpokenWord{}, []string{}
		_ = json.Unmarshal(words, &v.Words)
		_ = json.Unmarshal(notes, &v.Notes)
		if len(edit) > 0 && string(edit) != "null" {
			v.Edit = &videoEdit{}
			_ = json.Unmarshal(edit, v.Edit)
		}
		if n := len(v.Words); n > 0 {
			v.Seconds = v.Words[n-1].End
		}
		return v, err
	})
	return nonNil(list), err
}

// addVideoVersion keeps the video's files and words, as cur has them, as
// a new version in the state given, and says its number.
func addVideoVersion(ctx context.Context, tx pgx.Tx, cur videoView, state string, edit *videoEdit) (int, error) {
	words, _ := json.Marshal(cur.Words)
	notes, _ := json.Marshal(cur.Notes)
	var raw []byte
	if edit != nil {
		e := *edit
		// The words of the whole video are on the version already.
		e.Words = nil
		raw, _ = json.Marshal(e)
	}
	var number int
	err := tx.QueryRow(ctx, `INSERT INTO brand_video_versions (workspace_id, brand_id, video_id, number, state, base_asset_id, final_asset_id, words, notes, edit)
		VALUES ($1, $2, $3, (SELECT COALESCE(max(number), 0) + 1 FROM brand_video_versions WHERE video_id = $3), $4, $5, $6, $7, $8, $9) RETURNING number`,
		cur.ws, cur.BrandID, cur.ID, state, cur.BaseAssetID, cur.FinalAssetID, words, notes, raw).Scan(&number)
	return number, err
}

// currentVideoVersion is the number of the version the video is now. A
// video made before versions were kept gets one here.
func currentVideoVersion(ctx context.Context, tx pgx.Tx, cur videoView) (int, error) {
	var number int
	err := tx.QueryRow(ctx, `SELECT number FROM brand_video_versions WHERE video_id = $1 AND state = 'current'`, cur.ID).Scan(&number)
	if errors.Is(err, pgx.ErrNoRows) {
		return addVideoVersion(ctx, tx, cur, "current", nil)
	}
	return number, err
}

// showVideoVersion makes the video the files and words of one of its
// versions.
func showVideoVersion(ctx context.Context, tx pgx.Tx, videoID string, number int) error {
	_, err := tx.Exec(ctx, `UPDATE brand_videos v SET base_asset_id = ver.base_asset_id, final_asset_id = ver.final_asset_id, words = ver.words, notes = ver.notes, updated_at = now()
		FROM brand_video_versions ver WHERE ver.video_id = v.id AND ver.number = $2 AND v.id = $1`, videoID, number)
	return err
}

// versionWhenReady keeps what was just made as a version. A new video's
// is its first, and current. An edit's waits: the video goes back to
// what it was until a person keeps the fix.
func versionWhenReady(ctx context.Context, tx pgx.Tx, v videoView) error {
	made, err := findVideo(ctx, tx, v.ws, "", v.ID, false)
	if err != nil {
		return err
	}
	if v.Round == 0 {
		var has bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brand_video_versions WHERE video_id = $1)`, v.ID).Scan(&has); err != nil || has {
			return err
		}
		number, err := addVideoVersion(ctx, tx, made, "current", nil)
		if err != nil {
			return err
		}
		by := ""
		if v.CreatedBy != nil {
			by = *v.CreatedBy
		}
		return addHistory(ctx, tx, v.ws, v.BrandID, "video", v.ID, "made", "", by, map[string]any{"version": number})
	}
	var current int
	err = tx.QueryRow(ctx, `SELECT number FROM brand_video_versions WHERE video_id = $1 AND state = 'current'`, v.ID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing to go back to, so the edit is the video.
		_, err = addVideoVersion(ctx, tx, made, "current", v.Edit)
		return err
	}
	if err != nil {
		return err
	}
	number, err := addVideoVersion(ctx, tx, made, "waiting", v.Edit)
	if err != nil {
		return err
	}
	if err := showVideoVersion(ctx, tx, v.ID, current); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE brand_videos SET waiting_version = $2 WHERE id = $1`, v.ID, number); err != nil {
		return err
	}
	return addHistory(ctx, tx, v.ws, v.BrandID, "video", v.ID, "fix_ready", "", "", map[string]any{"version": number})
}

// undoFailedEdit puts a video whose edit failed half way back to its
// current version.
func undoFailedEdit(ctx context.Context, tx pgx.Tx, v videoView) error {
	var current int
	err := tx.QueryRow(ctx, `SELECT number FROM brand_video_versions WHERE video_id = $1 AND state = 'current'`, v.ID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return showVideoVersion(ctx, tx, v.ID, current)
}

// waitingFix finds the video and the fix that waits on it.
func waitingFix(ctx context.Context, tx pgx.Tx, a *access, id string) (videoView, int, error) {
	if !validUUID(id) {
		return videoView{}, 0, errNotFound
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM brand_videos WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, id, a.WorkspaceID, a.Brand); err != nil {
		return videoView{}, 0, err
	}
	cur, err := findVideo(ctx, tx, a.WorkspaceID, a.Brand, id, true)
	if err != nil {
		return cur, 0, err
	}
	if cur.WaitingVersion == nil {
		return cur, 0, errConflictMsg("no fix to this video waits for an answer")
	}
	return cur, *cur.WaitingVersion, nil
}

// keepVideoFix answers "yes" to the fix that waits: it becomes the video,
// and the version before it is kept to go back to.
func (s *Server) keepVideoFix(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		// Remember how the fix spelled the words for the voice, for every
		// later video of the brand.
		Remember bool `json:"remember"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	var v videoView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, number, err := waitingFix(ctx, tx, a, r.PathValue("video"))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_video_versions SET state = 'before' WHERE video_id = $1 AND state = 'current'`, cur.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_video_versions SET state = 'current', decided_by = $3, decided_at = now() WHERE video_id = $1 AND number = $2`, cur.ID, number, actorOrNil(a)); err != nil {
			return err
		}
		if err := showVideoVersion(ctx, tx, cur.ID, number); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_videos SET waiting_version = NULL WHERE id = $1`, cur.ID); err != nil {
			return err
		}
		remembered := false
		if idx := slices.IndexFunc(cur.Versions, func(ver videoVersion) bool { return ver.Number == number }); in.Remember && idx >= 0 {
			if e := cur.Versions[idx].Edit; e != nil && e.Say != "" && e.Text != "" && len(e.Text) <= 100 && len(e.Say) <= 200 {
				if err := insertLesson(ctx, tx, a, lessonView{Kind: "say", Word: e.Text, Say: e.Say, AppliesTo: "video", SourceType: "video", SourceID: &cur.ID}); err != nil {
					return err
				}
				remembered = true
			}
		}
		if err := addHistory(ctx, tx, a.WorkspaceID, a.Brand, "video", cur.ID, "fix_kept", "", a.ActorID, map[string]any{"version": number, "remembered": remembered}); err != nil {
			return err
		}
		if v, err = findVideo(ctx, tx, a.WorkspaceID, a.Brand, cur.ID, true); err != nil {
			return err
		}
		data := videoData(v)
		data["version"], data["remembered"] = number, remembered
		return record(ctx, tx, &a.principal, a.WorkspaceID, "video.fix_kept", "video", cur.ID, data)
	})
	if s.assetError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// discardVideoFix answers "no" to the fix that waits: the video stays as
// it was before, and the fix is kept in its history with why. With
// try_again the same words are fixed again, told what was wrong.
func (s *Server) discardVideoFix(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		// Why, in a few words (a page offers "Still said wrong", "Looks
		// different from the rest", "Sounds different" and the like).
		Reason string `json:"reason"`
		// What is wrong, in the person's own words.
		Note string `json:"note"`
		// Make the fix again, with note told to the models.
		TryAgain bool `json:"try_again"`
		// For try_again: how to spell the words for the voice this time.
		Say string `json:"say"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	in.Reason, in.Note, in.Say = strings.TrimSpace(in.Reason), strings.TrimSpace(in.Note), strings.TrimSpace(in.Say)
	switch {
	case len(in.Reason) > 200:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason is at most 200 characters")
		return
	case len(in.Note) > 1000 || len(in.Say) > 1000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note and say are at most 1,000 characters")
		return
	case in.TryAgain && (s.video == nil || s.files == nil):
		writeError(w, r, http.StatusConflict, "not_configured", "this server has no ffmpeg, which cuts and stitches videos, so the fix can't be made again here. Go back without try_again.")
		return
	}
	ctx := r.Context()
	var v videoView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, number, err := waitingFix(ctx, tx, a, r.PathValue("video"))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_video_versions SET state = 'gone_back', reason = $3, note = $4, decided_by = $5, decided_at = now() WHERE video_id = $1 AND number = $2`,
			cur.ID, number, in.Reason, in.Note, actorOrNil(a)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_videos SET waiting_version = NULL, updated_at = now() WHERE id = $1`, cur.ID); err != nil {
			return err
		}
		if err := addHistory(ctx, tx, a.WorkspaceID, a.Brand, "video", cur.ID, "gone_back", in.Note, a.ActorID, map[string]any{"version": number, "reason": in.Reason, "try_again": in.TryAgain}); err != nil {
			return err
		}
		if v, err = findVideo(ctx, tx, a.WorkspaceID, a.Brand, cur.ID, true); err != nil {
			return err
		}
		data := videoData(v)
		data["version"], data["try_again"], data["has_reason"] = number, in.TryAgain, in.Reason != "" || in.Note != ""
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "video.fix_discarded", "video", cur.ID, data); err != nil {
			return err
		}
		if !in.TryAgain {
			return nil
		}
		idx := slices.IndexFunc(cur.Versions, func(ver videoVersion) bool { return ver.Number == number })
		if idx < 0 || cur.Versions[idx].Edit == nil {
			return errConflictMsg("what this fix changed is no longer known, so it can't be made again; pick the words and fix them again")
		}
		e := cur.Versions[idx].Edit
		again := videoEditRequest{Action: e.Action, From: &e.From, To: &e.To, Note: in.Note}
		if e.Action == "redo" {
			again.Text, again.Say = e.Text, cmp.Or(in.Say, e.Say)
		}
		v, err = startVideoEdit(ctx, tx, a, v, again)
		return err
	})
	if s.assetError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// restoreVideoVersion makes an earlier version, or a fix that was turned
// down, the video again. The version it replaces is kept.
func (s *Server) restoreVideoVersion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	number, convErr := strconv.Atoi(r.PathValue("number"))
	var v videoView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(r.PathValue("video")) || convErr != nil {
			return errNotFound
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM brand_videos WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, r.PathValue("video"), a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		cur, err := findVideo(ctx, tx, a.WorkspaceID, a.Brand, r.PathValue("video"), true)
		if err != nil {
			return err
		}
		idx := slices.IndexFunc(cur.Versions, func(ver videoVersion) bool { return ver.Number == number })
		switch {
		case idx < 0:
			return errNotFound
		case cur.Status != "ready":
			return errConflictMsg("a version is gone back to once the video is ready; this one is " + cur.Status)
		case cur.WaitingVersion != nil:
			return errConflictMsg("a fix to this video waits for your answer: keep it or go back first")
		case cur.Versions[idx].State == "current":
			return errConflictMsg("the video is this version already")
		case cur.Versions[idx].FinalAssetID == nil:
			return errConflictMsg("the file of this version was deleted, so it can't be gone back to")
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_video_versions SET state = 'before' WHERE video_id = $1 AND state = 'current'`, cur.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_video_versions SET state = 'current', decided_by = $3, decided_at = now() WHERE video_id = $1 AND number = $2`, cur.ID, number, actorOrNil(a)); err != nil {
			return err
		}
		if err := showVideoVersion(ctx, tx, cur.ID, number); err != nil {
			return err
		}
		if err := addHistory(ctx, tx, a.WorkspaceID, a.Brand, "video", cur.ID, "version_restored", "", a.ActorID, map[string]any{"version": number}); err != nil {
			return err
		}
		if v, err = findVideo(ctx, tx, a.WorkspaceID, a.Brand, cur.ID, true); err != nil {
			return err
		}
		data := videoData(v)
		data["version"] = number
		return record(ctx, tx, &a.principal, a.WorkspaceID, "video.version_restored", "video", cur.ID, data)
	})
	if s.assetError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- Like and not this ----

type ratingRequest struct {
	// liked, not_this, or empty to take the answer back.
	Rating string `json:"rating"`
	Reason string `json:"reason"`
}

func (in *ratingRequest) check() error {
	in.Rating, in.Reason = strings.TrimSpace(in.Rating), strings.TrimSpace(in.Reason)
	if !slices.Contains([]string{"", "liked", "not_this"}, in.Rating) {
		return errInvalid("rating is liked, not_this, or empty to take it back")
	}
	if len(in.Reason) > 500 {
		return errInvalid("reason is at most 500 characters")
	}
	if in.Rating == "" {
		in.Reason = ""
	}
	return nil
}

// kind is what the history calls the answer.
func (in ratingRequest) kind() string {
	if in.Rating == "" {
		return "rating_cleared"
	}
	return in.Rating
}

func (s *Server) rateBrandVideo(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in ratingRequest
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	var v videoView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := in.check(); err != nil {
			return err
		}
		cur, err := findVideo(ctx, tx, a.WorkspaceID, a.Brand, r.PathValue("video"), false)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_videos SET rating = $2, rating_reason = $3, rated_by = $4, rated_at = CASE WHEN $2 = '' THEN NULL ELSE now() END, updated_at = now() WHERE id = $1`,
			cur.ID, in.Rating, in.Reason, actorOrNil(a)); err != nil {
			return err
		}
		if err := addHistory(ctx, tx, a.WorkspaceID, a.Brand, "video", cur.ID, in.kind(), in.Reason, a.ActorID, nil); err != nil {
			return err
		}
		if v, err = findVideo(ctx, tx, a.WorkspaceID, a.Brand, cur.ID, true); err != nil {
			return err
		}
		data := videoData(v)
		data["rating"], data["has_reason"] = in.Rating, in.Reason != ""
		return record(ctx, tx, &a.principal, a.WorkspaceID, "video.rated", "video", cur.ID, data)
	})
	if s.generationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) rateCreative(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in ratingRequest
	if !decode(w, r, &in) {
		return
	}
	if err := in.check(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	s.changeCreative(w, r, "creative.rated", func(ctx context.Context, tx pgx.Tx, cur creativeView) error {
		if _, err := tx.Exec(ctx, `UPDATE creatives SET rating = $2, rating_reason = $3, rated_by = $4, rated_at = CASE WHEN $2 = '' THEN NULL ELSE now() END, updated_at = now() WHERE id = $1`,
			cur.ID, in.Rating, in.Reason, actorOrNil(a)); err != nil {
			return err
		}
		return addHistory(ctx, tx, a.WorkspaceID, a.Brand, "creative", cur.ID, in.kind(), in.Reason, a.ActorID, nil)
	})
}

// ---- Variations ----

// firstLine is where a script's first sentence ends.
func firstLine(script string) int {
	for i, c := range script {
		if (c == '.' || c == '!' || c == '?') && (i+1 == len(script) || script[i+1] == ' ' || script[i+1] == '\n') {
			return i + 1
		}
		if c == '\n' {
			return i
		}
	}
	return len(script)
}

// varyBrandVideo makes more of a video that works: copies of it with one
// thing changed, each a video of its own that points back at the first.
func (s *Server) varyBrandVideo(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needFiles(w, r) {
		return
	}
	var in struct {
		// first_line, direction, scene or voice.
		Change string `json:"change"`
		// One variation per value: the new first line, the new direction,
		// the id of the scene picture, or the id of the voice.
		Values []string `json:"values"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	var list []videoView
	err := func() error {
		if !slices.Contains([]string{"first_line", "direction", "scene", "voice"}, in.Change) {
			return errInvalid("change is the one thing that differs: first_line, direction, scene or voice")
		}
		if len(in.Values) == 0 || len(in.Values) > maxVariations {
			return errInvalid("values holds 1 to " + strconv.Itoa(maxVariations) + " values, one per variation")
		}
		for i := range in.Values {
			in.Values[i] = strings.TrimSpace(in.Values[i])
			if in.Values[i] == "" || len(in.Values[i]) > 10000 {
				return errInvalid("each value is 1 to 10,000 characters")
			}
		}
		if in.Change == "scene" {
			for i, id := range in.Values {
				refs, err := s.readyReferences(ctx, a.WorkspaceID, a.Brand, connectors.MediaVideo, []generationRef{{AssetID: id}})
				if err != nil {
					return err
				}
				in.Values[i] = refs[0].AssetID
			}
		}
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			cur, err := findVideo(ctx, tx, a.WorkspaceID, a.Brand, r.PathValue("video"), false)
			if err != nil {
				return err
			}
			var today int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM brand_videos WHERE workspace_id = $1 AND brand_id = $2 AND created_at > now() - interval '24 hours'`,
				a.WorkspaceID, a.Brand).Scan(&today); err != nil {
				return err
			}
			if today+len(in.Values) > maxGenerationsPerDay/4 {
				return errConflictMsg("a brand makes at most " + strconv.Itoa(maxGenerationsPerDay/4) + " talking videos in 24 hours; try again later")
			}
			// Variations of a variation join the first video's family.
			root := cur.ID
			if cur.VariationOf != nil {
				root = *cur.VariationOf
			}
			for i, val := range in.Values {
				script, direction, scene, settings := cur.Script, cur.Direction, cur.SceneAssetID, cur.Settings
				switch in.Change {
				case "first_line":
					script = strings.TrimSpace(val + " " + strings.TrimSpace(cur.Script[firstLine(cur.Script):]))
					if len(script) > 5000 {
						return errInvalid("with that first line the script is over 5,000 characters")
					}
				case "direction":
					direction = val
				case "scene":
					scene = &in.Values[i]
				case "voice":
					settings.Voice.Voice = val
				}
				name := cmp.Or(cur.Name, "Talking video") + " (variation " + strconv.Itoa(cur.Variations+i+1) + ")"
				if len(name) > 200 {
					name = name[len(name)-200:]
				}
				raw, _ := json.Marshal(settings)
				v, err := insertVideo(ctx, tx, a, name, script, direction, scene, raw, actorOrNil(a), &root, map[string]any{"change": in.Change, "value": val, "from": cur.ID})
				if err != nil {
					return err
				}
				list = append(list, v)
			}
			if err := addHistory(ctx, tx, a.WorkspaceID, a.Brand, "video", cur.ID, "variation_made", "", a.ActorID, map[string]any{"change": in.Change, "count": len(list)}); err != nil {
				return err
			}
			data := videoData(cur)
			data["change"], data["count"] = in.Change, len(list)
			return record(ctx, tx, &a.principal, a.WorkspaceID, "video.variations_requested", "video", cur.ID, data)
		})
	}()
	if s.generationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": list})
}

// ---- History ----

type historyEntry struct {
	ID string `json:"id"`
	// made, fix_asked, fix_ready, fix_failed, fix_kept, gone_back,
	// version_restored, liked, not_this, rating_cleared, variation_made,
	// words_changed, approved, rejected.
	Kind string `json:"kind"`
	// What the person said, in their words.
	Said string         `json:"said"`
	Data map[string]any `json:"data"`
	// Who did it; null for something Outflare did by itself.
	ActorID   *string   `json:"actor_id"`
	ActorName *string   `json:"actor_name"`
	CreatedAt time.Time `json:"created_at"`
}

// addHistory writes one line of a creative's history. An empty actorID is
// something Outflare did by itself.
func addHistory(ctx context.Context, tx pgx.Tx, ws, brand, subjectType, subjectID, kind, said, actorID string, data map[string]any) error {
	raw := []byte("{}")
	if data != nil {
		raw, _ = json.Marshal(data)
	}
	if len(said) > 2000 {
		said = said[:2000]
	}
	_, err := tx.Exec(ctx, `INSERT INTO creative_history (workspace_id, brand_id, subject_type, subject_id, kind, said, data, actor_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::uuid, clock_timestamp())`, ws, brand, subjectType, subjectID, kind, said, raw, actorID)
	return err
}

// editSummary is what a history line keeps of an edit: which words, and
// what was asked for them.
func editSummary(e videoEdit, words []connectors.SpokenWord) map[string]any {
	d := map[string]any{"action": e.Action, "from": e.From, "to": e.To}
	if e.From >= 0 && e.To < len(words) && e.From <= e.To {
		d["words"] = joinWords(words[e.From : e.To+1])
	}
	if e.Action == "redo" {
		d["text"] = e.Text
		if e.Say != "" {
			d["say"] = e.Say
		}
	}
	return d
}

func (s *Server) history(w http.ResponseWriter, r *http.Request, subjectType, id string) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var list []historyEntry
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		if !validUUID(id) {
			return errNotFound
		}
		table := map[string]string{"video": "brand_videos", "creative": "creatives"}[subjectType]
		var found bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1 AND workspace_id = $2 AND brand_id = $3)`, id, a.WorkspaceID, a.Brand).Scan(&found); err != nil {
			return err
		}
		if !found {
			return errNotFound
		}
		rows, err := tx.Query(ctx, `SELECT h.id::text, h.kind, h.said, h.data, h.actor_id::text, NULLIF(ac.display_name, ''), h.created_at
			FROM creative_history h LEFT JOIN actors ac ON ac.id = h.actor_id
			WHERE h.workspace_id = $1 AND h.brand_id = $2 AND h.subject_type = $3 AND h.subject_id = $4 ORDER BY h.created_at DESC, h.id LIMIT $5`,
			a.WorkspaceID, a.Brand, subjectType, id, historyShown)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (historyEntry, error) {
			var h historyEntry
			var data []byte
			err := row.Scan(&h.ID, &h.Kind, &h.Said, &data, &h.ActorID, &h.ActorName, &h.CreatedAt)
			h.Data = map[string]any{}
			_ = json.Unmarshal(data, &h.Data)
			return h, err
		})
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list)})
}

func (s *Server) videoHistory(w http.ResponseWriter, r *http.Request) {
	s.history(w, r, "video", r.PathValue("video"))
}

func (s *Server) creativeHistory(w http.ResponseWriter, r *http.Request) {
	s.history(w, r, "creative", r.PathValue("creative"))
}

// ---- Lessons ----

// lessonView is something a person taught the brand's creatives.
type lessonView struct {
	ID string `json:"id"`
	// say: how to spell a word for a voice. rule: a line added to prompts.
	Kind string `json:"kind"`
	Word string `json:"word"`
	Say  string `json:"say"`
	Rule string `json:"rule"`
	// Which creatives it is for: all, image or video.
	AppliesTo string `json:"applies_to"`
	// The video or creative it was learned on.
	SourceType string    `json:"source_type"`
	SourceID   *string   `json:"source_id"`
	CreatedBy  *string   `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
}

func brandLessons(ctx context.Context, q pgQuerier, ws, brand string) ([]lessonView, error) {
	rows, err := q.Query(ctx, `SELECT id::text, kind, word, say, rule, applies_to, source_type, source_id::text, created_by::text, created_at
		FROM creative_lessons WHERE workspace_id = $1 AND brand_id = $2 AND removed_at IS NULL ORDER BY created_at DESC, id`, ws, brand)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (lessonView, error) {
		var l lessonView
		err := row.Scan(&l.ID, &l.Kind, &l.Word, &l.Say, &l.Rule, &l.AppliesTo, &l.SourceType, &l.SourceID, &l.CreatedBy, &l.CreatedAt)
		return l, err
	})
}

func (s *Server) listCreativeLessons(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := brandLessons(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list)})
}

// insertLesson adds a lesson. The same word taught again replaces the
// earlier spelling.
func insertLesson(ctx context.Context, tx pgx.Tx, a *access, l lessonView) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM creative_lessons WHERE workspace_id = $1 AND brand_id = $2 AND removed_at IS NULL`, a.WorkspaceID, a.Brand).Scan(&count); err != nil {
		return err
	}
	if count >= maxLessons {
		return errConflictMsg("a brand keeps at most " + strconv.Itoa(maxLessons) + " lessons; remove some first")
	}
	if l.Kind == "say" {
		if _, err := tx.Exec(ctx, `UPDATE creative_lessons SET removed_at = now() WHERE workspace_id = $1 AND brand_id = $2 AND kind = 'say' AND lower(word) = lower($3) AND removed_at IS NULL`,
			a.WorkspaceID, a.Brand, l.Word); err != nil {
			return err
		}
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO creative_lessons (workspace_id, brand_id, kind, word, say, rule, applies_to, source_type, source_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id::text`,
		a.WorkspaceID, a.Brand, l.Kind, l.Word, l.Say, l.Rule, l.AppliesTo, l.SourceType, l.SourceID, actorOrNil(a)).Scan(&id); err != nil {
		return err
	}
	return record(ctx, tx, &a.principal, a.WorkspaceID, "creative_lesson.added", "creative_lesson", id, map[string]any{"kind": l.Kind, "applies_to": l.AppliesTo, "source_type": l.SourceType})
}

func (s *Server) addCreativeLesson(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Kind       string `json:"kind"`
		Word       string `json:"word"`
		Say        string `json:"say"`
		Rule       string `json:"rule"`
		AppliesTo  string `json:"applies_to"`
		SourceType string `json:"source_type"`
		SourceID   string `json:"source_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	l := lessonView{Kind: in.Kind, Word: strings.TrimSpace(in.Word), Say: strings.TrimSpace(in.Say), Rule: strings.TrimSpace(in.Rule), AppliesTo: in.AppliesTo, SourceType: in.SourceType}
	var list []lessonView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		switch l.Kind {
		case "say":
			if l.Word == "" || len(l.Word) > 100 || l.Say == "" || len(l.Say) > 200 || l.Rule != "" {
				return errInvalid("a say lesson has word (as it is written, at most 100 characters) and say (how to spell it for a voice, at most 200), and no rule")
			}
			l.AppliesTo = "video"
		case "rule":
			if l.Rule == "" || len(l.Rule) > 500 || l.Word != "" || l.Say != "" {
				return errInvalid("a rule lesson has rule (a line added to every prompt, at most 500 characters), and no word or say")
			}
			if l.AppliesTo == "" {
				l.AppliesTo = "all"
			}
			if !slices.Contains([]string{"all", "image", "video"}, l.AppliesTo) {
				return errInvalid("applies_to is all, image or video")
			}
		default:
			return errInvalid("kind is say (how to say a word) or rule (a line added to every prompt)")
		}
		if l.SourceType != "" || in.SourceID != "" {
			table := map[string]string{"video": "brand_videos", "creative": "creatives"}[l.SourceType]
			if table == "" || !validUUID(in.SourceID) {
				return errInvalid("source_type is video or creative, with source_id the id of the one this was learned on")
			}
			var found bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1 AND workspace_id = $2 AND brand_id = $3)`, in.SourceID, a.WorkspaceID, a.Brand).Scan(&found); err != nil {
				return err
			}
			if !found {
				return errInvalid("source_id is not a " + l.SourceType + " of this brand")
			}
			l.SourceID = &in.SourceID
		}
		if err := insertLesson(ctx, tx, a, l); err != nil {
			return err
		}
		var err error
		list, err = brandLessons(ctx, tx, a.WorkspaceID, a.Brand)
		return err
	})
	if s.generationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, list[0])
}

func (s *Server) removeCreativeLesson(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id := r.PathValue("lesson")
		if !validUUID(id) {
			return errNotFound
		}
		var kind string
		err := tx.QueryRow(ctx, `UPDATE creative_lessons SET removed_at = now() WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 AND removed_at IS NULL RETURNING kind`,
			id, a.WorkspaceID, a.Brand).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "creative_lesson.removed", "creative_lesson", id, map[string]any{"kind": kind})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sayAs spells text for a voice the way the brand taught: each word of a
// say lesson, wherever it stands as a whole word, becomes its spelling.
func (s *Server) sayAs(ctx context.Context, v videoView, text string) string {
	lessons, err := brandLessons(ctx, s.pool, v.ws, v.BrandID)
	if err != nil {
		s.log.Warn("reading a brand's lessons", "video_id", v.ID, "error", err)
		return text
	}
	return applySayLessons(lessons, text)
}

func applySayLessons(lessons []lessonView, text string) string {
	var say []lessonView
	for _, l := range lessons {
		if l.Kind == "say" && l.Word != "" {
			say = append(say, l)
		}
	}
	// Longer words first, so "Outflare Cloud" wins over "Outflare".
	sort.SliceStable(say, func(i, j int) bool { return len(say[i].Word) > len(say[j].Word) })
	for _, l := range say {
		re, err := regexp.Compile(`(?i)(^|[^\pL\pN])` + regexp.QuoteMeta(l.Word) + `($|[^\pL\pN])`)
		if err != nil {
			continue
		}
		// Twice: a match takes the character after it, which may start the next.
		for range 2 {
			text = re.ReplaceAllString(text, "${1}"+strings.ReplaceAll(l.Say, "$", "$$")+"${2}")
		}
	}
	return text
}

// withRules adds the brand's rules for videos to what a video model is
// told, and what the person said is wrong with the part being made again.
func (s *Server) withRules(ctx context.Context, v videoView, direction, note string) string {
	lessons, err := brandLessons(ctx, s.pool, v.ws, v.BrandID)
	if err != nil {
		s.log.Warn("reading a brand's lessons", "video_id", v.ID, "error", err)
	}
	out := direction
	if rules := lessonRules(lessons, "video"); rules != "" {
		out = strings.TrimSpace(out + "\n\n" + rules)
	}
	if note != "" {
		out = strings.TrimSpace(out + "\n\nThe last take of this part was turned down. What was wrong: " + note)
	}
	return out
}

// lessonRules is the brand's rules for one kind of creative, as lines for
// a prompt; empty when there are none.
func lessonRules(lessons []lessonView, kind string) string {
	var lines []string
	for i := len(lessons) - 1; i >= 0; i-- {
		if l := lessons[i]; l.Kind == "rule" && (l.AppliesTo == "all" || l.AppliesTo == kind) {
			lines = append(lines, "- "+l.Rule)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Always:\n" + strings.Join(lines, "\n")
}
