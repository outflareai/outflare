import { useCallback, useEffect, useState } from "react";
import type { TelegramChat, TelegramMessage } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Loading, Menu, toast } from "../ui";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { PickBrand } from "@/components/pick-brand";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { ago, stamp } from "../format";
import { cn } from "@/lib/utils";

// Telegram chats synced from the company's Telegram accounts, and the queue
// of chats to link to a customer record. Outflare matches what it can by
// itself (a Telegram id, phone, username, email or company domain), the
// workspace's AI suggests the rest, and the account's holder confirms each
// one here. Confirming gives the record the chat's Telegram id, so the next
// chat with them links by itself.

type View = "queue" | "waiting" | "all";
type Ref = { id: string; type: string; name: string };

const reasonLabel: Record<string, string> = {
  telegram: "Same Telegram account",
  phone: "Same phone number",
  username: "Same username",
  email: "Email address in the chat",
  domain: "Company domain in the chat",
  ai: "AI suggestion",
};

const kindLabel = { unsorted: "Unsorted", customer: "Customer", internal: "Colleague", personal: "Personal" } as const;

export function TelegramChats({ ws, canManage, item, setItem }: { ws: string; canManage: boolean; item?: string; setItem: (id?: string) => void }) {
  const { brand, active } = useBrand(ws);
  if (!brand) return <PickBrand what="Telegram chats" brands={active.length} />;
  return <Chats key={brand.id} canManage={canManage} item={item} setItem={setItem} />;
}

function Chats({ canManage, item, setItem }: { canManage: boolean; item?: string; setItem: (id?: string) => void }) {
  const [view, setView] = useState<View>("queue");
  const [mine, setMine] = useState(true);
  const [rows, setRows] = useState<TelegramChat[] | null>(null);
  const [err, setErr] = useState("");
  const [open, setOpen] = useState<TelegramChat | null>(null);
  const [picking, setPicking] = useState<TelegramChat | null>(null);

  const load = useCallback(async () => {
    const query: Record<string, string | number> = { limit: 200 };
    if (view === "queue") query.match_status = "suggested,no_match";
    if (view === "waiting") query.waiting = "true";
    if (view !== "all" && mine) query.mine = "true";
    const r = await api.GET("/v1/brands/{brand}/telegram-chats", { params: { path: { brand: pickedBrandId() }, query: query as never } });
    if (r.error) setErr(errorMessage(r.error));
    else {
      setErr("");
      setRows(r.data?.data ?? []);
    }
  }, [view, mine]);

  useEffect(() => {
    setRows(null);
    void load();
  }, [load]);

  // A chat opened from a link, such as an alert.
  useEffect(() => {
    if (!item || open?.id === item) return;
    void api.GET("/v1/brands/{brand}/telegram-chats/{id}", { params: { path: { brand: pickedBrandId(), id: item } } }).then((r) => {
      if (r.data) setOpen(r.data);
      else if (r.error) toast(errorMessage(r.error), "error");
    });
  }, [item, open?.id]);

  const replace = (c: TelegramChat) => setRows((rs) => (rs ?? []).map((x) => (x.id === c.id ? c : x)).filter((x) => view === "all" || x.match_status !== "confirmed"));

  const match = async (c: TelegramChat, record: string | null) => {
    const r = await api.POST("/v1/brands/{brand}/telegram-chats/{id}/match", { params: { path: { brand: pickedBrandId(), id: c.id } }, body: { record_id: record } });
    if (r.error) return toast(errorMessage(r.error), "error");
    if (r.response.status === 202) return toast("Waiting for approval");
    replace(r.data as TelegramChat);
    toast(record ? `Linked to ${(r.data as TelegramChat).record?.name ?? "the record"}` : "Left for someone to pick");
  };

  const setKind = async (c: TelegramChat, kind: "personal" | "internal" | "customer") => {
    const r = await api.PATCH("/v1/brands/{brand}/telegram-chats/{id}", { params: { path: { brand: pickedBrandId(), id: c.id } }, body: { kind } });
    if (r.error) return toast(errorMessage(r.error), "error");
    if (kind === "personal" || kind === "internal") setRows((rs) => (rs ?? []).filter((x) => x.id !== c.id || view === "all").map((x) => (x.id === c.id ? (r.data as TelegramChat) : x)));
    else replace(r.data as TelegramChat);
    toast(kind === "personal" ? "Marked personal; its messages are removed" : kind === "internal" ? "Marked as a colleague" : "Marked as a customer");
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="inline-flex rounded-md border p-0.5 text-[13px]">
          {(["queue", "waiting", "all"] as const).map((v) => (
            <button key={v} type="button" onClick={() => setView(v)} className={cn("rounded px-3 py-1", view === v ? "bg-accent font-medium" : "text-muted-foreground hover:text-foreground")}>
              {v === "queue" ? "To confirm" : v === "waiting" ? "Waiting for an answer" : "All chats"}
            </button>
          ))}
        </div>
        {view !== "all" && (
          <label className="flex items-center gap-1.5 text-[13px] text-muted-foreground">
            <input type="checkbox" checked={mine} onChange={(e) => setMine(e.target.checked)} />
            Only my accounts
          </label>
        )}
      </div>
      {err && <ErrorText>{err}</ErrorText>}
      {!rows && !err && <Loading />}
      {rows && rows.length === 0 && (
        <Empty title={view === "queue" ? "Nothing to confirm" : view === "waiting" ? "Nobody is waiting" : "No chats yet"}>
          {view === "queue"
            ? "Every synced chat is linked to a customer, or still being looked at."
            : view === "waiting"
              ? "Every customer who wrote last has had an answer."
              : "Connect a company Telegram account on the Team page and start its sync to bring chats in."}
        </Empty>
      )}
      {rows && rows.length > 0 && (
        <div className="divide-y rounded-lg border">
          {rows.map((c) => (
            <Row key={c.id} c={c} canManage={canManage} onOpen={() => { setOpen(c); setItem(c.id); }} onConfirm={() => c.suggested_record && match(c, c.suggested_record.id)} onPick={() => setPicking(c)} onNone={() => match(c, null)} onKind={(k) => setKind(c, k)} />
          ))}
        </div>
      )}
      {open && <Messages chat={open} canSummarize={canManage || open.mine} onChange={(c) => { setOpen(c); setRows((rs) => rs?.map((x) => (x.id === c.id ? c : x)) ?? rs); }} onClose={() => { setOpen(null); setItem(undefined); }} />}
      {picking && (
        <PickRecord
          chat={picking}
          onClose={() => setPicking(null)}
          onPicked={async (id) => {
            await match(picking, id);
            setPicking(null);
          }}
        />
      )}
    </div>
  );
}

function Row({ c, canManage, onOpen, onConfirm, onPick, onNone, onKind }: { c: TelegramChat; canManage: boolean; onOpen: () => void; onConfirm: () => void; onPick: () => void; onNone: () => void; onKind: (k: "personal" | "internal" | "customer") => void }) {
  const linked = c.match_status === "confirmed" ? c.record : null;
  const suggested = c.match_status === "suggested" ? c.suggested_record : null;
  const writable = canManage || c.mine;
  return (
    <div className="flex flex-wrap items-start gap-3 px-4 py-3">
      <button type="button" onClick={onOpen} className="min-w-0 flex-1 text-left">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate font-medium">{c.title || c.username || c.peer_id}</span>
          {c.peer_kind !== "user" && <Badge>Group{c.members ? ` · ${c.members}` : ""}</Badge>}
          {c.kind !== "unsorted" && c.kind !== "customer" && <Badge tone="grey">{kindLabel[c.kind]}</Badge>}
          {c.suggested_kind === "personal" && c.kind === "unsorted" && <Badge tone="amber" title="The AI thinks this isn't company business">Looks personal</Badge>}
          {c.waiting_since && c.peer_kind === "user" && (c.kind === "customer" || c.kind === "unsorted") && <Badge tone={Date.now() - Date.parse(c.waiting_since) > 4 * 3600e3 ? "red" : "amber"} title={`Waiting since ${stamp(c.waiting_since)}`}>Waiting {waited(c.waiting_since)}</Badge>}
        </div>
        <div className="mt-0.5 text-[12px] text-muted-foreground">
          {[c.username && `@${c.username}`, c.phone, c.account_label, `${c.message_count} messages`, c.last_message_at && ago(c.last_message_at)].filter(Boolean).join(" · ")}
        </div>
        {linked && <div className="mt-1 text-[13px]">Linked to <span className="font-medium">{linked.name}</span></div>}
        {suggested && (
          <div className="mt-1 text-[13px]">
            Looks like <span className="font-medium">{suggested.name}</span>
            <span className="text-muted-foreground"> · {reasonLabel[c.match_reason ?? ""] ?? "Suggested"}{c.match_confidence != null && c.match_reason === "ai" ? ` (${Math.round(c.match_confidence * 100)}%)` : ""}</span>
          </div>
        )}
        {c.match_status === "no_match" && <div className="mt-1 text-[13px] text-muted-foreground">No customer found. Pick one or create one.</div>}
        {c.match_status === "pending" && <div className="mt-1 text-[13px] text-muted-foreground">Being matched…</div>}
        {c.match_note && c.match_status !== "confirmed" && <div className="mt-0.5 text-[12px] text-muted-foreground italic">{c.match_note}</div>}
      </button>
      {writable && (
        <div className="flex shrink-0 items-center gap-1.5">
          {suggested && <Button onClick={onConfirm}>Confirm</Button>}
          <Button variant={suggested ? "ghost" : "primary"} onClick={onPick}>{linked || suggested ? "Change" : "Pick a customer"}</Button>
          <Menu
            aria={`More for ${c.title}`}
            items={[
              suggested !== null && { label: "Not this one", onClick: onNone },
              c.kind !== "customer" && { label: "It's a customer", onClick: () => onKind("customer") },
              { label: "It's a colleague", onClick: () => onKind("internal") },
              { label: "It's personal (removes its messages)", onClick: () => onKind("personal"), danger: true },
            ]}
          />
        </div>
      )}
    </div>
  );
}

function PickRecord({ chat, onClose, onPicked }: { chat: TelegramChat; onClose: () => void; onPicked: (id: string) => Promise<void> }) {
  const group = chat.peer_kind !== "user";
  const [type, setType] = useState<"person" | "account">(group ? "account" : "person");
  const [q, setQ] = useState(chat.title);
  const [found, setFound] = useState<Ref[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let live = true;
    const t = setTimeout(async () => {
      const r = await api.GET("/v1/brands/{brand}/records", { params: { path: { brand: pickedBrandId() }, query: { type, q: q.trim(), limit: 20 } as never } });
      if (!live) return;
      if (r.error) setErr(errorMessage(r.error));
      else {
        setErr("");
        setFound((r.data?.data ?? []).map((x) => ({ id: x.id, type: x.type, name: x.name || x.id })));
      }
    }, 250);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [q, type]);

  const create = async () => {
    const name = q.trim();
    if (!name) return;
    setBusy(true);
    const r = await api.POST("/v1/brands/{brand}/records", { params: { path: { brand: pickedBrandId() } }, body: { type, name } });
    if (r.error) {
      setBusy(false);
      return setErr(errorMessage(r.error));
    }
    if (r.response.status === 202 || !r.data) {
      setBusy(false);
      return setErr("Creating customers is waiting for approval.");
    }
    await onPicked(r.data.id);
    setBusy(false);
  };

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Link {chat.title || "this chat"} to a customer</SheetTitle>
          <SheetDescription>The customer gets this chat's Telegram account, so future chats with them link by themselves.</SheetDescription>
        </SheetHeader>
        <div className="space-y-3 px-4 pb-6">
          <div className="flex gap-2">
            <select className={field} value={type} onChange={(e) => setType(e.target.value as "person" | "account")} aria-label="Type">
              <option value="person">Person</option>
              <option value="account">Company</option>
            </select>
            <input className={cn(field, "flex-1")} value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search by name, email or domain" aria-label="Search customers" autoFocus />
          </div>
          {err && <ErrorText>{err}</ErrorText>}
          {!found && !err && <Loading />}
          {found && (
            <div className="divide-y rounded-md border">
              {found.length === 0 && <div className="px-3 py-2 text-[13px] text-muted-foreground">No {type === "person" ? "people" : "companies"} match.</div>}
              {found.map((r) => (
                <button key={r.id} type="button" disabled={busy} onClick={() => { setBusy(true); void onPicked(r.id).finally(() => setBusy(false)); }} className="flex w-full items-center justify-between px-3 py-2 text-left text-[13px] hover:bg-accent">
                  <span className="truncate">{r.name}</span>
                  {chat.suggested_record?.id === r.id && <Badge tone="blue">Suggested</Badge>}
                </button>
              ))}
            </div>
          )}
          {q.trim() && (
            <Button variant="ghost" disabled={busy} onClick={create}>
              Create {type === "person" ? "person" : "company"} “{q.trim()}”
            </Button>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function waited(iso: string) {
  const h = Math.floor((Date.now() - Date.parse(iso)) / 3600e3);
  if (h < 1) return "under an hour";
  if (h < 48) return `${h}h`;
  return `${Math.floor(h / 24)} days`;
}

const waitingOnLabel = { us: "Waiting on us", them: "Waiting on them", nobody: "Nothing owed" } as const;

function Summary({ chat, canSummarize, onChange }: { chat: TelegramChat; canSummarize: boolean; onChange: (c: TelegramChat) => void }) {
  const [busy, setBusy] = useState(false);
  const s = chat.summary;
  const ask = async () => {
    setBusy(true);
    const before = s?.at;
    const r = await api.POST("/v1/brands/{brand}/telegram-chats/{id}/summary", { params: { path: { brand: pickedBrandId(), id: chat.id } } });
    if (r.error) {
      setBusy(false);
      return toast(errorMessage(r.error), "error");
    }
    // Written in the background: look again for up to a minute.
    for (let i = 0; i < 30; i++) {
      await new Promise((res) => setTimeout(res, 2000));
      const g = await api.GET("/v1/brands/{brand}/telegram-chats/{id}", { params: { path: { brand: pickedBrandId(), id: chat.id } } });
      if (g.data?.summary && g.data.summary.at !== before) {
        onChange(g.data);
        setBusy(false);
        return;
      }
    }
    setBusy(false);
    toast("No summary yet. Check Settings, AI has a model for Telegram summaries.", "error");
  };
  if (!s && !canSummarize) return null;
  return (
    <div className="rounded-lg border p-3 text-[13px]">
      <div className="mb-1 flex items-center justify-between gap-2">
        <span className="font-medium">Summary</span>
        {canSummarize && (
          <Button variant="ghost" disabled={busy} onClick={ask}>
            {busy ? "Summarising…" : s ? (s.stale ? "Update" : "Summarise again") : "Summarise"}
          </Button>
        )}
      </div>
      {s ? (
        <>
          <p className="whitespace-pre-wrap">{s.text}</p>
          {s.open_items.length > 0 && (
            <ul className="mt-2 list-disc pl-5 text-muted-foreground">
              {s.open_items.map((o, i) => <li key={i}>{o}</li>)}
            </ul>
          )}
          <div className="mt-2 text-[12px] text-muted-foreground">
            {waitingOnLabel[s.waiting_on]} · written {ago(s.at)}{s.stale ? " · new messages since" : ""}
          </div>
        </>
      ) : (
        <p className="text-muted-foreground">Your AI can summarise this chat: what they want, what was agreed, and what is still open.</p>
      )}
    </div>
  );
}

function Messages({ chat, canSummarize, onChange, onClose }: { chat: TelegramChat; canSummarize: boolean; onChange: (c: TelegramChat) => void; onClose: () => void }) {
  const [msgs, setMsgs] = useState<TelegramMessage[] | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    void api.GET("/v1/brands/{brand}/telegram-chats/{id}/messages", { params: { path: { brand: pickedBrandId(), id: chat.id }, query: { limit: 100 } as never } }).then((r) => {
      if (r.error) setErr(errorMessage(r.error));
      else setMsgs([...(r.data?.data ?? [])].reverse());
    });
  }, [chat.id]);
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{chat.title || chat.peer_id}</SheetTitle>
          <SheetDescription>{chat.account_label} · {chat.message_count} messages</SheetDescription>
        </SheetHeader>
        <div className="space-y-2 px-4 pb-6">
          {chat.kind !== "personal" && chat.kind !== "internal" && chat.message_count > 0 && <Summary chat={chat} canSummarize={canSummarize} onChange={onChange} />}
          {err && <ErrorText>{err}</ErrorText>}
          {!msgs && !err && <Loading />}
          {msgs && msgs.length === 0 && <div className="text-[13px] text-muted-foreground">No messages kept for this chat.</div>}
          {msgs?.map((m) =>
            m.service ? (
              <div key={m.id} className="text-center text-[12px] text-muted-foreground">{m.text}</div>
            ) : (
              <div key={m.id} className={cn("max-w-[85%] rounded-lg px-3 py-2 text-[13px]", m.outgoing ? "ml-auto bg-brand/10" : "bg-muted")}>
                {!m.outgoing && m.from_name && chat.peer_kind !== "user" && <div className="text-[12px] font-medium">{m.from_name}</div>}
                {m.media && <div className="text-[12px] text-muted-foreground">[{m.media}{m.file_name ? `: ${m.file_name}` : ""}]</div>}
                {m.text && <div className="whitespace-pre-wrap break-words">{m.text}</div>}
                <div className="mt-0.5 text-[11px] text-muted-foreground" title={stamp(m.sent_at)}>{ago(m.sent_at)}{m.edited_at ? " · edited" : ""}</div>
              </div>
            ),
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
