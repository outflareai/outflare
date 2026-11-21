# Partnerships

Partnerships is the one menu for every site and person a brand works with. Its tabs follow the work in order, and each has its own address (`/{workspace}/{brand}/partnerships/<tab>`):

| Tab | What it shows | Built on |
| --- | --- | --- |
| **Find** | Sites worth being on, ranked: they link to or mention your competitors but not you. Links to pages that lost links and to sellers' price lists. | [Presence](coverage.md) opportunities, Link building, the [Link orders](link-orders.md) catalog |
| **Offers** | One board of every offer, order and agreement with one site or person, paid or free: offers sent from a partner program, link orders and Presence agreements. Columns: Draft, Offer sent, Negotiating, Agreed, Live, Done (and, when shown, declined and lost). A card moves by itself as its status changes. | Partner proposals, link orders, coverage agreements |
| **Live** | Everywhere the brand appears right now, earned or paid. | Presence placements |
| **Affiliates** | Referral, affiliate and influencer programs. | [Partners](partners.md) |
| **Payments** | What you owe, in three lists: needs you (a live link to approve, a commission due), waiting or held (not live yet, or the link was lost before payment), and paid. Outflare never moves money. | Partner payouts, link orders, coverage payments |
| **Results** | Spend on links, live paid links, cost per paid link and how often a price was agreed, then [Link ROI](link-roi.md). | Link orders, Link ROI |

The older pages (Presence, Partners, Link orders, Link ROI, Link building and Link autopilot) still open at their own addresses, and count as Partnerships in the menu and the breadcrumb, so old links keep working. They are no longer listed on their own in the SEO, Outreach and Search menus.

Each tab shows only what the signed-in person may read: offers from partner programs need `partners.read`, link orders `coverage.read`, and agreements and coverage payments `coverage.finance.read`. The board is put together in the browser from the existing endpoints, so there is no new API; agents use the same calls (`list_link_orders`, the partner proposal and payout calls, and the coverage resources).

## Offers, counter-offers and the removed-link rule

**New offer** on Offers opens the offer form as a page of its own. Start it from a saved template, or save the one you've written with **Save as template** (`/v1/brands/{brand}/partner-offer-templates`).

The site owner can accept, decline or suggest changes up to three times. A counter-offer can change only the price and, for each deliverable, which of their pages the link goes on, the link text and the publish date. Where the link points, its type, that it stays up for good and that nothing is paid before it is live are fixed. Each round shows on the offer; **Accept these terms** (`accept_counter`) makes the latest one the agreement, spreading a new price over the paid milestones in their current proportions. To answer with other terms, revise the offer and invite them again.

**When a paid link is removed**, on Payments, is the brand's rule (`/v1/brands/{brand}/partner-link-rule`): restore within a number of days or refund, for a number of months (the default is 14 days and 12 months) or forever; a refund that falls evenly to nothing over the months; or only a warning. It is copied into each offer when it is sent, so an agreement keeps the rule it was made under.
