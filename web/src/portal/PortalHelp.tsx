import { useMemo, useState } from "react";
import {
  ArrowLeft,
  ArrowUpRight,
  BookOpen,
  LifeBuoy,
  Search,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { track } from "@/lib/analytics";
import { money, type Me, type Program } from "./client";

type Article = { id: string; title: string; category: string; body: string };
const categories: Record<string, string> = {
  getting_started: "Getting started",
  promotion: "Promotion tools",
  tracking: "Tracking & conversions",
  payouts: "Earnings & payouts",
  integrations: "Integrations",
  rules: "Program rules",
};
export function BrandLinks({
  links,
  className = "",
}: {
  links?: { label: string; url: string }[] | null;
  className?: string;
}) {
  return (
    <nav aria-label="Brand links" className={className}>
      {(links || []).map((l, i) => (
        <a
          className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
          key={i}
          href={l.url}
          target="_blank"
          rel="noopener noreferrer"
        >
          {l.label}
          <ArrowUpRight className="size-3" />
        </a>
      ))}
    </nav>
  );
}
export function SupportLink({ program }: { program: Program }) {
  const href =
    program.support_url ||
    (program.support_email ? `mailto:${program.support_email}` : "");
  return href ? (
    <Button variant="outline" asChild>
      <a href={href} target="_blank" rel="noopener noreferrer">
        <LifeBuoy className="size-4" />
        Contact {program.title.replace(/ partners$/i, "")}
      </a>
    </Button>
  ) : null;
}
export function PortalHelp({ program, me }: { program: Program; me?: Me }) {
  const [query, setQuery] = useState(""),
    [category, setCategory] = useState("all"),
    [selected, setSelected] = useState<Article | null>(null);
  const articles = useMemo(() => {
    const standard: Article[] = [
      {
        id: "proposals",
        title: "Review a personal partnership proposal",
        category: "getting_started",
        body: "A personal proposal belongs to the recipient invited by the brand. Preview the brief, then request the secure email sign-in link to respond. Forwarding a preview does not transfer the agreement.\n\nYou can accept or decline a priced offer. For a quote request, submit your price first; the brand then prepares a final offer. Discuss changes using the proposal's email link. Accepted terms are locked and can be downloaded.\n\nAfter acceptance, add payout details in Settings and any invoice instructions on the proposal. Submit the published URL for each deliverable. The brand verifies the work and approves payment milestones manually. Waiting periods start at brand verification; reaching a date never automatically sends payment. Keep placements live for their agreed duration.",
      },

      {
        id: "start",
        title: "Your first steps",
        category: "getting_started",
        body: "This is your private partner workspace. Start with an opportunity or your promotion link. Add a website, social profile or community to receive more relevant opportunities. You do not need an audience for every task.\n\nSome tasks can be started immediately; others need an application and a priced offer. Wait for an accepted price before doing commissioned work. Voluntary tasks have no payment.",
      },
      {
        id: "links",
        title: "Links, landing pages and campaign labels",
        category: "promotion",
        body: "Choose Promotion links to copy your tracking link. Select a brand landing page, or a page on the same website, and add a campaign label such as instagram_bio or newsletter. Campaign labels (also called sub IDs) distinguish placements in reporting.\n\nA coupon gives a customer a discount; your reward follows the program's reward rules. A coupon and a tracking link can be used together. Ask the brand which takes priority when they identify different partners.\n\nA friendly link on the brand's main website requires the brand to create a temporary redirect to your tracking link. Creating a tracking link does not create a page on their website.",
      },
      {
        id: "tracking",
        title: "Clicks, conversions and rewards",
        category: "tracking",
        body: "A click is a visit through your tracking link. A conversion is a signup, purchase or other event recorded by the brand. A click alone does not earn a commission. Eligibility, attribution windows and the brand's reward rules decide which conversions qualify.\n\nRefunds, cancellations or ineligible purchases may reverse rewards. Personal customer information is masked. Your campaign label helps you distinguish traffic sources; never put private customer information into a link.",
      },
      {
        id: "tasks",
        title: "Submitting work and verifying a website",
        category: "getting_started",
        body: "Open a task to review the brief, reward and evidence requirements. Submit the published work URL or the requested completion note. The brand may review it manually or verify a required link automatically. Submitted work is not approved payment.\n\nTo unlock a private website proposal, add the exact website hostname and follow its ownership verification instructions. Reach estimates and follower counts do not prove ownership. A human review hold can delay approval.",
      },
      {
        id: "earnings",
        title: "Understand your balance and payments",
        category: "payouts",
        body: `On hold rewards are awaiting release or approval. Available cash is approved cash not already allocated to a payout. Paid rewards have been recorded as paid by the brand. Product benefits and credits are separate from cash.\n\n${me ? `Minimum payout: ${money(me.min_payout_cents, me.currency)}. Program reward hold: ${me.hold_days} days.\n\n` : ""}${program.payout_terms || "The brand handles payment. Contact the brand for its payment schedule, supported methods and any applicable fees."}\n\nKeep your payout details current in Settings. This portal does not itself transfer funds.`,
      },
      {
        id: "postbacks",
        title: "Advanced tracking and postbacks",
        category: "integrations",
        body: "Campaign labels use the sub parameter on tracking links. If you operate your own tracker, set a postback URL in your account's advanced settings when available. Use supported placeholders such as {sub_id}, {conversion_id}, {commission} and {status}.\n\nPostbacks report attributed conversions and refunds. They do not replace the brand's checkout or server-side conversion integration. Never share your session or a brand's administrative API key.",
      },
    ];
    return [
      ...(program.help_articles || []),
      ...standard.map((a) => ({ ...a, id: "guide-" + a.id })),
    ];
  }, [program, me]);
  const shown = articles.filter(
    (a) =>
      (category === "all" || category === a.category) &&
      `${a.title} ${a.body}`.toLowerCase().includes(query.toLowerCase()),
  );
  return (
    <section className="space-y-7">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <p className="text-xs text-muted-foreground">
            A little guidance, whenever you need it
          </p>
          <h1 className="mt-2 text-3xl font-semibold tracking-tight">
            Partner help
          </h1>
          <p className="mt-3 text-sm text-muted-foreground">
            Get started, understand your rewards, and find the right support.
          </p>
        </div>
        <SupportLink program={program} />
      </div>
      {selected ? (
        <Card className="gap-5 p-6 sm:p-8 shadow-none">
          <Button
            variant="ghost"
            className="w-fit"
            onClick={() => setSelected(null)}
          >
            <ArrowLeft className="size-4" />
            All articles
          </Button>
          <p className="text-xs text-muted-foreground">
            {categories[selected.category]}
          </p>
          <h2 className="text-2xl font-semibold">{selected.title}</h2>
          <div className="max-w-3xl whitespace-pre-wrap text-sm leading-7 text-muted-foreground">
            {selected.body}
          </div>
          <div className="border-t pt-5">
            <SupportLink program={program} />
          </div>
        </Card>
      ) : (
        <>
          <div className="relative max-w-xl">
            <Search className="absolute left-3 top-3 size-4 text-muted-foreground" />
            <Input
              aria-label="Search help"
              className="pl-10"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search links, payments, getting started…"
            />
          </div>
          <div className="flex flex-wrap gap-2">
            {[["all", "All topics"], ...Object.entries(categories)].map(
              ([id, label]) => (
                <Button
                  key={id}
                  variant={category === id ? "secondary" : "ghost"}
                  size="sm"
                  onClick={() => setCategory(id)}
                >
                  {label}
                </Button>
              ),
            )}
          </div>
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {shown.map((a) => (
              <button
                key={a.id}
                className="rounded-xl border bg-card p-6 text-left transition hover:border-foreground/30 focus-visible:outline-2 focus-visible:outline-ring"
                onClick={() => {
                  setSelected(a);
                  track("ui.partner_help_opened", { category: a.category });
                }}
              >
                <BookOpen className="mb-5 size-5 text-muted-foreground" />
                <p className="text-[11px] text-muted-foreground">
                  {categories[a.category]}
                </p>
                <h2 className="mt-2 text-base font-semibold">{a.title}</h2>
                <p className="mt-3 line-clamp-2 text-xs leading-6 text-muted-foreground">
                  {a.body}
                </p>
                <span className="mt-5 inline-flex items-center gap-1 text-xs font-medium">
                  Read article
                  <ArrowUpRight className="size-3" />
                </span>
              </button>
            ))}
          </div>
          {!shown.length && (
            <p className="py-10 text-center text-sm text-muted-foreground">
              No matching articles. Try another topic or contact the brand.
            </p>
          )}
        </>
      )}
      {program.terms_url && (
        <a
          className="inline-flex items-center gap-2 text-xs underline"
          href={program.terms_url}
          target="_blank"
          rel="noopener noreferrer"
        >
          Read program terms
          <ArrowUpRight className="size-3" />
        </a>
      )}
    </section>
  );
}
