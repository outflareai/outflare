import { useEffect, useState } from "react";
import { api } from "../api";

/** Whether this server offers "Continue with Google" (GOOGLE_OAUTH_CLIENT_ID set). */
export function useGoogleSignIn(): boolean {
  const [on, setOn] = useState(false);
  useEffect(() => {
    api.GET("/v1/meta").then(({ data }) => setOn(!!data?.login_methods?.includes("google")));
  }, []);
  return on;
}

/** What Google's callback put in the URL: an error, or a Google account with no Outflare account yet. */
export function googleReturn() {
  const q = new URLSearchParams(location.search);
  return {
    error: q.get("login_error") ?? "",
    identity: q.get("identity") ?? "",
    email: q.get("identity_email") ?? "",
    name: q.get("identity_name") ?? "",
  };
}

/** Drops Google's parameters from the address bar, keeping any #fragment. */
export function clearGoogleReturn() {
  history.replaceState(null, "", location.pathname + location.hash);
}

export function GoogleButton({ returnTo = "/", label = "Continue with Google" }: { returnTo?: string; label?: string }) {
  return (
    <a
      href={`/v1/auth/google/start?return=${encodeURIComponent(returnTo)}`}
      className="flex h-9 w-full items-center justify-center gap-2 rounded-md border bg-background text-[13px] font-medium shadow-xs transition-colors hover:bg-accent"
    >
      <GoogleMark />
      {label}
    </a>
  );
}

export function OrDivider() {
  return (
    <div className="flex items-center gap-3 text-[12px] text-muted-foreground" aria-hidden="true">
      <span className="h-px flex-1 bg-border" />
      or
      <span className="h-px flex-1 bg-border" />
    </div>
  );
}

function GoogleMark() {
  return (
    <svg viewBox="0 0 18 18" className="size-4" aria-hidden="true">
      <path fill="#4285F4" d="M17.64 9.2c0-.64-.06-1.25-.16-1.84H9v3.48h4.84a4.14 4.14 0 0 1-1.8 2.72v2.26h2.92c1.7-1.57 2.68-3.88 2.68-6.62Z" />
      <path fill="#34A853" d="M9 18c2.43 0 4.47-.8 5.96-2.18l-2.92-2.26c-.8.54-1.84.86-3.04.86-2.34 0-4.32-1.58-5.03-3.7H.96v2.33A9 9 0 0 0 9 18Z" />
      <path fill="#FBBC05" d="M3.97 10.72A5.4 5.4 0 0 1 3.68 9c0-.6.1-1.18.29-1.72V4.95H.96A9 9 0 0 0 0 9c0 1.45.35 2.83.96 4.05l3.01-2.33Z" />
      <path fill="#EA4335" d="M9 3.58c1.32 0 2.5.45 3.44 1.35l2.58-2.58A9 9 0 0 0 .96 4.95l3.01 2.33C4.68 5.16 6.66 3.58 9 3.58Z" />
    </svg>
  );
}
