import { pages } from "virtual:outflare-pages";
import type { Tab } from "./paths";

export const pageExtension = (tab: Tab) => pages[tab];
// Navigation must not offer a hosted page before its implementation ships.
export const pageIsAvailable = (tab: Tab) => tab !== "agent-mail" || !!pages[tab];
