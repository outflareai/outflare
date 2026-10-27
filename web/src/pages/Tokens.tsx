import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { APIToken, Scope, Team } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, confirmAction, CopyButton as IconCopy, Empty, ErrorText, field, Input, Loading, PageHeader, toast } from "../ui";
import { ago, stamp } from "../format";
import { DataTable } from "@/components/data-table";
import { useBrand } from "@/lib/brand";
import { areas, canChange, changes, extras, levelOf, readOnly, summary, withLevel, type Level } from "@/lib/permissions";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { Tab } from "../App";

// Scopes by area, each with what it allows in plain words. Agency tokens
// still pick from these; workspace tokens use @/lib/permissions.
export const groups: { area: string; scopes: [Scope, string][] }[] = [
  { area: "Outreach", scopes: [["leads.read", "Read leads, lists and the suppression list"], ["leads.manage", "Import and edit leads, add suppressions"], ["sequences.read", "Read sequences, enrollments and sent emails"], ["sequences.manage", "Create, start and pause sequences, add leads"], ["inbox.read", "Read replies"], ["inbox.manage", "Reply to leads, mark read"]] },
  { area: "Missions", scopes: [["missions.read", "Read missions, their calls and held changes"], ["missions.run", "Run missions: plan, act inside them, report, pause or stop"], ["missions.approve", "Approve missions and their held changes (never for the agent itself)"]] },
  { area: "Customers", scopes: [["customers.read", "Read people, companies and other records, their timelines and consents, product events, and the catalog of their fields"], ["customers.manage", "Add and change records, their identities and links, send product events and record consent"], ["catalog.manage", "Add and change record types, fields, option lists and event definitions"]] },
  { area: "SEO", scopes: [["seo.read", "See SEO sites, crawls and the pages they found"], ["seo.manage", "Add and change SEO sites, check ownership and start or stop crawls"]] },
  { area: "Social", scopes: [["social.read", "See social watches, the posts they found and replies with their results"], ["social.manage", "Add and change watches, ignore posts, draft, edit and reject replies (only a person approves one)"]] },
  { area: "Webhooks", scopes: [["webhooks.read", "See webhook endpoints and their deliveries"], ["webhooks.manage", "Add, change and remove webhook endpoints, and see their signing secrets"]] },
  { area: "Lifecycle email", scopes: [["emails.read", "See emails sent through your email providers and their delivery"], ["emails.send", "Send transactional and lifecycle email, such as receipts and password resets"], ["emails.manage", "Manage email providers, sending domains and templates"]] },
  { area: "Mailboxes", scopes: [["mailboxes.read", "Read mailboxes and domain health"], ["mailboxes.manage", "Connect, pause and remove mailboxes"]] },
  { area: "Workspace", scopes: [["workspace.read", "Read the overview numbers"], ["workspace.manage", "Rename the workspace and change its URL"], ["context.manage", "Edit the company context and save memories"], ["members.read", "List members"], ["members.manage", "Invite and remove members"], ["suppressions.manage", "Remove entries from the suppression list"], ["tokens.manage", "Create and revoke API tokens"], ["audit.read", "Read the audit log"]] },
];

export const presets: { label: string; help: string; scopes: Scope[] }[] = [
  { label: "Reporting", help: "Reads everything, changes nothing", scopes: ["workspace.read", "mailboxes.read", "leads.read", "sequences.read", "inbox.read"] },
  { label: "Outreach agent", help: "What the MCP server needs to run outreach", scopes: ["workspace.read", "context.manage", "mailboxes.read", "leads.read", "leads.manage", "sequences.read", "sequences.manage", "inbox.read", "inbox.manage"] },
  { label: "Mission agent", help: "Runs missions: outreach plus planning and reporting, never approving", scopes: ["workspace.read", "context.manage", "mailboxes.read", "leads.read", "leads.manage", "sequences.read", "sequences.manage", "inbox.read", "inbox.manage", "missions.read", "missions.run"] },
];

/** A labelled Copy button, for secrets shown once. */
export function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button type="button" variant="ghost" onClick={async () => { await navigator.clipboard.writeText(text); setCopied(true); setTimeout(() => setCopied(false), 2000); }}>
      {copied ? "Copied" : "Copy"}
    </Button>
  );
}

/** A dark terminal-style block with a copy button. */
export function Snippet({ title, code }: { title: string; code: string }) {
  return (
    <div className="overflow-hidden rounded-lg border bg-muted/40">
      <div className="flex items-center justify-between border-b py-1 pr-1 pl-3">
        <span className="font-mono text-[11px] text-muted-foreground">{title}</span>
        <IconCopy text={code} label={`Copy ${title}`} />
      </div>
      <pre className="overflow-x-auto p-3 text-[12px] leading-5"><code>{code}</code></pre>
    </div>
  );
}

type Access = "read" | "write" | "custom";
const accessOf = (scopes: Scope[]): Access => (same(scopes, readOnly) ? "read" : same(scopes, canChange) ? "write" : "custom");
const same = (a: readonly string[], b: readonly string[]) => a.length === b.length && a.every((x) => b.includes(x));
const expiries = [{ days: 30, label: "30 days" }, { days: 90, label: "90 days" }, { days: 365, label: "1 year" }, { days: 0, label: "Never" }];

function expiresLabel(t: APIToken) {
  if (!t.expires_at) return "Never";
  const ms = new Date(t.expires_at).getTime() - Date.now();
  if (ms <= 0) return "Expired";
  const days = Math.ceil(ms / 86400000);
  return days <= 1 ? "Tomorrow" : days < 60 ? `In ${days} days` : stamp(t.expires_at).split(",")[0];
}

// expiresPhrase is expiresLabel in a sentence: "expires in 30 days".
function expiresPhrase(t: APIToken) {
  const l = expiresLabel(t);
  if (l === "Expired") return "expired";
  if (l === "Never") return "never expires";
  return l === "Tomorrow" || l.startsWith("In ") ? `expires ${l.toLowerCase()}` : `expires ${l}`;
}

export function Tokens({ ws, open, setOpen, go }: { ws: string; open?: string; setOpen: (id: string | undefined) => void; go: (t: Tab) => void }) {
  const [tokens, setTokens] = useState<APIToken[] | null>(null);
  const [teams, setTeams] = useState<Team[]>([]);
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<{ name: string; token: string } | null>(null);
  const { byId } = useBrand(ws);

  const load = useCallback(async () => {
    const [t, m] = await Promise.all([api.GET("/v1/workspaces/{ws}/api-tokens", { params: { path: { ws } } }), api.GET("/v1/workspaces/{ws}/teams", { params: { path: { ws } } })]);
    setTokens(t.data?.data ?? []);
    setTeams(m.data?.data ?? []);
  }, [ws]);

  useEffect(() => {
    load();
  }, [load]);

  const editing = tokens?.find((t) => t.id === open);

  return (
    <div className="space-y-6">
      <PageHeader
        title="API tokens"
        description="For scripts and integrations that call the Outflare API. Each token does only what you allow, and everything it does is in the audit log under its name."
        action={<Button onClick={() => setCreating(true)}>New token</Button>}
      />
      <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-muted/30 px-4 py-3 text-[13px]">
        <span className="min-w-0 flex-1">Connecting Claude, ChatGPT or Cursor? Use <span className="font-medium">Connect your AI</span> instead: the app signs in, so there's nothing to copy, and you change what it may do there.</span>
        <Button variant="ghost" onClick={() => go("ai")}>Connect your AI</Button>
      </div>
      {secret && (
        <section className="space-y-3 rounded-lg border border-green-500/30 bg-green-500/[0.06] p-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="font-medium"><span aria-hidden="true" className="mr-2 inline-block size-1.5 rounded-full bg-green-500 align-middle" />"{secret.name}" is ready. Copy the token now: it won't be shown again.</p>
            <Button variant="ghost" onClick={() => setSecret(null)}>Done</Button>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <code className="min-w-0 flex-1 break-all px-2 py-1.5 text-[13px]">{secret.token}</code>
            <CopyButton text={secret.token} />
          </div>
          <div className="grid gap-3 lg:grid-cols-2">
            <Snippet title="shell" code={`export OUTFLARE_URL=${location.origin}\nexport OUTFLARE_TOKEN=${secret.token}\n\ncurl "$OUTFLARE_URL/v1/workspaces/${ws}/stats" \\\n  -H "Authorization: Bearer $OUTFLARE_TOKEN"`} />
            <Snippet title="claude code (MCP)" code={`claude mcp add --transport http outflare ${location.origin}/mcp \\\n  --header "Authorization: Bearer ${secret.token}"`} />
          </div>
        </section>
      )}
      {tokens === null && <Loading />}
      {tokens?.length === 0 && !secret && <Empty title="No tokens yet" action={<Button onClick={() => setCreating(true)}>New token</Button>}>Create one for a script or an integration.</Empty>}
      {tokens && tokens.length > 0 && (
        <DataTable
          data={tokens}
          rowId={(t) => t.id!}
          columnsMenu={false}
          onRowClick={(t) => setOpen(t.id)}
          initialSort={[{ id: "created", desc: true }]}
          mobile={(t) => (
            <span className="block">
              <span className="block font-medium">{t.name}</span>
              <span className="block text-[12px] text-muted-foreground">{summary(t.scopes ?? [])} · last used {ago(t.last_used_at)} · {expiresPhrase(t)}</span>
            </span>
          )}
          columns={[
            {
              id: "name",
              header: "Name",
              accessorFn: (t) => t.name,
              cell: ({ row: { original: t } }) => (
                <span className="flex items-center gap-2">
                  <span className="font-medium">{t.name}</span>
                  <span className="font-mono text-[11px] text-muted-foreground">{t.prefix}…</span>
                  {byId(t.brand_id) && <Badge title={`Only ${byId(t.brand_id)!.name}`}>{byId(t.brand_id)!.name}</Badge>}
                </span>
              ),
            },
            {
              id: "access",
              header: "Can",
              accessorFn: (t) => summary(t.scopes ?? []),
              cell: ({ row: { original: t } }) => {
                const c = changes(t.scopes ?? []);
                return <span className="text-muted-foreground" title={c.length ? `Changes ${c.join(", ").toLowerCase()}` : undefined}>{summary(t.scopes ?? [])}</span>;
              },
            },
            { id: "used", header: "Last used", accessorFn: (t) => t.last_used_at ?? "", cell: ({ row: { original: t } }) => <span className="whitespace-nowrap text-muted-foreground" title={t.last_used_at ? stamp(t.last_used_at) : undefined}>{t.last_used_at ? ago(t.last_used_at) : "Never"}</span> },
            { id: "expires", header: "Expires", accessorFn: (t) => t.expires_at ?? "9", cell: ({ row: { original: t } }) => <span className={`whitespace-nowrap ${expiresLabel(t) === "Expired" ? "text-red-600 dark:text-red-400" : "text-muted-foreground"}`} title={t.expires_at ? stamp(t.expires_at) : undefined}>{expiresLabel(t)}</span> },
            { id: "created", header: "Created", accessorFn: (t) => t.created_at ?? "", cell: ({ row: { original: t } }) => <span className="whitespace-nowrap text-muted-foreground" title={t.created_at ? stamp(t.created_at) : undefined}>{ago(t.created_at)}</span> },
            { id: "open", header: () => <span className="sr-only">Edit</span>, enableSorting: false, meta: { align: "right" }, cell: ({ row: { original: t } }) => <Button variant="ghost" onClick={() => setOpen(t.id)}>Edit</Button> },
          ]}
        />
      )}
      {creating && (
        <TokenSheet
          ws={ws}
          teams={teams}
          onClose={() => setCreating(false)}
          onDone={(name, token) => {
            setCreating(false);
            setSecret({ name, token });
            load();
          }}
        />
      )}
      {editing && <TokenSheet ws={ws} teams={teams} token={editing} onClose={() => setOpen(undefined)} onDone={() => load()} onRevoked={() => { setOpen(undefined); load(); }} />}
    </div>
  );
}

// TokenSheet creates a token, or changes one: its name, what it may do and
// its sending limits. Brand and expiry are fixed once it exists.
function TokenSheet({ ws, teams, token: t, onClose, onDone, onRevoked }: { ws: string; teams: Team[]; token?: APIToken; onClose: () => void; onDone: (name: string, token: string) => void; onRevoked?: () => void }) {
  const [name, setName] = useState(t?.name ?? "");
  const [scopes, setScopes] = useState<Scope[]>(t?.scopes ?? canChange);
  const [access, setAccess] = useState<Access>(accessOf(t?.scopes ?? canChange));
  const [expires, setExpires] = useState(90);
  const [brand, setBrand] = useState("");
  const [budget, setBudget] = useState(t?.daily_send_budget ? String(t.daily_send_budget) : "");
  const [maxPriority, setMaxPriority] = useState<"" | "low" | "normal" | "high">(t ? (t.max_priority ?? "") : "normal");
  const [mayTake, setMayTake] = useState(t?.may_take_mailboxes ?? false);
  const [team, setTeam] = useState(t?.default_team_id ?? "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const { active: brands } = useBrand(ws);

  const pick = (a: Access) => {
    setAccess(a);
    if (a === "read") setScopes(readOnly);
    if (a === "write") setScopes(canChange);
  };
  const toggleExtra = (s: Scope) => setScopes(scopes.includes(s) ? scopes.filter((x) => x !== s) : [...scopes, s]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const limits = { daily_send_budget: budget ? Number(budget) : null, max_priority: maxPriority || null, may_take_mailboxes: mayTake, default_team_id: team || null };
    if (t) {
      const { error } = await api.PATCH("/v1/workspaces/{ws}/api-tokens/{id}", { params: { path: { ws, id: t.id! } }, body: { name: name.trim(), scopes, ...limits } });
      setBusy(false);
      if (error) return setError(errorMessage(error));
      toast(`Saved "${name.trim()}". It applies on the token's next request.`);
      onDone(name.trim(), "");
      return onClose();
    }
    const { data, error } = await api.POST("/v1/workspaces/{ws}/api-tokens", {
      params: { path: { ws } },
      body: { name: name.trim(), scopes, expires_in_days: expires || undefined, brand: brand || null, ...limits },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    onDone(name.trim(), data?.token ?? "");
  }

  async function revoke() {
    if (!t || !(await confirmAction(`Revoke "${t.name}"?`, <p>Anything using it (scripts, agents, integrations) stops working straight away. This can't be undone.</p>, "Revoke token"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/api-tokens/{id}", { params: { path: { ws, id: t.id! } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Revoked "${t.name}".`);
    onRevoked?.();
  }

  const choices: { id: Access; label: string; help: string }[] = [
    { id: "read", label: "Read only", help: "Sees everything, changes nothing." },
    { id: "write", label: "Can make changes", help: "Leads, sequences, replies, SEO and more. Never people, tokens or money." },
    { id: "custom", label: "Choose", help: "Pick area by area." },
  ];

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{t ? t.name : "New API token"}</SheetTitle>
          <SheetDescription>{t ? <>Created {ago(t.created_at)}, last used {ago(t.last_used_at)}, {expiresPhrase(t)}.</> : "You'll see the token once, right after you create it."}</SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-5 px-4 pb-6">
          <Input label="Name" placeholder="e.g. n8n, CRM sync" hint="Shows in the audit log next to everything it does." value={name} onChange={(e) => setName(e.target.value)} required autoFocus={!t} />

          <fieldset>
            <legend className="mb-2 block font-medium">What it can do</legend>
            <div className="grid gap-2 sm:grid-cols-3">
              {choices.map((c) => (
                <button key={c.id} type="button" onClick={() => pick(c.id)} aria-pressed={access === c.id} className={`rounded-lg border px-3 py-2.5 text-left transition-colors ${access === c.id ? "border-brand/60 bg-brand/[0.07] ring-1 ring-brand/40" : "border-border hover:border-ring/60"}`}>
                  <span className="block font-medium">{c.label}</span>
                  <span className="mt-0.5 block text-[12px] text-muted-foreground">{c.help}</span>
                </button>
              ))}
            </div>
            {access === "custom" && (
              <div className="mt-3 divide-y rounded-md border">
                {areas.map((a) => {
                  const level = levelOf(a, scopes);
                  const levels: Level[] = a.write.length ? ["none", "read", "write"] : ["none", "read"];
                  return (
                    <div key={a.id} className="flex flex-wrap items-center gap-2 px-3 py-2">
                      <span className="min-w-0 flex-1">
                        <span className="block text-[13px] font-medium">{a.label}</span>
                        <span className="block text-[12px] text-muted-foreground">{a.help}</span>
                      </span>
                      <span role="radiogroup" aria-label={a.label} className="inline-flex rounded-md border p-0.5">
                        {levels.map((l) => (
                          <button key={l} type="button" role="radio" aria-checked={level === l} onClick={() => setScopes(withLevel(scopes, a, l))} className={`rounded px-2 py-0.5 text-[12px] ${level === l ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground"}`}>
                            {l === "none" ? "No" : l === "read" ? "See" : "Change"}
                          </button>
                        ))}
                      </span>
                    </div>
                  );
                })}
              </div>
            )}
          </fieldset>

          <details className="rounded-lg border" open={extras.some((x) => scopes.includes(x.scope))}>
            <summary className="cursor-pointer px-3 py-2"><span className="font-medium">Also allow</span> <span className="text-muted-foreground">· off unless you need it</span></summary>
            <div className="space-y-1 border-t p-3">
              {extras.map((x) => (
                <label key={x.scope} className="flex items-start gap-2 py-1">
                  <input type="checkbox" className="mt-0.5" checked={scopes.includes(x.scope)} onChange={() => toggleExtra(x.scope)} />
                  <span><span className="block">{x.label}</span><span className="block text-[12px] text-muted-foreground">{x.help}</span></span>
                </label>
              ))}
            </div>
          </details>

          {!t && (
            <div className="grid gap-4 sm:grid-cols-2">
              <label className="block">
                <span className="mb-1.5 block font-medium">Expires</span>
                <select className={`${field} w-full`} value={expires} onChange={(e) => setExpires(Number(e.target.value))}>
                  {expiries.map((x) => <option key={x.days} value={x.days}>{x.label}</option>)}
                </select>
              </label>
              {brands.length > 0 && (
                <label className="block">
                  <span className="mb-1.5 block font-medium">Brands</span>
                  <select className={`${field} w-full`} value={brand} onChange={(e) => setBrand(e.target.value)}>
                    <option value="">Every brand</option>
                    {brands.map((b) => <option key={b.id} value={b.handle}>Only {b.name}</option>)}
                  </select>
                </label>
              )}
            </div>
          )}

          {scopes.includes("sequences.manage") && (
            <details className="rounded-lg border" open={!t || !!t.daily_send_budget}>
              <summary className="cursor-pointer px-3 py-2"><span className="font-medium">Sending limits</span> <span className="text-muted-foreground">· for sequences it starts</span></summary>
              <div className="grid gap-4 border-t p-3 sm:grid-cols-2">
                <Input label="Daily send budget" type="number" min={1} placeholder="No budget" hint="Most emails a day across the sequences it starts." value={budget} onChange={(e) => setBudget(e.target.value)} />
                <label className="block">
                  <span className="mb-1.5 block font-medium">Highest priority it may set</span>
                  <select className={`${field} w-full`} value={maxPriority} onChange={(e) => setMaxPriority(e.target.value as typeof maxPriority)}>
                    <option value="low">Low</option>
                    <option value="normal">Normal</option>
                    <option value="high">High</option>
                    <option value="">Any</option>
                  </select>
                </label>
                {teams.length > 0 && (
                  <label className="block">
                    <span className="mb-1.5 block font-medium">Team for its sequences</span>
                    <select className={`${field} w-full`} value={team} onChange={(e) => setTeam(e.target.value)}>
                      <option value="">No team</option>
                      {teams.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
                    </select>
                  </label>
                )}
                <label className="flex items-start gap-2 sm:col-span-2">
                  <input type="checkbox" className="mt-0.5" checked={mayTake} onChange={(e) => setMayTake(e.target.checked)} />
                  <span><span className="font-medium">May take mailboxes from lower-priority sequences</span><span className="block text-[12px] text-muted-foreground">Off: when its sequences are short, it asks an owner or admin to approve each move.</span></span>
                </label>
              </div>
            </details>
          )}

          <ErrorText>{error}</ErrorText>
          <div className="flex flex-wrap items-center gap-2">
            <Button disabled={busy || scopes.length === 0 || !name.trim()}>{busy ? "Saving…" : t ? "Save" : "Create token"}</Button>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            {t && <Button type="button" variant="danger" className="ml-auto" onClick={revoke}>Revoke</Button>}
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}
