import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { useEffect, useState } from "react";
import { Copy, Download } from "lucide-react";
import type { ContentItem } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, PageHeader, toast } from "../ui";
import { track } from "@/lib/analytics";

const statuses = [
  { value: "", label: "All" },
  { value: "draft", label: "Drafts" },
  { value: "approved", label: "Approved" },
  { value: "published", label: "Live" },
  { value: "archived", label: "Archived" },
];
const statusTone: Record<string, "grey" | "green" | "amber" | "blue"> = { draft: "grey", approved: "amber", published: "green", archived: "grey" };
const statusLabel: Record<string, string> = { draft: "Draft", approved: "Approved", published: "Live", archived: "Archived" };

// The Markdown file a site's repository takes: front matter from the
// item's fields, then the body.
export function markdownFile(c: ContentItem): string {
  const q = (s: string) => JSON.stringify(s);
  const lines = ["---", `title: ${q(c.title)}`];
  if (c.slug) lines.push(`slug: ${q(c.slug)}`);
  if (c.excerpt) lines.push(`description: ${q(c.excerpt)}`);
  if (c.language) lines.push(`lang: ${q(c.language)}`);
  if (c.published_at) lines.push(`date: ${q(c.published_at.slice(0, 10))}`);
  if (c.tags.length) lines.push(`tags: [${c.tags.map(q).join(", ")}]`);
  if (c.canonical_url) lines.push(`canonical: ${q(c.canonical_url)}`);
  if (c.seo.meta_title) lines.push(`meta_title: ${q(c.seo.meta_title)}`);
  if (c.seo.meta_description) lines.push(`meta_description: ${q(c.seo.meta_description)}`);
  if (c.seo.primary_keyword) lines.push(`keyword: ${q(c.seo.primary_keyword)}`);
  const hero = c.media.find((m) => m.role === "featured");
  if (hero) lines.push(`image: ${q(hero.url)}`, ...(hero.alt ? [`image_alt: ${q(hero.alt)}`] : []));
  lines.push("---", "", c.body ?? "");
  return lines.join("\n");
}

// Content: posts and pages written for the brand's own website
// (docs/content.md). Approved items go to the site through the
// content.approved webhook or a CMS connection; here a person can also
// copy or download one and put it in place by hand, then say where it
// went live.
export function Content({ ws, canManage, go }: { ws: string; canManage: boolean; go: (tab: "webhooks") => void }) {
  const { brand, active } = useBrand(ws);
  const brandId = brand?.id ?? "";
  const [status, setStatus] = useState("");
  const [items, setItems] = useState<ContentItem[] | null>(null);
  const [open, setOpen] = useState<ContentItem | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  async function load() {
    if (!brandId) return;
    const { data, error } = await api.GET("/v1/brands/{brand}/content", { params: { path: { brand: brandId }, query: status ? { status: status as "draft" } : {} } });
    if (data) setItems(data.data);
    setError(error ? errorMessage(error) : "");
  }
  useEffect(() => {
    setOpen(null);
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandId, status]);

  async function show(id: string) {
    const { data, error } = await api.GET("/v1/brands/{brand}/content/{id}", { params: { path: { brand: brandId, id } } });
    if (data) setOpen(data);
    setError(error ? errorMessage(error) : "");
  }

  async function approve(c: ContentItem) {
    if (!(await confirmAction("Approve this page?", "It may go live. Your site's webhook gets it now, with everything it needs to put it in place.", "Approve"))) return;
    setBusy("approve");
    const { data, error } = await api.PATCH("/v1/brands/{brand}/content/{id}", { params: { path: { brand: brandId, id: c.id } }, body: { status: "approved" } });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (data) {
      toast("Approved");
      show(c.id);
      load();
    }
  }

  async function markLive(c: ContentItem) {
    const r = await ask({
      title: "Where did it go live?",
      body: "The page's address on your site. Outflare marks it live and starts measuring it.",
      input: { label: "Address", placeholder: brand?.website ? `${brand.website.replace(/\/$/, "")}/blog/${c.slug || "a-post"}` : "https://", maxLength: 2000 },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Mark live", value: "ok" }],
    });
    if (r?.choice !== "ok" || !r.value) return;
    setBusy("live");
    const { data, error } = await api.POST("/v1/brands/{brand}/content/{id}/delivered", { params: { path: { brand: brandId, id: c.id } }, body: { url: r.value.trim(), revision: c.revision } });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (data) {
      toast("Marked live");
      setOpen(data);
      load();
    }
  }

  async function copy(c: ContentItem, format: "markdown" | "html") {
    try {
      await navigator.clipboard.writeText(format === "html" ? (c.html ?? "") : markdownFile(c));
      track("ui.content_copied", { format });
      toast(format === "html" ? "HTML copied" : "Markdown copied");
    } catch {
      setError("Your browser didn't allow copying. Use Download instead.");
    }
  }

  function download(c: ContentItem) {
    const url = URL.createObjectURL(new Blob([markdownFile(c)], { type: "text/markdown" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `${c.slug || "content"}.md`;
    a.click();
    URL.revokeObjectURL(url);
    track("ui.content_downloaded", { format: "markdown" });
  }

  if (!brandId) return <PickBrand what="Posts and pages" brands={active.length} />;

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Content"
        description="Posts and pages written for your website. Once approved, a page goes to your site through its webhook or CMS connection, or you can copy or download it and put it in place yourself."
        action={<select className={field} value={status} onChange={(e) => setStatus(e.target.value)} aria-label="Status">{statuses.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}</select>}
      />
      <ErrorText>{error}</ErrorText>
      {open ? (
        <Card
          title={open.title}
          description={<span className="flex flex-wrap items-center gap-2"><Badge tone={statusTone[open.status]}>{statusLabel[open.status]}</Badge>{open.revision > 0 && <span>Revision {open.revision}</span>}{open.language && <span>{open.language}</span>}{open.placement.path && <span className="font-mono">{open.placement.path}</span>}{open.seo.primary_keyword && <span>Keyword: {open.seo.primary_keyword}</span>}</span>}
          action={<Button variant="ghost" onClick={() => setOpen(null)}>Back to the list</Button>}
        >
          <div className="flex flex-col gap-4">
            <div className="flex flex-wrap gap-2">
              {canManage && open.status === "draft" && <Button disabled={busy === "approve"} onClick={() => approve(open)}>Approve</Button>}
              <Button variant="ghost" onClick={() => copy(open, "markdown")}><Copy className="size-4" />Copy Markdown</Button>
              <Button variant="ghost" onClick={() => copy(open, "html")}><Copy className="size-4" />Copy HTML</Button>
              <Button variant="ghost" onClick={() => download(open)}><Download className="size-4" />Download .md</Button>
              {canManage && (open.status === "approved" || open.status === "published") && <Button variant="ghost" disabled={busy === "live"} onClick={() => markLive(open)}>{open.status === "published" ? "Change live address" : "Mark live"}</Button>}
            </div>
            {open.url && <p className="text-[13px]">Live at <a className="underline" href={open.url} target="_blank" rel="noopener noreferrer">{open.url}</a></p>}
            {open.brief.check_before_live?.length ? (
              <div className="rounded-md border border-amber-300 bg-amber-50 p-3 text-[13px] dark:border-amber-800 dark:bg-amber-950/40">
                <div className="font-medium">Check before it goes live</div>
                <ul className="mt-1 list-disc pl-5">{open.brief.check_before_live.map((c) => <li key={c}>{c}</li>)}</ul>
              </div>
            ) : null}
            <pre className="max-h-[60vh] overflow-auto whitespace-pre-wrap rounded-md border bg-muted/40 p-3 font-mono text-[12px]">{open.body}</pre>
            <p className="text-[12px] text-muted-foreground">To send approved pages to your site automatically, add a webhook for content events. <button type="button" className="underline" onClick={() => go("webhooks")}>Webhooks</button></p>
          </div>
        </Card>
      ) : items === null ? null : items.length === 0 ? (
        <Empty title={status ? "Nothing here" : "No content yet"}>Ask the agent to write a post, or create one with the API or MCP (create_content).</Empty>
      ) : (
        <div className="divide-y rounded-lg border">
          {items.map((c) => (
            <button key={c.id} type="button" onClick={() => show(c.id)} className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-accent">
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium">{c.title}</span>
                <span className="block truncate text-[12px] text-muted-foreground">{c.placement.path || c.slug || (c.target === "pages" ? "Page" : "Post")}{c.seo.primary_keyword ? ` · ${c.seo.primary_keyword}` : ""}</span>
              </span>
              <Badge tone={statusTone[c.status]}>{statusLabel[c.status]}</Badge>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
