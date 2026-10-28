import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { ask, Badge, Button, Card, confirmAction, ErrorText, Input, Menu, toast } from "../ui";
import { Snippet } from "./Tokens";
import { ago, plural } from "../format";
import { ConsentBannerEditor } from "./ConsentBanner";

type SiteKey = components["schemas"]["SiteKey"];

// How an origin reads to a person, and back in the box to edit it:
// https://*.example.com is "example.com", which is also what they type.
const originText = (o: string) => o.replace(/^https:\/\/\*\./, "");
const parseOrigins = (s: string) => s.split(/[\s,]+/).filter(Boolean);
const sitesLine = (k: SiteKey) => (k.origins.length ? k.origins.map(originText).join(", ") : "Any site");

// Which brand a key counts for here: with a brand picked, every key listed
// is that brand's (its own, or one with no brand); otherwise by brand.
const groupOf = (k: SiteKey, picked: boolean) => (picked ? "picked" : (k.brand_id ?? ""));

// The script a brand uses: its own before one with no brand, then the one
// that sent a visit most recently, then the oldest. The same rule the
// brand's setup picks it by, so the page and the setup agree on which one
// to keep.
function mainKeys(keys: SiteKey[], picked: boolean): Set<string> {
  const rank = (x: SiteKey): [number, number, string] => [x.brand_id ? 0 : 1, -(x.last_used_at ? Date.parse(x.last_used_at) : 0), x.created_at];
  const better = (a: SiteKey, b: SiteKey) => {
    const [ra, rb] = [rank(a), rank(b)];
    return ra[0] !== rb[0] ? ra[0] < rb[0] : ra[1] !== rb[1] ? ra[1] < rb[1] : ra[2] < rb[2];
  };
  const best = new Map<string, SiteKey>();
  for (const k of keys) {
    const cur = best.get(groupOf(k, picked));
    if (!cur || better(k, cur)) best.set(groupOf(k, picked), k);
  }
  return new Set([...best.values()].map((k) => k.id));
}

/**
 * Website tracking: each site's script, which sites it takes visits from,
 * and the rare settings (rotate, proxy, delete). One script per brand is
 * enough; a second one only splits the brand's visits, so the page says so.
 */
export function SiteKeysCard({ ws, canManage, version = 0, onChange, hideEmpty = false }: { ws: string; canManage: boolean; version?: number; onChange?: () => void; hideEmpty?: boolean }) {
  const [keys, setKeys] = useState<SiteKey[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  // The script whose consent banner is open.
  const [banner, setBanner] = useState<string | null>(null);
  const [error, setError] = useState("");
  // A proxy secret just made, shown once.
  const [proxy, setProxy] = useState<{ id: string; secret: string } | null>(null);

  const { query: brandQuery, brand, byId } = useBrand(ws);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/site-keys", { params: { path: { ws }, query: brandQuery } });
    if (error) return setError(errorMessage(error));
    setKeys(data.data);
  }, [ws, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => void load(), [load, version]);
  // After a change here: this list, and whatever else shows the script.
  const reload = () => {
    void load();
    onChange?.();
  };

  const picked = !!brand;
  const main = mainKeys(keys ?? [], picked);
  // Every key that isn't its brand's main one is a spare.
  const extra = (keys ?? []).filter((k) => !main.has(k.id));
  const isExtra = (k: SiteKey) => extra.some((x) => x.id === k.id);
  // The script adding would duplicate: the picked brand's, or the only one.
  const ownKey = picked ? (keys ?? []).find((k) => main.has(k.id)) : main.size === 1 ? (keys ?? []).find((k) => main.has(k.id)) : undefined;

  useEffect(() => {
    if (extra.length > 0) track("ui.site_key_duplicates_shown", { extra: extra.length });
  }, [extra.length]);

  // Adding when the brand already has a script: one script covers every
  // page and subdomain, and another domain goes in its allowed sites, so
  // offer that first.
  async function startAdd() {
    if (!ownKey) return setAdding(true);
    const r = await ask({
      title: `${brand?.name ?? "This workspace"} already has a script`,
      body: (
        <div className="space-y-2">
          <p>One script records every page of the site and its subdomains. To track another domain, add it to the existing script's sites: the same script then works on both, and a visit that crosses them stays one visit.</p>
          <p>A second script splits the brand's visits in two. Make one only for a site that should be counted separately.</p>
        </div>
      ),
      choices: [
        { label: "Cancel", value: "cancel", variant: "ghost" },
        { label: "Make a second script", value: "second", variant: "ghost" },
        { label: "Edit the existing script", value: "edit", variant: "primary" },
      ],
    });
    track("ui.site_key_second_asked", { choice: r?.choice ?? "cancel" });
    if (r?.choice === "edit") setEditing(ownKey.id);
    if (r?.choice === "second") setAdding(true);
  }

  async function rotate(k: SiteKey) {
    if (!(await confirmAction(`Rotate ${k.name}'s script?`, <p>It gets a new key. The old one keeps working for seven days, so paste the new script into the site before then.</p>, "Rotate", false))) return;
    const { error } = await api.POST("/v1/workspaces/{ws}/site-keys/{id}/rotate", { params: { path: { ws, id: k.id } } });
    if (error) return setError(errorMessage(error));
    toast("Rotated. Paste the new script into the site.");
    reload();
  }

  async function makeProxySecret(k: SiteKey) {
    if (k.proxy_secret_at && !(await confirmAction(`New proxy secret for ${k.name}?`, <p>The current secret stops working at once. Put the new one in your proxy straight away.</p>, "Make new secret", false))) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/site-keys/{id}/proxy-secret", { params: { path: { ws, id: k.id } } });
    if (error) return setError(errorMessage(error));
    setProxy({ id: k.id, secret: data.secret });
    reload();
  }

  async function remove(k: SiteKey) {
    const spare = isExtra(k);
    const body = spare ? (
      <p>Visits sent with it stop being recorded straight away. If it isn't on your site, nothing changes. Visits already recorded stay.</p>
    ) : (
      <p>Visits to {k.name} stop being recorded straight away, until a new script is on the site. Visits already recorded stay.</p>
    );
    if (!(await confirmAction(`Delete ${k.name}'s script?`, body, "Delete script"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/site-keys/{id}", { params: { path: { ws, id: k.id } } });
    if (error) return setError(errorMessage(error));
    toast("Deleted.");
    reload();
  }

  // Where the guided steps make the script, this card waits for one.
  if (hideEmpty && keys?.length === 0 && !adding) return null;

  const showBrand = !brand && new Set((keys ?? []).map((k) => k.brand_id)).size > 1;

  return (
    <Card
      title="Your script"
      description="One script per brand records every page of its site and subdomains, with the campaign tags, ad click ids and referral codes visits arrive with. Visitors stay anonymous until your product identifies them."
      action={canManage && keys && keys.length > 0 && !adding ? <Button variant="ghost" onClick={startAdd}>Add a site</Button> : undefined}
    >
      <div className="space-y-4">
        {extra.length > 0 && (
          <div className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-[13px]">
            {extra.length === 1 ? "There's a second script" : `There are ${extra.length} extra scripts`} for the same {brand ? "brand" : "site"}. One is enough: keep the one marked <span className="font-medium">In use</span> and delete {extra.length === 1 ? "the other" : "the others"}.
          </div>
        )}
        {keys?.map((k) =>
          editing === k.id ? (
            <EditKey key={k.id} ws={ws} k={k} onDone={(saved) => { setEditing(null); if (saved) reload(); }} />
          ) : (
            <div key={k.id} className="space-y-2">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2 font-medium">
                    {k.name}
                    {showBrand && k.brand_id && <Badge>{byId(k.brand_id)?.name ?? "Brand"}</Badge>}
                    {extra.length > 0 && (main.has(k.id) ? <Badge tone="green">In use</Badge> : isExtra(k) && <Badge tone="amber">Not needed</Badge>)}
                  </div>
                  <div className="text-[12px] text-muted-foreground">
                    Sites: {sitesLine(k)} · {plural(k.events_today, "event")} today{k.bots_today ? ` (${plural(k.bots_today, "bot request")} left out)` : ""} · {k.last_used_at ? `last visit ${ago(k.last_used_at)}` : "no visits yet"}
                  </div>
                </div>
                {!canManage && !isExtra(k) && <Button variant="ghost" onClick={() => setBanner(banner === k.id ? null : k.id)}>Consent banner</Button>}
                {canManage && (
                  <div className="flex gap-1">
                    {!isExtra(k) && <Button variant="ghost" onClick={() => setBanner(banner === k.id ? null : k.id)}>Consent banner</Button>}
                    {isExtra(k) ? (
                      <Button variant="ghost" onClick={() => remove(k)}>Delete</Button>
                    ) : (
                      <Button variant="ghost" onClick={() => setEditing(k.id)}>Edit sites</Button>
                    )}
                    <Menu
                      aria={`More for ${k.name}`}
                      items={[
                        isExtra(k) && { label: "Edit sites", onClick: () => setEditing(k.id) },
                        { label: "Rotate key", onClick: () => rotate(k) },
                        { label: k.proxy_secret_at ? "New proxy secret" : "Proxy secret", onClick: () => makeProxySecret(k) },
                        !isExtra(k) && { label: "Delete script", onClick: () => remove(k), danger: true },
                      ]}
                    />
                  </div>
                )}
              </div>
              {banner === k.id && <ConsentBannerEditor ws={ws} keyID={k.id} name={k.name} canManage={canManage} onDone={() => setBanner(null)} />}
              {!isExtra(k) && <Snippet title="Paste into <head>" code={k.snippet} />}
              {proxy?.id === k.id && (
                <Snippet title="Proxy secret: shown once. Your proxy sends it in X-Outflare-Proxy, with the visitor's address in X-Outflare-Client-IP. Never put it in a page." code={proxy.secret} />
              )}
            </div>
          ),
        )}
        {canManage && (adding || keys?.length === 0) && (
          <AddKey ws={ws} brandID={brand?.id} first={keys?.length === 0} onDone={(saved) => { setAdding(false); if (saved) reload(); }} />
        )}
        {!canManage && keys?.length === 0 && <p className="text-[13px] text-muted-foreground">No script yet. An owner or admin adds one.</p>}
        <ErrorText>{error}</ErrorText>
      </div>
    </Card>
  );
}

// Change a script's name, allowed sites and left-out query parameters in place. The script stays the
// same unless the sites now span another domain, when the tag lists them.
function EditKey({ ws, k, onDone }: { ws: string; k: SiteKey; onDone: (saved: boolean) => void }) {
  const [name, setName] = useState(k.name);
  const [origins, setOrigins] = useState(k.origins.map(originText).join(", "));
  const [excluded, setExcluded] = useState(k.excluded_params.join(", "));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/site-keys/{id}", { params: { path: { ws, id: k.id } }, body: { name: name.trim(), origins: parseOrigins(origins), excluded_params: parseOrigins(excluded) } });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    toast(data.snippet === k.snippet ? "Saved. The script on your site stays the same." : "Saved. The script changed: paste the new one into your site.");
    onDone(true);
  }

  return (
    <form onSubmit={save} className="space-y-3 rounded-lg border p-3">
      <div className="grid gap-3 sm:grid-cols-2">
        <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
        <Input label="Sites it records" value={origins} onChange={(e) => setOrigins(e.target.value)} placeholder="Any site" hint="example.com covers every subdomain. Separate several with commas. Leave empty for any site." />
        <Input
          label="Query parameters to leave out"
          value={excluded}
          onChange={(e) => setExcluded(e.target.value)}
          placeholder="None"
          hint="Every other parameter is kept, so reports can filter on it. fb_* leaves out every name starting with fb_. Sign-in tokens, passwords, codes and email addresses are always left out."
        />
      </div>
      <ErrorText>{error}</ErrorText>
      <div className="flex gap-2">
        <Button type="submit" disabled={busy || !name.trim()}>{busy ? "Saving…" : "Save"}</Button>
        <Button type="button" variant="ghost" onClick={() => onDone(false)}>Cancel</Button>
      </div>
    </form>
  );
}

function AddKey({ ws, brandID, first, onDone }: { ws: string; brandID?: string; first: boolean; onDone: (saved: boolean) => void }) {
  const [name, setName] = useState("");
  const [origins, setOrigins] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const list = parseOrigins(origins);
    const { error } = await api.POST("/v1/workspaces/{ws}/site-keys", { params: { path: { ws } }, body: { name: name.trim(), origins: list, ...(brandID ? { brand_id: brandID } : {}) } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast("Script made. Paste it into your site's <head>.");
    onDone(true);
  }

  return (
    <form onSubmit={create} className="space-y-3 rounded-lg border p-3">
      <div className="grid gap-3 sm:grid-cols-2">
        <Input label="Site" value={name} onChange={(e) => setName(e.target.value)} placeholder="example.com" required maxLength={100} autoFocus={!first} />
        <Input label="Sites it records (optional)" value={origins} onChange={(e) => setOrigins(e.target.value)} placeholder="Any site" hint="example.com covers every subdomain. You can change this later." />
      </div>
      <ErrorText>{error}</ErrorText>
      <div className="flex gap-2">
        <Button type="submit" disabled={busy || !name.trim()}>{busy ? "Making…" : "Make script"}</Button>
        {!first && <Button type="button" variant="ghost" onClick={() => onDone(false)}>Cancel</Button>}
      </div>
    </form>
  );
}

/** Whether any site in the workspace (or the picked brand) has sent a visit;
 * null while it's loading or when it can't be read. */
export function useSiteTracking(ws: string): boolean | null {
  const { query } = useBrand(ws);
  const [sending, setSending] = useState<boolean | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/site-keys", { params: { path: { ws }, query } }).then(({ data }) => {
      setSending(data ? data.data.some((k) => !!k.last_used_at) : null);
    });
  }, [ws, query.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  return sending;
}
