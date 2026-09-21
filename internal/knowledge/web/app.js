"use strict";
const $ = id => document.getElementById(id);
let token = sessionStorage.getItem("knowledge.token") || "";
let library = null, libraries = [], sources = [], selection = 0, revision = 0;
let chatController = null, chatHistory = [], attachments = [];
let uploading = false;
let readerRequest = 0;
function resetChat() {
  chatController?.abort(); chatController = null;
  chatHistory = [];
  attachments = []; $("chatInput").value = "";
  $("chatInput").rows = 2;
  chatBusy(false); renderChat(); renderAttachments();
}
function chatBusy(busy) {
  $("sendChat").disabled = busy || uploading || conversationLoading || storedRunning; $("sendChat").hidden = busy; $("stopChat").hidden = !busy;
  $("clearChat").disabled = busy; $("chatFile").disabled = busy; $("chatSource").disabled = busy;
  $("uploadButton").disabled = busy || uploading;
  $("chatInput").disabled = busy || conversationLoading || storedRunning;
  $("messages").setAttribute("aria-busy", String(busy));
  $("generationState").textContent = busy || storedRunning ? "正在生成…" : conversationLoading ? "加载对话…" : uploading ? "正在上传…" : "";
}
function renderChat(draft = []) {
  const scroll = $("conversationScroll");
  const atBottom = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 100;
  $("chatEmpty").hidden = chatHistory.length + draft.length > 0;
  $("messages").hidden = !$("chatEmpty").hidden;
  $("emptyContext").textContent = library ? library.name : "选择或新建一个资料库";
  $("scopeLabel").textContent = library ? library.name : "尚未选择资料库";
  $("messages").replaceChildren(...chatHistory.concat(draft).map(message => {
    const entry = document.createElement("div"); entry.className = "message " + message.role;
    const name = document.createElement("div"); name.className = "message-heading";
    if (message.role === "assistant") {
      const avatar = document.createElement("span"); avatar.className = "assistant-avatar"; avatar.textContent = "K"; avatar.setAttribute("aria-hidden","true"); name.append(avatar);
    }
    name.append(document.createTextNode(message.role === "user" ? "你" : "资料助手"));
    const content = document.createElement("div"); content.className = "message-content"; content.textContent = message.content;
    content.hidden = !message.content && !!message.status;
    entry.append(name, content);
    if (message.status) {
      const state = document.createElement("p"); state.className = "subtle"; state.textContent = message.status; entry.append(state);
    }
    if (message.references?.length) {
      const refs = document.createElement("p"); refs.className = "subtle";
      refs.textContent = message.references.map(ref => ref.title + " · v" + ref.version).join(" / "); entry.append(refs);
    }
    if (message.evidence?.length) {
      const refs = document.createElement("div"); refs.className = "reading-evidence";
      const label = document.createElement("p"); label.className = "subtle"; label.textContent = "已读取资料"; refs.append(label);
      const libraryID = library.id, conversationID = conversation.id;
      for (const ref of message.evidence) {
        const link = document.createElement("button"); link.type = "button"; link.className = "evidence-link";
        link.textContent = `${ref.title} · v${ref.version} · 第 ${ref.offset + 1}–${ref.next_offset} 字符`;
        link.title = "查看已读取的原文片段";
        link.onclick = () => action(link, () => readEvidence(libraryID, conversationID, ref));
        refs.append(link);
      }
      entry.append(refs);
    }
    if (message.content && !draft.includes(message)) {
      const tools = document.createElement("div"); tools.className = "message-tools";
      const copy = document.createElement("button"); copy.className = "icon-button"; copy.type = "button"; copy.textContent = "⧉"; copy.title = "复制消息"; copy.setAttribute("aria-label","复制消息");
      copy.onclick = () => action(copy, async () => { await navigator.clipboard.writeText(message.content); notice("已复制"); });
      tools.append(copy); entry.append(tools);
    }
    return entry;
  }));
  if (atBottom || draft.length === 0) scroll.scrollTop = scroll.scrollHeight;
}
function renderAttachments() {
  $("attachments").replaceChildren(...attachments.map(source => {
    const button = document.createElement("button"); button.type = "button";
    button.textContent = source.title + " ×"; button.title = "移除附件";
    button.onclick = () => { attachments = attachments.filter(item => item.id !== source.id); renderAttachments(); };
    return button;
  }));
}
function attach(source) {
  if (attachments.some(item => item.id === source.id)) return;
  if (attachments.length >= 4) throw new Error("每次最多附加 4 份资料。");
  attachments.push(source); renderAttachments();
}
function showPanel(chat) {
  $("filesPanel").hidden = chat;
  $("filesTab").setAttribute("aria-expanded", String(!chat));
  $("chatPanel").inert = !chat && window.matchMedia("(max-width:900px)").matches;
}
function notice(text = "") { $("notice").textContent = text; }
function authView() { if (!token) { resetConversations(); resetChat(); sessionStorage.removeItem("knowledge.selection"); toggleSidebar(false); } $("auth").hidden = !!token; $("workspace").hidden = !token; $("logout").hidden = !token; }
function logout() { token = ""; sessionStorage.removeItem("knowledge.token"); revision++; library = null; libraries = []; sources = []; selection = 0; $("libraries").replaceChildren(); $("sources").replaceChildren(); $("body").textContent = "选择一份资料开始阅读。"; $("documentTitle").textContent = "原文"; $("documentMeta").textContent = ""; $("libraryTitle").textContent = "选择资料库"; $("importForm").reset(); $("importForm").hidden = true; $("moreLibraries").hidden = true; $("moreSources").hidden = true; $("password").value = ""; renderSources(); authView(); }
async function api(path, options = {}) {
  const headers = new Headers(options.headers);
  const requestToken = token;
  if (token) headers.set("Authorization", "Bearer " + token);
  const response = await fetch(path, {...options, headers});
  const data = await response.json();
  if (!response.ok) {
    if (response.status === 401 && requestToken === token && !path.includes("/user/")) logout();
    throw new Error(data.error || "请求失败");
  }
  return data;
}
async function action(button, work) {
  button.disabled = true; notice();
  try { await work(); } catch (err) { notice(err.message || "连接失败"); }
  finally { button.disabled = false; }
}
function item(title, detail, selected, click) {
  const button = document.createElement("button"); button.className = "item" + (selected ? " selected" : "");
  button.type = "button"; button.textContent = title;
  if (detail) { const small = document.createElement("small"); small.textContent = detail; button.append(small); }
  button.onclick = () => action(button, click); return button;
}
function renderLibraries() {
  $("libraries").replaceChildren(...libraries.map(lib => item(lib.name, "", library?.id === lib.id, () => selectLibrary(lib))));
}
function renderSources() {
  $("chatSource").replaceChildren(new Option("添加资料", ""), ...sources.map(src => new Option(src.title, src.id)));
  $("sourceCount").textContent = sources.length;
  $("attachCurrent").disabled = !selection;
  $("empty").hidden = sources.length > 0;
  $("empty").textContent = library ? "资料库还没有内容。" : "请先创建或选择资料库。";
  $("sources").replaceChildren(...sources.map(src => item(src.title, src.format.toUpperCase() + " · v1", selection === src.id, () => read(src))));
}
async function loadLibraries(more = false) {
  const epoch = revision;
  const data = await api("/api/v1/libraries?after=" + (more ? libraries.at(-1)?.id || 0 : 0));
  if (epoch !== revision || !token) return;
  libraries = more ? libraries.concat(data.items) : data.items;
  $("moreLibraries").hidden = data.items.length < 50; renderLibraries();
  if (!library && libraries.length) await selectLibrary(libraries.find(lib => lib.id === savedSelection().library) || libraries[0]);
}
async function selectLibrary(lib) {
  library = lib; revision++; sources = []; selection = 0;
  resetConversations();
  resetChat();
  toggleSidebar(false);
  $("libraryTitle").textContent = lib.name; $("importForm").hidden = false;
  $("documentTitle").textContent = "原文"; $("documentMeta").textContent = ""; $("body").textContent = "选择一份资料开始阅读。";
  renderLibraries(); renderSources(); await loadSources(); await loadConversations(false,true);
}
async function loadSources(more = false) {
  if (!library) return;
  const epoch = revision;
  const data = await api(`/api/v1/libraries/${library.id}/sources?after=${more ? sources.at(-1)?.id || 0 : 0}`);
  if (epoch !== revision) return;
  sources = more ? sources.concat(data.items) : data.items;
  $("moreSources").hidden = data.items.length < 50; renderSources();
}
async function read(src) {
  const epoch = revision, request = ++readerRequest; selection = src.id; renderSources();
  $("body").textContent = "正在读取…";
  try {
    const doc = await api(`/api/v1/libraries/${src.library_id}/sources/${src.id}`);
    if (epoch !== revision || request !== readerRequest) return;
    $("documentTitle").textContent = doc.source.title;
    $("documentMeta").textContent = doc.source.format.toUpperCase() + " · 版本 " + doc.version.version;
    // Display source text only. Imported markup is never executed as HTML.
    $("body").textContent = doc.version.body;
  } catch (err) { if (epoch === revision && request === readerRequest) $("body").textContent = "读取失败，请重试。"; throw err; }
}
async function readEvidence(libraryID, conversationID, ref) {
  const epoch = revision, request = ++readerRequest;
  selection = 0; renderSources(); showPanel(false);
  $("documentTitle").textContent = ref.title;
  $("documentMeta").textContent = `版本 ${ref.version} · 第 ${ref.offset + 1}–${ref.next_offset} 字符 · 已读取片段`;
  $("body").textContent = "正在读取…";
  $("documentTitle").tabIndex = -1; $("documentTitle").focus();
  $("documentTitle").scrollIntoView({block:"nearest"});
  try {
    const reading = await api(`/api/v1/libraries/${libraryID}/conversations/${conversationID}/turns/${ref.turn_id}/evidence/${ref.event_id}`);
    if (epoch !== revision || request !== readerRequest) return;
    $("body").textContent = reading.content;
  } catch (err) {
    if (epoch !== revision || request !== readerRequest) return;
    $("body").textContent = "读取失败，请重新打开来源。"; throw err;
  }
}
$("loginForm").onsubmit = event => { event.preventDefault(); action(event.submitter, async () => {
  const data = await api("/api/v1/user/login", {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify({username:$("username").value,password:$("password").value})});
  token = data.token; sessionStorage.setItem("knowledge.token", token); $("password").value = ""; authView(); await loadLibraries();
}); };
$("register").onclick = () => action($("register"), async () => {
  if (!$("loginForm").reportValidity()) return;
  await api("/api/v1/user/register", {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({username:$("username").value,password:$("password").value})});
  notice("注册成功，请登录。");
});
$("logout").onclick = () => { logout(); notice(); };
$("libraryForm").onsubmit = event => { event.preventDefault(); action(event.submitter, async () => {
  const epoch = revision;
  const lib = await api("/api/v1/libraries", {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({name:$("libraryName").value})});
  if (epoch !== revision || !token) return;
  $("libraryName").value = ""; $("libraryForm").hidden = true; await loadLibraries(); if (!token) return; if (!libraries.some(item => item.id === lib.id)) libraries.push(lib); await selectLibrary(lib);
}); };
$("importForm").onsubmit = event => { event.preventDefault(); action(event.submitter, async () => {
  const file = $("file").files[0]; if (!file || !library) return;
  if (file.size > 2*1024*1024) throw new Error("文件超过 2 MiB。");
  const epoch = revision, target = library.id;
  const data = new FormData(); data.append("file", file); data.append("title", $("sourceTitle").value);
  const result = await api(`/api/v1/libraries/${target}/sources`, {method:"POST",body:data});
  if (epoch !== revision) return;
  $("importForm").reset(); notice(result.duplicate ? "相同内容已存在，已打开原资料。" : "导入完成。");
  await loadSources(); await read(result.source);
}); };
$("refresh").onclick = () => action($("refresh"), async () => { await loadLibraries(); await loadSources(); await loadConversations(); if (!chatController) await reloadConversation(); });
$("moreLibraries").onclick = () => action($("moreLibraries"), () => loadLibraries(true));
$("moreSources").onclick = () => action($("moreSources"), () => loadSources(true));
$("filesTab").onclick = () => { showPanel(!$("filesPanel").hidden); if (!$("filesPanel").hidden) $("chatTab").focus(); };
$("chatTab").onclick = () => { showPanel(true); $("filesTab").focus(); };
$("moreConversations").onclick = () => action($("moreConversations"), () => loadConversations(true));
$("newLibrary").onclick = () => { $("libraryForm").hidden = !$("libraryForm").hidden; if (!$("libraryForm").hidden) $("libraryName").focus(); };
$("uploadButton").onclick = () => $("chatFile").click();
$("attachCurrent").onclick = () => {
  try { const src = sources.find(src => src.id === selection); if (src) { attach(src); showPanel(true); $("chatInput").focus(); } }
  catch (err) { notice(err.message); }
};
function toggleSidebar(open) {
  const mobile = window.matchMedia("(max-width:640px)").matches;
  $("workspace").classList.toggle("nav-open",open); $("sidebarBackdrop").hidden = !open;
  $("openSidebar").setAttribute("aria-expanded", String(open));
  $("sidebar").inert = mobile && !open;
  document.querySelector(".main-column").inert = mobile && open;
}
$("openSidebar").onclick = () => { toggleSidebar(true); $("closeSidebar").focus(); };
$("closeSidebar").onclick = $("sidebarBackdrop").onclick = () => { toggleSidebar(false); $("openSidebar").focus(); };
window.matchMedia("(max-width:640px)").addEventListener("change", () => { toggleSidebar(false); $("sidebar").inert = window.matchMedia("(max-width:640px)").matches; });
window.matchMedia("(max-width:900px)").addEventListener("change", () => showPanel($("filesPanel").hidden));
document.addEventListener("keydown", event => {
  if (event.key === "Escape") {
    const navOpen = $("workspace").classList.contains("nav-open");
    showPanel(true); toggleSidebar(false);
    (navOpen ? $("openSidebar") : $("filesTab")).focus();
  }
  if (event.key === "Tab" && $("workspace").classList.contains("nav-open")) {
    const focusable = [...$("sidebar").querySelectorAll('a, button:not(:disabled), input')].filter(el => el.getClientRects().length);
    const first = focusable[0], last = focusable.at(-1);
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  }
});
$("chatInput").onkeydown = event => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) { event.preventDefault(); if (!chatController && !uploading) $("chatForm").requestSubmit(); }
};
$("chatInput").oninput = () => { $("chatInput").rows = Math.min(6, Math.max(2, $("chatInput").value.split("\n").length)); };
$("clearChat").onclick = () => action($("clearChat"), createConversation);
$("stopChat").onclick = () => chatController?.abort();
$("chatSource").onchange = () => {
  try { const src = sources.find(src => src.id === Number($("chatSource").value)); if (src) attach(src); }
  catch (err) { notice(err.message); }
  $("chatSource").value = "";
};
$("chatFile").onchange = () => action($("chatFile"), async () => {
  const file = $("chatFile").files[0]; if (!file) return;
  if (!library) throw new Error("请先创建或选择资料库。");
  if (attachments.length >= 4) throw new Error("每次最多附加 4 份资料。");
  if (file.size > 2*1024*1024) throw new Error("文件超过 2 MiB。");
  const epoch = revision, target = library.id, data = new FormData(); data.append("file", file);
  uploading = true; chatBusy(!!chatController);
  try {
    const result = await api(`/api/v1/libraries/${target}/sources`, {method:"POST", body:data});
    if (epoch !== revision) return;
    attach(result.source); $("chatFile").value = ""; await loadSources();
  } finally { uploading = false; chatBusy(!!chatController); }
});
$("chatForm").onsubmit = async event => {
  event.preventDefault();
  if (chatController || uploading || conversationLoading || storedRunning) return;
  if (!library) { notice("请先创建或选择资料库。"); return; }
  const content = $("chatInput").value.trim(); if (!content) return;
  if (!conversation) {
    const attached = attachments.slice();
    conversationLoading = true; chatBusy(false);
    try { if (!await createConversation()) return; }
    catch (err) { notice(err.message); return; }
    finally { conversationLoading = false; chatBusy(false); }
    if (!conversation) return;
    attachments = attached; renderAttachments();
    $("chatInput").value = content;
  }
  const controller = new AbortController(), epoch = revision, target = library.id;
  chatController = controller; chatBusy(true); notice();
  const user = {role:"user", content}, assistant = {role:"assistant", content:""};
  renderChat([user, assistant]);
  let complete = false;
  const ids = attachments.map(src => src.id);
  if (!pendingTurn || pendingTurn.content !== content || JSON.stringify(pendingTurn.source_ids) !== JSON.stringify(ids)) {
    pendingTurn = {request_id:crypto.randomUUID(), expected_turn_id:conversation.turns?.at(-1)?.id || 0, content, source_ids:ids};
  }
  try {
    const response = await fetch(`/api/v1/libraries/${target}/conversations/${conversation.id}/turns`, {
      method:"POST", signal:controller.signal,
      headers:{"Authorization":"Bearer " + token, "Content-Type":"application/json"},
      body:JSON.stringify(pendingTurn)
    });
    if (!response.ok) {
      const error = await response.json();
      if (response.status === 401 && epoch === revision) logout();
      throw new Error(error.error || "对话请求失败");
    }
    if (response.headers.get("Content-Type")?.includes("application/json")) {
      const result = await response.json();
      notice("该请求已记录，已读取原状态。");
      if (result.turn?.status === "completed") $("chatInput").value = "";
      return;
    }
    const reader = response.body.getReader(), decoder = new TextDecoder();
    let pending = "";
    while (true) {
      const {value, done} = await reader.read();
      if (epoch !== revision) return;
      pending += decoder.decode(value, {stream:!done});
      let newline;
      while ((newline = pending.indexOf("\n")) >= 0) {
        const line = pending.slice(0, newline); pending = pending.slice(newline + 1);
        if (!line) continue;
        const item = JSON.parse(line);
        if (item.type === "error") throw new Error(item.error);
        if (item.type === "delta") { assistant.content += item.content; renderChat([user, assistant]); }
        if (item.type === "done") complete = true;
      }
      if (done) break;
    }
    if (!complete) throw new Error("连接中断，回复不完整。");
    pendingTurn = null;
    $("chatInput").value = ""; $("chatInput").rows = 2; renderChat();
  } catch (err) {
    controller.abort();
    if (epoch === revision) {
      notice(err.name === "AbortError" ? "已停止，当前回复未加入后续对话。" : err.message);
      assistant.content += "\n[回复未完成]"; renderChat([user, assistant]);
    }
  } finally {
    if (chatController === controller) {
      chatController = null; chatBusy(false);
      try { await reloadConversation(); await loadConversations(); }
      catch { notice("会话状态读取失败，请刷新核对，勿重复发送。"); }
    }
  }
};
toggleSidebar(false);
authView(); if (token) action($("refresh"), () => loadLibraries());
