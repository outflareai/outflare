package emails

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

func TestTimelineState(t *testing.T) {
	for _, c := range []struct {
		ev   connectors.ProviderEvent
		want string
	}{
		{connectors.ProviderEvent{Type: connectors.EventDelivered}, "delivered"},
		{connectors.ProviderEvent{Type: connectors.EventBounced, BounceType: "hard"}, "bounced"},
		{connectors.ProviderEvent{Type: connectors.EventBounced}, "bounced"},
		{connectors.ProviderEvent{Type: connectors.EventBounced, BounceType: "soft"}, ""},
		{connectors.ProviderEvent{Type: connectors.EventComplaint}, "complained"},
		{connectors.ProviderEvent{Type: connectors.EventFailed}, "rejected"},
		{connectors.ProviderEvent{Type: connectors.EventOpened}, ""},
	} {
		if got := timelineState(c.ev); got != c.want {
			t.Errorf("%+v = %q, want %q", c.ev, got, c.want)
		}
	}
}
