# Outflare for Chrome

A side panel for Chrome that shows what Outflare knows about who is in front of you:

- **In Gmail**, the person the open email is from: their company, owner, stage, where they came from, whether they get marketing email, their deals, support conversations and to-dos.
- **On any website**, the company the site belongs to.
- **Someone new** is added in two clicks, alone or with their company, and a note or a to-do goes on anyone.
- **The toolbar icon** shows a green mark on a company you know and a red `!` on a site on your do-not-contact list. A do-not-contact site or address can't be added from the panel.

It has no build step: plain JavaScript modules, loaded as they are.

## Install it

The extension is not in the Chrome Web Store yet. Until it is:

1. Open `chrome://extensions` and switch on **Developer mode** (top right).
2. Press **Load unpacked** and pick this `extension` folder.
3. Press the Outflare icon in the toolbar (pin it from the puzzle menu). The side panel opens.
4. Press **Connect Outflare**, sign in and press **Allow**. On your own server, press "Running Outflare on your own server?" first and enter its address.

Disconnect from the panel's footer, or in Outflare under Connect your AI, where the extension is listed as "Outflare for Chrome".

## What it may do, and what it sends

- **Signing in** uses Outflare's own approval page (`/oauth/authorize`, with PKCE). The extension gets a token that can list brands and read and add people and companies with their notes and to-dos (`workspace.read`, `customers.read`, `customers.manage`, less if your role has less). It can't send email, change settings or read anything else. The token lasts an hour and renews itself; it is kept in the extension's own storage.
- **The server knows the extension by its id.** Chrome hands the sign-in's answer only to the extension whose id is in the address (`https://<id>.chromiumapp.org/`). The `key` in `manifest.json` is the public key that fixes that id for a copy loaded from this folder; the server's list is `extensionIDs` in `engine/internal/api/agent_oauth.go`. Any other extension is an unknown app, which only reads until the server's operator checks it.
- **The sites you visit stay in the browser.** The extension fetches the brand's company websites and do-not-contact websites as salted digests (`GET /v1/brands/{brand}/records/known-domains`), every hour and when the panel opens, and checks the open site against that list itself. Outflare is asked about a website only when the list says it already holds it, or when you type it into the panel's search box. With more than 200,000 companies the list is cut, and the panel then asks Outflare about the open site.
- **In Gmail** the panel reads who the open email is from (name and address, never the email's text) and looks that address up in Outflare while the panel is open.
- It reads the address of the tab in front (the `tabs` permission) for the icon and the panel, and nothing else from any page.

## Files

| File | What it does |
| --- | --- |
| `manifest.json` | Permissions, the side panel, the Gmail script and the `key` that fixes the id. |
| `lib.js` | The server's address, signing in, API calls, and the list of known websites. |
| `background.js` | Opens the panel from the toolbar icon, marks the icon, refreshes the list. |
| `panel.html`, `panel.css`, `panel.js` | The side panel. |
| `gmail.js` | Tells the panel who the open email is from. |
| `icons/` | The Outflare mark, from the web app's icons. |

## Test it

`pnpm --filter @outflare/web test:extension` loads the extension in a real Chromium against a stand-in for the API (`web/tests/extension.mjs`). The server side is tested in `engine/internal/api` (`TestBrowserExtensionSignsIn`, `TestKnownDomains`).
