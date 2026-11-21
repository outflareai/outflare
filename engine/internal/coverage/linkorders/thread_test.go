package linkorders

import (
	"strings"
	"testing"
)

func TestAmountsIn(t *testing.T) {
	for in, want := range map[string]string{
		"Price is $150 for a guest post, or $ 90 for an insert": "$150|$ 90",
		"We can do 120 EUR, final.":                             "120 EUR",
		"€1.200,50 per post":                                    "€1.200,50",
		"Total: 1,500 USD":                                      "1,500 USD",
		"I have 3 sites and 2026 plans":                         "",
	} {
		if got := strings.Join(AmountsIn(in), "|"); got != want {
			t.Errorf("AmountsIn(%q) = %q, want %q", in, got, want)
		}
	}
}
