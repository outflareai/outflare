package deploys

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const githubAPI = "https://api.github.com"

var githubHeaders = map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}

// GitHub watches repositories: their production deployments (GitHub Pages,
// Actions, or a platform that reports deployments to GitHub, as Vercel and
// Netlify do), or, for a repository that records none, pushes to its
// default branch.
type GitHub struct{}

func (GitHub) Definition() c.Definition {
	return c.Definition{
		ID: "github", Name: "GitHub", Category: c.CategoryCode,
		Summary: "Know when your website's repository ships, so fixes are re-checked and experiments start on the real deploy.",
		Description: "Reads your repositories' production deployments, or pushes to the default branch when a repository records no deployments. " +
			"Outflare never pushes code, opens pull requests or changes settings through it.",
		Website: "https://github.com", DocsURL: "https://docs.github.com/en/rest/deployments/deployments",
		KeyURL: "https://github.com/settings/personal-access-tokens/new",
		Auth:   c.AuthOAuth2, AuthMethods: c.Both,
		OAuth: &c.OAuth{
			AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token",
			// GitHub's OAuth apps have no read-only scope for private
			// repositories; a fine-grained token below can be read-only.
			Scopes: []string{"repo", "read:user"},
		},
		Fields: []c.Field{{Name: "api_key", Label: "Fine-grained personal access token", Secret: true, Placeholder: "github_pat_…",
			Help: "On GitHub: Settings, Developer settings, Fine-grained tokens. Pick the website's repositories and give read-only access to Contents, Deployments and Metadata."}},
		Capabilities: []string{c.CapDeploys},
		Pricing:      "Works on every GitHub plan, including free",
	}
}

func (GitHub) call(ctx context.Context, cl *c.Client, creds c.Creds, path string, out any) error {
	return get(ctx, cl, "GitHub", "github", githubAPI, path, c.Token(creds), out, githubHeaders)
}

func (g GitHub) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := g.call(ctx, cl, creds, "/user", &u); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: fmt.Sprint(u.ID), Name: u.Login}, nil
}

type githubRepo struct {
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Homepage      string `json:"homepage"`
	Archived      bool   `json:"archived"`
}

func (g GitHub) DeployTargets(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.DeployTarget, error) {
	out := []c.DeployTarget{}
	for page := 1; page <= 3; page++ {
		var repos []githubRepo
		if err := g.call(ctx, cl, creds, fmt.Sprintf("/user/repos?per_page=100&sort=pushed&page=%d", page), &repos); err != nil {
			return nil, err
		}
		for _, r := range repos {
			if r.Archived {
				continue
			}
			t := c.DeployTarget{ID: r.FullName, Name: r.FullName, URL: r.HTMLURL, Branch: r.DefaultBranch}
			if h := hostOf(r.Homepage); h != "" {
				t.Domains = []string{h}
			}
			out = append(out, t)
		}
		if len(repos) < 100 {
			break
		}
	}
	return out, nil
}

// validRepo reports whether target looks like owner/name.
func validRepo(target string) bool {
	owner, name, ok := strings.Cut(target, "/")
	return ok && owner != "" && name != "" && !strings.ContainsAny(target, "?#% ") && !strings.Contains(name, "/")
}

// production reports whether a GitHub deployment environment is the live
// site.
func production(env string, flagged bool) bool {
	e := strings.ToLower(env)
	return flagged || e == "github-pages" || strings.HasPrefix(e, "prod")
}

func (g GitHub) Deploys(ctx context.Context, cl *c.Client, creds c.Creds, target string, since time.Time) ([]c.Deploy, error) {
	if !validRepo(target) {
		return nil, c.Errf(c.ErrInvalid, "a GitHub target is a repository, owner/name")
	}
	var list []struct {
		ID          int64     `json:"id"`
		SHA         string    `json:"sha"`
		Ref         string    `json:"ref"`
		Environment string    `json:"environment"`
		Production  bool      `json:"production_environment"`
		Description string    `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
	}
	if err := g.call(ctx, cl, creds, "/repos/"+target+"/deployments?per_page=50", &list); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return g.pushes(ctx, cl, creds, target, since)
	}
	out := []c.Deploy{}
	for _, d := range list {
		if !production(d.Environment, d.Production) || d.CreatedAt.Before(since) {
			continue
		}
		// The newest status says whether it went live.
		var statuses []struct {
			State     string    `json:"state"`
			LogURL    string    `json:"log_url"`
			TargetURL string    `json:"target_url"`
			CreatedAt time.Time `json:"created_at"`
		}
		if err := g.call(ctx, cl, creds, fmt.Sprintf("/repos/%s/deployments/%d/statuses?per_page=1", target, d.ID), &statuses); err != nil {
			return nil, err
		}
		dep := c.Deploy{ID: fmt.Sprint(d.ID), Kind: "deployment", State: c.DeployBuilding, Commit: d.SHA, Ref: d.Ref,
			Message: firstLine(d.Description), At: d.CreatedAt, URL: "https://github.com/" + target + "/deployments"}
		if len(statuses) > 0 {
			s := statuses[0]
			switch s.State {
			// inactive: it was live and a newer deploy replaced it.
			case "success", "inactive":
				dep.State, dep.At = c.DeployReady, s.CreatedAt
			case "failure", "error":
				dep.State, dep.At = c.DeployFailed, s.CreatedAt
			}
			if u := s.LogURL; strings.HasPrefix(u, "https://") {
				dep.URL = u
			}
		}
		out = append(out, dep)
		if len(out) == 20 {
			break
		}
	}
	return out, nil
}

// pushes are commits to the default branch, for a repository that records
// no deployments: the site deploys on push.
func (g GitHub) pushes(ctx context.Context, cl *c.Client, creds c.Creds, target string, since time.Time) ([]c.Deploy, error) {
	var repo githubRepo
	if err := g.call(ctx, cl, creds, "/repos/"+target, &repo); err != nil {
		return nil, err
	}
	q := url.Values{"sha": {repo.DefaultBranch}, "per_page": {"50"}, "since": {since.UTC().Format(time.RFC3339)}}
	var commits []struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message   string `json:"message"`
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := g.call(ctx, cl, creds, "/repos/"+target+"/commits?"+q.Encode(), &commits); err != nil {
		return nil, err
	}
	out := []c.Deploy{}
	for _, cm := range commits {
		out = append(out, c.Deploy{ID: cm.SHA, Kind: "push", State: c.DeployReady, Commit: cm.SHA, Ref: repo.DefaultBranch,
			Message: firstLine(cm.Commit.Message), URL: cm.HTMLURL, At: cm.Commit.Committer.Date})
	}
	return out, nil
}
