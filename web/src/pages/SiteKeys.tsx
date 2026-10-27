import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { Button, Card, confirmAction, ErrorText, Input, toast } from "../ui";
import { Snippet } from "./Tokens";
import { ago, plural } from "../format";

type SiteKey = components["schemas"]["SiteKey"];

/** Website tracking: a public key and the script tag to paste into a site. */
export function SiteKeysCard({ ws, canManage }: { ws: string; canManage: boolean }) {
  const [keys, setKeys] = useState<SiteKey[] | null>(null);
  const [name, setName] = useState("");
  const [origins, setOrigins] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // A proxy secret just made, shown once.
  const [proxy, setProxy] = useState<{ id: string; secret: string } | null>(null);
  const path = { ws };

  const { query: brandQuery, brand } = useBrand(ws);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/site-keys", { params: { path: { ws }, query: brandQuery } });
    if (error) return setError(errorMessage(error));
    setKeys(data.data);
  }, [ws, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => void load(), [load]);

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const list = origins.split(/[\s,]+/).filter(Boolean);
    const { error } = await api.POST("/v1/workspaces/{ws}/site-keys", { params: { path }, body: { name: name.trim(), origins: list, ...(brand ? { brand_id: brand.id } : {}) } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setError("");
    setName("");
    setOrigins("");
    toast("Site key added. Paste its snippet into your site's <head>.");
    load();
  }

  async function rotate(k: SiteKey) {
    if (!(await confirmAction(`Rotate ${k.name}?`, <p>It gets a new key. The old one keeps working for seven days, so paste the new snippet into the site before then.</p>, "Rotate", false))) return;
    const { error } = await api.POST("/v1/workspaces/{ws}/site-keys/{id}/rotate", { params: { path: { ws, id: k.id } } });
    if (error) return setError(errorMessage(error));
    toast("Rotated. Paste the new snippet into the site.");
    load();
  }

  async function makeProxySecret(k: SiteKey) {
    if (k.proxy_secret_at && !(await confirmAction(`New proxy secret for ${k.name}?`, <p>The current secret stops working at once. Put the new one in your proxy straight away.</p>, "Make new secret", false))) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/site-keys/{id}/proxy-secret", { params: { path: { ws, id: k.id } } });
    if (error) return setError(errorMessage(error));
    setProxy({ id: k.id, secret: data.secret });
    load();
  }

  async function revoke(k: SiteKey) {
    if (!(await confirmAction(`Revoke ${k.name}?`, <p>The site's visits stop being recorded straight away. Visits already recorded stay.</p>, "Revoke key"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/site-keys/{id}", { params: { path: { ws, id: k.id } } });
    if (error) return setError(errorMessage(error));
    toast("Revoked.");
    load();
  }

  return (
    <Card
      title="Website tracking"
      description="Records visits to your sites with the campaign tags, ad click ids and referral codes they arrived with, so sign-ups and revenue can be credited to the channel that brought them. Visitors stay anonymous until your product identifies them."
    >
      <div className="space-y-4">
        {keys?.map((k) => (
          <div key={k.id} className="space-y-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="min-w-0">
                <div className="font-medium">{k.name}</div>
                <div className="text-[12px] text-muted-foreground">
                  {k.origins.length ? k.origins.join(", ") : "Any site"} · {plural(k.events_today, "event")} today{k.bots_today ? ` (${plural(k.bots_today, "bot request")} left out)` : ""} · {k.last_used_at ? `last used ${ago(k.last_used_at)}` : "not used yet"}
                </div>
              </div>
              {canManage && (
                <div className="flex gap-1">
                  <Button variant="ghost" onClick={() => makeProxySecret(k)}>{k.proxy_secret_at ? "New proxy secret" : "Proxy secret"}</Button>
                  <Button variant="ghost" onClick={() => rotate(k)}>Rotate</Button>
                  <Button variant="ghost" onClick={() => revoke(k)}>Revoke</Button>
                </div>
              )}
            </div>
            <Snippet title="Paste into <head>" code={k.snippet} />
            {proxy?.id === k.id && (
              <Snippet title="Proxy secret: shown once. Your proxy sends it in X-Outflare-Proxy, with the visitor's address in X-Outflare-Client-IP. Never put it in a page." code={proxy.secret} />
            )}
          </div>
        ))}
        {canManage && (
          <form onSubmit={create} className="grid gap-2 sm:grid-cols-[1fr_1fr_auto] sm:items-end">
            <Input label="Site" value={name} onChange={(e) => setName(e.target.value)} placeholder="example.com" required maxLength={100} />
            <Input label="Origins (optional)" value={origins} onChange={(e) => setOrigins(e.target.value)} placeholder="example.com (covers every subdomain)" />
            <Button type="submit" className="sm:mb-5" disabled={busy || !name.trim()}>{busy ? "Adding…" : "Add site"}</Button>
          </form>
        )}
        <ErrorText>{error}</ErrorText>
      </div>
    </Card>
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
