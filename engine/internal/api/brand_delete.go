package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Deleting a brand: an owner or admin deletes a brand for good, from its
// own page. Everything the brand owns goes with it (its records, lists,
// sequences, mailboxes, sites, settings and history), in a job, because a
// big brand holds millions of rows. Nothing of it moves to "no brand", so
// no other brand ever sees it.

const jobBrandDelete = "brands.delete"

func (s *Server) routesBrandDelete(mux *http.ServeMux) {
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/brands/{brand}", s.inWorkspace(auth.ScopeWorkspaceManage, s.deleteBrand))
}

func (s *Server) registerBrandDeleteJobs(q *jobs.Queue) {
	q.Handle(jobBrandDelete, s.brandDeleteJob)
}

func (s *Server) deleteBrand(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// A workspace keeps at least one brand to work in.
		var others int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM brands WHERE workspace_id = $1 AND id <> $2 AND status = 'active'`,
			a.WorkspaceID, b.ID).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return errLastBrand
		}
		// Someone kept to this brand alone would see every brand once it
		// is gone (an empty brand list means all brands), so they move first.
		var only int
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM memberships WHERE workspace_id = $1 AND brand_ids = ARRAY[$2::uuid])
			     + (SELECT count(*) FROM invites WHERE workspace_id = $1 AND brand_ids = ARRAY[$2::uuid]
				    AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now())`,
			a.WorkspaceID, b.ID).Scan(&only); err != nil {
			return err
		}
		if only > 0 {
			return errBrandOnlyAccess
		}
		// Archived at once, so it leaves the switcher while the job runs.
		if _, err := tx.Exec(ctx, `UPDATE brands SET status = 'archived', updated_at = now() WHERE id = $1 AND workspace_id = $2`, b.ID, a.WorkspaceID); err != nil {
			return err
		}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobBrandDelete, WorkspaceID: &a.WorkspaceID,
			Payload: map[string]string{"brand_id": b.ID}, IdempotencyKey: "brand.delete:" + b.ID}); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.deleted", "brand", b.ID, map[string]any{})
	})
	if errors.Is(err, errLastBrand) {
		writeError(w, r, http.StatusConflict, "last_brand", b.Name+" is the workspace's only brand: add another brand before deleting it")
		return
	}
	if errors.Is(err, errBrandOnlyAccess) {
		writeError(w, r, http.StatusConflict, "brand_only_access", "some members or invites can see only "+b.Name+": give them another brand in Settings, Members first")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

var (
	errBrandOnlyAccess = errors.New("members limited to this brand")
	errLastBrand       = errors.New("the workspace's only brand")
)

// brandDeleteJob deletes every row that points at the brand, then the
// brand. Tables that keep their rows when a brand goes (ON DELETE SET NULL)
// would otherwise hand them to every brand; child brands only lose their
// parent. Tables refer to each other, so a delete can wait on another
// table's rows: it goes round until all are gone. Each delete stands alone,
// so a retry picks up where it stopped.
func (s *Server) brandDeleteJob(ctx context.Context, job jobs.Job) error {
	var p map[string]string
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p["brand_id"]) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws, id := *job.WorkspaceID, p["brand_id"]
	var status string
	err := s.pool.QueryRow(ctx, `SELECT status FROM brands WHERE id = $1 AND workspace_id = $2`, id, ws).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "archived" {
		// Restored after the delete was asked for: keep it.
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT format('%I.%I', n.nspname, t.relname), a.attname
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		CROSS JOIN LATERAL unnest(c.conkey, c.confkey) AS k(col, ref)
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.col
		JOIN pg_attribute ra ON ra.attrelid = c.confrelid AND ra.attnum = k.ref
		WHERE c.contype = 'f' AND c.confrelid = 'public.brands'::regclass AND ra.attname = 'id'
			AND c.conrelid <> 'public.brands'::regclass
		ORDER BY 1, 2`)
	if err != nil {
		return err
	}
	type ref struct{ table, column string }
	refs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ref, error) {
		var f ref
		return f, r.Scan(&f.table, &f.column)
	})
	if err != nil {
		return err
	}
	left := refs
	for range 20 {
		var waiting []ref
		var last error
		for _, f := range left {
			_, err := s.pool.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = $1`, f.table, pgx.Identifier{f.column}.Sanitize()), id)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				// Another table's rows still point at these: next round.
				waiting, last = append(waiting, f), err
				continue
			}
			if err != nil {
				return fmt.Errorf("delete from %s: %w", f.table, err)
			}
		}
		if len(waiting) == 0 {
			break
		}
		if len(waiting) == len(left) {
			return fmt.Errorf("brand %s: rows still referenced: %w", id, last)
		}
		left = waiting
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE integrations SET brand_ids = array_remove(brand_ids, $2::uuid)
			WHERE workspace_id = $1 AND $2::uuid = ANY(brand_ids)`, ws, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE memberships SET brand_ids = array_remove(brand_ids, $2::uuid)
			WHERE workspace_id = $1 AND $2::uuid = ANY(brand_ids) AND brand_ids <> ARRAY[$2::uuid]`, ws, id); err != nil {
			return err
		}
		// An invite kept to this brand alone can no longer be used.
		if _, err := tx.Exec(ctx, `UPDATE invites SET revoked_at = now()
			WHERE workspace_id = $1 AND brand_ids = ARRAY[$2::uuid] AND accepted_at IS NULL AND revoked_at IS NULL`, ws, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE invites SET brand_ids = array_remove(brand_ids, $2::uuid)
			WHERE workspace_id = $1 AND $2::uuid = ANY(brand_ids) AND brand_ids <> ARRAY[$2::uuid]`, ws, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM brands WHERE id = $1 AND workspace_id = $2`, id, ws)
		return err
	})
}
