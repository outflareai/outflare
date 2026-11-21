// Brand profile: what a brand's agents know about the company, as separate
// facts in twelve sections plus personas. The overview is a table of the
// sections; a section opens in the page with a Back button. Only a person
// confirms: what an agent added shows as waiting, and is never used with
// customers until someone says yes.

import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { BrandProfile as Profile, Persona, ProfileChange, ProfileFact, ProfileSection } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, Stat, Stats, Status, toast } from "../ui";
import { ago, plural } from "../format";

type Props = { ws: string; canManage: boolean; open: string; onOpen: (item: string) => void };
type FactSection = ProfileFact["section"];

// The question each section answers, shown under its name.
const questions: Record<ProfileSection["key"], string> = {
  company: "Who are we?",
  offer: "What do we sell, for how much?",
  customers: "Who do we serve?",
  personas: "Who is the person buying?",
  positioning: "Why us?",
  proof: "Why believe us?",
  competitors: "Who else do they look at?",
  objections: "What stops a deal?",
  voice: "How do we sound?",
  goals: "What are we trying to do now?",
  channels: "Where do we reach them?",
  rules: "What must never happen?",
};

const sources: Record<ProfileFact["source"], string> = {
  person: "Typed by a person",
  interview: "The interview",
  website: "Your website",
  conversation: "Conversations",
  agent: "Found by AI",
  import: "Imported",
  context: "About your company",
};

export function BrandProfile(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Brand profiles" brands={active.length} />;
  return <BrandProfilePage key={brand.id} {...props} brandId={brand.id} brandName={brand.name} />;
}

function BrandProfilePage({ canManage, open, onOpen, brandId, brandName }: Props & { brandId: string; brandName: string }) {
  const [profile, setProfile] = useState<Profile | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/profile", { params: { path: { brand: brandId } } });
    if (error) return setError(errorMessage(error));
    setError("");
    setProfile(data);
  }, [brandId]);
  useEffect(() => {
    load();
  }, [load]);

  if (!profile) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const back = { label: "Brand profile", onClick: () => onOpen("") };
  const section = profile.sections.find((s) => s.key === open);
  if (open === "review") return <Review profile={profile} brandId={brandId} canManage={canManage} back={back} reload={load} />;
  if (open === "history") return <History profile={profile} brandId={brandId} back={back} />;
  if (section?.key === "personas") return <Personas profile={profile} section={section} brandId={brandId} canManage={canManage} back={back} reload={load} />;
  if (section) return <Section profile={profile} section={section} brandId={brandId} canManage={canManage} back={back} reload={load} />;
  return <Overview profile={profile} brandName={brandName} onOpen={onOpen} error={error} />;
}

function Overview({ profile, brandName, onOpen, error }: { profile: Profile; brandName: string; onOpen: (item: string) => void; error: string }) {
  const c = profile.counts;
  const total = c.confirmed + c.suggested;
  return (
    <div>
      <PageHeader
        title="Brand profile"
        description={`What your agents know about ${brandName}: who you are, who you serve and why customers pick you. Every agent reads this before it writes or plans anything.`}
        action={<Button variant="ghost" onClick={() => onOpen("history")}>History</Button>}
      />
      <ErrorText>{error}</ErrorText>
      <Stats cols={4}>
        <Stat label="Facts agents can use" value={c.confirmed} sub={`across ${c.sections_filled} of ${c.sections_total} sections`} />
        <Stat label="Confirmed by a person" value={total ? `${Math.round((c.confirmed / total) * 100)}%` : "0%"} sub={`${c.confirmed} of ${plural(total, "fact")}`} tone={total && c.suggested === 0 ? "green" : undefined} />
        <Stat label="Waiting for you" value={c.suggested} sub="found by AI, not yet confirmed" tone={c.suggested ? "amber" : undefined} />
        <Stat label="Due for a check" value={c.due} sub="past their review date" tone={c.due ? "amber" : undefined} />
      </Stats>
      {c.suggested > 0 && (
        <div className="mt-4 flex flex-wrap items-center gap-3 rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-3">
          <p className="min-w-0 flex-1 text-[13px]">
            <span className="font-medium">{c.suggested === 1 ? "1 fact is" : `${c.suggested} facts are`} waiting for a yes or no.</span>{" "}
            <span className="text-muted-foreground">Agents use them for planning only, and never say them to a customer, until someone confirms.</span>
          </p>
          <Button onClick={() => onOpen("review")}>Review {plural(c.suggested, "fact")}</Button>
        </div>
      )}
      <div className="mt-5 overflow-x-auto rounded-xl border">
        <table className="w-full text-left text-[13px]">
          <thead className="border-b bg-muted/30 text-xs text-muted-foreground">
            <tr>
              <th className="px-4 py-2.5 font-medium">Section</th>
              <th className="px-4 py-2.5 font-medium">What agents know today</th>
              <th className="px-4 py-2.5 text-right font-medium">Facts</th>
              <th className="px-4 py-2.5 font-medium">Status</th>
              <th className="px-4 py-2.5 font-medium">Last changed</th>
            </tr>
          </thead>
          <tbody>
            {profile.sections.map((s) => {
              const first = s.key === "personas"
                ? profile.personas.filter((p) => p.status === "confirmed").map((p) => p.name).join(", ")
                : profile.facts.find((f) => f.section === s.key && f.status === "confirmed")?.text;
              const changed = (s.key === "personas" ? profile.personas : profile.facts.filter((f) => f.section === s.key))
                .map((x) => x.updated_at).sort().pop();
              return (
                <tr key={s.key} className="cursor-pointer border-b last:border-0 hover:bg-muted/30" onClick={() => onOpen(s.key)}>
                  <td className="px-4 py-3 align-top">
                    <button className="text-left font-medium hover:underline" onClick={(e) => { e.stopPropagation(); onOpen(s.key); }}>{s.label}</button>
                    <div className="text-xs text-muted-foreground">{questions[s.key]}</div>
                  </td>
                  <td className={`max-w-md px-4 py-3 align-top ${first ? "" : "text-muted-foreground"}`}>
                    <span className="line-clamp-2">{first || "Nothing yet."}</span>
                  </td>
                  <td className="px-4 py-3 text-right align-top tabular-nums">{s.confirmed + s.suggested}</td>
                  <td className="px-4 py-3 align-top">
                    {s.suggested > 0 ? <Status tone="amber">{s.suggested} to confirm</Status> : s.confirmed > 0 ? <Status tone="green">Confirmed</Status> : <Status>Empty</Status>}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 align-top text-muted-foreground">{changed ? ago(changed) : "Never"}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-xs text-muted-foreground">Source: this brand's profile. Private to your workspace.</p>
    </div>
  );
}

type Sub = { profile: Profile; brandId: string; canManage: boolean; back: { label: string; onClick: () => void }; reload: () => Promise<void> };

async function patchFact(brandId: string, id: string, body: Record<string, unknown>) {
  const { error } = await api.PATCH("/v1/brands/{brand}/profile/facts/{id}", { params: { path: { brand: brandId, id } }, body });
  if (error) toast(errorMessage(error), "error");
  return !error;
}

function factStatus(f: ProfileFact): ReactNode {
  if (f.status === "suggested") return <Status tone="amber">To confirm</Status>;
  return <Status tone="green">Confirmed{f.confirmed_by ? ` by ${f.confirmed_by}` : ""}</Status>;
}

const audience = (f: ProfileFact) => (f.visibility === "customers" ? <Badge tone="blue">Can say to customers</Badge> : <Badge>Team only</Badge>);

function Section({ profile, section, brandId, canManage, back, reload }: Sub & { section: ProfileSection }) {
  const [editing, setEditing] = useState<ProfileFact | "new" | null>(null);
  const facts = profile.facts.filter((f) => f.section === section.key);

  async function change(f: ProfileFact, body: Record<string, unknown>, done: string) {
    if (!(await patchFact(brandId, f.id, body))) return;
    toast(done);
    reload();
  }

  return (
    <div>
      <PageHeader back={back} title={section.label} description={section.description}
        action={canManage && <Button onClick={() => setEditing("new")}>Add a fact</Button>} />
      {facts.length === 0 ? (
        <Empty title={`Nothing in ${section.label} yet`} action={canManage && <Button onClick={() => setEditing("new")}>Add a fact</Button>}>
          {questions[section.key]} Add one fact at a time, in plain words. Agents read each one before they write or plan.
        </Empty>
      ) : (
        <div className="overflow-x-auto rounded-xl border">
          <table className="w-full text-left text-[13px]">
            <thead className="border-b bg-muted/30 text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">Fact</th>
                <th className="px-4 py-2.5 font-medium">Who can hear it</th>
                <th className="px-4 py-2.5 font-medium">Where it came from</th>
                <th className="px-4 py-2.5 font-medium">Status</th>
                <th className="px-4 py-2.5 font-medium">Changed</th>
                <th className="w-10" />
              </tr>
            </thead>
            <tbody>
              {facts.map((f) => (
                <tr key={f.id} className={`border-b last:border-0 ${canManage ? "cursor-pointer hover:bg-muted/30" : ""}`} onClick={() => canManage && setEditing(f)}>
                  <td className="max-w-xl px-4 py-3 align-top">
                    {f.title && <div className="font-medium">{f.title}</div>}
                    <div className="whitespace-pre-wrap">{f.text}</div>
                    {f.replaces_id && <div className="mt-1 text-xs text-muted-foreground">Would replace: {facts.find((x) => x.id === f.replaces_id)?.text ?? "a fact in another section"}</div>}
                    {f.evidence.map((e, i) => <div key={i} className="mt-1 text-xs text-muted-foreground">Because: {e.note}{e.ref ? ` (${e.ref})` : ""}</div>)}
                  </td>
                  <td className="px-4 py-3 align-top">{audience(f)}</td>
                  <td className="whitespace-nowrap px-4 py-3 align-top text-muted-foreground">{sources[f.source]}</td>
                  <td className="px-4 py-3 align-top">{factStatus(f)}</td>
                  <td className="whitespace-nowrap px-4 py-3 align-top text-muted-foreground">{ago(f.updated_at)}</td>
                  <td className="px-2 py-2 align-top" onClick={(e) => e.stopPropagation()}>
                    {canManage && (
                      <Menu aria={`Actions for ${f.title || "this fact"}`} items={[
                        { label: "Edit", onClick: () => setEditing(f) },
                        f.status === "suggested" && { label: "Confirm", onClick: () => change(f, { status: "confirmed" }, "Confirmed. Agents can use it now.") },
                        f.visibility === "internal"
                          ? { label: "Allow saying to customers", onClick: () => change(f, { visibility: "customers" }, "Agents may now say this to customers.") }
                          : { label: "Keep to the team", onClick: () => change(f, { visibility: "internal" }, "Agents will keep this to the team.") },
                        { label: "Retire", danger: true, onClick: () => change(f, { status: "retired" }, "Retired. It stays in the history.") },
                      ]} />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {editing && (
        <FactDialog brandId={brandId} section={section.key as FactSection} fact={editing === "new" ? null : editing}
          onClose={() => setEditing(null)} onSaved={() => { setEditing(null); reload(); }} />
      )}
    </div>
  );
}

function FactDialog({ brandId, section, fact, onClose, onSaved }: { brandId: string; section: FactSection; fact: ProfileFact | null; onClose: () => void; onSaved: () => void }) {
  const [title, setTitle] = useState(fact?.title ?? "");
  const [text, setText] = useState(fact?.text ?? "");
  const [visibility, setVisibility] = useState<ProfileFact["visibility"]>(fact?.visibility ?? "internal");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const body = { title: title.trim(), text: text.trim(), visibility };
    const { error } = fact
      ? await api.PATCH("/v1/brands/{brand}/profile/facts/{id}", { params: { path: { brand: brandId, id: fact.id } }, body })
      : await api.POST("/v1/brands/{brand}/profile/facts", { params: { path: { brand: brandId } }, body: { ...body, section } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(fact ? "Saved." : "Added. Agents can use it now.");
    onSaved();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{fact ? "Edit fact" : "Add a fact"}</DialogTitle>
          <DialogDescription>
            {fact?.context_field
              ? "This fact is also shown under Knowledge, About your company. Changing it here changes it there."
              : "One fact, in plain words. Every change is kept in the history."}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="space-y-3">
          <Input label="Short name" hint="Optional" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="What we sell" maxLength={200} />
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">The fact</span>
            <textarea className={`${field} h-auto w-full resize-y py-2 leading-relaxed`} rows={5} maxLength={fact?.context_field ? 10000 : 4000} value={text} required autoFocus
              onChange={(e) => setText(e.target.value)} placeholder="Three plans from $29 per vehicle a month, with a 14-day free trial." />
          </label>
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">Who can hear it</span>
            <select className={`${field} w-full`} value={visibility} onChange={(e) => setVisibility(e.target.value as ProfileFact["visibility"])}>
              <option value="internal">Team only: agents use it to plan, and never say it to a customer</option>
              <option value="customers">Customers: agents may say it in emails, posts and replies</option>
            </select>
          </label>
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy || !text.trim()}>{fact ? "Save" : "Add fact"}</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Personas({ profile, section, brandId, canManage, back, reload }: Sub & { section: ProfileSection }) {
  const [editing, setEditing] = useState<Persona | "new" | null>(null);

  async function change(p: Persona, status: Persona["status"], done: string) {
    const { error } = await api.PATCH("/v1/brands/{brand}/personas/{id}", { params: { path: { brand: brandId, id: p.id } }, body: { status } });
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    reload();
  }

  return (
    <div>
      <PageHeader back={back} title={section.label} description={`${section.description} A persona is a kind of person, never a real one.`}
        action={canManage && <Button onClick={() => setEditing("new")}>Add a persona</Button>} />
      {profile.personas.length === 0 ? (
        <Empty title="No personas yet" action={canManage && <Button onClick={() => setEditing("new")}>Add a persona</Button>}>
          Describe each kind of person you sell to: who they are, what they want, what hurts and what holds them back.
        </Empty>
      ) : (
        <div className="overflow-x-auto rounded-xl border">
          <table className="w-full text-left text-[13px]">
            <thead className="border-b bg-muted/30 text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">Persona</th>
                <th className="px-4 py-2.5 font-medium">What hurts</th>
                <th className="px-4 py-2.5 font-medium">What holds them back</th>
                <th className="px-4 py-2.5 font-medium">Status</th>
                <th className="px-4 py-2.5 font-medium">Changed</th>
                <th className="w-10" />
              </tr>
            </thead>
            <tbody>
              {profile.personas.map((p) => (
                <tr key={p.id} className="cursor-pointer border-b last:border-0 hover:bg-muted/30" onClick={() => setEditing(p)}>
                  <td className="px-4 py-3 align-top"><div className="font-medium">{p.name}</div><div className="text-xs text-muted-foreground">{p.role}</div></td>
                  <td className="max-w-xs px-4 py-3 align-top">{p.pains.slice(0, 2).join(" · ") || <span className="text-muted-foreground">Not said yet</span>}</td>
                  <td className="max-w-xs px-4 py-3 align-top">{p.objections.slice(0, 2).join(" · ") || <span className="text-muted-foreground">Not said yet</span>}</td>
                  <td className="px-4 py-3 align-top">{p.status === "suggested" ? <Status tone="amber">To confirm</Status> : <Status tone="green">Confirmed</Status>}</td>
                  <td className="whitespace-nowrap px-4 py-3 align-top text-muted-foreground">{ago(p.updated_at)}</td>
                  <td className="px-2 py-2 align-top" onClick={(e) => e.stopPropagation()}>
                    {canManage && (
                      <Menu aria={`Actions for ${p.name}`} items={[
                        { label: "Edit", onClick: () => setEditing(p) },
                        p.status === "suggested" && { label: "Confirm", onClick: () => change(p, "confirmed", "Confirmed. Agents can use it now.") },
                        { label: "Archive", danger: true, onClick: () => change(p, "archived", `Archived ${p.name}. It stays in the history.`) },
                      ]} />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {editing && (
        <PersonaDialog brandId={brandId} persona={editing === "new" ? null : editing} readOnly={!canManage}
          onClose={() => setEditing(null)} onSaved={() => { setEditing(null); reload(); }} />
      )}
    </div>
  );
}

const personaLists = [
  ["goals", "What they want"],
  ["pains", "What hurts today"],
  ["triggers", "What makes them start looking"],
  ["objections", "What holds them back"],
  ["channels", "Where to reach them"],
] as const;
type ListKey = (typeof personaLists)[number][0];

function PersonaDialog({ brandId, persona, readOnly, onClose, onSaved }: { brandId: string; persona: Persona | null; readOnly: boolean; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(persona?.name ?? "");
  const [role, setRole] = useState(persona?.role ?? "");
  const [summary, setSummary] = useState(persona?.summary ?? "");
  const [lists, setLists] = useState<Record<ListKey, string>>(() =>
    Object.fromEntries(personaLists.map(([k]) => [k, (persona?.[k] ?? []).join("\n")])) as Record<ListKey, string>);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const split = (v: string) => v.split("\n").map((l) => l.trim()).filter(Boolean);
    const body = {
      name: name.trim(), role: role.trim(), summary: summary.trim(),
      goals: split(lists.goals), pains: split(lists.pains), triggers: split(lists.triggers), objections: split(lists.objections), channels: split(lists.channels),
    };
    const { error } = persona
      ? await api.PATCH("/v1/brands/{brand}/personas/{id}", { params: { path: { brand: brandId, id: persona.id } }, body })
      : await api.POST("/v1/brands/{brand}/personas", { params: { path: { brand: brandId } }, body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(persona ? `Saved ${body.name}.` : `Added ${body.name}.`);
    onSaved();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{persona ? persona.name : "Add a persona"}</DialogTitle>
          <DialogDescription>A kind of person you sell to. In the lists, put one point on each line.</DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="space-y-3">
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Operations manager" maxLength={100} required autoFocus={!persona} readOnly={readOnly} />
          <Input label="Who they are" value={role} onChange={(e) => setRole(e.target.value)} placeholder="Runs dispatch at a courier with 5 to 80 vehicles" maxLength={200} readOnly={readOnly} />
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">In a few sentences</span>
            <textarea className={`${field} h-auto w-full resize-y py-2 leading-relaxed`} rows={3} maxLength={4000} value={summary} readOnly={readOnly} onChange={(e) => setSummary(e.target.value)} />
          </label>
          {personaLists.map(([key, label]) => (
            <label key={key} className="block text-sm">
              <span className="mb-1.5 block text-[13px] font-medium">{label}</span>
              <textarea className={`${field} h-auto w-full resize-y py-2 leading-relaxed`} rows={2} value={lists[key]} readOnly={readOnly}
                onChange={(e) => setLists((x) => ({ ...x, [key]: e.target.value }))} />
            </label>
          ))}
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>{readOnly ? "Close" : "Cancel"}</Button>
            {!readOnly && <Button disabled={busy || !name.trim()}>{persona ? "Save" : "Add persona"}</Button>}
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// Review: every suggestion waiting for a person, one yes or no each.
function Review({ profile, brandId, canManage, back, reload }: Sub) {
  const facts = profile.facts.filter((f) => f.status === "suggested");
  const personas = profile.personas.filter((p) => p.status === "suggested");
  const label = (key: string) => profile.sections.find((s) => s.key === key)?.label ?? key;

  async function fact(f: ProfileFact, status: "confirmed" | "retired") {
    if (!(await patchFact(brandId, f.id, { status }))) return;
    toast(status === "confirmed" ? "Confirmed. Agents can use it now." : "Marked as not true. It stays in the history.");
    reload();
  }
  async function persona(p: Persona, status: "confirmed" | "archived") {
    const { error } = await api.PATCH("/v1/brands/{brand}/personas/{id}", { params: { path: { brand: brandId, id: p.id } }, body: { status } });
    if (error) return toast(errorMessage(error), "error");
    toast(status === "confirmed" ? `Confirmed ${p.name}.` : `Archived ${p.name}.`);
    reload();
  }
  const buttons = (yes: () => void, no: () => void) => canManage && (
    <span className="flex shrink-0 gap-2">
      <Button variant="ghost" onClick={no}>Not true</Button>
      <Button onClick={yes}>Yes, that's right</Button>
    </span>
  );

  return (
    <div>
      <PageHeader back={back} title="Review what AI found"
        description="Each of these was added by an agent. Until you say yes, agents use it for planning only and never say it to a customer." />
      {facts.length + personas.length === 0 ? (
        <Empty title="Nothing is waiting" action={<Button onClick={back.onClick}>Back to the profile</Button>}>Every fact in the profile has been confirmed by a person.</Empty>
      ) : (
        <div className="space-y-3">
          {facts.map((f) => {
            const old = f.replaces_id ? profile.facts.find((x) => x.id === f.replaces_id) : undefined;
            return (
              <div key={f.id} className="flex flex-wrap items-start gap-4 rounded-xl border p-4">
                <div className="min-w-0 flex-1 text-[13px]">
                  <div className="mb-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">{label(f.section)} {audience(f)} <span>{sources[f.source]}, {ago(f.created_at)}</span></div>
                  {f.title && <div className="font-medium">{f.title}</div>}
                  <div className="whitespace-pre-wrap">{f.text}</div>
                  {old && <div className="mt-2 rounded-md bg-muted/40 px-3 py-2 text-xs text-muted-foreground">Would replace: {old.text}</div>}
                  {f.evidence.map((e, i) => <div key={i} className="mt-1 text-xs text-muted-foreground">Because: {e.note}{e.ref ? ` (${e.ref})` : ""}</div>)}
                </div>
                {buttons(() => fact(f, "confirmed"), () => fact(f, "retired"))}
              </div>
            );
          })}
          {personas.map((p) => (
            <div key={p.id} className="flex flex-wrap items-start gap-4 rounded-xl border p-4">
              <div className="min-w-0 flex-1 text-[13px]">
                <div className="mb-1 text-xs text-muted-foreground">Persona, {sources[p.source]}, {ago(p.created_at)}</div>
                <div className="font-medium">{p.name}</div>
                <div className="text-muted-foreground">{p.role}</div>
                {p.summary && <div className="mt-1 whitespace-pre-wrap">{p.summary}</div>}
              </div>
              {buttons(() => persona(p, "confirmed"), () => persona(p, "archived"))}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

const actions: Record<ProfileChange["action"], string> = {
  created: "Added", updated: "Changed", confirmed: "Confirmed", retired: "Retired", restored: "Brought back", imported: "Imported",
};

function History({ profile, brandId, back }: { profile: Profile; brandId: string; back: Sub["back"] }) {
  const [rows, setRows] = useState<ProfileChange[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async (from?: string) => {
    const { data, error } = await api.GET("/v1/brands/{brand}/profile/changes", { params: { path: { brand: brandId }, query: { limit: 100, cursor: from } } });
    if (error) return setError(errorMessage(error));
    setRows((old) => [...(from ? old ?? [] : []), ...data.data]);
    setCursor(data.next_cursor);
  }, [brandId]);
  useEffect(() => {
    load();
  }, [load]);

  if (!rows) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const text = (v: unknown) => (typeof v === "string" ? v : "");
  const what = (c: ProfileChange) => {
    if (c.persona_id) return `Persona: ${profile.personas.find((p) => p.id === c.persona_id)?.name ?? text(c.after.name) ?? ""}`;
    const f = profile.facts.find((x) => x.id === c.fact_id);
    return f?.title || text(c.after.text) || f?.text || text(c.before.text) || "A fact";
  };
  return (
    <div>
      <PageHeader back={back} title="History" description="Every change to this brand's profile: who made it, and what it said before and after. Nothing is ever erased." />
      <ErrorText>{error}</ErrorText>
      {rows.length === 0 ? <Empty title="No changes yet">Changes to facts and personas are listed here.</Empty> : (
        <div className="overflow-x-auto rounded-xl border">
          <table className="w-full text-left text-[13px]">
            <thead className="border-b bg-muted/30 text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">When</th>
                <th className="px-4 py-2.5 font-medium">Who</th>
                <th className="px-4 py-2.5 font-medium">What happened</th>
                <th className="px-4 py-2.5 font-medium">Before</th>
                <th className="px-4 py-2.5 font-medium">After</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => (
                <tr key={c.id} className="border-b last:border-0">
                  <td className="whitespace-nowrap px-4 py-3 align-top text-muted-foreground" title={c.created_at}>{ago(c.created_at)}</td>
                  <td className="whitespace-nowrap px-4 py-3 align-top">{c.actor ?? "Outflare"}{c.actor_type && c.actor_type !== "human" ? <span className="text-muted-foreground"> (agent)</span> : null}</td>
                  <td className="max-w-xs px-4 py-3 align-top"><span className="font-medium">{actions[c.action]}</span> <span className="line-clamp-2 text-muted-foreground">{what(c)}</span></td>
                  <td className="max-w-xs px-4 py-3 align-top text-muted-foreground"><span className="line-clamp-3">{text(c.before.text) || text(c.before.status)}</span></td>
                  <td className="max-w-xs px-4 py-3 align-top"><span className="line-clamp-3">{text(c.after.text) || text(c.after.status)}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {cursor && <div className="mt-3"><Button variant="ghost" onClick={() => load(cursor)}>Show older changes</Button></div>}
    </div>
  );
}
