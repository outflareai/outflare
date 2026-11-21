// Creatives library: every ad the brand made from its skills, as a list or
// a gallery, with what it is for and where it stands. A creative's page
// shows the finished ad in each size, the words laid over the picture
// (edited without making a new picture), what made it, and a person's
// approve or reject. Nothing reaches an ad network without that yes.

import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import type { Brand, Creative, CreativeHistoryEntry, CreativeStatus } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { track } from "../../lib/analytics";
import { ago } from "../../format";
import { Badge, Button, Empty, ErrorText, Loading, field, toast } from "../../ui";

const statuses: { id: CreativeStatus; label: string; tone: "amber" | "blue" | "green" | "grey" | "red" }[] = [
  { id: "needs_review", label: "Waiting for approval", tone: "amber" },
  { id: "approved", label: "Approved", tone: "green" },
  { id: "rejected", label: "Rejected", tone: "grey" },
  { id: "making", label: "Being made", tone: "blue" },
  { id: "failed", label: "Not made", tone: "red" },
];
const statusOf = (c: Creative) => statuses.find((s) => s.id === c.status) ?? statuses[0];
const viewKey = "outflare.creatives.view";
type View = "list" | "gallery";
const readView = (): View => { try { return localStorage.getItem(viewKey) === "gallery" ? "gallery" : "list"; } catch { return "list"; } };

const base = (c: Creative) => `/v1/brands/${c.brand_id}/creatives/${c.id}`;
// The finished ad. The version is in the address so a change of words
// shows a new picture.
const adUrl = (c: Creative, size: string, width: number) => `${base(c)}/ad?width=${width}${size ? `&size=${encodeURIComponent(size)}` : ""}&v=${c.version}`;
const fileUrl = (c: Creative) => `/v1/brands/${c.brand_id}/assets/${c.asset_id}/file`;
const sizesOf = (c: Creative) => (c.sizes.length ? c.sizes : ["1:1"]);
const forWhom = (c: Creative) => [c.persona, c.pain].filter(Boolean).join(" · ");
const madeBy = (c: Creative) => (c.skill_name ? `Made by ${c.skill_name} · v${c.skill_version} · prompt ${c.variant}` : "Its skill was deleted");

// A creative's picture: the finished ad, or a sign for what can't be shown yet.
function Art({ c, size, width, className = "" }: { c: Creative; size: string; width: number; className?: string }) {
  const [broken, setBroken] = useState(false);
  const sign = (text: string) => <span className={`flex items-center justify-center rounded-lg bg-muted/60 p-3 text-center text-[12px] text-muted-foreground ${className}`}>{text}</span>;
  if (c.status === "making") return sign("Being made…");
  if (c.status === "failed" || !c.asset_id) return sign("No picture was made");
  if (c.kind === "video") return <video src={fileUrl(c)} muted playsInline preload="metadata" className={`rounded-lg bg-black object-contain ${className}`} />;
  if (broken) return sign("The picture can't be shown");
  return <img src={adUrl(c, size, width)} alt={c.headline || c.name} loading="lazy" className={`rounded-lg object-contain ${className}`} onError={() => setBroken(true)} />;
}

const StatusBadge = ({ c }: { c: Creative }) => <Badge tone={statusOf(c).tone}>{statusOf(c).label}</Badge>;
const Sizes = ({ c, on }: { c: Creative; on?: string }) => <span className="flex flex-wrap gap-1">{sizesOf(c).map((s) => <span key={s} className={`rounded border px-1.5 font-mono text-[11px] ${s === on ? "bg-foreground text-background" : "text-muted-foreground"}`}>{s}</span>)}</span>;

type Props = { brand: Brand; canManage: boolean; item: string; open: (id: string) => void; back: () => void };

export function CreativeLibrary({ brand, canManage, item, open, back }: Props) {
  const [rows, setRows] = useState<Creative[] | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/creatives", { params: { path: { brand: brand.id } } });
    setError(error ? errorMessage(error) : "");
    setRows(data?.data ?? []);
  }, [brand.id]);
  useEffect(() => { setRows(null); void load(); }, [load]);
  // While something is being made, look again every few seconds.
  const making = (rows ?? []).some((c) => c.status === "making");
  useEffect(() => {
    if (!making) return;
    const t = setInterval(() => void load(), 5000);
    return () => clearInterval(t);
  }, [making, load]);

  // One answer for one or many creatives; says what happened once.
  const review = useCallback(async (list: Creative[], yes: boolean, reason = "") => {
    let done = 0;
    for (const c of list) {
      const params = { path: { brand: c.brand_id, creative: c.id } };
      const { error } = yes ? await api.POST("/v1/brands/{brand}/creatives/{creative}/approve", { params }) : await api.POST("/v1/brands/{brand}/creatives/{creative}/reject", { params, body: { reason } });
      if (error) toast(`${c.name}: ${errorMessage(error)}`, "error"); else done++;
    }
    if (done) toast(done === 1 ? (yes ? "Approved." : "Rejected.") : `${done} creatives ${yes ? "approved" : "rejected"}.`);
    await load();
  }, [load]);

  if (item) {
    if (!rows) return <Loading />;
    const c = rows.find((r) => r.id === item);
    if (!c) return <Empty title="This creative is gone" action={<Button onClick={back}>Back to Creatives</Button>} />;
    return <CreativePage key={c.id} c={c} canManage={canManage} back={back} reload={load} review={review} />;
  }
  return <Library brand={brand} rows={rows} error={error} canManage={canManage} open={open} review={review} />;
}

function Library({ brand, rows, error, canManage, open, review }: { brand: Brand; rows: Creative[] | null; error: string; canManage: boolean; open: (id: string) => void; review: (list: Creative[], yes: boolean, reason?: string) => Promise<void> }) {
  const [status, setStatus] = useState<CreativeStatus | "">("");
  const [q, setQ] = useState("");
  const [view, setViewState] = useState<View>(readView);
  const [picked, setPicked] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const setView = (v: View) => { setViewState(v); try { localStorage.setItem(viewKey, v); } catch { /* the choice just isn't kept */ } track("ui.creatives_view_changed", { view: v }); };
  const all = rows ?? [];
  const count = (s: CreativeStatus) => all.filter((c) => c.status === s).length;
  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return all.filter((c) => (!status || c.status === status) && (!needle || [c.name, c.headline, c.code, c.persona, c.pain, ...c.tags].some((t) => t.toLowerCase().includes(needle))));
  }, [all, status, q]);
  // Only creatives waiting for an answer can be picked for one.
  const pickable = shown.filter((c) => c.status === "needs_review");
  const chosen = pickable.filter((c) => picked.includes(c.id));
  const toggle = (id: string) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));
  const answer = async (list: Creative[], yes: boolean) => { setBusy(true); await review(list, yes); setBusy(false); setPicked([]); };
  const waiting = count("needs_review");
  const buttons = (c: Creative, wide = false) => canManage && c.status === "needs_review" ? (
    <span className="flex gap-2" onClick={(e) => e.stopPropagation()}>
      <Button className={wide ? "flex-1" : ""} disabled={busy} onClick={() => answer([c], true)}>Approve</Button>
      <Button variant="ghost" className={wide ? "flex-1" : ""} disabled={busy} onClick={() => answer([c], false)}>Reject</Button>
    </span>
  ) : c.status === "approved" && c.kind === "image" ? (
    <span onClick={(e) => e.stopPropagation()}><a href={`${adUrl(c, sizesOf(c)[0], 1080)}&download=true`} download><Button variant="ghost">Download</Button></a></span>
  ) : null;
  const check = (c: Creative) => c.status === "needs_review" && canManage ? <input type="checkbox" aria-label={`Select ${c.name}`} checked={picked.includes(c.id)} onClick={(e) => e.stopPropagation()} onChange={() => toggle(c.id)} /> : null;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="text-lg font-medium">{rows ? (all.length === 1 ? "1 creative" : `${all.length} creatives`) : "Creatives"}{waiting > 0 && `, ${waiting} waiting for you`}</h2>
          <p className="max-w-3xl text-[13px] text-muted-foreground">Every ad {brand.name} made from its skills. Each one waits here for a person's approval: nothing goes to an ad network without it.</p>
        </div>
        <span role="group" aria-label="View" className="inline-flex overflow-hidden rounded-md border text-[13px]">
          {(["list", "gallery"] as const).map((v) => <button key={v} type="button" aria-pressed={view === v} onClick={() => setView(v)} className={`px-3 py-1.5 ${view === v ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground"}`}>{v === "list" ? "☰ List" : "▦ Gallery"}</button>)}
        </span>
      </div>
      {error && <ErrorText>{error}</ErrorText>}

      <div className="flex flex-wrap items-center gap-1.5 text-[13px]">
        {[{ id: "" as const, label: "All", n: all.length }, ...statuses.filter((s) => count(s.id) > 0 || s.id === "needs_review" || s.id === "approved").map((s) => ({ id: s.id, label: s.label, n: count(s.id) }))].map((s) => (
          <button key={s.id} type="button" aria-pressed={status === s.id} onClick={() => { setStatus(s.id); setPicked([]); }} className={`rounded-full border px-3 py-1 ${s.id === status ? "border-foreground bg-foreground text-background" : "text-muted-foreground hover:text-foreground"}`}>{s.label} <span className="font-mono text-[11px] opacity-70">{s.n}</span></button>
        ))}
        <input type="search" aria-label="Search creatives" placeholder="Search by name, headline, persona or tag" value={q} onChange={(e) => setQ(e.target.value)} className={`${field} ml-auto w-80`} />
      </div>

      {canManage && pickable.length > 1 && (
        <div className="flex flex-wrap items-center gap-3 rounded-xl border bg-muted/30 px-4 py-2.5 text-[13px]">
          <label className="flex items-center gap-2"><input type="checkbox" aria-label="Select all waiting for approval" checked={chosen.length === pickable.length} onChange={(e) => setPicked(e.target.checked ? pickable.map((c) => c.id) : [])} />{chosen.length ? `${chosen.length} selected` : `Select all ${pickable.length} waiting for approval`}</label>
          {chosen.length > 0 && <><Button disabled={busy} onClick={() => answer(chosen, true)}>Approve {chosen.length}</Button><Button variant="ghost" disabled={busy} onClick={() => answer(chosen, false)}>Reject {chosen.length}</Button></>}
        </div>
      )}

      {!rows ? <Loading /> : all.length === 0 ? (
        <Empty title="No creatives yet">Creatives are made from a skill, under Skills and prompts. Each one lands here to be approved.</Empty>
      ) : shown.length === 0 ? (
        <Empty title="No creative matches" action={<Button variant="ghost" onClick={() => { setStatus(""); setQ(""); }}>Show all creatives</Button>} />
      ) : view === "list" ? (
        <div className="overflow-x-auto rounded-xl border">
          <table className="w-full text-[13px]">
            <thead className="border-b bg-muted/40 text-left text-[12px] text-muted-foreground"><tr>{["", "Creative", "Sizes", "Status", "Made", ""].map((h, i) => <th key={i} className="whitespace-nowrap px-3 py-2.5 font-medium">{h}</th>)}</tr></thead>
            <tbody className="divide-y">
              {shown.map((c) => (
                <tr key={c.id} className="cursor-pointer hover:bg-muted/30" onClick={() => open(c.id)}>
                  <td className="w-8 px-3">{check(c)}</td>
                  <td className="px-3 py-3">
                    <button type="button" aria-label={c.name} onClick={(e) => { e.stopPropagation(); open(c.id); }} className="flex items-center gap-4 text-left">
                      <span className="flex h-[136px] w-[128px] shrink-0 items-center justify-center rounded-lg bg-muted/40"><Art c={c} size={sizesOf(c)[0]} width={240} className="max-h-[128px] max-w-[120px] min-h-16 min-w-16" /></span>
                      <span>
                        <span className="block text-[15px] font-medium">{c.name}</span>
                        {forWhom(c) && <span className="mt-0.5 block text-muted-foreground">For {forWhom(c)}</span>}
                        <span className="mt-1.5 block text-[12px] text-muted-foreground">{madeBy(c)}{c.model ? ` · ${c.model}` : ""}</span>
                        <span className="mt-0.5 block font-mono text-[11px] text-muted-foreground">{c.code}</span>
                      </span>
                    </button>
                  </td>
                  <td className="px-3"><span className="mb-1 block text-[11px] text-muted-foreground">{c.kind === "video" ? "Video" : "Image"}</span><Sizes c={c} /></td>
                  <td className="px-3"><StatusBadge c={c} />{c.status === "failed" && c.error && <span className="mt-1 block max-w-[220px] text-[12px] text-muted-foreground">{c.error}</span>}{c.status === "rejected" && c.review_reason && <span className="mt-1 block max-w-[220px] text-[12px] text-muted-foreground">{c.review_reason}</span>}</td>
                  <td className="whitespace-nowrap px-3 text-muted-foreground">{ago(c.created_at)}</td>
                  <td className="px-3 text-right">{buttons(c)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="grid gap-5 md:grid-cols-2 xl:grid-cols-3">
          {shown.map((c) => (
            <article key={c.id} className="flex cursor-pointer flex-col overflow-hidden rounded-xl border bg-card hover:bg-muted/20" onClick={() => open(c.id)}>
              <div className="relative flex h-[320px] items-center justify-center bg-muted/40 p-4">
                <Art c={c} size={sizesOf(c)[0]} width={480} className="max-h-full max-w-full min-h-24 min-w-24" />
                <span className="absolute left-3 top-3"><StatusBadge c={c} /></span>
                <span className="absolute right-3 top-3">{check(c)}</span>
              </div>
              <div className="flex flex-1 flex-col gap-3 p-4">
                <div>
                  <div className="flex items-start justify-between gap-2"><button type="button" aria-label={c.name} className="text-left text-[15px] font-medium hover:underline" onClick={(e) => { e.stopPropagation(); open(c.id); }}>{c.name}</button><span className="shrink-0 font-mono text-[11px] text-muted-foreground">{c.code}</span></div>
                  {forWhom(c) && <div className="mt-1 text-[13px] text-muted-foreground">For {forWhom(c)}</div>}
                </div>
                <div className="flex items-center gap-2"><span className="text-[11px] text-muted-foreground">{c.kind === "video" ? "Video" : "Image"} sizes</span><Sizes c={c} /></div>
                <div className="text-[12px] text-muted-foreground">{madeBy(c)}{c.model ? ` · ${c.model}` : ""}</div>
                <div className="mt-auto">{buttons(c, true)}</div>
              </div>
            </article>
          ))}
        </div>
      )}
      <p className="text-[12px] text-muted-foreground">Source: creatives saved for {brand.name}. Every picture and video is made on your own account with the AI provider and billed by them, not by Outflare.</p>
    </div>
  );
}

function Panel({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return <section className="rounded-xl border"><header className="flex items-center justify-between gap-2 border-b px-4 py-2.5"><h3 className="text-[14px] font-medium">{title}</h3>{action}</header><div className="p-4">{children}</div></section>;
}
function Row({ k, children }: { k: string; children: ReactNode }) {
  return <div className="grid grid-cols-[130px_1fr] gap-2 py-1.5 text-[13px]"><dt className="text-muted-foreground">{k}</dt><dd className="min-w-0 break-words">{children}</dd></div>;
}

type Words = { headline: string; subline: string; cta: string };
type Filed = { persona: string; pain: string; hook: string; tags: string };

function CreativePage({ c, canManage, back, reload, review }: { c: Creative; canManage: boolean; back: () => void; reload: () => Promise<void>; review: (list: Creative[], yes: boolean, reason?: string) => Promise<void> }) {
  const [size, setSizeState] = useState(sizesOf(c)[0]);
  const [edit, setEdit] = useState<"" | "name" | "words" | "filed" | "reject">("");
  const [name, setName] = useState(c.name);
  const [words, setWords] = useState<Words>({ headline: c.headline, subline: c.subline, cta: c.cta });
  const [filed, setFiled] = useState<Filed>({ persona: c.persona, pain: c.pain, hook: c.hook, tags: c.tags.join(", ") });
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const setSize = (s: string) => { setSizeState(s); track("ui.creative_size_changed", { size: s }); };
  const start = (what: typeof edit) => { setName(c.name); setWords({ headline: c.headline, subline: c.subline, cta: c.cta }); setFiled({ persona: c.persona, pain: c.pain, hook: c.hook, tags: c.tags.join(", ") }); setReason(""); setEdit(what); };

  async function patch(body: Partial<Words> & { name?: string; persona?: string; pain?: string; hook?: string; tags?: string[] }, said: string) {
    setBusy(true);
    const { error } = await api.PATCH("/v1/brands/{brand}/creatives/{creative}", { params: { path: { brand: c.brand_id, creative: c.id } }, body: { ...body, version: c.version } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(said);
    setEdit("");
    await reload();
  }
  const answer = async (yes: boolean, why = "") => { setBusy(true); await review([c], yes, why); setBusy(false); setEdit(""); };

  // Like and not this: an opinion, separate from approving, kept with why.
  const [notThis, setNotThis] = useState<string | null>(null);
  const [asRule, setAsRule] = useState(false);
  const [history, setHistory] = useState<CreativeHistoryEntry[]>([]);
  useEffect(() => {
    void api.GET("/v1/brands/{brand}/creatives/{creative}/history", { params: { path: { brand: c.brand_id, creative: c.id } } }).then(({ data }) => setHistory(data?.data ?? []));
  }, [c.brand_id, c.id, c.version, c.rating, c.rating_reason]);
  const rate = async (rating: "" | "liked" | "not_this", why = "") => {
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/creatives/{creative}/rating", { params: { path: { brand: c.brand_id, creative: c.id } }, body: { rating, reason: why } });
    let ruled = false;
    if (!error && rating === "not_this" && asRule && why) {
      const made = await api.POST("/v1/brands/{brand}/creative-lessons", { params: { path: { brand: c.brand_id } }, body: { kind: "rule", rule: why, applies_to: c.kind, source_type: "creative", source_id: c.id } });
      if (made.error) toast(errorMessage(made.error), "error"); else ruled = true;
    }
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(rating === "liked" ? "Liked." : rating === "not_this" ? (ruled ? "Marked not this, and remembered as a rule for every new one." : "Marked not this.") : "Taken back.");
    setNotThis(null); setAsRule(false);
    await reload();
  };
  const historyText = (h: CreativeHistoryEntry) => {
    const said = h.said ? `: "${h.said}"` : "";
    return ({ made: `Made it${said}`, words_changed: "Changed its name, words or tags", approved: "Approved it", rejected: `Rejected it${said}`, liked: `Liked it${said}`, not_this: `Said not this${said}`, rating_cleared: "Took the like or not this back" } as Record<string, string>)[h.kind] ?? h.kind.replace(/_/g, " ") + said;
  };
  const answered = c.status === "approved" || c.status === "rejected";
  const input = (label: string, value: string, set: (v: string) => void, max: number, hint?: string) => (
    <label className="flex flex-col gap-1 text-[13px]">{label}<input aria-label={label} maxLength={max} value={value} onChange={(e) => set(e.target.value)} className={`${field} w-full`} />{hint && <span className="text-[12px] text-muted-foreground">{hint}</span>}</label>
  );
  const SaveCancel = ({ disabled }: { disabled?: boolean }) => <div className="flex gap-2 pt-1"><Button type="submit" disabled={busy || disabled}>Save</Button><Button type="button" variant="ghost" disabled={busy} onClick={() => setEdit("")}>Cancel</Button></div>;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <button type="button" onClick={back} className="text-[13px] text-muted-foreground hover:text-foreground">← Creatives</button>
          <h2 className="mt-1 flex flex-wrap items-center gap-2 text-xl font-medium">
            {edit === "name" ? (
              <form className="flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); if (name.trim()) void patch({ name: name.trim() }, "Renamed."); }}>
                <input autoFocus aria-label="Creative name" maxLength={200} value={name} onChange={(e) => setName(e.target.value)} className="h-9 w-80 rounded-md border bg-background px-2 text-[16px] font-normal" />
                <Button type="submit" disabled={busy || !name.trim()}>Save</Button><Button type="button" variant="ghost" onClick={() => setEdit("")}>Cancel</Button>
              </form>
            ) : <>{c.name}{canManage && <button type="button" onClick={() => start("name")} className="rounded-md border px-2 py-0.5 text-[12px] font-normal text-muted-foreground hover:text-foreground">✎ Rename</button>}</>}
            <StatusBadge c={c} />
          </h2>
          <p className="mt-0.5 text-[13px] text-muted-foreground"><span className="font-mono">{c.code}</span>{forWhom(c) && ` · For ${forWhom(c)}`} · made {ago(c.created_at)}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          {canManage && (c.status === "needs_review" || c.status === "rejected") && <Button disabled={busy} onClick={() => answer(true)}>Approve</Button>}
          {canManage && (c.status === "needs_review" || c.status === "approved") && <Button variant="ghost" disabled={busy} onClick={() => start("reject")}>Reject</Button>}
          {c.asset_id && <a href={c.kind === "image" ? `${adUrl(c, size, 1080)}&download=true` : `${fileUrl(c)}?download=true`} download><Button variant="ghost">Download{c.kind === "image" ? ` ${size}` : ""}</Button></a>}
        </div>
      </div>

      {edit === "reject" && (
        <form className="space-y-3 rounded-xl border p-4" onSubmit={(e) => { e.preventDefault(); void answer(false, reason.trim()); }}>
          <label className="flex flex-col gap-1 text-[13px]">Why are you rejecting it? (optional)<input autoFocus aria-label="Why are you rejecting it?" maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Looks made by AI" className={`${field} w-full max-w-xl`} /><span className="text-[12px] text-muted-foreground">The reason is kept with the creative, so the next ones can avoid it.</span></label>
          <div className="flex gap-2"><Button type="submit" disabled={busy}>Reject</Button><Button type="button" variant="ghost" onClick={() => setEdit("")}>Cancel</Button></div>
        </form>
      )}
      {c.status === "failed" && <div className="rounded-xl border border-red-500/40 bg-red-500/5 p-4 text-[13px]"><div className="text-[15px] font-medium">No picture was made</div>{c.error || "The AI account ended without a file."}</div>}
      {c.status === "rejected" && c.review_reason && <div className="rounded-xl border bg-muted/30 p-4 text-[13px]"><span className="font-medium">Rejected:</span> {c.review_reason}</div>}

      <div className="grid items-start gap-5 xl:grid-cols-[1fr_420px]">
        <div className="space-y-3">
          {c.kind === "image" && (
            <div role="group" aria-label="Size" className="inline-flex overflow-hidden rounded-md border text-[13px]">
              {sizesOf(c).map((s) => <button key={s} type="button" aria-pressed={s === size} onClick={() => setSize(s)} className={`px-3 py-1.5 font-mono ${s === size ? "bg-foreground text-background" : "text-muted-foreground hover:text-foreground"}`}>{s}</button>)}
            </div>
          )}
          <div className="flex h-[540px] items-center justify-center rounded-xl border bg-muted/40 p-4">
            {c.kind === "video" && c.asset_id ? <video src={fileUrl(c)} controls className="max-h-full max-w-full rounded-lg" /> : <Art c={c} size={size} width={1080} className="max-h-full max-w-full min-h-40 min-w-40" />}
          </div>
          {c.kind === "image" && <p className="text-[12px] text-muted-foreground">The headline, logo and button are laid over the picture by Outflare, so you can edit the words or switch size without paying for a new picture.</p>}
          {c.asset_id && (c.rating === "" ? (
            canManage && (notThis === null ? (
              <div className="flex flex-wrap items-center gap-2"><Button variant="ghost" disabled={busy} onClick={() => rate("liked")}>Like</Button><Button variant="ghost" disabled={busy} onClick={() => setNotThis("")}>Not this</Button><span className="text-[12px] text-muted-foreground">Like marks what to make more of. Not this asks why, and keeps the reason.</span></div>
            ) : (
              <form className="space-y-2 rounded-xl border p-4" onSubmit={(e) => { e.preventDefault(); void rate("not_this", notThis.trim()); }}>
                <label className="flex flex-col gap-1 text-[13px]">Why not? It is kept with the creative.<input autoFocus aria-label="Why not" maxLength={500} value={notThis} onChange={(e) => setNotThis(e.target.value)} placeholder="The hands look wrong" className={`${field} w-full max-w-xl`} /></label>
                <label className="flex items-center gap-2 text-[13px]"><input type="checkbox" checked={asRule} disabled={!notThis.trim()} onChange={(e) => setAsRule(e.target.checked)} />Remember this as a rule for every new {c.kind === "image" ? "picture" : "video"}</label>
                <div className="flex gap-2"><Button type="submit" disabled={busy}>Save</Button><Button type="button" variant="ghost" onClick={() => { setNotThis(null); setAsRule(false); }}>Cancel</Button></div>
              </form>
            ))
          ) : (
            <div className="rounded-xl border p-3 text-[13px]"><Badge tone={c.rating === "liked" ? "green" : "grey"}>{c.rating === "liked" ? "Liked" : "Not this"}</Badge>{c.rating_reason && <span className="ml-2">"{c.rating_reason}"</span>}{canManage && <button type="button" className="ml-2 text-[12px] underline" disabled={busy} onClick={() => rate("")}>Change my mind</button>}</div>
          ))}
          <Panel title="Everything that happened to this creative">
            {history.length === 0 ? <p className="text-[13px] text-muted-foreground">Nothing yet.</p> : (
              <table className="w-full text-[13px]"><tbody>{history.map((h) => <tr key={h.id} className="border-b last:border-0"><td className="whitespace-nowrap py-2 pr-4 text-muted-foreground">{ago(h.created_at)}</td><td className="whitespace-nowrap py-2 pr-4">{h.actor_name ?? (h.actor_id ? "Someone" : "Outflare")}</td><td className="py-2">{historyText(h)}</td></tr>)}</tbody></table>
            )}
          </Panel>
        </div>

        <div className="space-y-5">
          <Panel title="Words on the ad" action={canManage && edit !== "words" && c.kind === "image" ? <Button variant="ghost" onClick={() => start("words")}>Edit</Button> : undefined}>
            {edit === "words" ? (
              <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); void patch({ headline: words.headline.trim(), subline: words.subline.trim(), cta: words.cta.trim() }, "Words saved. No new picture was made."); }}>
                {input("Headline", words.headline, (v) => setWords({ ...words, headline: v }), 200)}
                {input("Second line", words.subline, (v) => setWords({ ...words, subline: v }), 300)}
                {input("Button", words.cta, (v) => setWords({ ...words, cta: v }), 60)}
                <SaveCancel />
              </form>
            ) : (
              <dl>
                <Row k="Headline">{c.headline || <span className="text-muted-foreground">None</span>}</Row>
                <Row k="Second line">{c.subline || <span className="text-muted-foreground">None</span>}</Row>
                <Row k="Button">{c.cta || <span className="text-muted-foreground">None</span>}</Row>
              </dl>
            )}
          </Panel>

          <Panel title="What it is filed under" action={canManage && edit !== "filed" ? <Button variant="ghost" onClick={() => start("filed")}>Edit</Button> : undefined}>
            {edit === "filed" ? (
              <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); void patch({ persona: filed.persona.trim(), pain: filed.pain.trim(), hook: filed.hook.trim(), tags: [...new Set(filed.tags.split(",").map((t) => t.trim().toLowerCase()).filter(Boolean))] }, "Saved."); }}>
                {input("Persona", filed.persona, (v) => setFiled({ ...filed, persona: v }), 200, "Who the ad is for.")}
                {input("Pain", filed.pain, (v) => setFiled({ ...filed, pain: v }), 200, "The one problem it is about.")}
                {input("Hook", filed.hook, (v) => setFiled({ ...filed, hook: v }), 200, "How it opens, in a few words.")}
                {input("Tags", filed.tags, (v) => setFiled({ ...filed, tags: v }), 1000, "Separate tags with commas.")}
                <SaveCancel />
              </form>
            ) : (
              <dl>
                <Row k="Persona">{c.persona || <span className="text-muted-foreground">None</span>}</Row>
                <Row k="Pain">{c.pain || <span className="text-muted-foreground">None</span>}</Row>
                <Row k="Hook">{c.hook || <span className="text-muted-foreground">None</span>}</Row>
                <Row k="Tags">{c.tags.length ? <span className="flex flex-wrap gap-1">{c.tags.map((t) => <Badge key={t} tone="grey">{t}</Badge>)}</span> : <span className="text-muted-foreground">None</span>}</Row>
              </dl>
            )}
            <p className="mt-2 text-[12px] text-muted-foreground">Results are compared by these, so you learn which persona, pain and opening pay.</p>
          </Panel>

          <Panel title="How it was made" action={<Button variant="ghost" onClick={() => { void navigator.clipboard.writeText(c.prompt); toast("Prompt copied."); }}>Copy prompt</Button>}>
            <dl>
              <Row k="Skill">{c.skill_name ? `${c.skill_name} · v${c.skill_version}` : <span className="text-muted-foreground">Deleted</span>}</Row>
              <Row k="Prompt">{c.variant || "—"}</Row>
              <Row k="AI model">{c.model ? `${c.model}${c.provider ? ` (${c.provider}, your own account)` : ""}` : "—"}</Row>
              {answered && c.reviewed_at && <Row k={c.status === "approved" ? "Approved" : "Rejected"}>{ago(c.reviewed_at)}</Row>}
            </dl>
            <div className="mt-2 max-h-48 overflow-auto whitespace-pre-line rounded-md border bg-background px-3 py-2 font-mono text-[12px] leading-6">{c.prompt}</div>
          </Panel>
        </div>
      </div>
    </div>
  );
}
