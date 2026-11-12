// Tracker → Offers and Landers (plans/ad-tracker-ui.md B3): the brand's
// offers and landers, each list with today's numbers, and New or a row
// opening a sheet: an offer's network, URL with a token picker, payout
// (fixed, from the postback, by country), cap with a fallback offer, and
// Preview; a lander's URL, calls to action with a click URL each, and the
// script for its page.

import { useCallback, useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, CopyButton, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";

type Offer = Schemas["TrackerOffer"];
type Lander = Schemas["TrackerLander"];
type Network = Schemas["PostbackSource"];

type Props = { ws: string; canManage: boolean };

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

export function TrackerOffers({ ws, canManage }: Props) {
  const { brand, active } = useBrand(ws);
  const [offers, setOffers] = useState<Offer[] | null>(null);
  const [networks, setNetworks] = useState<Network[]>([]);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<Offer | "new" | null>(null);
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
  return (
    <div className="space-y-4">
      <PageHeader
        title="Offers"
        description="The offers your campaigns send clicks to: where each one pays through, what it pays, and its daily cap."
        action={canManage && <Button variant="primary" onClick={() => setOpen("new")}>New offer</Button>}
      />
      {offers.length === 0 ? (
        <Empty title="No offers yet">An offer is a page an affiliate network pays you for: add it once, then send any campaign's clicks to it.</Empty>
      ) : (
        <Card title={`${offers.length} offer${offers.length === 1 ? "" : "s"}`}>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  <th className="py-2 pr-3 font-medium">Offer</th>
                  <th className="px-3 py-2 font-medium">Network</th>
                  <th className="px-3 py-2 text-right font-medium">Payout</th>
                  <th className="px-3 py-2 text-right font-medium">Clicks today</th>
                  <th className="px-3 py-2 text-right font-medium">Conversions today</th>
                  <th className="px-3 py-2 text-right font-medium">Used by</th>
                </tr>
              </thead>
              <tbody>
                {offers.map((o) => (
                  <tr key={o.id} className="cursor-pointer border-t hover:bg-muted/40" onClick={() => setOpen(o)}>
                    <td className="py-2 pr-3">
                      <span className="font-medium">{o.name}</span>
                      {o.missing_click_id && <Badge tone="amber" title="The URL carries no {click_id}, so a postback can't name the click">Missing click id</Badge>}
                      {o.cap_reached && <Badge tone="red">Capped</Badge>}
                    </td>
                    <td className="px-3 py-2">{network(o.source_id)}</td>
                    <td className="px-3 py-2 text-right font-mono tabular-nums">
                      {o.payout} {o.currency}
                      {Object.keys(o.country_payouts).length > 0 && <span className="text-xs text-muted-foreground"> +{Object.keys(o.country_payouts).length} countries</span>}
                    </td>
                    <td className="px-3 py-2 text-right font-mono tabular-nums">{o.clicks_today.toLocaleString()}</td>
                    <td className="px-3 py-2 text-right font-mono tabular-nums">{o.conversions_today.toLocaleString()}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{o.destinations === 0 ? "—" : `${o.destinations} destination${o.destinations === 1 ? "" : "s"}`}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      <Sheet open={open !== null} onOpenChange={(o) => !o && setOpen(null)}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
          {open !== null && (
            <OfferForm
              key={open === "new" ? "new" : open.id}
              offer={open === "new" ? null : open}
              offers={offers}
              networks={networks}
              canManage={canManage}
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

function OfferForm({ offer, offers, networks, canManage, onSaved }: { offer: Offer | null; offers: Offer[]; networks: Network[]; canManage: boolean; onSaved: () => void }) {
  const [name, setName] = useState(offer?.name ?? "");
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
  const missing = offer && offer.url === url ? offer.missing_click_id : !url.includes("{click_id}") && !network;
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
    };
    const brand = pickedBrandId();
    const r = offer
      ? await api.PATCH("/v1/brands/{brand}/offers/{id}", { params: { path: { brand, id: offer.id } }, body })
      : await api.POST("/v1/brands/{brand}/offers", { params: { path: { brand } }, body });
    setBusy(false);
    if (r.error) return toast(errorMessage(r.error), "error");
    toast(offer ? "Offer saved" : "Offer added", "ok");
    onSaved();
  };
  const archive = async () => {
    if (!offer || !(await confirmAction(`Archive ${offer.name}?`, "It leaves the list and can't be picked for new destinations. Its reports stay.", "Archive"))) return;
    const { error } = await api.PATCH("/v1/brands/{brand}/offers/{id}", { params: { path: { brand: pickedBrandId(), id: offer.id } }, body: { status: "archived" } });
    if (error) return toast(errorMessage(error), "error");
    toast("Offer archived", "ok");
    onSaved();
  };
  return (
    <form className="space-y-4 p-4" onSubmit={(e) => { e.preventDefault(); void save(); }}>
      <SheetHeader className="p-0">
        <SheetTitle>{offer ? offer.name : "New offer"}</SheetTitle>
        <SheetDescription>Where its clicks go, what it pays and how many it takes a day.</SheetDescription>
      </SheetHeader>
      <label className="block text-sm">
        Affiliate network
        <select aria-label="Affiliate network" className={`${field} mt-1 w-full`} value={network} disabled={!canManage} onChange={(e) => setNetwork(e.target.value)}>
          <option value="">None</option>
          {networks.map((n) => (
            <option key={n.id} value={n.id}>{n.name}</option>
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
        {missing && url && <p className="mt-1 text-xs text-amber-700 dark:text-amber-400">Missing click id: add {"{click_id}"} where the network takes a sub id, or its postback can't name the click.</p>}
        {url && (
          <button type="button" className="mt-1 text-xs text-muted-foreground hover:underline" onClick={() => window.open(preview(url), "_blank", "noopener")}>
            Preview with test values ↗
          </button>
        )}
      </div>
      <fieldset className="space-y-2 text-sm">
        <legend>Payout</legend>
        <div className="flex flex-wrap gap-3">
          <label className="flex items-center gap-1"><input type="radio" name="payout-mode" checked={mode === "postback"} disabled={!canManage} onChange={() => setMode("postback")} /> From the postback</label>
          <label className="flex items-center gap-1"><input type="radio" name="payout-mode" checked={mode === "fixed"} disabled={!canManage} onChange={() => setMode("fixed")} /> Fixed</label>
        </div>
        <div className="flex gap-2">
          <input aria-label="Payout" inputMode="decimal" className={`${field} w-28`} value={payout} disabled={!canManage} onChange={(e) => setPayout(e.target.value)} />
          <input aria-label="Currency" className={`${field} w-20 uppercase`} maxLength={3} value={currency} disabled={!canManage} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />
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
            {offers.filter((o) => o.id !== offer?.id).map((o) => (
              <option key={o.id} value={o.id}>{o.name}</option>
            ))}
          </select>
        </label>
      </fieldset>
      {offer && (
        <div className="text-sm">
          Tracking URL
          <div className="mt-1 flex items-start gap-2">
            <div className="min-w-0 flex-1 break-all rounded-md border bg-muted/40 p-2 font-mono text-xs">{offer.tracking_url}</div>
            <CopyButton text={offer.tracking_url} label="Copy" />
          </div>
        </div>
      )}
      {canManage && (
        <div className="flex flex-wrap gap-2">
          <Button type="submit" variant="primary" disabled={busy}>{offer ? "Save" : "Add offer"}</Button>
          {offer && <Button type="button" variant="ghost" onClick={() => void archive()}>Archive</Button>}
        </div>
      )}
    </form>
  );
}

export function TrackerLanders({ ws, canManage }: Props) {
  const { brand, active } = useBrand(ws);
  const [landers, setLanders] = useState<Lander[] | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<Lander | "new" | null>(null);
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
  return (
    <div className="space-y-4">
      <PageHeader
        title="Landers"
        description="Pages of your own that clicks see before the offer: each call to action has its own click URL."
        action={canManage && <Button variant="primary" onClick={() => setOpen("new")}>New lander</Button>}
      />
      {landers.length === 0 ? (
        <Empty title="No landers yet">A lander is a page before the offer, such as a quiz or a review. Add it, put its script on the page, and link its buttons to its click URLs.</Empty>
      ) : (
        <Card title={`${landers.length} lander${landers.length === 1 ? "" : "s"}`}>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  <th className="py-2 pr-3 font-medium">Lander</th>
                  <th className="px-3 py-2 text-right font-medium">Calls to action</th>
                  <th className="px-3 py-2 text-right font-medium">CTA clicks today</th>
                  <th className="px-3 py-2 text-right font-medium">Used by</th>
                </tr>
              </thead>
              <tbody>
                {landers.map((l) => (
                  <tr key={l.id} className="cursor-pointer border-t hover:bg-muted/40" onClick={() => setOpen(l)}>
                    <td className="py-2 pr-3">
                      <span className="font-medium">{l.name}</span>
                      <span className="block truncate text-xs text-muted-foreground">{l.url}</span>
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums">{l.ctas}</td>
                    <td className="px-3 py-2 text-right font-mono tabular-nums">{l.lander_clicks_today.toLocaleString()}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{l.destinations === 0 ? "—" : `${l.destinations} destination${l.destinations === 1 ? "" : "s"}`}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      <Sheet open={open !== null} onOpenChange={(o) => !o && setOpen(null)}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
          {open !== null && (
            <LanderForm
              key={open === "new" ? "new" : open.id}
              lander={open === "new" ? null : open}
              canManage={canManage}
              onSaved={async (id, close) => {
                const list = await load();
                if (close) return setOpen(null);
                const saved = list?.find((l) => l.id === id);
                if (saved) setOpen(saved);
              }}
            />
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}

function LanderForm({ lander, canManage, onSaved }: { lander: Lander | null; canManage: boolean; onSaved: (id: string, close: boolean) => void }) {
  const [name, setName] = useState(lander?.name ?? "");
  const [url, setUrl] = useState(lander?.url ?? "");
  const [ctas, setCtas] = useState(lander?.ctas ?? 1);
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
  const archive = async () => {
    if (!lander || !(await confirmAction(`Archive ${lander.name}?`, "It leaves the list and can't be picked for new destinations. Its reports stay.", "Archive"))) return;
    const { error } = await api.PATCH("/v1/brands/{brand}/landers/{id}", { params: { path: { brand: pickedBrandId(), id: lander.id } }, body: { status: "archived" } });
    if (error) return toast(errorMessage(error), "error");
    toast("Lander archived", "ok");
    onSaved(lander.id, true);
  };
  return (
    <form className="space-y-4 p-4" onSubmit={(e) => { e.preventDefault(); void save(); }}>
      <SheetHeader className="p-0">
        <SheetTitle>{lander ? lander.name : "New lander"}</SheetTitle>
        <SheetDescription>Its page, and where each call to action on it links.</SheetDescription>
      </SheetHeader>
      <label className="block text-sm">
        Name
        <input aria-label="Name" required className={`${field} mt-1 w-full`} value={name} disabled={!canManage} onChange={(e) => setName(e.target.value)} />
      </label>
      <label className="block text-sm">
        Lander URL
        <input aria-label="Lander URL" required className={`${field} mt-1 w-full font-mono text-xs`} placeholder="https://acme.example/quiz" value={url} disabled={!canManage} onChange={(e) => setUrl(e.target.value)} />
      </label>
      <label className="block text-sm">
        Calls to action
        <input aria-label="Calls to action" type="number" min={1} max={100} className={`${field} mt-1 w-24`} value={ctas} disabled={!canManage} onChange={(e) => setCtas(Math.max(1, Math.min(100, Number(e.target.value) || 1)))} />
        <span className="mt-1 block text-xs text-muted-foreground">One click URL sends to the path's offers by weight; each numbered one to that offer.</span>
      </label>
      {lander && (
        <>
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
        </>
      )}
      {canManage && (
        <div className="flex flex-wrap gap-2">
          <Button type="submit" variant="primary" disabled={busy}>{lander ? "Save" : "Add lander"}</Button>
          {lander && <Button type="button" variant="ghost" onClick={() => void archive()}>Archive</Button>}
        </div>
      )}
    </form>
  );
}
