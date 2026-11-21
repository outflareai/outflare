// Package assets looks at the files a brand keeps (logos, fonts, product
// shots, screenshots, example ads, voices) and makes smaller copies of its
// pictures. It reads only the bytes it is given: nothing here fetches a
// file, runs it or trusts the name or type the uploader sent.
package assets

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // read GIF pictures
	"image/jpeg"
	"image/png"
	"net/http"
	"path"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // read WebP pictures
)

// Kinds are what a file is for. They decide which types of file fit.
var Kinds = []string{"logo", "font", "product_shot", "screenshot", "example_ad", "voice", "person", "recording", "other"}

// Families of file a kind accepts.
var kindFamilies = map[string][]string{
	"logo":         {"image"},
	"font":         {"font"},
	"product_shot": {"image", "video"},
	"screenshot":   {"image"},
	"example_ad":   {"image", "video"},
	"voice":        {"audio"},
	"person":       {"image", "video"},
	"recording":    {"video"},
	"other":        {"image", "video", "audio", "font"},
}

// NeedsConsent reports whether files of this kind show or sound like a
// real person, so they may be used only once someone has said the person
// agreed.
func NeedsConsent(kind string) bool { return kind == "voice" || kind == "person" }

// MaxBytes is the largest file the store takes.
const MaxBytes = 50 << 20

// maxPixels is the largest picture that is decoded. A bigger one is kept
// as a file but gets no smaller copies, so a tiny file that unpacks into
// a huge picture can't use up the server's memory.
const maxPixels = 60_000_000

// ErrType is a file whose type the store doesn't take, or that doesn't
// fit its kind.
var ErrType = errors.New("assets: file type not accepted")

// Info is what a file turned out to be.
type Info struct {
	ContentType string // from the bytes, never from the uploader
	Family      string // image, video, audio or font
	Width       int    // pictures only; 0 when unknown
	Height      int
}

// types the store takes, by what the bytes say.
var families = map[string]string{
	"image/png": "image", "image/jpeg": "image", "image/gif": "image", "image/webp": "image", "image/svg+xml": "image",
	"video/mp4": "video", "video/webm": "video", "video/quicktime": "video",
	"audio/mpeg": "audio", "audio/wave": "audio", "audio/mp4": "audio", "audio/ogg": "audio",
	"font/ttf": "font", "font/otf": "font", "font/woff": "font", "font/woff2": "font",
}

// Inspect works out what data is from its bytes. filename only settles
// what the bytes can't: SVG is text, and QuickTime and M4A share MP4's
// container.
func Inspect(filename string, data []byte) (Info, error) {
	if len(data) == 0 {
		return Info{}, fmt.Errorf("%w: the file is empty", ErrType)
	}
	ct := http.DetectContentType(data)
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	ext := strings.ToLower(path.Ext(filename))
	switch {
	case ct == "application/ogg":
		ct = "audio/ogg"
	case ct == "audio/wav" || ct == "audio/x-wav":
		ct = "audio/wave"
	case (ct == "text/xml" || ct == "text/plain" || ct == "text/html") && ext == ".svg" && looksLikeSVG(data):
		ct = "image/svg+xml"
	case ct == "application/octet-stream" && ext == ".mp3" && isMP3Frame(data):
		// An MP3 without an ID3 tag starts straight with a frame.
		ct = "audio/mpeg"
	case isISOMedia(data):
		switch {
		case ext == ".mov" || isoBrand(data) == "qt  ":
			ct = "video/quicktime"
		case ext == ".m4a" || strings.HasPrefix(isoBrand(data), "M4A"):
			ct = "audio/mp4"
		default:
			ct = "video/mp4"
		}
	}
	fam, ok := families[ct]
	if !ok {
		return Info{}, fmt.Errorf("%w: pictures (PNG, JPEG, GIF, WebP, SVG), videos (MP4, WebM, MOV), sound (MP3, WAV, M4A, OGG) and fonts (TTF, OTF, WOFF, WOFF2) only", ErrType)
	}
	info := Info{ContentType: ct, Family: fam}
	if fam == "image" && ct != "image/svg+xml" {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			info.Width, info.Height = cfg.Width, cfg.Height
		}
	}
	return info, nil
}

// Fits reports whether a file of this family may be kept as kind.
func Fits(kind, family string) bool {
	for _, f := range kindFamilies[kind] {
		if f == family {
			return true
		}
	}
	return false
}

// FamiliesFor lists the families kind accepts, for an error message.
func FamiliesFor(kind string) string {
	names := map[string]string{"image": "a picture", "video": "a video", "audio": "a sound file", "font": "a font"}
	var out []string
	for _, f := range kindFamilies[kind] {
		out = append(out, names[f])
	}
	return strings.Join(out, " or ")
}

func looksLikeSVG(data []byte) bool {
	head := data
	if len(head) > 2048 {
		head = head[:2048]
	}
	return bytes.Contains(bytes.ToLower(head), []byte("<svg"))
}

// isMP3Frame reports whether data starts with an MPEG audio frame header:
// eleven sync bits, a real version and layer.
func isMP3Frame(data []byte) bool {
	return len(data) >= 4 && data[0] == 0xFF && data[1]&0xE0 == 0xE0 && data[1]&0x18 != 0x08 && data[1]&0x06 != 0
}

func isISOMedia(data []byte) bool { return len(data) >= 12 && string(data[4:8]) == "ftyp" }
func isoBrand(data []byte) string { return string(data[8:12]) }

// CanResize reports whether Resize can make a smaller copy of a file.
func CanResize(info Info) bool {
	if info.Family != "image" || info.ContentType == "image/svg+xml" {
		return false
	}
	return info.Width > 0 && info.Height > 0 && info.Width*info.Height <= maxPixels
}

// Resize makes a copy of a picture no wider than width, keeping its
// shape. A picture already that narrow is re-encoded at its own size. It
// returns PNG when the picture has see-through parts (a logo) and JPEG
// otherwise, and the copy's type. Animated GIFs give their first frame.
func Resize(data []byte, width int) ([]byte, string, error) {
	if width < 16 || width > 4096 {
		return nil, "", errors.New("assets: width is 16 to 4096")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("assets: not a picture: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, "", errors.New("assets: the picture is too large to resize")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("assets: not a picture: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > width {
		h = max(1, h*width/w)
		w = width
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	var out bytes.Buffer
	if !opaque(dst) {
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&out, dst); err != nil {
			return nil, "", err
		}
		return out.Bytes(), "image/png", nil
	}
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, "", err
	}
	return out.Bytes(), "image/jpeg", nil
}

// Decode reads a picture (PNG, JPEG, GIF or WebP). One too large to
// hold in memory is refused.
func Decode(data []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("assets: not a picture: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, errors.New("assets: the picture is too large to read")
	}
	m, _, err := image.Decode(bytes.NewReader(data))
	return m, err
}

func opaque(m *image.NRGBA) bool {
	for i := 3; i < len(m.Pix); i += 4 {
		if m.Pix[i] != 0xff {
			return false
		}
	}
	return true
}
