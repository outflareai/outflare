// AI receipts (plans/ai-accounts, step 3): where people forward the
// receipts of their AI subscriptions, how to set it up in Gmail, Google
// Workspace or Outlook, the personal addresses they forward from, and the
// receipts Outflare read. Inbound email is part of Outflare Cloud; other
// servers say so.

import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { AIAccount, AIReceipt, InboundAddress, InboundForwarder } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, CopyButton, ErrorText, field, Loading, toast } from "../ui";
import { AILogo, providerLabel } from "@/components/ai-logo";
import { useCapabilities } from "@/lib/cloud";
import { shortDay, usd } from "@/lib/aiAccounts";
import { cn } from "@/lib/utils";

type Props = {
  ws: string;
  accounts: AIAccount[];
  canSetUp: boolean; // workspace.manage: make or replace the address
  seeSpend: boolean;
  manageAll: boolean;
  open: (a: AIAccount) => void;
  reload: () => void;
};

// The Gmail search for AI receipts: the companies' own mail, and receipts
// for them sent by payment processors and app stores.
export const filterQuery =
  "(from:(anthropic.com OR openai.com OR cursor.com) OR (from:(stripe.com OR apple.com OR google.com OR github.com) (Anthropic OR Claude OR OpenAI OR ChatGPT OR Cursor OR Anysphere OR Copilot))) (receipt OR invoice OR payment OR subscription OR renewal OR refund)";
const senderRegex = String.raw`(?i)@([a-z0-9-]+\.)*(anthropic\.com|openai\.com|cursor\.com|github\.com)>?$`;
const outlookSenders = "anthropic.com; openai.com; cursor.com; github.com";
const gmailSettings = "https://mail.google.com/mail/u/0/#settings/fwdandpop";

const xmlEscape = (s: string) => s.replace(/&/g, "&amp;").replace(/'/g, "&apos;").replace(/</g, "&lt;");
export const filterFile = (address: string) => `<?xml version='1.0' encoding='UTF-8'?>
<feed xmlns='http://www.w3.org/2005/Atom' xmlns:apps='http://schemas.google.com/apps/2006'>
  <title>Mail Filters</title>
  <entry>
    <category term='filter'></category>
    <title>Outflare: AI receipts</title>
    <content></content>
    <apps:property name='hasTheWord' value='${xmlEscape(filterQuery)}'/>
    <apps:property name='forwardTo' value='${xmlEscape(address)}'/>
    <apps:property name='shouldNeverSpam' value='true'/>
  </entry>
</feed>`;

const agentSteps = (address: string) => `In my Gmail, set up forwarding of AI receipts to Outflare:

1. Open ${gmailSettings}
   Click "Add a forwarding address", enter ${address}, then Next → Proceed → OK.
   Do NOT select "Forward a copy of incoming mail to". Only the filter below forwards mail.
2. Gmail sends a confirmation to Outflare. Tell me to confirm it in Outflare (AI accounts → Receipts), then reload Gmail.
3. In the Gmail search bar, paste exactly:
   ${filterQuery}
   Open the search options (▾), click "Create filter", then tick:
   - Forward it to: ${address}
   - Never send it to Spam
   - Also apply filter to matching conversations
   Click "Create filter".
4. Change nothing else. Tell me when it's done.`;

function Code({ text, label, wrap }: { text: string; label: string; wrap?: boolean }) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-2 text-[12px] text-muted-foreground">
        <span>{label}</span>
        <CopyButton text={text} />
      </div>
      <pre className={cn("max-h-64 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-[12px] leading-relaxed", wrap && "whitespace-pre-wrap break-all")}>{text}</pre>
    </div>
  );
}

function Step({ n, title, done, children }: { n: number; title: string; done?: boolean; children: React.ReactNode }) {
  return (
    <li className="grid grid-cols-[1.75rem_minmax(0,1fr)] gap-3 rounded-md border p-3">
      <span className={cn("flex size-6 items-center justify-center rounded-full text-[12px] font-medium", done ? "bg-green-500/15 text-green-700 dark:text-green-400" : "bg-muted")}>{done ? "✓" : n}</span>
      <div className="min-w-0 space-y-2.5">
        <div className="font-medium">{title}</div>
        {children}
      </div>
    </li>
  );
}

export function ReceiptsView({ ws, accounts, canSetUp, seeSpend, manageAll, open, reload }: Props) {
  const caps = useCapabilities();
  const [address, setAddress] = useState<InboundAddress | null | undefined>(undefined);
  const [forwarders, setForwarders] = useState<InboundForwarder[]>([]);
  const [receipts, setReceipts] = useState<AIReceipt[] | null>(null);
  const [error, setError] = useState("");
  const [how, setHow] = useState<"gmail" | "workspace" | "outlook">("gmail");
  const [filterHow, setFilterHow] = useState<"file" | "hand">("file");

  const load = useCallback(async () => {
    const [a, f, r] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/inbound-addresses", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/inbound-forwarders", { params: { path: { ws } } }),
      seeSpend ? api.GET("/v1/workspaces/{ws}/ai-accounts/receipts", { params: { path: { ws } } }) : Promise.resolve({ data: { data: [] as AIReceipt[] }, error: undefined }),
    ]);
    if (a.error) setError(errorMessage(a.error));
    setAddress(a.data?.data.find((x) => x.purpose === "ai_receipts") ?? null);
    setForwarders(f.data?.data ?? []);
    setReceipts(r.data?.data ?? []);
  }, [ws, seeSpend]);
  useEffect(() => {
    if (caps?.includes("inbound_email")) load();
  }, [caps, load]);

  if (caps === null) return <Loading />;
  if (!caps.includes("inbound_email")) {
    return (
      <Card title="Receipt forwarding is part of Outflare Cloud" description="Forward the receipts of your AI subscriptions to one address, and Outflare keeps each account's price, seats and renewal date up to date.">
        <p className="text-[13px] text-muted-foreground">This server doesn't receive email. On Outflare Cloud, every workspace gets its own forwarding address. Here, keep prices and renewals up to date on each account.</p>
      </Card>
    );
  }

  async function makeAddress(replace: boolean) {
    if (replace && !(await confirmAction("Replace the forwarding address?", "Mail to the old address will be refused, so everyone forwarding has to update their filter.", "Replace address"))) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/inbound-addresses", { params: { path: { ws } }, body: { purpose: "ai_receipts", replace } });
    if (error) return toast(errorMessage(error), "error");
    setAddress(data ?? null);
    toast(replace ? "New address. Update your filters." : "Forwarding address ready.");
  }

  const review = (receipts ?? []).filter((r) => r.status === "needs_review");
  return (
    <div className="space-y-6 text-[13px]">
      <ErrorText>{error}</ErrorText>
      <Card title="Forward AI receipts" description="Send the receipts from Anthropic, OpenAI, Cursor and GitHub here. Outflare reads them and keeps each account's price, seats and renewal date up to date.">
        {address === undefined ? (
          <Loading />
        ) : address === null ? (
          canSetUp ? <Button onClick={() => makeAddress(false)}>Create the forwarding address</Button> : <p className="text-muted-foreground">An admin needs to create the workspace's forwarding address first.</p>
        ) : (
          <div className="flex flex-wrap items-center gap-3 rounded-md bg-muted/50 px-3 py-2.5">
            <div className="min-w-0 flex-1">
              <div className="text-[12px] text-muted-foreground">Your workspace's forwarding address</div>
              <div className="font-mono text-[14px] break-all">{address.email}</div>
            </div>
            <CopyButton text={address.email} />
            {canSetUp && <Button variant="ghost" onClick={() => makeAddress(true)}>Replace</Button>}
          </div>
        )}
      </Card>

      {address && (
        <Card title="Set it up" description="Only receipts are forwarded; the rest of your mail stays put.">
          <div className="mb-4 flex flex-wrap gap-1.5">
            {([["gmail", "Gmail"], ["workspace", "Google Workspace admin"], ["outlook", "Outlook / Microsoft 365"]] as const).map(([k, l]) => (
              <Button key={k} variant={how === k ? "primary" : "ghost"} onClick={() => setHow(k)}>{l}</Button>
            ))}
          </div>
          {how === "gmail" && (
            <ol className="space-y-3">
              <Step n={1} title="Link your personal address, if you forward from one">
                <p className="text-muted-foreground">Work addresses in this workspace can forward already. For a personal Gmail, link it here first so Outflare accepts it.</p>
                <Forwarders ws={ws} list={forwarders} onChange={setForwarders} />
              </Step>
              <Step n={2} title="Add the forwarding address in Gmail">
                <ol className="list-decimal space-y-1 pl-5">
                  <li>In the Gmail that gets AI receipts, <a className="underline underline-offset-2" href={gmailSettings} target="_blank" rel="noopener">open Forwarding settings</a>.</li>
                  <li>Click <b>Add a forwarding address</b>, paste <span className="font-mono">{address.email}</span>, then <b>Next → Proceed → OK</b>.</li>
                  <li>Leave <b>Disable forwarding</b> selected. The filter in step 3 forwards only receipts.</li>
                  <li>Gmail sends a confirmation to Outflare. It appears in step 1 within a minute: click <b>Confirm in Gmail</b>, then reload Gmail.</li>
                </ol>
              </Step>
              <Step n={3} title="Add the filter">
                <div className="flex gap-1.5">
                  <Button variant={filterHow === "file" ? "primary" : "ghost"} onClick={() => setFilterHow("file")}>Import a file</Button>
                  <Button variant={filterHow === "hand" ? "primary" : "ghost"} onClick={() => setFilterHow("hand")}>Create by hand</Button>
                </div>
                {filterHow === "file" ? (
                  <>
                    <ol className="list-decimal space-y-1 pl-5">
                      <li>Copy the file below and save it as <span className="font-mono">outflare-filter.xml</span>.</li>
                      <li>In Gmail open <b>Settings → See all settings → Filters and Blocked Addresses → Import filters</b>.</li>
                      <li>Choose the file, click <b>Open file</b>, tick <b>Apply new filters to existing email</b>, then <b>Create filters</b>.</li>
                    </ol>
                    <Code label="outflare-filter.xml" text={filterFile(address.email)} />
                  </>
                ) : (
                  <>
                    <ol className="list-decimal space-y-1 pl-5">
                      <li>Paste the search below into the Gmail search bar.</li>
                      <li>Click the options arrow (▾) at its right, then <b>Create filter</b>.</li>
                      <li>Tick <b>Forward it to</b> and choose <span className="font-mono">{address.email}</span>, tick <b>Never send it to Spam</b> and <b>Also apply filter to matching conversations</b>, then <b>Create filter</b>.</li>
                    </ol>
                    <Code label="Gmail search" text={filterQuery} wrap />
                  </>
                )}
                <p className="text-muted-foreground">Applying it to existing email sends past receipts too, so Outflare learns your renewal dates straight away.</p>
              </Step>
              <Step n={4} title="Or let an agent do it">
                <p className="text-muted-foreground">For an agent that can use your browser, such as Claude in Chrome, signed in to the Gmail that gets the receipts.</p>
                <Code label="Instructions" text={agentSteps(address.email)} wrap />
              </Step>
            </ol>
          )}
          {how === "workspace" && (
            <ol className="space-y-3">
              <Step n={1} title="One rule for every inbox">
                <p className="text-muted-foreground">A Google Workspace admin can copy AI receipts from every inbox to Outflare at once, with no filter for anyone to set up.</p>
                <ol className="list-decimal space-y-1 pl-5">
                  <li>Open the <a className="underline underline-offset-2" href="https://admin.google.com" target="_blank" rel="noopener">Google Admin console</a> → <b>Apps → Google Workspace → Gmail → Compliance</b>.</li>
                  <li>Under <b>Content compliance</b>, click <b>Configure</b> and name it <b>Outflare AI receipts</b>.</li>
                  <li>Messages to affect: <b>Inbound</b>. Add an expression: <b>Advanced content match</b>, location <b>Sender header</b>, <b>Matches regex</b>, with the expression below.</li>
                  <li>Action: <b>Modify message → Also deliver to → Add more recipients</b>, and add <span className="font-mono">{address.email}</span>. Save.</li>
                </ol>
                <Code label="Sender regex" text={senderRegex} wrap />
                <p className="text-muted-foreground">This catches mail the AI companies send themselves. Receipts sent through Stripe or app stores still need the Gmail filter.</p>
              </Step>
            </ol>
          )}
          {how === "outlook" && (
            <ol className="space-y-3">
              <Step n={1} title="Add an inbox rule">
                <ol className="list-decimal space-y-1 pl-5">
                  <li>Link your address in the Gmail tab's step 1 if it isn't a work address in this workspace.</li>
                  <li>In Outlook on the web open <b>Settings → Mail → Rules → Add new rule</b>, named <b>Outflare AI receipts</b>.</li>
                  <li>Condition: <b>From</b>, with each of these senders. Action: <b>Forward to</b> <span className="font-mono">{address.email}</span>. Save.</li>
                </ol>
                <Code label="Senders" text={outlookSenders} />
                <p className="text-muted-foreground">Microsoft 365 often blocks forwarding outside your company. If nothing arrives, ask your admin to allow it for this address.</p>
              </Step>
            </ol>
          )}
        </Card>
      )}

      {seeSpend && receipts === null && <Loading />}
      {review.length > 0 && <Review ws={ws} list={review} accounts={accounts} canResolve={manageAll} onDone={() => { load(); reload(); }} />}
      {seeSpend && receipts && receipts.length > 0 && (
        <Card title="Recent receipts" description="Only what each says is kept. The email itself is deleted after 30 days.">
          <div className="overflow-x-auto">
            <table className="w-full min-w-[42rem]">
              <thead className="text-left text-[11px] tracking-wide text-muted-foreground uppercase">
                <tr><th className="py-1.5 pr-3">Date</th><th className="pr-3">From</th><th className="pr-3">Amount</th><th className="pr-3">Account</th><th className="pr-3">Matched by</th><th>Forwarded by</th></tr>
              </thead>
              <tbody className="divide-y">
                {receipts.map((r) => {
                  const acc = accounts.find((a) => a.id === r.account_id);
                  return (
                    <tr key={r.id}>
                      <td className="py-2 pr-3 whitespace-nowrap tabular-nums">{r.charged_on ? shortDay(r.charged_on) : "—"}</td>
                      <td className="pr-3"><span className="flex items-center gap-2 whitespace-nowrap">{r.provider ? <AILogo provider={r.provider as AIAccount["provider"]} className="size-5" /> : null}{[r.provider ? providerLabel(r.provider as AIAccount["provider"]) : r.vendor, r.plan].filter(Boolean).join(" ")}</span></td>
                      <td className="pr-3 whitespace-nowrap tabular-nums">{amount(r)}</td>
                      <td className="pr-3">{r.status === "matched" ? (acc ? <button className="text-left underline-offset-2 hover:underline" onClick={() => open(acc)}>{acc.name}</button> : r.account_name ?? "—") : r.status === "ignored" ? <span className="text-muted-foreground">Ignored</span> : <Badge tone="amber">Needs review</Badge>}</td>
                      <td className="pr-3 text-muted-foreground">{matchedWords[r.matched_by ?? ""] ?? "—"}</td>
                      <td className="text-muted-foreground">{r.forwarded_by ?? "—"}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  );
}

const matchedWords: Record<string, string> = {
  account_email: "Account email on it",
  known_id: "Same as an earlier receipt",
  forwarder: "Who forwarded it",
  expected_renewal: "Expected renewal",
  person: "Assigned by hand",
};

function amount(r: AIReceipt) {
  if (r.amount_minor == null) return "—";
  if (r.currency === "USD" || !r.currency) return usd(r.amount_minor);
  const digits = ["JPY", "KRW", "ISK", "HUF"].includes(r.currency) ? 0 : 2;
  const original = (r.amount_minor / 10 ** digits).toLocaleString(undefined, { style: "currency", currency: r.currency });
  return r.usd_cents != null ? `${usd(r.usd_cents)} · charged as ${original}` : `charged as ${original}`;
}

function Forwarders({ ws, list, onChange }: { ws: string; list: InboundForwarder[]; onChange: (l: InboundForwarder[]) => void }) {
  const [email, setEmail] = useState("");
  const [error, setError] = useState("");
  async function link(e: FormEvent) {
    e.preventDefault();
    setError("");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/inbound-forwarders", { params: { path: { ws } }, body: { email } });
    if (error) return setError(errorMessage(error));
    onChange(data?.data ?? []);
    setEmail("");
  }
  async function unlink(addr: string) {
    const { error } = await api.DELETE("/v1/workspaces/{ws}/inbound-forwarders/{email}", { params: { path: { ws, email: addr } } });
    if (error) return toast(errorMessage(error), "error");
    onChange(list.filter((f) => f.email !== addr));
  }
  return (
    <div className="space-y-2">
      {list.map((f) => (
        <div key={f.email} className="flex flex-wrap items-center gap-2 rounded-md border px-2.5 py-1.5">
          <span className="min-w-0 flex-1 break-all">{f.email}</span>
          {f.confirmation_url ? (
            <>
              <span className="text-muted-foreground">Gmail code {f.confirmation_code}</span>
              <a className="inline-flex h-8 items-center rounded-md bg-primary px-3 font-medium text-primary-foreground" href={f.confirmation_url} target="_blank" rel="noopener noreferrer">Confirm in Gmail</a>
            </>
          ) : f.last_email_at ? (
            <Badge tone="green">Forwarding</Badge>
          ) : (
            <span className="text-muted-foreground">Waiting for Gmail's confirmation</span>
          )}
          <Button variant="ghost" onClick={() => unlink(f.email)}>Remove</Button>
        </div>
      ))}
      <form onSubmit={link} className="flex gap-2">
        <input className={`${field} min-w-0 flex-1`} type="email" required placeholder="you@gmail.com" value={email} onChange={(e) => setEmail(e.target.value)} aria-label="Personal address" />
        <Button type="submit">Link address</Button>
      </form>
      <ErrorText>{error}</ErrorText>
    </div>
  );
}

function Review({ ws, list, accounts, canResolve, onDone }: { ws: string; list: AIReceipt[]; accounts: AIAccount[]; canResolve: boolean; onDone: () => void }) {
  async function assign(r: AIReceipt, accountID: string) {
    const { error } = await api.POST("/v1/workspaces/{ws}/ai-accounts/receipts/{id}/assign", { params: { path: { ws, id: r.id } }, body: { account_id: accountID } });
    if (error) return toast(errorMessage(error), "error");
    toast("Assigned. Its next receipt will match by itself.");
    onDone();
  }
  async function addAccount(r: AIReceipt) {
    const provider = (r.provider ?? { anthropic: "claude", openai: "chatgpt", cursor: "cursor", github: "copilot" }[r.vendor]) as AIAccount["provider"];
    const { data, error } = await api.POST("/v1/workspaces/{ws}/ai-accounts", {
      params: { path: { ws } },
      body: { provider, name: [providerLabel(provider), r.plan].filter(Boolean).join(" "), billing_cycle: "monthly", ...(r.account_email ? { account_email: r.account_email } : {}) },
    });
    if (error || !data) return toast(errorMessage(error), "error");
    await assign(r, data.id);
  }
  async function ignore(r: AIReceipt) {
    const { error } = await api.POST("/v1/workspaces/{ws}/ai-accounts/receipts/{id}/ignore", { params: { path: { ws, id: r.id } } });
    if (error) return toast(errorMessage(error), "error");
    onDone();
  }
  return (
    <Card title={`Needs review · ${list.length}`} description="Receipts Outflare couldn't match on its own. Once you assign one, the next from the same account matches by itself.">
      <ul className="space-y-2">
        {list.map((r) => {
          const options = accounts.filter((a) => !r.provider || a.provider === r.provider);
          return (
            <li key={r.id} className="flex flex-wrap items-center gap-3 rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2">
              {r.provider && <AILogo provider={r.provider as AIAccount["provider"]} className="size-6" />}
              <div className="min-w-0 flex-1">
                <div className="font-medium">{[r.provider ? providerLabel(r.provider as AIAccount["provider"]) : r.vendor, r.plan].filter(Boolean).join(" ")} · {amount(r)}</div>
                <div className="text-[12px] text-muted-foreground">{r.charged_on ? shortDay(r.charged_on) : ""}{r.forwarded_by ? ` · forwarded by ${r.forwarded_by}` : ""}{r.account_email ? ` · for ${r.account_email}` : ""}</div>
              </div>
              {canResolve && (
                <div className="flex flex-wrap items-center gap-1.5">
                  <Button onClick={() => addAccount(r)}>Add as new account</Button>
                  <select aria-label="Assign to an account" className={`${field} max-w-48`} value="" onChange={(e) => e.target.value && assign(r, e.target.value)}>
                    <option value="">Assign to…</option>
                    {options.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
                  </select>
                  <Button variant="ghost" onClick={() => ignore(r)}>Ignore</Button>
                </div>
              )}
            </li>
          );
        })}
      </ul>
    </Card>
  );
}
