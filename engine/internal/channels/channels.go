// Package channels is the channel framework of the brand agent plan
// (section 3a): every channel an agent can work on is described the same
// way, so the agent's tool selection, the readiness checks (Ready, Connect
// first, Needs data) and the dashboard pick a new channel up by
// themselves, and the test kit (kit_test.go) holds every channel to the
// same bar. A channel is complete when it has connectors (the workspace's
// own keys), action kinds with their approval category, the reads the
// agent needs to decide, measurement into outcomes, playbooks, events,
// metrics, a dashboard row and alerts, and tests against recorded
// provider responses. Channel code is open; deciding when to use it is
// the agent's.
package channels

import (
	"context"
	"sort"

	"github.com/outflareai/outflare/engine/internal/playbooks"
)

// Action categories: how the action layer treats a kind of change an
// agent makes on the channel.
const (
	// CategoryRoutine runs within the agent's grants, or is held for a
	// person without one.
	CategoryRoutine = "routine"
	// CategorySpend spends the workspace's money: a run grant carries
	// daily and monthly caps and the largest change the agent may make.
	CategorySpend = "spend"
	// CategoryAlwaysPerson never runs by itself, whatever the grants: a
	// person decides each one.
	CategoryAlwaysPerson = "always_person"
)

// Action is one kind of change the agent can make on a channel, named by
// the API operation that makes it.
type Action struct {
	// Kind is a short name in object.verb form, such as campaign.pause.
	Kind string `json:"kind"`
	// OperationID of the API operation (api/openapi.yaml); the gate holds
	// or runs it by the token's grants on its path.
	OperationID string `json:"operation_id"`
	Category    string `json:"category"`
	// Undoable says a later action can put it back (pausing, a draft);
	// false for what reaches the outside world for good (a send, a post).
	Undoable bool `json:"undoable"`
	// DefaultDailyLimit is the number a run grant on it allows a day when
	// the person sets none; 0 means no default.
	DefaultDailyLimit int `json:"default_daily_limit,omitempty"`
}

// Measure is how the channel's results are judged, in the outcomes and
// attribution every channel shares.
type Measure struct {
	// Metric is the number's name, as the brand agent's objectives and
	// plays use it.
	Metric string `json:"metric"`
	// Source says where it comes from: outcomes, attribution, analytics,
	// the channel's own reports.
	Source string `json:"source"`
	// OperationID of the read that gives it.
	OperationID string `json:"operation_id"`
}

// Channel is one channel's manifest.
type Channel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	// Goals the channel serves, in the playbooks' vocabulary (search,
	// customers, replies, revenue, partners, retention, activation,
	// ai_answers, audience, coverage, content).
	Goals []string `json:"goals"`
	// Needs is what the workspace connects and collects before the channel
	// is ready: the same shape as a playbook's needs.
	Needs playbooks.Needs `json:"needs"`
	// Actions the agent can take; Reads it decides from (operation ids).
	Actions []Action `json:"actions"`
	Reads   []string `json:"reads"`
	// Measures map the channel's results into shared outcomes.
	Measures []Measure `json:"measures"`
	// Playbooks on this channel (ids from playbooks/).
	Playbooks []string `json:"playbooks"`
	// Docs is the page that explains the channel (docs/<name>.md).
	Docs string `json:"docs"`
	// Dashboard is the row title in deploy/monitoring/grafana/dashboards/outflare.json.
	Dashboard string `json:"dashboard"`
	// Metrics the engine exports for it (full names), Alerts the rules
	// that watch it (deploy/monitoring/alerts.yml).
	Metrics []string `json:"metrics"`
	Alerts  []string `json:"alerts"`
	// Tests are the test functions (engine/internal/api) that run the
	// channel against recorded provider responses and the shared
	// action-layer cases: the channel's test kit.
	Tests []string `json:"tests"`
}

// Readiness is whether a workspace, or a brand, can use a channel now.
type Readiness struct {
	// State is ready, connect_first (a required connection is missing) or
	// needs_data (connected, but the data it works on isn't there yet).
	State string `json:"state"`
	playbooks.Readiness
}

// States.
const (
	Ready        = "ready"
	ConnectFirst = "connect_first"
	NeedsData    = "needs_data"
)

// Check says whether a workspace with the connected connectors and the
// data in have can use the channel.
func (c Channel) Check(connected []playbooks.ConnectorInfo, have map[string]bool) Readiness {
	r := Readiness{Readiness: playbooks.Playbook{Needs: c.Needs}.Check(connected, have)}
	switch {
	case len(r.MissingConnections) > 0:
		r.State = ConnectFirst
	case len(r.MissingData) > 0:
		r.State = NeedsData
	default:
		r.State = Ready
	}
	return r
}

// dataQueries answer the channels' own data checks with one cheap EXISTS
// for one workspace; the playbooks' checks (records, customer_events,
// inbox, site_tracking, seo_site, mailboxes, leads) come from playbooks.
var dataQueries = map[string]string{
	"ad_accounts":     `SELECT EXISTS (SELECT 1 FROM ad_accounts WHERE workspace_id = $1 AND status = 'active')`,
	"social_accounts": `SELECT EXISTS (SELECT 1 FROM social_accounts WHERE workspace_id = $1)`,
	"review_pages":    `SELECT EXISTS (SELECT 1 FROM review_pages WHERE workspace_id = $1)`,
	"brand_context":   `SELECT EXISTS (SELECT 1 FROM brand_context WHERE workspace_id = $1 AND (about <> '' OR audience <> ''))`,
	"brand_accounts":  `SELECT EXISTS (SELECT 1 FROM brand_accounts WHERE workspace_id = $1 AND status <> 'archived')`,
}

// DataChecks names every data check a channel may use.
var DataChecks = map[string]string{
	"ad_accounts":     "An ad account read from a connected ad platform",
	"social_accounts": "A social account connected for the brand",
	"review_pages":    "The brand's review pages found",
	"brand_context":   "The brand's context written (about, audience)",
	"brand_accounts":  "An account of the brand's recorded on a site with no API (docs/accounts.md)",
}

// HaveData answers the data checks the channels use for workspace ws.
func HaveData(ctx context.Context, q playbooks.Querier, ws string) (map[string]bool, error) {
	// The playbooks' checks, through a stand-in playbook that needs them all.
	var stand playbooks.Playbook
	for name := range playbooks.DataChecks {
		stand.Needs.Data = append(stand.Needs.Data, playbooks.DataNeed{Check: name})
	}
	have, err := playbooks.HaveData(ctx, q, ws, []playbooks.Playbook{stand})
	if err != nil {
		return nil, err
	}
	for name, sql := range dataQueries {
		var ok bool
		if err := q.QueryRow(ctx, sql, ws).Scan(&ok); err != nil {
			return nil, err
		}
		have[name] = ok
	}
	return have, nil
}

// All lists the channels, by id.
func All() []Channel {
	out := make([]Channel, len(channels))
	copy(out, channels)
	for i := range out {
		out[i].fill()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// fill gives every list a value, so the API shows [] and never null.
func (c *Channel) fill() {
	if c.Needs.Data == nil {
		c.Needs.Data = []playbooks.DataNeed{}
	}
	if c.Needs.Connections == nil {
		c.Needs.Connections = []playbooks.ConnectionNeed{}
	}
	for _, l := range []*[]string{&c.Goals, &c.Reads, &c.Playbooks, &c.Metrics, &c.Alerts, &c.Tests} {
		if *l == nil {
			*l = []string{}
		}
	}
}

// Get finds a channel by id.
func Get(id string) (Channel, bool) {
	for _, c := range channels {
		if c.ID == id {
			c.fill()
			return c, true
		}
	}
	return Channel{}, false
}

func capability(cap, label string, recommended ...string) playbooks.ConnectionNeed {
	return playbooks.ConnectionNeed{Capability: cap, Label: label, Recommended: recommended}
}

func optional(cap, label string, recommended ...string) playbooks.ConnectionNeed {
	n := capability(cap, label, recommended...)
	n.Optional = true
	return n
}

func data(check, label string) playbooks.DataNeed {
	return playbooks.DataNeed{Check: check, Label: label}
}

var channels = []Channel{
	{
		ID: "newsletters", Name: "Newsletters", Summary: "Broadcasts to the brand's own subscribers from its own email provider, every send through the marketing email guardrails (consent, frequency cap, quiet hours), with lift measured against a holdout.",
		Goals: []string{"retention", "revenue", "audience"},
		Needs: playbooks.Needs{
			Data: []playbooks.DataNeed{data("records", "People with consent to email"), data("brand_context", "The brand's context and voice")},
		},
		Actions: []Action{
			{Kind: "broadcast.create", OperationID: "createBrandEmailBroadcast", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 2},
			{Kind: "broadcast.update", OperationID: "updateBrandEmailBroadcast", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 10},
			{Kind: "broadcast.send", OperationID: "sendBrandEmailBroadcast", Category: CategoryRoutine, Undoable: false, DefaultDailyLimit: 1},
			{Kind: "broadcast.cancel", OperationID: "cancelBrandEmailBroadcast", Category: CategoryRoutine, Undoable: false, DefaultDailyLimit: 5},
		},
		Reads:     []string{"listBrandEmailBroadcasts", "getBrandEmailBroadcast", "getBrandEmailBroadcastLift", "listBrandEmailBroadcastRecipients"},
		Measures:  []Measure{{Metric: "broadcast_lift", Source: "conversions against the broadcast's holdout", OperationID: "getBrandEmailBroadcastLift"}, {Metric: "subscribers", Source: "people with consent to email", OperationID: "listBrandEmailBroadcastRecipients"}},
		Playbooks: []string{"send-a-monthly-newsletter", "renew-what-works"},
		Docs:      "docs/lifecycle-email.md",
		Dashboard: "Marketing email guardrails (frequency cap, quiet hours)",
		Metrics:   []string{"outflare_emails_guarded_total"},
		Alerts:    []string{"OutflareEmailsCappedMany"},
		Tests:     []string{"TestEmailBroadcasts", "TestEmailBroadcastHoldout"},
	},
	{
		ID: "website_conversion", Name: "Website conversion", Summary: "A/B tests, experiments and feature flags on the brand's own site through its website script: versions of a page, who sees what, and results with lift, for a person to ship.",
		Goals: []string{"activation", "revenue"},
		Needs: playbooks.Needs{
			Data: []playbooks.DataNeed{data("site_tracking", "The website script installed on the brand's site")},
		},
		Actions: []Action{
			{Kind: "ab_test.create", OperationID: "createABTest", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 2},
			{Kind: "ab_test.update", OperationID: "updateABTest", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 10},
			{Kind: "ab_test.start", OperationID: "startABTest", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 2},
			{Kind: "ab_test.end", OperationID: "endABTest", Category: CategoryRoutine, Undoable: false, DefaultDailyLimit: 2},
			{Kind: "ab_test.ship", OperationID: "shipABTest", Category: CategoryAlwaysPerson, Undoable: false},
			{Kind: "experiment.create", OperationID: "createExperiment", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 2},
			{Kind: "experiment.start", OperationID: "startExperiment", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 2},
			{Kind: "experiment.stop", OperationID: "stopExperiment", Category: CategoryRoutine, Undoable: false, DefaultDailyLimit: 2},
			{Kind: "flag.create", OperationID: "createFlag", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
			{Kind: "flag.update", OperationID: "updateFlag", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 10},
		},
		Reads:     []string{"listABTests", "getABTest", "getABTestResults", "listABTestLearnings", "listExperiments", "getExperiment", "listFlags", "getFlag"},
		Measures:  []Measure{{Metric: "ab_test_lift", Source: "each test's results against its control", OperationID: "getABTestResults"}, {Metric: "experiment_results", Source: "SEO experiments' page results", OperationID: "getExperiment"}},
		Playbooks: []string{"test-your-best-page"},
		Docs:      "docs/ab-tests.md",
		Dashboard: "Feature flags",
		Metrics:   []string{"outflare_flags_requests_total", "outflare_flags_request_seconds"},
		Alerts:    []string{"OutflareFlagsFailing", "OutflareFlagsSlow"},
		Tests:     []string{"TestABTests", "TestABTestsChecklistAndShip", "TestExperiments", "TestFlags"},
	},
	{
		ID: "competitor_watch", Name: "Competitor watch", Summary: "Competitors tracked on the brand's own SEO and web search connections: their pages, gaps and dead links, and the news about them, fed into what the agent tries next.",
		Goals: []string{"search", "coverage", "ai_answers"},
		Needs: playbooks.Needs{
			Connections: []playbooks.ConnectionNeed{capability("analytics.read", "Google Search Console or Bing Webmaster Tools", "google_search_console", "bing_webmaster"), optional("web.search", "A web search connection, for the news about them", "geonode")},
			Data:        []playbooks.DataNeed{data("seo_site", "A site added to SEO")},
		},
		Actions: []Action{
			{Kind: "competitor.add", OperationID: "addSEOCompetitor", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
			{Kind: "competitor.update", OperationID: "updateSEOCompetitor", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 10},
			{Kind: "competitor.check", OperationID: "checkSEOCompetitor", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 10},
			{Kind: "news_search.create", OperationID: "createNewsSearch", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
			{Kind: "news_search.run", OperationID: "runNewsSearch", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 10},
		},
		Reads:     []string{"listSEOCompetitors", "listSEOCompetitorEvents", "getSEOCompetitorGaps", "listNewsSearches", "getNewsSearch"},
		Measures:  []Measure{{Metric: "competitor_events", Source: "changes seen on competitors' sites", OperationID: "listSEOCompetitorEvents"}, {Metric: "competitor_gaps", Source: "keywords competitors rank for and the brand doesn't", OperationID: "getSEOCompetitorGaps"}},
		Playbooks: []string{"link-competitor-gaps", "link-competitor-dead-pages", "find-creators-covering-competitors"},
		Docs:      "docs/news-watch.md",
		Dashboard: "News watch (saved news searches, on workspaces' own web search connections)",
		Metrics:   []string{"outflare_news_search_runs_total", "outflare_news_found_total"},
		Alerts:    []string{"OutflareNewsSearchesOverdue", "OutflareNewsSearchRunsFailing"},
		Tests:     []string{"TestCompetitorGaps", "TestCompetitorsSuggestTrackAndNumbers", "TestNewsWatchFindsArticlesAndCoverage"},
	},
	{
		ID: "listings", Name: "Listings and marketplaces", Summary: "Directories, review sites, app and integration marketplaces and comparison sites with no API: profiles submitted and kept current by a browser worker signed in as the brand's own account, each appearance kept in Coverage.",
		Goals: []string{"search", "customers", "coverage"},
		Needs: playbooks.Needs{
			Data: []playbooks.DataNeed{data("brand_context", "The brand's context, for what the profiles say"), data("brand_accounts", "The brand's account on the site, in the vault")},
		},
		Actions: []Action{
			{Kind: "account.record", OperationID: "createAccount", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
			{Kind: "browser_task.run", OperationID: "createBrowserTask", Category: CategoryRoutine, Undoable: false, DefaultDailyLimit: 10},
			{Kind: "takeover.request", OperationID: "requestAccountTakeover", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
		},
		Reads:     []string{"listAccounts", "getAccount", "listAccountIncidents", "listAccountTakeovers", "listBrowserTasks", "getBrowserTask", "listCoveragePlacements"},
		Measures:  []Measure{{Metric: "listings_live", Source: "Coverage placements of the directory, profile and integration kinds that are live", OperationID: "listCoveragePlacements"}, {Metric: "referral_visits", Source: "attribution by publisher", OperationID: "getCoverageReports"}},
		Playbooks: []string{"get-listed-where-buyers-look"},
		Docs:      "docs/browser-workers.md",
		Dashboard: "Browser workers (tasks in a browser on sites with no API)",
		Metrics:   []string{"outflare_browser_tasks_total", "outflare_browser_steps_total", "outflare_accounts_attempts_total"},
		Alerts:    []string{"OutflareBrowserTasksWaiting", "OutflareBrowserTasksFailing"},
		Tests:     []string{"TestBrowserTasks", "TestBrandAccounts", "TestBrandAccountTakeoversAndOffboarding"},
	},
	{
		ID: "forums", Name: "Forums and launch sites", Summary: "Hacker News, Product Hunt, Indie Hackers, Discord and Slack communities, Quora, Stack Overflow and niche forums: mostly listening, with replies and launches a person approves, through a browser worker signed in as the brand's own account.",
		Goals: []string{"customers", "audience", "coverage"},
		Needs: playbooks.Needs{
			Data: []playbooks.DataNeed{data("brand_context", "The brand's context and voice"), data("brand_accounts", "The brand's account on the community, in the vault")},
		},
		Actions: []Action{
			{Kind: "account.record", OperationID: "createAccount", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
			{Kind: "browser_task.run", OperationID: "createBrowserTask", Category: CategoryRoutine, Undoable: false, DefaultDailyLimit: 5},
			{Kind: "takeover.request", OperationID: "requestAccountTakeover", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
		},
		Reads:     []string{"listAccounts", "listAccountIncidents", "listAccountTakeovers", "listBrowserTasks", "getBrowserTask", "listCoveragePlacements"},
		Measures:  []Measure{{Metric: "community_threads_live", Source: "Coverage placements of the community kind that are live", OperationID: "listCoveragePlacements"}, {Metric: "community_visits", Source: "attribution by publisher", OperationID: "getCoverageReports"}},
		Playbooks: []string{"help-in-communities"},
		Docs:      "docs/browser-workers.md",
		Dashboard: "Browser workers (tasks in a browser on sites with no API)",
		Metrics:   []string{"outflare_browser_tasks_total", "outflare_browser_steps_total", "outflare_accounts_takeovers_total"},
		Alerts:    []string{"OutflareBrowserTasksWaiting", "OutflareAccountTakeoverWaiting"},
		Tests:     []string{"TestBrowserTasks", "TestBrandAccountTakeoversAndOffboarding"},
	},
	{
		ID: "paid_ads", Name: "Paid ads", Summary: "Campaigns on the brand's own ad accounts: read results, pause, resume and set daily budgets within spend grants; the kill switch pauses every campaign.",
		Goals: []string{"customers", "revenue"},
		Needs: playbooks.Needs{
			Connections: []playbooks.ConnectionNeed{capability("ads.manage", "An ad platform that can be managed", "google_ads", "meta_ads", "microsoft_ads", "linkedin_ads", "tiktok_ads")},
			Data:        []playbooks.DataNeed{data("ad_accounts", "An ad account read from the platform")},
		},
		Actions: []Action{
			{Kind: "campaign.pause", OperationID: "pauseAdCampaign", Category: CategorySpend, Undoable: true, DefaultDailyLimit: 10},
			{Kind: "campaign.resume", OperationID: "resumeAdCampaign", Category: CategorySpend, Undoable: true, DefaultDailyLimit: 10},
			{Kind: "campaign.budget", OperationID: "updateAdCampaign", Category: CategorySpend, Undoable: true, DefaultDailyLimit: 10},
			{Kind: "campaigns.pause_all", OperationID: "pauseAllAds", Category: CategorySpend, Undoable: true, DefaultDailyLimit: 1},
		},
		Reads:     []string{"getAdsSummary", "listAdCampaigns", "listAdItems", "listAdChanges", "getAttributionAds", "listAdConversionActions"},
		Measures:  []Measure{{Metric: "ad_conversions_30d", Source: "the ad platforms' reported conversions", OperationID: "getAdsSummary"}, {Metric: "ad_attributed_signups", Source: "attribution", OperationID: "getAttributionAds"}},
		Playbooks: []string{"move-ad-budget-to-what-works"},
		Docs:      "docs/paid-media.md",
		Dashboard: "Paid media (managing campaigns)",
		Metrics:   []string{"outflare_ads_changes_total", "outflare_ads_item_days_synced_total"},
		Alerts:    []string{"OutflareAdChangesFailing", "OutflareAdChangesUnverified"},
		Tests:     []string{"TestAdCampaignChanges", "TestAdCampaignChangesByAnAgent", "TestAdsGrantsGetDefaultSpendCaps", "TestAdsSyncAndReports"},
	},
	{
		ID: "content", Name: "Content", Summary: "Posts and pages written in Outflare for the brand's website, published through its own CMS as drafts or live.",
		Goals: []string{"search", "ai_answers", "content"},
		Needs: playbooks.Needs{
			Connections: []playbooks.ConnectionNeed{capability("cms.publish", "The brand's CMS", "wordpress", "ghost")},
			Data:        []playbooks.DataNeed{data("brand_context", "The brand's context, for facts and voice")},
		},
		Actions: []Action{
			{Kind: "content.create", OperationID: "createContent", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
			{Kind: "content.update", OperationID: "updateContent", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 20},
			{Kind: "content.publish", OperationID: "publishContent", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
		},
		Reads:     []string{"listContent", "getContent", "getSiteSearch", "findKnowledgePassages"},
		Measures:  []Measure{{Metric: "content_published_30d", Source: "items that went live", OperationID: "listContent"}, {Metric: "search_clicks", Source: "Search Console clicks per page", OperationID: "getSiteSearch"}},
		Playbooks: []string{"content-from-customer-questions", "pr-data-story"},
		Docs:      "docs/content.md",
		Dashboard: "Content (publishing to a CMS)",
		Metrics:   []string{"outflare_content_publishes_total"},
		Alerts:    []string{"OutflareContentPublishesFailing"},
		Tests:     []string{"TestContentPublishing", "TestContentPublishingByAnAgent"},
	},
	{
		ID: "social", Name: "Social", Summary: "The brand's own posts on X and LinkedIn, drafted in Outflare and approved by a person, with followers and post results read every day.",
		Goals: []string{"audience", "customers"},
		Needs: playbooks.Needs{
			Connections: []playbooks.ConnectionNeed{capability("social.publish", "A social account that can post", "x", "linkedin")},
			Data:        []playbooks.DataNeed{data("social_accounts", "A social account connected for the brand")},
		},
		Actions: []Action{
			{Kind: "post.draft", OperationID: "createSocialPublication", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 10},
			{Kind: "post.update", OperationID: "updateSocialPublication", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 20},
			{Kind: "post.cancel", OperationID: "cancelSocialPublication", Category: CategoryRoutine, Undoable: false, DefaultDailyLimit: 10},
			{Kind: "series.create", OperationID: "createSocialSeries", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 2},
			{Kind: "post.approve", OperationID: "approveSocialPublication", Category: CategoryAlwaysPerson, Undoable: false},
		},
		Reads:     []string{"getSocialOverview", "listSocialAccounts", "listSocialPosts", "getSocialGrowth", "listSocialPublications"},
		Measures:  []Measure{{Metric: "followers", Source: "the accounts' follower counts", OperationID: "getSocialGrowth"}, {Metric: "post_engagement", Source: "the brand's own posts' numbers", OperationID: "listSocialPosts"}},
		Playbooks: []string{"post-what-the-brand-learned"},
		Docs:      "docs/social.md",
		Dashboard: "Social publishing (the brand's own posts)",
		Metrics:   []string{"outflare_social_publishes_total", "outflare_social_account_reads_total"},
		Alerts:    []string{},
		Tests:     []string{"TestSocialPublishing", "TestSocialAccountsXAndLinkedIn"},
	},
	{
		ID: "community", Name: "Communities", Summary: "Reddit: posts the brand can help with, replies a person approves from the brand's own account, and campaigns through Engain within the brand's budget.",
		Goals: []string{"customers", "audience"},
		Needs: playbooks.Needs{
			Connections: []playbooks.ConnectionNeed{capability("social.search", "Reddit, with the workspace's own app", "reddit"), optional("reddit.campaigns", "Engain, for campaigns", "engain")},
		},
		Actions: []Action{
			{Kind: "watch.create", OperationID: "createSocialWatch", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
			{Kind: "reply.draft", OperationID: "draftSocialReply", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 20},
			{Kind: "reply.approve", OperationID: "approveSocialAction", Category: CategoryAlwaysPerson, Undoable: false},
			{Kind: "campaign.create", OperationID: "createRedditExperiment", Category: CategorySpend, Undoable: true, DefaultDailyLimit: 2},
			{Kind: "campaign.action", OperationID: "createRedditAction", Category: CategorySpend, Undoable: false, DefaultDailyLimit: 20},
		},
		Reads:     []string{"listSocialItems", "listSocialActions", "listRedditThreads", "getRedditReport", "getRedditBudget"},
		Measures:  []Measure{{Metric: "community_replies_live", Source: "replies that went live", OperationID: "listSocialActions"}, {Metric: "community_visits", Source: "attribution", OperationID: "getRedditReport"}},
		Playbooks: []string{"answer-reddit-questions"},
		Docs:      "docs/reddit-campaigns.md",
		Dashboard: "Reddit campaigns (actions sent through Engain)",
		Metrics:   []string{"outflare_reddit_sends_total", "outflare_reddit_checks_total"},
		Alerts:    []string{},
		Tests:     []string{"TestSocialRedditWatchDraftApprovePost", "TestRedditCampaigns"},
	},
	{
		ID: "outreach", Name: "Email outreach", Summary: "Sequences from the workspace's own mailboxes to people in its lists, every send through the sending policy, with replies classified in the inbox.",
		Goals: []string{"customers", "replies"},
		Needs: playbooks.Needs{
			Data: []playbooks.DataNeed{data("mailboxes", "Mailboxes for outreach"), data("leads", "Leads")},
		},
		Actions: []Action{
			{Kind: "sequence.create", OperationID: "createBrandSequence", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 3},
			{Kind: "sequence.update", OperationID: "updateBrandSequence", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 10},
			{Kind: "sequence.enroll", OperationID: "enrollBrandLeads", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
			{Kind: "enrollment.stop", OperationID: "stopBrandEnrollment", Category: CategoryRoutine, Undoable: false, DefaultDailyLimit: 50},
		},
		Reads:     []string{"listBrandSequences", "getBrandSequence", "listBrandEnrollments", "getBrandSequenceLift", "listInbox"},
		Measures:  []Measure{{Metric: "replies_30d", Source: "interested replies in the inbox", OperationID: "listInbox"}, {Metric: "meetings_30d", Source: "outcomes", OperationID: "outcomesReport"}},
		Playbooks: []string{"answer-interested-replies", "hand-off-warm-prospects"},
		Docs:      "docs/pipeline.md",
		Dashboard: "Exclusions (people journeys, broadcasts and sequences leave out)",
		Metrics:   []string{"outflare_emails_guarded_total", "outflare_automations_excluded_total"},
		Alerts:    []string{"OutflareEmailsCappedMany"},
		Tests:     []string{"TestSequenceSendsThroughPolicy", "TestSequenceDailySendLimit", "TestSequenceScopesAndIsolation"},
	},
	{
		ID: "link_building", Name: "Link building", Summary: "Prospects found from the brand's own SEO connection, pitched by email with a person's approval, and links checked until they are live.",
		Goals: []string{"search"},
		Needs: playbooks.Needs{
			Connections: []playbooks.ConnectionNeed{capability("seo.read", "An SEO tool that reads backlinks", "dataforseo", "ahrefs")},
			Data:        []playbooks.DataNeed{data("seo_site", "A site added to SEO"), data("mailboxes", "Mailboxes for outreach")},
		},
		Actions: []Action{
			{Kind: "prospects.add", OperationID: "addLinkProspects", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
			{Kind: "pitch.start", OperationID: "startLinkPitch", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 20},
			{Kind: "pitch.edit", OperationID: "editLinkPitch", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 50},
			{Kind: "pitch.approve", OperationID: "approveLinkPitch", Category: CategoryAlwaysPerson, Undoable: false},
			{Kind: "automation.create", OperationID: "createPlaybookAutomation", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
		},
		Reads:     []string{"getLinkResults", "listLinkProspects", "listLinkPitches", "listBacklinks", "getBacklinksSummary", "listPlaybookAutomations"},
		Measures:  []Measure{{Metric: "links_live_30d", Source: "links checked live", OperationID: "getLinkResults"}},
		Playbooks: []string{"link-reclaim-lost-links", "link-partners-and-customers", "link-competitor-dead-pages", "link-competitor-gaps", "link-broken-links", "link-unlinked-mentions", "link-resource-pages", "link-promote-an-asset"},
		Docs:      "docs/seo.md",
		Dashboard: "Link building (who links to a page, on workspaces' own SEO connections)",
		Metrics:   []string{"outflare_seo_link_reads_total", "outflare_seo_link_page_checks_total"},
		Alerts:    []string{},
		Tests:     []string{"TestLinkPitchesFromOpenerToLive", "TestLinkProspectsAddScoreAndPitch", "TestLinkReadsReportsAndSettings"},
	},
	{
		ID: "press", Name: "Press coverage", Summary: "Stories with approved quotes and sourced facts, pitched to the journalists who cover the topic, and coverage tracked as it lands.",
		Goals: []string{"coverage", "search"},
		Needs: playbooks.Needs{
			Connections: []playbooks.ConnectionNeed{capability("web.search", "A web search connection, for news and journalists", "geonode")},
			Data:        []playbooks.DataNeed{data("brand_context", "The brand's context, for facts and voice"), data("mailboxes", "Mailboxes for outreach")},
		},
		Actions: []Action{
			{Kind: "story.create", OperationID: "createStory", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 3},
			{Kind: "story.draft", OperationID: "draftStory", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
			{Kind: "quote.request_approval", OperationID: "requestQuoteApproval", Category: CategoryRoutine, Undoable: false, DefaultDailyLimit: 5},
			{Kind: "story.approve", OperationID: "approveStory", Category: CategoryAlwaysPerson, Undoable: false},
			{Kind: "pitches.start", OperationID: "startStoryPitches", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 3},
			{Kind: "news_search.create", OperationID: "createNewsSearch", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
		},
		Reads:     []string{"listStories", "getStoryResults", "listNewsArticles", "listJournalists", "listCoverage"},
		Measures:  []Measure{{Metric: "coverage_30d", Source: "coverage recorded", OperationID: "listCoverage"}},
		Playbooks: []string{"pr-react-to-news", "pr-expert-comment", "pr-data-story", "pr-regional-data-story"},
		Docs:      "docs/stories.md",
		Dashboard: "News watch (saved news searches, on workspaces' own web search connections)",
		Metrics:   []string{"outflare_news_search_runs_total", "outflare_stories_quote_approval_decisions_total", "outflare_coverage_checks_total"},
		Alerts:    []string{},
		Tests:     []string{"TestStoriesCheckAndApproval", "TestStoryPitches", "TestNewsWatchFindsArticlesAndCoverage"},
	},
	{
		ID: "seo", Name: "SEO", Summary: "The brand's site crawled and its search results read, with fixes ranked by the clicks they bring and page targets to work toward.",
		Goals: []string{"search", "ai_answers"},
		Needs: playbooks.Needs{
			Connections: []playbooks.ConnectionNeed{capability("analytics.read", "Google Search Console or Bing Webmaster Tools", "google_search_console", "bing_webmaster")},
			Data:        []playbooks.DataNeed{data("seo_site", "A site added to SEO")},
		},
		Actions: []Action{
			{Kind: "crawl.start", OperationID: "startCrawl", Category: CategoryRoutine, Undoable: false, DefaultDailyLimit: 2},
			{Kind: "fix.update", OperationID: "updateFix", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 20},
			{Kind: "fix.start", OperationID: "startFix", Category: CategoryAlwaysPerson, Undoable: false},
			{Kind: "target.set", OperationID: "setPageTarget", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 20},
			{Kind: "keywords.track", OperationID: "trackKeywords", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 5},
		},
		Reads:     []string{"listSEOSites", "getSiteSearch", "listCrawlIssues", "listFixes", "getFixPrompt", "listRankings", "getSiteRegressions"},
		Measures:  []Measure{{Metric: "sessions_30d", Source: "analytics", OperationID: "getSEOOverview"}, {Metric: "search_clicks", Source: "Search Console", OperationID: "getSiteSearch"}},
		Playbooks: []string{"seo-quick-wins", "get-named-in-ai-answers"},
		Docs:      "docs/seo.md",
		Dashboard: "Scheduled syncs (analytics, Search Console, ads, SEO)",
		Metrics:   []string{"outflare_sync_runs_total", "outflare_seo_archive_lookups_total"},
		Alerts:    []string{},
		Tests:     []string{"TestAnalyticsSyncAndHistory", "TestInternalLinkSuggestions"},
	},
	{
		ID: "reviews", Name: "Reviews", Summary: "The brand's review pages read daily, new reviews answered with a person's approval, and review requests sent to happy customers.",
		Goals: []string{"customers", "retention"},
		Needs: playbooks.Needs{
			Connections: []playbooks.ConnectionNeed{capability("web.search", "A web search connection, to find and read review pages", "geonode")},
			Data:        []playbooks.DataNeed{data("review_pages", "The brand's review pages found")},
		},
		Actions: []Action{
			{Kind: "pages.discover", OperationID: "discoverReviewPages", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 2},
			{Kind: "reply.draft", OperationID: "draftReviewReply", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 20},
			{Kind: "reply.mark_replied", OperationID: "markReviewReplied", Category: CategoryRoutine, Undoable: true, DefaultDailyLimit: 20},
		},
		Reads:     []string{"listReviews", "getReviewsSummary", "listReviewPages", "getReview"},
		Measures:  []Measure{{Metric: "reviews_30d", Source: "new reviews read", OperationID: "getReviewsSummary"}},
		Playbooks: []string{"answer-every-review"},
		Docs:      "docs/reviews.md",
		Dashboard: "Reviews (review pages read daily)",
		Metrics:   []string{"outflare_reviews_new_total", "outflare_reviews_page_reads_total"},
		Alerts:    []string{},
		Tests:     []string{"TestReviewsFindReadScoreAndDraft", "TestReviewRequests"},
	},
}
