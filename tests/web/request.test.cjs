'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../../internal/app/web/app.js'), 'utf8');
const flush = () => new Promise(resolve => setImmediate(resolve));

async function fixture() {
  let now = 0, next = 0;
  const timers = new Map(), nodes = new Map(), calls = [];
  const node = selector => {
    if (!nodes.has(selector)) nodes.set(selector, {
      tagName: 'BUTTON', disabled: false, hidden: false, textContent: '', listeners: {},
      addEventListener(event, listener) { this.listeners[event] = listener; },
      querySelector() { return node(selector + ' submit'); }
    });
    return nodes.get(selector);
  };
  let transport = async () => ({ ok: true, status: 200, json: async () => ({ authenticated: false, setup_required: false, csrf: '' }) });
  const context = vm.createContext({
    document: { querySelector: node, querySelectorAll: () => [] },
    AbortController, TypeError, URL, confirm: () => true,
    fetch: (...args) => { calls.push(args); return transport(...args); },
    setTimeout(fn, delay) { const id = ++next; timers.set(id, { fn, at: now + delay, delay }); return id; },
    clearTimeout(id) { timers.delete(id); }
  });
  vm.runInContext(source, context);
  await flush(); // Finish the real application's initial session request.
  calls.length = 0;
  return {
    node, calls, context, timers,
    run: code => vm.runInContext(code, context),
    fetch: fn => { transport = fn; },
    advance(ms) {
      now += ms;
      for (const [id, timer] of [...timers]) if (timer.at <= now) { timers.delete(id); timer.fn(); }
    }
  };
}
const response = (data, status = 200) => ({ ok: status < 400, status, json: async () => data });

test('wake request aborts after 10 seconds without retrying', async () => {
  const f = await fixture(); let signal, settled = false;
  f.fetch((_, options) => { signal = options.signal; return new Promise(() => {}); });
  const pending = f.run("api('/devices/pc/wake', 'POST')");
  pending.then(() => { settled = true; }, () => { settled = true; });
  f.advance(9999); await flush(); assert.equal(settled, false);
  f.advance(1);
  await assert.rejects(pending, error => error.code === 'timeout' && error.message.includes('无法确认发送结果'));
  assert.equal(signal.aborted, true); assert.equal(f.calls.length, 1); assert.equal(f.timers.size, 0);
});

test('deadline includes JSON response body reading', async () => {
  const f = await fixture(); let bodyRead = false;
  f.fetch(async () => ({ ok: true, status: 200, json: () => { bodyRead = true; return new Promise(() => {}); } }));
  const pending = f.run("api('/devices/pc/wake', 'POST')");
  await flush(); assert.equal(bodyRead, true); f.advance(10000);
  await assert.rejects(pending, error => error.code === 'timeout');
});

test('YAML export body reading has the same deadline', async () => {
  const f = await fixture(); let bodyRead = false;
  f.fetch(async () => ({ ok: true, status: 200, blob: () => { bodyRead = true; return new Promise(() => {}); } }));
  const pending = f.node('#export').listeners.click({});
  await flush(); assert.equal(bodyRead, true); assert.equal(f.node('#export').disabled, true);
  f.advance(10000); await pending;
  assert.equal(f.node('#export').disabled, false); assert.match(f.node('#notice').textContent, /请求超时/);
});

test('success clears the timer and returns the server result', async () => {
  const f = await fixture(); f.fetch(async () => response({ message: 'sent' }));
  const result = await f.run("api('/devices/pc/wake', 'POST')");
  assert.equal(result.message, 'sent'); assert.equal(f.timers.size, 0);
  f.advance(20000); assert.equal(f.calls.length, 1);
});

test('network errors report uncertain wake result and clear the timer', async () => {
  const f = await fixture(); f.fetch(async () => { throw new TypeError('Failed to fetch'); });
  await assert.rejects(f.run("api('/devices/pc/wake', 'POST')"), error => error.code === 'network' && error.message.includes('无法确认发送结果'));
  assert.equal(f.timers.size, 0); assert.equal(f.calls.length, 1);
});

test('HTTP failures preserve the server message and status', async () => {
  const f = await fixture(); f.fetch(async () => response({ error: '服务繁忙' }, 503));
  await assert.rejects(f.run("api('/devices/pc/wake', 'POST')"), error => error.code === 'http' && error.status === 503 && error.message === '服务繁忙');
  assert.equal(f.timers.size, 0);
});

test('invalid response is reported and does not leave a timer', async () => {
  const f = await fixture(); f.fetch(async () => ({ ok: false, status: 502, json: async () => { throw new SyntaxError('bad JSON'); } }));
  await assert.rejects(f.run("api('/devices/pc/wake', 'POST')"), error => error.code === 'invalid_response');
  assert.equal(f.timers.size, 0);
});

test('401 returns to login without starting a second network request', async () => {
  const f = await fixture(); f.run("csrf='old'; selected.add('pc')");
  f.fetch(async () => response({ error: '请先登录' }, 401));
  await assert.rejects(f.run("api('/devices/pc/wake', 'POST')"), error => error.status === 401);
  assert.equal(f.calls.length, 1); assert.equal(f.run('csrf'), '');
  assert.equal(f.node('#dashboard').hidden, true); assert.equal(f.node('#auth').hidden, false);
});

test('wake button is restored and error shown after timeout', async () => {
  const f = await fixture(); f.fetch(() => new Promise(() => {}));
  f.run("bind(document.querySelector('#wake-test'), 'click', () => api('/devices/pc/wake', 'POST'))");
  const pending = f.node('#wake-test').listeners.click({});
  assert.equal(f.node('#wake-test').disabled, true); f.advance(10000); await pending;
  assert.equal(f.node('#wake-test').disabled, false);
  assert.match(f.node('#notice').textContent, /请求超时.*无法确认发送结果/);
});

test('batch timeout stops remaining devices and restores its button', async () => {
  const f = await fixture(); f.fetch(() => new Promise(() => {}));
  f.run("selected.add('pc'); selected.add('pc2'); devices=[{id:'pc',name:'电脑一'},{id:'pc2',name:'电脑二'}]");
  const pending = f.node('#batch').listeners.click({});
  f.run('updateBatch()'); assert.equal(f.node('#batch').disabled, true);
  f.advance(10000); await pending;
  assert.equal(f.calls.length, 1); assert.equal(f.node('#batch').disabled, false);
  assert.match(f.node('#notice').textContent, /剩余 1 台未发送/);
});

test('Mac wake polls its task and deduplicates clicks without retrying auth', async () => {
  const f = await fixture(); let polls = 0;
  f.fetch(async (url, options) => {
    if (options.method === 'POST') return response({ job_id: 'job-one' }, 202);
    polls++;
    return response(polls === 1 ? { state: 'running', progress: '认证成功，正在保持连接' } : { state: 'done', progress: '已完成', result: { message: 'VNC 认证成功' } });
  });
  f.run("devices=[{id:'mac',name:'Mac',wake_method:'mac_vnc'}]; pendingMac=wakeDevice(devices[0])");
  const pending = f.run('pendingMac');
  assert.equal(f.run('wakeDevice(devices[0])'), pending);
  await flush(); assert.equal(f.run("wakeStates.get('mac').busy"), true);
  f.advance(1000); const result = await pending;
  assert.equal(result.message, 'VNC 认证成功');
  assert.equal(f.calls.filter(([, o]) => o.method === 'POST').length, 1);
  assert.equal(f.run("wakeStates.get('mac').busy"), false);
});

test('lost task query resumes the known job without sending another wake', async () => {
  const f = await fixture(); let fail = true;
  f.fetch(async (_, options) => {
    if (options.method === 'POST') return response({ job_id: 'job-two' }, 202);
    if (fail) throw new TypeError('connection lost');
    return response({ state: 'done', progress: '已完成', result: { message: 'VNC 认证成功' } });
  });
  f.run("devices=[{id:'mac',name:'Mac',wake_method:'mac_vnc'}]");
  await assert.rejects(f.run('wakeDevice(devices[0])'), e => e.code === 'network');
  fail = false; await f.run('wakeDevice(devices[0])');
  assert.equal(f.calls.filter(([, o]) => o.method === 'POST').length, 1);
});

test('active job from reload resumes once and allows a later explicit wake', async () => {
  const f = await fixture();
  f.fetch(async (_, options) => options.method === 'POST' ? response({ message: 'sent' }) : response({ state: 'done', result: { message: 'completed' } }));
  f.run("devices=[{id:'mac',name:'Mac',active_job_id:'old-job'}]");
  await f.run('wakeDevice(devices[0])');
  assert.equal(f.calls.filter(([, o]) => o.method === 'POST').length, 0);
  await f.run('wakeDevice(devices[0])');
  assert.equal(f.calls.filter(([, o]) => o.method === 'POST').length, 1);
});
