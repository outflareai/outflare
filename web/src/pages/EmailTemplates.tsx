// Email templates: the receipts, resets and onboarding emails your app
// sends by handle. Editing changes a draft; publishing makes it the version
// sends use, and every published version is kept.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { EmailPreview, EmailTemplate, EmailTemplateInput, EmailTemplateVariable, EmailTemplateVersion, EmailTopic } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { track } from "@/lib/analytics";
import { cn } from "@/lib/utils";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { SendSnippet, type Go } from "./Lifecycle";

// ---- Starters ----

const shell = (body: string) => `<!doctype html>
<html>
  <body style="margin:0;background:#f4f4f5;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;color:#18181b">
    <div style="max-width:560px;margin:0 auto;padding:32px 16px">
      <div style="background:#ffffff;border-radius:8px;padding:32px">
${body}
      </div>
      <p style="font-size:12px;color:#71717a;text-align:center;margin-top:16px">{{company|Your company}} · You get this email because you have an account with us.</p>
    </div>
  </body>
</html>`;

const button = (href: string, label: string) => `        <p style="margin:24px 0"><a href="${href}" style="background:#18181b;color:#ffffff;text-decoration:none;padding:10px 18px;border-radius:6px;font-weight:600;display:inline-block">${label}</a></p>`;

type Starter = { id: string; name: string; blurb: string; template: EmailTemplateInput };

const starters: Starter[] = [
  {
    id: "blank", name: "Blank", blurb: "Start from an empty email.",
    template: { name: "New template", purpose: "transactional", subject: "", html: shell("        <p>Hi {{first_name|there}},</p>\n        <p></p>"), variables: [{ name: "first_name", example: "Ana" }] },
  },
  {
    id: "password-reset", name: "Password reset", blurb: "A link to choose a new password.",
    template: {
      name: "Password reset", handle: "password-reset", purpose: "transactional", subject: "Reset your password",
      html: shell(`        <h1 style="font-size:20px;margin:0 0 16px">Reset your password</h1>
        <p>Hi {{first_name|there}}, someone asked to reset the password for your account. If it was you, choose a new one:</p>
${button("{{reset_url}}", "Choose a new password")}
        <p style="font-size:13px;color:#52525b">The link works for {{expires_in|1 hour}}. If you did not ask for this, ignore this email: your password stays the same.</p>`),
      variables: [{ name: "first_name", example: "Ana" }, { name: "reset_url", required: true, example: "https://app.example.com/reset/abc123" }, { name: "expires_in", default: "1 hour" }],
    },
  },
  {
    id: "sign-in-code", name: "Sign-in code", blurb: "A one-time code to sign in.",
    template: {
      name: "Sign-in code", handle: "sign-in-code", purpose: "transactional", subject: "{{code}} is your sign-in code",
      html: shell(`        <p>Hi {{first_name|there}}, here is your code to sign in:</p>
        <p style="font-size:28px;font-weight:700;letter-spacing:6px;font-family:ui-monospace,Menlo,monospace;margin:24px 0">{{code}}</p>
        <p style="font-size:13px;color:#52525b">It works for 10 minutes. If you did not try to sign in, you can ignore this email.</p>`),
      variables: [{ name: "first_name", example: "Ana" }, { name: "code", required: true, example: "482913" }],
    },
  },
  {
    id: "receipt", name: "Receipt", blurb: "What someone paid, and for what.",
    template: {
      name: "Receipt", handle: "receipt", purpose: "transactional", subject: "Your receipt for {{amount}}",
      html: shell(`        <h1 style="font-size:20px;margin:0 0 16px">Thanks for your payment</h1>
        <p>Hi {{first_name|there}}, we received {{amount}} for {{plan}} on {{date}}.</p>
        <table style="width:100%;border-collapse:collapse;margin:24px 0;font-size:14px">
          <tr><td style="padding:8px 0;color:#52525b">Receipt</td><td style="padding:8px 0;text-align:right">{{receipt_number}}</td></tr>
          <tr><td style="padding:8px 0;color:#52525b;border-top:1px solid #e4e4e7">Total</td><td style="padding:8px 0;text-align:right;border-top:1px solid #e4e4e7;font-weight:600">{{amount}}</td></tr>
        </table>
        {{#if invoice_url}}
${button("{{invoice_url}}", "Download invoice")}
        {{/if}}`),
      variables: [
        { name: "first_name", example: "Ana" }, { name: "amount", required: true, example: "$49.00" }, { name: "plan", required: true, example: "Pro, monthly" },
        { name: "date", required: true, example: "26 October 2026" }, { name: "receipt_number", required: true, example: "R-10432" }, { name: "invoice_url", example: "https://app.example.com/invoices/10432" },
      ],
    },
  },
  {
    id: "welcome", name: "Welcome", blurb: "The first email after sign-up.",
    template: {
      name: "Welcome", handle: "welcome", purpose: "lifecycle", subject: "Welcome to {{company|our product}}, {{first_name|there}}",
      html: shell(`        <h1 style="font-size:20px;margin:0 0 16px">Welcome aboard, {{first_name|there}}</h1>
        <p>Thanks for signing up. The quickest way to see what {{company|we}} can do is to finish setting up your account:</p>
${button("{{start_url}}", "Finish setting up")}
        <p>Reply to this email if you get stuck: a real person reads every reply.</p>
        <p style="font-size:12px;color:#71717a;margin-top:32px"><a href="{{unsubscribe_url}}" style="color:#71717a">Unsubscribe from tips and updates</a></p>`),
      variables: [{ name: "first_name", example: "Ana" }, { name: "company", example: "Acme" }, { name: "start_url", required: true, example: "https://app.example.com/start" }],
    },
  },
];

// Every starter's footer names the company.
for (const st of starters) {
  if (!st.template.variables?.some((v) => v.name === "company")) st.template.variables = [...(st.template.variables ?? []), { name: "company", example: "Acme" }];
}

// The first verified sending domain, to fill in a sender people can use.
function useSendingDomain(ws: string) {
  const [domain, setDomain] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/email-provider-domains", { params: { path: { ws } } }).then(({ data }) => setDomain(data?.data.find((d) => d.status === "verified")?.domain ?? ""));
  }, [ws]);
  return domain;
}

// ---- List ----

const columns: ColumnDef<EmailTemplate, unknown>[] = [
  {
    id: "name", header: "Template", enableHiding: false, accessorFn: (t) => `${t.name} ${t.handle}`,
    cell: ({ row: { original: t } }) => (
      <span className="block min-w-0">
        <span className="block truncate font-medium">{t.name}</span>
        <span className="block truncate font-mono text-[12px] text-muted-foreground">{t.handle}</span>
      </span>
    ),
  },
  {
    id: "version", header: "Published", accessorFn: (t) => t.published_version ?? 0,
    cell: ({ row: { original: t } }) => (
      <span className="inline-flex items-center gap-1.5">
        {t.published_version ? <Badge tone="green">v{t.published_version}</Badge> : <Badge tone="grey">Draft</Badge>}
        {t.published_version && t.unpublished_changes ? <Badge tone="amber">Unpublished changes</Badge> : null}
      </span>
    ),
  },
  { id: "purpose", header: "Kind", accessorFn: (t) => t.purpose, cell: ({ row: { original: t } }) => <span className="capitalize">{t.purpose}</span> },
  { id: "sent_30d", header: "Sent, 30 days", accessorFn: (t) => t.sent_30d, cell: ({ row: { original: t } }) => <span className="font-mono">{t.sent_30d.toLocaleString()}</span> },
  { id: "updated_at", header: "Edited", accessorFn: (t) => t.updated_at, cell: ({ row: { original: t } }) => <span className="whitespace-nowrap text-muted-foreground" title={stamp(t.updated_at)}>{ago(t.updated_at)}</span> },
];

export function Templates({ ws, canManage, canSend, myEmail, open, onOpen, go }: { ws: string; canManage: boolean; canSend: boolean; myEmail: string; open: string; onOpen: (item: string) => void; go: Go }) {
  const [status, setStatus] = useState<"active" | "archived">("active");
  const [templates, setTemplates] = useState<EmailTemplate[] | null>(null);
  const [starter, setStarter] = useState<Starter | null>(null);

  const { query: brandQuery } = useBrand(ws);
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/email-templates", { params: { path: { ws }, query: { status, ...brandQuery } } });
    setTemplates(data?.data ?? []);
  }, [ws, status, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
  }, [load]);

  if (open === "new") {
    if (!starter) return <StarterPicker back={() => onOpen("")} pick={setStarter} taken={new Set(templates?.map((t) => t.handle))} />;
    return <Editor key="new" ws={ws} canManage={canManage} canSend={canSend} myEmail={myEmail} starter={starter} back={() => { setStarter(null); onOpen(""); }} onCreated={(t) => { setStarter(null); load(); onOpen(t.handle); }} reload={load} go={go} />;
  }
  if (open) return <Editor key={open} ws={ws} id={open} canManage={canManage} canSend={canSend} myEmail={myEmail} back={() => { load(); onOpen(""); }} onCreated={() => {}} reload={load} go={go} onRenamed={(h) => onOpen(h)} />;
  if (!templates) return <Loading />;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Email templates"
        description="Write each email once, with variables like {{first_name}}, and send it from your app by its handle. Edits stay a draft until you publish, so a half-finished change never reaches customers."
        action={canManage && <Button onClick={() => onOpen("new")}>New template</Button>}
      />
      {templates.length === 0 && status === "active" ? (
        canManage ? (
          <div className="space-y-3">
            <p className="text-[13px] text-muted-foreground">Start from one of these. Each is a plain, well-tested layout you can change.</p>
            <StarterGrid pick={(s) => { setStarter(s); onOpen("new"); }} />
          </div>
        ) : <Empty title="No templates yet">An admin can create the first one.</Empty>
      ) : (
        <DataTable
          columns={columns}
          data={templates}
          rowId={(t) => t.id}
          onRowClick={(t) => onOpen(t.handle)}
          searchPlaceholder="Search templates"
          toolbar={
            <select aria-label="Show" className={field} value={status} onChange={(e) => setStatus(e.target.value as "active" | "archived")}>
              <option value="active">Active</option>
              <option value="archived">Archived</option>
            </select>
          }
          empty={status === "archived" ? "No archived templates." : "No templates match."}
          mobile={(t) => (
            <div className="flex min-w-0 items-center gap-3">
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[13px] font-medium">{t.name}</span>
                <span className="block truncate font-mono text-[12px] text-muted-foreground">{t.handle}</span>
              </span>
              {t.published_version ? <Badge tone="green">v{t.published_version}</Badge> : <Badge tone="grey">Draft</Badge>}
            </div>
          )}
        />
      )}
    </div>
  );
}

function StarterGrid({ pick, taken }: { pick: (s: Starter) => void; taken?: Set<string> }) {
  return (
    <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
      {starters.map((s) => (
        <button key={s.id} type="button" onClick={() => pick(s)} className="flex flex-col items-start gap-1 rounded-lg border bg-card p-4 text-left shadow-xs transition-colors hover:border-ring/60 hover:bg-accent/30 outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/40">
          <span className="text-[13px] font-medium">{s.name}</span>
          <span className="text-[12px] text-muted-foreground">{s.blurb}</span>
          {s.template.handle && taken?.has(s.template.handle) && <span className="text-[12px] text-amber-700 dark:text-amber-400">You already have one: this makes a copy.</span>}
        </button>
      ))}
    </div>
  );
}

function StarterPicker({ back, pick, taken }: { back: () => void; pick: (s: Starter) => void; taken: Set<string> }) {
  return (
    <div className="space-y-6">
      <PageHeader back={{ label: "Email templates", onClick: back }} title="New template" description="Pick a starting point. You can change everything." />
      <StarterGrid pick={pick} taken={taken} />
    </div>
  );
}

// ---- Editor ----

type Draft = { name: string; handle: string; purpose: "transactional" | "lifecycle"; from: string; reply_to: string; subject: string; html: string; text: string; variables: EmailTemplateVariable[] };

const toDraft = (t: EmailTemplateInput | EmailTemplate): Draft => ({
  name: t.name ?? "", handle: t.handle ?? "", purpose: t.purpose ?? "transactional", from: t.from ?? "", reply_to: t.reply_to ?? "",
  subject: t.subject ?? "", html: t.html ?? "", text: t.text ?? "", variables: t.variables ?? [],
});

function Editor({ ws, id, starter, canManage, canSend, myEmail, back, onCreated, onRenamed, reload, go }: {
  ws: string; id?: string; starter?: Starter; canManage: boolean; canSend: boolean; myEmail: string;
  back: () => void; onCreated: (t: EmailTemplate) => void; onRenamed?: (handle: string) => void; reload: () => void; go: Go;
}) {
  const [saved, setSaved] = useState<EmailTemplate | null>(null);
  const [draft, setDraft] = useState<Draft | null>(starter ? toDraft(starter.template) : null);
  const [missing, setMissing] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [versions, setVersions] = useState<EmailTemplateVersion[]>([]);
  const sendingDomain = useSendingDomain(ws);
  // A new template sends from the verified domain until someone says otherwise.
  useEffect(() => {
    if (starter && sendingDomain) setDraft((d) => (d && !d.from ? { ...d, from: `${d.purpose === "lifecycle" ? "hello" : "no-reply"}@${sendingDomain}` } : d));
  }, [starter, sendingDomain]);

  const load = useCallback(async () => {
    if (!id) return;
    const [t, v] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/email-templates/{id}", { params: { path: { ws, id } } }),
      api.GET("/v1/workspaces/{ws}/email-templates/{id}/versions", { params: { path: { ws, id } } }),
    ]);
    if (!t.data) return setMissing(true);
    setSaved(t.data);
    setDraft(toDraft(t.data));
    setVersions(v.data?.data ?? []);
  }, [ws, id]);
  useEffect(() => {
    load();
  }, [load]);

  const dirty = useMemo(() => !saved || !draft || JSON.stringify(toDraft(saved)) !== JSON.stringify(draft), [saved, draft]);

  if (missing) return <Empty title="Template not found" action={<Button onClick={back}>All templates</Button>}>It may have been deleted.</Empty>;
  if (!draft) return <Loading />;
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => (d ? { ...d, [k]: v } : d));
  const readOnly = !canManage;

  async function save(publish = false, note = "") {
    if (!draft) return null;
    setBusy(publish ? "publish" : "save");
    // Transactional email has no topic: going back to it clears the topic.
    const body: EmailTemplateInput = { ...draft, ...(draft.handle ? {} : { handle: undefined }), ...(publish ? { publish: true, note } : {}),
      ...(draft.purpose === "transactional" && saved?.topic ? { topic: null } : {}) };
    const res = saved
      ? await api.PATCH("/v1/workspaces/{ws}/email-templates/{id}", { params: { path: { ws, id: saved.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/email-templates", { params: { path: { ws } }, body });
    setBusy("");
    if (res.error || !res.data) {
      setError(errorMessage(res.error));
      return null;
    }
    setError("");
    const t = res.data as EmailTemplate;
    if (!saved) {
      toast(publish ? `Created and published ${t.name}.` : `Created ${t.name} as a draft.`);
      onCreated(t);
      return t;
    }
    toast(publish ? `Published version ${t.published_version}.` : "Draft saved.");
    setSaved(t);
    setDraft(toDraft(t));
    reload();
    if (t.handle !== saved.handle) onRenamed?.(t.handle);
    if (publish) {
      const v = await api.GET("/v1/workspaces/{ws}/email-templates/{id}/versions", { params: { path: { ws, id: t.id } } });
      setVersions(v.data?.data ?? []);
    }
    return t;
  }

  async function publish() {
    const r = await ask({
      title: saved?.published_version ? `Publish version ${saved.published_version + 1}?` : "Publish this template?",
      body: "Sends that name this template use the published version from now on. Earlier versions are kept.",
      input: { label: "What changed (optional)", placeholder: "For example: clearer subject line", maxLength: 500, optional: true },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Publish", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    if (saved && !dirty) {
      setBusy("publish");
      const { data, error } = await api.POST("/v1/workspaces/{ws}/email-templates/{id}/publish", { params: { path: { ws, id: saved.id } }, body: { note: r.value } });
      setBusy("");
      if (error || !data) return setError(errorMessage(error));
      toast(`Published version ${data.published_version}.`);
      setSaved(data);
      setDraft(toDraft(data));
      reload();
      const v = await api.GET("/v1/workspaces/{ws}/email-templates/{id}/versions", { params: { path: { ws, id: data.id } } });
      setVersions(v.data?.data ?? []);
      return;
    }
    await save(true, r.value);
  }

  async function archive() {
    if (!saved) return;
    const next = saved.status === "active" ? "archived" : "active";
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/email-templates/{id}", { params: { path: { ws, id: saved.id } }, body: { status: next } });
    if (error || !data) return toast(errorMessage(error), "error");
    toast(next === "archived" ? "Archived. Sends that name it are refused until you restore it." : "Restored.");
    setSaved(data);
    reload();
  }

  async function remove() {
    if (!saved) return;
    if (!(await confirmAction(`Delete ${saved.name}?`, `Its versions are deleted too, and sends that name ${saved.handle} are refused. Emails already sent keep their content.`, "Delete template"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/email-templates/{id}", { params: { path: { ws, id: saved.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Deleted.");
    back();
  }

  const status = !saved ? <Badge tone="grey">New</Badge>
    : saved.status === "archived" ? <Badge tone="grey">Archived</Badge>
      : saved.published_version ? <span className="inline-flex gap-1.5"><Badge tone="green">v{saved.published_version} live</Badge>{(saved.unpublished_changes || dirty) && <Badge tone="amber">Unpublished changes</Badge>}</span>
        : <Badge tone="grey">Draft, not published</Badge>;

  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Email templates", onClick: back }}
        title={draft.name || "Untitled template"}
        description={<span className="inline-flex flex-wrap items-center gap-2">{status}{saved && <span className="font-mono text-[12px]">{saved.handle}</span>}{saved && <span>{saved.sent_30d ? `Sent ${saved.sent_30d === 1 ? "once" : `${saved.sent_30d.toLocaleString()} times`} in 30 days` : "Not sent in 30 days"}</span>}</span>}
        action={canManage && (
          <>
            {saved && <Menu aria="More template actions" items={[{ label: saved.status === "active" ? "Archive" : "Restore", onClick: archive }, { label: "Delete", onClick: remove, danger: true }]} />}
            {saved?.status === "archived" ? <Button onClick={archive}>Restore</Button> : <>
            <Button variant="ghost" onClick={() => save(false)} disabled={!!busy || !dirty}>{busy === "save" ? "Saving…" : saved ? "Save draft" : "Create draft"}</Button>
            <Button onClick={publish} disabled={!!busy || (!!saved && !dirty && !saved.unpublished_changes && !!saved.published_version)}>{busy === "publish" ? "Publishing…" : "Publish"}</Button>
            </>}
          </>
        )}
      />
      <ErrorText>{error}</ErrorText>
      <div className="grid gap-6 xl:grid-cols-2">
        <div className="min-w-0 space-y-6">
          <Card title="Details">
            <fieldset disabled={readOnly} className="grid gap-3 sm:grid-cols-2">
              <Input label="Name" value={draft.name} onChange={(e) => set("name", e.target.value)} required />
              <Input label="Handle" value={draft.handle} onChange={(e) => set("handle", e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, "-"))} placeholder="Made from the name" hint="What your app sends by. Changing it breaks sends that use the old one." spellCheck={false} />
              <Input label="From" value={draft.from} onChange={(e) => set("from", e.target.value)} placeholder="Acme <billing@acme.example>" hint="On a verified sending domain. A send can override it." />
              <Input label="Reply to" value={draft.reply_to} onChange={(e) => set("reply_to", e.target.value)} placeholder="support@acme.example" />
              <label className="block text-sm sm:col-span-2">
                <span className="mb-1.5 block text-[13px] font-medium">Kind</span>
                <select className={`${field} w-full`} value={draft.purpose} onChange={(e) => set("purpose", e.target.value as Draft["purpose"])}>
                  <option value="transactional">Transactional: receipts, resets, sign-in codes</option>
                  <option value="lifecycle">Lifecycle: onboarding, tips, updates</option>
                </select>
                <span className="mt-1 block text-[12px] text-muted-foreground">{draft.purpose === "transactional" ? "Reaches people who unsubscribed from updates, because they need it. Bounces and complaints still stop it." : "Carries a one-click unsubscribe link to a preference page, and skips anyone who left its topic or all marketing email."}</span>
              </label>
            </fieldset>
          </Card>
          <Card title="Content" description={<>Use <code className="font-mono">{"{{name}}"}</code> for a variable, <code className="font-mono">{"{{name|fallback}}"}</code> for a fallback, <code className="font-mono">{"{{#if name}}…{{/if}}"}</code> to show a part only when it is set, and <code className="font-mono">{"{{unsubscribe_url}}"}</code> for the unsubscribe link.</>}>
            <fieldset disabled={readOnly} className="space-y-3">
              <Input label="Subject" value={draft.subject} onChange={(e) => set("subject", e.target.value)} required />
              <label className="block text-sm">
                <span className="mb-1.5 block text-[13px] font-medium">HTML</span>
                <textarea rows={18} className={`${field} w-full resize-y font-mono text-[12px] leading-relaxed`} value={draft.html} onChange={(e) => set("html", e.target.value)} spellCheck={false} />
              </label>
              <label className="block text-sm">
                <span className="mb-1.5 block text-[13px] font-medium">Plain text <span className="font-normal text-muted-foreground">(optional: made from the HTML when empty)</span></span>
                <textarea rows={5} className={`${field} w-full resize-y font-mono text-[12px]`} value={draft.text} onChange={(e) => set("text", e.target.value)} spellCheck={false} />
              </label>
            </fieldset>
          </Card>
          <Variables draft={draft} readOnly={readOnly} onChange={(v) => set("variables", v)} />
        </div>
        <div className="min-w-0 space-y-6">
          <Preview ws={ws} draft={draft} onDeclare={(names) => set("variables", [...draft.variables, ...names.map((name) => ({ name }))])} readOnly={readOnly} />
          {saved && saved.purpose === "lifecycle" && <TopicCard ws={ws} template={saved} canManage={canManage} go={go} onChange={(t) => { setSaved(t); reload(); }} />}
          {saved && <Tracking ws={ws} template={saved} canManage={canManage} go={go} onChange={(t) => { setSaved(t); reload(); }} />}
          {saved && canSend && <TestSend key={sendingDomain} ws={ws} template={saved} myEmail={myEmail} dirty={dirty} fallbackFrom={sendingDomain ? `no-reply@${sendingDomain}` : ""} />}
          {saved && <Card title="Send it from your app"><div className="text-[13px] text-muted-foreground [&_button.underline]:text-foreground"><SendSnippet go={go} domain={(saved.from.match(/@([^>\s]+)/)?.[1]) ?? "example.com"} template={saved.handle} data={Object.fromEntries(saved.variables.filter((v) => v.required || v.example).map((v) => [v.name, v.example || "…"]))} /></div></Card>}
          {versions.length > 0 && <Versions versions={versions} restore={canManage ? (v) => { setDraft({ ...draft, from: v.from, reply_to: v.reply_to, subject: v.subject, html: v.html, text: v.text, variables: v.variables }); toast(`Loaded version ${v.version} into the draft. Save or publish to keep it.`); } : undefined} />}
        </div>
      </div>
    </div>
  );
}

function Variables({ draft, readOnly, onChange }: { draft: Draft; readOnly: boolean; onChange: (v: EmailTemplateVariable[]) => void }) {
  const vars = draft.variables;
  const update = (i: number, patch: Partial<EmailTemplateVariable>) => onChange(vars.map((v, j) => (j === i ? { ...v, ...patch } : v)));
  return (
    <Card title="Variables" description="What your app passes in data. A required variable that is missing stops the send with a clear error; a default fills in when it is left out; the example shows in the preview.">
      {vars.length === 0 ? <p className="text-[13px] text-muted-foreground">None yet. Variables you use in the content are added when you save.</p> : (
        <fieldset disabled={readOnly} className="space-y-2">
          <div className="hidden grid-cols-[minmax(0,1fr)_4.5rem_minmax(0,1fr)_minmax(0,1fr)_2rem] gap-2 text-[12px] text-muted-foreground sm:grid">
            <span>Name</span><span>Required</span><span>Default</span><span>Example</span><span />
          </div>
          {vars.map((v, i) => (
            <div key={i} className="grid grid-cols-2 gap-2 rounded-md border p-2 sm:grid-cols-[minmax(0,1fr)_4.5rem_minmax(0,1fr)_minmax(0,1fr)_2rem] sm:border-0 sm:p-0">
              <input aria-label="Name" className={`${field} col-span-2 min-w-0 font-mono text-[12px] sm:col-span-1`} value={v.name} onChange={(e) => update(i, { name: e.target.value.replace(/[^A-Za-z0-9_]/g, "_") })} />
              <label className="flex h-8 items-center gap-2 text-[13px] sm:justify-center">
                <input type="checkbox" className="size-4" checked={!!v.required} onChange={(e) => update(i, { required: e.target.checked })} />
                <span className="sm:sr-only">Required</span>
              </label>
              <span className="sm:hidden" />
              <input aria-label="Default" placeholder="Default" className={`${field} min-w-0`} value={v.default ?? ""} onChange={(e) => update(i, { default: e.target.value })} />
              <input aria-label="Example" placeholder="Example" className={`${field} min-w-0`} value={v.example ?? ""} onChange={(e) => update(i, { example: e.target.value })} />
              {readOnly ? <span /> : <button type="button" aria-label={`Remove ${v.name}`} className="col-span-2 h-8 rounded-md text-muted-foreground hover:bg-accent hover:text-foreground sm:col-span-1" onClick={() => onChange(vars.filter((_, j) => j !== i))}>×</button>}
            </div>
          ))}
        </fieldset>
      )}
      {!readOnly && <Button variant="ghost" className="mt-3" onClick={() => onChange([...vars, { name: "" }])}>Add variable</Button>}
    </Card>
  );
}

type View = "desktop" | "phone" | "text";

function Preview({ ws, draft, onDeclare, readOnly }: { ws: string; draft: Draft; onDeclare: (names: string[]) => void; readOnly: boolean }) {
  const [view, setView] = useState<View>("desktop");
  const [out, setOut] = useState<EmailPreview | null>(null);
  const [error, setError] = useState("");
  const seq = useRef(0);
  const key = JSON.stringify([draft.subject, draft.html, draft.text, draft.variables]);

  useEffect(() => {
    const n = ++seq.current;
    const t = setTimeout(async () => {
      const { data, error } = await api.POST("/v1/workspaces/{ws}/email-templates/preview", {
        params: { path: { ws } },
        body: { subject: draft.subject, html: draft.html, text: draft.text, variables: draft.variables.filter((v) => v.name) },
      });
      if (n !== seq.current) return;
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setOut(data);
    }, 500);
    return () => clearTimeout(t);
  }, [ws, key]);

  function show(v: View) {
    setView(v);
    track("ui.email_template_previewed", { view: v });
  }

  return (
    <section aria-label="Preview" className="rounded-lg border bg-card shadow-xs xl:sticky xl:top-4">
      <header className="flex flex-wrap items-center gap-1 border-b px-2 py-1.5">
        <span className="px-2 text-[13px] font-semibold">Preview</span>
        <span className="ml-auto flex gap-1" role="tablist">
          {(["desktop", "phone", "text"] as const).map((v) => (
            <button key={v} role="tab" aria-selected={view === v} onClick={() => show(v)} className={cn("h-8 rounded-md px-3 text-[13px]", view === v ? "bg-accent font-medium" : "text-muted-foreground hover:text-foreground")}>{{ desktop: "Desktop", phone: "Phone", text: "Plain text" }[v]}</button>
          ))}
        </span>
      </header>
      <div className="space-y-2 p-3">
        {error ? <ErrorText>{error}</ErrorText> : out && (
          <>
            <p className="truncate text-[13px]"><span className="text-muted-foreground">Subject: </span><span className="font-medium">{out.subject || "(no subject)"}</span></p>
            {out.unknown.length > 0 && (
              <p className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-[12px]">
                Not declared yet: <span className="font-mono">{out.unknown.join(", ")}</span>.{" "}
                {!readOnly && <button className="underline" onClick={() => onDeclare(out.unknown)}>Add {out.unknown.length === 1 ? "it" : "them"}</button>}
              </p>
            )}
            {out.missing.length > 0 && <p className="text-[12px] text-muted-foreground">Shown empty because they have no example: <span className="font-mono">{out.missing.join(", ")}</span></p>}
          </>
        )}
        {view === "text"
          ? <pre className="h-[34rem] overflow-auto whitespace-pre-wrap rounded-md border bg-background p-3 font-mono text-[12px]">{out?.text}</pre>
          : (
            <div className={cn("overflow-hidden rounded-md border bg-white", view === "phone" && "mx-auto w-[375px] max-w-full")}>
              <iframe title="Email preview" sandbox="" srcDoc={out?.html ?? ""} className="h-[34rem] w-full" />
            </div>
          )}
      </div>
    </section>
  );
}

// Tracking is a setting, not content: switching it applies at once,
// without a publish.
function Tracking({ ws, template: t, canManage, go, onChange }: { ws: string; template: EmailTemplate; canManage: boolean; go: Go; onChange: (t: EmailTemplate) => void }) {
  const [busy, setBusy] = useState("");
  const [linkDomain, setLinkDomain] = useState<string | null | undefined>();
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/tracking-domains", { params: { path: { ws } } }).then(({ data }) => data && setLinkDomain(data.data.find((d) => d.status === "verified")?.domain ?? null));
  }, [ws]);
  async function toggle(key: "track_opens" | "track_clicks", on: boolean) {
    setBusy(key);
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/email-templates/{id}", { params: { path: { ws, id: t.id } }, body: { [key]: on } });
    setBusy("");
    if (error || !data) return toast(errorMessage(error), "error");
    toast(`${key === "track_opens" ? "Open" : "Click"} tracking ${on ? "on" : "off"} for new sends.`);
    onChange(data);
  }
  const rows = [
    { key: "track_opens" as const, label: "Track opens", hint: "Adds an invisible image that loads when the email is shown. A rough signal: Apple Mail loads images for privacy whether or not someone reads it, and some apps block images. Loads by mail scanners right after sending are left out." },
    { key: "track_clicks" as const, label: "Track link clicks", hint: "Links go through a redirect so clicks can be counted, which spam filters can count against you. Clicks by link scanners are left out." },
  ];
  return (
    <Card title="Tracking" description="Off by default, as in sequences. Changes apply to new sends straight away, without publishing. Your app can override them for one send.">
      <div className="space-y-3">
        {rows.map((r) => (
          <label key={r.key} className="flex items-start gap-2 text-[13px]">
            <input type="checkbox" className="mt-0.5" checked={t[r.key]} disabled={!canManage || !!busy} onChange={(e) => toggle(r.key, e.target.checked)} />
            <span>
              <span className="font-medium">{r.label}</span>
              <span className="block text-[12px] text-muted-foreground">{r.hint}</span>
            </span>
          </label>
        ))}
        {(t.track_opens || t.track_clicks) && (
          <p className="text-[12px] text-muted-foreground">
            {linkDomain ? <>Tracking links use {linkDomain}.</>
              : linkDomain === null && <>Tracking links use this server's address. <button type="button" className="underline" onClick={() => go("domains")}>Set up a tracking domain</button> so they match your brand.</>}
          </p>
        )}
      </div>
    </Card>
  );
}

// The topic is a setting too: it applies to new sends at once.
function TopicCard({ ws, template: t, canManage, go, onChange }: { ws: string; template: EmailTemplate; canManage: boolean; go: Go; onChange: (t: EmailTemplate) => void }) {
  const [topics, setTopics] = useState<EmailTopic[] | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/email-topics", { params: { path: { ws }, query: { status: "active" } } }).then(({ data }) => setTopics(data?.data ?? []));
  }, [ws]);
  async function pick(key: string) {
    setBusy(true);
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/email-templates/{id}", { params: { path: { ws, id: t.id } }, body: { topic: key || null } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    toast(data.topic ? `New sends go to people subscribed to ${data.topic.name}.` : "No topic: new sends go to everyone who hasn't left all marketing email.");
    onChange(data);
  }
  const current = topics?.find((x) => x.id === t.topic?.id);
  return (
    <Card title="Topic" description="What people choose to get on the preference page behind the unsubscribe link. Changes apply to new sends straight away.">
      {topics === null ? <Loading /> : topics.length === 0 && !t.topic ? (
        <p className="text-[13px] text-muted-foreground">
          No topics yet. <button type="button" className="underline text-foreground" onClick={() => go("topics")}>Add topics</button> such as product updates or a newsletter, so people can leave one without leaving them all.
        </p>
      ) : (
        <div className="space-y-2">
          <select className={`${field} w-full`} value={t.topic?.key ?? ""} disabled={!canManage || busy} onChange={(e) => pick(e.target.value)} aria-label="Topic">
            <option value="">No topic</option>
            {topics.map((x) => <option key={x.id} value={x.key}>{x.name}</option>)}
            {t.topic && !current && <option value={t.topic.key}>{t.topic.name} (archived)</option>}
          </select>
          <p className="text-[12px] text-muted-foreground">
            {!t.topic ? "Goes to everyone who hasn't unsubscribed from all marketing email."
              : current?.subscribed_by_default === false ? `Only people who opted in to ${t.topic.name} get it.`
              : `Everyone gets it until they unsubscribe from ${t.topic.name}.`}{" "}
            <button type="button" className="underline" onClick={() => go("topics")}>Manage topics</button>
          </p>
        </div>
      )}
    </Card>
  );
}

function TestSend({ ws, template, myEmail, dirty, fallbackFrom }: { ws: string; template: EmailTemplate; myEmail: string; dirty: boolean; fallbackFrom: string }) {
  const [to, setTo] = useState(myEmail);
  const [from, setFrom] = useState(template.from || fallbackFrom);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function send() {
    setBusy(true);
    const data = Object.fromEntries(template.variables.filter((v) => v.example).map((v) => [v.name, v.example!]));
    const res = await api.POST("/v1/workspaces/{ws}/emails", {
      params: { path: { ws } },
      body: { to: to.trim(), template: template.id, draft: true, test: true, data, ...(from.trim() ? { from: from.trim() } : {}) },
    });
    setBusy(false);
    if (res.error || !res.data) return setError(errorMessage(res.error));
    setError("");
    toast(`Test sent to ${res.data.to[0]}, filled with the examples.`);
  }
  return (
    <Card title="Send a test" description={dirty ? "Uses the last saved draft: save first to test your latest changes." : "Sends the saved draft, filled with each variable's example. Tests are left out of your numbers."}>
      <div className="grid gap-2 sm:grid-cols-2">
        <Input label="To" type="email" value={to} onChange={(e) => setTo(e.target.value)} />
        <Input label="From" value={from} onChange={(e) => setFrom(e.target.value)} placeholder="hello@your-domain.example" />
      </div>
      <ErrorText>{error}</ErrorText>
      <Button className="mt-3" onClick={send} disabled={busy || !to.trim()}>{busy ? "Sending…" : "Send test"}</Button>
    </Card>
  );
}

function Versions({ versions, restore }: { versions: EmailTemplateVersion[]; restore?: (v: EmailTemplateVersion) => void }) {
  return (
    <Card title="Published versions" description={`${plural(versions.length, "version")}. Your app can pin one with version in the send.`}>
      <ul className="-my-2 divide-y">
        {versions.map((v) => (
          <li key={v.version} className="flex items-center gap-3 py-2 text-[13px]">
            <Badge tone="grey">v{v.version}</Badge>
            <span className="min-w-0 flex-1">
              <span className="block truncate">{v.note || v.subject}</span>
              <span className="block text-[12px] text-muted-foreground" title={stamp(v.published_at)}>{ago(v.published_at)}{v.published_by ? ` by ${v.published_by}` : ""}</span>
            </span>
            {restore && <Button variant="ghost" onClick={() => restore(v)}>Load into draft</Button>}
          </li>
        ))}
      </ul>
    </Card>
  );
}
