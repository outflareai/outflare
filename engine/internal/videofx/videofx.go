// Package videofx finishes and edits videos with the ffmpeg program on
// the server: closing gaps in a voice-over, the look of a phone camera,
// captions, cutting a stretch out and stitching a new one in. It is what
// makes a rendered talking video look filmed, and what lets one word be
// fixed without paying for the whole video again.
//
// ffmpeg is optional. A server without it (Find reports false) still
// makes videos; it skips these steps and says so.
package videofx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font/gofont/gobold"
)

// Tool runs ffmpeg and ffprobe.
type Tool struct {
	ffmpeg, ffprobe string
}

// ErrBadFile is a file ffmpeg can't read or work on.
var ErrBadFile = errors.New("the file can't be read as video or sound")

// limit is how long one ffmpeg run may take.
const limit = 10 * time.Minute

// Find looks for ffmpeg and ffprobe on the server's PATH, with the H.264
// and AAC encoders the finished file needs. The reason says what is
// missing.
func Find() (*Tool, string) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, "ffmpeg is not installed on this server"
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		return nil, "ffprobe is not installed on this server"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ff, "-hide_banner", "-encoders").Output()
	if err != nil || !bytes.Contains(out, []byte("libx264")) || !bytes.Contains(out, []byte(" aac ")) {
		return nil, "the server's ffmpeg has no H.264 (libx264) or AAC encoder"
	}
	return &Tool{ffmpeg: ff, ffprobe: probe}, ""
}

// Info is what a file holds.
type Info struct {
	Seconds       float64
	Width, Height int
	Video, Audio  bool
}

// work is a temporary folder for one run.
type work struct{ dir string }

func newWork() (*work, error) {
	dir, err := os.MkdirTemp("", "outflare-video-")
	return &work{dir: dir}, err
}

func (w *work) close() { _ = os.RemoveAll(w.dir) }

func (w *work) path(name string) string { return filepath.Join(w.dir, name) }

func (w *work) put(name string, data []byte) (string, error) {
	p := w.path(name)
	return p, os.WriteFile(p, data, 0o600)
}

// input are the arguments that open one file: only local files, so a
// playlist inside an uploaded file can't make ffmpeg fetch anything.
func input(path string) []string {
	return []string{"-protocol_whitelist", "file", "-i", path}
}

// run runs ffmpeg with args and returns the file it wrote.
func (t *Tool) run(ctx context.Context, w *work, out string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	full := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}, args...)
	full = append(full, w.path(out))
	cmd := exec.CommandContext(ctx, t.ffmpeg, full...)
	cmd.Dir = w.dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return nil, fmt.Errorf("%w: %s", ErrBadFile, msg)
	}
	data, err := os.ReadFile(w.path(out))
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("%w: ffmpeg wrote nothing", ErrBadFile)
	}
	return data, nil
}

func (t *Tool) probe(ctx context.Context, path string) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, t.ffprobe, "-v", "error", "-protocol_whitelist", "file", "-show_entries",
		"format=duration:stream=codec_type,width,height", "-of", "json", path).Output()
	if err != nil {
		return Info{}, ErrBadFile
	}
	var p struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Type   string `json:"codec_type"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &p) != nil {
		return Info{}, ErrBadFile
	}
	var info Info
	info.Seconds, _ = strconv.ParseFloat(p.Format.Duration, 64)
	for _, s := range p.Streams {
		switch s.Type {
		case "video":
			if !info.Video {
				info.Video, info.Width, info.Height = true, s.Width, s.Height
			}
		case "audio":
			info.Audio = true
		}
	}
	if info.Seconds <= 0 || (!info.Video && !info.Audio) {
		return info, ErrBadFile
	}
	return info, nil
}

// Probe reads how long a video or sound file is and its size.
func (t *Tool) Probe(ctx context.Context, data []byte) (Info, error) {
	w, err := newWork()
	if err != nil {
		return Info{}, err
	}
	defer w.close()
	p, err := w.put("in", data)
	if err != nil {
		return Info{}, err
	}
	return t.probe(ctx, p)
}

// Tighten closes the silences in a voice-over that are longer than gap
// seconds, down to gap, so the pacing is that of someone talking to a
// camera. It answers with an MP3.
func (t *Tool) Tighten(ctx context.Context, audio []byte, gap float64) ([]byte, error) {
	w, err := newWork()
	if err != nil {
		return nil, err
	}
	defer w.close()
	in, err := w.put("in", audio)
	if err != nil {
		return nil, err
	}
	g := num(min(max(gap, 0.1), 2))
	filter := "silenceremove=start_periods=1:start_duration=0:start_threshold=-45dB:stop_periods=-1:stop_duration=" + g + ":stop_threshold=-45dB:stop_silence=" + g
	args := append(input(in), "-vn", "-af", filter, "-c:a", "libmp3lame", "-b:a", "128k", "-ar", "44100", "-f", "mp3")
	return t.run(ctx, w, "out.mp3", args...)
}

// Caption is a few words shown while they are said.
type Caption struct {
	Text       string
	Start, End float64
}

// Look is how a video is finished.
type Look struct {
	// Phone gives the picture the texture of a phone camera: 30 frames a
	// second, a little grain and sharpening, and a phone's compression.
	Phone bool
	// Captions are burned into the picture.
	Captions []Caption
}

// encode are the arguments every finished video is written with.
var encode = []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-ar", "44100",
	"-movflags", "+faststart", "-f", "mp4"}

// Finish gives a video its final look and answers with an MP4.
func (t *Tool) Finish(ctx context.Context, video []byte, look Look) ([]byte, error) {
	w, err := newWork()
	if err != nil {
		return nil, err
	}
	defer w.close()
	in, err := w.put("in", video)
	if err != nil {
		return nil, err
	}
	info, err := t.probe(ctx, in)
	if err != nil || !info.Video {
		return nil, ErrBadFile
	}
	// Even sizes only: H.264 with this pixel format needs them.
	filters := []string{"scale=trunc(iw/2)*2:trunc(ih/2)*2"}
	if look.Phone {
		filters = append(filters, "fps=30", "eq=contrast=1.03:saturation=1.04", "unsharp=5:5:0.5:5:5:0.0", "noise=alls=6:allf=t")
	}
	if len(look.Captions) > 0 {
		if _, err := w.put("captions.ass", []byte(captionFile(look.Captions, info.Width, info.Height))); err != nil {
			return nil, err
		}
		if err := os.Mkdir(w.path("fonts"), 0o700); err != nil {
			return nil, err
		}
		if _, err := w.put(filepath.Join("fonts", "caption.ttf"), gobold.TTF); err != nil {
			return nil, err
		}
		// Relative names: the run's folder is the working directory.
		filters = append(filters, "ass=captions.ass:fontsdir=fonts")
	}
	args := append(input(in), "-vf", strings.Join(filters, ","))
	return t.run(ctx, w, "out.mp4", append(args, encode...)...)
}

// Cut takes the stretch from one time to another out of a video and
// closes the gap.
func (t *Tool) Cut(ctx context.Context, video []byte, from, to float64) ([]byte, error) {
	w, err := newWork()
	if err != nil {
		return nil, err
	}
	defer w.close()
	in, err := w.put("in", video)
	if err != nil {
		return nil, err
	}
	info, err := t.probe(ctx, in)
	if err != nil || !info.Video {
		return nil, ErrBadFile
	}
	from, to = max(from, 0), min(to, info.Seconds)
	if to-from < 0.02 || (from < 0.02 && info.Seconds-to < 0.02) {
		return nil, fmt.Errorf("%w: nothing would be left", ErrBadFile)
	}
	var parts []string
	graph := ""
	add := func(a, b float64) {
		if b-a < 0.02 {
			return
		}
		i := strconv.Itoa(len(parts))
		graph += "[0:v]trim=" + num(a) + ":" + num(b) + ",setpts=PTS-STARTPTS[v" + i + "];"
		if info.Audio {
			graph += "[0:a]atrim=" + num(a) + ":" + num(b) + ",asetpts=PTS-STARTPTS[a" + i + "];"
		}
		parts = append(parts, i)
	}
	add(0, from)
	add(to, info.Seconds)
	return t.run(ctx, w, "out.mp4", append(append(input(in), joined(graph, parts, info.Audio)...), encode...)...)
}

// joined ends a filter graph by joining its parts one after another.
func joined(graph string, parts []string, audio bool) []string {
	for _, i := range parts {
		graph += "[v" + i + "]"
		if audio {
			graph += "[a" + i + "]"
		}
	}
	a := "0"
	if audio {
		a = "1"
	}
	graph += "concat=n=" + strconv.Itoa(len(parts)) + ":v=1:a=" + a + "[v]"
	args := []string{"-filter_complex", "", "-map", "[v]"}
	if audio {
		graph += "[a]"
		args = append(args, "-map", "[a]")
	}
	args[1] = graph
	return args
}

// Splice replaces the stretch from one time to another of a video with
// part, resized to match, and answers with the whole video.
func (t *Tool) Splice(ctx context.Context, base, part []byte, from, to float64) ([]byte, error) {
	w, err := newWork()
	if err != nil {
		return nil, err
	}
	defer w.close()
	in, err := w.put("base", base)
	if err != nil {
		return nil, err
	}
	seg, err := w.put("part", part)
	if err != nil {
		return nil, err
	}
	info, err := t.probe(ctx, in)
	if err != nil || !info.Video {
		return nil, ErrBadFile
	}
	pinfo, err := t.probe(ctx, seg)
	if err != nil || !pinfo.Video || pinfo.Audio != info.Audio {
		return nil, fmt.Errorf("%w: the new part doesn't match the video", ErrBadFile)
	}
	from, to = max(from, 0), min(to, info.Seconds)
	if to <= from {
		return nil, ErrBadFile
	}
	wd, ht := strconv.Itoa(info.Width/2*2), strconv.Itoa(info.Height/2*2)
	// Every part gets the same size, shape of pixel and frame rate, or
	// they can't be joined.
	same := ",scale=" + wd + ":" + ht + ":force_original_aspect_ratio=increase:flags=lanczos,crop=" + wd + ":" + ht + ",setsar=1,fps=30,format=yuv420p"
	sound := ",aresample=44100,aformat=channel_layouts=stereo"
	var parts []string
	graph := ""
	add := func(src string, a, b float64, whole bool) {
		if !whole && b-a < 0.02 {
			return
		}
		i := strconv.Itoa(len(parts))
		cut, acut := "trim="+num(a)+":"+num(b)+",", "atrim="+num(a)+":"+num(b)+","
		if whole {
			cut, acut = "", ""
		}
		graph += "[" + src + ":v]" + cut + "setpts=PTS-STARTPTS" + same + "[v" + i + "];"
		if info.Audio {
			graph += "[" + src + ":a]" + acut + "asetpts=PTS-STARTPTS" + sound + "[a" + i + "];"
		}
		parts = append(parts, i)
	}
	add("0", 0, from, false)
	add("1", 0, 0, true)
	add("0", to, info.Seconds, false)
	args := append(input(in), input(seg)...)
	return t.run(ctx, w, "out.mp4", append(append(args, joined(graph, parts, info.Audio)...), encode...)...)
}

// Frame is the picture a video shows at a time, as a PNG: the reference
// a new part is rendered from, so it starts where the old one stopped.
func (t *Tool) Frame(ctx context.Context, video []byte, at float64) ([]byte, error) {
	w, err := newWork()
	if err != nil {
		return nil, err
	}
	defer w.close()
	in, err := w.put("in", video)
	if err != nil {
		return nil, err
	}
	info, err := t.probe(ctx, in)
	if err != nil || !info.Video {
		return nil, ErrBadFile
	}
	at = min(max(at, 0), max(info.Seconds-0.05, 0))
	args := append([]string{"-ss", num(at)}, input(in)...)
	return t.run(ctx, w, "out.png", append(args, "-frames:v", "1", "-f", "image2", "-c:v", "png")...)
}

// SoundOf is the sound of a video from one time to another, as an MP3.
func (t *Tool) SoundOf(ctx context.Context, video []byte, from, to float64) ([]byte, error) {
	w, err := newWork()
	if err != nil {
		return nil, err
	}
	defer w.close()
	in, err := w.put("in", video)
	if err != nil {
		return nil, err
	}
	args := append([]string{"-ss", num(max(from, 0)), "-t", num(max(to-from, 0.05))}, input(in)...)
	return t.run(ctx, w, "out.mp3", append(args, "-vn", "-c:a", "libmp3lame", "-b:a", "128k", "-ar", "44100", "-f", "mp3")...)
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }

// captionFile writes captions as an ASS subtitle file: bold white words
// with a dark outline in the lower third, sized to the picture.
func captionFile(captions []Caption, width, height int) string {
	if width <= 0 || height <= 0 {
		width, height = 1080, 1920
	}
	size := max(min(width, height)/14, 18)
	var b strings.Builder
	fmt.Fprintf(&b, "[Script Info]\nScriptType: v4.00+\nPlayResX: %d\nPlayResY: %d\nWrapStyle: 0\nScaledBorderAndShadow: yes\n\n", width, height)
	b.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, OutlineColour, BackColour, Bold, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV\n")
	fmt.Fprintf(&b, "Style: Words,Go,%d,&H00FFFFFF,&H00000000,&H80000000,1,1,%d,0,2,%d,%d,%d\n\n", size, max(size/12, 2), width/12, width/12, height/4)
	b.WriteString("[Events]\nFormat: Layer, Start, End, Style, Text\n")
	for _, c := range captions {
		if c.End <= c.Start || strings.TrimSpace(c.Text) == "" {
			continue
		}
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Words,%s\n", assTime(c.Start), assTime(c.End), assText(c.Text))
	}
	return b.String()
}

func assTime(s float64) string {
	cs := int(s*100 + 0.5)
	return fmt.Sprintf("%d:%02d:%02d.%02d", cs/360000, cs/6000%60, cs/100%60, cs%100)
}

// assText keeps caption words from being read as ASS commands.
func assText(s string) string {
	s = strings.NewReplacer("\\", "", "{", "(", "}", ")", "\r", " ", "\n", " ").Replace(s)
	return strings.TrimSpace(s)
}
