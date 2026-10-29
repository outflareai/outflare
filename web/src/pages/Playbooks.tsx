import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import type { KitPlan, PlaybookConnectionNeed, WorkspacePlaybook } from "@outflare/sdk";
import { track } from "@/lib/analytics";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { KitFileSheet, KitPlanList } from "@/components/kit-export";
import { api, errorMessage } from "../api";
import { Badge, Button, CopyButton, Empty, ErrorText, field, Input, Loading, PageHeader, toast, type tones } from "../ui";
import { plural } from "../format";
import type { Tab } from "../App";

// Playbooks: ready-made recipes from the repository's playbooks/ folder.
// Anyone can add one with a pull request. A playbook keeps nothing of its
// own: starting one creates a mission draft, so everything it does goes
// through the mission's approvals and limits, and lands in the modules it
// uses (journeys, the inbox, campaign links, fixes).

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };

const tierLabel: Record<WorkspacePlaybook["tier"], string> = { obvious: "Obvious", clever: "Clever", edge: "Edge case" };
const tierTone: Record<WorkspacePlaybook["tier"], keyof typeof tones> = { obvious: "green", clever: "amber", edge: "blue" };
const views = { all: "All playbooks", ready: "Ready to run", started: "Started here" } as const;
type View = keyof typeof views;

const contributeURL = "https://github.com/outflareai/outflare/tree/main/playbooks#readme";

const needKey = (n: PlaybookConnectionNeed) => n.capability ?? n.category ?? n.label;

export function Playbooks({ ws, canManage, go }: Props) {
  const [list, setList] = useState<WorkspacePlaybook[] | null>(null);
  const [error, setError] = useState("");
  const [view, setView] = useState<View>("all");
  const [open, setOpen] = useState("");
  const [names, setNames] = useState<Record<string, string>>({});
  const [kitFile, setKitFile] = useState(false);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/playbooks", { params: { path: { ws } } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setList(data.data);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    api.GET("/v1/connectors").then(({ data }) => setNames(Object.fromEntries((data?.data ?? []).map((c) => [c.id, c.name]))));
  }, []);

  const shown = useMemo(() => (list ?? []).filter((p) => view === "all" || (view === "ready" ? p.readiness.ready : p.runs > 0)), [list, view]);
  const current = list?.find((p) => p.id === open);

  if (!list) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Playbooks"
        description="Ready-made recipes for growth. Each one says what it needs, runs as a mission on your own accounts, and waits for your approval before it changes anything. Copy the prompt into your own AI app, or start it here."
        action={
          <div className="flex flex-wrap items-center gap-3">
            <a className="text-[13px] text-muted-foreground underline-offset-4 hover:text-foreground hover:underline" href={contributeURL} target="_blank" rel="noreferrer">Write your own playbook</a>
            {canManage && <Button variant="ghost" onClick={() => setKitFile(true)}>Apply a kit file</Button>}
          </div>
        }
      />
      <ErrorText>{error}</ErrorText>
      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="Show" className={field} value={view} onChange={(e) => setView(e.target.value as View)}>
          {Object.entries(views).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
        </select>
        <span className="text-[13px] text-muted-foreground">{plural(list.filter((p) => p.readiness.ready).length, "playbook")} ready to run of {list.length}</span>
      </div>
      {shown.length === 0 ? (
        <Empty title={view === "ready" ? "Nothing is ready yet" : "None started yet"}>
          {view === "ready" ? "Open a playbook to see what to connect first." : "Start a playbook and it shows here."}
        </Empty>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {shown.map((p) => (
            <button
              key={p.id}
              type="button"
              onClick={() => {
                setOpen(p.id);
                track("ui.playbook_opened", { playbook_id: p.id, ready: p.readiness.ready });
              }}
              className="flex min-w-0 flex-col gap-2 rounded-lg border bg-card p-4 text-left transition-colors hover:border-ring/60 focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40"
            >
              <span className="flex flex-wrap items-center gap-1.5">
                <Badge tone={tierTone[p.tier]}>{tierLabel[p.tier]}</Badge>
                {p.publisher === "community" && <Badge title={`By @${p.author.github}`}>Community</Badge>}
                {p.readiness.ready ? <Badge tone="green">Ready</Badge> : <Badge tone="grey">{p.readiness.missing_connections.length > 0 ? "Connect first" : "Needs data"}</Badge>}
              </span>
              <span className="text-[14px] font-medium leading-snug">{p.title}</span>
              <span className="text-[13px] text-muted-foreground">{p.summary}</span>
              {p.runs > 0 && <span className="text-[12px] text-muted-foreground">Started {plural(p.runs, "time")}</span>}
            </button>
          ))}
        </div>
      )}
      {kitFile && <KitFileSheet ws={ws} onClose={() => setKitFile(false)} onApplied={() => { setKitFile(false); go("journeys"); }} />}
      {current && <PlaybookSheet ws={ws} playbook={current} names={names} canManage={canManage} go={go} onClose={() => setOpen("")} />}
    </div>
  );
}

function PlaybookSheet({ ws, playbook: p, names, canManage, go, onClose }: { ws: string; playbook: WorkspacePlaybook; names: Record<string, string>; canManage: boolean; go: Props["go"]; onClose: () => void }) {
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [kit, setKit] = useState<KitPlan | null>(null);
  const [applyKit, setApplyKit] = useState(false);
  const missing = new Set(p.readiness.missing_connections.map(needKey));
  const missingOptional = new Set(p.readiness.missing_optional.map(needKey));
  const missingData = new Set(p.readiness.missing_data.map((d) => d.check));
  const prompt = p.prompt.replace(/\{\{\s*([a-z0-9_]+)\s*\}\}/g, (m, name: string) => values[name]?.trim() || m);

  useEffect(() => {
    if (!p.kit) return;
    api.GET("/v1/workspaces/{ws}/kits/{id}/preview", { params: { path: { ws, id: p.kit } } }).then(({ data }) => {
      if (!data) return;
      setKit(data);
      setApplyKit(data.changes > 0 && data.conflicts === 0);
    });
  }, [ws, p.kit]);

  async function start(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/playbooks/{id}/runs", { params: { path: { ws, id: p.id } }, body: { inputs: values, apply_kit: applyKit } });
    setBusy(false);
    if (error || !data || !("id" in data)) return setError(errorMessage(error));
    toast(`Started ${p.title} as a mission draft. Approve it to let it run.`);
    go("missions", data.id);
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{p.title}</SheetTitle>
          <SheetDescription>
            {tierLabel[p.tier]} · {p.publisher === "community" ? <>Community playbook by <a className="underline underline-offset-4" href={`https://github.com/${p.author.github}`} target="_blank" rel="noreferrer">@{p.author.github}</a></> : "By Outflare"} · <a className="underline underline-offset-4" href={p.source_url} target="_blank" rel="noreferrer">Source</a>
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-6 px-4 pb-8 text-[13px]">
          <p className="whitespace-pre-line text-muted-foreground">{p.description || p.summary}</p>

          <section className="space-y-2">
            <h3 className="font-medium">What it needs</h3>
            <ul className="space-y-1.5">
              {p.needs.data.map((d) => (
                <li key={d.check} className="flex items-start gap-2">
                  <Badge tone={missingData.has(d.check) ? "amber" : "green"}>{missingData.has(d.check) ? "Missing" : "Have"}</Badge>
                  <span>{d.label}</span>
                </li>
              ))}
              {p.needs.connections.map((n) => {
                const k = needKey(n);
                const gap = missing.has(k) || missingOptional.has(k);
                return (
                  <li key={k} className="flex items-start gap-2">
                    <Badge tone={gap ? (n.optional ? "grey" : "amber") : "green"}>{gap ? (n.optional ? "Optional" : "Connect") : "Connected"}</Badge>
                    <span className="min-w-0">
                      {n.label}
                      {gap && n.recommended.length > 0 && <span className="text-muted-foreground"> · suggested: {n.recommended.map((id) => names[id] ?? id).join(", ")}</span>}
                      {gap && <> · <button type="button" className="underline underline-offset-4" onClick={() => go("connectors")}>Connectors</button></>}
                    </span>
                  </li>
                );
              })}
              {p.needs.data.length + p.needs.connections.length === 0 && <li className="text-muted-foreground">Nothing beyond Outflare itself.</li>}
            </ul>
            <p className="text-[12px] text-muted-foreground">Everything runs on your own accounts and keys. Outflare never lends its own.</p>
          </section>

          {kit && (
            <section className="space-y-2">
              <h3 className="font-medium">What it sets up</h3>
              <p className="text-muted-foreground">
                {kit.changes === 0 && kit.conflicts === 0 ? "Everything it needs is already set up." : kit.items.some((it) => it.kind === "journey" || it.kind === "rule")
                  ? "The setup kit adds what's missing: CRM fields, email drafts and automations. It only adds, and nothing goes live: you read the emails and turn each rule and journey on yourself."
                  : "The setup kit adds what's missing to your CRM and email drafts. It only adds: nothing you have changes, and drafts wait for you to publish them."}
              </p>
              <KitPlanList plan={kit} />
            </section>
          )}

          <section className="space-y-2">
            <h3 className="font-medium">Steps</h3>
            <ol className="list-decimal space-y-1.5 pl-5">
              {p.steps.map((s, i) => (
                <li key={i}>
                  {s.say}
                  {s.tools.length > 0 && <span className="block font-mono text-[11px] text-muted-foreground">{s.tools.join(", ")}</span>}
                </li>
              ))}
            </ol>
          </section>

          {p.guardrails.length > 0 && (
            <section className="space-y-2">
              <h3 className="font-medium">What it won't do</h3>
              <ul className="list-disc space-y-1 pl-5">{p.guardrails.map((g) => <li key={g}>{g}</li>)}</ul>
            </section>
          )}

          <section className="space-y-2">
            <h3 className="font-medium">How you'll know it worked</h3>
            <ul className="space-y-1">{p.measure.map((m) => <li key={m.metric}><span className="font-medium">{m.metric}.</span> <span className="text-muted-foreground">{m.how}</span></li>)}</ul>
          </section>

          <form className="space-y-3" onSubmit={start}>
            <h3 className="font-medium">Run it</h3>
            {p.inputs.map((i) => (
              <Input key={i.name} label={i.optional ? `${i.label} (optional)` : i.label} hint={i.help} placeholder={i.example} required={!i.optional} value={values[i.name] ?? ""} onChange={(e) => setValues({ ...values, [i.name]: e.target.value })} />
            ))}
            <div className="relative rounded-md border bg-muted/40 p-3">
              <pre className="whitespace-pre-wrap pr-8 font-mono text-[12px] leading-relaxed">{prompt}</pre>
              <span className="absolute right-2 top-2" onClickCapture={() => track("ui.playbook_prompt_copied", { playbook_id: p.id })}>
                <CopyButton text={prompt} label="Copy prompt" />
              </span>
            </div>
            <p className="text-[12px] text-muted-foreground">
              Paste the prompt into Claude, ChatGPT or Cursor with Outflare connected (<button type="button" className="underline underline-offset-4" onClick={() => go("ai")}>AI apps</button>), or start it here as a mission draft.
            </p>
            {kit && kit.changes > 0 && kit.conflicts === 0 && canManage && (
              <label className="flex items-center gap-2">
                <input type="checkbox" className="size-4" checked={applyKit} onChange={(e) => setApplyKit(e.target.checked)} />
                Set up what it needs when I start ({plural(kit.changes, "change")})
              </label>
            )}
            <ErrorText>{error}</ErrorText>
            {canManage && (
              <Button type="submit" disabled={busy || p.readiness.missing_connections.length > 0}>
                {p.readiness.missing_connections.length > 0 ? "Connect what it needs first" : busy ? "Starting…" : "Start as a mission"}
              </Button>
            )}
          </form>

          {p.example && (
            <section className="space-y-2">
              <h3 className="font-medium">Example</h3>
              <p className="whitespace-pre-line text-muted-foreground">{p.example}</p>
            </section>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
