import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { SocialAction, SocialItem, SocialOverview, SocialWatch } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, CopyButton, Empty, ErrorText, field, Input, Loading, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago, plural } from "../format";
import type { Tab } from "../App";

// Reddit, one channel of the Social module. Watches find posts the brand
// can honestly help with, the workspace's own AI (or a person) drafts a
// reply that discloses the brand, and a person approves each one before it
// posts from the brand's own account through the workspace's own Reddit
// app. Visitors and sign-ups come back to each reply by its UTM tag.

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;
type Connection = SocialOverview["channels"][number]["connections"][number];

const actionLabel: Record<SocialAction["status"], string> = { drafting: "Drafting", draft: "Waiting for approval", approved: "Posting", done: "Posted", rejected: "Rejected", failed: "Failed" };
const actionTone: Record<SocialAction["status"], Tone> = { drafting: "grey", draft: "amber", approved: "blue", done: "green", rejected: "grey", failed: "red" };

export function Reddit({ ws, canManage, go }: Props) {
  const brand = useBrand(ws);
  const brandKey = brand.query.brand ?? "";
  const [overview, setOverview] = useState<SocialOverview | null>(null);
  const [watches, setWatches] = useState<SocialWatch[]>([]);
  const [items, setItems] = useState<SocialItem[]>([]);
  const [actions, setActions] = useState<SocialAction[]>([]);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<SocialWatch | "new" | null>(null);
  const [replying, setReplying] = useState<SocialItem | null>(null);

  const load = useCallback(async () => {
    const query = { ...(brandKey ? { brand: brandKey } : {}), channel: "reddit" as const };
    const path = { ws };
    const [o, w, i, a] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/social", { params: { path, query: brandKey ? { brand: brandKey } : {} } }),
      api.GET("/v1/workspaces/{ws}/social/watches", { params: { path, query } }),
      api.GET("/v1/workspaces/{ws}/social/items", { params: { path, query: { ...query, status: "new" } } }),
      api.GET("/v1/workspaces/{ws}/social/actions", { params: { path, query: { ...query, limit: 100 } } }),
    ]);
    if (o.error || !o.data) return setError(errorMessage(o.error));
    setError("");
    setOverview(o.data);
    setWatches(w.data?.data ?? []);
    setItems((i.data?.data ?? []).filter((x) => !x.closed));
    setActions(a.data?.data ?? []);
  }, [ws, brandKey]);
  useEffect(() => {
    load();
  }, [load]);
  // Drafts the AI is writing appear without a reload.
  useEffect(() => {
    if (!actions.some((a) => a.status === "drafting" || a.status === "approved")) return;
    const t = setTimeout(load, 4000);
    return () => clearTimeout(t);
  }, [actions, load]);

  const header = (
    <PageHeader
      title="Reddit"
      description="Find threads where your brand can genuinely help, reply from the brand's own account with a clear disclosure, and see the visitors and sign-ups each reply brings. A person approves every reply. Agents use the social tools."
      action={canManage && overview && <Button onClick={() => setEditing("new")}>New watch</Button>}
    />
  );
  if (error && !overview) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!overview) return <div className="space-y-6">{header}<Loading /></div>;

  const channel = overview.channels.find((c) => c.id === "reddit");
  const conns = channel?.connections ?? [];
  const posting = conns.find((c) => c.can_act);
  const drafts = actions.filter((a) => a.status === "draft" || a.status === "drafting");
  const posted = actions.filter((a) => a.status === "done" || a.status === "approved" || a.status === "failed");
  const t = overview.totals;

  async function signIn(c: Connection) {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/integrations/{id}/sign-in", { params: { path: { ws, id: c.id }, query: { format: "json", return: "reddit" } } });
    if (error || !data) return toast(errorMessage(error), "error");
    location.assign((data as { url: string }).url);
  }

  return (
    <div className="space-y-6">
      {header}
      {error && <ErrorText>{error}</ErrorText>}
      {conns.length === 0 ? (
        <Card title="Connect your own Reddit app" description="Outflare searches and posts with your workspace's own Reddit app, never a shared one.">
          <ol className="list-decimal space-y-1.5 pl-5 text-[13px]">
            <li>Signed in to the brand's Reddit account, create an app at reddit.com/prefs/apps. Choose <span className="font-medium">web app</span>.</li>
            <li className="space-y-1">
              <span>Set its redirect URI to:</span>
              <span className="flex items-center gap-2"><code className="rounded bg-muted px-1.5 py-0.5 text-xs break-all">{channel?.redirect_uri}</code><CopyButton text={channel?.redirect_uri ?? ""} /></span>
            </li>
            <li>Paste the app's client id and secret under Connectors, Social, Reddit. Then sign in to the brand's account here.</li>
          </ol>
          {canManage && <div className="mt-4"><Button onClick={() => go("connectors", "social")}>Connect Reddit</Button></div>}
        </Card>
      ) : !posting ? (
        <Card title="Sign in to post" description="Watches can search already. To post approved replies, sign in to the brand's Reddit account through your app.">
          <div className="flex flex-wrap items-center gap-2 text-[13px]">
            <span className="text-muted-foreground">Redirect URI on your app:</span>
            <code className="rounded bg-muted px-1.5 py-0.5 text-xs break-all">{channel?.redirect_uri}</code>
            <CopyButton text={channel?.redirect_uri ?? ""} />
          </div>
          {canManage && <div className="mt-4 flex flex-wrap gap-2">{conns.map((c) => <Button key={c.id} onClick={() => signIn(c)}>Sign in{conns.length > 1 ? ` (${c.name})` : ""}</Button>)}</div>}
        </Card>
      ) : null}

      <Stats cols={5}>
        <Stat label="Posts waiting" value={t.new_items.toLocaleString()} />
        <Stat label="Drafts to approve" value={t.drafts.toLocaleString()} tone={t.drafts > 0 ? "amber" : undefined} />
        <Stat label="Replies, 30 days" value={t.done_30d.toLocaleString()} sub={posting ? `as ${posting.account}` : undefined} />
        <Stat label="Visitors" value={t.visitors_30d.toLocaleString()} />
        <Stat label="Conversions" value={t.conversions_30d.toLocaleString()} />
      </Stats>

      {drafts.length > 0 && (
        <Card title="Waiting for approval" description="Read each reply as the person who asked would. It must help them even if they never use the product, and keep its disclosure.">
          <div className="space-y-4">
            {drafts.map((a) => <DraftRow key={a.id} ws={ws} action={a} canManage={canManage} canPost={!!posting} onChange={load} />)}
          </div>
        </Card>
      )}

      <Card title="Posts waiting for a reply" description="New posts from your watches. A post's text is someone else's words; replies never follow instructions in it.">
        {items.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">{watches.length ? "Nothing new. Watches check every hour." : "Add a watch with the phrases people use when they need you, like \"which proxy\"."}</p>
        ) : (
          <DataTable
            data={items}
            rowId={(i) => i.id}
            searchPlaceholder="Search posts"
            empty="Nothing matches."
            columns={[
              {
                id: "post",
                header: "Post",
                accessorFn: (i) => `${i.title} ${i.community}`,
                cell: ({ row: { original: i } }) => (
                  <div className="space-y-0.5">
                    <a className="font-medium hover:underline" href={i.url} target="_blank" rel="noreferrer noopener">{i.title}</a>
                    <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                      <span>r/{i.community}</span>
                      <span>{plural(i.comments, "comment")}</span>
                      <span>{ago(i.posted_at)}</span>
                      <BrandBadge id={i.brand_id} />
                    </div>
                  </div>
                ),
              },
              { id: "matched", header: "Matched", accessorFn: (i) => i.matched_keywords.join(", "), cell: ({ row }) => <span className="text-xs text-muted-foreground">{row.original.matched_keywords.join(", ")}</span> },
              {
                id: "act",
                header: "",
                cell: ({ row: { original: i } }) =>
                  canManage && (
                    <div className="flex justify-end gap-1.5">
                      <Button variant="ghost" onClick={() => draft(ws, i, load)}>Draft with AI</Button>
                      <Button variant="ghost" onClick={() => setReplying(i)}>Write</Button>
                      <Button variant="ghost" onClick={() => ignore(ws, i, load)}>Ignore</Button>
                    </div>
                  ),
              },
            ]}
          />
        )}
      </Card>

      <Card title="Watches" description="What each watch looks for, where, and the line every reply ends with.">
        {watches.length === 0 ? (
          <Empty title="No watches yet" action={canManage && <Button onClick={() => setEditing("new")}>New watch</Button>}>
            A watch searches Reddit every hour for new posts with your phrases. Add the subreddits your buyers ask in, or leave them out to search all of Reddit.
          </Empty>
        ) : (
          <DataTable
            data={watches}
            rowId={(w) => w.id}
            onRowClick={canManage ? (w) => setEditing(w) : undefined}
            columns={[
              {
                id: "name",
                header: "Watch",
                accessorFn: (w) => w.name,
                cell: ({ row: { original: w } }) => (
                  <div className="space-y-0.5">
                    <div className="font-medium">{w.name} {w.status === "paused" && <Badge>Paused</Badge>}</div>
                    <div className="text-xs text-muted-foreground">{w.keywords.join(", ")}{w.communities.length ? ` · ${w.communities.map((c) => `r/${c}`).join(", ")}` : " · all of Reddit"}</div>
                    {w.last_error && <div className="text-xs text-destructive">{w.last_error}</div>}
                  </div>
                ),
              },
              { id: "new", header: "New", accessorFn: (w) => w.counts.new_items, meta: { align: "right", mono: true } },
              { id: "drafts", header: "Drafts", accessorFn: (w) => w.counts.drafts, meta: { align: "right", mono: true } },
              { id: "done", header: "Replies", accessorFn: (w) => w.counts.done, meta: { align: "right", mono: true } },
              { id: "checked", header: "Checked", accessorFn: (w) => w.last_checked_at ?? "", cell: ({ row }) => <span className="text-muted-foreground">{row.original.last_checked_at ? ago(row.original.last_checked_at) : "Not yet"}</span> },
            ]}
          />
        )}
      </Card>

      {posted.length > 0 && (
        <Card title="Replies" description="Visitors and conversions come from customer events tagged with each reply's link.">
          <DataTable
            data={posted}
            rowId={(a) => a.id}
            columns={[
              {
                id: "reply",
                header: "Reply",
                accessorFn: (a) => a.item?.title ?? "",
                cell: ({ row: { original: a } }) => (
                  <div className="space-y-0.5">
                    {a.external ? <a className="font-medium hover:underline" href={a.external.url} target="_blank" rel="noreferrer noopener">{a.item?.title ?? "Reply"}</a> : <span className="font-medium">{a.item?.title ?? "Reply"}</span>}
                    <div className="text-xs text-muted-foreground">{a.item ? `r/${a.item.community}` : ""}{a.approved_by ? ` · approved by ${a.approved_by.name}` : ""}{a.status_reason ? ` · ${a.status_reason}` : ""}</div>
                  </div>
                ),
              },
              { id: "status", header: "Status", accessorFn: (a) => a.status, cell: ({ row: { original: a } }) => <Badge tone={a.external?.removed ? "red" : actionTone[a.status]}>{a.external?.removed ? "Removed" : actionLabel[a.status]}</Badge> },
              { id: "score", header: "Score", accessorFn: (a) => a.external?.score ?? 0, meta: { align: "right", mono: true }, cell: ({ row }) => row.original.external?.score ?? "–" },
              { id: "visitors", header: "Visitors", accessorFn: (a) => a.results.visitors, meta: { align: "right", mono: true } },
              { id: "conv", header: "Conversions", accessorFn: (a) => a.results.conversions, meta: { align: "right", mono: true } },
              { id: "when", header: "Posted", accessorFn: (a) => a.done_at ?? "", cell: ({ row }) => <span className="text-muted-foreground">{row.original.done_at ? ago(row.original.done_at) : ""}</span> },
            ]}
          />
        </Card>
      )}

      {editing && <WatchSheet ws={ws} watch={editing === "new" ? null : editing} brandID={brand.brand?.id ?? null} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />}
      {replying && <ReplySheet ws={ws} item={replying} disclosure={watches.find((w) => w.id === replying.watch_id)?.disclosure ?? ""} onClose={() => setReplying(null)} onSaved={() => { setReplying(null); load(); }} />}
    </div>
  );
}

async function draft(ws: string, i: SocialItem, done: () => void) {
  const { data, error } = await api.POST("/v1/workspaces/{ws}/social/items/{id}/draft", { params: { path: { ws, id: i.id } } });
  if (error || !data) return toast(errorMessage(error), "error");
  toast("Drafting. It shows up under Waiting for approval.");
  done();
}

async function ignore(ws: string, i: SocialItem, done: () => void) {
  const { error } = await api.PATCH("/v1/workspaces/{ws}/social/items/{id}", { params: { path: { ws, id: i.id } }, body: { status: "ignored" } });
  if (error) return toast(errorMessage(error), "error");
  done();
}

function DraftRow({ ws, action, canManage, canPost, onChange }: { ws: string; action: SocialAction; canManage: boolean; canPost: boolean; onChange: () => void }) {
  const [body, setBody] = useState(action.body);
  const [busy, setBusy] = useState(false);
  useEffect(() => setBody(action.body), [action.id, action.body]);
  const changed = body !== action.body;

  async function run(f: () => Promise<{ error?: unknown }>, done: string) {
    setBusy(true);
    const { error } = await f();
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    onChange();
  }
  const path = { params: { path: { ws, id: action.id } } };
  const save = () => run(() => api.PATCH("/v1/workspaces/{ws}/social/actions/{id}", { ...path, body: { body } }), "Saved.");
  const approve = () =>
    run(async () => {
      if (changed) {
        const r = await api.PATCH("/v1/workspaces/{ws}/social/actions/{id}", { ...path, body: { body } });
        if (r.error) return r;
      }
      return api.POST("/v1/workspaces/{ws}/social/actions/{id}/approve", path);
    }, "Approved. It posts in a moment.");
  const reject = () => run(() => api.POST("/v1/workspaces/{ws}/social/actions/{id}/reject", { ...path, body: {} }), "Rejected. The post is open again.");

  return (
    <div className="space-y-2 rounded-md border p-3 text-[13px]">
      <div className="flex flex-wrap items-center gap-1.5">
        <a className="font-medium hover:underline" href={action.item?.url} target="_blank" rel="noreferrer noopener">{action.item?.title}</a>
        <span className="text-xs text-muted-foreground">r/{action.item?.community}</span>
        <Badge tone={actionTone[action.status]}>{actionLabel[action.status]}</Badge>
        {action.drafted_via === "ai" && action.relevance != null && <Badge title={action.relevance_reason ?? undefined}>Fit {Math.round(action.relevance * 100)}%</Badge>}
        <BrandBadge id={action.brand_id} />
      </div>
      {action.status === "drafting" ? (
        <p className="text-muted-foreground">Your AI is writing it.</p>
      ) : (
        <>
          <textarea rows={6} className={`${field} w-full resize-y`} value={body} onChange={(e) => setBody(e.target.value)} readOnly={!canManage} />
          {action.link_url && <p className="text-xs text-muted-foreground">Tracked link: <span className="break-all">{action.link_url}</span></p>}
          {canManage && (
            <div className="flex flex-wrap gap-2">
              <Button disabled={busy || !canPost || !body.includes(action.disclosure)} onClick={approve} title={!canPost ? "Sign in to the brand's Reddit account first" : undefined}>Approve and post</Button>
              {changed && <Button variant="ghost" disabled={busy} onClick={save}>Save</Button>}
              <Button variant="ghost" disabled={busy} onClick={reject}>Reject</Button>
              {!body.includes(action.disclosure) && <span className="self-center text-xs text-destructive">Keep the disclosure: {action.disclosure}</span>}
            </div>
          )}
        </>
      )}
    </div>
  );
}

function ReplySheet({ ws, item, disclosure, onClose, onSaved }: { ws: string; item: SocialItem; disclosure: string; onClose: () => void; onSaved: () => void }) {
  const [body, setBody] = useState(`\n\n${disclosure}`);
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/social/actions", { params: { path: { ws } }, body: { type: "reply", item_id: item.id, body } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Saved as a draft to approve.");
    onSaved();
  }
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>Reply to r/{item.community}</SheetTitle>
          <SheetDescription>{item.title}</SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-4 px-4 pb-6 text-[13px]">
          <p className="max-h-48 overflow-y-auto whitespace-pre-wrap text-muted-foreground">{item.body}</p>
          <textarea rows={10} className={`${field} w-full resize-y`} value={body} onChange={(e) => setBody(e.target.value)} autoFocus />
          <p className="text-xs text-muted-foreground">Answer the question first. Keep the disclosure at the end.</p>
          <Button type="submit" disabled={busy || !body.includes(disclosure)}>Save draft</Button>
        </form>
      </SheetContent>
    </Sheet>
  );
}

const lines = (s: string) => s.split(/[\n,]/).map((x) => x.trim()).filter(Boolean);

function WatchSheet({ ws, watch, brandID, onClose, onSaved }: { ws: string; watch: SocialWatch | null; brandID: string | null; onClose: () => void; onSaved: () => void }) {
  const [f, setF] = useState({
    name: watch?.name ?? "",
    keywords: watch?.keywords.join("\n") ?? "",
    exclude: watch?.exclude_keywords.join("\n") ?? "",
    communities: watch?.communities.join(", ") ?? "",
    landing: watch?.landing_url ?? "",
    disclosure: watch?.disclosure ?? "",
    guidance: watch?.guidance ?? "",
    autoDraft: watch?.auto_draft ?? true,
    limit: String(watch?.daily_limit ?? 5),
  });
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof f, v: string | boolean) => setF((x) => ({ ...x, [k]: v }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const body = {
      name: f.name, keywords: lines(f.keywords), exclude_keywords: lines(f.exclude), communities: lines(f.communities), landing_url: f.landing,
      guidance: f.guidance, auto_draft: f.autoDraft, daily_limit: Number(f.limit) || 5, ...(f.disclosure ? { disclosure: f.disclosure } : {}),
    };
    const { error } = watch
      ? await api.PATCH("/v1/workspaces/{ws}/social/watches/{id}", { params: { path: { ws, id: watch.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/social/watches", { params: { path: { ws } }, body: { ...body, channel: "reddit", brand_id: brandID } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(watch ? "Saved." : "Watching. The first check runs now.");
    onSaved();
  }
  async function pause() {
    if (!watch) return;
    const { error } = await api.PATCH("/v1/workspaces/{ws}/social/watches/{id}", { params: { path: { ws, id: watch.id } }, body: { status: watch.status === "active" ? "paused" : "active" } });
    if (error) return toast(errorMessage(error), "error");
    onSaved();
  }
  async function check() {
    if (!watch) return;
    const { error } = await api.POST("/v1/workspaces/{ws}/social/watches/{id}/check", { params: { path: { ws, id: watch.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Checking now.");
  }
  async function remove() {
    if (!watch) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/social/watches/{id}", { params: { path: { ws, id: watch.id } } });
    if (error) return toast(errorMessage(error), "error");
    onSaved();
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{watch ? watch.name : "New watch"}</SheetTitle>
          <SheetDescription>Posts from the last week with any of the phrases, checked every hour.</SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-4 px-4 pb-6">
          <Input label="Name" value={f.name} onChange={(e) => set("name", e.target.value)} maxLength={200} required placeholder="Proxy questions" />
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">Phrases</span>
            <textarea rows={3} className={`${field} w-full`} value={f.keywords} onChange={(e) => set("keywords", e.target.value)} required placeholder={"which proxy\nbest residential proxy"} />
            <span className="mt-1 block text-xs text-muted-foreground">One a line. A post must contain one.</span>
          </label>
          <Input label="Leave out posts with" hint="Comma separated." value={f.exclude} onChange={(e) => set("exclude", e.target.value)} />
          <Input label="Subreddits" hint="Comma separated, without r/. Empty searches all of Reddit." value={f.communities} onChange={(e) => set("communities", e.target.value)} placeholder="webscraping, webdev" />
          <Input label="Disclosure" hint="Every reply ends with it. Empty uses &quot;Disclosure: I work at <brand>.&quot;" value={f.disclosure} onChange={(e) => set("disclosure", e.target.value)} maxLength={300} placeholder="Disclosure: I work at Northpeak." />
          <Input label="Landing page" hint="Optional. Each reply's link gets its own UTM tags, so sign-ups come back to it." value={f.landing} onChange={(e) => set("landing", e.target.value)} type="url" placeholder="https://northpeak.example/" />
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">Guidance for replies</span>
            <textarea rows={4} className={`${field} w-full`} value={f.guidance} onChange={(e) => set("guidance", e.target.value)} maxLength={4000} placeholder="What the product is good and bad at, who it's for, the tone." />
          </label>
          <label className="flex items-center gap-2 text-[13px]">
            <input type="checkbox" checked={f.autoDraft} onChange={(e) => set("autoDraft", e.target.checked)} />
            Draft replies to new posts with our AI model (a person still approves each one)
          </label>
          <Input label="Most replies a day" type="number" min={1} max={50} value={f.limit} onChange={(e) => set("limit", e.target.value)} />
          <div className="flex flex-wrap gap-2">
            <Button type="submit" disabled={busy}>{watch ? "Save" : "Start watching"}</Button>
            {watch && <Button type="button" variant="ghost" onClick={check}>Check now</Button>}
            {watch && <Button type="button" variant="ghost" onClick={pause}>{watch.status === "active" ? "Pause" : "Resume"}</Button>}
            {watch && <Button type="button" variant="ghost" onClick={remove}>Delete</Button>}
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}
