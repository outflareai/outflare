package mentions

import (
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// Podcast transcripts: many shows publish one in their feed
// (podcast:transcript), as WebVTT, SubRip, the podcast JSON format, HTML or
// plain text. ParseTranscript reads each into lines with the second each
// starts; untimed ones (HTML, plain text) start every line at 0, so a name
// in them is found but not its second.

var (
	cueTime = regexp.MustCompile(`^\s*(?:(\d+):)?(\d{1,2}):(\d{2})[.,](\d{1,3})\s*-->`)
	tags    = regexp.MustCompile(`<[^>]*>`)
	spaces  = regexp.MustCompile(`\s+`)
)

// ParseTranscript reads a transcript of the given media type. A type it
// doesn't know is read by its look: JSON, then cues, then text.
func ParseTranscript(body, mediaType string) []Line {
	body = strings.TrimPrefix(body, "\ufeff")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	trimmed := strings.TrimSpace(body)
	switch {
	case mediaType == "application/json" || (mediaType == "" && strings.HasPrefix(trimmed, "{")):
		if lines := parseJSONTranscript(trimmed); len(lines) > 0 {
			return lines
		}
	case strings.Contains(mediaType, "vtt") || strings.Contains(mediaType, "srt") || strings.Contains(mediaType, "subrip") ||
		(mediaType == "" && strings.Contains(body, "-->")):
		if lines := parseCues(body); len(lines) > 0 {
			return lines
		}
	}
	if strings.Contains(mediaType, "html") || strings.HasPrefix(trimmed, "<") {
		body = tags.ReplaceAllString(strings.NewReplacer("</p>", "\n", "<br>", "\n", "<br/>", "\n", "<br />", "\n").Replace(body), "")
		body = html.UnescapeString(body)
	}
	var out []Line
	for _, l := range strings.Split(body, "\n") {
		if t := clean(l); t != "" {
			out = append(out, Line{Text: t})
		}
	}
	return out
}

func clean(s string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(tags.ReplaceAllString(html.UnescapeString(s), ""), " "))
}

// parseCues reads WebVTT or SubRip: a time line, then the cue's text.
func parseCues(body string) []Line {
	var out []Line
	var cur *Line
	for _, l := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if m := cueTime.FindStringSubmatch(l); m != nil {
			h, _ := strconv.Atoi(m[1])
			mi, _ := strconv.Atoi(m[2])
			se, _ := strconv.Atoi(m[3])
			out = append(out, Line{Start: float64(h*3600 + mi*60 + se)})
			cur = &out[len(out)-1]
			continue
		}
		t := strings.TrimSpace(l)
		if cur == nil || t == "" {
			if t == "" {
				cur = nil
			}
			continue
		}
		// A VTT voice tag names the speaker: <v Ana>Hello.
		if text := clean(t); text != "" {
			if cur.Text != "" {
				cur.Text += " "
			}
			cur.Text += text
		}
	}
	kept := out[:0]
	for _, l := range out {
		if l.Text != "" {
			kept = append(kept, l)
		}
	}
	return kept
}

// parseJSONTranscript reads the podcast JSON transcript format:
// {"segments": [{"startTime": 1.2, "body": "...", "speaker": "..."}]}.
func parseJSONTranscript(body string) []Line {
	var doc struct {
		Segments []struct {
			Start json.Number `json:"startTime"`
			Body  string      `json:"body"`
		} `json:"segments"`
	}
	d := json.NewDecoder(strings.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&doc); err != nil {
		return nil
	}
	var out []Line
	for _, s := range doc.Segments {
		t := clean(s.Body)
		if t == "" {
			continue
		}
		start, _ := s.Start.Float64()
		// Word-level transcripts are one segment a word: join each into
		// the line before when it starts within a few seconds.
		if n := len(out); n > 0 && start-out[n-1].Start < 5 && len(out[n-1].Text) < 300 {
			out[n-1].Text += " " + t
			continue
		}
		out = append(out, Line{Start: start, Text: t})
	}
	return out
}
