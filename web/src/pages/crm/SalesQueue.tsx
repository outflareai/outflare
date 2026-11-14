import type { Schemas } from "@outflare/sdk";
import { ArrowRight, ExternalLink, Mail } from "lucide-react";
import { Button, Empty, Loading, field } from "@/ui";
import { salesExternalURL, salesSections } from "@/lib/sales";
import { track } from "@/lib/analytics";

type Lead = Schemas["SalesLead"];
type Member = Schemas["SalesMember"];
const labels: Record<string, string> = {
  all: "All",
  new: "New",
  active: "Active",
  customer: "Customers",
  archived: "Archived",
};
const activityNames: Record<string, string> = {
  lead_arrived: "Conversation arrived",
  lead_taken: "Lead taken",
  handoff_queued: "Account manager introduction queued",
  customer_confirmed: "Customer confirmed",
  lead_archived: "Lead archived",
  lead_reopened: "Lead reopened",
  details_updated: "Shared details updated",
  message_received: "Message received",
  draft_saved: "Reply draft saved",
  template_applied: "Template applied to draft",
  send_sent: "Message sent",
  send_failed: "Message failed",
  send_uncertain: "Message delivery needs review",
  send_retry_queued: "Send retry queued",
  reply_queued: "Reply queued",
};
const date = (v: string) => new Date(v).toLocaleString();

export function SalesSources({ lead }: { lead: Lead }) {
  const website = salesExternalURL(lead.website);
  const linkedin = salesExternalURL(lead.linkedin_url);
  return (
    <div
      className="flex flex-wrap gap-x-4 gap-y-2 text-xs text-muted-foreground"
      aria-label="Lead sources"
    >
      {website && (
        <a
          href={website}
          onClick={() => track("ui.sales_source_opened", { kind: "website" })}
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
          onClick={() => track("ui.sales_source_opened", { kind: "linkedin" })}
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
          onClick={() => track("ui.sales_source_opened", { kind: "email" })}
          className="inline-flex items-center gap-1 hover:text-foreground hover:underline"
        >
          Email <Mail className="size-3" />
        </a>
      )}
    </div>
  );
}

export function SalesQueue({
  section,
  leads,
  team,
  manager,
  representative,
  connected,
  loading,
  taking,
  state,
  person,
  onFilter,
  onOpen,
  onNavigate,
  onTake,
}: {
  section: string;
  leads: Lead[];
  team: Member[];
  manager: boolean;
  representative: boolean;
  connected: boolean;
  loading: boolean;
  taking?: string;
  state: string;
  person: string;
  onFilter: (state: string, person: string) => void;
  onOpen: (id: string) => void;
  onNavigate: (section: string) => void;
  onTake: (lead: Lead) => Promise<void>;
}) {
  const arrivals = section === "new";
  const overview = section === "all";
  const empty =
    overview || arrivals
      ? [
          "No new conversations yet",
          "New Sales conversations will appear here with the prospect’s reply. Open any lead to read its full thread and see who is handling it.",
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
          : state === "active"
            ? [
                "No active leads yet",
                "Open New leads to read the incoming conversations and choose one to take.",
              ]
            : [
                `No ${state === "customer" ? "customers" : "archived leads"} here yet`,
                "Your conversations stay available here after you finish working them.",
              ];
  return (
    <>
      <header className="space-y-2">
        <h2 className="text-2xl font-semibold tracking-tight">
          {salesSections[section]}
        </h2>
        <p className="text-sm text-muted-foreground">
          {arrivals
            ? representative
              ? "Read their reply, open the conversation, then take the lead."
              : "Incoming conversations available to your SDRs and account managers. Open a lead to review it."
            : overview
              ? "Every Sales conversation, its owner and the latest activity. Open a lead to read the full thread."
              : section === "inbox"
                ? "Introductions and replies that need your attention."
                : section === "handoffs"
                  ? "Follow the conversations you introduced to an account manager."
                  : "Your conversations, shared notes and saved replies."}
        </p>
      </header>
      {arrivals && representative && !connected && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border px-4 py-3 text-sm">
          <p className="text-muted-foreground">
            You can read conversations now. Connect your email before taking a
            lead or sending a reply.
          </p>
          <Button variant="ghost" onClick={() => onNavigate("email")}>
            Connect my email
          </Button>
        </div>
      )}
      {(overview || section === "mine") && (
        <div
          className="flex flex-wrap items-center gap-2"
          aria-label="Lead filters"
        >
          {(overview
            ? ["all", "new", "active", "customer", "archived"]
            : ["active", "customer", "archived"]
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
      {loading ? (
        <Loading />
      ) : leads.length === 0 ? (
        <Empty
          title={
            overview && (state !== "all" || person)
              ? "No leads match these filters"
              : empty[0]
          }
        >
          {overview && (state !== "all" || person) ? (
            <Button variant="ghost" onClick={() => onFilter("all", "")}>
              Show all leads
            </Button>
          ) : (
            <>
              <p>{empty[1]}</p>
              {section === "mine" && state === "active" && (
                <Button variant="ghost" onClick={() => onNavigate("new")}>
                  Open New leads <ArrowRight className="size-3.5" />
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
                <th className="p-4 font-medium">Lead</th>
                <th className="min-w-64 p-4 font-medium">
                  {arrivals ? "Their reply" : "Latest message"}
                </th>
                {!arrivals && (
                  <th className="p-4 font-medium">Owner & status</th>
                )}
                <th className="p-4 font-medium">
                  {arrivals ? "Arrived" : "Updated"}
                </th>
                <th className="p-4">
                  <span className="sr-only">Open conversation</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {leads.map((lead) => {
                const preview = arrivals
                  ? lead.latest_reply
                  : lead.latest_message;
                const owner = team.find((m) => m.user_id === lead.owner_id);
                const actor = team.find(
                  (m) => m.user_id === lead.last_activity?.actor_id,
                );
                return (
                  <tr
                    key={lead.id}
                    className="cursor-pointer border-b align-top last:border-0 hover:bg-muted/30"
                    onClick={(e) => {
                      if (!(e.target as HTMLElement).closest("button,a"))
                        onOpen(lead.id);
                    }}
                  >
                    <td className="min-w-44 p-4">
                      <button
                        className="text-left hover:underline"
                        onClick={() => onOpen(lead.id)}
                      >
                        <strong className="flex items-center gap-2 font-medium">
                          {lead.unread && (
                            <span
                              className="size-1.5 shrink-0 rounded-full bg-emerald-400"
                              aria-label="Unread updates"
                            />
                          )}
                          {lead.company || lead.name}
                        </strong>
                        {lead.company && (
                          <span className="mt-1 block text-xs text-muted-foreground">
                            {lead.name}
                          </span>
                        )}
                      </button>
                      {lead.job_title && (
                        <p className="mt-1 text-xs text-muted-foreground">
                          {lead.job_title}
                        </p>
                      )}
                      <div className="mt-3">
                        <SalesSources lead={lead} />
                      </div>
                    </td>
                    <td className="max-w-xl p-4">
                      {preview ? (
                        <>
                          <p className="mb-2 text-xs text-muted-foreground">
                            {preview.direction === "incoming"
                              ? `Reply from ${lead.name}`
                              : preview.kind === "handoff"
                                ? "Account manager introduction"
                                : preview.kind === "introduction"
                                  ? "Introduction"
                                  : "Latest outgoing reply"}
                          </p>
                          <blockquote className="line-clamp-4 whitespace-pre-line border-l-2 border-emerald-500/40 pl-3 leading-relaxed">
                            {preview.body ||
                              "Open the conversation to view this message."}
                          </blockquote>
                        </>
                      ) : (
                        <p className="text-muted-foreground">
                          No recorded message yet. Open the lead for its shared
                          details.
                        </p>
                      )}
                      {manager && overview && lead.last_activity && (
                        <p className="mt-3 text-xs text-muted-foreground">
                          {activityNames[lead.last_activity.action] ??
                            lead.last_activity.action.replaceAll("_", " ")}
                          {actor ? ` · ${actor.name}` : ""}
                        </p>
                      )}
                    </td>
                    {!arrivals && (
                      <td className="min-w-36 p-4">
                        <p>{owner?.name ?? "Available to take"}</p>
                        <span className="mt-2 inline-block rounded-md border px-2 py-1 text-xs">
                          {lead.state === "customer"
                            ? "Customer"
                            : labels[lead.state]}
                        </span>
                        {lead.state === "active" && (
                          <p className="mt-2 text-xs text-muted-foreground">
                            {lead.reply_state === "reply_received"
                              ? "Reply received"
                              : "Waiting for reply"}
                          </p>
                        )}
                      </td>
                    )}
                    <td className="min-w-32 p-4 text-xs text-muted-foreground">
                      <time
                        dateTime={arrivals ? lead.created_at : lead.updated_at}
                      >
                        {date(arrivals ? lead.created_at : lead.updated_at)}
                      </time>
                    </td>
                    <td className="p-4">
                      <div className="flex flex-col items-start gap-2">
                        {arrivals && representative && (
                          <Button
                            disabled={!connected || !!taking}
                            onClick={() => void onTake(lead)}
                          >
                            {taking === lead.id ? "Taking…" : "Take & open"}
                            <ArrowRight className="size-3.5" />
                          </Button>
                        )}
                        <Button variant="ghost" onClick={() => onOpen(lead.id)}>
                          Open conversation <ArrowRight className="size-3.5" />
                        </Button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {arrivals && representative && (
        <p className="text-xs text-muted-foreground">
          Taking a lead sends an introduction in the existing thread and puts
          you on CC. Opening it sends nothing.
        </p>
      )}
    </>
  );
}
