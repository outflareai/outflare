import { useCallback, useEffect, useState } from "react";
import type { AuditEvent } from "@outflare/sdk";
import { api } from "../api";
import { useBrand } from "@/lib/brand";
import { Badge, Button, CopyButton, Empty, field, Loading, PageHeader } from "../ui";
import { Search } from "lucide-react";
import { ago, stamp } from "../format";
import { DataTable } from "@/components/data-table";

// Plain-language names for audit actions. Unknown ones fall back to the code.
const actions: Record<string, string> = {
  "workspace.created": "Created the workspace",
  "user.login": "Signed in",
  "member.role_changed": "Changed a member's role",
  "member.removed": "Removed a member",
  "invite.created": "Invited someone",
  "invite.accepted": "Joined from an invite",
  "invite.revoked": "Cancelled an invite",
  "token.created": "Created an API token",
  "token.revoked": "Revoked an API token",
  "mailbox.added": "Added a mailbox",
  "mailboxes.imported": "Imported mailboxes",
  "mailbox.connected": "Connected a mailbox",
  "mailbox.updated": "Changed a mailbox",
  "mailbox.paused": "Paused a mailbox",
  "mailbox.resumed": "Resumed a mailbox",
  "mailbox.verified": "Mailbox sign-in worked",
  "mailbox.verify_failed": "Mailbox sign-in failed",
  "mailbox.deleted": "Removed a mailbox",
  "mailbox.test_sent": "Sent a test email",
  "leads.imported": "Imported leads",
  "lead.created": "Added a lead",
  "lead.updated": "Changed a lead",
  "lead.deleted": "Deleted a lead",
  "lead.replied": "A lead replied",
  "lead.bounced": "An email bounced",
  "lead.unsubscribed": "A lead unsubscribed",
  "suppressions.added": "Added to the suppression list",
  "suppression.removed": "Removed from the suppression list",
  "sequence.created": "Created a sequence",
  "sequence.updated": "Changed a sequence",
  "sequence.steps_updated": "Edited a sequence's steps",
  "sequence.activated": "Started a sequence",
  "sequence.paused": "Paused a sequence",
  "sequence.archived": "Archived a sequence",
  "sequence.deleted": "Deleted a sequence",
  "sequence.leads_enrolled": "Added leads to a sequence",
  "enrollment.stopped": "Stopped emailing a lead",
  "inbox.replied": "Replied to a lead",
  "oauth.connection_created": "Added a Google or Microsoft connection",
  "oauth.connection_deleted": "Removed a Google or Microsoft connection",
  "domain.added": "Added a sending domain",
  "domain.updated": "Changed a sending domain",
  "domain.paused": "Paused a sending domain",
  "domain.resumed": "Resumed a sending domain",
  "domain.removed": "Removed a sending domain",
  "mission.created": "Created a mission",
  "mission.updated": "Changed a mission",
  "mission.submitted": "Sent a mission plan for approval",
  "mission.approved": "Approved a mission",
  "mission.paused": "Paused a mission",
  "mission.resumed": "Resumed a mission",
  "mission.stopped": "Stopped a mission",
  "mission.completed": "Completed a mission",
  "mission.action_simulated": "Made a dry-run change in a mission",
  "mission.action_held": "Asked to approve a change in a mission",
  "mission.action_approved": "Approved a mission's change",
  "mission.action_rejected": "Rejected a mission's change",
  "mission.connect_request_resolved": "Resolved a mission's connect request",
};

function details(e: AuditEvent): string {
  const d = (e.data ?? {}) as Record<string, unknown>;
  const parts = ["domain", "email", "name", "role", "count", "created", "added", "enrolled", "status"]
    .filter((k) => d[k] !== undefined && d[k] !== "" && typeof d[k] !== "object")
    .map((k) => `${k.replaceAll("_", " ")}: ${d[k]}`);
  return parts.join(" · ");
}

export function Audit({ ws }: { ws: string }) {
  const [events, setEvents] = useState<AuditEvent[] | null>(null);
  const [next, setNext] = useState<number | null>(null);
  const [q, setQ] = useState("");

  const { query: brandQuery } = useBrand(ws);
  const load = useCallback(
    async (before?: number) => {
      const { data } = await api.GET("/v1/workspaces/{ws}/audit-events", { params: { path: { ws }, query: { before, ...brandQuery } } });
      setEvents((prev) => (before ? [...(prev ?? []), ...(data?.data ?? [])] : data?.data ?? []));
      setNext(data?.next_before ?? null);
    },
    [ws, brandQuery.brand], // eslint-disable-line react-hooks/exhaustive-deps
  );

  useEffect(() => {
    load();
  }, [load]);

  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  const shown = events?.filter((e) => {
    const text = `${actor(e)} ${e.action} ${actions[e.action!] ?? ""} ${details(e)} ${e.target_id ?? ""}`.toLowerCase();
    return words.every((w) => text.includes(w));
  });

  return (
    <div className="space-y-6">
      <PageHeader title="Audit log" description="Every change in this workspace, who made it (a person or an API token) and when. Kept permanently." />
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <label className="relative min-w-0 flex-1 basis-56">
            <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <input className={`${field} w-full pl-8`} type="search" placeholder="Filter by actor, action or ID" aria-label="Filter events" value={q} onChange={(e) => setQ(e.target.value)} />
          </label>
          {events && <span className="font-mono text-[12px] text-muted-foreground tabular-nums">{events.length.toLocaleString()} loaded, newest first</span>}
        </div>
        {events === null && <Loading />}
        {events?.length === 0 && <Empty title="Nothing yet">Changes appear here as soon as anyone, or any API token, makes one.</Empty>}
        {shown && shown.length === 0 && events!.length > 0 && <p className="py-6 text-center text-muted-foreground">No loaded events match "{q}".{next && " Load older ones below to search further back."}</p>}
        {shown && shown.length > 0 && (
          <DataTable
            data={shown}
            rowId={(e) => String(e.id)}
            columnsMenu={false}
            columns={[
              { id: "time", header: "Time", accessorFn: (e) => e.created_at, meta: { mono: true }, cell: ({ row: { original: e } }) => <span className="whitespace-nowrap text-muted-foreground" title={stamp(e.created_at!)}>{ago(e.created_at)}</span> },
              {
                id: "actor",
                header: "Actor",
                accessorFn: (e) => actor(e),
                cell: ({ row: { original: e } }) => (
                  <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
                    {actor(e)}
                    {e.actor_type && e.actor_type !== "human" && <Badge tone={e.actor_type === "system" ? "grey" : "blue"}>{e.actor_type === "api_token" ? "token" : e.actor_type.replace("_", " ")}</Badge>}
                  </span>
                ),
              },
              {
                id: "action",
                header: "Action",
                accessorFn: (e) => e.action,
                cell: ({ row: { original: e } }) => (
                  <>
                    <div>{actions[e.action!] ?? e.action}</div>
                    <div className="font-mono text-[11px] text-muted-foreground">{e.action}</div>
                  </>
                ),
              },
              {
                id: "details",
                header: "Details",
                enableSorting: false,
                cell: ({ row: { original: e } }) => (
                  <details className="group max-w-96">
                    <summary className="cursor-pointer list-none truncate text-[12px] text-muted-foreground group-open:text-foreground">
                      {details(e) || <span className="font-mono">{e.target_id ?? "{…}"}</span>}
                    </summary>
                    <dl className="mt-2 space-y-1 font-mono text-[11px]">
                      {e.target_type && <Row k="target" v={`${e.target_type} ${e.target_id ?? ""}`.trim()} />}
                      {e.request_id && <Row k="request" v={e.request_id} />}
                      <Row k="event" v={String(e.id)} />
                    </dl>
                    <pre className="mt-2 overflow-x-auto rounded border bg-muted/40 p-2 text-[11px]"><code>{JSON.stringify(e.data ?? {}, null, 2)}</code></pre>
                  </details>
                ),
              },
            ]}
          />
        )}
        {next && <Button variant="ghost" onClick={() => load(next)}>Load older</Button>}
      </div>
    </div>
  );
}

function actor(e: AuditEvent) {
  return e.actor_name ?? (e.actor_type === "system" ? "Outflare" : e.actor_type ?? "");
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex items-center gap-2">
      <dt className="w-14 text-muted-foreground">{k}</dt>
      <dd className="flex min-w-0 items-center break-all">{v}<CopyButton text={v} label={`Copy ${k}`} /></dd>
    </div>
  );
}
