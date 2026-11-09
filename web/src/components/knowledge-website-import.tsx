import { useEffect, useState } from "react";
import type { KnowledgeWebsiteImport } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, field, toast } from "../ui";
import { ago } from "../format";
import { cn } from "@/lib/utils";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { track } from "@/lib/analytics";

// Importing the business's website into knowledge: up to max_pages pages,
// one article each, in a Website folder, re-checked every day.
export function KnowledgeWebsiteImportPanel({ ws, canManage, onDone }: { ws: string; canManage: boolean; onDone: () => void }) {
  const { brand } = useBrand(ws);
  const [list, setList] = useState<KnowledgeWebsiteImport[] | null>(null);
  const [url, setUrl] = useState("");
  const [max, setMax] = useState(100);
  const [publish, setPublish] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = () =>
    api.GET("/v1/brands/{brand}/knowledge/website-imports", { params: { path: { brand: pickedBrandId() } } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      setList(data.data);
    });
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => void load(), [ws, brand?.id]);
  const running = list?.find((i) => i.status === "queued" || i.status === "running");
  useEffect(() => {
    if (!running) return;
    const t = setTimeout(async () => {
      await load();
    }, 3000);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running?.id, list]);
  const last = list?.[0];
  useEffect(() => {
    if (last?.status === "done") onDone();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [last?.id, last?.status]);

  async function start() {
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/brands/{brand}/knowledge/website-imports", {
      params: { path: { brand: pickedBrandId() } },
      body: { url: url.trim(), brand: brand?.handle ?? null, max_pages: max, publish },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    track("ui.knowledge_website_import_started", { max_pages: max, publish });
    setList((l) => [data as KnowledgeWebsiteImport, ...(l ?? [])]);
    toast("Reading your website. Pages appear in the Website folder as they're imported.");
  }

  return (
    <div className="space-y-4">
      <p className="text-[13px] text-muted-foreground">
        Outflare reads your website politely, as OutflareBot, and keeps each page with enough words as an article in a <strong>Website</strong> folder.
        AI answers use them from then on. It reads your sitemap, and after the first import checks the site every day: changed pages are updated,
        new ones added, and pages you take off the site are unpublished here.
      </p>
      {canManage && (
        <div className="space-y-3">
          <label className="block space-y-1">
            <span className="text-[13px] font-medium">Website</span>
            <input className={cn(field, "w-full")} value={url} placeholder={brand ? `${brand.name}'s website` : "Your company's website"}
              onChange={(e) => setUrl(e.target.value)} maxLength={2000} />
            <span className="block text-xs text-muted-foreground">Leave it empty to use the website in your brand or company details.</span>
          </label>
          <label className="flex items-center gap-2 text-[13px]">
            Up to
            <input type="number" className={cn(field, "w-24")} min={1} max={500} value={max} onChange={(e) => setMax(Math.min(500, Math.max(1, Number(e.target.value) || 1)))} />
            pages
          </label>
          <label className="flex items-start gap-2 text-[13px]">
            <input type="checkbox" className="mt-1" checked={publish} onChange={(e) => setPublish(e.target.checked)} />
            <span>Use them for answers straight away<span className="block text-xs text-muted-foreground">Off keeps them as drafts for you to go through first.</span></span>
          </label>
          {error && <ErrorText>{error}</ErrorText>}
          <Button disabled={busy || !!running} onClick={start}>{running ? "Importing…" : busy ? "Starting…" : "Import website"}</Button>
        </div>
      )}
      {list && list.length > 0 && (
        <ul className="divide-y rounded-md border text-[13px]">
          {list.slice(0, 5).map((i) => (
            <li key={i.id} className="space-y-0.5 px-3 py-2">
              <p className="flex justify-between gap-2">
                <span className="truncate">{i.url}</span>
                <span className="shrink-0 text-xs text-muted-foreground">{ago(i.created_at)}</span>
              </p>
              <p className="text-xs text-muted-foreground">
                {i.status === "done"
                  ? `${i.scheduled ? "Daily check: " : ""}${i.pages_added} added, ${i.pages_updated} updated${i.pages_removed ? `, ${i.pages_removed} unpublished (gone from the site)` : ""}, ${i.pages_skipped} left out of ${i.pages_found} pages`
                  : i.status === "failed"
                    ? i.error || "It couldn't read this website."
                    : "Reading the website…"}
              </p>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
