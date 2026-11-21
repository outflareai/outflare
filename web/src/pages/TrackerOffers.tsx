// Tracker → Offers and Landers (plans/ad-tracker-ui.md B3): the brand's
// offers and landers, each list with today's numbers, a search box, and
// Edit, Duplicate and Delete on every row (Delete for several at once with
// the checkboxes). New or a row opens its details in the page's own space,
// with a Back button to the list, so the side menu stays where it is: an
// offer's network, URL with a token picker, payout
// (fixed, from the postback, by country), cap with a fallback offer, and
// Preview; a lander's URL, calls to action with a click URL each, and the
// script for its page.

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Copy, Pencil, Search, Trash2 } from "lucide-react";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { NumberInput } from "@/components/number-input";
import { CurrencySelect } from "@/components/currency-select";
import { pickedBrandId, useBrand } from "@/lib/brand";
import type { Tab } from "@/lib/paths";
import { api, errorMessage } from "../api";
import { BackButton, Badge, Button, Card, confirmAction, CopyButton, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";

type Offer = Schemas["TrackerOffer"];
type Lander = Schemas["TrackerLander"];

// Why a lander's page failed its check, in plain words.
function landerCheckWords(code: string): string {
  if (code === "http_404" || code === "http_410") return `the page isn't there any more (${code.slice(5)})`;
  if (code.startsWith("http_5")) return `its server answers with an error (${code.slice(5)})`;
  if (code.startsWith("http_")) return `it answers ${code.slice(5)}`;
  const words: Record<string, string> = {
    dns: "its domain can't be found",
    tls: "its HTTPS certificate isn't valid",
    timeout: "it takes over ten seconds to answer",
    redirect_loop: "it redirects round and round",
    bad_url: "its URL isn't a web address",
  };
  return words[code] ?? "it can't be reached";
}

// PageCheck: whether a lander's or an offer's page answers, as Outflare
// last found when it asked (a lander about hourly, an offer every six
// hours, while a link sends to it).
function PageCheck({ p, what }: { p: Pick<Lander, "check_status" | "check_code" | "checked_at" | "destinations"> & { check_enabled?: boolean }; what: "lander" | "offer" }) {
  if (p.check_enabled === false) return <span className="text-xs text-muted-foreground" title="This offer's check is switched off">Not checked</span>;
  if (p.check_status === "broken")
    return <Badge tone="red" title={`The page didn't load in two checks in a row: ${landerCheckWords(p.check_code)}. Clicks sent to it are wasted.`}>Offline</Badge>;
  if (p.check_status === "failing")
    return <Badge tone="amber" title={`Its last check failed: ${landerCheckWords(p.check_code)}. It is asked again within ten minutes.`}>Checking again</Badge>;
  if (p.check_status === "refused")
    return <Badge tone="grey" title={`The page turns automated requests away (${p.check_code.slice(5)}), so Outflare can't tell whether it answers.`}>Can't be checked</Badge>;
  if (p.check_status === "ok")
    return (
      <span className="inline-flex items-center gap-1.5 text-xs" title={`Outflare opened the page and it loaded${p.checked_at ? `, last checked ${new Date(p.checked_at).toLocaleString()}` : ""}.`}>
        <span aria-hidden className="size-2 rounded-full bg-emerald-500" />Online
      </span>
    );
  return <span className="text-xs text-muted-foreground" title={p.destinations === 0 ? `Only ${what}s a link sends clicks to are checked` : "Its first check runs within a few minutes"}>{p.destinations === 0 ? "Not checked" : "Checking soon"}</span>;
}
type Network = Schemas["PostbackSource"];

// item and go give each offer or lander its own address (the app passes
// them; a page mounted without them keeps what is open to itself).
type Props = { ws: string; canManage: boolean; item?: string; go?: (tab: Tab, item?: string) => void };

// The tokens an offer's URL can carry (docs/campaign-links.md, "Tokens in a
// destination"), as chips that insert at the cursor.
export const offerTokens: [string, string][] = [
  ["{click_id}", "Click id"], ["{source_click_id}", "Network click id"], ["{cost}", "Cost"],
  ["{field1}", "Field 1"], ["{field2}", "Field 2"], ["{field3}", "Field 3"],
  ["{campaign}", "Campaign"], ["{source}", "Source"], ["{country}", "Country"], ["{device}", "Device"], ["{os}", "OS"], ["{language}", "Language"],
];

// preview fills every token with a test value, so the offer opens as a
// click would open it.
export function preview(url: string): string {
  return url.replace(/\{([a-z0-9_]+)(\|[^}]*)?\}/g, (_, name: string) => (name === "click_id" ? "test-click-id" : name === "country" ? "US" : `test-${name}`));
}

type Preview = Schemas["DestinationPreview"];

const previewSource: Record<Preview["parameters"][number]["source"], string> = {
  click: "the click's id, added by Outflare",
  campaign: "from the campaign",
  link: "from the link",
  token: "your parameter, filled in for the click",
  url: "as you wrote it",
};

// EffectiveURL asks the server where a click would go for the URL as
// typed, built by the redirect's own code for a made-up click. Nothing is
// counted.
export function EffectiveURL({ url }: { url: string }) {
  const [shown, setShown] = useState<{ url: string; preview?: Preview; error?: string } | null>(null);
  const show = async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/destination-preview", { params: { path: { brand: pickedBrandId() }, query: { destination: url } } });
    setShown(error || !data ? { url, error: errorMessage(error) } : { url, preview: data });
  };
  const current = shown?.url === url ? shown : null;
  return (
    <div className="mt-1 text-xs">
      <button type="button" className="text-muted-foreground hover:underline" onClick={() => void show()}>Show what a click gets</button>
      {current?.error && <ErrorText>{current.error}</ErrorText>}
      {current?.preview && (
        <div className="mt-2 space-y-2 rounded-md border p-2" data-testid="effective-url">
          <div className="break-all font-mono">{current.preview.effective_url}</div>
          <ul className="space-y-0.5 text-muted-foreground">
            {current.preview.parameters.map((p, i) => (
              <li key={i}><span className="font-mono text-foreground">{p.name}</span>: {previewSource[p.source]}</li>
            ))}
          </ul>
          {current.preview.replaced.length > 0 && <div className="text-muted-foreground">Outflare sets {current.preview.replaced.join(", ")} itself, so the values written in the URL are dropped.</div>}
          <div className="text-muted-foreground">A sample click, with no campaign: a real one also carries its campaign's UTM tags and ofc. Nothing is counted.</div>
        </div>
      )}
    </div>
  );
}

// What a list keeps open: nothing, a new row (blank, or a copy of one), or
// one row's editor.
type Open<T> = null | { new: T | null } | { edit: T };

// useSelection: the rows ticked for an action on several at once.
function useSelection(ids: string[]) {
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const shown = ids.filter((id) => picked.has(id));
  return {
    picked: shown,
    has: (id: string) => picked.has(id),
    all: ids.length > 0 && shown.length === ids.length,
    toggle: (id: string) => setPicked((was) => { const next = new Set(was); if (!next.delete(id)) next.add(id); return next; }),
    toggleAll: () => setPicked(shown.length === ids.length ? new Set() : new Set(ids)),
    clear: () => setPicked(new Set()),
  };
}

// deleteItems asks once, then deletes the offers or landers: each comes off
// the links that use it and leaves the list. Their reports keep their numbers.
async function deleteItems(kind: "offer" | "lander", items: { id: string; name: string; destinations: number }[]): Promise<boolean> {
  if (items.length === 0) return false;
  const used = items.filter((i) => i.destinations > 0).length;
  const title = items.length === 1 ? `Delete ${items[0].name}?` : `Delete ${items.length} ${kind}s?`;
  const body = (
    <>
      {items.length === 1 ? "It leaves" : "They leave"} the list and can't be picked for new destinations. Reports keep {items.length === 1 ? "its" : "their"} numbers.
      {used > 0 && (
        <span className="mt-2 block text-amber-700 dark:text-amber-400">
          {items.length === 1 ? "A link sends clicks to it now" : `Links send clicks to ${used} of them now`}: deleting takes {items.length === 1 ? "it" : "them"} off those links, and the other {kind}s there share the clicks.
        </span>
      )}
    </>
  );
  if (!(await confirmAction(title, body, "Delete"))) return false;
  const brand = pickedBrandId();
  let failed = "";
  for (const i of items) {
    const { error } = kind === "offer"
      ? await api.DELETE("/v1/brands/{brand}/offers/{id}", { params: { path: { brand, id: i.id } } })
      : await api.DELETE("/v1/brands/{brand}/landers/{id}", { params: { path: { brand, id: i.id } } });
    if (error) failed = errorMessage(error);
  }
  if (failed) toast(failed, "error");
  if (!failed) toast(items.length === 1 ? `${items[0].name} deleted` : `${items.length} ${kind}s deleted`, "ok");
  return true;
}

// ListTools: the search box, and Delete for the ticked rows.
function ListTools({ kind, query, onQuery, picked, onDelete, onClear }: { kind: "offer" | "lander"; query: string; onQuery: (q: string) => void; picked: number; onDelete: () => void; onClear: () => void }) {
  return (
    <div className="mb-2 flex flex-wrap items-center gap-2">
      <div className="relative">
        <Search aria-hidden className="pointer-events-none absolute left-2 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <input aria-label={`Search ${kind}s`} className={`${field} w-64 pl-8`} placeholder={`Search ${kind}s`} value={query} onChange={(e) => onQuery(e.target.value)} />
      </div>
      {picked > 0 && (
        <div className="flex items-center gap-2 text-sm" data-testid="selection-bar">
          <span className="text-muted-foreground">{picked} selected</span>
          <Button type="button" variant="danger" onClick={onDelete}><Trash2 aria-hidden className="mr-1 inline size-4" />Delete {picked}</Button>
          <Button type="button" variant="ghost" onClick={onClear}>Clear</Button>
        </div>
      )}
    </div>
  );
}

// Details: one offer or lander in the page's own space, in place of the
// list, with a Back button that says where it goes.
function Details({ list, title, description, onBack, actions, children }: { list: string; title: string; description: string; onBack: () => void; actions?: ReactNode; children: ReactNode }) {
  return (
    <div data-testid="details">
      <BackButton to={list} onClick={onBack} />
      <PageHeader title={title} description={description} action={actions} />
      <div className="mt-4 rounded-lg border bg-card">{children}</div>
    </div>
  );
}

// useOpen is what is open in place of the list: nothing, a new item (from a
// copy or from scratch) or a saved one. With the app's item and go it lives
// in the address ("new", "copy:<id>" or the item's id), so each offer and
// lander has a link of its own and Back in the browser returns to the list.
function useOpen<T extends { id: string; name: string }>(tab: Tab, list: T[] | null, item: string | undefined, go: Props["go"]): [Open<T>, (o: Open<T>) => void] {
  const [local, setLocal] = useState<Open<T>>(null);
  const find = (id: string) => (list ?? []).find((x) => x.id === id) ?? null;
  let open: Open<T> = local;
  if (go) {
    const saved = item && item !== "new" && !item.startsWith("copy:") ? find(item) : null;
    open = !item ? null : item === "new" ? { new: null } : item.startsWith("copy:") ? { new: find(item.slice(5)) } : saved ? { edit: saved } : null;
  }
  const name = open && "edit" in open ? open.edit.name : "";
  useEffect(() => {
    // The breadcrumb shows the name, not the id.
    if (go && item && name) window.dispatchEvent(new CustomEvent("outflare:item-name", { detail: { id: item, name } }));
  }, [go, item, name]);
  const set = (o: Open<T>) => {
    if (!go) return setLocal(o);
    go(tab, o === null ? undefined : "edit" in o ? o.edit.id : o.new ? `copy:${o.new.id}` : "new");
  };
  return [open, set];
}

// RowActions: Edit, Duplicate and Delete, on every row.
function RowActions({ name, onEdit, onDuplicate, onDelete }: { name: string; onEdit: () => void; onDuplicate: () => void; onDelete: () => void }) {
  const act = (label: string, icon: ReactNode, run: () => void, danger = false) => (
    <button type="button" aria-label={`${label} ${name}`} title={label} className={`inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs hover:bg-muted ${danger ? "text-red-600 dark:text-red-400" : ""}`} onClick={(e) => { e.stopPropagation(); run(); }}>
      {icon}{label}
    </button>
  );
  return (
    <div className="flex items-center justify-end gap-1 whitespace-nowrap">
      {act("Edit", <Pencil aria-hidden className="size-3.5" />, onEdit)}
      {act("Duplicate", <Copy aria-hidden className="size-3.5" />, onDuplicate)}
      {act("Delete", <Trash2 aria-hidden className="size-3.5" />, onDelete, true)}
    </div>
  );
}

export function TrackerOffers({ ws, canManage, item, go }: Props) {
  const { brand, active } = useBrand(ws);
  const [offers, setOffers] = useState<Offer[] | null>(null);
  const [networks, setNetworks] = useState<Network[]>([]);
  const [error, setError] = useState("");
  const [open, setOpen] = useOpen<Offer>("tracker-offers", offers, item, go);
  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();
  const shown = (offers ?? []).filter((o) => !q || o.name.toLowerCase().includes(q) || o.url.toLowerCase().includes(q));
  const sel = useSelection(shown.map((o) => o.id));
  const load = useCallback(async () => {
    const b = pickedBrandId();
    const [o, n] = await Promise.all([
      api.GET("/v1/brands/{brand}/offers", { params: { path: { brand: b } } }),
      api.GET("/v1/brands/{brand}/postback-sources", { params: { path: { brand: b } } }),
    ]);
    if (o.error) return setError(errorMessage(o.error));
    setOffers((o.data?.data ?? []) as Offer[]);
    setNetworks((n.data?.data ?? []) as Network[]);
  }, []);
  useEffect(() => {
    if (brand) void load();
  }, [brand, load]);
  if (!brand) return <PickBrand what="offers" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!offers) return <Loading />;
  const network = (id: string | null) => networks.find((n) => n.id === id)?.name ?? "—";
  const remove = async (items: Offer[]) => {
    if (!(await deleteItems("offer", items))) return;
    sel.clear();
    setOpen(null);
    void load();
  };
  const editor = (offer: Offer | null, from: Offer | null) => (
    <OfferForm key={offer?.id ?? `new:${from?.id ?? ""}`} offer={offer} from={from} offers={offers} networks={networks} canManage={canManage} onCancel={() => setOpen(null)} onSaved={() => { setOpen(null); void load(); }} />
  );
  if (open) {
    const o = "edit" in open ? open.edit : null;
    const from = "new" in open ? open.new : null;
    return (
      <Details
        list="offers"
        title={o ? o.name : from ? `Copy of ${from.name}` : "New offer"}
        description="Where its clicks go, what it pays and how many it takes a day."
        onBack={() => setOpen(null)}
        actions={o && canManage && (
          <div className="flex gap-2">
            <Button variant="ghost" onClick={() => setOpen({ new: o })}><Copy aria-hidden className="mr-1 inline size-4" />Duplicate</Button>
            <Button variant="danger" onClick={() => void remove([o])}><Trash2 aria-hidden className="mr-1 inline size-4" />Delete</Button>
          </div>
        )}
      >
        {editor(o, from)}
      </Details>
    );
  }
  return (
    <div className="space-y-4">
      <PageHeader
        title="Offers"
        description="The offers your campaigns send clicks to: where each one pays through, what it pays, and its daily cap."
        action={canManage && <Button variant="primary" onClick={() => setOpen({ new: null })}>New offer</Button>}
      />
      {offers.length === 0 ? (
        <Empty title="No offers yet">An offer is a page an affiliate network pays you for: add it once, then send any campaign's clicks to it.</Empty>
      ) : (
        <Card title={`${offers.length} offer${offers.length === 1 ? "" : "s"}`}>
          <ListTools kind="offer" query={query} onQuery={setQuery} picked={sel.picked.length} onDelete={() => void remove(shown.filter((o) => sel.has(o.id)))} onClear={sel.clear} />
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  {canManage && <th className="w-8 py-2 pr-2"><input type="checkbox" aria-label="Select all offers" checked={sel.all} onChange={sel.toggleAll} /></th>}
                  <th className="py-2 pr-3 font-medium">Offer</th>
                  <th className="px-3 py-2 font-medium">Network</th>
                  <th className="px-3 py-2 text-right font-medium">Payout</th>
                  <th className="px-3 py-2 text-right font-medium" title="Clicks sent to the offer today, as its daily cap counts them: every press of a lander button counts, suspect clicks don't. The Report counts visits instead.">Clicks today <span aria-hidden className="text-muted-foreground">ⓘ</span></th>
                  <th className="px-3 py-2 text-right font-medium" title="Conversions the offer took today, as its daily cap counts them. One declined later still counts here; the Report leaves declined ones out.">Conversions today <span aria-hidden className="text-muted-foreground">ⓘ</span></th>
                  <th className="px-3 py-2 text-right font-medium" title="How many campaign routing lists send clicks to it now">In use <span aria-hidden className="text-muted-foreground">ⓘ</span></th>
                  <th className="px-3 py-2 text-right font-medium" title="Whether its web page loaded when Outflare last checked it">Page status <span aria-hidden className="text-muted-foreground">ⓘ</span></th>
                  {canManage && <th className="py-2 pl-3 text-right font-medium">Actions</th>}
                </tr>
              </thead>
              <tbody>
                {shown.length === 0 && <tr className="border-t"><td colSpan={9} className="py-6 text-center text-muted-foreground">No offer matches "{query}".</td></tr>}
                {shown.map((o) => (
                  <tr key={o.id} className="cursor-pointer border-t hover:bg-muted/40" onClick={() => setOpen({ edit: o })}>
                    {canManage && <td className="py-2 pr-2" onClick={(e) => e.stopPropagation()}><input type="checkbox" aria-label={`Select ${o.name}`} checked={sel.has(o.id)} onChange={() => sel.toggle(o.id)} /></td>}
                    <td className="py-2 pr-3">
                      <span className="font-medium">{o.name}</span>
                      {o.missing_click_id && <Badge tone="amber" title="This page isn't the brand's own and its URL passes no {click_id} on, so a postback can't name the click. Add {click_id} in the network's parameter, or change Click id delivery.">Missing click id</Badge>}
                      {o.cap_reached && <Badge tone="red">Capped</Badge>}
                    </td>
                    <td className="px-3 py-2">{network(o.source_id)}</td>
                    <td className="px-3 py-2 text-right font-mono tabular-nums">
                      {o.payout} {o.currency}
                      {Object.keys(o.country_payouts).length > 0 && <span className="text-xs text-muted-foreground"> +{Object.keys(o.country_payouts).length} countries</span>}
                    </td>
                    <td className="px-3 py-2 text-right font-mono tabular-nums">{o.clicks_today.toLocaleString()}</td>
                    <td className="px-3 py-2 text-right font-mono tabular-nums">{o.conversions_today.toLocaleString()}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{o.destinations === 0 ? <span className="text-muted-foreground">Not in use</span> : `${o.destinations} place${o.destinations === 1 ? "" : "s"}`}</td>
                    <td className="whitespace-nowrap px-3 py-2 text-right"><PageCheck p={o} what="offer" /></td>
                    {canManage && <td className="py-2 pl-3"><RowActions name={o.name} onEdit={() => setOpen({ edit: o })} onDuplicate={() => setOpen({ new: o })} onDelete={() => void remove([o])} /></td>}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  );
}

function OfferForm({ offer: saved, from, offers, networks, canManage, onSaved, onCancel }: { offer: Offer | null; from: Offer | null; offers: Offer[]; networks: Network[]; canManage: boolean; onSaved: () => void; onCancel: () => void }) {
  // A copy starts from another offer's settings, under a name of its own.
  const offer = saved ?? from;
  const [name, setName] = useState(saved?.name ?? (from ? `${from.name} (copy)` : ""));
  const [url, setUrl] = useState(offer?.url ?? "");
  const [network, setNetwork] = useState(offer?.source_id ?? "");
  const [mode, setMode] = useState<Offer["payout_mode"]>(offer?.payout_mode ?? "postback");
  const [payout, setPayout] = useState(offer?.payout ?? "0");
  const [currency, setCurrency] = useState(offer?.currency ?? "USD");
  const [countries, setCountries] = useState<[string, string][]>(Object.entries(offer?.country_payouts ?? {}));
  const [type, setType] = useState(offer?.conversion_type ?? "");
  const [cap, setCap] = useState(offer?.daily_cap == null ? "" : String(offer.daily_cap));
  const [capKind, setCapKind] = useState<Offer["cap_kind"]>(offer?.cap_kind ?? "conversions");
  const [fallback, setFallback] = useState(offer?.fallback_offer_id ?? "");
  const [check, setCheck] = useState(offer?.check_enabled ?? true);
  const [delivery, setDelivery] = useState<Offer["click_id_delivery"]>(offer?.click_id_delivery ?? "auto");
  const [busy, setBusy] = useState(false);
  const urlRef = useRef<HTMLInputElement>(null);
  const insert = (token: string) => {
    const el = urlRef.current;
    const at = el?.selectionStart ?? url.length;
    const end = el?.selectionEnd ?? at;
    setUrl(url.slice(0, at) + token + url.slice(end));
    requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(at + token.length, at + token.length);
    });
  };
  // The network's own click id parameter counts too; the engine says so on
  // a saved offer, and a new one is checked for the token itself.
  // The brand's own domains count as well, which only the engine knows: an
  // offer not yet saved, or being edited, is warned about only when it is
  // set to Affiliate.
  const unchanged = saved && saved.url === url && saved.click_id_delivery === delivery && (saved.source_id ?? "") === network;
  const missing = unchanged ? saved.missing_click_id : delivery === "affiliate" && !url.includes("{click_id}") && !network;
  const save = async () => {
    setBusy(true);
    const body = {
      name: name.trim(),
      url: url.trim(),
      source_id: network,
      payout_mode: mode,
      payout,
      currency,
      country_payouts: Object.fromEntries(countries.filter(([c, p]) => c.trim() && p.trim()).map(([c, p]) => [c.trim().toUpperCase(), p.trim()])),
      conversion_type: type.trim(),
      daily_cap: cap.trim() === "" ? null : Math.max(0, Number(cap) || 0),
      cap_kind: capKind,
      fallback_offer_id: fallback,
      check_enabled: check,
      click_id_delivery: delivery,
    };
    const brand = pickedBrandId();
    const r = saved
      ? await api.PATCH("/v1/brands/{brand}/offers/{id}", { params: { path: { brand, id: saved.id } }, body })
      : await api.POST("/v1/brands/{brand}/offers", { params: { path: { brand } }, body });
    setBusy(false);
    if (r.error) return toast(errorMessage(r.error), "error");
    toast(saved ? "Offer saved" : "Offer added", "ok");
    onSaved();
  };
  return (
    <form className="grid items-start gap-x-8 gap-y-4 p-4 lg:grid-cols-2" onSubmit={(e) => { e.preventDefault(); void save(); }}>
      <label className="block text-sm">
        Affiliate network
        <select aria-label="Affiliate network" className={`${field} mt-1 w-full`} value={network} disabled={!canManage} onChange={(e) => setNetwork(e.target.value)}>
          <option value="">None</option>
          {/* An archived network isn't offered; the offer's own stays, so opening the form never drops it. */}
          {networks.filter((n) => n.status !== "archived" || n.id === network).map((n) => (
            <option key={n.id} value={n.id}>{n.name}{n.status === "archived" ? " (archived)" : ""}</option>
          ))}
        </select>
      </label>
      <label className="block text-sm">
        Name
        <input aria-label="Name" required className={`${field} mt-1 w-full`} value={name} disabled={!canManage} onChange={(e) => setName(e.target.value)} />
      </label>
      <div className="text-sm">
        <label htmlFor="offer-url">Offer URL</label>
        <input id="offer-url" aria-label="Offer URL" required ref={urlRef} className={`${field} mt-1 w-full font-mono text-xs`} placeholder="https://network.example/offer?sub1={click_id}" value={url} disabled={!canManage} onChange={(e) => setUrl(e.target.value)} />
        {canManage && (
          <div className="mt-1 flex flex-wrap gap-1" aria-label="Tokens">
            {offerTokens.map(([t, label]) => (
              <button key={t} type="button" title={t} className="rounded-full border px-2 py-0.5 text-xs hover:bg-muted" onClick={() => insert(t)}>
                {label}
              </button>
            ))}
          </div>
        )}
        <p className="mt-1 text-xs text-muted-foreground">
          Outflare adds the campaign's UTM tags, ofc and the click id (ofclk) itself, once. Leave them out of the URL. For a network, put {"{click_id}"} in its own parameter, such as subid={"{click_id}"}.
        </p>
        {missing && url && <p className="mt-1 text-xs text-amber-700 dark:text-amber-400">Missing click id: add {"{click_id}"} where the network takes a sub id, or its postback can't name the click.</p>}
        {url && (
          <button type="button" className="mt-1 text-xs text-muted-foreground hover:underline" onClick={() => window.open(preview(url), "_blank", "noopener")}>
            Preview with test values ↗
          </button>
        )}
        {url && <EffectiveURL url={url} />}
      </div>
      <label className="block text-sm">
        Click id delivery
        <select aria-label="Click id delivery" className={`${field} mt-1 w-full`} value={delivery} disabled={!canManage} onChange={(e) => setDelivery(e.target.value as Offer["click_id_delivery"])}>
          <option value="auto">Automatic (work it out from the URL)</option>
          <option value="first_party">Our own page (reads the ofclk Outflare adds)</option>
          <option value="affiliate">Affiliate network (the URL passes {"{click_id}"} on)</option>
          <option value="unattributed">No conversions expected</option>
        </select>
        <span className="mt-1 block text-xs text-muted-foreground">
          {delivery === "first_party" && "Nothing to add: every redirect puts the click id on the page as ofclk."}
          {delivery === "affiliate" && "The network must get the click id in its own parameter, or its postback can't name the click."}
          {delivery === "unattributed" && "Outflare won't warn about a missing click id on this offer."}
          {delivery === "auto" && "A page on one of the brand's domains is treated as your own; any other page needs {click_id} in its URL or a network that adds it."}
        </span>
      </label>
      <fieldset className="space-y-2 text-sm">
        <legend>Payout</legend>
        <div className="flex flex-wrap gap-3">
          <label className="flex items-center gap-1"><input type="radio" name="payout-mode" checked={mode === "postback"} disabled={!canManage} onChange={() => setMode("postback")} /> From the postback</label>
          <label className="flex items-center gap-1"><input type="radio" name="payout-mode" checked={mode === "fixed"} disabled={!canManage} onChange={() => setMode("fixed")} /> Fixed</label>
        </div>
        <div className="flex gap-2">
          <input aria-label="Payout" inputMode="decimal" className={`${field} w-28`} value={payout} disabled={!canManage} onChange={(e) => setPayout(e.target.value)} />
          <CurrencySelect value={currency} disabled={!canManage} onChange={setCurrency} />
        </div>
        <p className="text-xs text-muted-foreground">{mode === "postback" ? "What the postback says; this amount when it says none." : "This amount, whatever the postback says."}</p>
        {countries.map(([c, p], i) => (
          <div key={i} className="flex items-center gap-2">
            <input aria-label="Country" className={`${field} w-16 uppercase`} maxLength={2} placeholder="US" value={c} disabled={!canManage} onChange={(e) => setCountries(countries.map((x, j) => (j === i ? [e.target.value.toUpperCase(), x[1]] : x)))} />
            <input aria-label="Country payout" inputMode="decimal" className={`${field} w-28`} value={p} disabled={!canManage} onChange={(e) => setCountries(countries.map((x, j) => (j === i ? [x[0], e.target.value] : x)))} />
            {canManage && <Button type="button" variant="ghost" aria-label="Remove country" onClick={() => setCountries(countries.filter((_, j) => j !== i))}>×</Button>}
          </div>
        ))}
        {canManage && <Button type="button" variant="ghost" onClick={() => setCountries([...countries, ["", ""]])}>+ Payout by country</Button>}
      </fieldset>
      <label className="block text-sm">
        Conversion type
        <input aria-label="Conversion type" className={`${field} mt-1 w-full`} placeholder="conversion" value={type} disabled={!canManage} onChange={(e) => setType(e.target.value)} />
      </label>
      <fieldset className="space-y-2 text-sm">
        <legend>Daily cap</legend>
        <div className="flex gap-2">
          <input aria-label="Daily cap" inputMode="numeric" className={`${field} w-28`} placeholder="No cap" value={cap} disabled={!canManage} onChange={(e) => setCap(e.target.value.replace(/\D/g, ""))} />
          <select aria-label="Cap counts" className={field} value={capKind} disabled={!canManage} onChange={(e) => setCapKind(e.target.value as Offer["cap_kind"])}>
            <option value="conversions">conversions a day</option>
            <option value="clicks">clicks a day</option>
          </select>
        </div>
        <label className="block">
          While capped, send clicks to
          <select aria-label="Fallback offer" className={`${field} mt-1 w-full`} value={fallback} disabled={!canManage} onChange={(e) => setFallback(e.target.value)}>
            <option value="">The path's other offers and pages</option>
            {offers.filter((o) => o.id !== saved?.id).map((o) => (
              <option key={o.id} value={o.id}>{o.name}</option>
            ))}
          </select>
        </label>
      </fieldset>
      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-0.5" checked={check} disabled={!canManage} onChange={(e) => setCheck(e.target.checked)} />
        <span>
          Check that this offer still answers
          <span className="block text-xs text-muted-foreground">
            Outflare asks for the offer's page every six hours while a link sends to it, and alerts you when it fails twice in a row. Its network counts each request as a click with no visitor behind it; switch this off if the network minds.
          </span>
        </span>
      </label>
      {saved && (
        <div className="text-sm">
          Tracking URL
          <div className="mt-1 flex items-start gap-2">
            <div className="min-w-0 flex-1 break-all rounded-md border bg-muted/40 p-2 font-mono text-xs">{saved.tracking_url}</div>
            <CopyButton text={saved.tracking_url} label="Copy" />
          </div>
        </div>
      )}
      {canManage && (
        <div className="flex flex-wrap gap-2 lg:col-span-2">
          <Button type="submit" variant="primary" disabled={busy}>{saved ? "Save" : "Add offer"}</Button>
          <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
        </div>
      )}
    </form>
  );
}

export function TrackerLanders({ ws, canManage, item, go }: Props) {
  const { brand, active } = useBrand(ws);
  const [landers, setLanders] = useState<Lander[] | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useOpen<Lander>("tracker-landers", landers, item, go);
  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();
  const shown = (landers ?? []).filter((l) => !q || l.name.toLowerCase().includes(q) || l.url.toLowerCase().includes(q));
  const sel = useSelection(shown.map((l) => l.id));
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/landers", { params: { path: { brand: pickedBrandId() } } });
    if (error) return setError(errorMessage(error));
    const list = (data?.data ?? []) as Lander[];
    setLanders(list);
    return list;
  }, []);
  useEffect(() => {
    if (brand) void load();
  }, [brand, load]);
  if (!brand) return <PickBrand what="landers" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!landers) return <Loading />;
  const remove = async (items: Lander[]) => {
    if (!(await deleteItems("lander", items))) return;
    sel.clear();
    setOpen(null);
    void load();
  };
  const editor = (lander: Lander | null, from: Lander | null) => (
    <LanderForm
      key={lander?.id ?? `new:${from?.id ?? ""}`}
      lander={lander}
      from={from}
      canManage={canManage}
      onCancel={() => setOpen(null)}
      onSaved={async (id, close) => {
        const list = await load();
        if (close) return setOpen(null);
        // A new lander stays open, so its click URLs and script can be
        // copied.
        const made = list?.find((l) => l.id === id);
        setOpen(made ? { edit: made } : null);
      }}
    />
  );
  if (open) {
    const l = "edit" in open ? open.edit : null;
    const from = "new" in open ? open.new : null;
    return (
      <Details
        list="landers"
        title={l ? l.name : from ? `Copy of ${from.name}` : "New lander"}
        description="Its page, and where each call to action on it links."
        onBack={() => setOpen(null)}
        actions={l && canManage && (
          <div className="flex gap-2">
            <Button variant="ghost" onClick={() => setOpen({ new: l })}><Copy aria-hidden className="mr-1 inline size-4" />Duplicate</Button>
            <Button variant="danger" onClick={() => void remove([l])}><Trash2 aria-hidden className="mr-1 inline size-4" />Delete</Button>
          </div>
        )}
      >
        {editor(l, from)}
      </Details>
    );
  }
  return (
    <div className="space-y-4">
      <PageHeader
        title="Landers"
        description="Pages of your own that clicks see before the offer: each call to action has its own click URL."
        action={canManage && <Button variant="primary" onClick={() => setOpen({ new: null })}>New lander</Button>}
      />
      {landers.length === 0 ? (
        <Empty title="No landers yet">A lander is a page before the offer, such as a quiz or a review. Add it, put its script on the page, and link its buttons to its click URLs.</Empty>
      ) : (
        <Card title={`${landers.length} lander${landers.length === 1 ? "" : "s"}`}>
          <ListTools kind="lander" query={query} onQuery={setQuery} picked={sel.picked.length} onDelete={() => void remove(shown.filter((l) => sel.has(l.id)))} onClear={sel.clear} />
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  {canManage && <th className="w-8 py-2 pr-2"><input type="checkbox" aria-label="Select all landers" checked={sel.all} onChange={sel.toggleAll} /></th>}
                  <th className="py-2 pr-3 font-medium">Lander</th>
                  <th className="px-3 py-2 text-right font-medium">Calls to action</th>
                  <th className="px-3 py-2 text-right font-medium" title="Presses of the lander's buttons today, repeat presses included. The Report's Lander clicks counts one per visit.">CTA clicks today <span aria-hidden className="text-muted-foreground">ⓘ</span></th>
                  <th className="px-3 py-2 text-right font-medium" title="How many campaign routing lists send clicks to it now">In use <span aria-hidden className="text-muted-foreground">ⓘ</span></th>
                  <th className="px-3 py-2 text-right font-medium" title="Whether its web page loaded when Outflare last checked it">Page status <span aria-hidden className="text-muted-foreground">ⓘ</span></th>
                  {canManage && <th className="py-2 pl-3 text-right font-medium">Actions</th>}
                </tr>
              </thead>
              <tbody>
                {shown.length === 0 && <tr className="border-t"><td colSpan={7} className="py-6 text-center text-muted-foreground">No lander matches "{query}".</td></tr>}
                {shown.map((l) => (
                  <tr key={l.id} className="cursor-pointer border-t hover:bg-muted/40" onClick={() => setOpen({ edit: l })}>
                    {canManage && <td className="py-2 pr-2" onClick={(e) => e.stopPropagation()}><input type="checkbox" aria-label={`Select ${l.name}`} checked={sel.has(l.id)} onChange={() => sel.toggle(l.id)} /></td>}
                    <td className="py-2 pr-3">
                      <span className="font-medium">{l.name}</span>
                      <span className="block truncate text-xs text-muted-foreground">{l.url}</span>
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums">{l.ctas}</td>
                    <td className="px-3 py-2 text-right font-mono tabular-nums">{l.lander_clicks_today.toLocaleString()}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{l.destinations === 0 ? <span className="text-muted-foreground">Not in use</span> : `${l.destinations} place${l.destinations === 1 ? "" : "s"}`}</td>
                    <td className="whitespace-nowrap px-3 py-2 text-right"><PageCheck p={l} what="lander" /></td>
                    {canManage && <td className="py-2 pl-3"><RowActions name={l.name} onEdit={() => setOpen({ edit: l })} onDuplicate={() => setOpen({ new: l })} onDelete={() => void remove([l])} /></td>}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  );
}

function LanderForm({ lander, from, canManage, onSaved, onCancel }: { lander: Lander | null; from: Lander | null; canManage: boolean; onSaved: (id: string, close: boolean) => void; onCancel: () => void }) {
  const [name, setName] = useState(lander?.name ?? (from ? `${from.name} (copy)` : ""));
  const [url, setUrl] = useState((lander ?? from)?.url ?? "");
  const [ctas, setCtas] = useState((lander ?? from)?.ctas ?? 1);
  const [busy, setBusy] = useState(false);
  const save = async () => {
    setBusy(true);
    const brand = pickedBrandId();
    const body = { name: name.trim(), url: url.trim(), ctas };
    const r = lander
      ? await api.PATCH("/v1/brands/{brand}/landers/{id}", { params: { path: { brand, id: lander.id } }, body })
      : await api.POST("/v1/brands/{brand}/landers", { params: { path: { brand } }, body });
    setBusy(false);
    if (r.error) return toast(errorMessage(r.error), "error");
    toast(lander ? "Lander saved" : "Lander added: put its script on the page", "ok");
    // A new lander stays open, so its click URLs and script can be copied.
    onSaved((r.data as Lander).id, !!lander);
  };
  return (
    <form className="grid items-start gap-x-8 gap-y-4 p-4 lg:grid-cols-2" onSubmit={(e) => { e.preventDefault(); void save(); }}>
      <div className="space-y-4">
      <label className="block text-sm">
        Name
        <input aria-label="Name" required className={`${field} mt-1 w-full`} value={name} disabled={!canManage} onChange={(e) => setName(e.target.value)} />
      </label>
      <label className="block text-sm">
        Lander URL
        <input aria-label="Lander URL" required className={`${field} mt-1 w-full font-mono text-xs`} placeholder="https://acme.example/quiz" value={url} disabled={!canManage} onChange={(e) => setUrl(e.target.value)} />
        <span className="mt-1 block text-xs text-muted-foreground">Outflare adds the campaign's UTM tags, ofc and the click id (ofclk) itself, once. Leave them out of the URL.</span>
        {url && <EffectiveURL url={url} />}
      </label>
      <label className="block text-sm">
        Calls to action
        <NumberInput label="Calls to action" min={1} max={100} className="mt-1 w-24" value={ctas} disabled={!canManage} onChange={setCtas} />
        <span className="mt-1 block text-xs text-muted-foreground">One click URL sends to the path's offers by weight; each numbered one to that offer.</span>
      </label>
      </div>
      {lander && (
        <div className="space-y-4">
          {lander.needs_domain && <ErrorText>Click URLs answer only on your own tracking domain: verify one in Domains.</ErrorText>}
          <div className="space-y-1 text-sm" data-testid="click-urls">
            <div>Click URLs</div>
            {[["Any offer, by weight", lander.click_url] as const, ...lander.cta_urls.map((u, i) => [`Call to action ${i + 1}`, u] as const)].map(([label, u]) => (
              <div key={label} className="flex items-center gap-2">
                <span className="w-36 shrink-0 text-xs text-muted-foreground">{label}</span>
                <span className="min-w-0 flex-1 truncate font-mono text-xs" title={u}>{u}</span>
                <CopyButton text={u} label="Copy" />
              </div>
            ))}
          </div>
          <div className="text-sm">
            Lander script
            <span className="block text-xs text-muted-foreground">Paste it into the page's head: it adds the click id to the links to its click URLs, and stores nothing in the browser.</span>
            <pre className="mt-1 overflow-x-auto rounded-md border bg-muted/40 p-2 font-mono text-xs">{lander.script}</pre>
            <CopyButton text={lander.script} label="Copy script" />
          </div>
        </div>
      )}
      {canManage && (
        <div className="flex flex-wrap gap-2 lg:col-span-2">
          <Button type="submit" variant="primary" disabled={busy}>{lander ? "Save" : "Add lander"}</Button>
          <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
        </div>
      )}
    </form>
  );
}
