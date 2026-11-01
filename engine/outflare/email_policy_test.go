package outflare

import (
	"context"
	"testing"
)

func TestExtensionEmailPolicyRejectsUnknownPurposeBeforeQuery(t *testing.T) {
	if _, err := CheckEmailRecipients(context.Background(), nil, "workspace", "bypass", []string{"recipient@example.com"}); err == nil {
		t.Fatal("unknown purpose accepted")
	}
	if _, err := CheckEmailRecipients(context.Background(), nil, "", "transactional", nil); err == nil {
		t.Fatal("missing workspace accepted")
	}
}
