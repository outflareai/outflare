package api

import "testing"

func TestCleanQuery(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"?utm_source=x&plan=pro&page=2", "page=2&plan=pro&utm_source=x"},
		// Anything that could sign someone in or name them.
		{"access_token=a&id_token=b&code=c&state=d&password=e&api_key=f&session_id=g&sig=h&X-Amz-Signature=i", ""},
		{"Email=ann%40acme.example&user_email=x&q=ann%40acme.example&plan=pro", "plan=pro"},
		{"t=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln&plan=pro", "plan=pro"},
		// Outflare's own.
		{"_ofa=a.1&ofvisit=v&ref=PARTNER1", "ref=PARTNER1"},
		// What the site leaves out: by name in any case, or by prefix.
		{"exp_hero=b&EXP_cta=a&variant=2&plan=pro", "plan=pro"},
		{"%zz", ""},
	} {
		if got := cleanQuery(c.in, []string{"exp_*", "Variant"}); got != c.want {
			t.Errorf("cleanQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanExcludedParams(t *testing.T) {
	if got, err := cleanExcludedParams([]string{" plan ", "fb_*", "plan", "", "items[]"}); err != nil || len(got) != 3 {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"a b", "*fb", "a=b", string(make([]byte, 101))} {
		if _, err := cleanExcludedParams([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
