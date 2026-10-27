import { useEffect, useState } from "react";
import type { ImportPreview, ImportRun, Integration } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, ErrorText, Loading } from "../ui";
import { plural } from "../format";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";

// Moving over from another sequencer: preview what the account holds, pick
// campaigns, import, and show what happened and what's left to do.
export function ImportDialog({ ws, integration, onClose, go }: { ws: string; integration: Integration; onClose: () => void; go: (tab: "sequences" | "mailboxes") => void }) {
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [error, setError] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [leads, setLeads] = useState(true);
  const [run, setRun] = useState<ImportRun | null>(null);
  const [busy, setBusy] = useState(false);
  const tool = integration.connector_name;

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/integrations/{id}/import-preview", { params: { path: { ws, id: integration.id } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setPreview(data);
      setPicked(new Set(data.campaigns.filter((c) => c.status !== "completed" && c.status !== "draft").map((c) => c.id)));
    });
  }, [ws, integration.id]);

  // Follow the run until it finishes.
  useEffect(() => {
    if (!run || run.status === "done" || run.status === "failed") return;
    const t = setTimeout(async () => {
      const { data } = await api.GET("/v1/workspaces/{ws}/imports/{id}", { params: { path: { ws, id: run.id } } });
      if (data) setRun(data);
    }, 1500);
    return () => clearTimeout(t);
  }, [run, ws]);

  async function start() {
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/integrations/{id}/imports", { params: { path: { ws, id: integration.id } }, body: { campaign_ids: [...picked], leads, mailboxes: true } });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setRun(data);
  }

  const toggle = (id: string) => setPicked((p) => { const n = new Set(p); if (n.has(id)) n.delete(id); else n.add(id); return n; });
  const missing = run?.summary.mailboxes?.missing ?? preview?.mailboxes.missing ?? [];
  const csv = "email,password,display_name,daily_limit\n" + missing.map((m) => `${m.email},,${(m.name ?? "").replace(/,/g, " ")},${m.daily_limit ?? ""}`).join("\n") + "\n";
  const missingNote = missing.length > 0 && (
    <div className="rounded-lg border border-amber-500/30 bg-amber-500/[0.06] p-3 text-[13px]">
      <p className="font-medium">{plural(missing.length, "sending account")} from {tool} {missing.length === 1 ? "isn't" : "aren't"} connected here yet</p>
      <p className="mt-0.5 text-[12px] text-muted-foreground">{tool} never shares passwords, so connect them once: sign in with Google or Microsoft, or fill in app passwords in this CSV and import it on Mailboxes. Until then, imported leads are sent from your other mailboxes.</p>
      <div className="mt-2 flex flex-wrap gap-2">
        <a className="inline-flex h-8 items-center rounded-md border px-3 text-[13px] hover:bg-accent" download={`${integration.connector}-mailboxes.csv`} href={`data:text/csv;charset=utf-8,${encodeURIComponent(csv)}`}>Download the CSV</a>
        <Button variant="ghost" onClick={() => { onClose(); go("mailboxes"); }}>Go to Mailboxes</Button>
      </div>
    </div>
  );

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader className="text-left">
          <DialogTitle className="text-[15px]">Import from {tool}</DialogTitle>
          <DialogDescription className="text-[13px]">Campaigns come over as draft sequences, so nothing sends until you start them. Leads keep the step they reached and the mailbox that wrote to them. Bounced and unsubscribed leads go on your suppression list. Nothing in {tool} changes, and running it again won't duplicate anything.</DialogDescription>
        </DialogHeader>

        {!preview && !error && <Loading />}

        {preview && !run && (
          <div className="space-y-4">
            {preview.campaigns.length === 0 ? (
              <p className="text-[13px] text-muted-foreground">No campaigns in this {tool} account.</p>
            ) : (
              <fieldset className="space-y-1">
                <legend className="mb-1 flex w-full items-center justify-between text-[13px] font-medium">
                  <span>Campaigns ({picked.size} of {preview.campaigns.length} selected)</span>
                  <button type="button" className="text-[12px] font-normal text-muted-foreground underline" onClick={() => setPicked(picked.size === preview.campaigns.length ? new Set() : new Set(preview.campaigns.map((c) => c.id)))}>
                    {picked.size === preview.campaigns.length ? "Select none" : "Select all"}
                  </button>
                </legend>
                <div className="max-h-72 divide-y overflow-y-auto rounded-lg border">
                  {preview.campaigns.map((c) => (
                    <label key={c.id} className="flex cursor-pointer items-start gap-3 px-3 py-2 hover:bg-accent/40">
                      <input type="checkbox" className="mt-1" checked={picked.has(c.id)} onChange={() => toggle(c.id)} />
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center gap-2 font-medium">{c.name}<Badge tone={c.status === "active" ? "green" : "grey"}>{c.status}</Badge></span>
                        <span className="block text-[12px] text-muted-foreground">{plural(c.steps.length, "step")} · {plural(c.mailbox_emails.length, "mailbox", "mailboxes")}{c.daily_limit ? ` · ${c.daily_limit}/day` : ""}</span>
                      </span>
                    </label>
                  ))}
                </div>
              </fieldset>
            )}
            <label className="flex items-start justify-between gap-4 rounded-lg border p-3">
              <span className="text-[13px]">
                <span className="block font-medium">Bring the leads too</span>
                <span className="block text-[12px] text-muted-foreground">New addresses are verified before their first email, as usual.</span>
              </span>
              <Switch checked={leads} onCheckedChange={setLeads} />
            </label>
            <p className="text-[13px]">Sending accounts: {preview.mailboxes.matched} of {preview.mailboxes.total} already connected here.</p>
            {missingNote}
          </div>
        )}

        {run && (
          <div className="space-y-3 text-[13px]">
            {(run.status === "queued" || run.status === "running") && <p aria-live="polite">Importing from {tool}… this can take a few minutes for large accounts. You can close this; it keeps going.</p>}
            {run.status === "failed" && <ErrorText>The import stopped: {run.error}</ErrorText>}
            {run.status === "done" && (
              <>
                <p className="font-medium">Done. {plural(run.summary.campaigns.length, "campaign")} imported as draft sequences.</p>
                <p className="text-muted-foreground">
                  {plural(run.summary.leads.seen, "lead")} ({run.summary.leads.created.toLocaleString()} new), {run.summary.leads.enrolled.toLocaleString()} enrolled where they left off, {run.summary.leads.suppressed.toLocaleString()} added to suppression, {run.summary.leads.skipped.toLocaleString()} not enrolled because they replied, finished or opted out.
                </p>
                <ul className="divide-y rounded-lg border">
                  {run.summary.campaigns.map((c) => (
                    <li key={c.external_id} className="px-3 py-2">
                      <div className="flex items-center justify-between gap-2"><span className="font-medium">{c.name}</span><span className="text-[12px] text-muted-foreground">{plural(c.steps, "step")} · {c.enrolled.toLocaleString()} enrolled{c.created ? "" : " · updated"}</span></div>
                      {c.warnings.length > 0 && <ul className="mt-1 list-disc pl-5 text-[12px] text-amber-700 dark:text-amber-400">{c.warnings.map((w) => <li key={w}>{w}</li>)}</ul>}
                    </li>
                  ))}
                </ul>
              </>
            )}
            {run.status !== "queued" && run.status !== "running" && missingNote}
          </div>
        )}

        <ErrorText>{error}</ErrorText>
        <DialogFooter className="flex-row justify-end gap-2">
          {!run && <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>}
          {!run && preview && <Button disabled={busy || picked.size === 0} onClick={start}>{busy ? "Starting…" : `Import ${plural(picked.size, "campaign")}`}</Button>}
          {run?.status === "done" && <Button onClick={() => { onClose(); go("sequences"); }}>Review the sequences</Button>}
          {run && run.status !== "done" && <Button variant="ghost" onClick={onClose}>Close</Button>}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
