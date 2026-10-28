import { useCallback, useEffect, useState } from "react";
import type { components, Connector } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Loading, Status, toast } from "../ui";
import { Icon } from "../icons";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { plural } from "../format";
import { ConnectorLogo } from "@/components/connector-logo";
import { AIPromptPanel, CopyText, DeveloperPanel, Panel } from "@/components/install-methods";
import { heldByMission } from "@/components/dns-one-click";
import { authOption, ConnectDialog, startConnectorOAuth, type ReturnPage } from "@/pages/Connectors";

type DNS = components["schemas"]["BrandDNS"];
type Group = DNS["groups"][number];
type Approval = components["schemas"]["DNSApproval"];
type Method = "oneclick" | "developer" | "ai" | "manual";

// Every DNS record a brand needs, in one list, added in one go: the site's
// proof, its sending domains' records and its tracking domain. Where the
// domain's DNS provider supports it, the person approves them on the
// provider's own screen, one group after another without coming back to
// press anything; otherwise the brand's Cloudflare connection adds them
// all with one button. A developer message, an AI prompt and the plain
// list cover everything else.

// The groups still to approve at the provider, kept in the tab while the
// browser is away on the provider's screen.
const queueKey = "outflare.dns_batch";
type Queued = { brand: string; provider: string; left: { subject: Group["subject"]; id: string }[]; total: number };

function readQueue(): Queued | null {
  try {
    const raw = sessionStorage.getItem(queueKey);
    return raw ? (JSON.parse(raw) as Queued) : null;
  } catch {
    return null;
  }
}

function writeQueue(q: Queued | null) {
  try {
    if (q) sessionStorage.setItem(queueKey, JSON.stringify(q));
    else sessionStorage.removeItem(queueKey);
  } catch {
    // Without it each group is approved from the page instead.
  }
}

async function approvalLink(ws: string, g: { subject: Group["subject"]; id: string }, ret: string) {
  const query = { return: ret };
  switch (g.subject) {
    case "seo_site":
      return api.GET("/v1/workspaces/{ws}/seo-sites/{site}/dns/approval", { params: { path: { ws, site: g.id }, query } });
    case "tracking_domain":
      return api.GET("/v1/workspaces/{ws}/tracking-domains/{id}/dns/approval", { params: { path: { ws, id: g.id }, query } });
    case "email_domain":
      return api.GET("/v1/workspaces/{ws}/email-provider-domains/{id}/dns/approval", { params: { path: { ws, id: g.id }, query } });
  }
}

async function reportApproval(ws: string, g: { subject: Group["subject"]; id: string }, body: { error?: string; provider?: string }) {
  switch (g.subject) {
    case "seo_site":
      return api.POST("/v1/workspaces/{ws}/seo-sites/{site}/dns/approved", { params: { path: { ws, site: g.id } }, body });
    case "tracking_domain":
      return api.POST("/v1/workspaces/{ws}/tracking-domains/{id}/dns/approved", { params: { path: { ws, id: g.id } }, body });
    case "email_domain":
      return api.POST("/v1/workspaces/{ws}/email-provider-domains/{id}/dns/approved", { params: { path: { ws, id: g.id } }, body });
  }
}

// Back from the provider: its state says which group, error says it
// didn't happen. Taken once, and removed from the address.
function takeReturn(): { id: string; error?: string } | null {
  const q = new URLSearchParams(location.search);
  const state = q.get("state");
  if (!state?.startsWith("dc.")) return null;
  const error = q.get("error") ?? undefined;
  for (const k of ["state", "error", "error_description"]) q.delete(k);
  history.replaceState(null, "", location.pathname + (q.size ? `?${q}` : ""));
  return { id: state.slice(3), error };
}

export function BrandDNS({ ws, brand, website, canManage, page, onChange }: {
  ws: string;
  brand: string;
  website: string;
  canManage: boolean;
  // The app page to come back to after a sign-in or approval.
  page: ReturnPage;
  // Something was added or approved, so the checklist can look again.
  onChange?: () => void;
}) {
  const [dns, setDNS] = useState<DNS | null>(null);
  const [error, setError] = useState("");
  const [method, setMethod] = useState<Method>("oneclick");
  const [approvals, setApprovals] = useState<Record<string, Approval>>({});
  const [cloudflare, setCloudflare] = useState<Connector | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<string>("");
  const path = { params: { path: { ws, brand } } };

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/brands/{brand}/dns", path);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    // A brand with a website and no site to verify yet gets one, so its
    // proof is on the list from the start.
    if (canManage && website && !data.groups.some((g) => g.subject === "seo_site")) {
      await api.POST("/v1/workspaces/{ws}/brands/{brand}/setup/prepare", { ...path, body: { step: "verify_site" } });
      const again = await api.GET("/v1/workspaces/{ws}/brands/{brand}/dns", path);
      if (again.data) return setDNS(again.data);
    }
    setDNS(data);
    return data;
  }, [ws, brand, website, canManage]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void load();
    api.GET("/v1/workspaces/{ws}/connectors", { params: { path: { ws } } }).then(({ data }) => setCloudflare(data?.data?.find((c) => c.id === "cloudflare") ?? null));
  }, [load, ws]);

  // Back from approving one group at the provider: record it, then go
  // straight on to the next one in the queue.
  useEffect(() => {
    if (!canManage) return;
    const back = takeReturn();
    const q = readQueue();
    if (!back || !q || q.brand !== brand) return;
    const done = q.left.find((g) => g.id === back.id);
    if (!done) return;
    const rest = q.left.filter((g) => g.id !== back.id);
    track("ui.dns_one_click_used", { where: "brand_dns", how: back.error ? "approval_cancelled" : "approved", groups: q.total });
    void reportApproval(ws, done, { error: back.error, provider: q.provider }).then(async ({ error }) => {
      if (error) toast(errorMessage(error), "error");
      if (back.error) {
        writeQueue(null);
        toast(back.error === "access_denied" ? "Nothing was changed: the records weren't approved." : "Your DNS provider couldn't add the records. Try again, or add them another way.", back.error === "access_denied" ? undefined : "error");
        void load();
        return;
      }
      if (rest.length === 0) {
        writeQueue(null);
        toast(`All records approved in ${q.provider}. Outflare checks for them on its own.`);
        void load();
        onChange?.();
        return;
      }
      writeQueue({ ...q, left: rest });
      setProgress(`${q.total - rest.length} of ${q.total} approved. Opening the next one at ${q.provider}…`);
      const { data } = await approvalLink(ws, rest[0], location.pathname + location.search);
      if (data?.available && data.url) location.assign(data.url);
      else {
        writeQueue(null);
        setProgress("");
        void load();
      }
    });
  }, [ws, brand, canManage]); // eslint-disable-line react-hooks/exhaustive-deps

  // Can the provider approve every group with missing records?
  const pending = dns?.groups.filter((g) => g.missing > 0) ?? [];
  const pendingKey = pending.map((g) => g.id).join(",");
  useEffect(() => {
    if (!canManage || pending.length === 0) return setApprovals({});
    let live = true;
    Promise.all(pending.map((g) => approvalLink(ws, g, location.pathname + location.search).then(({ data }) => [g.id, data] as const))).then((list) => {
      if (!live) return;
      const out: Record<string, Approval> = {};
      for (const [id, a] of list) if (a) out[id] = a;
      setApprovals(out);
    });
    return () => {
      live = false;
    };
  }, [ws, canManage, pendingKey]); // eslint-disable-line react-hooks/exhaustive-deps

  // While a one-click run is going, or Outflare is waiting to see records,
  // look again every few seconds.
  const queued = dns?.groups.some((g) => g.last?.status === "queued");
  useEffect(() => {
    if (!queued) return;
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [queued, load]);

  if (error) return <p className="text-[13px] text-destructive">{error}</p>;
  if (!dns) return <Loading />;
  if (dns.groups.length === 0) {
    return <Panel><p className="text-[13px] text-muted-foreground">Add the brand's website first: its DNS records come from it.</p></Panel>;
  }

  const used = (how: string) => track("ui.setup_method_used", { step: "dns", method, how });
  const provider = pending.length > 0 && pending.every((g) => approvals[g.id]?.available) ? approvals[pending[0].id]?.provider ?? "" : "";
  const connection = dns.connections[0];
  const failed = dns.groups.find((g) => g.last?.status === "failed");

  async function approveAll() {
    const first = pending[0];
    const a = approvals[first.id];
    if (!a?.url) return;
    writeQueue({ brand, provider, left: pending.map((g) => ({ subject: g.subject, id: g.id })), total: pending.length });
    track("ui.dns_one_click_used", { where: "brand_dns", how: "approval", groups: pending.length });
    location.assign(a.url);
  }

  async function addAll() {
    if (!connection) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/brands/{brand}/dns", { ...path, body: { integration_id: connection } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    track("ui.dns_one_click_used", { where: "brand_dns", how: "added", groups: pending.length });
    if (heldByMission(data)) toast("Waiting for approval in a mission.");
    void load();
    onChange?.();
  }

  const signIn = (def: Connector) => {
    track("ui.dns_one_click_used", { where: "brand_dns", how: "sign_in" });
    void startConnectorOAuth(ws, def, page);
  };

  return (
    <div className="space-y-4">
      <ul className="space-y-3">
        {dns.groups.map((g) => (
          <li key={g.id} className="rounded-lg border bg-card">
            <div className="flex flex-wrap items-start justify-between gap-2 border-b px-3 py-2.5">
              <div className="min-w-0">
                <p className="text-[13px] font-medium">{g.title}</p>
                <p className="text-[12px] text-muted-foreground">{g.why}</p>
              </div>
              {g.missing === 0 ? <Badge tone="green">In place</Badge> : g.last?.status === "queued" ? <Badge tone="blue">Adding</Badge> : g.last?.status === "done" ? <Badge tone="blue">Checking</Badge> : <Badge>{plural(g.missing, "to add", "to add")}</Badge>}
            </div>
            <ul className="divide-y text-[12px]">
              {g.records.map((r, i) => (
                <li key={i} className="grid items-center gap-x-3 gap-y-0.5 px-3 py-2 sm:grid-cols-[1.25rem_3.5rem_minmax(0,14rem)_1fr]">
                  {r.status === "in_place" ? <Icon name="check" className="size-3.5 text-green-600 dark:text-green-400" /> : <span aria-hidden="true" className="size-3 rounded-full border" />}
                  <span className="font-mono font-medium">{r.type}</span>
                  <code className="font-mono break-all">{r.name}</code>
                  <span className="flex items-center gap-1">
                    <code className="font-mono break-all text-muted-foreground">{r.priority != null ? `${r.priority} ` : ""}{r.value}</code>
                    {r.status !== "in_place" && <CopyText text={r.value} icon onCopy={() => used("copied")} />}
                  </span>
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>

      {dns.missing === 0 ? (
        <p className="text-[13px]"><Status tone="green">Every record is in place.</Status></p>
      ) : !canManage ? (
        <p className="text-[13px] text-muted-foreground">An owner or admin can add {dns.missing === 1 ? "it" : "them"} in one go.</p>
      ) : (
        <>
          <div role="tablist" className="flex flex-wrap gap-1.5">
            {(["oneclick", "developer", "ai", "manual"] as const).map((m) => (
              <button
                key={m}
                role="tab"
                aria-selected={method === m}
                onClick={() => setMethod(m)}
                className={cn("inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-[13px]", method === m ? "border-brand/50 bg-brand/10 font-medium" : "hover:bg-accent")}
              >
                {m === "oneclick" ? <><Icon name="check" className="size-3.5" />Add them all<Badge tone="green">Quickest</Badge></> : m === "developer" ? <><Icon name="inbox" className="size-3.5" />Send to a developer</> : m === "ai" ? <><Icon name="ai" className="size-3.5" />Ask your AI coding tool</> : <><Icon name="code" className="size-3.5" />Do it myself</>}
              </button>
            ))}
          </div>

          {method === "oneclick" && (
            <Panel>
              {progress ? (
                <p className="text-[13px]"><Status tone="blue">{progress}</Status></p>
              ) : provider ? (
                <>
                  <p className="text-[13px]">Your domain's DNS is at {provider}. It shows you the same {plural(dns.missing, "record")} and asks you to approve {pending.length === 1 ? "them once" : `each group (${pending.length} quick approvals, one after another)`}. Outflare gets no access to your account.</p>
                  <Button onClick={approveAll}><Icon name="check" className="size-3.5" /> Approve {plural(dns.missing, "record")} in {provider}</Button>
                </>
              ) : connection ? (
                <>
                  <p className="text-[13px]">Outflare adds {dns.missing === 1 ? "the missing record" : dns.missing === 2 ? "both missing records" : `all ${dns.missing} missing records`} in Cloudflare in one go, then checks for them every minute. It only adds records, each marked "Added by Outflare", and never changes or deletes one.</p>
                  <div className="flex flex-wrap items-center gap-3">
                    <Button disabled={busy || queued} onClick={addAll}><Icon name="check" className="size-3.5" /> {queued ? "Adding…" : failed ? "Try again" : `Add ${plural(dns.missing, "record")}`}</Button>
                    {failed && <span className="text-[13px] text-destructive">{failed.last?.error}</span>}
                  </div>
                </>
              ) : (
                <>
                  <p className="text-[13px] text-muted-foreground">
                    Is your domain on Cloudflare? Connect it once and Outflare adds {dns.missing === 1 ? "the record" : dns.missing === 2 ? "both records" : `all ${dns.missing} records`} with one button. Anywhere else, send them to whoever manages your DNS, or add them yourself: the other tabs have everything.
                  </p>
                  {cloudflare && (
                    <div className="flex flex-wrap items-center gap-2">
                      {authOption(cloudflare, "oauth2")?.available ? (
                        <>
                          <Button onClick={() => signIn(cloudflare)}><ConnectorLogo id="cloudflare" size="sm" /> Sign in with Cloudflare</Button>
                          <Button variant="ghost" onClick={() => setConnecting(true)}>Use an API token instead</Button>
                        </>
                      ) : (
                        <Button onClick={() => setConnecting(true)}><ConnectorLogo id="cloudflare" size="sm" /> Connect Cloudflare</Button>
                      )}
                    </div>
                  )}
                  {connecting && cloudflare && (
                    <ConnectDialog ws={ws} def={cloudflare} onSignIn={() => signIn(cloudflare)} onClose={() => setConnecting(false)} onDone={() => { setConnecting(false); track("ui.dns_one_click_used", { where: "brand_dns", how: "connected" }); void load(); }} />
                  )}
                </>
              )}
            </Panel>
          )}
          {method === "developer" && <DeveloperPanel message={dns.developer} used={used} />}
          {method === "ai" && <AIPromptPanel prompt={dns.ai_prompt} used={used} intro="Paste this into Claude Code, Cursor or whichever tool can reach your DNS host. It adds every record and leaves the rest alone." />}
          {method === "manual" && (
            <Panel>
              <p className="text-[13px] text-muted-foreground">Add each record marked with an empty circle above where your domain's DNS is managed, next to the records already there. Outflare checks for them on its own and ticks each one off.</p>
              <CopyText text={dns.ai_prompt.split("\n\nRules:")[0]} label="Copy all records" onCopy={() => used("copied_all")} />
            </Panel>
          )}
        </>
      )}
    </div>
  );
}
