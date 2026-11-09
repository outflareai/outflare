import { useEffect, useState } from "react";
import type { KnowledgeReplyImport } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, toast } from "../ui";
import { Icon } from "../icons";
import { ago } from "../format";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { track } from "@/lib/analytics";

// Learning from past replies, in Knowledge → Review: the answers your team
// already gave in support become suggestions to approve.
export function LearnFromReplies({ ws, canManage, onDone }: { ws: string; canManage: boolean; onDone: () => void }) {
  const { brand } = useBrand(ws);
  const [list, setList] = useState<KnowledgeReplyImport[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = () =>
    api.GET("/v1/brands/{brand}/knowledge/reply-imports", { params: { path: { brand: pickedBrandId() } } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      setList(data.data);
    });
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => void load(), [ws, brand?.id]);
  const last = list?.[0];
  const running = last && (last.status === "queued" || last.status === "running");
  useEffect(() => {
    if (!running) return;
    const t = setTimeout(() => void load(), 3000);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running, list]);
  useEffect(() => {
    if (last?.status === "done") onDone();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [last?.id, last?.status]);

  async function start() {
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/brands/{brand}/knowledge/reply-imports", {
      params: { path: { brand: pickedBrandId() } },
      body: { brand: brand?.handle ?? null, days: 90 },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    track("ui.knowledge_reply_learning_started", { days: 90 });
    setList((l) => [data as KnowledgeReplyImport, ...(l ?? [])]);
    toast("Reading your past replies. Answers appear here to review as they're found.");
  }

  if (!canManage && !last) return null;
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg border p-3">
      <Icon name="sparkles" className="size-4 shrink-0 text-violet-600 dark:text-violet-400" />
      <div className="min-w-0 flex-1">
        <p className="text-[13px] font-medium">Learn from past replies</p>
        <p className="text-xs text-muted-foreground">
          {running
            ? `Reading your support conversations… ${last.conversations_read} so far.`
            : last?.status === "done"
              ? `Last read ${ago(last.finished_at ?? last.created_at)}: ${last.conversations_read} conversations, ${last.suggestions_filed} new answers to review.`
              : last?.status === "failed"
                ? last.error || "It couldn't finish."
                : "Your AI reads the support conversations closed in the last 90 days and suggests the general answers your team gave, one per question. Nothing is used until you approve it."}
        </p>
        {error && <ErrorText>{error}</ErrorText>}
      </div>
      {canManage && <Button variant="ghost" disabled={busy || !!running} onClick={start}>{running ? "Reading…" : busy ? "Starting…" : last ? "Read again" : "Start"}</Button>}
    </div>
  );
}
