// A mailbox supplier's mailboxes (Cheap Inboxes and the like): what the
// connected account holds, what has been imported, and the import itself.
// Reached from the supplier's card on the Connectors page.

import { useCallback, useEffect, useMemo, useState } from "react";
import type { EmailBisonCompatCall, SupplierMailbox, SupplierMailboxList, SupplierPush, SupplierSync } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, field, Loading, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago, plural } from "../format";
import { DataTable } from "@/components/data-table";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
import { appPath } from "../lib/paths";

type State = SupplierMailbox["import_state"];
type Tone = keyof typeof tones;

const stateLabel: Record<State, string> = {
  available: "Not in Outflare",
  queued: "Bringing in",
  waiting: "Waiting for supplier",
  app_password: "Getting app password",
  imported: "In Outflare",
  failed: "Needs attention",
};
const stateTone: Record<State, Tone> = { available: "grey", queued: "blue", waiting: "amber", app_password: "blue", imported: "green", failed: "red" };

const supplierLabel: Record<SupplierMailbox["status"], string> = {
  provisioning: "Being set up",
  active: "Active",
  cancelling: "Cancelling",
  cancelled: "Cancelled",
  failed: "Setup failed",
};
const supplierTone: Record<SupplierMailbox["status"], Tone> = { provisioning: "amber", active: "green", cancelling: "amber", cancelled: "grey", failed: "red" };

// Names for the platforms a supplier says a mailbox is also connected to.
const platformNames: Record<string, string> = { emailbison: "EmailBison", instantly: "Instantly", smartlead: "Smartlead", plusvibe: "PlusVibe", apollo: "Apollo" };
const platformName = (p: string) => platformNames[p] ?? p;

// Cheap Inboxes creates 20 Google app passwords an hour.
const appPasswordsPerHour = 20;

function duration(hours: number): string {
  if (hours < 1) return "under an hour";
  if (hours < 1.5) return "about an hour";
  return `about ${Math.round(hours)} hours`;
}

export function SupplierMailboxes({ ws, integrationId, supplier, canManage, onBack }: { ws: string; integrationId: string; supplier: string; canManage: boolean; onBack: () => void }) {
  const [data, setData] = useState<SupplierMailboxList | null>(null);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState<"" | State | "ready">("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [more, setMore] = useState(false);
  const path = useMemo(() => ({ params: { path: { ws, id: integrationId } } }), [ws, integrationId]);

  const load = useCallback(async () => {
    const r = await api.GET("/v1/workspaces/{ws}/integrations/{id}/mailboxes", { params: { path: { ws, id: integrationId }, query: { limit: 1000 } } });
    if (r.error) return setError(errorMessage(r.error));
    setData(r.data ?? null);
  }, [ws, integrationId]);

  useEffect(() => {
    load();
  }, [load]);

  // Syncs and imports run in the background; refresh while anything moves.
  const counts = data?.counts.by_import_state ?? {};
  const pushing = data?.sync.push.state === "connecting";
  const moving = !data?.synced || (counts.queued ?? 0) > 0 || (counts.app_password ?? 0) > 0 || pushing;
  useEffect(() => {
    if (!moving) return;
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [moving, load]);

  if (error) return <Empty title="Could not load this supplier" action={<Button variant="ghost" onClick={onBack}>Back to connectors</Button>}>{error}</Empty>;
  if (!data) return <Loading />;

  // The supplier's name ("Cheap Inboxes") reads better in sentences than
  // the connection's own name.
  const name = supplier;
  const all = data.mailboxes;
  const importable = (m: SupplierMailbox) => m.status !== "cancelled" && m.status !== "failed" && (m.import_state === "available" || m.import_state === "failed");
  const ready = all.filter((m) => importable(m) && m.status === "active");
  const notYet = ready.filter((m) => m.import_state === "available");
  const failed = all.filter((m) => m.import_state === "failed");
  const shown = all.filter((m) => (filter === "" ? true : filter === "ready" ? importable(m) && m.status === "active" : m.import_state === filter));
  const picked = all.filter((m) => selected.has(m.external_id));
  const elsewhere = Object.entries(data.counts.connected_to);
  const googleWaiting = (counts.app_password ?? 0);

  async function importMailboxes(list: SupplierMailbox[] | "all") {
    // "All" imports the active ones now; ones still being set up follow.
    const many = list === "all" ? ready : list.filter((m) => m.status === "active" || m.status === "cancelling");
    const later = list === "all" ? all.filter((m) => importable(m) && m.status === "provisioning").length : list.length - many.length;
    const google = data!.sync.app_passwords ? many.filter((m) => m.provider === "google").length : 0;
    const connected = many.filter((m) => m.connected_to.length > 0);
    const ok = await confirmAction(
      many.length === 1 ? `Bring in ${many[0].email}?` : many.length === 0 ? `Bring in ${plural(later, "mailbox", "mailboxes")} when ready?` : `Bring in ${plural(many.length, "mailbox", "mailboxes")}?`,
      <div className="space-y-2">
        <p>Outflare reads each password from {name}, checks the mailbox can sign in over SMTP and IMAP, and checks its domain's DNS. Imported mailboxes start without warm-up and send nothing until you use them in a sequence.</p>
        {later > 0 && <p>{plural(later, "more mailbox is", "more mailboxes are")} still being set up by {name} and will import as soon as {later === 1 ? "it's" : "they're"} ready.</p>}
        {google > 0 && (
          <p>
            Google refuses plain passwords, so {name} first creates an app password for each Google mailbox, {appPasswordsPerHour} an hour.
            {google > appPasswordsPerHour && ` ${plural(google, "Google mailbox", "Google mailboxes")} will take ${duration(google / appPasswordsPerHour)}. You can leave this page; it carries on.`}
          </p>
        )}
        {connected.length > 0 && (
          <p className="text-amber-700 dark:text-amber-400">
            {plural(connected.length, "of these is", "of these are")} also connected to {[...new Set(connected.flatMap((m) => m.connected_to))].map(platformName).join(" and ")}. Importing doesn't disconnect them there. If both send from the same mailbox, its daily volume adds up, so send from one place.
          </p>
        )}
      </div>,
      many.length === 1 ? "Bring it in" : many.length === 0 ? "Bring in when ready" : `Bring in ${many.length.toLocaleString()}`,
      false,
    );
    if (!ok) return;
    const body = list === "all" ? { all: true } : { external_ids: list.map((m) => m.external_id) };
    const { data: res, error } = await api.POST("/v1/workspaces/{ws}/integrations/{id}/mailboxes/import", { ...path, body });
    if (error) return toast(errorMessage(error), "error");
    const parts = [res!.queued > 0 && `${plural(res!.queued, "mailbox", "mailboxes")} on the way`, res!.waiting > 0 && `${res!.waiting.toLocaleString()} will follow when ${name} finishes setting them up`].filter(Boolean);
    toast(parts.length ? `${parts.join(", ")}.` : "Nothing to bring in.");
    setSelected(new Set());
    load();
  }

  async function pushMailboxes(list: SupplierMailbox[]) {
    const elsewhere = list.filter((m) => m.connected_to.length > 0);
    const ok = await confirmAction(
      list.length === 1 ? `Push ${list[0].email}?` : `Push ${list.length.toLocaleString()} mailboxes?`,
      <p>
        {name} links {list.length === 1 ? "it" : "them"} to its Outflare connection and sends {list.length === 1 ? "it" : "them"} here.
        {elsewhere.length > 0 && ` ${plural(elsewhere.length, "of these is", "of these are")} also connected to ${[...new Set(elsewhere.flatMap((m) => m.connected_to))].map(platformName).join(" and ")}; ${name} may move ${elsewhere.length === 1 ? "it" : "them"} to Outflare there.`}
      </p>,
      list.length === 1 ? "Push mailbox" : `Push ${list.length.toLocaleString()}`,
      false,
    );
    if (!ok) return;
    const { data: res, error } = await api.POST("/v1/workspaces/{ws}/integrations/{id}/mailboxes/push", { ...path, body: { external_ids: list.map((m) => m.external_id) } });
    if (error) return toast(errorMessage(error), "error");
    const n = res && "mailboxes" in res ? res.mailboxes : 0;
    toast(n > 0 ? `${name} is pushing ${plural(n, "mailbox", "mailboxes")}.` : "Nothing to push.");
    setSelected(new Set());
    setTimeout(load, 3000);
  }

  async function sync() {
    const { error } = await api.POST("/v1/workspaces/{ws}/integrations/{id}/mailboxes/sync", path);
    if (error) return toast(errorMessage(error), "error");
    toast(`Reading your mailboxes from ${name} again.`);
    setTimeout(load, 1500);
  }

  async function update(body: { auto_import?: boolean; app_passwords?: boolean; daily_limit?: number | null }) {
    if (body.auto_import) {
      const n = all.filter((m) => importable(m) && m.status === "active").length;
      const ok = await confirmAction(
        "Bring in mailboxes automatically?",
        <p>{n > 0 ? `${plural(n, "active mailbox", "active mailboxes")} not in Outflare yet will come in now, ` : "Mailboxes "}and every new mailbox at {name} comes in as soon as it's ready. Turn this off any time; mailboxes already in Outflare stay.</p>,
        "Turn on",
        false,
      );
      if (!ok) return;
    }
    const { data: st, error } = await api.PATCH("/v1/workspaces/{ws}/integrations/{id}/mailbox-sync", { ...path, body });
    if (error) return toast(errorMessage(error), "error");
    setData((d) => (d ? { ...d, sync: st as SupplierSync } : d));
    load();
  }

  const toggle = (m: SupplierMailbox) =>
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(m.external_id)) n.delete(m.external_id);
      else n.add(m.external_id);
      return n;
    });
  const selectable = shown.filter(importable);
  const allPicked = selectable.length > 0 && selectable.every((m) => selected.has(m.external_id));

  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Connectors", onClick: onBack }}
        title={<>{name} mailboxes</>}
        description={`The mailboxes you have at ${name}. Outflare brings them in, checks each one can sign in and send, and keeps them up to date with ${name}.`}
        action={
          canManage && (
            <>
              <Button variant="ghost" onClick={sync}>Refresh</Button>
              {!data.sync.auto_import && notYet.length > 0 && <Button onClick={() => importMailboxes(notYet)}>Bring in {notYet.length.toLocaleString()}</Button>}
            </>
          )
        }
      />

      {!data.synced && !data.sync.last_sync_error && (
        <Card title={`Reading your mailboxes from ${name}…`} description="This takes a few seconds for a few hundred mailboxes.">
          <Loading />
        </Card>
      )}

      {data.sync.last_sync_error && (
        <div role="alert" className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-[13px] text-red-700 dark:text-red-300">
          The last sync with {name} failed: {data.sync.last_sync_error} It tries again every 15 minutes. If the API key was replaced, update it on the Connectors page.
        </div>
      )}

      {data.synced && (
        <Stats cols={4}>
          <Stat label={`At ${name}`} value={data.counts.total.toLocaleString()} sub={data.sync.last_synced_at ? `checked ${ago(data.sync.last_synced_at)}` : undefined} />
          <Stat label="In Outflare" value={(counts.imported ?? 0).toLocaleString()} tone={(counts.imported ?? 0) > 0 ? "green" : undefined} />
          <Stat
            label="On the way"
            value={((counts.queued ?? 0) + googleWaiting + (counts.waiting ?? 0)).toLocaleString()}
            sub={googleWaiting > 0 ? `${googleWaiting.toLocaleString()} waiting for a Google app password, ${duration(googleWaiting / appPasswordsPerHour)}` : (counts.waiting ?? 0) > 0 ? `${(counts.waiting ?? 0).toLocaleString()} still being set up by ${name}` : undefined}
          />
          {data.sync.auto_import || notYet.length === 0 ? (
            <Stat label="Need attention" value={failed.length.toLocaleString()} tone={failed.length > 0 ? "red" : undefined} />
          ) : (
            <Stat label="Not brought in" value={notYet.length.toLocaleString()} sub="Turn on automatic, or pick them below" />
          )}
        </Stats>
      )}

      {(counts.failed ?? 0) > 0 && (
        <div role="alert" className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-[13px] text-red-700 dark:text-red-300">
          <span>{plural(counts.failed ?? 0, "mailbox needs", "mailboxes need")} attention: {counts.failed === 1 ? "it" : "they"} could not be brought in. Each one shows why.</span>
          <span className="flex gap-2">
            <Button variant="ghost" onClick={() => setFilter("failed")}>Show them</Button>
            {canManage && <Button variant="ghost" onClick={() => importMailboxes(failed)}>Try again</Button>}
          </span>
        </div>
      )}

      {data.synced && elsewhere.length > 0 && (
        <p className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[13px] text-amber-800 dark:text-amber-300">
          {elsewhere.map(([p, n]) => `${n.toLocaleString()} ${n === 1 ? "is" : "are"} also connected to ${platformName(p)}`).join("; ")}. Bringing them into Outflare doesn't disconnect them there, so decide which tool sends from each mailbox.
        </p>
      )}

      {canManage && (
        <Card
          title="Settings"
          description={data.sync.webhook ? `${name} tells Outflare about changes as they happen, and Outflare checks every 15 minutes as well.` : `Outflare checks ${name} every 15 minutes. Mailboxes cancelled there are paused here.`}
          action={<Button variant="ghost" onClick={() => setMore(!more)} aria-expanded={more}>{more ? "Fewer settings" : "More settings"}</Button>}
        >
          <div className="grid gap-4">
            <label className="flex items-start gap-3 text-[13px]">
              <Switch checked={data.sync.auto_import} onCheckedChange={(v) => update({ auto_import: v })} aria-label="Bring in mailboxes automatically" />
              <span>
                <span className="font-medium">Bring in mailboxes automatically</span>
                <span className="block text-muted-foreground">{data.sync.auto_import ? `On: every mailbox at ${name} comes into Outflare, and new ones as soon as they're ready. Nothing else to do.` : "Off: pick the mailboxes to bring in from the list below."}</span>
              </span>
            </label>
            {more && (
              <div className="grid gap-4 border-t pt-4 sm:grid-cols-2">
                <label className="flex items-start gap-3 text-[13px]">
                  <Switch checked={data.sync.app_passwords} onCheckedChange={(v) => update({ app_passwords: v })} aria-label="Get Google app passwords" />
                  <span>
                    <span className="font-medium">Get Google app passwords</span>
                    <span className="block text-muted-foreground">Leave this on. Google mailboxes can't sign in with their normal password, so {name} makes an app password for each, {appPasswordsPerHour} an hour.</span>
                  </span>
                </label>
                <label className="text-[13px]">
                  <span className="font-medium">Emails each new mailbox may send a day</span>
                  <select
                    className={`${field} mt-1 block w-full`}
                    value={data.sync.daily_limit ?? ""}
                    onChange={(e) => update({ daily_limit: e.target.value ? Number(e.target.value) : null })}
                  >
                    <option value="">Same as {name} (or 30)</option>
                    {[10, 20, 30, 40, 50].map((n) => <option key={n} value={n}>{n} a day</option>)}
                  </select>
                  <span className="mt-1 block text-muted-foreground">A sending limit for mailboxes brought in from now on. Change existing ones on Mailboxes.</span>
                </label>
              </div>
            )}
          </div>
        </Card>
      )}

      {canManage && (more || data.sync.push.state === "failed") && <PushCard ws={ws} integrationId={integrationId} name={name} push={data.sync.push} notIn={notYet.length} onChange={load} />}

      {data.synced && all.length === 0 && <Empty title={`No mailboxes in ${name} yet`}>Order mailboxes in {name}; they show up here within 15 minutes{data.sync.webhook ? ", or as soon as they're ready" : ""}.</Empty>}

      {all.length > 0 && (
        <DataTable
          data={shown}
          rowId={(m) => m.external_id}
          searchPlaceholder="Search mailboxes or domains"
          empty="No mailboxes match."
          toolbar={
            <>
              <select aria-label="Show" className={field} value={filter} onChange={(e) => setFilter(e.target.value as typeof filter)}>
                <option value="">All ({all.length.toLocaleString()})</option>
                {(Object.keys(stateLabel) as State[]).map((s) => (counts[s] ? <option key={s} value={s}>{stateLabel[s]} ({counts[s].toLocaleString()})</option> : null))}
              </select>
              {canManage && picked.length > 0 && <Button onClick={() => importMailboxes(picked)}>Bring in {picked.length.toLocaleString()} selected</Button>}
              {canManage && picked.length > 0 && data.sync.push.state === "on" && <Button variant="ghost" onClick={() => pushMailboxes(picked)}>Push {picked.length.toLocaleString()} selected</Button>}
              {(counts.imported ?? 0) > 0 && <Button variant="ghost" onClick={() => (location.href = appPath(ws, "mailboxes"))}>Open Mailboxes</Button>}
            </>
          }
          mobile={(m) => (
            <div className="flex items-start gap-3">
              {canManage && <Checkbox className="mt-0.5" checked={selected.has(m.external_id)} disabled={!importable(m)} onCheckedChange={() => toggle(m)} aria-label={`Select ${m.email}`} />}
              <div className="min-w-0 flex-1 space-y-1.5">
                <div className="flex items-start justify-between gap-2">
                  <Name m={m} />
                  <ImportCell m={m} name={name} />
                </div>
                <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                  <Badge tone={supplierTone[m.status]}>{supplierLabel[m.status]}</Badge>
                  {m.connected_to.map((p) => <Badge key={p}>{platformName(p)}</Badge>)}
                </div>
              </div>
            </div>
          )}
          columns={[
            ...(canManage
              ? [{
                  id: "select",
                  enableSorting: false,
                  enableHiding: false,
                  header: () => <Checkbox checked={allPicked} disabled={selectable.length === 0} onCheckedChange={(v) => setSelected(v ? new Set(selectable.map((m) => m.external_id)) : new Set())} aria-label="Select all that can be imported" />,
                  cell: ({ row: { original: m } }: { row: { original: SupplierMailbox } }) => <Checkbox checked={selected.has(m.external_id)} disabled={!importable(m)} onCheckedChange={() => toggle(m)} aria-label={`Select ${m.email}`} />,
                }]
              : []),
            { id: "email", header: "Mailbox", accessorFn: (m) => `${m.email} ${m.name}`, enableHiding: false, cell: ({ row: { original: m } }) => <Name m={m} /> },
            { id: "status", header: `In ${name}`, accessorFn: (m) => supplierLabel[m.status], cell: ({ row: { original: m } }) => <Badge tone={supplierTone[m.status]}>{supplierLabel[m.status]}</Badge> },
            {
              id: "elsewhere",
              header: "Also in",
              accessorFn: (m) => m.connected_to.join(" "),
              cell: ({ row: { original: m } }) => (m.connected_to.length ? <div className="flex flex-wrap gap-1">{m.connected_to.map((p) => <Badge key={p}>{platformName(p)}</Badge>)}</div> : <span className="text-muted-foreground">–</span>),
            },
            { id: "import", header: "Outflare", accessorFn: (m) => stateLabel[m.import_state], cell: ({ row: { original: m } }) => <ImportCell m={m} name={name} /> },
            { id: "provider", header: "Provider", accessorFn: (m) => m.provider, cell: ({ row: { original: m } }) => (m.provider === "google" ? "Google" : m.provider === "microsoft" ? "Microsoft" : "Other") },
            { id: "limit", header: "Daily limit", accessorFn: (m) => m.daily_limit ?? 0, meta: { align: "right", mono: true }, cell: ({ row: { original: m } }) => m.daily_limit ?? "–" },
          ]}
          hidden={["provider"]}
        />
      )}
    </div>
  );
}

function Name({ m }: { m: SupplierMailbox }) {
  return (
    <div className="min-w-0">
      <div className="truncate font-mono text-[13px]">{m.email}</div>
      {m.name && <div className="truncate text-xs text-muted-foreground">{m.name}</div>}
    </div>
  );
}

function ImportCell({ m, name }: { m: SupplierMailbox; name: string }) {
  const why =
    m.import_state === "waiting" ? `Imports when ${name} finishes setting it up.` : m.import_state === "app_password" ? `${name} is creating a Google app password; it imports right after.` : undefined;
  return (
    <div className="flex flex-col items-start gap-1">
      <Badge tone={stateTone[m.import_state]} title={why}>{stateLabel[m.import_state]}</Badge>
      {m.import_state === "imported" && m.mailbox_status && m.mailbox_status !== "active" && (
        <span className="text-xs text-muted-foreground">{m.mailbox_status === "verifying" ? "checking sign-in" : m.mailbox_status === "paused" ? "paused" : "needs attention on Mailboxes"}</span>
      )}
      {m.import_error && <span className="max-w-64 text-xs text-red-700 dark:text-red-400">{m.import_error}</span>}
    </div>
  );
}

// PushCard lets the supplier push mailboxes in, as it does to a sequencer:
// Outflare adds itself there as a self-hosted EmailBison. It also shows the
// EmailBison calls the supplier has made, so a missing one is easy to spot.
function PushCard({ ws, integrationId, name, push, notIn, onChange }: { ws: string; integrationId: string; name: string; push: SupplierPush; notIn: number; onChange: () => void }) {
  const [calls, setCalls] = useState<EmailBisonCompatCall[] | null>(null);
  const [manual, setManual] = useState(false);
  const [busy, setBusy] = useState(false);
  const path = { params: { path: { ws, id: integrationId } } };
  const url = push.base_url ?? `${location.origin}/emailbison/${ws}`;
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/emailbison-compat/calls", { params: { path: { ws }, query: { limit: 20 } } }).then(({ data }) => setCalls(data?.data ?? []));
  }, [ws, push.state, push.last_pushed_at]);
  const missing = (calls ?? []).filter((c) => !c.supported);
  const copy = (v: string) => navigator.clipboard.writeText(v).then(() => toast("Copied."));

  async function setPush(on: boolean) {
    const ok = await confirmAction(
      on ? `Let ${name} push mailboxes in?` : "Turn off push?",
      on ? (
        <p>Outflare adds itself in {name} as an EmailBison connection, with an API token that can only read and add mailboxes. Nothing is sent until you choose Push.</p>
      ) : (
        <p>The token {name} uses stops working and the connection is removed in {name}. Mailboxes already pushed stay in Outflare.</p>
      ),
      on ? "Turn on push" : "Turn off push",
      !on,
    );
    if (!ok) return;
    setBusy(true);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/integrations/{id}/mailbox-sync", { ...path, body: { push: on } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(on ? `Connecting to ${name}…` : "Push is off.");
    onChange();
  }

  async function pushAll() {
    const ok = await confirmAction(
      `Push ${notIn.toLocaleString()} mailboxes?`,
      <p>{name} links every active mailbox not yet in Outflare to its Outflare connection and sends them here. Mailboxes it also connects to another sequencer may move to Outflare there. To try one first, select it in the table and choose Push selected.</p>,
      `Push ${notIn.toLocaleString()}`,
      false,
    );
    if (!ok) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/integrations/{id}/mailboxes/push", { ...path, body: { all: true } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    const n = data && "mailboxes" in data ? data.mailboxes : 0;
    toast(n > 0 ? `${name} is pushing ${plural(n, "mailbox", "mailboxes")}.` : "Nothing to push: every active mailbox is already in Outflare.");
    setTimeout(onChange, 3000);
  }

  const last = push.last_result;
  return (
    <Card
      title={`Or let ${name} push mailboxes in`}
      description={`Most people don't need this: Outflare already brings mailboxes in (above). This is the other way round: ${name} sends mailboxes here the way it sends them to a sequencer.`}
      action={
        !push.available ? null : push.state === "on" ? (
          <div className="flex gap-2">
            <Button disabled={busy || notIn === 0} onClick={pushAll}>{notIn > 0 ? `Push ${notIn.toLocaleString()} not in Outflare` : "All pushed"}</Button>
            <Button variant="ghost" disabled={busy} onClick={() => setPush(false)}>Turn off</Button>
          </div>
        ) : push.state === "connecting" ? (
          <Badge tone="blue">Connecting…</Badge>
        ) : (
          <Button disabled={busy} onClick={() => setPush(true)}>{push.state === "failed" ? "Try again" : "Turn on push"}</Button>
        )
      }
    >
      <div className="grid gap-3 text-[13px]">
        {!push.available && (
          <p className="text-muted-foreground">
            {name} needs to reach this Outflare from the internet. Set <span className="font-mono text-[12px]">PUBLIC_URL</span> to its https address to turn on push.
          </p>
        )}
        {push.state === "failed" && push.error && (
          <p className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-red-800 dark:text-red-300">{name} could not add the connection: {push.error}</p>
        )}
        {push.state === "on" && (
          <p className="text-muted-foreground">
            Connected {push.connected_at ? ago(push.connected_at) : ""}.{" "}
            {last && push.last_pushed_at
              ? `Last push ${ago(push.last_pushed_at)}: ${plural(last.synced, "mailbox", "mailboxes")} sent${last.failed ? `, ${last.failed.toLocaleString()} failed` : ""}${last.skipped ? `, ${last.skipped.toLocaleString()} skipped` : ""}.`
              : "Nothing pushed yet."}
            {push.error ? ` The last push failed: ${push.error}` : ""}
          </p>
        )}
        {push.available && push.state !== "on" && (
          <button type="button" className="w-fit text-left text-muted-foreground underline" onClick={() => setManual(!manual)}>
            {manual ? "Hide the steps" : `Or set it up in ${name} yourself`}
          </button>
        )}
        {manual && push.state !== "on" && (
          <ol className="grid list-decimal gap-2 pl-5">
            <li>In {name}, add a sequencer connection and choose <span className="font-medium">EmailBison</span>.</li>
            <li>
              Set the base URL to{" "}
              <button type="button" className="rounded bg-muted px-1.5 py-0.5 font-mono text-[12px]" onClick={() => copy(url)} title="Copy">{url}</button>
            </li>
            <li>
              For the API key, <a className="underline" href={appPath(ws, "tokens")}>make an Outflare API token</a> with <span className="font-mono text-[12px]">mailboxes.read</span> and <span className="font-mono text-[12px]">mailboxes.manage</span>.
            </li>
          </ol>
        )}
        {calls === null ? (
          <Loading />
        ) : calls.length === 0 ? null : (
          <details className="grid gap-2">
            <summary className="cursor-pointer text-muted-foreground">Calls from {name} ({calls.length.toLocaleString()} recent{missing.length ? `, ${missing.length.toLocaleString()} not answered yet` : ""})</summary>
            {missing.length > 0 && (
              <p className="mt-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-amber-800 dark:text-amber-300">
                {plural(missing.length, "recent call", "recent calls")} asked for something Outflare doesn't answer yet. Share this list with the Outflare project to get it added.
              </p>
            )}
            <ul className="mt-2 grid gap-1">
              {calls.map((c, i) => (
                <li key={i} className="flex flex-wrap items-center gap-2">
                  <Badge tone={c.supported && c.status < 400 ? "green" : c.supported ? "amber" : "red"}>{c.status}</Badge>
                  <span className="font-mono text-[12px]">{c.method} {c.path}</span>
                  {c.body_keys.length > 0 && <span className="text-muted-foreground">fields: {c.body_keys.join(", ")}</span>}
                  <span className="ml-auto text-muted-foreground">{ago(c.created_at)}</span>
                </li>
              ))}
            </ul>
          </details>
        )}
      </div>
    </Card>
  );
}
