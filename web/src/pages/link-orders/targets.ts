import { useEffect, useState } from "react";
import { api, errorMessage } from "../../api";
import type {
  LinkOrderAnchorOption,
  LinkOrderAnchorOptions,
  LinkOrderTarget,
  LinkOrderTargetReason,
  LinkOrderTargetSources,
  LinkOrderTargetSuggestion,
} from "./shared";

export const kindLabel: Record<string, string> = { article: "Article", product: "Product", "": "—" };

export const anchorSourceLabel: Record<string, string> = {
  se_ranking: "SE Ranking",
  page_target: "Keyword target",
  seo_keywords: "Stored keywords",
  manual: "Typed",
  brand: "Brand",
  url: "URL",
  "": "",
};

export const secondaryTypeLabel: Record<string, string> = {
  "": "No second anchor",
  brand: "Brand",
  url: "URL",
  keyword: "Another SE Ranking word",
};

export const emptyAnchors: LinkOrderAnchorOptions = { primary: [], secondary: { brand: [], url: [], keyword: [] } };

/** One line of evidence, in words. */
export function reasonText(r: LinkOrderTargetReason): string {
  if (r.kind === "keyword_gap") {
    const you = r.your_position == null ? "you are not in the top 100" : `you #${Math.round(r.your_position)}`;
    return `${r.competitor} #${Math.round(r.position ?? 0)} for “${r.keyword}” (${you})`;
  }
  const n = r.referring_domains;
  const page = r.competitor_url.replace(/^https?:\/\//, "");
  return `${n} site${n === 1 ? "" : "s"} link to ${page}${r.keyword ? ` (“${r.keyword}”)` : ""}`;
}

/** The brand's suggestions and saved targets, reloaded with tick. */
export function useTargets(brand: string, tick: number) {
  const [suggestions, setSuggestions] = useState<LinkOrderTargetSuggestion[]>([]);
  const [sources, setSources] = useState<LinkOrderTargetSources | null>(null);
  const [saved, setSaved] = useState<LinkOrderTarget[]>([]);
  const [error, setError] = useState("");
  const [ready, setReady] = useState(false);
  useEffect(() => {
    let gone = false;
    Promise.all([
      api.GET("/v1/brands/{brand}/link-orders/targets/suggestions", { params: { path: { brand }, query: { limit: 100 } } }),
      api.GET("/v1/brands/{brand}/link-orders/targets", { params: { path: { brand } } }),
    ]).then(([sug, list]) => {
      if (gone) return;
      const fail = sug.error ?? list.error;
      setError(fail ? errorMessage(fail) : "");
      setSuggestions(sug.data?.data ?? []);
      setSources(sug.data?.sources ?? null);
      setSaved(list.data?.data ?? []);
      setReady(true);
    });
    return () => {
      gone = true;
    };
  }, [brand, tick]);
  return { suggestions, sources, saved, error, ready };
}

/** Anchor choices for a typed page, from stored data only. */
export async function anchorsFor(brand: string, url: string): Promise<LinkOrderAnchorOptions> {
  const { data, error } = await api.GET("/v1/brands/{brand}/link-orders/targets/anchors", { params: { path: { brand }, query: { page: url } } });
  if (error || !data) return emptyAnchors;
  return data.anchors;
}

export function optionSource(options: LinkOrderAnchorOption[], text: string): string {
  const t = text.trim().toLowerCase().replace(/\s+/g, " ");
  return options.find((o) => o.text.toLowerCase() === t)?.source ?? (t ? "manual" : "");
}
