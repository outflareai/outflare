import { useCallback, useEffect, useState } from "react";
import { PickBrand } from "@/components/pick-brand";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { Tab } from "../lib/paths";
import { isID } from "../lib/paths";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Button, Empty, ErrorText, Loading, PageHeader, Stat, Stats } from "../ui";
import { Overview } from "./link-orders/Overview";
import { Catalog } from "./link-orders/Catalog";
import { Candidates } from "./link-orders/Candidates";
import { Orders } from "./link-orders/Orders";
import { Checks } from "./link-orders/Checks";
import { Impact } from "./link-orders/Impact";
import { Payments } from "./link-orders/Payments";
import { SettingsForm } from "./link-orders/SettingsForm";
import { DonorSheet } from "./link-orders/DonorSheet";
import {
  dash,
  money,
  sectionLabel,
  sections,
  type LinkOrder,
  type LinkOrderBudget,
  type LinkOrderCandidate,
  type LinkOrderDonor,
  type LinkOrderSettings,
  type LinkOrderSummary,
  type Member,
  type PageData,
  type Section,
} from "./link-orders/shared";

type Props = {
  ws: string;
  myId: string;
  item?: string;
  can: (scope: string) => boolean;
  go: (tab: Tab, item?: string) => void;
};

function sectionOf(item?: string): { section: Section; orderId?: string } {
  if (item && (sections as readonly string[]).includes(item)) return { section: item as Section };
  if (isID(item)) return { section: "orders", orderId: item };
  return { section: "overview" };
}

async function collect<T>(load: (cursor?: string) => Promise<{ data?: T[]; next_cursor?: string | null; error?: unknown }>): Promise<T[]> {
  const out: T[] = [];
  let cursor: string | undefined;
  for (let i = 0; i < 8; i++) {
    const page = await load(cursor);
    if (page.error) throw page.error;
    out.push(...(page.data ?? []));
    if (!page.next_cursor) break;
    cursor = page.next_cursor;
  }
  return out;
}

export function LinkOrders(props: Props) {
  const { brand, loaded, brands } = useBrand(props.ws);
  if (!loaded) return <Loading />;
  if (!brand) return <PickBrand what="Link orders" brands={brands.length} />;
  if (!props.can("coverage.read")) {
    return (
      <Empty title="Coverage read needed">Ask a workspace admin for Coverage read access to open Link orders.</Empty>
    );
  }
  return <LinkOrdersBrand key={brand.id} {...props} />;
}

function LinkOrdersBrand({ ws, myId, item, can, go }: Props) {
  const brand = pickedBrandId();
  const { section, orderId } = sectionOf(item);
  const [settings, setSettings] = useState<LinkOrderSettings | null>(null);
  const [donors, setDonors] = useState<LinkOrderDonor[]>([]);
  const [orders, setOrders] = useState<LinkOrder[]>([]);
  const [candidates, setCandidates] = useState<LinkOrderCandidate[]>([]);
  const [summary, setSummary] = useState<LinkOrderSummary | null>(null);
  const [budget, setBudget] = useState<LinkOrderBudget | null>(null);
  const [members, setMembers] = useState<Member[]>([]);
  const [error, setError] = useState("");
  const [ready, setReady] = useState(false);
  const [tick, setTick] = useState(0);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [cart, setCart] = useState<string[]>([]);
  const [donor, setDonor] = useState<LinkOrderDonor | undefined>();

  const reload = useCallback(() => setTick((n) => n + 1), []);

  useEffect(() => {
    let gone = false;
    setError("");
    Promise.all([
      api.GET("/v1/brands/{brand}/link-orders/settings", { params: { path: { brand } } }),
      collect<LinkOrderDonor>((cursor) =>
        api.GET("/v1/brands/{brand}/link-orders/catalog", { params: { path: { brand }, query: { limit: 200, cursor, archived: "false" } } }).then(({ data, error }) => ({ data: data?.data, next_cursor: data?.next_cursor, error })),
      ).then((data) => ({ data })).catch((error) => ({ error })),
      collect<LinkOrder>((cursor) =>
        api.GET("/v1/brands/{brand}/link-orders", { params: { path: { brand }, query: { limit: 200, cursor } } }).then(({ data, error }) => ({ data: data?.data, next_cursor: data?.next_cursor, error })),
      ).then((data) => ({ data })).catch((error) => ({ error })),
      api.GET("/v1/brands/{brand}/link-orders/candidates", { params: { path: { brand }, query: { limit: 200 } } }),
      api.GET("/v1/brands/{brand}/link-orders/summary", { params: { path: { brand } } }),
      can("coverage.finance.read")
        ? api.GET("/v1/brands/{brand}/link-orders/budget", { params: { path: { brand } } })
        : Promise.resolve({ data: undefined, error: undefined }),
      api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }),
    ]).then((results) => {
      if (gone) return;
      const [settingsRes, catalogRes, ordersRes, candidatesRes, summaryRes, budgetRes, membersRes] = results;
      const fail =
        settingsRes.error ??
        ("error" in catalogRes ? catalogRes.error : undefined) ??
        ("error" in ordersRes ? ordersRes.error : undefined) ??
        candidatesRes.error ??
        summaryRes.error;
      if (fail) {
        setError(errorMessage(fail));
        setReady(true);
        return;
      }
      setSettings(settingsRes.data ?? null);
      setDonors("data" in catalogRes && Array.isArray(catalogRes.data) ? catalogRes.data : []);
      setOrders("data" in ordersRes && Array.isArray(ordersRes.data) ? ordersRes.data : []);
      setCandidates(candidatesRes.data?.data ?? []);
      setSummary(summaryRes.data ?? null);
      setBudget("error" in budgetRes && budgetRes.error ? null : (budgetRes.data ?? null));
      setMembers("data" in membersRes && membersRes.data?.data ? membersRes.data.data : []);
      setReady(true);
    });
    return () => {
      gone = true;
    };
  }, [brand, ws, tick]);

  const goSection = (next: Section, nextOrder?: string) => {
    if (nextOrder) go("link-orders", nextOrder);
    else go("link-orders", next === "overview" ? undefined : next);
  };

  const isApprover = !!myId && !!settings?.approver_user_id && settings.approver_user_id === myId && can("coverage.approve");
  const canPay = isApprover && can("coverage.finance.manage");
  const canManage = can("coverage.manage");
  const canFinanceRead = can("coverage.finance.read");

  const data: PageData = {
    ws,
    brand,
    myId,
    can,
    canManage,
    canFinanceRead,
    isApprover,
    canPay,
    settings,
    donors,
    orders,
    candidates,
    summary,
    budget,
    members,
    reload,
    goSection,
  };

  const spent = summary?.spent ?? [];
  const committed = summary?.committed ?? [];
  const verified = summary?.counts?.verified ?? orders.filter((o) => o.status === "verified").length;
  const lost = summary?.counts?.lost ?? orders.filter((o) => o.status === "lost").length;
  const awaitingPay = summary?.counts?.published ?? orders.filter((o) => o.status === "published").length;
  const inPipe = statusesInPipe(summary, orders);
  const openCand = candidates.filter((c) => c.status === "not_in_catalog").length;
  const budgetRows = budget?.budgets ?? [];

  return (
    <>
      <PageHeader
        title="Link orders"
        description="Paid placements from our own donor catalog. Prices are what we pay the donor — there is no commission or client price. Approve and Paid are reserved for the brand's designated approver, and Paid only after a passing live-link check."
        action={
          <div className="flex flex-wrap gap-2">
            <Button variant="ghost" onClick={() => setSettingsOpen((v) => !v)}>
              Settings
            </Button>
            {canManage ? (
              <Button onClick={() => goSection("catalog")}>New request</Button>
            ) : null}
          </div>
        }
      />
      {error ? <ErrorText>{error}</ErrorText> : null}
      {!ready ? (
        <Loading />
      ) : (
        <>
          <Stats cols={6}>
            <Stat
              label="Budget"
              value={
                budgetRows.length ? (
                  <span className="flex flex-col gap-0.5">
                    {budgetRows.map((b) => (
                      <span key={b.currency}>{money(b.amount_minor, b.currency)}</span>
                    ))}
                  </span>
                ) : (
                  dash
                )
              }
              sub={budgetRows.length ? "Per brand, currencies kept apart" : "No link_orders budget set"}
            />
            <Stat
              label="Spent"
              value={
                spent.length ? (
                  <span className="flex flex-col gap-0.5">
                    {spent.map((s) => (
                      <span key={s.currency}>{money(s.amount_minor, s.currency)}</span>
                    ))}
                  </span>
                ) : (
                  money(0, budgetRows[0]?.currency ?? "USD")
                )
              }
              sub="Paid to donors, after publication"
            />
            <Stat
              label="Committed"
              value={
                committed.length ? (
                  <span className="flex flex-col gap-0.5">
                    {committed.map((s) => (
                      <span key={s.currency}>{money(s.amount_minor, s.currency)}</span>
                    ))}
                  </span>
                ) : (
                  money(0, budgetRows[0]?.currency ?? "USD")
                )
              }
              sub="Approved or published, not yet paid"
            />
            <Stat label="Verified links" value={verified} sub={`${inPipe} still in the pipeline`} />
            <Stat label="Candidates" value={openCand} sub={`${awaitingPay} published, waiting for Paid`} />
            <Stat label="Lost links" value={lost} sub={lost ? "replacement first" : "none open"} tone={lost ? "red" : undefined} />
          </Stats>

          {settingsOpen ? <div className="mb-4"><SettingsForm data={data} onClose={() => setSettingsOpen(false)} /></div> : null}

          <Tabs value={section} onValueChange={(v) => goSection(v as Section)} className="mt-4">
            <div className="overflow-x-auto">
            <TabsList variant="line">
              {sections.map((s) => (
                <TabsTrigger key={s} value={s}>
                  {sectionLabel[s]}
                </TabsTrigger>
              ))}
            </TabsList>
            </div>
          </Tabs>

          <div className="mt-4">
            {section === "overview" && <Overview data={data} />}
            {section === "catalog" && <Catalog data={data} cart={cart} setCart={setCart} onOpenDonor={setDonor} />}
            {section === "candidates" && <Candidates data={data} />}
            {section === "orders" && <Orders data={data} openId={orderId} onClose={() => { if (orderId) goSection("orders"); }} />}
            {section === "checks" && <Checks data={data} />}
            {section === "impact" && <Impact data={data} />}
            {section === "payments" && <Payments data={data} />}
          </div>
          <DonorSheet donor={donor} onClose={() => setDonor(undefined)} />
        </>
      )}
    </>
  );
}

function statusesInPipe(summary: LinkOrderSummary | null, orders: LinkOrder[]): number {
  const open = ["requested", "quoted", "negotiating", "approved", "published", "paid"] as const;
  if (summary?.counts) return open.reduce((n, s) => n + (summary.counts[s] ?? 0), 0);
  return orders.filter((o) => (open as readonly string[]).includes(o.status)).length;
}
