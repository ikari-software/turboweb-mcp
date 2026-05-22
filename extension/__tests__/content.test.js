import { describe, it, expect, vi, beforeAll, beforeEach } from 'vitest';
import fs from 'fs';
import path from 'path';
import vm from 'vm';

// --- Helper: mock getBoundingClientRect on an element ---
function mockRect(el, { x = 0, y = 0, width = 100, height = 20 } = {}) {
  el.getBoundingClientRect = () => ({
    x, y, width, height,
    top: y, left: x,
    right: x + width, bottom: y + height,
  });
}

// Give every element in the DOM a non-zero rect so extractText/findText don't skip them
function mockAllRects(root = document.body) {
  let yy = 0;
  root.querySelectorAll('*').forEach(el => {
    mockRect(el, { x: 0, y: yy, width: 200, height: 20 });
    yy += 20;
  });
}

let api;
let messageHandler;

beforeAll(() => {
  // Capture message handler registered by content.js
  chrome.runtime.onMessage.addListener.mockImplementation((fn) => {
    messageHandler = fn;
  });

  // The overlay attaches a CLOSED shadow root, which tests can't read into.
  // Force `open` AND stash the overlay's host element so handoff-overlay
  // tests can reach the rendered nodes even after the global beforeEach
  // wipes document.body (which detaches the host from the DOM but leaves
  // content.js's module-level `root` ref — and the shadow tree — intact).
  // Production stays closed; this is a test-only relaxation.
  const realAttachShadow = Element.prototype.attachShadow;
  Element.prototype.attachShadow = function (init) {
    const sr = realAttachShadow.call(this, { ...init, mode: 'open' });
    if (this.id === '__turbo_overlay_host') globalThis.__overlayHost = this;
    return sr;
  };

  // Load content.js — inject a globalThis export line before the closing IIFE
  const filePath = path.resolve(__dirname, '../content.js');
  let code = fs.readFileSync(filePath, 'utf8');

  const fns = [
    'sel', 'viewport', 'extractText', 'findText', 'inspectElement',
    'getInteractiveMap', 'queryElements', 'inspectForm', 'pageCapabilities',
    'clickElement', 'typeText',
    'scrollPage', 'getHTML', 'depthLimitedHTML', 'getPageStructure',
    'executeJS', 'injectScript', 'prepareForUserClick', 'overlay',
    'dragDropFile', 'runDropInPage', '__turboPerformDrop',
    'domMutationsMark', 'domMutationsSince', 'screenshotDiffMeta',
  ].join(', ');

  // Inject an export line right before the closing })(); so functions are accessible
  code = code.replace(
    /\}\)\(\);?\s*$/,
    `globalThis.__contentAPI = { ${fns} };\n})();`,
  );

  const script = new vm.Script(code, { filename: filePath });
  script.runInThisContext();
  api = globalThis.__contentAPI;
});

beforeEach(() => {
  document.body.innerHTML = '';
  window.scrollX = 0;
  window.scrollY = 0;
  window.scrollBy.mockClear();
});

// ============================================================
// sel() — CSS selector generation
// ============================================================
describe('sel', () => {
  it('returns #id for element with id', () => {
    document.body.innerHTML = '<div id="foo">x</div>';
    const el = document.getElementById('foo');
    expect(api.sel(el)).toBe('#foo');
  });

  it('returns data-testid selector', () => {
    document.body.innerHTML = '<div data-testid="bar">x</div>';
    const el = document.querySelector('[data-testid="bar"]');
    expect(api.sel(el)).toBe('[data-testid="bar"]');
  });

  it('builds path with nth-of-type for ambiguous siblings', () => {
    document.body.innerHTML = '<div><span>a</span><span>b</span></div>';
    const second = document.querySelectorAll('span')[1];
    const result = api.sel(second);
    expect(result).toContain('nth-of-type(2)');
  });

  it('uses id-bearing ancestor when needed for uniqueness', () => {
    // Two parallel <div><div><span> branches — uniqueness can only be
    // achieved by anchoring at the #root id. The simple chain
    // `div>span` matches BOTH spans, so sel() must walk up to #root.
    document.body.innerHTML = `
      <div id="root"><div><span>target</span></div></div>
      <div><div><span>decoy</span></div></div>
    `;
    const span = document.querySelector('#root span');
    const result = api.sel(span);
    expect(document.querySelectorAll(result).length).toBe(1);
    expect(document.querySelector(result)).toBe(span);
    expect(result).toContain('#root');
  });

  it('returns the shortest unique selector', () => {
    // Only one span — uniqueness is already achieved at the leaf, so
    // we don't bother walking up to the id'd ancestor.
    document.body.innerHTML = '<div id="root"><div><span>x</span></div></div>';
    const span = document.querySelector('span');
    const result = api.sel(span);
    expect(document.querySelectorAll(result).length).toBe(1);
    expect(document.querySelector(result)).toBe(span);
  });

  it('produces a selector that resolves uniquely on a deep tree', () => {
    // Regression test for the bug where the old sel() capped at 5
    // levels and returned non-unique chains like
    // `div:nth-of-type(3)>div:nth-of-type(2)>...>button`. On a tree
    // with multiple matching shapes, the agent's querySelector landed
    // on the FIRST tree-order match — completely the wrong element.
    document.body.innerHTML = `
      <div><div><div><div><button>a</button></div></div></div></div>
      <div><div><div><div><button>b</button></div></div></div></div>
      <div><div><div><div><button>c</button></div></div></div></div>
    `;
    const buttons = document.querySelectorAll('button');
    for (const btn of buttons) {
      const result = api.sel(btn);
      expect(document.querySelectorAll(result).length).toBe(1);
      expect(document.querySelector(result)).toBe(btn);
    }
  });

  it('handles single child without nth-of-type', () => {
    document.body.innerHTML = '<div><p>only</p></div>';
    const p = document.querySelector('p');
    const result = api.sel(p);
    expect(result).not.toContain('nth-of-type');
  });
});

// ============================================================
// viewport()
// ============================================================
describe('viewport', () => {
  it('returns window dimensions and scroll position', () => {
    const vp = api.viewport();
    expect(vp).toEqual({
      w: 1280, h: 800,
      scrollX: 0, scrollY: 0,
      pageW: expect.any(Number),
      pageH: expect.any(Number),
      dpr: 2,
    });
  });
});

// ============================================================
// extractText()
// ============================================================
describe('extractText', () => {
  it('extracts visible text blocks with positions', () => {
    document.body.innerHTML = '<div id="a">Hello</div><div id="b">World</div>';
    mockAllRects();
    const result = api.extractText({});
    expect(result.blocks.length).toBe(2);
    expect(result.blocks[0].text).toBe('Hello');
    expect(result.blocks[1].text).toBe('World');
    expect(result.blocks[0]).toHaveProperty('x');
    expect(result.blocks[0]).toHaveProperty('tag', 'div');
    expect(result.viewport).toHaveProperty('w', 1280);
  });

  it('respects selector scope', () => {
    document.body.innerHTML = '<div id="a">Outside</div><section id="s"><p>Inside</p></section>';
    mockAllRects();
    const result = api.extractText({ selector: '#s' });
    expect(result.blocks.length).toBe(1);
    expect(result.blocks[0].text).toBe('Inside');
  });

  it('throws on invalid selector', () => {
    expect(() => api.extractText({ selector: '#nope' })).toThrow('Element not found');
  });

  it('skips elements with display:none', () => {
    document.body.innerHTML = '<div>Visible</div><div style="display:none">Hidden</div>';
    mockAllRects();
    const result = api.extractText({});
    expect(result.blocks.length).toBe(1);
    expect(result.blocks[0].text).toBe('Visible');
  });

  it('skips elements with zero dimensions', () => {
    document.body.innerHTML = '<div id="a">Big</div><div id="b">Tiny</div>';
    mockRect(document.getElementById('a'), { width: 100, height: 20 });
    mockRect(document.getElementById('b'), { width: 0, height: 0 });
    const result = api.extractText({});
    expect(result.blocks.length).toBe(1);
  });

  it('respects max parameter', () => {
    document.body.innerHTML = '<div>A</div><div>B</div><div>C</div>';
    mockAllRects();
    const result = api.extractText({ max: 2 });
    expect(result.blocks.length).toBe(2);
  });

  it('filters by region', () => {
    document.body.innerHTML = '<div id="a">In</div><div id="b">Out</div>';
    mockRect(document.getElementById('a'), { x: 10, y: 10, width: 50, height: 20 });
    mockRect(document.getElementById('b'), { x: 500, y: 500, width: 50, height: 20 });
    const result = api.extractText({ region: { rx: 0, ry: 0, rw: 100, rh: 100 } });
    expect(result.blocks.length).toBe(1);
    expect(result.blocks[0].text).toBe('In');
  });

  it('truncates text at 500 chars', () => {
    document.body.innerHTML = `<div>${'x'.repeat(600)}</div>`;
    mockAllRects();
    const result = api.extractText({});
    expect(result.blocks[0].text.length).toBe(500);
  });
});

// ============================================================
// findText()
// ============================================================
describe('findText', () => {
  it('finds elements containing query text', () => {
    document.body.innerHTML = '<p>Hello world</p><p>Goodbye</p>';
    mockAllRects();
    const result = api.findText({ query: 'Hello' });
    expect(result.found).toBe(1);
    expect(result.results[0].text).toContain('Hello');
    expect(result.results[0]).toHaveProperty('selector');
  });

  it('case insensitive by default', () => {
    document.body.innerHTML = '<p>HELLO</p>';
    mockAllRects();
    const result = api.findText({ query: 'hello' });
    expect(result.found).toBe(1);
  });

  it('respects caseSensitive flag', () => {
    document.body.innerHTML = '<p>HELLO</p>';
    mockAllRects();
    const result = api.findText({ query: 'hello', caseSensitive: true });
    expect(result.found).toBe(0);
  });

  it('throws without query', () => {
    expect(() => api.findText({})).toThrow('query is required');
  });

  it('respects max parameter', () => {
    document.body.innerHTML = '<p>match</p><p>match</p><p>match</p>';
    mockAllRects();
    const result = api.findText({ query: 'match', max: 2 });
    expect(result.found).toBe(2);
  });

  it('dedupes by parent element', () => {
    document.body.innerHTML = '<p>foo bar foo</p>';
    mockAllRects();
    const result = api.findText({ query: 'foo' });
    expect(result.found).toBe(1);
  });
});

// ============================================================
// inspectElement()
// ============================================================
describe('inspectElement', () => {
  it('finds by selector and returns full info', () => {
    document.body.innerHTML = '<button id="btn" class="primary" aria-label="Go">Click</button>';
    const btn = document.getElementById('btn');
    mockRect(btn, { x: 10, y: 20, width: 80, height: 30 });

    const result = api.inspectElement({ selector: '#btn' });
    expect(result.tag).toBe('button');
    expect(result.text).toContain('Click');
    expect(result.rect).toEqual({ x: 10, y: 20, w: 80, h: 30 });
    expect(result.selector).toBe('#btn');
    expect(result.attrs).toHaveProperty('id', 'btn');
    expect(result.attrs).toHaveProperty('class', 'primary');
    expect(result.parents).toBeInstanceOf(Array);
  });

  it('finds by coordinates via elementFromPoint', () => {
    document.body.innerHTML = '<div id="target">Here</div>';
    const el = document.getElementById('target');
    mockRect(el, { x: 0, y: 0, width: 100, height: 50 });
    document.elementFromPoint = vi.fn(() => el);

    const result = api.inspectElement({ x: 50, y: 25 });
    expect(result.tag).toBe('div');
    expect(document.elementFromPoint).toHaveBeenCalledWith(50, 25);
  });

  it('finds by text search', () => {
    document.body.innerHTML = '<span>Find me</span>';
    mockAllRects();
    const result = api.inspectElement({ text: 'Find me' });
    expect(result.tag).toBe('span');
  });

  it('throws when element not found', () => {
    expect(() => api.inspectElement({ selector: '#nope' })).toThrow('Element not found');
  });

  it('includes children summary up to depth', () => {
    document.body.innerHTML = '<div id="p"><span>child</span></div>';
    mockAllRects();
    const result = api.inspectElement({ selector: '#p', depth: 2 });
    expect(result.children).toBeInstanceOf(Array);
    expect(result.children[0].tag).toBe('span');
  });

  it('includes nearby siblings', () => {
    document.body.innerHTML = '<div><p>sib1</p><p id="t">target</p><p>sib3</p></div>';
    mockAllRects();
    const result = api.inspectElement({ selector: '#t' });
    expect(result.siblings.length).toBe(2);
  });
});

// ============================================================
// getInteractiveMap()
// ============================================================
describe('getInteractiveMap', () => {
  it('finds buttons, links, inputs', () => {
    document.body.innerHTML = `
      <a href="/home" id="lnk">Home</a>
      <button id="btn">Click</button>
      <input id="inp" type="text" placeholder="Name">
    `;
    mockAllRects();
    const result = api.getInteractiveMap();
    expect(result.elements.length).toBe(3);
    expect(result.viewport).toHaveProperty('w');

    const link = result.elements.find(e => e.tag === 'a');
    expect(link).toHaveProperty('href');

    const input = result.elements.find(e => e.tag === 'input');
    expect(input).toHaveProperty('inputType', 'text');
    expect(input).toHaveProperty('placeholder', 'Name');
  });

  it('includes role-based interactive elements', () => {
    document.body.innerHTML = '<div role="button" id="rb">Role btn</div>';
    mockAllRects();
    const result = api.getInteractiveMap();
    expect(result.elements.length).toBe(1);
    expect(result.elements[0].role).toBe('button');
  });

  it('skips offscreen elements', () => {
    document.body.innerHTML = '<button id="b">ok</button>';
    mockRect(document.getElementById('b'), { x: 0, y: -200, width: 50, height: 20 });
    const result = api.getInteractiveMap();
    expect(result.elements.length).toBe(0);
  });

  it('includes select options', () => {
    document.body.innerHTML = '<select id="s"><option value="a">A</option><option value="b">B</option></select>';
    mockAllRects();
    const result = api.getInteractiveMap();
    const sel = result.elements.find(e => e.tag === 'select');
    expect(sel.options).toHaveLength(2);
    expect(sel.options[0]).toEqual({ v: 'a', t: 'A', s: true }); // first is selected by default
  });

  it('includes disabled state', () => {
    document.body.innerHTML = '<button disabled id="d">No</button>';
    mockAllRects();
    const result = api.getInteractiveMap();
    expect(result.elements[0].disabled).toBe(true);
  });
});

// ============================================================
// queryElements()
// ============================================================
describe('queryElements', () => {
  it('returns matching elements with attributes', () => {
    document.body.innerHTML = '<div class="card" data-id="1">Card</div>';
    mockAllRects();
    const result = api.queryElements({ selector: '.card' });
    expect(result.count).toBe(1);
    expect(result.elements[0].tag).toBe('div');
    expect(result.elements[0].attrs).toHaveProperty('class', 'card');
    expect(result.elements[0].attrs).toHaveProperty('data-id', '1');
  });

  it('respects limit', () => {
    document.body.innerHTML = '<p>a</p><p>b</p><p>c</p>';
    mockAllRects();
    const result = api.queryElements({ selector: 'p', limit: 2 });
    expect(result.count).toBe(3); // total matches
    expect(result.elements.length).toBe(2); // limited output
  });

  it('includes viewport info', () => {
    document.body.innerHTML = '<div>x</div>';
    mockAllRects();
    const result = api.queryElements({ selector: 'div' });
    expect(result.viewport).toHaveProperty('w', 1280);
  });

  it('enriches form fields with structured field metadata', () => {
    document.body.innerHTML =
      '<label for="wf">Workflow ID</label>' +
      '<input id="wf" name="workflowId" type="text" value="abc" required placeholder="enter id">';
    mockAllRects();
    const result = api.queryElements({ selector: '#wf' });
    const field = result.elements[0].field;
    expect(field).toMatchObject({
      type: 'text',
      name: 'workflowId',
      id: 'wf',
      value: 'abc',
      placeholder: 'enter id',
      required: true,
      label: 'Workflow ID',
    });
  });

  it('reports select options in field metadata', () => {
    document.body.innerHTML =
      '<select id="s"><option value="a">A</option><option value="b" selected>B</option></select>';
    mockAllRects();
    const result = api.queryElements({ selector: '#s' });
    expect(result.elements[0].field.type).toBe('select');
    expect(result.elements[0].field.options).toEqual([
      { value: 'a', text: 'A', selected: false },
      { value: 'b', text: 'B', selected: true },
    ]);
  });

  it('omits field metadata for non-form elements', () => {
    document.body.innerHTML = '<div id="d">x</div>';
    mockAllRects();
    const result = api.queryElements({ selector: '#d' });
    expect(result.elements[0].field).toBeUndefined();
  });
});

// ============================================================
// pageCapabilities()
// ============================================================
describe('pageCapabilities', () => {
  it('reports framework, iframe and shadow-root counts', () => {
    document.body.innerHTML = '<div data-reactroot><iframe></iframe></div>';
    const result = api.pageCapabilities();
    expect(result.framework).toBe('react');
    expect(result.iframes_count).toBe(1);
    expect(result.shadow_roots_count).toBe(0);
    expect(result.monaco_present).toBe(false);
  });

  it('counts open shadow roots', () => {
    document.body.innerHTML = '<div id="host"></div>';
    document.getElementById('host').attachShadow({ mode: 'open' });
    const result = api.pageCapabilities();
    expect(result.shadow_roots_count).toBe(1);
  });

  it('falls back to unknown framework', () => {
    document.body.innerHTML = '<div>plain</div>';
    expect(api.pageCapabilities().framework).toBe('unknown');
  });
});

// ============================================================
// clickElement()
// ============================================================
describe('clickElement', () => {
  it('clicks by selector and dispatches events', () => {
    document.body.innerHTML = '<button id="btn">Go</button>';
    const btn = document.getElementById('btn');
    mockRect(btn, { x: 10, y: 10, width: 80, height: 30 });

    const events = [];
    btn.addEventListener('mousedown', () => events.push('mousedown'));
    btn.addEventListener('mouseup', () => events.push('mouseup'));
    btn.addEventListener('click', () => events.push('click'));

    const result = api.clickElement({ selector: '#btn' });
    expect(events).toEqual(['mousedown', 'mouseup', 'click']);
    expect(result).toHaveProperty('clicked');
    expect(result.x).toBe(50); // center of 10 + 80/2
    expect(result.y).toBe(25); // center of 10 + 30/2
  });

  it('clicks by coordinates via elementFromPoint', () => {
    document.body.innerHTML = '<div id="d">x</div>';
    const el = document.getElementById('d');
    mockRect(el, { x: 0, y: 0, width: 100, height: 50 });
    document.elementFromPoint = vi.fn(() => el);

    const result = api.clickElement({ x: 50, y: 25 });
    expect(document.elementFromPoint).toHaveBeenCalledWith(50, 25);
    expect(result).toHaveProperty('clicked');
  });

  it('focuses an input it clicks so the field is actually active', () => {
    document.body.innerHTML = '<input id="inp" type="text">';
    const inp = document.getElementById('inp');
    mockRect(inp, { x: 0, y: 0, width: 100, height: 20 });

    const result = api.clickElement({ selector: '#inp' });
    expect(document.activeElement).toBe(inp);
    expect(result.focused).toBe(true);
  });

  it('throws when selector not found', () => {
    expect(() => api.clickElement({ selector: '#nope' })).toThrow('Element not found');
  });

  it('throws when no element at coordinates', () => {
    document.elementFromPoint = vi.fn(() => null);
    expect(() => api.clickElement({ x: 9999, y: 9999 })).toThrow('No element at');
  });

  it('throws without selector or coordinates', () => {
    expect(() => api.clickElement({})).toThrow('Provide selector or x,y');
  });
});

// ============================================================
// prepareForUserClick() — honest handoff
// ============================================================
describe('prepareForUserClick', () => {
  beforeEach(() => {
    // jsdom's scrollIntoView is a no-op stub — spy on it to assert calls.
    Element.prototype.scrollIntoView = vi.fn();
  });

  it('resolves a selector, scrolls into view, and reports the bbox', async () => {
    document.body.innerHTML = '<button id="allow">Allow</button>';
    const btn = document.getElementById('allow');
    mockRect(btn, { x: 100, y: 200, width: 96, height: 40 });
    document.elementFromPoint = vi.fn(() => btn);

    const result = await api.prepareForUserClick({
      selector: '#allow', hint: 'Click Allow to grant access', label: 'Allow',
    });

    expect(btn.scrollIntoView).toHaveBeenCalledWith(
      expect.objectContaining({ block: 'center', behavior: 'smooth' }),
    );
    expect(result.found).toBe(true);
    expect(result.bbox).toEqual({ x: 100, y: 200, width: 96, height: 40 });
    expect(result.label).toBe('Allow');
  });

  it('computes inViewport=false for an off-screen target', async () => {
    document.body.innerHTML = '<button id="b">x</button>';
    const btn = document.getElementById('b');
    // Beyond the 1280x800 viewport.
    mockRect(btn, { x: 100, y: 2000, width: 80, height: 30 });
    document.elementFromPoint = vi.fn(() => btn);

    const result = await api.prepareForUserClick({ selector: '#b', hint: 'scroll down' });
    expect(result.found).toBe(true);
    expect(result.inViewport).toBe(false);
  });

  it('computes occluded=true when another element covers the centre', async () => {
    document.body.innerHTML = '<button id="b">x</button><div id="cover">cookie banner</div>';
    const btn = document.getElementById('b');
    const cover = document.getElementById('cover');
    mockRect(btn, { x: 0, y: 0, width: 100, height: 40 });
    document.elementFromPoint = vi.fn(() => cover); // something else on top

    const result = await api.prepareForUserClick({ selector: '#b', hint: 'dismiss the banner first' });
    expect(result.occluded).toBe(true);
  });

  it('flags ambiguous when the selector matches multiple elements', async () => {
    document.body.innerHTML = '<button class="x">a</button><button class="x">b</button>';
    document.querySelectorAll('.x').forEach(el => mockRect(el, { x: 0, y: 0, width: 50, height: 20 }));
    document.elementFromPoint = vi.fn(() => document.querySelector('.x'));

    const result = await api.prepareForUserClick({ selector: '.x', hint: 'click the first' });
    expect(result.ambiguous).toBe(true);
    expect(result.found).toBe(true);
  });

  it('returns found=false for a missing selector', async () => {
    const result = await api.prepareForUserClick({ selector: '#nope', hint: 'click it' });
    expect(result.found).toBe(false);
    expect(result.reasonDetail).toBe('no_target');
  });

  it('supports coordinate mode (no element to scroll)', async () => {
    const result = await api.prepareForUserClick({ x: 300, y: 450, hint: 'click the captcha' });
    expect(result.found).toBe(true);
    expect(result.bbox.width).toBe(32);
    expect(result.occluded).toBe(false);
  });
});

// ============================================================
// handoff overlay — persistent banner + highlight
// ============================================================
describe('handoff overlay', () => {
  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn();
    // The overlay host is a singleton created lazily by ensure() — don't
    // remove it between tests (the module-level `root` ref would go stale).
    // clearHandoff() inside showHandoff already tears down prior handoffs.
    api.overlay.clearHandoff();
  });

  // Reach the overlay's shadow tree via the stashed host (see beforeAll) —
  // robust even after document.body has been wiped between tests.
  function overlayRoot() {
    const host = globalThis.__overlayHost;
    return host && host.shadowRoot ? host.shadowRoot : null;
  }

  it('handoff event renders a banner and a persistent highlight', async () => {
    document.body.innerHTML = '<button id="b">Go</button>';
    const btn = document.getElementById('b');
    mockRect(btn, { x: 10, y: 20, width: 96, height: 40 });

    await api.overlay.showHandoff({
      el: btn, x: 58, y: 40,
      bbox: { left: 10, top: 20, width: 96, height: 40 },
      hint: 'Click Allow to continue', label: 'Allow',
    });

    const sr = overlayRoot();
    expect(sr).not.toBeNull();
    const banner = sr.querySelector('.handoff-banner');
    expect(banner).not.toBeNull();
    expect(banner.querySelector('.hb-title').textContent).toBe('Allow');
    expect(banner.querySelector('.hb-hint').textContent).toBe('Click Allow to continue');
    // Persistent highlight: the breathing variant, with NO fade timer.
    const hl = sr.querySelector('.highlight.persist');
    expect(hl).not.toBeNull();
  });

  it('escapes a hint containing HTML / script', async () => {
    await api.overlay.showHandoff({
      el: null, x: 100, y: 100,
      bbox: { left: 90, top: 90, width: 20, height: 20 },
      hint: '<script>alert(1)</script>', label: '<b>x</b>',
    });
    const sr = overlayRoot();
    const hint = sr.querySelector('.handoff-banner .hb-hint');
    // The raw text is preserved; no live <script> node injected.
    expect(hint.textContent).toBe('<script>alert(1)</script>');
    expect(hint.querySelector('script')).toBeNull();
    expect(sr.querySelector('.handoff-banner .hb-title').querySelector('b')).toBeNull();
  });

  it('handoff_clear removes the banner and highlight', async () => {
    await api.overlay.showHandoff({
      el: null, x: 50, y: 50,
      bbox: { left: 40, top: 40, width: 20, height: 20 },
      hint: 'do the thing',
    });
    let sr = overlayRoot();
    expect(sr.querySelector('.handoff-banner')).not.toBeNull();

    api.overlay.clearHandoff();
    // The highlight is removed synchronously; the banner removal is on a
    // short exit timer, but it loses its `.on` class immediately.
    expect(sr.querySelector('.highlight.persist')).toBeNull();
    expect(sr.querySelector('.handoff-banner.on')).toBeNull();
  });

  it('a second handoff replaces the first (newest-wins)', async () => {
    await api.overlay.showHandoff({
      el: null, x: 50, y: 50,
      bbox: { left: 40, top: 40, width: 20, height: 20 },
      hint: 'first handoff', label: 'First',
    });
    await api.overlay.showHandoff({
      el: null, x: 200, y: 200,
      bbox: { left: 190, top: 190, width: 20, height: 20 },
      hint: 'second handoff', label: 'Second',
    });
    const sr = overlayRoot();
    const banners = sr.querySelectorAll('.handoff-banner.on');
    // Only the newest banner is active; the replaced one is exiting.
    expect(banners.length).toBe(1);
    expect(banners[0].querySelector('.hb-title').textContent).toBe('Second');
  });
});

// ============================================================
// typeText()
// ============================================================
describe('typeText', () => {
  it('types into element by selector', () => {
    document.body.innerHTML = '<input id="inp" type="text">';
    const inp = document.getElementById('inp');
    const focusSpy = vi.spyOn(inp, 'focus');

    const result = api.typeText({ selector: '#inp', text: 'hello' });
    expect(focusSpy).toHaveBeenCalled();
    expect(inp.value).toBe('hello');
    expect(result).toEqual({ typed: 5, verified: true, value: 'hello', element: '#inp' });
  });

  it('dispatches an input event so framework-controlled inputs react', () => {
    document.body.innerHTML = '<input id="inp" type="text">';
    const inp = document.getElementById('inp');
    const events = [];
    inp.addEventListener('input', () => events.push('input'));
    inp.addEventListener('change', () => events.push('change'));

    api.typeText({ selector: '#inp', text: 'hi' });
    expect(events).toEqual(['input', 'change']);
  });

  it('reports verified:false when the value does not stick', () => {
    document.body.innerHTML = '<input id="inp" type="text">';
    const inp = document.getElementById('inp');
    // Simulate a framework that owns the value and reverts it: an instance
    // getter that always reads empty. typeText writes through the prototype
    // setter, but its verify step reads back through this getter.
    Object.defineProperty(inp, 'value', {
      get: () => '', set: () => {}, configurable: true,
    });

    const result = api.typeText({ selector: '#inp', text: 'nope' });
    expect(result.verified).toBe(false);
    expect(result.hint).toBeTruthy();
  });

  it('types into active element when no selector', () => {
    document.body.innerHTML = '<input id="inp">';
    const inp = document.getElementById('inp');
    inp.focus();
    const result = api.typeText({ text: 'x' });
    expect(result.typed).toBe(1);
  });

  it('replaces the value when clear=true', () => {
    document.body.innerHTML = '<input id="inp" value="old">';
    const inp = document.getElementById('inp');

    const result = api.typeText({ selector: '#inp', text: 'new', clear: true });
    expect(inp.value).toBe('new');
    expect(result.verified).toBe(true);
  });

  it('appends to the existing value when clear is not set', () => {
    document.body.innerHTML = '<input id="inp" value="ab">';
    const inp = document.getElementById('inp');

    api.typeText({ selector: '#inp', text: 'cd' });
    expect(inp.value).toBe('abcd');
  });

  it('dispatches enter key events when pressEnter=true', () => {
    document.body.innerHTML = '<input id="inp">';
    const inp = document.getElementById('inp');
    const events = [];
    inp.addEventListener('keydown', (e) => events.push(['keydown', e.key]));
    inp.addEventListener('keypress', (e) => events.push(['keypress', e.key]));
    inp.addEventListener('keyup', (e) => events.push(['keyup', e.key]));

    api.typeText({ selector: '#inp', text: 'x', pressEnter: true });
    expect(events).toEqual([
      ['keydown', 'Enter'],
      ['keypress', 'Enter'],
      ['keyup', 'Enter'],
    ]);
  });

  it('throws when element not found', () => {
    expect(() => api.typeText({ selector: '#nope', text: 'x' })).toThrow('Element not found');
  });

  it('uses selectAll for contenteditable clear', () => {
    document.body.innerHTML = '<div id="ce" contenteditable="true">old</div>';
    const el = document.getElementById('ce');
    const execSpy = vi.spyOn(document, 'execCommand').mockReturnValue(true);

    api.typeText({ selector: '#ce', text: 'new', clear: true });
    expect(execSpy).toHaveBeenCalledWith('selectAll');
    expect(execSpy).toHaveBeenCalledWith('delete');
    execSpy.mockRestore();
  });
});

// ============================================================
// scrollPage()
// ============================================================
describe('scrollPage', () => {
  it('scrolls by direction', () => {
    api.scrollPage({ direction: 'down' });
    expect(window.scrollBy).toHaveBeenCalledWith({
      left: 0, top: 640, behavior: 'instant',
    });
  });

  it('scrolls up', () => {
    api.scrollPage({ direction: 'up' });
    expect(window.scrollBy).toHaveBeenCalledWith({
      left: 0, top: -640, behavior: 'instant',
    });
  });

  it('scrolls with custom amount', () => {
    api.scrollPage({ direction: 'down', amount: 200 });
    expect(window.scrollBy).toHaveBeenCalledWith({
      left: 0, top: 200, behavior: 'instant',
    });
  });

  it('scrolls left/right', () => {
    api.scrollPage({ direction: 'left' });
    expect(window.scrollBy).toHaveBeenCalledWith({
      left: -640, top: 0, behavior: 'instant',
    });
  });

  it('scrolls by raw x/y', () => {
    api.scrollPage({ x: 50, y: 100 });
    expect(window.scrollBy).toHaveBeenCalledWith({
      left: 50, top: 100, behavior: 'instant',
    });
  });

  it('scrolls a specific element by selector', () => {
    document.body.innerHTML = '<div id="box" style="overflow:auto;height:100px">tall content</div>';
    const box = document.getElementById('box');
    box.scrollBy = vi.fn();
    api.scrollPage({ selector: '#box', y: 50 });
    expect(box.scrollBy).toHaveBeenCalledWith({ left: 0, top: 50, behavior: 'instant' });
  });

  it('throws for invalid selector', () => {
    expect(() => api.scrollPage({ selector: '#nope', y: 10 })).toThrow('Element not found');
  });
});

// ============================================================
// getHTML()
// ============================================================
describe('getHTML', () => {
  it('returns innerHTML by default', () => {
    document.body.innerHTML = '<div id="d"><span>hi</span></div>';
    const result = api.getHTML({ selector: '#d' });
    expect(result.html).toBe('<span>hi</span>');
    expect(result.truncated).toBe(false);
  });

  it('returns outerHTML when outer=true', () => {
    document.body.innerHTML = '<div id="d"><span>hi</span></div>';
    const result = api.getHTML({ selector: '#d', outer: true });
    expect(result.html).toContain('<div id="d">');
    expect(result.html).toContain('</div>');
  });

  it('uses document.documentElement when no selector', () => {
    document.body.innerHTML = '<p>test</p>';
    const result = api.getHTML({});
    expect(result.html).toContain('<p>test</p>');
  });

  it('truncates at maxLength', () => {
    document.body.innerHTML = `<div>${'x'.repeat(2000)}</div>`;
    const result = api.getHTML({ selector: 'div', maxLength: 1000 });
    expect(result.html.length).toBe(1000);
    expect(result.truncated).toBe(true);
  });

  it('clamps maxLength to at least 1000', () => {
    document.body.innerHTML = `<div>${'x'.repeat(2000)}</div>`;
    const result = api.getHTML({ selector: 'div', maxLength: 10 });
    expect(result.html.length).toBe(1000);
  });

  it('throws for invalid selector', () => {
    expect(() => api.getHTML({ selector: '#nope' })).toThrow('Element not found');
  });
});

// ============================================================
// depthLimitedHTML()
// ============================================================
describe('depthLimitedHTML', () => {
  it('returns full HTML within depth limit', () => {
    document.body.innerHTML = '<div id="d"><span>hi</span></div>';
    const el = document.getElementById('d');
    const result = api.depthLimitedHTML(el, 3, false);
    expect(result).toContain('<span');
    expect(result).toContain('hi');
  });

  it('truncates children beyond maxDepth', () => {
    // span at depth 0 recurses; b at depth 1 hits maxDepth and has child <i>, so truncated
    document.body.innerHTML = '<div id="d"><span><b><i>deep</i></b></span></div>';
    const el = document.getElementById('d');
    const result = api.depthLimitedHTML(el, 1, false);
    expect(result).toContain('[1 children]');
  });

  it('returns outerHTML when outer=true', () => {
    document.body.innerHTML = '<div id="d">text</div>';
    const el = document.getElementById('d');
    const result = api.depthLimitedHTML(el, 2, true);
    expect(result).toMatch(/^<div/);
  });

  it('depth-limited via getHTML maxDepth', () => {
    document.body.innerHTML = '<div id="d"><span><b><i>deep</i></b></span></div>';
    const result = api.getHTML({ selector: '#d', maxDepth: 1 });
    expect(result.html).toContain('[1 children]');
  });
});

// ============================================================
// getPageStructure() — YAML output
// ============================================================
describe('getPageStructure', () => {
  it('returns YAML with page metadata', () => {
    document.body.innerHTML = '<h1>Title</h1>';
    mockAllRects();
    document.title = 'Test Page';
    const result = api.getPageStructure({});
    expect(result.yaml).toContain('page:');
    expect(result.yaml).toContain('title:');
    expect(result.yaml).toContain('viewport:');
    expect(result.viewport).toHaveProperty('w');
  });

  it('represents headings', () => {
    document.body.innerHTML = '<h2>Section</h2>';
    mockAllRects();
    const result = api.getPageStructure({});
    expect(result.yaml).toContain('h2:');
    expect(result.yaml).toContain('Section');
  });

  it('represents buttons', () => {
    document.body.innerHTML = '<button>Go</button>';
    mockAllRects();
    const result = api.getPageStructure({});
    expect(result.yaml).toContain('button:');
    expect(result.yaml).toContain('Go');
  });

  it('represents links', () => {
    document.body.innerHTML = '<a href="/home">Home</a>';
    mockAllRects();
    const result = api.getPageStructure({});
    expect(result.yaml).toContain('link:');
    expect(result.yaml).toContain('Home');
  });

  it('represents inputs', () => {
    document.body.innerHTML = '<input type="email" name="e" placeholder="Enter">';
    mockAllRects();
    const result = api.getPageStructure({});
    expect(result.yaml).toContain('input:');
    expect(result.yaml).toContain('type: email');
  });

  it('represents tables', () => {
    document.body.innerHTML = '<table><tr><th>Name</th></tr><tr><td>Alice</td></tr></table>';
    mockAllRects();
    const result = api.getPageStructure({});
    expect(result.yaml).toContain('table:');
    expect(result.yaml).toContain('Name');
  });

  it('represents lists', () => {
    document.body.innerHTML = '<ul><li>One</li><li>Two</li></ul>';
    mockAllRects();
    const result = api.getPageStructure({});
    expect(result.yaml).toContain('list:');
  });

  it('skips script/style/noscript tags', () => {
    document.body.innerHTML = '<script>alert(1)</script><style>body{}</style><p>visible</p>';
    mockAllRects();
    const result = api.getPageStructure({});
    expect(result.yaml).not.toContain('alert');
    expect(result.yaml).toContain('visible');
  });

  it('scopes to selector', () => {
    document.body.innerHTML = '<div id="a">Outside</div><section id="s"><p>Inside</p></section>';
    mockAllRects();
    const result = api.getPageStructure({ selector: '#s' });
    expect(result.yaml).toContain('Inside');
    // "Outside" won't appear because it's not in #s
  });

  it('throws for invalid selector', () => {
    expect(() => api.getPageStructure({ selector: '#nope' })).toThrow('Element not found');
  });
});

// ============================================================
// executeJS()
// ============================================================
describe('executeJS', () => {
  it('evaluates JS and returns result', () => {
    const result = api.executeJS({ code: '2 + 2' });
    expect(result).toEqual({ result: 4 });
  });

  it('returns null for undefined result', () => {
    const result = api.executeJS({ code: 'void 0' });
    expect(result).toEqual({ result: null });
  });

  it('returns error for bad code', () => {
    const result = api.executeJS({ code: 'throw new Error("boom")' });
    expect(result).toHaveProperty('error', 'boom');
    expect(result).toHaveProperty('stack');
  });
});

// ============================================================
// Message handler routing
// ============================================================
describe('message handler', () => {
  it('responds to ping', () => {
    const sendResponse = vi.fn();
    messageHandler({ action: 'ping' }, null, sendResponse);
    expect(sendResponse).toHaveBeenCalledWith({ ok: true });
  });

  it('routes extract_text to extractText', () => {
    document.body.innerHTML = '<div>test</div>';
    mockAllRects();
    const sendResponse = vi.fn();
    messageHandler({ action: 'extract_text', params: {} }, null, sendResponse);
    expect(sendResponse).toHaveBeenCalled();
    const result = sendResponse.mock.calls[0][0];
    expect(result).toHaveProperty('blocks');
  });

  it('routes execute_js_isolated to executeJS', () => {
    const sendResponse = vi.fn();
    messageHandler({ action: 'execute_js_isolated', params: { code: '1+1' } }, null, sendResponse);
    expect(sendResponse).toHaveBeenCalledWith({ result: 2 });
  });

  it('returns error for unknown action', () => {
    const sendResponse = vi.fn();
    messageHandler({ action: 'nonexistent' }, null, sendResponse);
    expect(sendResponse).toHaveBeenCalledWith({ error: expect.stringContaining('Unknown action') });
  });

  it('catches errors and returns them', () => {
    const sendResponse = vi.fn();
    messageHandler({ action: 'find_text', params: {} }, null, sendResponse);
    expect(sendResponse).toHaveBeenCalledWith({ error: expect.stringContaining('query is required') });
  });

  it('returns true for async handlers (inject_script)', () => {
    document.body.innerHTML = '<div>x</div>';
    const sendResponse = vi.fn();
    const result = messageHandler({ action: 'inject_script', params: { code: 'return 1' } }, null, sendResponse);
    expect(result).toBe(true); // signals async response
  });
});

describe('inspectForm', () => {
  it('returns field descriptors with resolved labels, values and flags', () => {
    document.body.innerHTML =
      '<form action="/submit" method="post">' +
      '  <label for="wf">Workflow ID</label>' +
      '  <input id="wf" name="workflowId" type="text" value="order-1" required>' +
      '  <input name="taskQueue" type="text" aria-label="Task Queue">' +
      '  <input name="token" type="password">' +
      '  <input name="locked" type="text" disabled>' +
      '  <select name="region"><option value="eu">EU</option></select>' +
      '  <textarea name="notes">hello</textarea>' +
      '  <input type="hidden" name="csrf" value="xyz">' +
      '</form>';
    mockAllRects();
    const result = api.inspectForm({});

    expect(result.form.action).toBe('/submit');
    expect(result.form.method).toBe('post');

    // Hidden input is excluded; the 6 user-fillable fields remain.
    const names = result.fields.map(f => f.name);
    expect(names).toEqual(['workflowId', 'taskQueue', 'token', 'locked', 'region', 'notes']);

    const wf = result.fields.find(f => f.name === 'workflowId');
    expect(wf).toMatchObject({
      tag: 'input', type: 'text', id: 'wf',
      label: 'Workflow ID', value: 'order-1', required: true,
    });

    const tq = result.fields.find(f => f.name === 'taskQueue');
    expect(tq.ariaLabel).toBe('Task Queue');

    const token = result.fields.find(f => f.name === 'token');
    expect(token.type).toBe('password');

    const locked = result.fields.find(f => f.name === 'locked');
    expect(locked.disabled).toBe(true);

    const notes = result.fields.find(f => f.name === 'notes');
    expect(notes).toMatchObject({ tag: 'textarea', value: 'hello' });
  });

  it('reads contenteditable value from textContent (typeText parity)', () => {
    document.body.innerHTML =
      '<form><div contenteditable="true" id="ce">live text</div></form>';
    mockAllRects();
    const result = api.inspectForm({});
    const ce = result.fields[0];
    expect(ce.type).toBe('contenteditable');
    expect(ce.value).toBe('live text');
    expect(ce.frameworkControlled).toBe(true);
  });

  it('picks the largest form when multiple forms exist', () => {
    document.body.innerHTML =
      '<form id="small"><input name="a"></form>' +
      '<form id="big"><input name="b"><input name="c"><input name="d"></form>';
    mockAllRects();
    const result = api.inspectForm({});
    expect(result.fields.map(f => f.name)).toEqual(['b', 'c', 'd']);
  });

  it('honors an explicit formSelector', () => {
    document.body.innerHTML =
      '<form id="small"><input name="a"></form>' +
      '<form id="big"><input name="b"><input name="c"></form>';
    mockAllRects();
    const result = api.inspectForm({ selector: '#small' });
    expect(result.fields.map(f => f.name)).toEqual(['a']);
  });

  it('throws when an explicit selector matches nothing', () => {
    document.body.innerHTML = '<form><input name="a"></form>';
    expect(() => api.inspectForm({ selector: '#missing' })).toThrow(/not found/);
  });

  it('falls back to the document when the page has no <form>', () => {
    document.body.innerHTML =
      '<div><input name="bare1"><input name="bare2"></div>';
    mockAllRects();
    const result = api.inspectForm({});
    expect(result.form.selector).toBe(''); // no form element
    expect(result.fields.map(f => f.name)).toEqual(['bare1', 'bare2']);
  });
});

// ============================================================
// drag_drop_file — non-CDP file upload
// ============================================================
describe('drag_drop_file', () => {
  // jsdom 26 ships no DataTransfer/DragEvent. Polyfill just enough for
  // __turboPerformDrop: a DataTransfer whose items.add builds a .files
  // list, and a DragEvent that carries clientX/clientY like a real one.
  beforeAll(() => {
    if (typeof globalThis.DataTransfer === 'undefined') {
      globalThis.DataTransfer = class DataTransfer {
        constructor() { this._files = []; this.items = { add: (f) => this._files.push(f) }; }
        get files() { return this._files; }
      };
    }
    if (typeof globalThis.DragEvent === 'undefined') {
      globalThis.DragEvent = class DragEvent extends Event {
        constructor(type, init = {}) {
          super(type, init);
          this.clientX = init.clientX || 0;
          this.clientY = init.clientY || 0;
        }
      };
    }
  });

  beforeEach(() => {
    document.body.innerHTML = '';
    globalThis.fetch = undefined;
  });

  // --- __turboPerformDrop: drop-zone path ---
  it('dispatches dragenter/dragover/drop in order on a drop zone', async () => {
    document.body.innerHTML = '<div class="dropzone">Drop here</div>';
    const zone = document.querySelector('.dropzone');
    mockRect(zone, { x: 10, y: 10, width: 200, height: 100 });

    const seen = [];
    let droppedFile = null;
    for (const k of ['dragenter', 'dragover', 'drop']) {
      zone.addEventListener(k, (ev) => {
        seen.push(ev.type);
        if (ev.type === 'drop') droppedFile = ev.dataTransfer.files[0];
      });
    }

    const blob = new Blob(['report-bytes'], { type: 'application/pdf' });
    const result = await api.__turboPerformDrop(zone, blob, 'report.pdf', 'application/pdf');

    expect(seen).toEqual(['dragenter', 'dragover', 'drop']);
    expect(result.method).toBe('drop');
    expect(result.events_dispatched).toEqual(['dragenter', 'dragover', 'drop']);
    expect(droppedFile).toBeInstanceOf(File);
    expect(droppedFile.name).toBe('report.pdf');
    expect(droppedFile.type).toBe('application/pdf');
  });

  it('attaches a DataTransfer carrying the File to the drop event', async () => {
    document.body.innerHTML = '<div id="z"></div>';
    const zone = document.querySelector('#z');
    mockRect(zone, { x: 0, y: 0, width: 100, height: 100 });

    let dt = null;
    zone.addEventListener('drop', (ev) => { dt = ev.dataTransfer; });
    const blob = new Blob(['abc']);
    await api.__turboPerformDrop(zone, blob, 'a.txt', 'text/plain');

    expect(dt).not.toBeNull();
    expect(dt.files.length).toBe(1);
    expect(dt.files[0].name).toBe('a.txt');
  });

  // --- __turboPerformDrop: input.files path (§3.5) ---
  it('assigns input.files and fires input/change for an <input type=file>', async () => {
    document.body.innerHTML = '<input type="file" id="f">';
    const input = document.querySelector('#f');
    mockRect(input, { x: 0, y: 0, width: 80, height: 20 });

    // In a real browser DataTransfer.prototype.files IS a FileList and
    // HTMLInputElement.files accepts it directly. jsdom has no DataTransfer
    // and its input.files setter strictly type-checks FileList, so we make
    // the property writable for this test to exercise the assignment +
    // event-dispatch logic of __turboPerformDrop.
    let assigned = null;
    Object.defineProperty(input, 'files', {
      get: () => assigned,
      set: (v) => { assigned = v; },
      configurable: true,
    });

    const events = [];
    input.addEventListener('input', () => events.push('input'));
    input.addEventListener('change', () => events.push('change'));

    const blob = new Blob(['xyz'], { type: 'image/png' });
    const result = await api.__turboPerformDrop(input, blob, 'pic.png', 'image/png');

    expect(result.method).toBe('input.files');
    expect(result.events_dispatched).toEqual(['input', 'change']);
    expect(events).toEqual(['input', 'change']);
    expect(input.files.length).toBe(1);
    expect(input.files[0].name).toBe('pic.png');
  });

  // --- reaction warning (honest failure surfacing) ---
  it('warns when the drop zone handler does not react', async () => {
    document.body.innerHTML = '<div id="inert"></div>';
    const zone = document.querySelector('#inert');
    mockRect(zone, { x: 0, y: 0, width: 50, height: 50 });
    // No listener: nothing mutates the DOM and dragover/drop are never
    // preventDefault()-ed, so BOTH honest-failure warnings fire.
    const result = await api.__turboPerformDrop(zone, new Blob(['x']), 'x.txt', 'text/plain');
    expect(result.warnings.length).toBeGreaterThan(0);
    expect(result.warnings.some((w) => /did not appear to react/.test(w))).toBe(true);
    expect(result.warnings.some((w) => /did not preventDefault/.test(w))).toBe(true);
  });

  it('warns when the drop zone does not preventDefault the drop', async () => {
    document.body.innerHTML = '<div id="live"></div>';
    const zone = document.querySelector('#live');
    mockRect(zone, { x: 0, y: 0, width: 50, height: 50 });
    // The handler mutates the DOM (so the reaction probe is satisfied) but
    // never calls preventDefault() — the page did NOT accept the drop, so
    // the defaultPrevented warning must still fire.
    zone.addEventListener('drop', () => {
      zone.appendChild(document.createElement('span')); // observable mutation
    });
    const result = await api.__turboPerformDrop(zone, new Blob(['x']), 'x.txt', 'text/plain');
    expect(result.warnings.some((w) => /did not preventDefault/.test(w))).toBe(true);
  });

  it('does not warn when the drop zone accepts the drop', async () => {
    document.body.innerHTML = '<div id="ok"></div>';
    const zone = document.querySelector('#ok');
    mockRect(zone, { x: 0, y: 0, width: 50, height: 50 });
    // A real drop zone preventDefault()s dragover/drop AND mutates the DOM —
    // it accepted the payload, so neither honest-failure warning fires.
    zone.addEventListener('dragover', (ev) => ev.preventDefault());
    zone.addEventListener('drop', (ev) => {
      ev.preventDefault();
      zone.appendChild(document.createElement('span')); // observable mutation
    });
    const result = await api.__turboPerformDrop(zone, new Blob(['x']), 'x.txt', 'text/plain');
    expect(result.warnings.length).toBe(0);
  });

  // --- dragDropFile: pre-flight validation & fetch ---
  it('rejects a missing selector', async () => {
    await expect(api.dragDropFile({ fileName: 'a.txt' }))
      .rejects.toThrow(/selector is required/);
  });

  it('rejects when no element matches the selector', async () => {
    await expect(api.dragDropFile({
      selector: '.nope', fileName: 'a.txt', fileHostPort: 18323, fileToken: 'tok',
    })).rejects.toThrow(/No element matches selector/);
  });

  it('rejects a zero-size target element', async () => {
    document.body.innerHTML = '<div class="hidden"></div>';
    mockRect(document.querySelector('.hidden'), { x: 0, y: 0, width: 0, height: 0 });
    await expect(api.dragDropFile({
      selector: '.hidden', fileName: 'a.txt', fileHostPort: 18323, fileToken: 'tok',
    })).rejects.toThrow(/zero-size bounding box/);
  });

  it('surfaces an unreachable file host', async () => {
    document.body.innerHTML = '<div class="dz"></div>';
    mockRect(document.querySelector('.dz'), { x: 0, y: 0, width: 100, height: 100 });
    globalThis.fetch = vi.fn().mockRejectedValue(new Error('network down'));
    await expect(api.dragDropFile({
      selector: '.dz', fileName: 'a.txt', fileHostPort: 18323, fileToken: 'tok',
    })).rejects.toThrow(/could not reach the loopback file host/);
  });

  it('surfaces an HTTP error from the file host (expired token)', async () => {
    document.body.innerHTML = '<div class="dz"></div>';
    mockRect(document.querySelector('.dz'), { x: 0, y: 0, width: 100, height: 100 });
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: false, status: 410 });
    await expect(api.dragDropFile({
      selector: '.dz', fileName: 'a.txt', fileHostPort: 18323, fileToken: 'tok',
    })).rejects.toThrow(/HTTP 410/);
  });

  it('fetches the loopback file host with the token in the path', async () => {
    document.body.innerHTML = '<div class="dz"></div>';
    mockRect(document.querySelector('.dz'), { x: 0, y: 0, width: 100, height: 100 });
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true, status: 200, blob: async () => new Blob(['bytes']),
    });
    // runDropInPage will not resolve here (the injected MAIN-world <script>
    // does not execute under jsdom), so just assert the fetch URL shape.
    api.dragDropFile({
      selector: '.dz', fileName: 'a.txt', mimeType: 'text/plain',
      size: 5, fileHostPort: 18399, fileToken: 'abc123',
    }).catch(() => {});
    await new Promise(r => setTimeout(r, 0));
    expect(globalThis.fetch).toHaveBeenCalledWith('http://127.0.0.1:18399/file/abc123');
  });

  it('routes drag_drop_file through the message handler', () => {
    document.body.innerHTML = '<div class="dz"></div>';
    mockRect(document.querySelector('.dz'), { x: 0, y: 0, width: 100, height: 100 });
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true, status: 200, blob: async () => new Blob(['bytes']),
    });
    const sendResponse = vi.fn();
    const r = messageHandler(
      { action: 'drag_drop_file', params: { selector: '.dz', fileName: 'a.txt', fileHostPort: 1, fileToken: 't' } },
      null, sendResponse,
    );
    expect(r).toBe(true); // async handler
  });
});

// ============================================================
// DOM-mutation signal — domMutationsMark / domMutationsSince /
// screenshotDiffMeta (the MutationObserver complement to the pixel diff)
// ============================================================

// flushMutations yields long enough for the MutationObserver callback —
// which jsdom delivers on a microtask — to run.
const flushMutations = () => new Promise((r) => setTimeout(r, 0));

describe('dom_mutations signal', () => {
  it('mark returns an opaque token', () => {
    const { token } = api.domMutationsMark();
    expect(typeof token).toBe('string');
    expect(token.length).toBeGreaterThan(0);
  });

  it('counts childList additions since a mark', async () => {
    const { token } = api.domMutationsMark();
    document.body.appendChild(document.createElement('div'));
    document.body.appendChild(document.createElement('span'));
    await flushMutations();

    const dom = api.domMutationsSince({ since: token });
    expect(dom.mutations).toBeGreaterThan(0);
    expect(dom.added).toBeGreaterThanOrEqual(2);
  });

  it('counts attribute mutations since a mark', async () => {
    const el = document.createElement('div');
    document.body.appendChild(el);
    await flushMutations();

    const { token } = api.domMutationsMark();
    el.setAttribute('data-x', '1');
    el.setAttribute('data-y', '2');
    await flushMutations();

    const dom = api.domMutationsSince({ since: token });
    expect(dom.attrs).toBeGreaterThanOrEqual(2);
  });

  it('reports zero mutations for a quiet window', async () => {
    await flushMutations();
    const { token } = api.domMutationsMark();
    await flushMutations(); // nothing mutated

    const dom = api.domMutationsSince({ since: token });
    expect(dom.mutations).toBe(0);
    expect(dom.added).toBe(0);
  });

  it('ignores mutations inside the TurboWeb overlay subtree', async () => {
    // Simulate the overlay host the content script injects.
    const host = document.createElement('div');
    host.id = '__turbo_overlay_host';
    document.body.appendChild(host);
    await flushMutations();

    const { token } = api.domMutationsMark();
    // Mutating INSIDE the overlay must not count — the agent cursor and
    // intent toast animate constantly and would otherwise inflate counts.
    host.appendChild(document.createElement('div'));
    host.appendChild(document.createElement('span'));
    await flushMutations();

    const dom = api.domMutationsSince({ since: token });
    expect(dom.mutations).toBe(0);
    expect(dom.added).toBe(0);
  });

  it('still counts real page mutations alongside ignored overlay ones', async () => {
    const host = document.createElement('div');
    host.id = '__turbo_overlay_host';
    document.body.appendChild(host);
    await flushMutations();

    const { token } = api.domMutationsMark();
    host.appendChild(document.createElement('i'));        // ignored
    document.body.appendChild(document.createElement('p')); // real
    await flushMutations();

    const dom = api.domMutationsSince({ since: token });
    expect(dom.added).toBe(1);
  });
});

describe('screenshot_diff_meta', () => {
  it('resolves ignore selectors to bounding boxes', () => {
    document.body.innerHTML = '<div id="clock">12:00</div>';
    const el = document.getElementById('clock');
    mockRect(el, { x: 10, y: 20, width: 80, height: 30 });

    const meta = api.screenshotDiffMeta({ ignore: ['#clock'] });
    const clockMask = meta.masks.find((m) => m.x === 10 && m.y === 20);
    expect(clockMask).toBeTruthy();
    expect(clockMask.w).toBe(80);
    expect(clockMask.h).toBe(30);
  });

  it('auto-masks the TurboWeb overlay strip', () => {
    const host = document.createElement('div');
    host.id = '__turbo_overlay_host';
    document.body.appendChild(host);

    const meta = api.screenshotDiffMeta({});
    // A full-width top strip is masked so the cursor/toast never diff.
    const strip = meta.masks.find((m) => m.y === 0 && m.w === window.innerWidth);
    expect(strip).toBeTruthy();
  });

  it('auto-masks video and canvas elements', () => {
    document.body.innerHTML = '<video></video><canvas></canvas>';
    const vid = document.querySelector('video');
    const cnv = document.querySelector('canvas');
    mockRect(vid, { x: 0, y: 100, width: 320, height: 240 });
    mockRect(cnv, { x: 400, y: 100, width: 200, height: 200 });

    const meta = api.screenshotDiffMeta({});
    expect(meta.masks.some((m) => m.w === 320 && m.h === 240)).toBe(true);
    expect(meta.masks.some((m) => m.w === 200 && m.h === 200)).toBe(true);
  });

  it('bundles the DOM summary and viewport', async () => {
    await flushMutations();
    const { token } = api.domMutationsMark();
    document.body.appendChild(document.createElement('div'));
    await flushMutations();

    const meta = api.screenshotDiffMeta({ since: token });
    expect(meta.dom.mutations).toBeGreaterThan(0);
    expect(meta.viewport.w).toBe(window.innerWidth);
    expect(meta.viewport.h).toBe(window.innerHeight);
    expect(typeof meta.url).toBe('string');
  });

  it('is reachable through the message router', async () => {
    const sendResponse = vi.fn();
    messageHandler({ action: 'screenshot_diff_meta', params: {} }, null, sendResponse);
    expect(sendResponse).toHaveBeenCalled();
    const arg = sendResponse.mock.calls[0][0];
    expect(arg).toHaveProperty('masks');
    expect(arg).toHaveProperty('dom');
  });
});

// ============================================================
// agent-driven tab flag — ⚡ prefix on document.title
// ============================================================
describe('agent-driven tab flag', () => {
  beforeEach(() => {
    api.overlay.unflagTitle();           // reset module state between tests
    document.title = 'Example Page';
  });

  it('prefixes the tab title with the brand bolt while an agent is active', async () => {
    await api.overlay.showStart({ action: 'get_html', intent: 'reading the page', id: 't1' });
    expect(document.title).toBe('⚡ Example Page');
  });

  it('does not double-prefix on repeated agent activity', async () => {
    await api.overlay.showStart({ action: 'get_html', intent: 'one', id: 't1' });
    await api.overlay.showStart({ action: 'get_html', intent: 'two', id: 't2' });
    expect(document.title).toBe('⚡ Example Page');
  });

  it('re-applies the flag when the page rewrites its own title', async () => {
    await api.overlay.showStart({ action: 'get_html', intent: 'reading', id: 't1' });
    expect(document.title).toBe('⚡ Example Page');
    document.title = 'Route Changed';                 // SPA-style title swap
    await new Promise((r) => setTimeout(r, 0));        // let the observer fire
    expect(document.title).toBe('⚡ Route Changed');
  });

  it('unflagTitle restores the original title', async () => {
    await api.overlay.showStart({ action: 'get_html', intent: 'reading', id: 't1' });
    expect(document.title).toBe('⚡ Example Page');
    api.overlay.unflagTitle();
    expect(document.title).toBe('Example Page');
  });
});
