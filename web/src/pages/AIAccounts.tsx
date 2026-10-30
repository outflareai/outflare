// AI Accounts: the AI subscriptions and API accounts the workspace's people
// pay for (plans/ai-accounts). Members see and change their own; admins
// see everyone's, and the money on them. People add accounts by hand for
// now; receipts, Outflare for desktop and admin keys fill them in later.

import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import type { AIAccount, AIAccountInput, AIProvider, Member } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, confirmAction, Empty, ErrorText, field, Loading, Logo, PageHeader, toast } from "../ui";
import type { Tab } from "@/lib/paths";
import { DataTable } from "@/components/data-table";
import { AILogo, providerLabel } from "@/components/ai-logo";
import { Switch } from "@/components/ui/switch";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { companyPaid, cycleWord, daysUntil, monthlyCents, owners, shortDay, usd } from "@/lib/aiAccounts";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PeopleView, SpendView, TimelineView, UsageView } from "./AIAccountsViews";
import { ReceiptsView } from "./AIAccountsReceipts";

type Props = {
  ws: string;
  me: string;
  canAdd: boolean;
  seeAll: boolean;
  manageAll: boolean;
  seeSpend: boolean;
  canSetUp: boolean;
  go: (t: Tab) => void;
};

type OutflareAI = { month: string; calls: number; cost_cents: number; providers: { provider: string; calls: number; cost_cents: number }[] };

const providers: AIProvider[] = ["claude", "chatgpt", "cursor", "copilot", "anthropic_api", "openai_api"];

function renewal(a: AIAccount) {
  if (a.cancelled) return <span className="text-muted-foreground">Cancelled</span>;
  if (!a.renews_on) return <span className="text-muted-foreground">—</span>;
  const n = daysUntil(a.renews_on);
  const when = n < 0 ? `${-n}d ago` : n === 0 ? "today" : `in ${n}d`;
  return (
    <span className="block whitespace-nowrap tabular-nums">
      <span className={cn(n >= 0 && n <= 3 && "text-amber-600 dark:text-amber-400")}>{shortDay(a.renews_on)}</span>
      <span className="block text-[12px] text-muted-foreground">{when}</span>
    </span>
  );
}

function price(a: AIAccount) {
  if (a.price_hidden) return <span className="text-muted-foreground" title="You need AI spend to see prices on other people's accounts">Hidden</span>;
  if (a.price_cents == null) return <span className="text-muted-foreground">—</span>;
  return (
    <span className="whitespace-nowrap tabular-nums">
      {usd(a.price_cents)}
      {a.billing_cycle && <span className="text-muted-foreground">/{cycleWord[a.billing_cycle]}</span>}
    </span>
  );
}

// Monthly cost of the company-paid accounts the caller can see the price of.
const monthly = (list: AIAccount[]) => list.filter(companyPaid).reduce((n, a) => n + (monthlyCents(a) ?? 0), 0);

// The page's views. Spend and people are for those who see everyone's
// accounts (spend also needs ai_spend.read).
type View = "accounts" | "spend" | "people" | "timeline" | "usage" | "receipts";

export function AIAccounts({ ws, me, canAdd, seeAll, manageAll, seeSpend, canSetUp, go }: Props) {
  const [accounts, setAccounts] = useState<AIAccount[] | null>(null);
  const [error, setError] = useState("");
  const [provider, setProvider] = useState<AIProvider | "">("");
  const [everyone, setEveryone] = useState(seeAll);
  const [editing, setEditing] = useState<AIAccount | "new" | null>(null);
  const [view, setView] = useState<View>("accounts");
  const views: { id: View; label: string }[] = [
    { id: "accounts", label: "Accounts" },
    ...(seeAll && seeSpend ? [{ id: "spend" as View, label: "Spend" }] : []),
    ...(seeAll ? [{ id: "people" as View, label: "People" }] : []),
    { id: "timeline", label: "Timeline" },
    { id: "usage", label: "Usage" },
    { id: "receipts", label: "Receipts" },
  ];

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/ai-accounts", { params: { path: { ws }, query: everyone ? {} : { mine: true } } });
    if (error) setError(errorMessage(error));
    setAccounts(data?.data ?? []);
  }, [ws, everyone]);
  useEffect(() => {
    load();
  }, [load]);

  // The AI account Outflare itself uses, on the workspace's own keys.
  const [outflare, setOutflare] = useState<OutflareAI | null>(null);
  useEffect(() => {
    if (!seeSpend) return;
    api.GET("/v1/workspaces/{ws}/ai-accounts/outflare", { params: { path: { ws } } }).then(({ data }) => setOutflare(data ?? null));
  }, [ws, seeSpend]);

  const counts = useMemo(() => {
    const c: Partial<Record<AIProvider, number>> = {};
    for (const a of accounts ?? []) c[a.provider] = (c[a.provider] ?? 0) + 1;
    return c;
  }, [accounts]);
  const shown = (accounts ?? []).filter((a) => !provider || a.provider === provider);
  const left = (accounts ?? []).filter((a) => a.owner_left && !a.cancelled);
  const total = monthly(shown);

  return (
    <div className="space-y-6">
      <PageHeader
        title="AI accounts"
        description={seeAll ? "Every AI subscription and API account people here pay for: what each costs, when it renews and who it belongs to." : "The AI subscriptions and API accounts you use: what each costs, when it renews and who else is on it."}
        action={canAdd && <Button onClick={() => setEditing("new")}>Add account</Button>}
      />
      <ErrorText>{error}</ErrorText>
      {left.length > 0 && seeAll && (
        <div className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-[13px]">
          {left.length === 1 ? "One account belongs" : `${left.length} accounts belong`} to people who left the workspace: cancel {left.length === 1 ? "it" : "them"} or hand {left.length === 1 ? "it" : "them"} to someone.{" "}
          <button className="underline underline-offset-2" onClick={() => setEditing(left[0])}>Review {left[0].name}</button>
        </div>
      )}
      {accounts !== null && (
        <Tabs value={view} onValueChange={(v) => { setView(v as View); track("ui.ai_accounts_view_opened", { view: v }); }}>
          <TabsList>
            {views.map((v) => <TabsTrigger key={v.id} value={v.id}>{v.label}</TabsTrigger>)}
          </TabsList>
        </Tabs>
      )}
      {accounts === null ? (
        <Loading />
      ) : view === "spend" ? (
        <SpendView accounts={accounts} open={setEditing} />
      ) : view === "people" ? (
        <PeopleView accounts={accounts} open={setEditing} />
      ) : view === "timeline" ? (
        <TimelineView accounts={accounts} open={setEditing} />
      ) : view === "usage" ? (
        <UsageView accounts={accounts} />
      ) : view === "receipts" ? (
        <ReceiptsView ws={ws} accounts={accounts} canSetUp={canSetUp} seeSpend={seeSpend} manageAll={manageAll} open={setEditing} reload={load} />
      ) : accounts.length === 0 ? (
        <Empty title={everyone ? "No AI accounts yet" : "You have no AI accounts yet"} action={canAdd && <Button onClick={() => setEditing("new")}>Add account</Button>}>
          Add the Claude, ChatGPT, Cursor or Copilot plans and API accounts you pay for, with what they cost and when they renew.
        </Empty>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <Chip on={!provider} onClick={() => setProvider("")}>All <span className="text-muted-foreground">{accounts.length}</span></Chip>
            {providers.filter((p) => counts[p]).map((p) => (
              <Chip key={p} on={provider === p} onClick={() => setProvider(provider === p ? "" : p)}>
                <AILogo provider={p} className="size-5 border-0 bg-transparent" /> {providerLabel(p)} <span className="text-muted-foreground">{counts[p]}</span>
              </Chip>
            ))}
            {seeAll && (
              <label className="ml-auto flex items-center gap-2 text-[13px] text-muted-foreground">
                <Switch checked={everyone} onCheckedChange={setEveryone} aria-label="Show everyone's accounts" /> Everyone's
              </label>
            )}
          </div>
          {(seeSpend || !everyone) && total > 0 && (
            <p className="text-[13px] text-muted-foreground">
              <span className="font-medium text-foreground tabular-nums">{usd(Math.round(total))}</span> a month on the company-paid plans shown, before usage.
            </p>
          )}
          {outflare && !provider && (
            <div className="flex flex-wrap items-center gap-3 rounded-md border px-3 py-2.5 text-[13px]">
              <span className="inline-flex size-7 items-center justify-center rounded-md border bg-background"><Logo markOnly className="[&_svg]:h-3.5" /></span>
              <div className="min-w-0 flex-1">
                <div className="font-medium">Outflare's own AI</div>
                <div className="text-muted-foreground">
                  {outflare.calls === 0
                    ? "No AI calls this month yet. Outflare uses the AI keys you connect, on your own bill."
                    : `${usd(outflare.cost_cents)} this month across ${outflare.calls.toLocaleString()} calls, on your own ${outflare.providers.map((p) => p.provider).join(" and ")} key${outflare.providers.length > 1 ? "s" : ""}.`}
                </div>
              </div>
              <Button variant="ghost" onClick={() => go("ai-usage")}>AI usage</Button>
            </div>
          )}
          <DataTable
            data={shown}
            rowId={(a) => a.id}
            searchPlaceholder="Search accounts"
            onRowClick={(a) => setEditing(a)}
            initialSort={[{ id: "renews", desc: false }]}
            mobile={(a) => (
              <div className="flex items-center gap-3">
                <AILogo provider={a.provider} />
                <div className="min-w-0 space-y-0.5">
                  <div className="font-medium">{a.name} {a.owner_left && <Badge tone="amber">Owner left</Badge>}</div>
                  <div className="truncate text-[12px] text-muted-foreground">{a.account_email ?? providerLabel(a.provider)} · {price(a)}</div>
                </div>
              </div>
            )}
            columns={[
              {
                id: "name", header: "Account", accessorFn: (a) => `${a.name} ${a.plan ?? ""} ${providerLabel(a.provider)}`,
                cell: ({ row: { original: a } }) => (
                  <div className="flex items-center gap-3">
                    <AILogo provider={a.provider} />
                    <div className="min-w-44">
                      <div className="font-medium">{a.name}</div>
                      <div className="text-[12px] text-muted-foreground">{[a.plan ?? providerLabel(a.provider), a.seats && a.seats > 1 ? `${a.seats} seats` : ""].filter(Boolean).join(" · ")}</div>
                    </div>
                  </div>
                ),
              },
              { id: "email", header: "Account email", accessorFn: (a) => a.account_email ?? "", cell: ({ row: { original: a } }) => <span className="text-muted-foreground">{a.account_email ?? "—"}</span> },
              {
                id: "people", header: "Belongs to", accessorFn: (a) => owners(a).map((m) => m.name).join(", "),
                cell: ({ row: { original: a } }) => (
                  <span className="flex flex-wrap items-center gap-1">
                    {owners(a).map((m) => m.name || m.email).join(", ") || <span className="text-muted-foreground">Nobody</span>}
                    {a.members.length > owners(a).length && <span className="text-muted-foreground">+{a.members.length - owners(a).length}</span>}
                    {a.owner_left && <Badge tone="amber">Owner left</Badge>}
                  </span>
                ),
              },
              { id: "renews", header: "Renews", accessorFn: (a) => (a.cancelled ? "9999" : a.renews_on ?? "9998"), cell: ({ row: { original: a } }) => renewal(a) },
              { id: "price", header: "Price", accessorFn: (a) => a.price_cents ?? -1, cell: ({ row: { original: a } }) => price(a) },
              { id: "paid", header: "Paid by", accessorFn: (a) => a.paid_by, cell: ({ row: { original: a } }) => (a.paid_by === "person" ? <Badge tone="blue">Person</Badge> : <span className="text-muted-foreground">Company</span>) },
              { id: "resets", header: "Resets left", accessorFn: (a) => a.resets_available ?? -1, cell: ({ row: { original: a } }) => (a.resets_available == null ? <span className="text-muted-foreground">—</span> : <span className="tabular-nums">{a.resets_available}</span>) },
            ]}
          />
        </>
      )}
      {editing && (
        <AccountSheet
          ws={ws}
          me={me}
          account={editing === "new" ? null : editing}
          manageAll={manageAll}
          canEdit={editing === "new" ? canAdd : manageAll || (canAdd && editing.members.some((m) => m.user_id === me))}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); load(); }}
        />
      )}
    </div>
  );
}

function Chip({ on, onClick, children }: { on: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" onClick={onClick} aria-pressed={on} className={cn("inline-flex h-8 items-center gap-1.5 rounded-full border px-3 text-[13px] hover:bg-accent", on && "border-foreground/30 bg-accent font-medium")}>
      {children}
    </button>
  );
}

type Person = { user_id: string; name: string; email: string; role: "owner" | "user"; left?: boolean };

function AccountSheet({ ws, me, account, manageAll, canEdit, onClose, onSaved }: { ws: string; me: string; account: AIAccount | null; manageAll: boolean; canEdit: boolean; onClose: () => void; onSaved: () => void }) {
  const [providerID, setProviderID] = useState<AIProvider>(account?.provider ?? "claude");
  const [name, setName] = useState(account?.name ?? "");
  const [email, setEmail] = useState(account?.account_email ?? "");
  const [plan, setPlan] = useState(account?.plan ?? "");
  const [seats, setSeats] = useState(account?.seats ? String(account.seats) : "");
  const [paidBy, setPaidBy] = useState<"company" | "person">(account?.paid_by ?? "company");
  const [cycle, setCycle] = useState(account?.billing_cycle ?? "monthly");
  const [renews, setRenews] = useState(account?.renews_on ?? "");
  const [dollars, setDollars] = useState(account?.price_cents != null ? String(account.price_cents / 100) : "");
  const [resets, setResets] = useState(account?.resets_available != null ? String(account.resets_available) : "");
  const [cancelled, setCancelled] = useState(account?.cancelled ?? false);
  const [people, setPeople] = useState<Person[]>(account?.members.map((m) => ({ ...m, role: m.role as Person["role"] })) ?? []);
  const [members, setMembers] = useState<Member[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) => {
      const list = data?.data ?? [];
      setMembers(list);
      if (!account) {
        const self = list.find((m) => m.user_id === me);
        setPeople([{ user_id: me, name: self?.name ?? "You", email: self?.email ?? "", role: "owner" }]);
      }
    });
  }, [ws, me, account]);

  const addable = members.filter((m) => !people.some((p) => p.user_id === m.user_id));

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    const cents = dollars.trim() === "" ? undefined : Math.round(Number(dollars) * 100);
    if (cents !== undefined && (!Number.isFinite(cents) || cents < 0)) return setError("The price must be a number of dollars, such as 20 or 19.99.");
    const body: AIAccountInput = {
      provider: providerID, name, account_email: email, plan, paid_by: paidBy, billing_cycle: cycle as AIAccountInput["billing_cycle"], renews_on: renews,
      ...(seats ? { seats: Number(seats) } : {}),
      ...(cents !== undefined ? { price_cents: cents } : {}),
      ...(resets ? { resets_available: Number(resets) } : {}),
      ...(account ? { cancelled } : {}),
      members: people.map((p) => ({ user_id: p.user_id, role: p.role })),
    };
    setBusy(true);
    const res = account
      ? await api.PATCH("/v1/workspaces/{ws}/ai-accounts/{id}", { params: { path: { ws, id: account.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/ai-accounts", { params: { path: { ws } }, body });
    setBusy(false);
    if (res.error) return setError(errorMessage(res.error));
    toast(account ? `Saved ${name}.` : `Added ${name}.`);
    onSaved();
  }

  async function remove() {
    if (!account) return;
    if (!(await confirmAction(`Remove ${account.name}?`, "Only for an account added by mistake. If it ended, mark it cancelled instead, so its history stays.", "Remove account"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/ai-accounts/{id}", { params: { path: { ws, id: account.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Removed ${account.name}.`);
    onSaved();
  }

  const label = "mb-1 block font-medium";
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2"><AILogo provider={providerID} /> {account ? account.name : "Add an AI account"}</SheetTitle>
          <SheetDescription>A plan or API account at an AI company, what it costs and who it belongs to.</SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-5 px-4 pb-6 text-[13px]">
          <fieldset disabled={!canEdit || busy} className="space-y-5">
            <div className="grid grid-cols-2 gap-3">
              <label className="col-span-2 block sm:col-span-1">
                <span className={label}>AI tool</span>
                <select className={`${field} w-full`} value={providerID} onChange={(e) => setProviderID(e.target.value as AIProvider)}>
                  {providers.map((p) => <option key={p} value={p}>{providerLabel(p)}</option>)}
                </select>
              </label>
              <label className="col-span-2 block sm:col-span-1">
                <span className={label}>Plan</span>
                <input className={`${field} w-full`} maxLength={100} placeholder="e.g. Max 20x, Plus, Team" value={plan} onChange={(e) => setPlan(e.target.value)} />
              </label>
              <label className="col-span-2 block">
                <span className={label}>Name</span>
                <input className={`${field} w-full`} required maxLength={100} placeholder="e.g. Claude Max — Design" value={name} onChange={(e) => setName(e.target.value)} />
              </label>
              <label className="col-span-2 block">
                <span className={label}>Account email</span>
                <input className={`${field} w-full`} type="email" placeholder="The email you sign in to it with" value={email} onChange={(e) => setEmail(e.target.value)} />
                <span className="mt-1 block text-[12px] text-muted-foreground">Receipts sent to it will update the price and renewal once receipt forwarding is set up.</span>
              </label>
            </div>

            <div className="grid grid-cols-2 gap-3">
              <label className="block">
                <span className={label}>Price (USD)</span>
                <input className={`${field} w-full`} inputMode="decimal" placeholder="20" value={dollars} onChange={(e) => setDollars(e.target.value)} />
              </label>
              <label className="block">
                <span className={label}>Billed</span>
                <select className={`${field} w-full`} value={cycle ?? ""} onChange={(e) => setCycle(e.target.value as typeof cycle)}>
                  <option value="monthly">Monthly</option>
                  <option value="yearly">Yearly</option>
                  <option value="prepaid">Prepaid credits</option>
                  <option value="usage">By usage</option>
                </select>
              </label>
              <label className="block">
                <span className={label}>Next renewal</span>
                <input className={`${field} w-full`} type="date" value={renews} onChange={(e) => setRenews(e.target.value)} />
              </label>
              <label className="block">
                <span className={label}>Seats</span>
                <input className={`${field} w-full`} type="number" min={1} max={100000} placeholder="1" value={seats} onChange={(e) => setSeats(e.target.value)} />
              </label>
              <label className="block">
                <span className={label}>Paid by</span>
                <select className={`${field} w-full`} value={paidBy} onChange={(e) => setPaidBy(e.target.value as "company" | "person")}>
                  <option value="company">The company</option>
                  <option value="person">The person</option>
                </select>
                <span className="mt-1 block text-[12px] text-muted-foreground">Only company-paid accounts count in spend.</span>
              </label>
              <label className="block">
                <span className={label}>Resets left</span>
                <input className={`${field} w-full`} type="number" min={0} max={1000} placeholder="Unknown" value={resets} onChange={(e) => setResets(e.target.value)} />
                <span className="mt-1 block text-[12px] text-muted-foreground">Banked resets, such as Codex's.</span>
              </label>
            </div>

            <div className="space-y-2">
              <span className="block font-medium">Belongs to</span>
              {people.map((p) => (
                <div key={p.user_id} className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate">{p.name || p.email}{p.user_id === me && <span className="text-muted-foreground"> (you)</span>} {p.left && <Badge tone="amber">Left</Badge>}</span>
                  <select aria-label="Role" className={`${field} w-28`} value={p.role} onChange={(e) => setPeople((ps) => ps.map((x) => (x.user_id === p.user_id ? { ...x, role: e.target.value as Person["role"] } : x)))}>
                    <option value="owner">Owner</option>
                    <option value="user">Also uses</option>
                  </select>
                  {(manageAll || p.user_id !== me) && <Button type="button" variant="ghost" onClick={() => setPeople((ps) => ps.filter((x) => x.user_id !== p.user_id))}>Remove</Button>}
                </div>
              ))}
              {addable.length > 0 && (
                <select aria-label="Add a person" className={`${field} w-full`} value="" onChange={(e) => { const m = members.find((x) => x.user_id === e.target.value); if (m) setPeople((ps) => [...ps, { user_id: m.user_id ?? "", name: m.name ?? "", email: m.email ?? "", role: ps.length ? "user" : "owner" }]); }}>
                  <option value="">Add a person…</option>
                  {addable.map((m) => <option key={m.user_id} value={m.user_id}>{m.name || m.email}</option>)}
                </select>
              )}
            </div>

            {account && (
              <label className="flex items-center gap-2">
                <Switch checked={cancelled} onCheckedChange={setCancelled} /> Cancelled: it no longer renews
              </label>
            )}
          </fieldset>
          <ErrorText>{error}</ErrorText>
          {canEdit && (
            <div className="flex items-center gap-2">
              <Button type="submit" disabled={busy}>{account ? "Save" : "Add account"}</Button>
              <Button type="button" variant="ghost" onClick={onClose}>Close</Button>
              {account && <Button type="button" variant="ghost" className="ml-auto text-destructive" onClick={remove}>Remove</Button>}
            </div>
          )}
        </form>
      </SheetContent>
    </Sheet>
  );
}
