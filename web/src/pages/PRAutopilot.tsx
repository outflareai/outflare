// The reactive PR autopilot (Outflare Cloud): a mission that, each morning,
// reads the news on a brand's experts' topics, starts reactive stories with
// a comment for the expert to approve, and pitches the journalists covering
// each one once a person has approved. The server lists the pr_autopilot
// capability where it runs; its routes are the hosted extension's, so they
// are called without the typed client. It runs on the workspace's own AI
// model and accounts, and a person approves the mission, every quote, every
// release and every pitch.

import { useCallback, useEffect, useState } from "react";
import type { PRExpert } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Autopilot = { id: string; mission_id: string; mission_status: string; brand_id: string; max_stories_per_week: number; created_at: string };
type Failure = { error: { code: string; message: string } };

// call reaches the autopilot's routes, with the same session as the typed
// client and errors in the engine's shape.
async function call<T>(method: string, path: string, body?: unknown): Promise<{ data?: T; error?: Failure }> {
  try {
    const res = await fetch(path, { method, credentials: "same-origin", headers: body ? { "Content-Type": "application/json" } : {}, body: body ? JSON.stringify(body) : undefined });
    const json = await res.json().catch(() => ({}));
    return res.ok ? { data: json as T } : { error: json as Failure };
  } catch {
    return { error: { error: { code: "network_error", message: "Could not reach Outflare. Check your connection and try again." } } };
  }
}

const statusLabel: Record<string, string> = {
  draft: "Waiting for your approval", pending_approval: "Waiting for your approval", running: "Running", paused: "Paused", completed: "Done", stopped: "Stopped", failed: "Failed",
};
const statusTone: Record<string, keyof typeof tones> = { draft: "amber", pending_approval: "amber", running: "green", paused: "grey", completed: "grey", stopped: "grey", failed: "red" };
const open = (s: string) => !["completed", "stopped", "failed"].includes(s);

export function PRAutopilot({ ws, canManage, go }: Props) {
  const { active, byId, query } = useBrand(ws);
  const base = `/v1/workspaces/${ws}/cloud/pr-autopilot`;
  const [list, setList] = useState<Autopilot[] | null>(null);
  const [brandID, setBrandID] = useState("");
  const [perWeek, setPerWeek] = useState(3);
  const [experts, setExperts] = useState<PRExpert[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const forBrand = query.brand ? active.find((b) => b.handle === query.brand)?.id ?? "" : brandID || active[0]?.id || "";

  const load = useCallback(async () => {
    const { data, error } = await call<{ data: Autopilot[] }>("GET", base);
    if (error || !data) return setError(error?.error.message ?? "Could not load the autopilots.");
    setError("");
    setList(data.data);
  }, [base]);
  useEffect(() => {
    load();
  }, [load]);
  // The brand's experts: the autopilot drafts comments for them.
  useEffect(() => {
    if (!forBrand) return;
    setExperts(null);
    api.GET("/v1/workspaces/{ws}/pr-experts", { params: { path: { ws }, query: { brand: forBrand } } }).then(({ data }) => setExperts(data?.data ?? []));
  }, [ws, forBrand]);

  const start = async () => {
    setBusy(true);
    const { data, error } = await call<Autopilot>("POST", base, { brand_id: forBrand, max_stories_per_week: perWeek });
    setBusy(false);
    if (error || !data) return setError(error?.error.message ?? errorMessage(error));
    setError("");
    toast("Autopilot set up. Approve its mission to start it.");
    go("missions", data.mission_id);
  };

  if (!list) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const running = new Set(list.filter((a) => open(a.mission_status)).map((a) => a.brand_id));
  const shown = query.brand ? list.filter((a) => a.brand_id === forBrand) : list;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Reactive PR autopilot"
        description="Every morning it reads the news on your experts' topics and, when a story is worth it, drafts your expert's comment and pitches the journalists covering it. You approve each quote, release and pitch."
      />
      {error && <ErrorText>{error}</ErrorText>}

      {canManage && active.length > 0 && (
        <Card title="Set one up" description="One autopilot per brand. It runs as a mission on your own AI model, and starts once you approve the mission.">
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              {!query.brand && active.length > 1 && (
                <select className={field} aria-label="Brand" value={forBrand} onChange={(e) => setBrandID(e.target.value)}>
                  {active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
                </select>
              )}
              <label className="flex items-center gap-2 text-[13px]">
                At most
                <input className={`${field} w-16`} type="number" min={1} max={20} aria-label="Stories a week" value={perWeek}
                  onChange={(e) => setPerWeek(Math.max(1, Math.min(20, Number(e.target.value) || 1)))} />
                stories a week
              </label>
              <Button disabled={busy || !forBrand || !experts?.length || running.has(forBrand)} onClick={start}>Set up the autopilot</Button>
            </div>
            {experts && experts.length === 0 && (
              <p className="text-[12px] text-muted-foreground">
                Add an expert for {byId(forBrand)?.name ?? "this brand"} first: the autopilot drafts comments for them to approve.{" "}
                <button type="button" className="underline" onClick={() => go("stories")}>Stories, Experts</button>
              </p>
            )}
            {running.has(forBrand) && <p className="text-[12px] text-muted-foreground">{byId(forBrand)?.name ?? "This brand"} has an autopilot already. Stop its mission to set up a new one.</p>}
            <p className="text-[12px] text-muted-foreground">
              It needs a news search for each expert topic (News watch), journalists from those searches, and a pitch sequence: 1 or 2 steps that use {"{{custom.opener}}"} and stop on any reply.
            </p>
          </div>
        </Card>
      )}

      {shown.length === 0 ? (
        <Empty title="No autopilot yet">Set one up for a brand with an expert, then approve its mission.</Empty>
      ) : (
        <Card title="Autopilots">
          <ul className="divide-y">
            {shown.map((a) => (
              <li key={a.id} className="flex flex-wrap items-center justify-between gap-2 py-2.5 text-[13px]">
                <div className="min-w-0 space-y-0.5">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{byId(a.brand_id)?.name ?? "A brand"}</span>
                    <Badge tone={statusTone[a.mission_status] ?? "grey"}>{statusLabel[a.mission_status] ?? a.mission_status}</Badge>
                  </div>
                  <div className="text-[12px] text-muted-foreground">At most {a.max_stories_per_week} stories a week · set up {ago(a.created_at)}</div>
                </div>
                <div className="flex gap-1">
                  <Button variant="ghost" onClick={() => go("missions", a.mission_id)}>{a.mission_status === "draft" || a.mission_status === "pending_approval" ? "Review and approve" : "Open mission"}</Button>
                  <Button variant="ghost" onClick={() => go("stories")}>Stories</Button>
                </div>
              </li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}
