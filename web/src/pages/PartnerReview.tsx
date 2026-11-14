// Partners → Review (plans/ad-tracker.md, B7d): the program's fraud review
// queue. A credited conversion with a partner fraud signal is flagged, and
// its rewards wait here, even past their hold, until someone clears the
// flag (they go on) or confirms it (pending rewards are rejected, and the
// partner can be suspended).

import { useCallback, useEffect, useRef, useState } from "react";
import type { PartnerProgram, Schemas } from "@outflare/sdk";
import { pickedBrandId } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { ago } from "../format";
import { Badge, Button, Empty, ErrorText, field, Loading, ask, toast } from "../ui";
import { money } from "./Partners";

type Flag = Schemas["PartnerFlag"];
type Status = Flag["status"];

// What each signal means, in a reviewer's words.
export const signalText: Record<Flag["signal"], { label: string; help: string }> = {
  shared_identity: { label: "Self-referral", help: "The partner and the customer share an email address, phone number or user id." },
  payout_matches_customer: { label: "Payout matches customer", help: "The partner's payout details contain the customer's email address." },
  repeat_click: { label: "Repeat clicks", help: "Many clicks from one address on the partner's links within a day of the customer's click: cookie stuffing." },
  many_signups_one_ip: { label: "Sign-ups from one address", help: "Several of the partner's referrals in 30 days came through clicks from one address." },
  fast_conversion: { label: "Instant conversion", help: "The conversion came seconds after the click, faster than a person signs up or pays." },
  refund_rate: { label: "High refund rate", help: "Many of the partner's sales in the last 90 days were refunded." },
  datacenter_network: { label: "Hosting network click", help: "The click that brought the customer came from a cloud or hosting provider's network." },
  filter_list: { label: "On your filter list", help: "The click that brought the customer matched one of your click filters." },
  honeypot: { label: "Bot on lander", help: "The click's lander followed the hidden link only bots follow." },
  ip_reputation: { label: "Risky address", help: "Your IP lookup provider scored the click's address as high risk." },
};

// detailText puts a flag's counts in words.
function detailText(f: Flag): string {
  const d = f.detail as Record<string, number | string>;
  switch (f.signal) {
    case "repeat_click": return `${d.clicks} clicks (flagged from ${d.threshold})`;
    case "many_signups_one_ip": return `${d.referrals} referrals (flagged from ${d.threshold})`;
    case "fast_conversion": return `${d.seconds}s after the click`;
    case "refund_rate": return `${d.refunds} of ${d.sales} sales refunded`;
    case "shared_identity": return `${d.identities} shared`;
    default: return d.from === "click" ? "from the tracker click" : "";
  }
}

export function PartnerReview({ program, canManage, onOpenPartner }: { program: PartnerProgram; canManage: boolean; onOpenPartner: (id: string) => void }) {
  const [status, setStatus] = useState<Status>("open");
  const [list, setList] = useState<Flag[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const seq = useRef(0);
  const load = useCallback(async () => {
    const n = ++seq.current;
    const { data, error } = await api.GET("/v1/brands/{brand}/partner-flags", { params: { path: { brand: pickedBrandId() }, query: { program: program.id, status } } });
    if (n !== seq.current) return;
    setError(error ? errorMessage(error) : "");
    setList(data?.data ?? []);
  }, [program.id, status]);
  useEffect(() => {
    setList(null);
    void load();
  }, [load]);

  const clear = async (f: Flag) => {
    const r = await ask({
      title: `Clear "${signalText[f.signal].label}"?`,
      body: "The signal was nothing. Once no other flag on this conversion is open, its rewards go on as usual.",
      input: { label: "Why (optional)", optional: true, maxLength: 2000 },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Clear flag", value: "ok", variant: "primary" }],
    });
    if (!r) return;
    setBusy(f.id);
    const { error } = await api.POST("/v1/brands/{brand}/partner-flags/{id}/clear", { params: { path: { brand: pickedBrandId(), id: f.id } }, body: { note: r.value } });
    setBusy("");
    if (error) return toast(errorMessage(error), "error");
    toast("Flag cleared", "ok");
    void load();
  };
  const confirm = async (f: Flag) => {
    const r = await ask({
      title: "Confirm fraud?",
      body: `Every open flag on this ${f.conversion_type} is confirmed and its pending rewards are rejected${f.held_cents ? ` (${money(f.held_cents, f.currency)} for the partner)` : ""}. Approved rewards stay; correct them with an adjustment.`,
      input: { label: "Why (the partner sees it on the rejected reward)", optional: true, maxLength: 2000 },
      choices: [
        { label: "Cancel", value: "cancel", variant: "ghost" },
        { label: "Confirm and suspend partner", value: "suspend", variant: "danger-solid" },
        { label: "Confirm", value: "ok", variant: "danger-solid" },
      ],
    });
    if (!r) return;
    setBusy(f.id);
    const { error } = await api.POST("/v1/brands/{brand}/partner-flags/{id}/confirm", {
      params: { path: { brand: pickedBrandId(), id: f.id } },
      body: { note: r.value, suspend_partner: r.choice === "suspend" },
    });
    setBusy("");
    if (error) return toast(errorMessage(error), "error");
    toast(r.choice === "suspend" ? "Fraud confirmed and partner suspended" : "Fraud confirmed: pending rewards rejected", "ok");
    void load();
  };

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="Flag status" className={field} value={status} onChange={(e) => setStatus(e.target.value as Status)}>
          <option value="open">Waiting for review</option>
          <option value="cleared">Cleared</option>
          <option value="confirmed">Confirmed</option>
        </select>
        <span className="text-[12px] text-muted-foreground">
          A flagged conversion's rewards wait here, even past their hold, until you clear or confirm it.
        </span>
      </div>
      <ErrorText>{error}</ErrorText>
      {list === null ? (
        <Loading />
      ) : list.length === 0 ? (
        <Empty title={status === "open" ? "Nothing to review" : `No ${status} flags`}>
          {status === "open" ? "Credited conversions are checked for self-referrals, cookie stuffing, instant conversions, high refund rates and suspect clicks." : "Flags you resolve show here."}
        </Empty>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-[13px]" data-testid="partner-flags">
            <thead className="text-left text-[12px] text-muted-foreground">
              <tr>
                <th className="py-1 pr-3 font-normal">Signal</th>
                <th className="py-1 pr-3 font-normal">Partner</th>
                <th className="py-1 pr-3 font-normal">Conversion</th>
                <th className="py-1 pr-3 font-normal">Held</th>
                <th className="py-1 pr-3 font-normal">Flagged</th>
                <th className="py-1" />
              </tr>
            </thead>
            <tbody>
              {list.map((f) => (
                <tr key={f.id} className="border-t align-top">
                  <td className="py-2 pr-3">
                    <div title={signalText[f.signal].help}>{signalText[f.signal].label}</div>
                    <div className="text-[12px] text-muted-foreground">{detailText(f)}</div>
                    {f.note && <div className="text-[12px] text-muted-foreground">“{f.note}”</div>}
                  </td>
                  <td className="py-2 pr-3">
                    <button type="button" className="underline underline-offset-2" onClick={() => onOpenPartner(f.partner_id)}>{f.partner_code}</button>
                  </td>
                  <td className="py-2 pr-3">{f.conversion_type}{f.amount_cents ? ` · ${money(f.amount_cents, f.currency)}` : ""}</td>
                  <td className="py-2 pr-3">{f.held_cents ? money(f.held_cents, f.currency) : "—"}</td>
                  <td className="py-2 pr-3 text-muted-foreground">{ago(f.created_at)}</td>
                  <td className="py-2 text-right whitespace-nowrap">
                    {f.status === "open" && canManage ? (
                      <span className="inline-flex gap-1">
                        <Button type="button" variant="ghost" disabled={busy === f.id} onClick={() => void clear(f)}>Clear</Button>
                        <Button type="button" variant="danger" disabled={busy === f.id} onClick={() => void confirm(f)}>Confirm fraud</Button>
                      </span>
                    ) : (
                      f.status !== "open" && <Badge tone={f.status === "confirmed" ? "red" : "grey"}>{f.status === "confirmed" ? "Confirmed" : "Cleared"}</Badge>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
