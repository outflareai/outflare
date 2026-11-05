// Presence opportunities: the places worth the team's effort, highest
// leverage first. Leverage is SEO (the site's domain rating and traffic)
// and AI answers (how often they cite the site) together, plus how many
// competitors are there. Picking one contacts nobody.
import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Badge, Button, ErrorText, field, toast } from "../../ui";
import { label } from "./client";

type Opportunity = components["schemas"]["PlacementOpportunity"];
type State = "open" | "picked" | "dismissed" | "won" | "all";
type Reason = "competitor_gap" | "ai_cited" | "unlinked_mention" | "link_removed";

const reasons: Record<Reason, string> = {
  competitor_gap: "Competitors are here, you aren't",
  ai_cited: "AI answers cite it",
  unlinked_mention: "Names you without a link",
  link_removed: "Your link was removed",
};
const actions: Record<string, string> = { pitch: "Pitch to be included", request_link: "Ask for a link", reclaim_link: "Win the link back" };
const parts: [string, string][] = [
  ["seo", "SEO"],
  ["ai", "AI answers"],
  ["competitors", "Competitors"],
  ["fit", "Page type"],
  ["relationship", "Relationship"],
];

export function Opportunities({ brand, manage, myId }: { brand: string; manage: boolean; myId: string }) {
  const [rows, setRows] = useState<Opportunity[]>([]),
    [total, setTotal] = useState(0),
    [state, setState] = useState<State>("open"),
    [reason, setReason] = useState<Reason | "">(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [reload, setReload] = useState(0);
  useEffect(() => {
    let active = true;
    api
      .GET("/v1/brands/{brand}/coverage/opportunities", {
        params: { path: { brand }, query: { state, limit: 100, ...(reason ? { reason } : {}) } },
      })
      .then(({ data, error }) => {
        if (!active) return;
        if (error) return setError(errorMessage(error));
        setRows(data.data);
        setTotal(data.total);
      });
    return () => {
      active = false;
    };
  }, [brand, state, reason, reload]);
  const act = async (fn: () => Promise<{ error?: unknown }>, done: string) => {
    setBusy(true);
    setError("");
    const { error } = await fn();
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setReload((n) => n + 1);
    toast(done);
  };
  const update = (id: string, body: { state?: "open" | "picked" | "dismissed" | "won"; owner_id?: string | null }, done: string) =>
    act(() => api.PATCH("/v1/brands/{brand}/coverage/opportunities/{id}", { params: { path: { brand, id } }, body }), done);
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3 text-sm">
        <div className="mr-auto">
          <h2 className="font-medium">Opportunities · {total}</h2>
          <p className="text-muted-foreground">
            Ranked by leverage: SEO and AI answers together, plus how many competitors are there. Updated every day and after each competitor scan.
          </p>
        </div>
        <select className={`${field} !w-auto`} aria-label="State" value={state} onChange={(e) => setState(e.target.value as State)}>
          <option value="open">Open</option>
          <option value="picked">Picked</option>
          <option value="won">Won</option>
          <option value="dismissed">Dismissed</option>
          <option value="all">All</option>
        </select>
        <select className={`${field} !w-auto`} aria-label="Reason" value={reason} onChange={(e) => setReason(e.target.value as Reason | "")}>
          <option value="">Every reason</option>
          {Object.entries(reasons).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </select>
        {manage && (
          <Button
            variant="ghost"
            disabled={busy}
            onClick={() =>
              act(() => api.POST("/v1/brands/{brand}/coverage/opportunities/refresh", { params: { path: { brand } } }), "Opportunities updated")
            }
          >
            Refresh
          </Button>
        )}
      </div>
      <ErrorText>{error}</ErrorText>
      <div className="divide-y rounded-xl border">
        {rows.map((o) => {
          const points = (o.factors.points ?? {}) as Record<string, number>;
          return (
            <div key={o.id} className="flex flex-wrap items-start gap-4 p-3 text-sm">
              <div className="w-14 shrink-0 text-center">
                <div className="text-lg font-semibold tabular-nums">{Math.round(o.score)}</div>
                <div className="h-1 rounded bg-muted">
                  <div className="h-1 rounded bg-brand" style={{ width: `${o.score}%` }} />
                </div>
              </div>
              <div className="min-w-0 flex-1 space-y-1">
                <a className="block truncate text-brand underline" href={o.url} target="_blank" rel="noreferrer noopener">
                  {o.title || o.url}
                </a>
                <p className="text-muted-foreground">
                  {o.host}
                  {o.kind ? ` · ${label(o.kind)}` : ""} · {reasons[o.reason]}
                  {o.evidence.likely_paid ? " · competitors likely paid" : ""}
                </p>
                <p className="flex flex-wrap gap-1">
                  <Badge tone="blue">{actions[o.action]}</Badge>
                  {parts
                    .filter(([k]) => points[k])
                    .map(([k, name]) => (
                      <Badge key={k}>
                        {name} +{points[k]}
                      </Badge>
                    ))}
                  {o.state !== "open" && <Badge tone={o.state === "won" ? "green" : "grey"}>{label(o.state)}</Badge>}
                </p>
              </div>
              {manage && (
                <div className="flex gap-1">
                  {o.state !== "picked" && o.state !== "won" && (
                    <Button variant="ghost" disabled={busy} onClick={() => update(o.id, { state: "picked", owner_id: myId }, "Picked: it's yours")}>
                      Pick
                    </Button>
                  )}
                  {o.state !== "won" && (
                    <Button variant="ghost" disabled={busy} onClick={() => update(o.id, { state: "won" }, "Marked won")}>
                      Won
                    </Button>
                  )}
                  {o.state === "open" ? (
                    <Button variant="ghost" disabled={busy} onClick={() => update(o.id, { state: "dismissed" }, "Dismissed")}>
                      Dismiss
                    </Button>
                  ) : (
                    <Button variant="ghost" disabled={busy} onClick={() => update(o.id, { state: "open" }, "Reopened")}>
                      Reopen
                    </Button>
                  )}
                </div>
              )}
            </div>
          );
        })}
        {rows.length === 0 && (
          <p className="p-4 text-center text-sm text-muted-foreground">
            Nothing here yet. Opportunities come from where competitors appear and from your placements.
          </p>
        )}
      </div>
    </div>
  );
}
