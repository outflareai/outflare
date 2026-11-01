package api

import "testing"

func TestPartnerOwnershipAndEvidence(t *testing.T) {
	if partnerDNSProof([]string{"token", "outflare-partner=wrong"}, "secret") || !partnerDNSProof([]string{"outflare-partner=secret"}, "secret") {
		t.Fatal("DNS ownership proof must match exactly")
	}
	for _, raw := range []string{"http://example.com", "https://127.0.0.1", "https://localhost", "https://user:password@example.com", "https://example.com:443", "https://service.internal"} {
		if _, _, err := partnerAudienceURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, page := range []string{`<script>https://brand.example/product</script>`, `<p>https://brand.example/product</p>`, `<a href="https://brand.example/product-evil">no</a>`, `<a href="https://brand.example.evil/product">no</a>`} {
		if partnerHasLink(page, "https://publisher.example/post", "https://brand.example/product") {
			t.Errorf("false link evidence: %s", page)
		}
	}
	if !partnerHasLink(`<a href="https://brand.example/product">Product</a>`, "https://publisher.example/post", "https://brand.example/product") {
		t.Fatal("real link missed")
	}
	for _, c := range []struct {
		html string
		want int64
		ok   bool
	}{{`<p>12.5K subscribers</p>`, 12500, true}, {`<script>10000 followers</script>`, 0, false}, {`<p>1K followers</p><p>5K members</p>`, 0, false}, {`<p>100 members</p>`, 100, true}} {
		n, ok := partnerPageReach(c.html)
		if n != c.want || ok != c.ok {
			t.Errorf("reach %s=%d,%v", c.html, n, ok)
		}
	}
}
