import { useCallback, useEffect, useState } from "react";
import type { SocialAccount, SocialPublication, SocialSeries } from "@outflare/sdk";
import { BrandBadge } from "@/components/brand-badge";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, field, toast } from "../ui";
import { ago, when } from "../format";

// Publishing: the brand's own posts on X and LinkedIn. Anyone who can
// manage social writes a post (or has the workspace's own AI draft it);
// a signed-in person approves it, and it goes out now or at its time. A
// recurring series is approved once, then fed by a script or agent.

type Pub = SocialPublication;
const limits: Record<string, number> = { x: 280, linkedin: 3000 };
const channelName: Record<string, string> = { x: "X", linkedin: "LinkedIn" };
const statusLabel: Record<Pub["status"], string> = {
  drafting: "AI drafting", draft: "Draft", approved: "Approved", publishing: "Posting", published: "Published", failed: "Failed", cancelled: "Cancelled",
};
const statusTone: Record<Pub["status"], "grey" | "blue" | "green" | "red" | "amber"> = {
  drafting: "grey", draft: "amber", approved: "blue", publishing: "blue", published: "green", failed: "red", cancelled: "grey",
};

// length counts like the channel: X counts every link as 23.
function length(channel: string, text: string) {
  if (channel !== "x") return [...text].length;
  let links = 0;
  const rest = text.replace(/https?:\/\/\S+/g, () => { links++; return ""; });
  return [...rest].length + links * 23;
}
const withLink = (body: string, link: string) => (link ? (body ? `${body}\n\n${link}` : link) : body);
const accountName = (a?: SocialAccount) => (a ? `${channelName[a.channel] ?? a.channel} · ${a.name || a.handle}${a.kind === "page" ? " (page)" : ""}` : "Account");

export function Publishing({ ws, accounts, canManage }: { ws: string; accounts: SocialAccount[]; canManage: boolean }) {
  const postable = accounts.filter((a) => (a.channel === "x" || a.channel === "linkedin") && a.status !== "disconnected" && a.integration_id);
  const [pubs, setPubs] = useState<Pub[]>([]);
  const [series, setSeries] = useState<SocialSeries[]>([]);
  const load = useCallback(async () => {
    const [p, s] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/social/publications", { params: { path: { ws }, query: { limit: 50 } } }),
      api.GET("/v1/workspaces/{ws}/social/series", { params: { path: { ws } } }),
    ]);
    setPubs(p.data?.data ?? []);
    setSeries(s.data?.data ?? []);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);
  // Drafting and posting finish in the background; look again shortly.
  useEffect(() => {
    if (!pubs.some((p) => p.status === "drafting" || p.status === "publishing" || (p.status === "approved" && !p.scheduled_for))) return;
    const t = setTimeout(load, 4000);
    return () => clearTimeout(t);
  }, [pubs, load]);

  if (postable.length === 0) return null;
  const byId = (id: string) => accounts.find((a) => a.id === id);
  return (
    <>
      {canManage && <Composer ws={ws} accounts={postable} onDone={load} />}
      {pubs.length > 0 && (
        <Card title="Posts" description="Scheduled and published posts on your own accounts. A person approves each one before it goes out; one that fails is never sent again.">
          <div className="space-y-3">{pubs.map((p) => <PubRow key={p.id} ws={ws} pub={p} account={byId(p.account_id)} canManage={canManage} onChange={load} />)}</div>
        </Card>
      )}
      <SeriesCard ws={ws} series={series} accounts={postable} byId={byId} canManage={canManage} onChange={load} />
    </>
  );
}

function Composer({ ws, accounts, onDone }: { ws: string; accounts: SocialAccount[]; onDone: () => void }) {
  const [account, setAccount] = useState(accounts[0]?.id ?? "");
  const [body, setBody] = useState("");
  const [prompt, setPrompt] = useState("");
  const [link, setLink] = useState("");
  const [at, setAt] = useState("");
  const [busy, setBusy] = useState(false);
  const acct = accounts.find((a) => a.id === account) ?? accounts[0];
  const channel = acct?.channel ?? "x";
  // The tracked link is longer than what was typed, but X counts it as 23
  // either way; LinkedIn counts it in full, so this is a close guess there.
  const count = length(channel, withLink(body.trim(), link.trim()));
  const over = count > (limits[channel] ?? 0);

  async function create(withAI: boolean) {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/social/publications", {
      params: { path: { ws } },
      body: {
        account_id: acct.id,
        ...(withAI ? { prompt: prompt.trim() } : { body: body.trim() }),
        ...(link.trim() ? { link_url: link.trim() } : {}),
        ...(at ? { scheduled_for: new Date(at).toISOString() } : {}),
      },
    });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(withAI ? "Your AI is drafting it. Approve it below when it's ready." : "Saved as a draft. Approve it below to post.");
    setBody("");
    setPrompt("");
    setLink("");
    setAt("");
    onDone();
  }

  return (
    <Card title="New post" description="Write a post for one of your accounts, or say what it's about and your own AI drafts it. Nothing goes out until a person approves it.">
      <div className="space-y-3 text-[13px]">
        <select className={`${field} w-full sm:w-auto`} value={acct?.id} onChange={(e) => setAccount(e.target.value)} aria-label="Account">
          {accounts.map((a) => <option key={a.id} value={a.id}>{accountName(a)}</option>)}
        </select>
        <div className="space-y-1">
          <textarea rows={4} className={`${field} w-full resize-y`} placeholder="What do you want to say?" value={body} onChange={(e) => setBody(e.target.value)} aria-label="Post" />
          <div className={`text-right text-xs tabular-nums ${over ? "text-destructive" : "text-muted-foreground"}`}>
            {count.toLocaleString()} / {(limits[channel] ?? 0).toLocaleString()}{channel === "x" && link.trim() ? " (the link counts as 23)" : ""}
          </div>
        </div>
        <div className="grid gap-2 sm:grid-cols-2">
          <input className={`${field} w-full`} placeholder="Link (optional), tagged so visits come back to this post" value={link} onChange={(e) => setLink(e.target.value)} aria-label="Link" />
          <input type="datetime-local" className={`${field} w-full`} value={at} onChange={(e) => setAt(e.target.value)} aria-label="Post at" title="Leave empty to post as soon as it's approved" />
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button disabled={busy || !body.trim() || over} onClick={() => create(false)}>Save draft</Button>
          <span className="text-xs text-muted-foreground">or</span>
          <input className={`${field} min-w-60 flex-1`} placeholder="What should the AI write about?" value={prompt} onChange={(e) => setPrompt(e.target.value)} aria-label="AI prompt" />
          <Button variant="ghost" disabled={busy || !prompt.trim()} onClick={() => create(true)}>Draft with AI</Button>
        </div>
      </div>
    </Card>
  );
}

function PubRow({ ws, pub, account, canManage, onChange }: { ws: string; pub: Pub; account?: SocialAccount; canManage: boolean; onChange: () => void }) {
  const [body, setBody] = useState(pub.body);
  const [busy, setBusy] = useState(false);
  useEffect(() => setBody(pub.body), [pub.id, pub.body]);
  const editable = pub.status === "draft";
  const changed = body !== pub.body;
  const count = length(pub.channel, withLink(body.trim(), pub.link_url));
  const over = count > pub.max_length;
  const path = { params: { path: { ws, id: pub.id } } };

  async function run(f: () => Promise<{ error?: unknown }>, done: string) {
    setBusy(true);
    const { error } = await f();
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    onChange();
  }
  const approve = () =>
    run(async () => {
      if (changed) {
        const r = await api.PATCH("/v1/workspaces/{ws}/social/publications/{id}", { ...path, body: { body: body.trim() } });
        if (r.error) return r;
      }
      return api.POST("/v1/workspaces/{ws}/social/publications/{id}/approve", path);
    }, pub.scheduled_for ? `Approved. It goes out ${when(pub.scheduled_for)}.` : "Approved. It goes out in a moment.");
  const save = () => run(() => api.PATCH("/v1/workspaces/{ws}/social/publications/{id}", { ...path, body: { body: body.trim() } }), "Saved.");
  const cancel = async () => {
    if (!(await confirmAction("Cancel this post?", "It won't go out. This can't be undone.", "Cancel post"))) return;
    run(() => api.DELETE("/v1/workspaces/{ws}/social/publications/{id}", path), "Cancelled.");
  };

  return (
    <div className="space-y-2 rounded-md border p-3 text-[13px]">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="font-medium">{accountName(account)}</span>
        <Badge tone={statusTone[pub.status]} title={pub.status_reason ?? undefined}>{statusLabel[pub.status]}</Badge>
        {pub.drafted_via === "ai" && <Badge>AI draft</Badge>}
        {pub.drafted_via === "series" && <Badge>Series</Badge>}
        <BrandBadge id={pub.brand_id} />
        <span className="text-xs text-muted-foreground">
          {pub.published_at ? `Published ${ago(pub.published_at)}` : pub.scheduled_for ? `For ${when(pub.scheduled_for)}` : `Added ${ago(pub.created_at)}`}
          {pub.approved_by && ` · approved by ${pub.approved_by.name}`}
        </span>
      </div>
      {pub.status === "drafting" ? (
        <p className="text-muted-foreground">Your AI is writing it.</p>
      ) : editable && canManage ? (
        <>
          <textarea rows={4} className={`${field} w-full resize-y`} value={body} onChange={(e) => setBody(e.target.value)} aria-label="Post" />
          <div className={`text-right text-xs tabular-nums ${over ? "text-destructive" : "text-muted-foreground"}`}>{count.toLocaleString()} / {pub.max_length.toLocaleString()}</div>
        </>
      ) : (
        <p className="whitespace-pre-wrap">{pub.body}</p>
      )}
      {pub.link_url && <p className="text-xs text-muted-foreground">Tracked link: <span className="break-all">{pub.link_url}</span></p>}
      {pub.status_reason && <p className={`text-xs ${pub.status === "failed" ? "text-destructive" : "text-muted-foreground"}`}>{pub.status_reason}</p>}
      <div className="flex flex-wrap gap-2">
        {pub.url && <a className="text-xs hover:underline" href={pub.url} target="_blank" rel="noreferrer noopener">View on {channelName[pub.channel] ?? pub.channel}</a>}
        {canManage && editable && (
          <Button disabled={busy || over || !body.trim()} onClick={approve} title="Only a person can approve a post">Approve{pub.scheduled_for ? "" : " and post"}</Button>
        )}
        {canManage && editable && changed && <Button variant="ghost" disabled={busy} onClick={save}>Save</Button>}
        {canManage && (pub.status === "draft" || pub.status === "approved" || pub.status === "drafting") && <Button variant="danger" disabled={busy} onClick={cancel}>Cancel</Button>}
      </div>
    </div>
  );
}

function SeriesCard({ ws, series, accounts, byId, canManage, onChange }: {
  ws: string; series: SocialSeries[]; accounts: SocialAccount[]; byId: (id: string) => SocialAccount | undefined; canManage: boolean; onChange: () => void;
}) {
  const [adding, setAdding] = useState(false);
  const [account, setAccount] = useState(accounts[0]?.id ?? "");
  const [name, setName] = useState("");
  const [template, setTemplate] = useState("");
  const [daily, setDaily] = useState(1);
  const [busy, setBusy] = useState(false);

  async function run(f: () => Promise<{ error?: unknown }>, done: string) {
    setBusy(true);
    const { error } = await f();
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    onChange();
  }
  const create = () =>
    run(async () => {
      const r = await api.POST("/v1/workspaces/{ws}/social/series", { params: { path: { ws } }, body: { account_id: account || accounts[0].id, name: name.trim(), template: template.trim(), daily_limit: daily } });
      if (!r.error) {
        setAdding(false);
        setName("");
        setTemplate("");
      }
      return r;
    }, "Series added. Approve it to let posts through.");

  if (series.length === 0 && !canManage) return null;
  return (
    <Card
      title="Recurring posts"
      description="A series, such as a daily changelog summary, is approved once. Then your script or agent hands in each post (POST .../social/series/{id}/posts) and it goes out with no other approval, up to its daily limit."
      action={canManage && !adding && <Button variant="ghost" onClick={() => setAdding(true)}>Add series</Button>}
    >
      <div className="space-y-3 text-[13px]">
        {adding && (
          <div className="space-y-2 rounded-md border p-3">
            <div className="grid gap-2 sm:grid-cols-3">
              <select className={`${field} w-full`} value={account} onChange={(e) => setAccount(e.target.value)} aria-label="Account">
                {accounts.map((a) => <option key={a.id} value={a.id}>{accountName(a)}</option>)}
              </select>
              <input className={`${field} w-full`} placeholder="Name, such as Daily changelog" value={name} onChange={(e) => setName(e.target.value)} aria-label="Name" />
              <label className="flex items-center gap-2">
                <span className="text-muted-foreground">Posts a day</span>
                <input type="number" min={1} max={24} className={`${field} w-20`} value={daily} onChange={(e) => setDaily(Math.min(24, Math.max(1, Number(e.target.value) || 1)))} aria-label="Daily limit" />
              </label>
            </div>
            <textarea rows={3} className={`${field} w-full resize-y`} placeholder="What it will post, for whoever approves it: a sample or a description" value={template} onChange={(e) => setTemplate(e.target.value)} aria-label="Template" />
            <div className="flex gap-2">
              <Button disabled={busy || !name.trim()} onClick={create}>Add</Button>
              <Button variant="ghost" onClick={() => setAdding(false)}>Cancel</Button>
            </div>
          </div>
        )}
        {series.length === 0 && !adding && <p className="text-muted-foreground">No series yet.</p>}
        {series.map((s) => {
          const path = { params: { path: { ws, id: s.id } } };
          return (
            <div key={s.id} className="flex flex-wrap items-center gap-2 rounded-md border p-3">
              <div className="min-w-0 flex-1 space-y-0.5">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="font-medium">{s.name}</span>
                  <Badge tone={s.paused ? "grey" : s.approved_by ? "green" : "amber"}>{s.paused ? "Paused" : s.approved_by ? "Approved" : "Waiting for approval"}</Badge>
                </div>
                <div className="text-xs text-muted-foreground">
                  {accountName(byId(s.account_id))} · {s.posts_today} of {s.daily_limit} today{s.last_post_at && ` · last ${ago(s.last_post_at)}`}
                  {s.approved_by && ` · approved by ${s.approved_by.name}`}
                </div>
                {s.template && <p className="whitespace-pre-wrap text-xs text-muted-foreground">{s.template}</p>}
              </div>
              {canManage && !s.approved_by && (
                <Button disabled={busy} onClick={() => run(() => api.POST("/v1/workspaces/{ws}/social/series/{id}/approve", path), "Approved. Posts handed in now go out.")}>Approve</Button>
              )}
              {canManage && (
                <Button variant="ghost" disabled={busy} onClick={() => run(() => api.PATCH("/v1/workspaces/{ws}/social/series/{id}", { ...path, body: { paused: !s.paused } }), s.paused ? "Resumed." : "Paused.")}>
                  {s.paused ? "Resume" : "Pause"}
                </Button>
              )}
            </div>
          );
        })}
      </div>
    </Card>
  );
}
