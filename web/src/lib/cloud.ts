// Outflare Cloud features (ADR 11). The engine lists each one so every
// server can show it where it sits; a server that runs it lists its
// capability in /v1/meta and the real page loads. Elsewhere the page says
// what it does and where to get it. Only a feature's name and one line go
// here, once it runs on app.outflare.ai.

import { useEffect, useState } from "react";
import { api } from "../api";
import type { Tab } from "./paths";

// tab is the page a feature has to itself; a feature that is one setting
// on an open page (such as Auto in Support settings) has none, and the
// page shows its line where the setting is.
export type CloudFeature = { tab?: Tab; capability: string; name: string; line: string };

export const cloudFeatures: CloudFeature[] = [
  {
    tab: "agent-mail",
    capability: "agent_mail",
    name: "Agent Mail",
    line: "Persistent inboxes for agents, with sending, receiving, attachments, API access and webhooks.",
  },
  {
    tab: "lead-finder",
    capability: "lead_finder",
    name: "Lead finder",
    line: "AI that searches the web for companies that fit your ideal customer, reads their websites and tells you which fit and why.",
  },
  {
    tab: "link-autopilot",
    capability: "link_autopilot",
    name: "Link autopilot",
    line: "Set how many new live links you want a month and a budget; Outflare's agent runs the link building playbooks toward it, and you approve each change.",
  },
  {
    tab: "pr-autopilot",
    capability: "pr_autopilot",
    name: "Reactive PR autopilot",
    line: "Each morning, your experts' comments on today's news, drafted for them to approve and pitched to the journalists covering it.",
  },
  {
    capability: "brand_agent",
    name: "Brand agent",
    line: "One agent per brand that works toward your objectives in turns on your own AI model, with a chat, objectives that read their own numbers, and every change through the action layer.",
  },
  {
    capability: "support_auto_replies",
    name: "Support assistant Auto",
    line: "The support assistant answers customers' chats and emails on its own when every check passes, signed as an AI assistant, and hands the rest to your team.",
  },
];

export const cloudFeature = (tab: Tab) => cloudFeatures.find((f) => f.tab === tab);
export const cloudCapability = (capability: string) => cloudFeatures.find((f) => f.capability === capability)!;

// The server's capabilities, read once per page load.
let capabilities: Promise<string[]> | undefined;
export function useCapabilities(): string[] | null {
  const [caps, setCaps] = useState<string[] | null>(null);
  useEffect(() => {
    capabilities ??= api.GET("/v1/meta").then(({ data }) => data?.capabilities ?? []).catch(() => []);
    let live = true;
    capabilities.then((c) => live && setCaps(c));
    return () => {
      live = false;
    };
  }, []);
  return caps;
}
