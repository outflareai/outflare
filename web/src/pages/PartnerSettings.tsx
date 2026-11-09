// A partner program's settings, as a page of its own: how partners are
// credited, how they're paid, where sales come from, fraud protection, the
// portal, the install and the Tolt import, then pausing or archiving.

import { createContext, useCallback, useContext, useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { PartnerAsset, PartnerImport, PartnerProgram } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { pickedBrandId } from "@/lib/brand";
import { appPath } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { Badge, Button, CopyButton, ErrorText, field, Input, PageHeader, ask, toast } from "../ui";
import { ago, plural } from "../format";
import { Switch } from "@/components/ui/switch";
import { PartnerInstall } from "./PartnerInstall";
import { PartnerPortalBrand } from "./PartnerPortalBrand";
import { payoutMethods } from "./Partners";

type PayoutMethod = PartnerProgram["payout_methods"][number];

// The tabs, each one topic, and the draft fields each holds so a tab can
// show it has unsaved changes. The tab is the last part of the URL:
// /partners/settings/payouts.
export const settingsTabs = [
  { id: "general", label: "General", fields: ["landing", "urlParams", "windowDays", "model", "leadDays", "accept"] },
  { id: "payouts", label: "Payouts", fields: ["hold", "auto", "minPayout", "term", "methods", "tokens"] },
  { id: "tracking", label: "Tracking", fields: ["salesSource", "payBrowser"] },
  { id: "fraud", label: "Fraud", fields: ["blockSelf", "blockAds", "blockSocial", "countries"] },
  { id: "portal", label: "Portal", fields: [] },
  { id: "import", label: "Import", fields: [] },
] as const satisfies readonly { id: string; label: string; fields: readonly (keyof Draft)[] }[];
export type SettingsTab = (typeof settingsTabs)[number]["id"];

// Which tab each section card sits on.
const sectionTab: Record<string, SettingsTab> = { credit: "general", applications: "general", danger: "general", payouts: "payouts", sales: "tracking", install: "tracking", fraud: "fraud", portal: "portal", import: "import" };
const TabContext = createContext<SettingsTab>("general");

const terms = [0, 15, 30, 45, 60, 90];

// The editable program settings as the form holds them (numbers as typed).
type Draft = {
  landing: string; urlParams: string; windowDays: string; model: "first" | "last"; leadDays: string; accept: boolean;
  hold: string; auto: boolean; minPayout: string; term: string; methods: PayoutMethod[]; tokens: string;
  salesSource: PartnerProgram["sales_source"]; payBrowser: boolean;
  blockSelf: boolean; blockAds: boolean; blockSocial: boolean; countries: string;
};

function draftOf(p: PartnerProgram): Draft {
  return {
    landing: p.landing_url, urlParams: p.url_params.join(", "), windowDays: String(p.credit_window_days), model: p.credit_model,
    leadDays: p.lead_expiry_days === null ? "" : String(p.lead_expiry_days), accept: p.auto_accept,
    hold: String(p.hold_days), auto: p.auto_approve, minPayout: String(p.min_payout_cents / 100),
    term: p.payout_term_days === null ? "" : String(p.payout_term_days), methods: p.payout_methods, tokens: p.payout_tokens.join(", "),
    salesSource: p.sales_source, payBrowser: p.pay_browser_signups,
    blockSelf: p.block_self_referrals, blockAds: p.block_ad_clicks, blockSocial: p.block_social_clicks, countries: p.blocked_countries.join(", "),
  };
}

const list = (s: string, f: (x: string) => string = (x) => x) => s.split(",").map((x) => f(x.trim())).filter(Boolean);

export function ProgramSettings({ ws, program: p, programs, canManage, tab, onTab, onPick, onChanged, back, openFlows }: {
  ws: string; program: PartnerProgram; programs: PartnerProgram[]; canManage: boolean; tab: SettingsTab; onTab: (t: SettingsTab) => void;
  onPick: (id: string) => void; onChanged: () => void; back: () => void; openFlows: () => void;
}) {
  const saved = draftOf(p);
  const [d, setD] = useState<Draft>(saved);
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((cur) => ({ ...cur, [k]: v }));
  const dirty = JSON.stringify(d) !== JSON.stringify(saved);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [postback, setPostback] = useState("");

  // Warn before leaving the page with unsaved changes.
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  async function save(e?: FormEvent) {
    e?.preventDefault();
    setBusy(true);
    setError("");
    const { error } = await api.PATCH("/v1/brands/{brand}/partner-programs/{id}", {
      params: { path: { brand: pickedBrandId(), id: p.id } },
      body: {
        landing_url: d.landing.trim(), credit_window_days: Number(d.windowDays), credit_model: d.model, hold_days: Number(d.hold), auto_approve: d.auto, auto_accept: d.accept,
        pay_browser_signups: d.payBrowser, block_self_referrals: d.blockSelf, min_payout_cents: Math.round(Number(d.minPayout) * 100), sales_source: d.salesSource,
        payout_term_days: d.term === "" ? null : Number(d.term), payout_methods: d.methods,
        payout_tokens: d.methods.includes("crypto") ? list(d.tokens) : p.payout_tokens,
        lead_expiry_days: d.leadDays.trim() === "" ? null : Number(d.leadDays),
        url_params: list(d.urlParams, (x) => x.toLowerCase()), block_ad_clicks: d.blockAds, block_social_clicks: d.blockSocial,
        blocked_countries: list(d.countries, (x) => x.toUpperCase()),
      },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast("Settings saved.");
    onChanged();
  }
  async function setStatus(status: PartnerProgram["status"]) {
    if (status === "archived") {
      const r = await ask({
        title: `Archive ${p.name}?`,
        body: "It leaves the Partners page and stops crediting anyone. Partners' links still land on your site. Past rewards and payouts are kept.",
        choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Archive", value: "ok", variant: "danger-solid" }],
      });
      if (r?.choice !== "ok") return;
    }
    const { error } = await api.PATCH("/v1/brands/{brand}/partner-programs/{id}", { params: { path: { brand: pickedBrandId(), id: p.id } }, body: { status } });
    if (error) return toast(errorMessage(error), "error");
    toast(status === "active" ? "Program resumed." : status === "paused" ? "Paused: nothing new is credited; links still land." : "Archived.");
    onChanged();
    if (status === "archived") back();
  }
  async function makePostback() {
    if (p.postback_hint) {
      const r = await ask({
        title: "Make a new postback URL?",
        body: "The current one stops working at once, so update every tracker or backend that calls it.",
        choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Make a new one", value: "ok" }],
      });
      if (r?.choice !== "ok") return;
    }
    const { data, error } = await api.POST("/v1/brands/{brand}/partner-programs/{id}/postback", { params: { path: { brand: pickedBrandId(), id: p.id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    setPostback(data.url);
  }

  const status = p.status === "paused" ? <Badge tone="amber">Paused</Badge> : <Badge tone="green">Active</Badge>;
  return (
    <div className="pb-24">
      <PageHeader
        back={{ label: "Partners", onClick: back }}
        title={<>{p.name} settings {status}</>}
        description="How this program credits partners, pays them and keeps out fraud. These settings belong to this program only; they never follow the workspace's attribution settings."
        action={programs.length > 1 && (
          <select aria-label="Program" className={field} value={p.id} onChange={(e) => onPick(e.target.value)} disabled={dirty} title={dirty ? "Save or discard your changes first" : undefined}>
            {programs.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
          </select>
        )}
      />
      <div className="grid gap-6 lg:grid-cols-[180px_minmax(0,1fr)] lg:gap-8">
        <nav aria-label="Settings" className="-mx-4 overflow-x-auto px-4 lg:mx-0 lg:overflow-visible lg:px-0">
          <ul className="flex gap-1 border-b pb-2 text-[13px] lg:sticky lg:top-4 lg:flex-col lg:gap-0.5 lg:border-b-0 lg:pb-0">
            {settingsTabs.map((t) => {
              const changed = t.fields.some((k) => JSON.stringify(d[k]) !== JSON.stringify(saved[k]));
              return (
                <li key={t.id} className="shrink-0">
                  <button type="button" onClick={() => onTab(t.id)} aria-current={tab === t.id ? "page" : undefined}
                    className={cn("flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left whitespace-nowrap text-muted-foreground hover:bg-muted hover:text-foreground", tab === t.id && "bg-muted font-medium text-foreground")}>
                    {t.label}
                    {t.id === "portal" && <span className={cn("size-1.5 rounded-full", p.portal_enabled ? "bg-emerald-500" : "bg-muted-foreground/40")} title={p.portal_enabled ? "Open" : "Closed"} />}
                    {changed && <span className="ml-auto text-[11px] text-amber-600 dark:text-amber-400" title="Unsaved changes">●</span>}
                  </button>
                </li>
              );
            })}
          </ul>
        </nav>
        <TabContext.Provider value={tab}>
        <div className="flex min-w-0 max-w-5xl flex-col gap-6">
          <form id="program-settings" onSubmit={save} className="contents">
            <fieldset disabled={!canManage} className="contents">
              <Section id="credit" title="Links and credit" description="Where partners' links send people, and which partner gets the credit for a sale.">
                <Input label="Landing page" type="url" value={d.landing} onChange={(e) => set("landing", e.target.value)} placeholder="https://example.com/pricing" maxLength={2000}
                  hint="Where a partner's link sends visitors. Their code is added to it as ?ref=." />
                <div className="grid gap-4 sm:grid-cols-2">
                  <Input label="Credit window (days)" type="number" min={1} max={365} value={d.windowDays} onChange={(e) => set("windowDays", e.target.value)} hint="How long after clicking a link a sign-up or sale still counts." />
                  <Select label="When two partners sent the same visitor" value={d.model} onChange={(v) => set("model", v as Draft["model"])} hint="Who gets the credit.">
                    <option value="last">The last partner gets it</option>
                    <option value="first">The first partner gets it</option>
                  </Select>
                  <Input label="Lead expiry (days)" type="number" min={1} max={3650} value={d.leadDays} onChange={(e) => set("leadDays", e.target.value)} placeholder="Never"
                    hint="A referred sign-up who hasn't bought by then no longer earns the partner anything. Empty: never expires." />
                  <Input label="Other link parameters" value={d.urlParams} onChange={(e) => set("urlParams", e.target.value)} placeholder="aff, fpr" maxLength={200}
                    hint="Besides ref and via, comma separated. Use it when links made on another platform carry the code in a different parameter." />
                </div>
              </Section>
              <Section id="applications" title="New partners" description="What happens when someone applies through the portal or a sign-up link.">
                <Rows>
                  <Toggle label="Review each application" help={d.accept ? "Off: everyone who applies becomes a partner straight away." : "Applications wait on the Partners page, marked pending, until you approve or reject them."} checked={!d.accept} onChange={(v) => set("accept", !v)} />
                </Rows>
              </Section>
            </fieldset>
          </form>

          <Section id="portal" title="Partner portal" action={p.portal_enabled ? <Badge tone="green">Open</Badge> : <Badge tone="grey">Closed</Badge>}
            description={p.portal_enabled ? <>Live at <a className="underline underline-offset-2" href={p.portal_url} target="_blank" rel="noreferrer">{p.portal_url}</a></> : "Where partners apply, sign in and see their links, rewards and payouts. It has its own Save button."}>
            <PortalSection ws={ws} program={p} canManage={canManage} onSaved={onChanged} />
          </Section>

          <fieldset disabled={!canManage} className="contents">
            <Section id="payouts" title="Rewards and payouts" description="When rewards are approved and how partners are paid. You pay partners from your own account; Outflare keeps the ledger.">
              <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border bg-muted/30 px-3 py-2 text-[12px]">
                <span className="text-muted-foreground">How much partners earn per sign-up or sale is set in Flows.</span>
                <Button type="button" variant="ghost" onClick={openFlows}>Open Flows</Button>
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <Input label="Hold (days)" type="number" min={0} max={365} value={d.hold} onChange={(e) => set("hold", e.target.value)} hint="A refund during the hold cancels the reward. 0: no hold." />
                <Input label={`Minimum payout (${p.currency})`} type="number" min={0} step="0.01" value={d.minPayout} onChange={(e) => set("minPayout", e.target.value)} hint="Partners are paid once their approved rewards reach this." />
                <Select label="When partners are paid" value={d.term} onChange={(v) => set("term", v)}>
                  <option value="">As soon as rewards are approved</option>
                  {terms.map((n) => <option key={n} value={String(n)}>{n === 0 ? "When the month ends (Net 0)" : `${n} days after the month ends (Net ${n})`}</option>)}
                  {d.term !== "" && !terms.includes(Number(d.term)) && <option value={d.term}>Net {d.term}</option>}
                </Select>
              </div>
              <Rows>
                <Toggle label="Approve rewards on their own" help="When the hold ends, rewards are approved without you checking each one." checked={d.auto} onChange={(v) => set("auto", v)} />
              </Rows>
              <div className="space-y-2">
                <div>
                  <div className="text-[13px] font-medium">Payout methods partners can choose</div>
                  <div className="text-[12px] text-muted-foreground">None ticked: partners describe any method in their own words.</div>
                </div>
                <div className="flex flex-wrap gap-2">
                  {payoutMethods.map(([m, label]) => (
                    <label key={m} className={cn("flex cursor-pointer items-center gap-2 rounded-md border px-3 py-1.5 text-[13px]", d.methods.includes(m) && "border-foreground/40 bg-muted")}>
                      <input type="checkbox" checked={d.methods.includes(m)} onChange={(e) => set("methods", e.target.checked ? [...d.methods, m] : d.methods.filter((x) => x !== m))} /> {label}
                    </label>
                  ))}
                </div>
                {d.methods.includes("crypto") && (
                  <Input label="Crypto tokens" value={d.tokens} onChange={(e) => set("tokens", e.target.value)} placeholder="USDC on Base, USDT on Tron" hint="Comma separated. Empty: any token." maxLength={1000} />
                )}
              </div>
            </Section>

            <Section id="sales" title="Where sales come from" description="Your backend can always report sign-ups and sales (see Install below). Turn on more sources here; each sale is counted once.">
              <Rows>
                <Toggle label="Stripe" help="Take sales and refunds from your connected Stripe account." checked={d.salesSource === "stripe"} onChange={(v) => set("salesSource", v ? "stripe" : "")} />
                {p.brand_id && (
                  <Toggle label="This brand's payments in Outflare" help="Take payments and refunds already sent to Outflare (the transactions API and Order Completed events)." checked={d.salesSource === "transactions"} onChange={(v) => set("salesSource", v ? "transactions" : "")} />
                )}
                <Toggle label="Pay for sign-ups reported by the browser" help="Sign-ups from outflare.signup() on your site. Off: they still credit the partner with the lead, but earn no sign-up reward, since anyone can call it." checked={d.payBrowser} onChange={(v) => set("payBrowser", v)} />
              </Rows>
              <div className="space-y-2 border-t pt-4">
                <div>
                  <div className="text-[13px] font-medium">Postback URL</div>
                  <p className="text-[12px] text-muted-foreground">For trackers and backends that can only call a URL, such as <code className="font-mono">?type=sale&amp;ref=…&amp;user_id=…&amp;amount=49.90&amp;order_id=…</code>. Anyone with it can report sales, so keep it private.</p>
                </div>
                {postback ? (
                  <div className="space-y-2 rounded-md border p-3">
                    <p className="font-mono text-[12px] break-all">{postback}</p>
                    <div className="flex flex-wrap items-center gap-2"><CopyButton text={postback} label="Copy URL" /><span className="text-[12px] text-muted-foreground">Copy it now: it's shown only once.</span></div>
                  </div>
                ) : (
                  <div className="flex flex-wrap items-center gap-2">
                    {p.postback_hint ? <span className="text-[12px] text-muted-foreground">Current URL starts <code className="font-mono">{p.postback_hint}</code></span> : <span className="text-[12px] text-muted-foreground">No postback URL yet.</span>}
                    {canManage && <Button type="button" variant="ghost" onClick={makePostback}>{p.postback_hint ? "Make a new one" : "Make a postback URL"}</Button>}
                  </div>
                )}
              </div>
            </Section>

            <Section id="fraud" title="Fraud protection" description="Visits and customers these rules catch never earn a partner anything.">
              <Rows>
                <Toggle label="Block self-referrals" help="A partner's own sign-ups and purchases earn them nothing." checked={d.blockSelf} onChange={(v) => set("blockSelf", v)} />
                <Toggle label="Ignore visits from paid ads" help="Stops partners bidding on your brand name in search ads to take credit." checked={d.blockAds} onChange={(v) => set("blockAds", v)} />
                <Toggle label="Ignore visits from social media" help="Only links on partners' own sites, emails and videos earn credit." checked={d.blockSocial} onChange={(v) => set("blockSocial", v)} />
              </Rows>
              <div>
                <Input label="Blocked countries" value={d.countries} onChange={(e) => set("countries", e.target.value)} placeholder="BR, IN" maxLength={1000}
                  hint="Two-letter country codes, comma separated. Visits and new customers from these countries don't credit partners. The country comes from the visitor's address, never the time zone." />
                <p className="mt-1 text-[11px] text-muted-foreground"><a href="https://db-ip.com" target="_blank" rel="noreferrer" className="underline underline-offset-2">IP geolocation by DB-IP</a></p>
              </div>
            </Section>
          </fieldset>

          <Section id="install" title="Install on your website" description="Two steps so partners get credit: the script on your site, then your backend reporting sign-ups and sales.">
            <PartnerInstall ws={ws} program={p} />
          </Section>

          <Section id="import" title="Import from Tolt" description="Moving from Tolt? Bring partners, links and history over. Uses your own Tolt API key.">
            <ImportSection ws={ws} program={p} canManage={canManage} />
          </Section>

          {canManage && (
            <Section id="danger" title="Pause or archive" danger>
              <Rows>
                <Row label={p.status === "paused" ? "Resume the program" : "Pause the program"}
                  help={p.status === "paused" ? "Start crediting partners again." : "Nothing new is credited while paused. Links still land on your site, and partners can still sign in."}>
                  {p.status === "paused" ? <Button type="button" variant="ghost" onClick={() => setStatus("active")}>Resume</Button> : <Button type="button" variant="ghost" onClick={() => setStatus("paused")}>Pause</Button>}
                </Row>
                <Row label="Archive the program" help="Removes it from the Partners page and stops all crediting. Past rewards and payouts are kept.">
                  <Button type="button" variant="danger" onClick={() => setStatus("archived")}>Archive</Button>
                </Row>
              </Rows>
            </Section>
          )}
        </div>
        </TabContext.Provider>
      </div>

      {canManage && dirty && (
        <div role="region" aria-label="Unsaved changes" className="fixed inset-x-0 bottom-0 z-40 border-t bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/80">
          <div className="mx-auto flex max-w-5xl flex-wrap items-center justify-end gap-2 px-4 py-3">
            <ErrorText>{error}</ErrorText>
            <span className="mr-auto text-[13px] text-muted-foreground">You have unsaved changes.</span>
            <Button type="button" variant="ghost" onClick={() => { setD(saved); setError(""); }} disabled={busy}>Discard</Button>
            <Button type="submit" form="program-settings" disabled={busy}>{busy ? "Saving…" : "Save changes"}</Button>
          </div>
        </div>
      )}
    </div>
  );
}

// Section is one card of the settings page. Cards on other tabs stay mounted
// but hidden, so what's typed in them survives switching tabs.
function Section({ id, title, description, action, danger = false, children }: { id: string; title: string; description?: ReactNode; action?: ReactNode; danger?: boolean; children: ReactNode }) {
  const tab = useContext(TabContext);
  return (
    <section id={id} hidden={sectionTab[id] !== tab} aria-labelledby={`${id}-title`} className={cn("scroll-mt-4 rounded-lg border bg-card text-card-foreground shadow-xs", danger && "border-destructive/40")}>
      <header className="flex flex-wrap items-start justify-between gap-2 border-b px-4 py-3">
        <div className="min-w-0">
          <h2 id={`${id}-title`} className={cn("text-[14px] font-semibold", danger && "text-destructive")}>{title}</h2>
          {description && <p className="mt-0.5 text-[12px] text-muted-foreground">{description}</p>}
        </div>
        {action}
      </header>
      <div className="space-y-4 p-4">{children}</div>
    </section>
  );
}

function Rows({ children }: { children: ReactNode }) {
  return <div className="divide-y rounded-md border">{children}</div>;
}

function Row({ label, help, children }: { label: string; help?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 px-3 py-2.5">
      <div className="min-w-0">
        <div className="text-[13px] font-medium">{label}</div>
        {help && <div className="text-[12px] text-muted-foreground">{help}</div>}
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}

// Toggle is a setting that is on or off: what it does on the left, a switch
// on the right.
function Toggle({ label, help, checked, onChange }: { label: string; help?: ReactNode; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <Row label={label} help={help}>
      <Switch aria-label={label} checked={checked} onCheckedChange={onChange} />
    </Row>
  );
}

function Select({ label, hint, value, onChange, children }: { label: string; hint?: string; value: string; onChange: (v: string) => void; children: ReactNode }) {
  return (
    <label className="block text-sm">
      <span className="mb-1.5 block text-[13px] font-medium">{label}</span>
      <select className={`${field} w-full`} value={value} onChange={(e) => onChange(e.target.value)}>{children}</select>
      {hint && <span className="mt-1 block text-xs text-muted-foreground">{hint}</span>}
    </label>
  );
}

// What an import run's issue codes mean, in words.
const importReasons: Record<string, string> = {
  incomplete: "missing its id or partner",
  no_email: "no email address",
  link_param: "a link parameter Outflare can't read (a name it uses already, or the program has 5 others)",
  link_param_added: "a link parameter the program now reads too (Settings → Other link parameters)",
  extra_link: "a partner's second link (their code is the first)",
  code_changed: "the code was taken or not allowed, so the partner got a new one",
  partner_missing: "its partner didn't come over",
  partner_unavailable: "a partner other objects name, but Tolt couldn't give them either",
  customer_missing: "its customer didn't come over",
  promo_code_invalid: "a promo code with characters not allowed here",
  promo_code_taken: "a promo code another partner has here",
  already_referred: "another partner already has this customer here",
  status_conflict: "rejected in Tolt after it was approved here",
  unreadable: "Tolt's answer couldn't be read, so this kind didn't come over; the others did",
  customer_id_taken: "another customer here already has this Tolt customer id",
  too_many_groups: "the program already has 50 groups",
  group_missing: "its group didn't come over",
  flow_condition_unsupported: "a flow with a condition Outflare can't read yet: add it on the Flows tab",
  flow_action_unsupported: "a flow whose action Outflare can't read yet: add it on the Flows tab",
  flow_trigger_unsupported: "a flow whose trigger Outflare can't read yet: add it on the Flows tab",
  flow_duration_unsupported: "a flow whose duration Outflare can't read yet: add it on the Flows tab",
  flow_approval_unsupported: "a flow whose approval Outflare can't read yet: add it on the Flows tab",
  flow_custom_rate_unsupported: "a flow with a custom rate Outflare can't read yet: add it on the Flows tab",
  flow_invalid: "a flow Outflare can't accept as it is: add it on the Flows tab",
  flow_paused: "a flow that's paused in Tolt, so it didn't come over",
  groups_unavailable: "Tolt didn't list groups for this account, so none came over",
  flows_unavailable: "Tolt didn't list flows for this account, so none came over: add them on the Flows tab",
  custom_rate_partner_missing: "a flow's custom rate for a partner who didn't come over (the flow came over without it)",
  partner_id_missing: "a promo code that names no partner where Outflare reads it: send us the fields below",
  partners_unlisted: "Tolt reported more partners than came over",
};

type ImportSource = { id: string; name: string; currency: string; status: string };

// ImportSection brings a Tolt program in, as often as needed until the
// switch-over day.
function ImportSection({ ws, program: p, canManage }: { ws: string; program: PartnerProgram; canManage: boolean }) {
  const [runs, setRuns] = useState<PartnerImport[]>([]);
  const [source, setSource] = useState("");
  // The programs in the Tolt account, or null when they couldn't be listed
  // (then the id is typed).
  const [sources, setSources] = useState<ImportSource[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ReactNode>("");
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/partner-programs/{id}/imports", { params: { path: { brand: pickedBrandId(), id: p.id } } });
    setRuns(data?.data ?? []);
    return data?.data ?? [];
  }, [ws, p.id]);
  useEffect(() => {
    (async () => {
      const past = await load();
      if (!canManage) return;
      const { data } = await api.GET("/v1/brands/{brand}/partner-programs/{id}/import-sources", { params: { path: { brand: pickedBrandId(), id: p.id } } });
      if (!data) return;
      setSources(data.data);
      // The program the last run read, else the one an import would take.
      const last = past.find((r) => r.source_program)?.source_program;
      setSource(data.data.some((s) => s.id === last) ? last! : data.suggested);
    })();
  }, [load, canManage, ws, p.id]);
  const running = runs.some((r) => r.status === "queued" || r.status === "running");
  useEffect(() => {
    if (!running) return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [running, load]);

  async function start() {
    setBusy(true);
    setError("");
    const { error } = await api.POST("/v1/brands/{brand}/partner-programs/{id}/imports", {
      params: { path: { brand: pickedBrandId(), id: p.id } }, body: source.trim() ? { source_program: source.trim() } : {},
    });
    setBusy(false);
    if (error) {
      if ((error as { error?: { code?: string } }).error?.code === "connector_needed") {
        return setError(<>Connect Tolt first, with your own API key: <a className="underline underline-offset-2" href={appPath(ws, "connectors", "tolt")}>Connectors, Tolt</a>.</>);
      }
      return setError(errorMessage(error));
    }
    toast("Import started.");
    load();
  }

  return (
    <div className="space-y-3">
      <p className="text-[12px] text-muted-foreground">Brings partners with their link codes (so their links keep working), payout details, promo codes, referred customers, transactions and commissions, paid ones as paid payouts. Run it as often as you like until you switch: each run adds only what's new and brings over status changes. Keep auto-approve off until then, so Tolt's own decisions come over.</p>
      {canManage && (
        <div className="flex flex-wrap items-end gap-2">
          {sources && sources.length > 1 ? (
            <label className="grid gap-1 text-[12px]">
              Tolt program
              <select className={field} value={source} onChange={(e) => setSource(e.target.value)}>
                {!source && <option value="">Choose a program</option>}
                {sources.map((s) => (
                  <option key={s.id} value={s.id}>{s.name || s.id}{s.status === "inactive" ? " (inactive)" : ""}{s.currency ? `, ${s.currency}` : ""}</option>
                ))}
              </select>
            </label>
          ) : sources?.length === 1 ? (
            <p className="text-[12px] text-muted-foreground">From your Tolt program {sources[0].name || sources[0].id}.</p>
          ) : (
            <Input label="Tolt program id" value={source} onChange={(e) => setSource(e.target.value)} placeholder="Only if your account has more than one" maxLength={200} />
          )}
          <Button type="button" onClick={start} disabled={busy || running || (!!sources && sources.length > 1 && !source)}>{running ? "Importing…" : runs.length ? "Run again" : "Import"}</Button>
        </div>
      )}
      <ErrorText>{error}</ErrorText>
      {runs.slice(0, 5).map((r) => (
        <div key={r.id} className="space-y-1 rounded-md border p-3 text-[12px]">
          <div className="flex items-center gap-2">
            <Badge tone={r.status === "failed" ? "red" : r.status === "done" ? "green" : "blue"}>{r.status}</Badge>
            <span className="text-muted-foreground">{ago(r.created_at)}</span>
          </div>
          {r.error && <p className="text-destructive">{r.error}</p>}
          <p className="text-muted-foreground">
            {r.summary.kinds.filter((k) => k.kind !== "links" && (k.seen > 0 || k.reported)).map((k) => `${k.kind.replace("_", " ")}: ${k.seen} read${k.reported ? ` of ${k.reported} Tolt reports` : ""}, ${k.created} new, ${k.updated} updated${k.skipped ? `, ${k.skipped} skipped` : ""}${k.by_status ? ` (${Object.entries(k.by_status).map(([s, n]) => `${n} ${s}`).join(", ")})` : ""}`).join(" · ") || "Nothing read yet."}
          </p>
          {r.summary.issues.length > 0 && (
            <details>
              <summary className="cursor-pointer">{plural(r.summary.issues.length + r.summary.more_issues, "thing", "things")} to check</summary>
              <ul className="mt-1 list-disc pl-5">
                {r.summary.issues.map((i) => (
                  <li key={i.kind + i.external_id + i.reason}>
                    {i.kind.replace("_", " ")} {i.external_id && <code>{i.external_id}</code>}: {importReasons[i.reason] ?? i.reason}
                    {i.message && <span className="block text-muted-foreground">{i.message}</span>}
                  </li>
                ))}
              </ul>
            </details>
          )}
        </div>
      ))}
    </div>
  );
}

// PortalPreview shows how the portal's sign-in page will look with the
// settings as typed, before they are saved.
function PortalPreview({ title, color, logo, favicon, welcome, poweredBy }: { title: string; color: string; logo: string; favicon: string; welcome: string; poweredBy: boolean }) {
  const https = (u: string) => (/^https:\/\/\S+$/.test(u) ? u : "");
  return (
    <div className="overflow-hidden rounded-md border text-[13px]" aria-label="Portal preview">
      <div className="flex items-center gap-2 border-b bg-muted/40 px-3 py-1.5 text-[12px] text-muted-foreground">
        {https(favicon) ? <img src={https(favicon)} alt="" className="size-4" /> : <span className="size-4 rounded-sm bg-muted" />}
        <span className="truncate">{title}</span>
      </div>
      <div className="flex items-center gap-2 border-b px-3 py-2">
        {https(logo) && <img src={https(logo)} alt="" className="h-6" />}
        <span className="font-semibold">{title}</span>
      </div>
      <div className="space-y-2 px-3 py-3">
        {welcome && <p className="whitespace-pre-line text-muted-foreground">{welcome}</p>}
        <span className="inline-block rounded-md px-3 py-1.5 font-semibold text-white" style={{ background: color }}>Email me a sign-in link</span>
        {poweredBy && <p className="text-[11px] text-muted-foreground">Powered by Outflare</p>}
      </div>
    </div>
  );
}

type PortalDomain = { id: string; domain: string; status: string; target: string; partner_program_id: string | null; https_status?: string | null };

// OwnDomain puts the portal on a domain of the brand's own, such as
// partners.example.com: a tracking domain tied to the program, which serves
// the portal at its root once its CNAME points at us.
function OwnDomain({ program: p, canManage, onChanged }: { program: PartnerProgram; canManage: boolean; onChanged: () => void }) {
  const brand = p.brand_id ?? pickedBrandId();
  const [domains, setDomains] = useState<PortalDomain[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    if (!brand) return setDomains([]);
    const { data } = await api.GET("/v1/brands/{brand}/tracking-domains", { params: { path: { brand } } });
    setDomains((data?.data ?? []) as PortalDomain[]);
  }, [brand]);
  useEffect(() => void load(), [load]);
  const mine = domains?.find((d) => d.partner_program_id === p.id);

  // Not a <form>: this sits inside the portal's own form.
  async function add() {
    const domain = name.trim().toLowerCase().replace(/^https?:\/\//, "").replace(/\/.*$/, "");
    if (!domain || !brand) return;
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/tracking-domains", { params: { path: { brand } }, body: { domain, partner_program: p.id } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Domain added. Point its CNAME at the address shown.");
    setAdding(false);
    setName("");
    load();
    onChanged();
  }
  async function remove(d: PortalDomain) {
    const r = await ask({ title: `Stop using ${d.domain}?`, body: "The portal goes back to its Outflare address. Links partners saved with this domain stop working.", choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Remove domain", value: "ok", variant: "danger-solid" }] });
    if (r?.choice !== "ok" || !brand) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/tracking-domains/{id}", { params: { path: { brand, id: d.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("The portal is back on its Outflare address.");
    load();
    onChanged();
  }

  return (
    <div className="space-y-1.5">
      <span className="block text-[13px] font-medium">Your own domain</span>
      {mine ? (
        <div className="space-y-1.5 rounded-md border px-3 py-2 text-[13px]">
          <div className="flex items-center gap-2">
            <span className="min-w-0 flex-1 truncate font-medium">{mine.domain}</span>
            <Badge tone={mine.status === "verified" ? "green" : mine.status === "failed" ? "red" : "amber"}>{mine.status === "verified" ? "Connected" : mine.status === "failed" ? "Not found" : "Waiting for DNS"}</Badge>
          </div>
          {mine.status !== "verified" && (
            <p className="text-xs text-muted-foreground">
              Add a CNAME record for <code className="font-mono">{mine.domain}</code> pointing to <code className="font-mono">{mine.target}</code>. On Cloudflare, set it to DNS only. It's checked every few minutes.
            </p>
          )}
          {canManage && <button type="button" className="text-xs text-muted-foreground hover:text-foreground" onClick={() => remove(mine)}>Stop using this domain</button>}
        </div>
      ) : !canManage ? (
        <p className="text-xs text-muted-foreground">None yet.</p>
      ) : adding ? (
        <div className="flex gap-2">
          <input autoFocus aria-label="Your own domain" className={cn(field, "min-w-0 flex-1")} placeholder="partners.example.com" value={name} onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); void add(); } }} />
          <Button type="button" disabled={busy || !name.trim()} onClick={() => void add()}>{busy ? "Adding…" : "Add"}</Button>
          <Button variant="ghost" type="button" onClick={() => setAdding(false)}>Cancel</Button>
        </div>
      ) : (
        <div>
          <Button variant="ghost" type="button" onClick={() => setAdding(true)}>Use your own domain</Button>
          <span className="mt-1 block text-xs text-muted-foreground">Such as partners.example.com. You add one DNS record; HTTPS is set up for you.</span>
        </div>
      )}
    </div>
  );
}

// reservedAddresses mirrors reservedSubs in engine/internal/api/partners_portal.go:
// names that are, or could pass for, the server's own hosts.
const reservedAddresses = new Set([
  "www", "app", "api", "admin", "mail", "partners", "portal", "static", "assets",
  "account", "accounts", "auth", "beta", "billing", "blog", "cdn", "click", "clicks", "console", "dashboard", "demo", "dev",
  "docs", "domains", "edge", "email", "ftp", "go", "grafana", "help", "id", "imap", "in", "inbound", "links", "login", "mx",
  "ns1", "ns2", "outflare", "pop", "preview", "prometheus", "secure", "signin", "signup", "smtp", "sso", "staging",
  "status", "support", "test", "track", "webmail",
]);

// portalAddressFor suggests a portal address from a program's name:
// "Geonode customer referral" becomes geonode-customer-referral.
export function portalAddressFor(name: string): string {
  const v = name.toLowerCase().normalize("NFKD").replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 63).replace(/-+$/, "");
  return reservedAddresses.has(v) ? "" : v;
}

// onThisServer reports whether a portal's link is a /p/ path on this server
// (no own domain or partners domain in front of it).
function onThisServer(url: string): boolean {
  try {
    const u = new URL(url);
    return u.host === location.host && u.pathname.startsWith("/p/");
  } catch {
    return false;
  }
}

function PortalSection({ ws, program: p, canManage, onSaved }: { ws: string; program: PartnerProgram; canManage: boolean; onSaved: () => void }) {
  const [brandPortal,setBrandPortal]=useState(p.portal);
  const [enabled, setEnabled] = useState(p.portal_enabled);
  // The portal's address: its readable name in the link. Suggested from the
  // program's name until one is saved.
  const [sub, setSub] = useState(p.portal_subdomain ?? portalAddressFor(p.name));
  const [title, setTitle] = useState(p.portal.title ?? "");
  const [color, setColor] = useState(p.portal.color || "#111827");
  const [logo, setLogo] = useState(p.portal.logo_url ?? "");
  const [favicon, setFavicon] = useState(p.portal.favicon_url ?? "");
  const [welcome, setWelcome] = useState(p.portal.welcome ?? "");
  const [from, setFrom] = useState(p.portal.email_from ?? "");
  const [signup, setSignup] = useState(!!p.portal.signup_open);
  const [poweredBy, setPoweredBy] = useState(!p.portal.hide_powered_by);
  const [businessOnly, setBusinessOnly] = useState(!!p.portal.business_email_only);
  const [siteKey, setSiteKey] = useState(p.portal.site_key_id ?? "");
  const [leadMode, setLeadMode] = useState<"" | "review" | "auto">(p.portal.lead_submissions ?? "");
  const [ga, setGA] = useState(p.portal.ga_measurement_id ?? "");
  const [siteKeys, setSiteKeys] = useState<{ id: string; name: string }[]>([]);
  useEffect(() => {
    // The program's brand's site keys.
    if (p.brand_id) api.GET("/v1/brands/{brand}/site-keys", { params: { path: { brand: p.brand_id } } }).then(({ data }) => setSiteKeys((data?.data ?? []).map((k) => ({ id: k.id, name: k.name }))));
  }, [ws, p.brand_id]);
  const [url, setUrl] = useState(p.portal_url);
  const [assets, setAssets] = useState<PartnerAsset[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const loadAssets = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/partner-programs/{id}/assets", { params: { path: { brand: pickedBrandId(), id: p.id } } });
    setAssets(data?.data ?? []);
  }, [ws, p.id]);
  useEffect(() => {
    loadAssets();
  }, [loadAssets]);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error } = await api.PUT("/v1/brands/{brand}/partner-programs/{id}/portal", {
      params: { path: { brand: pickedBrandId(), id: p.id } },
      body: { enabled, subdomain: sub.trim(), settings: { ...brandPortal, title: title.trim(), color, logo_url: logo.trim(), favicon_url: favicon.trim(), welcome: welcome.trim(), email_from: from.trim(), signup_open: signup, hide_powered_by: !poweredBy, business_email_only: businessOnly,
        site_key_id: siteKey, lead_submissions: leadMode, ga_measurement_id: siteKey ? ga.trim().toUpperCase() : "" } },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setUrl(data.portal_url);
    toast(enabled ? "Portal saved." : "Portal closed.");
    onSaved();
  }
  async function addAsset() {
    const r = await ask({ title: "Add an asset", body: "Copy or an email swipe partners can paste. For a banner or a link, put its https address on the first line.", input: { label: "Title", maxLength: 200 }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Next", value: "ok" }] });
    if (r?.choice !== "ok" || !r.value.trim()) return;
    const b = await ask({ title: r.value.trim(), input: { label: "Text, or an https address", maxLength: 10000 }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Add", value: "ok" }] });
    if (b?.choice !== "ok" || !b.value.trim()) return;
    const v = b.value.trim();
    const isURL = /^https:\/\/\S+$/.test(v);
    const kind = isURL ? (/\.(png|jpe?g|gif|webp|svg)(\?|$)/i.test(v) ? "image" : "link") : "text";
    const { error } = await api.POST("/v1/brands/{brand}/partner-programs/{id}/assets", {
      params: { path: { brand: pickedBrandId(), id: p.id } }, body: { kind, title: r.value.trim(), ...(isURL ? { url: v } : { body: v }) },
    });
    if (error) return toast(errorMessage(error), "error");
    loadAssets();
  }
  async function removeAsset(a: PartnerAsset) {
    const { error } = await api.DELETE("/v1/brands/{brand}/partner-programs/{id}/assets/{asset}", { params: { path: { brand: pickedBrandId(), id: p.id, asset: a.id } } });
    if (error) return toast(errorMessage(error), "error");
    loadAssets();
  }

  return (
    <div className="space-y-6">
      <form onSubmit={save} className="space-y-4">
        <p className="text-[12px] text-muted-foreground">Where partners apply, sign in with a link emailed through your own email provider, and see their links, referred customers (never their names), rewards and payouts. Put it on your own domain below. To show customers their referral link inside your own app instead, your backend opens the referral widget (<code>POST …/widget-sessions</code>), which works while the portal is closed.</p>
        <fieldset disabled={!canManage} className="min-w-0 space-y-3">
          <div className="rounded-md border">
            <Toggle label="Open the portal" help={enabled ? "Partners can sign in and see their links, customers, rewards and payouts. Save to apply." : "Closed: nobody can sign in. The referral widget in your own app still works."} checked={enabled} onChange={setEnabled} />
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Input label="Portal address" value={sub} onChange={(e) => setSub(e.target.value.toLowerCase())} placeholder="northpeak" maxLength={63}
              hint={onThisServer(url) ? `The portal's link: ${location.host}/p/${sub.trim() || "…"}/` : "The portal's name in its link. Lowercase letters, digits and hyphens."} />
            <OwnDomain program={p} canManage={canManage} onChanged={onSaved} />
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Input label="Title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder={p.name} maxLength={120} />
            <Input label="Sender" value={from} onChange={(e) => setFrom(e.target.value)} placeholder="Partners <partners@example.com>" hint="On a verified sending domain. Needed for email sign-in; your app can also use automatic sign-in." />
            <Input label="Colour" type="color" value={color} onChange={(e) => setColor(e.target.value)} />
            <Input label="Logo" type="url" value={logo} onChange={(e) => setLogo(e.target.value)} placeholder="https://example.com/logo.svg" maxLength={2000} />
            <Input label="Favicon" type="url" value={favicon} onChange={(e) => setFavicon(e.target.value)} placeholder="https://example.com/favicon.png" maxLength={2000} hint="The browser tab's icon." />
          </div>
          <PartnerPortalBrand currency={p.currency} value={brandPortal} onChange={setBrandPortal}/>
          <PortalPreview title={title.trim() || p.name} color={color} logo={logo.trim()} favicon={favicon.trim()} welcome={welcome.trim()} poweredBy={poweredBy} />
          <Input label="Welcome" value={welcome} onChange={(e) => setWelcome(e.target.value)} maxLength={4000} placeholder="Earn 20% of every sale you send our way." />
          <Rows>
            <Toggle label="Anyone can apply" help="Off: only partners you add or invite can sign in." checked={signup} onChange={setSignup} />
            <Toggle label="Work email addresses only" help="Refuse applications from free, disposable and relay addresses." checked={businessOnly} onChange={setBusinessOnly} />
            <Toggle label={'Show "Powered by Outflare"'} help="At the foot of the portal." checked={poweredBy} onChange={setPoweredBy} />
          </Rows>
          <label className="block text-[13px]">
            <span className="mb-1 block font-medium">Leads from partners</span>
            <select className={field + " w-full sm:w-auto"} value={leadMode} onChange={(e) => setLeadMode(e.target.value as typeof leadMode)}>
              <option value="">Off</option>
              <option value="review">Partners submit leads, you approve each (Partners → Leads)</option>
              <option value="auto">Partners submit leads, taken at once unless someone else has them</option>
            </select>
          </label>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block text-[13px]">
              <span className="mb-1 block font-medium">Website analytics</span>
              <select className={field + " w-full"} value={siteKey} onChange={(e) => setSiteKey(e.target.value)}>
                <option value="">Off</option>
                {siteKeys.map((k) => <option key={k.id} value={k.id}>{k.name}</option>)}
              </select>
              <span className="mt-1 block text-xs text-muted-foreground">Partners' visits to the portal show in your website analytics, through this site key and its cookie banner.</span>
            </label>
            {siteKey && <Input label="Google Analytics" value={ga} onChange={(e) => setGA(e.target.value)} placeholder="G-ABC123XYZ" maxLength={20} hint="Optional. Loads only once the visitor allows analytics where consent is needed." />}
          </div>
        </fieldset>
        <ErrorText>{error}</ErrorText>
        <div className="flex flex-wrap items-center gap-2">
          {canManage && <Button disabled={busy}>Save portal</Button>}
          {p.portal_enabled && <a className="text-[13px] underline" href={url} target="_blank" rel="noreferrer">{url}</a>}
        </div>
      </form>
      <div className="space-y-2 border-t pt-4">
        <h3 className="text-[13px] font-medium">Assets for partners</h3>
        <p className="text-[12px] text-muted-foreground">Banners, copy and links partners can use, shown in the portal.</p>
        {assets.length === 0 ? <p className="text-[12px] text-muted-foreground">No banners, copy or links yet.</p> : (
          <ul className="divide-y rounded-md border">
            {assets.map((a) => (
              <li key={a.id} className="flex items-center gap-2 px-3 py-2 text-[13px]">
                <Badge tone="grey">{a.kind}</Badge>
                <span className="min-w-0 flex-1 truncate">{a.title}</span>
                {canManage && <Button type="button" variant="ghost" onClick={() => removeAsset(a)}>Remove</Button>}
              </li>
            ))}
          </ul>
        )}
        {canManage && <Button type="button" variant="ghost" onClick={addAsset}>Add an asset</Button>}
      </div>
    </div>
  );
}
