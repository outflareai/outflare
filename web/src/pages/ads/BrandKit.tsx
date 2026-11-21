// Brand kit: the files a brand's creatives are built from (logos, fonts,
// product pictures, example ads, voices and people), and its colours. The
// list opens one file's page, where it is renamed, tagged, archived or
// deleted, and where someone confirms that a person shown or heard agreed.
// Only files marked "Ready to use" go into anything new.

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { Brand, BrandAsset, BrandAssetKind } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { track } from "../../lib/analytics";
import { ago } from "../../format";
import { Badge, Button, Empty, ErrorText, Loading, Status, confirmAction, field, toast } from "../../ui";

const kinds: { id: BrandAssetKind; one: string; many: string; takes: string }[] = [
  { id: "logo", one: "Logo", many: "Logos", takes: "image" },
  { id: "font", one: "Font", many: "Fonts", takes: "font" },
  { id: "product_shot", one: "Product shot", many: "Product shots", takes: "image video" },
  { id: "screenshot", one: "Screenshot", many: "Screenshots", takes: "image" },
  { id: "example_ad", one: "Example ad", many: "Example ads", takes: "image video" },
  { id: "person", one: "Person", many: "People", takes: "image video" },
  { id: "voice", one: "Voice", many: "Voices", takes: "audio" },
  { id: "recording", one: "Recording", many: "Recordings", takes: "video" },
  { id: "other", one: "Other", many: "Other", takes: "image video audio font" },
];
const kindName = (k: string) => kinds.find((x) => x.id === k)?.one ?? k;
const accept = ".png,.jpg,.jpeg,.gif,.webp,.svg,.mp4,.webm,.mov,.mp3,.wav,.m4a,.ogg,.ttf,.otf,.woff,.woff2";
const maxBytes = 50 * 1024 * 1024;
const viewKey = "outflare.brandkit.view";
type View = "list" | "gallery";
const readView = (): View => { try { return localStorage.getItem(viewKey) === "gallery" ? "gallery" : "list"; } catch { return "list"; } };

const size = (n: number) => (n < 1024 ? `${n} B` : n < 1024 * 1024 ? `${Math.round(n / 1024)} KB` : `${(n / 1024 / 1024).toFixed(1)} MB`);
const typeName = (a: BrandAsset) => (a.content_type.split("/")[1] ?? "").replace("svg+xml", "svg").replace("quicktime", "mov").replace("mpeg", "mp3").replace("wave", "wav").toUpperCase();
const typeLine = (a: BrandAsset) => (a.width && a.height ? `${typeName(a)} · ${a.width} × ${a.height}` : typeName(a));
const fileUrl = (a: BrandAsset, width?: number) => `/v1/brands/${a.brand_id}/assets/${a.id}/file${width && a.resizable ? `?width=${width}` : ""}`;

// The family a browser's file type belongs to, to guess what a file is for.
function familyOf(f: File): string {
  const ext = f.name.toLowerCase().split(".").pop() ?? "";
  if (["ttf", "otf", "woff", "woff2"].includes(ext)) return "font";
  if (f.type.startsWith("video/") || ["mp4", "webm", "mov"].includes(ext)) return "video";
  if (f.type.startsWith("audio/") || ["mp3", "wav", "m4a", "ogg"].includes(ext)) return "audio";
  return "image";
}
function guessKind(f: File, picked: BrandAssetKind | ""): BrandAssetKind {
  const fam = familyOf(f);
  if (picked && kinds.find((k) => k.id === picked)?.takes.includes(fam)) return picked;
  if (fam === "font") return "font";
  if (fam === "audio") return "voice";
  if (fam === "video") return "recording";
  return /logo/i.test(f.name) ? "logo" : /screen/i.test(f.name) ? "screenshot" : "product_shot";
}
function base64(f: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result).split(",")[1] ?? "");
    r.onerror = () => reject(r.error);
    r.readAsDataURL(f);
  });
}

// A file's picture: the picture itself, or a sign for what can't be drawn.
function Thumb({ a, px, width = 240 }: { a: BrandAsset; px: number; width?: number }) {
  const [broken, setBroken] = useState(false);
  const box = "flex shrink-0 items-center justify-center overflow-hidden rounded-md border bg-muted/50 text-muted-foreground";
  if (a.family === "image" && !broken) {
    return <span className={box} style={{ width: px, height: px }}><img src={fileUrl(a, width)} alt="" loading="lazy" className="max-h-full max-w-full object-contain" onError={() => setBroken(true)} /></span>;
  }
  const sign = a.family === "font" ? "Aa" : a.family === "audio" ? "♪" : a.family === "video" ? "▶" : "?";
  return <span className={`${box} font-semibold`} style={{ width: px, height: px, fontSize: Math.max(14, px / 3) }} aria-hidden>{sign}</span>;
}

const Consent = ({ a }: { a: BrandAsset }) => !a.consent.needed ? <span className="text-muted-foreground">Not needed</span> : a.consent.confirmed ? <Status tone="green">Consent on file</Status> : <Status tone="amber">Needs consent</Status>;
const Usable = ({ a }: { a: BrandAsset }) => a.archived_at ? <Status tone="grey">Archived</Status> : a.usable ? <Status tone="green">Ready to use</Status> : <Status tone="red">Can't be used yet</Status>;

type Props = { brand: Brand; ws: string; canManage: boolean; item: string; open: (id: string) => void; back: () => void; brandChanged?: () => void };

export function BrandKit({ brand, ws, canManage, item, open, back, brandChanged }: Props) {
  const [rows, setRows] = useState<BrandAsset[] | null>(null);
  const [storage, setStorage] = useState(true);
  const [error, setError] = useState("");
  const [archived, setArchived] = useState(false);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/assets", { params: { path: { brand: brand.id }, query: archived ? { archived: true } : {} } });
    setError(error ? errorMessage(error) : "");
    setRows(data?.data ?? []);
    setStorage(data?.storage ?? true);
  }, [brand.id, archived]);
  useEffect(() => { setRows(null); void load(); }, [load]);

  if (item) {
    if (!rows) return <Loading />;
    const a = rows.find((r) => r.id === item);
    if (!a) return <Empty title="This file is gone" action={<Button onClick={back}>Back to Brand kit</Button>}>It may have been deleted or archived.</Empty>;
    return <FilePage key={a.id} a={a} brandName={brand.name} canManage={canManage} back={back} reload={load} />;
  }
  return <List brand={brand} ws={ws} rows={rows} storage={storage} error={error} canManage={canManage} archived={archived} setArchived={setArchived} open={open} reload={load} brandChanged={brandChanged} />;
}

function List({ brand, ws, rows, storage, error, canManage, archived, setArchived, open, reload, brandChanged }: { brand: Brand; ws: string; rows: BrandAsset[] | null; storage: boolean; error: string; canManage: boolean; archived: boolean; setArchived: (v: boolean) => void; open: (id: string) => void; reload: () => Promise<void>; brandChanged?: () => void }) {
  const [kind, setKind] = useState<BrandAssetKind | "" | "waiting">("");
  const [q, setQ] = useState("");
  const [view, setViewState] = useState<View>(readView);
  const [chosen, setChosen] = useState<{ file: File; kind: BrandAssetKind }[]>([]);
  const input = useRef<HTMLInputElement>(null);
  const setView = (v: View) => { setViewState(v); try { localStorage.setItem(viewKey, v); } catch { /* the choice just isn't kept */ } track("ui.brand_kit_view_changed", { view: v }); };
  const waiting = useMemo(() => (rows ?? []).filter((a) => a.consent.needed && !a.consent.confirmed && !a.archived_at), [rows]);
  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (rows ?? []).filter((a) => (kind === "" || (kind === "waiting" ? waiting.includes(a) : a.kind === kind)) && (!needle || a.name.toLowerCase().includes(needle) || a.tags.some((t) => t.includes(needle))));
  }, [rows, kind, q, waiting]);
  const take = (files: FileList | null) => {
    const list = [...(files ?? [])];
    const big = list.filter((f) => f.size > maxBytes);
    if (big.length) toast(`${big.map((f) => f.name).join(", ")} ${big.length === 1 ? "is" : "are"} over 50 MB and can't be added.`, "error");
    setChosen(list.filter((f) => f.size <= maxBytes && f.size > 0).map((file) => ({ file, kind: guessKind(file, kind === "waiting" ? "" : kind) })));
  };
  const add = canManage && storage ? <Button onClick={() => input.current?.click()}>+ Add files</Button> : undefined;

  return (
    <div className="space-y-5">
      <input ref={input} type="file" multiple accept={accept} className="sr-only" aria-label="Files to add" onChange={(e) => { take(e.target.files); e.target.value = ""; }} />
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="text-lg font-medium">{brand.name}'s brand kit</h2>
          <p className="max-w-3xl text-[13px] text-muted-foreground">The files every creative is built from: logos, fonts, product pictures, good example ads, and the voices and people you allow in ads. Agents use only files marked "Ready to use".</p>
        </div>
        <span className="flex items-center gap-2">
          <span role="group" aria-label="View" className="inline-flex overflow-hidden rounded-md border text-[13px]">
            {(["list", "gallery"] as const).map((v) => <button key={v} type="button" aria-pressed={view === v} onClick={() => setView(v)} className={`px-3 py-1.5 ${view === v ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground"}`}>{v === "list" ? "☰ List" : "▦ Gallery"}</button>)}
          </span>
          {add}
        </span>
      </div>

      {error && <ErrorText>{error}</ErrorText>}
      {!storage && <div className="rounded-xl border border-amber-500/50 bg-amber-500/5 p-4 text-[13px]"><div className="text-[15px] font-medium">Files can't be added on this server yet</div>This server has no file storage. Whoever runs it sets <code>OUTFLARE_S3_*</code>, and files can be added from then on.</div>}

      {chosen.length > 0 && <Adding brand={brand.id} chosen={chosen} setChosen={setChosen} done={reload} />}

      {waiting.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-amber-500/50 bg-amber-500/5 p-4">
          <div><div className="text-[15px] font-medium">{waiting.length === 1 ? "1 file can't be used yet" : `${waiting.length} files can't be used yet`}</div><div className="text-[13px] text-muted-foreground">{waiting.length === 1 ? "It shows or sounds" : "They show or sound"} like a real person, and nobody has confirmed that person agreed. Open {waiting.length === 1 ? "it" : "each one"} to confirm.</div></div>
          {waiting.length === 1 ? <Button variant="ghost" onClick={() => open(waiting[0].id)}>Open it</Button> : kind !== "waiting" && <Button variant="ghost" onClick={() => setKind("waiting")}>Show these {waiting.length}</Button>}
        </div>
      )}

      <Colours brand={brand} ws={ws} canManage={canManage} changed={brandChanged} />

      <div className="flex flex-wrap items-center gap-1.5 text-[13px]">
        {([{ id: "" as const, many: "All" }, ...kinds.filter((k) => (rows ?? []).some((a) => a.kind === k.id) || k.id !== "other")] as { id: BrandAssetKind | ""; many: string }[]).map((k) => {
          const n = k.id === "" ? (rows ?? []).length : (rows ?? []).filter((a) => a.kind === k.id).length;
          return <button key={k.id} type="button" aria-pressed={kind === k.id} onClick={() => setKind(k.id)} className={`rounded-full border px-3 py-1 ${k.id === kind ? "border-foreground bg-foreground text-background" : "text-muted-foreground hover:text-foreground"}`}>{k.many} <span className="font-mono text-[11px] opacity-70">{n}</span></button>;
        })}
        {kind === "waiting" && <button type="button" aria-pressed className="rounded-full border border-foreground bg-foreground px-3 py-1 text-background" onClick={() => setKind("")}>Needs consent <span className="font-mono text-[11px] opacity-70">{waiting.length}</span> ✕</button>}
        <label className="ml-auto flex items-center gap-2 text-muted-foreground"><input type="checkbox" checked={archived} onChange={(e) => setArchived(e.target.checked)} />Show archived</label>
        <input type="search" aria-label="Search files" placeholder="Search by name or tag" value={q} onChange={(e) => setQ(e.target.value)} className={`${field} w-64`} />
      </div>

      {!rows ? <Loading /> : rows.length === 0 ? (
        <Empty title={archived ? "Nothing is archived" : "No files yet"} action={archived ? undefined : add}>{archived ? "Files you archive are kept here." : "Start with your logo, a font and a few product pictures. Outflare builds every creative from these."}</Empty>
      ) : shown.length === 0 ? (
        <Empty title="No file matches" action={<Button variant="ghost" onClick={() => { setKind(""); setQ(""); }}>Show all files</Button>} />
      ) : view === "list" ? (
        <div className="overflow-x-auto rounded-xl border">
          <table className="w-full text-[13px]">
            <thead className="border-b bg-muted/40 text-left text-[12px] text-muted-foreground"><tr>{["File", "What it is", "Type and size", "When to use it", "Consent", "Status", "Added"].map((h) => <th key={h} className="px-3 py-2 font-medium">{h}</th>)}</tr></thead>
            <tbody className="divide-y">
              {shown.map((a) => (
                <tr key={a.id} className="cursor-pointer hover:bg-muted/30" onClick={() => open(a.id)}>
                  <td className="px-3 py-2.5"><button type="button" onClick={(e) => { e.stopPropagation(); open(a.id); }} aria-label={a.name} className="flex items-center gap-3 text-left"><Thumb a={a} px={44} /><span><span className="block font-medium">{a.name}</span><span className="flex flex-wrap gap-1 pt-0.5">{a.tags.map((t) => <Badge key={t} tone="grey">{t}</Badge>)}</span></span></button></td>
                  <td className="px-3 py-2.5">{kindName(a.kind)}</td>
                  <td className="px-3 py-2.5 align-middle text-muted-foreground">{typeLine(a)}<span className="block text-[12px]">{size(a.bytes)}</span></td>
                  <td className="max-w-[240px] px-3 py-2.5 text-muted-foreground">{a.note || "—"}</td>
                  <td className="px-3 py-2.5"><Consent a={a} /></td>
                  <td className="px-3 py-2.5"><Usable a={a} /></td>
                  <td className="whitespace-nowrap px-3 py-2.5 text-muted-foreground">{ago(a.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-4 md:grid-cols-4 xl:grid-cols-5">
          {shown.map((a) => (
            <button key={a.id} type="button" aria-label={a.name} onClick={() => open(a.id)} className="space-y-2 rounded-xl border p-3 text-left hover:bg-muted/30">
              <span className="flex justify-center"><Thumb a={a} px={170} width={480} /></span>
              <span className="block truncate text-[13px] font-medium">{a.name}</span>
              <span className="flex items-center justify-between gap-2 text-[12px] text-muted-foreground"><span>{kindName(a.kind)}</span><Usable a={a} /></span>
            </button>
          ))}
        </div>
      )}
      <p className="text-[12px] text-muted-foreground">Up to 50 MB a file. Pictures, videos, sound and fonts. A file you add twice is kept once. Archived files are kept but not used for anything new.</p>
    </div>
  );
}

// The files just chosen, each with what it is, before they are added.
function Adding({ brand, chosen, setChosen, done }: { brand: string; chosen: { file: File; kind: BrandAssetKind }[]; setChosen: (c: { file: File; kind: BrandAssetKind }[]) => void; done: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const people = chosen.filter((c) => c.kind === "person" || c.kind === "voice").length;
  async function save() {
    setBusy(true);
    const failed: { file: File; kind: BrandAssetKind }[] = [];
    let added = 0;
    for (const c of chosen) {
      let data = "";
      try { data = await base64(c.file); } catch { /* reported below */ }
      const res = data ? await api.POST("/v1/brands/{brand}/assets", { params: { path: { brand } }, body: { kind: c.kind, filename: c.file.name, data } }) : null;
      if (!res || res.error) { failed.push(c); toast(`${c.file.name}: ${res ? errorMessage(res.error) : "the file could not be read."}`, "error"); } else added++;
    }
    setBusy(false);
    setChosen(failed);
    if (added) toast(added === 1 ? "1 file added." : `${added} files added.`);
    await done();
  }
  return (
    <div className="space-y-3 rounded-xl border p-4">
      <div className="text-[15px] font-medium">{chosen.length === 1 ? "Add this file" : `Add these ${chosen.length} files`}</div>
      <div className="divide-y rounded-lg border">
        {chosen.map((c, i) => (
          <div key={`${c.file.name}:${i}`} className="flex flex-wrap items-center justify-between gap-3 p-3 text-[13px]">
            <span><span className="font-medium">{c.file.name}</span> <span className="text-muted-foreground">{size(c.file.size)}</span></span>
            <span className="flex items-center gap-2">
              <select aria-label={`What ${c.file.name} is`} className={field} value={c.kind} disabled={busy} onChange={(e) => setChosen(chosen.map((x, n) => (n === i ? { ...x, kind: e.target.value as BrandAssetKind } : x)))}>
                {kinds.filter((k) => k.takes.includes(familyOf(c.file))).map((k) => <option key={k.id} value={k.id}>{k.one}</option>)}
              </select>
              <Button variant="ghost" disabled={busy} aria-label={`Leave out ${c.file.name}`} onClick={() => setChosen(chosen.filter((_, n) => n !== i))}>✕</Button>
            </span>
          </div>
        ))}
      </div>
      {people > 0 && <p className="text-[13px] text-muted-foreground">A voice or a picture of a person can't be used until you confirm that person agreed. You do that on the file's page after adding it.</p>}
      <div className="flex gap-2"><Button disabled={busy} onClick={save}>{busy ? "Adding…" : chosen.length === 1 ? "Add file" : `Add ${chosen.length} files`}</Button><Button variant="ghost" disabled={busy} onClick={() => { track("ui.brand_kit_upload_cancelled", { files: chosen.length }); setChosen([]); }}>Cancel</Button></div>
    </div>
  );
}

const colourParts: { id: "primary" | "background" | "text"; label: string; hint: string }[] = [
  { id: "primary", label: "Buttons", hint: "Buttons and links" },
  { id: "background", label: "Background", hint: "Behind the picture and words" },
  { id: "text", label: "Text", hint: "Words on the background" },
];
function Colours({ brand, ws, canManage, changed }: { brand: Brand; ws: string; canManage: boolean; changed?: () => void }) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(brand.colors);
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (!editing) setDraft(brand.colors); }, [brand.colors, editing]);
  const valid = colourParts.every((p) => draft[p.id] === "" || /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.test(draft[p.id]));
  async function save() {
    setBusy(true);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}", { params: { path: { ws, brand: brand.id } }, body: { colors: draft } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Colours saved.");
    setEditing(false);
    changed?.();
  }
  const none = colourParts.every((p) => !brand.colors[p.id]);
  return (
    <div className="rounded-xl border p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div><div className="text-[13px] font-medium">Brand colours</div><div className="text-[12px] text-muted-foreground">Used for backgrounds, buttons and text in every creative.</div></div>
        {!editing ? (
          <div className="flex flex-wrap items-center gap-4">
            {none ? <span className="text-[13px] text-muted-foreground">No colours set yet</span> : colourParts.filter((p) => brand.colors[p.id]).map((p) => <span key={p.id} className="flex items-center gap-2 text-[12px]"><span className="size-6 rounded-md border" style={{ background: brand.colors[p.id] }} /><span>{p.label}<span className="block font-mono text-[11px] uppercase text-muted-foreground">{brand.colors[p.id]}</span></span></span>)}
            {canManage && <Button variant="ghost" onClick={() => setEditing(true)}>{none ? "Set colours" : "Edit colours"}</Button>}
          </div>
        ) : (
          <form className="flex flex-wrap items-end gap-3" onSubmit={(e) => { e.preventDefault(); if (valid) void save(); }}>
            {colourParts.map((p) => (
              <label key={p.id} className="flex flex-col gap-1 text-[12px]"><span title={p.hint}>{p.label}</span>
                <span className="flex items-center gap-1.5"><input type="color" aria-label={`${p.label} colour picker`} value={/^#[0-9a-f]{6}$/i.test(draft[p.id]) ? draft[p.id] : "#000000"} onChange={(e) => setDraft({ ...draft, [p.id]: e.target.value })} className="size-8 cursor-pointer rounded border bg-transparent p-0.5" /><input aria-label={`${p.label} colour`} value={draft[p.id]} placeholder="#1d4ed8" maxLength={7} onChange={(e) => setDraft({ ...draft, [p.id]: e.target.value.trim() })} className={`${field} w-24 font-mono`} /></span>
              </label>
            ))}
            <Button type="submit" disabled={busy || !valid}>Save</Button><Button type="button" variant="ghost" disabled={busy} onClick={() => setEditing(false)}>Cancel</Button>
          </form>
        )}
      </div>
      {editing && !valid && <p className="mt-2 text-[12px] text-red-600">A colour is a # and three or six letters and numbers, like #1d4ed8.</p>}
    </div>
  );
}

function Fact({ k, children }: { k: string; children: ReactNode }) {
  return <div className="grid grid-cols-[150px_1fr] items-center gap-3 border-b py-2.5 text-[13px] last:border-0"><span className="text-muted-foreground">{k}</span><span className="flex flex-wrap items-center gap-2">{children}</span></div>;
}

function FilePage({ a, brandName, canManage, back, reload }: { a: BrandAsset; brandName: string; canManage: boolean; back: () => void; reload: () => Promise<void> }) {
  const [edit, setEdit] = useState<"" | "name" | "note" | "tag" | "consent">("");
  const [text, setText] = useState("");
  const [agreed, setAgreed] = useState(false);
  const [busy, setBusy] = useState(false);
  const path = { brand: a.brand_id, asset_id: a.id };
  const start = (what: typeof edit, value = "") => { setText(value); setAgreed(false); setEdit(what); };

  async function patch(body: { name?: string; note?: string; tags?: string[]; kind?: BrandAssetKind; consent?: { confirmed?: boolean; note?: string } }, said: string) {
    setBusy(true);
    const { error } = await api.PATCH("/v1/brands/{brand}/assets/{asset}", { params: { path: { brand: path.brand, asset: path.asset_id } }, body: { ...body, version: a.version } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(said);
    setEdit("");
    await reload();
  }
  async function archive(on: boolean) {
    const { error } = await api.POST(on ? "/v1/brands/{brand}/assets/{asset}/archive" : "/v1/brands/{brand}/assets/{asset}/restore", { params: { path: { brand: path.brand, asset: path.asset_id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(on ? `${a.name} is archived. It is kept, and not used for anything new.` : `${a.name} is back in the kit.`);
    if (on) back();
    await reload();
  }
  async function remove() {
    if (!(await confirmAction(`Delete ${a.name} for good?`, "The file and every copy of it are removed from storage. This can't be undone. To only stop using it, archive it instead.", "Delete for good"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/assets/{asset}", { params: { path: { brand: path.brand, asset: path.asset_id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`${a.name} is deleted.`);
    back();
    await reload();
  }
  async function takeBack() {
    if (!(await confirmAction("Take back consent?", `${a.name} stops being usable straight away. Creatives already made with it are not changed.`, "Take back consent"))) return;
    await patch({ consent: { confirmed: false } }, "Consent taken back. The file can't be used.");
  }
  const who = a.kind === "voice" ? "The person heard" : "The person shown";
  const inline = (what: "name" | "note", label: string, max: number, save: () => void) => (
    <form className="flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); save(); }}>
      <input autoFocus aria-label={label} maxLength={max} value={text} onChange={(e) => setText(e.target.value)} className={`${field} ${what === "name" ? "w-72" : "w-96 max-w-full"}`} />
      <Button type="submit" disabled={busy || (what === "name" && !text.trim())}>Save</Button><Button type="button" variant="ghost" onClick={() => setEdit("")}>Cancel</Button>
    </form>
  );

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Button variant="ghost" onClick={back}>← Brand kit</Button>
          {edit === "name" ? inline("name", "Name", 200, () => patch({ name: text.trim() }, "Renamed.")) : <><h2 className="text-lg font-medium">{a.name}</h2>{canManage && <Button variant="ghost" onClick={() => start("name", a.name)}>✎ Rename</Button>}</>}
          <Usable a={a} />
        </div>
        <span className="flex gap-2">
          <a href={`${fileUrl(a)}?download=true`} download={a.filename}><Button variant="ghost">Download</Button></a>
          {canManage && <Button variant="ghost" onClick={() => archive(!a.archived_at)}>{a.archived_at ? "Restore" : "Archive"}</Button>}
          {canManage && <Button variant="ghost" className="text-red-600" onClick={remove}>Delete for good</Button>}
        </span>
      </div>
      <div className="grid gap-6 lg:grid-cols-[360px_1fr]">
        <div className="flex min-h-64 items-center justify-center rounded-xl border bg-muted/40 p-6">
          {a.family === "video" ? <video src={fileUrl(a)} controls className="max-h-[420px] max-w-full rounded-md" /> : a.family === "audio" ? <audio src={fileUrl(a)} controls className="w-full" /> : a.family === "font" ? <span className="text-6xl font-semibold" aria-hidden>Aa</span> : <img src={fileUrl(a, 960)} alt={a.name} className="max-h-[420px] max-w-full rounded-md object-contain" />}
        </div>
        <div className="space-y-5">
          {a.consent.needed && !a.consent.confirmed && (
            <div className="rounded-xl border border-amber-500/50 bg-amber-500/5 p-4">
              <div className="text-[15px] font-medium">{a.kind === "voice" ? "This sounds like a real person, so it can't be used until they agree" : "This shows a real person, so it can't be used until they agree"}</div>
              <p className="mt-1 text-[13px] text-muted-foreground">Confirm only when {who.toLowerCase()} has agreed to be in your ads. Outflare records who confirmed it and when. Agents can never confirm this for you.</p>
              {!canManage ? null : edit !== "consent" ? <div className="mt-3"><Button onClick={() => start("consent")}>Confirm consent</Button></div> : (
                <form className="mt-3 space-y-3" onSubmit={(e) => { e.preventDefault(); void patch({ consent: { confirmed: true, note: text.trim() } }, "Consent confirmed. The file is ready to use."); }}>
                  <label className="flex flex-col gap-1 text-[13px]">How did they agree?<input autoFocus required maxLength={2000} value={text} onChange={(e) => setText(e.target.value)} placeholder="Signed release, 18 Nov 2026" className={`${field} w-full max-w-lg`} /></label>
                  <label className="flex items-center gap-2 text-[13px]"><input type="checkbox" checked={agreed} onChange={(e) => setAgreed(e.target.checked)} />{who} agreed to be in {brandName}'s ads</label>
                  <div className="flex gap-2"><Button type="submit" disabled={busy || !agreed || !text.trim()}>Save</Button><Button type="button" variant="ghost" onClick={() => setEdit("")}>Cancel</Button></div>
                </form>
              )}
            </div>
          )}
          <div className="rounded-xl border px-4">
            <Fact k="What it is">{canManage ? <select aria-label="What it is" className={`${field} w-56`} value={a.kind} disabled={busy} onChange={(e) => patch({ kind: e.target.value as BrandAssetKind }, `Now a ${kindName(e.target.value).toLowerCase()}.`)}>{kinds.filter((k) => k.takes.includes(a.family)).map((k) => <option key={k.id} value={k.id}>{k.one}</option>)}</select> : kindName(a.kind)}</Fact>
            <Fact k="File">{typeLine(a)} · {size(a.bytes)} <span className="text-muted-foreground">{a.filename}</span></Fact>
            <Fact k="When to use it">{edit === "note" ? inline("note", "When to use it", 2000, () => patch({ note: text.trim() }, "Saved.")) : <>{a.note || <span className="text-muted-foreground">No note yet</span>}{canManage && <Button variant="ghost" onClick={() => start("note", a.note)}>Edit</Button>}</>}</Fact>
            <Fact k="Tags">
              {a.tags.map((t) => <Badge key={t} tone="grey">{t}{canManage && <button type="button" aria-label={`Remove tag ${t}`} className="ml-1 opacity-60 hover:opacity-100" onClick={() => patch({ tags: a.tags.filter((x) => x !== t) }, "Tag removed.")}>✕</button>}</Badge>)}
              {edit === "tag" ? (
                <form className="flex items-center gap-2" onSubmit={(e) => { e.preventDefault(); const t = text.trim().toLowerCase(); if (t) void patch({ tags: [...a.tags.filter((x) => x !== t), t] }, "Tag added."); }}>
                  <input autoFocus aria-label="New tag" maxLength={50} value={text} onChange={(e) => setText(e.target.value)} className={`${field} w-40`} /><Button type="submit" disabled={busy || !text.trim()}>Save</Button><Button type="button" variant="ghost" onClick={() => setEdit("")}>Cancel</Button>
                </form>
              ) : canManage && a.tags.length < 20 && <Button variant="ghost" onClick={() => start("tag")}>+ Add</Button>}
            </Fact>
            <Fact k="Consent">{a.consent.needed && a.consent.confirmed ? <><Status tone="green">On file</Status><span className="text-muted-foreground">{a.consent.note}{a.consent.at ? ` · confirmed ${ago(a.consent.at)}` : ""}</span>{canManage && <Button variant="ghost" onClick={takeBack}>Take back</Button>}</> : <Consent a={a} />}</Fact>
            <Fact k="Added">{ago(a.created_at)}{a.source === "generated" ? " · made by Outflare" : ""}</Fact>
          </div>
        </div>
      </div>
    </div>
  );
}
