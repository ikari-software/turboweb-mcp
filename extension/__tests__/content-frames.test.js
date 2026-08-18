import { describe, it, expect, beforeAll, beforeEach } from 'vitest';
import fs from 'fs';
import path from 'path';
import vm from 'vm';

// Cross-iframe support: resolveRoot / deepElementFromPoint / listFrames and the
// `frame` param threaded through the DOM tools. jsdom gives same-origin iframes
// a real contentDocument, so we can exercise the same-origin path end to end.

let api;

beforeAll(() => {
  // content.js registers a message listener on load; capture it harmlessly.
  chrome.runtime.onMessage.addListener.mockImplementation(() => {});

  const filePath = path.resolve(__dirname, '../content.js');
  let code = fs.readFileSync(filePath, 'utf8');
  const fns = [
    'sel', 'resolveRoot', 'deepElementFromPoint', 'listFrames', 'frameSeg',
    'queryElements', 'findText', 'extractText', 'inspectElement',
    'clickElement', 'getHTML', 'getInteractiveMap', 'getPageStructure',
    'resolveFrameElement', 'navigateFrame', 'locateChildFrame',
  ].join(', ');
  code = code.replace(/\}\)\(\);?\s*$/, `globalThis.__contentAPI = { ${fns} };\n})();`);
  new vm.Script(code, { filename: filePath }).runInThisContext();
  api = globalThis.__contentAPI;
});

function rect(el, { x = 0, y = 0, width = 50, height = 20 } = {}) {
  el.getBoundingClientRect = () => ({
    x, y, width, height, top: y, left: x, right: x + width, bottom: y + height,
  });
}

// Build a same-origin child frame with id `frameId`, append it to `parentEl`
// in document `doc`, position the iframe element at (fx,fy), and fill its
// contentDocument with `innerHTML`. Returns the child document.
function addFrame(doc, parentEl, frameId, { fx = 0, fy = 0, innerHTML = '' } = {}) {
  const iframe = doc.createElement('iframe');
  iframe.id = frameId;
  // jsdom applies the UA default 2px inset iframe border; zero it so the
  // content origin equals the iframe's top-left and offset math is clean.
  // (Border/padding inclusion is covered by its own test below.)
  iframe.style.borderWidth = '0px';
  iframe.style.padding = '0px';
  parentEl.appendChild(iframe);
  rect(iframe, { x: fx, y: fy, width: 300, height: 200 });
  const cdoc = iframe.contentDocument;
  cdoc.body.innerHTML = innerHTML;
  return { iframe, cdoc };
}

beforeEach(() => {
  document.body.innerHTML = '';
  window.scrollX = 0;
  window.scrollY = 0;
});

describe('resolveRoot', () => {
  it('returns the top document for an empty spec', () => {
    const r = api.resolveRoot('');
    expect(r.doc).toBe(document);
    expect(r.offset).toEqual({ x: 0, y: 0 });
    expect(r.isSameOrigin).toBe(true);
    expect(r.framePath).toBe('');
  });

  it('resolves a single same-origin frame and accumulates its offset', () => {
    const { cdoc } = addFrame(document, document.body, 'top_frame', { fx: 100, fy: 50 });
    const r = api.resolveRoot('#top_frame');
    expect(r.doc).toBe(cdoc);
    expect(r.isSameOrigin).toBe(true);
    expect(r.framePath).toBe('#top_frame');
    expect(r.offset.x).toBe(100);
    expect(r.offset.y).toBe(50);
  });

  it('resolves a nested framePath and sums offsets', () => {
    const { iframe, cdoc } = addFrame(document, document.body, 'top_frame', { fx: 100, fy: 50 });
    const { cdoc: inner } = addFrame(cdoc, cdoc.body, 'csframe', { fx: 10, fy: 20 });
    const r = api.resolveRoot('#top_frame > #csframe');
    expect(r.doc).toBe(inner);
    expect(r.framePath).toBe('#top_frame > #csframe');
    expect(r.offset.x).toBe(110);
    expect(r.offset.y).toBe(70);
  });

  it('includes the frame border + padding in the content origin', () => {
    const iframe = document.createElement('iframe');
    iframe.id = 'bordered';
    iframe.style.borderWidth = '5px';
    iframe.style.paddingLeft = '3px';
    iframe.style.paddingTop = '4px';
    document.body.appendChild(iframe);
    rect(iframe, { x: 100, y: 50 });
    const r = api.resolveRoot('#bordered');
    // content viewport starts at frame top-left + border + padding
    expect(r.offset.x).toBe(100 + 5 + 3);
    expect(r.offset.y).toBe(50 + 5 + 4);
  });

  it('throws a clear error when a frame segment is not found', () => {
    expect(() => api.resolveRoot('#nope')).toThrow(/Frame not found/);
  });

  it('throws when the selector resolves to a non-frame element', () => {
    document.body.innerHTML = '<div id="notaframe"></div>';
    expect(() => api.resolveRoot('#notaframe')).toThrow(/Not a frame/);
  });

  it('reports cross-origin frames as unreachable (doc=null)', () => {
    const iframe = document.createElement('iframe');
    iframe.id = 'xo';
    iframe.src = 'https://other.example.com/page';
    document.body.appendChild(iframe);
    rect(iframe, { x: 0, y: 0 });
    // Simulate a cross-origin boundary: contentDocument access yields null.
    Object.defineProperty(iframe, 'contentDocument', { get: () => null, configurable: true });
    const r = api.resolveRoot('#xo');
    expect(r.isSameOrigin).toBe(false);
    expect(r.doc).toBe(null);
    expect(r.framePath).toBe('#xo');
  });
});

describe('listFrames', () => {
  it('returns empty when there are no frames', () => {
    const out = api.listFrames();
    expect(out.count).toBe(0);
    expect(out.frames).toEqual([]);
  });

  it('enumerates a nested same-origin tree with framePaths', () => {
    const { cdoc } = addFrame(document, document.body, 'top_frame', { fx: 100, fy: 50 });
    addFrame(cdoc, cdoc.body, 'csframe', { fx: 10, fy: 20 });
    const out = api.listFrames();
    expect(out.count).toBe(2);
    const paths = out.frames.map(f => f.framePath);
    expect(paths).toContain('#top_frame');
    expect(paths).toContain('#top_frame > #csframe');
    const top = out.frames.find(f => f.framePath === '#top_frame');
    expect(top.isSameOrigin).toBe(true);
    // frameId was dropped (it duplicated framePath); framePath is the sole id now.
    expect(top.frameId).toBeUndefined();
    // nested frame rect is offset into the top viewport (100+10, 50+20)
    const nested = out.frames.find(f => f.framePath === '#top_frame > #csframe');
    expect(nested.rect.x).toBe(110);
    expect(nested.rect.y).toBe(70);
  });

  it('marks a cross-origin frame isSameOrigin:false and does not recurse', () => {
    const iframe = document.createElement('iframe');
    iframe.id = 'xo';
    iframe.src = 'https://other.example.com/p';
    document.body.appendChild(iframe);
    rect(iframe);
    Object.defineProperty(iframe, 'contentDocument', { get: () => null, configurable: true });
    const out = api.listFrames();
    expect(out.count).toBe(1);
    expect(out.frames[0].isSameOrigin).toBe(false);
    expect(out.frames[0].origin).toBe('https://other.example.com');
  });
});

describe('queryElements with frame', () => {
  it('finds elements inside a same-origin frame and offsets their coordinates', () => {
    const { cdoc } = addFrame(document, document.body, 'top_frame', { fx: 100, fy: 50,
      innerHTML: '<input id="house_nbr" name="house_nbr">' });
    const input = cdoc.getElementById('house_nbr');
    rect(input, { x: 10, y: 20, width: 80, height: 24 });

    const out = api.queryElements({ selector: '#house_nbr', frame: '#top_frame' });
    expect(out.count).toBe(1);
    expect(out.frame).toBe('#top_frame');
    // viewport-relative: frame origin (100,50) + element-local (10,20)
    expect(out.elements[0].x).toBe(110);
    expect(out.elements[0].y).toBe(70);
  });

  it('does not see frame elements without the frame param', () => {
    addFrame(document, document.body, 'top_frame', { innerHTML: '<input id="house_nbr">' });
    const out = api.queryElements({ selector: '#house_nbr' });
    expect(out.count).toBe(0);
  });

  it('throws an actionable error for a cross-origin frame', () => {
    const iframe = document.createElement('iframe');
    iframe.id = 'xo';
    document.body.appendChild(iframe);
    rect(iframe);
    Object.defineProperty(iframe, 'contentDocument', { get: () => null, configurable: true });
    expect(() => api.queryElements({ selector: '#x', frame: '#xo' })).toThrow(/cross-origin/);
  });
});

describe('deepElementFromPoint (coordinate piercing)', () => {
  it('descends into a same-origin iframe instead of stopping at the wrapper', () => {
    const { iframe, cdoc } = addFrame(document, document.body, 'top_frame', { fx: 100, fy: 50,
      innerHTML: '<button id="go">Go</button>' });
    const btn = cdoc.getElementById('go');
    // Top-level hit-test lands on the iframe; inner hit-test (translated by the
    // frame's content origin) lands on the button.
    document.elementFromPoint = () => iframe;
    cdoc.elementFromPoint = () => btn;
    const hit = api.deepElementFromPoint(45, 77);
    expect(hit.el).toBe(btn);
    expect(hit.framePath).toBe('#top_frame');
    expect(hit.offset).toEqual({ x: 100, y: 50 });
  });

  it('returns the top element unchanged when no iframe is hit', () => {
    document.body.innerHTML = '<button id="b">x</button>';
    const b = document.getElementById('b');
    document.elementFromPoint = () => b;
    const hit = api.deepElementFromPoint(5, 5);
    expect(hit.el).toBe(b);
    expect(hit.framePath).toBe('');
    expect(hit.offset).toEqual({ x: 0, y: 0 });
  });
});

describe('navigateFrame', () => {
  it('navigates a frame by setting its src, leaving the parent intact', () => {
    addFrame(document, document.body, 'top_frame', { fx: 0, fy: 0 });
    const out = api.navigateFrame({ frame: '#top_frame', url: 'https://x/y' });
    expect(out.navigated).toBe(true);
    expect(out.frame).toBe('#top_frame');
    expect(document.getElementById('top_frame').getAttribute('src')).toBe('https://x/y');
    // parent document still has the frame element (frameset preserved)
    expect(document.querySelectorAll('#top_frame').length).toBe(1);
  });

  it('resolves a nested frame element through a same-origin parent', () => {
    const { cdoc } = addFrame(document, document.body, 'top_frame');
    addFrame(cdoc, cdoc.body, 'csframe');
    const el = api.resolveFrameElement('#top_frame > #csframe');
    expect(el.id).toBe('csframe');
    expect(el.ownerDocument).toBe(cdoc);
  });

  it('requires a url', () => {
    addFrame(document, document.body, 'top_frame');
    expect(() => api.navigateFrame({ frame: '#top_frame' })).toThrow(/url is required/);
  });

  it('throws when the parent chain is cross-origin', () => {
    const iframe = document.createElement('iframe');
    iframe.id = 'xo';
    document.body.appendChild(iframe);
    rect(iframe);
    Object.defineProperty(iframe, 'contentDocument', { get: () => null, configurable: true });
    expect(() => api.navigateFrame({ frame: '#xo > #inner', url: 'https://x' })).toThrow(/cross-origin/);
  });
});

describe('clickElement coordinate piercing', () => {
  it('clicks the real leaf inside a same-origin iframe and reports viewport coords', () => {
    const { iframe, cdoc } = addFrame(document, document.body, 'top_frame', { fx: 100, fy: 50,
      innerHTML: '<button id="go">Go</button>' });
    const btn = cdoc.getElementById('go');
    rect(btn, { x: 10, y: 10, width: 40, height: 20 });
    document.elementFromPoint = () => iframe;
    cdoc.elementFromPoint = () => btn;

    const out = api.clickElement({ x: 45, y: 77 });
    expect(out.clicked).toBe('#go');
    expect(out.frame).toBe('#top_frame');
    // local centre (10+20, 10+10) = (30,20) → +frame origin (100,50) = (130,70)
    expect(out.x).toBe(130);
    expect(out.y).toBe(70);
  });
});

describe('locateChildFrame (cross-origin frameId handshake)', () => {
  it('finds the iframe by selector, posts the nonce into it, returns the child origin', () => {
    const { iframe } = addFrame(document, document.body, 'embed', { fx: 120, fy: 60 });
    let posted = null;
    iframe.contentWindow.postMessage = (data) => { posted = data; };

    const out = api.locateChildFrame({ selector: '#embed', nonce: 'abc123' });
    expect(out).toEqual({ ok: true, origin: { x: 120, y: 60 } });
    expect(posted).toEqual({ __turbo_frame_probe: 'abc123' });
  });

  it('throws when the selector matches no frame', () => {
    expect(() => api.locateChildFrame({ selector: '#nope', nonce: 'n' }))
      .toThrow(/Frame not found/);
  });

  it('throws when the selector resolves to a non-frame element', () => {
    document.body.innerHTML = '<div id="notaframe"></div>';
    expect(() => api.locateChildFrame({ selector: '#notaframe', nonce: 'n' }))
      .toThrow(/Not a frame/);
  });
});

describe('frame probe ack listener', () => {
  it('acks to the background with the nonce when a probe message arrives', () => {
    chrome.runtime.sendMessage.mockClear();
    window.dispatchEvent(new window.MessageEvent('message', { data: { __turbo_frame_probe: 'nonce-xyz' } }));
    expect(chrome.runtime.sendMessage).toHaveBeenCalledWith({ action: '__frame_probe_ack', nonce: 'nonce-xyz' });
  });

  it('ignores unrelated postMessages', () => {
    chrome.runtime.sendMessage.mockClear();
    window.dispatchEvent(new window.MessageEvent('message', { data: { hello: 'world' } }));
    expect(chrome.runtime.sendMessage).not.toHaveBeenCalled();
  });
});

// --- 46a: coverage gaps for the cross-iframe feature ---

describe('queryElements with a NESTED frame (2-segment offset round-trip)', () => {
  // The single-level case is covered above; this guards the core invariant that
  // BOTH frame offsets are summed exactly once. A double-add or a missed
  // accumulation would pass silently without this.
  it('accumulates both frame offsets exactly once', () => {
    const { cdoc } = addFrame(document, document.body, 'top_frame', { fx: 100, fy: 50 });
    const { cdoc: inner } = addFrame(cdoc, cdoc.body, 'csframe', { fx: 10, fy: 20,
      innerHTML: '<input id="deep" name="deep">' });
    const input = inner.getElementById('deep');
    rect(input, { x: 5, y: 7, width: 60, height: 20 });

    const out = api.queryElements({ selector: '#deep', frame: '#top_frame > #csframe' });
    expect(out.count).toBe(1);
    expect(out.frame).toBe('#top_frame > #csframe');
    // top(100,50) + inner(10,20) + element-local(5,7), each added once.
    expect(out.elements[0].x).toBe(115);
    expect(out.elements[0].y).toBe(77);
  });
});

describe('getInteractiveMap off-screen culling with frame offset', () => {
  it('culls a control whose frame offset pushes it below the top viewport', () => {
    window.innerHeight = 768; window.innerWidth = 1024;
    // Frame content origin sits far below the fold (y=2000).
    const { cdoc } = addFrame(document, document.body, 'below', { fx: 0, fy: 2000,
      innerHTML: '<button id="hidden_btn">Deep</button>' });
    const btn = cdoc.getElementById('hidden_btn');
    rect(btn, { x: 0, y: 0, width: 80, height: 30 });
    const out = api.getInteractiveMap({ frame: '#below' });
    expect(out.elements.find(e => (e.selector || '').includes('hidden_btn'))).toBeUndefined();
  });

  it('keeps a control that is on-screen once the frame offset is applied, and offsets its coords', () => {
    window.innerHeight = 768; window.innerWidth = 1024;
    const { cdoc } = addFrame(document, document.body, 'vis', { fx: 0, fy: 40,
      innerHTML: '<button id="vis_btn">See</button>' });
    const btn = cdoc.getElementById('vis_btn');
    rect(btn, { x: 0, y: 0, width: 80, height: 30 });
    const out = api.getInteractiveMap({ frame: '#vis' });
    const found = out.elements.find(e => (e.selector || '').includes('vis_btn'));
    expect(found).toBeDefined();
    // Coordinates carry the frame offset (frame-local 0 + frame origin 40).
    expect(found.y).toBe(40);
  });
});

describe('getPageStructure frame scoping', () => {
  it('reports the child doc title, emits a frame: key + url line, and scopes to the child body', () => {
    const { cdoc } = addFrame(document, document.body, 'top_frame', { fx: 0, fy: 0 });
    // Top document's title is empty; setting the CHILD's title proves scoping
    // reads fdoc (the frame's document), not the top document.
    cdoc.title = 'Child Doc';
    cdoc.body.innerHTML = '<h1 id="h">Inner Heading</h1>';
    rect(cdoc.getElementById('h'), { x: 0, y: 10, width: 200, height: 30 });

    const out = api.getPageStructure({ frame: '#top_frame' });
    expect(out.yaml).toContain('frame: "#top_frame"');
    expect(out.yaml).toContain('title: "Child Doc"');
    expect(out.yaml).toMatch(/\n\s*url: /);        // reads fdoc.location.href
    expect(out.yaml).toContain('Inner Heading');   // child body content, scoped in
  });

  it('culls a child element pushed off-screen by the frame offset (visibleOnly)', () => {
    window.innerHeight = 768; window.innerWidth = 1024;
    const { cdoc } = addFrame(document, document.body, 'far', { fx: 0, fy: 5000 });
    cdoc.body.innerHTML = '<h1 id="deep_h">Below the fold</h1>';
    rect(cdoc.getElementById('deep_h'), { x: 0, y: 0, width: 200, height: 30 });
    const out = api.getPageStructure({ frame: '#far' });
    expect(out.yaml).not.toContain('Below the fold');
  });
});
