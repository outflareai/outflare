package mentions

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// videoIDPattern is what a YouTube video id looks like.
var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// VideoID is the id of the YouTube video a URL points at (a watch page, a
// youtu.be link, a short or a live page), or "".
func VideoID(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), "m.")
	var id string
	switch host {
	case "youtu.be":
		id, _, _ = strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
	case "youtube.com", "music.youtube.com":
		if u.Path == "/watch" {
			id = u.Query().Get("v")
		} else {
			for _, prefix := range []string{"/shorts/", "/live/", "/embed/"} {
				if rest, ok := strings.CutPrefix(u.Path, prefix); ok {
					id, _, _ = strings.Cut(rest, "/")
				}
			}
		}
	}
	if !videoIDPattern.MatchString(id) {
		return ""
	}
	return id
}

// WatchURL is a video's watch page.
func WatchURL(id string) string { return "https://www.youtube.com/watch?v=" + id }

// Video is what a watch page says about its video.
type Video struct {
	ID         string
	Title      string
	Channel    string
	ChannelID  string
	ChannelURL string
	Views      *int64
	Published  *time.Time
	Seconds    int
	// CaptionsURL is the best caption track: the video's own language
	// written by people first, then English, then automatic captions.
	CaptionsURL  string
	CaptionsLang string
}

// ParseWatch reads the player data in a YouTube watch page. ok is false
// when the page holds none (a consent page, a removed video).
func ParseWatch(html string) (Video, bool) {
	const marker = "ytInitialPlayerResponse"
	i := strings.Index(html, marker)
	for i >= 0 {
		rest := html[i+len(marker):]
		if j := strings.IndexByte(rest, '{'); j >= 0 && j < 8 {
			var p playerResponse
			if err := json.NewDecoder(strings.NewReader(rest[j:])).Decode(&p); err == nil && p.VideoDetails.VideoID != "" {
				return p.video(), true
			}
		}
		next := strings.Index(rest, marker)
		if next < 0 {
			break
		}
		i += len(marker) + next
	}
	return Video{}, false
}

type playerResponse struct {
	VideoDetails struct {
		VideoID       string `json:"videoId"`
		Title         string `json:"title"`
		Author        string `json:"author"`
		ChannelID     string `json:"channelId"`
		ViewCount     string `json:"viewCount"`
		LengthSeconds string `json:"lengthSeconds"`
	} `json:"videoDetails"`
	Microformat struct {
		Renderer struct {
			PublishDate     string `json:"publishDate"`
			OwnerProfileURL string `json:"ownerProfileUrl"`
		} `json:"playerMicroformatRenderer"`
	} `json:"microformat"`
	Captions struct {
		Renderer struct {
			Tracks []struct {
				BaseURL      string `json:"baseUrl"`
				LanguageCode string `json:"languageCode"`
				Kind         string `json:"kind"`
			} `json:"captionTracks"`
		} `json:"playerCaptionsTracklistRenderer"`
	} `json:"captions"`
}

func (p playerResponse) video() Video {
	d := p.VideoDetails
	v := Video{ID: d.VideoID, Title: d.Title, Channel: d.Author, ChannelID: d.ChannelID}
	if n, err := strconv.ParseInt(d.ViewCount, 10, 64); err == nil && n >= 0 {
		v.Views = &n
	}
	v.Seconds, _ = strconv.Atoi(d.LengthSeconds)
	if t, err := time.Parse(time.RFC3339, p.Microformat.Renderer.PublishDate); err == nil {
		v.Published = &t
	} else if t, err := time.Parse("2006-01-02", p.Microformat.Renderer.PublishDate); err == nil {
		v.Published = &t
	}
	switch owner := p.Microformat.Renderer.OwnerProfileURL; {
	case strings.HasPrefix(owner, "https://"):
		v.ChannelURL = owner
	case strings.HasPrefix(owner, "http://"):
		v.ChannelURL = "https://" + strings.TrimPrefix(owner, "http://")
	case d.ChannelID != "":
		v.ChannelURL = "https://www.youtube.com/channel/" + d.ChannelID
	}
	// People's captions before automatic ones, English before others.
	tracks := p.Captions.Renderer.Tracks
	best, bestRank := -1, 99
	for i, t := range tracks {
		if !strings.HasPrefix(t.BaseURL, "https://www.youtube.com/") && !strings.HasPrefix(t.BaseURL, "/") {
			continue
		}
		rank := 0
		if t.Kind == "asr" {
			rank += 2
		}
		if !strings.HasPrefix(t.LanguageCode, "en") {
			rank++
		}
		if rank < bestRank {
			best, bestRank = i, rank
		}
	}
	if best >= 0 {
		u := tracks[best].BaseURL
		if strings.HasPrefix(u, "/") {
			u = "https://www.youtube.com" + u
		}
		v.CaptionsURL, v.CaptionsLang = u, tracks[best].LanguageCode
	}
	return v
}

// CaptionsJSONURL asks a caption track for its JSON form.
func CaptionsJSONURL(track string) string {
	u, err := url.Parse(track)
	if err != nil {
		return track
	}
	q := u.Query()
	q.Set("fmt", "json3")
	u.RawQuery = q.Encode()
	return u.String()
}

// Line is one caption: what was said, from Start seconds into the video.
type Line struct {
	Start float64 `json:"start"`
	Text  string  `json:"text"`
}

// ParseCaptions reads a caption track in its JSON form (json3), or, when a
// page wraps it, the JSON inside the page. Lines are in time order.
func ParseCaptions(body string) []Line {
	b := []byte(strings.TrimSpace(body))
	if i := bytes.IndexByte(b, '{'); i > 0 {
		b = b[i:]
	}
	var raw struct {
		Events []struct {
			Start float64 `json:"tStartMs"`
			Segs  []struct {
				Text string `json:"utf8"`
			} `json:"segs"`
		} `json:"events"`
	}
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&raw); err != nil {
		return nil
	}
	var out []Line
	for _, e := range raw.Events {
		var sb strings.Builder
		for _, s := range e.Segs {
			sb.WriteString(s.Text)
		}
		text := strings.Join(strings.Fields(sb.String()), " ")
		if text == "" {
			continue
		}
		out = append(out, Line{Start: e.Start / 1000, Text: text})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// SpokenHit is one subject named in a video's captions.
type SpokenHit struct {
	Hit
	// At is the second of the video where it was said.
	At int
}

// FindSpoken reports each subject named in captions, once, at the first
// place it was said, with the words around it.
func FindSpoken(lines []Line, subjects []Subject) []SpokenHit {
	if len(lines) == 0 {
		return nil
	}
	var sb strings.Builder
	starts := make([]int, len(lines))
	for i, l := range lines {
		if i > 0 {
			sb.WriteByte(' ')
		}
		starts[i] = sb.Len()
		sb.WriteString(l.Text)
	}
	var out []SpokenHit
	for _, h := range Find(sb.String(), subjects) {
		i := sort.SearchInts(starts, h.Offset+1) - 1
		if i < 0 {
			i = 0
		}
		out = append(out, SpokenHit{Hit: h, At: int(lines[i].Start)})
	}
	return out
}

// YouTubeQuery is the web search that finds videos naming term.
func YouTubeQuery(term string) string {
	return `site:youtube.com "` + strings.ReplaceAll(term, `"`, "") + `"`
}
