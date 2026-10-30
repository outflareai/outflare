import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import type { DomainIssue, Mailbox, SendingDomain, SendingDomainDetail, TrackingDomain } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago, plural, replyRate } from "../format";
import type { Tab } from "../App";
import { DataTable } from "@/components/data-table";
import { health as sharedHealth, mailboxStatus } from "@/lib/status";
import { track } from "@/lib/analytics";
import { dnsHelp, DNSWays, heldByMission, takeDNSFocus, type DNSRow } from "@/components/dns-one-click";
import { takeConnectOutcome } from "./Connectors";
import { Icon } from "../icons";
import { cn } from "@/lib/utils";

type Health = SendingDomain["health"];

type Tone = keyof typeof tones;

// Sort order for the Health column: worst first when descending.
const healthRank: Record<Health, number> = { healthy: 0, pending: 1, warning: 2, critical: 3 };

const health: Record<Health, { label: string; tone: Tone }> = sharedHealth;

const severity: Record<DomainIssue["severity"], { label: string; box: string }> = {
  critical: { label: "Fix now", box: "border-red-300 bg-red-50 dark:border-red-900 dark:bg-red-950/40" },
  warning: { label: "Should fix", box: "border-amber-300 bg-amber-50 dark:border-amber-900 dark:bg-amber-950/40" },
  info: { label: "Good to know", box: "border-border bg-muted/40" },
};

function authTone(v: string): Tone {
  return v === "pass" ? "green" : v === "none_policy" ? "amber" : v === "pending" ? "grey" : "red";
}

function authLabel(v: string) {
  return { pass: "pass", missing: "missing", invalid: "2 records", none_policy: "p=none", pending: "…" }[v] ?? v;
}

// A compact badge for the list: the record's name with a mark, so the state
// does not rely on color alone.
function AuthBadge({ name, v }: { name: string; v: string }) {
  const mark = v === "pass" ? "✓" : v === "pending" ? "…" : v === "none_policy" ? "!" : "✕";
  return <Badge tone={authTone(v)} title={`${name}: ${authLabel(v)}`}>{mark} {name}</Badge>;
}

// "3 · 1.5%" for counts against sends, or just the count with no sends.
function rate(n: number, of: number) {
  // Replies can outnumber sends (several replies to one email), where a rate means nothing.
  if (of === 0 || n > of) return n === 0 ? "–" : String(n);
  return `${n} · ${Math.round((n / of) * 1000) / 10}%`;
}

type Props = { ws: string; canManage: boolean; open: string; onOpen: (domain: string) => void; go: (tab: Tab, item?: string) => void };

export function Domains(props: Props) {
  // Back from signing in to Cloudflare for a tracking domain's CNAME.
  useEffect(() => {
    takeConnectOutcome();
    takeDNSFocus();
  }, []);
  return props.open ? <Detail key={props.open} {...props} /> : <List {...props} />;
}

// ---- All domains ----

type Filter = "all" | "attention" | "healthy" | "paused" | "pending";

function List({ ws, canManage, onOpen, go }: Props) {
  const [domains, setDomains] = useState<SendingDomain[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");

  const brand = useBrand(ws).brand?.handle;
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/sending-domains", { params: { path: { ws }, query: { ...(brand ? { brand } : {}) } } });
    if (data) setDomains(data.data);
    setLoaded(true);
  }, [ws, brand]);

  useEffect(() => {
    load();
  }, [load]);

  // DNS checks run in the background; refresh until they settle.
  const pending = domains.some((d) => d.health === "pending");
  useEffect(() => {
    if (!pending) return;
    const t = setInterval(load, 2000);
    return () => clearInterval(t);
  }, [pending, load]);

  const count = (f: (d: SendingDomain) => boolean) => domains.filter(f).length;
  const attention = (d: SendingDomain) => d.health === "warning" || d.health === "critical";
  const shown = domains.filter((d) => {
    switch (filter) {
      case "attention": return attention(d);
      case "healthy": return d.health === "healthy";
      case "paused": return d.status === "paused";
      case "pending": return d.health === "pending";
    }
    return true;
  });

  async function checkAll() {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/sending-domains/check", { params: { path: { ws } } });
    if (error) toast(errorMessage(error), "error");
    else toast(`Re-checking ${plural(data?.queued ?? 0, "domain")}. Results appear in a few seconds.`);
    setTimeout(load, 1500);
  }

  if (!loaded) return <Loading />;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Domains"
        description="Every domain your mailboxes send from. Each one's DNS and blocklists are checked every day. Open a domain to see what to fix, how it is sending, and its settings."
        action={canManage && domains.length > 0 && <Button variant="ghost" onClick={checkAll}>Re-check all</Button>}
      />

      <Stats cols={6}>
        <Stat label="Domains" value={domains.length} />
        <Stat label="Healthy" value={count((d) => d.health === "healthy")} tone={count((d) => d.health === "healthy") > 0 ? "green" : undefined} />
        <Stat label="Need attention" value={count(attention)} tone={count((x) => x.health === "critical") > 0 ? "red" : count(attention) > 0 ? "amber" : undefined} />
        <Stat label="Paused" value={count((d) => d.status === "paused")} tone={count((d) => d.status === "paused") > 0 ? "amber" : undefined} />
        <Stat label="Capacity / day" value={domains.reduce((n, d) => n + d.daily_capacity, 0).toLocaleString()} />
        <Stat label="Sent · 24h" value={domains.reduce((n, d) => n + d.sent_24h, 0).toLocaleString()} />
      </Stats>

      {domains.length === 0 && (
        <Empty title="No domains yet" action={<Button onClick={() => go("mailboxes")}>Connect mailboxes</Button>}>
          A domain appears here as soon as you connect a mailbox on it.{canManage && " You can also add one below to check its DNS before you buy inboxes on it."}
        </Empty>
      )}

      {domains.length > 0 && (
        <DataTable
          data={shown}
          rowId={(d) => d.domain}
          onRowClick={(d) => onOpen(d.domain)}
          searchPlaceholder="Search domains"
          initialSort={[{ id: "health", desc: true }]}
          empty={<>No domains match. <button className="underline" onClick={() => setFilter("all")}>Show all</button></>}
          toolbar={
            <select className={field} aria-label="Show" value={filter} onChange={(e) => setFilter(e.target.value as Filter)}>
              <option value="all">All domains ({domains.length})</option>
              <option value="attention">Need attention ({count(attention)})</option>
              <option value="healthy">Healthy ({count((d) => d.health === "healthy")})</option>
              <option value="paused">Paused ({count((d) => d.status === "paused")})</option>
              {pending && <option value="pending">Checking ({count((d) => d.health === "pending")})</option>}
            </select>
          }
          mobile={(d) => (
            <div className="space-y-1.5">
              <div className="flex items-start justify-between gap-2">
                <span className="flex flex-wrap items-center gap-1.5"><span className="font-mono font-medium break-all">{d.domain}</span> <BrandBadge id={d.brand_id} /></span>
                <Badge tone={health[d.health].tone}>{health[d.health].label}</Badge>
              </div>
              <div className="flex flex-wrap gap-1">
                {d.status === "paused" && <Badge>Paused</Badge>}
                <AuthBadge name="SPF" v={d.spf} />
                <AuthBadge name="DKIM" v={d.dkim} />
                <AuthBadge name="DMARC" v={d.dmarc} />
              </div>
              <div className="text-xs text-muted-foreground">
                {d.mailbox_counts.active} of {d.mailboxes} mailboxes active · {d.sent_24h}/{d.daily_capacity} sent today · bounces {rate(d.bounces_7d, d.sent_7d)} · replies {rate(d.replies_7d, d.sent_7d)}
                {d.warmup.warming > 0 && <> · {d.warmup.warming} warming up</>}
              </div>
            </div>
          )}
          columns={[
            {
              id: "domain",
              header: "Domain",
              accessorFn: (d) => d.domain,
              enableHiding: false,
              cell: ({ row: { original: d } }) => (
                <>
                  <span className="font-mono text-[13px] font-medium group-hover:underline">{d.domain}</span> <BrandBadge id={d.brand_id} />
                  <div className="text-xs text-muted-foreground">
                    {d.status === "paused" ? <span className="font-medium text-amber-700 dark:text-amber-400">Paused</span> : providerName(d.mx_provider)}
                    {d.daily_limit !== null && <> · cap {d.daily_limit}/day</>}
                  </div>
                </>
              ),
            },
            {
              id: "health",
              header: "Health",
              accessorFn: (d) => healthRank[d.health],
              cell: ({ row: { original: d } }) => <Badge tone={health[d.health].tone} title={d.issues.filter((i) => i.severity !== "info").map((i) => i.title).join("\n") || undefined}>{health[d.health].label}</Badge>,
            },
            {
              id: "auth",
              header: "Authentication",
              enableSorting: false,
              cell: ({ row: { original: d } }) => (
                <span className="inline-flex flex-wrap gap-1">
                  <AuthBadge name="SPF" v={d.spf} />
                  <AuthBadge name="DKIM" v={d.dkim} />
                  <AuthBadge name="DMARC" v={d.dmarc} />
                </span>
              ),
            },
            {
              id: "mailboxes",
              header: "Mailboxes",
              accessorFn: (d) => d.mailboxes,
              meta: { align: "right", mono: true },
              cell: ({ row: { original: d } }) => (
                <>
                  {d.mailbox_counts.active}<span className="text-muted-foreground">/{d.mailboxes}</span>
                  {d.mailbox_counts.error > 0 && <div className="text-xs text-red-600 dark:text-red-400">{d.mailbox_counts.error} failing</div>}
                </>
              ),
            },
            { id: "sent", header: "Sent 24h", accessorFn: (d) => d.sent_24h, meta: { align: "right", mono: true }, cell: ({ row: { original: d } }) => <>{d.sent_24h}<span className="text-muted-foreground">/{d.daily_capacity}</span></> },
            { id: "bounces", header: "Bounces 7d", accessorFn: (d) => (d.sent_7d ? d.bounces_7d / d.sent_7d : 0), meta: { align: "right", mono: true }, cell: ({ row: { original: d } }) => <span className={d.issues.some((i) => i.code === "bounce_rate") ? "text-red-600 dark:text-red-400" : ""}>{rate(d.bounces_7d, d.sent_7d)}</span> },
            { id: "replies", header: "Replies 7d", accessorFn: (d) => d.replies_7d, meta: { align: "right", mono: true }, cell: ({ row: { original: d } }) => rate(d.replies_7d, d.sent_7d) },
            { id: "warming", header: "Warming", accessorFn: (d) => d.warmup.warming, meta: { align: "right", mono: true }, cell: ({ row: { original: d } }) => d.warmup.warming || <span className="text-muted-foreground">–</span> },
          ]}
        />
      )}

      {canManage && <AddDomain ws={ws} onAdded={(d) => onOpen(d)} />}
      <TrackingDomains ws={ws} canManage={canManage} />
    </div>
  );
}

function AddDomain({ ws, onAdded }: { ws: string; onAdded: (domain: string) => void }) {
  const [domain, setDomain] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/sending-domains", { params: { path: { ws } }, body: { domain } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    onAdded(data.domain);
  }

  return (
    <Card title="Add a domain">
      <form onSubmit={submit} className="space-y-2 text-sm">
        <p className="text-muted-foreground">Track a domain before its mailboxes are connected, for example one you just bought, to check its DNS and blocklists.</p>
        <div className="flex flex-wrap gap-2">
          <input className={`${field} min-w-0 flex-1 sm:max-w-xs`} placeholder="acme-com.example" aria-label="Domain" value={domain} onChange={(e) => { setDomain(e.target.value); setError(""); }} required />
          <Button disabled={busy || !domain.trim()}>{busy ? "Adding…" : "Add and check"}</Button>
        </div>
        <ErrorText>{error}</ErrorText>
      </form>
    </Card>
  );
}

// ---- Tracking domains ----

const trackingStatus: Record<TrackingDomain["status"], { label: string; tone: Tone }> = {
  verified: { label: "In use", tone: "green" },
  pending: { label: "Waiting for DNS", tone: "grey" },
  failed: { label: "Not pointing here", tone: "red" },
};

// A verified domain waits for its HTTPS certificate on servers that get
// one for it (Outflare Cloud); links use it only once that is active.
function trackingBadge(t: TrackingDomain): { label: string; tone: Tone } {
  if (t.status === "verified" && t.https_status === "pending") return { label: "Setting up HTTPS", tone: "grey" };
  if (t.status === "verified" && t.https_status === "failed") return { label: "HTTPS failed", tone: "red" };
  return trackingStatus[t.status];
}

function trackingReady(t: TrackingDomain) {
  return t.status === "verified" && (t.https_status === null || t.https_status === "active");
}

function trackingToast(t: TrackingDomain) {
  if (trackingReady(t)) return `${t.domain} is ready. New emails use it.`;
  if (t.status === "verified") return `${t.domain} points here. Setting up HTTPS: new emails use it once that's done, usually in a few minutes.`;
  return "";
}

function TrackingDomains({ ws, canManage }: { ws: string; canManage: boolean }) {
  const [list, setList] = useState<TrackingDomain[]>();
  const [target, setTarget] = useState("");
  const fallback = window.location.host;
  const [domain, setDomain] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const path = { ws };

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/tracking-domains", { params: { path: { ws } } });
    if (data) {
      setList(data.data);
      setTarget(data.target);
    }
  }, [ws]);
  // Outflare re-checks DNS and the certificate on its own; keep the page
  // current while a domain is on its way.
  const settling = list?.some((t) => !trackingReady(t) && t.https_status !== "failed");
  useEffect(() => {
    if (!settling) return;
    const id = setInterval(load, 20_000);
    return () => clearInterval(id);
  }, [settling, load]);
  useEffect(() => {
    load();
  }, [load]);
  // While a CNAME is being added in Cloudflare, or looked for after.
  const watching = list?.some((t) => t.dns_install?.status === "queued" || (t.status !== "verified" && t.dns_install && Date.now() - Date.parse(t.dns_install.at) < 15 * 60_000) || (t.status === "verified" && t.https_status === "pending"));
  useEffect(() => {
    if (!watching) return;
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [watching, load]);

  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy("add");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/tracking-domains", { params: { path }, body: { domain } });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setDomain("");
    setError("");
    toast(trackingToast(data) || `Added ${data.domain}. Add the CNAME record below, then check it.`);
    load();
  }

  async function check(t: TrackingDomain) {
    setBusy(t.id);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/tracking-domains/{id}/check", { params: { path: { ws, id: t.id } } });
    setBusy("");
    if (error) return toast(errorMessage(error), "error");
    toast(trackingToast(data) || `Not yet: ${data.error}`, data.status === "verified" ? "ok" : "error");
    load();
  }

  async function remove(t: TrackingDomain) {
    if (!(await confirmAction(`Remove ${t.domain}?`, <p>New emails go back to this server's address. Links in emails already sent keep working while {t.domain} still points here.</p>, "Remove"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/tracking-domains/{id}", { params: { path: { ws, id: t.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Removed ${t.domain}.`);
    load();
  }

  return (
    <Card title="Tracking domain" description="Optional. Unsubscribe links, and click links on sequences that track clicks, use your own domain instead of this server's address.">
      {list === undefined ? <Loading /> : (
        <div className="space-y-3 text-sm">
          {list.length === 0 && <p className="text-muted-foreground">None yet. Links use {fallback}.</p>}
          {list.map((t) => (
            <div key={t.id} className="space-y-2 rounded-md border p-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-[13px] font-medium">{t.domain}</span>
                <Badge tone={trackingBadge(t).tone}>{trackingBadge(t).label}</Badge>
                {t.last_checked_at && <span className="text-xs text-muted-foreground">checked {ago(t.last_checked_at)}</span>}
                {canManage && (
                  <span className="ml-auto flex gap-2">
                    <Button variant="ghost" onClick={() => check(t)} disabled={busy === t.id}>{busy === t.id ? "Checking…" : "Check now"}</Button>
                    <Button variant="ghost" onClick={() => remove(t)}>Remove</Button>
                  </span>
                )}
              </div>
              <TrackingSteps t={t} fallback={fallback} />
              {t.status !== "verified" && (
                <>
                  <div className="flex flex-wrap items-center gap-2 rounded bg-muted/50 px-2 py-1.5 font-mono text-xs">
                    <span className="text-muted-foreground">CNAME</span> <span>{t.domain}</span> <span className="text-muted-foreground">→</span> <span>{t.target}</span> <Copy text={t.target} />
                  </div>
                  <p className="text-xs text-muted-foreground">
                    On Cloudflare, set Proxy status to DNS only (the grey cloud), or the record can't be seen. After changing it, wait 5 minutes before you check again.{" "}
                    <a className="underline" href="https://outflare.ai/docs/guides/tracking-domains/#common-mistakes" target="_blank" rel="noreferrer" onClick={() => track("ui.docs_opened", { tab: "domains", from: "tracking_cname" })}>Common mistakes</a>
                  </p>
                  <TrackingDNS ws={ws} t={t} canManage={canManage} reload={load} />
                </>
              )}
            </div>
          ))}
          {canManage && (
            <form onSubmit={add} className="space-y-2">
              <div className="flex flex-wrap gap-2">
                <input className={`${field} min-w-0 flex-1 sm:max-w-xs`} placeholder="links.acme-com.example" aria-label="Tracking domain" value={domain} onChange={(e) => { setDomain(e.target.value); setError(""); }} required />
                <Button disabled={busy === "add" || !domain.trim()}>{busy === "add" ? "Adding…" : "Add tracking domain"}</Button>
              </div>
              <p className="text-xs text-muted-foreground">Use a subdomain of your sending domain, such as links.yourdomain-com.example, and point it at {target} with a CNAME record. Changes can take up to an hour to show.</p>
              <ErrorText>{error}</ErrorText>
            </form>
          )}
        </div>
      )}
    </Card>
  );
}

// ---- One domain ----

function Detail({ ws, canManage, open, onOpen, go }: Props) {
  const [d, setD] = useState<SendingDomainDetail | null>(null);
  const [missing, setMissing] = useState(false);
  const [mailboxes, setMailboxes] = useState<Mailbox[]>([]);
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState("");
  const checkedBefore = useRef<string | null>(null);
  const path = { params: { path: { ws, domain: open } } };

  const load = useCallback(async () => {
    const [dd, mm] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/sending-domains/{domain}", { params: { path: { ws, domain: open } } }),
      api.GET("/v1/workspaces/{ws}/mailboxes", { params: { path: { ws }, query: { domain: open } } }),
    ]);
    if (dd.response.status === 404) return setMissing(true);
    if (dd.data) setD(dd.data);
    setMailboxes(mm.data?.data ?? []);
  }, [ws, open]);

  useEffect(() => {
    load();
  }, [load]);

  // Refresh while the first check, a re-check or mailbox checks are running.
  const waiting = checking || d?.checked_at === null || mailboxes.some((m) => m.status === "verifying");
  useEffect(() => {
    if (!waiting) return;
    const t = setInterval(load, 2000);
    return () => clearInterval(t);
  }, [waiting, load]);
  useEffect(() => {
    if (checking && d && d.checked_at !== checkedBefore.current) setChecking(false);
  }, [checking, d]);
  useEffect(() => {
    if (!checking) return;
    const t = setTimeout(() => setChecking(false), 30000);
    return () => clearTimeout(t);
  }, [checking]);

  if (missing) {
    return (
      <div>
        <PageHeader title={open} back={{ label: "All domains", onClick: () => onOpen("") }} />
        <Empty title="Domain not found">There is no domain called {open} in this workspace. It may have been removed.</Empty>
      </div>
    );
  }
  if (!d) return <Loading />;

  async function patch(body: Record<string, unknown>) {
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/sending-domains/{domain}", { ...path, body });
    if (error) {
      setError(errorMessage(error));
      return false;
    }
    setError("");
    setD((old) => (old ? { ...old, ...data } : old));
    load();
    return true;
  }

  async function recheck() {
    checkedBefore.current = d!.checked_at;
    setChecking(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/sending-domains/{domain}/check", path);
    if (error) {
      setChecking(false);
      toast(errorMessage(error), "error");
    }
  }

  async function togglePause() {
    const pausing = d!.status === "active";
    const n = d!.mailboxes;
    if (pausing && !(await confirmAction(`Pause all sending from ${d!.domain}?`, <p>{plural(n, "mailbox", "mailboxes")} stop sending sequence and test emails until you resume. Leads wait where they are, and replies you write in the inbox still go.</p>, "Pause domain"))) return;
    if (await patch({ status: pausing ? "paused" : "active" })) toast(pausing ? `Paused ${d!.domain}. Nothing sends from it until you resume.` : `Resumed ${d!.domain}.`);
  }

  async function remove() {
    if (!(await confirmAction(`Stop tracking ${d!.domain}?`, <p>It leaves the Domains list. Add it again any time, or connect a mailbox on it.</p>, "Stop tracking"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/sending-domains/{domain}", path);
    if (error) return toast(errorMessage(error), "error");
    toast(`Stopped tracking ${d!.domain}.`);
    onOpen("");
  }

  const problems = d.issues.filter((i) => i.severity !== "info");
  const notes = d.issues.filter((i) => i.severity === "info");
  const bounceRate = d.sent_7d > 0 ? d.bounces_7d / d.sent_7d : 0;

  return (
    <div className="space-y-6">
      <div>
        <PageHeader
          back={{ label: "All domains", onClick: () => onOpen("") }}
          title={<><span className="break-all font-mono">{d.domain}</span><Badge tone={health[d.health].tone}>{health[d.health].label}</Badge>{d.status === "paused" && <Badge>Paused</Badge>}</>}
          description={<>{providerName(d.mx_provider)} · DNS checked {checking ? "now…" : d.checked_at ? ago(d.checked_at) : "not yet"}</>}
          action={canManage && (
            <>
              <Button variant="ghost" onClick={recheck} disabled={checking}>{checking ? "Checking…" : "Re-check DNS"}</Button>
              <Button variant={d.status === "active" ? "ghost" : "primary"} onClick={togglePause}>{d.status === "active" ? "Pause domain" : "Resume sending"}</Button>
            </>
          )}
        />
        <ErrorText>{error}</ErrorText>
      </div>

      <Card title={problems.length ? `What to fix (${problems.length})` : "What to fix"}>
        {d.health === "pending" && <p className="text-sm text-muted-foreground">Checking DNS and blocklists. This takes a few seconds.</p>}
        {d.health !== "pending" && problems.length === 0 && (
          <p className="text-sm text-green-700 dark:text-green-400">Nothing to fix. SPF, DKIM and DMARC are in place and the domain is on none of the blocklists checked.</p>
        )}
        <ul className="space-y-3">
          {[...problems, ...notes].map((i) => <IssueRow key={i.code + i.title} issue={i} domain={d.domain} />)}
        </ul>
        {(missingRecords(d).length > 0 || dkimHost(d)) && (
          <div className="mt-4">
            <SendingDNS ws={ws} d={d} canManage={canManage} reload={load} />
          </div>
        )}
      </Card>

      <Stats cols={5}>
        <Stat label="Sent · 24h" value={d.sent_24h} sub={`of ${d.daily_capacity} daily capacity`} />
        <Stat label="Sent · 7d" value={d.sent_7d} />
        <Stat label="Replies · 7d" value={d.replies_7d} sub={replyRate(d.replied_leads_7d, d.contacted_leads_7d) && `${replyRate(d.replied_leads_7d, d.contacted_leads_7d)} reply rate`} />
        <Stat label="Bounces · 7d" value={d.bounces_7d} sub={d.sent_7d ? `${pct(bounceRate)} bounce rate${bounceRate > 0.02 ? ", keep under 2%" : ""}` : undefined} tone={bounceRate > 0.02 ? "red" : undefined} />
        <Stat label="Warming up" value={d.warmup.warming} sub={d.warmup.warming > 0 ? "in a connected sequencer" : "turn on warm-up in Mailboxes"} />
      </Stats>

      <Card title="Last 14 days">
        <DailyChart days={d.daily} />
      </Card>

      <Card title="DNS records">
        <DNS d={d} />
      </Card>

      <Card title="Blocklists">
        <ul className="space-y-2 text-sm">
          {d.blocklists.length === 0 && <li className="text-muted-foreground">Not checked yet.</li>}
          {d.blocklists.map((b) => (
            <li key={b.zone} className="flex flex-wrap items-baseline gap-2">
              <Badge tone={b.status === "clear" ? "green" : b.status === "listed" ? "red" : "grey"}>{b.status === "clear" ? "not listed" : b.status === "listed" ? "listed" : "couldn't check"}</Badge>
              <span className="font-medium">{b.name}</span>
              {b.status === "unknown" && b.detail && <span className="text-muted-foreground">{b.detail}</span>}
            </li>
          ))}
        </ul>
        {d.blocklists.some((b) => b.status === "unknown") && (
          <p className="mt-3 text-xs text-muted-foreground">Blocklists refuse lookups from public DNS resolvers such as 8.8.8.8 and 1.1.1.1. To check them, point the server running Outflare at its own resolver (for example Unbound).</p>
        )}
      </Card>

      <Settings d={d} canManage={canManage} mailboxCount={mailboxes.length} onSave={patch} />

      <Card title={`Mailboxes on ${d.domain} (${mailboxes.length})`} action={<Button variant="ghost" onClick={() => go("mailboxes")}>All mailboxes</Button>}>
        {mailboxes.length === 0 ? (
          <div className="space-y-3 text-sm text-muted-foreground">
            <p>No mailboxes on this domain yet. <button className="font-medium underline underline-offset-2" onClick={() => go("mailboxes")}>Connect mailboxes</button> to send from it.</p>
            {canManage && <Button variant="danger" onClick={remove}>Stop tracking this domain</Button>}
          </div>
        ) : (
          <MailboxTable ws={ws} mailboxes={mailboxes} canManage={canManage} onChange={load} />
        )}
      </Card>
    </div>
  );
}

// missingRecords are the records the domain's issues ask for, with full
// names, as the server adds them. The SPF fix replaces the record there.
function missingRecords(d: SendingDomainDetail): DNSRow[] {
  return d.issues.flatMap((i) => (i.record ? [{ type: i.record.type, name: i.record.host === "@" ? d.domain : `${i.record.host}.${d.domain}`, value: i.record.value, priority: i.record.priority, change: i.code === "spf_provider" }] : []));
}

// dkimHost is the mail host whose DKIM key can be pasted in when the
// domain has none: Google Workspace or Microsoft 365.
function dkimHost(d: SendingDomainDetail): "google" | "microsoft" | null {
  if (!d.issues.some((i) => i.code === "dkim_missing")) return null;
  const host = d.mx_provider && d.mx_provider !== "none" ? d.mx_provider : d.mail_host;
  return host === "google" || host === "microsoft" ? host : null;
}

// dkimRows turns what was pasted into the records, checked the same way the
// server checks them, or null while it isn't a key yet.
const googleKey = /^v=DKIM1;\s*k=rsa;\s*p=([A-Za-z0-9+/]+={0,2})$/;
const microsoftTarget = /^selector[12]-[a-z0-9-]+\._domainkey\.[a-z0-9-]+(\.[a-z0-9-]+)+$/;
function dkimRows(domain: string, host: "google" | "microsoft", values: string[]): DNSRow[] | null {
  if (host === "google") {
    const m = values[0].trim().replace(/^"+|"+$/g, "").replaceAll('" "', "").trim().split(/\s+/).join(" ").match(googleKey);
    return m ? [{ type: "TXT", name: `google._domainkey.${domain}`, value: `v=DKIM1; k=rsa; p=${m[1]}` }] : null;
  }
  const t = values.map((v) => v.trim().toLowerCase().replace(/\.$/, ""));
  if (!t.every((v, i) => microsoftTarget.test(v) && v.startsWith(`selector${i + 1}-`))) return null;
  return t.map((v, i) => ({ type: "CNAME", name: `selector${i + 1}._domainkey.${domain}`, value: v }));
}

// The ways to add a domain's missing mail records: MX, SPF, DMARC, and the
// DKIM key once it's pasted from the mail host.
function SendingDNS({ ws, d, canManage, reload }: { ws: string; d: SendingDomainDetail; canManage: boolean; reload: () => void }) {
  const host = dkimHost(d);
  const [pasted, setPasted] = useState<string[]>(["", ""]);
  const values = host === "google" ? pasted.slice(0, 1) : pasted;
  const dkimRecords = host && values.some((v) => v.trim()) ? dkimRows(d.domain, host, values) : null;
  const dkim = dkimRecords ? values.map((v) => v.trim()) : undefined;
  const records = [...missingRecords(d), ...(dkimRecords ?? [])];
  const set = (i: number) => (e: { target: { value: string } }) => setPasted((p) => p.map((v, j) => (j === i ? e.target.value : v)));
  return (
    <div className="space-y-3">
      {host && canManage && (
        <div className="space-y-2 rounded-md border bg-muted/30 p-3 text-[13px]">
          {host === "google" ? (
            <>
              <p>To add the DKIM key too, open the Google Admin console, go to Apps, Google Workspace, Gmail, Authenticate email, pick {d.domain} and press Generate new record. Paste the TXT record value here:</p>
              <textarea className={cn(field, "w-full font-mono text-[12px]")} rows={3} value={pasted[0]} onChange={set(0)} placeholder="v=DKIM1; k=rsa; p=MIIBIjANBgkq…" aria-label="DKIM TXT record value" />
            </>
          ) : (
            <>
              <p>To add the DKIM keys too, open the Microsoft Defender portal, go to Email authentication settings, DKIM, pick {d.domain} and copy the two CNAME targets here:</p>
              <input className={cn(field, "w-full font-mono text-[12px]")} value={pasted[0]} onChange={set(0)} placeholder="selector1-acme-example._domainkey.acme.onmicrosoft.com" aria-label="selector1 CNAME target" />
              <input className={cn(field, "w-full font-mono text-[12px]")} value={pasted[1]} onChange={set(1)} placeholder="selector2-acme-example._domainkey.acme.onmicrosoft.com" aria-label="selector2 CNAME target" />
            </>
          )}
          {values.some((v) => v.trim()) && !dkimRecords && <p className="text-[12px] text-destructive">{host === "google" ? "That isn't a DKIM TXT value yet. It starts with v=DKIM1; k=rsa; p=" : "Paste both targets: the selector1 one first, then selector2."}</p>}
        </div>
      )}
      {records.length > 0 && <SendingDNSWays ws={ws} d={d} canManage={canManage} reload={reload} records={records} dkim={dkim} />}
    </div>
  );
}

function SendingDNSWays({ ws, d, canManage, reload, records, dkim }: { ws: string; d: SendingDomainDetail; canManage: boolean; reload: () => void; records: DNSRow[]; dkim?: string[] }) {
  return (
    <DNSWays ws={ws} page="domains" item={d.domain} approval={{ kind: "sending_domain", id: d.domain, dkim }} connections={d.dns_connections} canManage={canManage} reload={reload}
      records={records} run={d.dns_install} {...dnsHelp(d.domain, records, "so mail sent from it is delivered and passes SPF, DKIM and DMARC checks")}
      used={(way, how) => track("ui.dns_one_click_used", { where: "sending_domain", way, how })}
      manual={<>Add each record shown above where {d.domain}'s DNS is managed, then press Re-check DNS. If the domain already has an SPF record, change it to the one shown instead of adding a second.</>}
      intro={<>Connect the Cloudflare account {d.domain} is on.</>}
      after="Records already there stay as they are. Then Outflare checks the domain again."
      onAdd={async (integration) => {
        const { data, error } = await api.POST("/v1/workspaces/{ws}/sending-domains/{domain}/dns", { params: { path: { ws, domain: d.domain } }, body: { integration_id: integration, dkim } });
        return { error, held: heldByMission(data) };
      }} />
  );
}

type TrackingStep = { label: string; detail: ReactNode; state: "done" | "now" | "waiting" | "failed" | "todo" };

// Where a tracking domain is on its way to being used, step by step, and
// whether the next move is the person's or Outflare's.
function trackingSteps(t: TrackingDomain, fallback: string): TrackingStep[] {
  const found = t.status === "verified";
  const https = t.https_status;
  const steps: TrackingStep[] = [
    found
      ? { label: "Add the DNS record", detail: <>{t.domain} points to {t.target}.</>, state: "done" }
      : t.status === "failed"
        ? { label: "Add the DNS record", detail: <>It stopped pointing here: {t.error || "the record is missing"}. Fix the record below, then press Check now.</>, state: "failed" }
        : { label: "Add the DNS record", detail: <>Your turn: add the record below.{t.error && <> Last check: {t.error}</>}</>, state: "now" },
    found
      ? { label: "Outflare finds it", detail: "Found.", state: "done" }
      : { label: "Outflare finds it", detail: "Outflare checks on its own over the next hour, or press Check now once the record is added.", state: "todo" },
  ];
  if (https !== null) {
    steps.push(https === "active"
      ? { label: "HTTPS certificate", detail: "Issued.", state: "done" }
      : https === "failed"
        ? { label: "HTTPS certificate", detail: <>{t.https_error || "It couldn't be issued."} Press Check now to try again.</>, state: "failed" }
        : { label: "HTTPS certificate", detail: "Nothing to do: it's being issued, usually within a few minutes. This page updates on its own.", state: "waiting" });
  }
  steps.push(trackingReady(t)
    ? { label: "In use", detail: <>New emails' links use {t.domain}.</>, state: "done" }
    : { label: "In use", detail: <>Until then, links use {fallback}, so nothing breaks.</>, state: "todo" });
  return steps;
}

function TrackingSteps({ t, fallback }: { t: TrackingDomain; fallback: string }) {
  return (
    <ol className="space-y-1.5">
      {trackingSteps(t, fallback).map((s, i) => (
        <li key={s.label} className="flex gap-2.5">
          <TrackingStepMark state={s.state} n={i + 1} />
          <div className="min-w-0">
            <p className={cn("text-[13px] font-medium", s.state === "todo" && "text-muted-foreground")}>{s.label}</p>
            <p className="text-xs text-muted-foreground">{s.detail}</p>
          </div>
        </li>
      ))}
    </ol>
  );
}

function TrackingStepMark({ state, n }: { state: TrackingStep["state"]; n: number }) {
  const box = "mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full";
  if (state === "done") return <span className={cn(box, "bg-green-600 text-white dark:bg-green-500")}><Icon name="check" className="size-3" /></span>;
  if (state === "failed") return <span className={cn(box, "bg-destructive text-white")}><Icon name="x" className="size-3" /></span>;
  if (state === "waiting") return <span className={cn(box, "border-2 border-blue-500 border-t-transparent motion-safe:animate-spin")} aria-label="In progress" />;
  return <span className={cn(box, "border font-mono text-[10px]", state === "now" ? "border-blue-500 text-blue-600 dark:text-blue-400" : "text-muted-foreground")}>{n}</span>;
}

// The ways to add a tracking domain's CNAME.
function TrackingDNS({ ws, t, canManage, reload }: { ws: string; t: TrackingDomain; canManage: boolean; reload: () => void }) {
  const records = [{ type: "CNAME", name: t.domain, value: t.target }];
  return (
    <DNSWays ws={ws} page="domains" item={t.id} approval={{ kind: "tracking_domain", id: t.id }} connections={t.dns_connections} canManage={canManage} reload={reload}
      records={records} run={t.dns_install} {...dnsHelp(t.domain, records, "so links in our emails use our own domain")}
      used={(way, how) => track("ui.dns_one_click_used", { where: "tracking_domain", way, how })}
      manual={<>Add the CNAME above where {t.domain}'s DNS is managed, DNS only (not proxied), then press Check now.</>}
      intro={<>Connect the Cloudflare account {t.domain} is on.</>}
      after="Then Outflare looks for it on its own over the next hour."
      onAdd={async (integration) => {
        const { data, error } = await api.POST("/v1/workspaces/{ws}/tracking-domains/{id}/dns", { params: { path: { ws, id: t.id } }, body: { integration_id: integration } });
        return { error, held: heldByMission(data) };
      }} />
  );
}

function IssueRow({ issue, domain }: { issue: DomainIssue; domain: string }) {
  const s = severity[issue.severity];
  return (
    <li className={`rounded-md border p-3 text-sm ${s.box}`}>
      <div className="flex flex-wrap items-baseline gap-2">
        <span className="font-medium">{issue.title}</span>
        <span className="text-xs uppercase tracking-wide text-muted-foreground">{s.label}</span>
      </div>
      <p className="mt-1 text-foreground/80">{issue.detail}</p>
      {issue.record && (
        <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 rounded bg-white/70 p-2 text-xs dark:bg-black/30">
          <dt className="text-muted-foreground">Type</dt><dd className="font-mono">{issue.record.type}</dd>
          <dt className="text-muted-foreground">Host</dt>
          <dd className="font-mono">{issue.record.host} <span className="font-sans text-muted-foreground">({issue.record.host === "@" ? domain : `${issue.record.host}.${domain}`})</span></dd>
          <dt className="text-muted-foreground">Value</dt>
          <dd className="flex flex-wrap items-center gap-2"><code className="break-all">{issue.record.value}</code><Copy text={issue.record.value} /></dd>
        </dl>
      )}
    </li>
  );
}

function Copy({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      className="rounded border px-1.5 py-0.5 text-xs hover:bg-accent"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        } catch {
          toast("Couldn't copy. Select the value and copy it by hand.", "error");
        }
      }}
    >
      {done ? "Copied" : "Copy"}
    </button>
  );
}

function DNS({ d }: { d: SendingDomainDetail }) {
  const rows: [string, string, ReactNode, string][] = [
    ["MX", d.mx_provider === "none" ? "missing" : d.mx_provider ? "pass" : "pending", d.mx_hosts.length ? d.mx_hosts.join(", ") : null, "Where replies to this domain are delivered."],
    ["SPF", d.spf, d.spf_record, "Which servers may send as this domain."],
    ["DKIM", d.dkim, d.dkim_selector ? <>key found at <code>{d.dkim_selector}._domainkey</code></> : null, "Signs each email so receivers can tell it was not altered."],
    ["DMARC", d.dmarc, d.dmarc_record, "Tells receivers what to do with mail that fails SPF and DKIM."],
  ];
  return (
    <div className="divide-y text-sm">
      {rows.map(([name, status, value, help]) => (
        <div key={name} className="grid gap-1 py-2 first:pt-0 last:pb-0 sm:grid-cols-[6rem_6rem_1fr] sm:gap-3">
          <div className="font-medium">{name}</div>
          <div><Badge tone={authTone(status)}>{authLabel(status)}</Badge></div>
          <div className="min-w-0">
            {value ? <div className="flex flex-wrap items-center gap-2"><code className="break-all text-xs">{value}</code>{typeof value === "string" && <Copy text={value} />}</div> : <div className="text-muted-foreground">{status === "pending" ? "Checking…" : "No record found"}</div>}
            <div className="text-xs text-muted-foreground">{help}</div>
          </div>
        </div>
      ))}
    </div>
  );
}

function DailyChart({ days }: { days: SendingDomainDetail["daily"] }) {
  const [table, setTable] = useState(false);
  const [hover, setHover] = useState<number | null>(null);
  const max = Math.max(1, ...days.map((x) => x.sent));
  const total = days.reduce((n, x) => n + x.sent, 0);
  const label = (date: string) => new Date(date + "T00:00:00Z").toLocaleDateString(undefined, { day: "numeric", month: "short", timeZone: "UTC" });
  const h = hover === null ? null : days[hover];

  return (
    <div className="space-y-2 text-sm">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <span className="text-muted-foreground">Emails sent per day (UTC). {total === 0 && "Nothing sent from this domain in the last 14 days."}</span>
        {total > 0 && <button className="text-xs underline underline-offset-2" onClick={() => setTable(!table)}>{table ? "Show chart" : "Show as table"}</button>}
      </div>
      {total === 0 ? null : table ? (
        <div className="relative overflow-x-auto">
          <table className="w-full text-left text-sm [&_td]:pr-3 [&_th]:pr-3">
            <thead className="text-muted-foreground"><tr><th className="py-1 font-normal">Day</th><th className="font-normal">Sent</th><th className="font-normal">Replies</th><th className="font-normal">Bounces</th></tr></thead>
            <tbody>
              {days.map((x) => <tr key={x.date} className="border-t border-border"><td className="py-1">{label(x.date)}</td><td>{x.sent}</td><td>{x.replies}</td><td>{x.bounces}</td></tr>)}
            </tbody>
          </table>
        </div>
      ) : (
        <>
          <div className="h-5 text-xs text-muted-foreground" aria-live="polite">
            {h ? `${label(h.date)}: ${h.sent} sent, ${h.replies} repl${h.replies === 1 ? "y" : "ies"}, ${h.bounces} bounce${h.bounces === 1 ? "" : "s"}` : "Hover or tap a day for its numbers."}
          </div>
          <div className="flex h-32 items-end gap-0.5 border-b border-border" onMouseLeave={() => setHover(null)}>
            {days.map((x, i) => (
              <button
                key={x.date}
                className="flex h-full flex-1 items-end"
                aria-label={`${label(x.date)}: ${x.sent} sent`}
                onMouseEnter={() => setHover(i)}
                onFocus={() => setHover(i)}
                onClick={() => setHover(i)}
              >
                <span
                  className={`block w-full rounded-t ${hover === i ? "bg-foreground" : "bg-muted-foreground/60"}`}
                  style={{ height: x.sent ? `${Math.max(3, (x.sent / max) * 100)}%` : "0" }}
                />
              </button>
            ))}
          </div>
          <div className="flex justify-between text-xs text-muted-foreground">
            <span>{label(days[0].date)}</span>
            <span>peak {max}/day</span>
            <span>Today</span>
          </div>
        </>
      )}
    </div>
  );
}

function Settings({ d, canManage, mailboxCount, onSave }: { d: SendingDomain; canManage: boolean; mailboxCount: number; onSave: (body: Record<string, unknown>) => Promise<boolean> }) {
  const [cap, setCap] = useState(d.daily_limit === null ? "" : String(d.daily_limit));
  const [notes, setNotes] = useState(d.notes);
  const [perMailbox, setPerMailbox] = useState("");
  const [gap, setGap] = useState("");
  const dirty = cap !== (d.daily_limit === null ? "" : String(d.daily_limit)) || notes !== d.notes;

  async function save(e: FormEvent) {
    e.preventDefault();
    if (await onSave({ daily_limit: cap.trim() === "" ? null : Number(cap), notes })) toast("Saved.");
  }

  async function apply(e: FormEvent) {
    e.preventDefault();
    const body: Record<string, number> = {};
    if (perMailbox) body.mailbox_daily_limit = Number(perMailbox);
    if (gap) body.mailbox_min_gap_seconds = Number(gap) * 60;
    const what = [perMailbox && `a daily limit of ${perMailbox}`, gap && `${gap} min between sends`].filter(Boolean).join(" and ");
    if (!(await confirmAction(`Change every mailbox on ${d.domain}?`, <p>Sets {what} on all {plural(mailboxCount, "mailbox", "mailboxes")}. Each mailbox can still be changed on its own afterwards.</p>, `Update ${plural(mailboxCount, "mailbox", "mailboxes")}`, false))) return;
    if (await onSave(body)) {
      setPerMailbox("");
      setGap("");
      toast(`Updated ${plural(mailboxCount, "mailbox", "mailboxes")}.`);
    }
  }

  if (!canManage) {
    return (
      <Card title="Settings">
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Domain daily cap</dt><dd>{d.daily_limit ?? "None, mailbox limits only"}</dd>
          <dt className="text-muted-foreground">Notes</dt><dd className="whitespace-pre-line">{d.notes || "–"}</dd>
        </dl>
      </Card>
    );
  }

  return (
    <Card title="Settings">
      <div className="space-y-6 text-sm">
        <form onSubmit={save} className="space-y-3">
          <label className="block">
            <span className="mb-1 block font-medium">Domain daily cap</span>
            <input className={`${field} w-40`} type="number" min={1} max={100000} placeholder="No cap" value={cap} onChange={(e) => setCap(e.target.value)} />
            <span className="mt-1 block text-xs text-muted-foreground">
              Most emails all mailboxes on this domain send together in any 24 hours. Leave empty to use only each mailbox's own limit (now {d.mailbox_counts.active ? `${d.daily_capacity} a day in total` : "no active mailboxes"}).
            </span>
          </label>
          <label className="block">
            <span className="mb-1 block font-medium">Notes</span>
            <textarea className={`${field} w-full`} rows={2} maxLength={2000} placeholder="e.g. Bought 1 Oct from Cheap Inboxes, warming until 15 Oct" value={notes} onChange={(e) => setNotes(e.target.value)} />
          </label>
          <Button disabled={!dirty}>Save</Button>
        </form>

        {mailboxCount > 0 && (
          <form onSubmit={apply} className="space-y-3 border-t pt-4">
            <div>
              <div className="font-medium">Set on every mailbox on this domain</div>
              <p className="text-xs text-muted-foreground">Changes all {mailboxCount} mailbox{mailboxCount === 1 ? "" : "es"} at once. Leave a field empty to keep each mailbox's current value.</p>
            </div>
            <div className="flex flex-wrap items-end gap-3">
              <label className="block">
                <span className="mb-1 block text-xs text-muted-foreground">Daily limit per mailbox (1 to 500)</span>
                <input className={`${field} w-40`} type="number" min={1} max={500} placeholder="e.g. 30" value={perMailbox} onChange={(e) => setPerMailbox(e.target.value)} />
              </label>
              <label className="block">
                <span className="mb-1 block text-xs text-muted-foreground">Minutes between sends (0 to 60)</span>
                <input className={`${field} w-40`} type="number" min={0} max={60} placeholder="e.g. 2" value={gap} onChange={(e) => setGap(e.target.value)} />
              </label>
              <Button variant="ghost" disabled={!perMailbox && !gap}>Apply to all</Button>
            </div>
          </form>
        )}
      </div>
    </Card>
  );
}

function MailboxTable({ ws, mailboxes, canManage, onChange }: { ws: string; mailboxes: Mailbox[]; canManage: boolean; onChange: () => void }) {
  const styles = Object.fromEntries(Object.entries(mailboxStatus).map(([k, v]) => [k, v.tone])) as Record<Mailbox["status"], Tone>;
  const labels = Object.fromEntries(Object.entries(mailboxStatus).map(([k, v]) => [k, v.label])) as Record<Mailbox["status"], string>;
  async function toggle(m: Mailbox) {
    await api.PATCH("/v1/workspaces/{ws}/mailboxes/{id}", { params: { path: { ws, id: m.id } }, body: { status: m.status === "paused" ? "active" : "paused" } });
    onChange();
  }
  return (
    <DataTable
      data={mailboxes}
      rowId={(m) => m.id}
      columnsMenu={false}
      empty="No mailboxes on this domain yet."
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

function providerName(p: string | null) {
  return { google: "Google Workspace", microsoft: "Microsoft 365", other: "Other mail host", none: "No MX record" }[p ?? ""] ?? "Not checked yet";
}
