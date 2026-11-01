import { useState } from "react";
import { X } from "lucide-react";
import { Badge, Button, field } from "../../ui";
export function TagEditor({
  tags,
  onChange,
  label = "Tags",
}: {
  tags: string[];
  onChange: (tags: string[]) => void;
  label?: string;
}) {
  const [value, setValue] = useState("");
  function add() {
    const next = [
      ...new Set([
        ...tags,
        ...value
          .split(",")
          .map((x) => x.trim())
          .filter(Boolean),
      ]),
    ].slice(0, 20);
    if (next.some((x) => x.length > 40)) return;
    onChange(next);
    setValue("");
  }
  return (
    <div className="space-y-2">
      <label className="block text-xs font-medium">
        {label}
        <div className="mt-1 flex gap-2">
          <input
            className={field}
            value={value}
            maxLength={200}
            placeholder="Add a tag, then press Enter"
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                add();
              }
            }}
          />
          <Button
            type="button"
            variant="ghost"
            onClick={add}
            disabled={!value.trim()}
          >
            Add
          </Button>
        </div>
      </label>
      <div className="flex flex-wrap gap-1">
        {tags.map((t) => (
          <span
            key={t}
            className="inline-flex items-center gap-1 rounded-full border bg-muted px-2 py-1 text-xs"
          >
            {t}
            <button
              type="button"
              aria-label={`Remove ${t}`}
              onClick={() => onChange(tags.filter((x) => x !== t))}
            >
              <X className="size-3" />
            </button>
          </span>
        ))}
      </div>
      <p className="text-xs text-muted-foreground">
        Up to 20 tags. Use tags to organise and filter work and reports.
      </p>
    </div>
  );
}
export function Tags({ tags }: { tags: string[] }) {
  return (
    <span className="flex flex-wrap gap-1">
      {tags.map((t) => (
        <Badge key={t} tone="grey">
          {t}
        </Badge>
      ))}
    </span>
  );
}
export function StateBadge({ status }: { status: string }) {
  const tone = ["failed", "dead", "regressed"].includes(status)
    ? "red"
    : ["succeeded", "done", "healthy", "ready", "good"].includes(status)
      ? "green"
      : ["running", "in_progress", "queued", "partial_history"].includes(status)
        ? "amber"
        : "grey";
  return (
    <Badge tone={tone}>
      {status === "dead" ? "Failed" : status.replaceAll("_", " ")}
    </Badge>
  );
}
export function ErrorRecovery({
  message,
  onRetry,
}: {
  message: string;
  onRetry: () => void;
}) {
  return (
    <div
      role="alert"
      className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm"
    >
      <p>{message}</p>
      <Button variant="ghost" onClick={onRetry}>
        Try again
      </Button>
    </div>
  );
}
