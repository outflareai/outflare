// Two views behind Needs attention (docs/signals.md): which conversations
// the brand's own AI reads for signals, and whose signals turned into
// revenue.

import { useCallback, useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { track } from "@/lib/analytics";
import { cn } from "@/lib/utils";
import { api, errorMessage } from "../api";
import { plural } from "../format";
import { Button, Card, Empty, ErrorText, field, Loading, Stat, Stats, toast } from "../ui";
import type { Tab } from "../App";

type Settings = Schemas["ConversationSignals"];
type Credit = Schemas["SignalCredit"];
type Channel = Settings["channels"][number];

const channels: { id: Channel; label: string; help: string }[] = [
  { id: "support", label: "Support", help: "Email and chat conversations in Support." },
  { id: "telegram", label: "Telegram", help: "Chats from company Telegram accounts that are linked to a person or company." },
  { id: "email", label: "Email the team shares", help: "Only email your team has chosen to share and the privacy check found to be about work. Private mail is never read." },
  { id: "call", label: "Calls", help: "Transcripts your own call recorder sends in through the API. Outflare records no calls." },
];

export const channelText: Record<string, string> = { support: "a support conversation", telegram: "a Telegram chat", email: "email the team shares", call: "a call" };

function money(minor: number, currency: string) {
  return new Intl.NumberFormat(undefined, { style: "currency", currency: currency || "USD", maximumFractionDigits: 0 }).format(minor / 100);
}

// Which conversations are read. Off until someone switches it on, since
// every read is a call on the workspace's own AI.
export function ConversationSettings({ brandId, canManage, go }: { brandId: string; canManage: boolean; go: (tab: Tab) => void }) {
  const [s, setS] = useState<Settings | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/conversation-signals", { params: { path: { brand: brandId } } }).then(({ data, error }) => (data ? setS(data) : setError(errorMessage(error))));
  }, [brandId]);
  async function save(next: { enabled: boolean; channels: Channel[] }) {
    setBusy(true);
    const { data, error } = await api.PUT("/v1/brands/{brand}/conversation-signals", { params: { path: { brand: brandId } }, body: next });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    setS(data);
    track("ui.conversation_signals_saved", { enabled: data.enabled, channels: data.channels.length });
    toast(data.enabled ? "Conversations are read for signals" : "Reading conversations is off");
  }
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!s) return <Loading />;
  return (
    <div className="max-w-2xl space-y-4">
      <Card
        title="Read conversations for signals"
        action={
          canManage && (
            <Button variant={s.enabled ? "ghost" : "primary"} disabled={busy} onClick={() => save({ enabled: !s.enabled, channels: s.channels })}>
              {s.enabled ? "Switch off" : "Switch on"}
            </Button>
          )
        }
      >
        <div>
          <div>
            <p className="text-[13px] text-muted-foreground">
              Your AI reads what customers say and raises a signal when it hears something sales should act on: they are growing, want to commit, ask for something you don't do, mention a competitor, or want to
              leave. Each signal quotes the customer's own words and goes to the account's owner. The same read notes how the customer sounds, for a sentiment score: a conversation with no feeling either way says
              nothing, so unknown never counts as unhappy.
            </p>
          </div>
        </div>
        {!s.ai_connected && (
          <p className="mt-3 rounded-md border border-amber-300/60 bg-amber-50 px-3 py-2 text-[13px] text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
            Nothing is read until this workspace connects its own AI model.{" "}
            <button className="underline" onClick={() => go("ai")}>
              Connect one in Settings, AI
            </button>
          </p>
        )}
        <ul className="mt-4 divide-y border-t">
          {channels.map((c) => {
            const on = s.channels.includes(c.id);
            return (
              <li key={c.id} className="flex items-start gap-3 py-3">
                <input
                  id={`ch-${c.id}`}
                  type="checkbox"
                  className="mt-0.5"
                  checked={on}
                  disabled={!canManage || busy}
                  onChange={() => save({ enabled: s.enabled, channels: on ? s.channels.filter((x) => x !== c.id) : [...s.channels, c.id] })}
                />
                <label htmlFor={`ch-${c.id}`} className="min-w-0 text-[13px]">
                  <span className="font-medium">{c.label}</span>
                  <span className="block text-muted-foreground">{c.help}</span>
                </label>
              </li>
            );
          })}
        </ul>
      </Card>
      <p className="text-xs text-muted-foreground">
        {s.enabled
          ? `${plural(s.read_30d, "conversation")} read and ${plural(s.raised_30d, "signal")} raised in the last 30 days. `
          : "Off: nothing is read and nothing is spent. "}
        Reads run on your own AI account, about ten minutes after a customer writes and only when they said something new. Set a monthly limit for "Conversation signals" in Settings, AI.
      </p>
    </div>
  );
}

const sourceLabel: Record<string, string> = {
  support: "Support",
  telegram: "Telegram",
  email: "Shared email",
  call: "Calls",
  event: "Rules and events",
  journey: "Journeys",
  manual: "Flagged on an account",
  api: "The API",
  agent: "Agents",
  feed: "Slack channels",
};

// Whose signals turned into revenue.
export function Credit({ brandId }: { brandId: string }) {
  const [days, setDays] = useState(90);
  const [c, setC] = useState<Credit | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/signal-credit", { params: { path: { brand: brandId }, query: { days } } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setC(data);
  }, [brandId, days]);
  useEffect(() => {
    load();
  }, [load]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!c) return <Loading />;
  const raised = c.sources.reduce((n, s) => n + s.raised, 0);
  const won = c.sources.reduce((n, s) => n + s.won, 0);
  const revenue = c.sources.reduce((n, s) => n + s.revenue_minor, 0);
  const th = "px-4 py-2 text-left text-xs font-medium text-muted-foreground";
  const num = "px-4 py-2.5 text-right tabular-nums";
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <p className="min-w-0 flex-1 basis-72 text-[13px] text-muted-foreground">
          A signal closed as won is credited with what the account paid in the {c.credit_days} days after it was raised.
        </p>
        <select
          aria-label="Period"
          className={field}
          value={days}
          onChange={(e) => {
            setDays(Number(e.target.value));
            track("ui.signal_credit_period_changed", { days: Number(e.target.value) });
          }}
        >
          <option value={30}>Last 30 days</option>
          <option value={90}>Last 90 days</option>
          <option value={180}>Last 6 months</option>
          <option value={365}>Last 12 months</option>
        </select>
      </div>
      {raised === 0 ? (
        <Empty title="No signals in this period">Signals show here once they are raised: by a rule, by your AI reading conversations, or by a teammate pressing "Flag for sales".</Empty>
      ) : (
        <>
          <Stats>
            <Stat label="Signals raised" value={String(raised)} />
            <Stat label="Taken on" value={String(c.sources.reduce((n, s) => n + s.confirmed, 0))} />
            <Stat label="Won" value={String(won)} />
            <Stat label="Revenue after a won signal" value={money(revenue, c.currency)} />
          </Stats>
          <section>
            <h2 className="mb-2 text-sm font-medium">Who flagged them</h2>
            {c.people.length === 0 ? (
              <p className="text-[13px] text-muted-foreground">Nobody has flagged a signal by hand in this period. "Flag for sales" is on every support conversation, Telegram chat and account page.</p>
            ) : (
              <div className="overflow-x-auto rounded-xl border">
                <table className="w-full text-sm">
                  <thead className="border-b bg-muted/40">
                    <tr>
                      <th className={th}>Teammate</th>
                      <th className={cn(th, "text-right")}>Flagged</th>
                      <th className={cn(th, "text-right")}>Taken on</th>
                      <th className={cn(th, "text-right")}>Won</th>
                      <th className={cn(th, "text-right")}>Revenue</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y">
                    {c.people.map((p, i) => (
                      <tr key={p.user_id}>
                        <td className="px-4 py-2.5">
                          <span className="mr-2 inline-block w-4 text-xs text-muted-foreground tabular-nums">{i + 1}</span>
                          {p.name || "Unnamed"}
                        </td>
                        <td className={num}>{p.flagged}</td>
                        <td className={num}>{p.confirmed}</td>
                        <td className={num}>{p.won}</td>
                        <td className={cn(num, "font-medium")}>{money(p.revenue_minor, c.currency)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
          <section>
            <h2 className="mb-2 text-sm font-medium">Where signals come from</h2>
            <div className="overflow-x-auto rounded-xl border">
              <table className="w-full text-sm">
                <thead className="border-b bg-muted/40">
                  <tr>
                    <th className={th}>Source</th>
                    <th className={cn(th, "text-right")}>Raised</th>
                    <th className={cn(th, "text-right")}>Taken on</th>
                    <th className={cn(th, "text-right")}>Won</th>
                    <th className={cn(th, "text-right")}>Not useful</th>
                    <th className={cn(th, "text-right")}>Revenue</th>
                  </tr>
                </thead>
                <tbody className="divide-y">
                  {c.sources.map((s) => (
                    <tr key={s.source + String(s.ai)}>
                      <td className="px-4 py-2.5">
                        {sourceLabel[s.source] ?? s.source}
                        <span className="ml-2 text-xs text-muted-foreground">{s.ai ? "found by AI" : ["support", "telegram", "email", "call"].includes(s.source) ? "flagged by a person" : ""}</span>
                      </td>
                      <td className={num}>{s.raised}</td>
                      <td className={num}>{s.confirmed}</td>
                      <td className={num}>{s.won}</td>
                      <td className={num}>{s.not_useful}</td>
                      <td className={cn(num, "font-medium")}>{money(s.revenue_minor, c.currency)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </>
      )}
    </div>
  );
}
