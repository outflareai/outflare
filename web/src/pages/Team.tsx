import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Department, Member, StaffAccount, StaffMember, Team as TeamT, TelegramSession } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, Status, toast } from "../ui";
import { plural } from "../format";
import { useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Checkbox } from "@/components/ui/checkbox";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";

type Props = { ws: string; canManage: boolean };
type Kind = NonNullable<StaffAccount["kind"]>;

const kindLabel: Record<Kind, string> = { telegram: "Telegram", email: "Work email", phone: "Phone number" };
const kindHint: Record<Kind, string> = {
  telegram: "Its username, e.g. @acme_sales_1, or its phone number until it has one",
  email: "e.g. sales1@acme.example",
  phone: "With the country code, e.g. +44 7700 900123",
};
const statusTone = { active: "green", leaving: "amber", left: "grey" } as const;
const statusLabel = { active: "Active", leaving: "Leaving", left: "Left" } as const;

// The team directory: everyone who works here, whether or not they sign in
// to Outflare, their departments and teams, and the accounts the company
// owns and hands to them (a Telegram account, a work email, a phone
// number). Accounts belong to the company: when someone leaves, they pass
// to the next person, and the history keeps who held them.
export function TeamDirectory({ ws, canManage }: Props) {
  const [people, setPeople] = useState<StaffMember[] | null>(null);
  const [departments, setDepartments] = useState<Department[]>([]);
  const [teams, setTeams] = useState<TeamT[]>([]);
  const [accounts, setAccounts] = useState<StaffAccount[]>([]);
  const [members, setMembers] = useState<Member[]>([]);
  const [editing, setEditing] = useState<StaffMember | "new" | null>(null);
  const [addingAccount, setAddingAccount] = useState(false);
  const [telegram, setTelegram] = useState<StaffAccount | null>(null);

  const load = useCallback(async () => {
    const path = { params: { path: { ws } } };
    const [p, d, t, a, m] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/staff", path),
      api.GET("/v1/workspaces/{ws}/departments", path),
      api.GET("/v1/workspaces/{ws}/teams", path),
      api.GET("/v1/workspaces/{ws}/staff-accounts", path),
      api.GET("/v1/workspaces/{ws}/members", path),
    ]);
    if (p.error) toast(errorMessage(p.error), "error");
    setPeople(p.data?.data ?? []);
    setDepartments(d.data?.data ?? []);
    setTeams(t.data?.data ?? []);
    setAccounts(a.data?.data ?? []);
    setMembers(m.data?.data ?? []);
  }, [ws]);

  useEffect(() => {
    load();
  }, [load]);

  const personName = (id?: string | null) => (id ? people?.find((p) => p.id === id)?.name ?? "Someone removed" : "");
  const deptName = (id?: string | null) => (id ? departments.find((d) => d.id === id)?.name ?? "" : "");
  const teamName = (id?: string | null) => (id ? teams.find((t) => t.id === id)?.name ?? "" : "");
  const current = (people ?? []).filter((p) => p.status !== "left");

  async function patchPerson(p: StaffMember, body: Record<string, unknown>, done: string) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/staff/{id}", { params: { path: { ws, id: p.id! } }, body });
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    load();
  }

  async function markLeft(p: StaffMember) {
    const held = p.accounts?.length ?? 0;
    const ok = await confirmAction(
      `Mark ${p.name} as left?`,
      <p>{held ? `They still hold ${plural(held, "company account")}. Hand ${held === 1 ? "it" : "them"} to someone else below, so nothing is lost. ` : ""}They stay in the directory for history.</p>,
      "Mark as left",
      false,
    );
    if (ok) patchPerson(p, { status: "left" }, `${p.name} is marked as left.`);
  }

  async function removePerson(p: StaffMember) {
    const ok = await confirmAction(`Remove ${p.name}?`, <p>Only for someone added by mistake. If they worked here, mark them as left instead.</p>, "Remove");
    if (!ok) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/staff/{id}", { params: { path: { ws, id: p.id! } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Removed ${p.name}.`);
    load();
  }

  async function newDepartment() {
    const r = await ask({ title: "New department", input: { label: "Name", placeholder: "e.g. Sales" }, choices: [{ label: "Cancel", value: "cancel" }, { label: "Create", value: "ok", variant: "primary" }] });
    if (!r || r.choice !== "ok" || !r.value.trim()) return;
    const { error } = await api.POST("/v1/workspaces/{ws}/departments", { params: { path: { ws } }, body: { name: r.value.trim() } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Created ${r.value.trim()}.`);
    load();
  }

  async function patchDepartment(d: Department, body: Record<string, unknown>, done: string) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/departments/{id}", { params: { path: { ws, id: d.id! } }, body });
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    load();
  }

  async function renameDepartment(d: Department) {
    const r = await ask({ title: `Rename ${d.name}`, input: { label: "Name", placeholder: d.name }, choices: [{ label: "Cancel", value: "cancel" }, { label: "Rename", value: "ok", variant: "primary" }] });
    if (r?.choice === "ok" && r.value.trim()) patchDepartment(d, { name: r.value.trim() }, `Renamed to ${r.value.trim()}.`);
  }

  async function removeDepartment(d: Department) {
    const ok = await confirmAction(`Delete ${d.name}?`, <p>Its {plural(d.people ?? 0, "person", "people")} and {plural(d.teams ?? 0, "team")} stay, without a department.</p>, "Delete department");
    if (!ok) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/departments/{id}", { params: { path: { ws, id: d.id! } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Deleted ${d.name}.`);
    load();
  }

  async function patchAccount(a: StaffAccount, body: Record<string, unknown>, done: string) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/staff-accounts/{id}", { params: { path: { ws, id: a.id! } }, body });
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    load();
  }

  async function retireAccount(a: StaffAccount) {
    const ok = await confirmAction(`Retire ${a.address}?`, <p>Nobody will hold it any more. Its history stays. Do this only once the account or number is really out of use.</p>, "Retire", false);
    if (ok) patchAccount(a, { status: "retired" }, `Retired ${a.address}.`);
  }

  async function removeAccount(a: StaffAccount) {
    const ok = await confirmAction(`Remove ${a.address}?`, <p>Only for an account added by mistake. One someone has held is retired instead, so its history stays.</p>, "Remove");
    if (!ok) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/staff-accounts/{id}", { params: { path: { ws, id: a.id! } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Removed ${a.address}.`);
    load();
  }

  if (people === null) return <Loading />;
  const leaving = people.filter((p) => p.status !== "active" && (p.accounts?.length ?? 0) > 0);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Team"
        description="Everyone who works here, with their department, team and the accounts the company gives them. Company accounts stay with the company: when someone leaves, hand theirs to the next person."
        action={canManage && (
          <>
            <Button variant="ghost" onClick={() => setAddingAccount(!addingAccount)}>Add company account</Button>
            <Button onClick={() => setEditing(editing === "new" ? null : "new")}>Add person</Button>
          </>
        )}
      />

      {leaving.length > 0 && (
        <section className="rounded-lg border border-amber-600/30 bg-amber-500/[0.06] px-4 py-3">
          <p className="font-medium">{plural(leaving.length, "person who is leaving still holds", "people who are leaving still hold")} company accounts</p>
          <p className="text-[12px] text-muted-foreground">Hand each account to someone else in Company accounts below, so their customers' conversations carry on.</p>
          <ul className="mt-1 text-[12px]">
            {leaving.map((p) => <li key={p.id}>{p.name}: {(p.accounts ?? []).map((a) => a.address).join(", ")}</li>)}
          </ul>
        </section>
      )}

      {editing && (
        <PersonForm
          ws={ws}
          person={editing === "new" ? null : editing}
          people={people}
          departments={departments}
          teams={teams}
          members={members}
          onDone={(name) => {
            setEditing(null);
            if (name) {
              toast(editing === "new" ? `Added ${name}.` : `Saved ${name}.`);
              load();
            }
          }}
        />
      )}
      {addingAccount && <AccountForm ws={ws} people={current} onDone={(addr) => { setAddingAccount(false); if (addr) { toast(`Added ${addr}.`); load(); } }} />}

      <section className="space-y-3">
        <h2 className="flex items-baseline gap-2 text-[13px] font-semibold">People <span className="font-mono text-[12px] font-normal text-muted-foreground">{current.length}</span></h2>
        {people.length === 0 ? (
          <Empty title="Nobody here yet" action={canManage && <Button onClick={() => setEditing("new")}>Add person</Button>}>
            Add everyone who talks to customers or partners, including people who never sign in to Outflare. Their company accounts, and the conversations on them, hang off this list.
          </Empty>
        ) : (
          <DataTable
            data={people}
            rowId={(p) => p.id!}
            searchPlaceholder="Search people"
            initialSort={[{ id: "status", desc: false }, { id: "name", desc: false }]}
            columns={[
              {
                id: "name",
                header: "Name",
                accessorFn: (p) => p.name,
                cell: ({ row: { original: p } }) => (
                  <span className="block min-w-0">
                    <span className="font-medium">{p.name}</span>
                    {p.title && <span className="block text-[12px] text-muted-foreground">{p.title}</span>}
                  </span>
                ),
              },
              { id: "department", header: "Department", accessorFn: (p) => deptName(p.department_id), cell: ({ row: { original: p } }) => <span className="text-muted-foreground">{deptName(p.department_id) || "None"}</span> },
              { id: "team", header: "Team", accessorFn: (p) => teamName(p.team_id), cell: ({ row: { original: p } }) => <span className="text-muted-foreground">{teamName(p.team_id) || "None"}</span> },
              { id: "manager", header: "Manager", accessorFn: (p) => personName(p.manager_id), cell: ({ row: { original: p } }) => <span className="text-muted-foreground">{personName(p.manager_id) || "None"}</span> },
              {
                id: "accounts",
                header: "Company accounts",
                accessorFn: (p) => p.accounts?.length ?? 0,
                cell: ({ row: { original: p } }) =>
                  p.accounts?.length ? (
                    <span className="flex flex-wrap gap-1">{p.accounts.map((a) => <Badge key={a.id} title={kindLabel[a.kind as Kind]}>{a.kind === "telegram" && !a.address.startsWith("+") ? "@" : ""}{a.address}</Badge>)}</span>
                  ) : (
                    <span className="text-muted-foreground">None</span>
                  ),
              },
              { id: "status", header: "Status", accessorFn: (p) => ["active", "leaving", "left"].indexOf(p.status ?? "active"), cell: ({ row: { original: p } }) => <Status tone={statusTone[p.status ?? "active"]}>{statusLabel[p.status ?? "active"]}</Status> },
              ...(canManage
                ? [{
                    id: "actions",
                    header: () => <span className="sr-only">Actions</span>,
                    enableSorting: false,
                    meta: { align: "right" as const },
                    cell: ({ row: { original: p } }: { row: { original: StaffMember } }) => (
                      <Menu
                        items={[
                          { label: "Edit", onClick: () => setEditing(p) },
                          p.status === "active" && { label: "Start their handover (leaving)", onClick: () => patchPerson(p, { status: "leaving" }, `${p.name} is leaving: hand over their accounts below.`) },
                          p.status !== "left" && { label: "Mark as left", onClick: () => markLeft(p) },
                          p.status !== "active" && { label: "Mark as active", onClick: () => patchPerson(p, { status: "active", left_on: null }, `${p.name} is active again.`) },
                          { label: "Remove", onClick: () => removePerson(p), danger: true },
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
        <h2 className="flex items-baseline gap-2 text-[13px] font-semibold">Company accounts <span className="font-mono text-[12px] font-normal text-muted-foreground">{accounts.filter((a) => a.status === "active").length}</span></h2>
        {accounts.length === 0 ? (
          <Empty title="No company accounts yet" action={canManage && <Button variant="ghost" onClick={() => setAddingAccount(true)}>Add company account</Button>}>
            Add the Telegram accounts, work emails and phone numbers the company owns, and who holds each one. They stay with the company when someone leaves.
          </Empty>
        ) : (
          <DataTable
            data={accounts}
            rowId={(a) => a.id!}
            searchPlaceholder="Search accounts"
            initialSort={[{ id: "kind", desc: false }]}
            columns={[
              { id: "kind", header: "Kind", accessorFn: (a) => kindLabel[a.kind as Kind], cell: ({ row: { original: a } }) => <span className="text-muted-foreground">{kindLabel[a.kind as Kind]}</span> },
              {
                id: "address",
                header: "Account",
                accessorFn: (a) => `${a.address} ${a.label}`,
                cell: ({ row: { original: a } }) => (
                  <span className="block min-w-0">
                    <span className="font-mono text-[12px]">{a.kind === "telegram" && !a.address.startsWith("+") ? "@" : ""}{a.address}</span>
                    {a.label && <span className="block text-[12px] text-muted-foreground">{a.label}</span>}
                  </span>
                ),
              },
              { id: "holder", header: "Held by", accessorFn: (a) => personName(a.holder_id), cell: ({ row: { original: a } }) => (a.holder_id ? <span>{personName(a.holder_id)}</span> : <span className="text-muted-foreground">Nobody</span>) },
              { id: "renews", header: "Renews", accessorFn: (a) => a.renews_on ?? "", meta: { mono: true }, cell: ({ row: { original: a } }) => <span className="text-muted-foreground">{a.renews_on ?? "—"}</span> },
              { id: "status", header: "Status", accessorFn: (a) => a.status, cell: ({ row: { original: a } }) => <Status tone={a.status === "active" ? "green" : "grey"}>{a.status === "active" ? "Active" : "Retired"}</Status> },
              ...(canManage
                ? [{
                    id: "actions",
                    header: () => <span className="sr-only">Actions</span>,
                    enableSorting: false,
                    meta: { align: "right" as const },
                    cell: ({ row: { original: a } }: { row: { original: StaffAccount } }) => (
                      <Menu
                        items={[
                          ...(a.status === "active"
                            ? current.filter((p) => p.id !== a.holder_id && p.status === "active").map((p) => ({ label: `Hand to ${p.name}`, onClick: () => patchAccount(a, { holder_id: p.id }, `${a.address} is now held by ${p.name}.`) }))
                            : []),
                          a.kind === "telegram" && a.status === "active" && { label: "Sign in to Telegram…", onClick: () => setTelegram(a) },
                          a.status === "active" && a.holder_id != null && { label: "Take it back (nobody holds it)", onClick: () => patchAccount(a, { holder_id: null }, `Nobody holds ${a.address} now.`) },
                          a.status === "active" ? { label: "Retire", onClick: () => retireAccount(a) } : { label: "Reactivate", onClick: () => patchAccount(a, { status: "active" }, `Reactivated ${a.address}.`) },
                          { label: "Remove", onClick: () => removeAccount(a), danger: true },
                        ]}
                      />
                    ),
                  }]
                : []),
            ]}
          />
        )}
      </section>

      {telegram && <TelegramPanel ws={ws} account={telegram} canManage={canManage} onClose={() => setTelegram(null)} />}

      <section className="space-y-3">
        <h2 className="flex items-baseline justify-between gap-2 text-[13px] font-semibold">
          <span>Departments <span className="font-mono text-[12px] font-normal text-muted-foreground">{departments.length}</span></span>
          {canManage && <Button variant="ghost" onClick={newDepartment}>New department</Button>}
        </h2>
        {departments.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">No departments yet. Add Sales, Support or Partnerships to group people and teams.</p>
        ) : (
          <DataTable
            data={departments}
            rowId={(d) => d.id!}
            columnsMenu={false}
            initialSort={[{ id: "name", desc: false }]}
            columns={[
              { id: "name", header: "Name", accessorFn: (d) => d.name, cell: ({ row: { original: d } }) => <span className="font-medium">{d.name}</span> },
              { id: "head", header: "Head", accessorFn: (d) => personName(d.head_id), cell: ({ row: { original: d } }) => <span className="text-muted-foreground">{personName(d.head_id) || "None"}</span> },
              { id: "people", header: "People", accessorFn: (d) => d.people ?? 0, meta: { align: "right", mono: true } },
              { id: "teams", header: "Teams", accessorFn: (d) => d.teams ?? 0, meta: { align: "right", mono: true } },
              ...(canManage
                ? [{
                    id: "actions",
                    header: () => <span className="sr-only">Actions</span>,
                    enableSorting: false,
                    meta: { align: "right" as const },
                    cell: ({ row: { original: d } }: { row: { original: Department } }) => (
                      <Menu
                        items={[
                          { label: "Rename", onClick: () => renameDepartment(d) },
                          ...current.filter((p) => p.id !== d.head_id).map((p) => ({ label: `Make ${p.name} head`, onClick: () => patchDepartment(d, { head_id: p.id }, `${p.name} heads ${d.name}.`) })),
                          { label: "Delete", onClick: () => removeDepartment(d), danger: true },
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

function Select({ id, label, value, onChange, options, none }: { id: string; label: string; value: string; onChange: (v: string) => void; options: { value: string; label: string }[]; none: string }) {
  return (
    <label className="block" htmlFor={id}>
      <span className="mb-1.5 block font-medium">{label}</span>
      <select id={id} className={`${field} w-full`} value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">{none}</option>
        {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
      </select>
    </label>
  );
}

function PersonForm({ ws, person, people, departments, teams, members, onDone }: {
  ws: string;
  person: StaffMember | null;
  people: StaffMember[];
  departments: Department[];
  teams: TeamT[];
  members: Member[];
  onDone: (name?: string) => void;
}) {
  const [name, setName] = useState(person?.name ?? "");
  const [email, setEmail] = useState(person?.email ?? "");
  const [title, setTitle] = useState(person?.title ?? "");
  const [dept, setDept] = useState(person?.department_id ?? "");
  const [team, setTeam] = useState(person?.team_id ?? "");
  const [manager, setManager] = useState(person?.manager_id ?? "");
  const [user, setUser] = useState(person?.user_id ?? "");
  const [started, setStarted] = useState(person?.started_on ?? "");
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    const body = {
      name: name.trim(),
      email: email.trim() || null,
      title: title.trim(),
      department_id: dept || null,
      team_id: team || null,
      manager_id: manager || null,
      user_id: user || null,
      started_on: started || null,
    };
    const { error } = person
      ? await api.PATCH("/v1/workspaces/{ws}/staff/{id}", { params: { path: { ws, id: person.id! } }, body })
      : await api.POST("/v1/workspaces/{ws}/staff", { params: { path: { ws } }, body });
    if (error) return setError(errorMessage(error));
    onDone(name.trim());
  }

  return (
    <Card title={person ? `Edit ${person.name}` : "Add person"} action={<Button variant="ghost" type="button" onClick={() => onDone()}>Cancel</Button>}>
      <form onSubmit={submit} className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Input id="staff-name" label="Name" placeholder="e.g. Ana Silva" value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
          <Input id="staff-email" label="Work email" type="email" placeholder="ana@acme.example" value={email} onChange={(e) => setEmail(e.target.value)} />
          <Input id="staff-title" label="Job title" placeholder="e.g. Account manager" value={title} onChange={(e) => setTitle(e.target.value)} />
          <Input id="staff-started" label="Started on" type="date" value={started} onChange={(e) => setStarted(e.target.value)} />
          <Select id="staff-dept" label="Department" value={dept} onChange={setDept} none="No department" options={departments.map((d) => ({ value: d.id!, label: d.name! }))} />
          <Select id="staff-team" label="Team" value={team} onChange={setTeam} none="No team" options={teams.map((t) => ({ value: t.id!, label: t.name! }))} />
          <Select id="staff-manager" label="Manager" value={manager} onChange={setManager} none="No manager" options={people.filter((p) => p.id !== person?.id && p.status !== "left").map((p) => ({ value: p.id!, label: p.name! }))} />
          <Select id="staff-user" label="Outflare sign-in" value={user} onChange={setUser} none="Doesn't sign in" options={members.map((m) => ({ value: m.user_id!, label: `${m.name} (${m.email})` }))} />
        </div>
        <ErrorText>{error}</ErrorText>
        <Button disabled={!name.trim()}>{person ? "Save" : "Add person"}</Button>
      </form>
    </Card>
  );
}

function AccountForm({ ws, people, onDone }: { ws: string; people: StaffMember[]; onDone: (address?: string) => void }) {
  const [kind, setKind] = useState<Kind>("telegram");
  const [address, setAddress] = useState("");
  const [label, setLabel] = useState("");
  const [holder, setHolder] = useState("");
  const [renews, setRenews] = useState("");
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    const { data, error } = await api.POST("/v1/workspaces/{ws}/staff-accounts", {
      params: { path: { ws } },
      body: { kind, address: address.trim(), label: label.trim(), holder_id: holder || null, renews_on: renews || null },
    });
    if (error) return setError(errorMessage(error));
    onDone(data?.address ?? address.trim());
  }

  return (
    <Card title="Add company account" description="An account the company owns and gives to someone: it stays with the company when they leave." action={<Button variant="ghost" type="button" onClick={() => onDone()}>Cancel</Button>}>
      <form onSubmit={submit} className="space-y-4">
        <fieldset>
          <legend className="mb-2 font-medium">Kind</legend>
          <div className="grid gap-2 sm:grid-cols-3">
            {(Object.keys(kindLabel) as Kind[]).map((k) => (
              <button key={k} type="button" onClick={() => setKind(k)} aria-pressed={kind === k} className={`rounded-lg border px-3 py-2 text-left transition-colors ${kind === k ? "border-brand/60 bg-brand/[0.07] ring-1 ring-brand/40" : "border-border hover:border-ring/60"}`}>
                <span className="block font-medium">{kindLabel[k]}</span>
              </button>
            ))}
          </div>
        </fieldset>
        <div className="grid gap-4 sm:grid-cols-2">
          <Input id="account-address" label={kindLabel[kind]} hint={kindHint[kind]} value={address} onChange={(e) => setAddress(e.target.value)} required autoFocus />
          <Input id="account-label" label="Label (optional)" placeholder="e.g. Sales 1" value={label} onChange={(e) => setLabel(e.target.value)} />
          <Select id="account-holder" label="Held by" value={holder} onChange={setHolder} none="Nobody yet" options={people.filter((p) => p.status === "active").map((p) => ({ value: p.id!, label: p.name! }))} />
          {kind === "phone" && <Input id="account-renews" label="Renews on (optional)" hint="When the plan must be renewed or topped up, so the number never lapses." type="date" value={renews} onChange={(e) => setRenews(e.target.value)} />}
        </div>
        <ErrorText>{error}</ErrorText>
        <Button disabled={!address.trim()}>Add account</Button>
      </form>
    </Card>
  );
}

const telegramSteps: { id: string; label: string; hint: string }[] = [
  { id: "number_owned", label: "The phone number is the company's", hint: "Rented on the company's own account (Twilio, Telnyx), never a person's own SIM." },
  { id: "two_step_password", label: "Two-step password set", hint: "Telegram > Settings > Privacy and Security > Two-Step Verification." },
  { id: "login_email", label: "Company login email set", hint: "Sign-in codes go to a company inbox, so the number isn't the only way in." },
  { id: "passkey", label: "Passkey added", hint: "Privacy and Security > Passkeys, saved in the company's password manager." },
  { id: "password_saved", label: "Password saved in the company's password manager", hint: "With the number and the login email." },
  { id: "other_sessions", label: "Unknown devices signed out", hint: "Settings > Devices: keep only the holder's phone and Outflare." },
  { id: "auto_delete_off", label: "Account deletion set to the longest time", hint: "Privacy and Security > Delete my account if away for: 24 months." },
];

const telegramStatus: Record<TelegramSession["status"], { label: string; tone: "green" | "amber" | "grey" | "red" }> = {
  not_connected: { label: "Not signed in", tone: "grey" },
  starting: { label: "Starting", tone: "amber" },
  waiting_scan: { label: "Waiting for the scan", tone: "amber" },
  needs_password: { label: "Needs the two-step password", tone: "amber" },
  checking_password: { label: "Checking the password", tone: "amber" },
  connected: { label: "Signed in", tone: "green" },
  expired: { label: "The QR code ran out", tone: "grey" },
  disconnecting: { label: "Signing out", tone: "amber" },
  signed_out: { label: "Signed out", tone: "grey" },
  failed: { label: "Couldn't sign in", tone: "red" },
};

const telegramError: Record<string, string> = {
  wrong_password: "That two-step password was wrong. Try again.",
  signed_out: "Telegram ended Outflare's session, from the account's phone or by Telegram itself. Sign in again.",
  bad_app: "Telegram refused your app's api_id or api_hash. Check them on the Telegram connection.",
  no_app: "The Telegram connection is gone. Connect your Telegram app again under Connectors.",
  unreachable: "Couldn't reach Telegram. It is tried again; if it keeps happening, check the proxy on the Telegram connection.",
};

const busy = new Set<TelegramSession["status"]>(["starting", "waiting_scan", "checking_password", "disconnecting"]);

// TelegramPanel signs a company Telegram account in to Outflare as a linked
// device: someone with the account's phone scans the QR code once, the way
// Telegram Desktop links. Outflare then only reads its chats.
const syncLabel: Record<string, string> = {
  off: "Not syncing",
  importing: "Copying history",
  live: "Up to date",
  paused: "Paused",
};

const syncErrors: Record<string, string> = {
  unreachable: "Couldn't reach Telegram on the last try. It tries again in a few minutes.",
  slowed_down: "Telegram asked Outflare to slow down. It carries on by itself.",
  signed_out: "The session ended. Sign in again with a QR code.",
  no_app: "The Telegram app connection is gone. Add it again under Connectors.",
  bad_app: "Telegram refused the app's api_id or api_hash. Check them under Connectors.",
};

// TelegramSync copies a signed-in account's chats into one brand's CRM.
function TelegramSync({ ws, session, canManage, run, path }: {
  ws: string;
  session: TelegramSession;
  canManage: boolean;
  run: (call: () => Promise<{ data?: TelegramSession; error?: unknown }>) => Promise<void>;
  path: { params: { path: { ws: string; id: string } } };
}) {
  const { active, byId } = useBrand(ws);
  const [brand, setBrand] = useState(session.brand_id ?? "");
  const [days, setDays] = useState(String(session.history_days));
  const syncing = session.sync_status === "importing" || session.sync_status === "live";
  const start = () =>
    run(() => api.POST("/v1/workspaces/{ws}/staff-accounts/{id}/telegram/sync", { ...path, body: { brand_id: brand || undefined, history_days: Number(days) } }));
  const pause = () => run(() => api.POST("/v1/workspaces/{ws}/staff-accounts/{id}/telegram/sync/pause", path));
  return (
    <div className="space-y-2 rounded-lg border p-4">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-[13px] font-semibold">Chats in the CRM</h3>
        <Status tone={session.sync_status === "live" ? "green" : session.sync_status === "importing" ? "blue" : "grey"}>{syncLabel[session.sync_status]}</Status>
      </div>
      {session.sync_status !== "off" && (
        <p className="text-[12px] text-muted-foreground">
          {plural(session.chats, "chat")} and {plural(session.messages, "message")} in {byId(session.brand_id)?.name ?? "the brand"}'s CRM.
          {session.synced_at && ` Last synced ${new Date(session.synced_at).toLocaleString()}.`}
          {session.sync_status === "importing" && " New messages arrive every couple of minutes once the history is in."}
        </p>
      )}
      {session.sync_error && <ErrorText>{syncErrors[session.sync_error] ?? session.sync_error}</ErrorText>}
      {canManage && (syncing ? (
        <Button variant="ghost" onClick={pause}>Pause</Button>
      ) : (
        <div className="grid gap-2 sm:grid-cols-2">
          <label className="text-[12px]">
            Brand
            <select className={`${field} mt-1 w-full`} value={brand} disabled={session.chats > 0} onChange={(e) => setBrand(e.target.value)}>
              <option value="">Pick a brand</option>
              {active.map((b) => (
                <option key={b.id} value={b.id}>{b.name}</option>
              ))}
            </select>
          </label>
          <label className="text-[12px]">
            History
            <select className={`${field} mt-1 w-full`} value={days} onChange={(e) => setDays(e.target.value)}>
              <option value="30">Last 30 days</option>
              <option value="90">Last 3 months</option>
              <option value="365">Last year</option>
              <option value="1095">Last 3 years</option>
              <option value="0">Only new messages</option>
            </select>
          </label>
          <Button className="sm:col-span-2" disabled={!brand} onClick={start}>{session.sync_status === "paused" ? "Start syncing again" : "Start syncing"}</Button>
          <p className="text-[12px] text-muted-foreground sm:col-span-2">
            Read only: nothing is sent and messages stay unread on the phone. Chats with colleagues, Saved Messages, bots and channels are left out.
          </p>
        </div>
      ))}
    </div>
  );
}

function TelegramPanel({ ws, account, canManage, onClose }: { ws: string; account: StaffAccount; canManage: boolean; onClose: () => void }) {
  const [session, setSession] = useState<TelegramSession | null>(null);
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const path = { params: { path: { ws, id: account.id } } };

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/staff-accounts/{id}/telegram", path);
    if (error) setError(errorMessage(error));
    else setSession(data);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, account.id]);

  useEffect(() => {
    load();
  }, [load]);
  // While something is happening, look again every two seconds.
  useEffect(() => {
    if (!session) return;
    const importing = session.status === "connected" && session.sync_status === "importing";
    if (!busy.has(session.status) && !importing) return;
    const t = setTimeout(load, importing ? 5000 : 2000);
    return () => clearTimeout(t);
  }, [session, load]);

  async function run(call: () => Promise<{ data?: TelegramSession; error?: unknown }>) {
    setError("");
    const { data, error } = await call();
    if (error) setError(errorMessage(error));
    else if (data) setSession(data);
  }
  const connect = () => run(() => api.POST("/v1/workspaces/{ws}/staff-accounts/{id}/telegram/connect", { ...path, body: {} }));
  const signOut = async () => {
    if (await confirmAction("Sign this account out of Outflare?", <p>Its chats stop coming in until someone scans the QR code again.</p>, "Sign out")) {
      run(() => api.POST("/v1/workspaces/{ws}/staff-accounts/{id}/telegram/disconnect", path));
    }
  };
  async function sendPassword(e: FormEvent) {
    e.preventDefault();
    await run(() => api.POST("/v1/workspaces/{ws}/staff-accounts/{id}/telegram/password", { ...path, body: { password } }));
    setPassword("");
  }
  async function tick(step: string, value: boolean) {
    await run(() => api.PATCH("/v1/workspaces/{ws}/staff-accounts/{id}/telegram", { ...path, body: { checklist: { [step]: value } } }));
  }
  const status = session ? telegramStatus[session.status] : null;
  const name = account.address.startsWith("+") ? account.address : `@${account.address}`;

  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>Telegram: {name}</SheetTitle>
          <SheetDescription>
            Outflare signs in as a linked device, like Telegram Desktop, and only reads this account's chats so they reach customer records. It never sends from it.
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4 pb-6">
          {!session ? (
            <Loading />
          ) : (
            <>
              <div className="flex items-center justify-between gap-3">
                {status && <Status tone={status.tone}>{status.label}</Status>}
                {canManage && (
                  <span className="flex gap-2">
                    {session.status === "connected" && <Button variant="ghost" onClick={() => run(async () => { const r = await api.POST("/v1/workspaces/{ws}/staff-accounts/{id}/telegram/check", path); if (!r.error) toast("Checking it now."); return { error: r.error }; })}>Check now</Button>}
                    {["connected", "needs_password", "waiting_scan", "starting", "failed"].includes(session.status) && <Button variant="ghost" onClick={signOut}>Sign out</Button>}
                    {["not_connected", "expired", "signed_out", "failed"].includes(session.status) && <Button onClick={connect}>{session.status === "not_connected" ? "Sign in with a QR code" : "Show a new QR code"}</Button>}
                  </span>
                )}
              </div>
              {session.error_code && <ErrorText>{telegramError[session.error_code] ?? session.error_code}</ErrorText>}
              <ErrorText>{error}</ErrorText>

              {session.status === "waiting_scan" && session.login_qr && (
                <div className="space-y-3 rounded-lg border p-4 text-center">
                  <img src={session.login_qr} alt="QR code to link this Telegram account" className="mx-auto size-56 rounded bg-white p-2" />
                  <p className="text-[12px] text-muted-foreground">
                    On the phone with {name}: Telegram &gt; Settings &gt; Devices &gt; Link Desktop Device, then scan this. A new code appears every 30 seconds.
                  </p>
                </div>
              )}
              {session.status === "starting" && <p className="text-[12px] text-muted-foreground">Making a QR code…</p>}

              {session.status === "needs_password" && canManage && (
                <form onSubmit={sendPassword} className="space-y-2">
                  <Input id="telegram-password" label="Two-step password" type="password" autoComplete="off" hint={session.password_hint ? `Hint: ${session.password_hint}. Used once to finish signing in, then deleted.` : "Used once to finish signing in, then deleted."} value={password} onChange={(e) => setPassword(e.target.value)} />
                  <Button type="submit" disabled={!password}>Finish signing in</Button>
                </form>
              )}

              {session.status === "connected" && (
                <dl className="grid grid-cols-[auto,1fr] gap-x-4 gap-y-1 text-[13px]">
                  <dt className="text-muted-foreground">Signed in as</dt>
                  <dd>{session.telegram_name} {session.telegram_username && <span className="font-mono text-[12px]">@{session.telegram_username}</span>}</dd>
                  {session.telegram_phone && (<><dt className="text-muted-foreground">Number</dt><dd className="font-mono text-[12px]">{session.telegram_phone}</dd></>)}
                  <dt className="text-muted-foreground">Other devices</dt>
                  <dd>{session.other_sessions ?? "Not checked yet"}</dd>
                  <dt className="text-muted-foreground">Last checked</dt>
                  <dd>{session.last_checked_at ? new Date(session.last_checked_at).toLocaleString() : "Not yet"}</dd>
                </dl>
              )}
              {session.status === "connected" && session.telegram_username && session.telegram_username !== account.address && !account.address.startsWith("+") && (
                <p className="text-[12px] text-amber-700">This is signed in as @{session.telegram_username}, not {name}. Check the right phone scanned it, or change the account's username here.</p>
              )}

              {session.status === "connected" && <TelegramSync ws={ws} session={session} canManage={canManage} run={run} path={path} />}

              <div className="space-y-2">
                <h3 className="text-[13px] font-semibold">Lock it down</h3>
                <p className="text-[12px] text-muted-foreground">So the company never loses the account when someone leaves or a phone is lost.</p>
                <ul className="space-y-2">
                  {telegramSteps.map((step) => {
                    const fromTelegram = step.id === "two_step_password" ? session.two_step : step.id === "login_email" ? session.login_email : null;
                    const done = fromTelegram ?? session.checklist[step.id] ?? false;
                    return (
                      <li key={step.id} className="flex items-start gap-2">
                        <Checkbox id={`tg-${step.id}`} checked={done} disabled={!canManage || fromTelegram != null} onCheckedChange={(v) => tick(step.id, v === true)} />
                        <label htmlFor={`tg-${step.id}`} className="text-[13px]">
                          {step.label}
                          {fromTelegram != null && <span className="ml-1 text-[11px] text-muted-foreground">(read from Telegram)</span>}
                          <span className="block text-[12px] text-muted-foreground">{step.hint}</span>
                        </label>
                      </li>
                    );
                  })}
                </ul>
              </div>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
