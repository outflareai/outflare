package linkbuild

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/outflareai/outflare/engine/internal/netguard"
)

// Archive finds the closest snapshot of a URL.
type Archive interface {
	// Closest is the snapshot's URL and when it was taken (RFC 3339), or
	// "" when there is none.
	Closest(ctx context.Context, raw string) (string, string, error)
}

// waybackAPI is the Internet Archive's free availability API.
const waybackAPI = "https://archive.org/wayback/available"

// wayback asks the Internet Archive. Test workspaces get a made-up
// snapshot, so they never reach it.
type wayback struct{ test bool }

func (w wayback) Closest(ctx context.Context, raw string) (string, string, error) {
	if w.test {
		return "https://web.archive.org/web/20240101000000/" + raw, "2024-01-01T00:00:00Z", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, waybackAPI+"?url="+url.QueryEscape(raw), nil)
	if err != nil {
		return "", "", err
	}
	resp, err := netguard.Client(10 * time.Second).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var out struct {
		Snapshots struct {
			Closest struct {
				Available bool   `json:"available"`
				URL       string `json:"url"`
				Timestamp string `json:"timestamp"`
			} `json:"closest"`
		} `json:"archived_snapshots"`
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("the Internet Archive answered %d", resp.StatusCode)
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<16)).Decode(&out); err != nil {
		return "", "", err
	}
	c := out.Snapshots.Closest
	if !c.Available || c.URL == "" {
		return "", "", nil
	}
	at := ""
	if t, err := time.Parse("20060102150405", c.Timestamp); err == nil {
		at = t.UTC().Format(time.RFC3339)
	}
	return c.URL, at, nil
}
