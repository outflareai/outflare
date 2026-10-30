// AI accounts → Computers: the computers running Outflare for desktop,
// which read AI usage and report it. People see and unlink their own;
// admins see everyone's.

import { useCallback, useEffect, useState } from "react";
import type { AIDevice } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, Loading, toast } from "../ui";
import { Switch } from "@/components/ui/switch";
import { DataTable } from "@/components/data-table";
import { readAgo } from "@/lib/aiAccounts";

const platforms: Record<string, string> = { macos: "Mac", windows: "Windows", linux: "Linux" };

// What each error code from the app means for the person.
const problems: Record<string, string> = {
  not_signed_in: "An AI tool isn't signed in on this computer. Sign in to it again.",
  token_expired: "An AI tool's sign-in expired. Open it once to refresh it.",
  keychain_denied: "The app wasn't allowed to read a tool's sign-in. Allow it when the computer asks.",
  rate_limited: "An AI tool asked the app to slow down. It tries again later by itself.",
  forbidden: "An AI tool refused the app. Outflare is looking into it.",
  bad_response: "An AI tool answered something the app doesn't understand. Outflare is looking into it.",
  network: "The app couldn't reach an AI tool. Check the connection.",
};

function state(d: AIDevice) {
  if (d.silent) return <Badge>Silent</Badge>;
  if (d.status === "broken") return <Badge tone="red">Broken</Badge>;
  if (d.status === "attention") return <Badge tone="amber">Needs attention</Badge>;
  return <Badge tone="green">Reporting</Badge>;
}

export function ComputersView({ ws, me, seeAll, manageAll }: { ws: string; me: string; seeAll: boolean; manageAll: boolean }) {
  const [list, setList] = useState<AIDevice[] | null>(null);
  const [error, setError] = useState("");
  const [everyone, setEveryone] = useState(false);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/ai-devices", { params: { path: { ws }, query: everyone ? { everyone: true } : {} } });
    if (error) setError(errorMessage(error));
    setList(data?.data ?? []);
  }, [ws, everyone]);
  useEffect(() => {
    load();
  }, [load]);

  async function unlink(d: AIDevice) {
    const own = d.user_id === me;
    const body = own ? "It stops reporting your AI usage. Link it again from the app any time." : `It stops reporting ${d.user_name}'s AI usage until they link it again.`;
    if (!(await confirmAction(`Unlink ${d.name}?`, body, "Unlink"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/ai-devices/{id}", { params: { path: { ws, id: d.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Unlinked ${d.name}`);
    load();
  }

  return (
    <div className="space-y-6">
      <Card title="Outflare for desktop" description="A small app for Mac, Windows and Linux that sits in the menu bar or tray and reads how much of each limit your AI tools have used and when they reset: Claude, ChatGPT and Codex, Cursor and GitHub Copilot.">
        <ul className="space-y-1.5 text-[13px] text-muted-foreground">
          <li>Install it, open it and click <span className="font-medium text-foreground">Link this computer</span>. Outflare opens; check the code and approve it.</li>
          <li>It starts when you sign in to your computer and reports every 10 minutes. Only usage numbers, plans and resets reach Outflare: never your prompts, files or the tools' sign-ins.</li>
          <li>While it is being tested, your admin shares the installer.</li>
        </ul>
      </Card>
      <ErrorText>{error}</ErrorText>
      {seeAll && (
        <label className="flex items-center justify-end gap-2 text-[13px] text-muted-foreground">
          <Switch checked={everyone} onCheckedChange={setEveryone} aria-label="Show everyone's computers" /> Everyone's
        </label>
      )}
      {list === null ? (
        <Loading />
      ) : list.length === 0 ? (
        <Empty title={everyone ? "No computers linked yet" : "You haven't linked a computer yet"}>Install Outflare for desktop and link it to see live usage on your accounts.</Empty>
      ) : (
        <DataTable
          data={list}
          rowId={(d) => d.id}
          searchPlaceholder="Search computers"
          mobile={(d) => (
            <div className="space-y-0.5">
              <div className="font-medium">{d.name} {state(d)}</div>
              <div className="text-[12px] text-muted-foreground">{platforms[d.platform]} · {d.last_report_at ? readAgo(d.last_report_at) : "never reported"}</div>
            </div>
          )}
          columns={[
            {
              id: "name", header: "Computer", accessorFn: (d) => `${d.name} ${d.user_name}`,
              cell: ({ row: { original: d } }) => (
                <div>
                  <div className="font-medium">{d.name}</div>
                  <div className="text-[12px] text-muted-foreground">{platforms[d.platform]} · app {d.app_version}{everyone ? ` · ${d.user_name}` : ""}</div>
                </div>
              ),
            },
            {
              id: "state", header: "State", accessorFn: (d) => (d.silent ? 3 : d.status === "broken" ? 2 : d.status === "attention" ? 1 : 0),
              cell: ({ row: { original: d } }) => (
                <div className="max-w-80">
                  {state(d)}
                  {!d.silent && d.last_error_code && <div className="mt-1 text-[12px] text-muted-foreground">{problems[d.last_error_code] ?? "An AI tool couldn't be read."}</div>}
                  {d.silent && <div className="mt-1 text-[12px] text-muted-foreground">No report for over an hour: the computer is off or the app isn't running.</div>}
                </div>
              ),
            },
            { id: "report", header: "Last report", accessorFn: (d) => d.last_report_at ?? "", cell: ({ row: { original: d } }) => <span className="whitespace-nowrap text-muted-foreground">{d.last_report_at ? readAgo(d.last_report_at) : "Never"}</span> },
            {
              id: "actions", header: "", enableSorting: false,
              cell: ({ row: { original: d } }) => (d.user_id === me || manageAll) && <Button variant="ghost" onClick={() => unlink(d)}>Unlink</Button>,
            },
          ]}
        />
      )}
    </div>
  );
}
