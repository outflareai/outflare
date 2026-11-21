package api_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

func testPNG(t *testing.T, w, h int, shade uint8) string {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: shade, G: uint8(x), B: uint8(y), A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// A brand's files: uploaded once, typed from their bytes, with smaller
// copies of pictures, consent for people and voices, and nothing of
// another brand's.
func TestBrandAssets(t *testing.T) {
	got := track.Capture(t)
	bucket := &memBlobs{m: map[string][]byte{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Files = bucket })
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand + "/assets"

	// A logo: its type and size come from the bytes, its name from the file.
	logo := owner.must(201, "POST", base, map[string]any{"kind": "logo", "filename": "C:\\art\\Acme Logo.png", "data": testPNG(t, 800, 400, 10), "tags": []string{" Dark ", "dark", "wide"}})
	if logo["name"] != "Acme Logo" || logo["filename"] != "Acme Logo.png" || logo["content_type"] != "image/png" || logo["family"] != "image" ||
		logo["width"] != float64(800) || logo["height"] != float64(400) || logo["resizable"] != true || logo["usable"] != true || logo["source"] != "upload" {
		t.Fatalf("logo: %v", logo)
	}
	if tags := logo["tags"].([]any); len(tags) != 2 || tags[0] != "dark" || tags[1] != "wide" {
		t.Fatalf("tags: %v", tags)
	}
	id := logo["id"].(string)
	// The original and the list's small copy are in the bucket.
	if bucket.len() != 2 {
		t.Fatalf("bucket has %d files", bucket.len())
	}

	// The same file again is the same asset, not a second one.
	if again := owner.must(200, "POST", base, map[string]any{"kind": "logo", "filename": "copy.png", "data": testPNG(t, 800, 400, 10)}); again["id"] != id {
		t.Fatalf("duplicate: %v", again)
	}
	if bucket.len() != 2 {
		t.Fatalf("bucket after duplicate has %d files", bucket.len())
	}

	// What isn't accepted, and why.
	owner.must(400, "POST", base, map[string]any{"kind": "banner", "filename": "a.png", "data": testPNG(t, 4, 4, 1)})
	owner.must(400, "POST", base, map[string]any{"kind": "logo", "filename": "a.png", "data": "not base64!"})
	owner.must(400, "POST", base, map[string]any{"kind": "logo", "filename": "run.png", "data": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nrm -rf /\n"))})
	if b := owner.must(400, "POST", base, map[string]any{"kind": "font", "filename": "a.png", "data": testPNG(t, 4, 4, 2)}); !strings.Contains(b["error"].(map[string]any)["message"].(string), "a font") {
		t.Fatalf("font refusal: %v", b)
	}
	owner.must(400, "POST", base, map[string]any{"kind": "logo", "filename": "a.png", "data": testPNG(t, 4, 4, 3), "consent": map[string]any{"confirmed": true}})

	// The file, and a smaller copy made on first ask.
	code, h, body := owner.download(base + "/" + id + "/file")
	if code != 200 || h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("file: %d %v", code, h)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(body)); err != nil || cfg.Width != 800 {
		t.Fatalf("file body: %v %v", cfg, err)
	}
	code, h, body = owner.download(base + "/" + id + "/file?width=240")
	if code != 200 || h.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("copy: %d %v", code, h)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(body)); err != nil || cfg.Width != 240 || cfg.Height != 120 {
		t.Fatalf("copy body: %v %v", cfg, err)
	}
	if bucket.len() != 3 {
		t.Fatalf("bucket after copy has %d files", bucket.len())
	}
	if code, _, _ := owner.download(base + "/" + id + "/file?width=333"); code != 400 {
		t.Fatalf("odd width: %d", code)
	}

	// A person's picture can't be used until someone says they agreed.
	person := owner.must(201, "POST", base, map[string]any{"kind": "person", "name": "Ana, presenter", "filename": "ana.png", "data": testPNG(t, 64, 64, 77)})
	consent := person["consent"].(map[string]any)
	if person["usable"] != false || consent["needed"] != true || consent["confirmed"] != false {
		t.Fatalf("person before consent: %v", person)
	}
	pid := person["id"].(string)
	if list := owner.must(200, "GET", base+"?usable=true", nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("usable list: %v", list)
	}
	person = owner.must(200, "PATCH", base+"/"+pid, map[string]any{"consent": map[string]any{"confirmed": true, "note": "Signed release, 3 Nov"}})
	consent = person["consent"].(map[string]any)
	if person["usable"] != true || consent["confirmed"] != true || consent["at"] == nil || consent["by"] == nil || consent["note"] != "Signed release, 3 Nov" {
		t.Fatalf("person after consent: %v", person)
	}
	// Taking it back makes the file unusable again.
	if person = owner.must(200, "PATCH", base+"/"+pid, map[string]any{"consent": map[string]any{"confirmed": false}}); person["usable"] != false {
		t.Fatalf("consent withdrawn: %v", person)
	}

	// Renaming, a stale version, and a kind the file doesn't fit.
	v := owner.must(200, "PATCH", base+"/"+id, map[string]any{"name": " Main logo ", "note": "Use on dark backgrounds", "version": 1})
	if v["name"] != "Main logo" || v["version"] != float64(2) {
		t.Fatalf("rename: %v", v)
	}
	owner.must(409, "PATCH", base+"/"+id, map[string]any{"name": "x", "version": 1})
	owner.must(400, "PATCH", base+"/"+id, map[string]any{"kind": "voice"})

	// Lists: by kind, by search, archived apart.
	if list := owner.must(200, "GET", base+"?kind=logo", nil); len(list["data"].([]any)) != 1 || list["storage"] != true {
		t.Fatalf("logos: %v", list)
	}
	if list := owner.must(200, "GET", base+"?q=wide", nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("search by tag: %v", list)
	}
	owner.must(400, "GET", base+"?kind=banner", nil)
	if v := owner.must(200, "POST", base+"/"+id+"/archive", nil); v["archived_at"] == nil || v["usable"] != false {
		t.Fatalf("archive: %v", v)
	}
	if list := owner.must(200, "GET", base, nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("after archive: %v", list)
	}
	if list := owner.must(200, "GET", base+"?archived=true", nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("archived: %v", list)
	}
	owner.must(200, "POST", base+"/"+id+"/restore", nil)

	// Another brand sees and reaches none of it.
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Summitly"})["id"].(string)
	if list := owner.must(200, "GET", "/v1/brands/"+other+"/assets", nil)["data"].([]any); len(list) != 0 {
		t.Fatalf("other brand's list: %v", list)
	}
	owner.must(404, "GET", "/v1/brands/"+other+"/assets/"+id, nil)
	if code, _, _ := owner.download("/v1/brands/" + other + "/assets/" + id + "/file"); code != 404 {
		t.Fatalf("other brand's file: %d", code)
	}
	owner.must(404, "DELETE", "/v1/brands/"+other+"/assets/"+id, nil)

	// Deleting takes the row, the file and its copies.
	owner.must(204, "DELETE", base+"/"+id, nil)
	owner.must(404, "GET", base+"/"+id, nil)
	if bucket.len() != 2 { // the person's original and small copy
		t.Fatalf("bucket after delete has %d files", bucket.len())
	}

	want := map[string]int{"asset.uploaded": 2, "asset.consent_confirmed": 1, "asset.consent_withdrawn": 1, "asset.archived": 1, "asset.restored": 1, "asset.deleted": 1}
	seen := map[string]int{}
	for _, ev := range got() {
		if _, ok := want[ev.Name]; ok {
			seen[ev.Name]++
			for k := range ev.Props {
				if k == "name" || k == "filename" || k == "note" {
					t.Fatalf("%s carries %s", ev.Name, k)
				}
			}
		}
	}
	for name, n := range want {
		if seen[name] != n {
			t.Fatalf("%s events: %d, want %d (all: %v)", name, seen[name], n, seen)
		}
	}
}

// Without object storage the list says so and uploads are refused.
func TestBrandAssetsNeedStorage(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand + "/assets"
	if list := owner.must(200, "GET", base, nil); list["storage"] != false || len(list["data"].([]any)) != 0 {
		t.Fatalf("list: %v", list)
	}
	b := owner.must(503, "POST", base, map[string]any{"kind": "logo", "filename": "a.png", "data": testPNG(t, 4, 4, 1)})
	if b["error"].(map[string]any)["code"] != "not_configured" {
		t.Fatalf("upload: %v", b)
	}
}
