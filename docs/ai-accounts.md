# AI Accounts

AI Accounts lists every AI subscription and API account the workspace's people pay for: Claude, ChatGPT, Cursor and Copilot plans, and the Anthropic and OpenAI APIs. For each one it keeps the AI tool, the email the account is on, the plan and seats, the price and when it renews, who pays (the company or the person), how many resets the AI company still lets it use, and the people it belongs to.

It is being built and is switched on for a few workspaces first ([workspace features](workspace-features.md), `ai_accounts`). Until it is on for a workspace, its pages and API answer as if they didn't exist.

## Who sees what

- **Your own accounts** (`my_ai_accounts.read`, `my_ai_accounts.manage`): the accounts you belong to. Members and viewers have this. You can add accounts you belong to and change or remove them, including their price.
- **Everyone's accounts** (`ai_accounts.read`, `ai_accounts.manage`): every account in the workspace, and adding or changing anyone's. Owners and admins have this. It includes your own.
- **AI spend** (`ai_spend.read`): the price on accounts you don't belong to. Without it, those prices are left out (`price_hidden`).

A workspace can change what its roles allow under Settings → Members → Roles.

## Views

- **Accounts**: every account with its tool, email, owner, renewal, price, who pays and resets left.
- **Spend** (with AI spend): what company-paid plans cost a month and a year, spend by AI tool, renewals in the next 30 days, and accounts that need a look (owner left, renewal date passed, no price).
- **People** (with everyone's accounts): each person, the accounts they own and what the company-paid ones cost.
- **Timeline**: renewals on a calendar. Usage limit resets join them once Outflare for desktop reports them.
- **Usage**: which account to use, what runs out before it resets and which plans are too small or too big. It needs readings from Outflare for desktop; until then it can show an example, marked as one.

## Receipts (Outflare Cloud)

Forward the receipts of your AI subscriptions to the workspace's [inbound address](inbound-email.md) and Outflare reads them: the AI company, plan, seats, amount and the period paid for. Only receipts signed by the company that sent them (or the Stripe, Apple or Google receipt naming it) are read.

Each receipt is matched to an account, in this order: the account email it names, the receipt number or card of an earlier receipt, the only account of that AI tool belonging to whoever forwarded it, and the only account due to renew around the charge. The account's next renewal, plan, seats and US price follow it. Receipts it can't place wait under **Needs review**: add a new account from one, assign it to an account (later receipts like it then match by themselves), or ignore it.

The **Receipts** tab has the forwarding address and the steps for Gmail (a filter file to import, the search to paste, or instructions to give an agent), a Google Workspace admin rule that covers every inbox, and Outlook. Members link the personal addresses they forward from, and confirm Gmail's forwarding there with one click.

Receipts in another currency are converted to US dollars with the European Central Bank's reference rate for the day they were charged (or the last working day before it), and show both: `$14.80 · charged as S$20.00`. The dollar amount becomes the account's price like any other receipt. A receipt that arrives before that day's rate is published keeps only its amount as charged until the rate arrives, within 6 hours of publication.

API: `GET .../ai-accounts/receipts`, `POST .../ai-accounts/receipts/{id}/assign` and `.../ignore`. MCP: `list_ai_receipts`, `get_receipt_forwarding`, `assign_ai_receipt`, `ignore_ai_receipt`.

## Outflare for desktop

A small app each person installs once, for Mac, Windows and Linux. It sits in the menu bar or system tray, starts when they sign in, and every 10 minutes reads how much of each limit (the 5-hour and weekly windows, monthly requests, credits) their AI tools have used and when each resets: Claude, ChatGPT and Codex, Cursor and GitHub Copilot. It sends only those numbers, the plan and banked resets. Prompts, files and the tools' sign-ins never leave the computer.

It reads each tool two ways, which the app labels: **official** (the tool's own documented way) and **extended** (the endpoints the tool's own app calls, with the sign-in already on the computer). It only reads: it never sends a prompt, uses a reset or buys anything, and backs off when a tool asks it to. The one sign-in it renews is Claude Code's, which Claude Code itself renews only while it runs: when it has expired, the app renews it the same way and saves it back, so Claude Code stays signed in.

**Linking a computer.** The app shows a code like `ABCD-2345` and opens Outflare; sign in, check the code matches and approve it (**Link computer**). The computer is then linked to you in that workspace until you or an admin unlink it under AI accounts → **Computers**, which also shows when each last reported and any tool it couldn't read (sign in to that tool again).

**Where readings go.** To the account signed in with the same email anywhere in the workspace (a shared login adds you to it), else your only account for that tool, else a new account owned by you. Usage then shows on each account and in the **Usage** view, kept for 90 days.

The operator can switch a source off for every computer (a tool changed or asked us to stop) and the apps stop reading it within minutes, without an update. API: `POST /v1/desktop/links`, `POST /v1/desktop/token`, `GET /v1/desktop/config` and `POST /v1/desktop/reports` for the app (with its `ofd_` device token), and `GET`, `POST` and `DELETE .../ai-devices` for people.

## Accounts and people

An account belongs to one or more people in the workspace: its owner, and others who also sign in to it. When everyone who owns an account has left the workspace, the account stays and shows **owner left**, so someone can cancel it or take it over.

Only company-paid accounts count in spend. An account someone pays for themselves is still listed, with its usage and resets.

## In the API

```sh
# Add your Claude Max
curl -X POST https://app.outflare.ai/v1/workspaces/acme/ai-accounts \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"provider": "claude", "name": "Claude Max — Design", "account_email": "ana@acme.example",
       "plan": "Max 20x", "billing_cycle": "monthly", "renews_on": "2026-11-14", "price_cents": 20000}'

# List them: your own, or everyone's with ai_accounts.read
curl https://app.outflare.ai/v1/workspaces/acme/ai-accounts -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

`PATCH .../ai-accounts/{id}` changes the fields you send; an empty string clears optional text and dates, and `members` replaces the list. `DELETE` removes an account added by mistake; one that ended should stay with `cancelled: true`.

MCP tools: `list_ai_accounts`, `get_ai_account`, `create_ai_account`, `update_ai_account`, `delete_ai_account`.

## Coming next

Usage history over time (forecasts and plan fit), usage resets on the timeline, and the Anthropic and OpenAI admin APIs for API spend.
