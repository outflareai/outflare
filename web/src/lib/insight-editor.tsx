import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, confirmAction, CopyButton, Empty, ErrorText, field, Loading, toast } from "../ui";
import { ago } from "../format";
import { appPath, parsePath } from "@/lib/paths";
import { pageParams } from "@/lib/urlParam";
import { nameItem } from "@/lib/records";
import type { InsightEmbed } from "@/lib/insight-embed";

// One editor for the Funnels, Trends, Retention and Paths builders
// (docs/insights.md). What is on screen is a draft: the chart follows
// every change, and the saved insight changes only on Save. The address
// says what is open, so a link, a reload and Back all return to it:
//
//   /{workspace}/{brand}/funnels                the list of saved funnels
//   /{workspace}/{brand}/funnels/new            a new one, not saved yet
//   /{workspace}/{brand}/funnels/{insight id}   a saved funnel
//   ...?draft={definition}                      changes not saved yet, on
//                                               a saved one or a new one

type Insight = Schemas["Insight"];

const draftParam = "draft";
pageParams.add(draftParam);
// Longer than this and the address is left alone: browsers and proxies
// cut very long ones.
const draftLimit = 6000;

// draftHref is the address of a new, unsaved insight on this page that
// opens with spec, or "" when the spec is too long for an address.
export function draftHref(spec: unknown): string {
  const enc = encodeDraft(spec);
  return enc.length <= draftLimit ? `${itemHref(newItem)}?${draftParam}=${enc}` : "";
}

function encodeDraft(spec: unknown): string {
  const bytes = new TextEncoder().encode(JSON.stringify(spec));
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function decodeDraft<S>(raw: string): S | null {
  try {
    const s = atob(raw.replace(/-/g, "+").replace(/_/g, "/"));
    const v = JSON.parse(new TextDecoder().decode(Uint8Array.from(s, (c) => c.charCodeAt(0))));
    return v && typeof v === "object" && !Array.isArray(v) ? (v as S) : null;
  } catch {
    return null;
  }
}

// A draft that arrived on the list's address, kept for the new builder
// the page opens for it.
let carried: string | null = null;

function urlDraft<S>(): S | null {
  const raw = new URLSearchParams(location.search).get(draftParam) ?? carried;
  carried = null;
  return raw ? decodeDraft<S>(raw) : null;
}

function writeDraft(value: string | null) {
  const q = new URLSearchParams(location.search);
  if ((q.get(draftParam) ?? null) === value) return;
  if (value) q.set(draftParam, value);
  else q.delete(draftParam);
  const qs = q.toString();
  history.replaceState(history.state, "", location.pathname + (qs ? `?${qs}` : "") + location.hash);
}

/** The item part of the address of a new insight, not saved yet. */
export const newItem = "new";

/**
 * What the address opens: the list of saved insights, or the builder on a
 * saved one or a new one. A draft in the address or handed over by another
 * page, with no insight named, opens the new builder.
 */
export function useInsightRoute(item: string | undefined, onOpen: ((id?: string) => void) | undefined, handedOver: boolean): { listing: boolean; item?: string } {
  const handed = !item && (handedOver || !!new URLSearchParams(location.search).get(draftParam));
  useEffect(() => {
    if (!handed || !onOpen) return;
    // The draft leaves the list's address, so Back lands on the list and
    // stays there; the builder puts it back on its own address.
    carried = new URLSearchParams(location.search).get(draftParam);
    writeDraft(null);
    onOpen(newItem);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [handed]);
  return { listing: !item, item: item === newItem ? undefined : item };
}

/** The address of this page with another item, or none, open. */
export function itemHref(item?: string): string {
  const p = parsePath(location.pathname);
  return p.ws && p.tab ? appPath(p.ws, p.tab, item) : location.pathname;
}

export type InsightEditor<S> = {
  spec: S;
  setSpec: (next: S | ((s: S) => S)) => void;
  current: Insight | null;
  name: string;
  setName: (n: string) => void;
  saved: Insight[];
  /** The draft differs from what is saved (or, for a new one, from the starter). */
  dirty: boolean;
  /** The address names an insight that is gone, or one of another kind. */
  missing: boolean;
  loading: boolean;
  open: (id?: string) => Promise<void>;
  /** Back to the list, after asking when there are unsaved changes. */
  close: () => Promise<void>;
  save: () => Promise<void>;
  saveCopy: () => Promise<void>;
  cancel: () => void;
  remove: (warning: string) => Promise<void>;
};

export function useInsightEditor<S>(o: {
  brand: string;
  kind: Insight["kind"];
  starter: S;
  /** What the API is sent: the spec without what it would refuse. */
  clean: (s: S) => S;
  /** A spec handed over by another page, used once. */
  initial?: () => S | null;
  embed?: InsightEmbed;
  embedSpec?: () => S;
  /** The insight the address names, and how the page opens another. */
  item?: string;
  onOpen?: (id?: string) => void;
}): InsightEditor<S> {
  const { brand, kind, starter, clean, embed, item, onOpen } = o;
  const [current, setCurrent] = useState<Insight | null>(embed?.insight ?? null);
  const [spec, setSpec] = useState<S>(() => (embed ? (o.embedSpec?.() ?? starter) : item ? starter : (o.initial?.() ?? urlDraft<S>() ?? starter)));
  const [name, setName] = useState(embed?.insight?.name ?? "");
  const [saved, setSaved] = useState<Insight[]>([]);
  const [missing, setMissing] = useState(false);
  const [loading, setLoading] = useState(!embed && !!item);
  // The insight on screen, so a save that changes the address loads nothing.
  const shown = useRef<string | undefined>(embed?.insight?.id);
  // An opening the person asked for here starts from what is saved; one
  // from a link, a reload or Back also takes the draft in the address.
  const clicked = useRef(false);

  const loadSaved = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/insights", { params: { path: { brand }, query: { kind } } });
    if (data) setSaved(data.data);
  }, [brand, kind]);
  useEffect(() => {
    loadSaved();
  }, [loadSaved]);

  // Inside a dashboard the builder starts over the dashboard's dates, so
  // that start, not the saved spec, is what "unchanged" means there.
  const embedStart = useRef<S | null>(embed ? (o.embedSpec?.() ?? starter) : null);
  const baseline = useMemo(() => JSON.stringify(clean(embedStart.current ?? (current ? (current.spec as S) : starter))), [current, starter, clean]);
  const cleaned = useMemo(() => JSON.stringify(clean(spec)), [spec, clean]);
  const specDirty = cleaned !== baseline;
  const dirty = specDirty || (!!current && name.trim() !== current.name);

  // Follows the address: opens the insight it names, or a new one.
  useEffect(() => {
    if (embed) return;
    const byClick = clicked.current;
    clicked.current = false;
    if (!item) {
      setMissing(false);
      setLoading(false);
      if (shown.current === undefined) return; // already on a new one
      shown.current = undefined;
      setCurrent(null);
      setName("");
      setSpec((byClick ? null : urlDraft<S>()) ?? starter);
      return;
    }
    if (shown.current === item) return;
    let live = true;
    setLoading(true);
    api.GET("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: item } } }).then(({ data }) => {
      if (!live) return;
      setLoading(false);
      if (!data || data.kind !== kind) {
        shown.current = undefined;
        setCurrent(null);
        return setMissing(true);
      }
      shown.current = data.id;
      setMissing(false);
      setCurrent(data);
      setName(data.name);
      setSpec((byClick ? null : urlDraft<S>()) ?? (data.spec as S));
    });
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [brand, item]);

  // Keeps unsaved changes in the address, so a reload or a shared link
  // opens the same draft.
  useEffect(() => {
    if (embed || loading || missing) return;
    if ((item ?? undefined) !== shown.current) return; // the address is ahead of the page
    const t = setTimeout(() => {
      const enc = specDirty ? encodeDraft(JSON.parse(cleaned)) : null;
      writeDraft(enc && enc.length <= draftLimit ? enc : null);
    }, 300);
    return () => clearTimeout(t);
  }, [embed, loading, missing, item, specDirty, cleaned]);

  // The top bar's path shows the insight's name.
  useEffect(() => {
    if (!embed && current) nameItem(current.id, current.name);
  }, [embed, current]);

  // Warns before a reload or a closed tab loses changes to a saved insight.
  useEffect(() => {
    if (!current || !dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    addEventListener("beforeunload", warn);
    return () => removeEventListener("beforeunload", warn);
  }, [embed, current, dirty]);

  const go = useCallback(
    (id?: string) => {
      clicked.current = true;
      writeDraft(null);
      if (onOpen) return onOpen(id);
      // No address to follow (an embedded builder never gets here).
      clicked.current = false;
    },
    [onOpen],
  );

  async function open(id?: string) {
    if (id === (current?.id ?? undefined) && !missing) return;
    if (dirty && !(await confirmAction("Leave without saving?", current ? `Your changes to ${current.name} have not been saved.` : "What you built has not been saved.", "Leave"))) return;
    go(id);
  }

  async function close() {
    if (dirty && !(await confirmAction("Leave without saving?", current ? `Your changes to ${current.name} have not been saved.` : "What you built has not been saved.", "Leave"))) return;
    go(undefined);
  }

  function landed(data: Insight) {
    shown.current = data.id;
    setCurrent(data);
    setName(data.name);
    setSpec(data.spec as S);
    embed?.onSaved(data);
    toast("Saved");
    loadSaved();
    if (!embed && item !== data.id) go(data.id);
    else writeDraft(null);
  }

  async function save() {
    const body = clean(spec) as Insight["spec"];
    if (current) {
      const { data, error } = await api.PATCH("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } }, body: { name: name.trim(), spec: body } });
      if (!data) return toast(errorMessage(error), "error");
      return landed(data);
    }
    const { data, error } = await api.POST("/v1/brands/{brand}/insights", { params: { path: { brand } }, body: { kind, name: name.trim(), spec: body } });
    if (!data || !("id" in data)) return toast(errorMessage(error), "error");
    landed(data);
  }

  async function saveCopy() {
    if (!current) return;
    const copyName = (name.trim() && name.trim() !== current.name ? name.trim() : `${current.name} (copy)`).slice(0, 120);
    const { data, error } = await api.POST("/v1/brands/{brand}/insights", { params: { path: { brand } }, body: { kind, name: copyName, spec: clean(spec) as Insight["spec"] } });
    if (!data || !("id" in data)) return toast(errorMessage(error), "error");
    landed(data);
  }

  function cancel() {
    setSpec(embedStart.current ?? (current ? (current.spec as S) : starter));
    setName(current?.name ?? "");
    if (!embed) writeDraft(null);
  }

  async function remove(warning: string) {
    if (!current || !(await confirmAction(`Delete ${current.name}?`, warning, "Delete"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Deleted");
    loadSaved();
    if (embed || !onOpen) {
      shown.current = undefined;
      setCurrent(null);
      setName("");
      return;
    }
    go(undefined);
  }

  return { spec, setSpec, current, name, setName, saved, dirty, missing, loading, open, close, save, saveCopy, cancel, remove };
}

/**
 * The bar over a builder: what is open, whether it has unsaved changes,
 * and Save, Cancel, Save as a copy, Copy link and Delete.
 */
export function InsightBar<S>({ ed, what, all, canManage, canSave, placeholder, deleteWarning, embed }: { ed: InsightEditor<S>; what: string; /** The link back to the list, such as "All funnels". */ all: string; canManage: boolean; canSave: boolean; placeholder: string; deleteWarning: string; /** Inside a dashboard: no list to go back to, no address of its own to copy, and no Delete. */ embed?: boolean }) {
  const { current, dirty, name } = ed;
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-card px-3 py-2">
      {!embed && (
        <a
          href={itemHref()}
          onClick={(e) => {
            if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
            e.preventDefault();
            ed.close();
          }}
          className="shrink-0 whitespace-nowrap rounded px-1.5 py-1 text-[13px] text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          ← {all}
        </a>
      )}
      {canManage ? (
        <input aria-label={`Name of this ${what}`} className={`${field} min-w-[180px] flex-1`} placeholder={current ? "Name" : `Name this ${what} to save it, e.g. ${placeholder}`} value={name} onChange={(e) => ed.setName(e.target.value)} maxLength={120} />
      ) : (
        <span className="min-w-0 flex-1 truncate text-[14px] font-medium">{current?.name ?? `New ${what}`}</span>
      )}
      {current ? (
        dirty ? <Badge tone="amber" title="The saved version has not changed. Save to keep these changes, or Cancel to drop them.">Unsaved changes</Badge> : <Badge tone="green">Saved</Badge>
      ) : (
        <Badge title="Nothing is kept until you save it.">Not saved</Badge>
      )}
      {canManage && (
        <Button disabled={!name.trim() || !canSave || (!!current && !dirty)} onClick={ed.save}>{current ? "Save changes" : "Save"}</Button>
      )}
      {dirty && <Button variant="ghost" onClick={ed.cancel}>{current ? "Cancel" : "Start over"}</Button>}
      {canManage && current && <Button variant="ghost" disabled={!canSave} onClick={ed.saveCopy}>Save as a copy</Button>}
      {!embed && <CopyButton text={location.origin + itemHref(current?.id ?? newItem) + (dirty ? location.search : "")} label="Copy link" />}
      {!embed && canManage && current && (
        <Button variant="ghost" className="text-muted-foreground hover:text-destructive" onClick={() => ed.remove(deleteWarning)}>Delete</Button>
      )}
    </div>
  );
}

/** Shown when the address names an insight that is not there. */
export function InsightMissing<S>({ ed, what }: { ed: InsightEditor<S>; what: string }) {
  return (
    <Empty title={`This ${what} is not here`} action={<Button onClick={() => ed.open(undefined)}>Back to {what}s</Button>}>
      It may have been deleted, or the link belongs to another brand.
    </Empty>
  );
}

/** A step or series in a few words, for the list: its label, or what it counts. */
export function partName(p: { kind?: string; event?: string; conversion?: string; label?: string; name?: string } | undefined): string {
  if (!p) return "";
  const kinds: Record<string, string> = { visit: "Visit", page: "Page view", payment: "Payment", ad: "Ads", analytics: "Analytics", backlinks: "Backlinks", link_orders: "Link orders", search: "Search" };
  return p.label || p.event || p.conversion || p.name || (p.kind ? (kinds[p.kind] ?? p.kind) : "");
}

/**
 * The page's first screen: every saved insight of one kind in a table,
 * a search box once the list is long, and a button that starts a new one.
 * Each row is a real link, so it opens in a new tab too.
 */
export function InsightList({ brand, kind, what, plural, canManage, onOpen, column, describe, empty }: { brand: string; kind: Insight["kind"]; /** "funnel" */ what: string; /** "funnels" */ plural: string; canManage: boolean; onOpen?: (id?: string) => void; /** The heading of the column that says what each one holds, and its text. */ column: string; describe: (i: Insight) => string; /** What to say when there are none. */ empty: string }) {
  const [list, setList] = useState<Insight[] | null>(null);
  const [error, setError] = useState("");
  const [find, setFind] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/insights", { params: { path: { brand }, query: { kind } } }).then(({ data, error }) => (data ? setList(data.data) : setError(errorMessage(error))));
  }, [brand, kind]);
  const shown = useMemo(() => {
    const q = find.trim().toLowerCase();
    return (list ?? []).filter((i) => !q || i.name.toLowerCase().includes(q));
  }, [list, find]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!list) return <Loading />;
  const start = <Button onClick={() => onOpen?.(newItem)}>New {what}</Button>;
  if (list.length === 0) return <Empty title={`No ${plural} yet`} action={start}>{empty}</Empty>;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-3">
          {list.length > 8 && <input type="search" aria-label={`Search ${plural}`} className={`${field} w-64`} placeholder={`Search ${plural}`} value={find} onChange={(e) => setFind(e.target.value)} />}
          <span className="text-[13px] text-muted-foreground">
            {shown.length === list.length ? `${list.length} saved` : `${shown.length} of ${list.length} saved`}
          </span>
        </div>
        {start}
      </div>
      <div className="overflow-x-auto rounded-lg border bg-card">
        <table className="w-full text-[13px]">
          <thead>
            <tr className="border-b text-left text-[12px] text-muted-foreground">
              <th className="px-3 py-2 font-medium">Name</th>
              <th className="px-3 py-2 font-medium">{column}</th>
              <th className="px-3 py-2 font-medium whitespace-nowrap">Last changed</th>
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody>
            {shown.map((i) => (
              <tr key={i.id} className="border-b last:border-b-0 hover:bg-muted/50">
                <td className="max-w-[22rem] px-3 py-2">
                  <a
                    href={itemHref(i.id)}
                    onClick={(e) => {
                      if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
                      e.preventDefault();
                      onOpen?.(i.id);
                    }}
                    className="block truncate font-medium hover:underline"
                    title={i.name}
                  >
                    {i.name}
                  </a>
                </td>
                <td className="max-w-[28rem] truncate px-3 py-2 text-muted-foreground" title={describe(i)}>{describe(i)}</td>
                <td className="px-3 py-2 whitespace-nowrap text-muted-foreground" title={new Date(i.updated_at).toLocaleString()}>{ago(i.updated_at)}</td>
                <td className="px-3 py-1 text-right whitespace-nowrap">
                  <CopyButton text={location.origin + itemHref(i.id)} label="Copy link" />
                </td>
              </tr>
            ))}
            {shown.length === 0 && (
              <tr>
                <td colSpan={4} className="px-3 py-6 text-center text-muted-foreground">Nothing saved matches “{find.trim()}”.</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {!canManage && <p className="text-[12px] text-muted-foreground">You can build and look, but saving needs the permission to manage analytics.</p>}
    </div>
  );
}
