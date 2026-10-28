import type { Tab } from "@/lib/paths";
import { track } from "@/lib/analytics";
import { Button } from "../ui";
import { ConnectorLogo } from "./connector-logo";

// Connecting a service from the page that needs it: the button carries the
// service's logo and opens it on Connectors, which says what this page uses
// it for and comes back here once it's connected (or the dialog is closed).
// The intent rides in sessionStorage so it survives an OAuth sign-in too.

export type ConnectIntent = { connector: string; back: Tab; item?: string; why: string; at: number };

const key = "outflare.connect_for";
const maxAge = 30 * 60 * 1000;

function save(intent: ConnectIntent) {
  try {
    sessionStorage.setItem(key, JSON.stringify(intent));
  } catch {
    // Without storage the dialog still opens; it just doesn't come back.
  }
}

// The intent for this connector, if a page asked for it in the last half hour.
export function connectIntent(connector?: string): ConnectIntent | null {
  try {
    const v = JSON.parse(sessionStorage.getItem(key) ?? "null") as ConnectIntent | null;
    if (!v || Date.now() - v.at > maxAge || (connector && v.connector !== connector)) return null;
    return v;
  } catch {
    return null;
  }
}

export function clearConnectIntent() {
  try {
    sessionStorage.removeItem(key);
  } catch {
    // Nothing to clear.
  }
}

type Props = {
  connector: string;
  name: string;
  // The page to come back to (and the item open on it), and one line on
  // what it uses the service for.
  back: Tab;
  item?: string;
  why: string;
  go: (tab: Tab, item?: string) => void;
  variant?: "primary" | "ghost";
  label?: string;
};

export function ConnectFor({ connector, name, back, item, why, go, variant, label }: Props) {
  return (
    <Button
      variant={variant}
      className="gap-2"
      onClick={() => {
        save({ connector, back, item, why, at: Date.now() });
        track("ui.connect_for_opened", { connector, from: back });
        go("connectors", connector);
      }}
    >
      <ConnectorLogo id={connector} size="sm" className="-ml-1 size-[18px]" />
      {label ?? `Connect ${name}`}
    </Button>
  );
}

// What each page says it uses Geonode for. One place, so every page that
// asks for Geonode says it the same way.
export const geonodeWhy = {
  social: "We use the Geonode Scraper or Web Data API for this: it reads each brand's public social profiles (X, LinkedIn, YouTube, Instagram, TikTok and more) once a day for their followers, a token or so each, on your own Geonode account.",
  brands: "We use the Geonode Scraper or Web Data API for this: it reads each brand's website to find its social profiles and app store listings, a token or two per brand, on your own Geonode account.",
  reviews: "We use the Geonode Scraper or Web Data API for this: it searches the web for each brand's Trustpilot, G2 and Capterra pages, then reads every review page once a day, a token or so each, on your own Geonode account.",
  audits: "We use the Geonode Scraper or Web Data API for this: it reads the pages a firewall blocks OutflareBot from, on sites you've proved you own, for a token per page on your own Geonode account.",
} as const;
