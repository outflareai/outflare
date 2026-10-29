// The journey flow: the trigger card at the top, steps running down,
// branches side by side, and End where a path ends. The layout is
// automatic (dagre, top to bottom): nobody drags boxes. When the flow can
// be edited, a "+" on every link opens the step picker for that place.

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import dagre from "@dagrejs/dagre";
import {
  BaseEdge, EdgeLabelRenderer, getSmoothStepPath, Handle, MiniMap, Position, ReactFlow, ReactFlowProvider, useReactFlow,
  type Edge, type EdgeProps, type Node, type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/base.css";
import { CircleAlert, Clock, GitBranch, LogOut, Mail, Minus, Plus, Puzzle, TriangleAlert, Zap, type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { errorLink, kindOf, linkOf, outputLabel, outputs, type Problem, type Slot, type Step, type StepKind } from "./model";
import type { TestMark, TestMarks } from "./test-mode";

const W = 260;
const H = { trigger: 128, step: 88, end: 30 };

const kindIcon: Record<string, LucideIcon> = { email: Mail, wait: Clock, branch: GitBranch, exit: LogOut };
export const iconFor = (kind: string) => kindIcon[kind] ?? Puzzle;

export type StepCard = {
  label: string;
  // A short line under the label: the template, how long, what it checks.
  detail?: string;
  // Live numbers: outcome counts, and how many people are on it now.
  numbers: { text: string; tone?: "red" | "amber" }[];
  here: number;
  problems: Problem[];
};

type FlowProps = {
  steps: Step[];
  kinds: StepKind[];
  trigger: ReactNode;
  card: (s: Step) => StepCard;
  selected?: string;
  onOpen: (id: string) => void;
  onOpenTrigger: () => void;
  // When set, the flow can be edited: a "+" on each link adds a step there.
  onAdd?: (slot: Slot) => void;
  // A test run: its path lights up and everything else fades.
  test?: TestMarks | null;
};

type TriggerData = { content: ReactNode; open: () => void; lit?: boolean };
type StepData = { step: Step; kindLabel: string; card: StepCard; selected: boolean; open: () => void; mark?: TestMark; dim?: boolean };
type EndData = { add?: () => void };
type SlotData = { label: string; add?: () => void; first?: boolean; error?: boolean; lit?: boolean; dim?: boolean };

function layout(props: FlowProps): { nodes: Node[]; edges: Edge<SlotData>[]; width: number; height: number } {
  const { steps, kinds, test } = props;
  // Testing shows the flow as it is: no "+" to add steps.
  const onAdd = test ? undefined : props.onAdd;
  const way = (from: string, field: string) => (test ? { lit: test.links.has(`${from}>${field}`), dim: !test.links.has(`${from}>${field}`) } : {});
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: "TB", nodesep: 36, ranksep: 56, marginx: 16, marginy: 16 });
  g.setDefaultEdgeLabel(() => ({}));
  const nodes: Node[] = [];
  const edges: Edge<SlotData>[] = [];
  const ids = new Set(steps.map((s) => s.id));
  const end = (key: string, add?: () => void) => {
    const id = `end:${key}`;
    g.setNode(id, add ? { width: W, height: 64 } : { width: 72, height: H.end });
    nodes.push({ id, type: add ? "first" : "end", position: { x: 0, y: 0 }, data: { add } satisfies EndData });
    return id;
  };
  const link = (from: string, to: string, slot: Slot, label: string) => {
    g.setEdge(from, to);
    edges.push({ id: `${from}>${slot.field}>${to}`, source: from, target: to, type: "slot", data: { label, add: onAdd ? () => onAdd(slot) : undefined, first: to === "end:start" && !!onAdd, ...way(from, slot.field) } });
  };
  g.setNode("trigger", { width: W + 40, height: H.trigger });
  nodes.push({ id: "trigger", type: "trigger", position: { x: 0, y: 0 }, data: { content: props.trigger, open: props.onOpenTrigger, lit: !!test } satisfies TriggerData });
  // An empty journey shows one big button for its first step.
  const first: Slot = { from: null, field: "next" };
  link("trigger", steps.length ? steps[0].id : end("start", onAdd && (() => onAdd(first))), first, "");
  for (const s of steps) {
    g.setNode(s.id, { width: W, height: H.step });
    nodes.push({
      id: s.id, type: "step", position: { x: 0, y: 0 },
      data: { step: s, kindLabel: kindOf(kinds, s.kind)?.label ?? s.kind, card: props.card(s), selected: props.selected === s.id, open: () => props.onOpen(s.id),
        mark: test?.steps.get(s.id), dim: !!test && !test.steps.has(s.id) } satisfies StepData,
    });
  }
  for (const s of steps) {
    for (const field of outputs(kinds, s)) {
      const to = linkOf(s, field);
      link(s.id, to && ids.has(to) ? to : end(`${s.id}:${field}`), { from: s.id, field }, outputLabel(field));
    }
    // A step's error path, when its error mode names a step.
    const err = errorLink(kinds, s);
    if (err && ids.has(err)) {
      g.setEdge(s.id, err);
      edges.push({ id: `${s.id}>error>${err}`, source: s.id, target: err, type: "slot", data: { label: "On error", error: true, ...way(s.id, "error") } });
    }
  }
  dagre.layout(g);
  // Keep a step's ways in the order of its outputs, left to right (Yes,
  // then No): dagre orders siblings by its own heuristic.
  for (const s of steps) {
    const kids = outputs(kinds, s).map((f) => linkOf(s, f)).filter((to): to is string => !!to && ids.has(to) && g.predecessors(to)?.length === 1);
    const xs = kids.map((k) => g.node(k).x).sort((a, b) => a - b);
    if (kids.length > 1 && kids.some((k, i) => g.node(k).x !== xs[i])) {
      const moved = new Map(kids.map((k, i) => [k, xs[i] - g.node(k).x]));
      // Shift each child together with everything only it leads to.
      for (const [k, dx] of moved) {
        const todo = [k];
        const seen = new Set<string>();
        while (todo.length) {
          const id = todo.pop()!;
          if (seen.has(id)) continue;
          seen.add(id);
          g.node(id).x += dx;
          for (const n of g.successors(id) ?? []) if ((g.predecessors(n)?.length ?? 0) === 1) todo.push(n);
        }
      }
    }
  }
  let bottom = 0;
  let left = Infinity;
  let right = 0;
  for (const n of nodes) {
    const p = g.node(n.id);
    n.position = { x: p.x - p.width / 2, y: p.y - p.height / 2 };
    n.width = p.width;
    n.height = p.height;
    bottom = Math.max(bottom, p.y + p.height / 2);
    left = Math.min(left, p.x - p.width / 2);
    right = Math.max(right, p.x + p.width / 2);
  }
  return { nodes, edges, width: right - left + 32, height: bottom + 16 };
}

function TriggerNode({ data }: NodeProps<Node<TriggerData>>) {
  return (
    <div className="h-full w-full">
      {/* A div, not a button: the card holds links of its own (its rule). */}
      <div role="button" tabIndex={0} onClick={data.open} onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && e.target === e.currentTarget && (e.preventDefault(), data.open())}
        className={cn("flex h-full w-full cursor-pointer flex-col gap-1 overflow-hidden rounded-lg border-2 border-primary/50 bg-card p-3 text-left text-[12px] shadow-sm hover:border-primary",
          data.lit && "border-violet-500/70 hover:border-violet-500")}>
        <span className="flex items-center gap-1.5 text-[11px] font-medium tracking-wide text-muted-foreground uppercase"><Zap className="size-3.5" />Trigger</span>
        {data.content}
      </div>
      <Handle type="source" position={Position.Bottom} isConnectable={false} className="!size-1 !min-w-0 !border-0 !bg-transparent" />
    </div>
  );
}

function StepNode({ data }: NodeProps<Node<StepData>>) {
  const { step, card, mark } = data;
  const Icon = iconFor(step.kind);
  const errors = card.problems.filter((p) => p.level === "error");
  const warnings = card.problems.filter((p) => p.level === "warning");
  return (
    <div className="h-full w-full">
      <Handle type="target" position={Position.Top} isConnectable={false} className="!size-1 !min-w-0 !border-0 !bg-transparent" />
      <button type="button" onClick={data.open} aria-label={`${data.kindLabel}: ${card.label}`}
        className={cn(
          "flex h-full w-full flex-col gap-0.5 overflow-hidden rounded-lg border bg-card px-3 py-2 text-left shadow-sm transition-colors hover:border-ring",
          data.selected && "border-ring ring-2 ring-ring/40",
          mark && "border-violet-500 ring-2 ring-violet-500/30",
          data.dim && "opacity-40",
          errors.length > 0 && "border-red-500/70 dark:border-red-500/60",
          !errors.length && warnings.length > 0 && "border-amber-500/70",
        )}>
        <span className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <Icon className="size-3.5 shrink-0" />
          <span className="truncate">{data.kindLabel}</span>
          {mark && <span className="ml-auto flex size-4 shrink-0 items-center justify-center rounded-full bg-violet-600 text-[10px] font-semibold text-white tabular-nums" title={`Step ${mark.n} of the test`}>{mark.n}</span>}
          {!mark && !data.dim && card.here > 0 && <span className="ml-auto shrink-0 rounded-full bg-blue-500/15 px-1.5 text-[11px] font-medium text-blue-700 dark:text-blue-300" title="On this step now">{card.here} here</span>}
          {errors.length > 0 ? (
            <CircleAlert className={cn("size-3.5 shrink-0 text-red-600 dark:text-red-400", !card.here && !mark && "ml-auto")} aria-label={`${errors.length} problem${errors.length === 1 ? "" : "s"}`} />
          ) : warnings.length > 0 && <TriangleAlert className={cn("size-3.5 shrink-0 text-amber-600 dark:text-amber-400", !card.here && !mark && "ml-auto")} aria-label="A warning" />}
        </span>
        <span className="truncate text-[13px] font-medium">{card.label}</span>
        {/* Under test, the card says what the step would do instead. */}
        {card.detail && !mark && <span className="truncate text-[12px] text-muted-foreground">{card.detail}</span>}
        {mark ? (
          <span className={cn("mt-auto truncate text-[11px] font-medium text-violet-700 dark:text-violet-300", mark.tone === "red" && "text-red-600 dark:text-red-400", mark.tone === "amber" && "text-amber-700 dark:text-amber-400")} title={mark.text}>{mark.text}</span>
        ) : !data.dim && card.numbers.length > 0 && (
          <span className="mt-auto truncate text-[11px] text-muted-foreground tabular-nums">
            {card.numbers.map((n, i) => (
              <span key={i} className={cn(n.tone === "red" && "text-red-600 dark:text-red-400", n.tone === "amber" && "text-amber-700 dark:text-amber-400")}>{i > 0 && " · "}{n.text}</span>
            ))}
          </span>
        )}
      </button>
      <Handle type="source" position={Position.Bottom} isConnectable={false} className="!size-1 !min-w-0 !border-0 !bg-transparent" />
    </div>
  );
}

function EndNode() {
  return (
    <div className="flex h-full w-full items-center justify-center rounded-full border border-dashed text-[11px] text-muted-foreground">
      <Handle type="target" position={Position.Top} isConnectable={false} className="!size-1 !min-w-0 !border-0 !bg-transparent" />
      End
    </div>
  );
}

function FirstNode({ data }: NodeProps<Node<EndData>>) {
  return (
    <div className="h-full w-full">
      <Handle type="target" position={Position.Top} isConnectable={false} className="!size-1 !min-w-0 !border-0 !bg-transparent" />
      <button type="button" onClick={data.add}
        className="flex h-full w-full flex-col items-center justify-center gap-0.5 rounded-lg border-2 border-dashed text-[13px] font-medium text-muted-foreground hover:border-ring hover:text-foreground">
        <span className="flex items-center gap-1"><Plus className="size-4" />Add the first step</span>
        <span className="text-[11px] font-normal">Usually an email</span>
      </button>
    </div>
  );
}

function SlotEdge({ id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, data }: EdgeProps<Edge<SlotData>>) {
  const [path, x, y] = getSmoothStepPath({ sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, borderRadius: 10, offset: 18 });
  return (
    <>
      <BaseEdge id={id} path={path} style={{ ...(data?.error ? { strokeDasharray: "5 4", stroke: "#dc2626" } : {}), ...(data?.lit ? { stroke: "#7c3aed", strokeWidth: 2.5 } : {}), ...(data?.dim ? { opacity: 0.3 } : {}) }} />
      <EdgeLabelRenderer>
        <div className="nodrag nopan pointer-events-auto absolute flex items-center gap-1" style={{ transform: `translate(-50%, -50%) translate(${x}px, ${y}px)` }}>
          {data?.label && <span className={cn("rounded bg-muted px-1.5 text-[11px] font-medium text-muted-foreground", data.lit && "bg-violet-600 text-white", data.dim && "opacity-40")}>{data.label}</span>}
          {data?.add && edgeHasRoom(data) && (
            <button type="button" onClick={data.add} aria-label={data.label ? `Add a step on ${data.label}` : "Add a step here"}
              className="flex size-5 items-center justify-center rounded-full border bg-background text-muted-foreground shadow-xs hover:border-ring hover:text-foreground">
              <Plus className="size-3" />
            </button>
          )}
        </div>
      </EdgeLabelRenderer>
    </>
  );
}

// The link into an empty journey's big button needs no "+" of its own.
const edgeHasRoom = (d: SlotData) => !d.first;

const nodeTypes = { trigger: TriggerNode, step: StepNode, end: EndNode, first: FirstNode };
const edgeTypes = { slot: SlotEdge };

function Zoom({ deps }: { deps: unknown }) {
  const rf = useReactFlow();
  // Fit again whenever the steps change shape.
  useEffect(() => {
    const t = setTimeout(() => rf.fitView({ maxZoom: 1, minZoom: 0.35, padding: 0.08 }), 0);
    return () => clearTimeout(t);
  }, [rf, deps]);
  const btn = "flex size-7 items-center justify-center rounded-md border bg-background text-muted-foreground shadow-xs hover:text-foreground";
  return (
    <div className="absolute right-2 bottom-2 z-10 flex gap-1">
      <button type="button" className={btn} onClick={() => rf.zoomOut()} aria-label="Zoom out"><Minus className="size-3.5" /></button>
      <button type="button" className={btn} onClick={() => rf.zoomIn()} aria-label="Zoom in"><Plus className="size-3.5" /></button>
      <button type="button" className={cn(btn, "w-auto px-2 text-[11px]")} onClick={() => rf.fitView({ maxZoom: 1, padding: 0.08 })}>Fit</button>
    </div>
  );
}

export function Flow(props: FlowProps) {
  const { nodes, edges, width, height } = useMemo(() => layout(props), [props]);
  const shape = useMemo(() => nodes.map((n) => n.id).join("|"), [nodes]);
  // As tall as the flow at the scale it fits the width at, within bounds.
  const box = useRef<HTMLDivElement>(null);
  const [room, setRoom] = useState(0);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setRoom(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const scale = room ? Math.max(0.35, Math.min(1, room / width)) : 1;
  return (
    <div ref={box} className="journey-flow relative w-full overflow-hidden rounded-lg border bg-muted/30" style={{ height: Math.min(Math.max(height * scale + 48, 280), 1100) }}>
      <ReactFlowProvider>
        <ReactFlow
          nodes={nodes} edges={edges} nodeTypes={nodeTypes} edgeTypes={edgeTypes}
          nodesDraggable={false} nodesConnectable={false} elementsSelectable={false} nodesFocusable={false} edgesFocusable={false}
          zoomOnScroll={false} preventScrolling={false} zoomOnDoubleClick={false} panOnDrag minZoom={0.3} maxZoom={1.5}
          fitView fitViewOptions={{ maxZoom: 1, minZoom: 0.35, padding: 0.08 }}
        >
          <Zoom deps={shape} />
          {/* Big flows get a map to find their way, problems in red. */}
          {props.steps.length > 6 && (
            <MiniMap pannable zoomable position="bottom-left" className="!m-2 hidden !rounded-md !border !bg-background sm:block" style={{ width: 140, height: 100 }}
              maskColor="color-mix(in oklch, var(--muted) 70%, transparent)"
              nodeColor={(n) => n.type === "step" ? ((n.data as StepData).mark ? "#7c3aed" : (n.data as StepData).card.problems.some((p) => p.level === "error") ? "#dc2626" : (n.data as StepData).card.here ? "#2a78d6" : "#a1a1aa") : n.type === "trigger" ? "#71717a" : "transparent"} />
          )}
        </ReactFlow>
      </ReactFlowProvider>
    </div>
  );
}
