import { useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import {
  Archive,
  ArrowRight,
  ExternalLink,
  Mail,
  RotateCcw,
} from "lucide-react";
import { Button, Empty, Loading, field } from "@/ui";
import { salesExternalURL, salesSections } from "@/lib/sales";
import { track } from "@/lib/analytics";
import { SalesSourceBadge, salesFeedSource } from "./SalesFeedEntry";

import {
  SalesLocation,
  SalesAssignment,
  SalesFeedback,
  SalesQuality,
  SalesViewed,
  SalesSeen,
  salesMarkSeen,
  salesQualityNames,
} from "./SalesCollaboration";
import { salesFieldLabel } from "./SalesFeedEntry";

type Lead = Schemas["SalesLead"];
type Member = Schemas["SalesMember"];
// Which conversations the queue shows by where they came from: every one,
// those a named tool posted, or those that started from an email reply.
// Several can be on at once: the list shows conversations from any of them.
export type SalesSourceFilter = { sources: string[]; reply: boolean };
// New conversations by who holds them: everyone's, nobody's, mine, or one
// teammate's (their user id).
export type SalesAssignmentFilter = string;
export type SalesOwnerCount = { owner_id: string; count: number };
// Which conversations the queue shows by a rep's mark: any, one mark, or
// those nobody marked.
export type SalesCategoryFilter = "" | "inbound" | "outbound";
export type SalesQualityFilter =
  "" | keyof typeof salesQualityNames | "unrated";
// A row counts as just arrived for this long after it was created.
const justInMs = 10 * 60 * 1000;
export type SalesFeedOption = { id: string; channel_name: string };
const labels: Record<string, string> = {
  all: "All",
  open: "Open",
  new: "Not started",
  active: "Active",
  customer: "Customers",
  archived: "Archived",
};
const activityNames: Record<string, string> = {
  lead_arrived: "Conversation arrived",
  lead_taken: "Conversation taken",
  handoff_queued: "Account manager introduction queued",
  customer_confirmed: "Customer confirmed",
  lead_archived: "Conversation archived",
  lead_reopened: "Conversation reopened",
  details_updated: "Shared details updated",
  quality_set: "Quality marked",
  message_received: "Message received",
  draft_saved: "Reply draft saved",
  template_applied: "Template applied to draft",
  send_sent: "Message sent",
  send_failed: "Message failed",
  send_uncertain: "Message delivery needs review",
  send_retry_queued: "Send retry queued",
  reply_queued: "Reply queued",
};
const date = (v: string) =>
  new Date(v).toLocaleString(undefined, {
    dateStyle: "full",
    timeStyle: "long",
  });

// onOpened runs when one of the links is opened: in the queue, looking at
// someone's website, LinkedIn or address counts as having seen them.
export function SalesSources({
  lead,
  onOpened,
}: {
  lead: Lead;
  onOpened?: () => void;
}) {
  const website = salesExternalURL(lead.website);
  const linkedin = salesExternalURL(lead.linkedin_url);
  return (
    <div
      className="flex flex-wrap gap-x-4 gap-y-2 text-xs text-muted-foreground"
      aria-label="Conversation sources"
    >
      {website && (
        <a
          href={website}
          onClick={() => {
            track("ui.sales_source_opened", { kind: "website" });
            onOpened?.();
          }}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 hover:text-foreground hover:underline"
        >
          Website <ExternalLink className="size-3" />
        </a>
      )}
      {linkedin && (
        <a
          href={linkedin}
          onClick={() => {
            track("ui.sales_source_opened", { kind: "linkedin" });
            onOpened?.();
          }}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 hover:text-foreground hover:underline"
        >
          LinkedIn <ExternalLink className="size-3" />
        </a>
      )}
      {lead.email && (
        <a
          href={`mailto:${lead.email}`}
          onClick={() => {
            track("ui.sales_source_opened", { kind: "email" });
            onOpened?.();
          }}
          className="inline-flex items-center gap-1 hover:text-foreground hover:underline"
        >
          Email <Mail className="size-3" />
        </a>
      )}
    </div>
  );
}

export function SalesQueue({
  brand,
  assignment,
  onAssignmentFilter,
  newOwners,
  sinceSeen,
  onRefresh,
  section,
  leads,
  team,
  manager,
  representative,
  loading,
  updating,
  userID,
  state,
  person,
  onFilter,
  source,
  sources,
  feedChannel,
  feedChannels,
  slackChannels,
  onSourceFilter,
  quality,
  onQualityFilter,
  category,
  onCategoryFilter,
  onOpen,
  onNavigate,
  onArchive,
  onReopen,
}: {
  brand: string;
  assignment: SalesAssignmentFilter;
  onAssignmentFilter: (value: SalesAssignmentFilter) => void;
  newOwners: SalesOwnerCount[];
  // New conversations since this person last looked at one, and when that was.
  sinceSeen: { count: number; at?: string };
  onRefresh: () => unknown;
  section: string;
  leads: Lead[];
  team: Member[];
  manager: boolean;
  representative: boolean;
  loading: boolean;
  updating?: string;
  userID?: string;
  state: string;
  person: string;
  onFilter: (state: string, person: string) => void;
  source: SalesSourceFilter;
  sources: string[];
  feedChannel: string[];
  feedChannels: SalesFeedOption[];
  slackChannels: boolean;
  onSourceFilter: (source: SalesSourceFilter, feedChannel: string[]) => void;
  quality: SalesQualityFilter;
  onQualityFilter: (quality: SalesQualityFilter) => void;
  // The person's lead source category: any, inbound or outbound.
  category: SalesCategoryFilter;
  onCategoryFilter: (category: SalesCategoryFilter) => void;
  onOpen: (id: string) => void;
  onNavigate: (section: string) => void;
  onArchive: (lead: Lead) => Promise<void>;
  onReopen: (lead: Lead) => Promise<void>;
}) {
  const arrivals = section === "new";
  const overview = section === "all";
  const sourceFiltered =
    source.sources.length > 0 ||
    source.reply ||
    feedChannel.length > 0 ||
    !!quality ||
    !!category;
  // Conversations that arrived while this list was open. The first list of a
  // view is what was already there; a row that turns up later, newly made,
  // is marked "Just in" until the view changes.
  const view = [
    section,
    state,
    person,
    assignment,
    source.sources.join("\n"),
    source.reply,
    feedChannel.join(","),
    quality,
    category,
  ].join("|");
  const known = useRef<{
    view: string;
    ids: Set<string> | null;
    stale: Lead[];
  } | null>(null);
  const [justIn, setJustIn] = useState<Set<string>>(new Set());
  useEffect(() => {
    if (loading) return;
    if (known.current?.view !== view) {
      // The list on screen is still the previous view's when one was shown:
      // what was already there is taken from the next list to load.
      known.current = known.current
        ? { view, ids: null, stale: leads }
        : { view, ids: new Set(leads.map((l) => l.id)), stale: leads };
      setJustIn(new Set());
      return;
    }
    if (!known.current.ids) {
      if (leads === known.current.stale) return;
      known.current.ids = new Set(leads.map((l) => l.id));
      return;
    }
    const ids = known.current.ids;
    const fresh = leads.filter(
      (l) => !ids.has(l.id) && Date.now() - Date.parse(l.created_at) < justInMs,
    );
    leads.forEach((l) => ids.add(l.id));
    if (fresh.length > 0) {
      setJustIn((old) => new Set([...old, ...fresh.map((l) => l.id)]));
      track("ui.sales_arrivals_shown", { count: fresh.length });
    }
  }, [leads, loading, view]);
  const filtered =
    (overview && (state !== "all" || !!person)) ||
    (arrivals && assignment !== "all") ||
    sourceFiltered;
  // A conversation can outlive its removed channel, so a row from Slack
  // brings the Source column back even when the brand reads none now.
  const showSource = slackChannels || leads.some((l) => !!l.feed_kind);
  // Where a conversation came from: a post in a Sales channel, or a reply.
  // Shown only when the brand reads channels that start conversations.
  // Chips toggle: any number can be on, and All turns the rest off.
  const chip = (
    label: string,
    on: boolean,
    onClick: () => void,
    key = label,
  ) => (
    <button
      key={key}
      type="button"
      aria-pressed={on}
      className={`h-7 max-w-[14rem] truncate rounded-full border px-3 text-xs ${on ? "border-foreground bg-foreground text-background" : "text-muted-foreground hover:bg-muted/50 hover:text-foreground"}`}
      onClick={onClick}
    >
      {label}
    </button>
  );
  const toggled = (all: string[], v: string) =>
    all.includes(v) ? all.filter((x) => x !== v) : [...all, v];
  const sourceFilters = slackChannels && (
    <div
      className="flex flex-wrap items-center gap-2"
      aria-label="Filter by source"
    >
      <span className="text-xs text-muted-foreground">Source</span>
      {chip("All", source.sources.length === 0 && !source.reply, () =>
        onSourceFilter({ sources: [], reply: false }, feedChannel),
      )}
      {sources.map((name) =>
        chip(
          name,
          source.sources.includes(name),
          () =>
            onSourceFilter(
              { ...source, sources: toggled(source.sources, name) },
              feedChannel,
            ),
          `source:${name}`,
        ),
      )}
      {chip("Email reply", source.reply, () =>
        onSourceFilter({ ...source, reply: !source.reply }, feedChannel),
      )}
      <div className="ml-auto flex flex-wrap items-center gap-2">
        <select
          className={`${field} max-w-full`}
          aria-label="Filter by quality mark"
          value={quality}
          onChange={(e) =>
            onQualityFilter(e.target.value as SalesQualityFilter)
          }
        >
          <option value="">Any mark</option>
          {Object.entries(salesQualityNames).map(([value, name]) => (
            <option key={value} value={value}>
              {name}
            </option>
          ))}
          <option value="unrated">Not marked</option>
        </select>
      </div>
      {feedChannels.length > 1 && (
        <div
          className="flex basis-full flex-wrap items-center gap-2"
          aria-label="Filter by Sales channel"
        >
          <span className="text-xs text-muted-foreground">Channel</span>
          {chip(
            "All",
            feedChannel.length === 0,
            () => onSourceFilter(source, []),
            "channel:all",
          )}
          {feedChannels.map((c) =>
            chip(
              `#${c.channel_name}`,
              feedChannel.includes(c.id),
              () => onSourceFilter(source, toggled(feedChannel, c.id)),
              `channel:${c.id}`,
            ),
          )}
        </div>
      )}
    </div>
  );
  const empty =
    overview || arrivals
      ? [
          "No new conversations yet",
          "New Sales conversations will appear here with the prospect’s reply. Open any conversation to read its full thread and see who is handling it.",
        ]
      : section === "inbox"
        ? [
            "All caught up",
            "New introductions and replies to your conversations will appear here.",
          ]
        : section === "handoffs"
          ? [
              "No handoffs yet",
              "Conversations you introduce to an account manager will appear here.",
            ]
          : ["open", "new", "active"].includes(state)
            ? [
                "No open conversations yet",
                "Open New conversations to read the incoming conversations and choose one to take.",
              ]
            : [
                `No ${state === "customer" ? "customers" : "archived conversations"} here yet`,
                "Your conversations stay available here after you finish working them.",
              ];
  return (
    <>
      <header className="space-y-2">
        <h2 className="text-2xl font-semibold tracking-tight">
          {salesSections[section]}
        </h2>
      </header>
      {arrivals && sinceSeen.count > 0 && (
        <p className="flex flex-wrap items-center gap-2 text-sm" role="status">
          <span className="rounded-full bg-emerald-500 px-2 py-0.5 text-xs font-semibold tabular-nums text-white">
            {sinceSeen.count >= 1000 ? "999+" : sinceSeen.count}
          </span>
          <span>
            new{" "}
            {sinceSeen.at
              ? `since the last one you looked at (${new Date(
                  sinceSeen.at,
                ).toLocaleString(undefined, {
                  month: "short",
                  day: "numeric",
                  hour: "2-digit",
                  minute: "2-digit",
                })})`
              : "conversations you have not looked at yet"}
          </span>
          <span className="text-xs text-muted-foreground">
            Marked with a green edge below.
          </span>
        </p>
      )}
      {arrivals && (
        <div
          className="flex flex-wrap items-center gap-2"
          aria-label="Assignment filters"
        >
          {(["all", "unassigned", "mine"] as const).map((value) => (
            <Button
              key={value}
              variant={assignment === value ? "primary" : "ghost"}
              onClick={() => onAssignmentFilter(value)}
            >
              {value === "all"
                ? "All new"
                : value === "mine"
                  ? "Assigned to me"
                  : "Unassigned"}
            </Button>
          ))}
          {/* Anyone's share, with how many each teammate holds. */}
          <select
            className={`${field} max-w-full ${["all", "unassigned", "mine"].includes(assignment) ? "" : "border-foreground"}`}
            aria-label="Assigned to a teammate"
            value={
              ["all", "unassigned", "mine"].includes(assignment)
                ? ""
                : assignment
            }
            onChange={(e) => onAssignmentFilter(e.target.value || "all")}
          >
            <option value="">Assigned to…</option>
            {team
              .filter(
                (m) =>
                  m.role === "sdr" ||
                  m.role === "ae" ||
                  newOwners.some((o) => o.owner_id === m.user_id),
              )
              .map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  {m.name} (
                  {newOwners.find((o) => o.owner_id === m.user_id)?.count ?? 0})
                </option>
              ))}
          </select>
        </div>
      )}
      {(overview || section === "mine") && (
        <div
          className="flex flex-wrap items-center gap-2"
          aria-label="Conversation filters"
        >
          {(overview
            ? ["all", "new", "active", "customer", "archived"]
            : ["open", "new", "active", "customer", "archived"]
          ).map((value) => (
            <Button
              key={value}
              variant={state === value ? "primary" : "ghost"}
              onClick={() => onFilter(value, person)}
            >
              {labels[value]}
            </Button>
          ))}
          {overview && (
            <select
              className={`${field} ml-auto max-w-full`}
              aria-label="Filter by Sales teammate"
              value={person}
              onChange={(e) => onFilter(state, e.target.value)}
            >
              <option value="">Anyone</option>
              {team.map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  {m.name}
                </option>
              ))}
            </select>
          )}
        </div>
      )}
      {(arrivals || overview || section === "mine") && (
        <div
          className="flex flex-wrap items-center gap-2"
          aria-label="Filter by lead source category"
        >
          <span className="text-xs text-muted-foreground">Lead source</span>
          {chip("All", !category, () => onCategoryFilter(""), "category:all")}
          {chip(
            "Inbound",
            category === "inbound",
            () => onCategoryFilter(category === "inbound" ? "" : "inbound"),
            "category:inbound",
          )}
          {chip(
            "Outbound",
            category === "outbound",
            () => onCategoryFilter(category === "outbound" ? "" : "outbound"),
            "category:outbound",
          )}
        </div>
      )}
      {(arrivals || overview || section === "mine") && sourceFilters}
      {loading ? (
        <Loading />
      ) : leads.length === 0 ? (
        <Empty
          title={filtered ? "No conversations match these filters" : empty[0]}
        >
          {filtered ? (
            <Button
              variant="ghost"
              onClick={() => {
                if (arrivals && assignment !== "all") onAssignmentFilter("all");
                if (overview && (state !== "all" || person))
                  onFilter("all", "");
                if (sourceFiltered) {
                  onSourceFilter({ sources: [], reply: false }, []);
                  onQualityFilter("");
                  onCategoryFilter("");
                }
              }}
            >
              Show all conversations
            </Button>
          ) : (
            <>
              <p>{empty[1]}</p>
              {section === "mine" &&
                ["open", "new", "active"].includes(state) && (
                  <Button variant="ghost" onClick={() => onNavigate("new")}>
                    Open New conversations <ArrowRight className="size-3.5" />
                  </Button>
                )}
            </>
          )}
        </Empty>
      ) : (
        <div className="overflow-x-auto rounded-xl border bg-card">
          <table className="w-full text-left text-sm">
            <thead className="border-b bg-muted/20 text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-3 font-medium">Person</th>
                <th className="min-w-48 px-3 py-3 font-medium">
                  {arrivals
                    ? slackChannels
                      ? "Details"
                      : "Their reply"
                    : "Latest message"}
                </th>
                {showSource && (
                  <th className="px-3 py-3 font-medium">Source</th>
                )}
                <th className="px-3 py-3 font-medium">Owner & status</th>
                <th className="px-3 py-3">
                  <span className="sr-only">Conversation actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {leads.map((lead) => {
                const preview = arrivals
                  ? lead.latest_reply
                  : lead.latest_message;
                // The newest Slack post shows when there is no email yet, or
                // when it is newer than the latest email.
                const postFields = lead.latest_post?.fields ?? [];
                const post =
                  lead.latest_post &&
                  (lead.latest_post.body.trim() || postFields.length > 0)
                    ? lead.latest_post
                    : null;
                const shown =
                  post &&
                  (!preview ||
                    Date.parse(post.occurred_at) >
                      Date.parse(preview.occurred_at))
                    ? { body: post.body, occurred_at: post.occurred_at }
                    : preview;
                const fromPost = !!shown && shown !== preview;
                // A form or ticket leads with the fields its channel shows
                // first, not with a sentence about what the form is.
                const keyFields = fromPost ? postFields : [];
                const previewBody = shown?.body.replace(/\s+/g, " ").trim();
                const messageAt = shown?.occurred_at || lead.created_at;
                const messageLabel = fromPost
                  ? "Posted in Slack"
                  : preview
                    ? preview.direction === "incoming"
                      ? "Replied"
                      : "Sent"
                    : "Added";
                const owner = team.find((m) => m.user_id === lead.owner_id);
                const actor = team.find(
                  (m) => m.user_id === lead.last_activity?.actor_id,
                );
                const ownLead = lead.owner_id === userID;
                // Not opened by you yet and still waiting to be picked up.
                const isNew = lead.unread && lead.state === "new";
                const fresh = justIn.has(lead.id);
                // Arrived after the last conversation this person looked at.
                const sinceLast =
                  arrivals &&
                  lead.state === "new" &&
                  sinceSeen.count > 0 &&
                  (!sinceSeen.at ||
                    Date.parse(lead.created_at) > Date.parse(sinceSeen.at));
                const canArchive =
                  (lead.state === "new" &&
                    (manager ||
                      (representative && (!lead.owner_id || ownLead)))) ||
                  (lead.state === "active" && ownLead);
                const canReopen =
                  lead.state === "archived" &&
                  (ownLead || (manager && !lead.owner_id));
                return (
                  <tr
                    key={lead.id}
                    className={`cursor-pointer border-b align-top last:border-0 hover:bg-muted/30 ${fresh ? "bg-emerald-500/10" : isNew ? "bg-emerald-500/[0.04]" : ""} ${sinceLast ? "shadow-[inset_3px_0_0_0_rgb(16_185_129)]" : ""}`}
                    onClick={(e) => {
                      if (
                        e.currentTarget.contains(e.target as Node) &&
                        !(e.target as HTMLElement).closest(
                          "button,a,input,select,textarea,[role=dialog]",
                        )
                      )
                        onOpen(lead.id);
                    }}
                  >
                    <td className="min-w-40 px-3 py-3">
                      <button
                        className="text-left hover:underline"
                        onClick={() => onOpen(lead.id)}
                      >
                        <strong
                          className={`flex items-center gap-2 ${lead.unread ? "font-semibold" : "font-medium"}`}
                        >
                          {isNew || fresh ? (
                            <span className="shrink-0 rounded bg-emerald-500 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-white">
                              {fresh ? "Just in" : "New"}
                            </span>
                          ) : (
                            lead.unread && (
                              <span
                                className="size-1.5 shrink-0 rounded-full bg-emerald-400"
                                aria-label="Unread updates"
                              />
                            )
                          )}
                          {lead.company || lead.name}
                        </strong>
                        {lead.company && (
                          <span className="mt-1 block text-xs text-muted-foreground">
                            {lead.name}
                          </span>
                        )}
                      </button>
                      <SalesLocation lead={lead} compact />
                      <div className="mt-2">
                        <SalesSources
                          lead={lead}
                          onOpened={() => {
                            if (lead.unread)
                              void salesMarkSeen(brand, lead)
                                .then(onRefresh)
                                .catch(() => {});
                          }}
                        />
                      </div>
                    </td>
                    <td className="min-w-64 max-w-xl px-3 py-3">
                      {shown ? (
                        <>
                          {keyFields.length > 0 && (
                            <dl className="space-y-1.5">
                              {keyFields.map((f, i) => (
                                <div key={i}>
                                  <dt
                                    className="truncate text-xs text-muted-foreground"
                                    title={f.label}
                                  >
                                    {salesFieldLabel(f.label)}
                                  </dt>
                                  <dd
                                    className={`line-clamp-2 leading-snug [overflow-wrap:anywhere] ${f.value ? "" : "text-muted-foreground"}`}
                                    title={f.value}
                                  >
                                    {f.value || "Not given"}
                                  </dd>
                                </div>
                              ))}
                            </dl>
                          )}
                          {(previewBody || keyFields.length === 0) && (
                            <blockquote
                              className={`line-clamp-2 whitespace-normal leading-relaxed [overflow-wrap:anywhere] ${keyFields.length > 0 ? "mt-2 border-l-2 pl-2 text-muted-foreground" : ""}`}
                              title={shown.body}
                            >
                              {previewBody ||
                                "Message available in conversation."}
                            </blockquote>
                          )}
                        </>
                      ) : lead.feed_kind ? (
                        <p className="text-muted-foreground">
                          Arrived through Slack. Open the conversation to read
                          the post.
                        </p>
                      ) : (
                        <p className="text-muted-foreground">No message yet.</p>
                      )}
                      <time
                        className="mt-2 block text-xs text-muted-foreground"
                        dateTime={messageAt}
                        title={date(messageAt)}
                      >
                        {messageLabel}{" "}
                        {new Date(messageAt).toLocaleString(undefined, {
                          month: "short",
                          day: "numeric",
                          hour: "2-digit",
                          minute: "2-digit",
                        })}
                      </time>
                      {manager && overview && lead.last_activity && (
                        <p className="mt-3 text-xs text-muted-foreground">
                          {activityNames[lead.last_activity.action] ??
                            lead.last_activity.action.replaceAll("_", " ")}
                          {actor ? ` · ${actor.name}` : ""}
                        </p>
                      )}
                    </td>
                    {showSource && (
                      <td className="max-w-44 px-3 py-3 text-xs text-muted-foreground">
                        <SalesSourceBadge
                          source={lead.feed_source}
                          kind={lead.feed_kind}
                          channelName={lead.feed_channel_name ?? ""}
                        />
                        {lead.source_category && (
                          <span className="mt-1.5 block">
                            {lead.source_category === "inbound"
                              ? "Inbound"
                              : "Outbound"}
                          </span>
                        )}
                        {lead.feed_kind && (
                          <span className="mt-1.5 block">
                            {salesFeedSource(
                              lead.feed_kind,
                              lead.feed_channel_name ?? "",
                            )}
                          </span>
                        )}
                      </td>
                    )}
                    <td className="min-w-40 px-3 py-3">
                      {(lead.state === "new" && (representative || manager)) ||
                      (lead.state === "active" &&
                        (manager || (!!userID && lead.owner_id === userID))) ? (
                        <SalesAssignment
                          brand={brand}
                          lead={lead}
                          team={team}
                          userID={userID}
                          manager={manager}
                          onSaved={onRefresh}
                        />
                      ) : (
                        <p>{owner?.name ?? "Unassigned"}</p>
                      )}
                      <p className="mt-2 text-xs text-muted-foreground">
                        {lead.state === "active"
                          ? lead.reply_state === "reply_received"
                            ? "Reply received"
                            : "Waiting for reply"
                          : labels[lead.state]}
                      </p>
                      {(representative || manager) && (
                        <div className="mt-2">
                          <SalesQuality
                            brand={brand}
                            lead={lead}
                            onSaved={onRefresh}
                          />
                        </div>
                      )}
                    </td>
                    <td className="px-3 py-3">
                      <div className="flex items-center gap-1">
                        <SalesSeen
                          brand={brand}
                          lead={lead}
                          onSaved={onRefresh}
                          compact
                        />
                        <SalesFeedback
                          brand={brand}
                          lead={lead}
                          team={team}
                          onSaved={onRefresh}
                          compact
                        />
                        {canArchive && (
                          <Button
                            variant="ghost"
                            className="h-8 px-2 text-xs"
                            disabled={!!updating}
                            title="Archive"
                            aria-label="Archive"
                            onClick={() => void onArchive(lead)}
                          >
                            <Archive className="size-3.5" />
                            <span className="hidden 2xl:inline">
                              {updating === lead.id ? "Archiving…" : "Archive"}
                            </span>
                          </Button>
                        )}
                        {canReopen && (
                          <Button
                            variant="ghost"
                            className="h-8 px-2 text-xs"
                            disabled={!!updating}
                            title="Reopen"
                            aria-label="Reopen"
                            onClick={() => void onReopen(lead)}
                          >
                            <RotateCcw className="size-3.5" />
                            <span className="hidden 2xl:inline">
                              {updating === lead.id ? "Reopening…" : "Reopen"}
                            </span>
                          </Button>
                        )}
                      </div>
                      <SalesViewed lead={lead} team={team} />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
