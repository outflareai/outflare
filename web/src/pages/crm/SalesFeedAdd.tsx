import { useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { Plus } from "lucide-react";
import { Button, ErrorText, field } from "@/ui";

type Kind = Schemas["SalesFeedKind"];
export type SlackChannel = { id: string; name: string; private: boolean };
export type SignalType = { key: string; label: string };
export type NewFeedChannel = {
  channel_id: string;
  channel_name: string;
  kind: Kind;
  signal_type: string;
  read_back_days: number;
};

export const salesFeedKinds: [Kind, string][] = [
  ["new_person", "New person"],
  ["handover", "Handover"],
  ["signal", "Signal"],
  ["context", "Context only"],
];

export function KindSelect({
  value,
  onChange,
  label,
  disabled,
}: {
  value: Kind;
  onChange: (v: Kind) => void;
  label?: string;
  disabled?: boolean;
}) {
  return (
    <select
      aria-label={label}
      className={field}
      disabled={disabled}
      value={value}
      onChange={(e) => onChange(e.target.value as Kind)}
    >
      {salesFeedKinds.map(([k, name]) => (
        <option key={k} value={k}>
          {name}
        </option>
      ))}
    </select>
  );
}

// The brand's signal types, keeping a saved key the list no longer has.
export function SignalTypeSelect({
  types,
  value,
  onChange,
  label,
  disabled,
}: {
  types: SignalType[];
  value: string;
  onChange: (v: string) => void;
  label?: string;
  disabled?: boolean;
}) {
  return (
    <select
      aria-label={label}
      className={field}
      disabled={disabled}
      value={value}
      onChange={(e) => onChange(e.target.value)}
    >
      {!types.some((t) => t.key === value) && (
        <option value={value}>{value}</option>
      )}
      {types.map((t) => (
        <option key={t.key} value={t.key}>
          {t.label}
        </option>
      ))}
    </select>
  );
}

// Adding a Slack channel to the table; it is saved with the rest.
export function SalesFeedAdd({
  available,
  types,
  error,
  onAdd,
}: {
  available?: SlackChannel[];
  types: SignalType[];
  error: string;
  onAdd: (row: NewFeedChannel) => void;
}) {
  const [id, setID] = useState("");
  const [kind, setKind] = useState<Kind>("new_person");
  const [signal, setSignal] = useState("upsell");
  const [days, setDays] = useState(3);
  const label = (text: string) => (
    <span className="block text-xs text-muted-foreground">{text}</span>
  );
  function add() {
    const channel = available?.find((c) => c.id === id);
    if (!channel) return;
    onAdd({
      channel_id: channel.id,
      channel_name: channel.name,
      kind,
      signal_type: signal,
      read_back_days: days,
    });
    setID("");
  }
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4">
      <h3 className="text-sm font-medium">Add channel</h3>
      {error ? (
        <ErrorText>Slack's channels couldn't be listed: {error}</ErrorText>
      ) : available === undefined ? (
        <p className="text-sm text-muted-foreground">Loading Slack channels…</p>
      ) : (
        <div className="flex flex-wrap items-end gap-3 text-sm">
          <label className="space-y-1">
            {label("Slack channel")}
            <select
              className={`${field} min-w-48`}
              value={id}
              onChange={(e) => setID(e.target.value)}
            >
              <option value="">
                {available.length ? "Choose a channel" : "No other channels"}
              </option>
              {available.map((c) => (
                <option key={c.id} value={c.id}>
                  #{c.name}
                  {c.private ? " (private)" : ""}
                </option>
              ))}
            </select>
          </label>
          <label className="space-y-1">
            {label("Kind")}
            <KindSelect value={kind} onChange={setKind} />
          </label>
          {kind === "signal" && (
            <label className="space-y-1">
              {label("Signal type")}
              <SignalTypeSelect
                types={types}
                value={signal}
                onChange={setSignal}
              />
            </label>
          )}
          <label className="space-y-1">
            {label("Read back")}
            <select
              className={field}
              value={days}
              onChange={(e) => setDays(Number(e.target.value))}
            >
              {[0, 1, 2, 3, 4, 5, 6, 7].map((d) => (
                <option key={d} value={d}>
                  {d === 0
                    ? "New posts only"
                    : `${d} ${d === 1 ? "day" : "days"}`}
                </option>
              ))}
            </select>
          </label>
          <Button variant="ghost" disabled={!id} onClick={add}>
            <Plus className="size-3.5" /> Add channel
          </Button>
        </div>
      )}
      <p className="text-xs text-muted-foreground">
        Invite the Slack app to a channel before adding it. Private channels
        show here once the app is in them.
      </p>
    </section>
  );
}
