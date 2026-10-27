import { useEffect, useState, type FormEvent } from "react";
import { api, errorMessage } from "../api";
import { Button, CenteredForm, ErrorText, Input } from "../ui";
import { GoogleButton, OrDivider, clearGoogleReturn, googleReturn, useGoogleSignIn } from "./GoogleSignIn";
import { ForgotPassword } from "./AccountEmail";
import { getPasskey, passkeyError, passkeysSupported } from "../lib/passkeys";
import { Icon } from "../icons";

// Signs in with whichever passkey the browser offers for this site.
function PasskeyButton({ onDone, onError }: { onDone: () => void; onError: (m: string) => void }) {
  const [busy, setBusy] = useState(false);
  async function go() {
    setBusy(true);
    onError("");
    try {
      const { data: options, error } = await api.POST("/v1/auth/passkey/options");
      if (error) throw new Error(errorMessage(error));
      const assertion = await getPasskey(options as Record<string, unknown>);
      const res = await api.POST("/v1/auth/passkey", { body: assertion });
      if (res.error) throw new Error(errorMessage(res.error));
      onDone();
    } catch (e) {
      onError(passkeyError(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <button
      type="button"
      onClick={go}
      disabled={busy}
      className="flex h-9 w-full items-center justify-center gap-2 rounded-md border bg-background text-[13px] font-medium shadow-xs transition-colors hover:bg-accent disabled:opacity-60"
    >
      <Icon name="passkey" className="size-4" />
      {busy ? "Waiting for your passkey…" : "Sign in with a passkey"}
    </button>
  );
}

export function Login({ onDone }: { onDone: () => void }) {
  const google = useGoogleSignIn();
  const [passkey, setPasskey] = useState(false);
  useEffect(() => {
    if (!passkeysSupported()) return;
    api.GET("/v1/meta").then(({ data }) => setPasskey(!!data?.login_methods?.includes("passkey")));
  }, []);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState(() => {
    const g = googleReturn();
    if (g.error || g.identity) clearGoogleReturn();
    if (g.identity) return `No Outflare account uses ${g.email}. Ask a workspace admin to invite that address, or sign in with another account.`;
    return g.error;
  });
  const [busy, setBusy] = useState(false);
  // An expired reset link sends people to /?forgot=1.
  const [forgot, setForgot] = useState(() => new URLSearchParams(location.search).has("forgot"));

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { error } = await api.POST("/v1/auth/login", { body: { email, password } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    onDone();
  }

  if (forgot) {
    return (
      <ForgotPassword
        onBack={() => {
          history.replaceState(null, "", "/");
          setForgot(false);
        }}
      />
    );
  }

  return (
    <CenteredForm title="Sign in to Outflare" subtitle={location.pathname === "/oauth/authorize" ? "Sign in to connect your AI app." : undefined}>
      {(google || passkey) && (
        <>
          {passkey && <PasskeyButton onDone={onDone} onError={setError} />}
          {google && <GoogleButton returnTo={location.pathname === "/oauth/authorize" || location.pathname === "/connect" ? location.pathname + location.search : "/"} />}
          <OrDivider />
        </>
      )}
      <form onSubmit={submit} className="space-y-4">
        <Input label="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus={!google && !passkey} autoComplete="email" />
        <div className="space-y-1.5">
          <Input label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required autoComplete="current-password" />
          <button type="button" onClick={() => setForgot(true)} className="text-[12px] text-muted-foreground underline-offset-4 hover:text-foreground hover:underline">
            Forgot your password?
          </button>
        </div>
        <ErrorText>{error}</ErrorText>
        <Button disabled={busy} className="w-full">Sign in</Button>
      </form>
    </CenteredForm>
  );
}
