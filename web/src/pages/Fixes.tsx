import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import type { Fix, FixGuideline, FixTicket, FixTicketDetail, SEOSite } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { BrandBadge } from "@/components/brand-badge";
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
type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Status = Fix["status"];

const statusLabel: Record<Status, string> = { open: "Open", in_progress: "In progress", done: "Done", ignored: "Ignored" };
const statusTone: Record<Status, Tone> = { open: "grey", in_progress: "blue", done: "green", ignored: "grey" };
const views = { todo: "To do", open: "Open", in_progress: "In progress", done: "Done", ignored: "Ignored", all: "Everything" } as const;
type View = keyof typeof views;
const severityRank: Record<string, number> = { error: 0, warning: 1, notice: 2 };
const subjectName = (f: Pick<Fix, "source" | "subject">, hosts: Record<string, string>) => (f.source === "social" ? "A social watch" : hosts[f.subject.id] ?? "A site");

export function Fixes({ ws, canManage, go }: Props) {
  const [tickets, setTickets] = useState<FixTicket[] | null>(null);
  const [sites, setSites] = useState<SEOSite[]>([]);
  const [error, setError] = useState("");
  const [view, setView] = useState<View>("todo");
  const [severity, setSeverity] = useState("");
  const [site, setSite] = useState("");
  const [picked, setPicked] = useState<string>("");
  const [autonomy, setAutonomy] = useState(false);
  const [rules, setRules] = useState(false);
  const { query: brandQuery, brand } = useBrand(ws);
  const brandKey = brandQuery.brand ?? "";

  const load = useCallback(async () => {
    const query = { ...(brandKey ? { brand: brandKey } : {}), status: view, ...(site ? { subject_id: site } : {}) };
    const { data, error } = await api.GET("/v1/workspaces/{ws}/fix-tickets", { params: { path: { ws }, query } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setTickets(data.data);
  }, [ws, brandKey, view, site]);
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites", { params: { path: { ws }, query: brandKey ? { brand: brandKey } : {} } }).then(({ data }) => setSites(data?.data ?? []));
  }, [ws, brandKey]);

  const hosts = useMemo(() => Object.fromEntries(sites.map((s) => [s.id, s.host])), [sites]);
  const shown = useMemo(() => (tickets ?? []).filter((t) => !severity || t.rule.severity === severity), [tickets, severity]);
  const current = tickets?.find((t) => t.id === picked);
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
            onRowClick={(t) => setPicked(t.id)}
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
      {current && <TicketSheet ws={ws} ticket={current} host={hosts[current.subject.id]} canManage={canManage} onClose={() => setPicked("")} onChange={changed} onPick={setPicked} reload={load} go={go} />}
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

function TicketSheet({
  ws,
  ticket,
  host,
  canManage,
  onClose,
  onChange,
  onPick,
  reload,
  go,
}: {
  ws: string;
  ticket: FixTicket;
  host?: string;
  canManage: boolean;
  onClose: () => void;
  onChange: (t: FixTicket) => void;
  onPick: (id: string) => void;
  reload: () => void;
  go: Props["go"];
}) {
  const [d, setD] = useState<FixTicketDetail | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [mode, setMode] = useState<"" | "feedback" | "reject">("");
  const [text, setText] = useState("");
  const [remember, setRemember] = useState<"" | "site" | "brand" | "workspace">("");

  const fetchDetail = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/fix-tickets/{ticket}", { params: { path: { ws, ticket: ticket.id } } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setD(data);
    onChange(data);
  }, [ws, ticket.id]); // onChange only updates the list row

  useEffect(() => {
    setD(null);
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
    const { error } = await api.POST("/v1/workspaces/{ws}/fix-tickets/{ticket}/write", { params: { path: { ws, ticket: ticket.id } }, body: feedback ? { feedback } : {} });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setMode("");
    setText("");
    toast(feedback ? "Rewriting it with your feedback." : "Writing the ticket. It takes a few seconds.");
    fetchDetail();
  }

  async function review(body: Review, done: string) {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/fix-tickets/{ticket}/review", { params: { path: { ws, ticket: ticket.id } }, body });
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
      const { error } = await api.PATCH("/v1/workspaces/{ws}/fixes/{fix}", { params: { path: { ws, fix: f.id } }, body });
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
      const { error } = await api.PATCH("/v1/workspaces/{ws}/fixes/{fix}", { params: { path: { ws, fix: f.id } }, body: { status: "done" } });
      if (error) {
        setBusy(false);
        return toast(errorMessage(error), "error");
      }
    }
    const { data, error } = await api.POST("/v1/workspaces/{ws}/fixes/{fix}/verify", { params: { path: { ws, fix: open[0].id } }, body: {} });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    if ("queued" in data) return toast("Held for approval by the mission's owner.");
    toast("Checking now. A fresh crawl confirms it, or opens it again if the issue is still there.");
    fetchDetail();
    reload();
  }

  async function copy() {
    setBusy(true);
    const { data, error } = await api.GET("/v1/workspaces/{ws}/fix-tickets/{ticket}/prompt", { params: { path: { ws, ticket: ticket.id } } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    await navigator.clipboard?.writeText(data.prompt);
    track("ui.copied");
    toast("Copied as an issue. Paste it into GitHub, Linear or your coding agent.");
  }

  const t = d ?? ticket;
  const b = t.brief_status === "ready" ? t.brief : null;
  const active = t.status === "open" || t.status === "in_progress";
  const where = t.source === "social" ? "A social watch" : host ?? "A site";
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{ticketTitle(t)}</SheetTitle>
          <SheetDescription>
            {where} · {plural(t.count, t.unit.replace(/s$/, ""))}
            {t.parts > 1 && ` across ${plural(t.parts, "URL template")}`}
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-6 px-4 pb-8 text-[13px]">
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge tone={severityTone[t.rule.severity]}>{severityLabel[t.rule.severity]}</Badge>
            <Badge tone={statusTone[t.status]}>{statusLabel[t.status]}</Badge>
            <TicketState t={t} />
            {b && <Badge>{kindLabel[b.kind]}</Badge>}
            <BrandBadge id={t.brand_id} />
            <span className="text-xs text-muted-foreground">Found {ago(t.created_at)}</span>
          </div>
          {error && <ErrorText>{error}</ErrorText>}
          {t.impact && (t.impact.clicks > 0 || t.impact.impressions > 0) && (
            <p className="rounded-lg bg-muted/50 px-3 py-2">
              These pages got <span className="font-medium">{t.impact.clicks.toLocaleString()} clicks</span> and <span className="font-medium">{t.impact.impressions.toLocaleString()} impressions</span> from Google search in the last 28 days.
            </p>
          )}

          {b ? (
            <div className="space-y-4">
              <p className="text-[14px] leading-relaxed">{b.summary}</p>
              <Section title="Why it matters">{b.why}</Section>
              <Section title="The change">
                <div className="whitespace-pre-wrap">{b.change}</div>
              </Section>
              {b.done_when.length > 0 && (
                <Section title="Done when">
                  <ul className="space-y-1">
                    {b.done_when.map((x, i) => (
                      <li key={i} className="flex gap-2">
                        <span aria-hidden className="mt-0.5 size-3.5 shrink-0 rounded border" />
                        <span>{x}</span>
                      </li>
                    ))}
                  </ul>
                </Section>
              )}
              {b.dont.length > 0 && (
                <Section title="Don't">
                  <ul className="list-disc space-y-1 pl-4">
                    {b.dont.map((x, i) => <li key={i}>{x}</li>)}
                  </ul>
                </Section>
              )}
              <p className="text-xs text-muted-foreground">
                Written by your AI model{t.brief_model && ` (${t.brief_model})`}{t.brief_written_at && ` ${ago(t.brief_written_at)}`}{t.brief_feedback && `, with your feedback: "${t.brief_feedback}"`}
              </p>
            </div>
          ) : (
            <div className="space-y-3">
              <Section title="Why it matters">{t.rule.why}</Section>
              <Section title="How to fix it">{t.rule.fix}</Section>
              {writing && <p className="text-muted-foreground">Your AI model is writing this ticket from the pages, your brand, the other fixes and your guidelines…</p>}
              {t.brief_status === "failed" && <ErrorText>{t.brief_error || "The AI model couldn't write it."}</ErrorText>}
              {canManage && !writing && (
                <div className="flex flex-wrap items-center gap-3 rounded-lg border p-3">
                  <div className="min-w-0 flex-1">
                    <div className="font-medium">Write it up as a ticket</div>
                    <p className="text-muted-foreground">Your own AI model turns this into an issue a developer or coding agent can pick up: what to change, why, and how to know it's done.</p>
                  </div>
                  <Button disabled={busy} onClick={() => write()}>{t.brief_status === "failed" ? "Try again" : "Write ticket"}</Button>
                </div>
              )}
            </div>
          )}

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
            <div className="space-y-2 rounded-lg border p-3">
              <div className="font-medium">{t.source === "seo_links" ? "A suggestion, not a task Outflare runs" : "Hand it on"}</div>
              <p className="text-muted-foreground">
                {t.source === "seo_links"
                  ? "Outflare never contacts a site, asks for a link or disavows anything for you. Copy it for your own agent, or look at the links yourself."
                  : "Copy it as an issue for GitHub, Linear or a coding agent, with every page and your guidelines. Once the change is live, check it: a fresh crawl confirms it or opens it again."}
              </p>
              <div className="flex flex-wrap gap-2">
                <Button variant="ghost" disabled={busy} onClick={copy}>Copy as issue</Button>
                {canManage && active && t.status !== "in_progress" && <Button variant="ghost" disabled={busy} onClick={take}>Take it</Button>}
                {canManage && active && t.source === "seo_audit" && <Button variant="ghost" disabled={busy || !d} onClick={live}>It's live, check it</Button>}
                {t.source === "seo_links" && <Button variant="ghost" onClick={() => go(t.rule.id === "link_gap_domain" ? "competitors" : "backlinks")}>{t.rule.id === "link_gap_domain" ? "Open Competitors" : "Open Backlinks"}</Button>}
              </div>
            </div>
          )}
          {t.source === "social" && <Button variant="ghost" onClick={() => go("reddit")}>Open Reddit</Button>}

          {!d && !error && <Loading />}
          {d && d.fixes.length > 0 && (
            <Section
              title={t.unit === "pages" ? "Pages" : "Where"}
              action={t.source === "seo_audit" && <button className="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline" onClick={() => go("audits", t.subject.id)}>Open the site's audit</button>}
            >
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
                              {p.detail && <span className="text-[12px] text-muted-foreground">{p.detail}</span>}
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

          {d && d.related.length > 0 && (
            <Section title="Other fixes on this site">
              <ul className="space-y-1">
                {d.related.map((r) => (
                  <li key={r.id}>
                    <button className="flex w-full flex-wrap items-center gap-2 text-left hover:underline" onClick={() => onPick(r.id)}>
                      <span className="min-w-0 flex-1">{r.title}</span>
                      {r.review && <Badge tone={r.review === "approved" ? "green" : "red"}>{r.review === "approved" ? "Approved" : "Rejected"}</Badge>}
                      <Badge tone={statusTone[r.status]}>{statusLabel[r.status]}</Badge>
                    </button>
                  </li>
                ))}
              </ul>
            </Section>
          )}
        </div>
      </SheetContent>
    </Sheet>
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
    const { data, error } = await api.GET("/v1/workspaces/{ws}/fix-guidelines", { params: { path: { ws } } });
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
    const { error } = await api.POST("/v1/workspaces/{ws}/fix-guidelines", { params: { path: { ws } }, body });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setText("");
    toast("Saved. Every ticket written from now on follows it.");
    load();
  }

  async function remove(g: FixGuideline) {
    const { error } = await api.DELETE("/v1/workspaces/{ws}/fix-guidelines/{guideline}", { params: { path: { ws, guideline: g.id } } });
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
