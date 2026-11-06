package pricing

import "testing"

func TestExtractCards(t *testing.T) {
	page := `<html><head><style>.x{}</style></head><body>
	<nav><h3>Menu</h3><a>$1 off</a></nav>
	<h1>Pricing</h1><p>Simple plans for every team.</p>
	<div class="card"><h3>Starter</h3><div><span>$</span><span>19</span><span>/mo</span></div><ul><li>5 seats</li></ul></div>
	<div class="card"><h3>Pro</h3><div>US$ 1,299.50 per year</div></div>
	<div class="card"><h3>Business</h3><div>49,90 € a month</div></div>
	<div class="card"><h3>Enterprise</h3><div>Contact sales</div></div>
	<div class="card" style="display: none"><h3>Hidden</h3><div>$5/mo</div></div>
	<h3>Starter</h3><div>$15/mo billed yearly</div>
	<footer><h4>Company</h4><p>$0 setup</p></footer>
	</body></html>`
	got := Extract(page)
	want := []Plan{
		{Name: "Starter", Price: 19, Currency: "USD", Period: "month", Text: "$19/mo"},
		{Name: "Pro", Price: 1299.5, Currency: "USD", Period: "year", Text: "US$ 1,299.50 per year"},
		{Name: "Business", Price: 49.9, Currency: "EUR", Period: "month", Text: "49,90 € a month"},
	}
	if len(got) != len(want) {
		t.Fatalf("plans: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("plan %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestExtractTableAndStructured(t *testing.T) {
	table := `<table><tr><th>Plan</th><th>Price</th></tr>
		<tr><td>Residential 10 GB</td><td>$3.50 / GB</td></tr>
		<tr><td>Residential 100 GB</td><td>$2.75 / GB</td></tr></table>`
	got := Extract(table)
	if len(got) != 2 || got[0].Name != "Residential 10 GB" || got[0].Price != 3.5 || got[0].Period != "gb" || got[1].Price != 2.75 {
		t.Fatalf("table: %+v", got)
	}
	ld := `<script type="application/ld+json">{"@context":"https://schema.org","@type":"Product","name":"Northpeak",
		"offers":[{"@type":"Offer","name":"Basic","price":"9.00","priceCurrency":"usd"},
		{"@type":"Offer","name":"Team","priceSpecification":{"price":29,"unitText":"MONTH"},"priceCurrency":"USD"}]}</script>
		<h3>Ignored</h3><p>$1</p>`
	got = Extract(ld)
	if len(got) != 2 || got[0] != (Plan{Name: "Basic", Price: 9, Currency: "USD", Text: "9 USD"}) || got[1].Period != "month" || got[1].Price != 29 {
		t.Fatalf("structured: %+v", got)
	}
	if got := Extract(`<html><body><h2>About us</h2><p>We love proxies.</p></body></html>`); got != nil {
		t.Fatalf("no prices: %+v", got)
	}
}

func TestDiff(t *testing.T) {
	before := []Plan{{Name: "Starter", Price: 19, Currency: "USD", Period: "month"}, {Name: "Pro", Price: 49, Currency: "USD", Period: "month"}, {Name: "Old", Price: 5, Currency: "USD"}}
	after := []Plan{{Name: "starter", Price: 25, Currency: "USD", Period: "month"}, {Name: "Pro", Price: 39, Currency: "USD", Period: "month"}, {Name: "Team", Price: 99, Currency: "USD", Period: "month"}}
	kinds := map[string]string{}
	for _, c := range Diff(before, after) {
		kinds[c.Plan] = c.Kind
	}
	if kinds["starter"] != "price_up" || kinds["Pro"] != "price_down" || kinds["Team"] != "added" || kinds["Old"] != "removed" || len(kinds) != 4 {
		t.Fatalf("changes: %v", kinds)
	}
	if !Same(before, before) || Same(before, after) {
		t.Fatal("same")
	}
	if c := Diff(nil, before); len(c) != 3 || c[0].Kind != "added" {
		t.Fatalf("first read: %+v", c)
	}
}
