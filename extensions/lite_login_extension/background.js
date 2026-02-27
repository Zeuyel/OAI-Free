const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const LAST_SUCCESS_KEY = "lastSuccessState";
const ACCOUNT_ID_EMAIL_MAP_KEY = "accountIdEmailMap";
const SUBSCRIPTION_DIRTY_QUEUE_KEY = "subscriptionDirtyQueue";
const HANDLED_SUCCESS_URLS = new Set();

async function setupSidePanelBehavior() {
  try {
    if (chrome.sidePanel && chrome.sidePanel.setPanelBehavior) {
      await chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: true });
    }
  } catch (_) {
  }
}

chrome.runtime.onInstalled.addListener(() => {
  setupSidePanelBehavior();
});

chrome.runtime.onStartup.addListener(() => {
  setupSidePanelBehavior();
});

function parseBool(v) {
  return String(v || "").trim().toLowerCase() === "true";
}

function parseIntSafe(v, fallback = 0) {
  const n = Number.parseInt(String(v ?? "").trim(), 10);
  if (!Number.isFinite(n) || Number.isNaN(n)) return fallback;
  return n;
}

function storageGet(keys) {
  return new Promise((resolve) => chrome.storage.local.get(keys, resolve));
}

function storageSet(data) {
  return new Promise((resolve) => chrome.storage.local.set(data, resolve));
}

function queryTabs(queryInfo) {
  return new Promise((resolve) => chrome.tabs.query(queryInfo, resolve));
}

function updateTab(tabId, updateProperties) {
  return new Promise((resolve, reject) => {
    chrome.tabs.update(tabId, updateProperties, (tab) => {
      const err = chrome.runtime.lastError;
      if (err) return reject(new Error(err.message));
      resolve(tab);
    });
  });
}

function createTab(createProperties) {
  return new Promise((resolve, reject) => {
    chrome.tabs.create(createProperties, (tab) => {
      const err = chrome.runtime.lastError;
      if (err) return reject(new Error(err.message));
      resolve(tab);
    });
  });
}

function executeScriptOnTab(tabId, func, args = []) {
  return new Promise((resolve, reject) => {
    chrome.scripting.executeScript(
      {
        target: { tabId },
        func,
        args,
        world: "MAIN",
      },
      (results) => {
        const err = chrome.runtime.lastError;
        if (err) return reject(new Error(err.message));
        resolve(Array.isArray(results) ? results : []);
      },
    );
  });
}

function executeScriptAllFrames(tabId, func, args = []) {
  return new Promise((resolve, reject) => {
    chrome.scripting.executeScript(
      {
        target: { tabId, allFrames: true },
        func,
        args,
        world: "MAIN",
      },
      (results) => {
        const err = chrome.runtime.lastError;
        if (err) return reject(new Error(err.message));
        resolve(Array.isArray(results) ? results : []);
      },
    );
  });
}

function getCookie(details) {
  return new Promise((resolve) => chrome.cookies.get(details, resolve));
}

function getAllCookieStores() {
  return new Promise((resolve) => chrome.cookies.getAllCookieStores(resolve));
}

function sendRuntimeMessage(payload) {
  return new Promise((resolve) => {
    try {
      chrome.runtime.sendMessage(payload, () => resolve(true));
    } catch (_) {
      resolve(false);
    }
  });
}

async function getBestActiveTab(sourceWindowId) {
  const hasWindowId = Number.isInteger(sourceWindowId) && sourceWindowId >= 0;
  const inWindow = await queryTabs(
    hasWindowId ? { active: true, windowId: sourceWindowId } : { active: true, currentWindow: true },
  );
  if (inWindow && inWindow[0] && typeof inWindow[0].id === "number") {
    return inWindow[0];
  }
  const all = await queryTabs({ active: true });
  if (all && all[0] && typeof all[0].id === "number") {
    return all[0];
  }
  throw new Error("未找到活动标签页");
}

function isRestrictedBrowserUrl(rawUrl) {
  const u = String(rawUrl || "").trim().toLowerCase();
  if (!u) return false;
  return (
    u.startsWith("chrome://") ||
    u.startsWith("edge://") ||
    u.startsWith("about:") ||
    u.startsWith("devtools://") ||
    u.startsWith("chrome-extension://") ||
    u.startsWith("edge-extension://")
  );
}

function buildHeaders(settings, withJSON = false) {
  const headers = {};
  const apiKey = String(settings.apiKey || "").trim();
  if (apiKey) {
    headers["X-Api-Key"] = apiKey;
    headers["X-Service-Token"] = apiKey;
  }
  if (withJSON) {
    headers["Content-Type"] = "application/json";
  }
  return headers;
}

function getApiBase(settings) {
  const base = String(settings.apiBase || "").trim().replace(/\/+$/, "");
  if (!base) throw new Error("apiBase 为空");
  return base;
}

async function callGoTeamAPI(settings, path, options = {}) {
  const base = getApiBase(settings);
  const method = String(options.method || "GET").toUpperCase();
  const withJSON = options.body !== undefined;
  const headers = {
    ...buildHeaders(settings, withJSON),
    ...(options.headers || {}),
  };
  const res = await fetch(`${base}${path}`, {
    method,
    headers,
    body: withJSON ? JSON.stringify(options.body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok || !data.ok) {
    throw new Error(data.error || data.detail || `api ${res.status}`);
  }
  return data;
}

async function loadAccounts(settings) {
  const accountType = String(settings.accountType || "").trim();
  const onlyInvitePool = parseBool(settings.onlyInvitePool);
  const q = new URLSearchParams();
  q.set("limit", "500");
  if (accountType) q.set("account_type", accountType);
  if (onlyInvitePool) q.set("tag", "invite_pool");
  const data = await callGoTeamAPI(settings, `/v1/accounts?${q.toString()}`, { method: "GET" });
  const rows = Array.isArray(data.accounts) ? data.accounts : [];
  return { ok: true, count: rows.length, accounts: rows };
}

async function getAccountCredentials(settings, accountId) {
  const aid = String(accountId || "").trim();
  if (!aid) throw new Error("accountId 为空");
  const data = await callGoTeamAPI(settings, `/v1/accounts/${encodeURIComponent(aid)}/credentials`, { method: "GET" });
  const acc = data.account && typeof data.account === "object" ? data.account : null;
  if (!acc) throw new Error("后端未返回账号凭据");
  const email = String(acc.email || "").trim();
  const password = String(acc.password || "").trim();
  if (!email || !password) throw new Error("账号或密码为空");
  return acc;
}

async function fetchOtpByAccount(settings, accountId) {
  const aid = String(accountId || "").trim();
  if (!aid) throw new Error("accountId 为空");
  const timeout = Math.max(15, Math.min(300, parseIntSafe(settings.otpTimeoutSeconds, 120)));
  const data = await callGoTeamAPI(settings, `/v1/accounts/${encodeURIComponent(aid)}/otp-fetch`, {
    method: "POST",
    body: { timeout_seconds: timeout },
  });
  const code = String(data.otp_code || "").trim();
  if (!code) throw new Error("后端未返回 otp_code");
  return code;
}

async function markSubscribed(settings, email, teamSubscribed, extra = {}) {
  const em = String(email || "").trim();
  if (!em) return;
  const subscribed = !!teamSubscribed;
  if (subscribed) {
    await callGoTeamAPI(settings, "/v1/accounts/subscription-success", {
      method: "POST",
      body: {
        email: em,
        account_id: String(extra.accountId || "").trim(),
        access_token: String(extra.accessToken || "").trim(),
        source: "lite_login_extension_success_team",
      },
    });
    return;
  }
  await callGoTeamAPI(settings, "/mark-subscribed", {
    method: "POST",
    body: {
      email: em,
      team_subscribed: subscribed,
    },
  });
}

async function stageSubscriptionDirty(payload) {
  const data = await storageGet({ [SUBSCRIPTION_DIRTY_QUEUE_KEY]: [] });
  const prev = Array.isArray(data[SUBSCRIPTION_DIRTY_QUEUE_KEY]) ? data[SUBSCRIPTION_DIRTY_QUEUE_KEY] : [];
  const row = payload && typeof payload === "object" ? payload : {};
  const next = [...prev, row].slice(-100);
  await storageSet({ [SUBSCRIPTION_DIRTY_QUEUE_KEY]: next });
  return next.length;
}

async function resolveStoreIdByTab(tabId) {
  const stores = await getAllCookieStores();
  for (const s of stores || []) {
    const tabs = Array.isArray(s.tabIds) ? s.tabIds : [];
    if (tabs.includes(tabId)) return String(s.id || "");
  }
  return "";
}

async function getAccountIdFromTabCookies(tabId) {
  const storeId = await resolveStoreIdByTab(tabId);
  const details = { url: "https://chatgpt.com/", name: "_account" };
  if (storeId) details.storeId = storeId;
  const ck = await getCookie(details);
  const v = ck && ck.value ? String(ck.value).trim() : "";
  return { accountId: v, storeId };
}

async function rememberAccountMapping(accountId, email) {
  const aid = String(accountId || "").trim();
  const em = String(email || "").trim();
  if (!aid || !em) return;
  const data = await storageGet({ [ACCOUNT_ID_EMAIL_MAP_KEY]: {} });
  const map = (data && typeof data[ACCOUNT_ID_EMAIL_MAP_KEY] === "object" && data[ACCOUNT_ID_EMAIL_MAP_KEY]) || {};
  map[aid] = em;
  await storageSet({ [ACCOUNT_ID_EMAIL_MAP_KEY]: map });
}

async function resolveEmailByAccountId(accountId) {
  const aid = String(accountId || "").trim();
  if (!aid) return "";
  const data = await storageGet({ [ACCOUNT_ID_EMAIL_MAP_KEY]: {}, lastAutoLoginEmail: "" });
  const map = (data && typeof data[ACCOUNT_ID_EMAIL_MAP_KEY] === "object" && data[ACCOUNT_ID_EMAIL_MAP_KEY]) || {};
  const em = String(map[aid] || "").trim();
  if (em) return em;
  return String(data.lastAutoLoginEmail || "").trim();
}

async function fetchAccessTokenFromTab(tabId) {
  try {
    const res = await executeScriptOnTab(
      tabId,
      async () => {
        const keys = ["accessToken", "oai/access_token", "oai/apps/accessToken", "chat.openai.access_token"];
        let fromStorage = "";
        try {
          for (const k of keys) {
            const v = localStorage.getItem(k);
            if (v) {
              fromStorage = String(v);
              break;
            }
          }
          if (!fromStorage) {
            const sv = sessionStorage.getItem("accessToken");
            if (sv) fromStorage = String(sv);
          }
        } catch (_) {
        }
        let fromSessionApi = "";
        try {
          const r = await fetch("/api/auth/session", {
            method: "GET",
            credentials: "include",
            headers: { accept: "application/json" },
          });
          if (r.ok) {
            const j = await r.json().catch(() => null);
            if (j && typeof j === "object") {
              const t = j.accessToken || j.access_token || "";
              if (t) fromSessionApi = String(t);
            }
          }
        } catch (_) {
        }
        const token = fromSessionApi || fromStorage || "";
        return {
          accessToken: token,
          tokenLen: token ? token.length : 0,
          source: fromSessionApi ? "api_auth_session" : (fromStorage ? "storage" : ""),
        };
      },
      [],
    );
    const payload = res && res[0] && res[0].result ? res[0].result : {};
    return {
      accessToken: String(payload.accessToken || ""),
      tokenLen: Number(payload.tokenLen || 0),
      source: String(payload.source || ""),
    };
  } catch (_) {
    return { accessToken: "", tokenLen: 0, source: "" };
  }
}

async function runAutoLoginRound(tabId, email, password) {
  const payload = await executeScriptOnTab(
    tabId,
    (inputEmail, inputPassword) => {
      const norm = (s) => String(s || "").trim().toLowerCase();
      const isVisible = (el) => {
        if (!el) return false;
        const st = window.getComputedStyle(el);
        if (!st || st.display === "none" || st.visibility === "hidden") return false;
        const r = el.getBoundingClientRect();
        return r.width > 0 && r.height > 0;
      };
      const queryFirstVisible = (selectors) => {
        for (const sel of selectors) {
          const nodes = Array.from(document.querySelectorAll(sel));
          for (const el of nodes) {
            if (isVisible(el)) return el;
          }
        }
        return null;
      };
      const setInputValue = (el, value) => {
        if (!el) return false;
        try {
          el.focus();
          el.value = value;
          el.dispatchEvent(new Event("input", { bubbles: true }));
          el.dispatchEvent(new Event("change", { bubbles: true }));
          return true;
        } catch (_) {
          return false;
        }
      };
      const clickFirstBySelectors = (selectors) => {
        const el = queryFirstVisible(selectors);
        if (!el) return "";
        try {
          el.click();
          return String(el.textContent || el.value || selectors[0] || "");
        } catch (_) {
          return "";
        }
      };
      const clickFirstByText = (textList) => {
        const expected = (Array.isArray(textList) ? textList : []).map(norm).filter(Boolean);
        if (!expected.length) return "";
        const nodes = Array.from(document.querySelectorAll("button,a,[role='button'],input[type='submit'],input[type='button']"));
        for (const node of nodes) {
          if (!isVisible(node)) continue;
          const text = norm(node.textContent || node.value || "");
          if (!text) continue;
          if (expected.some((k) => text.includes(k))) {
            try {
              node.click();
              return text;
            } catch (_) {
            }
          }
        }
        return "";
      };

      const actions = [];
      const url = String(location.href || "");
      const bodyText = norm(document.body ? document.body.innerText : "");
      const hasCaptchaIframe = !!document.querySelector("iframe[src*='hcaptcha'], iframe[title*='captcha'], .h-captcha");
      if (hasCaptchaIframe || bodyText.includes("hcaptcha") || bodyText.includes("captcha")) {
        return { state: "captcha_required", url, actions, has_email_input: false, has_password_input: false, has_otp_input: false };
      }

      const otpInput = queryFirstVisible([
        "input[autocomplete='one-time-code']",
        "input[name='code']",
        "input[id='code']",
        "input[inputmode='numeric']",
      ]);
      if (url.includes("email-verification") || bodyText.includes("verification code") || bodyText.includes("one-time code") || bodyText.includes("验证码") || otpInput) {
        return { state: "otp_required", url, actions, has_email_input: false, has_password_input: false, has_otp_input: !!otpInput };
      }

      const loggedIn = url.startsWith("https://chatgpt.com/") && !url.includes("/auth/") && !url.includes("/login");
      if (loggedIn) {
        return { state: "logged_in", url, actions, has_email_input: false, has_password_input: false, has_otp_input: false };
      }

      const continueWithEmailClicked = clickFirstByText(["continue with email", "使用邮箱继续"]);
      if (continueWithEmailClicked) actions.push(`click:${continueWithEmailClicked}`);

      const emailInput = queryFirstVisible([
        "input[type='email']",
        "input[name='email']",
        "input[id='email']",
        "input[name='username']",
        "input[id='username']",
        "input[autocomplete='email']",
        "input[autocomplete='username']",
      ]);
      const passwordInput = queryFirstVisible([
        "input[type='password']",
        "input[name='password']",
        "input[id='password']",
        "input[autocomplete='current-password']",
      ]);

      if (emailInput && setInputValue(emailInput, inputEmail)) actions.push("fill:email");
      if (passwordInput && setInputValue(passwordInput, inputPassword)) actions.push("fill:password");

      const clickedBySelector = clickFirstBySelectors([
        "button[type='submit']",
        "input[type='submit']",
        "button[data-testid='continue-button']",
      ]);
      if (clickedBySelector) {
        actions.push(`click:${norm(clickedBySelector).slice(0, 32)}`);
      } else {
        const clickedByText = clickFirstByText(["继续", "continue", "next", "下一步", "log in", "login", "sign in", "登录"]);
        if (clickedByText) actions.push(`click:${clickedByText.slice(0, 32)}`);
      }

      return {
        state: actions.length > 0 ? "acted" : "idle",
        url,
        actions,
        has_email_input: !!emailInput,
        has_password_input: !!passwordInput,
        has_otp_input: false,
      };
    },
    [email, password],
  );
  return (payload && payload[0] && payload[0].result) || { state: "idle", actions: [], url: "" };
}

async function fillOtpInPage(tabId, otpCode) {
  const payloads = await executeScriptAllFrames(
    tabId,
    (code) => {
      const digits = String(code || "").replace(/\D/g, "");
      if (digits.length < 6) return { ok: false, reason: "otp_invalid" };
      const norm = (s) => String(s || "").toLowerCase();
      const isVisible = (el) => {
        if (!el) return false;
        const st = window.getComputedStyle(el);
        if (!st || st.display === "none" || st.visibility === "hidden") return false;
        const r = el.getBoundingClientRect();
        return r.width > 0 && r.height > 0;
      };
      const allRoots = [];
      const collectRoots = (root) => {
        if (!root) return;
        allRoots.push(root);
        let nodes = [];
        try {
          nodes = Array.from(root.querySelectorAll("*"));
        } catch (_) {
          nodes = [];
        }
        for (const n of nodes) {
          if (n && n.shadowRoot) collectRoots(n.shadowRoot);
        }
      };
      collectRoots(document);

      const queryAllAcrossRoots = (selector) => {
        const out = [];
        try {
          for (const root of allRoots) {
            try {
              const rows = Array.from(root.querySelectorAll(selector));
              for (const x of rows) out.push(x);
            } catch (_) {
            }
          }
        } catch (_) {
        }
        return out;
      };

      const setInput = (el, value) => {
        try {
          el.focus();
          el.value = value;
          el.dispatchEvent(new InputEvent("beforeinput", { bubbles: true, inputType: "insertText", data: value }));
          el.dispatchEvent(new Event("input", { bubbles: true }));
          el.dispatchEvent(new Event("change", { bubbles: true }));
          el.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
          el.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, key: "Enter" }));
          return true;
        } catch (_) {
          return false;
        }
      };

      let filled = false;
      let mode = "";

      const oneInputCandidates = queryAllAcrossRoots(
        "input[autocomplete='one-time-code'],input[name='code'],input[id='code'],input[name*='otp'],input[id*='otp'],input[name*='verify'],input[id*='verify'],input[name*='token'],input[id*='token'],input[inputmode='numeric'],input[type='tel'],input[type='text']"
      ).filter((el) => isVisible(el));

      let oneInput = null;
      for (const el of oneInputCandidates) {
        const maxLen = Number.parseInt(String(el.getAttribute("maxlength") || "0"), 10) || 0;
        const nm = norm(el.getAttribute("name") || "");
        const id = norm(el.getAttribute("id") || "");
        const ac = norm(el.getAttribute("autocomplete") || "");
        if (ac.includes("one-time-code") || nm.includes("code") || nm.includes("otp") || id.includes("code") || id.includes("otp") || maxLen >= 6 || maxLen === 0) {
          oneInput = el;
          break;
        }
      }

      if (oneInput && isVisible(oneInput)) {
        const maxLen = Number.parseInt(String(oneInput.getAttribute("maxlength") || "0"), 10) || 0;
        if (maxLen >= 6 || maxLen === 0) {
          filled = setInput(oneInput, digits.slice(0, 6));
          mode = "single";
        }
      }

      if (!filled) {
        const boxes = queryAllAcrossRoots("input[inputmode='numeric'],input[maxlength='1'],input[type='tel'],input[type='text']")
          .filter((el) => isVisible(el));
        const oneCharBoxes = boxes.filter((el) => {
          const maxLen = Number.parseInt(String(el.getAttribute("maxlength") || "0"), 10) || 0;
          return maxLen === 1;
        });
        const target = oneCharBoxes.length >= 6 ? oneCharBoxes : boxes;
        if (target.length >= 6) {
          for (let i = 0; i < 6; i++) {
            setInput(target[i], digits[i] || "");
          }
          filled = true;
          mode = "boxes";
        }
      }
      if (!filled) {
        return {
          ok: false,
          reason: "otp_input_not_found",
          input_candidates: oneInputCandidates.length,
          frame_url: String(location.href || ""),
        };
      }

      const candidates = queryAllAcrossRoots("button,input[type='submit'],a,[role='button']")
        .filter((el) => isVisible(el));
      let clicked = false;
      for (const el of candidates) {
        const text = String(el.textContent || el.value || "").toLowerCase();
        if (text.includes("继续") || text.includes("continue") || text.includes("verify") || text.includes("提交")) {
          try {
            el.click();
            clicked = true;
            break;
          } catch (_) {
          }
        }
      }
      return {
        ok: true,
        clicked,
        mode,
        frame_url: String(location.href || ""),
      };
    },
    [otpCode],
  );

  const results = [];
  for (const item of payloads || []) {
    const r = item && item.result ? item.result : null;
    if (r && typeof r === "object") {
      results.push({
        ...r,
        frameId: Number(item.frameId || 0),
      });
    }
  }
  const success = results.find((x) => x && x.ok);
  if (success) {
    return success;
  }
  const firstFail = results.find((x) => x && x.reason) || null;
  if (firstFail) {
    return firstFail;
  }
  return { ok: false, reason: "script_no_result" };
}

async function autoLoginWithAccount(settings, accountId, sourceWindowId) {
  const account = await getAccountCredentials(settings, accountId);
  const email = String(account.email || "").trim();
  const password = String(account.password || "").trim();
  await storageSet({ lastAutoLoginEmail: email });

  const tab = await getBestActiveTab(sourceWindowId);
  let workTabId = Number(tab.id);
  if (!Number.isInteger(workTabId)) throw new Error("tabId 无效");
  if (isRestrictedBrowserUrl(tab.url || "")) {
    const fresh = await createTab({
      url: "https://auth.openai.com/log-in",
      active: true,
      windowId: Number.isInteger(sourceWindowId) ? sourceWindowId : undefined,
    });
    workTabId = Number(fresh && fresh.id);
    if (!Number.isInteger(workTabId)) throw new Error("无法创建可自动化标签页");
  } else {
    await updateTab(workTabId, { url: "https://auth.openai.com/log-in", active: true });
  }
  await sleep(1800);

  const rounds = [];
  const autoFetchOtp = parseBool(settings.autoFetchOtp);
  let hadCredentialInteraction = false;
  let authRedirectRecovered = false;
  let otpFillFailedCount = 0;

  for (let i = 0; i < 42; i++) {
    let round;
    try {
      round = await runAutoLoginRound(workTabId, email, password);
    } catch (e) {
      const msg = String((e && e.message) || e || "");
      if (msg.includes("Cannot access chrome://") || msg.includes("Cannot access edge://")) {
        const fresh = await createTab({
          url: "https://auth.openai.com/log-in",
          active: true,
          windowId: Number.isInteger(sourceWindowId) ? sourceWindowId : undefined,
        });
        workTabId = Number(fresh && fresh.id);
        await sleep(1800);
        round = await runAutoLoginRound(workTabId, email, password);
      } else {
        throw e;
      }
    }
    rounds.push(round);

    if (Array.isArray(round.actions) && round.actions.some((x) => String(x).includes("fill:"))) {
      hadCredentialInteraction = true;
    }
    if (round.has_password_input) hadCredentialInteraction = true;

    if (round.state === "logged_in") {
      const m = await getAccountIdFromTabCookies(workTabId);
      if (m.accountId) {
        await rememberAccountMapping(m.accountId, email);
      }
      return {
        ok: true,
        status: "logged_in",
        email,
        tab_id: workTabId,
        account_id: m.accountId || "",
        rounds,
      };
    }

    if (round.state === "captcha_required") {
      return {
        ok: true,
          status: "manual_required",
          manual_stage: "captcha_required",
          email,
          tab_id: workTabId,
          rounds,
        };
    }

    if (round.state === "otp_required") {
      if (!autoFetchOtp) {
        return {
          ok: true,
          status: "manual_required",
          manual_stage: "otp_required",
          email,
          tab_id: workTabId,
          rounds,
        };
      }
      let otpCode = "";
      try {
        otpCode = await fetchOtpByAccount(settings, accountId);
      } catch (e) {
        rounds.push({ state: "otp_fetch_failed", error: String((e && e.message) || e || "") });
        return {
          ok: true,
          status: "manual_required",
          manual_stage: "otp_fetch_failed",
          manual_error: String((e && e.message) || e || ""),
          email,
          tab_id: workTabId,
          rounds,
        };
      }
      const fill = await fillOtpInPage(workTabId, otpCode);
      rounds.push({ state: "otp_filled", otp_len: String(otpCode).length, fill_ok: !!fill.ok, fill_reason: fill.reason || "", clicked: !!fill.clicked });
      if (!fill.ok) {
        // 关键容错：某些情况下 OTP 已被页面接收并开始跳转，
        // 注入脚本执行点已拿不到输入框，此时不应立刻判失败。
        if (String(fill.reason || "") === "otp_input_not_found") {
          rounds.push({ state: "otp_fill_probe", reason: "otp_input_not_found_skip_fail" });
          await sleep(1800);
          continue;
        }
        otpFillFailedCount += 1;
        if (otpFillFailedCount >= 3) {
          return {
            ok: true,
            status: "manual_required",
            manual_stage: "otp_fill_failed",
            manual_error: String(fill.reason || "otp_fill_failed"),
            email,
            tab_id: workTabId,
            rounds,
          };
        }
      } else {
        otpFillFailedCount = 0;
      }
      await sleep(1800);
      continue;
    }

    const u = String(round.url || "");
    if (
      hadCredentialInteraction &&
      !authRedirectRecovered &&
      u.startsWith("https://auth.openai.com/log-in") &&
      !round.has_email_input &&
      !round.has_password_input
    ) {
      authRedirectRecovered = true;
      await updateTab(workTabId, { url: "https://chatgpt.com/", active: true });
      await sleep(2000);
      continue;
    }

    await sleep(900);
  }

  return {
    ok: true,
    status: "pending",
    email,
    tab_id: workTabId,
    rounds,
  };
}

async function handleSuccessTeamPage(tabId, rawUrl) {
  const u = String(rawUrl || "").trim();
  if (!u || HANDLED_SUCCESS_URLS.has(u)) return;
  HANDLED_SUCCESS_URLS.add(u);
  if (HANDLED_SUCCESS_URLS.size > 200) {
    const first = HANDLED_SUCCESS_URLS.values().next();
    if (!first.done) HANDLED_SUCCESS_URLS.delete(first.value);
  }

  let accountId = "";
  try {
    const urlObj = new URL(u);
    accountId = String(urlObj.searchParams.get("account_id") || "").trim();
  } catch (_) {
  }

  const conf = await storageGet({ apiBase: "http://127.0.0.1:18081", apiKey: "", lastAutoLoginEmail: "" });
  let email = await resolveEmailByAccountId(accountId);
  if (!email) email = String(conf.lastAutoLoginEmail || "").trim();

  let tk = { accessToken: "", tokenLen: 0, source: "" };
  try {
    tk = await fetchAccessTokenFromTab(tabId);
  } catch (_) {
  }
  // 禁止自动回写后端：仅暂存脏数据，后续由人工决定是否提交。
  const dirtyCount = await stageSubscriptionDirty({
    at: new Date().toISOString(),
    source: "lite_login_extension_success_team",
    pageUrl: u,
    email,
    accountId,
    accessToken: tk.accessToken || "",
    accessTokenLen: tk.tokenLen || 0,
    accessTokenSource: tk.source || "",
    pendingManualPost: true,
  });
  const state = {
    at: new Date().toISOString(),
    pageUrl: u,
    accountId,
    email,
    markedSubscribed: false,
    markError: "auto_post_disabled_staged_dirty",
    dirtyStaged: true,
    dirtyCount,
    accessToken: tk.accessToken || "",
    accessTokenLen: tk.tokenLen || 0,
    accessTokenSource: tk.source || "",
  };
  await storageSet({ [LAST_SUCCESS_KEY]: state });
  await sendRuntimeMessage({ type: "SUCCESS_STATE_UPDATED", data: state });
}

async function resetSuccessState() {
  HANDLED_SUCCESS_URLS.clear();
  await storageSet({ [LAST_SUCCESS_KEY]: null });
  await sendRuntimeMessage({ type: "SUCCESS_STATE_UPDATED", data: null });
}

chrome.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
  const u = String(changeInfo.url || tab?.url || "").trim();
  if (!u) return;
  if (u.includes("/payments/success-team")) {
    handleSuccessTeamPage(tabId, u).catch(() => {});
  }
});

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (!message || !message.type) {
    sendResponse({ ok: false, error: "invalid message" });
    return true;
  }

  if (message.type === "LOAD_ACCOUNTS") {
    loadAccounts(message.settings || {})
      .then((data) => sendResponse({ ok: true, data }))
      .catch((err) => sendResponse({ ok: false, error: err.message || String(err) }));
    return true;
  }

  if (message.type === "GET_ACCOUNT_CREDENTIALS") {
    getAccountCredentials(message.settings || {}, message.accountId)
      .then((account) => sendResponse({ ok: true, data: { account } }))
      .catch((err) => sendResponse({ ok: false, error: err.message || String(err) }));
    return true;
  }

  if (message.type === "AUTO_LOGIN_ACCOUNT") {
    autoLoginWithAccount(message.settings || {}, message.accountId, message.sourceWindowId)
      .then((data) => sendResponse({ ok: true, data }))
      .catch((err) => sendResponse({ ok: false, error: err.message || String(err) }));
    return true;
  }

  if (message.type === "RESET_SUCCESS_STATE") {
    resetSuccessState()
      .then(() => sendResponse({ ok: true }))
      .catch((err) => sendResponse({ ok: false, error: err.message || String(err) }));
    return true;
  }

  sendResponse({ ok: false, error: "unsupported message type" });
  return true;
});
