const DEFAULTS = {
  apiBase: "http://127.0.0.1:18081",
  apiKey: "",
  autoFetchOtp: "true",
  otpTimeoutSeconds: "120",
  accountType: "",
  onlyInvitePool: "false",
  selectedAccountId: "",
  lastSuccessState: null,
};

const els = {
  apiBase: document.getElementById("apiBase"),
  apiKey: document.getElementById("apiKey"),
  autoFetchOtp: document.getElementById("autoFetchOtp"),
  otpTimeoutSeconds: document.getElementById("otpTimeoutSeconds"),
  accountType: document.getElementById("accountType"),
  onlyInvitePool: document.getElementById("onlyInvitePool"),
  accountSelect: document.getElementById("accountSelect"),
  loadAccountsBtn: document.getElementById("loadAccountsBtn"),
  loadCredBtn: document.getElementById("loadCredBtn"),
  saveBtn: document.getElementById("saveBtn"),
  autoLoginBtn: document.getElementById("autoLoginBtn"),
  credEmail: document.getElementById("credEmail"),
  credPassword: document.getElementById("credPassword"),
  lastSuccessInfo: document.getElementById("lastSuccessInfo"),
  accessTokenOut: document.getElementById("accessTokenOut"),
  copyAccessTokenBtn: document.getElementById("copyAccessTokenBtn"),
  copyEmailBtn: document.getElementById("copyEmailBtn"),
  copyPasswordBtn: document.getElementById("copyPasswordBtn"),
  log: document.getElementById("log"),
};

let loadedAccounts = [];
let currentCredentials = null;

function log(msg) {
  const t = new Date().toLocaleTimeString();
  els.log.textContent = `[${t}] ${msg}\n` + els.log.textContent;
}

function storageGet(keys) {
  return new Promise((resolve) => chrome.storage.local.get(keys, resolve));
}

function storageSet(data) {
  return new Promise((resolve) => chrome.storage.local.set(data, resolve));
}

function getSettingsFromForm() {
  return {
    apiBase: String(els.apiBase.value || "").trim(),
    apiKey: String(els.apiKey.value || "").trim(),
    autoFetchOtp: String(els.autoFetchOtp.value || "true").trim().toLowerCase(),
    otpTimeoutSeconds: String(els.otpTimeoutSeconds.value || "120").trim(),
    accountType: String(els.accountType.value || "").trim(),
    onlyInvitePool: String(els.onlyInvitePool.value || "false").trim().toLowerCase(),
    selectedAccountId: String(els.accountSelect.value || "").trim(),
  };
}

function renderSuccessState(state) {
  const s = state && typeof state === "object" ? state : null;
  if (!s) {
    els.lastSuccessInfo.value = "尚未检测到 success-team 页面";
    els.accessTokenOut.value = "";
    return;
  }
  const line = [
    String(s.email || "").trim(),
    String(s.accountId || "").trim() ? `aid=${String(s.accountId || "").trim()}` : "",
    String(s.at || "").trim(),
    s.dirtyStaged ? "已暂存脏数据" : (s.markedSubscribed ? "已自动标记订阅" : "未标记"),
  ].filter(Boolean).join(" | ");
  els.lastSuccessInfo.value = line || "success-team detected";
  els.accessTokenOut.value = String(s.accessToken || "");
}

async function saveSettings() {
  const settings = getSettingsFromForm();
  await storageSet(settings);
  log("配置已保存");
  return settings;
}

async function loadSettings() {
  const data = await storageGet(DEFAULTS);
  els.apiBase.value = data.apiBase || DEFAULTS.apiBase;
  els.apiKey.value = data.apiKey || "";
  els.autoFetchOtp.value = data.autoFetchOtp || "true";
  els.otpTimeoutSeconds.value = String(data.otpTimeoutSeconds || "120");
  els.accountType.value = data.accountType || "";
  els.onlyInvitePool.value = data.onlyInvitePool || "false";
  renderAccounts([], String(data.selectedAccountId || ""));
  renderSuccessState(data.lastSuccessState || null);
}

function renderAccounts(accounts, selectedAccountId = "") {
  loadedAccounts = Array.isArray(accounts) ? accounts : [];
  els.accountSelect.innerHTML = "";
  const first = document.createElement("option");
  first.value = "";
  first.textContent = loadedAccounts.length ? "(请选择账号)" : "(无可用账号)";
  els.accountSelect.appendChild(first);

  for (const acc of loadedAccounts) {
    const id = String(acc.id || "").trim();
    const email = String(acc.email || "").trim();
    if (!id || !email) continue;
    const team = acc.team_subscribed ? "✅" : "⬜";
    const t = String(acc.account_type || "-");
    const tags = Array.isArray(acc.tags) && acc.tags.length ? ` [${acc.tags.join(",")}]` : "";
    const opt = document.createElement("option");
    opt.value = id;
    opt.textContent = `${team} ${email} (${t})${tags}`;
    els.accountSelect.appendChild(opt);
  }
  if (selectedAccountId) {
    els.accountSelect.value = selectedAccountId;
  }
}

function getCurrentWindowId() {
  return new Promise((resolve) => {
    chrome.windows.getCurrent({}, (win) => {
      const err = chrome.runtime.lastError;
      if (err || !win || typeof win.id !== "number") return resolve(undefined);
      resolve(win.id);
    });
  });
}

async function loadAccounts() {
  els.loadAccountsBtn.disabled = true;
  try {
    const settings = await saveSettings();
    const resp = await chrome.runtime.sendMessage({ type: "LOAD_ACCOUNTS", settings });
    if (!resp || !resp.ok) throw new Error((resp && resp.error) || "load accounts failed");
    const rows = Array.isArray(resp.data && resp.data.accounts) ? resp.data.accounts : [];
    renderAccounts(rows, settings.selectedAccountId || "");
    log(`账号加载完成: ${rows.length} 条`);
  } catch (e) {
    log(`加载账号失败: ${e.message || String(e)}`);
  } finally {
    els.loadAccountsBtn.disabled = false;
  }
}

async function loadCredentials() {
  els.loadCredBtn.disabled = true;
  try {
    const settings = await saveSettings();
    const accountId = String(settings.selectedAccountId || "").trim();
    if (!accountId) throw new Error("请先选择账号");
    const resp = await chrome.runtime.sendMessage({
      type: "GET_ACCOUNT_CREDENTIALS",
      settings,
      accountId,
    });
    if (!resp || !resp.ok) throw new Error((resp && resp.error) || "load credentials failed");
    const account = resp.data && resp.data.account ? resp.data.account : null;
    if (!account) throw new Error("后端未返回账号数据");
    currentCredentials = account;
    els.credEmail.value = String(account.email || "");
    els.credPassword.value = String(account.password || "");
    log(`凭据已加载: ${String(account.email || "")}`);
  } catch (e) {
    currentCredentials = null;
    els.credEmail.value = "";
    els.credPassword.value = "";
    log(`加载凭据失败: ${e.message || String(e)}`);
  } finally {
    els.loadCredBtn.disabled = false;
  }
}

async function autoLogin() {
  els.autoLoginBtn.disabled = true;
  try {
    const settings = await saveSettings();
    const accountId = String(settings.selectedAccountId || "").trim();
    if (!accountId) throw new Error("请先选择账号");
    try {
      await chrome.runtime.sendMessage({ type: "RESET_SUCCESS_STATE" });
    } catch (_) {
    }
    log("开始自动登录...");
    const sourceWindowId = await getCurrentWindowId();
    const resp = await chrome.runtime.sendMessage({
      type: "AUTO_LOGIN_ACCOUNT",
      settings,
      accountId,
      sourceWindowId,
    });
    if (!resp || !resp.ok) throw new Error((resp && resp.error) || "auto login failed");
    const d = resp.data || {};
    const status = String(d.status || "unknown");
    const rounds = Array.isArray(d.rounds) ? d.rounds : [];
    const last = rounds.length ? rounds[rounds.length - 1] : {};
    log(`自动登录结束: status=${status} rounds=${rounds.length} url=${String(last.url || "")}`);
    if (status === "manual_required") {
      log(`请人工处理: ${String(d.manual_stage || "")}`);
      if (d.manual_error) {
        log(`失败原因: ${String(d.manual_error)}`);
      } else if (last && last.error) {
        log(`失败原因: ${String(last.error)}`);
      } else if (last && last.fill_reason) {
        log(`失败原因: ${String(last.fill_reason)}`);
      }
    }
    if (status === "logged_in") {
      log("登录完成。");
    }
    if (status === "otp_filled") {
      log("OTP 已自动填充，继续等待登录完成...");
    }
  } catch (e) {
    log(`自动登录失败: ${e.message || String(e)}`);
  } finally {
    els.autoLoginBtn.disabled = false;
  }
}

async function copyText(text, okMsg) {
  const v = String(text || "").trim();
  if (!v) {
    log("复制失败: 内容为空");
    return;
  }
  try {
    await navigator.clipboard.writeText(v);
    log(okMsg);
  } catch (e) {
    log(`复制失败: ${e.message || String(e)}`);
  }
}

els.saveBtn.addEventListener("click", () => {
  saveSettings();
});

els.loadAccountsBtn.addEventListener("click", () => {
  loadAccounts();
});

els.loadCredBtn.addEventListener("click", () => {
  loadCredentials();
});

els.autoLoginBtn.addEventListener("click", () => {
  autoLogin();
});

els.copyEmailBtn.addEventListener("click", () => {
  copyText(els.credEmail.value || "", "已复制账号");
});

els.copyPasswordBtn.addEventListener("click", () => {
  copyText(els.credPassword.value || "", "已复制密码");
});
els.copyAccessTokenBtn.addEventListener("click", () => {
  copyText(els.accessTokenOut.value || "", "已复制 Access Token");
});

els.accountSelect.addEventListener("change", async () => {
  await saveSettings();
  await loadCredentials();
});

els.accountType.addEventListener("change", () => {
  saveSettings();
});

els.onlyInvitePool.addEventListener("change", () => {
  saveSettings();
});
els.autoFetchOtp.addEventListener("change", () => {
  saveSettings();
});
els.otpTimeoutSeconds.addEventListener("change", () => {
  saveSettings();
});

chrome.runtime.onMessage.addListener((message) => {
  if (!message || message.type !== "SUCCESS_STATE_UPDATED") return;
  renderSuccessState(message.data || null);
  const s = message.data || {};
  if (!s || (!s.email && !s.accountId && !s.accessTokenLen)) return;
  const staged = s.dirtyStaged ? `dirty_staged=true count=${s.dirtyCount || 0}` : "";
  log(`检测到 success-team: email=${s.email || ""} marked=${s.markedSubscribed ? "true" : "false"} token_len=${s.accessTokenLen || 0} ${staged}`.trim());
});

loadSettings();
