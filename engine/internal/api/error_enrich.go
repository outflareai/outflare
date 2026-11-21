package api

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/jserrors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Errors as they are stored (docs/error-tracking.md). Each Error Caught
// gets, before it is stored:
//
//   - group: the issue it belongs to. For the website script it is made
//     here (jserrors.Group), so one bug is one issue whatever the browser
//     or the build; the fingerprint the script sent is kept as it was.
//     For a server, the fingerprint it sent, or else its type and message,
//     as before.
//   - noise: why it isn't the site's own bug (a browser extension, a link
//     scanner, a copy of the page), or nothing.
//   - message_full: the full message of a minified React or Redux error.
//
// Errors stored before this have them added by the errors.regroup job.

const jobErrorsRegroup = "errors.regroup"

// errorGroupVersion marks errors grouped this way, so the job knows which
// are done.
const errorGroupVersion = 2

// errorPlainFingerprint is the issue the first version made: the sender's
// fingerprint when plain, else the type and message.
const errorPlainFingerprint = `CASE WHEN properties->>'fingerprint' ~ '^[a-z0-9]{1,64}$' THEN properties->>'fingerprint'
	ELSE left(md5(coalesce(properties->>'type', '') || ':' || coalesce(properties->>'message', '')), 16) END`

// errorGroupOf is an event's group before any merge.
const errorGroupOf = `coalesce(properties->>'group', ` + errorPlainFingerprint + `)`

// errorFingerprint is an event's issue: its group, or the issue a person
// (or the regroup job) merged that group into. $2 is the brand.
const errorFingerprint = `coalesce((SELECT m.into_fingerprint FROM error_issue_merges m WHERE m.brand_id = $2 AND m.fingerprint = ` + errorGroupOf + `), ` + errorGroupOf + `)`

// enrichError adds group, noise and message_full to an Error Caught's
// properties.
func enrichError(props, ctxObj map[string]any, source string) {
	msg, stack := propText(props["message"]), propText(props["stack"])
	props["group"] = errorGroupFor(props, source)
	props["group_v"] = errorGroupVersion
	if source == siteSource {
		page, _ := ctxObj["page"].(map[string]any)
		if n := jserrors.Noise(msg, stack, propText(props["source"]), propText(page["url"])); n != "" {
			props["noise"] = n
		} else {
			delete(props, "noise")
		}
	}
	if full := jserrors.Expand(msg); full != "" {
		props["message_full"] = cutUTF8(scrubErrorText(full), 2000)
	}
}

// errorGroupFor is the group for an error's properties from source.
func errorGroupFor(props map[string]any, source string) string {
	if source == siteSource {
		return jserrors.Group(propText(props["type"]), propText(props["message"]), propText(props["stack"]))
	}
	return errorFingerprintOf(props)
}

var (
	scrubEmail = regexp.MustCompile(`[^\s@"'<>()]+@[^\s@"'<>()]+\.[A-Za-z]{2,}`)
	scrubKey   = regexp.MustCompile(`[A-Za-z0-9_\-]{32,}`)
)

// scrubErrorText takes email addresses and long keys out of text an
// error's address carried, as the script does.
func scrubErrorText(t string) string {
	t = scrubEmail.ReplaceAllString(t, "[email]")
	return scrubKey.ReplaceAllString(t, "[key]")
}

// regroupBatch is how many errors one run of the job regroups.
const regroupBatch = 2000

// errorsRegroupJob gives the workspace's stored errors (the last 90 days)
// their group, noise and full message, a batch at a time, and records
// each issue the first grouping made as merged into the one it is now
// part of, carrying over what a person decided about it.
func (s *Server) errorsRegroupJob(ctx context.Context, j jobs.Job) error {
	if j.WorkspaceID == nil {
		return fmt.Errorf("%w: no workspace", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	n, err := s.regroupErrors(ctx, ws)
	if err != nil {
		track.Fail(ctx, s.log, track.Event{Name: "errors.regroup_failed", WorkspaceID: ws, ActorType: "system", Surface: "job"}, "internal", err)
		return err
	}
	if n == regroupBatch {
		_, err = jobs.Enqueue(ctx, s.pool, jobs.NewJob{Kind: jobErrorsRegroup, WorkspaceID: &ws, Payload: map[string]any{},
			IdempotencyKey: "errors.regroup:" + ws + ":after:" + strconv.FormatInt(j.ID, 10), MaxAttempts: 10})
		return err
	}
	track.Emit(ctx, track.Event{Name: "errors.regrouped", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: "success"})
	return nil
}

// regroupErrors regroups one batch, in one transaction; it returns how
// many errors it did.
func (s *Server) regroupErrors(ctx context.Context, ws string) (int, error) {
	done := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, brand_id::text, source, properties, context FROM customer_events
			WHERE workspace_id = $1 AND name = $2 AND occurred_at >= $3 AND coalesce(properties->>'group_v', '') <> $4
			ORDER BY occurred_at LIMIT $5 FOR UPDATE SKIP LOCKED`, ws, errorEvent, time.Now().Add(-errorHistory), strconv.Itoa(errorGroupVersion), regroupBatch)
		if err != nil {
			return err
		}
		type ev struct {
			id, brand, source string
			props, ctx        map[string]any
		}
		var evs []ev
		var e ev
		var props, cx []byte
		if _, err := pgx.ForEachRow(rows, []any{&e.id, &e.brand, &e.source, &props, &cx}, func() error {
			v := ev{id: e.id, brand: e.brand, source: e.source, props: map[string]any{}, ctx: map[string]any{}}
			_ = json.Unmarshal(props, &v.props)
			_ = json.Unmarshal(cx, &v.ctx)
			evs = append(evs, v)
			return nil
		}); err != nil {
			return err
		}
		// old issue -> new group, per brand.
		moved := map[[2]string]string{}
		for _, v := range evs {
			old := errorFingerprintOf(v.props)
			enrichError(v.props, v.ctx, v.source)
			if g := v.props["group"].(string); g != old {
				moved[[2]string{v.brand, old}] = g
			}
			if _, err := tx.Exec(ctx, `UPDATE customer_events SET properties = $2 WHERE id = $1`, v.id, v.props); err != nil {
				return err
			}
		}
		for k, g := range moved {
			brand, old := k[0], k[1]
			if _, err := tx.Exec(ctx, `INSERT INTO error_issue_merges (workspace_id, brand_id, fingerprint, into_fingerprint, kind)
				VALUES ($1, $2, $3, $4, 'regroup') ON CONFLICT (brand_id, fingerprint) DO NOTHING`, ws, brand, old, g); err != nil {
				return err
			}
			// What a person decided about the old issue holds for the new
			// one, unless they decided about it more recently.
			if _, err := tx.Exec(ctx, `INSERT INTO error_issues (workspace_id, brand_id, fingerprint, status, status_at, updated_by)
				SELECT workspace_id, brand_id, $3, status, status_at, updated_by FROM error_issues WHERE brand_id = $2 AND fingerprint = $4 AND workspace_id = $1
				ON CONFLICT (brand_id, fingerprint) DO UPDATE SET status = EXCLUDED.status, status_at = EXCLUDED.status_at, updated_by = EXCLUDED.updated_by
				WHERE error_issues.status_at < EXCLUDED.status_at`, ws, brand, g, old); err != nil {
				return err
			}
		}
		done = len(evs)
		return nil
	})
	return done, err
}
