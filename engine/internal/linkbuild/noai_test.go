package linkbuild

import (
	"errors"
	"fmt"
	"testing"

	"github.com/outflareai/outflare/engine/internal/llm"
)

type codeErr string

func (e codeErr) Error() string { return string(e) }
func (e codeErr) Code() string  { return string(e) }

func TestNoAI(t *testing.T) {
	for err, want := range map[error]bool{
		llm.ErrNoKey:                        true,
		fmt.Errorf("open: %w", llm.ErrAuth): true,
		codeErr("connector_needed"):         true,
		codeErr("ai_model_needed"):          true,
		codeErr("ai_budget_reached"):        false,
		errors.New("provider is down"):      false,
	} {
		if got := noAI(err); got != want {
			t.Errorf("noAI(%v) = %v, want %v", err, got, want)
		}
	}
}
