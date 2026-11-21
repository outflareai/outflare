package mentions

import "testing"

func TestParseTranscript(t *testing.T) {
	vtt := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:04.000\n<v Ana>Welcome back.\n\n2\n00:01:35.500 --> 00:01:39.000\nWe moved to Summitly &amp; never looked back.\n"
	srt := "1\r\n00:00:01,000 --> 00:00:04,000\r\nWelcome back.\r\n\r\n2\r\n01:01:35,000 --> 01:01:39,000\r\nWe moved to Summitly.\r\n"
	js := `{"version":"1.0.0","segments":[{"startTime":1,"body":"Welcome"},{"startTime":2,"body":"back."},{"startTime":95.2,"body":"We moved to Summitly."}]}`
	for _, c := range []struct {
		name, body, typ string
		at              float64
	}{
		{"vtt", vtt, "text/vtt", 95},
		{"srt", srt, "application/x-subrip", 3695},
		{"json", js, "application/json", 95.2},
		{"vtt by look", vtt, "", 95},
	} {
		lines := ParseTranscript(c.body, c.typ)
		if len(lines) != 2 || lines[0].Text != "Welcome back." || lines[1].Start != c.at {
			t.Errorf("%s: %+v", c.name, lines)
		}
	}
	lines := ParseTranscript("<p>Welcome back.</p><p>We moved to <b>Summitly</b>.</p>", "text/html")
	if len(lines) != 2 || lines[1].Text != "We moved to Summitly." || lines[1].Start != 0 {
		t.Errorf("html: %+v", lines)
	}
	hits := FindSpoken(ParseTranscript(vtt, "text/vtt"), []Subject{{About: "competitor", Name: "Summitly", Terms: []string{"Summitly"}}})
	if len(hits) != 1 || hits[0].At != 95 {
		t.Errorf("spoken: %+v", hits)
	}
}
