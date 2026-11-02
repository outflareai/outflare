import { useEffect, useState } from "react";
import type { SupportChatWidget } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, CopyButton, ErrorText, field, Loading, toast } from "../ui";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";

// Chat on your website: a chat button for the business's site. Visitors'
// messages land in the support inbox, the team's replies show in the chat.
// It shows only on the sites listed; a site can vouch for signed-in users
// with the identity secret.

type Draft = { origins: string; title: string; greeting: string; color: string; require_email: boolean; show_articles: boolean };

const blank: Draft = { origins: "", title: "Chat with us", greeting: "Hi! Ask us anything, we usually reply within a few hours.", color: "#111827", require_email: false, show_articles: true };

function fromWidget(w: SupportChatWidget): Draft {
  return {
    origins: w.allowed_origins.map((o) => o.replace("https://*.", "")).join(", "),
    title: w.settings.title,
    greeting: w.settings.greeting,
    color: w.settings.color,
    require_email: w.settings.require_email,
    show_articles: w.settings.show_articles,
  };
}

function body(d: Draft) {
  return {
    allowed_origins: d.origins.split(/[\s,]+/).filter(Boolean),
    settings: { title: d.title, greeting: d.greeting, color: d.color, require_email: d.require_email, show_articles: d.show_articles },
  };
}

export function SupportChat({ ws, canManage }: { ws: string; canManage: boolean }) {
  const [list, setList] = useState<SupportChatWidget[] | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [editing, setEditing] = useState<string | null>(null);
  const [secret, setSecret] = useState<{ id: string; value: string } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function load() {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/support/chat-widgets", { params: { path: { ws } } });
    if (error) return setError(errorMessage(error));
    setList(data.data);
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws]);

  async function save() {
    if (!draft) return;
    setBusy(true);
    setError("");
    const res = editing
      ? await api.PATCH("/v1/workspaces/{ws}/support/chat-widgets/{id}", { params: { path: { ws, id: editing } }, body: body(draft) })
      : await api.POST("/v1/workspaces/{ws}/support/chat-widgets", { params: { path: { ws } }, body: body(draft) });
    setBusy(false);
    if (res.error) return setError(errorMessage(res.error));
    track(editing ? "ui.support_chat_saved" : "ui.support_chat_made", { require_email: draft.require_email, show_articles: draft.show_articles });
    toast(editing ? "Chat saved" : "Chat made: paste the code into your site");
    setDraft(null);
    setEditing(null);
    load();
  }

  async function toggle(w: SupportChatWidget) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/support/chat-widgets/{id}", { params: { path: { ws, id: w.id } }, body: { enabled: !w.enabled } });
    if (error) return toast(errorMessage(error), "error");
    toast(w.enabled ? "Chat turned off: it's gone from your site" : "Chat turned on");
    load();
  }

  async function rotate(w: SupportChatWidget) {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/support/chat-widgets/{id}/identity-secret", { params: { path: { ws, id: w.id } } });
    if (error) return toast(errorMessage(error), "error");
    setSecret({ id: w.id, value: data.secret });
    load();
  }

  if (!list && !error) return <Loading />;

  const form = draft && (
    <div className="space-y-3 rounded-md bg-muted/40 p-3">
      <label className="block space-y-1">
        <span className="text-xs font-medium">Your site</span>
        <input className={cn(field, "w-full")} placeholder="northpeak.example" value={draft.origins} onChange={(e) => setDraft({ ...draft, origins: e.target.value })} />
        <span className="block text-xs text-muted-foreground">The chat shows only here. A domain covers its subdomains; separate several with commas.</span>
      </label>
      <div className="grid gap-3 sm:grid-cols-[1fr_auto]">
        <label className="block space-y-1">
          <span className="text-xs font-medium">Title</span>
          <input className={cn(field, "w-full")} maxLength={60} value={draft.title} onChange={(e) => setDraft({ ...draft, title: e.target.value })} />
        </label>
        <label className="block space-y-1">
          <span className="text-xs font-medium">Colour</span>
          <input type="color" className="h-8 w-16 cursor-pointer rounded-md border bg-background p-0.5" value={draft.color} onChange={(e) => setDraft({ ...draft, color: e.target.value })} />
        </label>
      </div>
      <label className="block space-y-1">
        <span className="text-xs font-medium">Greeting</span>
        <textarea className={cn(field, "h-auto w-full py-1.5")} rows={2} maxLength={300} value={draft.greeting} onChange={(e) => setDraft({ ...draft, greeting: e.target.value })} />
      </label>
      <label className="flex items-start gap-2 text-[13px]">
        <input type="checkbox" className="mt-0.5" checked={draft.require_email} onChange={(e) => setDraft({ ...draft, require_email: e.target.checked })} />
        <span>Ask for an email before the first message<span className="block text-xs text-muted-foreground">So you can reach them after they leave.</span></span>
      </label>
      <label className="flex items-start gap-2 text-[13px]">
        <input type="checkbox" className="mt-0.5" checked={draft.show_articles} onChange={(e) => setDraft({ ...draft, show_articles: e.target.checked })} />
        <span>Let visitors search your help centre first<span className="block text-xs text-muted-foreground">Needs a live help centre.</span></span>
      </label>
      <div className="flex gap-2">
        <Button disabled={busy || !draft.origins.trim()} onClick={save}>{busy ? "Saving…" : editing ? "Save" : "Make the chat"}</Button>
        <Button variant="ghost" onClick={() => { setDraft(null); setEditing(null); setError(""); }}>Cancel</Button>
      </div>
    </div>
  );

  return (
    <div className="space-y-3 rounded-lg border p-4">
      <div>
        <p className="text-[13px] font-medium">Chat on your website</p>
        <p className="mt-0.5 text-xs text-muted-foreground">A chat button for your site. Messages come here like email does, and your replies show in their chat.</p>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {list?.map((w) =>
        editing === w.id ? (
          <div key={w.id}>{form}</div>
        ) : (
          <div key={w.id} className="space-y-2 rounded-md border p-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="size-3 shrink-0 rounded-full" style={{ background: w.settings.color }} aria-hidden />
              <span className="min-w-0 flex-1 truncate text-[13px] font-medium">{w.settings.title}</span>
              <span className={cn("rounded-full px-2 py-0.5 text-[11px] font-medium", w.enabled ? "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400" : "bg-muted text-muted-foreground")}>{w.enabled ? "On" : "Off"}</span>
            </div>
            <p className="text-xs text-muted-foreground">On {w.allowed_origins.map((o) => o.replace("https://*.", "")).join(", ")}</p>
            <div className="flex items-start gap-2 rounded bg-muted/50 p-2">
              <code className="min-w-0 flex-1 font-mono text-[11px] break-all">{w.embed_code}</code>
              <CopyButton text={w.embed_code} label="Copy code" />
            </div>
            <p className="text-xs text-muted-foreground">Paste it before <code className="font-mono">&lt;/body&gt;</code> on every page the chat should show on.</p>
            {secret?.id === w.id && (
              <div className="space-y-1.5 rounded-md border border-amber-500/40 bg-amber-50/60 p-2 text-xs dark:bg-amber-500/5">
                <p className="font-medium">Identity secret: shown once, keep it on your server</p>
                <div className="flex items-start gap-2">
                  <code className="min-w-0 flex-1 font-mono break-all">{secret.value}</code>
                  <CopyButton text={secret.value} label="Copy" />
                </div>
                <p className="text-muted-foreground">
                  Sign a signed-in user's lowercase email with it (hex HMAC-SHA256) and put{" "}
                  <code className="font-mono">window.OutflareChat = {"{ identity: { email, name, signature } }"}</code> before the chat code. Their chats show as verified.
                </p>
              </div>
            )}
            {canManage && (
              <div className="flex flex-wrap gap-2">
                <Button variant="ghost" onClick={() => { setEditing(w.id); setDraft(fromWidget(w)); }}>Edit</Button>
                <Button variant="ghost" onClick={() => toggle(w)}>{w.enabled ? "Turn off" : "Turn on"}</Button>
                <Button variant="ghost" onClick={() => rotate(w)}>{w.identity_verification ? "New identity secret" : "Verify signed-in users"}</Button>
              </div>
            )}
          </div>
        ),
      )}
      {!editing && form}
      {canManage && !draft && (
        <Button variant={list?.length ? "ghost" : "primary"} onClick={() => setDraft(blank)}>{list?.length ? "Add a chat for another site" : "Add a chat to your site"}</Button>
      )}
      {!canManage && !list?.length && <p className="text-xs text-muted-foreground">Ask someone who manages support to add a chat.</p>}
    </div>
  );
}
