import { PickBrand } from "@/components/pick-brand";
import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import type { Fix, FixGuideline, FixTicket, FixTicketDetail, SEOSite } from "@outflare/sdk";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { BrandBadge } from "@/components/brand-badge";
import { Markdown } from "@/components/markdown-editor";
import { DataTable } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago, plural } from "../format";
import type { Tab } from "../App";
import { severityLabel, severityTone } from "./SiteAudits";
import { FixAutonomy } from "./FixAutonomy";

// The fixes list: one list of work for the whole workspace, shared by
// people and agents. Site audits are its first source and link
// opportunities (seo_links, suggestions only) the next; another source adds
// its fixes here rather than keeping a list of its own. The page only needs
// the fixes API, so it can move to wherever the list lives next. It shows
// the list as tickets: one per issue and site, with every page behind it,
// written up by the workspace's AI model and reviewed by a person.

type Tone = keyof typeof tones;
// open is the ticket whose page is showing, from the URL (/fixes/{ticket}).
type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void; open: string; onOpen: (ticket: string) => void };
type Status = Fix["status"];

const statusLabel: Record<Status, string> = { open: "Open", in_progress: "In progress", done: "Done", ignored: "Ignored" };
const statusTone: Record<Status, Tone> = { open: "grey", in_progress: "blue", done: "green", ignored: "grey" };
const views = { todo: "To do", open: "Open", in_progress: "In progress", done: "Done", ignored: "Ignored", all: "Everything" } as const;
type View = keyof typeof views;
const severityRank: Record<string, number> = { error: 0, warning: 1, notice: 2 };
const subjectName = (f: Pick<Fix, "source" | "subject">, hosts: Record<string, string>) => (f.source === "social" ? "A social watch" : f.source === "ai_visibility" ? "AI answers" : hosts[f.subject.id] ?? "A site");

// Fixes belong to one brand.
export function Fixes(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Fixes" brands={active.length} />;
  return <FixesPage key={brand.id} {...props} />;
}

function FixesPage({ ws, canManage, go, open, onOpen }: Props) {
  const [tickets, setTickets] = useState<FixTicket[] | null>(null);
  const [sites, setSites] = useState<SEOSite[]>([]);
  const [error, setError] = useState("");
  const [view, setView] = useState<View>("todo");
  const [severity, setSeverity] = useState("");
  const [site, setSite] = useState("");
  const [autonomy, setAutonomy] = useState(false);
  const [rules, setRules] = useState(false);
  const { query: brandQuery, brand } = useBrand(ws);
  const brandKey = brandQuery.brand ?? "";

  const load = useCallback(async () => {
    const query = { ...(brandKey ? { brand: brandKey } : {}), status: view, ...(site ? { subject_id: site } : {}) };
    const { data, error } = await api.GET("/v1/brands/{brand}/fix-tickets", { params: { path: { brand: pickedBrandId() }, query } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setTickets(data.data);
  }, [ws, brandKey, view, site]);
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/seo-sites", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => setSites(data?.data ?? []));
  }, [ws, brandKey]);

  const hosts = useMemo(() => Object.fromEntries(sites.map((s) => [s.id, s.host])), [sites]);
  const shown = useMemo(() => (tickets ?? []).filter((t) => !severity || t.rule.severity === severity), [tickets, severity]);
  const changed = (t: FixTicket) => setTickets((all) => all?.map((x) => (x.id === t.id ? { ...x, ...t } : x)) ?? null);

  const header = (
    <PageHeader
      title="Fixes"
      description="Everything worth fixing on your sites, one ticket per problem with every page it affects. Your AI model writes each ticket like an issue for a developer; you approve it, reject it or ask for changes, and what you decide shapes the next ones."
      action={
        <div className="flex gap-2">
          <Button variant="ghost" onClick={() => setRules(true)}>Guidelines</Button>
          {canManage && <Button variant="ghost" onClick={() => setAutonomy(true)}>Agent autonomy</Button>}
          <Button variant="ghost" onClick={() => go("audits")}>Site audits</Button>
        </div>
      }
    />
  );
  const sheets = (
    <>
      <Sheet open={autonomy} onOpenChange={setAutonomy}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
          <SheetHeader className="p-0">
            <SheetTitle>Agent autonomy</SheetTitle>
            <SheetDescription>How far agents in a mission may go with this list on their own. Agents take a fix before they change the site; this decides whether that waits for a person.</SheetDescription>
          </SheetHeader>
          {autonomy && <FixAutonomy ws={ws} sites={sites} />}
        </SheetContent>
      </Sheet>
      {rules && <GuidelinesSheet ws={ws} sites={sites} brandId={brand?.id} brandName={brand?.name} canManage={canManage} onClose={() => setRules(false)} />}
    </>
  );
  // One ticket is a page of its own, inside the app like any other.
  if (open) {
    return (
      <>
        {sheets}
        <TicketPage ws={ws} id={open} ticket={tickets?.find((t) => t.id === open)} hosts={hosts} canManage={canManage} onBack={() => { onOpen(""); load(); }} onChange={changed} reload={load} go={go} />
      </>
    );
  }
  if (error && !tickets) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!tickets) return <div className="space-y-6">{header}<Loading /></div>;

  const filtered = view !== "todo" || severity || site;
  const counts = {
    errors: shown.filter((t) => t.rule.severity === "error").length,
    review: shown.filter((t) => t.brief_status === "ready" && t.review === "").length,
    pages: shown.reduce((n, t) => n + (t.unit === "pages" ? t.count : 0), 0),
  };

  const toolbar = (
    <>
      <select className={field} aria-label="Show" value={view} onChange={(e) => setView(e.target.value as View)}>
        {(Object.keys(views) as View[]).map((v) => <option key={v} value={v}>{views[v]}</option>)}
      </select>
      <select className={field} aria-label="Severity" value={severity} onChange={(e) => setSeverity(e.target.value)}>
        <option value="">Every severity</option>
        {Object.entries(severityLabel).map(([k, v]) => <option key={k} value={k}>{v}s</option>)}
      </select>
      {sites.length > 1 && (
        <select className={field} aria-label="Site" value={site} onChange={(e) => setSite(e.target.value)}>
          <option value="">Every site</option>
          {sites.map((s) => <option key={s.id} value={s.id}>{s.host}</option>)}
        </select>
      )}
    </>
  );

  return (
    <div className="space-y-6">
      {header}
      {sheets}
      {error && <ErrorText>{error}</ErrorText>}
      {tickets.length === 0 && !filtered && (
        <Empty title={sites.length ? "Nothing to fix" : "Nothing on the list yet"} action={!sites.length && <Button onClick={() => go("audits")}>Audit a site</Button>}>
          {sites.length ? "The latest audits found nothing worth fixing. New issues show up here after each crawl." : "Site audits are the first thing that adds fixes here. Add your website and the first crawl fills this list."}
        </Empty>
      )}
      {(tickets.length > 0 || filtered) && (
        <>
          <Stats cols={4}>
            <Stat label="Tickets" value={shown.length.toLocaleString()} />
            <Stat label="Errors" value={counts.errors.toLocaleString()} tone={counts.errors > 0 ? "red" : undefined} />
            <Stat label="Waiting for your review" value={counts.review.toLocaleString()} tone={counts.review > 0 ? "amber" : undefined} />
            <Stat label="Pages to fix" value={counts.pages.toLocaleString()} />
          </Stats>
          <DataTable
            data={shown}
            rowId={(t) => t.id}
            onRowClick={(t) => onOpen(t.id)}
            searchPlaceholder="Search fixes"
            toolbar={toolbar}
            empty="Nothing matches these filters."
            mobile={(t) => (
              <div className="space-y-1.5">
                <div className="flex items-start justify-between gap-2">
                  <span className="font-medium">{ticketTitle(t)}</span>
                  <Badge tone={severityTone[t.rule.severity]}>{severityLabel[t.rule.severity]}</Badge>
                </div>
                <div className="text-xs text-muted-foreground">{subjectName(t, hosts)} · {plural(t.count, t.unit.replace(/s$/, ""))} · {statusLabel[t.status]}</div>
              </div>
            )}
            columns={[
              { id: "severity", header: "Severity", accessorFn: (t) => severityRank[t.rule.severity], cell: ({ row }) => <Badge tone={severityTone[row.original.rule.severity]}>{severityLabel[row.original.rule.severity]}</Badge> },
              {
                id: "fix",
                header: "Fix",
                accessorFn: (t) => `${ticketTitle(t)} ${t.rule.title} ${hosts[t.subject.id] ?? ""}`,
                cell: ({ row: { original: t } }) => (
                  <div className="space-y-0.5">
                    <div className="font-medium">{ticketTitle(t)}</div>
                    <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                      <span>{subjectName(t, hosts)}</span>
                      {t.parts > 1 && <span>· {plural(t.parts, "template")}</span>}
                      <BrandBadge id={t.brand_id} />
                    </div>
                  </div>
                ),
              },
              { id: "count", header: "How many", accessorFn: (t) => t.count, meta: { align: "right" }, cell: ({ row }) => <span className="whitespace-nowrap">{plural(row.original.count, row.original.unit.replace(/s$/, ""))}</span> },
              { id: "ticket", header: "Ticket", accessorFn: (t) => ticketState(t).label, cell: ({ row }) => <TicketState t={row.original} /> },
              { id: "status", header: "Status", accessorFn: (t) => t.status, cell: ({ row: { original: t } }) => <Badge tone={statusTone[t.status]}>{statusLabel[t.status]}</Badge> },
              { id: "updated", header: "Updated", accessorFn: (t) => t.updated_at, cell: ({ row }) => <span className="text-muted-foreground">{ago(row.original.updated_at)}</span> },
            ]}
          />
        </>
      )}
    </div>
  );
}

type Review = { decision: "approve" | "reject" | "reopen"; note?: string; guideline?: "site" | "brand" | "workspace" };
const kindLabel = { code: "Code change", content: "Content change", config: "Settings change" } as const;
const scopeLabel = { workspace: "Every site", brand: "This brand", site: "This site" } as const;

function ticketTitle(t: FixTicket) {
  return t.brief?.title || t.title || t.rule.title;
}

function ticketState(t: FixTicket): { label: string; tone: Tone } {
  if (t.review === "approved") return { label: "Approved", tone: "green" };
  if (t.review === "rejected") return { label: "Rejected", tone: "red" };
  if (t.brief_status === "queued" || t.brief_status === "writing") return { label: "Writing", tone: "blue" };
  if (t.brief_status === "failed") return { label: "Couldn't write", tone: "amber" };
  if (t.brief_status === "ready") return { label: "Needs review", tone: "amber" };
  return { label: "Not written", tone: "grey" };
}

function TicketState({ t }: { t: FixTicket }) {
  const st = ticketState(t);
  return <Badge tone={st.tone}>{st.label}</Badge>;
}

// What each label means, so nobody has to guess.
const severityMeans: Record<string, string> = {
  error: "Breaks the page for visitors or search engines. Fix these first.",
  warning: "Hurts how the page is found or shown. Fix it soon.",
  notice: "Minor. Worth tidying, not urgent.",
};
const statusMeans: Record<Status, string> = {
  open: "Nobody has picked it up yet.",
  in_progress: "Someone is working on it.",
  done: "Fixed. The next crawl confirms it, or opens it again.",
  ignored: "Set aside. Its pages won't come back as fixes.",
};
const ticketMeans: Record<string, string> = {
  Approved: "You agreed with the write-up. It's ready to hand on.",
  Rejected: "You turned it down.",
  Writing: "Your AI model is writing it up now.",
  "Couldn't write": "Your AI model couldn't write it up. Try again.",
  "Needs review": "Your AI model wrote it up. Read it and approve or reject it.",
  "Not written": "Only the audit's short note so far. Your AI model can write the full ticket.",
};

// One ticket as a page: what is wrong and what to do on the left, its
// labels explained and what you can do with it on the right.
function TicketPage({
  ws,
  id,
  ticket,
  hosts,
  canManage,
  onBack,
  onChange,
  reload,
  go,
}: {
  ws: string;
  id: string;
  // The list's row, shown while the full ticket loads; absent on a direct link.
  ticket?: FixTicket;
  hosts: Record<string, string>;
  canManage: boolean;
  onBack: () => void;
  onChange: (t: FixTicket) => void;
  reload: () => void;
  go: Props["go"];
}) {
  const [d, setD] = useState<FixTicketDetail | null>(null);
  const [error, setError] = useState("");
  const [missing, setMissing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [mode, setMode] = useState<"" | "feedback" | "reject">("");
  const [text, setText] = useState("");
  const [remember, setRemember] = useState<"" | "site" | "brand" | "workspace">("");

  const fetchDetail = useCallback(async () => {
    const { data, error, response } = await api.GET("/v1/brands/{brand}/fix-tickets/{ticket}", { params: { path: { brand: pickedBrandId(), ticket: id } } });
    if (response.status === 404) return setMissing(true);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setD(data);
    onChange(data);
  }, [ws, id]); // eslint-disable-line react-hooks/exhaustive-deps -- onChange only updates the list row

  useEffect(() => {
    setD(null);
    setMissing(false);
    setMode("");
    setText("");
    setRemember("");
    fetchDetail();
  }, [fetchDetail]);
  // While the AI writes, look again every few seconds.
  const writing = d?.brief_status === "queued" || d?.brief_status === "writing";
  useEffect(() => {
    if (!writing) return;
    const t = setInterval(fetchDetail, 3000);
    return () => clearInterval(t);
  }, [writing, fetchDetail]);

  async function write(feedback?: string) {
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/fix-tickets/{ticket}/write", { params: { path: { brand: pickedBrandId(), ticket: id } }, body: feedback ? { feedback } : {} });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setMode("");
    setText("");
    toast(feedback ? "Rewriting it with your feedback." : "Writing the ticket. It takes a few seconds.");
    fetchDetail();
  }

  async function review(body: Review, done: string) {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/fix-tickets/{ticket}/review", { params: { path: { brand: pickedBrandId(), ticket: id } }, body });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    setMode("");
    setText("");
    setRemember("");
    toast(done);
    fetchDetail();
    reload();
  }

  // Ticket-wide actions change each of its open parts.
  async function eachOpen(body: { status: "in_progress" | "done"; assignee_id?: string }) {
    const open = (d?.fixes ?? []).filter((f) => f.status === "open" || f.status === "in_progress");
    for (const f of open) {
      const { error } = await api.PATCH("/v1/brands/{brand}/fixes/{fix}", { params: { path: { brand: pickedBrandId(), fix: f.id } }, body });
      if (error) return errorMessage(error);
    }
    return "";
  }

  async function take() {
    setBusy(true);
    const err = await eachOpen({ status: "in_progress", assignee_id: "me" });
    setBusy(false);
    if (err) return toast(err, "error");
    toast("It's yours.");
    fetchDetail();
    reload();
  }

  async function live() {
    const open = (d?.fixes ?? []).filter((f) => f.status === "open" || f.status === "in_progress");
    if (!open.length) return;
    setBusy(true);
    const rest = open.slice(1);
    for (const f of rest) {
      const { error } = await api.PATCH("/v1/brands/{brand}/fixes/{fix}", { params: { path: { brand: pickedBrandId(), fix: f.id } }, body: { status: "done" } });
      if (error) {
        setBusy(false);
        return toast(errorMessage(error), "error");
      }
    }
    const { data, error } = await api.POST("/v1/brands/{brand}/fixes/{fix}/verify", { params: { path: { brand: pickedBrandId(), fix: open[0].id } }, body: {} });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    if ("queued" in data) return toast("Held for approval by the mission's owner.");
    toast("Checking now. A fresh crawl confirms it, or opens it again if the issue is still there.");
    fetchDetail();
    reload();
  }

  // Two ways to hand a ticket on: as an issue a developer or a tracker
  // takes, or as instructions a coding agent can act on alone.
  async function copy(as: "issue" | "agent") {
    setBusy(true);
    const { data, error } = await api.GET("/v1/brands/{brand}/fix-tickets/{ticket}/prompt", { params: { path: { brand: pickedBrandId(), ticket: id }, query: { for: as } } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    await navigator.clipboard?.writeText(data.prompt);
    track("ui.fix_ticket_copied", { as });
    toast(as === "agent" ? "Copied. Paste it into Claude Code, Cursor or Codex, in your website's repository." : "Copied. Paste it into GitHub, Linear or a message to your developer.");
  }

  async function copyLink(url: string) {
    await navigator.clipboard?.writeText(url);
    track("ui.fix_ticket_copied", { as: "link" });
    toast("Link copied.");
  }

  const t = d ?? ticket;
  // The breadcrumb names the ticket rather than its ID.
  const name = t ? ticketTitle(t) : "";
  useEffect(() => {
    if (name) window.dispatchEvent(new CustomEvent("outflare:item-name", { detail: { id, name } }));
  }, [id, name]);

  const back = { label: "Fixes", onClick: onBack };
  if (missing) {
    return (
      <div className="space-y-6">
        <PageHeader title="Fix not found" back={back} />
        <Empty title="This fix isn't here" action={<Button onClick={onBack}>See every fix</Button>}>It may belong to another brand, or the issue went away and the ticket with it.</Empty>
      </div>
    );
  }
  if (!t) return <div className="space-y-6"><PageHeader title="Fix" back={back} />{error ? <ErrorText>{error}</ErrorText> : <Loading />}</div>;

  const b = t.brief_status === "ready" ? t.brief : null;
  const active = t.status === "open" || t.status === "in_progress";
  const where = subjectName(t, hosts);
  const thing = t.unit.replace(/s$/, "");
  const state = ticketState(t);
  // A few pages shown with what the audit read on them, before the full list.
  const examples = (d?.pages ?? []).filter((p) => p.detail).slice(0, 3);
  return (
    <div>
      <PageHeader
        title={<>{name}{t.number > 0 && <span className="font-mono text-[13px] font-normal text-muted-foreground">#{t.number}</span>}<BrandBadge id={t.brand_id} /></>}
        back={back}
        description={
          <>
            {plural(t.count, thing)} on {where}
            {t.parts > 1 && `, in ${plural(t.parts, "URL template")} (pages built the same way)`}. Found {ago(t.created_at)}.
          </>
        }
        action={
          t.source !== "social" && t.status !== "ignored" && (
            <>
              <Button variant="ghost" disabled={busy} onClick={() => copy("issue")}>Copy</Button>
              <Button disabled={busy} onClick={() => copy("agent")}>Give to agent</Button>
            </>
          )
        }
      />
      {error && <ErrorText>{error}</ErrorText>}
      <div className="grid gap-8 text-[13px] lg:grid-cols-[minmax(0,1fr)_20rem]">
        <div className="min-w-0 space-y-6">
          {b ? (
            <div className="space-y-4">
              <Markdown text={b.summary} className={`${prose} text-[14px]`} />
              <Section title="Why it matters"><Markdown text={b.why} className={prose} /></Section>
              <Section title="What to change"><Markdown text={b.change} className={prose} /></Section>
              {b.done_when.length > 0 && (
                <Section title="Done when">
                  <ul className="space-y-1">
                    {b.done_when.map((x, i) => (
                      <li key={i} className="flex gap-2">
                        <span aria-hidden className="mt-0.5 size-3.5 shrink-0 rounded border" />
                        <Markdown text={x} className={prose} />
                      </li>
                    ))}
                  </ul>
                </Section>
              )}
              {b.dont.length > 0 && (
                <Section title="Don't">
                  <ul className="list-disc space-y-1 pl-4">
                    {b.dont.map((x, i) => <li key={i}><Markdown text={x} className={prose} /></li>)}
                  </ul>
                </Section>
              )}
              <p className="text-xs text-muted-foreground">
                Written by your AI model{t.brief_model && ` (${t.brief_model})`}{t.brief_written_at && ` ${ago(t.brief_written_at)}`}{t.brief_feedback && `, with your feedback: "${t.brief_feedback}"`}
              </p>
            </div>
          ) : (
            <div className="space-y-4">
              <Section title="What's wrong">
                <p className="text-[14px] leading-relaxed">{t.rule.why}</p>
              </Section>
              {examples.length > 0 && (
                <Section title="For example">
                  <ul className="space-y-1.5">
                    {examples.map((p, i) => (
                      <li key={i}>
                        <a className="font-mono text-[12px] break-all underline-offset-2 hover:underline" href={p.url} target="_blank" rel="noreferrer noopener">{p.url}</a>
                        <div className="text-[12px] break-all text-muted-foreground">{p.detail}</div>
                      </li>
                    ))}
                  </ul>
                </Section>
              )}
              <Section title="How to fix it">{t.rule.fix}</Section>
              {writing && <p className="text-muted-foreground">Your AI model is writing this ticket from the pages, your brand, the other fixes and your guidelines…</p>}
              {t.brief_status === "failed" && <ErrorText>{t.brief_error || "The AI model couldn't write it."}</ErrorText>}
              {canManage && !writing && (
                <div className="flex flex-wrap items-center gap-3 rounded-lg border p-3">
                  <div className="min-w-0 flex-1">
                    <div className="font-medium">Want the full picture? Write it up as a ticket</div>
                    <p className="text-muted-foreground">This is only the audit's short note. Your own AI model reads these pages and writes what exactly to change, why, and how to know it's done, ready for a developer or coding agent.</p>
                  </div>
                  <Button disabled={busy} onClick={() => write()}>{t.brief_status === "failed" ? "Try again" : "Write ticket"}</Button>
                </div>
              )}
            </div>
          )}

          {t.impact && (t.impact.clicks > 0 || t.impact.impressions > 0) && (
            <p className="rounded-lg bg-muted/50 px-3 py-2">
              In the last 28 days Google showed these pages in search <span className="font-medium">{plural(t.impact.impressions, "time")}</span>, and people clicked through <span className="font-medium">{plural(t.impact.clicks, "time")}</span>.
            </p>
          )}

          {!d && !error && <Loading />}
          {d && d.fixes.length > 0 && (
            <Section
              title={t.unit === "pages" ? "Every page it affects" : "Where"}
              action={t.source === "seo_audit" && <button className="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline" onClick={() => go("audits", t.subject.id)}>Open the site's audit</button>}
            >
              {d.fixes.length > 1 && <p className="mb-3 text-muted-foreground">Grouped by URL template. Pages that share one are usually built by the same code, so one change fixes the whole group.</p>}
              <div className="space-y-3">
                {d.fixes.map((f) => {
                  const pages = d.pages.filter((p) => p.fix_id === f.id);
                  return (
                    <div key={f.id} className="space-y-1">
                      {(d.fixes.length > 1 || f.group) && (
                        <div className="flex flex-wrap items-center gap-2 text-xs">
                          <span className="font-mono">{f.group || "Everywhere"}</span>
                          <span className="text-muted-foreground">{plural(f.count, f.unit.replace(/s$/, ""))}</span>
                          <Badge tone={statusTone[f.status]}>{statusLabel[f.status]}</Badge>
                          {f.assignee && <span className="text-muted-foreground">{f.assignee.name}</span>}
                        </div>
                      )}
                      {pages.length > 0 && (
                        <ul className="space-y-0.5 pl-3">
                          {pages.map((p, i) => (
                            <li key={i} className="flex flex-wrap items-baseline gap-2">
                              <a className="font-mono text-[12px] break-all underline-offset-2 hover:underline" href={p.url} target="_blank" rel="noreferrer noopener">{p.url}</a>
                              {p.detail && <span className="text-[12px] break-all text-muted-foreground">{p.detail}</span>}
                            </li>
                          ))}
                          {f.unit === "pages" && f.count > pages.length && <li className="text-[12px] text-muted-foreground">And {plural(f.count - pages.length, "more page")}.</li>}
                        </ul>
                      )}
                    </div>
                  );
                })}
              </div>
            </Section>
          )}
        </div>

        <div className="space-y-4">
          <dl className="space-y-3 rounded-lg border p-3">
            {t.number > 0 && (
              <Fact label="Ticket" badge={<span className="font-mono">#{t.number}</span>}>
                Write "{`Outflare fix #${t.number}`}" in the pull request or commit that fixes it, so the change leads back here.{" "}
                <button className="underline underline-offset-2 hover:text-foreground" onClick={() => copyLink(t.url)}>Copy the link to this ticket</button>
              </Fact>
            )}
            <Fact label="Severity" badge={<Badge tone={severityTone[t.rule.severity]}>{severityLabel[t.rule.severity]}</Badge>}>{severityMeans[t.rule.severity]}</Fact>
            <Fact label="Status" badge={<Badge tone={statusTone[t.status]}>{statusLabel[t.status]}</Badge>}>{statusMeans[t.status]}</Fact>
            <Fact label="Write-up" badge={<Badge tone={state.tone}>{state.label}</Badge>}>{ticketMeans[state.label]}</Fact>
            {b && <Fact label="Kind of work" badge={<Badge>{kindLabel[b.kind]}</Badge>} />}
          </dl>

          {canManage && (b || t.review) && (
            <div className="space-y-3 rounded-lg border p-3">
              {t.review ? (
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <span className="font-medium">{t.review === "approved" ? "Approved" : "Rejected"}</span>
                    {t.reviewed_by && <span className="text-muted-foreground"> by {t.reviewed_by.name}</span>}
                    {t.reviewed_at && <span className="text-muted-foreground"> {ago(t.reviewed_at)}</span>}
                    {t.review_note && <p className="mt-1 text-muted-foreground">"{t.review_note}"</p>}
                  </div>
                  <Button variant="ghost" disabled={busy} onClick={() => review({ decision: "reopen" }, "Opened again.")}>Undo</Button>
                </div>
              ) : (
                <>
                  <div>
                    <div className="font-medium">Is this right?</div>
                    <p className="text-muted-foreground">What you decide here teaches the next tickets. Save a reason as a guideline and every ticket after this follows it.</p>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button disabled={busy} onClick={() => review({ decision: "approve" }, "Approved. Hand it on below.")}>Approve</Button>
                    <Button variant="ghost" disabled={busy} onClick={() => setMode(mode === "feedback" ? "" : "feedback")}>Ask for changes</Button>
                    <Button variant="ghost" disabled={busy} onClick={() => setMode(mode === "reject" ? "" : "reject")}>Reject</Button>
                  </div>
                </>
              )}
              {mode && (
                <form
                  className="space-y-2"
                  onSubmit={(e) => {
                    e.preventDefault();
                    if (mode === "feedback") return write(text.trim());
                    review({ decision: "reject", note: text.trim(), ...(remember ? { guideline: remember } : {}) }, remember ? "Rejected, and saved as a guideline." : "Rejected. Its pages won't come back as fixes.");
                  }}
                >
                  <textarea
                    className={`${field} min-h-20 w-full`}
                    value={text}
                    maxLength={mode === "feedback" ? 2000 : 1000}
                    autoFocus
                    onChange={(e) => setText(e.target.value)}
                    placeholder={mode === "feedback" ? "What should change? For example: keep titles under 55 characters and lead with the product name." : "Why not? For example: these pages are meant to be noindex."}
                  />
                  {mode === "reject" && (
                    <label className="flex flex-wrap items-center gap-2">
                      <span>Remember this for</span>
                      <select className={field} value={remember} onChange={(e) => setRemember(e.target.value as typeof remember)}>
                        <option value="">Only this ticket</option>
                        <option value="site">This issue on this site</option>
                        {t.brand_id && <option value="brand">Every ticket for this brand</option>}
                        <option value="workspace">Every ticket</option>
                      </select>
                    </label>
                  )}
                  <div className="flex gap-2">
                    <Button type="submit" disabled={busy || !text.trim()}>{mode === "feedback" ? "Rewrite" : "Reject"}</Button>
                    <Button type="button" variant="ghost" onClick={() => setMode("")}>Cancel</Button>
                  </div>
                </form>
              )}
            </div>
          )}

          {t.source !== "social" && t.status !== "ignored" && (
            <div className="space-y-3 rounded-lg border p-3">
              <div>
                <div className="font-medium">{t.source === "seo_links" ? "A suggestion, not a task Outflare runs" : "Hand it on"}</div>
                {t.source === "seo_links" && <p className="text-muted-foreground">Outflare never contacts a site, asks for a link or disavows anything for you. Copy it for your own agent, or look at the links yourself.</p>}
              </div>
              <HandOn title="Give to agent" action="Copy for an agent" primary disabled={busy} onClick={() => copy("agent")}>
                Instructions for Claude Code, Cursor or Codex. Paste them in your website's repository: the agent finds the code, makes the change and opens a pull request that names this ticket.
              </HandOn>
              <HandOn title="Give to a developer" action="Copy" disabled={busy} onClick={() => copy("issue")}>
                The ticket as an issue for GitHub or Linear, with every page, your guidelines and the link back here.
              </HandOn>
              {canManage && active && t.status !== "in_progress" && (
                <HandOn title="Doing it yourself?" action="Take it" disabled={busy} onClick={take}>
                  Puts your name on it, so nobody else starts the same work.
                </HandOn>
              )}
              {canManage && active && t.source === "seo_audit" && (
                <HandOn title="Is the change live?" action="Check it" disabled={busy || !d} onClick={live}>
                  Outflare crawls the site again and closes the ticket, or opens it again if the problem is still there.
                </HandOn>
              )}
              {t.source === "seo_links" && <Button variant="ghost" onClick={() => go(t.rule.id === "link_gap_domain" ? "competitors" : "backlinks")}>{t.rule.id === "link_gap_domain" ? "Open Competitors" : "Open Backlinks"}</Button>}
            </div>
          )}
          {t.source === "social" && <Button variant="ghost" onClick={() => go("reddit")}>Open Reddit</Button>}

          {d && d.guidelines.length > 0 && (
            <Section title="Guidelines it follows">
              <ul className="space-y-1">
                {d.guidelines.map((g) => (
                  <li key={g.id} className="flex gap-2">
                    <Badge>{scopeLabel[g.scope]}</Badge>
                    <span>{g.text}</span>
                  </li>
                ))}
              </ul>
            </Section>
          )}
        </div>
      </div>
    </div>
  );
}

// The write-up's own formatting, sized like the text around it.
const prose = "min-w-0 text-[13px] [&>*:first-child]:mt-0 [&>*:last-child]:mb-0";

// One way to hand a ticket on: what it is for, then the button.
function HandOn({ title, action, primary, disabled, onClick, children }: { title: string; action: string; primary?: boolean; disabled?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <div className="space-y-1.5 border-t pt-3">
      <div className="font-medium">{title}</div>
      <p className="text-muted-foreground">{children}</p>
      <Button variant={primary ? undefined : "ghost"} disabled={disabled} onClick={onClick}>{action}</Button>
    </div>
  );
}

// One label of a ticket, with what it means in plain words.
function Fact({ label, badge, children }: { label: string; badge: ReactNode; children?: ReactNode }) {
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between gap-2">
        <dt className="text-muted-foreground">{label}</dt>
        <dd>{badge}</dd>
      </div>
      {children && <dd className="text-xs text-muted-foreground">{children}</dd>}
    </div>
  );
}

function Section({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-2">
        <div className="text-xs font-medium tracking-wide text-muted-foreground uppercase">{title}</div>
        {action}
      </div>
      <div>{children}</div>
    </div>
  );
}

function GuidelinesSheet({ ws, sites, brandId, brandName, canManage, onClose }: { ws: string; sites: SEOSite[]; brandId?: string; brandName?: string; canManage: boolean; onClose: () => void }) {
  const [list, setList] = useState<FixGuideline[] | null>(null);
  const [error, setError] = useState("");
  const [text, setText] = useState("");
  const [scope, setScope] = useState(brandId ? "brand" : "workspace");
  const [busy, setBusy] = useState(false);
  const hosts = useMemo(() => Object.fromEntries(sites.map((s) => [s.id, s.host])), [sites]);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/fix-guidelines", { params: { path: { brand: pickedBrandId() } } });
    if (error || !data) return setError(errorMessage(error));
    setList(data.data);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);

  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const body = { text: text.trim(), ...(scope === "brand" && brandId ? { brand_id: brandId } : {}), ...(scope.startsWith("site:") ? { subject_id: scope.slice(5) } : {}) };
    const { error } = await api.POST("/v1/brands/{brand}/fix-guidelines", { params: { path: { brand: pickedBrandId() } }, body });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setText("");
    toast("Saved. Every ticket written from now on follows it.");
    load();
  }

  async function remove(g: FixGuideline) {
    const { error } = await api.DELETE("/v1/brands/{brand}/fix-guidelines/{guideline}", { params: { path: { brand: pickedBrandId(), guideline: g.id } } });
    if (error) return toast(errorMessage(error), "error");
    setList((l) => l?.filter((x) => x.id !== g.id) ?? null);
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>Guidelines</SheetTitle>
          <SheetDescription>Rules every fix ticket follows, so the AI writes them your way: "titles end with | Northpeak", "never change /legal/*". Reasons you give when rejecting a ticket can land here too.</SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4 pb-6 text-[13px]">
          {canManage && (
            <form className="space-y-2" onSubmit={add}>
              <textarea className={`${field} min-h-16 w-full`} value={text} maxLength={1000} onChange={(e) => setText(e.target.value)} placeholder="A rule for every ticket, in plain words" />
              <div className="flex flex-wrap items-center gap-2">
                <select className={field} aria-label="For" value={scope} onChange={(e) => setScope(e.target.value)}>
                  <option value="workspace">Every site</option>
                  {brandId && <option value="brand">{brandName ?? "This brand"}</option>}
                  {sites.map((s) => <option key={s.id} value={`site:${s.id}`}>{s.host}</option>)}
                </select>
                <Button type="submit" disabled={busy || !text.trim()}>Add</Button>
              </div>
            </form>
          )}
          {error && <ErrorText>{error}</ErrorText>}
          {!list && !error && <Loading />}
          {list && list.length === 0 && <p className="text-muted-foreground">No guidelines yet. Add one, or save a reason when you reject a ticket.</p>}
          {list && list.length > 0 && (
            <ul className="divide-y rounded-lg border">
              {list.map((g) => (
                <li key={g.id} className="flex items-start gap-3 p-3">
                  <div className="min-w-0 flex-1 space-y-1">
                    <div>{g.text}</div>
                    <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                      <Badge>{g.scope === "site" ? hosts[g.subject_id ?? ""] ?? "A site" : scopeLabel[g.scope]}</Badge>
                      {g.scope === "brand" && <BrandBadge id={g.brand_id} />}
                      {g.rule && <span className="font-mono">{g.rule}</span>}
                      {g.created_by && <span>{g.created_by.name}</span>}
                      <span>{ago(g.created_at)}</span>
                    </div>
                  </div>
                  {canManage && <Button variant="ghost" onClick={() => remove(g)}>Remove</Button>}
                </li>
              ))}
            </ul>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
