import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import type { BrandContext, Memory, WorkspaceContext } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, toast } from "../ui";
import { ago } from "../format";
import { cn } from "@/lib/utils";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";

// What Outflare knows about the picked brand: the context every agent,
// chat and teammate reads first, and the memories agents saved as they
// worked. Brands are fully separate, so each brand has its own and none is
// shared: what is answered here never has to be told to an agent again,
// and what an agent learned can be checked and corrected.

type Form = Pick<BrandContext, "name" | "website" | "about" | "audience" | "goals" | "voice" | "rules" | "notes">;
const keys = ["name", "website", "about", "audience", "goals", "voice", "rules", "notes"] as const;
const toForm = (c: BrandContext): Form => Object.fromEntries(keys.map((k) => [k, c[k]])) as Form;

const kindTone = { preference: "blue", decision: "green", fact: "grey", feedback: "amber" } as const;
const kindLabel = { preference: "Preference", decision: "Decision", fact: "Fact", feedback: "Feedback" } as const;

function TextArea({ label, hint, className, ...props }: React.TextareaHTMLAttributes<HTMLTextAreaElement> & { label: string; hint?: ReactNode }) {
  return (
    <label className="block text-sm">
      <span className="mb-1.5 block text-[13px] font-medium">{label}</span>
      <textarea className={cn(field, "w-full resize-y py-2 leading-relaxed h-auto", className)} {...props} />
      {hint && <span className="mt-1 block text-xs text-muted-foreground">{hint}</span>}
    </label>
  );
}

export function Company({ ws, canManage }: { ws: string; canManage: boolean }) {
  const { brand, active, loaded } = useBrand(ws);
  if (!loaded) return <Loading />;
  if (!brand) return <PickBrand what="What Outflare knows about your company" brands={active.length} />;
  return <BrandCompany key={brand.id} ws={ws} brand={brand.handle} brandId={brand.id} canManage={canManage} />;
}

function BrandCompany({ ws, brand, brandId, canManage }: { ws: string; brand: string; brandId: string; canManage: boolean }) {
  const [data, setData] = useState<WorkspaceContext | null>(null);
  const [memories, setMemories] = useState<Memory[]>([]);
  const [q, setQ] = useState("");

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/context", { params: { path: { ws }, query: { brand } } });
    if (data) setData(data);
  }, [ws, brand]);
  const loadMemories = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/memories", { params: { path: { brand: brandId }, query: { limit: 200, ...(q.trim() ? { q: q.trim() } : {}) } } });
    if (data) setMemories(data.data);
  }, [ws, brand, q]);
  useEffect(() => void load(), [load]);
  useEffect(() => {
    const t = setTimeout(loadMemories, 200);
    return () => clearTimeout(t);
  }, [loadMemories]);

  if (!data) return <Loading />;
  const c = data.brand;
  if (!c) return <Loading />;

  async function addMemory() {
    const r = await ask({
      title: "Remember something",
      body: "One lasting fact, preference or decision every agent should know, in a sentence.",
      input: { label: "Memory", placeholder: "Never mention pricing in a first email", maxLength: 500 },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok", variant: "primary" }],
    });
    if (r?.choice !== "ok" || !r.value.trim()) return;
    const { error } = await api.POST("/v1/brands/{brand}/memories", { params: { path: { brand: brandId } }, body: { text: r.value.trim(), kind: "preference", brand_id: brandId } });
    if (error) return toast(errorMessage(error), "error");
    toast("Saved. Agents will read it from now on.");
    loadMemories();
    load();
  }

  async function edit(m: Memory) {
    const r = await ask({
      title: "Correct this memory",
      input: { label: "Memory", value: m.text, maxLength: 500 },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok", variant: "primary" }],
    });
    if (r?.choice !== "ok" || !r.value.trim() || r.value.trim() === m.text) return;
    const { error } = await api.PATCH("/v1/brands/{brand}/memories/{id}", { params: { path: { brand: brandId, id: m.id } }, body: { text: r.value.trim() } });
    if (error) return toast(errorMessage(error), "error");
    loadMemories();
  }

  async function change(m: Memory, body: { pinned?: boolean; kind?: Memory["kind"] }) {
    const { error } = await api.PATCH("/v1/brands/{brand}/memories/{id}", { params: { path: { brand: brandId, id: m.id } }, body });
    if (error) return toast(errorMessage(error), "error");
    loadMemories();
  }

  async function forget(m: Memory) {
    if (!(await confirmAction("Forget this memory?", <>Agents will no longer read “{m.text}”.</>, "Forget"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/memories/{id}", { params: { path: { brand: brandId, id: m.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Forgotten.");
    loadMemories();
    load();
  }

  return (
    <div className="max-w-3xl space-y-6">
      <PageHeader
        title="About your company"
        description={`What Outflare knows about ${c.name}. Every agent, chat and teammate reads this first, so nobody has to explain it twice. Each brand has its own, and none is shared: pick another brand to see its.`}
      />

      <ContextCard ws={ws} context={c} canManage={canManage} onSaved={load} />

      <LookCard ws={ws} context={c} canManage={canManage} onSaved={load} />

      <Card
        title={<span className="flex items-center gap-2">Memories <Badge>{data.memory_count}</Badge></span>}
        description="Short facts, preferences and decisions people and agents saved as they worked. Pinned ones go into every conversation; agents search the rest. Correct or forget anything that is wrong."
        action={canManage && <Button variant="ghost" onClick={addMemory}>Add a memory</Button>}
      >
        <div className="space-y-3">
          {(data.memory_count > 5 || q) && <Input label="Search memories" value={q} onChange={(e) => setQ(e.target.value)} placeholder="pricing, Series A, tone…" />}
          {memories.length === 0 ? (
            <Empty title={q ? "No memory matches" : "Nothing remembered yet"}>
              {q ? "Try other words." : "When you tell an agent something lasting, such as a word to avoid or who not to email, it saves it here."}
            </Empty>
          ) : (
            <ul className="divide-y divide-border rounded-lg border">
              {memories.map((m) => (
                <li key={m.id} className="flex items-start gap-3 px-3 py-2.5">
                  <div className="min-w-0 flex-1">
                    <p className="text-sm">{m.text}</p>
                    <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                      <Badge tone={kindTone[m.kind]}>{kindLabel[m.kind]}</Badge>
                      {m.pinned && <Badge tone="blue">Pinned</Badge>}
                      <span>
                        {m.created_by ? `${m.created_by}, ` : ""}
                        {ago(m.updated_at)}
                        {m.mission_id ? " in a mission" : ""}
                      </span>
                    </p>
                  </div>
                  {canManage && (
                    <Menu
                      aria={`Actions for memory: ${m.text}`}
                      items={[
                        { label: m.pinned ? "Unpin" : "Pin to every conversation", onClick: () => change(m, { pinned: !m.pinned }) },
                        { label: "Edit", onClick: () => edit(m) },
                        ...(Object.keys(kindLabel) as Memory["kind"][]).filter((k) => k !== m.kind).map((k) => ({ label: `Mark as ${kindLabel[k].toLowerCase()}`, onClick: () => change(m, { kind: k }) })),
                        { label: "Forget", onClick: () => forget(m), danger: true },
                      ]}
                    />
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      </Card>

      <Card title="Ask your agent" description="In Claude, ChatGPT, Cursor or any MCP client connected to Outflare, these work without explaining anything first.">
        <ul className="space-y-1.5 text-sm text-muted-foreground">
          <li>“Set up Outflare for this brand.” Runs a short interview and fills in what is missing above.</li>
          <li>“What’s blocking my outreach?” Lists every problem and who has to fix it.</li>
          <li>“Remember we never email agencies.” Saves a memory here.</li>
        </ul>
      </Card>
    </div>
  );
}

function ContextCard({ ws, context: c, canManage, onSaved }: { ws: string; context: BrandContext; canManage: boolean; onSaved: () => void }) {
  const [form, setForm] = useState<Form>(() => toForm(c));
  const [base, setBase] = useState(c.version);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const saved = useMemo(() => JSON.stringify(toForm(c)), [c]);
  const dirty = JSON.stringify(form) !== saved;
  const brandPath = { params: { path: { ws, brand: c.brand_id } } };

  // A newer version from an agent replaces the form only when nothing is
  // being edited here; otherwise saving reports the clash.
  useEffect(() => {
    if (!dirty) {
      setForm(toForm(c));
      setBase(c.version);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [c.version, c.name, c.website]);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    // The name and website are the brand's own; the rest is its context.
    if (form.name.trim() !== c.name || form.website.trim() !== c.website) {
      const { error } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}", { ...brandPath, body: { name: form.name.trim(), website: form.website.trim() } });
      if (error) {
        setBusy(false);
        return setError(errorMessage(error));
      }
    }
    const { name: _name, website: _website, ...text } = form;
    const { data, error, response } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}/context", { ...brandPath, body: { ...text, version: base } });
    setBusy(false);
    if (error) {
      return setError(response.status === 409 ? "Someone (or an agent) changed this while you were editing. Copy anything you want to keep, then reload the page." : errorMessage(error));
    }
    setBase(data.version);
    setForm(toForm(data));
    toast(JSON.stringify(toForm(data)) !== saved ? "Saved. Every agent reads this from now on." : "No changes to save.");
    onSaved();
  }

  async function dismiss() {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}/context", { ...brandPath, body: { onboarding_dismissed: true } });
    if (error) return toast(errorMessage(error), "error");
    toast("Agents won't ask for the rest.");
    onSaved();
  }

  const set = (k: keyof Form) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const ro = !canManage;
  const labels: Record<string, string> = { about: "what you do", goals: "goals", voice: "voice" };

  return (
    <Card
      title={`About ${c.name}`}
      description={<>Only for this brand. {c.updated_at ? <>Version {c.version}, updated {ago(c.updated_at)}{c.updated_by ? ` by ${c.updated_by}` : ""}.</> : null}</>}
    >
      <form onSubmit={save} className="space-y-4">
        {c.ask_for_missing && !ro && (
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-dashed px-3 py-2 text-[13px] text-muted-foreground">
            <span>Still to fill: {c.missing.map((m) => labels[m] ?? m).join(", ")}. Agents will ask for these once.</span>
            <Button type="button" variant="ghost" onClick={dismiss}>Don't ask again</Button>
          </div>
        )}
        <div className="grid gap-4 md:grid-cols-2">
          <Input label="Name" value={form.name} onChange={set("name")} maxLength={200} readOnly={ro} placeholder="Acme Analytics" />
          <Input label="Website" value={form.website} onChange={set("website")} maxLength={253} readOnly={ro} placeholder="acme-com.example" />
        </div>
        <TextArea label="What you do" hint="A few sentences, the way you'd explain it to a new hire." value={form.about} onChange={set("about")} rows={3} maxLength={4000} readOnly={ro} placeholder="Usage-based billing analytics for B2B SaaS finance teams." />
        <TextArea label="Who it's for" hint="The people and companies it serves." value={form.audience} onChange={set("audience")} rows={2} maxLength={4000} readOnly={ro} placeholder="Finance leads at B2B SaaS companies with 50 to 500 staff." />
        <TextArea label="Goals" hint="What you want from Outflare, with numbers and dates." value={form.goals} onChange={set("goals")} rows={2} maxLength={4000} readOnly={ro} placeholder="20 demos a month from US fintech by March." />
        <TextArea label="Voice" hint="How emails should sound: tone, length, sign-off, words to avoid." value={form.voice} onChange={set("voice")} rows={2} maxLength={4000} readOnly={ro} placeholder="Plain and short. No exclamation marks. Sign off with first name only." />
        <TextArea label="Rules" hint="Things agents must never do." value={form.rules} onChange={set("rules")} rows={2} maxLength={10000} readOnly={ro} placeholder="Never mention pricing in a first email. Never email .gov addresses." />
        <TextArea
          label="Notes"
          hint="Markdown. Anything else worth knowing: proof points, competitors, past results, how it relates to your other brands."
          value={form.notes}
          onChange={set("notes")}
          rows={6}
          maxLength={210000}
          readOnly={ro}
          className="font-mono text-[12px]"
          placeholder={"## Proof\n- Cut Globex's close from 9 days to 2\n\n## Competitors\n- ChartMogul, Baremetrics"}
        />
        <ErrorText>{error}</ErrorText>
        {!ro && (
          <div className="flex flex-wrap items-center gap-2">
            <Button type="submit" disabled={busy || !dirty}>{busy ? "Saving…" : "Save"}</Button>
            {dirty && <Button type="button" variant="ghost" onClick={() => { setForm(toForm(c)); setError(""); }}>Discard changes</Button>}
            {dirty && !error && <span className="text-xs text-amber-700 dark:text-amber-400">Unsaved changes</span>}
          </div>
        )}
      </form>
    </Card>
  );
}

// The brand's logo and colours, which its consent banner's brand look
// (and anything else on brand) starts from.
function LookCard({ ws, context: c, canManage, onSaved }: { ws: string; context: BrandContext; canManage: boolean; onSaved: () => void }) {
  const [logo, setLogo] = useState(c.logo_url);
  const [colors, setColors] = useState(c.colors);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setLogo(c.logo_url);
    setColors(c.colors);
  }, [c.logo_url, c.colors]);
  const dirty = logo !== c.logo_url || JSON.stringify(colors) !== JSON.stringify(c.colors);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { error } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}", { params: { path: { ws, brand: c.brand_id } }, body: { logo_url: logo.trim(), colors } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast("Saved.");
    onSaved();
  }

  const labels = { primary: "Main colour", background: "Background", text: "Text" } as const;
  const placeholders = { primary: "#1d4ed8", background: "#ffffff", text: "#111827" } as const;
  return (
    <Card title="Logo and colours" description="This brand's website consent banner and help centre use these in their brand look.">
      <form onSubmit={save} className="space-y-3">
        <fieldset disabled={!canManage} className="space-y-3">
          <Input label="Logo" value={logo} onChange={(e) => setLogo(e.target.value)} placeholder="https://northpeak.example/logo.svg" hint="The https address of an image, such as an SVG or PNG on your site." />
          <div className="grid gap-3 sm:grid-cols-3">
            {(Object.keys(labels) as (keyof typeof labels)[]).map((k) => (
              <div key={k} className="flex items-end gap-2">
                <div className="flex-1">
                  <Input label={labels[k]} value={colors[k]} onChange={(e) => setColors({ ...colors, [k]: e.target.value })} placeholder={placeholders[k]} />
                </div>
                <span aria-hidden className="mb-1 size-6 shrink-0 rounded border" style={{ background: /^#([0-9a-f]{3}){1,2}$/i.test(colors[k]) ? colors[k] : "transparent" }} />
              </div>
            ))}
          </div>
        </fieldset>
        <ErrorText>{error}</ErrorText>
        {canManage && <Button type="submit" disabled={busy || !dirty}>{busy ? "Saving…" : "Save"}</Button>}
      </form>
    </Card>
  );
}
