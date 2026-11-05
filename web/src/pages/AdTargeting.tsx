// A campaign's targeting: how it bids, the customer list audiences it
// targets and the countries it shows in, read live from the platform. Each
// change is queued, made on the platform by a job and listed under Changes.

import { useCallback, useEffect, useState } from "react";
import type { AdCampaign, AdTargeting as Targeting } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, ErrorText, Loading, toast } from "../ui";
import { money, platformName } from "./Ads";

const methodWord: Record<string, string> = { maximize_conversions: "Maximise conversions", maximize_conversion_value: "Maximise conversion value" };

function biddingWord(t: Targeting["bidding"], currency: string) {
  const name = methodWord[t.strategy] ?? t.strategy.replace(/_/g, " ");
  if (t.strategy === "set_per_ad_set") return "Set on each ad set, in Ads Manager";
  if (t.target_cpa_micros != null) return `${name}, target ${money(t.target_cpa_micros, currency)} a conversion`;
  if (t.target_roas != null) return `${name}, target ${t.target_roas}× return`;
  return `${name}, no target`;
}

export function CampaignTargeting({ ws, campaign }: { ws: string; campaign: AdCampaign }) {
  const [data, setData] = useState<{ supported: boolean; targeting?: Targeting; bidding_methods: string[] } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const path = { ws, id: campaign.id };
  const load = useCallback(() => {
    api.GET("/v1/workspaces/{ws}/ads/campaigns/{id}/targeting", { params: { path: { ws, id: campaign.id } } }).then(({ data, error }) => {
      if (error) setError(errorMessage(error));
      else setData(data ?? null);
    });
  }, [ws, campaign.id]);
  useEffect(() => {
    load();
  }, [load]);

  async function done(error: unknown) {
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Change queued. It shows under Changes once the platform takes it.");
    setTimeout(load, 4000);
  }
  async function bidding() {
    const r = await ask({
      title: `Change how ${campaign.name} bids`,
      body: `Google's bidding takes one to two weeks to settle after a change. A target is optional: a cost per conversion in ${campaign.currency} for conversions, a return such as 3 (three times spend) for value.`,
      input: { label: "Target", type: "number", optional: true, placeholder: "Optional" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, ...data!.bidding_methods.map((m) => ({ label: methodWord[m] ?? m, value: m, variant: "primary" as const }))],
    });
    if (!r) return;
    const v = Number(r.value);
    const strategy = r.choice as "maximize_conversions" | "maximize_conversion_value";
    const target = r.value.trim() === "" || !(v > 0) ? {} : strategy === "maximize_conversions" ? { target_cpa_micros: Math.round(v * 1e6) } : { target_roas: v };
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/ads/campaigns/{id}/bidding", { params: { path }, body: { strategy, ...target } });
    done(error);
  }
  async function audience(id: string, name: string, action: "add" | "remove") {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/ads/campaigns/{id}/audiences", { params: { path }, body: { audience_id: id, audience_name: name, action } });
    done(error);
  }
  async function addAudience() {
    const { data: list, error } = await api.GET("/v1/workspaces/{ws}/ads/audiences", { params: { path: { ws }, query: { account_id: campaign.ad_account_id } } });
    if (error) return toast(errorMessage(error), "error");
    const have = new Set(data?.targeting?.audiences.map((a) => a.id));
    const choices = (list?.data ?? []).filter((a) => !have.has(a.id)).slice(0, 8);
    if (choices.length === 0) return toast("No other customer lists in this account. Journeys fill them from smart lists (Sync to an audience).");
    const r = await ask({
      title: `Add an audience to ${campaign.name}`,
      body: campaign.connector === "meta_ads"
        ? "It goes on every ad set, so they reach only people in it."
        : "On Search, Google bids with it in mind (observation) without narrowing whom the campaign reaches.",
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, ...choices.map((a) => ({ label: a.name || a.id, value: a.id, variant: "primary" as const }))],
    });
    if (!r) return;
    audience(r.choice, choices.find((a) => a.id === r.choice)?.name ?? "", "add");
  }
  async function country(code: string, action: "add" | "remove") {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/ads/campaigns/{id}/countries", { params: { path }, body: { country: code, action } });
    done(error);
  }
  async function addCountry() {
    const r = await ask({
      title: `Show ${campaign.name} in another country`,
      body: "A two-letter country code, such as AU, NZ or GB. It spends more if the campaign's budget isn't already used up.",
      input: { label: "Country", placeholder: "NZ", maxLength: 2 },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Add", value: "ok", variant: "primary" }],
    });
    if (r?.choice === "ok" && /^[a-z]{2}$/i.test(r.value.trim())) country(r.value.trim().toUpperCase(), "add");
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!data) return <Loading />;
  if (!data.supported || !data.targeting) return null;
  const t = data.targeting;
  return (
    <Card title="Targeting" description={`How this campaign bids and whom it reaches, read from ${platformName(campaign.connector)} now. Each change is queued, made on the platform and listed under Changes.`}>
      <div className="space-y-4 text-sm">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div><div className="text-muted-foreground text-[12px]">Bidding</div>{biddingWord(t.bidding, campaign.currency)}</div>
          {data.bidding_methods.length > 0 && <Button variant="ghost" disabled={busy} onClick={bidding}>Change bidding</Button>}
        </div>
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <div className="text-muted-foreground text-[12px]">Customer list audiences</div>
            {t.audiences.length === 0 ? "None" : (
              <div className="flex flex-wrap gap-1.5 pt-1">
                {t.audiences.map((a) => (
                  <Badge key={a.id} tone="grey">
                    {a.name || a.id}
                    <button className="ml-1 text-muted-foreground hover:text-foreground" disabled={busy} title="Take it out" aria-label={`Take out ${a.name || a.id}`} onClick={() => audience(a.id, a.name, "remove")}>×</button>
                  </Badge>
                ))}
              </div>
            )}
          </div>
          <Button variant="ghost" disabled={busy} onClick={addAudience}>Add audience</Button>
        </div>
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <div className="text-muted-foreground text-[12px]">Countries</div>
            {t.countries.length === 0 ? "None set here" : (
              <div className="flex flex-wrap gap-1.5 pt-1">
                {t.countries.map((c) => (
                  <Badge key={c} tone="grey">
                    {c}
                    {t.countries.length > 1 && (
                      <button className="ml-1 text-muted-foreground hover:text-foreground" disabled={busy} title="Take it out" aria-label={`Take out ${c}`} onClick={() => country(c, "remove")}>×</button>
                    )}
                  </Badge>
                ))}
              </div>
            )}
          </div>
          <Button variant="ghost" disabled={busy} onClick={addCountry}>Add country</Button>
        </div>
        {t.notes.length > 0 && <ul className="text-[12px] text-muted-foreground list-disc pl-4">{t.notes.map((n) => <li key={n}>{n}</li>)}</ul>}
      </div>
    </Card>
  );
}
