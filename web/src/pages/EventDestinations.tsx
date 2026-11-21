import { Fragment, useCallback, useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, CopyButton, Empty, ErrorText, field, Loading, confirmAction, toast } from "../ui";
import { ago, stamp } from "../format";

// Destinations: where a brand's events go on to as they arrive (its own
// webhook, Slack, Google Analytics 4, HubSpot, a table in its BigQuery
// or Snowflake), each on the workspace's own connection or key. A row opens in place to change it and to see
// what it did with its newest events.

type Destination = Schemas["Destination"];
type Send = Schemas["DestinationSend"];
type Kind = Destination["kind"];
type Integration = Schemas["Integration"];
type Channel = { id: string; name: string };

const kinds: Record<Kind, { label: string; what: string }> = {
  webhook: { label: "Webhook", what: "Post each event as JSON to a URL of yours, signed so you can check it came from Outflare." },
  slack: { label: "Slack", what: "Post a line in a channel when an event happens: what it was and who did it." },
  ga4: { label: "Google Analytics 4", what: "Send events to your GA4 property from the server, so they count even when a browser blocks the tag." },
  hubspot: { label: "HubSpot", what: "Create or update the person as a HubSpot contact when an event happens, and set properties on them." },
  bigquery: { label: "BigQuery", what: "Write each event as a row of a table in your own BigQuery dataset, for your own queries and reports." },
  snowflake: { label: "Snowflake", what: "Write each event as a row of a table in your own Snowflake database, for your own queries and reports." },
};
const warehouse = (k: Kind) => k === "bigquery" || k === "snowflake";
const connectorOf: Partial<Record<Kind, string>> = { slack: "slack", hubspot: "hubspot" };

type Draft = {
  kind: Kind;
  name: string;
  events: string[];
  url: string;
  channel: string;
  measurement: string;
  secret: string;
  integration: string;
  props: [string, string][];
  // bigquery and snowflake: where the table is.
  project: string;
  dataset: string;
  table: string;
  account: string;
  user: string;
  database: string;
  schema: string;
  warehouse: string;
  role: string;
};

const blank = (kind: Kind): Draft => ({ kind, name: kinds[kind].label, events: [], url: "", channel: "", measurement: "", secret: "", integration: "", props: [], project: "", dataset: "", table: "outflare_events", account: "", user: "", database: "", schema: "", warehouse: "", role: "" });
const draftOf = (d: Destination): Draft => ({
  kind: d.kind,
  name: d.name,
  events: d.events,
  url: d.config.url ?? "",
  channel: d.config.channel ?? "",
  measurement: d.config.measurement_id ?? "",
  secret: "",
  integration: d.integration_id ?? "",
  props: Object.entries(d.config.properties ?? {}),
  project: d.config.project ?? "",
  dataset: d.config.dataset ?? "",
  table: d.config.table ?? "",
  account: d.config.account ?? "",
  user: d.config.user ?? "",
  database: d.config.database ?? "",
  schema: d.config.schema ?? "",
  warehouse: d.config.warehouse ?? "",
  role: d.config.role ?? "",
});

export function EventDestinations({ ws, brand, canManage }: { ws: string; brand: string; canManage: boolean }) {
  const [list, setList] = useState<Destination[] | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const [adding, setAdding] = useState<Kind | null>(null);
  const [secret, setSecret] = useState("");
  const [integrations, setIntegrations] = useState<Integration[]>([]);
  const [names, setNames] = useState<string[]>([]);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/destinations", { params: { path: { brand } } });
    if (!data) return setError(errorMessage(error));
    setError("");
    setList(data.data);
  }, [brand]);
  useEffect(() => {
    void load();
    api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws } } }).then(({ data }) => {
      setIntegrations((data?.data ?? []).filter((i) => i.status === "connected" && (!i.brand_id || i.brand_id === brand)));
    });
    api.GET("/v1/brands/{brand}/catalog/events", { params: { path: { brand } } }).then(({ data }) => {
      setNames((data?.data ?? []).filter((e) => !e.same_as).map((e) => e.name));
    });
  }, [load, ws, brand]);

  if (error && !list) return <ErrorText>{error}</ErrorText>;
  if (!list) return <Loading />;

  return (
    <div className="space-y-4">
      <p className="max-w-3xl text-[13px] text-muted-foreground">
        Send your events on to the tools you already use, as they arrive. Each one runs on your own connection or key. Conversions for ad platforms (Meta, Google Ads and the others) are set under Paid ads, where ad consent is checked too.
      </p>
      {secret && (
        <div className="rounded-md border border-amber-500/40 bg-amber-500/5 p-4 text-[13px]">
          <div className="font-medium">Copy the signing secret now. It isn't shown again.</div>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <code className="break-all rounded bg-muted px-2 py-1 font-mono text-[12px]">{secret}</code>
            <CopyButton text={secret} />
            <Button variant="ghost" onClick={() => setSecret("")}>Done</Button>
          </div>
          <p className="mt-2 text-muted-foreground">Each delivery is signed with it (the webhook-signature header), the same way as Outflare's webhooks.</p>
        </div>
      )}
      {list.length === 0 && !adding && (
        <Empty title="No destinations yet">Pick a tool below to start sending this brand's events to it.</Empty>
      )}
      {list.length > 0 && (
        <div className="overflow-x-auto rounded-md border bg-card">
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-left font-mono text-[11px] uppercase text-muted-foreground">
                <th className="px-3 py-2 font-medium">Destination</th>
                <th className="px-3 py-2 font-medium">Events</th>
                <th className="px-3 py-2 text-right font-medium">Sent, 24 hours</th>
                <th className="px-3 py-2 text-right font-medium">Failed</th>
                <th className="px-3 py-2 text-right font-medium">Waiting</th>
                <th className="px-3 py-2 font-medium">Status</th>
              </tr>
            </thead>
            <tbody>
              {list.map((d) => (
                <Fragment key={d.id}>
                  <tr className={`cursor-pointer border-b last:border-0 hover:bg-muted/40 ${open === d.id ? "bg-muted/40" : ""}`} onClick={() => setOpen(open === d.id ? null : d.id)} aria-expanded={open === d.id}>
                    <td className="px-3 py-2 align-top">
                      <div className="font-medium">{d.name}</div>
                      <div className="text-[12px] text-muted-foreground">{kinds[d.kind].label}</div>
                    </td>
                    <td className="max-w-[320px] px-3 py-2 align-top text-muted-foreground">
                      {d.events.length === 0 ? "Every tracked event" : <span className="line-clamp-2 font-mono text-[12px]">{d.events.join(", ")}</span>}
                    </td>
                    <td className="px-3 py-2 text-right align-top tabular-nums">{d.sent_24h.toLocaleString()}</td>
                    <td className={`px-3 py-2 text-right align-top tabular-nums ${d.failed_24h ? "text-red-600" : ""}`}>{d.failed_24h.toLocaleString()}</td>
                    <td className="px-3 py-2 text-right align-top tabular-nums">{d.queued.toLocaleString()}</td>
                    <td className="px-3 py-2 align-top">
                      {!d.enabled ? <Badge tone="grey">Off</Badge> : d.failing_since ? <Badge tone="red" title={d.last_error}>Failing since {ago(d.failing_since)}</Badge> : <Badge tone="green">On</Badge>}
                    </td>
                  </tr>
                  {open === d.id && (
                    <tr className="border-b last:border-0">
                      <td colSpan={6} className="space-y-5 bg-muted/20 px-4 py-4">
                        {d.failing_since && d.last_error && <ErrorText>{d.last_error}</ErrorText>}
                        <Form
                          ws={ws}
                          brand={brand}
                          canManage={canManage}
                          current={d}
                          integrations={integrations}
                          names={names}
                          onDone={() => void load()}
                          onGone={() => {
                            setOpen(null);
                            void load();
                          }}
                        />
                        <Log brand={brand} id={d.id} />
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {canManage && !adding && (
        <div>
          <div className="mb-2 text-[13px] font-medium">Add a destination</div>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {(Object.keys(kinds) as Kind[]).map((k) => (
              <button key={k} className="flex flex-col rounded-md border bg-card p-3 text-left hover:border-foreground/40" onClick={() => setAdding(k)}>
                <div className="text-[13px] font-medium">{kinds[k].label}</div>
                <div className="mt-1 text-[12px] text-muted-foreground">{kinds[k].what}</div>
              </button>
            ))}
          </div>
        </div>
      )}
      {adding && (
        <div className="rounded-md border bg-card p-4">
          <div className="mb-3 text-[14px] font-medium">New {kinds[adding].label} destination</div>
          <Form
            ws={ws}
            brand={brand}
            canManage={canManage}
            kind={adding}
            integrations={integrations}
            names={names}
            onCancel={() => setAdding(null)}
            onDone={(made) => {
              setAdding(null);
              if (made?.signing_secret) setSecret(made.signing_secret);
              void load();
            }}
          />
        </div>
      )}
    </div>
  );
}

function Form({
  ws,
  brand,
  canManage,
  kind,
  current,
  integrations,
  names,
  onDone,
  onCancel,
  onGone,
}: {
  ws: string;
  brand: string;
  canManage: boolean;
  kind?: Kind;
  current?: Destination;
  integrations: Integration[];
  names: string[];
  onDone: (made?: Destination) => void;
  onCancel?: () => void;
  onGone?: () => void;
}) {
  const [d, setD] = useState<Draft>(() => (current ? draftOf(current) : blank(kind!)));
  const [channels, setChannels] = useState<Channel[]>([]);
  const [adding, setAdding] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const set = (patch: Partial<Draft>) => setD((x) => ({ ...x, ...patch }));
  const conns = integrations.filter((i) => i.connector === connectorOf[d.kind]);

  // One connection: use it without asking.
  useEffect(() => {
    if (!d.integration && conns.length === 1) set({ integration: conns[0].id });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conns.length]);
  useEffect(() => {
    if (d.kind !== "slack" || !d.integration) return;
    api.GET("/v1/workspaces/{ws}/alerts/channels", { params: { path: { ws }, query: { integration_id: d.integration } } }).then(({ data, error }) => {
      if (data) setChannels(data.data);
      if (error) setError(errorMessage(error));
    });
  }, [ws, d.kind, d.integration]);

  function addEvent(raw: string) {
    const name = raw.trim().toLowerCase();
    if (name && !d.events.includes(name)) set({ events: [...d.events, name] });
    setAdding("");
  }

  async function save() {
    setBusy(true);
    setError("");
    const config: Schemas["DestinationConfig"] =
      d.kind === "webhook"
        ? { url: d.url.trim() }
        : d.kind === "slack"
          ? { channel: d.channel, channel_name: channels.find((c) => c.id === d.channel)?.name ?? current?.config.channel_name ?? "" }
          : d.kind === "ga4"
            ? { measurement_id: d.measurement.trim().toUpperCase() }
            : d.kind === "bigquery"
              ? { project: d.project.trim(), dataset: d.dataset.trim(), table: d.table.trim() }
              : d.kind === "snowflake"
                ? { account: d.account.trim().replace(/\.snowflakecomputing\.com$/i, ""), user: d.user.trim(), database: d.database.trim(), schema: d.schema.trim(), table: d.table.trim(), warehouse: d.warehouse.trim(), ...(d.role.trim() ? { role: d.role.trim() } : {}) }
                : { properties: Object.fromEntries(d.props.filter(([k]) => k.trim()).map(([k, v]) => [k.trim(), v])) };
    const body: Schemas["DestinationWrite"] = { name: d.name, events: d.events, config };
    if (connectorOf[d.kind]) body.integration_id = d.integration || null;
    if ((d.kind === "ga4" || warehouse(d.kind)) && d.secret.trim()) body.secret = d.secret.trim();
    const res = current
      ? await api.PATCH("/v1/brands/{brand}/destinations/{destination}", { params: { path: { brand, destination: current.id } }, body })
      : await api.POST("/v1/brands/{brand}/destinations", { params: { path: { brand } }, body: { ...body, kind: d.kind } });
    setBusy(false);
    if (!res.data) return setError(errorMessage(res.error));
    toast(current ? "Saved" : "Destination added");
    set({ secret: "" });
    onDone(res.data as Destination);
  }

  async function toggle() {
    if (!current) return;
    setBusy(true);
    const { data, error } = await api.PATCH("/v1/brands/{brand}/destinations/{destination}", { params: { path: { brand, destination: current.id } }, body: { enabled: !current.enabled } });
    setBusy(false);
    if (!data) return setError(errorMessage(error));
    toast(current.enabled ? "Turned off" : "Turned on. It sends the events that arrive from now.");
    onDone();
  }

  // Send a test event, then watch the destination until the result is in.
  const [testing, setTesting] = useState(false);
  const [tested, setTested] = useState<Destination["test"] | null>(current?.test.at ? current.test : null);
  async function test() {
    if (!current) return;
    setTesting(true);
    setError("");
    const path = { brand, destination: current.id };
    const { error, response } = await api.POST("/v1/brands/{brand}/destinations/{destination}/test", { params: { path } });
    if (!response.ok) {
      setTesting(false);
      return setError(errorMessage(error));
    }
    for (let i = 0; i < 40; i++) {
      await new Promise((r) => setTimeout(r, 1500));
      const { data } = await api.GET("/v1/brands/{brand}/destinations/{destination}", { params: { path } });
      if (data && !data.test.running) {
        setTested(data.test);
        setTesting(false);
        return;
      }
    }
    setTesting(false);
    setError("The test is taking longer than usual. Open this destination again in a minute to see how it went.");
  }

  async function remove() {
    if (!current) return;
    if (!(await confirmAction(`Remove ${current.name}?`, "Events still waiting are dropped, and its log goes with it.", "Remove"))) return;
    const { error, response } = await api.DELETE("/v1/brands/{brand}/destinations/{destination}", { params: { path: { brand, destination: current.id } } });
    if (!response.ok) return setError(errorMessage(error));
    toast("Destination removed");
    onGone?.();
  }

  const label = "mb-1 block text-[12px] font-medium";
  const hint = "mt-1 text-[12px] text-muted-foreground";
  return (
    <fieldset disabled={!canManage || busy} className="grid max-w-3xl gap-4">
      <div>
        <label className={label} htmlFor="dest-name">Name</label>
        <input id="dest-name" className={`${field} w-80`} value={d.name} maxLength={120} onChange={(e) => set({ name: e.target.value })} />
      </div>
      {connectorOf[d.kind] &&
        (conns.length === 0 ? (
          <p className="rounded-md border border-dashed p-3 text-[13px] text-muted-foreground">
            Connect your own {kinds[d.kind].label} account first: open Connectors at the bottom of the left menu, add {kinds[d.kind].label}, then come back here.
          </p>
        ) : (
          conns.length > 1 && (
            <div>
              <label className={label} htmlFor="dest-conn">Connection</label>
              <select id="dest-conn" className={`${field} w-80`} value={d.integration} onChange={(e) => set({ integration: e.target.value })}>
                <option value="">Pick a connection</option>
                {conns.map((i) => (
                  <option key={i.id} value={i.id}>{i.name || kinds[d.kind].label}</option>
                ))}
              </select>
            </div>
          )
        ))}
      {d.kind === "webhook" && (
        <div>
          <label className={label} htmlFor="dest-url">URL</label>
          <input id="dest-url" className={`${field} w-full`} placeholder="https://example.com/outflare-events" value={d.url} onChange={(e) => set({ url: e.target.value })} />
          <p className={hint}>Each event is posted here as JSON. Answer with a 2xx within 10 seconds.</p>
        </div>
      )}
      {d.kind === "slack" && conns.length > 0 && (
        <div>
          <label className={label} htmlFor="dest-channel">Channel</label>
          <select id="dest-channel" className={`${field} w-80`} value={d.channel} onChange={(e) => set({ channel: e.target.value })}>
            <option value="">Pick a channel</option>
            {d.channel && !channels.some((c) => c.id === d.channel) && <option value={d.channel}>#{current?.config.channel_name || d.channel}</option>}
            {channels.map((c) => (
              <option key={c.id} value={c.id}>#{c.name}</option>
            ))}
          </select>
          <p className={hint}>Name the events to post below, or the channel gets every one.</p>
        </div>
      )}
      {d.kind === "ga4" && (
        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <label className={label} htmlFor="dest-mid">Measurement ID</label>
            <input id="dest-mid" className={`${field} w-full`} placeholder="G-ABC123DEF4" value={d.measurement} onChange={(e) => set({ measurement: e.target.value })} />
            <p className={hint}>In Google Analytics: Admin, Data streams, your web stream.</p>
          </div>
          <div>
            <label className={label} htmlFor="dest-secret">Measurement Protocol API secret</label>
            <input id="dest-secret" type="password" autoComplete="off" className={`${field} w-full`} placeholder={current?.has_secret ? "Saved. Type a new one to replace it." : ""} value={d.secret} onChange={(e) => set({ secret: e.target.value })} />
            <p className={hint}>On the same stream: Measurement Protocol API secrets, Create. Stored sealed and never shown again.</p>
          </div>
        </div>
      )}
      {d.kind === "bigquery" && (
        <div className="grid gap-4 sm:grid-cols-3">
          <div>
            <label className={label} htmlFor="dest-project">Google Cloud project ID</label>
            <input id="dest-project" className={`${field} w-full`} placeholder="acme-analytics" value={d.project} onChange={(e) => set({ project: e.target.value })} />
          </div>
          <div>
            <label className={label} htmlFor="dest-dataset">Dataset</label>
            <input id="dest-dataset" className={`${field} w-full`} placeholder="outflare" value={d.dataset} onChange={(e) => set({ dataset: e.target.value })} />
            <p className={hint}>It has to exist already.</p>
          </div>
          <div>
            <label className={label} htmlFor="dest-table">Table</label>
            <input id="dest-table" className={`${field} w-full`} value={d.table} onChange={(e) => set({ table: e.target.value })} />
            <p className={hint}>Made for you if it isn't there.</p>
          </div>
          <div className="sm:col-span-3">
            <label className={label} htmlFor="dest-key">Service account key (JSON)</label>
            <textarea id="dest-key" rows={4} autoComplete="off" spellCheck={false} className={`${field} h-auto w-full font-mono text-[12px]`} placeholder={current?.has_secret ? "Saved. Paste a new key file to replace it." : '{ "type": "service_account", … }'} value={d.secret} onChange={(e) => set({ secret: e.target.value })} />
            <p className={hint}>In Google Cloud: IAM and admin, Service accounts, make one of your own, give it BigQuery Data Editor on the dataset, then Keys, Add key, JSON. Paste the whole file. Stored sealed and never shown again.</p>
          </div>
        </div>
      )}
      {d.kind === "snowflake" && (
        <div className="grid gap-4 sm:grid-cols-3">
          <div>
            <label className={label} htmlFor="dest-account">Account identifier</label>
            <input id="dest-account" className={`${field} w-full`} placeholder="acme-prod1" value={d.account} onChange={(e) => set({ account: e.target.value })} />
            <p className={hint}>The part of your Snowflake address before .snowflakecomputing.com.</p>
          </div>
          <div>
            <label className={label} htmlFor="dest-user">User</label>
            <input id="dest-user" className={`${field} w-full`} placeholder="OUTFLARE" value={d.user} onChange={(e) => set({ user: e.target.value })} />
          </div>
          <div>
            <label className={label} htmlFor="dest-role">Role (optional)</label>
            <input id="dest-role" className={`${field} w-full`} placeholder="The user's default role" value={d.role} onChange={(e) => set({ role: e.target.value })} />
          </div>
          <div>
            <label className={label} htmlFor="dest-database">Database</label>
            <input id="dest-database" className={`${field} w-full`} placeholder="ANALYTICS" value={d.database} onChange={(e) => set({ database: e.target.value })} />
          </div>
          <div>
            <label className={label} htmlFor="dest-schema">Schema</label>
            <input id="dest-schema" className={`${field} w-full`} placeholder="PUBLIC" value={d.schema} onChange={(e) => set({ schema: e.target.value })} />
          </div>
          <div>
            <label className={label} htmlFor="dest-sftable">Table</label>
            <input id="dest-sftable" className={`${field} w-full`} value={d.table} onChange={(e) => set({ table: e.target.value })} />
            <p className={hint}>Made for you if it isn't there.</p>
          </div>
          <div>
            <label className={label} htmlFor="dest-wh">Warehouse</label>
            <input id="dest-wh" className={`${field} w-full`} placeholder="COMPUTE_WH" value={d.warehouse} onChange={(e) => set({ warehouse: e.target.value })} />
            <p className={hint}>It runs the writes, about once a minute while events arrive.</p>
          </div>
          <div className="sm:col-span-3">
            <label className={label} htmlFor="dest-sfkey">Private key</label>
            <textarea id="dest-sfkey" rows={4} autoComplete="off" spellCheck={false} className={`${field} h-auto w-full font-mono text-[12px]`} placeholder={current?.has_secret ? "Saved. Paste a new key to replace it." : "-----BEGIN PRIVATE KEY-----"} value={d.secret} onChange={(e) => set({ secret: e.target.value })} />
            <p className={hint}>Set the user up for key-pair sign-in in Snowflake (a key with no passphrase), give its role the right to use the warehouse and to create and write the table, then paste the private key here. Stored sealed and never shown again.</p>
          </div>
        </div>
      )}
      {d.kind === "hubspot" && conns.length > 0 && (
        <div>
          <div className={label}>Set these contact properties (optional)</div>
          <div className="space-y-2">
            {d.props.map(([k, v], i) => (
              <div key={i} className="flex flex-wrap items-center gap-2">
                <input className={`${field} w-56 font-mono`} placeholder="lifecyclestage" aria-label="HubSpot property" value={k} onChange={(e) => set({ props: d.props.map((p, j) => (j === i ? [e.target.value, p[1]] : p)) })} />
                <input className={`${field} w-56`} placeholder="customer" aria-label="Value" value={v} onChange={(e) => set({ props: d.props.map((p, j) => (j === i ? [p[0], e.target.value] : p)) })} />
                <Button variant="ghost" onClick={() => set({ props: d.props.filter((_, j) => j !== i) })}>Remove</Button>
              </div>
            ))}
            {d.props.length < 10 && <Button variant="ghost" onClick={() => set({ props: [...d.props, ["", ""]] })}>Add a property</Button>}
          </div>
          <p className={hint}>The person is created or updated as a contact by their email address. Use HubSpot's internal property names.</p>
        </div>
      )}
      <div>
        <div className={label}>Events to send</div>
        <div className="flex flex-wrap items-center gap-2">
          {d.events.map((e) => (
            <span key={e} className="inline-flex items-center gap-1 rounded-full border bg-background px-2 py-0.5 font-mono text-[12px]">
              {e}
              <button type="button" aria-label={`Remove ${e}`} className="text-muted-foreground hover:text-foreground" onClick={() => set({ events: d.events.filter((x) => x !== e) })}>×</button>
            </span>
          ))}
          <input
            className={`${field} w-56`}
            list="dest-event-names"
            placeholder="Add an event name"
            aria-label="Add an event name"
            value={adding}
            onChange={(e) => setAdding(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                addEvent(adding);
              }
            }}
            onBlur={() => addEvent(adding)}
          />
          <datalist id="dest-event-names">
            {names.filter((n) => !d.events.includes(n)).map((n) => (
              <option key={n} value={n} />
            ))}
          </datalist>
        </div>
        <p className={hint}>{d.events.length === 0 ? "None named: every tracked event is sent." : "Only these are sent."} Events that arrived before now are never sent.</p>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {tested?.at && !testing && (
        <div className="flex flex-wrap items-center gap-2 text-[13px]" role="status">
          <Badge tone={tested.ok ? "green" : "red"}>{tested.ok ? "Test passed" : "Test failed"}</Badge>
          <span>{tested.reason}</span>
          <span className="text-muted-foreground" title={stamp(tested.at)}>{ago(tested.at)}</span>
        </div>
      )}
      {canManage && (
        <div className="flex flex-wrap items-center gap-2">
          <Button onClick={save} disabled={!!connectorOf[d.kind] && conns.length === 0}>{current ? "Save" : "Add destination"}</Button>
          {onCancel && <Button variant="ghost" onClick={onCancel}>Cancel</Button>}
          {current && <Button variant="ghost" onClick={test} disabled={testing}>{testing ? "Sending a test event…" : "Send a test event"}</Button>}
          {current && <Button variant="ghost" onClick={toggle}>{current.enabled ? "Turn off" : "Turn on"}</Button>}
          {current && <Button variant="danger" onClick={remove}>Remove</Button>}
        </div>
      )}
    </fieldset>
  );
}

const sendTones: Record<Send["status"], "green" | "grey" | "red" | "blue"> = { sent: "green", skipped: "grey", failed: "red", queued: "blue" };
const sendNames: Record<Send["status"], string> = { sent: "Sent", skipped: "Skipped", failed: "Failed", queued: "Waiting" };

// Log lists what a destination did with its newest events.
function Log({ brand, id }: { brand: string; id: string }) {
  const [status, setStatus] = useState<"" | Send["status"]>("");
  const [rows, setRows] = useState<Send[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    api.GET("/v1/brands/{brand}/destinations/{destination}/sends", { params: { path: { brand, destination: id }, query: status ? { status } : {} } }).then(({ data, error }) => {
      if (!live) return;
      if (data) setRows(data.data);
      setError(data ? "" : errorMessage(error));
    });
    return () => {
      live = false;
    };
  }, [brand, id, status]);
  return (
    <div>
      <div className="mb-2 flex flex-wrap items-center gap-3">
        <div className="text-[13px] font-medium">Latest events</div>
        <select className={field} aria-label="Show" value={status} onChange={(e) => setStatus(e.target.value as typeof status)}>
          <option value="">All</option>
          <option value="sent">Sent</option>
          <option value="failed">Failed</option>
          <option value="skipped">Skipped</option>
          <option value="queued">Waiting</option>
        </select>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {rows && rows.length === 0 && <p className="text-[13px] text-muted-foreground">{status ? "None." : "Nothing yet. Events show here within a minute or two of arriving."}</p>}
      {rows && rows.length > 0 && (
        <div className="overflow-x-auto rounded-md border bg-card">
          <table className="w-full text-[13px]">
            <tbody>
              {rows.map((s) => (
                <tr key={s.id} className="border-b last:border-0">
                  <td className="px-3 py-1.5 font-mono text-[12px]">{s.event_name}</td>
                  <td className="px-3 py-1.5"><Badge tone={sendTones[s.status]}>{sendNames[s.status]}</Badge></td>
                  <td className="px-3 py-1.5 text-muted-foreground">{s.reason}</td>
                  <td className="whitespace-nowrap px-3 py-1.5 text-right text-muted-foreground" title={stamp(s.created_at)}>{ago(s.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
