package api

import (
	"context"
	"net/http"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/channels"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/playbooks"
)

// Channels (docs/channels.md): the channel framework's catalog, with
// each channel's readiness for the workspace or for one brand, so an
// agent chooses what it can work on now and a person sees what to
// connect next.

func (s *Server) routesChannels(mux *http.ServeMux) {
	read := auth.ScopeWorkspaceRead
	mux.HandleFunc("GET /v1/workspaces/{ws}/channels", s.inBrand(read, s.listChannels))
	mux.HandleFunc("GET /v1/brands/{brand}/channels", s.onBrand(read, s.listBrandChannels))
}

type channelView struct {
	channels.Channel
	Readiness channels.Readiness `json:"readiness"`
}

// channelReadiness lists the channels with their readiness, from the
// workspace's connections (those serving brand, when set) and data.
func (s *Server) channelReadiness(ctx context.Context, ws, brand string) ([]channelView, error) {
	var connected []playbooks.ConnectorInfo
	if s.integrations != nil {
		list, err := s.integrations.List(ctx, s.pool, ws, "")
		if err != nil {
			return nil, err
		}
		if brand != "" {
			list = connectors.ForBrand(list, brand)
		}
		for _, i := range list {
			if i.Status != connectors.StatusConnected {
				continue
			}
			if d, ok := s.integrations.Registry.Definition(i.Connector); ok {
				connected = append(connected, playbooks.ConnectorInfo{Category: d.Category, Capabilities: d.Capabilities})
			}
		}
	}
	have, err := channels.HaveData(ctx, s.pool, ws)
	if err != nil {
		return nil, err
	}
	out := []channelView{}
	for _, c := range channels.All() {
		out = append(out, channelView{Channel: c, Readiness: c.Check(connected, have)})
	}
	return out, nil
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	out, err := s.channelReadiness(r.Context(), a.WorkspaceID, brand.id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) listBrandChannels(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	out, err := s.channelReadiness(r.Context(), a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}
