package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// A brand's reporting time zone (plans/ad-tracker.md, B4a). Stored times
// stay in UTC; reports bucket them into the brand's days and hours when
// they read them, and tracker caps reset at the brand's midnight.

// validTimezone says name is an IANA time zone a brand can report in.
func validTimezone(name string) bool {
	if name == "" || name == "Local" || len(name) > 64 {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

var zones sync.Map // name → *time.Location

// zone is the named time zone, UTC for one Go doesn't know. Zones are
// loaded once per server.
func zone(name string) *time.Location {
	if name == "" || name == "UTC" {
		return time.UTC
	}
	if l, ok := zones.Load(name); ok {
		return l.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil || name == "Local" {
		loc = time.UTC
	}
	zones.Store(name, loc)
	return loc
}

// brandZone is a brand's reporting time zone.
func (s *Server) brandZone(ctx context.Context, ws, brand string) (*time.Location, error) {
	var tz string
	err := s.pool.QueryRow(ctx, `SELECT timezone FROM brands WHERE workspace_id = $1 AND id = $2`, ws, brand).Scan(&tz)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.UTC, nil
	}
	if err != nil {
		return time.UTC, err
	}
	return zone(tz), nil
}

// ownerZone is the reporting time zone of the brand a link or flow belongs
// to.
func (s *Server) ownerZone(ctx context.Context, ws string, o routeOwner) (*time.Location, error) {
	var tz string
	err := s.pool.QueryRow(ctx, `SELECT b.timezone FROM `+o.source()+` JOIN brands b ON b.id = o.b`, ws, o.id).Scan(&tz)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.UTC, nil
	}
	return zone(tz), err
}

// startOfDay is the start of t's day in loc.
func startOfDay(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// brandToday is today in the request's brand's time zone: its date, and
// when it started.
func (s *Server) brandToday(ctx context.Context) (string, time.Time) {
	a := accessFrom(ctx)
	loc, err := s.brandZone(ctx, a.WorkspaceID, a.Brand)
	if err != nil {
		s.log.Warn("brand time zone", "err", err)
	}
	now := time.Now()
	return capDay(now, loc), startOfDay(now, loc)
}

// civilDay is t's date in loc, as a date (midnight UTC), the form date
// query parameters parse to.
func civilDay(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// dayStart is when the date day (as parsed, midnight UTC) begins in loc.
func dayStart(day time.Time, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
}

// reportZone is the time zone a brand report buckets in: ?timezone= when
// given, else the brand's. It writes the error and returns false for a bad
// one.
func (s *Server) reportZone(w http.ResponseWriter, r *http.Request) (*time.Location, bool) {
	if v := r.URL.Query().Get("timezone"); v != "" {
		if !validTimezone(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "timezone is an IANA time zone, such as Europe/Berlin")
			return nil, false
		}
		return zone(v), true
	}
	a := accessFrom(r.Context())
	loc, err := s.brandZone(r.Context(), a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return loc, true
}
