// Run against a disposable local engine with the production web build.
// No real providers or email sends. Database writes below only seed ownership.
import { chromium } from "playwright";
import { createServer } from "node:http";
import { execFileSync } from "node:child_process";
import assert from "node:assert/strict";
const base = process.env.OUTFLARE_URL ?? "http://127.0.0.1:8098";
const db = process.env.OUTFLARE_TEST_DATABASE_URL;
if (
  !db ||
  !["localhost", "127.0.0.1"].includes(new URL(db).hostname) ||
  !["localhost", "127.0.0.1"].includes(new URL(base).hostname)
)
  throw new Error("Use a disposable local database and local engine.");
const provider = createServer((req, res) => {
  res.setHeader("Content-Type", "application/json");
  if (req.method !== "GET") {
    res.writeHead(500);
    res.end("{}");
    throw new Error("Smoke test must not send email");
  }
  res.end(
    JSON.stringify(
      req.url === "/api/users"
        ? {
            data: {
              id: 1,
              name: "Example Sales",
              email: "sender@example.test",
            },
          }
        : { data: [], meta: { last_page: 1 } },
    ),
  );
});
await new Promise((resolve) => provider.listen(0, "127.0.0.1", resolve));
const browser = await chromium.launch({
  ...(process.env.OUTFLARE_BROWSER_BINARY
    ? { executablePath: process.env.OUTFLARE_BROWSER_BINARY }
    : {}),
});
const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    colorScheme: "dark",
  }),
  page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
async function call(method, path, data) {
  const r = await page.request.fetch(base + path, { method, data });
  const v = await r.json();
  assert(r.ok(), `${method} ${path}: ${r.status()} ${JSON.stringify(v)}`);
  return v;
}
try {
  if ((await call("GET", "/v1/setup")).needed)
    await call("POST", "/v1/setup", {
      email: "maya@northpeak.example",
      name: "Maya Example",
      password: "test_password_not_real",
      workspace_name: "Example Sales",
      telemetry: false,
    });
  await call("POST", "/v1/auth/login", {
    email: "maya@northpeak.example",
    password: "test_password_not_real",
  });
  const me = await call("GET", "/v1/me"),
    ws = me.workspaces[0],
    brand = (await call("GET", `/v1/workspaces/${ws.id}/brands`)).data[0],
    root = `/v1/brands/${brand.id}/sales`;
  await call("PUT", `${root}/team/${me.user.id}`, { role: "ae" });
  const source = await call("POST", `/v1/workspaces/${ws.id}/integrations`, {
    connector: "emailbison",
    credentials: {
      base_url: `http://127.0.0.1:${provider.address().port}`,
      api_key: "test_not_a_real_key",
    },
  });
  const company = await call("POST", `/v1/brands/${brand.id}/records`, {
    type: "account",
    name: "Northpeak Example",
    attributes: { website: "https://northpeak.example" },
  });
  const rec = await call("POST", `/v1/brands/${brand.id}/records`, {
    type: "person",
    name: "Ana Example",
    attributes: {
      companies: [company.id],
      job_title: "Product lead",
      linkedin_url: "https://social.example/ana",
    },
    identities: [
      { namespace: "email", value: `ana.${Date.now()}@customer.example` },
    ],
  });
  const email = rec.identities?.[0]?.value ?? `ana@customer.example`;
  const lead = await call("POST", `${root}/bridge`, {
    source: "smoke_fixture",
    source_id: rec.id,
    record_id: rec.id,
    integration_id: source.id,
    sender_id: "1",
    reply_id: "501",
    messages: [
      {
        direction: "incoming",
        from: email,
        to: ["sender@outbound.example"],
        cc: [],
        subject: "Interested in a team plan",
        body: "We would like to discuss a plan for our team. Can you share pricing?",
        message_id: `<${rec.id}@customer.example>`,
        provider_id: "501",
        occurred_at: new Date().toISOString(),
      },
    ],
  });
  await context.addInitScript(() => localStorage.setItem("api-panel", "open"));
  await page.goto(base + `/${ws.slug}/${brand.handle}/crm`);
  await page.getByRole("heading", { name: "New conversations", exact: true }).waitFor();
  await page
    .getByText(
      "We would like to discuss a plan for our team. Can you share pricing?",
      { exact: true },
    )
    .waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "Close API panel", exact: true })
      .count(),
    0,
    "Sales inherited an open developer panel",
  );
  assert.equal(
    await page
      .getByRole("link", { name: "Website", exact: true })
      .first()
      .getAttribute("href"),
    "https://northpeak.example/",
  );
  assert.equal(
    await page
      .getByRole("link", { name: "LinkedIn", exact: true })
      .first()
      .getAttribute("href"),
    "https://social.example/ana",
  );
  assert.equal(await page.getByRole("button", { name: "Open conversation", exact: true }).count(), 0);
  const assignment = page.getByRole("combobox", { name: "Assigned to for Ana Example", exact: true });
  await assignment.waitFor();
  await page.getByRole("button", { name: "Mark seen", exact: true }).click();
  await page.getByRole("button", { name: "Seen by you", exact: true }).waitFor();
  const seen = await call("GET", `${root}/leads/${lead.id}`);
  assert.equal(seen.lead.owner_id, null, "seen must not assign ownership");
  assert(seen.lead.viewed_by.some(v => v.user_id === me.user.id));
  await assignment.selectOption(me.user.id);
  await page.getByRole("button", { name: "My conversations", exact: true }).click();
  await page.getByRole("heading", { name: "My conversations", exact: true }).waitFor();
  await page.getByText("Not started", { exact: true }).last().waitFor();
  await page.getByRole("button", { name: /Northpeak Example/ }).first().click();
  await page.getByRole("heading", { name: "Northpeak Example", exact: true }).waitFor();
  assert(await page.getByRole("button", { name: "Send introduction", exact: true }).isDisabled(), "sending without email consent must remain blocked");
  const reserved = await call("GET", `${root}/leads/${lead.id}`);
  assert.equal(reserved.lead.state, "new");
  assert.equal(reserved.lead.owner_id, me.user.id);
  assert.equal(reserved.messages.length, 1, "assignment must not send an introduction");
  await page.getByRole("combobox", { name: "Assigned to for Ana Example", exact: true }).selectOption("");
  await page.getByRole("button", { name: "New conversations", exact: true }).click();
  await page.getByRole("heading", { name: "New conversations", exact: true }).waitFor();
  // Queue cleanup needs no mailbox connection, does not open/take the conversation,
  // and can be undone without sending an introduction.
  await page.getByRole("button", { name: "Archive", exact: true }).first().click();
  await page.getByText("No new conversations yet", { exact: true }).waitFor();
  await page.getByRole("heading", { name: "New conversations", exact: true }).waitFor();
  const archived = await call("GET", `${root}/leads/${lead.id}`);
  assert.equal(archived.lead.state, "archived");
  assert.equal(archived.lead.owner_id, null);
  assert.equal(archived.messages.length, 1);
  await page.getByRole("button", { name: "Undo", exact: true }).click();
  await page.getByRole("button", { name: "Archive", exact: true }).first().waitFor();
  const restored = await call("GET", `${root}/leads/${lead.id}`);
  assert.equal(restored.lead.state, "new");
  assert.equal(restored.messages.length, 1);
  assert.deepEqual(
    restored.events.filter((e) => ["lead_archived", "lead_reopened"].includes(e.action)).map((e) => e.action),
    ["lead_archived", "lead_reopened"],
    "queue archive and undo must remain in the manager's activity log",
  );
  await page.screenshot({
    path: "/tmp/outflare-sales-ui-new-leads.png",
    fullPage: true,
  });

  await call("PUT", `${root}/team/${me.user.id}`, { role: "sdr" });
  await page.reload();
  await page
    .getByRole("button", { name: "Sent to AEs", exact: true })
    .waitFor();
  await page.getByRole("heading", { name: "New conversations", exact: true }).waitFor();

  await call("PUT", `${root}/team/${me.user.id}`, { role: "manager" });
  // Enter the default route after a role change, rather than reloading the
  // explicitly selected New conversations URL from the SDR's previous view.
  await page.goto(base + `/${ws.slug}/${brand.handle}/crm`);
  await page.getByRole("heading", { name: "All conversations", exact: true }).waitFor();
  await page.getByRole("button", { name: "New conversations", exact: true }).waitFor();
  await page
    .getByRole("combobox", { name: "Filter by Sales teammate" })
    .waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "Take & open", exact: true })
      .count(),
    0,
    "manager must review without impersonating a rep",
  );
  await page.getByRole("button", { name: "Active", exact: true }).click();
  await page
    .getByText("No conversations match these filters", { exact: true })
    .waitFor();
  await page
    .getByRole("button", { name: "Show all conversations", exact: true })
    .click();
  await page
    .getByText(
      "We would like to discuss a plan for our team. Can you share pricing?",
      { exact: true },
    )
    .waitFor();
  await page.screenshot({
    path: "/tmp/outflare-sales-ui-manager.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Archive", exact: true }).first().click();
  await page.getByRole("button", { name: "Reopen", exact: true }).first().waitFor();
  await page.getByRole("button", { name: "Archived", exact: true }).click();
  await page.getByRole("button", { name: "Reopen", exact: true }).first().click();
  await page.getByText("No conversations match these filters", { exact: true }).waitFor();
  await page.getByRole("button", { name: "Show all conversations", exact: true }).click();
  await page
    .getByRole("button", { name: /Northpeak Example/ })
    .first()
    .click();
  await page
    .getByRole("heading", { name: "Northpeak Example", exact: true })
    .waitFor();
  await page
    .getByRole("button", { name: "Back to all conversations", exact: true })
    .click();
  await page.getByRole("heading", { name: "All conversations", exact: true }).waitFor();
  await page.goto(base + `/${ws.slug}/${brand.handle}/crm/sales%3Aemail`);
  await page
    .getByText("You’re viewing Sales as a manager", { exact: true })
    .waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "Connect Gmail", exact: true })
      .count(),
    0,
  );
  assert(
    (await page.title()).startsWith("My email ·"),
    "browser title leaked a route key",
  );
  await page
    .getByRole("button", { name: "Open all conversations", exact: true })
    .click();
  await page.getByRole("heading", { name: "All conversations", exact: true }).waitFor();

  await call("PUT", `${root}/team/${me.user.id}`, { role: "ae" });
  await page.goto(base + `/${ws.slug}/${brand.handle}/crm`);
  await page.getByRole("heading", { name: "New conversations", exact: true }).waitFor();
  await page
    .getByRole("button", { name: /Northpeak Example/ })
    .first()
    .click();
  await page
    .getByRole("heading", { name: "Northpeak Example", exact: true })
    .waitFor();
  assert.equal(
    await page.getByRole("tab", { name: "History", exact: true }).count(),
    0,
  );
  await page.screenshot({
    path: "/tmp/outflare-sales-ui-new-conversation.png",
    fullPage: true,
  });
  execFileSync("psql", [
    db,
    "-v",
    "ON_ERROR_STOP=1",
    "-c",
    `UPDATE sales_leads SET state='active',owner_id='${me.user.id}',ae_id='${me.user.id}' WHERE id='${lead.id}'`,
  ]);
  const template = await call("POST", `${root}/templates`, {
    name: "Personal pricing",
    body: "Hi {first_name}, here is our team pricing.",
  });
  await page.reload();
  await page.getByLabel("Reply message").waitFor();
  await page.getByRole("button", { name: "Browse templates", exact: true }).click();
  await page.getByRole("button", { name: "Favorite Personal pricing", exact: true }).click();
  await page.getByRole("button", { name: "Unfavorite Personal pricing", exact: true }).waitFor();
  await page.getByRole("button", { name: "Apply text", exact: true }).click();
  await page.waitForFunction(
    () =>
      document.querySelector('[aria-label="Reply message"]')?.value ===
      "Hi Ana, here is our team pricing.",
  );
  await page.getByRole("button", { name: "Personal pricing", exact: true }).waitFor();
  assert.equal((await call("GET", `${root}/templates`)).data.find(t => t.id === template.id).favorite, true);
  await page.getByRole("button", { name: "Personalize with AI", exact: true }).waitFor();
  const savedReply = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/sales/leads/${lead.id}/draft`) &&
      r.request().method() === "PUT",
  );
  await page
    .getByLabel("Reply message")
    .fill("Hi Ana, here is pricing for your team of twelve.");
  await page.getByLabel("Reply message").blur();
  await savedReply;
  await page.getByText("Draft saved", { exact: true }).waitFor();
  const detail = await call("GET", `${root}/leads/${lead.id}`);
  assert.equal(
    detail.draft.body,
    "Hi Ana, here is pricing for your team of twelve.",
  );
  assert.equal(
    detail.messages.length,
    1,
    "applying/editing a template must not send",
  );
  await page.screenshot({
    path: "/tmp/outflare-sales-ui-active-conversation.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: "/tmp/outflare-sales-ui-mobile.png",
    fullPage: true,
  });
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    ),
    false,
    "mobile content overflows",
  );
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(base + `/${ws.slug}/${brand.handle}/crm/sales%3Amine`);
  await page.getByRole("button", { name: "Archive", exact: true }).first().click();
  await page.getByText("No open conversations yet", { exact: true }).waitFor();
  await page.getByRole("button", { name: "Archived", exact: true }).click();
  await page.getByRole("button", { name: "Reopen", exact: true }).first().click();
  await page.getByText("No archived conversations here yet", { exact: true }).waitFor();
  // Ownership was seeded without taking or sending an introduction, so reopen
  // must keep it Not started even though the fixture temporarily exercised an active draft.
  await page.getByRole("button", { name: "Not started", exact: true }).click();
  await page.getByRole("button", { name: "Archive", exact: true }).first().waitFor();
  const reopenedOwned = await call("GET", `${root}/leads/${lead.id}`);
  assert.equal(reopenedOwned.lead.state, "new");
  assert.equal(reopenedOwned.lead.owner_id, me.user.id);
  assert.equal(reopenedOwned.draft.body, detail.draft.body);
  assert.equal(reopenedOwned.messages.length, 1);
  assert.deepEqual(errors, []);
  console.log(
    "Sales browser smoke passed: queue archive/undo/reopen and recorded activity, populated queues, reply/source previews, SDR/AE/manager navigation, manager filters, full conversation, draft persistence, no implicit send, mobile layout.",
  );
} finally {
  await browser.close();
  await new Promise((resolve) => provider.close(resolve));
}
