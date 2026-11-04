import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";
import { ago } from "../format";

// Transactions (docs/transactions.md): a brand's payments, refunds,
// chargebacks and credits, from billing connectors and the API.

type Transaction = Schemas["Transaction"];
type Kind = Transaction["kind"];

const kindLabels: Record<Kind, string> = { payment: "Payment", refund: "Refund", chargeback: "Chargeback", credit: "Credit" };

// money shows minor units of a currency, such as 4900 USD as $49.00.
export function money(minor: number | null | undefined, currency: string): string {
  if (minor === null || minor === undefined) return "–";
  const digits = ["JPY", "KRW", "ISK", "HUF"].includes(currency) ? 0 : 2;
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency }).format(minor / 10 ** digits);
  } catch {
    return `${(minor / 10 ** digits).toFixed(digits)} ${currency}`;
  }
}

// signed is the amount with the way the money went: refunds and
// chargebacks went back.
function signed(t: Transaction, minor: number | null, currency: string): string {
  const out = t.kind === "refund" || t.kind === "chargeback";
  return minor === null ? "–" : `${out ? "−" : ""}${money(minor, currency)}`;
}

function useTransactions(brand: string, record?: string, kind?: Kind | "") {
  const [list, setList] = useState<Transaction[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(
    async (cursor?: string) => {
      const query = { limit: 50, ...(record ? { record } : {}), ...(kind ? { kind } : {}), ...(cursor ? { cursor } : {}) };
      const { data, error } = await api.GET("/v1/brands/{brand}/transactions", { params: { path: { brand }, query } });
      if (!data) return setError(errorMessage(error));
      setList((l) => (cursor ? [...(l ?? []), ...data.data] : data.data));
      setNext(data.next_cursor ?? null);
    },
    [brand, record, kind],
  );
  useEffect(() => {
    load();
  }, [load]);
  const drop = (id: string) => setList((l) => l?.filter((t) => t.id !== id) ?? l);
  return { list, next, error, more: () => next && load(next), drop };
}

// deleteTransaction asks, then deletes one transaction into Recently
// deleted. True when it went.
async function deleteTransaction(brand: string, t: Transaction): Promise<boolean> {
  const ok = await confirmAction(
    `Delete this ${kindLabels[t.kind].toLowerCase()}?`,
    <>
      <p>
        {signed(t, t.amount_minor, t.currency)} from {t.source} is hidden from transactions, Query and the person's page.
        A billing sync won't add it back while it is deleted.
      </p>
      <p>You can restore it from People → Recently deleted for 7 days; then it is gone for good.</p>
    </>,
    "Delete",
  );
  if (!ok) return false;
  const { error } = await api.POST("/v1/brands/{brand}/transactions/{transaction}/delete", { params: { path: { brand, transaction: t.id } } });
  if (error) {
    toast(errorMessage(error), "error");
    return false;
  }
  toast("Deleted. Restore it from Recently deleted within 7 days.");
  return true;
}

// Transactions is the Transactions tab of People.
export function Transactions({ ws, canManage, tabs, onOpen }: { ws: string; canManage: boolean; tabs: ReactNode; onOpen: (item: string) => void }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("transactions", "Transactions"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Transactions"
        description="Payments, refunds, chargebacks and credits, from your billing connections (from when they were connected) and your own billing through the API. Each amount is shown in its currency and the brand's."
      />
      {tabs}
      {brand ? (
        <TransactionsFor key={brand.id} brand={brand.id} canManage={canManage} onOpen={onOpen} />
      ) : (
        <Empty title="Pick a brand">
          Transactions belong to one brand. Pick {active.length > 1 ? `one of your ${active.length} brands` : "your brand"} in the brand switcher to see its transactions.
        </Empty>
      )}
    </div>
  );
}

function TransactionsFor({ brand, canManage, onOpen }: { brand: string; canManage: boolean; onOpen: (item: string) => void }) {
  const [kind, setKind] = useState<Kind | "">("");
  const { list, next, error, more, drop } = useTransactions(brand, undefined, kind);
  return (
    <div className="space-y-3">
      <select aria-label="Kind" className={field} value={kind} onChange={(e) => setKind(e.target.value as Kind | "")}>
        <option value="">Every kind</option>
        {(Object.keys(kindLabels) as Kind[]).map((k) => <option key={k} value={k}>{kindLabels[k]}s</option>)}
      </select>
      <ErrorText>{error}</ErrorText>
      {list === null && !error && <Loading />}
      {list && (
        <DataTable
          data={list}
          rowId={(t) => t.id}
          searchPlaceholder="Search transactions"
          empty="No transactions yet. They come from a billing connection (Settings, Connections) or POST /v1/brands/{brand}/transactions."
          mobile={(t) => (
            <div className="space-y-0.5">
              <div className="font-medium">{kindLabels[t.kind]} {signed(t, t.amount_minor, t.currency)}</div>
              <div className="text-[12px] text-muted-foreground">{t.source} · {ago(t.occurred_at)}</div>
            </div>
          )}
          columns={[
            { id: "kind", header: "Kind", accessorFn: (t) => t.kind, cell: ({ row: { original: t } }) => <span>{kindLabels[t.kind]} {t.status !== "succeeded" && <Badge tone="amber">{t.status}</Badge>}</span> },
            { id: "amount", header: "Amount", accessorFn: (t) => t.amount_minor, cell: ({ row: { original: t } }) => <span className="tabular-nums">{signed(t, t.amount_minor, t.currency)}</span> },
            { id: "brand_amount", header: "In the brand's currency", accessorFn: (t) => t.brand_amount_minor ?? -1, cell: ({ row: { original: t } }) => <span className="tabular-nums text-muted-foreground">{signed(t, t.brand_amount_minor, t.brand_currency)}</span> },
            { id: "who", header: "Who", enableSorting: false, cell: ({ row: { original: t } }) => (t.record_id ? <button type="button" className="underline" onClick={() => onOpen(t.record_id!)}>Open</button> : <span className="text-muted-foreground">–</span>) },
            { id: "source", header: "From", accessorFn: (t) => t.source, cell: ({ row: { original: t } }) => <span className="font-mono text-[12px]">{t.source}</span> },
            { id: "at", header: "When", accessorFn: (t) => t.occurred_at, cell: ({ row: { original: t } }) => <span className="text-muted-foreground">{ago(t.occurred_at)}</span> },
            ...(canManage ? [{
              id: "actions",
              header: "",
              enableSorting: false,
              cell: ({ row: { original: t } }) => (
                <Button variant="ghost" onClick={async () => (await deleteTransaction(brand, t)) && drop(t.id)}>
                  Delete
                </Button>
              ),
            } satisfies ColumnDef<Transaction, unknown>] : []),
          ]}
        />
      )}
      {next && <Button variant="ghost" onClick={more}>Show more</Button>}
    </div>
  );
}

// RecordTransactions is the transactions card on a person's or company's
// page: what they paid and got back.
export function RecordTransactions({ brand, record }: { brand: string; record: string }) {
  const { list, next, error, more } = useTransactions(brand, record);
  if (list && list.length === 0) return null;
  const brandCurrency = list?.[0]?.brand_currency ?? "USD";
  const net = (list ?? []).reduce((sum, t) => (t.status !== "succeeded" || t.brand_amount_minor === null ? sum : sum + (t.kind === "refund" || t.kind === "chargeback" ? -1 : 1) * t.brand_amount_minor), 0);
  return (
    <Card title="Transactions" description={list ? `${money(net, brandCurrency)} net of refunds, in the brand's currency.` : undefined}>
      <ErrorText>{error}</ErrorText>
      {list === null && !error && <Loading />}
      {list && (
        <ul className="divide-y text-[13px]">
          {list.map((t) => (
            <li key={t.id} className="flex flex-wrap items-center gap-x-2 py-1.5">
              <span className="font-medium tabular-nums">{signed(t, t.amount_minor, t.currency)}</span>
              <span>{kindLabels[t.kind]}</span>
              {t.status !== "succeeded" && <Badge tone="amber">{t.status}</Badge>}
              <span className="ml-auto text-[12px] text-muted-foreground">{ago(t.occurred_at)}</span>
            </li>
          ))}
        </ul>
      )}
      {next && <Button variant="ghost" onClick={more}>Show more</Button>}
    </Card>
  );
}
