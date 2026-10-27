package sending

import (
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestRender(t *testing.T) {
	vars := map[string]string{"first_name": "Ana", "company": "Acme", "icp_tier": "A", "title": " "}
	out, missing := Render("Hi {{ first_name }}, {{Company}} is tier {{custom.icp_tier}}. {{title|Leader}} {{city|}}{{phone}}{{phone}}", vars)
	if out != "Hi Ana, Acme is tier A. Leader " || len(missing) != 1 || missing[0] != "phone" {
		t.Fatalf("%q %v", out, missing)
	}
}

func TestEmailBytes(t *testing.T) {
	e := Email{
		FromName: "Jack Fitz", From: "jack@acme-io.example", To: "ana@beta-io.example", Subject: "Quick question, Ana — café?",
		Body: "Hi Ana,\nLine two with a long line " + strings.Repeat("x", 100) + "\n", MessageID: "abc@acme-io.example",
		InReplyTo: "prev@acme-io.example", References: "<prev@acme-io.example>", UnsubscribeURL: "https://outflare.example.com/u/t", Date: time.Now(),
	}
	m, err := mail.ReadMessage(strings.NewReader(string(e.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if subj != e.Subject || m.Header.Get("Message-ID") != "<abc@acme-io.example>" || m.Header.Get("In-Reply-To") != "<prev@acme-io.example>" ||
		m.Header.Get("List-Unsubscribe-Post") != "List-Unsubscribe=One-Click" {
		t.Fatalf("headers: %v (subject %q)", m.Header, subj)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(m.Body))
	if string(body) != strings.ReplaceAll(e.Body, "\n", "\r\n") {
		t.Fatalf("body: %q", body)
	}
}
