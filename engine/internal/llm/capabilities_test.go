package llm

import (
	"slices"
	"testing"
)

func TestCapabilities(t *testing.T) {
	c := CapabilitiesOf(Claude.ID, "claude-test")
	if !c.Tools || !c.Images || !c.Streaming || !c.WebSearch || !c.Cache {
		t.Fatalf("claude: %+v", c)
	}
	d := CapabilitiesOf(DeepSeek.ID, "deepseek-chat")
	if d.Images || d.WebSearch || !d.Tools {
		t.Fatalf("deepseek: %+v", d)
	}
	if got := d.Missing(Capabilities{Tools: true, Images: true}); !slices.Equal(got, []string{"images"}) {
		t.Fatalf("missing: %v", got)
	}
	if got := c.Missing(Capabilities{Tools: true, Images: true, Streaming: true}); len(got) != 0 {
		t.Fatalf("claude missing: %v", got)
	}
	for _, p := range Providers {
		if !CapabilitiesOf(p.ID, "m").Tools {
			t.Errorf("%s: every provider calls tools", p.ID)
		}
	}
}
