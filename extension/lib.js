// Shared by the side panel and the background worker: where Outflare is,
// signing in, calling the API, and the list of known websites.

export const DEFAULT_SERVER = "https://app.outflare.ai";

const store = chrome.storage.local;

export async function state() {
  return await store.get(["server", "clients", "auth", "workspace", "brand", "known"]);
}

export async function server() {
  return (await store.get("server")).server || DEFAULT_SERVER;
}

// cleanServer turns what someone typed into an origin, or "" when it isn't
// one. Only https, or http on this computer.
export function cleanServer(raw) {
  let u;
  try {
    u = new URL(/^https?:\/\//.test(raw.trim()) ? raw.trim() : "https://" + raw.trim());
  } catch {
    return "";
  }
  const local = u.hostname === "localhost" || u.hostname === "127.0.0.1";
  if (u.protocol !== "https:" && !(u.protocol === "http:" && local)) return "";
  return u.origin;
}

function b64url(bytes) {
  return btoa(String.fromCharCode(...new Uint8Array(bytes))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

async function sha256(text) {
  return await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
}

// connect signs the person in: the extension registers itself with the
// server once, sends them to Outflare's own approval page, and swaps the
// answer for a token that only reads and adds people and companies.
export async function connect(origin) {
  if (origin !== DEFAULT_SERVER && !(await chrome.permissions.request({ origins: [origin + "/*"] }))) {
    throw new Error("Outflare needs your permission to reach " + origin + ".");
  }
  const redirect = chrome.identity.getRedirectURL();
  const { clients = {} } = await store.get("clients");
  if (!clients[origin]) {
    const res = await fetch(origin + "/oauth/register", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ redirect_uris: [redirect], client_name: "Outflare for Chrome" }),
    });
    if (!res.ok) throw new Error("That address didn't answer like Outflare. Check it and try again.");
    clients[origin] = (await res.json()).client_id;
    await store.set({ clients });
  }
  const verifier = b64url(crypto.getRandomValues(new Uint8Array(48)));
  const stateParam = b64url(crypto.getRandomValues(new Uint8Array(16)));
  const ask = new URL(origin + "/oauth/authorize");
  ask.search = new URLSearchParams({
    response_type: "code",
    client_id: clients[origin],
    redirect_uri: redirect,
    state: stateParam,
    code_challenge: b64url(await sha256(verifier)),
    code_challenge_method: "S256",
  }).toString();
  const back = new URL(await chrome.identity.launchWebAuthFlow({ url: ask.toString(), interactive: true }));
  if (back.searchParams.get("state") !== stateParam) throw new Error("Signing in was interrupted. Try again.");
  if (back.searchParams.get("error")) {
    throw new Error(back.searchParams.get("error") === "access_denied" ? "You cancelled connecting." : "Outflare couldn't connect. Try again.");
  }
  const tok = await tokenCall(origin, {
    grant_type: "authorization_code",
    code: back.searchParams.get("code"),
    client_id: clients[origin],
    redirect_uri: redirect,
    code_verifier: verifier,
  });
  await store.set({ server: origin, auth: tok });
  await store.remove(["workspace", "brand", "known"]);
  const who = await api("GET", "/v1/whoami");
  await store.set({ workspace: who.token.workspace });
  return who;
}

async function tokenCall(origin, form) {
  const res = await fetch(origin + "/oauth/token", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams(form).toString(),
  });
  const out = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(out.error_description || "Outflare couldn't sign you in.");
  return { access: out.access_token, refresh: out.refresh_token, expires: Date.now() + (out.expires_in - 60) * 1000 };
}

export async function disconnect() {
  const { auth } = await store.get("auth");
  const origin = await server();
  if (auth) {
    // Best effort: the connection can also be removed in Outflare.
    fetch(origin + "/oauth/revoke", {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ token: auth.refresh }).toString(),
    }).catch(() => {});
  }
  await store.remove(["auth", "workspace", "brand", "known"]);
}

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

let refreshing;
async function freshToken(force) {
  const { auth } = await store.get("auth");
  if (!auth) throw new ApiError(401, "signed_out", "Connect Outflare first.");
  if (!force && auth.expires > Date.now()) return auth.access;
  refreshing ??= (async () => {
    try {
      const origin = await server();
      const { clients = {} } = await store.get("clients");
      const next = await tokenCall(origin, { grant_type: "refresh_token", refresh_token: auth.refresh, client_id: clients[origin] });
      await store.set({ auth: next });
      return next.access;
    } catch (e) {
      await store.remove(["auth", "known"]);
      throw new ApiError(401, "signed_out", "Your connection to Outflare ended. Connect again.");
    } finally {
      refreshing = undefined;
    }
  })();
  return await refreshing;
}

// api calls Outflare as the signed-in person. A 401 renews the token once.
export async function api(method, path, body) {
  const origin = await server();
  for (let attempt = 0; ; attempt++) {
    const token = await freshToken(attempt > 0);
    const res = await fetch(origin + path, {
      method,
      headers: { Authorization: "Bearer " + token, ...(body ? { "Content-Type": "application/json" } : {}) },
      body: body ? JSON.stringify(body) : undefined,
    });
    if (res.status === 401 && attempt === 0) continue;
    const out = res.status === 204 ? {} : await res.json().catch(() => ({}));
    if (!res.ok) throw new ApiError(res.status, out.error?.code || "error", out.error?.message || "Outflare couldn't do that. Try again.");
    return out;
  }
}

// ---- Known websites ----

async function digest(salt, domain) {
  const sum = new Uint8Array(await sha256(salt + ":" + domain));
  return [...sum.slice(0, 8)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// candidates are the domains a host could be filed under: itself without
// www, then each parent that still has a dot.
export function candidates(host) {
  const parts = host.toLowerCase().replace(/^www\./, "").split(".");
  const out = [];
  for (let i = 0; i + 1 < parts.length; i++) out.push(parts.slice(i).join("."));
  return out;
}

export function siteDomain(url) {
  try {
    const u = new URL(url);
    if (u.protocol !== "https:" && u.protocol !== "http:") return "";
    if (!u.hostname.includes(".") || /^[\d.]+$/.test(u.hostname)) return "";
    return u.hostname.toLowerCase().replace(/^www\./, "");
  } catch {
    return "";
  }
}

// refreshKnown fetches the brand's known and do-not-contact websites, as
// digests, so the toolbar icon can be set without telling Outflare which
// sites are visited.
export async function refreshKnown() {
  const { brand, auth } = await store.get(["brand", "auth"]);
  if (!brand || !auth) return null;
  const out = await api("GET", `/v1/brands/${brand.id}/records/known-domains`);
  const known = { salt: out.salt, companies: out.companies, blocked: out.do_not_contact, truncated: out.truncated, at: Date.now(), brand: brand.id };
  await store.set({ known });
  return known;
}

// siteStatus is "blocked", "known" or "new" for a page, from the stored
// list alone; "" when there is nothing to say (no list, not a website).
export async function siteStatus(url) {
  return await domainStatus(siteDomain(url));
}

// domainStatus is siteStatus for a domain.
export async function domainStatus(host) {
  const { known } = await store.get("known");
  if (!host || !known) return "";
  let status = "new";
  for (const d of candidates(host)) {
    const h = await digest(known.salt, d);
    if (known.blocked.includes(h)) return "blocked";
    if (known.companies.includes(h)) status = "known";
  }
  return status;
}

// Mailbox providers anyone can sign up to: an address there says nothing
// about a company.
export const FREE_MAIL = new Set(["gmail.com", "googlemail.com", "outlook.com", "hotmail.com", "live.com", "msn.com", "yahoo.com", "ymail.com",
  "icloud.com", "me.com", "mac.com", "aol.com", "proton.me", "protonmail.com", "pm.me", "gmx.com", "gmx.net", "mail.com", "zoho.com", "yandex.com",
  "hey.com", "fastmail.com", "tutanota.com", "tuta.com", "qq.com", "163.com"]);
