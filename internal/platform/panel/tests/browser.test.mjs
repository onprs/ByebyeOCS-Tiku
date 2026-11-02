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
const providerBaseURL = process.env.BYEBYEOCS_TIKU_TEST_PROVIDER_BASE_URL;
let browserProviderID = '';
if (!providerBaseURL || !['127.0.0.1', 'localhost', '[::1]'].includes(new URL(providerBaseURL).hostname)) throw new Error('浏览器回归需要独立回环上游地址。');
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
    const detail = await evaluate(`({error:document.querySelector('dialog [role=alert]')?.textContent, notice:document.querySelector('#notifications')?.textContent, submit:document.querySelector('dialog button[type=submit]')?.textContent, invalid:[...document.querySelectorAll('dialog input, dialog select, dialog textarea')].filter(value=>!value.checkValidity()).map(value=>({name:value.name,message:value.validationMessage})), formLinked:!!document.querySelector('dialog button[type=submit]')?.form})`);
    throw new Error(`等待页面状态超时：${expression}；表单状态：${JSON.stringify(detail)}；服务端错误：${JSON.stringify(serverErrors)}`);
  };
  const click = label => evaluate(`(() => {
    const element = [...document.querySelectorAll('button')].find(value => (value.getAttribute('aria-label') || value.textContent.trim()) === ${JSON.stringify(label)} && !value.disabled);
    if (!element) throw new Error('未找到可用操作');
    const menu = element.closest('details');
    if (menu) menu.open = true;
    element.click();
  })()`);
  const key = async (name, code, virtualKey) => {
    const text = name === 'Enter' ? '\r' : undefined;
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: name, code, windowsVirtualKeyCode: virtualKey, ...(text ? { text } : {}) });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: name, code, windowsVirtualKeyCode: virtualKey });
  };
  const navigate = async (route, heading) => {
    await call('Page.navigate', { url: base + '/panel/#' + route });
    await wait(`document.querySelector('#page h1')?.textContent === ${JSON.stringify(heading)} && !document.querySelector('#page.is-loading')`);
    assert.equal(await evaluate(`!!document.querySelector('#page .error-state')`), false, '页面加载失败：' + route);
  };
  const screenshot = async name => {
    await evaluate(`Promise.allSettled(document.getAnimations().filter(value => value.effect?.getTiming().iterations !== Infinity).map(value => value.finished))`);
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
  await wait(`document.body.textContent.includes('累计用量与定价') && !!document.querySelector('.overview-charts .trend-svg')`);
  const overviewCharts = await evaluate(`(() => {const trend=document.querySelector('.overview-charts .trend-svg');const rows=document.querySelectorAll('.distribution-row');const invalid=[...document.querySelectorAll('.trend-svg path')].some(path=>/NaN|undefined/.test(path.getAttribute('d')||''));return {trend:!!trend,primaryLines:document.querySelectorAll('.chart-line-primary').length,cacheLines:document.querySelectorAll('.chart-line-cache').length,rows:rows.length,legends:document.querySelectorAll('.overview-charts .chart-legend').length,invalid};})()`);
  assert.ok(overviewCharts.trend && overviewCharts.primaryLines === 1 && overviewCharts.cacheLines === 1 && overviewCharts.rows >= 1 && overviewCharts.legends === 2 && !overviewCharts.invalid, '运行总览图表未正确渲染：' + JSON.stringify(overviewCharts));
  assert.equal(await evaluate(`document.body.textContent.includes('按模型累计')`), true, '累计用量缺少按模型明细');
  await screenshot('overview-desktop');
  await navigate('offerings', '模型与价格');
  await wait(`!!document.querySelector('.model-grid')`);
  const leaked = await evaluate(`(async () => {
    const responses = await Promise.all(['/models','/providers','/offerings','/settings'].map(value=>fetch('/panel/api'+value).then(response=>response.text())));
    return responses.some(value=>['isolated-upstream-key','isolated-fork-service-token','password_hash_file','api_key_file','service_token_file'].some(secret=>value.includes(secret)));
  })()`);
  assert.equal(leaked, false, '设置响应包含私密配置');
  await click('停用');
  await wait(`document.querySelector('.model-grid .card .model-name .status')?.textContent === '已停用'`);
  await click('启用');
  await wait(`document.querySelector('.model-grid .card .model-name .status')?.textContent === '已启用'`);
  const tab = label => evaluate(`([...document.querySelectorAll('[role=tab]')].find(value=>value.textContent.startsWith(${JSON.stringify(label)})) || (()=>{throw new Error('缺少选项卡')})()).click()`);
  await tab('公开价格');
  await wait(`document.querySelector('[role=tab][aria-selected=true]')?.textContent.startsWith('公开价格') && !document.querySelector('#page.is-loading')`);
  assert.ok(await evaluate(`document.querySelectorAll('.model-grid .card').length`) >= 3, '不同上游模型的公开条目没有全部展示');
  await click('编辑');
  await wait(`!!document.querySelector('dialog input[name=display_name]')`);
  assert.equal(await evaluate(`!!document.querySelector('dialog input[name=input_price]') && !!document.querySelector('dialog input[name=cache_ttl_days]') && !!document.querySelector('dialog select[aria-label=关联模型]') && !document.querySelector('dialog input[name=price_version]')`), true, '公开条目编辑字段不正确');
  await evaluate(`document.querySelector('dialog input[name=display_name]').value='浏览器核对公开模型'`);
  await click('保存');
  await wait(`!document.querySelector('dialog') && document.querySelector('#page')?.textContent.includes('浏览器核对公开模型')`);
  await screenshot('public-offerings-desktop');
  await tab('提供商');
  await wait(`document.querySelector('[role=tab][aria-selected=true]')?.textContent.startsWith('提供商') && !document.querySelector('#page.is-loading')`);
  await click('新增提供商');
  await wait(`!!document.querySelector('dialog input[name=name]')`);
  assert.equal(await evaluate(`!document.querySelector('dialog input[name=id]')`), true, '提供商表单仍要求填写内部标识');
  assert.equal(await evaluate(`document.querySelector('dialog button[type=submit]').form === document.querySelector('dialog form')`), true, '固定操作栏没有关联提供商表单');
  await evaluate(`document.querySelector('dialog [role=combobox][aria-label=接入协议]').focus()`);
  await key('ArrowDown', 'ArrowDown', 40);
  await wait(`!!document.querySelector('[role=listbox]')`);
  await key('Escape', 'Escape', 27);
  assert.equal(await evaluate(`!!document.querySelector('dialog') && !document.querySelector('[role=listbox]') && document.activeElement.getAttribute('role') === 'combobox'`), true, 'Esc 应只关闭选项并恢复焦点');
  assert.equal(await evaluate(`(() => {const footer=document.querySelector('dialog .dialog-actions').getBoundingClientRect();return footer.top >= 0 && footer.bottom <= innerHeight;})()`), true, '提供商编辑操作栏不在视口内');
  await evaluate(`(() => {
    const set=(name,value)=>{document.querySelector('dialog [name='+name+']').value=value;};
    set('name','浏览器提供商');set('vendor','openai');set('base_url',${JSON.stringify(providerBaseURL)});set('api_key','browser-secret-key');
  })()`);
  await click('测试连接');
  await wait(`document.querySelector('dialog [role=status]')?.textContent.includes('找到 2 个可用模型')`);
  await click('添加提供商');
  await wait(`document.querySelector('dialog h2')?.textContent === '上游模型' && !!document.querySelector('.discovery-item')`);
  const discoveryGeometry = await evaluate(`(() => {const dialog=document.querySelector('dialog.discovery-drawer');const list=dialog.querySelector('.discovery-list');return {width:dialog.getBoundingClientRect().width,listHeight:list.getBoundingClientRect().height,scrollbar:getComputedStyle(list).scrollbarWidth};})()`);
  assert.ok(discoveryGeometry.width >= 800 && discoveryGeometry.listHeight >= 500, '模型目录未使用完整侧栏空间：' + JSON.stringify(discoveryGeometry));
  assert.equal(discoveryGeometry.scrollbar, 'thin', '模型目录未使用自定义滚动条');
  await evaluate(`(() => {const search=document.querySelector('dialog input[name=model_search]');search.value='gpt-4o';search.dispatchEvent(new Event('input'));})()`);
  await wait(`document.querySelector('.discovery-item strong')?.textContent === 'GPT-4o'`);
  assert.equal(await evaluate(`document.querySelector('.discovery-item-meta')?.textContent.includes('上下文 128,000') && document.querySelector('.discovery-item-meta')?.textContent.includes('图片') && document.querySelector('.discovery-item-meta')?.textContent.includes('工具调用')`), true, '目录模型元数据或能力没有显示');
  await click('使用模型');
  await wait(`document.querySelector('dialog h2')?.textContent === '新增上游模型' && document.querySelector('dialog input[name=upstream_model]')?.value === 'gpt-4o'`);
  assert.deepEqual(await evaluate(`({name:document.querySelector('dialog input[name=display_name]').value,context:Number(document.querySelector('dialog input[name=context]').value),vision:document.querySelector('dialog input[name=vision]').checked,tool:document.querySelector('dialog input[name=tool_call]').checked})`), { name: 'GPT-4o', context: 128000, vision: true, tool: true }, '选择模型没有自动填充元数据和能力');
  browserProviderID = await evaluate(`document.querySelector('dialog select[aria-label=提供商]').value`);
  await click('取消');
  await wait(`document.querySelector('#page')?.textContent.includes('浏览器提供商') && !document.querySelector('dialog')`);
  for (const width of [320, 390, 768, 1024, 1280, 1440]) {
    await call('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: width <= 390 });
    const layout = await evaluate(`(() => {
      const row=document.querySelector('.provider-row');
      const meta=row.querySelector('.provider-meta').getBoundingClientRect();
      const address=row.querySelector('.provider-address').getBoundingClientRect();
      return { height: Math.round(row.getBoundingClientRect().height), meta: {top:meta.top,bottom:meta.bottom,left:meta.left,right:meta.right}, address: {top:address.top,bottom:address.bottom,left:address.left,right:address.right}, overlap: meta.left < address.right && address.left < meta.right && meta.top < address.bottom && address.top < meta.bottom,
        overflow: document.documentElement.scrollWidth > innerWidth + 1 };
    })()`);
    const expectedHeight = width <= 760 ? 188 : width <= 1100 ? 160 : 104;
    assert.equal(layout.height, expectedHeight, '提供商行高随内容变化：' + width);
    assert.equal(layout.overlap, false, '提供商地址与状态重叠：' + width + ' ' + JSON.stringify(layout));
    assert.equal(layout.overflow, false, '提供商页面横向溢出：' + width);
    await screenshot('providers-' + width);
  }
  await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  await screenshot('providers-desktop');
  await tab('模型');
  await wait(`document.querySelector('[role=tab][aria-selected=true]')?.textContent.startsWith('模型') && !document.querySelector('#page.is-loading')`);
  await click('新增上游模型');
  await wait(`!!document.querySelector('dialog input[name=id]')`);
  await evaluate(`(() => {
    const set=(name,value)=>{document.querySelector('dialog [name='+name+']').value=value;};
    set('id','browser-model');set('display_name','浏览器上游模型');set('upstream_model','browser-upstream');
    const provider=document.querySelector('dialog select[aria-label=提供商]');provider.value=${JSON.stringify(browserProviderID)};provider.dispatchEvent(new Event('change'));
  })()`);
  await click('添加模型');
  await wait(`document.querySelector('#page')?.textContent.includes('浏览器上游模型') && !document.querySelector('dialog')`);
  const modelRowStyle = await evaluate(`(() => {const card=[...document.querySelectorAll('.model-grid .card')].find(value=>value.textContent.includes('浏览器上游模型'));const label=card.querySelector(':scope > .provider-name');return {height:Math.round(card.getBoundingClientRect().height),name:label.textContent,overflow:getComputedStyle(label).textOverflow,whiteSpace:getComputedStyle(label).whiteSpace};})()`);
  assert.deepEqual(modelRowStyle, { height: 104, name: '浏览器提供商', overflow: 'ellipsis', whiteSpace: 'nowrap' }, '模型行高或提供商副标题样式不正确');
  await tab('提供商');
  await wait(`document.querySelector('[role=tab][aria-selected=true]')?.textContent.startsWith('提供商') && !document.querySelector('#page.is-loading')`);
  await evaluate(`(() => {const card=[...document.querySelectorAll('.provider-row')].find(value=>value.textContent.includes('浏览器提供商'));const action=[...card.querySelectorAll('button')].find(value=>value.textContent.trim()==='获取模型');if(!action)throw new Error('提供商模型目录入口缺失');action.click();})()`);
  await wait(`!![...document.querySelectorAll('.discovery-item')].find(row=>row.textContent.includes('browser-upstream'))`);
  assert.deepEqual(await evaluate(`(() => {const row=[...document.querySelectorAll('.discovery-item')].find(value=>value.textContent.includes('browser-upstream'));const action=row.querySelector('button');return {text:action.textContent.trim(),disabled:action.disabled};})()`), { text: '已添加', disabled: true }, '已配置的上游模型仍显示可选择状态');
  await key('Escape', 'Escape', 27);
  await wait(`!document.querySelector('dialog')`);
  await tab('模型');
  await wait(`document.querySelector('[role=tab][aria-selected=true]')?.textContent.startsWith('模型') && !document.querySelector('#page.is-loading')`);
  await evaluate(`(() => {window.fetchOriginal=window.fetch;window.fetch=async (request,options)=>{if(String(request).includes('/panel/api/model-pricing?')){await new Promise(resolve=>setTimeout(resolve,250));return new Response(JSON.stringify({source:'models.dev',source_url:'https://models.dev/api.json',exchange_source:'Frankfurter',exchange_url:'https://api.frankfurter.dev/v2/rate/usd/cny',exchange_date:'2026-10-30',exchange_rate:'6.6572',provider_id:'deepseek',model_id:'deepseek-v4-flash',model_name:'DeepSeek V4.1 Flash',rates:{input_micros_per_million_tokens:'1234567',output_micros_per_million_tokens:'2345678',cache_read_micros_per_million_tokens:'345678',cache_write_micros_per_million_tokens:'456789'}}),{status:200,headers:{'Content-Type':'application/json'}});}return window.fetchOriginal(request,options);};})()`);
  await evaluate(`(() => {const card=[...document.querySelectorAll('.model-grid .card')].find(value=>value.textContent.includes('浏览器上游模型'));const publish=[...card.querySelectorAll('button')].find(value=>value.textContent.trim()==='发布');if(!publish)throw new Error('未发布模型缺少发布操作');publish.click();})()`);
  await wait(`document.querySelector('dialog h2')?.textContent === '发布公开模型'`);
  assert.equal(await evaluate(`!!document.querySelector('dialog input[name=cache_read_price]') && !!document.querySelector('dialog input[name=cache_write_price]') && [...document.querySelectorAll('dialog button')].some(value=>value.textContent.trim()==='自动获取并填入参考价')`), true, '发布模型表单缺少缓存价格或自动参考价入口');
  const pricingLoading = await wait(`(() => {const spinner=document.querySelector('.price-reference[data-state=loading] .reference-spinner');if(!spinner)return false;const fetchButton=[...document.querySelectorAll('dialog button')].find(value=>value.textContent.trim()==='自动获取并填入参考价');return {animation:getComputedStyle(spinner).animationDuration,busy:fetchButton?.disabled===true};})()`);
  assert.ok(parseFloat(pricingLoading.animation) > 0, '自动获取参考价时加载动画没有运行：' + JSON.stringify(pricingLoading));
  assert.equal(pricingLoading.busy, true, '自动获取参考价时按钮没有进入等待状态');
  await wait(`(() => JSON.stringify([...document.querySelectorAll('dialog input')].filter(value=>value.name.endsWith('_price')).map(value=>value.value)) === JSON.stringify(['1.234567','2.345678','0.345678','0.456789']))()`);
  assert.equal(await evaluate(`(() => {const text=document.querySelector('.price-reference')?.textContent||'';return text.includes('2026-10-30') && text.includes('1 USD = 6.6572 CNY') && !text.includes('未收录') && !text.includes('可手动填写');})()`), true, '自动获取状态没有紧凑显示来源与汇率');
  const priceLayout = await evaluate(`(() => {const row=document.querySelector('.price-reference-row');const button=row?.querySelector('button');const status=row?.querySelector('.price-reference');if(!row||!button||!status)return false;const b=button.getBoundingClientRect(),s=status.getBoundingClientRect();return {overlap:!(s.right<=b.left||s.left>=b.right||s.bottom<=b.top||s.top>=b.bottom),buttonBottom:Math.round(b.bottom),statusTop:Math.round(s.top)};})()`);
  assert.equal(priceLayout.overlap, false, '自动获取提示与按钮重叠：' + JSON.stringify(priceLayout));
  await evaluate(`(() => {const field=document.querySelector('dialog input[name=input_price]');field.value='9';field.dispatchEvent(new Event('input',{bubbles:true}));})()`);
  await click('自动获取并填入参考价');
  await wait(`document.querySelector('.price-reference[data-state=loading]')`);
  await wait(`document.querySelector('dialog input[name=input_price]').value==='1.234567'`);
  await wait(`(() => JSON.stringify([...document.querySelectorAll('dialog input')].filter(value=>value.name.endsWith('_price')).map(value=>value.value)) === JSON.stringify(['1.234567','2.345678','0.345678','0.456789']))()`);
  assert.equal(await evaluate(`document.querySelector('.price-reference')?.textContent.includes('1 USD = 6.6572 CNY')`), true, '参考汇率来源与日期没有显示');
  await evaluate(`(() => {window.pricingMock=window.fetch;window.fetch=async (request,options)=>String(request).includes('/panel/api/model-pricing?')?new Response(JSON.stringify({error:{code:'pricing_incomplete',message:'模型价格来源没有完整的输入与输出价格'}}),{status:404,headers:{'Content-Type':'application/json'}}):window.pricingMock(request,options);})()`);
  await click('自动获取并填入参考价');
  const pricingFailure = await wait(`(() => {const node=document.querySelector('.price-reference[data-state=warn]');return node?node.textContent:false;})()`);
  assert.equal(pricingFailure, '暂无可用参考价', '参考价获取失败没有使用简短界面状态：' + pricingFailure);
  await evaluate(`window.fetch=window.pricingMock`);
  await evaluate(`(() => {for(const [name,value] of Object.entries({input_price:'1',output_price:'2',cache_read_price:'0.5',cache_write_price:'1.5'})){const field=document.querySelector('dialog input[name='+name+']');field.value=value;field.dispatchEvent(new Event('input',{bubbles:true}));}})()`);
  await evaluate(`document.querySelector('dialog button[type=submit]')?.click()`);
  await wait(`!document.querySelector('dialog') && document.querySelector('#notifications')?.textContent.includes('公开模型已发布')`);
  assert.equal(await evaluate(`(() => {const card=[...document.querySelectorAll('.model-grid .card')].find(value=>value.textContent.includes('浏览器上游模型'));return [...card.querySelectorAll('button')].some(value=>value.textContent.trim()==='编辑公开条目') && ![...card.querySelectorAll('button')].some(value=>['发布','新增公开条目'].includes(value.textContent.trim()));})()`), true, '已关联条目的模型仍显示新增公开条目操作');
  await evaluate(`(() => {const card=[...document.querySelectorAll('.model-grid .card')].find(value=>value.textContent.includes('浏览器上游模型'));const edit=[...card.querySelectorAll('button')].find(value=>value.textContent.trim()==='编辑公开条目');if(!edit)throw new Error('已发布模型缺少编辑操作');edit.click();})()`);
  await wait(`document.querySelector('dialog h2')?.textContent === '编辑公开模型'`);
  await click('取消');
  await wait(`!document.querySelector('dialog')`);
  await tab('公开价格');
  await wait(`document.querySelector('[role=tab][aria-selected=true]')?.textContent.startsWith('公开价格') && !document.querySelector('#page.is-loading')`);
  await wait(`!![...document.querySelectorAll('.model-grid > .card')].find(card=>card.textContent.includes('浏览器上游模型'))`);
  const deleteControl = await evaluate(`(async () => {const card=[...document.querySelectorAll('.model-grid > .card')].find(value=>value.textContent.includes('浏览器上游模型'));const script=await fetch('/panel/app.js').then(response=>response.text());return {present:!!card?.querySelector('[aria-label="删除公开条目"]'),buttons:[...(card?.querySelectorAll('button')||[])].map(value=>({text:value.textContent.trim(),label:value.getAttribute('aria-label')})),scriptMarker:script.includes('公开条目已删除，历史价格与账单记录保留')};})()`);
  assert.equal(deleteControl.present, true, '公开价格条目缺少单独删除入口：' + JSON.stringify(deleteControl));
  await evaluate(`(() => {const card=[...document.querySelectorAll('.model-grid > .card')].find(value=>value.textContent.includes('浏览器上游模型'));card.querySelector('[aria-label="删除公开条目"]').click();})()`);
  await wait(`document.querySelector('dialog h2')?.textContent === '删除公开条目'`);
  const offeringDeletePrompt = await evaluate(`document.querySelector('dialog')?.textContent || ''`);
  assert.match(offeringDeletePrompt, /冻结价格及账单保留/, '删除确认没有说明历史账务保留：' + offeringDeletePrompt);
  await evaluate(`(() => {const confirm=[...document.querySelectorAll('dialog .dialog-actions button')].find(value=>value.textContent.trim()==='删除公开条目');if(!confirm)throw new Error('删除确认按钮缺失');confirm.click();})()`);
  await wait(`!document.querySelector('dialog') && document.querySelector('#notifications')?.textContent.includes('公开条目已删除') && ![...document.querySelectorAll('.model-grid > .card')].some(card=>card.textContent.includes('浏览器上游模型'))`);
  await tab('模型');
  await wait(`document.querySelector('[role=tab][aria-selected=true]')?.textContent.startsWith('模型') && !document.querySelector('#page.is-loading')`);
  await wait(`!![...document.querySelectorAll('.model-grid > .card')].find(card=>card.textContent.includes('浏览器上游模型'))`);
  await evaluate(`(() => {const card=[...document.querySelectorAll('.model-grid .card')].find(value=>value.textContent.includes('浏览器上游模型'));const publish=[...card.querySelectorAll('button')].find(value=>value.textContent.trim()==='发布');if(!publish)throw new Error('删除条目后模型绑定未释放');publish.click();})()`);
  await wait(`document.querySelector('dialog h2')?.textContent === '发布公开模型'`);
  await wait(`document.querySelector('.price-reference[data-state=ok]')`);
  await evaluate(`(() => {for(const [name,value] of Object.entries({input_price:'1',output_price:'2'})){const field=document.querySelector('dialog input[name='+name+']');field.value=value;field.dispatchEvent(new Event('input',{bubbles:true}));}})()`);
  await evaluate(`document.querySelector('dialog button[type=submit]')?.click()`);
  await wait(`!document.querySelector('dialog') && document.querySelector('#notifications')?.textContent.includes('公开模型已发布')`);
  await wait(`!!document.querySelector('details.row-menu')`);
  const menuGeometry = await evaluate(`(() => {const row=[...document.querySelectorAll('.model-grid > .card')].find(value=>value.textContent.includes('浏览器上游模型'));const menu=row.querySelector('details.row-menu');menu.open=true;const box=menu.querySelector(':scope > div').getBoundingClientRect();const next=row.nextElementSibling;const transition=getComputedStyle(menu.querySelector(':scope > div')).transitionDuration;return { open:row.matches(':has(.row-menu[open])'),rowBottom:row.getBoundingClientRect().bottom,menuTop:box.top,menuBottom:box.bottom,nextTop:next?.getBoundingClientRect().top,transition,visible:box.width>0&&box.height>0&&box.top>=0&&box.bottom<=innerHeight};})()`);
  assert.ok(menuGeometry.transition.split(',').some(value=>parseFloat(value) > 0), '更多操作菜单缺少展开与收起动画');
  assert.equal(menuGeometry.open, true, '展开菜单时行容器没有切换为可溢出状态');
  assert.equal(menuGeometry.visible, true, '更多操作菜单超出视口或被裁剪');
  assert.ok(menuGeometry.menuTop < menuGeometry.rowBottom, '更多操作菜单没有锚定在当前模型行');
  assert.ok(!menuGeometry.nextTop || menuGeometry.menuBottom <= menuGeometry.nextTop, '展开菜单被下一行覆盖');
  await evaluate(`(() => {const row=[...document.querySelectorAll('.model-grid > .card')].find(value=>value.textContent.includes('浏览器上游模型'));const menu=row.querySelector('details.row-menu');[...menu.querySelectorAll('button')].find(value=>value.textContent.trim()==='删除模型').click();})()`);
  await wait(`document.querySelector('dialog h2')?.textContent === '删除上游模型'`);
  const deletePrompt = await evaluate(`document.querySelector('dialog')?.textContent || ''`);
  assert.match(deletePrompt, /关联 \d+ 个公开条目（\d+ 个对用户可见）。确认后全部从当前目录移除/, '删除提示没有说明关联条目处理方式：' + deletePrompt);
  await evaluate(`document.querySelector('dialog .dialog-actions button.danger')?.click()`);
  await wait(`!document.querySelector('#page')?.textContent.includes('浏览器上游模型') && document.querySelector('#notifications')?.textContent.includes('1 个公开条目已移除')`);
  await tab('公开价格');
  await wait(`document.querySelector('[role=tab][aria-selected=true]')?.textContent.startsWith('公开价格') && !document.querySelector('#page.is-loading')`);
  await wait(`!document.querySelector('#page')?.textContent.includes('浏览器上游模型') && !document.querySelector('#page.is-loading')`);
  await tab('提供商');
  await wait(`document.querySelector('#page')?.textContent.includes(${JSON.stringify(browserProviderID)}) && !document.querySelector('#page.is-loading')`);
  await click('删除'); await click('删除提供商');
  await wait(`!document.querySelector('#page')?.textContent.includes(${JSON.stringify(browserProviderID)}) && !document.querySelector('dialog')`);
  await tab('模型');
  await wait(`document.querySelector('[role=tab][aria-selected=true]')?.textContent.startsWith('模型') && !document.querySelector('#page.is-loading')`);
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
  await wait(`document.querySelectorAll('.palette-option').length===3 && document.querySelectorAll('.theme-tile').length===3`);
  const appearanceSizes = await evaluate(`(() => {const size=value=>[Math.round(value.getBoundingClientRect().width),Math.round(value.getBoundingClientRect().height)];return {palette:[...document.querySelectorAll('.palette-option')].map(size),theme:[...document.querySelectorAll('.theme-tile')].map(size)};})()`);
  const [paletteWidth, paletteHeight] = appearanceSizes.palette[0];
  assert.ok(appearanceSizes.palette.every(([width, height]) => Math.abs(width - paletteWidth) <= 1 && Math.abs(height - paletteHeight) <= 1), '配色卡片尺寸不一致：' + JSON.stringify(appearanceSizes));
  assert.ok(appearanceSizes.theme.every(([width, height]) => Math.abs(width - paletteWidth) <= 1 && Math.abs(height - paletteHeight) <= 1), '主题卡片与配色卡片尺寸不一致：' + JSON.stringify(appearanceSizes));
  await click('使用青禾配色');
  await click('使用深色');
  await call('Page.reload');
  await wait(`document.querySelector('#page h1')?.textContent==='设置' && document.querySelectorAll('.palette-option').length===3`);
  assert.deepEqual(await evaluate(`({palette:document.documentElement.dataset.palette,mode:document.documentElement.dataset.mode,selected:[...document.querySelectorAll('.palette-option[aria-pressed=true]')].length})`), { palette: 'grove', mode: 'dark', selected: 1 }, '外观没有持久保存');
  await screenshot('settings-grove-dark');
  await click('使用靛蓝配色');
  assert.equal(await evaluate('document.documentElement.dataset.palette'), 'indigo');
  await click('使用石墨配色');
  await click('使用浅色');

  // 服务设置保存；价格缓存刷新使用本地成功夹具，避免浏览器测试访问外部来源。
  await evaluate(`(() => {for(const [name,value] of Object.entries({concurrency:'3',timeout_seconds:'20',max_output_tokens:'512',cache_ttl_days:'5',cache_max_ttl_days:'20',pricing_catalog_ttl_hours:'12',pricing_exchange_ttl_hours:'36'})){const field=document.querySelector('input[name='+name+']');field.value=value;field.dispatchEvent(new Event('input',{bubbles:true}));}})()`);
  await click('保存设置');
  await wait(`document.querySelector('#notifications')?.textContent.includes('设置已保存') && !document.querySelector('.settings-note')?.hidden`);
  assert.match(await evaluate(`document.querySelector('.settings-note')?.textContent||''`), /重启题库服务后生效/);
  await evaluate(`(() => {window.fetchOriginalSettings=window.fetch;window.fetch=async (request,options)=>String(request).includes('/panel/api/settings/pricing-cache/refresh')?new Response(JSON.stringify({pricing_cache:{catalog_ttl_hours:12,exchange_ttl_hours:36,catalog_cached_at:'2026-10-31T12:00:00Z',exchange_cached_at:'2026-10-31T12:00:00Z',catalog_entries:235,exchange_date:'2026-10-31',exchange_rate:'6.6571',stale:false},audit_pending:false}),{status:200,headers:{'Content-Type':'application/json'}}):window.fetchOriginalSettings(request,options);})()`);
  await click('立即刷新');
  await wait(`document.querySelector('#notifications')?.textContent.includes('价格缓存已刷新') && document.querySelector('.settings-cache-status')?.textContent.includes('235 个提供商')`);
  await evaluate(`window.fetch=window.fetchOriginalSettings`);
  for (const width of [1920, 1280, 1024, 768]) {
    await call('Emulation.setDeviceMetricsOverride', { width, height: 1000, deviceScaleFactor: 1, mobile: false });
    await navigate('settings', '设置');
    assert.equal(await evaluate('document.documentElement.scrollWidth <= innerWidth + 1'), true, '设置页横向溢出：' + width);
  }
  const mobile = [];
  await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
  for (const [route, heading] of [['overview', '运行总览'], ['offerings', '模型与价格'], ['requests', '查询记录'], ['cache', '精确缓存'], ['audit', '操作记录'], ['settings', '设置']]) {
    await navigate(route, heading);
    const sizes = await evaluate('({width:innerWidth,scroll:document.documentElement.scrollWidth})');
    assert.ok(sizes.scroll <= sizes.width, '手机页面出现整体横向溢出：' + route);
    mobile.push({ route, loaded: true, horizontal_overflow: false });
    if (route === 'offerings') {
      await tab('模型');
      await wait(`document.querySelector('[role=tab][aria-selected=true]')?.textContent.startsWith('模型') && !document.querySelector('#page.is-loading')`);
      const rowHeights = await evaluate(`[...document.querySelectorAll('.model-grid > .card')].map(row=>Math.round(row.getBoundingClientRect().height))`);
      assert.ok(rowHeights.every(height => height === 188), '手机模型行高不固定：' + JSON.stringify(rowHeights));
      await screenshot('offerings-models-mobile');
    } else {
      await screenshot(route + '-mobile');
    }
  }
  for (const width of [1920, 1280, 1024, 768]) {
    await call('Emulation.setDeviceMetricsOverride', { width, height: 1000, deviceScaleFactor: 1, mobile: false });
    await navigate('offerings', '模型与价格');
    assert.equal(await evaluate('document.documentElement.scrollWidth <= innerWidth + 1'), true, '页面横向溢出：' + width);
    await screenshot('offerings-' + width);
  }
  await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  await navigate('settings', '设置');
  await evaluate(`document.querySelector('[aria-label="使用青禾配色"]').focus()`);
  await key('Enter', 'Enter', 13);
  assert.equal(await evaluate('document.documentElement.dataset.colorScheme'), 'grove', '键盘选择未保存配色');
  for (const [scheme, label] of [['graphite', '石墨'], ['grove', '青禾'], ['indigo', '靛蓝']]) for (const [mode, modeLabel] of [['light', '浅色'], ['dark', '深色']]) {
    await click('使用' + label + '配色');
    await click('使用' + modeLabel);
    await screenshot('settings-' + scheme + '-' + mode);
  }
  await click('退出登录');
  await login('viewer');
  await navigate('offerings', '模型与价格');
  assert.equal(await evaluate(`!![...document.querySelectorAll('#page button')].find(value=>['新增上游模型','新增提供商','发布','编辑配置','删除模型'].includes(value.textContent.trim()))`), false, '只读账号出现写操作');
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
  await fs.rm(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
  if (temporaryArtifacts) await fs.rm(artifacts, { recursive: true, force: true });
}
