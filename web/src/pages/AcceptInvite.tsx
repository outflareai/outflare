import { useEffect, useState, type FormEvent } from "react";
import { api, errorMessage } from "../api";
import { Button, CenteredForm, ErrorText, Input } from "../ui";
import { GoogleButton, OrDivider, clearGoogleReturn, googleReturn, useGoogleSignIn } from "./GoogleSignIn";

export function AcceptInvite({ token, onDone }: { token: string; onDone: () => void }) {
  const google = useGoogleSignIn();
  const [g] = useState(() => {
    const r = googleReturn();
    if (r.error || r.identity) clearGoogleReturn();
    return r;
  });
  const [name, setName] = useState(g.name);
  const [password, setPassword] = useState("");
  const [error, setError] = useState(g.error);
  const [busy, setBusy] = useState(false);
  const [me, setMe] = useState<string | null>(null);
  useEffect(() => {
    api.GET("/v1/me").then(({ data }) => setMe(data?.user.email ?? null));
  }, []);

  async function join(body: { token: string; name?: string; password?: string; identity?: string }) {
    setBusy(true);
    const { error } = await api.POST("/v1/invites/accept", { body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    history.replaceState(null, "", "/");
    onDone();
  }

  // Back from Google with an account that has no Outflare login yet.
  if (g.identity) {
    return (
      <CenteredForm title="Join the workspace" subtitle={`Signed in with Google as ${g.email}. You'll sign in with Google from now on.`}>
        <form onSubmit={(e: FormEvent) => (e.preventDefault(), join({ token, name, identity: g.identity }))} className="space-y-4">
          <Input label="Your name" value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
          <ErrorText>{error}</ErrorText>
          <Button disabled={busy} className="w-full">Join</Button>
        </form>
      </CenteredForm>
    );
  }

  // Already signed in (for example just now with Google): one click.
  if (me) {
    return (
      <CenteredForm title="Join the workspace" subtitle={`You're signed in as ${me}.`}>
        <div className="space-y-4">
          <ErrorText>{error}</ErrorText>
          <Button disabled={busy} className="w-full" onClick={() => join({ token })}>Join as {me}</Button>
        </div>
      </CenteredForm>
    );
  }

  return (
    <CenteredForm title="Join the workspace" subtitle="New here? Choose your name and a password. Already have an account? Just enter your password.">
      {google && (
        <>
          <GoogleButton returnTo={`/invite#${token}`} />
          <OrDivider />
        </>
      )}
      <form onSubmit={(e: FormEvent) => (e.preventDefault(), join({ token, name, password }))} className="space-y-4">
        <Input label="Your name" value={name} onChange={(e) => setName(e.target.value)} autoFocus={!google} />
        <Input label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required autoComplete="new-password" />
        <ErrorText>{error}</ErrorText>
        <Button disabled={busy} className="w-full">Join</Button>
      </form>
    </CenteredForm>
  );
}
