import { Fragment, useCallback, useEffect, useState, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, CopyButton, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, confirmAction, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { nameItem } from "@/lib/records";

// Events: what a brand's product sent to POST /v1/brands/{brand}/events.
// Every event is saved first and processed in the background; this page
// shows what happened to each (accepted, duplicate, rejected with what to
// fix, or held for a name the catalog hasn't approved) and replays the
// rejected and held ones once they are fixed.

type Event = Schemas["BrandEvent"];
type Detail = Schemas["BrandEventDetail"];
type Summary = Schemas["BrandEventsSummary"];
type Status = Event["status"];

const statuses: Record<Status, { label: string; tone: "green" | "amber" | "red" | "grey" | "blue"; help: string }> = {
  pending: { label: "Waiting", tone: "blue", help: "Saved, waiting to be processed. Usually seconds." },
  accepted: { label: "Accepted", tone: "green", help: "Stored, and matched to its person." },
  duplicate: { label: "Duplicate", tone: "grey", help: "An event with this messageId was already stored, so this one wasn't stored again." },
  rejected: { label: "Rejected", tone: "red", help: "Something to fix: the reason says what. Fix it, then replay." },
  held: { label: "Held", tone: "amber", help: "A new event name, kept until someone approves it in the catalog. Approve it, then replay." },
};

export function BrandEvents({ ws, canManage, tabs, onOpen }: { ws: string; canManage: boolean; tabs: ReactNode; onOpen: (item: string) => void }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("events", "Events"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Product events"
        description="What your product sent to this brand, and what happened to each event. Events are saved the moment they arrive and processed within seconds; rejected and held ones are kept 90 days so you can fix them and replay them."
      />
      {tabs}
      {brand ? (
        <BrandEventsFor key={brand.id} ws={ws} brand={brand.id} canManage={canManage} onOpen={onOpen} />
      ) : (
        <Empty title="Pick a brand">
          Events belong to one brand. Pick {active.length > 1 ? `one of your ${active.length} brands` : "your brand"} in the brand switcher to see its events.
        </Empty>
      )}
    </div>
  );
}

function BrandEventsFor({ ws, brand, canManage, onOpen }: { ws: string; brand: string; canManage: boolean; onOpen: (item: string) => void }) {
  const [summary, setSummary] = useState<Summary | null>(null);
  const [list, setList] = useState<Event[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [status, setStatus] = useState<Status | "">("");
  const [code, setCode] = useState("");
  const [source, setSource] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [open, setOpen] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const loadSummary = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/events/summary", { params: { path: { brand } } });
    if (!data) return setError(errorMessage(error));
    setSummary(data);
  }, [brand]);

  const load = useCallback(
    async (cursor?: string) => {
      const query = { limit: 50, ...(status ? { status } : {}), ...(code ? { code } : {}), ...(source ? { source } : {}), ...(cursor ? { cursor } : {}) };
      const { data, error } = await api.GET("/v1/brands/{brand}/events", { params: { path: { brand }, query } });
      if (!data) return setError(errorMessage(error));
      setError("");
      setList((prev) => (cursor ? [...(prev ?? []), ...data.data] : data.data));
      setNext(data.next_cursor ?? null);
    },
    [brand, status, code, source],
  );

  const refresh = useCallback(() => {
    void loadSummary();
    void load();
  }, [load, loadSummary]);
  useEffect(() => {
    setPicked(new Set());
    void load();
  }, [load]);
  useEffect(() => {
    void loadSummary();
  }, [loadSummary]);
  // While events wait, look again every few seconds.
  useEffect(() => {
    if (!summary?.pending) return;
    const t = setTimeout(refresh, 3000);
    return () => clearTimeout(t);
  }, [summary, refresh]);

  function filter(name: "status" | "code" | "source", value: string) {
    track("ui.brand_events_filtered", { filter: name, value: name === "source" ? (value ? "set" : "all") : value || "all" });
    if (name === "status") setStatus(value as Status | "");
    if (name === "code") setCode(value);
    if (name === "source") setSource(value);
  }

  async function replay(body: Schemas["BrandEventsReplayInput"], what: string, how: "selected" | "code" | "held_name" | "one") {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/events/replay", { params: { path: { brand } }, body });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    if (data && "replayed" in data) {
      track("ui.brand_events_replayed", { how, replayed: data.replayed });
      toast(data.replayed ? `${plural(data.replayed, "event")} sent back to be processed${data.more ? "; replay again for the rest" : ""}.` : `Nothing to replay in ${what}: only rejected and held events can be.`);
    } else {
      toast("Held for approval in the mission.");
    }
    setPicked(new Set());
    refresh();
  }

  async function approve(name: string) {
    if (!(await confirmAction(`Approve ${name} and replay its events?`, <p>Approved names drive journeys, rules and reports. The events held for it are processed now.</p>, "Approve and replay", false))) return;
    setBusy(true);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/catalog/events/{event}", { params: { path: { ws, event: name } }, body: { status: "approved" } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    await replay({ status: "held", name }, name, "held_name");
  }

  const day = summary?.last_24h;
  const replayable = (e: Event) => e.status === "rejected" || e.status === "held";

  return (
    <div className="space-y-6">
      {summary && day && (
        <Stats cols={5}>
          <Stat label="Accepted, 24h" value={day.accepted.toLocaleString()} sub={`${summary.last_7d.accepted.toLocaleString()} in 7 days`} />
          <Stat label="Duplicates, 24h" value={day.duplicate.toLocaleString()} />
          <Stat label="Rejected, 24h" value={day.rejected.toLocaleString()} tone={day.rejected ? "red" : undefined} sub={`${summary.last_7d.rejected.toLocaleString()} in 7 days`} />
          <Stat label="Held, 24h" value={day.held.toLocaleString()} tone={day.held ? "amber" : undefined} sub={`${summary.last_7d.held.toLocaleString()} in 7 days`} />
          <Stat label="Waiting" value={summary.pending.toLocaleString()} sub={summary.oldest_pending_at ? `oldest ${ago(summary.oldest_pending_at)}` : "none"} />
        </Stats>
      )}

      {summary && summary.held_names.length > 0 && (
        <Card title="Names waiting for approval" description="Events with a name the catalog hasn't seen are held: kept, but changing no one, until someone approves the name. Approving one processes its held events.">
          <ul className="divide-y">
            {summary.held_names.map((n) => (
              <li key={n.name} className="flex flex-wrap items-center gap-3 py-2">
                <span className="font-mono text-[13px]">{n.name}</span>
                <span className="text-[12px] text-muted-foreground">{plural(n.count, "event")}{n.expires_at ? `, the first deleted ${stamp(n.expires_at)}` : ""}</span>
                <span className="ml-auto flex gap-2">
                  <Button variant="ghost" onClick={() => filter("status", "held")}>Show</Button>
                  {canManage && <Button disabled={busy} onClick={() => approve(n.name)}>Approve and replay</Button>}
                </span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {summary && summary.rejection_codes.length > 0 && (
        <Card title="Why events were rejected" description={`Rejected events are kept ${summary.retention_days.rejected ?? 90} days. Fix what the reason says, in your product or the catalog, then replay them.`}>
          <ul className="divide-y">
            {summary.rejection_codes.map((c) => (
              <li key={c.code} className="flex flex-wrap items-start gap-3 py-2">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-[13px]">{c.code}</span>
                    <span className="text-[12px] text-muted-foreground">{plural(c.count, "event")}{c.expires_at ? `, the first deleted ${stamp(c.expires_at)}` : ""}</span>
                  </div>
                  {c.reason && <p className="mt-0.5 text-[12px] text-muted-foreground">{c.reason}</p>}
                </div>
                <span className="flex gap-2">
                  <Button variant="ghost" onClick={() => { filter("status", "rejected"); filter("code", c.code); }}>Show</Button>
                  {canManage && <Button variant="ghost" disabled={busy} onClick={() => replay({ status: "rejected", code: c.code }, c.code, "code")}>Replay all {c.count.toLocaleString()}</Button>}
                </span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="Status" className={field} value={status} onChange={(e) => filter("status", e.target.value)}>
          <option value="">Every status</option>
          {(Object.keys(statuses) as Status[]).map((s) => <option key={s} value={s}>{statuses[s].label}</option>)}
        </select>
        <select aria-label="Code" className={field} value={code} onChange={(e) => filter("code", e.target.value)}>
          <option value="">Any code</option>
          {code && !summary?.rejection_codes.some((c) => c.code === code) && <option value={code}>{code}</option>}
          {summary?.rejection_codes.map((c) => <option key={c.code} value={c.code}>{c.code}</option>)}
          <option value="name_not_approved">name_not_approved</option>
        </select>
        <input aria-label="Source" className={`${field} w-40`} placeholder="Source, such as segment" value={source} onChange={(e) => filter("source", e.target.value.trim())} />
        {canManage && picked.size > 0 && (
          <Button className="ml-auto" disabled={busy} onClick={() => replay({ ids: [...picked] }, "the selected events", "selected")}>Replay {plural(picked.size, "event")}</Button>
        )}
      </div>

      <ErrorText>{error}</ErrorText>
      {!list && !error && <Loading />}
      {list?.length === 0 && (
        <Empty title={status || code || source ? "Nothing matches" : "No events yet"}>
          {status || code || source ? "Try another filter." : <>Send events to <span className="font-mono">POST /v1/brands/{brand}/events</span>, in Segment's format, from your product's servers. Each shows here within seconds.</>}
        </Empty>
      )}
      {list && list.length > 0 && (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-[13px]">
            <thead className="bg-muted/40 text-left text-[12px] text-muted-foreground">
              <tr>
                {canManage && <th className="w-8 px-3 py-2"><span className="sr-only">Pick</span></th>}
                <th className="px-3 py-2 font-medium">Event</th>
                <th className="px-3 py-2 font-medium">Status</th>
                <th className="hidden px-3 py-2 font-medium md:table-cell">Source</th>
                <th className="px-3 py-2 font-medium">Received</th>
              </tr>
            </thead>
            <tbody>
              {list.map((e) => (
                <Fragment key={e.id}>
                  <tr className="cursor-pointer border-t hover:bg-muted/30" onClick={() => setOpen(open === e.id ? null : e.id)}>
                    {canManage && (
                      <td className="px-3 py-2" onClick={(ev) => ev.stopPropagation()}>
                        {replayable(e) && (
                          <input
                            type="checkbox"
                            aria-label="Pick to replay"
                            checked={picked.has(e.id)}
                            onChange={(ev) => {
                              const s = new Set(picked);
                              if (ev.target.checked) s.add(e.id);
                              else s.delete(e.id);
                              setPicked(s);
                            }}
                          />
                        )}
                      </td>
                    )}
                    <td className="px-3 py-2">
                      <span className="font-mono">{e.name ?? "(no name)"}</span>
                      <span className="ml-2 text-[12px] text-muted-foreground">{e.type ?? "?"}</span>
                    </td>
                    <td className="px-3 py-2">
                      <Badge tone={statuses[e.status].tone} title={statuses[e.status].help}>{statuses[e.status].label}</Badge>
                      {e.code && <span className="ml-2 font-mono text-[12px] text-muted-foreground">{e.code}</span>}
                    </td>
                    <td className="hidden px-3 py-2 text-muted-foreground md:table-cell">{e.source}</td>
                    <td className="px-3 py-2 text-muted-foreground" title={stamp(e.received_at)}>{ago(e.received_at)}</td>
                  </tr>
                  {open === e.id && (
                    <tr className="border-t bg-muted/20">
                      <td colSpan={canManage ? 5 : 4} className="px-3 py-3">
                        <EventDetail brand={brand} id={e.id} canManage={canManage} busy={busy} onOpen={onOpen} onReplay={() => replay({ ids: [e.id] }, "this event", "one")} />
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {next && <Button variant="ghost" onClick={() => load(next)}>Show more</Button>}
    </div>
  );
}

function EventDetail({ brand, id, canManage, busy, onOpen, onReplay }: { brand: string; id: string; canManage: boolean; busy: boolean; onOpen: (item: string) => void; onReplay: () => void }) {
  const [ev, setEv] = useState<Detail | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/events/{event}", { params: { path: { brand, event: id } } }).then(({ data, error }) => {
      if (!data) return setError(errorMessage(error));
      setEv(data);
      track("ui.brand_event_opened", { status: data.status, code: data.code ?? undefined });
    });
  }, [brand, id]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!ev) return <Loading />;
  const raw = JSON.stringify(ev.raw, null, 2);
  return (
    <div className="space-y-3">
      <p className="text-[13px]">{statuses[ev.status].help}</p>
      {ev.reason && (
        <div className="rounded-md border bg-card px-3 py-2 text-[13px]">
          <span className="font-medium">{ev.status === "rejected" ? "What to fix: " : ""}</span>
          {ev.reason}
        </div>
      )}
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-[12px]">
        <dt className="text-muted-foreground">Receipt</dt>
        <dd className="font-mono">{ev.id}</dd>
        <dt className="text-muted-foreground">Batch</dt>
        <dd className="font-mono">{ev.batch_id}</dd>
        {ev.message_id && (<><dt className="text-muted-foreground">messageId</dt><dd className="font-mono">{ev.message_id}</dd></>)}
        <dt className="text-muted-foreground">Received</dt>
        <dd>{stamp(ev.received_at)}{ev.processed_at ? `, processed ${stamp(ev.processed_at)}` : ""}</dd>
        {ev.expires_at && (<><dt className="text-muted-foreground">Deleted</dt><dd>{stamp(ev.expires_at)}</dd></>)}
        {ev.record_id && (
          <>
            <dt className="text-muted-foreground">Person</dt>
            <dd><button className="underline" onClick={() => onOpen(ev.record_id!)}>Open their record</button></dd>
          </>
        )}
      </dl>
      <div>
        <div className="mb-1 flex items-center gap-2 text-[12px] text-muted-foreground">
          As sent <CopyButton text={raw} label="Copy JSON" />
        </div>
        <pre className="max-h-80 overflow-auto rounded-md border bg-card p-3 font-mono text-[12px]">{raw}</pre>
      </div>
      {canManage && (ev.status === "rejected" || ev.status === "held") && (
        <Button disabled={busy} onClick={onReplay}>Replay this event</Button>
      )}
    </div>
  );
}
