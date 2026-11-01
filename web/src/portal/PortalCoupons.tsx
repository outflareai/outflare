import { useState } from "react";
import { Copy, Gift, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { errorMessage } from "@/api";
import { track } from "@/lib/analytics";
import { toast } from "sonner";
import { request, type Me, type Program } from "./client";
export function PortalCoupons({
  me,
  program,
  reload,
}: {
  me: Me;
  program: Program;
  reload: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false),
    [code, setCode] = useState(""),
    [template, setTemplate] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const offers = (program.coupon_templates || []).filter(
    (t) =>
      !t.rules.disabled &&
      (!t.rules.ends_at || new Date(t.rules.ends_at) > new Date()),
  );
  const codes =
    me.promo_code_details ||
    me.promo_codes.map((code) => ({
      code,
      status: "local",
      rules: {},
      error: "",
      coupon: "",
    }));
  return (
    <Card className="gap-4 self-start p-6 shadow-none">
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Gift className="size-4" />
          <h2 className="text-sm font-semibold">Your coupon codes</h2>
        </div>
        {!!offers.length && (
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label="Generate coupon code"
            disabled={me.read_only || me.status !== "active"}
            onClick={() => {
              setTemplate(offers[0].id);
              setOpen(true);
              track("ui.partner_coupon_composer_opened");
            }}
          >
            <Plus className="size-4" />
          </Button>
        )}
      </div>
      <p className="text-xs leading-6 text-muted-foreground">
        Customer discounts are separate from your partner reward. Only share
        codes that are ready.
      </p>
      {codes.length ? (
        codes.map((c) => {
          const expired =
              c.rules.ends_at && new Date(c.rules.ends_at) <= new Date(),
            scheduled =
              c.rules.starts_at && new Date(c.rules.starts_at) > new Date(),
            ready =
              !c.rules.disabled &&
              !expired &&
              !scheduled &&
              (c.status === "created" || c.status === "local");
          return (
            <div
              key={c.code}
              className="flex items-center justify-between gap-3 rounded-lg border p-3"
            >
              <div>
                <p className="font-mono text-sm">{c.code}</p>
                <p className="mt-1 text-[11px] text-muted-foreground">
                  {c.rules.disabled
                    ? "Disabled"
                    : expired
                      ? "Expired"
                      : scheduled
                        ? "Scheduled"
                        : c.status === "pending"
                          ? "Preparing your code…"
                          : c.status === "failed"
                            ? "Not ready — contact the brand"
                            : "Ready to share"}
                </p>
              </div>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={`Copy ${c.code}`}
                disabled={!ready}
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(c.code);
                    track("ui.copied", { kind: "partner_coupon" });
                    toast.success("Code copied");
                  } catch {
                    setError(errorMessage(new Error("Copy the code manually from the text above.")));
                  }
                }}
              >
                <Copy className="size-4" />
              </Button>
            </div>
          );
        })
      ) : (
        <p className="text-xs leading-6 text-muted-foreground">
          {offers.length
            ? "Generate a memorable code using an offer approved by the brand."
            : "The brand will share any available codes here."}
        </p>
      )}
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>A code your audience will remember</DialogTitle>
            <DialogDescription>
              Choose an approved offer. The discount rules are set by the brand.
            </DialogDescription>
          </DialogHeader>
          <form
            className="space-y-5"
            onSubmit={async (e) => {
              e.preventDefault();
              setBusy(true);
              setError("");
              try {
                await request("promo-codes", "POST", { code, template });
                await reload();
                setOpen(false);
                toast.success(
                  "Your code is being prepared. Share it once it is ready.",
                );
              } catch (e) {
                setError(errorMessage(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            <label className="block space-y-2 text-xs">
              Offer
              <select
                className="block h-10 w-full rounded-md border bg-background px-3"
                value={template}
                onChange={(e) => setTemplate(e.target.value)}
              >
                {offers.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="block space-y-2 text-xs">
              Your code
              <Input
                placeholder="MAYA10"
                pattern="[A-Za-z0-9][A-Za-z0-9_-]{0,63}"
                maxLength={64}
                required
                value={code}
                onChange={(e) => setCode(e.target.value)}
              />
            </label>
            <p className="text-xs leading-6 text-muted-foreground">
              Codes may take a moment to activate. Scheduled offers become
              available at the brand's start time.
            </p>
            {error && (
              <p role="alert" className="text-xs text-destructive">
                {error}
              </p>
            )}
            <Button disabled={busy}>
              {busy ? "Creating…" : "Generate code"}
            </Button>
          </form>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
