import { pages } from "virtual:outflare-pages";
import type { Tab } from "./paths";

export const pageExtension = (tab: Tab) => pages[tab];
// Pages only a hosted server draws. Navigation must not offer one before
// its implementation ships.
const hostedOnly: readonly Tab[] = ["agent-mail", "outreach-mailboxes"];
export const pageIsAvailable = (tab: Tab) => !hostedOnly.includes(tab) || !!pages[tab];
