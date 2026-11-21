// The tracker's routing editor (plans/ad-tracker-ui.md B2, Destination; B3,
// Flow): path cards with their rules, each with a landers list and an offers
// list with weights and share, Equalise, and suggested weights applied by a
// click. A campaign's link and a flow both edit their routing with it; only
// where it loads from and saves to differs (RoutingBackend).

import { useCallback, useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { pickedBrandId } from "@/lib/brand";
import { NumberInput } from "@/components/number-input";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, ErrorText, field, Loading, toast } from "../ui";

type Offer = Schemas["TrackerOffer"];
type Lander = Schemas["TrackerLander"];
type Suggestions = Schemas["TrackerWeightSuggestions"];
type Condition = Schemas["TrackingPathCondition"];
type ApiDest = Schemas["TrackingLinkDestinations"]["destinations"][number];
type ApiPath = Schemas["TrackingLinkPaths"]["paths"][number];
type DestInput = ReturnType<typeof fromDest>;
type PathInput = { id?: string; name: string; active: boolean; conditions: Condition[]; destinations: DestInput[] };

// RoutingBackend: where one routing loads from and saves to. Each call
// resolves to an API error, or nothing when it went through.
export type RoutingBackend = {
  load: () => Promise<{ error?: unknown; destinations: ApiDest[]; paths: ApiPath[]; needsDomain: boolean; suggestions: Suggestions | null }>;
  save: (destinations: DestInput[], paths: PathInput[]) => Promise<unknown>;
  apply: (weights: { destination_id: string; weight: number }[]) => Promise<unknown>;
};

// A destination as the editor holds it: what PUT takes, with a key for React.
export type Dest = { key: string; id?: string; name: string; url: string; offer_id?: string; lander_id?: string; weight: number; daily_click_cap: number | null };
export type PathCard = { key: string; id?: string; name: string; active: boolean; conditions: Condition[]; destinations: Dest[] };

// The rule fields a path can match, as the engine names them.
export const conditionFields: [Condition["field"], string][] = [
  ["country", "Country"], ["region", "Region"], ["city", "City"], ["language", "Language"],
  ["device", "Device"], ["device_brand", "Device brand"], ["os", "OS"], ["os_version", "OS version"], ["browser", "Browser"], ["browser_version", "Browser version"],
  ["isp", "ISP"], ["asn", "ASN"], ["ip", "IP or range"], ["referrer", "Referrer"],
  ["weekday", "Weekday"], ["hour", "Hour"], ["visitor", "Visitor"],
  ...Array.from({ length: 20 }, (_, i) => [`field${i + 1}`, `Custom field ${i + 1}`] as [Condition["field"], string]),
];
const fieldLabel = Object.fromEntries(conditionFields) as Record<string, string>;

// sentence writes a path's rule as words: "Country is US or CA, and Device is mobile".
export function sentence(conds: Condition[]): string {
  if (conds.length === 0) return "Everyone";
  return conds.map((c) => `${fieldLabel[c.field] ?? c.field} ${c.op === "is_not" ? "is not" : "is"} ${c.values.join(" or ")}`).join(", and ");
}

let seq = 0;
export const key = () => `k${++seq}`;
export const toDest = (d: Schemas["TrackingLinkDestinations"]["destinations"][number]): Dest => ({
  key: key(), id: d.id, name: d.name, url: d.url, offer_id: d.offer_id ?? undefined, lander_id: d.lander_id ?? undefined, weight: d.weight, daily_click_cap: d.daily_click_cap,
});
export const fromDest = (d: Dest) => ({ id: d.id, name: d.name, url: d.offer_id || d.lander_id ? undefined : d.url, offer_id: d.offer_id, lander_id: d.lander_id, weight: d.weight, daily_click_cap: d.daily_click_cap });

// Routing: the path cards of one link or flow, with + Path and Save. confirm
// runs before a save and can stop it (a flow's "used by N links" warning).
export function Routing({ backend, canManage, confirm, onSaved }: { backend: RoutingBackend; canManage: boolean; confirm?: () => Promise<boolean>; onSaved?: () => void }) {
  const brand = pickedBrandId();
  const [offers, setOffers] = useState<Offer[]>([]);
  const [landers, setLanders] = useState<Lander[]>([]);
  const [cards, setCards] = useState<PathCard[] | null>(null);
  const [suggest, setSuggest] = useState<Suggestions | null>(null);
  const [needsDomain, setNeedsDomain] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const [o, l, r] = await Promise.all([
      api.GET("/v1/brands/{brand}/offers", { params: { path: { brand } } }),
      api.GET("/v1/brands/{brand}/landers", { params: { path: { brand } } }),
      backend.load(),
    ]);
    if (r.error) return setError(errorMessage(r.error));
    setOffers((o.data?.data ?? []) as Offer[]);
    setLanders((l.data?.data ?? []) as Lander[]);
    setNeedsDomain(r.needsDomain);
    setSuggest(r.suggestions);
    setCards([
      ...r.paths.map((x) => ({ key: key(), id: x.id, name: x.name, active: x.active, conditions: x.conditions, destinations: x.destinations.map(toDest) })),
      { key: "default", name: "Everyone else", active: true, conditions: [], destinations: r.destinations.map(toDest) },
    ]);
  }, [brand, backend]);
  useEffect(() => void load(), [load]);

  const save = async () => {
    if (!cards) return;
    const rules = cards.filter((c) => c.key !== "default");
    const def = cards.find((c) => c.key === "default")!;
    const bad = rules.find((c) => c.conditions.length === 0 || c.conditions.some((x) => x.values.length === 0) || c.destinations.length === 0);
    if (bad) return toast(`"${bad.name || "A path"}" needs a rule with a value and a destination`, "error");
    if (confirm && !(await confirm())) return;
    const err = await backend.save(
      def.destinations.map(fromDest),
      rules.map((c) => ({ id: c.id, name: c.name, active: c.active, conditions: c.conditions, destinations: c.destinations.map(fromDest) })),
    );
    if (err) return toast(errorMessage(err), "error");
    toast("Destinations saved", "ok");
    onSaved?.();
    void load();
  };
  const apply = async (list: Suggestions["lists"][number]) => {
    const err = await backend.apply(list.items.map((i) => ({ destination_id: i.destination_id, weight: i.suggested_weight })));
    if (err) return toast(errorMessage(err), "error");
    toast("Suggested weights applied", "ok");
    void load();
  };

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!cards) return <Loading />;
  const update = (k: string, patch: Partial<PathCard>) => setCards((cs) => cs!.map((c) => (c.key === k ? { ...c, ...patch } : c)));
  const move = (i: number, d: number) =>
    setCards((cs) => {
      const c = [...cs!];
      [c[i], c[i + d]] = [c[i + d], c[i]];
      return c;
    });
  return (
    <div className="space-y-4">
      {needsDomain && <ErrorText>Tracker routing runs only on your own tracking domain: verify one in Domains, and these paths take effect.</ErrorText>}
      {cards.map((c, i) => (
        <PathEditor
          key={c.key}
          card={c}
          isDefault={c.key === "default"}
          canManage={canManage}
          offers={offers}
          landers={landers}
          suggestions={suggest?.lists.filter((l) => (c.key === "default" ? l.path_id === null : l.path_id === c.id)) ?? []}
          onChange={(patch) => update(c.key, patch)}
          onRemove={() => setCards((cs) => cs!.filter((x) => x.key !== c.key))}
          onUp={i > 0 && c.key !== "default" ? () => move(i, -1) : undefined}
          onDown={i < cards.length - 2 ? () => move(i, 1) : undefined}
          onApply={apply}
        />
      ))}
      {canManage && (
        <div className="flex gap-2">
          <Button
            variant="ghost"
            disabled={cards.length > 10}
            onClick={() => setCards((cs) => [{ key: key(), name: `Path ${cs!.length}`, active: true, conditions: [{ field: "country", op: "is", values: [] }], destinations: [] }, ...cs!])}
          >
            + Path
          </Button>
          <Button variant="primary" onClick={() => void save()}>Save destinations</Button>
        </div>
      )}
    </div>
  );
}

function PathEditor({ card, isDefault, canManage, offers, landers, suggestions, onChange, onRemove, onUp, onDown, onApply }: {
  card: PathCard;
  isDefault: boolean;
  canManage: boolean;
  offers: Offer[];
  landers: Lander[];
  suggestions: Suggestions["lists"];
  onChange: (p: Partial<PathCard>) => void;
  onRemove: () => void;
  onUp?: () => void;
  onDown?: () => void;
  onApply: (l: Suggestions["lists"][number]) => void;
}) {
  const landerRows = card.destinations.filter((d) => d.lander_id);
  const offerRows = card.destinations.filter((d) => !d.lander_id);
  const setDests = (landersNext: Dest[], offersNext: Dest[]) => onChange({ destinations: [...landersNext, ...offersNext] });
  return (
    <Card
      title={isDefault ? "Default path: everyone else" : card.name || "Path"}
      description={isDefault ? "Clicks no path above matches." : sentence(card.conditions)}
      action={
        !isDefault &&
        canManage && (
          <div className="flex items-center gap-1">
            {onUp && <Button variant="ghost" aria-label="Move up" onClick={onUp}>↑</Button>}
            {onDown && <Button variant="ghost" aria-label="Move down" onClick={onDown}>↓</Button>}
            <label className="flex items-center gap-1 text-xs">
              <input type="checkbox" checked={card.active} onChange={(e) => onChange({ active: e.target.checked })} /> On
            </label>
            <Button variant="ghost" aria-label="Remove path" onClick={onRemove}>×</Button>
          </div>
        )
      }
    >
      <div className="space-y-4">
        {!isDefault && (
          <div className="space-y-2">
            <input aria-label="Path name" className={`${field} w-full`} value={card.name} disabled={!canManage} onChange={(e) => onChange({ name: e.target.value })} />
            {card.conditions.map((c, i) => (
              <div key={i} className="flex flex-wrap items-center gap-1 text-sm">
                <select aria-label="Rule field" className={field} value={c.field} disabled={!canManage} onChange={(e) => onChange({ conditions: card.conditions.map((x, j) => (j === i ? { ...x, field: e.target.value as Condition["field"] } : x)) })}>
                  {conditionFields.map(([k, l]) => (
                    <option key={k} value={k}>{l}</option>
                  ))}
                </select>
                <select aria-label="Rule operator" className={field} value={c.op} disabled={!canManage} onChange={(e) => onChange({ conditions: card.conditions.map((x, j) => (j === i ? { ...x, op: e.target.value as Condition["op"] } : x)) })}>
                  <option value="is">is</option>
                  <option value="is_not">is not</option>
                </select>
                <input
                  aria-label="Rule values"
                  className={`${field} min-w-48 flex-1`}
                  placeholder="US, CA"
                  value={c.values.join(", ")}
                  disabled={!canManage}
                  onChange={(e) => onChange({ conditions: card.conditions.map((x, j) => (j === i ? { ...x, values: e.target.value.split(",").map((v) => v.trim()).filter(Boolean) } : x)) })}
                />
                {canManage && card.conditions.length > 1 && <Button variant="ghost" aria-label="Remove rule" onClick={() => onChange({ conditions: card.conditions.filter((_, j) => j !== i) })}>×</Button>}
              </div>
            ))}
            {canManage && <Button variant="ghost" onClick={() => onChange({ conditions: [...card.conditions, { field: "device", op: "is", values: [] }] })}>+ Condition</Button>}
          </div>
        )}
        <WeightList title="Landers" rows={landerRows} canManage={canManage} kind="lander" offers={offers} landers={landers} onChange={(r) => setDests(r, offerRows)} />
        <WeightList title={landerRows.length ? "Offers after the lander" : "Offers and pages"} rows={offerRows} canManage={canManage} kind="offer" landersBefore={landerRows.length > 0} offers={offers} landers={landers} onChange={(r) => setDests(landerRows, r)} />
        {suggestions.map((l) => (
          <SuggestedWeights key={l.kind} list={l} canManage={canManage} onApply={() => onApply(l)} />
        ))}
      </div>
    </Card>
  );
}

// WeightList: one list of a path, each row with its weight, a live share
// bar and its cap; Equalise sets every weight the same.
function WeightList({ title, rows, kind, landersBefore = false, canManage, offers, landers, onChange }: {
  title: string;
  rows: Dest[];
  kind: "lander" | "offer";
  // landersBefore says the list has landers, so these come after one.
  landersBefore?: boolean;
  canManage: boolean;
  offers: Offer[];
  landers: Lander[];
  onChange: (rows: Dest[]) => void;
}) {
  const total = rows.reduce((s, r) => s + r.weight, 0);
  const [adding, setAdding] = useState(false);
  // What a row is, to say when one is in the list twice (it then gets two
  // shares, which is rarely meant).
  const what = (r: Dest) => (kind === "lander" ? r.lander_id : r.offer_id ? `offer:${r.offer_id}` : r.url.trim()) || "";
  const counts = new Map<string, number>();
  for (const r of rows) if (what(r)) counts.set(what(r), (counts.get(what(r)) ?? 0) + 1);
  const room = stageLimit - rows.length;
  const set = (k: string, patch: Partial<Dest>) => onChange(rows.map((r) => (r.key === k ? { ...r, ...patch } : r)));
  const pick = (k: string, v: string) => {
    if (kind === "lander") {
      const l = landers.find((x) => x.id === v);
      return set(k, { lander_id: v, name: l?.name ?? "", url: l?.url ?? "" });
    }
    if (v.startsWith("offer:")) {
      const o = offers.find((x) => x.id === v.slice(6));
      return set(k, { offer_id: o?.id, name: o?.name ?? "", url: o?.url ?? "" });
    }
    set(k, { offer_id: undefined, name: "", url: "" });
  };
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between text-sm font-medium">
        <span>{title}</span>
        {canManage && rows.length > 1 && (
          <button type="button" className="text-xs text-muted-foreground hover:underline" onClick={() => onChange(rows.map((r) => ({ ...r, weight: 100 })))}>
            Equalise
          </button>
        )}
      </div>
      {rows.length === 0 && !(kind === "offer" && landersBefore) && <p className="text-xs text-muted-foreground">{kind === "lander" ? "No landers: clicks go straight to the offers." : "None yet."}</p>}
      {rows.map((r) => {
        const share = total > 0 ? (r.weight / total) * 100 : 0;
        const twice = (counts.get(what(r)) ?? 0) > 1;
        return (
          <div key={r.key} className="grid grid-cols-[1fr_80px_100px_auto] items-center gap-2 text-sm">
            {kind === "lander" ? (
              <select aria-label="Lander" className={field} value={r.lander_id ?? ""} disabled={!canManage} onChange={(e) => pick(r.key, e.target.value)}>
                {!r.lander_id && <option value="">Pick a lander…</option>}
                {landers.map((l) => (
                  <option key={l.id} value={l.id}>{l.name}</option>
                ))}
              </select>
            ) : (
              <span className="flex min-w-0 gap-1">
                <select aria-label="Offer" className={`${field} min-w-0 flex-1`} value={r.offer_id ? `offer:${r.offer_id}` : ""} disabled={!canManage} onChange={(e) => pick(r.key, e.target.value)}>
                  <option value="">A page of your own</option>
                  {offers.map((o) => (
                    <option key={o.id} value={`offer:${o.id}`}>{o.name}</option>
                  ))}
                </select>
                {!r.offer_id && <input aria-label="Page" className={`${field} min-w-0 flex-1`} placeholder="https://" value={r.url} disabled={!canManage} onChange={(e) => set(r.key, { url: e.target.value })} />}
              </span>
            )}
            <NumberInput label="Weight" min={0} max={10000} value={r.weight} disabled={!canManage} onChange={(n) => set(r.key, { weight: n })} />
            <div className="flex items-center gap-1" title={`${percent(share)} of the ${kind === "lander" ? "clicks" : landersBefore ? "lander clicks" : "clicks"}`}>
              <div className="h-1.5 flex-1 rounded bg-muted">
                <div className="h-1.5 rounded bg-primary" style={{ width: `${share}%` }} />
              </div>
              <span className="w-11 text-right text-xs tabular-nums">{percent(share)}</span>
            </div>
            <span className="flex items-center gap-1">
              {twice && <Badge tone="amber" title="It is in this list more than once, so it gets more than one share of the clicks. Remove one, or keep both if you mean it.">Listed twice</Badge>}
              {r.daily_click_cap !== null && <Badge tone="grey" title="Daily click cap">cap {r.daily_click_cap}</Badge>}
              {canManage && <Button variant="ghost" aria-label="Remove" onClick={() => onChange(rows.filter((x) => x.key !== r.key))}>×</Button>}
            </span>
          </div>
        );
      })}
      {kind === "offer" && landersBefore && rows.length === 0 && (
        <p className="text-xs text-amber-600 dark:text-amber-400" data-testid="no-offers-warning">
          No offers after the landers: a lander button that links to /click goes to this link's own page. Add an offer, or leave it if your landers link on by themselves.
        </p>
      )}
      {canManage && room <= 0 && <p className="text-xs text-muted-foreground">A list takes up to {stageLimit} {kind === "lander" ? "landers" : "offers and pages"}.</p>}
      {canManage && room > 0 && !adding && (
        <div className="flex flex-wrap gap-1">
          <Button
            variant="ghost"
            onClick={() => onChange([...rows, kind === "lander" ? { key: key(), name: "", url: "", lander_id: landers[0]?.id, weight: 100, daily_click_cap: null } : { key: key(), name: offers[0]?.name ?? "", url: offers[0]?.url ?? "", offer_id: offers[0]?.id, weight: 100, daily_click_cap: null }])}
          >
            + {kind === "lander" ? "Lander" : "Offer or page"}
          </Button>
          {(kind === "lander" ? landers : offers).length > 1 && (
            <Button variant="ghost" onClick={() => setAdding(true)}>
              + Add several {kind === "lander" ? "landers" : "offers"}
            </Button>
          )}
        </div>
      )}
      {adding && (
        <AddSeveral
          kind={kind}
          items={kind === "lander" ? landers : offers}
          listed={new Set(rows.map((r) => (kind === "lander" ? r.lander_id : r.offer_id)).filter(Boolean) as string[])}
          room={room}
          onCancel={() => setAdding(false)}
          onAdd={(picked) => {
            onChange([
              ...rows,
              ...picked.map((x) => (kind === "lander" ? { key: key(), name: x.name, url: x.url, lander_id: x.id, weight: 100, daily_click_cap: null } : { key: key(), name: x.name, url: x.url, offer_id: x.id, weight: 100, daily_click_cap: null })),
            ]);
            setAdding(false);
          }}
        />
      )}
    </div>
  );
}

// A list takes this many landers, and this many offers and pages: the
// API's own limits.
const stageLimit = 100;

// percent writes a share with one decimal, so 1 of 31 reads 3.2%, not 3%.
const percent = (share: number) => `${share.toFixed(1)}%`;

// AddSeveral: pick many of the brand's landers or offers at once, by search
// or all of them, and add each as a row at weight 100. Ones already in the
// list are left out, so nothing is added twice.
function AddSeveral({ kind, items, listed, room, onAdd, onCancel }: {
  kind: "lander" | "offer";
  items: { id: string; name: string; url: string }[];
  listed: Set<string>;
  room: number;
  onAdd: (picked: { id: string; name: string; url: string }[]) => void;
  onCancel: () => void;
}) {
  const [q, setQ] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const noun = kind === "lander" ? "landers" : "offers";
  const free = items.filter((x) => !listed.has(x.id));
  const words = q.trim().toLowerCase();
  const shown = free.filter((x) => !words || x.name.toLowerCase().includes(words) || x.url.toLowerCase().includes(words));
  const allShown = shown.length > 0 && shown.every((x) => picked.has(x.id));
  const toggle = (id: string) =>
    setPicked((p) => {
      const n = new Set(p);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  const over = picked.size > room;
  return (
    <div className="space-y-2 rounded-md border p-3" data-testid={`add-several-${kind}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-medium">Add {noun}</span>
        <span className="text-xs text-muted-foreground">
          {free.length} not in this list yet{listed.size > 0 ? ` · ${listed.size} already in it` : ""}
        </span>
      </div>
      {free.length === 0 ? (
        <p className="text-xs text-muted-foreground">Every one of the brand's {noun} is already in this list.</p>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <input aria-label={`Search ${noun}`} className={`${field} min-w-48 flex-1`} placeholder={`Search ${free.length} ${noun} by name or page`} value={q} autoFocus onChange={(e) => setQ(e.target.value)} />
            <label className="flex items-center gap-1.5 text-xs">
              <input
                type="checkbox"
                aria-label={`Select all ${noun} shown`}
                checked={allShown}
                disabled={shown.length === 0}
                onChange={() =>
                  setPicked((p) => {
                    const n = new Set(p);
                    for (const x of shown) if (allShown) n.delete(x.id);
                    else n.add(x.id);
                    return n;
                  })
                }
              />
              Select all {shown.length}
            </label>
          </div>
          <ul className="max-h-64 divide-y overflow-y-auto rounded-md border text-sm">
            {shown.length === 0 && <li className="px-2 py-1.5 text-xs text-muted-foreground">Nothing matches "{q}".</li>}
            {shown.map((x) => (
              <li key={x.id}>
                <label className="flex cursor-pointer items-center gap-2 px-2 py-1.5 hover:bg-muted/50">
                  <input type="checkbox" checked={picked.has(x.id)} onChange={() => toggle(x.id)} />
                  <span className="min-w-0 flex-1 truncate">{x.name}</span>
                  <span className="min-w-0 max-w-[50%] truncate text-xs text-muted-foreground">{x.url}</span>
                </label>
              </li>
            ))}
          </ul>
        </>
      )}
      {over && <ErrorText>This list has room for {room} more {noun}; untick {picked.size - room}.</ErrorText>}
      <div className="flex items-center gap-2">
        <Button variant="primary" disabled={picked.size === 0 || over} onClick={() => onAdd(free.filter((x) => picked.has(x.id)))}>
          {picked.size === 0 ? `Add ${noun}` : `Add ${picked.size} ${picked.size === 1 ? noun.slice(0, -1) : noun}`}
        </Button>
        <Button variant="ghost" onClick={onCancel}>Cancel</Button>
        {picked.size > 0 && !over && <span className="text-xs text-muted-foreground">Each is added at weight 100, an equal share.</span>}
      </div>
    </div>
  );
}

// SuggestedWeights: the suggestion as a diff, applied only by a click
// (ClickFlare's test mode, plan T9).
function SuggestedWeights({ list, canManage, onApply }: { list: Suggestions["lists"][number]; canManage: boolean; onApply: () => void }) {
  const what = list.kind === "landers" ? "landers" : "offers and pages";
  if (!list.ready) {
    const why = { too_few_arms: "needs two or more to compare", not_enough_clicks: "needs more clicks on each", not_enough_conversions: "needs more conversions" }[list.reason ?? "too_few_arms"];
    return <p className="text-xs text-muted-foreground">Suggested weights for the {what}: {why}.</p>;
  }
  const changed = list.items.some((i) => i.suggested_weight !== i.weight);
  return (
    <div className="rounded-md border p-2 text-sm" data-testid="suggested-weights">
      <div className="mb-1 flex items-center justify-between">
        <span className="font-medium">Suggested weights for the {what}</span>
        {canManage && changed && <Button variant="primary" onClick={onApply}>Apply</Button>}
      </div>
      <ul className="space-y-0.5 text-xs">
        {list.items.map((i) => (
          <li key={i.destination_id} className="flex justify-between gap-2">
            <span>{i.name || i.url}</span>
            <span className="font-mono tabular-nums">
              {i.weight} → <b>{i.suggested_weight}</b> · {Math.round((i.probability_best ?? 0) * 100)}% best · {i.clicks} clicks
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}
