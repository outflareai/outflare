import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Invite, Member, WorkspaceRole } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, ErrorText, field, Input, PageHeader, toast } from "../ui";
import { CopyButton } from "./Tokens";
import { DataTable } from "@/components/data-table";
import { BrandsPicker, brandsLabel } from "@/components/brand-picker";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { useBrand } from "@/lib/brand";
import { baseScope, otherScopes, roleGroups, roleLevel, setRoleLevel, within, type RoleLevel } from "@/lib/rolePermissions";

type Scope = WorkspaceRole["scopes"][number];

// roleKey is how a member or invite names its role in the roles list.
const roleKey = (v: { role?: string; role_id?: string | null }) => v.role_id ?? v.role ?? "";

export function Members({ ws, myId, myScopes, myRole }: { ws: string; myId: string; myScopes: string[]; myRole?: string }) {
  const [members, setMembers] = useState<Member[]>([]);
  const [invites, setInvites] = useState<Invite[]>([]);
  const [roles, setRoles] = useState<WorkspaceRole[]>([]);
  const [error, setError] = useState("");
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("");
  const [inviteBrands, setInviteBrands] = useState<string[]>([]);
  const [link, setLink] = useState<{ email: string; url: string; emailed: boolean } | null>(null);
  const [editing, setEditing] = useState<WorkspaceRole | "new" | null>(null);
  const { active: brands } = useBrand(ws);
  const path = { params: { path: { ws } } };
  const can = (s: string) => myScopes.includes(s);
  const canInvite = can("members.manage");
  const canRemove = can("members.remove");
  const isOwner = myRole === "owner";

  const load = useCallback(async () => {
    const [m, r] = await Promise.all([api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }), api.GET("/v1/workspaces/{ws}/roles", { params: { path: { ws } } })]);
    setMembers(m.data?.data ?? []);
    setRoles(r.data?.data ?? []);
    if (canInvite) {
      const i = await api.GET("/v1/workspaces/{ws}/invites", { params: { path: { ws } } });
      setInvites(i.data?.data ?? []);
    }
  }, [ws, canInvite]);

  useEffect(() => {
    load();
  }, [load]);

  const find = (id: string) => roles.find((r) => r.id === id);
  // Roles you may give: nothing you can't do yourself, and Owner only from an owner.
  const assignable = roles.filter((r) => (r.locked ? isOwner : within(r.scopes, myScopes)));
  // Default to Admin for the people who can give it, since most invites are your own team.
  const inviteRole = role || (assignable.some((r) => r.id === "admin") ? "admin" : assignable.find((r) => r.id === "operator")?.id ?? assignable[0]?.id ?? "");
  const chosen = find(inviteRole);

  async function changeRole(m: Member, id: string) {
    const r = find(id);
    const body = r?.builtin ? { role: r.builtin } : { role_id: id };
    const { error } = await api.PATCH("/v1/workspaces/{ws}/members/{user}", { params: { path: { ws, user: m.user_id! } }, body });
    setError(error ? errorMessage(error) : "");
    if (!error) toast(`${m.name} is now ${r?.name ?? "on a new role"}.`);
    load();
  }

  async function changeBrands(m: Member, brand_ids: string[]) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/members/{user}", { params: { path: { ws, user: m.user_id! } }, body: { brand_ids } });
    setError(error ? errorMessage(error) : "");
    if (!error) toast(`${m.name} works on ${brand_ids.length ? brandsLabel(brand_ids, brands) : "every brand"}.`);
    load();
  }

  async function remove(m: Member) {
    if (!(await confirmAction(`Remove ${m.name} from the workspace?`, <p>They lose access straight away. What they did stays in the audit log.</p>, "Remove"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/members/{user}", { params: { path: { ws, user: m.user_id! } } });
    setError(error ? errorMessage(error) : "");
    if (!error) toast(`Removed ${m.name}.`);
    load();
  }

  async function leave() {
    if (!(await confirmAction("Leave this workspace?", <p>You lose access straight away, and API tokens you made here stop working. Someone will need to invite you again.</p>, "Leave"))) return;
    const { error } = await api.POST("/v1/workspaces/{ws}/leave", path);
    if (error) return setError(errorMessage(error));
    window.location.assign("/");
  }

  async function invite(e: FormEvent) {
    e.preventDefault();
    const r = find(inviteRole);
    const brand_ids = r?.brand_limitable ? inviteBrands : [];
    const body = r?.builtin ? { email, role: r.builtin, brand_ids } : { email, role_id: inviteRole, brand_ids };
    const { data, error } = await api.POST("/v1/workspaces/{ws}/invites", { ...path, body });
    if (error) return setError(errorMessage(error));
    setError("");
    setLink({ email, url: data?.invite_url ?? "", emailed: !!data?.emailed });
    toast(data?.emailed ? `Emailed an invite to ${email}.` : `Made an invite for ${email}.`);
    setEmail("");
    load();
  }

  async function revoke(i: Invite) {
    if (!(await confirmAction(`Cancel the invite for ${i.email}?`, <p>Their invite link stops working.</p>, "Cancel invite"))) return;
    await api.DELETE("/v1/workspaces/{ws}/invites/{id}", { params: { path: { ws, id: i.id! } } });
    toast(`Cancelled the invite for ${i.email}.`);
    load();
  }

  // You can change someone whose role is entirely within yours; owners only by owners.
  const canEdit = (m: Member) => {
    const r = find(roleKey(m));
    return canInvite && m.user_id !== myId && !!r && (r.locked ? isOwner : within(r.scopes, myScopes));
  };

  return (
    <div className="space-y-6">
      <PageHeader title="Members" description="Who can use this workspace, and what each person can do." action={<Button variant="ghost" onClick={leave}>Leave workspace</Button>} />
      <ErrorText>{error}</ErrorText>
      <Card title={`${members.length} ${members.length === 1 ? "member" : "members"}`}>
        <DataTable
          data={members}
          rowId={(m) => m.user_id!}
          columnsMenu={false}
          columns={[
            {
              id: "name",
              header: "Member",
              accessorFn: (m) => m.name,
              cell: ({ row: { original: m } }) => (
                <div className="flex min-w-0 items-center gap-3">
                  <span aria-hidden="true" className="flex size-7 shrink-0 items-center justify-center rounded-full bg-muted font-mono text-[11px] font-medium uppercase">{(m.name || m.email || "?")[0]}</span>
                  <div className="min-w-0">
                    <div className="font-medium">{m.name}{m.user_id === myId && <span className="ml-1.5 font-normal"><Badge>you</Badge></span>}</div>
                    <div className="truncate font-mono text-[12px] text-muted-foreground">{m.email}</div>
                  </div>
                </div>
              ),
            },
            {
              id: "role",
              header: "Role",
              accessorFn: (m) => roles.findIndex((r) => r.id === roleKey(m)),
              cell: ({ row: { original: m } }) => {
                const r = find(roleKey(m));
                return canEdit(m) ? (
                  <select aria-label={`Role for ${m.name}`} title={r?.description} className={field} value={roleKey(m)} onChange={(e) => changeRole(m, e.target.value)}>
                    {assignable.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
                  </select>
                ) : (
                  <Badge title={r?.description}>{m.role_name || r?.name || m.role}</Badge>
                );
              },
            },
            ...(brands.length > 0
              ? [{
                  id: "brands",
                  header: "Brands",
                  enableSorting: false,
                  cell: ({ row: { original: m } }: { row: { original: Member } }) => {
                    const limitable = !!find(roleKey(m))?.brand_limitable;
                    return canEdit(m) && limitable ? (
                      <BrandsPicker label={`Brands for ${m.name}`} value={m.brand_ids ?? []} brands={brands} onChange={(ids) => changeBrands(m, ids)} />
                    ) : (
                      <span className="text-[12px] text-muted-foreground" title={limitable ? undefined : "This role covers the whole workspace, so it works on every brand"}>{brandsLabel(m.brand_ids, brands)}</span>
                    );
                  },
                }]
              : []),
            { id: "can", header: "Can", enableSorting: false, cell: ({ row: { original: m } }) => <span className="text-[12px] text-muted-foreground">{find(roleKey(m))?.description}</span> },
            { id: "actions", header: () => <span className="sr-only">Actions</span>, enableSorting: false, meta: { align: "right" }, cell: ({ row: { original: m } }) => canRemove && canEdit(m) && <Button variant="danger" onClick={() => remove(m)}>Remove</Button> },
          ]}
        />
      </Card>

      {canInvite && (
        <Card title="Invite someone" description="We email them a link to join. It works once and expires after 7 days.">
          <form onSubmit={invite} className="flex max-w-3xl flex-wrap items-end gap-3">
            <div className="min-w-0 flex-1 basis-64">
              <Input label="Email" type="email" placeholder="name@yourcompany-com.example" value={email} onChange={(e) => setEmail(e.target.value)} required />
            </div>
            <label className="block">
              <span className="mb-1.5 block text-[13px] font-medium">Role</span>
              <select className={field} value={inviteRole} onChange={(e) => setRole(e.target.value)}>
                {assignable.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
              </select>
            </label>
            {brands.length > 0 && chosen?.brand_limitable && (
              <div className="block">
                <span className="mb-1.5 block text-[13px] font-medium">Brands</span>
                <BrandsPicker label="Brands they work on" value={inviteBrands} brands={brands} onChange={setInviteBrands} />
              </div>
            )}
            <Button>Send invite</Button>
          </form>
          {chosen && (
            <p className="mt-2 text-[12px] text-muted-foreground">
              <span className="font-medium text-foreground">{chosen.name}</span>: {chosen.description}
              {brands.length > 0 && (chosen.brand_limitable ? " You can keep them to some brands: they see those brands' work and anything shared, and change only their brands'." : " This role covers the whole workspace, so it works on every brand.")}
            </p>
          )}
          {link && (
            <div className="mt-4 rounded-lg border border-green-500/30 bg-green-500/[0.06] p-3">
              <p className="mb-2"><span aria-hidden="true" className="mr-2 inline-block size-1.5 rounded-full bg-green-500 align-middle" />{link.emailed ? <>We emailed an invite to <span className="font-mono">{link.email}</span>. You can also send them this link yourself. It's shown only once.</> : <>This server can't send email yet, so send this link to <span className="font-mono">{link.email}</span> yourself. It's shown only once.</>}</p>
              <div className="flex flex-wrap items-center gap-2">
                <code className="min-w-0 flex-1 break-all px-2 py-1.5">{link.url}</code>
                <CopyButton text={link.url} />
              </div>
            </div>
          )}
          {invites.length > 0 && (
            <div className="mt-5">
              <h3 className="mb-2 font-mono text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Waiting to join ({invites.length})</h3>
              <ul className="divide-y">
                {invites.map((i) => (
                  <li key={i.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
                    <span className="flex items-center gap-2"><span className="font-mono text-[12px]">{i.email}</span> <Badge>{i.role_name || find(roleKey(i))?.name || i.role}</Badge>{brands.length > 0 && <span className="text-[12px] text-muted-foreground">{brandsLabel(i.brand_ids, brands)}</span>}</span>
                    <Button variant="danger" onClick={() => revoke(i)}>Cancel invite</Button>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </Card>
      )}

      <Card
        title="Roles"
        description="What each role can do. Change any role but Owner, or add your own."
        action={can("roles.manage") && <Button variant="ghost" onClick={() => setEditing("new")}>New role</Button>}
      >
        <ul className="divide-y">
          {roles.map((r) => (
            <li key={r.id} className="flex flex-wrap items-center justify-between gap-3 py-2.5">
              <div className="min-w-0 flex-1 basis-64">
                <div className="flex flex-wrap items-center gap-2 font-medium">
                  {r.name}
                  {r.locked && <Badge>Fixed</Badge>}
                  {r.edited && <Badge>Changed</Badge>}
                  {!r.builtin && <Badge tone="blue">Custom</Badge>}
                </div>
                <div className="text-[12px] text-muted-foreground">{r.description}</div>
              </div>
              <span className="text-[12px] text-muted-foreground tabular-nums">{r.members} {r.members === 1 ? "person" : "people"}{r.invites > 0 && `, ${r.invites} invited`}</span>
              {can("roles.manage") && !r.locked && within(r.scopes, myScopes) ? (
                <Button variant="ghost" onClick={() => setEditing(r)}>Edit</Button>
              ) : (
                <Button variant="ghost" onClick={() => setEditing(r)}>View</Button>
              )}
            </li>
          ))}
        </ul>
      </Card>

      {editing && (
        <RoleSheet
          ws={ws}
          role={editing === "new" ? null : editing}
          roles={roles}
          myScopes={myScopes}
          readOnly={editing !== "new" && (!can("roles.manage") || editing.locked || !within(editing.scopes, myScopes))}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            load();
          }}
        />
      )}
    </div>
  );
}

function RoleSheet({ ws, role, roles, myScopes, readOnly, onClose, onSaved }: { ws: string; role: WorkspaceRole | null; roles: WorkspaceRole[]; myScopes: string[]; readOnly: boolean; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(role?.name ?? "");
  const [description, setDescription] = useState(role?.description ?? "");
  const [scopes, setScopes] = useState<string[]>(role?.scopes ?? [baseScope, "members.read"]);
  const [moveTo, setMoveTo] = useState("viewer");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const mine = (s: string) => myScopes.includes(s);
  const inUse = (role?.members ?? 0) + (role?.invites ?? 0) > 0;
  const lost = role ? role.scopes.filter((s) => !scopes.includes(s)) : [];

  function copyFrom(id: string) {
    const r = roles.find((x) => x.id === id);
    if (r) setScopes(r.scopes.filter(mine));
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const body = { name, description, scopes: [...new Set([baseScope, ...scopes])] as Scope[] };
    const { error } = role
      ? await api.PATCH("/v1/workspaces/{ws}/roles/{role}", { params: { path: { ws, role: role.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/roles", { params: { path: { ws } }, body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(role ? `Saved ${name}.` : `Added the ${name} role.`);
    onSaved();
  }

  async function del() {
    if (!role) return;
    const reset = !!role.builtin;
    const ok = await confirmAction(
      reset ? `Reset ${role.name} to its defaults?` : `Delete the ${role.name} role?`,
      <p>{reset ? "Everyone on it gets the default permissions straight away." : inUse ? `Its ${role.members} ${role.members === 1 ? "person moves" : "people move"} to ${roles.find((r) => r.id === moveTo)?.name}, with pending invites.` : "Nobody uses it."}</p>,
      reset ? "Reset" : "Delete",
    );
    if (!ok) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/roles/{role}", { params: { path: { ws, role: role.id }, query: inUse && !reset ? { move_to: moveTo } : {} } });
    if (error) return setError(errorMessage(error));
    toast(reset ? `Reset ${role.name}.` : `Deleted ${role.name}.`);
    onSaved();
  }

  const extra = otherScopes(role?.scopes ?? []).concat(otherScopes(myScopes)).filter((s, i, a) => a.indexOf(s) === i);

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{role ? (readOnly ? role.name : `Edit ${role.name}`) : "New role"}</SheetTitle>
          <SheetDescription>
            {readOnly
              ? role?.locked
                ? "Owners can always do everything, so this role can't be changed."
                : "You can see what this role allows. Changing it needs the Edit roles permission and everything it allows."
              : "Changes apply to everyone on this role straight away. You can only give permissions you have yourself."}
          </SheetDescription>
        </SheetHeader>
        <form onSubmit={save} className="space-y-5 px-4 pb-6">
          <ErrorText>{error}</ErrorText>
          {!readOnly && (
            <div className="grid gap-3">
              <Input label="Name" value={name} maxLength={60} onChange={(e) => setName(e.target.value)} required placeholder="Content writer" />
              <Input label="What they do" value={description} maxLength={300} onChange={(e) => setDescription(e.target.value)} placeholder="Writes SEO pages and social posts" />
              {!role && (
                <label className="block">
                  <span className="mb-1.5 block text-[13px] font-medium">Start from</span>
                  <select className={field} defaultValue="" onChange={(e) => copyFrom(e.target.value)}>
                    <option value="">Nothing</option>
                    {roles.filter((r) => !r.locked).map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
                  </select>
                </label>
              )}
            </div>
          )}
          {roleGroups.map((g) => (
            <section key={g.label} className="rounded-lg border">
              <h3 className="border-b bg-muted/40 px-3 py-2 font-mono text-[11px] font-medium tracking-wide text-muted-foreground uppercase">{g.label}</h3>
              <ul className="divide-y">
                {g.areas.map((a) => {
                  const level = roleLevel(scopes, a);
                  return (
                    <li key={a.manage} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
                      <div className="min-w-0 flex-1 basis-48">
                        <div className="text-[13px] font-medium">{a.label}</div>
                        <div className="text-[12px] text-muted-foreground">{a.help}</div>
                      </div>
                      <div role="radiogroup" aria-label={a.label} className="inline-flex rounded-md bg-muted p-0.5">
                        {(["none", "view", "edit"] as RoleLevel[]).map((l) => {
                          const allowed = l === "none" || mine(l === "view" ? a.read : a.manage);
                          return (
                            <button
                              key={l}
                              type="button"
                              role="radio"
                              aria-checked={level === l}
                              disabled={readOnly || !allowed}
                              title={allowed ? undefined : "You can't give what you don't have"}
                              onClick={() => setScopes(setRoleLevel(scopes, a, l))}
                              className={`rounded px-2.5 py-1 text-[12px] capitalize disabled:opacity-50 ${level === l ? "bg-background font-medium shadow-xs" : "text-muted-foreground"}`}
                            >
                              {l}
                            </button>
                          );
                        })}
                      </div>
                    </li>
                  );
                })}
                {g.toggles.map((t) => (
                  <li key={t.scope} className="flex items-center justify-between gap-3 px-3 py-2">
                    <div className="min-w-0">
                      <div className="text-[13px] font-medium">{t.label}</div>
                      <div className="text-[12px] text-muted-foreground">{t.help}</div>
                    </div>
                    <Switch
                      aria-label={t.label}
                      checked={scopes.includes(t.scope)}
                      disabled={readOnly || !mine(t.scope)}
                      onCheckedChange={(on) => setScopes(on ? [...scopes, t.scope] : scopes.filter((s) => s !== t.scope))}
                    />
                  </li>
                ))}
              </ul>
            </section>
          ))}
          {extra.length > 0 && (
            <section className="rounded-lg border">
              <h3 className="border-b bg-muted/40 px-3 py-2 font-mono text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Other</h3>
              <ul className="divide-y">
                {extra.map((s) => (
                  <li key={s} className="flex items-center justify-between gap-3 px-3 py-2">
                    <span className="font-mono text-[12px]">{s}</span>
                    <Switch aria-label={s} checked={scopes.includes(s)} disabled={readOnly || !mine(s)} onCheckedChange={(on) => setScopes(on ? [...scopes, s] : scopes.filter((x) => x !== s))} />
                  </li>
                ))}
              </ul>
            </section>
          )}
          {!readOnly && (
            <>
              {role && lost.length > 0 && role.members > 0 && (
                <p className="text-[12px] text-muted-foreground">
                  {role.members} {role.members === 1 ? "person" : "people"} on this role lose {lost.length} {lost.length === 1 ? "permission" : "permissions"} when you save, and API tokens they made lose them too.
                </p>
              )}
              <div className="flex flex-wrap items-center gap-2">
                <Button disabled={busy}>{role ? "Save" : "Add role"}</Button>
                <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
                {role && (role.builtin ? role.edited : true) && (
                  <span className="ml-auto flex flex-wrap items-center gap-2">
                    {!role.builtin && inUse && (
                      <select aria-label="Move its people to" className={field} value={moveTo} onChange={(e) => setMoveTo(e.target.value)}>
                        {roles.filter((r) => r.id !== role.id && !r.locked).map((r) => <option key={r.id} value={r.id}>Move people to {r.name}</option>)}
                      </select>
                    )}
                    <Button type="button" variant="danger" onClick={del}>{role.builtin ? "Reset to default" : "Delete role"}</Button>
                  </span>
                )}
              </div>
            </>
          )}
        </form>
      </SheetContent>
    </Sheet>
  );
}
