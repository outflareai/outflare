import { useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "@/api";
import { Button, ErrorText, field, toast } from "@/ui";
import { salesChecked as checked } from "@/lib/sales";
import type { SlackChannel } from "./SalesFeedAdd";

type Feeds = Schemas["SalesFeeds"];
type Alerts = Schemas["SalesAlerts"];

const problems: Record<string, string> = {
  slack_auth:
    "Slack refused the post. Add the bot scopes chat:write and reactions:write to your Slack app, reinstall it, and paste the new bot token into the Slack (sales channels) connection if it changed.",
  slack_channel:
    "Outflare can't post in this channel. Invite the Slack app to it, or pick another.",
};
// What each emoji on a conversation's message means.
const legend: [string, string][] = [
  ["👀", "seen"],
  ["🙋", "assigned"],
  ["💬", "introduction sent"],
  ["🤝", "handed to an account manager"],
  ["🎉", "customer"],
  ["👍", "good"],
  ["👎", "poor fit"],
  ["🗑️", "junk"],
  ["❌", "archived"],
];

// Sales alerts: every new conversation the team chooses is posted once to
// one Slack channel, and its message follows the conversation. Channel
// names come from Slack and are shown as text.
export function SalesAlerts({
  brand,
  feeds,
  slack,
  canEdit,
  onSaved,
}: {
  brand: string;
  feeds: Feeds;
  slack?: SlackChannel[];
  canEdit: boolean;
  onSaved: (alerts: Alerts) => void;
}) {
  const saved = feeds.alerts;
  const starts = feeds.channels.filter(
    (c) => c.kind === "new_person" || c.kind === "handover",
  );
  const [enabled, setEnabled] = useState(saved.enabled);
  const [channel, setChannel] = useState(saved.channel_id);
  const [replies, setReplies] = useState(saved.replies);
  const [from, setFrom] = useState<string[]>(
    starts.filter((c) => c.alert).map((c) => c.id),
  );
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setEnabled(saved.enabled);
    setChannel(saved.channel_id);
    setReplies(saved.replies);
    setFrom(
      feeds.channels
        .filter(
          (c) => (c.kind === "new_person" || c.kind === "handover") && c.alert,
        )
        .map((c) => c.id),
    );
  }, [feeds, saved]);
  // A channel Outflare reads can't be the one it posts in.
  const options = (slack ?? []).filter(
    (c) => !feeds.channels.some((r) => r.channel_id === c.id),
  );
  const known = options.some((c) => c.id === channel);
  async function save() {
    setBusy(true);
    setError("");
    try {
      const out = checked<Alerts>(
        await api.PUT("/v1/brands/{brand}/sales/feeds/alerts", {
          params: { path: { brand } },
          body: {
            enabled,
            channel_id: channel,
            replies,
            feed_channels: from,
          },
        }),
      );
      toast(enabled ? "Sales alerts are on." : "Sales alerts saved.");
      onSaved(out);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4">
      <div>
        <h3 className="text-sm font-medium">Post new conversations to Slack</h3>
        <p className="mt-1 max-w-2xl text-xs text-muted-foreground">
          One message per new conversation, in the channel you pick: who it is,
          their website, where they came from and the fields shown first. Emojis
          on the message follow the conversation. Your Slack app needs the bot
          scopes chat:write and reactions:write, and must be in the channel.
        </p>
      </div>
      {saved.enabled && saved.error_code && (
        <p className="max-w-2xl text-xs text-amber-700 dark:text-amber-400">
          {problems[saved.error_code] ?? "Slack refused the last post."}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-3 text-sm">
        <label className="inline-flex items-center gap-2">
          <input
            type="checkbox"
            disabled={!canEdit}
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          On
        </label>
        <select
          className={`${field} max-w-full`}
          aria-label="Slack channel to post in"
          disabled={!canEdit}
          value={channel}
          onChange={(e) => setChannel(e.target.value)}
        >
          <option value="">Choose a channel</option>
          {!known && channel && (
            <option value={channel}>#{saved.channel_name || channel}</option>
          )}
          {options.map((c) => (
            <option key={c.id} value={c.id}>
              #{c.name}
            </option>
          ))}
        </select>
      </div>
      <fieldset className="space-y-1.5 text-sm">
        <legend className="mb-1 text-xs text-muted-foreground">
          Post conversations from
        </legend>
        <label className="flex items-center gap-2">
          <input
            type="checkbox"
            disabled={!canEdit}
            checked={replies}
            onChange={(e) => setReplies(e.target.checked)}
          />
          Email replies
        </label>
        {starts.map((c) => (
          <label key={c.id} className="flex items-center gap-2">
            <input
              type="checkbox"
              disabled={!canEdit}
              checked={from.includes(c.id)}
              onChange={(e) =>
                setFrom((all) =>
                  e.target.checked
                    ? [...all, c.id]
                    : all.filter((id) => id !== c.id),
                )
              }
            />
            #{c.channel_name}
          </label>
        ))}
      </fieldset>
      <p className="text-xs text-muted-foreground">
        {legend.map(([emoji, meaning]) => `${emoji} ${meaning}`).join(" · ")}
      </p>
      <ErrorText>{error}</ErrorText>
      {canEdit && (
        <Button
          disabled={busy || (enabled && !channel)}
          onClick={() => void save()}
        >
          {busy ? "Saving…" : "Save alerts"}
        </Button>
      )}
    </section>
  );
}
