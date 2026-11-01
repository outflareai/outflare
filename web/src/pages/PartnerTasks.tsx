import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { PartnerProgram, Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import {
  Badge,
  Button,
  CopyButton,
  Empty,
  ErrorText,
  field,
  Input,
  Loading,
  toast,
} from "../ui";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { money } from "./Partners";
import { track } from "@/lib/analytics";

type Config = Schemas["PartnerTaskInput"];
type Action = Schemas["PartnerTaskAction"];
type Task = { id: string; status: string; config: Config };
type Submission = {
  id: string;
  task_id: string;
  membership_id: string;
  audience_id?: string;
  status: string;
  amount_cents: number;
  evidence_url: string;
  note: string;
  check_status: string;
  hold_until?: string;
};
type Audience = {
  id: string;
  membership_id: string;
  kind: string;
  role: string;
  url: string;
  host: string;
  topics: string;
  reach: number;
  reach_source: string;
  verified_at?: string;
  measured_at?: string;
};

export function PartnerTasks({
  ws,
  program,
  canManage,
}: {
  ws: string;
  program: PartnerProgram;
  canManage: boolean;
}) {
  const [tasks, setTasks] = useState<Task[] | null>(null);
  const [subs, setSubs] = useState<Submission[]>([]);
  const [audiences, setAudiences] = useState<Audience[]>([]);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [offer, setOffer] = useState<Submission | null>(null);
  const [price, setPrice] = useState("");
  const [note, setNote] = useState("");
  const path = { ws, id: program.id };
  const load = useCallback(async () => {
    const [a, b] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/partner-programs/{id}/tasks", {
        params: { path: { ws, id: program.id } },
      }),
      api.GET("/v1/workspaces/{ws}/partner-programs/{id}/audiences", {
        params: { path: { ws, id: program.id } },
      }),
    ]);
    if (a.error || b.error) {
      setError(errorMessage(a.error || b.error));
      return;
    }
    const result = a.data as { data: Task[]; submissions: Submission[] };
    setTasks(result.data);
    setSubs(result.submissions);
    setAudiences((b.data as { data: Audience[] }).data);
    setError("");
  }, [ws, program.id]);
  useEffect(() => {
    void load();
  }, [load]);
  async function review(
    s: Submission,
    action: Action["action"],
    amount?: number,
  ) {
    setBusy(true);
    try {
      const r = await api.POST(
        "/v1/workspaces/{ws}/partner-programs/{id}/task-submissions/{submission}",
        {
          params: { path: { ...path, submission: s.id } },
          body: {
            action,
            note,
            ...(amount == null ? {} : { amount_cents: amount }),
          },
        },
      );
      if (r.error) {
        setError(errorMessage(r.error));
        return;
      }
      setOffer(null);
      setNote("");
      toast(
        action === "offer"
          ? "Offer sent to the partner's portal"
          : "Submission updated",
      );
      await load();
    } finally {
      setBusy(false);
    }
  }
  async function toggle(t: Task) {
    setBusy(true);
    try {
      const r = await api.POST(
        "/v1/workspaces/{ws}/partner-programs/{id}/tasks/{task}",
        {
          params: { path: { ...path, task: t.id } },
          body: { action: t.status === "active" ? "pause" : "resume" },
        },
      );
      if (r.error) setError(errorMessage(r.error));
      else await load();
    } finally {
      setBusy(false);
    }
  }
  const pending = subs.filter(
    (s) => s.status === "applied" || s.status === "submitted",
  );
  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold">Tasks & opportunities</h2>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Give your users ways to help you grow. Match opportunities to their
            websites, channels and communities, with paid or voluntary tasks.
          </p>
        </div>
        {canManage && (
          <Button
            onClick={() => {
              track("ui.partner_task_composer_opened");
              setCreating(true);
            }}
          >
            Create task
          </Button>
        )}
      </div>
      <ErrorText>{error}</ErrorText>
      <div className="grid gap-3 sm:grid-cols-3">
        {[
          [
            "Live opportunities",
            tasks?.filter((t) => t.status === "active").length ?? 0,
          ],
          ["Waiting for review", pending.length],
          ["Partner profiles", audiences.length],
        ].map(([label, value]) => (
          <div key={label} className="rounded-lg border bg-card p-4">
            <p className="text-xs text-muted-foreground">{label}</p>
            <p className="mt-2 text-2xl font-semibold">{value}</p>
          </div>
        ))}
      </div>
      {!tasks ? (
        <Loading />
      ) : tasks.length === 0 ? (
        <Empty title="Your first partner task">
          Offer a blog placement, video tutorial, newsletter mention, or a
          simple voluntary task.
        </Empty>
      ) : (
        <div className="grid gap-3 md:grid-cols-2">
          {tasks.map((t) => (
            <div key={t.id} className="space-y-3 rounded-lg border bg-card p-5">
              <div className="flex justify-between gap-2">
                <Badge tone={t.status === "active" ? "green" : "grey"}>
                  {t.status}
                </Badge>
                <span className="text-sm font-semibold">
                  {rewardText(t.config, program.currency)}
                </span>
              </div>
              <h3 className="font-semibold">{t.config.title}</h3>
              <p className="whitespace-pre-wrap text-sm text-muted-foreground">
                {t.config.description}
              </p>
              <div className="flex flex-wrap gap-2 text-xs text-muted-foreground">
                <span>{t.config.category}</span>
                <span>·</span>
                <span>
                  {t.config.claim_mode === "instant"
                    ? "Instant claim"
                    : "Application"}
                </span>
                <span>·</span>
                <span>
                  {t.config.verification === "human"
                    ? "Human review"
                    : t.config.verification === "auto_hold"
                      ? `Automatic check · ${t.config.hold_days} day hold`
                      : "Automatic link check"}
                </span>
              </div>
              {t.config.target_host && (
                <div className="rounded-md bg-muted p-3 text-xs">
                  <b>Private website proposal</b>
                  <p className="mt-1">
                    Unlocked after verifying {t.config.target_host}.
                  </p>
                  <div className="mt-2 flex items-center gap-2">
                    <span>Invitation link</span>
                    <CopyButton
                      text={`${program.portal_url}?website=${encodeURIComponent(t.config.target_host)}#profiles`}
                    />
                  </div>
                </div>
              )}
              <div className="flex items-center justify-between border-t pt-3">
                <span className="text-xs text-muted-foreground">
                  {
                    subs.filter(
                      (s) => s.task_id === t.id && s.status !== "rejected",
                    ).length
                  }{" "}
                  / {t.config.slots} {t.config.slots === 1 ? "slot" : "slots"}
                </span>
                {canManage && (
                  <Button
                    variant="ghost"
                    disabled={busy}
                    onClick={() => void toggle(t)}
                  >
                    {t.status === "active" ? "Pause" : "Resume"}
                  </Button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
      <section className="space-y-3">
        <h3 className="font-semibold">Applications & work to review</h3>
        {pending.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            Applications and submitted work appear here. Approved cash joins the
            existing payout ledger; product rewards are delivered through your
            reward webhook.
          </p>
        ) : (
          pending.map((s) => {
            const task = tasks?.find((t) => t.id === s.task_id);
            const asset = audiences.find((a) => a.id === s.audience_id);
            return (
              <div key={s.id} className="space-y-2 rounded-lg border p-4">
                <div className="flex flex-wrap items-center gap-2">
                  <b className="text-sm">{task?.config.title}</b>
                  <Badge tone="amber">{s.status}</Badge>
                  {s.check_status && (
                    <Badge tone="grey">{s.check_status.replaceAll("_", " ")}</Badge>
                  )}
                </div>
                <p className="text-sm text-muted-foreground">
                  {asset
                    ? `${asset.kind} · ${asset.url} · ${asset.reach.toLocaleString()} reach (${asset.reach_source === "scraped" ? "public page" : "self-reported"})`
                    : "No audience profile required"}
                </p>
                {s.evidence_url && (
                  <a
                    className="block text-sm underline"
                    href={s.evidence_url}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    View submitted work
                  </a>
                )}
                {s.note && <p className="text-sm">{s.note}</p>}
                {s.hold_until && (
                  <p className="text-xs text-muted-foreground">
                    Held for human review until{" "}
                    {new Date(s.hold_until).toLocaleString()}.
                  </p>
                )}
                {canManage && (
                  <div className="flex gap-2">
                    <Button
                      disabled={busy}
                      onClick={() => {
                        if (s.status === "applied") {
                          setOffer(s);
                          setPrice(String(s.amount_cents / 100));
                          setNote("");
                        } else void review(s, "approve");
                      }}
                    >
                      {s.status === "applied" ? "Make offer" : "Approve work"}
                    </Button>
                    <Button
                      variant="ghost"
                      disabled={busy}
                      onClick={() => void review(s, "reject")}
                    >
                      Reject
                    </Button>
                  </div>
                )}
              </div>
            );
          })
        )}
      </section>
      <section className="space-y-3">
        <h3 className="font-semibold">Partner websites & channels</h3>
        {audiences.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            Partners add their profiles once. Their reach and asset type help
            match tasks; website ownership is checked separately.
          </p>
        ) : (
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full text-left text-sm">
              <thead className="border-b text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">Profile</th>
                  <th className="p-3">Role</th>
                  <th className="p-3">Reach</th>
                  <th className="p-3">Ownership</th>
                </tr>
              </thead>
              <tbody>
                {audiences.map((a) => (
                  <tr key={a.id} className="border-b last:border-0">
                    <td className="p-3">
                      <a
                        className="underline"
                        href={a.url}
                        target="_blank"
                        rel="noreferrer"
                      >
                        {a.url}
                      </a>
                      <p className="text-xs text-muted-foreground">
                        {a.kind} · {a.topics}
                      </p>
                    </td>
                    <td className="p-3">{a.role}</td>
                    <td className="p-3">
                      {a.reach.toLocaleString()}
                      <p className="text-xs text-muted-foreground">
                        {a.reach_source === "scraped" ? "Public page" : "Self-reported"}
                        {a.measured_at
                          ? ` · ${new Date(a.measured_at).toLocaleDateString()}`
                          : ""}
                      </p>
                    </td>
                    <td className="p-3">
                      {a.verified_at ? "Verified website" : "Unverified"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {creating && (
        <TaskComposer
          ws={ws}
          program={program}
          onClose={() => setCreating(false)}
          onSaved={() => {
            setCreating(false);
            void load();
          }}
        />
      )}
      {offer && (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!open) setOffer(null);
          }}
        >
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Make a partner offer</DialogTitle>
              <DialogDescription>
                Choose the reward for this partner's placement. They must accept
                before starting work.
              </DialogDescription>
            </DialogHeader>
            <form
              className="space-y-4"
              onSubmit={(e) => {
                e.preventDefault();
                void review(offer, "offer", Math.round(Number(price) * 100));
              }}
            >
              <Input
                label={`Reward (${program.currency})`}
                type="number"
                min="0"
                step="0.01"
                value={price}
                onChange={(e) => setPrice(e.target.value)}
                required
              />
              <Input
                label="Message to partner"
                value={note}
                onChange={(e) => setNote(e.target.value)}
                maxLength={2000}
              />
              <ErrorText>{error}</ErrorText>
              <Button disabled={busy}>Send offer</Button>
            </form>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}
function rewardText(c: Config, currency: string) {
  if (c.reward_kind === "free") return "Voluntary · unpaid";
  if (c.reward_kind === "action") return c.reward_label;
  if (c.pricing === "custom") return "Custom proposal";
  return c.pricing === "range"
    ? `${money(c.amount_cents ?? 0, currency)}–${money(c.max_amount_cents ?? 0, currency)}`
    : money(c.amount_cents ?? 0, currency);
}
function TaskComposer({
  ws,
  program,
  onClose,
  onSaved,
}: {
  ws: string;
  program: PartnerProgram;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [v, setV] = useState<Config>({
    title: "",
    description: "",
    category: "blog",
    reward_kind: "cash",
    pricing: "fixed",
    amount_cents: 5000,
    max_amount_cents: 15000,
    claim_mode: "instant",
    verification: "human",
    hold_days: 3,
    asset_kind: "website",
    min_reach: 0,
    slots: 20,
    target_host: "",
    required_link: "",
    reward_label: "",
    action: "",
  });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  function change<K extends keyof Config>(key: K, value: Config[K]) {
    setV((cur) => ({ ...cur, [key]: value }));
  }
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const r = await api.POST(
        "/v1/workspaces/{ws}/partner-programs/{id}/tasks",
        { params: { path: { ws, id: program.id } }, body: v },
      );
      if (r.error) setError(errorMessage(r.error));
      else {
        toast("Task created");
        onSaved();
      }
    } finally {
      setBusy(false);
    }
  }
  function select<K extends keyof Config>(
    label: string,
    key: K,
    options: [string, string][],
  ) {
    return (
      <label className="block space-y-1 text-sm">
        {label}
        <select
          className={field + " w-full"}
          value={String(v[key] ?? "")}
          onChange={(e) => change(key, e.target.value as Config[K])}
        >
          {options.map(([value, text]) => (
            <option key={value} value={value}>
              {text}
            </option>
          ))}
        </select>
      </label>
    );
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Create a partner task</DialogTitle>
          <DialogDescription>
            Publish a general opportunity or prepare a proposal for a specific
            website.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="space-y-4">
          <Input
            label="Title"
            value={v.title}
            onChange={(e) => change("title", e.target.value)}
            required
            maxLength={160}
            placeholder="Publish a tutorial on your blog"
          />
          <label className="block space-y-1 text-sm">
            What should the partner do?
            <textarea
              className={field + " min-h-24 w-full"}
              value={v.description}
              onChange={(e) => change("description", e.target.value)}
              required
              maxLength={6000}
              placeholder="Describe the placement, deliverables, duration and disclosure requirements."
            />
          </label>
          <div className="grid gap-4 sm:grid-cols-2">
            {select(
              "Category",
              "category",
              [
                "blog",
                "video",
                "social",
                "newsletter",
                "banner",
                "community",
                "other",
              ].map((x) => [x, x]),
            )}
            {select("Audience asset", "asset_kind", [
              ["", "No profile needed"],
              ...[
                "website",
                "youtube",
                "x",
                "linkedin",
                "instagram",
                "tiktok",
                "newsletter",
                "reddit",
                "facebook",
                "discord",
                "podcast",
                "other",
              ].map((x) => [x, x] as [string, string]),
            ])}
            {select("Reward", "reward_kind", [
              ["free", "Voluntary / unpaid"],
              ["cash", "Cash"],
              ["action", "Product credit, subscription or other reward"],
            ])}
            {v.reward_kind === "cash" &&
              select("Pricing", "pricing", [
                ["fixed", "Fixed price"],
                ["range", "Price range"],
                ["custom", "Custom proposal"],
              ])}
            {v.reward_kind === "cash" && v.pricing !== "custom" && (
              <Input
                label={`Reward${v.pricing === "range" ? " from" : ""} (${program.currency})`}
                type="number"
                min="0.01"
                step="0.01"
                value={(v.amount_cents ?? 0) / 100}
                onChange={(e) =>
                  change(
                    "amount_cents",
                    Math.round(Number(e.target.value) * 100),
                  )
                }
                required
              />
            )}
            {v.reward_kind === "cash" && v.pricing === "range" && (
              <Input
                label={`Maximum (${program.currency})`}
                type="number"
                min="0.01"
                step="0.01"
                value={(v.max_amount_cents ?? 0) / 100}
                onChange={(e) =>
                  change(
                    "max_amount_cents",
                    Math.round(Number(e.target.value) * 100),
                  )
                }
                required
              />
            )}
            {v.reward_kind === "action" && (
              <>
                <Input
                  label="Reward shown to partners"
                  value={v.reward_label ?? ""}
                  onChange={(e) => change("reward_label", e.target.value)}
                  required
                  maxLength={200}
                  placeholder="One month of Pro"
                />
                <Input
                  label="Reward webhook action"
                  value={v.action ?? ""}
                  onChange={(e) => change("action", e.target.value)}
                  required
                  maxLength={60}
                  placeholder="grant_pro_month"
                />
              </>
            )}
            {select("Joining a task", "claim_mode", [
              ["instant", "Claim immediately"],
              ["application", "Apply for approval"],
            ])}
            {select("Verification", "verification", [
              ["human", "Human review only"],
              ["auto", "Automatic website link check"],
              ["auto_hold", "Automatic check, then hold for human review"],
            ])}
            <Input
              label="Available slots"
              type="number"
              min="1"
              max="10000"
              value={v.slots}
              onChange={(e) => change("slots", Number(e.target.value))}
              required
            />
            <Input
              label="Minimum audience reach"
              type="number"
              min="0"
              value={v.min_reach}
              onChange={(e) => change("min_reach", Number(e.target.value))}
            />
          </div>
          {v.verification !== "human" && (
            <>
              <Input
                label="Required link on the partner's website"
                type="url"
                value={v.required_link ?? ""}
                onChange={(e) => change("required_link", e.target.value)}
                placeholder="https://example.com/product"
                required
              />
              <p className="text-xs text-muted-foreground">
                Requires a verified website and your workspace's own Geonode
                connection. A link check confirms presence; editorial quality
                needs human review.
              </p>
              {v.verification === "auto_hold" && (
                <Input
                  label="Human review hold (days)"
                  type="number"
                  min="1"
                  max="365"
                  value={v.hold_days}
                  onChange={(e) => change("hold_days", Number(e.target.value))}
                  required
                />
              )}
            </>
          )}
          <div className="rounded-lg border p-4">
            <Input
              label="Private proposal for a website (optional)"
              value={v.target_host ?? ""}
              onChange={(e) => change("target_host", e.target.value)}
              placeholder="example.com"
              hint="Only a partner who verifies this exact hostname can see the proposal. Private proposals reserve one slot. Copy the invitation link after creating it."
            />
          </div>
          <p className="text-xs text-muted-foreground">
            Use paid or unpaid tasks according to each platform's rules. Reward
            ranges and custom prices require an application and an accepted
            offer. Referral commissions continue through your existing program
            rules.
          </p>
          <ErrorText>{error}</ErrorText>
          <Button disabled={busy}>{busy ? "Creating…" : "Create task"}</Button>
        </form>
      </DialogContent>
    </Dialog>
  );
}
