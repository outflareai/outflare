package outflare

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/research"
)

type costModel struct{ usage llm.Usage }

func (m costModel) Chat(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: "assistant", Text: "ok"}, Usage: m.usage}, nil
}
func (costModel) Provider() llm.Provider { return llm.OpenRouter }
func (costModel) ID() string             { return "vendor/some-model" }

type costBook struct{ booked []llm.Usage }

func (b *costBook) ForTask(context.Context, string, string, int64) (research.Choice, error) {
	return research.Choice{}, nil
}
func (b *costBook) Spent(_ context.Context, _ string, _ research.Choice, u llm.Usage, _, _ string) error {
	b.booked = append(b.booked, u)
	return nil
}

// What an extension's AI call returns as its cost is what the provider
// bills: a router's reported cost (its models have no price of Outflare's,
// so pricing the tokens said 0), and cached input at its own rate.
func TestChatReturnsWhatTheCallCost(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		price llm.Price
		usage llm.Usage
		want  int64
	}{
		{"router reports the cost", llm.Price{}, llm.Usage{InputTokens: 10000, OutputTokens: 700, CostUSD: 0.0563}, 6},
		{"list price", llm.Price{Input: 400, Output: 2000}, llm.Usage{InputTokens: 10000, OutputTokens: 1000}, 6},
		{"cached input is cheaper", llm.Price{Input: 400, Output: 2000, CacheRead: 20}, llm.Usage{InputTokens: 4000, CacheRead: 6000, OutputTokens: 1000}, 4},
	}
	for _, c := range cases {
		book := &costBook{}
		_, cents, err := chatOn(ctx, book, "ws", "lead_finder", research.Choice{Model: costModel{c.usage}, Price: c.price}, AIRequest{}, "ref")
		if err != nil {
			t.Fatal(err)
		}
		if cents != c.want {
			t.Errorf("%s: cents = %d, want %d", c.name, cents, c.want)
		}
		if len(book.booked) != 1 {
			t.Errorf("%s: booked %d times, want once", c.name, len(book.booked))
		}
	}
}
