import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import type { WebhookDelivery, WebhookEndpoint, WebhookEventType } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, Status, toast } from "../ui";
import { CopyButton, Snippet } from "./Tokens";
import { ago, plural, stamp } from "../format";
import { DataTable } from "@/components/data-table";
import { useBrand } from "@/lib/brand";

// Which reply labels an endpoint hears about; the API takes groups and
// answers with their labels.
const replyChoices = [
  { value: "", label: "Every label" },
  { value: "hot", label: "Hot only: interested, meeting booked, question" },
  { value: "hot,later", label: "Hot and later: adds referral, not now, objection" },
  { value: "closed", label: "Closed only: not interested, wrong person, unsubscribed, complaint" },
];
const labelsText = (c: string[]) => (c.length === 0 ? "" : c.length <= 3 ? `replies: ${c.join(", ").replaceAll("_", " ")}` : `${c.length} reply labels`);

// What an endpoint's events list means, in words.
function eventsLabel(events: string[]): string {
  if (events.includes("*")) return "Every event";
  if (events.length <= 2) return events.join(", ");
  return `${events.slice(0, 2).join(", ")} and ${events.length - 2} more`;
}

function endpointStatus(e: WebhookEndpoint) {
  if (!e.enabled) return <Status tone="grey">Off{e.disabled_reason ? `: ${e.disabled_reason}` : ""}</Status>;
  if (e.failing_since) return <span title={`Failing since ${stamp(e.failing_since)}. It turns off after five days of failures.`}><Status tone="amber">Failing since {ago(e.failing_since)}</Status></span>;
  return <Status tone="green">Active</Status>;
}

// The catalog's descriptions go on to name data fields for developers; the
// first sentence is enough here.
const firstSentence = (d: string) => d.split(/(?<=\.)\s/)[0];

const deliveryTone = { delivered: "green", pending: "blue", failed: "red", cancelled: "grey" } as const;

/** Event picker: everything, or chosen categories and types. */
function EventPicker({ catalog, value, onChange }: { catalog: WebhookEventType[]; value: string[]; onChange: (v: string[]) => void }) {
  const all = value.includes("*");
  const categories = useMemo(() => {
    const m = new Map<string, WebhookEventType[]>();
    for (const t of catalog) if (t.type !== "webhook.test") m.set(t.category, [...(m.get(t.category) ?? []), t]);
    return [...m.entries()];
  }, [catalog]);
  const toggle = (t: string) => onChange(value.includes(t) ? value.filter((x) => x !== t) : [...value.filter((x) => x !== "*"), t]);
  // A category is picked when every type in it is.
  const toggleCategory = (types: WebhookEventType[]) => {
    const names = types.map((t) => t.type);
    const on = names.every((n) => value.includes(n));
    onChange(on ? value.filter((x) => !names.includes(x)) : [...new Set([...value.filter((x) => x !== "*"), ...names])]);
  };
  return (
    <div>
      <span className="mb-2 block font-medium">Events to send</span>
      <div className="mb-3 grid gap-2 sm:grid-cols-2">
        {[
          { on: all, label: "Every event", help: `All ${catalog.length - 1} types, and any added later`, pick: () => onChange(["*"]) },
          { on: !all, label: "Only some", help: "Pick categories or single events", pick: () => onChange(all ? ["lead.replied", "lead.bounced", "mailbox.auto_paused"] : value) },
        ].map((o) => (
          <button key={o.label} type="button" onClick={o.pick} aria-pressed={o.on} className={`rounded-lg border px-3 py-2.5 text-left transition-colors ${o.on ? "border-brand/60 bg-brand/[0.07] ring-1 ring-brand/40" : "border-border hover:border-ring/60"}`}>
            <span className="font-medium">{o.label}</span>
            <span className="mt-0.5 block text-[12px] text-muted-foreground">{o.help}</span>
          </button>
        ))}
      </div>
      {!all && (
        <div className="grid gap-4 rounded-lg border p-3 sm:grid-cols-2 lg:grid-cols-3">
          {categories.map(([category, types]) => (
            <details key={category} open={types.some((t) => value.includes(t.type)) && !types.every((t) => value.includes(t.type))}>
              <summary className="flex cursor-pointer items-center gap-2 font-medium capitalize [&::-webkit-details-marker]:hidden">
                <input type="checkbox" checked={types.every((t) => value.includes(t.type))} ref={(el) => { if (el) el.indeterminate = types.some((t) => value.includes(t.type)) && !types.every((t) => value.includes(t.type)); }} onChange={() => toggleCategory(types)} onClick={(e) => e.stopPropagation()} aria-label={`All ${category} events`} />
                {category}
                <span className="font-mono text-[11px] font-normal normal-case text-muted-foreground">{types.filter((t) => value.includes(t.type)).length}/{types.length}</span><span aria-hidden="true" className="text-muted-foreground">▾</span>
              </summary>
              {types.map((t) => (
                <label key={t.type} className="flex items-start gap-2 py-0.5 pl-5">
                  <input type="checkbox" className="mt-0.5" checked={value.includes(t.type)} onChange={() => toggle(t.type)} />
                  <span><span className="block font-mono text-[12px]">{t.type}</span><span className="block text-[12px] text-muted-foreground">{firstSentence(t.description)}</span></span>
                </label>
              ))}
            </details>
          ))}
        </div>
      )}
    </div>
  );
}

export function Webhooks({ ws, canManage, open, onOpen }: { ws: string; canManage: boolean; open: string; onOpen: (id: string) => void }) {
  const [endpoints, setEndpoints] = useState<WebhookEndpoint[] | null>(null);
  const [catalog, setCatalog] = useState<WebhookEventType[]>([]);
  const [adding, setAdding] = useState(false);
  const [url, setUrl] = useState("");
  const [description, setDescription] = useState("");
  const [events, setEvents] = useState<string[]>(["*"]);
  const [replyLabels, setReplyLabels] = useState("");
  const [brand, setBrand] = useState("");
  const { active: brands, byId } = useBrand(ws);
  const [error, setError] = useState("");
  const [secret, setSecret] = useState<{ url: string; secret: string; rotated?: boolean } | null>(null);

  const load = useCallback(async () => {
    const [e, c] = await Promise.all([api.GET("/v1/workspaces/{ws}/webhooks", { params: { path: { ws } } }), api.GET("/v1/webhook-events")]);
    setEndpoints(e.data?.data ?? []);
    setCatalog(c.data?.data ?? []);
  }, [ws]);

  useEffect(() => {
    load();
  }, [load]);

  async function create(e: FormEvent) {
    e.preventDefault();
    const { data, error } = await api.POST("/v1/workspaces/{ws}/webhooks", { params: { path: { ws } }, body: { url: url.trim(), description: description.trim(), events, categories: replyLabels ? replyLabels.split(",") : [], ...(brand ? { brand } : {}) } });
    if (error) return setError(errorMessage(error));
    setError("");
    setSecret({ url: data!.url, secret: data!.secret });
    setUrl("");
    setDescription("");
    setEvents(["*"]);
    setBrand("");
    setAdding(false);
    load();
  }

  async function update(e: WebhookEndpoint, body: { enabled?: boolean; events?: string[]; url?: string; description?: string }, done: string) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/webhooks/{id}", { params: { path: { ws, id: e.id } }, body });
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    load();
  }

  async function test(e: WebhookEndpoint) {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/webhooks/{id}/test", { params: { path: { ws, id: e.id } } });
    if (error) return toast(errorMessage(error), "error");
    if (data?.status === "delivered") toast(`Test event delivered: ${e.url} answered ${data.last_status_code}.`);
    else toast(`Test event failed: ${data?.last_status_code ? `${e.url} answered ${data.last_status_code}` : data?.last_error ?? "no answer"}.`, "error");
    load();
  }

  async function showSecret(e: WebhookEndpoint) {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/webhooks/{id}/secret", { params: { path: { ws, id: e.id } } });
    if (error) return toast(errorMessage(error), "error");
    setSecret({ url: e.url, secret: data!.secret });
  }

  async function rotate(e: WebhookEndpoint) {
    const r = await ask({
      title: "Make a new signing secret?",
      body: <p>For the next 24 hours each delivery is signed with both secrets, so you can switch your receiver to the new one without missing any. If the old secret leaked, stop using it straight away instead.</p>,
      choices: [{ label: "Stop the old one now", value: "now", variant: "danger-solid" }, { label: "Keep both for 24 hours", value: "later", variant: "primary" }],
    });
    if (!r) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/webhooks/{id}/rotate-secret", { params: { path: { ws, id: e.id } }, body: { expire_now: r.choice === "now" } });
    if (error) return toast(errorMessage(error), "error");
    setSecret({ url: e.url, secret: data!.secret, rotated: true });
  }

  async function changeUrl(e: WebhookEndpoint) {
    const r = await ask({ title: "Change the URL", input: { label: "URL", placeholder: e.url }, choices: [{ label: "Save", value: "ok", variant: "primary" }] });
    if (!r?.value.trim()) return;
    update(e, { url: r.value.trim() }, "URL changed. New events go there.");
  }

  async function remove(e: WebhookEndpoint) {
    if (!(await confirmAction(`Remove ${e.url}?`, <p>Outflare stops sending it events straight away, and deliveries still waiting are dropped. This can't be undone.</p>, "Remove endpoint"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/webhooks/{id}", { params: { path: { ws, id: e.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Endpoint removed.");
    if (open === e.id) onOpen("");
    load();
  }

  const current = endpoints?.find((e) => e.id === open);
  if (open && endpoints && current) return <Deliveries ws={ws} endpoint={current} canManage={canManage} back={() => onOpen("")} onTest={() => test(current)} />;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Webhooks"
        description={<>Outflare sends events to your URLs as they happen: replies, bounces, paused mailboxes, finished imports and more. Each one is signed so you can check it came from here, and failed deliveries are retried for about three and a half hours. <a className="underline" href="https://outflare.ai/docs/api/webhooks/" target="_blank" rel="noreferrer">How to receive webhooks</a>.</>}
        action={canManage && !adding && <Button onClick={() => setAdding(true)}>Add endpoint</Button>}
      />
      {secret && (
        <section className="space-y-3 rounded-lg border border-green-500/30 bg-green-500/[0.06] p-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="font-medium"><span aria-hidden="true" className="mr-2 inline-block size-1.5 rounded-full bg-green-500 align-middle" />{secret.rotated ? "New signing secret" : "Signing secret"} for {secret.url}</p>
            <Button variant="ghost" onClick={() => setSecret(null)}>Done</Button>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <code className="min-w-0 flex-1 break-all px-2 py-1.5 text-[13px]">{secret.secret}</code>
            <CopyButton text={secret.secret} />
          </div>
          <Snippet title="verify with @outflare/sdk" code={`import { verifyWebhook } from "@outflare/sdk";\n\n// Pass the raw body, before any JSON parsing.\nconst event = await verifyWebhook(rawBody, request.headers, process.env.OUTFLARE_WEBHOOK_SECRET);\nif (event.type === "lead.replied") { /* ... */ }`} />
        </section>
      )}
      {adding && (
        <Card title="Add an endpoint">
          <form onSubmit={create} className="space-y-4">
            <Input label="URL" type="url" placeholder="https://example.com/hooks/outflare" hint="Must be public https. Outflare POSTs JSON here." value={url} onChange={(e) => setUrl(e.target.value)} required />
            <Input label="Description" placeholder="e.g. CRM sync, Slack alerts" hint="Optional. Helps you tell endpoints apart." value={description} onChange={(e) => setDescription(e.target.value)} />
            <EventPicker catalog={catalog} value={events} onChange={setEvents} />
            {brands.length > 0 && (
              <label className="block text-sm">
                <span className="mb-1.5 block text-[13px] font-medium">Which brands</span>
                <select className={`${field} w-full sm:w-80`} value={brand} onChange={(e) => setBrand(e.target.value)}>
                  <option value="">Every brand</option>
                  {brands.map((b) => <option key={b.id} value={b.handle}>Only {b.name}</option>)}
                </select>
                <span className="mt-1 block text-xs text-muted-foreground">{brand ? "Only events about this brand's records, such as its sequences, mailboxes and replies." : "Events about every brand's records, and the workspace's own."}</span>
              </label>
            )}
            <label className="block space-y-1.5 text-sm">
              <span className="font-medium">Replies</span>
              <select className={`${field} w-full`} value={replyLabels} onChange={(e) => setReplyLabels(e.target.value)}>
                {replyChoices.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
              </select>
              <span className="block text-[12px] text-muted-foreground">Events about a labelled reply (such as inbox.classified) go only for these labels. Other events are not affected.</span>
            </label>
            <ErrorText>{error}</ErrorText>
            <div className="flex gap-2">
              <Button disabled={!url.trim() || events.length === 0}>Add endpoint</Button>
              <Button type="button" variant="ghost" onClick={() => { setAdding(false); setError(""); }}>Cancel</Button>
            </div>
          </form>
        </Card>
      )}
      {endpoints === null && <Loading />}
      {endpoints?.length === 0 && !adding && (
        <Empty title="No webhooks yet" action={canManage && <Button onClick={() => setAdding(true)}>Add endpoint</Button>}>
          Add a URL to hear about replies, bounces and paused mailboxes the moment they happen. Agents that can't receive webhooks can read the same events from <span className="font-mono text-[12px]">GET /v1/workspaces/{"{ws}"}/events</span>.
        </Empty>
      )}
      {endpoints && endpoints.length > 0 && (
        <DataTable
          data={endpoints}
          rowId={(e) => e.id}
          columnsMenu={false}
          onRowClick={(e) => onOpen(e.id)}
          mobile={(e) => (
            <span className="block space-y-1">
              <span className="block break-all font-mono text-[12px]">{e.url}</span>
              <span className="block text-[12px] text-muted-foreground">{[e.description, eventsLabel(e.events), labelsText(e.categories)].filter(Boolean).join(" · ")}</span>
              {endpointStatus(e)}
            </span>
          )}
          columns={[
            { id: "url", header: "Endpoint", accessorFn: (e) => e.url, cell: ({ row: { original: e } }) => <span className="block min-w-0"><span className="block break-all font-mono text-[12px]">{e.url}</span>{(e.description || e.brand_id) && <span className="block text-[12px] text-muted-foreground">{[e.description, e.brand_id && `Only ${byId(e.brand_id)?.name ?? "one brand"}`].filter(Boolean).join(" · ")}</span>}</span> },
            { id: "events", header: "Events", accessorFn: (e) => e.events.length, cell: ({ row: { original: e } }) => <span className="text-[12px] text-muted-foreground" title={e.events.join("\n")}>{eventsLabel(e.events)}</span> },
            { id: "status", header: "Status", accessorFn: (e) => (e.enabled ? (e.failing_since ? 1 : 0) : 2), cell: ({ row: { original: e } }) => endpointStatus(e) },
            { id: "last", header: "Last delivered", accessorFn: (e) => e.last_success_at ?? "", cell: ({ row: { original: e } }) => <span className="whitespace-nowrap text-muted-foreground" title={e.last_success_at ? stamp(e.last_success_at) : undefined}>{e.last_success_at ? ago(e.last_success_at) : "Never"}</span> },
            { id: "actions", header: () => <span className="sr-only">Actions</span>, enableSorting: false, meta: { align: "right" }, cell: ({ row: { original: e } }) => (
              <span className="inline-flex gap-1">
                <Button variant="ghost" onClick={() => onOpen(e.id)}>Deliveries</Button>
                {canManage && (
                  <Menu
                    items={[
                      { label: "Send a test event", onClick: () => test(e) },
                      { label: "Show signing secret", onClick: () => showSecret(e) },
                      { label: "Make a new signing secret", onClick: () => rotate(e) },
                      { label: "Change URL", onClick: () => changeUrl(e) },
                      e.enabled
                        ? { label: "Turn off", onClick: () => update(e, { enabled: false }, "Turned off. Events are not sent while it is off.") }
                        : { label: "Turn on", onClick: () => update(e, { enabled: true }, "Turned on. New events are sent again.") },
                      { label: "Remove", danger: true, onClick: () => remove(e) },
                    ]}
                  />
                )}
              </span>
            ) },
          ]}
        />
      )}
    </div>
  );
}

function Deliveries({ ws, endpoint, canManage, back, onTest }: { ws: string; endpoint: WebhookEndpoint; canManage: boolean; back: () => void; onTest: () => Promise<void> }) {
  const [rows, setRows] = useState<WebhookDelivery[] | null>(null);
  const [next, setNext] = useState<number | null>(null);
  const [status, setStatus] = useState<"" | WebhookDelivery["status"]>("");
  const [openRow, setOpenRow] = useState<number | null>(null);

  const load = useCallback(async (before?: number) => {
    const { data } = await api.GET("/v1/workspaces/{ws}/webhooks/{id}/deliveries", { params: { path: { ws, id: endpoint.id }, query: { before, status: status || undefined, limit: 50 } } });
    setRows((r) => (before ? [...(r ?? []), ...(data?.data ?? [])] : data?.data ?? []));
    setNext(data?.next_before ?? null);
  }, [ws, endpoint.id, status]);

  useEffect(() => {
    load();
  }, [load]);

  async function redeliver(d: WebhookDelivery) {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/webhooks/{id}/deliveries/{delivery}/redeliver", { params: { path: { ws, id: endpoint.id, delivery: d.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(data?.status === "delivered" ? "Delivered." : `Still failing: ${data?.last_status_code ?? data?.last_error ?? "no answer"}.`, data?.status === "delivered" ? "ok" : "error");
    load();
  }

  const counts = rows ? { delivered: rows.filter((r) => r.status === "delivered").length, failed: rows.filter((r) => r.status === "failed").length } : null;

  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Webhooks", onClick: back }}
        title={<span className="break-all font-mono text-[15px]">{endpoint.url}</span>}
        description={<span className="inline-flex flex-wrap items-center gap-3">{endpointStatus(endpoint)}<span>{eventsLabel(endpoint.events)}</span>{counts && <span>{counts.delivered.toLocaleString()} delivered · {counts.failed.toLocaleString()} failed in this list</span>}</span>}
        action={canManage && <Button variant="ghost" onClick={async () => { await onTest(); load(); }}>Send a test event</Button>}
      />
      <div className="flex items-center gap-2">
        <label className="sr-only" htmlFor="delivery-status">Status</label>
        <select id="delivery-status" className={field} value={status} onChange={(e) => setStatus(e.target.value as typeof status)}>
          <option value="">Every status</option>
          <option value="delivered">Delivered</option>
          <option value="pending">Retrying</option>
          <option value="failed">Failed</option>
          <option value="cancelled">Cancelled</option>
        </select>
        <span className="text-[12px] text-muted-foreground">Kept for 30 days.</span>
      </div>
      {rows === null && <Loading />}
      {rows?.length === 0 && <Empty title="No deliveries yet">{status ? "None with this status." : "Events this endpoint subscribes to show up here as they are sent."}</Empty>}
      {rows && rows.length > 0 && (
        <>
          <DataTable
            data={rows}
            rowId={(d) => String(d.id)}
            columnsMenu={false}
            onRowClick={(d) => setOpenRow(openRow === d.id ? null : d.id)}
            mobile={(d) => (
              <span className="block space-y-1">
                <span className="flex items-center justify-between gap-2"><span className="font-mono text-[12px]">{d.event_type}</span><Badge tone={deliveryTone[d.status]}>{d.status === "pending" ? "retrying" : d.status}</Badge></span>
                <span className="block text-[12px] text-muted-foreground">{d.last_status_code ?? "No answer"} · {plural(d.attempts, "try", "tries")} · {ago(d.created_at)}</span>
                {openRow === d.id && <pre className="max-h-80 overflow-auto rounded-md border bg-muted/40 p-2 text-[11px] leading-4">{JSON.stringify(d.payload, null, 2)}</pre>}
              </span>
            )}
            columns={[
              { id: "event", header: "Event", accessorFn: (d) => d.event_type, cell: ({ row: { original: d } }) => (
                <span className="block min-w-0">
                  <span className="font-mono text-[12px]">{d.event_type}</span>
                  {openRow === d.id && <pre className="mt-2 max-h-80 overflow-auto rounded-md border bg-muted/40 p-2 text-[11px] leading-4">{JSON.stringify(d.payload, null, 2)}</pre>}
                </span>
              ) },
              { id: "status", header: "Status", accessorFn: (d) => d.status, cell: ({ row: { original: d } }) => <Badge tone={deliveryTone[d.status]}>{d.status === "pending" ? "retrying" : d.status}</Badge> },
              { id: "answer", header: "Answer", accessorFn: (d) => d.last_status_code ?? 0, cell: ({ row: { original: d } }) => <span className="text-[12px] text-muted-foreground" title={d.last_error ?? undefined}>{d.last_status_code ?? (d.last_error ? "No answer" : "—")}{d.attempts > 1 && ` · ${plural(d.attempts, "try", "tries")}`}</span> },
              { id: "created", header: "Sent", accessorFn: (d) => d.created_at, cell: ({ row: { original: d } }) => <span className="whitespace-nowrap text-muted-foreground" title={stamp(d.created_at)}>{ago(d.created_at)}</span> },
              { id: "actions", header: () => <span className="sr-only">Actions</span>, enableSorting: false, meta: { align: "right" }, cell: ({ row: { original: d } }) => canManage && <Button variant="ghost" onClick={() => redeliver(d)}>Send again</Button> },
            ]}
          />
          {next !== null && <Button variant="ghost" onClick={() => load(next)}>Show older</Button>}
        </>
      )}
    </div>
  );
}
