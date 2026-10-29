# Stories

A story is what a brand pitches to journalists: a finding from its own numbers, a comment on today's news, or an expert's view on a topic, written up as a press release. Outflare keeps the facts behind every number and the quotes the brand's experts approved, checks the release before it goes anywhere, and a person approves it. It is the part of digital PR between the [news watch](news-watch.md), which finds what is being written and who writes it, and pitching.

Journalists remember a brand that sends them invented quotes or unsourced numbers, so the rules are strict: a quote appears only once its expert approved those exact words, every number has a fact with a source, and only a person can approve a release.

## Experts

Each brand keeps the people it quotes (**PR experts**): a person record, how to introduce them (`title`, such as CEO) and the topics they speak to. A brand's first expert is its default, and a quote with no expert named goes to the default. Make another expert the default with `is_default`. Add an expert by a person record you have, or by email and name (`POST /v1/workspaces/{ws}/pr-experts`). Only add people who agreed to be quoted.

## A story

| Field | What it is |
| --- | --- |
| `title` | The headline. |
| `kind` | `data` (a finding from numbers), `reactive` (a comment on a news article, `article_id`) or `comment` (an expert's view). |
| `summary` | Notes for the team. |
| `release` | The press release, as Markdown. |
| `embargo_until` | Not to be published before this time. |
| `status` | `idea`, `draft` (it has a release), `approved` (checked and approved by a person) or `archived`. |

### Facts

Every number in the release needs a **fact** that states it, with where it comes from: a link (`source_url`), a note on how to find it again such as a saved query's name (`source_note`), or both. The source kind says what it is: `own_data` (the brand's own numbers), `public_data`, `news`, `brand_fact` or `other`. Figures from the brand's own data are aggregates only, never about a person.

### Quotes

A **quote** is drafted for one of the brand's experts. It can appear in the release only once a person records that the expert approved those exact words (`status: approved`, with an `approval_note` saying how, such as "by email on 29 Oct"). Changing the words makes it a draft again. Tokens and agents can draft quotes but can't record an approval.

## The check

`POST /v1/workspaces/{ws}/stories/{story}/check` checks the release and keeps the result on the story (`last_check`):

| Code | Kind | What it means |
| --- | --- | --- |
| `no_release` | error | The story has no release yet. |
| `unsourced_number` | error | A number in the release appears in none of the story's facts. Numbers are compared as figures with their unit, so 37% and "37 percent" match. Years and single digits don't count. |
| `fact_without_source` | error | A fact has no link and no note. |
| `unapproved_quote` | error | A quoted passage in the release isn't part of a quote its expert approved. |
| `hype_phrase` | warning | Words journalists skip a release for, such as "revolutionary" or "game-changer". |
| `needs_second_look` | warning | A finance or health claim ("guaranteed returns", "cures"), for a person to check against the rules for those. |

Errors stop approval; warnings are for the person approving to read.

## Approval

`POST /v1/workspaces/{ws}/stories/{story}/approve` runs the check and, when it passes, marks the story approved. Only a person can approve: tokens and agents get 403, and a release that fails the check gets 409 `check_failed`. Changing an approved story's headline, kind, release or embargo, a quote's words, or removing a fact or the expert moves it back to draft, to be approved again.

## Permissions

Reading experts and stories, and running the check, needs `seo.read`; changing them and approving needs `seo.manage`. A member limited to some brands sees and changes only their brands' experts and stories.

## Events

`pr_expert.created`, `.updated`, `.deleted`; `story.created`, `.updated`, `.deleted`, `.checked` (with `ok`, `errors` and `warnings` counts) and `.approved`; `story_fact.added` and `.removed`; `story_quote.added`, `.approved`, `.withdrawn`, `.reopened` and `.updated`. They carry ids and counts, never the release, facts or quotes.

## Agents

Agents use `list_stories`, `get_story`, `create_story`, `update_story`, `add_story_fact`, `add_story_quote`, `check_story`, `list_pr_experts` and `create_pr_expert`. They can draft a story and fix what the check finds; a person approves quotes and releases in Outflare.
