// Popup — connects to background for live status updates.
// Renders connected agents, an expandable activity log with intent
// narration, and roll-up stats. Each agent that's actively talking to the
// MCP daemon is listed by session label so the human user knows who's
// driving the browser at any moment.

const port = chrome.runtime.connect({ name: 'popup' });

// In-memory store of activity entries keyed by command id; the popup is
// re-renderable from this map without losing detail when filters change.
const entries = new Map();
// Cap to keep long-running pop-out windows from accumulating thousands of
// entries (each ~1 KB). When breached we evict the oldest insertion-order
// entries; Map iteration order is insertion order so this is O(n) once and
// then bounded forever after.
const MAX_ENTRIES = 500;
let filterText = '';

port.onMessage.addListener((msg) => {
  switch (msg.type) {
    case 'status':       renderStatus(msg); break;
    case 'activity':     renderActivity(msg); break;
    case 'stats':        renderStats(msg); break;
    case 'mcp_clients':  renderClients(msg.clients || []); break;
    case 'log-batch':    msg.entries.forEach(e => renderActivity(e)); break;
  }
});

// Show the version straight from the manifest so the header never drifts
// from the actual build — manifest.json is the single source the build
// derives every artifact from.
document.getElementById('ver').textContent = 'MCP v' + chrome.runtime.getManifest().version;

// Request current state
port.postMessage({ type: 'getState' });

document.getElementById('filter').addEventListener('input', (e) => {
  filterText = e.target.value.trim().toLowerCase();
  rerenderLog();
});
document.getElementById('clear-log').addEventListener('click', () => {
  entries.clear();
  rerenderLog();
});

// Pop the popup out into a resizable chrome.windows.create window. Chrome's
// inline popup is capped at ~800x600 and unresizable; the windowed copy
// renders the same UI but can be sized however the user wants and stays
// open while they work. We pass ?windowed=1 so popup.js knows to apply
// the full-height flex layout.
const isWindowed = new URLSearchParams(location.search).get('windowed') === '1';
if (isWindowed) {
  document.body.classList.add('windowed');
  // Hide the pop-out button when already windowed.
  const popoutBtn = document.getElementById('popout');
  if (popoutBtn) popoutBtn.style.display = 'none';
} else {
  const popoutBtn = document.getElementById('popout');
  if (popoutBtn) {
    popoutBtn.addEventListener('click', () => {
      chrome.windows.create({
        url: chrome.runtime.getURL('popup.html?windowed=1'),
        type: 'popup',
        width: 720,
        height: 820,
      });
      window.close();
    });
  }
}

// --- Render functions ---

function renderStatus(data) {
  const dot = document.getElementById('dot');
  const label = document.getElementById('status-label');
  const browsers = document.getElementById('browsers');

  // "Connected"/"Disconnected" describes the WS between this extension
  // and the local daemon (ws://127.0.0.1:18321) — not whether any
  // browser is connected. The previous "No browsers detected" copy was
  // misleading: it appeared *inside the browser*, sounding like the
  // daemon couldn't see us, when really the extension just hadn't
  // reached the daemon yet.
  dot.className = data.connected ? 'dot on pulse' : 'dot';
  label.textContent = data.connected ? 'Connected' : 'Disconnected';
  browsers.textContent = data.connected
    ? 'Linked to daemon · ws://127.0.0.1:18321'
    : 'Daemon offline — retrying…';
}

function renderClients(clients) {
  const el = document.getElementById('clients');
  const empty = document.getElementById('clients-empty');
  const count = document.getElementById('clients-count');
  count.textContent = clients.length;

  // Always render fresh — the list is small and updates rarely.
  el.innerHTML = '';
  if (!clients.length) {
    if (empty) el.appendChild(empty);
    else {
      const e = document.createElement('div');
      e.className = 'clients-empty';
      e.id = 'clients-empty';
      e.textContent = 'No agents connected';
      el.appendChild(e);
    }
    return;
  }

  for (const c of clients) {
    const row = document.createElement('div');
    row.className = 'client';
    // Per-agent hue: the daemon assigns each connected agent a stable
    // hue (brand 40° + 45° per connection-order index). Setting it on
    // the row lets CSS tint the icon and name in lockstep.
    if (typeof c.hue === 'number') {
      row.style.setProperty('--client-hue', String(c.hue));
    }

    const icon = document.createElement('span');
    icon.className = 'agent-icon';
    icon.innerHTML = agentIcon(c.sessionType);
    row.appendChild(icon);

    const name = document.createElement('span');
    name.className = 'agent-name';
    name.textContent = c.label || 'unknown';
    row.appendChild(name);

    if (c.sessionType) {
      const tag = document.createElement('span');
      tag.className = 'agent-tag ' + c.sessionType;
      tag.textContent = c.sessionType;
      row.appendChild(tag);
    }

    const meta = document.createElement('span');
    meta.className = 'agent-meta';
    meta.textContent = c.connectedAt ? formatSince(c.connectedAt) : '';
    row.appendChild(meta);

    el.appendChild(row);
  }
}

function agentIcon(_type) {
  // Robot head silhouette. Uses currentColor so the inline `--client-hue`
  // set on the parent .client row tints it per agent. sessionType-keyed
  // colours used to live here but are now subsumed by the hue scheme —
  // the .agent-tag chip still flags the editor type explicitly.
  return `<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" xmlns="http://www.w3.org/2000/svg">
    <rect x="3" y="5" width="10" height="8" rx="2"/>
    <line x1="8" y1="5" x2="8" y2="3"/>
    <circle cx="8" cy="2.5" r="0.7" fill="currentColor"/>
    <circle cx="6" cy="9" r="0.9" fill="currentColor"/>
    <circle cx="10" cy="9" r="0.9" fill="currentColor"/>
  </svg>`;
}

function renderActivity(data) {
  if (!data.id) {
    // No id — generate a synthetic one so it can still be tracked.
    data.id = 'sync-' + Date.now() + '-' + Math.random().toString(36).slice(2, 6);
  }
  const existing = entries.get(data.id) || {};
  const merged = { ...existing, ...data };
  entries.set(data.id, merged);
  // Evict oldest entries past the cap. Map keeps insertion order so the
  // first key in the iterator is the oldest.
  while (entries.size > MAX_ENTRIES) {
    const oldest = entries.keys().next().value;
    if (oldest === undefined) break;
    entries.delete(oldest);
  }
  rerenderLog();
}

function rerenderLog() {
  const log = document.getElementById('log');
  if (!log) return;

  // Skip the rebuild when the popup is hidden — no point spending CPU on
  // a window the user isn't looking at. A visibilitychange listener
  // (registered once below) catches up on the next reveal so the user
  // sees current state when they refocus.
  if (typeof document !== 'undefined' && document.hidden) {
    pendingRerender = true;
    return;
  }
  pendingRerender = false;

  const list = [...entries.values()];
  // newest first
  list.sort((a, b) => (b.timestamp || 0) - (a.timestamp || 0));

  const filtered = filterText
    ? list.filter(e => entryMatchesFilter(e, filterText))
    : list;

  if (!filtered.length) {
    const msg = document.createElement('div');
    msg.className = 'empty';
    msg.id = 'empty-msg';
    msg.textContent = filterText ? 'No matches' : 'Waiting for commands...';
    // One DOM mutation instead of innerHTML='' + appendChild.
    log.replaceChildren(msg);
    return;
  }

  // Build all rows in memory, then commit with a single replaceChildren —
  // one reflow per re-render instead of one per row, which matters during
  // chained agent loops where dozens of commands can fire in a second.
  const rows = filtered.slice(0, 100).map(buildEntryRow);
  log.replaceChildren(...rows);
}

// Set when a rerender was skipped because the document was hidden. The
// visibilitychange listener below flushes it on reveal.
let pendingRerender = false;
if (typeof document !== 'undefined' && document.addEventListener) {
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && pendingRerender) rerenderLog();
  });
}

function entryMatchesFilter(e, q) {
  if (!q) return true;
  return [e.action, e.intent, e.clientLabel, e.clientType, e.error]
    .filter(Boolean).some(s => s.toLowerCase().includes(q));
}

function buildEntryRow(data) {
  const entry = document.createElement('div');
  entry.className = 'entry' + (data.status === 'start' ? ' running' : '');
  if (data.id) entry.id = 'entry-' + data.id;
  // Per-action hue so the client-chip and any chip-derived accent inside
  // this row track the agent that issued the action.
  if (typeof data.clientHue === 'number') {
    entry.style.setProperty('--client-hue', String(data.clientHue));
  }

  const time = new Date(data.timestamp || Date.now()).toLocaleTimeString('en-GB', { hour12: false });
  const errCls = data.status === 'error' || data.error ? ' err' : '';
  const intent = data.intent || data.error || data.resultSummary || '';
  const intentMuted = !data.intent ? ' muted' : '';
  const clientLabel = data.clientLabel ? data.clientLabel.split('/').pop() : '';

  const row = document.createElement('div');
  row.className = 'entry-row';
  row.innerHTML = `
    <span class="time">${time}</span>
    <span class="action${errCls}">${escapeHtml(data.action || '?')}</span>
    <span class="intent${intentMuted}">${escapeHtml(intent || '(no intent)')}</span>
    ${clientLabel ? `<span class="client-chip" title="${escapeHtml(data.clientLabel)}">${escapeHtml(clientLabel)}</span>` : ''}
    ${data.status === 'start'
      ? '<div class="spinner"></div>'
      : `<span class="dur ${durClass(data)}">${durText(data)}</span>`}
  `;

  const detail = document.createElement('div');
  detail.className = 'entry-detail';
  detail.appendChild(buildDetailFor(data));

  entry.appendChild(row);
  entry.appendChild(detail);

  entry.addEventListener('click', () => {
    entry.classList.toggle('expanded');
  });

  return entry;
}

function buildDetailFor(data) {
  const wrap = document.createDocumentFragment();
  if (data.intent) {
    wrap.appendChild(detailRow('Intent', data.intent));
  }
  if (data.clientLabel) {
    wrap.appendChild(detailRow('Agent', data.clientLabel + (data.clientType ? ' (' + data.clientType + ')' : '')));
  }
  if (data.params && Object.keys(data.params).length) {
    wrap.appendChild(detailRow('Params', prettyJSON(data.params)));
  }
  if (data.resultSummary) {
    wrap.appendChild(detailRow('Result', data.resultSummary));
  }
  if (data.error) {
    wrap.appendChild(detailRow('Error', data.error));
  }
  return wrap;
}

function detailRow(label, value) {
  const row = document.createElement('div');
  row.className = 'row';
  const k = document.createElement('div');
  k.className = 'key';
  k.textContent = label;
  row.appendChild(k);
  if (typeof value === 'object') {
    const pre = document.createElement('pre');
    pre.textContent = prettyJSON(value);
    row.appendChild(pre);
  } else if (value && value.length > 60) {
    const pre = document.createElement('pre');
    pre.textContent = String(value);
    row.appendChild(pre);
  } else {
    const v = document.createElement('div');
    v.textContent = String(value);
    row.appendChild(v);
  }
  return row;
}

function prettyJSON(v) {
  try { return JSON.stringify(v, null, 2); } catch { return String(v); }
}

function escapeHtml(s) {
  return String(s ?? '')
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

function durClass(data) {
  if (data.error) return 'err';
  if (data.duration < 200) return 'fast';
  if (data.duration > 2000) return 'slow';
  return '';
}

function durText(data) {
  if (data.error) return 'ERR';
  if (!data.duration && data.duration !== 0) return '-';
  if (data.duration < 1000) return data.duration + 'ms';
  return (data.duration / 1000).toFixed(1) + 's';
}

function formatSince(ts) {
  const ms = Date.now() - ts;
  if (ms < 60_000) return Math.round(ms / 1000) + 's';
  if (ms < 3_600_000) return Math.round(ms / 60_000) + 'm';
  return Math.round(ms / 3_600_000) + 'h';
}

function renderStats(data) {
  document.getElementById('s-cmds').textContent = data.commands || 0;
  const errs = document.getElementById('s-errs');
  errs.textContent = data.errors || 0;
  errs.className = data.errors > 0 ? 'val warn' : 'val';
  document.getElementById('s-avg').textContent = data.avgMs ? Math.round(data.avgMs) : '-';
  document.getElementById('s-up').textContent = formatUptime(data.uptimeMs || 0);
}

function formatUptime(ms) {
  const s = Math.floor(ms / 1000);
  if (s < 60) return s + 's';
  const m = Math.floor(s / 60);
  if (m < 60) return m + 'm';
  const h = Math.floor(m / 60);
  return h + 'h' + (m % 60) + 'm';
}

// Refresh stats every second (for uptime counter). Clean up when the SW
// recycles the port — otherwise the interval keeps firing into a dead
// channel and throws "Attempting to use a disconnected port" forever.
const statsTimer = setInterval(() => {
  try { port.postMessage({ type: 'getStats' }); }
  catch { clearInterval(statsTimer); }
}, 1000);
port.onDisconnect.addListener(() => clearInterval(statsTimer));
