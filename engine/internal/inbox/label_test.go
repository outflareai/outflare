package inbox

import (
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	v, err := ParseVerdict([]byte(`{"category":"referral","confidence":1.4,"reason":"points to the CFO",
		"details":{"referral_name":"Sam","referral_email":"sam@acme.test","colour":"red","meeting_time":"  "}}`))
	if err != nil || v.Category != "referral" || v.Confidence != 1 || len(v.Details) != 2 || v.Details["referral_email"] != "sam@acme.test" {
		t.Fatalf("verdict %+v %v", v, err)
	}
	for _, bad := range []string{`{"category":"maybe","confidence":0.9}`, `not json`} {
		if _, err := ParseVerdict([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestLabelPromptKeepsTheReplyAsData(t *testing.T) {
	p := labelPrompt("Re: hi", "Sure, let's talk.\n\nOn Mon, Ana wrote:\n> our pitch")
	if want := "<reply>\nSure, let's talk.\n</reply>"; !strings.Contains(p, want) || strings.Contains(p, "our pitch") {
		t.Fatalf("prompt %q", p)
	}
}
