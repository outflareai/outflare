import { useEffect, useState } from "react";
import type { SupportAssistantRun, SupportAssistantSetting } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, Loading, field, toast } from "../ui";
import { Icon } from "../icons";
import { cn } from "@/lib/utils";
import { useBrand } from "@/lib/brand";
import type { Tab } from "@/lib/paths";
import { ConnectFor } from "./connect-for";
import { cloudCapability, useCapabilities } from "@/lib/cloud";
import { track } from "@/lib/analytics";

// The support assistant: its settings card in Support settings, and the
// draft it left on a conversation, shown above the composer.

type Mode = SupportAssistantSetting["chat_mode"];

// Why it left a conversation to a person, in the team's words.
const handoffLabel: Record<string, string> = {
  not_in_knowledge: "your knowledge doesn't cover this yet. Your reply will be offered as a new answer in Knowledge",
  not_grounded: "its answer said more than your articles do",
  style: "it couldn't write a short, clean answer",
  asked_for_person: "they asked for a person",
  sensitive_topic: "it's about a dispute, legal, security or personal data",
  too_many_replies: "it already answered several times here",
  not_helpful: "the customer said its answer didn't help",
  needs_account: "it needs their account details",
  needs_action: "it needs someone to do something, like a refund or a change",
  upset: "they sound upset",
  unclear: "it wasn't sure what they need",
  error: "the AI call failed",
  topic: "your team always answers this topic",
  other: "it wasn't sure",
};

type Channel = { id: string; name: string; private: boolean };
type Integration = { id: string; connector: string; status: string; name: string; account?: { name?: string } | null };
type TestResult = { action: string; reason: string; reply: string; cited: SupportAssistantRun["cited"]; duration_ms: number };

export function SupportAssistantSettings({ ws, canManage, go }: { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void }) {
  const { brands } = useBrand(ws);
  const [all, setAll] = useState<SupportAssistantSetting[] | undefined>(undefined);
  const [scope, setScope] = useState("default");
  const [chat, setChat] = useState<Mode>("off");
  const [email, setEmail] = useState<Mode>("off");
  const [name, setName] = useState("");
  const [website, setWebsite] = useState(true);
  const [hold, setHold] = useState(5);
  const [slackID, setSlackID] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [slacks, setSlacks] = useState<Integration[]>([]);
  const [channels, setChannels] = useState<Channel[] | null>(null);
  const [auto, setAuto] = useState(false);
  // On Outflare Cloud Auto can still be off for one workspace (staff
  // turned it off); elsewhere it is a Cloud feature.
  const caps = useCapabilities();
  const onCloud = !!caps?.includes("support_auto_replies");
  const autoWhy = onCloud ? "Outflare staff turned Auto off for this workspace; contact support to turn it back on" : "Answering without a person is part of Outflare Cloud";
  const [stats, setStats] = useState<Report | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const brandID = scope === "default" ? null : scope;
  const own = all?.find((x) => x.brand_id === brandID) ?? null;
  const inherited = all?.find((x) => x.brand_id === null) ?? null;
  const shown = own ?? (brandID ? inherited : null);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/support/assistant-settings", { params: { path: { ws } } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      setAll(data.data);
      setAuto(data.auto_available);
    });
    api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws } } }).then(({ data }) => {
      setSlacks(((data?.data ?? []) as Integration[]).filter((i) => i.connector === "slack_knowledge" && i.status === "connected"));
    });
    api.GET("/v1/workspaces/{ws}/support/assistant/stats", { params: { path: { ws } } }).then(({ data }) => {
      if (data) setStats(data as Report);
    });
  }, [ws]);

  // The form shows the scope's own setting, or what it inherits.
  useEffect(() => {
    if (all === undefined) return;
    setChat(shown?.chat_mode ?? "off");
    setEmail(shown?.email_mode ?? "off");
    setName(shown?.name ?? "");
    setWebsite(shown?.find_on_website ?? true);
    setHold(shown?.email_hold_minutes ?? 5);
    setSlackID(shown?.slack_integration_id ?? "");
    setPicked(shown?.slack_channels.map((c) => c.id) ?? []);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [all, scope]);

  useEffect(() => {
    setChannels(null);
    if (!slackID || !canManage) return;
    api.GET("/v1/workspaces/{ws}/support/assistant/slack-channels", { params: { path: { ws }, query: { integration_id: slackID } } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      setChannels(data.data);
    });
  }, [ws, slackID, canManage]);

  async function save() {
    setBusy(true);
    setError("");
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/support/assistant-settings/{scope}", {
      params: { path: { ws, scope } },
      body: {
        chat_mode: chat, email_mode: email, name: name.trim(), find_on_website: website,
        slack_integration_id: slackID && picked.length ? slackID : null, slack_channels: slackID && picked.length ? picked : [],
        email_hold_minutes: hold,
      },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    const saved = data as SupportAssistantSetting;
    setAll((l) => [...(l ?? []).filter((x) => x.brand_id !== saved.brand_id), saved]);
    toast(chat === "off" && email === "off" ? "The assistant is off" : chat === "auto" || email === "auto" ? "Saved: it answers on its own where it can" : "Saved: it drafts replies for your team to send");
  }

  async function removeBrand() {
    setBusy(true);
    const { error } = await api.DELETE("/v1/workspaces/{ws}/support/assistant-settings/{scope}", { params: { path: { ws, scope } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setAll((l) => (l ?? []).filter((x) => x.brand_id !== brandID));
    toast("This brand uses the workspace's settings again");
  }

  if (all === undefined && !error) return <Loading />;
  const on = !!shown && (shown.chat_mode !== "off" || shown.email_mode !== "off");

  const modeRow = (label: string, hint: string, value: Mode, set: (m: Mode) => void) => (
    <div className="flex flex-wrap items-center gap-3 py-2.5">
      <div className="min-w-0 flex-1">
        <p className="text-[13px] font-medium">{label}</p>
        <p className="text-xs text-muted-foreground">{hint}</p>
      </div>
      <div className="flex rounded-md border p-0.5" role="radiogroup" aria-label={label}>
        {(["off", "draft", "auto"] as const).map((m) => (
          <button
            key={m}
            type="button"
            role="radio"
            aria-checked={value === m}
            disabled={!canManage || (m === "auto" && !auto)}
            title={m === "auto" && !auto ? autoWhy : m === "auto" ? "Sends checked answers on its own, signed as the assistant" : undefined}
            onClick={() => set(m)}
            className={cn(
              "rounded px-2.5 py-1 text-xs font-medium disabled:cursor-not-allowed",
              value === m ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground",
              m === "auto" && !auto && "opacity-50",
            )}
          >
            {m === "off" ? "Off" : m === "draft" ? "Draft" : "Auto"}
          </button>
        ))}
      </div>
    </div>
  );

  return (
    <div className="space-y-3 rounded-lg border p-4">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <p className="flex items-center gap-1.5 text-[13px] font-medium">
            <Icon name="ai" className="size-3.5 text-violet-600 dark:text-violet-400" />AI assistant
          </p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Answers new messages in a few short sentences from your approved knowledge, asks one question back, or leaves it to your team with the reason.
            It never answers disputes, legal, security or data requests, or anyone who asks for a person.
          </p>
        </div>
        <span className={cn("rounded-full px-2 py-0.5 text-[11px] font-medium", on ? "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400" : "bg-muted text-muted-foreground")}>{on ? "On" : "Off"}</span>
      </div>
      {brands.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <select aria-label="Settings for" className={cn(field, "w-auto")} value={scope} onChange={(e) => setScope(e.target.value)}>
            <option value="default">All brands</option>
            {brands.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
          </select>
          {brandID && (own
            ? canManage && <button type="button" className="text-muted-foreground underline" disabled={busy} onClick={removeBrand}>Use the workspace's settings</button>
            : <span className="text-muted-foreground">Uses the workspace's settings until you save its own.</span>)}
        </div>
      )}
      <div className="divide-y">
        {modeRow("Email", "Up to about 140 words, the answer first", email, setEmail)}
        {modeRow("Website chat", "One to three short sentences", chat, setChat)}
      </div>
      {!auto && caps !== null && (
        <p className="text-xs text-muted-foreground">
          {onCloud ? (
            <>Auto is off for this workspace: Outflare staff turned it off. Drafts keep working; contact support to turn it back on.</>
          ) : (
            <>
              <span className="font-medium text-foreground">Auto</span> is part of Outflare Cloud: {cloudCapability("support_auto_replies").line}{" "}
              <a className="underline" href="https://outflare.ai" target="_blank" rel="noopener noreferrer">outflare.ai</a>
            </>
          )}
        </p>
      )}
      {(chat === "auto" || email === "auto") && (
        <p className="rounded-md bg-violet-500/5 px-3 py-2 text-xs text-muted-foreground">
          In Auto it sends answers that pass every check, signed as the assistant. Anything it hands off waits for your team; on chat it tells the visitor a person will reply.
        </p>
      )}
      {email === "auto" && (
        <label className="flex flex-wrap items-center gap-2 text-[13px]">
          <span className="font-medium">Wait before sending an email</span>
          <input type="number" aria-label="Minutes to wait before sending an email" className={cn(field, "w-20")} min={0} max={60} value={hold}
            disabled={!canManage} onChange={(e) => setHold(Math.max(0, Math.min(60, Math.round(Number(e.target.value) || 0))))} />
          <span>minutes</span>
          <span className="basis-full text-xs text-muted-foreground">
            {hold === 0 ? "Email answers go at once." : "The answer waits in the conversation as a draft, so your team can stop or edit it before it goes. It never goes if someone replies first or the customer writes again."}
          </span>
        </label>
      )}
      <label className="block space-y-1">
        <span className="text-[13px] font-medium">Name</span>
        <input className={cn(field, "w-full")} value={name} maxLength={60} disabled={!canManage} placeholder="Your company's assistant" onChange={(e) => setName(e.target.value)} />
        <span className="block text-xs text-muted-foreground">It says it's an AI assistant, never a person.</span>
      </label>
      <fieldset className="space-y-2" disabled={!canManage}>
        <legend className="text-[13px] font-medium">When your knowledge has no answer</legend>
        <p className="text-xs text-muted-foreground">It looks here and suggests what it finds in Knowledge, for you to approve before anyone uses it.</p>
        <label className="flex items-center gap-2 text-[13px]">
          <input type="checkbox" checked={website} onChange={(e) => setWebsite(e.target.checked)} />Your website
        </label>
        <div className="space-y-2">
          <p className="flex items-center gap-2 text-[13px]">Slack channels</p>
          {slacks.length === 0 ? (
            <div className="flex flex-wrap items-center gap-2">
              <ConnectFor connector="slack_knowledge" name="Slack" back="support" variant="ghost" label="Connect Slack (knowledge)"
                why="The support assistant reads only the channels you pick here, such as #support, to find answers your team already gave. Read only; nothing is used until you approve it." go={go} />
            </div>
          ) : (
            <>
              <select aria-label="Slack connection" className={cn(field, "w-auto")} value={slackID} onChange={(e) => { setSlackID(e.target.value); setPicked([]); }}>
                <option value="">Don't read Slack</option>
                {slacks.map((i) => <option key={i.id} value={i.id}>{i.account?.name || i.name || "Slack"}</option>)}
              </select>
              {slackID && (channels === null ? <Loading /> : (
                <div className="flex max-h-40 flex-col gap-1 overflow-y-auto rounded-md border p-2">
                  {channels.map((c) => (
                    <label key={c.id} className="flex items-center gap-2 text-[13px]">
                      <input type="checkbox" checked={picked.includes(c.id)} disabled={!picked.includes(c.id) && picked.length >= 20}
                        onChange={(e) => setPicked((p) => (e.target.checked ? [...p, c.id] : p.filter((x) => x !== c.id)))} />
                      #{c.name}{c.private && <span className="text-xs text-muted-foreground">private: invite the app first</span>}
                    </label>
                  ))}
                </div>
              ))}
            </>
          )}
        </div>
      </fieldset>
      {stats && stats.runs > 0 && <AssistantReport r={stats} />}
      {error && <ErrorText>{error}</ErrorText>}
      {canManage && (
        <div className="flex justify-end">
          <Button disabled={busy || (!!slackID && picked.length === 0)} onClick={save}>{busy ? "Saving…" : "Save"}</Button>
        </div>
      )}
      {canManage && <TryAssistant ws={ws} brand={brandID} />}
    </div>
  );
}

type Rates = { answered: number | null; handed_off: number | null; sent_as_written: number | null; helpful: number | null };
type Report = {
  days: number;
  runs: number;
  handoff_reasons: Record<string, number>;
  rates: Rates;
  previous: { runs: number; rates: Rates };
};

// Hand-off reasons, short, for the report.
const reasonShort: Record<string, string> = {
  not_in_knowledge: "your knowledge didn't cover it",
  not_grounded: "its answer said more than your articles",
  style: "it couldn't keep the answer short and clean",
  asked_for_person: "they asked for a person",
  sensitive_topic: "disputes, legal, security or data",
  too_many_replies: "it had already answered several times",
  not_helpful: "they said an answer didn't help",
  needs_account: "it needed their account",
  needs_action: "someone had to do something",
  upset: "they sounded upset",
  unclear: "it wasn't clear what they needed",
  error: "the AI call failed",
};

const pct = (v: number | null) => (v === null ? "–" : `${Math.round(v * 100)}%`);

// AssistantReport shows the last 30 days against the 30 before: four
// shares and the commonest reasons it left conversations to the team.
function AssistantReport({ r }: { r: Report }) {
  const tiles: { key: keyof Rates; label: string; hint: string }[] = [
    { key: "answered", label: "Answered", hint: "of the messages it looked at" },
    { key: "handed_off", label: "Left to your team", hint: "with the reason" },
    { key: "sent_as_written", label: "Sent as written", hint: "of the drafts your team decided on" },
    { key: "helpful", label: "Helpful", hint: "of the chat answers visitors rated" },
  ];
  const reasons = Object.entries(r.handoff_reasons).sort((a, b) => b[1] - a[1]).slice(0, 3);
  return (
    <section aria-label="How the assistant is doing" className="space-y-2">
      <p className="text-[13px] font-medium">Last {r.days} days</p>
      <dl className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {tiles.map((t) => {
          const now = r.rates[t.key], before = r.previous.rates[t.key];
          return (
            <div key={t.key} className="rounded-md border px-3 py-2" title={t.hint}>
              <dt className="text-[11px] text-muted-foreground">{t.label}</dt>
              <dd className="text-lg font-semibold tabular-nums">{pct(now)}</dd>
              {before !== null && now !== null && (
                <dd className="text-[11px] text-muted-foreground tabular-nums">{pct(before)} the {r.days} days before</dd>
              )}
            </div>
          );
        })}
      </dl>
      {reasons.length > 0 && (
        <p className="text-xs text-muted-foreground">
          Left to your team most often because {reasons.map(([k, n], i) => (
            <span key={k}>{i > 0 && (i === reasons.length - 1 ? " and " : ", ")}{reasonShort[k] ?? "other reasons"} ({n})</span>
          ))}.
        </p>
      )}
    </section>
  );
}

const actionLabel: Record<string, string> = { answer: "Would answer", clarify: "Would ask", handoff: "Would leave it to your team", skipped: "Would stay out of it" };

// TryAssistant asks the assistant a made-up question: nothing is stored
// or sent.
function TryAssistant({ ws, brand }: { ws: string; brand: string | null }) {
  const [q, setQ] = useState("");
  const [channel, setChannel] = useState<"email" | "chat">("chat");
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<TestResult | null>(null);

  async function run() {
    setBusy(true);
    setRes(null);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/support/assistant/test", { params: { path: { ws } }, body: { question: q.trim(), channel, brand } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setRes(data as TestResult);
    track("ui.support_assistant_tried", { channel, action: (data as TestResult).action });
  }

  return (
    <div className="space-y-2 border-t pt-3">
      <p className="text-[13px] font-medium">Try it</p>
      <div className="flex flex-col gap-2 sm:flex-row">
        <input aria-label="A customer's question" className={cn(field, "flex-1")} value={q} maxLength={2000} placeholder="Ask what a customer would ask…"
          onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === "Enter" && q.trim() && !busy && run()} />
        <select aria-label="Channel" className={cn(field, "w-auto")} value={channel} onChange={(e) => setChannel(e.target.value as "email" | "chat")}>
          <option value="chat">Chat</option>
          <option value="email">Email</option>
        </select>
        <Button variant="ghost" disabled={busy || !q.trim()} onClick={run}>{busy ? "Thinking…" : "Try"}</Button>
      </div>
      {res && (
        <div className="space-y-1.5 rounded-md bg-muted/50 px-3 py-2.5 text-[13px]">
          <p className="text-[11px] font-medium text-muted-foreground">
            {actionLabel[res.action] ?? res.action}
            {res.action === "handoff" && `: ${handoffLabel[res.reason] ?? handoffLabel.other}`} · {(res.duration_ms / 1000).toFixed(1)}s
          </p>
          {res.reply && <p className="whitespace-pre-wrap">{res.reply}</p>}
          {res.cited.length > 0 && <p className="text-[11px] text-muted-foreground">From {res.cited.map((c) => c.title).join(", ")}</p>}
        </div>
      )}
    </div>
  );
}

// AssistantDraft shows what the assistant left on a conversation: a draft
// to use or discard, or why it left it to the team.
export function AssistantDraft({ ws, id, run, onUse, onDiscarded, onChanged }: {
  ws: string;
  id: string;
  run: SupportAssistantRun;
  onUse: (r: SupportAssistantRun) => void;
  onDiscarded: () => void;
  // onChanged reloads the conversation: a held reply was stopped or sent.
  onChanged: (r?: SupportAssistantRun) => void;
}) {
  const [busy, setBusy] = useState(false);
  const left = useCountdown(run.send_at);
  // When its hold ends, the reply goes: show the conversation as it is then.
  useEffect(() => {
    if (left !== 0) return;
    const t = setTimeout(() => onChanged(), 3000);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [left]);
  if (run.action === "skipped" || run.conversation_id !== id) return null;
  if (run.action === "handoff") {
    return (
      <p className="mb-2 flex items-start gap-1.5 rounded-md bg-muted/60 px-3 py-2 text-xs text-muted-foreground">
        <Icon name="ai" className="mt-0.5 size-3.5 shrink-0" />
        <span>The assistant left this to you: {handoffLabel[run.reason] ?? handoffLabel.other}.</span>
      </p>
    );
  }
  if (run.outcome !== "drafted") return null;

  const held = left !== null && left > 0;

  // stop keeps a held reply from going; it stays a draft.
  async function stop(): Promise<SupportAssistantRun | null> {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/support/assistant-runs/{id}/stop", { params: { path: { ws, id: run.id } } });
    setBusy(false);
    if (error) {
      toast(errorMessage(error), "error");
      return null;
    }
    track("ui.support_assistant_reply_stopped", {});
    return data as SupportAssistantRun;
  }

  // Editing a held reply stops it first, so it can't go while you edit.
  async function use() {
    track("ui.support_assistant_draft_used", { action: run.action });
    if (held) {
      const r = await stop();
      if (!r) return;
      return onUse(r);
    }
    onUse(run);
  }

  async function discard() {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/support/assistant-runs/{id}/discard", { params: { path: { ws, id: run.id } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    onDiscarded();
  }

  return (
    <div className="mb-2 space-y-2 rounded-md border border-violet-500/30 bg-violet-500/5 px-3 py-2.5">
      <p className="flex items-center gap-1.5 text-[11px] font-medium text-violet-700 dark:text-violet-400">
        <Icon name="ai" className="size-3.5" />
        {left !== null
          ? left > 0 ? `The assistant sends this in ${formatLeft(left)}` : "The assistant is sending this…"
          : run.action === "clarify" ? "The assistant would ask" : "The assistant drafted"}
      </p>
      <p className="text-[13px] whitespace-pre-wrap">{run.reply}</p>
      {run.cited.length > 0 && (
        <p className="text-[11px] text-muted-foreground">
          From{" "}
          {run.cited.map((c, i) => (
            <span key={c.id}>
              {i > 0 && ", "}
              {c.url ? <a className="underline" href={c.url} target="_blank" rel="noopener noreferrer">{c.title}</a> : c.title}
            </span>
          ))}
        </p>
      )}
      <div className="flex gap-2">
        {held && <Button disabled={busy} onClick={async () => { const r = await stop(); if (r) onChanged(r); }}>Stop</Button>}
        <Button variant={held ? "ghost" : undefined} disabled={busy} onClick={use}>{held ? "Edit" : "Use this"}</Button>
        <Button variant="ghost" disabled={busy} onClick={discard}>Discard</Button>
      </div>
    </div>
  );
}

// useCountdown is the seconds until at, every second; null without one.
function useCountdown(at: string | null | undefined): number | null {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!at) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [at]);
  if (!at) return null;
  return Math.max(0, Math.round((new Date(at).getTime() - now) / 1000));
}

function formatLeft(s: number): string {
  const m = Math.floor(s / 60);
  return `${m}:${String(s % 60).padStart(2, "0")}`;
}
