import { useCallback, useEffect, useState } from "react";
import type { Schemas, paths } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, field, Loading, Logo, Stat, Stats } from "../ui";
import { QueryChart } from "@/components/query-chart";
import { track } from "@/lib/analytics";
import { Bar, Leaderboard } from "./ai-visibility/overview";
import { Section, pct, score, th } from "./ai-visibility/shared";

// The page behind a brand's AI visibility share link (/share/ai/<token>):
// the overview, the trend against competitors and each question's
// results for the last 28 days, read-only, for anyone who has the link.
// A link with a password asks for it first and keeps the key it gets, in
// this tab only.

type Shared = paths["/v1/shared/ai-visibility/{token}"]["post"]["responses"]["200"]["content"]["application/json"];

export function SharedAIVisibility({ token }: { token: string }) {
  const [d, setD] = useState<Shared | null>(null);
  const [needsPassword, setNeedsPassword] = useState(false);
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const open = useCallback(
    async (body: Schemas["SharedDashboardOpen"]) => {
      setBusy(true);
      const { data, error, response } = await api.POST("/v1/shared/ai-visibility/{token}", { params: { path: { token } }, body });
      setBusy(false);
      if (data) {
        setD(data);
        setNeedsPassword(false);
        setError("");
        document.title = `AI visibility · ${data.brand}`;
        return;
      }
      const code = (error as { error?: { code?: string } } | undefined)?.error?.code;
      if (code === "password_required") {
        setNeedsPassword(true);
        return;
      }
      if (code === "wrong_password") {
        track("ui.shared_ai_visibility_password_wrong");
        setError("That isn't the password.");
        return;
      }
      setError(response?.status === 404 ? "This link is invalid, expired or was taken back." : errorMessage(error));
    },
    [token],
  );
  useEffect(() => {
    open({});
  }, [open]);

  if (!d && needsPassword) {
    return (
      <Frame>
        <form
          className="mx-auto mt-16 max-w-sm space-y-3 rounded-lg border bg-card p-6"
          onSubmit={(e) => {
            e.preventDefault();
            open({ password });
          }}
        >
          <h1 className="text-[15px] font-medium">This page asks for its password</h1>
          <p className="text-[13px] text-muted-foreground">Whoever sent you the link has it.</p>
          <input className={`${field} w-full`} type="password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} aria-label="Password" />
          {error && <ErrorText>{error}</ErrorText>}
          <Button type="submit" className="w-full" disabled={!password || busy}>
            Open
          </Button>
        </form>
      </Frame>
    );
  }
  if (!d && error) {
    return (
      <Frame>
        <div className="mx-auto mt-16 max-w-sm rounded-lg border bg-card p-6 text-[13px]">
          <ErrorText>{error}</ErrorText>
        </div>
      </Frame>
    );
  }
  if (!d) {
    return (
      <Frame>
        <Loading />
      </Frame>
    );
  }
  // The overview of the question set with the most answers.
  const v = [...d.data].sort((a, b) => b.answers - a.answers)[0];
  const board = v ? [{ ...v.brand, you: true }, ...v.competitors.map((c) => ({ ...c, you: false }))].sort((a, b) => (b.score ?? 0) - (a.score ?? 0)) : [];
  const lines = d.series.filter((s) => s.answers > 0);
  const label = (s: Shared["series"][number]) => (s.you ? `${s.label} (you)` : s.label);
  const questions = [...d.questions].filter((q) => q.answers > 0).sort((a, b) => (b.demand ?? 0) - (a.demand ?? 0) || b.answers - a.answers);
  return (
    <Frame brand={d.brand}>
      <div className="mb-4">
        <h1 className="text-[18px] font-semibold">AI visibility</h1>
        <p className="text-[13px] text-muted-foreground">How AI assistants answer the questions {d.brand}'s buyers ask, over the last {d.days} days.</p>
      </div>
      {!v || v.answers === 0 ? (
        <p className="text-[13px] text-muted-foreground">No answers in this period.</p>
      ) : (
        <div className="space-y-6">
          <Stats cols={4}>
            <Stat label="AI visibility score" value={score(v.brand.score)} sub={`${v.answers} answers`} />
            <Stat label="Rank" value={`#${board.findIndex((e) => e.you) + 1}`} sub={`of ${board.length} brands`} />
            <Stat label="Share of voice" value={pct(v.brand.share_of_voice)} sub="of every brand named" />
            <Stat label="Recommended" value={pct(v.brand.recommend_rate)} sub={`named in ${pct(v.brand.mention_rate)}`} />
          </Stats>
          <div className="grid gap-6 xl:grid-cols-[3fr_2fr]">
            <Section title="Score over time" note="Weekly, 0 to 100, against the competitors the answers name.">
              {lines.length === 0 || d.weeks.length < 2 ? (
                <p className="text-muted-foreground">The line starts after a second week of answers.</p>
              ) : (
                <QueryChart
                  columns={[{ name: "week", type: "text" }, ...lines.map((s) => ({ name: label(s), type: "float8" as const }))]}
                  rows={d.weeks.map((w, i) => [w, ...lines.map((s) => { const x = s.weeks[i]?.score; return x === null || x === undefined ? null : Math.round(x); })])}
                  spec={{ type: "line", x: "week", y: lines.map(label) }}
                />
              )}
            </Section>
            <Section title="Who wins AI answers">
              <Leaderboard rows={board.slice(0, 7)} />
            </Section>
          </div>
          <Section title="Question by question" note="Whether the answers name and recommend the brand, and the change against the period before.">
            <table className="w-full">
              <thead><tr><th className={th}>Question</th><th className={th}>Answers</th><th className={th}>Names you</th><th className={th}>Recommends you</th><th className={th}>Score</th><th className={th}>Change</th></tr></thead>
              <tbody className="divide-y">
                {questions.map((q) => (
                  <tr key={q.id}>
                    <td className="py-1.5 pr-3">{q.text}</td>
                    <td className="pr-3 tabular-nums">{q.answers}</td>
                    <td className="pr-3"><Bar value={q.mention_rate} /></td>
                    <td className="pr-3"><Bar value={q.recommend_rate} tone="bg-green-600" /></td>
                    <td className="pr-3 tabular-nums">{score(q.score)}</td>
                    <td className={`tabular-nums ${q.change === null ? "text-muted-foreground" : q.change > 0 ? "text-green-700" : q.change < 0 ? "text-red-700" : ""}`}>{q.change === null ? "–" : `${q.change > 0 ? "+" : ""}${Math.round(q.change)}`}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Section>
        </div>
      )}
    </Frame>
  );
}

function Frame({ brand, children }: { brand?: string; children: React.ReactNode }) {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="flex items-center justify-between border-b px-6 py-3 text-[13px]">
        <span className="flex items-center gap-3">
          <Logo />
          {brand && <span className="text-muted-foreground">{brand}</span>}
        </span>
        <span className="text-muted-foreground">Shared read-only</span>
      </header>
      <main className="mx-auto max-w-7xl px-6 py-6">{children}</main>
    </div>
  );
}
