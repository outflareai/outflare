package connectors

import "testing"

func TestBlocked(t *testing.T) {
	for _, page := range []string{
		"<html><title>Just a moment...</title></html>",
		`<html><script src="https://ct.captcha-delivery.com/c.js"></script></html>`,
		"<html><body>Verify you are human</body></html>",
	} {
		if !(FetchedPage{Status: 200, HTML: page}).Blocked() {
			t.Errorf("not seen as a bot check: %s", page)
		}
	}
	if !(FetchedPage{Status: 403, HTML: "<html></html>"}).Blocked() || !(FetchedPage{Status: 429}).Blocked() {
		t.Error("a refused status is a block")
	}
	if (FetchedPage{Status: 200, HTML: "<html><title>Northpeak reviews</title></html>"}).Blocked() || (FetchedPage{Status: 404}).Blocked() {
		t.Error("an ordinary page or a missing one is not a block")
	}
}
