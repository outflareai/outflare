package api

import (
	"net/url"
	"regexp"
	"strings"
)

// A company's look, in the Company hub: its logo and colours, kept with
// the company context and, for a brand that looks different, with the
// brand. The consent banner's presets start from it; a brand's look wins
// over the company's wherever it is set.

// lookColors are hex colours (#rgb or #rrggbb); "" for none.
type lookColors struct {
	// Buttons and links.
	Primary string `json:"primary"`
	// Panels such as the banner.
	Background string `json:"background"`
	// Text on the background.
	Text string `json:"text"`
}

var lookColor = regexp.MustCompile(`^#([0-9a-f]{3}|[0-9a-f]{6})$`)

func (c *lookColors) clean() error {
	for _, p := range []*string{&c.Primary, &c.Background, &c.Text} {
		*p = strings.ToLower(strings.TrimSpace(*p))
		if *p != "" && !lookColor.MatchString(*p) {
			return errInvalid("colors are hex colours such as #1d4ed8 or #fff, or empty for none")
		}
	}
	return nil
}

// or is c with the colours it lacks taken from d.
func (c lookColors) or(d lookColors) lookColors {
	if c.Primary == "" {
		c.Primary = d.Primary
	}
	if c.Background == "" {
		c.Background = d.Background
	}
	if c.Text == "" {
		c.Text = d.Text
	}
	return c
}

// cleanLogoURL checks a logo's address: https, or "" for none.
func cleanLogoURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(raw) > 2000 {
		return "", errInvalid("logo_url is an https address of an image, such as https://northpeak.example/logo.svg, or empty for none")
	}
	return u.String(), nil
}
