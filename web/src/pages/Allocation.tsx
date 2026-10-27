import { useCallback, useEffect, useState } from "react";
import type { AllocationBlocking, AllocationChange, AllocationMode, AllocationSuggestion, PlatformAccount } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, confirmAction, Empty, field, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import type { Tab } from "../App";
import { DataTable } from "@/components/data-table";

type Shortage = { sequence_id: string; sequence_name: string; needed_daily: number; active_daily: number; blocking: AllocationBlocking[]; note: string };
type Orphan = { sequence_id: string; sequence_name: string; leads: number };
type Attention = { approvals: AllocationSuggestion[]; suggestions_to_review: number; shortages: Shortage[]; orphaned_follow_ups: Orphan[]; unconfirmed_senders: number; platforms: PlatformAccount[] };

export const actionLabel: Record<string, string> = { assign: "Assigned", release: "Released", move: "Moved", drain: "Draining", finish_drain: "Joined after draining" };
const needsLabel: Record<string, string> = { review: "Review", team_approval: "Team approval", owner_approval: "Owner approval" };

/** Why mailboxes could not be used, as one short line. */
export function blockingText(b: AllocationBlocking[] | undefined): string {
  return (b ?? []).map((x) => `${x.count} ${x.reason}`).join(" · ");
}

// Where mailbox allocation needs a person, and what it has done.
export function Allocation({ ws, canManage, canSetMode, go }: { ws: string; canManage: boolean; canSetMode: boolean; go: (tab: Tab, item?: string) => void }) {
  const [attention, setAttention] = useState<Attention | null>(null);
  const [suggestions, setSuggestions] = useState<AllocationSuggestion[]>([]);
  const [changes, setChanges] = useState<AllocationChange[]>([]);
  const [mode, setMode] = useState<AllocationMode>("automatic");
  const [platforms, setPlatforms] = useState<PlatformAccount[]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const [running, setRunning] = useState(false);

  const load = useCallback(async () => {
    const path = { params: { path: { ws } } };
    const [a, s, c, m, p] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/allocation/attention", path),
      api.GET("/v1/workspaces/{ws}/allocation/suggestions", path),
      api.GET("/v1/workspaces/{ws}/allocation/changes", { params: { path: { ws }, query: { limit: 100 } } }),
      api.GET("/v1/workspaces/{ws}/allocation/settings", path),
      api.GET("/v1/workspaces/{ws}/platforms", path),
    ]);
    setPlatforms(p.data?.data ?? []);
    setAttention((a.data ?? null) as Attention | null);
    setSuggestions(s.data?.data ?? []);
    setChanges(c.data?.data ?? []);
    if (m.data?.mode) setMode(m.data.mode);
  }, [ws]);

  useEffect(() => {
    load();
    const t = setInterval(load, 30000);
    return () => clearInterval(t);
  }, [load]);

  async function run() {
    setRunning(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/allocation/run", { params: { path: { ws } } });
    setRunning(false);
    if (error) return toast(errorMessage(error), "error");
    const applied = data?.applied?.length ?? 0, suggested = data?.suggested?.length ?? 0;
    toast(applied + suggested === 0 ? "Every sequence already has what it can get." : [applied && `Made ${plural(applied, "change")}`, suggested && `suggested ${plural(suggested, "change")}`].filter(Boolean).join(", ") + ".");
    load();
  }

  async function setWorkspaceMode(next: AllocationMode) {
    const ok = await confirmAction(
      next === "automatic" ? "Allocate mailboxes automatically?" : "Only suggest mailbox changes?",
      next === "automatic"
        ? <p>Outflare assigns and moves mailboxes itself, explains each change here, and you can undo any of them. Teams or sequences set to suggest keep suggesting.</p>
        : <p>Outflare proposes changes and waits for someone to apply them. Sequences may run short of mailboxes until then. Teams or sequences set to automatic keep acting.</p>,
      next === "automatic" ? "Allocate automatically" : "Suggest only",
      false,
    );
    if (!ok) return;
    const { error } = await api.PATCH("/v1/workspaces/{ws}/allocation/settings", { params: { path: { ws } }, body: { mode: next } });
    if (error) return toast(errorMessage(error), "error");
    setMode(next);
    toast(next === "automatic" ? "Mailboxes are allocated automatically." : "Mailbox changes are now suggested for review.");
  }

  async function syncNow(p: PlatformAccount) {
    setBusy(p.integration_id!);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/platforms/{id}/sync", { params: { path: { ws, id: p.integration_id! } } });
    setBusy(null);
    if (error) return toast(errorMessage(error), "error");
    const accepted = data?.accepted ?? 0, confirmed = data?.confirmed ?? 0;
    toast(`Read ${plural(data?.mailboxes ?? 0, "mailbox", "mailboxes")} and ${plural(data?.campaigns ?? 0, "live campaign")}.` +
      (accepted ? ` Accepted ${plural(accepted, "change")} made there.` : "") + (confirmed ? ` ${plural(confirmed, "mailbox", "mailboxes")} confirmed.` : ""));
    load();
  }

  async function review(p: PlatformAccount) {
    const ok = await confirmAction(
      `Let Outflare change ${p.name}?`,
      <p>From now on mailbox allocation adds and removes mailboxes on its {plural(p.campaigns ?? 0, "campaign")} by itself in automatic mode, and explains each change here. Look at the suggested changes below first: they are what happens next.</p>,
      "Allow changes",
      false,
    );
    if (!ok) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/platforms/{id}/review", { params: { path: { ws, id: p.integration_id! } } });
    if (error) return toast(errorMessage(error), "error");
    const applied = data?.applied?.length ?? 0;
    toast(applied ? `Made ${plural(applied, "change")}. Each one is sent to ${p.name} and confirmed on the next read.` : "Allowed. Nothing needed changing yet.");
    load();
  }

  async function decide(s: AllocationSuggestion, decision: "apply" | "decline") {
    const opts = { params: { path: { ws, id: s.id! } } };
    const { error } = decision === "apply" ? await api.POST("/v1/workspaces/{ws}/allocation/suggestions/{id}/apply", opts) : await api.POST("/v1/workspaces/{ws}/allocation/suggestions/{id}/decline", opts);
    if (error) return toast(errorMessage(error), "error");
    toast(decision === "apply" ? `${s.mailbox_email} now sends for ${s.sequence_name}.` : `Declined. ${s.mailbox_email} won't be suggested for ${s.sequence_name} again.`);
    load();
  }

  async function undo(c: AllocationChange) {
    const ok = await confirmAction(`Undo this change to ${c.mailbox_email}?`, <p>It goes back to where it was. {c.source === "automatic" ? "Outflare won't make this choice again by itself." : ""}</p>, "Undo", false);
    if (!ok) return;
    const { error } = await api.POST("/v1/workspaces/{ws}/allocation/changes/{id}/undo", { params: { path: { ws, id: c.id! } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Undone.");
    load();
  }

  async function orphans(o: Orphan, action: "cancel" | "handoff") {
    const ok = await confirmAction(
      action === "cancel" ? `Cancel ${plural(o.leads, "follow-up")}?` : `Hand off ${plural(o.leads, "follow-up")}?`,
      action === "cancel"
        ? <p>These leads in {o.sequence_name} get no more emails from this sequence. Their first emails came from a mailbox that has since been removed.</p>
        : <p>Another of {o.sequence_name}'s mailboxes sends each lead's next step as a new email, not a reply, since that sender never wrote to them before.</p>,
      action === "cancel" ? "Cancel follow-ups" : "Hand off",
      action === "cancel",
    );
    if (!ok) return;
    const { error } = await api.POST("/v1/workspaces/{ws}/sequences/{id}/orphaned-follow-ups", { params: { path: { ws, id: o.sequence_id } }, body: { action } });
    if (error) return toast(errorMessage(error), "error");
    toast(action === "cancel" ? "Cancelled." : "Handed off. They send at the next opportunity.");
    load();
  }

  if (!attention) return <Loading />;
  const reviews = suggestions.filter((s) => s.needs === "review");
  const nothing = attention.approvals.length + reviews.length + attention.shortages.length + attention.orphaned_follow_ups.length + attention.unconfirmed_senders + (attention.platforms?.length ?? 0) === 0;
  const automaticToday = changes.filter((c) => c.source === "automatic" && Date.now() - Date.parse(c.created_at!) < 86400000).length;

  const suggestionTable = (list: AllocationSuggestion[]) => (
    <DataTable
      data={list}
      rowId={(s) => s.id!}
      columnsMenu={false}
      columns={[
        { id: "mailbox", header: "Mailbox", accessorFn: (s) => s.mailbox_email, meta: { mono: true } },
        {
          id: "change",
          header: "Change",
          accessorFn: (s) => s.sequence_name,
          cell: ({ row: { original: s } }) => (
            <span>
              {s.action === "assign" ? "Send for " : s.action === "drain" ? "Finish follow-ups, then leave " : "Free from "}
              <button className="font-medium underline decoration-border underline-offset-2 hover:decoration-foreground" onClick={() => go("sequences", s.sequence_id!)}>{s.sequence_name}</button>
            </span>
          ),
        },
        { id: "reason", header: "Why", accessorFn: (s) => s.reason, cell: ({ row: { original: s } }) => <span className="text-muted-foreground">{s.reason}</span> },
        { id: "needs", header: "Needs", accessorFn: (s) => s.needs, cell: ({ row: { original: s } }) => <Badge tone={s.needs === "review" ? "grey" : "amber"} title={s.approver_team_name ? `A member of ${s.approver_team_name}, or an owner or admin` : undefined}>{needsLabel[s.needs!]}{s.approver_team_name ? `: ${s.approver_team_name}` : ""}</Badge> },
        ...(canManage
          ? [{
              id: "actions",
              header: () => <span className="sr-only">Actions</span>,
              enableSorting: false,
              meta: { align: "right" as const },
              cell: ({ row: { original: s } }: { row: { original: AllocationSuggestion } }) => (
                <span className="inline-flex gap-1">
                  <Button variant="ghost" onClick={() => decide(s, "decline")}>Decline</Button>
                  <Button onClick={() => decide(s, "apply")}>{s.needs === "review" ? "Apply" : "Approve"}</Button>
                </span>
              ),
            }]
          : []),
      ]}
    />
  );

  return (
    <div className="space-y-6">
      <PageHeader
        title="Mailbox allocation"
        description="Each mailbox sends for one sequence at a time. Outflare gives each active sequence enough ready mailboxes for its daily target, own team first, and never moves a mailbox while it has follow-ups due."
        action={
          <>
            {canSetMode && (
              <label className="flex items-center gap-2 text-[13px] text-muted-foreground">
                Workspace mode
                <select className={field} value={mode} onChange={(e) => setWorkspaceMode(e.target.value as AllocationMode)} aria-label="Workspace allocation mode">
                  <option value="automatic">Automatic</option>
                  <option value="suggest">Suggest only</option>
                </select>
              </label>
            )}
            {canManage && <Button variant="ghost" onClick={run} disabled={running}>{running ? "Planning…" : "Re-plan now"}</Button>}
          </>
        }
      />

      <Stats cols={4}>
        <Stat label="Waiting on approval" value={attention.approvals.length} tone={attention.approvals.length ? "amber" : undefined} />
        <Stat label="Sequences short" value={attention.shortages.length} tone={attention.shortages.length ? "amber" : undefined} />
        <Stat label="Follow-ups without a mailbox" value={attention.orphaned_follow_ups.reduce((n, o) => n + o.leads, 0)} tone={attention.orphaned_follow_ups.length ? "red" : undefined} />
        <Stat label="Automatic changes, 24h" value={automaticToday} />
      </Stats>

      {nothing && <Empty title="Nothing needs you">Every active sequence has the mailboxes it can get, and nothing is waiting on a decision.</Empty>}

      {attention.orphaned_follow_ups.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-[13px] font-semibold">Follow-ups whose mailbox is gone</h2>
          <p className="text-[12px] text-muted-foreground">Follow-ups only ever go from the mailbox that sent the first email. These leads' mailbox was removed, so they wait until you decide.</p>
          <ul className="divide-y rounded-lg border bg-card">
            {attention.orphaned_follow_ups.map((o) => (
              <li key={o.sequence_id} className="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5">
                <span><span className="font-medium">{plural(o.leads, "lead")}</span> in <button className="underline decoration-border underline-offset-2" onClick={() => go("sequences", o.sequence_id)}>{o.sequence_name}</button></span>
                {canManage && (
                  <span className="inline-flex gap-1">
                    <Button variant="ghost" onClick={() => orphans(o, "cancel")}>Cancel them</Button>
                    <Button onClick={() => orphans(o, "handoff")}>Hand off</Button>
                  </span>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}

      {attention.approvals.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-[13px] font-semibold">Waiting on approval</h2>
          <p className="text-[12px] text-muted-foreground">Taking another team's mailbox needs one of its members (or an owner or admin). An agent taking a mailbox from a lower-priority sequence needs a person, unless its token allows it.</p>
          {suggestionTable(attention.approvals)}
        </section>
      )}

      {reviews.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-[13px] font-semibold">Suggested changes</h2>
          <p className="text-[12px] text-muted-foreground">These sequences are in suggest mode, so nothing changes until you apply it.</p>
          {suggestionTable(reviews)}
        </section>
      )}

      {attention.shortages.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-[13px] font-semibold">Short of mailboxes</h2>
          <ul className="divide-y rounded-lg border bg-card">
            {attention.shortages.map((s) => (
              <li key={s.sequence_id} className="space-y-0.5 px-4 py-2.5">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <button className="font-medium underline decoration-border underline-offset-2" onClick={() => go("sequences", s.sequence_id)}>{s.sequence_name}</button>
                  <span className="font-mono text-[12px] text-muted-foreground">{s.active_daily.toLocaleString()} of {s.needed_daily.toLocaleString()} sends a day</span>
                </div>
                <p className="text-[12px] text-muted-foreground">{s.note}</p>
                {s.blocking.length > 0 && <p className="text-[12px] text-muted-foreground">Not used: {blockingText(s.blocking)}</p>}
              </li>
            ))}
          </ul>
        </section>
      )}

      {attention.unconfirmed_senders > 0 && (
        <section className="flex flex-wrap items-center justify-between gap-3 rounded-lg border px-4 py-3">
          <p><span className="font-medium">{plural(attention.unconfirmed_senders, "mailbox has", "mailboxes have")} no confirmed sender,</span> <span className="text-muted-foreground">so no team's sequences can use them.</span></p>
          <Button variant="ghost" onClick={() => go("senders")}>Confirm senders</Button>
        </section>
      )}

      {platforms.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-[13px] font-semibold">Sequencer accounts</h2>
          <p className="text-[12px] text-muted-foreground">Outflare reads these every 5 minutes. Their live campaigns show up as sequences: the platform keeps their steps, leads and status, and Outflare decides which mailboxes each one uses. Changes you make in the platform are accepted as manual ones.</p>
          <DataTable
            data={platforms}
            rowId={(p) => p.integration_id!}
            columnsMenu={false}
            columns={[
              { id: "account", header: "Account", accessorFn: (p) => p.name, cell: ({ row: { original: p } }) => <span className="font-medium">{p.name}</span> },
              {
                id: "state",
                header: "State",
                accessorFn: (p) => (p.error ? 0 : p.stale ? 1 : p.awaiting_review ? 2 : 3),
                cell: ({ row: { original: p } }) =>
                  p.error ? <Badge tone="red" title={p.error}>Can't read it</Badge>
                  : p.stale ? <Badge tone="amber" title="Its mailboxes count as busy until it is read again.">Out of date</Badge>
                  : p.control === "read_only" ? <Badge tone="grey" title="This platform picks a mailbox for each contact itself, so Outflare only watches it.">Watched only</Badge>
                  : p.awaiting_review ? <Badge tone="amber" title="Changes for its campaigns are only suggested until you allow them.">First plan to review</Badge>
                  : <Badge tone="green">Managed</Badge>,
              },
              { id: "mailboxes", header: "Mailboxes", accessorFn: (p) => p.mailboxes, meta: { align: "right" as const, mono: true } },
              { id: "campaigns", header: "Live campaigns", accessorFn: (p) => p.campaigns, meta: { align: "right" as const, mono: true } },
              {
                id: "issues",
                header: "Waiting",
                accessorFn: (p) => (p.unconfirmed ?? 0) + (p.conflicts ?? 0),
                cell: ({ row: { original: p } }) => (
                  <span className="text-[12px] text-muted-foreground">
                    {[p.unconfirmed && `${p.unconfirmed} to confirm`, p.conflicts && `${plural(p.conflicts, "mailbox", "mailboxes")} on more than one campaign or not allocated`].filter(Boolean).join(" · ") || "None"}
                  </span>
                ),
              },
              { id: "read", header: "Last read", accessorFn: (p) => p.synced_at ?? "", cell: ({ row: { original: p } }) => <span className="whitespace-nowrap text-muted-foreground" title={p.synced_at ? stamp(p.synced_at) : undefined}>{p.synced_at ? ago(p.synced_at) : "Never"}</span> },
              ...(canManage
                ? [{
                    id: "actions",
                    header: () => <span className="sr-only">Actions</span>,
                    enableSorting: false,
                    meta: { align: "right" as const },
                    cell: ({ row: { original: p } }: { row: { original: PlatformAccount } }) => (
                      <span className="inline-flex gap-1">
                        <Button variant="ghost" disabled={busy === p.integration_id} onClick={() => syncNow(p)}>{busy === p.integration_id ? "Reading…" : "Read now"}</Button>
                        {p.awaiting_review && p.control === "full" && <Button onClick={() => review(p)}>Allow changes</Button>}
                      </span>
                    ),
                  }]
                : []),
            ]}
          />
        </section>
      )}

      <section className="space-y-3">
        <h2 className="flex items-baseline gap-2 text-[13px] font-semibold">Recent changes <span className="font-mono text-[12px] font-normal text-muted-foreground">last 100</span></h2>
        <DataTable
          data={changes}
          rowId={(c) => c.id!}
          searchPlaceholder="Search mailbox or sequence"
          empty="No mailbox has been assigned yet. Activate a sequence with a daily limit or target and it gets mailboxes straight away."
          initialSort={[{ id: "when", desc: true }]}
          columns={[
            { id: "when", header: "When", accessorFn: (c) => c.created_at, cell: ({ row: { original: c } }) => <span className="whitespace-nowrap text-muted-foreground" title={stamp(c.created_at!)}>{ago(c.created_at)}</span> },
            { id: "mailbox", header: "Mailbox", accessorFn: (c) => c.mailbox_email, meta: { mono: true } },
            {
              id: "change",
              header: "Change",
              accessorFn: (c) => `${c.from_sequence_name ?? ""} ${c.to_sequence_name ?? ""}`,
              cell: ({ row: { original: c } }) => (
                <span className={c.undone_at ? "text-muted-foreground line-through" : undefined}>
                  {actionLabel[c.action!] ?? c.action} {c.from_sequence_name && <>from <span className="font-medium">{c.from_sequence_name}</span> </>}
                  {c.to_sequence_name && <>to <span className="font-medium">{c.to_sequence_name}</span></>}
                </span>
              ),
            },
            { id: "reason", header: "Why", accessorFn: (c) => c.reason, cell: ({ row: { original: c } }) => <span className="text-muted-foreground">{c.reason}</span> },
            { id: "by", header: "By", accessorFn: (c) => c.actor_name ?? c.source, cell: ({ row: { original: c } }) => <span className="whitespace-nowrap text-muted-foreground">{c.source === "automatic" ? "Outflare" : c.source === "external" ? "In the platform" : c.actor_name ?? c.source}</span> },
            ...(canManage
              ? [{
                  id: "actions",
                  header: () => <span className="sr-only">Actions</span>,
                  enableSorting: false,
                  meta: { align: "right" as const },
                  cell: ({ row: { original: c } }: { row: { original: AllocationChange } }) =>
                    c.undone_at ? <span className="text-[12px] text-muted-foreground">Undone</span> : c.action !== "finish_drain" && <Button variant="ghost" onClick={() => undo(c)}>Undo</Button>,
                }]
              : []),
          ]}
        />
      </section>
    </div>
  );
}
