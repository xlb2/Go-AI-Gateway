"use strict";
let conversations = [], conversation = null, conversationLoading = false, storedRunning = false;
let conversationTimer = null, conversationLoadID = 0, pendingTurn = null;
function savedSelection() {
  try { return JSON.parse(sessionStorage.getItem("knowledge.selection")) || {}; } catch { return {}; }
}
function rememberConversation() {
  sessionStorage.setItem("knowledge.selection", JSON.stringify({library:library?.id, conversation:conversation?.id}));
}
function resetConversations() {
  clearTimeout(conversationTimer); conversationLoadID++;
  conversation = null; conversations = []; conversationLoading = false; storedRunning = false; pendingTurn = null;
  $("conversations").replaceChildren(); $("moreConversations").hidden = true;
}
function renderConversations() {
  $("conversations").replaceChildren(...conversations.map(conv => item(conv.title, "", conversation?.id === conv.id, () => selectConversation(conv))));
}
async function loadConversations(more = false, choose = false) {
  if (!library) return;
  const epoch = revision, preferred = savedSelection();
  const data = await api(`/api/v1/libraries/${library.id}/conversations?before=${more ? conversations.at(-1)?.id || 0 : 0}`);
  if (epoch !== revision) return;
  conversations = more ? conversations.concat(data.items) : data.items;
  $("moreConversations").hidden = data.items.length < 50; renderConversations();
  if (choose && !conversation) {
    const selected = preferred.library === library.id && preferred.conversation;
    const found = conversations.find(conv => conv.id === selected);
    if (found) await selectConversation(found);
    else if (selected) {
      try { await selectConversation({id:selected, library_id:library.id}); }
      catch (err) { if (conversations.length) await selectConversation(conversations[0]); else throw err; }
    } else if (conversations.length) await selectConversation(conversations[0]);
  }
}
async function selectConversation(conv) {
  revision++; clearTimeout(conversationTimer); conversationLoadID++;
  resetChat(); conversation = conv; pendingTurn = null; storedRunning = false;
  conversationLoading = true; chatBusy(false); renderConversations(); toggleSidebar(false);
  try { await reloadConversation(); }
  finally { if (conversation?.id === conv.id) { conversationLoading = false; chatBusy(!!chatController); } }
}
async function reloadConversation() {
  if (!library || !conversation) return;
  const epoch = revision, load = ++conversationLoadID;
  const data = await api(`/api/v1/libraries/${library.id}/conversations/${conversation.id}`);
  if (epoch !== revision || load !== conversationLoadID) return;
  conversation = {...data.conversation, turns:data.turns};
  const index = conversations.findIndex(conv => conv.id === conversation.id);
  if (index >= 0) conversations[index] = data.conversation;
  else conversations.unshift(data.conversation);
  storedRunning = data.turns.some(turn => turn.status === "running");
  const labels = {running:"正在生成",failed:"生成失败",canceled:"已取消",interrupted:"生成中断，未确认完成"};
  chatHistory = data.turns.flatMap(turn => [
    {role:"user", content:turn.question, references:JSON.parse(turn.sources_json || "[]")},
    {role:"assistant", content:turn.answer, evidence:turn.evidence || [], status:turn.status === "completed" ? "" : labels[turn.status] || turn.status}
  ]);
  if (pendingTurn && data.turns.some(turn => turn.request_id === pendingTurn.request_id)) pendingTurn = null;
  rememberConversation(); renderChat(); renderConversations(); chatBusy(!!chatController);
  clearTimeout(conversationTimer);
  if (storedRunning && !chatController) conversationTimer = setTimeout(() => {
    reloadConversation().catch(err => notice(err.message));
  },2000);
}
async function createConversation() {
  if (!library) { toggleSidebar(true); $("libraryForm").hidden = false; $("libraryName").focus(); throw new Error("请先创建或选择资料库。"); }
  const epoch = revision;
  const conv = await api(`/api/v1/libraries/${library.id}/conversations`,{method:"POST"});
  if (epoch !== revision) return;
  conversations.unshift(conv); await selectConversation(conv);
  if (conversation?.id !== conv.id || library?.id !== conv.library_id) return;
  showPanel(true); $("chatInput").focus();
  return conv;
}
