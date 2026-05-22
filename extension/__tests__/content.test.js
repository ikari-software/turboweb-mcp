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

  // Load content.js — inject a globalThis export line before the closing IIFE
  const filePath = path.resolve(__dirname, '../content.js');
  let code = fs.readFileSync(filePath, 'utf8');

  const fns = [
    'sel', 'viewport', 'extractText', 'findText', 'inspectElement',
    'getInteractiveMap', 'queryElements', 'inspectForm', 'pageCapabilities',
    'clickElement', 'typeText',
    'scrollPage', 'getHTML', 'depthLimitedHTML', 'getPageStructure',
    'executeJS', 'injectScript',
    'dragDropFile', 'runDropInPage', '__turboPerformDrop',
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
    // No listener mutates the DOM → MutationObserver sees nothing.
    const result = await api.__turboPerformDrop(zone, new Blob(['x']), 'x.txt', 'text/plain');
    expect(result.warnings.length).toBeGreaterThan(0);
    expect(result.warnings[0]).toMatch(/did not appear to react/);
  });

  it('does not warn when the handler mutates the DOM', async () => {
    document.body.innerHTML = '<div id="live"></div>';
    const zone = document.querySelector('#live');
    mockRect(zone, { x: 0, y: 0, width: 50, height: 50 });
    zone.addEventListener('drop', () => {
      zone.appendChild(document.createElement('span')); // observable mutation
    });
    const result = await api.__turboPerformDrop(zone, new Blob(['x']), 'x.txt', 'text/plain');
    expect(result.warnings.length).toBe(0);
  });

  // --- dragDropFile: pre-flight validation & fetch ---
  it('rejects a missing target_selector', async () => {
    await expect(api.dragDropFile({ fileName: 'a.txt' }))
      .rejects.toThrow(/target_selector is required/);
  });

  it('rejects when no element matches the selector', async () => {
    await expect(api.dragDropFile({
      target_selector: '.nope', fileName: 'a.txt', fileHostPort: 18323, fileToken: 'tok',
    })).rejects.toThrow(/No element matches selector/);
  });

  it('rejects a zero-size target element', async () => {
    document.body.innerHTML = '<div class="hidden"></div>';
    mockRect(document.querySelector('.hidden'), { x: 0, y: 0, width: 0, height: 0 });
    await expect(api.dragDropFile({
      target_selector: '.hidden', fileName: 'a.txt', fileHostPort: 18323, fileToken: 'tok',
    })).rejects.toThrow(/zero-size bounding box/);
  });

  it('surfaces an unreachable file host', async () => {
    document.body.innerHTML = '<div class="dz"></div>';
    mockRect(document.querySelector('.dz'), { x: 0, y: 0, width: 100, height: 100 });
    globalThis.fetch = vi.fn().mockRejectedValue(new Error('network down'));
    await expect(api.dragDropFile({
      target_selector: '.dz', fileName: 'a.txt', fileHostPort: 18323, fileToken: 'tok',
    })).rejects.toThrow(/could not reach the loopback file host/);
  });

  it('surfaces an HTTP error from the file host (expired token)', async () => {
    document.body.innerHTML = '<div class="dz"></div>';
    mockRect(document.querySelector('.dz'), { x: 0, y: 0, width: 100, height: 100 });
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: false, status: 410 });
    await expect(api.dragDropFile({
      target_selector: '.dz', fileName: 'a.txt', fileHostPort: 18323, fileToken: 'tok',
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
      target_selector: '.dz', fileName: 'a.txt', mimeType: 'text/plain',
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
      { action: 'drag_drop_file', params: { target_selector: '.dz', fileName: 'a.txt', fileHostPort: 1, fileToken: 't' } },
      null, sendResponse,
    );
    expect(r).toBe(true); // async handler
  });
});
