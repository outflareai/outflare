import { useEffect, useState } from "react";
import type { KnowledgeSuggestion } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Button, ErrorText, field, Loading, toast } from "../ui";
import { Icon } from "../icons";
import { ago } from "../format";
import { cn } from "@/lib/utils";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { track } from "@/lib/analytics";

// The review queue: answers Outflare found for questions the knowledge
// couldn't answer, and items that disagree. Nothing here is used by any AI
// or shown to customers until a person approves it.

const sourceLabel: Record<KnowledgeSuggestion["source"], string> = {
  support_reply: "From a teammate's reply in Support",
  website: "From your website",
  slack: "From Slack",
  agent: "Found by an AI agent",
  person: "Suggested by a teammate",
  answer: "Noticed while answering",
};

const useLabel = {
  customers_ai: "AI may tell customers",
  public: "On the help centre too",
  team: "Team only",
} as const;

export function KnowledgeReview({
  ws,
  canManage,
  tick,
  onChanged,
}: {
  ws: string;
  canManage: boolean;
  tick: number;
  onChanged: () => void;
}) {
  const { brand } = useBrand(ws);
  const [list, setList] = useState<KnowledgeSuggestion[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    void api
      .GET("/v1/brands/{brand}/knowledge/suggestions", {
        params: { path: { brand: pickedBrandId() } },
      })
      .then(({ data, error }) => {
        if (error) return setError(errorMessage(error));
        setList(data.data);
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand?.id, tick]);

  function done(s: KnowledgeSuggestion) {
    setList((l) => l?.filter((x) => x.id !== s.id) ?? null);
    onChanged();
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (list === null) return <Loading />;
  if (list.length === 0) return null;
  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-[15px] font-semibold">To review</h2>
        <p className="text-xs text-muted-foreground">
          Answers found for questions your knowledge couldn't answer, and
          articles that disagree. Nothing here is used until you approve it.
        </p>
      </div>
      {list.map((s) =>
        s.kind === "conflict" ? (
          <ConflictCard
            key={s.id}
            ws={ws}
            s={s}
            canManage={canManage}
            onDone={() => done(s)}
          />
        ) : (
          <AnswerCard
            key={s.id}
            ws={ws}
            s={s}
            canManage={canManage}
            onDone={() => done(s)}
          />
        ),
      )}
    </section>
  );
}

function Header({ s }: { s: KnowledgeSuggestion }) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-[11px]">
      <span className="inline-flex items-center gap-1 text-muted-foreground">
        <Icon name="sparkles" className="size-3" />
        {sourceLabel[s.source]}
      </span>
      <span className="text-muted-foreground">· {ago(s.created_at)}</span>
      {s.sensitive && (
        <span
          className="rounded bg-amber-500/15 px-1.5 py-0.5 font-medium text-amber-800 dark:text-amber-300"
          title="About prices, refunds, contracts, legal terms, security or personal data"
        >
          Check carefully
        </span>
      )}
    </div>
  );
}

async function reject(_ws: string, s: KnowledgeSuggestion, onDone: () => void) {
  const r = await ask({
    title:
      s.kind === "conflict" ? "Dismiss this conflict?" : "Reject this answer?",
    body: "It won't be suggested again.",
    input: {
      label: "Why (optional)",
      placeholder: "Out of date, wrong, only for one customer…",
      maxLength: 1000,
      optional: true,
    },
    choices: [
      { label: "Cancel", value: "cancel", variant: "ghost" },
      {
        label: s.kind === "conflict" ? "Dismiss" : "Reject",
        value: "ok",
        variant: "primary",
      },
    ],
  });
  if (!r || r.choice !== "ok") return;
  const { error } = await api.POST(
    "/v1/brands/{brand}/knowledge/suggestions/{id}/reject",
    { params: { path: { brand: pickedBrandId(), id: s.id } }, body: { reason: r.value } },
  );
  if (error) return toast(errorMessage(error), "error");
  toast(
    s.kind === "conflict"
      ? "Dismissed"
      : "Rejected: it won't be suggested again",
  );
  onDone();
}

function AnswerCard({
  ws,
  s,
  canManage,
  onDone,
}: {
  ws: string;
  s: KnowledgeSuggestion;
  canManage: boolean;
  onDone: () => void;
}) {
  const [title, setTitle] = useState(s.title);
  const [body, setBody] = useState(s.body);
  const [use, setUse] = useState<KnowledgeSuggestion["visibility"]>(
    s.visibility,
  );
  const [busy, setBusy] = useState(false);
  const [showQuote, setShowQuote] = useState(false);

  async function approve() {
    setBusy(true);
    const { error } = await api.POST(
      "/v1/brands/{brand}/knowledge/suggestions/{id}/approve",
      {
        params: { path: { brand: pickedBrandId(), id: s.id } },
        body: { title, body, visibility: use },
      },
    );
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    track("ui.knowledge_suggestion_approved", {
      source: s.source,
      edited: title !== s.title || body !== s.body,
    });
    toast("Added to your knowledge: every AI answers it from now on");
    onDone();
  }

  return (
    <div
      className={cn(
        "space-y-3 rounded-lg border p-4",
        s.sensitive && "border-amber-500/40",
      )}
    >
      <Header s={s} />
      <div>
        <p className="text-xs text-muted-foreground">Asked</p>
        <p className="text-[13px] font-medium">{s.question}</p>
      </div>
      <div className="space-y-2">
        <p className="text-[13px] font-medium">
          Is this correct, and can it be used for support from now on?
        </p>
        <input
          aria-label="Title"
          className={cn(field, "w-full")}
          value={title}
          disabled={!canManage}
          onChange={(e) => setTitle(e.target.value)}
          maxLength={300}
        />
        <textarea
          aria-label="Answer"
          className={cn(field, "h-auto w-full py-1.5")}
          rows={3}
          value={body}
          disabled={!canManage}
          onChange={(e) => setBody(e.target.value)}
          maxLength={20000}
        />
      </div>
      {s.source_quote && (
        <div className="text-xs">
          <button
            type="button"
            className="text-muted-foreground underline underline-offset-2 hover:text-foreground"
            onClick={() => setShowQuote((v) => !v)}
          >
            {showQuote ? "Hide where it came from" : "Show where it came from"}
          </button>
          {showQuote && (
            <blockquote className="mt-1.5 border-l-2 pl-3 whitespace-pre-wrap text-muted-foreground">
              {s.source_quote}
              {s.source_ref && /^https?:\/\//.test(s.source_ref) && (
                <a
                  className="mt-1 block underline"
                  href={s.source_ref}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  {s.source_ref}
                </a>
              )}
              {s.source_ref &&
                !/^https?:\/\//.test(s.source_ref) &&
                s.source !== "support_reply" && (
                  <span className="mt-1 block">{s.source_ref}</span>
                )}
            </blockquote>
          )}
        </div>
      )}
      {canManage && (
        <div className="flex flex-wrap items-center gap-2">
          <select
            aria-label="Who may use it"
            className={cn(field, "w-auto")}
            value={use}
            onChange={(e) => setUse(e.target.value as typeof use)}
          >
            {(Object.keys(useLabel) as (keyof typeof useLabel)[]).map((k) => (
              <option key={k} value={k}>
                {useLabel[k]}
              </option>
            ))}
          </select>
          <span className="flex-1" />
          <Button
            variant="ghost"
            disabled={busy}
            onClick={() => reject(ws, s, onDone)}
          >
            Reject
          </Button>
          <Button
            disabled={busy || !title.trim() || !body.trim()}
            onClick={approve}
          >
            {busy ? "Adding…" : "Approve"}
          </Button>
        </div>
      )}
    </div>
  );
}

function ConflictCard({
  ws,
  s,
  canManage,
  onDone,
}: {
  ws: string;
  s: KnowledgeSuggestion;
  canManage: boolean;
  onDone: () => void;
}) {
  const [keep, setKeep] = useState<string>("");
  const [busy, setBusy] = useState(false);

  async function settle() {
    setBusy(true);
    const { error } = await api.POST(
      "/v1/brands/{brand}/knowledge/suggestions/{id}/approve",
      { params: { path: { brand: pickedBrandId(), id: s.id } }, body: { keep_item_id: keep } },
    );
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    track("ui.knowledge_conflict_settled", { items: s.items.length });
    toast("Kept the right one; the others are back to drafts to fix or delete");
    onDone();
  }

  return (
    <div className={cn("space-y-3 rounded-lg border border-red-500/30 p-4")}>
      <Header s={s} />
      <div>
        <p className="text-[13px] font-medium">These disagree: {s.question}</p>
        {s.source_quote && (
          <p className="text-xs text-muted-foreground">
            Found answering “{s.source_quote}”. Until you settle it, answers
            avoid the point they disagree on.
          </p>
        )}
      </div>
      <fieldset className="space-y-2" disabled={!canManage}>
        <legend className="sr-only">Which one is right?</legend>
        {s.items.map((it) => (
          <label
            key={it.id}
            className={cn(
              "flex cursor-pointer gap-3 rounded-md border p-3",
              keep === it.id && "border-primary bg-accent/40",
            )}
          >
            <input
              type="radio"
              name={"keep-" + s.id}
              className="mt-1"
              checked={keep === it.id}
              onChange={() => setKeep(it.id)}
            />
            <span className="min-w-0">
              <span className="block text-[13px] font-medium">
                {it.title}
                {it.status === "draft" && (
                  <span className="ml-1 text-xs text-muted-foreground">
                    (draft)
                  </span>
                )}
              </span>
              <span className="block text-xs text-muted-foreground">
                {it.excerpt}
              </span>
            </span>
          </label>
        ))}
      </fieldset>
      {canManage && (
        <div className="flex flex-wrap items-center gap-2">
          <span className="flex-1 text-xs text-muted-foreground">
            Pick the one that's right. The others go back to drafts, so nothing
            uses them.
          </span>
          <Button
            variant="ghost"
            disabled={busy}
            onClick={() => reject(ws, s, onDone)}
          >
            Not a conflict
          </Button>
          <Button disabled={busy || !keep} onClick={settle}>
            Keep this one
          </Button>
        </div>
      )}
    </div>
  );
}
