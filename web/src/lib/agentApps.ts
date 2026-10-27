import { useEffect, useSyncExternalStore } from "react";
import type { AgentApp } from "@outflare/sdk";
import { api } from "../api";

// The AI apps (Claude, ChatGPT, Cursor...) connected to a workspace, shared
// by the sidebar, Home and the Connect your AI page, so connecting one
// updates all of them. null until loaded.

type State = { ws: string; apps: AgentApp[] | null };
let state: State = { ws: "", apps: null };
const listeners = new Set<() => void>();
const set = (next: State) => {
  state = next;
  listeners.forEach((l) => l());
};

export async function reloadAgentApps(ws: string) {
  const { data } = await api.GET("/v1/workspaces/{ws}/agent-apps", { params: { path: { ws } } });
  if (data) set({ ws, apps: data.data ?? [] });
}

export function useAgentApps(ws: string): AgentApp[] | null {
  const s = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => state,
  );
  useEffect(() => {
    if (ws && s.ws !== ws) reloadAgentApps(ws);
  }, [ws, s.ws]);
  return s.ws === ws ? s.apps : null;
}

// The hosted MCP server's address (GET /v1/meta mcp_url), or "" when this
// server has none and the MCP server runs on the person's computer.
let mcpUrl: Promise<string> | undefined;
export function hostedMcpUrl(): Promise<string> {
  mcpUrl ??= api.GET("/v1/meta").then(({ data }) => data?.mcp_url ?? "");
  return mcpUrl;
}

// What each kind of app is called on screen.
export const appLabels: Record<AgentApp["app"], string> = {
  claude: "Claude",
  chatgpt: "ChatGPT",
  cursor: "Cursor",
  vscode: "VS Code",
  local: "An app on a computer",
  unknown: "Another app",
};
