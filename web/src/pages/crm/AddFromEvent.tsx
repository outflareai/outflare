// AddFromEvent is a full page for the people the sales team met at an event
// or conference: pick the event once (or type a new one), then type the
// people in, paste them from a spreadsheet, or drop a CSV. Everyone gets
// Source Events and conferences and that event as their Sub-source. Each
// row is matched to someone already here by email; only a new email makes a
// new person, and people already here keep what they have.

import { useRef, useState, type ClipboardEvent, type DragEvent } from "react";
import { Plus, Trash2, Upload } from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, ErrorText, field } from "@/ui";
import { track } from "@/lib/analytics";
import { plural } from "@/format";
import { parseCSV } from "@/pages/brand-coverage/client";
import { SubSourcePicker, useSubSources } from "@/components/sub-source-picker";

type Row = { first_name: string; last_name: string; email: string; company: string; job_title: string };
const cols: { key: keyof Row; label: string; type?: string; placeholder: string }[] = [
  { key: "first_name", label: "First name", placeholder: "Ana" },
  { key: "last_name", label: "Last name", placeholder: "Silva" },
  { key: "email", label: "Email", type: "email", placeholder: "ana@acme.example" },
  { key: "company", label: "Company", placeholder: "Acme" },
  { key: "job_title", label: "Job title", placeholder: "Head of data" },
];
const blank = (): Row => ({ first_name: "", last_name: "", email: "", company: "", job_title: "" });
const filled = (r: Row) => Object.values(r).some((v) => v.trim());
const most = 10000;

// fromCSV reads a file's columns by their headers, whatever their case or
// spacing: first and last name (or one name column), email, company and
// job title.
function fromCSV(text: string): Row[] {
  const rows = parseCSV(text);
  const heads = Object.keys(rows[0] ?? {});
  const norm = (h: string) => h.trim().toLowerCase().replace(/[\s-]+/g, "_");
  const find = (re: RegExp) => heads.find((h) => re.test(norm(h)));
  const c = {
    first: find(/^(first_?name|given_name|first)$/),
    last: find(/^(last_?name|surname|family_name|last)$/),
    name: find(/^(full_?name|name|contact|contact_name)$/),
    email: find(/^(e_?mail|email_address|work_email)$/),
    company: find(/^(company|company_name|organi[sz]ation|organi[sz]ation_name|account)$/),
    title: find(/^(job_?title|title|position|role)$/),
  };
  if (!c.email) throw new Error("This file has no column named email. Name the first row's columns, such as first_name, last_name, email, company and job_title.");
  const cell = (r: Record<string, string>, k?: string) => (k ? (r[k] ?? "").trim() : "");
  return rows
    .map((r) => {
      let first = cell(r, c.first), last = cell(r, c.last);
      if (!first && !last && c.name) [first, last] = splitName(cell(r, c.name));
      return { first_name: first, last_name: last, email: cell(r, c.email), company: cell(r, c.company), job_title: cell(r, c.title) };
    })
    .filter(filled);
}

function splitName(n: string): [string, string] {
  const parts = n.trim().split(/\s+/);
  return [parts[0] ?? "", parts.slice(1).join(" ")];
}

const emailOK = (e: string) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(e.trim());

export function AddFromEvent({ brand, event: first, onDone, onBack }: { brand: string; event?: string; onDone: (event: string) => void; onBack: () => void }) {
  const [event, setEvent] = useState<string | null>(first ?? null);
  const events = useSubSources(brand, "event");
  const eventName = events.list?.find((s) => s.key === event)?.label ?? "";
  const [rows, setRows] = useState<Row[]>(() => Array.from({ length: 5 }, blank));
  const [file, setFile] = useState("");
  // Meeting someone isn't agreeing to marketing email: they get none until
  // they opt in, unless whoever adds them says they agreed.
  const [agreed, setAgreed] = useState(false);
  const [over, setOver] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState<{ created: number; matched: number; skipped: { row: number; reason: string }[] } | null>(null);
  const input = useRef<HTMLInputElement>(null);

  const people = rows.filter(filled);
  const noEmail = people.filter((r) => !emailOK(r.email)).length;
  const ready = people.length - noEmail;

  const set = (i: number, k: keyof Row, v: string) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, [k]: v } : r)));

  // Pasting cells from a spreadsheet fills the rows from where it lands,
  // in the columns' order.
  function paste(e: ClipboardEvent<HTMLInputElement>, i: number, k: keyof Row) {
    const text = e.clipboardData.getData("text");
    if (!/[\t\n]/.test(text.trim())) return;
    e.preventDefault();
    const start = cols.findIndex((c) => c.key === k);
    const lines = text.replace(/\r/g, "").split("\n").filter((l) => l.trim());
    setRows((rs) => {
      const next = [...rs];
      lines.forEach((line, n) => {
        const r = { ...(next[i + n] ?? blank()) };
        line.split("\t").forEach((v, m) => {
          const c = cols[start + m];
          if (c) r[c.key] = v.trim();
        });
        next[i + n] = r;
      });
      return next;
    });
    track("ui.event_people_pasted", { rows: lines.length });
  }

  async function take(f?: File | null) {
    if (!f) return;
    setError("");
    try {
      const got = fromCSV(await f.text());
      if (got.length === 0) throw new Error("No row in this file has anyone in it.");
      if (got.length > most) throw new Error(`This file has ${got.length.toLocaleString()} people. Upload up to ${most.toLocaleString()} at a time.`);
      setRows(got);
      setFile(f.name);
      track("ui.event_people_file_read", { rows: got.length });
    } catch (e) {
      setError(e instanceof Error ? e.message : "This file can't be read as a CSV.");
    }
  }
  function drop(e: DragEvent) {
    e.preventDefault();
    setOver(false);
    void take(e.dataTransfer.files?.[0]);
  }

  async function save() {
    if (!event || ready === 0) return;
    setBusy(true);
    setError("");
    const body = people.filter((r) => emailOK(r.email)).map((r) => ({ ...r, email: r.email.trim() }));
    const { data, error } = await api.POST("/v1/brands/{brand}/records/import", {
      params: { path: { brand } },
      body: { people: body, source: "event", sub_source: event, acquired_via: "event", origin: agreed ? "first_party" : "prospect" },
    });
    setBusy(false);
    if (!data) return setError(errorMessage(error));
    void events.reload();
    setDone({ created: data.created, matched: data.matched, skipped: data.skipped });
  }

  if (done)
    return (
      <div className="mx-auto max-w-2xl space-y-4 py-6">
        <h1 className="text-2xl font-semibold tracking-tight">Added people from {eventName || "the event"}</h1>
        <ul className="space-y-1 text-sm">
          <li>{plural(done.created, "new person", "new people")} added, with Source Events and conferences and {eventName || "the event"}.</li>
          {done.matched > 0 && <li>{plural(done.matched, "person was", "people were")} already here. They keep their Source, and get this event if they came from events and had none yet.</li>}
          {done.skipped.length > 0 && (
            <li>
              {plural(done.skipped.length, "row", "rows")} left out:
              <ul className="mt-1 list-disc pl-5 text-muted-foreground">
                {done.skipped.slice(0, 20).map((s) => (
                  <li key={s.row}>Row {s.row}: {s.reason}</li>
                ))}
              </ul>
            </li>
          )}
        </ul>
        <div className="flex flex-wrap gap-2">
          <Button onClick={() => onDone(event!)}>See them on People</Button>
          <Button
            variant="ghost"
            onClick={() => {
              setDone(null);
              setRows(Array.from({ length: 5 }, blank));
              setFile("");
            }}
          >
            Add more from this event
          </Button>
        </div>
      </div>
    );

  return (
    <div className="space-y-6">
      <header className="space-y-1">
        <button type="button" className="text-xs text-muted-foreground hover:text-foreground" onClick={onBack}>
          ← People
        </button>
        <h1 className="text-2xl font-semibold tracking-tight">Add people from an event</h1>
        <p className="text-sm text-muted-foreground">
          Pick the event or conference once, then add everyone you met there. They get Source Events and conferences and the event, so you can find and count them later.
        </p>
      </header>

      <section className="max-w-md space-y-1.5">
        <label htmlFor="event-pick" className="block text-sm font-medium">
          Event or conference
        </label>
        <SubSourcePicker
          id="event-pick"
          brand={brand}
          source="event"
          value={event}
          onChange={(k) => {
            setEvent(k);
            void events.reload();
          }}
        />
        <p className="text-xs text-muted-foreground">Type a name that isn't in the list to add it, such as “Web Summit 2026”.</p>
      </section>

      <section className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-semibold">People</h2>
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            {file ? <span>From {file}</span> : <span>Type them in, paste rows from a spreadsheet, or upload a CSV.</span>}
            <Button variant="ghost" onClick={() => input.current?.click()}>
              <Upload className="size-3.5" />
              Upload a CSV
            </Button>
            <input ref={input} type="file" accept=".csv,text/csv" className="hidden" onChange={(e) => void take(e.target.files?.[0])} />
          </div>
        </div>
        <div
          className={`overflow-x-auto rounded-lg border ${over ? "border-primary bg-muted/40" : ""}`}
          onDragOver={(e) => {
            e.preventDefault();
            setOver(true);
          }}
          onDragLeave={() => setOver(false)}
          onDrop={drop}
        >
          <table className="w-full min-w-[44rem] text-sm">
            <thead className="border-b bg-muted/30 text-left text-xs text-muted-foreground">
              <tr>
                {cols.map((c) => (
                  <th key={c.key} className="px-2 py-2 font-medium">
                    {c.label}
                    {c.key === "email" && <span className="text-destructive"> *</span>}
                  </th>
                ))}
                <th className="w-8" />
              </tr>
            </thead>
            <tbody>
              {rows.slice(0, 500).map((r, i) => {
                const bad = filled(r) && !emailOK(r.email);
                return (
                  <tr key={i} className="border-b last:border-0">
                    {cols.map((c) => (
                      <td key={c.key} className="px-1 py-1">
                        <input
                          aria-label={`${c.label}, row ${i + 1}`}
                          type={c.type ?? "text"}
                          className={`${field} w-full ${c.key === "email" && bad ? "border-destructive" : ""}`}
                          placeholder={i === 0 ? c.placeholder : ""}
                          value={r[c.key]}
                          onChange={(e) => set(i, c.key, e.target.value)}
                          onPaste={(e) => paste(e, i, c.key)}
                        />
                      </td>
                    ))}
                    <td className="px-1">
                      <button type="button" aria-label={`Remove row ${i + 1}`} className="rounded p-1.5 text-muted-foreground hover:text-foreground" onClick={() => setRows((rs) => (rs.length > 1 ? rs.filter((_, j) => j !== i) : [blank()]))}>
                        <Trash2 className="size-3.5" />
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {rows.length > 500 && <p className="px-3 py-2 text-xs text-muted-foreground">Showing the first 500 of {rows.length.toLocaleString()} rows. All of them are added.</p>}
        </div>
        <Button variant="ghost" onClick={() => setRows((rs) => [...rs, blank(), blank(), blank()])}>
          <Plus className="size-3.5" />
          More rows
        </Button>
      </section>

      <label className="flex max-w-2xl items-start gap-2 text-sm">
        <input type="checkbox" className="mt-1" checked={agreed} onChange={(e) => setAgreed(e.target.checked)} />
        <span>
          <span className="block font-medium">They agreed to get our marketing email</span>
          <span className="block text-xs text-muted-foreground">Tick this only if they said yes, for example on a sign-up sheet or a badge scan that asked. Otherwise new people get no marketing email until they opt in; you can still email them one to one.</span>
        </span>
      </label>

      <footer className="flex flex-wrap items-center gap-3 border-t pt-4">
        <Button disabled={!event || ready === 0 || busy} onClick={() => void save()}>
          {busy ? "Adding…" : ready ? `Add ${plural(ready, "person", "people")}` : "Add people"}
        </Button>
        <span className="text-xs text-muted-foreground">
          {!event ? "Pick the event first." : noEmail > 0 ? `${plural(noEmail, "row needs", "rows need")} an email before it can be added.` : "Someone already here is matched by email, not added twice."}
        </span>
        <ErrorText>{error}</ErrorText>
      </footer>
    </div>
  );
}
