import { useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { Check, Eye, MessageSquare, ThumbsDown, ThumbsUp, Trash2 } from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, ErrorText, field, toast } from "@/ui";
import { track } from "@/lib/analytics";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type Lead = Schemas["SalesLead"];
type Member = Schemas["SalesMember"];
type Detail = Schemas["SalesLeadDetail"];
export function salesChecked<T>(r: { data?: unknown; error?: unknown }): T {
  if (r.error || !r.data) throw r.error ?? new Error("No answer received.");
  if (
    typeof r.data === "object" &&
    ("action" in r.data || "mission_action" in r.data)
  )
    throw new Error("This action is waiting for approval.");
  return r.data as T;
}
export function SalesViewed({ lead, team }: { lead: Lead; team: Member[] }) {
  return (
    <p
      className="mt-2 text-xs text-muted-foreground"
      title="Opening a conversation or marking it seen records a view. It does not confirm that every message was read."
    >
      {lead.viewed_by?.length ? (
        <>
          Seen by{" "}
          {lead.viewed_by.map((view, i) => (
            <span key={view.user_id}>
              {i > 0 && ", "}
              <span title={new Date(view.seen_at).toLocaleString()}>
                {team.find((m) => m.user_id === view.user_id)?.name ??
                  "Teammate"}
              </span>
            </span>
          ))}
        </>
      ) : (
        "Not seen yet"
      )}
    </p>
  );
}
// salesMarkSeen records that the signed-in person has seen a conversation,
// as the Seen button does.
export async function salesMarkSeen(brand: string, lead: Lead) {
  salesChecked(
    await api.POST("/v1/brands/{brand}/sales/leads/{id}/actions", {
      params: { path: { brand, id: lead.id } },
      body: { action: "read", version: lead.version },
    }),
  );
}

// compact (the queue's row) keeps the words for wide screens only; the
// button's name stays for screen readers and as its tooltip.
export function SalesSeen({
  brand,
  lead,
  onSaved,
  compact,
}: {
  brand: string;
  lead: Lead;
  onSaved: () => unknown;
  compact?: boolean;
}) {
  const [busy, setBusy] = useState(false);
  return (
    <Button
      variant="ghost"
      className="h-8 px-2 text-xs"
      title={lead.unread ? "Mark this conversation seen" : "Seen by you"}
      aria-label={lead.unread ? "Mark seen" : "Seen by you"}
      disabled={busy || !lead.unread}
      onClick={async () => {
        setBusy(true);
        try {
          await salesMarkSeen(brand, lead);
          onSaved();
        } catch (e) {
          toast(errorMessage(e), "error");
        } finally {
          setBusy(false);
        }
      }}
    >
      {lead.unread ? (
        <Eye className="size-3.5" />
      ) : (
        <Check className="size-3.5" />
      )}
      <span className={compact ? "hidden 2xl:inline" : ""}>
        {busy ? "Saving…" : "Seen"}
      </span>
    </Button>
  );
}

// How good a conversation was, as a rep marks it. Counted per source on the
// Sales channels page, so the team sees which source sends what.
export const salesQualityNames = {
  good: "Good",
  poor_fit: "Poor fit",
  junk: "Junk",
} as const;
export type SalesQualityMark = keyof typeof salesQualityNames;
const qualityIcons = { good: ThumbsUp, poor_fit: ThumbsDown, junk: Trash2 };
const qualityOn: Record<SalesQualityMark, string> = {
  good: "border-emerald-500/50 bg-emerald-500/15 text-emerald-600 dark:text-emerald-300",
  poor_fit: "border-amber-500/50 bg-amber-500/15 text-amber-600 dark:text-amber-300",
  junk: "border-red-500/50 bg-red-500/15 text-red-600 dark:text-red-300",
};

export function SalesQuality({
  brand,
  lead,
  onSaved,
}: {
  brand: string;
  lead: Lead;
  onSaved: () => unknown;
}) {
  const [busy, setBusy] = useState(false);
  // Shown at once; the list's own reload then confirms it.
  const [picked, setPicked] = useState<SalesQualityMark | null>();
  const current = picked === undefined ? (lead.quality ?? null) : picked;
  async function mark(next: SalesQualityMark) {
    const quality = current === next ? "" : next;
    setBusy(true);
    setPicked(quality || null);
    try {
      salesChecked(
        await api.POST("/v1/brands/{brand}/sales/leads/{id}/actions", {
          params: { path: { brand, id: lead.id } },
          body: { action: "rate", version: lead.version, quality },
        }),
      );
      track("ui.sales_quality_marked", { quality: quality || "cleared" });
      onSaved();
    } catch (e) {
      setPicked(undefined);
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  }
  return (
    <div
      className="flex items-center gap-1"
      role="group"
      aria-label="How good was this conversation?"
    >
      {(Object.keys(salesQualityNames) as SalesQualityMark[]).map((q) => {
        const Icon = qualityIcons[q];
        const on = current === q;
        return (
          <button
            key={q}
            type="button"
            disabled={busy}
            aria-pressed={on}
            aria-label={salesQualityNames[q]}
            title={on ? `${salesQualityNames[q]}. Click to clear.` : `Mark as ${salesQualityNames[q].toLowerCase()}`}
            className={`inline-flex h-7 items-center gap-1 rounded-md border px-1.5 text-[11px] ${on ? qualityOn[q] : "border-transparent text-muted-foreground hover:bg-muted/50 hover:text-foreground"}`}
            onClick={() => void mark(q)}
          >
            <Icon className="size-3.5" />
            {on && salesQualityNames[q]}
          </button>
        );
      })}
    </div>
  );
}

const categories = {
  wrong_contact: "Wrong contact",
  poor_fit: "Poor fit",
  reply_classification: "Reply classified incorrectly",
  missing_information: "Missing information",
  question: "Question",
  other: "Other",
} as const;
export function SalesFeedback({
  brand,
  lead,
  team,
  onSaved,
  compact,
}: {
  brand: string;
  lead: Lead;
  team: Member[];
  onSaved: () => unknown;
  compact?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [detail, setDetail] = useState<Detail>();
  const [category, setCategory] = useState<keyof typeof categories>("question");
  const [body, setBody] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const path = { brand, id: lead.id };
  async function load() {
    const d = salesChecked<Detail>(
      await api.GET("/v1/brands/{brand}/sales/leads/{id}", {
        params: { path },
      }),
    );
    setDetail(d);
    return d;
  }
  async function submit() {
    setBusy(true);
    setError("");
    try {
      const current = await load();
      salesChecked(
        await api.POST("/v1/brands/{brand}/sales/leads/{id}/actions", {
          params: { path },
          body: {
            action: "feedback",
            version: current.lead.version,
            feedback_category: category,
            body,
          },
        }),
      );
      setBody("");
      await load();
      onSaved();
      toast("Feedback saved for the team.");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <Button
        title="Feedback for the team"
        aria-label="Feedback"
        variant="ghost"
        className="h-8 px-2 text-xs"
        onClick={() => {
          setOpen(true);
          setError("");
          track("ui.sales_dialog_opened", { kind: "feedback" });
          void load().catch((e) => setError(errorMessage(e)));
        }}
      >
        <MessageSquare className="size-3.5" />
        <span className={compact ? "hidden 2xl:inline" : ""}>Feedback</span>
        {lead.feedback_count ? ` (${lead.feedback_count})` : ""}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Lead feedback</DialogTitle>
            <DialogDescription>
              {lead.company || lead.name} · Shared with teammates who can access
              this lead.
            </DialogDescription>
          </DialogHeader>
          <ErrorText>{error}</ErrorText>
          <label className="space-y-2 text-sm">
            <span>What’s wrong with this lead, or what needs checking?</span>
            <select
              className={`${field} w-full`}
              value={category}
              onChange={(e) =>
                setCategory(e.target.value as keyof typeof categories)
              }
            >
              {Object.entries(categories).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </select>
          </label>
          <textarea
            aria-label="Lead feedback"
            className={`${field} min-h-28 w-full`}
            value={body}
            maxLength={4000}
            onChange={(e) => setBody(e.target.value)}
            placeholder="Describe the issue or question so the team can follow up…"
          />
          <DialogFooter>
            <Button
              disabled={busy || !body.trim()}
              onClick={() => void submit()}
            >
              {busy ? "Saving…" : "Save feedback"}
            </Button>
          </DialogFooter>
          {!!detail?.feedback?.length && (
            <div
              className="max-h-64 space-y-4 overflow-y-auto border-t pt-4"
              aria-label="Previous feedback"
            >
              {detail.feedback.map((item) => (
                <div key={item.id} className="text-sm">
                  <p className="text-xs text-muted-foreground">
                    {team.find((m) => m.user_id === item.author_id)?.name ??
                      "Former teammate"}{" "}
                    · {new Date(item.created_at).toLocaleString()} ·{" "}
                    {categories[item.category]}
                  </p>
                  <p className="mt-1 whitespace-pre-wrap break-words">
                    {item.body}
                  </p>
                </div>
              ))}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
export function SalesAssignment({
  brand,
  lead,
  team,
  userID,
  manager = false,
  onSaved,
}: {
  brand: string;
  lead: Lead;
  team: Member[];
  userID?: string;
  manager?: boolean;
  onSaved: () => unknown;
}) {
  const [busy, setBusy] = useState(false);
  // A new conversation can be assigned by any Sales teammate. A taken one can
  // be passed on by its owner or a manager, and cannot be left unassigned.
  const active = lead.state === "active";
  if (
    lead.state !== "new" &&
    !(active && (manager || (!!userID && lead.owner_id === userID)))
  )
    return null;
  const eligible = team
    .filter((m) => ["sdr", "ae"].includes(m.role))
    .sort((a, b) => Number(b.role === "sdr") - Number(a.role === "sdr"));
  async function assign(target: string) {
    if (busy || (!target && active) || target === (lead.owner_id ?? "")) return;
    setBusy(true);
    try {
      salesChecked(
        await api.POST("/v1/brands/{brand}/sales/leads/{id}/actions", {
          params: { path: { brand, id: lead.id } },
          body: {
            action: target ? "assign" : "release",
            version: lead.version,
            ...(target ? { assignee_id: target } : {}),
          },
        }),
      );
      onSaved();
      toast(target ? "Assignment saved." : "Conversation unassigned.");
    } catch (e) {
      toast(errorMessage(e), "error");
      onSaved();
    } finally {
      setBusy(false);
    }
  }
  return (
    <select
      aria-label={`Assigned to for ${lead.name}`}
      title="Assigned to"
      className={`${field} h-8 w-40 max-w-full py-1 text-xs`}
      disabled={busy}
      value={lead.owner_id ?? ""}
      onChange={(e) => void assign(e.target.value)}
    >
      {!active && <option value="">Unassigned</option>}
      {lead.owner_id && !eligible.some((m) => m.user_id === lead.owner_id) && (
        <option value={lead.owner_id} disabled>
          {team.find((m) => m.user_id === lead.owner_id)?.name ??
            "Assigned teammate"}
        </option>
      )}
      {eligible.map((m) => (
        <option key={m.user_id} value={m.user_id}>
          {m.name}
          {m.user_id === userID ? " (you)" : ""} ·{" "}
          {m.role === "sdr" ? "SDR" : "AE"}
        </option>
      ))}
    </select>
  );
}

export function SalesLocation({
  lead,
  compact = false,
}: {
  lead: Lead;
  compact?: boolean;
}) {
  const country = (value?: string) => {
    if (!value) return "";
    try {
      return (
        new Intl.DisplayNames(["en"], { type: "region" }).of(value) ?? value
      );
    } catch {
      return value;
    }
  };
  const personal = [lead.city, country(lead.country)]
    .filter(Boolean)
    .join(", ");
  if (compact)
    return (
      <p
        className="mt-1 text-xs text-muted-foreground"
        title="Contact location"
      >
        {personal || "Location unknown"}
      </p>
    );
  return (
    <div className="mt-2 space-y-1 text-xs text-muted-foreground">
      <p>Contact location: {personal || "Unknown"}</p>
      {lead.company_country && (
        <p>Company HQ: {country(lead.company_country)}</p>
      )}
    </div>
  );
}
