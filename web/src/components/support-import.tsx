import { useCallback, useEffect, useState } from "react";
import type { SupportImport } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, confirmAction, ErrorText, field, Loading, toast } from "../ui";
import { Icon } from "../icons";
import { ago } from "../format";
import { cn } from "@/lib/utils";
import { useBrand } from "@/lib/brand";
import type { Tab } from "../App";

// Intercom: keep answering there while Outflare keeps a copy of every
// conversation (with what Fin did) in the picked brand's support inbox.

type Connection = { id: string; name: string; connector: string; status: string };

const statusText: Record<SupportImport["status"], string> = {
  syncing: "Copying now",
  idle: "Up to date",
  paused: "Paused",
  failed: "Failing",
};

export function SupportImports({ ws, canManage, go }: { ws: string; canManage: boolean; go: (tab: Tab) => void }) {
  const { brand } = useBrand(ws);
  const [list, setList] = useState<SupportImport[] | null>(null);
  const [conns, setConns] = useState<Connection[]>([]);
  const [pick, setPick] = useState("");
  const [recent, setRecent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    if (!brand) return;
    const [imps, ints] = await Promise.all([
      api.GET("/v1/brands/{brand}/support/imports", { params: { path: { brand: brand.id } } }),
      api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws }, query: { brand: brand.handle } } }),
    ]);
    if (imps.error) return setError(errorMessage(imps.error));
    setList(imps.data.data);
    setConns(((ints.data as { data?: Connection[] } | undefined)?.data ?? []).filter((c) => c.connector === "intercom"));
  }, [ws, brand]);

  useEffect(() => {
    setList(null);
    void load();
  }, [load]);

  // While copying, look again every few seconds.
  useEffect(() => {
    if (!list?.some((i) => i.status === "syncing")) return;
    const t = setTimeout(load, 4000);
    return () => clearTimeout(t);
  }, [list, load]);

  if (!brand) {
    return (
      <section className="space-y-1 rounded-lg border p-4">
        <p className="text-[13px] font-medium">Intercom</p>
        <p className="text-xs text-muted-foreground">Pick a brand at the top to copy its Intercom conversations here.</p>
      </section>
    );
  }

  const free = conns.filter((c) => !list?.some((i) => i.integration_id === c.id));

  async function start() {
    if (!brand || !pick) return;
    setBusy(true);
    setError("");
    const history_from = recent ? new Date(Date.now() - 90 * 86400_000).toISOString() : null;
    const { error } = await api.POST("/v1/brands/{brand}/support/imports", { params: { path: { brand: brand.id } }, body: { integration_id: pick, history_from } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setPick("");
    toast("Copying your Intercom conversations");
    void load();
  }

  async function act(i: SupportImport, what: "sync" | "pause" | "resume" | "stop") {
    if (!brand) return;
    const path = { brand: brand.id, id: i.id };
    if (what === "stop") {
      const ok = await confirmAction("Stop copying from Intercom?", "Conversations copied so far stay in Support. New ones in Intercom won't come over.", "Stop copying");
      if (!ok) return;
    }
    const res =
      what === "sync"
        ? await api.POST("/v1/brands/{brand}/support/imports/{id}/sync", { params: { path } })
        : what === "stop"
          ? await api.DELETE("/v1/brands/{brand}/support/imports/{id}", { params: { path } })
          : await api.PATCH("/v1/brands/{brand}/support/imports/{id}", { params: { path }, body: { paused: what === "pause" } });
    if (res.error) return toast(errorMessage(res.error), "error");
    void load();
  }

  return (
    <section className="space-y-3 rounded-lg border p-4">
      <div className="space-y-1">
        <p className="text-[13px] font-medium">Intercom</p>
        <p className="text-xs text-muted-foreground">
          Keep answering in Intercom: every conversation, with its notes, ratings and what Fin did, is copied here for {brand.name} and kept in step every few
          minutes, and each customer joins your people. Copied conversations are read-only here.
        </p>
      </div>
      {list === null ? (
        <Loading />
      ) : (
        <>
          {list.length > 0 && (
            <ul className="divide-y rounded-md border">
              {list.map((i) => (
                <li key={i.id} className="flex flex-wrap items-center gap-2 px-3 py-2.5 text-[13px]">
                  <span className={cn("size-1.5 shrink-0 rounded-full", { syncing: "bg-blue-600", idle: "bg-emerald-600", paused: "bg-muted-foreground/50", failed: "bg-red-600" }[i.status])} />
                  <div className="min-w-0 flex-1">
                    <p className="font-medium">
                      {statusText[i.status]}
                      <span className="ml-1 font-normal text-muted-foreground">
                        · {i.conversations.toLocaleString()} conversations, {i.messages.toLocaleString()} messages
                      </span>
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {i.error ? (
                        <span className="text-destructive">
                          {i.error}
                          {i.error_code === "connector_auth" && (
                            <button type="button" className="ml-1 underline" onClick={() => go("connectors")}>Reconnect Intercom</button>
                          )}
                        </span>
                      ) : i.last_synced_at ? (
                        `Checked ${ago(i.last_synced_at)}`
                      ) : (
                        "Reading your history; a long one takes a while"
                      )}
                      {i.history_from && ` · since ${new Date(i.history_from).toLocaleDateString()}`}
                    </p>
                  </div>
                  {canManage && (
                    <div className="flex gap-1">
                      {i.status !== "paused" && i.status !== "syncing" && <Button variant="ghost" onClick={() => act(i, "sync")}>Sync now</Button>}
                      {i.status === "paused" ? (
                        <Button variant="ghost" onClick={() => act(i, "resume")}>Resume</Button>
                      ) : (
                        <Button variant="ghost" onClick={() => act(i, "pause")}>Pause</Button>
                      )}
                      <Button variant="ghost" aria-label="Stop copying" onClick={() => act(i, "stop")}><Icon name="x" className="size-3.5" /></Button>
                    </div>
                  )}
                </li>
              ))}
            </ul>
          )}
          {canManage && free.length > 0 && (
            <div className="space-y-2">
              <div className="flex flex-col gap-2 sm:flex-row">
                <select aria-label="Intercom connection" className={cn(field, "flex-1")} value={pick} onChange={(e) => setPick(e.target.value)}>
                  <option value="">Choose a connected Intercom…</option>
                  {free.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                </select>
                <Button disabled={!pick || busy} onClick={start}>{busy ? "Starting…" : "Copy conversations"}</Button>
              </div>
              <label className="flex items-center gap-2 text-xs text-muted-foreground">
                <input type="checkbox" checked={recent} onChange={(e) => setRecent(e.target.checked)} />
                Only the last 90 days (all history when unticked)
              </label>
            </div>
          )}
          {canManage && conns.length === 0 && list.length === 0 && (
            <div className="flex items-center justify-between gap-2">
              <p className="text-xs text-muted-foreground">Connect Intercom for this brand under Connectors; copying starts on its own.</p>
              <Button variant="ghost" onClick={() => go("connectors")}>Connect Intercom</Button>
            </div>
          )}
          {error && <ErrorText>{error}</ErrorText>}
        </>
      )}
    </section>
  );
}
