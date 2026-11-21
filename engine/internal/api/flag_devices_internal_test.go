package api

import (
	"encoding/json"
	"os"
	"testing"
)

// The server libraries decide device rules in their own memory, so they
// read a device from a User-Agent exactly as parseUA does: their tests
// read the same file.
func TestFlagDevicesMatchParseUA(t *testing.T) {
	raw, err := os.ReadFile("../flags/testdata/devices.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		UserAgent string `json:"user_agent"`
		Device    string `json:"device"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("devices.json: %v", err)
	}
	for _, c := range cases {
		if got, _, _ := parseUA(c.UserAgent); got != c.Device {
			t.Errorf("%q: parseUA gives %q, devices.json says %q", c.UserAgent, got, c.Device)
		}
	}
}
