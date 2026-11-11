import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import fs from 'fs';
import path from 'path';
import vm from 'vm';

let api;

// Fresh chrome mock state for each describe block's loading
function resetChromeMocks() {
  chrome.tabs.query.mockReset().mockResolvedValue([]);
  chrome.tabs.get.mockReset().mockResolvedValue({});
  chrome.tabs.update.mockReset().mockResolvedValue({});
  chrome.tabs.sendMessage.mockReset();
  chrome.tabs.captureVisibleTab.mockReset().mockResolvedValue('data:image/jpeg;base64,AAAA');
  chrome.scripting.executeScript.mockReset().mockResolvedValue([{ result: {} }]);
  chrome.windows.update.mockReset().mockResolvedValue({});
  chrome.debugger.attach.mockReset().mockResolvedValue(undefined);
  chrome.debugger.sendCommand.mockReset();
  chrome.alarms.create.mockReset();
  chrome.alarms.clear.mockReset();
  chrome.action.setBadgeText.mockReset();
  chrome.action.setBadgeBackgroundColor.mockReset();
  chrome.runtime._lastError = null;
}

beforeAll(() => {
  // Mock fetch globally
  globalThis.fetch = vi.fn();

  // Mock OffscreenCanvas and createImageBitmap (used by resizeLocal)
  globalThis.OffscreenCanvas = class {
    constructor(w, h) { this.width = w; this.height = h; }
    getContext() {
      return { drawImage: vi.fn() };
    }
    convertToBlob() {
      return Promise.resolve(new Blob(['fake'], { type: 'image/jpeg' }));
    }
  };
  globalThis.createImageBitmap = vi.fn().mockResolvedValue({
    width: 2560, height: 1600, close: vi.fn(),
  });

  // Load background.js — inject export line at the end
  const filePath = path.resolve(__dirname, '../background.js');
  let code = fs.readFileSync(filePath, 'utf8');
  const fns = [
    'getStats', 'broadcast', 'logActivity', 'updateBadge',
    'connect', 'scheduleReconnect', 'startKeepalive', 'stopKeepalive',
    'pingWS', 'handlePong', 'forceReconnect',
    'resolveTab', 'ensureContentScript', 'toContent', 'resolveFrameId',
    'ensureDebugger', 'cdpSend', 'cdpClick', 'cdpType', 'cdpKey', 'cdpScroll', 'humanGapMs', 'DEFAULT_TYPE_WPM',
    'setInputFiles', 'interceptFileChooser',
    'checkNative', 'resizeNative', 'resizeLocal', 'screenshot',
    'executeJsMain', 'adaptScript', 'dispatch', 'explainError', 'getSessionId',
    'PAGE_ACTIONS_THAT_GATE_ON_CURSOR',
  ].join(', ');

  code += `\nglobalThis.__bgAPI = { ${fns} };`;
  const script = new vm.Script(code, { filename: filePath });
  script.runInThisContext();
  api = globalThis.__bgAPI;
});

beforeEach(() => {
  resetChromeMocks();
});

// ============================================================
// getStats()
// ============================================================
describe('getStats', () => {
  it('returns stats with avgMs=0 when no commands', () => {
    const s = api.getStats();
    expect(s).toHaveProperty('commands');
    expect(s).toHaveProperty('errors');
    expect(s).toHaveProperty('avgMs');
    expect(s).toHaveProperty('uptimeMs');
    expect(s.uptimeMs).toBeGreaterThan(0);
  });
});

// ============================================================
// logActivity()
// ============================================================
describe('logActivity', () => {
  it('adds entries and broadcasts', () => {
    // logActivity calls broadcast internally — just verify no throw
    api.logActivity({ action: 'test', status: 'done', duration: 10 });
  });
});

// ============================================================
// updateBadge()
// ============================================================
describe('updateBadge', () => {
  it('sets ON badge when connected', () => {
    api.updateBadge(true);
    expect(chrome.action.setBadgeText).toHaveBeenCalledWith({ text: 'ON' });
    expect(chrome.action.setBadgeBackgroundColor).toHaveBeenCalledWith({ color: '#3fb950' });
  });

  it('clears badge when disconnected', () => {
    api.updateBadge(false);
    expect(chrome.action.setBadgeText).toHaveBeenCalledWith({ text: '' });
    expect(chrome.action.setBadgeBackgroundColor).toHaveBeenCalledWith({ color: '#f85149' });
  });
});

// ============================================================
// resolveTab()
// ============================================================
describe('resolveTab', () => {
  it('returns provided tabId directly', async () => {
    const id = await api.resolveTab(42);
    expect(id).toBe(42);
  });

  it('queries active tab when no tabId', async () => {
    chrome.tabs.query.mockResolvedValue([{ id: 7 }]);
    const id = await api.resolveTab();
    expect(chrome.tabs.query).toHaveBeenCalledWith({ active: true, lastFocusedWindow: true });
    expect(id).toBe(7);
  });

  it('throws when no active tab found', async () => {
    chrome.tabs.query.mockResolvedValue([]);
    await expect(api.resolveTab()).rejects.toThrow('No active tab');
  });
});

// ============================================================
// ensureContentScript()
// ============================================================
describe('ensureContentScript', () => {
  it('skips injection when ping succeeds', async () => {
    chrome.tabs.sendMessage.mockImplementation((_id, _msg, _opts, cb) => {
      if (cb) cb({ ok: true });
    });
    await api.ensureContentScript(1);
    expect(chrome.scripting.executeScript).not.toHaveBeenCalled();
  });

  it('injects content.js when ping fails', async () => {
    chrome.tabs.sendMessage.mockImplementation(() => { throw new Error('no receiver'); });
    chrome.scripting.executeScript.mockResolvedValue([]);
    await api.ensureContentScript(1);
    expect(chrome.scripting.executeScript).toHaveBeenCalledWith({
      target: { tabId: 1, allFrames: true },
      files: ['content.js'],
    });
  });
});

// ============================================================
// toContent()
// ============================================================
describe('toContent', () => {
  it('resolves tab, ensures script, sends message', async () => {
    // Ping succeeds (ensureContentScript)
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (cb) cb({ blocks: [] });
    });

    const result = await api.toContent(5, 'extract_text', { max: 10 });
    expect(result).toEqual({ blocks: [] });
  });

  it('rejects when chrome.runtime.lastError is set', async () => {
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      chrome.runtime._lastError = { message: 'tab closed' };
      if (cb) cb(undefined);
      chrome.runtime._lastError = null;
    });

    await expect(api.toContent(5, 'extract_text')).rejects.toThrow('tab closed');
  });

  it('rejects when response has error', async () => {
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (cb) cb({ error: 'not found' });
    });

    await expect(api.toContent(5, 'click', { selector: '#x' })).rejects.toThrow('not found');
  });
});

// ============================================================
// Cross-origin frame routing (all_frames + frameId handshake)
// ============================================================
describe('cross-origin frame routing', () => {
  it('resolveFrameId returns the top frame for an empty path', async () => {
    const r = await api.resolveFrameId(1, '');
    expect(r).toEqual({ frameId: 0, offset: { x: 0, y: 0 } });
  });

  it('resolveFrameId walks one hop: locate-probe + frameId ack, captures the child origin', async () => {
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (msg.action === '__locate_child_frame') {
        // Parent found the iframe and posted the nonce; simulate the child
        // acking from its (trusted) frameId 7.
        chrome.runtime.onMessage._fire({ action: '__frame_probe_ack', nonce: msg.params.nonce }, { frameId: 7 });
        if (cb) cb({ ok: true, origin: { x: 100, y: 50 } });
        return;
      }
      if (cb) cb({});
    });
    const r = await api.resolveFrameId(1, '#child');
    expect(r).toEqual({ frameId: 7, offset: { x: 100, y: 50 } });
  });

  it('resolveFrameId accumulates offset across two hops', async () => {
    const fid = { '#a': 3, '#b': 9 };
    const origin = { '#a': { x: 10, y: 20 }, '#b': { x: 5, y: 5 } };
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (msg.action === '__locate_child_frame') {
        const s = msg.params.selector;
        chrome.runtime.onMessage._fire({ action: '__frame_probe_ack', nonce: msg.params.nonce }, { frameId: fid[s] });
        if (cb) cb({ ok: true, origin: origin[s] });
        return;
      }
      if (cb) cb({});
    });
    const r = await api.resolveFrameId(1, '#a > #b');
    expect(r).toEqual({ frameId: 9, offset: { x: 15, y: 25 } });
  });

  it('resolveFrameId rejects when a hop never acks (timeout)', async () => {
    vi.useFakeTimers();
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (msg.action === '__locate_child_frame') { if (cb) cb({ ok: true, origin: { x: 0, y: 0 } }); return; } // no ack fired
      if (cb) cb({});
    });
    const p = api.resolveFrameId(1, '#ghost');
    const expectation = expect(p).rejects.toThrow(/did not answer the probe/);
    await vi.advanceTimersByTimeAsync(2100);
    await expectation;
    vi.useRealTimers();
  });

  it('toContent routes a frame-local action to the resolved frame, strips frame, injects offset', async () => {
    let actionSend = null;
    chrome.tabs.sendMessage.mockImplementation((id, msg, opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (msg.action === '__locate_child_frame') {
        chrome.runtime.onMessage._fire({ action: '__frame_probe_ack', nonce: msg.params.nonce }, { frameId: 4 });
        if (cb) cb({ ok: true, origin: { x: 30, y: 40 } });
        return;
      }
      actionSend = { msg, opts };
      if (cb) cb({ blocks: [] });
    });
    const result = await api.toContent(1, 'extract_text', { frame: '#child', max: 5 });
    expect(result).toEqual({ blocks: [] });
    expect(actionSend.opts).toEqual({ frameId: 4 });
    expect(actionSend.msg.params).toMatchObject({ max: 5, __frameOffset: { x: 30, y: 40 }, __framePath: '#child' });
    expect(actionSend.msg.params.frame).toBeUndefined();
  });

  it('toContent does NOT route non-frame-local actions — navigate_frame keeps frame + goes to top', async () => {
    let sent = null;
    chrome.tabs.sendMessage.mockImplementation((id, msg, opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      sent = { msg, opts };
      if (cb) cb({ navigated: true });
    });
    await api.toContent(1, 'navigate_frame', { frame: '#child', url: 'x' });
    expect(sent.opts).toEqual({ frameId: 0 });
    expect(sent.msg.params.frame).toBe('#child');
  });

  it('toContent sends unframed actions to the top frame', async () => {
    let sent = null;
    chrome.tabs.sendMessage.mockImplementation((id, msg, opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      sent = { msg, opts };
      if (cb) cb({ blocks: [] });
    });
    await api.toContent(1, 'extract_text', { max: 5 });
    expect(sent.opts).toEqual({ frameId: 0 });
  });
});

// ============================================================
// ensureDebugger()
// ============================================================
describe('ensureDebugger', () => {
  it('attaches debugger to tab', async () => {
    chrome.debugger.attach.mockResolvedValue(undefined);
    await api.ensureDebugger(10);
    expect(chrome.debugger.attach).toHaveBeenCalledWith({ tabId: 10 }, '1.3');
  });

  it('is idempotent — does not re-attach', async () => {
    // Use a fresh tabId not used in other tests
    chrome.debugger.attach.mockResolvedValue(undefined);
    await api.ensureDebugger(777);
    chrome.debugger.attach.mockClear();
    await api.ensureDebugger(777);
    // Second call should not attach again (first call adds to internal Set)
    expect(chrome.debugger.attach).not.toHaveBeenCalled();
  });

  it('handles "Already attached" gracefully', async () => {
    chrome.debugger.attach.mockRejectedValue(new Error('Already attached'));
    await api.ensureDebugger(99);
    // Should not throw — "Already attached" is expected
  });

  it('rethrows non-already-attached errors', async () => {
    chrome.debugger.attach.mockRejectedValue(new Error('Permission denied'));
    await expect(api.ensureDebugger(88)).rejects.toThrow('Permission denied');
  });
});

// ============================================================
// cdpSend()
// ============================================================
describe('cdpSend', () => {
  it('sends command via chrome.debugger.sendCommand', async () => {
    chrome.debugger.attach.mockResolvedValue(undefined);
    chrome.debugger.sendCommand.mockImplementation((_target, _method, _params, cb) => {
      cb({ result: 'ok' });
    });

    const result = await api.cdpSend(10, 'DOM.getDocument', {});
    expect(chrome.debugger.sendCommand).toHaveBeenCalledWith(
      { tabId: 10 }, 'DOM.getDocument', {}, expect.any(Function),
    );
    expect(result).toEqual({ result: 'ok' });
  });

  it('rejects on lastError', async () => {
    chrome.debugger.sendCommand.mockImplementation((_target, _method, _params, cb) => {
      chrome.runtime._lastError = { message: 'detached' };
      cb(undefined);
      chrome.runtime._lastError = null;
    });

    await expect(api.cdpSend(10, 'X', {})).rejects.toThrow('detached');
  });
});

// ============================================================
// cdpClick()
// ============================================================
describe('cdpClick', () => {
  it('sends mousePressed then mouseReleased', async () => {
    const calls = [];
    chrome.debugger.attach.mockResolvedValue(undefined);
    chrome.debugger.sendCommand.mockImplementation((_t, method, params, cb) => {
      calls.push({ method, type: params.type });
      cb();
    });

    await api.cdpClick(1, 100, 200);
    expect(calls[0]).toEqual({ method: 'Input.dispatchMouseEvent', type: 'mousePressed' });
    expect(calls[1]).toEqual({ method: 'Input.dispatchMouseEvent', type: 'mouseReleased' });
  });

  it('passes modifiers', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, params, cb) => {
      if (params.type === 'mousePressed') {
        expect(params.modifiers).toBe(8); // shift
      }
      cb();
    });

    await api.cdpClick(1, 50, 50, 8);
  });

  it('resolves selector → element centre before dispatching', async () => {
    const events = [];
    chrome.debugger.sendCommand.mockImplementation((_t, method, params, cb) => {
      if (method === 'Runtime.evaluate') {
        cb({ result: { value: { cx: 120, cy: 240 } } });
      } else if (method === 'Input.dispatchMouseEvent') {
        events.push({ type: params.type, x: params.x, y: params.y });
        cb();
      } else {
        cb({});
      }
    });
    const out = await api.cdpClick(1, undefined, undefined, false, '#submit');
    expect(events[0]).toEqual({ type: 'mousePressed', x: 120, y: 240 });
    expect(out).toMatchObject({ clicked: true, x: 120, y: 240, selector: '#submit' });
  });

  it('throws when selector matches nothing', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, method, _p, cb) => {
      if (method === 'Runtime.evaluate') cb({ result: { subtype: 'null', value: null } });
      else cb({});
    });
    await expect(api.cdpClick(1, undefined, undefined, false, '#nope')).rejects.toThrow(/No element matches/);
  });

  it('throws when neither selector nor coords are provided', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb({}));
    await expect(api.cdpClick(1, undefined, undefined, false)).rejects.toThrow(/selector or x,y/);
  });

  it('reports zero-size bbox cleanly', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, method, _p, cb) => {
      if (method === 'Runtime.evaluate') cb({ result: { value: { error: 'element has zero-size bbox' } } });
      else cb({});
    });
    await expect(api.cdpClick(1, undefined, undefined, false, '.hidden')).rejects.toThrow(/zero-size bbox/);
  });
});

// ============================================================
// cdpType()
// ============================================================
describe('cdpType', () => {
  it('sends keyDown/keyUp for each character', async () => {
    const events = [];
    chrome.debugger.sendCommand.mockImplementation((_t, _m, params, cb) => {
      events.push(params.type);
      cb();
    });

    await api.cdpType(1, 'ab', null, false, 0); // wpm=0 → instant, no timer delays
    expect(events).toEqual(['keyDown', 'keyUp', 'keyDown', 'keyUp']);
  });

  it('focuses selector before typing', async () => {
    const calls = [];
    chrome.debugger.sendCommand.mockImplementation((_t, method, params, cb) => {
      calls.push({ method, expression: params?.expression });
      if (method === 'Runtime.evaluate') cb({ result: { value: true } });
      else cb({});
    });
    const out = await api.cdpType(1, 'x', '#email', false, 0);
    expect(calls[0].method).toBe('Runtime.evaluate');
    expect(calls[0].expression).toMatch(/querySelector.*#email/);
    expect(out).toMatchObject({ typed: 1, selector: '#email' });
  });

  it('rejects when selector does not focus', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, method, _p, cb) => {
      if (method === 'Runtime.evaluate') cb({ result: { value: false } });
      else cb({});
    });
    await expect(api.cdpType(1, 'x', '#disabled')).rejects.toThrow(/did not take effect/);
  });

  it('rejects when selector matches nothing', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, method, _p, cb) => {
      if (method === 'Runtime.evaluate') cb({ result: { subtype: 'null', value: null } });
      else cb({});
    });
    await expect(api.cdpType(1, 'x', '#missing')).rejects.toThrow(/No element matches/);
  });
});

// ============================================================
// cdpKey()
// ============================================================
describe('cdpKey', () => {
  it('sends keyDown then keyUp with the Enter virtual key code', async () => {
    const events = [];
    chrome.debugger.sendCommand.mockImplementation((_t, _m, params, cb) => {
      events.push(params);
      cb();
    });

    await api.cdpKey(1, 'Enter');
    expect(events).toEqual([
      { type: 'keyDown', key: 'Enter', code: 'Enter', modifiers: 0, windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13, text: '\r' },
      { type: 'keyUp', key: 'Enter', code: 'Enter', modifiers: 0, windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 },
    ]);
  });

  it('Backspace carries windowsVirtualKeyCode 8 so the delete fires', async () => {
    const events = [];
    chrome.debugger.sendCommand.mockImplementation((_t, _m, params, cb) => { events.push(params); cb(); });
    await api.cdpKey(1, 'Backspace');
    expect(events[0]).toMatchObject({ type: 'rawKeyDown', key: 'Backspace', windowsVirtualKeyCode: 8 });
    expect(events[1]).toMatchObject({ type: 'keyUp', key: 'Backspace', windowsVirtualKeyCode: 8 });
  });

  it('chord (Meta+a) holds the modifier and sets the meta bitmask on the key', async () => {
    const events = [];
    chrome.debugger.sendCommand.mockImplementation((_t, _m, params, cb) => { events.push(params); cb(); });
    await api.cdpKey(1, 'a', ['Meta']);
    // Meta down (no bitmask yet), then 'a' down/up with meta=4, then Meta up.
    expect(events[0]).toMatchObject({ type: 'rawKeyDown', key: 'Meta' });
    expect(events[1]).toMatchObject({ type: 'keyDown', key: 'a', code: 'KeyA', windowsVirtualKeyCode: 65, modifiers: 4 });
    // 'a' must NOT carry text when a non-shift modifier is held (it's a command).
    expect(events[1].text).toBeUndefined();
    expect(events[2]).toMatchObject({ type: 'keyUp', key: 'a', modifiers: 4 });
    expect(events[3]).toMatchObject({ type: 'keyUp', key: 'Meta' });
  });

  it('dispatch cdp_key forwards modifiers', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb());
    const result = await api.dispatch('cdp_key', { tabId: 1, key: 'a', modifiers: ['Control'] });
    expect(result).toEqual({ pressed: 'a', modifiers: ['Control'] });
  });
});

// ============================================================
// cdpType clear + cdpClick multi-click (value-replacement path)
// ============================================================
describe('cdpType clear', () => {
  it('selects-all + Backspace before typing when clear=true', async () => {
    const keyEvents = [];
    chrome.debugger.sendCommand.mockImplementation((_t, method, params, cb) => {
      if (method === 'Input.dispatchKeyEvent') keyEvents.push(params);
      cb();
    });
    const out = await api.cdpType(1, 'new', null, true, 0);
    // A select-all chord (some modifier held with 'a') must appear before the typed text.
    const selectAll = keyEvents.find(e => e.key === 'a' && e.modifiers && (e.modifiers & ~8) !== 0);
    expect(selectAll).toBeTruthy();
    expect(keyEvents.some(e => e.key === 'Backspace')).toBe(true);
    expect(out).toMatchObject({ typed: 3, cleared: true });
  });
});

describe('cdpClick multi-click', () => {
  it('triple-click escalates clickCount 1→2→3', async () => {
    const counts = [];
    chrome.debugger.sendCommand.mockImplementation((_t, method, params, cb) => {
      if (method === 'Input.dispatchMouseEvent' && params.type === 'mousePressed') counts.push(params.clickCount);
      cb();
    });
    const out = await api.cdpClick(1, 10, 20, false, null, 3);
    expect(counts).toEqual([1, 2, 3]);
    expect(out.clickCount).toBe(3);
  });
});

// ============================================================
// Humanized typing cadence
// ============================================================
describe('humanGapMs', () => {
  const wpm = 110;
  const base = 60000 / (wpm * 5); // ~109ms

  it('scales with wpm and stays within the jitter band', () => {
    const rnd = vi.spyOn(Math, 'random').mockReturnValue(0.5); // 0.6+0.5 = 1.1×
    expect(humanGapMs('x', wpm)).toBeCloseTo(base * 1.1, 5);
    rnd.mockRestore();
  });

  it('pauses longer after a space and after punctuation', () => {
    const rnd = vi.spyOn(Math, 'random').mockReturnValue(0.5);
    const letter = humanGapMs('x', wpm);
    expect(humanGapMs(' ', wpm)).toBeCloseTo(letter * 1.8, 5);
    expect(humanGapMs('.', wpm)).toBeCloseTo(letter * 2.2, 5);
    rnd.mockRestore();
  });

  it('clamps to [20, 1500] ms', () => {
    const rnd = vi.spyOn(Math, 'random').mockReturnValue(0);
    expect(humanGapMs('x', 1)).toBe(1500);   // absurdly slow → capped high
    rnd.mockReturnValue(0);
    expect(humanGapMs('x', 100000)).toBe(20); // absurdly fast → floored low
    rnd.mockRestore();
  });
});

describe('cdpType cadence', () => {
  it('defaults to 110 WPM (humanized) when wpm is omitted', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb());
    const rnd = vi.spyOn(Math, 'random').mockReturnValue(0); // tiny dwell, single char → fast
    const out = await api.cdpType(1, 'a'); // no wpm
    expect(out.wpm).toBe(DEFAULT_TYPE_WPM);
    expect(out).toMatchObject({ typed: 1, cleared: false });
    rnd.mockRestore();
  });

  it('wpm=0 disables timing and reports wpm:0', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb());
    const out = await api.cdpType(1, 'abc', null, false, 0);
    expect(out.wpm).toBe(0);
    expect(out.typed).toBe(3);
  });
});

// ============================================================
// cdpScroll()
// ============================================================
describe('cdpScroll', () => {
  it('sends mouseWheel event', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, params, cb) => {
      expect(params.type).toBe('mouseWheel');
      expect(params.deltaY).toBe(300);
      cb();
    });

    await api.cdpScroll(1, 600, 400, 0, 300);
  });

  it('resolves selector → centre and dispatches wheel there', async () => {
    const calls = [];
    chrome.debugger.sendCommand.mockImplementation((_t, method, params, cb) => {
      calls.push({ method, params });
      if (method === 'Runtime.evaluate') cb({ result: { value: { cx: 200, cy: 600 } } });
      else cb({});
    });
    const out = await api.cdpScroll(1, undefined, undefined, 0, 400, '.dropdown');
    const wheel = calls.find(c => c.method === 'Input.dispatchMouseEvent' && c.params.type === 'mouseWheel');
    expect(wheel.params).toMatchObject({ x: 200, y: 600, deltaY: 400 });
    expect(out).toMatchObject({ scrolled: true, x: 200, y: 600, selector: '.dropdown' });
  });

  it('rejects when scroll selector matches nothing', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, method, _p, cb) => {
      if (method === 'Runtime.evaluate') cb({ result: { subtype: 'null', value: null } });
      else cb({});
    });
    await expect(api.cdpScroll(1, undefined, undefined, 0, 100, '#nope')).rejects.toThrow(/No element matches/);
  });
});

// ============================================================
// setInputFiles()
// ============================================================
describe('setInputFiles', () => {
  function wireCdp({ tagName = 'INPUT', type = 'file', multiple = true } = {}) {
    chrome.debugger.sendCommand.mockImplementation((_t, method, _params, cb) => {
      if (method === 'Runtime.evaluate') {
        cb({ result: { objectId: 'obj-1', subtype: undefined } });
      } else if (method === 'Runtime.callFunctionOn') {
        cb({ result: { value: { tagName, type, multiple, name: 'photos' } } });
      } else if (method === 'DOM.setFileInputFiles' || method === 'Runtime.releaseObject') {
        cb({});
      } else {
        cb({});
      }
    });
  }

  it('attaches files to a single-file input', async () => {
    wireCdp({ multiple: false });
    const r = await api.setInputFiles(1, 'input[type=file]', ['/a.jpg']);
    expect(r).toEqual({ attached: 1, multiple: false, name: 'photos' });
  });

  it('attaches multiple files to a multiple input', async () => {
    wireCdp({ multiple: true });
    const r = await api.setInputFiles(1, 'input[type=file]', ['/a.jpg', '/b.jpg']);
    expect(r.attached).toBe(2);
  });

  it('rejects when input is not multiple but more than one file given', async () => {
    wireCdp({ multiple: false });
    await expect(api.setInputFiles(1, '#x', ['/a.jpg', '/b.jpg'])).rejects.toThrow(/not 'multiple'/);
  });

  it('rejects when resolved element is not an input', async () => {
    wireCdp({ tagName: 'DIV', type: '' });
    await expect(api.setInputFiles(1, '#x', ['/a.jpg'])).rejects.toThrow(/not <input type=file>/);
  });

  it('rejects empty file list before touching CDP', async () => {
    await expect(api.setInputFiles(1, '#x', [])).rejects.toThrow(/non-empty array/);
  });

  it('rejects missing selector', async () => {
    await expect(api.setInputFiles(1, '', ['/a.jpg'])).rejects.toThrow(/selector is required/);
  });

  it('rejects when selector resolves to nothing', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, method, _p, cb) => {
      if (method === 'Runtime.evaluate') cb({ result: { subtype: 'null' } });
      else cb({});
    });
    await expect(api.setInputFiles(1, '#missing', ['/a.jpg'])).rejects.toThrow(/No <input type=file> resolved/);
  });
});

// ============================================================
// interceptFileChooser()
// ============================================================
describe('interceptFileChooser', () => {
  it('arms interception with files queued', async () => {
    const calls = [];
    chrome.debugger.sendCommand.mockImplementation((_t, method, params, cb) => {
      calls.push({ method, params });
      cb({});
    });
    const r = await api.interceptFileChooser(2, true, ['/photo.jpg']);
    expect(r).toMatchObject({ armed: true, tabId: 2, files: 1, mode: 'one-shot' });
    expect(r.note).toMatch(/one-shot|re-arm|next/i);
    expect(calls.map(c => c.method)).toContain('Page.setInterceptFileChooserDialog');
    const setCall = calls.find(c => c.method === 'Page.setInterceptFileChooserDialog');
    expect(setCall.params).toEqual({ enabled: true });
  });

  it('disarms interception', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb({}));
    const r = await api.interceptFileChooser(2, false);
    expect(r).toMatchObject({ armed: false, tabId: 2 });
  });

  it('rejects arming without files', async () => {
    await expect(api.interceptFileChooser(2, true, [])).rejects.toThrow(/non-empty array/);
  });
});

// ============================================================
// checkNative()
// ============================================================
describe('checkNative', () => {
  it('returns true when health check succeeds', async () => {
    globalThis.fetch.mockResolvedValue({ ok: true });
    // Reset cached value — checkNative caches the result
    // We call it fresh; the internal nativeAvailable might be cached from previous calls.
    // Since we can't reset the internal let, we test the fetch interaction.
    const result = await api.checkNative();
    expect(typeof result).toBe('boolean');
  });
});

// ============================================================
// dispatch()
// ============================================================
describe('dispatch', () => {
  it('list_tabs — queries all tabs', async () => {
    chrome.tabs.query.mockResolvedValue([
      { id: 1, title: 'Tab 1', url: 'https://a.com', active: true, windowId: 1 },
    ]);
    const result = await api.dispatch('list_tabs', {});
    expect(chrome.tabs.query).toHaveBeenCalledWith({});
    expect(result).toHaveLength(1);
    expect(result[0]).toHaveProperty('title', 'Tab 1');
  });

  it('navigate — updates tab URL', async () => {
    chrome.tabs.query.mockResolvedValue([{ id: 3 }]);
    chrome.tabs.update.mockResolvedValue({});
    const result = await api.dispatch('navigate', { url: 'https://b.com' });
    expect(chrome.tabs.update).toHaveBeenCalledWith(3, { url: 'https://b.com' });
    expect(result).toEqual({ tabId: 3, url: 'https://b.com' });
  });

  it('navigate — uses provided tabId', async () => {
    chrome.tabs.update.mockResolvedValue({});
    const result = await api.dispatch('navigate', { tabId: 5, url: 'https://c.com' });
    expect(chrome.tabs.update).toHaveBeenCalledWith(5, { url: 'https://c.com' });
    expect(result.tabId).toBe(5);
  });

  it('page_reload — soft reload of active tab', async () => {
    chrome.tabs.query.mockResolvedValue([{ id: 7 }]);
    const result = await api.dispatch('page_reload', {});
    expect(chrome.tabs.reload).toHaveBeenCalledWith(7, { bypassCache: false });
    expect(result).toEqual({ reloaded: true });
  });

  it('page_reload — hard reload with ignoreCache', async () => {
    const result = await api.dispatch('page_reload', { tabId: 9, ignoreCache: true });
    expect(chrome.tabs.reload).toHaveBeenCalledWith(9, { bypassCache: true });
    expect(result).toEqual({ reloaded: true });
  });

  // A tab strip the tabs.get/move mocks read and reorder, so move_tabs is
  // checked against where tabs end up rather than against the calls it makes.
  function mockStrip(ids, pinned = [], otherWindow = []) {
    // Window 1 holds `ids`, window 2 holds `otherWindow`.
    const wins = { 1: [...ids], 2: [...otherWindow] };
    const windowOf = (id) => Object.keys(wins).map(Number).find(w => wins[w].includes(id));
    chrome.tabs.get.mockReset().mockImplementation(async (id) => {
      const windowId = windowOf(id);
      if (!windowId) throw new Error(`No tab with id: ${id}`);
      return { id, index: wins[windowId].indexOf(id), windowId, title: `T${id}`, url: `https://t/${id}`, pinned: pinned.includes(id) };
    });
    chrome.tabs.move.mockReset().mockImplementation(async (id, { index, windowId }) => {
      const from = windowOf(id);
      const to = windowId ?? from;
      const before = { ...wins };
      wins[from] = wins[from].filter(t => t !== id);
      const target = [...wins[to]];
      target.splice(index < 0 ? target.length : index, 0, id);
      wins[to] = target;
      // Like Firefox, quietly ignore a move that would put a pinned tab
      // after an unpinned one.
      const flags = wins[to].map(t => pinned.includes(t));
      if (flags.some((p, i) => p && i > 0 && !flags[i - 1])) Object.assign(wins, before);
    });
    chrome.tabs.remove.mockReset().mockImplementation(async (gone) => {
      for (const w of Object.keys(wins)) wins[w] = wins[w].filter(t => !gone.includes(t));
    });
    return (windowId = 1) => wins[windowId];
  }

  it('move_tabs — moves tabs next to an anchor in another window', async () => {
    const strip = mockStrip([1, 2, 3], [], [7, 8]);
    const result = await api.dispatch('move_tabs', { tabIds: [3, 1], after: 7 });
    expect(strip(1)).toEqual([2]);
    expect(strip(2)).toEqual([7, 3, 1, 8]);
    expect(result.tabs).toMatchObject([{ id: 3, windowId: 2, index: 1 }, { id: 1, windowId: 2, index: 2 }]);
    expect(result.skipped).toEqual([]);
  });

  it('move_tabs — places tabs before an anchor in another window', async () => {
    const strip = mockStrip([1, 2, 3], [], [7, 8]);
    await api.dispatch('move_tabs', { tabIds: [1, 2], before: 8 });
    expect(strip(1)).toEqual([3]);
    expect(strip(2)).toEqual([7, 1, 2, 8]);
  });

  it('move_tabs — sends tabs to an index in the window named by windowId', async () => {
    const strip = mockStrip([1, 2, 3], [], [7, 8]);
    const result = await api.dispatch('move_tabs', { tabIds: [2], index: 0, windowId: 2 });
    expect(strip(1)).toEqual([1, 3]);
    expect(strip(2)).toEqual([2, 7, 8]);
    expect(result.tabs).toMatchObject([{ id: 2, windowId: 2, index: 0 }]);
  });

  it('list_tabs — reports strip position and pinned state', async () => {
    chrome.tabs.query.mockResolvedValue([
      { id: 1, title: 'A', url: 'https://a.com', active: true, windowId: 1, index: 4, pinned: true },
    ]);
    const [tab] = await api.dispatch('list_tabs', {});
    expect(tab).toMatchObject({ id: 1, index: 4, pinned: true });
  });

  it('move_tabs — appends in the given order by default', async () => {
    const strip = mockStrip([1, 2, 3, 4, 5]);
    const result = await api.dispatch('move_tabs', { tabIds: [4, 1] });
    expect(strip()).toEqual([2, 3, 5, 4, 1]);
    expect(result.tabs.map(t => [t.id, t.index])).toEqual([[4, 3], [1, 4]]);
  });

  it('move_tabs — places tabs after an anchor, from both sides of it', async () => {
    const strip = mockStrip([1, 2, 3, 4, 5]);
    await api.dispatch('move_tabs', { tabIds: [5, 1], after: 3 });
    expect(strip()).toEqual([2, 3, 5, 1, 4]);
  });

  it('move_tabs — places tabs before an anchor', async () => {
    const strip = mockStrip([1, 2, 3, 4, 5]);
    await api.dispatch('move_tabs', { tabIds: [5, 1], before: 3 });
    expect(strip()).toEqual([2, 5, 1, 3, 4]);
  });

  it('move_tabs — starts at an explicit index', async () => {
    const strip = mockStrip([1, 2, 3, 4, 5]);
    await api.dispatch('move_tabs', { tabIds: [5, 4], index: 0 });
    expect(strip()).toEqual([5, 4, 1, 2, 3]);
  });

  it('move_tabs — skips tabs that are gone and keeps going', async () => {
    const strip = mockStrip([1, 2, 3]);
    const result = await api.dispatch('move_tabs', { tabIds: [9, 1] });
    expect(strip()).toEqual([2, 3, 1]);
    expect(result.skipped).toEqual([{ id: 9, reason: 'not found' }]);
  });

  it('move_tabs — does not chain later tabs off one the browser refused to move', async () => {
    const strip = mockStrip([9, 1, 2, 3], [9]);
    const result = await api.dispatch('move_tabs', { tabIds: [1, 9, 2, 3] });
    expect(strip()).toEqual([9, 1, 2, 3]);
    expect(result.tabs.map(t => t.id)).toEqual([1, 2, 3]);
    expect(result.skipped).toEqual([{ id: 9, reason: expect.stringMatching(/left it at index 0/) }]);
  });

  it('move_tabs — still reports the rest when a moved tab closes before the read-back', async () => {
    mockStrip([1, 2, 3]);
    const get = chrome.tabs.get.getMockImplementation();
    let seen = 0;
    chrome.tabs.get.mockImplementation(async (id) => {
      // Tab 1 is read twice while it is being placed; the third read is the read-back.
      if (id === 1 && ++seen > 2) throw new Error('No tab with id: 1');
      return get(id);
    });
    const result = await api.dispatch('move_tabs', { tabIds: [2, 1] });
    expect(result.tabs.map(t => t.id)).toEqual([2]);
    expect(result.skipped).toEqual([{ id: 1, reason: 'closed before its final position was read' }]);
  });

  it('close_tabs — closes tabs but leaves pinned ones', async () => {
    const strip = mockStrip([1, 2, 3], [2]);
    const result = await api.dispatch('close_tabs', { tabIds: [1, 2, 9] });
    expect(strip()).toEqual([2, 3]);
    expect(result.closed.map(t => t.id)).toEqual([1]);
    expect(result.skipped).toEqual([
      { id: 2, reason: 'pinned', title: 'T2' },
      { id: 9, reason: 'not found' },
    ]);
  });

  it('close_tabs — closes pinned tabs when asked to', async () => {
    const strip = mockStrip([1, 2], [2]);
    await api.dispatch('close_tabs', { tabIds: [2], includePinned: true });
    expect(strip()).toEqual([1]);
  });

  it('open_tab — opens next to another tab and returns the new id', async () => {
    mockStrip([1, 2, 3]);
    chrome.tabs.create.mockReset().mockResolvedValue({ id: 3 });
    const result = await api.dispatch('open_tab', { nextTo: 1, active: false });
    expect(chrome.tabs.create).toHaveBeenCalledWith({ active: false, windowId: 1, index: 1 });
    expect(result).toMatchObject({ id: 3, windowId: 1 });
  });

  it('wake_tab — loads a discarded tab in place without focusing it', async () => {
    let state = { id: 4, index: 0, windowId: 1, url: 'https://a.com', discarded: true, status: 'complete' };
    chrome.tabs.get.mockReset().mockImplementation(async () => ({ ...state }));
    chrome.tabs.reload.mockReset().mockImplementation(async () => {
      state = { ...state, discarded: false, status: 'complete' };
    });
    chrome.tabs.sendMessage.mockResolvedValue({ pong: true });
    const result = await api.dispatch('wake_tab', { tabId: 4 });
    expect(chrome.tabs.reload).toHaveBeenCalledWith(4);
    expect(chrome.tabs.update).not.toHaveBeenCalled();
    expect(chrome.windows.update).not.toHaveBeenCalled();
    expect(result).toMatchObject({ id: 4, wasDiscarded: true, loaded: true, interactive: true });
  });

  it('wake_tab — leaves a loaded tab alone', async () => {
    chrome.tabs.get.mockReset().mockResolvedValue({ id: 4, index: 0, windowId: 1, status: 'complete' });
    chrome.tabs.reload.mockReset();
    chrome.tabs.sendMessage.mockResolvedValue({ pong: true });
    const result = await api.dispatch('wake_tab', { tabId: 4 });
    expect(chrome.tabs.reload).not.toHaveBeenCalled();
    expect(result).toMatchObject({ wasDiscarded: false, loaded: true, interactive: true });
  });

  it('wake_tab — reports a loaded page that cannot be scripted', async () => {
    chrome.tabs.get.mockReset().mockResolvedValue({ id: 4, index: 0, windowId: 1, status: 'complete' });
    chrome.tabs.sendMessage.mockRejectedValue(new Error('no receiver'));
    chrome.scripting.executeScript.mockRejectedValue(new Error('Missing host permission for the tab'));
    const result = await api.dispatch('wake_tab', { tabId: 4 });
    expect(result).toMatchObject({ loaded: true, interactive: false, reason: 'Missing host permission for the tab' });
  });

  it('wake_tab — gives up when the tab stays unloaded', async () => {
    chrome.tabs.get.mockReset().mockResolvedValue({ id: 4, index: 0, windowId: 1, discarded: true, status: 'complete' });
    const result = await api.dispatch('wake_tab', { tabId: 4, timeoutMs: 250 });
    expect(result).toMatchObject({ loaded: false, interactive: false, reason: 'still unloaded after waiting' });
  });

  it('wake_tab — sets the URL again when reload leaves the tab unloaded', async () => {
    vi.useFakeTimers();
    try {
      let state = { id: 4, index: 0, windowId: 1, url: 'https://a.com', discarded: true, status: 'complete' };
      chrome.tabs.get.mockReset().mockImplementation(async () => ({ ...state }));
      chrome.tabs.reload.mockReset().mockResolvedValue(undefined); // ignored by the browser
      chrome.tabs.update.mockReset().mockImplementation(async () => {
        state = { ...state, discarded: false, status: 'complete' };
      });
      chrome.tabs.sendMessage.mockResolvedValue({ pong: true });
      const pending = api.dispatch('wake_tab', { tabId: 4 });
      await vi.advanceTimersByTimeAsync(1500);
      expect(chrome.tabs.update).not.toHaveBeenCalled(); // reload gets its chance first
      await vi.advanceTimersByTimeAsync(1500);
      const result = await pending;
      expect(chrome.tabs.update).toHaveBeenCalledTimes(1);
      expect(chrome.tabs.update).toHaveBeenCalledWith(4, { url: 'https://a.com' });
      expect(result).toMatchObject({ wasDiscarded: true, loaded: true, interactive: true });
    } finally {
      vi.useRealTimers();
    }
  });

  it('wake_tab — waits for a tab that is still loading', async () => {
    let state = { id: 4, index: 0, windowId: 1, url: 'https://a.com', status: 'loading' };
    chrome.tabs.get.mockReset().mockImplementation(async () => ({ ...state }));
    chrome.tabs.reload.mockReset();
    chrome.tabs.sendMessage.mockResolvedValue({ pong: true });
    const pending = api.dispatch('wake_tab', { tabId: 4 });
    await vi.waitFor(() => expect(chrome.tabs.onUpdated.addListener).toHaveBeenCalled());
    state = { ...state, status: 'complete' };
    chrome.tabs.onUpdated.addListener.mock.lastCall[0](4, { status: 'complete' });
    const result = await pending;
    expect(chrome.tabs.reload).not.toHaveBeenCalled();
    expect(result).toMatchObject({ wasDiscarded: false, loaded: true, interactive: true });
  });

  it('wake_tab — reports a tab that is still loading at the deadline', async () => {
    chrome.tabs.get.mockReset().mockResolvedValue({ id: 4, index: 0, windowId: 1, url: 'https://a.com', status: 'loading' });
    const result = await api.dispatch('wake_tab', { tabId: 4, timeoutMs: 50 });
    expect(result).toMatchObject({ loaded: false, interactive: false, reason: 'still loading after waiting' });
  });

  it('open_tab — waits for the page and flags a load that timed out', async () => {
    vi.useFakeTimers();
    try {
      chrome.tabs.create.mockReset().mockResolvedValue({ id: 3 });
      chrome.tabs.get.mockReset().mockResolvedValue({ id: 3, index: 0, windowId: 1, url: 'https://slow.example/', status: 'loading' });
      const pending = api.dispatch('open_tab', { url: 'https://slow.example/' });
      await vi.advanceTimersByTimeAsync(15000);
      expect(await pending).toMatchObject({ id: 3, loadTimedOut: true });
    } finally {
      vi.useRealTimers();
    }
  });

  it('open_tab — returns at once with wait:false, and for a loaded page without a flag', async () => {
    chrome.tabs.create.mockReset().mockResolvedValue({ id: 3 });
    chrome.tabs.get.mockReset().mockResolvedValue({ id: 3, index: 0, windowId: 1, url: 'https://a.com', status: 'loading' });
    const added = chrome.tabs.onUpdated.addListener.mock.calls.length;
    const result = await api.dispatch('open_tab', { url: 'https://a.com', wait: false });
    expect(chrome.tabs.onUpdated.addListener.mock.calls.length).toBe(added); // did not wait
    expect(result).not.toHaveProperty('loadTimedOut');

    chrome.tabs.get.mockResolvedValue({ id: 3, index: 0, windowId: 1, url: 'https://a.com', status: 'complete' });
    expect(await api.dispatch('open_tab', { url: 'https://a.com' })).not.toHaveProperty('loadTimedOut');
  });

  it('explainError — points an unloaded tab at wake_tab', async () => {
    chrome.tabs.get.mockResolvedValue({ id: 4, discarded: true, url: 'https://a.com', status: 'complete' });
    const msg = await api.explainError(new Error('An unexpected error occurred'), { tabId: 4 });
    expect(msg).toMatch(/^An unexpected error occurred\. What to do: this tab is unloaded/);
    expect(msg).toMatch(/wake_tab/);
  });

  it('explainError — does not send a failed wake_tab back to wake_tab', async () => {
    chrome.tabs.get.mockResolvedValue({ id: 4, discarded: true, url: 'https://a.com', status: 'complete' });
    const msg = await api.explainError(new Error('An unexpected error occurred'), { tabId: 4 }, 'wake_tab');
    expect(msg).toMatch(/open_tab with its URL \(https:\/\/a\.com\)/);
    expect(msg).not.toMatch(/Call wake_tab/);
  });

  it('explainError — does not suggest reloading a page that refuses scripts', async () => {
    chrome.tabs.get.mockResolvedValue({ id: 4, url: 'https://a.com/doc.pdf', status: 'complete' });
    const msg = await api.explainError(new Error('Missing host permission for the tab'), { tabId: 4 });
    expect(msg).toMatch(/does not let extensions read or drive this page/);
    expect(msg).not.toMatch(/page_reload/);
  });

  it('getSessionId — is kept for the browser session and empty without session storage', async () => {
    const saved = chrome.storage;
    const store = {};
    chrome.storage = { session: {
      get: async () => ({ ...store }),
      set: async (v) => { Object.assign(store, v); },
    } };
    const first = await api.getSessionId();
    expect(first).toBeTruthy();
    expect(await api.getSessionId()).toBe(first);
    chrome.storage = {};
    expect(await api.getSessionId()).toBe('');
    chrome.storage = saved;
  });

  it('explainError — points a closed tab at list_tabs and open_tab', async () => {
    chrome.tabs.get.mockRejectedValue(new Error('No tab with id: 4'));
    const msg = await api.explainError(new Error('No tab with id: 4'), { tabId: 4 });
    expect(msg).toMatch(/no longer exists/);
    expect(msg).toMatch(/list_tabs.*open_tab/);
  });

  it('explainError — names a browser-internal page', async () => {
    chrome.tabs.get.mockResolvedValue({ id: 4, url: 'about:preferences', status: 'complete' });
    const msg = await api.explainError(new Error('Missing host permission for the tab'), { tabId: 4 });
    expect(msg).toMatch(/about:preferences, a browser-internal page/);
  });

  it('explainError — tells the agent to stay in this browser when the page is silent', async () => {
    chrome.tabs.get.mockResolvedValue({ id: 4, url: 'https://a.com', status: 'complete' });
    const msg = await api.explainError(new Error('Could not establish connection. Receiving end does not exist.'), { tabId: 4 });
    expect(msg).toMatch(/page_reload/);
    expect(msg).toMatch(/keep working in this browser/);
  });

  it('explainError — leaves ordinary page errors and explained errors alone', async () => {
    chrome.tabs.get.mockResolvedValue({ id: 4, url: 'https://a.com', status: 'complete' });
    expect(await api.explainError(new Error('No element matches selector: #x'), { tabId: 4 }))
      .toBe('No element matches selector: #x');
    expect(await api.explainError(new Error('nope. What to do: retry'), { tabId: 4 })).toBe('nope. What to do: retry');
    expect(await api.explainError(new Error('boom'), {})).toBe('boom');
  });

  it('set_input_files — routes to CDP', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, method, _p, cb) => {
      if (method === 'Runtime.evaluate') cb({ result: { objectId: 'o' } });
      else if (method === 'Runtime.callFunctionOn') cb({ result: { value: { tagName: 'INPUT', type: 'file', multiple: true } } });
      else cb({});
    });
    const result = await api.dispatch('set_input_files', { tabId: 1, selector: '#u', files: ['/a.jpg'] });
    expect(result.attached).toBe(1);
  });

  it('intercept_file_chooser — arms', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb({}));
    const result = await api.dispatch('intercept_file_chooser', { tabId: 1, enable: true, files: ['/a.jpg'] });
    expect(result).toMatchObject({ armed: true, files: 1 });
  });

  it('cdp_click — calls cdpClick', async () => {
    chrome.debugger.attach.mockResolvedValue(undefined);
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb());
    const result = await api.dispatch('cdp_click', { tabId: 1, x: 10, y: 20 });
    expect(result).toMatchObject({ clicked: true, x: 10, y: 20, shift: false, clickCount: 1 });
  });

  it('cdp_click with shift', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb());
    const result = await api.dispatch('cdp_click', { tabId: 1, x: 10, y: 20, shift: true });
    expect(result.shift).toBe(true);
  });

  it('cdp_type — types text', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb());
    const result = await api.dispatch('cdp_type', { tabId: 1, text: 'hi', wpm: 0 });
    expect(result).toMatchObject({ typed: 2, cleared: false, wpm: 0 });
  });

  it('cdp_key — presses key from map', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb());
    const result = await api.dispatch('cdp_key', { tabId: 1, key: 'Enter' });
    expect(result).toEqual({ pressed: 'Enter' });
  });

  it('cdp_key — handles unknown key', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb());
    const result = await api.dispatch('cdp_key', { tabId: 1, key: 'F5' });
    expect(result).toEqual({ pressed: 'F5' });
  });

  it('cdp_scroll — scrolls', async () => {
    chrome.debugger.sendCommand.mockImplementation((_t, _m, _p, cb) => cb());
    const result = await api.dispatch('cdp_scroll', { tabId: 1, deltaY: 100 });
    expect(result).toMatchObject({ scrolled: true });
    expect(typeof result.x).toBe('number');
    expect(typeof result.y).toBe('number');
  });

  it('execute_js — runs in MAIN world', async () => {
    chrome.scripting.executeScript.mockResolvedValue([{ result: { result: 42 } }]);
    const result = await api.dispatch('execute_js', { tabId: 1, code: '40+2' });
    expect(chrome.scripting.executeScript).toHaveBeenCalled();
    expect(result).toEqual({ result: 42 });
  });

  it('adapt_script — injects script', async () => {
    chrome.scripting.executeScript.mockResolvedValue([{ result: { result: 'ok' } }]);
    const result = await api.dispatch('adapt_script', { tabId: 1, code: 'return "ok"' });
    expect(result).toEqual({ result: 'ok' });
  });

  it('content-script actions route through toContent', async () => {
    // Mock the chain: resolveTab → ensureContentScript → sendMessage
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (cb) cb({ found: 1, results: [{ text: 'hi' }] });
    });

    const result = await api.dispatch('find_text', { tabId: 1, query: 'hi' });
    expect(result).toEqual({ found: 1, results: [{ text: 'hi' }] });
  });

  it('inspect_form — without url, routes straight to toContent', async () => {
    chrome.tabs.query.mockResolvedValue([{ id: 4 }]);
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (cb) cb({ form: { action: '' }, fields: [] });
    });
    const result = await api.dispatch('inspect_form', { formSelector: '#f' });
    // No navigation occurred — only the content-script inspect ran.
    expect(chrome.tabs.update).not.toHaveBeenCalled();
    // navTimedOut is folded in: false when no navigation was issued.
    expect(result).toEqual({ form: { action: '' }, fields: [], navTimedOut: false });
  });

  it('inspect_form — with url already on target, skips navigation', async () => {
    chrome.tabs.get.mockResolvedValue({ id: 4, url: 'https://app.example/new?x=1', status: 'complete' });
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (cb) cb({ form: {}, fields: [] });
    });
    await api.dispatch('inspect_form', { tabId: 4, url: 'https://app.example/new' });
    // Same origin+path as the tab's URL — no navigation needed.
    expect(chrome.tabs.update).not.toHaveBeenCalled();
  });

  it('inspect_form — with url off target, navigates then inspects', async () => {
    chrome.tabs.get.mockResolvedValue({ id: 4, url: 'https://other.example/', status: 'complete' });
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (cb) cb({ form: {}, fields: [] });
    });
    await api.dispatch('inspect_form', { tabId: 4, url: 'https://app.example/new' });
    // Off-target tab — navigate to the base URL first.
    expect(chrome.tabs.update).toHaveBeenCalledWith(4, { url: 'https://app.example/new' });
  });

  it('prepare_for_user_click — routes to content script and broadcasts a handoff', async () => {
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (cb) cb({ found: true, label: 'Allow', bbox: { x: 1, y: 2, width: 3, height: 4 } });
    });

    const result = await api.dispatch('prepare_for_user_click', {
      tabId: 1, selector: '#allow', hint: 'Click Allow', label: 'Allow', reason: 'auth',
    });
    expect(result).toMatchObject({ found: true, label: 'Allow' });
  });

  it('prepare_for_user_click — emits a handoff broadcast row for the popup', async () => {
    chrome.tabs.sendMessage.mockImplementation((id, msg, _opts, cb) => {
      if (msg.action === 'ping') { if (cb) cb({ ok: true }); return; }
      if (cb) cb({ found: true, label: 'Allow' });
    });
    // Register a popup port so broadcast() has a sink to post to.
    const port = {
      name: 'popup', postMessage: vi.fn(),
      onDisconnect: __makeEvent(), onMessage: __makeEvent(),
    };
    chrome.runtime.onConnect._fire(port);

    await api.dispatch('prepare_for_user_click', {
      tabId: 7, selector: '#allow', hint: 'Click Allow', reason: 'auth',
    });

    const handoffMsg = port.postMessage.mock.calls
      .map(c => c[0]).find(m => m.type === 'handoff');
    expect(handoffMsg).toBeDefined();
    expect(handoffMsg).toMatchObject({ active: true, tabId: 7, hint: 'Click Allow' });
  });

  it('throws for unknown action', async () => {
    await expect(api.dispatch('nonexistent', {})).rejects.toThrow('Unknown action');
  });
});

// ============================================================
// prepare_for_user_click — gating
// ============================================================
describe('prepare_for_user_click gating', () => {
  it('is in the cursor-gate set so the screenshot catches the banner', () => {
    expect(api.PAGE_ACTIONS_THAT_GATE_ON_CURSOR.has('prepare_for_user_click')).toBe(true);
  });
});

// ============================================================
// connect() — WebSocket
// ============================================================
describe('connect', () => {
  // connect() has a guard: if ws is CONNECTING or OPEN, it returns early.
  // The initial connect() during file load created a WS. We need to close it
  // to set the internal ws=null before each test.
  function closeExistingWs() {
    const last = MockWebSocket.instances[MockWebSocket.instances.length - 1];
    if (last && last.readyState !== WebSocket.CLOSED) {
      // Trigger onclose which sets internal ws = null
      last.readyState = WebSocket.CLOSED;
      if (last.onclose) last.onclose();
    }
  }

  it('creates a WebSocket to WS_URL', () => {
    closeExistingWs();
    const countBefore = MockWebSocket.instances.length;
    api.connect();
    expect(MockWebSocket.instances.length).toBeGreaterThan(countBefore);
    const ws = MockWebSocket.instances[MockWebSocket.instances.length - 1];
    expect(ws.url).toBe('ws://127.0.0.1:18321');
  });

  it('sets up badge and keepalive on open', () => {
    closeExistingWs();
    api.connect();
    const ws = MockWebSocket.instances[MockWebSocket.instances.length - 1];
    ws._open();
    expect(chrome.action.setBadgeText).toHaveBeenCalledWith({ text: 'ON' });
    expect(chrome.alarms.create).toHaveBeenCalledWith('turbo-keepalive', expect.any(Object));
  });

  it('clears badge and schedules reconnect on close', () => {
    closeExistingWs();
    api.connect();
    const ws = MockWebSocket.instances[MockWebSocket.instances.length - 1];
    ws._open();
    chrome.action.setBadgeText.mockClear();
    ws.close();
    expect(chrome.action.setBadgeText).toHaveBeenCalledWith({ text: '' });
    expect(chrome.alarms.create).toHaveBeenCalledWith('turbo-reconnect', expect.any(Object));
  });
});

// ============================================================
// startKeepalive / stopKeepalive
// ============================================================
describe('keepalive', () => {
  it('startKeepalive creates an alarm', () => {
    api.startKeepalive();
    expect(chrome.alarms.create).toHaveBeenCalledWith('turbo-keepalive', { periodInMinutes: 0.4 });
  });

  it('stopKeepalive clears the alarm', () => {
    api.stopKeepalive();
    expect(chrome.alarms.clear).toHaveBeenCalledWith('turbo-keepalive');
  });
});

// ============================================================
// pingWS / handlePong / forceReconnect — active WS health check
// ============================================================
describe('ping/pong health check', () => {
  function freshOpenWS() {
    // Tear down any prior connection so onclose doesn't pollute our mock,
    // then connect + open a fresh one.
    const prior = MockWebSocket.instances[MockWebSocket.instances.length - 1];
    if (prior && prior.readyState !== MockWebSocket.CLOSED) prior.close();
    api.connect();
    const ws = MockWebSocket.instances[MockWebSocket.instances.length - 1];
    ws._open();
    ws.send.mockClear();
    ws.close.mockClear();
    return ws;
  }

  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('sends {type:ping,id} on the open WS', () => {
    const ws = freshOpenWS();
    api.pingWS();
    const sent = JSON.parse(ws.send.mock.calls[0][0]);
    expect(sent.type).toBe('ping');
    expect(sent.id).toMatch(/^__ping_/);
  });

  it('matching pong cancels the timeout (no force-close)', () => {
    const ws = freshOpenWS();
    api.pingWS();
    const id = JSON.parse(ws.send.mock.calls[0][0]).id;
    api.handlePong(id);
    vi.advanceTimersByTime(5000);
    expect(ws.close).not.toHaveBeenCalled();
  });

  it('no pong → forceReconnect closes the socket and schedules reconnect', () => {
    const ws = freshOpenWS();
    chrome.alarms.create.mockClear();
    api.pingWS();
    vi.advanceTimersByTime(3500);
    expect(ws.close).toHaveBeenCalled();
    expect(chrome.alarms.create).toHaveBeenCalledWith('turbo-reconnect', expect.any(Object));
  });

  it('synchronous send failure triggers forceReconnect', () => {
    const ws = freshOpenWS();
    ws.send.mockImplementation(() => { throw new Error('socket closed'); });
    api.pingWS();
    expect(ws.close).toHaveBeenCalled();
  });

  it('non-matching pong is a no-op', () => {
    const ws = freshOpenWS();
    api.pingWS();
    api.handlePong('not-the-id');
    vi.advanceTimersByTime(3500);
    expect(ws.close).toHaveBeenCalled(); // timeout still fired
  });
});
