'use strict';

/* ------------------------------------------------------------------ state */

const state = {
  summaries: [],
  selectedId: null,
  exchange: null,
  detailSig: '',
  summarySig: '',
  config: null,
  paused: false,
  polling: false,
  filter: '',
  tab: 'messages',
  follow: true,
};

const TABS = [
  ['messages', 'Messages'],
  ['response', 'Response'],
  ['request', 'Request body'],
  ['raw', 'Response body'],
  ['events', 'Events'],
  ['headers', 'Headers'],
];

/* ------------------------------------------------------------------ dom */

const $ = (id) => document.getElementById(id);

function el(tag, attrs, ...children) {
  const node = document.createElement(tag);
  if (attrs) {
    for (const [key, value] of Object.entries(attrs)) {
      if (value === null || value === undefined || value === false) continue;
      if (key === 'class') {
        node.className = value;
      } else if (key === 'dataset') {
        Object.assign(node.dataset, value);
      } else if (key.startsWith('on') && typeof value === 'function') {
        node.addEventListener(key.slice(2), value);
      } else {
        node.setAttribute(key, value === true ? '' : String(value));
      }
    }
  }
  appendChildren(node, children);
  return node;
}

function appendChildren(node, children) {
  for (const child of children.flat(Infinity)) {
    if (child === null || child === undefined || child === false) continue;
    node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
}

/* ------------------------------------------------------------------ boot */

async function boot() {
  try {
    state.config = await fetchJSON('/api/config');
    if (state.config) {
      $('upstream').textContent = state.config.upstream || '';
      $('upstream').title = state.config.upstream || '';
    }
  } catch {
    /* the list poll reports the connection state */
  }

  const id = idFromHash();
  if (id) state.selectedId = id;

  $('filter').addEventListener('input', (event) => {
    state.filter = event.target.value;
    renderList();
  });

  $('pause').addEventListener('click', () => {
    state.paused = !state.paused;
    $('pause').textContent = state.paused ? 'Resume' : 'Pause';
    $('pause').classList.toggle('active', state.paused);
    if (!state.paused) poll();
  });

  $('clear').addEventListener('click', clearAll);
  window.addEventListener('popstate', () => {
    const next = idFromHash();
    if (next && next !== state.selectedId) select(next, false);
    if (!next) {
      state.selectedId = null;
      state.exchange = null;
      renderList();
      renderEmpty($('detail'));
    }
  });

  document.addEventListener('keydown', (event) => {
    if (event.key === '/' && document.activeElement !== $('filter')) {
      event.preventDefault();
      $('filter').focus();
    } else if (event.key === 'Escape' && document.activeElement === $('filter')) {
      $('filter').value = '';
      state.filter = '';
      renderList();
      $('filter').blur();
    }
  });

  renderEmpty($('detail'));
  poll();
}

/* ------------------------------------------------------------------ polling */

async function poll() {
  if (state.paused || state.polling) return;
  state.polling = true;
  try {
    const data = await fetchJSON('/api/exchanges');
    setConnection('live', true);
    state.summaries = data.exchanges || [];
    renderList();
    if (!state.selectedId) renderEmpty($('detail'));

    if (state.selectedId) {
      const summary = state.summaries.find((item) => item.id === state.selectedId);
      const sig = summary
        ? `${summary.state}|${summary.status}|${summary.duration_ms}|${summary.output_chars}`
        : 'missing';
      if (!summary || summary.state === 'in_flight' || sig !== state.summarySig) {
        state.summarySig = sig;
        await refreshDetail();
      }
    }
  } catch {
    setConnection('disconnected', false);
  } finally {
    state.polling = false;
    if (!state.paused) setTimeout(poll, 1200);
  }
}

async function refreshDetail() {
  const id = state.selectedId;
  if (!id) return;
  let record;
  try {
    record = await fetchJSON('/api/exchanges/' + encodeURIComponent(id));
  } catch (error) {
    if (state.selectedId !== id) return;
    state.selectedId = null;
    state.exchange = null;
    history.replaceState(null, '', location.pathname);
    renderList();
    renderEmpty($('detail'), String(error.message || error));
    return;
  }
  if (state.selectedId !== id) return;
  const sig = recordSignature(record);
  state.exchange = record;
  if (sig === state.detailSig) return;
  state.detailSig = sig;
  renderDetail();
}

function recordSignature(record) {
  return [
    record.state,
    record.status,
    record.duration_ms,
    record.events ? record.events.length : 0,
    (record.output_text || '').length,
    record.response_body ? 1 : 0,
    record.error || '',
  ].join('|');
}

function setConnection(text, ok) {
  const node = $('conn');
  node.textContent = state.paused ? 'paused' : text;
  node.className = 'conn ' + (state.paused ? '' : ok ? 'ok' : 'bad');
}

/* ------------------------------------------------------------------ list */

function renderList() {
  const list = $('list');
  const scroll = list.scrollTop;
  const term = state.filter.trim().toLowerCase();

  const rows = state.summaries.filter((summary) => {
    if (!term) return true;
    return [
      summary.model,
      summary.path,
      summary.api,
      summary.status,
      summary.state,
      summary.finish_reason,
      summary.id,
    ]
      .filter(Boolean)
      .join(' ')
      .toLowerCase()
      .includes(term);
  });

  $('count').textContent = term
    ? `${rows.length}/${state.summaries.length}`
    : String(state.summaries.length);

  if (!rows.length) {
    list.replaceChildren(
      el(
        'div',
        { class: 'empty-tab list-empty' },
        state.summaries.length ? 'No request matches the filter.' : 'No requests yet.',
      ),
    );
    return;
  }
  list.replaceChildren(...rows.map(summaryRow));
  list.scrollTop = scroll;
}

function summaryRow(summary) {
  const status = statusInfo(summary);
  const selected = summary.id === state.selectedId;
  return el(
    'button',
    {
      class: 'row' + (selected ? ' selected' : ''),
      type: 'button',
      role: 'option',
      'aria-selected': selected ? 'true' : 'false',
      onclick: () => select(summary.id, true),
    },
    el(
      'div',
      { class: 'row-top' },
      el('span', { class: 'status ' + status.cls }, el('span', { class: 'dot' }), status.text),
      el('span', { class: 'badge api' }, apiLabel(summary.api)),
      summary.stream ? el('span', { class: 'badge stream' }, 'stream') : null,
      el('span', { class: 'time' }, formatTime(summary.started_at)),
    ),
    el(
      'div',
      { class: 'row-model' + (summary.model ? '' : ' empty') },
      summary.model || '(no model)',
    ),
    el('div', { class: 'row-meta' }, summaryMeta(summary)),
  );
}

function summaryMeta(summary) {
  const parts = [];
  if (summary.usage) {
    parts.push('in ' + formatCount(summary.usage.input_tokens));
    parts.push('out ' + formatCount(summary.usage.output_tokens));
  }
  if (summary.duration_ms) parts.push(formatDuration(summary.duration_ms));
  if (summary.finish_reason) parts.push(summary.finish_reason);
  if (!parts.length) {
    parts.push(summary.state === 'in_flight' ? 'waiting for provider' : summary.path);
  }
  return parts.join(' · ');
}

function statusInfo(record) {
  if (record.state === 'in_flight') {
    return { cls: 'live', text: record.stream ? 'streaming' : 'waiting' };
  }
  if (record.state === 'canceled') {
    return { cls: 'warn', text: record.status ? String(record.status) : 'canceled' };
  }
  if (record.state === 'error' || record.state === 'interrupted') {
    return { cls: 'bad', text: record.status ? String(record.status) : 'error' };
  }
  const code = record.status;
  if (!code) return { cls: '', text: '—' };
  if (code < 300) return { cls: 'ok', text: String(code) };
  if (code < 500) return { cls: 'warn', text: String(code) };
  return { cls: 'bad', text: String(code) };
}

/* ------------------------------------------------------------------ select */

function select(id, push) {
  state.selectedId = id;
  state.exchange = null;
  state.detailSig = '';
  state.summarySig = '';
  if (push) history.pushState(null, '', '#/e/' + encodeURIComponent(id));
  renderList();
  $('detail').scrollTop = 0;
  $('detail').replaceChildren(el('div', { class: 'empty-tab' }, 'Loading…'));
  refreshDetail();
}

async function clearAll() {
  if (!window.confirm('Delete all captured exchanges?')) return;
  try {
    await fetchJSON('/api/exchanges', { method: 'DELETE' });
    state.summaries = [];
    state.selectedId = null;
    state.exchange = null;
    state.detailSig = '';
    state.summarySig = '';
    history.replaceState(null, '', location.pathname);
    renderList();
    renderEmpty($('detail'));
  } catch (error) {
    window.alert('Clear failed: ' + (error.message || error));
  }
}

/* ------------------------------------------------------------------ detail */

function renderDetail() {
  const host = $('detail');
  const record = state.exchange;
  if (!record) {
    renderEmpty(host);
    return;
  }
  const scroll = host.scrollTop;
  const atBottom = host.scrollTop + host.clientHeight >= host.scrollHeight - 48;
  const openEvents = collectOpenEvents(host);

  host.replaceChildren(detailHead(record), tabsBar(record), ...tabContent(record, openEvents));

  if (state.tab === 'events' && state.follow && atBottom && record.state === 'in_flight') {
    host.scrollTop = host.scrollHeight;
  } else {
    host.scrollTop = scroll;
  }
}

function detailHead(record) {
  const status = statusInfo(record);
  const line2 = [
    record.model || '(no model)',
    record.id,
    formatDateTime(record.started_at),
    record.duration_ms ? formatDuration(record.duration_ms) : null,
    record.ttfb_ms !== undefined ? 'ttfb ' + formatDuration(record.ttfb_ms) : null,
  ]
    .filter(Boolean)
    .join(' · ');

  const chips = [];
  if (record.usage) {
    chips.push(chip('input', formatCount(record.usage.input_tokens)));
    chips.push(chip('output', formatCount(record.usage.output_tokens)));
    if (record.usage.total_tokens) chips.push(chip('total', formatCount(record.usage.total_tokens)));
    if (record.usage.reasoning_tokens) {
      chips.push(chip('reasoning', formatCount(record.usage.reasoning_tokens)));
    }
    if (record.usage.cached_input_tokens) {
      chips.push(chip('cached', formatCount(record.usage.cached_input_tokens)));
    }
  }
  if (record.finish_reason) chips.push(chip('finish', record.finish_reason));
  if (record.output_text) {
    chips.push(chip('output text', formatCount(record.output_text.length) + ' chars'));
  }
  if (record.events && record.events.length) {
    chips.push(chip('events', String(record.events.length)));
  }
  if (record.body_truncated) chips.push(chip('capture', 'truncated', 'bad'));
  if (record.events_truncated) chips.push(chip('events', 'truncated', 'bad'));

  const nodes = [
    el(
      'div',
      { class: 'line1' },
      el('span', { class: 'method' }, record.method),
      el('span', { class: 'path' }, record.path + (record.query ? '?' + record.query : '')),
      el('span', { class: 'status ' + status.cls }, el('span', { class: 'dot' }), status.text),
      el('span', { class: 'badge api' }, apiLabel(record.api)),
      record.stream ? el('span', { class: 'badge stream' }, 'stream') : null,
      record.state === 'in_flight' ? el('span', { class: 'badge' }, 'in flight') : null,
    ),
    el('div', { class: 'line2' }, line2),
    el('div', { class: 'line3' }, 'upstream ' + record.upstream_url),
  ];

  if (record.error) nodes.push(el('div', { class: 'banner' }, record.error));
  if (chips.length) nodes.push(el('div', { class: 'chips' }, ...chips));
  nodes.push(
    el(
      'div',
      { class: 'toolbar' },
      el(
        'button',
        {
          class: 'ghost',
          type: 'button',
          onclick: async (event) => {
            const button = event.currentTarget;
            const ok = await copyText(JSON.stringify(state.exchange, null, 2));
            button.textContent = ok ? 'Copied' : 'Copy failed';
            setTimeout(() => {
              button.textContent = 'Copy JSON';
            }, 1200);
          },
        },
        'Copy JSON',
      ),
    ),
  );

  return el('header', { class: 'detail-head' }, ...nodes);
}

function tabsBar(record) {
  const counts = {
    messages: requestItemCount(record),
    events: record.events ? record.events.length : 0,
  };
  const tabs = TABS.map(([key, label]) => {
    let text = label;
    if (key === 'messages' && counts.messages) text += ` (${counts.messages})`;
    if (key === 'events') text += ` (${counts.events})`;
    return el(
      'button',
      {
        class: 'tab' + (state.tab === key ? ' active' : ''),
        type: 'button',
        onclick: () => {
          state.tab = key;
          state.detailSig = '';
          $('detail').scrollTop = 0;
          renderDetail();
        },
      },
      text,
    );
  });

  const note =
    state.tab === 'events'
      ? el(
          'label',
          { class: 'tab-note' },
          el('input', {
            type: 'checkbox',
            checked: state.follow,
            onchange: (event) => {
              state.follow = event.target.checked;
              state.detailSig = '';
              renderDetail();
            },
          }),
          'follow',
        )
      : null;

  return el('nav', { class: 'tabs' }, ...tabs, note);
}

function tabContent(record, openEvents) {
  switch (state.tab) {
    case 'messages':
      return requestTab(record);
    case 'response':
      return responseTab(record);
    case 'request':
      return [rawBody(record, 'request')];
    case 'raw':
      return [rawBody(record, 'response')];
    case 'events':
      return eventsTab(record, openEvents);
    case 'headers':
      return headersTab(record);
    default:
      return requestTab(record);
  }
}

/* ------------------------------------------------------------------ request */

function requestTab(record) {
  const body = record.request_body ? record.request_body.json : null;
  if (!body) {
    const text = record.request_body ? record.request_body.text : '';
    if (text) return [el('pre', { class: 'content' }, text)];
    return [el('div', { class: 'empty-tab' }, 'No request body captured.')];
  }

  const nodes = [];
  const shape = [];
  if (body.model) shape.push(chip('model', body.model));
  shape.push(chip('stream', body.stream ? 'yes' : 'no'));
  if (Array.isArray(body.tools)) shape.push(chip('tools', String(body.tools.length)));
  if (body.temperature !== undefined) shape.push(chip('temperature', String(body.temperature)));
  if (body.max_output_tokens !== undefined) {
    shape.push(chip('max output', String(body.max_output_tokens)));
  }
  if (body.max_tokens !== undefined) shape.push(chip('max tokens', String(body.max_tokens)));
  nodes.push(el('div', { class: 'chips' }, ...shape));

  if (record.api === 'chat_completions' || Array.isArray(body.messages)) {
    if (Array.isArray(body.messages)) {
      nodes.push(
        el('div', { class: 'section-title' }, `Messages (${body.messages.length})`),
        ...body.messages.map(chatMessageCard),
      );
    }
  } else if (record.api === 'responses' || body.input !== undefined) {
    if (body.instructions) {
      nodes.push(
        el('div', { class: 'section-title' }, 'Instructions'),
        messageCard('system', body.instructions),
      );
    }
    if (typeof body.input === 'string') {
      nodes.push(
        el('div', { class: 'section-title' }, 'Input'),
        messageCard('user', body.input),
      );
    } else if (Array.isArray(body.input)) {
      const count = body.input.length;
      nodes.push(
        el('div', { class: 'section-title' }, `Input (${count} item${count === 1 ? '' : 's'})`),
        ...body.input.map(responseItemCard),
      );
    }
  }

  const options = requestOptions(body);
  if (options) nodes.push(options);
  return nodes;
}

function requestOptions(body) {
  const keys = [
    'tool_choice',
    'parallel_tool_calls',
    'reasoning',
    'reasoning_effort',
    'text',
    'response_format',
    'top_p',
    'top_k',
    'max_output_tokens',
    'max_tokens',
    'instructions',
    'store',
    'metadata',
  ];
  const picked = {};
  for (const key of keys) {
    if (body[key] !== undefined && key !== 'instructions') picked[key] = body[key];
  }
  if (!Object.keys(picked).length) return null;
  return el(
    'details',
    { class: 'event' },
    el('summary', {}, el('span', { class: 'evname' }, 'Request options')),
    el('pre', { class: 'content' }, prettyJSON(picked)),
  );
}

function requestItemCount(record) {
  const body = record.request_body ? record.request_body.json : null;
  if (!body) return 0;
  if (Array.isArray(body.messages)) return body.messages.length;
  if (Array.isArray(body.input)) return body.input.length;
  if (body.input !== undefined) return 1;
  return 0;
}

function chatMessageCard(message) {
  if (!message || typeof message !== 'object') return rawCard('message', message);
  const extras = [];
  if (Array.isArray(message.tool_calls)) {
    for (const call of message.tool_calls) {
      const fn = call.function || {};
      extras.push(toolCallCard(fn.name, fn.arguments, call.id));
    }
  }
  if (message.function_call) {
    extras.push(
      toolCallCard(message.function_call.name, message.function_call.arguments, null),
    );
  }
  const meta = [message.name, message.tool_call_id].filter(Boolean).join(' · ');
  const content = message.refusal ? [message.content, message.refusal].filter(Boolean) : message.content;
  return messageCard(message.role || 'message', content, extras, meta);
}

function responseItemCard(item) {
  if (!item || typeof item !== 'object') return rawCard('item', item);
  const type = item.type || (item.role ? 'message' : 'item');
  if (type === 'message') {
    return messageCard(item.role || 'message', item.content, [], item.id || '');
  }
  if (type === 'function_call' || type === 'custom_tool_call') {
    return toolCallCard(item.name, item.arguments || item.input, item.call_id || item.id);
  }
  if (type === 'function_call_output' || type === 'custom_tool_call_output') {
    return messageCard('tool', item.output, [], item.call_id || '');
  }
  if (type === 'reasoning') {
    const summary = Array.isArray(item.summary)
      ? item.summary.map((part) => part.text || '').join('\n')
      : '';
    return messageCard('reasoning', summary || '(encrypted reasoning)', [], item.id || '');
  }
  if (type === 'item_reference') {
    return messageCard('reference', item.id || '', []);
  }
  return rawCard(type, item);
}

/* ------------------------------------------------------------------ response */

function responseTab(record) {
  const body = record.response_body ? record.response_body.json : null;
  if (body) {
    if (record.api === 'chat_completions' || Array.isArray(body.choices)) {
      return chatResponseNodes(body);
    }
    if (record.api === 'responses' || Array.isArray(body.output)) {
      return responsesResponseNodes(body);
    }
    return [el('pre', { class: 'content' }, prettyJSON(body))];
  }

  if (record.events && record.events.length) {
    if (record.api === 'chat_completions') return chatStreamNodes(record.events);
    if (record.api === 'responses') return responsesStreamNodes(record.events);
    return [el('div', { class: 'empty-tab' }, 'Streamed events are in the Events tab.')];
  }

  if (record.response_body && record.response_body.text) {
    return [el('pre', { class: 'content' }, record.response_body.text)];
  }
  if (record.state === 'in_flight') {
    return [el('div', { class: 'empty-tab' }, 'Waiting for the provider…')];
  }
  return [el('div', { class: 'empty-tab' }, 'No response body captured.')];
}

function chatResponseNodes(body) {
  const nodes = [];
  const choices = Array.isArray(body.choices) ? body.choices : [];
  for (const [index, choice] of choices.entries()) {
    const message = choice.message || {};
    const meta = [];
    if (choices.length > 1) meta.push('choice ' + index);
    if (choice.finish_reason) meta.push(choice.finish_reason);
    if (message.reasoning_content) {
      nodes.push(messageCard('reasoning', message.reasoning_content));
    }
    const extras = [];
    for (const call of message.tool_calls || []) {
      const fn = call.function || {};
      extras.push(toolCallCard(fn.name, fn.arguments, call.id));
    }
    if (message.refusal) extras.push(messageCard('refusal', message.refusal));
    nodes.push(
      messageCard('assistant', message.content || '', extras, meta.join(' · ')),
    );
  }
  if (body.error) nodes.unshift(errorCard(body.error));
  return nodes;
}

function responsesResponseNodes(body) {
  const nodes = [];
  if (Array.isArray(body.output)) {
    for (const item of body.output) {
      nodes.push(responseItemCard(item));
    }
  }
  if (!nodes.length && body.output_text) {
    nodes.push(messageCard('assistant', body.output_text));
  }
  if (body.error) nodes.unshift(errorCard(body.error));
  return nodes;
}

function chatStreamNodes(events) {
  let text = '';
  const calls = new Map();
  for (const event of events) {
    const data = event.data;
    if (!data || typeof data !== 'object') continue;
    for (const choice of data.choices || []) {
      const delta = choice.delta || {};
      if (typeof delta.content === 'string') text += delta.content;
      if (typeof delta.refusal === 'string') text += delta.refusal;
      for (const call of delta.tool_calls || []) {
        const index = call.index === undefined ? 0 : call.index;
        if (!calls.has(index)) calls.set(index, { id: '', name: '', arguments: '' });
        const acc = calls.get(index);
        if (call.id) acc.id = call.id;
        if (call.function && call.function.name) acc.name += call.function.name;
        if (call.function && call.function.arguments) acc.arguments += call.function.arguments;
      }
    }
  }
  const extras = [];
  for (const call of calls.values()) {
    extras.push(toolCallCard(call.name, call.arguments, call.id));
  }
  if (!text && !extras.length) {
    return [el('div', { class: 'empty-tab' }, 'No assistant content yet.')];
  }
  return [messageCard('assistant', text, extras)];
}

function responsesStreamNodes(events) {
  const items = [];
  let text = '';
  for (const event of events) {
    const data = event.data;
    if (!data || typeof data !== 'object') continue;
    if (event.name === 'response.output_text.delta' && typeof data.delta === 'string') {
      text += data.delta;
    }
    if (event.name === 'response.output_item.done' && data.item) {
      items.push(data.item);
    }
  }
  const nodes = [];
  let hasMessage = false;
  for (const item of items) {
    if (item && item.type === 'message') hasMessage = true;
    nodes.push(responseItemCard(item));
  }
  if (!hasMessage && text) nodes.push(messageCard('assistant', text));
  if (!nodes.length) {
    return [el('div', { class: 'empty-tab' }, 'No assistant content yet.')];
  }
  return nodes;
}

/* ------------------------------------------------------------------ events */

function eventsTab(record, openEvents) {
  const events = record.events || [];
  if (!events.length) {
    return [
      el(
        'div',
        { class: 'empty-tab' },
        record.state === 'in_flight'
          ? 'Waiting for the first event…'
          : 'No streamed events. This response was not a server-sent stream.',
      ),
    ];
  }
  if (record.events_truncated) {
    return [
      el('div', { class: 'banner' }, `Only the first ${events.length} events were captured.`),
      ...eventRows(events, openEvents),
    ];
  }
  return eventRows(events, openEvents);
}

function eventRows(events, openEvents) {
  return events.map((event, index) => {
    const name = event.name || 'chunk';
    return el(
      'details',
      { class: 'event', dataset: { index: String(index) }, open: openEvents.has(String(index)) },
      el(
        'summary',
        {},
        el('span', { class: 'idx' }, '#' + index),
        el('span', { class: 'evname' }, name),
        el('span', { class: 'evpreview' }, previewValue(event.data)),
      ),
      el('pre', { class: 'content' }, prettyJSON(event.data)),
    );
  });
}

function collectOpenEvents(host) {
  const open = new Set();
  host.querySelectorAll('details.event[open]').forEach((node) => {
    if (node.dataset.index !== undefined) open.add(node.dataset.index);
  });
  return open;
}

/* ------------------------------------------------------------------ headers */

function headersTab(record) {
  return [
    el('div', { class: 'section-title' }, 'Request headers'),
    headerTable(record.request_headers),
    el('div', { class: 'section-title' }, 'Response headers'),
    headerTable(record.response_headers),
  ];
}

function headerTable(headers) {
  const entries = Object.entries(headers || {}).sort(([a], [b]) => a.localeCompare(b));
  if (!entries.length) return el('div', { class: 'empty-tab' }, 'None');
  const table = el('table', { class: 'headers' });
  for (const [name, values] of entries) {
    table.append(
      el(
        'tr',
        {},
        el('td', {}, name),
        el('td', {}, Array.isArray(values) ? values.join(', ') : String(values)),
      ),
    );
  }
  return table;
}

/* ------------------------------------------------------------------ blocks */

function messageCard(role, content, extras = [], meta = '') {
  const hasContent =
    content !== null &&
    content !== undefined &&
    content !== '' &&
    !(Array.isArray(content) && content.length === 0);
  const nodes = [];
  if (hasContent || extras.length === 0) nodes.push(...contentNodes(content));
  nodes.push(...extras);
  return el(
    'article',
    { class: 'message role-' + roleClass(role) },
    el(
      'div',
      { class: 'message-head' },
      el('span', { class: 'role' }, role || 'item'),
      meta ? el('span', { class: 'message-meta' }, meta) : null,
    ),
    ...nodes,
  );
}

function contentNodes(content) {
  if (content === null || content === undefined || content === '') {
    return [el('div', { class: 'empty-tab' }, '(empty)')];
  }
  if (typeof content === 'string') {
    return [el('pre', { class: 'content' }, content)];
  }
  if (Array.isArray(content)) {
    let text = '';
    const others = [];
    for (const part of content) {
      if (typeof part === 'string') {
        text += part;
        continue;
      }
      if (!part || typeof part !== 'object') {
        text += String(part);
        continue;
      }
      const type = part.type;
      if (type === 'text' || type === 'input_text' || type === 'output_text') {
        text += part.text || '';
      } else if (type === 'refusal') {
        text += part.refusal || '';
      } else if (type === 'image_url' || type === 'input_image') {
        others.push(chip('image', imageLabel(part)));
      } else if (type === 'input_audio') {
        others.push(chip('audio', part.input_audio ? part.input_audio.format || 'input' : 'input'));
      } else {
        others.push(chip(type || 'part', ''));
      }
    }
    const nodes = [];
    if (text) nodes.push(el('pre', { class: 'content' }, text));
    if (others.length) nodes.push(el('div', { class: 'chips' }, ...others));
    return nodes.length ? nodes : [el('div', { class: 'empty-tab' }, '(empty)')];
  }
  return [el('pre', { class: 'content' }, prettyJSON(content))];
}

function toolCallCard(name, args, callId) {
  return el(
    'article',
    { class: 'toolcall' },
    el(
      'div',
      { class: 'message-head' },
      el('span', { class: 'role' }, 'tool call'),
      el('span', { class: 'toolname' }, name || '(unnamed)'),
      callId ? el('span', { class: 'message-meta' }, callId) : null,
    ),
    el('pre', { class: 'content' }, formatArguments(args)),
  );
}

function rawCard(label, value) {
  return el(
    'details',
    { class: 'event' },
    el(
      'summary',
      {},
      el('span', { class: 'evname' }, label || 'item'),
      el('span', { class: 'evpreview' }, previewValue(value)),
    ),
    el('pre', { class: 'content' }, prettyJSON(value)),
  );
}

function errorCard(error) {
  const message =
    typeof error === 'string'
      ? error
      : error && typeof error === 'object'
        ? error.message || prettyJSON(error)
        : String(error);
  return el('div', { class: 'banner' }, message);
}

function chip(label, value, tone) {
  return el(
    'span',
    { class: 'chip' + (tone ? ' ' + tone : '') },
    label,
    value !== '' ? el('b', {}, value) : null,
  );
}

function rawBody(record, which) {
  const body = which === 'request' ? record.request_body : record.response_body;
  if (!body || (!body.json && !body.text)) {
    let message = which === 'request' ? 'No request body captured.' : 'No response body captured.';
    if (which === 'response' && record.stream) message = 'Streamed response; open the Events tab.';
    return el('div', { class: 'empty-tab' }, message);
  }
  return el('pre', { class: 'content' }, body.json ? prettyJSON(body.json) : body.text);
}

/* ------------------------------------------------------------------ empty */

function renderEmpty(host, error) {
  const base = state.config ? publicBase(state.config.listen) : 'http://127.0.0.1:8787';
  const upstream = state.config ? state.config.upstream : '';
  const hasRequests = state.summaries.length > 0;
  const snippet = [
    '# ~/.config/cake/settings.toml',
    '[[models]]',
    'name = "proxied"',
    'model = "<provider-model-id>"',
    `base_url = "${base}/v1"`,
    'api_key_env = "CAKE_PROXY_TOKEN"   # any non-empty value',
    'api_type = "chat_completions"     # or "responses"',
  ].join('\n');

  const nodes = [
    el(
      'h1',
      {},
      error ? 'Exchange unavailable' : hasRequests ? 'Select a request' : 'Waiting for requests',
    ),
    el(
      'p',
      {},
      error
        ? error
        : hasRequests
          ? 'Requests appear in the list on the left. The newest is at the top.'
          : 'Point cake at this proxy and each request will appear in the list on the left.',
    ),
  ];
  if (!error && !hasRequests) {
    nodes.push(el('pre', {}, snippet));
    if (upstream) nodes.push(el('p', { class: 'muted' }, 'Upstream: ' + upstream));
  }
  host.replaceChildren(el('div', { class: 'empty' }, ...nodes));
}

/* ------------------------------------------------------------------ helpers */

function apiLabel(api) {
  if (api === 'chat_completions') return 'chat';
  if (api === 'responses') return 'responses';
  return 'other';
}

function roleClass(role) {
  const known = new Set(['system', 'developer', 'user', 'assistant', 'tool', 'function']);
  if (role === 'reasoning') return 'system';
  return known.has(role) ? role : 'other';
}

function imageLabel(part) {
  const url = part.image_url && part.image_url.url ? part.image_url.url : '';
  if (url.startsWith('data:')) return 'inline';
  if (url) {
    try {
      return new URL(url).host;
    } catch {
      return 'url';
    }
  }
  return '';
}

function formatArguments(args) {
  if (args === null || args === undefined || args === '') return '(no arguments)';
  if (typeof args !== 'string') return prettyJSON(args);
  try {
    return prettyJSON(JSON.parse(args));
  } catch {
    return args;
  }
}

function prettyJSON(value) {
  if (value === undefined) return '';
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function previewValue(value) {
  const text = typeof value === 'string' ? value : prettyJSON(value);
  const flat = text.replace(/\s+/g, ' ').trim();
  return flat.length > 110 ? flat.slice(0, 110) + '…' : flat;
}

function formatCount(value) {
  const number = Number(value) || 0;
  if (number < 1000) return String(number);
  if (number < 1000000) return (number / 1000).toFixed(number < 10000 ? 2 : 1) + 'k';
  return (number / 1000000).toFixed(2) + 'M';
}

function formatDuration(ms) {
  if (ms === undefined || ms === null || ms === 0) return '';
  if (ms < 1000) return ms + 'ms';
  return (ms / 1000).toFixed(ms < 10000 ? 2 : 1) + 's';
}

function formatTime(iso) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleTimeString([], { hour12: false });
}

function formatDateTime(iso) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleString([], { hour12: false });
}

function publicBase(listen) {
  if (!listen) return 'http://127.0.0.1:8787';
  const index = listen.lastIndexOf(':');
  if (index < 0) return 'http://' + listen;
  const host = listen.slice(0, index).replace(/^\[|\]$/g, '');
  if (!host || host === '0.0.0.0' || host === '::') {
    return 'http://127.0.0.1' + listen.slice(index);
  }
  return 'http://' + listen;
}

function idFromHash() {
  const match = location.hash.match(/^#\/e\/([A-Za-z0-9._-]+)$/);
  return match ? match[1] : null;
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const area = document.createElement('textarea');
    area.value = text;
    area.style.position = 'fixed';
    area.style.opacity = '0';
    document.body.append(area);
    area.select();
    const ok = document.execCommand('copy');
    area.remove();
    return ok;
  }
}

async function fetchJSON(url, options) {
  const response = await fetch(url, {
    cache: 'no-store',
    headers: { Accept: 'application/json' },
    ...options,
  });
  if (!response.ok) {
    let message = response.statusText || 'request failed';
    try {
      const body = await response.json();
      if (body && body.error) message = body.error;
    } catch {
      /* keep the status text */
    }
    throw new Error(message);
  }
  if (response.status === 204) return null;
  return response.json();
}

boot();
