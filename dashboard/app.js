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
const base = 'http://127.0.0.1:8087/v1';
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
  $('restartAPIButton').disabled = !running;
  $('refreshModelsBtn').disabled = !signed || logging;
  $('accountFact').textContent = signed ? status.account.email || '已登录' : '未登录';
  $('expiryFact').textContent = signed ? new Date(status.account.expires_at).toLocaleString('zh-CN',{hour12:false}) : '—';
  $('baseURL').textContent = status.base_url || base;
  $('serviceAddress').textContent = `127.0.0.1:${status.api_port ?? 8087}`;
  $('portFact').textContent = String(status.api_port ?? 8087);
  $('bindFact').textContent = status.lan_enabled ? '0.0.0.0（局域网）' : '127.0.0.1（仅本机）';
  if (document.activeElement !== $('apiPort')) $('apiPort').value = String(status.api_port ?? 8087);
  if (document.activeElement !== $('lanSwitch')) $('lanSwitch').checked = !!status.lan_enabled;
  $('lanURL').textContent = status.lan_enabled ? (status.lan_base_url || '未检测到可用 .local 主机名') : '局域网访问已关闭';
  $('copyLanBtn').disabled = !status.lan_enabled || !status.lan_base_url;
  const ips = Array.isArray(status.lan_ips) ? status.lan_ips : [];
  $('lanHint').textContent = status.lan_enabled
    ? `局域网 IP：${ips.length ? ips.map(ip=>`http://${ip}:${status.api_port}/v1`).join(' · ') : '未检测到'}。.local 需要 Bonjour/mDNS；普通 HTTP 不加密，请勿映射到公网或在不可信网络使用。`
    : '局域网已关闭，只有运行 Provider 的电脑可以访问 Agent API。';
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
$('saveKeyBtn').addEventListener('click', () => perform('保存 Key', async () => {
  const secret = $('customKey').value;
  if (secret.length < 16 || secret.length > 256 || /[^\x21-\x7E]/.test(secret)) {
    throw Error('请填写 16–256 个不含空格的英文可打印字符');
  }
  if (!confirm('确定更换 Agent API Key？旧 Key 将立即失效，已有 Agent 需要修改配置。')) return;
  await call('key',{method:'POST',body:{key:secret}});
  keyCache = null; keyShown = false;
  $('keyValue').textContent = '•••• •••• •••• ••••';
  $('customKey').value = '';
  toast('已保存新 Key。请更新所有 Agent 的 API Key 配置');
}));
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
$('saveNetworkBtn').addEventListener('click', () => perform('保存端口', async () => {
  const port = Number($('apiPort').value);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw Error('端口必须在 1–65535 之间');
  await call('settings', {method:'POST',body:{port,lan:$('lanSwitch').checked}});
  await fetchStatus();
  toast('网络设置已保存。API 运行时已自动切换到新监听地址');
}));
$('copyLanBtn').addEventListener('click', () => perform('复制局域网地址', async () => {
  await copyText(status?.lan_base_url);
}));
$('restartAPIButton').addEventListener('click', () => perform('重启 API', async () => {
  await call('restart-api', {method:'POST',body:{}});
  await fetchStatus(); toast('API 已重启');
}));
$('restartProgramBtn').addEventListener('click', () => {
  if (!confirm('重启整个 Provider？正在执行的 Agent 请求会中断。')) return;
  perform('重启程序', async () => {
    await call('restart', {method:'POST',body:{}});
    lock(); toast('程序正在重启，管理网页会重新打开并解锁');
  });
});
$('quitProgramBtn').addEventListener('click', () => {
  if (!confirm('彻底退出 Provider？局域网 Agent 将无法连接，直到再次启动程序。')) return;
  perform('退出程序', async () => {
    await call('quit', {method:'POST',body:{}});
    lock(); toast('已发送退出请求，稍后可重新双击 start.command 启动');
  });
});
$('lockBtn').addEventListener('click', () => perform('锁定', async () => {
  await call('session/logout', {method:'POST',body:{}}); lock(); toast('控制台已锁定；服务仍可保持运行');
}));
$('retryUnlock').addEventListener('click', unlock);
setInterval(async () => {if (unlocked && !busy) { try {await fetchStatus();} catch (_) {/* locked by fetchStatus */} }}, 3000);
unlock();
