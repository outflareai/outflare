// What Google asks a full-service Google Ads tool to offer (its Required
// Minimum Functionality, docs/paid-media.md) beyond the campaign actions:
// an account's conversion actions and a new website conversion with the
// code to put on the site, the account's own callouts, its portfolio
// bidding strategies, and the bidding strategy report. Lists are read from
// Google live; each change is queued, made by a job on the workspace's own
// account and read back, and this screen waits for it.

import { useCallback, useEffect, useState } from "react";
import type { AdAccount, AdAccountChange, AdAccountConversionAction, AdBiddingStrategy, AdBiddingStrategyRow, AdCallout } from "@outflare/sdk";
import { DataTable, type ColumnMeta } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { csvMoney } from "@/lib/csv";
import { api, errorMessage } from "../../api";
import { ask, Badge, Button, Card, CopyButton, ErrorText, Input, Loading, Menu, confirmAction, field, toast } from "../../ui";
import { money, n, reportDownload } from "../Ads";

const word = (s: string) => (s ? s.charAt(0).toUpperCase() + s.slice(1).replace(/_/g, " ") : "—");

// waitForChange polls a queued account change until the job has made it
// (or failed), for up to a minute, and answers it.
async function waitForChange(brand: string, account: string, id: string): Promise<AdAccountChange | null> {
  for (let i = 0; i < 24; i++) {
    await new Promise((r) => setTimeout(r, i === 0 ? 800 : 2500));
    const { data } = await api.GET("/v1/brands/{brand}/ads/accounts/{id}/changes/{change}", { params: { path: { brand, id: account, change: id } } });
    if (data && data.status !== "queued") return data;
  }
  return null;
}

// queued handles a 202: the change, or a person's approval (an agent's).
// It tells the person what happened and answers the change once made.
async function queued(brand: string, account: string, res: { data?: unknown; error?: unknown }, what: string): Promise<AdAccountChange | null> {
  if (res.error) {
    toast(errorMessage(res.error), "error");
    return null;
  }
  const body = res.data as { change?: AdAccountChange; approval_required?: boolean } | undefined;
  if (!body?.change) {
    toast(`${what} waits for a person to approve it.`);
    return null;
  }
  toast(`${what}: sent to Google Ads.`);
  const done = await waitForChange(brand, account, body.change.id);
  if (!done) toast(`${what} is still queued. It shows here once Google Ads takes it.`);
  else if (done.status === "failed") toast(`${what} failed: ${done.error}`, "error");
  return done;
}

// ---- conversion actions (C.65) ----

const categories: { id: "purchase" | "sign_up" | "lead" | "page_view" | "contact" | "other"; label: string; counting: "one" | "many" }[] = [
  { id: "purchase", label: "Purchase", counting: "many" },
  { id: "sign_up", label: "Sign-up", counting: "one" },
  { id: "lead", label: "Lead (a form sent)", counting: "one" },
  { id: "page_view", label: "Page view", counting: "one" },
  { id: "contact", label: "Contact", counting: "one" },
  { id: "other", label: "Other", counting: "one" },
];

// Snippets shows a website conversion's code with plain instructions.
export function Snippets({ action }: { action: AdAccountConversionAction }) {
  const html = action.snippets.find((s) => s.page_format === "html") ?? action.snippets[0];
  if (!html) return <p className="text-[13px] text-muted-foreground">Google hasn't made the code for this conversion yet. Check again in a minute.</p>;
  const box = (label: string, text: string, how: string) => (
    <div className="space-y-1">
      <div className="flex items-center justify-between gap-2">
        <div className="text-[13px] font-medium">{label}</div>
        <CopyButton text={text} label={`Copy the ${label.toLowerCase()}`} />
      </div>
      <p className="text-[12px] text-muted-foreground">{how}</p>
      <pre className="max-h-40 overflow-auto rounded-md border bg-muted/40 p-2 font-mono text-[11px] whitespace-pre-wrap break-all" data-testid={label === "Global site tag" ? "global-site-tag" : "event-snippet"}>{text}</pre>
    </div>
  );
  return (
    <div className="space-y-3">
      {box("Global site tag", html.global_site_tag, "Put it on every page of your site, once, right after the <head> tag. If the Google tag is already there (from Google Analytics or another conversion), skip this one.")}
      {box("Event snippet", html.event_snippet, `Put it on the page people reach when the ${action.name} happens (the thank-you or confirmation page), after the global site tag.`)}
      <p className="text-[12px] text-muted-foreground">Google shows the conversion as unverified until the tag fires once; conversions count from then on, and show in Google Ads within about three hours.</p>
    </div>
  );
}

function NewWebConversion({ brand, account, close }: { brand: string; account: AdAccount; close: (made: AdAccountConversionAction | null) => void }) {
  const [name, setName] = useState("");
  const [category, setCategory] = useState<(typeof categories)[number]["id"]>("sign_up");
  const [counting, setCounting] = useState<"one" | "many">("one");
  const [window, setWindow] = useState("30");
  const [value, setValue] = useState("");
  const [always, setAlways] = useState(false);
  const [busy, setBusy] = useState(false);
  async function save() {
    const days = Number(window);
    const v = value.trim() === "" ? null : Number(value);
    if (!name.trim()) return toast("Give the conversion a name.", "error");
    if (!(days >= 1 && days <= 90)) return toast("The click window is 1 to 90 days.", "error");
    if (v !== null && !(v >= 0)) return toast("The value is an amount of zero or more.", "error");
    setBusy(true);
    const res = await api.POST("/v1/brands/{brand}/ads/accounts/{id}/conversion-actions", {
      params: { path: { brand, id: account.id } },
      body: { name: name.trim(), category, counting, click_window_days: days, ...(v !== null ? { default_value_micros: Math.round(v * 1e6), always_use_default: always } : {}) },
    });
    const done = await queued(brand, account.id, res, `The ${name.trim()} conversion`);
    setBusy(false);
    if (!done || done.status !== "applied") return close(null);
    const list = await api.GET("/v1/brands/{brand}/ads/accounts/{id}/conversion-actions", { params: { path: { brand, id: account.id } } });
    close(list.data?.data.find((a) => a.id === done.result.conversion_action_id) ?? null);
  }
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && close(null)}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader className="text-left">
          <DialogTitle className="text-[15px]">New website conversion</DialogTitle>
          <DialogDescription className="text-[13px]">
            A conversion action on {account.name || account.external_id} that your website counts with Google's tag. Outflare creates it, then shows the code to put on your site.
          </DialogDescription>
        </DialogHeader>
        <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); save(); }}>
          <Input label="Name" value={name} maxLength={100} onChange={(e) => setName(e.target.value)} placeholder="Sign-up" required autoFocus />
          <label className="block space-y-1 text-[13px]">
            <span className="font-medium">What it counts</span>
            <select className={`${field} w-full`} value={category} onChange={(e) => { const c = categories.find((x) => x.id === e.target.value)!; setCategory(c.id); setCounting(c.counting); }}>
              {categories.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
            </select>
          </label>
          <label className="block space-y-1 text-[13px]">
            <span className="font-medium">How many per click</span>
            <select className={`${field} w-full`} value={counting} onChange={(e) => setCounting(e.target.value as "one" | "many")}>
              <option value="one">One: count one per ad click (sign-ups, leads)</option>
              <option value="many">Every one: count each (purchases)</option>
            </select>
          </label>
          <Input label="Click window, days" type="number" min={1} max={90} value={window} onChange={(e) => setWindow(e.target.value)} hint="How long after an ad click a conversion still counts. 30 is Google's default." />
          <Input label={`Value, ${account.currency}`} type="number" min={0} step="0.01" value={value} onChange={(e) => setValue(e.target.value)} placeholder="Optional" hint="What one conversion is worth when the page sends no value." />
          {value.trim() !== "" && (
            <label className="flex items-center gap-2 text-[13px]">
              <input type="checkbox" checked={always} onChange={(e) => setAlways(e.target.checked)} />
              Always use this value, whatever the page sends
            </label>
          )}
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" disabled={busy} onClick={() => close(null)}>Cancel</Button>
            <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create and show the code"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function GoogleConversionActions({ brand, account, canManage }: { brand: string; account: AdAccount; canManage: boolean }) {
  const [rows, setRows] = useState<AdAccountConversionAction[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [shown, setShown] = useState<AdAccountConversionAction | null>(null);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/accounts/{id}/conversion-actions", { params: { path: { brand, id: account.id } } });
    setError(error ? errorMessage(error) : "");
    setRows(data?.data ?? []);
  }, [brand, account.id]);
  useEffect(() => {
    load();
  }, [load]);
  return (
    <Card
      title={`Conversion actions · ${account.name || account.external_id}`}
      description="What this Google Ads account counts as a conversion, read from Google now. Primary ones are what bidding uses; the others count in all conversions. A website conversion counts with a tag on your site."
      action={canManage ? <Button variant="ghost" onClick={() => setAdding(true)}>New website conversion</Button> : undefined}
    >
      {error && <ErrorText>{error}</ErrorText>}
      {!rows ? <Loading /> : (
        <DataTable
          data={rows}
          rowId={(r) => r.id}
          columnsMenu={false}
          download={reportDownload("conversion_actions", account.name || account.external_id)}
          empty="No conversion actions on this account yet."
          mobile={(r) => (
            <span className="flex min-w-0 items-center justify-between gap-2">
              <span className="min-w-0 truncate"><span className="font-medium">{r.name}</span> · {word(r.category)} · {r.primary ? "Primary" : "Secondary"}</span>
              {r.type === "webpage" && <Button variant="ghost" onClick={() => setShown(r)}>Show the code</Button>}
            </span>
          )}
          columns={[
            { id: "name", header: "Conversion action", accessorFn: (r) => r.name, cell: ({ row: { original: r } }) => <span className="font-medium">{r.name}</span> },
            { id: "type", header: "Type", accessorFn: (r) => word(r.type) },
            { id: "category", header: "Category", accessorFn: (r) => word(r.category) },
            { id: "status", header: "Status", accessorFn: (r) => word(r.status) },
            { id: "primary", header: "Bidding uses it", accessorFn: (r) => (r.primary ? "Primary" : "Secondary") },
            { id: "counting", header: "Counts", accessorFn: (r) => (r.counting === "many" ? "Every one" : r.counting === "one" ? "One per click" : "—") },
            { id: "window", header: "Click window", accessorFn: (r) => r.click_window_days, cell: ({ row: { original: r } }) => `${r.click_window_days} days` },
            {
              id: "code", header: "", enableSorting: false, meta: { csv: false } satisfies ColumnMeta<AdAccountConversionAction>,
              cell: ({ row: { original: r } }) => r.type === "webpage" && <Button variant="ghost" onClick={() => setShown(r)}>Show the code</Button>,
            },
          ]}
        />
      )}
      {adding && <NewWebConversion brand={brand} account={account} close={(made) => { setAdding(false); load(); if (made) setShown(made); }} />}
      {shown && (
        <Dialog open onOpenChange={(o) => !o && setShown(null)}>
          <DialogContent className="sm:max-w-2xl">
            <DialogHeader className="text-left">
              <DialogTitle className="text-[15px]">Put {shown.name} on your site</DialogTitle>
              <DialogDescription className="text-[13px]">Two pieces of code from Google Ads. Paste them as they are; your developer or your site builder's custom code setting can add them.</DialogDescription>
            </DialogHeader>
            <Snippets action={shown} />
          </DialogContent>
        </Dialog>
      )}
    </Card>
  );
}

// ---- callouts (C.75) ----

export function GoogleCallouts({ brand, account, canManage }: { brand: string; account: AdAccount; canManage: boolean }) {
  const [rows, setRows] = useState<AdCallout[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/accounts/{id}/callouts", { params: { path: { brand, id: account.id } } });
    setError(error ? errorMessage(error) : "");
    setRows(data?.data ?? []);
  }, [brand, account.id]);
  useEffect(() => {
    load();
  }, [load]);
  async function add() {
    const r = await ask({
      title: "Add a callout",
      body: "A short line, up to 25 characters, Google can show under any ad of this account, such as Free shipping or 24/7 support.",
      input: { label: "Callout", maxLength: 25, placeholder: "Free 14-day trial" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Add", value: "ok", variant: "primary" }],
    });
    if (r?.choice !== "ok" || !r.value.trim()) return;
    setBusy(true);
    await queued(brand, account.id, await api.POST("/v1/brands/{brand}/ads/accounts/{id}/callouts", { params: { path: { brand, id: account.id } }, body: { text: r.value.trim() } }), "The callout");
    setBusy(false);
    load();
  }
  async function remove(c: AdCallout) {
    if (!(await confirmAction(`Remove "${c.text}"?`, "It stops showing under this account's ads. You can add it again later.", "Remove callout"))) return;
    setBusy(true);
    await queued(brand, account.id, await api.DELETE("/v1/brands/{brand}/ads/accounts/{id}/callouts/{asset}", { params: { path: { brand, id: account.id, asset: c.id } } }), "Removing the callout");
    setBusy(false);
    load();
  }
  return (
    <Card title={`Callouts · ${account.name || account.external_id}`} description="Short lines Google can show under every ad of this account, read from Google now. Up to 25 characters each."
      action={canManage ? <Button variant="ghost" disabled={busy} onClick={add}>Add callout</Button> : undefined}>
      {error && <ErrorText>{error}</ErrorText>}
      {!rows ? <Loading /> : rows.length === 0 ? <p className="text-[13px] text-muted-foreground">No account callouts yet.</p> : (
        <div className="flex flex-wrap gap-1.5">
          {rows.map((c) => (
            <Badge key={c.id} tone="grey">
              {c.text}
              {canManage && <button className="ml-1 text-muted-foreground hover:text-foreground" disabled={busy} title="Remove it" aria-label={`Remove ${c.text}`} onClick={() => remove(c)}>×</button>}
            </Badge>
          ))}
        </div>
      )}
    </Card>
  );
}

// ---- portfolio bidding strategies (C.96, C.97, M.96, M.97) ----

export const strategyWord: Record<string, string> = {
  target_cpa: "Target CPA", target_roas: "Target ROAS", maximize_conversions: "Maximise conversions", maximize_conversion_value: "Maximise conversion value",
  target_spend: "Maximise clicks", target_impression_share: "Target impression share",
};
const targetOf = (s: AdBiddingStrategy) =>
  s.target_cpa_micros != null ? `${money(s.target_cpa_micros, s.currency || "USD")} a conversion` : s.target_roas != null ? `${s.target_roas}× return` : "No target";

// askTarget asks for a target cost per conversion or return on spend.
async function askTarget(title: string, roas: boolean, currency: string, current?: number | null) {
  const r = await ask({
    title,
    body: roas ? "A return on spend: 3 means three times what is spent. Lowering it lets Google spend more." : `A cost per conversion in ${currency}. Raising it lets Google spend more.`,
    input: { label: roas ? "Target return" : "Target cost per conversion", type: "number", placeholder: roas ? "3" : "25.00", value: current == null ? "" : roas ? String(current) : (current / 1e6).toFixed(2) },
    choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok", variant: "primary" }],
  });
  const v = Number(r?.value);
  if (r?.choice !== "ok" || !(v > 0)) return null;
  return roas ? { target_roas: v } : { target_cpa_micros: Math.round(v * 1e6) };
}

export function GooglePortfolios({ brand, account, canManage }: { brand: string; account: AdAccount; canManage: boolean }) {
  const [rows, setRows] = useState<AdBiddingStrategy[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/accounts/{id}/bidding-strategies", { params: { path: { brand, id: account.id } } });
    setError(error ? errorMessage(error) : "");
    setRows(data?.data ?? []);
  }, [brand, account.id]);
  useEffect(() => {
    load();
  }, [load]);
  async function create() {
    const name = await ask({
      title: "New portfolio bidding strategy",
      body: "One target several campaigns share. Target CPA bids to a cost per conversion; Target ROAS to a return on spend. Put campaigns on it from each campaign's Targeting.",
      input: { label: "Name", maxLength: 255, placeholder: "Leads at $40" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Target ROAS", value: "target_roas", variant: "ghost" }, { label: "Target CPA", value: "target_cpa", variant: "primary" }],
    });
    if (!name || name.choice === "cancel" || !name.value.trim()) return;
    const type = name.choice as "target_cpa" | "target_roas";
    const target = await askTarget(`${strategyWord[type]} for ${name.value.trim()}`, type === "target_roas", account.currency);
    if (!target) return;
    setBusy(true);
    await queued(brand, account.id, await api.POST("/v1/brands/{brand}/ads/accounts/{id}/bidding-strategies", { params: { path: { brand, id: account.id } }, body: { name: name.value.trim(), type, ...target } }), "The strategy");
    setBusy(false);
    load();
  }
  async function retarget(s: AdBiddingStrategy) {
    const roas = s.target_roas != null || s.type === "target_roas" || s.type === "maximize_conversion_value";
    const target = await askTarget(`New target for ${s.name}`, roas, s.currency || account.currency, roas ? s.target_roas : s.target_cpa_micros);
    if (!target) return;
    setBusy(true);
    await queued(brand, account.id, await api.PATCH("/v1/brands/{brand}/ads/accounts/{id}/bidding-strategies/{strategy}", { params: { path: { brand, id: account.id, strategy: s.id } }, body: target }), "The new target");
    setBusy(false);
    load();
  }
  return (
    <Card title={`Portfolio bidding strategies · ${account.name || account.external_id}`} description="Targets several campaigns share, read from Google now. Every campaign on a strategy bids to its target."
      action={canManage ? <Button variant="ghost" disabled={busy} onClick={create}>New strategy</Button> : undefined}>
      {error && <ErrorText>{error}</ErrorText>}
      {!rows ? <Loading /> : (
        <DataTable
          data={rows}
          rowId={(r) => r.id}
          columnsMenu={false}
          empty="No portfolio strategies on this account."
          mobile={(r) => <span className="block truncate"><span className="font-medium">{r.name}</span> · {targetOf(r)} · {r.campaign_count} campaigns</span>}
          columns={[
            { id: "name", header: "Strategy", accessorFn: (r) => r.name, cell: ({ row: { original: r } }) => <span className="font-medium">{r.name}</span> },
            { id: "type", header: "Type", accessorFn: (r) => strategyWord[r.type] ?? word(r.type) },
            { id: "status", header: "Status", accessorFn: (r) => word(r.status) },
            { id: "target", header: "Target", accessorFn: (r) => targetOf(r) },
            { id: "campaigns", header: "Campaigns", accessorFn: (r) => r.campaign_count },
            {
              id: "manage", header: "", enableSorting: false,
              cell: ({ row: { original: r } }) => canManage && r.status !== "removed" && (r.target_cpa_micros != null || r.target_roas != null) && (
                <Menu label="Manage" aria={`Manage ${r.name}`} items={[{ label: "Change target", onClick: () => retarget(r) }]} />
              ),
            },
          ]}
        />
      )}
    </Card>
  );
}

// ---- the bidding strategy report (R.130) ----

export function BiddingStrategyReport({ brand, from, to, account }: { brand: string; from: string; to: string; account?: string }) {
  const [rows, setRows] = useState<AdBiddingStrategyRow[] | null>(null);
  const [errors, setErrors] = useState<string[]>([]);
  useEffect(() => {
    let live = true;
    api.GET("/v1/brands/{brand}/ads/bidding-strategies", { params: { path: { brand }, query: { from, to, ...(account ? { account_id: account } : {}) } } }).then(({ data, error }) => {
      if (!live) return;
      setErrors(error ? [errorMessage(error)] : (data?.errors ?? []).map((e) => e.message));
      setRows(data?.data ?? []);
    });
    return () => {
      live = false;
    };
  }, [brand, from, to, account]);
  if (!rows || (rows.length === 0 && errors.length === 0)) return null;
  const cur = (r: AdBiddingStrategyRow) => r.currency || "USD";
  const mono = (s: string) => <span className="font-mono tabular-nums">{s}</span>;
  return (
    <Card title="Bidding strategies" description={`Each portfolio bidding strategy from ${from} to ${to}, read from Google Ads now: what its campaigns spent and got together. Average CPC and cost per conversion are Google's own.`}>
      {errors.map((e) => <ErrorText key={e}>{e}</ErrorText>)}
      <DataTable
        data={rows}
        rowId={(r) => `${r.ad_account_id}|${r.id}`}
        columnsMenu={false}
        download={reportDownload("bidding_strategies", from, to)}
        initialSort={[{ id: "spend", desc: true }]}
        empty="No portfolio strategies."
        mobile={(r) => (
          <span className="block min-w-0">
            <span className="block truncate font-medium">{r.name}</span>
            <span className="block truncate text-[12px] text-muted-foreground">{strategyWord[r.type] ?? word(r.type)} · {money(r.spend_micros, cur(r))} · {n(r.clicks)} clicks · {n(r.conversions)} conv.</span>
          </span>
        )}
        columns={[
          { id: "name", header: "Strategy", accessorFn: (r) => r.name, cell: ({ row: { original: r } }) => <span className="font-medium">{r.name}</span> },
          { id: "account", header: "Account", accessorFn: (r) => r.account_name },
          { id: "type", header: "Type", accessorFn: (r) => strategyWord[r.type] ?? word(r.type) },
          { id: "status", header: "Status", accessorFn: (r) => word(r.status) },
          { id: "clicks", header: "Clicks", accessorFn: (r) => r.clicks, cell: ({ row: { original: r } }) => mono(n(r.clicks)) },
          { id: "impressions", header: "Impressions", accessorFn: (r) => r.impressions, cell: ({ row: { original: r } }) => mono(n(r.impressions)) },
          { id: "spend", header: "Cost", accessorFn: (r) => r.spend_micros, cell: ({ row: { original: r } }) => mono(money(r.spend_micros, cur(r))), meta: { csv: (r: AdBiddingStrategyRow) => csvMoney(r.spend_micros, cur(r)) } },
          { id: "cpc", header: "Avg. CPC", accessorFn: (r) => r.cpc_micros ?? -1, cell: ({ row: { original: r } }) => mono(money(r.cpc_micros, cur(r))), meta: { csv: (r: AdBiddingStrategyRow) => csvMoney(r.cpc_micros, cur(r)) } },
          { id: "conversions", header: "Conv.", accessorFn: (r) => r.conversions, cell: ({ row: { original: r } }) => mono(n(r.conversions)), meta: { csv: (r: AdBiddingStrategyRow) => r.conversions } },
          { id: "all_conversions", header: "All conv.", accessorFn: (r) => r.all_conversions, cell: ({ row: { original: r } }) => mono(n(r.all_conversions)) },
          { id: "cpa", header: "Cost / conv.", accessorFn: (r) => r.cost_per_conversion_micros ?? -1, cell: ({ row: { original: r } }) => mono(money(r.cost_per_conversion_micros, cur(r))), meta: { csv: (r: AdBiddingStrategyRow) => csvMoney(r.cost_per_conversion_micros, cur(r)) } },
        ]}
      />
    </Card>
  );
}

// GoogleAccountTools are the account-level cards for each of the brand's
// Google Ads accounts: callouts and portfolio strategies.
export function GoogleAccountTools({ brand, accounts, canManage }: { brand: string; accounts: AdAccount[]; canManage: boolean }) {
  const google = accounts.filter((a) => a.connector === "google_ads" && a.status === "active" && a.brand_id === brand);
  return (
    <>
      {google.map((a) => (
        <div key={a.id} className="space-y-6">
          <GoogleCallouts brand={brand} account={a} canManage={canManage} />
          <GooglePortfolios brand={brand} account={a} canManage={canManage} />
        </div>
      ))}
    </>
  );
}

