package api

import (
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

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/assets"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/media"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Pictures, video and voice made on a brand's own AI accounts
// (docs/brand-generations.md): OpenAI and fal.ai for pictures, fal.ai for
// video, ElevenLabs for voice. A request is a row in brand_generations and
// a job; the finished file is kept with the brand's files. Outflare holds
// no key of its own for any of them: without the connection, nothing runs.

const (
	jobGenerate = "asset.generate"
	// maxGenerationsPerDay keeps a runaway script or agent from spending a
	// brand's balance: every one is paid by the brand to its provider.
	maxGenerationsPerDay = 200
	// generationGiveUp is how long a provider gets to finish a request
	// before Outflare stops asking about it.
	generationGiveUp = 45 * time.Minute
	generationsShown = 200
	// generationTries is how often one step is tried when the provider
	// can't be reached.
	generationTries = 5
)

// generationFamily is the family of file each kind must come back as.
var generationFamily = map[string]string{connectors.MediaImage: "image", connectors.MediaVideo: "video", connectors.MediaVoice: "audio"}

func (s *Server) routesBrandGenerations(mux *http.ServeMux) {
	read, manage := auth.ScopeContentRead, auth.ScopeContentManage
	base := "/v1/brands/{brand}/generations"
	mux.HandleFunc("GET "+base, s.onBrand(read, s.listBrandGenerations))
	mux.HandleFunc("POST "+base, s.onBrand(manage, s.createBrandGeneration))
	mux.HandleFunc("GET "+base+"/providers", s.onBrand(read, s.listGenerationProviders))
	mux.HandleFunc("GET "+base+"/voices", s.onBrand(read, s.listGenerationVoices))
	mux.HandleFunc("GET "+base+"/{generation}", s.onBrand(read, s.getBrandGeneration))
	mux.HandleFunc("POST "+base+"/{generation}/reuse", s.onBrand(manage, s.reuseBrandGeneration))
}

func (s *Server) registerBrandGenerationJobs(q *jobs.Queue) {
	q.Handle(jobGenerate, s.generateJob)
}

type generationView struct {
	ID            string         `json:"id"`
	BrandID       string         `json:"brand_id"`
	Kind          string         `json:"kind"`
	Provider      string         `json:"provider"`
	IntegrationID *string        `json:"integration_id"`
	Model         string         `json:"model"`
	Prompt        string         `json:"prompt"`
	Voice         string         `json:"voice"`
	Options       map[string]any `json:"options"`
	// The brand files sent along as references, each with the model's
	// input it went in.
	References []generationRef `json:"references"`
	// The recipe: what was sent to the provider and what it answered
	// (without the file). Null until it was sent.
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
	// The generation whose recipe this one reuses.
	RecipeOf  *string `json:"recipe_of"`
	AssetKind string  `json:"asset_kind"`
	Name      string  `json:"name"`
	// queued, running, done or failed.
	Status string `json:"status"`
	// Why it failed, as a code and in words.
	ErrorCode *string `json:"error_code"`
	Error     *string `json:"error"`
	// The brand file it became, once done.
	AssetID *string `json:"asset_id"`
	// What the provider reported it used: tokens, characters or billable
	// units.
	Usage      map[string]float64 `json:"usage"`
	CreatedBy  *string            `json:"created_by"`
	CreatedAt  time.Time          `json:"created_at"`
	StartedAt  *time.Time         `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at"`

	runID, ws string
}

// generationRef is one brand file a generation is made from.
type generationRef struct {
	AssetID string `json:"asset_id"`
	// As is the model's own input the file goes in, such as image_urls,
	// audio_urls or video_url. Empty picks the usual one for the file:
	// image_urls, video_urls or audio_urls.
	As string `json:"as"`
}

const generationColumns = `g.id::text, g.brand_id::text, g.kind, g.connector, g.integration_id::text, g.model, g.prompt, g.voice, g.options, g.asset_kind, g.name,
	g.status, NULLIF(g.error_code, ''), NULLIF(g.error, ''), g.asset_id::text, g.usage, g.created_by::text, g.created_at, g.started_at, g.finished_at, g.run_id, g.workspace_id::text,
	g.refs, g.request, g.response, g.recipe_of::text`

func scanGeneration(row pgx.CollectableRow) (generationView, error) {
	var v generationView
	var options, usage, refs, request, response []byte
	err := row.Scan(&v.ID, &v.BrandID, &v.Kind, &v.Provider, &v.IntegrationID, &v.Model, &v.Prompt, &v.Voice, &options, &v.AssetKind, &v.Name,
		&v.Status, &v.ErrorCode, &v.Error, &v.AssetID, &usage, &v.CreatedBy, &v.CreatedAt, &v.StartedAt, &v.FinishedAt, &v.runID, &v.ws,
		&refs, &request, &response, &v.RecipeOf)
	v.Options, v.Usage, v.References = map[string]any{}, map[string]float64{}, []generationRef{}
	_ = json.Unmarshal(refs, &v.References)
	v.Request, v.Response = recipeJSON(request), recipeJSON(response)
	_ = json.Unmarshal(options, &v.Options)
	_ = json.Unmarshal(usage, &v.Usage)
	return v, err
}

func findGeneration(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, ws, brand, id string) (generationView, error) {
	if !validUUID(id) {
		return generationView{}, errNotFound
	}
	rows, err := q.Query(ctx, `SELECT `+generationColumns+` FROM brand_generations g WHERE g.id = $1 AND g.workspace_id = $2 AND ($3 = '' OR g.brand_id = $3::uuid)`, id, ws, brand)
	if err != nil {
		return generationView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

// recipeJSON is stored JSON as it is, or null.
func recipeJSON(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

// generationData is what a generation's events carry: never its prompt.
func generationData(v generationView) map[string]any {
	return map[string]any{"kind": v.Kind, "provider": v.Provider, "model": v.Model, "asset_kind": v.AssetKind, "references": len(v.References), "reused": v.RecipeOf != nil}
}

func (s *Server) listBrandGenerations(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	kind, status := q.Get("kind"), q.Get("status")
	if kind != "" && !slices.Contains(connectors.MediaKinds, kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is one of "+strings.Join(connectors.MediaKinds, ", "))
		return
	}
	if status != "" && !slices.Contains([]string{"queued", "running", "done", "failed"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is one of queued, running, done, failed")
		return
	}
	asset := q.Get("asset_id")
	if asset != "" && !validUUID(asset) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "asset_id is the id of a brand file")
		return
	}
	var list []generationView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT `+generationColumns+` FROM brand_generations g WHERE g.workspace_id = $1 AND g.brand_id = $2
			AND ($3 = '' OR g.kind = $3) AND ($4 = '' OR g.status = $4) AND ($6 = '' OR g.asset_id = NULLIF($6, '')::uuid)
			ORDER BY g.created_at DESC, g.id LIMIT $5`, a.WorkspaceID, a.Brand, kind, status, generationsShown, asset)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanGeneration)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list)})
}

func (s *Server) getBrandGeneration(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := findGeneration(r.Context(), s.pool, a.WorkspaceID, a.Brand, r.PathValue("generation"))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// generationConnection finds the brand's connected account for a
// provider: its own connection first, else one the workspace shares with
// it. Nil when there is none.
func (s *Server) generationConnection(ctx context.Context, ws, brand, connector string) (*connectors.Integration, error) {
	if s.integrations == nil {
		return nil, nil
	}
	list, err := s.integrations.List(ctx, s.pool, ws, connector)
	if err != nil {
		return nil, err
	}
	for _, i := range connectors.ForBrand(list, brand) {
		if i.Status == connectors.StatusConnected {
			return &i, nil
		}
	}
	return nil, nil
}

type generationProvider struct {
	// The connector to connect: openai, fal or elevenlabs.
	Provider string `json:"provider"`
	Name     string `json:"name"`
	// Connected: the brand has a working connection to it.
	Connected     bool    `json:"connected"`
	IntegrationID *string `json:"integration_id"`
}

type generationKind struct {
	Kind string `json:"kind"`
	// Ready: at least one provider for it is connected.
	Ready     bool                 `json:"ready"`
	Providers []generationProvider `json:"providers"`
}

// listGenerationProviders says which accounts can make each kind of file
// for the brand, and which of them are connected.
func (s *Server) listGenerationProviders(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	out := []generationKind{}
	for _, kind := range connectors.MediaKinds {
		k := generationKind{Kind: kind, Providers: []generationProvider{}}
		for _, id := range media.Providers[kind] {
			p := generationProvider{Provider: id, Name: id}
			if s.integrations != nil {
				if d, ok := s.integrations.Registry.Definition(id); ok {
					p.Name = d.Name
				}
			}
			conn, err := s.generationConnection(r.Context(), a.WorkspaceID, a.Brand, id)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if conn != nil {
				p.Connected, p.IntegrationID = true, &conn.ID
				k.Ready = true
			}
			k.Providers = append(k.Providers, p)
		}
		out = append(out, k)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "storage": s.files != nil})
}

// listGenerationVoices lists the voices of the brand's ElevenLabs account,
// to choose the one that reads a script.
func (s *Server) listGenerationVoices(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	conn, err := s.generationConnection(ctx, a.WorkspaceID, a.Brand, media.ElevenLabsID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if conn == nil {
		writeError(w, r, http.StatusConflict, "connector_needed", "connect your ElevenLabs account under Connectors, Pictures, video and voice, to list its voices")
		return
	}
	o, err := s.integrations.Open(ctx, s.pool, a.WorkspaceID, conn.ID)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	voices, err := media.ElevenLabs{}.Voices(ctx, o.Client, o.Creds)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": voices, "provider": media.ElevenLabsID})
}

type generationRequest struct {
	Kind string `json:"kind"`
	// Provider is openai, fal or elevenlabs; empty picks the first one
	// the brand has connected for the kind.
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Prompt   string         `json:"prompt"`
	Voice    string         `json:"voice"`
	Options  map[string]any `json:"options"`
	// References are brand files sent along: a person or scene to keep
	// the same, a voice-over a video is made to, a video to upscale.
	References []generationRef `json:"references"`
	// AssetKind is what the file is kept as among the brand's files.
	AssetKind string `json:"asset_kind"`
	Name      string `json:"name"`

	recipeOf string
}

// readyGeneration is a request that passed every check and has a
// connection to run on.
type readyGeneration struct {
	q               connectors.MediaRequest
	options, refs   []byte
	assetKind, name string
	recipeOf        *string
	conn            *connectors.Integration
}

// refInput is the usual input of a model for each family of file.
var refInput = map[string]string{"image": "image_urls", "video": "video_urls", "audio": "audio_urls"}

// readyReferences checks the brand files a request is made from: they are
// the brand's, usable (not archived, agreed to where a person or voice
// needs it) and of a sort the provider takes.
func (s *Server) readyReferences(ctx context.Context, ws, brand, kind string, in []generationRef) ([]generationRef, error) {
	out := []generationRef{}
	if len(in) > connectors.MediaFilesMax {
		return nil, errInvalid("references holds at most " + strconv.Itoa(connectors.MediaFilesMax) + " files")
	}
	if len(in) > 0 && kind == connectors.MediaVoice {
		return nil, errInvalid("a voice-over takes no references: the voice is the reference")
	}
	var total int64
	for _, ref := range in {
		if !validUUID(ref.AssetID) {
			return nil, errInvalid("each reference names a brand file by asset_id")
		}
		var family string
		var bytes int64
		var archived, consent *time.Time
		var assetKind string
		err := s.pool.QueryRow(ctx, `SELECT family, bytes, archived_at, consent_at, kind FROM brand_assets WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`,
			ref.AssetID, ws, brand).Scan(&family, &bytes, &archived, &consent, &assetKind)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errInvalid("reference " + ref.AssetID + " is not one of this brand's files")
		}
		if err != nil {
			return nil, err
		}
		if archived != nil || (assets.NeedsConsent(assetKind) && consent == nil) {
			return nil, errInvalid("reference " + ref.AssetID + " can't be used: it is archived, or it shows a person or a voice nobody has confirmed consent for (Brand kit)")
		}
		if refInput[family] == "" {
			return nil, errInvalid("a reference is a picture, a video or a sound file")
		}
		ref.As = strings.TrimSpace(ref.As)
		if ref.As == "" {
			ref.As = refInput[family]
		}
		if _, err := (connectors.MediaRequest{Kind: kind, Model: "m", Files: []connectors.MediaFile{{As: ref.As}}}).Normal(); err != nil {
			return nil, errInvalid("a reference's \"as\" is the model's own name for the input the file goes in, such as image_urls, audio_urls or video_url")
		}
		total += bytes
		out = append(out, ref)
	}
	if total > 2*assets.MaxBytes {
		return nil, errInvalid("the references are too large together (at most " + strconv.Itoa(2*assets.MaxBytes>>20) + " MB)")
	}
	return out, nil
}

// readyGeneration checks a request and finds the brand's connection for
// it. Nothing is written or sent.
func (s *Server) readyGeneration(ctx context.Context, ws, brand string, in generationRequest) (readyGeneration, error) {
	refs, err := s.readyReferences(ctx, ws, brand, in.Kind, in.References)
	if err != nil {
		return readyGeneration{}, err
	}
	files := make([]connectors.MediaFile, len(refs))
	for i, ref := range refs {
		files[i].As = ref.As
	}
	q, err := connectors.MediaRequest{Kind: in.Kind, Model: in.Model, Prompt: in.Prompt, Voice: in.Voice, Options: in.Options, Files: files}.Normal()
	if err != nil {
		var ce *connectors.Error
		if errors.As(err, &ce) {
			return readyGeneration{}, errInvalid(ce.Message)
		}
		return readyGeneration{}, err
	}
	g := readyGeneration{q: q, assetKind: in.AssetKind, name: strings.TrimSpace(in.Name)}
	g.refs, _ = json.Marshal(refs)
	if in.recipeOf != "" {
		g.recipeOf = &in.recipeOf
	}
	for k := range q.Options {
		for _, ref := range refs {
			if k == ref.As && !strings.HasSuffix(k, "s") {
				return g, errInvalid("options." + k + " is set by a reference: leave it out of options")
			}
		}
	}
	if g.options, err = json.Marshal(q.Options); err != nil || len(g.options) > 20000 {
		return g, errInvalid("options is at most 20,000 characters of JSON")
	}
	if q.Options == nil {
		g.options = []byte(`{}`)
	}
	if g.assetKind == "" {
		g.assetKind = "other"
	}
	if !slices.Contains(assets.Kinds, g.assetKind) || !assets.Fits(g.assetKind, generationFamily[q.Kind]) {
		return g, errInvalid("asset_kind is what the file is kept as among the brand's files: a kind that takes this sort of file, such as other, example_ad or product_shot")
	}
	if len(g.name) > 200 {
		return g, errInvalid("name is at most 200 characters")
	}
	providers := media.Providers[q.Kind]
	if in.Provider != "" {
		if !slices.Contains(providers, in.Provider) {
			return g, errInvalid("provider for a " + q.Kind + " is one of " + strings.Join(providers, ", "))
		}
		providers = []string{in.Provider}
	}
	for _, id := range providers {
		if g.conn, err = s.generationConnection(ctx, ws, brand, id); err != nil {
			return g, err
		}
		if g.conn != nil {
			return g, nil
		}
	}
	return g, errConnectorNeeded("making a " + q.Kind + " needs your own " + strings.Join(providers, " or ") +
		" account: connect it under Connectors (Pictures, video and voice; OpenAI is under AI models). Outflare has no account of its own for this.")
}

// queueGeneration writes a checked request and its job, in the caller's
// transaction.
func queueGeneration(ctx context.Context, tx pgx.Tx, a *access, g readyGeneration) (generationView, error) {
	var today, files int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM brand_generations WHERE workspace_id = $1 AND brand_id = $2 AND created_at > now() - interval '24 hours'),
			(SELECT count(*) FROM brand_assets WHERE workspace_id = $1 AND brand_id = $2)`, a.WorkspaceID, a.Brand).Scan(&today, &files); err != nil {
		return generationView{}, err
	}
	if today >= maxGenerationsPerDay {
		return generationView{}, errConflictMsg("a brand makes at most " + strconv.Itoa(maxGenerationsPerDay) + " pictures, videos and recordings in 24 hours; try again later")
	}
	if files >= maxBrandAssets {
		return generationView{}, errConflictMsg("a brand keeps at most " + strconv.Itoa(maxBrandAssets) + " files; delete some first")
	}
	var by *string
	if a.ActorID != "" {
		by = &a.ActorID
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO brand_generations (workspace_id, brand_id, kind, connector, integration_id, model, prompt, voice, options, asset_kind, name, created_by, refs, recipe_of)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id::text`,
		a.WorkspaceID, a.Brand, g.q.Kind, g.conn.Connector, g.conn.ID, g.q.Model, g.q.Prompt, g.q.Voice, g.options, g.assetKind, g.name, by, g.refs, g.recipeOf).Scan(&id); err != nil {
		return generationView{}, err
	}
	if err := enqueueGeneration(ctx, tx, a.WorkspaceID, id, 0, 0); err != nil {
		return generationView{}, err
	}
	v, err := findGeneration(ctx, tx, a.WorkspaceID, a.Brand, id)
	if err != nil {
		return v, err
	}
	return v, record(ctx, tx, &a.principal, a.WorkspaceID, "generation.requested", "generation", id, generationData(v))
}

// generationError answers a request that was refused.
func (s *Server) generationError(w http.ResponseWriter, r *http.Request, err error) bool {
	var need errConnectorNeeded
	if errors.As(err, &need) {
		writeError(w, r, http.StatusConflict, "connector_needed", string(need))
		return true
	}
	return s.assetError(w, r, err)
}

// createBrandGeneration takes a request for a picture, video or voice
// recording and queues it. Nothing is sent to the provider here.
func (s *Server) createBrandGeneration(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needFiles(w, r) {
		return
	}
	var in generationRequest
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	g, err := s.readyGeneration(ctx, a.WorkspaceID, a.Brand, in)
	if s.generationError(w, r, err) {
		return
	}
	var v generationView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err = queueGeneration(ctx, tx, a, g)
		return err
	})
	if s.generationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// generationReuse is what changes when a recipe is used again. A field
// left out stays as it was.
type generationReuse struct {
	Prompt     *string          `json:"prompt"`
	Model      *string          `json:"model"`
	Voice      *string          `json:"voice"`
	Options    *map[string]any  `json:"options"`
	References *[]generationRef `json:"references"`
	AssetKind  *string          `json:"asset_kind"`
	Name       *string          `json:"name"`
}

// reuseBrandGeneration makes a new file from the recipe of an earlier
// one: the same provider, model, prompt, settings and references, with
// whatever the caller changes. The earlier one stays as it is.
func (s *Server) reuseBrandGeneration(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needFiles(w, r) {
		return
	}
	var in generationReuse
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	old, err := findGeneration(ctx, s.pool, a.WorkspaceID, a.Brand, r.PathValue("generation"))
	if s.allocError(w, r, err) {
		return
	}
	req := generationRequest{Kind: old.Kind, Provider: old.Provider, Model: old.Model, Prompt: old.Prompt, Voice: old.Voice, Options: old.Options,
		References: old.References, AssetKind: old.AssetKind, Name: old.Name, recipeOf: old.ID}
	if len(req.Options) == 0 {
		req.Options = nil
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&req.Prompt, in.Prompt)
	set(&req.Model, in.Model)
	set(&req.Voice, in.Voice)
	set(&req.AssetKind, in.AssetKind)
	set(&req.Name, in.Name)
	if in.Options != nil {
		req.Options = *in.Options
	}
	if in.References != nil {
		req.References = *in.References
	}
	g, err := s.readyGeneration(ctx, a.WorkspaceID, a.Brand, req)
	if s.generationError(w, r, err) {
		return
	}
	var v generationView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err = queueGeneration(ctx, tx, a, g)
		return err
	})
	if s.generationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// generationFiles reads the reference files of a generation from the
// brand's files. A file that is gone ends the generation.
func (s *Server) generationFiles(ctx context.Context, g generationView) ([]connectors.MediaFile, string, error) {
	files := make([]connectors.MediaFile, 0, len(g.References))
	for _, ref := range g.References {
		var name, ct string
		err := s.pool.QueryRow(ctx, `SELECT filename, content_type FROM brand_assets WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 AND archived_at IS NULL`,
			ref.AssetID, g.ws, g.BrandID).Scan(&name, &ct)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "a reference file was deleted or archived before this was made. Choose another and ask again.", nil
		}
		if err != nil {
			return nil, "", err
		}
		data, err := s.files.Get(ctx, assetKey(g.ws, g.BrandID, ref.AssetID, "original"))
		if err != nil {
			return nil, "", fmt.Errorf("reading a reference file: %w", err)
		}
		files = append(files, connectors.MediaFile{As: ref.As, Name: name, ContentType: ct, Data: data})
	}
	return files, "", nil
}

// enqueueGeneration queues one step of a generation: starting it (step 0)
// or asking the provider how it is doing (step n), after wait.
func enqueueGeneration(ctx context.Context, q jobs.Querier, ws, id string, step int, wait time.Duration) error {
	j := jobs.NewJob{Kind: jobGenerate, WorkspaceID: &ws, Payload: map[string]string{"generation_id": id},
		IdempotencyKey: jobGenerate + ":" + id + ":" + strconv.Itoa(step), MaxAttempts: generationTries}
	if wait > 0 {
		j.RunAt = time.Now().Add(wait)
	}
	_, err := jobs.Enqueue(ctx, q, j)
	return err
}

func (s *Server) generateJob(ctx context.Context, job jobs.Job) error {
	ws, id, err := socialPayload(job, "generation_id")
	if err != nil {
		return err
	}
	err = s.RunGeneration(ctx, ws, id)
	if errors.Is(err, errGenerationWaiting) && job.Attempt >= job.MaxAttempts {
		// The last try: end it, so it doesn't wait for ever.
		g, ferr := findGeneration(ctx, s.pool, ws, "", id)
		if ferr != nil {
			return ferr
		}
		return s.failGeneration(ctx, g, "connector_unavailable", "the provider could not be reached after "+strconv.Itoa(generationTries)+" tries. Ask again in a few minutes.")
	}
	return err
}

// errGenerationWaiting is a provider that could not be reached: the job
// tries again.
var errGenerationWaiting = errors.New("the provider could not be reached")

// RunGeneration takes a generation one step on: sends it to the provider,
// or asks how a request the provider is still working on is doing, and
// keeps the file once there is one. It queues its own next look while the
// provider works. A request is sent once: when a step dies between
// sending and noting the answer, the generation fails rather than being
// sent (and paid for) again.
func (s *Server) RunGeneration(ctx context.Context, ws, id string) error {
	g, err := findGeneration(ctx, s.pool, ws, "", id)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if g.Status == "done" || g.Status == "failed" {
		return nil
	}
	gen, ok := media.For(g.Provider)
	if !ok || s.integrations == nil || s.files == nil {
		return s.failGeneration(ctx, g, "not_configured", "this server can no longer make this")
	}
	if g.IntegrationID == nil {
		return s.failGeneration(ctx, g, "connector_needed", "the connection this was to run on was removed. Connect the account again and ask once more.")
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, *g.IntegrationID)
	if err != nil {
		var ce *connectors.Error
		if errors.As(err, &ce) {
			return s.failGeneration(ctx, g, ce.ErrorCode(), ce.Message)
		}
		if errors.Is(err, connectors.ErrNotFound) {
			return s.failGeneration(ctx, g, "connector_needed", "the connection this was to run on was removed. Connect the account again and ask once more.")
		}
		return err
	}
	q := connectors.MediaRequest{Kind: g.Kind, Model: g.Model, Prompt: g.Prompt, Voice: g.Voice, Options: g.Options}
	if len(q.Options) == 0 {
		q.Options = nil
	}

	var run connectors.MediaRun
	if g.runID == "" {
		// Claim it before sending: only one step ever sends.
		tag, err := s.pool.Exec(ctx, `UPDATE brand_generations SET status = 'running', started_at = now() WHERE id = $1 AND status = 'queued'`, g.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return s.failGeneration(ctx, g, "generation_interrupted", "Outflare was interrupted while the provider worked on this, so it can't tell whether it was made. Check the provider's own history before asking again.")
		}
		files, gone, ferr := s.generationFiles(ctx, g)
		if ferr != nil || gone != "" {
			// Nothing was sent: free it, or end it when a file is gone.
			if _, uerr := s.pool.Exec(ctx, `UPDATE brand_generations SET status = 'queued', started_at = NULL WHERE id = $1 AND status = 'running'`, g.ID); uerr != nil {
				return uerr
			}
			if gone != "" {
				return s.failGeneration(ctx, g, "reference_missing", gone)
			}
			return ferr
		}
		q.Files = files
		run, err = gen.StartMedia(ctx, o.Client, o.Creds, q)
		if err != nil {
			var ce *connectors.Error
			if errors.As(err, &ce) && ce.Kind == connectors.ErrUnavailable && !connectors.IsTimeout(err) && ctx.Err() == nil {
				// Not reached, so nothing was made: free it for the next try.
				if _, uerr := s.pool.Exec(ctx, `UPDATE brand_generations SET status = 'queued', started_at = NULL WHERE id = $1 AND status = 'running'`, g.ID); uerr != nil {
					return uerr
				}
				if time.Since(g.CreatedAt) < generationGiveUp {
					return fmt.Errorf("%w: %w", errGenerationWaiting, err)
				}
			}
			return s.generationRefused(ctx, g, err)
		}
		if !run.Done && run.RunID != "" {
			if _, err := s.pool.Exec(ctx, `UPDATE brand_generations SET run_id = $2, request = $3 WHERE id = $1`, g.ID, run.RunID, recipeOrNil(run.Request)); err != nil {
				return err
			}
			return enqueueGeneration(ctx, s.pool, ws, g.ID, int(time.Now().Unix()), 10*time.Second)
		}
	} else {
		run, err = gen.FetchMedia(ctx, o.Client, o.Creds, q, g.runID)
		if err != nil {
			var ce *connectors.Error
			if errors.As(err, &ce) && ce.Kind == connectors.ErrUnavailable && time.Since(g.CreatedAt) < generationGiveUp {
				// Asking again is free.
				return fmt.Errorf("%w: %w", errGenerationWaiting, err)
			}
			return s.generationRefused(ctx, g, err)
		}
	}
	switch {
	case run.Failure != "":
		return s.failGeneration(ctx, g, "generation_failed", run.Failure)
	case run.Done:
		return s.keepGeneration(ctx, g, run)
	case time.Since(g.CreatedAt) > generationGiveUp:
		return s.failGeneration(ctx, g, "generation_timed_out", "the provider did not finish this in "+strconv.Itoa(int(generationGiveUp.Minutes()))+" minutes. Check the provider's own history before asking again.")
	}
	// Still working: look again, a little later each time, up to a minute.
	age := time.Since(g.CreatedAt)
	return enqueueGeneration(ctx, s.pool, ws, g.ID, int(time.Now().Unix()), min(10*time.Second+age/10, time.Minute))
}

// recipeOrNil is JSON to store, or SQL null for none.
func recipeOrNil(raw json.RawMessage) any {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil
	}
	return []byte(raw)
}

// generationRefused ends a generation the provider turned down.
func (s *Server) generationRefused(ctx context.Context, g generationView, err error) error {
	var ce *connectors.Error
	if errors.As(err, &ce) {
		return s.failGeneration(ctx, g, ce.ErrorCode(), ce.Message)
	}
	return err
}

// failGeneration ends a generation without a file, saying why.
func (s *Server) failGeneration(ctx context.Context, g generationView, code, message string) error {
	if ctx.Err() != nil {
		// The server is stopping: say so, with a context that still works.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE brand_generations SET status = 'failed', error_code = $2, error = $3, finished_at = now() WHERE id = $1 AND status IN ('queued', 'running')`,
			g.ID, code, message)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		data := generationData(g)
		data["error_code"] = code
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: g.ws, ActorType: "system", Action: "generation.failed", TargetType: "generation", TargetID: g.ID, Data: data})
	})
	if err != nil {
		return err
	}
	outcome := "failed"
	switch code {
	case "connector_invalid", "connector_auth", "connector_quota", "connector_needed":
		outcome = "refused"
	case "generation_timed_out":
		outcome = "timed_out"
	}
	metrics.MediaGenerated(g.Kind, g.Provider, outcome, 0)
	s.wakeVideo(ctx, g.ID)
	s.log.Warn("generation failed", "generation_id", g.ID, "kind", g.Kind, "provider", g.Provider, "error_code", code, "error", message)
	return nil
}

// generationExt is the file ending for what each kind usually comes back
// as; the real type is worked out from the bytes.
var generationExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp",
	"video/mp4": ".mp4", "video/webm": ".webm", "video/quicktime": ".mov", "audio/mpeg": ".mp3", "audio/wave": ".wav", "audio/mp4": ".m4a", "audio/ogg": ".ogg"}

// keepGeneration stores a finished file with the brand's files and marks
// the generation done. A file the brand already has is that asset.
func (s *Server) keepGeneration(ctx context.Context, g generationView, run connectors.MediaRun) error {
	ws := g.ws
	hint := map[string]string{connectors.MediaImage: "file.png", connectors.MediaVideo: "file.mp4", connectors.MediaVoice: "file.mp3"}[g.Kind]
	info, err := assets.Inspect(hint, run.Data)
	if err != nil || info.Family != generationFamily[g.Kind] || len(run.Data) > assets.MaxBytes {
		return s.failGeneration(ctx, g, "generation_wrong_file", "the provider answered with something that is not "+map[string]string{"image": "a picture", "video": "a video", "audio": "a sound file"}[generationFamily[g.Kind]]+" Outflare can keep, so it was not saved")
	}
	sum := sha256.Sum256(run.Data)
	hash := hex.EncodeToString(sum[:])
	name := g.Name
	if name == "" {
		name = map[string]string{connectors.MediaImage: "Picture", connectors.MediaVideo: "Video", connectors.MediaVoice: "Voice recording"}[g.Kind] + " " + g.CreatedAt.UTC().Format("2006-01-02 15:04")
	}
	filename := "generated-" + g.ID[:8] + generationExt[info.ContentType]

	var assetID string
	err = s.pool.QueryRow(ctx, `SELECT id::text FROM brand_assets WHERE workspace_id = $1 AND brand_id = $2 AND sha256 = $3`, ws, g.BrandID, hash).Scan(&assetID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	stored := assetID == ""
	resizable := assets.CanResize(info)
	if stored {
		if err := s.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&assetID); err != nil {
			return err
		}
		// The file goes to the bucket first, so a row never points at a
		// file that isn't there. A failure here is tried again: the
		// provider is not asked twice for a file it already made only
		// when it answers in one call, so those fail instead.
		if err := s.files.Put(ctx, assetKey(ws, g.BrandID, assetID, "original"), info.ContentType, run.Data); err != nil {
			track.Fail(ctx, s.log, track.Event{Name: "asset.store_failed", WorkspaceID: ws, ActorType: "system", Surface: "job"}, "unavailable", err)
			metrics.AssetUploaded("error", 0)
			if g.runID != "" && time.Since(g.CreatedAt) < generationGiveUp {
				return fmt.Errorf("storing the file: %w", err)
			}
			return s.failGeneration(ctx, g, "unavailable", "the file was made but could not be stored. Check the server's file storage and ask again.")
		}
		if resizable {
			if thumb, ct, err := assets.Resize(run.Data, assetThumbWidth); err != nil {
				resizable = false
			} else {
				// Lists make the small copy again when it is missing.
				_ = s.files.Put(ctx, assetKey(ws, g.BrandID, assetID, "w"+strconv.Itoa(assetThumbWidth)), ct, thumb)
			}
		}
	}
	usage, _ := json.Marshal(run.Usage)
	if len(run.Usage) == 0 {
		usage = []byte(`{}`)
	}
	done := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if stored {
			var width, height *int
			if info.Width > 0 && info.Height > 0 {
				width, height = &info.Width, &info.Height
			}
			if _, err := tx.Exec(ctx, `INSERT INTO brand_assets (id, workspace_id, brand_id, kind, name, filename, content_type, family, bytes, sha256, width, height, resizable, source, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'generated', $14)`,
				assetID, ws, g.BrandID, g.AssetKind, name, filename, info.ContentType, info.Family, len(run.Data), hash, width, height, resizable, g.CreatedBy); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE brand_generations SET status = 'done', asset_id = $2, usage = $3, finished_at = now(),
				request = COALESCE($4, request), response = $5 WHERE id = $1 AND status IN ('queued', 'running')`,
			g.ID, assetID, usage, recipeOrNil(run.Request), recipeOrNil(run.Response))
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		done = true
		data := generationData(g)
		data["bytes"], data["family"], data["asset_stored"] = len(run.Data), info.Family, stored
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "generation.completed", TargetType: "generation", TargetID: g.ID, Data: data,
			Props: map[string]any{"duration_ms": time.Since(g.CreatedAt).Milliseconds()}})
	})
	if err != nil {
		if stored {
			_ = s.files.Delete(ctx, assetKey(ws, g.BrandID, assetID, "original"))
			_ = s.files.Delete(ctx, assetKey(ws, g.BrandID, assetID, "w"+strconv.Itoa(assetThumbWidth)))
		}
		if g.runID == "" {
			// It can't be fetched again, so say what happened.
			if ferr := s.failGeneration(ctx, g, "unavailable", "the file was made but could not be saved. Ask again."); ferr != nil {
				return ferr
			}
			return nil
		}
		return err
	}
	if done {
		if stored {
			metrics.AssetUploaded("stored", len(run.Data))
		}
		metrics.MediaGenerated(g.Kind, g.Provider, "done", time.Since(g.CreatedAt))
		s.wakeVideo(ctx, g.ID)
	}
	return nil
}
