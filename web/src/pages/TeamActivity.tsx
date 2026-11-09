// The team's activity: approvals, agent runs and API calls about no brand
// in particular, such as invites, members and connections. Each brand's
// own are on its Agents and Logs pages.

import { useState } from "react";
import { PageHeader } from "../ui";
import { track } from "@/lib/analytics";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Everything, Waiting } from "./Agents";
import { AgentRunDetail, AgentRuns } from "./AgentRuns";
import { Logs } from "./Logs";

type Props = { ws: string; canApprove: boolean; canManage: boolean; canRun: boolean; canLogs: boolean };

export function TeamActivity({ ws, canApprove, canManage, canRun, canLogs }: Props) {
  const [tab, setTab] = useState("waiting");
  const [run, setRun] = useState<string>();
  const [, setCount] = useState(0);
  const pick = (v: string) => {
    setTab(v);
    track("ui.team_activity_opened", { view: v });
  };
  return (
    <div className="space-y-4">
      <PageHeader title="Team activity" description="Changes, agent runs and API calls about the team rather than one brand, such as invites, members and connections. Each brand's own are on its Agents and Logs pages." />
      <Tabs value={tab} onValueChange={pick}>
        <TabsList>
          <TabsTrigger value="waiting">Waiting for you</TabsTrigger>
          <TabsTrigger value="changes">Agent changes</TabsTrigger>
          <TabsTrigger value="runs">Agent runs</TabsTrigger>
          {canLogs && <TabsTrigger value="logs">API logs</TabsTrigger>}
        </TabsList>
        <TabsContent value="waiting">
          <Waiting ws={ws} canApprove={canApprove} canManage={canManage} onCount={setCount} team />
        </TabsContent>
        <TabsContent value="changes">
          <Everything ws={ws} tokens={null} team />
        </TabsContent>
        <TabsContent value="runs">
          {run ? <AgentRunDetail ws={ws} id={run} canRun={canRun} canApprove={canApprove} onBack={() => setRun(undefined)} team /> : <AgentRuns ws={ws} onOpen={setRun} team />}
        </TabsContent>
        {canLogs && (
          <TabsContent value="logs">
            <Logs ws={ws} canManage={canManage} team />
          </TabsContent>
        )}
      </Tabs>
    </div>
  );
}
