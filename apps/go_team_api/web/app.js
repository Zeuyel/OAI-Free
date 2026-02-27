const apiKeyInput = document.getElementById("apiKey");
const toastEl = document.getElementById("toast");
const tabs = [...document.querySelectorAll(".tab")];
const panels = [...document.querySelectorAll(".panel")];

const accState = { raw: [], filtered: [], page: 1, perPage: 20 };
const teamState = { rows: [], page: 1, perPage: 10, total: 0, totalPages: 1 };
let currentTeamId = "";
let currentOwnerCheckTeamId = "";
let deletingAccountID = "";
const otpMailState = { accountID: "", rows: [], activeID: "" };
const DEFAULT_ACCOUNT_TXT_PATH = "../../legacy/extension_backend/accounts.txt";
const DEFAULT_OWNER_STATUS_PATH = "../../legacy/extension_backend/account_status.json";

const STATUS_STYLE = {
  active: "background:#dcfce7;color:#166534;",
  full: "background:#fef9c3;color:#854d0e;",
  expired: "background:#ffedd5;color:#9a3412;",
  error: "background:#fee2e2;color:#991b1b;",
  banned: "background:#e5e7eb;color:#1f2937;",
};

function esc(v) {
  return String(v ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

function showToast(msg, ok = true) {
  toastEl.textContent = msg;
  toastEl.style.background = ok ? "#0f766e" : "#991b1b";
  toastEl.classList.remove("hidden");
  setTimeout(() => toastEl.classList.add("hidden"), 2200);
}

function openModal(id) {
  document.getElementById(id).classList.remove("hidden");
}

function closeModal(id) {
  document.getElementById(id).classList.add("hidden");
  if (id === "deleteAccountModal") {
    deletingAccountID = "";
    document.getElementById("deleteAccountMeta").textContent = "-";
    document.getElementById("deleteAccountConfirmInput").value = "";
    document.getElementById("confirmDeleteAccountBtn").disabled = true;
  }
}

function setLoading(btn, loading, text = "处理中...") {
  if (!btn) return;
  if (loading) {
    btn.dataset.oldText = btn.textContent;
    btn.textContent = text;
    btn.disabled = true;
  } else {
    btn.textContent = btn.dataset.oldText || btn.textContent;
    btn.disabled = false;
  }
}

function writeOtpLog(message) {
  const el = document.getElementById("otpFetchLog");
  if (!el) return;
  el.textContent = String(message || "");
}

function renderOTPFrame(mail) {
  const frame = document.getElementById("otpMailFrame");
  const metaEl = document.getElementById("otpMailMeta");
  if (!frame || !metaEl) return;
  if (!mail) {
    metaEl.textContent = "未选择邮件";
    frame.srcdoc = "<!doctype html><html><body style='font-family:Segoe UI,system-ui;padding:16px;color:#5f6f8b;'>请选择左侧邮件</body></html>";
    return;
  }
  metaEl.textContent = [
    `Subject: ${mail.subject || "-"}`,
    `From: ${mail.from || "-"}`,
    `To: ${mail.to || "-"}`,
    `OTP: ${mail.otp_code || "-"}`,
    `Created: ${mail.created_at || "-"}`,
  ].join(" | ");
  if (String(mail.html || "").trim()) {
    frame.srcdoc = String(mail.html);
    return;
  }
  const txt = esc(mail.text || mail.preview || "(empty)").replaceAll("\n", "<br>");
  frame.srcdoc = `<!doctype html><html><body style="font-family:Segoe UI,system-ui;padding:16px;line-height:1.5;color:#1f3554;">${txt}</body></html>`;
}

function renderOTPMailList() {
  const el = document.getElementById("otpMailList");
  if (!el) return;
  if (!otpMailState.rows.length) {
    el.innerHTML = `<div class="otp-mail-item"><div class="otp-mail-preview">暂无邮件，请先点击“拉取邮件”</div></div>`;
    renderOTPFrame(null);
    return;
  }
  el.innerHTML = otpMailState.rows.map((m) => `
    <div class="otp-mail-item ${m.id === otpMailState.activeID ? "active" : ""}" data-id="${esc(m.id)}">
      <div class="otp-mail-subject">${esc(m.subject || "(no subject)")}</div>
      <div class="otp-mail-preview">${esc(m.preview || m.text || "-")}</div>
      <div class="otp-mail-time">${esc(m.created_at || "-")}${m.otp_code ? ` · OTP ${esc(m.otp_code)}` : ""}</div>
    </div>
  `).join("");
  el.querySelectorAll(".otp-mail-item[data-id]").forEach((node) => {
    node.addEventListener("click", () => {
      otpMailState.activeID = node.dataset.id;
      renderOTPMailList();
      const mail = otpMailState.rows.find((x) => x.id === otpMailState.activeID);
      renderOTPFrame(mail || null);
    });
  });
  const active = otpMailState.rows.find((x) => x.id === otpMailState.activeID) || otpMailState.rows[0];
  otpMailState.activeID = active.id;
  renderOTPFrame(active);
}

function resetOTPMailViewer() {
  otpMailState.accountID = "";
  otpMailState.rows = [];
  otpMailState.activeID = "";
  writeOtpLog("等待操作...");
  renderOTPMailList();
}

async function loadAccountMails(accountID) {
  const target = String(accountID || "").trim();
  if (!target) throw new Error("account_id 不能为空");
  const out = await api(`/v1/accounts/${encodeURIComponent(target)}/mail-list?limit=30`);
  otpMailState.accountID = target;
  otpMailState.rows = Array.isArray(out.mails) ? out.mails : [];
  otpMailState.activeID = otpMailState.rows[0]?.id || "";
  writeOtpLog(`已拉取 ${otpMailState.rows.length} 封邮件`);
  renderOTPMailList();
  return out;
}

async function fetchOTPByAccount(accountID, timeoutSeconds) {
  const target = String(accountID || "").trim();
  if (!target) throw new Error("account_id 不能为空");
  const timeout = Number(timeoutSeconds || 0);
  const body = {};
  if (Number.isFinite(timeout) && timeout > 0) {
    body.timeout_seconds = Math.max(15, Math.min(300, Math.round(timeout)));
  }
  const out = await api(`/v1/accounts/${encodeURIComponent(target)}/otp-fetch`, {
    method: "POST",
    body: JSON.stringify(body),
  });
  const otp = String(out.otp_code || "").trim();
  if (otp) writeOtpLog(`取件成功，OTP: ${otp}`);
  else writeOtpLog("取件完成（未提取到 otp_code）");
  return out;
}

function openDeleteAccountModal(account) {
  deletingAccountID = String(account.id || "").trim();
  const meta = [
    `账号: ${account.email || "-"}`,
    `ID: ${account.id || "-"}`,
    `类型: ${account.account_type || "-"}`,
    `token: ${account.token_alive ? "alive" : "dead"}`,
  ].join(" | ");
  document.getElementById("deleteAccountMeta").textContent = meta;
  document.getElementById("deleteAccountConfirmInput").value = "";
  document.getElementById("confirmDeleteAccountBtn").disabled = true;
  openModal("deleteAccountModal");
}

function headers(needJSON = true) {
  const h = {};
  if (needJSON) h["Content-Type"] = "application/json";
  const key = apiKeyInput.value.trim();
  if (key) h["X-Api-Key"] = key;
  return h;
}

async function api(path, options = {}) {
  const init = { ...options };
  const hasCT = init.headers && Object.keys(init.headers).some((k) => k.toLowerCase() === "content-type");
  init.headers = { ...headers(!hasCT), ...(init.headers || {}) };
  const res = await fetch(path, init);
  const raw = await res.text();
  let data = {};
  try { data = raw ? JSON.parse(raw) : {}; } catch (_e) { data = { ok: false, error: raw }; }
  if (!res.ok || data.ok === false) throw new Error(data.error || `HTTP ${res.status}`);
  return data;
}

function pick(list, page, perPage) {
  return list.slice((page - 1) * perPage, (page - 1) * perPage + perPage);
}

function isFedAccount(account) {
  const st = String(account?.status || "").toLowerCase();
  const cpa = String(account?.cpa_filename || "").trim();
  return st.includes("fed") || cpa !== "";
}

function renderPager(containerId, page, totalPages, onPage) {
  const el = document.getElementById(containerId);
  const html = [];
  const btn = (label, p, disabled, active, title = "", cls = "") => `
    <button class="num ${cls} ${active ? "active" : ""}" data-p="${p}" ${disabled ? "disabled" : ""} title="${title}">${label}</button>
  `;
  html.push(btn("首页", 1, page <= 1, false, "首页", "nav"));
  html.push(btn("上一页", Math.max(1, page - 1), page <= 1, false, "上一页", "nav"));

  const pages = [];
  pages.push(1);
  for (let p = Math.max(2, page - 1); p <= Math.min(totalPages - 1, page + 1); p++) pages.push(p);
  if (totalPages > 1) pages.push(totalPages);

  let last = 0;
  for (const p of [...new Set(pages)]) {
    if (p - last > 1) html.push(`<span class="num gap">…</span>`);
    html.push(btn(String(p), p, false, p === page, `第 ${p} 页`));
    last = p;
  }

  html.push(btn("下一页", Math.min(totalPages, page + 1), page >= totalPages, false, "下一页", "nav"));
  html.push(btn("末页", totalPages, page >= totalPages, false, "末页", "nav"));
  el.innerHTML = html.join("");
  el.querySelectorAll("button[data-p]").forEach((b) => b.addEventListener("click", () => {
    if (b.disabled) return;
    onPage(Number(b.dataset.p));
  }));
}

async function loadAccounts() {
  const out = await api("/v1/accounts?limit=2000");
  accState.raw = Array.isArray(out.accounts) ? out.accounts : [];
  document.getElementById("stTotal").textContent = String(accState.raw.length);
  document.getElementById("stTeam").textContent = String(accState.raw.filter((x) => x.account_type === "team").length);
  document.getElementById("stAlive").textContent = String(accState.raw.filter((x) => x.token_alive).length);
  const fedEl = document.getElementById("stFed");
  if (fedEl) fedEl.textContent = String(accState.raw.filter((x) => isFedAccount(x)).length);
  applyAccountFilters();
}

function applyAccountFilters() {
  const q = document.getElementById("accSearch").value.trim().toLowerCase();
  const type = document.getElementById("accTypeFilter").value;
  const token = document.getElementById("accTokenFilter").value;
  accState.filtered = accState.raw.filter((a) => {
    if (type !== "all" && a.account_type !== type) return false;
    if (token === "alive" && !a.token_alive) return false;
    if (token === "dead" && a.token_alive) return false;
    if (!q) return true;
    const text = `${a.id} ${a.email} ${(a.tags || []).join(",")}`.toLowerCase();
    return text.includes(q);
  });
  renderAccounts();
}

function renderAccounts() {
  const tbody = document.getElementById("accountsTbody");
  const total = accState.filtered.length;
  const pages = Math.max(1, Math.ceil(total / accState.perPage));
  if (accState.page > pages) accState.page = pages;
  const rows = pick(accState.filtered, accState.page, accState.perPage);
  tbody.innerHTML = rows.map((a) => {
    const typeStyle = a.account_type === "team" ? "background:#eef2ff;color:#4338ca;" : "background:#e0f2fe;color:#0369a1;";
    const tokenStyle = a.token_alive ? "background:#dcfce7;color:#166534;" : "background:#fee2e2;color:#991b1b;";
    const fed = isFedAccount(a);
    const statusStyle = fed ? "background:#dcfce7;color:#166534;" : "background:#e5e7eb;color:#334155;";
    const statusText = fed ? "fed" : "not-fed";
    const cpaAction = fed ? "down" : "up";
    const cpaText = fed ? "CPA下号" : "CPA上号";
    return `
      <tr>
        <td class="account-main"><b>${esc(a.email)}</b><br><small style="color:#64748b;">${esc(a.id)}</small><br><small>tags: ${esc((a.tags || []).join(",")) || "-"}</small><br><small>cpa: ${esc(a.cpa_filename || "-")}</small><br><small>${esc(a.updated_at)}</small></td>
        <td><span class="badge" style="${typeStyle}">${esc(a.account_type)}</span></td>
        <td><span class="badge" style="${tokenStyle}">${a.token_alive ? "alive" : "dead"}</span></td>
        <td><span class="badge" style="${statusStyle}">${statusText}</span><br><small class="muted">raw: ${esc(a.status || "-")}</small>${a.error ? `<br><small style="color:#991b1b;">${esc(a.error)}</small>` : ""}</td>
        <td>${a.team_subscribed ? "yes" : "no"}</td>
        <td>${esc(a.created_at)}</td>
        <td>
          <div class="action-row">
            <button class="${fed ? "" : "primary"} js-cpa-toggle" data-id="${esc(a.id)}" data-action="${cpaAction}">${cpaText}</button>
            <button class="js-token-check" data-id="${esc(a.id)}">token-check</button>
            <button class="danger js-account-delete" data-id="${esc(a.id)}">删除账号</button>
          </div>
        </td>
      </tr>
    `;
  }).join("");
  if (!rows.length) tbody.innerHTML = `<tr><td colspan="7" style="text-align:center;color:#64748b;">暂无数据</td></tr>`;
  document.getElementById("accPageInfo").textContent = `共 ${total} 条 · 每页 ${accState.perPage} 条 · 第 ${accState.page}/${pages} 页`;
  renderPager("accPageNums", accState.page, pages, (next) => {
    accState.page = next;
    renderAccounts();
  });
}

async function loadTeams(page = 1) {
  teamState.page = page;
  const search = document.getElementById("teamSearch").value.trim();
  const out = await api(`/v1/teams?page=${teamState.page}&per_page=${teamState.perPage}&search=${encodeURIComponent(search)}`);
  teamState.rows = out.teams || [];
  const p = out.pagination || {};
  teamState.total = p.total || 0;
  teamState.totalPages = p.total_pages || 1;
  renderTeams();
}

function renderTeams() {
  const tbody = document.getElementById("teamsTbody");
  const statusText = (v) => ({
    active: "可用",
    full: "已满",
    expired: "已过期",
    error: "异常",
    banned: "已封禁",
  }[String(v || "").toLowerCase()] || (v || "-"));
  tbody.innerHTML = teamState.rows.map((t) => `
    <tr>
      <td><span class="mono muted">${esc(t.id)}</span></td>
      <td>
        <div class="cell-main">${esc(t.email || t.owner_email || "-")}</div>
        ${t.account_role && t.account_role !== "account-owner" ? `<small class="muted">角色: ${esc(t.account_role)}</small>` : ""}
      </td>
      <td><span class="mono muted">${esc(t.account_id || "-")}</span></td>
      <td>${esc(t.name || "-")}</td>
      <td>
        <span class="member-count">${Number(t.current_members || 0)}/${Number(t.max_members || 0)}</span>
        <br><small class="muted">joined ${Number(t.joined_count || 0)} / invited ${Number(t.invited_count || 0)}</small>
      </td>
      <td>${esc(t.subscription_plan || "-")}</td>
      <td>${esc(t.expires_at || "-")}</td>
      <td class="team-status-cell"><span class="badge" style="${STATUS_STYLE[t.status] || "background:#e5e7eb;color:#1f2937;"}">${esc(statusText(t.status))}</span></td>
      <td class="row team-actions">
        <button class="js-team-edit" data-id="${esc(t.id)}">编辑</button>
        <button class="js-team-members" data-id="${esc(t.id)}" data-name="${esc(t.name || "")}" data-owner="${esc(t.email || t.owner_email || "")}">成员管理</button>
        <button class="js-team-check primary" data-id="${esc(t.id)}">owner 测活</button>
      </td>
    </tr>
  `).join("");
  if (!teamState.rows.length) tbody.innerHTML = `<tr><td colspan="9" style="text-align:center;color:#64748b;">暂无 Team</td></tr>`;
  document.getElementById("teamPageInfo").textContent = `共 ${teamState.total} 条 · 每页 ${teamState.perPage} 条 · 第 ${teamState.page}/${teamState.totalPages} 页`;
  renderPager("teamPageNums", teamState.page, teamState.totalPages, (next) => loadTeams(next));
}

async function loadMembers(teamId) {
  const out = await api(`/v1/teams/${teamId}/members/list`);
  const rows = out.members || [];
  const invited = rows.filter((x) => x.status === "invited");
  const joined = rows.filter((x) => x.status === "joined" || x.status === "accepted");

  const invitedTbody = document.getElementById("invitedTbody");
  invitedTbody.innerHTML = invited.map((m) => `
    <tr>
      <td>${esc(m.email)}</td>
      <td>${esc(m.updated_at || "-")}</td>
      <td>
        <div class="action-row">
          <button class="primary js-otp-fetch" data-account-id="${esc(m.account_id || m.email)}">一键取件</button>
          <button class="js-revoke" data-email="${esc(m.email)}">撤回</button>
        </div>
      </td>
    </tr>
  `).join("");
  if (!invited.length) invitedTbody.innerHTML = `<tr><td colspan="3" style="text-align:center;color:#64748b;">暂无 invited</td></tr>`;

  const joinedTbody = document.getElementById("joinedTbody");
  joinedTbody.innerHTML = joined.map((m) => `
    <tr>
      <td>${esc(m.email)}</td>
      <td>${esc(m.role || "member")}</td>
      <td>${m.role === "account-owner" ? `<span style="color:#64748b;">不可删除</span>` : `<button class="danger js-del" data-account-id="${esc(m.account_id)}">删除</button>`}</td>
    </tr>
  `).join("");
  if (!joined.length) joinedTbody.innerHTML = `<tr><td colspan="3" style="text-align:center;color:#64748b;">暂无 joined</td></tr>`;

  const otpInput = document.getElementById("otpFetchAccountId");
  if (otpInput && invited.length) {
    const firstAccountID = String(invited[0].account_id || invited[0].email || "").trim();
    if (!otpInput.value.trim() && firstAccountID) otpInput.value = firstAccountID;
  }
}

async function openEditModal(teamId) {
  const out = await api(`/v1/teams/${teamId}/info`);
  const t = out.team || {};
  document.getElementById("editTeamId").value = t.id || "";
  document.getElementById("editOwnerId").value = t.owner_account_id || "";
  document.getElementById("editTeamEmail").value = t.email || t.owner_email || "";
  document.getElementById("editTeamAccountId").value = t.account_id || "";
  document.getElementById("editTeamName").value = t.team_name || t.name || "";
  document.getElementById("editTeamRole").value = t.account_role || "account-owner";
  document.getElementById("editTeamPlan").value = t.subscription_plan || "";
  document.getElementById("editTeamExpiresAt").value = t.expires_at || "";
  document.getElementById("editTeamMaxMembers").value = t.max_members || 6;
  document.getElementById("editTeamStatus").value = t.status || "active";
  document.getElementById("editTeamAccessToken").value = t.access_token || "";
  document.getElementById("editTeamRefreshToken").value = t.refresh_token || "";
  document.getElementById("editTeamSessionToken").value = t.session_token || "";
  document.getElementById("editTeamClientId").value = t.client_id || "";
  openModal("editTeamModal");
}

async function openMembersModal(teamId, name, owner) {
  currentTeamId = teamId;
  document.getElementById("membersMeta").textContent = `${name || "-"} · owner: ${owner || "-"} · team_id: ${teamId}`;
  resetOTPMailViewer();
  openModal("membersModal");
  await loadMembers(teamId);
}

async function runOwnerCheck() {
  if (!currentOwnerCheckTeamId) return;
  const out = await api(`/v1/teams/${currentOwnerCheckTeamId}/owner-check`, { method: "POST" });
  document.getElementById("ownerCheckLog").textContent = JSON.stringify(out, null, 2);
  await Promise.all([loadAccounts(), loadTeams(teamState.page)]);
}

function bind() {
  tabs.forEach((t) => t.addEventListener("click", () => {
    tabs.forEach((x) => x.classList.remove("active"));
    panels.forEach((x) => x.classList.remove("active"));
    t.classList.add("active");
    document.getElementById(t.dataset.tab).classList.add("active");
  }));

  document.querySelectorAll("[data-close]").forEach((b) => b.addEventListener("click", () => closeModal(b.dataset.close)));

  document.getElementById("saveKeyBtn").addEventListener("click", () => {
    localStorage.setItem("goTeamApiKey", apiKeyInput.value.trim());
    showToast("API Key 已保存");
  });

  document.getElementById("reloadAllBtn").addEventListener("click", async () => {
    try {
      await Promise.all([loadAccounts(), loadTeams(1)]);
      showToast("刷新完成");
    } catch (e) {
      showToast(e.message, false);
    }
  });

  ["accSearch", "accTypeFilter", "accTokenFilter"].forEach((id) => {
    const el = document.getElementById(id);
    el.addEventListener("input", () => { accState.page = 1; applyAccountFilters(); });
    el.addEventListener("change", () => { accState.page = 1; applyAccountFilters(); });
  });

  document.getElementById("toggleImportBtn").addEventListener("click", () => {
    document.getElementById("importBox").classList.toggle("hidden");
  });

  document.getElementById("importSubmitBtn").addEventListener("click", async (e) => {
    const text = document.getElementById("importText").value.trim();
    if (!text) {
      showToast("导入内容为空", false);
      return;
    }
    await submitImportWithLoading(e.target, { text, default_account_type: "normal" }, () => {
      document.getElementById("importText").value = "";
    });
  });

  document.getElementById("importAccountTxtBtn").addEventListener("click", async (e) => {
    await submitImportWithLoading(e.target, { path: DEFAULT_ACCOUNT_TXT_PATH, default_account_type: "normal" });
  });

  document.getElementById("accRefreshBtn").addEventListener("click", async () => {
    try { await loadAccounts(); } catch (e) { showToast(e.message, false); }
  });

  document.getElementById("accountsTbody").addEventListener("click", async (e) => {
    const cpaBtn = e.target.closest(".js-cpa-toggle");
    if (cpaBtn) {
      const action = String(cpaBtn.dataset.action || "").trim() || "up";
      setLoading(cpaBtn, true, action === "up" ? "上号中..." : "下号中...");
      try {
        await api(`/v1/accounts/${encodeURIComponent(cpaBtn.dataset.id)}/cpa-toggle`, {
          method: "POST",
          body: JSON.stringify({ action }),
        });
        showToast(action === "up" ? "CPA上号状态已更新" : "CPA下号状态已更新");
        await loadAccounts();
      } catch (err) {
        showToast(err.message, false);
      } finally {
        setLoading(cpaBtn, false);
      }
      return;
    }
    const deleteBtn = e.target.closest(".js-account-delete");
    if (deleteBtn) {
      const targetID = String(deleteBtn.dataset.id || "").trim().toLowerCase();
      const target = accState.raw.find((x) => String(x.id || "").trim().toLowerCase() === targetID);
      if (!target) {
        showToast("账号不存在或已刷新", false);
        return;
      }
      openDeleteAccountModal(target);
      return;
    }
    const tokenBtn = e.target.closest(".js-token-check");
    if (!tokenBtn) return;
    setLoading(tokenBtn, true, "checking...");
    try {
      await api(`/v1/accounts/${tokenBtn.dataset.id}/token-check`, { method: "POST" });
      showToast("token-check 已完成");
      await loadAccounts();
    } catch (err) {
      showToast(err.message, false);
    } finally {
      setLoading(tokenBtn, false);
    }
  });

  document.getElementById("deleteAccountConfirmInput").addEventListener("input", (e) => {
    const txt = String(e.target.value || "").trim().toUpperCase();
    document.getElementById("confirmDeleteAccountBtn").disabled = txt !== "DELETE";
  });

  document.getElementById("confirmDeleteAccountBtn").addEventListener("click", async (e) => {
    if (!deletingAccountID) return;
    setLoading(e.target, true, "删除中...");
    try {
      const out = await api(`/v1/accounts/${encodeURIComponent(deletingAccountID)}/delete`, { method: "POST" });
      closeModal("deleteAccountModal");
      showToast(`账号已删除，清理邀请 ${Number(out.removed_invites || 0)} 条`);
      await Promise.all([loadAccounts(), loadTeams(teamState.page)]);
    } catch (err) {
      showToast(err.message, false);
    } finally {
      setLoading(e.target, false);
    }
  });

  document.getElementById("teamRefreshBtn").addEventListener("click", async () => {
    try { await loadTeams(1); } catch (e) { showToast(e.message, false); }
  });
  document.getElementById("teamSearch").addEventListener("input", () => loadTeams(1).catch((e) => showToast(e.message, false)));

  document.getElementById("importOwnersBtn").addEventListener("click", async (e) => {
    const path = (document.getElementById("ownerImportPath").value || "").trim() || DEFAULT_OWNER_STATUS_PATH;
    setLoading(e.target, true);
    try {
      const out = await api("/v1/teams/import-owners", {
        method: "POST",
        body: JSON.stringify({ path }),
      });
      showToast(`导入完成 imported=${out.imported_count} created=${out.created_teams} skipped=${out.skipped_not_found || 0}`);
      await Promise.all([loadAccounts(), loadTeams(1)]);
    } catch (err) {
      showToast(err.message, false);
    } finally {
      setLoading(e.target, false);
    }
  });

  document.getElementById("teamsTbody").addEventListener("click", async (e) => {
    const editBtn = e.target.closest(".js-team-edit");
    if (editBtn) {
      try { await openEditModal(editBtn.dataset.id); } catch (err) { showToast(err.message, false); }
      return;
    }
    const membersBtn = e.target.closest(".js-team-members");
    if (membersBtn) {
      try { await openMembersModal(membersBtn.dataset.id, membersBtn.dataset.name, membersBtn.dataset.owner); } catch (err) { showToast(err.message, false); }
      return;
    }
    const checkBtn = e.target.closest(".js-team-check");
    if (checkBtn) {
      currentOwnerCheckTeamId = checkBtn.dataset.id;
      document.getElementById("ownerCheckMeta").textContent = `team_id: ${currentOwnerCheckTeamId}`;
      document.getElementById("ownerCheckLog").textContent = "等待执行...";
      openModal("ownerCheckModal");
      try {
        await runOwnerCheck();
        showToast("owner-check 完成");
      } catch (err) {
        document.getElementById("ownerCheckLog").textContent = String(err.message);
        showToast(err.message, false);
      }
    }
  });

  document.getElementById("saveTeamBtn").addEventListener("click", async (e) => {
    setLoading(e.target, true);
    try {
      const teamID = document.getElementById("editTeamId").value.trim();
      await api(`/v1/teams/${teamID}/update`, {
        method: "POST",
        body: JSON.stringify({
          name: document.getElementById("editTeamName").value.trim(),
          team_name: document.getElementById("editTeamName").value.trim(),
          owner_account_id: document.getElementById("editOwnerId").value.trim(),
          email: document.getElementById("editTeamEmail").value.trim(),
          account_id: document.getElementById("editTeamAccountId").value.trim(),
          account_role: document.getElementById("editTeamRole").value.trim(),
          subscription_plan: document.getElementById("editTeamPlan").value.trim(),
          expires_at: document.getElementById("editTeamExpiresAt").value.trim(),
          max_members: Number(document.getElementById("editTeamMaxMembers").value || 6),
          access_token: document.getElementById("editTeamAccessToken").value.trim(),
          refresh_token: document.getElementById("editTeamRefreshToken").value.trim(),
          session_token: document.getElementById("editTeamSessionToken").value.trim(),
          client_id: document.getElementById("editTeamClientId").value.trim(),
          status: document.getElementById("editTeamStatus").value,
        }),
      });
      closeModal("editTeamModal");
      showToast("Team 更新成功");
      await loadTeams(teamState.page);
    } catch (err) {
      showToast(err.message, false);
    } finally {
      setLoading(e.target, false);
    }
  });

  document.getElementById("addMemberBtn").addEventListener("click", async (e) => {
    if (!currentTeamId) return;
    setLoading(e.target, true);
    try {
      const email = document.getElementById("addMemberEmail").value.trim();
      if (!email) throw new Error("邮箱不能为空");
      await api(`/v1/teams/${currentTeamId}/members/add`, { method: "POST", body: JSON.stringify({ email }) });
      document.getElementById("addMemberEmail").value = "";
      showToast("邀请已发送");
      await Promise.all([loadMembers(currentTeamId), loadTeams(teamState.page), loadAccounts()]);
    } catch (err) {
      showToast(err.message, false);
    } finally {
      setLoading(e.target, false);
    }
  });

  document.getElementById("oneClickPoolBtn").addEventListener("click", async (e) => {
    if (!currentTeamId) return;
    setLoading(e.target, true);
    try {
      const count = Math.max(1, Math.min(20, Number(document.getElementById("oneClickCount").value || 1)));
      const out = await api(`/v1/teams/${currentTeamId}/one-click-onboard`, { method: "POST", body: JSON.stringify({ count }) });
      const ok = Number(out.selected_count || 0);
      const fail = Number(out.failed_count || 0);
      showToast(`从 invite_pool 真实邀请成功 ${ok} 个${fail > 0 ? `，失败 ${fail} 个` : ""}`, fail === 0);
      await Promise.all([loadMembers(currentTeamId), loadTeams(teamState.page)]);
    } catch (err) {
      showToast(err.message, false);
    } finally {
      setLoading(e.target, false);
    }
  });

  document.getElementById("oneClickRandomBtn").addEventListener("click", async (e) => {
    if (!currentTeamId) return;
    setLoading(e.target, true);
    try {
      const count = Math.max(1, Math.min(20, Number(document.getElementById("oneClickCount").value || 1)));
      const out = await api(`/v1/teams/${currentTeamId}/one-click-random-invite`, { method: "POST", body: JSON.stringify({ count }) });
      const ok = Number(out.selected_count || 0);
      const fail = Number(out.failed_count || 0);
      showToast(`随机池子真实邀请成功 ${ok} 个${fail > 0 ? `，失败 ${fail} 个` : ""}`, fail === 0);
      await Promise.all([loadMembers(currentTeamId), loadTeams(teamState.page), loadAccounts()]);
    } catch (err) {
      showToast(err.message, false);
    } finally {
      setLoading(e.target, false);
    }
  });

  document.getElementById("invitedTbody").addEventListener("click", async (e) => {
    const otpBtn = e.target.closest(".js-otp-fetch");
    if (otpBtn) {
      const accountID = String(otpBtn.dataset.accountId || "").trim();
      const timeoutVal = Number(document.getElementById("otpFetchTimeout").value || 120);
      document.getElementById("otpFetchAccountId").value = accountID;
      setLoading(otpBtn, true, "取件中...");
      try {
        const out = await fetchOTPByAccount(accountID, timeoutVal);
        const otp = String(out.otp_code || "").trim();
        if (otp) showToast(`OTP: ${otp}`);
        else showToast("取件完成（未提取到 otp_code）", false);
        await loadAccountMails(accountID);
      } catch (err) {
        showToast(err.message, false);
        writeOtpLog(err.message);
      } finally {
        setLoading(otpBtn, false);
      }
      return;
    }
    const revokeBtn = e.target.closest(".js-revoke");
    if (!revokeBtn || !currentTeamId) return;
    setLoading(revokeBtn, true);
    try {
      await api(`/v1/teams/${currentTeamId}/invites/revoke`, { method: "POST", body: JSON.stringify({ email: revokeBtn.dataset.email }) });
      showToast("撤回成功");
      await Promise.all([loadMembers(currentTeamId), loadTeams(teamState.page)]);
    } catch (err) {
      showToast(err.message, false);
    } finally {
      setLoading(revokeBtn, false);
    }
  });

  document.getElementById("otpFetchBtn").addEventListener("click", async (e) => {
    const accountID = document.getElementById("otpFetchAccountId").value.trim();
    const timeoutVal = Number(document.getElementById("otpFetchTimeout").value || 120);
    if (!accountID) {
      showToast("请输入 invited account_id / email", false);
      return;
    }
    setLoading(e.target, true, "取件中...");
    try {
      const out = await fetchOTPByAccount(accountID, timeoutVal);
      const otp = String(out.otp_code || "").trim();
      if (otp) showToast(`OTP: ${otp}`);
      else showToast("取件完成（未提取到 otp_code）", false);
      await loadAccountMails(accountID);
    } catch (err) {
      showToast(err.message, false);
      writeOtpLog(err.message);
    } finally {
      setLoading(e.target, false);
    }
  });

  document.getElementById("otpLoadMailsBtn").addEventListener("click", async (e) => {
    const accountID = document.getElementById("otpFetchAccountId").value.trim();
    if (!accountID) {
      showToast("请输入 invited account_id / email", false);
      return;
    }
    setLoading(e.target, true, "拉取中...");
    try {
      await loadAccountMails(accountID);
      showToast("邮件列表已更新");
    } catch (err) {
      showToast(err.message, false);
      writeOtpLog(err.message);
    } finally {
      setLoading(e.target, false);
    }
  });

  document.getElementById("joinedTbody").addEventListener("click", async (e) => {
    const btn = e.target.closest(".js-del");
    if (!btn || !currentTeamId) return;
    setLoading(btn, true);
    try {
      await api(`/v1/teams/${currentTeamId}/members/${btn.dataset.accountId}/delete`, { method: "POST" });
      showToast("删除成功");
      await Promise.all([loadMembers(currentTeamId), loadTeams(teamState.page)]);
    } catch (err) {
      showToast(err.message, false);
    } finally {
      setLoading(btn, false);
    }
  });

  document.getElementById("ownerCheckRunBtn").addEventListener("click", async (e) => {
    setLoading(e.target, true);
    try {
      await runOwnerCheck();
      showToast("owner-check 完成");
    } catch (err) {
      document.getElementById("ownerCheckLog").textContent = err.message;
      showToast(err.message, false);
    } finally {
      setLoading(e.target, false);
    }
  });
}

async function submitImportWithLoading(btn, payload, onSuccess) {
  setLoading(btn, true);
  try {
    const out = await api("/v1/accounts/import-txt", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    showToast(`导入成功 total=${out.total} inserted=${out.inserted} updated=${out.updated}`);
    if (typeof onSuccess === "function") onSuccess();
    await loadAccounts();
  } catch (err) {
    showToast(err.message, false);
  } finally {
    setLoading(btn, false);
  }
}

async function init() {
  apiKeyInput.value = localStorage.getItem("goTeamApiKey") || "";
  const ownerPathInput = document.getElementById("ownerImportPath");
  if (ownerPathInput) ownerPathInput.value = DEFAULT_OWNER_STATUS_PATH;
  bind();
  try {
    await Promise.all([loadAccounts(), loadTeams(1)]);
  } catch (e) {
    showToast(e.message, false);
  }
}

init();
