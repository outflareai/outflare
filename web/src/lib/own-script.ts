// Whether a crash is this app's: a browser extension or another injected
// script can throw on our page, and that is not ours to fix. A stack that
// names scripts but none of this site's is someone else's; no stack at all
// could be ours, so it counts.
export function ownScript(stack: string | undefined, origin: string): boolean {
  if (!stack) return true;
  const scripts = stack.match(/[a-z][a-z0-9+.-]*:\/\/[^\s)]+/gi) ?? [];
  if (scripts.length === 0) return true;
  return scripts.some((u) => u.startsWith(origin + "/"));
}
