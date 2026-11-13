// Tracker → Traffic sources and Affiliate networks (plans/ad-tracker-ui.md
// B4). New opens a gallery of the templates (search, category chips, Custom
// first, Connected where Outflare already syncs the network). A traffic
// source's form has its click id, cost and parameters table with a live
// campaign URL preview, and its postback back to the network with a token
// picker and send rules; a value marked REPLACE blocks the save until it is
// filled. An affiliate network's form gives the postback URL to paste into
// the network, shown once and rotated on request.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { ConnectorLogo } from "@/components/connector-logo";
import { PickBrand } from "@/components/pick-brand";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, CopyButton, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import type { Tab } from "../App";

type Source = Schemas["TrafficSource"];
type Template = Schemas["TrafficSourceTemplate"];
type Param = Schemas["TrafficSourceParameter"];
type Send = Schemas["TrafficSourcePostbackSend"];
type Network = Schemas["PostbackSource"];
type NetworkTemplate = Schemas["AffiliateNetworkTemplate"];

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };

// REPLACE marks a value only the brand has (integrations/traffic-sources).
export const placeholder = "REPLACE";

const categories: { id: Template["category"] | "all"; label: string }[] = [
  { id: "all", label: "All" },
  { id: "search_social", label: "Search & social" },
  { id: "native", label: "Native" },
  { id: "push_pop", label: "Push & pop" },
  { id: "display", label: "Display" },
  { id: "mobile", label: "Mobile" },
  { id: "adult", label: "Adult" },
];

// The tokens a postback back to the network can carry.
const postbackTokens: [string, string][] = [
  ["{source_click_id}", "Network click id"], ["{payout}", "Payout"], ["{currency}", "Currency"], ["{transaction_id}", "Transaction id"],
  ["{conversion_type}", "Type"], ["{status}", "Status"], ["{campaign}", "Campaign"], ["{country}", "Country"], ["{random}", "Random"],
];

// campaignURL is what a campaign on the source carries: the redirect link
// with each parameter set to the network's token.
export function campaignURL(click: Schemas["TrafficSourceParam"], cost: Schemas["TrafficSourceParam"], params: Param[]): string {
  const pairs = [click, cost, ...params].filter((p) => p.param && p.token).map((p) => `${p.param}=${p.token}`);
  return `https://<your tracking domain>/l/<code>${pairs.length ? `?${pairs.join("&")}` : ""}`;
}

// Monogram stands in for a network Outflare has no logo for.
function Monogram({ name }: { name: string }) {
  return <span aria-hidden className="inline-flex size-8 shrink-0 items-center justify-center rounded-md border bg-muted text-sm font-semibold">{name.slice(0, 1).toUpperCase()}</span>;
}

// Gallery: the templates as tiles, Custom first.
function Gallery<T extends { id: string; name: string }>({ open, title, description, items, chips, chipOf, badge, logo, onPick, onClose }: {
  open: boolean;
  title: string;
  description: string;
  items: T[];
  chips: { id: string; label: string }[];
  chipOf: (t: T) => string;
  badge?: (t: T) => string | null;
  logo: (t: T) => React.ReactNode;
  onPick: (t: T | null) => void;
  onClose: () => void;
}) {
  const [q, setQ] = useState("");
  const [chip, setChip] = useState("all");
  const shown = items.filter((t) => (chip === "all" || chipOf(t) === chip) && t.name.toLowerCase().includes(q.trim().toLowerCase()));
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <input aria-label="Search" autoFocus className={`${field} w-full`} placeholder="Search" value={q} onChange={(e) => setQ(e.target.value)} />
        <div className="flex flex-wrap gap-1">
          {chips.map((c) => (
            <button key={c.id} type="button" aria-pressed={chip === c.id} className={`rounded-full border px-2.5 py-0.5 text-xs ${chip === c.id ? "border-primary bg-primary text-primary-foreground" : "hover:bg-muted"}`} onClick={() => setChip(c.id)}>
              {c.label}
            </button>
          ))}
        </div>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 md:grid-cols-4">
          <button type="button" className="flex items-center gap-2 rounded-md border border-dashed p-3 text-left text-sm hover:bg-muted" onClick={() => onPick(null)}>
            <Monogram name="+" />
            <span className="font-medium">Custom</span>
          </button>
          {shown.map((t) => (
            <button key={t.id} type="button" className="flex items-center gap-2 rounded-md border p-3 text-left text-sm hover:bg-muted" onClick={() => onPick(t)}>
              {logo(t)}
              <span className="min-w-0">
                <span className="block truncate font-medium">{t.name}</span>
                {badge?.(t) && <Badge tone="green">{badge(t)}</Badge>}
              </span>
            </button>
          ))}
        </div>
        {shown.length === 0 && <p className="text-sm text-muted-foreground">Nothing matches: start from Custom and fill in the network's parameters.</p>}
      </DialogContent>
    </Dialog>
  );
}

// TokenInput: an input with chips under it that insert a token at the cursor.
function TokenInput({ label, value, onChange, tokens, disabled, placeholder: ph }: { label: string; value: string; onChange: (v: string) => void; tokens: [string, string][]; disabled: boolean; placeholder?: string }) {
  const ref = useRef<HTMLInputElement>(null);
  const insert = (t: string) => {
    const el = ref.current;
    const at = el?.selectionStart ?? value.length;
    const end = el?.selectionEnd ?? at;
    onChange(value.slice(0, at) + t + value.slice(end));
    requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(at + t.length, at + t.length);
    });
  };
  const unfilled = value.includes(placeholder);
  return (
    <div className="text-sm">
      <input aria-label={label} ref={ref} className={`${field} w-full font-mono text-xs ${unfilled ? "border-amber-500 ring-1 ring-amber-500" : ""}`} placeholder={ph} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)} />
      {unfilled && <p className="mt-1 text-xs text-amber-700 dark:text-amber-400">Replace each REPLACE with your own value from the network's dashboard.</p>}
      {!disabled && (
        <div className="mt-1 flex flex-wrap gap-1">
          {tokens.map(([t, l]) => (
            <button key={t} type="button" title={t} className="rounded-full border px-2 py-0.5 text-xs hover:bg-muted" onClick={() => insert(t)}>
              {l}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

export function TrackerTrafficSources({ ws, canManage, go }: Props) {
  const { brand, active } = useBrand(ws);
  const [sources, setSources] = useState<Source[] | null>(null);
  const [templates, setTemplates] = useState<Template[]>([]);
  const [connected, setConnected] = useState<Set<string>>(new Set());
  const [error, setError] = useState("");
  const [gallery, setGallery] = useState(false);
  const [open, setOpen] = useState<{ source: Source | null; template: Template | null } | null>(null);
  const load = useCallback(async () => {
    const b = pickedBrandId();
    const [s, t, i] = await Promise.all([
      api.GET("/v1/brands/{brand}/traffic-sources", { params: { path: { brand: b } } }),
      api.GET("/v1/brands/{brand}/traffic-source-templates", { params: { path: { brand: b } } }),
      api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws } } }),
    ]);
    if (s.error) return setError(errorMessage(s.error));
    setSources((s.data?.data ?? []) as Source[]);
    setTemplates((t.data?.data ?? []) as Template[]);
    setConnected(new Set(((i.data?.data ?? []) as { connector: string; status: string }[]).filter((x) => x.status === "connected").map((x) => x.connector)));
  }, [ws]);
  useEffect(() => {
    if (brand) void load();
  }, [brand, load]);
  if (!brand) return <PickBrand what="traffic sources" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!sources) return <Loading />;
  const templateName = (id: string) => templates.find((t) => t.id === id)?.name ?? "Custom";
  return (
    <div className="space-y-4">
      <PageHeader
        title="Traffic sources"
        description="The ad networks your campaigns buy clicks from: what each puts in the campaign URL, and the conversions sent back to it."
        action={canManage && <Button variant="primary" onClick={() => setGallery(true)}>New traffic source</Button>}
      />
      {sources.length === 0 ? (
        <Empty title="No traffic sources yet">Pick the network you buy clicks from: its click id, cost and parameters come filled in.</Empty>
      ) : (
        <Card title={`${sources.length} traffic source${sources.length === 1 ? "" : "s"}`}>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  <th className="py-2 pr-3 font-medium">Traffic source</th>
                  <th className="px-3 py-2 font-medium">Template</th>
                  <th className="px-3 py-2 font-medium">Conversions back</th>
                  <th className="px-3 py-2 text-right font-medium">Campaigns</th>
                </tr>
              </thead>
              <tbody>
                {sources.map((s) => (
                  <tr key={s.id} className="cursor-pointer border-t hover:bg-muted/40" onClick={() => setOpen({ source: s, template: templates.find((t) => t.id === s.template) ?? null })}>
                    <td className="py-2 pr-3 font-medium">{s.name}</td>
                    <td className="px-3 py-2">{templateName(s.template)}</td>
                    <td className="px-3 py-2">{s.postback.url ? <Badge tone="green">Postback</Badge> : <span className="text-muted-foreground">None</span>}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{s.campaigns}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      <Gallery
        open={gallery}
        title="New traffic source"
        description="Pick the network: its click id, cost and parameters come filled in, and every field can be edited."
        items={templates}
        chips={categories}
        chipOf={(t) => t.category}
        badge={(t) => (t.connector && connected.has(t.connector) ? "Connected" : null)}
        logo={(t) => (t.connector ? <ConnectorLogo id={t.connector} size="sm" /> : <Monogram name={t.name} />)}
        onPick={(t) => {
          setGallery(false);
          setOpen({ source: null, template: t });
        }}
        onClose={() => setGallery(false)}
      />
      <Sheet open={open !== null} onOpenChange={(o) => !o && setOpen(null)}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
          {open !== null && (
            <SourceForm
              key={open.source?.id ?? `new:${open.template?.id ?? "custom"}`}
              source={open.source}
              template={open.template}
              connected={!!open.template?.connector && connected.has(open.template.connector)}
              canManage={canManage}
              go={go}
              onSaved={() => {
                setOpen(null);
                void load();
              }}
            />
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}

const blankParam: Schemas["TrafficSourceParam"] = { param: "", token: "" };

function SourceForm({ source, template, connected, canManage, go, onSaved }: {
  source: Source | null;
  template: Template | null;
  connected: boolean;
  canManage: boolean;
  go: (tab: Tab) => void;
  onSaved: () => void;
}) {
  const from = source ?? template;
  const [name, setName] = useState(source?.name ?? template?.name ?? "");
  const [currency, setCurrency] = useState(from?.currency ?? "USD");
  const [click, setClick] = useState(from?.click_id ?? blankParam);
  const [cost, setCost] = useState(from?.cost ?? blankParam);
  const [params, setParams] = useState<Param[]>(from?.parameters ?? []);
  const [postback, setPostback] = useState(source ? source.postback.url : template?.postback?.url ?? "");
  const [method, setMethod] = useState<"GET" | "POST">(source ? source.postback.method : template?.postback?.method ?? "GET");
  const rules = source?.postback_rules;
  const [share, setShare] = useState(rules?.share ?? 100);
  const [decimals, setDecimals] = useState(rules?.decimals ?? 2);
  const [minPayout, setMinPayout] = useState(rules?.min_payout ?? "0");
  const [types, setTypes] = useState((rules?.types ?? []).join(", "));
  const [statuses, setStatuses] = useState<string[]>(rules?.statuses ?? ["approved"]);
  const [resend, setResend] = useState(rules?.resend ?? false);
  const [sends, setSends] = useState<Send[] | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!source) return;
    api.GET("/v1/brands/{brand}/traffic-sources/{id}/postbacks", { params: { path: { brand: pickedBrandId(), id: source.id } } }).then(({ data }) => setSends(((data?.data ?? []) as Send[]).slice(0, 10)));
  }, [source]);
  const preview = useMemo(() => campaignURL(click, cost, params), [click, cost, params]);
  // Every field still holding REPLACE; the save waits for them.
  const unfilled = [
    ...(postback.includes(placeholder) ? ["the postback URL"] : []),
    ...[click, cost, ...params].filter((p) => p.param.includes(placeholder) || p.token.includes(placeholder)).map((p) => `the ${p.param || "parameter"} parameter`),
  ];
  const usedFields = new Set(params.map((p) => p.field));
  const nextField = () => Array.from({ length: 20 }, (_, i) => i + 1).find((f) => !usedFields.has(f)) ?? 20;
  const save = async () => {
    setBusy(true);
    const brand = pickedBrandId();
    const body = {
      name: name.trim(),
      currency,
      click_id: click,
      cost,
      parameters: params,
      postback: { url: postback.trim(), method },
      postback_rules: { share, decimals, min_payout: minPayout, types: types.split(",").map((t) => t.trim()).filter(Boolean), statuses: statuses as ("approved" | "pending" | "declined")[], resend },
    };
    const r = source
      ? await api.PATCH("/v1/brands/{brand}/traffic-sources/{id}", { params: { path: { brand, id: source.id } }, body })
      : await api.POST("/v1/brands/{brand}/traffic-sources", { params: { path: { brand } }, body: { ...body, template: template?.id } });
    setBusy(false);
    if (r.error) return toast(errorMessage(r.error), "error");
    toast(source ? "Traffic source saved" : "Traffic source added", "ok");
    onSaved();
  };
  const archive = async () => {
    if (!source || !(await confirmAction(`Archive ${source.name}?`, "It leaves the list and can't be picked for new campaigns. Its campaigns and reports stay.", "Archive"))) return;
    const { error } = await api.PATCH("/v1/brands/{brand}/traffic-sources/{id}", { params: { path: { brand: pickedBrandId(), id: source.id } }, body: { status: "archived" } });
    if (error) return toast(errorMessage(error), "error");
    toast("Traffic source archived", "ok");
    onSaved();
  };
  const paramRow = (label: string, v: Schemas["TrafficSourceParam"], set: (p: Schemas["TrafficSourceParam"]) => void) => (
    <div className="grid grid-cols-[110px_1fr_1fr] items-center gap-2 text-sm">
      <span>{label}</span>
      <input aria-label={`${label} parameter`} className={`${field} font-mono text-xs`} placeholder="parameter" value={v.param} disabled={!canManage} onChange={(e) => set({ ...v, param: e.target.value })} />
      <input aria-label={`${label} token`} className={`${field} font-mono text-xs`} placeholder="network token" value={v.token} disabled={!canManage} onChange={(e) => set({ ...v, token: e.target.value })} />
    </div>
  );
  return (
    <form className="space-y-5 p-4" onSubmit={(e) => { e.preventDefault(); void save(); }}>
      <SheetHeader className="p-0">
        <SheetTitle>{source ? source.name : template ? `New ${template.name} source` : "New custom traffic source"}</SheetTitle>
        <SheetDescription>{template?.note || "What the network puts in the campaign URL, and the conversions sent back to it."}</SheetDescription>
      </SheetHeader>
      <div className="grid gap-3 sm:grid-cols-[1fr_100px]">
        <label className="text-sm">
          Name
          <input aria-label="Name" required className={`${field} mt-1 w-full`} value={name} disabled={!canManage} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="text-sm">
          Currency
          <input aria-label="Currency" className={`${field} mt-1 w-full uppercase`} maxLength={3} value={currency} disabled={!canManage} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />
        </label>
      </div>
      <section className="space-y-2">
        <h3 className="text-sm font-medium">Campaign URL</h3>
        {paramRow("Click id", click, setClick)}
        {paramRow("Cost", cost, setCost)}
        <table className="w-full text-sm" aria-label="Parameters">
          <thead>
            <tr className="text-left text-xs text-muted-foreground">
              <th className="py-1 font-medium">Name in reports</th>
              <th className="py-1 font-medium">Parameter</th>
              <th className="py-1 font-medium">Token</th>
              <th className="py-1 font-medium">Field</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {params.map((p, i) => {
              const set = (patch: Partial<Param>) => setParams(params.map((x, j) => (j === i ? { ...x, ...patch } : x)));
              return (
                <tr key={i}>
                  <td className="py-0.5 pr-1"><input aria-label="Parameter name" className={`${field} w-full`} value={p.name} disabled={!canManage} onChange={(e) => set({ name: e.target.value })} /></td>
                  <td className="py-0.5 pr-1"><input aria-label="Parameter" className={`${field} w-full font-mono text-xs`} value={p.param} disabled={!canManage} onChange={(e) => set({ param: e.target.value })} /></td>
                  <td className="py-0.5 pr-1"><input aria-label="Token" className={`${field} w-full font-mono text-xs`} value={p.token} disabled={!canManage} onChange={(e) => set({ token: e.target.value })} /></td>
                  <td className="py-0.5 pr-1">
                    <select aria-label="Field" className={field} value={p.field} disabled={!canManage} onChange={(e) => set({ field: Number(e.target.value) })}>
                      {Array.from({ length: 20 }, (_, f) => f + 1).filter((f) => f === p.field || !usedFields.has(f)).map((f) => (
                        <option key={f} value={f}>{f}</option>
                      ))}
                    </select>
                  </td>
                  <td>{canManage && <Button type="button" variant="ghost" aria-label="Remove parameter" onClick={() => setParams(params.filter((_, j) => j !== i))}>×</Button>}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {canManage && params.length < 20 && (
          <Button type="button" variant="ghost" onClick={() => setParams([...params, { name: "", param: "", token: "", field: nextField() }])}>+ Add parameter</Button>
        )}
        <div className="text-xs text-muted-foreground">A campaign on this source links to</div>
        <div className="break-all rounded-md border bg-muted/40 p-2 font-mono text-xs" data-testid="url-preview">{preview}</div>
      </section>
      <section className="space-y-2">
        <h3 className="text-sm font-medium">Conversions back to the network</h3>
        {template?.connector && (
          <p className="text-xs text-muted-foreground">
            {connected ? "Outflare syncs this network: its conversion API" : "This network has a conversion API"}, which sends richer data than a postback. Set it up in{" "}
            <button type="button" className="underline" onClick={() => go("ads")}>Paid ads → Send conversions</button>.
          </p>
        )}
        <div className="flex gap-2">
          <select aria-label="Postback method" className={field} value={method} disabled={!canManage} onChange={(e) => setMethod(e.target.value as "GET" | "POST")}>
            <option value="GET">GET</option>
            <option value="POST">POST</option>
          </select>
          <div className="min-w-0 flex-1">
            <TokenInput label="Postback URL" value={postback} onChange={setPostback} tokens={postbackTokens} disabled={!canManage} placeholder="None: no conversions are sent back" />
          </div>
        </div>
        {postback && (
          <div className="grid gap-2 text-sm sm:grid-cols-3">
            <label>
              Payout share %
              <input aria-label="Payout share" type="number" min={0} max={1000} className={`${field} mt-1 w-full`} value={share} disabled={!canManage} onChange={(e) => setShare(Math.max(0, Math.min(1000, Number(e.target.value) || 0)))} />
            </label>
            <label>
              Decimals
              <input aria-label="Decimals" type="number" min={0} max={6} className={`${field} mt-1 w-full`} value={decimals} disabled={!canManage} onChange={(e) => setDecimals(Math.max(0, Math.min(6, Number(e.target.value) || 0)))} />
            </label>
            <label>
              Smallest payout sent
              <input aria-label="Smallest payout" inputMode="decimal" className={`${field} mt-1 w-full`} value={minPayout} disabled={!canManage} onChange={(e) => setMinPayout(e.target.value)} />
            </label>
            <label className="sm:col-span-3">
              Conversion types sent
              <input aria-label="Conversion types" className={`${field} mt-1 w-full`} placeholder="Every type" value={types} disabled={!canManage} onChange={(e) => setTypes(e.target.value)} />
            </label>
            <fieldset className="flex flex-wrap items-center gap-3 sm:col-span-3">
              <legend className="mb-1">Statuses sent</legend>
              {(["approved", "pending", "declined"] as const).map((s) => (
                <label key={s} className="flex items-center gap-1">
                  <input type="checkbox" checked={statuses.includes(s)} disabled={!canManage} onChange={(e) => setStatuses(e.target.checked ? [...statuses, s] : statuses.filter((x) => x !== s))} /> {s}
                </label>
              ))}
              <label className="flex items-center gap-1">
                <input type="checkbox" checked={resend} disabled={!canManage} onChange={(e) => setResend(e.target.checked)} /> Send again when its status or payout changes
              </label>
            </fieldset>
          </div>
        )}
      </section>
      {sends && sends.length > 0 && (
        <section className="space-y-1">
          <h3 className="text-sm font-medium">Recent postbacks</h3>
          <table className="w-full text-xs">
            <tbody>
              {sends.map((s) => (
                <tr key={s.id} className="border-t">
                  <td className="py-1 pr-2"><Badge tone={s.status === "sent" ? "green" : s.status === "failed" ? "red" : "grey"}>{s.status}</Badge></td>
                  <td className="py-1 pr-2 font-mono tabular-nums">{s.payout}</td>
                  <td className="py-1 pr-2 tabular-nums">{s.http_status ?? "—"}</td>
                  <td className="max-w-0 truncate py-1 font-mono" title={s.error || s.url}>{s.error || s.url}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}
      {unfilled.length > 0 && <ErrorText>Fill in REPLACE in {unfilled.join(", ")} before saving, or clear the postback URL to send no conversions back.</ErrorText>}
      {canManage && (
        <div className="flex flex-wrap gap-2">
          <Button type="submit" variant="primary" disabled={busy || unfilled.length > 0}>{source ? "Save" : "Add traffic source"}</Button>
          {source && <Button type="button" variant="ghost" onClick={() => void archive()}>Archive</Button>}
        </div>
      )}
    </form>
  );
}

const policies: { id: Network["duplicate_policy"]; label: string; help: string }[] = [
  { id: "ignore", label: "Ignore", help: "A transaction id seen before is dropped." },
  { id: "new", label: "New conversion", help: "Each postback is its own conversion." },
  { id: "update", label: "Update", help: "It changes the conversion recorded, such as an approval." },
  { id: "add", label: "Add payout", help: "Its payout is added, for upsells." },
];

export function TrackerNetworks({ ws, canManage }: Props) {
  const { brand, active } = useBrand(ws);
  const [networks, setNetworks] = useState<Network[] | null>(null);
  const [templates, setTemplates] = useState<NetworkTemplate[]>([]);
  const [error, setError] = useState("");
  const [gallery, setGallery] = useState(false);
  const [open, setOpen] = useState<{ network: Network | null; template: NetworkTemplate | null } | null>(null);
  const load = useCallback(async () => {
    const b = pickedBrandId();
    const [n, t] = await Promise.all([
      api.GET("/v1/brands/{brand}/postback-sources", { params: { path: { brand: b } } }),
      api.GET("/v1/brands/{brand}/affiliate-network-templates", { params: { path: { brand: b } } }),
    ]);
    if (n.error) return setError(errorMessage(n.error));
    const list = (n.data?.data ?? []) as Network[];
    setNetworks(list);
    setTemplates((t.data?.data ?? []) as NetworkTemplate[]);
    return list;
  }, []);
  useEffect(() => {
    if (brand) void load();
  }, [brand, load]);
  if (!brand) return <PickBrand what="affiliate networks" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!networks) return <Loading />;
  const templateName = (id: string) => templates.find((t) => t.id === id)?.name ?? "Custom";
  return (
    <div className="space-y-4">
      <PageHeader
        title="Affiliate networks"
        description="Where your conversions come from: each network gets its own postback URL, in its own macros."
        action={canManage && <Button variant="primary" onClick={() => setGallery(true)}>New affiliate network</Button>}
      />
      {networks.length === 0 ? (
        <Empty title="No affiliate networks yet">Add the network that pays you: paste its postback URL into the network, and its conversions come in on the right clicks.</Empty>
      ) : (
        <Card title={`${networks.length} affiliate network${networks.length === 1 ? "" : "s"}`}>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  <th className="py-2 pr-3 font-medium">Network</th>
                  <th className="px-3 py-2 font-medium">Template</th>
                  <th className="px-3 py-2 font-medium">Duplicates</th>
                  <th className="px-3 py-2 text-right font-medium">Conversions</th>
                </tr>
              </thead>
              <tbody>
                {networks.map((n) => (
                  <tr key={n.id} className="cursor-pointer border-t hover:bg-muted/40" onClick={() => setOpen({ network: n, template: templates.find((t) => t.id === n.network) ?? null })}>
                    <td className="py-2 pr-3">
                      <span className="font-medium">{n.name}</span> <span className="font-mono text-xs text-muted-foreground">{n.token_hint}…</span>
                    </td>
                    <td className="px-3 py-2">{templateName(n.network)}</td>
                    <td className="px-3 py-2">{policies.find((p) => p.id === n.duplicate_policy)?.label}</td>
                    <td className="px-3 py-2 text-right font-mono tabular-nums">{n.conversions.toLocaleString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      <Gallery
        open={gallery}
        title="New affiliate network"
        description="Pick the network or the platform it runs on: its postback URL comes in its own macros."
        items={templates}
        chips={[{ id: "all", label: "All" }, { id: "network", label: "Networks" }, { id: "platform", label: "Platforms" }]}
        chipOf={(t) => t.kind}
        logo={(t) => <Monogram name={t.name} />}
        onPick={(t) => {
          setGallery(false);
          setOpen({ network: null, template: t });
        }}
        onClose={() => setGallery(false)}
      />
      <Sheet open={open !== null} onOpenChange={(o) => !o && setOpen(null)}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
          {open !== null && (
            <NetworkForm
              key={open.network?.id ?? `new:${open.template?.id ?? "custom"}`}
              network={open.network}
              template={open.template}
              canManage={canManage}
              onSaved={async (saved, close) => {
                const list = await load();
                if (close) return setOpen(null);
                // A new network stays open with its postback URL, shown once.
                const fresh = list?.find((n) => n.id === saved.id);
                if (fresh) setOpen({ network: { ...fresh, postback_url: saved.postback_url }, template: templates.find((t) => t.id === fresh.network) ?? null });
              }}
            />
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}

function NetworkForm({ network, template, canManage, onSaved }: { network: Network | null; template: NetworkTemplate | null; canManage: boolean; onSaved: (n: Network, close: boolean) => void }) {
  const [name, setName] = useState(network?.name ?? template?.name ?? "");
  const [clickParam, setClickParam] = useState(network?.click_id_param ?? template?.click_id_param ?? "");
  const [policy, setPolicy] = useState<Network["duplicate_policy"]>(network?.duplicate_policy ?? "ignore");
  const [ips, setIps] = useState((network?.ip_allowlist ?? []).join("\n"));
  const [currency, setCurrency] = useState(network?.currency ?? "USD");
  const [expiry, setExpiry] = useState(network?.click_expiry_days ?? 30);
  const [url, setUrl] = useState(network?.postback_url ?? "");
  const [busy, setBusy] = useState(false);
  const save = async () => {
    setBusy(true);
    const brand = pickedBrandId();
    const body = { name: name.trim(), click_id_param: clickParam.trim(), duplicate_policy: policy, ip_allowlist: ips.split(/[\s,]+/).filter(Boolean), currency, click_expiry_days: expiry };
    const r = network
      ? await api.PATCH("/v1/brands/{brand}/postback-sources/{id}", { params: { path: { brand, id: network.id } }, body })
      : await api.POST("/v1/brands/{brand}/postback-sources", { params: { path: { brand } }, body: { ...body, network: template?.id ?? "" } });
    setBusy(false);
    if (r.error) return toast(errorMessage(r.error), "error");
    toast(network ? "Network saved" : "Network added: copy its postback URL into the network", "ok");
    onSaved(r.data as Network, !!network);
  };
  const rotate = async () => {
    if (!network || !(await confirmAction("Make a new postback URL?", "The URL the network has now stops working at once: paste the new one into the network straight away.", "Make a new URL"))) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/postback-sources/{id}/rotate", { params: { path: { brand: pickedBrandId(), id: network.id } } });
    if (error) return toast(errorMessage(error), "error");
    setUrl((data as Network).postback_url ?? "");
    toast("New postback URL made", "ok");
  };
  const archive = async () => {
    if (!network || !(await confirmAction(`Archive ${network.name}?`, "Its postback URL stops taking conversions. Its conversions and reports stay.", "Archive"))) return;
    const { error } = await api.PATCH("/v1/brands/{brand}/postback-sources/{id}", { params: { path: { brand: pickedBrandId(), id: network.id } }, body: { status: "archived" } });
    if (error) return toast(errorMessage(error), "error");
    toast("Network archived", "ok");
    onSaved(network, true);
  };
  return (
    <form className="space-y-4 p-4" onSubmit={(e) => { e.preventDefault(); void save(); }}>
      <SheetHeader className="p-0">
        <SheetTitle>{network ? network.name : template ? `New ${template.name} network` : "New custom network"}</SheetTitle>
        <SheetDescription>{template?.note || "Where its conversions come from, and what a repeat does."}</SheetDescription>
      </SheetHeader>
      {network && (
        <div className="space-y-2 text-sm">
          <div>Postback URL to paste into the network</div>
          {network.needs_domain && <ErrorText>Postbacks answer only on your own tracking domain: verify one in Domains.</ErrorText>}
          {url ? (
            <>
              <div className="break-all rounded-md border bg-muted/40 p-3 font-mono text-sm" data-testid="postback-url">{url}</div>
              <div className="flex flex-wrap items-center gap-2">
                <CopyButton text={url} label="Copy" />
                <span className="text-xs text-muted-foreground">Shown once: copy it now. It carries a secret, so keep it out of chats and tickets.</span>
              </div>
            </>
          ) : (
            <p className="text-xs text-muted-foreground">The URL is shown only when it is made: it starts {network.token_hint}…. Make a new one if you've lost it.</p>
          )}
          {canManage && <Button type="button" variant="ghost" onClick={() => void rotate()}>Make a new postback URL</Button>}
        </div>
      )}
      <label className="block text-sm">
        Name
        <input aria-label="Name" required className={`${field} mt-1 w-full`} value={name} disabled={!canManage} onChange={(e) => setName(e.target.value)} />
      </label>
      <label className="block text-sm">
        Append the click id to offer URLs as
        <input aria-label="Click id parameter" className={`${field} mt-1 w-full font-mono text-xs`} placeholder="sub1 (empty adds none)" value={clickParam} disabled={!canManage} onChange={(e) => setClickParam(e.target.value)} />
        <span className="mt-1 block text-xs text-muted-foreground">Added to the URLs of this network's offers that don't carry {"{click_id}"}.</span>
      </label>
      <fieldset className="text-sm">
        <legend>A transaction id seen before</legend>
        <div className="mt-1 grid gap-2 sm:grid-cols-2">
          {policies.map((p) => (
            <label key={p.id} className={`flex cursor-pointer gap-2 rounded-md border p-2 ${policy === p.id ? "border-primary" : ""}`}>
              <input type="radio" name="duplicate-policy" checked={policy === p.id} disabled={!canManage} onChange={() => setPolicy(p.id)} />
              <span>
                <b>{p.label}</b>
                <span className="block text-xs text-muted-foreground">{p.help}</span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="text-sm">
          Currency
          <input aria-label="Currency" className={`${field} mt-1 w-full uppercase`} maxLength={3} value={currency} disabled={!canManage} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />
        </label>
        <label className="text-sm">
          Conversions count for
          <span className="mt-1 flex items-center gap-1">
            <input aria-label="Click expiry days" type="number" min={1} max={365} className={`${field} w-20`} value={expiry} disabled={!canManage} onChange={(e) => setExpiry(Math.max(1, Math.min(365, Number(e.target.value) || 1)))} /> days after the click
          </span>
        </label>
      </div>
      <label className="block text-sm">
        Only take postbacks from
        <textarea aria-label="IP allowlist" rows={3} className={`${field} mt-1 w-full font-mono text-xs`} placeholder="Any address. One address or range per line." value={ips} disabled={!canManage} onChange={(e) => setIps(e.target.value)} />
      </label>
      {canManage && (
        <div className="flex flex-wrap gap-2">
          <Button type="submit" variant="primary" disabled={busy}>{network ? "Save" : "Add network"}</Button>
          {network && <Button type="button" variant="ghost" onClick={() => void archive()}>Archive</Button>}
        </div>
      )}
    </form>
  );
}
