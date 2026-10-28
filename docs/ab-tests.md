# A/B tests

An A/B test asks a [feature flag](feature-flags.md) a question: which of its versions does best at a goal? The flag splits visitors between versions; the test says which version is the control, what counts as success, and when it measured. Outflare then shows how each version is doing, with numbers you can trust even if you check every day.

A/B tests are separate from SEO experiments, which compare groups of pages rather than visitors.

## Setting one up

1. Make a flag with two or more versions, such as `control` 50 and `faster` 50, and read it on the page (below).
2. Add a test of it (**Analytics → A/B tests**, `POST /v1/workspaces/{ws}/ab-tests`, or MCP `create_ab_test`):

   ```json
   {
     "flag": "pricing-headline",
     "hypothesis": "A shorter headline gets more signups",
     "control": "control",
     "goals": [{ "kind": "event", "name": "signup_started" }, { "kind": "page", "path": "/thanks" }]
   }
   ```

   - **Goals.** One to five. The first is the one the test is judged on; the rest are watched alongside it. A goal is a product event by its catalog name (`{"kind": "event", "name": "signup_started"}`) or a view of a page by path (`{"kind": "page", "path": "/thanks"}`; `/docs/*` matches every page under `/docs`).
   - **Control.** The version the others are compared with. Without it, `control` if the flag has one, else its first version.
   - **Smallest change worth finding** (`min_effect`). The relative change the test is tuned to detect: `0.1` (+10%) for small tweaks on a busy site, `0.25` (+25%, the default) for most tests, `0.5` (+50%) for bold changes on a small site. A test tuned for bigger changes stops a little sooner when the change really is big, and later when it is small. For example, at a 4% sign-up rate, a version that truly lifts it by 50% is safe to stop after about 2,400 visitors per version when tuned for +25%, against 3,900 when tuned for +10%. A true +10% lift takes about 52,000 per version when tuned for +25%, against 47,000 when tuned for +10%.
3. Start it (`POST .../ab-tests/{id}/start`, MCP `start_ab_test`). The flag must be live with at least two versions, the control among them. A flag that is off is turned on. A flag runs one test at a time. The control, goals and smallest change are fixed from here on, so the results can't be reshaped after the fact; the name and hypothesis can still change.
4. Watch the results (`GET .../ab-tests/{id}/results`, MCP `get_ab_test_results`).
5. End it with the version you chose (`POST .../ab-tests/{id}/end` with `{"decision": "faster"}`, or `""` for none). The results are kept as they stood. The flag is left as it is: ship the winner by changing the flag's weights, or archive it once the code no longer asks for it.

## Who is counted

A visitor joins a test the first time a page **shows them** the flag's version, not when the flag is decided. Pages that never show the change don't dilute the results. The website script sends a track event, Experiment Viewed (stored as `experiment_viewed`, with properties `flag` and `variant`), once per visit for each flag:

- when the page reads the flag with `outflare.flag("key")` or `outflare.flagPayload("key")`, or reads it from what `outflare.onFlags(fn)` gives;
- with `data-flags` on the script's tag, for every flag whose class (`of-flag-<flag>-<version>`) goes on `<html>`, since a CSS-only test shows its version that way. Leave `data-flags` off pages that don't show a test if you want those visitors left out.

Previews (`?of_flag=key:version`) send nothing: that is you looking, not a visitor.

A server that shows a version itself (a server-rendered page, an email, an app screen) sends the same event through `POST /v1/workspaces/{ws}/customer-events`:

```json
{ "type": "track", "event": "Experiment Viewed", "anonymousId": "the visitor's id", "properties": { "flag": "pricing-headline", "variant": "faster" } }
```

Use the same `anonymousId` as the website (`outflare.anonymousId()`, or the `of_aid` cookie), so the visitor's exposure and their goal events match.

A visitor's version is the first one they saw. A conversion counts when a goal event of theirs (matched on `anonymousId`) follows their first view, before the test ends. Each visitor counts once per goal, however many times they convert.

## Reading the results

For each goal and each version against the control:

- **Visitors and conversions**, and the rate.
- **Chance to beat the control**: the chance the version's true rate is above the control's (Bayesian, with a flat prior). "97% likely to beat Control" means what it says.
- **Lift**: the relative change of its rate over the control's, with a 95% range (`+18%, range +4% to +33%`).
- **p-value**: an *always-valid* p-value (a mixture sequential test, tuned by the test's smallest change worth finding). A classic p-value turns up false winners when people look every day and stop at the first good-looking result; this one stays honest however often you look. Below 0.05 it is **safe to stop**. That honesty costs traffic: it needs more visitors than a classic test that is only looked at once.
- **Sample ratio check**: if the split between versions is far from the flag's weights (a chance under 0.001 of happening by luck), something is losing or double-counting visitors, such as a redirect, a cache or bots. The results then shouldn't be trusted, and there is no winner.

The **winner** is a version at least 95% likely to beat the control with a p-value under 0.05 on the first goal; or the control, when every other version is at most 5% likely to beat it with the same proof; otherwise none yet. The maths is exact, with no sampling, so every reload gives the same numbers.

Only conversion-rate goals are supported today. Revenue and other averages, variance reduction (CUPED) and AI suggestions are planned.

## Access and limits

Reading needs `analytics.read`; adding, changing, starting and ending need `analytics.manage`, as for flags. A user limited to some brands sees the tests of the flags they see. A workspace can have 500 tests.

Results are counted from the workspace's product events each time a running test is opened, within 30 seconds; a finished test reads what was kept. Counting time and failures are on the Feature flags row of the [Grafana dashboard](monitoring.md).

## Events

`ab_test.created`, `ab_test.updated`, `ab_test.started` and `ab_test.ended` are audit actions and product events ([product analytics](product-analytics.md)). Starting a test that turns its flag on also records `flag.updated`.
