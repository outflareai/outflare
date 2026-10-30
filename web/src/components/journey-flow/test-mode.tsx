// Test mode: pick a person and the flow lights up the path they would take,
// each step saying what it would do. The engine runs the real steps and
// rolls everything back (POST .../journeys/{id}/test), so nothing is sent
// or written. A branch can be sent the other way to see that path too.

import { useCallback, useEffect, useRef, useState } from "react";
import { FlaskConical, X } from "lucide-react";
import type { EmailTemplate, JourneyTest } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { track } from "@/lib/analytics";
import { cn } from "@/lib/utils";
import { Button, ErrorText } from "../../ui";
import { ago } from "../../format";
import { PersonPicker, type Person } from "@/components/person-picker";
import { errorLink, stepLabel, waitText, type Step, type StepKind } from "./model";

export type TestMark = { n: number; text: string; tone?: "red" | "amber" };
// What the flow shows under test: each step on the path, in order, and the
// links taken (from step id, then the output field, or "error").
export type TestMarks = { steps: Map<string, TestMark>; links: Set<string> };

type Result = JourneyTest;
type TestStep = Result["steps"][number];

// The line a step shows under test, in the conditional: nothing happened.
function markText(r: TestStep, s: Step | undefined): Omit<TestMark, "n"> {
  if (r.outcome === "failed") return { text: `Would fail${r.detail ? `: ${r.detail}` : ""}`, tone: "red" };
  if (r.outcome === "skipped") return { text: `Skipped${r.detail ? `: ${r.detail}` : ""}`, tone: "amber" };
  switch (r.kind) {
    case "email":
      return { text: r.subject ? `Would send “${r.subject}”` : "Would send" };
    case "wait":
      return { text: `Waits ${waitText(Number(s?.wait_seconds ?? 0))}` };
    case "branch": {
      const way = r.outcome === "yes" ? "Yes" : "No";
      if (r.data && r.data !== r.outcome) return { text: `Goes ${way} (your choice)` };
      return { text: r.can_change ? `Goes ${way} for now` : `Goes ${way}` };
    }
    case "exit":
      return { text: "Exits here" };
  }
  return { text: r.outcome === "done" ? "Would do it" : r.outcome };
}

export function testMarks(res: Result, steps: Step[], kinds: StepKind[]): TestMarks {
  const byID = new Map(steps.map((s) => [s.id, s]));
  const marks = new Map<string, TestMark>();
  const links = new Set<string>();
  if (res.steps.length || res.end === "completed") links.add("trigger>next");
  res.steps.forEach((r, i) => {
    const s = byID.get(r.step);
    marks.set(r.step, { n: i + 1, ...markText(r, s) });
    const last = i === res.steps.length - 1;
    if (!r.next && !(last && res.end === "completed")) return;
    let field = "next";
    if (r.kind === "branch") field = r.outcome === "yes" ? "then" : "else";
    else if (r.outcome === "failed" && s && r.next && r.next === errorLink(kinds, s)) field = "error";
    links.add(`${r.step}>${field}`);
  });
  return { steps: marks, links };
}

const endText = (res: Result) =>
  res.end === "completed" ? "Reaches the end of the journey." :
  res.end_reason === "exit_condition" ? "Exits: they meet the journey's exit criteria." :
  res.end === "exited" ? "Exits at an exit step." :
  res.end_reason === "too many steps" ? "Stops: it goes round too many steps." :
  res.end_reason === "unknown_step" ? "Stops at a step that doesn't exist." :
  "Fails here, and the run would stop.";

// TestPanel sits above the flow while testing. version says which version
// the engine tests, and note says so to people; saved changes when the
// tested version is saved again, to test it afresh.
export function TestPanel({ ws, journey, version, note, steps, kinds, templates, triggerEvent, saved, onResult, onClose, onOpen }: {
  ws: string; journey: string; version: "live" | "draft"; note: string; steps: Step[]; kinds: StepKind[]; templates: EmailTemplate[]; triggerEvent: string | null; saved: string;
  onResult: (m: TestMarks | null) => void; onClose: () => void; onOpen: (step: string) => void;
}) {
  const [person, setPerson] = useState<Person | null>(null);
  const [paths, setPaths] = useState<Record<string, "yes" | "no">>({});
  const [res, setRes] = useState<Result | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const stepsRef = useRef(steps);
  stepsRef.current = steps;
  const run = useCallback(async (p: Person, ways: Record<string, "yes" | "no">) => {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/journeys/{id}/test", { params: { path: { ws, id: journey } }, body: { record_id: p.id, version, paths: ways } });
    setBusy(false);
    if (error || !data) {
      setError(errorMessage(error));
      return;
    }
    setError("");
    setRes(data);
  }, [ws, journey, version]);
  useEffect(() => {
    if (person) run(person, paths);
  }, [person, paths, run, saved]);
  useEffect(() => {
    onResult(res ? testMarks(res, stepsRef.current, kinds) : null);
  }, [res, kinds, onResult]);
  const byID = new Map(steps.map((s) => [s.id, s]));
  const who = person?.name || person?.email || "They";
  return (
    <section aria-label="Test" className="space-y-3 rounded-lg border border-violet-500/40 bg-violet-500/5 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex items-center gap-1.5 text-[13px] font-medium"><FlaskConical className="size-4 text-violet-600 dark:text-violet-400" />Test with</span>
        <PersonPicker ws={ws} person={person} onPick={(p) => { setPaths({}); setPerson(p); }} />
        <span className="text-[12px] text-muted-foreground">{note} Nothing is sent or saved.</span>
        <Button variant="ghost" className="ml-auto" onClick={onClose} aria-label="Close the test"><X className="size-4" /></Button>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {!person && <p className="text-[13px] text-muted-foreground">Pick someone to see the path they would take through this journey, and what each step would do for them.</p>}
      {person && res && (
        <div className={cn("space-y-2 text-[13px]", busy && "opacity-60")}>
          {res.refusal && <p className="rounded-md bg-amber-500/10 px-2 py-1 text-amber-800 dark:text-amber-300">{who} wouldn't start it now: {res.refusal}. The path below shows what it would do anyway.</p>}
          {triggerEvent && (
            <p className="text-[12px] text-muted-foreground">
              {res.event_at ? `Uses their latest ${triggerEvent} event, ${ago(res.event_at)}.` : `They have no ${triggerEvent} event, so its fields are empty in emails and checks.`}
            </p>
          )}
          <ol className="space-y-1">
            {res.steps.map((r, i) => {
              const s = byID.get(r.step);
              const m = markText(r, s);
              const other = r.outcome === "yes" ? "no" : "yes";
              return (
                <li key={`${r.step}:${i}`} className="grid grid-cols-[1.25rem_1fr] items-baseline gap-x-2">
                  <span className="text-right text-[12px] text-muted-foreground tabular-nums">{i + 1}.</span>
                  <span className="flex flex-wrap items-baseline gap-x-2">
                  <button type="button" className="font-medium hover:underline" onClick={() => onOpen(r.step)}>{stepLabel(s, kinds, templates) || r.step}</button>
                  <span className={cn("text-muted-foreground", m.tone === "red" && "text-red-600 dark:text-red-400", m.tone === "amber" && "text-amber-700 dark:text-amber-400")}>{m.text}</span>
                  {r.kind === "branch" && (
                    <button type="button" className="text-[12px] text-violet-700 underline dark:text-violet-300" onClick={() => {
                      track("ui.journey_test_path_switched", { to: other });
                      setPaths((p) => ({ ...p, [r.step]: other }));
                    }}>Try {other === "yes" ? "Yes" : "No"}</button>
                  )}
                  </span>
                </li>
              );
            })}
          </ol>
          <p className="font-medium">{endText(res)}</p>
          {Object.keys(paths).length > 0 && (
            <button type="button" className="text-[12px] underline" onClick={() => setPaths({})}>Follow their data again</button>
          )}
        </div>
      )}
    </section>
  );
}
