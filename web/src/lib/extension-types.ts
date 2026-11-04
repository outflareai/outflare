import type { ComponentType } from "react";
import type { Workspace } from "@outflare/sdk";
import type { Tab } from "./paths";

/** A page runs inside the existing app shell and inherits its chosen context.
 * Authorization is still enforced by its API; this slot grants no access. */
export type ExtensionPageProps = {
  workspace: Workspace;
  brandId?: string;
};
export type ExtensionPages = Partial<Record<Tab, ComponentType<ExtensionPageProps>>>;
