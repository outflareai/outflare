import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import type { KnowledgeCollection, KnowledgeGap, KnowledgeItem, KnowledgeStats, KnowledgeVersion, Member } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Button, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, Status, toast } from "../ui";
import { Icon, type IconName } from "../icons";
import { ago, plural } from "../format";
import { cn } from "@/lib/utils";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { BrandBadge } from "@/components/brand-badge";
import { Markdown, MarkdownEditor } from "@/components/markdown-editor";
import { KnowledgeAsk } from "@/components/knowledge-ask";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";

// Knowledge: what the business knows, written once, for its team and for
// every AI in Outflare. Articles and quick answers live in folders; each
// says who may use it. The page has an overview (what to add, where it is
// used, how it is answering), the content as a table that opens an item
// beside it, the questions it couldn't answer, and an editor that opens in
// its place, with history and a bin so nothing is lost by mistake.

type Kind = KnowledgeItem["kind"];
type Visibility = KnowledgeItem["visibility"];

// Starters: a blank page is the hardest part, so the common articles come
// with their headings filled in.
const starters: { id: string; kind: Kind; title: string; body: string; blurb: string }[] = [
  {
    id: "product",
    kind: "article",
    title: "What we sell",
    blurb: "Your product, who it's for and why it's better",
    body: "## What it is\n\n_In two or three sentences, the way you'd tell a new teammate._\n\n## Who it's for\n\n- _The kind of company or person_\n- _The problem they have_\n\n## Why customers choose us\n\n- _A reason, with proof if you have it_\n\n## What it doesn't do\n\n- _So nobody promises it_\n",
  },
  {
    id: "pricing",
    kind: "article",
    title: "Pricing and plans",
    blurb: "Plans, prices, trials and discounts",
    body: "## Plans\n\n| Plan | Price | Includes |\n| --- | --- | --- |\n| _Name_ | _Price_ | _What it includes_ |\n\n## Free trial\n\n_How long it lasts, and whether a card is needed._\n\n## Discounts\n\n- _Which discounts exist, and who may give them_\n",
  },
  {
    id: "refunds",
    kind: "article",
    title: "Refunds and cancellations",
    blurb: "What you refund, and how people cancel",
    body: "## Refunds\n\n_What you refund, and within how long._\n\n## How to cancel\n\n1. _First step_\n2. _Next step_\n\n## Exceptions\n\n- _Anything that works differently_\n",
  },
  {
    id: "objections",
    kind: "article",
    title: "Handling objections",
    blurb: "What prospects say, and how to answer",
    body: "## \"It's too expensive\"\n\n_How to answer._\n\n## \"We already use a competitor\"\n\n_How to answer._\n\n## \"Not the right time\"\n\n_How to answer._\n",
  },
  { id: "faq", kind: "snippet", title: "", blurb: "One question customers ask, and its answer", body: "" },
];

type View =
  | { kind: "overview" }
  | { kind: "content"; type?: Kind; folder?: string; status?: StatusFilter; use?: UseFilter }
  | { kind: "gaps" }
  | { kind: "bin" };
type StatusFilter = "" | "published" | "draft" | "due";
type UseFilter = "" | "customers" | "help_centre" | "team";

export function Knowledge({ ws, canManage, open, setOpen, go }: { ws: string; canManage: boolean; open?: string; setOpen: (id?: string) => void; go?: (tab: "ai") => void }) {
  const { brand, query: brandQuery } = useBrand(ws);
  const [folders, setFolders] = useState<KnowledgeCollection[] | null>(null);
  const [stats, setStats] = useState<KnowledgeStats | null>(null);
  const [view, setView] = useState<View>({ kind: "overview" });
  const [peek, setPeek] = useState<string | null>(null);
  const [testing, setTesting] = useState(false);
  // What a new item starts from, while the editor is open on "new"; gap is
  // the unanswered question it answers, set aside once it is saved.
  const [draft, setDraft] = useState<Draft>({ kind: "article", title: "", body: "", folder: null });
  const [tick, setTick] = useState(0);

  const loadFolders = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/knowledge/collections", { params: { path: { ws }, query: brandQuery } });
    if (data) setFolders(data.data);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand?.id]);
  const loadStats = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/knowledge/stats", { params: { path: { ws }, query: brandQuery } });
    if (data) setStats(data);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand?.id]);
  useEffect(() => void loadFolders(), [loadFolders]);
  useEffect(() => void loadStats(), [loadStats, tick]);
  // refresh reloads whatever shows counts after a change.
  const refresh = () => {
    setTick((t) => t + 1);
    void loadFolders();
  };

  const currentFolder = view.kind === "content" && view.folder && view.folder !== "none" ? view.folder : null;

  function start(kind: Kind, starter?: Starter, question?: string) {
    track("ui.knowledge_started", { kind, template: starter?.id ?? (question ? "question" : "none") });
    setDraft({ kind, title: question ?? starter?.title ?? "", body: starter?.body ?? "", folder: currentFolder, gap: question });
    setTesting(false);
    setPeek(null);
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
    refresh();
    setView({ kind: "content", folder: data.id });
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
    if (currentFolder === f.id) setView(parent ? { kind: "content", folder: parent.id } : { kind: "content" });
    refresh();
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
          refresh();
        }}
        onCreated={async (id) => {
          if (draft.gap) {
            await api.POST("/v1/workspaces/{ws}/knowledge/gaps/dismiss", { params: { path: { ws } }, body: { question: draft.gap, reason: "answered" } });
            setDraft((d) => ({ ...d, gap: undefined }));
          }
          setOpen(id);
        }}
      />
    );
  }

  const tree = folderTree(folders ?? []);
  const isContent = (f: (v: Extract<View, { kind: "content" }>) => boolean) => view.kind === "content" && f(view);

  return (
    <div className="space-y-5">
      <PageHeader
        title="Knowledge"
        description="What your business knows, written once. Your team reads it, and every AI in Outflare answers from it: agents, missions and, when you allow it, replies to customers."
        action={
          <div className="flex gap-2">
            <Button variant="ghost" onClick={() => setTesting(true)}><Icon name="ai" className="size-3.5" /> Test an answer</Button>
            {canManage && <NewMenu onStart={start} />}
          </div>
        }
      />

      <div className="grid gap-6 md:grid-cols-[210px_minmax(0,1fr)]">
        <nav aria-label="Knowledge sections" className="space-y-4 text-[13px]">
          <div className="space-y-0.5">
            <NavItem active={view.kind === "overview"} onClick={() => setView({ kind: "overview" })}>Overview</NavItem>
            <NavItem active={view.kind === "gaps"} onClick={() => setView({ kind: "gaps" })}>
              <span className="flex-1">Unanswered questions</span>
              {!!stats?.gaps && <span className="rounded-full bg-amber-500/15 px-1.5 text-[11px] font-medium text-amber-700 dark:text-amber-400">{stats.gaps}</span>}
            </NavItem>
          </div>
          <div>
            <p className="mb-1 px-2 font-mono text-[11px] text-muted-foreground uppercase">Content</p>
            <div className="space-y-0.5">
              <NavItem active={isContent((v) => !v.type && !v.folder)} onClick={() => setView({ kind: "content" })}>
                <span className="flex-1">All content</span>
                <Count n={stats?.items} />
              </NavItem>
              <NavItem active={isContent((v) => v.type === "article")} onClick={() => setView({ kind: "content", type: "article" })}>
                <Icon name="article" className="size-3.5 opacity-60" /><span className="flex-1">Articles</span>
                <Count n={stats?.articles} />
              </NavItem>
              <NavItem active={isContent((v) => v.type === "snippet")} onClick={() => setView({ kind: "content", type: "snippet" })}>
                <Icon name="answer" className="size-3.5 opacity-60" /><span className="flex-1">Quick answers</span>
                <Count n={stats?.snippets} />
              </NavItem>
            </div>
          </div>
          <div>
            <div className="mb-1 flex items-center justify-between px-2">
              <span className="font-mono text-[11px] text-muted-foreground uppercase">Folders</span>
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
                    <NavItem active={currentFolder === f.id} onClick={() => setView({ kind: "content", folder: f.id })}>
                      <span className="min-w-0 flex-1 truncate">{f.name}</span>
                      <Count n={f.item_count || undefined} />
                    </NavItem>
                    {canManage && (
                      <div className="opacity-0 group-hover:opacity-100 focus-within:opacity-100">
                        <Menu
                          label=""
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
                  <NavItem active={isContent((v) => v.folder === "none")} onClick={() => setView({ kind: "content", folder: "none" })}>
                    <span className="text-muted-foreground">Not in a folder</span>
                  </NavItem>
                </li>
              </ul>
            )}
          </div>
          <div className="space-y-0.5 border-t pt-3">
            <NavItem active={view.kind === "bin"} onClick={() => setView({ kind: "bin" })}>
              <span className="flex-1">Bin</span>
              <Count n={stats?.bin || undefined} />
            </NavItem>
          </div>
        </nav>

        <div className="min-w-0">
          {view.kind === "overview" ? (
            <Overview ws={ws} stats={stats} canManage={canManage} tick={tick} onStart={start} onView={setView} onOpen={setPeek} onTest={() => setTesting(true)} />
          ) : view.kind === "gaps" ? (
            <Gaps ws={ws} canManage={canManage} tick={tick} onWrite={(q) => start("snippet", undefined, q)} onChanged={refresh} onTest={() => setTesting(true)} />
          ) : (
            <Content
              key={view.kind === "bin" ? "bin" : `${view.type}-${view.folder}`}
              ws={ws}
              view={view}
              folders={folders ?? []}
              canManage={canManage}
              tick={tick}
              selected={peek}
              onView={setView}
              onOpen={setPeek}
              onStart={start}
              onChanged={refresh}
            />
          )}
        </div>
      </div>

      {peek && (
        <Peek
          key={peek}
          ws={ws}
          id={peek}
          folders={folders ?? []}
          canManage={canManage}
          onClose={() => setPeek(null)}
          onEdit={() => {
            setOpen(peek);
            setPeek(null);
          }}
          onChanged={refresh}
        />
      )}

      <Sheet open={testing} onOpenChange={setTesting}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
          <SheetHeader>
            <SheetTitle>Test an answer</SheetTitle>
            <SheetDescription>Ask what a teammate or a customer would ask, and see the answer AI gives from your knowledge, with the articles it used.</SheetDescription>
          </SheetHeader>
          <div className="px-4 pb-6">
            <KnowledgeAsk
              ws={ws}
              canManage={canManage}
              onOpen={(id) => {
                setTesting(false);
                setPeek(id);
              }}
              onWrite={(question) => start("snippet", undefined, question)}
              onConnectAI={() => go?.("ai")}
            />
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}

type Starter = (typeof starters)[number];
type Draft = { kind: Kind; title: string; body: string; folder: string | null; gap?: string };

function Count({ n }: { n?: number }) {
  return n === undefined ? null : <span className="text-[11px] text-muted-foreground tabular-nums">{n}</span>;
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

function NewMenu({ onStart }: { onStart: (kind: Kind, starter?: Starter) => void }) {
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button><Icon name="plus" className="size-3.5" /> New</Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72">
        <DropdownMenuItem onClick={() => onStart("article")} className="items-start">
          <Icon name="article" className="mt-0.5 size-4 opacity-60" />
          <span><span className="block font-medium">Article</span><span className="block text-xs text-muted-foreground">A guide, policy or process, with headings</span></span>
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onStart("snippet")} className="items-start">
          <Icon name="answer" className="mt-0.5 size-4 opacity-60" />
          <span><span className="block font-medium">Quick answer</span><span className="block text-xs text-muted-foreground">One question and the answer to give</span></span>
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <p className="px-2 py-1 text-[11px] text-muted-foreground">Start from a template</p>
        {starters.filter((s) => s.kind === "article").map((s) => (
          <DropdownMenuItem key={s.id} onClick={() => onStart(s.kind, s)}>{s.title}</DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// Overview: what to add, where the knowledge goes, how it is answering and
// what needs a look, the way a support team checks its knowledge base.
function Overview({
  ws,
  stats,
  canManage,
  tick,
  onStart,
  onView,
  onOpen,
  onTest,
}: {
  ws: string;
  stats: KnowledgeStats | null;
  canManage: boolean;
  tick: number;
  onStart: (kind: Kind, starter?: Starter, question?: string) => void;
  onView: (v: View) => void;
  onOpen: (id: string) => void;
  onTest: () => void;
}) {
  const { query: brandQuery, brand } = useBrand(ws);
  const [recent, setRecent] = useState<KnowledgeItem[] | null>(null);
  const [gaps, setGaps] = useState<KnowledgeGap[] | null>(null);
  useEffect(() => {
    void api.GET("/v1/workspaces/{ws}/knowledge/items", { params: { path: { ws }, query: { ...brandQuery, limit: 5 } } }).then(({ data }) => data && setRecent(data.data));
    void api.GET("/v1/workspaces/{ws}/knowledge/gaps", { params: { path: { ws }, query: brandQuery } }).then(({ data }) => data && setGaps(data.data.slice(0, 3)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand?.id, tick]);

  if (!stats) return <Loading />;
  const empty = stats.items === 0;
  const rate = stats.questions_30d ? Math.round((stats.answered_30d / stats.questions_30d) * 100) : null;
  return (
    <div className="space-y-7">
      {canManage && (
        <section className="space-y-2.5">
          <SectionTitle title={empty ? "Start with what people ask most" : "Add content"} hint={empty ? "Write it once here, and your team, your agents and (when you allow it) your customers get the same answer." : undefined} />
          <div className="grid gap-2.5 sm:grid-cols-3">
            <AddCard icon="article" title="Article" text="A guide, policy or process. Paste from Google Docs or Word." onClick={() => onStart("article")} />
            <AddCard icon="answer" title="Quick answer" text="One question customers or your team ask, and the answer." onClick={() => onStart("snippet")} />
            <AddCard icon="templates" title="From a template" text="Pricing, refunds, objections: the headings filled in." onClick={() => onStart("article", starters[1])} />
          </div>
          {empty && (
            <div className="flex flex-wrap gap-1.5 pt-1">
              {starters.filter((s) => s.kind === "article").map((s) => (
                <button key={s.id} type="button" onClick={() => onStart(s.kind, s)} className="rounded-full border px-2.5 py-1 text-xs text-muted-foreground hover:border-ring/60 hover:text-foreground">
                  {s.title}
                </button>
              ))}
            </div>
          )}
        </section>
      )}

      <section className="space-y-2.5">
        <SectionTitle title="Where your knowledge is used" hint="Every live item reaches your team. You choose, item by item, what customers may hear." />
        <div className="grid gap-2.5 md:grid-cols-3">
          <UseCard
            icon="members"
            title="Your team and agents"
            n={stats.team_ai}
            text="The Outflare agent, missions, and agents over MCP answer from every live item."
            action={{ label: "View", onClick: () => onView({ kind: "content", status: "published" }) }}
          />
          <UseCard
            icon="ai"
            title="AI talking to customers"
            n={stats.customers_ai}
            text="Replies and answers to customers use only the items you turn on for customers."
            action={{ label: "View", onClick: () => onView({ kind: "content", use: "customers" }) }}
          />
          <UseCard
            icon="domains"
            title="Help centre"
            n={stats.help_centre}
            text="Public articles, for customers to read themselves."
            action={{ label: "View", onClick: () => onView({ kind: "content", use: "help_centre" }) }}
          />
        </div>
      </section>

      <section className="space-y-2.5">
        <SectionTitle title="How it's answering" hint="Questions asked of your knowledge in the last 30 days, by people and by AI." />
        <div className="grid gap-2.5 md:grid-cols-[repeat(3,minmax(0,1fr))]">
          <Stat label="Questions asked" value={stats.questions_30d} />
          <Stat label="Answered from your knowledge" value={rate === null ? "–" : `${rate}%`} sub={rate === null ? "No questions yet" : `${stats.answered_30d} of ${stats.questions_30d}`} />
          <Stat
            label="Unanswered questions"
            value={stats.gaps}
            tone={stats.gaps ? "amber" : undefined}
            sub={stats.gaps ? "Write these next" : "Nothing missing"}
            onClick={() => onView({ kind: "gaps" })}
          />
        </div>
        {gaps && gaps.length > 0 && (
          <ul className="divide-y rounded-lg border text-[13px]">
            {gaps.map((g) => (
              <li key={g.question} className="flex items-center gap-3 px-3 py-2">
                <span className="min-w-0 flex-1 truncate">{g.question}</span>
                <span className="text-xs text-muted-foreground">Asked {plural(g.asked, "time")}</span>
                {canManage && <Button variant="ghost" onClick={() => onStart("snippet", undefined, g.question)}>Write the answer</Button>}
              </li>
            ))}
          </ul>
        )}
        {stats.questions_30d === 0 && (
          <p className="text-xs text-muted-foreground">
            Nothing asked yet. <button type="button" className="underline underline-offset-2 hover:text-foreground" onClick={onTest}>Test an answer</button> to see what AI says from your knowledge.
          </p>
        )}
      </section>

      {(stats.drafts > 0 || stats.check_due > 0) && (
        <section className="space-y-2.5">
          <SectionTitle title="Needs a look" />
          <div className="flex flex-wrap gap-2.5">
            {stats.drafts > 0 && <Attention n={stats.drafts} label={stats.drafts === 1 ? "draft" : "drafts"} text="Not used by AI until it's live" onClick={() => onView({ kind: "content", status: "draft" })} />}
            {stats.check_due > 0 && <Attention n={stats.check_due} label="due a check" text="Read through, then set the next date" onClick={() => onView({ kind: "content", status: "due" })} />}
          </div>
        </section>
      )}

      {recent && recent.length > 0 && (
        <section className="space-y-2.5">
          <SectionTitle title="Recently updated" action={<button type="button" className="text-xs text-muted-foreground hover:text-foreground" onClick={() => onView({ kind: "content" })}>View all</button>} />
          <ul className="divide-y rounded-lg border">
            {recent.map((it) => (
              <li key={it.id}>
                <button type="button" onClick={() => onOpen(it.id)} className="flex w-full items-center gap-3 px-3 py-2 text-left text-[13px] hover:bg-accent/40">
                  <KindIcon kind={it.kind} />
                  <span className="min-w-0 flex-1 truncate font-medium">{it.title}</span>
                  <StatusLabel it={it} />
                  <span className="w-28 text-right text-xs text-muted-foreground">{ago(it.updated_at)}</span>
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

function SectionTitle({ title, hint, action }: { title: string; hint?: string; action?: ReactNode }) {
  return (
    <div className="flex items-end justify-between gap-2">
      <div>
        <h2 className="text-sm font-semibold">{title}</h2>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      </div>
      {action}
    </div>
  );
}

function AddCard({ icon, title, text, onClick }: { icon: IconName; title: string; text: string; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className="group flex items-start gap-3 rounded-lg border bg-card p-3.5 text-left hover:border-ring/60 hover:bg-accent/40">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-background"><Icon name={icon} className="size-4" /></span>
      <span>
        <span className="flex items-center gap-1 text-[13px] font-medium">{title} <Icon name="plus" className="size-3 opacity-0 group-hover:opacity-60" /></span>
        <span className="block text-xs text-muted-foreground">{text}</span>
      </span>
    </button>
  );
}

function UseCard({ icon, title, n, text, action }: { icon: IconName; title: string; n: number; text: string; action: { label: string; onClick: () => void } }) {
  return (
    <div className="flex flex-col rounded-lg border bg-card p-3.5">
      <div className="flex items-center gap-2 text-[13px] font-medium">
        <Icon name={icon} className="size-4 text-muted-foreground" /> {title}
      </div>
      <p className="mt-2 text-2xl font-semibold tabular-nums">{n} <span className="text-xs font-normal text-muted-foreground">{n === 1 ? "item" : "items"}</span></p>
      <p className="mt-1 flex-1 text-xs text-muted-foreground">{text}</p>
      <button type="button" onClick={action.onClick} className="mt-2.5 self-start text-xs font-medium hover:underline">{action.label} →</button>
    </div>
  );
}

function Stat({ label, value, sub, tone, onClick }: { label: string; value: ReactNode; sub?: string; tone?: "amber"; onClick?: () => void }) {
  const Tag = onClick ? "button" : "div";
  return (
    <Tag type={onClick ? "button" : undefined} onClick={onClick} className={cn("rounded-lg border bg-card p-3.5 text-left", onClick && "hover:border-ring/60 hover:bg-accent/40")}>
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className={cn("mt-1 text-2xl font-semibold tabular-nums", tone === "amber" && value ? "text-amber-700 dark:text-amber-400" : "")}>{value}</p>
      {sub && <p className="text-xs text-muted-foreground">{sub}</p>}
    </Tag>
  );
}

function Attention({ n, label, text, onClick }: { n: number; label: string; text: string; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className="flex items-center gap-3 rounded-lg border px-3 py-2 text-left hover:border-ring/60 hover:bg-accent/40">
      <span className="text-lg font-semibold tabular-nums">{n}</span>
      <span>
        <span className="block text-[13px] font-medium">{label}</span>
        <span className="block text-xs text-muted-foreground">{text}</span>
      </span>
    </button>
  );
}

function KindIcon({ kind }: { kind: Kind }) {
  return <Icon name={kind === "snippet" ? "reddit" : "book"} className="size-3.5 shrink-0 text-muted-foreground" aria-label={kind === "snippet" ? "Quick answer" : "Article"} />;
}

function StatusLabel({ it }: { it: Pick<KnowledgeItem, "status" | "review_by"> }) {
  if (it.status === "draft") return <Status tone="grey">Draft</Status>;
  if (reviewDue(it)) return <Status tone="amber">Check due</Status>;
  return <Status tone="green">Live</Status>;
}

// Content: every item in a dense table, filtered by kind, folder, status
// and who may use it; a row opens it beside the list.
function Content({
  ws,
  view,
  folders,
  canManage,
  tick,
  selected,
  onView,
  onOpen,
  onStart,
  onChanged,
}: {
  ws: string;
  view: Extract<View, { kind: "content" | "bin" }>;
  folders: KnowledgeCollection[];
  canManage: boolean;
  tick: number;
  selected: string | null;
  onView: (v: View) => void;
  onOpen: (id: string) => void;
  onStart: (kind: Kind, starter?: Starter) => void;
  onChanged: () => void;
}) {
  const { brand, query: brandQuery } = useBrand(ws);
  const bin = view.kind === "bin";
  const filters: Omit<Extract<View, { kind: "content" }>, "kind"> = view.kind === "content" ? view : {};
  const [q, setQ] = useState("");
  const [status, setStatus] = useState<StatusFilter>(filters.status ?? "");
  const [use, setUse] = useState<UseFilter>(filters.use ?? "");
  const [items, setItems] = useState<KnowledgeItem[] | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const query: Record<string, string | number | boolean> = { ...brandQuery, limit: 200 };
    if (q.trim()) query.q = q.trim();
    if (filters.type) query.kind = filters.type;
    if (filters.folder) query.collection_id = filters.folder;
    if (bin) query.deleted = true;
    if (status === "published" || status === "draft") query.status = status;
    const { data, error } = await api.GET("/v1/workspaces/{ws}/knowledge/items", { params: { path: { ws }, query } });
    if (error) return setError(errorMessage(error));
    setError("");
    setItems(data.data);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, q, status, brand?.id, tick]);
  useEffect(() => {
    const t = setTimeout(load, q ? 200 : 0);
    return () => clearTimeout(t);
  }, [load, q]);

  const shown = (items ?? []).filter(
    (it) =>
      (status !== "due" || reviewDue(it)) &&
      (use === "" ||
        (use === "customers" && it.visibility !== "team") ||
        (use === "help_centre" && it.visibility === "public") ||
        (use === "team" && it.visibility === "team")),
  );
  const folderName = (id: string | null) => (id ? folders.find((f) => f.id === id)?.name : undefined);
  const heading = bin
    ? "Bin"
    : filters.folder === "none" ? "Not in a folder"
    : filters.folder ? folderName(filters.folder) ?? "Folder"
    : filters.type === "snippet" ? "Quick answers"
    : filters.type === "article" ? "Articles"
    : "All content";

  async function restore(item: KnowledgeItem) {
    const { error } = await api.POST("/v1/workspaces/{ws}/knowledge/items/{id}/restore", { params: { path: { ws, id: item.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Restored.");
    onChanged();
  }

  async function purge(item: KnowledgeItem) {
    if (!(await confirmAction(`Delete “${item.title}” for good?`, "It can't be brought back.", "Delete for good"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/knowledge/items/{id}", { params: { path: { ws, id: item.id }, query: { permanent: true } } });
    if (error) return toast(errorMessage(error), "error");
    onChanged();
  }

  const filtered = !!q || !!status || !!use;
  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="mr-auto text-sm font-semibold">
          {heading} {items && <span className="font-normal text-muted-foreground">{shown.length}</span>}
        </h2>
        {canManage && !bin && filters.folder && filters.folder !== "none" && (
          <Button variant="ghost" onClick={() => onStart(filters.type ?? "article")}><Icon name="plus" className="size-3.5" /> Add here</Button>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-full sm:w-64">
          <Icon name="search" className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <input aria-label="Search knowledge" className={cn(field, "w-full pl-8")} value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search titles and text" />
        </div>
        {!bin && (
          <>
            <select aria-label="Status" className={field} value={status} onChange={(e) => setStatus(e.target.value as StatusFilter)}>
              <option value="">Any status</option>
              <option value="published">Live</option>
              <option value="draft">Draft</option>
              <option value="due">Check due</option>
            </select>
            <select aria-label="Used by" className={field} value={use} onChange={(e) => setUse(e.target.value as UseFilter)}>
              <option value="">Used by anyone</option>
              <option value="customers">AI talking to customers</option>
              <option value="help_centre">Help centre</option>
              <option value="team">Team only</option>
            </select>
            {filtered && (
              <button type="button" className="text-xs text-muted-foreground hover:text-foreground" onClick={() => { setQ(""); setStatus(""); setUse(""); }}>
                Clear
              </button>
            )}
          </>
        )}
      </div>
      {bin && <p className="text-xs text-muted-foreground">Deleted knowledge stays here for 30 days, then it is deleted for good.</p>}
      <ErrorText>{error}</ErrorText>

      {items === null ? (
        <Loading />
      ) : shown.length === 0 ? (
        filtered ? (
          <Empty title="Nothing matches" action={<Button variant="ghost" onClick={() => { setQ(""); setStatus(""); setUse(""); }}>Clear filters</Button>}>Try other words or filters.</Empty>
        ) : bin ? (
          <Empty title="The bin is empty">Deleted knowledge waits here for 30 days.</Empty>
        ) : (
          <Empty
            title={filters.type === "snippet" ? "No quick answers yet" : "Nothing here yet"}
            action={canManage && <Button onClick={() => onStart(filters.type ?? "article")}>{filters.type === "snippet" ? "Add a quick answer" : "Write an article"}</Button>}
          >
            {filters.type === "snippet" ? "A question customers or your team ask, and the answer to give." : "Write it here, or move knowledge in from another folder."}
          </Empty>
        )
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-[13px]">
            <thead className="border-b bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="w-full px-3 py-2 font-medium">Title</th>
                {bin ? (
                  <>
                    <th className="px-3 py-2 font-medium">Deleted</th>
                    <th className="px-3 py-2" />
                  </>
                ) : (
                  <>
                    <th className="px-3 py-2 font-medium">Status</th>
                    <th className="px-3 py-2 text-center font-medium" title="AI may use it when talking to customers">Customers</th>
                    <th className="px-3 py-2 text-center font-medium" title="Published on your help centre">Help centre</th>
                    {!filters.folder && <th className="hidden px-3 py-2 font-medium lg:table-cell">Folder</th>}
                    <th className="px-3 py-2 text-right font-medium" title="AI answers that used it in the last 30 days">Used</th>
                    <th className="hidden px-3 py-2 font-medium xl:table-cell">Updated</th>
                  </>
                )}
              </tr>
            </thead>
            <tbody className="divide-y">
              {shown.map((it) => (
                <tr
                  key={it.id}
                  onClick={bin ? undefined : () => onOpen(it.id)}
                  className={cn(!bin && "cursor-pointer hover:bg-accent/40", selected === it.id && "bg-accent/60")}
                >
                  <td className="max-w-0 px-3 py-2">
                    <span className="flex items-center gap-2">
                      <KindIcon kind={it.kind} />
                      {bin ? (
                        <span className="truncate font-medium">{it.title}</span>
                      ) : (
                        <button type="button" className="truncate text-left font-medium" onClick={(e) => { e.stopPropagation(); onOpen(it.id); }}>{it.title}</button>
                      )}
                      <BrandBadge id={it.brand_id} />
                    </span>
                  </td>
                  {bin ? (
                    <>
                      <td className="px-3 py-2 whitespace-nowrap text-muted-foreground">{it.deleted_at && ago(it.deleted_at)}</td>
                      <td className="px-3 py-1.5 text-right whitespace-nowrap">
                        {canManage && (
                          <>
                            <Button variant="ghost" onClick={() => restore(it)}>Restore</Button>{" "}
                            <Button variant="danger" onClick={() => purge(it)}>Delete for good</Button>
                          </>
                        )}
                      </td>
                    </>
                  ) : (
                    <>
                      <td className="px-3 py-2"><StatusLabel it={it} /></td>
                      <td className="px-3 py-2 text-center"><Tick on={it.visibility !== "team"} /></td>
                      <td className="px-3 py-2 text-center"><Tick on={it.visibility === "public"} /></td>
                      {!filters.folder && <td className="hidden max-w-40 truncate px-3 py-2 text-muted-foreground lg:table-cell">{folderName(it.collection_id) ?? "–"}</td>}
                      <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">{it.uses_30d || "–"}</td>
                      <td className="hidden px-3 py-2 whitespace-nowrap text-muted-foreground xl:table-cell">{ago(it.updated_at)}</td>
                    </>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {!bin && items && items.length > 0 && filters.folder === undefined && !filtered && (
        <p className="text-xs text-muted-foreground">
          Tip: <button type="button" className="underline underline-offset-2 hover:text-foreground" onClick={() => onView({ kind: "gaps" })}>unanswered questions</button> show what to write next.
        </p>
      )}
    </section>
  );
}

function Tick({ on }: { on: boolean }) {
  return on ? (
    <span role="img" aria-label="Yes" className="inline-flex text-green-600 dark:text-green-400"><Icon name="check" className="size-3.5" /></span>
  ) : (
    <span role="img" aria-label="No" className="text-muted-foreground/50">–</span>
  );
}

// Unanswered questions: what people and AI asked that the knowledge
// didn't answer, the most asked first, each one click from its answer.
function Gaps({ ws, canManage, tick, onWrite, onChanged, onTest }: { ws: string; canManage: boolean; tick: number; onWrite: (q: string) => void; onChanged: () => void; onTest: () => void }) {
  const { brand, query: brandQuery } = useBrand(ws);
  const [gaps, setGaps] = useState<KnowledgeGap[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    void api.GET("/v1/workspaces/{ws}/knowledge/gaps", { params: { path: { ws }, query: brandQuery } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      setGaps(data.data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand?.id, tick]);

  async function dismiss(g: KnowledgeGap) {
    const { error } = await api.POST("/v1/workspaces/{ws}/knowledge/gaps/dismiss", { params: { path: { ws } }, body: { question: g.question, reason: "ignored" } });
    if (error) return toast(errorMessage(error), "error");
    setGaps((list) => list?.filter((x) => x !== g) ?? null);
    toast("Dismissed. It comes back if someone asks again.");
    onChanged();
  }

  return (
    <section className="space-y-3">
      <SectionTitle
        title="Unanswered questions"
        hint="Questions from the last 90 days that nothing in your knowledge answered. Write the answer once, and every AI answers it from then on."
      />
      <ErrorText>{error}</ErrorText>
      {gaps === null ? (
        <Loading />
      ) : gaps.length === 0 ? (
        <Empty title="Nothing unanswered" action={<Button variant="ghost" onClick={onTest}>Test an answer</Button>}>
          When a question asked of your knowledge finds no answer, it shows here.
        </Empty>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-[13px]">
            <thead className="border-b bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="w-full px-3 py-2 font-medium">Question</th>
                <th className="px-3 py-2 text-right font-medium">Asked</th>
                <th className="px-3 py-2 text-right font-medium">By customers</th>
                <th className="hidden px-3 py-2 font-medium md:table-cell">Last asked</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y">
              {gaps.map((g) => (
                <tr key={g.question}>
                  <td className="max-w-0 px-3 py-2"><span className="block truncate font-medium">{g.question}</span></td>
                  <td className="px-3 py-2 text-right tabular-nums">{g.asked}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">{g.customers || "–"}</td>
                  <td className="hidden px-3 py-2 whitespace-nowrap text-muted-foreground md:table-cell">{ago(g.last_asked)}</td>
                  <td className="px-3 py-1.5 text-right whitespace-nowrap">
                    {canManage && (
                      <>
                        <Button onClick={() => onWrite(g.question)}>Write the answer</Button>{" "}
                        <Button variant="ghost" onClick={() => dismiss(g)}>Dismiss</Button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

// Who may use an item, as switches: the team always (while it's live),
// AI talking to customers, and the help centre (which implies customers).
// They map onto the item's visibility.
function UseSwitches({ visibility, disabled, onChange }: { visibility: Visibility; disabled?: boolean; onChange: (v: Visibility) => void }) {
  const customers = visibility !== "team";
  const help = visibility === "public";
  const row = (title: string, text: string, control: ReactNode) => (
    <div className="flex items-start justify-between gap-3 py-2">
      <span>
        <span className="block font-medium">{title}</span>
        <span className="block text-xs text-muted-foreground">{text}</span>
      </span>
      {control}
    </div>
  );
  return (
    <div className="divide-y">
      {row("Your team and agents", "Always, while it's live.", <Switch checked disabled aria-label="Your team and agents" />)}
      {row(
        "AI talking to customers",
        "Replies and answers to customers may use it.",
        <Switch checked={customers} disabled={disabled} aria-label="AI talking to customers" onCheckedChange={(on) => onChange(on ? "customers_ai" : "team")} />,
      )}
      {row(
        "Help centre",
        "Customers can read it themselves.",
        <Switch checked={help} disabled={disabled} aria-label="Help centre" onCheckedChange={(on) => onChange(on ? "public" : "customers_ai")} />,
      )}
    </div>
  );
}

// Peek: an item beside the list, to read it and change its settings in
// place (each change saves at once), with its use and history; Edit opens
// the full editor for the text.
function Peek({
  ws,
  id,
  folders,
  canManage,
  onClose,
  onEdit,
  onChanged,
}: {
  ws: string;
  id: string;
  folders: KnowledgeCollection[];
  canManage: boolean;
  onClose: () => void;
  onEdit: () => void;
  onChanged: () => void;
}) {
  const { active: brands, byId } = useBrand(ws);
  const [item, setItem] = useState<KnowledgeItem | null>(null);
  const [members, setMembers] = useState<Member[]>([]);
  const [error, setError] = useState("");
  const [history, setHistory] = useState(false);
  const tree = useMemo(() => folderTree(folders), [folders]);

  useEffect(() => {
    void api.GET("/v1/workspaces/{ws}/knowledge/items/{id}", { params: { path: { ws, id } } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      setItem(data);
    });
    void api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) => data?.data && setMembers(data.data));
  }, [ws, id]);

  async function patch(body: Partial<Form>, done = "Saved.") {
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/knowledge/items/{id}", { params: { path: { ws, id } }, body });
    if (error) return toast(errorMessage(error), "error");
    setItem(data);
    toast(done);
    onChanged();
  }

  async function remove() {
    if (!item) return;
    if (!(await confirmAction(`Delete “${item.title}”?`, "It goes to the bin, where you can restore it for 30 days.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/knowledge/items/{id}", { params: { path: { ws, id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Moved to the bin. You can restore it for 30 days.");
    onChanged();
    onClose();
  }

  const ro = !canManage;
  const words = item?.body ? item.body.split(/\s+/).filter(Boolean).length : 0;
  const folderBrand = item?.collection_id ? folders.find((f) => f.id === item.collection_id)?.brand_id ?? null : null;
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full gap-0 overflow-y-auto sm:max-w-4xl">
        <SheetHeader className="border-b">
          <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
            {item && <KindIcon kind={item.kind} />} {item?.kind === "snippet" ? "Quick answer" : "Article"}
          </p>
          <SheetTitle className="pr-8 text-lg">{item?.title ?? "…"}</SheetTitle>
          <SheetDescription className="sr-only">Read it and change who may use it.</SheetDescription>
          {item && (
            <div className="flex flex-wrap items-center gap-2 pt-1">
              {canManage && <Button onClick={onEdit}>Edit</Button>}
              <Button variant="ghost" onClick={() => setHistory(true)}>History</Button>
              {canManage && <Menu aria="More actions" items={[{ label: "Delete", onClick: remove, danger: true }]} />}
            </div>
          )}
        </SheetHeader>
        <ErrorText>{error}</ErrorText>
        {!item ? (
          <div className="p-4"><Loading /></div>
        ) : (
          <div className="grid flex-1 md:grid-cols-[minmax(0,1fr)_300px]">
            <article className="min-w-0 px-5 py-4">
              {item.kind === "snippet" && <p className="mb-2 text-xs font-medium text-muted-foreground">Answer</p>}
              {item.body ? <Markdown text={item.body} /> : <p className="text-[13px] text-muted-foreground">No text yet.</p>}
            </article>
            <aside className="space-y-5 border-t px-4 py-4 text-[13px] md:border-t-0 md:border-l">
              <div>
                <h3 className="mb-1.5 text-xs font-medium text-muted-foreground">Status</h3>
                <div className="flex rounded-md border p-0.5" role="group" aria-label="Status">
                  {(["published", "draft"] as const).map((s) => (
                    <button key={s} type="button" disabled={ro} aria-pressed={item.status === s} onClick={() => item.status !== s && patch({ status: s }, s === "published" ? "Live: your team and AI use it now." : "Back to draft: AI stops using it.")} className={cn("flex-1 rounded px-2 py-1", item.status === s ? "bg-accent font-medium" : "text-muted-foreground")}>
                      {s === "published" ? "Live" : "Draft"}
                    </button>
                  ))}
                </div>
              </div>

              <div>
                <h3 className="text-xs font-medium text-muted-foreground">Who uses it</h3>
                <UseSwitches visibility={item.visibility} disabled={ro} onChange={(v) => patch({ visibility: v })} />
              </div>

              <div className="space-y-3">
                <h3 className="text-xs font-medium text-muted-foreground">Details</h3>
                <label className="block">
                  <span className="mb-1 block">Folder</span>
                  <select className={cn(field, "w-full")} disabled={ro} value={item.collection_id ?? ""} onChange={(e) => patch({ collection_id: e.target.value || null })}>
                    <option value="">No folder</option>
                    {tree.map(({ folder: f, depth }) => (
                      <option key={f.id} value={f.id} disabled={!!f.brand_id && !!item.brand_id && f.brand_id !== item.brand_id}>{"  ".repeat(depth)}{f.name}</option>
                    ))}
                  </select>
                </label>
                {brands.length > 0 && (
                  <label className="block">
                    <span className="mb-1 block">Brand</span>
                    <select className={cn(field, "w-full")} disabled={ro || !!folderBrand} value={item.brand_id ?? ""} onChange={(e) => patch({ brand_id: e.target.value || null })}>
                      <option value="">Every brand</option>
                      {brands.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
                    </select>
                    {folderBrand && <span className="mt-1 block text-xs text-muted-foreground">Set by its folder ({byId(folderBrand)?.name}).</span>}
                  </label>
                )}
                {members.length > 0 && (
                  <label className="block">
                    <span className="mb-1 block">Owner</span>
                    <select className={cn(field, "w-full")} disabled={ro} value={item.owner_id ?? ""} onChange={(e) => patch({ owner_id: e.target.value || null })}>
                      <option value="">Nobody</option>
                      {members.map((m) => <option key={m.user_id} value={m.user_id}>{m.name || m.email}</option>)}
                    </select>
                  </label>
                )}
                <label className="block">
                  <span className="mb-1 block">Check by</span>
                  <input type="date" className={cn(field, "w-full")} disabled={ro} value={item.review_by ?? ""} onChange={(e) => patch({ review_by: e.target.value || null })} />
                  {reviewDue(item) && <span className="mt-1 block text-xs text-amber-700 dark:text-amber-400">Due: read it through, then set the next date.</span>}
                </label>
              </div>

              <div>
                <h3 className="mb-1 text-xs font-medium text-muted-foreground">Last 30 days</h3>
                <p><span className="text-lg font-semibold tabular-nums">{item.uses_30d}</span> <span className="text-muted-foreground">{item.uses_30d === 1 ? "answer used it" : "answers used it"}</span></p>
              </div>

              <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
                <dt className="text-muted-foreground">Created</dt>
                <dd>{ago(item.created_at)}{item.created_by ? ` by ${item.created_by}` : ""}</dd>
                <dt className="text-muted-foreground">Updated</dt>
                <dd>{ago(item.updated_at)}{item.updated_by ? ` by ${item.updated_by}` : ""}</dd>
                <dt className="text-muted-foreground">Version</dt>
                <dd>{item.version}</dd>
                <dt className="text-muted-foreground">Length</dt>
                <dd>{plural(words, "word")}</dd>
              </dl>
            </aside>
          </div>
        )}
        {item && history && (
          <History
            ws={ws}
            item={item}
            canManage={canManage}
            onClose={() => setHistory(false)}
            onRestored={(data) => {
              setItem(data);
              setHistory(false);
              onChanged();
            }}
          />
        )}
      </SheetContent>
    </Sheet>
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
          <div>
            <p className="font-medium">Who uses it</p>
            <UseSwitches visibility={form.visibility} disabled={ro} onChange={(v) => set("visibility", v)} />
          </div>

          <fieldset disabled={ro}>
            <legend className="mb-1.5 font-medium">Status</legend>
            <div className="flex rounded-md border p-0.5" role="group">
              {(["published", "draft"] as const).map((s) => (
                <button key={s} type="button" aria-pressed={form.status === s} onClick={() => set("status", s)} className={cn("flex-1 rounded px-2 py-1", form.status === s ? "bg-accent font-medium" : "text-muted-foreground")}>
                  {s === "published" ? "Live" : "Draft"}
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
