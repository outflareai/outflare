# Help centre

A help centre is a public site where customers read and search your knowledge themselves. It shows every published article and quick answer you've turned on for the **Help centre** in [Knowledge](knowledge.md), grouped in the same folders, and nothing else: drafts, **Team only** items and items only for **AI talking to customers** never appear.

Each brand can have its own help centre, or the workspace has one for everything. A new help centre is off until you turn it on.

## In the app

**Knowledge → Help centre** sets it up:

- **Get started** makes one from your brand's (or company's) name, logo and colour, with a layout that works before you change anything.
- **Configure and style** shows a live preview beside every setting, on the home page, a collection or an article:
  - **Templates**: Classic (a coloured header and hero), Minimal (light and plain) or Bold (a large hero). **Import your brand** takes the brand's logo and colours again.
  - **General**: the site title, language, whether search engines may list it, the image shown when a page is shared, and the browser tab icon.
  - **Shared elements**: accent and background colours, font (the system font, or one of ten Google Fonts) and how round the corners are; whether articles show their author (the item's owner).
  - **Header**: logo, title, header colour, up to four links and a "Go to website" link.
  - **Hero**: title, subtitle, background image and the search box's placeholder.
  - **Layout and sections**: collections as a grid (one to three columns) or a list, popular articles, quick answers as questions and answers, and a contact block (an email or web address).
  - **Collection page**: folder descriptions, article excerpts and counts.
  - **Article page**: a table of contents from the headings, related articles from the same folder, **Copy for AI** (and a Markdown link), "Did this answer your question?" and the date it was updated.
  - **Footer**: a simple or columns layout, text, links, social profiles and colour.
- **Domain**: by default it is at `/h/<address>/` on your Outflare server. To use your own, such as `help.northpeak.example`, add it as a domain (a CNAME to the same target as tracking domains) and pick it here; once it is verified and has a certificate, the help centre answers at its root, and that domain serves nothing else.
- **Numbers**: views, searches, searches that found nothing, the most read articles and how readers answered "Did this answer your question?".
- **Collections** lists your top-level folders as readers see them; pick each one's icon there.

## What readers get

- Pages are rendered on the server, so search engines and AI assistants read every article. There is a `sitemap.xml` and a `robots.txt` (which turn off with indexing), and an `llms.txt` that lists every article with a link to its Markdown.
- Search finds articles by their words (never by an AI model, so readers' searches spend nothing). A search of two or more words that finds nothing is added to **Unanswered questions** in Knowledge, marked as asked by a customer, so the gaps readers hit are the next things to write.
- Article text is your Markdown; raw HTML in it is left out and unsafe links are dropped, and pages run under a strict content security policy.
- Views, searches and feedback are counted per day, as numbers only: nothing about a reader is kept. Crawlers and link previews aren't counted, and each network can record only a few answers an hour.

## The API and MCP

| What | API | MCP |
| --- | --- | --- |
| List | `GET /v1/workspaces/{ws}/help-centres` | `list_help_centres` |
| Make one | `POST .../help-centres` | `create_help_centre` |
| Read one | `GET .../help-centres/{id}` | `list_help_centres` |
| Change one (settings merge) | `PATCH .../help-centres/{id}` | `update_help_centre` |
| Delete one | `DELETE .../help-centres/{id}` | |
| Preview a page with unsaved settings | `POST .../help-centres/{id}/preview` | |
| Numbers | `GET .../help-centres/{id}/stats` | `get_help_centre_stats` |

A folder's icon is `icon` on `PATCH .../knowledge/collections/{id}`.

Changes are audit actions and product events: `help_centre.created`, `help_centre.updated`, `help_centre.published`, `help_centre.unpublished` and `help_centre.deleted`.
