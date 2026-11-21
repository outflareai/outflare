import { useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { Sparkles } from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, ErrorText, field } from "@/ui";
import { track } from "@/lib/analytics";
import { salesChecked } from "./SalesCollaboration";

type Preview = Schemas["SalesDraftAssist"];
export function SalesDraftAssistant({
  brand,
  leadID,
  disabled,
  save,
  apply,
  onSettings,
}: {
  brand: string;
  leadID: string;
  disabled: boolean;
  save: () => Promise<Schemas["SalesDraft"] | null>;
  apply: (preview: Preview) => Promise<void>;
  onSettings: () => void;
}) {
  const [instructions, setInstructions] = useState("");
  const [preview, setPreview] = useState<Preview>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [setup, setSetup] = useState(false);
  const guard = useRef(false);
  const waiting = preview?.status === "queued" || preview?.status === "running";
  useEffect(() => {
    if (!waiting || !preview) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const started = Date.now();
    const poll = async () => {
      try {
        const result = salesChecked<Preview>(
          await api.GET(
            "/v1/brands/{brand}/sales/leads/{id}/draft-assist/{request}",
            { params: { path: { brand, id: leadID, request: preview.id } } },
          ),
        );
        if (cancelled) return;
        setPreview(result);
        if (["queued", "running"].includes(result.status)) {
          if (Date.now() - started > 120000) {
            setPreview(undefined);
            setError(
              "The preview took too long. Try again; your saved reply is unchanged.",
            );
          } else timer = setTimeout(poll, 1500);
        } else if (result.status === "failed") {
          setError(
            result.error_code === "record_changed"
              ? "The draft or assignment changed. Generate a new preview from your latest draft."
              : "AI could not finish this preview. Check the workspace AI connection and budget, then try again.",
          );
          setSetup(result.error_code !== "record_changed");
        }
      } catch (e) {
        if (!cancelled) {
          setError(errorMessage(e));
          setPreview(undefined);
        }
      }
    };
    timer = setTimeout(poll, 1000);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [brand, leadID, preview?.id, waiting]);
  async function generate() {
    if (guard.current) return;
    guard.current = true;
    setBusy(true);
    setError("");
    setSetup(false);
    try {
      const draft = await save();
      const result = salesChecked<Preview>(
        await api.POST("/v1/brands/{brand}/sales/leads/{id}/draft-assist", {
          params: { path: { brand, id: leadID } },
          body: {
            request_id: crypto.randomUUID(),
            revision: draft?.revision ?? 0,
            instructions,
          },
        }),
      );
      setPreview(result);
    } catch (e) {
      const message = errorMessage(e);
      const code = (e as { error?: { code?: string } })?.error?.code ?? "";
      setError(
        ["ai_model_needed", "connector_needed"].includes(code)
          ? "Connect your workspace’s AI provider in AI settings to generate a draft."
          : message,
      );
      setSetup(
        [
          "ai_model_needed",
          "connector_needed",
          "ai_budget_exceeded",
          "ai_budget_reached",
          "budget_exceeded",
        ].includes(code),
      );
    } finally {
      guard.current = false;
      setBusy(false);
    }
  }
  return (
    <div className="space-y-3 border-t pt-3">
      <div className="flex flex-wrap items-center gap-3">
        <Button
          variant="ghost"
          disabled={disabled || busy || waiting}
          onClick={() => void generate()}
        >
          <Sparkles className="size-4" />
          {busy || waiting ? "Personalizing…" : "Personalize with AI"}
        </Button>
        <span className="text-xs text-muted-foreground">
          Preview first · You choose what to send
        </span>
      </div>
      <details
        onToggle={(e) => {
          if (e.currentTarget.open)
            track("ui.sales_dialog_opened", { kind: "ai_instructions" });
        }}
      >
        <summary className="cursor-pointer text-xs text-muted-foreground">
          Add instructions for AI
        </summary>
        <input
          aria-label="Instructions for AI draft"
          className={`${field} mt-2 w-full`}
          value={instructions}
          maxLength={4000}
          disabled={busy || waiting}
          onChange={(e) => setInstructions(e.target.value)}
          placeholder="Optional: ask about their use case, keep it brief…"
        />
      </details>
      <p className="text-xs text-muted-foreground">
        AI uses your draft, the conversation and your Sales role with your
        workspace’s AI connection.
      </p>
      <ErrorText>{error}</ErrorText>
      {setup && (
        <Button variant="ghost" onClick={onSettings}>
          Open AI settings
        </Button>
      )}
      {preview?.status === "ready" && (
        <div className="space-y-3 border-t pt-3">
          <p className="text-xs font-medium">
            AI preview · Check facts and wording before sending
          </p>
          <p className="whitespace-pre-wrap break-words text-sm leading-relaxed">
            {preview.body}
          </p>
          <div className="flex gap-2">
            <Button
              disabled={disabled || busy}
              onClick={async () => {
                setBusy(true);
                setError("");
                try {
                  await apply(preview);
                  setPreview(undefined);
                } catch (e) {
                  setError(errorMessage(e));
                } finally {
                  setBusy(false);
                }
              }}
            >
              Use this draft
            </Button>
            <Button
              variant="ghost"
              disabled={busy}
              onClick={() => {
                setPreview(undefined);
                track("ui.sales_ai_preview_discarded", {});
              }}
            >
              Discard preview
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
