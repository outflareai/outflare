package intercom

import (
	"context"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/intercom/intercomtest"
)

func TestIntercom(t *testing.T) {
	fake := intercomtest.New()
	defer fake.Close()
	cl := &c.Client{BaseURLs: map[string]string{"intercom": fake.URL}}
	creds := c.Creds{"api_key": intercomtest.Token}
	ctx := context.Background()

	acct, err := Intercom{}.Test(ctx, cl, creds)
	if err != nil || acct.ID != intercomtest.App {
		t.Fatalf("test: %+v %v", acct, err)
	}
	if _, err := (Intercom{}).Test(ctx, cl, c.Creds{"api_key": "wrong"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("a wrong token is an auth error: %v", err)
	}
	if _, err := (Intercom{}).Test(ctx, cl, c.Creds{"api_key": intercomtest.Token, "region": "mars"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("an unknown region is refused: %v", err)
	}

	agents, err := Intercom{}.SupportAgents(ctx, cl, creds)
	if err != nil || len(agents) != 3 || agents[2].Kind != "team" {
		t.Fatalf("agents: %+v %v", agents, err)
	}

	for i, id := range []string{"a", "b", "c"} {
		fake.Put(intercomtest.Conversation(id, 1_700_000_000+int64(i)*100))
	}
	refs, next, err := Intercom{}.SupportChanged(ctx, cl, creds, time.Unix(1_700_000_050, 0), "", 1)
	if err != nil || len(refs) != 1 || refs[0].ID != "b" || next == "" {
		t.Fatalf("first page: %+v %q %v", refs, next, err)
	}
	refs, next, err = Intercom{}.SupportChanged(ctx, cl, creds, time.Unix(1_700_000_050, 0), next, 1)
	if err != nil || len(refs) != 1 || refs[0].ID != "c" || next != "" {
		t.Fatalf("last page: %+v %q %v", refs, next, err)
	}

	cv, err := Intercom{}.SupportConversation(ctx, cl, creds, "a")
	if err != nil {
		t.Fatal(err)
	}
	if cv.Status != "closed" || cv.Channel != "chat" || cv.ExternalChannel != "conversation" || cv.Priority != "high" ||
		cv.ContactID != "c1" || cv.AssigneeID != "1001" || cv.TeamID != "7" || cv.Reopens != 1 || cv.ClosedAt == nil || cv.FirstReplyAt == nil {
		t.Fatalf("conversation: %+v", cv)
	}
	if cv.Rating == nil || cv.Rating.Score != 5 || cv.AI == nil || cv.AI.Resolution != "escalated" || cv.AI.Rating != 3 ||
		len(cv.AI.Sources) != 1 || cv.AI.Sources[0].Title != "Exporting reports" {
		t.Fatalf("rating and Fin: %+v %+v", cv.Rating, cv.AI)
	}
	// The question, Fin's answer, the note, the follow-up and the closing
	// reply; the assignment with nothing said is left out.
	want := []struct{ kind, author string }{{"in", "customer"}, {"out", "ai"}, {"note", "agent"}, {"in", "customer"}, {"out", "agent"}}
	if len(cv.Messages) != len(want) {
		t.Fatalf("messages: %+v", cv.Messages)
	}
	for i, w := range want {
		if m := cv.Messages[i]; m.Kind != w.kind || m.AuthorType != w.author || m.ID == "" || m.At.IsZero() {
			t.Errorf("message %d: %+v, want %s by %s", i, m, w.kind, w.author)
		}
	}
	if cv.Messages[0].AuthorName != "" || cv.Messages[0].AuthorID != "" {
		t.Error("a customer's message names no teammate")
	}

	ct, ok, err := Intercom{}.SupportContact(ctx, cl, creds, "c1")
	if err != nil || !ok || ct.Email != "ana@acme.example" {
		t.Fatalf("contact: %+v %v %v", ct, ok, err)
	}
	if _, ok, err := (Intercom{}).SupportContact(ctx, cl, creds, "gone"); err != nil || ok {
		t.Fatalf("a missing contact is not an error: %v %v", ok, err)
	}
}

func TestMessageAuthors(t *testing.T) {
	cases := []struct {
		a              author
		part           string
		kind, authorTy string
	}{
		{author{Type: "lead"}, "comment", "in", "customer"},
		{author{Type: "admin", ID: "1"}, "note_and_reopen", "note", "agent"},
		{author{Type: "bot", ID: "2", Name: "Routing workflow"}, "comment", "out", "system"},
		{author{Type: "bot", ID: "3", FromAIAgent: true}, "comment", "out", "ai"},
		{author{Type: "team", ID: "7"}, "comment", "out", "agent"},
	}
	for _, tc := range cases {
		if m := message(tc.a, tc.part, "hi", time.Now()); m.Kind != tc.kind || m.AuthorType != tc.authorTy {
			t.Errorf("%+v %s: got %s by %s", tc.a, tc.part, m.Kind, m.AuthorType)
		}
	}
}
