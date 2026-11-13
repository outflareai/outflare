package linkorders

import "testing"

func TestMinorToUSDCents(t *testing.T) {
	for _, c := range []struct {
		currency string
		minor    int64
		rate     float64
		want     int64
	}{
		{"GBP", 50000, 1.2708, 63540},     // £500.00
		{"EUR", 15000, 1.0842, 16263},     // €150.00 -> $162.63
		{"JPY", 75000, 0.006512, 48840},   // ¥75,000 has no minor unit -> $488.40
		{"HUF", 120000, 0.002765, 33180},  // 120,000 Ft -> $331.80
		{"PLN", 199999, 0.25, 50000},      // half a cent rounds away from zero
		{"UAH", 4150000, 0.024096, 99998}, // ₴41,500.00
	} {
		if got := minorToUSDCents(c.currency, c.minor, c.rate); got != c.want {
			t.Errorf("%s %d at %v: got %d, want %d", c.currency, c.minor, c.rate, got, c.want)
		}
	}
}

func TestInvoiceCurrencies(t *testing.T) {
	for _, c := range []string{"USD", "EUR", "GBP", "PLN", "CZK", "UAH", "CHF", "CAD", "AUD", "NZD", "JPY", "SEK", "NOK", "DKK", "RON", "HUF", "BGN", "TRY", "INR", "SGD", "HKD", "MXN", "BRL", "ZAR"} {
		if err := validInvoiceCurrency(c); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
	for _, c := range []string{"XYZ", "BTC", "gbp", ""} {
		if err := validInvoiceCurrency(c); err == nil {
			t.Errorf("%q accepted", c)
		}
	}
}
