import { useCallback, useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import {
  ArrowLeft,
  ArrowRight,
  Mail,
  Plus,
  RefreshCw,
  Users,
} from "lucide-react";
import { api, errorMessage } from "@/api";
import {
  Button,
  Empty,
  ErrorText,
  Loading,
  field,
  confirmAction,
  toast,
} from "@/ui";
import { track } from "@/lib/analytics";
import { salesSections } from "@/lib/sales";
import { SalesQueue, SalesSources } from "./SalesQueue";
import type { Tab } from "@/App";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type Lead = Schemas["SalesLead"];
type Member = Schemas["SalesMember"];
type Template = Schemas["SalesTemplate"];
type Draft = Schemas["SalesDraft"];
type Detail = Schemas["SalesLeadDetail"];
type Action = Schemas["SalesAction"];
function checked<T>(r: { data?: unknown; error?: unknown }): T {
  if (r.error || !r.data)
    throw r.error ?? { error: { message: "No answer received." } };
  if (
    typeof r.data === "object" &&
    ("action" in r.data || "mission_action" in r.data)
  )
    throw { error: { message: "This action is waiting for approval." } };
  return r.data as T;
}
const date = (v?: string | null) => (v ? new Date(v).toLocaleString() : "");
const stateName = (s: string) =>
  ({
    new: "New",
    active: "Active",
    customer: "Customer",
    archived: "Archived",
  })[s] ?? s;
function Dot() {
  return (
    <span
      className="inline-block size-1.5 shrink-0 rounded-full bg-emerald-400"
      aria-label="Unread updates"
    />
  );
}

export function SalesFloor({
  brand,
  open,
  setOpen,
  go,
  canManageTeam,
}: {
  ws: string;
  brand: string;
  open?: string;
  setOpen: (v?: string) => void;
  go: (tab: Tab, item?: string) => void;
  canManageTeam: boolean;
}) {
  const [me, setMe] = useState<Member>();
  const [team, setTeam] = useState<Member[]>([]);
  const [leads, setLeads] = useState<Lead[]>([]);
  const [templates, setTemplates] = useState<Template[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [unseen, setUnseen] = useState(false);
  const [unread, setUnread] = useState(false);
  const [myState, setMyState] = useState("active");
  const [allState, setAllState] = useState("all");
  const [person, setPerson] = useState("");
  const [returnQueue, setReturnQueue] = useState<string>();
  const [taking, setTaking] = useState<string>();
  const [cursor, setCursor] = useState<string>();
  const [next, setNext] = useState<string | null>();
  const manager = canManageTeam || me?.role === "manager";
  const representative = me?.role === "ae" || me?.role === "sdr";
  const defaultSection = manager && !representative ? "all" : "new";
  const [, section = defaultSection, leadID] = (open ?? "sales").split(":");
  const state = section === "all" ? allState : myState;
  const setState = section === "all" ? setAllState : setMyState;
  const path = { brand };
  const loadIdentity = useCallback(async () => {
    const [m, t, library] = await Promise.all([
      api.GET("/v1/brands/{brand}/sales/me", { params: { path: { brand } } }),
      api.GET("/v1/brands/{brand}/sales/team", { params: { path: { brand } } }),
      api.GET("/v1/brands/{brand}/sales/templates", {
        params: { path: { brand } },
      }),
    ]);
    setMe(checked<Member>(m));
    setTeam(checked<{ data: Member[] }>(t).data);
    setTemplates(checked<{ data: Template[] }>(library).data);
  }, [brand]);
  useEffect(() => {
    void loadIdentity().catch((e) => setError(errorMessage(e)));
  }, [loadIdentity]);
  useEffect(() => {
    setCursor(undefined);
  }, [section, state, person]);
  const loadLeads = useCallback(async () => {
    const queue = ["new", "mine", "inbox", "handoffs", "all"].includes(section)
      ? (section as "new" | "mine" | "inbox" | "handoffs" | "all")
      : "mine";
    const result = await api.GET("/v1/brands/{brand}/sales/leads", {
      params: {
        path: { brand },
        query: {
          queue,
          ...(["mine", "all"].includes(queue) && state !== "all"
            ? { state: state as Lead["state"] }
            : {}),
          ...(queue === "all" && person ? { person } : {}),
          cursor,
          limit: 50,
        },
      },
    });
    const data = checked<Schemas["SalesLeadList"]>(result);
    setLeads(data.data);
    setNext(data.next_cursor);
    setUnseen(data.new_unseen);
    setUnread(data.unread);
    if (section === "new") {
      checked(
        await api.POST("/v1/brands/{brand}/sales/queue-seen", {
          params: { path: { brand } },
        }),
      );
      setUnseen(false);
    }
  }, [brand, section, state, person, cursor]);
  useEffect(() => {
    if (!me) return;
    let active = true;
    setLoading(true);
    setError("");
    const run = () =>
      loadLeads()
        .catch((e) => {
          if (active) setError(errorMessage(e));
        })
        .finally(() => {
          if (active) setLoading(false);
        });
    void run();
    const timer = setInterval(() => void run(), 15000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [loadLeads, me]);
  const nav = (s: string) => {
    if (s.startsWith("lead:") && section !== "lead") setReturnQueue(section);
    setOpen(`sales:${s}`);
    track("ui.sales_section_opened", { section: s.split(":")[0] });
  };
  const items = [
    ...(manager && !representative
      ? [
          ["all", "All leads"],
          ["new", "New leads"],
        ]
      : []),
    ...(!manager || representative
      ? [
          ["new", "New leads"],
          ...(me?.role === "ae" ? [["inbox", "Inbox"]] : []),
          ["mine", "My leads"],
          ...(me?.role === "sdr" ? [["handoffs", "Sent to AEs"]] : []),
        ]
      : []),
    ...(!manager || representative
      ? [
          ["templates", "My templates"],
          ["introduction", "My introduction"],
          ["email", "My email"],
        ]
      : []),
    ...(manager
      ? [
          ...(representative ? [["all", "All leads"]] : []),
          ["team", "Team"],
          ["settings", "Sales settings"],
        ]
      : []),
  ];
  return (
    <div className="flex min-h-[75vh] flex-col gap-8 lg:flex-row">
      <aside className="w-full shrink-0 space-y-6 lg:w-48">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Sales</h1>
          <p className="mt-1 text-xs text-muted-foreground">
            {me?.name} ·{" "}
            {me?.role === "ae"
              ? "Account manager"
              : me?.role === "sdr"
                ? "SDR"
                : manager
                  ? "Manager"
                  : "Awaiting Sales role"}
          </p>
        </div>
        <nav
          aria-label="Sales navigation"
          className="flex flex-wrap gap-1 lg:flex-col"
        >
          {items.map(([key, label]) => (
            <button
              key={key}
              className={`flex items-center gap-2 rounded-lg px-3 py-2 text-left text-sm ${section === key ? "bg-muted font-medium" : "text-muted-foreground hover:bg-muted/50"}`}
              onClick={() => nav(key)}
            >
              <span className="w-2">
                {((key === "new" && unseen) ||
                  (["mine", "inbox"].includes(key) && unread)) && <Dot />}
              </span>
              {label}
            </button>
          ))}
        </nav>
        {manager && !representative && (
          <details
            className="text-sm text-muted-foreground"
            onToggle={(e) => {
              if (e.currentTarget.open)
                track("ui.sales_personal_settings_opened");
            }}
          >
            <summary className="cursor-pointer px-3 py-2">
              Personal settings
            </summary>
            <div className="mt-1 flex flex-col gap-1">
              {["templates", "introduction", "email"].map((key) => (
                <button
                  key={key}
                  className="rounded-lg px-5 py-2 text-left hover:bg-muted/50"
                  onClick={() => nav(key)}
                >
                  {salesSections[key]}
                </button>
              ))}
            </div>
          </details>
        )}
        <button
          className="text-xs text-muted-foreground hover:underline"
          onClick={() => setOpen("person")}
        >
          People, companies & CRM
        </button>
      </aside>
      <main className="min-w-0 flex-1 space-y-5">
        <ErrorText>{error}</ErrorText>
        {me?.role === "none" && !manager && (
          <Empty title="Your Sales role is not assigned">
            Ask your workspace manager to add you to the Sales team. Your
            personal email remains disconnected.
          </Empty>
        )}
        {section === "lead" && leadID && me ? (
          <SalesLeadPage
            key={`${brand}:${leadID}`}
            brand={brand}
            id={leadID}
            me={me}
            team={team}
            templates={templates}
            manager={manager}
            backLabel={`Back to ${(salesSections[returnQueue ?? (manager && !representative ? "all" : "mine")] ?? "leads").toLowerCase()}`}
            onBack={() =>
              nav(returnQueue ?? (manager && !representative ? "all" : "mine"))
            }
            onNext={async () => {
              if (manager && !representative) {
                const rows = checked<Schemas["SalesLeadList"]>(
                  await api.GET("/v1/brands/{brand}/sales/leads", {
                    params: { path, query: { queue: "all" } },
                  }),
                ).data;
                const target = rows.find((l) => l.id !== leadID);
                nav(target ? `lead:${target.id}` : "all");
                return;
              }
              const own = checked<Schemas["SalesLeadList"]>(
                await api.GET("/v1/brands/{brand}/sales/leads", {
                  params: { path, query: { queue: "mine", state: "active" } },
                }),
              ).data;
              const nextOwn = own.find(
                (l) => l.id !== leadID && l.reply_state === "reply_received",
              );
              const available = nextOwn
                ? []
                : checked<Schemas["SalesLeadList"]>(
                    await api.GET("/v1/brands/{brand}/sales/leads", {
                      params: { path, query: { queue: "new" } },
                    }),
                  ).data;
              const target = nextOwn ?? available.find((l) => l.id !== leadID);
              nav(target ? `lead:${target.id}` : "mine");
            }}
            onLibrary={loadIdentity}
          />
        ) : null}
        {["new", "mine", "inbox", "handoffs", "all"].includes(section) && (
          <>
            <SalesQueue
              section={section}
              leads={leads}
              team={team}
              manager={manager}
              representative={representative}
              connected={me?.connected ?? false}
              loading={loading}
              taking={taking}
              state={state}
              person={person}
              onFilter={(value, teammate) => {
                setCursor(undefined);
                setState(value);
                setPerson(teammate);
                track("ui.sales_filter_changed", {
                  section,
                  state: value,
                  person_filter: !!teammate,
                });
              }}
              onOpen={(id) => nav(`lead:${id}`)}
              onNavigate={nav}
              onTake={async (lead) => {
                if (taking) return;
                setTaking(lead.id);
                setError("");
                try {
                  checked(
                    await api.POST(
                      "/v1/brands/{brand}/sales/leads/{id}/actions",
                      {
                        params: { path: { brand, id: lead.id } },
                        body: { action: "take", version: lead.version },
                      },
                    ),
                  );
                  nav(`lead:${lead.id}`);
                } catch (e) {
                  setError(errorMessage(e));
                } finally {
                  setTaking(undefined);
                }
              }}
            />
            <div className="flex gap-2">
              {cursor && (
                <Button variant="ghost" onClick={() => setCursor(undefined)}>
                  First page
                </Button>
              )}
              {next && (
                <Button
                  variant="ghost"
                  onClick={() => setCursor(next ?? undefined)}
                >
                  More leads <ArrowRight className="size-3.5" />
                </Button>
              )}
            </div>
          </>
        )}
        {section === "templates" && (
          <SalesTemplates
            brand={brand}
            templates={templates}
            reload={loadIdentity}
          />
        )}
        {section === "introduction" && me && (
          <SalesIntroduction brand={brand} me={me} reload={loadIdentity} />
        )}
        {section === "email" && me && (
          <SalesEmail
            brand={brand}
            me={me}
            manager={manager && !representative}
            reload={loadIdentity}
            onPolicy={() => setOpen("settings")}
            onLeads={() => nav("all")}
          />
        )}
        {section === "team" && manager && (
          <>
            <header className="flex items-center justify-between">
              <h2 className="text-2xl font-semibold">Team</h2>
              {canManageTeam && (
                <Button onClick={() => go("members")}>
                  <Users className="size-3.5" /> Invite teammate
                </Button>
              )}
            </header>
            <p className="text-sm text-muted-foreground">
              Invite people using workspace invitations, then assign their Sales
              role here after they join. Each person connects their own email.
            </p>
            <div className="divide-y rounded-xl border bg-card">
              {team.map((m) => (
                <div
                  key={m.user_id}
                  className="flex flex-wrap items-center gap-4 p-4"
                >
                  <div className="flex-1">
                    <div className="font-medium">{m.name}</div>
                    <div className="text-xs text-muted-foreground">
                      {m.connected
                        ? "Send & receive connected"
                        : "Email not connected"}{" "}
                      · {m.open_leads} active leads
                    </div>
                  </div>
                  <select
                    aria-label={`Sales role for ${m.name}`}
                    className={field}
                    disabled={!canManageTeam}
                    value={m.role}
                    onChange={(e) => {
                      void (async () => {
                        try {
                          checked(
                            await api.PUT(
                              "/v1/brands/{brand}/sales/team/{user}",
                              {
                                params: { path: { brand, user: m.user_id } },
                                body: {
                                  role: e.target.value as Member["role"],
                                },
                              },
                            ),
                          );
                          await loadIdentity();
                        } catch (e) {
                          setError(errorMessage(e));
                        }
                      })();
                    }}
                  >
                    <option value="none">No Sales role</option>
                    <option value="sdr">SDR</option>
                    <option value="ae">Account manager</option>
                    <option value="manager">Manager</option>
                  </select>
                </div>
              ))}
            </div>
          </>
        )}
        {section === "settings" && manager && (
          <>
            <h2 className="text-2xl font-semibold">Sales settings</h2>
            <section className="space-y-4 rounded-xl border bg-card p-6">
              <h3 className="font-medium">EmailBison introductions</h3>
              <p className="max-w-xl text-sm text-muted-foreground">
                Each imported lead retains its original EmailBison connection
                and sender mailbox. Taking a lead queues an introduction in that
                thread, with the new owner on CC.
              </p>
              <Button onClick={() => go("connectors")}>
                Manage EmailBison connections
              </Button>
              <p className="text-sm text-muted-foreground">
                Personal Sales email uses each rep’s own Google account.
                Configure internal domains and excluded addresses in CRM mail
                settings before capture.
              </p>
              <Button variant="ghost" onClick={() => setOpen("settings")}>
                Mail privacy & internal domains
              </Button>
            </section>
          </>
        )}
      </main>
    </div>
  );
}

function SalesTemplates({
  brand,
  templates,
  reload,
}: {
  brand: string;
  templates: Template[];
  reload: () => Promise<void>;
}) {
  const [editing, setEditing] = useState<Template | null>();
  const [name, setName] = useState("");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const edit = (t?: Template) => {
    setEditing(t ?? null);
    setName(t?.name ?? "");
    setBody(t?.body ?? "");
    setError("");
    track("ui.sales_template_editor_opened", {});
  };
  async function save(archive = false) {
    if (busy) return;
    setBusy(true);
    try {
      const input = {
        name,
        body,
        version: editing?.version,
        archived: archive,
      };
      if (editing)
        checked(
          await api.PUT("/v1/brands/{brand}/sales/templates/{id}", {
            params: { path: { brand, id: editing.id } },
            body: input,
          }),
        );
      else
        checked(
          await api.POST("/v1/brands/{brand}/sales/templates", {
            params: { path: { brand } },
            body: input,
          }),
        );
      await reload();
      setEditing(undefined);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <header className="flex items-center justify-between">
        <h2 className="text-2xl font-semibold">My templates</h2>
        <Button onClick={() => edit()}>
          <Plus className="size-3.5" /> New template
        </Button>
      </header>
      <p className="text-sm text-muted-foreground">
        Your personal replies. Apply one to an editable draft, then send when
        you’re ready.
      </p>
      <div className="grid gap-3 md:grid-cols-2">
        {templates.map((t) => (
          <button
            key={t.id}
            onClick={() => edit(t)}
            className="rounded-xl border bg-card p-5 text-left hover:border-emerald-500/50"
          >
            <h3 className="font-medium">{t.name}</h3>
            <p className="mt-3 line-clamp-3 whitespace-pre-wrap text-sm text-muted-foreground">
              {t.body}
            </p>
          </button>
        ))}
      </div>
      {editing !== undefined && (
        <Dialog open onOpenChange={(v) => !v && !busy && setEditing(undefined)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>
                {editing ? "Edit template" : "New template"}
              </DialogTitle>
              <DialogDescription>
                Use {"{first_name}"}, {"{company}"} and {"{my_name}"}. Changes
                apply to future drafts; earlier versions stay recorded.
              </DialogDescription>
            </DialogHeader>
            <label className="space-y-1 text-sm">
              Name
              <input
                autoFocus
                className={`${field} w-full`}
                value={name}
                onChange={(e) => setName(e.target.value)}
                maxLength={100}
              />
            </label>
            <label className="space-y-1 text-sm">
              Message
              <textarea
                className={`${field} w-full`}
                rows={9}
                value={body}
                onChange={(e) => setBody(e.target.value)}
                maxLength={20000}
              />
            </label>
            <ErrorText>{error}</ErrorText>
            <DialogFooter>
              {editing && (
                <Button
                  variant="ghost"
                  disabled={busy}
                  onClick={() => void save(true)}
                >
                  Archive template
                </Button>
              )}
              <Button
                disabled={busy || !name.trim() || !body.trim()}
                onClick={() => void save()}
              >
                Save template
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </>
  );
}
function SalesIntroduction({
  brand,
  me,
  reload,
}: {
  brand: string;
  me: Member;
  reload: () => Promise<void>;
}) {
  const [line, setLine] = useState(me.introduction);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <div className="max-w-2xl space-y-5">
      <h2 className="text-2xl font-semibold">My introduction</h2>
      <p className="text-sm text-muted-foreground">
        The line colleagues use to introduce you. Saving it affects future
        introductions only.
      </p>
      <label className="block space-y-2 text-sm">
        How you help customers
        <textarea
          rows={3}
          className={`${field} w-full`}
          placeholder="who helps teams choose the right plan"
          value={line}
          onChange={(e) => setLine(e.target.value)}
          maxLength={1000}
        />
      </label>
      <div className="rounded-xl border bg-card p-5 text-sm">
        <h3 className="mb-3 font-medium">When you take a new lead</h3>
        <p className="text-muted-foreground">
          The original EmailBison sender introduces you in the existing
          conversation.
        </p>
        <p className="mt-4">
          I’m introducing {me.name}
          {line ? `, ${line}` : ""}. I’ve included them on CC so you can
          continue here.
        </p>
      </div>
      {me.role === "ae" && (
        <p className="text-sm text-muted-foreground">
          When an SDR hands you a lead, they send an editable introduction from
          their own email with you on CC. Opening a lead sends nothing.
        </p>
      )}
      <ErrorText>{error}</ErrorText>
      <Button
        disabled={busy}
        onClick={() => {
          setBusy(true);
          void api
            .PUT("/v1/brands/{brand}/sales/me", {
              params: { path: { brand } },
              body: { introduction: line },
            })
            .then((r) => {
              checked(r);
              return reload();
            })
            .then(() => toast("Introduction saved."))
            .catch((e) => setError(errorMessage(e)))
            .finally(() => setBusy(false));
        }}
      >
        Save introduction
      </Button>
    </div>
  );
}
function SalesEmail({
  brand,
  me,
  manager,
  reload,
  onPolicy,
  onLeads,
}: {
  brand: string;
  me: Member;
  manager: boolean;
  reload: () => Promise<void>;
  onPolicy: () => void;
  onLeads: () => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function run(kind: "connect" | "sync" | "disconnect") {
    if (busy) return;
    setBusy(true);
    try {
      if (kind === "connect") {
        const r = checked<{ url: string }>(
          await api.POST("/v1/brands/{brand}/sales/mail/authorize", {
            params: { path: { brand } },
          }),
        );
        window.location.assign(r.url);
      }
      if (kind === "sync") {
        checked(
          await api.POST("/v1/brands/{brand}/sales/mail/sync", {
            params: { path: { brand } },
          }),
        );
        toast("Email catch-up queued.");
      }
      if (
        kind === "disconnect" &&
        (await confirmAction(
          "Disconnect Sales email?",
          "New capture and sending stop. Shared conversation records remain in Sales.",
          "Disconnect",
        ))
      ) {
        checked(
          await api.DELETE("/v1/brands/{brand}/sales/mail/connection", {
            params: { path: { brand } },
          }),
        );
        await reload();
      }
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  const notice = new URLSearchParams(window.location.search).get(
    "sales_google",
  );
  if (manager)
    return (
      <div className="max-w-2xl space-y-5">
        <h2 className="text-2xl font-semibold">My email</h2>
        <section className="space-y-4 rounded-xl border bg-card p-6">
          <h3 className="font-medium">You’re viewing Sales as a manager</h3>
          <p className="text-sm text-muted-foreground">
            You can review incoming leads, read conversations and see who is
            handling them without connecting a mailbox. Personal email is for
            SDRs and account managers who take leads and reply.
          </p>
          <Button onClick={onLeads}>
            Open all leads <ArrowRight className="size-3.5" />
          </Button>
        </section>
      </div>
    );
  return (
    <div className="max-w-2xl space-y-5">
      <h2 className="text-2xl font-semibold">My email</h2>
      <section className="space-y-4 rounded-xl border bg-card p-6">
        <Mail className="size-6 text-emerald-400" />
        <h3 className="font-medium">Send and receive from your own Gmail</h3>
        <p className="text-sm text-muted-foreground">
          Authorize your own account to send replies and capture incoming and
          outgoing mail in your assigned Sales threads. These Sales
          conversations are shared with assigned colleagues and Sales managers.
          Unrelated personal mail stays outside Sales.
        </p>
        <p className="text-sm">
          {me.connected
            ? "Connected for sending and receiving"
            : "Connect before taking a lead or receiving a handoff"}
        </p>
        {me.mail_status === "sync_failed" && (
          <p role="alert" className="text-sm text-destructive">
            Catch-up did not finish. Review mail privacy settings or reconnect
            Gmail, then retry.
          </p>
        )}
        {me.last_synced_at && (
          <p className="text-xs text-muted-foreground">
            Last completed catch-up: {date(me.last_synced_at)}
          </p>
        )}
        {notice && (
          <p role="status" className="text-sm">
            {notice === "connected"
              ? "Google authorization completed."
              : "Google authorization did not complete. Use your matching work account and approve both read and send access."}
          </p>
        )}
        <ErrorText>{error}</ErrorText>
        <div className="flex flex-wrap gap-2">
          <Button
            disabled={busy || !["sdr", "ae"].includes(me.role)}
            onClick={() => void run("connect")}
          >
            {me.connected ? "Reconnect Gmail" : "Connect Gmail"}
          </Button>
          {me.connected && (
            <>
              <Button
                variant="ghost"
                disabled={busy}
                onClick={() => void run("sync")}
              >
                <RefreshCw className="size-3.5" /> Catch up now
              </Button>
              <Button
                variant="ghost"
                disabled={busy}
                onClick={() => void run("disconnect")}
              >
                Disconnect
              </Button>
            </>
          )}
        </div>
        <button
          className="text-xs text-muted-foreground underline"
          onClick={onPolicy}
        >
          Mail privacy and internal domains
        </button>
      </section>
    </div>
  );
}

function SalesLeadPage({
  brand,
  id,
  me,
  team,
  templates,
  manager,
  backLabel,
  onBack,
  onNext,
  onLibrary,
}: {
  brand: string;
  id: string;
  me: Member;
  team: Member[];
  templates: Template[];
  manager: boolean;
  backLabel: string;
  onBack: () => void;
  onNext: () => Promise<void>;
  onLibrary: () => Promise<void>;
}) {
  const [detail, setDetail] = useState<Detail>();
  const [body, setBody] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [saving, setSaving] = useState(false);
  const [dialog, setDialog] = useState<
    "archive" | "confirm_customer" | "handoff" | "details" | "template"
  >();
  const [note, setNote] = useState("");
  const [amount, setAmount] = useState("");
  const [reason, setReason] = useState("");
  const [target, setTarget] = useState("");
  const [intro, setIntro] = useState("");
  const [templateName, setTemplateName] = useState("");
  const draft = useRef<Draft | null>(null);
  const latest = useRef("");
  const saved = useRef("");
  const saveQueue = useRef<Promise<unknown>>(Promise.resolve());
  const initialized = useRef(false);
  const path = { brand, id };
  const load = useCallback(async () => {
    const data = checked<Detail>(
      await api.GET("/v1/brands/{brand}/sales/leads/{id}", {
        params: { path: { brand, id } },
      }),
    );
    setDetail(data);
    if (!initialized.current) {
      initialized.current = true;
      draft.current = data.draft;
      saved.current = latest.current = data.draft?.body ?? "";
      setBody(latest.current);
    }
    return data;
  }, [brand, id]);
  useEffect(() => {
    void load()
      .then(async (data) => {
        checked(
          await api.POST("/v1/brands/{brand}/sales/leads/{id}/actions", {
            params: { path },
            body: { action: "read", version: data.lead.version },
          }),
        );
      })
      .catch((e) => setError(errorMessage(e)));
    const timer = setInterval(
      () => void load().catch((e) => setError(errorMessage(e))),
      15000,
    );
    return () => clearInterval(timer);
  }, [load]);
  const save = useCallback(
    (templateID?: string): Promise<Draft | null> => {
      const task = saveQueue.current
        .catch(() => {})
        .then(async () => {
          if (latest.current === saved.current && !templateID)
            return draft.current;
          setSaving(true);
          try {
            const text = latest.current;
            const data = checked<Draft>(
              await api.PUT("/v1/brands/{brand}/sales/leads/{id}/draft", {
                params: { path: { brand, id } },
                body: {
                  revision: draft.current?.revision ?? 0,
                  body: text,
                  ...(templateID ? { template_id: templateID } : {}),
                },
              }),
            );
            draft.current = data;
            saved.current = data.body;
            if (templateID) {
              latest.current = data.body;
              setBody(data.body);
            }
            return data;
          } finally {
            setSaving(false);
          }
        });
      saveQueue.current = task;
      return task;
    },
    [brand, id],
  );
  useEffect(() => {
    if (
      !initialized.current ||
      latest.current === saved.current ||
      detail?.lead.owner_id !== me.user_id
    )
      return;
    const timer = setTimeout(
      () => void save().catch((e) => setError(errorMessage(e))),
      800,
    );
    return () => clearTimeout(timer);
  }, [body, save, detail?.lead.owner_id, me.user_id]);
  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (latest.current !== saved.current) {
        e.preventDefault();
      }
    };
    window.addEventListener("beforeunload", warn);
    return () => {
      window.removeEventListener("beforeunload", warn);
      if (initialized.current && latest.current !== saved.current)
        void save().catch((e) => toast(errorMessage(e), "error"));
    };
  }, [save]);
  async function navigate(next = false) {
    try {
      await save();
      if (next) await onNext();
      else onBack();
    } catch (e) {
      setError(errorMessage(e));
    }
  }
  function editBody(value: string) {
    latest.current = value;
    setBody(value);
  }
  function start(kind: typeof dialog) {
    if (!detail) return;
    setDialog(kind);
    setNote(detail.lead.note);
    setAmount(
      detail.lead.amount_minor == null
        ? ""
        : (detail.lead.amount_minor / 100).toFixed(2),
    );
    setReason("");
    setError("");
    track("ui.sales_dialog_opened", { kind });
  }
  function parsedAmount() {
    if (!amount.trim()) return null;
    if (!/^\d+(\.\d{1,2})?$/.test(amount))
      throw {
        error: {
          message:
            "Enter a nonnegative amount with up to two decimal places, or leave it blank.",
        },
      };
    const value = Math.round(Number(amount) * 100);
    if (!Number.isSafeInteger(value))
      throw { error: { message: "Amount is too large." } };
    return value;
  }
  async function action(
    kind: Action["action"],
    next = false,
    messageID?: string,
  ) {
    if (!detail || busy) return;
    setBusy(true);
    setError("");
    try {
      if (detail.lead.owner_id === me.user_id) await save();
      const input: Action = {
        action: kind,
        version: detail.lead.version,
        message_id: messageID,
      };
      if (kind === "details") {
        input.note = note;
        input.amount_minor = parsedAmount();
        input.currency = detail.lead.currency;
      }
      if (kind === "confirm_customer") input.amount_minor = parsedAmount();
      if (kind === "archive") input.reason = reason;
      if (kind === "handoff") {
        input.assignee_id = target;
        input.body = intro;
      }
      checked(
        await api.POST("/v1/brands/{brand}/sales/leads/{id}/actions", {
          params: { path },
          body: input,
        }),
      );
      setDialog(undefined);
      await load();
      if (next) await onNext();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function send() {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const d = await save();
      if (!d || !d.body.trim()) return;
      checked(
        await api.POST("/v1/brands/{brand}/sales/leads/{id}/send", {
          params: { path },
          body: { draft_id: d.id },
        }),
      );
      editBody("");
      await save();
      await load();
      toast("Reply queued. Its provider status appears in the conversation.");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function apply(templateID: string) {
    if (!templateID || busy) return;
    if (
      latest.current.trim() &&
      !(await confirmAction(
        "Replace this draft?",
        "The current saved revision stays in the conversation record.",
        "Replace draft",
      ))
    )
      return;
    setBusy(true);
    try {
      await save(templateID);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function saveTemplate() {
    setBusy(true);
    try {
      checked(
        await api.POST("/v1/brands/{brand}/sales/templates", {
          params: { path: { brand } },
          body: { name: templateName, body },
        }),
      );
      await onLibrary();
      setDialog(undefined);
      toast("Personal template saved. Your draft is unchanged.");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  if (!detail)
    return (
      <>
        <ErrorText>{error}</ErrorText>
        {!error && <Loading />}
      </>
    );
  const l = detail.lead;
  const owned = l.owner_id === me.user_id;
  const owner = team.find((t) => t.user_id === l.owner_id);
  const pending = detail.messages.some((m) =>
    ["queued", "sending", "uncertain"].includes(m.status ?? ""),
  );
  return (
    <article className="mx-auto max-w-4xl space-y-6">
      <div className="flex justify-between">
        <button
          className="flex items-center gap-2 text-xs text-muted-foreground"
          onClick={() => void navigate()}
        >
          <ArrowLeft className="size-3.5" /> {backLabel}
        </button>
        <Button
          variant="ghost"
          disabled={busy || saving}
          onClick={() => void navigate(true)}
        >
          Next lead <ArrowRight className="size-3.5" />
        </Button>
      </div>
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <p className="mb-1 text-xs text-muted-foreground">
            {stateName(l.state)} ·{" "}
            {l.reply_state === "reply_received"
              ? "Reply received"
              : "Waiting for reply"}
          </p>
          <h2 className="text-3xl font-semibold tracking-tight">
            {l.company || l.name}
          </h2>
          <p className="mt-2 text-sm text-muted-foreground">
            {l.company && `${l.name} · `}
            <a href={`mailto:${l.email}`} className="hover:underline">
              {l.email}
            </a>
          </p>
          {l.job_title && (
            <p className="mt-1 text-sm text-muted-foreground">{l.job_title}</p>
          )}
          <div className="mt-3">
            <SalesSources lead={l} />
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          {l.state === "new" && ["sdr", "ae"].includes(me.role) && (
            <Button
              disabled={busy || !me.connected}
              onClick={() => void action("take")}
            >
              Take lead
            </Button>
          )}
          {owned && l.state === "active" && me.role === "ae" && (
            <Button
              disabled={busy || pending}
              onClick={() => start("confirm_customer")}
            >
              Confirm customer
            </Button>
          )}
          {owned && l.state === "active" && me.role === "sdr" && (
            <Button disabled={busy || pending} onClick={() => start("handoff")}>
              Introduce an AE
            </Button>
          )}
          {owned && ["archived", "customer"].includes(l.state) && (
            <Button disabled={busy} onClick={() => void action("reopen")}>
              Reopen lead
            </Button>
          )}
          {(owned || (l.state === "new" && ["sdr", "ae"].includes(me.role))) &&
            l.state !== "archived" && (
              <Button
                variant="ghost"
                disabled={busy || pending}
                onClick={() => start("archive")}
              >
                Archive
              </Button>
            )}
        </div>
      </header>
      <div className="space-y-2 border-y py-4 text-sm text-muted-foreground">
        <p>
          Owner:{" "}
          <span className="text-foreground">
            {owner?.name ?? "Available to take"}
          </span>
          {l.sdr_id && l.sdr_id !== l.owner_id
            ? ` · Introduced by ${team.find((t) => t.user_id === l.sdr_id)?.name ?? "SDR"}`
            : ""}
        </p>
        {l.introduced_at && (
          <p>
            Introduction sent {date(l.introduced_at)} · Current owner included
            on CC.
          </p>
        )}
        {l.confirmed_at && (
          <p>
            Customer confirmed by{" "}
            {team.find((t) => t.user_id === l.confirmed_by)?.name ??
              "account manager"}{" "}
            · {date(l.confirmed_at)}. Payment is recorded separately.
          </p>
        )}
        {!me.connected && ["sdr", "ae"].includes(me.role) && (
          <p>
            Connect your own email in My email before taking a lead or sending.
          </p>
        )}
      </div>
      <ErrorText>{error}</ErrorText>
      <details className="rounded-xl border bg-card p-4">
        <summary className="cursor-pointer text-sm font-medium">
          Lead details
        </summary>
        <div className="mt-4 space-y-4">
          <div>
            <h3 className="text-xs text-muted-foreground">Shared note</h3>
            <p className="mt-2 whitespace-pre-wrap text-sm">
              {l.note || "No shared note yet."}
            </p>
          </div>
          <p className="text-sm text-muted-foreground">
            Expected monthly revenue:{" "}
            {l.amount_minor == null
              ? "Not entered"
              : `${l.currency} ${(l.amount_minor / 100).toFixed(2)}`}{" "}
            · Optional estimate
          </p>
          <Button variant="ghost" onClick={() => start("details")}>
            Edit details
          </Button>
        </div>
      </details>
      <section aria-label="Conversation" className="space-y-3">
        <h3 className="text-sm font-medium">Conversation</h3>
        {detail.messages.map((m, index) => (
          <div
            key={m.id ?? index}
            className={`rounded-xl border p-5 ${m.direction === "incoming" ? "border-l-2 border-l-emerald-500/50 bg-card" : "bg-muted/20"}`}
          >
            <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
              <span className="font-medium text-foreground">{m.from}</span>
              <span>{date(m.occurred_at)}</span>
            </div>
            <p className="mt-2 text-xs text-muted-foreground">
              To: {m.to.join(", ")}
              {m.cc?.length ? ` · CC: ${m.cc.join(", ")}` : ""}
            </p>
            <p className="mt-3 text-sm font-medium">{m.subject}</p>
            <p className="mt-3 whitespace-pre-wrap break-words text-sm leading-relaxed">
              {m.body}
            </p>
            <p className="mt-4 text-xs text-muted-foreground">
              {m.kind === "introduction"
                ? "Introduction · "
                : m.kind === "handoff"
                  ? "AE introduction · "
                  : ""}
              {m.status === "accepted"
                ? "Accepted by provider · delivery unconfirmed"
                : m.status === "uncertain"
                  ? "Send result uncertain. Reconciliation required; this message will not be resent automatically."
                  : m.status === "failed"
                    ? "Send failed. Review your email connection and sending policy, then retry."
                    : m.status}
            </p>
            {owned && m.status === "failed" && (
              <Button
                variant="ghost"
                disabled={busy || pending || !me.connected}
                onClick={() => void action("retry_send", false, m.id)}
              >
                Retry send
              </Button>
            )}
          </div>
        ))}
      </section>
      {owned && ["active", "customer"].includes(l.state) && (
        <section className="space-y-4 rounded-xl border border-emerald-500/30 bg-card p-5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h3 className="text-sm font-medium">Reply</h3>
            <select
              aria-label="Apply a personal template"
              className={field}
              value=""
              disabled={busy}
              onChange={(e) => void apply(e.target.value)}
            >
              <option value="">Apply a template…</option>
              {templates.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </div>
          <textarea
            aria-label="Reply message"
            disabled={busy}
            className={`${field} min-h-48 w-full`}
            value={body}
            maxLength={20000}
            onChange={(e) => editBody(e.target.value)}
            onBlur={() => void save().catch((e) => setError(errorMessage(e)))}
            placeholder="Write your reply…"
          />
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span role="status" className="text-xs text-muted-foreground">
              {saving
                ? "Saving draft…"
                : body === saved.current
                  ? "Draft saved"
                  : "Unsaved changes"}
            </span>
            <div className="flex gap-2">
              <Button
                variant="ghost"
                disabled={busy || !body.trim()}
                onClick={() => start("template")}
              >
                Save as template
              </Button>
              <Button
                disabled={busy || !me.connected || !body.trim() || pending}
                onClick={() => void send()}
              >
                Send reply
              </Button>
            </div>
          </div>
        </section>
      )}
      {manager && detail.events.length > 0 && (
        <details className="border-t pt-4">
          <summary className="cursor-pointer text-sm text-muted-foreground">
            Manager activity
          </summary>
          <ol className="mt-4 space-y-2">
            {detail.events.map((e) => (
              <li
                key={e.id}
                className="flex justify-between gap-4 text-xs text-muted-foreground"
              >
                <span>
                  {e.action.replaceAll("_", " ")} ·{" "}
                  {team.find((t) => t.user_id === e.actor_id)?.name ??
                    e.actor_role}
                </span>
                <time>{date(e.occurred_at)}</time>
              </li>
            ))}
          </ol>
        </details>
      )}
      {dialog && (
        <Dialog open onOpenChange={(v) => !v && !busy && setDialog(undefined)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>
                {
                  {
                    archive: "Archive lead",
                    confirm_customer: "Confirm customer",
                    handoff: "Introduce an account manager",
                    details: "Shared lead details",
                    template: "Save as personal template",
                  }[dialog]
                }
              </DialogTitle>
              <DialogDescription>
                {dialog === "confirm_customer"
                  ? "Confirm that the customer agreed to proceed. No payment is required or recorded."
                  : dialog === "handoff"
                    ? "Your introduction goes in the existing thread, with the account manager on CC. Ownership transfers after provider acceptance."
                    : dialog === "template"
                      ? "Save reusable wording. Your current reply remains editable and is not sent."
                      : "Notes, drafts and the conversation stay with this lead."}
              </DialogDescription>
            </DialogHeader>
            {dialog === "archive" && (
              <label className="text-sm">
                Reason (optional)
                <textarea
                  className={`${field} mt-2 w-full`}
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                  maxLength={1000}
                />
              </label>
            )}
            {(dialog === "details" || dialog === "confirm_customer") && (
              <>
                {dialog === "details" && (
                  <label className="text-sm">
                    Shared note
                    <textarea
                      rows={5}
                      className={`${field} mt-2 w-full`}
                      value={note}
                      onChange={(e) => setNote(e.target.value)}
                      maxLength={20000}
                    />
                  </label>
                )}
                <label className="text-sm">
                  Expected monthly revenue ({l.currency}, optional)
                  <input
                    inputMode="decimal"
                    className={`${field} mt-2 w-full`}
                    value={amount}
                    placeholder="Leave blank if unknown"
                    onChange={(e) => setAmount(e.target.value)}
                  />
                </label>
              </>
            )}
            {dialog === "handoff" && (
              <>
                <label className="text-sm">
                  Account manager
                  <select
                    className={`${field} mt-2 w-full`}
                    value={target}
                    onChange={(e) => {
                      setTarget(e.target.value);
                      const t = team.find((m) => m.user_id === e.target.value);
                      setIntro(
                        t
                          ? `Hi ${l.name.split(" ")[0]},\n\nI'm introducing ${t.name}${t.introduction ? `, ${t.introduction}` : ""}. I've included them on CC so you can continue here.`
                          : "",
                      );
                    }}
                  >
                    <option value="">Choose a colleague…</option>
                    {team
                      .filter((t) => t.role === "ae")
                      .map((t) => (
                        <option
                          key={t.user_id}
                          value={t.user_id}
                          disabled={!t.connected}
                        >
                          {t.name} · {t.open_leads} active
                          {!t.connected ? " · email not connected" : ""}
                        </option>
                      ))}
                  </select>
                </label>
                <textarea
                  aria-label="Introduction email"
                  rows={6}
                  className={`${field} w-full`}
                  value={intro}
                  onChange={(e) => setIntro(e.target.value)}
                />
              </>
            )}
            {dialog === "template" && (
              <label className="text-sm">
                Template name
                <input
                  className={`${field} mt-2 w-full`}
                  value={templateName}
                  onChange={(e) => setTemplateName(e.target.value)}
                  maxLength={100}
                />
              </label>
            )}
            <ErrorText>{error}</ErrorText>
            <DialogFooter>
              {["archive", "confirm_customer"].includes(dialog) && (
                <Button
                  variant="ghost"
                  disabled={busy}
                  onClick={() => void action(dialog as Action["action"], true)}
                >
                  {dialog === "archive"
                    ? "Archive & next lead"
                    : "Confirm & next lead"}
                </Button>
              )}
              <Button
                disabled={
                  busy ||
                  (dialog === "handoff" && (!target || !intro.trim())) ||
                  (dialog === "template" && !templateName.trim())
                }
                onClick={() =>
                  dialog === "template"
                    ? void saveTemplate()
                    : void action(dialog)
                }
              >
                {
                  {
                    archive: "Archive",
                    confirm_customer: "Confirm customer",
                    handoff: "Send introduction",
                    details: "Save details",
                    template: "Save template",
                  }[dialog]
                }
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </article>
  );
}
