import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, CopyButton, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { ago, stamp } from "../format";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";

// Developer setup: one brand's live checklist of what its developer sends
// (sign-ups, orders, payments, refunds, the website and server copies,
// refunds reaching ad networks), each with what it found and what to
// send to fix it, the refunds whose payment Outflare doesn't have, to
// link by hand, and a merge for payments saved twice.

type Setup = Schemas["DeveloperSetup"];
type Check = Schemas["DeveloperSetupCheck"];
type Transaction = Schemas["Transaction"];

const tones: Record<Check["status"], { label: string; tone: "green" | "amber" | "grey" }> = {
  ok: { label: "OK", tone: "green" },
  warning: { label: "Check", tone: "amber" },
  missing: { label: "Not yet", tone: "grey" },
};

export function DeveloperSetup({ ws, canManage, tabs }: { ws: string; canManage: boolean; tabs: ReactNode }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("setup", "Developer setup"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Developer setup"
        description="A live check of what this brand's developer sends: sign-ups, orders, payments and refunds, and whether the website and server agree. Anything that's off says what to send instead."
      />
      {tabs}
      {brand ? (
        <SetupFor key={brand.id} brand={brand.id} canManage={canManage} />
      ) : (
        <Empty title="Pick a brand">
          Each brand is set up on its own. Pick {active.length > 1 ? `one of your ${active.length} brands` : "your brand"} in the brand switcher.
        </Empty>
      )}
    </div>
  );
}

function SetupFor({ brand, canManage }: { brand: string; canManage: boolean }) {
  const [setup, setSetup] = useState<Setup | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/developer-setup", { params: { path: { brand } } });
    if (!data) return setError(errorMessage(error));
    setError("");
    setSetup(data);
  }, [brand]);
  useEffect(() => {
    void load();
    // Live: look again every 30 seconds while the page is open.
    const t = setInterval(() => void load(), 30000);
    return () => clearInterval(t);
  }, [load]);

  if (!setup) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const open = setup.checks.filter((c) => c.status !== "ok").length;
  return (
    <div className="space-y-6">
      <ErrorText>{error}</ErrorText>
      <Card
        title={open ? `${open} of ${setup.checks.length} to look at` : "Everything checks out"}
        description="Refreshes every 30 seconds. Counts are the last 30 days (the website's copies, 7 days)."
      >
        <ul className="divide-y">
          {setup.checks.map((c) => (
            <CheckRow key={c.key} check={c} brand={brand} canManage={canManage} onChanged={load} />
          ))}
        </ul>
      </Card>
      {setup.unmatched_refunds.length > 0 && (
        <Card
          title="Refunds to match"
          description="These refunds name a payment Outflare doesn't have, so they don't count yet. Enter the payment's order id (or its messageId, or Outflare's transaction id) to link each one: it then comes off that payment's revenue and its conversion in ad networks."
        >
          <ul className="divide-y">
            {setup.unmatched_refunds.map((t) => (
              <RefundRow key={t.id} brand={brand} refund={t} canManage={canManage} onLinked={load} />
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}

function CheckRow({ check: c, brand, canManage, onChanged }: { check: Check; brand: string; canManage: boolean; onChanged: () => void }) {
  const tone = tones[c.status];
  const [busy, setBusy] = useState(false);
  // Payments saved twice are merged here: nothing changes in what the
  // developer sends.
  async function merge() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/developer-setup/merge-duplicates", { params: { path: { brand } } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    toast(data.merged === 1 ? "1 payment merged." : `${data.merged.toLocaleString()} payments merged.`);
    onChanged();
  }
  return (
    <li className="space-y-1 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={tone.tone}>{tone.label}</Badge>
        <span className="text-[13px] font-medium">{c.title}</span>
        <span className="ml-auto text-[12px] text-muted-foreground" title={c.last_at ? stamp(c.last_at) : undefined}>
          {c.last_at ? `last ${ago(c.last_at)}` : "none yet"}
          {c.count ? `, ${c.count.toLocaleString()} recently` : ""}
        </span>
      </div>
      {c.detail && <p className="text-[12px] text-muted-foreground">{c.detail}</p>}
      {c.key === "duplicates" && c.status !== "ok" ? (
        canManage && (
          <div className="flex flex-wrap items-center gap-2">
            <p className="min-w-0 flex-1 text-[12px] text-muted-foreground">{c.fix}</p>
            <Button disabled={busy} onClick={merge}>
              Merge them
            </Button>
          </div>
        )
      ) : (
        c.status !== "ok" &&
        c.fix && (
          <div className="flex items-start gap-2 rounded-md bg-muted/50 p-2">
            <p className="min-w-0 flex-1 font-mono text-[12px] break-words">{c.fix}</p>
            <CopyButton text={c.fix} />
          </div>
        )
      )}
    </li>
  );
}

function RefundRow({ brand, refund: t, canManage, onLinked }: { brand: string; refund: Transaction; canManage: boolean; onLinked: () => void }) {
  const [payment, setPayment] = useState("");
  const [busy, setBusy] = useState(false);
  async function link() {
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/transactions/{transaction}/refund-of", {
      params: { path: { brand, transaction: t.id } },
      body: { payment: payment.trim() },
    });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Refund linked to its payment.");
    onLinked();
  }
  const amount = (t.amount_minor / 100).toLocaleString(undefined, { style: "currency", currency: t.currency });
  return (
    <li className="flex flex-wrap items-center gap-3 py-2">
      <div className="min-w-0 flex-1">
        <div className="text-[13px]">
          {t.amount_minor ? amount : "Full refund"} <span className="text-muted-foreground">· {t.source_id}</span>
        </div>
        <div className="text-[12px] text-muted-foreground">
          {stamp(t.occurred_at)}
          {t.refund_of ? `, named payment ${t.refund_of}` : ", named no payment"}
        </div>
      </div>
      {canManage && (
        <span className="flex gap-2">
          <input aria-label="Payment's order id" className={`${field} w-48`} placeholder="Payment's order id" value={payment} onChange={(e) => setPayment(e.target.value)} />
          <Button disabled={busy || !payment.trim()} onClick={link}>Link</Button>
        </span>
      )}
    </li>
  );
}
