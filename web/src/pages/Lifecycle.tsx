// Lifecycle email: receipts, password resets and onboarding email sent
// through the workspace's own provider (Amazon SES, Postmark or Resend).
// This file has the module's home (a get-started checklist, then how
// sending is going), the providers with their step-by-step setup guides,
// and sending domains with their DNS records.

import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import type { Connector, Email, EmailDomain, EmailDomainRecord, EmailOverview, EmailProvider, EmailProviderGuide, EmailStatus } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { Badge, Button, Card, confirmAction, CopyButton, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, Stat, Stats, Status, toast } from "../ui";
import { Icon } from "../icons";
import { Snippet } from "./Tokens";
import { ago, plural, stamp } from "../format";
import { track } from "@/lib/analytics";
import { isID, type Tab } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { ConnectorLogo } from "@/components/connector-logo";
import { DataTable } from "@/components/data-table";
import { takeConnectOutcome } from "./Connectors";
import { DNSWays, heldByMission, takeDNSFocus } from "@/components/dns-one-click";

export type Go = (tab: Tab, item?: string) => void;

// ---- Shared pieces ----

export const emailTone: Record<EmailStatus, "green" | "grey" | "amber" | "red" | "blue"> = {
  queued: "grey", scheduled: "grey", sending: "blue", sent: "blue", delivered: "green", delayed: "amber",
  bounced: "red", complained: "red", suppressed: "amber", failed: "red", cancelled: "grey",
};

export function EmailStatusBadge({ status }: { status: EmailStatus }) {
  return <Badge tone={emailTone[status]}>{status}</Badge>;
}

function eventsLabel(p: EmailProvider): { tone: "green" | "grey" | "amber" | "red"; text: string } {
  switch (p.events_status) {
    case "active":
      return { tone: "green", text: p.last_event_at ? `Delivery events working, last ${ago(p.last_event_at)}` : "Delivery events set up" };
    case "setting_up":
    case "not_set_up":
      return { tone: "grey", text: "Setting up delivery events…" };
    case "manual":
      return { tone: "amber", text: "Delivery events need one step from you" };
    default:
      return { tone: "red", text: "Delivery events failed to set up" };
  }
}

const recordTone = { verified: "green", pending: "grey", missing: "amber", mismatch: "red" } as const;
const recordLabel = { verified: "In place", pending: "Not checked yet", missing: "Not found yet", mismatch: "Different value" } as const;

// The DNS records a sending domain needs, each with copy buttons and
// whether public DNS has it yet.
export function DomainRecords({ records }: { records: EmailDomainRecord[] }) {
  if (records.length === 0) return <p className="text-[13px] text-muted-foreground">The provider is registering the domain. Its DNS records show here within a few seconds.</p>;
  return (
    <div className="space-y-2">
      {records.map((r, i) => (
        <div key={i} className="rounded-lg border p-3">
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <Badge tone="grey">{r.type}</Badge>
            <span className="text-[12px] font-medium">{({ dkim: "DKIM signing", spf: "SPF", return_path: "Bounce handling (MAIL FROM)", dmarc: "DMARC policy", verification: "Verification", mx: "Mail exchange" } as Record<string, string>)[r.purpose] ?? r.purpose}</span>
            {!r.required && <span className="text-[12px] text-muted-foreground">Recommended</span>}
            <span className="ml-auto"><Status tone={recordTone[r.status]}>{recordLabel[r.status]}</Status></span>
          </div>
          <dl className="grid gap-1.5 text-[12px] sm:grid-cols-[5rem_1fr]">
            <dt className="text-muted-foreground">Name</dt>
            <dd className="flex min-w-0 items-start gap-1"><code className="min-w-0 flex-1 break-all font-mono">{r.name}</code><CopyButton text={r.name} label="Copy name" /></dd>
            {r.type === "MX" && <><dt className="text-muted-foreground">Priority</dt><dd className="font-mono">{r.priority ?? 10}</dd></>}
            {r.purpose === "dmarc" && r.status === "verified" && r.found ? (
              // Any DMARC policy will do: show the one in place, not ours,
              // so nobody copies a weaker one over it.
              <><dt className="text-muted-foreground">Yours</dt><dd className="break-all font-mono">{r.found} <span className="font-sans text-muted-foreground">(keep it)</span></dd></>
            ) : (
              <><dt className="text-muted-foreground">Value</dt>
              <dd className="flex min-w-0 items-start gap-1"><code className="min-w-0 flex-1 break-all font-mono">{r.value}</code><CopyButton text={r.value} label="Copy value" /></dd></>
            )}
            {r.status === "mismatch" && r.found && <><dt className="text-muted-foreground">Found</dt><dd className="break-all font-mono text-red-700 dark:text-red-400">{r.found}</dd></>}
          </dl>
          {r.hint && <p className="mt-2 text-[12px] text-amber-700 dark:text-amber-400">{r.hint}</p>}
        </div>
      ))}
      <p className="text-[12px] text-muted-foreground">Some DNS hosts add your domain to the name for you: if yours does, paste only the part before your domain. Changes usually show within minutes, sometimes hours.</p>
    </div>
  );
}

function domainStatus(d: EmailDomain) {
  if (d.status === "verified") return <Status tone="green">Verified</Status>;
  if (d.status === "failed") return <Status tone="red">Not verified</Status>;
  const missing = d.records.filter((r) => r.required && r.status !== "verified").length;
  return <Status tone="amber">{d.records.length === 0 ? "Registering…" : missing ? `${plural(missing, "record")} to add` : "Waiting for the provider"}</Status>;
}

function useDomains(ws: string) {
  const [domains, setDomains] = useState<EmailDomain[] | null>(null);
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/email-provider-domains", { params: { path: { ws } } });
    setDomains(data?.data ?? []);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);
  // While any domain is pending, look again every 10 seconds: people add
  // records and watch.
  useEffect(() => {
    if (!domains?.some((d) => d.status === "pending")) return;
    const t = setInterval(load, domains.some((d) => d.dns_install?.status === "queued") ? 3000 : 10000);
    return () => clearInterval(t);
  }, [domains, load]);
  return { domains, load };
}

export function AddDomainForm({ ws, providers, onAdded, compact = false }: { ws: string; providers: EmailProvider[]; onAdded: (d: EmailDomain) => void; compact?: boolean }) {
  const [domain, setDomain] = useState("");
  const [provider, setProvider] = useState(providers.find((p) => p.is_default)?.id ?? providers[0]?.id ?? "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/email-provider-domains", { params: { path: { ws } }, body: { domain: domain.trim(), ...(provider ? { provider_id: provider } : {}) } });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setDomain("");
    toast(`Added ${data.domain}. Its DNS records show in a few seconds.`);
    onAdded(data);
  }
  return (
    <form onSubmit={add} className="space-y-2">
      <div className={cn("flex flex-wrap items-end gap-2", compact && "flex-nowrap")}>
        <label className="block min-w-48 flex-1 text-sm">
          <span className="mb-1.5 block text-[13px] font-medium">Domain you send from</span>
          <input className={`${field} w-full`} placeholder="example.com or mail.example.com" value={domain} onChange={(e) => setDomain(e.target.value)} required autoCapitalize="none" spellCheck={false} />
        </label>
        {providers.length > 1 && (
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">Provider</span>
            <select className={field} value={provider} onChange={(e) => setProvider(e.target.value)}>
              {providers.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
          </label>
        )}
        <Button disabled={busy || !domain.trim()}>{busy ? "Adding…" : "Add domain"}</Button>
      </div>
      <ErrorText>{error}</ErrorText>
    </form>
  );
}

// ---- Home ----

type Step = { id: string; title: string; done: boolean; body: ReactNode };

export function Lifecycle({ ws, canManage, canSend, go, myEmail }: { ws: string; canManage: boolean; canSend: boolean; go: Go; myEmail: string }) {
  const [overview, setOverview] = useState<EmailOverview | null>(null);
  const [providers, setProviders] = useState<EmailProvider[] | null>(null);
  const [guides, setGuides] = useState<EmailProviderGuide[]>([]);
  const [recent, setRecent] = useState<Email[]>([]);
  const { domains, load: loadDomains } = useDomains(ws);

  // Sending setup is usually shared by every brand, so providers aren't
  // narrowed; the recent emails are the picked brand's.
  const { query: brandQuery } = useBrand(ws);
  const load = useCallback(async () => {
    const [o, p, g, r] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/email-overview", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/email-providers", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/email-provider-guides", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/emails", { params: { path: { ws }, query: { limit: 6, ...brandQuery } } }),
    ]);
    if (o.data) setOverview(o.data);
    setProviders(p.data?.data ?? []);
    setGuides(g.data?.data ?? []);
    setRecent(r.data?.data ?? []);
  }, [ws, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load();
  }, [load]);

  if (!overview || !providers || !domains) return <Loading />;
  const setup = overview.setup;
  const verified = domains.filter((d) => d.status === "verified");
  const events = providers.some((p) => p.events_status === "active");
  const manual = providers.find((p) => p.events_status === "manual" || p.events_status === "failed");

  const steps: Step[] = [
    {
      id: "provider",
      title: "Connect an email provider",
      done: providers.length > 0,
      body: providers.length > 0
        ? <p>{providers.map((p) => p.name).join(", ")} connected. <button className="underline" onClick={() => go("providers")}>Manage providers</button></p>
        : (
          <div className="space-y-3">
            <p>Outflare sends through your own provider account, so you keep your reputation and pay the provider directly. Pick one and follow its guide: each takes a few minutes, and comes with a prompt your AI agent can follow to do it for you.</p>
            <ProviderChoices guides={guides} onPick={(c) => { track("ui.email_guide_opened", { connector: c, from: "lifecycle_home" }); go("providers", c); }} />
          </div>
        ),
    },
    {
      id: "domain",
      title: "Verify the domain you send from",
      done: verified.length > 0,
      body: verified.length > 0
        ? <p>{verified.map((d) => d.domain).join(", ")} verified.</p>
        : domains.length > 0
          ? <p>{domains.map((d) => d.domain).join(", ")}: add its DNS records at your DNS host. Outflare checks every minute. <button className="underline" onClick={() => go("providers")}>See the records</button></p>
          : providers.length === 0
            ? <p>Once a provider is connected, add your domain here and Outflare lists the DNS records to add.</p>
            : canManage ? <AddDomainForm ws={ws} providers={providers} onAdded={() => { loadDomains(); go("providers"); }} /> : <p>Ask an admin to add the domain you send from.</p>,
    },
    {
      id: "events",
      title: "Hear about deliveries, bounces and complaints",
      done: events,
      body: events
        ? <p>Bounces and spam complaints are added to your suppression list on their own.</p>
        : manual
          ? <p>{manual.name} needs one step from you. <button className="underline" onClick={() => go("providers", manual.id)}>Finish setting up events</button></p>
          : <p>Outflare sets this up with the provider when you connect it.</p>,
    },
    {
      id: "test",
      title: "Send a test email",
      done: setup.test_sent,
      body: verified.length === 0
        ? <p>Once a domain is verified, send yourself a test from here.</p>
        : canSend ? <TestSend ws={ws} myEmail={myEmail} domain={verified[0].domain} onSent={load} go={go} /> : <p>Ask a member who can send email to send a test.</p>,
    },
    {
      id: "app",
      title: "Send from your app",
      done: setup.sent,
      body: <SendSnippet go={go} domain={verified[0]?.domain ?? "example.com"} />,
    },
  ];
  const done = steps.filter((s) => s.done).length;
  const current = steps.find((s) => !s.done)?.id;
  const t = overview.totals_30d;
  const rate = (n: number) => (t.sent ? `${((n / t.sent) * 100).toFixed(n && n / t.sent < 0.01 ? 2 : 1)}%` : "—");

  return (
    <div className="space-y-6">
      <PageHeader
        title="Lifecycle overview"
        description="Receipts, password resets, sign-in codes and onboarding email, sent from your own provider account. Every send passes the suppression list, and bounces and complaints are suppressed for you. Cold outreach stays on Sequences."
        action={setup.providers > 0 && <><Button variant="ghost" onClick={() => go("templates")}>Templates</Button><Button onClick={() => go("emails")}>Sent emails</Button></>}
      />
      {done < steps.length && (
        <section aria-labelledby="get-started" className="rounded-lg border bg-card shadow-xs">
          <header className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
            <h2 id="get-started" className="text-[13px] font-semibold">Get started</h2>
            <span className="font-mono text-[12px] text-muted-foreground">{done} of {steps.length} done</span>
          </header>
          <ol className="divide-y">
            {steps.map((s, i) => (
              <li key={s.id} className={cn("flex gap-3 px-4 py-4", s.id !== current && !s.done && "opacity-70")}>
                <span aria-hidden="true" className={cn("mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full border font-mono text-[11px]", s.done ? "border-green-600/40 bg-green-500/15 text-green-700 dark:text-green-400" : s.id === current ? "border-brand bg-brand/10 text-foreground" : "text-muted-foreground")}>
                  {s.done ? <Icon name="check" className="size-3.5" /> : i + 1}
                </span>
                <div className="min-w-0 flex-1 space-y-1.5">
                  <h3 className={cn("text-[13px] font-medium", s.done && "text-muted-foreground")}>{s.title}<span className="sr-only">{s.done ? " (done)" : ""}</span></h3>
                  {(s.id === current || (s.done && s.id !== "app")) && <div className="text-[13px] text-muted-foreground [&_button.underline]:text-foreground">{s.body}</div>}
                </div>
              </li>
            ))}
          </ol>
        </section>
      )}
      {(t.sent > 0 || t.queued > 0 || recent.length > 0) && (
        <>
          <Stats cols={4}>
            <Stat label="Sent, 30 days" value={t.sent.toLocaleString()} sub={t.queued ? `${t.queued.toLocaleString()} waiting to send` : undefined} />
            <Stat label="Delivered" value={t.delivered + t.bounced + t.complained ? rate(t.delivered) : "—"} sub={!events ? "Needs delivery events" : t.delivered + t.bounced + t.complained ? `${t.delivered.toLocaleString()} confirmed` : "No delivery reports yet"} />
            <Stat label="Bounced" value={rate(t.bounced)} tone={t.sent && t.bounced / t.sent > 0.04 ? "red" : t.sent && t.bounced / t.sent > 0.02 ? "amber" : undefined} sub="Providers pause accounts above 5–10%" />
            <Stat label="Complaints" value={rate(t.complained)} tone={t.sent && t.complained / t.sent > 0.001 ? "red" : undefined} sub="Keep under 0.1%" />
          </Stats>
          <Card title="Last 30 days" description="Emails sent each day, by what happened to them. Test emails are left out.">
            <DailyChart days={overview.daily} />
          </Card>
          <Card title="Latest emails" action={<Button variant="ghost" onClick={() => go("emails")}>All sent emails</Button>}>
            {recent.length === 0 ? <p className="text-[13px] text-muted-foreground">Nothing sent yet.</p> : (
              <ul className="-my-2 divide-y">
                {recent.map((e) => (
                  <li key={e.id}>
                    <button className="flex w-full min-w-0 items-center gap-3 py-2 text-left hover:bg-accent/40" onClick={() => go("emails", e.id)}>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-[13px] font-medium">{e.subject}</span>
                        <span className="block truncate text-[12px] text-muted-foreground">{e.to.join(", ")}{e.template_handle ? ` · ${e.template_handle}` : ""}{e.test ? " · test" : ""}</span>
                      </span>
                      <EmailStatusBadge status={e.status} />
                      <span className="hidden w-28 text-right text-[12px] whitespace-nowrap text-muted-foreground sm:block" title={stamp(e.created_at)}>{ago(e.created_at)}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </>
      )}
    </div>
  );
}

function ProviderChoices({ guides, onPick }: { guides: EmailProviderGuide[]; onPick: (connector: string) => void }) {
  return (
    <div className="grid gap-2 sm:grid-cols-3">
      {guides.map((g) => (
        <button key={g.connector} type="button" onClick={() => onPick(g.connector)} className="flex flex-col items-start gap-2 rounded-lg border bg-background p-3 text-left transition-colors hover:border-ring/60 hover:bg-accent/30 outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/40">
          <span className="flex items-center gap-2 text-[13px] font-medium text-foreground"><ConnectorLogo id={g.connector} />{g.name}</span>
          <span className="text-[12px] text-muted-foreground">{g.summary}</span>
          <span className="mt-auto font-mono text-[11px] text-muted-foreground">About {g.minutes} minutes</span>
        </button>
      ))}
    </div>
  );
}

function DailyChart({ days }: { days: EmailOverview["daily"] }) {
  const max = Math.max(1, ...days.map((d) => d.sent + d.failed + d.suppressed));
  return (
    <div>
      <div className="flex h-32 items-end gap-[3px]" role="img" aria-label={`Emails sent per day over 30 days, at most ${max} in a day`}>
        {days.map((d) => {
          const total = d.sent + d.failed + d.suppressed;
          const ok = d.sent - d.bounced - d.complained;
          return (
            <div key={d.date} className="group relative flex h-full flex-1 flex-col justify-end" title={`${d.date}: ${d.sent} sent, ${d.delivered} delivered, ${d.bounced} bounced, ${d.complained} complaints, ${d.failed} failed, ${d.suppressed} suppressed`}>
              <div className="flex flex-col-reverse overflow-hidden rounded-[2px]" style={{ height: `${(total / max) * 100}%`, minHeight: total ? 2 : 0 }}>
                <div className="bg-brand/70" style={{ flex: Math.max(ok, 0) }} />
                <div className="bg-red-500/80" style={{ flex: d.bounced + d.complained + d.failed }} />
                <div className="bg-amber-500/70" style={{ flex: d.suppressed }} />
              </div>
              {!total && <div className="h-px bg-border" />}
            </div>
          );
        })}
      </div>
      <div className="mt-2 flex flex-wrap items-center justify-between gap-2 text-[11px] text-muted-foreground">
        <span className="font-mono">{days[0]?.date}</span>
        <span className="flex gap-3">
          <span className="inline-flex items-center gap-1"><span className="size-2 rounded-[2px] bg-brand/70" />Sent</span>
          <span className="inline-flex items-center gap-1"><span className="size-2 rounded-[2px] bg-red-500/80" />Bounced, complained or failed</span>
          <span className="inline-flex items-center gap-1"><span className="size-2 rounded-[2px] bg-amber-500/70" />Suppressed</span>
        </span>
        <span className="font-mono">{days[days.length - 1]?.date}</span>
      </div>
    </div>
  );
}

function TestSend({ ws, myEmail, domain, onSent, go }: { ws: string; myEmail: string; domain: string; onSent: () => void; go: Go }) {
  const [to, setTo] = useState(myEmail);
  const [from, setFrom] = useState(`hello@${domain}`);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function send(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/emails", {
      params: { path: { ws } },
      body: {
        to: to.trim(), from: from.trim(), test: true, subject: "Your first email from Outflare",
        html: `<p>It works. This email went through your own provider, signed for ${domain}.</p><p>Next: create a template and send it from your app.</p>`,
      },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    toast(`Test email queued to ${data.to[0]}.`);
    onSent();
    go("emails", data.id);
  }
  return (
    <form onSubmit={send} className="space-y-2">
      <div className="grid gap-2 sm:grid-cols-2">
        <Input label="To" type="email" value={to} onChange={(e) => setTo(e.target.value)} required />
        <Input label="From" value={from} onChange={(e) => setFrom(e.target.value)} hint={`Any address on ${domain}`} required />
      </div>
      <ErrorText>{error}</ErrorText>
      <Button disabled={busy}>{busy ? "Sending…" : "Send test email"}</Button>
    </form>
  );
}

export function SendSnippet({ go, domain, template = "password-reset", data = { first_name: "Ana", reset_url: "https://app.example.com/reset/…" } }: { go: Go; domain: string; template?: string; data?: Record<string, string> }) {
  const origin = typeof location === "undefined" ? "https://app.outflare.ai" : location.origin;
  const body = JSON.stringify({ to: "ana@customer.example", from: `Acme <billing@${domain}>`, template, data }, null, 2);
  return (
    <div className="space-y-2">
      <p>From your backend, with an API token that has the <span className="font-mono text-[12px]">emails.send</span> scope (<button className="underline" onClick={() => go("tokens")}>create one</button>). Send an Idempotency-Key so a retry never sends twice.</p>
      <Snippet title="curl" code={`curl ${origin}/v1/workspaces/$OUTFLARE_WORKSPACE/emails \\\n  -H "Authorization: Bearer $OUTFLARE_TOKEN" \\\n  -H "Content-Type: application/json" \\\n  -H "Idempotency-Key: $(uuidgen)" \\\n  -d '${body.replace(/'/g, "'\\''")}'`} />
      <Snippet title="TypeScript (@outflare/sdk)" code={`import { createClient } from "@outflare/sdk";\n\nconst outflare = createClient({ baseUrl: "${origin}", token: process.env.OUTFLARE_TOKEN });\n\nawait outflare.POST("/v1/workspaces/{ws}/emails", {\n  params: { path: { ws: process.env.OUTFLARE_WORKSPACE! }, header: { "Idempotency-Key": crypto.randomUUID() } },\n  body: ${body.split("\n").join("\n  ")},\n});`} />
    </div>
  );
}

// ---- Providers ----

export function Providers({ ws, canManage, open, onOpen, go }: { ws: string; canManage: boolean; open: string; onOpen: (item: string) => void; go: Go }) {
  const [providers, setProviders] = useState<EmailProvider[] | null>(null);
  const [guides, setGuides] = useState<EmailProviderGuide[] | null>(null);
  const { domains, load: loadDomains } = useDomains(ws);
  // Back from signing in to Cloudflare: open the sending domain it started
  // from, with its records ready to add.
  const [focus, setFocus] = useState<string | null>(null);
  useEffect(() => {
    const connected = takeConnectOutcome();
    const domain = takeDNSFocus();
    if (connected && domain) setFocus(domain);
  }, []);

  const load = useCallback(async () => {
    const [p, g] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/email-providers", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/email-provider-guides", { params: { path: { ws } } }),
    ]);
    setProviders(p.data?.data ?? []);
    setGuides(g.data?.data ?? []);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);
  // A new provider sets its events up in the background: look again.
  useEffect(() => {
    if (!providers?.some((p) => p.events_status === "not_set_up" || p.events_status === "setting_up")) return;
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [providers, load]);

  if (!providers || !guides || !domains) return <Loading />;
  const guide = guides.find((g) => g.connector === open);
  if (guide) return <GuidePage ws={ws} guide={guide} canManage={canManage} back={() => onOpen("")} onConnected={async (id) => { await load(); onOpen(id); }} />;
  const provider = isID(open) ? providers.find((p) => p.id === open) : undefined;
  if (provider) return <ProviderDetail ws={ws} provider={provider} domains={domains.filter((d) => d.provider_id === provider.id)} canManage={canManage} back={() => onOpen("")} reload={() => { load(); loadDomains(); }} />;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Email providers"
        description="The accounts your lifecycle email sends from: your own Amazon SES, Postmark or Resend. Connect one with its guide, then verify the domains you send from."
        action={<Button variant="ghost" onClick={() => go("lifecycle")}>Lifecycle overview</Button>}
      />
      {providers.length > 0 && (
        <DataTable
          data={providers}
          rowId={(p) => p.id}
          columnsMenu={false}
          onRowClick={(p) => onOpen(p.id)}
          mobile={(p) => (
            <span className="block space-y-1">
              <span className="flex items-center gap-2 font-medium"><ConnectorLogo id={p.connector} size="sm" />{p.name}{p.is_default && <Badge tone="blue">default</Badge>}</span>
              <span className="block text-[12px] text-muted-foreground">{p.domains_verified}/{p.domains} domains verified · {plural(p.sent_30d, "email")} in 30 days</span>
              <Status tone={eventsLabel(p).tone}>{eventsLabel(p).text}</Status>
            </span>
          )}
          columns={[
            { id: "name", header: "Provider", accessorFn: (p) => p.name, cell: ({ row: { original: p } }) => <span className="flex items-center gap-2 font-medium"><ConnectorLogo id={p.connector} size="sm" />{p.name}{p.is_default && <Badge tone="blue">default</Badge>}{p.status === "paused" && <Badge>Paused</Badge>}</span> },
            { id: "account", header: "Account", accessorFn: (p) => p.account.mode ?? "", cell: ({ row: { original: p } }) => p.connection === "error" ? <Status tone="red">Key refused</Status> : p.account.mode === "sandbox" ? <Status tone="amber">SES sandbox</Status> : <Status tone="green">{p.account.mode === "production" ? "Production" : "Connected"}</Status> },
            { id: "events", header: "Delivery events", accessorFn: (p) => p.events_status, cell: ({ row: { original: p } }) => <Status tone={eventsLabel(p).tone}>{eventsLabel(p).text}</Status> },
            { id: "domains", header: "Domains", accessorFn: (p) => p.domains, cell: ({ row: { original: p } }) => <span className="font-mono text-[12px]">{p.domains_verified}/{p.domains} verified</span> },
            { id: "sent", header: "Sent, 30 days", accessorFn: (p) => p.sent_30d, meta: { align: "right" }, cell: ({ row: { original: p } }) => <span className="font-mono text-[12px]">{p.sent_30d.toLocaleString()}</span> },
          ]}
        />
      )}
      {providers.length > 0 && (
        <section aria-labelledby="domains" className="space-y-3">
          <div className="flex flex-wrap items-end justify-between gap-2">
            <h2 id="domains" className="text-[13px] font-semibold">Sending domains</h2>
          </div>
          {domains.length === 0 ? <p className="text-[13px] text-muted-foreground">Add the domain your emails come from, such as example.com. Outflare registers it with the provider and lists the DNS records to add.</p> : (
            <div className="space-y-3">{domains.map((d) => <DomainCard key={d.id} ws={ws} domain={d} provider={providers.find((p) => p.id === d.provider_id)} canManage={canManage} reload={loadDomains} focused={focus === d.id} />)}</div>
          )}
          {canManage && <AddDomainForm ws={ws} providers={providers} onAdded={() => { loadDomains(); load(); }} />}
        </section>
      )}
      {guides.some((g) => !providers.some((p) => p.connector === g.connector)) && (
      <section aria-labelledby="add-provider" className="space-y-3">
        <h2 id="add-provider" className="text-[13px] font-semibold">{providers.length ? "Add another provider" : "Connect a provider"}</h2>
        <ProviderChoices guides={guides.filter((g) => !providers.some((p) => p.connector === g.connector))} onPick={(c) => { track("ui.email_guide_opened", { connector: c, from: "providers" }); onOpen(c); }} />
        <p className="text-[12px] text-muted-foreground">Not sure? Amazon SES is the cheapest at volume, Resend the quickest to set up, Postmark the most hands-off for transactional email.</p>
      </section>
      )}
    </div>
  );
}

function DomainCard({ ws, domain: d, provider, canManage, reload, focused }: { ws: string; domain: EmailDomain; provider?: EmailProvider; canManage: boolean; reload: () => void; focused?: boolean }) {
  const [open, setOpen] = useState(d.status !== "verified" || !!focused);
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    if (!focused) return;
    setOpen(true);
    ref.current?.scrollIntoView({ block: "start", behavior: "smooth" });
  }, [focused]);
  async function check() {
    const { error } = await api.POST("/v1/workspaces/{ws}/email-provider-domains/{id}/check", { params: { path: { ws, id: d.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Checking ${d.domain} now.`);
    setTimeout(reload, 3000);
  }
  async function remove() {
    if (!(await confirmAction(`Remove ${d.domain}?`, <p>Emails from {d.domain} are refused until it is added again. It stays in your {provider?.name ?? "provider"} account.</p>, "Remove domain"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/email-provider-domains/{id}", { params: { path: { ws, id: d.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`${d.domain} removed.`);
    reload();
  }
  return (
    <section ref={ref} className={cn("scroll-mt-4 rounded-lg border bg-card shadow-xs", focused && "border-brand/50 ring-2 ring-brand/20")}>
      <header className="flex flex-wrap items-center gap-3 px-4 py-3">
        <button type="button" className="flex min-w-0 flex-1 items-center gap-2 text-left" aria-expanded={open} onClick={() => setOpen(!open)}>
          <Icon name="chevron" className={cn("size-3.5 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")} />
          <span className="truncate font-mono text-[13px] font-medium">{d.domain}</span>
          {provider && <span className="hidden items-center gap-1 text-[12px] text-muted-foreground sm:inline-flex"><ConnectorLogo id={provider.connector} size="sm" />{provider.name}</span>}
        </button>
        {domainStatus(d)}
        {canManage && (
          <span className="flex gap-1">
            {d.status !== "verified" && <Button variant="ghost" onClick={check}>Check now</Button>}
            <Menu items={[{ label: "Check now", onClick: check }, { label: "Remove", danger: true, onClick: remove }]} />
          </span>
        )}
      </header>
      {open && (
        <div className="space-y-3 border-t px-4 py-3">
          {d.status_reason && d.status !== "verified" && <p className="text-[13px] text-muted-foreground">{d.status_reason}</p>}
          {d.checked_at && <p className="text-[12px] text-muted-foreground">Last checked {ago(d.checked_at)}{d.status === "pending" ? ". Outflare checks on its own, so there's nothing to press once the records are in: every minute for 15 minutes after you add the domain or press Check now, then less often, for three days." : "."}</p>}
          {d.status !== "verified" && d.records.some((r) => r.required && r.status !== "verified") && <DomainDNSWays ws={ws} domain={d} canManage={canManage} reload={reload} />}
          <DomainRecords records={d.records} />
        </div>
      )}
    </section>
  );
}

// The ways to add a sending domain's records, quickest first: one click
// through the workspace's own Cloudflare, a prompt for an AI agent, a
// message for a developer, or copying them from the list below.
function DomainDNSWays({ ws, domain: d, canManage, reload }: { ws: string; domain: EmailDomain; canManage: boolean; reload: () => void }) {
  return (
    <DNSWays ws={ws} page="providers" item={d.id} approval={{ kind: "email_domain", id: d.id }} connections={d.dns_connections} records={d.records.filter((r) => r.status !== "verified")} run={d.dns_install}
      canManage={canManage} reload={reload} used={(way, how) => track("ui.email_domain_dns_way_used", { way, how, connector: d.connector })}
      prompt={d.ai_prompt} developer={d.developer}
      manual={<>Add each record below where {d.domain}'s DNS is managed. Each one ticks off by itself once it's visible, and a wrong one says what's wrong.</>}
      intro={<>Connect the Cloudflare account {d.domain} is on.</>}
      after="Records already there stay as they are, including your DMARC policy. Then Outflare checks every minute until the provider confirms."
      onAdd={async (integration) => {
        const { data, error } = await api.POST("/v1/workspaces/{ws}/email-provider-domains/{id}/dns", { params: { path: { ws, id: d.id } }, body: { integration_id: integration } });
        return { error, held: heldByMission(data) };
      }} />
  );
}

// The prompt for the person's own AI agent: copy it, or open it in Claude
// or ChatGPT.
function AgentPrompt({ guide }: { guide: EmailProviderGuide }) {
  const [copied, setCopied] = useState(false);
  const used = (where: string) => track("ui.email_agent_prompt_used", { connector: guide.connector, where });
  const link = "inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-[13px] hover:bg-accent";
  return (
    <section aria-labelledby="agent-prompt" className="space-y-3 rounded-lg border border-brand/30 bg-brand/[0.04] p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="max-w-xl">
          <h2 id="agent-prompt" className="flex items-center gap-2 text-[13px] font-semibold"><Icon name="ai" className="size-4" />Have your AI agent do it</h2>
          <p className="mt-1 text-[12px] text-muted-foreground">Give this to an agent that can use your {guide.name} account (a browser agent, or one with the provider's CLI). It does the clicking and stops when it is time for you to create the key, so the secret never passes through the chat.</p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button onClick={async () => { await navigator.clipboard?.writeText(guide.agent_prompt); setCopied(true); used("copied"); setTimeout(() => setCopied(false), 1500); }}>
            <Icon name={copied ? "check" : "copy"} className="size-3.5" /> {copied ? "Copied" : "Copy prompt"}
          </Button>
          <a className={link} href={`https://claude.ai/new?q=${encodeURIComponent(guide.agent_prompt)}`} target="_blank" rel="noreferrer" onClick={() => used("claude")}>Open in Claude <Icon name="external" className="size-3" /></a>
          <a className={link} href={`https://chatgpt.com/?q=${encodeURIComponent(guide.agent_prompt)}`} target="_blank" rel="noreferrer" onClick={() => used("chatgpt")}>Open in ChatGPT <Icon name="external" className="size-3" /></a>
        </div>
      </div>
      <details>
        <summary className="cursor-pointer text-[12px] text-muted-foreground hover:text-foreground">Show the prompt</summary>
        <pre className="mt-2 max-h-72 overflow-y-auto rounded-md border bg-background px-3 py-2 font-mono text-[11.5px] leading-relaxed whitespace-pre-wrap text-muted-foreground">{guide.agent_prompt}</pre>
      </details>
    </section>
  );
}

function GuidePage({ ws, guide, canManage, back, onConnected }: { ws: string; guide: EmailProviderGuide; canManage: boolean; back: () => void; onConnected: (providerId: string) => void }) {
  const [def, setDef] = useState<Connector | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/connectors", { params: { path: { ws } } }).then(({ data }) => setDef(data?.data.find((c) => c.id === guide.connector) ?? null));
  }, [ws, guide.connector]);
  const connectStep = guide.steps.findIndex((s) => s.link_label?.startsWith("Connect"));
  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Email providers", onClick: back }}
        title={<><ConnectorLogo id={guide.connector} size="lg" />Set up {guide.name}</>}
        description={<>{guide.summary} About {guide.minutes} minutes.</>}
      />
      <AgentPrompt guide={guide} />
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_18rem]">
        <ol className="min-w-0 space-y-3">
          {guide.steps.map((s, i) => (
            <li key={i} className="rounded-lg border bg-card p-4 shadow-xs">
              <div className="flex gap-3">
                <span aria-hidden="true" className={cn("flex size-6 shrink-0 items-center justify-center rounded-full border font-mono text-[11px]", s.who === "outflare" ? "border-dashed border-green-600/50 text-green-700 dark:text-green-400" : "text-muted-foreground")}>{i + 1}</span>
                <div className="min-w-0 flex-1 space-y-2">
                  <h3 className="flex flex-wrap items-center gap-2 text-[13px] font-medium">{s.title}{s.who === "outflare" && <Badge tone="green">automatic</Badge>}</h3>
                  <p className="text-[13px] text-muted-foreground">{s.body}</p>
                  {s.code && <Snippet title={s.code_label ?? "copy"} code={s.code} />}
                  {i === connectStep ? (
                    canManage ? (def ? <ConnectForm ws={ws} def={def} onConnected={onConnected} /> : <Loading />) : <p className="text-[13px] text-amber-700 dark:text-amber-400">Only an admin can connect a provider. Send them this page.</p>
                  ) : s.link && (
                    <a className="inline-flex items-center gap-1 text-[13px] underline decoration-border underline-offset-2 hover:decoration-foreground" href={s.link} target="_blank" rel="noreferrer">{s.link_label ?? "Open"} <Icon name="external" className="size-3" /></a>
                  )}
                </div>
              </div>
            </li>
          ))}
        </ol>
        <aside className="space-y-4 text-[13px]">
          <Card title="Have ready">
            <ul className="list-disc space-y-1 pl-4 text-muted-foreground">{guide.needs.map((n) => <li key={n}>{n}</li>)}</ul>
          </Card>
          <Card title="New accounts">
            <p className="text-muted-foreground">{guide.limits}</p>
          </Card>
          <Card title="Keys stay safe">
            <p className="text-muted-foreground">Paste keys only into the form on this page, never into a chat. They are tested, stored encrypted and never shown again. Remove access any time by deleting the key in {guide.name}.</p>
          </Card>
        </aside>
      </div>
    </div>
  );
}

function ConnectForm({ ws, def, onConnected }: { ws: string; def: Connector; onConnected: (providerId: string) => void }) {
  // The region has a sensible default; everything else is pasted.
  const [values, setValues] = useState<Record<string, string>>(() => (def.fields.some((f) => f.name === "region") ? { region: "us-east-1" } : ({} as Record<string, string>)));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const creds = Object.fromEntries(Object.entries(values).map(([k, v]) => [k, v.trim()]).filter(([, v]) => v));
    const res = await api.POST("/v1/workspaces/{ws}/integrations", { params: { path: { ws } }, body: { connector: def.id, credentials: creds } });
    if (res.error || !res.data) {
      setBusy(false);
      return setError(errorMessage(res.error));
    }
    const p = await api.GET("/v1/workspaces/{ws}/email-providers", { params: { path: { ws } } });
    setBusy(false);
    const provider = p.data?.data.find((x) => x.integration_id === res.data!.id);
    toast(`Connected ${res.data.name}.${res.data.account?.details?.mode === "sandbox" ? " The account is in the SES sandbox: request production access to email anyone." : ""}`);
    if (provider) onConnected(provider.id);
  }
  return (
    <form onSubmit={submit} className="space-y-3 rounded-lg border bg-background p-3">
      {def.fields.map((f) => (
        <Input
          key={f.name}
          label={f.label + (f.optional ? " (optional)" : "")}
          type={f.secret ? "password" : "text"}
          autoComplete="off"
          spellCheck={false}
          placeholder={f.placeholder}
          hint={f.help}
          required={!f.optional}
          value={values[f.name] ?? ""}
          onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
        />
      ))}
      <ErrorText>{error}</ErrorText>
      <Button disabled={busy}>{busy ? "Testing the key…" : `Test and connect ${def.name}`}</Button>
    </form>
  );
}

function ProviderDetail({ ws, provider: p, domains, canManage, back, reload }: { ws: string; provider: EmailProvider; domains: EmailDomain[]; canManage: boolean; back: () => void; reload: () => void }) {
  const [callback, setCallback] = useState("");
  const [secret, setSecret] = useState("");
  const ev = eventsLabel(p);
  useEffect(() => {
    if (canManage && (p.events_status === "manual" || p.events_status === "failed")) {
      api.GET("/v1/workspaces/{ws}/email-providers/{id}/callback", { params: { path: { ws, id: p.id } } }).then(({ data }) => setCallback(data?.url ?? ""));
    }
  }, [ws, p.id, p.events_status, canManage]);

  async function update(body: { status?: "active" | "paused"; is_default?: true; purposes?: ("transactional" | "lifecycle")[] }, done: string) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/email-providers/{id}", { params: { path: { ws, id: p.id } }, body });
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    reload();
  }
  async function retryEvents() {
    const { error } = await api.POST("/v1/workspaces/{ws}/email-providers/{id}/events", { params: { path: { ws, id: p.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Setting up delivery events again.");
    setTimeout(reload, 3000);
  }
  async function saveSecret(e: FormEvent) {
    e.preventDefault();
    const { error } = await api.PUT("/v1/workspaces/{ws}/email-providers/{id}/signing-secret", { params: { path: { ws, id: p.id } }, body: { secret: secret.trim() } });
    if (error) return toast(errorMessage(error), "error");
    setSecret("");
    toast("Signing secret saved. Events show as working once the first one arrives.");
    reload();
  }
  const togglePurpose = (purpose: "transactional" | "lifecycle") => {
    const next = p.purposes.includes(purpose) ? p.purposes.filter((x) => x !== purpose) : [...p.purposes, purpose];
    if (next.length) update({ purposes: next }, "Saved.");
  };
  const facts = useMemo(() => Object.entries(p.account).filter(([k]) => ["region", "mode", "daily_quota", "max_send_rate", "domains"].includes(k)), [p.account]);

  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Email providers", onClick: back }}
        title={<><ConnectorLogo id={p.connector} size="lg" />{p.name}{p.is_default && <Badge tone="blue">default</Badge>}{p.status === "paused" && <Badge>Paused</Badge>}</>}
        description={<>Connected {ago(p.created_at)} · {plural(p.sent_30d, "email")} in 30 days</>}
        action={canManage && (
          <>
            {!p.is_default && <Button variant="ghost" onClick={() => update({ is_default: true }, `${p.name} is now the default.`)}>Make default</Button>}
            {p.status === "active"
              ? <Button variant="ghost" onClick={() => update({ status: "paused" }, "Paused. Emails wait in the queue until you resume it.")}>Pause</Button>
              : <Button onClick={() => update({ status: "active" }, "Resumed. Waiting emails go out now.")}>Resume</Button>}
          </>
        )}
      />
      {p.connection === "error" && <Callout tone="red" title="The provider refused the key">{p.connection_error ?? "Replace the key under Connectors."}</Callout>}
      {p.account.mode === "sandbox" && (
        <Callout tone="amber" title="This SES account is in the sandbox">
          It can only send to addresses verified in SES, 200 a day. <a className="underline" href={`https://console.aws.amazon.com/ses/home?region=${p.account.region ?? "us-east-1"}#/account`} target="_blank" rel="noreferrer">Request production access</a>: choose Transactional, describe the email you send and how people sign up. AWS usually answers within a day.
        </Callout>
      )}
      <div className="grid gap-4 lg:grid-cols-2">
        <Card title="Delivery events" description="Deliveries, bounces and complaints coming back from the provider.">
          <div className="space-y-3 text-[13px]">
            <Status tone={ev.tone}>{ev.text}</Status>
            {p.events_error && <p className="text-red-700 dark:text-red-400">{p.events_error}</p>}
            {p.events_status === "failed" && canManage && <Button variant="ghost" onClick={retryEvents}>Try again</Button>}
            {(p.events_status === "manual" || p.events_status === "failed") && callback && (
              <div className="space-y-2">
                <p className="text-muted-foreground">Add this URL as a webhook in {p.name} for deliveries, bounces and complaints. Keep it private.</p>
                <div className="flex items-center gap-1 rounded-md border bg-muted/40 px-2 py-1.5"><code className="min-w-0 flex-1 break-all font-mono text-[12px]">{callback}</code><CopyButton text={callback} label="Copy URL" /></div>
                {p.connector === "resend" && (
                  <form onSubmit={saveSecret} className="flex flex-wrap items-end gap-2">
                    <label className="block min-w-48 flex-1">
                      <span className="mb-1.5 block font-medium">Signing secret from Resend</span>
                      <input className={`${field} w-full`} type="password" autoComplete="off" placeholder="whsec_…" value={secret} onChange={(e) => setSecret(e.target.value)} required />
                    </label>
                    <Button disabled={!secret.trim()}>Save</Button>
                  </form>
                )}
              </div>
            )}
          </div>
        </Card>
        <Card title="Account" description="What the provider reports.">
          {facts.length === 0 ? <p className="text-[13px] text-muted-foreground">Nothing reported.</p> : (
            <dl className="grid grid-cols-2 gap-2 text-[13px]">
              {facts.map(([k, v]) => <div key={k}><dt className="text-[12px] text-muted-foreground">{{ region: "Region", mode: "Mode", daily_quota: "Daily quota", max_send_rate: "Sends a second", domains: "Domains in account" }[k]}</dt><dd className="font-mono">{v}</dd></div>)}
            </dl>
          )}
        </Card>
      </div>
      <Card title="What it sends" description="The kinds of email this provider may send. Each email goes through the default provider that allows its kind.">
        <div className="grid gap-2 sm:grid-cols-2">
          {([["transactional", "Transactional", "Receipts, resets, sign-in codes. Reaches people who unsubscribed from marketing."], ["lifecycle", "Lifecycle", "Onboarding and product updates. Carries a one-click unsubscribe."]] as const).map(([id, label, help]) => (
            <label key={id} className={cn("flex items-start gap-3 rounded-lg border p-3", !canManage && "opacity-70")}>
              <input type="checkbox" className="mt-0.5" checked={p.purposes.includes(id)} disabled={!canManage || (p.purposes.length === 1 && p.purposes.includes(id))} onChange={() => togglePurpose(id)} />
              <span className="text-[13px]"><span className="block font-medium">{label}</span><span className="block text-[12px] text-muted-foreground">{help}</span></span>
            </label>
          ))}
        </div>
      </Card>
      <section aria-labelledby="provider-domains" className="space-y-3">
        <h2 id="provider-domains" className="text-[13px] font-semibold">Sending domains</h2>
        {canManage && <AddDomainForm ws={ws} providers={[p]} onAdded={reload} />}
        {domains.length === 0 ? <Empty title="No sending domains yet">Add the domain your emails come from.</Empty> : domains.map((d) => <DomainCard key={d.id} ws={ws} domain={d} provider={p} canManage={canManage} reload={reload} />)}
      </section>
      <p className="text-[12px] text-muted-foreground">To change the key or remove {p.name}, go to Settings, Connectors. Removing it fails emails still waiting for it.</p>
    </div>
  );
}

function Callout({ tone, title, children }: { tone: "amber" | "red"; title: string; children: ReactNode }) {
  return (
    <div role="status" className={cn("rounded-lg border px-4 py-3 text-[13px]", tone === "amber" ? "border-amber-500/30 bg-amber-500/10 text-amber-900 dark:text-amber-200" : "border-red-500/30 bg-red-500/10 text-red-800 dark:text-red-200")}>
      <p className="font-medium">{title}</p>
      <p className="mt-0.5">{children}</p>
    </div>
  );
}
