# Connector logos

Each file in `svg/` is a company's own mark, used unmodified to name its
connector in the connector directory (nominative use, the usual practice for
integration directories). The trademarks belong to their owners; using a
logo here does not mean the company endorses Outflare.

The only changes made: the canvas is cropped square around the mark (so
every logo sits at the same optical size), editor metadata is stripped, and
where a mark is a wordmark only the symbol is kept. `{id}.dark.svg` is the
company's own light-on-dark variant for marks that are black, or, where the
company publishes none, the same mark with its black recoloured white.

A few vendors publish no vector mark at all. For those the file embeds the
company's own app icon (its `apple-touch-icon` or favicon PNG, at most
128px) as a data URI inside the SVG. The test allows `<image>` only for an
embedded PNG.

Connectors without a file here get a monogram (`logos.Monogram`), never a
guessed or redrawn logo.

| Connector | Source |
| --- | --- |
| ahrefs, apollo, bing_webmaster, calendly, claude, cloudflare, github, google_analytics, google_calendar, linkedin, outlook_calendar, plausible, posthog, resend, salesforce, vercel, x, zoom | [svgl](https://github.com/pheralb/svgl) (MIT), which collects official brand assets |
| amazon_ses, amplitude, cursor, google_search_console, google_tag_manager, hotjar, mailgun, matomo, route53, segment, sendgrid, vscode | [gilbarbara/logos](https://github.com/gilbarbara/logos) (CC0), via `@iconify-json/logos` 1.2.18 for `cursor-icon` and `visual-studio-code`; the Cursor mark is black, with a white dark variant |
| deepseek, gemini, mistral, openai, openrouter, xai | [Lobe Icons](https://github.com/lobehub/lobe-icons) (MIT), `@lobehub/icons-static-svg` 1.101.0: the colour marks for DeepSeek, Gemini and Mistral, the OpenAI, OpenRouter and xAI marks in black with a white dark variant |
| crunchbase, fathom, godaddy, hubspot, mixpanel, namecheap, reddit, simple_analytics, umami | [Simple Icons](https://simpleicons.org) (CC0), in the brand colour Simple Icons records |
| emailable, hunter, instantly, neverbounce, peopledatalabs | The company's own website logo (symbol only) |
| attio, bouncer, debounce, findymail, leadmagic, millionverifier, reoon, smartlead (bouncer and reoon now live in `integrations/connectors/<id>/logo.svg`) | The company's own website logo (symbol only), taken from its site in October 2026 |
| cal_com, chili_piper, commonroom, emailbison, fullenrich, kickbox, microsoft_clarity, moltsets, predictleads, trigify, usergems | The company's own SVG icon, favicon or pinned-tab mark from its website |
| geonode | Geonode's official logo pack (the square symbol, `geonode-mark.svg`), in its own dark navy with a white dark variant |
| bettercontact, bounceban, briteverify (Validity), cheapinboxes (also cheapverifier), clay, clearout, cognism, contactout, dataforseo, dropcontact, icypeas, infraforge, lusha, mailreef, pipedrive, plusvibe, prospeo, rb2b, rocketreach, savvycal, scrubby, theirstack, wiza, zerobounce, zoominfo | The company's own app icon PNG from its website, embedded (no vector published) |

## Adding a logo

1. Take the SVG from the company's brand or press page (or its website
   header or favicon). Never redraw it. Only when the company publishes no
   vector mark, embed its own app icon PNG as described above.
2. Crop the viewBox square around the mark with a little padding and save
   it as `svg/{connector id}.svg`. If the mark is black, add the company's
   white version as `svg/{connector id}.dark.svg`.
3. Add a row above. `go test ./engine/internal/connectors/logos` checks
   every file is valid SVG with no scripts.
