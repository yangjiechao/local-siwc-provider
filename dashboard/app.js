'use strict';
// Dashboard JS deliberately uses no localStorage/sessionStorage and no external services.
const $ = (id) => document.getElementById(id);
let unlocked = false;
let status = null;
let keyCache = null;
let modelList = [];
let configuredProviders = [];
// The DOM is refreshed from server data; open panels are a UI preference,
// not provider config. Keep them stable across catalog polls and reorders.
const expandedProviderModels = new Set();
const providerModelScroll = new Map();
let providersHaveRendered = false;
let siwcRenderFingerprint = null;
let siwcModels = [];
let siwcDisabledModels = [];
let siwcSignedIn = false;
let siwcCatalogOK = false;
let siwcCatalogError = '';
let modelsLoaded = false;
let updateInfo = null;
let busy = false;
let toastTimer;
const base = 'http://127.0.0.1:8087/v1';
function toast(message) {
  const el = $('toast'); el.textContent = message; el.hidden = false;
  clearTimeout(toastTimer); toastTimer = setTimeout(() => el.hidden = true, 3500);
}
function lock() {
  unlocked = false; status = null; keyCache = null;
  $('agentKeyRows').replaceChildren();
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
    await loadAgentKeys();
    await loadProviderPresets();
    await loadProviders();
    $('lockScreen').hidden = true;
    $('content').hidden = false;
    // Show the last known version immediately; GitHub check happens separately.
    // Version checking must never block the rest of the dashboard.
    refreshUpdateStatus().then(() => checkUpdates())
      .catch(err => toast(`检查更新失败：${err.message}`));
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
  $('serviceDetail').textContent = running ? '你的本地 Agent 可以连接。' : signed || status?.external_providers ? '点击下方按钮启动模型 API。' : '请先完成账号授权或配置额外模型商。';
  $('toggleServiceBtn').disabled = (!signed && !(status?.external_providers > 0)) || logging;
  $('toggleServiceBtn').textContent = running ? '停止 API 服务 ■' : '启动 API 服务 →';
  $('restartAPIButton').disabled = !running;
  $('refreshModelsBtn').disabled = (!signed && !(status?.external_providers > 0)) || logging;
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
    modelList = []; modelsLoaded = false; updateModels(); keyCache = null;
    await fetchStatus();await loadProviders();toast('已退出当前 ChatGPT 授权');
  });
});
$('toggleServiceBtn').addEventListener('click', () => perform('切换服务', async () => {
  await call('service',{method:'POST',body:{enabled:!status.api_running}});
  await fetchStatus(); toast(status.api_running ? 'API 已启动，可连接 Agent' : 'API 已停止');
}));
async function loadKey() {
  if (keyCache === null) keyCache = (await call('key')).key;
  if (!keyCache) throw Error('请先创建 Key，或补全已有 Key');
  return keyCache;
}
async function copyText(content) {
  if (!content) { toast('暂无可复制的内容'); return; }
  await navigator.clipboard.writeText(content);
  toast('已复制到剪贴板');
}
async function loadAgentKeys() {
  const data = await call('agent-keys');
  const list = $('agentKeyRows'); list.replaceChildren();
  if (!data.keys.length) { list.textContent = '暂无 Key，请在下方添加。'; return; }
  for (const item of data.keys) {
    const row = document.createElement('div'); row.className = 'agent-key-row';
    const info = document.createElement('div');
    const name = document.createElement('strong'); name.textContent = item.name;
    const value = document.createElement('code'); value.className = 'agent-key-value'; value.textContent = item.available ? '•••• •••• •••• ••••' : '原值未保存，请点更换并填写原 Key';
    info.append(name,value);
    const button = label => { const el = document.createElement('button'); el.type='button'; el.className='btn outline'; el.textContent=label; return el; };
    const reveal = async () => (await call('agent-keys/reveal', {method:'POST',body:{id:item.id}})).key;
    const show = button('显示'); let shown = false;
    show.addEventListener('click', () => perform('显示 Key', async () => {
      if (!item.available) throw Error('原值未保存，请点更换并填写原 Key');
      shown = !shown; value.textContent = shown ? await reveal() : '•••• •••• •••• ••••'; show.textContent = shown ? '隐藏' : '显示';
    }));
    const copy = button('复制'); copy.addEventListener('click', () => perform('复制 Key', async () => copyText(await reveal())));
    const edit = document.createElement('details'); edit.className='agent-key-edit';
    const summary = document.createElement('summary'); summary.textContent='更换';
    const input = document.createElement('input'); input.className='text-field'; input.type='password'; input.autocomplete='new-password'; input.maxLength=256; input.placeholder='填写 16–256 个不含空格的 ASCII 字符';
    const save = button('保存');
    save.addEventListener('click', () => perform('更换 Key', async () => {
      const secret = input.value;
      if (secret.length < 16 || secret.length > 256 || /[^\x21-\x7E]/.test(secret)) throw Error('请填写 16–256 个不含空格的英文可打印字符');
      if (!confirm(`保存“${item.name}”的 Key？若内容改变，原 Key 将立即失效。`)) return;
      await call('agent-keys/update',{method:'POST',body:{id:item.id,key:secret}}); keyCache=null; input.value=''; await loadAgentKeys(); toast('Key 已保存');
    }));
    edit.append(summary,input,save);
    const revoke = button('删除');
    revoke.addEventListener('click', () => {
      if (!confirm(`确定删除“${item.name}”的 Key？使用该 Key 的应用将立即无法访问。`)) return;
      perform('删除 Key', async () => {
        await call('agent-keys/revoke', {method:'POST', body:{id:item.id}});
        keyCache=null; await loadAgentKeys(); toast('Key 已删除');
      });
    });
    row.append(info,show,copy,edit,revoke); list.append(row);
  }
}
$('agentKeyForm').addEventListener('submit', event => {
  event.preventDefault();
  perform('生成 Key', async () => {
    const secret = $('agentKeyCustom').value;
    if (secret && (secret.length < 16 || secret.length > 256 || /[^\x21-\x7E]/.test(secret))) throw Error('自定义 Key 须为 16–256 个不含空格的英文可打印字符');
    await call('agent-keys', {method:'POST', body:{name:$('agentKeyName').value.trim(), key:secret}});
    keyCache=null;
    $('agentKeyName').value = ''; $('agentKeyCustom').value = ''; await loadAgentKeys();
    toast('Key 已添加，可以在列表中显示或复制');
  });
});
function renderModelRows() {
  const list = $('modelRows');
  list.replaceChildren();
  const query = $('modelSearch').value.trim().toLocaleLowerCase();
  const filtered = modelList.filter(id => id.toLocaleLowerCase().includes(query));
  const hint = $('modelListHint');
  if (!modelsLoaded) {
    hint.textContent = '刷新模型列表后，这里将显示可用模型。';
    return;
  }
  if (!modelList.length) {
    hint.textContent = '当前授权账号没有返回可列出的模型。';
    return;
  }
  hint.textContent = query ? `显示 ${filtered.length} / ${modelList.length} 个匹配模型` : `已获取 ${modelList.length} 个模型，点击“复制”获取完整 ID。`;
  if (!filtered.length) {
    const empty = document.createElement('li');
    empty.className = 'model-empty';
    empty.textContent = '没有匹配的模型，请尝试其他关键词。';
    list.append(empty);
    return;
  }
  for (const id of filtered) {
    const row = document.createElement('li');
    row.className = 'model-entry';
    const code = document.createElement('code');
    code.textContent = id; // Never interpolate upstream model names as HTML.
    code.title = id;
    const actions = document.createElement('div');
    actions.className = 'model-entry-actions';
    const select = document.createElement('button');
    select.type = 'button';
    select.className = 'model-action';
    select.textContent = $('modelSelect').value === id ? '已选用' : '选用';
    select.disabled = $('modelSelect').value === id;
    select.setAttribute('aria-label', `选用模型 ${id}`);
    select.addEventListener('click', () => {
      $('modelSelect').value = id;
      renderModelRows();
      toast(`已选择模型：${id}`);
    });
    const copy = document.createElement('button');
    copy.type = 'button';
    copy.className = 'model-action copy-model';
    copy.textContent = '复制 ⧉';
    copy.setAttribute('aria-label', `复制模型 ID ${id}`);
    copy.addEventListener('click', () => perform('复制模型', () => copyText(id)));
    actions.append(select, copy);
    row.append(code, actions);
    list.append(row);
  }
}
function updateModels() {
  $('modelCount').textContent = !modelsLoaded ? '尚未查询' : modelList.length ? `${modelList.length} 个可用模型` : '未找到可用模型';
  $('modelDetail').textContent = !modelsLoaded ? '登录或配置模型商后点击刷新。' : modelList.length ? '可在模型列表中查看完整 ID。' : '请确认 SIWC 账号或额外模型商的模型权限。';
  const sel = $('modelSelect'), chosen = sel.value;
  sel.replaceChildren();
  if (modelList.length === 0) sel.add(new Option('请先查询模型目录',''));
  else {
    for (const item of modelList) sel.add(new Option(item,item));
    if (modelList.includes(chosen)) sel.value = chosen;
  }
  $('modelSearch').disabled = !modelsLoaded || modelList.length === 0;
  $('copyAllModelsBtn').disabled = !modelList.length;
  renderModelRows();
}
$('modelSearch').addEventListener('input', renderModelRows);
$('modelSelect').addEventListener('change', renderModelRows);
$('copyAllModelsBtn').addEventListener('click', () => perform('复制模型列表', () => copyText(modelList.join('\n'))));
$('refreshModelsBtn').addEventListener('click', () => perform('查询模型', async () => {
  $('modelCount').textContent = '查询中…';
  $('modelListHint').textContent = '正在查询 SIWC 与额外模型商目录…';
  try {
    const result = await call('models');
    modelList = Array.isArray(result.models) ? result.models.filter(id => typeof id === 'string' && id.length > 0) : [];
    modelsLoaded = true;
    updateModels(); await loadProviders(false); toast(`查询完成：${modelList.length} 个模型`);
  } catch(err) {
    $('modelCount').textContent='查询失败';
    $('modelDetail').textContent=err.message;
    $('modelListHint').textContent='查询失败：' + err.message;
    throw err;
  }
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

// Source update controls use authenticated loopback-only dashboard endpoints.
function paintUpdate() {
  if (!updateInfo) return;
  const u = updateInfo;
  $('localVersion').textContent = u.local_version || '未知版本';
  $('localCommit').textContent = u.local_commit ? `提交：${u.local_commit.slice(0,12)}` : '当前构建未内嵌 Git 提交号';
  $('githubVersion').textContent = u.latest_commit ? u.latest_commit.slice(0,12) : '尚未查询';
  $('githubDate').textContent = u.latest_date ? `提交日期：${u.latest_date.slice(0,10)}` : '基于 GitHub main 分支';
  const labels = {unchecked:'尚未检查', checking:'检查中', current:'已是最新', available:'发现新版本', manual:'需手动更新', updating:'正在更新', restarting:'即将重启', ahead:'本地领先', diverged:'分支分叉', error:'检查失败'};
  $('updateBadge').textContent = labels[u.status] || '版本信息';
  $('updateMessage').textContent = u.message || '等待检查更新';
  $('checkUpdateBtn').disabled = !!u.busy || u.status === 'checking';
  $('applyUpdateBtn').disabled = !u.can_update || !!u.busy;
  $('applyUpdateBtn').textContent = u.busy ? '正在更新…' : '更新到最新版 ↗';
}
async function refreshUpdateStatus() {
  if (!unlocked) return;
  updateInfo = await call('update');
  paintUpdate();
}
async function checkUpdates() {
  if (!unlocked) return;
  if (updateInfo) { updateInfo.status = 'checking'; updateInfo.can_update = false; paintUpdate(); }
  updateInfo = await call('update/check', {method:'POST',body:{}});
  paintUpdate();
}
$('checkUpdateBtn').addEventListener('click', () => perform('检查更新', checkUpdates));
$('applyUpdateBtn').addEventListener('click', () => {
  if (!updateInfo?.can_update) return;
  const local = (updateInfo.local_commit || '').slice(0,7);
  const latest = (updateInfo.latest_commit || '').slice(0,7);
  if (!confirm(`从 ${local} 更新到 ${latest}？\n\n更新将先测试和编译新程序，然后重启 Provider，正在执行的 Agent 请求可能中断。`)) return;
  perform('执行更新', async () => {
    await call('update/apply', {method:'POST',body:{}});
    await refreshUpdateStatus();
    toast('正在执行更新，请留意进度信息。');
  });
});
setInterval(async () => {
  if (unlocked && !busy && updateInfo?.busy) {
    try {await refreshUpdateStatus();}catch (_) {/* app may be restarting */}
  }
}, 1400);

// External providers use a server-provided, fixed preset catalog.
// Never store or display vendor secrets, or construct icon URLs from user data.
let providerPresets = [];
let editingProvider = null;
const LOCAL_KIND = 'local-openai-compatible';
const CUSTOM_CHOICES = {
  'custom-cloud': {kind:'openai-compatible', label:'自定义云端 OpenAI 兼容 API'},
  'custom-local': {kind:LOCAL_KIND, label:'自定义本地或局域网模型服务'},
  'custom-video': {kind:'ark-video', label:'自定义 Ark Video API'}
};
function findPreset(id) { return providerPresets.find(p=>p.id===id) || null; }
function resolvedPreset(item) {
  if (item.preset && findPreset(item.preset)) return item.preset;
  const match=providerPresets.find(p=>p.kind===item.kind && p.base_url===item.base_url);
  if (match) return match.id;
  return item.kind===LOCAL_KIND?'custom-local':item.kind==='ark-video'?'custom-video':'custom-cloud';
}
function makeBrandLogo(name, fallback) {
  const logo=document.createElement('span');logo.className='provider-avatar';
  logo.setAttribute('aria-hidden','true');
  const letter=document.createElement('span');letter.className='provider-avatar-fallback';
  letter.textContent=(fallback||'?').trim().slice(0,2).toUpperCase() || '?';
  logo.append(letter);
  const known=['openai','deepseek','openrouter','groq','xai','qwen','volcengine','ollama','lmstudio','vllm'];
  if (known.includes(name)) {
    const img=document.createElement('img');img.alt='';img.loading='lazy';
    img.src='/ui/assets/logos/'+name+'.svg';
    img.addEventListener('error',()=>{img.hidden=true;letter.hidden=false;});
    letter.hidden=true;logo.prepend(img);
  }
  return logo;
}
function paintPresetPreview() {
  const choice=$('providerPreset').value;
  const preset=findPreset(choice);
  const custom=CUSTOM_CHOICES[choice];
  const isCustom=!!custom;
  const kind=preset?.kind || custom?.kind || '';
  $('providerKind').value=kind;
  $('providerBaseField').hidden=!isCustom;
  $('providerBase').required=isCustom;
  $('providerPresetPreview').hidden=!preset;
  if (preset) {
    $('providerPresetTitle').textContent=preset.name;
    $('providerPresetURL').textContent=preset.base_url;
    const holder=$('providerPresetLogo');holder.replaceChildren();
    holder.replaceWith(makeBrandLogo(preset.logo,preset.name));
    // Replace with an element carrying the stable ID for the next selection.
    $('providerPresetPreview').querySelector('.provider-avatar').id='providerPresetLogo';
    $('providerBase').value=preset.base_url;
  }
  const oldURL=editingProvider?.base_url || '';
  $('providerKey').required=kind!==LOCAL_KIND && (!editingProvider || (preset?.base_url||$('providerBase').value)!==oldURL);
  $('providerKey').placeholder=kind===LOCAL_KIND?'本地服务器无认证时可留空':'新建必填；编辑保持原地址时可留空';
  $('providerSecurityHint').textContent=kind===LOCAL_KIND
    ? '本地服务无需付费云端 API Key。仅允许明确指定的回环/私有网络地址；若连接另一台电脑，请在「自定义本地」填写其私有 IP:端口，并保证网络可信。'
    :kind==='ark-video'
      ? '视频任务需通过 Ark Video API 调用，具体模型和地域以服务商官方权限为准。'
      :kind==='openai-compatible'
        ? '云端服务只允许公网 HTTPS，使用该厂商独立 API Key 和计费规则，不会自动切换到 SIWC。'
        :'请先选择模型商。';
}
function resetProviderForm() {
  editingProvider=null;
  $('providerForm').reset();
  $('providerId').readOnly=false;
  $('providerKey').value='';
  $('providerKey').required=false;
  $('saveProviderBtn').textContent='保存模型商';
  $('providerEnabled').checked=true;
  $('providerBase').value='';
  $('providerKind').value='';
  $('providerBaseField').hidden=true;
  $('providerPresetPreview').hidden=true;
}
function openProviderDialog(item=null) {
  resetProviderForm();
  editingProvider=item;
  if (item) {
    $('providerPreset').value=resolvedPreset(item);
    $('providerId').value=item.id;$('providerId').readOnly=true;
    $('providerName').value=item.name;
    $('providerBase').value=item.base_url;
    $('providerModels').value=(item.manual_models||[]).join('\n');
    $('providerEnabled').checked=item.enabled;
    $('providerDialogTitle').textContent='编辑模型商 · '+item.name;
    $('saveProviderBtn').textContent='保存修改';
  } else {
    $('providerDialogTitle').textContent='新增模型商';
  }
  paintPresetPreview();
  // Legacy records that happen to have a known URL keep their saved data.
  if (item) $('providerBase').value=item.base_url;
  $('providerDialog').showModal();
  $('providerPreset').focus();
}
function closeProviderDialog() {
  if ($('providerDialog').open) $('providerDialog').close();
}
function providerButton(label,fn) {
  const button=document.createElement('button');button.type='button';
  button.className='btn outline';button.textContent=label;
  button.addEventListener('click',fn);return button;
}
function selectProviderPreset(value) {
  const preset=findPreset(value);
  if (!editingProvider) {
    if (preset) {
      $('providerId').value=preset.id;
      $('providerName').value=preset.name;
    } else if (CUSTOM_CHOICES[value]) {
      $('providerId').value='';
      $('providerName').value='';
    }
  }
  if (CUSTOM_CHOICES[value]) {
    $('providerBase').value=editingProvider?.base_url || '';
  }
  paintPresetPreview();
}
async function loadProviderPresets() {
  const el=$('providerPreset');
  try {
    const data=await call('provider-presets');
    providerPresets=Array.isArray(data.presets)?data.presets:[];
  } catch(err) {
    providerPresets=[];
    toast('预设列表加载失败，可以使用自定义地址：'+err.message);
  }
  el.replaceChildren();
  const placeholder=document.createElement('option');
  placeholder.value='';placeholder.textContent='请选择模型商或本地服务…';el.append(placeholder);
  for (const category of [{id:'cloud',title:'云端模型厂商'},{id:'local',title:'本地推理服务'}]) {
    const group=document.createElement('optgroup');group.label=category.title;
    for (const p of providerPresets.filter(p=>p.category===category.id)) {
      const option=document.createElement('option');option.value=p.id;option.textContent=p.name;
      group.append(option);
    }
    if (category.id==='cloud') {
      for (const id of ['custom-cloud','custom-video']) {
        const option=document.createElement('option');option.value=id;option.textContent='＋ '+CUSTOM_CHOICES[id].label;
        group.append(option);
      }
    } else {
      const option=document.createElement('option');option.value='custom-local';
      option.textContent='＋ '+CUSTOM_CHOICES['custom-local'].label;group.append(option);
    }
    el.append(group);
  }
}
// Reordering is persisted by the server; the inference router reads this same
// sequence. Filtering must not hide neighbors during a reorder operation.
async function moveProviderPriority(id, offset) {
  const index=configuredProviders.findIndex(p=>p.id===id);
  const next=index+offset;
  if (index<0||next<0||next>=configuredProviders.length) return;
  const ids=configuredProviders.map(p=>p.id);
  [ids[index],ids[next]]=[ids[next],ids[index]];
  await call('providers/reorder',{method:'POST',body:{ids}});
  await loadProviders(false);
  toast('已保存优先级：同名模型将匹配列表里靠前的模型商');
}

// Missing persisted opt-out flags mean enabled. Newly discovered models
// therefore start checked without requiring additional writes or migrations.
function modelCheckbox(providerID, model, disabledModels) {
  const label=document.createElement('label');label.className='model-toggle-item';
  const input=document.createElement('input');input.type='checkbox';
  input.checked=!disabledModels.includes(model);
  input.setAttribute('aria-label','启用 '+providerID+' 的模型 '+model);
  const modelText=document.createElement('code');modelText.textContent=model;
  label.append(input,modelText);
  input.addEventListener('change',()=>{
    const enabled=input.checked;
    if (busy) { input.checked=!enabled;return; }
    perform('设置模型',async()=>{
      input.disabled=true;
      try {
        await call('models/enable',{method:'POST',body:{provider:providerID,model,enabled}});
        await loadProviders();
        toast(providerID+' / '+model+(enabled?' 已启用':' 已停用')+'，路由规则已生效');
      } catch(err) {
        input.checked=!enabled;
        input.disabled=false;
        throw err;
      }
    });
  });
  return label;
}

function renderSIWCModelToggles() {
  const container=$('siwcModelToggleList');
  container.replaceChildren();
  const tag=$('siwcModelStatus');
  if (!siwcSignedIn) {
    tag.textContent='未登录';tag.className='provider-status disabled';
    const note=document.createElement('p');note.className='model-toggle-empty';
    note.textContent='登录 SIWC 后会自动显示发现的模型，并默认全部勾选。退出登录时可继续使用下方外部模型商。';
    container.append(note);return;
  }
  if (!siwcCatalogOK) {
    tag.textContent='目录不可用';tag.className='provider-status disabled';
    const note=document.createElement('p');note.className='model-toggle-empty';
    note.textContent='SIWC 模型目录暂不可用：'+(siwcCatalogError||'请点击刷新模型列表')+'。出于计费安全，无前缀模型暂不自动切换外部厂商。';
    container.append(note);return;
  }
  const disabled=new Set(siwcDisabledModels);
  const enabledCount=siwcModels.filter(m=>!disabled.has(m)).length;
  tag.textContent=enabledCount+'/'+siwcModels.length+' 已启用';
  tag.className='provider-status enabled';
  if (!siwcModels.length) {
    const note=document.createElement('p');note.className='model-toggle-empty';
    note.textContent='当前 SIWC 账号没有返回可管理的模型。';container.append(note);
    return;
  }
  for(const model of siwcModels) {
    container.append(modelCheckbox('siwc',model,siwcDisabledModels));
  }
}

function captureExpandedProviderModels() {
  // Capture the currently rendered list before replacing nodes. Preserve
  // remembered state for filtered-out providers until they become visible.
  for (const panel of $('providerList').querySelectorAll('details.provider-model-details[data-provider-id]')) {
    const id=panel.dataset.providerId;
    if (panel.open) expandedProviderModels.add(id);
    else expandedProviderModels.delete(id);
    const grid=panel.querySelector('.model-toggle-grid');
    if (grid) providerModelScroll.set(id,grid.scrollTop);
  }
}

function renderProviders() {
  const host=$('providerList');
  captureExpandedProviderModels();
  host.replaceChildren();
  const count=configuredProviders.length,enabled=configuredProviders.filter(x=>x.enabled).length;
  $('providerSummary').textContent=count?count+' 个模型商 · '+enabled+' 个已启用':'尚未配置额外模型商';
  const q=$('providerSearch').value.trim().toLocaleLowerCase();
  const filtered=configuredProviders.filter(x=>(x.id+' '+x.name+' '+x.kind+' '+x.base_url).toLocaleLowerCase().includes(q));
  const canReorder=q.length===0;
  $('providerSearch').title=canReorder?'输入名称可筛选，清空搜索可调整优先级':'清空搜索才能调整完整的模型商顺序';
  if (!filtered.length) {
    const p=document.createElement('p');p.className='provider-empty';
    p.textContent=count?'没有符合搜索条件的模型商。':'暂无额外模型商，点击「新增模型商」添加。';
    host.append(p);return;
  }
  for (const item of filtered) {
    const row=document.createElement('div');row.className='provider-entry';
    const info=document.createElement('div');info.className='provider-identity';
    const titleArea=document.createElement('div');titleArea.className='provider-identity-text';
    const name=document.createElement('div');name.className='name';
    name.textContent=item.name+' ('+item.id+')';
    const priority=document.createElement('span');priority.className='provider-priority';
    priority.textContent='P'+(configuredProviders.findIndex(p=>p.id===item.id)+1);
    priority.title='SIWC 始终最优先，这里显示外部供应商顺序';
    name.prepend(priority);
    const badge=document.createElement('span');badge.className='provider-status '+(item.enabled?'enabled':'disabled');
    badge.textContent=item.enabled?'已启用':'已禁用';name.append(badge);
    const meta=document.createElement('div');meta.className='details';
    const label=item.kind===LOCAL_KIND?'本地推理':item.kind==='ark-video'?'视频 API':'云端 API';
    meta.textContent=label+' · '+item.base_url+' · '+(item.models?.length||0)+' 个模型 · '+(item.kind===LOCAL_KIND&&!item.has_api_key?'无需 Key':item.has_api_key?'Key 已保存':'缺少 Key');
    titleArea.append(name,meta);
    if (item.discovery_error) {
      const warn=document.createElement('div');warn.className='error';
      warn.textContent='目录获取失败：'+item.discovery_error+'。可以手动填写模型 ID。';
      titleArea.append(warn);
    }
    if (Array.isArray(item.models)&&item.models.length) {
      const models=document.createElement('details');models.className='provider-model-details';
      models.dataset.providerId=item.id;
      models.open=expandedProviderModels.has(item.id);
      models.addEventListener('toggle',()=>{
        if(models.open) expandedProviderModels.add(item.id);
        else expandedProviderModels.delete(item.id);
      });
      const disabled=Array.isArray(item.disabled_models)?item.disabled_models:[];
      const activeCount=item.models.filter(m=>!disabled.includes(m)).length;
      const summary=document.createElement('summary');
      summary.textContent='模型开关：'+activeCount+' / '+item.models.length+' 已启用';
      const hint=document.createElement('p');hint.className='provider-model-hint';
      hint.textContent='默认全部勾选。取消后，未指定厂商的请求会继续寻找下一个已启用该模型的厂商。';
      const values=document.createElement('div');values.className='model-toggle-grid';
      for (const m of item.models) {
        values.append(modelCheckbox(item.id,m,disabled));
      }
      models.append(summary,hint,values);titleArea.append(models);
    }
    const preset=findPreset(resolvedPreset(item));
    info.append(makeBrandLogo(preset?.logo,item.name),titleArea);
    const actions=document.createElement('div');actions.className='provider-actions';
    const ordering=document.createElement('div');ordering.className='provider-order-actions';
    ordering.setAttribute('role','group');
    ordering.setAttribute('aria-label','调整 '+item.name+' 的优先级');
    const index=configuredProviders.findIndex(p=>p.id===item.id);
    const up=providerButton('↑',()=>perform('提高模型商优先级',()=>moveProviderPriority(item.id,-1)));
    const down=providerButton('↓',()=>perform('降低模型商优先级',()=>moveProviderPriority(item.id,1)));
    up.classList.add('provider-order-button');down.classList.add('provider-order-button');
    up.title='上移 '+item.name;down.title='下移 '+item.name;
    up.setAttribute('aria-label',up.title);down.setAttribute('aria-label',down.title);
    up.disabled=!canReorder||index===0;
    down.disabled=!canReorder||index===configuredProviders.length-1;
    ordering.append(up,down);actions.append(ordering);
    actions.append(providerButton(item.enabled?'禁用':'启用',()=>perform('切换模型商',async()=>{
      const next=!item.enabled;
      await call('providers',{method:'POST',body:{
        id:item.id,name:item.name,kind:item.kind,base_url:item.base_url,preset:item.preset||'',
        api_key:'',enabled:next,manual_models:item.manual_models||[]
      }});
      await loadProviders();await fetchStatus();
      toast('模型商 '+item.name+' 已'+(next?'启用':'禁用'));
    })));
    actions.append(providerButton('编辑',()=>openProviderDialog(item)));
    actions.append(providerButton('删除',()=>{
      if(!confirm('确定删除模型商 '+item.name+'？其保存的 API Key 也将移除。'))return;
      perform('删除模型商',async()=>{
        await call('providers/delete',{method:'POST',body:{id:item.id}});
        await loadProviders();toast('已删除 '+item.id);
      });
    }));
    row.append(info,actions);host.append(row);
    // Restore scroll only after insertion, so the expanded grid has layout.
    const expandedGrid=row.querySelector('details.provider-model-details[open] .model-toggle-grid');
    if (expandedGrid) expandedGrid.scrollTop=providerModelScroll.get(item.id)||0;
  }
}
async function loadProviders(withModels=true) {
  if (!unlocked) return;
  const data=await call('providers');
  const nextProviders=Array.isArray(data.providers)?data.providers:[];
  const providersChanged=JSON.stringify(configuredProviders)!==JSON.stringify(nextProviders);
  configuredProviders=nextProviders;
  siwcModels=Array.isArray(data.siwc_models)?data.siwc_models:[];
  siwcDisabledModels=Array.isArray(data.siwc_disabled_models)?data.siwc_disabled_models:[];
  siwcSignedIn=!!data.siwc_signed_in;
  siwcCatalogOK=!!data.siwc_catalog_ok;
  siwcCatalogError=data.siwc_error||'';
  const nextSIWCFingerprint=JSON.stringify([siwcModels,siwcDisabledModels,siwcSignedIn,siwcCatalogOK,siwcCatalogError]);
  if(nextSIWCFingerprint!==siwcRenderFingerprint){
    renderSIWCModelToggles();
    siwcRenderFingerprint=nextSIWCFingerprint;
  }
  // Most 15-second polls return identical provider data. Avoid replacing
  // checkboxes or details at all when nothing changed.
  if(!providersHaveRendered||providersChanged){
    renderProviders();
    providersHaveRendered=true;
  }
  // Expansions of removed providers must not linger forever.
  const existingIDs=new Set(configuredProviders.map(p=>p.id));
  for(const id of expandedProviderModels) if(!existingIDs.has(id)) expandedProviderModels.delete(id);
  for(const id of providerModelScroll.keys()) if(!existingIDs.has(id)) providerModelScroll.delete(id);
  if (withModels&&Array.isArray(data.models)){
    modelList=data.models.map(x=>x.id).filter(Boolean);
    modelsLoaded=true;updateModels();
  }
}
$('addProviderBtn').addEventListener('click',()=>openProviderDialog());
$('closeProviderBtn').addEventListener('click',closeProviderDialog);
$('resetProviderBtn').addEventListener('click',closeProviderDialog);
$('providerDialog').addEventListener('close',resetProviderForm);
$('providerPreset').addEventListener('change',event=>selectProviderPreset(event.target.value));
$('providerBase').addEventListener('input',()=>{
  if (editingProvider) $('providerKey').required=$('providerKind').value!==LOCAL_KIND && $('providerBase').value!==editingProvider.base_url;
});
$('providerSearch').addEventListener('input',renderProviders);
$('providerForm').addEventListener('submit',event=>{
  event.preventDefault();
  perform('保存模型商',async()=>{
    const id=$('providerId').value.trim();
    const presetID=$('providerPreset').value;
    const preset=findPreset(presetID);
    const custom=CUSTOM_CHOICES[presetID];
    if (!preset&&!custom) throw Error('请选择模型商');
    const kind=preset?.kind||custom.kind;
    const baseURL=preset?.base_url||$('providerBase').value.trim();
    const payload={
      id,name:$('providerName').value.trim(),kind,
      preset:preset?.id||'custom',base_url:baseURL,
      api_key:$('providerKey').value.trim(),
      enabled:$('providerEnabled').checked,
      manual_models:$('providerModels').value.split(/\r?\n/).map(s=>s.trim()).filter(Boolean)
    };
    await call('providers',{method:'POST',body:payload});
    closeProviderDialog();await loadProviders();await fetchStatus();
    toast('模型商 '+id+' 已保存，后台正在同步模型目录');
  });
});
$('refreshProvidersBtn').addEventListener('click',()=>perform('刷新模型',async()=>{
  const data=await call('providers/refresh',{method:'POST',body:{}});
  modelList=Array.isArray(data.models)?data.models.map(m=>m.id).filter(Boolean):[];
  modelsLoaded=true;updateModels();await loadProviders(false);
  toast('刷新完成，共 '+modelList.length+' 个模型');
}));

// The backend refreshes model catalogs at startup. Reflect the result without
// requiring a manual click, while avoiding requests during form editing.
setInterval(async()=>{
  if (unlocked && !busy && !$('providerDialog').open) {
    try {await loadProviders();}catch(_) { /* local dashboard may be exiting */ }
  }
},15000);


/* Page-level navigation: each advanced function has its own tab, no storage required. */
let activeDashboardTab = 'overview';
let usageData = {records: [], keys: {}, prices: {}};
function positionUsagePopup() {
  const popup = $('usageChartPopup');
  if (popup.hidden) return;
  const chart = $('usageChartWrap').getBoundingClientRect();
  const popupWidth = popup.offsetWidth;
  const anchor = chart.width * Number(popup.dataset.chartX) / $('usageChart').viewBox.baseVal.width;
  popup.style.left = `${Math.max(0, Math.min(chart.width - popupWidth, anchor - popupWidth / 2))}px`;

  popup.style.maxHeight = '';
  const height = popup.offsetHeight;
  const above = chart.top - 16;
  const below = window.innerHeight - chart.bottom - 16;
  if (Math.max(above, below) >= Math.min(height, 180)) {
    const showAbove = above >= height || above >= below;
    popup.style.maxHeight = `${Math.min(height, showAbove ? above : below)}px`;
    popup.style.top = showAbove ? 'auto' : 'calc(100% + 8px)';
    popup.style.bottom = showAbove ? 'calc(100% + 8px)' : 'auto';
  } else {
    const top = Math.max(8, Math.min(chart.top, window.innerHeight - height - 8));
    popup.style.top = `${top - chart.top}px`;
    popup.style.bottom = 'auto';
  }
}
window.addEventListener('resize',positionUsagePopup);
// 纵轴上限使用真实时段峰值，中间保留易读刻度，并避开峰值附近的标签重叠。
function usageChartScale(maximum, tokens) {
  const rawStep = (maximum || (tokens ? 4 : 1)) / 4;
  const magnitude = 10 ** Math.floor(Math.log10(rawStep));
  const niceStep = [1,2,2.5,5,10].find(n => n * magnitude >= rawStep) * magnitude;
  const step = tokens ? Math.max(1,Math.ceil(niceStep)) : niceStep;
  const max = maximum || (tokens ? 4 : 1), ticks = [0];
  for (let i=1; i*step<max; i++) {
    if ((max-i*step)/max >= 0.12) ticks.push(i*step);
  }
  ticks.push(max);
  return {step, max, ticks};
}
function usageChartNumber(number, step) {
  if (number && step < 0.0001) return new Intl.NumberFormat('zh-CN', {notation:'scientific',maximumFractionDigits:2}).format(number);
  const scale = number >= 1e9 ? 1e9 : number >= 1e6 ? 1e6 : number >= 1e3 ? 1e3 : 1;
  const digits = Math.max(0, Math.min(12, -Math.floor(Math.log10(step / scale)) + 1));
  return new Intl.NumberFormat('zh-CN', {maximumFractionDigits:digits}).format(number / scale) + (scale === 1e9 ? 'B' : scale === 1e6 ? 'M' : scale === 1e3 ? 'K' : '');
}
let usageChartWidth = 0;
// 图表按实际容器宽度绘制，窄屏只减少时间标签，不缩小文字。
if (typeof ResizeObserver !== 'undefined') new ResizeObserver(() => {
  const width = $('usageChartWrap').clientWidth;
  if (width && Math.abs(width - usageChartWidth) > 1 && activeDashboardTab === 'usage') renderUsage();
}).observe($('usageChartWrap'));
async function loadUsage() {
  usageData = await call('usage');
  usageData.records = (usageData.records || []).map(row => {
    const p = (usageData.official_prices || {})[row.model];
    if ((row.amount != null && row.cost_source === 'upstream') || row.input == null || row.output == null || !p) return row;
    const long = p.long_context && row.input > 272000;
    const hasCached = Number.isSafeInteger(row.cached_input) && row.cached_input >= 0 && row.cached_input <= row.input;
    const cached = hasCached ? row.cached_input : 0;
    const hasWrite = p.cache_writes && Number.isSafeInteger(row.cache_write) && row.cache_write >= 0 && row.cache_write <= row.input-cached;
    const writes = hasWrite ? row.cache_write : 0;
    const hasSplit = hasWrite && Number.isSafeInteger(row.cache_write_30) && Number.isSafeInteger(row.cache_write_12h) && row.cache_write_30 >= 0 && row.cache_write_12h >= 0 && row.cache_write_30 + row.cache_write_12h === writes;
    const shortWrite = hasSplit ? row.cache_write_30 : writes, longWrite = hasSplit ? row.cache_write_12h : 0;
    const complete = hasCached && (!p.cache_writes || hasWrite && (writes === 0 || hasSplit));
    const inputCost = (row.input-cached-writes)*p.input + cached*p.cached + shortWrite*p.input*1.25 + longWrite*p.input*2.5;
    return {...row, amount:(inputCost*(long ? 2 : 1)+row.output*p.output*(long ? 1.5 : 1))/1e6, currency:p.currency, cost_source:complete ? 'official_cached' : 'official'};
  });
  for (const [id, values] of [['usageKey', [...new Set((usageData.records || []).map(r => r.key))]], ['usageModel', [...new Set((usageData.records || []).map(r => r.model))]]]) {
    const select = $(id), previous = select.value;
    select.replaceChildren(new Option('全部', ''));
    for (const value of values.sort()) { const name=id === 'usageKey' ? (usageData.keys[value] || (usageData.records || []).find(r => r.key === value && r.key_name)?.key_name || value) : value; select.add(new Option(id === 'usageKey' && value !== '默认 Key' && value !== '未知 Key' ? `${name}（${value.slice(0,8)}）` : name, value)); }
    select.value = values.includes(previous) ? previous : '';
  }
  const metric = $('usageMetric'), selected = metric.value;
  const currencies = [...new Set((usageData.records || []).filter(r => r.amount != null && r.currency && ['upstream','estimate','official','official_cached'].includes(r.cost_source)).map(r => r.currency + ':' + r.cost_source))].sort();
  metric.replaceChildren(new Option('Token', 'tokens'));
  for (const currency of [...new Set(currencies.map(entry => entry.split(':')[0]))]) metric.add(new Option(`金额（${currency} · 已知合计，含估算）`, currency+':total'));
  for (const entry of currencies) { const [currency, source] = entry.split(':'); metric.add(new Option(`金额（${currency} · ${source === 'official' ? '官方价，缓存明细不全' : source === 'official_cached' ? '官方价，含缓存' : source === 'estimate' ? '配置价估算' : '上游报告'}）`, entry)); }
  metric.value = [...metric.options].some(o => o.value === selected) ? selected : 'tokens';
  const prices = usageData.prices || {};
  $('usageCoverage').textContent = usageData.started_at ? `从 ${new Date(usageData.started_at).toLocaleString('zh-CN',{hour12:false})} 开始记录通过本提供器的推理请求；更早的用量未知，上游未返回用量也标记为未知。` : '记录开始时间未知；上游未返回用量时标记为未知。';
  $('usagePrices').textContent = (Object.keys(prices).length ? '额外配置：' + Object.entries(prices).map(([model,p]) => `${model}：${p.currency} 输入 ${p.input} / 输出 ${p.output}`).join('；') : '尚无额外单价配置') + `。官方标准文本价核验日期：${usageData.official_prices_checked_at || '未知'}；其他模型需核实价格。`;
  renderUsage();
}
function renderUsage() {
  const range = Number($('usageRange').value), now = new Date(), bins = [];
  const started = new Date(usageData.started_at || NaN).getTime();
  const newBin = start => ({start, partial:!Number.isFinite(started) || start.getTime() < started, input:0, output:0, unknown:0, failed:0, pending:0, requests:0, costs:{}, unknownCost:0});
  const groups = {key:new Map(),model:new Map()};
  const add = (bin,row) => { bin.requests++; if (row.input == null || row.output == null) bin.unknown++; else { bin.input+=row.input; bin.output+=row.output; } if (row.status>=400 || row.incomplete) bin.failed++; if (row.task_state==='pending') bin.pending++; if (row.amount == null || !row.currency || !['upstream','estimate','official','official_cached'].includes(row.cost_source)) bin.unknownCost++; else { const k=row.currency+':'+row.cost_source; bin.costs[k]=(bin.costs[k]||0)+row.amount; } };
  if (range === 24) {
    const end = new Date(now); end.setMinutes(0, 0, 0);
    for (let i = 24; i >= 0; i--) bins.push(newBin(new Date(end.getTime() - i * 3600000)));
  } else {
    const day = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    for (let i = range - 1; i >= 0; i--) bins.push(newBin(new Date(day.getFullYear(), day.getMonth(), day.getDate() - i)));
  }
  const modelsByBin = bins.map(() => new Map());
  for (const row of usageData.records || []) {
    if (($('usageKey').value && row.key !== $('usageKey').value) || ($('usageModel').value && row.model !== $('usageModel').value)) continue;
    const t = new Date(row.time).getTime();
    if (!Number.isFinite(t) || t > now.getTime() || range === 24 && t < now.getTime()-24*3600000) continue;
    for (let i = bins.length - 1; i >= 0; i--) if (t >= bins[i].start.getTime()) {
      add(bins[i],row);
      const models = modelsByBin[i];
      if (!models.has(row.model)) models.set(row.model, newBin(bins[i].start));
      add(models.get(row.model), row);
      for (const kind of ['key','model']) { if (!groups[kind].has(row[kind])) groups[kind].set(row[kind],newBin(now)); add(groups[kind].get(row[kind]),row); }
      break;
    }
  }
  const sum = bins.reduce((a,b) => { a.input+=b.input; a.output+=b.output; a.unknown+=b.unknown; a.failed+=b.failed; a.pending+=b.pending; a.requests+=b.requests; a.unknownCost+=b.unknownCost; for (const [k,v] of Object.entries(b.costs)) a.costs[k]=(a.costs[k]||0)+v; return a; }, newBin(now));
  const costsText = b => {
    const totals = new Map();
    for (const [key,value] of Object.entries(b.costs)) { const [currency,source]=key.split(':'); if (!totals.has(currency)) totals.set(currency,{sum:0,parts:[]}); const item=totals.get(currency); item.sum+=value; item.parts.push(`${source === 'official' ? '官方价缓存明细不全' : source === 'official_cached' ? '官方价含缓存' : source === 'estimate' ? '配置价估算' : '上游报告'} ${Number(value.toPrecision(8))}`); }
    return [...[...totals].map(([currency,item])=>`${currency} ${Number(item.sum.toPrecision(8))}（已知合计：${item.parts.join('，')}）`), ...(b.unknownCost ? [`${b.unknownCost} 次金额未知`] : [])].join('；') || '无金额记录';
  };
  $('usageSummary').textContent = `已记录请求 ${sum.requests} 次，已知输入 ${sum.input.toLocaleString()}、输出 ${sum.output.toLocaleString()}、合计 ${(sum.input+sum.output).toLocaleString()} Token；${sum.unknown} 次用量未知（其中 ${sum.pending} 个视频任务待结果），${sum.failed} 次失败或中断。${costsText(sum)}。${bins.some(b=>b.partial) ? '所选范围包含未覆盖时段。' : ''}`;
  const rows = $('usageRows'); rows.replaceChildren();
  for (const bin of [...bins].reverse()) {
    const tr = document.createElement('tr');
    const uncovered = bin.partial && bin.requests === 0;
    const noTokens = uncovered || bin.requests > 0 && bin.unknown === bin.requests;
    for (const value of [bin.start.toLocaleString('zh-CN', {hour12:false}), bin.partial ? '不完整' : '完整', uncovered ? '—' : String(bin.requests), noTokens ? '—' : bin.input.toLocaleString(), noTokens ? '—' : bin.output.toLocaleString(), uncovered ? '—' : String(bin.unknown), uncovered ? '—' : String(bin.failed), uncovered ? '未覆盖' : costsText(bin)]) { const td = document.createElement('td'); td.textContent = value; tr.append(td); }
    rows.append(tr);
  }
  for (const [kind,id] of [['key','usageKeyRows'],['model','usageModelRows']]) {
    const target=$(id); target.replaceChildren();
    for (const [key,group] of [...groups[kind]].sort((a,b)=>String(a[0]).localeCompare(String(b[0])))) {
      const name=kind==='key' ? (usageData.keys[key] || (usageData.records || []).find(r=>r.key===key && r.key_name)?.key_name || key) : key;
      const label=kind==='key' && key!=='默认 Key' && key!=='未知 Key' ? `${name}（${key.slice(0,8)}）` : name;
      const tr=document.createElement('tr'); for (const value of [label,String(group.requests),group.input.toLocaleString(),group.output.toLocaleString(),String(group.unknown),costsText(group)]) { const td=document.createElement('td'); td.textContent=value; tr.append(td); } target.append(tr);
    }
  }
  const chart = $('usageChart'); chart.replaceChildren(); $('usageChartPopup').hidden = true;
  const metric = $('usageMetric').value;
  const isPartial = b => b.partial || (metric === 'tokens' ? b.unknown > 0 : b.unknownCost > 0);
  const value = b => {
    if (b.partial && !b.requests) return null;
    if (metric === 'tokens') return b.requests > 0 && b.unknown === b.requests ? null : b.input+b.output;
    const [currency,source] = metric.split(':');
    const selected = source === 'total' ? Object.entries(b.costs).filter(([key])=>key.startsWith(currency+':')).map(([,v])=>v) : (Object.hasOwn(b.costs,metric) ? [b.costs[metric]] : []);
    return b.unknownCost && !selected.length ? null : selected.reduce((a,b)=>a+b,0);
  };
  const values = bins.map(value), maximum = Math.max(0, ...values.filter(v => v !== null));
  const scale = usageChartScale(maximum, metric === 'tokens');
  const peakLabel = maximum.toLocaleString('zh-CN', metric === 'tokens' ? {maximumFractionDigits:0} : {maximumSignificantDigits:15,notation:maximum && maximum<0.000001 ? 'scientific' : 'standard'});
  const width = Math.max(260, $('usageChartWrap').clientWidth || 720), height = 280;
  usageChartWidth = width;
  chart.setAttribute('viewBox', `0 0 ${width} ${height}`);
  const plot = {left:Math.max(width < 480 ? 58 : 72, peakLabel.length*7+20), right:width - 18, top:12, bottom:214};
  const xAt = i => plot.left + i * (plot.right - plot.left) / (bins.length - 1);
  const yAt = v => plot.bottom - v * (plot.bottom - plot.top) / scale.max;
  const svg = (tag, attrs, text) => {
    const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
    for (const [key, val] of Object.entries(attrs)) el.setAttribute(key, val);
    if (text !== undefined) el.textContent = text;
    chart.append(el); return el;
  };
  const unit = metric === 'tokens' ? 'Token' : metric.split(':')[0];
  $('usageChartTitle').textContent = metric === 'tokens' ? 'Token 用量' : 'API 金额';
  $('usageChartUnit').textContent = `${unit} / ${range === 24 ? '小时' : '天'}${metric === 'tokens' ? ' · 输入 + 输出' : ' · ' + $('usageMetric').selectedOptions[0].textContent}`;
  $('usageChartInterval').textContent = range === 24 ? '过去 24 小时 · 按小时' : `最近 ${range} 天 · 按天`;
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const offset = -now.getTimezoneOffset();
  $('usageChartTimezone').textContent = `${timezone} (UTC${offset >= 0 ? '+' : '−'}${String(Math.floor(Math.abs(offset)/60)).padStart(2,'0')}:${String(Math.abs(offset)%60).padStart(2,'0')})`;
  $('usageChartStart').textContent = (range === 24 ? new Date(now.getTime()-24*3600000) : bins[0].start).toLocaleString('zh-CN',{hour12:false});
  $('usageChartEnd').textContent = now.toLocaleString('zh-CN',{hour12:false});
  const bounds = i => [i === 0 ? plot.left : (xAt(i-1)+xAt(i))/2, i === bins.length-1 ? plot.right : (xAt(i)+xAt(i+1))/2];
  values.forEach((v,i) => {
    if (v !== null) return;
    const [left,right] = bounds(i);
    svg('rect',{x:left,y:plot.top,width:right-left,height:plot.bottom-plot.top,fill:'#f8f9fa'});
    svg('line',{x1:left,x2:right,y1:plot.bottom,y2:plot.bottom,stroke:'#9aa0a6','stroke-dasharray':'3 4'});
  });
  for (const v of scale.ticks) {
    const y=yAt(v), peak=maximum>0 && v===maximum;
    svg('line',{x1:plot.left,x2:plot.right,y1:y,y2:y,class:`usage-chart-grid${v===0 ? ' usage-chart-baseline' : ''}`});
    svg('text',{x:plot.left-12,y:y+4,'text-anchor':'end',class:`usage-chart-tick${peak ? ' usage-chart-peak' : ''}`},peak ? peakLabel : usageChartNumber(v,scale.step));
  }
  const intervals = Math.max(2, Math.min(bins.length-1, Math.floor((plot.right-plot.left)/100)));
  const stride = (range === 24 ? [1,2,3,4,6,8,12,24] : [1,2,3,5,7,10,15,30]).find(n => n >= (bins.length-1)/intervals);
  const timeTicks = [];
  for (let i=0; i<bins.length; i+=stride) timeTicks.push(i);
  if (timeTicks[timeTicks.length-1] !== bins.length-1) {
    if (bins.length-1-timeTicks[timeTicks.length-1] < stride/2) timeTicks.pop();
    timeTicks.push(bins.length-1);
  }
  for (const i of timeTicks) {
    const x=xAt(i), date=bins[i].start;
    const anchor = i===0 ? 'start' : i===bins.length-1 ? 'end' : 'middle';
    svg('line',{x1:x,x2:x,y1:plot.bottom,y2:plot.bottom+5,stroke:'#bdc1c6'});
    svg('text',{x,y:239,'text-anchor':anchor,class:'usage-chart-tick'},date.toLocaleString('zh-CN',range===24 ? {hour:'2-digit',minute:'2-digit',hour12:false} : {month:'2-digit',day:'2-digit'}));
    svg('text',{x,y:257,'text-anchor':anchor,class:'usage-chart-date'},date.toLocaleString('zh-CN',range===24 ? {month:'2-digit',day:'2-digit'} : {weekday:'short'}));
  }
  let points = [];
  const drawLine = () => {
    if (points.length > 1) {
      svg('polygon',{points:`${points[0].split(',')[0]},${plot.bottom} ${points.join(' ')} ${points[points.length-1].split(',')[0]},${plot.bottom}`,class:'usage-chart-area'});
      svg('polyline',{points:points.join(' '),class:'usage-chart-line'});
    }
    points=[];
  };
  const coordinates = values.map((v,i) => v === null ? null : `${xAt(i)},${yAt(v)}`);
  const partial = bins.map(isPartial);
  coordinates.forEach((point,i) => {
    if (point === null || partial[i]) drawLine();
    else points.push(point);
  });
  drawLine();
  for (let i=1; i<coordinates.length; i++) {
    const previous = coordinates[i-1], current = coordinates[i];
    if (previous === null || current === null || !(partial[i-1] || partial[i])) continue;
    svg('polyline',{points:`${previous} ${current}`,class:'usage-chart-line usage-chart-line-partial'});
  }
  bins.forEach((bin,i) => {
    const v=values[i]; if (v===null) return;
    const dot=svg('circle',{cx:xAt(i),cy:yAt(v),r:partial[i] ? 3.5 : 2.5,class:`usage-chart-dot${partial[i] ? ' is-partial' : ''}`});
    const title=document.createElementNS('http://www.w3.org/2000/svg','title'); title.textContent=`${bin.start.toLocaleString('zh-CN')}：${partial[i] ? '部分数据；' : ''}${bin.requests} 次，已知输入 ${bin.input}，已知输出 ${bin.output}，${costsText(bin)}`; dot.append(title);
  });
  if (!sum.requests) svg('text',{x:(plot.left+plot.right)/2,y:(plot.top+plot.bottom)/2,'text-anchor':'middle',class:'usage-chart-tick'},values.every(v=>v===null) ? '该范围尚无已知用量' : '该范围暂无请求');
  const marker=svg('line',{y1:plot.top,y2:plot.bottom,stroke:'#9aa0a6','stroke-dasharray':'3 3','pointer-events':'none',visibility:'hidden'});
  const hits = [];
  let pinned = false;
  const showBin = i => {
    const bin=bins[i], popup=$('usageChartPopup'), details=$('usageChartDetails');
    const begin = range === 24 ? new Date(Math.max(bin.start.getTime(), now.getTime()-24*3600000)) : bin.start;
    const end = new Date(Math.min(i+1<bins.length ? bins[i+1].start.getTime() : now.getTime(), now.getTime()));
    const uncovered=bin.partial && bin.requests===0, noTokens=uncovered || bin.requests>0 && bin.unknown===bin.requests;
    details.replaceChildren();
    const heading=document.createElement('strong'); heading.textContent=`${begin.toLocaleString('zh-CN',{hour12:false})} — ${end.toLocaleString('zh-CN',{hour12:false})}`; details.append(heading);
    for (const line of [uncovered ? '此时段尚未开始记录' : `请求 ${bin.requests} 次${bin.partial ? '（部分覆盖）' : ''}`, noTokens ? 'Token：未知' : `输入 ${bin.input.toLocaleString()} · 输出 ${bin.output.toLocaleString()} · 合计 ${(bin.input+bin.output).toLocaleString()} Token`, uncovered ? '金额：未知' : `金额：${costsText(bin)}`, uncovered ? '' : `用量未知 ${bin.unknown} 次 · 失败或中断 ${bin.failed} 次`]) { if (!line) continue; const item=document.createElement('div'); item.textContent=line; details.append(item); }
    const models = [...modelsByBin[i]].sort(([a, usageA], [b, usageB]) => usageB.requests - usageA.requests || a.localeCompare(b));
    if (models.length) {
      const title = document.createElement('h4');
      title.textContent = '按模型统计';
      const list = document.createElement('ul');
      list.className = 'usage-popup-models';
      for (const [model, usage] of models) {
        const item = document.createElement('li');
        const name = document.createElement('strong');
        name.textContent = `${model} · ${usage.requests} 次请求`;
        const tokens = document.createElement('div');
        tokens.textContent = usage.unknown === usage.requests
          ? 'Token：未知'
          : `输入 ${usage.input.toLocaleString()} · 输出 ${usage.output.toLocaleString()} · 合计 ${(usage.input + usage.output).toLocaleString()} Token`;
        if (usage.unknown && usage.unknown < usage.requests) tokens.textContent += `（${usage.unknown} 次用量未知）`;
        const amount = document.createElement('div');
        amount.textContent = `金额：${costsText(usage)}`;
        item.append(name, tokens, amount);
        list.append(item);
      }
      details.append(title, list);
    }
    details.scrollTop = 0;
    const x=xAt(i); marker.setAttribute('x1',x); marker.setAttribute('x2',x); marker.setAttribute('visibility','visible');
    for (const [index,hit] of hits.entries()) hit.setAttribute('aria-pressed',index===i ? 'true' : 'false');
    popup.hidden=false;
    popup.dataset.chartX=String(x); positionUsagePopup();
  };
  bins.forEach((bin,i) => {
    const [previous,next]=bounds(i);
    const hit=document.createElementNS('http://www.w3.org/2000/svg','rect');
    hit.setAttribute('x',previous); hit.setAttribute('y',plot.top); hit.setAttribute('width',next-previous); hit.setAttribute('height',plot.bottom-plot.top); hit.setAttribute('fill','transparent'); hit.setAttribute('class','usage-chart-hit'); hit.setAttribute('tabindex','0'); hit.setAttribute('role','button'); hit.setAttribute('aria-pressed','false'); hit.setAttribute('aria-label',`查看 ${bin.start.toLocaleString('zh-CN',{hour12:false})} 的用量`);
    hit.addEventListener('pointerenter',()=>{ if (!pinned) showBin(i); });
    hit.addEventListener('click',()=>{ pinned=true; showBin(i); });
    hit.addEventListener('keydown',event=>{ if (event.key==='Enter' || event.key===' ') { event.preventDefault(); pinned=true; showBin(i); } else if (event.key==='ArrowLeft' || event.key==='ArrowRight') { event.preventDefault(); pinned=true; const nextIndex=Math.max(0,Math.min(bins.length-1,i+(event.key==='ArrowLeft' ? -1 : 1))); hits[nextIndex].focus(); showBin(nextIndex); } else if (event.key==='Escape') closeDetails(); });
    hits.push(hit); chart.append(hit);
  });
  const closeDetails=()=>{ pinned=false; $('usageChartPopup').hidden=true; marker.setAttribute('visibility','hidden'); for (const hit of hits) hit.setAttribute('aria-pressed','false'); };
  chart.onpointerleave=()=>{ if (!pinned) closeDetails(); };
  $('usageChartClose').onclick=closeDetails;
}
for (const id of ['usageRange', 'usageKey', 'usageModel', 'usageMetric']) $(id).addEventListener('change', renderUsage);
$('usageRefresh').addEventListener('click', () => loadUsage().catch(error => toast('用量读取失败：' + error.message)));
$('usageModel').addEventListener('change', () => { const model=$('usageModel').value, price=(usageData.prices||{})[model]; if (model) { $('priceModel').value=model; $('priceInput').value=price ? price.input : ''; $('priceOutput').value=price ? price.output : ''; $('priceCurrency').value=price ? price.currency : 'USD'; } });
$('priceModel').addEventListener('change', () => { const price=(usageData.prices||{})[$('priceModel').value.trim()]; $('priceInput').value=price ? price.input : ''; $('priceOutput').value=price ? price.output : ''; $('priceCurrency').value=price ? price.currency : 'USD'; });
$('usagePriceForm').addEventListener('submit', async event => { event.preventDefault(); try { await call('usage/price', {method:'POST', body:{model:$('priceModel').value.trim(),price:{input:Number($('priceInput').value),output:Number($('priceOutput').value),currency:$('priceCurrency').value}}}); await loadUsage(); toast('单价已保存'); } catch(error) { toast('保存失败：'+error.message); } });
$('priceDelete').addEventListener('click', async () => { try { await call('usage/price', {method:'POST', body:{model:$('priceModel').value.trim(),price:null}}); await loadUsage(); toast('单价已移除'); } catch(error) { toast('移除失败：'+error.message); } });
const dashboardTabButtons = Array.from(document.querySelectorAll('[data-tab-target]'));
function selectDashboardTab(tabName, focus = false) {
  const target = $('tab-' + tabName);
  if (!target) return;
  activeDashboardTab = tabName;
  for (const button of dashboardTabButtons) {
    const selected = button.dataset.tabTarget === tabName;
    button.classList.toggle('is-active', selected);
    button.setAttribute('aria-selected', selected ? 'true' : 'false');
    button.tabIndex = selected ? 0 : -1;
    if (selected && focus) button.focus();
  }
  for (const panel of document.querySelectorAll('.tab-panel')) {
    panel.hidden = panel !== target;
  }
  if (tabName === 'diagnostics' && unlocked) {
    loadDiagnostics().catch(error => toast('诊断读取失败：' + error.message));
  }
  if (tabName === 'usage' && unlocked) loadUsage().catch(error => toast('用量读取失败：' + error.message));
}
for (const [index, button] of dashboardTabButtons.entries()) {
  button.addEventListener('click', () => selectDashboardTab(button.dataset.tabTarget));
  button.addEventListener('keydown', event => {
    if (!['ArrowRight', 'ArrowLeft', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const delta = event.key === 'ArrowRight' ? 1 : -1;
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? dashboardTabButtons.length - 1 : (index + delta + dashboardTabButtons.length) % dashboardTabButtons.length;
    selectDashboardTab(dashboardTabButtons[next].dataset.tabTarget, true);
  });
}
for (const button of document.querySelectorAll('[data-open-tab]')) {
  button.addEventListener('click', () => {
    selectDashboardTab(button.dataset.openTab, true);
    if (button.dataset.scrollTarget) document.getElementById(button.dataset.scrollTarget)?.scrollIntoView({ block: 'start' });
  });
}
async function loadDiagnostics() {
  const snapshot = await call('diagnostics');
  $('diagTotal').textContent = Number(snapshot.total || 0).toLocaleString('zh-CN');
  $('diagLimited').textContent = Number(snapshot.rate_limited || 0).toLocaleString('zh-CN');
  $('diagActive').textContent = Number(snapshot.active || 0).toLocaleString('zh-CN');
  $('diagConcurrent').textContent = snapshot.siwc_concurrency_limit ?? '—';
  $('diagStatus').textContent = '当前进程内记录 · ' + new Date().toLocaleTimeString('zh-CN');
  const target = $('diagnosticRows');
  target.replaceChildren();
  const records = snapshot.requests || [];
  if (!records.length) {
    const row = target.insertRow();
    const cell = row.insertCell();
    cell.colSpan = 6;
    cell.textContent = '暂无请求记录，运行一次 Agent 推理后可查看。';
    return;
  }
  for (const record of records) {
    const row = target.insertRow();
    const values = [
      new Date(record.started_at).toLocaleTimeString('zh-CN'),
      record.provider + ' / ' + record.model,
      record.endpoint,
      (record.request_bytes / 1024).toFixed(1) + ' KiB',
      record.duration_ms + ' ms',
      String(record.status) + (record.retry_count ? ' · 重试' + record.retry_count + '次' : '')
    ];
    for (const value of values) row.insertCell().textContent = value;
    row.cells[5].className = record.status === 429 ? 'diag-http-error' : 'diag-http-ok';
  }
}
$('refreshDiagnosticsBtn').addEventListener('click', () => {
  if (!unlocked) return;
  loadDiagnostics().catch(error => toast('诊断读取失败：' + error.message));
});
setInterval(() => {
  if (unlocked && activeDashboardTab === 'diagnostics' && !busy && !document.hidden) {
    loadDiagnostics().catch(() => {});
  }
}, 5000);
