import { cn } from "@/lib/utils";

// A connector's logo on a small tile, served by the engine: the company's
// own mark, or a monogram when there is none. Both theme variants load and
// CSS shows the one for the app's theme, so switching themes never flickers.
const sizes = { sm: "size-5 rounded-[5px] p-0.5", md: "size-8 rounded-md p-1", lg: "size-10 rounded-lg p-1.5" };

export function ConnectorLogo({ id, size = "md", className }: { id: string; size?: keyof typeof sizes; className?: string }) {
  // v: see logoRev in the engine (integrations.go).
  const src = `/v1/connectors/${encodeURIComponent(id)}/logo.svg?v=2`;
  return (
    <span aria-hidden className={cn("inline-flex shrink-0 items-center justify-center border bg-background", sizes[size], className)}>
      <img src={src} alt="" loading="lazy" decoding="async" draggable={false} className="size-full object-contain dark:hidden" />
      <img src={`${src}&theme=dark`} alt="" loading="lazy" decoding="async" draggable={false} className="hidden size-full object-contain dark:block" />
    </span>
  );
}
