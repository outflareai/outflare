package cookies

import (
	"strings"
	"testing"
	"time"
)

func TestPolicyNamesItsOwner(t *testing.T) {
	v := Version{Policy: Policy{Site: "northpeak.example"}, Version: 1, Published: time.Unix(0, 0), Owner: "Northpeak Example Ltd, 1 Example Way, Leeds LS1 1AA, United Kingdom"}
	want := "This site is run by Northpeak Example Ltd, 1 Example Way, Leeds LS1 1AA, United Kingdom."
	for name, out := range map[string]string{"markdown": v.Markdown(), "text": v.Text(), "html": v.HTML()} {
		if !strings.Contains(out, want) {
			t.Errorf("%s has no owner:\n%s", name, out)
		}
	}
	v.Owner = ""
	if strings.Contains(v.Markdown(), "run by") {
		t.Error("no owner, no sentence")
	}
}
