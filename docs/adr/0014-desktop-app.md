# 14. The desktop app is the web app in a Tauri window

Status: Accepted (8 November 2026). Keeps [ADR 11](0011-open-core-line.md): the shell is a tool, like every tool.

## Decision

Outflare's desktop app ("Outflare", for the Mac first) is a [Tauri 2](https://tauri.app) shell whose window loads the deployed web app. It has no pages, features or build of the web app of its own.

- **The window is the web app.** Every deploy of `web/` is in the desktop app the same minute. The shell owns one page, shown when the server can't be reached.
- **Sign-in happens in the system browser.** The app opens the sign-in page in the browser; the person signs in as they usually do and approves "Sign in to Outflare on this computer"; the browser hands the app a one-time code through the `outflare://` scheme; the app completes it with a verifier it kept (as PKCE does), and the server sets the ordinary session cookie. Three public operations under `/v1/auth/app/` and the package `engine/internal/appsignin` ([desktop-app.md](../desktop-app.md)).
- **Native touches go through a small bridge** the web app calls when it runs in the shell: open a link in the browser, show a system notification, set the dock badge, install a waiting update. Each is a command the shell exposes to the web app's origin only.
- **It is a separate product from Outflare for desktop**, the AI usage agent in `desktop/`: a different name, folder (`app/`), crate, bundle identifier (`ai.outflare.app`), store, update feed and workflow, and no shared code.

## Why

The desktop app must always have the same features as the web version, with the least to maintain. A native rewrite, a cross-platform UI toolkit or a copy of the web build inside the installer all drift: every web change would need a desktop release, and app and server versions would disagree. Loading the deployed web app removes the problem instead of managing it.

Tauri over Electron because the system webview gives installers of a few megabytes and a fraction of the memory, Rust and the Tauri toolchain are already in this repository's CI, and the shell has so little code that Electron's larger ecosystem buys nothing.

Sign-in in the browser because Google refuses OAuth in embedded webviews and passkeys are unreliable there, and because one path then covers every method, including ones added later. The verifier the app keeps makes a hijacked deep link useless.

## Consequences

- The web app must keep working in WebKit, which it already does for Safari, and gains a `desktop_app` value of the `surface` event property (`desktop` is Outflare for desktop's) plus a few `ui.*` events for what happens only in the shell.
- The engine gains the sign-in hand-off (one expand-only migration: the pending sign-ins table and a nullable `sessions.client`), which works for any server.
- The shell updates itself: it downloads and verifies a newer version in the background, the web app shows an Update button at the bottom of its sidebar, and one click installs it and reopens the app. Every update is signed, and a build without the public half of the signing key never updates itself.
- The Desktop app workflow checks and builds the shell on every change to `app/`.
