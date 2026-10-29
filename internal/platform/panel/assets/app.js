import { enhanceSelect, closeSelect, installIndicator, markButtonBusy } from './controls.js';

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
  document.documentElement.dataset.colorScheme = appearance.palette;
  document.documentElement.dataset.mode = appearance.mode === 'system' ? (media.matches ? 'dark' : 'light') : appearance.mode;
  document.documentElement.dataset.theme = document.documentElement.dataset.mode;
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
const errorLabels = { unsupported_protocol: '该协议不支持无用量连通测试', rate_limited: '上游请求受限', upstream_rate_limited: '上游请求受限', model_auth_failed: '模型服务认证失败', response_too_large: '模型响应超过大小上限', upstream_timeout: '上游响应超时', empty_answer: '模型返回空答案', model_disabled: '模型已停用', usage_incomplete: '用量不完整', request_cancelled: '请求已取消', model_unavailable: '模型暂不可用' };
const actionLabels = { 'provider.create.request': '尝试新增提供商', 'provider.update.request': '尝试更新提供商', 'provider.delete.request': '尝试删除提供商', 'upstream_model.create.request': '尝试新增模型', 'upstream_model.update.request': '尝试更新模型', 'upstream_model.delete.request': '尝试删除模型', 'provider.create': '新增提供商', 'provider.update': '更新提供商', 'provider.delete': '删除提供商', 'upstream_model.create': '新增模型', 'upstream_model.update': '更新模型', 'upstream_model.delete': '删除模型', 'session.login': '登录', 'session.logout': '退出登录', 'model.state': '调整模型状态', 'model.test.request': '开始模型测试', 'model.test.result': '模型测试结束', 'offering.create': '发布公开条目', 'offering.update': '更新公开条目', 'offering.retire': '停用公开条目', 'offering.test.request': '开始公开条目测试', 'offering.test.result': '公开条目测试结束', 'cache.invalidate': '使缓存失效' };
const pageState = new Map();
const formatNumber = value => value == null ? '—' : Number(value).toLocaleString('zh-CN');
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
  element.addEventListener('click', async () => {
    if (element.getAttribute('aria-busy') === 'true') return;
    const menu = element.closest('details');
    if (menu) menu.open = false;
    try {
      const result = action(element);
      if (result && typeof result.then === 'function') {
        markButtonBusy(element, true);
        try { await result; } finally { markButtonBusy(element, false); }
      }
    } catch (error) { notify(error.message || '操作未完成', true); }
  });
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
  while (notices.childElementCount > 3) notices.firstElementChild.remove();
  setTimeout(() => element.remove(), 5000);
}
function notifyConfigResult(value, message) {
  notify(value.audit_pending ? message + '，操作记录待核对' : message, !!value.audit_pending);
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
  if (input.tagName === 'SELECT') { input.setAttribute('aria-label', label); input = enhanceSelect(input); }
  const wrapper = node('div', 'field');
  const title = node('label', '', label);
  const control = input.control || input;
  if (!control.id) control.id = 'field-' + crypto.randomUUID();
  title.htmlFor = control.id;
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
  pageViews.clear();
  currentDialog?.close();
  closeSelect();
  root.querySelector('.navigation')?.disposeIndicator?.();
  root.replaceChildren();
  const wrapper = node('main', 'login-wrap');
  const card = node('section', 'login');
  card.append(brand(), node('h1', '', '题库维护'));
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
  wrapper.append(node('p', 'login-footer', `© ${new Date().getFullYear()} ByebyeOCS · 版权所有 · Created by onprs`));
  root.append(wrapper);
}
function renderShell() {
  pageViews.clear();
  closeSelect();
  root.querySelector('.navigation')?.disposeIndicator?.();
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
  sidebar.append(nav);
  installIndicator(nav, 'a[aria-current=page]');
  const workspace = node('div', 'workspace');
  const top = node('header', 'topbar');
  const account = node('div', 'account');
  account.append(node('span', '', session.username), node('span', 'status', session.role === 'maintainer' ? '维护员' : '只读账号'));
  account.append(button('退出登录', async element => {
    element.disabled = true;
    try { await api('/logout', { method: 'POST', body: '{}' }); session = undefined; renderLogin(); } catch (error) { notify(error.message, true); element.disabled = false; }
  }));
  top.append(account);
  const main = node('main', 'page');
  main.id = 'page';
  const footer = node('footer', 'site-footer');
  footer.append(node('span', '', `© ${new Date().getFullYear()} ByebyeOCS · 版权所有`), node('span', '', 'Created by onprs'));
  workspace.append(top, main, footer);
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
  if (!rows.length) return node('div', 'empty empty-surface', '暂无记录');
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
  return enhanceSelect(element);
}
function openDialog(title, drawer = false) {
  currentDialog?.close();
  const opener = document.activeElement;
  const dialog = node('dialog', drawer ? 'drawer' : '');
  currentDialog = dialog;
  const heading = node('div', 'dialog-heading');
  const titleNode = node('h2', '', title);
  titleNode.id = 'dialog-title';
  dialog.setAttribute('aria-labelledby', titleNode.id);
  const close = button('', () => dialog.close(), 'icon-button');
  close.setAttribute('aria-label', '关闭');
  close.append(icon('M6 6l12 12 M18 6L6 18'));
  heading.append(titleNode, close);
  dialog.content = node('div', 'dialog-body');
  dialog.footer = node('div', 'dialog-actions');
  dialog.append(heading, dialog.content, dialog.footer);
  document.body.append(dialog);
  dialog.addEventListener('close', () => {
    closeSelect();
    dialog.remove();
    if (currentDialog === dialog) currentDialog = undefined;
    if (opener?.isConnected) opener.focus();
  }, { once: true });
  dialog.showModal();
  return dialog;
}
function formSection(title, ...children) {
  const section = node('fieldset', 'form-section');
  section.append(node('legend', '', title), ...children);
  return section;
}
function formActions(dialog, form, submit) {
  const formId = 'form-' + crypto.randomUUID();
  form.setAttribute('id', formId);
  submit.setAttribute('form', formId);
  dialog.footer.append(button('取消', () => dialog.close()), submit);
}
async function showRequest(id) {
  const dialog = openDialog('查询详情', true);
  const state = node('p', 'muted', '正在读取…');
  dialog.content.append(state);
  try {
    const value = await api('/requests/' + encodeURIComponent(id));
    state.remove();
    dialog.content.append(detailGrid([
      ['请求编号', value.id], ['模型', value.model_id || '未记录'], ['状态', statusLabels[value.status] || value.status],
      ['查询模式', value.mode === 'cache_first' ? '优先使用缓存' : value.mode === 'always_llm' ? '每次使用模型' : '未记录'],
      ['缓存范围', value.cache_scope === 'shared' ? '共享' : value.cache_scope === 'user' ? '用户隔离' : '未记录'],
      ['输入 / 输出 token', formatNumber(value.usage?.input_tokens) + ' / ' + formatNumber(value.usage?.output_tokens)],
      ['用量完整性', value.usage?.complete === true ? '完整' : '未完整返回'], ['请求时间', formatTime(value.created_at)],
    ]));
    if (value.error_code) dialog.content.append(node('p', 'form-error', errorLabels[value.error_code] || '请求未成功，请核对服务状态'));
    dialog.content.append(node('h3', '', '答案'), node('div', 'detail-text', value.answer || '暂无可用答案内容'));
    if (value.calls?.length) {
      dialog.content.append(node('h3', '', '模型调用'));
      dialog.content.append(table(['用途', '输入', '输出', '用量'], value.calls.map(call => [call.slot === 'caption' ? '图片转述' : '回答', formatNumber(call.input_tokens), formatNumber(call.output_tokens), call.usage_complete ? '完整' : '不完整'])));
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
  const totals = summary.all_time || {};
  const pricing = summary.pricing || { published: 0, priced: 0 };
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

  const usage = node('section', 'overview-section');
  const usageHeader = node('div', 'card-header');
  const coverageTone = pricing.published > 0 && pricing.priced === pricing.published ? ' good' : ' warn';
  usageHeader.append(
    node('h2', '', '累计用量与定价'),
    node('span', 'status' + coverageTone, pricing.published === 0 ? '尚未发布模型' : `${pricing.priced} / ${pricing.published} 个已定价`),
  );
  usage.append(usageHeader);
  const usageGrid = detailGrid([
    ['累计查询', formatNumber(totals.queries)],
    ['累计缓存命中', formatNumber(totals.cache_hits)],
    ['累计输入 token', formatNumber(totals.input_tokens)],
    ['累计输出 token', formatNumber(totals.output_tokens)],
    ['用量不完整', formatNumber(totals.incomplete)],
    ['当前有效缓存', `${formatNumber(summary.active_cache)} 条`],
  ]);
  usageGrid.classList.add('detail-grid-wide');
  usage.append(usageGrid);
  const models = Array.isArray(summary.by_model) ? summary.by_model : [];
  if (models.length) {
    usage.append(node('h3', '', '按模型累计'));
    usage.append(table(['模型', '查询', '缓存命中', '输入 token', '输出 token'], models.map(model => [
      model.display_name || model.model_id,
      formatNumber(model.queries),
      formatNumber(model.cache_hits),
      formatNumber(model.input_tokens),
      formatNumber(model.output_tokens),
    ])));
  } else {
    usage.append(node('p', 'muted', '尚无模型用量。'));
  }
  page.append(usage);

  const card = node('section', 'overview-section');
  const heading = node('div', 'card-header');
  heading.append(node('h2', '', '最近查询'));
  card.append(heading, recent.items.length ? requestsTable(recent.items) : node('p', 'muted', '暂无查询记录。'));
  page.append(card);
}
function modelTest(model, functional) {
  const dialog = openDialog(functional ? '功能测试' : '连通测试');
  dialog.content.append(node('p', '', model.display_name));
  let checkbox;
  if (functional) {
    dialog.content.append(node('p', 'muted', '功能测试会请求模型生成内容，并产生模型用量。'));
    const check = node('div', 'check-row');
    checkbox = input('checkbox', 'confirm_cost');
    checkbox.id = 'confirm-cost';
    checkbox.required = true;
    const label = node('label', '', '我已了解本次测试会产生用量');
    label.htmlFor = checkbox.id;
    check.append(checkbox, label);
    dialog.content.append(check);
  }
  const result = node('p', 'muted');
  result.setAttribute('role', 'status');
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
  dialog.content.append(result);
  dialog.footer.append(submit);
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
  if (offering && !offering.enabled) return { text: '已下架', tone: '' };
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
  if (offering) tags.append(node('span', 'status' + (offering.health === 'unavailable' ? ' bad' : ''), offering.health === 'unavailable' ? '模型不可用' : '已配置'));
  card.append(tags);
  if (offering) {
    const summary = node('div', 'model-pricing');
    for (const [label, value] of [
      ['输入', formatTokenPrice(offering.pricing?.input_micros_per_million_tokens ?? null)],
      ['输出', formatTokenPrice(offering.pricing?.output_micros_per_million_tokens ?? null)],
      ['缓存', offering.cache_policy.allow ? (offering.cache_policy.scope === 'shared' ? '共享缓存' : '用户隔离') + ' · ' + offering.cache_policy.ttl_days + ' 天' : '已关闭'],
    ]) {
      const item = node('span');
      item.append(node('span', 'muted', label + ' '), document.createTextNode(value));
      summary.append(item);
    }
    card.append(summary);
  } else {
    card.append(node('p', 'muted', '尚未发布公开条目；用户不能选择此模型。'));
  }
  if (orphan) card.append(node('p', 'muted', '关联模型已移除'));
  const actions = node('div', 'actions model-actions');
  actions.append(button('详情', () => modelDetailsDialog(model, offering)));
  if (session.role === 'maintainer') {
    actions.append(button('编辑', () => offeringDialog(offering, model, settings)));
    if (offering.enabled) actions.append(button('下架', () => retireOfferingDialog(offering)));
  }
  card.append(actions);
  return card;
}
function retireOfferingDialog(offering) {
  const dialog = openDialog('下架公开模型');
  const error = node('p', 'form-error'); error.setAttribute('role', 'alert');
  dialog.content.append(node('p', '', '确定下架 ' + offering.display_name + '？'), error);
  dialog.footer.append(button('取消', () => dialog.close()), button('确认下架', async () => {
    try {
      await api('/offerings/' + encodeURIComponent(offering.public_id) + '/retirements', { method: 'POST', body: JSON.stringify({ version: offering.version }) });
      dialog.close(); notify('公开条目已下架'); loadPage();
    } catch (failure) { error.textContent = failure.message; }
  }, 'danger'));
}
function modelDetailsDialog(model, offering) {
  const dialog = openDialog('模型详情', true);
  dialog.content.append(detailGrid([
    ['模型名称', offering?.display_name || model.display_name],
    ['模型标识', model.id],
    ['接入协议', protocolLabels[model.protocol] || model.protocol || '配置缺失'],
    ['公开编号', offering?.public_id || '未发布'],
    ['价格版本', offering?.pricing?.version || '未定价'],
    ['输入价格', formatTokenPrice(offering?.pricing?.input_micros_per_million_tokens)],
    ['输出价格', formatTokenPrice(offering?.pricing?.output_micros_per_million_tokens)],
    ['最近测试', model.last_test ? formatTime(model.last_test.created_at) : '尚未测试'],
  ]));
}
let modelTab = 'models';
function providerDialog(provider) {
  const isNew = !provider;
  const dialog = openDialog(isNew ? '新增提供商' : '编辑提供商', true);
  const form = node('form');
  const id = input('text', 'id'); id.value = provider?.id ?? ''; id.disabled = !isNew;
  const name = input('text', 'name'); name.value = provider?.name ?? '';
  const vendor = input('text', 'vendor'); vendor.required = false; vendor.value = provider?.vendor ?? '';
  const protocol = select('接入协议', Object.entries(protocolLabels), provider?.protocol ?? 'openai-chat', () => {});
  const address = input('url', 'base_url'); address.value = provider?.base_url ?? ''; address.placeholder = 'https://api.example.com/v1';
  const key = input('password', 'api_key', 'new-password'); key.required = false;
  key.placeholder = isNew ? '请输入密钥' : '地址和协议不变时留空保留';
  const error = node('p', 'form-error'); error.setAttribute('role', 'alert');
  const submit = node('button', 'primary', isNew ? '添加提供商' : '保存提供商'); submit.type = 'submit';
  form.append(formSection('提供商', field('提供商标识', id), field('名称', name), field('厂商标识（可选）', vendor)),
    formSection('连接', field('接入协议', protocol), field('接口地址', address), field('API 密钥', key)), error);
  formActions(dialog, form, submit);
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (submit.disabled) return;
    error.textContent = '';
    submit.disabled = true;
    try {
      const payload = { name: name.value.trim(), vendor: vendor.value.trim(), protocol: protocol.value, base_url: address.value.trim(), api_key: key.value.trim() };
      if (isNew) payload.id = id.value.trim();
      const result = await api(isNew ? '/providers' : '/providers/' + encodeURIComponent(provider.id), { method: isNew ? 'POST' : 'PATCH', body: JSON.stringify(payload) });
      dialog.close(); notifyConfigResult(result, isNew ? '提供商已添加' : '提供商已更新'); loadPage();
    } catch (failure) { error.textContent = failure.message; submit.disabled = false; }
  });
  dialog.content.append(form);
}
function deleteProviderDialog(provider) {
  const dialog = openDialog('删除提供商');
  const error = node('p', 'form-error'); error.setAttribute('role', 'alert');
  dialog.content.append(node('p', '', '确定删除 ' + provider.name + '？'), error);
  dialog.footer.append(button('取消', () => dialog.close()), button('删除提供商', async () => {
    try {
      const result = await api('/providers/' + encodeURIComponent(provider.id), { method: 'DELETE' });
      dialog.close(); notifyConfigResult(result, '提供商已删除'); loadPage();
    } catch (failure) { error.textContent = failure.message; }
  }, 'danger'));
}
function providerCard(provider) {
  const card = node('section', 'card provider-row');
  const title = node('div', 'model-name');
  title.append(node('h3', '', provider.name), node('span', 'status', protocolLabels[provider.protocol] || provider.protocol));
  const meta = node('div', 'provider-meta');
  meta.append(node('span', 'mono muted', provider.id), node('span', 'muted', provider.model_count + ' 个模型'),
    node('span', 'muted', provider.key_configured ? '密钥已配置' : '无密钥'));
  card.append(title, meta, node('div', 'mono muted provider-address', provider.base_url));
  if (session.role === 'maintainer') {
    const actions = node('div', 'actions model-actions');
    actions.append(button('编辑提供商', () => providerDialog(provider)), button('删除', () => deleteProviderDialog(provider), 'danger'));
    card.append(actions);
  }
  return card;
}
function upstreamCard(model, offerings, providers, settings) {
  const card = node('section', 'card');
  const title = node('div', 'model-name');
  const state = model.locked ? { text: '配置停用', tone: ' bad' } : model.enabled ? { text: '已启用', tone: ' good' } : { text: '已停用', tone: ' warn' };
  title.append(node('h3', '', model.display_name), node('span', 'status' + state.tone, state.text));
  const provider = providers.find(item => item.id === model.provider_id);
  card.append(title, node('div', 'mono muted', model.id + ' · ' + (provider?.name || '独立连接')));
  const meta = node('div', 'model-meta');
  meta.append(node('span', 'status', protocolLabels[model.protocol] || model.protocol));
  for (const capability of model.capabilities) meta.append(node('span', 'status', capabilityLabels[capability] || capability));
  card.append(meta, node('div', 'model-pricing muted', `${offerings} 个公开条目 · ${model.upstream_model}`));
  if (session.role === 'maintainer') {
    const actions = node('div', 'actions model-actions');
    actions.append(button('发布', () => offeringDialog(null, model, settings)), button('编辑配置', () => upstreamModelDialog(model)));
    const menu = node('details', 'row-menu'); menu.append(node('summary', '', '更多操作'));
    const options = node('div');
    options.append(button('模型详情', () => modelDetailsDialog(model, null)), button('连通测试', () => modelTest(model, false)), button('功能测试', () => modelTest(model, true)));
    const toggle = button(model.enabled ? '停用' : '启用', async () => {
      await api('/models/' + encodeURIComponent(model.id), { method: 'PATCH', body: JSON.stringify({ enabled: !model.enabled }) });
      notify('模型状态已更新'); loadPage();
    });
    toggle.disabled = model.locked;
    options.append(toggle, button('删除模型', () => deleteModelDialog(model), 'danger'));
    menu.append(options); actions.append(menu); card.append(actions);
  } else card.append(button('模型详情', () => modelDetailsDialog(model, null)));
  return card;
}
async function renderOfferings(page) {
  const [offeringData, modelData, providerData] = await Promise.all([api('/offerings'), api('/models'), api('/providers')]);
  const models = modelData.items, providers = providerData.items, offerings = offeringData.items;
  const tabs = node('div', 'section-tabs'); tabs.setAttribute('role', 'tablist'); tabs.setAttribute('aria-label', '模型管理');
  const choices = [['models', '模型', models.length], ['providers', '提供商', providers.length], ['public', '公开价格', offerings.length]];
  for (const [index, [value, label, count]] of choices.entries()) {
    const tab = button(label + ' ' + count, () => { modelTab = value; loadPage(); });
    tab.setAttribute('role', 'tab'); tab.setAttribute('aria-selected', String(modelTab === value));
    tab.tabIndex = modelTab === value ? 0 : -1;
    tab.addEventListener('keydown', event => {
      if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      event.preventDefault();
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? choices.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + choices.length) % choices.length;
      modelTab = choices[next][0];
      loadPage().then(() => document.querySelector('#page [role=tab][aria-selected=true]')?.focus());
    });
    tabs.append(tab);
  }
  page.append(tabs);
  const toolbar = node('div', 'section-toolbar');
  toolbar.append(node('h2', '', modelTab === 'models' ? '模型' : modelTab === 'providers' ? '提供商' : '公开价格'));
  if (session.role === 'maintainer' && modelTab === 'providers') toolbar.append(button('新增提供商', () => providerDialog(null), 'primary'));
  if (session.role === 'maintainer' && modelTab === 'models') toolbar.append(button('新增上游模型', () => upstreamModelDialog(null), 'primary'));
  page.append(toolbar);
  const grid = node('div', 'model-grid');
  if (modelTab === 'providers') for (const provider of providers) grid.append(providerCard(provider));
  if (modelTab === 'models') {
    const grouped = new Map();
    for (const item of offerings) grouped.set(item.provider_ref, (grouped.get(item.provider_ref) || 0) + 1);
    for (const model of models) grid.append(upstreamCard(model, grouped.get(model.id) || 0, providers, offeringData));
  }
  if (modelTab === 'public') for (const offering of offerings) {
    const model = models.find(item => item.id === offering.provider_ref);
    grid.append(offeringCard(model || { id: offering.provider_ref, display_name: offering.display_name, capabilities: offering.capabilities,
      enabled: false, locked: true, protocol: '', last_test: null }, offering, offeringData, !model));
  }
  if (grid.childElementCount) page.append(grid);
  else page.append(node('div', 'empty empty-surface', modelTab === 'providers' ? '尚未配置提供商' : modelTab === 'models' ? '尚未配置模型' : '尚未发布公开条目'));
}
async function upstreamModelDialog(model) {
  const isNew = !model;
  const providers = (await api('/providers')).items;
  if (isNew && !providers.length) { providerDialog(null); return; }
  const dialog = openDialog(isNew ? '新增上游模型' : '编辑上游模型', true);
  const form = node('form');
  const id = input('text', 'id');
  id.required = isNew;
  id.value = model?.id ?? '';
  id.disabled = !isNew;
  const displayName = input('text', 'display_name');
  displayName.value = model?.display_name ?? '';
  const providerChoices = [['', isNew ? '选择提供商' : '原有独立连接'], ...providers.map(item => [item.id, item.name])];
  const provider = select('提供商', providerChoices, model?.provider_id ?? '', () => updateConnection());
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
  const connection = formSection('独立连接', field('接入协议', protocol), field('接口地址', baseURL),
    field('厂商标识（可选）', vendor), field('API 密钥', apiKey));
  function updateConnection() {
    connection.hidden = !!provider.value || isNew;
    baseURL.required = !connection.hidden;
  }
  updateConnection();
  form.append(
    formSection('基本信息',
      field('模型标识', id), field('显示名称', displayName), field('提供商', provider), field('上游模型名', upstreamModel)),
    connection,
    formSection('模型能力',
      makeCheck(vision, '支持图片输入'), makeCheck(toolCall, '支持工具调用'),
      makeCheck(reasoning, '支持推理内容'), field('上下文长度', context),
      field('单次最大输出 token', maxOutput), makeCheck(enabled, '配置允许启用'),
      makeCheck(caption, '作为图片转述模型')),
    error,
  );
  formActions(dialog, form, submit);
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
      if (isNew && !provider.value) throw new Error('请选择提供商');
      if (!provider.value && !baseURL.value.trim()) throw new Error('请填写接口地址');
      const payload = {
        provider_id: provider.value,
        display_name: displayName.value.trim(),
        upstream_model: upstreamModel.value.trim(),
        vision: vision.checked,
        tool_call: toolCall.checked,
        reasoning: reasoning.checked,
        context: Number(context.value) || 0,
        max_output_tokens: Number(maxOutput.value) || 0,
        disabled: !enabled.checked,
        is_caption: caption.checked,
      };
      if (!provider.value) {
        payload.protocol = protocolValue;
        payload.vendor = vendor.value.trim();
        payload.base_url = baseURL.value.trim();
        if (apiKey.value.trim()) payload.api_key = apiKey.value.trim();
      }
      if (isNew) payload.id = id.value.trim();
      const result = await api(isNew ? '/upstream-models' : '/upstream-models/' + encodeURIComponent(model.id), { method: isNew ? 'POST' : 'PATCH', body: JSON.stringify(payload) });
      dialog.close();
      notifyConfigResult(result, isNew ? '上游模型已添加' : '上游模型已更新');
      loadPage();
    } catch (failure) {
      error.textContent = failure.message;
      submit.disabled = false;
    }
  });
  dialog.content.append(form);
}

function deleteModelDialog(model) {
  const dialog = openDialog('删除上游模型');
  const error = node('p', 'form-error');
  error.setAttribute('role', 'alert');
  dialog.content.append(node('p', '', '删除 ' + model.display_name + '（' + model.id + '）后，该模型不能再用于作答。仍有公开条目引用时会被拒绝。'), error);
  const submit = button('删除模型', async element => {
    element.disabled = true;
    try {
      const result = await api('/upstream-models/' + encodeURIComponent(model.id), { method: 'DELETE' });
      dialog.close();
      notifyConfigResult(result, '上游模型已删除');
      loadPage();
    } catch (failure) {
      error.textContent = failure.message;
      element.disabled = false;
    }
  }, 'danger');
  dialog.footer.append(button('取消', () => dialog.close()), submit);
}

async function offeringDialog(offering, model, settings) {
  const availableModels = (await api('/models')).items;
  const selected = availableModels.some(item => item.id === model.id) ? model.id : '';
  const dialog = openDialog(offering ? '编辑公开模型' : '发布公开模型', true);
  const form = node('form');
  const modelSelect = select('关联模型', [['', '选择模型'], ...availableModels.map(item => [item.id, item.display_name + ' · ' + item.id])], selected, value => {
    model = availableModels.find(item => item.id === value) || null;
    updateVision();
  });
  model = availableModels.find(item => item.id === selected) || null;
  const name = input('text', 'display_name');
  name.value = offering?.display_name ?? model?.display_name ?? '';
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
  priceVersion.placeholder = (model?.id || 'model') + '-v1';
  const rounding = select('价格取整方式', [['ceil', '向上取整'], ['floor', '向下取整']], offering?.pricing?.rounding ?? 'ceil', () => { });
  const allowCache = input('checkbox', 'allow_cache');
  allowCache.required = false;
  allowCache.checked = offering?.cache_policy.allow ?? true;
  const scopeChoices = [['user', '用户隔离']];
  if (settings.shared_cache_enabled || offering?.cache_policy.scope === 'shared') scopeChoices.push(['shared', settings.shared_cache_enabled ? '共享缓存' : '共享缓存（当前不可用）']);
  const selectedScope = offering?.cache_policy.scope ?? 'user';
  const scope = select('缓存范围', scopeChoices, selectedScope, () => { });
  const ttl = input('number', 'cache_ttl_days');
  ttl.required = false;
  ttl.min = '1';
  ttl.max = String(settings.cache_max_ttl_days);
  ttl.value = String(offering?.cache_policy.ttl_days ?? Math.min(7, settings.cache_max_ttl_days));
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
  vision.checked = offering ? offering.capabilities.includes('vision') : !!model?.capabilities.includes('vision');
  const makeCheck = (inputNode, labelText) => {
    const row = node('div', 'check-row');
    if (!inputNode.id) inputNode.id = 'field-' + crypto.randomUUID();
    const label = node('label', '', labelText);
    label.htmlFor = inputNode.id;
    row.append(inputNode, label);
    return row;
  };
  function updateVision() {
    vision.disabled = !model || !model.capabilities.includes('vision');
    if (vision.disabled) vision.checked = false;
  }
  updateVision();
  const error = node('p', 'form-error');
  error.setAttribute('role', 'alert');
  const submit = node('button', 'primary', offering ? '保存' : '发布');
  submit.type = 'submit';
  form.append(
    formSection('公开信息', field('关联模型', modelSelect), field('公开名称', name), field('说明', description), makeCheck(vision, '支持图片题')),
    formSection('Token 价格', field('输入价格（元 / 百万 token）', inputPrice),
      field('输出价格（元 / 百万 token）', outputPrice), field('价格版本', priceVersion),
      field('价格取整方式', rounding),
      node('p', 'field-hint', '输入与输出价格都留空表示未定价。价格调整需填写新的价格版本。')),
    formSection('精确缓存', makeCheck(allowCache, '启用精确缓存'),
      field('缓存范围', scope), field('缓存期限（天）', ttl)),
    formSection('发布设置', makeCheck(enabled, '对用户可见'),
      makeCheck(isDefault, '设为默认模型'), field('排序值', sortOrder)),
    error,
  );
  formActions(dialog, form, submit);
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
      if (!model) throw new Error('请选择关联模型');
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
  dialog.content.append(form);
}
function cacheDialog(value) {
  const dialog = openDialog('缓存详情', true);
  dialog.content.append(detailGrid([['缓存编号', value.id], ['模型', value.model_id], ['范围', value.scope === 'shared' ? '共享' : '用户隔离'], ['状态', statusLabels[value.status]], ['保存时间', formatTime(value.created_at)], ['到期时间', formatTime(value.expires_at)], ['来源请求', value.source_request_id]]));
  dialog.content.append(node('h3', '', '题目'), node('p', 'detail-text', value.question_title), node('h3', '', '答案'), node('p', 'detail-text', value.answer));
  if (value.invalidation_reason) dialog.content.append(node('p', 'muted', '失效原因：' + value.invalidation_reason));
  if (session.role !== 'maintainer' || value.status !== 'active') return;
  const form = node('form');
  const reason = node('textarea');
  reason.name = 'reason';
  reason.required = true;
  reason.maxLength = 1000;
  const error = node('p', 'form-error');
  const submit = node('button', 'danger', '使此缓存失效');
  submit.type = 'submit';
  error.setAttribute('role', 'alert');
  form.append(field('失效原因', reason), error);
  formActions(dialog, form, submit);
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (submit.disabled) return;
    submit.disabled = true;
    try { await api('/cache/' + encodeURIComponent(value.id) + '/invalidations', { method: 'POST', body: JSON.stringify({ reason: reason.value }) }); dialog.close(); notify('缓存已失效'); loadPage(); } catch (failure) { error.textContent = failure.message; submit.disabled = false; }
  });
  dialog.content.append(form);
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
  const appearanceCard = node('section', 'settings-section');
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
  const serviceCard = node('section', 'settings-section');
  serviceCard.append(node('h2', '', '当前服务设置'), detailGrid([
    ['默认缓存期限', settings.cache_ttl_days + ' 天'], ['最长缓存期限', settings.cache_max_ttl_days + ' 天'], ['共享缓存', settings.shared_cache_enabled ? '已启用' : '未启用'],
    ['默认公开条目', settings.default_offering_id || '未指定'], ['已发布条目', settings.published_offering_count + ' / ' + settings.offering_count],
    ['图片转述模型', settings.caption_model_id || '未指定'], ['配置模型数量', settings.model_count],
    ['同时处理查询', settings.concurrency], ['查询超时', settings.timeout_seconds + ' 秒'], ['最大输出 token', formatNumber(settings.max_output_tokens)],
  ]));
  page.append(appearanceCard, serviceCard);
}
const pageViews = new Map();
function loadingSurface() {
  const surface = node('div', 'table-wrap');
  surface.setAttribute('role', 'status');
  surface.setAttribute('aria-label', '正在加载');
  for (let row = 0; row < 6; row++) {
    const line = node('div', 'skeleton-row');
    for (let column = 0; column < 4; column++) line.append(node('div', 'skeleton'));
    surface.append(line);
  }
  return surface;
}
async function loadPage() {
  if (!session) return;
  const generation = ++epoch;
  currentDialog?.close();
  closeSelect();
  let route = location.hash.slice(1) || 'overview';
  if (!navigation.some(value => value[0] === route)) route = 'overview';
  for (const element of document.querySelectorAll('[data-route]')) {
    if (element.dataset.route === route) element.setAttribute('aria-current', 'page'); else element.removeAttribute('aria-current');
  }
  document.querySelector('.navigation')?.updateIndicator?.();
  const main = document.querySelector('#page');
  if (!main) return;
  const changed = main.dataset.page !== route;
  if (changed && pageViews.has(main.dataset.page)) pageViews.get(main.dataset.page).scroll = window.scrollY;
  let view = pageViews.get(route);
  if (!view) {
    const heading = node('div', 'page-heading');
    const actions = node('div', 'actions');
    actions.append(button('刷新', () => loadPage()));
    heading.append(node('h1', '', navigation.find(value => value[0] === route)[1]), actions);
    const body = node('div', 'page-body');
    body.append(loadingSurface());
    view = { heading, body, scroll: 0, loaded: false };
    pageViews.set(route, view);
  }
  if (changed) {
    main.dataset.page = route;
    main.replaceChildren(node('div', 'page-progress'), view.heading, view.body);
    view.body.classList.add('entering');
    window.scrollTo(0, view.scroll);
  }
  main.classList.add('is-loading');
  view.body.setAttribute('aria-busy', 'true');
  const focused = document.activeElement;
  const focusLabel = view.body.contains(focused) ? focused.getAttribute('aria-label') : null;
  const content = node('div');
  try {
    if (route === 'overview') await renderOverview(content);
    else if (route === 'offerings') await renderOfferings(content);
    else if (route === 'settings') await renderSettings(content);
    else await renderList(content, route);
    if (generation !== epoch) return;
    view.body.replaceChildren(...content.childNodes);
    view.loaded = true;
    if (changed) window.scrollTo(0, view.scroll);
    if (focusLabel && !focused.isConnected && document.activeElement === document.body) {
      [...view.body.querySelectorAll('button[aria-label]')].find(item => item.getAttribute('aria-label') === focusLabel)?.focus({ preventScroll: true });
    }
  } catch (error) {
    if (generation !== epoch) return;
    const failure = node('div', 'error-state', error.message);
    failure.setAttribute('role', 'alert');
    failure.append(button('重试', () => loadPage()));
    if (view.loaded) view.body.prepend(failure); else view.body.replaceChildren(failure);
  } finally {
    if (generation === epoch) {
      main.classList.remove('is-loading');
      view.body.removeAttribute('aria-busy');
    }
  }
}
window.addEventListener('hashchange', loadPage);
try { session = await api('/session'); renderShell(); } catch { renderLogin(); }
