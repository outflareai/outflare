import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { ConnectionHook, HookEvent, Integration } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, Status, toast } from "../ui";
import { ago, stamp } from "../format";
import { UrlBox } from "../pages/ConnectAI";

// Live updates: a connection's own webhook address, so the tool tells
// Outflare when something happens instead of being asked.

const statusLabel: Record<ConnectionHook["status"], { tone: "green" | "grey" | "amber" | "red" | "blue"; label: string }> = {
  off: { tone: "grey", label: "Off" },
  setting_up: { tone: "blue", label: "Setting up" },
  waiting: { tone: "amber", label: "Waiting for the first event" },
  receiving: { tone: "green", label: "Receiving" },
  failed: { tone: "red", label: "Couldn't set up" },
};

export function LiveUpdates({ ws, integration, canManage }: { ws: string; integration: Integration; canManage: boolean }) {
  const [hook, setHook] = useState<ConnectionHook | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [secret, setSecret] = useState("");
  const path = { ws, id: integration.id };

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/integrations/{id}/hook", { params: { path } });
    if (error) return setError(errorMessage(error));
    setHook(data);
  }, [ws, integration.id]);

  useEffect(() => {
    void load();
  }, [load]);
  // Follow a registration until it settles.
  useEffect(() => {
    if (hook?.status !== "setting_up") return;
    const t = setTimeout(() => void load(), 2000);
    return () => clearTimeout(t);
  }, [hook, load]);

  async function run(fn: () => Promise<{ data?: ConnectionHook; error?: unknown }>, done?: string) {
    setBusy(true);
    const { data, error } = await fn();
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    if (data) setHook(data);
    else await load();
    if (done) toast(done);
  }

  const turnOn = () => run(() => api.POST("/v1/workspaces/{ws}/integrations/{id}/hook", { params: { path } }));
  async function rotate() {
    if (!(await confirmAction("Make a new address?", <p>The current address stops working straight away.{hook?.setup === "auto" ? ` Outflare gives ${integration.connector_name} the new one.` : ` Paste the new one into ${integration.connector_name}.`}</p>, "Make a new address", false))) return;
    await run(() => api.POST("/v1/workspaces/{ws}/integrations/{id}/hook/rotate", { params: { path } }), "New address made.");
  }
  async function turnOff() {
    if (!(await confirmAction("Turn off live updates?", <p>The address stops working{hook?.setup === "auto" ? ` and Outflare removes it from ${integration.connector_name}` : ""}. The event log stays.</p>, "Turn off"))) return;
    await run(async () => {
      const { error } = await api.DELETE("/v1/workspaces/{ws}/integrations/{id}/hook", { params: { path } });
      return { error };
    }, "Live updates turned off.");
  }
  async function saveSecret(e: FormEvent) {
    e.preventDefault();
    await run(() => api.PUT("/v1/workspaces/{ws}/integrations/{id}/hook/signing-secret", { params: { path }, body: { secret: secret.trim() } }), secret.trim() ? "Signing secret saved." : "Signing secret removed.");
    setSecret("");
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!hook) return <Loading />;
  const st = statusLabel[hook.status];
  const name = integration.connector_name;

  return (
    <Card
      title="Live updates"
      description={`${name} tells Outflare when something happens, such as ${hook.supports.events.slice(0, 2).join(" or ")}, so it shows up straight away.`}
      action={<Status tone={st.tone}>{st.label}{hook.status === "receiving" && hook.last_event_at ? ` · last event ${ago(hook.last_event_at)}` : ""}</Status>}
    >
      <div className="space-y-4 p-4">
        {!hook.reachable ? (
          <p className="text-[13px] text-muted-foreground">This server can't be reached from the internet, so {name} can't send it webhooks. Set PUBLIC_URL to the server's public https address to use live updates.</p>
        ) : hook.status === "off" ? (
          <div className="flex flex-wrap items-center gap-3">
            <p className="text-[13px] text-muted-foreground">{hook.supports.auto ? `Outflare adds its address in your ${name} account for you.` : `You get an address to paste into ${name}.`}</p>
            {canManage && <Button disabled={busy} onClick={turnOn}>Turn on live updates</Button>}
          </div>
        ) : (
          <>
            {hook.url && <UrlBox url={hook.url} />}
            {hook.status === "setting_up" && <p className="text-[13px] text-muted-foreground">Adding the address in {name}…</p>}
            {hook.status === "failed" && (
              <div className="space-y-2">
                <ErrorText>{name} refused the address{hook.last_error ? `: ${hook.last_error}` : "."}</ErrorText>
                {canManage && <Button disabled={busy} onClick={turnOn}>Try again</Button>}
              </div>
            )}
            {(hook.setup === "manual" || hook.status === "failed") && <p className="text-[13px] text-muted-foreground">{hook.supports.steps}</p>}
            {canManage && hook.supports.signed && hook.setup === "manual" && (
              <form onSubmit={saveSecret} className="flex flex-wrap items-center gap-2">
                <input id="hook-signing-secret" type="password" autoComplete="off" className={`${field} w-72`} placeholder={hook.signed ? "Signing secret saved: paste to replace" : `Signing secret from ${name}`} value={secret} onChange={(e) => setSecret(e.target.value)} aria-label="Signing secret" />
                <Button type="submit" variant="ghost" disabled={busy || (!secret.trim() && !hook.signed)}>{secret.trim() || !hook.signed ? "Save" : "Remove"}</Button>
              </form>
            )}
            <p className="text-[12px] text-muted-foreground">
              {hook.setup === "auto" ? `Outflare added this address in ${name}. ` : ""}
              {hook.signed ? "Every delivery's signature is checked. " : "Anyone with this address can send events, so keep it private. "}
              Repeats are ignored.
            </p>
            {canManage && (
              <div className="flex flex-wrap gap-2">
                <Button variant="ghost" disabled={busy} onClick={rotate}>New address</Button>
                <Button variant="ghost" disabled={busy} onClick={turnOff}>Turn off</Button>
              </div>
            )}
          </>
        )}
        {hook.status !== "off" && <HookEvents ws={ws} integrationId={integration.id} canManage={canManage} compact />}
      </div>
    </Card>
  );
}

const eventTone: Record<HookEvent["status"], "green" | "grey" | "red" | "blue"> = { handled: "green", ignored: "grey", failed: "red", queued: "blue" };
const eventLabel: Record<HookEvent["status"], string> = { handled: "handled", ignored: "logged", failed: "failed", queued: "queued" };

// What a summary says in a few words: a job id and its counts.
function describe(s: HookEvent["summary"]) {
  const parts: string[] = [];
  for (const [k, v] of Object.entries(s ?? {})) {
    if (typeof v === "number") parts.push(`${k.replace(/_/g, " ")} ${v.toLocaleString()}`);
    else if (typeof v === "string" && k !== "job_id") parts.push(v);
  }
  return parts.join(" · ");
}

// HookEvents lists webhook events, for one connection or the workspace.
export function HookEvents({ ws, integrationId, canManage, compact, names }: { ws: string; integrationId?: string; canManage: boolean; compact?: boolean; names?: Map<string, string> }) {
  const [status, setStatus] = useState<HookEvent["status"] | "">("");
  const [events, setEvents] = useState<HookEvent[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const load = useCallback(
    async (before?: string) => {
      const { data, error } = await api.GET("/v1/workspaces/{ws}/hook-events", { params: { path: { ws }, query: { integration_id: integrationId, status: status || undefined, before, limit: compact ? 10 : 50 } } });
      if (error) return toast(errorMessage(error), "error");
      setEvents((cur) => (before ? [...(cur ?? []), ...data.data] : data.data));
      setNext(compact ? null : (data.next_before ?? null));
    },
    [ws, integrationId, status, compact],
  );
  useEffect(() => {
    setEvents(null);
    void load();
  }, [load]);

  async function replay(e: HookEvent) {
    const { error } = await api.POST("/v1/workspaces/{ws}/hook-events/{event}/replay", { params: { path: { ws, event: e.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Queued to run again.");
    void load();
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        {compact ? <h3 className="text-[13px] font-semibold">Recent events</h3> : (
          <select className={field} aria-label="Event status" value={status} onChange={(e) => setStatus(e.target.value as HookEvent["status"] | "")}>
            <option value="">Every event</option>
            <option value="failed">Failed</option>
            <option value="handled">Handled</option>
            <option value="ignored">Logged only</option>
            <option value="queued">Queued</option>
          </select>
        )}
        <span className="text-[12px] text-muted-foreground">Kept 90 days; replays work for 30.</span>
      </div>
      {events === null && <Loading />}
      {events?.length === 0 && (compact ? <p className="text-[13px] text-muted-foreground">Nothing yet. Events show up here as soon as they arrive.</p> : <Empty title="No events yet">When a connected tool sends Outflare a webhook, such as Geonode finishing a scrape, it shows up here. Turn on live updates on the tool's connection page.</Empty>)}
      {events && events.length > 0 && (
        <ul className="divide-y rounded-md border font-mono text-[12px]">
          {events.map((e) => (
            <li key={e.id} className="grid grid-cols-[88px_76px_minmax(0,1fr)_auto] items-center gap-3 px-3 py-1.5">
              <span className="text-muted-foreground" title={stamp(e.received_at)}>{ago(e.received_at)}</span>
              <span><Badge tone={eventTone[e.status]} title={e.status === "ignored" ? "Received and logged; nothing in Outflare uses this event yet." : e.error_code ?? undefined}>{eventLabel[e.status]}</Badge></span>
              <span className="min-w-0 truncate" title={describe(e.summary)}>
                {!integrationId && names?.get(e.integration_id) && <span className="text-muted-foreground">{names.get(e.integration_id)} · </span>}
                {e.type}
                {describe(e.summary) && <span className="text-muted-foreground"> · {describe(e.summary)}</span>}
                {e.duplicates > 0 && <span className="text-muted-foreground"> · {e.duplicates} repeat{e.duplicates === 1 ? "" : "s"} ignored</span>}
              </span>
              <span>{canManage && e.replayable && e.status !== "queued" && <Button variant="ghost" onClick={() => replay(e)}>Run again</Button>}</span>
            </li>
          ))}
        </ul>
      )}
      {next && <Button variant="ghost" onClick={() => load(next)}>Load older</Button>}
    </div>
  );
}
