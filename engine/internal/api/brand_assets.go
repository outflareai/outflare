package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/assets"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/blobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A brand's files: logos, fonts, product shots, screenshots, example ads,
// voices and pictures of people (docs/brand-assets.md). The bytes are in
// the server's object storage; brand_assets says what each file is. Ads
// and other content are built from these.

const (
	// maxBrandAssets keeps one brand from filling the bucket.
	maxBrandAssets = 5000
	// assetThumbWidth is the small copy lists show.
	assetThumbWidth = 480
)

// assetWidths are the copies of a picture that can be asked for.
var assetWidths = []int{240, 480, 960, 1920}

func (s *Server) routesBrandAssets(mux *http.ServeMux) {
	read, manage := auth.ScopeContentRead, auth.ScopeContentManage
	base := "/v1/brands/{brand}/assets"
	mux.HandleFunc("GET "+base, s.onBrand(read, s.listBrandAssets))
	mux.HandleFunc("POST "+base, s.onBrand(manage, s.uploadBrandAsset))
	mux.HandleFunc("GET "+base+"/{asset}", s.onBrand(read, s.getBrandAsset))
	mux.HandleFunc("PATCH "+base+"/{asset}", s.onBrand(manage, s.updateBrandAsset))
	mux.HandleFunc("DELETE "+base+"/{asset}", s.onBrand(manage, s.deleteBrandAsset))
	mux.HandleFunc("GET "+base+"/{asset}/file", s.onBrand(read, s.brandAssetFile))
	mux.HandleFunc("POST "+base+"/{asset}/archive", s.onBrand(manage, s.archiveBrandAsset))
	mux.HandleFunc("POST "+base+"/{asset}/restore", s.onBrand(manage, s.restoreBrandAsset))
}

type assetConsent struct {
	// Needed: the file shows or sounds like a real person.
	Needed    bool       `json:"needed"`
	Confirmed bool       `json:"confirmed"`
	At        *time.Time `json:"at"`
	By        *string    `json:"by"`
	Note      string     `json:"note"`
}

type assetView struct {
	ID          string       `json:"id"`
	BrandID     string       `json:"brand_id"`
	Kind        string       `json:"kind"`
	Name        string       `json:"name"`
	Note        string       `json:"note"`
	Tags        []string     `json:"tags"`
	Filename    string       `json:"filename"`
	ContentType string       `json:"content_type"`
	Family      string       `json:"family"`
	Bytes       int64        `json:"bytes"`
	SHA256      string       `json:"sha256"`
	Width       *int         `json:"width"`
	Height      *int         `json:"height"`
	Resizable   bool         `json:"resizable"`
	Source      string       `json:"source"`
	Consent     assetConsent `json:"consent"`
	// Usable: not archived, and agreed to where that is needed. Only
	// usable files go into ads and other content.
	Usable     bool       `json:"usable"`
	FilePath   string     `json:"file_path"`
	ArchivedAt *time.Time `json:"archived_at"`
	Version    int        `json:"version"`
	CreatedBy  *string    `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

const assetColumns = `a.id::text, a.brand_id::text, a.kind, a.name, a.note, a.tags, a.filename, a.content_type, a.family, a.bytes, a.sha256,
	a.width, a.height, a.resizable, a.source, a.consent_at, a.consent_by::text, a.consent_note, a.archived_at, a.version, a.created_by::text, a.created_at, a.updated_at`

func scanAsset(row pgx.CollectableRow) (assetView, error) {
	var v assetView
	err := row.Scan(&v.ID, &v.BrandID, &v.Kind, &v.Name, &v.Note, &v.Tags, &v.Filename, &v.ContentType, &v.Family, &v.Bytes, &v.SHA256,
		&v.Width, &v.Height, &v.Resizable, &v.Source, &v.Consent.At, &v.Consent.By, &v.Consent.Note, &v.ArchivedAt, &v.Version, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt)
	v.Tags = nonNil(v.Tags)
	v.Consent.Needed = assets.NeedsConsent(v.Kind)
	v.Consent.Confirmed = v.Consent.At != nil
	v.Usable = v.ArchivedAt == nil && (!v.Consent.Needed || v.Consent.Confirmed)
	v.FilePath = "/v1/brands/" + v.BrandID + "/assets/" + v.ID + "/file"
	return v, err
}

func assetKey(ws, brand, id, part string) string {
	return fmt.Sprintf("assets/%s/%s/%s/%s", ws, brand, id, part)
}

// assetData is what an asset's events carry: never its name or note.
func assetData(v assetView) map[string]any {
	return map[string]any{"kind": v.Kind, "family": v.Family, "bytes": v.Bytes, "source": v.Source, "consent_needed": v.Consent.Needed, "consent_confirmed": v.Consent.Confirmed}
}

// needFiles answers for a server with no object storage.
func (s *Server) needFiles(w http.ResponseWriter, r *http.Request) bool {
	if s.files != nil {
		return true
	}
	writeError(w, r, http.StatusServiceUnavailable, "not_configured", "this server has no object storage (OUTFLARE_S3_*), so it can't keep files")
	return false
}

func (s *Server) listBrandAssets(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != "" && !slices.Contains(assets.Kinds, kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is one of "+strings.Join(assets.Kinds, ", "))
		return
	}
	archived := q.Get("archived") == "true"
	usable := q.Get("usable") == "true"
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	var list []assetView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT `+assetColumns+` FROM brand_assets a WHERE a.workspace_id = $1 AND a.brand_id = $2
			AND (a.archived_at IS NOT NULL) = $3 AND ($4 = '' OR a.kind = $4)
			AND ($5 = '' OR position($5 in lower(a.name)) > 0 OR $5 = ANY (a.tags))
			AND (NOT $6 OR a.kind NOT IN ('voice', 'person') OR a.consent_at IS NOT NULL)
			ORDER BY a.created_at DESC, a.id LIMIT $7`, a.WorkspaceID, a.Brand, archived, kind, search, usable, maxBrandAssets)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanAsset)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list), "storage": s.files != nil})
}

func findAsset(r *http.Request, tx pgx.Tx, a *access, id string) (assetView, error) {
	if !validUUID(id) {
		return assetView{}, errNotFound
	}
	rows, err := tx.Query(r.Context(), `SELECT `+assetColumns+` FROM brand_assets a WHERE a.id = $1 AND a.workspace_id = $2 AND a.brand_id = $3`,
		id, a.WorkspaceID, a.Brand)
	if err != nil {
		return assetView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanAsset)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) getBrandAsset(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var v assetView
	err := s.readTx(r.Context(), func(tx pgx.Tx) (err error) {
		v, err = findAsset(r, tx, a, r.PathValue("asset"))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type assetConsentInput struct {
	Confirmed *bool   `json:"confirmed"`
	Note      *string `json:"note"`
}

type assetUpload struct {
	Kind     string  `json:"kind"`
	Name     *string `json:"name"`
	Note     *string `json:"note"`
	Filename string  `json:"filename"`
	// The file, base64-encoded.
	Data    string             `json:"data"`
	Tags    *[]string          `json:"tags"`
	Consent *assetConsentInput `json:"consent"`
}

// cleanTags lowercases, trims and de-duplicates tags.
func cleanTags(in []string) ([]string, error) {
	if len(in) > 20 {
		return nil, errInvalid("an asset has at most 20 tags")
	}
	out := []string{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || len(t) > 50 {
			return nil, errInvalid("a tag is 1 to 50 characters")
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (c *assetConsentInput) check() error {
	if c == nil {
		return nil
	}
	if c.Note != nil && len(*c.Note) > 2000 {
		return errInvalid("consent.note is at most 2000 characters")
	}
	return nil
}

func (s *Server) uploadBrandAsset(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needFiles(w, r) {
		return
	}
	var in assetUpload
	// Base64 is a third larger than the file it carries.
	if !decodeLarge(w, r, &in, assets.MaxBytes/3*4+(1<<20)) {
		return
	}
	bad := func(msg string) {
		metrics.AssetUploaded("refused", 0)
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
	}
	if !slices.Contains(assets.Kinds, in.Kind) {
		bad("kind is one of " + strings.Join(assets.Kinds, ", "))
		return
	}
	filename := path.Base(strings.ReplaceAll(strings.TrimSpace(in.Filename), "\\", "/"))
	if filename == "" || filename == "." || filename == "/" || len(filename) > 255 || strings.ContainsAny(filename, "\x00\r\n\"") {
		bad("filename is the file's name, such as logo.png, 1 to 255 characters")
		return
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil || len(data) == 0 {
		bad("data is the file, base64-encoded")
		return
	}
	if len(data) > assets.MaxBytes {
		bad("the file is larger than " + strconv.Itoa(assets.MaxBytes>>20) + " MB")
		return
	}
	info, err := assets.Inspect(filename, data)
	if err != nil {
		bad(strings.TrimPrefix(err.Error(), assets.ErrType.Error()+": "))
		return
	}
	if !assets.Fits(in.Kind, info.Family) {
		bad("a " + in.Kind + " is " + assets.FamiliesFor(in.Kind) + "; this file is " + info.ContentType)
		return
	}
	name := strings.TrimSuffix(filename, path.Ext(filename))
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if name == "" || len(name) > 200 {
		bad("name is 1 to 200 characters")
		return
	}
	note := ""
	if in.Note != nil {
		note = strings.TrimSpace(*in.Note)
	}
	if len(note) > 2000 {
		bad("note is at most 2000 characters")
		return
	}
	tags := []string{}
	if in.Tags != nil {
		if tags, err = cleanTags(*in.Tags); err != nil {
			bad(err.Error())
			return
		}
	}
	if err := in.Consent.check(); err != nil {
		bad(err.Error())
		return
	}
	if in.Consent != nil && !assets.NeedsConsent(in.Kind) {
		bad("consent is only for a voice or a person")
		return
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	resizable := assets.CanResize(info)
	ctx := r.Context()

	// The same file again is the asset the brand already has.
	var existing assetView
	var count int
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM brand_assets WHERE brand_id = $1 AND workspace_id = $2 AND sha256 = $3`, a.Brand, a.WorkspaceID, hash).Scan(&id)
		if err == nil {
			existing, err = findAsset(r, tx, a, id)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM brand_assets WHERE brand_id = $1 AND workspace_id = $2`, a.Brand, a.WorkspaceID).Scan(&count)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if existing.ID != "" {
		metrics.AssetUploaded("duplicate", 0)
		writeJSON(w, http.StatusOK, existing)
		return
	}
	if count >= maxBrandAssets {
		metrics.AssetUploaded("refused", 0)
		writeError(w, r, http.StatusConflict, "conflict", "a brand keeps at most "+strconv.Itoa(maxBrandAssets)+" files; delete some first")
		return
	}

	// The file goes to the bucket first, under an id nothing else has, so
	// a row never points at a file that isn't there.
	var id string
	if err := s.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		s.internal(w, r, err)
		return
	}
	storeFailed := func(err error) {
		metrics.AssetUploaded("error", 0)
		track.Fail(ctx, s.log, track.Event{Name: "asset.store_failed", WorkspaceID: a.WorkspaceID, ActorType: "system", Surface: "system"}, "unavailable", err)
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the file could not be stored; try again")
	}
	if err := s.files.Put(ctx, assetKey(a.WorkspaceID, a.Brand, id, "original"), info.ContentType, data); err != nil {
		storeFailed(err)
		return
	}
	cleanup := func() {
		_ = s.files.Delete(ctx, assetKey(a.WorkspaceID, a.Brand, id, "original"))
		_ = s.files.Delete(ctx, assetKey(a.WorkspaceID, a.Brand, id, "w"+strconv.Itoa(assetThumbWidth)))
	}
	if resizable {
		// The small copy lists show. A picture that won't decode is
		// kept as a file without one.
		if thumb, ct, err := assets.Resize(data, assetThumbWidth); err != nil {
			resizable = false
		} else if err := s.files.Put(ctx, assetKey(a.WorkspaceID, a.Brand, id, "w"+strconv.Itoa(assetThumbWidth)), ct, thumb); err != nil {
			cleanup()
			storeFailed(err)
			return
		}
	}
	var v assetView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var by *string
		if a.ActorID != "" {
			by = &a.ActorID
		}
		var width, height *int
		if info.Width > 0 && info.Height > 0 {
			width, height = &info.Width, &info.Height
		}
		confirmed := in.Consent != nil && in.Consent.Confirmed != nil && *in.Consent.Confirmed
		consentNote := ""
		if in.Consent != nil && in.Consent.Note != nil {
			consentNote = strings.TrimSpace(*in.Consent.Note)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO brand_assets (id, workspace_id, brand_id, kind, name, note, tags, filename, content_type, family, bytes, sha256,
				width, height, resizable, consent_at, consent_by, consent_note, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, CASE WHEN $16 THEN now() END, CASE WHEN $16 THEN $17::uuid END, $18, $17)`,
			id, a.WorkspaceID, a.Brand, in.Kind, name, note, tags, filename, info.ContentType, info.Family, len(data), hash,
			width, height, resizable, confirmed, by, consentNote); err != nil {
			return err
		}
		var err error
		if v, err = findAsset(r, tx, a, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "asset.uploaded", "asset", id, assetData(v))
	})
	if err != nil {
		cleanup()
		if uniqueViolation(err) {
			// The same file arrived twice at once: the other upload won.
			metrics.AssetUploaded("duplicate", 0)
			writeError(w, r, http.StatusConflict, "conflict", "this file was just added to the brand's files; list them to find it")
			return
		}
		metrics.AssetUploaded("error", 0)
		s.internal(w, r, err)
		return
	}
	metrics.AssetUploaded("stored", len(data))
	writeJSON(w, http.StatusCreated, v)
}

type assetPatch struct {
	Kind    *string            `json:"kind"`
	Name    *string            `json:"name"`
	Note    *string            `json:"note"`
	Tags    *[]string          `json:"tags"`
	Consent *assetConsentInput `json:"consent"`
	Version *int               `json:"version"`
}

func (s *Server) updateBrandAsset(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f assetPatch
	if !decode(w, r, &f) {
		return
	}
	ctx := r.Context()
	var v assetView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findAsset(r, tx, a, r.PathValue("asset"))
		if err != nil {
			return err
		}
		if f.Version != nil && *f.Version != cur.Version {
			return errConflictMsg("the file changed since you read it (version " + strconv.Itoa(cur.Version) + "); read it again")
		}
		kind := cur.Kind
		if f.Kind != nil {
			kind = *f.Kind
			if !slices.Contains(assets.Kinds, kind) {
				return errInvalid("kind is one of " + strings.Join(assets.Kinds, ", "))
			}
			if !assets.Fits(kind, cur.Family) {
				return errInvalid("a " + kind + " is " + assets.FamiliesFor(kind) + "; this file is " + cur.ContentType)
			}
		}
		if f.Name != nil && (strings.TrimSpace(*f.Name) == "" || len(*f.Name) > 200) {
			return errInvalid("name is 1 to 200 characters")
		}
		if f.Note != nil && len(*f.Note) > 2000 {
			return errInvalid("note is at most 2000 characters")
		}
		var tags []string
		if f.Tags != nil {
			if tags, err = cleanTags(*f.Tags); err != nil {
				return err
			}
		}
		if err := f.Consent.check(); err != nil {
			return err
		}
		if f.Consent != nil && !assets.NeedsConsent(kind) {
			return errInvalid("consent is only for a voice or a person")
		}
		// Consent: confirmed true stamps who and when, false takes it
		// back. Moving a file to a kind that needs none clears it.
		setConsent, confirmed := false, false
		if f.Consent != nil && f.Consent.Confirmed != nil {
			setConsent, confirmed = *f.Consent.Confirmed != cur.Consent.Confirmed, *f.Consent.Confirmed
		}
		if !assets.NeedsConsent(kind) && cur.Consent.Confirmed {
			setConsent, confirmed = true, false
		}
		var by *string
		if a.ActorID != "" {
			by = &a.ActorID
		}
		var consentNote *string
		if f.Consent != nil {
			consentNote = trimmed(f.Consent.Note)
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_assets SET kind = $2, name = COALESCE($3, name), note = COALESCE($4, note),
				tags = CASE WHEN $5 THEN $6::text[] ELSE tags END,
				consent_at = CASE WHEN NOT $7 THEN consent_at WHEN $8 THEN now() END,
				consent_by = CASE WHEN NOT $7 THEN consent_by WHEN $8 THEN $9::uuid END,
				consent_note = COALESCE($10, consent_note), version = version + 1, updated_at = now()
			WHERE id = $1`, cur.ID, kind, trimmed(f.Name), trimmed(f.Note), f.Tags != nil, tags, setConsent, confirmed, by, consentNote); err != nil {
			return err
		}
		if v, err = findAsset(r, tx, a, cur.ID); err != nil {
			return err
		}
		if setConsent {
			action := "asset.consent_withdrawn"
			if confirmed {
				action = "asset.consent_confirmed"
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "asset", cur.ID, assetData(v)); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "asset.updated", "asset", cur.ID, assetData(v))
	})
	if s.assetError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) assetError(w http.ResponseWriter, r *http.Request, err error) bool {
	var c errConflictMsg
	if errors.As(err, &c) {
		writeError(w, r, http.StatusConflict, "conflict", string(c))
		return true
	}
	return s.allocError(w, r, err)
}

func (s *Server) archiveBrandAsset(w http.ResponseWriter, r *http.Request) {
	s.setAssetArchived(w, r, true)
}
func (s *Server) restoreBrandAsset(w http.ResponseWriter, r *http.Request) {
	s.setAssetArchived(w, r, false)
}

// setAssetArchived archives or restores a file. An archived file keeps
// its bytes and history but is no longer used for anything new.
func (s *Server) setAssetArchived(w http.ResponseWriter, r *http.Request, archive bool) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var v assetView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findAsset(r, tx, a, r.PathValue("asset"))
		if err != nil {
			return err
		}
		if (cur.ArchivedAt != nil) == archive {
			v = cur
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_assets SET archived_at = CASE WHEN $2 THEN now() END, version = version + 1, updated_at = now() WHERE id = $1`,
			cur.ID, archive); err != nil {
			return err
		}
		if v, err = findAsset(r, tx, a, cur.ID); err != nil {
			return err
		}
		action := "asset.restored"
		if archive {
			action = "asset.archived"
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "asset", cur.ID, assetData(v))
	})
	if s.assetError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// deleteBrandAsset removes a file for good: its row, its bytes and its
// smaller copies. This is how a person's picture or voice is taken out
// when they no longer agree.
func (s *Server) deleteBrandAsset(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needFiles(w, r) {
		return
	}
	ctx := r.Context()
	var cur assetView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if cur, err = findAsset(r, tx, a, r.PathValue("asset")); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM brand_assets WHERE id = $1`, cur.ID); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "asset.deleted", "asset", cur.ID, assetData(cur)); err != nil {
			return err
		}
		// The bytes go inside the transaction: if the bucket can't
		// delete them, the row stays and the delete can be tried again.
		parts := []string{"original"}
		for _, wd := range assetWidths {
			parts = append(parts, "w"+strconv.Itoa(wd))
		}
		for _, p := range parts {
			if err := s.files.Delete(ctx, assetKey(a.WorkspaceID, a.Brand, cur.ID, p)); err != nil && !errors.Is(err, blobs.ErrNotFound) {
				return errStore{err}
			}
		}
		return nil
	})
	var se errStore
	if errors.As(err, &se) {
		track.Fail(ctx, s.log, track.Event{Name: "asset.store_failed", WorkspaceID: a.WorkspaceID, ActorType: "system", Surface: "system"}, "unavailable", se.error)
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the file could not be deleted from storage; try again")
		return
	}
	if s.assetError(w, r, err) {
		return
	}
	metrics.AssetDeleted()
	w.WriteHeader(http.StatusNoContent)
}

// errStore is a failure of the object storage.
type errStore struct{ error }

// brandAssetFile sends a file's bytes, or with width= a smaller copy of a
// picture. Copies are made once and kept.
func (s *Server) brandAssetFile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needFiles(w, r) {
		return
	}
	ctx := r.Context()
	var v assetView
	err := s.readTx(ctx, func(tx pgx.Tx) (err error) {
		v, err = findAsset(r, tx, a, r.PathValue("asset"))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	part, ct := "original", v.ContentType
	width := 0
	if raw := r.URL.Query().Get("width"); raw != "" {
		width, _ = strconv.Atoi(raw)
		if !slices.Contains(assetWidths, width) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "width is one of 240, 480, 960 or 1920")
			return
		}
		if !v.Resizable {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "only pictures (PNG, JPEG, GIF, WebP) have smaller copies")
			return
		}
		part = "w" + strconv.Itoa(width)
	}
	data, err := s.files.Get(ctx, assetKey(a.WorkspaceID, a.Brand, v.ID, part))
	if errors.Is(err, blobs.ErrNotFound) && width > 0 {
		// First time this size is asked for: make it and keep it.
		var orig []byte
		if orig, err = s.files.Get(ctx, assetKey(a.WorkspaceID, a.Brand, v.ID, "original")); err == nil {
			if data, ct, err = assets.Resize(orig, width); err == nil {
				// Not keeping the copy only costs making it again.
				_ = s.files.Put(ctx, assetKey(a.WorkspaceID, a.Brand, v.ID, part), ct, data)
			}
		}
	}
	switch {
	case errors.Is(err, blobs.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "the file is missing from storage")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if width > 0 {
		// A copy is PNG or JPEG whatever the original was.
		ct = http.DetectContentType(data)
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	// A file is whatever someone uploaded: it never runs as a page of
	// this site, and the browser never guesses a different type for it.
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "private, max-age=3600")
	disposition := "inline"
	if v.Family == "font" || r.URL.Query().Get("download") == "true" {
		disposition = "attachment"
	}
	h.Set("Content-Disposition", disposition+`; filename="`+strings.ReplaceAll(v.Filename, `"`, "")+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
