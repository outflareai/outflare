// The automation map: every rule and journey in the workspace as one
// read-only picture (GET .../automation-map). What sets each one off sits
// on the left, what it touches on the right, laid out automatically.
// Picking a node lights up everything it sets off and everything that
// sets it off; the flags above say what needs a look.

import { useCallback, useEffect, useMemo, useState } from "react";
import dagre from "@dagrejs/dagre";
import {
  BaseEdge, EdgeLabelRenderer, getSmoothStepPath, Handle, MiniMap, Position, ReactFlow, ReactFlowProvider, useReactFlow,
  type Edge, type EdgeProps, type Node, type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/base.css";
import { CircleAlert, Info, ListChecks, Minus, PencilLine, Plus, Route, Send, Tag, Workflow, X, Zap, type LucideIcon } from "lucide-react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { track } from "@/lib/analytics";
import { cn } from "@/lib/utils";
import { Badge, Button, Empty, ErrorText, field, Loading, PageHeader, type tones } from "../ui";
import { plural } from "../format";
import type { Tab } from "../App";
import { PersonPicker, type Person } from "@/components/person-picker";

type Map = Schemas["AutomationMap"];
type MapNode = Map["nodes"][number];
type MapEdge = Map["edges"][number];
type Flag = Map["flags"][number];

const kindInfo: Record<MapNode["kind"], { label: string; icon: LucideIcon; tab?: Tab }> = {
  event: { label: "Event", icon: Zap },
  field: { label: "Field change", icon: PencilLine },
  rule: { label: "Rule", icon: Workflow, tab: "rules" },
  journey: { label: "Journey", icon: Route, tab: "journeys" },
  smart_list: { label: "People list", icon: ListChecks, tab: "lists" },
  sequence: { label: "Sequence", icon: Send, tab: "sequences" },
  tag: { label: "Tag", icon: Tag },
};

// What a link says; links that only start things say nothing.
const edgeLabel: Partial<Record<MapEdge["kind"], string>> = {
  waits_for: "waits for", checks: "checks", emits: "emits", goal: "goal", exits_on: "exits on", adds_to: "adds", removes_from: "removes",
  enters: "enters", exits: "takes out", enrolls: "enrolls", stops: "stops", untags: "untags",
};

const statusTone: Record<string, keyof typeof tones> = { active: "green", draft: "grey", paused: "amber" };

const W = 220;
const H = 72;

type NodeData = { n: MapNode; selected: boolean; dim: boolean; flagged: boolean; pick: () => void };

// What a person has to do with a card, with ?record=.
const reachedText: Record<NonNullable<MapNode["reached"]>, string> = {
  did: "They did this", member: "In this list", fired: "Fired for them", in: "In it now", entered: "Went through it",
};
type EdgeData = { label?: string; lit: boolean; dim: boolean };

// Layout: left to right, so causes read before effects.
function layout(m: Map, shown: Set<string>, lit: Set<string> | null, flagged: Set<string>, selected: string, pick: (id: string) => void) {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: "LR", nodesep: 20, ranksep: 70, marginx: 16, marginy: 16 });
  g.setDefaultEdgeLabel(() => ({}));
  const nodes: Node<NodeData>[] = [];
  for (const n of m.nodes) {
    if (!shown.has(n.id)) continue;
    g.setNode(n.id, { width: W, height: H });
    nodes.push({ id: n.id, type: "map", position: { x: 0, y: 0 }, data: { n, selected: n.id === selected, dim: !!lit && !lit.has(n.id), flagged: flagged.has(n.id), pick: () => pick(n.id) } });
  }
  const edges: Edge<EdgeData>[] = [];
  for (const e of m.edges) {
    if (!shown.has(e.from) || !shown.has(e.to)) continue;
    g.setEdge(e.from, e.to);
    const on = !!lit && lit.has(e.from) && lit.has(e.to);
    edges.push({ id: `${e.from}>${e.kind}>${e.to}`, source: e.from, target: e.to, type: "map", data: { label: edgeLabel[e.kind], lit: on, dim: !!lit && !on } });
  }
  dagre.layout(g);
  for (const n of nodes) {
    const p = g.node(n.id);
    n.position = { x: p.x - W / 2, y: p.y - H / 2 };
    n.width = W;
    n.height = H;
  }
  return { nodes, edges };
}

// reach is everything a node sets off (down), everything that sets it off
// (up), or both.
function reach(m: Map, from: string, dirs: ("down" | "up")[] = ["down", "up"]): Set<string> {
  const out = new Set([from]);
  for (const dir of dirs) {
    const todo = [from];
    const seen = new Set([from]);
    while (todo.length) {
      const id = todo.pop()!;
      for (const e of m.edges) {
        const [a, b] = dir === "down" ? [e.from, e.to] : [e.to, e.from];
        if (a === id && !seen.has(b)) {
          seen.add(b);
          out.add(b);
          todo.push(b);
        }
      }
    }
  }
  return out;
}

function MapCard({ data }: NodeProps<Node<NodeData>>) {
  const { n } = data;
  const info = kindInfo[n.kind];
  const Icon = info.icon;
  return (
    // Cards take clicks themselves: the map has nothing selectable, so
    // React Flow would hand them to the pane.
    <div className="pointer-events-auto h-full w-full">
      <Handle type="target" position={Position.Left} isConnectable={false} className="!size-1 !min-w-0 !border-0 !bg-transparent" />
      <button type="button" onClick={data.pick} aria-label={`${info.label}: ${n.label}`}
        className={cn("flex h-full w-full flex-col gap-0.5 overflow-hidden rounded-lg border bg-card px-3 py-2 text-left shadow-sm transition-colors hover:border-ring",
          (n.kind === "event" || n.kind === "field") && "rounded-full px-4",
          data.selected && "border-ring ring-2 ring-ring/40",
          data.flagged && !data.selected && "border-amber-500/70",
          data.dim && "opacity-30")}>
        <span className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <Icon className="size-3.5 shrink-0" />
          <span className="truncate">{info.label}</span>
          {n.status && (n.kind === "rule" || n.kind === "journey" || n.kind === "sequence") && n.status !== "active" && <Badge tone={statusTone[n.status] ?? "grey"}>{n.status}</Badge>}
          {data.flagged && <CircleAlert className="ml-auto size-3.5 shrink-0 text-amber-600 dark:text-amber-400" aria-label="Flagged" />}
          {n.reached && <span className={cn("shrink-0 rounded-full bg-violet-500/15 px-1.5 text-[10px] font-medium text-violet-700 dark:text-violet-300", !data.flagged && "ml-auto")}>{reachedText[n.reached]}</span>}
        </span>
        <span className={cn("truncate text-[13px] font-medium", (n.kind === "event" || n.kind === "field") && "font-mono text-[12px]")}>{n.label}</span>
        <span className="truncate text-[11px] text-muted-foreground tabular-nums">
          {n.volume != null ? volumeText(n) : n.detail}
        </span>
      </button>
      <Handle type="source" position={Position.Right} isConnectable={false} className="!size-1 !min-w-0 !border-0 !bg-transparent" />
    </div>
  );
}

const volumeText = (n: MapNode) =>
  n.kind === "journey" ? `${plural(n.volume ?? 0, "person", "people")} entered in 7 days` :
  n.kind === "rule" ? `Fired ${plural(n.volume ?? 0, "time")} in 7 days` :
  `${plural(n.volume ?? 0, "time")} in 7 days`;

function MapLink({ id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, data }: EdgeProps<Edge<EdgeData>>) {
  const [path, x, y] = getSmoothStepPath({ sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, borderRadius: 10 });
  return (
    <>
      <BaseEdge id={id} path={path} style={{ ...(data?.lit ? { stroke: "#7c3aed", strokeWidth: 2 } : {}), ...(data?.dim ? { opacity: 0.15 } : {}) }} />
      {data?.label && !data.dim && (
        <EdgeLabelRenderer>
          <div className="nodrag nopan pointer-events-none absolute rounded bg-muted px-1.5 text-[10px] font-medium text-muted-foreground" style={{ transform: `translate(-50%, -50%) translate(${x}px, ${y}px)` }}>
            {data.label}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  );
}

const nodeTypes = { map: MapCard };
const edgeTypes = { map: MapLink };

function Controls({ shape }: { shape: string }) {
  const rf = useReactFlow();
  useEffect(() => {
    const t = setTimeout(() => rf.fitView({ maxZoom: 1, minZoom: 0.2, padding: 0.06 }), 0);
    return () => clearTimeout(t);
  }, [rf, shape]);
  const btn = "flex size-7 items-center justify-center rounded-md border bg-background text-muted-foreground shadow-xs hover:text-foreground";
  return (
    <div className="absolute right-2 bottom-2 z-10 flex gap-1">
      <button type="button" className={btn} onClick={() => rf.zoomOut()} aria-label="Zoom out"><Minus className="size-3.5" /></button>
      <button type="button" className={btn} onClick={() => rf.zoomIn()} aria-label="Zoom in"><Plus className="size-3.5" /></button>
      <button type="button" className={cn(btn, "w-auto px-2 text-[11px]")} onClick={() => rf.fitView({ maxZoom: 1, padding: 0.06 })}>Fit</button>
    </div>
  );
}

const shows = { all: "Everything", live: "Only what's live" } as const;
type Show = keyof typeof shows;

export function AutomationMap({ ws, go }: { ws: string; go: (tab: Tab, item?: string) => void }) {
  const [m, setMap] = useState<Map | null>(null);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState("");
  // A flag's nodes, lit up together.
  const [focus, setFocus] = useState<Set<string> | null>(null);
  const [show, setShow] = useState<Show>("all");
  const [q, setQ] = useState("");
  // A sequence, list or tag: only what leads to it.
  const [touches, setTouches] = useState("");
  // A person: what they reached, and what that sets off next.
  const [person, setPerson] = useState<Person | null>(null);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/automation-map", { params: { path: { ws }, query: person ? { record: person.id } : {} } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setMap(data);
  }, [ws, person]);
  useEffect(() => {
    load();
  }, [load]);

  const flagged = useMemo(() => new Set((m?.flags ?? []).flatMap((f) => f.nodes)), [m]);
  // What's on the map: live only hides drafts and paused rules and
  // journeys, then anything left with no link to what's shown.
  const shown = useMemo(() => {
    if (!m) return new Set<string>();
    let ids = new Set(m.nodes.filter((n) => show === "all" || !((n.kind === "rule" || n.kind === "journey") && n.status !== "active")).map((n) => n.id));
    if (show === "live") {
      const linked = new Set(m.edges.filter((e) => ids.has(e.from) && ids.has(e.to)).flatMap((e) => [e.from, e.to]));
      ids = new Set([...ids].filter((id) => linked.has(id) || m.nodes.find((n) => n.id === id)?.kind === "rule" || m.nodes.find((n) => n.id === id)?.kind === "journey"));
    }
    return ids;
  }, [m, show]);
  const lit = useMemo(() => {
    if (!m) return null;
    if (focus) return focus;
    if (selected) return reach(m, selected);
    if (touches) return reach(m, touches, ["up"]);
    if (person) {
      const reached = m.nodes.filter((n) => n.reached);
      return new Set(reached.flatMap((n) => [...reach(m, n.id, ["down"])]));
    }
    const term = q.trim().toLowerCase();
    if (!term) return null;
    const hits = m.nodes.filter((n) => n.label.toLowerCase().includes(term));
    return hits.length ? new Set(hits.flatMap((n) => [...reach(m, n.id)])) : new Set<string>();
  }, [m, focus, selected, touches, person, q]);
  const pick = useCallback((id: string) => {
    setFocus(null);
    setSelected((cur) => {
      if (cur === id) return "";
      const kind = m?.nodes.find((n) => n.id === id)?.kind;
      track("ui.automation_map_node_picked", { kind });
      return id;
    });
  }, [m]);
  const graph = useMemo(() => (m ? layout(m, shown, lit, flagged, selected, pick) : { nodes: [], edges: [] }), [m, shown, lit, flagged, selected, pick]);

  if (!m) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const node = m.nodes.find((n) => n.id === selected);
  const automations = m.nodes.filter((n) => n.kind === "rule" || n.kind === "journey").length;
  const targets = m.nodes.filter((n) => n.kind === "sequence" || n.kind === "smart_list" || n.kind === "tag").sort((a, b) => a.kind.localeCompare(b.kind) || a.label.localeCompare(b.label));
  return (
    <div className="space-y-4">
      <PageHeader
        title="Automation map"
        description="Every rule and journey in one picture: what sets each one off, what it does next, and how often in the last 7 days. Pick anything to light up what it sets off and what sets it off."
      />
      <ErrorText>{error}</ErrorText>
      {automations === 0 ? (
        <Empty title="No automations yet" action={<Button onClick={() => go("playbooks")}>Browse playbooks</Button>}>
          Rules and journeys show here once you have some. A playbook can set a whole one up for you, as drafts.
        </Empty>
      ) : (
        <>
          {m.flags.length > 0 && (
            <section aria-label="What needs a look" className="space-y-1.5 rounded-lg border p-3 text-[13px]">
              <h2 className="font-medium">What needs a look</h2>
              <ul className="space-y-1">
                {m.flags.map((f, i) => <FlagLine key={i} f={f} active={!!focus && f.nodes.every((id) => focus.has(id)) && focus.size === f.nodes.length} onShow={() => {
                  track("ui.automation_map_flag_opened", { flag: f.kind });
                  setSelected("");
                  setFocus(new Set(f.nodes));
                }} />)}
              </ul>
            </section>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <select aria-label="Show" className={field} value={show} onChange={(e) => {
              setShow(e.target.value as Show);
              track("ui.automation_map_filtered", { show: e.target.value });
            }}>
              {Object.entries(shows).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
            </select>
            <input aria-label="Find" className={cn(field, "w-56")} placeholder="Find an event, rule or journey" value={q} onChange={(e) => { setQ(e.target.value); setSelected(""); setFocus(null); }} />
            {targets.length > 0 && (
              <select aria-label="Touches" className={field} value={touches} onChange={(e) => {
                setTouches(e.target.value);
                setSelected("");
                setFocus(null);
                if (e.target.value) track("ui.automation_map_filtered", { touches: m.nodes.find((n) => n.id === e.target.value)?.kind });
              }}>
                <option value="">Touches anything</option>
                {targets.map((n) => <option key={n.id} value={n.id}>{kindInfo[n.kind].label}: {n.label}</option>)}
              </select>
            )}
            <PersonPicker ws={ws} person={person} startOpen={false} onPick={(p) => {
              setPerson(p);
              setSelected("");
              setFocus(null);
              setTouches("");
              track("ui.automation_map_person_picked");
            }} />
            {(selected || focus || touches || person) && <Button variant="ghost" onClick={() => { setSelected(""); setFocus(null); setTouches(""); setPerson(null); }}><X className="size-4" />Show everything</Button>}
            <span className="ml-auto text-[12px] text-muted-foreground">{plural(m.nodes.filter((n) => n.kind === "rule").length, "rule")} · {plural(m.nodes.filter((n) => n.kind === "journey").length, "journey")}</span>
          </div>
          {person && !selected && !focus && (
            <p className="text-[13px] text-muted-foreground">
              {m.nodes.some((n) => n.reached)
                ? <>Lit up: what {person.name || person.email || "they"} did, the lists they're in, the rules that fired for them and the journeys they went through, then everything those set off next.</>
                : <>{person.name || person.email || "This person"} hasn't done anything these automations listen to yet.</>}
            </p>
          )}
          {node && (
            <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-card px-3 py-2 text-[13px]">
              <span className="text-muted-foreground">{kindInfo[node.kind].label}</span>
              <span className="font-medium">{node.label}</span>
              {node.detail && <span className="text-muted-foreground">· {node.detail}</span>}
              {kindInfo[node.kind].tab && node.ref_id && <Button className="ml-auto" onClick={() => go(kindInfo[node.kind].tab!, node.ref_id)}>Open</Button>}
            </div>
          )}
          <div className="journey-flow relative h-[70vh] min-h-[420px] w-full overflow-hidden rounded-lg border bg-muted/30">
            <ReactFlowProvider>
              <ReactFlow
                nodes={graph.nodes} edges={graph.edges} nodeTypes={nodeTypes} edgeTypes={edgeTypes}
                nodesDraggable={false} nodesConnectable={false} elementsSelectable={false} nodesFocusable={false} edgesFocusable={false}
                zoomOnScroll={false} preventScrolling={false} zoomOnDoubleClick={false} panOnDrag minZoom={0.2} maxZoom={1.5}
                fitView fitViewOptions={{ maxZoom: 1, minZoom: 0.2, padding: 0.06 }}
              >
                <Controls shape={[...shown].join("|")} />
                {graph.nodes.length > 12 && (
                  <MiniMap pannable zoomable position="bottom-left" className="!m-2 hidden !rounded-md !border !bg-background sm:block" style={{ width: 160, height: 110 }}
                    maskColor="color-mix(in oklch, var(--muted) 70%, transparent)"
                    nodeColor={(n) => (n.data as NodeData).flagged ? "#d97706" : (n.data as NodeData).n.kind === "journey" ? "#2a78d6" : "#a1a1aa"} />
                )}
              </ReactFlow>
            </ReactFlowProvider>
          </div>
        </>
      )}
    </div>
  );
}

function FlagLine({ f, active, onShow }: { f: Flag; active: boolean; onShow: () => void }) {
  const Icon = f.severity === "warning" ? CircleAlert : Info;
  return (
    <li className="flex items-start gap-2">
      <Icon className={cn("mt-0.5 size-4 shrink-0", f.severity === "warning" ? "text-amber-600 dark:text-amber-400" : "text-muted-foreground")} />
      <span className="min-w-0 flex-1">{f.message}</span>
      <button type="button" className={cn("shrink-0 text-[12px] underline underline-offset-4", active && "font-medium")} onClick={onShow}>Show</button>
    </li>
  );
}
