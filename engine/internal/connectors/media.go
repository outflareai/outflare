package connectors

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Making pictures, video and voice: connectors that turn a prompt into a
// file on the workspace's own account with the provider. The model is
// always the caller's choice, never a default here: providers retire and
// rename models every few months, so a model is a setting, not code.

// CategoryMedia groups the providers that make pictures, video and voice.
const CategoryMedia = "media"

// Capabilities of a MediaGenerator, one per kind of file it makes.
const (
	CapMediaImage = "media.image"
	CapMediaVideo = "media.video"
	CapMediaVoice = "media.voice"
)

// Kinds of file a MediaGenerator makes.
const (
	MediaImage = "image"
	MediaVideo = "video"
	MediaVoice = "voice"
)

// MediaKinds lists the kinds.
var MediaKinds = []string{MediaImage, MediaVideo, MediaVoice}

// MediaCapability is the capability a connector needs to make kind.
func MediaCapability(kind string) string { return "media." + kind }

// Limits on a media request.
const (
	MediaPromptMax = 32000 // characters in a prompt or script
	MediaModelMax  = 200
	// MediaMaxBytes is the largest file a generator hands back.
	MediaMaxBytes = 50 << 20
)

// MediaRequest asks for one file.
type MediaRequest struct {
	// Kind is image, video or voice.
	Kind string `json:"kind"`
	// Model is the provider's own id for the model, such as an OpenAI
	// image model or a fal.ai endpoint (owner/app/path).
	Model string `json:"model"`
	// Prompt describes the picture or video; for a voice it is the script
	// to read out.
	Prompt string `json:"prompt"`
	// Voice is the provider's id of the voice that reads the script.
	Voice string `json:"voice,omitempty"`
	// Options are the model's own settings (size, quality, duration,
	// aspect ratio, voice settings), passed to the provider as they are.
	// They never replace the prompt or the model.
	Options map[string]any `json:"options,omitempty"`
	// Files are reference files sent with the request: a picture of a
	// person or a scene to keep, a voice-over a video is made to, a video
	// to upscale. Each names the model's input it goes in.
	Files []MediaFile `json:"-"`
}

// MediaFile is one reference file of a request.
type MediaFile struct {
	// As is the model's own name for the input the file goes in, such as
	// image_urls, audio_urls or video_url. A name that ends in "s" takes
	// several files.
	As          string
	Name        string
	ContentType string
	Data        []byte
}

// MediaFilesMax is how many reference files one request carries.
const MediaFilesMax = 12

var mediaInputRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,59}$`)

// Normal checks a request and returns it tidied. A request it refuses is
// an invalid Error, before anything is sent or spent.
func (q MediaRequest) Normal() (MediaRequest, error) {
	q.Kind = strings.TrimSpace(q.Kind)
	ok := false
	for _, k := range MediaKinds {
		ok = ok || k == q.Kind
	}
	if !ok {
		return q, Errf(ErrInvalid, "kind is one of %s", strings.Join(MediaKinds, ", "))
	}
	q.Model = strings.TrimSpace(q.Model)
	if q.Model == "" || len(q.Model) > MediaModelMax || strings.ContainsAny(q.Model, " \t\r\n?#\\") {
		return q, Errf(ErrInvalid, "model is the provider's id of the model to use, such as the name of an image model or a fal.ai endpoint")
	}
	if len(q.Files) > MediaFilesMax {
		return q, Errf(ErrInvalid, "a request carries at most %d reference files", MediaFilesMax)
	}
	for _, f := range q.Files {
		if !mediaInputRe.MatchString(f.As) || f.As == "prompt" {
			return q, Errf(ErrInvalid, "a reference file names the model's input it goes in, such as image_urls or video_url")
		}
	}
	q.Prompt = strings.TrimSpace(q.Prompt)
	// A model that only works on a file (an upscaler) takes no prompt.
	if q.Prompt == "" && (q.Kind == MediaVoice || len(q.Files) == 0) {
		if q.Kind == MediaVoice {
			return q, Errf(ErrInvalid, "prompt is the script the voice reads out")
		}
		return q, Errf(ErrInvalid, "prompt says what to make")
	}
	if utf8.RuneCountInString(q.Prompt) > MediaPromptMax {
		return q, Errf(ErrInvalid, "the prompt is too long (at most %d characters)", MediaPromptMax)
	}
	q.Voice = strings.TrimSpace(q.Voice)
	if len(q.Voice) > 100 || strings.ContainsAny(q.Voice, " \t\r\n/?#\\") {
		return q, Errf(ErrInvalid, "voice is the provider's id of a voice")
	}
	if q.Kind == MediaVoice && q.Voice == "" {
		return q, Errf(ErrInvalid, "a voice needs voice: the provider's id of the voice that reads the script")
	}
	if len(q.Options) > 50 {
		return q, Errf(ErrInvalid, "options holds at most 50 settings")
	}
	return q, nil
}

// MediaRun is where a request stands with the provider.
type MediaRun struct {
	// RunID is the provider's id for work that is still going, to ask
	// about later instead of starting (and paying for) it again. Empty
	// when the provider answered in one call.
	RunID string
	// Done is true once Data holds the file.
	Done bool
	// Data is the file; ContentType what the provider says it is (the
	// caller works the real type out from the bytes).
	Data        []byte
	ContentType string
	// Usage is what the provider reported it used (tokens, characters,
	// billable units): numbers only.
	Usage map[string]float64
	// Request is what was sent to the provider, as JSON, with reference
	// files as their links or names and never a key. Response is the
	// provider's answer without the file itself. Together they are the
	// recipe of the file: enough to make one like it again.
	Request  json.RawMessage
	Response json.RawMessage
	// Failure says, in words a person can act on, why a run ended without
	// a file. Asking about that run again gives the same answer.
	Failure string
}

// MediaGenerator is implemented by connectors that make pictures, video or
// voice. StartMedia sends the request; when the provider works on it for
// minutes it returns a RunID, and FetchMedia asks how that run is doing. A
// failure the provider puts down to the request itself (a prompt its rules
// refuse, a setting the model doesn't have) is an invalid Error, which the
// caller doesn't retry.
type MediaGenerator interface {
	// MediaKinds are the kinds it makes.
	MediaKinds() []string
	StartMedia(ctx context.Context, cl *Client, creds Creds, q MediaRequest) (MediaRun, error)
	FetchMedia(ctx context.Context, cl *Client, creds Creds, q MediaRequest, runID string) (MediaRun, error)
}

// CapTranscribe is the capability of a connector that turns speech into
// words with the time each one is said.
const CapTranscribe = "media.transcribe"

// SpokenWord is one word of a recording, with when it starts and ends in
// seconds.
type SpokenWord struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Transcript is what a recording says.
type Transcript struct {
	Text     string       `json:"text"`
	Language string       `json:"language"`
	Seconds  float64      `json:"seconds"`
	Words    []SpokenWord `json:"words"`
}

// Transcriber is implemented by connectors that write a recording out
// word by word. The model is the caller's choice, as for a MediaGenerator.
type Transcriber interface {
	Transcribe(ctx context.Context, cl *Client, creds Creds, model string, file MediaFile) (Transcript, error)
}
