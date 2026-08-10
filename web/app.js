import { renderMarkdown } from "./markdown.js";

const $ = (selector) => document.querySelector(selector);
const els = {
  authView: $("#auth-view"), shell: $("#app-shell"), authForm: $("#auth-form"),
  list: $("#conversation-list"), title: $("#conversation-title"), messages: $("#messages"),
  empty: $("#empty-state"), composer: $("#composer"), input: $("#message-input"),
  members: $("#member-list"), stack: $("#participant-stack"), typing: $("#typing"),
  relay: $("#relay-switch"), relayLabel: $("#relay-toggle"), routeStatus: $("#route-status"),
  send: $("#send-button"),
  roundActions: $("#round-actions"), reviewDialog: $("#review-dialog"), reviewAgent: $("#review-agent"),
  chatView: $(".chat"), settingsView: $("#settings-view"), conversationSettings: $("#conversation-settings"), globalSettings: $("#global-settings"),
  skillsView: $("#skills-view"), tabSkills: $("#tab-skills"), conversationSkills: $("#conversation-skills"), skillList: $("#skill-list"), skillAssignments: $("#skill-assignments"), skillDialog: $("#skill-dialog"), skillPreviewDialog: $("#skill-preview-dialog"),
  tasksView: $("#tasks-view"), tabTasks: $("#tab-tasks"), taskBoard: $("#task-board"),
  scopeGlobal: $("#settings-global-scope"), scopeConversation: $("#settings-conversation-scope"),
  settingRelay: $("#setting-auto-relay"), settingReview: $("#setting-auto-review"), settingRounds: $("#setting-review-rounds"), settingSkillMode: $("#setting-skill-mode"), settingSkillShell: $("#setting-skill-shell"), settingSkillNetwork: $("#setting-skill-network"), settingSkillWrite: $("#setting-skill-write"),
  newDialog: $("#new-dialog"), agentDialog: $("#agent-dialog"), conversationDialog: $("#conversation-dialog"), toast: $("#toast"),
};
let state = null;
let currentUser = null;
let authMode = "login";
let events = null;
let activeId = null;
let renderedConversationId = null;
let renderedMessageCount = 0;
let activeView = "chat";
let settingsScope = "conversation";
let showArchived = false;
let providerStatuses = null;
let providerConfigs = [];
let maintenanceStatus = null;
let auditEvents = [];
const lastSubmitted = new Map();
const olderMessages = new Map();
const olderHasMore = new Map();

async function api(url, options) {
  const request = options || {};
  const uploading = request.body instanceof FormData;
  const response = await fetch(url, { ...request, headers: { ...(uploading ? {} : { "content-type":"application/json" }), ...(request.headers || {}) } });
  const value = await response.json();
  if (response.status === 401 && !url.startsWith("/api/auth/")) showAuth();
  if (!response.ok) throw new Error(value.error || "请求失败");
  return value;
}

function initials(name) { return name.slice(0, 2).toUpperCase(); }
function providerLabel(provider) { return ({ claude:"Claude Code", codex:"Codex CLI", pi:"Pi Agent" })[provider] || provider; }
function providerStatus(provider) { return providerStatuses?.find((item) => item.provider === provider); }
function isProviderInstalled(provider) { const status = providerStatus(provider); return !status || Boolean(status.installed); }
function time(value) { return new Intl.DateTimeFormat("zh-CN", { hour:"2-digit", minute:"2-digit" }).format(new Date(value)); }
function escapeHtml(value) { return value.replace(/[&<>"']/g, (char) => ({ "&":"&amp;", "<":"&lt;", ">":"&gt;", '"':"&quot;", "'":"&#39;" })[char]); }
function toast(text) { els.toast.textContent = text; els.toast.hidden = false; setTimeout(() => { els.toast.hidden = true; }, 2400); }

function showAuth() {
  currentUser = null;
  state = null;
  els.shell.hidden = true;
  els.authView.hidden = false;
  if (events) { events.close(); events = null; }
  setTimeout(() => $("#auth-username").focus(), 30);
}

function showApp(user) {
  currentUser = user;
  activeId = localStorage.getItem(`agent-tag-active:${user.id}`);
  els.authView.hidden = true;
  els.shell.hidden = false;
  $("#account-name").textContent = user.username;
  $("#account-avatar").textContent = user.username.slice(0, 1).toUpperCase();
}

function rememberActiveConversation() {
  if (!currentUser) return;
  if (activeId) localStorage.setItem(`agent-tag-active:${currentUser.id}`, activeId);
  else localStorage.removeItem(`agent-tag-active:${currentUser.id}`);
}

function setAuthMode(mode) {
  authMode = mode;
  const registering = mode === "register";
  $("#auth-login-tab").classList.toggle("active", !registering);
  $("#auth-register-tab").classList.toggle("active", registering);
  $("#auth-title").textContent = registering ? "创建本地账号" : "登录本地团队";
  $("#auth-description").textContent = registering ? "首个账号会接管升级前已有的对话记录。" : "每个账号拥有独立的对话、消息与设置。";
  $("#auth-submit").textContent = registering ? "创建账号" : "登录";
  $("#auth-password").autocomplete = registering ? "new-password" : "current-password";
  $("#auth-error").hidden = true;
}

function connectEvents() {
  if (events) events.close();
  events = new EventSource("/api/events");
  events.addEventListener("changed", () => refresh().catch(() => {}));
  events.onerror = () => { if (currentUser) toast("正在重新连接本地团队…"); };
}

async function refresh() {
  state = await api("/api/state");
  $("#team-name").textContent = state.team.name;
  if (activeId && !state.conversations.some((item) => item.id === activeId)) activeId = null;
  const available = state.conversations.filter((item) => !item.archived);
  if (activeId && state.conversations.find((item) => item.id === activeId)?.archived) showArchived = true;
  if (!activeId && available.length) activeId = [...available].sort((a,b) => b.updatedAt.localeCompare(a.updatedAt))[0].id;
  render();
}

function render() {
  const conversations = state.conversations.filter((item) => !item.archived).sort((a,b) => b.updatedAt.localeCompare(a.updatedAt));
  const archived = state.conversations.filter((item) => item.archived).sort((a,b) => b.updatedAt.localeCompare(a.updatedAt));
  els.list.innerHTML = conversations.map((item) => `<button class="conversation-item ${item.id === activeId ? "active" : ""}" data-id="${item.id}">${escapeHtml(item.title)}</button>`).join("") || '<span class="conversation-item">还没有对话</span>';
  $("#archived-count").textContent = String(archived.length);
  $("#archived-toggle").hidden = archived.length === 0;
  $("#archived-toggle").classList.toggle("active", showArchived);
  $("#archived-conversation-list").hidden = !showArchived || archived.length === 0;
  $("#archived-conversation-list").innerHTML = archived.map((item) => `<button class="conversation-item ${item.id === activeId ? "active" : ""}" data-id="${item.id}">${escapeHtml(item.title)}</button>`).join("");
  const conversation = state.conversations.find((item) => item.id === activeId);
  const hasConversation = Boolean(conversation);
  const showingSettings = activeView === "settings";
  const showingSkills = activeView === "skills";
  const showingTasks = activeView === "tasks";
  els.chatView.hidden = showingSettings || showingSkills || showingTasks;
  els.settingsView.hidden = !showingSettings;
  els.skillsView.hidden = !showingSkills;
  els.tasksView.hidden = !showingTasks;
  els.conversationSettings.disabled = !hasConversation;
  els.conversationSkills.disabled = !hasConversation;
  $("#conversation-manage").disabled = !hasConversation;
  els.globalSettings.classList.toggle("active", showingSettings && settingsScope === "global");
  els.tabSkills.classList.toggle("active", showingSkills);
  els.tabTasks.classList.toggle("active", showingTasks);
  els.empty.hidden = hasConversation; els.messages.hidden = !hasConversation; els.composer.hidden = !hasConversation;
  const globalScope = settingsScope === "global";
  const settings = globalScope ? (state.defaults || { reviewRounds:1, skillMode:"auto", allowSkillExecution:true }) : conversation;
  els.scopeGlobal.classList.toggle("active", globalScope);
  els.scopeConversation.classList.toggle("active", !globalScope);
  els.scopeConversation.disabled = !hasConversation;
  $("#settings-scope").textContent = globalScope ? "全局默认" : "当前会话";
  $("#settings-title").textContent = globalScope ? "新会话默认设置" : (conversation?.title || "讨论设置");
  $("#settings-heading").textContent = globalScope ? "默认讨论流程" : "会话讨论流程";
  $("#settings-description").textContent = globalScope ? "应用于之后创建的新会话，不修改已有会话。" : "以下配置仅作用于当前会话，不会影响其他对话。";
  $("#settings-hint").textContent = globalScope ? "新建会话会复制这些默认值，之后仍可单独调整。" : "设置会自动保存，并与当前会话绑定。";
  if (settings) {
    els.settingRelay.checked = Boolean(settings.autoRelay);
    els.settingRelay.disabled = !globalScope && !conversation.started;
    els.settingReview.checked = Boolean(settings.autoReview);
    els.settingRounds.value = String(settings.reviewRounds || 1);
    els.settingRounds.disabled = !settings.autoReview;
    els.settingSkillMode.value = settings.skillMode || "auto";
    const permissions = settings.skillPermissions || { shell:Boolean(settings.allowSkillExecution), network:Boolean(settings.allowSkillExecution), write:Boolean(settings.allowSkillExecution) };
    els.settingSkillShell.checked = Boolean(permissions.shell);
    els.settingSkillNetwork.checked = Boolean(permissions.network);
    els.settingSkillWrite.checked = Boolean(permissions.write);
  }
  renderProviderStatuses();
  renderSkills(conversation);
  renderTasks();
  if (!conversation) { els.title.textContent = "选择或新建一个对话"; els.members.innerHTML = ""; els.stack.innerHTML = ""; els.roundActions.hidden = true; return; }
  els.title.textContent = conversation.title;
  els.stack.innerHTML = conversation.participants.map((p) => `<span class="mini-avatar ${p.provider} ${isProviderInstalled(p.provider) ? "" : "unavailable"}" title="${escapeHtml(p.name)}${isProviderInstalled(p.provider) ? "" : "（未安装）"}">${initials(p.name)}</span>`).join("");
  els.members.innerHTML = conversation.participants.map((p) => `<div class="member ${isProviderInstalled(p.provider) ? "" : "unavailable"}"><span class="member-avatar ${p.provider}">${initials(p.name)}</span><span><b>${escapeHtml(p.name)}</b><small>${providerLabel(p.provider)}${p.model ? ` · ${escapeHtml(p.model)}` : ""}</small></span><span class="member-flags">${isProviderInstalled(p.provider) ? "" : '<i class="provider-missing">未安装</i>'}${p.sessionId ? '<i class="session-live">SESSION</i>' : ""}${p.autoDiscuss ? '<i class="auto">AUTO</i>' : ""}</span><span class="member-actions"><button type="button" data-edit-agent="${escapeHtml(p.name)}" title="编辑">✎</button>${p.sessionId ? `<button type="button" data-reset-agent="${escapeHtml(p.name)}" title="重置会话">↻</button>` : ""}<button type="button" data-remove-agent="${escapeHtml(p.name)}" title="移除">×</button></span></div>`).join("");
  const recentMessages = state.chatMessages.filter((item) => item.conversationId === activeId);
  const cachedOlder = olderMessages.get(activeId) || [];
  const byMessageId = new Map([...cachedOlder, ...recentMessages].map((message) => [message.id, message]));
  const messages = [...byMessageId.values()].sort((a,b) => a.createdAt.localeCompare(b.createdAt));
  const wasNearBottom = els.messages.scrollHeight - els.messages.clientHeight - els.messages.scrollTop < 120;
  const conversationChanged = renderedConversationId !== activeId;
  const receivedMessage = messages.length > renderedMessageCount;
  const liveReplies = (state.liveReplies || []).filter((item) => item.conversationId === activeId);
  const persistedMarkup = messages.map((message) => {
    if (message.kind === "system") return `<div class="message system">${escapeHtml(message.body)}${message.retryAgent ? `<button type="button" data-retry-agent="${escapeHtml(message.retryAgent)}">重试 ${escapeHtml(message.retryAgent)}</button>` : ""}</div>`;
    const user = message.kind === "user";
    const renderedBody = user ? escapeHtml(message.body) : renderMarkdown(message.body);
    const recordedSteps = !user && Array.isArray(message.steps) ? message.steps : [];
    const steps = !user && recordedSteps.length ? recordedSteps : (!user ? ["完成分析并组织回复"] : []);
    const process = steps.length ? `<details class="process"><summary><span>思考与执行</span><em>（${steps.length} 步）</em></summary><ol>${steps.map((step) => `<li>${escapeHtml(step)}</li>`).join("")}</ol></details>` : "";
    const artifacts = !user && Array.isArray(message.artifacts) && message.artifacts.length ? `<div class="message-artifacts">${message.artifacts.map((artifact) => `<span title="${escapeHtml(artifact.path)}">↳ ${escapeHtml(artifact.label)}</span>`).join("")}</div>` : "";
    const phaseLabel = message.phase === "synthesis" ? '<i class="phase-badge">综合</i>' : message.phase === "review" ? `<i class="phase-badge">评议 ${message.reviewRound || 1}</i>` : "";
    return `<article class="message ${user ? "user" : message.provider}"><span class="avatar">${user ? "ME" : initials(message.author)}</span><div class="message-content"><div class="message-meta"><span>${escapeHtml(message.author)}</span>${phaseLabel}<time>${time(message.createdAt)}</time></div>${process}${artifacts}<div class="bubble ${user ? "" : "markdown"}">${renderedBody}</div></div></article>`;
  }).join("");
  const liveMarkup = liveReplies.map((reply) => {
    const steps = Array.isArray(reply.steps) ? reply.steps : [];
    const process = steps.length ? `<details class="process" open><summary><span>实时执行</span><em>（${steps.length} 步）</em></summary><ol>${steps.map((step) => `<li>${escapeHtml(step)}</li>`).join("")}</ol></details>` : "";
    const phaseLabel = reply.phase === "synthesis" ? '<i class="phase-badge">综合</i>' : reply.phase === "review" ? `<i class="phase-badge">评议 ${reply.reviewRound || 1}</i>` : "";
    const body = reply.text ? renderMarkdown(reply.text) : '<span class="live-waiting">正在准备回复…</span>';
    return `<article class="message ${reply.provider} live-reply"><span class="avatar">${initials(reply.author)}</span><div class="message-content"><div class="message-meta"><span>${escapeHtml(reply.author)}</span>${phaseLabel}<i class="live-dot"></i><button class="stop-agent" type="button" data-stop-agent="${escapeHtml(reply.author)}">停止此 Agent</button></div>${process}<div class="bubble markdown">${body}<span class="stream-caret"></span></div></div></article>`;
  }).join("");
  const canLoadOlder = olderHasMore.get(activeId) === true || ((state.messageRemaining?.[activeId] || 0) > cachedOlder.length);
  els.messages.innerHTML = (canLoadOlder ? '<button class="load-older" type="button" data-load-older>加载更早消息</button>' : "") + persistedMarkup + liveMarkup;
  const activeTyping = state.typing.find((item) => item.conversationId === activeId)?.names || [];
  const isRunning = state.activeConversations?.includes(activeId) || activeTyping.length > 0;
  const latestUserIndex = messages.findLastIndex((message) => message.kind === "user");
  const currentRound = latestUserIndex >= 0 ? messages.slice(latestUserIndex + 1) : [];
  const reviewReplies = currentRound.filter((message) => message.kind === "agent" && message.phase === "review");
  const maxReviewRound = reviewReplies.reduce((max, message) => Math.max(max, message.reviewRound || 1), 0);
  const latestReplies = maxReviewRound
    ? reviewReplies.filter((message) => (message.reviewRound || 1) === maxReviewRound)
    : currentRound.filter((message) => message.kind === "agent" && (!message.phase || message.phase === "primary"));
  const canReview = !isRunning && latestReplies.length >= 2;
  els.roundActions.hidden = !canReview;
  $("#round-status").textContent = maxReviewRound ? `第 ${maxReviewRound} 轮评议完成` : "首轮观点已齐";
  els.reviewAgent.innerHTML = conversation.participants.map((p) => `<option value="${escapeHtml(p.name)}">${escapeHtml(p.name)} · ${providerLabel(p.provider)}</option>`).join("");
  els.typing.hidden = !activeTyping.length;
  els.typing.textContent = activeTyping.length ? `${activeTyping.join("、")} 正在思考` : "";
  els.composer.classList.toggle("running", isRunning);
  els.send.classList.toggle("stop", isRunning);
  els.send.setAttribute("aria-label", isRunning ? "终止当前回复" : "发送消息");
  els.send.innerHTML = isRunning ? '<span class="stop-icon"></span>' : '<span>↗</span>';
  els.relay.checked = Boolean(conversation.autoRelay);
  els.relay.disabled = !conversation.started;
  els.relayLabel.classList.toggle("disabled", !conversation.started);
  els.relayLabel.title = conversation.started ? "控制群内 Agent 是否同时抢答" : "首次 @ Agent 回复后可开启";
  if (isRunning) {
    els.routeStatus.innerHTML = '<span class="pulse"></span><span>先终止当前回复，再重新发送</span>';
  } else if (!conversation.started) {
    els.input.placeholder = "首次消息请 @任意 Agent 开启会话…";
    els.routeStatus.innerHTML = conversation.participants.map((p) => `<button type="button" data-mention="@${escapeHtml(p.name)}" ${isProviderInstalled(p.provider) ? "" : "disabled title=\"Agent 未安装\""}>@${escapeHtml(p.name)}</button>`).join("") + '<span>首次需 @</span>';
  } else if (conversation.autoRelay) {
    els.input.placeholder = "直接输入，群内 Agent 会同时抢答…";
    els.routeStatus.innerHTML = '<span class="pulse"></span><span>并行抢答已开启</span>';
  } else {
    els.input.placeholder = "并行抢答已关闭，使用 @成员名唤醒…";
    els.routeStatus.innerHTML = conversation.participants.map((p) => `<button type="button" data-mention="@${escapeHtml(p.name)}" ${isProviderInstalled(p.provider) ? "" : "disabled title=\"Agent 未安装\""}>@${escapeHtml(p.name)}</button>`).join("") + '<span>定向唤醒</span>';
  }
  renderedConversationId = activeId;
  renderedMessageCount = messages.length;
  if (conversationChanged || (receivedMessage && wasNearBottom)) requestAnimationFrame(() => { els.messages.scrollTop = els.messages.scrollHeight; });
}

function renderProviderStatuses() {
  const list = $("#provider-health-list");
  if (!providerStatuses) { list.innerHTML = '<span class="provider-checking">正在检测…</span>'; return; }
  list.innerHTML = providerStatuses.map((item) => `<div class="provider-health-row"><i class="${item.installed ? "available" : "missing"}"></i><span><b>${providerLabel(item.provider)}</b><small>${escapeHtml(item.version || item.error || "版本未知")}</small></span><code title="${escapeHtml(item.path || "")}">${escapeHtml(item.path || "未安装")}</code><span class="provider-row-actions">${item.installed ? "" : `<button type="button" class="install" data-install-provider="${item.provider}">安装</button>`}<button type="button" data-config-provider="${item.provider}">配置</button></span></div>`).join("");
}

async function loadProviderStatuses() {
  providerStatuses = null;
  renderProviderStatuses();
  try { const result = await api("/api/providers"); providerStatuses = result.providers; providerConfigs = result.configs || []; }
  catch (error) { toast(error.message); providerStatuses = []; }
  renderProviderStatuses();
  if (state) render();
}

async function installProvider(provider) {
  const result = await api(`/api/providers/${provider}/install`, { method:"POST", body:"{}" });
  await loadProviderStatuses();
  if (!providerStatus(provider)?.installed) throw new Error(`${providerLabel(provider)} 安装后仍不可用，请配置 CLI 路径`);
  toast(`${providerLabel(provider)} 安装完成`);
  return result;
}

function formatBytes(value) { if (value < 1024) return `${value} B`; if (value < 1048576) return `${(value/1024).toFixed(1)} KiB`; return `${(value/1048576).toFixed(1)} MiB`; }
async function loadMaintenanceStatus() {
  try { maintenanceStatus = await api("/api/maintenance"); $("#storage-summary").textContent = `元数据 ${formatBytes(maintenanceStatus.stateBytes)} · SQLite ${formatBytes(maintenanceStatus.databaseBytes)} · 产物 ${formatBytes(maintenanceStatus.artifactBytes)}`; $("#backup-list").textContent = maintenanceStatus.backups?.length ? `最近备份：${maintenanceStatus.backups.slice(0,3).join("、")}` : "尚无备份"; }
  catch (error) { $("#storage-summary").textContent = error.message; }
}
async function loadAudit() {
  try {
    auditEvents = (await api("/api/audit?limit=50")).events || [];
    $("#audit-list").innerHTML = auditEvents.length ? auditEvents.map((event) => `<div class="audit-row"><time>${time(event.createdAt)}</time><b>${escapeHtml(event.actor)}</b><span>${escapeHtml(event.action)}</span><code>${escapeHtml(event.details)}</code></div>`).join("") : '<span class="provider-checking">暂无审计记录</span>';
  } catch (error) { toast(error.message); }
}

els.messages.onclick = async (event) => {
  const loadOlder = event.target.closest("[data-load-older]");
  if (loadOlder && activeId) {
    loadOlder.disabled = true;
    const current = olderMessages.get(activeId) || [];
    const recent = state.chatMessages.filter((item) => item.conversationId === activeId);
    const first = [...current, ...recent].sort((a,b) => a.createdAt.localeCompare(b.createdAt))[0];
    try {
      const page = await api(`/api/conversations/${activeId}/messages?before=${encodeURIComponent(first?.id || "")}&limit=100`);
      const merged = new Map([...page.messages, ...current].map((message) => [message.id, message]));
      olderMessages.set(activeId, [...merged.values()].sort((a,b) => a.createdAt.localeCompare(b.createdAt)));
      olderHasMore.set(activeId, Boolean(page.hasMore));
      render();
    } catch (error) { toast(error.message); loadOlder.disabled = false; }
    return;
  }
  const retry = event.target.closest("[data-retry-agent]");
  if (retry && activeId) {
    retry.disabled = true;
    try { await api(`/api/conversations/${activeId}/retry`, { method:"POST", body:JSON.stringify({ agent:retry.dataset.retryAgent }) }); await refresh(); }
    catch (error) { toast(error.message); retry.disabled = false; }
    return;
  }
  const stop = event.target.closest("[data-stop-agent]");
  if (!stop || !activeId) return;
  stop.disabled = true;
  try { await api(`/api/conversations/${activeId}/participants/${encodeURIComponent(stop.dataset.stopAgent)}/cancel`, { method:"POST", body:"{}" }); await refresh(); }
  catch (error) { toast(error.message); stop.disabled = false; }
};

function renderSkills(conversation) {
  const skills = state.skills || [];
  $("#skill-count").textContent = `${skills.length} 可用`;
  els.skillList.innerHTML = skills.length ? skills.map((skill) => {
    const edit = skill.editable ? `<button type="button" data-edit-skill="${skill.id}" title="编辑">✎</button>` : "";
    const remove = skill.deletable ? `<button type="button" data-delete-skill="${skill.id}" title="删除">×</button>` : "";
    return `<article class="skill-card"><div><div class="skill-title"><b>${escapeHtml(skill.name)}</b><span class="skill-source ${escapeHtml(skill.sourceType)}">${escapeHtml(skill.source)}</span></div><p>${escapeHtml(skill.description)}</p></div><div class="skill-card-actions"><button type="button" data-view-skill="${skill.id}" title="查看 SKILL.md">⌘</button>${edit}${remove}</div></article>`;
  }).join("") : '<div class="skill-empty">没有发现 Skill<br>可以导入 ZIP 或手工新建</div>';
  if (!conversation) {
    $("#skill-assignment-context").textContent = "选择一个会话后，可以为其中的 Agent 分配 Skill。";
    els.skillAssignments.innerHTML = '<div class="skill-empty">当前没有选中的会话</div>';
    return;
  }
  $("#skill-assignment-context").textContent = `当前会话：${conversation.title} · ${conversation.skillMode === "auto" ? "自动匹配，同时保留手工固定项" : "固定绑定"}`;
  if (!skills.length) {
    els.skillAssignments.innerHTML = '<div class="skill-empty">请先创建 Skill</div>';
    return;
  }
  els.skillAssignments.innerHTML = conversation.participants.map((participant) => {
    const assigned = new Set(participant.skillIds || []);
    const checks = skills.map((skill) => `<label class="skill-check"><input type="checkbox" data-agent="${escapeHtml(participant.name)}" data-skill-id="${skill.id}" ${assigned.has(skill.id) ? "checked" : ""}>${escapeHtml(skill.name)}</label>`).join("");
    return `<article class="agent-skill-row"><div class="agent-skill-head"><span class="member-avatar ${participant.provider}">${initials(participant.name)}</span><span><b>${escapeHtml(participant.name)}</b><small>${providerLabel(participant.provider)}</small></span></div><div class="skill-checks">${checks}</div></article>`;
  }).join("");
}

function renderTasks() {
  const tasks = state.tasks || [];
  const runs = state.taskRuns || [];
  const agents = state.taskAgents || [];
  $("#task-agent-strip").innerHTML = agents.length ? agents.map((agent) => `<span class="task-agent ${agent.status}"><i></i>${escapeHtml(agent.name)} · ${providerLabel(agent.provider)}</span>`).join("") : '<span class="provider-checking">暂无运行中的 Worker</span>';
  const columns = [["pending","待处理"],["in_progress,cancel_requested","进行中"],["blocked","已阻塞"],["canceled","已取消"],["completed","已完成"]];
  els.taskBoard.innerHTML = columns.map(([statuses,label]) => { const accepted = statuses.split(","); const items = tasks.filter((task) => accepted.includes(task.status)); return `<section class="task-column"><header><b>${label}</b><span>${items.length}</span></header><div>${items.map((task) => { const taskRuns = runs.filter((run) => run.taskId === task.id).sort((a,b) => b.startedAt.localeCompare(a.startedAt)); const latestRun = taskRuns[0]; return `<article class="task-card"><small>${task.id}${task.assignee ? ` · ${escapeHtml(task.assignee)}` : ""}${task.status === "cancel_requested" ? " · 正在取消" : ""}</small><h3>${escapeHtml(task.title)}</h3>${task.description ? `<p>${escapeHtml(task.description)}</p>` : ""}${task.depends?.length ? `<code>依赖：${task.depends.map(escapeHtml).join(", ")}</code>` : ""}${task.scopes?.length ? `<code>范围：${task.scopes.map(escapeHtml).join(", ")}</code>` : ""}${task.summary ? `<em>${escapeHtml(task.summary)}</em>` : ""}${task.lastError ? `<strong>${escapeHtml(task.lastError)}</strong>` : ""}${latestRun ? `<details class="task-log"><summary>运行日志 · ${escapeHtml(latestRun.agent)}</summary>${latestRun.stdout ? `<pre>${escapeHtml(latestRun.stdout)}</pre>` : ""}${latestRun.stderr ? `<pre class="stderr">${escapeHtml(latestRun.stderr)}</pre>` : ""}</details>` : ""}<footer>${task.status === "in_progress" ? `<button data-cancel-task="${task.id}">取消运行</button>` : ""}${!["in_progress","cancel_requested"].includes(task.status) ? `<button data-edit-task="${task.id}">编辑</button>` : ""}${!["in_progress","cancel_requested","pending"].includes(task.status) ? `<button data-retry-task="${task.id}">重试</button>` : ""}${!["in_progress","cancel_requested"].includes(task.status) ? `<button data-delete-task="${task.id}">删除</button>` : ""}</footer></article>`; }).join("") || '<span class="task-empty">暂无</span>'}</div></section>`; }).join("");
}

function openNew() { $("#new-title").value = ""; els.newDialog.showModal(); setTimeout(() => $("#new-title").focus(), 50); }
$("#new-chat").onclick = openNew; $("#empty-new").onclick = openNew;
function showView(view) { activeView = view; render(); }
function showSettings(scope) { settingsScope = scope; showView("settings"); }
els.globalSettings.onclick = () => showSettings("global");
els.conversationSettings.onclick = () => { if (activeId) showSettings("conversation"); };
els.tabSkills.onclick = () => showView("skills");
els.tabTasks.onclick = () => showView("tasks");
els.conversationSkills.onclick = () => showView("skills");
$("#skills-back").onclick = () => showView("chat");
$("#tasks-back").onclick = () => showView("chat");
$("#new-task").onclick = () => { $("#task-form").reset(); $("#task-id").value = ""; $("#task-dialog-title").textContent = "新建任务"; $("#task-dialog").showModal(); };
$("#task-form").onsubmit = async (event) => {
  if (event.submitter?.value === "cancel") return;
  event.preventDefault();
  const csv = (value) => value.split(",").map((item) => item.trim()).filter(Boolean);
  const id = $("#task-id").value;
  try { await api(id ? `/api/tasks/${id}` : "/api/tasks", { method:id ? "PATCH" : "POST", body:JSON.stringify({ title:$("#task-title").value, description:$("#task-description").value, depends:csv($("#task-depends").value), scopes:csv($("#task-scopes").value) }) }); $("#task-dialog").close(); await refresh(); }
  catch (error) { toast(error.message); }
};
els.taskBoard.onclick = async (event) => {
  const retry = event.target.closest("[data-retry-task]");
  const cancel = event.target.closest("[data-cancel-task]");
  const edit = event.target.closest("[data-edit-task]");
  const remove = event.target.closest("[data-delete-task]");
  if (edit) { const task = (state.tasks || []).find((item) => item.id === edit.dataset.editTask); if (!task) return; $("#task-id").value = task.id; $("#task-dialog-title").textContent = `编辑 ${task.id}`; $("#task-title").value = task.title; $("#task-description").value = task.description || ""; $("#task-depends").value = (task.depends || []).join(", "); $("#task-scopes").value = (task.scopes || []).join(", "); $("#task-dialog").showModal(); return; }
  try {
    if (retry) await api(`/api/tasks/${retry.dataset.retryTask}`, { method:"PATCH", body:JSON.stringify({ retry:true }) });
    if (cancel && window.confirm(`取消正在运行的任务 ${cancel.dataset.cancelTask}？`)) await api(`/api/tasks/${cancel.dataset.cancelTask}`, { method:"PATCH", body:JSON.stringify({ cancel:true }) });
    if (remove && window.confirm(`删除任务 ${remove.dataset.deleteTask}？`)) await api(`/api/tasks/${remove.dataset.deleteTask}`, { method:"DELETE", body:"{}" });
    await refresh();
  } catch (error) { toast(error.message); }
};
els.scopeGlobal.onclick = () => showSettings("global");
els.scopeConversation.onclick = () => { if (activeId) showSettings("conversation"); };
$("#settings-back").onclick = () => showView("chat");
$("#refresh-providers").onclick = loadProviderStatuses;
$("#create-backup").onclick = async () => { try { const result = await api("/api/maintenance", { method:"POST", body:JSON.stringify({ action:"backup" }) }); toast(`备份完成：${result.backup}`); await loadMaintenanceStatus(); } catch (error) { toast(error.message); } };
$("#cleanup-artifacts").onclick = async () => { if (!window.confirm("清理已不存在对话所遗留的共享产物？")) return; try { const result = await api("/api/maintenance", { method:"POST", body:JSON.stringify({ action:"cleanup-artifacts" }) }); toast(`已清理 ${result.removed} 个产物目录`); await loadMaintenanceStatus(); } catch (error) { toast(error.message); } };
$("#refresh-audit").onclick = loadAudit;
$("#provider-health-list").onclick = (event) => {
  const install = event.target.closest("[data-install-provider]");
  if (install) {
    const provider = install.dataset.installProvider;
    if (!window.confirm(`将在本机全局安装 ${providerLabel(provider)}，是否继续？`)) return;
    install.disabled = true;
    install.textContent = "安装中…";
    installProvider(provider).catch((error) => { toast(error.message); install.disabled = false; install.textContent = "重试"; });
    return;
  }
  const button = event.target.closest("[data-config-provider]");
  if (!button) return;
  const provider = button.dataset.configProvider;
  const config = providerConfigs.find((item) => item.provider === provider) || {};
  $("#provider-config-name").value = provider;
  $("#provider-dialog-title").textContent = `配置 ${providerLabel(provider)}`;
  $("#provider-executable").value = config.executable || "";
  $("#provider-extra-args").value = config.extraArgs || "";
  $("#provider-timeout").value = String(config.timeoutSeconds || 300);
  $("#provider-dialog").showModal();
};
$("#provider-form").onsubmit = async (event) => {
  if (event.submitter?.value === "cancel") return;
  event.preventDefault();
  const provider = $("#provider-config-name").value;
  try {
    await api(`/api/providers/${provider}`, { method:"PATCH", body:JSON.stringify({ executable:$("#provider-executable").value, extraArgs:$("#provider-extra-args").value, timeoutSeconds:Number($("#provider-timeout").value) }) });
    $("#provider-dialog").close();
    await loadProviderStatuses();
  } catch (error) { toast(error.message); }
};
els.list.onclick = (event) => { const button = event.target.closest("[data-id]"); if (button) { activeId = button.dataset.id; activeView = "chat"; rememberActiveConversation(); render(); } };
$("#archived-conversation-list").onclick = els.list.onclick;
$("#archived-toggle").onclick = () => { showArchived = !showArchived; render(); };
$("#new-form").onsubmit = async (event) => {
  if (event.submitter?.value === "cancel") return;
  event.preventDefault();
  try { const conversation = await api("/api/conversations", { method:"POST", body:JSON.stringify({ title:$("#new-title").value }) }); activeId = conversation.id; activeView = "chat"; rememberActiveConversation(); els.newDialog.close(); await refresh(); }
  catch (error) { toast(error.message); }
};
function openAgentEditor(participant = null) {
  $("#agent-original-name").value = participant?.name || "";
  $("#agent-name").value = participant?.name || "";
  $("#agent-provider").value = participant?.provider || "codex";
  $("#agent-model").value = participant?.model || "";
  $("#agent-auto-discuss").checked = participant ? Boolean(participant.autoDiscuss) : true;
  $("#agent-dialog-title").textContent = participant ? "编辑 Agent" : "添加 Agent";
  updateAgentProviderStatus();
  els.agentDialog.showModal();
}
function updateAgentProviderStatus() {
  const provider = $("#agent-provider").value;
  const status = providerStatus(provider);
  const note = $("#agent-provider-status");
  note.classList.toggle("missing", Boolean(status && !status.installed));
  note.textContent = !status ? "正在检测本机 Agent…" : status.installed ? `已安装：${status.version || status.path || providerLabel(provider)}` : `${providerLabel(provider)} 尚未安装；保存时可选择是否安装。`;
}
$("#agent-provider").onchange = updateAgentProviderStatus;
$("#add-agent").onclick = () => { if (activeId) openAgentEditor(); };
$("#agent-form").onsubmit = async (event) => {
  if (event.submitter?.value === "cancel") return;
  event.preventDefault();
  const originalName = $("#agent-original-name").value;
  const payload = { name:$("#agent-name").value, provider:$("#agent-provider").value, model:$("#agent-model").value, autoDiscuss:$("#agent-auto-discuss").checked };
  const endpoint = originalName ? `/api/conversations/${activeId}/participants/${encodeURIComponent(originalName)}` : `/api/conversations/${activeId}/participants`;
  try {
    if (!isProviderInstalled(payload.provider)) {
      if (!window.confirm(`${providerLabel(payload.provider)} 尚未安装。是否现在安装？\n\n选择“取消”将不会添加或切换到这个 Agent。`)) return;
      const submit = event.submitter;
      submit.disabled = true; submit.textContent = "安装中…";
      try { await installProvider(payload.provider); }
      finally { submit.disabled = false; submit.textContent = originalName ? "保存" : "添加"; }
    }
    await api(endpoint, { method:originalName ? "PATCH" : "POST", body:JSON.stringify(payload) }); els.agentDialog.close(); await refresh();
  }
  catch (error) { toast(error.message); }
};
els.members.onclick = async (event) => {
  const conversation = state.conversations.find((item) => item.id === activeId);
  const edit = event.target.closest("[data-edit-agent]");
  if (edit) { openAgentEditor(conversation?.participants.find((item) => item.name === edit.dataset.editAgent)); return; }
  const reset = event.target.closest("[data-reset-agent]");
  if (reset) {
    if (!window.confirm(`重置 ${reset.dataset.resetAgent} 的原生会话？下一次将从当前聊天记录重新开始。`)) return;
    try { await api(`/api/conversations/${activeId}/participants/${encodeURIComponent(reset.dataset.resetAgent)}/reset-session`, { method:"POST", body:"{}" }); await refresh(); }
    catch (error) { toast(error.message); }
    return;
  }
  const remove = event.target.closest("[data-remove-agent]");
  if (!remove || !window.confirm(`从对话中移除 ${remove.dataset.removeAgent}？`)) return;
  try { await api(`/api/conversations/${activeId}/participants/${encodeURIComponent(remove.dataset.removeAgent)}`, { method:"DELETE", body:"{}" }); await refresh(); }
  catch (error) { toast(error.message); }
};
$("#conversation-manage").onclick = () => {
  const conversation = state.conversations.find((item) => item.id === activeId);
  if (!conversation) return;
  $("#manage-conversation-title").value = conversation.title;
  $("#archive-conversation").textContent = conversation.archived ? "恢复" : "归档";
  els.conversationDialog.showModal();
};
$("#conversation-form").onsubmit = async (event) => {
  if (event.submitter?.value === "cancel") return;
  event.preventDefault();
  try { await api(`/api/conversations/${activeId}`, { method:"PATCH", body:JSON.stringify({ title:$("#manage-conversation-title").value }) }); els.conversationDialog.close(); await refresh(); }
  catch (error) { toast(error.message); }
};
$("#archive-conversation").onclick = async () => {
  const conversation = state.conversations.find((item) => item.id === activeId);
  if (!conversation) return;
  try { await api(`/api/conversations/${activeId}`, { method:"PATCH", body:JSON.stringify({ archived:!conversation.archived }) }); els.conversationDialog.close(); if (!conversation.archived) activeId = null; rememberActiveConversation(); await refresh(); }
  catch (error) { toast(error.message); }
};
$("#delete-conversation").onclick = async () => {
  const conversation = state.conversations.find((item) => item.id === activeId);
  if (!conversation || !window.confirm(`永久删除对话“${conversation.title}”及其全部消息和共享产物？此操作无法撤销。`)) return;
  try { await api(`/api/conversations/${activeId}`, { method:"DELETE", body:"{}" }); activeId = null; rememberActiveConversation(); els.conversationDialog.close(); await refresh(); }
  catch (error) { toast(error.message); }
};
function openSkillEditor(skill = null) {
  $("#skill-id").value = skill?.id || "";
  $("#skill-name").value = skill?.name || "";
  $("#skill-description").value = skill?.description || "";
  $("#skill-content").value = skill?.content || "";
  $("#skill-dialog-title").textContent = skill ? "编辑 Skill" : "新建 Skill";
  els.skillDialog.showModal();
  setTimeout(() => $("#skill-name").focus(), 40);
}
$("#new-skill").onclick = () => openSkillEditor();
$("#import-skill").onclick = () => $("#skill-import").click();
$("#skill-import").onchange = async (event) => {
  const file = event.target.files?.[0];
  if (!file) return;
  const form = new FormData();
  form.append("file", file);
  try {
    const result = await api("/api/skills/import", { method:"POST", body:form });
    toast(`已导入 ${result.skills.length} 个 Skill`);
    await refresh();
  } catch (error) { toast(error.message); }
  finally { event.target.value = ""; }
};
els.skillList.onclick = async (event) => {
  const view = event.target.closest("[data-view-skill]");
  if (view) {
    try {
      const skill = await api(`/api/skills/${view.dataset.viewSkill}`);
      $("#skill-preview-title").textContent = skill.name;
      $("#skill-preview-source").textContent = `${skill.source} · ${skill.sourceType}`;
      $("#skill-preview-meta").textContent = skill.location || skill.description;
      $("#skill-preview-content").textContent = skill.content;
      els.skillPreviewDialog.showModal();
    } catch (error) { toast(error.message); }
    return;
  }
  const edit = event.target.closest("[data-edit-skill]");
  if (edit) {
    try { openSkillEditor(await api(`/api/skills/${edit.dataset.editSkill}`)); }
    catch (error) { toast(error.message); }
    return;
  }
  const remove = event.target.closest("[data-delete-skill]");
  if (!remove) return;
  const skill = state.skills.find((item) => item.id === remove.dataset.deleteSkill);
  if (!skill || !window.confirm(`删除 Skill “${skill.name}”？所有 Agent 的分配也会移除。`)) return;
  try { await api(`/api/skills/${skill.id}`, { method:"DELETE", body:"{}" }); await refresh(); }
  catch (error) { toast(error.message); }
};
$("#skill-form").onsubmit = async (event) => {
  if (event.submitter?.value === "cancel") return;
  event.preventDefault();
  const id = $("#skill-id").value;
  const payload = { name:$("#skill-name").value, description:$("#skill-description").value, content:$("#skill-content").value };
  try {
    await api(id ? `/api/skills/${id}` : "/api/skills", { method:id ? "PATCH" : "POST", body:JSON.stringify(payload) });
    els.skillDialog.close();
    await refresh();
  } catch (error) { toast(error.message); }
};
els.skillAssignments.onchange = async (event) => {
  const checkbox = event.target.closest("[data-agent][data-skill-id]");
  if (!checkbox || !activeId) return;
  const agent = checkbox.dataset.agent;
  const skillIds = [...els.skillAssignments.querySelectorAll("[data-agent][data-skill-id]")].filter((item) => item.dataset.agent === agent && item.checked).map((item) => item.dataset.skillId);
  try { await api(`/api/conversations/${activeId}/skills`, { method:"PATCH", body:JSON.stringify({ agent, skillIds }) }); await refresh(); }
  catch (error) { toast(error.message); await refresh(); }
};
$("#synthesize-round").onclick = () => { if (activeId) els.reviewDialog.showModal(); };
$("#peer-review-round").onclick = async () => {
  if (!activeId) return;
  try { await api(`/api/conversations/${activeId}/review`, { method:"POST", body:JSON.stringify({ mode:"peer" }) }); await refresh(); }
  catch (error) { toast(error.message); }
};
$("#review-form").onsubmit = async (event) => {
  if (event.submitter?.value === "cancel") return;
  event.preventDefault();
  try {
    await api(`/api/conversations/${activeId}/review`, { method:"POST", body:JSON.stringify({ mode:"synthesize", agent:els.reviewAgent.value }) });
    els.reviewDialog.close(); await refresh();
  } catch (error) { toast(error.message); }
};
els.composer.onsubmit = async (event) => {
  event.preventDefault();
  if (!activeId) return;
  const isRunning = state.activeConversations?.includes(activeId) || state.typing.some((item) => item.conversationId === activeId);
  if (isRunning) {
    try {
      await api(`/api/conversations/${activeId}/cancel`, { method:"POST", body:"{}" });
      if (!els.input.value.trim() && lastSubmitted.has(activeId)) {
        els.input.value = lastSubmitted.get(activeId);
        els.input.dispatchEvent(new Event("input"));
      }
      els.input.focus();
      await refresh();
    }
    catch (error) { toast(error.message); }
    return;
  }
  const text = els.input.value.trim(); if (!text) return;
  lastSubmitted.set(activeId, text);
  els.input.value = ""; els.input.style.height = "auto";
  try { await api(`/api/conversations/${activeId}/messages`, { method:"POST", body:JSON.stringify({ body:text }) }); await refresh(); }
  catch (error) { els.input.value = text; toast(error.message); }
};
els.input.oninput = () => { els.input.style.height = "auto"; els.input.style.height = `${Math.min(150, els.input.scrollHeight)}px`; };
els.input.onkeydown = (event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); els.composer.requestSubmit(); } };
els.routeStatus.onclick = (event) => { const button = event.target.closest("[data-mention]"); if (button) { els.input.value += `${button.dataset.mention} `; els.input.focus(); } };
els.relay.onchange = async () => {
  if (!activeId || els.relay.disabled) return;
  const desired = els.relay.checked;
  try { await api(`/api/conversations/${activeId}/settings`, { method:"PATCH", body:JSON.stringify({ autoRelay:desired }) }); await refresh(); }
  catch (error) { els.relay.checked = !desired; toast(error.message); }
};
async function updateSettings(patch) {
  if (settingsScope === "conversation" && !activeId) return;
  const endpoint = settingsScope === "global" ? "/api/settings" : `/api/conversations/${activeId}/settings`;
  try { await api(endpoint, { method:"PATCH", body:JSON.stringify(patch) }); await refresh(); }
  catch (error) { toast(error.message); await refresh(); }
}
els.settingRelay.onchange = () => updateSettings({ autoRelay:els.settingRelay.checked });
els.settingReview.onchange = () => updateSettings({ autoReview:els.settingReview.checked });
els.settingRounds.onchange = () => updateSettings({ reviewRounds:Number(els.settingRounds.value) });
els.settingSkillMode.onchange = () => updateSettings({ skillMode:els.settingSkillMode.value });
function updateSkillPermissions() { return updateSettings({ skillPermissions:{ shell:els.settingSkillShell.checked, network:els.settingSkillNetwork.checked, write:els.settingSkillWrite.checked } }).then(loadAudit); }
els.settingSkillShell.onchange = updateSkillPermissions;
els.settingSkillNetwork.onchange = updateSkillPermissions;
els.settingSkillWrite.onchange = updateSkillPermissions;

$("#auth-login-tab").onclick = () => setAuthMode("login");
$("#auth-register-tab").onclick = () => setAuthMode("register");
els.authForm.onsubmit = async (event) => {
  event.preventDefault();
  const submit = $("#auth-submit");
  const error = $("#auth-error");
  submit.disabled = true;
  error.hidden = true;
  try {
    const result = await api(`/api/auth/${authMode}`, { method:"POST", body:JSON.stringify({ username:$("#auth-username").value, password:$("#auth-password").value }) });
    $("#auth-password").value = "";
    showApp(result.user);
    connectEvents();
    await refresh();
    loadProviderStatuses();
    loadMaintenanceStatus();
    loadAudit();
  } catch (cause) {
    error.textContent = cause.message;
    error.hidden = false;
  } finally {
    submit.disabled = false;
  }
};
$("#logout").onclick = async () => {
  try { await api("/api/auth/logout", { method:"POST", body:"{}" }); } catch {}
  showAuth();
};

async function bootstrap() {
  const response = await fetch("/api/auth/me");
  if (!response.ok) { showAuth(); return; }
  const result = await response.json();
  showApp(result.user);
  connectEvents();
  await refresh();
  loadProviderStatuses();
  loadMaintenanceStatus();
  loadAudit();
}

setAuthMode("login");
bootstrap().catch(() => showAuth());
