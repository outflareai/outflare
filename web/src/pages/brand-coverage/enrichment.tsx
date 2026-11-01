import { useEffect, useState } from "react";
import { api, errorMessage } from "../../api";
import { Badge, Button, ErrorText, Loading, toast } from "../../ui";
import { DataTable } from "../../components/data-table";
import { label, type Resource, type Row } from "./client";

type Suggestion = { field: string; value: unknown; quote: string };
type Extraction = {
  status: string;
  source_version: number;
  job_id?: number;
  error_code?: string;
  suggestions: Suggestion[];
};
export function CoverageExtraction({
  brand,
  document,
  manage,
  review,
}: {
  brand: string;
  document: Row;
  manage: boolean;
  review: (resource: Resource, row: Row) => void;
}) {
  const [result, setResult] = useState<Extraction | undefined>(
      document.extraction as Extraction | undefined,
    ),
    [selected, setSelected] = useState<string[]>([]),
    [error, setError] = useState(""),
    [queued, setQueued] = useState(false),
    [jobBefore, setJobBefore] = useState<number | undefined>();
  const read = async () => {
    const r = await api.GET("/v1/brands/{brand}/coverage/documents/{id}", {
      params: { path: { brand, id: document.id } },
    });
    if (r.error) throw new Error(errorMessage(r.error));
    const next = r.data?.extraction as Extraction | undefined;
    if (next) {
      setResult(next);
      if (next.job_id !== jobBefore) setQueued(false);
    }
  };
  useEffect(() => {
    if (!queued) return;
    const timer = setInterval(
      () => void read().catch((e) => setError(String(e))),
      5000,
    );
    return () => clearInterval(timer);
  }, [brand, document.id, queued, jobBefore]);
  const current = result?.source_version === document.version;
  const currency = String(
    result?.suggestions.find((s) => s.field === "currency")?.value ?? "",
  );
  const suggestionValue = (s: Suggestion) => {
    if (s.field === "amount_minor" && /^[A-Z]{3}$/.test(currency)) {
      const formatter = new Intl.NumberFormat(undefined, {
        style: "currency",
        currency,
      });
      const digits = formatter.resolvedOptions().maximumFractionDigits ?? 2;
      return formatter.format(Number(s.value) / 10 ** digits);
    }
    return String(s.value ?? "Unknown");
  };
  return (
    <section className="space-y-3 rounded-xl border p-4">
      <h3 className="font-medium">Extract agreement terms</h3>
      <p className="text-sm text-muted-foreground">
        Paste the source document text when editing this document. AI can
        suggest dates, currency, amounts and terms with exact quotes. Review the
        selection, then save through the agreement form.
      </p>
      <ErrorText>{error}</ErrorText>
      <div className="flex gap-2">
        {manage && (
          <Button
            disabled={queued || !document.source_text}
            onClick={async () => {
              try {
                setJobBefore(result?.job_id);
                const r = await api.POST(
                  "/v1/brands/{brand}/coverage/documents/{id}/extract",
                  { params: { path: { brand, id: document.id } } },
                );
                if (r.error) throw new Error(errorMessage(r.error));
                if (r.data && "queued" in r.data) {
                  setQueued(true);
                  toast("Term extraction queued");
                } else toast("Extraction is waiting for mission approval");
              } catch (e) {
                setError(String(e));
              }
            }}
          >
            {queued ? "Extracting…" : "Extract terms"}
          </Button>
        )}
        <Button
          variant="ghost"
          onClick={() => void read().catch((e) => setError(String(e)))}
        >
          Refresh result
        </Button>
      </div>
      {result && (
        <>
          <Badge tone={current ? "blue" : "grey"}>
            {current
              ? label(result.status)
              : "Source text changed; extract again"}
          </Badge>
          {result.error_code && (
            <p className="text-sm text-amber-500">
              {label(result.error_code)}. Check your workspace AI connection and
              budget.
            </p>
          )}
          {result.suggestions.map((s) => (
            <label
              key={s.field}
              className="block space-y-2 rounded-lg border p-3 text-sm"
            >
              <span className="flex gap-2">
                <input
                  type="checkbox"
                  disabled={!current || !manage}
                  checked={selected.includes(s.field)}
                  onChange={(e) =>
                    setSelected((old) =>
                      e.target.checked
                        ? [...old, s.field]
                        : old.filter((f) => f !== s.field),
                    )
                  }
                />
                <strong>{label(s.field.replace(/_minor$/, ""))}</strong>
                <span className="break-words">{suggestionValue(s)}</span>
              </span>
              <blockquote className="border-l-2 pl-3 text-muted-foreground">
                {s.quote}
              </blockquote>
            </label>
          ))}
          {manage && (
            <Button
              disabled={!current || !selected.length}
              onClick={async () => {
                try {
                  const r = await api.GET(
                    "/v1/brands/{brand}/coverage/agreements/{id}",
                    {
                      params: {
                        path: { brand, id: String(document.agreement_id) },
                      },
                    },
                  );
                  if (r.error) throw new Error(errorMessage(r.error));
                  if (r.data) {
                    const fields = Object.fromEntries(
                      result.suggestions
                        .filter((s) => selected.includes(s.field))
                        .map((s) => [s.field, s.value]),
                    );
                    review("agreements", { ...r.data, ...fields } as Row);
                  }
                } catch (e) {
                  setError(String(e));
                }
              }}
            >
              Review selected fields in agreement
            </Button>
          )}
          <p className="text-xs text-muted-foreground">
            Extraction does not change the agreement. Saving revised terms
            clears any prior approval; commercial validation still applies.
          </p>
        </>
      )}
    </section>
  );
}
export function CoverageOutcomes({
  brand,
  from,
  to,
}: {
  brand: string;
  from?: string;
  to?: string;
}) {
  const [data, setData] = useState<Record<string, unknown>>(),
    [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    setError("");
    api
      .GET("/v1/brands/{brand}/coverage/outcomes", {
        params: { path: { brand }, query: { from, to } },
      })
      .then((r) => {
        if (!active) return;
        if (r.error) setError(errorMessage(r.error));
        else setData(r.data);
      })
      .catch((e) => active && setError(String(e)));
    return () => {
      active = false;
    };
  }, [brand, from, to]);
  return (
    <section className="space-y-3 rounded-xl border p-5">
      <h2 className="font-semibold">Brand-wide recorded outcomes</h2>
      <ErrorText>{error}</ErrorText>
      {!data && !error ? (
        <Loading />
      ) : (
        data && (
          <>
            <p className="text-sm text-muted-foreground">
              {label(String(data.model))} · {String(data.lookback_days)}-day
              lookback · All tracked publisher websites for this brand.
              Placement filters above do not change this attribution view.
            </p>
            {data.measurement_state === "no_sessions_observed" ? (
              <p className="text-sm text-muted-foreground">
                No website sessions have been recorded for this brand. Delivery
                reports and discovered links cannot supply missing attribution.
              </p>
            ) : (
              <DataTable
                data={(data.data ?? []) as Record<string, unknown>[]}
                rowId={(r) => String(r.domain)}
                columns={[
                  { accessorKey: "domain", header: "Publisher website" },
                  { accessorKey: "sessions", header: "Observed sessions" },
                  {
                    accessorKey: "credited_conversions",
                    header: "Credited conversions",
                  },
                  {
                    accessorKey: "revenue",
                    header: "Attributed revenue",
                    cell: ({
                      row,
                    }: {
                      row: { original: Record<string, unknown> };
                    }) =>
                      (
                        (row.original.revenue ?? []) as {
                          currency: string;
                          amount_minor: number;
                        }[]
                      )
                        .map((r) => {
                          try {
                            const digits =
                              new Intl.NumberFormat(undefined, {
                                style: "currency",
                                currency: r.currency,
                              }).resolvedOptions().maximumFractionDigits ?? 2;
                            return new Intl.NumberFormat(undefined, {
                              style: "currency",
                              currency: r.currency,
                            }).format(r.amount_minor / 10 ** digits);
                          } catch {
                            return `${r.amount_minor} ${r.currency}`;
                          }
                        })
                        .join(" · ") || "—",
                  },
                ]}
              />
            )}
            <p className="text-xs text-muted-foreground">
              {String(data.measurement_note)}
            </p>
            {data.attribution_refreshed_at && (
              <p className="text-xs text-muted-foreground">
                Attribution refreshed{" "}
                {new Date(
                  String(data.attribution_refreshed_at),
                ).toLocaleString()}
              </p>
            )}
          </>
        )
      )}
    </section>
  );
}
