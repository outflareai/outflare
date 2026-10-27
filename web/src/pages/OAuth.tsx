import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { ImportResult, OAuthConnection } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, confirmAction, ErrorText, toast } from "../ui";

const field = "rounded-md border border-neutral-300 bg-transparent px-3 py-1.5 dark:border-neutral-700";
const label = { google: "Google Workspace", microsoft: "Microsoft 365" } as const;

// OAuth connects Google Workspace and Microsoft 365 mailboxes without
// passwords: one at a time through the consent screen, or in bulk through
// an admin connection.
export function OAuthConnect({ ws, onDone }: { ws: string; onDone: () => void }) {
  const [providers, setProviders] = useState<string[]>([]);
  const [connections, setConnections] = useState<OAuthConnection[]>([]);
  const [pool, setPool] = useState("");
  const [adding, setAdding] = useState(false);

  const load = useCallback(async () => {
    const [meta, c] = await Promise.all([api.GET("/v1/meta"), api.GET("/v1/workspaces/{ws}/oauth-connections", { params: { path: { ws } } })]);
    setProviders(meta.data?.oauth_providers ?? []);
    setConnections(c.data?.data ?? []);
  }, [ws]);

  useEffect(() => {
    load();
  }, [load]);

  async function remove(c: OAuthConnection) {
    if (!(await confirmAction(`Remove the connection ${c.name}?`, <p>Its stored credentials are deleted. You can only remove it once no mailbox uses it.</p>, "Remove connection"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/oauth-connections/{id}", { params: { path: { ws, id: c.id } } });
    if (error) toast(errorMessage(error), "error");
    else toast(`Removed ${c.name}.`);
    load();
  }

  const authorize = (p: string) => `/v1/workspaces/${ws}/oauth/${p}/authorize${pool ? `?pool=${encodeURIComponent(pool)}` : ""}`;

  return (
    <Card title="Sign in with Google or Microsoft" description="No passwords to manage." action={!adding && <Button variant="ghost" onClick={() => setAdding(true)}>Add admin connection</Button>}>
      <div className="space-y-4 text-sm">
        <p className="text-neutral-600 dark:text-neutral-400">
          Connect Google Workspace and Microsoft 365 mailboxes without passwords. An admin connection (a Google service account or a Microsoft Entra app) adds every mailbox in an organisation at once.
        </p>
        {providers.length > 0 && (
          <div className="flex flex-wrap items-end gap-3">
            {providers.map((p) => (
              <a key={p} href={authorize(p)} className="rounded-md border border-neutral-300 px-3 py-1.5 font-medium hover:bg-neutral-100 dark:border-neutral-700 dark:hover:bg-neutral-800">
                Connect a {p === "google" ? "Google" : "Microsoft"} mailbox
              </a>
            ))}
            <input aria-label="Pool for the connected mailbox" className={field} placeholder="Pool (optional)" value={pool} onChange={(e) => setPool(e.target.value)} />
          </div>
        )}
        {adding && <NewConnection ws={ws} onDone={() => { setAdding(false); load(); }} onCancel={() => setAdding(false)} />}
        {connections.map((c) => <Connection key={c.id} ws={ws} c={c} onRemove={() => remove(c)} onAdded={() => { load(); onDone(); }} />)}
      </div>
    </Card>
  );
}

function NewConnection({ ws, onDone, onCancel }: { ws: string; onDone: () => void; onCancel: () => void }) {
  const [provider, setProvider] = useState<"google" | "microsoft">("google");
  const [name, setName] = useState("");
  const [keyFile, setKeyFile] = useState<File | null>(null);
  const [tenant, setTenant] = useState("");
  const [clientId, setClientId] = useState("");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const body =
      provider === "google"
        ? { provider, name, service_account: keyFile ? await keyFile.text() : "" }
        : { provider, name, tenant_id: tenant, client_id: clientId, client_secret: secret };
    const { error } = await api.POST("/v1/workspaces/{ws}/oauth-connections", { params: { path: { ws } }, body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    onDone();
  }

  return (
    <form onSubmit={submit} className="space-y-3 rounded-md border border-neutral-200 p-3 dark:border-neutral-800">
      <div className="flex flex-wrap gap-3">
        <select aria-label="Provider" className={field} value={provider} onChange={(e) => setProvider(e.target.value as "google" | "microsoft")}>
          <option value="google">{label.google}: service account</option>
          <option value="microsoft">{label.microsoft}: Entra app</option>
        </select>
        <input aria-label="Name" className={field} placeholder="Name (optional)" value={name} onChange={(e) => setName(e.target.value)} />
      </div>
      {provider === "google" ? (
        <label className="block">
          <span className="mb-1 block font-medium">Service account key file (JSON)</span>
          <input type="file" accept=".json,application/json" onChange={(e) => setKeyFile(e.target.files?.[0] ?? null)} />
          <span className="mt-1 block text-neutral-500">Grant it domain-wide delegation for https://mail.google.com/ in the Google Admin console.</span>
        </label>
      ) : (
        <div className="grid gap-2 sm:grid-cols-3">
          <input aria-label="Tenant id" className={field} placeholder="Directory (tenant) id" value={tenant} onChange={(e) => setTenant(e.target.value)} />
          <input aria-label="Client id" className={field} placeholder="Application (client) id" value={clientId} onChange={(e) => setClientId(e.target.value)} />
          <input aria-label="Client secret" type="password" className={field} placeholder="Client secret" value={secret} onChange={(e) => setSecret(e.target.value)} />
        </div>
      )}
      <div className="flex items-start gap-3 [&>button]:shrink-0">
        <Button disabled={busy || (provider === "google" ? !keyFile : !tenant || !clientId || !secret)}>{busy ? "Checking…" : "Save connection"}</Button>
        <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
        <ErrorText>{error}</ErrorText>
      </div>
    </form>
  );
}

function Connection({ ws, c, onRemove, onAdded }: { ws: string; c: OAuthConnection; onRemove: () => void; onAdded: () => void }) {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const [pool, setPool] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [error, setError] = useState("");

  async function add(e: FormEvent) {
    e.preventDefault();
    // One mailbox per line: "email" or "email, Display Name".
    const mailboxes = text
      .split("\n")
      .map((l) => l.trim())
      .filter(Boolean)
      .map((l) => {
        const [email, ...name] = l.split(",");
        return { email: email.trim(), display_name: name.join(",").trim() };
      });
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/oauth-connections/{id}/mailboxes", {
      params: { path: { ws, id: c.id } },
      body: { mailboxes, ...(pool ? { pool } : {}) },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setError("");
    setResult(data ?? null);
    setText("");
    onAdded();
  }

  return (
    <div className="rounded-md border border-neutral-200 p-3 dark:border-neutral-800">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <span className="font-medium">{c.name}</span>{" "}
          <span className="text-neutral-500">
            {label[c.provider]} · {c.mailbox_count} mailbox{c.mailbox_count === 1 ? "" : "es"}
          </span>
        </div>
        <div className="flex gap-2">
          <Button variant="ghost" onClick={() => setOpen(!open)}>Add mailboxes</Button>
          <Button variant="danger" onClick={onRemove}>Remove</Button>
        </div>
      </div>
      {open && (
        <form onSubmit={add} className="mt-3 space-y-2">
          <textarea aria-label="Mailboxes" className={`${field} min-h-28 w-full font-mono`} placeholder={"ana@acme-com.example, Ana Smith\nbo@acme-com.example"} value={text} onChange={(e) => setText(e.target.value)} />
          <div className="flex flex-wrap items-center gap-3">
            <input aria-label="Pool" className={field} placeholder="Pool (optional)" value={pool} onChange={(e) => setPool(e.target.value)} />
            <Button disabled={!text.trim() || busy}>{busy ? "Adding…" : "Add"}</Button>
            <ErrorText>{error}</ErrorText>
          </div>
          {result && (
            <p className="text-neutral-600 dark:text-neutral-400">
              {result.created} added, {result.updated} updated{result.errors.length > 0 && `, ${result.errors.length} skipped (${result.errors.map((e) => e.email || `line ${e.line}`).join(", ")})`}. Connection checks are running.
            </p>
          )}
        </form>
      )}
    </div>
  );
}
