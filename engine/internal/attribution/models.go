package attribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/utm"
)

// Spec is how a model shares a conversion between the touches before it.
// The five built-in models are specs with only Base set; a workspace's own
// models (attribution_models) add options on top of one of them.
type Spec struct {
	// Base is the built-in model it starts from.
	Base string `json:"base"`
	// LookbackDays is how far back a touch can be: 0 for the workspace's.
	LookbackDays int `json:"lookback_days,omitempty"`
	// ChannelLookbackDays gives channels their own window, such as 7 days
	// for paid ads.
	ChannelLookbackDays map[string]int `json:"channel_lookback_days,omitempty"`
	// ExcludeChannels never get credit.
	ExcludeChannels []string `json:"exclude_channels,omitempty"`
	// FirstWeight and LastWeight are the position model's shares for the
	// first and last touch (0.4 each by default); the middle shares the
	// rest.
	FirstWeight *float64 `json:"first_weight,omitempty"`
	LastWeight  *float64 `json:"last_weight,omitempty"`
	// HalfLifeDays is the time-decay model's half-life (7 by default).
	HalfLifeDays float64 `json:"half_life_days,omitempty"`
}

// Channels a spec can name: every campaign channel, and direct.
func Channels() []string {
	out := make([]string, 0, len(utm.Channels)+1)
	for _, c := range utm.Channels {
		out = append(out, c.ID)
	}
	return append(out, Direct)
}

// Validate reports what is wrong with a spec, for a person to fix.
func (s Spec) Validate() error {
	if !ValidModel(s.Base) {
		return fmt.Errorf("base is one of %s", strings.Join(Models, ", "))
	}
	if s.LookbackDays < 0 || s.LookbackDays > 365 {
		return errors.New("lookback_days must be 0 (the workspace's) to 365")
	}
	channels := Channels()
	for ch, days := range s.ChannelLookbackDays {
		if !slices.Contains(channels, ch) {
			return fmt.Errorf("channel_lookback_days: %q is not a channel (%s)", ch, strings.Join(channels, ", "))
		}
		if days < 1 || days > 365 {
			return fmt.Errorf("channel_lookback_days: %s must be 1 to 365 days", ch)
		}
	}
	for _, ch := range s.ExcludeChannels {
		if !slices.Contains(channels, ch) {
			return fmt.Errorf("exclude_channels: %q is not a channel (%s)", ch, strings.Join(channels, ", "))
		}
	}
	if (s.FirstWeight != nil || s.LastWeight != nil) && s.Base != Position {
		return errors.New("first_weight and last_weight are for the position model")
	}
	first, last := s.positionWeights()
	if first < 0 || last < 0 || first+last > 1 || first+last == 0 {
		return errors.New("first_weight and last_weight are 0 to 1 each, and add up to more than 0 and at most 1")
	}
	if s.HalfLifeDays != 0 && s.Base != TimeDecay {
		return errors.New("half_life_days is for the time_decay model")
	}
	if s.HalfLifeDays < 0 || s.HalfLifeDays > 365 || (s.HalfLifeDays > 0 && s.HalfLifeDays < 0.5) {
		return errors.New("half_life_days must be 0.5 to 365")
	}
	return nil
}

func (s Spec) positionWeights() (first, last float64) {
	first, last = 0.4, 0.4
	if s.FirstWeight != nil {
		first = *s.FirstWeight
	}
	if s.LastWeight != nil {
		last = *s.LastWeight
	}
	return first, last
}

// counts reports whether a touch can get credit under the spec.
func (s Spec) counts(t Point, at time.Time) bool {
	if slices.Contains(s.ExcludeChannels, t.Channel) {
		return false
	}
	days := s.LookbackDays
	if d, ok := s.ChannelLookbackDays[t.Channel]; ok {
		days = d
	}
	return days == 0 || !t.At.Before(at.Add(-time.Duration(days)*24*time.Hour))
}

// window is the furthest back the spec looks, with def for the
// workspace's look-back.
func (s Spec) window(def int) int {
	days := s.LookbackDays
	if days == 0 {
		days = def
	}
	for _, d := range s.ChannelLookbackDays {
		days = max(days, d)
	}
	return days
}

// CreditModel is a model the runner credits conversions under: its id in
// attribution_credits.model and how it shares them.
type CreditModel struct {
	ID   string
	Spec Spec
}

// customID is a model version's id in attribution_credits.model.
func customID(versionID string) string { return "m:" + versionID }

// creditModels are the models a workspace's conversions are credited
// under: the five built in, each custom model's latest version unless
// archived, and every pinned version. lookback is the workspace's, given
// to specs without their own.
func creditModels(ctx context.Context, q pgx.Tx, ws string, lookback int) ([]CreditModel, error) {
	out := make([]CreditModel, 0, len(Models))
	for _, m := range Models {
		out = append(out, CreditModel{ID: m, Spec: Spec{Base: m, LookbackDays: lookback}})
	}
	rows, err := q.Query(ctx, `
		SELECT m.id::text, m.spec FROM attribution_models m
		WHERE m.workspace_id = $1 AND (
			(m.archived_at IS NULL AND m.version = (SELECT max(version) FROM attribution_models l WHERE l.workspace_id = m.workspace_id AND l.key = m.key))
			OR EXISTS (SELECT 1 FROM attribution_model_pins p WHERE p.model_id = m.id))
		ORDER BY m.key, m.version`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var s Spec
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		if s.LookbackDays == 0 {
			s.LookbackDays = lookback
		}
		out = append(out, CreditModel{ID: customID(id), Spec: s})
	}
	return out, rows.Err()
}

// Model is one version of a workspace's own model.
type Model struct {
	ID         string     `json:"id"`
	Key        string     `json:"key"`
	Version    int        `json:"version"`
	Name       string     `json:"name"`
	Spec       Spec       `json:"spec"`
	CreatedAt  time.Time  `json:"created_at"`
	ArchivedAt *time.Time `json:"archived_at"`
	// PinnedBy lists the modules that pin this version, as module:ref.
	PinnedBy []string `json:"pinned_by"`
}

// Errors from resolving a model reference.
var (
	ErrUnknownModel = errors.New("no such model")
	// ErrNotCredited is a version that is neither the latest nor pinned,
	// so no conversion is credited under it.
	ErrNotCredited = errors.New("that version is not credited: only a model's latest version and pinned versions are")
)

var refRE = regexp.MustCompile(`^([a-z][a-z0-9-]{0,39})(?:@([1-9][0-9]{0,5}))?$`)

// KeyRE is what a model key looks like.
var KeyRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// Querier runs queries, in or out of a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Resolve turns a model reference into the id its credits are stored
// under: a built-in model's id, a workspace model's key (its latest
// version), or key@version. It also returns the version for a workspace
// model.
func Resolve(ctx context.Context, q Querier, ws, ref string) (string, *Model, error) {
	if ValidModel(ref) {
		return ref, nil, nil
	}
	m := refRE.FindStringSubmatch(ref)
	if m == nil {
		return "", nil, ErrUnknownModel
	}
	var version *int
	if m[2] != "" {
		v, _ := strconv.Atoi(m[2])
		version = &v
	}
	model, credited, err := loadModel(ctx, q, ws, m[1], version)
	if err != nil {
		return "", nil, err
	}
	if !credited {
		return "", nil, ErrNotCredited
	}
	return customID(model.ID), model, nil
}

// loadModel reads a model's version (its latest when version is nil), and
// whether conversions are credited under it.
func loadModel(ctx context.Context, q Querier, ws, key string, version *int) (*Model, bool, error) {
	var m Model
	var raw []byte
	var latest bool
	err := q.QueryRow(ctx, `
		SELECT m.id::text, m.key, m.version, m.name, m.spec, m.created_at, m.archived_at,
			m.version = (SELECT max(version) FROM attribution_models l WHERE l.workspace_id = m.workspace_id AND l.key = m.key),
			coalesce((SELECT array_agg(p.module || ':' || p.ref ORDER BY p.module, p.ref) FROM attribution_model_pins p WHERE p.model_id = m.id), '{}')
		FROM attribution_models m
		WHERE m.workspace_id = $1 AND m.key = $2 AND ($3::int IS NULL OR m.version = $3)
		ORDER BY m.version DESC LIMIT 1`, ws, key, version).
		Scan(&m.ID, &m.Key, &m.Version, &m.Name, &raw, &m.CreatedAt, &m.ArchivedAt, &latest, &m.PinnedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrUnknownModel
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(raw, &m.Spec); err != nil {
		return nil, false, err
	}
	return &m, (latest && m.ArchivedAt == nil) || len(m.PinnedBy) > 0, nil
}

// Pin keeps the version ref names (key@version, or key for its latest)
// credited for a module, such as an affiliate program, so editing the
// model never changes what that module counted. It returns the version.
func Pin(ctx context.Context, q Querier, ws, ref, module, refID string) (*Model, error) {
	_, model, err := Resolve(ctx, q, ws, ref)
	if err != nil {
		return nil, err
	}
	if model == nil {
		return nil, errors.New("built-in models never change, so there is nothing to pin")
	}
	if _, err := q.Exec(ctx, `INSERT INTO attribution_model_pins (model_id, workspace_id, module, ref) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, model.ID, ws, module, refID); err != nil {
		return nil, err
	}
	return model, nil
}

// Unpin lets go of a module's pins on a workspace's models.
func Unpin(ctx context.Context, q Querier, ws, module, refID string) error {
	_, err := q.Exec(ctx, `DELETE FROM attribution_model_pins WHERE workspace_id = $1 AND module = $2 AND ref = $3`, ws, module, refID)
	return err
}

const modelCols = `m.id::text, m.key, m.version, m.name, m.spec, m.created_at, m.archived_at,
	coalesce((SELECT array_agg(p.module || ':' || p.ref ORDER BY p.module, p.ref) FROM attribution_model_pins p WHERE p.model_id = m.id), '{}')`

func scanModels(rows pgx.Rows) ([]Model, error) {
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		var m Model
		var raw []byte
		if err := rows.Scan(&m.ID, &m.Key, &m.Version, &m.Name, &raw, &m.CreatedAt, &m.ArchivedAt, &m.PinnedBy); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &m.Spec); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LatestModels lists each of a workspace's models at its latest version,
// archived ones too when archived is true.
func LatestModels(ctx context.Context, q Querier, ws string, archived bool) ([]Model, error) {
	rows, err := q.Query(ctx, `SELECT `+modelCols+` FROM attribution_models m
		WHERE m.workspace_id = $1 AND ($2 OR m.archived_at IS NULL)
		  AND m.version = (SELECT max(version) FROM attribution_models l WHERE l.workspace_id = m.workspace_id AND l.key = m.key)
		ORDER BY m.key`, ws, archived)
	if err != nil {
		return nil, err
	}
	return scanModels(rows)
}

// Versions lists every version of a model, newest first.
func Versions(ctx context.Context, q Querier, ws, key string) ([]Model, error) {
	rows, err := q.Query(ctx, `SELECT `+modelCols+` FROM attribution_models m WHERE m.workspace_id = $1 AND m.key = $2 ORDER BY m.version DESC`, ws, key)
	if err != nil {
		return nil, err
	}
	return scanModels(rows)
}

// SaveVersion adds a model's next version (version 1 for a new key) and
// returns it. The caller credits conversions again.
func SaveVersion(ctx context.Context, q Querier, ws, key, name string, spec Spec, actor *string) (Model, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return Model{}, err
	}
	var id string
	if err := q.QueryRow(ctx, `INSERT INTO attribution_models (workspace_id, key, version, name, spec, created_by)
		SELECT $1, $2, coalesce(max(version), 0) + 1, $3, $4, $5 FROM attribution_models WHERE workspace_id = $1 AND key = $2
		RETURNING id::text`, ws, key, name, raw, actor).Scan(&id); err != nil {
		return Model{}, err
	}
	rows, err := q.Query(ctx, `SELECT `+modelCols+` FROM attribution_models m WHERE m.id = $1`, id)
	if err != nil {
		return Model{}, err
	}
	list, err := scanModels(rows)
	if err != nil || len(list) == 0 {
		return Model{}, errors.Join(err, ErrUnknownModel)
	}
	return list[0], nil
}
