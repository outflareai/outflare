import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { useEffect, useMemo, useState } from "react";
import type { Alert, AlertKind, AlertSetting, Integration } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, Empty, ErrorText, field, PageHeader, toastUndo } from "../ui";
import { Checkbox } from "@/components/ui/checkbox";
import { appPath, type Tab } from "@/lib/paths";

type Channel = { id: string; name: string; private: boolean };
type Settings = { kinds: AlertKind[]; default_email: string; daily_email_limit: number; settings: AlertSetting[]; muted: string[] };

// What the "Show" dropdown can narrow the kinds to. Every bulk button
// acts on exactly what is shown (or ticked), and says how many.
const shows = [
  { value: "all", label: "All kinds" },
  { value: "unmuted", label: "Not muted for me" },
  { value: "muted", label: "Muted for me" },
  { value: "email:owner", label: "Emailed to the person it's for" },
  { value: "email:admins", label: "Emailed to owners and admins" },
  { value: "email:off", label: "Not emailed" },
];

const choices = [
  { value: "owner", label: "The person it's for" },
  { value: "admins", label: "Owners and admins" },
  { value: "off", label: "Nobody, keep it here" },
];

const count = (n: number) => (n === 1 ? "1 kind" : `${n} kinds`);

// The page an alert opens: its path is "{tab}/{item}" under the workspace.
function link(ws: string, path: string) {
  // A page of its own on this server, such as an extension's "/billing".
  if (path.startsWith("/") && !path.startsWith("//")) return path;
  const [tab, ...item] = path.split("/");
  return appPath(ws, tab as Tab, item.length ? item.join("/") : undefined);
}

// Alerts: what needs someone now, from every part of Outflare, and who
// hears about it: by email, in a Slack channel and in a chat of the
// workspace's own Telegram bot.
export function Alerts({ ws, canManage }: { ws: string; canManage: boolean }) {
  const { brand, active } = useBrand(ws);
  const brandId = brand?.id ?? "";
  const path = { params: { path: { ws } } };
  const [list, setList] = useState<Alert[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [set, setSet] = useState<Settings | null>(null);
  const [slack, setSlack] = useState<Integration | null>(null);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [bot, setBot] = useState<Integration | null>(null);
  const [chats, setChats] = useState<Channel[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [show, setShow] = useState("all");
  const [q, setQ] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());

  // The workspace's own Slack and Telegram bot, and where each can post.
  useEffect(() => {
    if (!canManage) return;
    api.GET("/v1/workspaces/{ws}/integrations", path).then(({ data }) => {
      const found = data?.data.find((i) => i.connector === "slack" && i.status === "connected") ?? null;
      setSlack(found);
      if (found)
        api.GET("/v1/workspaces/{ws}/alerts/channels", { params: { path: { ws }, query: { integration_id: found.id } } }).then(({ data, error }) => {
          if (data) setChannels(data.data);
          if (error) setError(errorMessage(error));
        });
      const tg = data?.data.find((i) => i.connector === "telegram_bot" && i.status === "connected") ?? null;
      setBot(tg);
      if (tg)
        api.GET("/v1/workspaces/{ws}/alerts/channels", { params: { path: { ws }, query: { integration_id: tg.id } } }).then(({ data, error }) => {
          if (data) setChats(data.data);
          if (error) setError(errorMessage(error));
        });
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, canManage]);

  // Alerts, who hears about them and your mutes are all one brand's (the
  // picked one): brands share none.
  async function load() {
    const [l, s] = await Promise.all([
      brandId ? api.GET("/v1/brands/{brand}/alerts", { params: { path: { brand: brandId } } }) : Promise.resolve({ data: { data: [] as Alert[], next_cursor: null }, error: undefined }),
      brandId ? api.GET("/v1/brands/{brand}/alerts/settings", { params: { path: { brand: brandId } } }) : Promise.resolve({ data: undefined, error: undefined }),
    ]);
    if (l.data) {
      setList(l.data.data);
      setNext(l.data.next_cursor ?? null);
    }
    if (s.data) setSet(s.data as Settings);
    setError(l.error ? errorMessage(l.error) : s.error ? errorMessage(s.error) : "");
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandId]);

  async function more() {
    if (!next) return;
    setBusy("more");
    const { data, error } = await api.GET("/v1/brands/{brand}/alerts", { params: { path: { brand: brandId }, query: { cursor: next } } });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (data) {
      setList([...(list ?? []), ...data.data]);
      setNext(data.next_cursor ?? null);
    }
  }

  async function choose(kind: string, email: string) {
    setBusy(kind);
    if (!brandId) return;
    const { error } = await api.PUT("/v1/brands/{brand}/alerts/settings", { params: { path: { brand: brandId } }, body: { kind, email: email as "owner" | "admins" | "off" } });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (!error) load();
  }

  async function post(kind: string, channel: string) {
    setBusy(kind);
    const body = channel && slack ? { kind, slack_integration_id: slack.id, slack_channel: channel } : { kind, slack_integration_id: null, slack_channel: null };
    if (!brandId) return;
    const { error } = await api.PUT("/v1/brands/{brand}/alerts/settings", { params: { path: { brand: brandId } }, body });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (!error) load();
  }

  // A Telegram chat for a kind: one the bot heard from, none, or another
  // the bot is in, named by its id (Telegram has no list of a bot's chats).
  async function telegram(kind: string, chat: string) {
    if (!brandId || !bot) return;
    if (chat === "other") {
      const r = await ask({
        title: "Post to another Telegram chat",
        body: "Add your bot to the group or channel first (as an admin in a channel). Then paste the chat's number, such as -1001234567890, or a public channel's @name.",
        input: { label: "Chat id or @name", placeholder: "-1001234567890", maxLength: 64 },
        choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Post here", value: "ok", variant: "primary" }],
      });
      if (!r || !r.value.trim()) return;
      chat = r.value.trim();
    }
    setBusy(kind);
    const body = chat ? { kind, telegram_integration_id: bot.id, telegram_chat: chat } : { kind, telegram_integration_id: null, telegram_chat: null };
    const { error } = await api.PUT("/v1/brands/{brand}/alerts/settings", { params: { path: { brand: brandId } }, body });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (!error) load();
  }

  async function mute(kind: string, muted: boolean) {
    setBusy("mute-" + kind);
    if (!brandId) return;
    const { error } = await api.PUT("/v1/brands/{brand}/alerts/mutes", { params: { path: { brand: brandId } }, body: { kind, muted } });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (!error) load();
  }

  // Mutes or unmutes many kinds for you in one request, with Undo putting
  // back exactly the ones that changed.
  async function muteMany(kinds: string[], muted: boolean, undo = false) {
    if (!brandId || kinds.length === 0) return;
    const changed = kinds.filter((k) => set?.muted.includes(k) !== muted);
    setBusy("mute-many");
    const { error } = await api.PUT("/v1/brands/{brand}/alerts/mutes", { params: { path: { brand: brandId } }, body: { kinds, muted } });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (error) return;
    setPicked(new Set());
    load();
    if (!undo && changed.length > 0)
      toastUndo(`${muted ? "Muted" : "Unmuted"} ${count(changed.length)} for you`, () => muteMany(changed, !muted, true));
  }

  const mine = (kind: string) => set?.settings.find((s) => s.kind === kind);
  const current = (kind: string) => mine(kind)?.email ?? set?.kinds.find((k) => k.id === kind)?.default_email ?? set?.default_email ?? "owner";

  const shown = useMemo(() => {
    const words = q.trim().toLowerCase();
    return (set?.kinds ?? []).filter((k) => {
      const muted = set?.muted.includes(k.id) ?? false;
      if (show === "muted" && !muted) return false;
      if (show === "unmuted" && muted) return false;
      if (show.startsWith("email:") && current(k.id) !== show.slice(6)) return false;
      return !words || `${k.title} ${k.description}`.toLowerCase().includes(words);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [set, show, q]);
  const ticked = shown.filter((k) => picked.has(k.id)).map((k) => k.id);
  // The bulk buttons act on the ticked kinds, else on every kind shown.
  const target = ticked.length > 0 ? ticked : shown.map((k) => k.id);
  const all = ticked.length === 0 ? "all " : "";
  const anyMuted = target.some((k) => set?.muted.includes(k));
  const anyUnmuted = target.some((k) => !set?.muted.includes(k));

  return (
    <div className="max-w-3xl space-y-6">
      <PageHeader title="Alerts" description="What needs someone now, such as a hot reply or a hot website visitor. Each alert is kept here, sent as the alert.raised webhook and counted in the digest." />

      <Card title="Recent alerts">
        {!brandId && <PickBrand what="Alerts" brands={active.length} />}
        {brandId && list && list.length === 0 && <Empty title="No alerts yet">Hot replies and hot website visitors show here as they come in.</Empty>}
        <ul className="divide-y">
          {list?.map((a) => (
            <li key={a.id} className="flex items-start justify-between gap-3 py-2 text-sm">
              <div className="min-w-0">
                <a className="font-medium hover:underline" href={link(ws, a.path)}>{a.title}</a>
                <p className="text-muted-foreground">{a.text}</p>
              </div>
              <span className="shrink-0 text-xs text-muted-foreground">{new Date(a.created_at).toLocaleString()}</span>
            </li>
          ))}
        </ul>
        {next && <Button variant="ghost" disabled={busy !== ""} onClick={more}>{busy === "more" ? "Loading…" : "Show older"}</Button>}
      </Card>

      {set && (
        <Card
          title="Where they go"
          description={`Emails come from Outflare's own account email, at most ${set.daily_email_limit} a day for each person; the rest wait here. Emails name what happened and link to it, and never quote a message.`}
        >
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <label className="flex items-center gap-1.5 text-[13px] text-muted-foreground">Show
              <select className={field} aria-label="Show kinds of alert" value={show} onChange={(e) => { setShow(e.target.value); setPicked(new Set()); }}>
                {shows.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
              </select>
            </label>
            <input type="search" className={`${field} w-full sm:w-56`} value={q} onChange={(e) => { setQ(e.target.value); setPicked(new Set()); }} placeholder="Find a kind, such as mailbox" aria-label="Find a kind of alert" />
          </div>
          {shown.length > 0 && (
            <div role="toolbar" aria-label="Mute in bulk" className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/50 px-3 py-2 text-[13px]">
              <Checkbox aria-label="Tick every kind shown" checked={ticked.length === 0 ? false : ticked.length === shown.length ? true : "indeterminate"} onCheckedChange={(v) => setPicked(v ? new Set(shown.map((k) => k.id)) : new Set())} />
              <span className="font-medium">{ticked.length > 0 ? `${ticked.length} ticked` : `All ${count(shown.length)} shown`}</span>
              <span className="ml-auto flex flex-wrap items-center gap-2">
                {anyUnmuted && <Button disabled={busy !== ""} onClick={() => muteMany(target, true)}>{`Mute ${all}${target.length} for me`}</Button>}
                {anyMuted && <Button variant="ghost" disabled={busy !== ""} onClick={() => muteMany(target, false)}>{`Unmute ${all}${target.length} for me`}</Button>}
                {ticked.length > 0 && <Button variant="ghost" onClick={() => setPicked(new Set())}>Clear</Button>}
              </span>
            </div>
          )}
          {shown.length === 0 && <Empty title="No kinds match">Try another Show option or clear the search.</Empty>}
          <ul className="divide-y">
            {shown.map((k) => {
              const muted = set.muted.includes(k.id);
              return (
                <li key={k.id} className="grid gap-2 py-3 sm:grid-cols-[auto_1fr_auto] sm:items-center">
                  <Checkbox className="mt-0.5 self-start sm:self-center" aria-label={`Tick ${k.title}`} checked={picked.has(k.id)} onCheckedChange={(v) => { const p = new Set(picked); if (v) p.add(k.id); else p.delete(k.id); setPicked(p); }} />
                  <div className="text-sm">
                    <p className="flex items-center gap-2 font-medium">{k.title}{muted && <Badge tone="grey">Muted for you</Badge>}</p>
                    <p className="text-muted-foreground">{k.description}</p>
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    {slack && canManage && (
                      <select className={field} aria-label={`Slack channel for ${k.title}`} disabled={busy !== ""} value={mine(k.id)?.slack_channel ?? ""} onChange={(e) => post(k.id, e.target.value)}>
                        <option value="">Not in Slack</option>
                        {mine(k.id)?.slack_channel && !channels.some((c) => c.id === mine(k.id)?.slack_channel) && <option value={mine(k.id)!.slack_channel!}>#{mine(k.id)!.slack_channel_name}</option>}
                        {channels.map((c) => <option key={c.id} value={c.id}>#{c.name}</option>)}
                      </select>
                    )}
                    {bot && canManage && (
                      <select className={field} aria-label={`Telegram chat for ${k.title}`} disabled={busy !== ""} value={mine(k.id)?.telegram_chat ?? ""} onChange={(e) => telegram(k.id, e.target.value)}>
                        <option value="">Not in Telegram</option>
                        {mine(k.id)?.telegram_chat && !chats.some((c) => c.id === mine(k.id)?.telegram_chat) && <option value={mine(k.id)!.telegram_chat!}>{mine(k.id)!.telegram_chat_name}</option>}
                        {chats.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                        <option value="other">Another chat…</option>
                      </select>
                    )}
                    <select className={field} aria-label={`Who gets ${k.title} by email`} disabled={!canManage || busy !== ""} value={current(k.id)} onChange={(e) => choose(k.id, e.target.value)}>
                      {choices.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
                    </select>
                    <Button variant="ghost" disabled={busy !== ""} onClick={() => mute(k.id, !muted)}>{muted ? "Unmute for me" : "Mute for me"}</Button>
                  </div>
                </li>
              );
            })}
          </ul>
          {canManage && !slack && (
            <p className="mt-3 text-sm text-muted-foreground">
              Want them in Slack too? <a className="underline" href={appPath(ws, "connectors", "slack")}>Connect your Slack</a>, then pick a channel for each kind here.
            </p>
          )}
          {canManage && !bot && (
            <p className="mt-3 text-sm text-muted-foreground">
              Want them in Telegram? <a className="underline" href={appPath(ws, "connectors", "telegram_bot")}>Connect your Telegram bot</a>, then pick a chat for each kind here.
            </p>
          )}
          {canManage && bot && chats.length === 0 && (
            <p className="mt-3 text-sm text-muted-foreground">
              Your Telegram bot hasn&apos;t heard from a chat in the last day. Add it to the group or channel (or send it a message) and reload, or choose Another chat and paste the chat&apos;s id.
            </p>
          )}
          {!canManage && <p className="mt-3 text-sm text-muted-foreground">Only owners and admins can change who gets them. Anyone can mute a kind for themselves.</p>}
          <ErrorText>{error}</ErrorText>
        </Card>
      )}
      {!set && <ErrorText>{error}</ErrorText>}
    </div>
  );
}
