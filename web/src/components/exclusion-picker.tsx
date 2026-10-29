// The exclusion picker: who a journey, a broadcast or a sequence leaves
// out. People on any of the chosen lists (smart or static), and people
// with any of the tags, or at a company with one. Unlike suppression, an
// exclusion only keeps people out of the automation that names it.

import { useEffect, useState, type KeyboardEvent } from "react";
import type { Schemas } from "@outflare/sdk";
import { api } from "@/api";
import { field } from "@/ui";

export type Exclusion = Schemas["Exclusion"];

export const noExclusion: Exclusion = { lists: [], tags: [] };

type ListRef = { id: string; name: string; kind?: string };

export function ExclusionPicker({ ws, value, onChange, disabled, what }: {
  ws: string; value: Exclusion; onChange: (x: Exclusion) => void; disabled?: boolean;
  // What leaves them out, in words: "This journey never lets them in".
  what: string;
}) {
  const [lists, setLists] = useState<ListRef[]>([]);
  const [tag, setTag] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/smart-lists", { params: { path: { ws } } }).then(({ data }) => setLists((data?.data ?? []) as ListRef[]));
  }, [ws]);
  const name = (id: string) => lists.find((l) => l.id === id)?.name ?? "A deleted list";
  const addTag = () => {
    const t = tag.trim().toLowerCase();
    if (t && !value.tags.includes(t)) onChange({ ...value, tags: [...value.tags, t] });
    setTag("");
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      addTag();
    }
  };
  const chip = (text: string, remove: () => void) => (
    <span key={text} className="inline-flex items-center gap-1 rounded-full border bg-muted px-2 py-0.5 text-[12px]">
      {text}
      {!disabled && <button type="button" aria-label={`Remove ${text}`} className="text-muted-foreground hover:text-foreground" onClick={remove}>×</button>}
    </span>
  );
  return (
    <fieldset className="space-y-2 text-[13px]" disabled={disabled}>
      <legend className="mb-1 font-medium">Leave out</legend>
      <p className="text-[12px] text-muted-foreground">{what} people on these lists or with these tags, or who work at a company with one. Suppression still applies on top.</p>
      <div className="flex flex-wrap gap-1.5">
        {value.lists.map((id) => chip(name(id), () => onChange({ ...value, lists: value.lists.filter((x) => x !== id) })))}
        {value.tags.map((t) => chip(`#${t}`, () => onChange({ ...value, tags: value.tags.filter((x) => x !== t) })))}
      </div>
      <div className="flex flex-wrap gap-2">
        <select aria-label="Add a list" className={`${field} min-w-0 flex-1`} value="" onChange={(e) => e.target.value && onChange({ ...value, lists: [...value.lists, e.target.value] })}>
          <option value="">Add a list…</option>
          {lists.filter((l) => !value.lists.includes(l.id)).map((l) => <option key={l.id} value={l.id}>{l.name}</option>)}
        </select>
        <input aria-label="Add a tag" className={`${field} w-40`} value={tag} placeholder="Add a tag, such as competitor" maxLength={50}
          onChange={(e) => setTag(e.target.value)} onKeyDown={onKey} onBlur={addTag} />
      </div>
    </fieldset>
  );
}
