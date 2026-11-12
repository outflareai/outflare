# Link ROI

The **Link ROI** page in the SEO section sits next to [Link orders](coverage.md). For each published, paid or verified link order it shows the donor price and already-stored changes on the target page 30, 60 and 90 days around publication:

- Search Console clicks, impressions and average position (`search_daily`)
- Stored keyword positions (`seo_snapshots` for the keywords report)
- Distinct referring domains already stored against the target URL in `backlinks`

It does not call Ahrefs, DataForSEO or Search Console, and it does not claim that a link caused a change. Amounts stay in their own currency; USD and EUR are never summed. Cost per click or referring domain gained is only shown when the change is positive and the order has a donor or quoted amount.

The API is `GET /v1/brands/{brand}/link-roi` (`coverage.read`). Agents use `get_link_roi`. Orders that are not yet published are counted in the totals and have no change windows.

An example of the page, with fictional data, is in [example.html](link-roi/example.html).
