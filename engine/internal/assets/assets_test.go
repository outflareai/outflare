package assets

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func pngOf(t *testing.T, w, h int, alpha uint8) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 90, A: alpha})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestInspect(t *testing.T) {
	pic := pngOf(t, 40, 20, 255)
	info, err := Inspect("anything.exe", pic)
	if err != nil || info.ContentType != "image/png" || info.Family != "image" || info.Width != 40 || info.Height != 20 {
		t.Fatalf("png: %+v %v", info, err)
	}
	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`)
	if info, err := Inspect("logo.svg", svg); err != nil || info.ContentType != "image/svg+xml" || CanResize(info) {
		t.Fatalf("svg: %+v %v", info, err)
	}
	// The name alone never makes a file a picture.
	if _, err := Inspect("logo.svg", []byte("#!/bin/sh\necho hi\n")); !errors.Is(err, ErrType) {
		t.Fatalf("script named .svg: %v", err)
	}
	if _, err := Inspect("page.html", []byte("<html><svg></svg></html>")); !errors.Is(err, ErrType) {
		t.Fatalf("html: %v", err)
	}
	if _, err := Inspect("a.png", nil); !errors.Is(err, ErrType) {
		t.Fatalf("empty: %v", err)
	}
	mov := append([]byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'q', 't', ' ', ' '}, make([]byte, 32)...)
	if info, err := Inspect("clip.mov", mov); err != nil || info.ContentType != "video/quicktime" || info.Family != "video" {
		t.Fatalf("mov: %+v %v", info, err)
	}
	m4a := append([]byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'M', '4', 'A', ' '}, make([]byte, 32)...)
	if info, err := Inspect("voice.m4a", m4a); err != nil || info.ContentType != "audio/mp4" {
		t.Fatalf("m4a: %+v %v", info, err)
	}
}

func TestFits(t *testing.T) {
	for _, c := range []struct {
		kind, family string
		want         bool
	}{
		{"logo", "image", true}, {"logo", "video", false}, {"font", "font", true}, {"font", "image", false},
		{"voice", "audio", true}, {"voice", "image", false}, {"person", "video", true}, {"other", "font", true},
		{"nonsense", "image", false},
	} {
		if got := Fits(c.kind, c.family); got != c.want {
			t.Errorf("Fits(%s, %s) = %v", c.kind, c.family, got)
		}
	}
	if !NeedsConsent("voice") || !NeedsConsent("person") || NeedsConsent("logo") {
		t.Error("NeedsConsent")
	}
}

func TestResize(t *testing.T) {
	out, ct, err := Resize(pngOf(t, 400, 200, 255), 100)
	if err != nil || ct != "image/jpeg" {
		t.Fatalf("opaque: %s %v", ct, err)
	}
	m, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil || m.Bounds().Dx() != 100 || m.Bounds().Dy() != 50 {
		t.Fatalf("opaque size: %v %v", m.Bounds(), err)
	}
	// See-through pictures stay PNG, and small ones are never enlarged.
	out, ct, err = Resize(pngOf(t, 40, 20, 128), 480)
	if err != nil || ct != "image/png" {
		t.Fatalf("see-through: %s %v", ct, err)
	}
	p, err := png.Decode(bytes.NewReader(out))
	if err != nil || p.Bounds().Dx() != 40 {
		t.Fatalf("see-through size: %v %v", p.Bounds(), err)
	}
	if _, _, err := Resize([]byte("not a picture"), 100); err == nil {
		t.Fatal("junk resized")
	}
	if _, _, err := Resize(pngOf(t, 10, 10, 255), 5); err == nil {
		t.Fatal("width 5 accepted")
	}
}
