package journeys

import (
	"context"
	"errors"
	"strings"

	"github.com/outflareai/outflare/engine/internal/emails"
)

// The ai step asks the workspace's own AI model about the person: to sort
// them into one of a few choices (classify) or to write a short text, from
// a prompt that {{person.x}}, {{event.x}} and {{vars.x}} fill in. The
// answer is kept in vars.<save_as> as {answer} for later steps: a branch
// on vars.<save_as>.answer, an email template, a webhook body. It uses the
// workspace's own key and its AI spend limits (bring your own keys);
// with no AI model connected the step fails and says so. What the model
// writes is data for later steps, never instructions to Outflare.

// AI asks the workspace's own AI model one question. The API server
// implements it, so the model choice, spend limits and the AI usage log
// are the same as everywhere else.
type AI interface {
	Ask(ctx context.Context, ws string, q AIQuestion) (string, error)
}

// AIQuestion is one ai step's question, filled in for one person.
type AIQuestion struct {
	Prompt string
	// Choices, when set, are the only answers allowed (classify).
	Choices []string
	// MaxWords caps a written answer.
	MaxWords int
	// Ref names the run and step, for the AI usage log.
	Ref string
}

// AIError is why the workspace's model couldn't answer: Code is from the
// error catalog (ai_model_needed, ai_budget_reached, ai_failed), and Retry
// says another try could work (the provider was down or slow).
type AIError struct {
	Code, Message string
	Retry         bool
}

func (e *AIError) Error() string { return e.Message }

// Limits on an ai step.
const (
	maxPrompt   = 4000
	maxChoices  = 20
	maxWords    = 300
	maxAnswered = 4000 // characters of answer kept
)

func checkAI(st Step) string {
	switch {
	case strings.TrimSpace(st.Prompt) == "":
		return "an ai step needs a prompt"
	case len(st.Prompt) > maxPrompt:
		return "prompt is at most 4,000 characters"
	case st.SaveAs == "" || !saveRE.MatchString(st.SaveAs):
		return "save_as is where the answer is kept (vars.<save_as>): lowercase letters, digits and _"
	case len(st.Choices) > maxChoices:
		return "at most 20 choices"
	case st.MaxWords < 0 || st.MaxWords > maxWords:
		return "max_words is 1 to 300, or 0 for the default (100)"
	case len(st.Choices) > 0 && st.MaxWords != 0:
		return "max_words is for written answers; a step with choices answers with one of them"
	}
	if err := emails.CheckFill(st.Prompt); err != nil {
		return "prompt: " + err.Error()
	}
	seen := map[string]bool{}
	for _, c := range st.Choices {
		c = strings.TrimSpace(c)
		if c == "" || len(c) > 100 {
			return "choices are 1 to 100 characters"
		}
		if seen[c] {
			return "choices must differ: " + c + " is there twice"
		}
		seen[c] = true
	}
	if len(st.Choices) == 1 {
		return "give at least two choices, or none for a written answer"
	}
	return ""
}

func aiCall(ctx context.Context, c *call) callResult {
	if c.s.AI == nil {
		return failed("not_configured", "AI steps aren't available on this server")
	}
	prompt, err := emails.Fill(c.st.Prompt, c.data, nil)
	if err != nil {
		return failed("invalid_request", "prompt: "+err.Error())
	}
	words := c.st.MaxWords
	if words == 0 && len(c.st.Choices) == 0 {
		words = 100
	}
	answer, err := c.s.AI.Ask(ctx, c.ws, AIQuestion{Prompt: prompt, Choices: c.st.Choices, MaxWords: words, Ref: "journey_run:" + c.r.id + ":" + c.st.ID})
	var ae *AIError
	switch {
	case errors.As(err, &ae) && ae.Retry:
		return retry(ae.Code, ae.Message)
	case errors.As(err, &ae):
		return failed(ae.Code, ae.Message)
	case err != nil:
		return retry("internal", "the AI model could not be asked")
	}
	if len(answer) > maxAnswered {
		answer = answer[:maxAnswered]
	}
	detail := answer
	if len(detail) > 120 {
		detail = detail[:117] + "..."
	}
	return callResult{outcome: "done", detail: detail, saved: map[string]any{"answer": answer}}
}
