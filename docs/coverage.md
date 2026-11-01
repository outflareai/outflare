# Coverage implementation

Coverage stores a brand's external placements, publisher relationships and commercial delivery. Customer-facing usage documentation belongs at [outflare.ai/docs](https://outflare.ai/docs).

## Resources and permissions

The API lives under `/v1/brands/{brand}/coverage`. Publishers reference CRM company records; multiple publisher domains can reference the same company, and each website can have multiple placements. A placement may have no URL yet, and different inventory positions on one URL remain distinct. Format, commercial arrangement, pipeline stage and observed page health are independent fields.

`coverage.read` and `coverage.manage` cover placements, maintenance and discovery. Finance resources require `coverage.finance.read` or `coverage.finance.manage`. Agreement approval additionally requires `coverage.approve` and a human principal. CRM conversation context and recorded attribution retain their existing CRM and analytics permissions. Private saved views remain private to their creator. Every resource and reference is scoped to workspace and brand.

Payments are immutable entries with explicit reversals. Agreement amounts, allocations, invoices, credits and payments use integer currency minor units. Reports separate commitments, invoiced and paid amounts, and never add currencies together. Editing approved terms invalidates approval. Recording or approving an agreement never purchases inventory or sends money.

## Observation and maintenance

`coverage.check` runs polite page reads with safe URL handling, robots and opt-out checks. The connected reader uses the workspace's explicit crawl connection and budget. Native reads remain available without a provider. Jobs preserve observed link destinations, anchors and rel tokens.

Page availability is separate from verified brand presence. First verification requires a positive mention or backlink observation. A network failure preserves previous positive evidence. Two agreeing observations are needed for confirmed page loss or missing/changed expected links. A rotating ad or newsletter cannot be declared delivered or absent from one sampled page; use the delivery records and evidence instead.

Fixed maintenance rules create owned work for publication deadlines, confirmed losses, first verification, earned relationship review and potential accuracy problems. Finance reminders have separate permissions. Repeated signals do not duplicate open work. Dismissing an incident suppresses the same evidence; recovery followed by another loss can create a new incident. Snoozing work does not stop monitoring.

## AI assistance

`coverage.review` uses the workspace's model and budget to compare page evidence with published customer-facing Knowledge. Results retain source versions, the external quote and the retrieved Knowledge passage. Source edits invalidate old results; withdrawn Knowledge is removed from returned assessments. Classification and outreach drafts are suggestions for review, with thank-you and link requests as distinct purposes. No scraped instructions receive tools or execute actions.

Placements can opt into fixed scheduled accuracy checks with `monitor_accuracy`. Cadence is at least 24 hours and no more frequent than page monitoring. Unchanged page text, purpose and retrieved Knowledge versions reuse an existing successful assessment before a model call. Paused and archived placements are excluded. Review findings create work; sending outreach remains a separate approved action.

`coverage.extract` reads explicitly supplied commercial document text and returns quoted proposed agreement fields. Values are independently validated and restricted to supported commercial fields. Changing the source version invalidates its extraction. Applying selected suggestions opens the ordinary agreement form; validation and reapproval still apply.

## Discovery and reports

Reconciliation imports the brand's existing news coverage, backlink and link-prospect sources. Each call processes up to 2,000 unseen source records, records receipts for invalid inputs as well as accepted candidates, and returns `has_more` for continuation. Canonical URLs deduplicate pages while history preserves each source. Provider discovery does not establish publication or verify delivery.

Reports distinguish discovery, claimed publication, first verified presence and imported baseline inventory. Page availability/loss history, retention evidence windows and unknown observations have explicit definitions. Commercial delivery reports remain source claims; overlapping submissions can overlap. The attribution endpoint reads actual recorded referrer sessions and attribution credits, deduplicated by publisher website, with model/window and currency labels. It requires analytics permission and does not infer causal lift.

Report snapshots are immutable server-computed results. They exclude live privileged finance and attribution panels. Exports preserve the displayed snapshot instead of silently replacing it with current figures.

## Validation

`TestBrandCoverage*` exercises brand isolation, permissions, money constraints, imports, source receipts, page loss/recovery, task recurrence, fixed schedules, workspace-only provider credentials, quoted AI results, version invalidation, snapshots and attribution deduplication. The standard API contract validator checks requests, responses and scope declarations. Run the repository race-test shards and regenerate both SDKs after contract changes.
