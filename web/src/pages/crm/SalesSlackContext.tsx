import { useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { Check, Copy, ExternalLink } from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, ErrorText } from "@/ui";
import { track } from "@/lib/analytics";
import { salesChecked, salesExternalURL } from "@/lib/sales";

type Lead = Schemas["SalesLead"];
type Search = Schemas["SalesSlackSearch"];

// What the workspace's Slack says about this person or company, searched
// once when the conversation opens. Hits come from Slack and are shown as
// text. A failure stays inside this panel.
export function SalesSlackContext({
  brand,
  lead,
}: {
  brand: string;
  lead: Lead;
}) {
  const id = lead.id;
  const [result, setResult] = useState<Search>();
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  const copiedTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(copiedTimer.current), []);
  useEffect(() => {
    let cancelled = false;
    setResult(undefined);
    setError("");
    api
      .GET("/v1/brands/{brand}/sales/leads/{id}/slack-search", {
        params: { path: { brand, id } },
      })
      .then((r) => salesChecked<Search>(r))
      .then((data) => {
        if (!cancelled) setResult(data);
      })
      .catch((e) => {
        if (!cancelled) setError(errorMessage(e));
      });
    return () => {
      cancelled = true;
    };
  }, [brand, id]);

  const hits = result?.data ?? [];
  const query = result?.query ?? "";
  return (
    <section
      aria-label="In Slack"
      className="space-y-3 rounded-xl border bg-card p-4 text-sm"
    >
      <h3 className="font-medium">In Slack</h3>
      {error ? (
        <ErrorText>{error}</ErrorText>
      ) : !result ? (
        <p className="text-xs text-muted-foreground">Searching Slack…</p>
      ) : !result.available ? (
        query ? (
          <div className="space-y-2">
            <p className="text-xs text-muted-foreground">
              Outflare can't search your Slack from here. Paste this into
              Slack's search:
            </p>
            <p className="break-words rounded border bg-muted/20 px-2 py-1 font-mono text-xs">
              {query}
            </p>
            <Button
              variant="ghost"
              className="-ml-2"
              onClick={() => {
                // Like CopyButton: no message when the browser refuses.
                navigator.clipboard
                  ?.writeText(query)
                  .then(() => {
                    track("ui.copied");
                    setCopied(true);
                    clearTimeout(copiedTimer.current);
                    copiedTimer.current = setTimeout(
                      () => setCopied(false),
                      1500,
                    );
                  })
                  .catch(() => {});
              }}
            >
              {copied ? (
                <Check className="size-3.5" />
              ) : (
                <Copy className="size-3.5" />
              )}
              {copied ? "Copied" : "Copy search for Slack"}
            </Button>
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">
            No email, website or company name to search Slack for.
          </p>
        )
      ) : hits.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          Nothing in Slack about this person or company
        </p>
      ) : (
        <ol className="space-y-3">
          {hits.map((h, i) => {
            const link = salesExternalURL(h.link);
            return (
              <li key={i} className="border-b pb-3 last:border-0 last:pb-0">
                <p className="flex flex-wrap justify-between gap-x-2 text-xs text-muted-foreground">
                  <span>{h.channel_name ? `#${h.channel_name}` : "Slack"}</span>
                  <time dateTime={h.at}>{new Date(h.at).toLocaleString()}</time>
                </p>
                <p className="mt-1 whitespace-pre-wrap break-words">{h.text}</p>
                {link && (
                  <a
                    href={link}
                    target="_blank"
                    rel="noreferrer noopener"
                    onClick={() =>
                      track("ui.sales_slack_hit_opened", {
                        position: i + 1,
                        hits: hits.length,
                      })
                    }
                    className="mt-1 inline-flex items-center gap-1 text-xs underline underline-offset-2"
                  >
                    Open in Slack <ExternalLink className="size-3" />
                  </a>
                )}
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}
