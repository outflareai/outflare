// The brand agent (Outflare Cloud): one agent per brand that works toward
// the team's objectives. Its routes are the hosted extension's, so they
// are called without the typed client. The link and PR pages give the
// brand's agent an objective instead of starting an autopilot of their own.

export type AgentObjective = {
  id: string;
  kind: string;
  title: string;
  target: Record<string, unknown>;
  status: string;
  notes: string;
  created_at: string;
  current: number | null;
  target_value: number | null;
  progress: number | null;
  measured_at: string | null;
  measure_error: string;
};
export type BrandAgent = { id: string; brand_id: string; status: string; objectives: AgentObjective[] };
type Failure = { error: { code: string; message: string } };

async function call<T>(method: string, path: string, body?: unknown): Promise<{ data?: T; error?: Failure }> {
  try {
    const res = await fetch(path, { method, credentials: "same-origin", headers: body ? { "Content-Type": "application/json" } : {}, body: body ? JSON.stringify(body) : undefined });
    const json = await res.json().catch(() => ({}));
    return res.ok ? { data: json as T } : { error: json as Failure };
  } catch {
    return { error: { error: { code: "network_error", message: "Could not reach Outflare. Check your connection and try again." } } };
  }
}

/** The workspace's brand agents (every brand), stopped ones left out. */
export async function listBrandAgents(ws: string): Promise<{ data?: BrandAgent[]; error?: Failure }> {
  const r = await call<{ data: BrandAgent[] }>("GET", `/v1/workspaces/${ws}/cloud/brand-agents`);
  return r.data ? { data: r.data.data.filter((a) => a.status !== "stopped") } : { error: r.error };
}

/** Where a brand's agent is managed: its objectives tab. */
export const brandAgentURL = (ws: string, brandID: string, tab = "objectives") => `/brand-agent?workspace=${ws}&brand=${brandID}&tab=${tab}`;

/**
 * Gives the brand's agent an objective, setting the agent up first when the
 * brand has none (which takes the tokens.manage scope: the agent gets a
 * token of its own). Returns the agent and the objective.
 */
export async function addBrandObjective(
  ws: string,
  brandID: string,
  objective: { kind: string; title: string; target?: Record<string, unknown>; notes?: string },
): Promise<{ data?: { agent: BrandAgent; objective: AgentObjective }; error?: Failure }> {
  const agents = await listBrandAgents(ws);
  if (agents.error) return { error: agents.error };
  const agent = agents.data?.find((a) => a.brand_id === brandID);
  if (!agent) {
    const made = await call<BrandAgent>("POST", `/v1/workspaces/${ws}/cloud/brand-agents`, { brand_id: brandID, objectives: [objective] });
    if (made.error || !made.data) return { error: made.error };
    return { data: { agent: made.data, objective: made.data.objectives[0] } };
  }
  const added = await call<AgentObjective>("POST", `/v1/workspaces/${ws}/cloud/brand-agents/${agent.id}/objectives`, objective);
  if (added.error || !added.data) return { error: added.error };
  return { data: { agent, objective: added.data } };
}
