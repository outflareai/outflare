package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// People setup: how a brand's People, Companies and Accounts fit the way it
// sells. Three kinds of record are kept apart: a person (one human), a
// customer account (their workspace in the brand's product, record type
// customer_account) and a company. Who the brand sells to decides which
// lists the CRM shows and how a sign-up is filed; every brand starts with
// the defaults for its business model and can change each one.

// sellsTo values: consumers (a customer is a person; Companies hidden),
// companies (a customer is a company; work email suggests it), mixed (both;
// people with no company show as Individual, free-mail sign-ups stay
// Individual), marketplace (people with a Buyer or seller label).
var sellsToValues = []string{"consumers", "companies", "mixed", "marketplace"}

type peopleSetup struct {
	SellsTo string `json:"sells_to"`
	// Accounts: the brand's product has accounts (workspaces, teams) that
	// people are members of. Group calls make customer accounts, not
	// companies, and the CRM lists Accounts.
	Accounts bool `json:"accounts"`
	// Which of the two are the business model's defaults (nothing stored).
	SellsToDefault  bool `json:"sells_to_default"`
	AccountsDefault bool `json:"accounts_default"`
	// The business model the defaults came from, or null.
	BusinessModel *string `json:"business_model"`
	// What the CRM shows for these settings.
	Lists []string `json:"lists"`
}

// peopleDefaults is who a business model sells to and whether its product
// has accounts.
func peopleDefaults(model string) (sellsTo string, accounts bool) {
	switch model {
	case "b2b_saas_sales_led":
		return "companies", true
	case "b2b_saas_self_serve", "developer_tools":
		return "mixed", true
	case "wholesale", "agency", "consulting", "legal_accounting", "recruiting", "government", "lead_gen_affiliate":
		return "companies", false
	case "marketplace":
		return "marketplace", false
	case "other", "":
		return "mixed", false
	}
	return "consumers", false
}

// loadPeopleSetup is a brand's people setup, defaults filled in.
func loadPeopleSetup(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID, brandID string) (peopleSetup, error) {
	var model string
	var raw map[string]any
	if err := q.QueryRow(ctx, `SELECT business_model, people_setup FROM brands WHERE id = $1 AND workspace_id = $2`, brandID, workspaceID).
		Scan(&model, &raw); err != nil {
		return peopleSetup{}, err
	}
	sells, accounts := peopleDefaults(model)
	out := peopleSetup{SellsTo: sells, Accounts: accounts, SellsToDefault: true, AccountsDefault: true}
	if model != "" {
		out.BusinessModel = &model
	}
	if v, ok := raw["sells_to"].(string); ok && slices.Contains(sellsToValues, v) {
		out.SellsTo, out.SellsToDefault = v, false
	}
	if v, ok := raw["accounts"].(bool); ok {
		out.Accounts, out.AccountsDefault = v, false
	}
	out.Lists = []string{"people"}
	if out.SellsTo != "consumers" {
		out.Lists = append(out.Lists, "companies")
	}
	if out.Accounts {
		out.Lists = append(out.Lists, "accounts")
	}
	out.Lists = append(out.Lists, "deals")
	return out, nil
}

func (s *Server) routesPeopleSetup(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/people-setup", s.onBrand(auth.ScopeCustomersRead, s.getPeopleSetup))
	mux.HandleFunc("PATCH /v1/brands/{brand}/people-setup", s.onBrand(auth.ScopeCatalogManage, s.updatePeopleSetup))
}

func (s *Server) getPeopleSetup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	out, err := loadPeopleSetup(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "brand not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updatePeopleSetup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	// null takes a setting back to the business model's default.
	var req map[string]json.RawMessage
	if !decode(w, r, &req) {
		return
	}
	set := map[string]any{}
	clear := []string{}
	for k, v := range req {
		switch k {
		case "sells_to":
			var x *string
			if json.Unmarshal(v, &x) != nil || (x != nil && !slices.Contains(sellsToValues, *x)) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "sells_to is consumers, companies, mixed or marketplace, or null for the default")
				return
			}
			if x == nil {
				clear = append(clear, k)
			} else {
				set[k] = *x
			}
		case "accounts":
			var x *bool
			if json.Unmarshal(v, &x) != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "accounts is true, false, or null for the default")
				return
			}
			if x == nil {
				clear = append(clear, k)
			} else {
				set[k] = *x
			}
		default:
			writeError(w, r, http.StatusBadRequest, "invalid_request", "only sells_to and accounts can be set")
			return
		}
	}
	ctx := r.Context()
	var out peopleSetup
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := loadPeopleSetup(ctx, tx, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brands SET people_setup = (people_setup - $3::text[]) || $4::jsonb, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, a.Brand, a.WorkspaceID, clear, set); err != nil {
			return err
		}
		if out, err = loadPeopleSetup(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		if before.SellsTo == out.SellsTo && before.Accounts == out.Accounts && before.SellsToDefault == out.SellsToDefault && before.AccountsDefault == out.AccountsDefault {
			return nil
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "people_setup.updated", "brand", a.Brand, map[string]any{
			"sells_to": out.SellsTo, "accounts": out.Accounts, "previous_sells_to": before.SellsTo, "previous_accounts": before.Accounts,
			"sells_to_default": out.SellsToDefault, "accounts_default": out.AccountsDefault,
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "brand not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
