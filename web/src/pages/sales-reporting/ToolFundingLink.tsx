import { ExternalLink } from "lucide-react";
import { billingPage } from "./results";

export function ToolFundingLink({
  wallet,
  onOpen,
}: {
  wallet: string | null;
  onOpen: (wallet: string) => void;
}) {
  const page = billingPage(wallet);
  return page && wallet ? (
    <a
      href={page.url}
      target="_blank"
      rel="noopener noreferrer"
      className="sr-text-link mt-1 inline-flex whitespace-nowrap text-xs"
      aria-label={`${page.action} at ${page.name} (opens in new tab)`}
      onClick={() => onOpen(wallet)}
    >
      {page.action} at {page.name}
      <ExternalLink className="size-3" />
    </a>
  ) : null;
}
