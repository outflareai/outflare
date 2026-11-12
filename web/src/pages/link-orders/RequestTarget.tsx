import { useEffect, useMemo, useState } from "react";
import { Badge, field } from "../../ui";
import type { LinkOrderAnchorOptions, LinkOrderTargetSuggestion } from "./shared";
import { anchorSourceLabel, anchorsFor, emptyAnchors, kindLabel, optionSource, reasonText, secondaryTypeLabel } from "./targets";

export type SecondaryType = "" | "brand" | "url" | "keyword";

/** What a new order points at, and its two anchors. */
export type RequestTargetValue = {
  targetId: string | null;
  url: string;
  kind: "" | "article" | "product";
  choice: "manual" | "suggested";
  primary: string;
  secondary: string;
  secondaryType: SecondaryType;
  anchors: LinkOrderAnchorOptions;
};

export const emptyRequestTarget: RequestTargetValue = {
  targetId: null,
  url: "",
  kind: "",
  choice: "manual",
  primary: "",
  secondary: "",
  secondaryType: "",
  anchors: emptyAnchors,
};

/** The order fields the picker fills. */
export function requestTargetBody(v: RequestTargetValue) {
  const primarySource = optionSource(v.anchors.primary, v.primary);
  return {
    target_url: v.url.trim(),
    target_id: v.targetId,
    target_kind: v.kind,
    target_choice: v.choice,
    anchor_primary: v.primary.trim(),
    anchor_primary_source: (primarySource || "") as "" | "manual" | "se_ranking" | "page_target" | "seo_keywords",
    anchor_secondary: v.secondaryType ? v.secondary.trim() : "",
    anchor_secondary_type: v.secondary.trim() ? v.secondaryType : ("" as SecondaryType),
  };
}

function fromSuggestion(s: LinkOrderTargetSuggestion, choice: "manual" | "suggested", prev: RequestTargetValue): RequestTargetValue {
  const secondaryType = prev.secondaryType;
  return {
    targetId: s.target_id ?? null,
    url: s.url,
    kind: s.kind,
    choice,
    primary: s.anchors.primary[0]?.text ?? "",
    secondaryType,
    secondary: secondaryType ? (s.anchors.secondary[secondaryType][0]?.text ?? "") : "",
    anchors: s.anchors,
  };
}

/**
 * Pick the page a link points at: the top suggestion is ticked by default
 * (kept as "suggested"), another one can be ticked instead, or a page typed.
 * Then the primary anchor (SE Ranking first) and an optional second anchor
 * whose type the person picks.
 */
export function RequestTarget({ brand, suggestions, value, onChange }: {
  brand: string;
  suggestions: LinkOrderTargetSuggestion[];
  value: RequestTargetValue;
  onChange: (v: RequestTargetValue) => void;
}) {
  const [kind, setKind] = useState<"" | "article" | "product">("");
  const shown = useMemo(() => suggestions.filter((s) => !kind || s.kind === kind).slice(0, 8), [suggestions, kind]);
  const top = suggestions[0];

  useEffect(() => {
    if (!value.url && top) onChange(fromSuggestion(top, "suggested", value));
    // Only when the suggestions arrive and nothing is picked yet.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [top?.url]);

  const tick = (s: LinkOrderTargetSuggestion) => {
    if (value.url === s.url) {
      onChange({ ...emptyRequestTarget, secondaryType: value.secondaryType });
      return;
    }
    onChange(fromSuggestion(s, s.url === top?.url ? "suggested" : "manual", value));
  };

  const typed = !suggestions.some((s) => s.url === value.url) ? value.url : "";
  const typeURL = (url: string) => onChange({ ...value, targetId: null, url, kind: "", choice: "manual" });
  const loadTypedAnchors = async () => {
    if (!typed.trim()) return;
    const anchors = await anchorsFor(brand, typed.trim());
    onChange({
      ...value,
      anchors,
      primary: value.primary || anchors.primary[0]?.text || "",
      secondary: value.secondaryType && !value.secondary ? (anchors.secondary[value.secondaryType][0]?.text ?? "") : value.secondary,
    });
  };

  const secondaryOptions = value.secondaryType ? value.anchors.secondary[value.secondaryType] : [];
  const primarySource = optionSource(value.anchors.primary, value.primary);

  return (
    <div className="flex flex-col gap-3 rounded-md border p-3" data-testid="request-target">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-[13px] font-medium">Link target</div>
        <div className="flex rounded-md border p-0.5">
          {([["", "All"], ["article", "Articles"], ["product", "Products"]] as const).map(([id, label]) => (
            <button
              key={id || "all"}
              type="button"
              className={`rounded px-2 py-1 text-[12px] ${kind === id ? "bg-accent font-medium" : "text-muted-foreground"}`}
              onClick={() => setKind(id)}
            >
              {label}
            </button>
          ))}
        </div>
      </div>
      {shown.length ? (
        <ul className="flex flex-col gap-1">
          {shown.map((s) => (
            <li key={s.url}>
              <label className="flex cursor-pointer items-start gap-2 text-[13px]">
                <input type="checkbox" className="mt-1" checked={value.url === s.url} onChange={() => tick(s)} aria-label={`Use ${s.url}`} />
                <span className="flex min-w-0 flex-col">
                  <span className="flex flex-wrap items-center gap-1.5">
                    <span className="truncate font-mono text-[12px]">{s.url}</span>
                    <Badge tone={s.kind === "article" ? "blue" : "green"} title={s.kind_guessed ? "Guessed from the address" : "Saved on Targets"}>
                      {kindLabel[s.kind]}{s.kind_guessed ? "?" : ""}
                    </Badge>
                    {s.url === top?.url ? <Badge tone="amber">Suggested</Badge> : null}
                  </span>
                  {s.reasons[0] ? <span className="text-[12px] text-muted-foreground">{reasonText(s.reasons[0])}</span> : null}
                </span>
              </label>
            </li>
          ))}
        </ul>
      ) : (
        <div className="text-[12px] text-muted-foreground">No suggestions from stored data yet. Type the page, or tick pages on Targets.</div>
      )}
      <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
        Or another page of this brand
        <input
          className={field}
          value={typed}
          placeholder="https://… (your own site)"
          onChange={(e) => typeURL(e.target.value)}
          onBlur={() => void loadTypedAnchors()}
        />
      </label>

      <div className="grid gap-2 sm:grid-cols-2">
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          <span>
            Primary anchor{primarySource ? <span className="ml-1"><Badge tone={primarySource === "se_ranking" ? "green" : "grey"}>{anchorSourceLabel[primarySource]}</Badge></span> : null}
          </span>
          <input
            className={field}
            list="link-order-primary-anchors"
            value={value.primary}
            onChange={(e) => onChange({ ...value, primary: e.target.value })}
            placeholder={value.anchors.primary.length ? "" : "No SE Ranking keyword for this page; type one"}
          />
          <datalist id="link-order-primary-anchors">
            {value.anchors.primary.map((o) => <option key={o.text} value={o.text}>{anchorSourceLabel[o.source]}</option>)}
          </datalist>
        </label>
        <div className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          <span>Second anchor</span>
          <div className="flex gap-2">
            <select
              className={field}
              aria-label="Second anchor type"
              value={value.secondaryType}
              onChange={(e) => {
                const t = e.target.value as SecondaryType;
                onChange({ ...value, secondaryType: t, secondary: t ? (value.anchors.secondary[t][0]?.text ?? "") : "" });
              }}
            >
              {(["", "brand", "url", "keyword"] as const).map((t) => <option key={t} value={t}>{secondaryTypeLabel[t]}</option>)}
            </select>
            {value.secondaryType ? (
              <>
                <input
                  className={`${field} min-w-0 flex-1`}
                  aria-label="Second anchor"
                  list="link-order-secondary-anchors"
                  value={value.secondary}
                  onChange={(e) => onChange({ ...value, secondary: e.target.value })}
                />
                <datalist id="link-order-secondary-anchors">
                  {secondaryOptions.map((o) => <option key={o.text} value={o.text} />)}
                </datalist>
              </>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  );
}
