# Site audit follow-ups

The site audits page now shows distinct pages per severity, hides notices
until a person asks to see them, and can roll templates up across locales.
These remain the next steps, in this order, for a manager to see what to
fix this week. None of them needs a new crawl or a higher page cap.

1. **Compare crawls and close stale groups when a crawl hits the page cap.**
   Show new, fixed, still open and regressed issues. A capped crawl should
   be allowed to confirm or merge groups a wider template now covers (for
   example `/id/products/*` once `/id/*/*` exists). Today a crawl that
   stops at the limit never confirms fixes, so leftover groups stay open.
2. **Refresh Search Console impact after every crawl** and sort by errors
   plus clicks, including the largest structural issues. The ticket model
   already has the field; it is filled when a brief is written, so the
   biggest issues often have no traffic number.
3. **One fix per product or article template, with a locale breakdown**,
   not ten tickets for the same bug. The issues list already folds
   locales; the fixes list and `syncFixes` grouping still treat `/de/…`
   and `/fr/…` as separate work.
4. **Recheck only the URLs in a fix**, and wait for that check to finish
   before marking the fix done. Starting a full-site crawl to verify one
   group is too slow, and a capped crawl cannot confirm it.
5. **Spend the existing page budget better** (normalise www, treat a
   redirect as a redirect, prefer sitemap and high-click URLs) before
   raising the cap, and return every crawled page from the pages API.

Linear export, CJK length thresholds, and adding CrUX or Search Console
coverage into the audit can follow after those.
