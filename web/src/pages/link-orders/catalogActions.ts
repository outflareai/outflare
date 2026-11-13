import type { LinkOrderDonor, PageData } from "./shared";

// The catalog's right-click menu. Each action is one entry here: the menu
// reads this list, so a new action is a new entry, not a change to the
// table. Keep labels plain and in sentence case.

/** The catalog column a right-click landed on. */
export type CatalogColumn = "request" | "site" | "dr" | "traffic" | "niche" | "intermediary" | "rel" | "duration" | "price";

/** What an action knows about the cell it was opened on. */
export type CatalogCellContext = {
  donor: LinkOrderDonor;
  column: CatalogColumn;
  data: PageData;
};

/** What an action may open. The catalog provides these. */
export type CatalogActionHost = {
  openDonor: (d: LinkOrderDonor) => void;
  findEmail: (d: LinkOrderDonor) => void;
  sendToSequence: (d: LinkOrderDonor) => void;
};

export type CatalogCellAction = {
  id: string;
  label: string;
  /** A group starts a new section of the menu. */
  group: "outreach" | "donor";
  /** A plain reason the action can't run for this person, or null when it can. */
  disabledReason: (ctx: CatalogCellContext) => string | null;
  run: (ctx: CatalogCellContext, host: CatalogActionHost) => void;
};

export const catalogCellActions: CatalogCellAction[] = [
  {
    id: "find-email",
    label: "Find email",
    group: "outreach",
    disabledReason: ({ data }) => (data.can("coverage.read") ? null : "You can't see the catalog."),
    run: ({ donor }, host) => host.findEmail(donor),
  },
  {
    id: "send-to-sequence",
    label: "Send to a sequence",
    group: "outreach",
    disabledReason: ({ data }) => {
      if (!data.canManage) return "You need permission to manage link orders.";
      if (!data.can("sequences.manage")) return "You need permission to manage sequences.";
      return null;
    },
    run: ({ donor }, host) => host.sendToSequence(donor),
  },
  {
    id: "open-donor",
    label: "Open donor",
    group: "donor",
    disabledReason: () => null,
    run: ({ donor }, host) => host.openDonor(donor),
  },
];

/** The catalog's outreach actions (Find email, Send to a sequence): the
 * same entries the menu shows, also offered as buttons on a site that is
 * recommended for direct contact. */
export const outreachActions = catalogCellActions.filter((a) => a.group === "outreach");

const directMissing: Record<string, string> = {
  pbn: "flagged as PBN",
  no_metrics: "no DR or traffic stored",
  no_email: "no stored email at the site",
};

/** Why a site is or isn't recommended for direct contact, in plain words. */
export function directContactReason(d: LinkOrderDonor): string {
  const dc = d.direct_contact;
  if (dc.recommended) {
    return `Worth writing to directly, not only through ${d.intermediary || "the intermediary"}: not a PBN, DR or traffic is stored, and ${dc.emails} stored email${dc.emails === 1 ? "" : "s"} at the site. Nothing is sent until you send it to a sequence.`;
  }
  return `Not recommended for direct contact: ${dc.missing.map((m) => directMissing[m] ?? m).join(", ")}.`;
}

/** The column a table cell belongs to, from its data-col attribute. */
export function cellFromEvent(target: EventTarget | null): { donorId: string; column: CatalogColumn } | null {
  if (!(target instanceof Element)) return null;
  const cell = target.closest<HTMLElement>("td[data-col]");
  const row = cell?.closest<HTMLElement>("tr[data-donor]");
  if (!cell || !row) return null;
  return { donorId: row.dataset.donor ?? "", column: cell.dataset.col as CatalogColumn };
}
