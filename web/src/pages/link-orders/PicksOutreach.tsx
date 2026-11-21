import { useEffect, useRef, useState } from "react";
import { api, errorMessage } from "../../api";
import { track } from "../../lib/analytics";
import { Button, ErrorText, field, toast } from "../../ui";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { idemp, type LinkOrderDonor } from "./shared";

// "Find emails and send to a sequence" for recommended donors: OutflareBot
// searches each site for who to write to (its advertising desk first), and
// the best address of each joins the chosen Outreach sequence, with
// Outreach's usual rules. Nothing is sent outside the sequence's schedule.

type Seq = { id: string; name: string; status: string };

// NEW is the "create a sequence now" choice.
const NEW = "new";
const lastKey = (brand: string) => `outflare.link-orders.outreach-sequence.${brand}`;

function lastSequence(brand: string): string {
  try {
    return window.localStorage.getItem(lastKey(brand)) ?? "";
  } catch {
    return "";
  }
}

function rememberSequence(brand: string, id: string) {
  try {
    window.localStorage.setItem(lastKey(brand), id);
  } catch {
    // A per-browser convenience only.
  }
}

/**
 * pickSequence is the sequence to preselect: the one used last time for
 * donors, else one named for link building, else an active one; with
 * none, a new one is made.
 */
export function pickSequence(list: Seq[], last: string): string {
  if (last && list.some((s) => s.id === last)) return last;
  const linky = list.filter((s) => /link|backlink|placement|guest|outreach|ссыл|донор/i.test(s.name));
  return (linky.find((s) => s.status === "active") ?? linky[0] ?? list.find((s) => s.status === "active"))?.id ?? NEW;
}

// The starting letters for a new sequence; edited before it is made. They
// never name the brand: a site that knows who is buying asks a higher
// price, so the first email comes from a marketer, not from the company.
const NEW_SUBJECT = "Sponsored article on {{company|your site}}";
const NEW_BODY = `Hi {{first_name|there}},

I'm {{sender_name}}, a digital marketing specialist. We would like to publish a sponsored article on {{company|your site}} with one link, or add a link to one of your existing articles.

Could you send your rates and requirements?

Thanks,
{{sender_name}}`;
const NEW_FOLLOW_UP = `Hi {{first_name|there}},

Just checking you saw my note about a sponsored article. Happy to work with your usual terms.

{{sender_name}}`;
type Row = { donor: LinkOrderDonor; state: "searching" | "sending" | "done"; text: string; ok: boolean };

/** How many sites one find-emails request may take (the API's limit). */
const FIND_BATCH = 50;
/** How many sites are waited on and sent at once. */
const WORKERS = 8;

/** inPool runs fn over items, at most n at a time. */
async function inPool<T>(items: T[], n: number, fn: (item: T, i: number) => Promise<void>): Promise<void> {
  let next = 0;
  await Promise.all(Array.from({ length: Math.min(n, items.length) }, async () => {
    while (next < items.length) {
      const i = next++;
      await fn(items[i], i);
    }
  }));
}

/** waitForSearch polls the row until its email search is newer than since. */
async function waitForSearch(brand: string, id: string, since: number, give: () => boolean): Promise<LinkOrderDonor | null> {
  for (let i = 0; i < 40 && !give(); i++) {
    const { data } = await api.GET("/v1/brands/{brand}/link-orders/catalog/items/{id}", { params: { path: { brand, id } } });
    if (data?.email_search && Date.parse(data.email_search.at) >= since) return data;
    await new Promise((r) => setTimeout(r, 4000));
  }
  return null;
}

/** onClose gets true once a run started, so the page reloads and sites now in outreach leave the picks. */
export function PicksOutreach({ brand, donors, onClose }: { brand: string; donors: LinkOrderDonor[]; onClose: (ran: boolean) => void }) {
  const [sequences, setSequences] = useState<Seq[] | null>(null);
  const [seq, setSeq] = useState("");
  const [newName, setNewName] = useState(`Link building ${new Date().toISOString().slice(0, 10)}`);
  const [newSubject, setNewSubject] = useState(NEW_SUBJECT);
  const [newBody, setNewBody] = useState(NEW_BODY);
  const [rows, setRows] = useState<Row[] | null>(null);
  const [err, setErr] = useState("");
  // Closing the dialog stops waiting for searches.
  const stop = useRef(false);

  useEffect(() => {
    let gone = false;
    void api.GET("/v1/brands/{brand}/sequences", { params: { path: { brand } } }).then(({ data, error }) => {
      if (gone) return;
      if (error || !data) return setErr(errorMessage(error));
      const list = data.data.filter((s) => s.status !== "archived" && !(s.links?.length));
      setSequences(list.map((s) => ({ id: s.id, name: s.name, status: s.status })));
      const seqs = list.map((s) => ({ id: s.id, name: s.name, status: s.status }));
      setSeq(pickSequence(seqs, lastSequence(brand)));
    });
    return () => {
      gone = true;
    };
  }, [brand]);

  const run = async () => {
    if (!seq) return setErr("Pick a sequence.");
    setErr("");
    let target = sequences?.find((s) => s.id === seq);
    if (seq === NEW) {
      // Made as a draft: it sends nothing until it is started in Outreach.
      if (!newName.trim() || !newSubject.trim() || !newBody.trim()) return setErr("Give the new sequence a name, a subject and a first email.");
      const { data, error } = await api.POST("/v1/brands/{brand}/sequences", {
        params: { path: { brand }, header: idemp() },
        body: { name: newName.trim(), steps: [{ subject: newSubject, body: newBody, delay_days: 0 }, { subject: "", body: NEW_FOLLOW_UP, delay_days: 4 }] },
      });
      if (error || !data || !("id" in data)) return setErr(error ? errorMessage(error) : "Workspace changes are held right now; try again later.");
      track("ui.link_order_outreach_sequence_created", {});
      target = { id: data.id, name: data.name, status: data.status };
      setSequences([...(sequences ?? []), target]);
      setSeq(target.id);
    }
    if (!target) return setErr("Pick a sequence.");
    const sequenceID = target.id;
    rememberSequence(brand, sequenceID);
    let cur: Row[] = donors.map((d) => ({ donor: d, state: "searching", text: "Looking for an address on the site…", ok: false }));
    const set = (i: number, r: Partial<Row>) => {
      cur = cur.map((x, j) => (j === i ? { ...x, ...r } : x));
      setRows(cur);
    };
    setRows(cur);
    track("ui.link_order_picks_outreach_started", { count: donors.length });
    const since = Date.now() - 2000;
    // The API takes up to FIND_BATCH sites a request: send them in batches.
    for (let at = 0; at < donors.length; at += FIND_BATCH) {
      const { error } = await api.POST("/v1/brands/{brand}/link-orders/catalog/find-emails", {
        params: { path: { brand } },
        body: { ids: donors.slice(at, at + FIND_BATCH).map((d) => d.id) },
      });
      if (error) {
        setErr(errorMessage(error));
        if (at === 0) setRows(null);
        return;
      }
    }
    let sent = 0;
    // A few sites at a time, so 100 or 200 donors don't poll all at once.
    await inPool(donors, WORKERS, async (d, i) => {
      // A row already searched today is not searched again: use that.
      const fresh = d.email_search && Date.now() - Date.parse(d.email_search.at) < 86_400_000 ? d : await waitForSearch(brand, d.id, since, () => stop.current);
      let email = fresh?.email_search?.emails[0] ?? "";
      if (!email) {
        // Fall back to an address already stored for the site.
        const { data } = await api.GET("/v1/brands/{brand}/link-orders/catalog/items/{id}/contacts", { params: { path: { brand, id: d.id } } });
        email = data?.contacts.find((c) => !c.suppressed && c.at_domain)?.email ?? "";
      }
      if (!email) {
        const why = !fresh ? "the search did not finish in time; try again later" : fresh.email_search?.status === "blocked" ? "the site's robots.txt blocks OutflareBot" : "the site lists no address to write to";
        return set(i, { state: "done", text: `No address: ${why}.`, ok: false });
      }
      set(i, { state: "sending", text: `Adding ${email}…` });
      const { data, error } = await api.POST("/v1/brands/{brand}/link-orders/catalog/items/{id}/outreach", {
        params: { path: { brand, id: d.id }, header: idemp() },
        body: { sequence_id: sequenceID, email },
      });
      if (error || !data) return set(i, { state: "done", text: `${email}: ${errorMessage(error)}`, ok: false });
      const ok = data.result === "enrolled" || data.result === "already_enrolled";
      if (data.result === "enrolled") sent++;
      const text = {
        enrolled: `Added ${email}.`,
        already_enrolled: `${email} is already in the sequence.`,
        suppressed: `${email} is on the suppression list: not added.`,
        excluded: `${email} is left out by the sequence's exclusions.`,
        active_elsewhere: `${email} is active in another sequence: not added.`,
      }[data.result];
      set(i, { state: "done", text, ok });
    });
    toast(`${sent} of ${donors.length} added to ${target.name}${target.status !== "active" ? ` (${target.status}: connect a mailbox and start it in Outreach, nothing is sent before)` : ""}.`);
  };

  const busy = !!rows && rows.some((r) => r.state !== "done");
  const chosen = sequences?.find((s) => s.id === seq);
  return (
    <Dialog open onOpenChange={(o) => { if (!o) { stop.current = true; onClose(!!rows); } }}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Find emails and send {donors.length} donor{donors.length === 1 ? "" : "s"} to a sequence</DialogTitle>
          <DialogDescription>
            OutflareBot reads each site's home page and its contact, advertising, imprint and about pages and keeps the addresses it publishes, the advertising desk first; data-protection, legal, abuse, jobs and no-reply mailboxes are never used. The best address of each site joins the sequence with Outreach's usual rules, and the sequence sends from its own mailboxes on its own schedule.
          </DialogDescription>
        </DialogHeader>
        {!rows ? (
          <div className="flex flex-col gap-3">
            <div className="flex flex-col gap-2 rounded-md border-2 border-primary/60 bg-primary/5 p-3">
              <label htmlFor="picks-sequence" className="text-[13px] font-semibold">1. Choose the sequence to send from</label>
              <p className="text-[12px] text-muted-foreground">The emails and follow-ups these donors get are this sequence's steps. Its mailboxes and schedule send them.</p>
              <select id="picks-sequence" className={`${field} h-10 text-[14px] font-medium`} value={seq} onChange={(e) => setSeq(e.target.value)} disabled={!sequences?.length}>
                {!seq ? <option value="">Pick a sequence…</option> : null}
                {sequences?.map((s) => <option key={s.id} value={s.id}>{s.name}{s.status === "active" ? " · sending" : ` · ${s.status}`}</option>)}
                <option value={NEW}>+ New link-building sequence (made now)</option>
              </select>
              {chosen && chosen.status !== "active" ? <p className="text-[12px] text-amber-700 dark:text-amber-400">This sequence is {chosen.status}: nothing is sent until you start it in Outreach.</p> : null}
            </div>
            <div className="text-[13px] font-semibold">2. Donors to find emails for ({donors.length})</div>
            {seq === NEW ? (
              <div className="flex flex-col gap-2 rounded-md border p-3">
                <p className="text-[12px] text-muted-foreground">Made now as a draft with this email and one follow-up four days later (a reply in the same thread). It sends nothing until you connect a mailbox and start it in Outreach. Variables such as {"{{first_name|there}}"} and {"{{sender_name}}"} are filled per address.</p>
                <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Name<input className={field} value={newName} onChange={(e) => setNewName(e.target.value)} /></label>
                <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Subject<input className={field} value={newSubject} onChange={(e) => setNewSubject(e.target.value)} /></label>
                <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">First email<textarea className={`${field} min-h-40`} value={newBody} onChange={(e) => setNewBody(e.target.value)} /></label>
              </div>
            ) : null}
            <ul className="max-h-48 overflow-y-auto text-[12px] text-muted-foreground">{donors.map((d) => <li key={d.id}>{d.domain}</li>)}</ul>
            {err ? <ErrorText>{err}</ErrorText> : null}
            <div className="flex gap-2">
              <Button disabled={!seq} onClick={() => void run()}>{seq === NEW ? "Create the sequence, find emails and add" : "Find emails and add"}</Button>
              <Button variant="ghost" onClick={() => onClose(false)}>Cancel</Button>
            </div>
          </div>
        ) : (
          <div className="flex flex-col gap-3" role="status">
            <ul className="flex flex-col divide-y text-[13px]">
              {rows.map((r) => (
                <li key={r.donor.id} className="flex flex-wrap justify-between gap-2 py-1.5">
                  <span className="font-medium">{r.donor.domain}</span>
                  <span className={r.state === "done" ? (r.ok ? "text-emerald-700 dark:text-emerald-400" : "text-muted-foreground") : "text-muted-foreground"}>{r.text}</span>
                </li>
              ))}
            </ul>
            {err ? <ErrorText>{err}</ErrorText> : null}
            <div className="flex gap-2">
              <Button disabled={busy} onClick={() => { stop.current = true; onClose(true); }}>{busy ? "Working…" : "Done"}</Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
