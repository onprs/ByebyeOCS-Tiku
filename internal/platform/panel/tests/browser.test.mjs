import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

// 由隔离 Go 集成夹具提供真实服务地址，只使用合成维护账号。
const base = process.argv[2];
if (!base || !['127.0.0.1', 'localhost', '[::1]'].includes(new URL(base).hostname)) throw new Error('浏览器回归需要回环隔离服务地址。');
const password = 'Isolated-Maintenance-2026!';
const profile = await fs.mkdtemp(path.join(os.tmpdir(), 'tiku-panel-browser-'));
const temporaryArtifacts = !process.env.BYEBYEOCS_TIKU_BROWSER_ARTIFACT_DIR;
const artifacts = process.env.BYEBYEOCS_TIKU_BROWSER_ARTIFACT_DIR || await fs.mkdtemp(path.join(os.tmpdir(), 'tiku-panel-artifacts-'));
await fs.mkdir(artifacts, { recursive: true });
const candidates = process.env.CHROME_BIN ? [process.env.CHROME_BIN] : ['google-chrome', 'chromium', 'chromium-browser'];
const executable = candidates.find(candidate => spawnSync(candidate, ['--version'], { timeout: 5000 }).status === 0);
if (!executable) throw new Error('需要维护环境 Chrome 或 Chromium，可设置 CHROME_BIN。');
const chrome = spawn(executable, ['--headless=new', '--no-sandbox', '--disable-gpu', '--remote-debugging-port=0', `--user-data-dir=${profile}`, 'about:blank'], { stdio: 'ignore' });
const pause = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
let socket;
try {
  let port;
  for (let attempt = 0; attempt < 100; attempt++) {
    if (chrome.exitCode !== null) throw new Error('浏览器提前退出。');
    try { port = Number((await fs.readFile(path.join(profile, 'DevToolsActivePort'), 'utf8')).split('\n')[0]); if (port) break; } catch { }
    await pause(50);
  }
  assert.ok(port, '浏览器调试端口未启动');
  const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  socket = new WebSocket(targets.find(target => target.type === 'page').webSocketDebuggerUrl);
  await once(socket, 'open');
  let sequence = 0;
  const pending = new Map();
  const exceptions = [];
  const serverErrors = [];
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    if (message.method === 'Runtime.exceptionThrown') exceptions.push(message.params.exceptionDetails.text);
    if (message.method === 'Network.responseReceived' && message.params.response.status >= 500) serverErrors.push({ url: message.params.response.url, status: message.params.response.status });
    const request = pending.get(message.id);
    if (!request) return;
    pending.delete(message.id);
    clearTimeout(request.timer);
    message.error ? request.reject(new Error(message.error.message)) : request.resolve(message.result);
  });
  const call = (method, params = {}) => new Promise((resolve, reject) => {
    const id = ++sequence;
    const timer = setTimeout(() => { pending.delete(id); reject(new Error(`浏览器调用超时：${method}`)); }, 15000);
    pending.set(id, { resolve, reject, timer });
    socket.send(JSON.stringify({ id, method, params }));
  });
  const evaluate = async expression => {
    const result = await call('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  const wait = async expression => {
    for (let attempt = 0; attempt < 160; attempt++) {
      const value = await evaluate(expression);
      if (value) return value;
      await pause(50);
    }
    throw new Error(`等待页面状态超时：${expression}`);
  };
  const click = label => evaluate(`(() => {
    const element = [...document.querySelectorAll('button')].find(value => value.textContent.trim() === ${JSON.stringify(label)} && !value.disabled);
    if (!element) throw new Error('未找到可用操作'); element.click();
  })()`);
  const navigate = async (route, heading) => {
    await call('Page.navigate', { url: base + '/panel/#' + route });
    await wait(`document.querySelector('#page h1')?.textContent === ${JSON.stringify(heading)} && ![...document.querySelectorAll('#page [role=status]')].some(value => value.textContent.includes('正在读取'))`);
    assert.equal(await evaluate(`!!document.querySelector('#page .error-state')`), false, '页面加载失败：' + route);
  };
  const screenshot = async name => {
    const image = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
    await fs.writeFile(path.join(artifacts, name + '.png'), Buffer.from(image.data, 'base64'));
  };
  const login = async username => {
    await wait(`!!document.querySelector('input[name=password]')`);
    await evaluate(`(() => {document.querySelector('input[name=username]').value=${JSON.stringify(username)};document.querySelector('input[name=password]').value=${JSON.stringify(password)};})()`);
    await click('登录');
    await wait(`!!document.querySelector('.navigation')`);
  };
  await Promise.all([call('Runtime.enable'), call('Page.enable'), call('Network.enable')]);
  await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  await call('Page.navigate', { url: base + '/panel/#overview' });
  await login('maintainer');
  await navigate('overview', '运行总览');
  await screenshot('overview-desktop');
  await navigate('offerings', '模型与价格');
  await wait(`!!document.querySelector('.model-grid')`);
  const leaked = await evaluate(`(async () => {
    const responses = await Promise.all(['/models','/offerings','/settings'].map(value=>fetch('/panel/api'+value).then(response=>response.text())));
    return responses.some(value=>['isolated-upstream-key','isolated-fork-service-token','password_hash_file','api_key_file','service_token_file'].some(secret=>value.includes(secret)));
  })()`);
  assert.equal(leaked, false, '设置响应包含私密配置');
  await click('停用');
  await wait(`document.querySelector('.model-grid .card .model-name .status')?.textContent === '模型已停用'`);
  await click('启用');
  await wait(`document.querySelector('.model-grid .card .model-name .status')?.textContent === '已发布'`);
  await click('编辑');
  await wait(`!!document.querySelector('dialog input[name=display_name]')`);
  assert.equal(await evaluate(`!!document.querySelector('dialog input[name=input_price]') && !!document.querySelector('dialog input[name=cache_ttl_days]')`), true, '公开条目编辑缺少价格或缓存字段');
  await evaluate(`(() => {const field=document.querySelector('dialog input[name=display_name]');field.value='浏览器核对公开模型';})()`);
  await click('保存');
  await wait(`!document.querySelector('dialog') && document.querySelector('#page')?.textContent.includes('浏览器核对公开模型')`);
  await click('功能测试');
  await wait(`!!document.querySelector('dialog')`);
  assert.equal(await evaluate(`[...document.querySelectorAll('dialog button')].find(value=>value.textContent==='开始测试').disabled`), true, '功能测试未经确认就可发起');
  await evaluate(`(() => {const check=document.querySelector('input[name=confirm_cost]');check.checked=true;check.dispatchEvent(new Event('change'));})()`);
  await click('开始测试');
  await wait(`document.querySelector('dialog')?.textContent.includes('测试通过')`);
  await click('关闭');
  await screenshot('offerings-desktop');
  await navigate('requests', '查询记录');
  await wait(`!!document.querySelector('#page table .link')`);
  await evaluate(`document.querySelector('#page table .link').click()`);
  await wait(`document.querySelector('dialog')?.textContent.includes('用量完整性')`);
  await screenshot('request-detail');
  await click('关闭');
  await navigate('cache', '精确缓存');
  await wait(`!!document.querySelector('#page table')`);
  await evaluate(`(() => {const row=[...document.querySelectorAll('#page tbody tr')].find(value=>value.querySelector('.status')?.textContent==='有效');if(!row)throw new Error('缺少有效缓存夹具');row.querySelector('button').click();})()`);
  await wait(`!!document.querySelector('textarea[name=reason]')`);
  await evaluate(`document.querySelector('textarea[name=reason]').value='浏览器回归核对答案'`);
  await click('使此缓存失效');
  await wait(`!document.querySelector('dialog') && document.querySelector('#notifications')?.textContent.includes('缓存已失效')`);
  await navigate('audit', '操作记录');
  await wait(`document.querySelector('#page')?.textContent.includes('浏览器回归核对答案')`);
  await navigate('settings', '设置');
  await evaluate(`(() => {const palette=document.querySelector('select[aria-label=配色]');palette.value='grove';palette.dispatchEvent(new Event('change'));const mode=document.querySelector('select[aria-label=显示模式]');mode.value='dark';mode.dispatchEvent(new Event('change'));})()`);
  await call('Page.reload');
  await wait(`document.querySelector('#page h1')?.textContent==='设置' && !!document.querySelector('.appearance')`);
  assert.deepEqual(await evaluate(`({palette:document.documentElement.dataset.palette,mode:document.documentElement.dataset.mode})`), { palette: 'grove', mode: 'dark' }, '外观没有持久保存');
  await screenshot('settings-grove-dark');
  await evaluate(`(() => {const palette=document.querySelector('select[aria-label=配色]');palette.value='indigo';palette.dispatchEvent(new Event('change'));})()`);
  assert.equal(await evaluate('document.documentElement.dataset.palette'), 'indigo');
  await evaluate(`(() => {const palette=document.querySelector('select[aria-label=配色]');palette.value='graphite';palette.dispatchEvent(new Event('change'));const mode=document.querySelector('select[aria-label=显示模式]');mode.value='light';mode.dispatchEvent(new Event('change'));})()`);
  const mobile = [];
  await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
  for (const [route, heading] of [['overview', '运行总览'], ['offerings', '模型与价格'], ['requests', '查询记录'], ['cache', '精确缓存'], ['audit', '操作记录'], ['settings', '设置']]) {
    await navigate(route, heading);
    const sizes = await evaluate('({width:innerWidth,scroll:document.documentElement.scrollWidth})');
    assert.ok(sizes.scroll <= sizes.width, '手机页面出现整体横向溢出：' + route);
    mobile.push({ route, loaded: true, horizontal_overflow: false });
    await screenshot(route + '-mobile');
  }
  await click('退出登录');
  await login('viewer');
  await navigate('offerings', '模型与价格');
  assert.equal(await evaluate('document.querySelectorAll(".model-actions").length'), 0, '只读账号出现写操作');
  const viewerWrite = await evaluate(`(async()=>{const session=await fetch('/panel/api/session').then(value=>value.json());return (await fetch('/panel/api/models/primary',{method:'PATCH',headers:{'Content-Type':'application/json','X-CSRF-Token':session.csrf_token},body:JSON.stringify({enabled:false})})).status})()`);
  assert.equal(viewerWrite, 403, '只读账号绕过了服务端权限');
  await click('退出登录');
  await wait(`!!document.querySelector('input[name=password]')`);
  assert.deepEqual(exceptions, [], '页面存在未处理脚本异常');
  assert.deepEqual(serverErrors, [], '实际维护 API 返回服务器错误');
  const report = { source: '实际 PostgreSQL fork，无模拟管理 API', desktop: '通过', mobile, authentication: '独立 Cookie 与退出通过', permissions: '只读界面与服务端写保护通过', credentials: '响应无模型密钥和服务令牌', appearance: '石墨、青禾、靛蓝及深色持久化通过', mutations: '公开条目编辑、模型启停、功能测试、缓存失效和审计通过', exceptions, server_errors: serverErrors };
  await fs.writeFile(path.join(artifacts, 'browser-report.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify({ result: 'passed', mobile_pages: mobile.length, report: path.join(artifacts, 'browser-report.json') }));
} finally {
  socket?.close();
  chrome.kill('SIGTERM');
  if (chrome.exitCode === null) await Promise.race([once(chrome, 'exit'), pause(5000)]);
  if (chrome.exitCode === null) { chrome.kill('SIGKILL'); await once(chrome, 'exit').catch(() => {}); }
  await fs.rm(profile, { recursive: true, force: true });
  if (temporaryArtifacts) await fs.rm(artifacts, { recursive: true, force: true });
}
