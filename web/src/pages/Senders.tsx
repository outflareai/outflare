import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Member, Sender, Team } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, Status, toast } from "../ui";
import { plural } from "../format";
import { DataTable } from "@/components/data-table";

type Match = { mailbox_id: string; email: string; display_name: string; sender_id: string; sender_name: string };

const kinds: { value: NonNullable<Sender["kind"]>; label: string; help: string }[] = [
  { value: "person", label: "Person", help: "A real member of your team, e.g. Ana Smith" },
  { value: "persona", label: "Persona", help: "A name you send as that isn't one person" },
  { value: "shared", label: "Shared", help: "A team identity, e.g. Acme Partnerships" },
];

const modeLabel = { automatic: "Automatic", suggest: "Suggest" } as const;

// Teams own sequences and senders; senders are who mailboxes send as. Both
// shape which mailboxes a sequence gets, so they share a page.
export function Senders({ ws, canManage, canManageTeams }: { ws: string; canManage: boolean; canManageTeams: boolean }) {
  const [senders, setSenders] = useState<Sender[] | null>(null);
  const [teams, setTeams] = useState<Team[]>([]);
  const [members, setMembers] = useState<Member[]>([]);
  const [matches, setMatches] = useState<Match[]>([]);
  const [unconfirmed, setUnconfirmed] = useState(0);
  const [adding, setAdding] = useState<"" | "sender" | "team">("");

  const load = useCallback(async () => {
    const path = { params: { path: { ws } } };
    const [s, t, m, x] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/senders", path),
      api.GET("/v1/workspaces/{ws}/teams", path),
      api.GET("/v1/workspaces/{ws}/members", path),
      api.GET("/v1/workspaces/{ws}/senders/matches", path),
    ]);
    setSenders(s.data?.data ?? []);
    setTeams(t.data?.data ?? []);
    setMembers(m.data?.data ?? []);
    setMatches((x.data?.data ?? []) as Match[]);
    setUnconfirmed(x.data?.unconfirmed ?? 0);
  }, [ws]);

  useEffect(() => {
    load();
  }, [load]);

  const teamName = (id: string) => teams.find((t) => t.id === id)?.name ?? "Unknown team";
  const memberName = (id?: string | null) => members.find((m) => m.user_id === id)?.name;

  async function confirmMatches() {
    const bySender = new Map<string, string[]>();
    for (const m of matches) bySender.set(m.sender_id, [...(bySender.get(m.sender_id) ?? []), m.mailbox_id]);
    for (const [sender_id, mailbox_ids] of bySender) {
      const { error } = await api.POST("/v1/workspaces/{ws}/mailboxes/sender", { params: { path: { ws } }, body: { mailbox_ids, sender_id } });
      if (error) return toast(errorMessage(error), "error");
    }
    toast(`Confirmed the sender for ${plural(matches.length, "mailbox", "mailboxes")}.`);
    load();
  }

  async function patchSender(s: Sender, body: Record<string, unknown>, done: string) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/senders/{id}", { params: { path: { ws, id: s.id! } }, body });
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    load();
  }

  async function renameSender(s: Sender) {
    const r = await ask({ title: `Rename ${s.name}`, body: <p>This is the name recipients see. Mailboxes keep their own display name until you change it there.</p>, input: { label: "Name", placeholder: s.name }, choices: [{ label: "Cancel", value: "cancel" }, { label: "Rename", value: "ok", variant: "primary" }] });
    if (r?.value.trim()) patchSender(s, { name: r.value.trim() }, `Renamed to ${r.value.trim()}.`);
  }

  async function retire(s: Sender) {
    const ok = await confirmAction(
      `Retire ${s.name}?`,
      <p>{s.mailboxes ? `Its ${plural(s.mailboxes ?? 0, "mailbox", "mailboxes")} stop taking new leads, finish the follow-ups they've started, and are then freed for other sequences.` : "It won't be offered for new mailboxes."} You can reactivate it later.</p>,
      "Retire sender",
    );
    if (ok) patchSender(s, { status: "retired" }, `Retired ${s.name}.`);
  }

  async function removeSender(s: Sender) {
    const ok = await confirmAction(`Delete ${s.name}?`, <p>{s.mailboxes ? `Its ${plural(s.mailboxes ?? 0, "mailbox", "mailboxes")} will have no confirmed sender. ` : ""}Retire it instead if it might come back.</p>, "Delete sender");
    if (!ok) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/senders/{id}", { params: { path: { ws, id: s.id! } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Deleted ${s.name}.`);
    load();
  }

  async function patchTeam(t: Team, body: Record<string, unknown>, done: string) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/teams/{id}", { params: { path: { ws, id: t.id! } }, body });
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    load();
  }

  async function renameTeam(t: Team) {
    const r = await ask({ title: `Rename ${t.name}`, input: { label: "Name", placeholder: t.name }, choices: [{ label: "Cancel", value: "cancel" }, { label: "Rename", value: "ok", variant: "primary" }] });
    if (r?.value.trim()) patchTeam(t, { name: r.value.trim() }, `Renamed to ${r.value.trim()}.`);
  }

  async function removeTeam(t: Team) {
    const ok = await confirmAction(`Delete the ${t.name} team?`, <p>Its {plural(t.sequences ?? 0, "sequence")} and {plural(t.senders ?? 0, "sender")} stay, without a team. Their mailboxes become available to everyone.</p>, "Delete team");
    if (!ok) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/teams/{id}", { params: { path: { ws, id: t.id! } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Deleted ${t.name}.`);
    load();
  }

  if (senders === null) return <Loading />;
  const hasTeams = teams.length > 0;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Teams & senders"
        description="Senders are who your mailboxes send as. Teams own sequences and senders, so a team's sequences use its own senders' mailboxes first, then shared ones. Without teams, every mailbox is shared."
        action={canManage && (
          <>
            {canManageTeams && <Button variant="ghost" onClick={() => setAdding(adding === "team" ? "" : "team")}>New team</Button>}
            <Button onClick={() => setAdding(adding === "sender" ? "" : "sender")}>New sender</Button>
          </>
        )}
      />

      {unconfirmed > 0 && (
        <section className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-amber-600/30 bg-amber-500/[0.06] px-4 py-3">
          <div className="min-w-0">
            <p className="font-medium">{plural(unconfirmed, "mailbox has", "mailboxes have")} no confirmed sender</p>
            <p className="text-[12px] text-muted-foreground">
              {hasTeams ? "With teams set up, those mailboxes aren't allocated to any sequence until you confirm who they send as. " : "They still send for any sequence, since there are no teams yet. "}
              {matches.length > 0 ? `${plural(matches.length, "mailbox matches", "mailboxes match")} a sender by display name.` : "Set a sender on the Mailboxes page, or create one here first."}
            </p>
            {matches.length > 0 && (
              <details className="mt-1">
                <summary className="cursor-pointer text-[12px] text-muted-foreground">Show matches</summary>
                <ul className="mt-1 space-y-0.5 font-mono text-[12px]">
                  {matches.map((m) => <li key={m.mailbox_id}>{m.email} → {m.sender_name}</li>)}
                </ul>
              </details>
            )}
          </div>
          {canManage && matches.length > 0 && <Button onClick={confirmMatches}>Confirm {plural(matches.length, "match", "matches")}</Button>}
        </section>
      )}

      {adding === "sender" && <SenderForm ws={ws} teams={teams} members={members} onDone={(name) => { setAdding(""); if (name) { toast(`Created ${name}.`); load(); } }} />}
      {adding === "team" && <TeamForm ws={ws} members={members} onDone={(name) => { setAdding(""); if (name) { toast(`Created the ${name} team.`); load(); } }} />}

      <section className="space-y-3">
        <h2 className="flex items-baseline gap-2 text-[13px] font-semibold">Senders <span className="font-mono text-[12px] font-normal text-muted-foreground">{senders.length}</span></h2>
        {senders.length === 0 ? (
          <Empty title="No senders yet" action={canManage && <Button onClick={() => setAdding("sender")}>New sender</Button>}>
            Add the people and personas your mailboxes send as. Name each one exactly as its mailboxes' display name, and Outflare offers to match them for you.
          </Empty>
        ) : (
          <DataTable
            data={senders}
            rowId={(s) => s.id!}
            searchPlaceholder="Search senders"
            initialSort={[{ id: "name", desc: false }]}
            columns={[
              { id: "name", header: "Name", accessorFn: (s) => s.name, cell: ({ row: { original: s } }) => <span className="font-medium">{s.name}</span> },
              { id: "kind", header: "Kind", accessorFn: (s) => s.kind, cell: ({ row: { original: s } }) => <span className="text-muted-foreground">{kinds.find((k) => k.value === s.kind)?.label}{s.kind === "person" && memberName(s.user_id) ? ` · ${memberName(s.user_id)}` : ""}</span> },
              {
                id: "teams",
                header: "Teams",
                accessorFn: (s) => (s.team_ids ?? []).map(teamName).join(", "),
                cell: ({ row: { original: s } }) => (s.team_ids?.length ? <span className="flex flex-wrap gap-1">{s.team_ids.map((id) => <Badge key={id}>{teamName(id)}</Badge>)}</span> : <span className="text-muted-foreground" title="Any team's sequences may use its mailboxes">Shared</span>),
              },
              { id: "mailboxes", header: "Mailboxes", accessorFn: (s) => s.mailboxes ?? 0, meta: { align: "right", mono: true } },
              { id: "status", header: "Status", accessorFn: (s) => s.status, cell: ({ row: { original: s } }) => <Status tone={s.status === "active" ? "green" : "grey"}>{s.status}</Status> },
              ...(canManage
                ? [{
                    id: "actions",
                    header: () => <span className="sr-only">Actions</span>,
                    enableSorting: false,
                    meta: { align: "right" as const },
                    cell: ({ row: { original: s } }: { row: { original: Sender } }) => (
                      <Menu
                        items={[
                          { label: "Rename", onClick: () => renameSender(s) },
                          ...teams.map((t) => {
                            const on = s.team_ids?.includes(t.id!);
                            return { label: on ? `Remove from ${t.name}` : `Add to ${t.name}`, onClick: () => patchSender(s, { team_ids: on ? s.team_ids!.filter((x) => x !== t.id) : [...(s.team_ids ?? []), t.id!] }, on ? `${s.name} left ${t.name}.` : `${s.name} joined ${t.name}.`) };
                          }),
                          s.status === "active" ? { label: "Retire", onClick: () => retire(s) } : { label: "Reactivate", onClick: () => patchSender(s, { status: "active" }, `Reactivated ${s.name}.`) },
                          { label: "Delete", onClick: () => removeSender(s), danger: true },
                        ]}
                      />
                    ),
                  }]
                : []),
            ]}
          />
        )}
      </section>

      <section className="space-y-3">
        <h2 className="flex items-baseline gap-2 text-[13px] font-semibold">Teams <span className="font-mono text-[12px] font-normal text-muted-foreground">{teams.length}</span></h2>
        {!hasTeams ? (
          <Empty title="No teams" action={canManageTeams && <Button variant="ghost" onClick={() => setAdding("team")}>New team</Button>}>
            Fine for one team. Add teams when several groups share this workspace and each should send from its own mailboxes first.
          </Empty>
        ) : (
          <DataTable
            data={teams}
            rowId={(t) => t.id!}
            columnsMenu={false}
            initialSort={[{ id: "name", desc: false }]}
            columns={[
              { id: "name", header: "Name", accessorFn: (t) => t.name, cell: ({ row: { original: t } }) => <span className="font-medium">{t.name}</span> },
              { id: "members", header: "Members", accessorFn: (t) => t.member_ids?.length ?? 0, cell: ({ row: { original: t } }) => <span className="text-muted-foreground">{t.member_ids?.length ? t.member_ids.map((id) => memberName(id) ?? "Former member").join(", ") : "None"}</span> },
              { id: "senders", header: "Senders", accessorFn: (t) => t.senders ?? 0, meta: { align: "right", mono: true } },
              { id: "sequences", header: "Sequences", accessorFn: (t) => t.sequences ?? 0, meta: { align: "right", mono: true } },
              { id: "mode", header: "Allocation", accessorFn: (t) => t.allocation_mode ?? "", cell: ({ row: { original: t } }) => <span className="text-muted-foreground">{t.allocation_mode ? modeLabel[t.allocation_mode] : "Workspace default"}</span> },
              ...(canManageTeams
                ? [{
                    id: "actions",
                    header: () => <span className="sr-only">Actions</span>,
                    enableSorting: false,
                    meta: { align: "right" as const },
                    cell: ({ row: { original: t } }: { row: { original: Team } }) => (
                      <Menu
                        items={[
                          { label: "Rename", onClick: () => renameTeam(t) },
                          ...members.map((m) => {
                            const on = t.member_ids?.includes(m.user_id!);
                            return { label: on ? `Remove ${m.name}` : `Add ${m.name}`, onClick: () => patchTeam(t, { member_ids: on ? t.member_ids!.filter((x) => x !== m.user_id) : [...(t.member_ids ?? []), m.user_id!] }, on ? `${m.name} left ${t.name}.` : `${m.name} joined ${t.name}.`) };
                          }),
                          t.allocation_mode !== "automatic" && { label: "Allocate automatically", onClick: () => patchTeam(t, { allocation_mode: "automatic" }, `${t.name} allocates automatically.`) },
                          t.allocation_mode !== "suggest" && { label: "Suggest changes only", onClick: () => patchTeam(t, { allocation_mode: "suggest" }, `${t.name} now suggests changes for review.`) },
                          t.allocation_mode != null && { label: "Use the workspace default", onClick: () => patchTeam(t, { allocation_mode: null }, `${t.name} follows the workspace default.`) },
                          { label: "Delete", onClick: () => removeTeam(t), danger: true },
                        ]}
                      />
                    ),
                  }]
                : []),
            ]}
          />
        )}
      </section>
    </div>
  );
}

function SenderForm({ ws, teams, members, onDone }: { ws: string; teams: Team[]; members: Member[]; onDone: (created?: string) => void }) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState<NonNullable<Sender["kind"]>>("person");
  const [userId, setUserId] = useState("");
  const [teamIds, setTeamIds] = useState<string[]>([]);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    const { error } = await api.POST("/v1/workspaces/{ws}/senders", {
      params: { path: { ws } },
      body: { name: name.trim(), kind, team_ids: teamIds, user_id: kind === "person" && userId ? userId : null },
    });
    if (error) return setError(errorMessage(error));
    onDone(name.trim());
  }

  return (
    <Card title="New sender" action={<Button variant="ghost" type="button" onClick={() => onDone()}>Cancel</Button>}>
      <form onSubmit={submit} className="space-y-4">
        <Input label="Name" placeholder="e.g. Ana Smith" hint="Use the display name on its mailboxes, so they can be matched." value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        <fieldset>
          <legend className="mb-2 font-medium">Kind</legend>
          <div className="grid gap-2 sm:grid-cols-3">
            {kinds.map((k) => (
              <button key={k.value} type="button" onClick={() => setKind(k.value)} aria-pressed={kind === k.value} className={`rounded-lg border px-3 py-2 text-left transition-colors ${kind === k.value ? "border-brand/60 bg-brand/[0.07] ring-1 ring-brand/40" : "border-border hover:border-ring/60"}`}>
                <span className="block font-medium">{k.label}</span>
                <span className="block text-[12px] text-muted-foreground">{k.help}</span>
              </button>
            ))}
          </div>
        </fieldset>
        {kind === "person" && members.length > 0 && (
          <label className="block">
            <span className="mb-1.5 block font-medium">Workspace member <span className="font-normal text-muted-foreground">(optional)</span></span>
            <select className={field} value={userId} onChange={(e) => setUserId(e.target.value)}>
              <option value="">Not a member here</option>
              {members.map((m) => <option key={m.user_id} value={m.user_id}>{m.name} ({m.email})</option>)}
            </select>
          </label>
        )}
        {teams.length > 0 && (
          <fieldset>
            <legend className="mb-1 font-medium">Teams</legend>
            <p className="mb-2 text-[12px] text-muted-foreground">Leave all unticked for a shared sender any team's sequences may use.</p>
            <div className="flex flex-wrap gap-x-4 gap-y-1">
              {teams.map((t) => (
                <label key={t.id} className="flex items-center gap-2">
                  <input type="checkbox" checked={teamIds.includes(t.id!)} onChange={() => setTeamIds(teamIds.includes(t.id!) ? teamIds.filter((x) => x !== t.id) : [...teamIds, t.id!])} />
                  {t.name}
                </label>
              ))}
            </div>
          </fieldset>
        )}
        <ErrorText>{error}</ErrorText>
        <Button disabled={!name.trim()}>Create sender</Button>
      </form>
    </Card>
  );
}

function TeamForm({ ws, members, onDone }: { ws: string; members: Member[]; onDone: (created?: string) => void }) {
  const [name, setName] = useState("");
  const [memberIds, setMemberIds] = useState<string[]>([]);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    const { error } = await api.POST("/v1/workspaces/{ws}/teams", { params: { path: { ws } }, body: { name: name.trim(), member_ids: memberIds } });
    if (error) return setError(errorMessage(error));
    onDone(name.trim());
  }

  return (
    <Card title="New team" description="Once a workspace has teams, mailboxes are only allocated after their sender is confirmed." action={<Button variant="ghost" type="button" onClick={() => onDone()}>Cancel</Button>}>
      <form onSubmit={submit} className="space-y-4">
        <Input label="Name" placeholder="e.g. Enterprise, EMEA, Agency client A" value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        {members.length > 0 && (
          <fieldset>
            <legend className="mb-1 font-medium">Members</legend>
            <p className="mb-2 text-[12px] text-muted-foreground">Members can approve another team borrowing this team's mailboxes.</p>
            <div className="flex flex-wrap gap-x-4 gap-y-1">
              {members.map((m) => (
                <label key={m.user_id} className="flex items-center gap-2">
                  <input type="checkbox" checked={memberIds.includes(m.user_id!)} onChange={() => setMemberIds(memberIds.includes(m.user_id!) ? memberIds.filter((x) => x !== m.user_id) : [...memberIds, m.user_id!])} />
                  {m.name}
                </label>
              ))}
            </div>
          </fieldset>
        )}
        <ErrorText>{error}</ErrorText>
        <Button disabled={!name.trim()}>Create team</Button>
      </form>
    </Card>
  );
}
