// The email finder: propose a likely work address for a person at a company,
// and check an address, using the per-workspace pattern engine behind
// /v1/workspaces/{ws}/email-finder (engine/internal/emailfind). Finding is
// free and learns only from this workspace's own leads and their verification
// outcomes; verifying rolls up to the workspace's own connected verifier only
// when it has to, and that spend stays on the workspace's account. Nothing a
// workspace does here is shared with any other workspace.

import { useState, type FormEvent } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, CopyButton, ErrorText, field, PageHeader, Stat, Stats, tones } from "../ui";
import { Search, Mail, ShieldCheck } from "lucide-react";

type Pattern = Schemas["EmailPattern"];
type ItemResult = Schemas["EmailFindItemResult"];
type FindCode = Schemas["EmailFindCode"];

// How each outcome code reads: the label, its tone and a plain-language note.
const codes: Record<FindCode, { label: string; tone: keyof typeof tones; note: string }> = {
  found: { label: "Found", tone: "green", note: "A likely work address, from this workspace's own data." },
  verified: { label: "Verified", tone: "green", note: "The mailbox exists and accepts mail." },
  risky: { label: "Risky", tone: "amber", note: "A catch-all, role or disposable address: it may bounce." },
  not_found: { label: "No match", tone: "grey", note: "Not enough known about this domain to propose an address yet." },
  undeliverable: { label: "Invalid", tone: "red", note: "The mailbox doesn't exist. It won't be emailed." },
  pending: { label: "Checking…", tone: "blue", note: "A slower check is running. Try again in a moment." },
  retry: { label: "Try again", tone: "amber", note: "The check couldn't finish just now. Try again shortly." },
  stop: { label: "Stopped", tone: "red", note: "The finder couldn't continue for this one." },
};

function Outcome({ r }: { r: ItemResult }) {
  const c = codes[r.code] ?? codes.not_found;
  return (
    <div className="flex items-center gap-2">
      <Badge tone={c.tone} title={c.note}>{c.label}</Badge>
      {r.code !== "not_found" && r.code !== "stop" && <span className="text-[13px] text-muted-foreground">{r.confidence}% sure</span>}
    </div>
  );
}

// Where a proposed address came from, in plain words.
const evidenceLabel: Record<string, string> = {
  history: "from a known address at this domain",
  pattern: "from this domain's learned email shape",
  default: "a best guess (little is known about this domain yet)",
  network: "confirmed by the data network",
};

export function EmailFinder({ ws, canManage }: { ws: string; canManage: boolean }) {
  // Find a work email.
  const [first, setFirst] = useState("");
  const [last, setLast] = useState("");
  const [domain, setDomain] = useState("");
  const [finding, setFinding] = useState(false);
  const [found, setFound] = useState<ItemResult | null>(null);
  const [findErr, setFindErr] = useState("");

  // Check an address.
  const [email, setEmail] = useState("");
  const [verifying, setVerifying] = useState(false);
  const [verified, setVerified] = useState<ItemResult | null>(null);
  const [verifyErr, setVerifyErr] = useState("");

  // Look up a domain's pattern.
  const [patternDomain, setPatternDomain] = useState("");
  const [looking, setLooking] = useState(false);
  const [pattern, setPattern] = useState<Pattern | null>(null);
  const [patternErr, setPatternErr] = useState("");

  async function find(e: FormEvent) {
    e.preventDefault();
    setFinding(true);
    setFindErr("");
    setFound(null);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/email-finder/find", {
      params: { path: { ws } },
      body: { items: [{ ref: "1", first_name: first.trim(), last_name: last.trim(), domain: domain.trim() }] },
    });
    setFinding(false);
    if (error) return setFindErr(errorMessage(error));
    setFound(data?.data?.items?.[0] ?? null);
  }

  async function verify(e: FormEvent) {
    e.preventDefault();
    setVerifying(true);
    setVerifyErr("");
    setVerified(null);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/email-finder/verify", {
      params: { path: { ws } },
      body: { items: [{ ref: "1", email: email.trim() }] },
    });
    setVerifying(false);
    if (error) return setVerifyErr(errorMessage(error));
    setVerified(data?.data?.items?.[0] ?? null);
  }

  // Send a found address straight into the checker.
  function checkFound() {
    if (!found?.email) return;
    setEmail(found.email);
    setVerified(null);
    setVerifyErr("");
  }

  async function lookup(e: FormEvent) {
    e.preventDefault();
    setLooking(true);
    setPatternErr("");
    setPattern(null);
    const { data, error } = await api.GET("/v1/workspaces/{ws}/email-finder/pattern", {
      params: { path: { ws }, query: { domain: patternDomain.trim() } },
    });
    setLooking(false);
    if (error) return setPatternErr(errorMessage(error));
    setPattern(data?.data ?? null);
  }

  return (
    <div className="space-y-4">
      <PageHeader
        title="Email finder"
        description="Propose a likely work email for someone at a company, and check whether an address is good. It learns only from this workspace's own leads, so it works best on companies you already have contacts at. Finding is free; checking an address may use your own connected verifier."
      />

      <Card title="Find a work email" description="Give a name and the company's website domain.">
        <form onSubmit={find} className="flex flex-wrap items-end gap-2">
          <label className="flex flex-col gap-1 text-[13px]">
            <span className="text-muted-foreground">First name</span>
            <input className={field} value={first} onChange={(e) => setFirst(e.target.value)} placeholder="Alice" required />
          </label>
          <label className="flex flex-col gap-1 text-[13px]">
            <span className="text-muted-foreground">Last name</span>
            <input className={field} value={last} onChange={(e) => setLast(e.target.value)} placeholder="Nguyen" required />
          </label>
          <label className="flex flex-col gap-1 text-[13px]">
            <span className="text-muted-foreground">Company domain</span>
            <input className={field} value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="acme.example" required />
          </label>
          <Button type="submit" disabled={finding}>
            <Search className="size-3.5" /> {finding ? "Finding…" : "Find email"}
          </Button>
        </form>
        {findErr && <div className="mt-3"><ErrorText>{findErr}</ErrorText></div>}
        {found && (
          <div className="mt-4 rounded-md border bg-muted/30 p-3">
            {found.email ? (
              <div className="flex flex-wrap items-center gap-3">
                <Mail className="size-4 text-muted-foreground" />
                <span className="font-mono text-[13px]">{found.email}</span>
                <CopyButton text={found.email} />
                <Outcome r={found} />
                {canManage && <Button variant="ghost" onClick={checkFound}>Check it</Button>}
              </div>
            ) : (
              <Outcome r={found} />
            )}
            <p className="mt-2 text-[13px] text-muted-foreground">
              {codes[found.code]?.note}
              {found.email && found.evidence ? ` · ${evidenceLabel[found.evidence] ?? found.evidence}` : ""}
            </p>
          </div>
        )}
      </Card>

      {canManage && (
        <Card title="Check an address" description="Run one address through the waterfall: free checks first, your own verifier only if needed.">
          <form onSubmit={verify} className="flex flex-wrap items-end gap-2">
            <label className="flex flex-1 flex-col gap-1 text-[13px]" style={{ minWidth: 240 }}>
              <span className="text-muted-foreground">Email address</span>
              <input className={field} type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="alice.nguyen@acme.example" required />
            </label>
            <Button type="submit" disabled={verifying}>
              <ShieldCheck className="size-3.5" /> {verifying ? "Checking…" : "Check address"}
            </Button>
          </form>
          {verifyErr && <div className="mt-3"><ErrorText>{verifyErr}</ErrorText></div>}
          {verified && (
            <div className="mt-4 rounded-md border bg-muted/30 p-3">
              <Outcome r={verified} />
              <p className="mt-2 text-[13px] text-muted-foreground">
                {codes[verified.code]?.note}
                {verified.cost && verified.cost.cents > 0 ? ` · used your ${verified.cost.connection || verified.cost.provider} verifier` : " · no third-party spend"}
              </p>
            </div>
          )}
        </Card>
      )}

      <Card title="Domain email pattern" description="See the address shape a company domain uses, learned from this workspace's own data.">
        <form onSubmit={lookup} className="flex flex-wrap items-end gap-2">
          <label className="flex flex-1 flex-col gap-1 text-[13px]" style={{ minWidth: 240 }}>
            <span className="text-muted-foreground">Company domain</span>
            <input className={field} value={patternDomain} onChange={(e) => setPatternDomain(e.target.value)} placeholder="acme.example" required />
          </label>
          <Button type="submit" variant="ghost" disabled={looking}>{looking ? "Looking…" : "Look up"}</Button>
        </form>
        {patternErr && <div className="mt-3"><ErrorText>{patternErr}</ErrorText></div>}
        {pattern && (
          <div className="mt-4">
            {pattern.sample_size === 0 ? (
              <p className="text-[13px] text-muted-foreground">
                Nothing learned for <span className="font-mono">{pattern.domain}</span> yet. Add a few contacts at this domain and it will pick up the shape.
              </p>
            ) : (
              <Stats cols={4}>
                <Stat label="Email shape" value={pattern.top_shape || "—"} sub={pattern.catch_all ? "catch-all domain" : undefined} tone={pattern.catch_all ? "amber" : undefined} />
                <Stat label="Confidence" value={`${pattern.confidence}%`} />
                <Stat label="Mail provider" value={pattern.provider || "Unknown"} />
                <Stat label="Learned from" value={`${pattern.sample_size} ${pattern.sample_size === 1 ? "address" : "addresses"}`} />
              </Stats>
            )}
            {pattern.catch_all && pattern.sample_size > 0 && (
              <p className="mt-2 text-[13px] text-muted-foreground">This domain accepts any address, so a match alone doesn't prove the mailbox exists — check the address to be sure.</p>
            )}
          </div>
        )}
      </Card>
    </div>
  );
}
