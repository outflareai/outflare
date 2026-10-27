package emails

import (
	"fmt"
	"strings"

	"github.com/outflareai/outflare/engine/internal/connectors/emailsend"
)

// A Guide walks a person through connecting one email provider, step by
// step, and comes with a prompt they can hand to their own AI agent to do
// the clicking for them. Every guide keeps the secret out of chats: the
// person creates the key and pastes it into Outflare themselves.

// Guide is the setup guide for one provider.
type Guide struct {
	Connector string `json:"connector"`
	Name      string `json:"name"`
	// Minutes is roughly how long it takes, not counting waiting for the
	// provider to approve the account.
	Minutes int    `json:"minutes"`
	Summary string `json:"summary"`
	// Needs lists what to have ready.
	Needs []string    `json:"needs"`
	Steps []GuideStep `json:"steps"`
	// Limits explains new-account limits (the SES sandbox, Postmark's
	// approval) and how to lift them.
	Limits      string `json:"limits"`
	AgentPrompt string `json:"agent_prompt"`
}

// GuideStep is one step. Who does it: "you" in the provider's console,
// "outflare" on its own once connected.
type GuideStep struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	Who       string `json:"who"`
	Link      string `json:"link,omitempty"`
	LinkLabel string `json:"link_label,omitempty"`
	Code      string `json:"code,omitempty"`
	CodeLabel string `json:"code_label,omitempty"`
}

// GuideParams fill in the parts that depend on where Outflare runs.
type GuideParams struct {
	// ConnectURL is the page in Outflare where the keys are pasted.
	ConnectURL string
	// Domain is the domain the person wants to send from, when known.
	Domain string
}

// Guides returns the guide for every provider, in the order to offer them.
func Guides(p GuideParams) []Guide {
	return []Guide{sesGuide(p), postmarkGuide(p), resendGuide(p)}
}

// GuideFor returns one provider's guide.
func GuideFor(connector string, p GuideParams) (Guide, bool) {
	for _, g := range Guides(p) {
		if g.Connector == connector {
			return g, true
		}
	}
	return Guide{}, false
}

func domainOr(p GuideParams) string {
	if p.Domain != "" {
		return p.Domain
	}
	return "your domain"
}

// keyRule is the same in every prompt: the agent never handles the secret.
func keyRule(connectURL, what string) string {
	return fmt.Sprintf(`Rules:
- Never ask me to paste %s into this chat, and never print, log or save it to a file. When it is time to create it, stop and tell me exactly where to click; I will create it and paste it into Outflare myself at %s.
- Do not change anything in the account that these steps do not mention.
- Tell me when each step is done, and if a screen looks different from what you expect, describe it to me instead of guessing.`, what, connectURL)
}

func mailFrom(p GuideParams) string {
	if p.Domain != "" {
		return "mail." + p.Domain
	}
	return "such as mail.example.com"
}

func sesGuide(p GuideParams) Guide {
	steps := []GuideStep{
		{Title: "Choose your region", Who: "you",
			Body: "Open the Amazon SES console and note the region in the top right, such as us-east-1 (N. Virginia) or eu-west-1 (Ireland). Use the region your SES account already sends from, or the one closest to your customers if it is new.",
			Link: "https://console.aws.amazon.com/ses/home", LinkLabel: "Open Amazon SES"},
		{Title: "Create an IAM user for Outflare", Who: "you",
			Body: "In IAM, go to Users and Create user. Name it outflare-ses and leave console access off. On the permissions step choose Attach policies directly, then Create policy, switch to JSON and paste the policy below. Name the policy OutflareSES, create it, attach it to the user and finish.",
			Link: "https://console.aws.amazon.com/iam/home#/users/create", LinkLabel: "Create IAM user",
			Code: emailsend.SESPolicy, CodeLabel: "IAM policy (JSON)"},
		{Title: "Create an access key", Who: "you",
			Body: "Open the outflare-ses user, then Security credentials, Create access key. Choose Application running outside AWS, and create it. Keep the page open: the secret access key is shown only once.",
			Link: "https://console.aws.amazon.com/iam/home#/users", LinkLabel: "Open IAM users"},
		{Title: "Paste the key into Outflare", Who: "you",
			Body: "Paste the access key ID, the secret access key and the region into the form on this page and press Connect. Outflare checks the key and shows whether the account is in the SES sandbox.",
			Link: p.ConnectURL, LinkLabel: "Connect Amazon SES"},
		{Title: "Delivery events are set up for you", Who: "outflare",
			Body: "Outflare creates a configuration set called outflare and an SNS topic called outflare-email-events, and subscribes to it, so deliveries, bounces and complaints come back on their own. Hard bounces and complaints are added to your suppression list."},
		{Title: "Add your sending domain", Who: "you",
			Body: "Add " + domainOr(p) + " under Sending domains. Outflare registers it with Easy DKIM and a custom MAIL FROM subdomain (" + mailFrom(p) + ") and lists the DNS records: three DKIM CNAMEs, an MX and an SPF TXT for the MAIL FROM subdomain, and a DMARC record if you do not have one. Add them at your DNS host; Outflare checks every minute for the first quarter of an hour, then less often."},
		{Title: "Request production access", Who: "you",
			Body: "New SES accounts start in the sandbox: they can send only to addresses you have verified, 200 emails a day. In SES, open Account dashboard and Request production access. Choose Transactional, give your website, and describe the email: who gets it, how they signed up, and how bounces, complaints and unsubscribes are handled (Outflare suppresses them automatically). AWS usually answers within a day.",
			Link: "https://console.aws.amazon.com/ses/home#/account", LinkLabel: "Open SES account dashboard"},
	}
	prompt := fmt.Sprintf(`I want to send transactional and lifecycle email (receipts, password resets, onboarding) from my own Amazon SES account through Outflare. Please set up SES for me, step by step, in the AWS console (or with the AWS CLI if I have it configured).

1. Ask me which AWS region to use (default us-east-1), and use the same region throughout.
2. In IAM, create a user named outflare-ses with no console access.
3. Create a customer managed policy named OutflareSES with exactly this JSON, and attach it to that user:

%s

4. Stop here and tell me to create the access key myself: IAM, Users, outflare-ses, Security credentials, Create access key, "Application running outside AWS". I will paste the access key ID, the secret access key and the region into Outflare.
5. Once I say it is connected, check SES, Account dashboard. If the account is in the sandbox, help me write the production access request: use case Transactional, my website, the kinds of email I send, how recipients signed up, and that bounces, complaints and unsubscribes are suppressed automatically by Outflare. Show me the text before submitting it.
6. If my DNS is somewhere you can reach, help me add the DNS records Outflare lists for %s under Sending domains, exactly as shown.

%s`, emailsend.SESPolicy, domainOr(p), keyRule(p.ConnectURL, "the secret access key"))
	return Guide{Connector: "amazon_ses", Name: "Amazon SES", Minutes: 10,
		Summary:     "The cheapest way to send at volume, from your own AWS account: about $0.10 per 1,000 emails.",
		Needs:       []string{"An AWS account you can sign in to as an administrator", "Access to your domain's DNS"},
		Steps:       steps,
		Limits:      "New accounts are in the SES sandbox (verified recipients only, 200 a day) until AWS grants production access, usually within a day. Outflare shows the sandbox state on the provider.",
		AgentPrompt: prompt}
}

func postmarkGuide(p GuideParams) Guide {
	steps := []GuideStep{
		{Title: "Create a server", Who: "you",
			Body: "In Postmark, open Servers and Create server. Name it after your product, choose Live, and create it. Its default Transactional stream is the one Outflare uses.",
			Link: "https://account.postmarkapp.com/servers", LinkLabel: "Open Postmark servers"},
		{Title: "Copy the server API token", Who: "you",
			Body: "Open the server, then API Tokens, and copy the Server API token."},
		{Title: "Optional: copy the account API token", Who: "you",
			Body: "With the account token, Outflare adds sending domains and their DKIM and Return-Path records for you. Find it under Account, API Tokens. Without it, add your domain in Postmark under Sender Signatures yourself.",
			Link: "https://account.postmarkapp.com/api_tokens", LinkLabel: "Open account API tokens"},
		{Title: "Paste the tokens into Outflare", Who: "you",
			Body: "Paste the server token (and the account token if you have it) into the form on this page and press Connect.",
			Link: p.ConnectURL, LinkLabel: "Connect Postmark"},
		{Title: "Delivery events are set up for you", Who: "outflare",
			Body: "Outflare adds a webhook to the server for deliveries, bounces and spam complaints. Hard bounces and complaints are added to your suppression list."},
		{Title: "Add your sending domain", Who: "you",
			Body: "Add " + domainOr(p) + " under Sending domains and add the DKIM TXT and Return-Path CNAME records it lists at your DNS host."},
		{Title: "Get your account approved", Who: "you",
			Body: "New Postmark accounts can only send to addresses on their own domain until Postmark approves them. Request approval from the banner in Postmark, describing the email you send and how people sign up for it.",
			Link: "https://account.postmarkapp.com/", LinkLabel: "Open Postmark"},
	}
	prompt := fmt.Sprintf(`I want to send transactional and lifecycle email from my own Postmark account through Outflare. Please set up Postmark for me, step by step.

1. In Postmark, create a server for my product (ask me its name), type Live.
2. Stop and tell me where to copy its Server API token (the server, API Tokens), and where the Account API token is (Account, API Tokens). I will paste both into Outflare myself.
3. If the account is not approved yet, help me write the approval request: the kinds of email I send, how recipients signed up, and that bounces, complaints and unsubscribes are suppressed automatically by Outflare. Show me the text before sending it.
4. If my DNS is somewhere you can reach, help me add the DKIM and Return-Path records Outflare lists for %s under Sending domains, exactly as shown.

%s`, domainOr(p), keyRule(p.ConnectURL, "the API tokens"))
	return Guide{Connector: "postmark", Name: "Postmark", Minutes: 5,
		Summary:     "Known for fast, reliable delivery of transactional email, with a free tier of 100 emails a month.",
		Needs:       []string{"A Postmark account", "Access to your domain's DNS"},
		Steps:       steps,
		Limits:      "Until Postmark approves the account, it sends only to addresses on your own domain.",
		AgentPrompt: prompt}
}

func resendGuide(p GuideParams) Guide {
	steps := []GuideStep{
		{Title: "Create an API key", Who: "you",
			Body: "In Resend, open API Keys and Create API Key. Name it Outflare and choose Full access: a sending-only key cannot add domains or webhooks.",
			Link: "https://resend.com/api-keys", LinkLabel: "Open Resend API keys"},
		{Title: "Paste the key into Outflare", Who: "you",
			Body: "Paste the key (it starts with re_) into the form on this page and press Connect.",
			Link: p.ConnectURL, LinkLabel: "Connect Resend"},
		{Title: "Delivery events are set up for you", Who: "outflare",
			Body: "Outflare adds a webhook for deliveries, bounces and complaints, and keeps its signing secret. If Resend refuses, the provider page shows the URL to add under Webhooks in Resend, and a box for the signing secret."},
		{Title: "Add your sending domain", Who: "you",
			Body: "Add " + domainOr(p) + " under Sending domains and add the DKIM, SPF and MX records it lists at your DNS host. Resend verifies within minutes once they are in place."},
	}
	prompt := fmt.Sprintf(`I want to send transactional and lifecycle email from my own Resend account through Outflare. Please help me set it up.

1. Stop and tell me how to create the API key myself: Resend, API Keys, Create API Key, name Outflare, permission Full access (sending-only keys cannot add domains). I will paste it into Outflare myself.
2. Once I say it is connected, check Resend, Webhooks, for an endpoint pointing at Outflare. If there is none, tell me; Outflare's provider page shows the URL and where to paste the signing secret.
3. If my DNS is somewhere you can reach, help me add the records Outflare lists for %s under Sending domains, exactly as shown.

%s`, domainOr(p), keyRule(p.ConnectURL, "the API key"))
	return Guide{Connector: "resend", Name: "Resend", Minutes: 3,
		Summary:     "The quickest to set up, with a free tier of 3,000 emails a month.",
		Needs:       []string{"A Resend account", "Access to your domain's DNS"},
		Steps:       steps,
		Limits:      "The free plan allows one domain and 100 emails a day.",
		AgentPrompt: strings.TrimSpace(prompt)}
}
