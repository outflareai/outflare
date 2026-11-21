// Audiences: one of the brand's lists of people, named once and sent to
// the ad accounts the brand chooses, so the same people reach several ad
// networks. The table shows each audience and how every network's copy is
// doing; an audience's own page sets the list and the networks, and gives
// a file for a network that only takes uploads.

import { useCallback, useEffect, useState } from "react";
import type { BrandAdAudience, SmartList } from "@outflare/sdk";
import type { Tab } from "../../App";
import { api, errorMessage } from "../../api";
import { DataTable } from "@/components/data-table";
import { track } from "../../lib/analytics";
import { ago } from "../../format";
import { Badge, Button, Card, Empty, ErrorText, Input, Loading, Menu, PageHeader, ask, field, toast } from "../../ui";

type Account = { id: string; name: string; connector: string; connector_name: string; can_sync: boolean; can_exclude: boolean };
type Destination = BrandAdAudience["destinations"][number];

const destTone = { waiting: "amber", synced: "green", error: "red" } as const;
const destLabel = { waiting: "Waiting for first sync", synced: "Synced", error: "Failed" } as const;
const count = (n: number) => n.toLocaleString();

function useAudiences(brand: string) {
  const [rows, setRows] = useState<BrandAdAudience[] | null>(null);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/audiences", { params: { path: { brand } } });
    setError(error ? errorMessage(error) : "");
    setRows(data?.data ?? []);
    setAccounts(data?.accounts ?? []);
  }, [brand]);
  useEffect(() => {
    load();
  }, [load]);
  return { rows, accounts, error, load };
}

async function syncNow(brand: string, a: BrandAdAudience, done: () => void) {
  const { error } = await api.POST("/v1/brands/{brand}/ads/audiences/{id}/sync", { params: { path: { brand, id: a.id } } });
  if (error) return toast(errorMessage(error), "error");
  toast(`Syncing ${a.name}. The table updates in a minute or two.`);
  done();
}

async function setEnabled(brand: string, a: BrandAdAudience, enabled: boolean, done: () => void) {
  const { error } = await api.PATCH("/v1/brands/{brand}/ads/audiences/{id}", { params: { path: { brand, id: a.id } }, body: { enabled } });
  if (error) return toast(errorMessage(error), "error");
  toast(enabled ? `${a.name} is on. It syncs now, then once a day.` : `${a.name} is off. Nobody is added or taken out until it is on again.`);
  done();
}

async function remove(brand: string, a: BrandAdAudience, done: () => void) {
  const ok = await ask({ title: `Delete ${a.name}?`, body: "Outflare stops updating it. The lists already made on the ad networks stay there, with whoever is in them.", choices: [{ label: "Delete", value: "delete", variant: "danger-solid" }, { label: "Keep", value: "keep", variant: "ghost" }] });
  if (ok?.choice !== "delete") return;
  const { error } = await api.DELETE("/v1/brands/{brand}/ads/audiences/{id}", { params: { path: { brand, id: a.id } } });
  if (error) return toast(errorMessage(error), "error");
  toast(`${a.name} is deleted.`);
  done();
}

// The audience's file, for a network that takes audiences as an upload.
async function download(brand: string, a: BrandAdAudience) {
  const res = await fetch(`/v1/brands/${brand}/ads/audiences/${a.id}/file`, { credentials: "same-origin" });
  if (!res.ok) return toast("The file could not be made. Try again in a minute.", "error");
  const skipped = Number(res.headers.get("X-Outflare-Skipped") ?? 0);
  const url = URL.createObjectURL(await res.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = `${a.name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "audience"}.csv`;
  link.click();
  URL.revokeObjectURL(url);
  track("ui.ads_audience_file_downloaded", { skipped: skipped > 0 });
  toast(skipped > 0 ? `Downloaded. ${count(skipped)} people were left out: no ad consent, or no email or phone number.` : "Downloaded.");
}

function Networks({ a }: { a: BrandAdAudience }) {
  if (a.destinations.length === 0) return <span className="text-muted-foreground">File only</span>;
  return (
    <div className="flex flex-wrap gap-1.5">
      {a.destinations.map((d) => <Badge key={d.id} tone={a.enabled ? destTone[d.status] : "grey"} title={d.error || `${d.account_name}: ${destLabel[d.status]}`}>{d.connector_name}</Badge>)}
    </div>
  );
}

const lastSync = (a: BrandAdAudience) => a.destinations.map((d) => d.synced_at).filter((t): t is string => !!t).sort().pop() ?? null;

export function Audiences({ brand, canManage, go, item }: { brand: string; canManage: boolean; go: (tab: Tab, item?: string) => void; item: string }) {
  const { rows, accounts, error, load } = useAudiences(brand);
  const open = (id: string) => go("ads", `audiences/${id}`);

  if (item === "new") return <AudienceForm brand={brand} accounts={accounts} go={go} saved={load} />;
  if (item) {
    if (!rows) return <Loading />;
    const a = rows.find((r) => r.id === item);
    if (!a) return <Empty title="This audience is gone" action={<Button onClick={() => go("ads", "audiences")}>Back to Audiences</Button>}>It may have been deleted.</Empty>;
    return <AudiencePage key={a.id} brand={brand} a={a} accounts={accounts} canManage={canManage} go={go} reload={load} />;
  }

  return (
    <Card
      title="Audiences"
      description="Pick a list of people once and send it to every ad network you use. Outflare makes the list on each network, adds people as hashed emails and phone numbers, and updates it once a day. Source: your lists in Outflare."
      action={canManage ? <Button onClick={() => go("ads", "audiences/new")}>New audience</Button> : undefined}
    >
      {error && <ErrorText>{error}</ErrorText>}
      {!rows ? <Loading /> : rows.length === 0 ? (
        <Empty title="No audiences yet" action={canManage ? <Button onClick={() => go("ads", "audiences/new")}>New audience</Button> : undefined}>
          For example: paying customers to leave out of prospecting, people who signed up and never paid to win back, or big spenders to find more like them.
        </Empty>
      ) : (
        <DataTable
          data={rows}
          rowId={(a) => a.id}
          onRowClick={(a) => open(a.id)}
          empty="No audience matches."
          mobile={(a) => (
            <div className="space-y-1">
              <div className="flex items-center justify-between gap-2"><span className="font-medium">{a.name}</span><Badge tone={a.enabled ? "green" : "grey"}>{a.enabled ? "On" : "Off"}</Badge></div>
              <div className="text-[12px] text-muted-foreground">{a.list ? `${a.list.name} · ${count(a.list.members)} people` : "List deleted"}</div>
              <Networks a={a} />
            </div>
          )}
          columns={[
            { id: "name", header: "Audience", accessorFn: (a) => a.name, cell: ({ row: { original: a } }) => <button type="button" className="font-medium text-blue-700 underline-offset-2 hover:underline dark:text-blue-400" onClick={(e) => { e.stopPropagation(); open(a.id); }}>{a.name}</button> },
            { id: "status", header: "Status", accessorFn: (a) => (a.enabled ? "On" : "Off"), cell: ({ row: { original: a } }) => !a.list ? <Badge tone="red">List deleted</Badge> : <Badge tone={a.enabled ? "green" : "grey"}>{a.enabled ? "On" : "Off"}</Badge> },
            { id: "list", header: "List", accessorFn: (a) => a.list?.name ?? "", cell: ({ row: { original: a } }) => a.list ? a.list.name : <span className="text-muted-foreground">Choose another list</span> },
            { id: "people", header: "People in list", meta: { align: "right" }, accessorFn: (a) => a.list?.members ?? 0, cell: ({ row: { original: a } }) => <span className="font-mono tabular-nums">{a.list ? count(a.list.members) : "—"}</span> },
            { id: "networks", header: "Sent to", enableSorting: false, accessorFn: (a) => a.destinations.map((d) => `${d.connector_name} ${destLabel[d.status]}`).join(", "), cell: ({ row: { original: a } }) => <Networks a={a} /> },
            { id: "synced", header: "Last synced", accessorFn: (a) => lastSync(a) ?? "", cell: ({ row: { original: a } }) => lastSync(a) ? <span className="whitespace-nowrap">{ago(lastSync(a))}</span> : <span className="text-muted-foreground">Not yet</span> },
            ...(canManage ? [{
              id: "actions", header: "", enableSorting: false, accessorFn: () => "", cell: ({ row: { original: a } }: { row: { original: BrandAdAudience } }) => (
                <div className="flex justify-end gap-1.5" onClick={(e) => e.stopPropagation()}>
                  {a.enabled && a.list && a.destinations.length > 0 && <Button variant="ghost" onClick={() => syncNow(brand, a, load)}>Sync now</Button>}
                  <Menu aria={`More for ${a.name}`} items={[
                    { label: "Open", onClick: () => open(a.id) },
                    !!a.list && { label: "Download file", onClick: () => download(brand, a) },
                    { label: a.enabled ? "Turn off" : "Turn on", onClick: () => setEnabled(brand, a, !a.enabled, load) },
                    { label: "Delete", danger: true, onClick: () => remove(brand, a, load) },
                  ]} />
                </div>
              ),
            }] : []),
          ]}
        />
      )}
    </Card>
  );
}

// The fields an audience is made of: its name, its list, and the ad
// accounts it goes to. With one list or one account there is nothing to
// pick, so it is picked already.
function AudienceForm({ brand, accounts, a, go, saved }: { brand: string; accounts: Account[]; a?: BrandAdAudience; go: (tab: Tab, item?: string) => void; saved: () => void }) {
  const [lists, setLists] = useState<SmartList[] | null>(null);
  const [name, setName] = useState(a?.name ?? "");
  const [list, setList] = useState(a?.list?.id ?? "");
  const [picked, setPicked] = useState<string[]>(a ? a.destinations.map((d) => d.ad_account_id) : []);
  const [exclude, setExclude] = useState(a?.exclude ?? false);
  const [busy, setBusy] = useState(false);
  const canSync = accounts.filter((c) => c.can_sync);
  // Networks picked that can't take the list as an exclusion from here.
  const byHand = [...new Set(canSync.filter((c) => picked.includes(c.id) && !c.can_exclude).map((c) => c.connector_name))];
  const fileOnly = [...new Set(accounts.filter((c) => !c.can_sync).map((c) => c.connector_name))];

  useEffect(() => {
    api.GET("/v1/brands/{brand}/smart-lists", { params: { path: { brand } } }).then(({ data }) => {
      const people = ((data?.data ?? []) as SmartList[]).filter((l) => l.record_type === "person");
      setLists(people);
      if (!a && people.length === 1) setList(people[0].id);
    });
  }, [brand, a]);
  useEffect(() => {
    if (!a && canSync.length === 1) setPicked([canSync[0].id]);
    // Only when the accounts arrive.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accounts.length]);

  async function save() {
    setBusy(true);
    const body = { name: name.trim(), list_id: list, ad_account_ids: picked, exclude };
    const { data, error } = a
      ? await api.PATCH("/v1/brands/{brand}/ads/audiences/{id}", { params: { path: { brand, id: a.id } }, body })
      : await api.POST("/v1/brands/{brand}/ads/audiences", { params: { path: { brand } }, body });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    toast(a ? "Saved. It syncs now." : picked.length > 0 ? `${name.trim()} is made. The first sync starts now.` : `${name.trim()} is made. Download its file from this page.`);
    saved();
    if (!a && "id" in data) go("ads", `audiences/${data.id}`);
  }

  const chosen = lists?.find((l) => l.id === list);
  const form = (
    <form className="max-w-xl space-y-4" onSubmit={(e) => { e.preventDefault(); save(); }}>
      <Input label="Name" hint="The list on each ad network is named this, followed by (Outflare)." required maxLength={120} value={name} onChange={(e) => setName(e.target.value)} placeholder="Paying customers" />
      <label className="flex flex-col gap-1 text-[13px]">
        <span className="font-medium">List</span>
        {!lists ? <span className="text-muted-foreground">Loading your lists…</span> : lists.length === 0 ? (
          <span className="text-muted-foreground">This brand has no lists of people yet. <button type="button" className="text-blue-700 underline-offset-2 hover:underline dark:text-blue-400" onClick={() => go("lists")}>Make a list</button>, then come back.</span>
        ) : (
          <select className={field} required value={list} onChange={(e) => setList(e.target.value)}>
            <option value="" disabled>Choose a list</option>
            {lists.map((l) => <option key={l.id} value={l.id}>{l.name} ({count(l.members)} people)</option>)}
          </select>
        )}
        <span className="text-[12px] text-muted-foreground">{chosen ? `Whoever is in ${chosen.name} is in the audience. People who join are added, people who leave are taken out.` : "Whoever is in the list is in the audience."}</span>
      </label>
      <fieldset className="space-y-2 text-[13px]">
        <legend className="font-medium">Send to</legend>
        {canSync.length === 0 && <p className="text-muted-foreground">No connected ad account can take a synced list yet. Google Ads, Meta Ads, X Ads, Reddit Ads and ChatGPT Ads can. <button type="button" className="text-blue-700 underline-offset-2 hover:underline dark:text-blue-400" onClick={() => go("ads", "accounts")}>Ad accounts</button></p>}
        {canSync.map((c) => (
          <label key={c.id} className="flex items-center gap-2">
            <input type="checkbox" checked={picked.includes(c.id)} onChange={(e) => setPicked((p) => e.target.checked ? [...p, c.id] : p.filter((x) => x !== c.id))} />
            <span>{c.connector_name}</span><span className="text-muted-foreground">{c.name}</span>
          </label>
        ))}
        {fileOnly.length > 0 && <p className="text-[12px] text-muted-foreground">{fileOnly.join(" and ")} only {fileOnly.length === 1 ? "takes" : "take"} an uploaded file. Download the audience's file from its page and upload it there.</p>}
      </fieldset>
      <fieldset className="space-y-2 text-[13px]">
        <legend className="font-medium">What campaigns do with these people</legend>
        <label className="flex items-start gap-2">
          <input type="radio" name="audience-use" className="mt-0.5" checked={!exclude} onChange={() => setExclude(false)} />
          <span><span className="font-medium">You choose</span><span className="block text-muted-foreground">The list is on each network, and you pick which campaigns target or exclude it.</span></span>
        </label>
        <label className="flex items-start gap-2">
          <input type="radio" name="audience-use" className="mt-0.5" checked={exclude} onChange={() => setExclude(true)} />
          <span><span className="font-medium">Never show them ads</span><span className="block text-muted-foreground">Outflare excludes the list on every active campaign of the accounts above, and on new campaigns within a day. Good for people who were refunded.</span></span>
        </label>
        {exclude && byHand.length > 0 && <p className="text-[12px] text-amber-700 dark:text-amber-400">{byHand.join(" and ")} can't be told to exclude a list from here. The list is still kept up to date there: exclude it on your campaigns in {byHand.join(" and ")} yourself.</p>}
        {a?.exclude && !exclude && <p className="text-[12px] text-muted-foreground">Saving takes the exclusion off the campaigns Outflare put it on.</p>}
      </fieldset>
      <div className="flex gap-2">
        <Button type="submit" disabled={busy || !name.trim() || !list}>{a ? "Save" : "Create audience"}</Button>
        {!a && <Button type="button" variant="ghost" onClick={() => go("ads", "audiences")}>Cancel</Button>}
      </div>
    </form>
  );
  if (a) return form;
  return (
    <>
      <PageHeader title="New audience" description="Choose who is in it and which ad networks get it. You can send the same audience to several." back={{ label: "Audiences", onClick: () => go("ads", "audiences") }} />
      {form}
    </>
  );
}

const size = (d: Destination) => {
  const n = Math.max(d.size.search ?? 0, d.size.display ?? 0);
  return n > 0 ? count(n) : null;
};

function AudiencePage({ brand, a, accounts, canManage, go, reload }: { brand: string; a: BrandAdAudience; accounts: Account[]; canManage: boolean; go: (tab: Tab, item?: string) => void; reload: () => void }) {
  const state = !a.list ? <Badge tone="red">List deleted</Badge> : <Badge tone={a.enabled ? "green" : "grey"}>{a.enabled ? "On" : "Off"}</Badge>;
  return (
    <>
      <PageHeader
        title={<>{a.name} {state}{a.exclude && <> <Badge tone="amber">Never shown ads</Badge></>}</>}
        description={a.list ? `Everyone in the list ${a.list.name} (${count(a.list.members)} people), updated on each network once a day.${a.exclude ? " Campaigns are told not to show to them." : ""}` : "Its list was deleted, so it no longer updates. Choose another list below."}
        back={{ label: "Audiences", onClick: () => go("ads", "audiences") }}
        action={canManage ? (
          <>
            {a.enabled && a.list && a.destinations.length > 0 && <Button variant="ghost" onClick={() => syncNow(brand, a, reload)}>Sync now</Button>}
            {a.list && <Button variant="ghost" onClick={() => download(brand, a)}>Download file</Button>}
            <Button variant="ghost" onClick={() => setEnabled(brand, a, !a.enabled, reload)}>{a.enabled ? "Turn off" : "Turn on"}</Button>
            <Menu aria="More" items={[{ label: "Delete", danger: true, onClick: () => remove(brand, a, () => { reload(); go("ads", "audiences"); }) }]} />
          </>
        ) : undefined}
      />
      <div className="space-y-4">
        <Card title="Ad networks" description="One row per ad account this audience goes to. Matched is what the network found among the people sent; networks hide the number for small lists and take a day or two to count a new one. Source: Outflare's sync record and each network.">
          {a.destinations.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">This audience goes to no ad account. Add one under Settings below, or use Download file for a network that takes an upload.</p>
          ) : (
            <DataTable
              data={a.destinations}
              rowId={(d) => d.id}
              empty="No ad account."
              columns={[
                { id: "network", header: "Network", accessorFn: (d) => d.connector_name },
                { id: "account", header: "Ad account", accessorFn: (d) => d.account_name },
                { id: "status", header: "Status", accessorFn: (d) => destLabel[d.status], cell: ({ row: { original: d } }) => (
                  <div className="max-w-[24rem] whitespace-normal">
                    <Badge tone={destTone[d.status]}>{destLabel[d.status]}</Badge>
                    {d.error && <div className="mt-1 text-[12px] text-muted-foreground">{d.error}</div>}
                  </div>
                ) },
                { id: "sent", header: "People sent", meta: { align: "right" }, accessorFn: (d) => d.members, cell: ({ row: { original: d } }) => <span className="font-mono tabular-nums">{count(d.members)}</span> },
                { id: "skipped", header: "Left out", meta: { align: "right" }, accessorFn: (d) => d.skipped, cell: ({ row: { original: d } }) => <span className="font-mono tabular-nums" title="No ad consent, or no email or phone number">{count(d.skipped)}</span> },
                { id: "matched", header: "Matched", meta: { align: "right" }, accessorFn: (d) => Math.max(d.size.search ?? 0, d.size.display ?? 0), cell: ({ row: { original: d } }) => size(d) ? <span className="font-mono tabular-nums">{size(d)}</span> : <span className="text-muted-foreground">Not reported</span> },
                { id: "rate", header: "Match rate", meta: { align: "right" }, accessorFn: (d) => d.size.match_rate ?? -1, cell: ({ row: { original: d } }) => d.size.match_rate != null ? <span className="font-mono tabular-nums">{d.size.match_rate}%</span> : <span className="text-muted-foreground">—</span> },
                ...(a.exclude ? [{ id: "excluded", header: "Excluded on", accessorFn: (d: Destination) => d.excluded_campaigns, cell: ({ row: { original: d } }: { row: { original: Destination } }) => d.can_exclude
                  ? <span className="whitespace-nowrap">{count(d.excluded_campaigns)} {d.excluded_campaigns === 1 ? "campaign" : "campaigns"}</span>
                  : <span className="text-muted-foreground" title={`Exclude this list on your campaigns in ${d.connector_name}`}>Set it in {d.connector_name}</span> }] : []),
                { id: "synced", header: "Last synced", accessorFn: (d) => d.synced_at ?? "", cell: ({ row: { original: d } }) => d.synced_at ? <span className="whitespace-nowrap">{ago(d.synced_at)}</span> : <span className="text-muted-foreground">Not yet</span> },
                { id: "list_id", header: "List id on the network", accessorFn: (d) => d.external_id, cell: ({ row: { original: d } }) => d.external_id ? <span className="font-mono text-[12px]">{d.external_id}</span> : <span className="text-muted-foreground">—</span> },
              ]}
            />
          )}
        </Card>
        {canManage && (
          <Card title="Settings" description="Change the name, the list or where it goes. Taking an ad account away leaves its list on that network as it is.">
            <AudienceForm brand={brand} accounts={accounts} a={a} go={go} saved={reload} />
          </Card>
        )}
      </div>
    </>
  );
}
