package crawl

import (
	"net/url"
	"slices"
	"testing"
)

func TestReadLook(t *testing.T) {
	base, _ := url.Parse("https://www.northpeak.example/")
	page := `<html><head>
		<link rel="mask-icon" href="/pin.svg">
		<link rel="icon" sizes="16x16" href="/icon-16.png">
		<link rel="icon" sizes="192x192" href="/icon-192.png">
		<link rel="apple-touch-icon" href="/apple.png">
		<link rel="icon" type="image/svg+xml" href="/icon.svg">
		<link rel="icon" href="data:image/png;base64,AAAA">
		<meta name="theme-color" content="#0F766E">
		<script type="application/ld+json">{"@type":"Organization","logo":{"@type":"ImageObject","url":"http://cdn.northpeak.example/logo.png"}}</script>
		</head><body><link rel="icon" href="/late.png"></body></html>`
	got := ReadLook(base, []byte(page))
	want := []string{
		"https://cdn.northpeak.example/logo.png",
		"https://www.northpeak.example/icon.svg",
		"https://www.northpeak.example/apple.png",
		"https://www.northpeak.example/icon-192.png",
		"https://www.northpeak.example/favicon.ico",
	}
	if !slices.Equal(got.Logos, want) || got.ThemeColor != "#0f766e" {
		t.Fatalf("look: %+v", got)
	}
	// A bare page still offers /favicon.ico; a named colour is not a hex.
	got = ReadLook(base, []byte(`<html><head><meta name="theme-color" content="teal"><script type="application/ld+json">{"logo":"/brand.png"}</script></head></html>`))
	if !slices.Equal(got.Logos, []string{"https://www.northpeak.example/brand.png", "https://www.northpeak.example/favicon.ico"}) || got.ThemeColor != "" {
		t.Fatalf("bare look: %+v", got)
	}
}
