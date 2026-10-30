package api_test

import (
	"context"
	"testing"
)

// TestPartnerImportInvited: Tolt's invited and suspended partners come over
// (invited as pending), with the customers they referred. When the list
// refuses the invited filter, the rest still come over, and an invited
// partner a customer names is read by id.
func TestPartnerImportInvited(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		tolt := &fakeTolt{data: map[string][]map[string]any{}}
		if refuse {
			tolt.refuseStatus = "invited"
		}
		tolt.set("/programs", map[string]any{"id": "prg_1", "name": "Northpeak partners", "currency": "usd", "status": "active"})
		tolt.set("/partners",
			map[string]any{"id": "p1", "first_name": "Ana", "email": "ana@acme.example", "status": "active"},
			map[string]any{"id": "p2", "first_name": "Ivy", "email": "ivy@acme.example", "status": "invited"},
			map[string]any{"id": "p3", "first_name": "Sam", "email": "sam@acme.example", "status": "suspended"})
		tolt.set("/links")
		tolt.set("/promotion-codes")
		tolt.set("/customers",
			map[string]any{"id": "cus_1", "partner_id": "p2", "customer_id": "u-1"},
			map[string]any{"id": "cus_2", "partner_id": "p3", "customer_id": "u-2"})
		tolt.set("/transactions")
		tolt.set("/commissions")
		e, _, _, v := runToltImport(t, tolt)

		got := map[string]string{}
		rows, err := e.pool.Query(context.Background(), `SELECT i.value, m.status FROM partner_memberships m
			JOIN record_identities i ON i.record_id = m.record_id AND i.namespace = 'email'`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var email, st string
			rows.Scan(&email, &st) //nolint:errcheck
			got[email] = st
		}
		var referrals int
		e.pool.QueryRow(context.Background(), `SELECT count(*) FROM partner_referrals`).Scan(&referrals) //nolint:errcheck
		if got["ana@acme.example"] != "active" || got["ivy@acme.example"] != "pending" || got["sam@acme.example"] != "suspended" || referrals != 2 {
			t.Fatalf("refuse=%v: partners = %v, referrals = %d, summary = %v", refuse, got, referrals, v["summary"])
		}
	}
}
