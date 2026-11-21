# Brand profile

The brand profile is what a brand's agents know about the company: who it is, what it sells, who it serves, why it is different and how it talks. Every agent reads it before it writes or plans anything for the brand. It is kept as separate facts, so an agent can cite the one it used and a person can correct one without rewriting a page of text.

In the app it is the first page under **Company**. The overview lists the twelve sections with how full each is; a section opens in the page, with a Back button.

## Sections

| Section | What belongs in it |
| --- | --- |
| Company | Who the company is: what it does, where it is, how big it is and how it started |
| Offer | What it sells: products, plans, prices, guarantees and what is not on offer |
| Customers and segments | Who it serves, and who is not a fit |
| Personas | The kinds of people it sells to |
| Positioning | Why it is different: the problem it solves, for whom, and why it beats the alternatives |
| Proof | Why to believe it: results, numbers, customers that may be named, reviews and awards |
| Competitors | Where it wins, where it loses, and what to say about each alternative |
| Objections | What holds buyers back, and the honest answer to each |
| Voice | How it sounds: tone, words to use, words to avoid |
| Goals | What the company wants from its marketing and sales |
| Channels | Where it reaches people, and what has and hasn't worked |
| Rules | What must always or never be said or done |

## Facts

A fact (`brand_profile_facts`) is one statement in one section, with:

- **`source`**: where it came from (`person`, `interview`, `website`, `conversation`, `agent`, `import`, or `context` for one that started as a context field).
- **`evidence`**: why it is believed, as notes with an optional page or document.
- **`confidence`**: low, medium or high.
- **`visibility`**: `customers` (agents may say it in what a customer reads) or `internal` (for the team and its agents only). New facts are internal unless said otherwise.
- **`status`**: `suggested`, `confirmed` or `retired`.
- **`review_at`**: an optional date to look at it again; a fact past it is `due`.

Three rules hold everywhere:

1. **Only a person confirms.** A fact a signed-in person adds is confirmed. A fact an API token adds (an agent) is always a suggestion, and a token can neither confirm a suggestion nor change a fact a person confirmed. To propose a change to a confirmed fact, an agent adds a suggestion with `replaces_id`; when a person confirms it, its text is written into that fact and the suggestion is retired.
2. **A suggestion is never used with customers.** `say_to_customers` is true only for a confirmed fact whose visibility is `customers`. That one field is the test for using a fact in an email, a post, an ad or a reply.
3. **Nothing is erased.** A fact is retired, not deleted, and can be brought back. Every change (added, changed, confirmed, retired, brought back) is a row in `brand_profile_changes` with who made it and what it said before and after. The page's **History** shows them.

A profile holds at most 500 facts that aren't retired, each up to 4,000 characters.

## Personas

A persona (`brand_personas`) is a kind of person the brand sells to: a name, who they are, a summary, and lists of what they want (`goals`), what hurts (`pains`), what makes them start looking (`triggers`), what holds them back (`objections`) and where to reach them (`channels`). A persona is a type, never a real person: real people and companies live in customer records. The same rules apply: an agent's persona is a suggestion a person confirms, and a persona is archived, not deleted. A brand keeps at most 50.

## The older context fields

A brand's context ("About your company": about, audience, goals, voice, rules) is still read by everything that read it before. Each of those five fields that has text has one fact that mirrors it (`context_field`): Company, Customers and segments, Goals, Voice and Rules each start with it. Changing the field changes the fact and changing the fact changes the field, in one transaction, so there is one truth. Emptying the field retires the fact; bringing the fact back restores the field. A mirror fact stays in its section and may be as long as its field.

## What agents read

- `GET /v1/workspaces/{ws}/brands/{brand}/context` (MCP `get_brand_context`) carries `profile`, the confirmed facts that mirror no context field (at most 100, each with `say_to_customers`), and `personas`, the confirmed personas (at most 20). Suggestions are left out.
- `GET /v1/brands/{brand}/profile` (MCP `get_brand_profile`) is the whole profile: the sections with their counts, every fact that isn't retired and every persona that isn't archived.

Facts and personas are data an agent reads, never instructions that change what runs.

## API

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/profile` | The whole profile |
| `GET`, `POST /v1/brands/{brand}/profile/facts` | List facts (`section`, `status`, `due`), or add one |
| `GET`, `PATCH /v1/brands/{brand}/profile/facts/{id}` | Read one; change, confirm, retire or bring one back |
| `GET /v1/brands/{brand}/profile/changes` | The history (`fact_id`, `persona_id`) |
| `GET`, `POST /v1/brands/{brand}/personas` | List personas, or add one |
| `GET`, `PATCH /v1/brands/{brand}/personas/{id}` | Read one; change, confirm, archive or bring one back |

Reading needs `workspace.read`; writing needs `context.manage`. A profile belongs to one brand and is never shared with another brand or another workspace.

MCP tools: `get_brand_profile`, `suggest_profile_fact`, `update_profile_fact`, `list_personas`, `suggest_persona`, `update_persona` and `list_profile_changes`.

## Events

Changes record `profile_fact.created`, `profile_fact.updated`, `profile_fact.confirmed`, `profile_fact.retired`, `profile_fact.restored`, `persona.created`, `persona.updated`, `persona.confirmed`, `persona.archived` and `persona.restored` in the audit log, in the same transaction, with the brand, the section, the source and the names of the fields that changed. A change to a mirror fact also records `brand.context_updated`. The events never carry the text of a fact.

## Asking questions of the profile

The profile is readable in Query as three tables: `brand_profile_facts`, `brand_personas` and `brand_profile_changes` (scope `workspace.read`). See [Query](query.md).
