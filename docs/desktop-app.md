# The Outflare desktop app

Outflare for Mac: the same product you use at app.outflare.ai, in its own window, in the dock, signed in once. It is for people who live in Outflare all day and want it to behave like an app rather than a browser tab.

It is a different app from **Outflare for desktop**, the small menu bar agent that reports your AI tools' usage for [AI Accounts](ai-accounts.md). You can install both; neither knows about the other.

## What it is

The window shows the deployed web app at app.outflare.ai. There is nothing to keep in step: every change to the web app is in the desktop app the same minute, with no update to install. The app itself is a thin shell around that window:

- its own window, dock icon and menu, which remember their size and position;
- links that leave Outflare open in your browser, not in the app;
- system notifications and a dock badge, when the web app asks for them;
- deep links (`outflare://open/<path>`) that bring the window forward on a page;
- a small page of its own when app.outflare.ai can't be reached;
- automatic updates of the shell itself.

It only connects to app.outflare.ai. Self-hosted servers have the same sign-in endpoints (below), but the app has no server setting yet.

## Signing in

The app never shows a sign-in form. Signing in happens in your browser, where you are usually signed in already and where Google and passkeys work:

1. In the app, choose **Sign in in your browser**. The app asks the server to start a sign-in (`POST /v1/auth/app/start`) with a PKCE challenge, and opens the page the server names.
2. In the browser, sign in if you aren't, then approve **Sign in to Outflare on this computer** (`POST /v1/auth/app/signins/{state}/approve`). The page shows the computer's name as the app reported it.
3. The browser hands the app a one-time code through the app's URL scheme (`outflare://signed-in?...`). The app completes the sign-in (`GET /v1/auth/app/complete`) with the code and the verifier it kept, and the server gives it the ordinary session cookie.

The code alone is useless: completing needs the verifier, which never left your computer, so a link another program intercepts can't sign anyone in. A code works once, whatever happens, and a sign-in expires after 10 minutes. The app's sessions are marked as the app's (`sessions.client = 'app'`), so they can be told apart from browser sessions.

Each step is a product event ([product-analytics.md](product-analytics.md)): `app_signin.started`, `app_signin.approved`, `app_signin.failed` with its `error_code`, and `user.signed_in` with `via: app`.

## Self-hosting

Nothing to configure: the endpoints are part of every server. The app itself only opens app.outflare.ai for now.
