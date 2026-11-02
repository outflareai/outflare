import { chromium } from "playwright";
import assert from "node:assert/strict";
const base = process.env.OUTFLARE_URL ?? "http://127.0.0.1:8095";
if (!["localhost", "127.0.0.1"].includes(new URL(base).hostname)) throw new Error("Use a local fictional demo workspace.");
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox"] });
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
const requests = [];
page.on("request", (r) => { const url = new URL(r.url()); if (r.method() === "GET" && url.pathname.endsWith("/records")) requests.push(url); });
async function call(method, path, data) {
  const res = await page.request.fetch(base + path, { method, data });
  const body = await res.json();
  assert(res.ok(), `${method} ${path}: ${res.status()} ${JSON.stringify(body)}`);
  return body;
}
try {
  await call("POST", "/v1/auth/login", { email: "maya@northpeak.example", password: "demo-password-not-real" });
  const ws = (await call("GET", "/v1/me")).workspaces[0];
  const brand = (await call("GET", `/v1/workspaces/${ws.id}/brands`)).data[0];
  const records = `/v1/brands/${brand.id}/records`;
  for (const type of ["person", "deal"]) {
    const prefix = `Paging ${type} `;
    const existing = (await call("GET", records + `?q=${encodeURIComponent(prefix)}&limit=200`)).data;
    for (let i = 0; i < 61; i++) {
      const name = prefix + String(i).padStart(3, "0");
      if (!existing.some((r) => r.name === name)) await call("POST", records, { type, name, ...(type === "deal" ? { attributes: { stage: "lead" } } : {}) });
    }
  }
  await page.goto(base + `/${ws.slug}/${brand.handle}/crm`);
  await page.getByRole("heading", { name: "People", exact: true }).waitFor();
  await page.getByLabel("Search people", { exact: true }).fill("Paging person");
  await page.getByLabel("Sort records").selectOption("name");
  await page.getByRole("button", { name: "Sort ascending", exact: true }).click();
  await page.getByRole("button", { name: "Open Paging person 000", exact: true }).waitFor();
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await page.getByRole("button", { name: "Open Paging person 050", exact: true }).waitFor();
  assert.equal(await page.getByRole("button", { name: "Open Paging person 000", exact: true }).count(), 0);
  assert(await page.getByRole("button", { name: "Next", exact: true }).isDisabled());
  await page.getByRole("button", { name: "Previous", exact: true }).click();
  await page.getByRole("button", { name: "Open Paging person 000", exact: true }).waitFor();
  await page.getByRole("button", { name: "Deals", exact: true }).click();
  await page.getByLabel("Search deals", { exact: true }).fill("Paging deal");
  await page.getByLabel("Sort records").selectOption("name");
  await page.getByRole("button", { name: "Sort ascending", exact: true }).click();
  await page.getByRole("button", { name: "Open Paging deal 000", exact: true }).waitFor();
  await page.getByRole("button", { name: "Show more (11)", exact: true }).click();
  await page.getByRole("button", { name: "Open Paging deal 060", exact: true }).waitFor();
  assert.equal(await page.getByRole("button", { name: "Open Paging deal 000", exact: true }).count(), 1);
  assert(requests.some((u) => u.searchParams.has("cursor")), "No server-issued cursor was used");
  assert(requests.every((u) => !u.searchParams.has("offset") && !u.searchParams.has("before")), "Legacy pagination parameter used");
  assert.deepEqual(errors, []);
  console.log("PASS: table next/previous and independent board Show more follow server-issued cursors without missing or duplicate records.");
} finally { await browser.close(); }
