package outflare

import (
	"errors"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/secrets"
)

func TestSealSecretIsBoundToItsPurpose(t *testing.T) {
	h := &Host{}
	if _, err := h.SealSecret("example-purpose", []byte("x")); err == nil {
		t.Fatal("sealed before the engine started")
	}
	box, err := secrets.NewBox("ZGV2LW9ubHktZGV2LW9ubHktZGV2LW9ubHktZGV2LTA=")
	if err != nil {
		t.Fatal(err)
	}
	h.box = box
	sealed, err := h.SealSecret("example-purpose", []byte("sk-test-not-a-real-key"))
	if err != nil || strings.Contains(string(sealed), "not-a-real") {
		t.Fatalf("sealed %q, %v", sealed, err)
	}
	if got, err := h.OpenSecret("example-purpose", sealed); err != nil || string(got) != "sk-test-not-a-real-key" {
		t.Fatalf("opened %q, %v", got, err)
	}
	if _, err := h.OpenSecret("something-else", sealed); err == nil {
		t.Fatal("opened under another purpose")
	}
	if _, err := h.SealSecret("", []byte("x")); err == nil {
		t.Fatal("sealed without a purpose")
	}
}

func TestOpenOperatorModel(t *testing.T) {
	if _, err := OpenOperatorModel("nobody", "sk-test-not-a-real-key", ""); err == nil {
		t.Fatal("opened an unknown provider")
	}
	if _, err := OpenOperatorModel(llm.Claude.ID, "", ""); !errors.Is(err, llm.ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
	if _, err := OpenOperatorModel(llm.Claude.ID, "sk-test-not-a-real-key", "claude-unpriced-0"); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("unpriced model: %v", err)
	}
	m, err := OpenOperatorModel(llm.Claude.ID, "sk-test-not-a-real-key", "claude-haiku-5-5")
	if err != nil || m.Model() != "claude-haiku-5-5" {
		t.Fatalf("haiku: %v", err)
	}
}
