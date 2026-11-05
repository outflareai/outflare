import type { ComponentType } from "react";
import type { Workspace } from "@outflare/sdk";
import type { Tab } from "./paths";

/** A page runs inside the existing app shell and inherits its chosen context.
 * Authorization is still enforced by its API; this slot grants no access. */
export type ExtensionPageProps = {
  workspace: Workspace;
  brandId?: string;
  /** Where the page is drawn: its own page (the default), the panel beside
   * every page ("dock"), or a card on Home ("home"). */
  variant?: "page" | "dock" | "home";
  /** Opens the panel beside every page (from a Home card). */
  onOpenDock?: () => void;
  /** Closes the panel (from the panel itself). */
  onClose?: () => void;
  /** Goes to the page's own tab, for the whole view. */
  onOpenPage?: () => void;
};
export type ExtensionPages = Partial<Record<Tab, ComponentType<ExtensionPageProps>>>;
