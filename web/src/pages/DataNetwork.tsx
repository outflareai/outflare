import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, ErrorText, Status, toast } from "../ui";
import { track } from "@/lib/analytics";

type State = components["schemas"]["DataNetwork"];
type Preview = components["schemas"]["DataNetworkPreview"];

// The data network: workspaces that join share whether the addresses they
// email deliver or bounce, and get unlimited lookups. Only the owner can
// join, and only after seeing exactly what would be shared. Owners and
// admins can leave.
export function DataNetworkCard({ ws, isOwner, canManage }: { ws: string; isOwner: boolean; canManage: boolean }) {
  const [s, setS] = useState<State | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const path = { params: { path: { ws } } };

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/data-network", path).then(({ data, error }) => (error ? setError(errorMessage(error)) : setS(data ?? null)));
  }, [ws]);

  async function showPreview() {
    setBusy(true);
    const { data, error } = await api.GET("/v1/workspaces/{ws}/data-network/preview", path);
    setBusy(false);
    setError(error ? errorMessage(error) : "");
    if (data) {
      setPreview(data);
      setConfirming(true);
      track("ui.data_network_previewed", { addresses: data.addresses });
    }
  }

  async function join() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/data-network/join", { ...path, body: { confirm: true } });
    setBusy(false);
    setError(error ? errorMessage(error) : "");
    if (data && "status" in data) {
      setS(data);
      setConfirming(false);
      toast("Joined the data network. Your lookups are now unlimited.");
    }
  }

  async function leave() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/data-network/leave", path);
    setBusy(false);
    setError(error ? errorMessage(error) : "");
    if (data && "status" in data) {
      setS(data);
      toast("Left the data network. What you shared is removed within 30 days.");
    }
  }

  function cancel() {
    setConfirming(false);
    track("ui.data_network_join_cancelled", { addresses: preview?.addresses ?? 0 });
  }

  // A server without a data network has nothing to show.
  if (s && !s.available) return null;
  const joined = s?.status === "joined";
  const allowance = s?.allowance;
  return (
    <div id="data-network">
      <Card
        title="Data network"
        description="Find and check work emails at companies you have never emailed, using what other Outflare workspaces learned. Nobody sees your contacts."
      >
        {!s ? (
          <ErrorText>{error}</ErrorText>
        ) : (
          <div className="space-y-3 text-[13px]">
            <div className="flex flex-wrap items-center gap-2">
              {joined ? <Status tone="green">Joined</Status> : <Status tone="grey">Not joined</Status>}
              {allowance && (
                <span className="text-muted-foreground">
                  {allowance.unlimited
                    ? "Unlimited lookups"
                    : `${Math.max((allowance.limit ?? 0) - allowance.used, 0)} of ${allowance.limit ?? 0} lookups left this month`}
                </span>
              )}
              {s.status === "left" && s.withdraw_by && (
                <span className="text-muted-foreground">What you shared is removed by {new Date(s.withdraw_by).toLocaleDateString()}.</span>
              )}
            </div>
            <ul className="list-disc space-y-1 pl-5 text-muted-foreground">
              <li>Members share whether the addresses their sequences emailed delivered or bounced, and the shape of each address (such as first.last).</li>
              <li>Never shared: names, lists, replies, message text, or anyone who unsubscribed or was blocked. Personal addresses such as Gmail are left out.</li>
              <li>The network only confirms an address, or a name and company, that you bring. It never shows who else knows them.</li>
              <li>You can leave at any time, and what you shared is removed within 30 days.</li>
            </ul>
            <ErrorText>{error}</ErrorText>
            {confirming && preview && (
              <div className="space-y-2 rounded-md border bg-muted/30 p-3">
                <p className="font-medium">Joining shares this now, and new sends as they settle:</p>
                <table className="w-full text-[13px] tabular-nums">
                  <tbody>
                    <tr><td className="py-0.5 text-muted-foreground">Addresses that delivered</td><td className="text-right">{preview.delivered.toLocaleString()}</td></tr>
                    <tr><td className="py-0.5 text-muted-foreground">Addresses that bounced</td><td className="text-right">{preview.bounced.toLocaleString()}</td></tr>
                    <tr><td className="py-0.5 text-muted-foreground">Company domains</td><td className="text-right">{preview.domains.toLocaleString()}</td></tr>
                    <tr><td className="py-0.5 text-muted-foreground">Names, lists, replies</td><td className="text-right">never</td></tr>
                  </tbody>
                </table>
                <p className="text-xs text-muted-foreground">A send is shared once it is {preview.settle_days} days old, so a bounce has had time to come back. Every owner and admin is emailed.</p>
                <div className="flex gap-2">
                  <Button disabled={busy} onClick={join}>{busy ? "Joining…" : "I'm the owner. Join"}</Button>
                  <Button variant="ghost" disabled={busy} onClick={cancel}>Cancel</Button>
                </div>
              </div>
            )}
            {!joined && !confirming && (isOwner ? (
              <Button variant="ghost" disabled={busy} onClick={showPreview}>{busy ? "Counting…" : "See what you'd share"}</Button>
            ) : (
              <p className="text-muted-foreground">Only the workspace's owner can join.</p>
            ))}
            {joined && (canManage ? (
              <Button variant="ghost" disabled={busy} onClick={leave}>{busy ? "Leaving…" : "Leave the data network"}</Button>
            ) : (
              <p className="text-muted-foreground">Only owners and admins can leave.</p>
            ))}
          </div>
        )}
      </Card>
    </div>
  );
}
