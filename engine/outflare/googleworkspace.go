package outflare

import (
	"context"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

const CapWorkspaceInspect = connectors.CapWorkspaceInspect

// Codes ErrorCode returns for a Google Workspace connection that signs
// without a key and cannot get a delegated token yet.
const (
	// Nobody has signed the connection in, or the sign-in expired.
	CodeWorkspaceSignerNeeded = connectors.CodeWorkspaceSignerNeeded
	// The signed-in account may not sign as the service account.
	CodeWorkspaceSignerDenied = connectors.CodeWorkspaceSignerDenied
	// The tenant has not authorized delegation for the read-only scopes.
	CodeWorkspaceDelegationDenied = connectors.CodeWorkspaceDelegationDenied
)

type WorkspaceDomain = connectors.WorkspaceDomain

// OpenID opens exactly the selected connected account serving this brand.
// It never silently falls back to a different tenant. Job scope must come from
// the persisted workspace/brand row, not an untrusted job payload.
func (cs *Connections) OpenID(ctx context.Context, workspaceID string, brand *string, id, capability string) (*Connection, error) {
	if cs.h.store == nil {
		return nil, ErrNotConnected
	}
	b := ""
	if brand != nil {
		b = *brand
	}
	list, err := cs.h.store.List(ctx, cs.h.Pool, workspaceID, "")
	if err != nil {
		return nil, err
	}
	for _, i := range connectors.ForBrand(list, b) {
		if i.ID != id || i.Status != connectors.StatusConnected {
			continue
		}
		d, ok := cs.h.store.Registry.Definition(i.Connector)
		if !ok || !d.Has(capability) {
			return nil, ErrNotConnected
		}
		o, err := cs.h.store.Open(ctx, cs.h.Pool, workspaceID, id)
		if err != nil {
			return nil, err
		}
		if !o.Integration.Serves(b, list) || o.Status != connectors.StatusConnected {
			return nil, ErrNotConnected
		}
		return &Connection{ID: o.ID, WorkspaceID: o.WorkspaceID, Connector: d.ID, o: o, h: cs.h}, nil
	}
	return nil, ErrNotConnected
}

// InspectWorkspaceDomain uses only the selected connection's credentials.
func (c *Connection) InspectWorkspaceDomain(ctx context.Context, customerID, domain string) (WorkspaceDomain, error) {
	v, ok := c.o.Connector.(connectors.WorkspaceInspector)
	if !ok {
		return WorkspaceDomain{}, ErrNotConnected
	}
	if err := c.withinLimits(ctx); err != nil {
		return WorkspaceDomain{}, err
	}
	return v.InspectWorkspaceDomain(ctx, c.o.Client, c.o.Creds, customerID, domain)
}

// CapWorkspaceManageUsers changes existing users' names, photos and Gmail
// signatures in a tenant the workspace connected and allowed to be changed.
const CapWorkspaceManageUsers = connectors.CapWorkspaceManageUsers

// CodeWorkspaceWritesOff: the connection is read-only; the person turns on
// profile changes and authorizes the write scopes.
const CodeWorkspaceWritesOff = connectors.CodeWorkspaceWritesOff

type (
	WorkspaceUserUpdate = connectors.WorkspaceUserUpdate
	WorkspaceUserResult = connectors.WorkspaceUserResult
)

// UpdateWorkspaceUser changes one existing user's profile with only the
// selected connection's credentials.
func (c *Connection) UpdateWorkspaceUser(ctx context.Context, u WorkspaceUserUpdate) (WorkspaceUserResult, error) {
	v, ok := c.o.Connector.(connectors.WorkspaceUserManager)
	if !ok {
		return WorkspaceUserResult{}, ErrNotConnected
	}
	if err := c.withinLimits(ctx); err != nil {
		return WorkspaceUserResult{}, err
	}
	return v.UpdateWorkspaceUser(ctx, c.o.Client, c.o.Creds, u)
}
