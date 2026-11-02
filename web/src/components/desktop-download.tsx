import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";

export function DesktopDownloadLink({ location }: { location: "ai_accounts" | "computers" }) {
  return <div className="flex flex-col items-start gap-1"><a href="https://github.com/outflareai/outflare/releases/download/desktop-update-test/Outflare-desktop-update-test-macos.dmg" target="_blank" rel="noopener noreferrer"
    className={cn(buttonVariants({ variant: "outline", size: "sm" }), "text-[13px]")}
    onClick={() => track("ui.desktop_download_opened", { location })}>
    Download for Mac · update test
  </a><span className="text-xs text-muted-foreground">Tests one automatic upgrade. Stable release coming soon.</span></div>;
}
