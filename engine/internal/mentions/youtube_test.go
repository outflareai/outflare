package mentions

import (
	"strings"
	"testing"
)

func TestVideoID(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.youtube.com/watch?v=abcDEF12345&t=90s": "abcDEF12345",
		"https://m.youtube.com/watch?v=abcDEF12345":         "abcDEF12345",
		"https://youtu.be/abcDEF12345?si=x":                 "abcDEF12345",
		"https://www.youtube.com/shorts/abcDEF12345":        "abcDEF12345",
		"https://www.youtube.com/live/abcDEF12345":          "abcDEF12345",
		"https://www.youtube.com/@northpeak":                "",
		"https://www.youtube.com/watch?v=short":             "",
		"https://video.example.com/watch?v=abcDEF12345":     "",
		"not a url": "",
	} {
		if got := VideoID(in); got != want {
			t.Errorf("VideoID(%q) = %q, want %q", in, got, want)
		}
	}
}

const watchPage = `<html><script>var ytInitialPlayerResponse = {"videoDetails":{"videoId":"abcDEF12345","title":"Proxy tools compared","author":"Ana Builds","channelId":"UCexample000","viewCount":"48210","lengthSeconds":"754"},
"microformat":{"playerMicroformatRenderer":{"publishDate":"2026-11-02T08:00:00-07:00","ownerProfileUrl":"http://www.youtube.com/@anabuilds"}},
"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[
{"baseUrl":"https://www.youtube.com/api/timedtext?v=abcDEF12345&lang=de&kind=asr","languageCode":"de","kind":"asr"},
{"baseUrl":"https://www.youtube.com/api/timedtext?v=abcDEF12345&lang=en&kind=asr","languageCode":"en","kind":"asr"},
{"baseUrl":"https://evil.example/api/timedtext","languageCode":"en"}]}}};var meta = {};</script></html>`

func TestParseWatch(t *testing.T) {
	v, ok := ParseWatch(watchPage)
	if !ok {
		t.Fatal("no player data")
	}
	if v.ID != "abcDEF12345" || v.Title != "Proxy tools compared" || v.Channel != "Ana Builds" || v.ChannelURL != "https://www.youtube.com/@anabuilds" {
		t.Fatalf("video: %+v", v)
	}
	if v.Views == nil || *v.Views != 48210 || v.Seconds != 754 || v.Published == nil || v.Published.UTC().Hour() != 15 {
		t.Fatalf("numbers: %+v", v)
	}
	// English automatic captions win over German ones; a track off YouTube is never read.
	if !strings.Contains(v.CaptionsURL, "lang=en") || v.CaptionsLang != "en" {
		t.Fatalf("captions: %q", v.CaptionsURL)
	}
	if !strings.Contains(CaptionsJSONURL(v.CaptionsURL), "fmt=json3") {
		t.Fatal("json form")
	}
	if _, ok := ParseWatch("<html>Before you continue to YouTube</html>"); ok {
		t.Fatal("consent page read as a video")
	}
}

func TestFindSpoken(t *testing.T) {
	lines := ParseCaptions(`{"events":[{"tStartMs":0,"segs":[{"utf8":"welcome back"}]},{"tStartMs":61500,"segs":[{"utf8":"today we try "},{"utf8":"Acme Proxy"}]},
		{"tStartMs":64000},{"tStartMs":125000,"segs":[{"utf8":"and northpeak\nis cheaper"}]}]}`)
	if len(lines) != 3 || lines[1].Text != "today we try Acme Proxy" || lines[2].Start != 125 {
		t.Fatalf("lines: %+v", lines)
	}
	subjects := []Subject{
		{About: "brand", Name: "Northpeak", Terms: []string{"Northpeak"}},
		{About: "competitor", CompetitorID: "c1", Name: "Acme", Terms: []string{"Acme Proxy", "Acme"}},
		{About: "competitor", CompetitorID: "c2", Name: "Other", Terms: []string{"Otherco"}},
	}
	hits := FindSpoken(lines, subjects)
	if len(hits) != 2 {
		t.Fatalf("hits: %+v", hits)
	}
	if hits[0].Subject.Name != "Northpeak" || hits[0].At != 125 || hits[1].Matched != "Acme Proxy" || hits[1].At != 61 {
		t.Fatalf("hits: %+v", hits)
	}
	if ParseCaptions("not json") != nil || FindSpoken(nil, subjects) != nil {
		t.Fatal("empty")
	}
}
