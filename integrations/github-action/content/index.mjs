// The Outflare content GitHub Action (README.md). It runs in the site
// owner's own repository with their own Outflare token, so Outflare never
// holds access to the repository. Each run:
//
//  1. lists the brand's approved pages;
//  2. reports a page live when its file is already on the base branch at the
//     approved revision (the pull request merged);
//  3. otherwise writes the page to a branch, outflare/content-<id>, and opens
//     a pull request for a person to review, or updates the one that's open.
//
// Every git and gh call takes its arguments as a list, never through a
// shell, because titles and bodies are text from outside the repository.
// It needs Node 20 or later and no packages.

import { execFileSync } from "node:child_process";
import { appendFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";

// --- Pure helpers (tested in index.test.mjs) ---

const safe = (s) => String(s ?? "").toLowerCase().replace(/[^a-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "");

// fill puts an item's values in a path or URL template. A value is reduced to
// letters, digits, dots, hyphens and underscores, so it can't add a folder or
// leave the repository; {path} is the page's placement path, which the server
// has already checked.
export function fill(template, item) {
  const values = {
    slug: safe(item.slug) || safe(item.id),
    id: safe(item.id),
    language: safe(item.language),
    collection: safe(item.placement?.collection),
    target: safe(item.target),
    path: item.placement?.path ?? "",
  };
  return template.replace(/\{(\w+)\}/g, (m, k) => (k in values ? values[k] : m));
}

// filePath is where an item's file goes in the repository, or throws when the
// template would put it outside.
export function filePath(template, item) {
  const p = fill(template.replaceAll("{path}", ""), item).replace(/\/{2,}/g, "/");
  const parts = p.split("/");
  if (!p || p.startsWith("/") || p.includes("\\") || parts.some((s) => s === "" || s === "." || s === ".." || s === ".git" || s === ".github")) {
    throw new Error(`the path "${p}" isn't a file in the repository; check the path input`);
  }
  return p;
}

// pageURL is the page's address on the site, or "" when the template needs a
// placement path the item doesn't have.
export function pageURL(siteURL, template, item) {
  if (!siteURL) return "";
  if (template.includes("{path}") && !item.placement?.path) return "";
  const p = fill(template, item);
  if (/^https?:\/\//i.test(p)) return new URL(p).href;
  return new URL(p.replace(/^\/*/, "/"), siteURL.replace(/\/*$/, "/")).href;
}

const q = (s) => JSON.stringify(String(s));

// markdownFile is the page as a Markdown file with front matter: the same
// fields as Copy Markdown on the Content page, plus outflare_id and
// outflare_revision, which tell later runs which version the file holds.
export function markdownFile(c) {
  const lines = ["---", `title: ${q(c.title)}`];
  if (c.slug) lines.push(`slug: ${q(c.slug)}`);
  if (c.excerpt) lines.push(`description: ${q(c.excerpt)}`);
  if (c.language) lines.push(`lang: ${q(c.language)}`);
  if (c.tags?.length) lines.push(`tags: [${c.tags.map(q).join(", ")}]`);
  if (c.canonical_url) lines.push(`canonical: ${q(c.canonical_url)}`);
  const seo = c.seo ?? {};
  if (seo.meta_title) lines.push(`meta_title: ${q(seo.meta_title)}`);
  if (seo.meta_description) lines.push(`meta_description: ${q(seo.meta_description)}`);
  if (seo.primary_keyword) lines.push(`keyword: ${q(seo.primary_keyword)}`);
  if (seo.robots) lines.push(`robots: ${q(seo.robots)}`);
  const hero = (c.media ?? []).find((m) => m.role === "featured");
  if (hero) {
    lines.push(`image: ${q(hero.url)}`);
    if (hero.alt) lines.push(`image_alt: ${q(hero.alt)}`);
  }
  if (c.placement?.nav_label) lines.push(`nav_label: ${q(c.placement.nav_label)}`);
  lines.push(`outflare_id: ${q(c.id)}`, `outflare_revision: ${Number(c.revision) || 0}`, "---", "", (c.body ?? "").replace(/\s*$/, ""), "");
  return lines.join("\n");
}

// revisionIn reads outflare_revision from a file's front matter, or -1.
export function revisionIn(text) {
  const m = /^---\n([\s\S]*?)\n---/.exec(text ?? "");
  const r = m && /^outflare_revision:\s*(\d+)\s*$/m.exec(m[1]);
  return r ? Number(r[1]) : -1;
}

// prBody is the pull request's description: what a reviewer needs to put the
// page in place well, from the item's brief, links and media.
export function prBody(c, url) {
  const out = [];
  const b = c.brief ?? {};
  out.push(`Approved in Outflare${c.approved_by ? ` by ${c.approved_by}` : ""}. Revision ${c.revision}.`);
  if (url) out.push(`It will be reported live at ${url} once this merges.`);
  if (b.goal || b.opportunity) out.push("", "## Why this page", ...[b.goal && `Goal: ${b.goal}`, b.opportunity].filter(Boolean));
  if (b.check_before_live?.length) out.push("", "## Check before it goes live", ...b.check_before_live.map((x) => `- [ ] ${x}`));
  const p = c.placement ?? {};
  if (p.type || p.parent || p.template_hint) {
    out.push("", "## Placement", ...[p.type && `- Type: ${p.type}`, p.path && `- Path: ${p.path}`, p.parent && `- Parent: ${p.parent}`, p.template_hint && `- Follow the layout of ${p.template_hint}`].filter(Boolean));
  }
  const from = c.links?.add_links_from ?? [];
  if (from.length) out.push("", "## Link to it from", ...from.map((l) => `- [ ] ${l.page}: link "${l.anchor}"${l.after_text ? ` after "${l.after_text}"` : ""}`));
  if (b.overlaps?.length) out.push("", "## Pages that overlap", ...b.overlaps.map((o) => `- ${o.page}${o.advice ? `: ${o.advice}` : ""}`));
  if (b.redirects?.length) out.push("", "## Redirects", ...b.redirects.map((r) => `- ${r.from} → ${r.to}`));
  if (c.media?.length) out.push("", "## Images", "Copy these onto the site's own storage.", ...c.media.map((m) => `- ${m.role ?? "image"}: ${m.url}${m.alt ? ` (alt: ${m.alt})` : ""}`));
  if (c.seo?.schema?.length) out.push("", "## Structured data", "```json", JSON.stringify(c.seo.schema, null, 2), "```");
  out.push("", "Edit the file on this branch as you like. Approving the page again in Outflare updates this pull request.");
  return out.join("\n");
}

// --- Running ---

const val = (v, def = "") => (v ?? def).trim();

function run(cmd, args, opts = {}) {
  return execFileSync(cmd, args, { encoding: "utf8", stdio: ["pipe", "pipe", "pipe"], ...opts }).trim();
}

function tryRun(cmd, args, opts) {
  try {
    return run(cmd, args, opts);
  } catch {
    return null;
  }
}

async function api(base, token, method, path, body) {
  const res = await fetch(base.replace(/\/*$/, "") + path, {
    method,
    headers: { Authorization: `Bearer ${token}`, Accept: "application/json", ...(body ? { "Content-Type": "application/json" } : {}), "User-Agent": "outflare-content-action" },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    // not JSON
  }
  if (!res.ok) {
    const e = new Error(`${method} ${path}: ${res.status} ${data?.error?.message ?? text.slice(0, 200)}`);
    e.status = res.status;
    throw e;
  }
  return data;
}

async function main() {
  const token = val(process.env.OUTFLARE_TOKEN);
  const server = val(process.env.OUTFLARE_URL, "https://app.outflare.ai");
  const brand = val(process.env.OUTFLARE_BRAND);
  const pathTemplate = val(process.env.OUTFLARE_PATH, "content/{target}/{slug}.md");
  const siteURL = val(process.env.OUTFLARE_SITE_URL);
  const urlTemplate = val(process.env.OUTFLARE_PAGE_URL, "/{slug}/");
  const reportLive = val(process.env.OUTFLARE_REPORT_LIVE, "true") !== "false";
  const labels = val(process.env.OUTFLARE_LABELS);
  if (!token) throw new Error("outflare-token is empty: add an Outflare API token with content.read and content.manage as a repository secret");
  if (!/^[0-9a-f-]{36}$/i.test(brand)) throw new Error("brand must be the brand's id");

  const base = val(process.env.OUTFLARE_BASE) || run("gh", ["repo", "view", "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name"]);
  run("git", ["fetch", "--quiet", "origin", base]);

  const enc = encodeURIComponent(brand);
  const items = [];
  for (let cursor = ""; ; ) {
    const page = await api(server, token, "GET", `/v1/brands/${enc}/content?status=approved&limit=200${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`);
    items.push(...page.data);
    if (!page.next_cursor) break;
    cursor = page.next_cursor;
  }
  console.log(`${items.length} approved page(s)`);

  let opened = 0, reported = 0, failed = 0;
  for (const listed of items) {
    try {
      const c = await api(server, token, "GET", `/v1/brands/${enc}/content/${encodeURIComponent(listed.id)}`);
      const file = filePath(pathTemplate, c);
      const url = pageURL(siteURL, urlTemplate, c);

      // Merged already: report it live.
      if (revisionIn(tryRun("git", ["show", `origin/${base}:${file}`])) === c.revision) {
        if (!reportLive) continue;
        if (!url) {
          console.log(`::warning::${c.title}: merged, but not reported live: set site-url (and url when it uses {path})`);
          continue;
        }
        await api(server, token, "POST", `/v1/brands/${enc}/content/${encodeURIComponent(c.id)}/delivered`, { url, revision: c.revision });
        console.log(`Reported live: ${c.title} at ${url}`);
        reported++;
        continue;
      }

      const branch = `outflare/content-${safe(c.id)}`;
      const prs = JSON.parse(run("gh", ["pr", "list", "--head", branch, "--state", "all", "--json", "number,state,headRefOid", "--limit", "20"]) || "[]");
      const onBranch = tryRun("git", ["ls-remote", "--exit-code", "--heads", "origin", branch]) !== null;
      if (onBranch) {
        run("git", ["fetch", "--quiet", "origin", branch]);
        if (revisionIn(tryRun("git", ["show", `origin/${branch}:${file}`])) === c.revision) {
          // This revision is on its branch already. A pull request closed
          // without merging means a person turned it down: leave it.
          if (prs.some((p) => p.state === "OPEN" || p.state === "CLOSED")) continue;
        }
      }

      // Write this revision on a fresh branch from the base.
      const dir = mkdtempSync(join(tmpdir(), "outflare-"));
      try {
        run("git", ["worktree", "add", "--quiet", "-B", branch, dir, `origin/${base}`]);
        mkdirSync(dirname(join(dir, file)), { recursive: true });
        writeFileSync(join(dir, file), markdownFile(c));
        run("git", ["add", "--", file], { cwd: dir });
        run("git", ["-c", "user.name=Outflare", "-c", "user.email=outflare@users.noreply.github.com", "commit", "--quiet", "-m", `${c.title}\n\nApproved in Outflare, revision ${c.revision}.`], { cwd: dir });
        run("git", ["push", "--quiet", "--force", "origin", `HEAD:refs/heads/${branch}`], { cwd: dir });
      } finally {
        tryRun("git", ["worktree", "remove", "--force", dir]);
        rmSync(dir, { recursive: true, force: true });
      }

      const body = prBody(c, url);
      const open = prs.find((p) => p.state === "OPEN");
      if (open) {
        run("gh", ["pr", "edit", String(open.number), "--title", c.title, "--body", body]);
        console.log(`Updated #${open.number}: ${c.title} (revision ${c.revision})`);
      } else {
        const args = ["pr", "create", "--base", base, "--head", branch, "--title", c.title, "--body", body];
        for (const l of labels.split(",").map((s) => s.trim()).filter(Boolean)) args.push("--label", l);
        console.log(`Opened ${run("gh", args)}: ${c.title}`);
      }
      opened++;
    } catch (err) {
      failed++;
      console.log(`::error::${listed.title}: ${err.message}`);
    }
  }

  if (process.env.OUTFLARE_OUTPUT_FILE) appendFileSync(process.env.OUTFLARE_OUTPUT_FILE, `opened=${opened}\nreported=${reported}\n`);
  console.log(`Opened or updated ${opened}, reported live ${reported}, failed ${failed}.`);
  if (failed) process.exitCode = 1;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  main().catch((err) => {
    console.log(`::error::${err.message}`);
    process.exitCode = 1;
  });
}
