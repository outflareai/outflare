import type { AgentAppKind } from "@outflare/sdk";
import { cn } from "@/lib/utils";
import { ConnectorLogo } from "./connector-logo";
import { Icon } from "../icons";
import cursor from "@/assets/apps/cursor.svg";
import cursorDark from "@/assets/apps/cursor.dark.svg";
import vscode from "@/assets/apps/vscode.svg";

// A small square naming an AI app: its logo where Outflare has one (Claude
// and ChatGPT's OpenAI mark from the connector logos; Cursor and VS Code,
// which aren't connectors, bundled from gilbarbara/logos, CC0), otherwise a
// plain glyph. Never a redrawn brand mark.
export function AppMark({ app, className }: { app: AgentAppKind | "other"; className?: string }) {
  const box = cn("inline-flex size-9 shrink-0 items-center justify-center rounded-lg border bg-background", className);
  switch (app) {
    case "claude":
      return <ConnectorLogo id="claude" className={cn("size-9 rounded-lg p-1.5", className)} />;
    case "cursor":
      return <Logo light={cursor} dark={cursorDark} className={className} />;
    case "chatgpt":
      return <ConnectorLogo id="openai" className={cn("size-9 rounded-lg p-1.5", className)} />;
    case "vscode":
      return <Logo light={vscode} dark={vscode} className={className} />;
    case "local":
      return <span aria-hidden className={box}><Icon name="terminal" className="size-5" /></span>;
    default:
      return <span aria-hidden className={box}><Icon name="connectors" className="size-5" /></span>;
  }
}

// A bundled logo pair, shown like ConnectorLogo: CSS picks the theme's one.
function Logo({ light, dark, className }: { light: string; dark: string; className?: string }) {
  return (
    <span aria-hidden className={cn("inline-flex size-9 shrink-0 items-center justify-center rounded-lg border bg-background p-1.5", className)}>
      <img src={light} alt="" draggable={false} className="size-full object-contain dark:hidden" />
      <img src={dark} alt="" draggable={false} className="hidden size-full object-contain dark:block" />
    </span>
  );
}
