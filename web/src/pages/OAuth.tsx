import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Connection as Connected, Connector, ImportResult, Integration } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, confirmAction, ErrorText, toast } from "../ui";
import { authOption, startConnectorOAuth } from "./Connectors";

const field = "rounded-md border border-neutral-300 bg-transparent px-3 py-1.5 dark:border-neutral-700";
// Mailbox sign-ins are connectors: Gmail (google_mail) and Outlook
// (outlook_mail). Signing in connects one mailbox; an admin connection (a
// Google service account or a Microsoft Entra app) adds any mailbox of its
// organisation.
const mailConnectors = ["google_mail", "outlook_mail"] as const;
const label: Record<string, string> = { google_mail: "Google Workspace", outlook_mail: "Microsoft 365", google: "Google Workspace", microsoft: "Microsoft 365" };
const admin: Record<string, string> = { google_mail: "service account", outlook_mail: "Entra app" };

// OAuth connects Google Workspace and Microsoft 365 mailboxes without
// passwords: one at a time through the sign-in screen, or in bulk through
// an admin connection.
export function OAuthConnect({ ws, onDone }: { ws: string; onDone: () => void }) {
  const [defs, setDefs] = useState<Connector[]>([]);
  const [connections, setConnections] = useState<Integration[]>([]);
  // Admin connections made before mailbox sign-ins were connectors.
  const [older, setOlder] = useState<Connected[]>([]);
  const [pool, setPool] = useState("");
  const [adding, setAdding] = useState(false);

  const load = useCallback(async () => {
    const [cat, list, old] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/connectors", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws }, query: { category: "mailbox_provider" } } }),
      api.GET("/v1/workspaces/{ws}/connections", { params: { path: { ws }, query: { category: "mailbox_provider" } } }),
    ]);
    setDefs((cat.data?.data ?? []).filter((d) => (mailConnectors as readonly string[]).includes(d.id)));
    setConnections((list.data?.data ?? []).filter((i) => (mailConnectors as readonly string[]).includes(i.connector) && i.auth_method === "api_key"));
    setOlder((old.data?.data ?? []).filter((c) => c.kind === "mailbox_oauth"));
  }, [ws]);

  useEffect(() => {
    load();
  }, [load]);

  async function removeOlder(c: Connected) {
    if (!(await confirmAction(`Remove the connection ${c.name}?`, <p>Its stored credentials are deleted. You can only remove it once no mailbox uses it.</p>, "Remove connection"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/oauth-connections/{id}", { params: { path: { ws, id: c.id } } });
    if (error) toast(errorMessage(error), "error");
    else toast(`Removed ${c.name}.`);
    load();
  }

  async function remove(c: Integration) {
    if (!(await confirmAction(`Remove the connection ${c.name}?`, <p>Its stored credentials are deleted. You can only remove it once no mailbox uses it.</p>, "Remove connection"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/integrations/{id}", { params: { path: { ws, id: c.id } } });
    if (error) toast(errorMessage(error), "error");
    else toast(`Removed ${c.name}.`);
    load();
  }

  const signIns = defs.filter((d) => authOption(d, "oauth2")?.available);
  const admins = defs.filter((d) => authOption(d, "api_key")?.available);

  return (
    <Card title="Sign in with Google or Microsoft" description="No passwords to manage." action={!adding && admins.length > 0 && <Button variant="ghost" onClick={() => setAdding(true)}>Add admin connection</Button>}>
      <div className="space-y-4 text-sm">
        <p className="text-neutral-600 dark:text-neutral-400">
          Connect Google Workspace and Microsoft 365 mailboxes without passwords. Each sign-in is a connection, listed with your other connectors. An admin connection (a Google service account or a Microsoft Entra app) adds every mailbox in an organisation at once.
        </p>
        {signIns.length > 0 && (
          <div className="flex flex-wrap items-end gap-3">
            {signIns.map((d) => (
              <Button key={d.id} variant="ghost" onClick={() => startConnectorOAuth(ws, d, "mailboxes", undefined, pool)}>
                Connect a {d.id === "google_mail" ? "Google" : "Microsoft"} mailbox
              </Button>
            ))}
            <input aria-label="Pool for the connected mailbox" className={field} placeholder="Pool (optional)" value={pool} onChange={(e) => setPool(e.target.value)} />
          </div>
        )}
        {adding && <NewConnection ws={ws} defs={admins} onDone={() => { setAdding(false); load(); }} onCancel={() => setAdding(false)} />}
        {connections.map((c) => (
          <Connection key={c.id} name={c.name} kind={`${label[c.connector]} ${admin[c.connector]}`} add={(body) => api.POST("/v1/workspaces/{ws}/integrations/{id}/mailboxes", { params: { path: { ws, id: c.id } }, body })} onRemove={() => remove(c)} onAdded={() => { load(); onDone(); }} />
        ))}
        {older.map((c) => (
          <Connection key={c.id} name={c.name} kind={label[c.connector] ?? c.connector} add={(body) => api.POST("/v1/workspaces/{ws}/oauth-connections/{id}/mailboxes", { params: { path: { ws, id: c.id } }, body })} onRemove={() => removeOlder(c)} onAdded={() => { load(); onDone(); }} />
        ))}
      </div>
    </Card>
  );
}

function NewConnection({ ws, defs, onDone, onCancel }: { ws: string; defs: Connector[]; onDone: () => void; onCancel: () => void }) {
  const [id, setId] = useState(defs[0]?.id ?? "");
  const def = defs.find((d) => d.id === id);
  const [name, setName] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const missing = !def || def.fields.some((f) => !f.optional && !values[f.name]?.trim());

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!def) return;
    setBusy(true);
    const credentials = Object.fromEntries(def.fields.map((f) => [f.name, (values[f.name] ?? "").trim()]).filter(([, v]) => v));
    const { error } = await api.POST("/v1/workspaces/{ws}/integrations", { params: { path: { ws } }, body: { connector: def.id, credentials, ...(name.trim() ? { name: name.trim() } : {}) } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    onDone();
  }

  return (
    <form onSubmit={submit} className="space-y-3 rounded-md border border-neutral-200 p-3 dark:border-neutral-800">
      <div className="flex flex-wrap gap-3">
        <select aria-label="Provider" className={field} value={id} onChange={(e) => { setId(e.target.value); setValues({}); }}>
          {defs.map((d) => <option key={d.id} value={d.id}>{label[d.id]}: {admin[d.id]}</option>)}
        </select>
        <input aria-label="Name" className={field} placeholder="Name (optional)" value={name} onChange={(e) => setName(e.target.value)} />
      </div>
      {def?.id === "google_mail" ? (
        <label className="block">
          <span className="mb-1 block font-medium">Service account key file (JSON)</span>
          <input type="file" accept=".json,application/json" onChange={async (e) => { const f = e.target.files?.[0]; setValues({ service_account_json: f ? await f.text() : "" }); }} />
          <span className="mt-1 block text-neutral-500">Grant it domain-wide delegation for https://mail.google.com/ in the Google Admin console.</span>
        </label>
      ) : (
        <div className="grid gap-2 sm:grid-cols-3">
          {def?.fields.map((f) => (
            <input key={f.name} aria-label={f.label} type={f.secret ? "password" : "text"} className={field} placeholder={f.label} value={values[f.name] ?? ""} onChange={(e) => setValues({ ...values, [f.name]: e.target.value })} />
          ))}
        </div>
      )}
      <div className="flex items-start gap-3 [&>button]:shrink-0">
        <Button disabled={busy || missing}>{busy ? "Checking…" : "Save connection"}</Button>
        <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
        <ErrorText>{error}</ErrorText>
      </div>
    </form>
  );
}

type AddBody = { mailboxes: { email: string; display_name?: string }[]; pool?: string };

function Connection({ name, kind, add: send, onRemove, onAdded }: { name: string; kind: string; add: (body: AddBody) => Promise<{ data?: ImportResult; error?: unknown }>; onRemove: () => void; onAdded: () => void }) {
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
    const { data, error } = await send({ mailboxes, ...(pool ? { pool } : {}) });
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
          <span className="font-medium">{name}</span> <span className="text-neutral-500">{kind}</span>
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
