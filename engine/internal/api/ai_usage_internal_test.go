package api

import (
	"strings"
	"testing"
)

func TestAIBudgetMessageSaysWhetherCallsStop(t *testing.T) {
	for _, c := range []struct {
		level    int
		hardStop bool
		want     string
	}{
		{100, true, "makes no more of these AI calls"},
		{100, false, "keeps making these AI calls"},
		{80, true, "stops these AI calls"},
		{80, false, "doesn't stop these AI calls"},
	} {
		m := aiBudgetMessage("https://app.example.com/acme/ai-usage", "ana@acme.example", "Acme", "", c.level, 5000, 5000, c.hardStop)
		if !strings.Contains(m.Text, c.want) {
			t.Errorf("level %d, hard stop %v: %q lacks %q", c.level, c.hardStop, m.Text, c.want)
		}
	}
}
