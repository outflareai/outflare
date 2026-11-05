// A brand's Presence web searches: Google results read through the
// workspace's own search results connection (DataForSEO or Geonode), every
// day or every few days. Each result that isn't the brand's own page
// becomes a find to review.
import { useEffect, useState } from "react";
import { api, errorMessage } from "../../api";
import { appPath } from "../../lib/paths";
import { Button, ErrorText, field, toast } from "../../ui";
import { label, list, save, type Row } from "./client";

const every: [number, string][] = [
  [1, "Daily"],
  [7, "Weekly"],
  [30, "Monthly"],
];
const often = (n: unknown) => every.find(([d]) => d === n)?.[1] ?? `Every ${n} days`;

export function WebSearches({ ws, brand, manage }: { ws: string; brand: string; manage: boolean }) {
  const [rows, setRows] = useState<Row[]>([]),
    [query, setQuery] = useState(""),
    [days, setDays] = useState(1),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [reload, setReload] = useState(0);
  useEffect(() => {
    let active = true;
    list(brand, "searches", { limit: 200 })
      .then((r) => active && setRows(r.data))
      .catch((e) => active && setError(String(e)));
    return () => {
      active = false;
    };
  }, [brand, reload]);
  const act = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      setReload((n) => n + 1);
      toast(done);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  const run = (id: string) =>
    act(async () => {
      const { error } = await api.POST("/v1/brands/{brand}/coverage/searches/{id}/run", { params: { path: { brand, id } } });
      if (error) throw new Error(errorMessage(error));
    }, "Search queued; its finds appear in this list when it's done");
  return (
    <details className="rounded-xl border p-4" open={rows.length > 0}>
      <summary className="cursor-pointer font-medium">Web searches · {rows.length}</summary>
      <div className="mt-4 space-y-4">
        <p className="text-sm text-muted-foreground">
          Outflare reads the Google results for each search through your own{" "}
          <a className="text-brand underline" href={appPath(ws, "connectors")}>
            DataForSEO or Geonode connection
          </a>
          , within its budget. Every page that isn't yours becomes a find, with its type guessed from the address. The
          brand's name is searched from the start.
        </p>
        <ErrorText>{error}</ErrorText>
        {rows.map((r) => (
          <div key={r.id} className="flex flex-wrap items-center gap-3 border-b pb-3 text-sm">
            <div className="min-w-0 flex-1">
              <strong className="break-words">{String(r.query)}</strong>
              <p className="text-muted-foreground">
                {r.enabled ? often(r.every_days) : "Paused"}
                {r.last_run_at ? ` · last run ${new Date(String(r.last_run_at)).toLocaleDateString()}, ${r.last_found} new` : " · not run yet"}
              </p>
              {r.last_error ? (
                <p className="text-amber-500">
                  {label(String(r.last_error))}
                  {r.last_error === "connector_needed" && (
                    <>
                      {" "}
                      ·{" "}
                      <a className="underline" href={appPath(ws, "connectors")}>
                        Connect one
                      </a>
                    </>
                  )}
                </p>
              ) : null}
            </div>
            {manage && (
              <>
                <Button variant="ghost" disabled={busy} onClick={() => run(r.id)}>
                  Run now
                </Button>
                <Button
                  variant="ghost"
                  disabled={busy}
                  onClick={() => act(() => save(brand, "searches", { enabled: !r.enabled }, r), r.enabled ? "Paused" : "Resumed")}
                >
                  {r.enabled ? "Pause" : "Resume"}
                </Button>
              </>
            )}
          </div>
        ))}
        {manage && (
          <form
            className="flex flex-wrap gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              act(async () => {
                await save(brand, "searches", { query, every_days: days });
                setQuery("");
              }, "Web search saved");
            }}
          >
            <input
              className={`${field} flex-1`}
              aria-label="Web search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder='"Your brand" alternatives'
              maxLength={300}
              required
            />
            <select className={`${field} !w-auto`} aria-label="How often" value={days} onChange={(e) => setDays(Number(e.target.value))}>
              {every.map(([d, name]) => (
                <option key={d} value={d}>
                  {name}
                </option>
              ))}
            </select>
            <Button type="submit" disabled={busy}>
              Save search
            </Button>
          </form>
        )}
      </div>
    </details>
  );
}
