// Package layout lays an ad's words over its picture: the headline, a
// second line, a button and the brand's logo, in any shape. The picture
// itself carries no text, so the words can change and the ad can be
// made in another size without making (and paying for) a new picture.
package layout

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Limits on what is drawn.
const (
	MinWidth = 240
	MaxWidth = 2160
)

// Ad is what goes on one picture.
type Ad struct {
	// Picture fills the whole ad, cropped around its middle.
	Picture image.Image
	// Logo sits in the top left corner; nil for none.
	Logo image.Image
	// Headline, Subline and CTA are the words; any may be empty.
	Headline, Subline, CTA string
	// Font is a TrueType or OpenType font for every word; nil, or one
	// that can't be read, uses the built-in one.
	Font []byte
	// Accent colours the button; nil gives a white one.
	Accent *color.NRGBA
}

// Shape reads a size such as 4:5 as width over height. Shapes more than
// three times as wide as tall, or as tall as wide, are refused.
func Shape(size string) (float64, bool) {
	a, b, ok := strings.Cut(strings.TrimSpace(size), ":")
	if !ok {
		return 0, false
	}
	w, err1 := strconv.ParseFloat(a, 64)
	h, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 || w/h > 3 || h/w > 3 {
		return 0, false
	}
	return w / h, true
}

// HexColor reads #rgb or #rrggbb.
func HexColor(s string) (color.NRGBA, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return color.NRGBA{}, false
	}
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{R: uint8(n >> 16), G: uint8(n >> 8), B: uint8(n), A: 255}, true
}

// Render draws the ad in the given shape (width over height), width
// pixels wide.
func Render(ad Ad, shape float64, width int) (*image.NRGBA, error) {
	if ad.Picture == nil {
		return nil, errors.New("layout: no picture")
	}
	if width < MinWidth || width > MaxWidth || shape <= 0 {
		return nil, errors.New("layout: width is " + strconv.Itoa(MinWidth) + " to " + strconv.Itoa(MaxWidth))
	}
	height := int(math.Round(float64(width) / shape))
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	cover(dst, ad.Picture)

	bold, regular := faces(ad.Font)
	margin := width * 6 / 100
	inner := width - 2*margin
	hasTop := ad.Headline != "" || ad.Subline != "" || ad.Logo != nil
	if hasTop {
		shade(dst, 0, height*55/100, 0.68, 0)
	}
	y := margin
	if ad.Logo != nil {
		y += logo(dst, ad.Logo, margin, y, width*7/100, width*40/100) + margin*6/10
	}
	white := image.NewUniform(color.White)
	if ad.Headline != "" {
		// Shrink the headline until it fits in three lines.
		size := float64(width) * 0.085
		lines, face := []string(nil), font.Face(nil)
		for {
			face = newFace(bold, size)
			lines = wrap(face, ad.Headline, inner)
			if len(lines) <= 3 || size <= float64(width)*0.05 {
				break
			}
			size *= 0.92
		}
		y = text(dst, face, white, lines, margin, y, 1.12, 4)
		y += int(size * 0.35)
	}
	if ad.Subline != "" {
		face := newFace(regular, float64(width)*0.042)
		y = text(dst, face, image.NewUniform(color.NRGBA{255, 255, 255, 235}), wrap(face, ad.Subline, inner), margin, y, 1.25, 3)
	}
	if ad.CTA != "" {
		shade(dst, height*72/100, height, 0, 0.5)
		button(dst, newFace(bold, float64(width)*0.036), ad.CTA, margin, height-margin, inner, ad.Accent)
	}
	return dst, nil
}

// Encode writes a rendered ad as JPEG (format "jpeg") or PNG.
func Encode(m image.Image, format string) ([]byte, string, error) {
	var out bytes.Buffer
	if format == "png" {
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		err := enc.Encode(&out, m)
		return out.Bytes(), "image/png", err
	}
	err := jpeg.Encode(&out, m, &jpeg.Options{Quality: 90})
	return out.Bytes(), "image/jpeg", err
}

// cover fills dst with src, scaled to cover it and cropped around the
// middle.
func cover(dst *image.NRGBA, src image.Image) {
	sb, db := src.Bounds(), dst.Bounds()
	sw, sh := float64(sb.Dx()), float64(sb.Dy())
	want := float64(db.Dx()) / float64(db.Dy())
	crop := sb
	if sw/sh > want {
		w := int(math.Round(sh * want))
		crop.Min.X = sb.Min.X + (sb.Dx()-w)/2
		crop.Max.X = crop.Min.X + w
	} else {
		h := int(math.Round(sw / want))
		crop.Min.Y = sb.Min.Y + (sb.Dy()-h)/2
		crop.Max.Y = crop.Min.Y + h
	}
	draw.CatmullRom.Scale(dst, db, src, crop, draw.Src, nil)
}

// shade darkens the rows from y0 to y1, from strength a0 at the top to
// a1 at the bottom, so white words stay readable on any picture.
func shade(dst *image.NRGBA, y0, y1 int, a0, a1 float64) {
	b := dst.Bounds()
	for y := max(y0, 0); y < min(y1, b.Dy()); y++ {
		t := float64(y-y0) / float64(max(y1-y0, 1))
		keep := 1 - (a0 + (a1-a0)*t)
		row := dst.Pix[y*dst.Stride : y*dst.Stride+b.Dx()*4]
		for i := 0; i < len(row); i += 4 {
			row[i] = uint8(float64(row[i]) * keep)
			row[i+1] = uint8(float64(row[i+1]) * keep)
			row[i+2] = uint8(float64(row[i+2]) * keep)
		}
	}
}

// logo draws the logo h pixels high (narrower when it would pass maxW)
// and returns the height it took.
func logo(dst *image.NRGBA, src image.Image, x, y, h, maxW int) int {
	sb := src.Bounds()
	if sb.Dx() == 0 || sb.Dy() == 0 {
		return 0
	}
	w := sb.Dx() * h / sb.Dy()
	if w > maxW {
		w, h = maxW, sb.Dy()*maxW/sb.Dx()
	}
	draw.CatmullRom.Scale(dst, image.Rect(x, y, x+max(w, 1), y+max(h, 1)), src, sb, draw.Over, nil)
	return h
}

func faces(custom []byte) (bold, regular *opentype.Font) {
	if len(custom) > 0 {
		if f, err := opentype.Parse(custom); err == nil {
			return f, f
		}
	}
	bold, _ = opentype.Parse(gobold.TTF)
	regular, _ = opentype.Parse(goregular.TTF)
	return bold, regular
}

func newFace(f *opentype.Font, size float64) font.Face {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		fallback, _ := opentype.Parse(goregular.TTF)
		face, _ = opentype.NewFace(fallback, &opentype.FaceOptions{Size: size, DPI: 72})
	}
	return face
}

// wrap breaks words into lines no wider than width.
func wrap(face font.Face, s string, width int) []string {
	lines, line := []string{}, ""
	for _, word := range strings.Fields(s) {
		try := word
		if line != "" {
			try = line + " " + word
		}
		if line != "" && font.MeasureString(face, try).Ceil() > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line = try
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// text draws up to most lines from the top at y and returns the y below
// them. Lines past most are dropped, the last one ending in an ellipsis.
func text(dst *image.NRGBA, face font.Face, src image.Image, lines []string, x, y int, spacing float64, most int) int {
	if len(lines) > most {
		lines = append(lines[:most-1:most-1], lines[most-1]+"…")
	}
	m := face.Metrics()
	step := int(float64((m.Ascent + m.Descent).Ceil()) * spacing)
	d := font.Drawer{Dst: dst, Src: src, Face: face}
	for _, l := range lines {
		d.Dot = fixed.P(x, y+m.Ascent.Ceil())
		d.DrawString(l)
		y += step
	}
	return y
}

// button draws the call to action as a pill whose bottom left corner is
// at (x, bottom).
func button(dst *image.NRGBA, face font.Face, label string, x, bottom, maxW int, accent *color.NRGBA) {
	m := face.Metrics()
	th := (m.Ascent + m.Descent).Ceil()
	padX, padY := th*9/10, th*5/10
	tw := font.MeasureString(face, label).Ceil()
	for tw+2*padX > maxW && len(label) > 1 {
		r := []rune(label)
		label = string(r[:len(r)-1])
		tw = font.MeasureString(face, label+"…").Ceil()
		if tw+2*padX <= maxW {
			label += "…"
		}
	}
	fill, ink := color.NRGBA{255, 255, 255, 255}, color.NRGBA{17, 17, 17, 255}
	if accent != nil {
		fill = *accent
		// Dark words on a light button, light words on a dark one.
		if 0.299*float64(fill.R)+0.587*float64(fill.G)+0.114*float64(fill.B) < 150 {
			ink = color.NRGBA{255, 255, 255, 255}
		}
	}
	h := th + 2*padY
	pill(dst, image.Rect(x, bottom-h, x+tw+2*padX, bottom), fill)
	d := font.Drawer{Dst: dst, Src: image.NewUniform(ink), Face: face, Dot: fixed.P(x+padX, bottom-h+padY+m.Ascent.Ceil())}
	d.DrawString(label)
}

// pill fills a rectangle with fully rounded ends, with soft edges.
func pill(dst *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	rad := float64(r.Dy()) / 2
	cy := float64(r.Min.Y) + rad
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if !image.Pt(x, y).In(dst.Bounds()) {
				continue
			}
			// Distance from the pill's spine, a segment between the two
			// end circles' centres.
			px, py := float64(x)+0.5, float64(y)+0.5
			nx := math.Min(math.Max(px, float64(r.Min.X)+rad), float64(r.Max.X)-rad)
			cover := math.Min(math.Max(rad-math.Hypot(px-nx, py-cy)+0.5, 0), 1)
			if cover == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			a := cover * float64(c.A) / 255
			dst.Pix[i] = uint8(float64(c.R)*a + float64(dst.Pix[i])*(1-a))
			dst.Pix[i+1] = uint8(float64(c.G)*a + float64(dst.Pix[i+1])*(1-a))
			dst.Pix[i+2] = uint8(float64(c.B)*a + float64(dst.Pix[i+2])*(1-a))
			dst.Pix[i+3] = 255
		}
	}
}
