export const salesSections: Record<string, string> = {
  new: "New conversations",
  mine: "My conversations",
  inbox: "Waiting for you",
  handoffs: "Sent to AEs",
  all: "All conversations",
  lead: "Conversation",
  templates: "My templates",
  introduction: "My introduction",
  email: "My email",
  team: "Team",
  activity: "Team activity",
  settings: "Sales settings",
  channels: "Sales channels",
  needs_look: "Needs a look",
  spam: "Spam",
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

// An API answer's body, or a throw errorMessage() can show: an error, no
// body, or a write held for approval (202).
export function salesChecked<T>(r: { data?: unknown; error?: unknown }): T {
  if (r.error || !r.data)
    throw r.error ?? { error: { message: "No answer received." } };
  if (
    typeof r.data === "object" &&
    ("action" in r.data || "mission_action" in r.data)
  )
    throw { error: { message: "This action is waiting for approval." } };
  return r.data as T;
}
