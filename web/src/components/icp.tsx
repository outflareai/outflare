import { useState, type KeyboardEvent } from "react";
import type { ICP } from "@outflare/sdk";
import { field } from "../ui";
import { cn } from "@/lib/utils";

// The customer filters of an ideal customer profile, in the same terms as
// lead searches, and the tag box they are typed into.

export const icpFields: { key: keyof ICP; label: string; placeholder: string }[] = [
  { key: "titles", label: "Job titles", placeholder: "CFO, VP Finance" },
  { key: "seniorities", label: "Seniority", placeholder: "C-suite, VP, Director" },
  { key: "industries", label: "Industries", placeholder: "Fintech, Banking" },
  { key: "employee_ranges", label: "Company size (employees)", placeholder: "51-200, 201-500" },
  { key: "locations", label: "Locations", placeholder: "Australia, New Zealand" },
  { key: "keywords", label: "Keywords", placeholder: "Series B, SOC 2" },
];

export const emptyICP = (): ICP => ({ titles: [], seniorities: [], industries: [], employee_ranges: [], locations: [], keywords: [] });

export function TagInput({ label, values, onChange, placeholder, readOnly }: { label: string; values: string[]; onChange: (v: string[]) => void; placeholder: string; readOnly?: boolean }) {
  const [draft, setDraft] = useState("");
  const add = (raw: string) => {
    const next = [...values];
    for (const v of raw.split(",").map((x) => x.trim()).filter(Boolean)) {
      if (!next.some((n) => n.toLowerCase() === v.toLowerCase())) next.push(v.slice(0, 200));
    }
    onChange(next);
    setDraft("");
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if ((e.key === "Enter" || e.key === ",") && draft.trim()) {
      e.preventDefault();
      add(draft);
    } else if (e.key === "Backspace" && !draft && values.length) {
      onChange(values.slice(0, -1));
    }
  };
  const inputId = `tag-${label.replace(/\W+/g, "-").toLowerCase()}`;
  return (
    <div className="text-sm">
      <label htmlFor={inputId} className="mb-1.5 block text-[13px] font-medium">{label}</label>
      <div className={cn(field, "flex h-auto min-h-8 w-full flex-wrap items-center gap-1 py-1", readOnly && "bg-muted/30")}>
        {values.map((v) => (
          <span key={v} className="inline-flex h-6 items-center gap-1 rounded border bg-muted px-1.5 text-[12px]">
            {v}
            {!readOnly && (
              <button type="button" aria-label={`Remove ${v}`} className="text-muted-foreground hover:text-foreground" onClick={() => onChange(values.filter((x) => x !== v))}>×</button>
            )}
          </span>
        ))}
        {!readOnly && (
          <input
            id={inputId}
            value={draft}
            onChange={(e) => (e.target.value.includes(",") ? add(e.target.value) : setDraft(e.target.value))}
            onKeyDown={onKey}
            onBlur={() => draft.trim() && add(draft)}
            placeholder={values.length ? "" : placeholder}
            className="min-w-24 flex-1 bg-transparent text-[13px] outline-none placeholder:text-muted-foreground"
          />
        )}
        {readOnly && values.length === 0 && <span id={inputId} className="text-[13px] text-muted-foreground">None</span>}
      </div>
    </div>
  );
}
