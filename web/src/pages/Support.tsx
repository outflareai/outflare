import { PickBrand } from "@/components/pick-brand";
import { useCallback, useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from "react";
import type { InboxView, Mailbox, Member, SupportArticle, SupportAssistantRun, SupportConversation, SupportCounts, SupportMailbox, SupportMessage, SupportPerson } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, confirmAction, Empty, ErrorText, field, Loading, Menu, toast, toastUndo } from "../ui";
import { Icon } from "../icons";
import { ago, listTime, stamp } from "../format";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { FlagButton } from "./Signals";
import type { Tab } from "../App";
import { SupportForwarding } from "@/components/support-forwarding";
import { SupportSLA } from "@/components/support-sla";
import { SupportChat } from "@/components/support-chat";
import { AssistantDraft, SupportAssistantSettings } from "@/components/support-assistant";
import { SupportHandoffTopics } from "@/components/support-topics";
import { SalesReplies } from "./Inbox";
import { SavedReplies } from "@/components/saved-replies";
import { BulkBar, RowMore, useRowMenu, type RowAction } from "@/components/row-menu";
import { PresenceSwitch } from "@/components/inbox-presence";
import { NewCount, useLoopIns } from "@/components/loop-ins";
import { AlertsSwitch, useNewConversations } from "@/components/inbox-alerts";
import { useViewers, ViewingNow } from "@/components/viewing";
import { HandOff, salesTeam, SpottedBanner, useSpotted } from "@/components/hand-off";
import { askToWrite, KeysButton, neighbour, useInboxKeys, writeEvent } from "@/components/inbox-keys";
import { SupportImports } from "@/components/support-import";

// The support inbox: customers who write to a support mailbox, as
// conversations the team works through. Folders on the left, the list in
// the middle, the conversation and its composer on the right, with the
// customer's details beside it. A reply goes out from the support mailbox,
// in the same thread. Suggest a reply drafts one from knowledge customers
// may be told, only when someone asks.

type Folder = "mine" | "watching" | "unassigned" | "overdue" | "open" | "pending" | "closed" | "all" | "sales";
type Team = { id: string; name: string };

const folders: { id: Folder; label: string; hint: string; count: (c: SupportCounts) => number | undefined }[] = [
  { id: "mine", label: "Your inbox", hint: "Assigned to you and not closed", count: (c) => c.mine },
  { id: "watching", label: "Watching", hint: "Conversations a teammate looped you in on", count: () => undefined },
  { id: "unassigned", label: "Unassigned", hint: "Nobody has picked these up yet", count: (c) => c.unassigned },
  { id: "overdue", label: "Overdue", hint: "Past the reply or resolution time you promised", count: (c) => c.overdue },
  { id: "open", label: "Open", hint: "Waiting on your team", count: (c) => c.open },
  { id: "pending", label: "Waiting on customer", hint: "You replied; it opens again when they write", count: (c) => c.pending },
  { id: "closed", label: "Closed", hint: "Done", count: () => undefined },
  { id: "all", label: "All conversations", hint: "Everything", count: () => undefined },
];

function query(f: Folder, salesTeamId = ""): Record<string, string> {
  switch (f) {
    case "sales":
      // Conversations support handed to the sales team.
      return { team_id: salesTeamId || "00000000-0000-0000-0000-000000000000", status: "all" };
    case "mine":
      return { assignee: "me", status: "all" };
    case "watching":
      return { following: "me", status: "all" };
    case "unassigned":
      return { assignee: "none", status: "open" };
    case "overdue":
      return { status: "all", sla: "overdue", sort: "due" };
    case "all":
      return { status: "all" };
    default:
      return { status: f };
  }
}

// The one Inbox puts replies to outreach beside support: these are the
// sales views in its views column, and what each asks the replies list for.
type SalesStatus = "open" | "watching" | "snoozed" | "done" | "all";
const salesViews: { id: SalesStatus; label: string; hint: string }[] = [
  { id: "open", label: "New replies", hint: "Replies to your outreach that still need an answer" },
  { id: "watching", label: "Watching", hint: "Replies a teammate looped you in on" },
  { id: "snoozed", label: "Snoozed", hint: "Put aside until later; they come back on their own" },
  { id: "done", label: "Done", hint: "Answered or dealt with" },
  { id: "all", label: "All replies", hint: "Everything" },
];
// What is picked in the views column: a support folder, a sales view or
// a saved view of replies.
type Picked = { kind: "support"; folder: Folder } | { kind: "sales"; status: SalesStatus; view?: InboxView };
const pickedKey = (p: Picked) => (p.kind === "support" ? `support:${p.folder}` : p.view ? `view:${p.view.id}` : `sales:${p.status}`);

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

// chatLive says whether a chat visitor is likely still on the site: their
// chat checks in every few seconds while it is open.
function chatLive(c: SupportConversation) {
  return c.channel === "chat" && !!c.visitor_seen_at && Date.now() - Date.parse(c.visitor_seen_at) < 60_000;
}

// chatLeft says whether a chat visitor has gone (no check-in for two
// minutes): a reply then also goes to their email when they left one and
// the brand has a support mailbox.
function chatLeft(c: SupportConversation) {
  return c.channel === "chat" && (!c.visitor_seen_at || Date.now() - Date.parse(c.visitor_seen_at) > 120_000);
}

// who is how a conversation's customer is shown: a chat visitor who gave
// no email is a website visitor.
function who(c: SupportConversation) {
  return c.contact.name || c.contact.email || "Website visitor";
}

// SlaChip says when the reply or the resolution is due, or how late it is.
function SlaChip({ c, long = false }: { c: SupportConversation; long?: boolean }) {
  const { sla } = c;
  if (c.status === "closed") return null;
  const now = Date.now();
  const late = sla.reply_breached_at ?? sla.resolve_breached_at;
  if (late) {
    const what = sla.reply_breached_at ? "Reply" : "Resolution";
    const due = (sla.reply_breached_at ? sla.reply_due_at : sla.resolve_due_at) ?? late;
    return (
      <span className="inline-flex shrink-0 items-center rounded bg-red-500/10 px-1 text-[11px] font-medium whitespace-nowrap text-red-700 dark:text-red-400" title={`${what} was due ${stamp(due)}`}>
        {`${what === "Reply" ? "Reply" : "Resolve"} overdue ${long ? "by " : ""}${duration((now - Date.parse(due)) / 1000)}`}
      </span>
    );
  }
  const due = sla.reply_due_at ?? null;
  if (!due) return null;
  const left = (Date.parse(due) - now) / 1000;
  return (
    <span className={cn("inline-flex shrink-0 items-center rounded px-1 text-[11px] font-medium whitespace-nowrap", left < 3600 ? "bg-amber-500/10 text-amber-700 dark:text-amber-400" : "text-muted-foreground")} title={`Reply due ${stamp(due)}`}>
      {left <= 0 ? "Reply due now" : `Reply in ${duration(left)}`}
    </span>
  );
}

// Support belongs to one brand.
export function Support(props: {
  ws: string;
  canManage: boolean;
  canSetUp: boolean;
  myId: string;
  open?: string;
  setOpen: (id?: string) => void;
  go: (tab: Tab, item?: string) => void;
  // The one Inbox: support and replies to outreach in one frame. An open
  // support conversation is "support-<id>" in the address, a reply its id.
  unified?: boolean;
  onRead?: () => void;
}) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Support conversations" brands={active.length} />;
  return <SupportPage key={brand.id} {...props} />;
}

function SupportPage({
  ws,
  canManage,
  canSetUp,
  myId,
  open: item,
  setOpen: setItem,
  go,
  unified = false,
  onRead,
}: {
  ws: string;
  canManage: boolean;
  canSetUp: boolean;
  myId: string;
  open?: string;
  setOpen: (id?: string) => void;
  go: (tab: Tab, item?: string) => void;
  unified?: boolean;
  onRead?: () => void;
}) {
  const itemIsSupport = !unified || !!item?.startsWith("support-");
  const open = !item || !itemIsSupport ? undefined : unified ? item.slice("support-".length) : item;
  const openReply = unified && item && !itemIsSupport ? item : undefined;
  const setOpen = (id?: string) => setItem(id && unified ? `support-${id}` : id);
  // New loop-ins: counted on the Watching views until opened.
  const loopIns = useLoopIns(unified ? myId : "", setItem);
  useEffect(() => {
    if (!item) return;
    const t = setTimeout(loopIns.reload, 1500);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [item]);
  // Nothing picked yet means: support when it is set up, replies when not.
  const [picked, setPicked] = useState<Picked | null>(() => {
    if (openReply) return { kind: "sales", status: "all" };
    if (!unified || item) return { kind: "support", folder: item ? "all" : "open" };
    try {
      const last = localStorage.getItem("outflare.inbox.view") ?? "";
      const sales = salesViews.find((v) => `sales:${v.id}` === last);
      if (sales) return { kind: "sales", status: sales.id };
      const f = folders.find((x) => `support:${x.id}` === last);
      if (f) return { kind: "support", folder: f.id };
    } catch {
      // No storage: start from the default.
    }
    return null;
  });
  const folder: Folder = picked?.kind === "support" ? picked.folder : "open";
  const pick = (p: Picked) => {
    setPicked(p);
    setItem(undefined);
    try {
      if (unified && !(p.kind === "sales" && p.view)) localStorage.setItem("outflare.inbox.view", pickedKey(p));
    } catch {
      // Remembering the last view is a convenience only.
    }
  };
  const setFolder = (f: Folder) => pick({ kind: "support", folder: f });
  // The sales team, for the "From support" view of what support handed over.
  const [salesTeamId, setSalesTeamId] = useState("");
  // Everyone and every team, for Assign to, Move to team and Loop in on a
  // row's menu and on the ticked rows.
  const [allTeams, setAllTeams] = useState<Team[]>([]);
  const [people, setPeople] = useState<Member[]>([]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/teams", { params: { path: { ws } } }).then(({ data }) => {
      const teams = ((data as { data?: Team[] } | undefined)?.data ?? []) as Team[];
      setAllTeams(teams);
      setSalesTeamId(salesTeam(teams)?.id ?? "");
    });
    api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) => setPeople(((data as { data?: Member[] } | undefined)?.data ?? []).filter((m) => m.user_id)));
  }, [ws]);
  // Sales conversations (CRM) are part of the Inbox for the people who work
  // them: its queues are linked under Sales, with a dot when something is
  // new for this person.
  const [salesFloor, setSalesFloor] = useState<{ fresh: boolean; unread: boolean } | null>(null);
  useEffect(() => {
    if (!unified) return;
    let live = true;
    (async () => {
      const brand = pickedBrandId();
      const me = await api.GET("/v1/brands/{brand}/sales/me", { params: { path: { brand } } });
      if (!live || !me.data || me.data.role === "none") return;
      const { data } = await api.GET("/v1/brands/{brand}/sales/leads", { params: { path: { brand }, query: { queue: "new", limit: 1 } } });
      if (live && data) setSalesFloor({ fresh: !!data.new_unseen, unread: !!data.unread });
    })();
    return () => {
      live = false;
    };
  }, [unified]);
  const [replyViews, setReplyViews] = useState<InboxView[]>([]);
  const [viewsTick, setViewsTick] = useState(0);
  useEffect(() => {
    if (!unified) return;
    api.GET("/v1/brands/{brand}/inbox-views", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => setReplyViews(data?.data ?? []));
  }, [unified, viewsTick]);
  const [q, setQ] = useState("");
  const [list, setList] = useState<SupportConversation[] | null>(null);
  const [counts, setCounts] = useState<SupportCounts | null>(null);
  const [mailboxes, setMailboxes] = useState<SupportMailbox[] | null>(null);
  // Support addresses mail is forwarded to (Outflare Cloud).
  const [forwarding, setForwarding] = useState<string[] | null>(null);
  // Answers and conflicts waiting in Knowledge, Review.
  const [review, setReview] = useState(0);
  const [stats, setStats] = useState<{ median_first_reply_seconds: number | null; median_resolution_seconds: number | null; conversations: number; sla_missed?: number } | null>(null);
  const [error, setError] = useState("");
  const [settings, setSettings] = useState(false);
  const [tick, setTick] = useState(0);
  const refresh = useCallback(() => setTick((t) => t + 1), []);
  // Something new to answer: reload the list only, and say so.
  const [listTick, setListTick] = useState(0);
  useNewConversations(myId, (id) => setOpen(id), () => setListTick((t) => t + 1));
  const { brand } = useBrand(ws);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/support/mailboxes", { params: { path: { ws } } }).then(({ data }) => setMailboxes(data?.data ?? []));
    api.GET("/v1/workspaces/{ws}/inbound-addresses", { params: { path: { ws } } }).then(({ data }) =>
      setForwarding((data?.data ?? []).filter((a) => a.purpose === "support").map((a) => a.email)),
    );
    api.GET("/v1/brands/{brand}/support/stats", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => data && setStats(data));
    api.GET("/v1/brands/{brand}/knowledge/suggestions", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => data && setReview(data.counts.answers + data.counts.conflicts));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, tick, brand?.id]);

  useEffect(() => {
    let live = true;
    const t = setTimeout(async () => {
      const { data, error } = await api.GET("/v1/brands/{brand}/support/conversations", {
        params: { path: { brand: pickedBrandId() }, query: { ...query(folder, salesTeamId), ...(q.trim() ? { q: q.trim() } : {}), limit: 100 } as never },
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
  }, [ws, folder, q, tick, listTick, brand?.id, salesTeamId]);

  // What a row offers on a right-click or from its More button, and what
  // the ticked rows can have done to them together.
  const rowMenu = useRowMenu();
  const [sel, setSel] = useState<string[]>([]);
  const toggle = (id: string) => setSel((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]));
  async function act(ids: string[], change: Record<string, unknown>, said: string, undo?: Record<string, unknown>) {
    const res = await Promise.all(ids.map((id) => api.PATCH("/v1/brands/{brand}/support/conversations/{id}", { params: { path: { brand: pickedBrandId(), id } }, body: change as never })));
    const bad = res.find((r) => r.error);
    if (bad) toast(errorMessage(bad.error), "error");
    else if (undo) toastUndo(said, () => void act(ids, undo, "Undone."));
    else toast(said);
    setSel([]);
    refresh();
  }
  async function loopInMany(ids: string[], m: Member) {
    const res = await Promise.all(ids.map((id) => api.PUT("/v1/brands/{brand}/support/conversations/{id}/followers/{user}", { params: { path: { brand: pickedBrandId(), id, user: m.user_id! } } })));
    const bad = res.find((r) => r.error);
    if (bad) toast(errorMessage(bad.error), "error");
    else toast(`${m.name || m.email} is looped in${ids.length > 1 ? ` on ${ids.length}` : ""}. It shows under their Watching view.`);
    setSel([]);
    refresh();
  }
  // Choices that open inside the menu: who to assign to, which team, who
  // to loop in. now is what one conversation has, to tick and to undo to.
  const assignTo = (ids: string[], now?: SupportConversation): RowAction[] => [
    ...people.map((m) => ({ label: `${now?.assignee?.id === m.user_id ? "✓ " : ""}${m.user_id === myId ? "Me" : m.name || m.email}`, onClick: () => act(ids, { assignee_id: m.user_id }, `Assigned${ids.length > 1 ? ` ${ids.length}` : ""} to ${m.user_id === myId ? "you" : m.name || m.email}.`, now ? { assignee_id: now.assignee?.id ?? null } : undefined) })),
    null,
    { label: "Nobody", onClick: () => act(ids, { assignee_id: null }, "Unassigned.", now ? { assignee_id: now.assignee?.id ?? null } : undefined) },
  ];
  const moveTo = (ids: string[], now?: SupportConversation): RowAction[] => [
    ...allTeams.map((t) => ({ label: `${now?.team?.id === t.id ? "✓ " : ""}${t.name}`, onClick: () => act(ids, { team_id: t.id }, `Moved${ids.length > 1 ? ` ${ids.length}` : ""} to ${t.name}.`, now ? { team_id: now.team?.id ?? null } : undefined) })),
    allTeams.length > 0 && null,
    allTeams.length > 0 && { label: "No team", onClick: () => act(ids, { team_id: null }, "Moved out of its team.", now ? { team_id: now.team?.id ?? null } : undefined) },
  ];
  const loopInTo = (ids: string[]): RowAction[] => people.filter((m) => m.user_id !== myId).map((m) => ({ label: m.name || m.email || "Teammate", onClick: () => void loopInMany(ids, m) }));
  const linkTo = (id: string) => `${location.origin}${location.pathname.replace(/\/(inbox|support)(\/.*)?$/, "")}/${unified ? `inbox/support-${id}` : `support/${id}`}`;
  function rowActions(c: SupportConversation): RowAction[] {
    return [
      { label: "Open in a new tab", onClick: () => window.open(linkTo(c.id), "_blank", "noopener") },
      canManage && !c.unread && { label: "Mark as unread", onClick: () => act([c.id], { unread: true }, "Marked as unread.") },
      canManage && !!myId && c.assignee?.id !== myId && { label: "Assign to me", onClick: () => act([c.id], { assignee_id: myId }, "Assigned to you.", { assignee_id: c.assignee?.id ?? null }) },
      canManage && { label: "Assign to", items: assignTo([c.id], c) },
      canManage && { label: "Move to team", items: moveTo([c.id], c) },
      canManage && { label: "Loop in", items: loopInTo([c.id]) },
      null,
      canManage && c.status === "open" && { label: "Mark as waiting on customer", onClick: () => act([c.id], { status: "pending" }, "Waiting on the customer.", { status: "open" }) },
      canManage && (c.status === "closed" ? { label: "Reopen", onClick: () => act([c.id], { status: "open" }, "Reopened.") } : { label: "Close", onClick: () => act([c.id], { status: "closed" }, "Closed.", { status: c.status }) }),
      null,
      { label: "Copy link", onClick: () => void navigator.clipboard?.writeText(linkTo(c.id)).then(() => toast("Link copied.")) },
    ];
  }
  const bulk = [
    { label: "Close", onClick: () => act(sel, { status: "closed" }, `Closed ${sel.length}.`, { status: "open" }) },
    ...(myId ? [{ label: "Assign to me", onClick: () => act(sel, { assignee_id: myId }, `Assigned ${sel.length} to you.`) }] : []),
    { label: "Assign to", onClick: (e: ReactMouseEvent) => rowMenu.open(e, assignTo(sel)) },
    { label: "Move to team", onClick: (e: ReactMouseEvent) => rowMenu.open(e, moveTo(sel)) },
    { label: "Loop in", onClick: (e: ReactMouseEvent) => rowMenu.open(e, loopInTo(sel)) },
    { label: "Mark unread", onClick: () => act(sel, { unread: true }, `Marked ${sel.length} as unread.`) },
  ];

  // Shortcuts for the support list; the replies list has its own while a
  // sales view is showing.
  const openConv = list?.find((c) => c.id === open);
  const keysHelp = useInboxKeys(
    !list || (unified && (picked?.kind === "sales" || (!picked && !mailboxes?.length && !forwarding?.length && !list.length)))
      ? []
      : [
          { key: "j", label: "Next conversation", run: () => setOpen(neighbour(list, open, 1)) },
          { key: "k", label: "Previous conversation", run: () => setOpen(neighbour(list, open, -1)) },
          { key: "r", label: "Write a reply", run: () => askToWrite("reply") },
          { key: "n", label: "Write a private note", run: () => askToWrite("note") },
          ...(canManage
            ? [
                { key: "e", label: "Close", run: () => openConv && openConv.status !== "closed" && act([openConv.id], { status: "closed" }, "Closed.", { status: openConv.status }) },
                { key: "u", label: "Mark as unread", run: () => openConv && (act([openConv.id], { unread: true }, "Marked as unread."), setOpen(undefined)) },
                { key: "a", label: "Assign to me", run: () => openConv && myId && act([openConv.id], { assignee_id: myId }, "Assigned to you.", { assignee_id: openConv.assignee?.id ?? null }) },
              ]
            : []),
          { key: "/", label: "Search", run: () => document.querySelector<HTMLInputElement>('input[aria-label="Search conversations"]')?.focus() },
        ],
  );

  const frame = useFillHeight();
  if (mailboxes === null || forwarding === null || (list === null && !error)) return <Loading />;

  const setUp = mailboxes.length > 0 || forwarding.length > 0;
  const needsSetUp = !setUp && !list?.length;
  const now: Picked = picked ?? (needsSetUp ? { kind: "sales", status: "open" } : { kind: "support", folder: "open" });
  const sales = unified && now.kind === "sales" ? now : null;
  const viewSelect = (
    <select
      aria-label="View"
      className={cn(field, "flex-1")}
      value={pickedKey(now)}
      onChange={(e) => {
        const v = e.target.value;
        const f = folders.find((x) => `support:${x.id}` === v);
        const sv = salesViews.find((x) => `sales:${x.id}` === v);
        const rv = replyViews.find((x) => `view:${x.id}` === v);
        if (f) setFolder(f.id);
        else if (sv) pick({ kind: "sales", status: sv.id });
        else if (rv) pick({ kind: "sales", status: "open", view: rv });
      }}
    >
      {unified ? (
        <>
          <optgroup label="Support">
            {folders.map((f) => <option key={f.id} value={`support:${f.id}`}>{f.label}{counts && f.count(counts) ? ` (${f.count(counts)})` : ""}</option>)}
          </optgroup>
          <optgroup label="Sales">
            {salesViews.map((v) => <option key={v.id} value={`sales:${v.id}`}>{v.label}</option>)}
            {replyViews.map((v) => <option key={v.id} value={`view:${v.id}`}>{v.name}</option>)}
          </optgroup>
        </>
      ) : (
        folders.map((f) => <option key={f.id} value={`support:${f.id}`}>{f.label}{counts && f.count(counts) ? ` (${f.count(counts)})` : ""}</option>)
      )}
    </select>
  );
  if (settings || (needsSetUp && !unified)) {
    return <Settings ws={ws} canSetUp={canSetUp} canManage={canManage} mailboxes={mailboxes} first={!setUp} onBack={setUp || unified ? () => setSettings(false) : undefined} onChanged={refresh} go={go} />;
  }

  return (
    <div ref={frame.ref} style={{ height: frame.height }} className="-mx-4 -mb-4 flex min-h-[520px] border-t md:-mx-6 md:-mb-6">
      {/* Folders */}
      <nav aria-label="Support folders" className={cn("hidden w-[200px] shrink-0 flex-col border-r bg-muted/20 px-2 py-3 text-[13px] lg:flex")}>
        <div className="mb-3 flex items-center justify-between px-2">
          <h1 className="text-[15px] font-semibold">{unified ? "Inbox" : "Support"}</h1>
          <button type="button" aria-label="Support settings" title="Support settings" onClick={() => setSettings(true)} className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground">
            <Icon name="settings" className="size-3.5" />
          </button>
        </div>
        <PresenceSwitch myId={myId} canManage={canManage} />
        {myId && <AlertsSwitch />}
        <div className="space-y-0.5">
          {unified && <p className="px-2 pb-1 font-mono text-[10px] tracking-wider text-muted-foreground uppercase">Support</p>}
          {folders.map((f) => (
            <button
              key={f.id}
              type="button"
              title={f.hint}
              aria-current={!sales && folder === f.id ? "page" : undefined}
              onClick={() => setFolder(f.id)}
              className={cn("flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left", !sales && folder === f.id ? "bg-accent font-medium text-foreground" : "text-muted-foreground hover:bg-accent/60 hover:text-foreground")}
            >
              <span className="flex-1">{f.label}</span>
              {f.id === "watching" && <NewCount n={loopIns.support} />}
              {counts && f.count(counts) ? <span className={cn("text-[11px] tabular-nums", f.id === "overdue" && "rounded-full bg-red-500/10 px-1.5 font-medium text-red-700 dark:text-red-400")}>{f.count(counts)}</span> : null}
            </button>
          ))}
        </div>
        {unified && (
          <div className="mt-4 space-y-0.5">
            <p className="px-2 pb-1 font-mono text-[10px] tracking-wider text-muted-foreground uppercase">Sales</p>
            {salesViews.map((v) => {
              const on = !!sales && !sales.view && sales.status === v.id;
              return (
                <button key={v.id} type="button" title={v.hint} aria-current={on ? "page" : undefined} onClick={() => pick({ kind: "sales", status: v.id })} className={cn("flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left", on ? "bg-accent font-medium text-foreground" : "text-muted-foreground hover:bg-accent/60 hover:text-foreground")}>
                  <span className="flex-1">{v.label}</span>
                  {v.id === "watching" && <NewCount n={loopIns.replies} />}
                </button>
              );
            })}
            {salesTeamId && (
              <button type="button" title="Conversations support handed to the sales team" aria-current={!sales && folder === "sales" ? "page" : undefined} onClick={() => setFolder("sales")} className={cn("flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left", !sales && folder === "sales" ? "bg-accent font-medium text-foreground" : "text-muted-foreground hover:bg-accent/60 hover:text-foreground")}>
                <span className="flex-1">From support</span>
              </button>
            )}
            {salesFloor && <p className="px-2 pt-3 pb-1 font-mono text-[10px] tracking-wider text-muted-foreground uppercase">Sales conversations</p>}
            {salesFloor &&
              ([["new", "New conversations", "Replies waiting for someone in sales to take them", salesFloor.fresh], ["mine", "My conversations", "The sales conversations you own", salesFloor.unread]] as const).map(([id, label, hint, dot]) => (
                <button
                  key={id}
                  type="button"
                  title={hint}
                  onClick={() => {
                    track("ui.inbox_sales_conversations_opened", { queue: id });
                    go("crm", `sales:${id}`);
                  }}
                  className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-muted-foreground hover:bg-accent/60 hover:text-foreground"
                >
                  <span className="flex-1">{label}</span>
                  {dot && <span className="size-1.5 rounded-full bg-emerald-500" aria-label="New" />}
                </button>
              ))}
            {replyViews.length > 0 && <p className="px-2 pt-3 pb-1 font-mono text-[10px] tracking-wider text-muted-foreground uppercase">Saved views</p>}
            {replyViews.map((v) => {
              const on = sales?.view?.id === v.id;
              return (
                <button key={v.id} type="button" title={v.shared ? "Shared with the workspace" : "Only you see this view"} aria-current={on ? "page" : undefined} onClick={() => pick({ kind: "sales", status: "open", view: v })} className={cn("flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left", on ? "bg-accent font-medium text-foreground" : "text-muted-foreground hover:bg-accent/60 hover:text-foreground")}>
                  <span className="flex-1 truncate">{v.name}</span>
                </button>
              );
            })}
          </div>
        )}
        <div className="mt-auto px-0 pb-2 text-xs"><KeysButton /></div>
        <div className="space-y-2 border-t px-2 pt-3 text-xs text-muted-foreground">
          <p className="font-mono text-[11px] uppercase">Last 30 days</p>
          <p className="flex justify-between"><span>Conversations</span><span className="tabular-nums text-foreground">{stats?.conversations ?? "–"}</span></p>
          <p className="flex justify-between" title="Median time from the customer's first email to your first reply"><span>First reply</span><span className="tabular-nums text-foreground">{duration(stats?.median_first_reply_seconds)}</span></p>
          <p className="flex justify-between" title="Median time from the first email to closing"><span>Time to close</span><span className="tabular-nums text-foreground">{duration(stats?.median_resolution_seconds)}</span></p>
          {!!stats?.sla_missed && (
            <p className="flex justify-between" title="Conversations that missed a response time at least once"><span>Missed SLA</span><span className="tabular-nums text-red-700 dark:text-red-400">{stats.sla_missed}</span></p>
          )}
          {review > 0 && (
            <button type="button" onClick={() => go("knowledge")} className="mt-1 flex w-full items-center gap-1.5 rounded-md bg-violet-500/10 px-2 py-1.5 text-left text-violet-800 hover:bg-violet-500/15 dark:text-violet-300">
              <Icon name="sparkles" className="size-3.5 shrink-0" />
              <span className="flex-1">{review} to review in Knowledge</span>
            </button>
          )}
        </div>
      </nav>

      {sales && (
        <SalesReplies
          key={pickedKey(sales)}
          ws={ws}
          canManage={canManage}
          myId={myId}
          open={openReply}
          setOpen={setItem}
          onRead={onRead ?? (() => {})}
          embedded={{
            title: sales.view?.name ?? salesViews.find((v) => v.id === sales.status)?.label ?? "Replies",
            preset: sales.view ? { status: "open", ...(sales.view.filters as Record<string, string>) } : sales.status === "watching" ? { status: "all", following: "me" } : { status: sales.status },
            switcher: <div className="border-b p-3 lg:hidden">{viewSelect}</div>,
            onViews: () => setViewsTick((t) => t + 1),
          }}
        />
      )}
      {!sales && needsSetUp && (
        <div className="flex flex-1 items-center justify-center p-8">
          <Empty title="Support isn't set up yet" action={canSetUp ? <Button onClick={() => setSettings(true)}>Set up support</Button> : undefined}>
            Connect a support mailbox or the chat on your site, and customers' messages land here beside your sales replies.
          </Empty>
        </div>
      )}
      {/* The list */}
      {!sales && !needsSetUp && <section aria-label="Conversations" className={cn("flex w-full shrink-0 flex-col border-r md:w-[300px]", open && "hidden md:flex")}>
        <div className="space-y-2 border-b p-3">
          <div className="flex items-center gap-2 lg:hidden">
            {viewSelect}
            <Button variant="ghost" aria-label="Support settings" onClick={() => setSettings(true)}><Icon name="settings" className="size-3.5" /></Button>
          </div>
          <p className="hidden text-[13px] font-medium lg:block">{folder === "sales" ? "From support" : folders.find((f) => f.id === folder)?.label}</p>
          <input type="search" aria-label="Search conversations" placeholder="Search name, email or words" className={cn(field, "w-full")} value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        {error && <div className="p-3"><ErrorText>{error}</ErrorText></div>}
        {canManage && sel.length > 0 && <BulkBar count={sel.length} onClear={() => setSel([])} actions={bulk} />}
        <ul className="flex-1 overflow-y-auto">
          {list?.length === 0 && (
            <li className="p-6 text-center text-[13px] text-muted-foreground">
              {q ? "Nothing matches." : folder === "mine" ? "Nothing assigned to you. Pick one up from Unassigned." : folder === "open" ? "All caught up: nobody is waiting on you." : folder === "watching" ? "Nothing here yet. When a teammate loops you in on a conversation, it shows up here." : folder === "sales" ? "Nothing handed over yet. When support presses Sales opportunity on a conversation, it shows up here." : folder === "overdue" ? "Nothing overdue. Set response times in Support settings to track them." : "No conversations here."}
            </li>
          )}
          {list?.map((c) => (
            <li key={c.id} className="group relative" onContextMenu={(e) => rowMenu.open(e, rowActions(c))}>
              <span className={cn("absolute right-2 bottom-2 z-10 flex items-center gap-1.5", sel.length === 0 && "opacity-0 group-hover:opacity-100 focus-within:opacity-100")}>
                <RowMore name={who(c)} onOpen={(e) => rowMenu.open(e, rowActions(c))} />
                {canManage && <input type="checkbox" aria-label={`Select ${who(c)}`} checked={sel.includes(c.id)} onChange={() => toggle(c.id)} />}
              </span>
              <button
                type="button"
                onClick={() => setOpen(c.id)}
                aria-current={open === c.id ? "true" : undefined}
                className={cn("flex w-full gap-3 border-b px-3 py-3 text-left hover:bg-accent/50", open === c.id && "bg-accent")}
              >
                <Avatar name={c.contact.name} email={c.contact.email} />
                <span className="min-w-0 flex-1">
                  <span className="flex items-baseline gap-2">
                    <span className={cn("min-w-0 flex-1 truncate text-[13px]", c.unread ? "font-semibold" : "font-medium")}>
                      {c.channel === "chat" && <Icon name="discuss" className={cn("mr-1 inline size-3 align-[-1px]", chatLive(c) ? "text-emerald-600" : "text-muted-foreground")} aria-label={chatLive(c) ? "Chat, on your site now" : "Chat"} />}
                      {who(c)}
                    </span>
                    <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">{listTime(c.last_message_at)}</span>
                  </span>
                  <span className={cn("block truncate text-[13px]", c.unread ? "text-foreground" : "text-muted-foreground")}>{c.subject || "(no subject)"}</span>
                  <span className="line-clamp-2 text-xs text-muted-foreground">{c.last_from === "team" && <span className="text-foreground/70">You: </span>}{c.preview}</span>
                  <span className="mt-1 flex flex-wrap items-center gap-1.5">
                    {c.unread && <span className="size-2 rounded-full bg-blue-600" aria-label="Unread" />}
                    <SlaChip c={c} />
                    {(folder === "all" || folder === "mine") && <Badge tone={statusTone[c.status]}>{statusLabel[c.status]}</Badge>}
                    {c.assignee && folder !== "mine" && <span className="text-[11px] text-muted-foreground">→ {c.assignee.id === myId ? "You" : c.assignee.name}</span>}
                    {c.tags.slice(0, 3).map((t) => <span key={t} className="rounded border bg-muted px-1 font-mono text-[10px] text-muted-foreground">{t}</span>)}
                  </span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      </section>}

      {rowMenu.menu}
      {keysHelp}
      {/* The conversation */}
      {!sales && !needsSetUp && <div className={cn("min-w-0 flex-1", !open && "hidden md:flex")}>
        {open ? (
          <Conversation key={open} ws={ws} id={open} canManage={canManage} myId={myId} emailsChat={mailboxes.length > 0} onBack={() => setOpen(undefined)} onChanged={refresh} go={go} />
        ) : (
          <div className="flex flex-1 items-center justify-center p-8 text-center text-[13px] text-muted-foreground">
            <div className="max-w-xs space-y-2">
              <Icon name="support" className="mx-auto size-8 opacity-40" />
              <p>Pick a conversation to read and answer it.</p>
              <p className="text-xs">Mail to {[...mailboxes.map((m) => m.email), ...forwarding].join(", ")} lands here.</p>
            </div>
          </div>
        )}
      </div>}
    </div>
  );
}

function Conversation({ ws, id, canManage, myId, emailsChat, onBack, onChanged, go }: { ws: string; id: string; canManage: boolean; myId: string; emailsChat: boolean; onBack: () => void; onChanged: () => void; go: (tab: Tab) => void }) {
  const viewing = useViewers("support", id, !!myId);
  const [conv, setConv] = useState<SupportConversation | null>(null);
  // What the brand's own AI found in this conversation, if it reads them.
  const spotted = useSpotted(conv);
  const [messages, setMessages] = useState<SupportMessage[]>([]);
  const [others, setOthers] = useState<SupportConversation[]>([]);
  const [members, setMembers] = useState<Member[]>([]);
  const [teams, setTeams] = useState<Team[]>([]);
  const [handing, setHanding] = useState(false);
  const [error, setError] = useState("");
  // The breadcrumb names the customer, not the conversation's id (which
  // the one Inbox writes as support-<id>).
  const crumb = conv ? conv.contact.name || conv.contact.email : "";
  useEffect(() => {
    if (!crumb) return;
    for (const item of [id, `support-${id}`]) window.dispatchEvent(new CustomEvent("outflare:item-name", { detail: { id: item, name: crumb } }));
  }, [id, crumb]);
  const [mode, setMode] = useState<"reply" | "note">("reply");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  // Teammates looped in: it shows under their Watching view.
  const [followers, setFollowers] = useState<SupportPerson[]>([]);
  async function loopIn(m: Member) {
    if (!m.user_id) return;
    const on = followers.some((f) => f.id === m.user_id);
    const params = { path: { ...path, user: m.user_id } };
    const { data, error } = on
      ? await api.DELETE("/v1/brands/{brand}/support/conversations/{id}/followers/{user}", { params })
      : await api.PUT("/v1/brands/{brand}/support/conversations/{id}/followers/{user}", { params });
    if (error || !data) return toast(errorMessage(error), "error");
    setFollowers(data.followers);
    toast(on ? `${m.name || m.email} is no longer looped in.` : `${m.name || m.email} is looped in. It shows under their Watching view.`);
  }
  const [suggesting, setSuggesting] = useState(false);
  // The assistant's latest run here, the draft the reply came from, and
  // why it couldn't run (no AI connected).
  const [run, setRun] = useState<SupportAssistantRun | null>(null);
  const [runId, setRunId] = useState("");
  const [aiProblem, setAiProblem] = useState<{ code: string; message: string } | null>(null);
  const [articles, setArticles] = useState(false);
  const end = useRef<HTMLDivElement>(null);
  const box = useRef<HTMLTextAreaElement>(null);
  const path = { brand: pickedBrandId(), id };

  // "r" and "n" in the list put the cursor in the reply or note box.
  useEffect(() => {
    const write = (e: Event) => {
      setMode((e as CustomEvent<"reply" | "note">).detail);
      box.current?.focus();
    };
    window.addEventListener(writeEvent, write);
    return () => window.removeEventListener(writeEvent, write);
  }, []);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/support/conversations/{id}", { params: { path } });
    if (error) return setError(errorMessage(error));
    setConv(data.conversation);
    // A conversation copied from Intercom is answered there: only notes here.
    if (data.conversation.source !== "outflare") setMode("note");
    setMessages(data.messages);
    setOthers(data.other_conversations);
    setFollowers(data.followers);
    const a = await api.GET("/v1/brands/{brand}/support/conversations/{id}/assistant", { params: { path } });
    if (a.data) setRun(a.data.run ?? null);
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
    const { data, error } = await api.PATCH("/v1/brands/{brand}/support/conversations/{id}", { params: { path }, body: change as never });
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
        ? await api.POST("/v1/brands/{brand}/support/conversations/{id}/notes", { params: { path }, body: { body } })
        : await api.POST("/v1/brands/{brand}/support/conversations/{id}/replies", {
            params: { path },
            body: { body, ...(after ? { status: after } : {}), ...(runId ? { assistant_run_id: runId } : {}) },
          });
    setBusy(false);
    if (res.error) return toast(errorMessage(res.error), "error");
    setBody("");
    setRunId("");
    setAiProblem(null);
    await load();
    onChanged();
    if (after) toast("Sent and closed");
  }

  // insertLink puts an article's title and page where the cursor is, on
  // a line of its own, so the customer can read the rest there.
  function insertLink(title: string, url: string, from: "suggested" | "search") {
    const el = box.current;
    const at = el ? el.selectionEnd : body.length;
    const before = body.slice(0, at).replace(/\s+$/, "");
    const after = body.slice(at).replace(/^\s+/, "");
    const line = `${title}: ${url}`;
    const next = (before ? before + "\n\n" : "") + line + (after ? "\n\n" + after : "");
    setMode("reply");
    setBody(next);
    track("ui.support_article_linked", { from });
    requestAnimationFrame(() => {
      const pos = (before ? before.length + 2 : 0) + line.length;
      box.current?.focus();
      box.current?.setSelectionRange(pos, pos);
    });
  }

  async function suggest() {
    setSuggesting(true);
    setAiProblem(null);
    const { data, error } = await api.POST("/v1/brands/{brand}/support/conversations/{id}/assistant", { params: { path } });
    setSuggesting(false);
    if (error) {
      const e = (error as { error?: { code?: string; message?: string } }).error;
      if (e?.code && /^(connector_|ai_)/.test(e.code)) return setAiProblem({ code: e.code, message: e.message ?? "" });
      return toast(errorMessage(error), "error");
    }
    setRun(data as SupportAssistantRun);
  }

  function useDraft(r: SupportAssistantRun) {
    setMode("reply");
    setBody(r.reply);
    setRunId(r.id);
    setRun(null);
    requestAnimationFrame(() => box.current?.focus());
  }

  if (error) return <div className="p-6"><ErrorText>{error}</ErrorText></div>;
  if (!conv) return <div className="flex-1"><Loading /></div>;
  const closed = conv.status === "closed";
  const forwarded = conv.via === "forwarding";
  const chat = conv.channel === "chat";
  const imported = conv.source !== "outflare";
  const canReply = !imported && (chat || !!conv.mailbox_id || (forwarded && !!conv.reply_from));

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
              <SlaChip c={conv} long />
              <span className="truncate">· {conv.contact.name ? `${conv.contact.name} · ` : ""}{conv.contact.email || (chat ? "Website visitor" : "")}</span>
              {chat && <span className={cn("shrink-0", chatLive(conv) ? "text-emerald-700 dark:text-emerald-400" : "")}>· {chatLive(conv) ? "On your site now" : "Chat"}</span>}
            </p>
          </div>
          {canManage && !closed && !imported && <HandOff conv={conv} teams={teams} spotted={spotted} open={handing} onOpen={setHanding} onDone={() => { load(); onChanged(); }} />}
          {canManage && !imported && (
            closed ? (
              <Button variant="ghost" onClick={() => patch({ status: "open" }, "Reopened")}>Reopen</Button>
            ) : (
              <Button onClick={() => patch({ status: "closed" }, "Closed")}><Icon name="check" className="size-3.5" /> Close</Button>
            )
          )}
          {canManage && !imported && (
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

        {imported && (
          <p className="flex items-center gap-1.5 border-b bg-muted/40 px-4 py-2 text-xs text-muted-foreground">
            <Icon name="connectors" className="size-3.5 shrink-0" />
            Copied from Intercom, where it is answered, assigned and closed; changes there show here within minutes. Notes stay with your team here.
          </p>
        )}
        <div className="flex-1 space-y-4 overflow-y-auto bg-muted/20 px-4 py-5">
          {messages.map((m) => <MessageView key={m.id} m={m} contact={conv.contact} />)}
          <div ref={end} />
        </div>

        {canManage && spotted && !closed && conv.team?.id !== salesTeam(teams)?.id && <SpottedBanner spotted={spotted} onHandOff={() => setHanding(true)} />}
        <ViewingNow viewers={viewing.viewers} />
        {canManage && (
          <div className="border-t bg-background p-3">
            {aiProblem && (
              <p className="mb-2 flex items-start gap-1.5 rounded-md border border-violet-500/30 bg-violet-500/5 px-3 py-2 text-xs">
                <Icon name="ai" className="mt-0.5 size-3.5 shrink-0 text-violet-600" />
                <span className="flex-1">
                  {aiProblem.message}{" "}
                  {aiProblem.code !== "connector_auth" && <button type="button" className="underline" onClick={() => go("ai")}>Connect an AI model</button>}
                </span>
                <button type="button" aria-label="Dismiss" onClick={() => setAiProblem(null)} className="text-muted-foreground hover:text-foreground"><Icon name="x" className="size-3.5" /></button>
              </p>
            )}
            {run && !closed && <AssistantDraft ws={ws} id={id} run={run} onUse={useDraft} onDiscarded={() => setRun(null)}
              onChanged={(r) => (r ? setRun(r) : load().then(onChanged))} />}
            {articles && mode === "reply" && (
              <ArticlePicker ws={ws} id={id} onPick={(a, from) => { if (a.url) insertLink(a.title, a.url, from); setArticles(false); }} onClose={() => setArticles(false)} go={go} />
            )}
            <div className={cn("rounded-lg border focus-within:ring-[3px] focus-within:ring-ring/40", mode === "note" && "border-amber-500/40 bg-amber-50/60 dark:bg-amber-500/5")}>
              <div className="flex items-center gap-1 border-b px-2 py-1.5" role="tablist" aria-label="Write">
                {(imported ? (["note"] as const) : (["reply", "note"] as const)).map((k) => (
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
                  {mode === "note"
                    ? "Only your team sees notes"
                    : chat
                      ? emailsChat && chatLeft(conv) && conv.contact.email
                        ? "Also sent by email: they left your site"
                        : `In their chat${chatLive(conv) ? ": they're on your site now" : conv.contact.email ? "; they see it next time they open it" : ""}`
                      : canReply
                      ? `To ${conv.contact.email}, from ${forwarded ? conv.reply_from : conv.mailbox_email}`
                      : forwarded
                        ? "Outflare doesn't know which of your addresses they wrote to"
                        : "This conversation's support mailbox was removed"}
                </span>
                <span className="flex-1" />
                {mode === "reply" && (
                  <button
                    type="button"
                    onClick={() => setArticles((v) => !v)}
                    aria-expanded={articles}
                    className={cn("inline-flex shrink-0 items-center gap-1 rounded px-2 py-0.5 text-xs font-medium text-muted-foreground hover:bg-accent hover:text-foreground", articles && "bg-accent text-foreground")}
                  >
                    <Icon name="article" className="size-3.5" />Articles
                  </button>
                )}
                {mode === "reply" && (
                  <SavedReplies
                    canManage={canManage}
                    firstName={conv.contact.name.split(" ")[0]}
                    myName={members.find((m) => m.user_id === myId)?.name ?? ""}
                    draft={body}
                    onPick={(text) => { setBody((cur) => (cur.trim() ? `${cur.trimEnd()}\n\n${text}` : text)); box.current?.focus(); }}
                  />
                )}
                {mode === "reply" && (
                  <button
                    type="button"
                    onClick={suggest}
                    disabled={suggesting}
                    className="inline-flex shrink-0 items-center gap-1 rounded px-2 py-0.5 text-xs font-medium text-violet-700 hover:bg-violet-500/10 disabled:opacity-60 dark:text-violet-400"
                  >
                    <Icon name="ai" className="size-3.5" />{suggesting ? "Drafting…" : "Draft with AI"}
                  </button>
                )}
              </div>
              <textarea
                ref={box}
                aria-label={mode === "note" ? "Note" : "Reply"}
                rows={4}
                value={body}
                onChange={(e) => { setBody(e.target.value); viewing.typing(); }}
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

      <Details conv={conv} others={others} members={members} teams={teams} followers={followers} onLoopIn={loopIn} canManage={canManage} myId={myId} onPatch={patch} go={go} />
    </div>
  );
}

// finOutcome says how a conversation ended for Fin, from Intercom's
// resolution_state.
function finOutcome(resolution: string) {
  return (
    {
      assumed_resolution: "Resolved (assumed)",
      confirmed_resolution: "Resolved (confirmed)",
      escalated: "Handed to the team",
      routed_to_team: "Handed to the team",
      negative_feedback: "Unhelpful, said the customer",
      procedure_handoff: "Handed to a procedure",
      abandoned: "Customer left",
    } as Record<string, string>
  )[resolution] ?? (resolution ? resolution.replace(/_/g, " ") : "Answered");
}

function MessageView({ m, contact }: { m: SupportMessage; contact: SupportConversation["contact"] }) {
  if (m.kind === "note") {
    return (
      <div className="mx-auto max-w-2xl rounded-lg border border-amber-500/30 bg-amber-50 px-3 py-2 text-[13px] dark:bg-amber-500/10">
        <p className="mb-1 text-[11px] text-amber-800 dark:text-amber-300">Note by {m.external_author ?? m.author?.name ?? "someone"} · <span title={stamp(m.created_at)}>{ago(m.created_at)}</span></p>
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
          {mine ? (m.external_author ?? m.author?.name ?? (m.from_name || "Your team")) : m.from_name || m.from_email || contact.name || contact.email || "Customer"}
          {m.author_type === "ai" && <span className="ml-1 rounded bg-violet-500/10 px-1 py-px text-[10px] font-medium text-violet-700 dark:text-violet-400">AI agent</span>}
          {" · "}<span title={stamp(m.created_at)}>{ago(m.created_at)}</span>
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

// ArticlePicker finds help centre articles to link to: the best matches
// for what the customer wrote, or a search.
function ArticlePicker({ ws, id, onPick, onClose, go }: {
  ws: string;
  id: string;
  onPick: (a: SupportArticle, from: "suggested" | "search") => void;
  onClose: () => void;
  go: (t: Tab) => void;
}) {
  const [q, setQ] = useState("");
  const [list, setList] = useState<SupportArticle[] | null>(null);
  const [centre, setCentre] = useState<{ url: string } | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    const t = setTimeout(async () => {
      const { data, error } = await api.GET("/v1/brands/{brand}/support/conversations/{id}/articles", {
        params: { path: { brand: pickedBrandId(), id }, query: q.trim() ? { q: q.trim() } : {} },
      });
      if (!live) return;
      if (error) return setError(errorMessage(error));
      setError("");
      setList(data.data);
      setCentre(data.help_centre);
    }, q ? 250 : 0);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [ws, id, q]);
  return (
    <div className="mb-2 rounded-md border bg-background text-xs shadow-xs">
      <div className="flex items-center gap-2 border-b px-2 py-1.5">
        <Icon name="search" className="size-3.5 text-muted-foreground" />
        <input
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => e.key === "Escape" && onClose()}
          placeholder="Search your help centre…"
          aria-label="Search articles"
          className="min-w-0 flex-1 bg-transparent py-0.5 outline-none"
        />
        <button type="button" aria-label="Close" onClick={onClose} className="text-muted-foreground hover:text-foreground"><Icon name="x" className="size-3.5" /></button>
      </div>
      {error && <p className="px-3 py-2 text-destructive">{error}</p>}
      {!centre && list && (
        <p className="border-b bg-muted/40 px-3 py-1.5 text-muted-foreground">
          No help centre is live, so there's nothing to link to yet.{" "}
          <button type="button" className="underline" onClick={() => go("help-centre")}>Set one up</button>
        </p>
      )}
      {list === null ? (
        <p className="px-3 py-2 text-muted-foreground">Finding articles…</p>
      ) : list.length === 0 ? (
        <p className="px-3 py-2 text-muted-foreground">{q ? "No public article matches." : "No public article matches what they wrote. Search instead."}</p>
      ) : (
        <ul className="max-h-56 overflow-y-auto py-1">
          {!q && <li className="px-3 pt-1 pb-0.5 text-[11px] text-muted-foreground">Suggested from their message</li>}
          {list.map((a) => (
            <li key={a.id}>
              <button
                type="button"
                disabled={!a.url}
                onClick={() => onPick(a, q ? "search" : "suggested")}
                className="block w-full px-3 py-1.5 text-left hover:bg-accent disabled:cursor-default disabled:hover:bg-transparent"
              >
                <span className="block truncate font-medium">{a.title}</span>
                <span className="block truncate text-muted-foreground">{a.excerpt}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Details({
  conv,
  others,
  members,
  teams,
  followers,
  onLoopIn,
  canManage,
  myId,
  onPatch,
  go,
}: {
  conv: SupportConversation;
  others: SupportConversation[];
  members: Member[];
  teams: Team[];
  followers: SupportPerson[];
  onLoopIn: (m: Member) => void;
  canManage: boolean;
  myId: string;
  onPatch: (change: Record<string, unknown>) => void;
  go: (tab: Tab, item?: string) => void;
}) {
  const [tag, setTag] = useState("");
  const imported = conv.source !== "outflare";
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
            <p className="truncate font-medium">{who(conv)}</p>
            <p className="truncate text-xs text-muted-foreground">
              {conv.contact.email || "No email given"}
              {conv.identity_verified && <span className="ml-1 text-emerald-700 dark:text-emerald-400" title="Your site vouched for this email">· verified</span>}
            </p>
          </div>
        </div>
        {conv.record_id && (
          <button type="button" className="text-xs text-muted-foreground underline hover:text-foreground" onClick={() => go("crm")}>Open in People</button>
        )}
        {conv.record_id && canManage && <FlagButton brandId={pickedBrandId()} recordId={conv.record_id} conversation={{ channel: "support", ref: conv.id }} label="Flag for attention" />}
      </section>

      {imported ? (
        <section className="space-y-1.5 text-xs">
          <p className="font-mono text-[11px] text-muted-foreground uppercase">In Intercom</p>
          <p className="flex justify-between gap-2"><span className="text-muted-foreground">Assignee</span><span className="truncate">{conv.external_assignee ?? "Unassigned"}</span></p>
          {conv.external_team && <p className="flex justify-between gap-2"><span className="text-muted-foreground">Team</span><span className="truncate">{conv.external_team}</span></p>}
          {conv.priority && <p className="flex justify-between gap-2"><span className="text-muted-foreground">Priority</span><span className="capitalize">{conv.priority}</span></p>}
          {conv.reopens > 0 && <p className="flex justify-between gap-2"><span className="text-muted-foreground">Reopened</span><span>{conv.reopens === 1 ? "once" : `${conv.reopens} times`}</span></p>}
          {conv.tags.length > 0 && (
            <div className="flex flex-wrap gap-1 pt-1">
              {conv.tags.map((t) => <span key={t} className="rounded border bg-muted px-1.5 font-mono text-[11px]">{t}</span>)}
            </div>
          )}
        </section>
      ) : (
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
          <span className="block text-xs text-muted-foreground">Looped in</span>
          <p className="text-[13px]">{followers.map((f) => f.name).join(", ") || "Nobody yet"}</p>
          {canManage && (
            <Menu
              label="Loop in"
              aria="Loop a teammate in"
              items={members.map((m) => ({ label: `${followers.some((f) => f.id === m.user_id) ? "✓ " : ""}${m.name || m.email}`, onClick: () => onLoopIn(m) }))}
            />
          )}
        </div>
        {conv.topic && (
          <p className="text-xs text-muted-foreground" title="The topic the AI assistant gave this conversation (Support settings, Hand-off topics)">
            Topic <span className="ml-1 rounded bg-violet-500/10 px-1.5 py-0.5 text-[11px] font-medium text-violet-700 dark:text-violet-400">{conv.topic.name}</span>
          </p>
        )}
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
      )}

      {(conv.rating || conv.ai) && (
        <section className="space-y-1.5 text-xs">
          {conv.rating && (
            <>
              <p className="font-mono text-[11px] text-muted-foreground uppercase">Rating</p>
              <p className="flex justify-between gap-2"><span className="text-muted-foreground">Customer</span><span aria-label={`${conv.rating.score} out of 5`}>{"★".repeat(conv.rating.score)}<span className="text-muted-foreground/40">{"★".repeat(5 - conv.rating.score)}</span></span></p>
              {conv.rating.remark && <p className="text-muted-foreground italic">“{conv.rating.remark}”</p>}
            </>
          )}
          {conv.ai && (
            <>
              <p className="pt-1 font-mono text-[11px] text-muted-foreground uppercase">Fin (Intercom's AI agent)</p>
              <p className="flex justify-between gap-2"><span className="text-muted-foreground">Outcome</span><span>{finOutcome(conv.ai.resolution)}</span></p>
              {conv.ai.source_title && <p className="flex justify-between gap-2"><span className="shrink-0 text-muted-foreground">Ran by</span><span className="truncate" title={conv.ai.source_title}>{conv.ai.source_title}</span></p>}
              {conv.ai.rating && <p className="flex justify-between gap-2"><span className="text-muted-foreground">Rated</span><span>{conv.ai.rating} of 5</span></p>}
              {conv.ai.rating_remark && <p className="text-muted-foreground italic">“{conv.ai.rating_remark}”</p>}
              {conv.ai.sources.length > 0 && (
                <div className="space-y-0.5">
                  <p className="text-muted-foreground">Answered from</p>
                  <ul className="space-y-0.5">
                    {conv.ai.sources.map((src, i) => (
                      <li key={i} className="truncate">
                        {src.url ? <a href={src.url} target="_blank" rel="noreferrer" className="underline hover:text-foreground">{src.title || src.url}</a> : src.title || src.type}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </>
          )}
        </section>
      )}

      <section className="space-y-1.5 text-xs">
        <p className="font-mono text-[11px] text-muted-foreground uppercase">Times</p>
        <p className="flex justify-between"><span className="text-muted-foreground">Started</span><span title={stamp(conv.first_message_at)}>{ago(conv.first_message_at)}</span></p>
        <p className="flex justify-between">
          <span className="text-muted-foreground">First reply</span>
          <span>{conv.first_reply_at ? duration((Date.parse(conv.first_reply_at) - Date.parse(conv.first_message_at)) / 1000) + " after" : "Not yet"}</span>
        </p>
        {conv.closed_at && <p className="flex justify-between"><span className="text-muted-foreground">Closed</span><span title={stamp(conv.closed_at)}>{ago(conv.closed_at)}</span></p>}
        {conv.status !== "closed" && conv.sla.reply_due_at && (
          <p className="flex justify-between"><span className="text-muted-foreground">Reply due</span><span className={cn(conv.sla.reply_breached_at && "text-red-700 dark:text-red-400")} title={stamp(conv.sla.reply_due_at)}>{stamp(conv.sla.reply_due_at)}</span></p>
        )}
        {conv.status !== "closed" && conv.sla.resolve_due_at && (
          <p className="flex justify-between"><span className="text-muted-foreground">Resolve by</span><span className={cn(conv.sla.resolve_breached_at && "text-red-700 dark:text-red-400")} title={stamp(conv.sla.resolve_due_at)}>{stamp(conv.sla.resolve_due_at)}</span></p>
        )}
        {conv.via === "forwarding" ? (
          <>
            <p className="flex justify-between gap-2"><span className="shrink-0 text-muted-foreground">Wrote to</span><span className="truncate" title={conv.reply_from ?? undefined}>{conv.reply_from ?? "unknown"}</span></p>
            <p className="flex justify-between gap-2"><span className="shrink-0 text-muted-foreground">Forwarded to</span><span className="truncate" title={conv.inbound_address ?? undefined}>{conv.inbound_address ?? "a removed address"}</span></p>
            <p className="text-[11px] text-muted-foreground">Replies go out through your email provider.</p>
          </>
        ) : (
          <p className="flex justify-between gap-2"><span className="shrink-0 text-muted-foreground">Via</span><span className="truncate">{imported ? `Intercom${conv.external_channel ? ` (${conv.external_channel.replace(/_/g, " ")})` : ""}` : conv.channel === "chat" ? "Chat on your website" : (conv.mailbox_email ?? "removed mailbox")}</span></p>
        )}
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
  canManage,
  mailboxes,
  first,
  onBack,
  onChanged,
  go,
}: {
  ws: string;
  canSetUp: boolean;
  canManage: boolean;
  mailboxes: SupportMailbox[];
  first: boolean;
  onBack?: () => void;
  onChanged: () => void;
  go: (tab: Tab, item?: string) => void;
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
            ? "Bring in the mailbox customers write to, such as support@ or help@: connect it, or forward it to your support address. Every email to it becomes a conversation your team can assign, answer in the same thread and close, with your knowledge one click away."
            : "Bring customers' mail in by connecting your support mailbox or forwarding it to your support address. A connected support mailbox no longer goes to the sales inbox, and sequences never send from it."}
        </p>
      </div>

      {first && (
        <ol className="grid gap-3 sm:grid-cols-3">
          {[
            ["Connect or forward", "Connect your support mailbox, or forward it to your support address below."],
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
          <p className="text-[13px] font-medium">{mailboxes.length ? "Add another mailbox" : "Connect your support mailbox"}</p>
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
            A mailbox that sends cold email can't be used: take it off its sequences first, so customers' mail never mixes with replies to outreach.
          </p>
        </div>
      ) : (
        <p className="text-[13px] text-muted-foreground">Ask someone who manages mailboxes to choose the support mailbox.</p>
      )}
      <SupportForwarding ws={ws} canSetUp={canSetUp} go={go} onChanged={onChanged} />
      <SupportChat ws={ws} canManage={canManage} />
      <SupportImports ws={ws} canManage={canManage} go={go} />
      <SupportAssistantSettings ws={ws} canManage={canManage} go={go} />
      <SupportHandoffTopics ws={ws} canManage={canManage} />
      {!first && <SupportSLA ws={ws} canManage={canManage} onChanged={onChanged} />}
    </div>
  );
}
