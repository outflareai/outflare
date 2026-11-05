import { useCallback, useEffect, useState, type FormEvent } from "react";
import { pickedBrandId } from "@/lib/brand";
import type { PartnerProgram } from "@outflare/sdk";
import { Plus, FileText, ArrowLeft, Copy, CheckCircle2 } from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import { ProposalDocument } from "@/portal/ProposalDocument";
import {
  blankProposal,
  milestoneDue,
  proposalStage,
  type Proposal,
  type ProposalConfig,
} from "@/portal/proposals";
import { money } from "./Partners";
import { track } from "@/lib/analytics";
export function PartnerProposals({
  ws,
  program,
  canManage,
}: {
  ws: string;
  program: PartnerProgram;
  canManage: boolean;
}) {
  const [rows, setRows] = useState<Proposal[]>([]),
    [selected, setSelected] = useState(""),
    [composer, setComposer] = useState(false),
    [editing, setEditing] = useState<Proposal | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [invite, setInvite] = useState(""),
    [filter, setFilter] = useState("all");
  const load = useCallback(async () => {
    const { data, error } = await api.GET(
      "/v1/brands/{brand}/partner-programs/{id}/proposals",
      { params: { path: { brand: pickedBrandId(), id: program.id } } },
    );
    if (error) throw error;
    setRows((data?.data || []) as unknown as Proposal[]);
  }, [ws, program.id]);
  useEffect(() => {
    void load().catch((e) => setError(errorMessage(e)));
  }, [load]);
  const p = rows.find((x) => x.id === selected);
  async function act(
    action: "invite" | "verify" | "approve" | "complete",
    extra: { deliverable_id?: string; milestone_id?: string } = {},
  ) {
    if (!p) return;
    setBusy(true);
    setError("");
    const { data, error } = await api.POST(
      "/v1/brands/{brand}/partner-programs/{id}/proposals/{proposal}",
      {
        params: { path: { brand: pickedBrandId(), id: program.id, proposal: p.id } },
        body: { action, version: p.version, ...extra },
      },
    );
    setBusy(false);
    if (error) {
      setError(errorMessage(error));
      return;
    }
    if (data?.invitation_url) setInvite(data.invitation_url);
    await load();
  }
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h2 className="text-xl font-semibold">Partnership proposals</h2>
          <p className="mt-2 text-sm text-muted-foreground">
            One recipient. A clear agreement. Every payment approved by your
            team.
          </p>
        </div>
        {canManage && (
          <Button
            onClick={() => {
              setEditing(null);
              setComposer(true);
              track("ui.partner_proposal_composer_opened");
            }}
          >
            <Plus className="size-4" />
            New proposal
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
          <div className="flex flex-wrap gap-3">
            <Button
              variant="ghost"
              onClick={() => {
                setSelected("");
                setInvite("");
              }}
            >
              <ArrowLeft className="size-4" />
              All proposals
            </Button>
            {canManage && !["accepted", "completed"].includes(p.status) && (
              <Button
                variant="outline"
                onClick={() => {
                  setEditing(p);
                  setComposer(true);
                }}
              >
                {p.status === "quote_received"
                  ? "Prepare priced offer"
                  : "Edit proposal"}
              </Button>
            )}
            {canManage &&
              ["draft", "offered", "quote_requested"].includes(p.status) && (
                <Button disabled={busy} onClick={() => act("invite")}>
                  Create invitation link
                </Button>
              )}
            {canManage && p.status === "accepted" && (
              <Button
                variant="outline"
                disabled={busy}
                onClick={() => act("complete")}
              >
                Mark agreement complete
              </Button>
            )}
          </div>
          {invite && (
            <Card className="gap-3 border-emerald-600/20 p-5 shadow-none">
              <h3 className="text-sm font-semibold">Ready for your outreach</h3>
              <p className="text-xs leading-6 text-muted-foreground">
                Copy this private preview link into your email to {p.recipient}.
                They verify their email before responding. Creating another link
                replaces this one. No outreach has been sent.
              </p>
              <div className="flex min-w-0 gap-2">
                <Input
                  aria-label="Proposal invitation link"
                  readOnly
                  value={invite}
                />
                <Button
                  variant="outline"
                  aria-label="Copy invitation link"
                  onClick={() => {
                    void navigator.clipboard
                      .writeText(invite)
                      .then(() =>
                        track("ui.copied", {
                          kind: "partner_proposal_invitation",
                        }),
                      )
                      .catch((e) => setError(errorMessage(e)));
                  }}
                >
                  <Copy className="size-4" />
                </Button>
              </div>
            </Card>
          )}
          {p.status === "quote_received" && (
            <Card className="gap-3 p-5 shadow-none">
              <h3 className="text-sm font-semibold">
                Quote received:{" "}
                {money(p.progress.quoted_cents, program.currency)}
              </h3>
              <p className="whitespace-pre-wrap text-sm text-muted-foreground">
                {p.progress.quote_note}
              </p>
              <p className="text-xs text-muted-foreground">
                Discuss changes by email, then prepare and invite them to the
                final priced offer.
              </p>
            </Card>
          )}
          <Card className="gap-6 p-6 shadow-none sm:p-8">
            <p className="text-xs text-muted-foreground">
              Prepared for {p.recipient}
            </p>
            <ProposalDocument
              config={p.config}
              proposal={p}
              currency={program.currency}
            />
          </Card>
          {p.status === "accepted" && (
            <>
              <section className="space-y-3">
                <h3 className="text-base font-semibold">Publication review</h3>
                {p.config.deliverables.map((d) => {
                  const progress = p.progress.deliverables?.[d.id];
                  return (
                    <Card key={d.id} className="gap-3 p-5 shadow-none">
                      <div className="flex flex-wrap items-center justify-between gap-3">
                        <h4 className="text-sm font-semibold">{d.title}</h4>
                        {progress?.verified_at ? (
                          <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
                            <CheckCircle2 className="size-4" />
                            Verified{" "}
                            {new Date(
                              progress.verified_at,
                            ).toLocaleDateString()}
                          </span>
                        ) : (
                          <Button
                            variant="outline"
                            disabled={
                              !canManage || busy || !progress?.submitted_at
                            }
                            onClick={() =>
                              act("verify", { deliverable_id: d.id })
                            }
                          >
                            Verify published work
                          </Button>
                        )}
                      </div>
                      {progress?.url ? (
                        <a
                          className="break-all text-sm underline"
                          href={progress.url}
                          target="_blank"
                          rel="noopener noreferrer"
                        >
                          {progress.url}
                        </a>
                      ) : (
                        <p className="text-xs text-muted-foreground">
                          Awaiting the partner's submission.
                        </p>
                      )}
                      {progress?.note && (
                        <p className="whitespace-pre-wrap text-xs text-muted-foreground">
                          {progress.note}
                        </p>
                      )}
                    </Card>
                  );
                })}
              </section>
              <section className="space-y-3">
                <h3 className="text-base font-semibold">
                  Manual milestone approval
                </h3>
                <p className="text-xs leading-6 text-muted-foreground">
                  A date never releases payment automatically. Approve only when
                  the agreed work and live period are satisfied. Cash joins the
                  existing payout ledger; benefits use the program's fulfillment
                  workflow.
                </p>
                {p.config.milestones.map((m) => {
                  const approved = p.progress.milestones?.[m.id]?.approved_at,
                    due = milestoneDue(p, m);
                  return (
                    <Card
                      key={m.id}
                      className="flex-row flex-wrap items-center justify-between gap-4 p-5 shadow-none"
                    >
                      <div>
                        <h4 className="text-sm font-medium">{m.title}</h4>
                        <p className="mt-2 text-xs text-muted-foreground">
                          {money(m.amount_cents, program.currency)}
                          {m.benefit ? ` + ${m.benefit}` : ""} ·{" "}
                          {approved
                            ? "Approved"
                            : due
                              ? `Eligible ${due.toLocaleString()}`
                              : "Verify publication first"}
                        </p>
                      </div>
                      <Button
                        disabled={
                          !canManage ||
                          busy ||
                          !!approved ||
                          !due ||
                          due > new Date()
                        }
                        onClick={() => act("approve", { milestone_id: m.id })}
                      >
                        {approved ? "Approved" : "Approve milestone"}
                      </Button>
                    </Card>
                  );
                })}
              </section>
              <Card className="gap-3 p-5 shadow-none">
                <h3 className="text-sm font-semibold">
                  Invoice / receipt instructions
                </h3>
                <p className="whitespace-pre-wrap text-sm text-muted-foreground">
                  {p.progress.invoice_note ||
                    "No instructions provided yet. The partner manages their payout method in the portal."}
                </p>
              </Card>
            </>
          )}
        </>
      ) : (
        <>
          <div className="flex flex-wrap gap-2">
            {[
              "all",
              "draft",
              "quote_requested",
              "quote_received",
              "offered",
              "accepted",
              "completed",
              "declined",
            ].map((s) => (
              <Button
                key={s}
                variant={filter === s ? "secondary" : "ghost"}
                size="sm"
                onClick={() => setFilter(s)}
              >
                {s.replaceAll("_", " ")}
              </Button>
            ))}
          </div>
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {rows
              .filter((x) => filter === "all" || x.status === filter)
              .map((x) => (
                <button
                  key={x.id}
                  className="rounded-xl border bg-card p-6 text-left hover:border-foreground/30"
                  onClick={() => {
                    setSelected(x.id);
                    setInvite("");
                  }}
                >
                  <FileText className="mb-5 size-5 text-muted-foreground" />
                  <p className="text-xs text-muted-foreground">
                    {proposalStage(x)} · {x.recipient}
                  </p>
                  <h3 className="mt-2 text-base font-semibold">
                    {x.config.title}
                  </h3>
                  <p className="mt-3 line-clamp-2 text-xs leading-6 text-muted-foreground">
                    {x.config.brief}
                  </p>
                  <p className="mt-4 text-xs font-medium">
                    {x.config.request_quote
                      ? "Quote requested"
                      : money(
                          x.config.milestones.reduce(
                            (n, m) => n + m.amount_cents,
                            0,
                          ),
                          program.currency,
                        )}{" "}
                    · {x.config.deliverables.length} deliverables
                  </p>
                </button>
              ))}
          </div>
          {rows.length === 0 && (
            <Card className="gap-3 p-10 text-center shadow-none">
              <h3 className="text-lg font-semibold">
                Start a conversation with a clear offer
              </h3>
              <p className="text-sm text-muted-foreground">
                Draft a cross-platform promotion, request a quote, and agree on
                publication and payment milestones.
              </p>
            </Card>
          )}
        </>
      )}
      {composer && (
        <ProposalComposer
          ws={ws}
          program={program}
          existing={editing}
          onClose={() => setComposer(false)}
          onSaved={async (id) => {
            await load();
            setSelected(id);
            setInvite("");
            setComposer(false);
          }}
        />
      )}
    </div>
  );
}
function ProposalComposer({
  program,
  existing,
  onClose,
  onSaved,
}: {
  ws: string;
  program: PartnerProgram;
  existing: Proposal | null;
  onClose: () => void;
  onSaved: (id: string) => Promise<void>;
}) {
  const [config, setConfig] = useState<ProposalConfig>(() => {
      const c = existing ? structuredClone(existing.config) : blankProposal();
      if (!existing) c.reply_to = program.portal.support_email || "";
      if (existing?.status === "quote_received") {
        c.request_quote = false;
        if (c.milestones.length === 1)
          c.milestones[0].amount_cents = existing.progress.quoted_cents;
      }
      return c;
    }),
    [email, setEmail] = useState(""),
    [name, setName] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const set = (v: Partial<ProposalConfig>) =>
    setConfig((c) => ({ ...c, ...v }));
  const text = (
    label: string,
    key: "title" | "reply_to" | "brand_kit_url" | "product_url",
    type = "text",
  ) => (
    <label className="block space-y-2 text-xs font-medium">
      {label}
      <Input
        type={type}
        value={config[key]}
        onChange={(e) => set({ [key]: e.target.value })}
        required={key === "title" || key === "reply_to"}
      />
    </label>
  );
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const path = { brand: pickedBrandId(), id: program.id };
    const res = existing
      ? await api.POST(
          "/v1/brands/{brand}/partner-programs/{id}/proposals/{proposal}",
          {
            params: { path: { ...path, proposal: existing.id } },
            body: { action: "revise", version: existing.version, config },
          },
        )
      : await api.POST("/v1/brands/{brand}/partner-programs/{id}/proposals", {
          params: { path },
          body: { email, name, config },
        });
    setBusy(false);
    if (res.error || !res.data) {
      setError(errorMessage(res.error));
      return;
    }
    await onSaved(res.data.id);
  }
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-3xl">
        <SheetHeader>
          <SheetTitle>
            {existing ? "Revise proposal" : "A proposal for one partner"}
          </SheetTitle>
          <SheetDescription>
            Save a draft, review it, then generate its private invitation link.
            Changes are discussed by email.
          </SheetDescription>
        </SheetHeader>
        <form onSubmit={save} className="space-y-7 px-5 pb-8">
          {!existing && (
            <div className="grid gap-4 sm:grid-cols-2">
              <label className="space-y-2 text-xs font-medium">
                Recipient email
                <Input
                  type="email"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                />
              </label>
              <label className="space-y-2 text-xs font-medium">
                Recipient name (optional)
                <Input value={name} onChange={(e) => setName(e.target.value)} />
              </label>
            </div>
          )}
          <Card className="gap-4 p-4 shadow-none">
            <div>
              <h3 className="text-sm font-semibold">Invitation teaser</h3>
              <p className="mt-2 text-xs leading-6 text-muted-foreground">
                Partners see this website or channel before signing in. The
                brief, price and agreement stay private.
              </p>
            </div>
            {(
              [
                "partner_site_name",
                "partner_site_url",
                "partner_site_icon_url",
              ] as const
            ).map((k) => (
              <label key={k} className="space-y-2 text-xs">
                {
                  {
                    partner_site_name: "Website or channel name (optional)",
                    partner_site_url: "Website or channel URL (optional)",
                    partner_site_icon_url: "Custom favicon URL (optional)",
                  }[k]
                }
                <Input
                  type={k === "partner_site_name" ? "text" : "url"}
                  maxLength={k === "partner_site_name" ? 160 : 2000}
                  placeholder={
                    k === "partner_site_name"
                      ? "Fieldnotes"
                      : k === "partner_site_url"
                        ? "https://fieldnotes.example"
                        : "https://…"
                  }
                  value={config[k] || ""}
                  onChange={(e) => set({ [k]: e.target.value })}
                />
              </label>
            ))}
          </Card>
          {text("Proposal title", "title")}
          <label className="block space-y-2 text-xs font-medium">
            The brief
            <Textarea
              required
              rows={4}
              maxLength={10000}
              placeholder="What are we making, and why is this a good fit for their audience?"
              value={config.brief}
              onChange={(e) => set({ brief: e.target.value })}
            />
          </label>
          {text("Reply-to email for changes", "reply_to", "email")}
          <div className="grid gap-4 sm:grid-cols-2">
            {text("Brand kit (optional)", "brand_kit_url", "url")}
            {text("Product or pricing page (optional)", "product_url", "url")}
          </div>
          <section className="space-y-4">
            <h3 className="text-sm font-semibold">Deliverables</h3>
            {config.deliverables.map((d, i) => (
              <Card key={d.id} className="gap-4 p-4 shadow-none">
                <div className="flex items-center justify-between">
                  <h4 className="text-xs font-semibold">Deliverable {i + 1}</h4>
                  {config.deliverables.length > 1 && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() =>
                        set({
                          deliverables: config.deliverables.filter(
                            (x) => x.id !== d.id,
                          ),
                          milestones: config.milestones.filter(
                            (m) => m.deliverable_id !== d.id,
                          ),
                        })
                      }
                    >
                      Remove
                    </Button>
                  )}
                </div>
                {(["title", "channel", "format", "deadline"] as const).map(
                  (k) => (
                    <label key={k} className="block space-y-2 text-xs">
                      {
                        {
                          title: "What will they publish?",
                          channel: "Channel or platform",
                          format: "Format / placement",
                          deadline: "Deadline or timing",
                        }[k]
                      }
                      <Input
                        required={k === "title"}
                        value={d[k]}
                        placeholder={
                          k === "channel"
                            ? "Any website, social platform, newsletter, event…"
                            : ""
                        }
                        onChange={(e) =>
                          set({
                            deliverables: config.deliverables.map((x) =>
                              x.id === d.id ? { ...x, [k]: e.target.value } : x,
                            ),
                          })
                        }
                      />
                    </label>
                  ),
                )}
                <label className="block space-y-2 text-xs">
                  Requirements & guidance
                  <Textarea
                    rows={3}
                    maxLength={10000}
                    value={d.requirements}
                    onChange={(e) =>
                      set({
                        deliverables: config.deliverables.map((x) =>
                          x.id === d.id
                            ? { ...x, requirements: e.target.value }
                            : x,
                        ),
                      })
                    }
                  />
                </label>
                <label className="block space-y-2 text-xs">
                  Minimum live duration (days; 0 = unspecified)
                  <Input
                    type="number"
                    min={0}
                    max={3650}
                    value={d.live_days}
                    onChange={(e) =>
                      set({
                        deliverables: config.deliverables.map((x) =>
                          x.id === d.id
                            ? { ...x, live_days: Number(e.target.value) }
                            : x,
                        ),
                      })
                    }
                  />
                </label>
                <details className="text-xs">
                  <summary className="cursor-pointer font-medium">
                    Placement and link requirements (optional)
                  </summary>
                  <div className="mt-4 space-y-4">
                    {(
                      [
                        "placement_url",
                        "link_url",
                        "link_text",
                        "link_attribute",
                      ] as const
                    ).map((k) => (
                      <label className="block space-y-2" key={k}>
                        {
                          {
                            placement_url: "Placement website or channel URL",
                            link_url: "Required destination link",
                            link_text: "Anchor or link text",
                            link_attribute: "Link attributes / disclosure",
                          }[k]
                        }
                        <Input
                          type={k.endsWith("url") ? "url" : "text"}
                          value={d[k]}
                          onChange={(e) =>
                            set({
                              deliverables: config.deliverables.map((x) =>
                                x.id === d.id
                                  ? { ...x, [k]: e.target.value }
                                  : x,
                              ),
                            })
                          }
                        />
                      </label>
                    ))}
                    <p className="leading-6 text-muted-foreground">
                      Paid web links generally need sponsorship disclosure and
                      rel="sponsored". Specify requirements that fit the
                      platform.
                    </p>
                  </div>
                </details>
              </Card>
            ))}
            <Button
              type="button"
              variant="outline"
              disabled={config.deliverables.length >= 30}
              onClick={() => {
                const d = blankProposal().deliverables[0];
                set({
                  deliverables: [
                    ...config.deliverables,
                    {
                      ...d,
                      id:
                        "d_" +
                        crypto.randomUUID().replaceAll("-", "").slice(0, 16),
                    },
                  ],
                });
              }}
            >
              <Plus className="size-4" />
              Add deliverable
            </Button>
          </section>
          <section className="space-y-4">
            <h3 className="text-sm font-semibold">Compensation & milestones</h3>
            <label className="flex items-center gap-3 text-sm">
              <input
                type="checkbox"
                checked={config.request_quote}
                onChange={(e) => set({ request_quote: e.target.checked })}
              />
              Ask for a quote before making a priced offer
            </label>
            <p className="text-xs leading-6 text-muted-foreground">
              Mix cash, product benefits and commission. No cash or benefits
              means an unpaid collaboration. Every milestone requires manual
              approval.
            </p>
            {config.milestones.map((m, i) => (
              <Card className="gap-4 p-4 shadow-none" key={m.id}>
                <div className="flex items-center justify-between">
                  <h4 className="text-xs font-semibold">Milestone {i + 1}</h4>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() =>
                      set({
                        milestones: config.milestones.filter(
                          (x) => x.id !== m.id,
                        ),
                      })
                    }
                  >
                    Remove
                  </Button>
                </div>
                {(["title", "benefit"] as const).map((k) => (
                  <label className="block space-y-2 text-xs" key={k}>
                    {k === "title"
                      ? "Milestone name"
                      : "Product or credit benefit (optional)"}
                    <Input
                      value={m[k]}
                      required={k === "title"}
                      onChange={(e) =>
                        set({
                          milestones: config.milestones.map((x) =>
                            x.id === m.id ? { ...x, [k]: e.target.value } : x,
                          ),
                        })
                      }
                    />
                  </label>
                ))}
                <label className="block space-y-2 text-xs">
                  Linked deliverable
                  <select
                    className="h-10 w-full rounded-md border bg-background px-3"
                    value={m.deliverable_id}
                    onChange={(e) =>
                      set({
                        milestones: config.milestones.map((x) =>
                          x.id === m.id
                            ? { ...x, deliverable_id: e.target.value }
                            : x,
                        ),
                      })
                    }
                  >
                    {config.deliverables.map((d) => (
                      <option key={d.id} value={d.id}>
                        {d.title || "Untitled deliverable"}
                      </option>
                    ))}
                  </select>
                </label>
                <div className="grid gap-4 sm:grid-cols-2">
                  <label className="space-y-2 text-xs">
                    Cash amount ({program.currency})
                    <Input
                      type="number"
                      min={0}
                      step="0.01"
                      value={m.amount_cents / 100}
                      onChange={(e) =>
                        set({
                          milestones: config.milestones.map((x) =>
                            x.id === m.id
                              ? {
                                  ...x,
                                  amount_cents: Math.round(
                                    Number(e.target.value) * 100,
                                  ),
                                }
                              : x,
                          ),
                        })
                      }
                    />
                  </label>
                  <label className="space-y-2 text-xs">
                    Wait after brand verification (days)
                    <Input
                      type="number"
                      min={0}
                      max={3650}
                      value={m.wait_days}
                      onChange={(e) =>
                        set({
                          milestones: config.milestones.map((x) =>
                            x.id === m.id
                              ? { ...x, wait_days: Number(e.target.value) }
                              : x,
                          ),
                        })
                      }
                    />
                  </label>
                </div>
              </Card>
            ))}
            <Button
              variant="outline"
              type="button"
              disabled={config.milestones.length >= 30}
              onClick={() =>
                set({
                  milestones: [
                    ...config.milestones,
                    {
                      id:
                        "m_" +
                        crypto.randomUUID().replaceAll("-", "").slice(0, 16),
                      title: "",
                      deliverable_id: config.deliverables[0].id,
                      amount_cents: 0,
                      benefit: "",
                      wait_days: 0,
                    },
                  ],
                })
              }
            >
              <Plus className="size-4" />
              Add milestone
            </Button>
            <label className="block space-y-2 text-xs">
              Commission arrangement (optional)
              <Textarea
                value={config.commission_terms}
                maxLength={2000}
                placeholder="Describe the program's commission rules. Configure the actual rates in Flows."
                onChange={(e) => set({ commission_terms: e.target.value })}
              />
            </label>
          </section>
          <label className="block space-y-2 text-xs font-medium">
            Additional terms
            <Textarea
              rows={5}
              maxLength={10000}
              value={config.terms}
              placeholder="Payment timing, content usage rights, cancellation and any other agreed terms."
              onChange={(e) => set({ terms: e.target.value })}
            />
          </label>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <Button disabled={busy}>
            {busy ? "Saving…" : "Save proposal draft"}
          </Button>
        </form>
      </SheetContent>
    </Sheet>
  );
}
