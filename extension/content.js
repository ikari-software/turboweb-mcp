// TurboWeb MCP by ikari — Content Script
// Runs in every page. Handles DOM queries, spatial mapping, OCR, clicks, typing.
// Communicates with background.js via chrome.runtime messages.

(() => {
  'use strict';

  // --- Selector generation ---
  // Build a CSS selector that uniquely identifies an element. Walks up
  // the DOM building tag>nth-of-type>... chains, and verifies after each
  // level that the chain resolves to exactly this element. Stops as
  // soon as uniqueness is achieved, or when we hit an id-bearing
  // ancestor (which anchors the chain). Capped at 15 levels so we don't
  // hammer querySelectorAll on pathological trees.
  //
  // The previous implementation capped at 5 levels and never checked
  // uniqueness — the result was that on a complex SPA, querySelector()
  // for the generated selector returned the *first* tree-order match,
  // which could be anywhere in the document. Both the cursor animation
  // (resolveTarget) and the real DOM click (clickElement) re-resolve
  // via that selector, so the two were consistently going to the wrong
  // element. This fix is what makes the click animation actually point
  // at the thing the agent meant to click.
  function sel(el) {
    if (!el || !el.tagName) return '';
    // Resolve uniqueness against the element's OWN document, not the global
    // top-frame `document` — otherwise selectors generated for elements inside
    // a (same-origin) child frame would be validated against the wrong tree.
    const ownerDoc = el.ownerDocument || document;
    // Local helper: true iff `s` matches exactly one element AND that
    // element is the target. Used to confirm uniqueness at each level
    // of the walk-up chain below.
    const isUnique = (s) => {
      try {
        const m = ownerDoc.querySelectorAll(s);
        return m.length === 1 && m[0] === el;
      } catch { return false; }
    };
    // Best case: element itself has an id.
    if (el.id) {
      const trial = '#' + CSS.escape(el.id);
      if (isUnique(trial)) return trial;
    }
    // Next best: data-testid on the element.
    const tid = el.getAttribute && el.getAttribute('data-testid');
    if (tid) {
      const trial = `[data-testid=${JSON.stringify(tid)}]`;
      if (isUnique(trial)) return trial;
    }

    const parts = [];
    let cur = el;
    for (let depth = 0; depth < 15 && cur && cur !== ownerDoc.body && cur !== ownerDoc.documentElement; depth++) {
      let segment;
      if (cur.id) {
        segment = '#' + CSS.escape(cur.id);
      } else {
        segment = cur.tagName.toLowerCase();
        const td = cur.getAttribute && cur.getAttribute('data-testid');
        if (td) {
          segment += `[data-testid=${JSON.stringify(td)}]`;
        } else {
          const parent = cur.parentElement;
          if (parent) {
            const same = Array.from(parent.children).filter(c => c.tagName === cur.tagName);
            if (same.length > 1) {
              segment += ':nth-of-type(' + (same.indexOf(cur) + 1) + ')';
            }
          }
        }
      }
      parts.unshift(segment);

      // After each prepend, re-check uniqueness. Done as soon as it
      // resolves to exactly our target.
      const trial = parts.join('>');
      if (isUnique(trial)) return trial;

      // An id-bearing ancestor anchors the chain. Going further only
      // adds a redundant prefix.
      if (cur.id && cur !== el) break;

      cur = cur.parentElement;
    }

    // Fallback: didn't reach uniqueness within the depth cap. Return
    // the longest chain we built — better than nothing, and consistent
    // with the chain the agent will use to re-resolve.
    return parts.join('>');
  }

  // --- Viewport info (included in many responses) ---
  function viewport() {
    return {
      w: window.innerWidth,
      h: window.innerHeight,
      scrollX: Math.round(window.scrollX),
      scrollY: Math.round(window.scrollY),
      pageW: document.documentElement.scrollWidth,
      pageH: document.documentElement.scrollHeight,
      dpr: window.devicePixelRatio,
    };
  }

  // --- Cross-iframe support ---------------------------------------------
  // content.js runs only in the top frame (manifest all_frames:false), but it
  // CAN reach SAME-ORIGIN child frames through their contentDocument. These
  // helpers let every DOM tool target a nested frame and keep the coordinates
  // it reports viewport-relative — so a query inside a frame round-trips to a
  // later coordinate click. Cross-origin frames are opaque here; the cdp_*
  // tools (real input via BiDi) pierce those natively.
  //
  // A `frame` spec is a framePath: a ">"-separated list of CSS selectors, each
  // resolving an <iframe>/<frame> within the previous frame's document, e.g.
  //   "#top_frame"  or  "#top_frame > #csframe".

  // getComputedStyle bound to an element's own window (cross-document safe).
  function gcs(el) {
    const v = (el.ownerDocument && el.ownerDocument.defaultView) || window;
    return v.getComputedStyle(el);
  }

  // A short, resolvable selector segment for a frame element. Prefers id / name
  // (framesets almost always name their frames); falls back to a unique path.
  function frameSeg(el) {
    if (el.id) return '#' + CSS.escape(el.id);
    const nm = el.getAttribute && el.getAttribute('name');
    if (nm) return el.tagName.toLowerCase() + '[name=' + JSON.stringify(nm) + ']';
    return sel(el);
  }

  // Origin of a frame's content viewport within its parent document, including
  // the frame's own border + padding (where the child viewport actually begins).
  function frameContentOrigin(el) {
    const r = el.getBoundingClientRect();
    const cs = gcs(el);
    return {
      x: r.left + (parseFloat(cs.borderLeftWidth) || 0) + (parseFloat(cs.paddingLeft) || 0),
      y: r.top + (parseFloat(cs.borderTopWidth) || 0) + (parseFloat(cs.paddingTop) || 0),
    };
  }

  // Resolve a frame spec to its document + cumulative viewport offset. Returns
  // { doc, win, offset, framePath, isSameOrigin }. `doc` is null when a segment
  // is cross-origin (isSameOrigin:false). With no spec, returns the top frame.
  function resolveRoot(frameSpec) {
    if (!frameSpec) {
      return { doc: document, win: window, offset: { x: 0, y: 0 }, framePath: '', isSameOrigin: true };
    }
    const segments = String(frameSpec).split('>').map(s => s.trim()).filter(Boolean);
    let doc = document, win = window, offX = 0, offY = 0;
    const parts = [];
    for (const segSel of segments) {
      let frameEl;
      try { frameEl = doc.querySelector(segSel); }
      catch { throw new Error('Invalid frame selector: ' + JSON.stringify(segSel)); }
      const where = parts.length ? ' (within ' + parts.join(' > ') + ')' : '';
      if (!frameEl) throw new Error('Frame not found: ' + segSel + where);
      if (frameEl.tagName !== 'IFRAME' && frameEl.tagName !== 'FRAME') {
        throw new Error('Not a frame: ' + segSel + ' resolved to <' + frameEl.tagName.toLowerCase() + '>');
      }
      const o = frameContentOrigin(frameEl);
      offX += o.x; offY += o.y;
      parts.push(frameSeg(frameEl));
      let childDoc = null;
      try { childDoc = frameEl.contentDocument; } catch { childDoc = null; }
      if (!childDoc) {
        return { doc: null, win: frameEl.contentWindow || null, offset: { x: offX, y: offY }, framePath: parts.join(' > '), isSameOrigin: false };
      }
      doc = childDoc; win = frameEl.contentWindow || win;
    }
    return { doc, win, offset: { x: offX, y: offY }, framePath: parts.join(' > '), isSameOrigin: true };
  }

  // Resolve a frame spec and assert the content script can actually read it.
  // Returns { root, off, framePath }. Throws an actionable error for cross-origin.
  function frameCtx(frameSpec) {
    const r = resolveRoot(frameSpec);
    if (!r.doc) {
      throw new Error(
        'Frame "' + r.framePath + '" is cross-origin — the content script cannot read into it. ' +
        'Use cdp_click / cdp_type / cdp_scroll (real input via BiDi pierces cross-origin frames), ' +
        'or target a same-origin frame.'
      );
    }
    return { root: r.doc, off: r.offset, framePath: r.framePath };
  }

  // Hit-test that descends through SAME-ORIGIN iframes. Standard
  // document.elementFromPoint returns the <iframe> element itself; this recurses
  // into the frame's contentDocument (translating the point by the frame's
  // content origin) and returns the real leaf element plus the frame it lives in.
  function deepElementFromPoint(x, y) {
    let doc = document, offX = 0, offY = 0, guard = 0;
    const parts = [];
    let el = doc.elementFromPoint(x, y);
    while (el && (el.tagName === 'IFRAME' || el.tagName === 'FRAME') && guard++ < 16) {
      let childDoc = null;
      try { childDoc = el.contentDocument; } catch { childDoc = null; }
      if (!childDoc) break; // cross-origin: the <iframe> element is as deep as we go
      const o = frameContentOrigin(el);
      offX += o.x; offY += o.y;
      parts.push(frameSeg(el));
      const inner = childDoc.elementFromPoint(x - offX, y - offY);
      doc = childDoc;
      if (!inner) { el = null; break; }
      el = inner;
    }
    return { el, doc, offset: { x: offX, y: offY }, framePath: parts.join(' > ') };
  }

  // --- list_frames: enumerate the frame tree (same- and cross-origin) ---
  function listFrames() {
    const frames = [];
    (function walk(doc, parentPath, parentOff, depth) {
      if (depth > 16) return;
      for (const el of doc.querySelectorAll('iframe,frame')) {
        const seg = frameSeg(el);
        const framePath = parentPath ? parentPath + ' > ' + seg : seg;
        const r = el.getBoundingClientRect();
        let childDoc = null;
        try { childDoc = el.contentDocument; } catch { childDoc = null; }
        const sameOrigin = !!childDoc;
        let url, origin;
        try {
          if (sameOrigin && childDoc.location) { url = childDoc.location.href; origin = childDoc.location.origin; }
          else if (el.src) { origin = new URL(el.src, location.href).origin; }
        } catch { /* opaque */ }
        frames.push({
          frameId: framePath,
          framePath,
          id: el.id || undefined,
          name: (el.getAttribute && el.getAttribute('name')) || undefined,
          src: el.src || undefined,
          url: url || undefined,
          origin: origin || undefined,
          isSameOrigin: sameOrigin,
          rect: { x: Math.round(parentOff.x + r.x), y: Math.round(parentOff.y + r.y), w: Math.round(r.width), h: Math.round(r.height) },
        });
        if (sameOrigin) {
          const o = frameContentOrigin(el);
          walk(childDoc, framePath, { x: parentOff.x + o.x, y: parentOff.y + o.y }, depth + 1);
        }
      }
    })(document, '', { x: 0, y: 0 }, 0);
    return { count: frames.length, frames };
  }

  // --- Extract visible text with positions (DOM-based OCR) ---
  // Now supports: selector scope, region filter {rx,ry,rw,rh}, and max results
  function extractText({ selector, region, max = 500, frame } = {}) {
    const { root: fdoc, off, framePath } = frameCtx(frame);
    const root = selector ? fdoc.querySelector(selector) : fdoc.body;
    if (!root) throw new Error('Element not found: ' + selector);

    const walker = fdoc.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
      acceptNode(node) {
        if (!node.textContent.trim()) return NodeFilter.FILTER_REJECT;
        const el = node.parentElement;
        if (!el) return NodeFilter.FILTER_REJECT;
        const st = gcs(el);
        if (st.display === 'none' || st.visibility === 'hidden' || st.opacity === '0') return NodeFilter.FILTER_REJECT;
        return NodeFilter.FILTER_ACCEPT;
      }
    });

    const blocks = new Map();
    while (walker.nextNode()) {
      const p = walker.currentNode.parentElement;
      if (!blocks.has(p)) blocks.set(p, []);
      blocks.get(p).push(walker.currentNode.textContent.trim());
    }

    const out = [];
    for (const [el, texts] of blocks) {
      if (out.length >= max) break;
      const r = el.getBoundingClientRect();
      if (r.width < 1 || r.height < 1) continue;
      // Region filter: skip if outside specified rectangle
      if (region) {
        const { rx, ry, rw, rh } = region;
        if (r.right < rx || r.left > rx + rw || r.bottom < ry || r.top > ry + rh) continue;
      }
      const text = texts.join(' ').trim();
      if (!text) continue;
      out.push({
        text: text.substring(0, 500),
        x: Math.round(r.x + off.x), y: Math.round(r.y + off.y),
        w: Math.round(r.width), h: Math.round(r.height),
        tag: el.tagName.toLowerCase(),
      });
    }
    return { viewport: viewport(), frame: framePath || undefined, count: out.length, blocks: out };
  }

  // --- Find elements by visible text (like Cmd+F but structured) ---
  function findText({ query, max = 20, caseSensitive = false, frame }) {
    if (!query) throw new Error('query is required');
    const { root: fdoc, off, framePath } = frameCtx(frame);
    const q = caseSensitive ? query : query.toLowerCase();
    const walker = fdoc.createTreeWalker(fdoc.body, NodeFilter.SHOW_TEXT, {
      acceptNode(node) {
        const t = caseSensitive ? node.textContent : node.textContent.toLowerCase();
        if (!t.includes(q)) return NodeFilter.FILTER_REJECT;
        const el = node.parentElement;
        if (!el) return NodeFilter.FILTER_REJECT;
        const st = gcs(el);
        if (st.display === 'none' || st.visibility === 'hidden') return NodeFilter.FILTER_REJECT;
        return NodeFilter.FILTER_ACCEPT;
      }
    });

    // Dedupe by parent element
    const seen = new Set();
    const out = [];
    while (walker.nextNode() && out.length < max) {
      const el = walker.currentNode.parentElement;
      if (seen.has(el)) continue;
      seen.add(el);
      const r = el.getBoundingClientRect();
      out.push({
        text: el.innerText.substring(0, 300),
        x: Math.round(r.x + off.x), y: Math.round(r.y + off.y),
        w: Math.round(r.width), h: Math.round(r.height),
        tag: el.tagName.toLowerCase(),
        selector: sel(el),
      });
    }
    return { query, frame: framePath || undefined, found: out.length, results: out };
  }

  // --- Inspect: deep one-shot inspection of an element ---
  // Find by selector, coordinates, or text search. Returns everything useful in one call.
  function inspectElement({ selector, x, y, text: searchText, depth = 2, frame }) {
    let el, off = { x: 0, y: 0 }, framePath = '';
    if (selector) {
      const ctx = frameCtx(frame); off = ctx.off; framePath = ctx.framePath;
      el = ctx.root.querySelector(selector);
    } else if (x !== undefined && y !== undefined) {
      // Coordinate hit-test descends through same-origin iframes so x,y no
      // longer stops at the <iframe> wrapper.
      const hit = deepElementFromPoint(x, y);
      el = hit.el; off = hit.offset; framePath = hit.framePath;
    } else if (searchText) {
      const ctx = frameCtx(frame); off = ctx.off; framePath = ctx.framePath;
      // Find first element containing this text
      const walker = ctx.root.createTreeWalker(ctx.root.body, NodeFilter.SHOW_TEXT, {
        acceptNode(n) { return n.textContent.includes(searchText) ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT; }
      });
      if (walker.nextNode()) el = walker.currentNode.parentElement;
    }
    if (!el) throw new Error('Element not found');
    const ownerDoc = el.ownerDocument || document;

    const r = el.getBoundingClientRect();
    const cs = gcs(el);

    // Gather attributes
    const attrs = {};
    for (const a of el.attributes) attrs[a.name] = a.value.substring(0, 200);

    // Parent chain (compact)
    const parents = [];
    let cur = el.parentElement;
    for (let i = 0; i < 5 && cur && cur !== ownerDoc.documentElement; i++) {
      const pr = cur.getBoundingClientRect();
      parents.push({
        tag: cur.tagName.toLowerCase(),
        id: cur.id || undefined,
        cls: (cur.className || '').toString().split(/\s+/).filter(c => c.length < 40).slice(0, 3).join(' ') || undefined,
        rect: { x: Math.round(pr.x + off.x), y: Math.round(pr.y + off.y), w: Math.round(pr.width), h: Math.round(pr.height) },
      });
      cur = cur.parentElement;
    }

    // Children summary (up to depth)
    function summarizeChildren(node, d) {
      if (d > depth) return null;
      const kids = [...node.children];
      if (!kids.length) return null;
      return kids.slice(0, 20).map(c => {
        const cr = c.getBoundingClientRect();
        const entry = {
          tag: c.tagName.toLowerCase(),
          text: (c.innerText || '').substring(0, 120).replace(/\n/g, ' '),
          rect: { x: Math.round(cr.x + off.x), y: Math.round(cr.y + off.y), w: Math.round(cr.width), h: Math.round(cr.height) },
        };
        if (c.id) entry.id = c.id;
        const sub = summarizeChildren(c, d + 1);
        if (sub) entry.children = sub;
        return entry;
      });
    }

    // Nearby siblings
    const siblings = [];
    const parent = el.parentElement;
    if (parent) {
      const sibs = [...parent.children];
      const idx = sibs.indexOf(el);
      for (let i = Math.max(0, idx - 2); i < Math.min(sibs.length, idx + 3); i++) {
        if (sibs[i] === el) continue;
        const sr = sibs[i].getBoundingClientRect();
        siblings.push({
          tag: sibs[i].tagName.toLowerCase(),
          text: (sibs[i].innerText || '').substring(0, 100).replace(/\n/g, ' '),
          rect: { x: Math.round(sr.x + off.x), y: Math.round(sr.y + off.y), w: Math.round(sr.width), h: Math.round(sr.height) },
        });
      }
    }

    return {
      tag: el.tagName.toLowerCase(),
      text: (el.innerText || '').substring(0, 1000),
      frame: framePath || undefined,
      rect: { x: Math.round(r.x + off.x), y: Math.round(r.y + off.y), w: Math.round(r.width), h: Math.round(r.height) },
      selector: sel(el),
      attrs,
      style: {
        display: cs.display, position: cs.position,
        bg: cs.backgroundColor !== 'rgba(0, 0, 0, 0)' ? cs.backgroundColor : undefined,
        border: cs.borderWidth !== '0px' ? `${cs.borderColor} ${cs.borderWidth}` : undefined,
        font: `${cs.fontSize} ${cs.fontWeight} ${cs.fontFamily.split(',')[0]}`,
      },
      parents,
      children: summarizeChildren(el, 0),
      siblings,
    };
  }

  // --- Interactive element map with spatial positions ---
  function getInteractiveMap({ frame } = {}) {
    const { root: fdoc, off, framePath } = frameCtx(frame);
    const Q = 'a[href],button,input,select,textarea,[role="button"],[role="link"],[role="tab"],[role="menuitem"],[role="checkbox"],[role="radio"],[role="switch"],[onclick],[tabindex]:not([tabindex="-1"]),summary,[contenteditable="true"]';
    const els = fdoc.querySelectorAll(Q);
    const items = [];

    for (const el of els) {
      const r = el.getBoundingClientRect();
      if (r.width < 1 || r.height < 1) continue;
      // Skip offscreen — compare against the TOP viewport using the frame offset,
      // so a control inside a scrolled-off frame is correctly excluded.
      const top = r.top + off.y, bottom = r.bottom + off.y, left = r.left + off.x, right = r.right + off.x;
      if (bottom < 0 || top > window.innerHeight + 100) continue;
      if (right < 0 || left > window.innerWidth + 100) continue;

      const item = {
        tag: el.tagName.toLowerCase(),
        text: (el.textContent || '').trim().substring(0, 120),
        x: Math.round(r.x + off.x), y: Math.round(r.y + off.y),
        w: Math.round(r.width), h: Math.round(r.height),
        selector: sel(el),
      };

      // Enrich based on element type
      const role = el.getAttribute('role');
      if (role) item.role = role;
      const aria = el.getAttribute('aria-label');
      if (aria) item.ariaLabel = aria;
      if (el.tagName === 'A') item.href = el.href;
      if (el.tagName === 'INPUT') {
        item.inputType = el.type;
        item.value = el.value;
        item.name = el.name;
        if (el.placeholder) item.placeholder = el.placeholder;
        if (el.checked !== undefined) item.checked = el.checked;
      }
      if (el.tagName === 'SELECT') {
        item.value = el.value;
        item.options = [...el.options].slice(0, 20).map(o => ({ v: o.value, t: o.text, s: o.selected }));
      }
      if (el.tagName === 'TEXTAREA') {
        item.value = el.value;
        item.name = el.name;
      }
      if (el.disabled) item.disabled = true;

      items.push(item);
    }

    // Fix 3: detect inputs whose name= is shared with hidden siblings.
    // When name= matches multiple elements (e.g. an ebook field and a
    // hidden print-book field), querySelector returns the wrong one.
    // Annotate visible items so the agent prefers the ID selector.
    const nameSiblings = {};
    for (const el of document.querySelectorAll('input[name],textarea[name],select[name]')) {
      const n = el.name;
      if (!nameSiblings[n]) nameSiblings[n] = { total: 0, hidden: 0 };
      nameSiblings[n].total++;
      if (el.offsetParent === null) nameSiblings[n].hidden++;
    }
    for (const item of items) {
      if (item.name) {
        const s = nameSiblings[item.name];
        if (s && s.hidden > 0) {
          item.duplicateName = `⚠ ${s.total} elements share name="${item.name}", `
            + `${s.hidden} hidden — use id selector (e.g. #${item.selector.replace(/^.*#/, '') || '…'}) to avoid targeting the wrong one`;
        }
      }
    }

    return { viewport: viewport(), frame: framePath || undefined, elements: items };
  }

  // --- Form-field metadata ---
  // The accessible label for a control: associated <label> (for= or wrapping),
  // else aria-label, else aria-labelledby target text.
  function labelFor(el) {
    if (el.labels && el.labels.length) {
      return el.labels[0].textContent.trim().substring(0, 120);
    }
    const aria = el.getAttribute('aria-label');
    if (aria) return aria.trim().substring(0, 120);
    const lblId = el.getAttribute('aria-labelledby');
    if (lblId) {
      const lbl = document.getElementById(lblId);
      if (lbl) return lbl.textContent.trim().substring(0, 120);
    }
    return null;
  }

  // Structured view of a form control — the live value/checked state, label,
  // and constraints an agent needs to actually fill a form. Raw attributes
  // alone are not enough: `value` is a live property (not an attribute), and
  // the label usually lives in a separate element. Returns null for non-fields.
  function fieldInfo(el) {
    const tag = el.tagName;
    if (tag !== 'INPUT' && tag !== 'TEXTAREA' && tag !== 'SELECT') return null;
    const f = { type: tag === 'INPUT' ? el.type : tag.toLowerCase() };
    if (el.name) f.name = el.name;
    if (el.id) f.id = el.id;
    f.value = el.value;
    if (el.placeholder) f.placeholder = el.placeholder;
    if (el.required) f.required = true;
    if (el.disabled) f.disabled = true;
    if (el.readOnly) f.readonly = true;
    if (el.type === 'checkbox' || el.type === 'radio') f.checked = el.checked;
    if (typeof el.maxLength === 'number' && el.maxLength >= 0) f.maxLength = el.maxLength;
    const label = labelFor(el);
    if (label) f.label = label;
    if (tag === 'SELECT') {
      f.options = [...el.options].slice(0, 30)
        .map((o) => ({ value: o.value, text: o.text.trim(), selected: o.selected }));
    }
    return f;
  }

  // --- CSS selector query with positions ---
  function queryElements({ selector, limit = 50, frame }) {
    const { root: fdoc, off, framePath } = frameCtx(frame);
    const els = fdoc.querySelectorAll(selector);
    const out = [];
    let i = 0;
    for (const el of els) {
      if (i++ >= limit) break;
      const r = el.getBoundingClientRect();
      const attrs = {};
      for (const a of el.attributes) attrs[a.name] = a.value.substring(0, 200);
      const item = {
        tag: el.tagName.toLowerCase(),
        text: (el.textContent || '').trim().substring(0, 300),
        x: Math.round(r.x + off.x), y: Math.round(r.y + off.y),
        w: Math.round(r.width), h: Math.round(r.height),
        attrs,
        selector: sel(el),
      };
      // Surface form-field state so an agent can reason about a form without
      // a separate inspect call per input.
      const field = fieldInfo(el);
      if (field) item.field = field;
      out.push(item);
    }
    return { viewport: viewport(), frame: framePath || undefined, count: els.length, elements: out };
  }

  // --- Form inspection (try_url_prefill discovery + verification) ---
  // Returns a descriptor of one form: its action/method and every field's
  // metadata. The Go orchestrator maps formData keys to URL params off this,
  // then re-calls it post-navigation to verify which fields got populated.
  function inspectForm({ selector } = {}) {
    // Resolve the target form. An explicit selector wins; otherwise pick the
    // <form> with the most fields. Many SPAs skip <form> entirely, so when
    // there are none, fall back to the whole document as the field container.
    let form = null;
    if (selector) {
      form = document.querySelector(selector);
      if (!form) throw new Error('Form not found: ' + selector);
    } else {
      let best = null, bestCount = -1;
      for (const f of document.querySelectorAll('form')) {
        const n = f.querySelectorAll('input,select,textarea,[contenteditable]').length;
        if (n > bestCount) { best = f; bestCount = n; }
      }
      form = best; // may stay null — handled below
    }

    const container = form || document.body;
    const fieldEls = container.querySelectorAll(
      'input,select,textarea,[contenteditable=""],[contenteditable="true"]');

    const fields = [];
    for (const el of fieldEls) {
      // Skip inputs that can't carry a user value.
      const type = (el.getAttribute('type') || '').toLowerCase();
      if (el.tagName === 'INPUT' && (type === 'hidden' || type === 'submit'
        || type === 'button' || type === 'reset' || type === 'image')) continue;

      // Detect contenteditable from the attribute (an empty or "true"
      // value both enable it) rather than el.isContentEditable, which
      // also flips true for descendants of an editable host.
      const ceAttr = el.getAttribute('contenteditable');
      const isCE = el.tagName !== 'INPUT' && el.tagName !== 'SELECT'
        && el.tagName !== 'TEXTAREA' && (ceAttr === '' || ceAttr === 'true');
      const r = el.getBoundingClientRect();
      // value reading mirrors typeText's verify path: native .value for
      // inputs, .textContent for contenteditable — same value the agent sees.
      const value = isCE ? (el.textContent || '') : (el.value || '');
      fields.push({
        tag: el.tagName.toLowerCase(),
        type: isCE ? 'contenteditable' : (el.tagName === 'INPUT' ? (el.type || 'text') : el.tagName.toLowerCase()),
        name: el.getAttribute('name') || '',
        id: el.id || '',
        placeholder: el.getAttribute('placeholder') || '',
        label: labelFor(el) || '',
        ariaLabel: el.getAttribute('aria-label') || '',
        required: !!el.required,
        disabled: !!el.disabled,
        value,
        visible: r.width > 0 && r.height > 0,
        // A framework-controlled widget: contenteditable, or a custom role
        // on a non-native field. Heuristic — surfaced so the agent can
        // decide whether URL prefill is likely to stick.
        frameworkControlled: isCE || (!!el.getAttribute('role') && el.tagName !== 'INPUT'
          && el.tagName !== 'SELECT' && el.tagName !== 'TEXTAREA'),
        selector: sel(el),
      });
    }

    return {
      form: {
        action: form ? (form.getAttribute('action') || '') : '',
        method: form ? (form.getAttribute('method') || 'get') : '',
        selector: form ? sel(form) : '',
        location: location.href,
      },
      fields,
    };
  }

  // --- Page capability probe ---
  // Best-effort diagnostics so an agent can route around a page's constraints
  // up front instead of discovering each broken tool one failed call at a
  // time. The CSP-eval check is deliberately NOT done here: content scripts
  // run in an isolated world the page CSP does not govern, so background.js
  // probes the page's MAIN world for that and merges it into the result.
  function pageCapabilities() {
    const q = (s) => { try { return document.querySelector(s); } catch { return null; } };

    let framework = 'unknown';
    if (window.__svelte || q('[class*="svelte-"]')) framework = 'svelte';
    else if (window.React || window.__REACT_DEVTOOLS_GLOBAL_HOOK__ || q('[data-reactroot]')) framework = 'react';
    else if (window.Vue || window.__VUE__ || q('[data-v-app]')) framework = 'vue';
    else if (window.ng || q('[ng-version]')) framework = 'angular';

    let shadowRoots = 0;
    for (const el of document.querySelectorAll('*')) {
      if (el.shadowRoot) shadowRoots++;
    }

    return {
      framework,
      monaco_present: !!(window.monaco || q('.monaco-editor')),
      iframes_count: document.querySelectorAll('iframe,frame').length,
      shadow_roots_count: shadowRoots,
    };
  }

  // Resolve a CSS selector preferring the first visible match when multiple
  // elements share it (e.g. two inputs with the same name= attribute — one
  // visible, one hidden in a collapsed section). "Visible" = offsetParent !== null.
  // Throws a descriptive error listing element IDs when every match is hidden,
  // so the caller knows to switch to an ID selector instead (Fix 1 + Fix 4).
  function resolveVisible(selector, doc = document) {
    const matches = Array.from(doc.querySelectorAll(selector));
    if (!matches.length) throw new Error('Element not found: ' + selector);
    const visible = matches.filter(el => el.offsetParent !== null);
    if (visible.length) {
      return { el: visible[0], allCount: matches.length, hiddenSkipped: matches.length - visible.length };
    }
    // All matches are hidden — surface their IDs so the caller can be specific
    const ids = matches.map(e => e.id ? '#' + e.id : `<${e.tagName.toLowerCase()}[name="${e.name || ''}"]>`).join(', ');
    throw new Error(
      `Selector "${selector}" matched ${matches.length} element(s) but all are hidden ` +
      `(offsetParent=null). Use an ID selector to target the visible one. Found: ${ids}`
    );
  }

  // --- Click ---
  function clickElement({ selector, x, y, frame }) {
    let el;
    let selectorNote = null;
    let off = { x: 0, y: 0 }, framePath = '';
    if (selector) {
      const ctx = frameCtx(frame); off = ctx.off; framePath = ctx.framePath;
      const { el: resolved, hiddenSkipped } = resolveVisible(selector, ctx.root);
      el = resolved;
      if (hiddenSkipped > 0) selectorNote = `Skipped ${hiddenSkipped} hidden duplicate(s) — used ${el.id ? '#' + el.id : sel(el)}`;
    } else if (x !== undefined && y !== undefined) {
      // Hit-test descends through same-origin iframes — a coordinate click no
      // longer stops at the <iframe> wrapper and lands on the real leaf element.
      const hit = deepElementFromPoint(x, y);
      el = hit.el; off = hit.offset; framePath = hit.framePath;
      if (!el) throw new Error(`No element at (${x}, ${y})`);
    } else {
      throw new Error('Provide selector or x,y coordinates');
    }

    // r/cx/cy are in the target element's OWN document — the synthetic events
    // must carry document-local clientX/clientY. Reported coordinates are
    // offset back to the top viewport so they round-trip.
    const r = el.getBoundingClientRect();
    const cx = r.x + r.width / 2;
    const cy = r.y + r.height / 2;
    const opts = { bubbles: true, cancelable: true, clientX: cx, clientY: cy, button: 0 };
    const ptrOpts = { ...opts, pointerId: 1, pointerType: 'mouse', isPrimary: true };

    // A real pointer press also fires pointer events and — critically — moves
    // focus to the control. Synthetic MouseEvents alone never focus anything,
    // so an input clicked this way looked clicked but stayed inert: it was not
    // the active element, and the type_text that followed went nowhere.
    // Dispatch the full pointer+mouse sequence and explicitly focus the nearest
    // focusable target between press and release, as a trusted click does.
    const focusable = el.closest(
      'input,textarea,select,button,a[href],[contenteditable=""],' +
      '[contenteditable="true"],[tabindex]'
    ) || el;

    el.dispatchEvent(new PointerEvent('pointerover', ptrOpts));
    el.dispatchEvent(new PointerEvent('pointerenter', { ...ptrOpts, bubbles: false }));
    el.dispatchEvent(new MouseEvent('mouseover', opts));
    el.dispatchEvent(new PointerEvent('pointerdown', ptrOpts));
    el.dispatchEvent(new MouseEvent('mousedown', opts));

    try { focusable.focus({ preventScroll: true }); } catch { /* not focusable */ }

    el.dispatchEvent(new PointerEvent('pointerup', ptrOpts));
    el.dispatchEvent(new MouseEvent('mouseup', opts));
    el.dispatchEvent(new MouseEvent('click', opts));

    const od = el.ownerDocument || document;
    const focused = od.activeElement === focusable && focusable !== od.body;
    const out = { clicked: sel(el), x: Math.round(cx + off.x), y: Math.round(cy + off.y), focused };
    if (framePath) out.frame = framePath;
    if (selectorNote) out.note = selectorNote;

    // Hint when synthetic clicks tend to misfire — most often because the
    // page uses <a href="javascript:..."> (CSP blocks the navigation) or
    // guards its handlers on `event.isTrusted`. cdp_click dispatches real
    // input through chrome.debugger, which produces trusted events.
    const href = (el.tagName === 'A' || el.tagName === 'AREA') ? el.getAttribute('href') : null;
    if (href != null && /^\s*javascript:/i.test(href)) {
      out.hint = 'Target is <a href="javascript:..."> — if this click had no effect, retry with cdp_click (real input, trusted events).';
    }
    return out;
  }

  // --- Prepare for user click (honest handoff) ---
  // Resolves the target, scrolls it into view, paints the persistent
  // handoff banner + highlight, and reports the post-scroll bbox plus
  // inViewport/occluded flags so the Go layer can put them in the result.
  // This tool deliberately does NOT click — the human does.
  async function prepareForUserClick({ selector, x, y, hint, label, reason }) {
    let el = null;
    let ambiguous = false;

    if (selector) {
      const matches = document.querySelectorAll(selector);
      if (matches.length > 1) ambiguous = true;
      el = matches[0] || null;
      if (el) {
        // The overlay only renders in the top frame; a selector inside an
        // iframe can't be resolved here. Fall through to coordinate mode.
        const probe = el.getBoundingClientRect();
        if (probe.width < 1 && probe.height < 1 && el.tagName === 'IFRAME') {
          return { found: false, reasonDetail: 'target_in_iframe' };
        }
      }
    }

    let bbox, cx, cy;
    if (el) {
      // Scroll the control to centre, then wait for the smooth scroll to
      // settle (scrollend where supported, capped by a timeout) before
      // re-reading the rect — the banner anchors to the settled position.
      el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'smooth' });
      await new Promise((resolve) => {
        let done = false;
        const finish = () => { if (!done) { done = true; resolve(); } };
        window.addEventListener('scrollend', finish, { once: true });
        setTimeout(finish, 600);
      });
      const r = el.getBoundingClientRect();
      if (r.width < 1 && r.height < 1) {
        return { found: false, reasonDetail: 'zero_size_bbox' };
      }
      bbox = { x: Math.round(r.x), y: Math.round(r.y), width: Math.round(r.width), height: Math.round(r.height) };
      cx = r.left + r.width / 2;
      cy = r.top + r.height / 2;
    } else if (typeof x === 'number' && typeof y === 'number') {
      // Coordinate mode: lower fidelity — no element to scroll, anchor, or
      // attach a click listener to. Draw a small fixed box around the point.
      cx = x; cy = y;
      bbox = { x: Math.round(x - 16), y: Math.round(y - 16), width: 32, height: 32 };
    } else {
      return { found: false, reasonDetail: 'no_target' };
    }

    // inViewport: the rect sits fully within the visible viewport.
    const inViewport = bbox.x >= 0 && bbox.y >= 0 &&
      bbox.x + bbox.width <= window.innerWidth &&
      bbox.y + bbox.height <= window.innerHeight;
    // occluded: something other than the target (or a descendant) covers
    // its centre point. Only meaningful in element mode.
    let occluded = false;
    if (el) {
      const hit = document.elementFromPoint(cx, cy);
      occluded = !!hit && hit !== el && !el.contains(hit) && !hit.contains(el);
    }

    // Await the overlay so the response resolves only once the cursor +
    // banner have landed — the background gates the follow-up screenshot
    // on this so the captured image shows the settled handoff.
    await overlay.showHandoff({
      el,
      x: cx, y: cy,
      bbox: { left: bbox.x, top: bbox.y, width: bbox.width, height: bbox.height },
      hint, label,
    });

    return {
      found: true,
      selector: selector || undefined,
      label: label || undefined,
      bbox,
      inViewport,
      occluded,
      ambiguous,
      overlayShown: window.top === window,
    };
  }

  // --- Type text ---
  function typeText({ selector, text, clear = false, pressEnter = false, frame }) {
    let el, selectorNote = null;
    const ctx = frameCtx(frame);
    if (selector) {
      const { el: resolved, hiddenSkipped } = resolveVisible(selector, ctx.root);
      el = resolved;
      if (hiddenSkipped > 0) selectorNote = `Skipped ${hiddenSkipped} hidden duplicate(s) — typed into ${el.id ? '#' + el.id : sel(el)}`;
    } else {
      el = ctx.root.activeElement;
      if (!el) throw new Error('No active element to type into');
    }

    el.focus();
    const ownerDoc = el.ownerDocument || document;
    const ownerWin = ownerDoc.defaultView || window;

    const isField = el.tagName === 'INPUT' || el.tagName === 'TEXTAREA';

    if (isField) {
      // Framework-controlled inputs (React/Vue/Svelte/Solid) own their value:
      // React installs a per-instance value setter and tracks the last value
      // it wrote, so a plain `el.value = x` is either invisible or reverted on
      // the next render. The fix every testing library uses: write through the
      // *prototype* value setter (bypassing React's instance setter so its
      // change tracker sees a delta), then dispatch a bubbling `input` event
      // so the framework's handler reads the new value. `change` follows for
      // listeners (and <select>-style logic) that key off it.
      // Use the element's OWN realm prototype — an element inside a child frame
      // belongs to that frame's global, so the parent's HTMLInputElement setter
      // would be the wrong realm.
      const proto = el.tagName === 'TEXTAREA'
        ? (ownerWin.HTMLTextAreaElement || HTMLTextAreaElement).prototype
        : (ownerWin.HTMLInputElement || HTMLInputElement).prototype;
      const nativeSetter = Object.getOwnPropertyDescriptor(proto, 'value').set;
      const next = clear ? text : (el.value + text);
      nativeSetter.call(el, next);
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
    } else {
      // contenteditable / other editable hosts have no .value — execCommand
      // keeps caret semantics and fires the native input events.
      if (clear) {
        ownerDoc.execCommand('selectAll');
        ownerDoc.execCommand('delete');
      }
      ownerDoc.execCommand('insertText', false, text);
    }

    if (pressEnter) {
      const enterOpts = { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true };
      el.dispatchEvent(new KeyboardEvent('keydown', enterOpts));
      el.dispatchEvent(new KeyboardEvent('keypress', enterOpts));
      el.dispatchEvent(new KeyboardEvent('keyup', enterOpts));
    }

    // Verify the value actually landed. type_text must not claim success when
    // a disabled field, a maxlength cap, an input-type rejection (e.g. letters
    // into type=number), or stolen focus silently dropped the text.
    let verified = true;
    if (isField) {
      verified = clear ? el.value === text : el.value.includes(text);
    }
    const out = { typed: text.length, verified, element: sel(el) };
    if (selectorNote) out.note = selectorNote;
    if (isField) {
      out.value = el.value;
      if (!verified) {
        out.hint = 'Value did not stick — the field may be disabled, capped by '
          + 'maxlength, reject this input type, or be inside an iframe. '
          + 'Check page_capabilities, or retry with cdp_type (trusted input).';
      }
    }
    return out;
  }

  // --- Fill input (React-compatible form fill) ---
  // Resolves the visible element when a selector matches multiple (Fix 1),
  // uses the native prototype value setter so React's change-tracker sees a
  // delta, dispatches input+change events, and verifies the value stuck.
  // Works for <input>, <textarea>, and <select>. Use type_text for
  // contenteditable hosts (CKEditor, Quill, etc.).
  function fillInput({ selector, value, frame }) {
    const ctx = frameCtx(frame);
    const { el, hiddenSkipped } = resolveVisible(selector, ctx.root);
    const tag = el.tagName;
    const ownerWin = (el.ownerDocument && el.ownerDocument.defaultView) || window;
    if (tag !== 'INPUT' && tag !== 'TEXTAREA' && tag !== 'SELECT') {
      throw new Error(
        `fill_input: <${tag.toLowerCase()}> is not a form field. ` +
        `Use type_text for contenteditable, or click for buttons.`
      );
    }

    el.focus();

    if (tag === 'SELECT') {
      el.value = value;
      el.dispatchEvent(new Event('change', { bubbles: true }));
      const stuck = el.value === value;
      const out = { filled: stuck, selector: sel(el), id: el.id || null, value: el.value };
      if (hiddenSkipped > 0) out.note = `Skipped ${hiddenSkipped} hidden duplicate(s) — filled ${el.id ? '#' + el.id : sel(el)}`;
      return out;
    }

    const proto = tag === 'TEXTAREA'
      ? (ownerWin.HTMLTextAreaElement || HTMLTextAreaElement).prototype
      : (ownerWin.HTMLInputElement || HTMLInputElement).prototype;
    const nativeSetter = Object.getOwnPropertyDescriptor(proto, 'value').set;
    nativeSetter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));

    const stuck = el.value === value;
    const out = { filled: stuck, selector: sel(el), id: el.id || null, value: el.value };
    if (hiddenSkipped > 0) out.note = `Skipped ${hiddenSkipped} hidden duplicate(s) — filled ${el.id ? '#' + el.id : sel(el)}`;
    if (!stuck) {
      out.hint =
        `Value did not stick — the field may be disabled, capped by maxlength, ` +
        `or reject this input type. Current value: "${el.value}".`;
    }
    return out;
  }

  // --- Scroll ---
  function scrollPage({ x, y, selector, direction, amount, frame }) {
    const ctx = frameCtx(frame);
    const fdoc = ctx.root;
    const fwin = fdoc.defaultView || window;
    if (selector) {
      const el = fdoc.querySelector(selector);
      if (!el) throw new Error('Element not found: ' + selector);
      el.scrollBy({ left: x || 0, top: y || 0, behavior: 'instant' });
    } else if (direction) {
      const dist = amount || fwin.innerHeight * 0.8;
      const map = { up: [0, -dist], down: [0, dist], left: [-dist, 0], right: [dist, 0] };
      const [dx, dy] = map[direction] || [0, 0];
      fwin.scrollBy({ left: dx, top: dy, behavior: 'instant' });
    } else {
      fwin.scrollBy({ left: x || 0, top: y || 0, behavior: 'instant' });
    }
    return { scrollX: Math.round(fwin.scrollX), scrollY: Math.round(fwin.scrollY), frame: ctx.framePath || undefined };
  }

  // --- Get HTML (with depth/length limits) ---
  function getHTML({ selector, outer = false, maxDepth = 0, maxLength = 200000, frame }) {
    const { root: fdoc, framePath } = frameCtx(frame);
    const el = selector ? fdoc.querySelector(selector) : fdoc.documentElement;
    if (!el) throw new Error('Element not found: ' + selector);

    let html;
    if (maxDepth > 0) {
      // Depth-limited HTML: truncate deeply nested content
      html = depthLimitedHTML(el, maxDepth, outer);
    } else {
      html = outer ? el.outerHTML : el.innerHTML;
    }
    const clamped = Math.min(Math.max(maxLength, 1000), 500000);
    return { html: html.substring(0, clamped), truncated: html.length > clamped, length: html.length, frame: framePath || undefined };
  }

  function depthLimitedHTML(el, maxDepth, outer = false) {
    function recurse(node, depth) {
      if (node.nodeType === Node.TEXT_NODE) {
        return node.textContent;
      }
      if (node.nodeType !== Node.ELEMENT_NODE) return '';

      const tag = node.tagName.toLowerCase();
      const attrs = [...node.attributes].map(a => ` ${a.name}="${a.value.substring(0, 100)}"`).join('');

      if (depth >= maxDepth) {
        const childText = (node.textContent || '').trim();
        if (childText) {
          const childCount = node.children.length;
          return `<${tag}${attrs}>${childText.substring(0, 200)}${childCount > 0 ? ` [${childCount} children]` : ''}</${tag}>`;
        }
        return `<${tag}${attrs} />`;
      }

      const children = [...node.childNodes].map(c => recurse(c, depth + 1)).join('');
      return `<${tag}${attrs}>${children}</${tag}>`;
    }

    if (outer) return recurse(el, 0);
    return [...el.childNodes].map(c => recurse(c, 0)).join('');
  }

  // --- YAML Structure: semantic page representation ---
  // More logical than HTML, strips noise, shows structure + content + spatial info
  function getPageStructure({ selector, maxDepth = 6, visibleOnly = true, timeLimitMs = 5000, frame }) {
    const { root: fdoc, off, framePath } = frameCtx(frame);
    const root = selector ? fdoc.querySelector(selector) : fdoc.body;
    if (!root) throw new Error('Element not found: ' + selector);

    const deadline = Date.now() + timeLimitMs;
    let timedOut = false;

    const SKIP = new Set(['SCRIPT', 'STYLE', 'NOSCRIPT', 'SVG', 'PATH', 'META', 'LINK', 'BR', 'HR']);
    const SEMANTIC = new Set(['HEADER', 'NAV', 'MAIN', 'ARTICLE', 'SECTION', 'ASIDE', 'FOOTER', 'FORM', 'DIALOG', 'TABLE']);
    const HEADING = new Set(['H1', 'H2', 'H3', 'H4', 'H5', 'H6']);
    const INLINE = new Set(['SPAN', 'STRONG', 'EM', 'B', 'I', 'A', 'CODE', 'SMALL', 'SUB', 'SUP', 'MARK', 'ABBR', 'LABEL']);

    let lines = [];
    const vp = viewport();
    lines.push(`page:`);
    lines.push(`  title: ${JSON.stringify(fdoc.title)}`);
    lines.push(`  url: ${JSON.stringify((fdoc.location && fdoc.location.href) || location.href)}`);
    if (framePath) lines.push(`  frame: ${JSON.stringify(framePath)}`);
    lines.push(`  viewport: {w: ${vp.w}, h: ${vp.h}}`);
    lines.push(`  scroll: {x: ${vp.scrollX}, y: ${vp.scrollY}}`);
    lines.push(`  content:`);

    function isVisible(el) {
      if (!visibleOnly) return true;
      const st = gcs(el);
      if (st.display === 'none' || st.visibility === 'hidden' || st.opacity === '0') return false;
      // Also require the element to overlap the current viewport — elements
      // scrolled above/below the fold have negative or oversized y values and
      // can't be clicked without scrolling first, which makes their coordinates
      // confusing. Pass visibleOnly=false to include off-screen elements.
      // Frame offset maps the frame-local rect into the top viewport.
      const r = el.getBoundingClientRect();
      return (r.bottom + off.y) > 0 && (r.top + off.y) < window.innerHeight &&
             (r.right + off.x) > 0 && (r.left + off.x) < window.innerWidth;
    }

    function directText(el) {
      let text = '';
      for (const child of el.childNodes) {
        if (child.nodeType === Node.TEXT_NODE) {
          const t = child.textContent.trim();
          if (t) text += (text ? ' ' : '') + t;
        }
      }
      return text;
    }

    function nodeType(el) {
      const tag = el.tagName;
      if (HEADING.has(tag)) return tag.toLowerCase();
      if (tag === 'P') return 'p';
      if (tag === 'A') return 'link';
      if (tag === 'IMG') return 'img';
      if (tag === 'BUTTON' || el.getAttribute('role') === 'button') return 'button';
      if (tag === 'INPUT') return 'input';
      if (tag === 'SELECT') return 'select';
      if (tag === 'TEXTAREA') return 'textarea';
      if (tag === 'TABLE') return 'table';
      if (tag === 'UL' || tag === 'OL') return 'list';
      if (tag === 'LI') return 'item';
      if (tag === 'FORM') return 'form';
      if (tag === 'VIDEO') return 'video';
      if (tag === 'AUDIO') return 'audio';
      if (tag === 'IFRAME') return 'iframe';
      if (SEMANTIC.has(tag)) return tag.toLowerCase();
      if (tag === 'DIV' || tag === 'SPAN') return null; // generic container
      return tag.toLowerCase();
    }

    function posStr(el) {
      const r = el.getBoundingClientRect();
      if (r.width < 1 && r.height < 1) return '';
      return ` @${Math.round(r.x + off.x)},${Math.round(r.y + off.y)} ${Math.round(r.width)}x${Math.round(r.height)}`;
    }

    function processNode(el, indent, depth) {
      if (timedOut) return;
      if (Date.now() > deadline) { timedOut = true; return; }
      if (depth > maxDepth) return;
      if (SKIP.has(el.tagName)) return;
      if (el.nodeType !== Node.ELEMENT_NODE) return;
      if (!isVisible(el)) return;

      const pad = '    ' + '  '.repeat(indent);
      const type = nodeType(el);
      const pos = posStr(el);
      const text = directText(el);
      const role = el.getAttribute('role');
      const ariaLabel = el.getAttribute('aria-label');

      // Determine what to output
      if (el.tagName === 'INPUT') {
        const attrs = [];
        if (el.type && el.type !== 'text') attrs.push(`type: ${el.type}`);
        if (el.name) attrs.push(`name: ${el.name}`);
        if (el.value) attrs.push(`value: ${JSON.stringify(el.value)}`);
        if (el.placeholder) attrs.push(`placeholder: ${JSON.stringify(el.placeholder)}`);
        if (el.checked) attrs.push('checked: true');
        if (el.disabled) attrs.push('disabled: true');
        lines.push(`${pad}- input: {${attrs.join(', ')}}${pos}`);
        return;
      }

      if (el.tagName === 'SELECT') {
        const opts = [...el.options].slice(0, 10).map(o => `${o.selected ? '*' : ''}${o.text}`).join(', ');
        lines.push(`${pad}- select: [${opts}]${pos}`);
        return;
      }

      if (el.tagName === 'TEXTAREA') {
        lines.push(`${pad}- textarea: ${JSON.stringify((el.value || '').substring(0, 100))}${pos}`);
        return;
      }

      if (el.tagName === 'IMG') {
        const alt = el.alt || '';
        lines.push(`${pad}- img: ${JSON.stringify(alt)}${pos}`);
        return;
      }

      if (el.tagName === 'A') {
        const href = el.getAttribute('href') || '';
        lines.push(`${pad}- link: ${JSON.stringify(text.substring(0, 100))} -> ${href.substring(0, 150)}${pos}`);
        // Don't recurse into links — text is enough
        return;
      }

      if (HEADING.has(el.tagName)) {
        lines.push(`${pad}- ${el.tagName.toLowerCase()}: ${JSON.stringify(text.substring(0, 200))}${pos}`);
        return;
      }

      if (el.tagName === 'BUTTON' || el.getAttribute('role') === 'button') {
        lines.push(`${pad}- button: ${JSON.stringify(text.substring(0, 100))}${pos}`);
        return;
      }

      // For lists
      if (el.tagName === 'UL' || el.tagName === 'OL') {
        const items = [...el.querySelectorAll(':scope > li')];
        if (items.length > 0) {
          lines.push(`${pad}- list: (${items.length} items)${pos}`);
          items.slice(0, 20).forEach(li => {
            const liText = (li.textContent || '').trim().substring(0, 150);
            lines.push(`${pad}  - ${JSON.stringify(liText)}`);
          });
          return;
        }
      }

      // For tables — extract structure
      if (el.tagName === 'TABLE') {
        const rows = el.querySelectorAll('tr');
        lines.push(`${pad}- table: (${rows.length} rows)${pos}`);
        [...rows].slice(0, 15).forEach((row, i) => {
          const cells = [...row.querySelectorAll('th, td')].map(c => c.textContent.trim().substring(0, 50));
          lines.push(`${pad}  ${i === 0 ? 'header' : `row${i}`}: [${cells.join(' | ')}]`);
        });
        return;
      }

      // Generic containers: only output if they have semantic meaning or direct text
      const hasSemanticType = type && !INLINE.has(el.tagName);
      const hasContent = text.length > 0;
      const hasChildren = el.children.length > 0;

      if (hasSemanticType || (hasContent && !INLINE.has(el.tagName))) {
        if (hasChildren && depth < maxDepth) {
          const label = type || 'group';
          const roleStr = role ? ` [role=${role}]` : '';
          const ariaStr = ariaLabel ? ` "${ariaLabel}"` : '';
          const textStr = text && !hasChildren ? `: ${JSON.stringify(text.substring(0, 200))}` : '';
          lines.push(`${pad}- ${label}${roleStr}${ariaStr}${textStr}${pos}:`);
          for (const child of el.children) {
            processNode(child, indent + 1, depth + 1);
          }
        } else if (text) {
          const label = type || 'text';
          lines.push(`${pad}- ${label}: ${JSON.stringify(text.substring(0, 300))}${pos}`);
        }
      } else {
        // Transparent wrapper — just process children
        for (const child of el.children) {
          processNode(child, indent, depth);
        }
      }
    }

    for (const child of root.children) {
      processNode(child, 0, 0);
    }

    return { yaml: lines.join('\n'), viewport: vp, timedOut };
  }

  // --- Execute arbitrary JS in page context ---
  // Note: This runs in content script isolated world. For page-context JS,
  // background.js uses chrome.scripting.executeScript with world:'MAIN'.
  // This is a fallback for simpler expressions.
  function executeJS({ code }) {
    try {
      const result = eval(code);
      return { result: JSON.parse(JSON.stringify(result ?? null)) };
    } catch (e) {
      return { error: e.message, stack: e.stack };
    }
  }

  // --- Adapt: inject and run a custom script ---
  function injectScript({ code, returnVar }) {
    return new Promise((resolve) => {
      const script = document.createElement('script');
      const callbackName = '__turbo_cb_' + Date.now();
      const wrappedCode = `
        try {
          const __result = (function() { ${code} })();
          window.postMessage({ type: '${callbackName}', result: JSON.parse(JSON.stringify(__result ?? null)) }, '*');
        } catch(e) {
          window.postMessage({ type: '${callbackName}', error: e.message }, '*');
        }
      `;

      function listener(event) {
        if (event.data?.type === callbackName) {
          window.removeEventListener('message', listener);
          script.remove();
          resolve(event.data.error ? { error: event.data.error } : { result: event.data.result });
        }
      }
      window.addEventListener('message', listener);

      script.textContent = wrappedCode;
      (document.head || document.documentElement).appendChild(script);

      // Timeout fallback
      setTimeout(() => {
        window.removeEventListener('message', listener);
        script.remove();
        resolve({ error: 'Script execution timed out (10s)' });
      }, 10000);
    });
  }

  // --- Drag-and-drop file upload (non-CDP fallback) ----------------------
  // Synthesizes a real File and drops it on a drop zone, or assigns it to an
  // <input type=file>. This never touches chrome.debugger, so it works when
  // CDP is unavailable and in Firefox.
  //
  // The file bytes are fetched here, in the content script's isolated world,
  // which is NOT subject to the page's CSP connect-src — so a strict page
  // CSP can't block the loopback fetch. The Blob is then handed to a
  // MAIN-world script (via injectScript / window.postMessage) so that the
  // File/DataTransfer/DragEvent are constructed in the page's own JS realm —
  // framework `instanceof File` checks and React's nativeEvent reads then
  // pass. See design/drag_drop_file.md §3.3.
  async function dragDropFile({ selector, fileName, mimeType, size, fileToken, fileHostPort }) {
    if (!selector) throw new Error('selector is required');

    // 1. Resolve + sanity-check the target before fetching anything.
    const target = document.querySelector(selector);
    if (!target) throw new Error('No element matches selector: ' + selector);
    const rect = target.getBoundingClientRect();
    if (rect.width < 1 && rect.height < 1) {
      throw new Error('Target element has a zero-size bounding box: ' + selector +
        ' (an invisible element is almost certainly the wrong drop target)');
    }

    // 2. Fetch the file bytes from the loopback file host. Done here in the
    //    isolated world so the page's CSP can't block it. A single-use
    //    token gates the endpoint server-side.
    const url = 'http://127.0.0.1:' + fileHostPort + '/file/' + fileToken;
    let blob;
    try {
      const resp = await fetch(url);
      if (!resp.ok) {
        throw new Error('file host returned HTTP ' + resp.status +
          (resp.status === 410 ? ' (file grant expired or already used — retry)' : ''));
      }
      blob = await resp.blob();
    } catch (e) {
      // Distinguish an unreachable host from an HTTP error.
      const m = String(e?.message || e);
      if (/HTTP \d/.test(m)) throw new Error('drag_drop_file: ' + m);
      throw new Error('drag_drop_file: could not reach the loopback file host at ' + url + ' (' + m + ')');
    }

    // 3. Hand the Blob to a MAIN-world script which builds the File +
    //    DataTransfer in the page realm and dispatches the drop. Blobs
    //    structured-clone cleanly across the postMessage realm boundary.
    return await runDropInPage(selector, blob, fileName, mimeType, size);
  }

  // __turboPerformDrop is the page-realm drop logic. It is defined here so
  // it can be unit-tested directly, but it is NEVER called from the content
  // script's isolated world at runtime — runDropInPage serializes it with
  // .toString() and injects it into the page's MAIN world, so that the
  // File/DataTransfer/DragEvent it builds belong to the page's own JS realm.
  // Framework `instanceof File` checks and React's nativeEvent reads then
  // pass. See design/drag_drop_file.md §3.3.
  //
  // It auto-detects an <input type=file> target (assigns input.files — the
  // CDP-free §3.5 path) versus a drop zone (dispatches the real browser
  // dragenter/dragover/drop choreography), and returns which path ran plus a
  // best-effort "did the widget react" observation.
  async function __turboPerformDrop(el, blob, fileName, mimeType) {
    const file = new File([blob], fileName || 'file', {
      type: mimeType || 'application/octet-stream',
      lastModified: Date.now(),
    });
    const dt = new DataTransfer();
    dt.items.add(file); // dt.files is now [file]

    // Best-effort reaction probe: watch the target subtree for any DOM
    // mutation in a short window. Uploads are often async/silent, so a
    // lack of reaction is a warning, never a hard failure.
    let reacted = false;
    const obs = new MutationObserver(() => { reacted = true; });
    obs.observe(el, { childList: true, subtree: true, attributes: true, characterData: true });

    const result = { warnings: [] };

    if (el instanceof HTMLInputElement && el.type === 'file') {
      // CDP-free path for a real <input type=file>: assign the FileList.
      // The resulting change event has isTrusted:false — see the warning.
      el.files = dt.files;
      el.dispatchEvent(new Event('input',  { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
      result.method = 'input.files';
      result.events_dispatched = ['input', 'change'];
    } else {
      // Drop-zone path: dispatch the real browser drag choreography at the
      // element's centre. dragenter -> dragover -> drop mirrors a human
      // drop; many libraries arm `drop` only after dragenter/dragover.
      const r = el.getBoundingClientRect();
      const cx = r.left + r.width / 2, cy = r.top + r.height / 2;
      const fire = (kind) => {
        const ev = new DragEvent(kind, {
          bubbles: true, cancelable: true, composed: true,
          clientX: cx, clientY: cy,
        });
        // The DragEvent ctor's dataTransfer option is not honored by every
        // engine; override the getter so the DataTransfer is always present.
        Object.defineProperty(ev, 'dataTransfer', { value: dt });
        el.dispatchEvent(ev);
        return ev;
      };
      fire('dragenter');
      // A drop zone signals it accepts the payload by preventDefault()-ing
      // dragover and drop. If neither was prevented the page rejected the
      // drop — surface that as a warning rather than reporting silent
      // success.
      const overEv = fire('dragover');
      const dropEv = fire('drop');
      result.method = 'drop';
      result.events_dispatched = ['dragenter', 'dragover', 'drop'];
      if (!overEv.defaultPrevented && !dropEv.defaultPrevented) {
        result.warnings.push('drop zone did not preventDefault() on dragover/drop — ' +
          'the page did not accept the drop; it may not be a real drop target ' +
          '(consider set_input_files if a file input exists)');
      }
    }

    // Give async handlers a beat, then report whether anything moved. A
    // non-reaction is surfaced as a warning so the agent can fall back to
    // set_input_files (CDP, trusted) — we never falsely report success.
    await new Promise((res) => setTimeout(res, 1500));
    obs.disconnect();
    if (!reacted) {
      result.warnings.push('drop zone handler did not appear to react within 1500ms — ' +
        'the page may gate on event.isTrusted; consider set_input_files (CDP, trusted)');
    }
    return result;
  }

  // runDropInPage injects a MAIN-world <script> that runs __turboPerformDrop,
  // transfers the Blob to it via window.postMessage, and resolves with the
  // script's result.
  //
  // The isolated world and the MAIN world have *separate* `window` objects,
  // so the Blob cannot be passed via a shared global — it is sent over
  // window.postMessage (structured clone, which Blobs survive cleanly), and
  // the injected script awaits its arrival before dispatching. The script is
  // injected first (it installs its Blob listener synchronously) and the
  // Blob posted after, so the message is guaranteed to be heard.
  function runDropInPage(selector, blob, fileName, mimeType, size) {
    return new Promise((resolve, reject) => {
      const cbName  = '__turbo_drop_cb_'  + Date.now() + '_' + Math.random().toString(36).slice(2, 8);
      const blobMsg = '__turbo_drop_blob_' + Date.now() + '_' + Math.random().toString(36).slice(2, 8);

      let settled = false;
      function listener(event) {
        if (event.data?.type !== cbName) return;
        window.removeEventListener('message', listener);
        if (settled) return;
        settled = true;
        if (event.data.error) { reject(new Error(event.data.error)); return; }
        const r = event.data.result || {};
        if (r.error) { reject(new Error(r.error)); return; }
        resolve({
          dropped: true,
          selector,
          method: r.method,
          file: { name: fileName, size, type: mimeType },
          events_dispatched: r.events_dispatched || [],
          warnings: r.warnings || [],
        });
      }
      window.addEventListener('message', listener);

      // Inject the MAIN-world script. It carries __turboPerformDrop verbatim
      // (via .toString()), awaits the Blob postMessage, re-queries the target
      // immediately before dispatch (so a vanished element errors cleanly),
      // and posts the result back.
      const script = document.createElement('script');
      script.textContent = `
        (async function() {
          const performDrop = ${__turboPerformDrop.toString()};
          try {
            const blob = await new Promise((res, rej) => {
              const to = setTimeout(() => {
                window.removeEventListener('message', h);
                rej(new Error('file blob was not delivered to the page'));
              }, 5000);
              function h(ev) {
                if (ev.data && ev.data.type === ${JSON.stringify(blobMsg)}) {
                  clearTimeout(to);
                  window.removeEventListener('message', h);
                  res(ev.data.blob);
                }
              }
              window.addEventListener('message', h);
            });
            const el = document.querySelector(${JSON.stringify(selector)});
            if (!el) throw new Error('target element disappeared before drop: ' + ${JSON.stringify(selector)});
            const __r = await performDrop(el, blob, ${JSON.stringify(fileName || 'file')}, ${JSON.stringify(mimeType || 'application/octet-stream')});
            window.postMessage({ type: ${JSON.stringify(cbName)}, result: __r }, '*');
          } catch (e) {
            window.postMessage({ type: ${JSON.stringify(cbName)}, error: e.message }, '*');
          }
        })();
      `;
      (document.head || document.documentElement).appendChild(script);
      script.remove();

      // Hand the Blob to the page realm (structured clone).
      window.postMessage({ type: blobMsg, blob }, '*');

      // Timeout guard: blob-wait + 1.5s reaction window + injection overhead.
      setTimeout(() => {
        if (settled) return;
        settled = true;
        window.removeEventListener('message', listener);
        reject(new Error('drag_drop_file: drop script timed out (8s)'));
      }, 8000);
    });
  }

  // --- Agent overlay -----------------------------------------------------
  // A tiny on-page UI (Shadow-DOM-isolated) that shows the human user what
  // the agent is doing in real time: a persistent badge in the top-right
  // identifying who's driving, an animated cursor that moves to click
  // targets with a sin-eased ease, a flash/highlight on the target, and a
  // toast with the agent's stated intent. None of this is visible to the
  // page itself (Shadow DOM, pointer-events: none).
  const overlay = (() => {
    let root = null;
    let hostEl = null;
    let cursor = null;
    // Toast stack: newest-first. Each entry { el, shownAt, ended,
    // position: 'bottom'|'stacked', fadeTimer, maxLifeTimer }. The
    // current action's toast sits at the bottom; a new action bumps it
    // up to the "stacked" slot and the previously-stacked one exits.
    // Each toast lives at least 15s after it appeared; if its action
    // takes longer, it stays until the result/error event arrives.
    let toastStack = [];
    const TOAST_MIN_VISIBLE_MS = 15_000;
    const TOAST_MAX_LIFE_MS = 60_000;
    const TOAST_EXIT_MS = 280;
    const TOAST_STACK_CAP = 2;
    let badge = null;
    let badgeTimer = null;
    let idleFadeTimer = null;
    let cursorPos = { x: window.innerWidth / 2, y: -40 };
    // The glyph's "active point" within its 24×24 box — the arrow's tip,
    // the centre of the eyes, the hand's fingertip. setCursorMode swaps it
    // so whichever glyph is shown still lands precisely on the target.
    let cursorHotspot = { x: 5, y: 3 };
    // After this much agent inactivity the cursor and badge fade away so
    // they don't permanently obscure the page.
    const IDLE_FADE_MS = 45_000;
    // Hard ceiling on how long the badge can stay up if the agent never
    // sends a result/error. Normally the badge fades shortly after the
    // last active task ends (see markTaskEnd).
    const BADGE_MAX_LIFETIME_MS = 60_000;
    // Grace period from "all tasks done" to badge fade-out — gives the
    // human a beat to register the final intent before it disappears.
    const BADGE_TASK_END_GRACE_MS = 1_800;

    // Set of in-flight command IDs. Populated by showStart, drained by
    // showResult/showError. Badge stays up while non-empty.
    //
    // Bounded so a long-running session with cancelled / navigation-
    // dropped tool calls (start delivered, result never arrives) can't
    // grow this without bound. The 60s ceiling timer ALSO clears the
    // set, so even a stranded id only delays the next grace-fade by at
    // most BADGE_MAX_LIFETIME_MS.
    const activeTasks = new Set();
    const MAX_ACTIVE_TASKS = 200;

    // Mouse-proximity state. Updated on document.mousemove (passive,
    // rAF-coalesced); badge + toast opacity is recomputed each frame
    // based on distance from the cursor's bounding rect.
    let mouseX = -10000, mouseY = -10000;
    let proximityRafPending = false;
    let proximityInstalled = false;
    // AbortController for the document-level mousemove listener so it
    // can be torn down on pagehide (no listener leaks across SPA route
    // changes that keep the document alive).
    let proximityAbortController = null;
    // Tracks the showError's "remove .error class" timer so back-to-back
    // errors don't strip the red shake mid-frame on the second one.
    let errorTimer = null;

    // Element anchor: a real mouse is viewport-fixed, but the agent cursor
    // represents what the agent is acting on, not where the user is
    // looking. So we glue the cursor to the target element and reposition
    // it on scroll/resize until the next action takes over or the
    // anchored element is detached. offsetX/offsetY preserve the
    // cursor's original hotspot within the element's bbox (a click on the
    // centre of a 200px button stays in the centre as the page scrolls,
    // not at the top-left).
    let anchor = null;
    let anchorRAF = 0;
    let anchorAbort = null;

    // Handoff state: a persistent banner + highlight set up by
    // prepare_for_user_click. Unlike toasts these don't auto-expire; they
    // clear on an explicit handoff_clear or the safety ceiling below.
    // handoffState = { banner, highlight, target, clickAbort, expireTimer }.
    let handoffState = null;
    // Generous ceiling — human response time is the expected variable, so
    // this is much longer than the toast's 60s. Stops a forgotten handoff
    // from leaving the page permanently overlaid.
    const HANDOFF_MAX_LIFE_MS = 10 * 60 * 1000;

    function clearAnchor() {
      if (anchorAbort) { anchorAbort.abort(); anchorAbort = null; }
      if (anchorRAF) { cancelAnimationFrame(anchorRAF); anchorRAF = 0; }
      anchor = null;
    }

    function updateAnchorPosition() {
      if (!anchor || !cursor) return;
      if (!anchor.el.isConnected) { clearAnchor(); return; }
      const r = anchor.el.getBoundingClientRect();
      const x = r.left + anchor.offsetX;
      const y = r.top + anchor.offsetY;
      cursorPos = { x, y };
      cursor.style.transform = `translate(${x - cursorHotspot.x}px, ${y - cursorHotspot.y}px)`;
    }

    function setAnchor(el, x, y) {
      clearAnchor();
      if (!el || !el.isConnected) return;
      const r = el.getBoundingClientRect();
      anchor = { el, offsetX: x - r.left, offsetY: y - r.top };
      anchorAbort = new AbortController();
      const onScroll = () => {
        if (anchorRAF) return;
        anchorRAF = requestAnimationFrame(() => {
          anchorRAF = 0;
          updateAnchorPosition();
        });
      };
      // capture: true picks up scrolls in any ancestor scroll container,
      // not just window. passive: true so we don't block scrolling.
      window.addEventListener('scroll', onScroll, { capture: true, passive: true, signal: anchorAbort.signal });
      window.addEventListener('resize', onScroll, { passive: true, signal: anchorAbort.signal });
    }

    function robotSVG(size = 14, color = '#d29922') {
      return `<svg width="${size}" height="${size}" viewBox="0 0 16 16" fill="none" stroke="${color}" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" xmlns="http://www.w3.org/2000/svg">
        <rect x="3" y="5" width="10" height="8" rx="2"/>
        <line x1="8" y1="5" x2="8" y2="3"/>
        <circle cx="8" cy="2.5" r="0.7" fill="${color}"/>
        <circle cx="6" cy="9" r="0.9" fill="${color}"/>
        <circle cx="10" cy="9" r="0.9" fill="${color}"/>
      </svg>`;
    }

    function ensure() {
      if (root) return;
      // Don't render the overlay inside iframes — only the top frame.
      if (window.top !== window) return;

      const host = document.createElement('div');
      host.id = '__turbo_overlay_host';
      // --client-hue: 40 = brand orange. showStart overwrites it per
      // action from the agent's daemon-assigned hue. Stored on the host
      // (outside the Shadow DOM) so CSS custom properties inherit
      // through the shadow boundary into every overlay child.
      host.style.cssText = 'position:fixed;top:0;left:0;width:0;height:0;z-index:2147483647;pointer-events:none;--client-hue:40;';
      (document.body || document.documentElement).appendChild(host);
      hostEl = host;

      const sr = host.attachShadow({ mode: 'closed' });

      const style = document.createElement('style');
      style.textContent = `
        :host, * { box-sizing: border-box; }
        .badge {
          position: fixed; top: 12px; right: 12px;
          display: flex; align-items: center; gap: 6px;
          padding: 5px 10px;
          background: rgba(13, 17, 23, 0.92);
          border: 1px solid #d29922;
          border-radius: 16px;
          color: #c9d1d9;
          font: 11px/1.2 'SF Mono', Menlo, monospace;
          backdrop-filter: blur(6px);
          opacity: 0;
          /* Faster transition on opacity so the proximity fade tracks
             the mouse without a noticeable lag, but still smooth enough
             for the fade-in/out edge transitions to look intentional. */
          transition: opacity 120ms ease;
          max-width: 360px;
          pointer-events: none;
        }
        /* When the badge is "on", its opacity is driven by the
           --proximity CSS variable that mouse-proximity logic updates
           in JS. Default is 0.95 (no mouse near). When the mouse is
           directly over the badge it goes near-transparent so the user
           can read the page beneath. */
        .badge.on { opacity: var(--proximity, 0.95); }
        .badge .agent-mark { color: hsl(var(--client-hue, 40), 78%, 48%); display: inline-flex; align-items: center; flex-shrink: 0; transition: color 220ms ease; }
        .badge .agent-name { color: hsl(var(--client-hue, 40), 78%, 48%); font-weight: 600; flex-shrink: 0; transition: color 220ms ease; }
        .badge .label { color: #c9d1d9; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
        .badge svg { flex-shrink: 0; }
        .cursor {
          position: fixed; top: 0; left: 0;
          width: 24px; height: 24px;
          transform: translate(-1000px, -1000px);
          will-change: transform;
          pointer-events: none;
          z-index: 10;
          opacity: 0;
          transition: opacity 200ms ease;
          filter: drop-shadow(0 4px 8px rgba(0,0,0,0.4));
        }
        .cursor.on { opacity: 1; }
        /* Three interchangeable glyphs share the one 24×24 box; the active
           mode class picks which is shown. Default = arrow pointer;
           .reading = a pair of eyes (read/scan ops); .clicking = a pointing
           hand (clicks). setCursorMode() flips the class and the hotspot. */
        .cursor .cursor-glyph { width: 24px; height: 24px; display: none; }
        .cursor .glyph-pointer { display: block; }
        .cursor.reading .glyph-pointer,
        .cursor.clicking .glyph-pointer { display: none; }
        .cursor.reading .glyph-eyes { display: block; }
        .cursor.clicking .glyph-hand { display: block; }
        /* Robot is a small "name tag" attached above-right of the pointer
           so it doesn't fight the pointer for the user's eye. The
           pointer's tip is what marks the click point; the robot just
           identifies WHO is clicking. */
        .cursor .robot {
          position: absolute;
          top: -6px; left: 14px;
          width: 14px; height: 14px;
          background: hsl(var(--client-hue, 40), 78%, 48%);
          border-radius: 50%;
          display: flex; align-items: center; justify-content: center;
          box-shadow: 0 1px 4px rgba(0,0,0,0.4);
          border: 1.5px solid #0d1117;
          transition: background 220ms ease;
        }
        .cursor .robot svg { width: 9px; height: 9px; }
        .cursor.click .cursor-glyph { animation: clickPulse 350ms ease-out; }
        @keyframes clickPulse {
          0%   { transform: scale(1); }
          40%  { transform: scale(0.78); }
          100% { transform: scale(1); }
        }
        .ripple {
          position: fixed;
          border: 3px solid var(--ripple-ring, #d29922);
          border-radius: 50%;
          pointer-events: none;
          animation: rippleAnim 600ms cubic-bezier(0.18, 0.7, 0.4, 1) forwards;
        }
        @keyframes rippleAnim {
          0%   { width: 14px;  height: 14px;  margin-left: -7px;   margin-top: -7px;  opacity: 1;   border-width: 3px; }
          100% { width: 130px; height: 130px; margin-left: -65px;  margin-top: -65px; opacity: 0;   border-width: 1px; }
        }
        .ripple-fill {
          position: fixed;
          border-radius: 50%;
          pointer-events: none;
          background: radial-gradient(circle, var(--ripple-fill, rgba(210, 153, 34, 0.55)), transparent 70%);
          animation: rippleFill 420ms ease-out forwards;
        }
        @keyframes rippleFill {
          0%   { width: 12px;  height: 12px;  margin-left: -6px;   margin-top: -6px;  opacity: 0.85; }
          100% { width: 70px;  height: 70px;  margin-left: -35px;  margin-top: -35px; opacity: 0;    }
        }
        .highlight {
          position: fixed;
          border: 2px solid var(--highlight-color, #d29922);
          border-radius: 4px;
          pointer-events: none;
          box-shadow: 0 0 18px var(--highlight-glow, rgba(210, 153, 34, 0.6));
          animation: highlightFade 1100ms ease-out forwards;
        }
        @keyframes highlightFade {
          0%   { opacity: 0.95; }
          100% { opacity: 0; }
        }
        /* Persistent handoff highlight: no one-shot fade. Instead a slow 2s
           breathing pulse so the box stays alive and eye-catching while the
           human reads the banner — a static border the eye filters out. */
        .highlight.persist {
          animation: highlightBreathe 2000ms ease-in-out infinite;
        }
        @keyframes highlightBreathe {
          0%, 100% { opacity: 0.95; box-shadow: 0 0 14px var(--highlight-glow, rgba(227, 179, 65, 0.65)); }
          50%      { opacity: 0.6;  box-shadow: 0 0 28px var(--highlight-glow, rgba(227, 179, 65, 0.65)); }
        }
        /* Handoff banner: a sticky top-centre callout carrying the agent's
           instruction. Unlike toasts it does NOT auto-fade and does NOT go
           transparent on mouse-proximity — it is an instruction the human
           must act on, not narration. Cleared only by an explicit
           handoff_clear or the safety ceiling. */
        .handoff-banner {
          position: fixed;
          top: 56px; left: 50%;
          transform: translateX(-50%) translateY(-16px);
          display: flex; align-items: flex-start; gap: 10px;
          padding: 12px 16px;
          max-width: 460px;
          background: rgba(13, 17, 23, 0.96);
          border: 1px solid hsl(var(--client-hue, 40), 78%, 52%);
          border-left-width: 4px;
          border-radius: 8px;
          color: #c9d1d9;
          font: 13px/1.4 -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
          box-shadow: 0 8px 28px rgba(0, 0, 0, 0.5), 0 0 16px hsla(var(--client-hue, 40), 78%, 52%, 0.25);
          backdrop-filter: blur(8px);
          opacity: 0;
          transition: opacity 200ms ease, transform 280ms cubic-bezier(0.2, 0.7, 0.3, 1);
          pointer-events: none;
          z-index: 20;
        }
        .handoff-banner.on { opacity: 1; transform: translateX(-50%) translateY(0); }
        .handoff-banner .hb-mark {
          flex-shrink: 0;
          width: 26px; height: 26px;
          display: flex; align-items: center; justify-content: center;
          background: hsl(var(--client-hue, 40), 78%, 48%);
          border-radius: 50%;
          border: 1.5px solid #0d1117;
        }
        .handoff-banner .hb-body { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
        .handoff-banner .hb-title {
          font-weight: 700; font-size: 13px; color: #f0f3f6;
        }
        .handoff-banner .hb-hint { color: #c9d1d9; }
        .handoff-banner .hb-hint strong { color: #f0f3f6; }
        .handoff-banner .hb-caption {
          margin-top: 4px;
          font-size: 11px; color: hsl(var(--client-hue, 40), 60%, 62%);
        }
        /* Loupe: glowing rings shown over each match during read-only
           scans (find_text, extract_text). Communicates "the agent looked
           here". Deliberately the inverse of the click ripple — the rings
           start at a large radius and contract *inward* toward the point,
           so a read reads as "focusing in", never as a tap landing. */
        .loupe {
          position: fixed;
          width: 60px; height: 60px;
          margin-left: -30px; margin-top: -30px;
          border: 2.5px solid #58a6ff;
          border-radius: 50%;
          box-shadow: 0 0 10px rgba(88, 166, 255, 0.5), inset 0 0 6px rgba(88, 166, 255, 0.35);
          pointer-events: none;
          animation: loupeFocus 640ms cubic-bezier(0.33, 0, 0.35, 1) forwards;
        }
        @keyframes loupeFocus {
          0%   { opacity: 0;    transform: scale(2.6); }
          38%  { opacity: 0.95; transform: scale(1.5); }
          100% { opacity: 0;    transform: scale(0.32); }
        }
        /* Scan-flash: a thin outline pulse drawn around every interactive
           element after get_interactive_map, so the user sees "the agent
           just enumerated everything you can click." */
        .scan-flash {
          position: fixed;
          border: 1.5px solid rgba(88, 166, 255, 0.85);
          border-radius: 3px;
          pointer-events: none;
          box-shadow: 0 0 6px rgba(88, 166, 255, 0.4);
          animation: scanFlashAnim 700ms ease-out forwards;
        }
        @keyframes scanFlashAnim {
          0%   { opacity: 0;   transform: scale(0.97); }
          25%  { opacity: 1;   transform: scale(1.02); }
          100% { opacity: 0;   transform: scale(1);    }
        }
        /* Failure shake + red tint: applied to the cursor when the tool
           returns an error. The wrapper carries the position transform
           via JS, so we shake the inner SVG instead to avoid clobbering
           the cursor's coordinates. */
        .cursor.error .cursor-glyph { animation: shake 420ms ease-out; filter: drop-shadow(0 0 4px rgba(248, 81, 73, 0.9)); }
        .cursor.error .robot { background: #f85149; box-shadow: 0 0 10px rgba(248, 81, 73, 0.7); }
        @keyframes shake {
          0%, 100% { transform: translateX(0)  scale(1); }
          15%      { transform: translateX(-5px) scale(1); }
          30%      { transform: translateX(5px)  scale(1); }
          45%      { transform: translateX(-4px) scale(1); }
          60%      { transform: translateX(4px)  scale(1); }
          80%      { transform: translateX(-2px) scale(1); }
        }
        /* Camera flash for screenshot / turbo_snapshot — quick whitish
           pulse covering the whole viewport so the user can see the
           snapshot the agent just grabbed. */
        .camera-flash {
          position: fixed;
          inset: 0;
          background: rgba(255, 255, 255, 0.4);
          pointer-events: none;
          animation: cameraFlashAnim 320ms ease-out forwards;
        }
        @keyframes cameraFlashAnim {
          0%   { opacity: 0;    }
          20%  { opacity: 1;    }
          100% { opacity: 0;    }
        }
        /* Read sweep: a thin horizontal scanline that travels top→bottom
           in ~520ms, marking "the agent is reading the DOM". Used for
           get_html, page_yaml, dom_snapshot, get_accessibility_tree,
           query_elements, get_storage, get_cookies — anything that
           doesn't have a more specific visualisation. */
        .read-sweep {
          position: fixed;
          left: 0;
          width: 100vw;
          height: 3px;
          top: 0;
          background: linear-gradient(to right,
            transparent 0%,
            rgba(88, 166, 255, 0.6) 30%,
            rgba(88, 166, 255, 0.9) 50%,
            rgba(88, 166, 255, 0.6) 70%,
            transparent 100%);
          box-shadow: 0 0 12px rgba(88, 166, 255, 0.6);
          pointer-events: none;
          animation: readSweepAnim 520ms cubic-bezier(0.4, 0, 0.6, 1) forwards;
        }
        @keyframes readSweepAnim {
          0%   { top: 0;     opacity: 0; }
          15%  { opacity: 1;             }
          85%  { opacity: 1;             }
          100% { top: 100vh; opacity: 0; }
        }
        /* Network/console/storage indicator — small icon pulse near the
           agent badge so monitoring ops register as something even if
           there's no visible page change. */
        .data-pulse {
          position: fixed;
          top: 40px; right: 20px;
          width: 24px; height: 24px;
          border: 2px solid #3fb950;
          border-radius: 50%;
          pointer-events: none;
          animation: dataPulseAnim 600ms ease-out forwards;
        }
        @keyframes dataPulseAnim {
          0%   { transform: scale(0.5); opacity: 0; }
          40%  { transform: scale(1.1); opacity: 1; }
          100% { transform: scale(0.95); opacity: 0; }
        }
        .toast {
          position: fixed;
          bottom: 24px; left: 50%;
          transform: translateX(-50%) translateY(20px);
          padding: 8px 14px;
          background: rgba(13, 17, 23, 0.92);
          border: 1px solid #21262d;
          border-radius: 6px;
          color: #c9d1d9;
          font: 12px/1.3 'SF Mono', Menlo, monospace;
          opacity: 0;
          /* Opacity is snappy so mouse-proximity fades feel responsive;
             transform/bottom are slower so the stack slide-up animation
             reads as deliberate. */
          transition: opacity 150ms ease, transform 280ms cubic-bezier(0.2, 0.7, 0.3, 1), bottom 280ms cubic-bezier(0.2, 0.7, 0.3, 1);
          max-width: 80vw;
          backdrop-filter: blur(6px);
          pointer-events: none;
          font-style: italic;
        }
        /* The --proximity CSS variable is set by the mouse-distance
           listener (1.0 when the mouse is far, ~0.15 when over the
           toast). The bottom toast uses it directly; the stacked toast
           multiplies it by the 0.7 dim. */
        .toast.on { opacity: var(--proximity, 0.95); transform: translateX(-50%) translateY(0); }
        .toast.stacked {
          bottom: 70px;
          opacity: calc(0.7 * var(--proximity, 1));
          transform: translateX(-50%) translateY(0) scale(0.94);
        }
        .toast.exiting {
          opacity: 0;
          transform: translateX(-50%) translateY(-12px) scale(0.9);
        }
        .toast .who { color: hsl(var(--client-hue, 40), 78%, 48%); font-weight: 600; font-style: normal; margin-right: 6px; transition: color 220ms ease; }
      `;

      badge = document.createElement('div');
      badge.className = 'badge';
      // The robot SVG uses `currentColor` so it inherits whatever colour
      // the surrounding context sets. We wrap it in a span whose color is
      // tied to --client-hue so the whole robot mark flips per agent.
      badge.innerHTML = `
        <span class="agent-mark">${robotSVG(14, 'currentColor')}</span>
        <span class="agent-name">Agent</span>
        <span class="label">idle</span>
      `;

      cursor = document.createElement('div');
      cursor.className = 'cursor';
      // Three glyphs, one shown at a time per the mode class (see CSS):
      // arrow pointer (default), eyes (reads), pointing hand (clicks).
      cursor.innerHTML = `
        <svg class="cursor-glyph glyph-pointer" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
          <path d="M5 3 L5 19 L9.5 15 L11.6 21 L14.4 20 L12.3 14 L18 14 Z"
                fill="#fff" stroke="#0d1117" stroke-width="1.4" stroke-linejoin="round"/>
        </svg>
        <svg class="cursor-glyph glyph-eyes" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
          <ellipse cx="8" cy="12" rx="5.4" ry="3.9" fill="#fff" stroke="#0d1117" stroke-width="1.3"/>
          <ellipse cx="16" cy="12" rx="5.4" ry="3.9" fill="#fff" stroke="#0d1117" stroke-width="1.3"/>
          <circle cx="9.4" cy="12" r="2" fill="#0d1117"/>
          <circle cx="17.4" cy="12" r="2" fill="#0d1117"/>
        </svg>
        <svg class="cursor-glyph glyph-hand" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
          <path d="M8.5 3.2 a1.5 1.5 0 0 1 3 0 v7.3 l1.1-0.3 a1.3 1.3 0 0 1 1.6 1.1
                   a1.3 1.3 0 0 1 1.7 1.0 a1.3 1.3 0 0 1 1.6 1.2 v3.6 a4 4 0 0 1-4 4
                   h-2.3 a4.2 4.2 0 0 1-3.6-2.1 l-2.4-4.2 a1.35 1.35 0 0 1 2-1.7 l1.3 1.1 z"
                fill="#fff" stroke="#0d1117" stroke-width="1.3" stroke-linejoin="round"/>
        </svg>
        <div class="robot">${robotSVG(13, '#0d1117')}</div>
      `;

      // Toasts are appended on demand (stack); no singleton DOM node.
      sr.appendChild(style);
      sr.appendChild(badge);
      sr.appendChild(cursor);

      root = sr;
    }

    // --- Agent-driven tab flag -------------------------------------------
    // While an agent is acting on this tab, prepend the brand bolt to
    // document.title so the browser tab strip shows which tabs an agent is
    // driving. The flag runs on its own grace timer, independent of the
    // badge: it clears TITLE_FLAG_GRACE_MS after the agent's *last* action,
    // long enough to stay steady across the gaps between tool calls (model
    // thinking time) so the tab title never flickers mid-run.
    const TITLE_FLAG = '⚡ ';
    const TITLE_FLAG_GRACE_MS = 15_000;
    let titleFlagActive = false;
    let titleObserver = null;
    let titleFlagTimer = null;

    // flagTitle prefixes document.title and keeps the prefix in place even
    // as a single-page app rewrites its own title. Called on every agent
    // action; re-arms the grace timer each call so the flag clears
    // TITLE_FLAG_GRACE_MS after the last one.
    function flagTitle() {
      // Re-arm on every call, including when the flag is already up.
      if (titleFlagTimer) clearTimeout(titleFlagTimer);
      titleFlagTimer = setTimeout(unflagTitle, TITLE_FLAG_GRACE_MS);
      if (titleFlagActive) return;
      titleFlagActive = true;
      if (!document.title.startsWith(TITLE_FLAG)) {
        document.title = TITLE_FLAG + document.title;
      }
      if (!titleObserver) {
        // Re-apply the flag when the page changes its own title. Our own
        // write re-fires this, but the startsWith guard makes that a
        // no-op — no loop.
        titleObserver = new MutationObserver(() => {
          if (titleFlagActive && !document.title.startsWith(TITLE_FLAG)) {
            document.title = TITLE_FLAG + document.title;
          }
        });
      }
      titleObserver.observe(document.head || document.documentElement, {
        childList: true, subtree: true, characterData: true,
      });
    }

    // unflagTitle strips the prefix and stops tracking. Safe when not set.
    function unflagTitle() {
      if (titleFlagTimer) { clearTimeout(titleFlagTimer); titleFlagTimer = null; }
      if (!titleFlagActive) return;
      titleFlagActive = false;
      if (titleObserver) titleObserver.disconnect();
      if (document.title.startsWith(TITLE_FLAG)) {
        document.title = document.title.slice(TITLE_FLAG.length);
      }
    }

    // hideBadge fades the agent badge. The tab-title flag is deliberately
    // NOT cleared here — it runs on its own longer grace timer (see
    // flagTitle) so it doesn't blink off in the ~1.8s gaps between an
    // agent's tool calls.
    function hideBadge() {
      if (badge) badge.classList.remove('on');
    }

    function setBadge({ display, intent }) {
      ensure();
      if (!badge) return;
      badge.querySelector('.agent-name').textContent = display || 'Agent';
      badge.querySelector('.label').textContent = intent || 'working…';
      badge.classList.add('on');
      flagTitle();
      installProximityListener();
      clearTimeout(badgeTimer);
      // Hard ceiling — if showResult/showError never fire (shouldn't
      // happen but be defensive), the badge fades after BADGE_MAX.
      // Normal lifecycle: stays up while activeTasks is non-empty; fades
      // BADGE_TASK_END_GRACE_MS after the last task ends.
      badgeTimer = setTimeout(() => hideBadge(), BADGE_MAX_LIFETIME_MS);
    }

    // markTaskStart / markTaskEnd track the set of in-flight commands so
    // the badge stays visible as long as the agent is actively doing
    // something. The grace period after the last task gives the human a
    // beat to read the final intent before the badge fades. Defensive
    // against out-of-order start/end delivery and stranded ids.
    function markTaskStart(id) {
      if (!id) return;
      activeTasks.add(id);
      // Evict oldest if we hit the cap — a stranded id (start delivered
      // but result lost to navigation/cancellation) can't accumulate.
      if (activeTasks.size > MAX_ACTIVE_TASKS) {
        const oldest = activeTasks.values().next().value;
        if (oldest !== undefined) activeTasks.delete(oldest);
      }
      clearTimeout(badgeTimer);
      // Hard ceiling on badge — also clear the Set when it fires so a
      // single stranded id doesn't permanently demote the grace-fade
      // behaviour ("size===0" path) for the rest of the session.
      badgeTimer = setTimeout(() => {
        hideBadge();
        activeTasks.clear();
      }, BADGE_MAX_LIFETIME_MS);
    }
    function markTaskEnd(id) {
      if (!id) return;
      // chrome.tabs.sendMessage doesn't guarantee ordering across two
      // separate calls — if `result` is delivered before `start`,
      // markTaskEnd would no-op then markTaskStart would add the id
      // permanently. Guard by remembering recently-ended ids and
      // dropping any subsequent start for them.
      activeTasks.delete(id);
      recentlyEndedTasks.add(id);
      if (recentlyEndedTasks.size > MAX_ACTIVE_TASKS) {
        const oldest = recentlyEndedTasks.values().next().value;
        if (oldest !== undefined) recentlyEndedTasks.delete(oldest);
      }
      if (activeTasks.size === 0) {
        clearTimeout(badgeTimer);
        badgeTimer = setTimeout(() => hideBadge(), BADGE_TASK_END_GRACE_MS);
      }
    }
    // Ids whose end was already observed — used to ignore late starts
    // that arrive out-of-order with their matching end.
    const recentlyEndedTasks = new Set();

    function moveCursorTo(x, y, opts = {}) {
      ensure();
      if (!cursor) return Promise.resolve();
      const start = { ...cursorPos };
      const dx = x - start.x;
      const dy = y - start.y;
      const dist = Math.hypot(dx, dy);
      // Quadratic Bezier control point: midpoint of the straight line
      // pushed perpendicular to the motion direction so the cursor
      // travels along a gentle arc instead of a straight diagonal —
      // closer to how a human flicks the mouse than a robot's
      // shortest-path teleport. The perpendicular `(dy, -dx) / dist`
      // is the unit vector to the right of motion, so left→right hops
      // bow upward, top→bottom hops bow rightward, and so on. Curve
      // depth scales with sqrt(distance), capped at 80px.
      const bow = dist > 0 ? Math.min(80, Math.sqrt(dist) * 3) : 0;
      const perpX = dist > 0 ?  dy / dist : 0;
      const perpY = dist > 0 ? -dx / dist : 0;
      const midX = start.x + dx / 2 + perpX * bow;
      const midY = start.y + dy / 2 + perpY * bow;
      // Sin-eased motion: scale duration with sqrt(distance). Ensures
      // short hops feel snappy and long traversals feel deliberate.
      // ~1.5× faster than the original (147 / 12 / 600 vs 220 / 18 / 900).
      const duration = opts.instant ? 0 : Math.min(600, 147 + Math.sqrt(dist) * 12);
      const t0 = performance.now();
      cursor.classList.add('on');

      return new Promise((resolve) => {
        function step(now) {
          const t = duration === 0 ? 1 : Math.min(1, (now - t0) / duration);
          // 0.5 - 0.5*cos(πt) is a half-cosine (sin) ease in/out: starts
          // and ends slow, full speed at the midpoint. Feels like a
          // human moving the mouse.
          const eased = 0.5 - 0.5 * Math.cos(Math.PI * t);
          // Quadratic Bezier B(u) = (1-u)²·P0 + 2(1-u)u·P1 + u²·P2.
          const u = 1 - eased;
          const cx = u * u * start.x + 2 * u * eased * midX + eased * eased * x;
          const cy = u * u * start.y + 2 * u * eased * midY + eased * eased * y;
          // Offset by the active glyph's hotspot so its meaningful point
          // (arrow tip, eye centre, fingertip) lands exactly on (x, y),
          // not the wrapper's top-left corner.
          cursor.style.transform = `translate(${cx - cursorHotspot.x}px, ${cy - cursorHotspot.y}px)`;
          if (t < 1) requestAnimationFrame(step);
          else {
            cursorPos = { x, y };
            resolve();
          }
        }
        requestAnimationFrame(step);
      });
    }

    // setCursorMode swaps the cursor glyph: '' = arrow pointer (default,
    // for type/navigate/etc.), 'reading' = eyes (find_text, query_elements,
    // DOM reads), 'clicking' = pointing hand (click/cdp_click). The hotspot
    // moves with the glyph so the active point still lands on the target.
    function setCursorMode(mode) {
      ensure();
      if (!cursor) return;
      cursor.classList.remove('reading', 'clicking');
      if (mode === 'reading') {
        cursor.classList.add('reading');
        cursorHotspot = { x: 12, y: 12 };
      } else if (mode === 'clicking') {
        cursor.classList.add('clicking');
        cursorHotspot = { x: 10, y: 2 };
      } else {
        cursorHotspot = { x: 5, y: 3 };
      }
    }

    function clickPulse() {
      if (!cursor) return;
      cursor.classList.remove('click');
      // Force a reflow so the animation restarts.
      void cursor.offsetWidth;
      cursor.classList.add('click');
    }

    // actionPalette returns the colour scheme for visualising a given
    // action: orange for click, blue for type/key, green for scroll, red
    // for error fallback. Each tool category gets a glance-distinguishable
    // ripple/highlight tint so a watching user can tell what just
    // happened without reading the popup.
    function actionPalette(action) {
      switch (action) {
        case 'type_text': case 'cdp_type': case 'cdp_key': case 'set_input_files':
        case 'fill_input': case 'drag_drop_file':
          return { ring: '#58a6ff', fill: 'rgba(88, 166, 255, 0.55)',  glow: 'rgba(88, 166, 255, 0.55)' };
        case 'scroll': case 'cdp_scroll':
          return { ring: '#3fb950', fill: 'rgba(63, 185, 80, 0.45)',   glow: 'rgba(63, 185, 80, 0.5)'   };
        case 'navigate': case 'page_reload':
          return { ring: '#a371f7', fill: 'rgba(163, 113, 247, 0.5)',  glow: 'rgba(163, 113, 247, 0.55)' };
        case '__error':
          return { ring: '#f85149', fill: 'rgba(248, 81, 73, 0.55)',   glow: 'rgba(248, 81, 73, 0.6)'   };
        case 'prepare_for_user_click':
          // Calm, attention-holding amber-gold — distinct from the orange
          // click flash and the red error. The handoff is a "look here and
          // act" beat, not a "the agent did something" beat.
          return { ring: '#e3b341', fill: 'rgba(227, 179, 65, 0.5)',    glow: 'rgba(227, 179, 65, 0.65)' };
        case '__handoff_success':
          // Green success pulse fired when the human actually clicks.
          return { ring: '#3fb950', fill: 'rgba(63, 185, 80, 0.55)',    glow: 'rgba(63, 185, 80, 0.6)'   };
        default: // click, cdp_click, anything unknown
          return { ring: '#d29922', fill: 'rgba(210, 153, 34, 0.55)',  glow: 'rgba(210, 153, 34, 0.6)'  };
      }
    }

    function flashAt(x, y, palette) {
      ensure();
      if (!root) return;
      const p = palette || actionPalette('click');
      // Stack a radial-gradient fill behind the stroked ring so the click
      // reads as a real "tap landed here" beat rather than just a fading
      // outline. Both elements are absolutely positioned at (x, y).
      const fill = document.createElement('div');
      fill.className = 'ripple-fill';
      fill.style.left = x + 'px';
      fill.style.top = y + 'px';
      fill.style.setProperty('--ripple-fill', p.fill);
      root.appendChild(fill);
      setTimeout(() => fill.remove(), 480);

      const ring = document.createElement('div');
      ring.className = 'ripple';
      ring.style.left = x + 'px';
      ring.style.top = y + 'px';
      ring.style.setProperty('--ripple-ring', p.ring);
      root.appendChild(ring);
      setTimeout(() => ring.remove(), 700);
    }

    // When persist is set, highlightRect keeps the box alive (slow breathing
    // pulse instead of a one-shot fade) and returns the node so the caller
    // can reposition/remove it — used by the handoff banner.
    function highlightRect(rect, palette, opts = {}) {
      ensure();
      if (!root || !rect) return null;
      const p = palette || actionPalette('click');
      const h = document.createElement('div');
      h.className = opts.persist ? 'highlight persist' : 'highlight';
      h.style.left = (rect.left - 2) + 'px';
      h.style.top = (rect.top - 2) + 'px';
      h.style.width = (rect.width + 4) + 'px';
      h.style.height = (rect.height + 4) + 'px';
      h.style.setProperty('--highlight-color', p.ring);
      h.style.setProperty('--highlight-glow', p.glow);
      root.appendChild(h);
      if (!opts.persist) setTimeout(() => h.remove(), 1200);
      return h;
    }

    // showHandoff renders the persistent handoff overlay: cursor lands on
    // the target, a ripple beat fires, a breathing highlight box appears,
    // and the instruction banner slides in top-centre. The banner + box are
    // glued to the target so they track page scroll/reflow. Returns once the
    // cursor has arrived so the caller can gate the follow-up screenshot.
    //
    // hint/label are agent-supplied — escaped via escapeHtml, same as toast
    // text. A new handoff replaces any active one (newest-wins, §6).
    async function showHandoff({ el, x, y, bbox, hint, label, who }) {
      ensure();
      if (!root) return;
      clearHandoff();
      // Newest-wins: hard-remove any banner/highlight still mid-exit from a
      // just-replaced handoff so two instructions never stack on one page.
      root.querySelectorAll('.handoff-banner, .highlight.persist').forEach(n => n.remove());

      await moveCursorTo(x, y);
      flashAt(x, y, actionPalette('prepare_for_user_click'));

      const highlight = bbox
        ? highlightRect(bbox, actionPalette('prepare_for_user_click'), { persist: true })
        : null;
      if (el) setAnchor(el, x, y);
      cameraFlash();

      const banner = document.createElement('div');
      banner.className = 'handoff-banner';
      const title = label || 'Your turn';
      // who identifies which agent is handing off (essential in multi-agent
      // sessions). Fall back to the live badge name set by showStart.
      const agent = who || badge?.querySelector('.agent-name')?.textContent || '';
      const caption = agent && agent !== 'Agent'
        ? `TurboWeb · ${escapeHtml(agent)} is waiting for you`
        : 'TurboWeb · the agent is waiting for you';
      banner.innerHTML = `
        <span class="hb-mark">${robotSVG(15, '#0d1117')}</span>
        <span class="hb-body">
          <span class="hb-title">${escapeHtml(title)}</span>
          <span class="hb-hint">${escapeHtml(hint || '')}</span>
          <span class="hb-caption">${caption}</span>
        </span>
      `;
      root.appendChild(banner);
      // Trigger the enter transition next frame (initial offset paints
      // first). Awaited so the handoff is fully "entered" before we return.
      await new Promise((resolve) => requestAnimationFrame(() => {
        banner.classList.add('on');
        resolve();
      }));

      const state = { banner, highlight, el, x, y, expireTimer: 0, trackAbort: null };

      // Track scroll/reflow: keep the highlight glued to the live element.
      if (el && highlight) {
        state.trackAbort = new AbortController();
        const reposition = () => {
          if (!el.isConnected) { clearHandoff(); return; }
          const r = el.getBoundingClientRect();
          highlight.style.left = (r.left - 2) + 'px';
          highlight.style.top = (r.top - 2) + 'px';
          highlight.style.width = (r.width + 4) + 'px';
          highlight.style.height = (r.height + 4) + 'px';
        };
        window.addEventListener('scroll', reposition, { capture: true, passive: true, signal: state.trackAbort.signal });
        window.addEventListener('resize', reposition, { passive: true, signal: state.trackAbort.signal });
      }

      // Opportunistic detection: a one-shot listener on the target so a
      // human click clears the handoff with a green success pulse. Pure
      // observation — the agent never depends on it.
      if (el) {
        state.clickAbort = new AbortController();
        el.addEventListener('click', () => {
          const r = el.getBoundingClientRect();
          flashAt(r.left + r.width / 2, r.top + r.height / 2, actionPalette('__handoff_success'));
          clearHandoff();
        }, { once: true, signal: state.clickAbort.signal });
      }

      // Safety ceiling so a never-acted-on handoff doesn't overlay forever.
      state.expireTimer = setTimeout(() => clearHandoff(), HANDOFF_MAX_LIFE_MS);

      handoffState = state;
    }

    // clearHandoff tears down the persistent banner + highlight and detaches
    // every listener/timer. Safe to call when no handoff is active.
    function clearHandoff() {
      const s = handoffState;
      if (!s) return;
      handoffState = null;
      if (s.expireTimer) clearTimeout(s.expireTimer);
      if (s.trackAbort) s.trackAbort.abort();
      if (s.clickAbort) s.clickAbort.abort();
      if (s.highlight) s.highlight.remove();
      if (s.banner) {
        s.banner.classList.remove('on');
        setTimeout(() => s.banner.remove(), 240);
      }
      clearAnchor();
    }

    // Loupe: a circular spotlight shown over each match in read-only
    // scans like find_text. Communicates "the agent looked here" without
    // pretending a click happened.
    function loupeAt(x, y) {
      ensure();
      if (!root) return;
      // Three concentric rings, staggered, each contracting inward toward
      // (x, y) — an inward ripple that reads as "focusing in / looking
      // here", the deliberate opposite of the outward click ripple.
      for (let i = 0; i < 3; i++) {
        const l = document.createElement('div');
        l.className = 'loupe';
        l.style.left = x + 'px';
        l.style.top = y + 'px';
        l.style.animationDelay = (i * 110) + 'ms';
        root.appendChild(l);
        setTimeout(() => l.remove(), 640 + i * 110 + 80);
      }
    }

    // scanFlash: outline-pulse a list of element bboxes briefly. Used by
    // get_interactive_map so the user sees the agent enumerated every
    // interactive element on the page in a single perceptible beat.
    function scanFlash(items) {
      ensure();
      if (!root || !items) return;
      // Cap the simultaneous flashes so we don't spawn 500+ DOM nodes
      // when the page has a huge interactive map.
      const cap = Math.min(items.length, 80);
      for (let i = 0; i < cap; i++) {
        const el = items[i];
        if (!el || !el.w || !el.h) continue;
        const f = document.createElement('div');
        f.className = 'scan-flash';
        f.style.left = el.x + 'px';
        f.style.top = el.y + 'px';
        f.style.width = el.w + 'px';
        f.style.height = el.h + 'px';
        // Stagger by index so it sweeps left-to-right rather than firing
        // every outline at the exact same frame — feels less like a flash
        // bulb, more like a radar sweep.
        f.style.animationDelay = Math.min(280, i * 6) + 'ms';
        root.appendChild(f);
        setTimeout(() => f.remove(), 1100);
      }
    }

    // cameraFlash: viewport-wide white pulse, used when the agent takes
    // a screenshot. Reads as "snap" — a beat the user can register even
    // if the page itself didn't visibly change.
    function cameraFlash() {
      ensure();
      if (!root) return;
      const f = document.createElement('div');
      f.className = 'camera-flash';
      root.appendChild(f);
      setTimeout(() => f.remove(), 360);
    }

    // readSweep: a thin scanline travelling top→bottom across the
    // viewport. Used as the generic "agent is reading the page" cue for
    // any DOM-read tool that doesn't have a more specific visualisation
    // (get_html, page_yaml, dom_snapshot, get_accessibility_tree,
    // query_elements, get_storage, get_cookies, etc.).
    function readSweep() {
      ensure();
      if (!root) return;
      const s = document.createElement('div');
      s.className = 'read-sweep';
      root.appendChild(s);
      setTimeout(() => s.remove(), 560);
    }

    // dataPulse: a small green ring near the top-right corner, used when
    // the agent peeks at non-DOM state (network log, console messages,
    // cookies, storage). Doesn't move the cursor — these ops touch
    // browser state, not the page itself, so animating across the page
    // would be misleading.
    function dataPulse() {
      ensure();
      if (!root) return;
      const p = document.createElement('div');
      p.className = 'data-pulse';
      root.appendChild(p);
      setTimeout(() => p.remove(), 650);
    }

    // scanLoupe: animate the agent cursor across a sequence of result
    // bboxes (find_text, extract_text), placing a loupe at each. Caller
    // is responsible for capping the items list to avoid 30s scans.
    async function scanLoupe(items) {
      // A prior anchor would yank the cursor back during/after the sweep
      // on the next scroll; the sweep doesn't have stable elements to
      // anchor to (loupe positions are raw viewport bboxes).
      clearAnchor();
      setCursorMode('reading');
      for (const it of items) {
        if (!it || typeof it.x !== 'number') continue;
        const cx = it.x + (it.w || 0) / 2;
        const cy = it.y + (it.h || 0) / 2;
        await moveCursorTo(cx, cy);
        loupeAt(cx, cy);
        // Short hold so the user can register what was looked at before
        // the cursor leaves for the next match.
        await new Promise((r) => setTimeout(r, 160));
      }
    }

    function showToast(text, who) {
      ensure();
      if (!root) return;

      // Promote the current bottom toast to "stacked"; evict anything
      // already stacked so we never show more than TOAST_STACK_CAP at once.
      for (const t of toastStack) {
        if (t.exiting) continue;
        if (t.position === 'bottom') {
          t.position = 'stacked';
          t.el.classList.add('stacked');
        } else if (t.position === 'stacked') {
          exitToast(t);
        }
      }

      const el = document.createElement('div');
      el.className = 'toast';
      el.innerHTML = (who ? `<span class="who">${escapeHtml(who)}</span>` : '') + escapeHtml(text);
      root.appendChild(el);
      // Trigger the enter transition next frame so the initial offset
      // gets painted first.
      requestAnimationFrame(() => el.classList.add('on'));

      const entry = {
        el,
        shownAt: Date.now(),
        ended: false,
        exiting: false,
        position: 'bottom',
        fadeTimer: 0,
        // Safety: never let a toast persist forever if its action's
        // end event is dropped or never fires.
        maxLifeTimer: setTimeout(() => exitToast(entry), TOAST_MAX_LIFE_MS),
      };
      toastStack.unshift(entry);
      // Each new toast needs its own --proximity seed so it doesn't
      // pop in at full opacity before the next mousemove tick.
      el.style.setProperty('--proximity', '0.95');

      while (toastStack.filter(t => !t.exiting).length > TOAST_STACK_CAP) {
        // Evict the deepest non-exiting toast (oldest).
        for (let i = toastStack.length - 1; i >= 0; i--) {
          if (!toastStack[i].exiting) { exitToast(toastStack[i]); break; }
        }
      }

      // Arm the proximity listener so the new toast (and the badge)
      // start fading when the user mouses over them.
      installProximityListener();
    }

    function exitToast(t) {
      if (t.exiting) return;
      t.exiting = true;
      clearTimeout(t.fadeTimer);
      clearTimeout(t.maxLifeTimer);
      t.el.classList.remove('on', 'stacked');
      t.el.classList.add('exiting');
      setTimeout(() => {
        if (t.el.parentNode) t.el.parentNode.removeChild(t.el);
        toastStack = toastStack.filter(x => x !== t);
      }, TOAST_EXIT_MS);
    }

    // Mark the oldest still-running toast as ended and schedule its
    // fade-out for whichever is later: 15s after appearance or right now.
    // Called from showResult/showError; assumes FIFO completion across
    // overlapping actions, which holds for the common single-agent case.
    function markToastEnded() {
      for (let i = toastStack.length - 1; i >= 0; i--) {
        const t = toastStack[i];
        if (t.exiting || t.ended) continue;
        t.ended = true;
        clearTimeout(t.fadeTimer);
        const wait = Math.max(0, t.shownAt + TOAST_MIN_VISIBLE_MS - Date.now());
        t.fadeTimer = setTimeout(() => exitToast(t), wait);
        return;
      }
    }

    // Mouse-proximity: when the user moves the mouse near (or over) the
    // badge or any toast in the stack, the element fades. Stays opaque
    // when the mouse is far (>150px from the nearest edge), goes
    // near-transparent (0.15) when the mouse is directly on it, and
    // ramps smoothly between. pointer-events: none means clicks already
    // pass through — this is purely about visual access.
    function installProximityListener() {
      if (proximityInstalled) return;
      proximityAbortController = new AbortController();
      const signal = proximityAbortController.signal;
      document.addEventListener('mousemove', (e) => {
        mouseX = e.clientX;
        mouseY = e.clientY;
        if (proximityRafPending) return;
        proximityRafPending = true;
        requestAnimationFrame(() => {
          proximityRafPending = false;
          updateProximityOpacity();
        });
      }, { passive: true, capture: true, signal });
      // pagehide cleanup so SPA / extension reloads don't leak handlers.
      window.addEventListener('pagehide', () => proximityAbortController?.abort(), { once: true, signal });
      proximityInstalled = true;
    }
    function updateProximityOpacity() {
      // Two-pass: read all bounding rects first, then write all CSS
      // vars. Avoids forcing a layout flush between consecutive elements.
      const targets = [];
      if (badge && badge.classList.contains('on')) {
        targets.push({ el: badge, rect: badge.getBoundingClientRect() });
      }
      for (const t of toastStack) {
        if (t.exiting) continue;
        targets.push({ el: t.el, rect: t.el.getBoundingClientRect() });
      }
      for (const { el, rect: r } of targets) {
        const dx = Math.max(0, r.left - mouseX, mouseX - r.right);
        const dy = Math.max(0, r.top - mouseY, mouseY - r.bottom);
        const dist = Math.hypot(dx, dy);
        // Piecewise: 0px → 0.15, 50px → 0.55, ≥150px → 0.95.
        let opacity;
        if (dist <= 0) opacity = 0.15;
        else if (dist < 50) opacity = 0.15 + (dist / 50) * 0.40;
        else if (dist < 150) opacity = 0.55 + ((dist - 50) / 100) * 0.40;
        else opacity = 0.95;
        el.style.setProperty('--proximity', opacity.toFixed(2));
      }
    }

    function escapeHtml(s) {
      return String(s ?? '')
        .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    }

    function resolveTarget(action, params) {
      if (!params) return null;
      if (action === 'click' || action === 'cdp_click') {
        if (params.selector) {
          const el = document.querySelector(params.selector);
          if (el) {
            const r = el.getBoundingClientRect();
            return { el, x: r.left + r.width / 2, y: r.top + r.height / 2, bbox: r };
          }
        }
        if (typeof params.x === 'number' && typeof params.y === 'number') {
          return { x: params.x, y: params.y };
        }
      }
      if ((action === 'type_text' || action === 'fill_input') && params.selector) {
        const el = document.querySelector(params.selector);
        if (el) {
          const r = el.getBoundingClientRect();
          return { el, x: r.left + Math.min(20, r.width / 4), y: r.top + r.height / 2, bbox: r, type: 'type' };
        }
      }
      if (action === 'cdp_type' && typeof document.activeElement?.getBoundingClientRect === 'function') {
        const el = document.activeElement;
        const r = el.getBoundingClientRect();
        if (r.width > 0 && r.height > 0) {
          return { el, x: r.left + 14, y: r.top + r.height / 2, bbox: r, type: 'type' };
        }
      }
      if (action === 'inspect' && params.selector) {
        const el = document.querySelector(params.selector);
        if (el) {
          const r = el.getBoundingClientRect();
          return { el, x: r.left + r.width / 2, y: r.top + r.height / 2, bbox: r, type: 'inspect' };
        }
      }
      if (action === 'drag_drop_file' && params.selector) {
        // Animate the cursor to the drop zone itself — it is the visible
        // thing the user would drag a file onto.
        const el = document.querySelector(params.selector);
        if (el) {
          const r = el.getBoundingClientRect();
          if (r.width > 0 || r.height > 0) {
            return { el, x: r.left + r.width / 2, y: r.top + r.height / 2, bbox: r, type: 'type' };
          }
        }
      }
      if (action === 'set_input_files' && params.selector) {
        // The real <input type=file> is usually hidden — useless as a
        // cursor target. Prefer the visible thing the user actually clicks:
        // the selector as given if it has a non-zero bbox, otherwise the
        // <label for=id> if one exists.
        const el = document.querySelector(params.selector);
        if (el) {
          let target = el;
          let r = el.getBoundingClientRect();
          if ((r.width < 1 || r.height < 1) && el.id) {
            const lbl = document.querySelector(`label[for="${CSS.escape(el.id)}"]`);
            if (lbl) {
              const lr = lbl.getBoundingClientRect();
              if (lr.width > 0 && lr.height > 0) { target = lbl; r = lr; }
            }
          }
          if (r.width > 0 && r.height > 0) {
            return { el: target, x: r.left + r.width / 2, y: r.top + r.height / 2, bbox: r, type: 'type' };
          }
        }
      }
      return null;
    }

    // Actions that make the agent cursor a pair of eyes — the agent is
    // reading/scanning the page rather than acting on it.
    const READING_ACTIONS = new Set([
      'find_text', 'extract_text', 'inspect', 'get_interactive_map',
      'get_html', 'page_yaml', 'get_page_structure', 'dom_snapshot',
      'get_accessibility_tree', 'query_elements', 'describe', 'turbo_snapshot',
      'list_frames',
    ]);

    // Read-only / state-only ops that don't have a specific cursor
    // animation in resolveTarget. Each value picks which visual the
    // overlay fires for that action:
    //   - 'flash'  → viewport-wide camera flash (screenshot-ish)
    //   - 'sweep'  → top-to-bottom scanline ("agent reading the page")
    //   - 'pulse'  → small corner ring ("agent reading browser state")
    const READ_ONLY_VISUALS = new Map([
      // Camera flash for snapshot-style actions
      ['screenshot',          'flash'],
      ['turbo_snapshot',      'flash'],
      // Page-DOM read → scanline sweep
      ['get_html',            'sweep'],
      ['page_yaml',           'sweep'],
      ['get_page_structure',  'sweep'],
      ['dom_snapshot',        'sweep'],
      ['get_accessibility_tree', 'sweep'],
      ['query_elements',      'sweep'],
      ['list_frames',         'sweep'],
      // Browser-state read → corner pulse
      ['get_cookies',         'pulse'],
      ['set_cookie',          'pulse'],
      ['delete_cookies',      'pulse'],
      ['get_storage',         'pulse'],
      ['network_get',         'pulse'],
      ['network_enable',      'pulse'],
      ['network_disable',     'pulse'],
      ['network_get_body',    'pulse'],
      ['network_throttle',    'pulse'],
      ['console_get',         'pulse'],
      ['console_enable',      'pulse'],
      ['console_disable',     'pulse'],
      ['console_clear',       'pulse'],
      ['get_performance',     'pulse'],
      ['css_coverage_start',  'pulse'],
      ['css_coverage_stop',   'pulse'],
      ['emulate_device',      'pulse'],
      ['page_reload',         'pulse'],
    ]);

    async function showStart({ action, intent, clientLabel, clientType, clientHue, params, id }) {
      try {
        // Drop out-of-order delivery: if this id's matching end already
        // arrived, the task isn't actually active.
        if (!id || !recentlyEndedTasks.has(id)) markTaskStart(id);
        // Apply the agent's hue to the overlay root so cursor, badge,
        // and toast all flip in lockstep. Defaults to brand orange (40°)
        // when missing — keeps the single-agent case unchanged.
        if (hostEl && typeof clientHue === 'number') {
          hostEl.style.setProperty('--client-hue', String(clientHue));
        }
        const display = clientLabel ? clientLabel.split('/').pop() : 'agent';
        setBadge({ display, intent: intent || `${action}…` });
        if (intent) showToast(intent, display);

        // Pick the cursor glyph for this action before it moves: a hand
        // for clicks, eyes for reads/scans, the arrow for everything else.
        if (action === 'click' || action === 'cdp_click') setCursorMode('clicking');
        else if (READING_ACTIONS.has(action)) setCursorMode('reading');
        else setCursorMode('');

        const target = resolveTarget(action, params);
        if (target) {
          const palette = actionPalette(action);
          await moveCursorTo(target.x, target.y);
          if (target.type === 'type' || target.type === 'inspect') {
            highlightRect(target.bbox, palette);
          } else {
            // Default = click-style flash + bbox highlight.
            clickPulse();
            flashAt(target.x, target.y, palette);
            if (target.bbox) highlightRect(target.bbox, palette);
          }
          // Glue the cursor to the element it just acted on so it tracks
          // on scroll. Bare coord targets (x/y click without a selector)
          // have no element to follow — drop any previous anchor instead.
          if (target.el) setAnchor(target.el, target.x, target.y);
          else clearAnchor();
        } else {
          // Read-only ops (no DOM target) get their own up-front visual
          // beat so the user sees the agent doing something. The
          // result-time visualisation (scan-flash, loupe sweep) is
          // additive — turbo_snapshot for instance does both.
          switch (READ_ONLY_VISUALS.get(action)) {
            case 'flash': cameraFlash(); break;
            case 'sweep': readSweep();   break;
            case 'pulse': dataPulse();   break;
          }
        }
      } catch (e) {
        // Overlay is non-critical; never throw upstream.
      } finally {
        // Reset the idle-fade clock on every overlay activity. After
        // IDLE_FADE_MS of nothing happening, the cursor + badge fade away
        // so the page isn't permanently overlaid.
        scheduleIdleFade();
      }
    }

    // showResult visualises the result of read-only tools that didn't
    // already place a cursor on the page during showStart: find_text and
    // extract_text get a moving loupe sweep, get_interactive_map gets a
    // scan-flash over every interactive element. Anything else is silent.
    async function showResult({ action, result, id }) {
      // Mark the matching toast as "action ended" so it can begin
      // its post-action fade after the 15s minimum.
      markToastEnded();
      try {
        markTaskEnd(id);
        if (!result) return;
        if (action === 'find_text' && Array.isArray(result.results)) {
          // Cap so a 50-match find doesn't take 10 seconds to play out.
          await scanLoupe(result.results.slice(0, 6));
        } else if (action === 'extract_text' && Array.isArray(result.blocks)) {
          await scanLoupe(result.blocks.slice(0, 6));
        } else if (action === 'get_interactive_map' && Array.isArray(result.elements)) {
          scanFlash(result.elements);
        } else if (action === 'turbo_snapshot' && result.interactiveMap?.elements) {
          scanFlash(result.interactiveMap.elements);
        }
      } catch (e) {
        // Visualisation is non-critical.
      } finally {
        scheduleIdleFade();
      }
    }

    // showError flashes a red ripple at the current cursor position and
    // shakes the cursor — a glance-readable "that just failed" cue
    // without forcing the user to expand the popup activity row.
    function showError({ action, error, id }) {
      try {
        markTaskEnd(id);
        ensure();
        if (cursor) {
          cursor.classList.add('on');
          cursor.classList.add('error');
          // Track the timer so back-to-back errors don't strip .error
          // mid-shake on the second one.
          clearTimeout(errorTimer);
          errorTimer = setTimeout(() => cursor && cursor.classList.remove('error'), 600);
        }
        flashAt(cursorPos.x, cursorPos.y, actionPalette('__error'));
        const display = badge?.querySelector('.agent-name')?.textContent || 'agent';
        // Mark the running intent toast as ended (it'll fade after its
        // 15s minimum, now sliding up into the stacked slot), push the
        // error message as the new bottom toast, then mark that one
        // ended too — the error is itself a terminal state.
        markToastEnded();
        showToast('✗ ' + (error || 'tool failed'), display);
        markToastEnded();
      } catch (e) {
        // Visualisation is non-critical.
      } finally {
        scheduleIdleFade();
      }
    }

    function scheduleIdleFade() {
      if (idleFadeTimer) clearTimeout(idleFadeTimer);
      idleFadeTimer = setTimeout(() => {
        if (cursor) {
          cursor.classList.remove('on');
          // Park the cursor off-screen so its next reveal feels like an
          // entrance rather than a teleport from its last spot.
          cursor.style.transform = 'translate(-1000px, -1000px)';
          cursorPos = { x: window.innerWidth / 2, y: -40 };
        }
        hideBadge();
        clearAnchor();
      }, IDLE_FADE_MS);
    }

    return { showStart, showResult, showError, showHandoff, clearHandoff, unflagTitle };
  })();

  // --- DOM-mutation signal (the cheap complement to the pixel diff) ------
  // A persistent MutationObserver answers "did the DOM change?" for free —
  // no screenshot, no image, a handful of bytes. screenshot_diff folds this
  // into its verdict; for most actions (a click that opens a menu, a type
  // that fills a field) the DOM signal alone is sufficient and the pixel
  // diff becomes a confirm-only fallback. See design/screenshot_diff.md §5.
  const domSignal = (() => {
    // Rolling counters since process start. screenshot_diff_meta diffs
    // these against a stored mark snapshot rather than resetting them, so
    // overlapping verify windows don't clobber each other.
    let total = { mutations: 0, added: 0, removed: 0, attrs: 0, text: 0 };
    // Coarse CSS-path label of the highest-impact mutated node, as a
    // "what changed" hint. Reset whenever a fresh mark is taken.
    let largestSubtree = '';
    let largestImpact = 0;

    // isOverlayNode filters out TurboWeb's own overlay subtree: the agent
    // cursor, intent toast and flash are injected into the page and would
    // otherwise inflate the counts (the same nodes the pixel diff masks).
    // closest() is a native ancestor search — cheaper than a manual walk and
    // uncapped — and matches by id so it works before the overlay's host
    // reference is wired up.
    function isOverlayNode(node) {
      if (!node) return false;
      const el = node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement;
      return !!(el && el.closest && el.closest('#__turbo_overlay_host'));
    }

    // record folds one MutationRecord into the rolling counters, skipping
    // overlay-originated records entirely.
    function record(rec) {
      if (isOverlayNode(rec.target)) return;
      total.mutations++;
      if (rec.type === 'childList') {
        // addedNodes/removedNodes can themselves be overlay nodes.
        for (const n of rec.addedNodes) {
          if (!isOverlayNode(n)) total.added++;
        }
        for (const n of rec.removedNodes) {
          if (!isOverlayNode(n)) total.removed++;
        }
        // Impact heuristic: a subtree-replacing childList mutation on a
        // large element is the most informative "what changed" label.
        const el = rec.target;
        if (el && el.nodeType === Node.ELEMENT_NODE) {
          const impact = (rec.addedNodes.length + rec.removedNodes.length);
          if (impact > largestImpact) {
            largestImpact = impact;
            try { largestSubtree = sel(el); } catch { largestSubtree = ''; }
          }
        }
      } else if (rec.type === 'attributes') {
        total.attrs++;
      } else if (rec.type === 'characterData') {
        total.text++;
      }
    }

    // Install one observer on the document root. Only the top frame runs
    // the observer — cross-origin iframes are not observable anyway, which
    // partial() reflects.
    try {
      const observer = new MutationObserver((records) => {
        for (const rec of records) record(rec);
      });
      observer.observe(document.documentElement, {
        childList: true, subtree: true, attributes: true, characterData: true,
      });
    } catch {
      // MutationObserver is universally available; if construction throws
      // (extremely locked-down environment) the signal simply stays at 0.
    }

    // snapshot returns a copy of the current counters — the value stored
    // behind a mark token.
    function snapshot() {
      return { ...total };
    }

    // partial reports whether the page has DOM the observer cannot see
    // (cross-origin iframes), so a zero count must not suppress the pixel
    // diff (§5 caveat).
    function partial() {
      try {
        for (const f of document.querySelectorAll('iframe')) {
          // Accessing contentDocument throws / returns null cross-origin.
          if (!f.contentDocument) return true;
        }
      } catch {
        return true;
      }
      return false;
    }

    // since computes the delta between now and a mark snapshot, packaging
    // it in the shape the Go domSignal struct expects.
    function since(mark) {
      const base = mark || { mutations: 0, added: 0, removed: 0, attrs: 0, text: 0 };
      return {
        mutations: total.mutations - (base.mutations || 0),
        added: total.added - (base.added || 0),
        removed: total.removed - (base.removed || 0),
        attrs: total.attrs - (base.attrs || 0),
        text: total.text - (base.text || 0),
        largest_subtree: largestSubtree,
        partial: partial(),
      };
    }

    // mark snapshots the counters and resets the impact tracker so the
    // next window's largest_subtree reflects only post-mark mutations. The
    // token IS the snapshot — encoded so it survives the JSON round trip.
    function mark() {
      largestSubtree = '';
      largestImpact = 0;
      return JSON.stringify(snapshot());
    }

    return { mark, since, snapshot };
  })();

  // domMutationsMark — content-script action: snapshot+reset the mutation
  // counter and return an opaque token the verify call diffs against.
  function domMutationsMark() {
    return { token: domSignal.mark() };
  }

  // domMutationsSince — content-script action: return the mutation counts
  // accumulated since the given mark token.
  function domMutationsSince({ since } = {}) {
    let mark = null;
    if (typeof since === 'string' && since) {
      try { mark = JSON.parse(since); } catch { mark = null; }
    }
    return domSignal.since(mark);
  }

  // screenshotDiffMeta — content-script action backing screenshot_diff's
  // single metadata round trip: resolves the agent's `ignore` selectors to
  // viewport bounding boxes, auto-adds the TurboWeb overlay box (so the
  // animated cursor/toast never contaminate the pixel diff), and bundles
  // the DOM-mutation summary since the baseline's mark.
  function screenshotDiffMeta({ ignore, since } = {}) {
    const masks = [];
    // Resolve each ignore selector to its bounding box(es).
    if (Array.isArray(ignore)) {
      for (const selector of ignore) {
        if (typeof selector !== 'string' || !selector) continue;
        let els;
        try { els = document.querySelectorAll(selector); } catch { continue; }
        for (const el of els) {
          const r = el.getBoundingClientRect();
          if (r.width > 0 && r.height > 0) {
            masks.push({ x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) });
          }
        }
      }
    }
    // Auto-mask the overlay host: TurboWeb's own cursor/toast/flash move
    // between captures and would diff as noise (§4.3). The host is a 0×0
    // fixed element, but its shadow children paint across the viewport, so
    // mask the regions the overlay can occupy: the top-right badge strip
    // and a generous cursor margin. Cheapest correct option is to mask the
    // overlay-occupied corner; the agent rarely cares about pixels there.
    const host = document.getElementById('__turbo_overlay_host');
    if (host) {
      // Badge sits at top:12 right:12 — mask a conservative top strip.
      masks.push({ x: 0, y: 0, w: window.innerWidth, h: 48 });
    }
    // Also auto-mask <video> and <canvas>: they differ frame-to-frame and
    // produce no DOM mutations (§7), so excluding them avoids false
    // positives. The agent can still opt in by not relying on this tool.
    for (const el of document.querySelectorAll('video, canvas')) {
      const r = el.getBoundingClientRect();
      if (r.width > 0 && r.height > 0) {
        masks.push({ x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) });
      }
    }

    let mark = null;
    if (typeof since === 'string' && since) {
      try { mark = JSON.parse(since); } catch { mark = null; }
    }
    return {
      masks,
      dom: domSignal.since(mark),
      viewport: { w: window.innerWidth, h: window.innerHeight, scrollY: Math.round(window.scrollY) },
      url: location.href,
    };
  }

  // --- Message router ---
  chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
    if (msg.action === 'ping') {
      sendResponse({ ok: true });
      return;
    }

    if (msg.action === '__turbo_overlay') {
      const payload = msg.payload || {};
      if (payload.kind === 'start') {
        // Resolve sendResponse only after the cursor has actually arrived
        // at the target. The background waits on this for click/type
        // actions so the visible click happens AT the cursor, not while
        // the cursor is still in flight. showStart kicks off the ripple
        // synchronously after arrival, so the ripple coincides with the
        // real DOM click that follows.
        overlay.showStart(payload).then(() => sendResponse({ ok: true }));
        return true; // async response
      }
      if (payload.kind === 'result') {
        // Visualise the result of read-only tools (loupe sweep / scan
        // flash). Fire-and-forget; the response can be sync.
        overlay.showResult(payload);
        sendResponse({ ok: true });
        return;
      }
      if (payload.kind === 'error') {
        overlay.showError(payload);
        sendResponse({ ok: true });
        return;
      }
      if (payload.kind === 'handoff') {
        // Persistent handoff: resolve only once the cursor + banner have
        // landed so the background can gate the follow-up screenshot.
        overlay.showHandoff(payload).then(() => sendResponse({ ok: true }));
        return true; // async response
      }
      if (payload.kind === 'handoff_clear') {
        overlay.clearHandoff();
        sendResponse({ ok: true });
        return;
      }
      sendResponse({ ok: true });
      return;
    }

    const handlers = {
      extract_text: (p) => extractText(p),
      find_text: (p) => findText(p),
      inspect: (p) => inspectElement(p),
      get_interactive_map: (p) => getInteractiveMap(p),
      query_elements: (p) => queryElements(p),
      list_frames: () => listFrames(),
      inspect_form: (p) => inspectForm(p),
      page_capabilities: () => pageCapabilities(),
      click: (p) => clickElement(p),
      prepare_for_user_click: (p) => prepareForUserClick(p),
      type_text: (p) => typeText(p),
      fill_input: (p) => fillInput(p),
      scroll: (p) => scrollPage(p),
      get_html: (p) => getHTML(p),
      get_page_structure: (p) => getPageStructure(p),
      execute_js_isolated: (p) => executeJS(p),
      inject_script: (p) => injectScript(p),
      drag_drop_file: (p) => dragDropFile(p),
      dom_mutations_mark: () => domMutationsMark(),
      dom_mutations_since: (p) => domMutationsSince(p),
      screenshot_diff_meta: (p) => screenshotDiffMeta(p),
    };

    const handler = handlers[msg.action];
    if (!handler) {
      sendResponse({ error: 'Unknown action: ' + msg.action });
      return;
    }

    try {
      const result = handler(msg.params || {});
      if (result instanceof Promise) {
        result.then(r => sendResponse(r)).catch(e => sendResponse({ error: e.message }));
        return true; // async
      }
      sendResponse(result);
    } catch (e) {
      sendResponse({ error: e.message });
    }
  });
})();
