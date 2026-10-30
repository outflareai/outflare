// Outflare Cloud features (ADR 11). The engine lists each one so every
// server can show it where it sits; a server that runs it lists its
// capability in /v1/meta and the real page loads. Elsewhere the page says
// what it does and where to get it. Only a feature's name and one line go
// here, once it runs on app.outflare.ai.

import { useEffect, useState } from "react";
import { api } from "../api";
import type { Tab } from "./paths";

export type CloudFeature = { tab: Tab; capability: string; name: string; line: string };

export const cloudFeatures: CloudFeature[] = [
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
];

export const cloudFeature = (tab: Tab) => cloudFeatures.find((f) => f.tab === tab);

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
