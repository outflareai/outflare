package deploys

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const vercelAPI = "https://api.vercel.com"

// Vercel watches projects' production deployments.
//
// It connects with an access token: Vercel's own sign-in for apps is an
// Integration with a Marketplace listing, so a pasted token is the way
// every server can offer.
type Vercel struct{}

func (Vercel) Definition() c.Definition {
	return c.Definition{
		ID: "vercel", Name: "Vercel", Category: c.CategoryCode,
		Summary: "Know when your website's Vercel project goes live, so fixes are re-checked and experiments start on the real deploy.",
		Description: "Reads your projects' production deployments and the domains they serve. " +
			"Outflare never deploys, promotes, rolls back or changes settings through it.",
		Website: "https://vercel.com", DocsURL: "https://vercel.com/docs/rest-api/reference/endpoints/deployments/list-deployments",
		KeyURL: "https://vercel.com/account/settings/tokens",
		Auth:   c.AuthAPIKey,
		Fields: []c.Field{
			{Name: "api_key", Label: "Access token", Secret: true,
				Help: "On Vercel: Account Settings, Tokens, Create. Scope it to the team that owns the website."},
			{Name: "team_id", Label: "Team ID", Optional: true, Placeholder: "team_…",
				Help: "For a team's projects: Team Settings, General, Team ID. Leave empty for your own projects."},
		},
		Capabilities: []string{c.CapDeploys},
		Pricing:      "Works on every Vercel plan, including Hobby",
	}
}

func (Vercel) call(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	if q == nil {
		q = url.Values{}
	}
	if t := strings.TrimSpace(creds["team_id"]); t != "" {
		q.Set("teamId", t)
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return get(ctx, cl, "Vercel", "vercel", vercelAPI, path, c.Token(creds), out, nil)
}

func (v Vercel) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var u struct {
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"user"`
	}
	if err := v.call(ctx, cl, creds, "/v2/user", nil, &u); err != nil {
		return c.Account{}, err
	}
	a := c.Account{ID: u.User.ID, Name: u.User.Username, Email: u.User.Email}
	if t := strings.TrimSpace(creds["team_id"]); t != "" {
		a.Details = map[string]string{"team_id": t}
		// The token must reach the team's projects too.
		if err := v.call(ctx, cl, creds, "/v10/projects", url.Values{"limit": {"1"}}, nil); err != nil {
			return c.Account{}, err
		}
	}
	return a, nil
}

func (v Vercel) DeployTargets(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.DeployTarget, error) {
	var res struct {
		Projects []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Link *struct {
				ProductionBranch string `json:"productionBranch"`
			} `json:"link"`
			Targets map[string]*struct {
				Alias []string `json:"alias"`
			} `json:"targets"`
		} `json:"projects"`
	}
	if err := v.call(ctx, cl, creds, "/v10/projects", url.Values{"limit": {"100"}}, &res); err != nil {
		return nil, err
	}
	out := []c.DeployTarget{}
	for _, p := range res.Projects {
		t := c.DeployTarget{ID: p.ID, Name: p.Name, URL: "https://vercel.com/" + url.PathEscape(p.Name)}
		if p.Link != nil {
			t.Branch = p.Link.ProductionBranch
		}
		if prod := p.Targets["production"]; prod != nil {
			for _, a := range prod.Alias {
				// Skip the project's own *.vercel.app names.
				if h := hostOf(a); h != "" && !strings.HasSuffix(h, ".vercel.app") {
					t.Domains = append(t.Domains, h)
				}
			}
		}
		out = append(out, t)
	}
	return out, nil
}

func (v Vercel) Deploys(ctx context.Context, cl *c.Client, creds c.Creds, target string, since time.Time) ([]c.Deploy, error) {
	if target == "" || strings.ContainsAny(target, "/?#&% ") {
		return nil, c.Errf(c.ErrInvalid, "a Vercel target is a project id")
	}
	q := url.Values{"projectId": {target}, "target": {"production"}, "limit": {"50"}, "since": {strconv.FormatInt(since.UnixMilli(), 10)}}
	var res struct {
		Deployments []struct {
			UID          string            `json:"uid"`
			State        string            `json:"state"`
			Created      int64             `json:"created"`
			Ready        int64             `json:"ready"`
			InspectorURL string            `json:"inspectorUrl"`
			Meta         map[string]string `json:"meta"`
		} `json:"deployments"`
	}
	if err := v.call(ctx, cl, creds, "/v6/deployments", q, &res); err != nil {
		return nil, err
	}
	out := []c.Deploy{}
	for _, d := range res.Deployments {
		dep := c.Deploy{ID: d.UID, Kind: "deployment", State: c.DeployBuilding, Commit: d.Meta["githubCommitSha"],
			Ref: d.Meta["githubCommitRef"], Message: firstLine(d.Meta["githubCommitMessage"]), At: time.UnixMilli(d.Created)}
		if strings.HasPrefix(d.InspectorURL, "https://") {
			dep.URL = d.InspectorURL
		}
		switch d.State {
		case "READY":
			dep.State = c.DeployReady
			if d.Ready > 0 {
				dep.At = time.UnixMilli(d.Ready)
			}
		case "ERROR", "CANCELED":
			dep.State = c.DeployFailed
		}
		out = append(out, dep)
	}
	return out, nil
}
