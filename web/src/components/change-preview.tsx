import { plural } from "../format";

type StepBody = { subject?: string; body?: string; delay_days?: number };

// A readable view of what a held change will do: the emails of a sequence,
// how many leads it adds, and its other settings.
// hide leaves fields out, such as ids nobody can read or one shown elsewhere.
export function ChangePreview({ body, hide }: { body: unknown; hide?: (key: string) => boolean }) {
  if (typeof body !== "object" || body === null || Array.isArray(body)) return null;
  const b = body as Record<string, unknown>;
  const steps = Array.isArray(b.steps) ? (b.steps as StepBody[]) : [];
  const shown = Object.entries(b).filter(([k]) => !hide?.(k));
  const lists = shown.filter(([, v]) => Array.isArray(v) && v !== b.steps) as [string, unknown[]][];
  const fields = shown.filter(([, v]) => v !== null && ["string", "number", "boolean"].includes(typeof v));
  if (steps.length === 0 && lists.length === 0 && fields.length === 0) return null;
  const label = (k: string) => k.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase());
  return (
    <div className="mt-3 space-y-3 rounded-md border bg-muted/30 p-3 text-[13px]">
      {(fields.length > 0 || lists.length > 0) && (
        <dl className="grid gap-x-4 gap-y-1 sm:grid-cols-[9rem_1fr]">
          {fields.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-muted-foreground">{label(k)}</dt>
              <dd className="min-w-0 break-words">{String(v)}</dd>
            </div>
          ))}
          {lists.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-muted-foreground">{label(k)}</dt>
              <dd>{plural(v.length, "item")}</dd>
            </div>
          ))}
        </dl>
      )}
      {steps.length > 0 && (
        <ol className="space-y-2">
          {steps.map((st, i) => (
            <li key={i} className="rounded border bg-background p-2">
              <div className="text-[12px] text-muted-foreground">
                Email {i + 1}
                {i > 0 && ` · ${st.delay_days ? `${plural(st.delay_days, "day")} after the last` : "same day"}`}
                {i > 0 && !st.subject && " · replies in the same thread"}
              </div>
              {st.subject && <div className="font-medium">{st.subject}</div>}
              {st.body && <p className="mt-1 whitespace-pre-wrap text-muted-foreground">{st.body}</p>}
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}
