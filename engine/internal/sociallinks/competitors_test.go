package sociallinks

import "testing"

func TestSuggest(t *testing.T) {
	list := []CompetitorLink{
		{Kind: "x", URL: "https://x.com/rivalhq", State: Dismissed, Source: FromHomePage},
	}
	found := []Link{
		{Kind: "x", URL: "https://x.com/rivalhq"},
		{Kind: "youtube", URL: "https://www.youtube.com/@rivaltv"},
		{Kind: "trustpilot", URL: "https://www.trustpilot.com/review/rival.example"},
		// A LinkedIn person has no public counts to read.
		{Kind: "linkedin", URL: "https://www.linkedin.com/in/someone"},
		{Kind: "youtube", URL: "https://www.youtube.com/@rivaltv"},
	}
	got, added := Suggest(list, found)
	if added != 2 || len(got) != 3 || got[0].State != Dismissed || got[1].State != Suggested || got[2].Kind != "trustpilot" || got[2].Source != FromHomePage {
		t.Fatalf("suggest: %d %+v", added, got)
	}
	if again, n := Suggest(got, found); n != 0 || len(again) != 3 {
		t.Fatalf("again: %d %+v", n, again)
	}
}
