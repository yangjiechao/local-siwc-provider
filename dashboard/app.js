'use strict';
// Dashboard JS deliberately uses no localStorage/sessionStorage and no external services.
const $ = (id) => document.getElementById(id);
let unlocked = false;
let status = null;
let keyCache = null;
let keyShown = false;
let modelList = [];
let configuredProviders = [];
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
function renderModelRows() {
  const list = $('modelRows');
  list.replaceChildren();
  const query = $('modelSearch').value.trim().toLocaleLowerCase();
  const filtered = modelList.filter(id => id.toLocaleLowerCase().includes(query));
  const hint = $('modelListHint');
  if (!modelsLoaded) {
    hint.textContent = '点击上方“刷新模型列表”后，这里将显示全部可用模型。';
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
  $('modelDetail').textContent = !modelsLoaded ? '登录或配置模型商后点击刷新。' : modelList.length ? '完整模型 ID 已在下方列出。' : '请确认 SIWC 账号或额外模型商的模型权限。';
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

function renderProviders() {
  const host=$('providerList');host.replaceChildren();
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
  }
}
async function loadProviders(withModels=true) {
  if (!unlocked) return;
  const data=await call('providers');
  configuredProviders=Array.isArray(data.providers)?data.providers:[];
  siwcModels=Array.isArray(data.siwc_models)?data.siwc_models:[];
  siwcDisabledModels=Array.isArray(data.siwc_disabled_models)?data.siwc_disabled_models:[];
  siwcSignedIn=!!data.siwc_signed_in;
  siwcCatalogOK=!!data.siwc_catalog_ok;
  siwcCatalogError=data.siwc_error||'';
  renderSIWCModelToggles();
  renderProviders();
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
