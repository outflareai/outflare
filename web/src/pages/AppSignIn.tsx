import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Button, CenteredForm, ErrorText } from "../ui";
import { track } from "@/lib/analytics";
import { desktop } from "@/lib/desktop";

// Signing in to the Outflare desktop app (docs/desktop-app.md).
//
// In a browser, /signin/app?state=... is the approve page the app opened:
// the signed-in person sees which computer asks and approves, and the page
// hands the one-time code to the app through outflare://signed-in.
//
// In the app's own window, the same path is where a failed completion
// lands (?error=...), and the sign-in screen itself (AppSignInShell): the
// app never shows a form, it sends the person to their browser.

type Info = { name: string; platform: string; app_version: string; expires_at: string; approved: boolean };

const platformName: Record<string, string> = { macos: "Mac", windows: "Windows PC", linux: "Linux computer" };

export function AppSignIn({ me }: { me: { user: { email?: string; name?: string } } }) {
  const state = new URLSearchParams(location.search).get("state") ?? "";
  const [info, setInfo] = useState<Info | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<string | null>(null);

  useEffect(() => {
    if (!state) return setError("This link is missing its sign-in. Start again in the Outflare app.");
    api.GET("/v1/auth/app/signins/{state}", { params: { path: { state } } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      setInfo(data as Info);
    });
  }, [state]);

  async function approve() {
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/auth/app/signins/{state}/approve", { params: { path: { state } } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    const url = (data as { url: string }).url;
    setDone(url);
    // The deep link hands the code to the app; the button below is for a
    // browser that asked first and was told no.
    location.assign(url);
  }

  if (done) {
    return (
      <CenteredForm title="You're signed in on your computer" subtitle="Go back to the Outflare app. If it didn't open by itself, open it from here.">
        <a href={done} className="block text-center text-[13px] underline">Open the Outflare app</a>
      </CenteredForm>
    );
  }

  const who = me.user.email || me.user.name || "your account";
  const computer = info ? `${info.name} (${platformName[info.platform] ?? info.platform})` : "a computer";
  return (
    <CenteredForm title="Sign in to Outflare on this computer?" subtitle={`The Outflare app on ${computer} wants to sign in as ${who}.`}>
      <div className="space-y-4">
        <p className="text-[13px] text-muted-foreground">Only approve this if you just chose “Sign in in your browser” in the app on your own computer. The app gets the same access you have here.</p>
        <ErrorText>{error}</ErrorText>
        {info && !error && (
          <Button onClick={approve} disabled={busy || info.approved} className="w-full">
            {info.approved ? "Already approved" : busy ? "Signing in…" : "Sign in on this computer"}
          </Button>
        )}
        {error && <a href="/" className="block text-center text-[13px] underline">Go to Outflare</a>}
      </div>
    </CenteredForm>
  );
}

const errorText: Record<string, string> = {
  app_signin_expired: "That sign-in expired. Start again.",
  app_signin_mismatch: "That sign-in didn't come back to this app. Start again, and approve it only from your own browser.",
  not_found: "That sign-in isn't waiting any more. Start again.",
  rate_limited: "Too many sign-ins from this network. Wait 15 minutes, then start again.",
};

// The sign-in screen inside the desktop app's window.
export function AppSignInShell() {
  const code = new URLSearchParams(location.search).get("error") ?? "";
  const [error, setError] = useState(code ? errorText[code] ?? "Signing in didn't finish. Start again." : "");
  const [waiting, setWaiting] = useState(false);

  async function start() {
    setError("");
    try {
      await desktop.startSignIn();
      track("ui.app_signin_started");
      setWaiting(true);
    } catch {
      setError("Couldn't open your browser. Try again.");
    }
  }

  return (
    <CenteredForm title="Sign in to Outflare" subtitle="Signing in happens in your browser, where you're already signed in and where Google and passkeys work. You'll be back here in a moment.">
      <div className="space-y-4">
        {waiting && <p className="text-[13px] text-muted-foreground">Waiting for your browser. Approve the sign-in there, then come back to this window.</p>}
        <ErrorText>{error}</ErrorText>
        <Button onClick={start} className="w-full">{waiting ? "Open the browser again" : "Sign in in your browser"}</Button>
      </div>
    </CenteredForm>
  );
}
