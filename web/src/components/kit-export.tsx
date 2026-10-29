// Kit files: a journey exported as a flow kit, and a kit file applied in
// another workspace. The engine gathers what a journey uses and writes the
// file (POST .../kits/export); applying one checks it as CI checks a
// shipped kit and adds every part as a draft (POST .../kits/apply).

import { useEffect, useRef, useState, type FormEvent } from "react";
import { Download, FileUp } from "lucide-react";
import type { KitPlan, Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { track } from "@/lib/analytics";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Badge, Button, CopyButton, ErrorText, field, Input, Loading, toast, type tones } from "../ui";
import { plural } from "../format";

const contributeURL = "https://github.com/outflareai/outflare/tree/main/playbooks#exporting-a-flow-as-a-kit";

const kitAction: Record<KitPlan["items"][number]["action"], { label: string; tone: keyof typeof tones }> = {
  add: { label: "Adds", tone: "blue" },
  extend: { label: "Extends", tone: "blue" },
  reuse: { label: "Have", tone: "green" },
  conflict: { label: "Conflict", tone: "amber" },
};
const kitKind: Record<KitPlan["items"][number]["kind"], string> = {
  option_set: "List", record_type: "Pipeline", field: "Field", email_template: "Email draft", smart_list: "People list", rule: "Rule", journey: "Journey",
};

// KitPlanList is what applying a kit adds, has already, or conflicts with.
export function KitPlanList({ plan }: { plan: KitPlan }) {
  return (
    <>
      <ul className="space-y-1.5">
        {plan.items.filter((it) => it.kind !== "field" || it.action !== "reuse").map((it) => (
          <li key={it.kind + it.key} className="flex items-start gap-2">
            <Badge tone={kitAction[it.action].tone}>{kitAction[it.action].label}</Badge>
            <span className="min-w-0">
              {kitKind[it.kind]}: {it.label}
              {it.detail && <span className="text-muted-foreground"> · {it.detail}</span>}
            </span>
          </li>
        ))}
      </ul>
      {plan.conflicts > 0 && <p className="text-[12px] text-muted-foreground">A conflict is something you already have under the same name but set up differently. Rename or change it first.</p>}
    </>
  );
}

type Export = Schemas["KitExport"];

// KitExportSheet writes a journey, and what it uses, as a kit file.
export function KitExportSheet({ ws, journey, onClose }: { ws: string; journey: string; onClose: () => void }) {
  const [res, setRes] = useState<Export | null>(null);
  // The events that become inputs; null until the first export says
  // which events there are.
  const [inputs, setInputs] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    setBusy(true);
    api.POST("/v1/workspaces/{ws}/kits/export", { params: { path: { ws } }, body: { journeys: [journey], inputs: inputs ?? [] } }).then(({ data, error }) => {
      if (!live) return;
      setBusy(false);
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setRes(data);
      // At first, every event becomes an input except those the kit's own
      // rules emit, which only mean something inside the kit.
      if (inputs === null) {
        const emitted = new Set(data.kit.rules.map((r) => String(r.emit_event ?? "")));
        setInputs(data.events.map((e) => e.name).filter((n) => !emitted.has(n)));
      }
    });
    return () => { live = false; };
  }, [ws, journey, inputs]);

  function download(r: Export) {
    const url = URL.createObjectURL(new Blob([r.yaml], { type: "application/yaml" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `${r.kit.id}.yaml`;
    a.click();
    URL.revokeObjectURL(url);
    track("ui.kit_downloaded", { journeys: r.kit.journeys.length, rules: r.kit.rules.length, inputs: r.kit.inputs.length });
  }

  const k = res?.kit;
  const parts = k && [
    plural(k.journeys.length, "journey"),
    k.rules.length > 0 && plural(k.rules.length, "rule"),
    k.smart_lists.length > 0 && plural(k.smart_lists.length, "people list"),
    k.email_templates.length > 0 && plural(k.email_templates.length, "email draft"),
  ].filter(Boolean).join(", ");
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>Export as a kit</SheetTitle>
          <SheetDescription>
            A kit file sets this journey up somewhere else, with what it uses, all as drafts. Apply it in another workspace from Playbooks, or <a className="underline underline-offset-4" href={contributeURL} target="_blank" rel="noreferrer">contribute it as a playbook</a>.
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-6 px-4 pb-8 text-[13px]">
          <ErrorText>{error}</ErrorText>
          {!res ? !error && <Loading /> : (
            <>
              <section className="space-y-2">
                <h3 className="font-medium">What it takes along</h3>
                <p className="text-muted-foreground">{parts}. {[...k!.journeys, ...k!.rules, ...k!.smart_lists].map((p) => p.name).join(" · ")}</p>
                {res.notes.length > 0 && (
                  <ul className="list-disc space-y-1 pl-5 text-amber-800 dark:text-amber-300">{res.notes.map((n) => <li key={n}>{n}</li>)}</ul>
                )}
              </section>
              {res.events.length > 0 && (
                <section className="space-y-2">
                  <h3 className="font-medium">Events they can change</h3>
                  <p className="text-muted-foreground">A ticked event becomes a setting of the kit: the workspace applying it names the event it sends for the same thing, with yours as the default.</p>
                  <ul className="space-y-1">
                    {res.events.map((e) => (
                      <li key={e.name}>
                        <label className="flex items-center gap-2">
                          <input type="checkbox" className="size-4" disabled={busy} checked={(inputs ?? []).includes(e.name)}
                            onChange={(ev) => setInputs((cur) => ev.target.checked ? [...(cur ?? []), e.name] : (cur ?? []).filter((n) => n !== e.name))} />
                          <span className="font-mono text-[12px]">{e.name}</span>
                        </label>
                      </li>
                    ))}
                  </ul>
                </section>
              )}
              <section className="space-y-2">
                <div className="flex items-center gap-2">
                  <h3 className="mr-auto font-medium">{k!.id}.yaml</h3>
                  <CopyButton text={res.yaml} label="Copy" />
                  <Button variant="ghost" onClick={() => download(res)}><Download className="size-4" />Download</Button>
                </div>
                <pre className={`max-h-96 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-[12px] leading-relaxed ${busy ? "opacity-60" : ""}`}>{res.yaml}</pre>
              </section>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// KitFileSheet applies a kit file, such as one exported from another
// workspace: preview first, then add every part as a draft.
export function KitFileSheet({ ws, onClose, onApplied }: { ws: string; onClose: () => void; onApplied: () => void }) {
  const [yaml, setYaml] = useState("");
  const [plan, setPlan] = useState<KitPlan | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const [sequences, setSequences] = useState<{ id: string; name: string }[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!plan?.inputs.some((i) => i.kind === "sequence")) return;
    api.GET("/v1/workspaces/{ws}/sequences", { params: { path: { ws } } }).then(({ data }) => setSequences((data?.data ?? []).map((s) => ({ id: s.id, name: s.name }))));
  }, [ws, plan]);

  async function preview(text: string) {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/kits/preview", { params: { path: { ws } }, body: { yaml: text } });
    setBusy(false);
    setPlan(data ?? null);
    setError(error ? errorMessage(error) : "");
  }

  async function apply(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/kits/apply", { params: { path: { ws } }, body: { yaml, inputs: values } });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    if (!("items" in data)) {
      toast("Applying the kit waits for approval.");
      return onClose();
    }
    toast(`Set up ${data.title}: ${plural(data.changes, "part")} added as drafts.`);
    onApplied();
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>Apply a kit file</SheetTitle>
          <SheetDescription>
            Set up flows from a kit file, such as one exported from another workspace (Export as a kit, on a journey). It only adds, and nothing goes live: every rule and journey is a draft you check and turn on.
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-6 px-4 pb-8 text-[13px]">
          <section className="space-y-2">
            <div className="flex items-center gap-2">
              <h3 className="mr-auto font-medium">The kit file</h3>
              <input ref={fileRef} type="file" accept=".yaml,.yml,text/yaml" className="hidden" onChange={async (e) => {
                const f = e.target.files?.[0];
                if (!f) return;
                track("ui.kit_file_chosen");
                const text = await f.text();
                setYaml(text);
                preview(text);
              }} />
              <Button variant="ghost" onClick={() => fileRef.current?.click()}><FileUp className="size-4" />Choose a file</Button>
            </div>
            <textarea aria-label="Kit file" className={`${field} min-h-40 w-full font-mono text-[12px]`} placeholder="id: activate-new-trials&#10;version: 1&#10;…" value={yaml}
              onChange={(e) => { setYaml(e.target.value); setPlan(null); }} />
            {!plan && <Button onClick={() => preview(yaml)} disabled={busy || !yaml.trim()}>{busy ? "Checking…" : "Preview"}</Button>}
          </section>
          <ErrorText>{error}</ErrorText>
          {plan && (
            <form className="space-y-4" onSubmit={apply}>
              <section className="space-y-2">
                <h3 className="font-medium">{plan.title}</h3>
                <KitPlanList plan={plan} />
              </section>
              {plan.inputs.length > 0 && (
                <section className="space-y-3">
                  <h3 className="font-medium">Settings</h3>
                  {plan.inputs.map((i) => i.kind === "sequence" ? (
                    <label key={i.name} className="block space-y-1">
                      <span className="block font-medium">{i.label}{i.optional ? " (optional)" : ""}</span>
                      <select className={`${field} w-full`} required={!i.optional} value={values[i.name] ?? ""} onChange={(e) => setValues({ ...values, [i.name]: e.target.value })}>
                        <option value="">Pick a sequence</option>
                        {sequences.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
                      </select>
                      {i.help && <span className="block text-[12px] text-muted-foreground">{i.help}</span>}
                    </label>
                  ) : (
                    <Input key={i.name} label={i.optional ? `${i.label} (optional)` : i.label} hint={i.help} placeholder={i.default || i.example}
                      required={!i.optional && !i.default} value={values[i.name] ?? ""} onChange={(e) => setValues({ ...values, [i.name]: e.target.value })} />
                  ))}
                </section>
              )}
              <Button type="submit" disabled={busy || plan.conflicts > 0 || plan.changes === 0}>
                {plan.conflicts > 0 ? "Sort out the conflicts first" : plan.changes === 0 ? "Already set up" : busy ? "Setting up…" : `Set up ${plural(plan.changes, "part")} as drafts`}
              </Button>
            </form>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
