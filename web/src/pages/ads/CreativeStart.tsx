// Start here: the first run of the Creatives tab. It shows what a brand
// still needs before its first ad (files, an AI account, a skill, a
// creative), starter skills that are added with one click, and a box where
// a person says the ad they want and their own AI model drafts the skill
// for them to read first, so nobody begins from an empty page.
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import type { Brand, Persona } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Badge, Button, ErrorText, Loading, field, toast } from "../../ui";

type Provider = { provider: string; name: string; connected: boolean; kinds: string[] };
type State = { files: number; providers: Provider[]; image: boolean; video: boolean; voice: boolean; storage: boolean; skills: { id: string; name: string; model: string }[]; creatives: number; approved: number; personas: Persona[] };

// How often a draft being written is asked for; tests shorten it.
const pollMs = (globalThis as { outflareTestPollMs?: number }).outflareTestPollMs ?? 1500;

const kindWord: Record<string, string> = { image: "pictures", video: "video", voice: "voice" };

// The starter skills. Each is two prompts sharing the creatives between
// them, so the first ads already say which opening works.
const rules = "Looks like a real photo taken on a phone or a plain screenshot, never like stock or AI art. No text, letters, numbers or logos inside the picture. One clear subject, with empty space at the top for a headline.";
export const starterSkills = [
  {
    id: "problem-first", name: "Problem first", line: "A person stuck on the pain, and a headline that names it as a question.",
    variants: [
      { name: "At the desk", picture: "A {persona} at a cluttered desk late in the evening, looking at a laptop with a tired, annoyed face because of {pain}. Natural room light, slightly off-centre, shot on a phone by a colleague.", copy: "Name {pain} as a short question the {persona} would ask out loud. At most 6 words. Second line: what {brand} changes, in plain words, using {proof}.", share: 50 },
      { name: "Close on the screen", picture: "Over the shoulder of a {persona}, close on a laptop screen that shows something going wrong because of {pain}, the words too blurred to read. One hand on the forehead. Daylight, shot on a phone.", copy: "Say what {pain} costs the {persona}, in at most 6 words. Second line: how {brand} ends it, using {proof}.", share: 50 },
    ],
  },
  {
    id: "one-number", name: "One number", line: "Your product on screen and one proof number as the headline.",
    variants: [
      { name: "The number first", picture: "A clean photo of a laptop on a desk showing a simple dashboard with one large calm area in the brand's colour, a coffee cup beside it, daylight. The screen is blurred enough that no words can be read.", copy: "Lead with {proof} as one number or one short fact. At most 6 words. Second line: who it is for ({persona}) and the pain it removes ({pain}).", share: 50 },
      { name: "The person and the number", picture: "A relaxed {persona} leaning back from a laptop, half smiling, in a bright room. Shot on a phone from across the desk.", copy: "Lead with {proof}. At most 6 words. Second line: the pain that is gone ({pain}).", share: 50 },
    ],
  },
  {
    id: "before-after", name: "Before and after", line: "Two halves: the mess today, and the calm after.",
    variants: [
      { name: "Split picture", picture: "One photo split down the middle. Left half: a {persona} frustrated by {pain}, cool dim light, a messy desk. Right half: the same person and desk, tidy, warm daylight, at ease. Shot on a phone.", copy: "Headline: before and after in at most 6 words. Second line: what {brand} did in between, using {proof}.", share: 50 },
      { name: "Only the after", picture: "A {persona} closing a laptop at the end of the day, calm, with the work finished. Warm evening light, shot on a phone.", copy: "Say what the day looks like without {pain}. At most 6 words. Second line: {proof}.", share: 50 },
    ],
  },
] as const;

function Step({ n, title, why, done, next, action, children }: { n: number; title: string; why: string; done: boolean; next: boolean; action?: ReactNode; children: ReactNode }) {
  return (
    <div data-step={n} data-done={done} className={`flex flex-wrap items-start gap-4 border-b px-5 py-4 last:border-b-0 ${next ? "bg-muted/40" : ""}`}>
      <span className={`mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-full text-[13px] font-medium ${done ? "bg-foreground text-background" : "border"}`}>{done ? "✓" : n}</span>
      <div className="min-w-0 flex-1 space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[15px] font-medium">{title}</span>
          {done && <Badge tone="green">Done</Badge>}
          {next && <Badge tone="amber">Do this next</Badge>}
        </div>
        <p className="text-[13px] text-muted-foreground">{why}</p>
        <div className="text-[13px]">{children}</div>
      </div>
      {action}
    </div>
  );
}

type Draft = { name: string; summary: string; picture: string; copy: string };

type Props = { brand: Brand; canManage: boolean; open: (item: string) => void; onConnectors: () => void; openProfile?: () => void };

export function CreativeStart({ brand, canManage, open, onConnectors, openProfile }: Props) {
  const [state, setState] = useState<State | null>(null);
  const [error, setError] = useState("");
  const [model, setModel] = useState("");
  const [adding, setAdding] = useState("");
  const [shown, setShown] = useState("");
  const [said, setSaid] = useState("");
  const [writing, setWriting] = useState(false);
  const [sayError, setSayError] = useState<{ text: string; connect: boolean } | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  const alive = useRef(true);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const load = useCallback(async () => {
    const path = { brand: brand.id };
    const [files, providers, skills, creatives, personas] = await Promise.all([
      api.GET("/v1/brands/{brand}/assets", { params: { path } }),
      api.GET("/v1/brands/{brand}/generations/providers", { params: { path } }),
      api.GET("/v1/brands/{brand}/creative-skills", { params: { path } }),
      api.GET("/v1/brands/{brand}/creatives", { params: { path } }),
      api.GET("/v1/brands/{brand}/personas", { params: { path } }),
    ]);
    const failed = files.error ?? providers.error ?? skills.error ?? creatives.error;
    if (failed) return setError(errorMessage(failed));
    setError("");
    const by = new Map<string, Provider>();
    for (const k of providers.data?.data ?? []) for (const p of k.providers) {
      const have = by.get(p.provider) ?? { provider: p.provider, name: p.name, connected: p.connected, kinds: [] };
      have.kinds.push(k.kind); have.connected = have.connected || p.connected; by.set(p.provider, have);
    }
    const ready = (kind: string) => (providers.data?.data ?? []).some((k) => k.kind === kind && k.ready);
    const made = creatives.data?.data ?? [];
    const list = (skills.data?.data ?? []).map((s) => ({ id: s.id, name: s.name, model: s.kind === "image" ? s.current.model : "" }));
    setState({
      files: (files.data?.data ?? []).length, providers: [...by.values()], image: ready("image"), video: ready("video"), voice: ready("voice"), storage: providers.data?.storage !== false,
      skills: list, creatives: made.length, approved: made.filter((c) => c.status === "approved").length,
      // Personas are a help, never a need: a brand without them still starts.
      personas: personas.data?.data ?? [],
    });
    // The model a brand already uses for pictures is the one to offer;
    // there is no default of ours.
    setModel((m) => m || list.find((s) => s.model)?.model || "");
  }, [brand.id]);
  useEffect(() => { setState(null); void load(); }, [load]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!state) return <Loading />;

  const done = [state.files > 0, state.image, state.skills.length > 0, state.creatives > 0];
  const count = done.filter(Boolean).length;
  const next = done.indexOf(false);
  const nextWord = ["add your brand files", "connect an AI account", "pick a starter skill", "make your first creative"][next] ?? "";

  async function add(id: string) {
    const s = starterSkills.find((x) => x.id === id);
    if (!s) return;
    setAdding(id);
    const { data, error } = await api.POST("/v1/brands/{brand}/creative-skills", { params: { path: { brand: brand.id } }, body: { name: s.name, description: s.line, kind: "image", provider: "", model: model.trim(), sizes: ["1:1", "4:5"], rules, variants: s.variants.map((v) => ({ ...v })) } });
    setAdding("");
    if (error) return toast(errorMessage(error), "error");
    toast(`${s.name} is added. Nothing was made or paid for.`);
    if (data && "id" in data) open(`creative-skills/${data.id}`); else await load();
  }
  // The person's own AI model drafts the skill; nothing is made, and
  // nothing is saved until they have read it and press Save.
  async function write() {
    setWriting(true); setSayError(null); setDraft(null);
    const path = { brand: brand.id };
    const asked = await api.POST("/v1/brands/{brand}/creative-skill-drafts", { params: { path }, body: { description: said.trim() } });
    if (asked.error || !asked.data || !("id" in asked.data)) {
      setWriting(false);
      const code = (asked.error as { error?: { code?: string } } | undefined)?.error?.code ?? "";
      return setSayError({ text: asked.error ? errorMessage(asked.error) : "This is waiting for approval in a mission.", connect: code === "connector_needed" || code === "connector_auth" });
    }
    const id = asked.data.id;
    for (let i = 0; i < 80 && alive.current; i++) {
      await new Promise((r) => setTimeout(r, pollMs));
      const got = await api.GET("/v1/brands/{brand}/creative-skill-drafts/{draft}", { params: { path: { ...path, draft: id } } });
      if (got.error) { setWriting(false); return setSayError({ text: errorMessage(got.error), connect: false }); }
      if (got.data.status === "failed") { setWriting(false); return setSayError({ text: got.data.error || "The draft could not be written. Try again.", connect: false }); }
      if (got.data.status === "ready") { setWriting(false); return setDraft({ name: got.data.name, summary: got.data.summary, picture: got.data.picture, copy: got.data.copy }); }
    }
    setWriting(false);
    setSayError({ text: "Your AI model is taking longer than usual. Try again in a minute.", connect: false });
  }
  async function saveDraft() {
    if (!draft) return;
    setSaving(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/creative-skills", { params: { path: { brand: brand.id } }, body: { name: draft.name.trim(), description: draft.summary.trim(), kind: "image", provider: "", model: model.trim(), sizes: ["1:1", "4:5"], rules, variants: [{ name: "Prompt A", picture: draft.picture, copy: draft.copy, share: 100 }] } });
    setSaving(false);
    if (error) return toast(errorMessage(error), "error");
    toast(`${draft.name.trim()} is saved as a skill. Nothing was made or paid for.`);
    if (data && "id" in data) open(`creative-skills/${data.id}`); else await load();
  }
  const ideas = state.personas.filter((p) => p.pains.length > 0).slice(0, 4);

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-xl font-medium">{state.approved > 0 ? "You have made your first ad" : "Make your first ad"}</h2>
          <p className="text-[13px] text-muted-foreground">{state.approved > 0 ? "Everything below stays here if you want another starter skill." : "Four steps, about 15 minutes. Pictures are made on your own AI account, and you approve each one before it goes anywhere."}</p>
        </div>
        <div className="w-64" role="group" aria-label="Progress">
          <div className="flex justify-between text-[12px] text-muted-foreground"><span>{count} of 4 done</span>{nextWord && <span>Next: {nextWord}</span>}</div>
          <div className="mt-1 h-2 rounded-full bg-muted"><div className="h-2 rounded-full bg-foreground" style={{ width: `${count * 25}%` }} /></div>
        </div>
      </div>

      <section className="rounded-xl border" aria-label="Steps">
        <Step n={1} title="Add your brand files" why="Real logos and product screenshots are what stop an ad looking AI-made." done={done[0]} next={next === 0} action={<Button variant="ghost" onClick={() => open("brand-kit")}>{done[0] ? "Open Brand kit" : "Add files"}</Button>}>
          {done[0] ? `${state.files} ${state.files === 1 ? "file is" : "files are"} in the Brand kit.` : "Start with your logo and three or four screenshots of your product."}
        </Step>
        <Step n={2} title="Connect your AI accounts" why="Pictures, video and voice are made on your own accounts, and the provider bills you. Outflare has none of its own." done={done[1]} next={next === 1}>
          <span className="flex flex-wrap gap-2">
            {state.providers.map((p) => (
              <span key={p.provider} className="inline-flex items-center gap-2 rounded-md border px-2.5 py-1.5">
                <Badge tone={p.connected ? "green" : "amber"}>{p.connected ? "Connected" : "Not connected"}</Badge>
                {p.name} · {p.kinds.map((k) => kindWord[k] ?? k).join(", ")}
                {!p.connected && canManage && <Button variant="ghost" onClick={onConnectors}>Connect</Button>}
              </span>
            ))}
          </span>
          {!state.storage && <p className="mt-2 text-muted-foreground">This server has no file storage set up, so nothing can be made yet. Ask whoever runs it.</p>}
          {!done[1] && <p className="mt-2 text-muted-foreground">One account that makes pictures is enough to start. Video and voice are only needed for talking videos.</p>}
        </Step>
        <Step n={3} title="Pick a starter skill" why="A skill is a saved recipe for one kind of ad. Start from one of ours and change it later." done={done[2]} next={next === 2} action={done[2] ? <Button variant="ghost" onClick={() => open("creative-skills")}>Open skills</Button> : undefined}>
          {done[2] ? `You have ${state.skills.length} ${state.skills.length === 1 ? "skill" : "skills"}: ${state.skills.slice(0, 3).map((s) => s.name).join(", ")}${state.skills.length > 3 ? " and more" : ""}.` : "Choose one below."}
        </Step>
        <Step n={4} title="Make your first creative" why="One picture, with your headline laid over it. You approve it before it goes anywhere." done={done[3]} next={next === 3} action={done[3] ? <Button variant="ghost" onClick={() => open("creative-library")}>Open the library</Button> : done[2] ? <Button onClick={() => open(`creative-skills/${state.skills[0].id}`)}>Make a creative</Button> : undefined}>
          {done[3] ? `${state.creatives} made, ${state.approved} approved.` : "Open a skill and press Make a creative. Say who it is for, the pain, and one proof."}
          {!done[3] && ideas.length > 0 && (
            <span className="mt-2 flex flex-wrap items-center gap-2">
              <span className="text-muted-foreground">Ideas from your Brand profile:</span>
              {ideas.map((p) => <span key={p.id} className="rounded-full border px-3 py-1">{p.name} · {p.pains[0]}</span>)}
            </span>
          )}
          {!done[3] && ideas.length === 0 && openProfile && <span className="mt-2 block text-muted-foreground">Add who you sell to and what hurts them in your <button type="button" className="underline" onClick={openProfile}>Brand profile</button>, and ideas show here.</span>}
        </Step>
      </section>

      <section className="rounded-xl border" aria-label="Starter skills">
        <header className="flex flex-wrap items-center justify-between gap-2 border-b px-5 py-3">
          <div><h3 className="text-[15px] font-medium">Starter skills</h3><p className="text-[12px] text-muted-foreground">Add one with a click. Each comes with two prompts to split test and rules that keep pictures from looking AI-made. Change anything afterwards.</p></div>
          {canManage && <Button variant="ghost" onClick={() => open("creative-skills/new")}>Start from a blank skill</Button>}
        </header>
        <div className="space-y-4 p-5">
          {canManage && (
            <label className="flex flex-wrap items-center gap-2 text-[13px]">
              <span>Picture model on your AI account</span>
              <input aria-label="Picture model" value={model} maxLength={200} onChange={(e) => setModel(e.target.value)} placeholder="The model's id, as your AI account names it" className={`${field} w-72`} />
              <span className="basis-full text-[12px] text-muted-foreground">Outflare has no default, because providers rename and retire models. Type it once here and every starter skill uses it.</span>
            </label>
          )}
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            {starterSkills.map((s) => {
              const have = state.skills.some((x) => x.name === s.name);
              return (
                <article key={s.id} className="flex flex-col gap-2 rounded-lg border p-4">
                  <span className="flex items-center gap-2"><Badge tone="grey">Picture</Badge><span className="text-[15px] font-medium">{s.name}</span></span>
                  <p className="flex-1 text-[13px] text-muted-foreground">{s.line}</p>
                  <div className="flex flex-wrap gap-2">
                    {have ? <Badge tone="green">Added</Badge> : canManage && <Button disabled={!model.trim() || adding !== ""} title={model.trim() ? undefined : "Type the picture model first"} onClick={() => void add(s.id)}>{adding === s.id ? "Adding…" : "Add this skill"}</Button>}
                    <Button variant="ghost" aria-expanded={shown === s.id} onClick={() => setShown(shown === s.id ? "" : s.id)}>{shown === s.id ? "Hide the prompts" : "See the prompts"}</Button>
                  </div>
                </article>
              );
            })}
            <article className="flex flex-col gap-2 rounded-lg border p-4">
              <span className="flex items-center gap-2"><Badge tone="grey">Video</Badge><span className="text-[15px] font-medium">Talking to camera</span></span>
              <p className="flex-1 text-[13px] text-muted-foreground">A person says a short script, filmed like a phone video. Needs an account for video and one for voice.</p>
              <div className="flex flex-wrap gap-2">
                {state.video && state.voice ? <Button variant="ghost" onClick={() => open("talking-videos/new")}>New talking video</Button> : <Button variant="ghost" disabled={!canManage} onClick={onConnectors}>Connect video and voice first</Button>}
              </div>
            </article>
          </div>
          {starterSkills.filter((s) => s.id === shown).map((s) => (
            <div key={s.id} className="space-y-3 rounded-lg border p-4 text-[13px]" aria-label={`Prompts of ${s.name}`}>
              {s.variants.map((v, i) => (
                <div key={v.name} className="space-y-1">
                  <div className="font-medium">Prompt {"AB"[i]}: {v.name}</div>
                  <p><span className="text-muted-foreground">Picture: </span>{v.picture}</p>
                  <p><span className="text-muted-foreground">Words: </span>{v.copy}</p>
                </div>
              ))}
              <p><span className="text-muted-foreground">Rules: </span>{rules}</p>
            </div>
          ))}
        </div>
      </section>

      {canManage && (
        <section className="rounded-xl border" aria-label="Say what you want">
          <header className="border-b px-5 py-3">
            <h3 className="text-[15px] font-medium">Or just say what you want</h3>
            <p className="text-[12px] text-muted-foreground">Describe the ad in your own words. Your own AI model turns it into a skill you can read and change before anything is made.</p>
          </header>
          <div className="space-y-3 p-5 text-[13px]">
            <textarea aria-label="The ad you want" value={said} maxLength={1000} rows={3} onChange={(e) => setSaid(e.target.value)} placeholder="Who the ad is for, what it shows, and what it leads with" className={`${field} h-auto w-full py-2`} />
            {ideas.length > 0 && (
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-muted-foreground">Ideas from your Brand profile:</span>
                {ideas.map((p) => <button key={p.id} type="button" className="rounded-full border px-3 py-1 hover:bg-muted" onClick={() => setSaid(`An ad for ${p.name} who struggle with this: ${p.pains[0]}.`)}>{p.name} · {p.pains[0]}</button>)}
              </div>
            )}
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="text-[12px] text-muted-foreground">This only writes the prompt, on your own AI model. No picture is made yet.</span>
              <Button disabled={!said.trim() || writing} onClick={() => void write()}>{writing ? "Writing…" : "Write the prompt"}</Button>
            </div>
            {sayError && (
              <div role="alert" className="flex flex-wrap items-center gap-2 rounded-md border p-3">
                <span className="flex-1">{sayError.text}</span>
                {sayError.connect && <Button variant="ghost" onClick={onConnectors}>Open Connectors</Button>}
              </div>
            )}
            {draft && (
              <div className="space-y-3 rounded-lg border p-4" aria-label="Draft skill">
                <p className="text-muted-foreground">Read it and change anything. Words in curly brackets, such as {"{Proof point}"}, are filled in for each creative.</p>
                <label className="block space-y-1"><span className="font-medium">Name</span><input aria-label="Skill name" value={draft.name} maxLength={100} onChange={(e) => setDraft({ ...draft, name: e.target.value })} className={`${field} w-full`} /></label>
                <label className="block space-y-1"><span className="font-medium">Picture prompt</span><textarea aria-label="Picture prompt" value={draft.picture} maxLength={8000} rows={5} onChange={(e) => setDraft({ ...draft, picture: e.target.value })} className={`${field} h-auto w-full py-2`} /></label>
                <label className="block space-y-1"><span className="font-medium">Words over the picture</span><textarea aria-label="Words over the picture" value={draft.copy} maxLength={4000} rows={4} onChange={(e) => setDraft({ ...draft, copy: e.target.value })} className={`${field} h-auto w-full py-2`} /></label>
                <div className="flex flex-wrap items-center gap-2">
                  <Button disabled={saving || !draft.name.trim() || !draft.picture.trim() || !model.trim()} title={model.trim() ? undefined : "Type the picture model above first"} onClick={() => void saveDraft()}>{saving ? "Saving…" : "Save as a skill"}</Button>
                  <Button variant="ghost" onClick={() => setDraft(null)}>Cancel</Button>
                  {!model.trim() && <span className="text-[12px] text-muted-foreground">Type the picture model under Starter skills first.</span>}
                </div>
              </div>
            )}
          </div>
        </section>
      )}
    </div>
  );
}
