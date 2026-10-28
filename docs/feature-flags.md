# Feature flags

A feature flag turns something on for the visitors you pick, or splits them between versions so you can see which works better. Your website reads flags through Outflare's tracking script, and your servers through the API, and both get the same answer for the same visitor. [A/B tests](ab-tests.md), with results, are built on flags.

## Kinds

- **A switch** has one version, `on`. Visitors it covers get `"on"`; everyone else gets `false`.
- **A split** has two to ten versions, each with a weight, such as `control` 50 and `faster` 50. Each visitor gets one of them. A version can carry content (any JSON up to 4 KB, such as `{"headline": "Ship faster"}`), so a page can show a new headline without a deploy.

A flag is off until you turn it on. When it is off, every visitor gets `false`, and the website doesn't even learn that it exists.

## Who gets it

- **Rules.** Conditions every one of which must hold: the page's path, site (host), full address, a parameter of the address (`query.utm_source`), where the visitor came from (referrer), their device (desktop, mobile, tablet) and browser language. Servers can also send `signed_in` (true when they pass a `user_id`) and their own properties (`property.plan`). Each rule is `is`, `is_not`, `contains`, `not_contains`, `starts_with`, `ends_with` (any of its values, case ignored), `exists` or `not_exists`.
- **Rollout.** Of the visitors who pass the rules, the share that gets the flag at all, 0 to 100%. Raising it only adds visitors; nobody who had it loses it.
- **Brand.** A flag for one brand reaches only that brand's site (the brand on its site key); a flag with no brand reaches every site.
- **Website or servers only.** A flag with `on_site` false is never sent to the website script, so visitors can't see it exists. Servers still read it.

## Sticky versions

The version comes from a hash of the flag's own salt and the visitor's id, so the same visitor gets the same version on every page, every visit and every server, with nothing stored about them. Two flags split visitors independently. Changing the weights of a split that is on moves only the visitors near the boundaries that moved; the page warns about it. Changes reach visitors within 10 seconds.

The website script uses the visitor's `anonymousId`. A server should pass the same id (`outflare.anonymousId()` in the page, or the `of_aid` cookie) as `anonymous_id`, so the server and the page agree. With only a `user_id`, the version follows the user instead.

## On your website

Add `data-flags` to the tracking script's tag:

```html
<script async src="https://app.outflare.ai/site.js" data-key="ofk_test_not_a_real_key" data-flags></script>
```

The script then asks for the visitor's flags as the page loads (and again after in-app navigation, since rules can depend on the page), and keeps the last answer for the visit so the next page starts with it.

```html
<script>
  window.outflare = window.outflare || [];
  function onFlags(fn) { outflare.onFlags ? outflare.onFlags(fn) : outflare.push(["onFlags", fn]); }

  onFlags(function (flags) {
    if (flags["pricing-headline"] === "faster") {
      document.querySelector("h1").textContent = outflare.flagPayload("pricing-headline").headline;
    }
  });
</script>
```

- `outflare.onFlags(fn)` runs `fn` with every flag that is on (its version, or `false`) once they are known, at once when they already are.
- `outflare.flag("key")` returns the version or `false`. It is `false` until the flags arrive, so use `onFlags` when it matters. Calling it on a page without `data-flags` starts asking.
- `outflare.flagPayload("key")` returns the version's content, if it has any.
- `<html>` gets a class for each version the visitor has, `of-flag-<flag>-<version>` (`of-flag-pricing-headline-faster`), so a test can be done in CSS alone.
- Reading a flag (or, with `data-flags`, its class going on `<html>`) sends Experiment Viewed once per visit, so the flag's [A/B test](ab-tests.md) counts the visitor from then.

### No flicker

The script loads without holding up the page, so the original can show for a moment before a version replaces it. To prevent that, put this in `<head>` before anything else: the page stays hidden until the flags arrive, and never longer than one and a half seconds.

```html
<script>document.documentElement.classList.add("of-wait"); setTimeout(function () { document.documentElement.classList.remove("of-wait"); }, 1500);</script>
<style>.of-wait body { opacity: 0 }</style>
```

### Preview a version

Add `?of_flag=pricing-headline:faster` to any page's address to see that version yourself, with its content, even before the flag is turned on. Several go in one parameter (`?of_flag=pricing-headline:faster,chat-widget:on`), and `off` shows a flag as off. The choice lasts for the rest of your visit, applies only to your browser and is taken off the address straight away.

## On your servers

`POST /v1/workspaces/{ws}/flags/evaluate` with a token that has `analytics.read` (MCP `evaluate_flags`):

```json
{ "anonymous_id": "the visitor's id", "url": "https://northpeak.example/pricing", "properties": { "plan": "pro" } }
```

It returns every flag that is on, with the version or `false`, any content, and why (`match`, `rules`, `rollout`). `keys` limits it to some flags. Nothing is stored.

## Managing flags

**Analytics → Feature flags** lists them with a switch to turn each on or off. The API (`GET/POST .../flags`, `GET/PATCH/DELETE .../flags/{id or key}`) needs `analytics.read` to read and `analytics.manage` to change; the MCP tools are `list_flags`, `get_flag`, `create_flag`, `update_flag` and `archive_flag`. A flag's key can't change, since code asks for it. Archiving turns a flag off for good and frees its key; archived flags stay listed with `status=archived`. A workspace can have 200 live flags.

## Limits

The website asks `POST /v1/site/flags` with its site key, from the key's allowed origins, at most 6,000 times a minute per key. Each server process keeps a workspace's flags in memory for 10 seconds, so a page view costs no database query. Requests, their time and the share answered from memory are on the Feature flags row of the [Grafana dashboard](monitoring.md).
