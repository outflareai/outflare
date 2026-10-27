import { useCallback, useEffect, useState } from "react";
import type { AllocationBlocking, AllocationChange, AllocationSuggestion, AssignedMailbox, CampaignLink, Mailbox, MailboxAllocationResult, Sequence } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import type { Tab } from "../App";
import { Badge, Button, Card, confirmAction, Menu, Status, toast } from "../ui";
import { ago, plural, shortPlatform, stamp } from "../format";
import { DataTable } from "@/components/data-table";
import { actionLabel, blockingText } from "./Allocation";

type SeqAllocation = {
  mode: "suggest" | "automatic";
  needed_daily: number | null;
  active_daily: number;
  blocking: AllocationBlocking[];
  note: string;
  planned_at: string | null;
  mailboxes: AssignedMailbox[];
  incoming: AssignedMailbox[];
  suggestions: AllocationSuggestion[];
  changes: AllocationChange[];
  orphaned_follow_ups: number;
  links: CampaignLink[];
};

const lockLabel = { sequence: "Kept here", team: "Team only", manual_only: "Manual only" } as const;

// Which mailboxes this sequence sends from, why, and what is short.
export function SequenceMailboxes({ ws, seq, canManage, go, onChange }: { ws: string; seq: Sequence; canManage: boolean; go: (t: Tab, item?: string) => void; onChange: () => void }) {
  const [a, setA] = useState<SeqAllocation | null>(null);
  const [picking, setPicking] = useState<Mailbox[] | null>(null);
  const [chosen, setChosen] = useState<string[]>([]);

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/sequences/{id}/allocation", { params: { path: { ws, id: seq.id } } });
    if (data) setA(data as SeqAllocation);
  }, [ws, seq.id]);

  useEffect(() => {
    load();
  }, [load, seq.updated_at]);

  const refresh = () => {
    load();
    onChange();
  };

  function report(results: MailboxAllocationResult[] | undefined, verb: string) {
    const list = results ?? [];
    const refused = list.filter((r) => r.result === "refused");
    const draining = list.filter((r) => r.result === "draining").length;
    const done = list.length - refused.length;
    if (done) toast(`${verb} ${plural(done, "mailbox", "mailboxes")}.${draining ? ` ${plural(draining, "is", "are")} finishing follow-ups first.` : ""}`);
    if (refused.length) toast(refused.length === 1 ? refused[0].message! : `${plural(refused.length, "mailbox", "mailboxes")} refused: ${refused[0].message}`, "error");
  }

  async function openPicker() {
    const { data } = await api.GET("/v1/workspaces/{ws}/mailboxes", { params: { path: { ws } } });
    // A platform campaign can only use mailboxes connected in its account;
    // Outflare can only send from mailboxes it has a login for.
    const accounts = new Set((seq.links ?? []).filter((l) => l.control === "full").map((l) => l.integration_id));
    const all = (data?.data ?? []).filter((m) =>
      m.assignment?.sequence_id !== seq.id &&
      (accounts.size ? (m.platforms ?? []).some((p) => accounts.has(p.integration_id) && p.status === "ok") : m.has_login !== false));
    setChosen([]);
    setPicking(all);
  }

  async function assign() {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/mailboxes/assign", { params: { path: { ws } }, body: { sequence_id: seq.id, mailbox_ids: chosen } });
    if (error) return toast(errorMessage(error), "error");
    report(data?.data, "Assigned");
    setPicking(null);
    refresh();
  }

  async function release(m: AssignedMailbox) {
    const ok = await confirmAction(
      `Stop using ${m.email} here?`,
      <p>{m.in_flight ? `It takes no new leads, finishes the follow-ups for its ${plural(m.in_flight ?? 0, "lead")} in this sequence, then is free.` : "It's free for other sequences straight away."} {a?.mode === "automatic" && "Outflare may give this sequence another mailbox if it's still short."}</p>,
      "Release",
      false,
    );
    if (!ok) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/mailboxes/release", { params: { path: { ws } }, body: { mailbox_ids: [m.mailbox_id!] } });
    if (error) return toast(errorMessage(error), "error");
    report(data?.data, "Released");
    refresh();
  }

  async function lock(m: AssignedMailbox, kind: "sequence" | "manual_only" | null) {
    const { error } = await api.POST("/v1/workspaces/{ws}/mailboxes/lock", {
      params: { path: { ws } },
      body: { mailbox_ids: [m.mailbox_id!], kind, ...(kind === "sequence" ? { sequence_id: seq.id } : {}) },
    });
    if (error) return toast(errorMessage(error), "error");
    toast(kind === "sequence" ? `${m.email} stays on this sequence.` : kind === "manual_only" ? `${m.email} is only moved by hand now.` : `Unlocked ${m.email}.`);
    refresh();
  }

  async function decide(s: AllocationSuggestion, apply: boolean) {
    const opts = { params: { path: { ws, id: s.id! } } };
    const { error } = apply ? await api.POST("/v1/workspaces/{ws}/allocation/suggestions/{id}/apply", opts) : await api.POST("/v1/workspaces/{ws}/allocation/suggestions/{id}/decline", opts);
    if (error) return toast(errorMessage(error), "error");
    toast(apply ? "Applied." : "Declined. It won't be suggested again.");
    refresh();
  }

  async function undo(c: AllocationChange) {
    const { error } = await api.POST("/v1/workspaces/{ws}/allocation/changes/{id}/undo", { params: { path: { ws, id: c.id! } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Undone.");
    refresh();
  }

  if (!a) return null;
  const short = a.needed_daily != null && a.active_daily < a.needed_daily && seq.status === "active";
  const summary =
    a.needed_daily != null
      ? a.active_daily >= a.needed_daily
        ? `${a.active_daily.toLocaleString()} sends a day allocated, covering the ${a.needed_daily.toLocaleString()} needed`
        : `${a.active_daily.toLocaleString()} of ${a.needed_daily.toLocaleString()} sends a day allocated`
      : a.mailboxes.length
        ? `${a.active_daily.toLocaleString()} sends a day, assigned by hand`
        : "Set new leads per day or a daily send target below to get mailboxes automatically";

  return (
    <Card
      title={<span className="flex items-center gap-2">Mailboxes {short && <Badge tone="amber">Short</Badge>}</span>}
      description={`${summary} · ${a.mode === "automatic" ? "allocated automatically" : "changes suggested for review"}${a.planned_at ? ` · planned ${ago(a.planned_at)}` : ""}`}
      action={canManage && <Button variant="ghost" onClick={picking ? () => setPicking(null) : openPicker}>{picking ? "Cancel" : "Add mailboxes"}</Button>}
    >
      <div className="space-y-4">
        {a.orphaned_follow_ups > 0 && (
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-red-600/25 bg-red-500/[0.06] px-3 py-2">
            <span>{plural(a.orphaned_follow_ups, "lead is", "leads are")} waiting on follow-ups from a mailbox that was removed.</span>
            <Button variant="ghost" onClick={() => go("allocation")}>Decide</Button>
          </div>
        )}
        {a.links.length > 0 && (
          <p className="rounded-lg border bg-muted/40 px-3 py-2 text-[13px]">
            Runs as {a.links.map((l, i) => <span key={l.id}>{i > 0 && " and "}the <span className="font-medium">{l.platform}</span> campaign <span className="font-medium">{l.name}</span></span>)}.{" "}
            <span className="text-muted-foreground">
              {a.links.every((l) => l.control === "read_only")
                ? "That platform picks a mailbox for each contact itself, so Outflare only shows what it uses."
                : "Its steps, leads and status stay there. Outflare decides which mailboxes it uses and adds or removes them there; a mailbox taken off stays until the campaign's follow-ups are done."}
            </span>
          </p>
        )}
        {(short || (seq.status === "active" && a.note)) && (
          <p className="text-[13px]">
            {a.note}
            {a.blocking.length > 0 && <span className="block text-[12px] text-muted-foreground">Not used: {blockingText(a.blocking)}</span>}
          </p>
        )}

        {picking && (
          <div className="space-y-3 rounded-lg border p-3">
            <p className="text-[12px] text-muted-foreground">Mailboxes sending for another sequence finish their follow-ups there first. Locked mailboxes are refused.</p>
            {picking.length === 0 ? (
              <p className="text-muted-foreground">Every mailbox already sends for this sequence.</p>
            ) : (
              <ul className="max-h-72 divide-y overflow-y-auto rounded-md border">
                {picking.map((m) => (
                  <li key={m.id}>
                    <label className="flex cursor-pointer items-center gap-3 px-3 py-2 hover:bg-muted/40">
                      <input type="checkbox" checked={chosen.includes(m.id!)} onChange={() => setChosen(chosen.includes(m.id!) ? chosen.filter((x) => x !== m.id) : [...chosen, m.id!])} />
                      <span className="min-w-0 flex-1">
                        <span className="block font-mono text-[12px]">{m.email}</span>
                        <span className="block text-[12px] text-muted-foreground">
                          {[m.sender?.confirmed ? m.sender.name : "No confirmed sender", m.assignment ? `sending for ${m.assignment.sequence_name}` : "free", m.status !== "active" && m.status, m.lock && lockLabel[m.lock.kind as keyof typeof lockLabel]].filter(Boolean).join(" · ")}
                        </span>
                      </span>
                      <span className="font-mono text-[12px] text-muted-foreground">{m.daily_limit}/day</span>
                    </label>
                  </li>
                ))}
              </ul>
            )}
            <Button onClick={assign} disabled={chosen.length === 0}>Assign {chosen.length ? plural(chosen.length, "mailbox", "mailboxes") : ""}</Button>
          </div>
        )}

        {a.suggestions.length > 0 && (
          <ul className="divide-y rounded-lg border">
            {a.suggestions.map((s) => (
              <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
                <span className="min-w-0">
                  <span className="font-mono text-[12px]">{s.mailbox_email}</span> <span className="text-muted-foreground">· {s.reason}</span>
                  {s.needs !== "review" && <Badge tone="amber">{s.needs === "team_approval" ? `Needs ${s.approver_team_name ?? "team"} approval` : "Needs owner approval"}</Badge>}
                </span>
                {canManage && (
                  <span className="inline-flex gap-1">
                    <Button variant="ghost" onClick={() => decide(s, false)}>Decline</Button>
                    <Button onClick={() => decide(s, true)}>{s.needs === "review" ? "Apply" : "Approve"}</Button>
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}

        {a.mailboxes.length > 0 && (
          <DataTable
            data={a.mailboxes}
            rowId={(m) => m.mailbox_id!}
            columnsMenu={false}
            initialSort={[{ id: "mailbox", desc: false }]}
            columns={[
              {
                id: "mailbox",
                header: "Mailbox",
                accessorFn: (m) => m.email,
                cell: ({ row: { original: m } }) => (
                  <>
                    <span className="font-mono text-[12px]">{m.email}</span>
                    <span className="block text-[12px] text-muted-foreground">{m.sender_name ?? "No confirmed sender"}</span>
                  </>
                ),
              },
              {
                id: "state",
                header: "State",
                accessorFn: (m) => m.state,
                cell: ({ row: { original: m } }) =>
                  m.state === "draining" ? <Status tone="amber">{m.drain_until ? `Leaves ${stamp(m.drain_until)}` : "Finishing follow-ups"}</Status>
                  : m.confirmed === false ? <span title={`Asked ${m.platform} to add it; confirmed on the next read, within 5 minutes.`}><Status tone="blue">Waiting for {shortPlatform(m.platform)}</Status></span>
                  : m.mailbox_status !== "active" ? <Status tone="red">{m.mailbox_status}</Status>
                  : <Status tone="green">{m.platform ? `Sending in ${shortPlatform(m.platform)}` : "Sending"}</Status>,
              },
              { id: "inflight", header: "Mid-sequence", accessorFn: (m) => m.in_flight ?? 0, meta: { align: "right", mono: true } },
              { id: "limit", header: "Per day", accessorFn: (m) => m.daily_limit ?? 0, meta: { align: "right", mono: true } },
              {
                id: "why",
                header: "Why",
                accessorFn: (m) => m.reason,
                cell: ({ row: { original: m } }) => (
                  <span className="text-muted-foreground" title={stamp(m.assigned_at!)}>
                    {m.source === "manual" ? "Assigned by hand" : m.reason}
                    {m.lock_kind && <> <Badge>{lockLabel[m.lock_kind as keyof typeof lockLabel]}</Badge></>}
                  </span>
                ),
              },
              ...(canManage
                ? [{
                    id: "actions",
                    header: () => <span className="sr-only">Actions</span>,
                    enableSorting: false,
                    meta: { align: "right" as const },
                    cell: ({ row: { original: m } }: { row: { original: AssignedMailbox } }) => (
                      <Menu
                        items={[
                          m.lock_kind !== "sequence" && { label: "Keep on this sequence", onClick: () => lock(m, "sequence") },
                          m.lock_kind !== "manual_only" && { label: "Only move by hand", onClick: () => lock(m, "manual_only") },
                          !!m.lock_kind && { label: "Unlock", onClick: () => lock(m, null) },
                          { label: m.in_flight ? "Release after follow-ups" : "Release", onClick: () => release(m), danger: true },
                        ]}
                      />
                    ),
                  }]
                : []),
            ]}
          />
        )}
        {a.mailboxes.length === 0 && !picking && <p className="text-muted-foreground">No mailboxes yet. {seq.status === "active" ? "" : "They're allocated when you start sending, or add them by hand."}</p>}

        {a.incoming.length > 0 && (
          <p className="text-[12px] text-muted-foreground">
            Joining once they finish follow-ups elsewhere: <span className="font-mono">{a.incoming.map((m) => m.email).join(", ")}</span>
          </p>
        )}

        {a.changes.length > 0 && (
          <details>
            <summary className="cursor-pointer text-[12px] text-muted-foreground">History ({a.changes.length})</summary>
            <ul className="mt-2 space-y-1 text-[12px]">
              {a.changes.map((c, i) => (
                <li key={c.id} className="flex flex-wrap items-baseline gap-x-2">
                  <span className="w-16 shrink-0 text-muted-foreground" title={stamp(c.created_at!)}>{ago(c.created_at)}</span>
                  <span className={c.undone_at ? "line-through" : undefined}>
                    {actionLabel[c.action!] ?? c.action} <span className="font-mono">{c.mailbox_email}</span>
                    {c.to_sequence_id !== seq.id && c.to_sequence_name && <> to {c.to_sequence_name}</>}
                    {c.from_sequence_id !== seq.id && c.from_sequence_name && <> from {c.from_sequence_name}</>}
                  </span>
                  <span className="text-muted-foreground">{c.source === "automatic" ? "by Outflare" : c.actor_name ? `by ${c.actor_name}` : ""} · {c.reason}</span>
                  {canManage && !c.undone_at && i === a.changes.findIndex((x) => x.mailbox_id === c.mailbox_id) && c.action !== "finish_drain" && (
                    <button className="text-muted-foreground underline underline-offset-2 hover:text-foreground" onClick={() => undo(c)}>Undo</button>
                  )}
                </li>
              ))}
            </ul>
          </details>
        )}
      </div>
    </Card>
  );
}
