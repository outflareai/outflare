import { useEffect, useState } from "react";
import type { InboundAddress } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, CopyButton, ErrorText, field, Loading, toast } from "../ui";
import { Icon } from "../icons";
import { ago } from "../format";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { useBrand } from "@/lib/brand";
import type { Tab } from "../App";

// Forward your support address: each brand gets a support address at the
// inbound domain that reads like a support inbox (northpeak-support@...).
// The business forwards its own support@ there; replies go out through
// its email provider from the address customers wrote to. Part of
// Outflare Cloud; a self-hosted server says so.

type Addresses = { available: boolean; domain: string | null; purposes: string[]; data: InboundAddress[] };
type Mail = "google" | "microsoft" | "group" | "other";

const steps: Record<Mail, { label: string; steps: (addr: string) => string[] }> = {
  google: {
    label: "Google Workspace",
    steps: (a) => [
      "In the Google Admin console, open Apps, Google Workspace, Gmail, Routing.",
      `Add a routing rule for your support mailbox: add more recipients, and enter ${a}.`,
      "Save. New mail to your support address now reaches Outflare too.",
    ],
  },
  group: {
    label: "Google Group",
    steps: (a) => [
      "Open your support group in Google Groups, then Members.",
      `Add ${a} as a member, with email delivery set to each email.`,
      "Outflare reads who wrote from the group's Reply-To, so replies reach the customer.",
    ],
  },
  microsoft: {
    label: "Microsoft 365",
    steps: (a) => [
      "In the Exchange admin center, open Recipients, Mailboxes, and your support mailbox.",
      `Under Mail flow settings, Email forwarding, forward to ${a} and keep a copy in the mailbox.`,
      "Save. Allow external forwarding in the outbound spam policy if Microsoft blocks it.",
    ],
  },
  other: {
    label: "Gmail or other",
    steps: (a) => [
      "In your mail settings, open Forwarding.",
      `Add ${a} as a forwarding address. Gmail sends a confirmation: it appears here for you to confirm.`,
      "Turn forwarding on, keeping a copy in your mailbox.",
    ],
  },
};

export function SupportForwarding({ ws, canSetUp, go, onChanged }: { ws: string; canSetUp: boolean; go: (tab: Tab) => void; onChanged?: () => void }) {
  const { active } = useBrand(ws);
  const [list, setList] = useState<Addresses | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [providers, setProviders] = useState<number | null>(null);

  async function load() {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/inbound-addresses", { params: { path: { ws } } });
    if (error) return setError(errorMessage(error));
    setList(data as Addresses);
  }
  useEffect(() => {
    load();
    api.GET("/v1/workspaces/{ws}/email-providers", { params: { path: { ws } } }).then(({ data }) => setProviders((data as { data?: unknown[] } | undefined)?.data?.length ?? 0));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!list) return <Loading />;
  if (!list.available || !list.purposes.includes("support")) {
    return (
      <div className="rounded-lg border p-4 text-[13px]">
        <p className="font-medium">Forward your support address</p>
        <p className="mt-1 text-muted-foreground">
          Forwarding to a support address is part of Outflare Cloud. On this server, connect the support mailbox itself above.
        </p>
      </div>
    );
  }

  // One address for the workspace, and one per brand.
  const rows: { key: string; label: string; brand?: string; addr?: InboundAddress }[] = [
    { key: "", label: active.length ? "Every brand" : "Your support address", addr: list.data.find((a) => a.purpose === "support" && !a.brand_id) },
    ...active.map((b) => ({ key: b.id, label: b.name, brand: b.handle, addr: list.data.find((a) => a.purpose === "support" && a.brand_id === b.id) })),
  ];

  async function make(brand?: string, name?: string) {
    setBusy(brand ?? "-");
    const { error } = await api.POST("/v1/workspaces/{ws}/inbound-addresses", {
      params: { path: { ws } },
      body: { purpose: "support", ...(brand ? { brand } : {}), ...(name ? { name } : {}) } as never,
    });
    setBusy("");
    if (error) {
      toast(errorMessage(error), "error");
      return false;
    }
    track(name ? "ui.support_address_renamed" : "ui.support_address_made", { brand: !!brand });
    await load();
    onChanged?.();
    return true;
  }

  return (
    <div className="space-y-3 rounded-lg border p-4">
      <div>
        <p className="text-[13px] font-medium">Forward your support address</p>
        <p className="text-xs text-muted-foreground">
          Keep support@ where it is and forward it here: every email becomes a conversation. Replies go out from the address customers wrote to, through your email provider.
        </p>
      </div>
      {providers === 0 && (
        <p className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs">
          <Icon name="lifecycle" className="mt-0.5 size-3.5 shrink-0 text-amber-600" />
          <span className="flex-1">To reply to forwarded mail, connect your email provider (Amazon SES, Postmark or Resend) and verify your support address's domain.</span>
          <button type="button" className="shrink-0 underline" onClick={() => go("providers")}>Set it up</button>
        </p>
      )}
      <ul className="divide-y rounded-md border">
        {rows.map((r) => (
          <AddressRow key={r.key} row={r} canSetUp={canSetUp} busy={busy === (r.brand ?? "-")} onMake={() => make(r.brand)} onRename={(n) => make(r.brand, n)} />
        ))}
      </ul>
    </div>
  );
}

function AddressRow({
  row,
  canSetUp,
  busy,
  onMake,
  onRename,
}: {
  row: { label: string; brand?: string; addr?: InboundAddress };
  canSetUp: boolean;
  busy: boolean;
  onMake: () => void;
  onRename: (name: string) => Promise<boolean>;
}) {
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState("");
  const [how, setHow] = useState<Mail | null>(null);
  const a = row.addr;
  const [local, domain] = a ? a.email.split("@") : ["", ""];
  return (
    <li className="space-y-2 px-3 py-3 text-[13px]">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div className="min-w-0 basis-full sm:basis-auto sm:flex-1">
          <p className="text-xs text-muted-foreground">{row.label}</p>
          {a ? (
            editing ? (
              <form
                className="mt-1 flex flex-wrap items-center gap-1"
                onSubmit={async (e) => {
                  e.preventDefault();
                  if (await onRename(name.trim().toLowerCase())) setEditing(false);
                }}
              >
                <input aria-label="Address name" autoFocus className={cn(field, "w-48")} value={name} onChange={(e) => setName(e.target.value)} maxLength={64} />
                <span className="text-muted-foreground">@{domain}</span>
                <Button disabled={!name.trim() || busy}>Save</Button>
                <Button type="button" variant="ghost" onClick={() => setEditing(false)}>Cancel</Button>
              </form>
            ) : (
              <p className="mt-0.5 flex min-w-0 items-center gap-2">
                <span className="min-w-0 font-mono text-[12.5px]">{local}@<wbr />{domain}</span>
                <CopyButton text={a.email} />
              </p>
            )
          ) : (
            <p className="mt-0.5 text-muted-foreground">No support address yet</p>
          )}
        </div>
        {a && !editing && (
          <span className="flex-1 text-xs text-muted-foreground sm:flex-none">{a.last_email_at ? `Last email ${ago(a.last_email_at)}` : "No email yet"}</span>
        )}
        {canSetUp && !a && <Button disabled={busy} onClick={onMake}>{busy ? "Making…" : "Make address"}</Button>}
        {canSetUp && a && !editing && (
          <Button variant="ghost" onClick={() => { setName(local); setEditing(true); }}>Rename</Button>
        )}
      </div>
      {a?.confirmation && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-blue-500/30 bg-blue-500/5 px-3 py-2 text-xs">
          <Icon name="check" className="size-3.5 text-blue-600" />
          <span className="min-w-0 flex-1">
            Gmail asks to confirm forwarding from <strong>{a.confirmation.from}</strong>
            {a.confirmation.code ? <> (code <span className="font-mono">{a.confirmation.code}</span>)</> : null}.
          </span>
          {a.confirmation.url && (
            <a className="font-medium underline" href={a.confirmation.url} target="_blank" rel="noreferrer noopener">Confirm in Gmail</a>
          )}
        </div>
      )}
      {a && (
        <div className="text-xs">
          <div className="flex flex-wrap items-center gap-1" role="group" aria-label="How to forward">
            <span className="mr-1 text-muted-foreground">Set up forwarding in</span>
            {(Object.keys(steps) as Mail[]).map((k) => (
              <button
                key={k}
                type="button"
                aria-pressed={how === k}
                onClick={() => setHow(how === k ? null : k)}
                className={cn("rounded border px-2 py-0.5", how === k ? "border-foreground/40 bg-accent font-medium" : "text-muted-foreground hover:text-foreground")}
              >
                {steps[k].label}
              </button>
            ))}
          </div>
          {how && (
            <ol className="mt-2 list-decimal space-y-1 pl-5 text-muted-foreground">
              {steps[how].steps(a.email).map((s) => <li key={s}>{s}</li>)}
            </ol>
          )}
        </div>
      )}
    </li>
  );
}
