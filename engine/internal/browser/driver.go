package browser

import (
	"context"
	"fmt"
	"strings"
)

// Element is something on the page the model may act on, by its ref.
type Element struct {
	Ref   int    `json:"ref"`
	Role  string `json:"role"` // link, button, textbox, checkbox, select, ...
	Label string `json:"label"`
	Value string `json:"value,omitempty"`
}

// Page is what the model sees of the current page: its address, title,
// visible text and the elements it may act on. Never a screenshot (an
// image could carry a code or a secret the text scrubber can't see).
type Page struct {
	URL      string
	Title    string
	Text     string
	Elements []Element
}

// Render is the page as the model reads it, cut to max characters.
func (p Page) Render(max int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\nTitle: %s\n\n", p.URL, p.Title)
	text := p.Text
	if max > 0 && len(text) > max {
		text = text[:max] + "\n… (cut)"
	}
	b.WriteString(text)
	b.WriteString("\n\nElements:\n")
	for _, e := range p.Elements {
		fmt.Fprintf(&b, "[%d] %s %q", e.Ref, e.Role, e.Label)
		if e.Value != "" {
			fmt.Fprintf(&b, " value=%q", e.Value)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Driver is one browser session for one task. The Chrome driver enforces
// the task's Boundary on every request; a test driver is scripted.
type Driver interface {
	// Navigate opens a URL and waits for the page to load.
	Navigate(ctx context.Context, url string) error
	// Page reads the current page.
	Page(ctx context.Context) (Page, error)
	// Click clicks an element by its ref from the last Page.
	Click(ctx context.Context, ref int) error
	// Type puts text into an element by its ref, replacing what was there,
	// and presses Enter when submit is set.
	Type(ctx context.Context, ref int, text string, submit bool) error
	// Close ends the session and forgets everything it held.
	Close() error
}

// Opener starts a browser session inside a boundary.
type Opener interface {
	Open(ctx context.Context, b Boundary) (Driver, error)
}
