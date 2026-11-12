// UploadPeople makes a list from a CSV of people: your own users, customers
// or subscribers, ready for a broadcast. It says what the file needs, shows
// what it read before anything is saved, and then says what it did. Each
// row is matched to someone already here by user id, then email; only a new
// email makes a new person.

import { useRef, useState, type DragEvent } from "react";
import type { SmartList } from "@outflare/sdk";
import { api, errorMessage } from "@/api";
import { plural } from "@/format";
import { parseCSV } from "@/pages/brand-coverage/client";
import { Button, ErrorText, Input } from "@/ui";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

type Origin = "first_party" | "prospect" | "import";
type Person = { email?: string; user_id?: string; name?: string };
type Read = { file: string; people: Person[]; rows: number; columns: { email?: string; user_id?: string; name?: string } };

const example = "email,user_id,name\nana@acme.example,u_1001,Ana Silva\nben@northpeak.example,u_1002,Ben Okafor\n";
const most = 10000;

const origins: { id: Origin; title: string; body: string }[] = [
  { id: "first_party", title: "My own users or customers", body: "They signed up with you or bought from you. They can get service messages, and marketing email unless they opted out." },
  { id: "prospect", title: "People from a list I bought or found", body: "They never signed up with you. Marketing email skips them until they agree to it." },
  { id: "import", title: "I'm not sure", body: "Treated carefully, like a found list, until you say otherwise." },
];

// read finds the email, user id and name columns by their headers, whatever
// their case or spacing, and keeps the rows that have an email or a user id.
function read(name: string, text: string): Read {
  const rows = parseCSV(text);
  const key = (re: RegExp) => Object.keys(rows[0] ?? {}).find((h) => re.test(h.trim().toLowerCase().replace(/[\s-]+/g, "_")));
  const columns = { email: key(/^(e_?mail|email_address)$/), user_id: key(/^(user_?id|userid|account_id|customer_id|id)$/), name: key(/^(full_)?name$/) };
  if (!columns.email && !columns.user_id) throw new Error("This file has no column named email or user_id. Name the first row's columns, as in the example.");
  const cell = (r: Record<string, string>, k?: string) => (k ? r[k]?.trim() || undefined : undefined);
  const people = rows.map((r) => ({ email: cell(r, columns.email), user_id: cell(r, columns.user_id), name: cell(r, columns.name) })).filter((p) => p.email || p.user_id);
  if (people.length === 0) throw new Error("No row in this file has an email or a user id.");
  if (people.length > most) throw new Error(`This file has ${people.length.toLocaleString()} people. Upload up to ${most.toLocaleString()} at a time.`);
  return { file: name, people, rows: rows.length, columns };
}

export function UploadPeople({ brand, onClose, onDone }: { brand: string; onClose: () => void; onDone: (list: SmartList) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [got, setGot] = useState<Read | null>(null);
  const [name, setName] = useState("");
  const [origin, setOrigin] = useState<Origin | "">("");
  const [over, setOver] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState<{ list: SmartList; text: string; skipped: string[] } | null>(null);

  async function take(file?: File | null) {
    if (!file) return;
    setError("");
    try {
      const r = read(file.name, await file.text());
      setGot(r);
      setName(file.name.replace(/\.csv$/i, "").replace(/[-_]+/g, " ").trim().slice(0, 200) || "Uploaded people");
    } catch (e) {
      setGot(null);
      setError(e instanceof Error ? e.message : "This file can't be read as a CSV.");
    }
  }
  function drop(e: DragEvent) {
    e.preventDefault();
    setOver(false);
    take(e.dataTransfer.files?.[0]);
  }
  async function save() {
    if (!got || !origin) return;
    setBusy(true);
    setError("");
    const made = await api.POST("/v1/brands/{brand}/smart-lists", { params: { path: { brand } }, body: { name: name.trim(), kind: "static" } });
    if (made.error || !made.data) {
      setBusy(false);
      return setError(errorMessage(made.error));
    }
    const res = await api.POST("/v1/brands/{brand}/records/import", { params: { path: { brand } }, body: { people: got.people, origin, list_id: made.data.id } });
    setBusy(false);
    if (res.error || !res.data) return setError(errorMessage(res.error));
    const r = res.data;
    const members = r.list_members ?? r.record_ids.length;
    setDone({
      list: { ...made.data, members },
      text: `${plural(members, "person is", "people are")} in ${made.data.name}: ${r.matched.toLocaleString()} already here, ${r.created.toLocaleString()} new.`,
      skipped: [...new Set(r.skipped.map((x) => x.reason))].map((why) => `${plural(r.skipped.filter((x) => x.reason === why).length, "row")} skipped: ${why}`),
    });
  }

  const cols = got && ([["Email", got.columns.email], ["User id", got.columns.user_id], ["Name", got.columns.name]] as const);
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{done ? "Your list is ready" : "Upload a CSV of people"}</DialogTitle>
          <DialogDescription>{done ? "You'll find it under Lists, and can send a broadcast to it." : "Makes a list from a spreadsheet of people, such as an export of your users. Nothing is emailed."}</DialogDescription>
        </DialogHeader>
        {done ? (
          <div className="space-y-4 text-[13px]">
            <p>{done.text}</p>
            {done.skipped.length > 0 && <ul className="list-disc space-y-1 pl-5 text-muted-foreground">{done.skipped.map((s) => <li key={s}>{s}</li>)}</ul>}
            <div className="flex justify-end"><Button onClick={() => onDone(done.list)}>Done</Button></div>
          </div>
        ) : (
          <div className="space-y-5 text-[13px]">
            <section className="space-y-2">
              <h3 className="font-medium">1. Check your file</h3>
              <p className="text-muted-foreground">Save your spreadsheet as a CSV. The first row names the columns. It needs an <b className="font-medium text-foreground">email</b> column, a <b className="font-medium text-foreground">user_id</b> column (your own id for each person), or both. A <b className="font-medium text-foreground">name</b> column is optional, and other columns are ignored.</p>
              <div className="overflow-x-auto rounded-md border">
                <table className="w-full text-left font-mono text-[12px]">
                  <thead className="bg-muted/50"><tr><th className="px-3 py-1.5 font-medium">email</th><th className="px-3 py-1.5 font-medium">user_id</th><th className="px-3 py-1.5 font-medium">name</th></tr></thead>
                  <tbody className="text-muted-foreground">
                    <tr className="border-t"><td className="px-3 py-1.5">ana@acme.example</td><td className="px-3 py-1.5">u_1001</td><td className="px-3 py-1.5">Ana Silva</td></tr>
                    <tr className="border-t"><td className="px-3 py-1.5">ben@northpeak.example</td><td className="px-3 py-1.5">u_1002</td><td className="px-3 py-1.5">Ben Okafor</td></tr>
                  </tbody>
                </table>
              </div>
              <a className="inline-block text-muted-foreground underline hover:text-foreground" download="people-example.csv" href={`data:text/csv;charset=utf-8,${encodeURIComponent(example)}`}>Download this example</a>
            </section>

            <section className="space-y-2">
              <h3 className="font-medium">2. Choose the file</h3>
              <input ref={input} type="file" accept=".csv,text/csv" className="sr-only" aria-label="CSV file" onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ""; take(f); }} />
              <button
                type="button"
                onClick={() => input.current?.click()}
                onDragOver={(e) => { e.preventDefault(); setOver(true); }}
                onDragLeave={() => setOver(false)}
                onDrop={drop}
                className={`flex w-full flex-col items-center gap-1 rounded-md border border-dashed px-4 py-6 text-center hover:bg-muted/50 ${over ? "border-primary bg-muted/50" : ""}`}
              >
                <span className="font-medium">{got ? got.file : "Drop a CSV here, or click to choose one"}</span>
                <span className="text-[12px] text-muted-foreground">{got ? `${plural(got.people.length, "person", "people")} found. Click to choose another file.` : `Up to ${most.toLocaleString()} people at a time.`}</span>
              </button>
              {got && cols && (
                <div className="space-y-2">
                  <ul className="flex flex-wrap gap-x-4 gap-y-1 text-[12px]">
                    {cols.map(([label, col]) => (
                      <li key={label} className={col ? "" : "text-muted-foreground"}>{label}: {col ? <span className="font-mono">{col}</span> : "not in this file"}</li>
                    ))}
                  </ul>
                  {got.rows > got.people.length && <p className="text-[12px] text-muted-foreground">{plural(got.rows - got.people.length, "row")} with no email or user id will be left out.</p>}
                  <div className="overflow-x-auto rounded-md border">
                    <table className="w-full text-left text-[12px]">
                      <thead className="bg-muted/50"><tr><th className="px-3 py-1.5 font-medium">Email</th><th className="px-3 py-1.5 font-medium">User id</th><th className="px-3 py-1.5 font-medium">Name</th></tr></thead>
                      <tbody>
                        {got.people.slice(0, 3).map((p, i) => (
                          <tr key={i} className="border-t"><td className="px-3 py-1.5">{p.email ?? "–"}</td><td className="px-3 py-1.5">{p.user_id ?? "–"}</td><td className="px-3 py-1.5">{p.name ?? "–"}</td></tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  {got.people.length > 3 && <p className="text-[12px] text-muted-foreground">And {plural(got.people.length - 3, "more person", "more people")}.</p>}
                </div>
              )}
            </section>

            {got && (
              <>
                <section className="space-y-2">
                  <h3 className="font-medium">3. Who are these people?</h3>
                  <p className="text-muted-foreground">This decides what they may be sent, so pick the one that is true.</p>
                  <div className="grid gap-2">
                    {origins.map((o) => (
                      <label key={o.id} className={`flex cursor-pointer items-start gap-2 rounded-md border p-3 ${origin === o.id ? "border-primary bg-muted/50" : ""}`}>
                        <input type="radio" name="origin" className="mt-0.5" checked={origin === o.id} onChange={() => setOrigin(o.id)} />
                        <span>
                          <span className="block font-medium">{o.title}</span>
                          <span className="block text-[12px] text-muted-foreground">{o.body}</span>
                        </span>
                      </label>
                    ))}
                  </div>
                </section>
                <section className="space-y-2">
                  <h3 className="font-medium">4. Name the list</h3>
                  <Input label="List name" value={name} onChange={(e) => setName(e.target.value)} maxLength={200} hint="People already in Outflare are matched by user id, then email, so nobody is added twice." />
                </section>
              </>
            )}
            <ErrorText>{error}</ErrorText>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button>
              <Button onClick={save} disabled={busy || !got || !origin || !name.trim()}>{busy ? "Uploading…" : got ? `Make a list of ${plural(got.people.length, "person", "people")}` : "Make the list"}</Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
