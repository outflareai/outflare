package linkorders

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Target kinds: the brand's own page a paid link points at.
const (
	TargetArticle = "article"
	TargetProduct = "product"
)

// How an order's target was chosen.
const (
	ChoiceManual    = "manual"
	ChoiceSuggested = "suggested"
)

// Target is one saved page on the brand's own site that a link can point
// at. Selected is the checkbox: only ticked rows are offered when a link
// is ordered.
type Target struct {
	ID        uuid.UUID `json:"id"`
	URL       string    `json:"url"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Selected  bool      `json:"selected"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TargetInput is a new or updated saved target. Nil fields keep the
// stored value (or the default on insert).
type TargetInput struct {
	URL      string
	Kind     *string
	Title    *string
	Selected *bool
	Source   string
}

func validKind(k string) bool { return k == TargetArticle || k == TargetProduct }

// NormalizeTargetURL keeps one spelling per page: an http(s) URL with a
// lowercase host without www., no fragment, no query and no trailing
// slash except for the home page.
func NormalizeTargetURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: url is required", ErrInvalid)
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", fmt.Errorf("%w: url must be an http(s) address", ErrInvalid)
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if !strings.Contains(host, ".") && host != "localhost" {
		return "", fmt.Errorf("%w: url must be an http(s) address", ErrInvalid)
	}
	if p := u.Port(); p != "" {
		host += ":" + p
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	out := u.Scheme + "://" + host + path
	if len(out) > 2048 {
		return "", fmt.Errorf("%w: url is longer than 2048 characters", ErrInvalid)
	}
	return out, nil
}

// targetHost is the normalised host of a normalised URL.
func targetHost(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(p.Host), "www.")
}

// BrandHosts are the hosts a target may live on: the brand's website and
// domains and its SEO sites, lowercase without www.
func BrandHosts(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID) (name string, hosts []string, err error) {
	var website string
	var domains []string
	if err = q.QueryRow(ctx, `SELECT name, website, domains FROM brands WHERE workspace_id = $1 AND id = $2`,
		workspaceID, brandID).Scan(&name, &website, &domains); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, ErrNotFound
		}
		return "", nil, err
	}
	rows, err := q.Query(ctx, `SELECT host FROM seo_sites WHERE workspace_id = $1 AND brand_id = $2`, workspaceID, brandID)
	if err != nil {
		return "", nil, err
	}
	sites, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", nil, err
	}
	seen := map[string]bool{}
	add := func(h string) {
		h = strings.TrimSpace(strings.ToLower(h))
		if i := strings.Index(h, "://"); i >= 0 {
			h = h[i+3:]
		}
		h, _, _ = strings.Cut(h, "/")
		h = strings.TrimPrefix(h, "www.")
		if h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	add(website)
	for _, d := range domains {
		add(d)
	}
	for _, s := range sites {
		add(s)
	}
	return name, hosts, nil
}

// onBrandHost reports whether u's host is one of hosts or a subdomain of
// one. With no known hosts every host is accepted, since the brand has
// not said what its site is yet.
func onBrandHost(u string, hosts []string) bool {
	if len(hosts) == 0 {
		return true
	}
	h := targetHost(u)
	hn, _, _ := strings.Cut(h, ":")
	for _, b := range hosts {
		bn, _, _ := strings.Cut(b, ":")
		if h == b || hn == bn || strings.HasSuffix(hn, "."+bn) {
			return true
		}
	}
	return false
}

// GuessKind says whether a page looks like an article or a product page
// from its address alone. It is only a default: a saved target's kind is
// what a person chose.
func GuessKind(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return TargetProduct
	}
	path := "/" + strings.Trim(strings.ToLower(p.Path), "/") + "/"
	for _, m := range []string{"/blog/", "/blogs/", "/article/", "/articles/", "/news/", "/guide/", "/guides/", "/learn/",
		"/resources/", "/resource/", "/post/", "/posts/", "/tutorial/", "/tutorials/", "/insights/", "/academy/", "/wiki/", "/glossary/"} {
		if strings.Contains(path, m) {
			return TargetArticle
		}
	}
	for _, m := range []string{"/how-to-", "/what-is-", "/best-", "-vs-", "/top-"} {
		if strings.Contains(path, m) {
			return TargetArticle
		}
	}
	return TargetProduct
}

const targetColumns = `id, url, kind, title, selected, source, created_at, updated_at`

func scanTarget(r pgx.Row) (Target, error) {
	var t Target
	err := r.Scan(&t.ID, &t.URL, &t.Kind, &t.Title, &t.Selected, &t.Source, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// TargetFilter narrows ListTargets.
type TargetFilter struct {
	Kind     string
	Selected *bool
}

// ListTargets returns the brand's saved targets, ticked first, then by URL.
func ListTargets(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, f TargetFilter) ([]Target, error) {
	where := []string{"workspace_id = $1", "brand_id = $2"}
	args := []any{workspaceID, brandID}
	if f.Kind != "" {
		args = append(args, f.Kind)
		where = append(where, fmt.Sprintf("kind = $%d", len(args)))
	}
	if f.Selected != nil {
		args = append(args, *f.Selected)
		where = append(where, fmt.Sprintf("selected = $%d", len(args)))
	}
	rows, err := q.Query(ctx, `SELECT `+targetColumns+` FROM coverage_link_targets WHERE `+strings.Join(where, " AND ")+`
		ORDER BY selected DESC, kind, url LIMIT 2000`, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Target, error) { return scanTarget(r) })
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Target{}
	}
	return out, nil
}

// GetTarget returns one saved target, or ErrNotFound.
func GetTarget(ctx context.Context, q Querier, workspaceID, brandID, id uuid.UUID) (*Target, error) {
	t, err := scanTarget(q.QueryRow(ctx, `SELECT `+targetColumns+` FROM coverage_link_targets
		WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`, workspaceID, brandID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// SaveTarget inserts a target, or updates the saved row with the same
// URL. created reports whether a row was inserted. The URL must be on one
// of the brand's own hosts.
func SaveTarget(ctx context.Context, tx pgx.Tx, workspaceID, brandID, actorID uuid.UUID, in TargetInput) (*Target, bool, error) {
	u, err := NormalizeTargetURL(in.URL)
	if err != nil {
		return nil, false, err
	}
	_, hosts, err := BrandHosts(ctx, tx, workspaceID, brandID)
	if err != nil {
		return nil, false, err
	}
	if !onBrandHost(u, hosts) {
		return nil, false, fmt.Errorf("%w: url must be a page on one of the brand's own sites (%s)", ErrInvalid, strings.Join(hosts, ", "))
	}
	kind := GuessKind(u)
	if in.Kind != nil {
		if !validKind(*in.Kind) {
			return nil, false, fmt.Errorf("%w: kind is article or product", ErrInvalid)
		}
		kind = *in.Kind
	}
	title := ""
	if in.Title != nil {
		title = strings.TrimSpace(*in.Title)
		if len([]rune(title)) > 500 {
			return nil, false, fmt.Errorf("%w: title is longer than 500 characters", ErrInvalid)
		}
	}
	selected := true
	if in.Selected != nil {
		selected = *in.Selected
	}
	source := in.Source
	if source == "" {
		source = SourceManual
	}
	if source != SourceManual && source != ChoiceSuggested {
		return nil, false, fmt.Errorf("%w: source is manual or suggested", ErrInvalid)
	}
	var t Target
	var inserted bool
	err = tx.QueryRow(ctx, `
		INSERT INTO coverage_link_targets (workspace_id, brand_id, url, kind, title, selected, source, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		ON CONFLICT (workspace_id, brand_id, url) DO UPDATE SET
			kind = CASE WHEN $9 THEN EXCLUDED.kind ELSE coverage_link_targets.kind END,
			title = CASE WHEN $10 THEN EXCLUDED.title ELSE coverage_link_targets.title END,
			selected = CASE WHEN $11 THEN EXCLUDED.selected ELSE coverage_link_targets.selected END,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()
		RETURNING `+targetColumns+`, (xmax = 0)`,
		workspaceID, brandID, u, kind, title, selected, source, nilIfNil(actorID),
		in.Kind != nil, in.Title != nil, in.Selected != nil,
	).Scan(&t.ID, &t.URL, &t.Kind, &t.Title, &t.Selected, &t.Source, &t.CreatedAt, &t.UpdatedAt, &inserted)
	if err != nil {
		return nil, false, err
	}
	return &t, inserted, nil
}

// PatchTarget changes a saved target's kind, title or checkbox.
func PatchTarget(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id, actorID uuid.UUID, kind, title *string, selected *bool) (*Target, error) {
	cur, err := GetTarget(ctx, tx, workspaceID, brandID, id)
	if err != nil {
		return nil, err
	}
	if kind != nil && !validKind(*kind) {
		return nil, fmt.Errorf("%w: kind is article or product", ErrInvalid)
	}
	if title != nil {
		t := strings.TrimSpace(*title)
		if len([]rune(t)) > 500 {
			return nil, fmt.Errorf("%w: title is longer than 500 characters", ErrInvalid)
		}
		title = &t
	}
	if kind == nil && title == nil && selected == nil {
		return cur, nil
	}
	t, err := scanTarget(tx.QueryRow(ctx, `
		UPDATE coverage_link_targets SET
			kind = COALESCE($4, kind), title = COALESCE($5, title), selected = COALESCE($6, selected),
			updated_by = $7, updated_at = now()
		WHERE workspace_id = $1 AND brand_id = $2 AND id = $3
		RETURNING `+targetColumns, workspaceID, brandID, id, kind, title, selected, nilIfNil(actorID)))
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// resolveOrderTarget fills and checks an order's target. With a saved
// target, the URL defaults to the target's and must be the same page,
// the target must be ticked, and the kind defaults to the target's. A
// typed URL keeps whatever kind is sent (or none). Choice defaults to
// manual.
func resolveOrderTarget(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, targetURL *string, targetID **uuid.UUID, kind, choice *string) error {
	*targetURL = strings.TrimSpace(*targetURL)
	if *targetID != nil {
		t, err := GetTarget(ctx, q, workspaceID, brandID, **targetID)
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: target_id is not one of this brand's link targets", ErrInvalid)
		}
		if err != nil {
			return err
		}
		if !t.Selected {
			return fmt.Errorf("%w: that link target is unticked; tick it on Targets first", ErrInvalid)
		}
		if *targetURL == "" {
			*targetURL = t.URL
		} else if normURL(*targetURL) != t.URL {
			return fmt.Errorf("%w: target_url and target_id are different pages", ErrInvalid)
		}
		if *kind == "" {
			*kind = t.Kind
		}
	}
	if *targetURL == "" {
		return fmt.Errorf("%w: target_url or target_id is required", ErrInvalid)
	}
	if len(*targetURL) > 2048 {
		return fmt.Errorf("%w: target_url is longer than 2048 characters", ErrInvalid)
	}
	if *kind != "" && !validKind(*kind) {
		return fmt.Errorf("%w: target_kind is article or product", ErrInvalid)
	}
	switch *choice {
	case "":
		*choice = ChoiceManual
	case ChoiceManual, ChoiceSuggested:
	default:
		return fmt.Errorf("%w: target_choice is manual or suggested", ErrInvalid)
	}
	return nil
}
