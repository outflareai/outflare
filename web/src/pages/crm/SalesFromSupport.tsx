import { useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { ArrowRight } from "lucide-react";
import { api } from "@/api";
import { Button } from "@/ui";
import { salesTeam } from "@/components/hand-off";
import { track } from "@/lib/analytics";
import type { Tab } from "@/App";

type Conversation = Schemas["SupportConversation"];
type Team = { id: string; name: string };

// What support handed to the sales team from the Inbox ("Sales
// opportunity"), shown with New conversations so a rep working from Sales
// sees it too. These are support conversations: they open in the Inbox,
// where the reply goes out from the support address.
export function SalesFromSupport({ ws, brand, go }: { ws: string; brand: string; go: (tab: Tab, item?: string) => void }) {
  const [rows, setRows] = useState<Conversation[]>([]);
  useEffect(() => {
    let live = true;
    (async () => {
      const teams = await api.GET("/v1/workspaces/{ws}/teams", { params: { path: { ws } } });
      const team = salesTeam(((teams.data as { data?: Team[] } | undefined)?.data ?? []) as Team[]);
      if (!team || !live) return;
      const { data } = await api.GET("/v1/brands/{brand}/support/conversations", {
        params: { path: { brand }, query: { team_id: team.id, status: "open", limit: 20 } as never },
      });
      if (live && data) setRows(data.data);
    })();
    return () => {
      live = false;
    };
  }, [ws, brand]);
  if (rows.length === 0) return null;
  return (
    <section className="space-y-2" aria-label="From support">
      <div>
        <h3 className="text-sm font-medium">From support</h3>
        <p className="text-xs text-muted-foreground">Customers support handed to sales. They open in the Inbox, where you reply.</p>
      </div>
      <div className="overflow-x-auto rounded-xl border bg-card">
        <table className="w-full text-left text-sm">
          <thead className="border-b bg-muted/20 text-xs text-muted-foreground">
            <tr>
              <th className="p-4 font-medium">Customer</th>
              <th className="min-w-64 p-4 font-medium">Conversation</th>
              <th className="p-4 font-medium">Owner</th>
              <th className="p-4 font-medium">Updated</th>
              <th className="p-4"><span className="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => (
              <tr key={c.id} className="border-b last:border-0">
                <td className="p-4 font-medium">{c.contact?.name || c.contact?.email || "Unknown customer"}</td>
                <td className="p-4">
                  <p className="font-medium">{c.subject || "No subject"}</p>
                  {c.preview && <p className="line-clamp-2 text-muted-foreground">{c.preview}</p>}
                </td>
                <td className="p-4 text-muted-foreground">{c.assignee?.name || "Nobody yet"}</td>
                <td className="p-4 whitespace-nowrap text-muted-foreground">{new Date(c.last_message_at ?? c.created_at).toLocaleString()}</td>
                <td className="p-4 text-right">
                  <Button
                    variant="ghost"
                    onClick={() => {
                      track("ui.sales_support_handoff_opened");
                      go("inbox", `support-${c.id}`);
                    }}
                  >
                    Open in Inbox <ArrowRight className="size-3.5" />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
