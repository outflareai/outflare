package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/recmetrics"
)

// The account summary: everything the brand knows about one person or
// company in one call, so its page answers a salesperson's questions
// without ten requests. Where they are in the lifecycle and what moved
// them, who owns them, what they pay, which products they use and whether
// use is up or down, their people, deals, open conversations, who spoke
// last, where they came from, when they last visited, the open tasks, the
// automations that hold them, and what needs attention now.
//
// It reads stored data only and decides nothing. Every part is empty when
// the brand has no such data (no payments, no products, no support inbox),
// so a page shows only the cards that have something to say. A company's
// summary counts its people too.

// renewalAhead is how far ahead (and how long after) a renewal date is
// flagged.
const renewalAhead = 60 * 24 * time.Hour

// summaryList caps each list a summary carries.
const summaryList = 10

type summaryRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type summaryStage struct {
	// Stage is the record's lifecycle_stage field.
	Stage *string `json:"stage"`
	// Since is when the field last changed, and By what changed it: a
	// source such as manual, api or journey.
	Since *time.Time `json:"since"`
	By    *string    `json:"by"`
	// Status, Flags and ActiveProducts come from the product lifecycle,
	// when the brand uses it.
	Status         *string  `json:"status"`
	Flags          []string `json:"flags"`
	ActiveProducts int      `json:"active_products"`
	// LastMove is the latest lifecycle move, such as activated or churned.
	LastMove *summaryMove `json:"last_move"`
}

type summaryMove struct {
	Move       string    `json:"move"`
	FromStage  *string   `json:"from_stage"`
	ToStage    string    `json:"to_stage"`
	ProductKey *string   `json:"product_key"`
	At         time.Time `json:"at"`
}

type summaryBilling struct {
	Plan            any        `json:"plan"`
	Status          any        `json:"status"`
	MRR             any        `json:"mrr"`
	LifetimeRevenue any        `json:"lifetime_revenue"`
	CustomerSince   any        `json:"customer_since"`
	Currency        *string    `json:"currency"`
	Paid30d         int64      `json:"paid_30d"`
	PaidPrev30d     int64      `json:"paid_prev_30d"`
	LastPaymentAt   *time.Time `json:"last_payment_at"`
	FailedPayments  int        `json:"failed_payments_30d"`
}

type summaryProduct struct {
	ProductID  string     `json:"product_id"`
	Key        string     `json:"key"`
	Name       string     `json:"name"`
	Stage      string     `json:"stage"`
	Category   *string    `json:"category"`
	EnteredAt  time.Time  `json:"entered_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// summaryMetric is one of the record's metrics. Previous is the same
// measure over the window just before, when the brand has that metric too
// (usage this week and usage the week before), so a page can say up or
// down.
type summaryMetric struct {
	Key        string  `json:"key"`
	Name       string  `json:"name"`
	ProductKey *string `json:"product_key"`
	Value      any     `json:"value"`
	Previous   any     `json:"previous"`
	WindowDays int     `json:"window_days"`
}

type summaryPerson struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	JobTitle *string `json:"job_title"`
	// Role is the link's role property, such as buyer or decision_maker.
	Role *string `json:"role"`
	Kind string  `json:"kind"`
}

type summaryDeal struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Stage       string     `json:"stage"`
	State       string     `json:"state"`
	AmountCents *int64     `json:"amount_cents"`
	Currency    *string    `json:"currency"`
	OpenedAt    *time.Time `json:"opened_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	// LostReason and LostNote are the deal record's fields, for a lost
	// deal: why they didn't move forward.
	LostReason *string `json:"lost_reason"`
	LostNote   *string `json:"lost_note"`
}

// summaryPipeline is a record of another type that points at this one: a
// deal, a partnership, or a type the brand made.
type summaryPipeline struct {
	ID    string  `json:"id"`
	Type  string  `json:"type"`
	Name  string  `json:"name"`
	Stage *string `json:"stage"`
	Value any     `json:"value"`
	// RenewsOn is the record's renews_on date field, when its type has
	// one, such as an agreement.
	RenewsOn  *string   `json:"renews_on"`
	UpdatedAt time.Time `json:"updated_at"`
}

type summaryConversation struct {
	ID            string     `json:"id"`
	Subject       string     `json:"subject"`
	Channel       string     `json:"channel"`
	Status        string     `json:"status"`
	LastFrom      *string    `json:"last_from"`
	LastMessageAt *time.Time `json:"last_message_at"`
	AssigneeID    *string    `json:"assignee_id"`
	AssigneeName  *string    `json:"assignee_name"`
	Breached      bool       `json:"breached"`
}

type summaryTask struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Priority     string     `json:"priority"`
	DueAt        *time.Time `json:"due_at"`
	AssignedTo   *string    `json:"assigned_to"`
	AssignedName *string    `json:"assigned_name"`
	RecordID     string     `json:"record_id"`
}

type summaryAutomation struct {
	// Kind is journey or sequence.
	Kind     string     `json:"kind"`
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Status   string     `json:"status"`
	Since    time.Time  `json:"since"`
	NextAt   *time.Time `json:"next_at"`
	Reason   *string    `json:"reason"`
	RecordID string     `json:"record_id"`
}

type summaryAlert struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	OwnerID   *string   `json:"owner_id"`
	OwnerName *string   `json:"owner_name"`
	CreatedAt time.Time `json:"created_at"`
}

// summaryAttention is one thing to act on now. Kind is one of
// task_overdue, conversation_waiting, reply_overdue, payment_failed,
// billing_past_due, renewal_due, at_risk, no_owner and came_back.
type summaryAttention struct {
	Kind  string     `json:"kind"`
	Count int        `json:"count"`
	At    *time.Time `json:"at"`
	ID    *string    `json:"id"`
}

type accountSummary struct {
	RecordID string       `json:"record_id"`
	Type     string       `json:"type"`
	Name     string       `json:"name"`
	Company  *summaryRef  `json:"company"`
	Owner    *summaryRef  `json:"owner"`
	Stage    summaryStage `json:"stage"`
	// Plan is the account plan the owner keeps: the goal, next_step,
	// hesitations and limits fields.
	Plan     map[string]any   `json:"plan"`
	Billing  *summaryBilling  `json:"billing"`
	Products []summaryProduct `json:"products"`
	Metrics  []summaryMetric  `json:"metrics"`
	People   []summaryPerson  `json:"people"`
	// PeopleTotal counts all the company's people; People lists the first.
	PeopleTotal int `json:"people_total"`
	Deals       struct {
		Open   int           `json:"open"`
		Won    int           `json:"won"`
		Lost   int           `json:"lost"`
		Latest []summaryDeal `json:"latest"`
	} `json:"deals"`
	Pipelines     []summaryPipeline `json:"pipelines"`
	Conversations struct {
		Open int `json:"open"`
		// Waiting is how many open ones the customer wrote last in.
		Waiting int                   `json:"waiting"`
		Latest  []summaryConversation `json:"latest"`
	} `json:"conversations"`
	Contact struct {
		LastAt *time.Time `json:"last_at"`
		// LastChannel is outreach, inbox, meeting, support or email, and
		// LastDirection in (they wrote) or out (the brand did).
		LastChannel   *string    `json:"last_channel"`
		LastDirection *string    `json:"last_direction"`
		LastInAt      *time.Time `json:"last_in_at"`
		LastOutAt     *time.Time `json:"last_out_at"`
		Stale         bool       `json:"stale"`
	} `json:"contact"`
	Team    []relCheckTeammate `json:"team"`
	Website struct {
		FirstVisitAt *time.Time `json:"first_visit_at"`
		LastVisitAt  *time.Time `json:"last_visit_at"`
		Visits30d    int        `json:"visits_30d"`
		LastPath     *string    `json:"last_path"`
	} `json:"website"`
	Origin struct {
		// Origin is how the record was made: import, api, form and so on.
		Origin      string    `json:"origin"`
		AcquiredVia *string   `json:"acquired_via"`
		Source      *string   `json:"source"`
		Medium      *string   `json:"medium"`
		Campaign    *string   `json:"campaign"`
		Referrer    *string   `json:"referrer"`
		LandingPath *string   `json:"landing_path"`
		CreatedAt   time.Time `json:"created_at"`
	} `json:"origin"`
	Tasks struct {
		Open    int           `json:"open"`
		Overdue int           `json:"overdue"`
		Next    []summaryTask `json:"next"`
	} `json:"tasks"`
	Automations []summaryAutomation `json:"automations"`
	Alerts      []summaryAlert      `json:"alerts"`
	Attention   []summaryAttention  `json:"attention"`
}

func (s *Server) routesRecordSummary(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/records/{record}/summary", s.onBrand(auth.ScopeCustomersRead, s.accountSummary))
}

// planKeys are the account plan's fields.
var planKeys = []string{"goal", "next_step", "hesitations", "limits"}

// accountPlanFields are the account plan: what the owner is trying to get to
// with a person or company, and what stands in the way.
var accountPlanFields = []fieldView{
	builtinField("goal", "Goal", "text", "", "", false, false, "What you want with them next: a first order, a renewal, a bigger plan."),
	builtinField("next_step", "Next step", "text", "", "", false, false, "The one thing that happens next, and who does it."),
	builtinField("hesitations", "Hesitations", "text", "", "", false, false, "What holds them back: price, timing, a competitor, trust."),
	builtinField("limits", "Limits", "text", "", "", false, false, "What they can't do: budget, contract dates, who has to sign."),
}

func (s *Server) accountSummary(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	out := accountSummary{Plan: map[string]any{}, Products: []summaryProduct{}, Metrics: []summaryMetric{}, People: []summaryPerson{},
		Pipelines: []summaryPipeline{}, Team: []relCheckTeammate{}, Automations: []summaryAutomation{}, Alerts: []summaryAlert{}, Attention: []summaryAttention{}}
	out.Stage.Flags, out.Deals.Latest, out.Conversations.Latest, out.Tasks.Next = []string{}, []summaryDeal{}, []summaryConversation{}, []summaryTask{}
	var subjects []string
	var support struct{ in, out *time.Time }
	var sent, received, met *time.Time
	var breached, openSignals int
	var breachedAt, signalDue *time.Time
	var overdueAt *time.Time
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rec, err := loadRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		// The page is for a person or a company of this brand.
		if rec.BrandID == nil || *rec.BrandID != a.Brand || (rec.Type != "person" && rec.Type != "account") {
			return errNotFound
		}
		ws, brand := a.WorkspaceID, a.Brand
		out.RecordID, out.Type, out.Name = rec.ID, rec.Type, rec.Name
		out.Origin.Origin, out.Origin.CreatedAt = rec.Origin, rec.CreatedAt
		if err := tx.QueryRow(ctx, `SELECT NULLIF(acquired_via, '') FROM records WHERE id = $1 AND workspace_id = $2`, rec.ID, a.WorkspaceID).Scan(&out.Origin.AcquiredVia); err != nil {
			return err
		}
		subjects = []string{rec.ID}

		// The person's company, or the company's people.
		if rec.Type == "person" {
			company, err := relationshipCompany(ctx, tx, ws, rec)
			if err != nil {
				return err
			}
			if company != "" {
				ref := summaryRef{ID: company}
				if err := tx.QueryRow(ctx, `SELECT name FROM records WHERE id = $1 AND workspace_id = $2`, company, ws).Scan(&ref.Name); err != nil {
					return err
				}
				out.Company = &ref
			}
		} else {
			rows, err := tx.Query(ctx, `SELECT DISTINCT ON (p.id) p.id::text, p.name, NULLIF(p.attributes->>'job_title', ''), NULLIF(l.properties->>'role', ''), l.kind
				FROM record_relationships l
				JOIN records p ON p.id = CASE WHEN l.from_id = $1 THEN l.to_id ELSE l.from_id END
				WHERE (l.from_id = $1 OR l.to_id = $1) AND l.valid_to IS NULL AND l.workspace_id = $2
					AND p.workspace_id = $2 AND p.record_type = 'person' AND p.deleted_at IS NULL AND p.merged_into IS NULL
				ORDER BY p.id, (l.properties->>'role') IS NULL, l.valid_from LIMIT $3`, rec.ID, ws, timelinePeople)
			if err != nil {
				return err
			}
			people, err := pgx.CollectRows(rows, pgx.RowToStructByPos[summaryPerson])
			if err != nil {
				return err
			}
			// People with a role first, then by name.
			slices.SortStableFunc(people, func(x, y summaryPerson) int {
				if (x.Role == nil) != (y.Role == nil) {
					if x.Role != nil {
						return -1
					}
					return 1
				}
				switch {
				case x.Name < y.Name:
					return -1
				case x.Name > y.Name:
					return 1
				}
				return 0
			})
			out.PeopleTotal = len(people)
			for _, p := range people {
				subjects = append(subjects, p.ID)
			}
			out.People = people[:min(len(people), summaryList)]
		}

		// Owner.
		if owner, _ := rec.Attributes["owner"].(string); validUUID(owner) {
			ref := summaryRef{ID: owner}
			err := tx.QueryRow(ctx, `SELECT u.name FROM users u JOIN memberships m ON m.user_id = u.id AND m.workspace_id = $2 WHERE u.id = $1
				UNION ALL SELECT st.name FROM staff st WHERE st.id = $1 AND st.workspace_id = $2 AND st.user_id IS NULL LIMIT 1`, owner, ws).Scan(&ref.Name)
			if err == nil {
				out.Owner = &ref
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}

		// Stage: the field, when and what last changed it, and the product
		// lifecycle.
		if st, _ := rec.Attributes["lifecycle_stage"].(string); st != "" {
			out.Stage.Stage = &st
			err := tx.QueryRow(ctx, `SELECT changed_at, source FROM attribute_changes
				WHERE workspace_id = $1 AND record_id = $2 AND field_key = 'lifecycle_stage' ORDER BY changed_at DESC, id DESC LIMIT 1`, ws, rec.ID).
				Scan(&out.Stage.Since, &out.Stage.By)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var flags []string
		err = tx.QueryRow(ctx, `SELECT status, flags, active_products FROM lifecycle_overall WHERE record_id = $1 AND workspace_id = $2 AND brand_id = $3`,
			rec.ID, ws, brand).Scan(&out.Stage.Status, &flags, &out.Stage.ActiveProducts)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out.Stage.Flags = nonNil(flags)
		var mv summaryMove
		err = tx.QueryRow(ctx, `SELECT t.move, t.from_stage, t.to_stage, p.key, t.at FROM lifecycle_transitions t LEFT JOIN products p ON p.id = t.product_id
			WHERE t.record_id = $1 AND t.workspace_id = $2 AND t.brand_id = $3 ORDER BY t.at DESC, t.id DESC LIMIT 1`, rec.ID, ws, brand).
			Scan(&mv.Move, &mv.FromStage, &mv.ToStage, &mv.ProductKey, &mv.At)
		if err == nil {
			out.Stage.LastMove = &mv
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		// The account plan.
		for _, k := range planKeys {
			if v, ok := rec.Attributes[k]; ok && v != nil && v != "" {
				out.Plan[k] = v
			}
		}

		// Billing: the computed fields, plus payments in the last 30 days
		// and the 30 before.
		var b summaryBilling
		b.Plan, b.Status, b.MRR = rec.Attributes["billing_plan"], rec.Attributes["billing_status"], rec.Attributes["mrr"]
		b.LifetimeRevenue, b.CustomerSince = rec.Attributes["lifetime_revenue"], rec.Attributes["customer_since"]
		if err := tx.QueryRow(ctx, `SELECT
				COALESCE(sum(CASE WHEN kind = 'payment' THEN brand_amount_minor ELSE -brand_amount_minor END) FILTER (WHERE status = 'succeeded' AND kind IN ('payment', 'refund') AND occurred_at > now() - interval '30 days'), 0)::bigint,
				COALESCE(sum(CASE WHEN kind = 'payment' THEN brand_amount_minor ELSE -brand_amount_minor END) FILTER (WHERE status = 'succeeded' AND kind IN ('payment', 'refund') AND occurred_at <= now() - interval '30 days' AND occurred_at > now() - interval '60 days'), 0)::bigint,
				max(occurred_at) FILTER (WHERE status = 'succeeded' AND kind = 'payment'),
				count(*) FILTER (WHERE status = 'failed' AND kind = 'payment' AND occurred_at > now() - interval '30 days'),
				max(brand_currency)
			FROM transactions WHERE workspace_id = $1 AND brand_id = $2 AND record_id = ANY($3)`, ws, brand, subjects).
			Scan(&b.Paid30d, &b.PaidPrev30d, &b.LastPaymentAt, &b.FailedPayments, &b.Currency); err != nil {
			return err
		}
		if b.Plan != nil || b.Status != nil || b.MRR != nil || b.LifetimeRevenue != nil || b.LastPaymentAt != nil || b.FailedPayments > 0 {
			out.Billing = &b
		}

		// Products and metrics.
		rows, err := tx.Query(ctx, `SELECT s.product_id::text, p.key, COALESCE(p.name, p.key), s.stage, st.category, s.entered_at, s.last_used_at
			FROM lifecycle_states s JOIN products p ON p.id = s.product_id AND p.archived_at IS NULL
			LEFT JOIN lifecycle_stages st ON st.brand_id = s.brand_id AND st.level = s.level AND st.key = s.stage
			WHERE s.record_id = $1 AND s.workspace_id = $2 AND s.brand_id = $3 ORDER BY 3`, rec.ID, ws, brand)
		if err != nil {
			return err
		}
		if out.Products, err = pgx.CollectRows(rows, pgx.RowToStructByPos[summaryProduct]); err != nil {
			return err
		}
		// A metric's Previous is the value of the metric that measures the
		// same thing over the window just before it.
		rows, err = tx.Query(ctx, `SELECT d.key, d.name, p.key, v.value, pv.value, d.window_days
			FROM metric_values v JOIN metric_definitions d ON d.id = v.metric_id AND d.status = 'active'
			LEFT JOIN products p ON p.id = v.product_id AND v.product_id::text <> $4
			LEFT JOIN metric_definitions pd ON pd.workspace_id = d.workspace_id AND pd.brand_id = d.brand_id AND pd.status = 'active' AND pd.id <> d.id
				AND pd.aggregate = d.aggregate AND pd.events = d.events AND pd.usage = d.usage AND pd.property = d.property
				AND pd.window_days = d.window_days AND pd.per_product = d.per_product AND d.window_days > 0
				AND pd.offset_days = d.offset_days + d.window_days
			LEFT JOIN metric_values pv ON pv.metric_id = pd.id AND pv.record_id = v.record_id AND pv.product_id = v.product_id
			WHERE v.record_id = $1 AND v.workspace_id = $2 AND v.brand_id = $3 AND d.offset_days = 0
			ORDER BY d.key, p.key NULLS FIRST LIMIT 100`, rec.ID, ws, brand, recmetrics.NoProduct)
		if err != nil {
			return err
		}
		if out.Metrics, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (summaryMetric, error) {
			var m summaryMetric
			var v, pv []byte
			if err := row.Scan(&m.Key, &m.Name, &m.ProductKey, &v, &pv, &m.WindowDays); err != nil {
				return m, err
			}
			if len(v) > 0 {
				_ = json.Unmarshal(v, &m.Value)
			}
			if len(pv) > 0 {
				_ = json.Unmarshal(pv, &m.Previous)
			}
			return m, nil
		}); err != nil {
			return err
		}

		// Deals from outreach and connected CRMs, and records of other
		// types that point here.
		const dealsOf = `FROM deals d WHERE d.workspace_id = $1 AND (d.brand_id = $2 OR d.brand_id IS NULL) AND (d.record_id = ANY($3)
			OR EXISTS (SELECT 1 FROM record_relationships l WHERE l.from_id = d.id AND l.to_id = ANY($3) AND l.valid_to IS NULL))`
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE d.state = 'open'), count(*) FILTER (WHERE d.state = 'won'), count(*) FILTER (WHERE d.state = 'lost') `+dealsOf,
			ws, brand, subjects).Scan(&out.Deals.Open, &out.Deals.Won, &out.Deals.Lost); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT d.id::text, COALESCE(d.name, ''), COALESCE(d.stage, ''), COALESCE(d.state, 'open'), d.amount_cents, d.currency, d.opened_at, d.closed_at,
				(SELECT NULLIF(r.attributes->>'lost_reason', '') FROM records r WHERE r.id = d.id AND r.workspace_id = d.workspace_id),
				(SELECT NULLIF(r.attributes->>'lost_note', '') FROM records r WHERE r.id = d.id AND r.workspace_id = d.workspace_id) `+dealsOf+`
			ORDER BY (d.state = 'open') DESC, COALESCE(d.closed_at, d.opened_at, d.created_at) DESC LIMIT $4`, ws, brand, subjects, summaryList)
		if err != nil {
			return err
		}
		if out.Deals.Latest, err = pgx.CollectRows(rows, pgx.RowToStructByPos[summaryDeal]); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT r.id::text, r.record_type, r.name, NULLIF(r.attributes->>'stage', ''), r.attributes->'value',
				CASE WHEN r.attributes->>'renews_on' ~ '^\d{4}-\d{2}-\d{2}$' THEN r.attributes->>'renews_on' END, r.updated_at FROM records r
			WHERE r.workspace_id = $1 AND r.brand_id = $2 AND r.record_type NOT IN ('person', 'account')
				AND r.deleted_at IS NULL AND r.merged_into IS NULL AND r.archived_at IS NULL
				AND NOT EXISTS (SELECT 1 FROM deals d WHERE d.id = r.id AND d.workspace_id = $1)
				AND (r.attributes->>'company' = ANY($4) OR EXISTS (SELECT 1 FROM record_relationships l
					WHERE l.workspace_id = $1 AND l.from_id = r.id AND l.to_id = ANY($3) AND l.valid_to IS NULL))
			ORDER BY r.updated_at DESC LIMIT $5`, ws, brand, subjects, subjects, summaryList)
		if err != nil {
			return err
		}
		if out.Pipelines, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (summaryPipeline, error) {
			var p summaryPipeline
			var v []byte
			if err := row.Scan(&p.ID, &p.Type, &p.Name, &p.Stage, &v, &p.RenewsOn, &p.UpdatedAt); err != nil {
				return p, err
			}
			if len(v) > 0 {
				_ = json.Unmarshal(v, &p.Value)
			}
			return p, nil
		}); err != nil {
			return err
		}

		// Support conversations.
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status <> 'closed'),
				count(*) FILTER (WHERE status = 'open' AND last_from = 'customer'),
				count(*) FILTER (WHERE status <> 'closed' AND (reply_breached_at IS NOT NULL OR resolve_breached_at IS NOT NULL)),
				min(LEAST(reply_breached_at, resolve_breached_at)) FILTER (WHERE status <> 'closed'),
				max(last_message_at) FILTER (WHERE last_from = 'customer'), max(last_message_at) FILTER (WHERE last_from = 'team')
			FROM support_conversations WHERE workspace_id = $1 AND brand_id = $2 AND record_id = ANY($3)`, ws, brand, subjects).
			Scan(&out.Conversations.Open, &out.Conversations.Waiting, &breached, &breachedAt, &support.in, &support.out); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT c.id::text, c.subject, c.channel, c.status, c.last_from, c.last_message_at, c.assignee_id::text, u.name,
				(c.reply_breached_at IS NOT NULL OR c.resolve_breached_at IS NOT NULL)
			FROM support_conversations c LEFT JOIN users u ON u.id = c.assignee_id
			WHERE c.workspace_id = $1 AND c.brand_id = $2 AND c.record_id = ANY($3)
			ORDER BY (c.status <> 'closed') DESC, c.last_message_at DESC NULLS LAST LIMIT $4`, ws, brand, subjects, summaryList)
		if err != nil {
			return err
		}
		if out.Conversations.Latest, err = pgx.CollectRows(rows, pgx.RowToStructByPos[summaryConversation]); err != nil {
			return err
		}

		// Outreach sent, replies received and meetings held.
		if err := tx.QueryRow(ctx, `SELECT
				(SELECT max(m.sent_at) FROM messages m WHERE m.workspace_id = $1 AND (m.brand_id = $2 OR m.brand_id IS NULL) AND m.record_id = ANY($3) AND m.sent_at IS NOT NULL),
				(SELECT max(n.received_at) FROM inbound_messages n WHERE n.workspace_id = $1 AND (n.brand_id = $2 OR n.brand_id IS NULL) AND n.record_id = ANY($3)),
				(SELECT max(g.starts_at) FROM meetings g WHERE g.workspace_id = $1 AND (g.brand_id = $2 OR g.brand_id IS NULL) AND g.record_id = ANY($3) AND g.starts_at <= now())`,
			ws, brand, subjects).Scan(&sent, &received, &met); err != nil {
			return err
		}

		// Website visits.
		if err := tx.QueryRow(ctx, `SELECT min(started_at), max(last_seen_at), count(*) FILTER (WHERE started_at > now() - interval '30 days'),
				(array_agg(landing_path ORDER BY started_at DESC))[1],
				(array_agg(NULLIF(COALESCE(NULLIF(utm_source, ''), source), '') ORDER BY started_at))[1],
				(array_agg(NULLIF(utm_medium, '') ORDER BY started_at))[1],
				(array_agg(NULLIF(utm_campaign, '') ORDER BY started_at))[1],
				(array_agg(NULLIF(referrer_host, '') ORDER BY started_at))[1],
				(array_agg(NULLIF(landing_path, '') ORDER BY started_at))[1]
			FROM site_sessions WHERE workspace_id = $1 AND brand_id = $2 AND record_id = ANY($3)`, ws, brand, subjects).
			Scan(&out.Website.FirstVisitAt, &out.Website.LastVisitAt, &out.Website.Visits30d, &out.Website.LastPath,
				&out.Origin.Source, &out.Origin.Medium, &out.Origin.Campaign, &out.Origin.Referrer, &out.Origin.LandingPath); err != nil {
			return err
		}
		// A person's first visit is kept on the record, also after its
		// sessions age out.
		for k, to := range map[string]**string{"first_touch_source": &out.Origin.Source, "first_touch_medium": &out.Origin.Medium,
			"first_touch_campaign": &out.Origin.Campaign, "first_touch_referrer": &out.Origin.Referrer, "first_touch_landing_page": &out.Origin.LandingPath} {
			if v, _ := rec.Attributes[k].(string); v != "" {
				*to = &v
			}
		}

		// Open tasks.
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE due_at < now()), min(due_at) FILTER (WHERE due_at < now())
			FROM crm_activities WHERE workspace_id = $1 AND brand_id = $2 AND record_id = ANY($3) AND kind = 'task' AND status = 'open'`,
			ws, brand, subjects).Scan(&out.Tasks.Open, &out.Tasks.Overdue, &overdueAt); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT t.id::text, t.title, t.priority, t.due_at, t.assigned_to::text, u.name, t.record_id::text
			FROM crm_activities t LEFT JOIN users u ON u.id = t.assigned_to
			WHERE t.workspace_id = $1 AND t.brand_id = $2 AND t.record_id = ANY($3) AND t.kind = 'task' AND t.status = 'open'
			ORDER BY t.due_at NULLS LAST, t.created_at LIMIT $4`, ws, brand, subjects, summaryList)
		if err != nil {
			return err
		}
		if out.Tasks.Next, err = pgx.CollectRows(rows, pgx.RowToStructByPos[summaryTask]); err != nil {
			return err
		}

		// Automations: journeys they are in or left lately, and sequences.
		rows, err = tx.Query(ctx, `SELECT * FROM (
				SELECT 'journey' AS kind, y.id::text, COALESCE(y.name, '') AS name, COALESCE(j.status, '') AS status, j.entered_at AS since, j.next_at, NULLIF(j.exit_reason, '') AS reason, j.record_id::text
				FROM journey_runs j JOIN journeys y ON y.id = j.journey_id
				WHERE j.workspace_id = $1 AND y.brand_id = $2 AND j.record_id = ANY($3)
					AND (j.status = 'active' OR j.finished_at > now() - interval '30 days')
				UNION ALL
				SELECT 'sequence', q.id::text, COALESCE(q.name, ''), COALESCE(e.status, ''), COALESCE(e.created_at, e.updated_at), e.next_send_at, NULLIF(e.status_reason, ''), l.record_id::text
				FROM enrollments e JOIN leads l ON l.id = e.lead_id JOIN sequences q ON q.id = e.sequence_id
				WHERE e.workspace_id = $1 AND q.brand_id = $2 AND l.record_id = ANY($3)
					AND (e.status = 'active' OR e.updated_at > now() - interval '30 days')
			) x ORDER BY (status = 'active') DESC, since DESC LIMIT $4`, ws, brand, subjects, summaryList)
		if err != nil {
			return err
		}
		if out.Automations, err = pgx.CollectRows(rows, pgx.RowToStructByPos[summaryAutomation]); err != nil {
			return err
		}

		// Alerts raised about them.
		rows, err = tx.Query(ctx, `SELECT al.id::text, al.kind, al.owner_id::text, u.name, al.created_at FROM alerts al LEFT JOIN users u ON u.id = al.owner_id
			WHERE al.workspace_id = $1 AND al.brand_id = $2 AND al.record_id = ANY($3) ORDER BY al.created_at DESC LIMIT $4`, ws, brand, subjects, summaryList)
		if err != nil {
			return err
		}
		if out.Alerts, err = pgx.CollectRows(rows, pgx.RowToStructByPos[summaryAlert]); err != nil {
			return err
		}

		// Open signals about them (docs/signals.md), the soonest due first.
		return tx.QueryRow(ctx, `SELECT count(*), min(due_at) FROM signals WHERE workspace_id = $1 AND brand_id = $2 AND record_id = ANY($3)
			AND status IN ('open', 'accepted') AND (snoozed_until IS NULL OR snoozed_until < now())`, ws, brand, subjects).Scan(&openSignals, &signalDue)
	})
	if s.recordError(w, r, err) {
		return
	}

	// Personal email counts where the person asking may see it.
	var emailed *time.Time
	if a.UserID != "" && s.box != nil {
		team, last, err := s.relationshipTeam(ctx, a, subjects)
		if s.allocError(w, r, err) {
			return
		}
		out.Team, emailed = team, last
	}

	// Who spoke last, on any channel.
	type touch struct {
		at        *time.Time
		ch, dir   string
		countsTwo bool
	}
	two := false
	for _, t := range []touch{{sent, "outreach", "out", false}, {received, "inbox", "in", true}, {met, "meeting", "out", true},
		{support.in, "support", "in", true}, {support.out, "support", "out", false}, {emailed, "email", "out", true}} {
		if t.at == nil {
			continue
		}
		two = two || t.countsTwo
		if t.dir == "in" && (out.Contact.LastInAt == nil || t.at.After(*out.Contact.LastInAt)) {
			out.Contact.LastInAt = t.at
		}
		if t.dir == "out" && t.ch != "meeting" && (out.Contact.LastOutAt == nil || t.at.After(*out.Contact.LastOutAt)) {
			out.Contact.LastOutAt = t.at
		}
		if out.Contact.LastAt == nil || t.at.After(*out.Contact.LastAt) {
			ch, dir := t.ch, t.dir
			out.Contact.LastAt, out.Contact.LastChannel, out.Contact.LastDirection = t.at, &ch, &dir
			if ch == "meeting" || ch == "email" {
				out.Contact.LastDirection = nil
			}
		}
	}
	out.Contact.Stale = two && out.Contact.LastAt != nil && time.Since(*out.Contact.LastAt) > staleAfter

	// What needs attention, most pressing first.
	add := func(kind string, n int, at *time.Time) {
		if n > 0 {
			out.Attention = append(out.Attention, summaryAttention{Kind: kind, Count: n, At: at})
		}
	}
	add("signal", openSignals, signalDue)
	add("reply_overdue", breached, breachedAt)
	add("conversation_waiting", out.Conversations.Waiting, support.in)
	add("task_overdue", out.Tasks.Overdue, overdueAt)
	if out.Billing != nil {
		add("payment_failed", out.Billing.FailedPayments, nil)
		if st, _ := out.Billing.Status.(string); st == "past_due" {
			add("billing_past_due", 1, nil)
		}
	}
	// A renewal date in the next 60 days, on a record that hasn't ended.
	var renewals int
	var soonest *time.Time
	for _, p := range out.Pipelines {
		if p.RenewsOn == nil || (p.Stage != nil && slices.Contains([]string{"ended", "renewed", "lost", "declined"}, *p.Stage)) {
			continue
		}
		day, err := time.Parse(time.DateOnly, *p.RenewsOn)
		if err != nil || time.Until(day) > renewalAhead || time.Until(day) < -renewalAhead {
			continue
		}
		renewals++
		if soonest == nil || day.Before(*soonest) {
			soonest = &day
		}
	}
	add("renewal_due", renewals, soonest)
	if slices.Contains(out.Stage.Flags, "at_risk") {
		add("at_risk", 1, nil)
	}
	// They came back to the site after the last time anyone talked to them.
	if v := out.Website.LastVisitAt; v != nil && time.Since(*v) < 7*24*time.Hour && out.Contact.LastAt != nil && v.After(*out.Contact.LastAt) &&
		time.Since(*out.Contact.LastAt) > 14*24*time.Hour {
		add("came_back", 1, v)
	}
	if out.Owner == nil && (out.Deals.Open > 0 || out.Conversations.Open > 0 || out.Billing != nil) {
		add("no_owner", 1, nil)
	}
	writeJSON(w, http.StatusOK, out)
}
