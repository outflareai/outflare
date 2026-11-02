package api

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGmailTextWithoutAttachments(t *testing.T) {
	var p gmailPart
	p.Mime = "multipart/mixed"
	p.Headers = []gmailHeader{{Name: "From", Value: "ana@customer.example"}, {Name: "To", Value: "employee@northpeak.example"}}
	plain := gmailPart{Mime: "text/plain"}
	plain.Body.Data = base64.RawURLEncoding.EncodeToString([]byte("Customer message"))
	attachment := gmailPart{Mime: "application/pdf", Filename: "contract.pdf"}
	attachment.Body.Attachment = "do-not-fetch"
	p.Parts = []gmailPart{plain, attachment}
	raw, err := gmailTextMessage(p)
	if err != nil || !strings.Contains(raw, "Customer message") || strings.Contains(raw, "do-not-fetch") {
		t.Fatal(raw, err)
	}
	for _, label := range []string{"DRAFT", "SCHEDULED", "SPAM", "TRASH"} {
		if !privateGoogleLabels([]string{label}) {
			t.Error("private label accepted", label)
		}
	}
}
