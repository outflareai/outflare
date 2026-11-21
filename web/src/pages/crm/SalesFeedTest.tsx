import type { Schemas } from "@outflare/sdk";
import { ErrorText, Loading } from "@/ui";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type Preview = Schemas["SalesFeedPreview"];

const wouldMake: Record<Preview["would_make"], string> = {
  conversation: "A Sales conversation",
  signal: "A signal on the company",
  context: "Context on the person",
  needs_look: "Needs a look",
  spam: "Spam",
};

// What trying a channel found: its latest posts and what reading each one
// would make. Post text and fields come from Slack and are shown as text.
export function SalesFeedTestDialog({
  channelName,
  previews,
  error,
  onClose,
}: {
  channelName: string;
  previews: Preview[] | null;
  error: string;
  onClose: () => void;
}) {
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Test #{channelName}</DialogTitle>
          <DialogDescription>
            The channel’s latest posts and what reading each one would make.
            Nothing is saved.
          </DialogDescription>
        </DialogHeader>
        <ErrorText>{error}</ErrorText>
        {error ? null : previews === null ? (
          <Loading />
        ) : previews.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            The channel has no posts to read yet.
          </p>
        ) : (
          <ol className="divide-y rounded-lg border">
            {(previews ?? []).map((p) => (
              <li key={p.slack_ts} className="space-y-2 p-3 text-sm">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <strong className="font-medium">
                    {wouldMake[p.would_make]}
                  </strong>
                  <span className="text-xs text-muted-foreground">
                    {new Date(p.at).toLocaleString()}
                  </span>
                </div>
                {p.summary && (
                  <p className="text-muted-foreground">{p.summary}</p>
                )}
                {p.fields.length > 0 && (
                  <dl className="grid grid-cols-[minmax(6rem,auto)_1fr] gap-x-3 gap-y-1 text-xs">
                    {p.fields.map((f, i) => (
                      <div key={i} className="contents">
                        <dt className="text-muted-foreground">{f.label}</dt>
                        <dd className="break-words">{f.value}</dd>
                      </div>
                    ))}
                  </dl>
                )}
                {p.candidates.length > 0 && (
                  <p className="text-xs">
                    <span className="text-muted-foreground">
                      Addresses found:{" "}
                    </span>
                    {p.candidates.join(", ")}
                  </p>
                )}
                <p className="line-clamp-4 whitespace-pre-wrap break-words text-xs text-muted-foreground">
                  {p.text}
                </p>
              </li>
            ))}
          </ol>
        )}
      </DialogContent>
    </Dialog>
  );
}
