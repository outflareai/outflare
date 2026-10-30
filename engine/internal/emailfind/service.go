package emailfind

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// JobRebuild rebuilds one domain's pattern profile for a workspace.
const JobRebuild = "emailfind.rebuild"

// Service rebuilds pattern profiles in the background and runs find/verify.
type Service struct {
	Pool     *pgxpool.Pool
	Verify   *verify.Service
	Resolver Resolver
	Log      *slog.Logger
}

type rebuildPayload struct {
	Domain string `json:"domain"`
}

// Register adds the rebuild job handler.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobRebuild, s.job) }

func (s *Service) job(ctx context.Context, job jobs.Job) error {
	var p rebuildPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	prof, err := Rebuild(ctx, s.Pool, s.Resolver, *job.WorkspaceID, p.Domain)
	if err != nil {
		track.Fail(ctx, s.Log, track.Event{Name: "email.pattern_learned", WorkspaceID: *job.WorkspaceID,
			ActorType: "system", Surface: "job"}, "internal_error", err)
		return err
	}
	track.Emit(ctx, track.Event{Name: "email.pattern_learned", WorkspaceID: *job.WorkspaceID,
		ActorType: "system", Surface: "job", Props: map[string]any{
			"sample_size": prof.SampleSize, "confidence": prof.Confidence,
			"has_pattern": prof.TopShape != "", "catch_all": prof.CatchAll, "provider": prof.Provider}})
	return nil
}

// EnqueueRebuild schedules a rebuild of a domain's profile. Call it in the
// transaction that changed what the domain's profile is learned from (a new
// lead, a verification result). At most one rebuild per domain per day is
// queued, so repeated calls coalesce.
func EnqueueRebuild(ctx context.Context, q jobs.Querier, ws, domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil
	}
	key := ws + ":" + domain + ":" + time.Now().UTC().Format("2006-01-02")
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobRebuild, WorkspaceID: &ws,
		Payload: rebuildPayload{Domain: domain}, IdempotencyKey: key, MaxAttempts: 3})
	return err
}

// VerifyItem is one address to verify.
type VerifyItem struct {
	Ref   string `json:"ref"`
	Email string `json:"email"`
}

// VerifyRequest is a batch to verify.
type VerifyRequest struct {
	Items []VerifyItem `json:"items"`
	Force bool         `json:"force"`
}

// VerifyEmails checks a batch of addresses with the workspace's verifier (its
// own connected one, or the free built-in checks), mapped to the shared result
// codes so a pipeline can drive its loop from them.
func (s *Service) VerifyEmails(ctx context.Context, ws string, req VerifyRequest) (FindResult, error) {
	byEmail := map[string]string{} // email -> caller ref
	emails := make([]string, 0, len(req.Items))
	for _, it := range req.Items {
		e := strings.ToLower(strings.TrimSpace(it.Email))
		if e == "" {
			continue
		}
		byEmail[e] = it.Ref
		emails = append(emails, e)
	}
	results, err := s.Verify.Verify(ctx, ws, emails, req.Force)
	if err != nil {
		// A refused key or exhausted quota is a stop, not a crash: the caller
		// pauses the run with a plain reason.
		if k := connectors.KindOf(err); k == connectors.ErrAuth || k == connectors.ErrQuota {
			out := FindResult{Items: make([]ItemResult, 0, len(emails))}
			for _, e := range emails {
				out.Items = append(out.Items, ItemResult{Ref: byEmail[e], Email: e,
					Code: CodeStop, StopCode: stopCode(k)})
			}
			return out, nil
		}
		return FindResult{}, err
	}
	// Addresses checked recently enough aren't checked again: answer them
	// from the stored result, at no cost.
	paid := map[string]bool{} // checked now, on a paid verifier
	for _, res := range results {
		paid[res.Email] = res.IntegrationID != nil
	}
	var cached []string
	for _, e := range emails {
		if _, ok := paid[e]; !ok {
			cached = append(cached, e)
		}
	}
	if len(cached) > 0 {
		set, err := verify.LoadSettings(ctx, s.Pool, ws)
		if err != nil {
			return FindResult{}, err
		}
		stored, err := verify.List(ctx, s.Pool, ws, "", cached, len(cached), 0)
		if err != nil {
			return FindResult{}, err
		}
		// One that failed and will be checked again has no fresh result
		// and gets no answer yet: the runner retries it.
		now := time.Now()
		for _, r := range stored {
			if set.Fresh(r, now) {
				results = append(results, r)
			}
		}
	}
	out := FindResult{Items: make([]ItemResult, 0, len(results))}
	for _, res := range results {
		it := ItemResult{Ref: byEmail[res.Email], Email: res.Email,
			Code: verifyCode(res.Status), Evidence: EvidenceHistory,
			Cost: Cost{Provider: res.Provider}}
		if paid[res.Email] {
			// One credit an address, as the spend ledger books it.
			it.Cost.Connection, it.Cost.Units = *res.IntegrationID, 1
		}
		if res.Score != nil {
			it.Confidence = *res.Score
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

func verifyCode(status string) Code {
	switch status {
	case connectors.Deliverable:
		return CodeVerified
	case connectors.Undeliverable:
		return CodeUndeliverable
	case connectors.Risky:
		return CodeRisky
	default: // unknown: the verifier could not tell — caller's policy
		return CodeRisky
	}
}

func stopCode(k string) string {
	if k == connectors.ErrQuota {
		return "connector_quota"
	}
	return "connector_needed"
}
