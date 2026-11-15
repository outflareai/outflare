package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// A company's group: the parent it belongs to and the companies under
// it, with what each pays, its health and its open signals rolled up to
// one line. And its share of wallet: what the brand earns from the
// account against what the account spends on what the brand sells.

// groupFields link a company to its parent.
var groupFields = []fieldView{
	builtinField("parent_company", "Parent company", "record_reference", "", "account", false, true,
		"The company this one belongs to: a subsidiary's group, a separate sign-up's main account. Revenue, health and signals roll up to the parent."),
}

const (
	groupDepth = 6   // how far above and below a company its group is followed
	groupMax   = 200 // the most companies one group shows
)

func (s *Server) routesRecordGroup(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/records/{record}/group", s.onBrand(auth.ScopeCustomersRead, s.recordGroup))
}

type groupCompany struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id"`
	// Depth is 0 for the top of the group.
	Depth int `json:"depth"`
	// MRR is what its subscriptions bring in a month at the last revenue
	// count; Paid12m what it and its people paid in the last 12 months.
	MRR         int64   `json:"mrr_minor"`
	Paid12m     int64   `json:"paid_12m_minor"`
	Health      *int    `json:"health"`
	HealthBand  *string `json:"health_band"`
	OpenSignals int     `json:"open_signals"`
	People      int     `json:"people"`
	// Wallet is its own estimate, when it has one in the brand's currency.
	Wallet *int64 `json:"wallet_minor"`
}

type groupTotals struct {
	Companies   int   `json:"companies"`
	MRR         int64 `json:"mrr_minor"`
	Paid12m     int64 `json:"paid_12m_minor"`
	OpenSignals int   `json:"open_signals"`
	People      int   `json:"people"`
	// LowHealth is how many companies are in the low health band.
	LowHealth int `json:"low_health"`
	// Wallet is the group's estimates added up, and Share what the
	// companies that have one paid in 12 months against it; both null
	// with no estimate.
	Wallet *int64   `json:"wallet_minor"`
	Share  *float64 `json:"share"`
}

type recordWallet struct {
	// Estimate is what the account spends a year on what the brand sells,
	// with every supplier.
	Estimate   int64   `json:"estimate_minor"`
	Currency   string  `json:"currency"`
	Confidence *string `json:"confidence"`
	Source     *string `json:"source"`
	Suppliers  *string `json:"suppliers"`
	Paid12m    int64   `json:"paid_12m_minor"`
	// Share is Paid12m over Estimate; null when the estimate is in
	// another currency than the brand's.
	Share *float64 `json:"share"`
}

type recordGroup struct {
	RecordID string `json:"record_id"`
	Currency string `json:"currency"`
	// Parent is the company straight above; RootID the top of the group.
	Parent *summaryRef `json:"parent"`
	RootID string      `json:"root_id"`
	// Companies is the whole group from the top down, a parent before its
	// children; just the record itself when it has no group.
	Companies []groupCompany `json:"companies"`
	Totals    groupTotals    `json:"totals"`
	Truncated bool           `json:"truncated"`
	Wallet    *recordWallet  `json:"wallet"`
}

func walletOf(attrs map[string]any, currency string) (int64, string, bool) {
	m, _ := attrs["wallet_estimate"].(map[string]any)
	amount, ok := m["amount_minor"].(float64)
	code, _ := m["currency"].(string)
	if !ok || amount <= 0 {
		return 0, "", false
	}
	if code == "" {
		code = currency
	}
	return int64(amount), code, true
}

func textAttr(attrs map[string]any, key string) *string {
	if v, _ := attrs[key].(string); strings.TrimSpace(v) != "" {
		return &v
	}
	return nil
}

func (s *Server) recordGroup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	out := recordGroup{Companies: []groupCompany{}}
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rec, err := loadRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		if rec.BrandID == nil || *rec.BrandID != a.Brand {
			return errNotFound
		}
		out.RecordID, out.RootID = rec.ID, rec.ID
		if err := tx.QueryRow(ctx, `SELECT currency FROM brands WHERE id = $1 AND workspace_id = $2`, a.Brand, a.WorkspaceID).Scan(&out.Currency); err != nil {
			return err
		}
		if rec.Type != "account" {
			return nil
		}
		// Up to the top, never round a loop.
		const live = ` AND c.workspace_id = $1 AND c.brand_id = $2 AND c.record_type = 'account' AND c.merged_into IS NULL AND c.deleted_at IS NULL`
		if err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (
				SELECT $3::uuid AS id, 0 AS depth, ARRAY[$3::uuid] AS seen
				UNION ALL
				SELECT c.id, up.depth + 1, up.seen || c.id FROM up
				JOIN record_relationships l ON l.from_id = up.id AND l.field_key = 'parent_company' AND l.valid_to IS NULL AND l.workspace_id = $1
				JOIN records c ON c.id = l.to_id`+live+`
				WHERE up.depth < $4 AND NOT c.id = ANY (up.seen))
			SELECT id::text FROM up ORDER BY depth DESC LIMIT 1`, a.WorkspaceID, a.Brand, rec.ID, groupDepth).Scan(&out.RootID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `WITH RECURSIVE down AS (
				SELECT c.id, NULL::uuid AS parent_id, 0 AS depth, ARRAY[c.id] AS seen, ARRAY[c.name] AS path FROM records c WHERE c.id = $3`+live+`
				UNION ALL
				SELECT c.id, down.id, down.depth + 1, down.seen || c.id, down.path || c.name FROM down
				JOIN record_relationships l ON l.to_id = down.id AND l.field_key = 'parent_company' AND l.valid_to IS NULL AND l.workspace_id = $1
				JOIN records c ON c.id = l.from_id`+live+`
				WHERE down.depth < $4 AND NOT c.id = ANY (down.seen)),
			people AS (
				SELECT d.id, l.from_id AS person FROM down d JOIN record_relationships l ON l.to_id = d.id AND l.kind = 'companies' AND l.valid_to IS NULL AND l.workspace_id = $1)
			SELECT d.id::text, c.name, d.parent_id::text, d.depth, c.attributes,
				COALESCE((SELECT sum(v.mrr_minor) FROM revenue_customers v WHERE v.workspace_id = $1 AND v.brand_id = $2
					AND (v.record_id = d.id OR v.record_id IN (SELECT person FROM people p WHERE p.id = d.id))), 0)::bigint,
				COALESCE((SELECT sum(t.brand_amount_minor) FROM transactions t WHERE t.workspace_id = $1 AND t.brand_id = $2 AND t.status = 'succeeded'
					AND t.kind = 'payment' AND t.brand_amount_minor IS NOT NULL AND t.occurred_at >= now() - interval '365 days'
					AND (t.record_id = d.id OR t.record_id IN (SELECT person FROM people p WHERE p.id = d.id))), 0)::bigint,
				(SELECT count(*) FROM signals g WHERE g.workspace_id = $1 AND g.brand_id = $2 AND g.record_id = d.id AND g.status IN ('open', 'accepted')),
				(SELECT count(*) FROM people p WHERE p.id = d.id)
			FROM down d JOIN records c ON c.id = d.id ORDER BY d.path LIMIT $5`, a.WorkspaceID, a.Brand, out.RootID, groupDepth, groupMax+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		// The group's share counts only what companies with an estimate paid.
		var wallet, walletPaid int64
		for rows.Next() {
			var c groupCompany
			var attrs map[string]any
			if err := rows.Scan(&c.ID, &c.Name, &c.ParentID, &c.Depth, &attrs, &c.MRR, &c.Paid12m, &c.OpenSignals, &c.People); err != nil {
				return err
			}
			if len(out.Companies) == groupMax {
				out.Truncated = true
				break
			}
			if scores, _ := attrs["scores"].(map[string]any); scores != nil {
				if v, ok := scores["health"].(float64); ok {
					n := int(v)
					c.Health = &n
				}
				if b, _ := scores["health_band"].(string); b != "" {
					c.HealthBand = &b
					if b == "low" {
						out.Totals.LowHealth++
					}
				}
			}
			if amount, code, ok := walletOf(attrs, out.Currency); ok && code == out.Currency {
				c.Wallet = &amount
				wallet += amount
				walletPaid += c.Paid12m
			}
			out.Totals.Companies++
			out.Totals.MRR += c.MRR
			out.Totals.Paid12m += c.Paid12m
			out.Totals.OpenSignals += c.OpenSignals
			out.Totals.People += c.People
			if c.ID == rec.ID {
				if amount, code, ok := walletOf(attrs, out.Currency); ok {
					wl := recordWallet{Estimate: amount, Currency: code, Confidence: textAttr(attrs, "wallet_confidence"), Source: textAttr(attrs, "wallet_source"),
						Suppliers: textAttr(attrs, "suppliers"), Paid12m: c.Paid12m}
					if code == out.Currency {
						share := float64(c.Paid12m) / float64(amount)
						wl.Share = &share
					}
					out.Wallet = &wl
				}
				if c.ParentID != nil {
					out.Parent = &summaryRef{ID: *c.ParentID}
				}
			}
			out.Companies = append(out.Companies, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if out.Parent != nil {
			for _, c := range out.Companies {
				if c.ID == out.Parent.ID {
					out.Parent.Name = c.Name
				}
			}
		}
		if wallet > 0 {
			share := float64(walletPaid) / float64(wallet)
			out.Totals.Wallet, out.Totals.Share = &wallet, &share
		}
		return nil
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such person or company in this brand")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// checkParent refuses a parent that is the company itself, one of the
// companies under it (so a group never loops) or a company of another
// brand.
func checkParent(ctx context.Context, tx pgx.Tx, ws, id string, parent any) error {
	p, _ := parent.(string)
	if p == "" {
		return nil
	}
	if p == id {
		return errInvalid("a company can't be its own parent")
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM records WHERE id = $2::uuid AND workspace_id = $1 AND record_type = 'account'
			AND brand_id IS NOT DISTINCT FROM (SELECT brand_id FROM records WHERE id = $3::uuid AND workspace_id = $1)
			AND merged_into IS NULL AND deleted_at IS NULL)`, ws, p, id).Scan(&same); err != nil {
		return err
	}
	if !same {
		return errInvalid("the parent must be a company of this brand")
	}
	var loops bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (
			SELECT $2::uuid AS id, 0 AS depth
			UNION ALL
			SELECT l.to_id, up.depth + 1 FROM up JOIN record_relationships l ON l.from_id = up.id AND l.field_key = 'parent_company' AND l.valid_to IS NULL AND l.workspace_id = $1
			WHERE up.depth < 20)
		SELECT EXISTS (SELECT 1 FROM up WHERE id = $3::uuid)`, ws, p, id).Scan(&loops)
	if err != nil {
		return err
	}
	if loops {
		return errInvalid("that company is already under this one, so it can't be its parent")
	}
	return nil
}
