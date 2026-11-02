import { chromium } from "playwright";
import assert from "node:assert/strict";
const base = process.env.OUTFLARE_URL ?? "http://127.0.0.1:8092";
if (!["localhost", "127.0.0.1"].includes(new URL(base).hostname))
  throw new Error("Use only a local fictional demo workspace.");
const browser = await chromium.launch({
  executablePath: "/usr/bin/chromium",
  args: ["--no-sandbox"],
});
const adminContext = await browser.newContext({
  viewport: { width: 1440, height: 1000 },
});
const salesContext = await browser.newContext();
const page = await adminContext.newPage();
const sales = await salesContext.newPage();
const errors = [];
for (const p of [page, sales]) p.on("pageerror", (e) => errors.push(e.message));
async function call(p, method, path, data) {
  const r = await p.request.fetch(base + path, { method, data });
  const body = await r.json();
  assert(r.ok(), `${method} ${path}: ${r.status()} ${JSON.stringify(body)}`);
  return body;
}
try {
  await call(page, "POST", "/v1/auth/login", {
    email: "maya@northpeak.example",
    password: "demo-password-not-real",
  });
  const me = await call(page, "GET", "/v1/me");
  const ws = me.workspaces[0];
  const brand = (await call(page, "GET", `/v1/workspaces/${ws.id}/brands`))
    .data[0];
  const root = `/${ws.slug}/${brand.handle}/crm`;
  const api = `/v1/brands/${brand.id}/crm/mail`;
  let login = await sales.request.post(base + "/v1/auth/login", {
    data: {
      email: "sales@northpeak.example",
      password: "demo-password-not-real",
    },
  });
  if (!login.ok()) {
    const invite = await call(page, "POST", `/v1/workspaces/${ws.id}/invites`, {
      email: "sales@northpeak.example",
      role: "operator",
    });
    await call(sales, "POST", "/v1/invites/accept", {
      token: invite.token,
      name: "Sales colleague",
      password: "demo-password-not-real",
    });
  }
  const salesMe = (await call(sales, "GET", "/v1/me")).user;
  const policy = await call(page, "GET", api + "/policy");
  await call(page, "PUT", api + "/policy", {
    ...policy,
    internal_domains: ["northpeak.example"],
  });
  const rollout = await call(page, "GET", api + "/rollout");
  await call(page, "PUT", api + "/rollout", {
    enabled: false,
    allowed_user_ids: [],
    excluded_user_ids: [],
    version: rollout.version,
  });
  await page.goto(base + root + "/settings");
  await page
    .getByRole("heading", { name: "Sales eligibility", exact: true })
    .waitFor();
  await page
    .getByLabel("Enable individual Google connections for selected members", {
      exact: true,
    })
    .check();
  async function chooseMembers(label, names) {
    const trigger = page.getByLabel(label, { exact: true });
    await trigger.click();
    const id = await trigger.getAttribute("aria-controls");
    assert(id);
    const list = page.locator(`[id="${id}"]`);
    for (const name of names)
      await list
        .getByRole("option", {
          name: `${name} · ${name === me.user.name ? me.user.email : salesMe.email}`,
          exact: true,
        })
        .click();
    await page.keyboard.press("Escape");
    await list.waitFor({ state: "hidden" });
  }
  await chooseMembers("Eligible sales members", [me.user.name, salesMe.name]);
  await chooseMembers("Always excluded members", [me.user.name]);
  await Promise.all([
    page.waitForResponse(
      (r) =>
        r.url().endsWith("/rollout") &&
        r.request().method() === "PUT" &&
        r.status() === 200,
    ),
    page
      .getByRole("button", { name: "Save sales eligibility", exact: true })
      .click(),
  ]);
  const saved = await call(page, "GET", api + "/rollout");
  assert(saved.allowed_user_ids.includes(salesMe.id));
  assert(saved.excluded_user_ids.includes(me.user.id));
  assert.equal((await call(page, "GET", api + "/connection")).eligible, false);
  assert.equal((await call(sales, "GET", api + "/connection")).eligible, true);
  const records = (
    await call(
      sales,
      "GET",
      `/v1/workspaces/${ws.id}/records?type=person&limit=200`,
    )
  ).data;
  const person = records.find((r) =>
    r.identities.some(
      (i) => i.namespace === "email" && i.value === "ana@acme.example",
    ),
  );
  assert(person, "Run crm-smoke.mjs first.");
  const target = api + `/records/${person.id}/capture`;
  const prior = await call(sales, "GET", target);
  await call(sales, "PUT", target, { enabled: false, version: prior.version });
  await sales.goto(base + root + "/person");
  await sales.getByRole("button", { name: /Open Ana Diaz/ }).click();
  await sales.getByRole("tab", { name: "email", exact: true }).click();
  await Promise.all([
    sales.waitForResponse(
      (r) =>
        r.url().endsWith("/capture") &&
        r.request().method() === "PUT" &&
        r.status() === 200,
    ),
    sales
      .getByLabel("Include this contact/account in my Google capture", {
        exact: true,
      })
      .check(),
  ]);
  assert.equal((await call(sales, "GET", target)).enabled, true);
  await sales.goto(base + root + "/settings");
  await sales
    .getByRole("heading", { name: "My Google account", exact: true })
    .waitFor();
  assert.equal(
    await sales
      .getByRole("heading", { name: "Sales eligibility", exact: true })
      .count(),
    0,
  );
  // Local QA deliberately has no real Google app or credentials.
  assert.equal(
    (await call(sales, "GET", api + "/connection")).configured,
    false,
  );
  await sales
    .getByText("Google app setup is required.", { exact: true })
    .waitFor();
  assert.equal(
    await sales
      .getByRole("button", { name: "Connect my Google account", exact: true })
      .count(),
    0,
  );
  await page
    .getByText("Google Workspace administrator setup", { exact: true })
    .click();
  const setup = await call(page, "GET", api + "/connection");
  assert.equal(
    await page
      .getByLabel("Personal Google redirect URI", { exact: true })
      .inputValue(),
    setup.redirect_uri,
  );
  await page.screenshot({
    path: "/tmp/crm-google-rollout.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: "/tmp/crm-google-rollout-mobile.png",
    fullPage: true,
  });
  await page
    .getByLabel("Enable individual Google connections for selected members", {
      exact: true,
    })
    .uncheck();
  await Promise.all([
    page.waitForResponse(
      (r) =>
        r.url().endsWith("/rollout") &&
        r.request().method() === "PUT" &&
        r.status() === 200,
    ),
    page
      .getByRole("button", { name: "Save sales eligibility", exact: true })
      .click(),
  ]);
  assert.equal((await call(sales, "GET", api + "/connection")).eligible, false);
  await sales.goto(base + root + "/person");
  await sales.getByRole("button", { name: /Open Ana Diaz/ }).click();
  await sales.getByRole("tab", { name: "email", exact: true }).click();
  await Promise.all([
    sales.waitForResponse(
      (r) =>
        r.url().endsWith("/capture") &&
        r.request().method() === "PUT" &&
        r.status() === 200,
    ),
    sales
      .getByLabel("Include this contact/account in my Google capture", {
        exact: true,
      })
      .uncheck(),
  ]);
  assert.deepEqual(errors, []);
  console.log(
    "PASS: managed eligibility, exclusion precedence, salesperson-only capture selection, admin controls hidden from sales, unconfigured Google gate, disable and desktop/mobile views.",
  );
} catch (e) {
  console.error(e);
  await page.screenshot({
    path: "/tmp/crm-google-rollout-failure.png",
    fullPage: true,
  });
  process.exitCode = 1;
} finally {
  await browser.close();
}
