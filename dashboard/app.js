'use strict';
// Dashboard JS deliberately uses no localStorage/sessionStorage and no external services.
const $ = (id) => document.getElementById(id);
let unlocked = false;
let status = null;
let keyCache = null;
let keyShown = false;
let modelList = [];
let busy = false;
let toastTimer;
const base = `${location.origin}/v1`;
function toast(message) {
  const el = $('toast'); el.textContent = message; el.hidden = false;
  clearTimeout(toastTimer); toastTimer = setTimeout(() => el.hidden = true, 3500);
}
function lock() {
  unlocked = false; status = null; keyCache = null; keyShown = false;
  $('content').hidden = true; $('lockScreen').hidden = false;
}
async function call(path, {method='GET', body}={}) {
  const response = await fetch(`/ui/api/${path}`, {
    method, credentials: 'same-origin', cache: 'no-store',
    headers: {'Content-Type':'application/json','X-SIWC-Dashboard':'1'},
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  let data = {};
  try { data = await response.json(); } catch (_) { /* default below */ }
  if (response.status === 401) { lock(); throw Error(data?.error?.message || '控制台需要重新解锁'); }
  if (!response.ok) throw Error(data?.error?.message || `HTTP ${response.status}`);
  return data;
}
async function unlock() {
  const fragment = new URLSearchParams(location.hash.slice(1));
  const secret = fragment.get('unlock');
  // Remove the fragment immediately, before any other navigation or request.
  if (location.hash) history.replaceState(null, '', location.pathname + location.search);
  if (secret) {
    try { await call('session', {method:'POST',body:{key:secret}}); }
    catch (err) { lock(); toast(`解锁失败：${err.message}`); return; }
  }
  try {
    unlocked = true;
    await fetchStatus();
    $('lockScreen').hidden = true;
    $('content').hidden = false;
  } catch (_) { lock(); }
}
async function fetchStatus() {
  if (!unlocked) return;
  status = await call('status');
  paintStatus();
}
function paintStatus() {
  if (!status) return;
  const signed = !!status.signed_in, running = !!status.api_running, logging = !!status.login_busy;
  $('accountBadge').textContent = signed ? '● 已授权 ChatGPT' : logging ? '◌ 授权进行中' : '○ 未授权';
  $('accountBadge').className = 'pill ' + (signed ? 'good' : 'muted');
  $('serviceBadge').textContent = running ? '● API 运行中' : '○ API 已停止';
  $('serviceBadge').className = 'pill ' + (running ? 'good' : 'muted');
  $('loginState').textContent = signed ? status.account.email || '授权成功' : logging ? '等待浏览器授权…' : '尚未登录';
  $('loginDetail').textContent = signed ? '已获得授权，凭据保存在当前电脑。' : logging ? '请在打开的 OpenAI 页面完成登录。' : status.login_error || '首次授权需要浏览器确认。';
  $('loginBtn').disabled = logging;
  $('loginBtn').textContent = logging ? '授权进行中…' : signed ? '重新授权 ChatGPT ↗' : '使用 ChatGPT 登录 ↗';
  $('logoutBtn').hidden = !signed;
  $('logoutBtn').disabled = logging;
  const link = $('oauthLink'); link.hidden = !(logging && status.login_url);
  if (!link.hidden) link.href = status.login_url;
  else link.removeAttribute('href');
  $('serviceState').textContent = running ? '服务运行中' : 'API 已停止';
  $('serviceDetail').textContent = running ? '你的本地 Agent 可以连接。' : signed ? '点击下方按钮启动模型 API。' : '请先完成账号授权。';
  $('toggleServiceBtn').disabled = !signed || logging;
  $('toggleServiceBtn').textContent = running ? '停止 API 服务 ■' : '启动 API 服务 →';
  $('refreshModelsBtn').disabled = !signed || logging;
  $('accountFact').textContent = signed ? status.account.email || '已登录' : '未登录';
  $('expiryFact').textContent = signed ? new Date(status.account.expires_at).toLocaleString('zh-CN',{hour12:false}) : '—';
  $('baseURL').textContent = status.base_url || base;
  try { $('portFact').textContent = new URL(status.base_url).port; } catch (_) {}
}
async function perform(label, fn) {
  if (busy) return;
  busy = true;
  try { await fn(); } catch (err) { toast(`${label}失败：${err.message}`); }
  finally { busy = false; }
}
$('loginBtn').addEventListener('click', () => perform('授权', async () => {
  await call('login', {method:'POST', body:{}});
  toast('已启动授权流程，请查看弹出的官方登录页面');
  await fetchStatus();
}));
$('logoutBtn').addEventListener('click', () => {
  if (!confirm('确定退出 ChatGPT 授权？本机 API 服务将停止。')) return;
  perform('退出登录', async () => {
    await call('logout', {method:'POST', body:{}});
    modelList = []; updateModels(); keyCache = null;
    await fetchStatus(); toast('已退出当前 ChatGPT 授权');
  });
});
$('toggleServiceBtn').addEventListener('click', () => perform('切换服务', async () => {
  await call('service',{method:'POST',body:{enabled:!status.api_running}});
  await fetchStatus(); toast(status.api_running ? 'API 已启动，可连接 Agent' : 'API 已停止');
}));
async function loadKey() {
  if (keyCache === null) keyCache = (await call('key')).key;
  return keyCache;
}
$('revealKeyBtn').addEventListener('click', () => perform('读取 Key', async () => {
  keyShown = !keyShown;
  $('keyValue').textContent = keyShown ? await loadKey() : '•••• •••• •••• ••••';
  $('revealKeyBtn').setAttribute('aria-label',keyShown?'隐藏本地 Key':'显示本地 Key');
}));
async function copyText(content) {
  if (!content) { toast('暂无可复制的内容'); return; }
  await navigator.clipboard.writeText(content);
  toast('已复制到剪贴板');
}
$('copyKeyBtn').addEventListener('click', () => perform('复制 Key', async () => {await copyText(await loadKey());}));
function updateModels() {
  $('modelCount').textContent = modelList.length ? `${modelList.length} 个可用模型` : '未找到可用模型';
  $('modelDetail').textContent = modelList.length ? '从当前授权账号实时返回。' : '请确认账号的模型使用资格。';
  const sel = $('modelSelect'), chosen = sel.value;
  sel.replaceChildren();
  if (modelList.length === 0) {const o=new Option('请先查询模型目录','');sel.add(o);return;}
  for (const item of modelList) sel.add(new Option(item,item));
  if (modelList.includes(chosen)) sel.value = chosen;
}
$('refreshModelsBtn').addEventListener('click', () => perform('查询模型', async () => {
  $('modelCount').textContent = '查询中…';
  try {
    const result = await call('models');
    modelList = Array.isArray(result.models) ? result.models : [];
    updateModels(); toast(`查询完成：${modelList.length} 个模型`);
  } catch(err) { $('modelCount').textContent='查询失败'; $('modelDetail').textContent=err.message; throw err; }
}));
for (const btn of document.querySelectorAll('[data-copy]')) btn.addEventListener('click', () => perform('复制', async () => {
  const field=btn.dataset.copy;
  const value=field==='base' ? (status?.base_url || base) : field==='key' ? await loadKey() : $('modelSelect').value;
  await copyText(value);
}));
$('copyConfigBtn').addEventListener('click', () => perform('复制配置', async () => {
  const model=$('modelSelect').value;
  if (!model) {toast('请先刷新并选择一个可用模型');return;}
  const config=`base_url: ${status?.base_url || base}\napi_key: ${await loadKey()}\nmodel: ${model}`;
  await copyText(config);
}));
$('lockBtn').addEventListener('click', () => perform('锁定', async () => {
  await call('session/logout', {method:'POST',body:{}}); lock(); toast('控制台已锁定；服务仍可保持运行');
}));
$('retryUnlock').addEventListener('click', unlock);
setInterval(async () => {if (unlocked && !busy) { try {await fetchStatus();} catch (_) {/* locked by fetchStatus */} }}, 3000);
unlock();
