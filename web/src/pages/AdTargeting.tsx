// A campaign's targeting: how it bids, the customer list audiences it
// targets and the countries it shows in, read live from the platform. Each
// change is queued, made on the platform by a job and listed under Changes.

import { useCallback, useEffect, useState } from "react";
import type { AdBiddingStrategy, AdCampaign, AdTargeting as Targeting } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, ErrorText, Loading, toast } from "../ui";
import { money, platformName } from "./Ads";
import { pickedBrandId } from "@/lib/brand";

const methodWord: Record<string, string> = { maximize_conversions: "Maximise conversions", maximize_conversion_value: "Maximise conversion value", portfolio: "Portfolio strategy",
  target_impression_share: "Target impression share", target_spend: "Maximise clicks" };
const shareWhere: Record<string, string> = { anywhere_on_page: "anywhere on the page", top_of_page: "at the top of the page", absolute_top_of_page: "in the very first position" };

function biddingWord(t: Targeting["bidding"], currency: string) {
  const name = methodWord[t.strategy] ?? t.strategy.replace(/_/g, " ");
  if (t.strategy === "set_per_ad_set") return "Set on each ad set, in Ads Manager";
  if (t.bidding_strategy_id) return `${name}, on portfolio strategy ${t.bidding_strategy_id}`;
  const cpc = t.max_cpc_micros != null ? `, at most ${money(t.max_cpc_micros, currency)} a click` : "";
  if (t.impression_share != null) return `${name}: ${Math.round(t.impression_share * 100)}% of searches ${shareWhere[t.impression_share_location ?? ""] ?? ""}${cpc}`;
  if (t.strategy === "target_spend") return `${name}${cpc || ", no limit on a click"}`;
  if (t.target_cpa_micros != null) return `${name}, target ${money(t.target_cpa_micros, currency)} a conversion`;
  if (t.target_roas != null) return `${name}, target ${t.target_roas}× return`;
  return `${name}, no target`;
}

export function CampaignTargeting({ ws, campaign }: { ws: string; campaign: AdCampaign }) {
  const [data, setData] = useState<{ supported: boolean; targeting?: Targeting; bidding_methods: string[] } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const path = { brand: pickedBrandId(), id: campaign.id };
  const load = useCallback(() => {
    api.GET("/v1/brands/{brand}/ads/campaigns/{id}/targeting", { params: { path: { brand: pickedBrandId(), id: campaign.id } } }).then(({ data, error }) => {
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
  // portfolio puts the campaign on one of its account's portfolio
  // strategies, picked from the account's list.
  async function portfolio() {
    const { data: list, error } = await api.GET("/v1/brands/{brand}/ads/accounts/{id}/bidding-strategies", { params: { path: { brand: pickedBrandId(), id: campaign.ad_account_id } } });
    if (error) return toast(errorMessage(error), "error");
    const usable = (list?.data ?? []).filter((s: AdBiddingStrategy) => s.status !== "removed" && s.id !== data?.targeting?.bidding.bidding_strategy_id).slice(0, 8);
    if (usable.length === 0) return toast("This account has no other portfolio strategy. Create one under Accounts, Portfolio bidding strategies.");
    const target = (s: AdBiddingStrategy) => (s.target_cpa_micros != null ? `, ${money(s.target_cpa_micros, s.currency || campaign.currency)} a conversion` : s.target_roas != null ? `, ${s.target_roas}× return` : "");
    const r = await ask({
      title: `Put ${campaign.name} on a portfolio strategy`,
      body: "The campaign bids to the strategy's target, shared with its other campaigns. To move the target, change it on the strategy.",
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, ...usable.map((s) => ({ label: `${s.name}${target(s)}`, value: s.id, variant: "primary" as const }))],
    });
    if (!r || r.choice === "cancel") return;
    setBusy(true);
    const res = await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/bidding", { params: { path }, body: { strategy: "portfolio", bidding_strategy_id: r.choice } });
    done(res.error);
  }
  // impressionShare asks where on the page, how often, and the most to
  // pay for a click: Google needs all three.
  async function impressionShare() {
    const now = data?.targeting?.bidding;
    const where = await ask({
      title: "Where should the ad show, and how often?",
      body: "Google bids to show the ad in that spot for this share of the searches it could show on. 100 means every one.",
      input: { label: "Share of searches (%)", type: "number", value: String(Math.round((now?.impression_share ?? 1) * 100)) },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Anywhere on the page", value: "anywhere_on_page", variant: "ghost" },
        { label: "Top of the page", value: "top_of_page", variant: "ghost" }, { label: "Very first position", value: "absolute_top_of_page", variant: "primary" }],
    });
    if (!where || where.choice === "cancel") return;
    const share = Number(where.value);
    if (!(share > 0 && share <= 100)) return toast("The share is a number from 1 to 100.", "error");
    const cap = await ask({
      title: "The most to pay for a click",
      body: `Google never bids above it, in ${campaign.currency}. Without a limit this method can pay any price to hold the spot, so one is required.`,
      input: { label: `Most for a click (${campaign.currency})`, type: "number", placeholder: "5.00", value: now?.max_cpc_micros != null ? (now.max_cpc_micros / 1e6).toFixed(2) : "" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Change bidding", value: "ok", variant: "primary" }],
    });
    if (!cap || cap.choice !== "ok") return;
    const max = Number(cap.value);
    if (!(max > 0)) return toast("The most to pay for a click is an amount above zero.", "error");
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/bidding", { params: { path },
      body: { strategy: "target_impression_share", impression_share_location: where.choice as "anywhere_on_page" | "top_of_page" | "absolute_top_of_page", impression_share: share / 100, max_cpc_micros: Math.round(max * 1e6) } });
    done(error);
  }
  async function bidding() {
    const r = await ask({
      title: `Change how ${campaign.name} bids`,
      body: `Google's bidding takes one to two weeks to settle after a change. A target is optional: a cost per conversion in ${campaign.currency} for conversions, a return such as 3 (three times spend) for value, the most to pay for a click for clicks. Target impression share asks for its own settings next.`,
      input: { label: "Target", type: "number", optional: true, placeholder: "Optional" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, ...data!.bidding_methods.map((m) => ({ label: methodWord[m] ?? m, value: m, variant: "primary" as const }))],
    });
    if (!r) return;
    if (r.choice === "portfolio") return portfolio();
    if (r.choice === "target_impression_share") return impressionShare();
    const v = Number(r.value);
    const strategy = r.choice as "maximize_conversions" | "maximize_conversion_value" | "target_spend";
    const target = r.value.trim() === "" || !(v > 0) ? {} : strategy === "maximize_conversions" ? { target_cpa_micros: Math.round(v * 1e6) }
      : strategy === "target_spend" ? { max_cpc_micros: Math.round(v * 1e6) } : { target_roas: v };
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/bidding", { params: { path }, body: { strategy, ...target } });
    done(error);
  }
  async function audience(id: string, name: string, action: "add" | "remove") {
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/audiences", { params: { path }, body: { audience_id: id, audience_name: name, action } });
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
    const { error } = await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/countries", { params: { path }, body: { country: code, action } });
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
            {t.target_all_locations ? "Everywhere it isn't excluded from" : t.countries.length === 0 ? "No whole country, see the places below" : (
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
        {t.included_locations.some((l) => l.target_type !== "Country") && (
          <div>
            <div className="text-muted-foreground text-[12px]">Regions, cities and radiuses it shows in</div>
            {t.included_locations.filter((l) => l.target_type !== "Country").map((l) => l.radius != null ? `${l.radius} ${l.radius_units} around ${l.name || "a point"}` : l.name || l.geo_target_constant).join("; ")}
          </div>
        )}
        <div>
          <div className="text-muted-foreground text-[12px]">Excluded places</div>
          {t.excluded_locations.length === 0 ? "None" : (
            <div className="flex flex-wrap gap-1.5 pt-1" data-excluded-places>
              {t.excluded_locations.map((l) => <Badge key={l.criterion_id || l.name} tone="grey">{l.name || l.geo_target_constant}</Badge>)}
            </div>
          )}
        </div>
        {t.notes.length > 0 && <ul className="text-[12px] text-muted-foreground list-disc pl-4">{t.notes.map((n) => <li key={n}>{n}</li>)}</ul>}
      </div>
    </Card>
  );
}
