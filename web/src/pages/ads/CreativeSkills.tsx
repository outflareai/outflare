// Skills and prompts: a brand's saved recipes for one kind of ad. The list
// shows each skill with its AI model, whether a split test between prompts
// is running and an On/Off switch. A skill's page edits its prompts and
// settings; saving makes a new version, and every earlier version stays to
// go back to. A creative is made from here, on the brand's own AI account.

import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { Brand, CreativeSkill, CreativeSkillVersion, components } from "@outflare/sdk";
import { Switch } from "@/components/ui/switch";
import { api, errorMessage } from "../../api";
import { track } from "../../lib/analytics";
import { ago } from "../../format";
import { Badge, Button, Empty, ErrorText, Loading, confirmAction, field, toast } from "../../ui";

type Prompt = { key: string; name: string; picture: string; copy: string; share: number };
type Draft = { provider: "" | "openai" | "fal"; model: string; sizes: string[]; rules: string; prompts: Prompt[] };

const letters = ["A", "B", "C", "D"];
const allSizes = ["1:1", "4:5", "9:16", "16:9"];
// Filled in by Outflare for every creative; the rest are asked for.
const builtIn = ["persona", "pain", "hook", "brand"];
const providers: { id: Draft["provider"]; label: string }[] = [{ id: "", label: "The first account connected" }, { id: "openai", label: "OpenAI" }, { id: "fal", label: "fal.ai" }];
const providerName = (id: string) => providers.find((p) => p.id === id && id)?.label ?? "";
const fillInsOf = (...texts: string[]) => [...new Set(texts.flatMap((t) => [...t.matchAll(/\{([A-Za-z][A-Za-z0-9 _]{0,39})\}/g)].map((m) => m[1].trim().toLowerCase().replace(/ /g, "_"))))];
const draftOf = (v: CreativeSkillVersion): Draft => ({ provider: (v.provider as Draft["provider"]) || "", model: v.model, sizes: v.sizes, rules: v.rules, prompts: v.variants.map((p) => ({ key: p.key, name: p.name, picture: p.picture, copy: p.copy, share: p.share })) });
const same = (a: Draft, b: Draft) => JSON.stringify(a) === JSON.stringify(b);
const testing = (prompts: { share: number }[]) => prompts.filter((p) => p.share > 0).length > 1;
const splitLabel = (s: CreativeSkill) => (s.split_test ? s.current.variants.filter((p) => p.share > 0).map((p) => p.key).join(" against ") : "");

const th = "whitespace-nowrap px-3 py-2.5 text-left text-[12px] font-medium text-muted-foreground";
const td = "px-3 py-3 align-middle text-[13px]";
const area = `${field} w-full font-mono text-[12px] leading-6`;

function Panel({ title, sub, action, children }: { title: string; sub?: string; action?: ReactNode; children: ReactNode }) {
  return (
    <section className="rounded-xl border">
      <header className="flex flex-wrap items-center justify-between gap-2 border-b px-5 py-3">
        <div><h3 className="text-[15px] font-medium">{title}</h3>{sub && <p className="text-[12px] text-muted-foreground">{sub}</p>}</div>
        {action}
      </header>
      <div className="p-5">{children}</div>
    </section>
  );
}
function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className="grid items-start gap-2 sm:grid-cols-[120px_1fr]">
      <span className="pt-1.5 text-[13px]">{label}</span>
      <span className="flex flex-wrap items-center gap-2">{children}{hint && <span className="basis-full text-[12px] text-muted-foreground">{hint}</span>}</span>
    </div>
  );
}
const FillIns = ({ keys }: { keys: string[] }) => keys.length === 0 ? null : (
  <div className="flex flex-wrap items-center gap-2 text-[12px] text-muted-foreground">Fill-ins this prompt uses:{keys.map((k) => <span key={k} className="rounded border px-1.5 py-0.5 font-mono text-[11px] text-foreground">{`{${k}}`}</span>)}</div>
);

type Props = { brand: Brand; canManage: boolean; item: string; open: (id: string) => void; back: () => void; openCreative?: (id: string) => void; onConnectors?: () => void };

export function CreativeSkills({ brand, canManage, item, open, back, openCreative, onConnectors }: Props) {
  const [rows, setRows] = useState<CreativeSkill[] | null>(null);
  const [error, setError] = useState("");
  const [archived, setArchived] = useState(false);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/creative-skills", { params: { path: { brand: brand.id }, query: archived ? { archived: true } : {} } });
    setError(error ? errorMessage(error) : "");
    setRows(data?.data ?? []);
  }, [brand.id, archived]);
  useEffect(() => { setRows(null); void load(); }, [load]);

  const change = useCallback(async (s: CreativeSkill, body: { enabled?: boolean; archived?: boolean }, said: string) => {
    const { error } = await api.PATCH("/v1/brands/{brand}/creative-skills/{skill}", { params: { path: { brand: s.brand_id, skill: s.id } }, body });
    if (error) { toast(errorMessage(error), "error"); return false; }
    toast(said);
    await load();
    return true;
  }, [load]);
  const turn = (s: CreativeSkill, on: boolean) => change(s, { enabled: on }, on ? `${s.name} is on.` : `${s.name} is off. It makes nothing until you turn it on.`);

  if (item === "new") return <NewSkill brand={brand} onConnectors={onConnectors} back={back} created={async (id) => { await load(); open(id); }} />;
  if (item) {
    if (!rows) return <Loading />;
    const s = rows.find((r) => r.id === item);
    if (!s) return <Empty title="This skill is gone" action={<Button onClick={back}>Back to Skills and prompts</Button>} />;
    return <SkillPage key={s.id} s={s} brand={brand} onConnectors={onConnectors} canManage={canManage} back={back} reload={load} turn={turn} change={change} openCreative={openCreative} />;
  }

  const all = rows ?? [];
  const tests = all.filter((s) => s.split_test).length;
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="text-lg font-medium">{rows ? `${all.length === 1 ? "1 skill" : `${all.length} skills`}${archived ? " archived" : `, ${tests === 1 ? "1 split test" : `${tests} split tests`} running`}` : "Skills and prompts"}</h2>
          <p className="max-w-3xl text-[13px] text-muted-foreground">A skill is a saved recipe for one kind of ad: the prompt, the AI model and the sizes. A split test runs two to four prompts side by side, so you can see which makes better ads.</p>
        </div>
        <div className="flex items-center gap-3">
          <label className="flex items-center gap-2 text-[13px] text-muted-foreground"><input type="checkbox" aria-label="Show archived" checked={archived} onChange={(e) => setArchived(e.target.checked)} />Show archived</label>
          {canManage && <Button onClick={() => open("new")}>New skill</Button>}
        </div>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {!rows ? <Loading /> : all.length === 0 ? (
        archived ? <Empty title="Nothing is archived" /> : <Empty title="No skills yet" action={canManage ? <Button onClick={() => open("new")}>New skill</Button> : undefined}>Add a skill for one kind of ad, such as a headline over one picture. Creatives are then made from it.</Empty>
      ) : (
        <div className="overflow-x-auto rounded-xl border">
          <table className="w-full">
            <thead><tr className="border-b bg-muted/40"><th className={th}>Skill</th><th className={th}>Makes</th><th className={th}>AI model</th><th className={th}>Split test</th><th className={`${th} text-right`}>Creatives made</th><th className={th}>On</th><th className={th} /></tr></thead>
            <tbody>{all.map((s) => (
              <tr key={s.id} className="cursor-pointer border-b last:border-0 hover:bg-muted/30" onClick={() => open(s.id)}>
                <td className={td}>
                  <button type="button" aria-label={s.name} onClick={(e) => { e.stopPropagation(); open(s.id); }} className="text-left"><span className="text-[14px] font-medium hover:underline">{s.name}</span> <span className="font-mono text-[11px] text-muted-foreground">v{s.version}</span></button>
                  {s.description && <div className="text-[12px] text-muted-foreground">{s.description}</div>}
                </td>
                <td className={td}><Badge tone="grey">{s.kind === "video" ? "Video" : "Image"}</Badge></td>
                <td className={td}><span className="font-mono text-[12px]">{s.current.model}</span>{providerName(s.current.provider) && <span className="block text-[12px] text-muted-foreground">{providerName(s.current.provider)}</span>}</td>
                <td className={td}>{s.split_test ? <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-emerald-500" />{splitLabel(s)}</span> : <span className="text-muted-foreground">No test running</span>}</td>
                <td className={`${td} text-right font-mono`}>{s.creatives}</td>
                <td className={td} onClick={(e) => e.stopPropagation()}>{s.archived_at ? <span className="text-muted-foreground">Archived</span> : <Switch checked={s.enabled} disabled={!canManage} onCheckedChange={(on) => void turn(s, on)} aria-label={`${s.name} on or off`} />}</td>
                <td className={`${td} text-right`} onClick={(e) => e.stopPropagation()}>{s.archived_at ? canManage && <Button variant="ghost" onClick={() => void change(s, { archived: false }, `${s.name} is back.`)}>Restore</Button> : <Button variant="ghost" onClick={() => open(s.id)}>{canManage ? "Edit" : "Open"}</Button>}</td>
              </tr>
            ))}</tbody>
          </table>
        </div>
      )}
      <p className="text-[12px] text-muted-foreground">Source: skills saved for {brand.name}. Every AI model runs on your own account with that provider and is billed by them, not by Outflare.</p>
    </div>
  );
}

// The prompts of a skill, edited in place: one tab per prompt.
function Prompts({ draft, set, sel, setSel, readOnly }: { draft: Draft; set: (d: Draft) => void; sel: string; setSel: (k: string) => void; readOnly: boolean }) {
  const p = draft.prompts.find((x) => x.key === sel) ?? draft.prompts[0];
  const edit = (patch: Partial<Prompt>) => set({ ...draft, prompts: draft.prompts.map((x) => (x.key === p.key ? { ...x, ...patch } : x)) });
  const add = () => {
    const key = letters[draft.prompts.length];
    // A new prompt starts as a copy, with an even share for everyone.
    const share = Math.floor(100 / (draft.prompts.length + 1));
    set({ ...draft, prompts: [...draft.prompts.map((x) => ({ ...x, share })), { key, name: "", picture: p.picture, copy: p.copy, share }] });
    setSel(key);
  };
  const remove = () => {
    const rest = draft.prompts.filter((x) => x.key !== p.key).map((x, i) => ({ ...x, key: letters[i] }));
    set({ ...draft, prompts: rest.length === 1 ? [{ ...rest[0], share: 100 }] : rest });
    setSel("A");
  };
  const total = draft.prompts.reduce((n, x) => n + x.share, 0);
  return (
    <Panel
      title={testing(draft.prompts) ? "Prompts: a split test is running" : "Prompt"}
      sub={testing(draft.prompts) ? "Each new creative is made with the prompt furthest behind its share. To end the test, keep only the winning prompt." : "Add a second prompt to test it against this one."}
      action={!readOnly && <div className="flex gap-2">{draft.prompts.length < 4 && <Button variant="ghost" onClick={add}>Add prompt {letters[draft.prompts.length]}</Button>}{draft.prompts.length > 1 && <Button variant="ghost" onClick={remove}>Remove prompt {p.key}</Button>}</div>}
    >
      <div className="space-y-4">
        {draft.prompts.length > 1 && (
          <div role="group" aria-label="Prompt" className="flex flex-wrap gap-2">
            {draft.prompts.map((x) => <button key={x.key} type="button" aria-pressed={x.key === p.key} onClick={() => setSel(x.key)} className={`rounded-lg border px-3 py-2 text-left text-[13px] ${x.key === p.key ? "border-foreground" : "text-muted-foreground"}`}><span className="mr-2 font-mono">{x.key}</span>{x.name || "No name"}<span className="ml-3 font-mono text-[12px]">{x.share}%</span></button>)}
          </div>
        )}
        <div className="grid gap-3 sm:grid-cols-[1fr_140px]">
          <label className="flex flex-col gap-1 text-[13px]">Name<input aria-label={`Prompt ${p.key} name`} maxLength={100} disabled={readOnly} placeholder="Problem first" value={p.name} onChange={(e) => edit({ name: e.target.value })} className={`${field} w-full`} /></label>
          {draft.prompts.length > 1 && <label className="flex flex-col gap-1 text-[13px]">Share of new creatives<span className="flex items-center gap-1"><input aria-label={`Prompt ${p.key} share`} type="number" min={0} max={100} disabled={readOnly} value={p.share} onChange={(e) => edit({ share: Math.max(0, Math.min(100, Math.round(Number(e.target.value) || 0))) })} className={`${field} w-20`} />%</span></label>}
        </div>
        {draft.prompts.length > 1 && total !== 100 && <p className="text-[12px] text-muted-foreground">The shares add up to {total}, not 100. That is fine: they are compared with each other. 0 takes a prompt out of the test.</p>}
        <label className="flex flex-col gap-1 text-[13px]">Picture<span className="text-[12px] text-muted-foreground">What the AI model is asked to make. Words in curly brackets, such as {"{persona}"} or {"{pain}"}, are filled in for each creative.</span><textarea aria-label={`Prompt ${p.key} picture`} rows={6} maxLength={8000} disabled={readOnly} value={p.picture} onChange={(e) => edit({ picture: e.target.value })} className={area} /></label>
        <label className="flex flex-col gap-1 text-[13px]">Headline and button<span className="text-[12px] text-muted-foreground">How to write the words. Outflare lays them over the picture, so they can change without a new picture.</span><textarea aria-label={`Prompt ${p.key} headline and button`} rows={4} maxLength={4000} disabled={readOnly} value={p.copy} onChange={(e) => edit({ copy: e.target.value })} className={area} /></label>
        <div className="flex flex-wrap items-center justify-between gap-2"><FillIns keys={fillInsOf(p.picture, p.copy)} /><Button variant="ghost" onClick={() => { void navigator.clipboard.writeText([p.picture, p.copy].filter(Boolean).join("\n\n")); toast("Prompt copied."); }}>Copy prompt</Button></div>
      </div>
    </Panel>
  );
}

function Settings({ kind, draft, set, readOnly, onConnectors }: { kind: "image" | "video"; draft: Draft; set: (d: Draft) => void; readOnly: boolean; onConnectors?: () => void }) {
  const toggle = (s: string) => set({ ...draft, sizes: draft.sizes.includes(s) ? draft.sizes.filter((x) => x !== s) : allSizes.filter((x) => x === s || draft.sizes.includes(x)) });
  return (
    <Panel title="Skill settings" sub="These apply to every prompt in this skill.">
      <div className="space-y-4">
        <Field label="Makes"><Badge tone="grey">{kind === "video" ? "Video" : "Image"}</Badge></Field>
        <Field label="AI account" hint={<>Your own account with the provider makes and bills every creative. {onConnectors && <button type="button" className="underline" onClick={onConnectors}>Connect an account</button>}</>}>
          <select aria-label="AI account" disabled={readOnly} value={draft.provider} onChange={(e) => set({ ...draft, provider: e.target.value as Draft["provider"] })} className={`${field} w-full`}>{providers.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}</select>
        </Field>
        <Field label="AI model" hint="The provider's own id for the model: an OpenAI image model, or a fal.ai endpoint such as owner/app/path.">
          <input aria-label="AI model" maxLength={200} disabled={readOnly} value={draft.model} onChange={(e) => set({ ...draft, model: e.target.value })} className={`${field} w-full font-mono`} />
        </Field>
        <Field label="Sizes" hint={kind === "image" ? "Extra sizes cost nothing: the same picture is cropped and the words are laid out again." : "The shapes the video is wanted in."}>
          {allSizes.map((s) => <button key={s} type="button" aria-pressed={draft.sizes.includes(s)} disabled={readOnly} onClick={() => toggle(s)} className={`rounded-md border px-2 py-1 font-mono text-[11px] ${draft.sizes.includes(s) ? "border-foreground" : "text-muted-foreground"}`}>{draft.sizes.includes(s) ? "✓ " : ""}{s}</button>)}
        </Field>
        <Field label="Rules" hint="Added to every prompt, such as what must never be in the picture.">
          <textarea aria-label="Rules" rows={3} maxLength={4000} disabled={readOnly} value={draft.rules} onChange={(e) => set({ ...draft, rules: e.target.value })} className={`${field} w-full`} />
        </Field>
      </div>
    </Panel>
  );
}

const recipe = (d: Draft) => ({ provider: d.provider, model: d.model.trim(), sizes: d.sizes, rules: d.rules.trim(), variants: d.prompts.map((p) => ({ name: p.name.trim(), picture: p.picture, copy: p.copy, share: p.share })) });
const wrong = (d: Draft) => (!d.model.trim() ? "Say which AI model makes it." : d.prompts.some((p) => !p.picture.trim()) ? "Every prompt needs its Picture filled in." : d.prompts.every((p) => p.share === 0) ? "At least one prompt needs a share above 0." : "");

function SkillPage({ s, brand, onConnectors, canManage, back, reload, turn, change, openCreative }: { s: CreativeSkill; brand: Brand; onConnectors?: () => void; canManage: boolean; back: () => void; reload: () => Promise<void>; turn: (s: CreativeSkill, on: boolean) => Promise<boolean>; change: (s: CreativeSkill, body: { archived?: boolean }, said: string) => Promise<boolean>; openCreative?: (id: string) => void }) {
  const saved = draftOf(s.current);
  const [draft, setDraft] = useState<Draft>(saved);
  const [sel, setSel] = useState("A");
  const [note, setNote] = useState("");
  const [about, setAbout] = useState<null | { name: string; description: string }>(null);
  const [busy, setBusy] = useState(false);
  const [versions, setVersions] = useState<CreativeSkillVersion[] | null>(null);
  const path = { brand: s.brand_id, skill: s.id };
  const loadVersions = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/creative-skills/{skill}/versions", { params: { path: { brand: s.brand_id, skill: s.id } } });
    setVersions(data?.data ?? []);
  }, [s.brand_id, s.id, s.version]);
  useEffect(() => { void loadVersions(); }, [loadVersions]);
  // A saved version is the new starting point.
  useEffect(() => { setDraft(draftOf(s.current)); setNote(""); }, [s.version]);

  const dirty = !same(draft, saved);
  const problem = wrong(draft);
  const next = s.version + 1;
  async function patch(body: components["schemas"]["CreativeSkillUpdate"], said: string) {
    setBusy(true);
    const { error } = await api.PATCH("/v1/brands/{brand}/creative-skills/{skill}", { params: { path }, body });
    setBusy(false);
    if (error) { toast(errorMessage(error), "error"); return false; }
    toast(said);
    await reload();
    return true;
  }
  const cancel = () => { track("ui.creative_skill_edit_cancelled", { changed: (Object.keys(draft) as (keyof Draft)[]).filter((k) => JSON.stringify(draft[k]) !== JSON.stringify(saved[k])).length }); setDraft(saved); setNote(""); setSel("A"); };
  const goBack = async (v: CreativeSkillVersion) => {
    if (!(await confirmAction(`Go back to v${v.version}?`, `Its prompts and settings are saved again as v${next}. Nothing is lost: v${s.version} stays in the list.`, `Go back to v${v.version}`, false))) return;
    await patch({ restore_version: v.version, version: s.version }, `v${v.version} is saved again as v${next}.`);
  };
  const archive = async () => {
    if (!(await confirmAction(`Archive ${s.name}?`, "It makes nothing while archived. Its creatives and versions are kept, and you can restore it under Show archived.", "Archive", false))) return;
    if (await change(s, { archived: true }, `${s.name} is archived.`)) back();
  };
  const readOnly = !canManage || !!s.archived_at;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <button type="button" onClick={back} className="text-[13px] text-muted-foreground hover:text-foreground">← Skills and prompts</button>
          {about ? (
            <form className="mt-1 flex max-w-xl flex-col gap-2" onSubmit={async (e) => { e.preventDefault(); if (about.name.trim() && await patch({ name: about.name.trim(), description: about.description.trim() }, "Saved.")) setAbout(null); }}>
              <label className="flex flex-col gap-1 text-[13px]">Name<input autoFocus aria-label="Skill name" maxLength={100} value={about.name} onChange={(e) => setAbout({ ...about, name: e.target.value })} className={`${field} w-full`} /></label>
              <label className="flex flex-col gap-1 text-[13px]">What the ad is, in one line<input aria-label="Skill description" maxLength={500} value={about.description} onChange={(e) => setAbout({ ...about, description: e.target.value })} className={`${field} w-full`} /></label>
              <div className="flex gap-2"><Button type="submit" disabled={busy || !about.name.trim()}>Save</Button><Button type="button" variant="ghost" onClick={() => setAbout(null)}>Cancel</Button></div>
            </form>
          ) : (
            <>
              <h2 className="mt-1 flex flex-wrap items-center gap-2 text-xl font-medium">{s.name} <Badge tone="grey">v{s.version}</Badge>{s.archived_at && <Badge tone="grey">Archived</Badge>}{!readOnly && <button type="button" onClick={() => setAbout({ name: s.name, description: s.description })} className="rounded-md border px-2 py-0.5 text-[12px] font-normal text-muted-foreground hover:text-foreground">✎ Rename</button>}</h2>
              <p className="text-[13px] text-muted-foreground">{s.description ? `${s.description}. ` : ""}{s.creatives === 1 ? "1 creative made." : `${s.creatives} creatives made.`}</p>
            </>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {!s.archived_at && <label className="mr-2 flex items-center gap-2 text-[13px]"><Switch checked={s.enabled} disabled={!canManage} onCheckedChange={(on) => void turn(s, on)} aria-label="Skill on or off" />Skill {s.enabled ? "on" : "off"}</label>}
          {canManage && (s.archived_at ? <Button variant="ghost" onClick={async () => { await change(s, { archived: false }, `${s.name} is back.`); }}>Restore</Button> : <Button variant="ghost" onClick={archive}>Archive</Button>)}
        </div>
      </div>

      {dirty && !readOnly && (
        <form className="sticky top-2 z-10 flex flex-wrap items-center gap-3 rounded-xl border bg-background px-4 py-3 shadow-sm" onSubmit={async (e) => { e.preventDefault(); if (!problem) await patch({ ...recipe(draft), note: note.trim(), version: s.version }, `Saved as v${next}.`); }}>
          <span className="text-[13px] font-medium">You changed this skill.</span>
          <input aria-label="What changed and why" maxLength={500} placeholder="What changed and why (shown in the list of versions)" value={note} onChange={(e) => setNote(e.target.value)} className={`${field} min-w-64 flex-1`} />
          {problem && <span className="text-[12px] text-red-600">{problem}</span>}
          <Button type="submit" disabled={busy || !!problem}>Save as v{next}</Button><Button type="button" variant="ghost" disabled={busy} onClick={cancel}>Cancel</Button>
        </form>
      )}

      <div className="grid items-start gap-5 xl:grid-cols-2">
        <Prompts draft={draft} set={setDraft} sel={sel} setSel={setSel} readOnly={readOnly} />
        <Settings kind={s.kind} draft={draft} set={setDraft} readOnly={readOnly} onConnectors={onConnectors} />
      </div>

      {canManage && !s.archived_at && <MakeOne s={s} brand={brand} dirty={dirty} done={reload} openCreative={openCreative} />}

      <Panel title="Versions" sub="Every saved change to the prompts or settings is a new version, so you can see what each one made and go back.">
        {!versions ? <Loading /> : (
          <table className="w-full">
            <thead><tr className="border-b"><th className={th}>Version</th><th className={th}>Saved</th><th className={th}>What changed</th><th className={th}>Prompts</th><th className={`${th} text-right`}>Creatives made</th><th className={th} /></tr></thead>
            <tbody>{versions.map((v) => (
              <tr key={v.version} className="border-b last:border-0">
                <td className={`${td} font-mono`}>v{v.version}{v.version === s.version && <span className="ml-2 font-sans text-[11px] text-muted-foreground">current</span>}</td>
                <td className={`${td} whitespace-nowrap text-muted-foreground`}>{ago(v.created_at)}</td>
                <td className={td}>{v.note || <span className="text-muted-foreground">No note</span>}</td>
                <td className={`${td} font-mono text-[12px]`}>{v.variants.map((p) => p.key).join(", ")} · {v.model}</td>
                <td className={`${td} text-right font-mono`}>{v.creatives}</td>
                <td className={`${td} text-right`}>{!readOnly && v.version !== s.version && <Button variant="ghost" disabled={busy} onClick={() => void goBack(v)}>Go back to v{v.version}</Button>}</td>
              </tr>
            ))}</tbody>
          </table>
        )}
      </Panel>
    </div>
  );
}

// Make one creative from the skill as saved: who it is for, the words
// laid over the picture, and a word for each of the prompt's own fill-ins.
function MakeOne({ s, brand, dirty, done, openCreative }: { s: CreativeSkill; brand: Brand; dirty: boolean; done: () => Promise<void>; openCreative?: (id: string) => void }) {
  const [open, setOpen] = useState(false);
  const [variant, setVariant] = useState("");
  const [f, setF] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const prompts = s.current.variants.filter((p) => p.share > 0 || s.current.variants.length === 1);
  // Without a pick, any prompt may be used, so ask for every prompt's fill-ins.
  const extra = [...new Set((variant ? s.current.variants.filter((p) => p.key === variant) : prompts).flatMap((p) => p.fill_ins))].filter((k) => !builtIn.includes(k));
  const v = (k: string) => f[k] ?? "";
  const input = (k: string, label: string, max: number, hint?: string) => <label className="flex flex-col gap-1 text-[13px]">{label}<input aria-label={label} maxLength={max} value={v(k)} onChange={(e) => setF({ ...f, [k]: e.target.value })} className={`${field} w-full`} />{hint && <span className="text-[12px] text-muted-foreground">{hint}</span>}</label>;
  const missing = !v("persona").trim() || !v("pain").trim() || extra.some((k) => !v(`fill:${k}`).trim());
  async function make() {
    setBusy(true); setError("");
    const { data, error } = await api.POST("/v1/brands/{brand}/creatives", { params: { path: { brand: s.brand_id } }, body: {
      skill_id: s.id, ...(variant ? { variant } : {}), persona: v("persona").trim(), pain: v("pain").trim(), hook: v("hook").trim(), headline: v("headline").trim(), subline: v("subline").trim(), cta: v("cta").trim(),
      fill_ins: Object.fromEntries(extra.map((k) => [k, v(`fill:${k}`).trim()])),
    } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`${data && "code" in data ? data.code : "The creative"} is being made. It will wait in Creatives for your approval.`);
    setOpen(false); setF({});
    await done();
    if (data && "id" in data) openCreative?.(data.id);
  }
  if (!open) return (
    <Panel title="Make a creative" sub={`One ${s.kind === "video" ? "video" : "picture"} from this skill, on ${brand.name}'s own AI account. The provider bills you for it.`} action={<Button disabled={!s.enabled || dirty} onClick={() => setOpen(true)}>Make a creative</Button>}>
      <p className="text-[13px] text-muted-foreground">{!s.enabled ? "The skill is off. Turn it on to make creatives." : dirty ? "Save or cancel your changes first: a creative is made from the saved version." : `Made with v${s.version}. It then waits in Creatives for a person's approval.`}</p>
    </Panel>
  );
  return (
    <Panel title="Make a creative" sub={`Made with v${s.version} on ${brand.name}'s own AI account. The provider bills you for each one.`}>
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (!missing) void make(); }}>
        <div className="grid gap-4 md:grid-cols-3">
          {input("persona", "Persona", 200, "Who the ad is for.")}
          {input("pain", "Pain", 200, "The one problem it is about.")}
          {input("hook", "Hook (optional)", 200, "How it opens, in a few words.")}
        </div>
        {s.kind === "image" && <div className="grid gap-4 md:grid-cols-3">
          {input("headline", "Headline", 200, "Laid over the picture. You can change it later for free.")}
          {input("subline", "Second line", 300)}
          {input("cta", "Button", 60)}
        </div>}
        {extra.length > 0 && <div className="grid gap-4 md:grid-cols-3">{extra.map((k) => <label key={k} className="flex flex-col gap-1 text-[13px]"><span className="font-mono">{`{${k}}`}</span><input aria-label={`Fill-in ${k}`} maxLength={1000} value={v(`fill:${k}`)} onChange={(e) => setF({ ...f, [`fill:${k}`]: e.target.value })} className={`${field} w-full`} /></label>)}</div>}
        {prompts.length > 1 && <label className="flex max-w-sm flex-col gap-1 text-[13px]">Prompt<select aria-label="Prompt to use" value={variant} onChange={(e) => setVariant(e.target.value)} className={`${field} w-full`}><option value="">The one furthest behind its share (keeps the test even)</option>{prompts.map((p) => <option key={p.key} value={p.key}>{p.key}{p.name ? `: ${p.name}` : ""}</option>)}</select></label>}
        {error && <ErrorText>{error}</ErrorText>}
        <div className="flex items-center gap-2"><Button type="submit" disabled={busy || missing}>Make 1 creative</Button><Button type="button" variant="ghost" disabled={busy} onClick={() => { setOpen(false); setError(""); }}>Cancel</Button>{missing && <span className="text-[12px] text-muted-foreground">Fill in the persona, the pain{extra.length ? " and every fill-in" : ""}.</span>}</div>
      </form>
    </Panel>
  );
}

function NewSkill({ brand, onConnectors, back, created }: { brand: Brand; onConnectors?: () => void; back: () => void; created: (id: string) => Promise<void> }) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [kind, setKind] = useState<"image" | "video">("image");
  const [draft, setDraft] = useState<Draft>({ provider: "", model: "", sizes: ["1:1", "4:5"], rules: "", prompts: [{ key: "A", name: "", picture: "", copy: "", share: 100 }] });
  const [sel, setSel] = useState("A");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const problem = !name.trim() ? "Give the skill a name." : wrong(draft);
  async function save() {
    setBusy(true); setError("");
    const { data, error } = await api.POST("/v1/brands/{brand}/creative-skills", { params: { path: { brand: brand.id } }, body: { name: name.trim(), description: description.trim(), kind, ...recipe(draft) } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`${name.trim()} is saved. Nothing was made or paid for.`);
    if (data && "id" in data) await created(data.id);
  }
  return (
    <form className="space-y-5" onSubmit={(e) => { e.preventDefault(); if (!problem) void save(); }}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <button type="button" onClick={back} className="text-[13px] text-muted-foreground hover:text-foreground">← Skills and prompts</button>
          <h2 className="mt-1 text-xl font-medium">New skill</h2>
          <p className="text-[13px] text-muted-foreground">A recipe for one kind of ad for {brand.name}. Saving it makes nothing and costs nothing.</p>
        </div>
        <div className="flex items-center gap-2">{problem && <span className="text-[12px] text-muted-foreground">{problem}</span>}<Button type="submit" disabled={busy || !!problem}>Save skill</Button><Button type="button" variant="ghost" onClick={back}>Cancel</Button></div>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      <Panel title="What it is">
        <div className="grid gap-4 md:grid-cols-[1fr_2fr_160px]">
          <label className="flex flex-col gap-1 text-[13px]">Name<input autoFocus aria-label="Skill name" maxLength={100} placeholder="Static ad" value={name} onChange={(e) => setName(e.target.value)} className={`${field} w-full`} /></label>
          <label className="flex flex-col gap-1 text-[13px]">What the ad is, in one line<input aria-label="Skill description" maxLength={500} placeholder="One headline over one picture" value={description} onChange={(e) => setDescription(e.target.value)} className={`${field} w-full`} /></label>
          <label className="flex flex-col gap-1 text-[13px]">Makes<select aria-label="Makes" value={kind} onChange={(e) => setKind(e.target.value as "image" | "video")} className={`${field} w-full`}><option value="image">Image</option><option value="video">Video</option></select></label>
        </div>
      </Panel>
      <div className="grid items-start gap-5 xl:grid-cols-2">
        <Prompts draft={draft} set={setDraft} sel={sel} setSel={setSel} readOnly={false} />
        <Settings kind={kind} draft={draft} set={setDraft} readOnly={false} onConnectors={onConnectors} />
      </div>
    </form>
  );
}
