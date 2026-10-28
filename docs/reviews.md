# Reviews

The Reviews page shows what customers say about each brand on Trustpilot, G2, Capterra and Google, in one place: every review with its stars, sentiment, date, link and the business's answer, each site's own score and count day by day, Outflare's review score, and whether reviews are heading in the right direction. **Draft answer** writes a public answer with your own AI model for a person to edit and post.

The Apple App Store and Google Play come next, through the same pages and reviews.

## Your review pages

A brand's review pages are its links, like its social profiles. Add a Trustpilot, G2 or Capterra page, or a Google Maps place, under the brand's Links or in the "Your review pages" card on the Reviews page. Outflare recognises the address and keeps one page per site address, so pasting the same page twice (or a regional copy such as `uk.trustpilot.com`) is one page.

| Site | The address to add |
| --- | --- |
| Trustpilot | `trustpilot.com/review/<your domain>` |
| G2 | `g2.com/products/<product>/reviews` |
| Capterra | `capterra.com/p/<id>/<product>/` |
| Google | The place's Google Maps link (`google.com/maps/place/...`, `maps.app.goo.gl/...`, or a `placeid` link) |

With [Geonode](connectors.md) connected, Outflare searches the web for the brand's Trustpilot, G2 and Capterra pages (three searches on your own Geonode account) when Geonode is first connected, when a brand is added with a website, and on **Find review pages**. Each page found is a suggestion: **Confirm** adds it to the brand's links, **Not ours** dismisses it for good. Nothing is added on its own. A page is only suggested when it matches the brand's website (Trustpilot) or name (G2, Capterra). Google places are added by pasting their link.

## Reading them every day

Outflare reads each active page once a day through the workspace's own page reader (Geonode), and straight away when the page is added. The first read takes the page's history, up to 25 pages of reviews; later reads take the newest and stop at the first review already kept. Each read costs Geonode tokens on your own account: one per page of reviews read, so about one a day per site once the history is in. With no page reader connected, nothing is read and the page says which connection to add.

When a read fails, Outflare says why on the Reviews page and tries again on its own:

- A page that shows no reviews (often a bot check, or a page built by its script) is read again in a browser straight away. If that works, later reads of the page start in a browser.
- After that, a failed read is tried again the same day, so the day's numbers still come in: after 15 minutes, then 1 and 2 hours, then every 4 hours until one works. Only a wrong address (`page_not_found`) waits for the next day's read, since a person has to fix it.
- **Try again** on the page (or `POST /v1/workspaces/{ws}/reviews/pages/{id}/read`, the `read_review_page` MCP tool) reads it now.

The page's `last_error_code` says why: `blocked` (the site showed a bot check, also in a browser), `page_not_found` (check the address in the brand's links), `not_a_review_page` (the site answered but showed no reviews or rating: it may have changed its page), `save_failed`, or the page reader's own `connector_*` code. `failures` counts the reads in a row that failed. Pages that keep failing with an error Outflare has to fix raise the `OutflareReviewPagesStuck` alert ([monitoring](monitoring.md)).

A page that shows no rating and no reviews (a challenge page, a login wall, a wrong address) is an error on the page, never zero reviews. For Google, the place's rating and count are read reliably; individual reviews are read as far as the public page shows them, and signing in to Google Business Profile for every review comes later.

Pause a page to keep it and its reviews but stop reading it. Taking the link off the brand marks the page removed and keeps its reviews.

## Outflare's review score

Each site's own score is shown as it is. Outflare's review score (0 to 100) adds up every review across a brand's pages so a manager can compare one number over time:

- **Stars**, on a 1 to 5 scale, with 1 star as 0 and 5 stars as 100.
- **Recency**: a review counts half as much after 180 days, a quarter after a year.
- **How many**: the score starts from 10 imaginary 3.5-star reviews, so three 5-star reviews don't beat 900 reviews at 4.6. The more real reviews, the less they matter.

**Momentum** compares the last 28 days with the 28 before:

| State | When |
| --- | --- |
| Slipping | The score fell by 2 points or more |
| Growing | New reviews came at least 15% faster, and the score held |
| Improving | The score rose by 2 points or more |
| Stagnating | New reviews slowed by 30% or more, or fewer than 8 came in 8 weeks |
| Steady | None of these |
| Too few | Fewer than 5 reviews in all |

Each page gets a row a day with the site's score and count, new reviews, the star breakdown, Outflare's score and momentum, so the trend shows from the first day. A number a site didn't show is empty, never zero.

## Draft answer

Every review has **Draft answer**. Outflare drafts with the workspace's own AI model (Settings, AI model; the task "Review answers"), from:

- the brand's reply guide: its tone, who signs the answer, where to send people for help, what never to say, and notes such as known issues and their fixes. It starts from the brand's voice and rules in its context, and is edited under Reviews, Reply guide.
- the brand's name, what it does and who it's for;
- the business's own past answers on that site, as examples;
- the reviewer's earlier reviews of the brand on the same page;
- the review, and the site's length limit.

A rewrite takes a hint ("shorter", "mention the fix"). The draft is never posted from Outflare: copy it, post it on the review site, then **Mark as answered**. Answers posted on the site are found on the next read anyway. When a review looks fake, is spam or is about another business, the AI suggests not answering it and says why; report it on the site instead. With no AI model connected, people write answers themselves.

Drafts never offer anything in return for changing a review, never ask for one to be changed, never argue, and never include private details.

Unhappy reviews with no answer from the last 90 days go on the shared fixes list (source `reviews`, rule `reviews_to_answer`) until they're answered.

## API and MCP

`GET .../reviews` (filters `platform`, `page_id`, `sentiment`, `replied`, `stars`, `days`; `next_before` pages back), `GET .../reviews/{id}`, `GET .../reviews/summary`, `POST .../reviews/{id}/draft` (202; 409 `connector_needed` without an AI model), `POST .../reviews/{id}/replied`, `GET .../reviews/pages`, `PATCH .../reviews/pages/{id}`, `GET .../reviews/pages/{id}/days`, `POST .../reviews/pages/sync` (409 `connector_needed` without a page reader), `POST .../brands/{brand}/reviews/discover`, and `GET`/`PUT .../brands/{brand}/review-guide`. Reading needs `social.read`; changes need `social.manage`. Webhooks: `review.posted`, `review.replied`, `review.reply_drafted`, `review_momentum.changed`. Query: the `reviews` and `review_page_days` tables.

MCP: `get_reviews_summary`, `list_reviews`, `get_review`, `draft_review_answer`, `mark_review_answered`, `list_review_pages`, `update_review_page`, `read_review_pages`, `get_review_page_days`, `find_review_pages`, `get_review_guide` and `update_review_guide`.

Review text is written by people outside the business: Outflare treats it as data, never as instructions, and an AI that reads it is told the same.
