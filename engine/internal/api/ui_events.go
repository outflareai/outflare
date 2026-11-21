package api

import (
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/track"
)

// The web app's own events: what happens only in the browser (an error
// shown, a crash, a request that got no answer, a page opened, a step with
// no request). The app posts them here and the server records them like its
// own events, so they reach wherever the operator keeps events (package
// track) and a browser never needs to talk to an analytics service for them.

const (
	maxUIEvents     = 50
	maxUIProperties = 30
	maxUIText       = 200
)

var (
	uiEventName = regexp.MustCompile(`^ui\.[a-z0-9_]{1,60}$`)
	uiPropName  = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
	uiRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
)

type uiEvent struct {
	Name       string         `json:"name"`
	OccurredAt *time.Time     `json:"occurred_at"`
	Properties map[string]any `json:"properties"`
}

func (s *Server) routesUIEvents(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/ui-events", s.requireUser(s.recordUIEvents))
}

func (s *Server) recordUIEvents(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WorkspaceID string    `json:"workspace_id"`
		Surface     string    `json:"surface"`
		Events      []uiEvent `json:"events"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Events) == 0 || len(in.Events) > maxUIEvents {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "events must hold 1 to 50 events")
		return
	}
	if in.Surface == "" {
		in.Surface = "web"
	}
	if in.Surface != "web" && in.Surface != "desktop_app" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "surface must be web or desktop_app")
		return
	}
	p := principalFrom(r.Context())
	ws := ""
	if in.WorkspaceID != "" {
		// Only a workspace the person is in is written on their events.
		if !validUUID(in.WorkspaceID) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "workspace_id must be a workspace's id")
			return
		}
		var one int
		err := s.pool.QueryRow(r.Context(), `SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id = $2 LIMIT 1`, in.WorkspaceID, p.UserID).Scan(&one)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return
		}
		if err == nil {
			ws = in.WorkspaceID
			noteWorkspace(r, ws)
		}
	}
	now := time.Now().UTC()
	events := make([]track.Event, 0, len(in.Events))
	for _, e := range in.Events {
		if !uiEventName.MatchString(e.Name) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "events must be named ui. and lowercase letters, digits and underscores")
			return
		}
		if len(e.Properties) > maxUIProperties {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "events must have at most 30 properties each")
			return
		}
		ev := track.Event{Name: e.Name, UserID: p.personID(), ActorID: p.ActorID, ActorType: p.ActorType, WorkspaceID: ws,
			Surface: in.Surface, RequestID: RequestID(r.Context()), Props: map[string]any{}, At: now}
		if p.ViewerID != "" {
			ev.UserID = p.ViewerID
		}
		// A browser's clock can be wrong: a time is kept only when it is
		// in the last day, which covers events queued while offline.
		if t := e.OccurredAt; t != nil && !t.After(now) && now.Sub(*t) < 24*time.Hour {
			ev.At = t.UTC()
		}
		for k, v := range e.Properties {
			if !uiPropName.MatchString(k) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "events must name properties in lowercase letters, digits and underscores")
				return
			}
			switch t := v.(type) {
			case string:
				if len(t) > maxUIText {
					t = t[:maxUIText]
				}
				switch k {
				case "outcome":
					if t == track.Failure || t == track.Error {
						ev.Outcome = t
					}
				case "error_code":
					ev.ErrorCode = t
				case "request_id":
					// The call an error message came from, so the event
					// leads to that call's log line.
					if uiRequestID.MatchString(t) {
						ev.RequestID = t
					}
				case "surface", "workspace_id", "actor_id", "actor_type":
					// The server says these itself.
				default:
					ev.Props[k] = t
				}
			case float64, bool:
				ev.Props[k] = t
			case nil:
			default:
				writeError(w, r, http.StatusBadRequest, "invalid_request", "events must give each property as a string, a number or true or false")
				return
			}
		}
		// An error code without an outcome is something refused.
		if ev.ErrorCode != "" && ev.Outcome == "" {
			ev.Outcome = track.Failure
		}
		events = append(events, ev)
	}
	for _, ev := range events {
		track.Emit(r.Context(), ev)
	}
	w.WriteHeader(http.StatusNoContent)
}
