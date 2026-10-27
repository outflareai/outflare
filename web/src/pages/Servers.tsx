import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import type { Mailbox, SendingServer, ServerIssue } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, CopyButton, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago, plural, replyRate } from "../format";
import type { Tab } from "../App";
import { DataTable } from "@/components/data-table";
import { health as sharedHealth, mailboxStatus } from "@/lib/status";

type Health = SendingServer["health"];

type Tone = keyof typeof tones;

// Sort order for the Health column: worst first when descending.
const healthRank: Record<Health, number> = { healthy: 0, pending: 1, warning: 2, critical: 3 };

const health: Record<Health, { label: string; tone: Tone }> = sharedHealth;

const severity: Record<ServerIssue["severity"], { label: string; box: string }> = {
  critical: { label: "Fix now", box: "border-red-300 bg-red-50 dark:border-red-900 dark:bg-red-950/40" },
  warning: { label: "Should fix", box: "border-amber-300 bg-amber-50 dark:border-amber-900 dark:bg-amber-950/40" },
  info: { label: "Good to know", box: "border-border bg-muted/40" },
};

// Where to ask a blocklist to remove an IP.
function delistURL(zone: string) {
  if (zone.includes("spamhaus")) return "https://check.spamhaus.org";
  if (zone.includes("spamcop")) return "https://www.spamcop.net/bl.shtml";
  return "https://www.barracudacentral.org/rbl/removal-request";
}

// "3 · 1.5%" for counts against sends, or just the count with no sends.
function rate(n: number, of: number) {
  if (of === 0 || n > of) return n === 0 ? "–" : String(n);
  return `${n} · ${Math.round((n / of) * 1000) / 10}%`;
}

const listedIPs = (s: SendingServer) => new Set(s.blocklists.filter((b) => b.status === "listed").map((b) => b.ip)).size;

function pausedLabel(s: SendingServer) {
  return s.paused_by === "blocklist" ? "Paused: IP blocklisted" : "Paused";
}

type Props = { ws: string; canManage: boolean; open: string; onOpen: (id: string) => void; go: (tab: Tab, item?: string) => void };

export function Servers(props: Props) {
  return props.open ? <Detail key={props.open} {...props} /> : <List {...props} />;
}

// ---- All servers ----

type Filter = "all" | "attention" | "healthy" | "paused";

function List({ ws, canManage, onOpen, go }: Props) {
  const [servers, setServers] = useState<SendingServer[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/sending-servers", { params: { path: { ws } } });
    if (data) setServers(data.data);
    setLoaded(true);
  }, [ws]);

  useEffect(() => {
    load();
  }, [load]);

  // Checks run in the background; refresh until they settle.
  const pending = servers.some((s) => s.health === "pending");
  useEffect(() => {
    if (!pending) return;
    const t = setInterval(load, 2000);
    return () => clearInterval(t);
  }, [pending, load]);

  const count = (f: (s: SendingServer) => boolean) => servers.filter(f).length;
  const attention = (s: SendingServer) => s.health === "warning" || s.health === "critical";
  const shown = servers.filter((s) => {
    switch (filter) {
      case "attention": return attention(s);
      case "healthy": return s.health === "healthy";
      case "paused": return s.status === "paused";
      default: return true;
    }
  });

  async function checkAll() {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/sending-servers/check", { params: { path: { ws } } });
    if (error) toast(errorMessage(error), "error");
    else toast(`Re-checking ${plural(data?.queued ?? 0, "server")}. Results appear in a few seconds.`);
    setTimeout(load, 1500);
  }

  if (!loaded) return <Loading />;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Sending servers"
        description="Mailboxes on your own mail server or a cold-email SMTP vendor, grouped by the SMTP host they send through. They share its IP addresses, so a blocklisting hits all of them at once. Every IP is checked against Spamhaus, SpamCop and Barracuda each day."
        action={canManage && servers.length > 0 && <Button variant="ghost" onClick={checkAll}>Re-check all</Button>}
      />

      {servers.length > 0 && (
        <Stats cols={6}>
          <Stat label="Servers" value={servers.length} />
          <Stat label="Healthy" value={count((s) => s.health === "healthy")} tone={count((s) => s.health === "healthy") > 0 ? "green" : undefined} />
          <Stat label="Need attention" value={count(attention)} tone={count((x) => x.health === "critical") > 0 ? "red" : count(attention) > 0 ? "amber" : undefined} />
          <Stat label="Paused" value={count((s) => s.status === "paused")} tone={count((s) => s.status === "paused") > 0 ? "amber" : undefined} />
          <Stat label="Capacity / day" value={servers.reduce((n, s) => n + s.daily_capacity, 0).toLocaleString()} />
          <Stat label="Sent · 24h" value={servers.reduce((n, s) => n + s.sent_24h, 0).toLocaleString()} />
        </Stats>
      )}

      {servers.length === 0 && (
        <Empty title="No sending servers" action={<Button onClick={() => go("mailboxes")}>Connect mailboxes</Button>}>
          A server appears here when you connect a mailbox that signs in to your own SMTP server or a cold-email SMTP vendor, such as Mailreef or Infraforge. Google Workspace and Microsoft 365 mailboxes are not listed: their IPs belong to Google and Microsoft.
        </Empty>
      )}

      {servers.length > 0 && (
        <DataTable
          data={shown}
          rowId={(s) => s.id}
          onRowClick={(s) => onOpen(s.id)}
          searchPlaceholder="Search servers, IPs or domains"
          initialSort={[{ id: "health", desc: true }]}
          empty={<>No servers match. <button className="underline" onClick={() => setFilter("all")}>Show all</button></>}
          toolbar={
            <select className={field} aria-label="Show" value={filter} onChange={(e) => setFilter(e.target.value as Filter)}>
              <option value="all">All servers ({servers.length})</option>
              <option value="attention">Need attention ({count(attention)})</option>
              <option value="healthy">Healthy ({count((s) => s.health === "healthy")})</option>
              <option value="paused">Paused ({count((s) => s.status === "paused")})</option>
            </select>
          }
          mobile={(s) => (
            <div className="space-y-1.5">
              <div className="flex items-start justify-between gap-2">
                <span className="min-w-0">
                  <span className="block font-medium break-all">{s.name || <span className="font-mono">{s.host}</span>}</span>
                  {s.name && <span className="block font-mono text-xs break-all text-muted-foreground">{s.host}</span>}
                </span>
                <Badge tone={health[s.health].tone}>{health[s.health].label}</Badge>
              </div>
              {s.status === "paused" && <Badge>{pausedLabel(s)}</Badge>}
              <div className="text-xs text-muted-foreground">
                {plural(s.ips.length, "IP")}{listedIPs(s) > 0 && <span className="text-red-600 dark:text-red-400"> · {listedIPs(s)} listed</span>} · {s.mailbox_counts.active} of {s.mailboxes} mailboxes active{s.status === "active" && <> · {s.sent_24h}/{s.daily_capacity} sent today</>}{s.sent_7d > 0 && <> · bounces {rate(s.bounces_7d, s.sent_7d)}</>}
              </div>
            </div>
          )}
          columns={[
            {
              id: "server",
              header: "Server",
              // Search matches the name, host, IPs and domains.
              accessorFn: (s) => [s.name, s.host, ...s.ips, ...s.domains].join(" "),
              sortingFn: (a, b) => (a.original.name || a.original.host).localeCompare(b.original.name || b.original.host),
              enableHiding: false,
              cell: ({ row: { original: s } }) => (
                <>
                  <span className={`font-medium group-hover:underline ${s.name ? "" : "font-mono text-[13px]"}`}>{s.name || s.host}</span>
                  <div className="text-xs text-muted-foreground">
                    {s.name && <span className="font-mono">{s.host} · </span>}
                    {s.status === "paused" ? <span className="font-medium text-amber-700 dark:text-amber-400">{pausedLabel(s)}</span> : s.domains.length > 0 ? plural(s.domains.length, "domain") : "No mailboxes"}
                    {s.daily_limit !== null && <> · cap {s.daily_limit.toLocaleString()}/day</>}
                  </div>
                </>
              ),
            },
            {
              id: "health",
              header: "Health",
              accessorFn: (s) => healthRank[s.health],
              cell: ({ row: { original: s } }) => <Badge tone={health[s.health].tone} title={s.issues.filter((i) => i.severity !== "info").map((i) => i.title).join("\n") || undefined}>{health[s.health].label}</Badge>,
            },
            {
              id: "ips",
              header: "IPs",
              accessorFn: (s) => listedIPs(s) * 1000 + s.ips.length,
              meta: { align: "right", mono: true },
              cell: ({ row: { original: s } }) => (
                <>
                  {s.ips.length === 0 && s.checked_at ? <span className="text-amber-700 dark:text-amber-400">none</span> : s.ips.length}
                  {listedIPs(s) > 0 && <div className="text-xs text-red-600 dark:text-red-400">{listedIPs(s)} listed</div>}
                  {s.ips_source === "manual" && <div className="font-sans text-xs text-muted-foreground">set by hand</div>}
                </>
              ),
            },
            {
              id: "mailboxes",
              header: "Mailboxes",
              accessorFn: (s) => s.mailboxes,
              meta: { align: "right", mono: true },
              cell: ({ row: { original: s } }) => (
                <>
                  {s.mailbox_counts.active}<span className="text-muted-foreground">/{s.mailboxes}</span>
                  {s.mailbox_counts.error > 0 && <div className="text-xs text-red-600 dark:text-red-400">{s.mailbox_counts.error} failing</div>}
                </>
              ),
            },
            { id: "sent", header: "Sent 24h", accessorFn: (s) => s.sent_24h, meta: { align: "right", mono: true }, cell: ({ row: { original: s } }) => s.status === "paused" ? <>{s.sent_24h}<div className="font-sans text-xs text-muted-foreground">paused</div></> : <>{s.sent_24h}<span className="text-muted-foreground">/{s.daily_capacity}</span></> },
            { id: "bounces", header: "Bounces 7d", accessorFn: (s) => (s.sent_7d ? s.bounces_7d / s.sent_7d : 0), meta: { align: "right", mono: true }, cell: ({ row: { original: s } }) => <span className={s.issues.some((i) => i.code === "bounce_rate") ? "text-red-600 dark:text-red-400" : ""}>{rate(s.bounces_7d, s.sent_7d)}</span> },
            { id: "replies", header: "Replies 7d", accessorFn: (s) => s.replies_7d, meta: { align: "right", mono: true }, cell: ({ row: { original: s } }) => rate(s.replies_7d, s.sent_7d) },
          ]}
        />
      )}
    </div>
  );
}

// ---- One server ----

function Detail({ ws, canManage, open, onOpen, go }: Props) {
  const [s, setS] = useState<SendingServer | null>(null);
  const [missing, setMissing] = useState(false);
  const [mailboxes, setMailboxes] = useState<Mailbox[]>([]);
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState("");
  const checkedBefore = useRef<string | null>(null);
  const path = { params: { path: { ws, id: open } } };

  const load = useCallback(async () => {
    const [ss, mm] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/sending-servers/{id}", { params: { path: { ws, id: open } } }),
      api.GET("/v1/workspaces/{ws}/mailboxes", { params: { path: { ws }, query: { server_id: open } } }),
    ]);
    if (ss.response.status === 404 || ss.response.status === 400) return setMissing(true);
    if (ss.data) setS(ss.data);
    setMailboxes(mm.data?.data ?? []);
  }, [ws, open]);

  useEffect(() => {
    load();
  }, [load]);

  // Refresh while the first check, a re-check or mailbox checks are running.
  const waiting = checking || s?.checked_at === null || mailboxes.some((m) => m.status === "verifying");
  useEffect(() => {
    if (!waiting) return;
    const t = setInterval(load, 2000);
    return () => clearInterval(t);
  }, [waiting, load]);
  useEffect(() => {
    if (checking && s && s.checked_at !== checkedBefore.current) setChecking(false);
  }, [checking, s]);
  useEffect(() => {
    if (!checking) return;
    const t = setTimeout(() => setChecking(false), 30000);
    return () => clearTimeout(t);
  }, [checking]);

  if (missing) {
    return (
      <div>
        <PageHeader title="Server not found" back={{ label: "All servers", onClick: () => onOpen("") }} />
        <Empty title="Server not found">There is no such server in this workspace. It may have been removed.</Empty>
      </div>
    );
  }
  if (!s) return <Loading />;

  async function patch(body: Record<string, unknown>) {
    // New IPs are checked right away; refresh until the results are in.
    if ("ips" in body) checkedBefore.current = s!.checked_at;
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/sending-servers/{id}", { ...path, body });
    if (error) {
      setError(errorMessage(error));
      return false;
    }
    setError("");
    setS(data);
    if ("ips" in body) setChecking(true);
    load();
    return true;
  }

  async function recheck() {
    checkedBefore.current = s!.checked_at;
    setChecking(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/sending-servers/{id}/check", path);
    if (error) {
      setChecking(false);
      toast(errorMessage(error), "error");
    }
  }

  const label = s.name || s.host;

  async function togglePause() {
    const pausing = s!.status === "active";
    if (pausing && !(await confirmAction(`Pause all sending through ${label}?`, <p>{plural(s!.mailboxes, "mailbox", "mailboxes")} on this server stop sending sequence and test emails until you resume. Leads wait where they are, and replies you write in the inbox still go. A pause you make here is never lifted by a blocklist check.</p>, "Pause server"))) return;
    if (await patch({ status: pausing ? "paused" : "active" })) toast(pausing ? `Paused ${label}. Nothing sends through it until you resume.` : `Resumed ${label}.`);
  }

  async function remove() {
    if (!(await confirmAction(`Remove ${label}?`, <p>It leaves the Servers list with its settings and notes. It comes back on its own if you connect a mailbox on {s!.host} again.</p>, "Remove server"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/sending-servers/{id}", path);
    if (error) return toast(errorMessage(error), "error");
    toast(`Removed ${label}.`);
    onOpen("");
  }

  const problems = s.issues.filter((i) => i.severity !== "info");
  const notes = s.issues.filter((i) => i.severity === "info");
  const bounceRate = s.sent_7d > 0 ? s.bounces_7d / s.sent_7d : 0;

  return (
    <div className="space-y-6">
      <div>
        <PageHeader
          back={{ label: "All servers", onClick: () => onOpen("") }}
          title={<><span className={s.name ? "break-all" : "break-all font-mono"}>{label}</span><Badge tone={health[s.health].tone}>{health[s.health].label}</Badge>{s.status === "paused" && <Badge>{pausedLabel(s)}</Badge>}</>}
          description={<>{s.name && <span className="font-mono">{s.host} · </span>}{s.ips.length === 0 ? (s.ips_source === "manual" ? "No IPs set" : "No IPs found in DNS") : `${plural(s.ips.length, "IP")} ${s.ips_source === "manual" ? "set by hand" : "from DNS"}`} · checked {checking ? "now…" : s.checked_at ? ago(s.checked_at) : "not yet"}</>}
          action={canManage && (
            <>
              <Button variant="ghost" onClick={recheck} disabled={checking}>{checking ? "Checking…" : "Re-check IPs"}</Button>
              <Button variant={s.status === "active" ? "ghost" : "primary"} onClick={togglePause}>{s.status === "active" ? "Pause server" : "Resume sending"}</Button>
            </>
          )}
        />
        <ErrorText>{error}</ErrorText>
      </div>

      <Card title={problems.length ? `What to fix (${problems.length})` : "What to fix"}>
        {s.health === "pending" && <p className="text-sm text-muted-foreground">Looking up the server's IPs and checking them against blocklists. This takes a few seconds.</p>}
        {s.health !== "pending" && problems.length === 0 && (
          <p className="text-sm text-green-700 dark:text-green-400">Nothing to fix. None of the server's IPs is on the blocklists checked.</p>
        )}
        <ul className="space-y-3">
          {[...problems, ...notes].map((i) => <IssueRow key={i.code + i.title} issue={i} />)}
        </ul>
      </Card>

      <Stats cols={5}>
        <Stat label="Sent · 24h" value={s.sent_24h} sub={s.status === "paused" ? "paused, nothing sends" : `of ${s.daily_capacity} daily capacity`} />
        <Stat label="Sent · 7d" value={s.sent_7d} />
        <Stat label="Replies · 7d" value={s.replies_7d} sub={replyRate(s.replied_leads_7d, s.contacted_leads_7d) && `${replyRate(s.replied_leads_7d, s.contacted_leads_7d)} reply rate`} />
        <Stat label="Bounces · 7d" value={s.bounces_7d} sub={s.sent_7d ? `${pct(bounceRate)} bounce rate${bounceRate > 0.02 ? ", keep under 2%" : ""}` : undefined} tone={bounceRate > 0.02 ? "red" : undefined} />
        <Stat label="Domains" value={s.domains.length} sub={s.domains.length ? undefined : "no mailboxes yet"} />
      </Stats>

      <Card title="IP addresses and blocklists">
        <Blocklists s={s} />
      </Card>

      <Settings s={s} canManage={canManage} onSave={patch} />

      <Card title={`Mailboxes on this server (${mailboxes.length})`} action={<Button variant="ghost" onClick={() => go("mailboxes")}>All mailboxes</Button>}>
        {s.domains.length > 0 && (
          <div className="mb-3 flex flex-wrap items-center gap-1.5 text-sm">
            <span className="text-muted-foreground">Domains:</span>
            {s.domains.map((d) => (
              <button key={d} className="rounded border px-1.5 py-0.5 font-mono text-xs hover:bg-accent" onClick={() => go("domains", d)}>{d}</button>
            ))}
          </div>
        )}
        {mailboxes.length === 0 ? (
          <div className="space-y-3 text-sm text-muted-foreground">
            <p>No mailboxes use {s.host} any more. <button className="font-medium underline underline-offset-2" onClick={() => go("mailboxes")}>Connect mailboxes</button> with it as their SMTP host to send through it.</p>
            {canManage && <Button variant="danger" onClick={remove}>Remove this server</Button>}
          </div>
        ) : (
          <MailboxTable ws={ws} mailboxes={mailboxes} canManage={canManage} onChange={load} />
        )}
      </Card>
    </div>
  );
}

function IssueRow({ issue }: { issue: ServerIssue }) {
  const s = severity[issue.severity];
  return (
    <li className={`rounded-md border p-3 text-sm ${s.box}`}>
      <div className="flex flex-wrap items-baseline gap-2">
        <span className="font-medium">{issue.title}</span>
        <span className="text-xs uppercase tracking-wide text-muted-foreground">{s.label}</span>
      </div>
      <p className="mt-1 text-foreground/80">{issue.detail}</p>
    </li>
  );
}

// One row per IP with its result on each blocklist.
function Blocklists({ s }: { s: SendingServer }) {
  if (!s.checked_at) return <p className="text-sm text-muted-foreground">Not checked yet.</p>;
  if (s.ips.length === 0) {
    return <p className="text-sm text-muted-foreground">No IPs found for {s.host}. Set the sending IPs by hand in Settings below to check them.</p>;
  }
  const byIP = s.ips.map((ip) => ({ ip, lists: s.blocklists.filter((b) => b.ip === ip) }));
  return (
    <div className="space-y-3 text-sm">
      <div className="divide-y">
        {byIP.map(({ ip, lists }) => (
          <div key={ip} className="grid gap-1.5 py-2 first:pt-0 last:pb-0 sm:grid-cols-[14rem_1fr] sm:gap-3">
            <div className="flex items-center gap-2">
              <code className="font-mono text-[12px] break-all">{ip}</code>
              <CopyButton text={ip} label="" />
            </div>
            <ul className="flex flex-wrap items-center gap-1.5">
              {lists.length === 0 && <li className="text-muted-foreground">Not checked yet.</li>}
              {lists.map((b) => (
                <li key={b.zone} className="inline-flex items-center gap-1.5">
                  <Badge tone={b.status === "clear" ? "green" : b.status === "listed" ? "red" : "grey"} title={b.detail || undefined}>
                    {b.status === "clear" ? "✓" : b.status === "listed" ? "✕" : "?"} {b.name}
                  </Badge>
                  {b.status === "listed" && <a className="text-xs underline underline-offset-2" href={delistURL(b.zone)} target="_blank" rel="noreferrer">Request removal</a>}
                  {b.status === "unknown" && b.detail && <span className="text-xs text-muted-foreground">{b.detail}</span>}
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
      <p className="text-xs text-muted-foreground">✓ not listed · ✕ listed · ? couldn't check. Checked again every day{s.pause_on_listing ? "; a listing pauses the server until every IP is clear" : ""}.</p>
      {s.blocklists.some((b) => b.status === "unknown" && b.ip.includes(".")) && (
        <p className="text-xs text-muted-foreground">Blocklists refuse lookups from public DNS resolvers such as 8.8.8.8 and 1.1.1.1. To check them, point the server running Outflare at its own resolver (for example Unbound).</p>
      )}
    </div>
  );
}

function Settings({ s, canManage, onSave }: { s: SendingServer; canManage: boolean; onSave: (body: Record<string, unknown>) => Promise<boolean> }) {
  const initialIPs = s.ips_source === "manual" ? s.ips.join("\n") : "";
  const [name, setName] = useState(s.name);
  const [cap, setCap] = useState(s.daily_limit === null ? "" : String(s.daily_limit));
  const [autoPause, setAutoPause] = useState(s.pause_on_listing);
  const [notes, setNotes] = useState(s.notes);
  const [ips, setIPs] = useState(initialIPs);
  const ipList = ips.split(/[\s,]+/).filter(Boolean);
  const ipsDirty = ips.trim() !== initialIPs;
  const dirty = name !== s.name || cap !== (s.daily_limit === null ? "" : String(s.daily_limit)) || autoPause !== s.pause_on_listing || notes !== s.notes || ipsDirty;

  async function save(e: FormEvent) {
    e.preventDefault();
    const body: Record<string, unknown> = { name: name.trim(), daily_limit: cap.trim() === "" ? null : Number(cap), pause_on_listing: autoPause, notes };
    // Empty goes back to the host's DNS records.
    if (ipsDirty) body.ips = ipList.length ? ipList : null;
    if (await onSave(body)) toast(ipsDirty ? "Saved. Checking the new IPs now." : "Saved.");
  }

  if (!canManage) {
    return (
      <Card title="Settings">
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Name</dt><dd>{s.name || "–"}</dd>
          <dt className="text-muted-foreground">Server daily cap</dt><dd>{s.daily_limit?.toLocaleString() ?? "None, mailbox and domain limits only"}</dd>
          <dt className="text-muted-foreground">Sending IPs</dt><dd>{s.ips_source === "manual" ? "Set by hand" : `From ${s.host}'s DNS records`}</dd>
          <dt className="text-muted-foreground">Pause when an IP is listed</dt><dd>{s.pause_on_listing ? "Yes" : "No"}</dd>
          <dt className="text-muted-foreground">Notes</dt><dd className="whitespace-pre-line">{s.notes || "–"}</dd>
        </dl>
      </Card>
    );
  }

  return (
    <Card title="Settings">
      <form onSubmit={save} className="space-y-4 text-sm">
        <label className="block">
          <span className="mb-1 block font-medium">Name</span>
          <input className={`${field} w-full sm:max-w-sm`} maxLength={200} placeholder="e.g. Mailreef pool 1" value={name} onChange={(e) => setName(e.target.value)} />
          <span className="mt-1 block text-xs text-muted-foreground">Shown instead of the host name in lists.</span>
        </label>
        <label className="block">
          <span className="mb-1 block font-medium">Server daily cap</span>
          <input className={`${field} w-40`} type="number" min={1} max={1000000} placeholder="No cap" value={cap} onChange={(e) => setCap(e.target.value)} />
          <span className="mt-1 block text-xs text-muted-foreground">
            Most emails all mailboxes on this server send together in any 24 hours, on top of each mailbox's and domain's own limits. Leave empty for no server cap{s.status === "active" && <> (now {s.mailbox_counts.active ? `${s.daily_capacity.toLocaleString()} a day in total` : "no active mailboxes"})</>}.
          </span>
        </label>
        <label className="block">
          <span className="mb-1 block font-medium">Sending IPs</span>
          <textarea className={`${field} w-full font-mono sm:max-w-sm`} rows={3} placeholder="One IP per line, e.g. 203.0.113.10" value={ips} onChange={(e) => setIPs(e.target.value)} />
          <span className="mt-1 block text-xs text-muted-foreground">
            {s.ips_source === "dns" ? <>Now taken from {s.host}'s DNS records.</> : <>Clear the box to take them from {s.host}'s DNS records again.</>} Fill in when mail leaves from other addresses than the one mailboxes sign in to. Ask your provider which IPs send your mail.
          </span>
        </label>
        <label className="flex items-start gap-2">
          <input type="checkbox" className="mt-0.5" checked={autoPause} onChange={(e) => setAutoPause(e.target.checked)} />
          <span>
            <span className="block font-medium">Pause when an IP is blocklisted</span>
            <span className="block text-xs text-muted-foreground">Stops sending as soon as a daily check finds any of its IPs listed, and resumes on its own once every IP is clear. Turn off to only be warned.</span>
          </span>
        </label>
        <label className="block">
          <span className="mb-1 block font-medium">Notes</span>
          <textarea className={`${field} w-full`} rows={2} maxLength={2000} placeholder="e.g. Mailreef dedicated IPs, renewed monthly on the 3rd" value={notes} onChange={(e) => setNotes(e.target.value)} />
        </label>
        <Button disabled={!dirty}>Save</Button>
      </form>
    </Card>
  );
}

function MailboxTable({ ws, mailboxes, canManage, onChange }: { ws: string; mailboxes: Mailbox[]; canManage: boolean; onChange: () => void }) {
  const styles = Object.fromEntries(Object.entries(mailboxStatus).map(([k, v]) => [k, v.tone])) as Record<Mailbox["status"], Tone>;
  const labels = Object.fromEntries(Object.entries(mailboxStatus).map(([k, v]) => [k, v.label])) as Record<Mailbox["status"], string>;
  async function toggle(m: Mailbox) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/mailboxes/{id}", { params: { path: { ws, id: m.id } }, body: { status: m.status === "paused" ? "active" : "paused" } });
    if (error) toast(errorMessage(error), "error");
    onChange();
  }
  return (
    <DataTable
      data={mailboxes}
      rowId={(m) => m.id}
      columnsMenu={false}
      searchPlaceholder={mailboxes.length > 10 ? "Search mailboxes" : undefined}
      empty="No mailboxes on this server yet."
      columns={[
        {
          id: "email",
          header: "Email",
          accessorFn: (m) => m.email,
          cell: ({ row: { original: m } }) => (
            <>
              <div className="font-mono text-[12px]">{m.email}</div>
              {m.status === "error" && m.status_reason && <div className="max-w-md text-xs whitespace-pre-line text-red-600 dark:text-red-400">{m.status_reason}</div>}
            </>
          ),
        },
        { id: "status", header: "Status", accessorFn: (m) => labels[m.status], cell: ({ row: { original: m } }) => <Badge tone={styles[m.status]}>{labels[m.status]}</Badge> },
        { id: "limit", header: "Per day", accessorFn: (m) => m.daily_limit, meta: { align: "right", mono: true } },
        { id: "gap", header: "Gap", accessorFn: (m) => m.min_gap_seconds, meta: { align: "right", mono: true }, cell: ({ row: { original: m } }) => `${Math.round(m.min_gap_seconds / 60)} min` },
        {
          id: "actions",
          header: () => <span className="sr-only">Actions</span>,
          enableSorting: false,
          meta: { align: "right" },
          cell: ({ row: { original: m } }) => canManage && m.status !== "verifying" && <Button variant="ghost" onClick={() => toggle(m)}>{m.status === "paused" ? "Resume" : "Pause"}</Button>,
        },
      ]}
    />
  );
}

function pct(r: number) {
  return `${Math.round(r * 1000) / 10}%`;
}
