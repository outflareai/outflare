import { useEffect, useState } from "react";
import type { Journalist, PRExpert, Story, StoryCheck, StoryPitch, StoryResult } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// Stories: what a brand pitches to journalists. A story's release has a
// sourced fact behind every number and only quotes its experts approved;
// the check says what's missing and a person approves it. The workspace's
// own AI can suggest ideas and draft a release from the facts and approved
// quotes. The second part of digital PR, after the news watch.

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;
type Section = "stories" | "experts";
type Fact = NonNullable<Story["facts"]>[number];
type Quote = NonNullable<Story["quotes"]>[number];

const statusLabel: Record<Story["status"], string> = { idea: "Idea", draft: "Draft", approved: "Approved", archived: "Archived" };
const statusTone: Record<Story["status"], Tone> = { idea: "grey", draft: "blue", approved: "green", archived: "grey" };
const kindLabel: Record<Story["kind"], string> = { data: "Data", reactive: "Reactive", comment: "Comment" };
const sourceLabel: Record<Fact["source_kind"], string> = { own_data: "Our own data", public_data: "Public data", news: "News", brand_fact: "Brand fact", other: "Other" };
const quoteTone: Record<Quote["status"], Tone> = { draft: "amber", approved: "green", withdrawn: "grey" };
const problemLabel: Record<StoryCheck["errors"][number]["code"], string> = {
  no_release: "No release yet",
  unsourced_number: "A number with no fact",
  fact_without_source: "A fact with no source",
  unapproved_quote: "A quote the expert hasn't approved",
  hype_phrase: "Hype",
  needs_second_look: "A finance or health claim to check",
};
// AI errors that need someone to set something up, worded for the button.
const aiSetup = new Set(["connector_needed", "ai_model_needed"]);

export function Stories({ ws, canManage, go }: Props) {
  const [section, setSection] = useState<Section>("stories");
  const [open, setOpen] = useState("");
  const brandKey = useBrand(ws).query.brand ?? "";
  const brand = brandKey ? { brand: brandKey } : {};
  if (open) return <StoryDetail ws={ws} id={open} canManage={canManage} back={() => setOpen("")} go={go} />;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Stories"
        description="Press releases for your brands, with the source behind every number and only quotes your experts approved. Outflare checks each release, and a person approves it. Agents use create_story, draft_story and check_story."
      />
      <Tabs value={section} onValueChange={(v) => setSection(v as Section)} className="max-w-full overflow-x-auto">
        <TabsList className="h-8">
          <TabsTrigger value="stories" className="px-3 text-[12px]">Stories</TabsTrigger>
          <TabsTrigger value="experts" className="px-3 text-[12px]">Experts</TabsTrigger>
        </TabsList>
      </Tabs>
      {section === "stories" && <StoryList ws={ws} canManage={canManage} brand={brand} open={setOpen} go={go} />}
      {section === "experts" && <Experts ws={ws} canManage={canManage} brand={brand} />}
    </div>
  );
}

// aiFailed shows an AI error, with a way to set the AI up when that's what
// it needs.
function aiFailed(error: unknown, go: (tab: Tab) => void, setError: (s: string) => void) {
  const code = (error as { error?: { code?: string } })?.error?.code ?? "";
  if (aiSetup.has(code)) {
    ask({ title: "Set up your AI model", body: errorMessage(error), choices: [{ label: "Not now", value: "no", variant: "ghost" }, { label: "Open AI settings", value: "go" }] })
      .then((r) => r?.choice === "go" && go(code === "connector_needed" ? "connectors" : "ai"));
    return;
  }
  setError(errorMessage(error));
}

function StoryList({ ws, canManage, brand, open, go }: { ws: string; canManage: boolean; brand: { brand?: string }; open: (id: string) => void; go: (tab: Tab) => void }) {
  const { active, byId } = useBrand(ws);
  const [show, setShow] = useState<"open" | "archived">("open");
  const [list, setList] = useState<Story[] | null>(null);
  const [title, setTitle] = useState("");
  const [brandID, setBrandID] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const forBrand = brand.brand ? active.find((b) => b.handle === brand.brand)?.id : brandID || active[0]?.id;

  useEffect(() => {
    setList(null);
    api.GET("/v1/workspaces/{ws}/stories", { params: { path: { ws }, query: { ...brand, status: show === "open" ? "idea,draft,approved" : "archived", limit: 200 } } })
      .then(({ data, error }) => {
        if (error || !data) return setError(errorMessage(error));
        setList(data.data);
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, show, reload, brand.brand]);

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim() || !forBrand) return;
    setBusy("add");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/stories", { params: { path: { ws } }, body: { brand_id: forBrand, title: title.trim() } });
    setBusy("");
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setTitle("");
    open(data.id);
  };
  const ideas = async () => {
    if (!forBrand) return;
    const r = await ask({
      title: "Suggest story ideas",
      body: "Your AI model reads your brand, its facts, its experts' topics, its coverage and the news your topic searches found, and suggests stories only you can tell. Ideas never state numbers or quotes.",
      input: { label: "What about (optional)", placeholder: "Angles for the bot traffic beat", maxLength: 1000, optional: true },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Suggest 5 ideas", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    setBusy("ideas");
    const { error } = await api.POST("/v1/workspaces/{ws}/stories/ideas", { params: { path: { ws } }, body: { brand_id: forBrand, count: 5, brief: r.value.trim() } });
    setBusy("");
    if (error) return aiFailed(error, go, setError);
    setError("");
    toast("Your AI model is thinking. Ideas show here in a minute.");
    setTimeout(() => setReload((n) => n + 1), 20000);
  };

  return (
    <div className="space-y-4">
      {error && <ErrorText>{error}</ErrorText>}
      {canManage && active.length > 0 && (
        <form onSubmit={add} className="flex flex-wrap items-center gap-2">
          <input className={`${field} w-96 max-w-full`} aria-label="Headline" placeholder="A third of sites block their own customers' bots" value={title} onChange={(e) => setTitle(e.target.value)} maxLength={300} />
          {!brand.brand && active.length > 1 && (
            <select className={field} aria-label="Brand" value={forBrand} onChange={(e) => setBrandID(e.target.value)}>
              {active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
            </select>
          )}
          <Button type="submit" disabled={!title.trim() || busy === "add"}>Start a story</Button>
          <Button type="button" variant="ghost" disabled={busy === "ideas"} onClick={ideas}>Suggest ideas with AI</Button>
        </form>
      )}
      {!list ? (
        <Loading />
      ) : (
        <DataTable
          data={list}
          rowId={(s) => s.id}
          searchPlaceholder="Search stories"
          toolbar={
            <select className={field} aria-label="Show" value={show} onChange={(e) => setShow(e.target.value as "open" | "archived")}>
              <option value="open">Ideas, drafts and approved</option>
              <option value="archived">Archived</option>
            </select>
          }
          empty={show === "open" ? "No stories yet. Start one above, or let your AI model suggest ideas from your coverage and the news." : "Nothing archived."}
          mobile={(s) => (
            <button type="button" className="w-full space-y-1 text-left" onClick={() => open(s.id)}>
              <div className="flex items-start justify-between gap-2">
                <span className="font-medium">{s.title}</span>
                <Badge tone={statusTone[s.status]}>{statusLabel[s.status]}</Badge>
              </div>
              <div className="text-xs text-muted-foreground">{[byId(s.brand_id)?.name, kindLabel[s.kind], `changed ${ago(s.updated_at)}`].filter(Boolean).join(" · ")}</div>
            </button>
          )}
          columns={[
            {
              id: "title",
              header: "Story",
              accessorFn: (s) => `${s.title} ${s.summary}`,
              cell: ({ row: { original: s } }) => (
                <div className="min-w-0 space-y-0.5">
                  <button type="button" className="block max-w-[28rem] truncate text-left font-medium underline-offset-2 hover:underline" onClick={() => open(s.id)} title={s.title}>{s.title}</button>
                  {s.summary && <div className="max-w-[28rem] truncate text-[12px] text-muted-foreground" title={s.summary}>{s.summary}</div>}
                </div>
              ),
            },
            { id: "kind", header: "Kind", accessorFn: (s) => s.kind, cell: ({ row }) => <span className="text-[12px]">{kindLabel[row.original.kind]}</span> },
            { id: "brand", header: "Brand", accessorFn: (s) => byId(s.brand_id)?.name ?? "", cell: ({ row }) => <span className="text-[12px]">{byId(row.original.brand_id)?.name ?? ""}</span> },
            {
              id: "status",
              header: "Status",
              accessorFn: (s) => s.status,
              cell: ({ row: { original: s } }) => (
                <div className="space-y-0.5">
                  <Badge tone={statusTone[s.status]}>{statusLabel[s.status]}</Badge>
                  <div className="text-[11px] text-muted-foreground">
                    {s.ai_status === "drafting" ? "AI is drafting" : s.last_check && !s.last_check.ok ? `${s.last_check.errors.length} to fix` : s.source === "ai_ideas" ? "suggested by AI" : ""}
                  </div>
                </div>
              ),
            },
            { id: "updated", header: "Changed", accessorFn: (s) => s.updated_at, cell: ({ row }) => <span className="text-[12px] whitespace-nowrap">{ago(row.original.updated_at)}</span> },
          ]}
        />
      )}
    </div>
  );
}

function StoryDetail({ ws, id, canManage, back, go }: { ws: string; id: string; canManage: boolean; back: () => void; go: (tab: Tab) => void }) {
  const { byId } = useBrand(ws);
  const [story, setStory] = useState<Story | null>(null);
  const [experts, setExperts] = useState<PRExpert[]>([]);
  const [edit, setEdit] = useState({ title: "", kind: "data" as Story["kind"], summary: "", release: "" });
  const [fact, setFact] = useState({ statement: "", source_kind: "own_data" as Fact["source_kind"], source_url: "", source_note: "" });
  const [quote, setQuote] = useState({ body: "", expert_id: "" });
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const path = { ws, story: id };

  const show = (s: Story) => {
    setStory(s);
    setEdit({ title: s.title, kind: s.kind, summary: s.summary, release: s.release });
  };
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/stories/{story}", { params: { path } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      show(data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, id, reload]);
  useEffect(() => {
    if (!story) return;
    api.GET("/v1/workspaces/{ws}/pr-experts", { params: { path: { ws }, query: { brand: story.brand_id } } }).then(({ data }) => setExperts(data?.data ?? []));
  }, [ws, story?.brand_id]); // eslint-disable-line react-hooks/exhaustive-deps
  // While the AI drafts, look again every few seconds.
  useEffect(() => {
    if (story?.ai_status !== "drafting") return;
    const t = setTimeout(() => setReload((n) => n + 1), 4000);
    return () => clearTimeout(t);
  }, [story]);

  // done takes a write's answer: the story, or an error.
  const done = (res: { data?: Story; error?: unknown }, ok?: string) => {
    setBusy("");
    if (res.error || !res.data) return setError(errorMessage(res.error));
    setError("");
    show(res.data);
    if (ok) toast(ok);
  };
  if (!story) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const facts = story.facts ?? [];
  const quotes = story.quotes ?? [];
  const changed = edit.title !== story.title || edit.kind !== story.kind || edit.summary !== story.summary || edit.release !== story.release;
  const drafting = story.ai_status === "drafting";
  const expertName = (eid: string) => experts.find((x) => x.id === eid)?.name ?? "An expert";

  const save = async () => {
    setBusy("save");
    done(await api.PATCH("/v1/workspaces/{ws}/stories/{story}", { params: { path }, body: edit }), "Saved.");
  };
  const draft = async () => {
    if (changed && !(await confirmAction("Draft over your changes?", "Your AI model's draft replaces the release, and your unsaved changes are lost.", "Draft anyway"))) return;
    const r = await ask({
      title: story.release ? "Redraft with AI" : "Draft with AI",
      body: "Your AI model writes the release from the headline, notes, facts and approved quotes only. It replaces the release, and Outflare checks it straight away.",
      input: { label: "What to stress (optional)", placeholder: "Lead with the 37%.", maxLength: 1000, optional: true },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Draft", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    setBusy("draft");
    const { error } = await api.POST("/v1/workspaces/{ws}/stories/{story}/draft", { params: { path }, body: { guidance: r.value.trim() } });
    setBusy("");
    if (error) return aiFailed(error, go, setError);
    setError("");
    setReload((n) => n + 1);
  };
  const check = async () => {
    setBusy("check");
    const { error } = await api.POST("/v1/workspaces/{ws}/stories/{story}/check", { params: { path } });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setReload((n) => n + 1);
  };
  const approve = async () => {
    if (!(await confirmAction("Approve this release?", "It may be pitched as it stands. Any change to its headline, release, facts or quotes sends it back to draft.", "Approve", false))) return;
    setBusy("approve");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/stories/{story}/approve", { params: { path } });
    if (error) setReload((n) => n + 1);
    done({ data: data as Story | undefined, error }, "Approved.");
  };
  const move = async (status: "idea" | "draft" | "archived") => {
    setBusy("move");
    done(await api.PATCH("/v1/workspaces/{ws}/stories/{story}", { params: { path }, body: { status } }));
  };
  const remove = async () => {
    if (!(await confirmAction(`Delete "${story.title}"?`, "Its facts and quotes go too.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/stories/{story}", { params: { path } });
    if (error) return setError(errorMessage(error));
    back();
  };
  const addFact = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy("fact");
    const res = await api.POST("/v1/workspaces/{ws}/stories/{story}/facts", { params: { path }, body: { ...fact, statement: fact.statement.trim() } });
    if (!res.error) setFact({ ...fact, statement: "", source_url: "", source_note: "" });
    done(res);
  };
  const removeFact = async (f: Fact) => {
    const { error } = await api.DELETE("/v1/workspaces/{ws}/stories/{story}/facts/{fact}", { params: { path: { ...path, fact: f.id } } });
    if (error) return setError(errorMessage(error));
    setReload((n) => n + 1);
  };
  const addQuote = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy("quote");
    const res = await api.POST("/v1/workspaces/{ws}/stories/{story}/quotes", { params: { path }, body: { body: quote.body.trim(), ...(quote.expert_id ? { expert_id: quote.expert_id } : {}) } });
    if (!res.error) setQuote({ ...quote, body: "" });
    done(res);
  };
  const patchQuote = async (q: Quote, body: { status: Quote["status"]; approval_note?: string }) => {
    done(await api.PATCH("/v1/workspaces/{ws}/stories/{story}/quotes/{quote}", { params: { path: { ...path, quote: q.id } }, body }));
  };
  const approveQuote = async (q: Quote) => {
    const r = await ask({
      title: `Did ${expertName(q.expert_id)} approve these exact words?`,
      body: <blockquote className="border-l-2 pl-3 text-[13px]">{q.body}</blockquote>,
      input: { label: "How they approved it", placeholder: "By email on 29 Oct", maxLength: 500 },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Record approval", value: "ok" }],
    });
    if (r?.choice === "ok") patchQuote(q, { status: "approved", approval_note: r.value.trim() });
  };

  const problems = story.last_check ? [...story.last_check.errors.map((p) => ({ ...p, error: true })), ...story.last_check.warnings.map((p) => ({ ...p, error: false }))] : [];
  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Stories", onClick: back }}
        title={story.title}
        description={[byId(story.brand_id)?.name, kindLabel[story.kind], story.approved_at ? `approved ${ago(story.approved_at)}` : `changed ${ago(story.updated_at)}`].filter(Boolean).join(" · ")}
        action={
          <div className="flex items-center gap-2">
            <Badge tone={statusTone[story.status]}>{statusLabel[story.status]}</Badge>
            {canManage && (
              <Menu
                aria="More for this story"
                items={[
                  story.status !== "idea" && story.status !== "archived" && { label: "Back to idea", onClick: () => move("idea") },
                  story.status !== "archived" && { label: "Archive", onClick: () => move("archived") },
                  story.status === "archived" && { label: "Back to draft", onClick: () => move("draft") },
                  { label: "Delete", onClick: remove, danger: true },
                ]}
              />
            )}
          </div>
        }
      />
      {error && <ErrorText>{error}</ErrorText>}
      {story.ai_status === "failed" && <ErrorText>The AI draft failed: {story.ai_error}</ErrorText>}

      <Card
        title="Release"
        description="Markdown. Every number needs a fact below, and every quote must be one its expert approved."
        action={
          canManage && story.status !== "archived" && (
            <div className="flex flex-wrap items-center gap-2">
              <Button variant="ghost" disabled={drafting || busy === "draft"} onClick={draft}>{drafting ? "Drafting..." : story.release ? "Redraft with AI" : "Draft with AI"}</Button>
              <Button variant="ghost" disabled={!changed || busy === "save" || drafting} onClick={save}>Save</Button>
            </div>
          )
        }
      >
        <div className="space-y-3">
          <div className="flex flex-wrap gap-2">
            <input className={`${field} min-w-64 flex-1`} aria-label="Headline" value={edit.title} onChange={(e) => setEdit({ ...edit, title: e.target.value })} maxLength={300} disabled={!canManage || drafting} />
            <select className={field} aria-label="Kind" value={edit.kind} onChange={(e) => setEdit({ ...edit, kind: e.target.value as Story["kind"] })} disabled={!canManage || drafting}>
              <option value="data">Data: a finding from numbers</option>
              <option value="reactive">Reactive: on a news article</option>
              <option value="comment">Comment: an expert's view</option>
            </select>
          </div>
          <textarea className={`${field} w-full`} rows={2} aria-label="Notes" placeholder="Notes for the team: the angle, who it's for" value={edit.summary}
            onChange={(e) => setEdit({ ...edit, summary: e.target.value })} maxLength={2000} disabled={!canManage || drafting} />
          <textarea className={`${field} w-full font-mono text-[12px]`} rows={14} aria-label="Release" placeholder={drafting ? "Your AI model is drafting..." : "# Headline\n\nThe news, in short paragraphs."}
            value={edit.release} onChange={(e) => setEdit({ ...edit, release: e.target.value })} maxLength={20000} disabled={!canManage || drafting} />
        </div>
      </Card>

      <Card
        title="Check"
        description={story.checked_at ? `Checked ${ago(story.checked_at)}.` : "Not checked yet."}
        action={
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="ghost" disabled={busy === "check" || changed || drafting} onClick={check}>Check</Button>
            {canManage && story.status !== "archived" && story.status !== "approved" && (
              <Button disabled={busy === "approve" || changed || drafting || !story.release} onClick={approve}>Approve</Button>
            )}
          </div>
        }
      >
        {changed && <p className="mb-2 text-[12px] text-muted-foreground">Save your changes to check them.</p>}
        {!story.last_check ? (
          <p className="text-[13px] text-muted-foreground">The check makes sure every number has a sourced fact and every quote was approved, and flags hype and finance or health claims.</p>
        ) : problems.length === 0 ? (
          <p className="text-[13px]">Passed, with nothing to look at.</p>
        ) : (
          <ul className="space-y-1.5">
            {problems.map((p, i) => (
              <li key={i} className="flex items-start gap-2 text-[13px]">
                <Badge tone={p.error ? "red" : "amber"}>{p.error ? "Fix" : "Look"}</Badge>
                <span>{problemLabel[p.code]}: <span className="font-mono text-[12px]">{p.detail}</span></span>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card title="Facts" description="Each number in the release and where it comes from. Figures from your own data are aggregates only, never about a person.">
        <div className="space-y-3">
          {facts.length === 0 && <p className="text-[13px] text-muted-foreground">No facts yet.</p>}
          <ul className="space-y-2">
            {facts.map((f) => (
              <li key={f.id} className="flex items-start justify-between gap-3 text-[13px]">
                <div className="min-w-0 space-y-0.5">
                  <div>{f.statement}</div>
                  <div className="text-[12px] text-muted-foreground">
                    {sourceLabel[f.source_kind]}
                    {f.source_url && <> · <a className="underline-offset-2 hover:underline" href={f.source_url} target="_blank" rel="noreferrer noopener nofollow">{f.source_url}</a></>}
                    {f.source_note && <> · {f.source_note}</>}
                  </div>
                </div>
                {canManage && <Button variant="ghost" onClick={() => removeFact(f)}>Remove</Button>}
              </li>
            ))}
          </ul>
          {canManage && (
            <form onSubmit={addFact} className="flex flex-wrap items-center gap-2">
              <input className={`${field} min-w-64 flex-1`} aria-label="Fact" placeholder="37% of the sites we measured blocked their customers' bots" value={fact.statement} onChange={(e) => setFact({ ...fact, statement: e.target.value })} maxLength={1000} />
              <select className={field} aria-label="Source kind" value={fact.source_kind} onChange={(e) => setFact({ ...fact, source_kind: e.target.value as Fact["source_kind"] })}>
                {Object.entries(sourceLabel).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
              </select>
              <input className={`${field} w-56`} aria-label="Source link" placeholder="https://..." value={fact.source_url} onChange={(e) => setFact({ ...fact, source_url: e.target.value })} maxLength={2048} />
              <input className={`${field} w-56`} aria-label="Source note" placeholder="or how to find it again" value={fact.source_note} onChange={(e) => setFact({ ...fact, source_note: e.target.value })} maxLength={1000} />
              <Button type="submit" disabled={!fact.statement.trim() || (!fact.source_url.trim() && !fact.source_note.trim()) || busy === "fact"}>Add fact</Button>
            </form>
          )}
        </div>
      </Card>

      <Card title="Quotes" description="Drafted for one of the brand's experts. A quote can go in the release only once you record that the expert approved those exact words.">
        <div className="space-y-3">
          {quotes.length === 0 && <p className="text-[13px] text-muted-foreground">No quotes yet.</p>}
          <ul className="space-y-2">
            {quotes.map((q) => (
              <li key={q.id} className="flex items-start justify-between gap-3 text-[13px]">
                <div className="min-w-0 space-y-0.5">
                  <div>"{q.body}"</div>
                  <div className="flex flex-wrap items-center gap-1.5 text-[12px] text-muted-foreground">
                    <span>{expertName(q.expert_id)}</span>
                    <Badge tone={quoteTone[q.status]}>{q.status === "draft" ? "Waiting for approval" : q.status === "approved" ? "Approved" : "Withdrawn"}</Badge>
                    {q.approval_note && <span>{q.approval_note}</span>}
                  </div>
                </div>
                {canManage && (
                  <Menu
                    aria="More for this quote"
                    items={[
                      q.status !== "approved" && { label: "Record the expert's approval", onClick: () => approveQuote(q) },
                      q.status !== "withdrawn" && { label: "Withdraw", onClick: () => patchQuote(q, { status: "withdrawn" }) },
                      q.status === "withdrawn" && { label: "Back to draft", onClick: () => patchQuote(q, { status: "draft" }) },
                    ]}
                  />
                )}
              </li>
            ))}
          </ul>
          {canManage && experts.length === 0 && <p className="text-[12px] text-muted-foreground">Add an expert for this brand under Experts to draft quotes.</p>}
          {canManage && experts.length > 0 && (
            <form onSubmit={addQuote} className="flex flex-wrap items-center gap-2">
              <input className={`${field} min-w-64 flex-1`} aria-label="Quote" placeholder="What the expert would say, for them to approve" value={quote.body} onChange={(e) => setQuote({ ...quote, body: e.target.value })} maxLength={2000} />
              {experts.length > 1 && (
                <select className={field} aria-label="Expert" value={quote.expert_id} onChange={(e) => setQuote({ ...quote, expert_id: e.target.value })}>
                  <option value="">{experts.find((x) => x.is_default)?.name ?? "The default expert"}</option>
                  {experts.filter((x) => !x.is_default).map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
                </select>
              )}
              <Button type="submit" disabled={!quote.body.trim() || busy === "quote"}>Draft quote</Button>
            </form>
          )}
        </div>
      </Card>

      <Pitches ws={ws} story={story} canManage={canManage} go={go} />
    </div>
  );
}

const pitchTone: Record<StoryPitch["status"], Tone> = { writing: "grey", draft: "amber", enrolled: "green", rejected: "grey" };
const pitchLabel: Record<StoryPitch["status"], string> = { writing: "Writing", draft: "Waiting for approval", enrolled: "Sent", rejected: "Rejected" };
const replyLabel: Record<NonNullable<StoryPitch["reply_kind"]>, string> = {
  covering: "Covering it", wants_data: "Wants the data", wants_interview: "Wants an interview", not_my_beat: "Not my beat", declined: "Declined", other: "Replied",
};
const skipLabel: Record<string, string> = {
  no_email: "no email address", already_pitched: "already pitched for this story", pitched_this_week: "pitched in the last week", not_a_person: "not a person", not_found: "not found",
};

// Pitches: the story's pitches to journalists, picking journalists and a
// sequence to pitch, and its results.
function Pitches({ ws, story, canManage, go }: { ws: string; story: Story; canManage: boolean; go: (tab: Tab) => void }) {
  const [list, setList] = useState<StoryPitch[] | null>(null);
  const [result, setResult] = useState<StoryResult | null>(null);
  const [sequences, setSequences] = useState<{ id: string; name: string }[]>([]);
  const [journalists, setJournalists] = useState<Journalist[]>([]);
  const [picked, setPicked] = useState<string[]>([]);
  const [sequence, setSequence] = useState("");
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const approved = story.status === "approved";

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/story-pitches", { params: { path: { ws }, query: { story_id: story.id, limit: 500 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setList(data.data);
    });
    api.GET("/v1/workspaces/{ws}/stories/results", { params: { path: { ws }, query: { story_id: story.id, days: 365 } } })
      .then(({ data }) => setResult(data?.data[0] ?? null));
  }, [ws, story.id, reload]);
  // While openers are written, look again every few seconds.
  useEffect(() => {
    if (!list?.some((p) => p.status === "writing")) return;
    const t = setTimeout(() => setReload((n) => n + 1), 4000);
    return () => clearTimeout(t);
  }, [list]);
  useEffect(() => {
    if (!approved || !canManage) return;
    api.GET("/v1/workspaces/{ws}/sequences", { params: { path: { ws }, query: { brand: story.brand_id } } }).then(({ data }) => {
      const open = (data?.data ?? []).filter((q) => q.status !== "archived");
      setSequences(open);
      setSequence((s) => s || open[0]?.id || "");
    });
    api.GET("/v1/workspaces/{ws}/journalists", { params: { path: { ws }, query: { brand: story.brand_id, limit: 200 } } }).then(({ data }) => setJournalists(data?.data ?? []));
  }, [ws, story.brand_id, approved, canManage]);

  const pitched = new Set((list ?? []).filter((p) => p.status !== "rejected").map((p) => p.record_id));
  const start = async () => {
    setBusy("start");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/stories/{story}/pitches", { params: { path: { ws, story: story.id } }, body: { sequence_id: sequence, record_ids: picked } });
    setBusy("");
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setPicked([]);
    const skipped = data.skipped.map((x) => skipLabel[x.reason] ?? x.reason);
    toast(`${data.data.length} pitch${data.data.length === 1 ? "" : "es"} started; your AI model is writing the openers.${skipped.length ? ` Skipped ${skipped.length}: ${[...new Set(skipped)].join(", ")}.` : ""}`);
    setReload((n) => n + 1);
  };
  const approve = async (p: StoryPitch) => {
    if (!(await confirmAction(`Pitch ${p.journalist_name}?`, story.embargo_until && new Date(story.embargo_until) > new Date() ?
      `They're added to ${p.sequence_name}, and it writes to them once the embargo lifts.` : `They're added to ${p.sequence_name}, which writes to them in its next sending window.`, "Approve", false))) return;
    setBusy(p.id);
    const edited = edits[p.id];
    const { error } = await api.POST("/v1/workspaces/{ws}/story-pitches/{pitch}/approve", { params: { path: { ws, pitch: p.id } }, body: edited !== undefined ? { opener: edited } : {} });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setError("");
    setReload((n) => n + 1);
  };
  const reject = async (p: StoryPitch) => {
    const { error } = await api.POST("/v1/workspaces/{ws}/story-pitches/{pitch}/reject", { params: { path: { ws, pitch: p.id } }, body: {} });
    if (error) return setError(errorMessage(error));
    setReload((n) => n + 1);
  };

  return (
    <Card
      title="Pitches"
      description="An approved story goes to journalists through one of your sequences: one email and at most one follow-up, stopping on any reply. You approve each pitch."
    >
      <div className="space-y-4">
        {error && <ErrorText>{error}</ErrorText>}
        {result && (
          <p className="text-[13px]">
            {[`${result.pitched} pitched`, `${result.replied} replied`, ...Object.entries(result.reply_kinds).map(([k, n]) => `${n} ${replyLabel[k as keyof typeof replyLabel]?.toLowerCase() ?? k}`),
              `${result.coverage} article${result.coverage === 1 ? "" : "s"}`, `${result.linked} linked`].join(" · ")}
          </p>
        )}
        {!approved && <p className="text-[13px] text-muted-foreground">Approve the release above to pitch it.</p>}
        {approved && canManage && (
          <div className="space-y-2">
            {sequences.length === 0 ? (
              <p className="text-[12px] text-muted-foreground">
                Make a sequence for pitches first: 1 or 2 steps that use {"{{custom.opener}}"}, stopping on reply.{" "}
                <button type="button" className="underline" onClick={() => go("sequences")}>Sequences</button>
              </p>
            ) : journalists.length === 0 ? (
              <p className="text-[12px] text-muted-foreground">
                No journalists yet. They come from the articles your news searches find.{" "}
                <button type="button" className="underline" onClick={() => go("news")}>News watch</button>
              </p>
            ) : (
              <>
                <div className="max-h-64 space-y-1 overflow-y-auto rounded-md border p-2">
                  {journalists.map((j) => (
                    <label key={j.record_id} className={`flex items-start gap-2 text-[13px] ${pitched.has(j.record_id) ? "opacity-50" : ""}`}>
                      <input type="checkbox" className="mt-1" disabled={pitched.has(j.record_id) || !j.email} checked={picked.includes(j.record_id)}
                        onChange={(e) => setPicked(e.target.checked ? [...picked, j.record_id].slice(0, 50) : picked.filter((x) => x !== j.record_id))} />
                      <span className="min-w-0">
                        <span className="font-medium">{j.name || j.email}</span>
                        <span className="text-muted-foreground"> · {j.outlets.join(", ")}</span>
                        {j.latest_article.title && <span className="block truncate text-[12px] text-muted-foreground">{j.latest_article.title}</span>}
                      </span>
                    </label>
                  ))}
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <select className={field} aria-label="Sequence" value={sequence} onChange={(e) => setSequence(e.target.value)}>
                    {sequences.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
                  </select>
                  <Button disabled={picked.length === 0 || !sequence || busy === "start"} onClick={start}>
                    Pitch {picked.length || ""} journalist{picked.length === 1 ? "" : "s"}
                  </Button>
                </div>
              </>
            )}
          </div>
        )}
        {list && list.length > 0 && (
          <ul className="divide-y">
            {list.map((p) => (
              <li key={p.id} className="space-y-1.5 py-2.5 text-[13px]">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{p.journalist_name}</span>
                  <Badge tone={pitchTone[p.status]}>{pitchLabel[p.status]}</Badge>
                  {p.reply_kind && <Badge tone={p.reply_kind === "covering" ? "green" : p.reply_kind === "wants_data" || p.reply_kind === "wants_interview" ? "blue" : "grey"}>{replyLabel[p.reply_kind]}</Badge>}
                  <span className="text-[12px] text-muted-foreground">{p.sequence_name}{p.approved_at ? ` · approved ${ago(p.approved_at)}` : ""}</span>
                </div>
                {p.article_title && <div className="text-[12px] text-muted-foreground">Their latest: {p.article_title}</div>}
                {p.status === "draft" && canManage ? (
                  <textarea className={`${field} w-full`} rows={3} aria-label={`Opener for ${p.journalist_name}`} placeholder="Write the opener: why it fits what they cover, then the news."
                    value={edits[p.id] ?? p.opener} onChange={(e) => setEdits({ ...edits, [p.id]: e.target.value })} maxLength={2000} />
                ) : (
                  p.opener && <div className="text-muted-foreground">{p.opener}</div>
                )}
                {p.reason && p.status === "draft" && <div className="text-[12px] text-amber-700 dark:text-amber-400">{p.reason.startsWith("check_failed") ? `Fix before approving: ${p.reason.slice(14)}` : `No opener yet (${p.reason}): write one.`}</div>}
                {p.status === "draft" && canManage && (
                  <div className="flex gap-2">
                    <Button disabled={busy === p.id || !(edits[p.id] ?? p.opener).trim()} onClick={() => approve(p)}>Approve</Button>
                    <Button variant="ghost" onClick={() => reject(p)}>Reject</Button>
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  );
}

function Experts({ ws, canManage, brand }: { ws: string; canManage: boolean; brand: { brand?: string } }) {
  const { active, byId } = useBrand(ws);
  const [list, setList] = useState<PRExpert[] | null>(null);
  const [draft, setDraft] = useState({ name: "", email: "", title: "", topics: "", brand_id: "" });
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const forBrand = brand.brand ? active.find((b) => b.handle === brand.brand)?.id : draft.brand_id || active[0]?.id;

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/pr-experts", { params: { path: { ws }, query: brand } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setList(data.data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, reload, brand.brand]);

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!draft.name.trim() || !draft.email.trim() || !forBrand) return;
    setBusy("add");
    const topics = draft.topics.split(",").map((t) => t.trim()).filter(Boolean);
    const { error } = await api.POST("/v1/workspaces/{ws}/pr-experts", {
      params: { path: { ws } },
      body: { brand_id: forBrand, name: draft.name.trim(), email: draft.email.trim(), title: draft.title.trim(), topics },
    });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setError("");
    setDraft({ ...draft, name: "", email: "", title: "", topics: "" });
    setReload((n) => n + 1);
  };
  const makeDefault = async (x: PRExpert) => {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/pr-experts/{expert}", { params: { path: { ws, expert: x.id } }, body: { is_default: true } });
    if (error) return setError(errorMessage(error));
    setReload((n) => n + 1);
  };
  const remove = async (x: PRExpert) => {
    if (!(await confirmAction(`Remove ${x.name} as an expert?`, "Their person record stays. Stories with their approved quotes go back to draft.", "Remove"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/pr-experts/{expert}", { params: { path: { ws, expert: x.id } } });
    if (error) return setError(errorMessage(error));
    setReload((n) => n + 1);
  };

  if (!list) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  return (
    <div className="space-y-4">
      {error && <ErrorText>{error}</ErrorText>}
      <p className="text-[12px] text-muted-foreground">The people each brand quotes. Only add people who agreed to be quoted. A quote with no expert named goes to the brand's default.</p>
      {canManage && active.length > 0 && (
        <form onSubmit={add} className="flex flex-wrap items-center gap-2">
          <input className={`${field} w-44`} aria-label="Name" placeholder="Ana Ruiz" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} maxLength={200} />
          <input className={`${field} w-56`} aria-label="Email" type="email" placeholder="ana@northpeak.example" value={draft.email} onChange={(e) => setDraft({ ...draft, email: e.target.value })} maxLength={320} />
          <input className={`${field} w-32`} aria-label="Title" placeholder="CEO" value={draft.title} onChange={(e) => setDraft({ ...draft, title: e.target.value })} maxLength={200} />
          <input className={`${field} w-56`} aria-label="Topics" placeholder="Topics, separated by commas" value={draft.topics} onChange={(e) => setDraft({ ...draft, topics: e.target.value })} />
          {!brand.brand && active.length > 1 && (
            <select className={field} aria-label="Brand" value={forBrand} onChange={(e) => setDraft({ ...draft, brand_id: e.target.value })}>
              {active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
            </select>
          )}
          <Button type="submit" disabled={!draft.name.trim() || !draft.email.trim() || busy === "add"}>Add expert</Button>
        </form>
      )}
      {list.length === 0 ? (
        <Empty title="No experts yet">Add the people your brand quotes, such as its founder, so stories can carry their approved quotes.</Empty>
      ) : (
        <DataTable
          data={list}
          rowId={(x) => x.id}
          searchPlaceholder="Search experts"
          empty="No experts match."
          mobile={(x) => (
            <div className="space-y-1">
              <div className="flex items-start justify-between gap-2">
                <span className="font-medium">{x.name}</span>
                {x.is_default && <Badge tone="green">Default</Badge>}
              </div>
              <div className="text-xs text-muted-foreground">{[x.title, byId(x.brand_id)?.name, x.topics.join(", ")].filter(Boolean).join(" · ")}</div>
            </div>
          )}
          columns={[
            {
              id: "name",
              header: "Expert",
              accessorFn: (x) => `${x.name} ${x.email ?? ""}`,
              cell: ({ row: { original: x } }) => (
                <div className="min-w-0 space-y-0.5">
                  <div className="font-medium">{x.name}{x.title && <span className="font-normal text-muted-foreground">, {x.title}</span>}</div>
                  {x.email && <div className="font-mono text-[12px] text-muted-foreground">{x.email}</div>}
                </div>
              ),
            },
            { id: "brand", header: "Brand", accessorFn: (x) => byId(x.brand_id)?.name ?? "", cell: ({ row }) => <span className="text-[12px]">{byId(row.original.brand_id)?.name ?? ""}</span> },
            { id: "topics", header: "Topics", accessorFn: (x) => x.topics.join(", "), cell: ({ row }) => <span className="text-[12px]">{row.original.topics.join(", ")}</span> },
            { id: "default", header: "Default", accessorFn: (x) => (x.is_default ? 1 : 0), cell: ({ row }) => row.original.is_default && <Badge tone="green">Default</Badge> },
            {
              id: "actions",
              header: () => <span className="sr-only">Actions</span>,
              enableSorting: false,
              meta: { align: "right" },
              cell: ({ row: { original: x } }) =>
                canManage && (
                  <Menu
                    aria={`More for ${x.name}`}
                    items={[
                      !x.is_default && { label: "Make the default", onClick: () => makeDefault(x) },
                      { label: "Remove", onClick: () => remove(x), danger: true },
                    ]}
                  />
                ),
            },
          ]}
        />
      )}
    </div>
  );
}
