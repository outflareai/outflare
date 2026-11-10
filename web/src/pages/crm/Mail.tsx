import { useCallback, useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { LockKeyhole, Mail, ShieldCheck, Upload, Trash2 } from "lucide-react";
import { api, errorMessage } from "@/api";
import {
  Button,
  Empty,
  ErrorText,
  Loading,
  field,
  toast,
  confirmAction,
} from "@/ui";
import { useBrand } from "@/lib/brand";
import { stamp } from "@/format";
import { MemberPicker } from "../RecordEdit";
import { GoogleMailSetup, GoogleCaptureControl } from "./GoogleMail";

type Policy = Schemas["CRMMailPolicy"];
type Preferences = Schemas["CRMMailPreferences"];
type Sharing = Schemas["CRMMailSharing"];
type Message = Schemas["CRMMailMessage"];
const values = (raw: string) =>
  raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
const DEFAULT_LABELS = ["Personal", "HR", "Legal", "Finance", "Medical"];

// What team sharing means for you, in plain words, so nobody's mail is
// shared without them knowing.
function TeamSharingNotice({ on, from }: { on: boolean; from: string | null }) {
  if (!on || !from) return null;
  const starts = new Date(from);
  const started = starts.getTime() <= Date.now();
  return (
    <div className="space-y-1 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
      <p className="font-medium">
        {started
          ? "Team email sharing is on for this brand"
          : `Team email sharing starts ${stamp(from)}`}
      </p>
      <p className="text-muted-foreground">
        Your work email with CRM contacts {started ? "is" : "will be"} readable
        by everyone on this brand. To keep a contact&rsquo;s email to yourself,
        open it and set its email to private. Mail labelled Personal, HR, Legal,
        Finance or Medical, and payroll, HR and banking mail, is never saved.
      </p>
    </div>
  );
}

const privateRule = (): Sharing => ({
  visibility: "private",
  audience: "selected",
  viewer_ids: [],
  version: 0,
});

export function MailPrivacy({
  ws,
  canManage,
  canManagePolicy,
}: {
  ws: string;
  canManage: boolean;
  canManagePolicy: boolean;
}) {
  const { brand } = useBrand(ws);
  const [personal, setPersonal] = useState<Preferences | null>(null);
  const [policy, setPolicy] = useState<Policy | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!brand) return;
    const c = new AbortController();
    setPersonal(null);
    setPolicy(null);
    setError("");
    void (async () => {
      try {
        const own = await api.GET("/v1/brands/{brand}/crm/mail/preferences", {
          params: { path: { brand: brand.id } },
          signal: c.signal,
        });
        if (c.signal.aborted) return;
        if (!own.data) {
          setError(errorMessage(own.error));
          return;
        }
        setPersonal(own.data);
        if (canManagePolicy) {
          const result = await api.GET("/v1/brands/{brand}/crm/mail/policy", {
            params: { path: { brand: brand.id } },
            signal: c.signal,
          });
          if (c.signal.aborted) return;
          if (result.data) setPolicy(result.data);
          else setError(errorMessage(result.error));
        }
      } catch (e) {
        if (!c.signal.aborted) setError(errorMessage(e));
      }
    })();
    return () => c.abort();
  }, [brand?.id, canManagePolicy]);
  async function save(global: boolean) {
    if (!brand || busy) return;
    setBusy(true);
    setError("");
    try {
      if (global && policy) {
        const result = await api.PUT("/v1/brands/{brand}/crm/mail/policy", {
          params: { path: { brand: brand.id } },
          body: policy,
        });
        if (result.data) setPolicy(result.data);
        else {
          setError(errorMessage(result.error));
          return;
        }
      } else if (personal) {
        const result = await api.PUT(
          "/v1/brands/{brand}/crm/mail/preferences",
          { params: { path: { brand: brand.id } }, body: personal },
        );
        if (result.data) setPersonal(result.data);
        else {
          setError(errorMessage(result.error));
          return;
        }
      }
      toast(
        "Exclusions saved. They also apply to previously imported CRM mail.",
      );
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function clearMail() {
    if (
      !brand ||
      busy ||
      !(await confirmAction(
        "Delete all my imported email copies?",
        "This removes every personal CRM email copy you imported in this brand, including copies hidden by exclusions. Your original mailbox is unaffected. Sharing preferences are kept.",
        "Delete my copies",
      ))
    )
      return;
    setBusy(true);
    setError("");
    try {
      const r = await api.DELETE("/v1/brands/{brand}/crm/mail/messages", {
        params: { path: { brand: brand.id } },
      });
      if (r.data)
        toast(`${r.data.deleted_count} imported email copies deleted.`);
      else setError(errorMessage(r.error));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <GoogleMailSetup
        ws={ws}
        canManage={canManage}
        canManagePolicy={canManagePolicy}
      />
      <section className="space-y-4 rounded-xl border bg-card p-5">
        <header className="flex items-center gap-2 font-semibold">
          <ShieldCheck className="size-4" /> Email privacy
        </header>
        <TeamSharingNotice
          on={!!personal?.team_sharing}
          from={personal?.team_sharing_from ?? null}
        />
        {personal?.capture_all_contacts && (
          <p className="max-w-3xl rounded-lg border border-primary/30 bg-primary/5 p-3 text-sm">
            This brand captures your work email with every CRM contact and
            company, once you connect Gmail.
          </p>
        )}
        {!personal?.team_sharing && (
          <p className="max-w-3xl text-sm text-muted-foreground">
            Your personal mail is private until you share it on a CRM record.
            Managers, admins and HR get no automatic access. Choose specific
            people, including HR when appropriate, or share customer
            correspondence with the brand.
          </p>
        )}
        <div className="rounded-lg border bg-muted/30 p-3 text-xs text-muted-foreground">
          Internal and mixed internal/customer conversations are excluded.
          Workspace members’ exact addresses are always treated as internal.
          Brand domains include their subdomains. Exclusions take precedence
          over every sharing rule. Shared outreach inboxes have their existing
          team access; these controls apply to personal CRM mail.
        </div>
        <ErrorText>{error}</ErrorText>
        {!personal && !error && <Loading />}
        {personal && (
          <div className="space-y-3">
            <h3 className="text-sm font-medium">My exclusions</h3>
            <RuleInput
              label="Never capture these email addresses"
              value={personal.blocked_addresses}
              disabled={!canManage || busy}
              onChange={(v) =>
                setPersonal({ ...personal, blocked_addresses: v })
              }
            />
            <RuleInput
              label="Never capture these domains"
              value={personal.blocked_domains}
              disabled={!canManage || busy}
              onChange={(v) => setPersonal({ ...personal, blocked_domains: v })}
            />
            <RuleInput
              label="Never capture mail with these Gmail labels (on top of the brand's)"
              value={personal.private_labels ?? []}
              disabled={!canManage || busy}
              onChange={(v) => setPersonal({ ...personal, private_labels: v })}
            />
            {canManage && (
              <Button disabled={busy} onClick={() => void save(false)}>
                Save my exclusions
              </Button>
            )}
          </div>
        )}
        {canManage && (
          <div className="space-y-2 border-t pt-4">
            <p className="text-xs text-muted-foreground">
              Exclusions hide older imports but keep the encrypted copies until
              you delete them. You can remove all your imported copies here,
              including hidden ones. Original mailbox messages are unaffected.
            </p>
            <Button
              disabled={busy}
              variant="ghost"
              onClick={() => void clearMail()}
            >
              <Trash2 className="size-3.5" />
              Delete my imported email copies
            </Button>
          </div>
        )}
        {policy && canManagePolicy && (
          <div className="space-y-3 border-t pt-4">
            <div className="space-y-2 rounded-lg border p-3">
              <label className="flex items-start gap-3 text-sm">
                <input
                  id="team-sharing"
                  type="checkbox"
                  className="mt-1"
                  checked={policy.team_sharing}
                  disabled={busy}
                  onChange={(e) =>
                    setPolicy({ ...policy, team_sharing: e.target.checked })
                  }
                />
                <span className="space-y-1">
                  <span className="block font-medium">Team email sharing</span>
                  <span className="block text-xs text-muted-foreground">
                    Everyone on this brand can read each other&rsquo;s work
                    email with CRM contacts, so the history with a customer,
                    partner or supplier isn&rsquo;t stuck in one inbox. It
                    starts 48 hours after you save, and every member is told
                    now. People can still mark a contact private. Mail with the
                    Gmail labels below, and from payroll, HR, benefits, banking
                    and expense services, is never saved. Each time someone
                    reads a colleague&rsquo;s email it&rsquo;s recorded.
                  </span>
                </span>
              </label>
              <label className="flex items-start gap-3 border-t pt-3 text-sm">
                <input
                  id="capture-all"
                  type="checkbox"
                  className="mt-1"
                  checked={!!policy.capture_all_contacts}
                  disabled={busy}
                  onChange={(e) =>
                    setPolicy({
                      ...policy,
                      capture_all_contacts: e.target.checked,
                    })
                  }
                />
                <span className="space-y-1">
                  <span className="block font-medium">
                    Capture email with every CRM contact
                  </span>
                  <span className="block text-xs text-muted-foreground">
                    Each person enabled for Gmail capture saves their email with
                    any contact or company in this brand&rsquo;s CRM, not only
                    the ones they picked. An email is saved only when everyone
                    outside the company in it belongs to one contact, or one
                    company. Newsletters and notifications are skipped, and
                    every rule here still applies.
                  </span>
                </span>
              </label>
              <RuleInput
                label="Never capture mail with these Gmail labels"
                value={policy.private_labels ?? DEFAULT_LABELS}
                disabled={busy}
                onChange={(v) => setPolicy({ ...policy, private_labels: v })}
              />
            </div>
            <h3 className="text-sm font-medium">Brand safeguards</h3>
            <p className="text-xs text-muted-foreground">
              These rules apply to everyone in this brand. Editing them does not
              let you read anyone’s personal mail.
            </p>
            <RuleInput
              label="Internal company domains"
              value={policy.internal_domains}
              disabled={busy}
              onChange={(v) => setPolicy({ ...policy, internal_domains: v })}
            />
            <RuleInput
              label="Excluded addresses for this brand"
              value={policy.blocked_addresses}
              disabled={busy}
              onChange={(v) => setPolicy({ ...policy, blocked_addresses: v })}
            />
            <RuleInput
              label="Excluded domains for this brand"
              value={policy.blocked_domains}
              disabled={busy}
              onChange={(v) => setPolicy({ ...policy, blocked_domains: v })}
            />
            <Button disabled={busy} onClick={() => void save(true)}>
              Save brand safeguards
            </Button>
          </div>
        )}
      </section>
    </>
  );
}
function RuleInput({
  label,
  value,
  disabled,
  onChange,
}: {
  label: string;
  value: string[];
  disabled: boolean;
  onChange: (v: string[]) => void;
}) {
  // Keep free text while editing so separators/newlines do not disappear mid-entry.
  const [text, setText] = useState(value.join("\n"));
  const previous = useRef(value.join("\n"));
  useEffect(() => {
    const next = value.join("\n");
    if (next !== previous.current) {
      setText(next);
      previous.current = next;
    }
  }, [value]);
  return (
    <label className="block text-xs font-medium">
      {label}
      <textarea
        aria-label={label}
        className={`${field} mt-1 block min-h-20 w-full font-mono text-xs`}
        disabled={disabled}
        placeholder="One address or domain per line"
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          const parsed = values(e.target.value);
          previous.current = parsed.join("\n");
          onChange(parsed);
        }}
      />
      <span className="mt-1 block font-normal text-muted-foreground">
        One per line or separated by commas. Use plain domains without
        wildcards.
      </span>
    </label>
  );
}

export function RecordMail({
  ws,
  record,
  recordType,
  myId,
  canManage,
}: {
  ws: string;
  record: string;
  recordType: string;
  myId?: string;
  canManage: boolean;
}) {
  const { brand } = useBrand(ws);
  const [messages, setMessages] = useState<Message[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [sharing, setSharing] = useState<Sharing>(privateRule);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const request = useRef(0);
  const load = useCallback(
    async (before?: string, signal?: AbortSignal) => {
      if (!brand) return;
      const version = ++request.current;
      setLoading(true);
      setError("");
      try {
        const result = await api.GET(
          "/v1/brands/{brand}/crm/mail/records/{record}/messages",
          {
            params: {
              path: { brand: brand.id, record },
              query: { cursor: before, limit: 25 },
            },
            signal,
          },
        );
        if (signal?.aborted || version !== request.current) return;
        if (!result.data) {
          setError(errorMessage(result.error));
          return;
        }
        setMessages((prev) =>
          before ? [...prev, ...result.data!.data] : result.data!.data,
        );
        setNext(result.data.next_cursor ?? null);
      } catch (e) {
        if (!signal?.aborted && version === request.current)
          setError(errorMessage(e));
      } finally {
        if (!signal?.aborted && version === request.current) setLoading(false);
      }
    },
    [brand?.id, record],
  );
  useEffect(() => {
    const c = new AbortController();
    setMessages([]);
    setSharing(privateRule());
    setNext(null);
    void load(undefined, c.signal);
    if (brand)
      void api
        .GET("/v1/brands/{brand}/crm/mail/records/{record}/sharing", {
          params: { path: { brand: brand.id, record } },
          signal: c.signal,
        })
        .then((r) => {
          if (c.signal.aborted) return;
          if (r.data) setSharing(r.data);
          else setError(errorMessage(r.error));
        })
        .catch((e) => {
          if (!c.signal.aborted) setError(errorMessage(e));
        });
    return () => {
      c.abort();
      request.current++;
    };
  }, [load, brand?.id, record]);
  async function saveSharing() {
    if (!brand || busy) return;
    setBusy(true);
    setError("");
    try {
      const result = await api.PUT(
        "/v1/brands/{brand}/crm/mail/records/{record}/sharing",
        { params: { path: { brand: brand.id, record } }, body: sharing },
      );
      if (result.data) {
        setSharing(result.data);
        toast("Sharing updated for your mail on this record.");
        void load();
      } else setError(errorMessage(result.error));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function importFile(file?: File) {
    if (!file || !brand || busy) return;
    if (file.size > 2 * 1024 * 1024) {
      setError("Choose an email smaller than 2 MiB.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const raw = await file.text();
      const result = await api.POST(
        "/v1/brands/{brand}/crm/mail/records/{record}/messages",
        { params: { path: { brand: brand.id, record } }, body: { raw } },
      );
      if (result.data) {
        toast(
          result.data.stored
            ? "Email imported after privacy checks."
            : "This email was already imported.",
        );
        await load();
      } else setError(errorMessage(result.error));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
      if (input.current) input.current.value = "";
    }
  }
  async function remove(id: string) {
    if (
      !brand ||
      busy ||
      !(await confirmAction(
        "Delete this imported email?",
        "The imported copy will be removed from the CRM. The original email stays in your mailbox.",
        "Delete email",
      ))
    )
      return;
    setBusy(true);
    try {
      const r = await api.DELETE("/v1/brands/{brand}/crm/mail/messages/{id}", {
        params: { path: { brand: brand.id, id } },
      });
      if (r.error) setError(errorMessage(r.error));
      else {
        toast("Email deleted.");
        await load();
      }
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="space-y-5">
      <div className="rounded-lg border bg-muted/30 p-3 text-xs text-muted-foreground">
        <LockKeyhole className="mb-2 size-4" />
        Personal CRM mail is private by default. Only messages you can access
        appear here. Internal participants and exclusions block capture and
        sharing. Approved sales members can connect Google in CRM settings and
        select records for automatic capture. You can also import an individual
        email from your own account.
      </div>
      {["person", "account"].includes(recordType) ? (
        <GoogleCaptureControl ws={ws} record={record} canManage={canManage} />
      ) : (
        <p className="text-xs text-muted-foreground">
          Select a linked contact or company to configure automatic Google
          capture.
        </p>
      )}
      {canManage && (
        <details className="rounded-xl border p-4">
          <summary className="cursor-pointer text-sm font-medium">
            Share my correspondence on this record
          </summary>
          <div className="mt-4 space-y-3">
            <label className="block text-xs">
              Visibility
              <select
                aria-label="My email visibility"
                className={`${field} mt-1 block w-full`}
                value={sharing.visibility}
                onChange={(e) =>
                  setSharing({
                    ...sharing,
                    visibility: e.target.value as Sharing["visibility"],
                  })
                }
                disabled={busy}
              >
                <option value="private">Only me</option>
                <option value="metadata">
                  Activity only — no subject, body or participants
                </option>
                <option value="full">Full email content</option>
              </select>
            </label>
            {sharing.visibility !== "private" && (
              <>
                <label className="block text-xs">
                  Who can see it?
                  <select
                    aria-label="My email audience"
                    className={`${field} mt-1 block w-full`}
                    value={sharing.audience}
                    disabled={busy}
                    onChange={(e) =>
                      setSharing({
                        ...sharing,
                        audience: e.target.value as Sharing["audience"],
                      })
                    }
                  >
                    <option value="selected">Specific people</option>
                    <option value="brand">
                      People who can access this brand’s CRM
                    </option>
                  </select>
                </label>
                {sharing.audience === "selected" && (
                  <div className="text-xs">
                    <label htmlFor="mail-viewers" className="mb-1 block">
                      Approved people
                    </label>
                    <MemberPicker
                      ws={ws}
                      id="mail-viewers"
                      value={sharing.viewer_ids}
                      multiple
                      onChange={(v) =>
                        setSharing({
                          ...sharing,
                          viewer_ids: Array.isArray(v) ? (v as string[]) : [],
                        })
                      }
                    />
                  </div>
                )}
                <p className="text-xs text-muted-foreground">
                  This applies to your existing and future imported emails on
                  this record. Full sharing may expose quoted content.
                  Exclusions always win. Other people keep control of their own
                  correspondence.
                </p>
              </>
            )}
            <Button disabled={busy} onClick={() => void saveSharing()}>
              Save email sharing
            </Button>
          </div>
        </details>
      )}
      <ErrorText>{error}</ErrorText>
      {canManage && (
        <div>
          <input
            ref={input}
            type="file"
            accept=".eml,message/rfc822"
            className="hidden"
            aria-label="Import my email file"
            onChange={(e) => void importFile(e.target.files?.[0])}
          />
          <Button disabled={busy} onClick={() => input.current?.click()}>
            <Upload className="size-3.5" />
            {busy ? "Working…" : "Import my email (.eml)"}
          </Button>
          <p className="mt-2 text-xs text-muted-foreground">
            Your signed-in address must be a participant. All other participants
            must match this contact’s email or this company’s domain.
            Attachments are not retained.
          </p>
        </div>
      )}
      {loading && !messages.length ? (
        <Loading />
      ) : !messages.length && !error ? (
        <Empty title="No email available to you">
          <Mail className="mx-auto mb-2 size-5" />
          Import a customer email, or ask its owner to share their
          correspondence on this record.
        </Empty>
      ) : null}
      <div className="space-y-3">
        {messages.map((m) => (
          <article key={m.id} className="rounded-xl border p-4">
            <header className="flex items-start justify-between gap-3">
              <div>
                <h3 className="text-sm font-semibold">
                  {m.visibility === "metadata"
                    ? "Email activity"
                    : m.subject || "(No subject)"}
                </h3>
                <p className="mt-1 text-xs text-muted-foreground">
                  {stamp(m.occurred_at)}
                  {m.owner_id === myId
                    ? " · Your email"
                    : " · Shared by its owner"}
                </p>
              </div>
              {m.owner_id === myId && canManage && (
                <button
                  disabled={busy}
                  aria-label="Delete my imported email"
                  className="rounded p-1 hover:bg-muted"
                  onClick={() => void remove(m.id)}
                >
                  <Trash2 className="size-3.5" />
                </button>
              )}
            </header>
            {m.visibility === "metadata" ? (
              <p className="mt-3 text-xs text-muted-foreground">
                The owner shared activity only. Subject, content and
                participants are private.
              </p>
            ) : (
              <>
                <p className="mt-3 break-words text-xs text-muted-foreground">
                  {m.participants?.join(" · ")}
                </p>
                <p className="mt-4 whitespace-pre-wrap break-words text-sm">
                  {m.body}
                </p>
              </>
            )}
          </article>
        ))}
      </div>
      {next && (
        <Button
          disabled={loading}
          variant="ghost"
          onClick={() => void load(next)}
        >
          Show more emails
        </Button>
      )}
    </div>
  );
}
