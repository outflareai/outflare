import { useCallback, useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { Trash2 } from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, Empty, ErrorText, Loading, confirmAction, toast } from "@/ui";
import { track } from "@/lib/analytics";
import { salesChecked as checked } from "@/lib/sales";
import { ConnectFor } from "@/components/connect-for";
import type { Tab } from "@/App";
import { SalesFeedTestDialog } from "./SalesFeedTest";
import { SalesFeedLayoutDialog, SalesSourceQuality } from "./SalesFeedLayout";
import { SalesAlerts } from "./SalesAlerts";
import type { SalesFeedStatus } from "./SalesFeedItems";
import {
  KindSelect,
  SalesFeedAdd,
  SignalTypeSelect,
  type SignalType,
  type SlackChannel,
} from "./SalesFeedAdd";

type Feeds = Schemas["SalesFeeds"];
type Channel = Schemas["SalesFeedChannel"];
type Kind = Schemas["SalesFeedKind"];
type Preview = Schemas["SalesFeedPreview"];
// One row of the table as edited: a saved channel, or one added since.
type Row = {
  channel_id: string;
  channel_name: string;
  kind: Kind;
  signal_type: string;
  read_back_days: number;
  enabled: boolean;
  saved?: Channel;
};

const feedErrors: Record<string, string> = {
  slack_auth: "Slack refused the connection. Reconnect Slack (sales channels).",
  slack_channel:
    "Outflare can't read this channel. Invite the Slack app to it, or check it still exists.",
  slack_unavailable: "Slack couldn't be reached. Outflare keeps trying.",
};

const rowsOf = (feeds: Feeds): Row[] =>
  feeds.channels.map((c) => ({
    channel_id: c.channel_id,
    channel_name: c.channel_name,
    kind: c.kind,
    signal_type: c.signal_type || "upsell",
    read_back_days: c.read_back_days,
    enabled: c.enabled,
    saved: c,
  }));
const shape = (rows: Row[]) =>
  JSON.stringify(
    rows.map((r) => [r.channel_id, r.kind, r.signal_type, r.enabled]),
  );
// A channel whose Slack connection was deleted reads again once it is saved
// with the connection in use, so Save stays available for it.
const waiting = (rows: Row[]) => rows.some((r) => r.saved?.connection_removed);

function StatusDot({ row }: { row: Row }) {
  const [tone, label] = !row.saved
    ? ["bg-muted-foreground/40", "Not saved yet"]
    : row.saved.connection_removed
      ? ["bg-amber-400", "Slack connection removed"]
      : !row.enabled
      ? ["bg-muted-foreground/40", "Off"]
      : row.saved.error_code
        ? ["bg-amber-400", "Can't read"]
        : ["bg-emerald-400", "Reading"];
  return (
    <span
      className={`inline-block size-2 shrink-0 rounded-full ${tone}`}
      role="img"
      aria-label={label}
      title={label}
    />
  );
}

// Sales channels: the Slack channels the brand reads, what each one carries,
// and the last week of posts. Only someone who can manage the team edits it;
// the rest of the Sales team sees it as it is.
export function SalesChannels({
  brand,
  canEdit,
  go,
  onList,
}: {
  brand: string;
  canEdit: boolean;
  go: (tab: Tab, item?: string) => void;
  onList: (status: SalesFeedStatus, channel?: string) => void;
}) {
  const [feeds, setFeeds] = useState<Feeds>();
  const [rows, setRows] = useState<Row[]>([]);
  const [slack, setSlack] = useState<SlackChannel[]>();
  const [slackError, setSlackError] = useState("");
  const [types, setTypes] = useState<SignalType[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [test, setTest] = useState<{
    name: string;
    previews: Preview[] | null;
    error: string;
  }>();
  const [layout, setLayout] = useState<Channel>();
  const integration = feeds?.integration_id ?? null;

  const load = useCallback(async () => {
    const [f, t] = await Promise.all([
      api.GET("/v1/brands/{brand}/sales/feeds", {
        params: { path: { brand } },
      }),
      api.GET("/v1/brands/{brand}/signal-types", {
        params: { path: { brand } },
      }),
    ]);
    const data = checked<Feeds>(f);
    setFeeds(data);
    setRows(rowsOf(data));
    setTypes(t.data?.data.map(({ key, label }) => ({ key, label })) ?? []);
  }, [brand]);
  useEffect(() => {
    void load().catch((e) => setError(errorMessage(e)));
  }, [load]);
  useEffect(() => {
    if (!canEdit || !integration) return;
    void api
      .GET("/v1/brands/{brand}/sales/feeds/channels", {
        params: { path: { brand }, query: { integration_id: integration } },
      })
      .then((r) => setSlack(checked<{ data: SlackChannel[] }>(r).data))
      .catch((e) => setSlackError(errorMessage(e)));
  }, [brand, canEdit, integration]);

  const dirty = useMemo(
    () => !!feeds && (shape(rows) !== shape(rowsOf(feeds)) || waiting(rows)),
    [feeds, rows],
  );
  const change = (id: string, patch: Partial<Row>) =>
    setRows((all) =>
      all.map((r) => (r.channel_id === id ? { ...r, ...patch } : r)),
    );

  async function save() {
    if (busy || !integration) return;
    setBusy(true);
    setError("");
    try {
      const data = checked<Feeds>(
        await api.PUT("/v1/brands/{brand}/sales/feeds", {
          params: { path: { brand } },
          body: {
            integration_id: integration,
            channels: rows.map((r) => ({
              channel_id: r.channel_id,
              kind: r.kind,
              signal_type: r.signal_type,
              read_back_days: r.read_back_days,
              enabled: r.enabled,
            })),
          },
        }),
      );
      setFeeds(data);
      setRows(rowsOf(data));
      toast("Sales channels saved.");
    } catch (e) {
      const message = errorMessage(e);
      setError(message);
      toast(message, "error");
    } finally {
      setBusy(false);
    }
  }

  async function remove(row: Row) {
    if (
      row.saved &&
      !(await confirmAction(
        `Remove #${row.channel_name}?`,
        "When you save, Outflare stops reading it. The posts it already read stay on their people, companies and conversations. Add it again later to start reading it again.",
        "Remove channel",
      ))
    )
      return;
    setRows((all) => all.filter((r) => r.channel_id !== row.channel_id));
  }

  async function runTest(row: Row) {
    if (!integration) return;
    setTest({ name: row.channel_name, previews: null, error: "" });
    try {
      const data = checked<{ data: Preview[] }>(
        await api.POST("/v1/brands/{brand}/sales/feeds/test", {
          params: { path: { brand } },
          body: {
            integration_id: integration,
            channel_id: row.channel_id,
            kind: row.kind,
          },
        }),
      ).data;
      setTest({ name: row.channel_name, previews: data, error: "" });
      track("ui.sales_feed_tested", {
        kind: row.kind,
        outcome: "success",
        previews: data.length,
      });
    } catch (e) {
      setTest({ name: row.channel_name, previews: [], error: errorMessage(e) });
      track("ui.sales_feed_tested", { kind: row.kind, outcome: "failure" });
    }
  }

  if (!feeds)
    return (
      <>
        <h2 className="text-2xl font-semibold">Sales channels</h2>
        {error ? <ErrorText>{error}</ErrorText> : <Loading />}
      </>
    );
  return (
    <>
      <header className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-2xl font-semibold">Sales channels</h2>
        <div className="flex gap-2">
          <Button variant="ghost" onClick={() => onList("needs_look")}>
            Needs a look
          </Button>
          <Button variant="ghost" onClick={() => onList("spam")}>
            Spam
          </Button>
          <Button variant="ghost" onClick={() => onList("failed")}>
            Failed
          </Button>
        </div>
      </header>
      <p className="max-w-2xl text-sm text-muted-foreground">
        Outflare reads new posts in the Slack channels you choose, such as a
        website form feed, and turns each one into a person and a Sales
        conversation, a signal or context on a person.
      </p>
      {feeds.ai !== "on" && (
        <p className="text-sm text-muted-foreground">
          Without an AI model, posts with one clear email address still work;
          others wait under Needs a look.{" "}
          <button
            type="button"
            className="underline underline-offset-2"
            onClick={() => go("ai")}
          >
            Settings, AI
          </button>
        </p>
      )}
      <ErrorText>{error}</ErrorText>
      {!integration ? (
        <section className="space-y-4 rounded-xl border bg-card p-6 text-sm">
          <p>
            Connect Slack (sales channels) to read the channels your tools post
            into.
          </p>
          {rows.length > 0 && (
            <p className="text-amber-700 dark:text-amber-400">
              {rows.length === 1
                ? "1 Sales channel waits"
                : `${rows.length} Sales channels wait`}{" "}
              for a Slack connection. The posts already read are kept; reconnect
              and save to keep reading.
            </p>
          )}
          <ol className="list-decimal space-y-1 pl-5 text-muted-foreground">
            <li>Create a Slack app at api.slack.com/apps.</li>
            <li>
              Add the bot scopes channels:read, groups:read, channels:history
              and groups:history.
            </li>
            <li>Install the app to your Slack workspace.</li>
            <li>Invite the app to each channel Outflare should read.</li>
            <li>Paste the bot token into Slack (sales channels).</li>
            <li>
              Optional: add the user scope search:read and paste the user token
              as the search token, to search Slack from a conversation.
            </li>
          </ol>
          <ConnectFor
            connector="slack_feeds"
            name="Slack (sales channels)"
            back="crm"
            item="sales:channels"
            why="Sales channels reads the Slack channels you choose and turns their posts into people and Sales conversations."
            go={go}
          />
        </section>
      ) : (
        <>
          {rows.length === 0 ? (
            <Empty title="No Sales channels yet">
              {canEdit
                ? "Add a Slack channel below, such as the one your website form posts into."
                : "A manager chooses the Slack channels Outflare reads."}
            </Empty>
          ) : (
            <div className="overflow-x-auto rounded-xl border bg-card">
              <table className="w-full text-left text-sm">
                <thead className="border-b bg-muted/20 text-xs text-muted-foreground">
                  <tr>
                    <th className="px-3 py-3 font-medium">Channel</th>
                    <th className="px-3 py-3 font-medium">Kind</th>
                    {[
                      ["Read", "Read in the last 7 days"],
                      ["Needs a look", "Needs a look, last 7 days"],
                      ["Spam", "Spam, last 7 days"],
                      ["Failed", "Failed, last 7 days"],
                    ].map(([name, hint]) => (
                      <th
                        key={name}
                        title={hint}
                        className="whitespace-nowrap px-3 py-3 text-right font-medium"
                      >
                        {name}
                      </th>
                    ))}
                    <th className="px-3 py-3 font-medium">On</th>
                    {canEdit && (
                      <th className="px-3 py-3">
                        <span className="sr-only">Channel actions</span>
                      </th>
                    )}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r) => {
                    const c = r.saved;
                    const count = (
                      n: number | undefined,
                      status?: SalesFeedStatus,
                    ) =>
                      n === undefined ? (
                        ""
                      ) : status && n > 0 && c ? (
                        <button
                          type="button"
                          className="underline underline-offset-2"
                          onClick={() => onList(status, c.id)}
                        >
                          {n}
                        </button>
                      ) : (
                        n
                      );
                    return (
                      <tr
                        key={r.channel_id}
                        className="border-b align-top last:border-0"
                      >
                        <td className="min-w-52 px-3 py-3">
                          <div className="flex items-center gap-2 font-medium">
                            <StatusDot row={r} />#{r.channel_name}
                          </div>
                          {c?.connection_removed ? (
                            <p className="mt-1 max-w-xs text-xs text-amber-700 dark:text-amber-400">
                              Slack connection removed. Reconnect and save to
                              keep reading.
                            </p>
                          ) : (
                            c?.error_code &&
                            r.enabled && (
                              <p className="mt-1 max-w-xs text-xs text-amber-700 dark:text-amber-400">
                                {feedErrors[c.error_code] ??
                                  "Outflare couldn't read this channel."}
                              </p>
                            )
                          )}
                          <p className="mt-1 text-xs text-muted-foreground">
                            {!c
                              ? `Not saved yet · reads back ${r.read_back_days} ${r.read_back_days === 1 ? "day" : "days"}`
                              : c.last_read_at
                                ? `Read ${new Date(c.last_read_at).toLocaleString()}`
                                : "Not read yet"}
                          </p>
                          {c && r.kind !== "signal" && (
                            <p className="mt-1 max-w-xs text-xs text-muted-foreground">
                              Shown first:{" "}
                              {(c.preview_fields ?? []).length > 0
                                ? (c.preview_fields ?? []).join(", ")
                                : "Outflare picks"}{" "}
                              <button
                                type="button"
                                className="underline underline-offset-2 hover:text-foreground"
                                aria-label={`Choose what #${r.channel_name} shows first`}
                                onClick={() => setLayout(c)}
                              >
                                Change
                              </button>
                            </p>
                          )}
                        </td>
                        <td className="space-y-2 px-3 py-3 [&>select]:block">
                          <KindSelect
                            label={`Kind of #${r.channel_name}`}
                            disabled={!canEdit}
                            value={r.kind}
                            onChange={(kind) => change(r.channel_id, { kind })}
                          />
                          {r.kind === "signal" && (
                            <SignalTypeSelect
                              label={`Signal type of #${r.channel_name}`}
                              disabled={!canEdit}
                              types={types}
                              value={r.signal_type}
                              onChange={(v) =>
                                change(r.channel_id, { signal_type: v })
                              }
                            />
                          )}
                          {r.kind === "signal" &&
                            types.length > 0 &&
                            !types.some((t) => t.key === r.signal_type) && (
                              <p className="max-w-xs text-xs text-amber-700 dark:text-amber-400">
                                This signal type no longer exists. Pick
                                another.
                              </p>
                            )}
                        </td>
                        <td className="px-3 py-3 text-right tabular-nums">
                          {count(c?.counts_7d.read)}
                        </td>
                        <td className="px-3 py-3 text-right tabular-nums">
                          {count(c?.counts_7d.needs_look, "needs_look")}
                        </td>
                        <td className="px-3 py-3 text-right tabular-nums">
                          {count(c?.counts_7d.spam, "spam")}
                        </td>
                        <td className="px-3 py-3 text-right tabular-nums">
                          {count(c?.counts_7d.failed, "failed")}
                        </td>
                        <td className="px-3 py-3">
                          <input
                            type="checkbox"
                            aria-label={`Read #${r.channel_name}`}
                            disabled={!canEdit}
                            checked={r.enabled}
                            onChange={(e) =>
                              change(r.channel_id, {
                                enabled: e.target.checked,
                              })
                            }
                          />
                        </td>
                        {canEdit && (
                          <td className="whitespace-nowrap px-3 py-3 text-right">
                            <Button
                              variant="ghost"
                              onClick={() => void runTest(r)}
                            >
                              Test
                            </Button>
                            <Button
                              variant="ghost"
                              aria-label={`Remove #${r.channel_name}`}
                              title="Remove"
                              onClick={() => void remove(r)}
                            >
                              <Trash2 className="size-3.5" />
                            </Button>
                          </td>
                        )}
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
          {canEdit && (
            <SalesFeedAdd
              available={
                slack &&
                slack.filter((c) => !rows.some((r) => r.channel_id === c.id))
              }
              types={types}
              error={slackError}
              onAdd={(row) =>
                setRows((all) => [...all, { ...row, enabled: true }])
              }
            />
          )}
          {canEdit && (
            <div className="flex items-center gap-3">
              <Button disabled={busy || !dirty} onClick={() => void save()}>
                {busy ? "Saving…" : "Save"}
              </Button>
              {dirty && (
                <span className="text-xs text-muted-foreground">
                  Unsaved changes
                </span>
              )}
            </div>
          )}
        </>
      )}
      {feeds?.alerts && feeds.channels.length > 0 && (
        <SalesAlerts
          brand={brand}
          feeds={feeds}
          slack={slack}
          canEdit={canEdit}
          onSaved={() => void load().catch(() => {})}
        />
      )}
      {feeds && <SalesSourceQuality rows={feeds.source_quality ?? []} />}
      {layout && (
        <SalesFeedLayoutDialog
          brand={brand}
          channel={layout}
          onClose={() => setLayout(undefined)}
          onSaved={() => void load().catch(() => {})}
        />
      )}
      {test && (
        <SalesFeedTestDialog
          channelName={test.name}
          previews={test.previews}
          error={test.error}
          onClose={() => setTest(undefined)}
        />
      )}
    </>
  );
}
