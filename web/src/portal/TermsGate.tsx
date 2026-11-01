import { useState } from "react";
import { ArrowUpRight, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { request, type Program } from "./client";
import { errorMessage } from "@/api";
import { SupportLink } from "./PortalHelp";
export function TermsGate({
  program,
  readOnly,
  onAccepted,
}: {
  program: Program;
  readOnly: boolean;
  onAccepted: () => Promise<void>;
}) {
  const [accept, setAccept] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <div className="mx-auto max-w-xl py-10">
      <Card className="gap-6 p-7 sm:p-9 shadow-none">
        <ShieldCheck className="size-8 text-muted-foreground" />
        <div>
          <p className="text-xs text-muted-foreground">
            Before you start promoting
          </p>
          <h1 className="mt-2 text-2xl font-semibold tracking-tight">
            Review your program terms
          </h1>
          <p className="mt-4 text-sm leading-6 text-muted-foreground">
            Please review {program.title}'s current terms, including promotion
            requirements and reward eligibility. When these terms change, we'll
            ask you to review them again.
          </p>
        </div>
        <Button asChild variant="outline">
          <a href={program.terms_url} target="_blank" rel="noopener noreferrer">
            Open program terms
            <ArrowUpRight className="size-4" />
          </a>
        </Button>
        <label className="flex items-start gap-3 text-sm leading-6">
          <input
            className="mt-1 size-4"
            type="checkbox"
            checked={accept}
            onChange={(e) => setAccept(e.target.checked)}
            disabled={readOnly}
          />
          I have read and agree to the current program terms.
        </label>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <Button
          disabled={!accept || busy || readOnly}
          onClick={async () => {
            setBusy(true);
            try {
              await request("accept-terms", "POST", {
                accept: true,
                version: program.terms_version,
              });
              await onAccepted();
            } catch (e) {
              setError(errorMessage(e));
            } finally {
              setBusy(false);
            }
          }}
        >
          {busy ? "Saving…" : "Agree and continue"}
        </Button>
        {readOnly && (
          <p className="text-xs text-muted-foreground">
            Only the partner can accept terms. This preview cannot consent on
            their behalf.
          </p>
        )}
        <SupportLink program={program} />
      </Card>
    </div>
  );
}
