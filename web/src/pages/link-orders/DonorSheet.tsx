import { useEffect, useState } from "react";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../../api";
import { Badge, Button, ErrorText, field, toast } from "../../ui";
import { dash, day, durationLabel, idemp, metricFields, moneyList, siteAgeLabel, type LinkOrderDonor } from "./shared";

export function DonorSheet({ donor, onClose, brand, canManage, onSaved }: {
  donor?: LinkOrderDonor;
  onClose: () => void;
  brand?: string;
  canManage?: boolean;
  onSaved?: (d: LinkOrderDonor) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [form, setForm] = useState({ intermediary: "", theme: "", siteAge: "" });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    setEditing(false);
    setErr("");
    if (donor) setForm({ intermediary: donor.intermediary, theme: donor.theme, siteAge: donor.site_since ?? "" });
  }, [donor]);

  if (!donor) return null;

  const save = async () => {
    if (!brand) return;
    setBusy(true);
    setErr("");
    const { data, error } = await api.PATCH("/v1/brands/{brand}/link-orders/catalog/items/{id}", {
      params: { path: { brand, id: donor.id }, header: idemp() },
      body: { intermediary: form.intermediary.trim(), theme: form.theme.trim(), site_age: form.siteAge.trim() || null },
    });
    setBusy(false);
    if (error || !data) return setErr(errorMessage(error));
    toast(`Saved ${donor.domain}.`);
    setEditing(false);
    onSaved?.(data);
  };

  const metrics = metricFields.filter((m) => donor.metrics?.[m.key] != null);
  const groups = [...new Set(metrics.map((m) => m.group))];
  const show = (v: string | number | null | undefined) => (v == null ? dash : typeof v === "number" ? v.toLocaleString() : v);

  return (
    <Sheet open={!!donor} onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle className="flex flex-wrap items-center gap-2">
            {donor.domain}
            {donor.pbn ? <Badge tone="amber">PBN</Badge> : null}
          </SheetTitle>
          <SheetDescription>Catalog donor. Prices are what we pay the donor — there is no commission or client price. PBN is a manual flag.</SheetDescription>
        </SheetHeader>
        {editing ? (
          <div className="flex flex-col gap-2">
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Intermediary
              <input className={field} value={form.intermediary} onChange={(e) => setForm({ ...form, intermediary: e.target.value })} placeholder="Broker or contact" />
            </label>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Theme
              <input className={field} value={form.theme} onChange={(e) => setForm({ ...form, theme: e.target.value })} />
            </label>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Site age
              <input className={field} value={form.siteAge} onChange={(e) => setForm({ ...form, siteAge: e.target.value })} placeholder="2012, 2012-05-01 or 12 years" />
            </label>
            {err ? <ErrorText>{err}</ErrorText> : null}
            <div className="flex gap-2">
              <Button disabled={busy} onClick={() => void save()}>Save</Button>
              <Button variant="ghost" onClick={() => setEditing(false)}>Cancel</Button>
            </div>
          </div>
        ) : null}
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[13px]">
          <dt className="text-muted-foreground">Intermediary</dt>
          <dd>{donor.intermediary || dash}</dd>
          <dt className="text-muted-foreground">Theme</dt>
          <dd>{donor.theme || dash}</dd>
          <dt className="text-muted-foreground">Site age</dt>
          <dd>{siteAgeLabel(donor)}</dd>
          <dt className="text-muted-foreground">DR</dt>
          <dd>{donor.domain_rating ?? dash}</dd>
          <dt className="text-muted-foreground">Traffic (organic)</dt>
          <dd>{donor.traffic == null ? dash : donor.traffic.toLocaleString()}</dd>
          <dt className="text-muted-foreground">Country</dt>
          <dd>{donor.country || dash}</dd>
          <dt className="text-muted-foreground">Languages</dt>
          <dd>{donor.languages.join(", ") || dash}</dd>
          <dt className="text-muted-foreground">Niches</dt>
          <dd>{donor.niches.join(", ") || dash}</dd>
          <dt className="text-muted-foreground">Casino / gambling</dt>
          <dd>{donor.casino_allowed ? "Allowed" : "Not allowed"}</dd>
          <dt className="text-muted-foreground">Rel</dt>
          <dd>{donor.rel_offered.join(", ") || dash}</dd>
          <dt className="text-muted-foreground">Types</dt>
          <dd>{donor.slot_types.join(", ") || dash}</dd>
          <dt className="text-muted-foreground">Duration</dt>
          <dd>{durationLabel(donor.duration)}{donor.duration_days != null ? ` · ${donor.duration_days} days` : ""}</dd>
          <dt className="text-muted-foreground">TAT</dt>
          <dd>{donor.tat_days != null ? `${donor.tat_days} days` : dash}</dd>
          <dt className="text-muted-foreground">Guarantee</dt>
          <dd>{donor.guarantee_days != null ? `${donor.guarantee_days} days` : dash}</dd>
          <dt className="text-muted-foreground">Donor price</dt>
          <dd className="font-medium">{moneyList(donor.prices)}</dd>
          <dt className="text-muted-foreground">Source</dt>
          <dd>{donor.source === "competitor_gap" ? "Competitor gap" : donor.source === "csv" ? "CSV" : "Manual"}</dd>
          <dt className="text-muted-foreground">Metrics</dt>
          <dd>{donor.metrics_source || dash}{donor.metrics_at ? ` · ${day(donor.metrics_at)}` : ""}</dd>
        </dl>
        {canManage && brand && !editing ? (
          <div>
            <Button variant="ghost" onClick={() => setEditing(true)}>Edit intermediary, theme, site age</Button>
          </div>
        ) : null}
        {groups.length ? (
          <section className="flex flex-col gap-3" aria-label="Intermediary metrics">
            <h3 className="text-[13px] font-semibold">Intermediary metrics</h3>
            {groups.map((g) => (
              <div key={g}>
                <h4 className="mb-1 text-[12px] font-medium text-muted-foreground">{g}</h4>
                <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[13px]">
                  {metrics.filter((m) => m.group === g).map((m) => (
                    <div key={m.key} className="contents">
                      <dt className="text-muted-foreground">{m.label}</dt>
                      <dd className="tabular-nums">{show(donor.metrics[m.key])}</dd>
                    </div>
                  ))}
                </dl>
              </div>
            ))}
          </section>
        ) : null}
        {donor.notes ? <p className="text-[13px] text-muted-foreground">{donor.notes}</p> : null}
      </SheetContent>
    </Sheet>
  );
}
