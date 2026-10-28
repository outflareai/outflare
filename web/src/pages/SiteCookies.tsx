import { useCallback, useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { track } from "@/lib/analytics";
import { Badge, Button, ErrorText, field, toast } from "../ui";
import { Snippet } from "./Tokens";
import { ago, plural } from "../format";

type Item = components["schemas"]["SiteCookie"];
type Scan = components["schemas"]["CookieScan"];
type Category = NonNullable<components["schemas"]["SiteCookieInput"]["category"]>;

const categories: { value: Category; label: string }[] = [
  { value: "necessary", label: "Necessary" },
  { value: "analytics", label: "Analytics" },
  { value: "marketing", label: "Marketing" },
  { value: "preferences", label: "Preferences" },
];
const kindLabel: Record<Item["kind"], string> = {
  cookie: "Cookie",
  local_storage: "Local storage",
  session_storage: "Session storage",
  script: "Script",
  iframe: "Embed",
  pixel: "Pixel",
};
const toneOf = { necessary: "grey", analytics: "blue", marketing: "amber", preferences: "green", unknown: "red" } as const;

type Listing = {
  data: Item[];
  counts: { total: number; listed: number; to_review: number; before_consent: number };
  scan: Scan | null;
  policy: { version: number; published_at: string; url: string } | null;
};

/**
 * A site's cookies, storage keys and scripts, as scans and visitors'
 * browsers found them, with the ones the catalogue doesn't know waiting
 * for a person, and the cookie policy they make.
 */
export function SiteCookies({ ws, keyID, name, canManage, onDone }: { ws: string; keyID: string; name: string; canManage: boolean; onDone: () => void }) {
  const [list, setList] = useState<Listing | null>(null);
  const [error, setError] = useState("");
  const [showAll, setShowAll] = useState(false);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/site-keys/{id}/cookies", { params: { path: { ws, id: keyID } } });
    if (error || !data) return setError(errorMessage(error));
    setList(data as Listing);
  }, [ws, keyID]);
  useEffect(() => {
    track("ui.site_cookies_opened", { site_key_id: keyID });
    void load();
  }, [load, keyID]);
  // While a scan runs, look again every few seconds.
  const running = list?.scan?.status === "queued" || list?.scan?.status === "running";
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => void load(), 3000);
    return () => clearInterval(t);
  }, [running, load]);

  async function scan() {
    const { error } = await api.POST("/v1/workspaces/{ws}/site-keys/{id}/cookie-scans", { params: { path: { ws, id: keyID } } });
    if (error) return setError(errorMessage(error));
    toast("Scanning the site. This takes a minute.");
    void load();
  }

  async function classify(it: Item, body: components["schemas"]["SiteCookieInput"]) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/site-keys/{id}/cookies/{cookie}", { params: { path: { ws, id: keyID, cookie: it.id } }, body });
    if (error) return setError(errorMessage(error));
    toast(body.status === "ignored" ? "Left out of the policy." : "Saved. The cookie policy is updated.");
    void load();
  }

  if (!list) return <div className="rounded-lg border p-3 text-[13px] text-muted-foreground">{error ? <ErrorText>{error}</ErrorText> : "Loading…"}</div>;
  const review = list.data.filter((i) => i.category === "unknown" && i.status === "new");
  const rest = list.data.filter((i) => !review.includes(i));
  const shown = showAll ? rest : rest.slice(0, 12);
  const s = list.scan;

  return (
    <div className="space-y-4 rounded-lg border p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <div className="font-medium">Cookies and scripts on {name}</div>
          <p className="text-[12px] text-muted-foreground">
            {plural(list.counts.listed, "item")} in the cookie policy
            {list.counts.to_review ? `, ${list.counts.to_review} to review` : ""}
            {list.counts.before_consent ? `, ${list.counts.before_consent} set before consent` : ""}.{" "}
            {s ? (s.status === "failed" ? `Last scan failed: ${s.error}.` : running ? "Scanning now…" : `Scanned ${ago(s.finished_at ?? s.created_at)}, ${plural(s.pages, "page")}.`) : "Not scanned yet."}{" "}
            Visitors' browsers also report what scripts set, by name only.
          </p>
        </div>
        <div className="flex gap-1">
          {canManage && <Button variant="ghost" onClick={scan} disabled={running}>{running ? "Scanning…" : "Scan now"}</Button>}
          <Button variant="ghost" onClick={onDone}>Close</Button>
        </div>
      </div>

      {list.policy && (
        <div className="space-y-2">
          <p className="text-[13px]">
            Cookie policy version {list.policy.version}, updated {ago(list.policy.published_at)}.{" "}
            <a className="underline" href={list.policy.url} target="_blank" rel="noreferrer">Open the hosted page</a>
          </p>
          <Snippet title="Or show it on your own page" code={`<div data-outflare-cookie-policy></div>`} />
        </div>
      )}

      {review.length > 0 && (
        <div className="space-y-2">
          <div className="text-[13px] font-medium">To review: Outflare doesn't know {review.length === 1 ? "this one" : "these"}</div>
          {review.map((it) => <Review key={it.id} it={it} canManage={canManage} onSave={(b) => classify(it, b)} />)}
        </div>
      )}

      {rest.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-[13px]">
            <thead>
              <tr className="text-left text-muted-foreground">
                <th className="py-1.5 pr-3 font-medium">Name</th>
                <th className="py-1.5 pr-3 font-medium">Vendor</th>
                <th className="py-1.5 pr-3 font-medium">Category</th>
                <th className="py-1.5 pr-3 font-medium">Kept for</th>
                <th className="py-1.5 font-medium">Seen</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((it) => (
                <tr key={it.id} className="border-t align-top">
                  <td className="py-1.5 pr-3">
                    <code className="break-all">{it.name}</code>
                    <div className="text-[12px] text-muted-foreground">{kindLabel[it.kind]}</div>
                  </td>
                  <td className="py-1.5 pr-3">{it.vendor_name || "This site"}</td>
                  <td className="py-1.5 pr-3">
                    <div className="flex flex-wrap gap-1">
                      <Badge tone={toneOf[it.category]}>{it.category}</Badge>
                      {it.status === "ignored" && <Badge>Left out</Badge>}
                      {it.before_consent && it.status !== "ignored" && (
                        <span title="A browser had it before the visitor allowed this category. Hold its tag back with type=&quot;text/plain&quot; data-category.">
                          <Badge tone="red">Set before consent</Badge>
                        </span>
                      )}
                    </div>
                  </td>
                  <td className="py-1.5 pr-3">{it.retention || "–"}</td>
                  <td className="py-1.5 text-muted-foreground">{ago(it.last_seen_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {rest.length > shown.length && (
            <Button variant="ghost" onClick={() => setShowAll(true)}>Show all {rest.length}</Button>
          )}
        </div>
      )}
      {list.data.length === 0 && !running && <p className="text-[13px] text-muted-foreground">Nothing found yet. Scan the site, or wait for visits.</p>}
      <ErrorText>{error}</ErrorText>
    </div>
  );
}

function Review({ it, canManage, onSave }: { it: Item; canManage: boolean; onSave: (b: components["schemas"]["SiteCookieInput"]) => void }) {
  const [category, setCategory] = useState<Category | "">("");
  const [purpose, setPurpose] = useState("");
  return (
    <div className="flex flex-wrap items-end gap-2 rounded-md border px-3 py-2 text-[13px]">
      <div className="min-w-40 flex-1">
        <code className="break-all">{it.name}</code>
        <div className="text-[12px] text-muted-foreground">
          {kindLabel[it.kind]}
          {it.host ? ` · ${it.host}` : ""}
          {it.pages.length ? ` · on ${it.pages[0]}` : ""}
        </div>
      </div>
      {canManage && (
        <>
          <select className={field} value={category} onChange={(e) => setCategory(e.target.value as Category)} aria-label={`Category of ${it.name}`}>
            <option value="">Category…</option>
            {categories.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
          </select>
          <input className={field + " min-w-48 flex-1"} value={purpose} onChange={(e) => setPurpose(e.target.value)} placeholder="What it's for" aria-label={`Purpose of ${it.name}`} />
          <Button disabled={!category} onClick={() => category && onSave({ category, purpose: purpose.trim() || undefined })}>Save</Button>
          <Button variant="ghost" onClick={() => onSave({ status: "ignored" })}>Leave out</Button>
        </>
      )}
    </div>
  );
}
