// What the Errors page copies: an error as plain text, and a prompt that
// hands one or more errors to an AI coding assistant to fix. Built only
// from what the page already shows: no visitor ids, no person.

import type { paths } from "@outflare/sdk";

type Detail = paths["/v1/brands/{brand}/errors/{fingerprint}"]["get"]["responses"]["200"]["content"]["application/json"];
type Occurrence = Detail["occurrences"][number];

const pct = (x: number) => (x > 0 && x < 0.001 ? "under 0.1%" : `${(x * 100).toLocaleString("en", { maximumFractionDigits: x < 0.1 ? 1 : 0 })}%`);
const plural = (n: number, one: string) => `${n.toLocaleString("en")} ${one}${n === 1 ? "" : "s"}`;
const day = (s: string) => s.slice(0, 10);

/** The error's one-line title: "TypeError: plan 7 not found". */
export const errorTitle = (type: string, message: string) => `${type || "Error"}: ${message || "No message"}`;

// Browsers and systems as they are written: "macos" is "macOS".
const proper: Record<string, string> = { chrome: "Chrome", firefox: "Firefox", safari: "Safari", edge: "Edge", opera: "Opera", samsung: "Samsung Internet", macos: "macOS", ios: "iOS", ipados: "iPadOS", windows: "Windows", android: "Android", linux: "Linux", chromeos: "ChromeOS" };
/** A browser's or system's name as it is written. */
export const properName = (s: string) => proper[s.toLowerCase()] ?? s;

/** Where it ran, as a person says it: "Chrome on macOS (desktop)". */
export function browserLine(o: Pick<Occurrence, "browser" | "os" | "device">): string {
  const what = [properName(o.browser), o.os && `on ${properName(o.os)}`].filter(Boolean).join(" ");
  return what ? `${what}${o.device ? ` (${o.device})` : ""}` : "";
}

// The browser with its versions: "Chrome 142.0 on macOS 15.1 (desktop)".
function browserDetail(o: Occurrence): string {
  const b = [properName(o.browser), o.browser_version].filter(Boolean).join(" ");
  const os = [properName(o.os), o.os_version].filter(Boolean).join(" ");
  const what = [b, os && `on ${os}`].filter(Boolean).join(" ");
  return what ? `${what}${o.device ? ` (${o.device})` : ""}` : "";
}

// What the visitor's browser was set to: window size, language, colours, motion.
function settingLine(o: Occurrence): string {
  return [
    o.viewport && `window ${o.viewport}`,
    o.language && `language ${o.language}`,
    o.country && `country ${o.country}`,
    o.color_scheme === "dark" && "dark mode",
    o.reduced_motion === "reduce" && "reduced motion",
  ].filter(Boolean).join(", ");
}

// The stack to hand over: the one read through source maps when there is
// one, which names the brand's own files.
const bestStack = (o?: Occurrence) => (o ? o.stack_original || o.stack : "");

// Looks minified: short names everywhere and no source map read it.
const minified = (o?: Occurrence) => !!o && !o.stack_original && /\bat [a-zA-Z$_]{1,2} \(|\.min\.js|\/assets\/[^/]+-[A-Za-z0-9]{6,}\.js/.test(o.stack);

/** One error as text, to paste into a ticket or a chat. */
export function errorText(d: Detail, o: Occurrence | undefined = d.occurrences[0]): string {
  const i = d.issue;
  const lines = [errorTitle(o?.type ?? i.type, o?.message ?? i.message)];
  const full = o?.message_full || i.message_full;
  if (full) lines.push(`In full: ${full}`);
  const stack = bestStack(o);
  if (stack) lines.push("", stack);
  if (o?.component_stack) lines.push("", "React components:", o.component_stack.trim());
  lines.push("");
  if (o?.url || o?.path) lines.push(`Page: ${o.url || o.path}`);
  if (o) {
    const b = browserDetail(o);
    if (b) lines.push(`Browser: ${b}`);
    const s = settingLine(o);
    if (s) lines.push(`Setting: ${s}`);
    if (o.release) lines.push(`Release: ${o.release}`);
    lines.push(`${o.handled ? "Caught by our code (captureException)" : "Uncaught"}, ${o.source === "browser" ? "in the browser" : "on the server"}`);
  }
  lines.push(`Seen ${plural(i.count, "time")} by ${plural(i.visitors, "visitor")}; first ${day(i.first_seen)}, last ${day(i.last_seen)}`);
  return lines.join("\n");
}

// One issue's part of a prompt: the error, where and how often, and its stack.
function issueSection(d: Detail, n?: number): string {
  const i = d.issue;
  const o = d.occurrences[0];
  const head = n === undefined ? "## The error" : `## Error ${n}`;
  const out = [head, "", errorTitle(o?.type ?? i.type, o?.message ?? i.message), ""];
  const full = o?.message_full || i.message_full;
  if (full) out.push(`The production build shortens this message to a code. In full it says: ${full}`, "");
  const where = o ? (o.source === "browser" ? "In the browser (our website or web app)" : "On our server") : "";
  const facts = [
    where && `- Where: ${where}, ${o!.handled ? "caught by our own code and reported with captureException" : "not caught by anything"}.`,
    `- How often: ${plural(i.count, "time")}, to ${plural(i.visitors, "visitor")}, in the last ${plural(d.days.length - 1 || 1, "day")}. First seen ${day(i.first_seen)}, last seen ${day(i.last_seen)}.`,
    i.regressed ? "- It was marked fixed before and has happened again since, so an earlier fix didn't cover every case." : "",
    i.visitor_share != null ? `- How common: it happens to ${pct(i.visitor_share)} of the site's visitors${i.worst_page ? `, and to ${pct(i.worst_page.share)} of the visitors to ${i.worst_page.path}` : ""}.` : "",
    i.host ? `- Site: ${i.host}${i.hosts > 1 ? ` (and ${i.hosts - 1} more)` : ""}.` : "",
    d.pages.length ? `- Pages: ${d.pages.slice(0, 5).map((p) => `${p.path || "/"} (${p.count}${p.page_views ? `, ${pct(p.view_share)} of its views` : ""})`).join(", ")}.` : "",
  ];
  const browsers = [...new Set(d.occurrences.map(browserDetail).filter(Boolean))].slice(0, 5);
  if (browsers.length) facts.push(`- Browsers: ${browsers.join(", ")}.`);
  const settings = o ? settingLine(o) : "";
  if (settings) facts.push(`- The latest visitor's browser: ${settings}.`);
  if (i.first_release) facts.push(`- First appeared in release ${i.first_release}${i.last_release && i.last_release !== i.first_release ? `, latest in ${i.last_release}` : ""}. Compare that release with the one before it.`);
  if (o?.url) facts.push(`- Latest page address: ${o.url}`);
  out.push(...facts.filter(Boolean));
  const stack = bestStack(o);
  if (stack) {
    out.push("", o?.stack_original ? "Stack trace (mapped to our source files):" : minified(o) ? "Stack trace (from minified code, so names are short):" : "Stack trace:", "", "```", stack, "```");
  } else {
    out.push("", "It came with no stack trace.");
  }
  if (o?.component_stack) out.push("", "React components it happened in (innermost first):", "", "```", o.component_stack.trim(), "```");
  return out.join("\n");
}

/** A prompt for an AI coding assistant to fix one or more errors. */
export function fixPrompt(details: Detail[]): string {
  if (details.length === 0) return "";
  const one = details.length === 1;
  const anyMinified = details.some((d) => minified(d.occurrences[0]) || !d.occurrences[0]?.stack);
  const intro = one
    ? "This error is happening in production. Please find its root cause in this codebase and fix it."
    : `These ${details.length} errors are happening in production. Please find the root cause of each in this codebase and fix them, one at a time.`;
  const sections = details.map((d, n) => issueSection(d, one ? undefined : n + 1));
  const steps = [
    "## What to do",
    "",
    "1. Find where it's thrown: follow the stack trace to our own files and functions" + (anyMinified ? ". Where the stack is minified or missing, search the codebase for the error message and the code that runs on the pages listed." : "."),
    "2. Explain the root cause in a sentence or two: what input or state makes it happen.",
    "3. Fix the cause, not the symptom. Handle the case properly; don't wrap it in an empty try/catch or hide the message.",
    "4. Add or update a test that fails without the fix.",
    "5. Tell me what you changed and anything you couldn't confirm.",
  ];
  return [intro, "", ...sections.flatMap((s) => [s, ""]), ...steps].join("\n");
}
