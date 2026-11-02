import { chromium } from "playwright";
import assert from "node:assert/strict";
const base = process.env.OUTFLARE_URL ?? "http://127.0.0.1:8090";
if (!["localhost", "127.0.0.1"].includes(new URL(base).hostname))
  throw new Error("CRM smoke tests require a local fictional demo workspace.");
const browser = await chromium.launch({
  executablePath: "/usr/bin/chromium",
  args: ["--no-sandbox"],
});
const context = await browser.newContext({
  viewport: { width: 1440, height: 1000 },
});
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
async function call(method, path, data) {
  const r = await page.request.fetch(base + path, { method, data });
  const body = await r.json();
  assert(r.ok(), `${method} ${path}: ${r.status()} ${JSON.stringify(body)}`);
  return body;
}
try {
  const setup = await call("GET", "/v1/setup");
  if (setup.needed)
    await call("POST", "/v1/setup", {
      email: "maya@northpeak.example",
      name: "Maya Lindqvist",
      password: "demo-password-not-real",
      workspace_name: "Northpeak",
      telemetry: false,
    });
  await call("POST", "/v1/auth/login", {
    email: "maya@northpeak.example",
    password: "demo-password-not-real",
  });
  const me = await call("GET", "/v1/me");
  const ws = me.workspaces[0];
  const brands = await call("GET", `/v1/workspaces/${ws.id}/brands`);
  const brand = brands.data[0];
  const records = `/v1/workspaces/${ws.id}/records`;
  const existing = await call("GET", records + "?limit=200");
  const company =
    existing.data.find((r) => r.type === "account") ??
    (await call("POST", records, {
      type: "account",
      brand_id: brand.id,
      name: "Acme",
      identities: [{ namespace: "domain", value: "acme.example" }],
      attributes: {
        website: "https://acme.example",
        industry: "software",
        company_size: "11_50",
      },
    }).catch(async () =>
      call("POST", records, {
        type: "account",
        brand_id: brand.id,
        name: "Acme",
        attributes: { website: "https://acme.example" },
      }),
    ));
  const person =
    existing.data.find((r) => r.type === "person") ??
    (await call("POST", records, {
      type: "person",
      brand_id: brand.id,
      identities: [{ namespace: "email", value: "ana@acme.example" }],
      attributes: {
        first_name: "Ana",
        last_name: "Diaz",
        job_title: "Founder",
        companies: [company.id],
      },
    }));
  const deal =
    existing.data.find((r) => r.type === "deal") ??
    (await call("POST", records, {
      type: "deal",
      brand_id: brand.id,
      name: "Acme annual plan",
      attributes: {
        stage: "lead",
        value: { amount_minor: 120000, currency: "USD" },
        associated_company: company.id,
        associated_people: [person.id],
      },
    }));
  const root = `/${ws.slug}/${brand.handle}/crm`;
  await page.goto(base + root + "/person");
  await page.getByRole("heading", { name: "People", exact: true }).waitFor();
  await page.getByRole("button", { name: /Ana Diaz/ }).click();
  await page.getByRole("heading", { name: "Ana Diaz", exact: true }).waitFor();
  await page.getByRole("tab", { name: /notes/i }).click();
  await page.getByRole("button", { name: "Write a note", exact: true }).click();
  await page.getByLabel("Title", { exact: true }).fill("Discovery call");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page
    .getByRole("heading", { name: "Discovery call", exact: true })
    .first()
    .waitFor();
  await page.getByRole("button", { name: "Close", exact: true }).last().click();
  await page.getByRole("button", { name: "Deals", exact: true }).click();
  await page.getByRole("button", { name: /Acme annual plan/ }).waitFor();
  await Promise.all([
    page.waitForResponse(
      (r) =>
        r.url().includes("/records/") &&
        r.request().method() === "PATCH" &&
        r.status() === 200,
    ),
    page.getByLabel("Move Acme annual plan to stage").selectOption("qualified"),
  ]);
  await page.getByRole("button", { name: "Save view", exact: true }).click();
  await page.getByLabel("View name", { exact: true }).fill("Active pipeline");
  await page
    .getByRole("button", { name: "Save view", exact: true })
    .last()
    .click();
  await page
    .getByLabel("Saved view")
    .getByRole("option", { name: "Active pipeline · Personal" })
    .first()
    .waitFor({ state: "attached" });
  await page.goto(base + `/${ws.slug}/${brand.handle}/leads`);
  await page
    .getByRole("button", { name: "Add lead", exact: true })
    .first()
    .click();
  await page.getByLabel("Email", { exact: true }).fill("ana@acme.example");
  await page
    .getByText("Ana Diaz is already in your CRM.", { exact: false })
    .waitFor();
  assert.equal(
    await page.getByLabel("First name", { exact: true }).inputValue(),
    "Ana",
  );
  await page
    .getByRole("button", { name: "Add lead", exact: true })
    .last()
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const leads = await call("GET", `/v1/workspaces/${ws.id}/leads?q=ana`);
  assert.equal(leads.data[0].record_id, person.id);
  const summary = await call("GET", `/v1/brands/${brand.id}/crm/summary`);
  assert.equal(summary.pipeline[0].currency, "USD");
  await page.goto(base + root + "/collections");
  const existingLists = await call(
    "GET",
    `/v1/workspaces/${ws.id}/catalog/types`,
  );
  if (!existingLists.data.some((t) => t.key === "list_strategic_accounts")) {
    await page.getByRole("button", { name: "New list", exact: true }).click();
    await page
      .getByLabel("List name", { exact: true })
      .fill("Strategic accounts");
    await page
      .getByLabel("List record type", { exact: true })
      .selectOption("account");
    await page
      .getByRole("button", { name: "Create list", exact: true })
      .click();
  } else await page.getByRole("button", { name: /Strategic accounts/ }).click();
  const priorEntries = await call(
    "GET",
    records + "?type=list_strategic_accounts",
  );
  if (
    !priorEntries.data.some(
      (entry) => entry.attributes.outflare_parent_record === company.id,
    )
  ) {
    await page
      .getByRole("button", { name: /Add strategic accounts entry/i })
      .click();
    await page.getByLabel("Linked record", { exact: true }).click();
    await page.getByRole("option", { name: /Acme/ }).click();
    await page
      .getByRole("dialog")
      .last()
      .getByRole("button", { name: "Add", exact: true })
      .click();
    await page.getByRole("heading", { name: "Acme", exact: true }).waitFor();
  }
  const entries = await call("GET", records + "?type=list_strategic_accounts");
  assert.equal(entries.data[0].attributes.outflare_parent_record, company.id);
  await page.goto(base + root + "/person");
  await page.getByRole("button", { name: /Ana Diaz/ }).waitFor();
  await page.screenshot({ path: "/tmp/crm-desktop.png", fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: "/tmp/crm-mobile.png", fullPage: true });
  assert.deepEqual(errors, []);
  console.log(
    "PASS: record preview, notes, pipeline update, saved view, lead autofill, existing-record reuse, summary, native lists and linked entries, desktop/mobile render.",
  );
} catch (e) {
  console.error(e);
  await page.screenshot({ path: "/tmp/crm-failure.png", fullPage: true });
  process.exitCode = 1;
} finally {
  await browser.close();
}
