// The side panel: who the open email is from, or which company the open
// website belongs to, as Outflare knows them, and adding the ones it doesn't.
import { api, ApiError, candidates, cleanServer, connect, DEFAULT_SERVER, disconnect, domainStatus, FREE_MAIL, refreshKnown, server, siteDomain, state } from "./lib.js";

const app = document.getElementById("app");
const brandPick = document.getElementById("brand");
const foot = document.getElementById("foot");

// h builds an element. Text only ever goes in as text, never as HTML.
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "class") el.className = v;
    else if (v === true) el.setAttribute(k, "");
    else el.setAttribute(k, v);
  }
  for (const kid of kids.flat()) if (kid != null && kid !== false) el.append(kid);
  return el;
}
const show = (...kids) => app.replaceChildren(...kids.flat().filter(Boolean));

let manual = null; // what the search box asked for, until the tab changes
let run = 0; // the latest render; older ones stop when they see a newer

const EMAIL = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;

function when(iso) {
  if (!iso) return "";
  const days = Math.round((Date.now() - new Date(iso).getTime()) / 86400000);
  if (days <= 0) return "today";
  if (days === 1) return "yesterday";
  if (days < 30) return days + " days ago";
  return new Date(iso).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}

function money(cents, currency) {
  if (cents == null) return "";
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: currency || "USD", maximumFractionDigits: 0 }).format(cents / 100);
  } catch {
    return String(Math.round(cents / 100));
  }
}

const SOURCES = { cold: "Cold", inbound: "Inbound", event: "Events and conferences", signup: "Sign-up", invited: "Invited", customer: "Customer", other: "Other" };
const ORIGINS = { first_party: "Your product or forms", prospect: "Cold outreach", crm_sync: "Your CRM", import: "An import", manual: "Added by hand" };

// ---- What the panel is about ----

// context is the open email's sender in Gmail, or the open website.
async function context() {
  if (manual) return manual;
  const [tab] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  if (!tab || !tab.url) return null;
  if (tab.url.startsWith("https://mail.google.com/")) {
    const s = await chrome.tabs.sendMessage(tab.id, { type: "outflare.sender" }).catch(() => null);
    return s ? { kind: "email", email: s.email, name: s.name } : { kind: "gmail" };
  }
  const domain = siteDomain(tab.url);
  return domain ? { kind: "site", domain } : null;
}

async function lookup(brand, namespace, value) {
  const out = await api("GET", `/v1/brands/${brand.id}/records/lookup?` + new URLSearchParams({ namespace, value }));
  return out.data.find((r) => r.id === out.owner) || out.data[0] || null;
}

// ---- Views ----

function connectView(error) {
  let own = false;
  const input = h("input", { type: "url", placeholder: "https://outflare.example.com", "aria-label": "Your Outflare address" });
  const box = h("label", { hidden: true }, "Your Outflare address", input);
  const err = h("p", { class: "error", role: "alert" }, error || "");
  const button = h("button", { class: "primary" }, "Connect Outflare");
  button.addEventListener("click", async () => {
    const origin = own ? cleanServer(input.value) : DEFAULT_SERVER;
    if (!origin) {
      err.textContent = "Enter your Outflare address, starting with https://";
      return;
    }
    button.disabled = true;
    button.textContent = "Connecting…";
    try {
      await connect(origin);
      render();
    } catch (e) {
      show(connectView(e.message));
    }
  });
  const toggle = h("button", { class: "link" }, "Running Outflare on your own server?");
  toggle.addEventListener("click", () => {
    own = true;
    box.hidden = false;
    toggle.hidden = true;
    input.focus();
  });
  return h("div", { class: "center" },
    h("img", { src: "icons/128.png", width: 48, height: 48, alt: "" }),
    h("h1", null, "See who you're dealing with"),
    h("p", { class: "muted" }, "Open an email in Gmail or any company's website, and Outflare shows what you know about them."),
    box, button, err, toggle,
    h("p", { class: "muted" }, "Outflare is only asked about a website it already holds, or one you look up yourself."));
}

function searchBox(value) {
  const input = h("input", { type: "search", placeholder: "Look up an email or a website", "aria-label": "Look up an email or a website", value: value || "" });
  const form = h("form", { class: "search" }, input, h("button", { type: "submit" }, "Look up"));
  form.style.flexDirection = "row";
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const v = input.value.trim().toLowerCase();
    if (EMAIL.test(v)) manual = { kind: "email", email: v, name: "" };
    else if (siteDomain("https://" + v.replace(/^https?:\/\//, ""))) manual = { kind: "site", domain: siteDomain("https://" + v.replace(/^https?:\/\//, "")) };
    else return;
    render();
  });
  return form;
}

function appLink(st, id) {
  return `${st.server || DEFAULT_SERVER}/${st.workspace.slug}/${st.brand.handle}/people/${id}`;
}

function section(title, ...kids) {
  return h("section", null, h("h2", null, title), ...kids);
}

function recordCard(st, rec, sum, canAdd, blocked) {
  const person = rec.type === "person";
  const email = rec.identities.find((i) => i.namespace === "email")?.value;
  const domain = rec.identities.find((i) => i.namespace === "domain")?.value;
  const rows = [];
  const row = (k, v) => v && rows.push(h("dt", null, k), h("dd", null, v));
  row(person ? "Email" : "Website", person ? email : domain);
  if (person && sum.company) row("Company", h("a", { href: appLink(st, sum.company.id), target: "_blank", rel: "noreferrer" }, sum.company.name));
  if (person && rec.attributes?.job_title) row("Role", String(rec.attributes.job_title));
  row("Owner", sum.owner?.name);
  row("Stage", sum.stage?.stage);
  row("Source", (person && SOURCES[rec.attributes?.source]) || ORIGINS[rec.origin] || rec.origin);
  if (person && rec.contactability) row("Marketing email", rec.contactability.allowed.marketing ? "Yes" : "No");
  if (!person && sum.people_total) row("People", String(sum.people_total));
  row("Last contact", when(sum.contact?.last_at));

  const kids = [
    blocked && h("div", { class: "banner", role: "alert" }, "Do not contact", h("span", null, "This website is on your do-not-contact list.")),
    h("div", { class: "card" },
      h("div", null, h("h1", null, rec.name || email || domain || "No name"), h("p", { class: "muted" }, h("span", { class: "dot green" }), person ? "A person you know" : "A company you know")),
      h("dl", { class: "rows" }, rows)),
  ];
  if (sum.deals && (sum.deals.open || sum.deals.latest?.length)) {
    kids.push(section(`Deals (${sum.deals.open} open)`, h("ul", { class: "list" }, sum.deals.latest.slice(0, 3).map((d) =>
      h("li", null, h("span", null, d.name), h("span", { class: "muted" }, [d.stage, money(d.amount_cents, d.currency)].filter(Boolean).join(" · ")))))));
  }
  if (sum.conversations && (sum.conversations.open || sum.conversations.latest?.length)) {
    kids.push(section(`Support (${sum.conversations.open} open)`, h("ul", { class: "list" }, sum.conversations.latest.slice(0, 3).map((c) =>
      h("li", null, h("span", null, c.subject || "No subject"), h("span", { class: "muted" }, when(c.last_message_at)))))));
  }
  if (sum.tasks?.next?.length) {
    kids.push(section(`To-dos (${sum.tasks.open} open)`, h("ul", { class: "list" }, sum.tasks.next.slice(0, 3).map((t) =>
      h("li", null, h("span", null, t.title), h("span", { class: "muted" }, t.due_at ? "due " + new Date(t.due_at).toLocaleDateString(undefined, { day: "numeric", month: "short" }) : ""))))));
  }

  const forms = h("div", null);
  const open = h("a", { href: appLink(st, rec.id), target: "_blank", rel: "noreferrer" }, "Open in Outflare");
  if (canAdd) {
    kids.push(h("div", { class: "actions" },
      h("button", { onclick: () => forms.replaceChildren(noteForm(st, rec, "note")) }, "Add note"),
      h("button", { onclick: () => forms.replaceChildren(noteForm(st, rec, "task")) }, "Add to-do")), forms);
  }
  kids.push(h("p", null, open));
  return kids;
}

function noteForm(st, rec, kind) {
  const task = kind === "task";
  const text = task ? h("input", { type: "text", required: true, maxlength: 300, placeholder: "What needs doing?", "aria-label": "To-do" })
    : h("textarea", { required: true, maxlength: 20000, placeholder: "Write a note", "aria-label": "Note" });
  const due = task && h("input", { type: "date", "aria-label": "Due date" });
  const err = h("p", { class: "error", role: "alert" });
  const save = h("button", { class: "primary", type: "submit" }, task ? "Add to-do" : "Add note");
  const form = h("form", { class: "card" }, text, task && h("label", null, "Due (optional)", due), h("div", { class: "actions" }, save), err);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const body = text.value.trim();
    if (!body) return;
    save.disabled = true;
    try {
      await api("POST", `/v1/brands/${st.brand.id}/crm/activities`, {
        record_id: rec.id,
        kind,
        title: task ? body : body.split("\n")[0].slice(0, 120),
        body: task ? "" : body,
        ...(task && due.value ? { due_at: new Date(due.value + "T09:00:00").toISOString() } : {}),
      });
      render();
    } catch (e2) {
      err.textContent = e2.message;
      save.disabled = false;
    }
  });
  setTimeout(() => text.focus());
  return form;
}

function newPerson(st, ctx, canAdd, blocked) {
  const domain = ctx.email.split("@")[1];
  const company = !FREE_MAIL.has(domain);
  const head = h("div", null, h("h1", null, ctx.name || ctx.email), h("p", { class: "muted" }, h("span", { class: "dot" }), "Not in Outflare yet"));
  if (blocked) {
    return [h("div", { class: "banner", role: "alert" }, "Do not contact", h("span", null, `${domain} is on your do-not-contact list, so this person can't be added from here.`)), h("div", { class: "card" }, head, h("p", null, ctx.email))];
  }
  if (!canAdd) return [h("div", { class: "card" }, head, h("p", null, ctx.email), h("p", { class: "muted" }, "Your role can look people up but not add them."))];
  const name = h("input", { type: "text", value: ctx.name || "", maxlength: 500, required: true });
  // Their Source; Events and conferences also asks which one, from the
  // brand's list or typed new.
  const from = h("select", null,
    h("option", { value: "inbound" }, "They came to us"),
    h("option", { value: "cold" }, "Cold outreach"),
    h("option", { value: "event" }, "Met at an event or conference"),
    h("option", { value: "customer" }, "A customer"),
    h("option", { value: "other" }, "Other"));
  const events = h("datalist", { id: "of-events" });
  const event = h("input", { type: "text", list: "of-events", maxlength: 120, placeholder: "Pick or type the event" });
  const eventRow = h("label", { hidden: true }, "Event or conference", event, events);
  let known = null;
  from.addEventListener("change", async () => {
    eventRow.hidden = from.value !== "event";
    if (from.value === "event" && !known) {
      known = (await api("GET", `/v1/brands/${st.brand.id}/sub-sources?source=event`).catch(() => ({ data: [] }))).data || [];
      events.replaceChildren(...known.map((x) => h("option", { value: x.label })));
    }
  });
  const err = h("p", { class: "error", role: "alert" });
  const add = h("button", { class: "primary", type: "submit", value: "person" }, "Add person");
  const both = company && h("button", { type: "submit", value: "both" }, "Add with company");
  const form = h("form", { class: "card" }, head,
    h("label", null, "Name", name),
    h("label", null, "Email", h("input", { type: "email", value: ctx.email, readonly: true })),
    h("label", null, "Where they came from", from),
    eventRow,
    h("p", { class: "muted" }, "Marketing email starts as No until they agree to it."),
    h("div", { class: "actions" }, add, both), err);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const withCompany = e.submitter?.value === "both";
    add.disabled = true;
    if (both) both.disabled = true;
    const origin = from.value === "cold" ? "prospect" : "manual";
    try {
      const attributes = { source: from.value };
      if (from.value === "event" && event.value.trim()) {
        // Adds the event to the brand's list, or finds it there.
        const sub = await api("POST", `/v1/brands/${st.brand.id}/sub-sources`, { source: "event", label: event.value.trim() });
        attributes.sub_source = sub.key;
      }
      const made = await api("POST", `/v1/brands/${st.brand.id}/records`, {
        type: "person", name: name.value.trim(), origin, acquired_via: "extension", attributes,
        identities: [{ namespace: "email", value: ctx.email }],
      });
      if (withCompany) {
        const firm = (await lookup(st.brand, "domain", domain)) || (await api("POST", `/v1/brands/${st.brand.id}/records`, {
          type: "account", name: domain, origin, acquired_via: "extension",
          identities: [{ namespace: "domain", value: domain }],
        }));
        await api("POST", `/v1/brands/${st.brand.id}/records/${made.id}/relationships`, { to: firm.id, kind: "works_at" });
        refreshKnown().catch(() => {});
      }
      render();
    } catch (e2) {
      err.textContent = e2.message;
      add.disabled = false;
      if (both) both.disabled = false;
    }
  });
  return [form];
}

function newCompany(st, ctx, canAdd, blocked) {
  const head = h("div", null, h("h1", null, ctx.domain), h("p", { class: "muted" }, h("span", { class: blocked ? "dot red" : "dot" }), blocked ? "Do not contact" : "Not in Outflare yet"));
  if (blocked) {
    return [h("div", { class: "banner", role: "alert" }, "Do not contact", h("span", null, "This website is on your do-not-contact list, so it can't be added from here.")), h("div", { class: "card" }, head)];
  }
  if (!canAdd) return [h("div", { class: "card" }, head, h("p", { class: "muted" }, "Your role can look companies up but not add them."))];
  const name = h("input", { type: "text", value: ctx.domain, maxlength: 500, required: true });
  const from = h("select", null, h("option", { value: "prospect" }, "Cold outreach"), h("option", { value: "manual" }, "They came to us"));
  const err = h("p", { class: "error", role: "alert" });
  const add = h("button", { class: "primary", type: "submit" }, "Add company");
  const form = h("form", { class: "card" }, head,
    h("label", null, "Name", name),
    h("label", null, "Where they came from", from),
    h("div", { class: "actions" }, add), err);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    add.disabled = true;
    try {
      // Someone may have added it since the list was fetched.
      if (!(await lookup(st.brand, "domain", ctx.domain))) {
        await api("POST", `/v1/brands/${st.brand.id}/records`, {
          type: "account", name: name.value.trim(), origin: from.value, acquired_via: "extension",
          identities: [{ namespace: "domain", value: ctx.domain }],
        });
      }
      await refreshKnown().catch(() => {});
      manual = ctx;
      render();
    } catch (e2) {
      err.textContent = e2.message;
      add.disabled = false;
    }
  });
  return [form];
}

// ---- The panel ----

async function brands(st) {
  const out = await api("GET", `/v1/workspaces/${st.workspace.id}/brands`);
  return out.data.filter((b) => !b.status || b.status === "active");
}

// The brands and what the token may do change rarely: asked for at most
// every five minutes, not on every tab.
let cached = null;
async function basics(st) {
  if (cached && cached.ws === st.workspace.id && Date.now() - cached.at < 300000) return cached;
  const [list, who] = await Promise.all([brands(st), api("GET", "/v1/whoami")]);
  cached = { ws: st.workspace.id, at: Date.now(), list, canAdd: who.token.scopes.includes("customers.manage") };
  return cached;
}

async function render() {
  const mine = ++run;
  const stale = () => mine !== run;
  let st = await state();
  st.server = await server();
  foot.hidden = !st.auth;
  brandPick.hidden = true;
  if (!st.auth) return show(connectView());
  document.getElementById("where").textContent = st.workspace ? st.workspace.name : "";
  try {
    if (!st.workspace) {
      const who = await api("GET", "/v1/whoami");
      await chrome.storage.local.set({ workspace: who.token.workspace });
      st.workspace = who.token.workspace;
    }
    const { list, canAdd } = await basics(st);
    if (stale()) return;
    if (!list.length) return show(h("p", { class: "muted" }, "This workspace has no brand yet. Add one in Outflare first."));
    if (!st.brand || !list.some((b) => b.id === st.brand.id)) {
      if (list.length > 1 && !st.brand) {
        return show(h("div", { class: "card" }, h("h1", null, "Which brand?"), h("p", { class: "muted" }, "Each brand has its own people and companies."),
          h("div", { class: "actions" }, list.map((b) => h("button", { onclick: () => pickBrand(b) }, b.name || b.handle)))));
      }
      await pickBrand(list[0], true);
      st = { ...st, ...(await state()) };
    }
    if (list.length > 1) {
      brandPick.replaceChildren(...list.map((b) => h("option", { value: b.id, selected: b.id === st.brand.id }, b.name || b.handle)));
      brandPick.onchange = () => pickBrand(list.find((b) => b.id === brandPick.value));
      brandPick.hidden = false;
    }
    if (!st.known || st.known.brand !== st.brand.id || Date.now() - st.known.at > 300000) st.known = await refreshKnown().catch(() => st.known);

    const ctx = await context();
    if (stale()) return;
    const search = searchBox(manual ? manual.email || manual.domain : "");
    if (!ctx || ctx.kind === "gmail") {
      return show(search, h("p", { class: "muted" }, ctx ? "Open an email to see who it's from." : "Open a company's website or an email in Gmail, or look someone up above."));
    }
    show(search, h("p", { class: "muted" }, "Looking…"));
    let rec = null;
    let blocked = false;
    if (ctx.kind === "email") {
      rec = await lookup(st.brand, "email", ctx.email);
      blocked = (await domainStatus(ctx.email.split("@")[1])) === "blocked";
    } else {
      // A website you only happen to be on is checked against the list
      // kept in the browser. Outflare is asked about it only when it is
      // already one of your companies or on your do-not-contact list, or
      // when you typed it yourself.
      const status = await domainStatus(ctx.domain);
      blocked = status === "blocked";
      if (manual || status !== "new" || st.known?.truncated) {
        for (const d of candidates(ctx.domain)) if ((rec = await lookup(st.brand, "domain", d))) break;
      }
    }
    if (stale()) return;
    if (!rec) return show(search, ctx.kind === "email" ? newPerson(st, ctx, canAdd, blocked) : newCompany(st, ctx, canAdd, blocked));
    const sum = await api("GET", `/v1/brands/${st.brand.id}/records/${rec.id}/summary`);
    if (stale()) return;
    show(search, recordCard(st, rec, sum, canAdd, blocked));
  } catch (e) {
    if (stale()) return;
    if (e instanceof ApiError && e.code === "signed_out") return show(connectView(e.message));
    show(h("p", { class: "error", role: "alert" }, e.message || "Something went wrong."), h("button", { onclick: render }, "Try again"));
  }
}

async function pickBrand(b, quiet) {
  await chrome.storage.local.set({ brand: { id: b.id, handle: b.handle, name: b.name } });
  await chrome.storage.local.remove("known");
  manual = null;
  if (!quiet) render();
}

document.getElementById("disconnect").addEventListener("click", async () => {
  await disconnect();
  manual = null;
  cached = null;
  render();
});

// Follow the tab in front: another tab, another page, another email.
const follow = () => {
  manual = null;
  render();
};
chrome.tabs.onActivated.addListener(follow);
chrome.tabs.onUpdated.addListener((_id, change, tab) => {
  if (tab.active && (change.url || change.status === "complete")) follow();
});
chrome.runtime.onMessage.addListener((msg) => {
  if (msg && msg.type === "outflare.sender_changed") follow();
});

render();
