import { useCallback, useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, ErrorText, Loading, PageHeader, ask, confirmAction, toast } from "../ui";
import { ago } from "../format";
import { createPasskey, deviceName, passkeyError, passkeysSupported } from "../lib/passkeys";

type Passkey = components["schemas"]["Passkey"];
type User = { id?: string; name?: string; email?: string; email_verified?: boolean; has_password?: boolean };

// Your own sign-in: email, password and passkeys. Not part of any
// workspace, and only ever about the signed-in person.
export function Account({ onChanged }: { onChanged: () => void }) {
  const [user, setUser] = useState<User>();
  const [methods, setMethods] = useState<string[]>([]);
  const [delivery, setDelivery] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const [me, meta] = await Promise.all([api.GET("/v1/me"), api.GET("/v1/meta")]);
    if (!me.data) return setError(errorMessage(me.error));
    setUser(me.data.user as User);
    setMethods(meta.data?.login_methods ?? []);
    setDelivery(meta.data?.account_email ?? "");
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!user) return <Loading />;

  return (
    <>
      <PageHeader title="Your account" description="How you sign in to Outflare. This is yours alone, not a workspace setting." />
      <div className="max-w-3xl space-y-6">
        <EmailCard user={user} onChanged={() => (load(), onChanged())} />
        <PasswordCard user={user} delivery={delivery} />
        <PasskeysCard on={methods.includes("passkey")} />
      </div>
    </>
  );
}

function EmailCard({ user, onChanged }: { user: User; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  async function resend() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/me/email-verification");
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    if (data?.email_verified) return onChanged();
    toast(`Sent a new link to ${user.email}.`);
  }
  return (
    <Card title="Email">
      <div className="flex flex-wrap items-center justify-between gap-3 text-[13px]">
        <div className="flex items-center gap-2">
          <span className="font-medium">{user.email}</span>
          {user.email_verified ? <Badge tone="green">confirmed</Badge> : <Badge tone="amber">not confirmed</Badge>}
        </div>
        {!user.email_verified && (
          <Button variant="ghost" disabled={busy} onClick={resend}>{busy ? "Sending…" : "Send confirmation link"}</Button>
        )}
      </div>
      {!user.email_verified && (
        <p className="mt-2 text-[12px] text-muted-foreground">Open the link we emailed you. A confirmed address is how you get back in if you forget your password.</p>
      )}
    </Card>
  );
}

function PasswordCard({ user, delivery }: { user: User; delivery: string }) {
  const [busy, setBusy] = useState(false);
  async function sendLink() {
    setBusy(true);
    const { error } = await api.POST("/v1/auth/password-reset", { body: { email: user.email ?? "" } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(delivery === "log" ? "Link made. This server has no email service, so it's in the server log." : `Sent a link to ${user.email}. It works for an hour.`);
  }
  return (
    <Card
      title="Password"
      description={user.has_password ? "Changing it signs you out everywhere else." : "You sign in with Google, so there's no password yet. Set one to sign in without Google too."}
      action={<Button variant="ghost" disabled={busy} onClick={sendLink}>{busy ? "Sending…" : user.has_password ? "Email me a link to change it" : "Email me a link to set one"}</Button>}
    >
      <p className="text-[13px] text-muted-foreground">
        For your safety the change happens from a link sent to {user.email}, so nobody at an unlocked computer can change it.
      </p>
    </Card>
  );
}

function PasskeysCard({ on }: { on: boolean }) {
  const [passkeys, setPasskeys] = useState<Passkey[]>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const supported = passkeysSupported();

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/me/passkeys");
    if (error) return setError(errorMessage(error));
    setPasskeys(data.passkeys);
  }, []);
  useEffect(() => {
    if (on) load();
  }, [on, load]);

  async function add() {
    setBusy(true);
    setError("");
    try {
      const { data: options, error } = await api.POST("/v1/me/passkeys/options");
      if (error) throw new Error(errorMessage(error));
      const credential = await createPasskey(options as Record<string, unknown>);
      const saved = await api.POST("/v1/me/passkeys", { params: { query: { name: deviceName() } }, body: credential });
      if (saved.error) throw new Error(errorMessage(saved.error));
      toast(`Passkey saved as "${saved.data.name}". Next time, choose Sign in with a passkey.`);
      load();
    } catch (e) {
      setError(passkeyError(e));
    } finally {
      setBusy(false);
    }
  }

  async function rename(p: Passkey) {
    const r = await ask({
      title: "Rename passkey",
      input: { label: "Name", placeholder: p.name },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok", variant: "primary" }],
    });
    if (r?.choice !== "ok" || !r.value.trim()) return;
    const { error } = await api.PATCH("/v1/me/passkeys/{id}", { params: { path: { id: p.id } }, body: { name: r.value.trim() } });
    if (error) return toast(errorMessage(error), "error");
    load();
  }

  async function remove(p: Passkey) {
    if (!(await confirmAction(`Remove "${p.name}"?`, <p>It will no longer sign you in. You can add it again later.</p>, "Remove passkey"))) return;
    const { error } = await api.DELETE("/v1/me/passkeys/{id}", { params: { path: { id: p.id } } });
    if (error) return toast(errorMessage(error), "error");
    load();
  }

  if (!on) {
    return (
      <Card title="Passkeys" description="Sign in with Face ID, Touch ID, Windows Hello or your phone instead of a password.">
        <p className="text-[13px] text-muted-foreground">
          Not on for this server yet. A passkey is tied to the server's web address, so they turn on once PUBLIC_URL is its final https address.
        </p>
      </Card>
    );
  }

  return (
    <Card
      title="Passkeys"
      description="Sign in with Face ID, Touch ID, Windows Hello or your phone instead of a password."
      action={supported && <Button disabled={busy} onClick={add}>{busy ? "Waiting for your device…" : "Add a passkey"}</Button>}
    >
      {!supported && <p className="mb-3 text-[13px] text-muted-foreground">This browser can't make passkeys. Try a current Chrome, Safari, Edge or Firefox.</p>}
      <ErrorText>{error}</ErrorText>
      {!passkeys ? (
        <Loading />
      ) : passkeys.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">No passkeys yet. Add one on each device you use, or one synced through iCloud Keychain or Google Password Manager.</p>
      ) : (
        <ul className="divide-y">
          {passkeys.map((p) => (
            <li key={p.id} className="flex flex-wrap items-center justify-between gap-3 py-2.5 text-[13px] first:pt-0 last:pb-0">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{p.name}</span>
                  {p.synced ? <Badge tone="blue" title="Backed up by a password manager, so it survives losing the device">synced</Badge> : <Badge title="Only on the device that made it">this device only</Badge>}
                </div>
                <p className="text-[12px] text-muted-foreground">
                  Added {ago(p.created_at)} · {p.last_used_at ? `last used ${ago(p.last_used_at)}` : "not used yet"}
                </p>
              </div>
              <div className="flex gap-1">
                <Button variant="ghost" onClick={() => rename(p)}>Rename</Button>
                <Button variant="danger" onClick={() => remove(p)}>Remove</Button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
