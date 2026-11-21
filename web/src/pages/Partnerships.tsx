// Partnerships is the one place for working with other sites and people:
// finding sites worth being on, every deal with one of them (offers, link
// orders, agreements), what is live, affiliate programs, what you owe and
// what it did. Each tab is its own URL (/partnerships/<tab>), and the pages
// that used to be separate menus (Presence, Partners, Link orders, Link ROI)
// open inside it.
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Plus } from "lucide-react";
import { api, errorMessage } from "../api";
import { appPath, type Tab } from "@/lib/paths";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { track } from "@/lib/analytics";
import { Badge, Button, Empty, ErrorText, InsideSection, Loading, PageHeader, Status } from "../ui";
import { Coverage } from "./Coverage";
import { Partners } from "./Partners";
import { PartnerProposals } from "./PartnerProposals";
import { LinkROI } from "./LinkROI";
import { Opportunities } from "./brand-coverage/opportunities";
import { list, type Row } from "./brand-coverage/client";
import type { components } from "@outflare/sdk";
import { removalText, type LinkRule } from "@/portal/proposals";

type Program = components["schemas"]["PartnerProgram"];
type Proposal = components["schemas"]["PartnerProposal"];
type Order = components["schemas"]["LinkOrder"];
type Payout = components["schemas"]["PartnerPayout"];
type Go = (tab: Tab, item?: string) => void;

export const partnershipTabs = [
  ["find", "Find", "Sites worth being on, ranked: they link to or mention your competitors but not you. Make an offer or ask for a free mention from any row."],
  ["offers", "Offers", "Every offer, order and agreement with one site or person, paid or free. Each moves along as it is answered, agreed, found live and paid. (Sales deals live in CRM.)"],
  ["live", "Live", "Everywhere you appear right now: links, mentions, videos, podcasts and listings, earned or paid. Checked on a schedule; a paid one that disappears is flagged."],
  ["affiliates", "Affiliates", "Partners who earn commission on the customers they send, through their referral link."],
  ["payments", "Payments", "Everything you owe, from offers and affiliates, in one list. Outflare never sends money: you approve, then pay."],
  ["results", "Results", "What you spent, what went live, and what it did for the pages you got links to."],
] as const;
export type PartnershipTab = (typeof partnershipTabs)[number][0];

type Props = { ws: string; item?: string; go: Go; can: (scope: string) => boolean; myId: string; writer: boolean };

export function Partnerships(props: Props) {
  const { brand, active, loaded } = useBrand(props.ws);
  if (!loaded) return <Loading />;
  if (!brand) return <PickBrand what="Partnerships" brands={active.length} />;
  return <PartnershipsBrand key={brand.id} {...props} brand={brand.id} />;
}

function PartnershipsBrand({ ws, item = "", go, can, myId, writer, brand }: Props & { brand: string }) {
  const [given, ...rest] = item.split("/");
  // The tab was called Deals before; old links still open it.
  const tab = given === "deals" ? "offers" : given;
  const current = (partnershipTabs.find(([t]) => t === tab)?.[0] ?? "find") as PartnershipTab;
  const sub = rest.join("/");
  const open = (t: PartnershipTab, s?: string) => {
    track("ui.partnerships_tab_opened", { tab: t });
    go("partnerships", s ? `${t}/${s}` : t);
  };
  const desc = partnershipTabs.find(([t]) => t === current)![2];
  const action =
    current === "offers" && !sub ? (
      <Button onClick={() => open("offers", "new")}>
        <Plus className="size-4" />
        New offer
      </Button>
    ) : undefined;
  return (
    <div>
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-xl font-semibold tracking-tight">Partnerships</h1>
          <p className="mt-1 max-w-3xl text-[13px] text-muted-foreground">{desc}</p>
        </div>
        {action && <div className="flex flex-wrap gap-2">{action}</div>}
      </div>
      <nav aria-label="Partnerships" className="mt-4 mb-6 flex gap-1 overflow-x-auto border-b">
        {partnershipTabs.map(([t, label]) => (
          <a
            key={t}
            href={appPath(ws, "partnerships", t)}
            onClick={(e) => {
              e.preventDefault();
              open(t);
            }}
            aria-current={t === current ? "page" : undefined}
            className={`-mb-px shrink-0 border-b-2 px-3 py-2 text-[13px] ${t === current ? "border-foreground font-medium" : "border-transparent text-muted-foreground hover:text-foreground"}`}
          >
            {label}
          </a>
        ))}
      </nav>
      <InsideSection.Provider value>
        {current === "find" && <Find ws={ws} brand={brand} can={can} myId={myId} go={go} />}
        {current === "offers" && <Deals ws={ws} brand={brand} can={can} go={go} sub={sub} open={(s) => open("offers", s)} writer={writer} myId={myId} />}
        {current === "live" && <Coverage ws={ws} myId={myId} can={can} go={go} section="placements" />}
        {current === "affiliates" && <Partners ws={ws} canManage={writer} open={sub} onOpen={(id) => open("affiliates", id || undefined)} go={go} />}
        {current === "payments" && <Payments brand={brand} can={can} go={go} open={(s) => open("offers", s)} />}
        {current === "results" && <Results ws={ws} brand={brand} can={can} go={go} />}
      </InsideSection.Provider>
    </div>
  );
}

function Find({ ws, brand, can, myId, go }: { ws: string; brand: string; can: (s: string) => boolean; myId: string; go: Go }) {
  return (
    <div className="space-y-4">
      <p className="text-[13px] text-muted-foreground">
        More places to look: <a className="cursor-pointer underline underline-offset-2" onClick={() => go("link-building")}>pages that lost links or mention you without one</a> and{" "}
        <a className="cursor-pointer underline underline-offset-2" onClick={() => go("link-orders", "catalog")}>sellers' price lists</a>.
      </p>
      {can("coverage.read") ? <Opportunities ws={ws} brand={brand} manage={can("coverage.manage")} myId={myId} /> : <Empty title="Presence access needed">Ask a workspace admin for Coverage read access.</Empty>}
    </div>
  );
}

// ───────────── Offers: offers, link orders and agreements on one board

const stages = ["Draft", "Offer sent", "Negotiating", "Agreed", "Live", "Done"] as const;
type Stage = (typeof stages)[number] | "Closed";
type Deal = { key: string; kind: string; name: string; who: string; value: string; stage: Stage; next: string; urgent: boolean; open: () => void; at: string };

const money = (minor: number | null | undefined, currency?: string | null) =>
  minor == null ? "" : new Intl.NumberFormat("en-US", { style: "currency", currency: currency || "USD", maximumFractionDigits: minor % 100 ? 2 : 0 }).format(minor / 100);
const host = (u: string) => {
  try {
    return new URL(u.includes("://") ? u : `https://${u}`).hostname.replace(/^www\./, "");
  } catch {
    return u;
  }
};

const offerStage: Record<Proposal["status"], [Stage, string, boolean]> = {
  draft: ["Draft", "Not sent yet", true],
  quote_requested: ["Offer sent", "Waiting for their price", false],
  offered: ["Offer sent", "Waiting for an answer", false],
  quote_received: ["Negotiating", "They sent a price: your turn", true],
  accepted: ["Agreed", "Accepted: waiting for it to go live", false],
  completed: ["Done", "Completed", false],
  declined: ["Closed", "Declined", false],
};
const orderStage: Record<Order["status"], [Stage, string, boolean]> = {
  requested: ["Offer sent", "Waiting for their price", false],
  quoted: ["Negotiating", "They sent a price: your turn", true],
  negotiating: ["Negotiating", "Negotiating", false],
  approved: ["Agreed", "Approved: waiting for it to go live", false],
  published: ["Live", "Live: approve the payment", true],
  paid: ["Done", "Paid", false],
  verified: ["Done", "Paid and checked", false],
  lost: ["Closed", "Link lost", true],
};
const agreementStage: Record<string, [Stage, string, boolean]> = {
  draft: ["Draft", "Draft agreement", true],
  proposed: ["Offer sent", "Proposed", false],
  active: ["Agreed", "Active", false],
  completed: ["Done", "Completed", false],
  cancelled: ["Closed", "Cancelled", false],
};

async function loadDeals(brand: string, can: (s: string) => boolean, open: (s: string) => void, go: Go) {
  const deals: Deal[] = [];
  let programs: Program[] = [];
  const errors: string[] = [];
  if (can("partners.read")) {
    const { data, error } = await api.GET("/v1/brands/{brand}/partner-programs", { params: { path: { brand } } });
    if (error) errors.push(errorMessage(error));
    programs = (data?.data ?? []).filter((p) => p.status !== "archived");
    for (const p of programs) {
      const r = await api.GET("/v1/brands/{brand}/partner-programs/{id}/proposals", { params: { path: { brand, id: p.id } } });
      if (r.error) errors.push(errorMessage(r.error));
      for (const x of (r.data?.data ?? []) as Proposal[]) {
        const [stage, next, urgent] = offerStage[x.status];
        const c = x.config;
        const cents = (c.milestones ?? []).reduce((n, m) => n + (m.amount_cents ?? 0), 0);
        deals.push({
          key: `offer:${x.id}`,
          kind: "Offer",
          name: c.partner_site_name || (c.partner_site_url ? host(c.partner_site_url) : c.title),
          who: x.recipient,
          value: cents ? money(cents, p.currency) : c.request_quote ? "Asked for their price" : "",
          stage,
          next,
          urgent,
          at: x.updated_at,
          open: () => open(`offer/${p.id}/${x.id}`),
        });
      }
    }
  }
  if (can("coverage.read")) {
    const { data, error } = await api.GET("/v1/brands/{brand}/link-orders", { params: { path: { brand }, query: { limit: 200 } } });
    if (error) errors.push(errorMessage(error));
    for (const o of data?.data ?? []) {
      const [stage, next, urgent] = o.status === "lost" && o.paid_at ? (["Closed", "Link lost after payment", true] as [Stage, string, boolean]) : orderStage[o.status];
      deals.push({
        key: `order:${o.id}`,
        kind: "Link order",
        name: host(o.domain),
        who: o.anchor_primary ? `“${o.anchor_primary}”` : "",
        value: money(o.donor_amount_minor ?? o.quoted_amount_minor, o.currency ?? o.quoted_currency),
        stage,
        next,
        urgent,
        at: o.updated_at,
        open: () => go("link-orders", o.id),
      });
    }
  }
  if (can("coverage.finance.read")) {
    try {
      const [agreements, publishers] = await Promise.all([list(brand, "agreements", { limit: 200 }), list(brand, "publishers", { limit: 200 })]);
      const domain = new Map(publishers.data.map((p: Row) => [p.id, String(p.domain ?? "")]));
      for (const a of agreements.data) {
        const [stage, next, urgent] = agreementStage[String(a.status)] ?? agreementStage.draft;
        deals.push({
          key: `agreement:${a.id}`,
          kind: "Agreement",
          name: domain.get(String(a.publisher_id)) || String(a.title),
          who: String(a.title),
          value: money(Number(a.amount_minor ?? 0) || null, String(a.currency ?? "USD")),
          stage,
          next,
          urgent,
          at: String(a.updated_at ?? a.created_at),
          open: () => open(`agreement/${a.id}`),
        });
      }
    } catch (e) {
      errors.push(errorMessage(e));
    }
  }
  deals.sort((a, b) => b.at.localeCompare(a.at));
  return { deals, programs, error: errors[0] ?? "" };
}

function Deals({ ws, brand, can, go, sub, open, writer, myId }: { ws: string; brand: string; can: (s: string) => boolean; go: Go; sub: string; open: (s?: string) => void; writer: boolean; myId: string }) {
  const [data, setData] = useState<{ deals: Deal[]; programs: Program[]; error: string } | null>(null);
  const [closed, setClosed] = useState(false);
  const [kind, setKind] = useState("");
  useEffect(() => {
    if (sub) return;
    let live = true;
    void loadDeals(brand, can, (s) => open(s), go).then((d) => live && setData(d));
    return () => {
      live = false;
    };
  }, [brand, sub]); // eslint-disable-line react-hooks/exhaustive-deps
  const [what, a, b] = sub.split("/");
  const back = { label: "Offers", onClick: () => open() };
  if (what === "agreement" && a) {
    return (
      <>
        <PageHeader title="Agreement" back={back} />
        <Coverage ws={ws} myId={myId} can={can} go={go} section="commercial" record={`agreements/${a}`} />
      </>
    );
  }
  if (what === "offer" || what === "new") return <OfferPage ws={ws} brand={brand} program={what === "new" ? "" : a} proposal={what === "new" ? "" : b} writer={writer} back={back} />;
  if (!data) return <Loading />;
  const shown = data.deals.filter((d) => (closed || d.stage !== "Closed") && (!kind || d.kind === kind));
  const columns: Stage[] = closed ? [...stages, "Closed"] : [...stages];
  return (
    <div className="space-y-4">
      <ErrorText>{data.error}</ErrorText>
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        {["", "Offer", "Link order", "Agreement"].map((k) => (
          <Button key={k || "all"} variant={kind === k ? "primary" : "ghost"} className="h-7" onClick={() => setKind(k)}>
            {k ? `${k}s` : "All offers"}
            <span className="font-mono text-[11px] opacity-70">{data.deals.filter((d) => !k || d.kind === k).length}</span>
          </Button>
        ))}
        <label className="ml-auto flex items-center gap-1.5 text-muted-foreground">
          <input type="checkbox" checked={closed} onChange={(e) => setClosed(e.target.checked)} />
          Show declined and lost
        </label>
      </div>
      {data.deals.length === 0 ? (
        <Empty title="No offers yet">Open Find and press Make an offer on a site, or press New offer.</Empty>
      ) : (
        <div className="grid gap-3 overflow-x-auto" style={{ gridTemplateColumns: `repeat(${columns.length}, minmax(180px, 1fr))` }}>
          {columns.map((c) => {
            const cards = shown.filter((d) => d.stage === c);
            return (
              <div key={c} className="min-w-0 rounded-lg bg-muted/40 p-2">
                <div className="mb-2 flex items-center justify-between px-1 text-[12px] font-medium">
                  <span>{c}</span>
                  <span className="font-mono text-muted-foreground">{cards.length}</span>
                </div>
                <div className="space-y-2">
                  {cards.map((d) => (
                    <button key={d.key} onClick={d.open} className="block w-full rounded-md border bg-card p-2.5 text-left text-[12px] shadow-xs hover:border-foreground/30">
                      <div className="truncate text-[13px] font-medium">{d.name}</div>
                      <div className="mt-1 flex flex-wrap items-center gap-1.5 text-muted-foreground">
                        <Badge>{d.kind}</Badge>
                        {d.value && <span>{d.value}</span>}
                      </div>
                      {d.who && <div className="mt-1 truncate text-muted-foreground">{d.who}</div>}
                      <div className={`mt-2 ${d.urgent ? "font-medium text-amber-700 dark:text-amber-400" : "text-muted-foreground"}`}>{d.next}</div>
                    </button>
                  ))}
                </div>
              </div>
            );
          })}
        </div>
      )}
      <p className="text-[12px] text-muted-foreground">Cards move between columns by themselves as offers are answered, links go live and payments are made.</p>
    </div>
  );
}

// An offer, or a new one, shown with the program it belongs to. Offers live
// in a referral program today, so a new one uses the brand's first program.
function OfferPage({ ws, brand, program, proposal, writer, back }: { ws: string; brand: string; program: string; proposal: string; writer: boolean; back: { label: string; onClick: () => void } }) {
  const [programs, setPrograms] = useState<Program[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    void api.GET("/v1/brands/{brand}/partner-programs", { params: { path: { brand } } }).then(({ data, error }) => {
      if (error) setError(errorMessage(error));
      setPrograms((data?.data ?? []).filter((p) => p.status !== "archived"));
    });
  }, [brand]);
  if (!programs) return <Loading />;
  const p = programs.find((x) => x.id === program) ?? programs[0];
  return (
    <>
      <PageHeader title={proposal ? "Offer" : "New offer"} back={back} />
      <ErrorText>{error}</ErrorText>
      {p ? (
        <PartnerProposals ws={ws} program={p} canManage={writer} open={proposal} compose={!proposal} />
      ) : (
        <Empty title="Set up partners first">Offers are sent from a partner portal. Open Affiliates and create a program, then come back.</Empty>
      )}
    </>
  );
}

// ───────────── Payments: what you owe, from every kind of deal

type Payment = { key: string; who: string; what: string; amount: string; status: ReactNode; group: "now" | "waiting" | "paid"; at: string; open: () => void };

function Payments({ brand, can, go, open }: { brand: string; can: (s: string) => boolean; go: Go; open: (s: string) => void }) {
  const [rows, setRows] = useState<Payment[] | null>(null);
  const [error, setError] = useState("");
  const [group, setGroup] = useState<Payment["group"]>("now");
  useEffect(() => {
    let live = true;
    (async () => {
      const out: Payment[] = [];
      if (can("partners.read")) {
        const { data, error } = await api.GET("/v1/brands/{brand}/partner-payouts", { params: { path: { brand }, query: { limit: 500 } as never } });
        if (error) setError(errorMessage(error));
        for (const p of (data?.data ?? []) as Payout[]) {
          if (p.status === "canceled") continue;
          out.push({
            key: `payout:${p.id}`,
            who: p.partner_name,
            what: `Affiliate commission, ${p.rewards} reward${p.rewards === 1 ? "" : "s"}`,
            amount: money(p.amount_cents, p.currency),
            status: p.status === "due" ? <Status tone="amber">Due</Status> : <Status tone="green">Paid {p.paid_at?.slice(0, 10)}</Status>,
            group: p.status === "due" ? "now" : "paid",
            at: p.paid_at ?? p.created_at,
            open: () => go("partnerships", "affiliates"),
          });
        }
      }
      if (can("coverage.read")) {
        const { data, error } = await api.GET("/v1/brands/{brand}/link-orders", { params: { path: { brand }, query: { limit: 200 } } });
        if (error) setError(errorMessage(error));
        for (const o of data?.data ?? []) {
          const amount = money(o.donor_amount_minor ?? o.quoted_amount_minor, o.currency ?? o.quoted_currency);
          const base = { key: `order:${o.id}`, who: host(o.domain), what: "Link order", amount, at: o.updated_at, open: () => go("link-orders", o.id) };
          if (o.status === "published") out.push({ ...base, status: <Status tone="amber">Live: ready to approve</Status>, group: "now" });
          else if (o.status === "approved") out.push({ ...base, status: <Status>Waiting: not live yet</Status>, group: "waiting" });
          else if (o.status === "lost" && !o.paid_at) out.push({ ...base, status: <Status tone="red">Held: link lost</Status>, group: "waiting" });
          else if (o.paid_at) out.push({ ...base, status: <Status tone="green">Paid {o.paid_at.slice(0, 10)}</Status>, group: "paid", at: o.paid_at });
        }
      }
      if (can("coverage.finance.read")) {
        try {
          const payments = await list(brand, "payments", { limit: 200 });
          for (const p of payments.data)
            out.push({
              key: `payment:${p.id}`,
              who: String(p.title),
              what: "Agreement payment",
              amount: money(Number(p.amount_minor ?? 0), String(p.currency ?? "USD")),
              status: <Status tone="green">Paid {String(p.paid_on ?? "")}</Status>,
              group: "paid",
              at: String(p.paid_on ?? p.created_at),
              open: () => open(`agreement/${String(p.agreement_id ?? "")}`),
            });
        } catch (e) {
          setError(errorMessage(e));
        }
      }
      out.sort((a, b) => b.at.localeCompare(a.at));
      if (live) setRows(out);
    })();
    return () => {
      live = false;
    };
  }, [brand]); // eslint-disable-line react-hooks/exhaustive-deps
  const counts = useMemo(() => ({ now: rows?.filter((r) => r.group === "now").length ?? 0, waiting: rows?.filter((r) => r.group === "waiting").length ?? 0, paid: rows?.filter((r) => r.group === "paid").length ?? 0 }), [rows]);
  if (!rows) return <Loading />;
  const shown = rows.filter((r) => r.group === group);
  return (
    <div className="space-y-4">
      <ErrorText>{error}</ErrorText>
      <div className="flex flex-wrap gap-2">
        {(
          [
            ["now", "Needs you"],
            ["waiting", "Waiting or held"],
            ["paid", "Paid"],
          ] as const
        ).map(([g, l]) => (
          <Button key={g} variant={group === g ? "primary" : "ghost"} className="h-7" onClick={() => setGroup(g)}>
            {l}
            <span className="font-mono text-[11px] opacity-70">{counts[g]}</span>
          </Button>
        ))}
      </div>
      {shown.length === 0 ? (
        <Empty title={group === "now" ? "Nothing to approve" : "Nothing here"}>{group === "now" ? "Payments show here once a link is live or a commission is due." : "Nothing in this list yet."}</Empty>
      ) : (
        <div className="overflow-hidden rounded-lg border">
          <table className="w-full text-[13px]">
            <thead>
              <tr>
                {["Who", "For", "Amount", "Status", ""].map((h) => (
                  <th key={h} className="h-9 border-b bg-muted/40 px-3 text-left text-[12px] font-medium text-muted-foreground">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => (
                <tr key={r.key}>
                  <td className="border-b px-3 py-2.5 font-medium">{r.who}</td>
                  <td className="border-b px-3 py-2.5">{r.what}</td>
                  <td className="border-b px-3 py-2.5 font-mono">{r.amount}</td>
                  <td className="border-b px-3 py-2.5">{r.status}</td>
                  <td className="border-b px-3 py-2.5 text-right">
                    <Button variant="ghost" className="h-7" onClick={r.open}>
                      Open
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="text-[12px] text-muted-foreground">Approve and mark payments paid from each offer or affiliate. Outflare never moves money.</p>
      {can("partners.read") && <RemovedLinkRule brand={brand} canEdit={can("partners.manage")} />}
    </div>
  );
}

// RemovedLinkRule is the brand's rule for a paid link removed after payment.
// It is copied into each offer when it is sent.
function RemovedLinkRule({ brand, canEdit }: { brand: string; canEdit: boolean }) {
  const [rule, setRule] = useState<LinkRule | null>(null);
  const [saved, setSaved] = useState<LinkRule | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    void api.GET("/v1/brands/{brand}/partner-link-rule", { params: { path: { brand } } }).then(({ data, error }) => {
      if (error) setError(errorMessage(error));
      else if (data) {
        setRule(data);
        setSaved(data);
      }
    });
  }, [brand]);
  if (!rule) return error ? <ErrorText>{error}</ErrorText> : null;
  const changed = JSON.stringify(rule) !== JSON.stringify(saved);
  const save = async () => {
    setBusy(true);
    setError("");
    const { data, error } = await api.PUT("/v1/brands/{brand}/partner-link-rule", { params: { path: { brand } }, body: rule });
    setBusy(false);
    if (error) setError(errorMessage(error));
    else if (data) {
      setRule(data);
      setSaved(data);
    }
  };
  const number = (key: "restore_days" | "guarantee_months", label: string, max: number) => (
    <label className="space-y-1 text-[12px]">
      <span className="block text-muted-foreground">{label}</span>
      <input type="number" min={1} max={max} disabled={!canEdit} className="h-8 w-24 rounded-md border bg-transparent px-2 text-[13px]" value={rule[key]} onChange={(e) => setRule({ ...rule, [key]: Number(e.target.value) })} />
    </label>
  );
  return (
    <section className="space-y-3 rounded-lg border p-4">
      <div>
        <h3 className="text-[14px] font-medium">When a paid link is removed</h3>
        <p className="text-[12px] text-muted-foreground">Copied into each offer when you send it. Offers already sent keep the rule they were sent with.</p>
      </div>
      <div className="flex flex-wrap items-end gap-4">
        <label className="space-y-1 text-[12px]">
          <span className="block text-muted-foreground">Rule</span>
          <select disabled={!canEdit} className="h-8 rounded-md border bg-transparent px-2 text-[13px]" value={rule.removal} onChange={(e) => setRule({ ...rule, removal: e.target.value as LinkRule["removal"] })}>
            <option value="restore_or_refund">Restore or refund, for a set time</option>
            <option value="restore_or_refund_forever">Restore or refund, forever</option>
            <option value="shrinking_refund">Refund that shrinks over time</option>
            <option value="warn_only">Only tell me</option>
          </select>
        </label>
        {rule.removal !== "warn_only" && number("restore_days", "Days to restore it", 90)}
        {(rule.removal === "restore_or_refund" || rule.removal === "shrinking_refund") && number("guarantee_months", "Months it applies", 120)}
      </div>
      <p className="text-[13px]">{removalText(rule)}</p>
      <ErrorText>{error}</ErrorText>
      {canEdit && (
        <div className="flex gap-2">
          <Button variant="primary" disabled={!changed || busy} onClick={() => void save()}>
            Save
          </Button>
          <Button variant="ghost" disabled={!changed || busy} onClick={() => setRule(saved)}>
            Cancel
          </Button>
        </div>
      )}
    </section>
  );
}

// ───────────── Results

function Results({ ws, brand, can, go }: { ws: string; brand: string; can: (s: string) => boolean; go: Go }) {
  const [stats, setStats] = useState<{ spent: string; live: number; per: string; accepted: string } | null>(null);
  useEffect(() => {
    if (!can("coverage.read")) return;
    void api.GET("/v1/brands/{brand}/link-orders", { params: { path: { brand }, query: { limit: 200 } } }).then(({ data }) => {
      const orders = data?.data ?? [];
      const paid = orders.filter((o) => o.paid_at);
      const cents = paid.reduce((n, o) => n + (o.donor_amount_minor ?? 0), 0);
      const live = orders.filter((o) => ["published", "paid", "verified"].includes(o.status)).length;
      const asked = orders.filter((o) => o.status !== "requested").length;
      const agreed = orders.filter((o) => ["approved", "published", "paid", "verified"].includes(o.status)).length;
      const currency = paid[0]?.currency ?? "USD";
      setStats({ spent: money(cents, currency) || "$0", live, per: live ? money(Math.round(cents / Math.max(paid.length, 1)), currency) : "–", accepted: asked ? `${Math.round((agreed / asked) * 100)}%` : "–" });
    });
  }, [brand]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div className="space-y-6">
      {stats && (
        <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
          {[
            ["Spent on links", stats.spent],
            ["Live paid links", String(stats.live)],
            ["Cost per paid link", stats.per],
            ["Prices agreed", stats.accepted],
          ].map(([l, v]) => (
            <div key={l} className="rounded-lg border bg-card p-4 shadow-xs">
              <div className="text-[12px] text-muted-foreground">{l}</div>
              <div className="mt-1 text-2xl font-semibold tracking-tight">{v}</div>
            </div>
          ))}
        </div>
      )}
      <LinkROI ws={ws} go={go} />
      <p className="text-[13px] text-muted-foreground">
        Where you appear, by month and by kind: <a className="cursor-pointer underline underline-offset-2" onClick={() => go("coverage")}>Presence reports</a>.
      </p>
    </div>
  );
}
