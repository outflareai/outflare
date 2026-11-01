import { BrandLinks, SupportLink } from "./PortalHelp";
import { toast } from "sonner";
import { useCallback, useEffect, useState } from "react";
import {
  ArrowLeft,
  ArrowRight,
  FileText,
  Globe2,
  LockKeyhole,
  Sparkles,
  CheckCircle2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { errorMessage } from "@/api";
import { ProposalDocument } from "./ProposalDocument";
import { proposalStage, type Proposal } from "./proposals";
import { request, type Me, type Program, money } from "./client";
export const invitationToken =
  /(?:^#|&)invite=(ofpi_[A-Za-z0-9_-]+)/.exec(location.hash)?.[1] || "";
type InvitationTeaser = {
  id: string;
  site_name: string;
  site_url: string;
  site_icon_url: string;
};
export function ProposalInvitation({ program }: { program: Program }) {
  const [teaser, setTeaser] = useState<InvitationTeaser | null>(null),
    [error, setError] = useState(""),
    [sent, setSent] = useState(false),
    [busy, setBusy] = useState(false),
    [iconFailed, setIconFailed] = useState(false);
  useEffect(() => {
    request<InvitationTeaser>("proposal-preview", "POST", {
      token: invitationToken,
    })
      .then(setTeaser)
      .catch((e) => setError(errorMessage(e)));
  }, []);
  async function sendLink() {
    setBusy(true);
    setError("");
    try {
      await request("proposal-sign-in", "POST", { token: invitationToken });
      setSent(true);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  let website: URL | null = null;
  try {
    if (teaser?.site_url) website = new URL(teaser.site_url);
  } catch {
    /* The invitation remains usable without a website. */
  }
  const siteName =
    teaser?.site_name ||
    website?.hostname.replace(/^www\./, "") ||
    "Your website or channel";
  const favicon =
    teaser?.site_icon_url ||
    (website?.protocol === "https:"
      ? new URL("/favicon.ico", website.origin).href
      : "");
  return (
    <div className="flex min-h-screen flex-col bg-sidebar/40 text-foreground">
      <header className="mx-auto flex w-full max-w-6xl flex-wrap items-center justify-between gap-4 px-6 py-7 sm:px-10">
        <div className="flex items-center gap-3">
          {program.logo_url && (
            <img
              src={program.logo_url}
              alt=""
              className="size-9 object-contain"
            />
          )}
          <span className="text-sm font-semibold">{program.title}</span>
        </div>
        <BrandLinks
          links={program.header_links}
          className="flex flex-wrap gap-5"
        />
      </header>
      <main className="mx-auto flex w-full max-w-xl flex-1 flex-col justify-center px-5 py-10 sm:py-16">
        <div className="mb-7 text-center">
          <span className="inline-flex items-center gap-2 rounded-full border bg-background px-3 py-1.5 text-[11px] text-muted-foreground">
            <Sparkles className="size-3.5" />A partnership, made for you
          </span>
          <h1 className="mt-6 text-3xl font-semibold tracking-tight sm:text-[38px] sm:leading-tight">
            We’d love to work with you
          </h1>
          <p className="mx-auto mt-4 max-w-md text-sm leading-7 text-muted-foreground">
            {program.title.replace(/ partners$/i, "")} has prepared a personal
            collaboration proposal. Sign in to see what we have in mind.
          </p>
        </div>
        <Card className="gap-6 p-6 shadow-none sm:p-8">
          {teaser ? (
            <>
              <div className="flex min-w-0 items-center gap-4 rounded-xl border bg-muted/20 p-4">
                <div className="grid size-14 shrink-0 place-items-center rounded-xl border bg-background">
                  {favicon && !iconFailed ? (
                    <img
                      src={favicon}
                      alt=""
                      referrerPolicy="no-referrer"
                      className="size-8 object-contain"
                      onError={() => setIconFailed(true)}
                    />
                  ) : (
                    <Globe2 className="size-7 text-muted-foreground" />
                  )}
                </div>
                <div className="min-w-0">
                  <p className="mb-1 text-[11px] text-muted-foreground">
                    A proposal for
                  </p>
                  <h2 className="break-words text-lg font-semibold">
                    {siteName}
                  </h2>
                  {website && (
                    <a
                      href={website.href}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="mt-1 block break-all text-xs text-muted-foreground hover:text-foreground"
                    >
                      {website.host}
                      {website.pathname !== "/" ? website.pathname : ""}
                    </a>
                  )}
                </div>
              </div>
              {sent ? (
                <div role="status" className="space-y-4 text-center">
                  <CheckCircle2 className="mx-auto size-8 text-emerald-600" />
                  <h2 className="text-xl font-semibold">Check your inbox</h2>
                  <p className="text-sm leading-6 text-muted-foreground">
                    Open the secure link sent to the email address that received
                    this invitation. It will take you straight to your proposal.
                  </p>
                  <Button variant="ghost" disabled={busy} onClick={sendLink}>
                    {busy ? "Sending…" : "Send the link again"}
                  </Button>
                </div>
              ) : (
                <>
                  <div className="flex items-start gap-3">
                    <LockKeyhole className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                    <p className="text-xs leading-6 text-muted-foreground">
                      Your proposal is private. Sign in to view the details,
                      compensation, and terms, then accept or reply to discuss
                      changes.
                    </p>
                  </div>
                  <div className="space-y-3">
                    <Button
                      size="lg"
                      className="w-full"
                      disabled={busy}
                      onClick={sendLink}
                    >
                      {busy ? "Sending your link…" : "Log in to view proposal"}
                      <ArrowRight className="size-4" />
                    </Button>
                    <div className="text-center">
                      <p className="text-xs text-muted-foreground">
                        First time here?
                      </p>
                      <Button
                        variant="ghost"
                        className="mt-1"
                        disabled={busy}
                        onClick={sendLink}
                      >
                        Sign up to view proposal
                      </Button>
                    </div>
                  </div>
                  <p className="text-center text-[11px] leading-5 text-muted-foreground">
                    One secure email link. No password to remember.
                  </p>
                </>
              )}
            </>
          ) : (
            !error && (
              <p
                role="status"
                className="py-6 text-center text-sm text-muted-foreground"
              >
                Opening your invitation…
              </p>
            )
          )}
          {error && (
            <p
              role="alert"
              className="rounded-lg border border-destructive/20 p-4 text-sm text-destructive"
            >
              {error}
            </p>
          )}
        </Card>
      </main>
      <footer className="mx-auto flex w-full max-w-6xl flex-wrap items-center justify-between gap-5 px-6 py-7 sm:px-10">
        <BrandLinks
          links={program.footer_links}
          className="flex flex-wrap gap-5"
        />
        <SupportLink program={program} />
      </footer>
    </div>
  );
}
export function PartnerProposals({
  me,
  program,
}: {
  me: Me;
  program: Program;
}) {
  const [list, setList] = useState<Proposal[]>([]),
    [selected, setSelected] = useState(
      new URLSearchParams(location.search).get("proposal") || "",
    ),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [agree, setAgree] = useState(false),
    [quote, setQuote] = useState(""),
    [note, setNote] = useState("");
  const load = useCallback(
    async () =>
      setList((await request<{ data: Proposal[] }>("proposals")).data || []),
    [],
  );
  useEffect(() => {
    void load().catch((e) => setError(errorMessage(e)));
  }, [load]);
  const p = list.find((x) => x.id === selected);
  async function act(action: string, extra: Record<string, unknown> = {}) {
    if (!p) return;
    setBusy(true);
    setError("");
    try {
      await request(`proposals/${p.id}`, "POST", {
        action,
        version: p.version,
        ...extra,
      });
      await load();
      toast.success(
        (
          {
            accept: "Proposal accepted",
            decline: "Proposal declined",
            quote: "Quote submitted",
            billing: "Invoice instructions saved",
            submit: "Work submitted for brand review",
          } as Record<string, string>
        )[action] || "Saved",
      );
      setAgree(false);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <p className="text-xs text-muted-foreground">
            Made for your partnership
          </p>
          <h1 className="mt-2 text-3xl font-semibold tracking-tight">
            Your proposals
          </h1>
        </div>
        {p && (
          <Button variant="ghost" onClick={() => setSelected("")}>
            <ArrowLeft className="size-4" />
            All proposals
          </Button>
        )}
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {p ? (
        <>
          <Card className="gap-6 p-6 shadow-none sm:p-8">
            <ProposalDocument
              config={p.config}
              proposal={p}
              currency={me.currency}
            />
            {p.status === "offered" && (
              <div className="space-y-4 border-t pt-6">
                <label className="flex items-start gap-3 text-sm leading-6">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={agree}
                    onChange={(e) => setAgree(e.target.checked)}
                  />
                  I agree to this proposal's deliverables, compensation and
                  terms.
                </label>
                <div className="flex gap-3">
                  <Button
                    disabled={busy || !agree || me.read_only}
                    onClick={() => act("accept")}
                  >
                    Accept proposal
                  </Button>
                  <Button
                    variant="outline"
                    disabled={busy || me.read_only}
                    onClick={() => act("decline")}
                  >
                    Decline
                  </Button>
                </div>
              </div>
            )}
            {p.status === "quote_requested" && (
              <form
                className="space-y-4 border-t pt-5"
                onSubmit={(e) => {
                  e.preventDefault();
                  void act("quote", {
                    amount_cents: Math.round(Number(quote) * 100),
                    note,
                  });
                }}
              >
                <h3 className="text-sm font-semibold">
                  Your quote ({me.currency})
                </h3>
                <Input
                  aria-label="Quote amount"
                  type="number"
                  min="0.01"
                  step="0.01"
                  required
                  value={quote}
                  onChange={(e) => setQuote(e.target.value)}
                />
                <Textarea
                  aria-label="Quote note"
                  placeholder="Optional context for your price"
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                />
                <Button disabled={busy || me.read_only}>Submit quote</Button>
                <Button
                  type="button"
                  variant="ghost"
                  disabled={busy || me.read_only}
                  onClick={() => act("decline")}
                >
                  Decline
                </Button>
              </form>
            )}
            {p.status === "quote_received" && (
              <p className="rounded-lg bg-muted/40 p-4 text-sm">
                Quote submitted: {money(p.progress.quoted_cents, me.currency)}.
                The brand will prepare a final offer for you to review.
              </p>
            )}
          </Card>
          {p.status === "accepted" && (
            <>
              <Card className="gap-4 p-6 shadow-none">
                <h3 className="text-base font-semibold">
                  Payment & invoice details
                </h3>
                <p className="text-xs leading-6 text-muted-foreground">
                  Add your payment method in account settings. Invoice or
                  receipt instructions below are shared with the brand for this
                  agreement.
                </p>
                <Button asChild variant="outline" className="w-fit">
                  <a href="#settings">
                    {me.payout_method
                      ? "Review payout details"
                      : "Add payout details"}
                  </a>
                </Button>
                <form
                  className="space-y-3"
                  onSubmit={(e) => {
                    e.preventDefault();
                    const data = new FormData(e.currentTarget);
                    void act("billing", {
                      note: String(data.get("invoice") || ""),
                    });
                  }}
                >
                  <Textarea
                    key={p.progress.invoice_note}
                    name="invoice"
                    aria-label="Invoice instructions"
                    defaultValue={p.progress.invoice_note}
                    maxLength={4000}
                    placeholder="Invoice reference, purchase order or receipt instructions. Do not enter bank credentials here."
                  />
                  <Button variant="outline" disabled={busy || me.read_only}>
                    Save invoice instructions
                  </Button>
                </form>
              </Card>
              <section className="space-y-4">
                <h3 className="text-lg font-semibold">Submit your work</h3>
                {p.config.deliverables.map((d) => {
                  const progress = p.progress.deliverables?.[d.id];
                  return (
                    <Card key={d.id} className="gap-4 p-5 shadow-none">
                      <h4 className="text-sm font-semibold">{d.title}</h4>
                      {progress?.verified_at ? (
                        <p className="text-xs text-muted-foreground">
                          Verified by the brand{" "}
                          {new Date(progress.verified_at).toLocaleDateString()}.
                          Keep this placement live for its agreed duration.
                        </p>
                      ) : (
                        <form
                          className="space-y-3"
                          onSubmit={(e) => {
                            e.preventDefault();
                            const f = new FormData(e.currentTarget);
                            void act("submit", {
                              deliverable_id: d.id,
                              url: f.get("url"),
                              note: f.get("note"),
                            });
                          }}
                        >
                          <Input
                            name="url"
                            aria-label={`Published URL for ${d.title}`}
                            type="url"
                            required
                            defaultValue={progress?.url}
                            placeholder="https://…"
                          />
                          <Textarea
                            name="note"
                            aria-label={`Evidence note for ${d.title}`}
                            defaultValue={progress?.note}
                            placeholder="Optional evidence or completion notes"
                            maxLength={4000}
                          />
                          <Button disabled={busy || me.read_only}>
                            {progress?.submitted_at
                              ? "Update submission"
                              : "Submit for brand review"}
                          </Button>
                        </form>
                      )}
                    </Card>
                  );
                })}
              </section>
            </>
          )}
        </>
      ) : list.length ? (
        <div className="grid gap-4 md:grid-cols-2">
          {list.map((x) => (
            <button
              key={x.id}
              className="rounded-xl border bg-card p-6 text-left transition hover:border-foreground/30"
              onClick={() => setSelected(x.id)}
            >
              <FileText className="mb-5 size-5 text-muted-foreground" />
              <p className="text-xs text-muted-foreground">
                {proposalStage(x)} · Version {x.version}
              </p>
              <h2 className="mt-2 text-lg font-semibold">{x.config.title}</h2>
              <p className="mt-3 line-clamp-2 text-sm leading-6 text-muted-foreground">
                {x.config.brief}
              </p>
              <span className="mt-5 inline-flex items-center gap-2 text-xs font-medium">
                View your proposal
                <ArrowRight className="size-3" />
              </span>
            </button>
          ))}
        </div>
      ) : (
        <Card className="gap-3 p-8 text-center shadow-none">
          <h2 className="text-lg font-semibold">
            Your next partnership starts here
          </h2>
          <p className="text-sm text-muted-foreground">
            Personal proposals from {program.title} will appear here.
          </p>
        </Card>
      )}
    </section>
  );
}
