package logos

import (
	"bytes"
	"encoding/xml"
	"io"
	"io/fs"
	"strings"
	"testing"
)

func TestFilesAreSafeSVG(t *testing.T) {
	names, _ := fs.Glob(files, "svg/*.svg")
	if len(names) == 0 {
		t.Fatal("no logos embedded")
	}
	for _, n := range names {
		b, _ := files.ReadFile(n)
		low := strings.ToLower(string(b))
		for _, bad := range []string{"<script", "javascript:", "onload=", "onerror=", "<foreignobject", "href=\"http"} {
			if strings.Contains(low, bad) {
				t.Errorf("%s contains %q", n, bad)
			}
		}
		// A vendor that publishes no vector mark gets its own icon PNG,
		// embedded: never a link to another file or another host.
		if strings.Count(low, "<image") != strings.Count(low, `href="data:image/png;base64,`) {
			t.Errorf("%s: an <image> must embed a PNG", n)
		}
		if !strings.HasPrefix(low, "<svg") || !strings.Contains(low, "viewbox=") {
			t.Errorf("%s: want a bare <svg> with a viewBox", n)
		}
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := d.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Errorf("%s: %v", n, err)
				break
			}
		}
	}
}

func TestGet(t *testing.T) {
	if b, ok := Get("hubspot_private_app", "HubSpot (private app token)", false); !ok || !bytes.Contains(b, []byte("<svg")) {
		t.Error("aliases share the company mark")
	}
	if a, _ := Get("cheapverifier", "", false); !bytes.Equal(a, must(Get("cheapinboxes", "", false))) {
		t.Error("Cheap Verifier uses the Cheap Inboxes mark")
	}
	light, _ := Get("vercel", "Vercel", false)
	dark, _ := Get("vercel", "Vercel", true)
	if bytes.Equal(light, dark) {
		t.Error("vercel has a dark variant")
	}
	if a, _ := Get("hubspot", "", true); !bytes.Equal(a, must(Get("hubspot", "", false))) {
		t.Error("no dark file falls back to the light mark")
	}
	b, ok := Get("unknown_tool", "Smartlead", false)
	if ok || !bytes.Contains(b, []byte(">S</text>")) {
		t.Errorf("monogram: %s", b)
	}
}

func must(b []byte, _ bool) []byte { return b }

func TestInitials(t *testing.T) {
	for in, want := range map[string]string{"People Data Labs": "PD", "Salesforce (sandbox)": "S", "Cal.com": "C", "EmailBison": "EB", "Smartlead": "S", "builtin": "B", "": ""} {
		if got := Initials(in); got != want {
			t.Errorf("Initials(%q) = %q, want %q", in, got, want)
		}
	}
}
