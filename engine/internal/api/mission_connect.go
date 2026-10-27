package api

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Connect requests in a mission close themselves: once the workspace has a
// working connection to the connector an agent asked for, the request is
// resolved with a note, and Outflare's agent takes a turn at once to carry
// on. People no longer have to come back and press "Mark as done".

// connectorConnected reports whether the workspace has a working connection
// to connector.
func connectorConnected(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, ws, connector string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM integrations WHERE workspace_id = $1 AND connector = $2 AND status = 'connected')`, ws, connector).Scan(&ok)
	return ok, err
}

// resolveConnectedRequests closes the open mission connect requests whose
// connector is now connected, in workspace ws, or everywhere when ws is
// empty. It reports how many it closed.
func (s *Server) resolveConnectedRequests(ctx context.Context, ws string) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE mission_updates u SET resolved_at = now()
			WHERE u.kind = 'connect_request' AND u.resolved_at IS NULL AND ($1 = '' OR u.workspace_id = $1::uuid)
			  AND EXISTS (SELECT 1 FROM integrations i
			              WHERE i.workspace_id = u.workspace_id AND i.connector = u.data->>'connector' AND i.status = 'connected')
			  AND EXISTS (SELECT 1 FROM missions m WHERE m.id = u.mission_id AND m.status IN ('draft', 'pending_approval', 'running', 'paused'))
			RETURNING u.mission_id::text, u.workspace_id::text, u.data->>'connector', COALESCE(u.data->>'connector_name', u.data->>'connector')`, ws)
		if err != nil {
			return err
		}
		type closed struct{ mission, ws, connector, name string }
		var list []closed
		for rows.Next() {
			var c closed
			if err := rows.Scan(&c.mission, &c.ws, &c.connector, &c.name); err != nil {
				return err
			}
			list = append(list, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range list {
			name := c.name
			if s.integrations != nil {
				if d, ok := s.integrations.Registry.Definition(c.connector); ok {
					name = d.Name
				}
			}
			if err := addUpdateData(ctx, tx, c.mission, c.ws, "", "system", "note", name+" is connected, so the request for it is closed.", map[string]any{"connector": c.connector}); err != nil {
				return err
			}
			if err := wakeBuiltin(ctx, tx, c.mission); err != nil {
				return err
			}
			if err := record(ctx, tx, nil, c.ws, "mission.connect_request_resolved", "mission", c.mission, map[string]any{"connector": c.connector, "automatic": true}); err != nil {
				return err
			}
		}
		n = len(list)
		return nil
	})
	return n, err
}
