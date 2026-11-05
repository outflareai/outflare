import { PickBrand } from "@/components/pick-brand";
import { pickedBrandId } from "@/lib/brand";
import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { ask, Badge, Button, Card, confirmAction, ErrorText, Input, Menu, toast } from "../ui";
import { Snippet } from "./Tokens";
import { ago, plural } from "../format";
import { ConsentBannerEditor } from "./ConsentBanner";
import { SiteCookies } from "./SiteCookies";
import { Switch } from "@/components/ui/switch";

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
// Website scripts belong to one brand (plans/brands/README.md): with all brands
// picked the page asks for one, and switching brands opens it afresh.
export function SiteKeysCard(props: { ws: string; canManage: boolean; version?: number; onChange?: () => void; hideEmpty?: boolean }) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Website scripts" brands={active.length} />;
  return <SiteKeysList key={brand.id} {...props} />;
}

function SiteKeysList({ ws, canManage, version = 0, onChange, hideEmpty = false }: { ws: string; canManage: boolean; version?: number; onChange?: () => void; hideEmpty?: boolean }) {
  const [keys, setKeys] = useState<SiteKey[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  // The script whose consent banner is open.
  const [banner, setBanner] = useState<string | null>(null);
  // The script whose cookies are open.
  const [cookiesOf, setCookiesOf] = useState<string | null>(null);
  const [error, setError] = useState("");
  // A proxy secret just made, shown once.
  const [proxy, setProxy] = useState<{ id: string; secret: string } | null>(null);
  const [ownDomainOf, setOwnDomainOf] = useState<string | null>(null);

  const { query: brandQuery, brand, byId } = useBrand(ws);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/site-keys", { params: { path: { brand: pickedBrandId() } } });
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
    const { error } = await api.POST("/v1/brands/{brand}/site-keys/{id}/rotate", { params: { path: { brand: pickedBrandId(), id: k.id } } });
    if (error) return setError(errorMessage(error));
    toast("Rotated. Paste the new script into the site.");
    reload();
  }

  async function makeProxySecret(k: SiteKey) {
    if (k.proxy_secret_at && !(await confirmAction(`New proxy secret for ${k.name}?`, <p>The current secret stops working at once. Put the new one in your proxy straight away.</p>, "Make new secret", false))) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/site-keys/{id}/proxy-secret", { params: { path: { brand: pickedBrandId(), id: k.id } } });
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
    const { error } = await api.DELETE("/v1/brands/{brand}/site-keys/{id}", { params: { path: { brand: pickedBrandId(), id: k.id } } });
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
                  {!isExtra(k) && <OwnDomainLine k={k} onOpen={() => { const next = ownDomainOf === k.id ? null : k.id; setOwnDomainOf(next); if (next) track("ui.site_own_domain_opened", { site_key_id: k.id, status: k.own_domain.status }); }} />}
                  <div className="text-[12px] text-muted-foreground">
                    Sites: {sitesLine(k)} · {plural(k.events_today, "event")} today{k.bots_today ? ` (${plural(k.bots_today, "bot request")} left out)` : ""} · {k.last_used_at ? `last visit ${ago(k.last_used_at)}` : "no visits yet"}
                  </div>
                </div>
                {!canManage && !isExtra(k) && (
                  <div className="flex gap-1">
                    <Button variant="ghost" onClick={() => setBanner(banner === k.id ? null : k.id)}>Consent banner</Button>
                    <Button variant="ghost" onClick={() => setCookiesOf(cookiesOf === k.id ? null : k.id)}>Cookies</Button>
                  </div>
                )}
                {canManage && (
                  <div className="flex gap-1">
                    {!isExtra(k) && <Button variant="ghost" onClick={() => setBanner(banner === k.id ? null : k.id)}>Consent banner</Button>}
                    {!isExtra(k) && <Button variant="ghost" onClick={() => setCookiesOf(cookiesOf === k.id ? null : k.id)}>Cookies</Button>}
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
              {cookiesOf === k.id && <SiteCookies ws={ws} keyID={k.id} name={k.name} canManage={canManage} onDone={() => setCookiesOf(null)} />}
              {ownDomainOf === k.id && <OwnDomainSteps k={k} canManage={canManage} onProxySecret={() => makeProxySecret(k)} />}
              {!isExtra(k) && ownDomainOf !== k.id && <Snippet title="Paste into <head>" code={k.own_domain.status === "working" || k.own_domain.status === "partly" ? k.own_domain.snippet : k.snippet} />}
              {!isExtra(k) && ownDomainOf !== k.id && k.tests && (
                <details className="text-[13px]">
                  <summary className="cursor-pointer text-muted-foreground">Optional: stop A/B tests flashing the original</summary>
                  <p className="mt-2 text-muted-foreground">Paste this once at the very top of your site's &lt;head&gt;, above everything else. On a visitor's next visit it hides just the parts their test changes, for a second at most, until their version shows. Pages look the same for everyone else.</p>
                  <Snippet title="Top of <head>" code={k.flicker_guard} />
                </details>
              )}
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

// OwnDomainLine says whether the script is served from the site's own
// domain, from the last 7 days of events, and opens the steps.
function OwnDomainLine({ k, onOpen }: { k: SiteKey; onOpen: () => void }) {
  const o = k.own_domain;
  const share = o.events ? Math.round((o.through_own_domain * 100) / o.events) : 0;
  const safari = o.events ? Math.round((o.safari_direct * 100) / o.events) : 0;
  const text =
    o.status === "working" ? "Served from your own domain" :
    o.status === "partly" ? `${share}% of events come through your own domain` :
    o.status === "no_traffic" ? "Not served from your own domain yet" :
    safari > 0 ? `Not served from your own domain: Safari forgets ${safari}% of your visitors after 7 days` : "Not served from your own domain";
  return (
    <button type="button" className="mt-0.5 flex items-center gap-1.5 text-[12px] underline-offset-2 hover:underline" onClick={onOpen}>
      <Badge tone={o.status === "working" ? "green" : o.status === "partly" || safari > 0 ? "amber" : "grey"}>{o.status === "working" ? "Own domain" : "Set up own domain"}</Badge>
      <span className="text-muted-foreground">{text}</span>
    </button>
  );
}

const ownDomainHosts = [
  { id: "nextjs", label: "Next.js" },
  { id: "vercel", label: "Vercel" },
  { id: "netlify", label: "Netlify" },
  { id: "cloudflare", label: "Cloudflare" },
  { id: "nginx", label: "nginx" },
] as const;

// OwnDomainSteps: forward /ofx/ on the site to Outflare, then load the
// script from there. Events then go to the site's own domain, so ad
// blockers leave them alone and Safari keeps the visitor for a year.
function OwnDomainSteps({ k, canManage, onProxySecret }: { k: SiteKey; canManage: boolean; onProxySecret: () => void }) {
  const [host, setHost] = useState<(typeof ownDomainHosts)[number]["id"]>("nextjs");
  const origin = /src="(https?:\/\/[^/"]+)\//.exec(k.snippet)?.[1] ?? "https://app.outflare.ai";
  const hostName = origin.replace(/^https?:\/\//, "");
  const configs: Record<typeof host, { title: string; code: string }> = {
    nextjs: { title: "next.config.js", code: `module.exports = {\n  async rewrites() {\n    return [{ source: "/ofx/:path*", destination: "${origin}/:path*" }];\n  },\n};` },
    vercel: { title: "vercel.json", code: `{\n  "rewrites": [{ "source": "/ofx/:path*", "destination": "${origin}/:path*" }]\n}` },
    netlify: { title: "_redirects (or netlify.toml)", code: `/ofx/*  ${origin}/:splat  200` },
    cloudflare: {
      title: "A Worker on the route example.com/ofx/*",
      code: `export default {\n  async fetch(request, env) {\n    const url = new URL(request.url);\n    const to = new URL("${origin}" + url.pathname.replace(/^\\/ofx/, "") + url.search);\n    const headers = new Headers(request.headers);\n    // With a proxy secret (env.OUTFLARE_PROXY), Outflare sees each visitor's own address and country.\n    if (env.OUTFLARE_PROXY) {\n      headers.set("X-Outflare-Proxy", env.OUTFLARE_PROXY);\n      headers.set("X-Outflare-Client-IP", request.headers.get("CF-Connecting-IP") ?? "");\n      headers.set("X-Outflare-Country", request.headers.get("CF-IPCountry") ?? "");\n    }\n    return fetch(to, { method: request.method, headers, body: request.body });\n  },\n};`,
    },
    nginx: {
      title: "nginx",
      code: `location /ofx/ {\n  proxy_pass ${origin}/;\n  proxy_set_header Host ${hostName};\n  proxy_ssl_server_name on;\n  # With a proxy secret, Outflare sees each visitor's own address:\n  # proxy_set_header X-Outflare-Proxy "your proxy secret";\n  # proxy_set_header X-Outflare-Client-IP $remote_addr;\n}`,
    },
  };
  const o = k.own_domain;
  return (
    <div className="space-y-3 rounded-md border p-3 text-[13px]">
      <div>
        <div className="font-medium">Serve the script from your own domain</div>
        <p className="text-muted-foreground">
          Safari and every iPhone browser forget a visitor after 7 days when the script talks to Outflare directly, so a returning visitor looks new and never joins their earlier visits. Ad blockers drop those events too. Forward one path on your site to Outflare and both problems go away: the visitor is kept for a year.
        </p>
      </div>
      <ol className="list-decimal space-y-3 pl-5">
        <li className="space-y-2">
          <div>Forward <code>/ofx/</code> on your site to Outflare. Pick where your site runs:</div>
          <div className="flex flex-wrap rounded-md border" role="group" aria-label="Where your site runs">
            {ownDomainHosts.map((h) => (
              <button key={h.id} type="button" className={`px-2.5 py-1 ${h.id === host ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => { setHost(h.id); track("ui.site_own_domain_host_picked", { host: h.id }); }}>{h.label}</button>
            ))}
          </div>
          <Snippet title={configs[host].title} code={configs[host].code} />
        </li>
        <li className="space-y-2">
          <div>Replace the tag in your site's &lt;head&gt; with this one:</div>
          <Snippet title="Paste into <head>" code={o.snippet} />
        </li>
        <li>
          Optional: a <span className="font-medium">proxy secret</span> lets Outflare see each visitor's own address and country (for company lookups, consent rules and limits) instead of your server's.{" "}
          {canManage && <button type="button" className="underline" onClick={onProxySecret}>{k.proxy_secret_at ? "Make a new one" : "Make one"}</button>}
        </li>
      </ol>
      <p className="text-muted-foreground">
        {o.status === "working"
          ? `Working: ${plural(o.through_own_domain, "event")} of ${o.events.toLocaleString()} in the last 7 days came through your own domain.`
          : o.status === "partly"
            ? `Partly working: ${plural(o.through_own_domain, "event")} of ${o.events.toLocaleString()} in the last 7 days came through your own domain. Pages still loading the old tag go straight to Outflare.`
            : o.events
              ? `Not working yet: none of the last 7 days' ${plural(o.events, "event")} came through your own domain${o.safari_direct ? `, and ${plural(o.safari_direct, "event")} came from Safari or an iPhone` : ""}. This updates as soon as events arrive the new way.`
              : "Once events arrive through your own domain, this says so."}
      </p>
    </div>
  );
}

// Change a script's name, allowed sites and left-out query parameters in place. The script stays the
// same unless the sites now span another domain, when the tag lists them.
function EditKey({ k, onDone }: { ws: string; k: SiteKey; onDone: (saved: boolean) => void }) {
  const [name, setName] = useState(k.name);
  const [origins, setOrigins] = useState(k.origins.map(originText).join(", "));
  const [excluded, setExcluded] = useState(k.excluded_params.join(", "));
  const [tests, setTests] = useState(k.tests);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.PATCH("/v1/brands/{brand}/site-keys/{id}", { params: { path: { brand: pickedBrandId(), id: k.id } }, body: { name: name.trim(), origins: parseOrigins(origins), excluded_params: parseOrigins(excluded), tests } });
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
      <label className="flex items-start gap-2 text-[13px]">
        <Switch checked={tests} onCheckedChange={setTests} aria-label="Run A/B tests on this site" className="mt-0.5" />
        <span>
          <span className="block font-medium">Run A/B tests on this site</span>
          <span className="block text-muted-foreground">Tests you make with the visual editor run on their own, with no change to the script. Off: only pages whose code asks for flags get them.</span>
        </span>
      </label>
      <ErrorText>{error}</ErrorText>
      <div className="flex gap-2">
        <Button type="submit" disabled={busy || !name.trim()}>{busy ? "Saving…" : "Save"}</Button>
        <Button type="button" variant="ghost" onClick={() => onDone(false)}>Cancel</Button>
      </div>
    </form>
  );
}

function AddKey({ brandID, first, onDone }: { ws: string; brandID?: string; first: boolean; onDone: (saved: boolean) => void }) {
  const [name, setName] = useState("");
  const [origins, setOrigins] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const list = parseOrigins(origins);
    const { error } = await api.POST("/v1/brands/{brand}/site-keys", { params: { path: { brand: pickedBrandId() } }, body: { name: name.trim(), origins: list, ...(brandID ? { brand_id: brandID } : {}) } });
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
    api.GET("/v1/brands/{brand}/site-keys", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => {
      setSending(data ? data.data.some((k) => !!k.last_used_at) : null);
    });
  }, [ws, query.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  return sending;
}
