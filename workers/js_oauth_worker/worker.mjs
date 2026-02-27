#!/usr/bin/env node

/**
 * JS OAuth worker (protocol-only, no browser automation).
 * Node >=20 or Deno.
 */

const AUTH = "https://auth.openai.com";
const SENTINEL = "https://sentinel.openai.com/backend-api/sentinel/req";
const DEFAULT_UA =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36";

function isDeno() {
  return typeof globalThis.Deno !== "undefined";
}

function nowIso() {
  return new Date().toISOString();
}

function log(msg) {
  console.log(`[${nowIso()}] ${msg}`);
}

function getenv(name, fallback = "") {
  if (isDeno()) {
    const v = Deno.env.get(name);
    return typeof v === "string" && v.trim() ? v.trim() : fallback;
  }
  const v = process.env[name];
  return typeof v === "string" && v.trim() ? v.trim() : fallback;
}

function getenvInt(name, fallback) {
  const raw = getenv(name, "");
  if (!raw) return fallback;
  const n = Number.parseInt(raw, 10);
  return Number.isFinite(n) ? n : fallback;
}

function getenvBool(name, fallback) {
  const raw = getenv(name, "").toLowerCase();
  if (!raw) return fallback;
  if (["1", "true", "yes", "on"].includes(raw)) return true;
  if (["0", "false", "no", "off"].includes(raw)) return false;
  return fallback;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function bytesToString(bytes) {
  return new TextDecoder().decode(bytes);
}

function stringToBytes(s) {
  return new TextEncoder().encode(s);
}

function base64Encode(bytes) {
  if (typeof Buffer !== "undefined") return Buffer.from(bytes).toString("base64");
  let bin = "";
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin);
}

function base64DecodeToBytes(b64) {
  if (typeof Buffer !== "undefined") return new Uint8Array(Buffer.from(b64, "base64"));
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function base64UrlEncode(bytes) {
  return base64Encode(bytes).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
}

function base64UrlDecodeToString(raw) {
  const padded = raw.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (raw.length % 4)) % 4);
  return bytesToString(base64DecodeToBytes(padded));
}

function randomToken(bytes = 32) {
  const arr = new Uint8Array(bytes);
  crypto.getRandomValues(arr);
  return base64UrlEncode(arr);
}

function uuidv4() {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

async function sha256Base64Url(input) {
  const digest = await crypto.subtle.digest("SHA-256", stringToBytes(input));
  return base64UrlEncode(new Uint8Array(digest));
}

function parseJwtPayload(token) {
  try {
    const parts = String(token || "").split(".");
    if (parts.length < 2) return {};
    return JSON.parse(base64UrlDecodeToString(parts[1]));
  } catch {
    return {};
  }
}

function parseTs(value) {
  if (value === null || value === undefined) return 0;
  const n = Number(value);
  if (Number.isFinite(n) && n > 0) return n > 1e12 ? n / 1000 : n;
  const t = Date.parse(String(value));
  return Number.isFinite(t) ? t / 1000 : 0;
}

function extractOtp(mailObj) {
  const fields = ["subject", "raw", "rawData", "rawdata", "text", "textBody", "plain", "html", "htmlBody", "content", "body"];
  const chunks = [];
  const walk = (obj) => {
    if (obj === null || obj === undefined) return;
    if (typeof obj === "string") {
      if (obj.trim()) chunks.push(obj);
      return;
    }
    if (Array.isArray(obj)) {
      for (const x of obj) walk(x);
      return;
    }
    if (typeof obj === "object") {
      for (const k of fields) if (k in obj) walk(obj[k]);
      for (const v of Object.values(obj)) if (v && typeof v === "object") walk(v);
    }
  };
  walk(mailObj);
  const text = chunks.join("\n").replace(/=(\r\n|\n|\r)/g, "").replace(/=([A-Fa-f0-9]{2})/g, (_, h) => String.fromCharCode(Number.parseInt(h, 16)));
  const m = text.match(/\b(\d{6})\b/i) || text.match(/code\D{0,20}(\d{6})/i) || text.match(/otp\D{0,20}(\d{6})/i);
  return m && m[1] ? m[1] : "";
}

function mailTs(mailObj) {
  const keys = ["createdAt", "created_at", "created", "updatedAt", "date", "timestamp"];
  for (const k of keys) {
    if (k in mailObj) {
      const t = parseTs(mailObj[k]);
      if (t > 0) return t;
    }
  }
  return 0;
}

class CookieJar {
  constructor() {
    this.store = new Map(); // domain -> Map(name,value)
  }

  setCookie(name, value, domain) {
    const d = String(domain || "").trim().toLowerCase().replace(/^\./, "");
    if (!d || !name) return;
    if (!this.store.has(d)) this.store.set(d, new Map());
    this.store.get(d).set(String(name), String(value ?? ""));
  }

  getCookie(name, host = "") {
    const h = String(host || "").toLowerCase();
    for (const [domain, kv] of this.store.entries()) {
      if (h === domain || h.endsWith(`.${domain}`)) {
        if (kv.has(name)) return kv.get(name);
      }
    }
    return "";
  }

  absorbSetCookie(setCookie, requestUrl) {
    const u = new URL(requestUrl);
    const parts = String(setCookie || "").split(";").map((s) => s.trim()).filter(Boolean);
    if (!parts.length) return;
    const first = parts[0];
    const idx = first.indexOf("=");
    if (idx <= 0) return;
    const name = first.slice(0, idx).trim();
    const value = first.slice(idx + 1).trim();
    let domain = u.hostname;
    for (const p of parts.slice(1)) {
      const [k, v] = p.split("=", 2);
      if (String(k || "").toLowerCase() === "domain" && v) domain = String(v).trim();
    }
    this.setCookie(name, value, domain);
  }

  cookieHeader(urlObj) {
    const host = String(urlObj.hostname || "").toLowerCase();
    const merged = new Map();
    for (const [domain, kv] of this.store.entries()) {
      if (host === domain || host.endsWith(`.${domain}`)) {
        for (const [k, v] of kv.entries()) merged.set(k, v);
      }
    }
    if (!merged.size) return "";
    return [...merged.entries()].map(([k, v]) => `${k}=${v}`).join("; ");
  }
}

class HttpSession {
  constructor({ timeoutMs = 30000, userAgent = DEFAULT_UA } = {}) {
    this.timeoutMs = timeoutMs;
    this.userAgent = userAgent;
    this.jar = new CookieJar();
  }

  async request(url, { method = "GET", headers = {}, body = undefined, redirect = "manual", timeoutMs } = {}) {
    const t = Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : this.timeoutMs;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(new Error("timeout")), t);
    const reqHeaders = new Headers(headers || {});
    if (!reqHeaders.has("user-agent")) reqHeaders.set("user-agent", this.userAgent);
    if (!reqHeaders.has("accept")) reqHeaders.set("accept", "*/*");
    const c = this.jar.cookieHeader(new URL(url));
    if (c) reqHeaders.set("cookie", c);
    try {
      const resp = await fetch(url, { method, headers: reqHeaders, body, redirect, signal: controller.signal });
      if (typeof resp.headers.getSetCookie === "function") {
        for (const sc of resp.headers.getSetCookie()) this.jar.absorbSetCookie(sc, url);
      } else {
        const one = resp.headers.get("set-cookie");
        if (one) this.jar.absorbSetCookie(one, url);
      }
      return resp;
    } finally {
      clearTimeout(timer);
    }
  }

  async json(url, opts = {}) {
    const resp = await this.request(url, opts);
    const text = await resp.text();
    let data = {};
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        data = {};
      }
    }
    return { resp, text, data };
  }
}

class Pow {
  constructor(deviceId, maxAttempts = 300000) {
    this.deviceId = deviceId;
    this.maxAttempts = maxAttempts;
    this.sid = uuidv4();
  }

  static fnv32(text) {
    let h = 2166136261 >>> 0;
    for (let i = 0; i < text.length; i++) {
      h ^= text.charCodeAt(i);
      h = Math.imul(h, 16777619) >>> 0;
    }
    h ^= h >>> 16;
    h = Math.imul(h, 2246822507) >>> 0;
    h ^= h >>> 13;
    h = Math.imul(h, 3266489909) >>> 0;
    h ^= h >>> 16;
    return (h >>> 0).toString(16).padStart(8, "0");
  }

  static b64(obj) {
    return base64Encode(stringToBytes(JSON.stringify(obj)));
  }

  cfg() {
    const pn = Math.random() * (50000 - 1000) + 1000;
    return [
      "1920x1080",
      new Date().toUTCString().replace("GMT", "GMT+0000 (UTC)"),
      4294705152,
      Math.random(),
      DEFAULT_UA,
      "https://sentinel.openai.com/sentinel/sdk.js",
      null,
      null,
      "en-US",
      "en-US,en",
      Math.random(),
      "vendorSub-undefined",
      "location",
      "Object",
      pn,
      this.sid,
      "",
      [4, 8, 12, 16][Math.floor(Math.random() * 4)],
      Date.now() - pn,
    ];
  }

  req() {
    const c = this.cfg();
    c[3] = 1;
    c[9] = Math.round(Math.random() * 45 + 5);
    return `gAAAAAC${Pow.b64(c)}`;
  }

  solve(seed, diff) {
    const start = Date.now();
    const c = this.cfg();
    const target = String(diff || "0");
    const dl = target.length;
    for (let i = 0; i < this.maxAttempts; i++) {
      c[3] = i;
      c[9] = Date.now() - start;
      const pay = Pow.b64(c);
      if (Pow.fnv32(seed + pay).slice(0, dl) <= target) return `gAAAAAB${pay}~S`;
    }
    throw new Error("pow exceeded max attempts");
  }
}

async function sentinelToken(session, timeoutMs, deviceId, flow) {
  const pw = new Pow(deviceId);
  const body = JSON.stringify({ p: pw.req(), id: deviceId, flow });
  const headers = {
    "content-type": "text/plain;charset=UTF-8",
    origin: "https://sentinel.openai.com",
    referer: "https://sentinel.openai.com/backend-api/sentinel/frame.html",
    "user-agent": DEFAULT_UA,
  };
  const { resp, data } = await session.json(SENTINEL, { method: "POST", headers, body, timeoutMs });
  if (resp.status !== 200) throw new Error(`sentinel challenge failed ${resp.status}`);
  const info = data && typeof data === "object" ? data.proofofwork || {} : {};
  const seed = String(info.seed || "");
  const diff = String(info.difficulty || "0");
  const required = Boolean(info.required);
  const p = required && seed ? pw.solve(seed, diff) : pw.req();
  return JSON.stringify({ p, t: "", c: String(data?.token || ""), id: deviceId, flow });
}

async function pkcePair() {
  const verifier = randomToken(64);
  const challenge = await sha256Base64Url(verifier);
  return { verifier, challenge };
}

function codeFromUrl(url) {
  if (!url || !String(url).includes("code=")) return "";
  try {
    const code = String(new URL(url).searchParams.get("code") || "");
    if (!code || code.length < 12 || code.includes(" ")) return "";
    return code;
  } catch {
    return "";
  }
}

function cpaSafeName(email) {
  const e = String(email || "").trim().toLowerCase().replace(/[^a-z0-9_.-]/g, "_");
  return e || "unknown";
}

function cpaPayload(email, tokens) {
  const accessToken = String(tokens?.access_token || "");
  const refreshToken = String(tokens?.refresh_token || "");
  const idToken = String(tokens?.id_token || "");
  const payload = parseJwtPayload(accessToken);
  const ns = payload && typeof payload === "object" ? payload["https://api.openai.com/auth"] || {} : {};
  const accountId = typeof ns === "object" ? String(ns.chatgpt_account_id || "") : "";
  const exp = Number(payload?.exp || 0);
  const expired = exp > 0 ? new Date(exp * 1000).toISOString() : "";
  return {
    type: "codex",
    email: String(email || ""),
    expired,
    id_token: idToken,
    account_id: accountId,
    access_token: accessToken,
    last_refresh: nowIso(),
    refresh_token: refreshToken,
  };
}

class SupabaseClient {
  constructor({ url, key, accountsTable }) {
    this.url = String(url || "").replace(/\/+$/, "");
    this.key = String(key || "");
    this.accountsTable = accountsTable || "accounts_pool";
  }

  headers(prefer = "return=minimal") {
    return {
      apikey: this.key,
      authorization: `Bearer ${this.key}`,
      "content-type": "application/json",
      prefer,
    };
  }

  async listAccounts(limit = 200) {
    const qs = new URLSearchParams({
      select: "email,password,status,token_alive,refresh_token,account_identity,updated_at",
      order: "updated_at.asc",
      limit: String(limit),
    });
    const url = `${this.url}/rest/v1/${this.accountsTable}?${qs.toString()}`;
    const resp = await fetch(url, { headers: this.headers("return=representation") });
    const text = await resp.text();
    if (!resp.ok) throw new Error(`supabase list failed ${resp.status}: ${text.slice(0, 300)}`);
    const rows = JSON.parse(text || "[]");
    return Array.isArray(rows) ? rows : [];
  }

  async getAccountByEmail(email) {
    const qs = new URLSearchParams({
      select: "email,password,status,token_alive,refresh_token,account_identity,updated_at",
      email: `eq.${email}`,
      limit: "1",
    });
    const url = `${this.url}/rest/v1/${this.accountsTable}?${qs.toString()}`;
    const resp = await fetch(url, { headers: this.headers("return=representation") });
    const text = await resp.text();
    if (!resp.ok) throw new Error(`supabase get failed ${resp.status}: ${text.slice(0, 300)}`);
    const arr = JSON.parse(text || "[]");
    return Array.isArray(arr) && arr.length ? arr[0] : null;
  }

  async patchByEmail(email, patch) {
    const qs = new URLSearchParams({ email: `eq.${email}` });
    const url = `${this.url}/rest/v1/${this.accountsTable}?${qs.toString()}`;
    const resp = await fetch(url, {
      method: "PATCH",
      headers: this.headers("return=minimal"),
      body: JSON.stringify(patch),
    });
    const text = await resp.text();
    if (!resp.ok) throw new Error(`supabase patch failed ${resp.status}: ${text.slice(0, 300)}`);
  }
}

class ProtocolClient {
  constructor(cfg) {
    this.cfg = cfg;
    this.device = uuidv4();
    this.session = new HttpSession({ timeoutMs: cfg.network.timeoutMs, userAgent: cfg.network.userAgent });
    this.mailSession = new HttpSession({ timeoutMs: cfg.network.timeoutMs, userAgent: cfg.network.userAgent });
  }

  setDeviceCookies() {
    this.session.jar.setCookie("oai-did", this.device, "auth.openai.com");
    this.session.jar.setCookie("oai-did", this.device, ".auth.openai.com");
  }

  async apiHeaders(referer, flow = "") {
    const headers = {
      accept: "application/json",
      "accept-language": "en-US,en;q=0.9",
      "content-type": "application/json",
      origin: AUTH,
      referer,
      "user-agent": this.cfg.network.userAgent,
      "oai-device-id": this.device,
    };
    if (flow) headers["openai-sentinel-token"] = await sentinelToken(this.session, this.cfg.network.timeoutMs, this.device, flow);
    return headers;
  }

  navHeaders(referer = "") {
    const headers = {
      accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
      "accept-language": "en-US,en;q=0.9",
      "user-agent": this.cfg.network.userAgent,
    };
    if (referer) headers.referer = referer;
    return headers;
  }

  authUrl(codeChallenge, state, signup) {
    const q = new URLSearchParams({
      response_type: "code",
      client_id: this.cfg.oauth.clientId,
      redirect_uri: this.cfg.oauth.redirectUri,
      scope: this.cfg.oauth.scope,
      code_challenge: codeChallenge,
      code_challenge_method: "S256",
      state,
    });
    if (signup) {
      q.set("screen_hint", "signup");
      q.set("prompt", "login");
    }
    return `${AUTH}/oauth/authorize?${q.toString()}`;
  }

  async postContinue(email, signup) {
    const ref = signup ? `${AUTH}/create-account` : `${AUTH}/log-in`;
    const body = { username: { kind: "email", value: email } };
    if (signup) body.screen_hint = "signup";
    return this.session.json(`${AUTH}/api/accounts/authorize/continue`, {
      method: "POST",
      headers: await this.apiHeaders(ref, "authorize_continue"),
      body: JSON.stringify(body),
    });
  }

  decodeAuthCookie() {
    const raw = this.session.jar.getCookie("oai-client-auth-session", "auth.openai.com");
    if (!raw) return {};
    try {
      const seg = String(raw).split(".")[0];
      return JSON.parse(base64UrlDecodeToString(seg));
    } catch {
      return {};
    }
  }

  async consentSelect(consentUrl) {
    const decoded = this.decodeAuthCookie();
    const ws = Array.isArray(decoded?.workspaces) ? decoded.workspaces : [];
    if (!ws.length || typeof ws[0] !== "object") return "";
    const wid = String(ws[0]?.id || "");
    if (!wid) return "";

    const hh = await this.apiHeaders(consentUrl, "");
    const wsr = await this.session.json(`${AUTH}/api/accounts/workspace/select`, {
      method: "POST",
      headers: hh,
      body: JSON.stringify({ workspace_id: wid }),
      redirect: "manual",
    });
    if ([301, 302, 303, 307, 308].includes(wsr.resp.status)) {
      const loc = String(wsr.resp.headers.get("location") || "");
      if (loc) return new URL(loc, AUTH).toString();
    }

    const wsj = wsr.data && typeof wsr.data === "object" ? wsr.data : {};
    const wsNext = String(wsj.continue_url || "");
    let orgs = [];
    if (typeof wsj.data === "object" && Array.isArray(wsj.data.orgs)) orgs = wsj.data.orgs;
    if (!orgs.length && Array.isArray(wsj.orgs)) orgs = wsj.orgs;

    if (orgs.length && typeof orgs[0] === "object") {
      const o0 = orgs[0];
      const oid = String(o0.id || "");
      const projects = Array.isArray(o0.projects) ? o0.projects : [];
      const pid = projects.length && typeof projects[0] === "object" ? String(projects[0].id || "") : "";
      if (oid) {
        const body = pid ? { org_id: oid, project_id: pid } : { org_id: oid };
        const orr = await this.session.json(`${AUTH}/api/accounts/organization/select`, {
          method: "POST",
          headers: hh,
          body: JSON.stringify(body),
          redirect: "manual",
        });
        if ([301, 302, 303, 307, 308].includes(orr.resp.status)) {
          const loc = String(orr.resp.headers.get("location") || "");
          if (loc) return new URL(loc, AUTH).toString();
        }
        const oj = orr.data && typeof orr.data === "object" ? orr.data : {};
        const onext = String(oj.continue_url || "");
        if (onext) return new URL(onext, AUTH).toString();
      }
    }
    if (wsNext) return new URL(wsNext, AUTH).toString();
    return "";
  }

  async followCode(startUrl) {
    let u = startUrl;
    if (!u) return "";
    for (let i = 0; i < 16; i++) {
      const direct = codeFromUrl(u);
      if (direct) return direct;
      const resp = await this.session.request(u, {
        method: "GET",
        headers: this.navHeaders(),
        redirect: "manual",
      });
      const urlUsed = String(resp.url || u);
      const fromUsed = codeFromUrl(urlUsed);
      if (fromUsed) return fromUsed;
      if ([301, 302, 303, 307, 308].includes(resp.status)) {
        const loc = String(resp.headers.get("location") || "");
        if (!loc) break;
        const abs = new URL(loc, u).toString();
        const fromLoc = codeFromUrl(abs);
        if (fromLoc) return fromLoc;
        u = abs;
        continue;
      }
      if (resp.status === 200 && urlUsed.includes("consent")) {
        const next = await this.consentSelect(urlUsed);
        if (next) {
          u = next;
          continue;
        }
      }
      break;
    }
    return "";
  }

  async tokenExchange(code, verifier) {
    const form = new URLSearchParams({
      grant_type: "authorization_code",
      client_id: this.cfg.oauth.clientId,
      code,
      redirect_uri: this.cfg.oauth.redirectUri,
      code_verifier: verifier,
    });
    const headers = {
      accept: "application/json",
      "content-type": "application/x-www-form-urlencoded",
      origin: AUTH,
      referer: AUTH,
      "user-agent": this.cfg.network.userAgent,
    };
    const { resp, data, text } = await this.session.json(`${AUTH}/oauth/token`, { method: "POST", headers, body: form.toString() });
    if (resp.status !== 200) return { ok: false, error: `oauth_token_failed_${resp.status}`, body: text.slice(0, 300) };
    if (!String(data?.access_token || "")) return { ok: false, error: "oauth_token_missing_access_token" };
    return { ok: true, tokens: data };
  }

  async adminToken(email) {
    const wd = this.cfg.mail.workerDomain;
    const ap = this.cfg.mail.adminPassword;
    if (!wd || !ap || !email) return "";
    const h = { "x-admin-auth": ap };
    const q = new URLSearchParams({ query: email, limit: "1", offset: "0" });
    const r1 = await this.mailSession.json(`https://${wd}/admin/address?${q.toString()}`, { method: "GET", headers: h });
    if (r1.resp.status !== 200) return "";
    const arr = Array.isArray(r1.data?.results) ? r1.data.results : [];
    if (!arr.length || typeof arr[0] !== "object") return "";
    const aid = String(arr[0].id || "");
    if (!aid) return "";
    const r2 = await this.mailSession.json(`https://${wd}/admin/show_password/${encodeURIComponent(aid)}`, { method: "GET", headers: h });
    if (r2.resp.status !== 200) return "";
    return String(r2.data?.jwt || "");
  }

  async fetchMails(email, cfToken) {
    const wd = this.cfg.mail.workerDomain;
    let token = String(cfToken || "").trim();
    if (!token) token = await this.adminToken(email);
    if (!token) return [];
    const rows = [];
    const maxPages = Math.max(1, this.cfg.otp.maxPages);
    for (let i = 0; i < maxPages; i++) {
      const q = new URLSearchParams({
        limit: String(this.cfg.otp.pageSize),
        offset: String(i * this.cfg.otp.pageSize),
      });
      const { resp, data } = await this.mailSession.json(`https://${wd}/api/mails?${q.toString()}`, {
        method: "GET",
        headers: { authorization: `Bearer ${token}` },
      });
      if (resp.status !== 200) break;
      const batch = Array.isArray(data?.results) ? data.results : [];
      if (!batch.length) break;
      rows.push(...batch.filter((x) => x && typeof x === "object"));
      if (batch.length < this.cfg.otp.pageSize) break;
    }
    return rows;
  }

  async waitOtp(email, cfToken, { notBeforeTs = 0, requireNewMail = false, rejectCodes = [] } = {}) {
    const start = Date.now() / 1000;
    const rejects = new Set((rejectCodes || []).map((x) => String(x || "").trim()).filter(Boolean));
    const inPre = (mt) => mt > 0 && mt >= start - this.cfg.otp.prePollWindowSeconds && mt <= start + 10;

    const base = await this.fetchMails(email, cfToken);
    const baseIds = new Set(base.map((x) => String(x.id || "")));
    const baseTs = Math.max(0, ...base.map((x) => mailTs(x)));

    if (this.cfg.otp.latestOnly && !requireNewMail) {
      for (const m of [...base].sort((a, b) => mailTs(b) - mailTs(a)).slice(0, 5)) {
        const code = extractOtp(m);
        const mt = mailTs(m);
        if (!code || rejects.has(code)) continue;
        if (notBeforeTs > 0 && mt > 0 && mt < notBeforeTs) continue;
        if (mt === 0 || Date.now() / 1000 - mt <= this.cfg.otp.recentSeconds || inPre(mt)) return code;
      }
    }

    const tried = new Set();
    const end = Date.now() + this.cfg.otp.timeoutSeconds * 1000;
    while (Date.now() < end) {
      const all = await this.fetchMails(email, cfToken);
      const sorted = [...all].sort((a, b) => mailTs(b) - mailTs(a));
      const candidates = this.cfg.otp.latestOnly
        ? (requireNewMail ? sorted.filter((x) => !baseIds.has(String(x.id || "")) || mailTs(x) >= baseTs) : sorted.slice(0, 10))
        : sorted.filter((x) => !baseIds.has(String(x.id || "")) || mailTs(x) >= baseTs);

      for (const m of candidates) {
        const code = extractOtp(m);
        const mt = mailTs(m);
        if (!code || tried.has(code) || rejects.has(code)) continue;
        if (notBeforeTs > 0 && mt > 0 && mt < notBeforeTs) continue;
        if (mt > 0 && Date.now() / 1000 - mt > this.cfg.otp.recentSeconds && !inPre(mt)) continue;
        tried.add(code);
        return code;
      }
      await sleep(this.cfg.otp.pollIntervalSeconds * 1000);
    }
    return "";
  }

  async login(email, password, cfToken = "") {
    this.setDeviceCookies();
    const { verifier, challenge } = await pkcePair();
    const state = randomToken(24);

    log(`login ${email} step=authorize`);
    const r0 = await this.session.request(this.authUrl(challenge, state, false), {
      method: "GET",
      headers: this.navHeaders(),
      redirect: "follow",
    });
    if (![200, 302].includes(r0.status)) return { ok: false, error: `authorize_failed_${r0.status}` };

    log(`login ${email} step=authorize_continue`);
    const r1 = await this.postContinue(email, false);
    if (r1.resp.status !== 200) return { ok: false, error: `authorize_continue_failed_${r1.resp.status}`, body: r1.text.slice(0, 300) };

    log(`login ${email} step=password_verify`);
    const r2 = await this.session.json(`${AUTH}/api/accounts/password/verify`, {
      method: "POST",
      headers: await this.apiHeaders(`${AUTH}/log-in/password`, "password_verify"),
      body: JSON.stringify({ password }),
      redirect: "manual",
    });
    if (r2.resp.status !== 200) return { ok: false, error: `password_verify_failed_${r2.resp.status}`, body: r2.text.slice(0, 300) };

    let continueUrl = String(r2.data?.continue_url || "");
    const pageType = String(r2.data?.page?.type || "");
    if (!continueUrl) return { ok: false, error: "continue_url_missing_after_password" };

    if (continueUrl.includes("email-verification") || pageType === "email_otp_verification") {
      log(`login ${email} step=otp`);
      const otpMark = Date.now() / 1000;
      let code = await this.waitOtp(email, cfToken, { notBeforeTs: otpMark - 2, requireNewMail: true });
      if (!code) return { ok: false, error: "otp_not_found_for_login" };
      let r3 = await this.session.json(`${AUTH}/api/accounts/email-otp/validate`, {
        method: "POST",
        headers: await this.apiHeaders(`${AUTH}/email-verification`, ""),
        body: JSON.stringify({ code }),
      });
      if (r3.resp.status === 401) {
        const code2 = await this.waitOtp(email, cfToken, {
          notBeforeTs: Date.now() / 1000 - 2,
          requireNewMail: true,
          rejectCodes: [code],
        });
        if (!code2) return { ok: false, error: "otp_not_found_for_login_retry" };
        r3 = await this.session.json(`${AUTH}/api/accounts/email-otp/validate`, {
          method: "POST",
          headers: await this.apiHeaders(`${AUTH}/email-verification`, ""),
          body: JSON.stringify({ code: code2 }),
        });
      }
      if (r3.resp.status !== 200) return { ok: false, error: `otp_validate_login_failed_${r3.resp.status}`, body: r3.text.slice(0, 300) };
      if (typeof r3.data?.continue_url === "string" && r3.data.continue_url) continueUrl = r3.data.continue_url;
    }

    log(`login ${email} step=consent_code`);
    const consentUrl = new URL(continueUrl, AUTH).toString();
    let code = "";
    const r4 = await this.session.request(consentUrl, { method: "GET", headers: this.navHeaders(), redirect: "manual" });
    if ([301, 302, 303, 307, 308].includes(r4.status)) {
      const loc = String(r4.headers.get("location") || "");
      const abs = loc ? new URL(loc, consentUrl).toString() : "";
      code = codeFromUrl(abs) || (abs ? await this.followCode(abs) : "");
    } else if (r4.status === 200) {
      code = codeFromUrl(String(r4.url || ""));
      if (!code) code = await this.followCode(consentUrl);
    }
    if (!code) return { ok: false, error: "authorization_code_not_found" };

    log(`login ${email} step=oauth_token`);
    const tk = await this.tokenExchange(code, verifier);
    if (!tk.ok) return tk;
    const at = String(tk.tokens?.access_token || "");
    const payload = parseJwtPayload(at);
    const ns = payload && typeof payload === "object" ? payload["https://api.openai.com/auth"] || {} : {};
    const accountId = typeof ns === "object" ? String(ns.chatgpt_account_id || "") : "";
    return { ok: true, tokens: tk.tokens, account_id: accountId, token_len: at.length };
  }
}

async function cpaUpload(cpaCfg, email, tokens) {
  if (!cpaCfg.enabled) return { ok: false, skipped: true, reason: "cpa_disabled" };
  if (!cpaCfg.base || !cpaCfg.key) return { ok: false, skipped: true, reason: "cpa_config_missing" };
  const name = `${cpaCfg.filenamePrefix}${cpaSafeName(email)}.json`;
  const url = `${cpaCfg.base.replace(/\/+$/, "")}/v0/management/auth-files?name=${encodeURIComponent(name)}`;
  const payload = JSON.stringify(cpaPayload(email, tokens));
  const resp = await fetch(url, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      authorization: `Bearer ${cpaCfg.key}`,
      "x-management-key": cpaCfg.key,
    },
    body: payload,
  });
  const text = await resp.text();
  if (!resp.ok) return { ok: false, status: resp.status, error: text.slice(0, 300), filename: name };
  return { ok: true, filename: name };
}

function buildConfig() {
  return {
    oauth: {
      clientId: getenv("OAUTH_CLIENT_ID", "app_EMoamEEZ73f0CkXaXp7hrann"),
      redirectUri: getenv("OAUTH_REDIRECT_URI", "http://localhost:1455/auth/callback"),
      scope: getenv("OAUTH_SCOPE", "openid profile email offline_access"),
    },
    network: {
      timeoutMs: getenvInt("NETWORK_TIMEOUT_MS", 30000),
      userAgent: getenv("NETWORK_USER_AGENT", DEFAULT_UA),
    },
    mail: {
      workerDomain: getenv("MAIL_WORKER_DOMAIN", "cf-temp-email.mengcenfay.workers.dev"),
      adminPassword: getenv("MAIL_ADMIN_PASSWORD", ""),
      cfToken: getenv("MAIL_CF_TOKEN", ""),
    },
    otp: {
      timeoutSeconds: getenvInt("OTP_TIMEOUT_SECONDS", 120),
      pollIntervalSeconds: getenvInt("OTP_POLL_SECONDS", 3),
      recentSeconds: getenvInt("OTP_RECENT_SECONDS", 180),
      prePollWindowSeconds: getenvInt("OTP_PRE_POLL_WINDOW_SECONDS", 30),
      latestOnly: getenvBool("OTP_LATEST_ONLY", true),
      maxPages: getenvInt("OTP_MAX_PAGES", 3),
      pageSize: getenvInt("OTP_PAGE_SIZE", 10),
    },
    supabase: {
      url: getenv("SUPABASE_URL", ""),
      key: getenv("SUPABASE_SERVICE_KEY", ""),
      accountsTable: getenv("SUPABASE_ACCOUNTS_TABLE", "accounts_pool"),
    },
    cpa: {
      enabled: getenvBool("CPA_ENABLED", false),
      base: getenv("CPA_BASE", ""),
      key: getenv("CPA_KEY", ""),
      filenamePrefix: getenv("CPA_FILENAME_PREFIX", "codex-"),
    },
    worker: {
      statuses: getenv("WORKER_STATUSES", "registered_pending_login,login_failed,normal")
        .split(",")
        .map((x) => x.trim().toLowerCase())
        .filter(Boolean),
      includeTeam: getenvBool("WORKER_INCLUDE_TEAM", false),
      processWithRefresh: getenvBool("WORKER_PROCESS_WITH_REFRESH", false),
      maxItems: getenvInt("WORKER_MAX_ITEMS", 10),
      listLimit: getenvInt("WORKER_LIST_LIMIT", 200),
      successStatus: getenv("WORKER_SUCCESS_STATUS", "ready"),
      failedStatus: getenv("WORKER_FAILED_STATUS", "login_failed"),
      loop: getenvBool("WORKER_LOOP", false),
      intervalSeconds: getenvInt("WORKER_INTERVAL_SECONDS", 180),
      dryRun: getenvBool("WORKER_DRY_RUN", false),
      singleEmail: getenv("WORKER_EMAIL", "").toLowerCase(),
    },
  };
}

function parseCliArgs() {
  const raw = isDeno() ? Deno.args : process.argv.slice(2);
  const out = {};
  for (let i = 0; i < raw.length; i++) {
    const arg = raw[i];
    if (!arg.startsWith("--")) continue;
    const s = arg.slice(2);
    const eq = s.indexOf("=");
    if (eq >= 0) {
      out[s.slice(0, eq)] = s.slice(eq + 1);
      continue;
    }
    const next = raw[i + 1];
    if (!next || next.startsWith("--")) out[s] = "true";
    else {
      out[s] = next;
      i++;
    }
  }
  return out;
}

function applyCli(cfg, cli) {
  if (cli.email) cfg.worker.singleEmail = String(cli.email).trim().toLowerCase();
  if (cli["max-items"]) cfg.worker.maxItems = Number.parseInt(cli["max-items"], 10) || cfg.worker.maxItems;
  if (cli.limit) cfg.worker.listLimit = Number.parseInt(cli.limit, 10) || cfg.worker.listLimit;
  if (cli.statuses) cfg.worker.statuses = String(cli.statuses).split(",").map((x) => x.trim().toLowerCase()).filter(Boolean);
  if (cli["include-team"] === "true") cfg.worker.includeTeam = true;
  if (cli["process-with-refresh"] === "true") cfg.worker.processWithRefresh = true;
  if (cli["dry-run"] === "true") cfg.worker.dryRun = true;
  if (cli.loop === "true") cfg.worker.loop = true;
  if (cli["interval-seconds"]) {
    const n = Number.parseInt(cli["interval-seconds"], 10);
    if (Number.isFinite(n) && n > 0) cfg.worker.intervalSeconds = n;
  }
}

function selectCandidates(rows, cfg) {
  const out = [];
  for (const row of rows) {
    if (!row || typeof row !== "object") continue;
    const email = String(row.email || "").trim().toLowerCase();
    const password = String(row.password || "").trim();
    if (!email || !password) continue;
    if (cfg.worker.singleEmail && cfg.worker.singleEmail !== email) continue;
    if (Boolean(row.token_alive)) continue;
    const status = String(row.status || "").trim().toLowerCase();
    if (cfg.worker.statuses.length && !cfg.worker.statuses.includes(status)) continue;
    const identity = String(row.account_identity || "normal").trim().toLowerCase();
    if (!cfg.worker.includeTeam && ["team_owner", "team_member"].includes(identity)) continue;
    const rt = String(row.refresh_token || "").trim();
    if (!cfg.worker.processWithRefresh && rt) continue;
    out.push({ ...row, email, password, status, account_identity: identity });
    if (cfg.worker.maxItems > 0 && out.length >= cfg.worker.maxItems) break;
  }
  return out;
}

async function processOne(sb, cfg, account) {
  const email = String(account.email || "").trim().toLowerCase();
  const password = String(account.password || "");
  const protocol = new ProtocolClient(cfg);
  const cfToken = cfg.mail.cfToken || (await protocol.adminToken(email));
  if (!cfToken) {
    const err = "mail_token_missing";
    await sb.patchByEmail(email, {
      status: cfg.worker.failedStatus,
      error: err,
      token_alive: false,
      token_check_method: "oauth_protocol_login",
      last_token_check: nowIso(),
      updated_at: nowIso(),
    });
    return { email, ok: false, error: err };
  }

  const login = await protocol.login(email, password, cfToken);
  if (!login.ok) {
    const err = String(login.error || "protocol_login_failed");
    await sb.patchByEmail(email, {
      status: cfg.worker.failedStatus,
      error: err,
      token_alive: false,
      token_check_method: "oauth_protocol_login",
      last_token_check: nowIso(),
      updated_at: nowIso(),
    });
    return { email, ok: false, error: err };
  }

  const tk = login.tokens || {};
  const accessToken = String(tk.access_token || "");
  const refreshToken = String(tk.refresh_token || "");
  const idToken = String(tk.id_token || "");
  const payload = parseJwtPayload(accessToken);
  const exp = Number(payload?.exp || 0);
  const expIso = exp > 0 ? new Date(exp * 1000).toISOString() : null;

  const cpa = await cpaUpload(cfg.cpa, email, tk);
  const status = cpa.ok ? "fed" : cfg.worker.successStatus;
  const error = cpa.ok || cpa.skipped ? null : String(cpa.error || "cpa_upload_failed");

  await sb.patchByEmail(email, {
    status,
    error,
    token_len: accessToken.length,
    cpa_filename: cpa.filename || null,
    access_token: accessToken || null,
    refresh_token: refreshToken || null,
    id_token: idToken || null,
    token_alive: accessToken.length > 0,
    token_check_method: "oauth_protocol_login",
    token_expired_at: expIso,
    token_last_refresh: refreshToken ? nowIso() : null,
    last_token_check: nowIso(),
    updated_at: nowIso(),
  });

  return { email, ok: true, status, token_len: accessToken.length, cpa_ok: Boolean(cpa.ok) };
}

async function runOnce(cfg) {
  if (!cfg.supabase.url || !cfg.supabase.key) throw new Error("SUPABASE_URL and SUPABASE_SERVICE_KEY are required");
  if (!cfg.mail.workerDomain || !cfg.mail.adminPassword) throw new Error("MAIL_WORKER_DOMAIN and MAIL_ADMIN_PASSWORD are required");
  const sb = new SupabaseClient(cfg.supabase);
  let rows = [];
  if (cfg.worker.singleEmail) {
    const one = await sb.getAccountByEmail(cfg.worker.singleEmail);
    rows = one ? [one] : [];
  } else {
    rows = await sb.listAccounts(cfg.worker.listLimit);
  }
  const candidates = selectCandidates(rows, cfg);
  const results = [];

  for (const row of candidates) {
    const email = String(row.email || "");
    try {
      if (cfg.worker.dryRun) {
        results.push({ email, ok: true, dry_run: true });
        continue;
      }
      results.push(await processOne(sb, cfg, row));
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      results.push({ email, ok: false, error: msg });
      try {
        await sb.patchByEmail(email, {
          status: cfg.worker.failedStatus,
          error: msg.slice(0, 500),
          token_alive: false,
          token_check_method: "oauth_protocol_login",
          last_token_check: nowIso(),
          updated_at: nowIso(),
        });
      } catch {
        // ignore
      }
    }
  }
  const failed = results.filter((x) => !x.ok).length;
  return {
    ok: failed === 0,
    listed: rows.length,
    selected: candidates.length,
    processed: results.length,
    failed,
    dry_run: cfg.worker.dryRun,
    results,
  };
}

async function main() {
  const cfg = buildConfig();
  applyCli(cfg, parseCliArgs());
  if (!cfg.worker.loop) {
    const out = await runOnce(cfg);
    console.log(JSON.stringify(out));
    if (!out.ok) {
      if (isDeno()) Deno.exit(2);
      process.exit(2);
    }
    return;
  }
  while (true) {
    try {
      console.log(JSON.stringify(await runOnce(cfg)));
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      console.error(JSON.stringify({ ok: false, error: msg }));
    }
    await sleep(Math.max(10, cfg.worker.intervalSeconds) * 1000);
  }
}

main().catch((err) => {
  const msg = err instanceof Error ? err.message : String(err);
  console.error(JSON.stringify({ ok: false, fatal: true, error: msg }));
  if (isDeno()) Deno.exit(1);
  process.exit(1);
});
