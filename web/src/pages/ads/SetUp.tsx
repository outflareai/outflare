// Set up: the guided path to measuring ads in cost per new paying customer,
// one step at a time with the button that fixes it, and the offer sheet
// ("What Outflare thinks you sell") a person checks before any ad runs.

import { useCallback, useEffect, useState } from "react";
import type { Tab } from "../../App";
import { api, errorMessage } from "../../api";
import { track } from "@/lib/analytics";
import { parsePath } from "@/lib/paths";
import { Badge, Button, Card, ErrorText, Loading, toast } from "../../ui";
import { ago } from "../../format";
import { setPayerTarget } from "./Hero";
import type { AdEconomics, AdSetup, BrandKnowledgeSheet } from "./api";

type Step = AdSetup["steps"][number];

// Why the website could not be read, and what to connect (the same words
// as the brand's own knowledge card).
function readFailed(code: string): string {
  switch (code) {
    case "connector_needed": return "Connect Geonode under Connectors so Outflare can read the website.";
    case "ai_model_needed": return "Choose an AI model under Settings, AI model, so Outflare can read the website.";
    case "ai_budget_reached": return "The AI budget for this month is spent; the website is read again next month or when the budget is raised.";
    case "blocked": return "The site showed a bot check instead of its pages.";
    case "slow_page": return "The site was too slow to read.";
    case "ai_failed": return "The AI model couldn't write the sheet up this time.";
    default: return code ? `The last read failed (${code}).` : "";
  }
}

// The lists the sheet shows, in the order a person checks them.
const sheetLists: { id: string; label: string }[] = [
  { id: "products", label: "Products" }, { id: "services", label: "Services" }, { id: "prices", label: "Prices" }, { id: "audiences", label: "Who it serves" },
];

type Props = {
  brand: string;
  setup: AdSetup | null;
  economics: AdEconomics | null;
  error: string;
  canManage: boolean;
  reload: () => void;
  go: (tab: Tab, item?: string) => void;
};

export function SetUp({ brand, setup, economics, error, canManage, reload, go }: Props) {
  // The page that fixes a step: the ads page's own screens for its own
  // steps, the target dialog for the economics step, the sheet below for
  // the knowledge step, and the step's fix page for the rest.
  function open(step: Step) {
    track("ui.ads_setup_step_opened", { step: step.step, done: step.done });
    switch (step.step) {
      case "economics":
        if (canManage) return setPayerTarget(brand, economics?.currency || "USD", economics?.target_cost_per_payer_micros ?? null, reload);
        return go("ads");
      case "tracking_template": return go("ads", "tracking");
      case "conversion_feed": return go("ads", "conversions");
      case "consent": return go("ads", "conversions");
      case "ad_account": return go("ads", "accounts");
      case "knowledge_sheet":
        document.getElementById("ads-knowledge")?.scrollIntoView({ behavior: "smooth", block: "start" });
        return;
      default: {
        const p = parsePath(step.fix.page);
        return go(p.tab ?? "setup", p.item);
      }
    }
  }
  const fixWord = (s: Step) => (s.step === "economics" ? (s.done ? "Change the target" : "Set the target") : s.step === "knowledge_sheet" ? "Check the sheet" : s.done ? "Open" : "Fix it");

  return (
    <div className="space-y-6">
      <Card
        title="Set up"
        description="What Outflare needs to count each sign-up and first payment for the ad click that brought it. Each step opens the page that fixes it."
        action={setup && <Badge tone={setup.ready ? "green" : "amber"}>{setup.done} of {setup.total} done</Badge>}
      >
        {error && !setup && <ErrorText>{error}</ErrorText>}
        {!setup && !error && <Loading />}
        {setup && (
          <ol className="divide-y">
            {setup.steps.map((s, i) => (
              <li key={s.step} className="flex flex-wrap items-start justify-between gap-3 py-3 first:pt-0 last:pb-0">
                <div className="flex min-w-0 flex-1 items-start gap-3">
                  <span aria-hidden="true" className={`mt-0.5 inline-flex size-5 shrink-0 items-center justify-center rounded-full font-mono text-[11px] ${s.done ? "bg-green-500/15 text-green-700 dark:text-green-400" : "bg-muted text-muted-foreground"}`}>{s.done ? "✓" : i + 1}</span>
                  <div className="min-w-0 space-y-0.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-[13px] font-medium">{s.title}</span>
                      <Badge tone={s.done ? "green" : "amber"}>{s.done ? "Done" : "To do"}</Badge>
                      {s.fix.who === "agent" && !s.done && <Badge tone="blue" title="Your agent can do this step">Agent can do it</Badge>}
                    </div>
                    <p className="text-[13px] text-muted-foreground">{s.detail}</p>
                  </div>
                </div>
                {(canManage || s.step === "knowledge_sheet") && <Button variant={s.done ? "ghost" : "primary"} onClick={() => open(s)}>{fixWord(s)}</Button>}
              </li>
            ))}
          </ol>
        )}
        {setup && <p className="mt-3 text-[12px] text-muted-foreground">Checked {ago(setup.checked_at)}.</p>}
      </Card>
      <OfferSheet brand={brand} canManage={canManage} reload={reload} go={go} />
    </div>
  );
}

// What Outflare thinks the brand sells, read from its website: the sheet
// every new ad draws its facts from. A person says it looks right, or
// edits it on the brand's page.
export function OfferSheet({ brand, canManage, reload, go }: { brand: string; canManage: boolean; reload: () => void; go: (tab: Tab, item?: string) => void }) {
  const [sheet, setSheet] = useState<BrandKnowledgeSheet | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/knowledge", { params: { path: { brand } } });
    setError(error ? errorMessage(error) : "");
    if (data) setSheet(data);
  }, [brand]);
  useEffect(() => {
    load();
  }, [load]);

  async function looksRight() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/knowledge/review", { params: { path: { brand } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    if (data && "brand_id" in data) setSheet(data);
    toast("Reviewed. New ads draw their facts from this sheet.");
    reload();
  }

  const k = sheet?.knowledge;
  const empty = !!k && !k.summary && sheetLists.every((l) => (k.lists[l.id] ?? []).length === 0);
  const reviewed = !!sheet?.reviewed_at && !sheet.changed_since_review;
  return (
    <section id="ads-knowledge" aria-labelledby="ads-knowledge-title" className="rounded-lg border bg-card text-card-foreground shadow-xs">
      <header className="flex min-h-11 flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <div>
          <h2 id="ads-knowledge-title" className="text-[13px] font-semibold">What Outflare thinks you sell</h2>
          <p className="text-[12px] text-muted-foreground">Read from the brand's website{sheet?.refresh_daily ? ", kept fresh daily" : ""}. Every new ad uses only facts from this sheet, so check it before any ad runs.</p>
        </div>
        {sheet && (
          <Badge tone={reviewed ? "green" : sheet.read_error ? "red" : "amber"}>
            {reviewed ? `Reviewed ${ago(sheet.reviewed_at)}` : sheet.reviewed_at ? "Changed since review" : sheet.read_error ? "Read failed" : "Not reviewed"}
          </Badge>
        )}
      </header>
      <div className="space-y-3 p-4 text-[13px]">
        {error && !sheet && <ErrorText>{error}</ErrorText>}
        {!sheet && !error && <Loading />}
        {sheet && sheet.read_error && <p role="alert" className="text-[13px] text-red-600 dark:text-red-400">{readFailed(sheet.read_error)}</p>}
        {sheet && k && (
          <>
            {empty ? (
              <p className="text-muted-foreground">{sheet.read_at ? "The website gave Outflare nothing to go on yet." : "The website hasn't been read yet."} Edit the sheet to tell Outflare what you sell.</p>
            ) : (
              <>
                {k.summary && <p>{k.summary}</p>}
                <dl className="grid gap-3 sm:grid-cols-2">
                  {sheetLists.map((l) => {
                    const items = k.lists[l.id] ?? [];
                    if (items.length === 0) return null;
                    return (
                      <div key={l.id}>
                        <dt className="font-mono text-[11px] tracking-wide text-muted-foreground uppercase">{l.label}</dt>
                        <dd><ul className="mt-1 list-disc space-y-0.5 pl-4">{items.slice(0, 8).map((x, i) => <li key={i}>{x.text}</li>)}{items.length > 8 && <li className="list-none text-muted-foreground">and {items.length - 8} more</li>}</ul></dd>
                      </div>
                    );
                  })}
                </dl>
              </>
            )}
            {sheet.read_at && <p className="text-[12px] text-muted-foreground">Read {ago(sheet.read_at)} from {sheet.pages} {sheet.pages === 1 ? "page" : "pages"}.</p>}
          </>
        )}
        {sheet && (
          <div className="flex flex-wrap gap-2 pt-1">
            {canManage && !reviewed && <Button disabled={busy || empty} onClick={looksRight} title={empty ? "Nothing to review yet" : undefined}>Looks right</Button>}
            {canManage && <Button variant="ghost" onClick={() => go("brands", brand)}>Edit</Button>}
            {!canManage && <p className="text-muted-foreground">Ask an admin to review it.</p>}
          </div>
        )}
      </div>
    </section>
  );
}
