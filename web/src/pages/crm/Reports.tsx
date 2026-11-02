import { useEffect, useState } from "react";
import type { paths } from "@outflare/sdk";
import {
  AlertCircle,
  ArrowUpRight,
  Building2,
  CheckSquare,
  RefreshCw,
  Users,
} from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, Empty, ErrorText, Loading } from "@/ui";
import { useBrand } from "@/lib/brand";
import { ago } from "@/format";
import type { CatalogInfo } from "@/lib/records";

type Summary =
  paths["/v1/brands/{brand}/crm/summary"]["get"]["responses"][200]["content"]["application/json"];
export function CRMReports({
  ws,
  cat,
  onOpen,
  onTasks,
}: {
  ws: string;
  cat: CatalogInfo;
  onOpen: (id: string) => void;
  onTasks: () => void;
}) {
  const { brand } = useBrand(ws);
  const [data, setData] = useState<Summary | null>(null);
  const [error, setError] = useState("");
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (!brand) return;
    const controller = new AbortController();
    setError("");
    api
      .GET("/v1/brands/{brand}/crm/summary", {
        params: { path: { brand: brand.id } },
        signal: controller.signal,
      })
      .then(({ data, error }) => {
        if (controller.signal.aborted) return;
        if (data) setData(data);
        else setError(errorMessage(error));
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(errorMessage(e));
      });
    return () => controller.abort();
  }, [brand?.id, tick]);
  const stage = cat.fields("deal").find((f) => f.key === "stage");
  const options = stage ? cat.options(stage) : [];
  const label = (key: string) =>
    options.find((o) => o.key === key)?.label ?? (key || "No stage");
  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="font-semibold">Sales overview</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Live relationship, follow-up and pipeline data for this brand.
          </p>
        </div>
        <Button variant="ghost" onClick={() => setTick((n) => n + 1)}>
          <RefreshCw className="size-3.5" />
          Refresh
        </Button>
      </div>
      <ErrorText>{error}</ErrorText>
      {!data && !error && <Loading />}
      {data && (
        <>
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
            {[
              { label: "People", value: data.people, Icon: Users },
              { label: "Companies", value: data.companies, Icon: Building2 },
              {
                label: "Open tasks",
                value: data.open_tasks,
                Icon: CheckSquare,
              },
              {
                label: "Overdue tasks",
                value: data.overdue_tasks,
                Icon: AlertCircle,
              },
            ].map(({ label, value, Icon }) => (
              <div
                key={label}
                className="rounded-xl border bg-card p-4 shadow-xs"
              >
                <div className="flex items-center justify-between text-xs text-muted-foreground">
                  {label}
                  <Icon className="size-4" />
                </div>
                <p className="mt-3 text-2xl font-semibold tabular-nums">
                  {value.toLocaleString()}
                </p>
                {label.includes("tasks") && (
                  <button className="mt-3 text-xs underline" onClick={onTasks}>
                    View follow-ups
                  </button>
                )}
              </div>
            ))}
          </div>
          <section className="rounded-xl border bg-card">
            <header className="border-b p-4">
              <h3 className="text-sm font-semibold">Pipeline by stage</h3>
              <p className="mt-1 text-xs text-muted-foreground">
                Amounts remain separate for each currency. These are recorded
                values, not probability-weighted forecasts.
              </p>
            </header>
            {!data.pipeline.length ? (
              <Empty title="Your pipeline will appear here">
                Create a deal to start tracking sales opportunities.
              </Empty>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-left text-sm">
                  <thead className="text-xs text-muted-foreground">
                    <tr>
                      <th className="p-3">Stage</th>
                      <th className="p-3">Deals</th>
                      <th className="p-3">Value</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y">
                    {data.pipeline.map((s) => (
                      <tr key={`${s.stage}:${s.currency}`}>
                        <td className="p-3">{label(s.stage)}</td>
                        <td className="p-3 tabular-nums">{s.count}</td>
                        <td className="p-3 tabular-nums">
                          {s.currency
                            ? money(s.amount_minor, s.currency)
                            : "No value set"}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
          <section className="rounded-xl border bg-card">
            <header className="border-b p-4">
              <h3 className="text-sm font-semibold">
                Deals that need attention
              </h3>
              <p className="mt-1 text-xs text-muted-foreground">
                Open deals with no record update in 14 days. Oldest first, up to
                20.
              </p>
            </header>
            {data.stale_deals.length ? (
              <div className="divide-y">
                {data.stale_deals.map((d) => (
                  <button
                    key={d.id}
                    className="flex w-full items-center gap-3 p-4 text-left hover:bg-muted/30"
                    onClick={() => onOpen(d.id)}
                  >
                    <span className="min-w-0 flex-1">
                      <span className="block text-sm font-medium">
                        {d.name || "Unnamed deal"}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {label(d.stage)} · Updated {ago(d.updated_at)}
                      </span>
                    </span>
                    <ArrowUpRight className="size-4 text-muted-foreground" />
                  </button>
                ))}
              </div>
            ) : (
              <p className="p-6 text-sm text-muted-foreground">
                No stale open deals.
              </p>
            )}
          </section>
        </>
      )}
    </div>
  );
}
function money(amount: number, currency: string) {
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency,
    }).format(amount / 100);
  } catch {
    return `${amount / 100} ${currency}`;
  }
}
