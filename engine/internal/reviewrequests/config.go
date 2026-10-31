// Package reviewrequests sends honest review invitations through lifecycle email.
package reviewrequests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

type Platform struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Invitation bool   `json:"invitation"`
	Monitoring bool   `json:"monitoring"`
	Note       string `json:"note"`
}

// Platform capabilities are explicit: a destination is not an attribution integration.
var Platforms = []Platform{
	{"google_reviews", "Google", true, true, "Use the business's write-a-review link."},
	{"trustpilot", "Trustpilot", true, true, "Use the business's invitation link and follow its invitation rules."},
	{"facebook", "Facebook", true, false, "Link to the brand's Recommendations page."},
	{"yelp", "Yelp", false, false, "Yelp discourages asking customers for reviews. Save a reference link; excluded from invitation emails."},
	{"tripadvisor", "Tripadvisor", true, false, "Invite genuine customers using the property's review link."},
	{"g2", "G2", true, true, "Invite genuine users under G2's review guidelines."},
	{"capterra", "Capterra", true, true, "Invite genuine users under Capterra's review guidelines."},
	{"trustradius", "TrustRadius", true, false, "Use an approved review invitation link."},
	{"gartner_peer_insights", "Gartner Peer Insights", true, false, "Use the vendor's approved review sourcing link."},
	{"clutch", "Clutch", true, false, "Use the business's client review link."},
	{"bbb", "BBB", true, false, "Use the business's customer review link."},
	{"angi", "Angi", true, false, "Use an eligible customer's review link."},
	{"houzz", "Houzz", true, false, "Use the professional's review link."},
	{"healthgrades", "Healthgrades", true, false, "Invite genuine patients without including health details in the email."},
	{"zocdoc", "Zocdoc", false, false, "Reviews require Zocdoc's eligible-patient flow. External email invitations are unavailable."},
	{"opentable", "OpenTable", false, false, "Reviews use OpenTable's eligible-diner flow. External email invitations are unavailable."},
	{"booking", "Booking.com", false, false, "Reviews require an eligible booking and Booking.com's invitation flow."},
	{"expedia", "Expedia", false, false, "Reviews require an eligible booking and Expedia's invitation flow."},
	{"app_store", "Apple App Store", true, false, "Use the app's App Store write-review link; follow Apple's review rules."},
	{"google_play", "Google Play", true, false, "Use the app's Google Play listing; follow Google's review rules."},
}

type Destination struct {
	Platform string `json:"platform"`
	URL      string `json:"url"`
}
type Settings struct {
	From           string        `json:"from"`
	ReplyTo        string        `json:"reply_to"`
	Frequency      string        `json:"frequency"`
	IntervalDays   int           `json:"interval_days"`
	FrequencyScope string        `json:"frequency_scope"`
	Destinations   []Destination `json:"destinations"`
}

func Defaults() Settings {
	return Settings{Frequency: "interval", IntervalDays: 90, FrequencyScope: "brand", Destinations: []Destination{}}
}
func LoadSettings(ctx context.Context, q jobs.Querier, ws, brand string) (Settings, error) {
	s := Defaults()
	var raw []byte
	err := q.QueryRow(ctx, `SELECT config FROM review_request_settings WHERE workspace_id=$1 AND brand_id=$2`, ws, brand).Scan(&raw)
	if err == pgx.ErrNoRows {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(raw, &s)
	return s, err
}
func platform(id string) *Platform {
	for i := range Platforms {
		if Platforms[i].ID == id {
			return &Platforms[i]
		}
	}
	return nil
}
func address(v string) bool {
	if v == "" {
		return true
	}
	_, err := mail.ParseAddress(v)
	return err == nil && len(v) <= 320
}
func (s Settings) Check() error {
	if !address(s.From) || !address(s.ReplyTo) {
		return fmt.Errorf("sender and reply-to must be valid email addresses")
	}
	if !slices.Contains([]string{"interval", "once", "unlimited"}, s.Frequency) || !slices.Contains([]string{"brand", "platform"}, s.FrequencyScope) || s.IntervalDays < 1 || s.IntervalDays > 36500 {
		return fmt.Errorf("choose a frequency, scope, and interval of 1 to 36,500 days")
	}
	if len(s.Destinations) > 20 {
		return fmt.Errorf("at most 20 review destinations")
	}
	seen := map[string]bool{}
	for _, d := range s.Destinations {
		u, err := url.Parse(d.URL)
		if platform(d.Platform) == nil || seen[d.Platform] || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(d.URL) > 2000 || strings.Contains(d.URL, "{{") {
			return fmt.Errorf("each platform needs one valid HTTPS review link")
		}
		seen[d.Platform] = true
	}
	return nil
}

type Audience struct {
	Kind      string            `json:"kind"`
	ListID    string            `json:"list_id,omitempty"`
	RecordIDs []string          `json:"record_ids,omitempty"`
	Rules     []smartlists.Rule `json:"rules,omitempty"`
	Match     string            `json:"match,omitempty"`
}
type Config struct {
	Name      string   `json:"name"`
	Mode      string   `json:"mode"`
	From      string   `json:"from"`
	ReplyTo   string   `json:"reply_to"`
	Subject   string   `json:"subject"`
	Body      string   `json:"body"`
	Platforms []string `json:"platforms"`
	Audience  Audience `json:"audience"`
}

func (c Config) Check() error {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 200 || !slices.Contains([]string{"manual", "workflow"}, c.Mode) {
		return fmt.Errorf("provide a name and manual or workflow mode")
	}
	if !address(c.From) || !address(c.ReplyTo) {
		return fmt.Errorf("sender and reply-to must be valid email addresses")
	}
	if strings.TrimSpace(c.Subject) == "" || len(c.Subject) > 500 || strings.TrimSpace(c.Body) == "" || len(c.Body) > 20000 {
		return fmt.Errorf("provide a subject (up to 500 characters) and message (up to 20,000 characters)")
	}
	if strings.Contains(c.Body, "{{{") || strings.Contains(c.Subject, "{{{") {
		return fmt.Errorf("use escaped personalization such as {{person.first_name|there}}")
	}
	if err := emails.Check(c.Subject, "", c.Body); err != nil {
		return err
	}
	if len(c.Platforms) == 0 || len(c.Platforms) > 20 {
		return fmt.Errorf("choose at least one review destination")
	}
	seen := map[string]bool{}
	for _, id := range c.Platforms {
		p := platform(id)
		if p == nil || !p.Invitation || seen[id] {
			return fmt.Errorf("choose unique platforms that support email invitations")
		}
		seen[id] = true
	}
	if c.Mode == "workflow" {
		return nil
	}
	switch c.Audience.Kind {
	case "list":
		if c.Audience.ListID == "" {
			return fmt.Errorf("choose a customer list")
		}
	case "rules":
		if len(c.Audience.Rules) == 0 {
			return fmt.Errorf("provide at least one audience rule")
		}
		if msg := smartlists.Check(c.Audience.Rules, c.Audience.Match); msg != "" {
			return fmt.Errorf("%s", msg)
		}
	case "records":
		if len(c.Audience.RecordIDs) == 0 || len(c.Audience.RecordIDs) > 10000 {
			return fmt.Errorf("select 1 to 10,000 customers")
		}
	default:
		return fmt.Errorf("choose a customer list, rules, or selected customer records")
	}
	return nil
}
func (c Config) Destinations(s Settings) ([]Destination, error) {
	out := []Destination{}
	for _, id := range c.Platforms {
		found := false
		for _, d := range s.Destinations {
			if d.Platform == id {
				out = append(out, d)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("add the %s review link in brand settings", id)
		}
	}
	return out, nil
}
