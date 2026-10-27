package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/outflareai/outflare/engine/internal/llm"
)

// ErrRefused is returned when the model declines to plan the goal.
var ErrRefused = errors.New("the model declined to plan this goal")

// LLM is a Model backed by the workspace's chosen AI model.
type LLM struct{ M llm.Model }

// Complete asks the model for JSON matching schema.
func (l LLM) Complete(ctx context.Context, system, prompt string, schema map[string]any) (json.RawMessage, Usage, error) {
	res, err := l.M.Chat(ctx, llm.Request{
		System:     system,
		Messages:   []llm.Message{{Role: "user", Text: prompt}},
		JSONSchema: schema,
		MaxTokens:  16000,
	})
	usage := Usage{Model: res.Usage.Model, InputTokens: res.Usage.InputTokens + res.Usage.CacheRead + res.Usage.CacheWrite, OutputTokens: res.Usage.OutputTokens, LLM: res.Usage}
	if usage.Model == "" {
		usage.Model = l.M.ID()
	}
	if err != nil {
		return nil, usage, err
	}
	switch {
	case res.Stop == llm.StopRefusal:
		return nil, usage, ErrRefused
	case res.Stop == llm.StopMaxTokens:
		return nil, usage, fmt.Errorf("%w: the answer was cut off", ErrBadDraft)
	case res.Message.Text == "":
		return nil, usage, fmt.Errorf("%w: no text in the answer", ErrBadDraft)
	}
	return json.RawMessage(res.Message.Text), usage, nil
}
