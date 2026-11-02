package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/news"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Adding journalists by hand or from a file, and changing their beats.
// Each is a person record in a brand (found by their email there, or made),
// marked by the journalist field and put on their outlet's company record
// (found by its domain, or made as a prospect company), so the news watch,
// pitching and media lists all see the same person. Outflare never buys
// journalist data: these are people the workspace already knows.

const (
	// maxJournalistImport bounds one file: each row finds or makes two
	// records.
	maxJournalistImport      = 5000
	maxJournalistImportBytes = 5 << 20
)

type journalistInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	// The outlet: its website, or a company record already there.
	OutletWebsite  string   `json:"outlet_website"`
	OutletRecordID string   `json:"outlet_record_id"`
	OutletName     string   `json:"outlet_name"`
	OutletTier     string   `json:"outlet_tier"`
	JobTitle       string   `json:"job_title"`
	Beats          []string `json:"beats"`
}

// check tidies the input and says what is wrong with it.
func (in *journalistInput) check() error {
	in.Name = strings.Join(strings.Fields(in.Name), " ")
	in.JobTitle = strings.TrimSpace(in.JobTitle)
	in.OutletName = strings.TrimSpace(in.OutletName)
	in.OutletTier = strings.ToLower(strings.TrimSpace(in.OutletTier))
	in.OutletRecordID = strings.TrimSpace(in.OutletRecordID)
	switch {
	case in.Name == "" || len(in.Name) > 200:
		return errInvalid("name is the journalist's name, at most 200 characters")
	case len(in.JobTitle) > 200:
		return errInvalid("job_title is at most 200 characters")
	case len(in.OutletName) > 200:
		return errInvalid("outlet_name is at most 200 characters")
	case in.OutletTier != "" && !slices.Contains(news.Tiers, in.OutletTier):
		return errInvalid("outlet_tier is national, trade, regional, local or newsletter")
	case in.OutletRecordID != "" && !validUUID(in.OutletRecordID):
		return errInvalid("outlet_record_id is a company record's id")
	case in.OutletRecordID != "" && in.OutletWebsite != "":
		return errInvalid("give the outlet as outlet_website or outlet_record_id, not both")
	case in.OutletTier != "" && in.OutletRecordID == "" && in.OutletWebsite == "":
		return errInvalid("outlet_tier is the outlet's: give the outlet too, as outlet_website or outlet_record_id")
	case len(in.Beats) > news.MaxBeats:
		return errInvalid("a journalist has at most 30 beats")
	}
	e, err := normalizeEmail(in.Email)
	if err != nil {
		return errInvalid("email is the journalist's email address")
	}
	in.Email = e
	if in.OutletWebsite != "" {
		d := customers.CompanyDomain(in.OutletWebsite)
		if d == "" {
			return errInvalid("outlet_website is the outlet's website or domain, like news.example.org")
		}
		in.OutletWebsite = d
	}
	for _, b := range in.Beats {
		if news.CleanBeat(b) == "" || len(b) > 200 {
			return errInvalid("each beat is a topic, at most 100 characters")
		}
	}
	in.Beats = news.MergeBeats(nil, in.Beats...)
	return nil
}

// journalistBrand is the brand a write's people are found and made in: the
// one asked for (an id or handle), else the workspace's first, or a
// brand-limited caller's only brand.
func (s *Server) journalistBrand(ctx context.Context, a *access, raw json.RawMessage) (string, error) {
	brand, err := s.recordBrand(ctx, a, raw)
	if err == nil {
		brand, err = brandWrite(ctx, s.pool, a, brand)
	}
	if err != nil {
		return "", err
	}
	if brand == nil {
		if brand, err = catalogBrand(ctx, s.pool, a.WorkspaceID, ""); err != nil {
			return "", err
		}
	}
	if brand == nil || !a.writes(brand) {
		return "", errInvalid("brand_id is required: you can only add people to your own brands")
	}
	return *brand, nil
}

// putJournalist finds or makes the journalist and their outlet in brand and
// writes what in says about them, as source manual: a person said so. It
// returns their record, whether it is new, and the outlet's record ("" for
// none).
func putJournalist(ctx context.Context, tx pgx.Tx, a *access, c *catalog, brand, origin, via string, in journalistInput) (string, bool, string, error) {
	outlet := ""
	switch {
	case in.OutletRecordID != "":
		var b *string
		err := tx.QueryRow(ctx, `SELECT brand_id::text FROM records WHERE id = $1 AND workspace_id = $2 AND record_type = 'account'
			AND merged_into IS NULL AND deleted_at IS NULL`, in.OutletRecordID, a.WorkspaceID).Scan(&b)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!a.sees(b) || (b != nil && *b != brand))) {
			return "", false, "", errInvalid("outlet_record_id is not a company record in the journalist's brand")
		}
		if err != nil {
			return "", false, "", err
		}
		outlet = in.OutletRecordID
	case in.OutletWebsite != "":
		got, err := customers.EnsureCompanies(ctx, tx, a.WorkspaceID, &brand, via, []customers.Company{{Domain: in.OutletWebsite, Name: in.OutletName}})
		if err != nil {
			return "", false, "", err
		}
		outlet = got[in.OutletWebsite]
	}
	var had *string
	if err := tx.QueryRow(ctx, `SELECT outflare_brand_person_with_email($1::uuid, $2)::text`, brand, in.Email).Scan(&had); err != nil {
		return "", false, "", err
	}
	people, err := customers.EnsurePeopleIn(ctx, tx, a.WorkspaceID, brand, origin, via, []customers.Person{{Email: in.Email, Name: in.Name}})
	if err != nil {
		return "", false, "", err
	}
	id, ok := people[in.Email]
	if !ok {
		return "", false, "", errors.New("journalist: no person for the email")
	}
	rec, err := lockRecord(ctx, tx, a, id)
	if err != nil {
		return "", false, "", err
	}
	if rec.Attributes == nil {
		rec.Attributes = map[string]any{}
	}
	if rec.AttributeSources == nil {
		rec.AttributeSources = map[string]string{}
	}
	attrs := map[string]json.RawMessage{"journalist": json.RawMessage(`true`)}
	put := func(key string, v any) {
		b, _ := json.Marshal(v)
		attrs[key] = b
	}
	if len(in.Beats) > 0 {
		put("beats", news.MergeBeats(news.Beats(rec.Attributes["beats"]), in.Beats...))
	}
	if in.JobTitle != "" {
		put("job_title", in.JobTitle)
	}
	if _, ok := rec.Attributes["first_name"]; !ok {
		if first, last := splitPersonName(in.Name); first != "" {
			put("first_name", first)
			if last != "" {
				put("last_name", last)
			}
		}
	}
	if outlet != "" {
		// The companies they are on already, by the field or a link the
		// news watch made, stay.
		rows, err := tx.Query(ctx, `SELECT DISTINCT to_id::text FROM record_relationships
			WHERE workspace_id = $1 AND from_id = $2 AND field_key = 'companies' AND valid_to IS NULL`, a.WorkspaceID, rec.ID)
		if err != nil {
			return "", false, "", err
		}
		companies, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return "", false, "", err
		}
		if list, ok := rec.Attributes["companies"].([]any); ok {
			for _, x := range list {
				if s, ok := x.(string); ok && !slices.Contains(companies, s) {
					companies = append(companies, s)
				}
			}
		}
		if !slices.Contains(companies, outlet) {
			put("companies", append(companies, outlet))
		}
	}
	t := c.recordType("person")
	if t == nil {
		return "", false, "", errNotFound
	}
	if _, err := applyAttributes(ctx, tx, c, t, a, &rec, attrs, "manual"); err != nil {
		return "", false, "", err
	}
	if rec.Name == "" {
		rec.Name = in.Name
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET name = $2, attributes = $3, attribute_sources = $4, version = version + 1, updated_at = now()
		WHERE id = $1`, rec.ID, rec.Name, rec.Attributes, rec.AttributeSources); err != nil {
		return "", false, "", err
	}
	// The relationship keeps the outlet's link however companies is set.
	if outlet != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, field_key, created_by)
			SELECT $1, $2, $3, 'companies', 'companies', $4
			WHERE NOT EXISTS (SELECT 1 FROM record_relationships WHERE workspace_id = $1 AND from_id = $2 AND to_id = $3 AND kind = 'companies' AND valid_to IS NULL)`,
			a.WorkspaceID, rec.ID, outlet, nullIfEmpty(a.ActorID)); err != nil {
			return "", false, "", err
		}
	}
	if outlet != "" && in.OutletTier != "" {
		if err := setOutletTier(ctx, tx, a, c, outlet, in.OutletTier); err != nil {
			return "", false, "", err
		}
	}
	return rec.ID, had == nil, outlet, nil
}

// setOutletTier writes an outlet's tier on its company record.
func setOutletTier(ctx context.Context, tx pgx.Tx, a *access, c *catalog, outlet, tier string) error {
	rec, err := lockRecord(ctx, tx, a, outlet)
	if err != nil {
		return err
	}
	if rec.Attributes == nil {
		rec.Attributes = map[string]any{}
	}
	if rec.AttributeSources == nil {
		rec.AttributeSources = map[string]string{}
	}
	t := c.recordType("account")
	if t == nil {
		return errNotFound
	}
	b, _ := json.Marshal(tier)
	changed, err := applyAttributes(ctx, tx, c, t, a, &rec, map[string]json.RawMessage{"outlet_tier": b}, "manual")
	if err != nil || !changed {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
		rec.ID, rec.Attributes, rec.AttributeSources)
	return err
}

// splitPersonName is a name's first word and the rest.
func splitPersonName(n string) (string, string) {
	first, rest, _ := strings.Cut(strings.TrimSpace(n), " ")
	return first, strings.TrimSpace(rest)
}

// journalistOne reads one journalist the caller just wrote.
func journalistOne(ctx context.Context, tx pgx.Tx, ws, id string) (journalistView, error) {
	list, _, err := queryJournalists(ctx, tx, ws, brandSel{}, journalistFilter{only: id}, 1, 0)
	if err == nil && len(list) == 0 {
		err = errNotFound
	}
	if err != nil {
		return journalistView{}, err
	}
	return list[0], nil
}

func (s *Server) addJournalist(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		journalistInput
		BrandID json.RawMessage `json:"brand_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	in := req.journalistInput
	if s.allocError(w, r, in.check()) {
		return
	}
	brand, err := s.journalistBrand(ctx, a, req.BrandID)
	if s.allocError(w, r, err) {
		return
	}
	var view journalistView
	var created bool
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, brand, false)
		if err != nil {
			return err
		}
		id, made, outlet, err := putJournalist(ctx, tx, a, c, brand, "manual", "journalists", in)
		if err != nil {
			return err
		}
		created = made
		data := map[string]any{"record_id": id, "created": made, "outlet": outlet != "", "beats": len(in.Beats), "brand_id": brand}
		if in.OutletTier != "" {
			data["outlet_tier"] = in.OutletTier
		}
		if outlet != "" {
			data["outlet_record_id"] = outlet
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "journalist.added", "record", id, data); err != nil {
			return err
		}
		view, err = journalistOne(ctx, tx, a.WorkspaceID, id)
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, view)
}

func (s *Server) updateJournalist(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		Beats      *[]string       `json:"beats"`
		JobTitle   json.RawMessage `json:"job_title"`
		Journalist *bool           `json:"journalist"`
	}
	if !decode(w, r, &req) {
		return
	}
	attrs := map[string]json.RawMessage{}
	if req.Beats != nil {
		if len(*req.Beats) > news.MaxBeats {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a journalist has at most 30 beats")
			return
		}
		for _, b := range *req.Beats {
			if news.CleanBeat(b) == "" || len(b) > 200 {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "each beat is a topic, at most 100 characters")
				return
			}
		}
		beats := news.MergeBeats(nil, *req.Beats...)
		attrs["beats"], _ = json.Marshal(beats)
		if len(beats) == 0 {
			attrs["beats"] = json.RawMessage(`null`)
		}
	}
	if len(req.JobTitle) > 0 {
		var t *string
		if err := json.Unmarshal(req.JobTitle, &t); err != nil || (t != nil && len(strings.TrimSpace(*t)) > 200) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "job_title is text of at most 200 characters, or null")
			return
		}
		attrs["job_title"] = json.RawMessage(`null`)
		if t != nil && strings.TrimSpace(*t) != "" {
			attrs["job_title"], _ = json.Marshal(strings.TrimSpace(*t))
		}
	}
	if req.Journalist != nil {
		attrs["journalist"], _ = json.Marshal(*req.Journalist)
	}
	if len(attrs) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send beats, job_title or journalist")
		return
	}
	var view journalistView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := lockRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		if rec.Type != "person" {
			return errNotFound
		}
		if rec.Attributes == nil {
			rec.Attributes = map[string]any{}
		}
		if rec.AttributeSources == nil {
			rec.AttributeSources = map[string]string{}
		}
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, deref(rec.BrandID), false)
		if err != nil {
			return err
		}
		t := c.recordType("person")
		if t == nil {
			return errNotFound
		}
		changed, err := applyAttributes(ctx, tx, c, t, a, &rec, attrs, "manual")
		if err != nil {
			return err
		}
		if !changed {
			view, err = journalistOne(ctx, tx, a.WorkspaceID, rec.ID)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
			rec.ID, rec.Attributes, rec.AttributeSources); err != nil {
			return err
		}
		fields := make([]string, 0, len(attrs))
		for k := range attrs {
			fields = append(fields, k)
		}
		slices.Sort(fields)
		data := map[string]any{"record_id": rec.ID, "fields": fields, "beats": len(news.Beats(rec.Attributes["beats"]))}
		if req.Journalist != nil {
			data["journalist"] = *req.Journalist
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "journalist.updated", "record", rec.ID, data); err != nil {
			return err
		}
		view, err = journalistOne(ctx, tx, a.WorkspaceID, rec.ID)
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// errDryRun rolls back an import that only checks.
var errDryRun = errors.New("dry run")

func (s *Server) importJournalists(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "text/csv" && mt != "text/plain" {
		writeError(w, r, http.StatusUnsupportedMediaType, "invalid_request", "send the CSV file as the body with Content-Type: text/csv")
		return
	}
	q := r.URL.Query()
	dry := q.Get("dry_run") == "true"
	if v := q.Get("dry_run"); v != "" && v != "true" && v != "false" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "dry_run is true or false")
		return
	}
	ctx := r.Context()
	var rawBrand json.RawMessage
	if b := q.Get("brand"); b != "" {
		rawBrand, _ = json.Marshal(b)
	}
	brand, err := s.journalistBrand(ctx, a, rawBrand)
	if s.allocError(w, r, err) {
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxJournalistImportBytes)
	parsed, bad, err := leads.ParseCSV(body)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the file is larger than 5 MB")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	io.Copy(io.Discard, body) //nolint:errcheck
	// Rows repeating an email merge into the first: later values fill in,
	// and beats add up.
	var merged []journalistInput
	var first []leads.Lead
	at := map[string]int{}
	for _, l := range parsed {
		in := journalistFromRow(l)
		k := strings.ToLower(strings.TrimSpace(l.Email))
		i, seen := at[k]
		if !seen {
			at[k] = len(merged)
			merged, first = append(merged, in), append(first, l)
			continue
		}
		o := &merged[i]
		for _, p := range [][2]*string{{&o.Name, &in.Name}, {&o.JobTitle, &in.JobTitle}, {&o.OutletWebsite, &in.OutletWebsite},
			{&o.OutletName, &in.OutletName}, {&o.OutletTier, &in.OutletTier}} {
			if *p[1] != "" {
				*p[0] = *p[1]
			}
		}
		o.Beats = append(o.Beats, in.Beats...)
	}
	dups := len(parsed) - len(merged)
	if len(merged) > maxJournalistImport {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a file holds at most 5,000 journalists; split it")
		return
	}
	if bad == nil {
		bad = []leads.RowError{}
	}
	rows := make([]journalistInput, 0, len(merged))
	lines := make([]leads.Lead, 0, len(merged))
	for i, in := range merged {
		if len(in.Beats) > news.MaxBeats {
			in.Beats = news.MergeBeats(nil, in.Beats...)
		}
		if err := in.check(); err != nil {
			bad = append(bad, leads.RowError{Line: first[i].Line, Email: first[i].Email, Error: err.Error()})
			continue
		}
		rows = append(rows, in)
		lines = append(lines, first[i])
	}
	created, updated := 0, 0
	var batchID string
	run := ctx
	if dry {
		run = track.Mute(ctx)
	}
	err = pgx.BeginFunc(run, s.pool, func(tx pgx.Tx) error {
		if len(rows) > 0 && !dry {
			if batchID, err = startBatch(run, tx, &a.principal, a.WorkspaceID, brand, batchCSVImport, "journalists_csv", "Journalists", ""); err != nil {
				return err
			}
		}
		c, err := loadCatalog(run, tx, a.WorkspaceID, brand, false)
		if err != nil {
			return err
		}
		for i, in := range rows {
			// Each row in its own savepoint, so one that can't be written
			// is reported and the rest go in.
			var made bool
			err := pgx.BeginFunc(run, tx, func(tx pgx.Tx) error {
				var err error
				_, made, _, err = putJournalist(run, tx, a, c, brand, "import", "csv", in)
				return err
			})
			var inv errInvalid
			switch {
			case errors.As(err, &inv), errors.As(err, new(errDenied)), errors.Is(err, errNotFound), errors.As(err, new(recordConflict)):
				bad = append(bad, leads.RowError{Line: lines[i].Line, Email: lines[i].Email, Error: err.Error()})
				continue
			case err != nil:
				return err
			}
			if made {
				created++
			} else {
				updated++
			}
		}
		if dry {
			return errDryRun
		}
		if created+updated == 0 {
			return nil
		}
		return record(run, tx, &a.principal, a.WorkspaceID, "journalist.imported", "workspace", a.WorkspaceID, map[string]any{
			"created": created, "updated": updated, "duplicates": dups, "failed": len(bad), "batch_id": batchID, "brand_id": brand,
		})
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	if s.allocError(w, r, err) {
		return
	}
	slices.SortFunc(bad, func(x, y leads.RowError) int { return x.Line - y.Line })
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "updated": updated, "duplicates": dups, "errors": bad,
		"dry_run": dry, "brand_id": brand, "batch_id": nullIfEmpty(batchID)})
}

// journalistFromRow reads a file's row: the lead columns (email, name,
// title, company, website), with the outlet as the company and website,
// and beats and outlet_tier from their own columns.
func journalistFromRow(l leads.Lead) journalistInput {
	in := journalistInput{Email: l.Email, Name: strings.TrimSpace(l.FirstName + " " + l.LastName), JobTitle: l.Title,
		OutletWebsite: l.Website, OutletName: l.Company}
	if in.OutletWebsite == "" && customers.CompanyDomain(l.Company) != "" {
		in.OutletWebsite, in.OutletName = l.Company, ""
	}
	for _, k := range []string{"outlet_tier", "tier", "outlet_type"} {
		if v := l.Custom[k]; v != "" {
			in.OutletTier = v
			break
		}
	}
	if strings.EqualFold(strings.TrimSpace(in.OutletTier), "newsletter or blog") || strings.EqualFold(strings.TrimSpace(in.OutletTier), "blog") {
		in.OutletTier = "newsletter"
	}
	for _, k := range []string{"beats", "beat", "topics"} {
		if v := l.Custom[k]; v != "" {
			for _, b := range strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == ',' || r == '|' }) {
				if b = strings.TrimSpace(b); b != "" {
					in.Beats = append(in.Beats, b)
				}
			}
			break
		}
	}
	return in
}
