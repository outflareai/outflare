package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/assets"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/media"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/videofx"
)

// Talking videos (docs/brand-videos.md): a person in a scene saying a
// script, made stage by stage on the brand's own accounts.
//
//	voice    the script read out (ElevenLabs)
//	words    gaps closed, then every word with its time (ElevenLabs)
//	render   the video, made to that voice from the scene picture (fal.ai)
//	upscale  the cheap small render made large (fal.ai), when asked for
//	finish   the look of a phone camera and captions (ffmpeg on the server)
//
// Each stage is a step that keeps what it made and, for a provider, the
// generation with its recipe. An edit picks words of the finished video:
// "remove" cuts them out; "redo" reads them again, renders only that
// stretch from the frame it starts on, and stitches it in. Outflare holds
// no key for any provider: without the brand's connections nothing runs.

const (
	jobVideo = "video.advance"
	// videoGiveUp ends a video that is still not made.
	videoGiveUp   = 3 * time.Hour
	videosShown   = 200
	videoMaxWords = 1500
	// videoPartMin is the shortest stretch a video model renders: an edit
	// of less is widened to its neighbours.
	videoPartMin = 2.0
	// videoVoiceMax is how long a voice-over a video model takes in one
	// request, unless the video's settings say otherwise.
	videoVoiceMax = 30.0
	// videoGap is the longest silence left between words when the voice
	// is tightened.
	videoGap = 0.3
)

// VideoTool finishes and edits videos on the server (videofx.Tool, which
// runs ffmpeg). Nil on a server without ffmpeg: videos are still made,
// without the finish and without edits.
type VideoTool interface {
	Probe(ctx context.Context, data []byte) (videofx.Info, error)
	Tighten(ctx context.Context, audio []byte, gap float64) ([]byte, error)
	Finish(ctx context.Context, video []byte, look videofx.Look) ([]byte, error)
	Cut(ctx context.Context, video []byte, from, to float64) ([]byte, error)
	Splice(ctx context.Context, base, part []byte, from, to float64) ([]byte, error)
	Frame(ctx context.Context, video []byte, at float64) ([]byte, error)
}

func (s *Server) routesBrandVideos(mux *http.ServeMux) {
	read, manage := auth.ScopeContentRead, auth.ScopeContentManage
	base := "/v1/brands/{brand}/videos"
	mux.HandleFunc("GET "+base, s.onBrand(read, s.listBrandVideos))
	mux.HandleFunc("POST "+base, s.onBrand(manage, s.createBrandVideo))
	mux.HandleFunc("GET "+base+"/{video}", s.onBrand(read, s.getBrandVideo))
	mux.HandleFunc("PATCH "+base+"/{video}", s.onBrand(manage, s.updateBrandVideo))
	mux.HandleFunc("DELETE "+base+"/{video}", s.onBrand(manage, s.deleteBrandVideo))
	mux.HandleFunc("POST "+base+"/{video}/edits", s.onBrand(manage, s.editBrandVideo))
	mux.HandleFunc("POST "+base+"/{video}/retry", s.onBrand(manage, s.retryBrandVideo))
}

func (s *Server) registerBrandVideoJobs(q *jobs.Queue) {
	q.Handle(jobVideo, s.videoJob)
}

// videoModel is a provider's model with its own settings.
type videoModel struct {
	Model   string         `json:"model"`
	Options map[string]any `json:"options,omitempty"`
}

// videoSettings names the model of every stage. None has a default:
// providers rename and retire models, so the choice is the brand's.
type videoSettings struct {
	// The ElevenLabs voice that reads the script, and the speech model.
	Voice struct {
		videoModel
		Voice string `json:"voice"`
	} `json:"voice"`
	// The ElevenLabs speech to text model that times every word.
	Words struct {
		Model string `json:"model"`
	} `json:"words"`
	// The fal.ai video model. SceneAs and AudioAs are its own names for
	// the picture and sound inputs (image_urls and audio_urls when left
	// out); MaxSeconds is the longest voice-over it takes (30).
	Video struct {
		videoModel
		SceneAs    string  `json:"scene_as,omitempty"`
		AudioAs    string  `json:"audio_as,omitempty"`
		MaxSeconds float64 `json:"max_seconds,omitempty"`
	} `json:"video"`
	// The fal.ai model that makes the render large. Left out, the video
	// stays the size it was rendered at.
	Upscale *struct {
		videoModel
		VideoAs string `json:"video_as,omitempty"`
	} `json:"upscale,omitempty"`
	// Work done on the server with ffmpeg.
	Finish struct {
		// Tighten closes long silences in the voice-over before the
		// video is made to it.
		Tighten bool `json:"tighten"`
		// PhoneLook gives the picture the texture of a phone camera.
		PhoneLook bool `json:"phone_look"`
		// Captions burns the words in as they are said.
		Captions bool `json:"captions"`
	} `json:"finish"`
}

func (st *videoSettings) check() error {
	clean := func(m *videoModel, what string) error {
		m.Model = strings.TrimSpace(m.Model)
		if m.Model == "" || len(m.Model) > connectors.MediaModelMax || strings.ContainsAny(m.Model, " \t\r\n?#\\") {
			return errInvalid("settings." + what + ".model is the provider's id of the model to use")
		}
		if len(m.Options) > 50 {
			return errInvalid("settings." + what + ".options holds at most 50 settings")
		}
		return nil
	}
	if err := clean(&st.Voice.videoModel, "voice"); err != nil {
		return err
	}
	st.Voice.Voice = strings.TrimSpace(st.Voice.Voice)
	if st.Voice.Voice == "" {
		return errInvalid("settings.voice.voice is the id of the ElevenLabs voice that reads the script (GET .../generations/voices lists them)")
	}
	words := videoModel{Model: st.Words.Model}
	if err := clean(&words, "words"); err != nil {
		return err
	}
	st.Words.Model = words.Model
	if err := clean(&st.Video.videoModel, "video"); err != nil {
		return err
	}
	if st.Upscale != nil {
		if err := clean(&st.Upscale.videoModel, "upscale"); err != nil {
			return err
		}
	}
	if st.Video.MaxSeconds < 0 || st.Video.MaxSeconds > 600 {
		return errInvalid("settings.video.max_seconds is the longest voice-over the video model takes, in seconds")
	}
	return nil
}

// videoEdit is the edit a video is going through.
type videoEdit struct {
	// remove or redo.
	Action string `json:"action"`
	// The words picked, by position, first and last.
	From int `json:"from"`
	To   int `json:"to"`
	// For redo: the words to show in their place, and how to spell them
	// for the voice when it reads them wrong.
	Text string `json:"text"`
	Say  string `json:"say"`
	// What is wrong, in the person's words: added to what the video model
	// is told for the stretch, and kept in the video's history.
	Note string `json:"note,omitempty"`
	// What the edit covers once widened to whole words and gaps.
	FirstWord int     `json:"first_word"`
	LastWord  int     `json:"last_word"`
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
	// What the edit has made so far.
	VoiceAssetID string                  `json:"voice_asset_id,omitempty"`
	PartAssetID  string                  `json:"part_asset_id,omitempty"`
	Words        []connectors.SpokenWord `json:"words,omitempty"`
}

type videoStep struct {
	Round int    `json:"round"`
	Stage string `json:"stage"`
	// running, done, failed or skipped.
	Status       string         `json:"status"`
	GenerationID *string        `json:"generation_id"`
	AssetID      *string        `json:"asset_id"`
	Detail       map[string]any `json:"detail"`
	CreatedAt    time.Time      `json:"created_at"`
	FinishedAt   *time.Time     `json:"finished_at"`
}

type videoView struct {
	ID           string        `json:"id"`
	BrandID      string        `json:"brand_id"`
	Name         string        `json:"name"`
	Script       string        `json:"script"`
	Direction    string        `json:"direction"`
	SceneAssetID *string       `json:"scene_asset_id"`
	Settings     videoSettings `json:"settings"`
	// working, ready or failed.
	Status string `json:"status"`
	// voice, words, render, upscale, finish, or an edit's stage; done
	// once ready.
	Stage string `json:"stage"`
	// How many edits it has been through.
	Round     int        `json:"round"`
	Edit      *videoEdit `json:"edit"`
	ErrorCode *string    `json:"error_code"`
	Error     *string    `json:"error"`
	// The files: the voice-over, the video as rendered, as it stands
	// before the finish, and finished.
	VoiceAssetID  *string `json:"voice_asset_id"`
	RenderAssetID *string `json:"render_asset_id"`
	BaseAssetID   *string `json:"base_asset_id"`
	FinalAssetID  *string `json:"final_asset_id"`
	// Every word and when it is said, in seconds: what an edit picks from.
	Words   []connectors.SpokenWord `json:"words"`
	Seconds float64                 `json:"seconds"`
	Notes   []string                `json:"notes"`
	// A fix that is made and waits for keep or go back: the number of its
	// version. Until then the video is as it was before the fix.
	WaitingVersion *int `json:"waiting_version"`
	// The video this one is a variation of, and the one thing changed.
	VariationOf     *string        `json:"variation_of"`
	VariationChange map[string]any `json:"variation_change"`
	// How many variations were made of this one.
	Variations int `json:"variations"`
	// liked, not_this or empty, and why.
	Rating       string     `json:"rating"`
	RatingReason string     `json:"rating_reason"`
	RatedAt      *time.Time `json:"rated_at"`
	// Every version, newest first. Only on one video, not in lists.
	Versions []videoVersion `json:"versions"`
	// What each stage did, oldest first. Only on one video, not in lists.
	Steps      []videoStep `json:"steps"`
	CreatedBy  *string     `json:"created_by"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
	FinishedAt *time.Time  `json:"finished_at"`

	ws string
}

const videoColumns = `v.id::text, v.brand_id::text, v.name, v.script, v.direction, v.scene_asset_id::text, v.settings, v.status, v.stage, v.round, v.edit,
	NULLIF(v.error_code, ''), NULLIF(v.error, ''), v.voice_asset_id::text, v.render_asset_id::text, v.base_asset_id::text, v.final_asset_id::text,
	v.words, v.notes, v.created_by::text, v.created_at, v.updated_at, v.finished_at, v.workspace_id::text,
	v.waiting_version, v.variation_of::text, v.variation_change, v.rating, v.rating_reason, v.rated_at,
	(SELECT count(*) FROM brand_videos c WHERE c.variation_of = v.id)`

func scanVideo(row pgx.CollectableRow) (videoView, error) {
	var v videoView
	var settings, edit, words, notes, change []byte
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.Script, &v.Direction, &v.SceneAssetID, &settings, &v.Status, &v.Stage, &v.Round, &edit,
		&v.ErrorCode, &v.Error, &v.VoiceAssetID, &v.RenderAssetID, &v.BaseAssetID, &v.FinalAssetID,
		&words, &notes, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt, &v.FinishedAt, &v.ws,
		&v.WaitingVersion, &v.VariationOf, &change, &v.Rating, &v.RatingReason, &v.RatedAt, &v.Variations)
	v.VariationChange, v.Versions = map[string]any{}, []videoVersion{}
	_ = json.Unmarshal(change, &v.VariationChange)
	_ = json.Unmarshal(settings, &v.Settings)
	if len(edit) > 0 && string(edit) != "null" {
		v.Edit = &videoEdit{}
		_ = json.Unmarshal(edit, v.Edit)
	}
	v.Words, v.Notes, v.Steps = []connectors.SpokenWord{}, []string{}, []videoStep{}
	_ = json.Unmarshal(words, &v.Words)
	_ = json.Unmarshal(notes, &v.Notes)
	if n := len(v.Words); n > 0 {
		v.Seconds = v.Words[n-1].End
	}
	return v, err
}

type videoQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func findVideo(ctx context.Context, q videoQuerier, ws, brand, id string, steps bool) (videoView, error) {
	if !validUUID(id) {
		return videoView{}, errNotFound
	}
	rows, err := q.Query(ctx, `SELECT `+videoColumns+` FROM brand_videos v WHERE v.id = $1 AND v.workspace_id = $2 AND ($3 = '' OR v.brand_id = $3::uuid)`, id, ws, brand)
	if err != nil {
		return videoView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanVideo)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err != nil || !steps {
		return v, err
	}
	rows, err = q.Query(ctx, `SELECT round, stage, status, generation_id::text, asset_id::text, detail, created_at, finished_at
		FROM brand_video_steps WHERE video_id = $1 AND workspace_id = $2 ORDER BY created_at, id`, id, ws)
	if err != nil {
		return v, err
	}
	v.Steps, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (videoStep, error) {
		var st videoStep
		var detail []byte
		err := row.Scan(&st.Round, &st.Stage, &st.Status, &st.GenerationID, &st.AssetID, &detail, &st.CreatedAt, &st.FinishedAt)
		st.Detail = map[string]any{}
		_ = json.Unmarshal(detail, &st.Detail)
		return st, err
	})
	v.Steps = nonNil(v.Steps)
	if err != nil {
		return v, err
	}
	v.Versions, err = videoVersions(ctx, q, ws, id)
	return v, err
}

// videoData is what a video's events carry: never its script or name.
func videoData(v videoView) map[string]any {
	d := map[string]any{"round": v.Round, "stage": v.Stage, "words": len(v.Words), "seconds": int(v.Seconds),
		"upscale": v.Settings.Upscale != nil, "phone_look": v.Settings.Finish.PhoneLook, "captions": v.Settings.Finish.Captions, "scene": v.SceneAssetID != nil}
	if v.Edit != nil {
		d["edit"] = v.Edit.Action
	}
	return d
}

func (s *Server) listBrandVideos(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains([]string{"working", "ready", "failed"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is one of working, ready, failed")
		return
	}
	// family: a video with the variations made of it.
	family := r.URL.Query().Get("family")
	if family != "" && !validUUID(family) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "family is the id of a video: it lists that video and its variations")
		return
	}
	var list []videoView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT `+videoColumns+` FROM brand_videos v WHERE v.workspace_id = $1 AND v.brand_id = $2 AND ($3 = '' OR v.status = $3)
			AND ($5 = '' OR v.variation_of = NULLIF($5, '')::uuid OR v.id = NULLIF($5, '')::uuid)
			ORDER BY v.created_at DESC, v.id LIMIT $4`, a.WorkspaceID, a.Brand, status, videosShown, family)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanVideo)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// finishing: this server can tighten, finish and edit videos (ffmpeg).
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list), "storage": s.files != nil, "finishing": s.video != nil})
}

func (s *Server) getBrandVideo(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := findVideo(r.Context(), s.pool, a.WorkspaceID, a.Brand, r.PathValue("video"), true)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type videoRequest struct {
	Name         string        `json:"name"`
	Script       string        `json:"script"`
	Direction    string        `json:"direction"`
	SceneAssetID string        `json:"scene_asset_id"`
	Settings     videoSettings `json:"settings"`
}

// createBrandVideo takes a script and the settings of each stage, and
// starts making the video. Nothing is sent to a provider here.
func (s *Server) createBrandVideo(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needFiles(w, r) {
		return
	}
	var in videoRequest
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	in.Name, in.Script, in.Direction = strings.TrimSpace(in.Name), strings.TrimSpace(in.Script), strings.TrimSpace(in.Direction)
	var scene *string
	err := func() error {
		switch {
		case in.Script == "" || len(in.Script) > 5000:
			return errInvalid("script is what the person says, at most 5,000 characters")
		case len(in.Direction) > 10000:
			return errInvalid("direction says how the video looks and moves, at most 10,000 characters")
		case len(in.Name) > 200:
			return errInvalid("name is at most 200 characters")
		}
		if err := in.Settings.check(); err != nil {
			return err
		}
		if in.SceneAssetID != "" {
			refs, err := s.readyReferences(ctx, a.WorkspaceID, a.Brand, connectors.MediaVideo, []generationRef{{AssetID: in.SceneAssetID, As: in.Settings.Video.SceneAs}})
			if err != nil {
				return err
			}
			scene = &refs[0].AssetID
		}
		for _, id := range []string{media.ElevenLabsID, media.FalID} {
			conn, err := s.generationConnection(ctx, a.WorkspaceID, a.Brand, id)
			if err != nil {
				return err
			}
			if conn == nil {
				return errConnectorNeeded("a talking video needs your own ElevenLabs account (the voice and the word timings) and your own fal.ai account (the video): connect both under Connectors, Pictures, video and voice. Outflare has no account of its own for this.")
			}
		}
		return nil
	}()
	if s.generationError(w, r, err) {
		return
	}
	settings, _ := json.Marshal(in.Settings)
	var by *string
	if a.ActorID != "" {
		by = &a.ActorID
	}
	var v videoView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var working int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM brand_videos WHERE workspace_id = $1 AND brand_id = $2 AND created_at > now() - interval '24 hours'`,
			a.WorkspaceID, a.Brand).Scan(&working); err != nil {
			return err
		}
		if working >= maxGenerationsPerDay/4 {
			return errConflictMsg("a brand makes at most " + strconv.Itoa(maxGenerationsPerDay/4) + " talking videos in 24 hours; try again later")
		}
		var err error
		v, err = insertVideo(ctx, tx, a, in.Name, in.Script, in.Direction, scene, settings, by, nil, nil)
		return err
	})
	if s.generationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// insertVideo adds a video and starts making it. of and change are set
// for a variation: the video it was made from and the one thing changed.
func insertVideo(ctx context.Context, tx pgx.Tx, a *access, name, script, direction string, scene *string, settings []byte, by, of *string, change map[string]any) (videoView, error) {
	raw := []byte("{}")
	if change != nil {
		raw, _ = json.Marshal(change)
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO brand_videos (workspace_id, brand_id, name, script, direction, scene_asset_id, settings, created_by, variation_of, variation_change)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id::text`, a.WorkspaceID, a.Brand, name, script, direction, scene, settings, by, of, raw).Scan(&id); err != nil {
		return videoView{}, err
	}
	if err := enqueueVideo(ctx, tx, a.WorkspaceID, id, 0); err != nil {
		return videoView{}, err
	}
	v, err := findVideo(ctx, tx, a.WorkspaceID, a.Brand, id, true)
	if err != nil {
		return v, err
	}
	return v, record(ctx, tx, &a.principal, a.WorkspaceID, "video.requested", "video", id, videoData(v))
}

func (s *Server) updateBrandVideo(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Name *string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	var v videoView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findVideo(ctx, tx, a.WorkspaceID, a.Brand, r.PathValue("video"), false)
		if err != nil {
			return err
		}
		if in.Name == nil {
			return errInvalid("name is the only thing to change on a video; to change what is said, use its edits")
		}
		name := strings.TrimSpace(*in.Name)
		if len(name) > 200 {
			return errInvalid("name is at most 200 characters")
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_videos SET name = $2, updated_at = now() WHERE id = $1`, cur.ID, name); err != nil {
			return err
		}
		if v, err = findVideo(ctx, tx, a.WorkspaceID, a.Brand, cur.ID, true); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "video.renamed", "video", cur.ID, videoData(v))
	})
	if s.assetError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// deleteBrandVideo removes a video and its steps. The files it made stay
// with the brand's files, where they can be deleted one by one.
func (s *Server) deleteBrandVideo(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findVideo(ctx, tx, a.WorkspaceID, a.Brand, r.PathValue("video"), false)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM brand_videos WHERE id = $1 AND workspace_id = $2`, cur.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "video.deleted", "video", cur.ID, videoData(cur))
	})
	if s.assetError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// retryBrandVideo starts a failed video again at the stage that failed.
// Stages that were made are kept, and not paid for again.
func (s *Server) retryBrandVideo(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var v videoView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findVideo(ctx, tx, a.WorkspaceID, a.Brand, r.PathValue("video"), false)
		if err != nil {
			return err
		}
		if cur.Status != "failed" {
			return errConflictMsg("only a video that failed can be tried again; this one is " + cur.Status)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM brand_video_steps WHERE video_id = $1 AND round = $2 AND stage = $3 AND status <> 'done'`, cur.ID, cur.Round, cur.Stage); err != nil {
			return err
		}
		// created_at moves so the give-up clock starts again.
		if _, err := tx.Exec(ctx, `UPDATE brand_videos SET status = 'working', error_code = '', error = '', finished_at = NULL, created_at = now(), updated_at = now() WHERE id = $1`, cur.ID); err != nil {
			return err
		}
		if err := enqueueVideo(ctx, tx, a.WorkspaceID, cur.ID, 0); err != nil {
			return err
		}
		if v, err = findVideo(ctx, tx, a.WorkspaceID, a.Brand, cur.ID, true); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "video.retried", "video", cur.ID, videoData(v))
	})
	if s.assetError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type videoEditRequest struct {
	Action string `json:"action"`
	From   *int   `json:"from"`
	To     *int   `json:"to"`
	Text   string `json:"text"`
	Say    string `json:"say"`
	Note   string `json:"note"`
}

// gapBefore and gapAfter are where a stretch of words starts and ends:
// half way into the silence around it, so what is left keeps its pauses.
func gapBefore(words []connectors.SpokenWord, i int) float64 {
	if i <= 0 {
		return 0
	}
	return (words[i-1].End + words[i].Start) / 2
}

func gapAfter(words []connectors.SpokenWord, i int, total float64) float64 {
	if i >= len(words)-1 {
		return max(total, words[len(words)-1].End)
	}
	return (words[i].End + words[i+1].Start) / 2
}

// editBrandVideo changes a finished video by its words: "remove" cuts
// the picked words out; "redo" has them read again (as text, or as say
// spells them for the voice), renders only that stretch and stitches it
// in. The rest of the video is not made, or paid for, again.
func (s *Server) editBrandVideo(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needFiles(w, r) {
		return
	}
	if s.video == nil {
		writeError(w, r, http.StatusConflict, "not_configured", "this server has no ffmpeg, which cuts and stitches videos, so a video can't be edited here. Install ffmpeg on the server.")
		return
	}
	var in videoEditRequest
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	var v videoView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findVideo(ctx, tx, a.WorkspaceID, a.Brand, r.PathValue("video"), false)
		if err != nil {
			return err
		}
		v, err = startVideoEdit(ctx, tx, a, cur, in)
		return err
	})
	if s.assetError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// startVideoEdit begins an edit of a ready video. What it makes waits as
// a new version for the person's keep or go back; the video stays as it
// was until then.
func startVideoEdit(ctx context.Context, tx pgx.Tx, a *access, cur videoView, in videoEditRequest) (videoView, error) {
	var v videoView
	if cur.Status != "ready" || cur.BaseAssetID == nil {
		return v, errConflictMsg("a video is edited once it is ready; this one is " + cur.Status)
	}
	if cur.WaitingVersion != nil {
		return v, errConflictMsg("a fix to this video waits for your answer: keep it or go back first")
	}
	n := len(cur.Words)
	if in.From == nil || in.To == nil || *in.From < 0 || *in.To < *in.From || *in.To >= n {
		return v, errInvalid("from and to are the positions of the first and last word picked, counted from 0 in the video's words (it has " + strconv.Itoa(n) + ")")
	}
	e := videoEdit{Action: in.Action, From: *in.From, To: *in.To, Text: strings.TrimSpace(in.Text), Say: strings.TrimSpace(in.Say), Note: strings.TrimSpace(in.Note), FirstWord: *in.From, LastWord: *in.To}
	if len(e.Text) > 1000 || len(e.Say) > 1000 || len(e.Note) > 1000 {
		return v, errInvalid("text, say and note are at most 1,000 characters")
	}
	stage := ""
	switch e.Action {
	case "remove":
		if e.From == 0 && e.To == n-1 {
			return v, errInvalid("that would remove every word; delete the video instead")
		}
		if e.Text != "" || e.Say != "" {
			return v, errInvalid("text and say are for redo; remove takes only from and to")
		}
		stage = "edit_cut"
	case "redo":
		if e.Text == "" {
			e.Text = joinWords(cur.Words[e.From : e.To+1])
		}
		// A stretch too short for a video model takes in its neighbours.
		for cur.Words[e.LastWord].End-cur.Words[e.FirstWord].Start < videoPartMin && (e.FirstWord > 0 || e.LastWord < n-1) {
			if e.LastWord < n-1 {
				e.LastWord++
			}
			if cur.Words[e.LastWord].End-cur.Words[e.FirstWord].Start < videoPartMin && e.FirstWord > 0 {
				e.FirstWord--
			}
		}
		stage = "edit_voice"
	default:
		return v, errInvalid("action is remove (cut the words out) or redo (read them again and render that stretch again)")
	}
	e.Start, e.End = gapBefore(cur.Words, e.FirstWord), gapAfter(cur.Words, e.LastWord, cur.Seconds)
	// The video as it stands is a version, so it can be gone back to.
	if _, err := currentVideoVersion(ctx, tx, cur); err != nil {
		return v, err
	}
	raw, _ := json.Marshal(e)
	if _, err := tx.Exec(ctx, `UPDATE brand_videos SET status = 'working', stage = $2, round = round + 1, edit = $3, error_code = '', error = '',
		finished_at = NULL, created_at = now(), updated_at = now() WHERE id = $1`, cur.ID, stage, raw); err != nil {
		return v, err
	}
	if err := enqueueVideo(ctx, tx, a.WorkspaceID, cur.ID, 0); err != nil {
		return v, err
	}
	v, err := findVideo(ctx, tx, a.WorkspaceID, a.Brand, cur.ID, true)
	if err != nil {
		return v, err
	}
	if err := addHistory(ctx, tx, a.WorkspaceID, cur.BrandID, "video", cur.ID, "fix_asked", e.Note, a.ActorID, editSummary(e, cur.Words)); err != nil {
		return v, err
	}
	return v, record(ctx, tx, &a.principal, a.WorkspaceID, "video.edit_requested", "video", cur.ID, videoData(v))
}

func joinWords(words []connectors.SpokenWord) string {
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = w.Text
	}
	return strings.Join(parts, " ")
}

// ---- Making it ----

func enqueueVideo(ctx context.Context, q jobs.Querier, ws, id string, wait time.Duration) error {
	j := jobs.NewJob{Kind: jobVideo, WorkspaceID: &ws, Payload: map[string]string{"video_id": id},
		IdempotencyKey: jobVideo + ":" + id + ":" + strconv.FormatInt(time.Now().UnixNano(), 10), MaxAttempts: generationTries}
	if wait > 0 {
		j.RunAt = time.Now().Add(wait)
	}
	_, err := jobs.Enqueue(ctx, q, j)
	return err
}

// wakeVideo moves on the video that waits for a generation, once the
// generation ended.
func (s *Server) wakeVideo(ctx context.Context, generationID string) {
	var ws, id string
	err := s.pool.QueryRow(ctx, `SELECT workspace_id::text, video_id::text FROM brand_video_steps WHERE generation_id = $1`, generationID).Scan(&ws, &id)
	if err != nil {
		return
	}
	if err := enqueueVideo(ctx, s.pool, ws, id, 0); err != nil {
		// The video looks again by itself within a minute.
		s.log.Warn("waking a video", "video_id", id, "error", err)
	}
}

func (s *Server) videoJob(ctx context.Context, job jobs.Job) error {
	ws, id, err := socialPayload(job, "video_id")
	if err != nil {
		return err
	}
	return s.AdvanceVideo(ctx, ws, id)
}

// videoFailure ends a video's making, in words a person can act on.
type videoFailure struct{ code, message string }

func (f *videoFailure) Error() string { return f.message }

func videoFail(code, format string, args ...any) error {
	return &videoFailure{code: code, message: fmt.Sprintf(format, args...)}
}

// AdvanceVideo takes a video as far as it can go: through the stages
// done on the server, up to one that waits for a provider. It is safe to
// call at any time and from several workers: a stage that is already
// running is waited for, never started twice.
func (s *Server) AdvanceVideo(ctx context.Context, ws, id string) error {
	// One worker at a time per video.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('video:' || $1, 0))`, id).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return enqueueVideo(ctx, s.pool, ws, id, 15*time.Second)
	}
	defer func() {
		unlock, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlock, `SELECT pg_advisory_unlock(hashtextextended('video:' || $1, 0))`, id)
	}()

	for range 16 {
		v, err := findVideo(ctx, s.pool, ws, "", id, false)
		if errors.Is(err, errNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if v.Status != "working" {
			return nil
		}
		if s.integrations == nil || s.files == nil {
			return s.failVideo(ctx, v, "not_configured", "this server can no longer make this")
		}
		if time.Since(v.CreatedAt) > videoGiveUp {
			return s.failVideo(ctx, v, "generation_timed_out", "the video was not made in "+strconv.Itoa(int(videoGiveUp.Hours()))+" hours. Try again.")
		}
		began := time.Now()
		next, waiting, err := s.videoStage(ctx, v)
		var f *videoFailure
		switch {
		case errors.As(err, &f):
			return s.failVideo(ctx, v, f.code, f.message)
		case err != nil:
			return err
		case waiting:
			// The generation wakes the video when it ends; this is the
			// look in case that was missed.
			return enqueueVideo(ctx, s.pool, ws, id, time.Minute)
		}
		metrics.VideoStage(v.Stage, time.Since(began))
		if next == "done" {
			return s.readyVideo(ctx, v)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE brand_videos SET stage = $2, updated_at = now() WHERE id = $1 AND status = 'working' AND stage = $3`, id, next, v.Stage); err != nil {
			return err
		}
	}
	return enqueueVideo(ctx, s.pool, ws, id, 0)
}

// videoStage does the video's current stage and says which comes next,
// or that it waits for a provider.
func (s *Server) videoStage(ctx context.Context, v videoView) (next string, waiting bool, err error) {
	st := v.Settings
	switch v.Stage {
	case "voice":
		asset, waiting, err := s.videoGenerate(ctx, v, generationRequest{Kind: connectors.MediaVoice, Provider: media.ElevenLabsID, Model: st.Voice.Model,
			Prompt: s.sayAs(ctx, v, v.Script), Voice: st.Voice.Voice, Options: st.Voice.Options, AssetKind: "other", Name: videoName(v, "voice-over")})
		if waiting || err != nil {
			return "", waiting, err
		}
		return "words", false, s.setVideo(ctx, v, `voice_asset_id = $2`, asset)

	case "words":
		if v.VoiceAssetID == nil {
			return "", false, videoFail("reference_missing", "the voice-over was deleted before the video was made. Try again.")
		}
		voice, err := s.videoFile(ctx, v, *v.VoiceAssetID)
		if err != nil {
			return "", false, err
		}
		notes := v.Notes
		if st.Finish.Tighten {
			if s.video == nil {
				notes = addNote(notes, "The pauses in the voice-over were not shortened: this server has no ffmpeg.")
			} else if tight, err := s.video.Tighten(ctx, voice.Data, videoGap); err != nil {
				notes = addNote(notes, "The pauses in the voice-over could not be shortened, so it is used as it was read.")
			} else {
				id, err := s.keepBrandFile(ctx, v, "voice.mp3", videoName(v, "voice-over, pauses shortened"), tight)
				if err != nil {
					return "", false, err
				}
				if err := s.setVideo(ctx, v, `voice_asset_id = $2`, id); err != nil {
					return "", false, err
				}
				if err := s.videoStepDone(ctx, v, "tighten", &id, map[string]any{"longest_gap_seconds": videoGap}); err != nil {
					return "", false, err
				}
				voice.Data = tight
			}
		}
		t, err := s.videoTranscribe(ctx, v, voice)
		if err != nil {
			return "", false, err
		}
		limit := st.Video.MaxSeconds
		if limit == 0 {
			limit = videoVoiceMax
		}
		if t.Seconds > limit {
			return "", false, videoFail("video_too_long", "the voice-over runs %.0f seconds and the video model takes at most %.0f. Shorten the script, or raise settings.video.max_seconds if your model takes more.", t.Seconds, limit)
		}
		for _, n := range scriptDiffers(v.Script, t.Words) {
			notes = addNote(notes, n)
		}
		words, _ := json.Marshal(t.Words)
		rawNotes, _ := json.Marshal(notes)
		if err := s.setVideo(ctx, v, `words = $2, notes = $3`, words, rawNotes); err != nil {
			return "", false, err
		}
		return "render", false, s.videoStepDone(ctx, v, "words", nil, map[string]any{"words": len(t.Words), "seconds": t.Seconds, "model": st.Words.Model, "language": t.Language})

	case "render":
		if v.VoiceAssetID == nil {
			return "", false, videoFail("reference_missing", "the voice-over was deleted before the video was made. Try again.")
		}
		var refs []generationRef
		if v.SceneAssetID != nil {
			refs = append(refs, generationRef{AssetID: *v.SceneAssetID, As: st.Video.SceneAs})
		}
		refs = append(refs, generationRef{AssetID: *v.VoiceAssetID, As: st.Video.AudioAs})
		asset, waiting, err := s.videoGenerate(ctx, v, generationRequest{Kind: connectors.MediaVideo, Provider: media.FalID, Model: st.Video.Model,
			Prompt: videoPrompt(s.withRules(ctx, v, v.Direction, ""), v.Script), Options: st.Video.Options, References: refs, AssetKind: "other", Name: videoName(v, "render")})
		if waiting || err != nil {
			return "", waiting, err
		}
		return "upscale", false, s.setVideo(ctx, v, `render_asset_id = $2, base_asset_id = $2`, asset)

	case "upscale", "edit_upscale":
		source := v.BaseAssetID
		if v.Stage == "edit_upscale" && v.Edit != nil {
			source = &v.Edit.PartAssetID
		}
		after := map[string]string{"upscale": "finish", "edit_upscale": "edit_stitch"}[v.Stage]
		if st.Upscale == nil {
			return after, false, nil
		}
		if source == nil || *source == "" {
			return "", false, videoFail("reference_missing", "the video to upscale was deleted. Try again.")
		}
		asset, waiting, err := s.videoGenerate(ctx, v, generationRequest{Kind: connectors.MediaVideo, Provider: media.FalID, Model: st.Upscale.Model,
			Options: st.Upscale.Options, References: []generationRef{{AssetID: *source, As: cmp.Or(st.Upscale.VideoAs, "video_url")}}, AssetKind: "other", Name: videoName(v, "upscaled")})
		if waiting || err != nil {
			return "", waiting, err
		}
		if v.Stage == "edit_upscale" {
			e := *v.Edit
			e.PartAssetID = asset
			return after, false, s.setVideoEdit(ctx, v, e)
		}
		return after, false, s.setVideo(ctx, v, `base_asset_id = $2`, asset)

	case "finish":
		if v.BaseAssetID == nil {
			return "", false, videoFail("reference_missing", "the rendered video was deleted before it was finished. Try again.")
		}
		look := videofx.Look{Phone: st.Finish.PhoneLook}
		if st.Finish.Captions {
			look.Captions = captionsOf(v.Words)
		}
		if !look.Phone && len(look.Captions) == 0 {
			return "done", false, s.setVideo(ctx, v, `final_asset_id = $2`, *v.BaseAssetID)
		}
		if s.video == nil {
			notes, _ := json.Marshal(addNote(v.Notes, "The phone-camera look and captions were not added: this server has no ffmpeg. The video is as the model made it."))
			return "done", false, s.setVideo(ctx, v, `final_asset_id = $2, notes = $3`, *v.BaseAssetID, notes)
		}
		base, err := s.videoFile(ctx, v, *v.BaseAssetID)
		if err != nil {
			return "", false, err
		}
		done, err := s.video.Finish(ctx, base.Data, look)
		if err != nil {
			return "", false, videoToolError(err, "the video could not be finished")
		}
		id, err := s.keepBrandFile(ctx, v, "video.mp4", videoName(v, ""), done)
		if err != nil {
			return "", false, err
		}
		if err := s.setVideo(ctx, v, `final_asset_id = $2`, id); err != nil {
			return "", false, err
		}
		return "done", false, s.videoStepDone(ctx, v, "finish", &id, map[string]any{"phone_look": look.Phone, "captions": len(look.Captions)})
	}

	// The stages of an edit.
	e := v.Edit
	if e == nil || v.BaseAssetID == nil || s.video == nil {
		return "", false, videoFail("not_configured", "this edit can no longer be made")
	}
	switch v.Stage {
	case "edit_cut":
		base, err := s.videoFile(ctx, v, *v.BaseAssetID)
		if err != nil {
			return "", false, err
		}
		cut, err := s.video.Cut(ctx, base.Data, e.Start, e.End)
		if err != nil {
			return "", false, videoToolError(err, "the words could not be cut out")
		}
		id, err := s.keepBrandFile(ctx, v, "video.mp4", videoName(v, "edit "+strconv.Itoa(v.Round)), cut)
		if err != nil {
			return "", false, err
		}
		words := spliceWords(v.Words, e.FirstWord, e.LastWord, nil, e.Start, e.End-e.Start, 0)
		raw, _ := json.Marshal(words)
		if err := s.setVideo(ctx, v, `base_asset_id = $2, words = $3`, id, raw); err != nil {
			return "", false, err
		}
		return "finish", false, s.videoStepDone(ctx, v, "edit_cut", &id, map[string]any{"from_seconds": e.Start, "to_seconds": e.End, "words_removed": e.LastWord - e.FirstWord + 1})

	case "edit_voice":
		// The picked words as they should be said, with the neighbours
		// the stretch was widened to as they were.
		said := cmp.Or(e.Say, s.sayAs(ctx, v, e.Text))
		before, after := s.sayAs(ctx, v, joinWords(v.Words[e.FirstWord:e.From])), s.sayAs(ctx, v, joinWords(v.Words[e.To+1:e.LastWord+1]))
		options := map[string]any{}
		for k, val := range st.Voice.Options {
			options[k] = val
		}
		// What comes before and after, so the voice carries on in the same tone.
		if _, ok := options["previous_text"]; !ok && e.FirstWord > 0 {
			options["previous_text"] = joinWords(v.Words[:e.FirstWord])
		}
		if _, ok := options["next_text"]; !ok && e.LastWord < len(v.Words)-1 {
			options["next_text"] = joinWords(v.Words[e.LastWord+1:])
		}
		asset, waiting, err := s.videoGenerate(ctx, v, generationRequest{Kind: connectors.MediaVoice, Provider: media.ElevenLabsID, Model: st.Voice.Model,
			Prompt: strings.TrimSpace(before + " " + said + " " + after), Voice: st.Voice.Voice, Options: options, AssetKind: "other", Name: videoName(v, "voice-over, edit "+strconv.Itoa(v.Round))})
		if waiting || err != nil {
			return "", waiting, err
		}
		ne := *e
		ne.VoiceAssetID = asset
		return "edit_words", false, s.setVideoEdit(ctx, v, ne)

	case "edit_words":
		voice, err := s.videoFile(ctx, v, e.VoiceAssetID)
		if err != nil {
			return "", false, err
		}
		t, err := s.videoTranscribe(ctx, v, voice)
		if err != nil {
			return "", false, err
		}
		// The words shown are the ones meant, timed as they were said.
		shown := strings.Fields(strings.TrimSpace(joinWords(v.Words[e.FirstWord:e.From]) + " " + e.Text + " " + joinWords(v.Words[e.To+1:e.LastWord+1])))
		ne := *e
		ne.Words = timeWords(shown, t)
		if err := s.setVideoEdit(ctx, v, ne); err != nil {
			return "", false, err
		}
		return "edit_render", false, s.videoStepDone(ctx, v, "edit_words", nil, map[string]any{"words": len(ne.Words), "seconds": t.Seconds, "model": st.Words.Model})

	case "edit_render":
		// The new stretch starts on the frame the old one started on.
		var frameID string
		err := s.pool.QueryRow(ctx, `SELECT asset_id::text FROM brand_video_steps WHERE video_id = $1 AND round = $2 AND stage = 'edit_frame' AND asset_id IS NOT NULL`, v.ID, v.Round).Scan(&frameID)
		if errors.Is(err, pgx.ErrNoRows) {
			base, err := s.videoFile(ctx, v, *v.BaseAssetID)
			if err != nil {
				return "", false, err
			}
			frame, err := s.video.Frame(ctx, base.Data, e.Start)
			if err != nil {
				return "", false, videoToolError(err, "the frame the new stretch starts on could not be read")
			}
			if frameID, err = s.keepBrandFile(ctx, v, "frame.png", videoName(v, "frame, edit "+strconv.Itoa(v.Round)), frame); err != nil {
				return "", false, err
			}
			if err := s.videoStepDone(ctx, v, "edit_frame", &frameID, map[string]any{"at_seconds": e.Start}); err != nil {
				return "", false, err
			}
		} else if err != nil {
			return "", false, err
		}
		refs := []generationRef{{AssetID: frameID, As: st.Video.SceneAs}}
		// The scene picture too, where the model takes several, so the
		// person stays the same.
		if v.SceneAssetID != nil && strings.HasSuffix(cmp.Or(st.Video.SceneAs, refInput["image"]), "s") {
			refs = append(refs, generationRef{AssetID: *v.SceneAssetID, As: st.Video.SceneAs})
		}
		refs = append(refs, generationRef{AssetID: e.VoiceAssetID, As: st.Video.AudioAs})
		asset, waiting, err := s.videoGenerate(ctx, v, generationRequest{Kind: connectors.MediaVideo, Provider: media.FalID, Model: st.Video.Model,
			Prompt: videoPrompt(s.withRules(ctx, v, v.Direction, e.Note), joinWords(e.Words)), Options: st.Video.Options, References: refs, AssetKind: "other", Name: videoName(v, "stretch, edit "+strconv.Itoa(v.Round))})
		if waiting || err != nil {
			return "", waiting, err
		}
		ne := *e
		ne.PartAssetID = asset
		return "edit_upscale", false, s.setVideoEdit(ctx, v, ne)

	case "edit_stitch":
		base, err := s.videoFile(ctx, v, *v.BaseAssetID)
		if err != nil {
			return "", false, err
		}
		part, err := s.videoFile(ctx, v, e.PartAssetID)
		if err != nil {
			return "", false, err
		}
		info, err := s.video.Probe(ctx, part.Data)
		if err != nil {
			return "", false, videoToolError(err, "the new stretch can't be read")
		}
		whole, err := s.video.Splice(ctx, base.Data, part.Data, e.Start, e.End)
		if err != nil {
			return "", false, videoToolError(err, "the new stretch could not be stitched in")
		}
		id, err := s.keepBrandFile(ctx, v, "video.mp4", videoName(v, "edit "+strconv.Itoa(v.Round)), whole)
		if err != nil {
			return "", false, err
		}
		words := spliceWords(v.Words, e.FirstWord, e.LastWord, e.Words, e.Start, e.End-e.Start, info.Seconds)
		raw, _ := json.Marshal(words)
		if err := s.setVideo(ctx, v, `base_asset_id = $2, words = $3`, id, raw); err != nil {
			return "", false, err
		}
		return "finish", false, s.videoStepDone(ctx, v, "edit_stitch", &id, map[string]any{"from_seconds": e.Start, "to_seconds": e.End, "new_seconds": info.Seconds})
	}
	return "", false, videoFail("not_configured", "this server does not know the stage %q", v.Stage)
}

// videoName names a file a video made, after the video.
func videoName(v videoView, part string) string {
	name := cmp.Or(v.Name, "Talking video "+v.CreatedAt.UTC().Format("2006-01-02 15:04"))
	if part != "" {
		name += " (" + part + ")"
	}
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

// videoPrompt is what the video model is told: how the video looks, and
// the words the person says in time with the sound.
func videoPrompt(direction, script string) string {
	said := "The person says, in time with the audio: \"" + strings.ReplaceAll(script, "\"", "'") + "\""
	if direction == "" {
		return said
	}
	return direction + "\n\n" + said
}

func addNote(notes []string, n string) []string {
	if slices.Contains(notes, n) || len(notes) >= 20 {
		return notes
	}
	return append(notes, n)
}

// videoToolError turns a failed ffmpeg run into why the video failed.
func videoToolError(err error, what string) error {
	if errors.Is(err, videofx.ErrBadFile) {
		return videoFail("video_failed", "%s: the file is not a video ffmpeg can work on", what)
	}
	return err
}

// setVideo changes columns of a video that is being made. The first
// argument after the id is $2.
func (s *Server) setVideo(ctx context.Context, v videoView, set string, args ...any) error {
	_, err := s.pool.Exec(ctx, `UPDATE brand_videos SET `+set+`, updated_at = now() WHERE id = $1 AND status = 'working'`, append([]any{v.ID}, args...)...)
	return err
}

func (s *Server) setVideoEdit(ctx context.Context, v videoView, e videoEdit) error {
	raw, _ := json.Marshal(e)
	return s.setVideo(ctx, v, `edit = $2`, raw)
}

// videoStepDone notes a stage done on the server.
func (s *Server) videoStepDone(ctx context.Context, v videoView, stage string, asset *string, detail map[string]any) error {
	raw, _ := json.Marshal(detail)
	_, err := s.pool.Exec(ctx, `INSERT INTO brand_video_steps (workspace_id, brand_id, video_id, round, stage, status, asset_id, detail, finished_at)
		VALUES ($1, $2, $3, $4, $5, 'done', $6, $7, now())
		ON CONFLICT (video_id, round, stage) DO UPDATE SET status = 'done', asset_id = EXCLUDED.asset_id, detail = EXCLUDED.detail, finished_at = now()`,
		v.ws, v.BrandID, v.ID, v.Round, stage, asset, raw)
	return err
}

// videoGenerate runs the video's current stage as a generation on the
// brand's account: it starts one the first time, and afterwards says
// whether it is still going or which file it made. A generation that
// failed fails the video with the provider's words.
func (s *Server) videoGenerate(ctx context.Context, v videoView, req generationRequest) (asset string, waiting bool, err error) {
	var genID *string
	err = s.pool.QueryRow(ctx, `SELECT generation_id::text FROM brand_video_steps WHERE video_id = $1 AND round = $2 AND stage = $3`, v.ID, v.Round, v.Stage).Scan(&genID)
	if errors.Is(err, pgx.ErrNoRows) {
		g, err := s.readyGeneration(ctx, v.ws, v.BrandID, req)
		var inv errInvalid
		var need errConnectorNeeded
		switch {
		case errors.As(err, &inv):
			return "", false, videoFail("connector_invalid", "%s", string(inv))
		case errors.As(err, &need):
			return "", false, videoFail("connector_needed", "%s", string(need))
		case err != nil:
			return "", false, err
		}
		a := &access{WorkspaceID: v.ws, Brand: v.BrandID, principal: principal{ActorType: "system"}}
		if v.CreatedBy != nil {
			a.ActorID = *v.CreatedBy
			_ = s.pool.QueryRow(ctx, `SELECT type FROM actors WHERE id = $1`, a.ActorID).Scan(&a.ActorType)
		}
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			gen, err := queueGeneration(ctx, tx, a, g)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO brand_video_steps (workspace_id, brand_id, video_id, round, stage, generation_id) VALUES ($1, $2, $3, $4, $5, $6)`,
				v.ws, v.BrandID, v.ID, v.Round, v.Stage, gen.ID)
			return err
		})
		var limit errConflictMsg
		if errors.As(err, &limit) {
			return "", false, videoFail("conflict", "%s", string(limit))
		}
		return "", err == nil, err
	}
	if err != nil {
		return "", false, err
	}
	if genID == nil {
		return "", false, videoFail("generation_failed", "the record of this stage's request was deleted. Try again.")
	}
	g, err := findGeneration(ctx, s.pool, v.ws, "", *genID)
	if err != nil {
		return "", false, err
	}
	switch g.Status {
	case "done":
		if g.AssetID == nil {
			return "", false, videoFail("reference_missing", "the file this stage made was deleted. Try again.")
		}
		_, err := s.pool.Exec(ctx, `UPDATE brand_video_steps SET status = 'done', asset_id = $4, finished_at = now() WHERE video_id = $1 AND round = $2 AND stage = $3`,
			v.ID, v.Round, v.Stage, *g.AssetID)
		return *g.AssetID, false, err
	case "failed":
		code, msg := "generation_failed", "the provider could not make this"
		if g.ErrorCode != nil {
			code = *g.ErrorCode
		}
		if g.Error != nil {
			msg = *g.Error
		}
		return "", false, videoFail(code, "%s", msg)
	}
	return "", true, nil
}

// videoFile reads one of the brand's files a video is made from.
func (s *Server) videoFile(ctx context.Context, v videoView, assetID string) (connectors.MediaFile, error) {
	var f connectors.MediaFile
	err := s.pool.QueryRow(ctx, `SELECT filename, content_type FROM brand_assets WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, assetID, v.ws, v.BrandID).Scan(&f.Name, &f.ContentType)
	if errors.Is(err, pgx.ErrNoRows) || !validUUID(assetID) {
		return f, videoFail("reference_missing", "a file this video is made from was deleted. Try again.")
	}
	if err != nil {
		return f, err
	}
	f.Data, err = s.files.Get(ctx, assetKey(v.ws, v.BrandID, assetID, "original"))
	return f, err
}

// videoTranscribe times every word of a recording on the brand's
// ElevenLabs account.
func (s *Server) videoTranscribe(ctx context.Context, v videoView, file connectors.MediaFile) (connectors.Transcript, error) {
	conn, err := s.generationConnection(ctx, v.ws, v.BrandID, media.ElevenLabsID)
	if err != nil {
		return connectors.Transcript{}, err
	}
	if conn == nil {
		return connectors.Transcript{}, videoFail("connector_needed", "the ElevenLabs connection was removed. Connect the account again and try again.")
	}
	o, err := s.integrations.Open(ctx, s.pool, v.ws, conn.ID)
	if err == nil {
		var t connectors.Transcript
		if t, err = (media.ElevenLabs{}).Transcribe(ctx, o.Client, o.Creds, v.Settings.Words.Model, file); err == nil {
			if len(t.Words) > videoMaxWords {
				return t, videoFail("video_too_long", "the voice-over has more than %d words", videoMaxWords)
			}
			metrics.MediaGenerated("transcript", media.ElevenLabsID, "done", 0)
			return t, nil
		}
	}
	var ce *connectors.Error
	if errors.As(err, &ce) && ce.Kind != connectors.ErrUnavailable {
		metrics.MediaGenerated("transcript", media.ElevenLabsID, "refused", 0)
		return connectors.Transcript{}, videoFail(ce.ErrorCode(), "%s", ce.Message)
	}
	// Not reached: the job tries again.
	return connectors.Transcript{}, err
}

// keepBrandFile stores a file a video's stage made on the server with
// the brand's files. A file the brand already has is that file.
func (s *Server) keepBrandFile(ctx context.Context, v videoView, hint, name string, data []byte) (string, error) {
	info, err := assets.Inspect(hint, data)
	if err != nil || len(data) > assets.MaxBytes {
		return "", videoFail("generation_wrong_file", "a file this stage made is too large or not one Outflare can keep (at most %d MB)", assets.MaxBytes>>20)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	var id string
	err = s.pool.QueryRow(ctx, `SELECT id::text FROM brand_assets WHERE workspace_id = $1 AND brand_id = $2 AND sha256 = $3`, v.ws, v.BrandID, hash).Scan(&id)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return "", err
	}
	if err := s.files.Put(ctx, assetKey(v.ws, v.BrandID, id, "original"), info.ContentType, data); err != nil {
		metrics.AssetUploaded("error", 0)
		return "", fmt.Errorf("storing a video's file: %w", err)
	}
	resizable := assets.CanResize(info)
	if resizable {
		if thumb, ct, err := assets.Resize(data, assetThumbWidth); err != nil {
			resizable = false
		} else {
			_ = s.files.Put(ctx, assetKey(v.ws, v.BrandID, id, "w"+strconv.Itoa(assetThumbWidth)), ct, thumb)
		}
	}
	var width, height *int
	if info.Width > 0 && info.Height > 0 {
		width, height = &info.Width, &info.Height
	}
	filename := "video-" + v.ID[:8] + "-" + id[:8] + generationExt[info.ContentType]
	if _, err := s.pool.Exec(ctx, `INSERT INTO brand_assets (id, workspace_id, brand_id, kind, name, filename, content_type, family, bytes, sha256, width, height, resizable, source, created_by)
		VALUES ($1, $2, $3, 'other', $4, $5, $6, $7, $8, $9, $10, $11, $12, 'generated', $13)`,
		id, v.ws, v.BrandID, name, filename, info.ContentType, info.Family, len(data), hash, width, height, resizable, v.CreatedBy); err != nil {
		_ = s.files.Delete(ctx, assetKey(v.ws, v.BrandID, id, "original"))
		return "", err
	}
	metrics.AssetUploaded("stored", len(data))
	return id, nil
}

// readyVideo marks a video made.
func (s *Server) readyVideo(ctx context.Context, v videoView) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE brand_videos SET status = 'ready', stage = 'done', edit = NULL, error_code = '', error = '', finished_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'working'`, v.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if err := versionWhenReady(ctx, tx, v); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: v.ws, ActorType: "system", Action: "video.completed", TargetType: "video", TargetID: v.ID, Data: videoData(v),
			Props: map[string]any{"duration_ms": time.Since(v.CreatedAt).Milliseconds()}})
	})
	if err == nil {
		metrics.VideoEnded(map[bool]string{true: "edited", false: "made"}[v.Round > 0], time.Since(v.CreatedAt))
	}
	return err
}

// failVideo ends a video's making, saying why. An edit that fails leaves
// the video as it was before the edit: ready, with the reason.
func (s *Server) failVideo(ctx context.Context, v videoView, code, message string) error {
	edit := v.Round > 0 && v.FinalAssetID != nil
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		set := `status = 'failed'`
		if edit {
			message = "The edit was not made, and the video is as it was: " + message
			set = `status = 'ready', stage = 'done', edit = NULL`
		}
		tag, err := tx.Exec(ctx, `UPDATE brand_videos SET `+set+`, error_code = $2, error = $3, finished_at = now(), updated_at = now() WHERE id = $1 AND status = 'working'`, v.ID, code, message)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_video_steps SET status = 'failed', finished_at = now() WHERE video_id = $1 AND round = $2 AND status = 'running'`, v.ID, v.Round); err != nil {
			return err
		}
		if edit {
			if err := undoFailedEdit(ctx, tx, v); err != nil {
				return err
			}
			if err := addHistory(ctx, tx, v.ws, v.BrandID, "video", v.ID, "fix_failed", message, "", map[string]any{"error_code": code}); err != nil {
				return err
			}
		}
		data := videoData(v)
		data["error_code"] = code
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: v.ws, ActorType: "system", Action: map[bool]string{true: "video.edit_failed", false: "video.failed"}[edit],
			TargetType: "video", TargetID: v.ID, Data: data})
	})
	if err != nil {
		return err
	}
	metrics.VideoEnded(map[bool]string{true: "edit_failed", false: "failed"}[edit], 0)
	s.log.Warn("video failed", "video_id", v.ID, "stage", v.Stage, "round", v.Round, "error_code", code, "error", message)
	return nil
}

// ---- Words ----

// plainWord is a word without case or punctuation, to compare what was
// said with what was written.
func plainWord(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

// scriptDiffers says where the voice read something other than the
// script: the first few places, each with its time, so a person knows
// which words to listen to and fix.
func scriptDiffers(script string, said []connectors.SpokenWord) []string {
	var want []string
	for _, w := range strings.Fields(script) {
		if p := plainWord(w); p != "" {
			want = append(want, p)
		}
	}
	got := make([]string, len(said))
	for i, w := range said {
		got[i] = plainWord(w.Text)
	}
	if len(want) > videoMaxWords || len(got) > videoMaxWords {
		return nil
	}
	// The longest run of words both share, in order; what is left over on
	// the spoken side was read differently.
	lcs := make([][]int, len(want)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(got)+1)
	}
	for i := len(want) - 1; i >= 0; i-- {
		for j := len(got) - 1; j >= 0; j-- {
			if want[i] == got[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var notes []string
	i, j := 0, 0
	for i < len(want) && j < len(got) && len(notes) < 5 {
		switch {
		case want[i] == got[j]:
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			// A script word the voice didn't say as written.
			i++
		default:
			notes = append(notes, fmt.Sprintf("At %.1f seconds the voice says \"%s\", which is not in the script there. Listen to it; if it is wrong, pick the word and redo it.", said[j].Start, said[j].Text))
			j++
		}
	}
	if missing := len(want) - lcs[0][0]; missing > 0 && len(notes) == 0 {
		notes = append(notes, strconv.Itoa(missing)+" of the script's words were not heard as written in the voice-over. Play it before using the video.")
	}
	return notes
}

// timeWords gives the words meant the times of the words heard: one for
// one when there are as many, else spread evenly over the recording.
func timeWords(shown []string, t connectors.Transcript) []connectors.SpokenWord {
	out := make([]connectors.SpokenWord, len(shown))
	if len(shown) == len(t.Words) {
		for i, w := range t.Words {
			out[i] = connectors.SpokenWord{Text: shown[i], Start: w.Start, End: w.End}
		}
		return out
	}
	start, end := 0.0, t.Seconds
	if n := len(t.Words); n > 0 {
		start, end = t.Words[0].Start, t.Words[n-1].End
	}
	each := (end - start) / float64(max(len(shown), 1))
	for i, w := range shown {
		out[i] = connectors.SpokenWord{Text: w, Start: start + each*float64(i), End: start + each*float64(i+1)}
	}
	return out
}

// spliceWords replaces words first..last with others. The stretch began
// at start and ran for was seconds; the new one runs for now seconds, so
// the new words (timed from 0) move to start and every later word moves
// by the difference.
func spliceWords(words []connectors.SpokenWord, first, last int, with []connectors.SpokenWord, start, was, now float64) []connectors.SpokenWord {
	out := append([]connectors.SpokenWord{}, words[:first]...)
	for _, w := range with {
		out = append(out, connectors.SpokenWord{Text: w.Text, Start: round3(start + w.Start), End: round3(start + w.End)})
	}
	for _, w := range words[last+1:] {
		out = append(out, connectors.SpokenWord{Text: w.Text, Start: round3(w.Start + now - was), End: round3(w.End + now - was)})
	}
	return out
}

func round3(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

// captionsOf groups words into captions of a few words, shown while they
// are said.
func captionsOf(words []connectors.SpokenWord) []videofx.Caption {
	var out []videofx.Caption
	for i := 0; i < len(words); {
		c := videofx.Caption{Text: words[i].Text, Start: words[i].Start, End: words[i].End}
		j := i + 1
		// Up to three words, unless there is a pause or a sentence ends.
		for ; j < len(words) && j-i < 3 && words[j].Start-words[j-1].End < 0.4 && !strings.ContainsAny(words[j-1].Text[len(words[j-1].Text)-1:], ".!?"); j++ {
			c.Text += " " + words[j].Text
			c.End = words[j].End
		}
		// Each stays up until the next one starts, so the words don't flicker.
		if j < len(words) && words[j].Start-c.End < 0.4 {
			c.End = words[j].Start
		}
		out = append(out, c)
		i = j
	}
	return out
}
