// 原生表单保留提交值；自定义触发器负责键盘、焦点和列表呈现。
let activeSelect;
const element = (tag, className, text) => {
  const result = document.createElement(tag);
  result.className = className;
  if (text != null) result.textContent = text;
  return result;
};
export function closeSelect(focus = false) { activeSelect?.close(focus); }
document.addEventListener('pointerdown', event => {
  if (activeSelect && !activeSelect.trigger.contains(event.target) && !activeSelect.list.contains(event.target)) closeSelect();
});
window.addEventListener('resize', () => activeSelect?.position());
window.addEventListener('scroll', () => activeSelect?.position(), true);

export function enhanceSelect(native) {
  if (native.parentElement?.classList.contains('custom-select')) return native.parentElement;
  const wrapper = element('div', 'custom-select');
  const trigger = element('button', 'select-trigger');
  trigger.type = 'button';
  trigger.id = 'select-' + crypto.randomUUID();
  trigger.setAttribute('role', 'combobox');
  trigger.setAttribute('aria-haspopup', 'listbox');
  trigger.setAttribute('aria-expanded', 'false');
  if (native.getAttribute('aria-label')) trigger.setAttribute('aria-label', native.getAttribute('aria-label'));
  native.hidden = true;
  native.tabIndex = -1;
  native.setAttribute('aria-hidden', 'true');
  const label = element('span', 'select-value');
  const chevron = element('span', 'select-chevron', '⌄');
  chevron.setAttribute('aria-hidden', 'true');
  trigger.append(label, chevron);
  wrapper.append(native, trigger);
  wrapper.control = trigger;
  const sync = () => {
    label.textContent = native.selectedOptions[0]?.textContent || '请选择';
    trigger.disabled = native.disabled;
  };
  for (const property of ['value', 'name', 'disabled']) {
    Object.defineProperty(wrapper, property, { get: () => native[property], set: value => { native[property] = value; sync(); } });
  }
  native.addEventListener('change', sync);
  new MutationObserver(sync).observe(native, { childList: true, subtree: true, attributes: true, attributeFilter: ['disabled', 'selected'] });
  sync();
  let highlighted = -1;
  const options = () => [...native.options];
  const highlight = index => {
    const items = options();
    highlighted = Math.max(0, Math.min(index, items.length - 1));
    activeSelect?.list.querySelectorAll('[role=option]').forEach((item, position) => item.classList.toggle('highlighted', position === highlighted));
    const id = trigger.id + '-option-' + highlighted;
    trigger.setAttribute('aria-activedescendant', id);
    document.getElementById(id)?.scrollIntoView({ block: 'nearest' });
  };
  const commit = index => {
    const option = options()[index];
    if (!option || option.disabled) return;
    native.value = option.value;
    native.dispatchEvent(new Event('change', { bubbles: true }));
    closeSelect(true);
  };
  const open = () => {
    closeSelect();
    const list = element('div', 'select-listbox');
    list.id = trigger.id + '-list';
    list.setAttribute('role', 'listbox');
    list.setAttribute('aria-label', trigger.getAttribute('aria-label') || label.textContent);
    for (const [index, option] of options().entries()) {
      const item = element('div', 'select-option', option.textContent);
      item.id = trigger.id + '-option-' + index;
      item.setAttribute('role', 'option');
      item.setAttribute('aria-selected', String(option.selected));
      item.setAttribute('aria-disabled', String(option.disabled));
      item.addEventListener('pointerdown', event => event.preventDefault());
      item.addEventListener('click', () => commit(index));
      list.append(item);
    }
    const host = native.closest('dialog') || document.body;
    host.append(list);
    const position = () => {
      const rect = trigger.getBoundingClientRect();
      const width = Math.min(Math.max(rect.width, 160), innerWidth - 32);
      const below = innerHeight - rect.bottom - 12;
      const above = rect.top - 12;
      const upward = below < 160 && above > below;
      const height = Math.min(240, upward ? above : below);
      Object.assign(list.style, { left: Math.max(16, Math.min(rect.left, innerWidth - width - 16)) + 'px', width: width + 'px', maxHeight: Math.max(80, height) + 'px', top: upward ? 'auto' : rect.bottom + 4 + 'px', bottom: upward ? innerHeight - rect.top + 4 + 'px' : 'auto' });
    };
    activeSelect = { trigger, list, position, close(focus) {
      list.remove();
      trigger.setAttribute('aria-expanded', 'false');
      trigger.removeAttribute('aria-activedescendant');
      activeSelect = undefined;
      if (focus && trigger.isConnected) trigger.focus();
    } };
    trigger.setAttribute('aria-controls', list.id);
    trigger.setAttribute('aria-expanded', 'true');
    position();
    highlight(Math.max(0, native.selectedIndex));
  };
  trigger.addEventListener('click', () => activeSelect?.trigger === trigger ? closeSelect() : open());
  trigger.addEventListener('keydown', event => {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End', 'Enter', ' ', 'Escape', 'Tab'].includes(event.key)) return;
    if (event.key === 'Tab') { closeSelect(); return; }
    event.preventDefault();
    if (event.key === 'Escape') { event.stopPropagation(); closeSelect(true); return; }
    if (activeSelect?.trigger !== trigger) { open(); return; }
    if (event.key === 'Enter' || event.key === ' ') { commit(highlighted); return; }
    const last = options().length - 1;
    highlight(event.key === 'Home' ? 0 : event.key === 'End' ? last : event.key === 'ArrowDown' ? (highlighted + 1) % (last + 1) : (highlighted + last) % (last + 1));
  });
  return wrapper;
}

export function installIndicator(container, selector) {
  const indicator = element('span', 'selection-indicator');
  indicator.setAttribute('aria-hidden', 'true');
  container.prepend(indicator);
  const update = () => {
    const selected = container.querySelector(selector);
    if (!selected) { indicator.style.opacity = '0'; return; }
    const parent = container.getBoundingClientRect();
    const rect = selected.getBoundingClientRect();
    Object.assign(indicator.style, { width: rect.width + 'px', height: rect.height + 'px', transform: `translate(${rect.left - parent.left}px, ${rect.top - parent.top}px)`, opacity: '1' });
  };
  const observer = new ResizeObserver(update);
  observer.observe(container);
  container.updateIndicator = update;
  container.disposeIndicator = () => observer.disconnect();
  update();
}

document.addEventListener('submit', event => {
  const form = event.target;
  const submit = event.submitter || [...(form.elements || [])].find(item => item instanceof HTMLButtonElement && item.type === 'submit');
  if (!submit || submit.disabled) return;
  markButtonBusy(submit, true);
  const finish = () => { markButtonBusy(submit, false); observer.disconnect(); };
  const observer = new MutationObserver(() => { if (!submit.disabled) finish(); });
  observer.observe(submit, { attributes: true, attributeFilter: ['disabled'] });
  queueMicrotask(() => { if (!submit.disabled) finish(); });
}, true);

export function markButtonBusy(button, busy) {
  if (busy) {
    button.style.width = button.getBoundingClientRect().width + 'px';
    button.setAttribute('aria-busy', 'true');
    button.classList.add('is-busy');
  } else {
    button.style.removeProperty('width');
    button.removeAttribute('aria-busy');
    button.classList.remove('is-busy');
  }
}
