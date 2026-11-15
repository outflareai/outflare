// The account summary on a person's or company's page: where they stand,
// what needs attention, and everything the brand knows about them, from
// one call. Each card shows only when it has something to say, so a brand
// with no payments or products sees neither card.
import { useEffect, useState, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import {
  AlertTriangle,
  ArrowDownRight,
  ArrowUpRight,
  Check,
  CircleCheck,
  Minus,
} from "lucide-react";
import { api, errorMessage } from "@/api";
import { Badge, ErrorText, Status, toast } from "@/ui";
import { ago, plural, stamp } from "@/format";
import { pickedBrandId } from "@/lib/brand";
import type { CatalogInfo } from "@/lib/records";
import { InlineField } from "./RecordPreview";
import { FlagButton } from "../Signals";
import { RecordScores, bandText, bandTone } from "@/components/record-scores";
import { track } from "@/lib/analytics";

type Rec = Schemas["Record"];
type Summary = Schemas["RecordSummary"];
type Attention = Summary["attention"][number];
type Group = Schemas["RecordGroup"];

const planKeys = ["goal", "next_step", "hesitations", "limits"];

function money(minor: number, currency?: string | null): string {
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: currency || "USD",
      maximumFractionDigits: minor % 100 === 0 ? 0 : 2,
    }).format(minor / 100);
  } catch {
    return (minor / 100).toLocaleString();
  }
}

// A currency field's value is { amount_minor, currency }.
function fieldMoney(v: unknown): string {
  const c = v as { amount_minor?: number; currency?: string } | null;
  return c && typeof c.amount_minor === "number"
    ? money(c.amount_minor, c.currency)
    : "";
}

// A day still to come: today, tomorrow, or the date.
function due(iso: string): string {
  const days = Math.round((new Date(iso).setHours(0, 0, 0, 0) - new Date().setHours(0, 0, 0, 0)) / 86400000);
  if (days <= 0) return "today";
  if (days === 1) return "tomorrow";
  if (days < 7) return `in ${days} days`;
  return new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

const words = (s: string) => s.replaceAll("_", " ");
const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s);

// Up, down or flat against the window before, as a small labelled arrow.
function Trend({ now, before }: { now: number; before: number }) {
  if (before === 0 && now === 0) return null;
  const pct = before === 0 ? null : Math.round(((now - before) / before) * 100);
  const up = now > before;
  const flat = now === before || (pct !== null && Math.abs(pct) < 5);
  const Icon = flat ? Minus : up ? ArrowUpRight : ArrowDownRight;
  const color = flat
    ? "text-muted-foreground"
    : up
      ? "text-green-600 dark:text-green-400"
      : "text-red-600 dark:text-red-400";
  return (
    <span
      className={`inline-flex items-center gap-0.5 text-xs ${color}`}
      title="Against the same length of time just before"
    >
      <Icon className="size-3.5" aria-hidden="true" />
      {flat ? "flat" : pct === null ? "new" : `${up ? "up" : "down"} ${Math.abs(pct)}%`}
    </span>
  );
}

function Panel({
  title,
  action,
  children,
}: {
  title: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="rounded-xl border">
      <header className="flex items-center justify-between gap-2 border-b px-3 py-2">
        <h3 className="text-xs font-semibold text-muted-foreground">
          {title.toUpperCase()}
        </h3>
        {action}
      </header>
      <div className="p-3 text-sm">{children}</div>
    </section>
  );
}

function Row({
  children,
  onClick,
}: {
  children: ReactNode;
  onClick?: () => void;
}) {
  const cls = "flex w-full items-center gap-3 py-1.5 text-left text-sm";
  return onClick ? (
    <button type="button" className={`${cls} rounded hover:bg-muted/40`} onClick={onClick}>
      {children}
    </button>
  ) : (
    <div className={cls}>{children}</div>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] text-muted-foreground">{label}</dt>
      <dd className="truncate text-sm font-medium">{children}</dd>
    </div>
  );
}

// What one attention item means, in a plain sentence, and where it leads.
function attentionLine(a: Attention): { text: string; section?: string; tone: "red" | "amber" | "blue" } {
  const n = a.count;
  switch (a.kind) {
    case "signal": {
      const late = !!a.at && new Date(a.at).getTime() < Date.now();
      return { text: `${plural(n, "open signal")} to act on${a.at ? `, the first ${late ? `was due ${ago(a.at)}` : `due ${due(a.at)}`}` : ""}`, tone: late ? "red" : "amber" };
    }
    case "reply_overdue":
      return { text: `${plural(n, "support conversation")} past ${n === 1 ? "its" : "their"} reply or resolution time`, tone: "red" };
    case "conversation_waiting":
      return { text: `${plural(n, "support conversation")} waiting on your reply${a.at ? `, since ${ago(a.at)}` : ""}`, tone: "amber" };
    case "task_overdue":
      return { text: `${plural(n, "task")} overdue${a.at ? `, the oldest due ${ago(a.at)}` : ""}`, section: "tasks", tone: "amber" };
    case "payment_failed":
      return { text: `${plural(n, "payment")} failed in the last 30 days`, tone: "red" };
    case "billing_past_due":
      return { text: "Billing is past due", tone: "red" };
    case "renewal_due": {
      const late = !!a.at && new Date(a.at).getTime() < Date.now();
      return { text: `${plural(n, "renewal")} ${late ? "passed" : "coming up"}${a.at ? `, the first ${late ? ago(a.at) : due(a.at)}` : ""}`, tone: late ? "red" : "amber" };
    }
    case "at_risk":
      return { text: "At risk: a product they pay for has gone unused", tone: "amber" };
    case "came_back":
      return { text: `Back on your website ${a.at ? ago(a.at) : "this week"}, after a quiet spell. A good moment to reach out`, tone: "blue" };
    case "no_owner":
      return { text: "Nobody owns this account yet", tone: "amber" };
  }
  return { text: words(a.kind), tone: "amber" };
}

const toneText = {
  red: "text-red-700 dark:text-red-400",
  amber: "text-amber-700 dark:text-amber-400",
  blue: "text-blue-700 dark:text-blue-400",
};

const walletKeys = ["wallet_estimate", "wallet_confidence", "wallet_source", "suppliers"];

// A company's share of wallet and the group it belongs to. Both cards
// stay away until they have something to say: the wallet card needs the
// kit's fields, the group card a parent or a company underneath.
function GroupPanels({
  ws,
  record,
  cat,
  canManage,
  names,
  onOpen,
  onChanged,
}: {
  ws: string;
  record: Rec;
  cat: CatalogInfo;
  canManage: boolean;
  names: Record<string, string>;
  onOpen: (id: string) => void;
  onChanged: () => void;
}) {
  const [group, setGroup] = useState<Group | null>(null);
  const brand = record.brand_id ?? pickedBrandId();
  useEffect(() => {
    let live = true;
    void api
      .GET("/v1/brands/{brand}/records/{record}/group", { params: { path: { brand, record: record.id } } })
      .then(({ data }) => live && setGroup(data ?? null))
      .catch(() => live && setGroup(null));
    return () => {
      live = false;
    };
  }, [brand, record.id, record.version]);
  if (!group) return null;

  const fields = cat.fields(record.type);
  const walletFields = walletKeys
    .map((k) => fields.find((f) => f.key === k))
    .filter((f): f is Schemas["Field"] => !!f);
  const editable = canManage && !record.archived && !record.merged_into;
  const w = group.wallet;
  const t = group.totals;
  const share = w?.share ?? null;
  const pct = share === null ? null : Math.round(share * 100);
  const room = w && share !== null ? Math.max(0, w.estimate_minor - w.paid_12m_minor) : 0;
  const inGroup = group.companies.length > 1;
  const ccy = group.currency;

  return (
    <>
      {walletFields.length > 0 && (w || editable) && (
        <Panel title="Share of wallet" action={pct !== null && <Badge tone={pct >= 60 ? "green" : pct >= 25 ? "amber" : "blue"}>{pct}% yours</Badge>}>
          {w ? (
            <>
              {pct !== null && (
                <div
                  className="mb-2 h-2 overflow-hidden rounded-full bg-muted"
                  role="img"
                  aria-label={`${pct}% of their yearly spend is with you`}
                >
                  <div className="h-full rounded-full bg-primary" style={{ width: `${Math.min(100, Math.max(2, pct))}%` }} />
                </div>
              )}
              <dl className="mb-2 grid grid-cols-3 gap-3">
                <Fact label="Paid you, 12 months">{money(w.paid_12m_minor, ccy)}</Fact>
                <Fact label="They spend a year">{money(w.estimate_minor, w.currency)}</Fact>
                <Fact label="Room left">
                  {share === null ? <span className="text-muted-foreground">Other currency</span> : money(room, ccy)}
                </Fact>
              </dl>
            </>
          ) : (
            <p className="mb-2 text-xs text-muted-foreground">
              Add what they spend a year on what you sell, with every supplier, to see how much of it is yours.
            </p>
          )}
          <div className="divide-y">
            {walletFields.map((f) => (
              <div key={f.key} className="grid grid-cols-[minmax(90px,1fr)_2fr] items-start gap-3 py-1.5">
                <span className="pt-1 text-xs text-muted-foreground" title={f.description}>
                  {f.label}
                </span>
                <InlineField ws={ws} record={record} f={f} cat={cat} names={names} canManage={canManage} onSaved={onChanged} />
              </div>
            ))}
          </div>
        </Panel>
      )}

      {inGroup && (
        <Panel title="Company group" action={<span className="text-xs text-muted-foreground">{plural(t.companies, "company", "companies")}</span>}>
          <dl className="mb-2 grid grid-cols-3 gap-3">
            <Fact label={t.mrr_minor > 0 ? "Monthly revenue" : "Paid, 12 months"}>
              {money(t.mrr_minor > 0 ? t.mrr_minor : t.paid_12m_minor, ccy)}
            </Fact>
            <Fact label="Needs attention">
              {t.open_signals > 0 ? plural(t.open_signals, "signal") : <span className="text-muted-foreground">Nothing</span>}
            </Fact>
            <Fact label="Low health">
              {t.low_health > 0 ? plural(t.low_health, "company", "companies") : <span className="text-muted-foreground">None</span>}
            </Fact>
          </dl>
          <ul className="divide-y border-t">
            {group.companies.map((c) => {
              const here = c.id === record.id;
              return (
                <li key={c.id} style={{ paddingLeft: `${Math.min(c.depth, 4) * 14}px` }}>
                  <Row
                    onClick={
                      here
                        ? undefined
                        : () => {
                            track("ui.account_group_opened", { depth: c.depth });
                            onOpen(c.id);
                          }
                    }
                  >
                    <span className={`min-w-0 flex-1 truncate ${here ? "font-semibold" : "font-medium"}`}>
                      {c.name || "Unnamed company"}
                      {here && <span className="ml-1.5 text-xs font-normal text-muted-foreground">this company</span>}
                    </span>
                    {c.open_signals > 0 && <Badge tone="amber">{c.open_signals}</Badge>}
                    {c.health !== null && c.health_band && (
                      <Status tone={bandTone[c.health_band as keyof typeof bandTone] ?? "grey"}>
                        {c.health} {bandText[c.health_band as keyof typeof bandText] ?? ""}
                      </Status>
                    )}
                    <span className="w-24 shrink-0 text-right text-xs tabular-nums text-muted-foreground">
                      {c.mrr_minor > 0
                        ? `${money(c.mrr_minor, ccy)}/mo`
                        : c.paid_12m_minor > 0
                          ? money(c.paid_12m_minor, ccy)
                          : "No revenue"}
                    </span>
                  </Row>
                </li>
              );
            })}
          </ul>
          {group.truncated && (
            <p className="mt-2 text-xs text-muted-foreground">Showing the first 200 companies of this group.</p>
          )}
        </Panel>
      )}
    </>
  );
}

export function AccountSummary({
  ws,
  record,
  cat,
  canManage,
  myId,
  names = {},
  onOpen,
  onChanged,
  onSection,
  compact = false,
}: {
  ws: string;
  record: Rec;
  cat: CatalogInfo;
  canManage: boolean;
  myId?: string;
  names?: Record<string, string>;
  onOpen: (id: string) => void;
  onChanged: () => void;
  onSection?: (section: string) => void;
  // One column, for the side sheet.
  compact?: boolean;
}) {
  const [sum, setSum] = useState<Summary | null>(null);
  const [error, setError] = useState("");
  const [moving, setMoving] = useState(false);
  const [flagged, setFlagged] = useState(0);
  const [routing, setRouting] = useState(false);
  const brand = record.brand_id ?? pickedBrandId();
  useEffect(() => {
    let live = true;
    void api
      .GET("/v1/brands/{brand}/records/{record}/summary", {
        params: { path: { brand, record: record.id } },
      })
      .then(({ data, error }) => {
        if (!live) return;
        if (data) {
          setSum(data);
          setError("");
        } else setError(errorMessage(error));
      })
      .catch((e) => live && setError(errorMessage(e)));
    return () => {
      live = false;
    };
    // The version changes on every edit, so the summary follows the record.
  }, [brand, record.id, record.version, flagged]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!sum)
    return (
      <div className="space-y-3" aria-busy="true" aria-label="Loading the summary">
        <div className="h-9 animate-pulse rounded-lg bg-muted" />
        <div className="h-24 animate-pulse rounded-xl bg-muted/60" />
      </div>
    );

  const fields = cat.fields(record.type);
  const stageField = fields.find((f) => f.key === "lifecycle_stage");
  const stages = stageField ? cat.options(stageField) : [];
  const typeName = (key: string) =>
    cat.catalog?.types.find((t) => t.key === key)?.name ?? words(key);
  const editable = canManage && !record.archived && !record.merged_into;

  async function setStage(key: string) {
    if (moving || !editable || key === sum?.stage.stage) return;
    setMoving(true);
    try {
      const { data, error } = await api.PATCH("/v1/brands/{brand}/records/{record}", {
        params: { path: { brand, record: record.id } },
        body: { version: record.version, attributes: { lifecycle_stage: key } },
      });
      if (error) return toast(errorMessage(error), "error");
      if (data && "id" in data) {
        if (data.skipped?.length)
          return toast("The stage is set by another source and was kept.", "error");
        toast("Stage updated.");
        onChanged();
      }
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setMoving(false);
    }
  }

  const at = stages.findIndex((s) => s.key === sum.stage.stage);
  const b = sum.billing;
  const mrr = b ? fieldMoney(b.mrr) : "";
  const others = sum.team.filter((t) => t.user_id !== myId);
  const c = sum.contact;
  const lastContact = c.last_at
    ? c.last_direction === "in"
      ? `They wrote ${ago(c.last_at)}`
      : c.last_direction === "out"
        ? `You wrote ${ago(c.last_at)}`
        : `${cap(c.last_channel ?? "contact")} ${ago(c.last_at)}`
    : "No contact yet";
  const planFields = planKeys
    .map((k) => fields.find((f) => f.key === k))
    .filter((f): f is Schemas["Field"] => !!f);
  const hasPlan = Object.keys(sum.plan).length > 0;
  const usage = sum.metrics.filter((m) => typeof m.value === "number");

  return (
    <div className="space-y-4">
      {/* Where they are in the lifecycle. */}
      {stages.length > 0 && (
        <section aria-label="Lifecycle stage">
          <ol className="flex overflow-hidden rounded-lg border text-xs">
            {stages.map((s, i) => {
              const current = i === at;
              const done = at >= 0 && i < at;
              return (
                <li key={s.key} className="min-w-0 flex-1 border-r last:border-r-0">
                  <button
                    type="button"
                    disabled={!editable || moving}
                    aria-current={current ? "step" : undefined}
                    title={editable && !current ? `Move to ${s.label}` : undefined}
                    onClick={() => void setStage(s.key)}
                    className={`flex h-9 w-full items-center justify-center gap-1 truncate px-2 font-medium ${
                      current
                        ? "bg-primary text-primary-foreground"
                        : done
                          ? "bg-muted/60 text-foreground"
                          : "text-muted-foreground"
                    } ${editable && !current ? "hover:bg-muted" : ""}`}
                  >
                    {done && <Check className="size-3 shrink-0" aria-hidden="true" />}
                    <span className="truncate">{s.label}</span>
                  </button>
                </li>
              );
            })}
          </ol>
          <p className="mt-1.5 text-xs text-muted-foreground">
            {at < 0
              ? editable
                ? "No stage yet. Pick one above."
                : "No stage yet."
              : `${stages[at].label}${sum.stage.since ? ` since ${ago(sum.stage.since)}` : ""}${
                  sum.stage.by ? `, set ${sum.stage.by === "manual" ? "by hand" : `by ${words(sum.stage.by)}`}` : ""
                }.`}
            {sum.stage.last_move && (
              <span title={stamp(sum.stage.last_move.at)}>
                {" "}
                Last product move: {words(sum.stage.last_move.move)}
                {sum.stage.last_move.product_key ? ` (${sum.stage.last_move.product_key})` : ""},{" "}
                {ago(sum.stage.last_move.at)}.
              </span>
            )}
          </p>
        </section>
      )}

      {/* What to act on now. */}
      {sum.attention.length > 0 ? (
        <section
          aria-label="Needs attention"
          className="rounded-xl border border-amber-500/40 bg-amber-500/5 p-3"
        >
          <h3 className="mb-1.5 flex items-center gap-1.5 text-xs font-semibold">
            <AlertTriangle className="size-3.5 text-amber-600" aria-hidden="true" />
            NEEDS ATTENTION
          </h3>
          <ul className="space-y-1 text-sm">
            {sum.attention.map((a) => {
              const line = attentionLine(a);
              return (
                <li key={a.kind} className={toneText[line.tone]}>
                  {line.section && onSection ? (
                    <button
                      type="button"
                      className="text-left underline-offset-2 hover:underline"
                      onClick={() => onSection(line.section!)}
                    >
                      {line.text}.
                    </button>
                  ) : (
                    <>{line.text}.</>
                  )}
                </li>
              );
            })}
          </ul>
        </section>
      ) : (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <CircleCheck className="size-3.5 text-green-600" aria-hidden="true" />
          Nothing needs attention right now.
        </p>
      )}
      {canManage && <FlagButton brandId={brand} recordId={record.id} onRaised={() => setFlagged((n) => n + 1)} />}

      {/* How they score, and why. */}
      <RecordScores brand={brand} record={record.id} version={record.version} />

      {/* At a glance. */}
      <dl className={`grid grid-cols-2 gap-3 rounded-xl border p-3 ${compact ? "" : "sm:grid-cols-4"}`}>
        <Fact label="Owner">
          {sum.owner?.name || (
            <span className="inline-flex items-center gap-2">
              <span className="text-muted-foreground">Nobody</span>
              {canManage && !record.archived && !record.merged_into && (
                <button
                  type="button"
                  className="text-xs underline underline-offset-2 disabled:opacity-50"
                  disabled={routing}
                  title="Give them to the teammate your routing rules pick."
                  onClick={async () => {
                    setRouting(true);
                    const { data, error } = await api.POST("/v1/brands/{brand}/records/{record}/routing", { params: { path: { brand, record: record.id } } });
                    setRouting(false);
                    if (error) return toast(errorMessage(error), "error");
                    track("ui.record_routed", { outcome: data.outcome });
                    if (data.outcome === "assigned") {
                      toast(`Routed${data.rule_name ? ` by the rule ${data.rule_name}` : ""}.`);
                      onChanged();
                    } else {
                      toast(data.outcome === "no_member" ? "The rule that matches has nobody left in it. Add people under People, Routing." : "No routing rule matches them. Add one under People, Routing.", "error");
                    }
                  }}
                >
                  Route
                </button>
              )}
            </span>
          )}
        </Fact>
        <Fact label="Last contact">
          <span title={c.last_at ? stamp(c.last_at) : undefined}>{lastContact}</span>
        </Fact>
        <Fact label="Open deals">
          {sum.deals.open + sum.pipelines.length > 0
            ? sum.deals.open + sum.pipelines.length
            : <span className="text-muted-foreground">None</span>}
        </Fact>
        <Fact label={mrr ? "Monthly revenue" : "Paid, last 30 days"}>
          {b ? (
            <span className="inline-flex items-center gap-1.5">
              {mrr || money(b.paid_30d, b.currency)}
              <Trend now={b.paid_30d} before={b.paid_prev_30d} />
            </span>
          ) : (
            <span className="text-muted-foreground">None</span>
          )}
        </Fact>
      </dl>
      {sum.company && (
        <p className="text-xs text-muted-foreground">
          Works at{" "}
          <button type="button" className="font-medium text-foreground underline underline-offset-2" onClick={() => onOpen(sum.company!.id)}>
            {sum.company.name || "Unnamed company"}
          </button>
          .
        </p>
      )}
      {c.stale && (
        <p className="text-xs text-muted-foreground">
          The conversation went quiet more than 60 days ago. Worth picking back up.
        </p>
      )}
      {others.length > 0 && (
        <p className="text-xs text-muted-foreground">
          Warm intro:{" "}
          {others
            .map((t) => `${t.name || "A teammate"} (${plural(t.messages, "email")}, ${ago(t.last_at)})`)
            .join(", ")}{" "}
          {others.length === 1 ? "has" : "have"} emailed them.
        </p>
      )}

      {/* The account plan: always there for someone who can fill it in. */}
      {planFields.length > 0 && (hasPlan || editable) && (
        <Panel title="Account plan">
          {!hasPlan && (
            <p className="mb-2 text-xs text-muted-foreground">
              Write down what you want with them and what is in the way, so anyone
              who opens this knows the next move.
            </p>
          )}
          <div className="divide-y">
            {planFields.map((f) => (
              <div key={f.key} className="grid grid-cols-[minmax(90px,1fr)_3fr] items-start gap-3 py-1.5">
                <span className="pt-1 text-xs text-muted-foreground" title={f.description}>
                  {f.label}
                </span>
                <InlineField ws={ws} record={record} f={f} cat={cat} names={names} canManage={canManage} onSaved={onChanged} />
              </div>
            ))}
          </div>
        </Panel>
      )}

      <div className={`grid gap-4 ${compact ? "" : "md:grid-cols-2"}`}>
        {record.type === "account" && (
          <GroupPanels ws={ws} record={record} cat={cat} canManage={canManage} names={names} onOpen={onOpen} onChanged={onChanged} />
        )}
        {b && (
          <Panel
            title="Billing"
            action={
              typeof b.status === "string" && (
                <Badge tone={b.status === "active" ? "green" : b.status === "past_due" ? "red" : "grey"}>
                  {words(b.status)}
                </Badge>
              )
            }
          >
            <dl className="grid grid-cols-2 gap-3">
              {typeof b.plan === "string" && b.plan && <Fact label="Plan">{b.plan}</Fact>}
              {mrr && <Fact label="Monthly revenue">{mrr}</Fact>}
              <Fact label="Paid, last 30 days">
                <span className="inline-flex items-center gap-1.5">
                  {money(b.paid_30d, b.currency)}
                  <Trend now={b.paid_30d} before={b.paid_prev_30d} />
                </span>
              </Fact>
              {fieldMoney(b.lifetime_revenue) && <Fact label="Lifetime revenue">{fieldMoney(b.lifetime_revenue)}</Fact>}
              {b.last_payment_at && <Fact label="Last payment">{ago(b.last_payment_at)}</Fact>}
              {typeof b.customer_since === "string" && <Fact label="Customer since">{ago(b.customer_since)}</Fact>}
            </dl>
          </Panel>
        )}

        {(sum.products.length > 0 || usage.length > 0) && (
          <Panel title="Products and usage">
            {sum.products.map((p) => (
              <Row key={p.product_id}>
                <span className="min-w-0 flex-1 truncate font-medium">{p.name}</span>
                <Status tone={p.category === "active" ? "green" : p.category === "at_risk" ? "amber" : p.category === "churned" ? "red" : "grey"}>
                  {words(p.stage)}
                </Status>
                <span className="w-24 shrink-0 text-right text-xs text-muted-foreground">
                  {p.last_used_at ? `used ${ago(p.last_used_at)}` : "not used yet"}
                </span>
              </Row>
            ))}
            {usage.length > 0 && sum.products.length > 0 && <hr className="my-2" />}
            {usage.map((m) => (
              <Row key={`${m.key}:${m.product_key ?? ""}`}>
                <span className="min-w-0 flex-1 truncate">
                  {m.name}
                  {m.product_key && <span className="text-muted-foreground"> · {m.product_key}</span>}
                </span>
                <span className="font-mono text-xs tabular-nums">{(m.value as number).toLocaleString()}</span>
                {typeof m.previous === "number" && (
                  <span className="w-20 shrink-0 text-right">
                    <Trend now={m.value as number} before={m.previous} />
                  </span>
                )}
              </Row>
            ))}
          </Panel>
        )}

        {sum.people.length > 0 && (
          <Panel title="People" action={<span className="text-xs text-muted-foreground">{sum.people_total}</span>}>
            {sum.people.map((p) => (
              <Row key={p.id} onClick={() => onOpen(p.id)}>
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium">{p.name || "Unnamed"}</span>
                  {p.job_title && <span className="block truncate text-xs text-muted-foreground">{p.job_title}</span>}
                </span>
                {p.role && <Badge tone="blue">{words(p.role)}</Badge>}
              </Row>
            ))}
            {sum.people_total > sum.people.length && (
              <p className="pt-1 text-xs text-muted-foreground">
                And {sum.people_total - sum.people.length} more.
              </p>
            )}
          </Panel>
        )}

        {(sum.deals.latest.length > 0 || sum.pipelines.length > 0) && (
          <Panel
            title="Deals and pipelines"
            action={
              sum.deals.won + sum.deals.lost > 0 && (
                <span className="text-xs text-muted-foreground">
                  {sum.deals.won} won · {sum.deals.lost} lost
                </span>
              )
            }
          >
            {sum.pipelines.map((p) => (
              <Row key={p.id} onClick={() => onOpen(p.id)}>
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium">{p.name || "Unnamed"}</span>
                  <span className="block truncate text-xs text-muted-foreground">
                    {typeName(p.type)} · {p.renews_on ? `renews ${new Date(p.renews_on).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" })}` : `changed ${ago(p.updated_at)}`}
                  </span>
                </span>
                {fieldMoney(p.value) && <span className="font-mono text-xs">{fieldMoney(p.value)}</span>}
                {p.stage && <Badge>{words(p.stage)}</Badge>}
              </Row>
            ))}
            {sum.deals.latest.map((d) => (
              <Row key={d.id} onClick={() => onOpen(d.id)}>
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium">{d.name || "Deal"}</span>
                  {d.state === "lost" && (d.lost_reason || d.lost_note) && (
                    <span className="block truncate text-xs text-muted-foreground" title={d.lost_note ?? undefined}>
                      Lost: {[d.lost_reason ? (cat.labels.get("lost_reason")?.get(d.lost_reason) ?? words(d.lost_reason)) : "", d.lost_note].filter(Boolean).join(". ")}
                    </span>
                  )}
                </span>
                {d.amount_cents != null && d.amount_cents > 0 && (
                  <span className="font-mono text-xs">{money(d.amount_cents, d.currency)}</span>
                )}
                <Badge tone={d.state === "won" ? "green" : d.state === "lost" ? "red" : "blue"}>
                  {d.state === "open" && d.stage ? words(d.stage) : d.state}
                </Badge>
              </Row>
            ))}
          </Panel>
        )}

        {sum.conversations.latest.length > 0 && (
          <Panel
            title="Support conversations"
            action={
              <span className="text-xs text-muted-foreground">
                {sum.conversations.open > 0 ? `${sum.conversations.open} open` : "none open"}
              </span>
            }
          >
            {sum.conversations.latest.map((v) => (
              <Row key={v.id}>
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium">{v.subject || "No subject"}</span>
                  <span className="block truncate text-xs text-muted-foreground">
                    {v.status !== "closed" && v.last_from === "customer"
                      ? "Waiting on your reply"
                      : v.status !== "closed"
                        ? "Waiting on them"
                        : "Closed"}
                    {v.last_message_at ? ` · ${ago(v.last_message_at)}` : ""}
                    {v.assignee_name ? ` · ${v.assignee_name}` : ""}
                  </span>
                </span>
                {v.breached && v.status !== "closed" && <Badge tone="red">overdue</Badge>}
                {v.status !== "closed" && !v.breached && (
                  <Badge tone={v.last_from === "customer" ? "amber" : "grey"}>{v.status}</Badge>
                )}
              </Row>
            ))}
          </Panel>
        )}

        {sum.tasks.next.length > 0 && (
          <Panel
            title="Open tasks"
            action={
              onSection && (
                <button type="button" className="text-xs underline underline-offset-2" onClick={() => onSection("tasks")}>
                  All tasks
                </button>
              )
            }
          >
            {sum.tasks.next.map((t) => {
              const late = !!t.due_at && new Date(t.due_at).getTime() < Date.now();
              return (
                <Row key={t.id} onClick={onSection ? () => onSection("tasks") : undefined}>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-medium">{t.title}</span>
                    <span className={`block truncate text-xs ${late ? toneText.red : "text-muted-foreground"}`}>
                      {t.due_at ? (late ? `Was due ${ago(t.due_at)}` : `Due ${due(t.due_at)}`) : "No due date"}
                      {t.assigned_name ? ` · ${t.assigned_name}` : " · unassigned"}
                    </span>
                  </span>
                  {t.priority === "high" && <Badge tone="red">high</Badge>}
                </Row>
              );
            })}
          </Panel>
        )}

        {sum.automations.length > 0 && (
          <Panel title="Automations">
            {sum.automations.map((a) => (
              <Row key={`${a.kind}:${a.id}:${a.record_id}`}>
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium">{a.name || cap(a.kind)}</span>
                  <span className="block truncate text-xs text-muted-foreground">
                    {cap(a.kind)} · {a.status === "active" ? "in it" : words(a.status)} since {ago(a.since)}
                    {a.status === "active" && a.next_at ? ` · next step ${ago(a.next_at)}` : ""}
                    {a.reason ? ` · ${words(a.reason)}` : ""}
                  </span>
                </span>
                <Status tone={a.status === "active" ? "green" : a.status === "failed" || a.status === "bounced" ? "red" : "grey"}>
                  {a.status === "active" ? "running" : words(a.status)}
                </Status>
              </Row>
            ))}
          </Panel>
        )}

        {(sum.website.last_visit_at || sum.origin.source || sum.origin.referrer) && (
          <Panel title="Website and origin">
            <dl className="grid grid-cols-2 gap-3">
              {sum.website.last_visit_at && (
                <Fact label="Last visit">
                  <span title={stamp(sum.website.last_visit_at)}>{ago(sum.website.last_visit_at)}</span>
                </Fact>
              )}
              {sum.website.last_visit_at && <Fact label="Visits, last 30 days">{sum.website.visits_30d}</Fact>}
              {sum.website.last_path && <Fact label="Last landed on">{sum.website.last_path}</Fact>}
              {(sum.origin.source || sum.origin.referrer) && (
                <Fact label="First came from">
                  {[sum.origin.source ?? sum.origin.referrer, sum.origin.medium, sum.origin.campaign].filter(Boolean).join(" · ")}
                </Fact>
              )}
              {sum.origin.landing_path && <Fact label="First page">{sum.origin.landing_path}</Fact>}
              {sum.website.first_visit_at && <Fact label="First visit">{ago(sum.website.first_visit_at)}</Fact>}
            </dl>
          </Panel>
        )}

        {sum.alerts.length > 0 && (
          <Panel title="Alerts raised">
            {sum.alerts.map((a) => (
              <Row key={a.id}>
                <span className="min-w-0 flex-1 truncate">{cap(words(a.kind.replaceAll(".", " ")))}</span>
                <span className="text-xs text-muted-foreground">
                  {a.owner_name ? `${a.owner_name} · ` : ""}
                  {ago(a.created_at)}
                </span>
              </Row>
            ))}
          </Panel>
        )}
      </div>
    </div>
  );
}
