import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { ago, plural } from "../format";
import { displayName, fieldLabelValue, nameItem, namespaces, originPhrases, origins, type CatalogInfo, type Rec } from "@/lib/records";

// Duplicates: pairs of records that may be the same human because they
// share an email or phone. Merge them, link them as one human kept apart
// (two accounts in your product), or dismiss the pair.

type Pair = Schemas["RecordDuplicate"];

const statuses: Record<Pair["status"], { label: string; tone: "green" | "amber" | "grey" }> = {
  open: { label: "To review", tone: "amber" },
  blocked: { label: "Two accounts", tone: "amber" },
  merged: { label: "Merged", tone: "green" },
  linked: { label: "Linked", tone: "green" },
  dismissed: { label: "Not the same", tone: "grey" },
};

export function Duplicates({ ws, cat, canManage, tabs, onOpen }: { ws: string; cat: CatalogInfo; canManage: boolean; tabs: ReactNode; onOpen: (item: string) => void }) {
  const [pairs, setPairs] = useState<Pair[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [decided, setDecided] = useState(false);
  const [type, setType] = useState("");

  const load = useCallback(
    async (before?: string) => {
      const { data, error } = await api.GET("/v1/workspaces/{ws}/records/duplicates", {
        params: { path: { ws }, query: { limit: 50, ...(decided ? { status: "merged,linked,dismissed" } : {}), ...(type ? { type } : {}), ...(before ? { before } : {}) } },
      });
      if (!data) return setError(errorMessage(error));
      setError("");
      setPairs((prev) => (before ? [...(prev ?? []), ...data.data] : data.data));
      setNext(data.next_before ?? null);
    },
    [ws, decided, type],
  );
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => nameItem("duplicates", "Duplicates"), []);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Duplicates"
        description="People and companies that may be the same, because they share an email, phone or domain. Merge them into one record, link them when they are one person with two accounts in your product, or say they're not the same."
      />
      {tabs}
      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="Show" className={field} value={decided ? "decided" : "review"} onChange={(e) => setDecided(e.target.value === "decided")}>
          <option value="review">To review</option>
          <option value="decided">Decided</option>
        </select>
        <select aria-label="Type" className={field} value={type} onChange={(e) => setType(e.target.value)}>
          <option value="">People and companies</option>
          <option value="person">People</option>
          <option value="account">Companies</option>
        </select>
      </div>
      <ErrorText>{error}</ErrorText>
      {!pairs && !error && <Loading />}
      {pairs?.length === 0 && (
        <Empty title={decided ? "Nothing decided yet" : "No duplicates to review"}>
          {decided ? "Pairs you merge, link or dismiss show here." : "When two records share an email or phone, they show here for you to decide."}
        </Empty>
      )}
      {pairs && pairs.length > 0 && (
        <ul className="space-y-4">
          {pairs.map((p) => <PairCard key={p.id} ws={ws} pair={p} cat={cat} canManage={canManage} onDone={() => load()} onOpen={onOpen} />)}
        </ul>
      )}
      {next && <Button variant="ghost" onClick={() => load(next)}>Show more</Button>}
    </div>
  );
}

function PairCard({ ws, pair, cat, canManage, onDone, onOpen }: { ws: string; pair: Pair; cat: CatalogInfo; canManage: boolean; onDone: () => void; onOpen: (item: string) => void }) {
  const [busy, setBusy] = useState(false);
  const [a, b] = pair.records;
  if (!a || !b) return null;
  const ns = String(pair.evidence.namespace ?? pair.reason);
  const shared = a.identities.find((i) => i.namespace === ns && b.identities.some((j) => j.namespace === ns && j.value === i.value))?.value;
  const open = pair.status === "open" || pair.status === "blocked";
  // The fields either one has, the differing ones marked.
  const keys = cat.fields(pair.record_type).filter((f) => !emptyValue(a.attributes[f.key]) || !emptyValue(b.attributes[f.key]));

  async function merge(keep: Rec, from: Rec) {
    // Two records of the same name are told apart by where they came from.
    const twin = displayName(keep) === displayName(from);
    const sameOrigin = keep.origin === from.origin;
    const k = twin ? (sameOrigin ? "this record" : `the record ${originPhrases[keep.origin]}`) : displayName(keep);
    const f = twin ? (sameOrigin ? "the other" : `the one ${originPhrases[from.origin]}`) : displayName(from);
    const r = await ask({
      title: twin ? `${displayName(keep)}: keep ${k} and merge ${f} into it?` : `Keep ${k} and merge ${f} into it?`,
      body: (
        <>
          <p>The emails and ids, links, product events, consent, emails, replies, meetings and deals of {f} move to {k}.</p>
          <p>Its fields fill in what {k} doesn't have, or replace a value from a weaker source; a value you typed on {k} stays. {twin ? "The other" : f} is archived, and its old links lead here.</p>
        </>
      ),
      choices: [
        { label: "Cancel", value: "cancel", variant: "ghost" },
        { label: "Merge", value: "merge", variant: "primary" },
      ],
    });
    if (r?.choice !== "merge") return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/records/{record}/merge", {
      params: { path: { ws, record: keep.id } },
      body: { from: from.id, version: keep.version },
    });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    if (data && "moved" in data) {
      const moved = Object.values(data.moved).reduce((n, c) => n + c, 0);
      toast(`Merged into ${displayName(keep)}${moved ? `: ${plural(moved, "thing")} moved` : ""}.`);
    }
    onDone();
  }

  async function decide(how: "link" | "dismiss") {
    setBusy(true);
    const path = { params: { path: { ws, candidate: pair.id } } };
    const { error } = how === "link" ? await api.POST("/v1/workspaces/{ws}/records/duplicates/{candidate}/link", path) : await api.POST("/v1/workspaces/{ws}/records/duplicates/{candidate}/dismiss", path);
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(how === "link" ? "Linked as the same person, kept as two records." : "Marked as not the same. You won't be asked again.");
    onDone();
  }

  return (
    <li className="rounded-md border">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2.5 text-sm">
        <span>
          Both have {(namespaces[ns] ?? ns).toLowerCase()} {shared ? <span className="font-mono text-[12px]">{shared}</span> : null}
          {pair.evidence.verified ? <span className="text-muted-foreground"> (verified on one)</span> : null}
        </span>
        <span className="flex items-center gap-2 text-xs text-muted-foreground">
          <Badge tone={statuses[pair.status].tone}>{statuses[pair.status].label}</Badge>
          {pair.decided_at ? `decided ${ago(pair.decided_at)}` : `found ${ago(pair.created_at)}`}
        </span>
      </div>
      <div className="grid gap-px bg-border sm:grid-cols-2">
        {[a, b].map((r, i) => {
          const other = i === 0 ? b : a;
          return (
            <div key={r.id} className="space-y-2 bg-background p-4 text-sm">
              <div className="flex items-start justify-between gap-2">
                <button className="text-left font-medium underline" onClick={() => onOpen(r.id)}>{displayName(r)}</button>
                <span className="shrink-0 text-xs text-muted-foreground">{origins[r.origin]} · added {ago(r.created_at)}</span>
              </div>
              <div className="space-y-0.5">
                {r.identities.map((id) => (
                  <div key={id.id} className="break-all font-mono text-[12px]">
                    {id.value} <span className="font-sans text-muted-foreground">{namespaces[id.namespace] ?? id.namespace}{id.verified ? ", verified" : ""}</span>
                  </div>
                ))}
              </div>
              {keys.length > 0 && (
                <dl className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-x-3 gap-y-1">
                  {keys.map((f) => {
                    const v = fieldLabelValue(cat, pair.record_type, f.key, r.attributes[f.key]);
                    const differs = !emptyValue(r.attributes[f.key]) && !emptyValue(other.attributes[f.key]) && JSON.stringify(r.attributes[f.key]) !== JSON.stringify(other.attributes[f.key]);
                    return (
                      <div key={f.key} className="contents">
                        <dt className="truncate text-muted-foreground">{f.label}</dt>
                        <dd className={differs ? "font-medium text-amber-600 dark:text-amber-400" : ""} title={differs ? "The two records disagree" : undefined}>{v || "–"}</dd>
                      </div>
                    );
                  })}
                </dl>
              )}
              {canManage && open && pair.mergeable && (
                <Button variant="ghost" disabled={busy} onClick={() => merge(r, other)}>Keep this one</Button>
              )}
            </div>
          );
        })}
      </div>
      {canManage && open && (
        <div className="flex flex-wrap items-center gap-2 border-t px-4 py-2.5">
          {!pair.mergeable && <span className="mr-auto text-xs text-muted-foreground">Both have their own user id: two accounts in your product, so they can be linked but not merged.</span>}
          <Button variant="ghost" disabled={busy} onClick={() => decide("link")} title="The same human, kept as two records, such as one person with two accounts">Same person, keep both</Button>
          <Button variant="ghost" disabled={busy} onClick={() => decide("dismiss")}>Not the same</Button>
        </div>
      )}
    </li>
  );
}

const emptyValue = (v: unknown) => v === null || v === undefined || v === "" || (Array.isArray(v) && v.length === 0);
