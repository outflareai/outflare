package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/datanetwork"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The data network (internal/datanetwork, plans/data-network.md): a
// workspace's owner can join, after seeing what would be shared, and an
// owner or admin can leave. Every owner and admin is emailed either way.
// It is internal for now: only workspaces the network is turned on for
// see it (Network.Enabled); to the rest it isn't there.

const jobDataNetworkEmail = "data_network.email"

func (s *Server) routesDataNetwork(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/data-network", s.inWorkspace(auth.ScopeLeadsRead, s.getDataNetwork))
	mux.HandleFunc("GET /v1/workspaces/{ws}/data-network/preview", s.inWorkspace(auth.ScopeLeadsRead, s.previewDataNetwork))
	mux.HandleFunc("POST /v1/workspaces/{ws}/data-network/join", s.inWorkspace(auth.ScopeIntegrationsManage, s.joinDataNetwork))
	mux.HandleFunc("POST /v1/workspaces/{ws}/data-network/leave", s.inWorkspace(auth.ScopeIntegrationsManage, s.leaveDataNetwork))
}

type dataNetworkAllowanceView struct {
	Unlimited bool       `json:"unlimited"`
	Limit     *int       `json:"limit"`
	Used      int        `json:"used"`
	ResetsAt  *time.Time `json:"resets_at"`
}

type dataNetworkView struct {
	Available  bool                      `json:"available"`
	Status     string                    `json:"status"`
	JoinedAt   *time.Time                `json:"joined_at"`
	LeftAt     *time.Time                `json:"left_at"`
	WithdrawBy *time.Time                `json:"withdraw_by"`
	Allowance  *dataNetworkAllowanceView `json:"allowance"`
}

func (s *Server) dataNetworkView(ctx context.Context, ws string) (dataNetworkView, error) {
	m, err := datanetwork.Load(ctx, s.pool, ws)
	if err != nil {
		return dataNetworkView{}, err
	}
	v := dataNetworkView{Available: s.dataNetwork.Available(ctx, ws), Status: m.Status, JoinedAt: m.JoinedAt, LeftAt: m.LeftAt}
	if m.Status == datanetwork.NotJoined {
		v.JoinedAt = nil
	}
	if m.Status == datanetwork.Left && m.WithdrawnAt == nil {
		v.WithdrawBy = m.WithdrawDueAt
	}
	a, ok, err := s.dataNetwork.Allowance(ctx, ws, m.Status == datanetwork.Joined)
	if err != nil {
		// The page still shows membership; the allowance is left out.
		track.Fail(ctx, s.log, track.Event{Name: "data_network.allowance_failed", WorkspaceID: ws, ActorType: "system", Surface: "system"}, "unavailable", err)
	}
	if ok {
		av := &dataNetworkAllowanceView{Unlimited: a.Unlimited, Used: a.Used}
		if !a.Unlimited {
			limit := a.Limit
			av.Limit = &limit
		}
		if !a.ResetsAt.IsZero() {
			at := a.ResetsAt
			av.ResetsAt = &at
		}
		v.Allowance = av
	}
	return v, nil
}

func (s *Server) getDataNetwork(w http.ResponseWriter, r *http.Request) {
	v, err := s.dataNetworkView(r.Context(), accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) previewDataNetwork(w http.ResponseWriter, r *http.Request) {
	ws := accessFrom(r.Context()).WorkspaceID
	if !s.dataNetwork.Available(r.Context(), ws) {
		writeError(w, r, http.StatusConflict, "data_network_unavailable", "the data network isn't available for this workspace")
		return
	}
	p, err := datanetwork.PreviewCounts(r.Context(), s.pool, ws)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"addresses": p.Addresses, "delivered": p.Delivered, "bounced": p.Bounced,
		"domains": p.Domains, "settle_days": int(datanetwork.SettleAfter / (24 * time.Hour)),
		"withdraw_days": int(datanetwork.WithdrawWithin / (24 * time.Hour))})
}

func (s *Server) joinDataNetwork(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	if a.Role != auth.RoleOwner || a.TokenID != "" || a.UserID == "" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only the workspace's owner, signed in, can join the data network")
		return
	}
	if !req.Confirm {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "confirm must be true: show the owner what will be shared first (GET .../data-network/preview)")
		return
	}
	if !s.dataNetwork.Available(ctx, a.WorkspaceID) {
		writeError(w, r, http.StatusConflict, "data_network_unavailable", "the data network isn't available for this workspace")
		return
	}
	p, err := datanetwork.PreviewCounts(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		changed, err := datanetwork.Join(ctx, tx, a.WorkspaceID, a.UserID)
		if err != nil || !changed {
			return err
		}
		ws := a.WorkspaceID
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: datanetwork.JobShare, WorkspaceID: &ws, MaxAttempts: 5,
			IdempotencyKey: "datanetwork.share:" + ws + ":join:" + time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
			return err
		}
		if err := enqueueDataNetworkEmails(ctx, tx, ws, "joined"); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, ws, "data_network.joined", "workspace", ws,
			map[string]any{"addresses": p.Addresses}, map[string]any{"addresses": p.Addresses, "domains": p.Domains})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getDataNetwork(w, r)
}

func (s *Server) leaveDataNetwork(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		changed, err := datanetwork.Leave(ctx, tx, a.WorkspaceID, a.UserID)
		if err != nil || !changed {
			return err
		}
		m, err := datanetwork.Load(ctx, tx, a.WorkspaceID)
		if err != nil {
			return err
		}
		if err := enqueueDataNetworkEmails(ctx, tx, a.WorkspaceID, "left"); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "data_network.left", "workspace", a.WorkspaceID,
			map[string]any{"withdraw_by": m.WithdrawDueAt})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getDataNetwork(w, r)
}

// enqueueDataNetworkEmails queues an email to each owner and admin with a
// confirmed address, saying the workspace joined or left.
func enqueueDataNetworkEmails(ctx context.Context, q jobs.Querier, ws, change string) error {
	var users []string
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(array_agg(u.id::text ORDER BY u.id), '{}') FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 AND m.role IN ('owner', 'admin') AND u.email_verified_at IS NOT NULL`, ws).Scan(&users); err != nil {
		return err
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	for _, u := range users {
		if _, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: jobDataNetworkEmail, WorkspaceID: &ws, MaxAttempts: 5,
			IdempotencyKey: fmt.Sprintf("%s:%s:%s:%s", ws, change, at, u),
			Payload:        map[string]any{"user_id": u, "change": change}}); err != nil {
			return err
		}
	}
	return nil
}

// dataNetworkEmailJob emails one owner or admin that the workspace joined
// or left the data network. It is Outflare's own account email (sysmail):
// it quotes nothing from the workspace's data but its name.
func (s *Server) dataNetworkEmailJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		UserID string `json:"user_id"`
		Change string `json:"change"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.UserID) ||
		(p.Change != "joined" && p.Change != "left") {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	var email, workspace string
	err := s.pool.QueryRow(ctx, `
		SELECT u.email, w.name FROM memberships m
		JOIN users u ON u.id = m.user_id AND u.email_verified_at IS NOT NULL
		JOIN workspaces w ON w.id = m.workspace_id
		WHERE m.workspace_id = $1 AND m.user_id = $2 AND m.role IN ('owner', 'admin')`, ws, p.UserID).Scan(&email, &workspace)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // no longer an owner or admin, or the workspace is gone
	}
	if err != nil {
		return err
	}
	ev := track.Event{Name: "data_network.emailed", WorkspaceID: ws, UserID: p.UserID, ActorType: "system", Surface: "job",
		Props: map[string]any{"change": p.Change}}
	url := s.publicURL + "/" + s.workspaceSlug(ctx, ws) + "/workspace#data-network"
	if err := s.sendAccountEmail(ctx, dataNetworkMessage(url, email, workspace, p.Change)); err != nil {
		track.Fail(ctx, s.log, ev, "email_failed", err)
		return err
	}
	track.Emit(ctx, ev)
	return nil
}

func dataNetworkMessage(url, email, workspace, change string) sysmail.Message {
	m := sysmail.Email{To: email, Button: "See the data network", URL: url}
	days := int(datanetwork.WithdrawWithin / (24 * time.Hour))
	if change == "joined" {
		m.Subject = workspace + " joined the Outflare data network"
		m.Heading = "Joined the data network"
		m.Preview = "The owner of " + workspace + " joined the data network."
		m.Body = []string{
			"The owner of " + workspace + " joined the Outflare data network. From now on the email finder gets unlimited network lookups, and the workspace shares whether the addresses its sequences email deliver or bounce, and the shape of each address (such as first.last).",
			"Names, lists, replies and message text are never shared, and nor is anyone who unsubscribed or was blocked. The network only confirms addresses someone already has; it never shows who else knows them.",
			fmt.Sprintf("An owner or admin can leave at any time under Settings → Workspace → Data network. What was shared is then removed within %d days.", days),
		}
		return m.Message()
	}
	m.Subject = workspace + " left the Outflare data network"
	m.Heading = "Left the data network"
	m.Preview = workspace + " no longer shares with the data network."
	m.Body = []string{
		workspace + " left the Outflare data network. Nothing more is shared, and the email finder is back to the monthly number of network lookups.",
		fmt.Sprintf("Everything the workspace shared is removed from the network within %d days. The owner can join again under Settings → Workspace → Data network.", days),
	}
	return m.Message()
}
