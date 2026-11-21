# Switching from PostHog

A guide for the developer who moves a site or app from PostHog to Outflare. It maps each thing you do with PostHog today to its Outflare counterpart, in the order that is safest to do it. Every Outflare feature named here is off until you turn it on, and both tools can run side by side while you compare numbers.

## The order to do it in

1. **Add the website script** next to PostHog's ([Website tracking](website-tracking.md)). Pageviews, visits and sources start at once.
2. **Move your custom events** (`posthog.capture`) to `outflare.track`, or name the clicks without code.
3. **Identify people from your server**, and send payments and subscriptions from your billing.
4. **Move feature flags and A/B tests.**
5. **Turn on errors and recordings** if you used them.
6. **Leave your own team out**, compare a week of numbers, then remove PostHog.

## Call by call

| In PostHog | In Outflare |
| --- | --- |
| The snippet, `posthog.init` | One script tag with the site's key ([Website tracking](website-tracking.md#set-it-up)). There is nothing to initialise. |
| Automatic pageviews | Sent by the script, including in-app navigation. |
| `posthog.capture("Signup Started", {plan})` | `outflare.track("Signup Started", {plan: "pro"})` in the browser, or `track` in a [server library](sdks.md). |
| Autocapture | Two switches on the site's script: **Capture clicks on buttons and links** and **Capture form submits** ([autocapture](website-tracking.md#clicks-and-form-submits-without-code-autocapture)). What a visitor typed is never read. |
| Actions (a named click or pageview) | `data-outflare-event="Plan Chosen"` on the element, or **Analytics**, **Named clicks** to name it with no change to the site. |
| `posthog.identify(userId, traits)` | From your server: `identify` in a server library, with your user id and the visitor's `anonymousId`. From the browser: `outflare.identify(userId, signature)`, where your server signs the id ([browser identify](website-tracking.md#identify-from-the-browser-signed-by-your-server)). A page can't name a person on its own word. |
| `posthog.reset()` on sign-out | `outflare.reset()`. |
| `posthog.group(...)` | `group` in a server library: the person joins a company record. |
| `posthog.isFeatureEnabled`, `getFeatureFlag`, `getFeatureFlagPayload`, `onFeatureFlags` | `outflare.flag("key")`, `outflare.flagPayload("key")`, `outflare.onFlags(fn)` with `data-flags` on the script tag ([Feature flags](feature-flags.md#on-your-website)). |
| Server-side flags with local evaluation | `LocalFlags` in the TypeScript and Python libraries: `decide`, `variant`, `isEnabled`, `payload` ([decide in memory](feature-flags.md#decide-in-your-own-servers-memory)). |
| Bootstrapping flags into the page | `flagsBootstrapTag(decisions, anonymousId)` in the page's head, so there is no flicker. |
| `$feature_flag_called` exposure events | Reading a flag in the browser sends Experiment Viewed once per visit. From a server, call the event library's `exposed`. |
| Experiments | [A/B tests](ab-tests.md) on a flag, with goals from any event, payment or named click. |
| `posthog.captureException`, error tracking | **Capture errors** on the site's script, or `outflare.captureException(err)`; `captureException` in the server libraries. Upload source maps to read minified stacks ([Errors](error-tracking.md)). |
| Session replay | [Recordings and heatmaps](session-recordings.md), with their own On/Off switch. |
| Internal and test user filters | **Analytics**, **Internal and test people** ([leaving out your own team](insights.md#leaving-out-your-own-team)). |
| Revenue events sent from billing | `POST /v1/brands/{brand}/transactions` for payments and refunds, and `POST /v1/brands/{brand}/billing-subscriptions` for subscriptions ([Transactions](transactions.md)). |
| Person properties set from billing (plan, MRR, status) | Worked out for you from the subscriptions and payments you send: `mrr`, `billing_status`, `billing_plan`, `lifetime_revenue`, `customer_since`. |
| Insights: trends, funnels, retention, paths, stickiness, lifecycle | The same six under **Analytics** ([Insights](insights.md)). |

## Things that differ

- **Event names are stored in one style.** `Signup Started`, `signupStarted` and `signup_started` are all stored as `signup_started`, so names that differ only in case or spacing in PostHog become one event here ([names](customers.md#product-events)). Dotted names such as `product.activated` stay as they are.
- **Flag keys are lowercase**, with digits, hyphens and underscores. Rename a key such as `NewCheckout` to `new-checkout` when you recreate the flag, and change it in your code in the same release.
- **Identify comes from your server, or is signed by it.** PostHog trusts `posthog.identify` from any page. Outflare does not, so nobody can pose as another user from a browser console.
- **Money is sent once.** Send each payment as a transaction or as an `Order Completed` event with the same id; Outflare keeps it once either way, and counts `payment.succeeded`. Don't also send a custom "purchase" event for revenue reports.
- **Dates on old events.** Events carry your own `timestamp`, so a replay of recent history lands on the right days. Send each with the `messageId` it had, and sending it twice stores it once.

## Checking it worked

- The Product events page lists every event name the brand has received: each `posthog.capture` name you moved should show there, in its stored style.
- [Developer setup](developer-setup.md) checks what your server sends: sign-ups, orders, payments counted once, refunds finding their payment.
- Build the funnel you trust most in both tools over the same week. Expect small differences from ad blockers and from the people you left out, not from missing steps.
