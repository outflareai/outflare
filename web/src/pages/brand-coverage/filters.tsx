import { Button, field } from "../../ui";
import { label, type Definition } from "./client";
export type FilterGroup = {
  mode: "all" | "any";
  rules: { field: string; op: string; value: string }[];
};
export const emptyFilters: FilterGroup = { mode: "all", rules: [] };
export function FilterBuilder({
  definition,
  value,
  onChange,
}: {
  definition: Definition;
  value: FilterGroup;
  onChange: (v: FilterGroup) => void;
}) {
  const fields = [
    ...definition.fields.map((f) => f.name),
    "created_at",
    "updated_at",
    ...(definition.name === "placements" ? ["health"] : []),
  ];
  return (
    <details className="rounded-lg border p-3">
      <summary className="cursor-pointer text-sm">
        Advanced filters
        {value.rules.length ? ` · ${value.rules.length} rules` : ""}
      </summary>
      <div className="mt-3 space-y-2">
        <label className="flex items-center gap-2 text-sm">
          Match
          <select
            className={`${field} !w-auto`}
            aria-label="Filter match mode"
            value={value.mode}
            onChange={(e) =>
              onChange({ ...value, mode: e.target.value as "all" | "any" })
            }
          >
            <option value="all">All conditions</option>
            <option value="any">Any condition</option>
          </select>
        </label>
        {value.rules.map((r, i) => (
          <div key={i} className="flex flex-wrap gap-2">
            <select
              className={`${field} !w-auto`}
              aria-label={`Filter ${i + 1} field`}
              value={r.field}
              onChange={(e) =>
                onChange({
                  ...value,
                  rules: value.rules.map((x, j) =>
                    j === i
                      ? { field: e.target.value, op: "eq", value: "" }
                      : x,
                  ),
                })
              }
            >
              {fields.map((f) => (
                <option key={f} value={f}>
                  {label(f)}
                </option>
              ))}
            </select>
            <select
              className={`${field} !w-auto`}
              aria-label={`Filter ${i + 1} operator`}
              value={r.op}
              onChange={(e) =>
                onChange({
                  ...value,
                  rules: value.rules.map((x, j) =>
                    j === i ? { ...x, op: e.target.value } : x,
                  ),
                })
              }
            >
              {["eq", "ne", "contains", "before", "after", "empty"].map(
                (op) => (
                  <option key={op} value={op}>
                    {
                      {
                        eq: "is",
                        ne: "is not",
                        contains: "contains",
                        before: "less than / before",
                        after: "greater than / after",
                        empty: "is empty",
                      }[op as "eq"]
                    }
                  </option>
                ),
              )}
            </select>
            {r.op !== "empty" && (
              <FilterValue
                definition={definition}
                name={r.field}
                index={i}
                value={r.value}
                onChange={(v) =>
                  onChange({
                    ...value,
                    rules: value.rules.map((x, j) =>
                      j === i ? { ...x, value: v } : x,
                    ),
                  })
                }
              />
            )}
            <Button
              variant="ghost"
              onClick={() =>
                onChange({
                  ...value,
                  rules: value.rules.filter((_, j) => j !== i),
                })
              }
            >
              Remove
            </Button>
          </div>
        ))}
        <Button
          variant="ghost"
          disabled={value.rules.length >= 12}
          onClick={() =>
            onChange({
              ...value,
              rules: [
                ...value.rules,
                { field: fields[0], op: "eq", value: "" },
              ],
            })
          }
        >
          Add condition
        </Button>
      </div>
    </details>
  );
}

function FilterValue({
  definition,
  name,
  index,
  value,
  onChange,
}: {
  definition: Definition;
  name: string;
  index: number;
  value: string;
  onChange: (v: string) => void;
}) {
  const f = definition.fields.find((f) => f.name === name),
    choices =
      name === "health"
        ? ["unchecked", "up", "gone", "redirected", "blocked", "unreachable"]
        : f?.kind === "bool"
          ? ["true", "false"]
          : (f?.choices ?? []);
  const aria = `Filter ${index + 1} value`;
  if (choices.length)
    return (
      <select
        className={`${field} !w-auto`}
        aria-label={aria}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      >
        <option value="">Choose value…</option>
        {choices.map((v) => (
          <option key={v} value={v}>
            {v === "true" ? "Yes" : v === "false" ? "No" : label(v)}
          </option>
        ))}
      </select>
    );
  return (
    <input
      className={`${field} max-w-xs`}
      aria-label={aria}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      type={
        f?.kind === "date" || name.endsWith("_at")
          ? "date"
          : ["money", "number", "hours", "count"].includes(f?.kind ?? "")
            ? "number"
            : "text"
      }
      placeholder={f?.kind === "uuid" ? "Linked record ID" : "Value"}
    />
  );
}
