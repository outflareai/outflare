package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mailbox"
)

// Job kinds.
const (
	// JobQuote asks the plan's supplier for prices and availability.
	JobQuote = "infra.quote"
	// JobAdvance moves an approved plan forward: orders, checks the
	// order, DNS and connected mailboxes.
	JobAdvance = "infra.advance"
	// JobCancel cancels a retired mailbox at its supplier.
	JobCancel = "infra.cancel"

	// advanceEvery is how often active plans are moved forward on their own.
	advanceEvery = 5 * time.Minute
	// recheckDNS is how often a domain waiting on DNS is checked again.
	recheckDNS = 10 * time.Minute
)

// Plan statuses.
const (
	PlanDraft     = "draft"
	PlanBuilding  = "building"
	PlanWarming   = "warming"
	PlanReady     = "ready"
	PlanCancelled = "cancelled"
)

// Plan item stages.
const (
	ItemPlanned    = "planned"
	ItemBuying     = "buying"
	ItemDNS        = "dns"
	ItemConnecting = "connecting"
	ItemDone       = "done"
	ItemFailed     = "failed"
	ItemCancelled  = "cancelled"
)

// Service runs plans and the mailbox lifecycle.
type Service struct {
	Pool *pgxpool.Pool
	Mail *mailbox.Service
	Log  *slog.Logger
	// Now is the clock; tests replace it.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Register adds the job handlers to a queue.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(JobQuote, s.quoteJob)
	q.Handle(JobAdvance, s.advanceJob)
	q.Handle(JobCancel, s.cancelJob)
}

type planPayload struct {
	PlanID string `json:"plan_id"`
}

// QueueQuote schedules a quote for a draft plan.
func QueueQuote(ctx context.Context, q jobs.Querier, ws, planID string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobQuote, WorkspaceID: &ws, Payload: planPayload{planID}, MaxAttempts: 3})
	return err
}

// QueueAdvance schedules a plan to move forward now.
func QueueAdvance(ctx context.Context, q jobs.Querier, ws, planID string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobAdvance, WorkspaceID: &ws, Payload: planPayload{planID}, MaxAttempts: 3})
	return err
}

type cancelPayload struct {
	PlanID string `json:"plan_id"`
	Email  string `json:"email"`
}

// QueueCancel schedules cancelling a mailbox at the supplier of the plan
// that built it.
func QueueCancel(ctx context.Context, q jobs.Querier, ws, planID, email string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobCancel, WorkspaceID: &ws, Payload: cancelPayload{planID, email},
		MaxAttempts: 5, IdempotencyKey: "cancel:" + planID + ":" + strings.ToLower(email)})
	return err
}

// Run ticks every interval until ctx ends.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("infra tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick queues active plans to move forward and moves every mailbox that
// is due to its next lifecycle stage.
func (s *Service) Tick(ctx context.Context) error {
	now := s.now()
	rows, err := s.Pool.Query(ctx, `SELECT id::text, workspace_id::text FROM infra_plans WHERE status IN ('building', 'warming')`)
	if err != nil {
		return err
	}
	type ref struct{ id, ws string }
	plans, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ref, error) {
		var p ref
		return p, r.Scan(&p.id, &p.ws)
	})
	if err != nil {
		return err
	}
	bucket := now.Unix() / int64(advanceEvery.Seconds())
	for _, p := range plans {
		if _, err := jobs.Enqueue(ctx, s.Pool, jobs.NewJob{Kind: JobAdvance, WorkspaceID: &p.ws, Payload: planPayload{p.id},
			MaxAttempts: 3, IdempotencyKey: fmt.Sprintf("%s:%d", p.id, bucket)}); err != nil {
			return err
		}
	}
	_, err = s.Lifecycle(ctx)
	return err
}

// plan is a plan as the jobs need it.
type plan struct {
	ID, WorkspaceID, Status, Supplier, Provider string
	BrandID, IntegrationID, OrderRef            *string
	OrderStarted, ApprovedAt                    *time.Time
	WarmupDays, MailboxDailyLimit               int
}

func loadPlan(ctx context.Context, q jobs.Querier, id string, lock bool) (*plan, error) {
	var p plan
	sql := `SELECT id::text, workspace_id::text, status, supplier, provider, brand_id::text, supplier_integration_id::text,
	               order_ref, order_started_at, approved_at, warmup_days, mailbox_daily_limit
	        FROM infra_plans WHERE id = $1`
	if lock {
		sql += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, sql, id).Scan(&p.ID, &p.WorkspaceID, &p.Status, &p.Supplier, &p.Provider, &p.BrandID, &p.IntegrationID,
		&p.OrderRef, &p.OrderStarted, &p.ApprovedAt, &p.WarmupDays, &p.MailboxDailyLimit)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

func (p *plan) account() Account {
	a := Account{WorkspaceID: p.WorkspaceID}
	if p.IntegrationID != nil {
		a.IntegrationID = *p.IntegrationID
	}
	return a
}

// item is one plan item.
type item struct {
	ID, Kind, Name, Domain, FirstName, LastName, Stage, Detail string
	MailboxID                                                  *string
}

func loadItems(ctx context.Context, q pgx.Tx, planID string) ([]item, error) {
	rows, err := q.Query(ctx, `
		SELECT id::text, kind, name, domain, first_name, last_name, stage, detail, mailbox_id::text
		FROM infra_plan_items WHERE plan_id = $1 ORDER BY kind DESC, domain, name`, planID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (item, error) {
		var it item
		return it, r.Scan(&it.ID, &it.Kind, &it.Name, &it.Domain, &it.FirstName, &it.LastName, &it.Stage, &it.Detail, &it.MailboxID)
	})
}

func setItem(ctx context.Context, tx pgx.Tx, id, stage, detail string) error {
	_, err := tx.Exec(ctx, `
		UPDATE infra_plan_items SET
			stage_changed_at = CASE WHEN stage <> $2 THEN now() ELSE stage_changed_at END,
			stage = $2, detail = $3
		WHERE id = $1`, id, stage, detail)
	return err
}

func system(ws, action, targetType, targetID string, data map[string]any) audit.Event {
	return audit.Event{WorkspaceID: ws, ActorType: "system", Action: action, TargetType: targetType, TargetID: targetID, Data: data}
}

// quoteJob prices a draft plan at its supplier.
func (s *Service) quoteJob(ctx context.Context, job jobs.Job) error {
	var pl planPayload
	if err := json.Unmarshal(job.Payload, &pl); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	return s.quote(ctx, pl.PlanID, job.Attempt >= job.MaxAttempts)
}

// Quote prices a draft plan at its supplier now.
func (s *Service) Quote(ctx context.Context, planID string) error { return s.quote(ctx, planID, true) }

// quote prices a plan; a supplier error is saved on the plan on the last
// try, and returned for a retry before that.
func (s *Service) quote(ctx context.Context, planID string, last bool) error {
	p, err := loadPlan(ctx, s.Pool, planID, false)
	if err != nil || p == nil || p.Status != PlanDraft || p.Supplier == SupplierManual {
		return err
	}
	sup, err := LookupSupplier(p.Supplier)
	if err != nil {
		return s.saveQuote(ctx, p.ID, nil, "No connector for "+p.Supplier+" is installed on this server.")
	}
	var req QuoteRequest
	var perDomain int
	if err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(array_agg(i.name ORDER BY i.name) FILTER (WHERE i.kind = 'domain' AND NOT i.owned), '{}'), max(pl.mailboxes_per_domain)
		FROM infra_plan_items i JOIN infra_plans pl ON pl.id = i.plan_id WHERE i.plan_id = $1`, p.ID).Scan(&req.Domains, &perDomain); err != nil {
		return err
	}
	req.Provider, req.MailboxesPerDomain = p.Provider, perDomain
	q, err := sup.Quote(ctx, p.account(), req)
	if err != nil {
		if !last {
			return err
		}
		return s.saveQuote(ctx, p.ID, nil, "The supplier did not answer: "+err.Error())
	}
	return s.saveQuote(ctx, p.ID, &q, "")
}

// saveQuote stores a quote, or why there is none, on a plan still in draft.
func (s *Service) saveQuote(ctx context.Context, planID string, q *Quote, failure string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		v := map[string]any{"status": "failed", "error": failure}
		if q != nil {
			v = map[string]any{"status": "ready", "currency": q.Currency, "once_cents": q.OnceCents, "monthly_cents": q.MonthlyCents,
				"domains": q.Domains, "quoted_at": s.now().UTC()}
			for _, d := range q.Domains {
				if _, err := tx.Exec(ctx, `
					UPDATE infra_plan_items SET available = $3, price_cents = $4,
						detail = CASE WHEN $3 THEN '' ELSE $5 END
					WHERE plan_id = $1 AND kind = 'domain' AND lower(name) = lower($2)`,
					planID, d.Domain, d.Available, d.PriceCents, unavailable(d)); err != nil {
					return err
				}
			}
		}
		b, _ := json.Marshal(v)
		_, err := tx.Exec(ctx, `UPDATE infra_plans SET quote = $2, updated_at = now() WHERE id = $1 AND status = 'draft'`, planID, b)
		return err
	})
}

func unavailable(d DomainQuote) string {
	if d.Reason != "" {
		return "Not available: " + d.Reason + ". Replace it before approving."
	}
	return "Not available. Replace it before approving."
}

// advanceJob moves one approved plan forward.
func (s *Service) advanceJob(ctx context.Context, job jobs.Job) error {
	var pl planPayload
	if err := json.Unmarshal(job.Payload, &pl); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	return s.Advance(ctx, pl.PlanID)
}

// Advance moves an approved plan as far forward as it can go now. It is
// safe to run any number of times.
func (s *Service) Advance(ctx context.Context, planID string) error {
	p, err := loadPlan(ctx, s.Pool, planID, false)
	if err != nil || p == nil || (p.Status != PlanBuilding && p.Status != PlanWarming) {
		return err
	}
	if p.Supplier != SupplierManual {
		if err := s.order(ctx, p); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		p, err := loadPlan(ctx, tx, planID, true)
		if err != nil || p == nil || (p.Status != PlanBuilding && p.Status != PlanWarming) {
			return err
		}
		return s.build(ctx, tx, p)
	})
}

// order places the plan's order once, or checks on it.
func (s *Service) order(ctx context.Context, p *plan) error {
	sup, err := LookupSupplier(p.Supplier)
	if err != nil {
		return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				UPDATE infra_plan_items SET stage = 'failed', detail = $2, stage_changed_at = now()
				WHERE plan_id = $1 AND stage IN ('planned', 'buying')`,
				p.ID, "No connector for "+p.Supplier+" is installed on this server, so nothing was ordered.")
			return err
		})
	}

	switch {
	case p.OrderRef == nil && p.OrderStarted == nil:
		var req OrderRequest
		started := false
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			lp, err := loadPlan(ctx, tx, p.ID, true)
			if err != nil || lp == nil || lp.Status != PlanBuilding || lp.OrderStarted != nil {
				return err
			}
			items, err := loadItems(ctx, tx, p.ID)
			if err != nil {
				return err
			}
			req = OrderRequest{Provider: p.Provider, IdempotencyKey: p.ID}
			// Domains to buy, and domains the workspace already has that
			// only need mailboxes.
			byDomain := map[string]int{}
			domainPlanned := map[string]bool{}
			for _, it := range items {
				if it.Kind == "domain" {
					domainPlanned[it.Domain] = it.Stage == ItemPlanned
					if it.Stage == ItemPlanned {
						byDomain[it.Domain] = len(req.Domains)
						req.Domains = append(req.Domains, OrderDomain{Domain: it.Domain})
					}
				}
			}
			for _, it := range items {
				if it.Kind != "mailbox" || it.Stage != ItemPlanned {
					continue
				}
				i, ok := byDomain[it.Domain]
				if !ok {
					i = len(req.Domains)
					byDomain[it.Domain] = i
					req.Domains = append(req.Domains, OrderDomain{Domain: it.Domain, Owned: !domainPlanned[it.Domain]})
				}
				req.Domains[i].Mailboxes = append(req.Domains[i].Mailboxes, OrderMailbox{Email: it.Name, FirstName: it.FirstName, LastName: it.LastName})
			}
			if len(req.Domains) == 0 {
				return nil
			}
			// Recorded before the call: if the engine stops mid-call, the
			// order is never placed a second time.
			if _, err := tx.Exec(ctx, `UPDATE infra_plans SET order_started_at = now(), updated_at = now() WHERE id = $1`, p.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE infra_plan_items SET stage = 'buying', detail = $2, stage_changed_at = now()
				WHERE plan_id = $1 AND stage = 'planned'`, p.ID, "Ordering from "+supplierName(p.Supplier)+"."); err != nil {
				return err
			}
			started = true
			return nil
		})
		if err != nil || !started {
			return err
		}
		ref, oerr := sup.Order(ctx, p.account(), req)
		return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if oerr != nil {
				if _, err := tx.Exec(ctx, `
					UPDATE infra_plan_items SET stage = 'failed', detail = $2, stage_changed_at = now()
					WHERE plan_id = $1 AND stage = 'buying'`,
					p.ID, "The order failed: "+oerr.Error()+". Check your "+supplierName(p.Supplier)+" account before trying again."); err != nil {
					return err
				}
				return audit.Record(ctx, tx, system(p.WorkspaceID, "infra.order_failed", "infra_plan", p.ID, map[string]any{"error": oerr.Error()}))
			}
			if _, err := tx.Exec(ctx, `UPDATE infra_plans SET order_ref = $2, updated_at = now() WHERE id = $1`, p.ID, ref); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE infra_plan_items SET detail = $2 WHERE plan_id = $1 AND stage = 'buying'`,
				p.ID, "Ordered from "+supplierName(p.Supplier)+"; waiting for it to be set up."); err != nil {
				return err
			}
			return audit.Record(ctx, tx, system(p.WorkspaceID, "infra.ordered", "infra_plan", p.ID, map[string]any{"supplier": p.Supplier, "order_ref": ref}))
		})

	case p.OrderRef == nil:
		// Started but never confirmed: the engine stopped during the call.
		return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				UPDATE infra_plan_items SET stage = 'failed', detail = $2, stage_changed_at = now()
				WHERE plan_id = $1 AND stage = 'buying'`,
				p.ID, "The order may or may not have gone through. Check your "+supplierName(p.Supplier)+" account; nothing will be ordered again on its own.")
			return err
		})
	}

	st, err := sup.OrderStatus(ctx, p.account(), *p.OrderRef)
	if err != nil {
		s.Log.Warn("infra order status", "plan", p.ID, "err", err)
		return nil // checked again on the next tick
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, d := range st.Domains {
			switch d.Status {
			case OrderDone:
				// Registered: DNS is checked by build.
				if _, err := tx.Exec(ctx, `
					UPDATE infra_plan_items SET stage = 'dns', detail = '', stage_changed_at = now()
					WHERE plan_id = $1 AND kind = 'domain' AND lower(name) = lower($2) AND stage = 'buying'`, p.ID, d.Name); err != nil {
					return err
				}
			case OrderFailed:
				if _, err := tx.Exec(ctx, `
					UPDATE infra_plan_items SET stage = 'failed', detail = $3, stage_changed_at = now()
					WHERE plan_id = $1 AND kind = 'domain' AND lower(name) = lower($2) AND stage = 'buying'`, p.ID, d.Name, "The supplier could not set it up: "+d.Detail); err != nil {
					return err
				}
			}
		}
		for _, m := range st.Mailboxes {
			if m.Status == OrderFailed {
				if _, err := tx.Exec(ctx, `
					UPDATE infra_plan_items SET stage = 'failed', detail = $3, stage_changed_at = now()
					WHERE plan_id = $1 AND kind = 'mailbox' AND lower(name) = lower($2) AND stage IN ('buying', 'connecting')`,
					p.ID, m.Name, "The supplier could not create it: "+m.Detail); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func supplierName(s string) string {
	switch s {
	case "cheapinboxes":
		return "Cheap Inboxes"
	case SupplierManual:
		return "you"
	}
	return s
}

func providerName(p string) string {
	if p == "microsoft" {
		return "Microsoft 365"
	}
	return "Google Workspace"
}

// build moves domains through buying and DNS, links connected mailboxes,
// and updates the plan's status. tx holds the plan's row lock.
func (s *Service) build(ctx context.Context, tx pgx.Tx, p *plan) error {
	items, err := loadItems(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	now := s.now()
	domainStage := map[string]string{}
	perDomain := map[string]int{}
	for _, it := range items {
		if it.Kind == "mailbox" {
			perDomain[it.Domain]++
		}
	}

	for i := range items {
		it := &items[i]
		if it.Kind != "domain" {
			continue
		}
		if it.Stage == ItemPlanned && p.Supplier == SupplierManual {
			it.Stage = ItemBuying
			it.Detail = fmt.Sprintf("Buy %s at your registrar and add %s on %s, then mark it bought. "+
				"The plan also moves on by itself once the domain's MX records point at %[3]s.",
				it.Name, mailbox.Plural(perDomain[it.Domain], "mailbox", "mailboxes"), providerName(p.Provider))
			if err := setItem(ctx, tx, it.ID, it.Stage, it.Detail); err != nil {
				return err
			}
		}
		var mx *string
		var checked *time.Time
		var spf, dkim, dmarc string
		found := true
		err := tx.QueryRow(ctx, `SELECT mx_provider, spf, dkim, dmarc, checked_at FROM sending_domains WHERE workspace_id = $1 AND domain = $2`,
			p.WorkspaceID, it.Domain).Scan(&mx, &spf, &dkim, &dmarc, &checked)
		if errors.Is(err, pgx.ErrNoRows) {
			found = false
		} else if err != nil {
			return err
		}
		if it.Stage == ItemBuying && p.Supplier == SupplierManual && found && mx != nil && *mx == p.Provider {
			it.Stage = ItemDNS
			if err := setItem(ctx, tx, it.ID, it.Stage, ""); err != nil {
				return err
			}
		}
		if it.Stage == ItemDNS {
			if !found {
				if err := mailbox.EnsureDomain(ctx, tx, p.WorkspaceID, it.Domain, "plan"); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE sending_domains SET brand_id = COALESCE(brand_id, $3) WHERE workspace_id = $1 AND domain = $2`,
					p.WorkspaceID, it.Domain, p.BrandID); err != nil {
					return err
				}
			}
			if dnsReady(spf, dkim, dmarc) {
				it.Stage, it.Detail = ItemDone, ""
			} else {
				it.Detail = dnsWaiting(found && checked != nil, spf, dkim, dmarc)
				if s.Mail != nil && (checked == nil || now.Sub(*checked) > recheckDNS) {
					if err := s.Mail.QueueDomainCheck(ctx, tx, p.WorkspaceID, it.Domain); err != nil {
						return err
					}
				}
			}
			if err := setItem(ctx, tx, it.ID, it.Stage, it.Detail); err != nil {
				return err
			}
		}
		domainStage[it.Domain] = it.Stage
	}

	for i := range items {
		it := &items[i]
		if it.Kind != "mailbox" || it.Stage == ItemDone || it.Stage == ItemFailed || it.Stage == ItemCancelled {
			continue
		}
		ds := domainStage[it.Domain]
		if ds == ItemFailed || ds == ItemCancelled {
			if err := setItem(ctx, tx, it.ID, ItemCancelled, "Its domain was not set up."); err != nil {
				return err
			}
			it.Stage = ItemCancelled
			continue
		}
		var id string
		var created time.Time
		err := tx.QueryRow(ctx, `SELECT id::text, created_at FROM mailboxes WHERE workspace_id = $1 AND lower(email) = lower($2) FOR UPDATE`,
			p.WorkspaceID, it.Name).Scan(&id, &created)
		if errors.Is(err, pgx.ErrNoRows) {
			// Nothing to connect until the domain is bought.
			if ds == ItemPlanned || ds == ItemBuying {
				if it.Stage == ItemConnecting {
					if err := setItem(ctx, tx, it.ID, ItemPlanned, ""); err != nil {
						return err
					}
				}
				continue
			}
			detail := "Waiting for the mailbox to be connected. Import it on the Mailboxes page, or sign in with " + providerName(p.Provider) + "."
			if p.Supplier != SupplierManual {
				detail = "Waiting for " + supplierName(p.Supplier) + " to create it and hand over its sign-in."
			}
			if it.Stage != ItemConnecting || it.Detail != detail {
				if err := setItem(ctx, tx, it.ID, ItemConnecting, detail); err != nil {
					return err
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		// A mailbox connected for this plan starts in the warming stage;
		// its warm-up runs in a connected sequencer. One the workspace
		// already used keeps its stage and only joins the plan.
		fresh := p.ApprovedAt == nil || !created.Before(*p.ApprovedAt)
		if _, err := tx.Exec(ctx, `
			UPDATE mailboxes SET
				infra_plan_id = $2,
				brand_id = COALESCE(brand_id, $3),
				auto_rest = true,
				lifecycle_stage = CASE WHEN $4 THEN 'warming' ELSE lifecycle_stage END,
				stage_reason = CASE WHEN $4 THEN 'Warming up for ' || $5::int || ' days.' ELSE stage_reason END,
				stage_until = CASE WHEN $4 THEN now() + make_interval(days => $5::int) ELSE stage_until END,
				stage_changed_at = CASE WHEN $4 THEN now() ELSE stage_changed_at END,
				daily_limit = CASE WHEN $4 THEN $6 ELSE daily_limit END,
				updated_at = now()
			WHERE id = $1`, id, p.ID, p.BrandID, fresh, p.WarmupDays, p.MailboxDailyLimit); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE infra_plan_items SET stage = 'done', detail = '', mailbox_id = $2, stage_changed_at = now() WHERE id = $1`, it.ID, id); err != nil {
			return err
		}
		it.Stage = ItemDone
		if err := audit.Record(ctx, tx, system(p.WorkspaceID, "infra.mailbox_joined", "mailbox", id,
			map[string]any{"plan_id": p.ID, "email": it.Name, "warming": fresh})); err != nil {
			return err
		}
	}

	// The plan's status follows its items and mailboxes.
	open, done := 0, 0
	for _, it := range items {
		switch it.Stage {
		case ItemDone:
			done++
		case ItemFailed, ItemCancelled:
		default:
			open++
		}
	}
	next := p.Status
	if p.Status == PlanBuilding && open == 0 && done > 0 {
		next = PlanWarming
	}
	if next == PlanWarming {
		var waiting int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM mailboxes WHERE infra_plan_id = $1 AND lifecycle_stage = 'warming'`, p.ID).Scan(&waiting); err != nil {
			return err
		}
		if waiting == 0 {
			next = PlanReady
		}
	}
	if next == p.Status {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE infra_plans SET status = $2, completed_at = CASE WHEN $2 = 'ready' THEN now() END, updated_at = now() WHERE id = $1`,
		p.ID, next); err != nil {
		return err
	}
	return audit.Record(ctx, tx, system(p.WorkspaceID, "infra.plan_"+next, "infra_plan", p.ID, map[string]any{"from": p.Status}))
}

// dnsReady reports whether a domain's records let it send: SPF and DKIM
// pass, and DMARC exists (p=none is fine for a new domain).
func dnsReady(spf, dkim, dmarc string) bool {
	return spf == "pass" && dkim == "pass" && (dmarc == "pass" || dmarc == "none_policy")
}

func dnsWaiting(checked bool, spf, dkim, dmarc string) string {
	if !checked {
		return "Checking DNS."
	}
	var missing []string
	if spf != "pass" {
		missing = append(missing, "SPF")
	}
	if dkim != "pass" {
		missing = append(missing, "DKIM")
	}
	if dmarc != "pass" && dmarc != "none_policy" {
		missing = append(missing, "DMARC")
	}
	return "Waiting for " + strings.Join(missing, ", ") + ". The domain's page shows the exact records to add; it is checked again every 10 minutes."
}

// cancelJob cancels a retired mailbox at its supplier.
func (s *Service) cancelJob(ctx context.Context, job jobs.Job) error {
	var pl cancelPayload
	if err := json.Unmarshal(job.Payload, &pl); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	p, err := loadPlan(ctx, s.Pool, pl.PlanID, false)
	if err != nil || p == nil || p.Supplier == SupplierManual {
		return err
	}
	sup, err := LookupSupplier(p.Supplier)
	if err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	if err := sup.Cancel(ctx, p.account(), pl.Email); err != nil {
		return err
	}
	return audit.Record(ctx, s.Pool, system(p.WorkspaceID, "infra.mailbox_cancelled", "infra_plan", p.ID,
		map[string]any{"email": pl.Email, "supplier": p.Supplier}))
}
