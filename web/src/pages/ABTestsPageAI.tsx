import { pickedBrandId } from "@/lib/brand";
// The versions of an A/B test, one card each: what changes, a preview on a
// phone or a computer, the visual editor on the page itself, and the
// workspace's own AI writing changes from a sentence. Nothing the AI writes
// reaches visitors until a person keeps it on a version.

import { useEffect, useState } from "react";
import type { ABTest, ABTestAIRun, ABTestPreviews, Flag, FlagChange, FlagVariant } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, ErrorText, field, toast } from "../ui";
import { plural } from "../format";
import { AIRunCard, useAIRun, VersionsPanel } from "./ABTestsAI";

type Payload = { changes?: FlagChange[]; css?: string; redirect?: string };
const payloadOf = (v?: FlagVariant): Payload => (v?.payload && typeof v.payload === "object" && !Array.isArray(v.payload) ? (v.payload as Payload) : {});

const clip = (s: string, n = 60) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

// describeChange says in words what one change does.
export function describeChange(c: FlagChange): string {
  const where = c.device === "mobile" ? " (phones only)" : c.device === "desktop" ? " (computers only)" : "";
  let what = "";
  if (c.text !== undefined) what = `Text: “${clip(c.text)}”`;
  else if (c.rich !== undefined) what = `Text: “${clip(c.rich.replace(/\*\*|\*|\[|\]\([^)]*\)/g, ""))}”`;
  else if (c.hide) what = "Hidden";
  else if (c.href !== undefined) what = `Link goes to ${c.href}`;
  else if (c.src !== undefined || c.alt !== undefined) what = c.src ? "New image" : `Image text: “${clip(c.alt ?? "")}”`;
  else if (c.style) what = `Style: ${Object.entries(c.style).map(([k, v]) => `${k} ${v}`).join(", ")}`;
  else if (c.move) what = `Moved ${c.move} ${c.target}`;
  else if (c.insert) what = `New ${c.insert.kind} ${c.insert.where} it${c.insert.text ? `: “${clip(c.insert.text)}”` : ""}${c.insert.href ? ` → ${c.insert.href}` : ""}`;
  return `${what}${where}`;
}

// openEditor opens the visual editor on the page for one version.
export async function openEditor(flag: Flag, variant: string, page: string): Promise<string> {
  if (!/^https?:\/\/[^/\s]+/i.test(page.trim())) return "Enter the address of a page on your site to edit, starting with https://.";
  const win = window.open("about:blank", "_blank");
  const { data, error } = await api.POST("/v1/brands/{brand}/flags/{flag}/edit-link", { params: { path: { brand: pickedBrandId(), flag: flag.id } }, body: { variant, url: page.trim() } });
  if (error || !data) {
    win?.close();
    return errorMessage(error);
  }
  if (win) {
    win.opener = null;
    win.location.href = data.url;
  } else window.location.href = data.url;
  toast(`The editor is open on your page for ${variant}. Save there, and the changes show up here.`);
  return "";
}

function openPreview(url: string, phone: boolean) {
  const w = phone ? window.open(url, "of_phone", "width=390,height=844") : window.open(url, "_blank");
  if (w) w.opener = null;
}

const examples = [
  "Make the headline shorter and more direct",
  "Add a line about the free trial under the headline",
  "Make the main button stand out more",
  "Move the customer logos above the fold",
];

// VersionsTab lists the test's versions. Today's page (the control) only
// previews; each other version can be edited, described to the AI, or
// previewed. A test of a developer's flag edits its content instead.
export function VersionsTab({ ws, test, flag, canManage, onFlagChanged, go }: { ws: string; test: ABTest; flag?: Flag; canManage: boolean; onFlagChanged: () => void; go: (t: "ai") => void }) {
  const [previews, setPreviews] = useState<ABTestPreviews | null>(null);
  const [page, setPage] = useState(test.page.replace(/\*$/, ""));
  const [asking, setAsking] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/ab-tests/{test}/previews", { params: { path: { brand: pickedBrandId(), test: test.id } } }).then(({ data }) => setPreviews(data ?? null));
  }, [test.id, flag?.updated_at]);
  if (!flag) return <p className="text-[13px] text-muted-foreground">This test's flag is gone.</p>;
  const linkFor = (v: string) => previews?.pages.flatMap((p) => p.links).find((l) => l.variant === v)?.url;
  const checkFor = (v: string) => previews?.versions.find((x) => x.key === v)?.check;
  const noCode = !!test.page;
  const wild = !test.page || test.page.endsWith("*");
  const editable = canManage && noCode;

  return (
    <div className="space-y-4 text-[13px]">
      {noCode && test.status === "draft" && canManage && <PageAIBox ws={ws} test={test} flag={flag} onSaved={onFlagChanged} go={go} />}
      {editable && wild && (
        <label className="block">
          <span className="mb-1 block font-medium">Page to edit on</span>
          <input className={`${field} w-full`} type="url" placeholder="https://northpeak.example/pricing" value={page} onChange={(e) => setPage(e.target.value)} />
          <span className="mt-1 block text-[12px] text-muted-foreground">The test runs on every page under {test.page}; pick one of them to edit on.</span>
        </label>
      )}
      <ErrorText>{error}</ErrorText>
      <div className="grid gap-3 md:grid-cols-2">
        {flag.variants.map((v) => {
          const p = payloadOf(v);
          const cs = p.changes ?? [];
          const control = v.key === test.control;
          const link = linkFor(v.key);
          const check = checkFor(v.key);
          return (
            <div key={v.key} className="space-y-2 rounded-lg border p-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono font-medium">{v.key}</span>
                {control && <Badge>Today's page</Badge>}
                {check && !check.stale && <Badge tone={check.status === "ok" ? "green" : "red"}>{check.status === "ok" ? "Looks right" : "Broken"}</Badge>}
                {check?.stale && <Badge tone="amber">Changed since checked</Badge>}
                <span className="ml-auto text-[12px] text-muted-foreground">{v.weight}% of visitors</span>
              </div>
              {control ? (
                <p className="text-muted-foreground">What visitors see today, unchanged. The other versions are compared with it.</p>
              ) : p.redirect ? (
                <p>Sends visitors to <span className="font-mono">{p.redirect}</span>.</p>
              ) : noCode ? (
                cs.length === 0 && !p.css ? (
                  <p className="text-amber-700 dark:text-amber-400">No changes yet: it looks like today's page.</p>
                ) : (
                  <ul className="space-y-0.5">
                    {cs.slice(0, 6).map((c, i) => <li key={i} className="truncate" title={c.selector}>{describeChange(c)}</li>)}
                    {cs.length > 6 && <li className="text-muted-foreground">and {plural(cs.length - 6, "more change")}</li>}
                    {p.css && <li className="text-muted-foreground">Its own CSS</li>}
                  </ul>
                )
              ) : (
                <p className="text-muted-foreground">{v.payload ? clip(JSON.stringify(v.payload), 140) : "Shown by your own code."}</p>
              )}
              <div className="flex flex-wrap gap-2">
                {link && (
                  <>
                    <Button variant="ghost" onClick={() => openPreview(link, false)}>Preview</Button>
                    <Button variant="ghost" onClick={() => openPreview(link, true)}>On a phone</Button>
                  </>
                )}
                {editable && !control && (
                  <>
                    <Button variant="ghost" onClick={async () => setError(await openEditor(flag, v.key, page))}>Edit on your page</Button>
                    {!p.redirect && <Button variant="ghost" onClick={() => setAsking(asking === v.key ? "" : v.key)}>Describe a change</Button>}
                  </>
                )}
              </div>
              {asking === v.key && <PageAIBox ws={ws} test={test} flag={flag} version={v.key} onSaved={() => { setAsking(""); onFlagChanged(); }} go={go} />}
            </div>
          );
        })}
      </div>
      {!noCode && canManage && test.status === "draft" && (
        <div className="space-y-2 rounded-md border p-3">
          <span className="block font-medium">New versions with AI</span>
          <VersionsPanel ws={ws} flag={flag} onAdded={onFlagChanged} />
        </div>
      )}
      {previews && <p className="text-[12px] text-muted-foreground">{previews.hint}</p>}
    </div>
  );
}

type PageVersion = { key: string; summary: string; rationale: string; changes: FlagChange[] };

// PageAIBox asks the workspace's AI for changes to the test's page: new
// versions, or (with version) more changes to one. A person keeps or
// discards each one.
export function PageAIBox({ ws, test, flag, version, onSaved, go }: { ws: string; test: ABTest; flag: Flag; version?: string; onSaved: () => void; go: (t: "ai") => void }) {
  const ai = useAIRun(ws);
  const [prompt, setPrompt] = useState("");
  const [count, setCount] = useState(1);
  const [kept, setKept] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const out = ai.ready?.output as { mode?: string; versions?: PageVersion[] } | null;

  // A run started before the page was left carries on: pick it up.
  useEffect(() => {
    let live = true;
    api.GET("/v1/brands/{brand}/ab-test-ai-runs", { params: { path: { brand: pickedBrandId() }, query: { test: test.id, kind: "versions" } } }).then(({ data }) => {
      const r = data?.data.find((x: ABTestAIRun) => (x.status === "queued" || x.status === "running") && ((x.input as { version?: string } | null)?.version ?? "") === (version ?? ""));
      if (live && r) ai.resume(r);
    });
    return () => {
      live = false;
    };
  }, [test.id, version]); // eslint-disable-line react-hooks/exhaustive-deps

  const ask = () =>
    ai.start(() =>
      api.POST("/v1/brands/{brand}/ab-tests/versions", { params: { path: { brand: pickedBrandId() } }, body: { test: test.id, prompt, ...(version ? { version } : { count }) } }),
    );
  useEffect(() => setKept([]), [ai.run?.id]);

  // keep saves one proposal: on version, after its own changes; otherwise
  // on the first version with no changes yet, or a new version.
  async function keep(v: PageVersion) {
    setBusy(true);
    try {
      let target = version;
      if (!target) target = flag.variants.find((x) => x.key !== test.control && !kept.includes(x.key) && !(payloadOf(x).changes?.length) && !payloadOf(x).redirect && !payloadOf(x).css)?.key;
      if (!target) {
        if (test.status !== "draft") return toast("Add versions while the test is a draft.", "error");
        const used = new Set(flag.variants.map((x) => x.key));
        target = v.key;
        for (let i = 0; used.has(target); i++) target = String.fromCharCode(98 + i);
        const n = flag.variants.length + 1;
        const even = Math.floor(100 / n);
        const variants: FlagVariant[] = [...flag.variants, { key: target, weight: 0 }].map((x, i) => ({ ...x, weight: i === 0 ? 100 - even * (n - 1) : even }));
        const { error } = await api.PATCH("/v1/brands/{brand}/flags/{flag}", { params: { path: { brand: pickedBrandId(), flag: flag.id } }, body: { variants } });
        if (error) return toast(errorMessage(error), "error");
      }
      const cur = await api.GET("/v1/brands/{brand}/ab-tests/{test}/versions/{version}", { params: { path: { brand: pickedBrandId(), test: test.id, version: target } } });
      if (cur.error) return toast(errorMessage(cur.error), "error");
      const changes = version ? [...cur.data.changes, ...v.changes] : v.changes;
      const { error } = await api.PUT("/v1/brands/{brand}/ab-tests/{test}/versions/{version}", { params: { path: { brand: pickedBrandId(), test: test.id, version: target } }, body: { changes, css: cur.data.css, redirect: cur.data.redirect } });
      if (error) return toast(errorMessage(error), "error");
      setKept((k) => [...k, v.key, target!]);
      toast(`Kept on version ${target}. Preview it before you start.`);
      onSaved();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-3 rounded-lg border bg-muted/30 p-3 text-[13px]">
      <span className="block font-medium">{version ? `Describe a change to version ${version}` : "Describe the versions you want"}</span>
      <textarea
        aria-label="What to change"
        className={`${field} w-full`}
        rows={2}
        maxLength={2000}
        placeholder={version ? "e.g. make the button green and say Start free" : "e.g. under the header, add a line about premium plans and a Try it button to /sign-up"}
        value={prompt}
        onChange={(e) => setPrompt(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && prompt.trim() && !ai.busy) ask();
        }}
      />
      {!prompt && (
        <div className="flex flex-wrap gap-1.5">
          {examples.map((x) => (
            <button key={x} type="button" className="rounded-full border px-2.5 py-0.5 text-[12px] text-muted-foreground hover:text-foreground" onClick={() => setPrompt(x)}>{x}</button>
          ))}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button disabled={ai.busy || !prompt.trim()} onClick={ask}>{ai.busy ? "Working…" : out ? "Ask again" : version ? "Write the change" : "Write versions"}</Button>
        {!version && (
          <select aria-label="How many versions" className={field} value={count} onChange={(e) => setCount(Number(e.target.value))}>
            {[1, 2, 3].map((n) => <option key={n} value={n}>{plural(n, "version")}</option>)}
          </select>
        )}
        <span className="text-[12px] text-muted-foreground">Your AI model reads the page and changes only what's on it.</span>
      </div>
      <AIRunCard ai={ai} working={version ? "Reading your page and writing the change" : "Reading your page and writing versions"} onRetry={prompt.trim() ? ask : undefined} go={go} />
      {out?.versions?.map((v) => {
        const done = kept.includes(v.key);
        return (
          <div key={v.key} className="space-y-1.5 rounded-md border bg-background p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="font-medium">{v.summary || v.key}</span>
              {done ? <Badge tone="green">Kept</Badge> : (
                <div className="flex gap-2">
                  <Button disabled={busy} onClick={() => keep(v)}>{version ? `Keep on ${version}` : "Keep"}</Button>
                  <Button variant="ghost" disabled={busy} onClick={() => setKept((k) => [...k, `discarded:${v.key}`])}>Discard</Button>
                </div>
              )}
            </div>
            {!kept.includes(`discarded:${v.key}`) && (
              <>
                <ul className="list-disc space-y-0.5 pl-5">{v.changes.map((c, i) => <li key={i}>{describeChange(c)}</li>)}</ul>
                {v.rationale && <p className="text-[12px] text-muted-foreground">{v.rationale}</p>}
              </>
            )}
          </div>
        );
      })}
      {ai.ready && (
        <p className="text-[12px] text-muted-foreground">
          Written by {ai.ready.model}. Anything it suggested that isn't allowed on a page (scripts, outside images) was left out. Check the words are true, then preview before you start.
        </p>
      )}
    </div>
  );
}
