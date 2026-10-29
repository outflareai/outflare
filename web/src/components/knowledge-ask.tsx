import { useState, type FormEvent } from "react";
import type { KnowledgeAnswer, KnowledgePassage } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, ErrorText, field } from "../ui";
import { Icon } from "../icons";
import { cn } from "@/lib/utils";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";

// Ask your knowledge: a question asked the way an agent or support reply
// would, for the team or for a customer, with the answer, the articles it
// used (each one click from its editor) and, when nothing covers it, a
// way to write the answer there and then.

type Audience = "team" | "customers";

const visibilityLabel: Record<KnowledgePassage["visibility"], { label: string; tone: "green" | "blue" | "grey" }> = {
  public: { label: "Help centre", tone: "green" },
  customers_ai: { label: "Customers", tone: "blue" },
  team: { label: "Team only", tone: "grey" },
};

export function KnowledgeAsk({
  ws,
  canManage,
  onOpen,
  onWrite,
  onConnectAI,
}: {
  ws: string;
  canManage: boolean;
  onOpen: (itemId: string) => void;
  onWrite: (question: string) => void;
  onConnectAI: () => void;
}) {
  const { brand } = useBrand(ws);
  const [question, setQuestion] = useState("");
  const [audience, setAudience] = useState<Audience>("team");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<KnowledgeAnswer | null>(null);
  const [showPassages, setShowPassages] = useState(false);

  async function submit(e?: FormEvent) {
    e?.preventDefault();
    const q = question.trim();
    if (!q || busy) return;
    setBusy(true);
    setError("");
    setShowPassages(false);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/knowledge/ask", {
      params: { path: { ws } },
      body: { question: q, audience, ...(brand ? { brand: brand.handle } : {}) },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setResult(data);
  }

  function pick(a: Audience) {
    setAudience(a);
    track("ui.knowledge_audience_picked", { audience: a });
  }

  const noAI = result?.ai.problem;
  const needsModel = noAI?.code === "connector_needed" || noAI?.code === "ai_model_needed";
  const cited = new Set(result?.citations.map((c) => c.chunk_id));
  return (
    <section aria-label="Ask your knowledge" className="rounded-lg border bg-card">
      <form onSubmit={submit} className="space-y-2.5 p-3.5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="flex items-center gap-2 text-sm font-semibold">
            <Icon name="ai" className="size-4 text-muted-foreground" /> Ask your knowledge
          </h2>
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <span>Answer as if for</span>
            <div className="flex rounded-md border p-0.5" role="group" aria-label="Who the answer is for">
              {(["team", "customers"] as const).map((a) => (
                <button
                  key={a}
                  type="button"
                  aria-pressed={audience === a}
                  onClick={() => pick(a)}
                  className={cn("rounded px-2 py-0.5 text-[12px]", audience === a ? "bg-accent font-medium text-foreground" : "text-muted-foreground")}
                >
                  {a === "team" ? "My team" : "A customer"}
                </button>
              ))}
            </div>
          </div>
        </div>
        <div className="flex gap-2">
          <input
            aria-label="Question"
            className={cn(field, "h-9 flex-1 text-[14px]")}
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
            maxLength={1000}
            placeholder={audience === "team" ? "What discount can I offer on the Pro plan?" : "Can I get a refund on an annual plan?"}
          />
          <Button type="submit" disabled={busy || !question.trim()}>{busy ? "Asking…" : "Ask"}</Button>
        </div>
        <p className="text-xs text-muted-foreground">
          {audience === "team"
            ? "Answers the way agents and missions working for your team would, from every published item."
            : "Answers the way a customer would hear it: only from items turned on for customers."}
        </p>
        <ErrorText>{error}</ErrorText>
      </form>

      {result && (
        <div className="space-y-3 border-t p-3.5" aria-live="polite">
          {noAI ? (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-dashed px-3 py-2 text-[13px]">
              <span className="text-muted-foreground">
                {needsModel
                  ? "Written answers need an AI model on your own key (Claude, OpenAI, Gemini and others)."
                  : noAI.message}{" "}
                {result.passages.length > 0 ? "Here is what your knowledge says." : "Nothing written matches this yet either."}
              </span>
              {needsModel && <Button variant="ghost" onClick={onConnectAI}>Connect an AI model</Button>}
            </div>
          ) : (
            <div className="space-y-2">
              <div className="flex items-center gap-2">
                {result.answered ? <Badge tone="green">Answered</Badge> : <Badge tone="amber">Not covered yet</Badge>}
                <span className="text-xs text-muted-foreground">
                  {result.audience === "customers" ? "As a customer would hear it" : "For your team"}
                  {result.ai.model ? ` · ${result.ai.model}` : ""}
                </span>
              </div>
              {result.answer && <p className="text-[14px] leading-relaxed whitespace-pre-line">{result.answer}</p>}
              {!result.answered && canManage && (
                <div className="flex flex-wrap items-center gap-2 rounded-md bg-amber-500/5 px-3 py-2 text-[13px]">
                  <span className="text-muted-foreground">Nothing written answers this. Write it once, and every AI answers it from now on.</span>
                  <Button variant="ghost" onClick={() => onWrite(result.question)}>Write the answer</Button>
                </div>
              )}
            </div>
          )}

          {result.citations.length > 0 && (
            <div>
              <h3 className="mb-1.5 text-xs font-medium text-muted-foreground">Sources</h3>
              <ul className="space-y-1.5">
                {result.citations.map((c, i) => (
                  <PassageRow key={c.chunk_id} n={i + 1} p={c} onOpen={onOpen} />
                ))}
              </ul>
            </div>
          )}

          {result.passages.length > 0 && (noAI || result.passages.some((p) => !cited.has(p.chunk_id))) && (
            <div>
              <button type="button" className="text-xs text-muted-foreground hover:text-foreground" onClick={() => setShowPassages((s) => !s)} aria-expanded={showPassages || !!noAI}>
                {showPassages || noAI ? "▾" : "▸"} What it found ({result.passages.length})
              </button>
              {(showPassages || noAI) && (
                <ul className="mt-1.5 space-y-1.5">
                  {result.passages.map((p) => (
                    <PassageRow key={p.chunk_id} p={p} onOpen={onOpen} />
                  ))}
                </ul>
              )}
            </div>
          )}
          {!result.meaning && !noAI && result.passages.length > 0 && (
            <p className="text-[11px] text-muted-foreground">
              Found by words only. With meaning-based search on (an AI connection that makes embeddings, such as OpenAI or Gemini), questions worded differently find the right article too.
            </p>
          )}
        </div>
      )}
    </section>
  );
}

function PassageRow({ p, n, onOpen }: { p: KnowledgePassage; n?: number; onOpen: (itemId: string) => void }) {
  const v = visibilityLabel[p.visibility];
  return (
    <li>
      <button type="button" onClick={() => onOpen(p.item_id)} className="w-full rounded-md border px-3 py-2 text-left hover:border-ring/60 hover:bg-accent/40">
        <span className="flex flex-wrap items-center gap-2 text-[13px] font-medium">
          {n && <span className="inline-flex size-5 items-center justify-center rounded-full bg-muted text-[11px]">{n}</span>}
          <span className="truncate">{p.title}</span>
          {p.heading && <span className="font-normal text-muted-foreground">› {p.heading}</span>}
          <Badge tone={v.tone}>{v.label}</Badge>
          <span className="ml-auto text-[11px] font-normal text-muted-foreground">Edit</span>
        </span>
        <span className="mt-0.5 line-clamp-2 block text-xs text-muted-foreground">{plain(p.text)}</span>
      </button>
    </li>
  );
}

// plain is a passage as one line of text: no Markdown marks, list markers
// or table rules.
function plain(text: string) {
  return text
    .split("\n")
    .map((l) => l.trim().replace(/^([-*+]|\d+[.)])\s+(\[[ xX]\]\s+)?/, ""))
    .filter((l) => l.replace(/[|\-:\s]/g, "") !== "")
    .join(" ")
    .replace(/[#*_`>]/g, "")
    .replace(/\|/g, " · ")
    .replace(/\s+/g, " ");
}
