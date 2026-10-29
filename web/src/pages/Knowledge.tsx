import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import type { KnowledgeCollection, KnowledgeItem, KnowledgeVersion, Member } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, toast } from "../ui";
import { Icon } from "../icons";
import { ago, plural } from "../format";
import { cn } from "@/lib/utils";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { BrandBadge } from "@/components/brand-badge";
import { Markdown, MarkdownEditor } from "@/components/markdown-editor";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";

// Knowledge: what the business knows, written once, for its team and for
// every AI in Outflare. Articles and quick answers live in folders; each
// says who may see it. The page is a library (folders on the left, search
// and the list on the right) and an editor that opens in its place, with
// history and a bin so nothing is lost by mistake.

type Kind = KnowledgeItem["kind"];
type Visibility = KnowledgeItem["visibility"];
type Section = { kind: "all" } | { kind: "snippet" } | { kind: "article" } | { kind: "bin" } | { kind: "folder"; id: string } | { kind: "loose" };

export const visibilityInfo: Record<Visibility, { label: string; help: string; tone: "green" | "blue" | "grey" }> = {
  public: { label: "Public", help: "On your help centre. AI may tell customers.", tone: "green" },
  customers_ai: { label: "Customers via AI", help: "AI may use it to answer customers. Not published.", tone: "blue" },
  team: { label: "Team only", help: "Your team and AI working for them. Never told to a customer.", tone: "grey" },
};

// Starters: a blank page is the hardest part, so the common articles come
// with their headings filled in.
const starters: { id: string; kind: Kind; title: string; body: string; blurb: string }[] = [
  {
    id: "product",
    kind: "article",
    title: "What we sell",
    blurb: "Your product, who it's for and why it's better",
    body: "## What it is\n\n\n## Who it's for\n\n- \n\n## Why customers choose us\n\n- \n\n## What it doesn't do\n\n- \n",
  },
  {
    id: "pricing",
    kind: "article",
    title: "Pricing and plans",
    blurb: "Plans, prices, trials and discounts",
    body: "## Plans\n\n| Plan | Price | Includes |\n| --- | --- | --- |\n|  |  |  |\n\n## Free trial\n\n\n## Discounts\n\n- \n",
  },
  {
    id: "refunds",
    kind: "article",
    title: "Refunds and cancellations",
    blurb: "What you refund, and how people cancel",
    body: "## Refunds\n\n\n## How to cancel\n\n1. \n\n## Exceptions\n\n- \n",
  },
  {
    id: "objections",
    kind: "article",
    title: "Handling objections",
    blurb: "What prospects say, and how to answer",
    body: "## \"It's too expensive\"\n\n\n## \"We already use a competitor\"\n\n\n## \"Not the right time\"\n\n",
  },
  { id: "faq", kind: "snippet", title: "", blurb: "One question customers ask, and its answer", body: "" },
];

export function Knowledge({ ws, canManage, open, setOpen }: { ws: string; canManage: boolean; open?: string; setOpen: (id?: string) => void }) {
  const { brand, query: brandQuery } = useBrand(ws);
  const [folders, setFolders] = useState<KnowledgeCollection[] | null>(null);
  const [section, setSection] = useState<Section>({ kind: "all" });
  const [q, setQ] = useState("");
  const [items, setItems] = useState<KnowledgeItem[] | null>(null);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState("");
  // What a new item starts from, while the editor is open on "new".
  const [draft, setDraft] = useState<{ kind: Kind; title: string; body: string; folder: string | null }>({ kind: "article", title: "", body: "", folder: null });

  const loadFolders = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/knowledge/collections", { params: { path: { ws }, query: brandQuery } });
    if (error) return setError(errorMessage(error));
    setFolders(data.data);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand?.id]);

  const loadItems = useCallback(async () => {
    const query: Record<string, string | number | boolean> = { ...brandQuery, limit: 200 };
    if (q.trim()) query.q = q.trim();
    if (section.kind === "snippet" || section.kind === "article") query.kind = section.kind;
    if (section.kind === "bin") query.deleted = true;
    if (section.kind === "folder") query.collection_id = section.id;
    if (section.kind === "loose") query.collection_id = "none";
    const { data, error } = await api.GET("/v1/workspaces/{ws}/knowledge/items", { params: { path: { ws }, query } });
    if (error) return setError(errorMessage(error));
    setError("");
    setItems(data.data);
    setTotal(data.total);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, q, section, brand?.id]);

  useEffect(() => void loadFolders(), [loadFolders]);
  useEffect(() => {
    const t = setTimeout(loadItems, q ? 200 : 0);
    return () => clearTimeout(t);
  }, [loadItems, q]);

  const currentFolder = section.kind === "folder" ? section.id : null;

  function start(kind: Kind, starter?: (typeof starters)[number]) {
    track("ui.knowledge_started", { kind, template: starter?.id ?? "none" });
    setDraft({ kind, title: starter?.title ?? "", body: starter?.body ?? "", folder: currentFolder });
    setOpen("new");
  }

  async function newFolder(parent: string | null) {
    const r = await ask({
      title: parent ? "New folder inside this one" : "New folder",
      input: { label: "Name", placeholder: "Pricing", maxLength: 100 },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Create", value: "ok", variant: "primary" }],
    });
    if (r?.choice !== "ok" || !r.value.trim()) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/knowledge/collections", {
      params: { path: { ws } },
      body: { name: r.value.trim(), parent_id: parent, ...(brand && !parent ? { brand_id: brand.id } : {}) },
    });
    if (error) return toast(errorMessage(error), "error");
    await loadFolders();
    setSection({ kind: "folder", id: data.id });
  }

  async function renameFolder(f: KnowledgeCollection) {
    const r = await ask({
      title: "Rename folder",
      input: { label: "Name", value: f.name, maxLength: 100 },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok", variant: "primary" }],
    });
    if (r?.choice !== "ok" || !r.value.trim() || r.value.trim() === f.name) return;
    const { error } = await api.PATCH("/v1/workspaces/{ws}/knowledge/collections/{id}", { params: { path: { ws, id: f.id } }, body: { name: r.value.trim() } });
    if (error) return toast(errorMessage(error), "error");
    loadFolders();
  }

  async function moveFolder(f: KnowledgeCollection, parent: string | null) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/knowledge/collections/{id}", { params: { path: { ws, id: f.id } }, body: { parent_id: parent } });
    if (error) return toast(errorMessage(error), "error");
    loadFolders();
  }

  async function deleteFolder(f: KnowledgeCollection) {
    const parent = folders?.find((x) => x.id === f.parent_id);
    const ok = await confirmAction(
      `Delete the folder “${f.name}”?`,
      <>Nothing in it is deleted: what it holds moves to {parent ? <>“{parent.name}”</> : "the top level"}.</>,
      "Delete folder",
    );
    if (!ok) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/knowledge/collections/{id}", { params: { path: { ws, id: f.id } } });
    if (error) return toast(errorMessage(error), "error");
    if (currentFolder === f.id) setSection(parent ? { kind: "folder", id: parent.id } : { kind: "all" });
    loadFolders();
    loadItems();
  }

  async function moveItem(item: KnowledgeItem, folder: string | null) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/knowledge/items/{id}", { params: { path: { ws, id: item.id } }, body: { collection_id: folder } });
    if (error) return toast(errorMessage(error), "error");
    toast(folder ? `Moved to “${folders?.find((f) => f.id === folder)?.name}”.` : "Moved out of its folder.");
    loadItems();
    loadFolders();
  }

  async function binItem(item: KnowledgeItem) {
    const { error } = await api.DELETE("/v1/workspaces/{ws}/knowledge/items/{id}", { params: { path: { ws, id: item.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Moved to the bin. You can restore it for 30 days.");
    loadItems();
    loadFolders();
  }

  async function restore(item: KnowledgeItem) {
    const { error } = await api.POST("/v1/workspaces/{ws}/knowledge/items/{id}/restore", { params: { path: { ws, id: item.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Restored.");
    loadItems();
    loadFolders();
  }

  async function purge(item: KnowledgeItem) {
    if (!(await confirmAction(`Delete “${item.title}” for good?`, "It can't be brought back.", "Delete for good"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/knowledge/items/{id}", { params: { path: { ws, id: item.id }, query: { permanent: true } } });
    if (error) return toast(errorMessage(error), "error");
    loadItems();
  }

  if (open) {
    return (
      <Editor
        key={open}
        ws={ws}
        id={open === "new" ? null : open}
        draft={draft}
        folders={folders ?? []}
        canManage={canManage}
        onClose={() => {
          setOpen(undefined);
          loadItems();
          loadFolders();
        }}
        onCreated={(id) => setOpen(id)}
      />
    );
  }

  const tree = folderTree(folders ?? []);
  const heading =
    section.kind === "folder" ? folders?.find((f) => f.id === section.id)?.name ?? "Folder"
    : section.kind === "bin" ? "Bin"
    : section.kind === "snippet" ? "Quick answers"
    : section.kind === "article" ? "Articles"
    : section.kind === "loose" ? "Not in a folder"
    : "All knowledge";

  return (
    <div className="space-y-5">
      <PageHeader
        title="Knowledge"
        description="What your business knows, written once: products, prices, policies and stock answers. Your team reads it, and every AI in Outflare answers from it."
        action={
          canManage && (
            <div className="flex gap-2">
              <Button variant="ghost" onClick={() => start("snippet")}>Quick answer</Button>
              <Button onClick={() => start("article")}><Icon name="plus" className="size-3.5" /> Write an article</Button>
            </div>
          )
        }
      />

      <div className="grid gap-5 md:grid-cols-[220px_minmax(0,1fr)]">
        <nav aria-label="Knowledge sections" className="space-y-4 text-[13px]">
          <div className="space-y-0.5">
            <NavItem active={section.kind === "all"} onClick={() => setSection({ kind: "all" })}>All knowledge</NavItem>
            <NavItem active={section.kind === "article"} onClick={() => setSection({ kind: "article" })}>Articles</NavItem>
            <NavItem active={section.kind === "snippet"} onClick={() => setSection({ kind: "snippet" })}>Quick answers</NavItem>
          </div>
          <div>
            <div className="mb-1 flex items-center justify-between px-2">
              <span className="font-mono text-[11px] uppercase text-muted-foreground">Folders</span>
              {canManage && (
                <button type="button" aria-label="New folder" title="New folder" onClick={() => newFolder(null)} className="rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground">
                  <Icon name="plus" className="size-3.5" />
                </button>
              )}
            </div>
            {folders === null ? null : tree.length === 0 ? (
              <p className="px-2 text-xs text-muted-foreground">Group knowledge in folders, such as Pricing or Policies.</p>
            ) : (
              <ul className="space-y-0.5">
                {tree.map(({ folder: f, depth }) => (
                  <li key={f.id} className="group flex items-center" style={{ paddingLeft: depth * 12 }}>
                    <NavItem active={currentFolder === f.id} onClick={() => setSection({ kind: "folder", id: f.id })}>
                      <span className="min-w-0 flex-1 truncate">{f.name}</span>
                      {f.item_count > 0 && <span className="text-[11px] text-muted-foreground">{f.item_count}</span>}
                    </NavItem>
                    {canManage && (
                      <div className="opacity-0 group-hover:opacity-100 focus-within:opacity-100">
                        <Menu
                          aria={`Actions for folder ${f.name}`}
                          items={[
                            { label: "Rename", onClick: () => renameFolder(f) },
                            depth < 4 && { label: "New folder inside", onClick: () => newFolder(f.id) },
                            f.parent_id !== null && { label: "Move to the top level", onClick: () => moveFolder(f, null) },
                            { label: "Delete folder", onClick: () => deleteFolder(f), danger: true },
                          ]}
                        />
                      </div>
                    )}
                  </li>
                ))}
                <li>
                  <NavItem active={section.kind === "loose"} onClick={() => setSection({ kind: "loose" })}>
                    <span className="text-muted-foreground">Not in a folder</span>
                  </NavItem>
                </li>
              </ul>
            )}
          </div>
          <div className="space-y-0.5 border-t pt-3">
            <NavItem active={section.kind === "bin"} onClick={() => setSection({ kind: "bin" })}>
              <span className="flex-1">Bin</span>
            </NavItem>
          </div>
        </nav>

        <section className="min-w-0 space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="mr-auto text-sm font-semibold">
              {heading} {items && <span className="font-normal text-muted-foreground">{total}</span>}
            </h2>
            <div className="relative w-full sm:w-72">
              <Icon name="search" className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <input
                aria-label="Search knowledge"
                className={cn(field, "w-full pl-8")}
                value={q}
                onChange={(e) => setQ(e.target.value)}
                placeholder="Search titles and text"
              />
            </div>
          </div>
          {section.kind === "bin" && <p className="text-xs text-muted-foreground">Deleted knowledge stays here for 30 days, then it is deleted for good.</p>}
          <ErrorText>{error}</ErrorText>

          {items === null ? (
            <Loading />
          ) : items.length === 0 ? (
            q ? (
              <Empty title="Nothing matches">Try other words, or look in All knowledge.</Empty>
            ) : section.kind === "bin" ? (
              <Empty title="The bin is empty">Deleted knowledge waits here for 30 days.</Empty>
            ) : section.kind === "all" && canManage ? (
              <GetStarted onStart={start} />
            ) : (
              <Empty
                title={section.kind === "snippet" ? "No quick answers yet" : "Nothing here yet"}
                action={canManage && <Button onClick={() => start(section.kind === "snippet" ? "snippet" : "article")}>{section.kind === "snippet" ? "Add a quick answer" : "Write an article"}</Button>}
              >
                {section.kind === "snippet" ? "A question customers or your team ask, and the answer to give." : "Write it here, or move knowledge in from another folder."}
              </Empty>
            )
          ) : (
            <ul className="divide-y divide-border rounded-lg border">
              {items.map((it) => (
                <li key={it.id} className="flex items-start gap-3 px-3 py-2.5 hover:bg-accent/40">
                  <button type="button" className="min-w-0 flex-1 text-left" onClick={() => (section.kind === "bin" ? undefined : setOpen(it.id))} disabled={section.kind === "bin"}>
                    <p className="flex items-center gap-2 text-sm font-medium">
                      {it.kind === "snippet" && <Badge tone="blue" title="Quick answer">Q&A</Badge>}
                      <span className="truncate">{it.title}</span>
                    </p>
                    {it.excerpt && <p className="mt-0.5 line-clamp-2 text-[13px] text-muted-foreground">{it.excerpt}</p>}
                    <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                      <Badge tone={visibilityInfo[it.visibility].tone}>{visibilityInfo[it.visibility].label}</Badge>
                      {it.status === "draft" && <Badge tone="amber">Draft</Badge>}
                      {reviewDue(it) && <Badge tone="amber" title={`Check by ${it.review_by}`}>Check due</Badge>}
                      <BrandBadge id={it.brand_id} />
                      {section.kind !== "folder" && it.collection_id && <span>{folders?.find((f) => f.id === it.collection_id)?.name}</span>}
                      <span>{it.deleted_at ? `Deleted ${ago(it.deleted_at)}` : `Updated ${ago(it.updated_at)}${it.updated_by ? ` by ${it.updated_by}` : ""}`}</span>
                    </p>
                  </button>
                  {canManage && section.kind === "bin" && (
                    <div className="flex gap-1">
                      <Button variant="ghost" onClick={() => restore(it)}>Restore</Button>
                      <Button variant="ghost" onClick={() => purge(it)}>Delete for good</Button>
                    </div>
                  )}
                  {canManage && section.kind !== "bin" && (
                    <Menu
                      aria={`Actions for ${it.title}`}
                      items={[
                        { label: "Edit", onClick: () => setOpen(it.id) },
                        ...tree.filter(({ folder: f }) => f.id !== it.collection_id && (!f.brand_id || f.brand_id === it.brand_id)).slice(0, 12).map(({ folder: f }) => ({ label: `Move to ${f.name}`, onClick: () => moveItem(it, f.id) })),
                        it.collection_id !== null && { label: "Move out of its folder", onClick: () => moveItem(it, null) },
                        { label: "Delete", onClick: () => binItem(it), danger: true },
                      ]}
                    />
                  )}
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
    </div>
  );
}

function NavItem({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-current={active ? "page" : undefined}
      onClick={onClick}
      className={cn("flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-1 text-left", active ? "bg-accent font-medium text-foreground" : "text-muted-foreground hover:bg-accent/60 hover:text-foreground")}
    >
      {children}
    </button>
  );
}

function GetStarted({ onStart }: { onStart: (kind: Kind, starter?: (typeof starters)[number]) => void }) {
  return (
    <div className="rounded-lg border border-dashed p-5">
      <h3 className="text-sm font-semibold">Start with what people ask most</h3>
      <p className="mt-1 text-[13px] text-muted-foreground">Write it once here, and your team, your agents and (when you allow it) your customers get the same answer. Pick one to start; the headings are filled in.</p>
      <div className="mt-4 grid gap-2 sm:grid-cols-2">
        {starters.map((s) => (
          <button key={s.id} type="button" onClick={() => onStart(s.kind, s)} className="rounded-lg border px-3 py-2.5 text-left hover:border-ring/60 hover:bg-accent/40">
            <span className="block text-[13px] font-medium">{s.kind === "snippet" ? "A quick answer" : s.title}</span>
            <span className="block text-xs text-muted-foreground">{s.blurb}</span>
          </button>
        ))}
        <button type="button" onClick={() => onStart("article")} className="rounded-lg border px-3 py-2.5 text-left hover:border-ring/60 hover:bg-accent/40">
          <span className="block text-[13px] font-medium">A blank article</span>
          <span className="block text-xs text-muted-foreground">Anything else worth writing down</span>
        </button>
      </div>
      <p className="mt-4 text-xs text-muted-foreground">Agents in Claude, ChatGPT or Cursor can add to it too: “Write down our refund policy: …”.</p>
    </div>
  );
}

// folderTree orders folders as a tree, each with its depth.
function folderTree(list: KnowledgeCollection[]): { folder: KnowledgeCollection; depth: number }[] {
  const out: { folder: KnowledgeCollection; depth: number }[] = [];
  const ids = new Set(list.map((f) => f.id));
  const walk = (parent: string | null, depth: number) => {
    list
      .filter((f) => (parent === null ? f.parent_id === null || !ids.has(f.parent_id) : f.parent_id === parent))
      .sort((a, b) => a.position - b.position || a.name.localeCompare(b.name))
      .forEach((f) => {
        out.push({ folder: f, depth });
        if (depth < 10) walk(f.id, depth + 1);
      });
  };
  walk(null, 0);
  return out;
}

const today = () => new Date().toISOString().slice(0, 10);
const reviewDue = (it: Pick<KnowledgeItem, "review_by">) => !!it.review_by && it.review_by <= today();

function monthsFromNow(n: number) {
  const d = new Date();
  d.setMonth(d.getMonth() + n);
  return d.toISOString().slice(0, 10);
}

type Form = {
  title: string;
  body: string;
  visibility: Visibility;
  status: KnowledgeItem["status"];
  collection_id: string | null;
  brand_id: string | null;
  owner_id: string | null;
  review_by: string | null;
};

function Editor({
  ws,
  id,
  draft,
  folders,
  canManage,
  onClose,
  onCreated,
}: {
  ws: string;
  id: string | null;
  draft: { kind: Kind; title: string; body: string; folder: string | null };
  folders: KnowledgeCollection[];
  canManage: boolean;
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const { brand, active: brands, byId } = useBrand(ws);
  const [item, setItem] = useState<KnowledgeItem | null>(null);
  const [form, setForm] = useState<Form | null>(null);
  const [saved, setSaved] = useState("");
  const [members, setMembers] = useState<Member[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [history, setHistory] = useState(false);

  useEffect(() => {
    void (async () => {
      if (!id) {
        const folder = folders.find((f) => f.id === draft.folder);
        const f: Form = {
          title: draft.title,
          body: draft.body,
          visibility: "team",
          status: "published",
          collection_id: draft.folder,
          brand_id: folder?.brand_id ?? brand?.id ?? null,
          owner_id: null,
          review_by: null,
        };
        setForm(f);
        setSaved(JSON.stringify(f));
        return;
      }
      const { data, error } = await api.GET("/v1/workspaces/{ws}/knowledge/items/{id}", { params: { path: { ws, id } } });
      if (error) return setError(errorMessage(error));
      setItem(data);
      const f = toForm(data);
      setForm(f);
      setSaved(JSON.stringify(f));
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, id]);

  useEffect(() => {
    // Owners are members; someone who can't list members just doesn't pick one.
    void api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) => data?.data && setMembers(data.data));
  }, [ws]);

  const dirty = !!form && JSON.stringify(form) !== saved;
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const kind: Kind = item?.kind ?? draft.kind;
  const snippet = kind === "snippet";
  const tree = useMemo(() => folderTree(folders), [folders]);

  if (error && !form) return <ErrorText>{error}</ErrorText>;
  if (!form) return <Loading />;
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setForm((f) => (f ? { ...f, [k]: v } : f));
  const ro = !canManage;

  async function save() {
    if (!form || busy) return;
    if (!form.title.trim()) return setError(snippet ? "Write the question first." : "Give it a title first.");
    if (snippet && !form.body.trim()) return setError("Write the answer too.");
    setBusy(true);
    setError("");
    const body = { ...form, title: form.title.trim(), ...(id ? {} : { kind }), ...(form.owner_id === null && !id ? { owner_id: undefined } : {}) };
    const res = id
      ? await api.PATCH("/v1/workspaces/{ws}/knowledge/items/{id}", { params: { path: { ws, id } }, body })
      : await api.POST("/v1/workspaces/{ws}/knowledge/items", { params: { path: { ws } }, body });
    setBusy(false);
    if (res.error || !res.data) return setError(errorMessage(res.error));
    const data = res.data as KnowledgeItem;
    toast(id ? "Saved." : snippet ? "Quick answer saved." : "Article saved.");
    if (!id) return onCreated(data.id);
    setItem(data);
    const f = toForm(data);
    setForm(f);
    setSaved(JSON.stringify(f));
  }

  async function close() {
    if (dirty && !(await confirmAction("Leave without saving?", "Your changes will be lost.", "Leave"))) return;
    onClose();
  }

  async function remove() {
    if (!item) return;
    if (!(await confirmAction(`Delete “${item.title}”?`, "It goes to the bin, where you can restore it for 30 days.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/knowledge/items/{id}", { params: { path: { ws, id: item.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Moved to the bin.");
    onClose();
  }

  const folderBrand = form.collection_id ? folders.find((f) => f.id === form.collection_id)?.brand_id ?? null : null;

  return (
    <div className="space-y-5">
      <PageHeader
        back={{ label: "Knowledge", onClick: close }}
        title={id ? (snippet ? "Quick answer" : "Article") : snippet ? "New quick answer" : "New article"}
        description={item ? <>Version {item.version}, updated {ago(item.updated_at)}{item.updated_by ? ` by ${item.updated_by}` : ""}.</> : snippet ? "A question your customers or team ask, and the answer to give." : "Write it the way you'd explain it to a new teammate."}
        action={
          <div className="flex items-center gap-2">
            {dirty && <span className="text-xs text-amber-700 dark:text-amber-400">Unsaved changes</span>}
            {item && <Button variant="ghost" onClick={() => setHistory(true)}>History</Button>}
            {item && canManage && <Menu aria="More actions" items={[{ label: "Delete", onClick: remove, danger: true }]} />}
            {canManage && <Button onClick={save} disabled={busy || (!!id && !dirty)}>{busy ? "Saving…" : "Save"}</Button>}
          </div>
        }
      />
      <ErrorText>{error}</ErrorText>

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_280px]">
        <div className="min-w-0 space-y-3">
          <label className="block">
            <span className="sr-only">{snippet ? "Question" : "Title"}</span>
            {snippet && <span className="mb-1 block text-[13px] font-medium">Question</span>}
            <input
              autoFocus={!id}
              value={form.title}
              readOnly={ro}
              maxLength={300}
              onChange={(e) => set("title", e.target.value)}
              onKeyDown={(e) => {
                if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
                  e.preventDefault();
                  void save();
                }
              }}
              placeholder={snippet ? "Do you offer a free trial?" : "Title"}
              className={cn("w-full border-0 bg-transparent px-0 outline-none placeholder:text-muted-foreground/60", snippet ? cn(field, "px-2.5 text-[14px]") : "text-2xl font-semibold")}
            />
          </label>
          {snippet && <span className="block text-[13px] font-medium">Answer</span>}
          <MarkdownEditor
            label={snippet ? "Answer" : "Text"}
            value={form.body}
            onChange={(v) => set("body", v)}
            readOnly={ro}
            minRows={snippet ? 5 : 16}
            onSave={save}
            placeholder={snippet ? "Yes: every plan has a 7-day free trial, no card needed." : "Write, or paste from Google Docs or Word. Type / for headings, lists and tables."}
          />
        </div>

        <aside className="space-y-5 text-[13px]">
          <fieldset disabled={ro} className="space-y-1.5">
            <legend className="mb-1.5 font-medium">Who can see it</legend>
            {(Object.keys(visibilityInfo) as Visibility[]).map((v) => (
              <label key={v} className={cn("flex cursor-pointer items-start gap-2 rounded-md border px-2.5 py-2", form.visibility === v ? "border-ring bg-accent/50" : "hover:bg-accent/30")}>
                <input type="radio" name="visibility" className="mt-0.5" checked={form.visibility === v} onChange={() => set("visibility", v)} />
                <span>
                  <span className="block font-medium">{visibilityInfo[v].label}</span>
                  <span className="block text-xs text-muted-foreground">{visibilityInfo[v].help}</span>
                </span>
              </label>
            ))}
          </fieldset>

          <fieldset disabled={ro}>
            <legend className="mb-1.5 font-medium">Status</legend>
            <div className="flex rounded-md border p-0.5" role="group">
              {(["published", "draft"] as const).map((s) => (
                <button key={s} type="button" aria-pressed={form.status === s} onClick={() => set("status", s)} className={cn("flex-1 rounded px-2 py-1", form.status === s ? "bg-accent font-medium" : "text-muted-foreground")}>
                  {s === "published" ? "Published" : "Draft"}
                </button>
              ))}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">{form.status === "draft" ? "Only people editing it see a draft. AI doesn't use it yet." : "Your team and AI use it now."}</p>
          </fieldset>

          <label className="block">
            <span className="mb-1.5 block font-medium">Folder</span>
            <select className={cn(field, "w-full")} disabled={ro} value={form.collection_id ?? ""} onChange={(e) => {
              const next = e.target.value || null;
              const fb = next ? folders.find((f) => f.id === next)?.brand_id ?? null : null;
              setForm((f) => (f ? { ...f, collection_id: next, ...(fb ? { brand_id: fb } : {}) } : f));
            }}>
              <option value="">No folder</option>
              {tree.map(({ folder: f, depth }) => (
                <option key={f.id} value={f.id} disabled={!!f.brand_id && !!form.brand_id && f.brand_id !== form.brand_id && !!id}>
                  {"  ".repeat(depth)}{f.name}{f.brand_id ? ` (${byId(f.brand_id)?.name ?? "a brand"})` : ""}
                </option>
              ))}
            </select>
          </label>

          {brands.length > 0 && (
            <label className="block">
              <span className="mb-1.5 block font-medium">Brand</span>
              <select className={cn(field, "w-full")} disabled={ro || !!folderBrand} value={form.brand_id ?? ""} onChange={(e) => set("brand_id", e.target.value || null)}>
                <option value="">Every brand</option>
                {brands.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
              </select>
              {folderBrand && <span className="mt-1 block text-xs text-muted-foreground">Set by its folder.</span>}
            </label>
          )}

          {members.length > 0 && (
            <label className="block">
              <span className="mb-1.5 block font-medium">Owner</span>
              <select className={cn(field, "w-full")} disabled={ro} value={form.owner_id ?? (id ? "" : "me")} onChange={(e) => set("owner_id", e.target.value === "me" ? null : e.target.value || null)}>
                {!id && <option value="me">Me</option>}
                {id && <option value="">Nobody</option>}
                {members.map((m) => <option key={m.user_id} value={m.user_id}>{m.name || m.email}</option>)}
              </select>
              <span className="mt-1 block text-xs text-muted-foreground">Who keeps it right.</span>
            </label>
          )}

          <div>
            <label className="block">
              <span className="mb-1.5 block font-medium">Check by</span>
              <input type="date" className={cn(field, "w-full")} disabled={ro} value={form.review_by ?? ""} onChange={(e) => set("review_by", e.target.value || null)} />
            </label>
            {!ro && (
              <div className="mt-1.5 flex flex-wrap gap-1.5 text-xs">
                {[3, 6, 12].map((n) => (
                  <button key={n} type="button" className="rounded border px-1.5 py-0.5 text-muted-foreground hover:text-foreground" onClick={() => set("review_by", monthsFromNow(n))}>
                    In {n} months
                  </button>
                ))}
                {form.review_by && <button type="button" className="rounded px-1.5 py-0.5 text-muted-foreground hover:text-foreground" onClick={() => set("review_by", null)}>Clear</button>}
              </div>
            )}
            {reviewDue(form) && <p className="mt-1 text-xs text-amber-700 dark:text-amber-400">Due: read it through, then set the next date.</p>}
          </div>
        </aside>
      </div>

      {item && history && (
        <History
          ws={ws}
          item={item}
          canManage={canManage}
          onClose={() => setHistory(false)}
          onRestored={(data) => {
            setItem(data);
            const f = toForm(data);
            setForm(f);
            setSaved(JSON.stringify(f));
            setHistory(false);
          }}
        />
      )}
    </div>
  );
}

function toForm(it: KnowledgeItem): Form {
  return {
    title: it.title,
    body: it.body ?? "",
    visibility: it.visibility,
    status: it.status,
    collection_id: it.collection_id,
    brand_id: it.brand_id,
    owner_id: it.owner_id,
    review_by: it.review_by,
  };
}

function History({ ws, item, canManage, onClose, onRestored }: { ws: string; item: KnowledgeItem; canManage: boolean; onClose: () => void; onRestored: (it: KnowledgeItem) => void }) {
  const [versions, setVersions] = useState<KnowledgeVersion[] | null>(null);
  const [shown, setShown] = useState<number | null>(null);
  useEffect(() => {
    void api.GET("/v1/workspaces/{ws}/knowledge/items/{id}/versions", { params: { path: { ws, id: item.id } } }).then(({ data, error }) => {
      if (error) return toast(errorMessage(error), "error");
      setVersions(data.data);
      setShown(data.data[1]?.version ?? data.data[0]?.version ?? null);
    });
  }, [ws, item.id]);

  async function restore(v: KnowledgeVersion) {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/knowledge/items/{id}/versions/{version}/restore", { params: { path: { ws, id: item.id, version: v.version } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Version ${v.version} is back, saved as version ${data.version}.`);
    onRestored(data);
  }

  const v = versions?.find((x) => x.version === shown);
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>History</SheetTitle>
          <SheetDescription>Every saved change to the title and text. Restoring one saves it as a new version, so you can always go back.</SheetDescription>
        </SheetHeader>
        {!versions ? (
          <Loading />
        ) : (
          <div className="grid gap-4 px-4 pb-6 sm:grid-cols-[160px_minmax(0,1fr)]">
            <ul className="space-y-0.5 text-[13px]">
              {versions.map((x) => (
                <li key={x.version}>
                  <button type="button" onClick={() => setShown(x.version)} className={cn("w-full rounded-md px-2 py-1.5 text-left", x.version === shown ? "bg-accent" : "hover:bg-accent/50")}>
                    <span className="block font-medium">Version {x.version}{x.version === item.version ? " (now)" : ""}</span>
                    <span className="block text-xs text-muted-foreground">{ago(x.created_at)}{x.created_by ? `, ${x.created_by}` : ""}</span>
                  </button>
                </li>
              ))}
              {versions.length >= 100 && <li className="px-2 text-xs text-muted-foreground">The last {plural(100, "version")}.</li>}
            </ul>
            {v && (
              <div className="min-w-0 space-y-3">
                <div className="flex items-center justify-between gap-2">
                  <h3 className="truncate text-base font-semibold">{v.title}</h3>
                  {canManage && v.version !== item.version && <Button onClick={() => restore(v)}>Restore this version</Button>}
                </div>
                <div className="rounded-lg border px-4 py-3"><Markdown text={v.body || "_No text._"} /></div>
              </div>
            )}
          </div>
        )}
      </SheetContent>
    </Sheet>
  );
}
