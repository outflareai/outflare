import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, ErrorText, Status, toast } from "../ui";
import { appPath } from "@/lib/paths";

type State = components["schemas"]["SemanticSearch"];

export const semanticSearchDocs = "https://outflare.ai/docs/guides/query-and-search/#meaning-based-search";

// Meaning-based search: on unless an admin turns it off. It spends a little
// on the workspace's own AI provider, so the card says what it costs and why.
export function SemanticSearchCard({ ws, canManage }: { ws: string; canManage: boolean }) {
  const [s, setS] = useState<State | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const path = { params: { path: { ws } } };

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/semantic-search", path).then(({ data, error }) => (error ? setError(errorMessage(error)) : setS(data)));
  }, [ws]);

  async function toggle(enabled: boolean) {
    setBusy(true);
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/semantic-search", { ...path, body: { enabled } });
    setBusy(false);
    setError(error ? errorMessage(error) : "");
    if (data && "enabled" in data) {
      setS(data);
      toast(enabled ? "Meaning-based search is on." : "Meaning-based search is off, and its vectors are deleted.");
    }
  }

  return (
    <div id="meaning-search">
      <Card title="Meaning-based search" description="Search that finds what you mean, not only the words you type.">
        {!s ? (
          <ErrorText>{error}</ErrorText>
        ) : (
          <div className="space-y-3 text-[13px]">
            <div className="flex flex-wrap items-center gap-2">
              {!s.enabled ? (
                <Status tone="grey">Off</Status>
              ) : s.problem ? (
                <Status tone="amber">{s.problem.message}</Status>
              ) : (
                <Status tone="green">On{s.waiting > 0 ? `: ${s.waiting.toLocaleString()} items to go` : ""}</Status>
              )}
              {s.enabled && !s.problem && <span className="text-muted-foreground">{s.embedded.toLocaleString()} items ready, with {s.model}</span>}
            </div>
            <p className="text-muted-foreground">
              Searching "unhappy with price" also finds a reply saying "too expensive for us". To do that, Outflare turns each record, reply, meeting and deal into a
              vector (a list of numbers that captures its meaning) with your own AI key. Each item is sent once, and again only when it changes, so this
              usually costs cents a month. It shows as "Meaning-based search" in{" "}
              <a className="underline underline-offset-2" href={appPath(ws, "ai-usage")}>AI usage</a>, where you can also give it a limit.{" "}
              <a className="underline underline-offset-2" href={semanticSearchDocs} target="_blank" rel="noreferrer">Learn more</a>
            </p>
            <ErrorText>{error}</ErrorText>
            {canManage ? (
              <Button variant="ghost" disabled={busy} onClick={() => toggle(!s.enabled)}>
                {busy ? "Saving…" : s.enabled ? "Turn off" : "Turn on"}
              </Button>
            ) : (
              <p className="text-muted-foreground">Only owners and admins can change this.</p>
            )}
            {canManage && s.enabled && <p className="text-xs text-muted-foreground">Turning it off stops these AI calls and deletes the vectors. Word search keeps working.</p>}
          </div>
        )}
      </Card>
    </div>
  );
}
