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

## With AI

Two tools use the workspace's own AI model (the **PR stories** task under Settings, AI model). Outflare never uses an AI key of its own: without a connected model they answer 409 `connector_needed`, and they count against the workspace's AI budget.

- **Ideas.** `POST /v1/workspaces/{ws}/stories/ideas` with `brand_id`, `count` (1 to 10, 5 by default) and an optional `brief` suggests story ideas from the brand, its facts, its experts' topics, its recent coverage and the last two weeks of news its topic searches found. Each one is a story with status `idea` and source `ai_ideas`; a reactive idea keeps the article it answers. Ideas never state numbers or quotes: a data idea says which of the brand's numbers to look up.
- **Drafts.** `POST /v1/workspaces/{ws}/stories/{story}/draft`, with optional `guidance`, writes the release from the story's headline, notes, facts and approved quotes only, never a draft or withdrawn quote, and improves the current release when there is one. The draft replaces the release, moves the story to `draft` and runs the check, so `last_check` shows straight away whether it used a number with no fact or changed a quote. A person still approves it. While it writes, the story's `ai_status` is `drafting`; it is `failed`, with `ai_error`, when it couldn't.

## Pitching

An approved story is pitched to journalists through one of the workspace's own Outreach sequences, from its existing mailboxes and their rules. `POST /v1/workspaces/{ws}/stories/{story}/pitches` with `sequence_id` and up to 50 journalists' person records (`record_ids`, such as from [`/journalists`](news-watch.md)) starts a pitch for each.

- **The sequence** must stop on any reply, have 1 or 2 steps (one follow-up at most) and use `{{custom.opener}}`. `{{custom.story_title}}` holds the headline.
- **The opener.** The workspace's own AI (the PR stories task) writes 2 to 4 sentences from the story's facts and approved quotes and the journalist's latest article. It is checked like the release: a number in none of the facts or a quote that isn't approved leaves the draft with `reason` `check_failed`, for a person to fix. Without a connected AI model the draft waits with no opener, for a person to write.
- **A person approves each pitch** (`POST /v1/workspaces/{ws}/story-pitches/{pitch}/approve`, with the opener edited if they like); tokens and agents get 403. Approving makes the journalist a lead and enrolls them, **held until the story's embargo** (`embargo_until`).
- **Gentle with journalists.** A journalist is pitched once per story, and at most once a week by the workspace, whichever brand. Anyone who asked not to be contacted is refused, and the sequence stops on their reply.
- **Replies.** The AI reads each reply as `covering`, `wants_data`, `wants_interview`, `not_my_beat`, `declined` or `other`, kept on the pitch as `reply_kind` and tagged in the inbox (`pr: covering it`, `pr: wants the data`, `pr: wants an interview`, `pr: not my beat`, `pr: declined`). Data and interview requests also go to a person (`needs_review`).

## Results

`GET /v1/workspaces/{ws}/stories/results` (`brand`, `days`, 28 by default, `story_id`) gives each story's pitches, replies by kind and coverage in the window, and their total. A story's coverage is coverage a person put on it (`story_id` on [coverage](news-watch.md)), or coverage of its brand written by a journalist it pitched, after the pitch. Home shows the totals for the last 28 days next to the channels.

## Playbooks

Four [playbooks](playbooks.md) run digital PR end to end with these tools, each as a mission a person approves:

- **Data story** (`pr-data-story`): a surprising finding in the brand's own data, as aggregates, written up and pitched to journalists on the topic.
- **Regional data story** (`pr-regional-data-story`): one finding split by region, each version pitched to that region's journalists.
- **React to today's news** (`pr-react-to-news`): the expert's approved comment on a story the news watch found, pitched within hours to the journalists covering it.
- **Expert comment** (`pr-expert-comment`): the expert offered to journalists on their beat, with an approved quote and an interview.

**Turn mentions into links** (`link-unlinked-mentions`) asks coverage that names the brand without linking to add the link.

## Permissions

Reading experts and stories, results, and running the check, needs `seo.read`; changing them and approving needs `seo.manage`. Pitches enroll leads in sequences, so they need `sequences.read` to list and `sequences.manage` to start, change, approve or reject. A member limited to some brands sees and changes only their brands' experts and stories.

## Events

`pr_expert.created`, `.updated`, `.deleted`; `story.created`, `.updated`, `.deleted`, `.checked` (with `ok`, `errors` and `warnings` counts) and `.approved`; `story_fact.added` and `.removed`; `story_quote.added`, `.approved`, `.withdrawn`, `.reopened` and `.updated`; `story.ideas_requested`, `.ideas_suggested` (with `ideas`), `.ideas_failed`, `.draft_requested`, `.drafted` (with the check's `ok` and `errors`) and `.draft_failed`, the last two failures with the error code; `story_pitch.started`, `.written` (with whether the opener passed the check), `.write_failed`, `.edited`, `.approved` (with `embargoed`), `.rejected`, `.reply_read` (with `kind`) and `.reply_failed`. They carry ids and counts, never the release, facts or quotes.

## Agents

Agents use `list_stories`, `get_story`, `create_story`, `update_story`, `add_story_fact`, `add_story_quote`, `check_story`, `suggest_stories`, `draft_story`, `pitch_story`, `list_story_pitches`, `update_story_pitch`, `reject_story_pitch`, `get_story_results`, `list_pr_experts` and `create_pr_expert`. They can draft a story and fix what the check finds; a person approves quotes and releases in Outflare.
