import { chromium } from "playwright";
import assert from "node:assert/strict";
const base = process.env.OUTFLARE_URL ?? "http://127.0.0.1:8091";
if (!["localhost", "127.0.0.1"].includes(new URL(base).hostname))
  throw new Error("Use only a local fictional demo workspace.");
const browser = await chromium.launch({
  executablePath: "/usr/bin/chromium",
  args: ["--no-sandbox"],
});
const owner = await browser.newContext({
  viewport: { width: 1440, height: 1000 },
});
const employee = await browser.newContext({
  viewport: { width: 1440, height: 1000 },
});
const page = await owner.newPage();
const sales = await employee.newPage();
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
  const records = (
    await call(
      page,
      "GET",
      `/v1/workspaces/${ws.id}/records?type=person&limit=200`,
    )
  ).data;
  const person = records.find((r) =>
    r.identities.some(
      (i) => i.namespace === "email" && i.value === "ana@acme.example",
    ),
  );
  assert(person, "Run crm-smoke.mjs first to create fictional CRM records.");
  const root = `/${ws.slug}/${brand.handle}/crm`;
  await page.goto(base + root + "/settings");
  await page
    .getByLabel("Internal company domains", { exact: true })
    .fill("northpeak.example");
  await page
    .getByRole("button", { name: "Save brand safeguards", exact: true })
    .click();
  await page.getByText("Exclusions saved.", { exact: false }).waitFor();
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
  const mail = `/v1/brands/${brand.id}/crm/mail/records/${person.id}/messages`;
  // Keep repeat runs deterministic without deleting anyone else's messages.
  const mine = await call(sales, "GET", mail);
  for (const m of mine.data)
    if (m.subject === "Customer renewal privacy test")
      await call(
        sales,
        "DELETE",
        `/v1/brands/${brand.id}/crm/mail/messages/${m.id}`,
      );
  await call(
    sales,
    "PUT",
    `/v1/brands/${brand.id}/crm/mail/records/${person.id}/sharing`,
    {
      ...(await call(
        sales,
        "GET",
        `/v1/brands/${brand.id}/crm/mail/records/${person.id}/sharing`,
      )),
      visibility: "private",
      audience: "selected",
      viewer_ids: [],
    },
  );
  async function openMail(p) {
    await p.goto(base + root + "/person");
    await p.getByRole("button", { name: /Open Ana Diaz/ }).click();
    await p.getByRole("tab", { name: "email", exact: true }).click();
    await p
      .getByRole("heading", {
        name: "My automatic Google capture",
        exact: true,
      })
      .waitFor();
  }
  async function saveSharing() {
    await Promise.all([
      sales.waitForResponse(
        (r) =>
          r.url().endsWith("/sharing") &&
          r.request().method() === "PUT" &&
          r.status() === 200,
      ),
      sales
        .getByRole("button", { name: "Save email sharing", exact: true })
        .click(),
    ]);
  }
  await openMail(sales);
  const raw =
    "From: ana@acme.example\r\nTo: sales@northpeak.example\r\nSubject: Customer renewal privacy test\r\nMessage-ID: <crm-privacy-smoke@acme.example>\r\n\r\nFictional customer renewal details.";
  await sales
    .getByLabel("Import my email file", { exact: true })
    .setInputFiles({
      name: "renewal.eml",
      mimeType: "message/rfc822",
      buffer: Buffer.from(raw),
    });
  await sales
    .getByRole("heading", {
      name: "Customer renewal privacy test",
      exact: true,
    })
    .waitFor();
  await openMail(page);
  await page.getByText("No email available to you", { exact: true }).waitFor();
  await sales
    .getByText("Share my correspondence on this record", { exact: true })
    .click();
  await sales
    .getByLabel("My email visibility", { exact: true })
    .selectOption("metadata");
  await sales
    .getByLabel("My email audience", { exact: true })
    .selectOption("brand");
  await saveSharing();
  let visible = (await call(page, "GET", mail)).data;
  assert.equal(visible.length, 1);
  assert.equal(visible[0].body, null);
  assert.equal(visible[0].subject, null);
  assert.equal(visible[0].participants, null);
  await openMail(page);
  await page
    .getByRole("heading", { name: "Email activity", exact: true })
    .waitFor();
  await sales
    .getByLabel("My email visibility", { exact: true })
    .selectOption("full");
  await saveSharing();
  visible = (await call(page, "GET", mail)).data;
  assert.equal(visible[0].body, "Fictional customer renewal details.");
  await sales
    .getByLabel("My email visibility", { exact: true })
    .selectOption("private");
  await saveSharing();
  assert.equal((await call(page, "GET", mail)).data.length, 0);
  const mixed = raw.replace("Subject:", "Cc: hr@northpeak.example\r\nSubject:");
  await sales
    .getByLabel("Import my email file", { exact: true })
    .setInputFiles({
      name: "internal.eml",
      mimeType: "message/rfc822",
      buffer: Buffer.from(mixed),
    });
  await sales.getByText("message excluded:", { exact: false }).waitFor();
  await sales.screenshot({ path: "/tmp/crm-mail-privacy.png", fullPage: true });
  await sales.setViewportSize({ width: 390, height: 844 });
  await sales.screenshot({
    path: "/tmp/crm-mail-privacy-mobile.png",
    fullPage: true,
  });
  assert.equal((await call(sales, "GET", mail)).data.length, 1);
  await sales.goto(base + root + "/settings");
  await sales
    .getByRole("button", {
      name: "Delete my imported email copies",
      exact: true,
    })
    .click();
  await Promise.all([
    sales.waitForResponse(
      (r) =>
        r.url().endsWith("/crm/mail/messages") &&
        r.request().method() === "DELETE" &&
        r.status() === 200,
    ),
    sales
      .getByRole("button", { name: "Delete my copies", exact: true })
      .click(),
  ]);
  assert.equal((await call(sales, "GET", mail)).data.length, 0);
  assert.deepEqual(errors, []);
  console.log(
    "PASS: separate-user private email, admin denial, metadata redaction, explicit full sharing, revocation, mixed internal rejection, privacy settings, owner-only bulk deletion and desktop/mobile rendering.",
  );
} catch (e) {
  console.error(e);
  await page.screenshot({
    path: "/tmp/crm-mail-owner-failure.png",
    fullPage: true,
  });
  await sales.screenshot({
    path: "/tmp/crm-mail-privacy-failure.png",
    fullPage: true,
  });
  process.exitCode = 1;
} finally {
  await browser.close();
}
