package outflare

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/alerts"
)

func TestAddAlertKind(t *testing.T) {
	describe := func(context.Context, AlertQuerier, AlertRow) (AlertView, error) {
		return AlertView{Title: "Usage", Path: "billing"}, nil
	}
	h := &Host{}
	k := AlertKind{ID: "ext_test.allowance", Title: "Allowance used", Describe: describe}
	h.AddAlertKind(k)
	h.AddAlertKind(k) // a second Setup in one process keeps the first
	if _, ok := alerts.Find(k.ID); !ok {
		t.Fatal("the kind isn't registered")
	}
	for _, bad := range []AlertKind{
		{ID: "allowance", Title: "No area", Describe: describe},
		{ID: "ext_test.no_title", Describe: describe},
		{ID: "ext_test.no_describe", Title: "No describe"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%q: a malformed kind was accepted", bad.ID)
				}
			}()
			h.AddAlertKind(bad)
		}()
	}
}
