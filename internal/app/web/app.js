'use strict';
const $ = s => document.querySelector(s);
let csrf = '', setup = false, devices = [], config = {}, networks = [], editing = null;
const selected = new Set();
let noticeTimer;
function notice(message) { $('#notice').textContent = message; $('#notice').hidden = false; clearTimeout(noticeTimer); noticeTimer = setTimeout(() => { $('#notice').hidden = true; }, 7000); }
async function api(path, method = 'GET', body) {
  const response = await fetch('/api' + path, { method, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: body === undefined ? undefined : JSON.stringify(body) });
  const data = await response.json();
  if (!response.ok) { if (response.status === 401 && path !== '/login') await session(); throw new Error(data.error || '请求失败'); }
  return data;
}
function bind(element, event, action) { element.addEventListener(event, async e => { if (event === 'submit') e.preventDefault(); const button = event === 'submit' ? element.querySelector('[type=submit]') : element; if (button.tagName === 'BUTTON') button.disabled = true; try { await action(e); } catch (error) { notice(error.message); } finally { if (button.tagName === 'BUTTON') button.disabled = false; updateBatch(); } }); }
function el(tag, text, cls) { const node = document.createElement(tag); if (text !== undefined) node.textContent = text; if (cls) node.className = cls; return node; }
function button(text, cls, action) { const b = el('button', text, cls); b.type = 'button'; bind(b, 'click', action); return b; }
async function session() {
  const s = await api('/session'); csrf = s.csrf; setup = s.setup_required;
  $('#auth').hidden = s.authenticated; $('#dashboard').hidden = !s.authenticated; $('#logout').hidden = !s.authenticated;
  $('#auth-title').textContent = setup ? '设置管理员密码' : '欢迎回来';
  $('#auth-description').textContent = setup ? '输入启动终端显示的初始化令牌，创建至少 12 字节的密码。' : '登录后管理你的设备。';
  $('#token-label').hidden = !setup; $('#token').required = setup;
  $('#password').minLength = setup ? 12 : 1; $('#password').autocomplete = setup ? 'new-password' : 'current-password';
  $('#auth-submit').textContent = setup ? '设置密码' : '登录';
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
function updateBatch() { $('#batch').disabled = selected.size === 0; $('#batch').textContent = selected.size ? `唤醒所选 (${selected.size})` : '唤醒所选'; }
function render() {
  const query = $('#search').value.toLowerCase();
  const filtered = devices.filter(d => `${d.name} ${d.mac} ${d.note}`.toLowerCase().includes(query));
  $('#count').textContent = `${filtered.length} / ${devices.length} 台设备`;
  $('#empty').hidden = devices.length !== 0;
  const cards = filtered.map(d => {
    const card = el('article', undefined, 'card'), top = el('div', undefined, 'card-top');
    const check = document.createElement('input'); check.type = 'checkbox'; check.checked = selected.has(d.id); check.setAttribute('aria-label', `选择 ${d.name}`); check.addEventListener('change', () => { if (check.checked) selected.add(d.id); else selected.delete(d.id); updateBatch(); });
    top.append(check, el('h2', d.name));
    const status = el('p', '尚未检测', 'status');
    const actions = el('div', undefined, 'actions');
    actions.append(button('⏻ 唤醒', 'primary', async () => { const r = await api(`/devices/${d.id}/wake`, 'POST'); status.textContent = '唤醒包已发送 · 可稍后检测上线'; notice(r.message + (r.warning ? '\n' + r.warning : '')); }), button('检测', '', async () => { const r = await api(`/devices/${d.id}/probe`, 'POST'); status.textContent = r.message; }), button('编辑', '', () => openEditor(d)), button('删除', 'delete', async () => { if (confirm(`删除设备“${d.name}”？`)) { await api(`/devices/${d.id}`, 'DELETE'); await load(); } }));
    card.append(top, el('code', d.mac), el('div', `${d.broadcast}:${d.port}${d.interface ? ' · ' + d.interface : ''}`, 'address'), el('p', d.note || '—', 'note'), status, actions);
    return card;
  });
  if (devices.length && !filtered.length) cards.push(el('p', '没有匹配的设备。'));
  $('#devices').replaceChildren(...cards); updateBatch();
}
function openEditor(d) {
  editing = d ? d.id : null; const form = $('#device-form'); form.reset();
  $('#editor-title').textContent = d ? '编辑设备' : '添加设备';
  const defaults = d || { name: '', mac: '', broadcast: config.broadcast, port: config.port, repeat: config.repeat, interface: '', ip: '', probe_port: 0, note: '' };
  if (d && d.interface && ![...$('#interface').options].some(o => o.value === d.interface)) $('#interface').add(new Option(d.interface + '（当前不可用）', d.interface));
  for (const [key, value] of Object.entries(defaults)) if (key !== 'id' && form.elements[key]) form.elements[key].value = value;
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
bind($('#logout'), 'click', async () => { await api('/logout', 'POST'); selected.clear(); devices = []; await session(); });
bind($('#add'), 'click', () => openEditor()); bind($('#empty-add'), 'click', () => openEditor());
$('#close-editor').addEventListener('click', () => $('#editor').close());
$('#search').addEventListener('input', render);
$('#interface').addEventListener('change', () => { const n = networks.find(n => n.name === $('#interface').value); if (n) $('#device-form').elements.broadcast.value = n.broadcast; });
bind($('#device-form'), 'submit', async () => {
  const d = Object.fromEntries(new FormData($('#device-form')));
  for (const key of ['port', 'repeat', 'probe_port']) d[key] = Number(d[key]);
  if (editing) d.id = editing;
  await api(editing ? `/devices/${editing}` : '/devices', editing ? 'PUT' : 'POST', d);
  $('#editor').close(); await load(); notice('设备已保存');
});
bind($('#batch'), 'click', async () => {
  const ids = [...selected]; if (!confirm(`向 ${ids.length} 台设备发送唤醒包？`)) return;
  let sent = 0; const failures = [];
  for (const id of ids) { try { await api(`/devices/${id}/wake`, 'POST'); sent++; } catch (e) { failures.push(`${devices.find(d => d.id === id)?.name || id}: ${e.message}`); } }
  notice(`已向 ${sent} / ${ids.length} 台设备发送唤醒包` + (failures.length ? '\n' + failures.slice(0, 3).join('\n') : ''));
});
bind($('#export'), 'click', async () => {
  const response = await fetch('/api/export'); if (!response.ok) throw new Error('导出失败，请确认登录状态');
  const url = URL.createObjectURL(await response.blob()), a = document.createElement('a'); a.href = url; a.download = 'devices.yaml'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
});
bind($('#settings-form'), 'submit', async () => {
  const c = Object.fromEntries(new FormData($('#settings-form'))); for (const key of ['port', 'repeat', 'log_limit']) c[key] = Number(c[key]);
  const r = await api('/config', 'PUT', c); config = c; notice(r.message);
});
for (const tab of document.querySelectorAll('[data-view]')) bind(tab, 'click', async () => { for (const view of ['devices', 'history', 'settings']) $('#' + view + '-view').hidden = view !== tab.dataset.view; for (const t of document.querySelectorAll('[data-view]')) t.classList.toggle('active', t === tab); if (tab.dataset.view === 'history') await history(); });
bind($('#refresh-history'), 'click', history);
session().catch(e => notice('无法加载管理界面：' + e.message));
