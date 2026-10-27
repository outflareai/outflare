import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Agency as AgencyT, AgencyClient, AgencyToken, Member, Scope, Workspace } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Input, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { DataTable } from "@/components/data-table";
import { ago, stamp } from "../format";
import { CopyButton, groups, presets, Snippet } from "./Tokens";

type Props = {
  agencies: AgencyT[];
  ws?: Workspace;
  myId: string;
  onReload: () => void;
  openClient: (id: string) => void;
};

// The agency page: one place to run every client. With no agency yet it
// explains what an agency is and creates one.
export function Agency({ agencies, ws, myId, onReload, openClient }: Props) {
  const agency = agencies.find((a) => a.id === ws?.agency_id) ?? agencies[0];
  if (!agency) return <CreateAgency ws={ws} onDone={onReload} />;
  return <AgencyHome key={agency.id} agency={agency} myId={myId} onReload={onReload} openClient={openClient} />;
}

function CreateAgency({ ws, onDone }: { ws?: Workspace; onDone: () => void }) {
  const [name, setName] = useState("");
  const [include, setInclude] = useState(ws?.role === "owner");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { error } = await api.POST("/v1/agencies", { body: { name, workspace_ids: include && ws ? [ws.id] : [] } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`Created ${name}.`);
    onDone();
  }

  return (
    <div className="space-y-6">
      <PageHeader title="Agency" description="Run outbound for several clients from one place. Each client gets its own workspace, with its own mailboxes, leads, sequences and inbox, so nothing mixes between clients." />
      <div className="grid gap-4 lg:grid-cols-3">
        {[
          ["One login for every client", "Your team gets its agency role in every client workspace. Invite client staff to just their own workspace."],
          ["One agent for every client", "An agency token works in every client, so one AI agent can run them all over the API or MCP."],
          ["Your brand, not ours", "Client staff see your agency's name, logo and colour instead of Outflare."],
        ].map(([t, d]) => (
          <div key={t} className="rounded-lg border p-4">
            <div className="font-medium">{t}</div>
            <p className="mt-1 text-[13px] text-muted-foreground">{d}</p>
          </div>
        ))}
      </div>
      <Card title="Create your agency">
        <form onSubmit={create} className="max-w-lg space-y-4">
          <Input label="Agency name" placeholder="e.g. Northwind Growth" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
          {ws?.role === "owner" && (
            <label className="flex items-start gap-2 text-[13px]">
              <input type="checkbox" className="mt-0.5" checked={include} onChange={(e) => setInclude(e.target.checked)} />
              <span>Make <b>{ws.name}</b> its first client. Its members keep their access.</span>
            </label>
          )}
          <ErrorText>{error}</ErrorText>
          <Button disabled={busy || !name.trim()}>{busy ? "Creating…" : "Create agency"}</Button>
        </form>
      </Card>
    </div>
  );
}

type Section = "clients" | "people" | "tokens" | "brand" | "log";

function AgencyHome({ agency, myId, onReload, openClient }: { agency: AgencyT; myId: string; onReload: () => void; openClient: (id: string) => void }) {
  const isAdmin = agency.role === "owner" || agency.role === "admin";
  const [section, setSection] = useState<Section>("clients");
  const path = { ag: agency.id };
  return (
    <div className="space-y-6">
      <PageHeader title={agency.name} description={<>Every client in one place. Your team has the <b>{agency.role}</b> role in each client workspace.</>} />
      <Tabs value={section} onValueChange={(v) => setSection(v as Section)}>
        <TabsList className="h-8">
          <TabsTrigger value="clients" className="px-3 text-[12px]">Clients</TabsTrigger>
          <TabsTrigger value="people" className="px-3 text-[12px]">Team</TabsTrigger>
          {isAdmin && <TabsTrigger value="tokens" className="px-3 text-[12px]">Agent tokens</TabsTrigger>}
          {isAdmin && <TabsTrigger value="brand" className="px-3 text-[12px]">Branding</TabsTrigger>}
          {isAdmin && <TabsTrigger value="log" className="px-3 text-[12px]">Log</TabsTrigger>}
        </TabsList>
      </Tabs>
      {section === "clients" && <Clients path={path} isAdmin={isAdmin} onReload={onReload} openClient={openClient} />}
      {section === "people" && <Team path={path} role={agency.role ?? "viewer"} myId={myId} />}
      {section === "tokens" && isAdmin && <AgencyTokens agency={agency} role={agency.role ?? "viewer"} />}
      {section === "brand" && isAdmin && <Branding agency={agency} onReload={onReload} />}
      {section === "log" && isAdmin && <Log path={path} />}
    </div>
  );
}

const pct = (v: number | null | undefined) => (v === null || v === undefined ? "–" : `${v}%`);

function Clients({ path, isAdmin, onReload, openClient }: { path: { ag: string }; isAdmin: boolean; onReload: () => void; openClient: (id: string) => void }) {
  const [list, setList] = useState<AgencyClient[] | null>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/agencies/{ag}/clients", { params: { path } });
    setList(data?.data ?? []);
  }, [path.ag]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load();
  }, [load]);

  async function add(e: FormEvent) {
    e.preventDefault();
    const { data, error } = await api.POST("/v1/agencies/{ag}/clients", { params: { path }, body: { name } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setName("");
    toast(`Created ${data.name}. Connect its mailboxes next.`);
    onReload();
    load();
  }

  if (!list) return <Loading />;
  const sum = (k: keyof AgencyClient) => list.reduce((n, c) => n + (c[k] as number), 0);
  const sent = sum("sent_7d");
  return (
    <div className="space-y-6">
      <Stats cols={4}>
        <Stat label="Clients" value={list.length} />
        <Stat label="Sent, last 7 days" value={sent.toLocaleString()} />
        <Stat label="Replies, last 7 days" value={sum("replies_7d").toLocaleString()} sub={sent ? `from ${sent.toLocaleString()} emails` : undefined} />
        <Stat label="Replies waiting" value={sum("open_replies")} tone={sum("open_replies") > 0 ? "amber" : undefined} />
      </Stats>
      {list.length === 0 ? (
        <Empty title="No clients yet">Create a workspace for your first client below.</Empty>
      ) : (
        <DataTable
          data={list}
          rowId={(c) => c.id}
          columnsMenu={false}
          onRowClick={(c) => openClient(c.id)}
          columns={[
            { id: "name", header: "Client", accessorFn: (c) => c.name, cell: ({ row: { original: c } }) => <span className="font-medium">{c.name}</span> },
            {
              id: "mailboxes",
              header: "Mailboxes",
              accessorFn: (c) => c.mailboxes_active,
              cell: ({ row: { original: c } }) => (
                <span className="inline-flex items-center gap-1.5">
                  {c.mailboxes_active}
                  {c.mailboxes_paused > 0 && <Badge tone="amber" title="Paused mailboxes">{c.mailboxes_paused} paused</Badge>}
                </span>
              ),
            },
            { id: "sequences", header: "Live sequences", accessorFn: (c) => c.active_sequences },
            { id: "sent", header: "Sent 7d", accessorFn: (c) => c.sent_7d, meta: { mono: true } },
            { id: "reply", header: "Reply rate", accessorFn: (c) => c.reply_rate_7d ?? -1, meta: { mono: true }, cell: ({ row: { original: c } }) => pct(c.reply_rate_7d) },
            {
              id: "bounce",
              header: "Bounce rate",
              accessorFn: (c) => c.bounce_rate_7d ?? -1,
              meta: { mono: true },
              cell: ({ row: { original: c } }) => {
                const v = c.bounce_rate_7d;
                return v !== null && v >= 3 ? <Badge tone={v >= 5 ? "red" : "amber"} title="Above 3% hurts deliverability">{pct(v)}</Badge> : pct(v);
              },
            },
            { id: "open", header: "Replies waiting", accessorFn: (c) => c.open_replies, cell: ({ row: { original: c } }) => (c.open_replies > 0 ? <Badge tone="blue">{c.open_replies}</Badge> : <span className="text-muted-foreground">0</span>) },
            { id: "go", header: () => <span className="sr-only">Open</span>, enableSorting: false, meta: { align: "right" }, cell: ({ row: { original: c } }) => <Button variant="ghost" onClick={(e) => { e.stopPropagation(); openClient(c.id); }}>Open</Button> },
          ]}
        />
      )}
      {isAdmin && (
        <Card title="Add a client" description="Creates a new workspace for the client. Your team gets in through the agency; invite the client's own staff from that workspace's Members page.">
          <form onSubmit={add} className="flex max-w-lg flex-wrap items-end gap-2">
            <div className="min-w-0 flex-1"><Input label="Client name" placeholder="e.g. Contoso" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} /></div>
            <Button disabled={!name.trim()}>Create client workspace</Button>
          </form>
          <ErrorText>{error}</ErrorText>
        </Card>
      )}
    </div>
  );
}

const roleHelp: Record<string, string> = {
  owner: "Everything, including branding, tokens and removing clients",
  admin: "Add clients and people, create agent tokens, change branding",
  operator: "Run outreach in every client: mailboxes, leads, sequences, inbox",
  viewer: "Read every client, change nothing",
};

function Team({ path, role, myId }: { path: { ag: string }; role: string; myId: string }) {
  const [list, setList] = useState<Member[] | null>(null);
  const [email, setEmail] = useState("");
  const [newRole, setNewRole] = useState("operator");
  const [error, setError] = useState("");
  const canManage = role === "owner" || role === "admin";

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/agencies/{ag}/members", { params: { path } });
    setList(data?.data ?? []);
  }, [path.ag]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load();
  }, [load]);

  async function add(e: FormEvent) {
    e.preventDefault();
    const { error } = await api.POST("/v1/agencies/{ag}/members", { params: { path }, body: { email, role: newRole as Member["role"] & string } });
    if (error) return setError(errorMessage(error));
    setError("");
    setEmail("");
    toast(`Added ${email}.`);
    load();
  }

  async function change(m: Member, r: string) {
    const { error } = await api.PATCH("/v1/agencies/{ag}/members/{user}", { params: { path: { ...path, user: m.user_id! } }, body: { role: r as Member["role"] & string } });
    if (error) return toast(errorMessage(error), "error");
    toast(`${m.name || m.email} is now ${r}.`);
    load();
  }

  async function remove(m: Member) {
    if (!(await confirmAction(`Remove ${m.name || m.email} from the agency?`, <p>They lose access to every client workspace they aren't a direct member of.</p>, "Remove"))) return;
    const { error } = await api.DELETE("/v1/agencies/{ag}/members/{user}", { params: { path: { ...path, user: m.user_id! } } });
    if (error) return toast(errorMessage(error), "error");
    load();
  }

  if (!list) return <Loading />;
  const allowed = Object.keys(roleHelp).filter((r) => role === "owner" || r !== "owner");
  return (
    <div className="space-y-6">
      <DataTable
        data={list}
        rowId={(m) => m.user_id!}
        columnsMenu={false}
        columns={[
          { id: "name", header: "Name", accessorFn: (m) => m.name || m.email, cell: ({ row: { original: m } }) => <span><span className="font-medium">{m.name}</span>{m.user_id === myId && <span className="text-muted-foreground"> (you)</span>}<span className="block text-[12px] text-muted-foreground">{m.email}</span></span> },
          {
            id: "role",
            header: "Role in every client",
            accessorFn: (m) => m.role,
            cell: ({ row: { original: m } }) =>
              canManage && (role === "owner" || m.role !== "owner") ? (
                <select aria-label={`Role for ${m.email}`} className={field} value={m.role} onChange={(e) => change(m, e.target.value)}>
                  {allowed.map((r) => <option key={r} value={r}>{r}</option>)}
                </select>
              ) : (
                <span className="font-mono text-[12px]">{m.role}</span>
              ),
          },
          { id: "added", header: "Added", accessorFn: (m) => m.created_at, cell: ({ row: { original: m } }) => <span className="text-muted-foreground">{ago(m.created_at)}</span> },
          { id: "actions", header: () => <span className="sr-only">Actions</span>, enableSorting: false, meta: { align: "right" }, cell: ({ row: { original: m } }) => (canManage || m.user_id === myId) && <Button variant="danger" onClick={() => remove(m)}>{m.user_id === myId ? "Leave" : "Remove"}</Button> },
        ]}
      />
      {canManage && (
        <Card title="Add someone to the team" description="They need an Outflare account already. If they don't have one, invite them to any workspace first.">
          <form onSubmit={add} className="space-y-3">
            <div className="flex max-w-xl flex-wrap items-end gap-2">
              <div className="min-w-0 flex-1"><Input label="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required /></div>
              <select aria-label="Role" className={field} value={newRole} onChange={(e) => setNewRole(e.target.value)}>
                {allowed.map((r) => <option key={r} value={r}>{r}</option>)}
              </select>
              <Button disabled={!email.trim()}>Add</Button>
            </div>
            <p className="text-[12px] text-muted-foreground">{roleHelp[newRole]}.</p>
            <ErrorText>{error}</ErrorText>
          </form>
        </Card>
      )}
    </div>
  );
}

const roleScopes: Record<string, Scope[]> = {
  owner: groups.flatMap((g) => g.scopes.map(([s]) => s)),
  admin: groups.flatMap((g) => g.scopes.map(([s]) => s)),
  operator: ["workspace.read", "members.read", "mailboxes.read", "mailboxes.manage", "leads.read", "leads.manage", "sequences.read", "sequences.manage", "inbox.read", "inbox.manage"],
  viewer: ["workspace.read", "members.read", "mailboxes.read", "leads.read", "sequences.read", "inbox.read"],
};

function AgencyTokens({ agency, role }: { agency: AgencyT; role: string }) {
  const path = { ag: agency.id };
  const [list, setList] = useState<AgencyToken[] | null>(null);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<Scope[]>(presets[1].scopes);
  const [canCreate, setCanCreate] = useState(false);
  const [secret, setSecret] = useState<{ name: string; token: string } | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/agencies/{ag}/api-tokens", { params: { path } });
    setList(data?.data ?? []);
  }, [agency.id]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load();
  }, [load]);

  async function create(e: FormEvent) {
    e.preventDefault();
    const { data, error } = await api.POST("/v1/agencies/{ag}/api-tokens", { params: { path }, body: { name, scopes, can_create_clients: canCreate } });
    if (error) return setError(errorMessage(error));
    setError("");
    setSecret({ name, token: data?.token ?? "" });
    setName("");
    load();
  }

  async function revoke(t: AgencyToken) {
    if (!(await confirmAction(`Revoke "${t.name}"?`, <p>Agents using it stop working in every client straight away. This can't be undone.</p>, "Revoke token"))) return;
    await api.DELETE("/v1/agencies/{ag}/api-tokens/{id}", { params: { path: { ...path, id: t.id! } } });
    toast(`Revoked "${t.name}".`);
    load();
  }

  const mine = roleScopes[role] ?? [];
  const toggle = (s: Scope) => setScopes(scopes.includes(s) ? scopes.filter((x) => x !== s) : [...scopes, s]);
  return (
    <div className="space-y-6">
      <p className="max-w-3xl text-[13px] text-muted-foreground">An agent token works in every client workspace, with the same permissions in each, so one AI agent can run all your clients. Everything it does shows in each client's audit log under the token's name.</p>
      {secret && (
        <section className="space-y-3 rounded-lg border border-green-500/30 bg-green-500/[0.06] p-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="font-medium">"{secret.name}" is ready. Copy the token now: it won't be shown again.</p>
            <Button variant="ghost" onClick={() => setSecret(null)}>Done</Button>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <code className="min-w-0 flex-1 break-all px-2 py-1.5 text-[13px]">{secret.token}</code>
            <CopyButton text={secret.token} />
          </div>
          <Snippet title="claude code (MCP)" code={`claude mcp add outflare-agency \\\n  -e OUTFLARE_URL=${location.origin} \\\n  -e OUTFLARE_TOKEN=${secret.token} \\\n  -e OUTFLARE_AGENCY=${agency.id} \\\n  -- node mcp/dist/index.js`} />
        </section>
      )}
      <Card title="Create an agent token">
        <form onSubmit={create} className="space-y-4">
          <Input label="Name" placeholder="e.g. Claude agency agent" value={name} onChange={(e) => setName(e.target.value)} required />
          <div className="grid gap-2 sm:grid-cols-2">
            {presets.map((p) => (
              <button key={p.label} type="button" onClick={() => setScopes(p.scopes.filter((s) => mine.includes(s)))} className={`rounded-lg border px-3 py-2.5 text-left ${p.scopes.every((s) => scopes.includes(s)) && scopes.length === p.scopes.length ? "border-brand/60 bg-brand/[0.07] ring-1 ring-brand/40" : "border-border hover:border-ring/60"}`}>
                <span className="font-medium">{p.label}</span>
                <span className="mt-0.5 block text-[12px] text-muted-foreground">{p.help}, in every client</span>
              </button>
            ))}
          </div>
          <details className="rounded-lg border">
            <summary className="cursor-pointer px-3 py-2 text-muted-foreground">Choose permissions one by one ({scopes.length} selected)</summary>
            <div className="grid gap-4 border-t p-3 sm:grid-cols-3">
              {groups.map((g) => (
                <fieldset key={g.area}>
                  <legend className="mb-1 font-medium">{g.area}</legend>
                  {g.scopes.filter(([s]) => mine.includes(s)).map(([s, help]) => (
                    <label key={s} className="flex items-start gap-2 py-1">
                      <input type="checkbox" className="mt-0.5" checked={scopes.includes(s)} onChange={() => toggle(s)} />
                      <span><span className="block font-mono text-[12px]">{s}</span><span className="block text-[12px] text-muted-foreground">{help}</span></span>
                    </label>
                  ))}
                </fieldset>
              ))}
            </div>
          </details>
          <label className="flex items-start gap-2 text-[13px]">
            <input type="checkbox" className="mt-0.5" checked={canCreate} onChange={(e) => setCanCreate(e.target.checked)} />
            <span>Can create new client workspaces <span className="block text-[12px] text-muted-foreground">Lets the agent onboard a new client on its own.</span></span>
          </label>
          <ErrorText>{error}</ErrorText>
          <Button disabled={!name.trim() || scopes.length === 0}>Create token</Button>
        </form>
      </Card>
      {list && list.length > 0 && (
        <DataTable
          data={list}
          rowId={(t) => t.id!}
          columnsMenu={false}
          columns={[
            { id: "name", header: "Name", accessorFn: (t) => t.name, cell: ({ row: { original: t } }) => <span className="font-medium">{t.name}</span> },
            { id: "prefix", header: "Token", accessorFn: (t) => t.prefix, cell: ({ row: { original: t } }) => <span className="font-mono text-muted-foreground">{t.prefix}…</span> },
            { id: "scopes", header: "Permissions", accessorFn: (t) => t.scopes?.length ?? 0, cell: ({ row: { original: t } }) => <span className="text-muted-foreground">{t.scopes?.length} scopes{t.can_create_clients ? ", creates clients" : ""}</span> },
            { id: "used", header: "Last used", accessorFn: (t) => t.last_used_at ?? "", cell: ({ row: { original: t } }) => <span className="text-muted-foreground" title={t.last_used_at ? stamp(t.last_used_at) : undefined}>{t.last_used_at ? ago(t.last_used_at) : "Never"}</span> },
            { id: "actions", header: () => <span className="sr-only">Actions</span>, enableSorting: false, meta: { align: "right" }, cell: ({ row: { original: t } }) => <Button variant="danger" onClick={() => revoke(t)}>Revoke</Button> },
          ]}
        />
      )}
    </div>
  );
}

function Branding({ agency, onReload }: { agency: AgencyT; onReload: () => void }) {
  const [v, setV] = useState({ name: agency.name, brand_name: agency.brand_name, logo_url: agency.logo_url, accent_color: agency.accent_color, support_email: agency.support_email });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { error } = await api.PATCH("/v1/agencies/{ag}", { params: { path: { ag: agency.id } }, body: v });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setError("");
    toast("Saved. Client workspaces show the new branding.");
    onReload();
  }

  const shown = v.brand_name || "Outflare";
  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_18rem]">
      <Card title="White label" description="What people in your client workspaces see instead of Outflare. Leave a field empty to keep the default.">
        <form onSubmit={save} className="max-w-lg space-y-4">
          <Input label="Agency name" value={v.name} onChange={(e) => setV({ ...v, name: e.target.value })} required maxLength={100} hint="Your team sees this." />
          <Input label="Brand name" value={v.brand_name} onChange={(e) => setV({ ...v, brand_name: e.target.value })} maxLength={60} placeholder="e.g. Northwind" hint="Shown in place of Outflare in client workspaces." />
          <Input label="Logo URL" value={v.logo_url} onChange={(e) => setV({ ...v, logo_url: e.target.value })} placeholder="https://…/logo.png" hint="A square image on https, shown at 24 pixels." />
          <div className="flex items-end gap-2">
            <div className="min-w-0 flex-1"><Input label="Accent colour" value={v.accent_color} onChange={(e) => setV({ ...v, accent_color: e.target.value })} placeholder="#2563eb" /></div>
            <input type="color" aria-label="Pick accent colour" className="h-8 w-10 cursor-pointer rounded border bg-transparent" value={v.accent_color || "#22c55e"} onChange={(e) => setV({ ...v, accent_color: e.target.value })} />
          </div>
          <Input label="Support email" type="email" value={v.support_email} onChange={(e) => setV({ ...v, support_email: e.target.value })} placeholder="help@youragency-com.example" hint="Where client staff go for help." />
          <ErrorText>{error}</ErrorText>
          <Button disabled={busy}>{busy ? "Saving…" : "Save branding"}</Button>
        </form>
      </Card>
      <div className="space-y-2">
        <div className="text-[12px] font-medium text-muted-foreground">Preview</div>
        <div className="flex items-center gap-2 rounded-lg border p-3 font-semibold">
          {v.logo_url ? <img src={v.logo_url} alt="" className="size-6 rounded-md object-cover" /> : <span className="flex size-6 items-center justify-center rounded-md font-mono text-[11px] text-white" style={{ background: v.accent_color || "#171717" }}>{shown[0]}</span>}
          {shown}
        </div>
      </div>
    </div>
  );
}

function Log({ path }: { path: { ag: string } }) {
  type Ev = { id?: string; actor?: string | null; action?: string; target?: string; created_at?: string };
  const [list, setList] = useState<Ev[] | null>(null);
  useEffect(() => {
    api.GET("/v1/agencies/{ag}/events", { params: { path } }).then(({ data }) => setList(data?.data ?? []));
  }, [path.ag]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!list) return <Loading />;
  const label: Record<string, string> = {
    "agency.created": "Created the agency",
    "agency.updated": "Changed the agency or its branding",
    "agency.client_added": "Added a client",
    "agency.client_removed": "Removed a client",
    "agency.member_added": "Added someone to the team",
    "agency.member_updated": "Changed someone's role",
    "agency.member_removed": "Removed someone from the team",
    "agency.token_created": "Created an agent token",
    "agency.token_revoked": "Revoked an agent token",
  };
  if (list.length === 0) return <Empty title="Nothing yet" />;
  return (
    <ul className="divide-y rounded-lg border text-[13px]">
      {list.map((e) => (
        <li key={e.id} className="flex flex-wrap items-baseline justify-between gap-2 px-3 py-2">
          <span><span className="font-medium">{e.actor ?? "Someone"}</span> {(label[e.action ?? ""] ?? e.action)?.toLowerCase()}</span>
          <span className="text-[12px] text-muted-foreground" title={e.created_at ? stamp(e.created_at) : undefined}>{ago(e.created_at)}</span>
        </li>
      ))}
    </ul>
  );
}
