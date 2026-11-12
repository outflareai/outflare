import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Badge } from "../../ui";
import { dash, day, durationLabel, moneyList, type LinkOrderDonor } from "./shared";

export function DonorSheet({ donor, onClose }: { donor?: LinkOrderDonor; onClose: () => void }) {
  if (!donor) return null;
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
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[13px]">
          <dt className="text-muted-foreground">DR</dt>
          <dd>{donor.domain_rating ?? dash}</dd>
          <dt className="text-muted-foreground">Traffic</dt>
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
        {donor.notes ? <p className="text-[13px] text-muted-foreground">{donor.notes}</p> : null}
      </SheetContent>
    </Sheet>
  );
}
