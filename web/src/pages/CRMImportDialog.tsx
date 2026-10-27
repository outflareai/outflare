import { useEffect, useState } from "react";
import type { CRMImport, CRMImportPreview, Integration } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, ErrorText, Loading } from "../ui";
import { plural } from "../format";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

// Moving over from another CRM, once: see what it holds, pick objects,
// import them into customer records, and show what happened.
export function CRMImportDialog({ ws, integration, onClose }: { ws: string; integration: Integration; onClose: () => void }) {
  const [preview, setPreview] = useState<CRMImportPreview | null>(null);
  const [error, setError] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [run, setRun] = useState<CRMImport | null>(null);
  const [busy, setBusy] = useState(false);
  const tool = integration.connector_name;

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/integrations/{id}/crm-import-preview", { params: { path: { ws, id: integration.id } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setPreview(data);
      setPicked(new Set(data.objects.filter((o) => o.records.count > 0).map((o) => o.slug)));
    });
  }, [ws, integration.id]);

  // Follow the import until it finishes.
  useEffect(() => {
    if (!run || run.status === "done" || run.status === "failed") return;
    const t = setTimeout(async () => {
      const { data } = await api.GET("/v1/workspaces/{ws}/crm-imports/{import}", { params: { path: { ws, import: run.id } } });
      if (data) setRun(data);
    }, 1500);
    return () => clearTimeout(t);
  }, [run, ws]);

  async function start() {
    if (!preview) return;
    setBusy(true);
    setError("");
    const objects = picked.size === preview.objects.length ? [] : [...picked];
    const { data, error } = await api.POST("/v1/workspaces/{ws}/integrations/{id}/crm-imports", { params: { path: { ws, id: integration.id } }, body: { objects } });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    if ("id" in data) setRun(data);
  }

  const toggle = (slug: string) => setPicked((p) => { const n = new Set(p); if (n.has(slug)) n.delete(slug); else n.add(slug); return n; });
  const count = (c: { count: number; capped: boolean }) => `${c.count.toLocaleString()}${c.capped ? "+" : ""}`;
  const later = preview && preview.lists.length + preview.notes.count + preview.tasks.count > 0;
  const sum = run?.summary;

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader className="text-left">
          <DialogTitle className="text-[15px]">Import from {tool}</DialogTitle>
          <DialogDescription className="text-[13px]">
            People, companies, deals and your own objects become customer records, with their fields, stages, owners and links. People and companies already here with the same verified email or domain are filled in, not duplicated, and nothing you typed in Outflare is overwritten. Nothing in {tool} changes, and running it again updates the same records.
          </DialogDescription>
        </DialogHeader>

        {!preview && !error && <Loading />}

        {preview && !run && (
          <div className="space-y-4">
            <fieldset className="space-y-1">
              <legend className="mb-1 text-[13px] font-medium">Objects in {preview.workspace} ({picked.size} of {preview.objects.length} selected)</legend>
              <div className="max-h-72 divide-y overflow-y-auto rounded-lg border">
                {preview.objects.map((o) => (
                  <label key={o.slug} className="flex cursor-pointer items-start gap-3 px-3 py-2 hover:bg-accent/40">
                    <input type="checkbox" className="mt-1" checked={picked.has(o.slug)} onChange={() => toggle(o.slug)} />
                    <span className="min-w-0 flex-1">
                      <span className="block font-medium">{o.plural || o.name}</span>
                      <span className="block text-[12px] text-muted-foreground">{count(o.records)} records · {plural(o.fields.filter((f) => f.kind !== "unsupported").length, "field")}</span>
                    </span>
                  </label>
                ))}
              </div>
            </fieldset>
            {later && (
              <p className="text-[12px] text-muted-foreground">
                Lists, notes and tasks ({plural(preview.lists.length, "list")}, {count(preview.notes)} notes, {count(preview.tasks)} tasks) stay in {tool} for now. They come over once Outflare's CRM has them.
              </p>
            )}
          </div>
        )}

        {run && sum && (
          <div className="space-y-3 text-[13px]">
            {(run.status === "queued" || run.status === "running") && (
              <p aria-live="polite">Importing from {tool}… {plural(sum.objects.reduce((n, o) => n + o.seen, 0), "record")} so far. Large CRMs take a while; you can close this and it keeps going.</p>
            )}
            {run.status === "failed" && <ErrorText>The import stopped: {run.error}</ErrorText>}
            {sum.objects.length > 0 && (
              <ul className="divide-y rounded-lg border">
                {sum.objects.map((o) => (
                  <li key={o.object} className="flex items-center justify-between gap-2 px-3 py-2">
                    <span className="font-medium">{o.name}<Badge tone="grey">{o.record_type}</Badge></span>
                    <span className="text-[12px] text-muted-foreground">
                      {o.created.toLocaleString()} new · {o.matched.toLocaleString()} matched · {o.updated.toLocaleString()} updated · {o.unchanged.toLocaleString()} unchanged
                    </span>
                  </li>
                ))}
              </ul>
            )}
            {run.status === "done" && (
              <p className="text-muted-foreground">
                Added {plural(sum.catalog.types_created.length, "record type")}, {plural(sum.catalog.fields_created.length, "field")} and {plural(sum.catalog.option_sets_created.length, "option list")}. {sum.members.matched} of {plural(sum.members.total, "member")} matched by email.
                {sum.placeholders > 0 && ` ${plural(sum.placeholders, "linked record")} ${sum.placeholders === 1 ? "wasn't" : "weren't"} in ${tool} and ${sum.placeholders === 1 ? "is" : "are"} kept as a placeholder.`}
              </p>
            )}
            {sum.warnings.length > 0 && (
              <details className="rounded-lg border p-3">
                <summary className="cursor-pointer text-[12px]">{plural(sum.warnings.length + sum.more_warnings, "value")} left out</summary>
                <ul className="mt-2 list-disc pl-5 text-[12px] text-amber-700 dark:text-amber-400">{sum.warnings.map((w) => <li key={w}>{w}</li>)}</ul>
              </details>
            )}
            {sum.catalog.fields_skipped.length > 0 && run.status === "done" && (
              <details className="rounded-lg border p-3">
                <summary className="cursor-pointer text-[12px]">{plural(sum.catalog.fields_skipped.length, "field")} not imported</summary>
                <ul className="mt-2 list-disc pl-5 text-[12px] text-muted-foreground">{sum.catalog.fields_skipped.map((f) => <li key={f}>{f}</li>)}</ul>
              </details>
            )}
          </div>
        )}

        <ErrorText>{error}</ErrorText>
        <DialogFooter className="flex-row justify-end gap-2">
          {!run && <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>}
          {!run && preview && <Button disabled={busy || picked.size === 0} onClick={start}>{busy ? "Starting…" : `Import ${plural(picked.size, "object")}`}</Button>}
          {run && <Button variant={run.status === "done" ? undefined : "ghost"} onClick={onClose}>{run.status === "done" ? "Done" : "Close"}</Button>}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
