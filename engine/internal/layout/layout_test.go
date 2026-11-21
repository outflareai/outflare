package layout

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
)

func flat(w, h int, c color.NRGBA) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return m
}

func TestShapeAndColor(t *testing.T) {
	for size, want := range map[string]float64{"1:1": 1, "4:5": 0.8, "16:9": 16.0 / 9, "1.91:1": 1.91} {
		if got, ok := Shape(size); !ok || got < want-0.001 || got > want+0.001 {
			t.Fatalf("Shape(%q) = %v, %v", size, got, ok)
		}
	}
	for _, bad := range []string{"", "square", "0:1", "10:1", "1:4", "4:"} {
		if _, ok := Shape(bad); ok {
			t.Fatalf("Shape(%q) was accepted", bad)
		}
	}
	if c, ok := HexColor("#0a8"); !ok || c != (color.NRGBA{0x00, 0xaa, 0x88, 255}) {
		t.Fatalf("HexColor: %v %v", c, ok)
	}
	if _, ok := HexColor("green"); ok {
		t.Fatal("HexColor took a word")
	}
}

// The words are drawn over the picture in every shape, and the picture
// shows through where there are none.
func TestRender(t *testing.T) {
	blue := color.NRGBA{20, 60, 200, 255}
	green := color.NRGBA{0, 170, 90, 255}
	ad := Ad{Picture: flat(800, 600, blue), Logo: flat(120, 40, color.NRGBA{255, 255, 255, 255}),
		Headline: "Blocked again? Not with a network that gets through every single time you ask",
		Subline:  "99.9% of requests succeed.", CTA: "Start free", Accent: &green}
	for size, wantH := range map[string]int{"1:1": 1080, "4:5": 1350, "9:16": 1920, "16:9": 608} {
		shape, _ := Shape(size)
		m, err := Render(ad, shape, 1080)
		if err != nil {
			t.Fatal(err)
		}
		if b := m.Bounds(); b.Dx() != 1080 || b.Dy() != wantH {
			t.Fatalf("%s: %v", size, b)
		}
		// The middle is the picture, untouched.
		if c := m.NRGBAAt(540, wantH*62/100); c != blue {
			t.Fatalf("%s: middle is %v", size, c)
		}
		// Words: white pixels in the top half; the button: its accent near the bottom left.
		white, accent := 0, 0
		for y := 0; y < wantH; y++ {
			for x := 0; x < 1080; x++ {
				switch c := m.NRGBAAt(x, y); {
				case y < wantH/2 && c.R > 240 && c.G > 240 && c.B > 240:
					white++
				case y > wantH*3/4 && c == green:
					accent++
				}
			}
		}
		if white < 2000 || accent < 2000 {
			t.Fatalf("%s: %d white and %d accent pixels", size, white, accent)
		}
		if dir := os.Getenv("OUTFLARE_TEST_SHOTS"); dir != "" {
			var out bytes.Buffer
			_ = png.Encode(&out, m)
			_ = os.WriteFile(dir+"/layout-"+size[:1]+"x"+size[len(size)-1:]+".png", out.Bytes(), 0o644)
		}
	}

	// No words, no change: the picture alone.
	m, err := Render(Ad{Picture: flat(400, 400, blue)}, 1, 400)
	if err != nil || m.NRGBAAt(10, 10) != blue || m.NRGBAAt(390, 390) != blue {
		t.Fatalf("bare picture: %v %v", err, m.NRGBAAt(10, 10))
	}
	// A font that can't be read falls back to the built-in one.
	if _, err := Render(Ad{Picture: flat(400, 400, blue), Headline: "Hello", Font: []byte("not a font")}, 1, 400); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(ad, 1, 100); err == nil {
		t.Fatal("a width below the minimum was drawn")
	}
	if _, err := Render(Ad{}, 1, 400); err == nil {
		t.Fatal("an ad with no picture was drawn")
	}
	for _, f := range []string{"jpeg", "png"} {
		if data, ct, err := Encode(m, f); err != nil || len(data) == 0 || ct != "image/"+f {
			t.Fatalf("encode %s: %v %s", f, err, ct)
		}
	}
}
