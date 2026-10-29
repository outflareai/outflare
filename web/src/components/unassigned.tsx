// Records in no brand. With one brand picked, every page shows only that
// brand's records, so these show nowhere but under All brands. This card
// lists them by kind and puts the ones ticked into a brand in one go. A
// record in a project follows its project, so only the project is listed.

import { useCallback, useEffect, useState } from "react";
import type { Brand } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, Loading, field, toast } from "../ui";
import { plural } from "../format";
import { Checkbox } from "@/components/ui/checkbox";

type Kind = "project_ids" | "domains" | "mailbox_ids" | "lead_list_ids" | "sequence_ids";
type Row = { key: string; name: string; sub?: string };

const kinds: { kind: Kind; label: string; one: string; many: string }[] = [
  { kind: "project_ids", label: "Projects (being removed)", one: "project", many: "projects" },
  { kind: "domains", label: "Sending domains", one: "domain", many: "domains" },
  { kind: "mailbox_ids", label: "Mailboxes", one: "mailbox", many: "mailboxes" },
  { kind: "lead_list_ids", label: "Lead lists", one: "lead list", many: "lead lists" },
  { kind: "sequence_ids", label: "Sequences", one: "sequence", many: "sequences" },
];

export function NotInABrand({ ws, brands, canEdit, onMoved }: { ws: string; brands: Brand[]; canEdit: boolean; onMoved?: () => void }) {
  const [rows, setRows] = useState<Record<Kind, Row[]> | null>(null);
  const [picked, setPicked] = useState<Record<Kind, string[]>>({ project_ids: [], domains: [], mailbox_ids: [], lead_list_ids: [], sequence_ids: [] });
  const [open, setOpen] = useState<Kind | null>(null);
  const [to, setTo] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const none = { params: { path: { ws }, query: { brand: "none" } } };
    const [p, d, m, l, s] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/projects", none),
      api.GET("/v1/workspaces/{ws}/sending-domains", none),
      api.GET("/v1/workspaces/{ws}/mailboxes", none),
      api.GET("/v1/workspaces/{ws}/lead-lists", none),
      api.GET("/v1/workspaces/{ws}/sequences", none),
    ]);
    // Records in a project follow it; moving the project moves them.
    const loose = <T extends { project_id?: string | null }>(x: T[] | undefined) => (x ?? []).filter((r) => !r.project_id);
    setRows({
      project_ids: (p.data?.data ?? []).filter((x) => x.status !== "archived").map((x) => ({ key: x.id, name: x.name })),
      domains: loose(d.data?.data).map((x) => ({ key: x.domain, name: x.domain })),
      mailbox_ids: loose(m.data?.data).filter((x) => x.lifecycle.stage !== "retired").map((x) => ({ key: x.id, name: x.email, sub: x.domain })),
      lead_list_ids: loose(l.data?.data).map((x) => ({ key: x.id, name: x.name })),
      sequence_ids: loose(s.data?.data).filter((x) => x.status !== "archived").map((x) => ({ key: x.id, name: x.name })),
    });
    setPicked({ project_ids: [], domains: [], mailbox_ids: [], lead_list_ids: [], sequence_ids: [] });
  }, [ws]);
  useEffect(() => void load(), [load]);

  if (!rows) return <Card title="Not in a brand"><Loading /></Card>;
  const total = kinds.reduce((n, k) => n + rows[k.kind].length, 0);
  if (total === 0) return null;
  const chosen = kinds.reduce((n, k) => n + picked[k.kind].length, 0);
  const toggle = (kind: Kind, key: string, on: boolean) => setPicked((p) => ({ ...p, [kind]: on ? [...p[kind], key] : p[kind].filter((k) => k !== key) }));
  const all = (kind: Kind, on: boolean) => setPicked((p) => ({ ...p, [kind]: on ? rows[kind].map((r) => r.key) : [] }));

  async function move() {
    const brand = brands.find((b) => b.id === to);
    if (!brand) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/brands/{brand}/add", { params: { path: { ws, brand: brand.id } }, body: picked });
    setBusy(false);
    if (error || !data || !("moved" in data)) return toast(errorMessage(error), "error");
    toast(`Moved ${plural(chosen, "record", "records")} to ${brand.name}.`);
    await load();
    onMoved?.();
  }

  return (
    <Card
      title={`Not in a brand · ${total}`}
      description="These count for no brand, so they don't show when one brand is picked in the sidebar. Tick what belongs to a brand and move it there. A domain brings its mailboxes. Projects are being removed; until then, a project's domains, mailboxes, lists and sequences move with it."
    >
      <div className="space-y-2">
        {kinds.filter((k) => rows[k.kind].length > 0).map((k) => {
          const list = rows[k.kind];
          const on = picked[k.kind];
          const expanded = open === k.kind;
          return (
            <section key={k.kind} className="rounded-lg border">
              <div className="flex min-h-11 items-center gap-3 px-3 py-2">
                {canEdit && <Checkbox aria-label={`All ${k.many}`} checked={on.length === list.length ? true : on.length > 0 ? "indeterminate" : false} onCheckedChange={(v) => all(k.kind, v === true)} />}
                <button type="button" className="flex flex-1 items-center justify-between gap-2 text-left text-sm" aria-expanded={expanded} onClick={() => setOpen(expanded ? null : k.kind)}>
                  <span><span className="font-medium">{k.label}</span> <span className="text-muted-foreground">· {list.length}{on.length ? `, ${on.length} ticked` : ""}</span></span>
                  <span className="text-xs text-muted-foreground">{expanded ? "Hide" : "Show"}</span>
                </button>
              </div>
              {expanded && (
                <ul className="max-h-72 divide-y divide-border overflow-y-auto border-t">
                  {list.map((r) => (
                    <li key={r.key} className="flex min-h-10 items-center gap-3 px-3 py-1.5 text-sm">
                      {canEdit && <Checkbox aria-label={`Tick ${r.name}`} checked={on.includes(r.key)} onCheckedChange={(v) => toggle(k.kind, r.key, v === true)} />}
                      <span className="min-w-0 flex-1 truncate">{r.name}</span>
                      {r.sub && <span className="truncate font-mono text-xs text-muted-foreground">{r.sub}</span>}
                    </li>
                  ))}
                </ul>
              )}
            </section>
          );
        })}
        {canEdit && (
          <div className="flex flex-wrap items-center gap-2 pt-1">
            <select aria-label="Brand to move them to" className={`${field} w-48`} value={to} onChange={(e) => setTo(e.target.value)}>
              <option value="">Choose a brand…</option>
              {brands.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
            </select>
            <Button onClick={move} disabled={busy || !to || chosen === 0}>{busy ? "Moving…" : chosen ? `Move ${plural(chosen, "record", "records")}` : "Tick what to move"}</Button>
          </div>
        )}
      </div>
    </Card>
  );
}
