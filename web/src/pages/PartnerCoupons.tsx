import { useState, type FormEvent } from "react";
import { pickedBrandId } from "@/lib/brand";
import type { Partner, Schemas } from "@outflare/sdk";
import { Plus, Ticket, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { api, errorMessage } from "@/api";
import { track } from "@/lib/analytics";

type Rules = Schemas["PartnerPromoRules"];
export type CouponTemplate = {
  id: string;
  label: string;
  coupon: string;
  rules: Rules;
};
const initial = (currency: string): Rules => ({
  currency,
  duration: "once",
  percent_bp: 1000,
});
export function CouponRules({
  rules: r,
  coupon,
  onChange,
  onCoupon,
}: {
  rules: Rules;
  coupon: string;
  onChange: (r: Rules, coupon?: string) => void;
  onCoupon: (v: string) => void;
}) {
  const set = (p: Partial<Rules>) => onChange({ ...r, ...p });
  const kind = coupon ? "existing" : r.amount_cents ? "fixed" : "percent";
  const date = (s: string | null | undefined) =>
    s ? new Date(s).toISOString().slice(0, 16) : "";
  const field = (
    label: string,
    key: "max_redemptions" | "min_order_cents" | "months",
    factor = 1,
  ) => (
    <label className="space-y-2 text-xs">
      {label}
      <Input
        type="number"
        min="0"
        step={factor === 100 ? "0.01" : "1"}
        value={(r[key] || 0) / factor}
        onChange={(e) =>
          set({ [key]: Math.round(Number(e.target.value) * factor) })
        }
      />
    </label>
  );
  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <label className="space-y-2 text-xs">
          Customer discount
          <select
            className="block h-10 w-full rounded-md border bg-background px-3"
            value={kind}
            onChange={(e) => {
              if (e.target.value === "existing") {
                onChange({ ...r, percent_bp: 0, amount_cents: 0 }, "coupon_id");
              } else {
                onChange(
                  {
                    ...r,
                    ...(e.target.value === "fixed"
                      ? { percent_bp: 0, amount_cents: 1000 }
                      : { percent_bp: 1000, amount_cents: 0 }),
                  },
                  "",
                );
              }
            }}
          >
            <option value="percent">Percentage off</option>
            <option value="fixed">Fixed amount off</option>
            <option value="existing">Existing billing coupon</option>
          </select>
        </label>
        {kind === "existing" ? (
          <label className="space-y-2 text-xs">
            Billing coupon ID
            <Input
              value={coupon}
              onChange={(e) => onCoupon(e.target.value)}
              required
            />
          </label>
        ) : (
          <label className="space-y-2 text-xs">
            {kind === "percent" ? "Discount (%)" : "Discount amount"}
            <Input
              type="number"
              min="0.01"
              max={kind === "percent" ? 100 : 1000000}
              step="0.01"
              value={
                (kind === "percent" ? r.percent_bp : r.amount_cents || 0)! / 100
              }
              onChange={(e) =>
                set(
                  kind === "percent"
                    ? { percent_bp: Math.round(Number(e.target.value) * 100) }
                    : {
                        amount_cents: Math.round(Number(e.target.value) * 100),
                      },
                )
              }
              required
            />
          </label>
        )}
        <label className="space-y-2 text-xs">
          Currency
          <Input
            value={r.currency || "USD"}
            maxLength={3}
            onChange={(e) => set({ currency: e.target.value.toUpperCase() })}
          />
        </label>
        <label className="space-y-2 text-xs">
          Subscription discount duration
          <select
            className="block h-10 w-full rounded-md border bg-background px-3"
            value={r.duration || "once"}
            onChange={(e) =>
              set({ duration: e.target.value as Rules["duration"] })
            }
          >
            <option value="once">First invoice</option>
            <option value="repeating">A number of months</option>
            <option value="forever">Ongoing</option>
          </select>
        </label>
        {r.duration === "repeating" && field("Number of months", "months")}
        {field(
          "Total redemptions per code (0 = no added limit)",
          "max_redemptions",
        )}
        {field("Minimum order amount", "min_order_cents", 100)}
        {(["starts_at", "ends_at"] as const).map((k) => (
          <label className="space-y-2 text-xs" key={k}>
            {k === "starts_at" ? "Starts at (UTC)" : "Redeem until (UTC)"}
            <Input
              type="datetime-local"
              value={date(r[k])}
              onChange={(e) =>
                set({
                  [k]: e.target.value
                    ? new Date(e.target.value + "Z").toISOString()
                    : null,
                })
              }
            />
          </label>
        ))}
      </div>
      <label className="flex items-center gap-2 text-xs">
        <input
          type="checkbox"
          checked={!!r.first_order_only}
          onChange={(e) => set({ first_order_only: e.target.checked })}
        />
        First-time purchases only
      </label>
      <label className="flex items-center gap-2 text-xs">
        <input
          type="checkbox"
          checked={!r.disabled}
          onChange={(e) => set({ disabled: !e.target.checked })}
        />
        Enable this offer
      </label>
      <label className="block space-y-2 text-xs">
        Eligible billing product IDs (optional)
        <Input
          value={(r.product_ids || []).join(", ")}
          onChange={(e) =>
            set({
              product_ids: e.target.value
                .split(",")
                .map((s) => s.trim())
                .filter(Boolean),
            })
          }
          placeholder="prod_example, prod_other"
        />
      </label>
      <p className="text-[11px] leading-5 text-muted-foreground">
        These rules control the customer's checkout discount. Partner
        commissions are configured separately. Billing enforces checkout
        eligibility and redemption limits; discount stacking is controlled by
        your checkout.
      </p>
    </div>
  );
}
export function CouponTemplates({
  value,
  onChange,
  currency,
}: {
  value: CouponTemplate[];
  onChange: (v: CouponTemplate[]) => void;
  currency: string;
}) {
  return (
    <div className="space-y-4">
      <p className="text-xs leading-6 text-muted-foreground">
        Let active partners generate memorable codes using only these approved
        offers. Your own billing connection with promotion-code write access is
        required. Assigned codes stay attached to their original rules when you
        edit an offer.
      </p>
      {value.map((t, i) => (
        <Card className="gap-4 p-4 shadow-none" key={t.id}>
          <div className="flex items-center gap-3">
            <Ticket className="size-4" />
            <Input
              aria-label={`Coupon offer ${i + 1} name`}
              placeholder="10% off your first purchase"
              value={t.label}
              maxLength={100}
              onChange={(e) =>
                onChange(
                  value.map((x, j) =>
                    j === i ? { ...x, label: e.target.value } : x,
                  ),
                )
              }
            />
            <Button
              type="button"
              size="icon-sm"
              variant="ghost"
              aria-label={`Remove coupon offer ${i + 1}`}
              onClick={() => onChange(value.filter((_, j) => i !== j))}
            >
              <Trash2 className="size-4" />
            </Button>
          </div>
          <CouponRules
            coupon={t.coupon}
            rules={t.rules}
            onCoupon={(coupon) =>
              onChange(value.map((x, j) => (j === i ? { ...x, coupon } : x)))
            }
            onChange={(rules, coupon) =>
              onChange(
                value.map((x, j) =>
                  j === i ? { ...x, rules, coupon: coupon ?? x.coupon } : x,
                ),
              )
            }
          />
        </Card>
      ))}
      <Button
        type="button"
        variant="outline"
        disabled={value.length >= 12}
        onClick={() => {
          onChange([
            ...value,
            {
              id: `offer_${crypto.randomUUID().replaceAll("-", "").slice(0, 16)}`,
              label: "",
              coupon: "",
              rules: initial(currency),
            },
          ]);
          track("ui.partner_coupon_composer_opened");
        }}
      >
        <Plus className="size-4" />
        Add coupon offer
      </Button>
    </div>
  );
}
export function PartnerCouponDialog({
  partner,
  currency,
  onClose,
  onSaved,
}: {
  ws: string;
  partner: Partner;
  currency: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [rules, setRules] = useState<Rules>(initial(currency)),
    [coupon, setCoupon] = useState(""),
    [code, setCode] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { error } = await api.POST(
      "/v1/brands/{brand}/partners/{id}/promo-codes",
      {
        params: { path: { brand: pickedBrandId(), id: partner.id } },
        body: { code, coupon, rules },
      },
    );
    setBusy(false);
    if (error) {
      setError(errorMessage(error));
      return;
    }
    onSaved();
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Partner coupon codes</DialogTitle>
          <DialogDescription>
            Assign a customer discount to {partner.name || partner.code}, or
            pause an existing code.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          {partner.promo_codes.map((c) => (
            <div
              className="flex items-center justify-between gap-3 rounded-lg border p-3"
              key={c.code}
            >
              <div>
                <p className="font-mono text-sm">{c.code}</p>
                <p className="mt-1 text-[11px] text-muted-foreground">
                  {c.rules?.disabled
                    ? "Disabled"
                    : c.rules?.starts_at &&
                        new Date(c.rules.starts_at) > new Date()
                      ? "Scheduled"
                      : c.rules?.ends_at &&
                          new Date(c.rules.ends_at) <= new Date()
                        ? "Expired"
                        : c.status === "created"
                          ? "Ready in billing"
                          : c.status === "local"
                            ? "Managed in your checkout"
                            : c.status === "pending"
                              ? "Syncing with billing"
                              : "Billing sync needs attention"}
                </p>
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={async () => {
                  setBusy(true);
                  const { error } = await api.PATCH(
                    "/v1/brands/{brand}/partners/{id}/promo-codes/{code}",
                    {
                      params: { path: { brand: pickedBrandId(), id: partner.id, code: c.code } },
                      body: { enabled: !!c.rules?.disabled },
                    },
                  );
                  setBusy(false);
                  if (error) setError(errorMessage(error));
                  else onSaved();
                }}
              >
                {c.rules?.disabled ? "Enable" : "Disable"}
              </Button>
            </div>
          ))}
        </div>
        <form onSubmit={save} className="space-y-5 border-t pt-5">
          <label className="block space-y-2 text-xs">
            New code
            <Input
              placeholder="MAYA10"
              pattern="[A-Za-z0-9][A-Za-z0-9_-]{0,63}"
              maxLength={64}
              value={code}
              onChange={(e) => setCode(e.target.value)}
              required
            />
          </label>
          <CouponRules
            rules={rules}
            coupon={coupon}
            onChange={(r, c) => {
              setRules(r);
              if (c !== undefined) setCoupon(c);
            }}
            onCoupon={setCoupon}
          />
          {error && (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          )}
          <Button disabled={busy}>Create and assign code</Button>
        </form>
      </DialogContent>
    </Dialog>
  );
}
