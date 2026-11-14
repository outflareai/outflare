export const salesSections: Record<string, string> = {
  new: "New leads",
  mine: "My leads",
  inbox: "Inbox",
  handoffs: "Sent to AEs",
  all: "All leads",
  lead: "Conversation",
  templates: "My templates",
  introduction: "My introduction",
  email: "My email",
  team: "Team",
  settings: "Sales settings",
};

export function salesPageName(item?: string) {
  return item?.startsWith("sales:")
    ? salesSections[item.split(":")[1]]
    : undefined;
}

export function salesExternalURL(value?: string) {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    return ["https:", "http:"].includes(url.protocol) ? url.href : undefined;
  } catch {
    return undefined;
  }
}
