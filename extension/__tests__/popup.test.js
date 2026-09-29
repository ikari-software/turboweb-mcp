import { describe, it, expect, vi, beforeAll, beforeEach } from 'vitest';
import fs from 'fs';
import path from 'path';
import vm from 'vm';

let api;
let mockPort;

beforeAll(() => {
  // Set up popup.html DOM
  const html = fs.readFileSync(path.resolve(__dirname, '../popup.html'), 'utf8');
  // Extract body content (between <body> and </body>, excluding the script tag)
  const bodyMatch = html.match(/<body[^>]*>([\s\S]*)<script/);
  if (bodyMatch) {
    document.body.innerHTML = bodyMatch[1];
  }

  // Set up chrome.runtime.connect mock to return a controllable port
  mockPort = {
    onMessage: __makeEvent(),
    onDisconnect: __makeEvent(),
    postMessage: vi.fn(),
    name: 'popup',
  };
  chrome.runtime.connect.mockReturnValue(mockPort);

  // Use fake timers to prevent setInterval from running
  vi.useFakeTimers();

  // Load popup.js — inject export line at the end
  const filePath = path.resolve(__dirname, '../popup.js');
  let code = fs.readFileSync(filePath, 'utf8');
  const fns = [
    'renderStatus', 'renderActivity', 'renderStats',
    'renderClients', 'rerenderLog', 'entries',
    'durClass', 'durText', 'formatUptime',
  ].join(', ');

  code += `\nglobalThis.__popupAPI = { ${fns} };`;
  const script = new vm.Script(code, { filename: filePath });
  script.runInThisContext();
  api = globalThis.__popupAPI;

  vi.useRealTimers();
});

beforeEach(() => {
  // Reset DOM to initial state for each test
  const log = document.getElementById('log');
  if (log) {
    log.innerHTML = '<div class="empty" id="empty-msg">Waiting for commands...</div>';
  }
  // The popup's activity-log map is module-scoped; clear it so each test
  // starts with a known empty state.
  if (api?.entries?.clear) api.entries.clear();
  // Reset stat values
  const cmds = document.getElementById('s-cmds');
  if (cmds) cmds.textContent = '0';
  const errs = document.getElementById('s-errs');
  if (errs) { errs.textContent = '0'; errs.className = 'val'; }
  const avg = document.getElementById('s-avg');
  if (avg) avg.textContent = '-';
  const up = document.getElementById('s-up');
  if (up) up.textContent = '0s';
  // Reset status
  const dot = document.getElementById('dot');
  if (dot) dot.className = 'dot';
  const label = document.getElementById('status-label');
  if (label) label.textContent = 'Disconnected';
});

// ============================================================
// durClass()
// ============================================================
describe('durClass', () => {
  it('returns "err" for error entries', () => {
    expect(api.durClass({ error: 'fail' })).toBe('err');
  });

  it('returns "fast" for < 200ms', () => {
    expect(api.durClass({ duration: 100 })).toBe('fast');
    expect(api.durClass({ duration: 0 })).toBe('fast');
  });

  it('returns "slow" for > 2000ms', () => {
    expect(api.durClass({ duration: 3000 })).toBe('slow');
  });

  it('returns empty string for mid-range', () => {
    expect(api.durClass({ duration: 500 })).toBe('');
    expect(api.durClass({ duration: 2000 })).toBe('');
  });
});

// ============================================================
// durText()
// ============================================================
describe('durText', () => {
  it('returns "ERR" for error entries', () => {
    expect(api.durText({ error: 'fail' })).toBe('ERR');
  });

  it('returns ms for < 1s', () => {
    expect(api.durText({ duration: 150 })).toBe('150ms');
    expect(api.durText({ duration: 0 })).toBe('0ms');
  });

  it('returns seconds for >= 1s', () => {
    expect(api.durText({ duration: 2500 })).toBe('2.5s');
    expect(api.durText({ duration: 1000 })).toBe('1.0s');
  });

  it('returns "-" when no duration', () => {
    expect(api.durText({})).toBe('-');
  });
});

// ============================================================
// formatUptime()
// ============================================================
describe('formatUptime', () => {
  it('formats seconds', () => {
    expect(api.formatUptime(5000)).toBe('5s');
    expect(api.formatUptime(0)).toBe('0s');
  });

  it('formats minutes', () => {
    expect(api.formatUptime(120000)).toBe('2m');
    expect(api.formatUptime(90000)).toBe('1m');
  });

  it('formats hours and minutes', () => {
    expect(api.formatUptime(3660000)).toBe('1h1m');
    expect(api.formatUptime(7200000)).toBe('2h0m');
  });
});

// ============================================================
// renderStatus()
// ============================================================
describe('renderStatus', () => {
  it('shows connected state', () => {
    api.renderStatus({ connected: true, browsers: 'Chrome' });
    const dot = document.getElementById('dot');
    const label = document.getElementById('status-label');
    expect(dot.className).toContain('on');
    expect(dot.className).toContain('pulse');
    expect(label.textContent).toBe('Connected');
  });

  it('shows disconnected state', () => {
    api.renderStatus({ connected: false });
    const dot = document.getElementById('dot');
    const label = document.getElementById('status-label');
    expect(dot.className).toBe('dot');
    expect(label.textContent).toBe('Disconnected');
  });

  it('shows daemon endpoint when connected', () => {
    api.renderStatus({ connected: true });
    const browsers = document.getElementById('browsers');
    expect(browsers.textContent).toContain('ws://127.0.0.1:18321');
  });

  it('shows daemon-offline message when disconnected', () => {
    api.renderStatus({ connected: false });
    const browsers = document.getElementById('browsers');
    expect(browsers.textContent).toMatch(/offline|retrying/i);
  });
});

// ============================================================
// renderActivity()
// ============================================================
describe('renderActivity', () => {
  it('adds entry to log', () => {
    api.renderActivity({ action: 'click', status: 'done', duration: 50, timestamp: Date.now() });
    const log = document.getElementById('log');
    const entries = log.querySelectorAll('.entry');
    expect(entries.length).toBe(1);
    expect(entries[0].querySelector('.action').textContent).toBe('click');
  });

  it('removes empty message', () => {
    expect(document.getElementById('empty-msg')).toBeTruthy();
    api.renderActivity({ action: 'test', status: 'done', duration: 10, timestamp: Date.now() });
    expect(document.getElementById('empty-msg')).toBeNull();
  });

  it('shows spinner for start status', () => {
    api.renderActivity({ id: 'r1', action: 'screenshot', status: 'start', timestamp: Date.now() });
    const entry = document.getElementById('entry-r1');
    expect(entry).toBeTruthy();
    expect(entry.querySelector('.spinner')).toBeTruthy();
    expect(entry.className).toContain('running');
  });

  it('updates existing entry on completion', () => {
    api.renderActivity({ id: 'r2', action: 'click', status: 'start', timestamp: Date.now() });
    api.renderActivity({ id: 'r2', action: 'click', status: 'done', duration: 42, timestamp: Date.now() });
    const entry = document.getElementById('entry-r2');
    expect(entry.querySelector('.spinner')).toBeNull();
    expect(entry.querySelector('.dur').textContent).toBe('42ms');
    expect(entry.className).not.toContain('running');
  });

  it('marks error entries', () => {
    api.renderActivity({ id: 'r3', action: 'fail', status: 'start', timestamp: Date.now() });
    api.renderActivity({ id: 'r3', action: 'fail', status: 'error', error: 'boom', duration: 100, timestamp: Date.now() });
    const entry = document.getElementById('entry-r3');
    const action = entry.querySelector('.action');
    expect(action.className).toContain('err');
  });

  it('caps log at 100 entries', () => {
    const log = document.getElementById('log');
    log.innerHTML = '';
    for (let i = 0; i < 105; i++) {
      api.renderActivity({ action: 'x', status: 'done', duration: 1, timestamp: Date.now() });
    }
    expect(log.children.length).toBeLessThanOrEqual(100);
  });
});

// ============================================================
// renderStats()
// ============================================================
describe('renderStats', () => {
  it('updates all stat elements', () => {
    api.renderStats({ commands: 42, errors: 3, avgMs: 150, uptimeMs: 120000 });
    expect(document.getElementById('s-cmds').textContent).toBe('42');
    expect(document.getElementById('s-errs').textContent).toBe('3');
    expect(document.getElementById('s-errs').className).toContain('warn');
    expect(document.getElementById('s-avg').textContent).toBe('150');
    expect(document.getElementById('s-up').textContent).toBe('2m');
  });

  it('shows zero errors without warn class', () => {
    api.renderStats({ commands: 10, errors: 0, avgMs: 50, uptimeMs: 5000 });
    expect(document.getElementById('s-errs').className).toBe('val');
  });

  it('shows dash for zero avgMs', () => {
    api.renderStats({ commands: 0, errors: 0, avgMs: 0, uptimeMs: 1000 });
    expect(document.getElementById('s-avg').textContent).toBe('-');
  });

  it('handles missing values', () => {
    api.renderStats({});
    expect(document.getElementById('s-cmds').textContent).toBe('0');
    expect(document.getElementById('s-avg').textContent).toBe('-');
    expect(document.getElementById('s-up').textContent).toBe('0s');
  });
});

// ============================================================
// Port message routing
// ============================================================
describe('port message routing', () => {
  it('connects with name "popup"', () => {
    expect(chrome.runtime.connect).toHaveBeenCalledWith({ name: 'popup' });
  });

  it('requests initial state', () => {
    expect(mockPort.postMessage).toHaveBeenCalledWith({ type: 'getState' });
  });

  it('routes status messages to renderStatus', () => {
    mockPort.onMessage._fire({ type: 'status', connected: true, browsers: 'Test' });
    expect(document.getElementById('dot').className).toContain('on');
  });

  it('routes stats messages to renderStats', () => {
    mockPort.onMessage._fire({ type: 'stats', commands: 5, errors: 0, avgMs: 10, uptimeMs: 5000 });
    expect(document.getElementById('s-cmds').textContent).toBe('5');
  });

  it('routes activity messages to renderActivity', () => {
    mockPort.onMessage._fire({ type: 'activity', action: 'scroll', status: 'done', duration: 20, timestamp: Date.now() });
    const log = document.getElementById('log');
    expect(log.querySelector('.action').textContent).toBe('scroll');
  });

  it('handles log-batch messages', () => {
    const log = document.getElementById('log');
    log.innerHTML = '';
    mockPort.onMessage._fire({
      type: 'log-batch',
      entries: [
        { action: 'a', status: 'done', duration: 10, timestamp: Date.now() },
        { action: 'b', status: 'done', duration: 20, timestamp: Date.now() },
      ],
    });
    expect(log.querySelectorAll('.entry').length).toBe(2);
  });
});

describe('renderClients', () => {
  const rows = () => [...document.querySelectorAll('#clients .client')];

  it('shows self-introduced name, project, host chip and model/task', () => {
    api.renderClients([{
      label: 'Claude · turboweb-mcp', displayName: 'Claude', project: 'turboweb-mcp',
      sessionType: 'claude-code', hostName: 'Claude Code',
      model: 'claude-opus-4', task: 'Testing labels', hue: 40, connectedAt: Date.now(),
    }]);
    const [row] = rows();
    expect(row.querySelector('.agent-name').textContent).toBe('Claude · turboweb-mcp');
    expect(row.querySelector('.agent-tag').textContent).toBe('Claude Code');
    expect(row.querySelector('.agent-tag').classList.contains('claude-code')).toBe(true);
    expect(row.querySelector('.agent-sub').textContent).toBe('claude-opus-4 — Testing labels');
  });

  it('omits a host chip that would repeat the name', () => {
    api.renderClients([{
      label: 'Cursor · app', displayName: 'Cursor', project: 'app',
      sessionType: 'cursor', hostName: 'Cursor',
    }]);
    const [row] = rows();
    expect(row.querySelector('.agent-name').textContent).toBe('Cursor · app');
    expect(row.querySelector('.agent-tag')).toBeNull();
    expect(row.querySelector('.agent-sub')).toBeNull();
  });

  it('still renders legacy labels from older servers', () => {
    api.renderClients([{ label: 'claude-code/gem-knight#61157', sessionType: 'claude-code' }]);
    const [row] = rows();
    expect(row.querySelector('.agent-name').textContent).toBe('claude-code/gem-knight#61157');
    expect(row.querySelector('.agent-tag').textContent).toBe('claude-code');
  });
});

describe('activity client chip', () => {
  it('prefers clientName over label parsing', () => {
    api.renderActivity({ id: 'n1', action: 'click', status: 'start', clientName: 'Codex', clientLabel: 'Codex · proj' });
    expect(document.querySelector('#entry-n1 .client-chip').textContent).toBe('Codex');
  });
  it('parses new-style labels without clientName', () => {
    api.renderActivity({ id: 'n2', action: 'click', status: 'start', clientLabel: 'jcode · proj' });
    expect(document.querySelector('#entry-n2 .client-chip').textContent).toBe('jcode');
  });
});
