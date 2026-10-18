import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import fs from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const buildDir = fileURLToPath(new URL('../dist/', import.meta.url));
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));

function findChrome() {
  const candidates = process.env.CHROME_BIN ? [process.env.CHROME_BIN] : [
    'google-chrome', 'chromium', 'chromium-browser',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    ...[process.env.PROGRAMFILES, process.env['PROGRAMFILES(X86)'], process.env.LOCALAPPDATA]
      .filter(Boolean).map(root => path.join(root, 'Google/Chrome/Application/chrome.exe')),
  ];
  const executable = candidates.find(candidate => spawnSync(candidate, ['--version'], { timeout: 5000 }).status === 0);
  if (!executable) throw new Error('需要 Chrome 或 Chromium，可通过 CHROME_BIN 指定浏览器。');
  return executable;
}

async function connectBrowser(chrome, profile) {
  let port;
  for (let attempt = 0; attempt < 100; attempt++) {
    if (chrome.exitCode !== null) throw new Error('浏览器启动失败');
    try {
      port = Number((await fs.readFile(path.join(profile, 'DevToolsActivePort'), 'utf8')).split('\n')[0]);
      break;
    } catch {
      await pause(100);
    }
  }
  if (!port) throw new Error('浏览器调试端口未启动');
  const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const socket = new WebSocket(targets.find(target => target.type === 'page').webSocketDebuggerUrl);
  await once(socket, 'open');
  let id = 0;
  const pending = new Map();
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    const request = pending.get(message.id);
    if (request) {
      pending.delete(message.id);
      message.error ? request.reject(new Error(message.error.message)) : request.resolve(message.result);
    }
  });
  const call = (method, params = {}) => new Promise((resolve, reject) => {
    const requestID = ++id;
    pending.set(requestID, { resolve, reject });
    socket.send(JSON.stringify({ id: requestID, method, params }));
  });
  const evaluate = async expression => {
    const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  const wait = async expression => {
    for (let attempt = 0; attempt < 100; attempt++) {
      const value = await evaluate(expression);
      if (value) return value;
      await pause(50);
    }
    throw new Error(`等待页面状态超时：${expression}`);
  };
  const click = text => evaluate(`(() => {
    const button = Array.from(document.querySelectorAll('button')).find(element => element.textContent.trim() === ${JSON.stringify(text)});
    if (!button) throw new Error('未找到按钮：' + ${JSON.stringify(text)});
    button.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 }));
    button.click();
  })()`);
  return { call, evaluate, wait, click, close: () => socket.close() };
}

// API 使用独立模拟数据，浏览器仅访问本地构建产物。
test('服务设置、记录详情与看板回归', { timeout: 30000 }, async t => {
  await fs.access(path.join(buildDir, 'index.html'));
  const profile = await fs.mkdtemp(path.join(os.tmpdir(), 'ocs-browser-test-'));
  const requests = [];
  const capability = { vision: false, tool_call: false, reasoning: false, context: 0, matched: false, method: '', entry: '' };
  const settings = {
    services: { deepseek: { name: 'DeepSeek', protocol: 'openai-chat', base_url: 'https://api.deepseek.com', api_key: 'existing-test-key', vendor: 'deepseek' } },
    templates: [
      { id: 'deepseek', name: 'DeepSeek', protocol: 'openai-chat', base_url: 'https://api.deepseek.com', vendor: 'deepseek' },
      { id: 'alibaba-cn', name: '通义千问', protocol: 'openai-chat', base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1', vendor: 'alibaba-cn' },
    ],
    protocols: [{ id: 'openai-chat', label: 'OpenAI 兼容' }],
    answer: {
      temperature: 0.1, system_prompt: '答题提示词', caption_prompt: '图片提示词',
      answer: { service: 'deepseek', model: 'saved-model', vision: 'off', tool_call: 'off' },
      caption: { service: '', model: '', vision: 'auto', tool_call: 'auto' },
      agent: { enabled: false, max_iterations: 3, tools: null },
    },
    capability: { answer: capability, caption: capability },
    catalog: { origin: 'builtin', generated: '', checked: '', vendors: 2, entries: 2 },
  };
  let completed = false;
  let detailRequests = 0;
  const record = () => ({
    id: 1, timestamp: '2026-01-01T12:00:00', type: 'single', status: completed ? 'complete' : 'pending',
    processed_title: '等待处理的题目', raw_options: '', processed_options: '', answer: completed ? '已完成答案' : '',
    reasoning: '', raw_content: '', images: [], final_prompt: '', answer_model: 'saved-model', caption_model: '', vision: false, total_time_ms: 100, calls: [],
  });
  const json = (response, value, status = 200) => {
    response.writeHead(status, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify(value));
  };
  const server = http.createServer(async (request, response) => {
    try {
      const url = new URL(request.url, 'http://127.0.0.1');
      if (url.pathname.startsWith('/api/')) {
        const chunks = [];
        for await (const chunk of request) chunks.push(chunk);
        const text = Buffer.concat(chunks).toString();
        const body = text ? JSON.parse(text) : null;
        requests.push({ method: request.method, path: url.pathname, body });
        if (url.pathname === '/api/settings') return json(response, settings);
        if (url.pathname.startsWith('/api/services/') && request.method === 'POST') {
          const id = url.pathname.split('/').at(-1);
          if (Object.hasOwn(settings.services, id)) return json(response, { error: '服务标识已存在' }, 409);
          settings.services[id] = body;
          return json(response, { ok: true }, 201);
        }
        if (url.pathname === '/api/models/list') return json(response, { models: [] });
        if (url.pathname.startsWith('/api/test/')) return json(response, { ok: true });
        if (url.pathname === '/api/records') return json(response, { records: [record()], total: 1, page: 1, pages: 1 });
        if (url.pathname === '/api/records/1') { detailRequests++; return json(response, record()); }
        if (url.pathname === '/api/dashboard') return json(response, { stats: { today: 7, total: 7, success_rate: 100, today_tokens: 123, total_tokens: 456 }, daily_counts: [] });
        if (url.pathname === '/api/config') return json(response, []);
        return json(response, {});
      }
      const relative = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
      let file = path.join(buildDir, relative);
      if (!file.startsWith(buildDir)) return json(response, { error: '路径无效' }, 404);
      let content;
      try { content = await fs.readFile(file); } catch { file = path.join(buildDir, 'index.html'); content = await fs.readFile(file); }
      const mime = { '.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html' }[path.extname(file)] || 'application/octet-stream';
      response.writeHead(200, { 'Content-Type': mime });
      response.end(content);
    } catch (error) {
      json(response, { error: error.message }, 500);
    }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${server.address().port}`;
  let chrome;
  let browser;
  try {
    chrome = spawn(findChrome(), ['--headless=new', '--no-sandbox', '--disable-gpu', '--disable-background-networking', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', `--user-data-dir=${profile}`, 'about:blank'], { stdio: 'ignore' });
    browser = await connectBrowser(chrome, profile);
    const { call, evaluate, wait, click } = browser;
    const navigate = async route => {
      await call('Page.navigate', { url: base + route });
      await wait(`location.pathname === ${JSON.stringify(route)} && document.readyState === 'complete'`);
    };
    const idInput = `Array.from(document.querySelectorAll('label')).find(element => element.textContent.trim() === '服务标识')?.parentElement.querySelector('input')`;
    const setInput = (selector, value) => evaluate(`(() => {
      const input = ${selector};
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, ${JSON.stringify(value)});
      input.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);

    await t.test('中文预置服务使用有效且可编辑的标识', async () => {
      await navigate('/settings');
      await wait(`document.body.textContent.includes('添加服务')`);
      await click('通义千问');
      assert.equal(await evaluate(`(${idInput}).value`), 'alibaba-cn');
      assert.equal(await evaluate(`(${idInput}).readOnly`), false);
      await setInput(idInput, 'caption-service');
      await click('保存');
      await wait(`!Array.from(document.querySelectorAll('label')).some(element => element.textContent.trim() === '服务标识')`);
      assert.equal(requests.find(request => request.path === '/api/services/caption-service')?.method, 'POST');
      assert.equal(settings.services['caption-service'].name, '通义千问');
    });

    await t.test('新增同厂商服务不覆盖已有服务，重复标识不提交', async () => {
      await click('DeepSeek');
      assert.equal(await evaluate(`(${idInput}).value`), 'deepseek-2');
      await setInput(idInput, 'deepseek');
      const before = requests.filter(request => request.path === '/api/services/deepseek').length;
      await click('保存');
      await wait(`document.body.textContent.includes('服务标识已存在')`);
      assert.equal(requests.filter(request => request.path === '/api/services/deepseek').length, before);
      await setInput(idInput, 'deepseek-2');
      await click('保存');
      await wait(`!Array.from(document.querySelectorAll('label')).some(element => element.textContent.trim() === '服务标识')`);
      assert.equal(settings.services.deepseek.api_key, 'existing-test-key');
      assert.ok(settings.services['deepseek-2']);
    });

    await t.test('连通性测试提交当前未保存的模型草稿', async () => {
      await click('答题模型');
      await wait(`Boolean(document.querySelector('input[list="models-answer"]'))`);
      await setInput(`document.querySelector('input[list="models-answer"]')`, 'draft-model');
      await click('测试连通性');
      await wait(`document.body.textContent.includes('连通性测试通过')`);
      const request = requests.find(request => request.path === '/api/test/answer');
      assert.equal(request.method, 'POST');
      assert.equal(request.body.model, 'draft-model');
      assert.equal(settings.answer.answer.model, 'saved-model');
    });

    await t.test('待处理详情自动完成并停止轮询', async () => {
      await navigate('/records');
      await wait(`document.querySelector('tbody tr')?.textContent.includes('等待处理的题目')`);
      await evaluate(`document.querySelector('tbody tr').click()`);
      await wait(`document.querySelector('[role="dialog"]')?.textContent.includes('处理中...')`);
      completed = true;
      await wait(`document.querySelector('[role="dialog"]')?.textContent.includes('已完成答案')`);
      assert.ok(detailRequests >= 2);
      const before = detailRequests;
      await pause(2200);
      assert.equal(detailRequests, before);
    });

    await t.test('记录详情适配手机和平板宽度', async () => {
      for (const width of [375, 320, 768]) {
        await call('Emulation.setDeviceMetricsOverride', { width, height: 812, deviceScaleFactor: 1, mobile: true });
        await pause(100);
        const box = await evaluate(`(() => { const box = document.querySelector('[role="dialog"]').getBoundingClientRect(); return { left: box.left, right: box.right, width: box.width }; })()`);
        assert.ok(box.left >= -1 && box.right <= width + 1 && box.width <= width, JSON.stringify({ viewport: width, ...box }));
      }
      await call('Emulation.clearDeviceMetricsOverride');
    });

    await t.test('看板只请求一次统计数据', async () => {
      const before = requests.length;
      await navigate('/');
      await wait(`document.body.textContent.includes('累计 456 tokens')`);
      const dashboardRequests = requests.slice(before);
      assert.equal(dashboardRequests.filter(request => request.path === '/api/dashboard').length, 1);
      assert.equal(dashboardRequests.filter(request => request.path === '/api/stats').length, 0);
    });
  } finally {
    browser?.close();
    if (chrome && chrome.exitCode === null) {
      const exited = once(chrome, 'exit');
      chrome.kill();
      await exited;
    }
    await new Promise(resolve => server.close(resolve));
    await fs.rm(profile, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
  }
});
