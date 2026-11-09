'use strict';
const $ = s => document.querySelector(s);
let csrf = '', setup = false, devices = [], config = {}, networks = [], editing = null;
const selected = new Set();
let noticeTimer, batchRunning = false;
const wakeStates = new Map(), wakePromises = new Map(), knownJobs = new Map();
const REQUEST_TIMEOUT_MS = 10000;
function notice(message) { $('#notice').textContent = message; $('#notice').hidden = false; clearTimeout(noticeTimer); noticeTimer = setTimeout(() => { $('#notice').hidden = true; }, 7000); }
function requestError(message, code, status) {
  const error = new Error(message); error.code = code; error.status = status; return error;
}
function transportError(path, method, code) {
  const reason = code === 'timeout' ? '请求超时（10 秒）' : '网络连接失败';
  const detail = path.endsWith('/wake') ? '无法确认发送结果，请检查设备状态。' : method !== 'GET' ? '无法确认操作结果，请刷新后检查。' : '请检查网络后重试。';
  return requestError(reason + '，' + detail, code);
}
async function request(path, method = 'GET', body, format = 'json') {
  const controller = new AbortController();
  let timer, timedOut = false;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => {
      timedOut = true; controller.abort(); reject(transportError(path, method, 'timeout'));
    }, REQUEST_TIMEOUT_MS);
  });
  const operation = async () => {
    const response = await fetch('/api' + path, { method, signal: controller.signal, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: body === undefined ? undefined : JSON.stringify(body) });
    // Reset locally: another network request here could extend the deadline.
    if (response.status === 401 && path !== '/login') {
      selected.clear(); setSessionState({ authenticated: false, setup_required: false, csrf: '' });
    }
    let data;
    try { data = await (response.ok && format === 'blob' ? response.blob() : response.json()); }
    catch (error) {
      if (timedOut || error.name === 'AbortError' || error instanceof TypeError) throw error;
      throw requestError('服务响应格式异常，请检查操作状态后重试。', 'invalid_response', response.status);
    }
    if (!response.ok) throw requestError(data.error || `请求失败（HTTP ${response.status}）`, 'http', response.status);
    return data;
  };
  try { return await Promise.race([operation(), timeout]); }
  catch (error) {
    if (timedOut) throw transportError(path, method, 'timeout');
    if (error instanceof TypeError || error.name === 'AbortError') throw transportError(path, method, 'network');
    throw error;
  } finally { clearTimeout(timer); }
}
function api(path, method = 'GET', body) { return request(path, method, body); }

function bind(element, event, action) { element.addEventListener(event, async e => { if (event === 'submit') e.preventDefault(); const button = event === 'submit' ? element.querySelector('[type=submit]') : element; if (button.tagName === 'BUTTON') button.disabled = true; try { await action(e); } catch (error) { notice(error.message); } finally { if (button.tagName === 'BUTTON') button.disabled = false; updateBatch(); } }); }
function el(tag, text, cls) { const node = document.createElement(tag); if (text !== undefined) node.textContent = text; if (cls) node.className = cls; return node; }
function button(text, cls, action) { const b = el('button', text, cls); b.type = 'button'; bind(b, 'click', action); return b; }
function setSessionState(s) {
  csrf = s.csrf; setup = s.setup_required;
  $('#auth').hidden = s.authenticated; $('#dashboard').hidden = !s.authenticated; $('#logout').hidden = !s.authenticated;
  $('#auth-title').textContent = setup ? '设置管理员密码' : '欢迎回来';
  $('#auth-description').textContent = setup ? '输入启动终端或服务日志中的初始化令牌，创建至少 12 字节的密码。' : '登录后管理你的设备。';
  $('#token-label').hidden = !setup; $('#token').required = setup;
  $('#password').minLength = setup ? 12 : 1; $('#password').autocomplete = setup ? 'new-password' : 'current-password';
  $('#auth-submit').textContent = setup ? '设置密码' : '登录';
}
async function session() {
  const s = await api('/session'); setSessionState(s);
  if (s.authenticated) await load();
}
async function load() {
  [devices, config, networks] = await Promise.all([api('/devices'), api('/config'), api('/networks')]);
  for (const id of selected) if (!devices.some(d => d.id === id)) selected.delete(id);
  render();
  for (const key of ['listen', 'public_url', 'broadcast', 'port', 'repeat', 'log_limit']) $('#settings-form').elements[key].value = config[key];
  $('#networks').replaceChildren(...networks.map(n => el('p', `${n.name} · ${n.ip} → ${n.broadcast}`)));
  if (!networks.length) $('#networks').append(el('p', '未发现可用的 IPv4 网卡。'));
  const select = $('#interface'); select.replaceChildren(new Option('由系统选择', ''));
  for (const name of new Set(networks.map(n => n.name))) select.add(new Option(name, name));
}
function methodLabel(d) { return ({wol:'WoL 魔术包',mac_vnc:'Mac 屏幕共享（VNC）',wol_mac_vnc:'WoL＋Mac 屏幕共享（VNC）'})[d.wake_method || 'wol']; }
function updateWakeState(id, state) {
  wakeStates.set(id, { ...(wakeStates.get(id) || {}), ...state });
  const status = document.querySelector(`[data-device="${id}"] .status`);
  if (status) status.textContent = wakeStates.get(id).message || '尚未检测';
  const button = document.querySelector(`[data-device="${id}"] [data-wake]`);
  if (button) button.disabled = !!wakeStates.get(id).busy;
}
async function waitWakeJob(id, jobID) {
  const deadline = Date.now() + 80000;
  while (Date.now() < deadline) {
    let job;
    try { job = await api(`/wake-jobs/${jobID}`); }
    catch (error) { if (error.status === 404) knownJobs.delete(id); throw error; }
    updateWakeState(id, { message: job.progress });
    if (job.state !== 'running') {
      knownJobs.delete(id);
      if (job.state !== 'done') throw requestError(job.result.message || job.progress, job.state);
      return job.result;
    }
    await new Promise(resolve => setTimeout(resolve, 1000));
  }
  throw requestError('任务查询超时，请刷新查看状态；不会自动重新认证。', 'timeout');
}
function wakeDevice(d) {
  if (wakePromises.has(d.id)) return wakePromises.get(d.id);
  updateWakeState(d.id, { busy: true, message: '正在提交唤醒请求…' });
  const pending = (async () => {
    try {
      const previous = knownJobs.get(d.id) || d.active_job_id; delete d.active_job_id;
      const r = previous ? { job_id: previous } : await api(`/devices/${d.id}/wake`, 'POST');
      if (r.job_id) {
        knownJobs.set(d.id, r.job_id);
        return await waitWakeJob(d.id, r.job_id);
      }
      updateWakeState(d.id, { message: r.message });
      return r;
    } catch (error) {
      updateWakeState(d.id, { message: error.message });
      throw error;
    } finally {
      wakePromises.delete(d.id);
      updateWakeState(d.id, { busy: false });
    }
  })();
  wakePromises.set(d.id, pending);
  return pending;
}
function updateBatch() { $('#batch').disabled = batchRunning || selected.size === 0; $('#batch').textContent = batchRunning ? '正在唤醒…' : selected.size ? `唤醒所选 (${selected.size})` : '唤醒所选'; }
function render() {
  const query = $('#search').value.toLowerCase();
  const filtered = devices.filter(d => `${d.name} ${d.mac} ${d.note}`.toLowerCase().includes(query));
  $('#count').textContent = `${filtered.length} / ${devices.length} 台设备`;
  $('#empty').hidden = devices.length !== 0;
  const cards = filtered.map(d => {
    const card = el('article', undefined, 'card'), top = el('div', undefined, 'card-top'); card.dataset.device = d.id;
    const check = document.createElement('input'); check.type = 'checkbox'; check.checked = selected.has(d.id); check.setAttribute('aria-label', `选择 ${d.name}`); check.addEventListener('change', () => { if (check.checked) selected.add(d.id); else selected.delete(d.id); updateBatch(); });
    top.append(check, el('h2', d.name));
    const status = el('p', (wakeStates.get(d.id) || {}).message || '尚未检测', 'status');
    const actions = el('div', undefined, 'actions');
    const wakeButton = button('⏻ 唤醒', 'primary', async () => {
      const r = await wakeDevice(d); notice(r.message + (r.warning ? '\n' + r.warning : ''));
    });
    wakeButton.dataset.wake = ''; wakeButton.disabled = !!(wakeStates.get(d.id) || {}).busy;
    actions.append(wakeButton, button('检测', '', async () => { const r = await api(`/devices/${d.id}/probe`, 'POST'); updateWakeState(d.id,{message:r.message}); }), button('编辑', '', () => openEditor(d)), button('删除', 'delete', async () => { if (confirm(`删除设备“${d.name}”？`)) { await api(`/devices/${d.id}`, 'DELETE'); knownJobs.delete(d.id); wakeStates.delete(d.id); await load(); } }));
    const address = d.wake_method === 'mac_vnc' ? `${d.ip}:${d.vnc.port}` : `${d.broadcast}:${d.port}${d.interface ? ' · ' + d.interface : ''}`;
    card.append(top, el('p',methodLabel(d)), el('code', d.mac || d.ip), el('div', address, 'address'), el('p', d.note || '—', 'note'), status, actions);
    if (d.active_job_id && !wakePromises.has(d.id)) setTimeout(() => wakeDevice(d).catch(e => notice(e.message)), 0);
    return card;
  });
  if (devices.length && !filtered.length) cards.push(el('p', '没有匹配的设备。'));
  $('#devices').replaceChildren(...cards); updateBatch();
}
function openEditor(d) {
  editing = d ? d.id : null; const form = $('#device-form'); form.reset();
  $('#editor-title').textContent = d ? '编辑设备' : '添加设备';
  const defaults = d || { wake_method: 'wol', name: '', mac: '', broadcast: config.broadcast, port: config.port, repeat: config.repeat, interface: '', ip: '', probe_port: 0, note: '' };
  if (d && d.interface && ![...$('#interface').options].some(o => o.value === d.interface)) $('#interface').add(new Option(d.interface + '（当前不可用）', d.interface));
  for (const [key, value] of Object.entries(defaults)) if (key !== 'id' && form.elements[key]) form.elements[key].value = value;
  form.elements.wake_method.value = d?.wake_method || 'wol';
  form.elements.vnc_port.value = d?.vnc?.port || 5900;
  form.elements.vnc_username.value = d?.vnc?.username || '';
  form.elements.vnc_hold_seconds.value = d?.vnc?.hold_seconds || 0;
  $('#vnc-password').value = ''; $('#vnc-password').disabled = false; $('#clear-vnc-password').checked = false;
  $('#vnc-password').placeholder = d?.password_configured ? '已配置；留空保留，输入新值可替换' : '输入 Mac 密码';
  $('#vnc-password-state').textContent = (d?.password_configured ? '已配置密码，无法查看。' : '尚未配置密码。') + 'HTTP 会明文传输新密码，建议本机或 HTTPS 配置；仅在可信网络使用 VNC。';
  updateWakeFields();
  form.querySelector('details').open = !!(d && (d.ip || d.note)); $('#editor').showModal();
}
async function history() {
  const events = await api('/events');
  const rows = events.map(e => { const tr = el('tr'); tr.append(el('td', new Date(e.time).toLocaleString()), el('td', e.name), el('td', e.result)); return tr; });
  if (!rows.length) { const tr = el('tr'), td = el('td', '暂无操作记录'); td.colSpan = 3; tr.append(td); rows.push(tr); }
  $('#events').replaceChildren(...rows);
}
bind($('#auth-form'), 'submit', async () => {
  const password = $('#password').value;
  if (setup) { await api('/setup', 'POST', { token: $('#token').value, password }); $('#token').value = ''; }
  const s = await api('/login', 'POST', { password }); csrf = s.csrf; $('#password').value = ''; await session();
});
bind($('#logout'), 'click', async () => { await api('/logout', 'POST'); selected.clear(); devices = []; knownJobs.clear(); wakeStates.clear(); await session(); });
bind($('#add'), 'click', () => openEditor()); bind($('#empty-add'), 'click', () => openEditor());
$('#close-editor').addEventListener('click', () => $('#editor').close());
$('#editor').addEventListener('close', () => { $('#vnc-password').value = ''; });
function updateWakeFields() {
  const form = $('#device-form'), method = form.elements.wake_method.value;
  const hasVNC = method !== 'wol', hasWOL = method !== 'mac_vnc';
  $('#wol-fields').hidden = !hasWOL; $('#wol-fields').disabled = !hasWOL;
  $('#vnc-fields').hidden = !hasVNC; $('#vnc-fields').disabled = !hasVNC;
  form.elements.ip.required = hasVNC;
}
$('#wake-method').addEventListener('change', updateWakeFields);
$('#clear-vnc-password').addEventListener('change', () => { const clear = $('#clear-vnc-password').checked; $('#vnc-password').disabled = clear; if (clear) $('#vnc-password').value = ''; });
$('#search').addEventListener('input', render);
$('#interface').addEventListener('change', () => { const n = networks.find(n => n.name === $('#interface').value); if (n) $('#device-form').elements.broadcast.value = n.broadcast; });
bind($('#device-form'), 'submit', async () => {
  const form = $('#device-form');
  const d = { name: form.elements.name.value, wake_method: form.elements.wake_method.value, mac: form.elements.mac.value, broadcast: form.elements.broadcast.value, port: Number(form.elements.port.value), repeat: Number(form.elements.repeat.value), interface: form.elements.interface.value, ip: form.elements.ip.value, probe_port: Number(form.elements.probe_port.value), note: form.elements.note.value };
  if (d.wake_method !== 'wol') {
    d.vnc = { port: Number(form.elements.vnc_port.value), username: form.elements.vnc_username.value, hold_seconds: Number(form.elements.vnc_hold_seconds.value) };
    d.vnc_password = form.elements.vnc_password.value;
    d.clear_vnc_password = form.elements.clear_vnc_password.checked;
  }
  if (d.wake_method === 'wol' && editing) { const previous = devices.find(device => device.id === editing); if (previous?.vnc?.username) d.vnc = previous.vnc; }
  if (editing) d.id = editing;
  try { await api(editing ? `/devices/${editing}` : '/devices', editing ? 'PUT' : 'POST', d); }
  finally { $('#vnc-password').value = ''; delete d.vnc_password; }
  $('#editor').close(); await load(); notice('设备已保存');
});
bind($('#batch'), 'click', async () => {
  const ids = [...selected]; if (!confirm(`唤醒所选 ${ids.length} 台设备？`)) return;
  let sent = 0; const failures = [];
  batchRunning = true; updateBatch();
  try {
    for (let i = 0; i < ids.length; i++) {
      const id = ids[i];
      try { await wakeDevice(devices.find(d => d.id === id)); sent++; }
      catch (error) {
        failures.push(`${devices.find(d => d.id === id)?.name || id}: ${error.message}`);
        if (['timeout', 'network', 'invalid_response'].includes(error.code) || [401, 403].includes(error.status)) {
          if (i + 1 < ids.length) failures.push(`已停止批量操作，剩余 ${ids.length - i - 1} 台未发送。`);
          break;
        }
      }
    }
    notice(`已完成 ${sent} / ${ids.length} 台设备的唤醒流程` + (failures.length ? '\n' + failures.slice(0, 3).join('\n') : ''));
  } finally { batchRunning = false; updateBatch(); }
});
bind($('#export'), 'click', async () => {
  const blob = await request('/export', 'GET', undefined, 'blob');
  const url = URL.createObjectURL(blob), a = document.createElement('a'); a.href = url; a.download = 'devices.yaml'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
});
bind($('#settings-form'), 'submit', async () => {
  const c = Object.fromEntries(new FormData($('#settings-form'))); for (const key of ['port', 'repeat', 'log_limit']) c[key] = Number(c[key]);
  const r = await api('/config', 'PUT', c); config = c; notice(r.message);
});
for (const tab of document.querySelectorAll('[data-view]')) bind(tab, 'click', async () => { for (const view of ['devices', 'history', 'settings']) $('#' + view + '-view').hidden = view !== tab.dataset.view; for (const t of document.querySelectorAll('[data-view]')) t.classList.toggle('active', t === tab); if (tab.dataset.view === 'history') await history(); });
bind($('#refresh-history'), 'click', history);
session().catch(e => notice('无法加载管理界面：' + e.message));
