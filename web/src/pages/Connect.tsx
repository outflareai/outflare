import { useEffect, useState, type FormEvent } from "react";
import { AICostNotice } from "./AIUsage";
import type { Connector, ConnectorAccount } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { resetAnalytics } from "../lib/analytics";
import { Button, CenteredForm, ErrorText, Input } from "../ui";
import { Login } from "./Login";
import { authOption, provider, signInWith } from "./Connectors";
import { ConnectorLogo } from "@/components/connector-logo";

type Lookup = { workspace_name: string; connector: Connector; name: string; reason: string; requested_by: "agent" | "person"; requested_by_email?: string | null; expires_at: string };
type Done = { connector_name: string; name: string; account?: ConnectorAccount; workspace_name: string };

const pendingKey = "outflare_connect_link";

// pendingConnectToken is a link's token kept while its opener signs in.
export function pendingConnectToken(): string {
  try {
    return sessionStorage.getItem(pendingKey) ?? "";
  } catch {
    return "";
  }
}

function keepToken(token: string) {
  try {
    if (token) sessionStorage.setItem(pendingKey, token);
    else sessionStorage.removeItem(pendingKey);
  } catch {
    // Private windows may refuse; signing in with a password still works.
  }
}

// The page behind a connect request link (/connect#ofc_...). An agent asked
// for a service to be connected; a signed-in member of that workspace who
// may connect services pastes the key or approves the sign-in here, so the
// secret never passes through the agent. Anyone else is turned away.
export function Connect({ token }: { token: string }) {
  const [info, setInfo] = useState<Lookup | null>(null);
  const [signIn, setSignIn] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [values, setValues] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [fatal, setFatal] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<Done | null>(null);
  // Signing in is offered first when it works; the key form opens on request.
  const [useKey, setUseKey] = useState(false);
  const [costOK, setCostOK] = useState(false);

  useEffect(() => {
    // Back from a provider's sign-in page.
    const q = new URLSearchParams(location.search);
    if (q.get("integration")) return setDone({ connector_name: "The service", name: "", workspace_name: "" });
    if (q.get("connect_error")) return setFatal(q.get("connect_error")!);
    if (!token) return setFatal("The link is incomplete. Copy the whole link, including the part after #.");
    api.POST("/v1/connect/lookup", { body: { token } }).then(({ data, error, response }) => {
      if (response.status === 401) {
        keepToken(token);
        return setSignIn(true);
      }
      keepToken("");
      setSignIn(false);
      if (error || !data) setFatal(errorMessage(error));
      else setInfo(data as Lookup);
    });
  }, [token, attempt]);

  async function switchAccount() {
    await api.POST("/v1/auth/logout");
    resetAnalytics();
    setFatal("");
    setAttempt((n) => n + 1);
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const credentials = Object.fromEntries(Object.entries(values).map(([k, v]) => [k, v.trim()]).filter(([, v]) => v));
    const { data, error } = await api.POST("/v1/connect/complete", { body: { token, credentials } });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    history.replaceState(null, "", "/connect");
    setDone(data as Done);
  }

  async function startOAuth() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/connect/oauth", { body: { token } });
    if (error || !data) {
      setBusy(false);
      return setError(errorMessage(error));
    }
    location.assign((data as { url: string }).url);
  }

  if (signIn) {
    return (
      <div>
        <p className="mx-auto mt-8 max-w-sm px-4 text-center text-[13px] text-muted-foreground">Sign in to open this link. It is for members of the workspace that asked for a connection.</p>
        <Login onDone={() => setAttempt((n) => n + 1)} />
      </div>
    );
  }
  if (fatal) {
    return (
      <CenteredForm title="This link can't be used" subtitle={fatal}>
        <p className="text-[13px] text-muted-foreground">Ask whoever sent it for a new link, or sign in with the account you use in that workspace.</p>
        <Button variant="ghost" className="w-full" onClick={switchAccount}>Sign in with another account</Button>
      </CenteredForm>
    );
  }
  if (done) {
    return (
      <CenteredForm title={`${done.connector_name} is connected`} subtitle={done.name ? `Saved as "${done.name}" in ${done.workspace_name}.` : undefined}>
        {done.account?.credits !== undefined && <p className="text-[13px]">{done.account.credits.toLocaleString()} credits left on the account.</p>}
        <p className="text-[13px] text-muted-foreground">You can close this page. Your agent can carry on now.</p>
      </CenteredForm>
    );
  }
  if (!info) return null;
  const def = info.connector;
  const who = info.requested_by === "agent" ? (info.requested_by_email ? `An AI agent working for ${info.requested_by_email}` : "An AI agent") : (info.requested_by_email ?? "A teammate");
  const oauth = authOption(def, "oauth2"), key = authOption(def, "api_key");
  const canSignIn = !!oauth?.available;
  const showKey = !!key && (useKey || !canSignIn);
  // An AI model runs on the person's own provider bill: they acknowledge
  // that before connecting one.
  const isAI = def.category === "model";
  const costBlocked = isAI && !costOK;

  return (
    <CenteredForm title={`Connect ${def.name}`} subtitle={`${who} working in ${info.workspace_name} asked for this connection.`}>
      <ConnectorLogo id={def.id} size="lg" />
      {info.reason && <blockquote className="rounded-md border-l-2 border-brand bg-muted/50 px-3 py-2 text-[13px]">{info.reason}</blockquote>}
      <p className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-[12px]">
        Only connect if you expected this request. It gives {info.workspace_name} access to your {def.name} account. Outflare never asks for keys in an email or a chat.
      </p>
      <p className="text-[13px] text-muted-foreground">{def.description || def.summary}</p>
      {isAI && (
        <div className="space-y-2">
          <AICostNotice />
          <label className="flex items-start gap-2.5 text-[13px]">
            <input type="checkbox" className="mt-0.5 size-4" checked={costOK} onChange={(e) => setCostOK(e.target.checked)} />
            <span>I understand {def.name} bills my account for every AI call Outflare makes with this key, that managing that spend is my responsibility, and that I should set a hard limit with {def.name}.</span>
          </label>
        </div>
      )}
      {oauth && (canSignIn || !key) && (
        <div className="space-y-2">
          <Button className="w-full" disabled={busy || !canSignIn || costBlocked} onClick={startOAuth}>{busy && !showKey ? "Opening…" : signInWith(def)}</Button>
          {canSignIn ? key && <p className="text-[12px] text-muted-foreground">Recommended: you approve access on {provider(def)}'s own page, with nothing to copy.</p> : <p className="text-[12px] text-muted-foreground">This server isn't set up for {def.name} sign-in yet.</p>}
        </div>
      )}
      {canSignIn && key && !useKey && (
        <button type="button" className="block w-full rounded-md py-1.5 text-center text-[13px] text-muted-foreground underline decoration-border underline-offset-2 hover:text-foreground" onClick={() => setUseKey(true)}>
          {def.id.startsWith("google_") ? "Use a service account key instead" : "Use an API key instead"}
        </button>
      )}
      {canSignIn && showKey && <div className="flex items-center gap-3 text-[12px] text-muted-foreground"><span className="h-px flex-1 bg-border" />or paste a key<span className="h-px flex-1 bg-border" /></div>}
      {!showKey && <ErrorText>{error}</ErrorText>}
      {showKey && (
        <form onSubmit={submit} className="space-y-4">
          {def.fields.map((f) => (
            <Input
              key={f.name}
              label={f.label + (f.optional ? " (optional)" : "")}
              type={f.secret ? "password" : "text"}
              autoComplete="off"
              spellCheck={false}
              placeholder={f.placeholder}
              hint={f.help}
              required={!f.optional}
              value={values[f.name] ?? ""}
              onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
              autoFocus={f === def.fields[0]}
            />
          ))}
          {def.key_url && (
            <p className="text-[12px] text-muted-foreground">
              Find it in your {def.name} account: <a className="underline hover:text-foreground" href={def.key_url} target="_blank" rel="noreferrer">open the API key page</a>.
            </p>
          )}
          <ErrorText>{error}</ErrorText>
          <Button disabled={busy || costBlocked} className="w-full">{busy ? "Testing the key…" : "Test and connect"}</Button>
        </form>
      )}
      <p className="text-[12px] text-muted-foreground">
        A key goes straight to Outflare, is tested, stored encrypted and never shown to the agent. This link works once and expires {new Date(info.expires_at).toLocaleString()}.
      </p>
    </CenteredForm>
  );
}
