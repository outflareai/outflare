import { useEffect, useRef, useState, type ReactNode } from "react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

// Keyboard shortcuts for the Inbox. Each is a single key, pressed while
// not typing; "?" (or the Keyboard shortcuts button under the views)
// lists them, so none has to be remembered or guessed.
export type InboxKey = { key: string; label: string; run: () => void };

const showKeys = "outflare:inbox-keys";
// Ask the open conversation to put the cursor in its reply or note box.
export const writeEvent = "outflare:inbox-write";
export const askToWrite = (mode: "reply" | "note") => window.dispatchEvent(new CustomEvent(writeEvent, { detail: mode }));

export function KeysButton() {
  return (
    <button type="button" onClick={() => window.dispatchEvent(new Event(showKeys))} className="flex w-full items-center justify-between gap-2 rounded-md px-2 py-1.5 text-left text-muted-foreground hover:bg-accent/60 hover:text-foreground">
      Keyboard shortcuts
      <kbd className="rounded border px-1 font-mono text-[11px]">?</kbd>
    </button>
  );
}

export function useInboxKeys(keys: InboxKey[]): ReactNode {
  const [help, setHelp] = useState(false);
  const latest = useRef(keys);
  latest.current = keys;
  // No keys (another list is showing): nothing listens.
  const on = keys.length > 0;
  useEffect(() => {
    if (!on) return;
    // "g" starts the app's go-to shortcuts, so the key after it is theirs.
    let g = 0;
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement;
      if (e.metaKey || e.ctrlKey || e.altKey || el.closest("input, textarea, select, [contenteditable], [role=dialog], [role=alertdialog], [role=menu]")) return;
      if (e.key === "g") return void (g = Date.now());
      if (Date.now() - g < 1200) return void (g = 0);
      if (e.key === "?") {
        e.preventDefault();
        return setHelp(true);
      }
      const k = latest.current.find((x) => x.key === e.key);
      if (!k) return;
      e.preventDefault();
      k.run();
    };
    const show = () => setHelp(true);
    window.addEventListener("keydown", onKey);
    window.addEventListener(showKeys, show);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener(showKeys, show);
    };
  }, [on]);
  return (
    <Dialog open={help} onOpenChange={setHelp}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>Press a key while you aren't typing. Everything here is also in the right-click menu or on the page.</DialogDescription>
        </DialogHeader>
        <dl className="space-y-1.5 text-[13px]">
          {[...keys, { key: "?", label: "Show this list" }].map((k) => (
            <div key={k.key} className="flex items-center justify-between gap-4">
              <dt>{k.label}</dt>
              <dd><kbd className="rounded border bg-muted px-1.5 py-0.5 font-mono text-[12px]">{k.key}</kbd></dd>
            </div>
          ))}
        </dl>
      </DialogContent>
    </Dialog>
  );
}

// The row after (or before) the open one; the first when none is open.
export function neighbour<T extends { id: string }>(list: T[] | null, open: string | undefined, step: 1 | -1): string | undefined {
  if (!list?.length) return undefined;
  const at = list.findIndex((x) => x.id === open);
  if (at < 0) return list[0].id;
  return list[Math.min(list.length - 1, Math.max(0, at + step))].id;
}
