import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import type { KnowledgeAnswer, Mailbox, Member, SupportConversation, SupportCounts, SupportMailbox, SupportMessage } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, confirmAction, Empty, ErrorText, field, Loading, Menu, toast } from "../ui";
import { Icon } from "../icons";
import { ago, listTime, plural, stamp } from "../format";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { useBrand } from "@/lib/brand";
import type { Tab } from "../App";

// The support inbox: customers who write to a support mailbox, as
// conversations the team works through. Folders on the left, the list in
// the middle, the conversation and its composer on the right, with the
// customer's details beside it. A reply goes out from the support mailbox,
// in the same thread. Suggest a reply drafts one from knowledge customers
// may be told, only when someone asks.

type Folder = "mine" | "unassigned" | "open" | "pending" | "closed" | "all";
type Team = { id: string; name: string };

const folders: { id: Folder; label: string; hint: string; count: (c: SupportCounts) => number | undefined }[] = [
  { id: "mine", label: "Your inbox", hint: "Assigned to you and not closed", count: (c) => c.mine },
  { id: "unassigned", label: "Unassigned", hint: "Nobody has picked these up yet", count: (c) => c.unassigned },
  { id: "open", label: "Open", hint: "Waiting on your team", count: (c) => c.open },
  { id: "pending", label: "Waiting on customer", hint: "You replied; it opens again when they write", count: (c) => c.pending },
  { id: "closed", label: "Closed", hint: "Done", count: () => undefined },
  { id: "all", label: "All conversations", hint: "Everything", count: () => undefined },
];

function query(f: Folder): Record<string, string> {
  switch (f) {
    case "mine":
      return { assignee: "me", status: "all" };
    case "unassigned":
      return { assignee: "none", status: "open" };
    case "all":
      return { status: "all" };
    default:
      return { status: f };
  }
}

const statusTone = { open: "blue", pending: "amber", closed: "grey" } as const;
const statusLabel = { open: "Open", pending: "Waiting on customer", closed: "Closed" } as const;

function initials(name: string, email: string) {
  const src = name.trim() || email;
  const parts = src.split(/[\s@._-]+/).filter(Boolean);
  return ((parts[0]?.[0] ?? "?") + (parts[1]?.[0] ?? "")).toUpperCase();
}

function Avatar({ name, email, className }: { name: string; email: string; className?: string }) {
  // A steady colour per person, so the same customer is easy to spot.
  let h = 0;
  for (const ch of email) h = (h * 31 + ch.charCodeAt(0)) % 360;
  return (
    <span
      aria-hidden
      className={cn("inline-flex size-8 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold text-white", className)}
      style={{ background: `hsl(${h} 45% 45%)` }}
    >
      {initials(name, email)}
    </span>
  );
}

// useFillHeight sizes the inbox to the rest of the window below it, so the
// list and the thread scroll on their own and the composer stays in view,
// whatever banners sit above.
function useFillHeight() {
  const [height, setHeight] = useState<number>();
  const node = useRef<HTMLDivElement | null>(null);
  const measure = useCallback(() => {
    const el = node.current;
    if (el) setHeight(Math.max(520, window.innerHeight - (el.getBoundingClientRect().top + window.scrollY)));
  }, []);
  useEffect(() => {
    window.addEventListener("resize", measure);
    const ro = new ResizeObserver(measure);
    ro.observe(document.body);
    return () => {
      window.removeEventListener("resize", measure);
      ro.disconnect();
    };
  }, [measure]);
  const ref = useCallback(
    (el: HTMLDivElement | null) => {
      node.current = el;
      measure();
    },
    [measure],
  );
  return { ref, height };
}

function duration(seconds: number | null | undefined) {
  if (seconds == null) return "–";
  if (seconds < 90) return `${Math.round(seconds)}s`;
  if (seconds < 5400) return `${Math.round(seconds / 60)}m`;
  if (seconds < 172800) return `${(seconds / 3600).toFixed(1).replace(/\.0$/, "")}h`;
  return `${Math.round(seconds / 86400)}d`;
}

export function Support({
  ws,
  canManage,
  canSetUp,
  myId,
  open,
  setOpen,
  go,
}: {
  ws: string;
  canManage: boolean;
  canSetUp: boolean;
  myId: string;
  open?: string;
  setOpen: (id?: string) => void;
  go: (tab: Tab) => void;
}) {
  const [folder, setFolder] = useState<Folder>("open");
  const [q, setQ] = useState("");
  const [list, setList] = useState<SupportConversation[] | null>(null);
  const [counts, setCounts] = useState<SupportCounts | null>(null);
  const [mailboxes, setMailboxes] = useState<SupportMailbox[] | null>(null);
  const [stats, setStats] = useState<{ median_first_reply_seconds: number | null; median_resolution_seconds: number | null; conversations: number } | null>(null);
  const [error, setError] = useState("");
  const [settings, setSettings] = useState(false);
  const [tick, setTick] = useState(0);
  const refresh = useCallback(() => setTick((t) => t + 1), []);
  const { brand } = useBrand(ws);
  const brandQ = brand ? { brand: brand.handle } : {};

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/support/mailboxes", { params: { path: { ws } } }).then(({ data }) => setMailboxes(data?.data ?? []));
    api.GET("/v1/workspaces/{ws}/support/stats", { params: { path: { ws }, query: brandQ } }).then(({ data }) => data && setStats(data));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, tick, brand?.id]);

  useEffect(() => {
    let live = true;
    const t = setTimeout(async () => {
      const { data, error } = await api.GET("/v1/workspaces/{ws}/support/conversations", {
        params: { path: { ws }, query: { ...query(folder), ...brandQ, ...(q.trim() ? { q: q.trim() } : {}), limit: 100 } as never },
      });
      if (!live) return;
      if (error) return setError(errorMessage(error));
      setError("");
      setList(data.data);
      setCounts(data.counts);
    }, q ? 250 : 0);
    return () => {
      live = false;
      clearTimeout(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, folder, q, tick, brand?.id]);

  const frame = useFillHeight();
  if (mailboxes === null || (list === null && !error)) return <Loading />;

  const setUp = mailboxes.length > 0;
  if (settings || (!setUp && !list?.length)) {
    return <Settings ws={ws} canSetUp={canSetUp} mailboxes={mailboxes} first={!setUp} onBack={setUp ? () => setSettings(false) : undefined} onChanged={refresh} go={go} />;
  }

  return (
    <div ref={frame.ref} style={{ height: frame.height }} className="-mx-4 -mb-4 flex min-h-[520px] border-t md:-mx-6 md:-mb-6">
      {/* Folders */}
      <nav aria-label="Support folders" className={cn("hidden w-[200px] shrink-0 flex-col border-r bg-muted/20 px-2 py-3 text-[13px] lg:flex")}>
        <div className="mb-3 flex items-center justify-between px-2">
          <h1 className="text-[15px] font-semibold">Support</h1>
          <button type="button" aria-label="Support settings" title="Support settings" onClick={() => setSettings(true)} className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground">
            <Icon name="settings" className="size-3.5" />
          </button>
        </div>
        <div className="space-y-0.5">
          {folders.map((f) => (
            <button
              key={f.id}
              type="button"
              title={f.hint}
              aria-current={folder === f.id ? "page" : undefined}
              onClick={() => {
                setFolder(f.id);
                setOpen(undefined);
              }}
              className={cn("flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left", folder === f.id ? "bg-accent font-medium text-foreground" : "text-muted-foreground hover:bg-accent/60 hover:text-foreground")}
            >
              <span className="flex-1">{f.label}</span>
              {counts && f.count(counts) ? <span className="text-[11px] tabular-nums">{f.count(counts)}</span> : null}
            </button>
          ))}
        </div>
        <div className="mt-auto space-y-2 border-t px-2 pt-3 text-xs text-muted-foreground">
          <p className="font-mono text-[11px] uppercase">Last 30 days</p>
          <p className="flex justify-between"><span>Conversations</span><span className="tabular-nums text-foreground">{stats?.conversations ?? "–"}</span></p>
          <p className="flex justify-between" title="Median time from the customer's first email to your first reply"><span>First reply</span><span className="tabular-nums text-foreground">{duration(stats?.median_first_reply_seconds)}</span></p>
          <p className="flex justify-between" title="Median time from the first email to closing"><span>Time to close</span><span className="tabular-nums text-foreground">{duration(stats?.median_resolution_seconds)}</span></p>
        </div>
      </nav>

      {/* The list */}
      <section aria-label="Conversations" className={cn("flex w-full shrink-0 flex-col border-r md:w-[300px]", open && "hidden md:flex")}>
        <div className="space-y-2 border-b p-3">
          <div className="flex items-center gap-2 lg:hidden">
            <select aria-label="Folder" className={cn(field, "flex-1")} value={folder} onChange={(e) => setFolder(e.target.value as Folder)}>
              {folders.map((f) => (
                <option key={f.id} value={f.id}>{f.label}{counts && f.count(counts) ? ` (${f.count(counts)})` : ""}</option>
              ))}
            </select>
            <Button variant="ghost" aria-label="Support settings" onClick={() => setSettings(true)}><Icon name="settings" className="size-3.5" /></Button>
          </div>
          <p className="hidden text-[13px] font-medium lg:block">{folders.find((f) => f.id === folder)?.label}</p>
          <input type="search" aria-label="Search conversations" placeholder="Search name, email or words" className={cn(field, "w-full")} value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        {error && <div className="p-3"><ErrorText>{error}</ErrorText></div>}
        <ul className="flex-1 overflow-y-auto">
          {list?.length === 0 && (
            <li className="p-6 text-center text-[13px] text-muted-foreground">
              {q ? "Nothing matches." : folder === "mine" ? "Nothing assigned to you. Pick one up from Unassigned." : folder === "open" ? "All caught up: nobody is waiting on you." : "No conversations here."}
            </li>
          )}
          {list?.map((c) => (
            <li key={c.id}>
              <button
                type="button"
                onClick={() => setOpen(c.id)}
                aria-current={open === c.id ? "true" : undefined}
                className={cn("flex w-full gap-3 border-b px-3 py-3 text-left hover:bg-accent/50", open === c.id && "bg-accent")}
              >
                <Avatar name={c.contact.name} email={c.contact.email} />
                <span className="min-w-0 flex-1">
                  <span className="flex items-baseline gap-2">
                    <span className={cn("min-w-0 flex-1 truncate text-[13px]", c.unread ? "font-semibold" : "font-medium")}>{c.contact.name || c.contact.email}</span>
                    <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">{listTime(c.last_message_at)}</span>
                  </span>
                  <span className={cn("block truncate text-[13px]", c.unread ? "text-foreground" : "text-muted-foreground")}>{c.subject || "(no subject)"}</span>
                  <span className="line-clamp-2 text-xs text-muted-foreground">{c.last_from === "team" && <span className="text-foreground/70">You: </span>}{c.preview}</span>
                  <span className="mt-1 flex flex-wrap items-center gap-1.5">
                    {c.unread && <span className="size-2 rounded-full bg-blue-600" aria-label="Unread" />}
                    {(folder === "all" || folder === "mine") && <Badge tone={statusTone[c.status]}>{statusLabel[c.status]}</Badge>}
                    {c.assignee && folder !== "mine" && <span className="text-[11px] text-muted-foreground">→ {c.assignee.id === myId ? "You" : c.assignee.name}</span>}
                    {c.tags.slice(0, 3).map((t) => <span key={t} className="rounded border bg-muted px-1 font-mono text-[10px] text-muted-foreground">{t}</span>)}
                  </span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      </section>

      {/* The conversation */}
      <div className={cn("min-w-0 flex-1", !open && "hidden md:flex")}>
        {open ? (
          <Conversation key={open} ws={ws} id={open} canManage={canManage} myId={myId} onBack={() => setOpen(undefined)} onChanged={refresh} go={go} />
        ) : (
          <div className="flex flex-1 items-center justify-center p-8 text-center text-[13px] text-muted-foreground">
            <div className="max-w-xs space-y-2">
              <Icon name="support" className="mx-auto size-8 opacity-40" />
              <p>Pick a conversation to read and answer it.</p>
              <p className="text-xs">Mail to {mailboxes.map((m) => m.email).join(", ")} lands here.</p>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function Conversation({ ws, id, canManage, myId, onBack, onChanged, go }: { ws: string; id: string; canManage: boolean; myId: string; onBack: () => void; onChanged: () => void; go: (tab: Tab) => void }) {
  const [conv, setConv] = useState<SupportConversation | null>(null);
  const [messages, setMessages] = useState<SupportMessage[]>([]);
  const [others, setOthers] = useState<SupportConversation[]>([]);
  const [members, setMembers] = useState<Member[]>([]);
  const [teams, setTeams] = useState<Team[]>([]);
  const [error, setError] = useState("");
  const [mode, setMode] = useState<"reply" | "note">("reply");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [suggesting, setSuggesting] = useState(false);
  const [suggestion, setSuggestion] = useState<KnowledgeAnswer | null>(null);
  const end = useRef<HTMLDivElement>(null);
  const path = { ws, id };

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/support/conversations/{id}", { params: { path } });
    if (error) return setError(errorMessage(error));
    setConv(data.conversation);
    setMessages(data.messages);
    setOthers(data.other_conversations);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, id]);

  useEffect(() => {
    load().then(onChanged);
    api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) => setMembers((data as { data?: Member[] } | undefined)?.data ?? []));
    api.GET("/v1/workspaces/{ws}/teams", { params: { path: { ws } } }).then(({ data }) => setTeams(((data as { data?: Team[] } | undefined)?.data ?? []) as Team[]));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [load, ws]);

  // A reply shows as sending until the job has sent it.
  useEffect(() => {
    if (!messages.some((m) => m.status === "sending")) return;
    const t = setTimeout(load, 1500);
    return () => clearTimeout(t);
  }, [messages, load]);

  useEffect(() => {
    end.current?.scrollIntoView({ block: "end" });
  }, [messages.length]);

  async function patch(change: Record<string, unknown>, done?: string) {
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/support/conversations/{id}", { params: { path }, body: change as never });
    if (error) return toast(errorMessage(error), "error");
    setConv(data as SupportConversation);
    onChanged();
    if (done) toast(done);
  }

  async function send(after?: "closed") {
    if (!body.trim() || busy) return;
    setBusy(true);
    const res =
      mode === "note"
        ? await api.POST("/v1/workspaces/{ws}/support/conversations/{id}/notes", { params: { path }, body: { body } })
        : await api.POST("/v1/workspaces/{ws}/support/conversations/{id}/replies", { params: { path }, body: { body, ...(after ? { status: after } : {}) } });
    setBusy(false);
    if (res.error) return toast(errorMessage(res.error), "error");
    setBody("");
    setSuggestion(null);
    await load();
    onChanged();
    if (after) toast("Sent and closed");
  }

  async function suggest() {
    setSuggesting(true);
    setSuggestion(null);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/support/conversations/{id}/suggest-reply", { params: { path } });
    setSuggesting(false);
    if (error) return toast(errorMessage(error), "error");
    setSuggestion(data);
    if (data.answer && data.answered) {
      setMode("reply");
      setBody(data.answer);
    }
  }

  if (error) return <div className="p-6"><ErrorText>{error}</ErrorText></div>;
  if (!conv) return <div className="flex-1"><Loading /></div>;
  const closed = conv.status === "closed";
  const canReply = !!conv.mailbox_id;

  return (
    <div className="flex h-full min-w-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center gap-2 border-b px-4 py-3">
          <button type="button" onClick={onBack} aria-label="Back to the list" className="rounded p-1 text-muted-foreground hover:bg-accent md:hidden">
            <Icon name="chevron" className="size-4 rotate-180" />
          </button>
          <div className="min-w-0 flex-1">
            <h2 className="line-clamp-2 text-[15px] leading-snug font-semibold">{conv.subject || "(no subject)"}</h2>
            <p className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
              <span className={cn("size-1.5 shrink-0 rounded-full", { open: "bg-blue-600", pending: "bg-amber-500", closed: "bg-muted-foreground/50" }[conv.status])} />
              <span className="shrink-0 text-foreground/80">{statusLabel[conv.status]}</span>
              <span className="truncate">· {conv.contact.name ? `${conv.contact.name} · ` : ""}{conv.contact.email}</span>
            </p>
          </div>
          {canManage && (
            closed ? (
              <Button variant="ghost" onClick={() => patch({ status: "open" }, "Reopened")}>Reopen</Button>
            ) : (
              <Button onClick={() => patch({ status: "closed" }, "Closed")}><Icon name="check" className="size-3.5" /> Close</Button>
            )
          )}
          {canManage && (
            <Menu
              aria="More actions"
              items={[
                conv.assignee?.id !== myId && !!myId && { label: "Assign to me", onClick: () => patch({ assignee_id: myId }, "Assigned to you") },
                conv.status !== "pending" && !closed && { label: "Mark as waiting on customer", onClick: () => patch({ status: "pending" }) },
                { label: "Mark as unread", onClick: () => patch({ unread: true }).then(onBack) },
              ]}
            />
          )}
        </header>

        <div className="flex-1 space-y-4 overflow-y-auto bg-muted/20 px-4 py-5">
          {messages.map((m) => <MessageView key={m.id} m={m} contact={conv.contact} />)}
          <div ref={end} />
        </div>

        {canManage && (
          <div className="border-t bg-background p-3">
            {suggestion && <SuggestionNote s={suggestion} onConnectAI={() => go("ai")} onDismiss={() => setSuggestion(null)} onUse={(t) => { setMode("reply"); setBody(t); }} />}
            <div className={cn("rounded-lg border focus-within:ring-[3px] focus-within:ring-ring/40", mode === "note" && "border-amber-500/40 bg-amber-50/60 dark:bg-amber-500/5")}>
              <div className="flex items-center gap-1 border-b px-2 py-1.5" role="tablist" aria-label="Write">
                {(["reply", "note"] as const).map((k) => (
                  <button
                    key={k}
                    type="button"
                    role="tab"
                    aria-selected={mode === k}
                    onClick={() => setMode(k)}
                    className={cn("rounded px-2 py-0.5 text-xs font-medium", mode === k ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground")}
                  >
                    {k === "reply" ? "Reply" : "Note"}
                  </button>
                ))}
                <span className="ml-1 truncate text-[11px] text-muted-foreground">
                  {mode === "note" ? "Only your team sees notes" : canReply ? `To ${conv.contact.email}, from ${conv.mailbox_email}` : "This conversation's support mailbox was removed"}
                </span>
                <span className="flex-1" />
                {mode === "reply" && (
                  <button
                    type="button"
                    onClick={suggest}
                    disabled={suggesting}
                    className="inline-flex shrink-0 items-center gap-1 rounded px-2 py-0.5 text-xs font-medium text-violet-700 hover:bg-violet-500/10 disabled:opacity-60 dark:text-violet-400"
                  >
                    <Icon name="ai" className="size-3.5" />{suggesting ? "Drafting…" : "Suggest a reply"}
                  </button>
                )}
              </div>
              <textarea
                aria-label={mode === "note" ? "Note" : "Reply"}
                rows={4}
                value={body}
                onChange={(e) => setBody(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) send();
                }}
                placeholder={mode === "note" ? "Add a note for your team…" : "Write your reply…"}
                className="block max-h-72 min-h-24 w-full resize-y bg-transparent px-3 py-2 text-[13px] outline-none"
              />
              <div className="flex items-center justify-end gap-2 px-2 pb-2">
                <span className="mr-auto hidden text-[11px] text-muted-foreground sm:inline">⌘↵ to send</span>
                {mode === "reply" && <Button variant="ghost" disabled={!body.trim() || busy || !canReply} onClick={() => send("closed")}>Send and close</Button>}
                <Button disabled={!body.trim() || busy || (mode === "reply" && !canReply)} onClick={() => send()}>{busy ? "Sending…" : mode === "note" ? "Add note" : "Send"}</Button>
              </div>
            </div>
          </div>
        )}
      </div>

      <Details conv={conv} others={others} members={members} teams={teams} canManage={canManage} myId={myId} onPatch={patch} go={go} />
    </div>
  );
}

function MessageView({ m, contact }: { m: SupportMessage; contact: SupportConversation["contact"] }) {
  if (m.kind === "note") {
    return (
      <div className="mx-auto max-w-2xl rounded-lg border border-amber-500/30 bg-amber-50 px-3 py-2 text-[13px] dark:bg-amber-500/10">
        <p className="mb-1 text-[11px] text-amber-800 dark:text-amber-300">Note by {m.author?.name ?? "someone"} · <span title={stamp(m.created_at)}>{ago(m.created_at)}</span></p>
        <p className="break-words whitespace-pre-wrap">{m.body}</p>
      </div>
    );
  }
  const mine = m.kind === "out";
  return (
    <div className={cn("flex gap-2", mine && "flex-row-reverse")}>
      {!mine && <Avatar name={m.from_name || contact.name} email={m.from_email} className="size-7 text-[10px]" />}
      <div className={cn("max-w-[min(42rem,85%)] min-w-0")}>
        <p className={cn("mb-1 text-[11px] text-muted-foreground", mine && "text-right")}>
          {mine ? (m.author?.name ?? m.from_name ?? "Your team") : m.from_name || m.from_email} · <span title={stamp(m.created_at)}>{ago(m.created_at)}</span>
          {m.auto_reply && " · automatic reply"}
        </p>
        <div className={cn("rounded-2xl px-3.5 py-2.5 text-[13px] leading-relaxed shadow-xs", mine ? "rounded-tr-sm bg-primary text-primary-foreground" : "rounded-tl-sm border bg-background", m.auto_reply && "opacity-70")}>
          <p className="break-words whitespace-pre-wrap">{m.body}</p>
        </div>
        {mine && (
          <p className="mt-1 text-right text-[11px]">
            {m.status === "sending" && <span className="text-muted-foreground">Sending…</span>}
            {m.status === "sent" && <span className="text-muted-foreground">Sent</span>}
            {m.status === "failed" && <span className="text-destructive" title={m.error ?? undefined}>Not sent: {m.error}</span>}
          </p>
        )}
      </div>
    </div>
  );
}

function SuggestionNote({ s, onUse, onDismiss, onConnectAI }: { s: KnowledgeAnswer; onUse: (text: string) => void; onDismiss: () => void; onConnectAI: () => void }) {
  let text: ReactNode;
  if (s.ai.problem) {
    text = (
      <>
        {s.ai.problem.message}{" "}
        {s.ai.problem.code !== "connector_auth" && <button type="button" className="underline" onClick={onConnectAI}>Connect an AI model</button>}
      </>
    );
  } else if (!s.answered) {
    text = "Your knowledge doesn't cover this yet, so there's no draft. Write the answer, then add it to Knowledge so next time it's there.";
  } else {
    text = <>Draft written from {plural(s.citations.length, "article")} customers may be told. Check it, edit it, then send.</>;
  }
  return (
    <div className="mb-2 flex items-start gap-2 rounded-md border border-violet-500/30 bg-violet-500/5 px-3 py-2 text-xs">
      <Icon name="ai" className="mt-0.5 size-3.5 shrink-0 text-violet-600" />
      <div className="min-w-0 flex-1 space-y-1">
        <p>{text}</p>
        {s.answered && s.citations.length > 0 && (
          <p className="truncate text-muted-foreground">Sources: {s.citations.map((c) => c.title).join(", ")}</p>
        )}
        {!s.answered && s.answer && !s.ai.problem && s.passages.length > 0 && (
          <button type="button" className="underline" onClick={() => onUse(s.answer ?? "")}>Use what it found anyway</button>
        )}
      </div>
      <button type="button" aria-label="Dismiss" onClick={onDismiss} className="text-muted-foreground hover:text-foreground"><Icon name="x" className="size-3.5" /></button>
    </div>
  );
}

function Details({
  conv,
  others,
  members,
  teams,
  canManage,
  myId,
  onPatch,
  go,
}: {
  conv: SupportConversation;
  others: SupportConversation[];
  members: Member[];
  teams: Team[];
  canManage: boolean;
  myId: string;
  onPatch: (change: Record<string, unknown>) => void;
  go: (tab: Tab) => void;
}) {
  const [tag, setTag] = useState("");
  function addTag() {
    const t = tag.trim().toLowerCase();
    if (!t || conv.tags.includes(t)) return setTag("");
    onPatch({ tags: [...conv.tags, t] });
    setTag("");
  }
  return (
    <aside aria-label="Details" className="hidden w-[248px] shrink-0 space-y-5 overflow-y-auto border-l px-4 py-4 text-[13px] xl:block">
      <section className="space-y-2">
        <div className="flex items-center gap-2">
          <Avatar name={conv.contact.name} email={conv.contact.email} />
          <div className="min-w-0">
            <p className="truncate font-medium">{conv.contact.name || conv.contact.email}</p>
            <p className="truncate text-xs text-muted-foreground">{conv.contact.email}</p>
          </div>
        </div>
        {conv.record_id && (
          <button type="button" className="text-xs text-muted-foreground underline hover:text-foreground" onClick={() => go("people")}>Open in People</button>
        )}
      </section>

      <section className="space-y-3">
        <label className="block space-y-1">
          <span className="text-xs text-muted-foreground">Assignee</span>
          <select
            className={cn(field, "w-full")}
            disabled={!canManage}
            value={conv.assignee?.id ?? ""}
            onChange={(e) => onPatch({ assignee_id: e.target.value || null })}
          >
            <option value="">Unassigned</option>
            {members.map((m) => (
              <option key={m.user_id} value={m.user_id}>{m.user_id === myId ? `You (${m.name || m.email})` : m.name || m.email}</option>
            ))}
          </select>
        </label>
        <label className="block space-y-1">
          <span className="text-xs text-muted-foreground">Team</span>
          <select className={cn(field, "w-full")} disabled={!canManage} value={conv.team?.id ?? ""} onChange={(e) => onPatch({ team_id: e.target.value || null })}>
            <option value="">No team</option>
            {teams.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
          </select>
        </label>
        <div className="space-y-1">
          <span className="text-xs text-muted-foreground">Tags</span>
          <div className="flex flex-wrap gap-1">
            {conv.tags.map((t) => (
              <span key={t} className="inline-flex items-center gap-1 rounded border bg-muted px-1.5 font-mono text-[11px]">
                {t}
                {canManage && (
                  <button type="button" aria-label={`Remove tag ${t}`} onClick={() => onPatch({ tags: conv.tags.filter((x) => x !== t) })} className="text-muted-foreground hover:text-foreground">×</button>
                )}
              </span>
            ))}
          </div>
          {canManage && (
            <input
              aria-label="Add a tag"
              placeholder="Add a tag"
              className={cn(field, "w-full")}
              value={tag}
              maxLength={40}
              onChange={(e) => setTag(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  addTag();
                }
              }}
              onBlur={addTag}
            />
          )}
        </div>
      </section>

      <section className="space-y-1.5 text-xs">
        <p className="font-mono text-[11px] text-muted-foreground uppercase">Times</p>
        <p className="flex justify-between"><span className="text-muted-foreground">Started</span><span title={stamp(conv.first_message_at)}>{ago(conv.first_message_at)}</span></p>
        <p className="flex justify-between">
          <span className="text-muted-foreground">First reply</span>
          <span>{conv.first_reply_at ? duration((Date.parse(conv.first_reply_at) - Date.parse(conv.first_message_at)) / 1000) + " after" : "Not yet"}</span>
        </p>
        {conv.closed_at && <p className="flex justify-between"><span className="text-muted-foreground">Closed</span><span title={stamp(conv.closed_at)}>{ago(conv.closed_at)}</span></p>}
        <p className="flex justify-between"><span className="text-muted-foreground">Via</span><span className="truncate pl-2">{conv.mailbox_email ?? "removed mailbox"}</span></p>
      </section>

      {others.length > 0 && (
        <section className="space-y-1.5">
          <p className="font-mono text-[11px] text-muted-foreground uppercase">Earlier conversations</p>
          <ul className="space-y-1">
            {others.map((o) => (
              <li key={o.id} className="flex items-center gap-2 text-xs">
                <span className={cn("size-1.5 shrink-0 rounded-full", o.status === "closed" ? "bg-muted-foreground/40" : "bg-blue-600")} />
                <span className="min-w-0 flex-1 truncate">{o.subject || "(no subject)"}</span>
                <span className="text-muted-foreground">{listTime(o.last_message_at)}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </aside>
  );
}

function Settings({
  ws,
  canSetUp,
  mailboxes,
  first,
  onBack,
  onChanged,
  go,
}: {
  ws: string;
  canSetUp: boolean;
  mailboxes: SupportMailbox[];
  first: boolean;
  onBack?: () => void;
  onChanged: () => void;
  go: (tab: Tab) => void;
}) {
  const [all, setAll] = useState<Mailbox[] | null>(null);
  const [pick, setPick] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/mailboxes", { params: { path: { ws } } }).then(({ data }) => setAll((data as { data?: Mailbox[] } | undefined)?.data ?? []));
  }, [ws, mailboxes.length]);

  const chosen = new Set(mailboxes.map((m) => m.id));
  const candidates = (all ?? []).filter((m) => !chosen.has(m.id) && m.has_login);

  async function add() {
    if (!pick) return;
    setBusy(true);
    setError("");
    const { error } = await api.PUT("/v1/workspaces/{ws}/support/mailboxes/{id}", { params: { path: { ws, id: pick } } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    track("ui.support_mailbox_picked", { first });
    setPick("");
    toast("Its new mail now comes to Support");
    onChanged();
  }

  async function remove(m: SupportMailbox) {
    const ok = await confirmAction(
      `Stop using ${m.email} for support?`,
      "Its new mail goes to the sales inbox again. Conversations stay here, but can't be answered by email until you choose a support mailbox again.",
      "Stop using it",
    );
    if (!ok) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/support/mailboxes/{id}", { params: { path: { ws, id: m.id } } });
    if (error) return toast(errorMessage(error), "error");
    onChanged();
  }

  return (
    <div className="mx-auto max-w-2xl space-y-6 py-2">
      <div className="space-y-1">
        {onBack && (
          <button type="button" onClick={onBack} className="text-xs text-muted-foreground hover:text-foreground">← Back to the inbox</button>
        )}
        <h1 className="text-xl font-semibold">{first ? "Answer your customers here" : "Support settings"}</h1>
        <p className="text-[13px] text-muted-foreground">
          {first
            ? "Choose the mailbox customers write to, such as support@ or help@. Every email to it becomes a conversation your team can assign, answer in the same thread and close, with your knowledge one click away."
            : "Mail to these mailboxes comes to Support instead of the sales inbox, and replies go out from them. Sequences never send from a support mailbox."}
        </p>
      </div>

      {first && (
        <ol className="grid gap-3 sm:grid-cols-3">
          {[
            ["Choose a mailbox", "One you've connected with its login, used only for support."],
            ["Customers write in", "Each email becomes a conversation, threaded with replies."],
            ["Answer as a team", "Assign, leave notes, suggest a reply from knowledge, and close."],
          ].map(([t, d], i) => (
            <li key={t} className="rounded-lg border p-3 text-[13px]">
              <span className="mb-2 inline-flex size-5 items-center justify-center rounded-full bg-primary text-[11px] font-semibold text-primary-foreground">{i + 1}</span>
              <p className="font-medium">{t}</p>
              <p className="text-xs text-muted-foreground">{d}</p>
            </li>
          ))}
        </ol>
      )}

      {mailboxes.length > 0 && (
        <ul className="divide-y rounded-lg border">
          {mailboxes.map((m) => (
            <li key={m.id} className="flex items-center gap-3 px-4 py-3 text-[13px]">
              <Icon name="mailboxes" className="size-4 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <p className="truncate font-medium">{m.email}</p>
                <p className="text-xs text-muted-foreground">
                  {m.sync_error ? <span className="text-destructive">Can't read it: {m.sync_error}</span> : m.last_synced_at ? `Checked ${ago(m.last_synced_at)}` : "Not checked yet"}
                  {m.status !== "active" && ` · ${m.status}`}
                </p>
              </div>
              {canSetUp && <Button variant="danger" onClick={() => remove(m)}>Stop using</Button>}
            </li>
          ))}
        </ul>
      )}

      {canSetUp ? (
        <div className="space-y-2 rounded-lg border p-4">
          <p className="text-[13px] font-medium">{mailboxes.length ? "Add another mailbox" : "Your support mailbox"}</p>
          {all === null ? (
            <Loading />
          ) : candidates.length === 0 ? (
            <Empty title={mailboxes.length ? "No other mailbox to add" : "No mailbox to choose yet"} action={<Button onClick={() => go("mailboxes")}>Connect a mailbox</Button>}>
              Connect the mailbox customers write to (Google, Microsoft or any IMAP and SMTP mailbox) under Mailboxes, then choose it here.
            </Empty>
          ) : (
            <div className="flex flex-col gap-2 sm:flex-row">
              <select aria-label="Mailbox" className={cn(field, "flex-1")} value={pick} onChange={(e) => setPick(e.target.value)}>
                <option value="">Choose a connected mailbox…</option>
                {candidates.map((m) => (
                  <option key={m.id} value={m.id}>{m.email}{m.assignment ? " (sends for a sequence)" : ""}</option>
                ))}
              </select>
              <Button disabled={!pick || busy} onClick={add}>{busy ? "Adding…" : "Use for support"}</Button>
            </div>
          )}
          {error && <ErrorText>{error}</ErrorText>}
          <p className="text-xs text-muted-foreground">
            A mailbox that sends cold email can't be used: take it off its sequences first, so customers' mail never mixes with replies to outreach. Forwarding from your own support address is coming next.
          </p>
        </div>
      ) : (
        <p className="text-[13px] text-muted-foreground">Ask someone who manages mailboxes to choose the support mailbox.</p>
      )}
    </div>
  );
}
