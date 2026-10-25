const root = document.querySelector('#app');
const notices = document.querySelector('#notifications');
const preferenceKey = 'byebyeocs-tiku-appearance';
const media = matchMedia('(prefers-color-scheme: dark)');
let appearance = { palette: 'graphite', mode: 'system' };
try { appearance = { ...appearance, ...JSON.parse(localStorage.getItem(preferenceKey) || '{}') }; } catch { }
function applyAppearance() {
  if (!['graphite', 'grove', 'indigo'].includes(appearance.palette)) appearance.palette = 'graphite';
  if (!['light', 'dark', 'system'].includes(appearance.mode)) appearance.mode = 'system';
  document.documentElement.dataset.palette = appearance.palette;
  document.documentElement.dataset.mode = appearance.mode === 'system' ? (media.matches ? 'dark' : 'light') : appearance.mode;
}
applyAppearance();
media.addEventListener('change', applyAppearance);
let session;
let epoch = 0;
let currentDialog;
const navigation = [
  ['overview', '运行总览', 'M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z'],
  ['offerings', '模型与价格', 'M8 3v3 M16 3v3 M8 18v3 M16 18v3 M3 8h3 M18 8h3 M3 16h3 M18 16h3 M6 6h12v12H6z M10 10h4v4h-4z'],
  ['requests', '查询记录', 'M6 3h9l4 4v14H6z M14 3v5h5 M9 12h7 M9 16h7'],
  ['cache', '精确缓存', 'M4 5c0-4 16-4 16 0s-16 4-16 0v14c0 4 16 4 16 0V5 M4 12c0 4 16 4 16 0'],
  ['audit', '操作记录', 'M9 3h6v4H9z M9 5H5v16h14V5h-4 M8 12h8 M8 16h5'],
  ['settings', '设置', 'M12 8a4 4 0 1 0 0 8a4 4 0 0 0 0-8 M12 2v3 M12 19v3 M2 12h3 M19 12h3 M5 5l2 2 M17 17l2 2 M5 19l2-2 M17 7l2-2'],
];
const statusLabels = { answered: '已回答', cache_hit: '缓存命中', usage_incomplete: '用量不完整', pending: '处理中', completed: '已完成', failed: '失败', unknown: '结果待核对', active: '有效', expired: '已过期', invalidated: '已失效' };
const errorLabels = { rate_limited: '上游请求受限', upstream_timeout: '上游响应超时', empty_answer: '模型返回空答案', model_disabled: '模型已停用', usage_incomplete: '用量不完整', request_cancelled: '请求已取消', model_unavailable: '模型暂不可用' };
const actionLabels = { 'session.login': '登录', 'session.logout': '退出登录', 'model.state': '调整模型状态', 'model.test.request': '开始模型测试', 'model.test.result': '模型测试结束', 'offering.create': '发布公开条目', 'offering.update': '更新公开条目', 'offering.retire': '停用公开条目', 'offering.test.request': '开始公开条目测试', 'offering.test.result': '公开条目测试结束', 'cache.invalidate': '使缓存失效' };
const pageState = new Map();
const formatNumber = value => value == null ? '未返回' : Number(value).toLocaleString('zh-CN');
const formatTime = value => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
const shorten = value => value && value.length > 20 ? value.slice(0, 10) + '…' + value.slice(-6) : value || '—';
function node(tag, className, text) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text != null) element.textContent = text;
  return element;
}
function icon(path) {
  const element = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  element.setAttribute('viewBox', '0 0 24 24');
  element.setAttribute('fill', 'none');
  element.setAttribute('stroke', 'currentColor');
  element.setAttribute('stroke-width', '1.6');
  element.setAttribute('stroke-linecap', 'round');
  element.setAttribute('stroke-linejoin', 'round');
  element.setAttribute('aria-hidden', 'true');
  const shape = document.createElementNS(element.namespaceURI, 'path');
  shape.setAttribute('d', path);
  element.append(shape);
  return element;
}
function button(label, action, className = '') {
  const element = node('button', className, label);
  element.type = 'button';
  element.addEventListener('click', () => action(element));
  return element;
}
function brand() {
  const element = node('div', 'brand');
  const mark = node('span', 'brand-mark');
  mark.append(icon('M4 4h6c2 0 2 2 2 2s0-2 2-2h6v15h-6c-2 0-2 2-2 2s0-2-2-2H4z M12 6v15'));
  element.append(mark, node('span', '', 'ByebyeOCS'));
  return element;
}
function notify(message, bad = false) {
  const element = node('div', bad ? 'notice bad' : 'notice', message);
  notices.append(element);
  setTimeout(() => element.remove(), 5000);
}
async function api(path, options = {}) {
  const headers = { Accept: 'application/json', ...options.headers };
  if (options.body) headers['Content-Type'] = 'application/json';
  if (session?.csrf_token && options.method && options.method !== 'GET') headers['X-CSRF-Token'] = session.csrf_token;
  let response;
  try {
    response = await fetch('/panel/api' + path, { ...options, headers, credentials: 'same-origin', signal: AbortSignal.timeout(45000) });
  } catch { throw new Error('连接未完成，请检查网络后重试'); }
  const data = await response.json().catch(() => null);
  if (!response.ok) {
    if (response.status === 401 && path !== '/login') { session = undefined; renderLogin(); }
    throw new Error(data?.error?.message || '操作未完成，请稍后重试');
  }
  return data;
}
function field(label, input) {
  const wrapper = node('div', 'field');
  const title = node('label', '', label);
  if (!input.id) input.id = 'field-' + crypto.randomUUID();
  title.htmlFor = input.id;
  wrapper.append(title, input);
  return wrapper;
}
function input(type, name, autocomplete) {
  const element = node('input');
  element.type = type;
  element.name = name;
  element.required = true;
  if (autocomplete) element.autocomplete = autocomplete;
  return element;
}
function renderLogin() {
  epoch++;
  currentDialog?.close();
  root.replaceChildren();
  const wrapper = node('main', 'login-wrap');
  const card = node('section', 'login');
  card.append(brand(), node('h1', '', '题库维护'), node('p', 'muted', '登录后查看服务状态与管理模型。'));
  const form = node('form');
  const username = input('text', 'username', 'username');
  const password = input('password', 'password', 'current-password');
  username.maxLength = 64;
  const error = node('div', 'form-error');
  error.setAttribute('role', 'alert');
  const submit = node('button', 'primary', '登录');
  submit.type = 'submit';
  form.append(field('账号', username), field('密码', password), error, submit);
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (submit.disabled) return;
    submit.disabled = true;
    error.textContent = '';
    try {
      session = await api('/login', { method: 'POST', body: JSON.stringify({ username: username.value, password: password.value }) });
      password.value = '';
      renderShell();
    } catch (failure) { error.textContent = failure.message; } finally { submit.disabled = false; }
  });
  card.append(form);
  wrapper.append(card);
  root.append(wrapper);
}
function renderShell() {
  root.replaceChildren();
  const shell = node('div', 'shell');
  const sidebar = node('aside', 'sidebar');
  sidebar.append(brand(), node('p', 'brand-subtitle', '题库维护'));
  const nav = node('nav', 'navigation');
  nav.setAttribute('aria-label', '主要导航');
  for (const [key, title, shape] of navigation) {
    const link = node('a', '', title);
    link.href = '#' + key;
    link.dataset.route = key;
    link.prepend(icon(shape));
    nav.append(link);
  }
  sidebar.append(nav, node('div', 'sidebar-footer', '独立题库服务'));
  const workspace = node('div', 'workspace');
  const top = node('header', 'topbar');
  top.append(node('span', 'small muted', '题库控制台'));
  const account = node('div', 'account');
  account.append(node('span', '', session.username), node('span', 'status', session.role === 'maintainer' ? '维护员' : '只读账号'));
  account.append(button('退出登录', async element => {
    element.disabled = true;
    try { await api('/logout', { method: 'POST', body: '{}' }); session = undefined; renderLogin(); } catch (error) { notify(error.message, true); element.disabled = false; }
  }));
  top.append(account);
  const main = node('main', 'page');
  main.id = 'page';
  workspace.append(top, main);
  shell.append(sidebar, workspace);
  root.append(shell);
  loadPage();
}
function badge(status) {
  let tone = 'status';
  if (['answered', 'cache_hit', 'active', 'ok'].includes(status)) tone += ' good';
  else if (['usage_incomplete', 'pending', 'unknown'].includes(status)) tone += ' warn';
  else if (status === 'failed') tone += ' bad';
  return node('span', tone, statusLabels[status] || (status === 'ok' ? '测试通过' : status));
}
function detailGrid(entries) {
  const element = node('dl', 'detail-grid');
  for (const [label, value] of entries) element.append(node('dt', '', label), node('dd', '', String(value ?? '—')));
  return element;
}
function table(headers, rows) {
  if (!rows.length) return node('div', 'card empty', '暂无记录');
  const wrapper = node('div', 'table-wrap');
  const element = node('table');
  const head = node('thead');
  const headRow = node('tr');
  headers.forEach(label => headRow.append(node('th', '', label)));
  head.append(headRow);
  const body = node('tbody');
  for (const values of rows) {
    const row = node('tr');
    for (const value of values) {
      const cell = node('td');
      cell.append(value instanceof Node ? value : document.createTextNode(String(value ?? '—')));
      row.append(cell);
    }
    body.append(row);
  }
  element.append(head, body);
  wrapper.append(element);
  return wrapper;
}
function select(label, choices, selected, onChange) {
  const element = node('select');
  element.setAttribute('aria-label', label);
  for (const [value, title] of choices) {
    const option = node('option', '', title);
    option.value = value;
    option.selected = value === selected;
    element.append(option);
  }
  element.addEventListener('change', () => onChange(element.value));
  return element;
}
function openDialog(title) {
  currentDialog?.close();
  const dialog = node('dialog');
  currentDialog = dialog;
  const heading = node('div', 'dialog-heading');
  const titleNode = node('h2', '', title);
  titleNode.id = 'dialog-title';
  dialog.setAttribute('aria-labelledby', titleNode.id);
  heading.append(titleNode, button('关闭', () => dialog.close()));
  dialog.append(heading);
  document.body.append(dialog);
  dialog.addEventListener('close', () => { dialog.remove(); if (currentDialog === dialog) currentDialog = undefined; }, { once: true });
  dialog.showModal();
  return dialog;
}
async function showRequest(id) {
  const dialog = openDialog('查询详情');
  const state = node('p', 'muted', '正在读取…');
  dialog.append(state);
  try {
    const value = await api('/requests/' + encodeURIComponent(id));
    state.remove();
    dialog.append(detailGrid([
      ['请求编号', value.id], ['模型', value.model_id || '未记录'], ['状态', statusLabels[value.status] || value.status],
      ['查询模式', value.mode === 'cache_first' ? '优先使用缓存' : value.mode === 'always_llm' ? '每次使用模型' : '未记录'],
      ['缓存范围', value.cache_scope === 'shared' ? '共享' : value.cache_scope === 'user' ? '用户隔离' : '未记录'],
      ['输入 / 输出 token', formatNumber(value.usage?.input_tokens) + ' / ' + formatNumber(value.usage?.output_tokens)],
      ['用量完整性', value.usage?.complete === true ? '完整' : '未完整返回'], ['请求时间', formatTime(value.created_at)],
    ]));
    if (value.error_code) dialog.append(node('p', 'form-error', errorLabels[value.error_code] || '请求未成功，请核对服务状态'));
    dialog.append(node('h3', '', '答案'), node('div', 'detail-text', value.answer || '暂无可用答案内容'));
    if (value.calls?.length) {
      dialog.append(node('h3', '', '模型调用'));
      dialog.append(table(['用途', '输入', '输出', '用量'], value.calls.map(call => [call.slot === 'caption' ? '图片转述' : '回答', formatNumber(call.input_tokens), formatNumber(call.output_tokens), call.usage_complete ? '完整' : '不完整'])));
    }
  } catch (error) { state.textContent = error.message; state.className = 'form-error'; }
}
function requestsTable(items) {
  return table(['请求编号', '模型', '状态', 'token', '请求时间'], items.map(value => [
    button(shorten(value.id), () => showRequest(value.id), 'link mono'), value.model_id || '未记录', badge(value.status),
    value.usage?.complete === true ? formatNumber(value.usage.total_tokens) : '未完整返回', formatTime(value.created_at),
  ]));
}
async function renderOverview(page) {
  const [summary, recent] = await Promise.all([api('/overview'), api('/requests?limit=6')]);
  const stats = node('div', 'stats');
  const values = [
    ['近24小时查询', formatNumber(summary.queries), `处理中 ${formatNumber(summary.pending)} · 异常 ${formatNumber(summary.failed)}`],
    ['缓存命中', formatNumber(summary.cache_hits), '近24小时查询'],
    ['已知 token 用量', formatNumber(summary.input_tokens + summary.output_tokens), '近24小时输入与输出'],
    ['用量不完整', formatNumber(summary.incomplete), '需要核对用量的查询'],
  ];
  for (const [label, value, note] of values) {
    const stat = node('div', 'stat');
    stat.append(node('div', 'stat-label', label), node('div', 'stat-number', value), node('div', 'stat-note', note));
    stats.append(stat);
  }
  page.append(stats);
  const card = node('section', 'card');
  const heading = node('div', 'card-header');
  heading.append(node('h2', '', '最近查询'), node('span', 'small muted', `当前有效缓存 ${formatNumber(summary.active_cache)} 条`));
  card.append(heading, requestsTable(recent.items));
  page.append(card);
}
function modelTest(model, functional) {
  const dialog = openDialog(functional ? '功能测试' : '连通测试');
  dialog.append(node('p', '', model.display_name));
  let checkbox;
  if (functional) {
    dialog.append(node('p', 'muted', '功能测试会请求模型生成内容，并产生模型用量。'));
    const check = node('div', 'check-row');
    checkbox = input('checkbox', 'confirm_cost');
    checkbox.id = 'confirm-cost';
    checkbox.required = true;
    const label = node('label', '', '我已了解本次测试会产生用量');
    label.htmlFor = checkbox.id;
    check.append(checkbox, label);
    dialog.append(check);
  }
  const result = node('p', 'muted');
  const footer = node('div', 'dialog-footer');
  const submit = button('开始测试', async element => {
    if (functional && !checkbox.checked) return;
    element.disabled = true;
    result.textContent = '正在测试…';
    try {
      const value = await api('/models/' + encodeURIComponent(model.id) + '/tests', { method: 'POST', body: JSON.stringify({ kind: functional ? 'functional' : 'reachability', confirm_cost: functional }) });
      result.textContent = value.status === 'ok' ? `测试通过，用时 ${formatNumber(value.latency_ms)} 毫秒` : errorLabels[value.error_code] || '测试失败，请核对模型服务状态';
      if (functional && value.usage?.complete === false) result.textContent += '；用量未完整返回';
      if (value.usage?.complete === true) result.textContent += `，共 ${formatNumber(value.usage.total_tokens)} token`;
      element.textContent = '测试已结束';
    } catch (error) { result.textContent = error.message; element.textContent = '本次测试未完成'; }
  }, 'primary');
  if (functional) { submit.disabled = true; checkbox.addEventListener('change', () => { submit.disabled = !checkbox.checked; }); }
  footer.append(submit);
  dialog.append(result, footer);
}
const protocolLabels = { 'openai-chat': 'OpenAI 兼容', 'anthropic-messages': 'Anthropic', 'gemini-generate-content': 'Gemini', 'ollama-chat': 'Ollama' };
const capabilityLabels = { text: '文本', vision: '图片', tool_call: '工具调用' };
const microsToYuanInput = value => {
  if (value == null || value === '') return '';
  const padded = String(value).padStart(7, '0');
  const whole = padded.slice(0, -6);
  const fraction = padded.slice(-6).replace(/0+$/, '');
  return fraction ? whole + '.' + fraction : whole;
};
const parseYuanMicros = raw => {
  const text = String(raw).trim();
  if (!text) return null;
  if (!/^\d+(\.\d{1,6})?$/.test(text)) throw new Error('价格最多保留 6 位小数');
  const [whole, fraction = ''] = text.split('.');
  return (BigInt(whole) * 1000000n + BigInt((fraction + '000000').slice(0, 6))).toString();
};
const formatTokenPrice = value => value == null ? '未定价' : microsToYuanInput(value) + ' 元 / 百万 token';
function offeringStatus(model, offering, orphan) {
  if (orphan) return { text: '模型已移除', tone: ' bad' };
  if (model.locked) return { text: '配置停用', tone: ' bad' };
  if (!model.enabled) return { text: '模型已停用', tone: ' warn' };
  if (!offering) return { text: '未发布', tone: ' warn' };
  if (offering.enabled) return { text: '已发布', tone: ' good' };
  return { text: '已下架', tone: '' };
}
function offeringCard(model, offering, settings, orphan = false) {
  const status = offeringStatus(model, offering, orphan);
  const card = node('section', 'card');
  const title = node('div', 'model-name');
  title.append(node('h3', '', offering ? offering.display_name : model.display_name), node('span', 'status' + status.tone, status.text));
  const protocolText = model.protocol ? (protocolLabels[model.protocol] || model.protocol) : '配置缺失';
  card.append(title, node('div', 'mono muted', model.id + ' · ' + protocolText));
  const tags = node('div', 'model-meta');
  for (const capability of (offering ? offering.capabilities : model.capabilities)) tags.append(node('span', 'status', capabilityLabels[capability] || capability));
  if (offering?.is_default) tags.append(node('span', 'status', '默认模型'));
  if (offering) tags.append(node('span', 'status' + (offering.health === 'unavailable' ? ' bad' : ' good'), offering.health === 'unavailable' ? '模型不可用' : '模型可用'));
  card.append(tags);
  if (offering) {
    card.append(detailGrid([
      ['公开编号', offering.public_id],
      ['输入价格', formatTokenPrice(offering.pricing?.input_micros_per_million_tokens ?? null)],
      ['输出价格', formatTokenPrice(offering.pricing?.output_micros_per_million_tokens ?? null)],
      ['价格版本', offering.price_version || '未定价'],
      ['缓存策略', offering.cache_policy.allow ? (offering.cache_policy.scope === 'shared' ? '共享缓存' : '用户隔离') + ' · ' + offering.cache_policy.ttl_days + ' 天' : '已关闭'],
      ['最近测试', model.last_test ? (model.last_test.status === 'ok' ? '通过 · ' : '失败 · ') + formatTime(model.last_test.created_at) : '尚未测试'],
    ]));
  } else {
    card.append(node('p', 'muted', '尚未发布公开条目；用户不能选择此模型。'));
  }
  if (orphan) {
    card.append(node('p', 'muted', '题库配置中已没有该模型引用；条目不会对用户提供。恢复模型配置后可继续编辑。'));
    if (session.role === 'maintainer' && offering) {
      card.append(button('停用条目', async element => {
        element.disabled = true;
        try {
          await api('/offerings/' + encodeURIComponent(offering.public_id) + '/retirements', { method: 'POST', body: JSON.stringify({ version: offering.version }) });
          notify('公开条目已停用');
          loadPage();
        } catch (error) { notify(error.message, true); element.disabled = false; }
      }));
    }
  } else if (session.role === 'maintainer') {
    const actions = node('div', 'actions model-actions');
    actions.append(button(offering ? '编辑' : '发布', () => offeringDialog(offering, model, settings)));
    actions.append(button('编辑配置', () => upstreamModelDialog(model)));
    actions.append(button('连通测试', () => modelTest(model, false)), button('功能测试', () => modelTest(model, true)));
    const toggle = button(model.enabled ? '停用' : '启用', async element => {
      element.disabled = true;
      try { await api('/models/' + encodeURIComponent(model.id), { method: 'PATCH', body: JSON.stringify({ enabled: !model.enabled }) }); notify('模型状态已更新'); loadPage(); } catch (error) { notify(error.message, true); element.disabled = false; }
    });
    toggle.disabled = model.locked;
    actions.append(toggle, button('删除模型', () => deleteModelDialog(model)));
    card.append(actions);
  }
  return card;
}
async function renderOfferings(page) {
  const [offeringData, modelData, settings] = await Promise.all([api('/offerings'), api('/models'), api('/settings')]);
  const models = modelData.items;
  const setup = node('section', 'card');
  const setupHeader = node('div', 'card-header');
  setupHeader.append(node('h2', '', '上游模型'));
  if (session.role === 'maintainer') setupHeader.append(button('新增上游模型', () => upstreamModelDialog(null), 'primary'));
  setup.append(setupHeader, node('p', 'muted', '在这里配置上游模型地址、模型名和密钥；保存后立即生效。密钥只写入题库服务私密文件，不会回显。'));
  page.append(setup);
  if (!models.length) {
    const empty = node('div', 'card empty');
    empty.append(node('p', '', '尚未配置上游模型。'), session.role === 'maintainer' ? button('新增上游模型', () => upstreamModelDialog(null), 'primary') : node('span', '', '请联系维护员配置上游模型。'));
    page.append(empty);
    return;
  }
  const byProvider = new Map(offeringData.items.map(item => [item.provider_ref, item]));
  const ordered = [...models].sort((a, b) => {
    const left = byProvider.get(a.id), right = byProvider.get(b.id);
    if (left && right) return (left.sort_order - right.sort_order) || left.public_id.localeCompare(right.public_id);
    if (left) return -1;
    if (right) return 1;
    return a.id.localeCompare(b.id);
  });
  const grid = node('div', 'model-grid');
  const configured = new Set(models.map(model => model.id));
  for (const model of ordered) grid.append(offeringCard(model, byProvider.get(model.id) || null, settings));
  for (const offering of offeringData.items) {
    if (configured.has(offering.provider_ref)) continue;
    grid.append(offeringCard({ id: offering.provider_ref, display_name: offering.display_name, capabilities: offering.capabilities,
      enabled: false, locked: true, protocol: '', last_test: null }, offering, settings, true));
  }
  page.append(grid);
  page.append(node('p', 'small muted', '价格与缓存策略保存后立即对主平台生效；已完成查询继续使用当时冻结的价格与策略。已发布条目不提供删除，只能下架。'));
}
function upstreamModelDialog(model) {
  const isNew = !model;
  const dialog = openDialog(isNew ? '新增上游模型' : '编辑上游模型');
  const form = node('form');
  const id = input('text', 'id');
  id.required = isNew;
  id.value = model?.id ?? '';
  id.disabled = !isNew;
  const displayName = input('text', 'display_name');
  displayName.value = model?.display_name ?? '';
  const protocol = select('接入协议', [['openai-chat', 'OpenAI 兼容'], ['anthropic-messages', 'Anthropic'], ['gemini-generate-content', 'Gemini'], ['ollama-chat', 'Ollama']], model?.protocol ?? 'openai-chat', () => { });
  const vendor = input('text', 'vendor');
  vendor.required = false;
  vendor.value = model?.vendor ?? '';
  const baseURL = input('text', 'base_url');
  baseURL.value = model?.base_url ?? '';
  baseURL.placeholder = 'https://models.example.com/v1';
  const upstreamModel = input('text', 'upstream_model');
  upstreamModel.value = model?.upstream_model ?? '';
  const apiKey = input('password', 'api_key', 'new-password');
  apiKey.required = false;
  apiKey.placeholder = isNew ? '非 Ollama 模型必填' : '留空保留原密钥';
  const vision = input('checkbox', 'vision');
  vision.required = false;
  vision.checked = model?.vision ?? false;
  const toolCall = input('checkbox', 'tool_call');
  toolCall.required = false;
  toolCall.checked = model?.tool_call ?? false;
  const reasoning = input('checkbox', 'reasoning');
  reasoning.required = false;
  reasoning.checked = model?.reasoning ?? false;
  const context = input('number', 'context');
  context.required = false;
  context.min = '0';
  context.value = String(model?.context ?? 128000);
  const maxOutput = input('number', 'max_output_tokens');
  maxOutput.required = false;
  maxOutput.min = '0';
  maxOutput.value = String(model?.max_output_tokens ?? 2048);
  const enabled = input('checkbox', 'enabled');
  enabled.required = false;
  enabled.checked = model ? !model.locked : true;
  const caption = input('checkbox', 'is_caption');
  caption.required = false;
  caption.checked = model?.is_caption ?? false;
  const makeCheck = (inputNode, labelText) => {
    const row = node('div', 'check-row');
    if (!inputNode.id) inputNode.id = 'field-' + crypto.randomUUID();
    const label = node('label', '', labelText);
    label.htmlFor = inputNode.id;
    row.append(inputNode, label);
    return row;
  };
  const error = node('p', 'form-error');
  error.setAttribute('role', 'alert');
  const submit = node('button', 'primary', isNew ? '添加模型' : '保存模型');
  submit.type = 'submit';
  form.append(
    field('模型标识', id),
    field('显示名称', displayName),
    field('接入协议', protocol),
    field('供应商（可选）', vendor),
    field('接口地址', baseURL),
    field('上游模型名', upstreamModel),
    field('API 密钥', apiKey),
    makeCheck(vision, '支持图片输入'),
    makeCheck(toolCall, '支持工具调用'),
    makeCheck(reasoning, '支持推理内容'),
    field('上下文长度', context),
    field('单次最大输出 token', maxOutput),
    makeCheck(enabled, '配置允许启用'),
    makeCheck(caption, '作为图片转述模型'),
    node('p', 'small muted', '接口地址必须使用 HTTPS；本地测试的 http 回环地址需要服务配置显式允许。密钥只保存在题库服务的私密文件中。面板的“停用/启用”是独立的临时开关。'),
    error,
    submit,
  );
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (submit.disabled) return;
    error.textContent = '';
    submit.disabled = true;
    try {
      const protocolValue = protocol.value;
      if (isNew && !id.value.trim()) throw new Error('请填写模型标识');
      if (!displayName.value.trim()) throw new Error('请填写显示名称');
      if (!upstreamModel.value.trim()) throw new Error('请填写上游模型名');
      if (!baseURL.value.trim()) throw new Error('请填写接口地址');
      if (protocolValue !== 'ollama-chat' && isNew && !apiKey.value.trim()) throw new Error('请填写 API 密钥');
      const payload = {
        display_name: displayName.value.trim(),
        protocol: protocolValue,
        vendor: vendor.value.trim(),
        base_url: baseURL.value.trim(),
        upstream_model: upstreamModel.value.trim(),
        vision: vision.checked,
        tool_call: toolCall.checked,
        reasoning: reasoning.checked,
        context: Number(context.value) || 0,
        max_output_tokens: Number(maxOutput.value) || 0,
        disabled: !enabled.checked,
        is_caption: caption.checked,
      };
      if (apiKey.value.trim()) payload.api_key = apiKey.value.trim();
      if (isNew) payload.id = id.value.trim();
      await api(isNew ? '/upstream-models' : '/upstream-models/' + encodeURIComponent(model.id), { method: isNew ? 'POST' : 'PATCH', body: JSON.stringify(payload) });
      dialog.close();
      notify(isNew ? '上游模型已添加' : '上游模型已更新');
      loadPage();
    } catch (failure) {
      error.textContent = failure.message;
      submit.disabled = false;
    }
  });
  dialog.append(form);
}

function deleteModelDialog(model) {
  const dialog = openDialog('删除上游模型');
  const error = node('p', 'form-error');
  dialog.append(node('p', '', '删除 ' + model.display_name + '（' + model.id + '）后，该模型不能再用于作答。仍有公开条目引用时会被拒绝。'), error);
  const footer = node('div', 'dialog-footer');
  const submit = button('删除模型', async element => {
    element.disabled = true;
    try {
      await api('/upstream-models/' + encodeURIComponent(model.id), { method: 'DELETE' });
      dialog.close();
      notify('上游模型已删除');
      loadPage();
    } catch (failure) {
      error.textContent = failure.message;
      element.disabled = false;
    }
  }, 'danger');
  footer.append(button('取消', () => dialog.close()), submit);
  dialog.append(footer);
}

function offeringDialog(offering, model, settings) {
  const dialog = openDialog(offering ? '编辑公开模型' : '发布公开模型');
  const form = node('form');
  const name = input('text', 'display_name');
  name.value = offering?.display_name ?? model.display_name;
  name.maxLength = 80;
  const description = node('textarea');
  description.name = 'description';
  description.maxLength = 500;
  description.value = offering?.description ?? '';
  const inputPrice = input('text', 'input_price');
  inputPrice.required = false;
  inputPrice.inputMode = 'decimal';
  inputPrice.value = microsToYuanInput(offering?.pricing?.input_micros_per_million_tokens ?? '');
  const outputPrice = input('text', 'output_price');
  outputPrice.required = false;
  outputPrice.inputMode = 'decimal';
  outputPrice.value = microsToYuanInput(offering?.pricing?.output_micros_per_million_tokens ?? '');
  const priceVersion = input('text', 'price_version');
  priceVersion.required = false;
  priceVersion.maxLength = 64;
  priceVersion.value = offering?.pricing?.version ?? '';
  priceVersion.placeholder = model.id + '-v1';
  const rounding = select('价格取整方式', [['ceil', '向上取整'], ['floor', '向下取整']], offering?.pricing?.rounding ?? 'ceil', () => { });
  const allowCache = input('checkbox', 'allow_cache');
  allowCache.required = false;
  allowCache.checked = offering?.cache_policy.allow ?? true;
  const scopeChoices = [['user', '用户隔离']];
  if (settings.shared_cache_enabled) scopeChoices.push(['shared', '共享缓存']);
  const selectedScope = offering?.cache_policy.scope === 'shared' && settings.shared_cache_enabled ? 'shared' : 'user';
  const scope = select('缓存范围', scopeChoices, selectedScope, () => { });
  const ttl = input('number', 'cache_ttl_days');
  ttl.required = false;
  ttl.min = '1';
  ttl.max = String(settings.cache_max_ttl_days);
  ttl.value = String(offering?.cache_policy.ttl_days ?? 7);
  const enabled = input('checkbox', 'enabled');
  enabled.required = false;
  enabled.checked = offering?.enabled ?? false;
  const isDefault = input('checkbox', 'is_default');
  isDefault.required = false;
  isDefault.checked = offering?.is_default ?? false;
  const sortOrder = input('number', 'sort_order');
  sortOrder.required = false;
  sortOrder.value = String(offering?.sort_order ?? 0);
  const vision = input('checkbox', 'capability_vision');
  vision.required = false;
  vision.checked = offering ? offering.capabilities.includes('vision') : model.capabilities.includes('vision');
  const makeCheck = (inputNode, labelText) => {
    const row = node('div', 'check-row');
    if (!inputNode.id) inputNode.id = 'field-' + crypto.randomUUID();
    const label = node('label', '', labelText);
    label.htmlFor = inputNode.id;
    row.append(inputNode, label);
    return row;
  };
  const error = node('p', 'form-error');
  error.setAttribute('role', 'alert');
  const submit = node('button', 'primary', offering ? '保存' : '发布');
  submit.type = 'submit';
  form.append(
    field('公开名称', name),
    field('说明', description),
    field('输入价格（元 / 百万 token）', inputPrice),
    field('输出价格（元 / 百万 token）', outputPrice),
    field('价格版本', priceVersion),
    field('价格取整方式', rounding),
    makeCheck(allowCache, '启用精确缓存'),
    field('缓存范围', scope),
    field('缓存期限（天）', ttl),
    ...(model.capabilities.includes('vision') ? [makeCheck(vision, '支持图片题')] : []),
    makeCheck(enabled, '对用户可见'),
    makeCheck(isDefault, '设为默认模型'),
    field('排序值', sortOrder),
    node('p', 'small muted', '输入与输出价格都留空表示暂未定价；已定价条目必须填写价格版本，同一版本不能使用不同费率。'),
    error,
    submit,
  );
  enabled.addEventListener('change', () => { if (!enabled.checked) isDefault.checked = false; });
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (submit.disabled) return;
    error.textContent = '';
    let pricing = null;
    try {
      const inputRaw = inputPrice.value.trim();
      const outputRaw = outputPrice.value.trim();
      if (inputRaw || outputRaw) {
        if (!inputRaw || !outputRaw) throw new Error('请同时填写输入与输出价格，或都留空表示未定价');
        const version = priceVersion.value.trim();
        if (!version) throw new Error('请填写价格版本');
        pricing = {
          version,
          input_micros_per_million_tokens: parseYuanMicros(inputRaw),
          output_micros_per_million_tokens: parseYuanMicros(outputRaw),
          micros_per_minor: '10000',
          rounding: rounding.value,
          rule: 'tokens_x_micros_per_million_v1',
        };
      }
      const cacheDays = Number(ttl.value);
      if (!Number.isInteger(cacheDays) || cacheDays < 1 || cacheDays > settings.cache_max_ttl_days) throw new Error('缓存期限必须在 1 到 ' + settings.cache_max_ttl_days + ' 天之间');
      if (isDefault.checked && !enabled.checked) throw new Error('默认模型必须同时设为可见');
      const capabilities = ['text'];
      if (vision.checked) capabilities.push('vision');
      const payload = {
        display_name: name.value.trim(),
        description: description.value.trim(),
        provider_ref: model.id,
        capabilities,
        pricing,
        cache_policy: { allow: allowCache.checked, scope: scope.value, ttl_days: cacheDays },
        enabled: enabled.checked,
        is_default: isDefault.checked,
        sort_order: Number(sortOrder.value) || 0,
      };
      if (offering) payload.version = offering.version;
      submit.disabled = true;
      await api(offering ? '/offerings/' + encodeURIComponent(offering.public_id) : '/offerings', { method: offering ? 'PATCH' : 'POST', body: JSON.stringify(payload) });
      dialog.close();
      notify(offering ? '公开模型已更新' : '公开模型已发布');
      loadPage();
    } catch (failure) {
      error.textContent = failure.message;
      submit.disabled = false;
    }
  });
  dialog.append(node('p', 'muted', '模型引用：' + model.id + ' · ' + (protocolLabels[model.protocol] || model.protocol)), form);
}
function cacheDialog(value) {
  const dialog = openDialog('缓存详情');
  dialog.append(detailGrid([['缓存编号', value.id], ['模型', value.model_id], ['范围', value.scope === 'shared' ? '共享' : '用户隔离'], ['状态', statusLabels[value.status]], ['保存时间', formatTime(value.created_at)], ['到期时间', formatTime(value.expires_at)], ['来源请求', value.source_request_id]]));
  dialog.append(node('h3', '', '题目'), node('p', 'detail-text', value.question_title), node('h3', '', '答案'), node('p', 'detail-text', value.answer));
  if (value.invalidation_reason) dialog.append(node('p', 'muted', '失效原因：' + value.invalidation_reason));
  if (session.role !== 'maintainer' || value.status !== 'active') return;
  const form = node('form');
  const reason = node('textarea');
  reason.name = 'reason';
  reason.required = true;
  reason.maxLength = 1000;
  const error = node('p', 'form-error');
  const submit = node('button', 'danger', '使此缓存失效');
  submit.type = 'submit';
  form.append(field('失效原因', reason), error, submit);
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (submit.disabled) return;
    submit.disabled = true;
    try { await api('/cache/' + encodeURIComponent(value.id) + '/invalidations', { method: 'POST', body: JSON.stringify({ reason: reason.value }) }); dialog.close(); notify('缓存已失效'); loadPage(); } catch (failure) { error.textContent = failure.message; submit.disabled = false; }
  });
  dialog.append(form);
}
async function renderList(page, route) {
  if (!pageState.has(route)) pageState.set(route, { cursor: '', previous: [], model: '', status: '' });
  const state = pageState.get(route);
  const query = new URLSearchParams({ limit: '30' });
  if (state.cursor) query.set('cursor', state.cursor);
  if (state.model) query.set('model', state.model);
  if (state.status) query.set('status', state.status);
  const [data, modelData] = await Promise.all([api('/' + route + '?' + query), route === 'audit' ? Promise.resolve({ items: [] }) : api('/models')]);
  if (route !== 'audit') {
    const filters = node('div', 'filters');
    const update = (key, value) => { state[key] = value; state.cursor = ''; state.previous = []; loadPage(); };
    filters.append(select('筛选模型', [['', '全部模型'], ...modelData.items.map(model => [model.id, model.display_name])], state.model, value => update('model', value)));
    const statuses = route === 'cache' ? ['active', 'expired', 'invalidated'] : ['answered', 'cache_hit', 'usage_incomplete', 'pending', 'failed', 'unknown', 'expired'];
    filters.append(select('筛选状态', [['', '全部状态'], ...statuses.map(status => [status, statusLabels[status]])], state.status, value => update('status', value)));
    page.append(filters);
  }
  if (route === 'requests') page.append(requestsTable(data.items));
  if (route === 'cache') page.append(table(['题目', '模型', '范围', '状态', '到期时间', '操作'], data.items.map(value => {
    const title = node('div', 'truncate', value.question_title);
    title.title = value.question_title;
    return [title, value.model_id, value.scope === 'shared' ? '共享' : '用户隔离', badge(value.status), formatTime(value.expires_at), button('查看', () => cacheDialog(value), 'link')];
  })));
  if (route === 'audit') page.append(table(['操作', '账号', '对象', '详情', '时间'], data.items.map(value => [actionLabels[value.action] || '维护操作', value.actor, shorten(value.resource_id), value.detail?.reason || (value.detail?.enabled === true ? '启用模型' : value.detail?.enabled === false ? '停用模型' : value.detail?.kind === 'functional' ? '功能测试' : value.detail?.kind === 'reachability' ? '连通测试' : '—'), formatTime(value.created_at)])));
  const pager = node('div', 'pagination');
  const previous = button('上一页', () => { state.cursor = state.previous.pop() || ''; loadPage(); });
  previous.disabled = !state.previous.length;
  const next = button('下一页', () => { state.previous.push(state.cursor); state.cursor = data.next_cursor; loadPage(); });
  next.disabled = !data.next_cursor;
  pager.append(previous, next);
  page.append(pager);
}
async function renderSettings(page) {
  const settings = await api('/settings');
  const appearanceCard = node('section', 'card');
  appearanceCard.append(node('h2', '', '外观'));
  const choices = node('div', 'appearance');
  const update = (key, value) => {
    appearance[key] = value;
    applyAppearance();
    try { localStorage.setItem(preferenceKey, JSON.stringify(appearance)); } catch { notify('外观已应用，本次未能保存到设备', true); }
  };
  choices.append(field('配色', select('配色', [['graphite', '石墨'], ['grove', '青禾'], ['indigo', '靛蓝']], appearance.palette, value => update('palette', value))));
  choices.append(field('显示模式', select('显示模式', [['system', '跟随系统'], ['light', '浅色'], ['dark', '深色']], appearance.mode, value => update('mode', value))));
  appearanceCard.append(choices);
  const serviceCard = node('section', 'card');
  serviceCard.append(node('h2', '', '当前服务设置'), detailGrid([
    ['默认缓存期限', settings.cache_ttl_days + ' 天'], ['最长缓存期限', settings.cache_max_ttl_days + ' 天'], ['共享缓存', settings.shared_cache_enabled ? '已启用' : '未启用'],
    ['默认公开条目', settings.default_offering_id || '未指定'], ['已发布条目', settings.published_offering_count + ' / ' + settings.offering_count],
    ['图片转述模型', settings.caption_model_id || '未指定'], ['配置模型数量', settings.model_count],
    ['同时处理查询', settings.concurrency], ['查询超时', settings.timeout_seconds + ' 秒'], ['最大输出 token', formatNumber(settings.max_output_tokens)],
  ]));
  page.append(appearanceCard, serviceCard);
}
async function loadPage() {
  if (!session) return;
  const generation = ++epoch;
  currentDialog?.close();
  let route = location.hash.slice(1) || 'overview';
  if (!navigation.some(value => value[0] === route)) route = 'overview';
  for (const element of document.querySelectorAll('[data-route]')) {
    if (element.dataset.route === route) element.setAttribute('aria-current', 'page'); else element.removeAttribute('aria-current');
  }
  const main = document.querySelector('#page');
  if (!main) return;
  main.replaceChildren();
  const heading = node('div', 'page-heading');
  const title = node('div');
  const subtitles = { overview: '查看查询、模型用量与缓存状态。', offerings: '维护公开模型、token 价格与精确缓存策略，并测试模型可用性。', requests: '查看每次查询的结果、缓存来源与模型用量。', cache: '查看精确缓存及其有效期，处理错误答案。', audit: '查看维护账号的操作记录。', settings: '管理当前设备外观，查看服务设置。' };
  title.append(node('h1', '', navigation.find(value => value[0] === route)[1]), node('p', '', subtitles[route]));
  heading.append(title, button('刷新', () => loadPage()));
  main.append(heading);
  const loading = node('div', 'card empty', '正在读取…');
  loading.setAttribute('role', 'status');
  main.append(loading);
  const content = node('div');
  try {
    if (route === 'overview') await renderOverview(content);
    else if (route === 'offerings') await renderOfferings(content);
    else if (route === 'settings') await renderSettings(content);
    else await renderList(content, route);
    if (generation !== epoch) return;
    loading.remove();
    main.append(content);
  } catch (error) {
    if (generation !== epoch) return;
    loading.remove();
    main.append(node('div', 'error-state', error.message));
  }
}
window.addEventListener('hashchange', loadPage);
try { session = await api('/session'); renderShell(); } catch { renderLogin(); }
