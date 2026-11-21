// One recommended action on its own page: what it would change, every
// number behind it, what to expect, and what happened to it. A person can
// ask the brand's agent about it (where the server runs one), decide it
// here, and say whether it was good or bad advice. That verdict and its
// reason are kept as one of the brand's memories, which agents read, so
// the next advice follows it.

import { useCallback, useEffect, useState } from "react";
import type { Tab } from "../../App";
import { api, errorMessage } from "../../api";
import { track } from "@/lib/analytics";
import { useCapabilities } from "@/lib/cloud";
import { Badge, Button, Card, confirmAction, ErrorText, Loading, Status, toast } from "../../ui";
import { ago } from "../../format";
import { money, n, platformName } from "../Ads";
import { changeLine, evidenceLine, impactLine, typeOf } from "./Inbox";
import { acknowledgeFinding, isAlert, isDone, isMeasuring, isOpen, isShadow, leverWord, revertFinding, spendsMore, statusOf, type Finding } from "./api";

// askAgent opens the brand agent's chat beside the page with a question
// already asked. The chat belongs to the server's extension (Outflare
// Cloud): the question waits in this tab's storage, the app opens the
// panel, and the panel takes the question and sends it. The page it is
// asked from tells the agent which action is meant.
export const askAgentEvent = "outflare:ask-agent";
export const askAgentKey = "outflare:agent-ask";
export function askAgent(prompt: string) {
  try {
    sessionStorage.setItem(askAgentKey, prompt);
  } catch {
    // Without storage the panel still opens, with nothing asked.
  }
  window.dispatchEvent(new CustomEvent(askAgentEvent));
}

// The numbers behind a finding, by the name its rule gave each one.
const labels: Record<string, string> = {
  days: "Days looked at", spend_micros: "Spent", new_payers: "New customers", signups: "Sign-ups", conversions: "Conversions (as the network counts)",
  platform_conversions: "Conversions (as the network counts)", clicks: "Clicks", impressions: "Impressions",
  cost_per_new_payer_micros: "Cost per new customer", cost_per_signup_micros: "Cost per sign-up", cost_per_conversion_micros: "Cost per conversion",
  target_cost_per_payer_micros: "Your target per new customer", target_cost_per_signup_micros: "Your target per sign-up", target_cost_per_conversion_micros: "Target per conversion",
  daily_budget_micros: "Daily budget", days_budget_spent: "Days the budget ran out", serving_days: "Days it showed ads", expected_micros: "Usual spend",
  paid_visits: "Visits from ads", with_click_id: "Visits with a click id", capture_rate: "Share of visits with a click id", lag_hours: "Hours since the last numbers",
  sent: "Conversions sent", failed: "Conversions refused", keyword: "Keyword", term: "Search term", match_type: "Match type",
  target_cpa_micros: "Target cost per conversion", target_roas: "Target return on ad spend", ratio: "Compared with usual", share: "Share",
};
const say = (key: string) => labels[key] ?? (key.charAt(0).toUpperCase() + key.slice(1)).replace(/_micros$/, "").replace(/_/g, " ");

function show(key: string, v: unknown, currency: string): string | null {
  if (v === null || v === undefined || v === "") return null;
  if (typeof v === "number") {
    if (key.endsWith("_micros")) return money(v, currency);
    if (/(rate|share)$/.test(key) && v >= 0 && v <= 1) return `${(v * 100).toFixed(1)}%`;
    if (key === "ratio") return `${v.toFixed(2)}×`;
    return Number.isInteger(v) ? n(v) : v.toFixed(2);
  }
  if (typeof v === "boolean") return v ? "Yes" : "No";
  if (typeof v === "string") return v;
  if (Array.isArray(v)) return v.every((x) => typeof x === "string" || typeof x === "number") ? v.map((x) => (typeof x === "number" && key.endsWith("_micros") ? money(x, currency) : String(x))).join(", ") : null;
  return null;
}

function Facts({ of, currency, empty }: { of: Record<string, unknown>; currency: string; empty: string }) {
  const rows = Object.entries(of).map(([k, v]) => [say(k), show(k, v, currency)] as const).filter((r): r is readonly [string, string] => r[1] !== null);
  if (rows.length === 0) return <p className="text-[13px] text-muted-foreground">{empty}</p>;
  return (
    <table className="w-full max-w-xl text-[13px]">
      <tbody>
        {rows.map(([label, value]) => (
          <tr key={label} className="border-b last:border-0">
            <th scope="row" className="py-1.5 pr-4 text-left font-normal text-muted-foreground">{label}</th>
            <td className="py-1.5 text-right font-mono tabular-nums">{value}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// Where the action stands, in a few words.
function standing(f: Finding): { word: string; tone: "green" | "grey" | "amber" | "blue" | "red" } {
  const s = statusOf(f);
  if (isShadow(f)) return { word: "Preview only", tone: "grey" };
  if (s === "open") return { word: "Waiting for you", tone: "amber" };
  if (s === "testing") return { word: "Being tested", tone: "blue" };
  if (s === "measuring") return { word: f.measure_until ? `Being measured, result by ${f.measure_until}` : "Being measured", tone: "blue" };
  if (s === "judged") return { word: f.verdict ? `Result: ${f.verdict.replace(/_/g, " ")}` : "Judged", tone: f.verdict === "worked" ? "green" : "grey" };
  if (s === "dismissed") return { word: "Dismissed", tone: "grey" };
  if (s === "acknowledged") return { word: "Handled", tone: "grey" };
  if (s === "expired") return { word: "No longer applies", tone: "grey" };
  return { word: s.charAt(0).toUpperCase() + s.slice(1), tone: "grey" };
}

type Props = { brand: string; id: string; canManage: boolean; go: (tab: Tab, item?: string) => void; changed?: () => void };

export function ActionPage({ brand, id, canManage, go, changed }: Props) {
  const [f, setF] = useState<Finding | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [rating, setRating] = useState<"good" | "bad" | "">("");
  const [note, setNote] = useState("");
  const agent = useCapabilities()?.includes("brand_agent") ?? false;
  const path = { params: { path: { brand, id } } };

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/findings/{id}", { params: { path: { brand, id } } });
    setError(error ? errorMessage(error) : "");
    if (data) {
      setF(data as Finding);
      setRating(data.feedback);
      setNote(data.feedback_note);
    }
  }, [brand, id]);
  useEffect(() => {
    load();
  }, [load]);

  if (error && !f) return <div className="space-y-3"><Button variant="ghost" onClick={() => go("ads", "actions")}>← Actions</Button><ErrorText>{error}</ErrorText></div>;
  if (!f) return <Loading />;

  async function run(call: () => Promise<{ error?: unknown }>, done: string) {
    setBusy(true);
    const { error } = await call();
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    load();
    changed?.();
  }
  async function approve() {
    if (!f) return;
    if (spendsMore(f.kind) && !(await confirmAction(`${leverWord(f.kind)} on ${f.campaign_name}?`, "This change can spend more. Outflare sends it to the network, measures it and tells you whether it worked. You can undo it at any time.", "Approve", false))) return;
    await run(() => api.POST("/v1/brands/{brand}/ads/findings/{id}/approve", { ...path, body: {} }), "Change sent to the network. Outflare now measures it.");
  }
  async function testFirst() {
    if (!f?.test_first) return;
    if (!(await confirmAction(`Test it first on ${f.campaign_name}?`, f.test_first.note, "Start the test", false))) return;
    await run(() => api.POST("/v1/brands/{brand}/ads/findings/{id}/approve", { ...path, body: { test: true } }), "Test queued. Follow it under Experiments.");
  }
  async function undo() {
    if (!f || !(await confirmAction(`Undo: ${leverWord(f.kind).toLowerCase()} on ${f.campaign_name}?`, "Outflare makes the opposite change on the network.", "Undo", false))) return;
    await run(() => revertFinding(brand, f.id), "Undone. The network shows the change back within a minute.");
  }
  async function rate(next: "good" | "bad", text: string) {
    setRating(next);
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/ads/findings/{id}/feedback", { ...path, body: { rating: next, ...(text.trim() ? { note: text.trim() } : {}) } });
    setBusy(false);
    if (error || !data) {
      setRating(f?.feedback ?? "");
      return toast(errorMessage(error), "error");
    }
    if ("feedback" in data) setF(data as Finding);
    toast(text.trim() ? "Saved. Outflare remembers this for the brand." : "Saved. Add why, and the agent learns more from it.");
  }
  const ask = (prompt: string) => {
    track("ui.ads_action_agent_asked", { kind: f.kind });
    askAgent(prompt);
  };

  const type = typeOf(f);
  const where = standing(f);
  const alert = isAlert(f);
  const title = f.reason || leverWord(f.kind);
  const effect = impactLine(f);
  const saved = f.feedback !== "" && f.feedback === rating && f.feedback_note === note.trim();

  return (
    <div className="space-y-6">
      <div>
        <Button variant="ghost" onClick={() => go("ads", "actions")}>← Actions</Button>
      </div>
      <section aria-label="Action" className="space-y-3 rounded-lg border bg-card p-5 shadow-xs">
        <div className="flex flex-wrap items-center gap-2 text-[13px]">
          <Badge tone={type.tone}>{type.word}</Badge>
          <Status tone={where.tone}>{where.word}</Status>
          <span className="text-muted-foreground">Found {ago(f.created_at)}</span>
        </div>
        <h2 className="text-lg font-semibold">{title}</h2>
        {changeLine(f) && <p className="text-[14px]">{changeLine(f)}</p>}
        <p className="text-[13px] text-muted-foreground">
          {f.campaign_id ? <button type="button" className="text-blue-700 underline-offset-2 hover:underline dark:text-blue-400" onClick={() => go("ads", f.campaign_id)}>{f.campaign_name}</button> : f.campaign_name} · {platformName(f.connector)} · {f.account_name}
        </p>
        <div className="flex flex-wrap gap-2 pt-1">
          {agent && <Button onClick={() => ask("Why are you recommending this? Explain it with the numbers, and tell me what else we could do instead.")}>Ask the agent why</Button>}
          {canManage && isOpen(f) && f.test_first && <Button variant={agent ? "ghost" : "primary"} disabled={busy} onClick={testFirst}>Test it first</Button>}
          {canManage && isOpen(f) && <Button variant={agent || f.test_first ? "ghost" : "primary"} disabled={busy} onClick={approve}>{f.test_first ? "Make the change now" : "Approve"}</Button>}
          {canManage && isOpen(f) && <Button variant="ghost" disabled={busy} onClick={() => run(() => api.POST("/v1/brands/{brand}/ads/findings/{id}/dismiss", path), "Dismissed.")}>Dismiss</Button>}
          {canManage && alert && statusOf(f) === "open" && <Button variant="ghost" disabled={busy} onClick={() => run(() => acknowledgeFinding(brand, f.id), "Marked as handled.")}>Mark as handled</Button>}
          {canManage && isDone(f) && <Button variant="ghost" disabled={busy} onClick={undo}>Undo</Button>}
          {isMeasuring(f) && <Button variant="ghost" onClick={() => go("ads", "experiments")}>See the result</Button>}
          {f.campaign_id && <Button variant="ghost" onClick={() => go("ads", f.campaign_id)}>Open the campaign</Button>}
        </div>
      </section>

      <Card title="Why Outflare recommends this" description="The numbers the daily review looked at. Source: your ad account and Outflare's own record of sign-ups and payments.">
        {evidenceLine(f) && <p className="mb-3 text-[13px]">{evidenceLine(f)}.</p>}
        <Facts of={f.evidence} currency={f.currency} empty="This one came with no numbers. Ask the agent to look into it." />
        {f.window_note && <p className="mt-3 text-[12px] text-muted-foreground">{f.window_note}</p>}
      </Card>

      {!alert && (
        <Card title="What it would change" description={effect ?? undefined}>
          <Facts of={f.proposed} currency={f.currency} empty={`${leverWord(f.kind)} on ${f.campaign_name}.`} />
          {spendsMore(f.kind) && <p className="mt-3 text-[12px] text-muted-foreground">This change can spend more, so it always asks you first.</p>}
          {f.test_first && isOpen(f) && <p className="mt-2 text-[13px]" data-testid="test-first-note"><span className="font-medium">Better as an experiment.</span> {f.test_first.note}</p>}
        </Card>
      )}

      {(f.verdict || f.verdict_note) && (
        <Card title="How it turned out">
          <p className="text-[13px]">{f.verdict ? f.verdict.charAt(0).toUpperCase() + f.verdict.slice(1).replace(/_/g, " ") : "Judged"}{f.verdict_note ? `: ${f.verdict_note}` : ""}</p>
          <div className="mt-3"><Facts of={f.result} currency={f.currency} empty="" /></div>
        </Card>
      )}

      {canManage && (
        <Card title="Was this good advice?" description="Your answer and reason are saved to this brand's memory. Outflare's agents read it before they advise you again.">
          <div className="max-w-xl space-y-3">
            <div className="flex flex-wrap gap-2" role="group" aria-label="Rate this advice">
              <Button variant={rating === "good" ? "primary" : "ghost"} aria-pressed={rating === "good"} disabled={busy} onClick={() => rate("good", note)}>{rating === "good" ? "✓ " : ""}Good advice</Button>
              <Button variant={rating === "bad" ? "primary" : "ghost"} aria-pressed={rating === "bad"} disabled={busy} onClick={() => rate("bad", note)}>{rating === "bad" ? "✓ " : ""}Bad advice</Button>
              {f.feedback_at && <span className="self-center text-[12px] text-muted-foreground">Saved {ago(f.feedback_at)}</span>}
            </div>
            <label className="block space-y-1 text-[13px]">
              <span className="text-muted-foreground">Why? What should Outflare do differently next time?</span>
              <textarea value={note} onChange={(e) => setNote(e.target.value)} maxLength={400} rows={3} placeholder={rating === "bad" ? "For example: never cut the budget on brand campaigns." : "For example: yes, always flag search terms like this."}
                className="w-full rounded-md border border-input bg-transparent px-3 py-2 text-[13px] shadow-xs dark:bg-input/30" />
            </label>
            <div className="flex flex-wrap items-center gap-2">
              <Button disabled={busy || rating === "" || saved} onClick={() => rating && rate(rating, note)}>{saved ? "Saved" : "Save feedback"}</Button>
              {rating === "" && <span className="text-[12px] text-muted-foreground">Pick Good advice or Bad advice first.</span>}
              {agent && <Button variant="ghost" onClick={() => ask("I'd rather do something different here. Suggest two or three other options and what each would cost or save.")}>Discuss other options</Button>}
            </div>
          </div>
        </Card>
      )}
    </div>
  );
}
