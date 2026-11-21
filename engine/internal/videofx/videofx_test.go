package videofx

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// sample makes a short test video with sound (or only a sound file) with
// ffmpeg's own test sources.
func sample(t *testing.T, tool *Tool, seconds string, video bool) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "sample")
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if video {
		args = append(args, "-f", "lavfi", "-i", "testsrc=size=320x568:rate=24:duration="+seconds)
	}
	args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:duration="+seconds)
	if video {
		args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", "-f", "mp4")
	} else {
		// A second of tone, a second and a half of silence, another second.
		args = append(args, "-af", "volume=enable='between(t,1,2.5)':volume=0", "-c:a", "libmp3lame", "-f", "mp3")
	}
	if msg, err := exec.Command(tool.ffmpeg, append(args, out)...).CombinedOutput(); err != nil {
		t.Fatalf("making a sample: %v: %s", err, msg)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func near(a, b, by float64) bool { return math.Abs(a-b) <= by }

func TestTool(t *testing.T) {
	tool, why := Find()
	if tool == nil {
		t.Skip(why)
	}
	ctx := context.Background()
	video := sample(t, tool, "4", true)
	info, err := tool.Probe(ctx, video)
	if err != nil || !info.Video || !info.Audio || info.Width != 320 || info.Height != 568 || !near(info.Seconds, 4, 0.2) {
		t.Fatalf("probe: %+v %v", info, err)
	}
	if _, err := tool.Probe(ctx, []byte("not a video")); err == nil {
		t.Fatal("probed a file that is not a video")
	}

	// The phone look and captions: still the same length, 30 frames a second.
	done, err := tool.Finish(ctx, video, Look{Phone: true, Captions: []Caption{{Text: "Meet {Northpeak}", Start: 0.2, End: 1.5}, {Text: "today", Start: 1.5, End: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if info, err = tool.Probe(ctx, done); err != nil || info.Width != 320 || !near(info.Seconds, 4, 0.2) {
		t.Fatalf("finished: %+v %v", info, err)
	}

	// A second cut out of the middle.
	cut, err := tool.Cut(ctx, video, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if info, err = tool.Probe(ctx, cut); err != nil || !near(info.Seconds, 3, 0.2) || !info.Audio {
		t.Fatalf("cut: %+v %v", info, err)
	}
	if _, err := tool.Cut(ctx, video, 0, 10); err == nil {
		t.Fatal("cut the whole video away")
	}

	// A two-second part in place of one second: a second longer.
	part := sample(t, tool, "2", true)
	spliced, err := tool.Splice(ctx, video, part, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if info, err = tool.Probe(ctx, spliced); err != nil || !near(info.Seconds, 5, 0.25) || info.Width != 320 || info.Height != 568 {
		t.Fatalf("spliced: %+v %v", info, err)
	}

	frame, err := tool.Frame(ctx, video, 1.5)
	if err != nil || len(frame) < 8 || string(frame[1:4]) != "PNG" {
		t.Fatalf("frame: %d bytes, %v", len(frame), err)
	}
	if sound, err := tool.SoundOf(ctx, video, 1, 2); err != nil || len(sound) == 0 {
		t.Fatalf("sound: %v", err)
	}

	// A voice-over with a long silence comes back shorter.
	voice := sample(t, tool, "3.5", false)
	tight, err := tool.Tighten(ctx, voice, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	if info, err = tool.Probe(ctx, tight); err != nil || info.Seconds > 2.9 || info.Seconds < 1.8 {
		t.Fatalf("tightened: %+v %v", info, err)
	}
}

func TestCaptionFile(t *testing.T) {
	got := captionFile([]Caption{{Text: "a {\\b1}b", Start: 61.5, End: 62}}, 1080, 1920)
	for _, want := range []string{"PlayResX: 1080", "Dialogue: 0,0:01:01.50,0:01:02.00,Words,a (b1)b"} {
		if !contains(got, want) {
			t.Fatalf("caption file lacks %q:\n%s", want, got)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
