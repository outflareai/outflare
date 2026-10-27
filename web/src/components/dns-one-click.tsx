import { useEffect, useState, type ReactNode } from "react";
import type { components, Connector } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Status, toast } from "../ui";
import { Icon } from "../icons";
import { plural } from "../format";
import { ConnectorLogo } from "@/components/connector-logo";
import { Panel } from "@/components/install-methods";
import { authOption, ConnectDialog, startConnectorOAuth, type ReturnPage } from "@/pages/Connectors";

// One click for any DNS record Outflare asks for (a sending domain's, a
// site's proof, a tracking domain's CNAME). Where the domain's DNS
// provider supports it, the person approves exactly these records once on
// the provider's own screen (Domain Connect) and Outflare keeps no access.
// Otherwise it goes through the workspace's own Cloudflare connection:
// see exactly which records go in, and add them with one button.

export type DNSRow = { type: string; name: string; value: string; priority?: number | null };
export type DNSRun = { status: "queued" | "done" | "failed"; added?: number; existing?: number; zone?: string; error?: string; provider?: string };
type Approval = components["schemas"]["DNSApproval"];
// What the records belong to, for the one-off approval at the provider.
export type DNSApprovalFor = { kind: "seo_site" | "tracking_domain" | "email_domain"; id: string };

async function approvalLink(ws: string, f: DNSApprovalFor, ret: string) {
  const query = { return: ret };
  switch (f.kind) {
    case "seo_site":
      return api.GET("/v1/workspaces/{ws}/seo-sites/{site}/dns/approval", { params: { path: { ws, site: f.id }, query } });
    case "tracking_domain":
      return api.GET("/v1/workspaces/{ws}/tracking-domains/{id}/dns/approval", { params: { path: { ws, id: f.id }, query } });
    case "email_domain":
      return api.GET("/v1/workspaces/{ws}/email-provider-domains/{id}/dns/approval", { params: { path: { ws, id: f.id }, query } });
  }
}

async function reportApproval(ws: string, f: DNSApprovalFor, body: { error?: string; provider?: string }) {
  switch (f.kind) {
    case "seo_site":
      return api.POST("/v1/workspaces/{ws}/seo-sites/{site}/dns/approved", { params: { path: { ws, site: f.id } }, body });
    case "tracking_domain":
      return api.POST("/v1/workspaces/{ws}/tracking-domains/{id}/dns/approved", { params: { path: { ws, id: f.id } }, body });
    case "email_domain":
      return api.POST("/v1/workspaces/{ws}/email-provider-domains/{id}/dns/approved", { params: { path: { ws, id: f.id } }, body });
  }
}

// Back from the provider: it adds state (dc.<id>) and, when it didn't
// happen, error. Taken once, and removed from the address.
const providerKey = "outflare.dns_approval_provider";
function takeApproval(id: string): { error?: string; provider?: string } | null {
  const q = new URLSearchParams(location.search);
  if (q.get("state") !== `dc.${id}`) return null;
  const error = q.get("error") ?? undefined;
  for (const k of ["state", "error", "error_description"]) q.delete(k);
  history.replaceState(null, "", location.pathname + (q.size ? `?${q}` : ""));
  let provider: string | undefined;
  try {
    provider = sessionStorage.getItem(providerKey) ?? undefined;
    sessionStorage.removeItem(providerKey);
  } catch {
    // The provider's name is only for the record.
  }
  return { error, provider };
}

// Where a sign-in started, kept in the tab while the browser is away.
const returnKey = "outflare.dns_after_connect";

function remember(item: string) {
  try {
    sessionStorage.setItem(returnKey, JSON.stringify({ path: location.pathname + location.search, item }));
  } catch {
    // Without it the sign-in still works; it lands on the page's list.
  }
}

// restoreDNSReturn runs before the app reads the URL: back from a sign-in
// that started at a DNS record, it puts the browser on the exact page it
// left (a provider, a site), keeping the sign-in's outcome in the query.
export function restoreDNSReturn() {
  try {
    const q = new URLSearchParams(location.search);
    const raw = sessionStorage.getItem(returnKey);
    if (!raw || (!q.has("integration") && !q.has("connect_error"))) return;
    const { path } = JSON.parse(raw) as { path: string };
    if (!path.startsWith("/")) return;
    const back = new URL(path, location.origin);
    q.forEach((v, k) => back.searchParams.append(k, v));
    history.replaceState(null, "", back.pathname + back.search);
  } catch {
    // Stay where the sign-in landed.
  }
}

// takeDNSFocus gives the item a sign-in started from (a domain or site
// id), once, so the page can open it.
export function takeDNSFocus(): string | null {
  try {
    const raw = sessionStorage.getItem(returnKey);
    sessionStorage.removeItem(returnKey);
    return raw ? (JSON.parse(raw) as { item: string }).item : null;
  } catch {
    return null;
  }
}

export function DNSOneClick({ ws, page, item, approval, connections, records, run, canManage, intro, after, onAdd, reload, used }: {
  ws: string;
  // Where the records go, for approving them once at the DNS provider.
  approval?: DNSApprovalFor;
  // The app page to come back to after signing in, and what to open there.
  page: ReturnPage;
  item: string;
  connections: string[];
  // The records the server adds: the ones not in place yet.
  records: DNSRow[];
  run: DNSRun | null | undefined;
  canManage: boolean;
  intro: ReactNode;
  // What happens once they're in, such as "checks every minute".
  after: ReactNode;
  onAdd: (integration: string) => Promise<{ error?: unknown; held?: boolean }>;
  reload: () => void;
  used: (how: string) => void;
}) {
  const [def, setDef] = useState<Connector | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [busy, setBusy] = useState(false);
  const [appr, setAppr] = useState<Approval | null>(null);
  const pending = records.length > 0 && run?.status !== "queued";
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/connectors", { params: { path: { ws } } }).then(({ data }) => setDef(data?.data?.find((c) => c.id === "cloudflare") ?? null));
  }, [ws]);
  // Back from approving at the provider: say so, and start the checks.
  useEffect(() => {
    if (!approval || !canManage) return;
    const back = takeApproval(approval.id);
    if (!back) return;
    used(back.error ? "approval_cancelled" : "approved");
    reportApproval(ws, approval, back).then(({ error }) => {
      if (error) return toast(errorMessage(error), "error");
      if (back.error) toast(back.error === "access_denied" ? "Nothing was changed: the records weren't approved." : "Your DNS provider couldn't add the records. Try again, or add them another way.", back.error === "access_denied" ? undefined : "error");
      reload();
    });
  }, [ws, approval?.kind, approval?.id, canManage]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!approval || !canManage || !pending) return setAppr(null);
    let live = true;
    approvalLink(ws, approval, location.pathname + location.search).then(({ data }) => live && setAppr(data ?? null));
    return () => {
      live = false;
    };
  }, [ws, approval?.kind, approval?.id, canManage, pending]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!canManage) return <Panel><p className="text-[13px] text-muted-foreground">An owner or admin can add {records.length === 1 ? "it" : "them"} in one click.</p></Panel>;
  const these = records.length === 1 ? "this record" : `these ${records.length} records`;
  const list = (
    <ul className="divide-y rounded-md border bg-background text-[12px]">
      {records.map((r, i) => (
        <li key={i} className="grid gap-x-3 gap-y-0.5 px-3 py-2 sm:grid-cols-[3.5rem_minmax(0,14rem)_1fr]">
          <span className="font-mono font-medium">{r.type}</span>
          <code className="break-all font-mono">{r.name}</code>
          <code className="break-all font-mono text-muted-foreground">{r.type === "MX" ? `${r.priority ?? 10} ` : ""}{r.value}</code>
        </li>
      ))}
    </ul>
  );
  if (run?.provider && run.status === "done") {
    return (
      <Panel>
        <Status tone="green">Approved in {run.provider}</Status>
        <p className="text-[12px] text-muted-foreground">{after}</p>
      </Panel>
    );
  }
  if (appr?.available && appr.url && pending) {
    const url = appr.url;
    const go = () => {
      remember(item);
      try {
        sessionStorage.setItem(providerKey, appr.provider ?? "");
      } catch {
        // Only for the record.
      }
      used("approval");
      location.assign(url);
    };
    return (
      <Panel>
        <p className="text-[13px]">Add {these} at {appr.provider}:</p>
        {list}
        <p className="text-[12px] text-muted-foreground">{appr.provider} shows you the same records and asks you to approve them once. Outflare gets no access to your account. {after}</p>
        <div className="flex flex-wrap items-center gap-2">
          <Button onClick={go}><Icon name="check" className="size-3.5" /> Approve in {appr.provider}</Button>
        </div>
      </Panel>
    );
  }
  const signIn = (def: Connector) => {
    remember(item);
    used("sign_in");
    void startConnectorOAuth(ws, def, page);
  };
  const integration = connections[0];
  if (!integration) {
    const oauth = !!def && !!authOption(def, "oauth2")?.available;
    return (
      <Panel>
        <p className="text-[13px] text-muted-foreground">{intro} You see every record before anything is added. Outflare only adds records, each marked "Added by Outflare", and never changes or deletes one.</p>
        <div className="flex flex-wrap items-center gap-2">
          {oauth ? (
            <>
              <Button onClick={() => signIn(def)}><ConnectorLogo id="cloudflare" size="sm" /> Sign in with Cloudflare</Button>
              <Button variant="ghost" onClick={() => setConnecting(true)}>Use an API token instead</Button>
            </>
          ) : (
            <Button disabled={!def} onClick={() => setConnecting(true)}><ConnectorLogo id="cloudflare" size="sm" /> Connect Cloudflare</Button>
          )}
        </div>
        {connecting && def && <ConnectDialog ws={ws} def={def} onSignIn={() => signIn(def)} onClose={() => setConnecting(false)} onDone={() => { setConnecting(false); used("connected"); reload(); }} />}
      </Panel>
    );
  }
  async function add() {
    setBusy(true);
    const { error, held } = await onAdd(integration);
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    used("added");
    if (held) toast("Waiting for approval in a mission.");
    reload();
  }
  const queued = run?.status === "queued";
  const ready = records.length > 0 && !queued && run?.status !== "done";
  return (
    <Panel>
      {ready && (
        <>
          <p className="text-[13px]">Outflare will add {these} in Cloudflare:</p>
          {list}
          <p className="text-[12px] text-muted-foreground">{after}</p>
        </>
      )}
      <div className="flex flex-wrap items-center gap-3">
        {(ready || queued) && <Button disabled={busy || queued} onClick={add}><Icon name="check" className="size-3.5" /> {queued ? "Adding…" : run?.status === "failed" ? "Try again" : `Add ${these}`}</Button>}
        {run?.status === "done" && <Status tone="green">{run.added === undefined ? "Added in Cloudflare" : run.added ? `Added ${plural(run.added, "record")}${run.zone ? ` to ${run.zone}` : ""}` : "Already there"}{run.existing && run.added ? `, ${run.existing} already there` : ""}</Status>}
        {run?.status === "failed" && <span className="text-[13px] text-destructive">{run.error}</span>}
      </div>
    </Panel>
  );
}

// heldByMission says whether a 202 was held for approval in a mission.
export const heldByMission = (data: unknown) => !!data && typeof data === "object" && "mission_id" in data;
